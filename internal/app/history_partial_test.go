package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"plume-agent/internal/model"
)

// TestSpecFailedContinuationRetainsAllDisplayedText 守住工具后续调用失败时保留已显示正文与 Thought。
func TestSpecFailedContinuationRetainsAllDisplayedText(t *testing.T) {
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "already displayed answer", Reasoning: "already displayed thought", ToolCalls: []model.ToolCall{{ID: "call-1", Name: "calculate", Arguments: `{"expression":"1+1"}`}}}, FinishReason: model.FinishToolCalls}},
		model.FakeScript{Err: errors.New("second request failed")},
	)
	svc, history := newPersistedService(t, fake)
	defer svc.Close()
	if _, err := svc.Submit(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, svc)
	svc.waitIdle()
	var displayed strings.Builder
	var thought strings.Builder
	for _, e := range events {
		displayed.WriteString(e.TextDelta)
		thought.WriteString(e.ReasoningDelta)
	}
	if !strings.Contains(displayed.String(), "already displayed answer") || !strings.Contains(thought.String(), "already displayed thought") {
		t.Fatalf("fixture showed no initial answer/thought: %q/%q", displayed.String(), thought.String())
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v error=%v", runs, err)
	}
	if !strings.Contains(runs[0].Reply, "already displayed answer") || !strings.Contains(runs[0].Reasoning, "already displayed thought") {
		t.Fatalf("displayed text lost on failure: reply=%q reasoning=%q status=%s", runs[0].Reply, runs[0].Reasoning, runs[0].Status)
	}
}
