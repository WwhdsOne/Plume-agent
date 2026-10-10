package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"plume-agent/internal/model/openai"
	"sync/atomic"
	"testing"
)

// TestG3SDKToolCycleStreamAndGenerate 用真实 openai-go 适配器守住 SDK 层工具循环：
// 工具声明随请求下发、续轮请求保留 reasoning 与 tool_call_id 关联，流式与非流式都成立。
func TestG3SDKToolCycleStreamAndGenerate(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var count atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("request decode")
					w.WriteHeader(400)
					return
				}
				index := count.Add(1)
				decl, ok := req["tools"].([]any)
				if !ok || len(decl) != 2 {
					t.Error("missing declarations")
				}
				messages := req["messages"].([]any)
				if index == 2 {
					if messages[2].(map[string]any)["reasoning_content"] != "retained" || messages[3].(map[string]any)["tool_call_id"] != "c" {
						t.Error("reasoning or tool result association lost")
					}
				}
				if !streaming {
					w.Header().Set("Content-Type", "application/json")
					if index == 1 {
						_, _ = w.Write([]byte(`{"id":"r1","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"reasoning_content":"retained","tool_calls":[{"id":"c","type":"function","function":{"name":"calculate","arguments":"{\"operation\":\"multiply\",\"a\":6,\"b\":7}"}}]}}]}`))
					} else {
						_, _ = w.Write([]byte(`{"id":"r2","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"42"}}]}`))
					}
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if index == 1 {
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"retained\",\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"calculate\",\"arguments\":\"{\\\"operation\\\":\\\"multiply\\\",\"}}]},\"finish_reason\":null}]}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a\\\":6,\\\"b\\\":7}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
				} else {
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"42\"},\"finish_reason\":\"stop\"}]}\n\n")
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			adapter, err := openai.NewDeepSeekAdapter(srv.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			runtime := New(adapter, "m")
			var result *RunResult
			if streaming {
				result, err = runtime.RunStream(context.Background(), nil, "6*7", nil, nil)
			} else {
				result, err = runtime.Run(context.Background(), nil, "6*7")
			}
			if err != nil || count.Load() != 2 || result.Message.Content != "42" {
				t.Fatalf("calls %d result %+v err %v", count.Load(), result, err)
			}
			if result.Messages[1].Reasoning != "retained" || result.Messages[2].Content != `{"ok":true,"result":42}` {
				t.Fatal("lost full tool turn")
			}
		})
	}
}
