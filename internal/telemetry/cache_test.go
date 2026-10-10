package telemetry

import (
	"bytes"
	"context"
	"testing"

	"plume-agent/internal/model"
)

// TestModelSpanRecordsCachedPromptTokens 守住缓存 token 的如实上报：
// 有值记值（0 也是已知）、未知记 null，缓存字段不得影响 usage_known 判定，
// 重复 End 以首个终态为准。
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

// TestModelSpanNoResponseRecordsUnknownCache 守住字段在场契约：响应整体缺失
// （如取消）时终态也要带 cached_prompt_tokens 字段且值为 null，不能悄悄缺字段。
func TestModelSpanNoResponseRecordsUnknownCache(t *testing.T) {
	var out bytes.Buffer
	r := NewModelRecorder(&out)
	r.StartModel("r/model-1", "fake", model.ProtocolOpenAIChatCompletions, "m").End(nil, context.Canceled)
	terminal := reviewModelTerminal(t, &out)
	if cache, exists := terminal["cached_prompt_tokens"]; !exists || cache != nil {
		t.Fatalf("missing response must record unknown cache: %+v", terminal)
	}
}
