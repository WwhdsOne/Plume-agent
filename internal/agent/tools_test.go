package agent

import (
	"context"
	"errors"
	"plume-agent/internal/model"
	"plume-agent/internal/tools"
	"reflect"
	"strings"
	"testing"
	"time"
)

func toolScript(id, name, args string) model.FakeScript {
	return model.FakeScript{Stream: []model.FakeStep{
		{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &model.ToolCall{ID: id, Name: name, Arguments: args}}},
		{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishToolCalls}},
		{Event: model.Event{Kind: model.EventStreamEnded}},
	}}
}

func finalScript(text string) model.FakeScript {
	return model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: text}, FinishReason: model.FinishStop}}
}

func TestG3ErrorsReturnToModelAndDuplicatesDoNotRerun(t *testing.T) {
	for _, tc := range []struct{ name, args, code string }{
		{"unknown", `{}`, "unknown_tool"},
		{"calculate", `{"operation":"divide","a":1,"b":0}`, "division_by_zero"},
		{"calculate", `{"operation":"add","a":1}`, "invalid_arguments"},
		{"calculate", `{"operation":"add"`, "invalid_arguments"},
	} {
		t.Run(tc.code+tc.args, func(t *testing.T) {
			fake := model.NewFake(toolScript("c", tc.name, tc.args), finalScript("explained"))
			result, err := New(fake, "m").RunStream(context.Background(), nil, "task", nil, nil)
			if err != nil || result.ToolCalls != 1 {
				t.Fatalf("%+v %v", result, err)
			}
			messages := fake.Calls()[1].Messages
			if !strings.Contains(messages[len(messages)-1].Content, tc.code) {
				t.Fatal("missing structured error")
			}
		})
	}
	fake := model.NewFake(toolScript("same", "current_time", `{}`), toolScript("same", "current_time", `{}`))
	count := 0
	r := New(fake, "m")
	r.SetTools(tools.Builtins(func() time.Time { count++; return time.Time{} }))
	result, err := r.RunStream(context.Background(), nil, "time", nil, nil)
	if err == nil || count != 1 || result.ToolCalls != 1 {
		t.Fatalf("duplicate rerun: %d %+v %v", count, result, err)
	}
}

func TestG3BatchPreflightAndIncompleteCallsNeverExecute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script model.FakeScript
		modify func(*Limits)
	}{
		{"no continuation", toolScript("c", "current_time", `{}`), func(l *Limits) { l.ModelCalls = 1 }},
		{"no tool budget", toolScript("c", "current_time", `{}`), func(l *Limits) { l.ToolCalls = -1 }},
		{"missing id", toolScript("", "current_time", `{}`), func(l *Limits) {}},
		{"missing args", toolScript("c", "current_time", ``), func(l *Limits) {}},
		{"truncated", model.FakeScript{Response: &model.ChatResponse{Message: model.Message{ToolCalls: []model.ToolCall{{ID: "c", Name: "current_time", Arguments: `{}`}}}, FinishReason: model.FinishLength}}, func(l *Limits) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			r := New(model.NewFake(tc.script), "m")
			r.SetTools(tools.Builtins(func() time.Time { count++; return time.Time{} }))
			l := DefaultLimits()
			tc.modify(&l)
			r.SetLimits(l)
			_, err := r.RunStream(context.Background(), nil, "task", nil, nil)
			if err == nil || count != 0 {
				t.Fatalf("executed incomplete call: %d %v", count, err)
			}
		})
	}
}

func TestG3FragmentedCallsAndGenerateConsistency(t *testing.T) {
	response := model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Content: "checking", Reasoning: "keep", ToolCalls: []model.ToolCall{{ID: "a", Name: "calculate", Arguments: `{"operation":"add","a":1,"b":2}`}, {ID: "b", Name: "current_time", Arguments: `{}`}}}, FinishReason: model.FinishToolCalls}}
	now := func() time.Time { return time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC) }
	var results []*RunResult
	var requests [][]model.ChatRequest
	for _, stream := range []bool{false, true} {
		fake := model.NewFake(response, finalScript("3"))
		r := New(fake, "m")
		r.SetTools(tools.Builtins(now))
		var result *RunResult
		var err error
		if stream {
			result, err = r.RunStream(context.Background(), nil, "task", nil, nil)
		} else {
			result, err = r.Run(context.Background(), nil, "task")
		}
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
		requests = append(requests, fake.Calls())
	}
	if !reflect.DeepEqual(results[0].Messages, results[1].Messages) || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatal("Generate/Stream semantics differ")
	}
	frag := toolScript("c", "cal", `{"operation":"add",`)
	frag.Stream = append(frag.Stream[:1], append([]model.FakeStep{{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &model.ToolCall{Name: "culate", Arguments: `"a":1,"b":2}`}}}}, frag.Stream[1:]...)...)
	fake := model.NewFake(frag, finalScript("3"))
	_, err := New(fake, "m").RunStream(context.Background(), nil, "task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestG3ToolTimeoutAndRunCancellation(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelRun], func(t *testing.T) {
			entered := make(chan struct{})
			registry, err := tools.New(tools.Definition{Name: "slow", Parameters: `{"type":"object"}`, Execute: func(ctx context.Context, _ string) tools.Result {
				close(entered)
				<-ctx.Done()
				return tools.Result{Code: "cancelled"}
			}})
			if err != nil {
				t.Fatal(err)
			}
			fake := model.NewFake(toolScript("c", "slow", `{}`), finalScript("timed out"))
			r := New(fake, "m")
			r.SetTools(registry)
			limits := DefaultLimits()
			limits.ToolTimeout = 20 * time.Millisecond
			r.SetLimits(limits)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelRun {
				go func() { <-entered; cancel() }()
			}
			result, err := r.RunStream(ctx, nil, "task", nil, nil)
			if cancelRun {
				if !errors.Is(err, context.Canceled) || len(fake.Calls()) != 1 || len(result.Messages) != 0 {
					t.Fatalf("cancel: %+v %v", result, err)
				}
			} else {
				if err != nil || !strings.Contains(fake.Calls()[1].Messages[3].Content, "timeout") {
					t.Fatalf("timeout result: %+v %v", result, err)
				}
			}
		})
	}
}

