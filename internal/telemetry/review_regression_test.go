package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"plume-agent/internal/model"
)

func reviewModelTerminal(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	var terminal map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] == EventModelFailed || event["msg"] == EventModelCompleted {
			if terminal != nil {
				t.Fatal("model span recorded more than one terminal")
			}
			terminal = event
		}
	}
	if terminal == nil {
		t.Fatal("model span did not record a terminal")
	}
	return terminal
}

func TestReviewContextSpanClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code model.ErrCode
	}{
		{"cancelled", context.Canceled, model.ErrCancelled},
		{"timeout", context.DeadlineExceeded, model.ErrTimeout},
		{"wrapped_cancelled", fmt.Errorf("stream emit: %w", context.Canceled), model.ErrCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := NewModelRecorder(&out)
			s := r.StartModel("r/model-1", "fake", model.ProtocolOpenAIChatCompletions, "m")
			s.End(&model.ChatResponse{}, tc.err)
			terminal := reviewModelTerminal(t, &out)
			if terminal["msg"] != EventModelFailed || terminal["error_code"] != string(tc.code) {
				t.Fatalf("context error lost classification: %+v", terminal)
			}
		})
	}
}

func TestReviewFailedSpanPreservesActualUsage(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			var out bytes.Buffer
			r := NewModelRecorder(&out)
			s := r.StartModel("r/model-1", "fake", model.ProtocolOpenAIChatCompletions, "m")
			resp := &model.ChatResponse{
				Message:      model.Message{Content: "private answer", Reasoning: "private reasoning"},
				FinishReason: model.FinishStop,
				Usage:        model.Usage{OK: known, PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
			}
			s.End(resp, model.NewError(model.ErrStreamInterrupt))
			s.End(resp, nil)
			terminal := reviewModelTerminal(t, &out)
			if terminal["msg"] != EventModelFailed || terminal["usage_known"] != known || terminal["finish_reason"] != string(model.FinishStop) {
				t.Fatalf("failed stream lost response metadata: %+v", terminal)
			}
			if known {
				if terminal["prompt_tokens"] != float64(2) || terminal["completion_tokens"] != float64(3) || terminal["total_tokens"] != float64(5) {
					t.Fatalf("failed stream lost known usage: %+v", terminal)
				}
			} else if _, ok := terminal["total_tokens"]; ok {
				t.Fatal("unknown usage was reported as tokens")
			}
			if strings.Contains(out.String(), "private answer") || strings.Contains(out.String(), "private reasoning") {
				t.Fatal("trace contains response content")
			}
		})
	}
}
