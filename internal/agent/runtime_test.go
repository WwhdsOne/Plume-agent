package agent

import (
	"context"
	"testing"

	"plume-agent/internal/model"
)

func TestRunMergesHistoryAndInput(t *testing.T) {
	history := []model.Message{
		{Role: model.RoleUser, Content: "first"},
		{Role: model.RoleAssistant, Content: "answer one"},
	}
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message:      model.Message{Role: model.RoleAssistant, Content: "answer two"},
		FinishReason: model.FinishStop,
	}})
	runtime := New(fake, "test-model")

	result, err := runtime.Run(context.Background(), history, "second")
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if result.Message.Content != "answer two" {
		t.Errorf("reply = %q", result.Message.Content)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("call count = %d", len(calls))
	}
	if calls[0].Model != "test-model" {
		t.Errorf("request model = %q, want test-model", calls[0].Model)
	}
	want := len(history) + 2 // 基础规则 + 历史 + 当前输入
	if len(calls[0].Messages) != want {
		t.Fatalf("messages = %d, want history+input = %d", len(calls[0].Messages), want)
	}
	last := calls[0].Messages[len(calls[0].Messages)-1]
	if last.Role != model.RoleUser || last.Content != "second" {
		t.Errorf("last message = %+v, want user/second", last)
	}
}

func TestRunRejectsEmptyInput(t *testing.T) {
	runtime := New(model.NewFake(), "m")
	if _, err := runtime.Run(context.Background(), nil, ""); err == nil {
		t.Fatal("empty input should be rejected")
	}
}
