package telemetry

import (
	"bytes"
	"context"
	"testing"

	"plume-agent/internal/model"
)

func TestModelSpanRecordsCachedPromptTokens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		known bool
		cache *int64
		err   error
	}{
		{"known-hit", true, new(int64(6)), nil},
		{"known-zero", true, new(int64(0)), nil},
		{"known-unknown-cache", true, nil, nil},
		{"partial-hit", false, new(int64(6)), nil},
		{"failed-hit", true, new(int64(6)), model.NewError(model.ErrStreamInterrupt)},
		{"cancelled-partial-zero", false, new(int64(0)), context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			r := NewModelRecorder(&out)
			s := r.StartModel("r/model-1", "fake", model.ProtocolOpenAIChatCompletions, "m")
			response := &model.ChatResponse{Usage: model.Usage{OK: tc.known, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CachedPromptTokens: tc.cache}}
			s.End(response, tc.err)
			s.End(response, nil)
			terminal := reviewModelTerminal(t, &out)
			value, exists := terminal["cached_prompt_tokens"]
			if !exists {
				t.Fatal("cached prompt token trace field is missing")
			}
			if tc.cache == nil {
				if value != nil {
					t.Fatalf("unknown cache became known: %+v", terminal)
				}
			} else if value != float64(*tc.cache) {
				t.Fatalf("cached prompt token trace=%v want=%d", value, *tc.cache)
			}
			if terminal["usage_known"] != tc.known {
				t.Fatalf("cache changed base usage knowledge: %+v", terminal)
			}
		})
	}
}

func TestModelSpanNoResponseRecordsUnknownCache(t *testing.T) {
	var out bytes.Buffer
	r := NewModelRecorder(&out)
	r.StartModel("r/model-1", "fake", model.ProtocolOpenAIChatCompletions, "m").End(nil, context.Canceled)
	terminal := reviewModelTerminal(t, &out)
	if cache, exists := terminal["cached_prompt_tokens"]; !exists || cache != nil {
		t.Fatalf("missing response must record unknown cache: %+v", terminal)
	}
}