func TestG3PromptBudgetAndGroups(t *testing.T) {
	fake := model.NewFake(finalScript("ok"))
	r := New(fake, "m")
	l := DefaultLimits()
	l.RequestBytes = 100
	r.SetLimits(l)
	if _, err := r.Run(context.Background(), nil, "task"); err == nil || len(fake.Calls()) != 0 {
		t.Fatal("oversized mandatory request sent")
	}
	r.SetLimits(DefaultLimits())
	if _, err := r.Run(context.Background(), []model.Message{{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c", Name: "current_time", Arguments: `{}`}}}}, "task"); err == nil || len(fake.Calls()) != 0 {
		t.Fatal("broken history sent")
	}
}

func TestG3ResultTruncationAndAllowlist(t *testing.T) {
	registry, err := tools.New(tools.Definition{Name: "large", Parameters: `{"type":"object"}`, Execute: func(context.Context, string) tools.Result {
		return tools.Result{OK: true, Value: strings.Repeat("x", 1000)}
	}})
	if err != nil {
		t.Fatal(err)
	}
	fake := model.NewFake(toolScript("c", "large", `{}`), finalScript("truncated"))
	r := New(fake, "m")
	r.SetTools(registry)
	limits := DefaultLimits()
	limits.ResultBytes = 100
	r.SetLimits(limits)
	_, err = r.RunStream(context.Background(), nil, "task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := fake.Calls()[1].Messages[3].Content
	if len(last) > 100 || !strings.Contains(last, `"truncated":true`) {
		t.Fatal("result not bounded with explicit truncation marker")
	}
	fake = model.NewFake(toolScript("c", "current_time", `{}`), finalScript("not allowed"))
	r = New(fake, "m")
	r.SetTools(nil)
	_, err = r.RunStream(context.Background(), nil, "task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls()[0].Tools) != 0 || !strings.Contains(fake.Calls()[1].Messages[3].Content, "unknown_tool") {
		t.Fatal("disabled tool escaped allowlist")
	}
}

func TestG3MalformedStreamNeverExecutes(t *testing.T) {
	for _, kind := range []string{"conflict", "sparse", "duplicate", "after_finish", "no_done"} {
		t.Run(kind, func(t *testing.T) {
			first := toolScript("c", "current_time", `{}`)
			switch kind {
			case "conflict":
				first.Stream = append(first.Stream[:1], append([]model.FakeStep{{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &model.ToolCall{ID: "different"}}}}, first.Stream[1:]...)...)
			case "sparse":
				first.Stream[0].Event.ToolIndex = 1
			case "duplicate":
				first.Stream = append(first.Stream[:1], append([]model.FakeStep{{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 1, ToolCall: &model.ToolCall{ID: "c", Name: "current_time", Arguments: `{}`}}}}, first.Stream[1:]...)...)
			case "after_finish":
				first.Stream = append(first.Stream[:2], append([]model.FakeStep{{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &model.ToolCall{ID: "c"}}}}, first.Stream[2:]...)...)
			case "no_done":
				first.Stream = first.Stream[:2]
			}
			count := 0
			r := New(model.NewFake(first), "m")
			r.SetTools(tools.Builtins(func() time.Time { count++; return time.Time{} }))
			_, err := r.RunStream(context.Background(), nil, "task", nil, nil)
			if err == nil || count != 0 {
				t.Fatalf("protocol damage executed tools %d, err %v", count, err)
			}
		})
	}
}

func TestG3ToolRoundTrip(t *testing.T) {
	fake := model.NewFake(toolScript("c1", "calculate", `{"operation":"multiply","a":6,"b":7}`), model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "42"}, FinishReason: model.FinishStop}})
	result, err := New(fake, "m").RunStream(context.Background(), nil, "calculate 6*7", nil, nil)
	if err != nil {
		t.Fatalf("tool loop failed: %v", err)
	}
	if result.Message.Content != "42" {
		t.Fatalf("answer = %q", result.Message.Content)
	}
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	req := calls[1]
	if len(req.Tools) != 2 || len(req.Messages) != 4 {
		t.Fatalf("tools/messages = %d/%d", len(req.Tools), len(req.Messages))
	}
	if req.Messages[0].Role != model.RoleSystem || req.Messages[2].ToolCalls[0].ID != "c1" || req.Messages[3].ToolCallID != "c1" {
		t.Fatalf("broken role or ID association: %+v", req.Messages)
	}
	if req.Messages[3].Content != `{"ok":true,"result":42}` {
		t.Fatalf("tool result = %s", req.Messages[3].Content)
	}
}

func TestG3TruncatedResponseFails(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Content: "partial"}, FinishReason: model.FinishLength}})
	_, err := New(fake, "m").RunStream(context.Background(), nil, "hello", nil, nil)
	if err == nil {
		t.Fatal("length must not be treated as successful final answer")
	}
}
