package telemetry

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"plume-agent/internal/model"
)

// parseEvents 把 JSON Lines 解析成 map 切片，msg 字段即事件名。
func parseEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("trace line is not JSON: %v (%s)", err, line)
		}
		events = append(events, event)
	}
	return events
}

func TestModelSpanSuccessTerminal(t *testing.T) {
	buf := &bytes.Buffer{}
	rec := NewModelRecorder(buf)

	span := rec.StartModel("call-1", "deepseek", model.ProtocolOpenAIChatCompletions, "deepseek-flash")
	span.End(&model.ChatResponse{
		FinishReason: model.FinishStop,
		Usage:        model.Usage{OK: true, PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}, nil)

	events := parseEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want start + one terminal", len(events))
	}
	start, done := events[0], events[1]
	if start["msg"] != EventModelStart {
		t.Errorf("first event = %v, want %s", start["msg"], EventModelStart)
	}
	for _, field := range []string{"model_call_id", "provider", "protocol", "model"} {
		if start[field] == "" {
			t.Errorf("start event missing %q", field)
		}
	}
	if done["msg"] != EventModelCompleted {
		t.Errorf("terminal event = %v, want %s", done["msg"], EventModelCompleted)
	}
	if done["status"] != "ok" || done["finish_reason"] != "stop" {
		t.Errorf("terminal fields = %v, want ok/stop", done)
	}
	if done["usage_known"] != true || done["total_tokens"] != float64(5) {
		t.Errorf("usage fields = %v, want usage_known=true total=5", done)
	}
}

func TestModelSpanFailureClassification(t *testing.T) {
	buf := &bytes.Buffer{}
	rec := NewModelRecorder(buf)

	span := rec.StartModel("call-2", "custom", model.ProtocolOpenAIChatCompletions, "m")
	span.End(nil, model.NewError(model.ErrAuthentication).WithStatus(401).WithRequestID("req-9"))

	events := parseEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want start + failed", len(events))
	}
	failed := events[1]
	if failed["msg"] != EventModelFailed {
		t.Fatalf("terminal event = %v, want %s", failed["msg"], EventModelFailed)
	}
	if failed["error_code"] != "authentication" || failed["status_code"] != float64(401) || failed["request_id"] != "req-9" {
		t.Errorf("failure fields = %v", failed)
	}
}

func TestModelSpanUsageUnknownOmitsTokenCounts(t *testing.T) {
	buf := &bytes.Buffer{}
	rec := NewModelRecorder(buf)

	span := rec.StartModel("call-3", "custom", model.ProtocolOpenAIChatCompletions, "m")
	span.End(&model.ChatResponse{FinishReason: model.FinishStop, Usage: model.Usage{OK: false}}, nil)

	events := parseEvents(t, buf)
	failed := events[1]
	if failed["usage_known"] != false {
		t.Errorf("usage_known = %v, want false", failed["usage_known"])
	}
	for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if _, present := failed[field]; present {
			t.Errorf("unknown usage must not emit %q (unknown is not 0)", field)
		}
	}
}

func TestModelSpanEndIsIdempotent(t *testing.T) {
	buf := &bytes.Buffer{}
	rec := NewModelRecorder(buf)

	span := rec.StartModel("call-4", "custom", model.ProtocolOpenAIChatCompletions, "m")
	span.End(nil, model.NewError(model.ErrTimeout))
	span.End(nil, model.NewError(model.ErrCancelled)) // 重复终态必须被忽略

	events := parseEvents(t, buf)
	if len(events) != 2 {
		t.Fatalf("event count = %d, want exactly one terminal state", len(events))
	}
	if events[1]["error_code"] != "timeout" {
		t.Errorf("first terminal state must win, got %v", events[1]["error_code"])
	}
}
