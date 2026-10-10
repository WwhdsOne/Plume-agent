package telemetry

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// TestStreamMetricsContainNoContentAndUnknownIsNull 守住流式 trace 的两条红线：
// 任何正文（增量或完整内容）不落盘；未发生的首思考耗时必须是显式 null 而非 0，
// span 恰好终态一次。
func TestStreamMetricsContainNoContentAndUnknownIsNull(t *testing.T) {
	var out bytes.Buffer
	r := NewModelRecorder(&out)
	r.RunStart("r", time.Now())
	r.RunPhase("r", "waiting")
	s := r.StartModel("r/model-1", "fake", model.ProtocolOpenAIChatCompletions, "m")
	s.Reasoning(ReasoningInfo{Requested: "none", Source: "configured", Effective: "none", Capability: "verified"})
	s.Observe(model.Event{Kind: model.EventTextDelta, TextDelta: "private secret"})
	s.End(&model.ChatResponse{Message: model.Message{Content: "private secret"}}, nil)
	r.RunEnd("r", RunMetrics{Duration: time.Millisecond}, nil)
	if strings.Contains(out.String(), "private secret") {
		t.Fatal("trace contains response content")
	}
	if !strings.Contains(out.String(), `"first_reasoning_ms":null`) {
		t.Fatal("missing first reasoning must be null")
	}
	if strings.Count(out.String(), `"msg":"model_completed"`) != 1 {
		t.Fatal("model span must end once")
	}
}

// TestFailureTraceDoesNotIncludeUpstreamSummary 守住错误摘要脱敏：上游错误的
// summary 可能带响应正文，不得整段落进 trace。
func TestFailureTraceDoesNotIncludeUpstreamSummary(t *testing.T) {
	var out bytes.Buffer
	r := NewModelRecorder(&out)
	r.StartModel("r/model-1", "fake", "protocol", "model").End(nil, model.NewError(model.ErrUpstream).WithSummary("private upstream content"))
	if strings.Contains(out.String(), "private upstream content") {
		t.Fatal("trace leaked upstream content")
	}
}
