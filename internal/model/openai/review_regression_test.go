package openai

import (
	"context"
	"strings"
	"testing"

	"plume-agent/internal/model"
)

func consumeReviewStream(t *testing.T, body string) error {
	t.Helper()
	srv := streamServer(t, body)
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		return err
	}
	defer s.Close()
	for s.Next(context.Background()) {
	}
	return s.Err()
}

// 守住：带杂字符的 [DONE] 不是合法结束标记——伪造 DONE 后仍有数据帧则整条流拒绝。
func TestReviewFakeDoneBeforeRealDoneIsRejected(t *testing.T) {
	body := answerFrame + finishFrame + "data: [DONE]garbage\n\n" + answerFrame + doneFrame
	assertCode(t, consumeReviewStream(t, body), model.ErrInvalidResponse)
}

// 守住：同一 tool call id 不得别名到不同 index 的调用——流式、完成体与跨帧三种形态都拒绝。
func TestReviewToolIDsCannotAliasDifferentIndices(t *testing.T) {
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"same","function":{"name":"a","arguments":"{}"}},{"index":1,"id":"same","function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" + doneFrame
	t.Run("stream", func(t *testing.T) { assertCode(t, consumeReviewStream(t, body), model.ErrInvalidResponse) })
	completion := `{"choices":[{"message":{"tool_calls":[{"id":"same","function":{"name":"a","arguments":"{}"}},{"id":"same","function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	t.Run("generate", func(t *testing.T) {
		srv, _ := captureServer(t, 200, completion)
		_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
		assertCode(t, err, model.ErrInvalidResponse)
	})
	t.Run("different-frames", func(t *testing.T) {
		first := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"same","function":{"name":"a","arguments":"{}"}}]}}]}` + "\n\n"
		second := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"same","function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
		assertCode(t, consumeReviewStream(t, first+second+doneFrame), model.ErrInvalidResponse)
	})
}

// 守住：具名 SSE 事件受控——未知事件名按 ErrUnsupported 拒绝，error 事件归 ErrUpstream 且正文不透传，已知 message 事件放行。
func TestReviewNamedSSEEventsAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name, event, data string
		code              model.ErrCode
	}{
		{"unknown", "future.feature", `{"future_feature":true}`, model.ErrUnsupported},
		{"unknown-empty", "future.feature", "", model.ErrUnsupported},
		{"thread", "thread.message.delta", `{"future_feature":true}`, model.ErrUnsupported},
		{"error", "error", `{"message":"secret-api-key"}`, model.ErrUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := consumeReviewStream(t, "event: "+tc.event+"\ndata: "+tc.data+"\n\n"+answerFrame+finishFrame+doneFrame)
			assertCode(t, err, tc.code)
			if strings.Contains(err.Error(), "secret-api-key") {
				t.Fatal("upstream event body leaked")
			}
		})
	}
	if err := consumeReviewStream(t, "event: message\n"+answerFrame+finishFrame+doneFrame); err != nil {
		t.Fatalf("message event: %v", err)
	}
}

