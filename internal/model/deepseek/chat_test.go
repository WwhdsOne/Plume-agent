package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"plume-agent/internal/model"
)

// 守住 DeepSeek 适配器的组合契约：请求必须命中 /chat/completions，
// provider、protocol 标识与响应正文不因组合而变形。
func TestDeepseekAdapterComposesCompatibleImplementation(t *testing.T) {
	// DeepSeek 适配器组合兼容实现；本测试锁定 provider 标识与基本转发，
	// 供应商差异在后续单元验证后落在本包。
	const body = `{"id":"cmpl-9","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"deepseek ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	adapter, err := NewAdapter(srv.URL, "test-key")
	if err != nil {
		t.Fatalf("NewAdapter error = %v", err)
	}
	resp, err := adapter.Generate(context.Background(), model.ChatRequest{
		Model:    "deepseek-flash",
		Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate error = %v", err)
	}
	if resp.Provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek", resp.Provider)
	}
	if resp.Protocol != model.ProtocolOpenAIChatCompletions {
		t.Errorf("protocol = %q, want openai-chat-completions", resp.Protocol)
	}
	if resp.Message.Content != "deepseek ok" {
		t.Errorf("content = %q", resp.Message.Content)
	}
}

// 守住 Stream 的应答校验：服务端不以 text/event-stream 应答（此处的空
// 应答）时必须报错，不得静默给出空流。
func TestDeepseekStreamUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	adapter, err := NewAdapter(srv.URL, "")
	if err != nil {
		t.Fatalf("NewAdapter error = %v", err)
	}
	if _, err := adapter.Stream(context.Background(), model.ChatRequest{}); err == nil {
		t.Fatal("Stream should return unsupported until G1b.3")
	}
}

// 守住 DeepSeek 思考参数的编码契约：thinking.type 随档位启停、effort 档位
// 映射（medium 归一为 high，none 不发 reasoning_effort）、无工具时历史思考
// 不回传、响应 reasoning_content 解析进 Message.Reasoning。
func TestDeepseekReasoningEncodingAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		effort        model.ReasoningEffort
		enabled, want string
	}{
		{model.ReasoningHigh, "enabled", "high"}, {model.ReasoningMedium, "enabled", "high"}, {model.ReasoningLow, "enabled", "low"}, {model.ReasoningMax, "enabled", "max"}, {model.ReasoningNone, "disabled", ""},
	} {
		t.Run(string(tc.effort), func(t *testing.T) {
			var request map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&request)
				w.Header().Set("Content-Type", "application/json")
				reasoning := "plan"
				if tc.effort == model.ReasoningNone {
					reasoning = ""
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "answer", "reasoning_content": reasoning}}}})
			}))
			defer srv.Close()
			a, _ := NewAdapter(srv.URL, "")
			resp, err := a.Generate(context.Background(), model.ChatRequest{Model: "m", ReasoningEffort: tc.effort, Messages: []model.Message{{Role: model.RoleAssistant, Content: "prior", Reasoning: "private"}, {Role: model.RoleUser, Content: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			thinking, _ := request["thinking"].(map[string]any)
			if thinking["type"] != tc.enabled || request["reasoning_effort"] != func() any {
				if tc.want == "" {
					return nil
				}
				return tc.want
			}() {
				t.Fatalf("request=%v", request)
			}
			first := request["messages"].([]any)[0].(map[string]any)
			if _, ok := first["reasoning_content"]; ok {
				t.Fatal("history reasoning must not be sent without tools")
			}
			if tc.effort != model.ReasoningNone && resp.Message.Reasoning != "plan" {
				t.Fatalf("reasoning=%q", resp.Message.Reasoning)
			}
		})
	}
}

// 守住关闭思考的强校验：ReasoningNone 下服务端仍返回 reasoning_content
// 必须报 unsupported，不得静默丢弃。
func TestDeepseekNoneRejectsUnexpectedReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"finish_reason":"stop","message":{"content":"answer","reasoning_content":"unexpected"}}]}`)
	}))
	defer srv.Close()
	a, _ := NewAdapter(srv.URL, "")
	_, err := a.Generate(context.Background(), model.ChatRequest{Model: "m", ReasoningEffort: model.ReasoningNone, Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	var e *model.Error
	if !errors.As(err, &e) || e.Code != model.ErrUnsupported {
		t.Fatalf("err=%v", err)
	}
}
