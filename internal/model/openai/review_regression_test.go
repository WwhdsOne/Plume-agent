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

func TestReviewFakeDoneBeforeRealDoneIsRejected(t *testing.T) {
	body := answerFrame + finishFrame + "data: [DONE]garbage\n\n" + answerFrame + doneFrame
	assertCode(t, consumeReviewStream(t, body), model.ErrInvalidResponse)
}

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
