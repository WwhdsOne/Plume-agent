package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"plume-agent/internal/model"
	"strings"
	"testing"
)

func TestGenerateStreamReasoningAnswerUsageAgree(t *testing.T) {
	for _, effort := range []model.ReasoningEffort{model.ReasoningHigh, model.ReasoningNone} {
		t.Run(string(effort), func(t *testing.T) {
			reasoning := "先思考后回答"
			if effort == model.ReasoningNone {
				reasoning = ""
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				usage := map[string]any{"prompt_tokens": 2, "completion_tokens": 5, "total_tokens": 7}
				if req["stream"] == true {
					w.Header().Set("Content-Type", "text/event-stream")
					frame := func(delta map[string]any, finish any) {
						data, _ := json.Marshal(map[string]any{"id": "c", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
						_, _ = io.WriteString(w, "data: "+string(data)+"\n\n")
					}
					frame(map[string]any{"reasoning_content": reasoning, "content": "答案"}, nil)
					frame(map[string]any{"reasoning_content": "", "content": "后半"}, "stop")
					data, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": usage})
					// 重复尾帧是完整计量快照，不是增量 token。
					_, _ = io.WriteString(w, "data: "+string(data)+"\n\ndata: "+string(data)+"\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "c", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"content": "答案后半", "reasoning_content": reasoning}, "finish_reason": "stop"}}, "usage": usage})
				}
			}))
			defer srv.Close()
			a, _ := NewAdapter(srv.URL, "")
			req := model.ChatRequest{Model: "m", ReasoningEffort: effort, Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}}
			generated, err := a.Generate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			s, err := a.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var answer, thought string
			var usage model.Usage
			var finish model.FinishReason
			for s.Next(context.Background()) {
				e := s.Event()
				answer += e.TextDelta
				thought += e.ReasoningDelta
				if e.Kind == model.EventUsageUpdate {
					usage = e.Usage
				}
				if e.Kind == model.EventModelDone {
					finish = e.FinishReason
				}
			}
			if s.Err() != nil || answer != generated.Message.Content || thought != generated.Message.Reasoning || usage != generated.Usage || finish != generated.FinishReason {
				t.Fatalf("stream answer=%q thought=%q usage=%+v finish=%q err=%v generate=%+v", answer, thought, usage, finish, s.Err(), generated)
			}
		})
	}
}

func TestStreamReasoningVariants(t *testing.T) {
	cases := []struct {
		name, delta     string
		effort          model.ReasoningEffort
		code            model.ErrCode
		answer, thought string
	}{
		{"only-reasoning", `{"reasoning_content":"plan"}`, model.ReasoningHigh, model.ErrInvalidResponse, "", "plan"},
		{"disabled-unexpected", `{"reasoning_content":"plan","content":"answer"}`, model.ReasoningNone, model.ErrUnsupported, "", ""},
		{"missing-field", `{"content":"answer"}`, model.ReasoningHigh, "", "answer", ""},
		{"null-field", `{"reasoning_content":null,"content":"answer"}`, model.ReasoningHigh, "", "answer", ""},
		{"wrong-field-type", `{"reasoning_content":17,"content":"answer"}`, model.ReasoningHigh, model.ErrInvalidResponse, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `data: {"choices":[{"index":0,"delta":` + tc.delta + `}]}\n\n` + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n`
			body = strings.ReplaceAll(body, `\n`, "\n")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			a, _ := NewAdapter(srv.URL, "")
			s, err := a.Stream(context.Background(), model.ChatRequest{Model: "m", ReasoningEffort: tc.effort, Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var answer, thought string
			for s.Next(context.Background()) {
				answer += s.Event().TextDelta
				thought += s.Event().ReasoningDelta
			}
			if answer != tc.answer || thought != tc.thought {
				t.Fatalf("answer=%q thought=%q", answer, thought)
			}
			if tc.code == "" {
				if s.Err() != nil {
					t.Fatal(s.Err())
				}
			} else {
				var e *model.Error
				if !errors.As(s.Err(), &e) || e.Code != tc.code {
					t.Fatalf("err=%v want=%s", s.Err(), tc.code)
				}
			}
		})
	}
}
