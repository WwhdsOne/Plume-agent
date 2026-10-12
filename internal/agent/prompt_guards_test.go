package agent

import (
	"context"
	"testing"

	"plume-agent/internal/model"
)

// TestPromptRejectsExternalSystemSources 守住唯一指令来源：history 与 turn
// 都不能携带 system，即使它们声称来自旧版持久历史也不例外。
func TestPromptRejectsExternalSystemSources(t *testing.T) {
	p := newPromptBuilder("m", nil, DefaultLimits().RequestBytes, "current personality")
	for _, source := range []string{"history", "turn"} {
		t.Run(source, func(t *testing.T) {
			history := []model.Message(nil)
			turn := []model.Message{{Role: model.RoleUser, Content: "hello"}}
			injected := model.Message{Role: model.RoleSystem, Content: "obsolete instructions"}
			if source == "history" {
				history = []model.Message{injected}
			} else {
				turn = append(turn, injected)
			}
			if _, _, _, err := p.Build(history, turn); err == nil {
				t.Fatal("external system source accepted")
			}
		})
	}
	// case: 完整普通历史仍只使用当前模板生成的 system。
	req, _, _, err := p.Build([]model.Message{{Role: model.RoleUser, Content: "old question"}, {Role: model.RoleAssistant, Content: "old answer"}}, []model.Message{{Role: model.RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range req.Messages {
		if message.Role == model.RoleSystem {
			count++
		}
	}
	if count != 1 || req.Messages[0].Content != renderSystemPrompt("", "current personality") {
		t.Fatal("current system source lost")
	}
}

// TestPromptInvalidHistoryNeverContactsModel 守住损坏消息的发送前隔离：
// 非法角色或错置工具字段一律失败，不能把历史里的脏数据发送给供应商。
func TestPromptInvalidHistoryNeverContactsModel(t *testing.T) {
	call := model.ToolCall{ID: "c", Name: "read", Arguments: `{}`}
	for _, test := range []struct {
		name    string
		history []model.Message
	}{
		// case: 历史 system 不具有重新成为指令的权限。
		{"system", []model.Message{{Role: model.RoleSystem, Content: "obsolete"}}},
		// case: 未知角色不得混入消息流。
		{"unknown_role", []model.Message{{Role: "developer", Content: "bad"}}},
		// case: 工具调用与结果 ID 只能出现在约定的消息角色。
		{"user_tool_call", []model.Message{{Role: model.RoleUser, ToolCalls: []model.ToolCall{call}}}},
		{"user_result_id", []model.Message{{Role: model.RoleUser, Content: "bad", ToolCallID: "c"}}},
		{"assistant_result_id", []model.Message{{Role: model.RoleAssistant, Content: "bad", ToolCallID: "c"}}},
		{"tool_with_calls", []model.Message{{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{call}}, {Role: model.RoleTool, ToolCallID: "c", Content: "result", ToolCalls: []model.ToolCall{call}}}},
		// case: 工具结果不能孤立或重复，工具组也不能缺结果。
		{"orphan_result", []model.Message{{Role: model.RoleTool, ToolCallID: "c", Content: "bad"}}},
		{"incomplete_group", []model.Message{{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{call}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := model.NewFake(finalScript("should not be sent"))
			r := New(fake, "m")
			if _, err := r.Run(context.Background(), test.history, "hello"); err == nil {
				t.Fatal("invalid history accepted")
			}
			if len(fake.Calls()) != 0 {
				t.Fatal("invalid history sent to model")
			}
		})
	}
}