// 守住：SDK 的宽松类型转换不得放行——content、tool_calls、usage 等字段类型不符在流式与完成体双路径一律拒绝。
func TestReviewResponseTypesRejectSDKCoercion(t *testing.T) {
	cases := []struct{ name, choice, usage string }{
		{"numeric-content", `"content":17`, ""},
		{"object-content", `"content":{}`, ""},
		{"numeric-reasoning", `"content":"ok","reasoning_content":17`, ""},
		{"tool-array", `"content":"ok","tool_calls":"oops"`, ""},
		{"tool-object", `"content":"ok","tool_calls":[17]`, ""},
		{"tool-id", `"tool_calls":[{"index":0,"id":17,"function":{"name":"a","arguments":"{}"}}]`, ""},
		{"tool-index", `"tool_calls":[{"index":"oops","id":"x","function":{"name":"a","arguments":"{}"}}]`, ""},
		{"tool-function", `"tool_calls":[{"index":0,"id":"x","function":17}]`, ""},
		{"tool-name", `"tool_calls":[{"index":0,"id":"x","function":{"name":17,"arguments":"{}"}}]`, ""},
		{"tool-arguments", `"tool_calls":[{"index":0,"id":"x","function":{"name":"a","arguments":17}}]`, ""},
		{"negative-usage", `"content":"ok"`, `,"usage":{"prompt_tokens":-1,"completion_tokens":2,"total_tokens":1}`},
		{"string-usage", `"content":"ok"`, `,"usage":{"prompt_tokens":"1","completion_tokens":2,"total_tokens":3}`},
		{"fraction-usage", `"content":"ok"`, `,"usage":{"prompt_tokens":1,"completion_tokens":2.5,"total_tokens":3}`},
		{"usage-object", `"content":"ok"`, `,"usage":"oops"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream := `data: {"choices":[{"index":0,"delta":{` + tc.choice + `}}]` + tc.usage + "}\n\n" + finishFrame + doneFrame
			t.Run("stream", func(t *testing.T) { assertCode(t, consumeReviewStream(t, stream), model.ErrInvalidResponse) })
			completion := `{"choices":[{"index":0,"message":{` + tc.choice + `},"finish_reason":"stop"}]` + tc.usage + `}`
			t.Run("generate", func(t *testing.T) {
				srv, _ := captureServer(t, 200, completion)
				_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
				assertCode(t, err, model.ErrInvalidResponse)
			})
		})
	}
}

// 守住：choices 的结构类型契约——非对象元素、非整数或缺失 index、delta/message 非对象都拒绝。
func TestReviewChoiceStructureTypes(t *testing.T) {
	for _, choices := range []string{`"oops"`, `[17]`, `[{"index":"oops","delta":{"content":"ok"},"message":{"content":"ok"}}]`, `[{"index":0.5,"delta":{"content":"ok"},"message":{"content":"ok"}}]`, `[{"index":0,"delta":17,"message":17}]`} {
		t.Run(choices, func(t *testing.T) {
			t.Run("stream", func(t *testing.T) {
				assertCode(t, consumeReviewStream(t, "data: {\"choices\":"+choices+"}\n\n"+answerFrame+finishFrame+doneFrame), model.ErrInvalidResponse)
			})
			t.Run("generate", func(t *testing.T) {
				srv, _ := captureServer(t, 200, `{"choices":`+choices+`}`)
				_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
				assertCode(t, err, model.ErrInvalidResponse)
			})
		})
	}
	assertCode(t, consumeReviewStream(t, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n"+finishFrame+doneFrame), model.ErrInvalidResponse)
}

// 守住：校验收紧不误伤合法响应——null 字段与跨帧分片的 tool_calls（id 与 function 分帧到达）仍受支持。
func TestReviewValidNullsAndToolFragmentsRemainSupported(t *testing.T) {
	if err := consumeReviewStream(t, `data: {"choices":[{"index":0,"delta":{"content":null,"reasoning_content":null},"finish_reason":null}],"usage":null}`+"\n\n"+answerFrame+finishFrame+doneFrame); err != nil {
		t.Fatalf("valid null fields: %v", err)
	}
	first := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-a","type":"function"},{"index":1,"id":"call-b","type":"function"}]}}]}` + "\n\n"
	second := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{}"}},{"index":1,"function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n"
	if err := consumeReviewStream(t, first+second+doneFrame); err != nil {
		t.Fatalf("independent tool fragments: %v", err)
	}
	srv, _ := captureServer(t, 200, `{"choices":[{"message":{"content":null,"reasoning_content":null,"tool_calls":[{"id":"call-a","function":{"name":"a","arguments":"{}"}},{"id":"call-b","function":{"name":"b","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":null}`)
	resp, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
	if err != nil || len(resp.Message.ToolCalls) != 2 || resp.Usage.OK {
		t.Fatalf("valid tool completion: resp=%+v err=%v", resp, err)
	}
}

// 守住：部分 usage（仅 total_tokens）保持 unknown——完成体不置 OK，流式也不伪造 usage 事件。
func TestReviewPartialUsageStaysUnknown(t *testing.T) {
	srv, _ := captureServer(t, 200, `{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":3}}`)
	resp, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.OK {
		t.Fatalf("partial usage became known: %+v", resp.Usage)
	}
	srv = streamServer(t, answerFrame+finishFrame+`data: {"choices":[],"usage":{"total_tokens":3}}`+"\n\n"+doneFrame)
	s, err := newTestAdapter(t, srv.URL).Stream(context.Background(), simpleRequest("m"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for s.Next(context.Background()) {
		if s.Event().Kind == model.EventUsageUpdate {
			t.Fatal("partial usage update fabricated")
		}
	}
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
}

// 守住：顶层 id 非字符串（数字/null/true/对象）时拒绝，SDK 的隐式强转不得放行。
func TestReviewTopLevelIDCannotBeCoerced(t *testing.T) {
	for _, id := range []string{`17`, `null`, `true`, `{}`} {
		t.Run(id, func(t *testing.T) {
			t.Run("generate", func(t *testing.T) {
				srv, _ := captureServer(t, 200, `{"id":`+id+`,"choices":[{"message":{"content":"ok"}}]}`)
				_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
				assertCode(t, err, model.ErrInvalidResponse)
			})
			t.Run("stream", func(t *testing.T) {
				body := `data: {"id":` + id + `,"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n" + finishFrame + doneFrame
				assertCode(t, consumeReviewStream(t, body), model.ErrInvalidResponse)
			})
		})
	}
}

// 守住：未知或缺失的 tool type 按 ErrUnsupported 拒绝——协议仅支持 function，流式与完成体一致。
func TestReviewUnknownToolTypeIsUnsupported(t *testing.T) {
	for _, toolType := range []string{"future", ""} {
		t.Run(toolType, func(t *testing.T) {
			call := `{"index":0,"id":"call-a","type":"` + toolType + `","function":{"name":"a","arguments":"{}"}}`
			t.Run("generate", func(t *testing.T) {
				srv, _ := captureServer(t, 200, `{"choices":[{"message":{"tool_calls":[`+call+`]},"finish_reason":"tool_calls"}]}`)
				_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
				assertCode(t, err, model.ErrUnsupported)
			})
			t.Run("stream", func(t *testing.T) {
				body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[` + call + `]},"finish_reason":"tool_calls"}]}` + "\n\n" + doneFrame
				assertCode(t, consumeReviewStream(t, body), model.ErrUnsupported)
			})
		})
	}
}

// 守住：重复 JSON 字段（含 \u 转写绕过）若类型冲突不得依赖 SDK 静默取值放行，一律拒绝。
func TestReviewDuplicateJSONFieldsCannotDisagreeWithSDK(t *testing.T) {
	for _, fields := range []string{
		`"content":17,"content":"ok"`,
		`"content":17,"\u0063ontent":"ok"`,
		`"content":"ok","tool_calls":[{"index":0,"id":17,"id":"call-a","function":{"name":"a","arguments":"{}"}}]`,
	} {
		t.Run(fields, func(t *testing.T) {
			t.Run("generate", func(t *testing.T) {
				srv, _ := captureServer(t, 200, `{"choices":[{"message":{`+fields+`}}]}`)
				_, err := newTestAdapter(t, srv.URL).Generate(context.Background(), simpleRequest("m"))
				assertCode(t, err, model.ErrInvalidResponse)
			})
			t.Run("stream", func(t *testing.T) {
				body := `data: {"choices":[{"index":0,"delta":{` + fields + `}}]}` + "\n\n" + finishFrame + doneFrame
				assertCode(t, consumeReviewStream(t, body), model.ErrInvalidResponse)
			})
		})
	}
}
