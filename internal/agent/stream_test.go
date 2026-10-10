package agent

import (
	"context"
	"testing"

	"plume-agent/internal/model"
)

// TestRunStreamSeparatesReasoningAndRequiresAnswer 守住流式的两条底线：
// 推理与正文增量分开回调、空最终回答必须报错；历史里的 reasoning 在请求中不丢失。
func TestRunStreamSeparatesReasoningAndRequiresAnswer(t *testing.T) {
	for _, answer := range []string{"answer", ""} {
		fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Reasoning: "private thought", Content: answer}, FinishReason: model.FinishStop}})
		r := New(fake, "m")
		var reasoning, text string
		result, err := r.RunStream(context.Background(), []model.Message{{Role: model.RoleAssistant, Content: "old", Reasoning: "old thought"}}, "hello", func() {}, func(e model.Event) error {
			reasoning += e.ReasoningDelta
			text += e.TextDelta
			return nil
		})
		if (err != nil) != (answer == "") {
			t.Fatalf("answer=%q result=%+v err=%v", answer, result, err)
		}
		if answer != "" && (text != answer || reasoning != "private thought" || result.Message.Content != answer) {
			t.Fatalf("text=%q reasoning=%q result=%+v", text, reasoning, result)
		}
		if fake.Calls()[0].Messages[1].Reasoning != "old thought" {
			t.Fatal("reasoning lost from structured tool-capable history")
		}
	}
}
