package openai

import (
	"context"
	"reflect"
	"testing"

	"plume-agent/internal/model"
)

func assertCachedPromptTokens(t *testing.T, usage model.Usage, want *int64) {
	t.Helper()
	got := usage.CachedPromptTokens
	if want == nil {
		if got != nil {
			t.Fatalf("cached prompt tokens = %d, want unknown", *got)
		}
		return
	}
	if got == nil || *got != *want {
		t.Fatalf("cached prompt tokens = %v, want %d", got, *want)
	}
}

// 每份 HTTP fixture 都走真实 SDK 的 Generate 与 Stream 两种解析路径。
func exerciseCacheFixture(t *testing.T, usage string, deepseek bool) (model.Usage, model.Usage) {
	t.Helper()
	newAdapter := func(url string) *Adapter {
		var a *Adapter
		var err error
		if deepseek {
			a, err = NewDeepSeekAdapter(url, "")
		} else {
			// 品牌字符串也不能使通用适配器启用供应商扩展。
			a, err = NewAdapter("deepseek", url, "")
		}
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	srv, _ := captureServer(t, 200, `{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":`+usage+`}`)
	response, err := newAdapter(srv.URL).Generate(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	srv = streamServer(t, answerFrame+finishFrame+`data: {"choices":[],"usage":`+usage+"}\n\n"+doneFrame)
	stream, err := newAdapter(srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var streamed model.Usage
	for stream.Next(context.Background()) {
		if stream.Event().Kind == model.EventUsageUpdate {
			streamed = stream.Event().Usage
		}
	}
	if stream.Err() != nil {
		t.Fatal(stream.Err())
	}
	return response.Usage, streamed
}

func TestCachedPromptTokensGenerateAndStream(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		deepseek    bool
		known       bool
		cached      *int64
	}{
		{"openai-hit", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":6}}`, false, true, new(int64(6))},
		{"openai-zero", `{"prompt_tokens":0,"completion_tokens":2,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":0}}`, false, true, new(int64(0))},
		{"openai-missing", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`, false, true, nil},
		{"openai-details-missing-count", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"audio_tokens":0}}`, false, true, nil},
		{"openai-details-null", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":null}`, false, true, nil},
		{"openai-partial-usage", `{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":6}}`, false, false, new(int64(6))},
		{"openai-unknown-prompt", `{"prompt_tokens_details":{"cached_tokens":6}}`, false, false, new(int64(6))},
		{"deepseek-hit", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":6}`, true, true, new(int64(6))},
		{"deepseek-zero", `{"prompt_tokens":0,"completion_tokens":2,"total_tokens":2,"prompt_cache_hit_tokens":0}`, true, true, new(int64(0))},
		{"deepseek-both-agree", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":6,"prompt_tokens_details":{"cached_tokens":6}}`, true, true, new(int64(6))},
		{"deepseek-standard-fallback", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":6}}`, true, true, new(int64(6))},
		{"custom-extension-ignored", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":6}`, false, true, nil},
		{"custom-unverified-type-ignored", `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_cache_hit_tokens":"future"}`, false, true, nil},
		{"missing-usage", `null`, false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			generated, streamed := exerciseCacheFixture(t, tc.usage, tc.deepseek)
			for _, usage := range []model.Usage{generated, streamed} {
				if usage.OK != tc.known {
					t.Fatalf("usage known = %v, want %v", usage.OK, tc.known)
				}
				assertCachedPromptTokens(t, usage, tc.cached)
			}
			if !reflect.DeepEqual(generated, streamed) {
				t.Fatalf("Generate/Stream usage disagree: %+v / %+v", generated, streamed)
			}
		})
	}
}

func TestCachedPromptTokensRejectInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		deepseek    bool
	}{
		{"details-string", `"prompt_tokens_details":"wrong"`, false},
		{"standard-string", `"prompt_tokens_details":{"cached_tokens":"6"}`, false},
		{"standard-fraction", `"prompt_tokens_details":{"cached_tokens":0.5}`, false},
		{"standard-null", `"prompt_tokens_details":{"cached_tokens":null}`, false},
		{"standard-negative", `"prompt_tokens_details":{"cached_tokens":-1}`, false},
		{"standard-above-prompt", `"prompt_tokens_details":{"cached_tokens":11}`, false},
		{"standard-overflow", `"prompt_tokens_details":{"cached_tokens":9223372036854775808}`, false},
		{"standard-duplicate", `"prompt_tokens_details":{"cached_tokens":1,"cached_tokens":2}`, false},
		{"deepseek-string", `"prompt_cache_hit_tokens":"6"`, true},
		{"deepseek-fraction", `"prompt_cache_hit_tokens":0.5`, true},
		{"deepseek-null", `"prompt_cache_hit_tokens":null`, true},
		{"deepseek-negative", `"prompt_cache_hit_tokens":-1`, true},
		{"deepseek-above-prompt", `"prompt_cache_hit_tokens":11`, true},
		{"deepseek-conflict", `"prompt_cache_hit_tokens":6,"prompt_tokens_details":{"cached_tokens":5}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := `{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,` + tc.extra + `}`
			newAdapter := func(url string) *Adapter {
				if tc.deepseek {
					a, err := NewDeepSeekAdapter(url, "")
					if err != nil {
						t.Fatal(err)
					}
					return a
				}
				return newTestAdapter(t, url)
			}
			t.Run("generate", func(t *testing.T) {
				srv, _ := captureServer(t, 200, `{"choices":[{"message":{"content":"answer"}}],"usage":`+usage+`}`)
				_, err := newAdapter(srv.URL).Generate(context.Background(), simpleRequest("m"))
				assertCode(t, err, model.ErrInvalidResponse)
			})
			t.Run("stream", func(t *testing.T) {
				srv := streamServer(t, answerFrame+finishFrame+`data: {"choices":[],"usage":`+usage+"}\n\n"+doneFrame)
				stream, err := newAdapter(srv.URL).Stream(context.Background(), simpleRequest("m"))
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				for stream.Next(context.Background()) {
				}
				assertCode(t, stream.Err(), model.ErrInvalidResponse)
			})
		})
	}
}

func TestCachedPromptTokensStreamUsageSnapshots(t *testing.T) {
	body := answerFrame + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":6}}}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":8}}}` + "\n\n" +
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}` + "\n\n" + doneFrame
	srv := streamServer(t, body)
	stream, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var snapshots []model.Usage
	for stream.Next(context.Background()) {
		if stream.Event().Kind == model.EventUsageUpdate {
			snapshots = append(snapshots, stream.Event().Usage)
		}
	}
	if stream.Err() != nil || len(snapshots) != 3 {
		t.Fatalf("usage snapshots=%+v err=%v", snapshots, stream.Err())
	}
	for i, want := range []*int64{new(int64(6)), new(int64(8)), nil} {
		assertCachedPromptTokens(t, snapshots[i], want)
	}
	if snapshots[1].TotalTokens != 12 || snapshots[2].TotalTokens != 12 {
		t.Fatalf("usage snapshots were accumulated: %+v", snapshots)
	}
}
