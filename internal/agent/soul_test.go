package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
	"plume-agent/internal/tools"
)

func writeSoulForTest(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSoulRunSnapshotAndNextRunReload 守住人格的 run 级快照：每 run 只读一次（工具中途改写不影响当前 run），
// system 拼装恰好一份且占位符不递归展开、正文不进提交历史，下一 run 才重新加载新内容。
func TestSoulRunSnapshotAndNextRunReload(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "generate", true: "stream"}[streaming], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "soul.md")
			original, edited := "用简洁的中文，保留 {runtime} 字面量。", "先讲结论，再解释细节。"
			writeSoulForTest(t, path, original)
			toolResponse := model.FakeScript{Response: &model.ChatResponse{
				Message:      model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "clock", Name: "current_time", Arguments: `{}`}}},
				FinishReason: model.FinishToolCalls,
			}}
			fake := model.NewFake(toolResponse, finalScript("done"), finalScript("next"))
			r := New(fake, "m")
			r.SetSoul(path, 64<<10)
			r.SetTools(tools.Builtins(func() time.Time {
				writeSoulForTest(t, path, edited)
				return time.Time{}
			}))
			run := func(history []model.Message) (*RunResult, error) {
				if streaming {
					return r.RunStream(context.Background(), history, "task", nil, nil)
				}
				return r.Run(context.Background(), history, "task")
			}
			first, err := run(nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := fake.Calls()
			if len(calls) != 2 || calls[0].Messages[0].Content != calls[1].Messages[0].Content {
				t.Fatal("personality changed inside one tool loop")
			}
			text := calls[0].Messages[0].Content
			if !strings.HasPrefix(text, baseRules) || strings.Count(text, original) != 1 || strings.Contains(text, edited) || strings.Contains(text, "{soul.md}") {
				t.Fatal("personality is missing, duplicated, reordered, or recursively expanded")
			}
			if len(calls[0].Tools) != 2 {
				t.Fatal("structured tool declarations lost")
			}
			for _, msg := range first.Messages {
				if msg.Role == model.RoleSystem || strings.Contains(msg.Content, original) {
					t.Fatal("personality leaked into committed turn")
				}
			}
			if _, err := run(first.Messages); err != nil {
				t.Fatal(err)
			}
			next := fake.Calls()[2].Messages[0].Content
			if strings.Count(next, edited) != 1 || strings.Contains(next, original) {
				t.Fatal("next run did not reload edited personality")
			}
		})
	}
}

// TestSoulMissingEmptyAndDisabled 守住人格缺失时的兜底：文件缺失/空白/禁用都不追加标题或空白，
// system 消息保持纯基础规则；禁用后连非法文件也不读取。
func TestSoulMissingEmptyAndDisabled(t *testing.T) {
	for _, state := range []string{"missing", "empty", "whitespace", "disabled"} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "soul.md")
			switch state {
			case "empty":
				writeSoulForTest(t, path, "")
			case "whitespace":
				writeSoulForTest(t, path, " \n\t ")
			case "disabled":
				// 即使文件无效，关闭后也不能读取它。
				writeSoulForTest(t, path, string([]byte{0xff}))
				path = ""
			}
			fake := model.NewFake(finalScript("done"))
			r := New(fake, "m")
			r.SetSoul(path, 64<<10)
			if _, err := r.Run(context.Background(), nil, "task"); err != nil {
				t.Fatal(err)
			}
			if fake.Calls()[0].Messages[0].Content != baseRules {
				t.Fatal("absent personality should add no heading or whitespace")
			}
		})
	}
}

// TestSoulErrorsStopBeforeModel 守住"人格读取失败在发送前失败"：非法 UTF-8、路径是目录、超源限制、
// 超请求预算、已取消，都不联系模型、不提交历史，错误正确分类且不泄漏人格正文。
func TestSoulErrorsStopBeforeModel(t *testing.T) {
	for _, state := range []string{"invalid UTF8", "directory", "source limit", "request budget", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "soul.md")
			writeSoulForTest(t, path, "private personality phrase")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := model.NewFake(finalScript("must not call"))
			r := New(fake, "m")
			maxBytes := 64 << 10
			want := model.ErrInvalidConfig
			switch state {
			case "invalid UTF8":
				writeSoulForTest(t, path, string([]byte{0xff}))
			case "directory":
				path = filepath.Dir(path)
			case "source limit":
				maxBytes = 4
			case "request budget":
				limits := DefaultLimits()
				limits.RequestBytes = 4
				r.SetLimits(limits)
				want = model.ErrBudgetExceeded
			case "cancelled":
				cancel()
			}
			r.SetSoul(path, maxBytes)
			result, err := r.RunStream(ctx, nil, "task", nil, nil)
			if err == nil || len(fake.Calls()) != 0 || result.ModelCalls != 0 || len(result.Messages) != 0 {
				t.Fatalf("source failure contacted model or committed history: result=%+v err=%v", result, err)
			}
			if state == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			} else if m, ok := errors.AsType[*model.Error](err); !ok || m.Code != want {
				t.Fatalf("error classification: %v, want %s", err, want)
			}
			if strings.Contains(err.Error(), "private personality phrase") {
				t.Fatal("source error leaked personality body")
			}
		})
	}
}

// TestSoulGenerateStreamRequestsMatchAndTraceIsRedacted 守住 Generate 与 Stream 的人格请求逐字段一致，
// 且 trace 只记 prompt 版本、不含人格正文与文件路径。
func TestSoulGenerateStreamRequestsMatchAndTraceIsRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	private := "private-soul-marker-请以简洁中文回答"
	writeSoulForTest(t, path, private)
	var calls [][]model.ChatRequest
	for _, streaming := range []bool{false, true} {
		fake := model.NewFake(finalScript("done"))
		r := New(fake, "m")
		r.SetSoul(path, 64<<10)
		var trace bytes.Buffer
		r.SetRecorder(telemetry.NewModelRecorder(&trace), "fixture", telemetry.ReasoningInfo{})
		var err error
		if streaming {
			_, err = r.RunStream(context.Background(), nil, "task", nil, nil)
		} else {
			_, err = r.Run(context.Background(), nil, "task")
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(trace.String(), PromptVersion) || strings.Contains(trace.String(), private) || strings.Contains(trace.String(), path) {
			t.Fatal("trace must record version without personality body or file path")
		}
		calls = append(calls, fake.Calls())
	}
	if !reflect.DeepEqual(calls[0], calls[1]) {
		t.Fatal("Generate/Stream personality requests differ")
	}
}
