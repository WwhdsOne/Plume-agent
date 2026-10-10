// Package eval 承载评测：固定种子数据集、运行器与后续的统计/比较。
// G1b.1 只包含 12 个离线工程回归种子（eval/datasets/smoke.v1.jsonl），
// 全部经 httptest 驱动，不产生真实付费调用。
package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/model/openai"
	"plume-agent/internal/telemetry"
)

// smokeCase 对应数据集一行的结构。
type smokeCase struct {
	CaseID      string            `json:"case_id"`
	Category    string            `json:"category"`
	Description string            `json:"description"`
	Behavior    string            `json:"behavior"`
	Request     model.ChatRequest `json:"request"`
	Expect      smokeExpect       `json:"expect"`
}

type smokeExpect struct {
	FinishReason    string `json:"finish_reason,omitempty"`
	ContentContains string `json:"content_contains,omitempty"`
	UsageKnown      *bool  `json:"usage_known,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	TraceTerminal   string `json:"trace_terminal,omitempty"`
	NoSecret        bool   `json:"no_secret,omitempty"`
}

const (
	// fakeKey 只用于验证 trace 脱敏，与真实凭据无关。
	fakeKey = "sk-smoke-secret-key-never-leak"
)

func loadDataset(t *testing.T) []smokeCase {
	t.Helper()
	path := filepath.Join("..", "..", "eval", "datasets", "smoke.v1.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dataset: %v", err)
	}
	var cases []smokeCase
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var c smokeCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("dataset line %d is not valid JSON: %v", i+1, err)
		}
		cases = append(cases, c)
	}
	return cases
}

// 守住 smoke.v1 数据集形状：固定 12 例、case_id 唯一非空、类别配比与 behavior 枚举不被意外改动。
func TestDatasetShape(t *testing.T) {
	cases := loadDataset(t)
	if len(cases) != 12 {
		t.Fatalf("dataset has %d cases, want 12", len(cases))
	}
	wantCounts := map[string]int{"normal": 4, "invalid_input": 2, "model_error": 2, "timeout_cancel": 2, "trace": 2}
	gotCounts := map[string]int{}
	seen := map[string]bool{}
	behaviors := map[string]bool{"ok": true, "none": true, "http_401": true, "http_429": true, "http_500": true, "timeout": true, "cancel": true}
	for _, c := range cases {
		if c.CaseID == "" || seen[c.CaseID] {
			t.Errorf("case id %q empty or duplicated", c.CaseID)
		}
		seen[c.CaseID] = true
		gotCounts[c.Category]++
		if !behaviors[c.Behavior] {
			t.Errorf("%s: unknown behavior %q", c.CaseID, c.Behavior)
		}
	}
	for cat, want := range wantCounts {
		if gotCounts[cat] != want {
			t.Errorf("category %s has %d cases, want %d", cat, gotCounts[cat], want)
		}
	}
}

// newSmokeServer 按 behavior 构造httptest 服务器。
func newSmokeServer(t *testing.T, behavior string) (*httptest.Server, *serverState) {
	t.Helper()
	state := &serverState{}
	var handler http.HandlerFunc
	switch behavior {
	case "ok":
		handler = func(w http.ResponseWriter, r *http.Request) {
			state.requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Id", "req-smoke-ok")
			_, _ = w.Write([]byte(`{"id":"cmpl-smoke","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"pong from smoke fixture"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		}
	case "http_401", "http_429", "http_500":
		status := map[string]int{"http_401": 401, "http_429": 429, "http_500": 500}[behavior]
		handler = func(w http.ResponseWriter, r *http.Request) {
			state.requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Id", "req-smoke-err")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"simulated failure","type":"server_error"}}`))
		}
	case "timeout":
		handler = func(w http.ResponseWriter, r *http.Request) {
			state.requests.Add(1)
			time.Sleep(2 * time.Second)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"late"}`))
		}
	case "cancel":
		started := make(chan struct{})
		handler = func(w http.ResponseWriter, r *http.Request) {
			state.requests.Add(1)
			state.startedOnce.Do(func() { close(started) })
			// client 侧取消后 SDK 不保证立即关闭底层连接；release 让
			// 测试结束时 handler 一定退出，不依赖服务端感知断开。
			select {
			case <-r.Context().Done():
			case <-state.release:
			}
		}
		state.started = started
	case "none":
		handler = func(w http.ResponseWriter, r *http.Request) {
			state.requests.Add(1)
			t.Errorf("behavior none must not send HTTP requests")
		}
	default:
		t.Fatalf("unsupported behavior %q", behavior)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// 注册顺序晚于 srv.Close：cleanup 按 LIFO 执行，先关闭 release 让
	// cancel 场景的 handler 退出，srv.Close 才能等到连接结束。
	state.release = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-state.release:
		default:
			close(state.release)
		}
	})
	return srv, state
}

type serverState struct {
	requests    atomic.Int64
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
}

// 逐例回归：适配器的错误码/finish_reason/usage、trace 恰一对起止且终态唯一、密钥不进 trace，全部按数据集断言。
func TestRunSmokeCases(t *testing.T) {
	cases := loadDataset(t)
	for _, c := range cases {
		t.Run(c.CaseID, func(t *testing.T) {
			ctx := context.Background()
			trace := &bytes.Buffer{}
			rec := telemetry.NewModelRecorder(trace)
			t.Cleanup(func() { _ = rec.Close() })

			var client model.Client
			if c.Behavior == "none" {
				// 本地校验路径：指向不可达回环地址，若实现错误会得到 transport 类错误。
				adapter, err := openai.NewAdapter("custom-openai", "http://127.0.0.1:1", fakeKey)
				if err != nil {
					t.Fatalf("adapter: %v", err)
				}
				client = adapter
			} else {
				srv, state := newSmokeServer(t, c.Behavior)
				adapter, err := openai.NewAdapter("custom-openai", srv.URL, fakeKey)
				if err != nil {
					t.Fatalf("adapter: %v", err)
				}
				client = adapter
				switch c.Behavior {
				case "timeout":
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
					defer cancel()
				case "cancel":
					// 服务器收到请求后取消，模拟用户中断。
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					go func() {
						<-state.started
						cancel()
					}()
				}
			}

			callID := c.CaseID
			span := rec.StartModel(callID, "custom-openai", model.ProtocolOpenAIChatCompletions, c.Request.Model)
			resp, err := client.Generate(ctx, c.Request)
			span.End(resp, err)

			if err != nil {
				var mErr *model.Error
				if !errors.As(err, &mErr) {
					t.Fatalf("error = %v (%T), want *model.Error", err, err)
				}
				if c.Expect.ErrorCode == "" {
					t.Fatalf("unexpected error: %v", err)
				}
				if string(mErr.Code) != c.Expect.ErrorCode {
					t.Fatalf("error_code = %q, want %q", mErr.Code, c.Expect.ErrorCode)
				}
			} else if c.Expect.ErrorCode != "" {
				t.Fatalf("expected error %q, got success", c.Expect.ErrorCode)
			}

			if resp != nil {
				if c.Expect.FinishReason != "" && string(resp.FinishReason) != c.Expect.FinishReason {
					t.Errorf("finish_reason = %q, want %q", resp.FinishReason, c.Expect.FinishReason)
				}
				if c.Expect.ContentContains != "" && !strings.Contains(resp.Message.Content, c.Expect.ContentContains) {
					t.Errorf("content = %q, want contains %q", resp.Message.Content, c.Expect.ContentContains)
				}
				if c.Expect.UsageKnown != nil && resp.Usage.OK != *c.Expect.UsageKnown {
					t.Errorf("usage_known = %v, want %v", resp.Usage.OK, *c.Expect.UsageKnown)
				}
			}

			if c.Expect.TraceTerminal != "" {
				assertTraceTerminal(t, trace, callID, c.Expect.TraceTerminal)
			}
			if c.Expect.NoSecret {
				if strings.Contains(trace.String(), fakeKey) {
					t.Errorf("trace leaked the key: %s", trace.String())
				}
			}
		})
	}
}

// assertTraceTerminal 断言指定 call 的 trace 包含 start 与唯一指定终态。
func assertTraceTerminal(t *testing.T, trace *bytes.Buffer, callID, wantTerminal string) {
	t.Helper()
	events := []map[string]any{}
	for line := range strings.SplitSeq(strings.TrimSpace(trace.String()), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("trace line is not JSON: %v (%s)", err, line)
		}
		events = append(events, event)
	}
	starts, terminals := 0, 0
	for _, event := range events {
		if event["model_call_id"] != callID {
			continue
		}
		switch event["msg"] {
		case telemetry.EventModelStart:
			starts++
		case telemetry.EventModelCompleted, telemetry.EventModelFailed:
			terminals++
			if event["msg"] != wantTerminal {
				t.Errorf("terminal event = %v, want %s", event["msg"], wantTerminal)
			}
		default:
			t.Errorf("unexpected trace event: %v", event["msg"])
		}
	}
	if starts != 1 || terminals != 1 {
		t.Errorf("trace for %s: starts=%d terminals=%d, want 1/1", callID, starts, terminals)
	}
}
