package model

import (
	"context"
	"reflect"
	"testing"
)

// 守住 ToolDemo 的协议边界：自然语言输入不触发工具调用，只有显式
// /demo 指令才返回 ToolCalls。
func TestG3DemoUsesProtocolNotNaturalLanguage(t *testing.T) {
	demo := NewToolDemo()
	for _, input := range []string{"time please", "/demo time"} {
		resp, err := demo.Generate(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: input}}})
		if err != nil {
			t.Fatal(err)
		}
		if (len(resp.Message.ToolCalls) > 0) != (input == "/demo time") {
			t.Fatalf("input %s calls %+v", input, resp.Message.ToolCalls)
		}
	}
}

// 守住 /demo workspace 的完整工作流：六步工具（write、glob、grep、
// read、edit、bash）按固定顺序声明，全部回放后以 FinishStop 收尾。
func TestWorkspaceDemoDeclaresCompleteWorkflow(t *testing.T) {
	demo := NewToolDemo()
	messages := []Message{{Role: RoleUser, Content: "/demo workspace"}}
	names := []string{}
	for range 7 {
		response, err := demo.Generate(context.Background(), ChatRequest{Messages: messages})
		if err != nil {
			t.Fatal(err)
		}
		if response.FinishReason == FinishStop {
			break
		}
		names = append(names, response.Message.ToolCalls[0].Name)
		messages = append(messages, response.Message, Message{Role: RoleTool, ToolCallID: response.Message.ToolCalls[0].ID, Content: `{"ok":true,"result":{}}`})
	}
	if !reflect.DeepEqual(names, []string{"write", "glob", "grep", "read", "edit", "bash"}) {
		t.Fatalf("workflow=%v", names)
	}
}

// 守住 /demo budget 的终止边界：默认无限预算下恰好 12 次工具调用后以
// FinishStop 收尾，不会无限循环。
func TestWorkspaceBudgetDemoTerminatesWithUnlimitedDefaults(t *testing.T) {
	demo := NewToolDemo()
	messages := []Message{{Role: RoleUser, Content: "/demo budget"}}
	for i := range 13 {
		response, err := demo.Generate(context.Background(), ChatRequest{Messages: messages})
		if err != nil {
			t.Fatal(err)
		}
		if i == 12 {
			if response.FinishReason != FinishStop {
				t.Fatal("budget demo loops indefinitely")
			}
			return
		}
		messages = append(messages, response.Message, Message{Role: RoleTool, ToolCallID: response.Message.ToolCalls[0].ID, Content: `{"ok":true,"result":"UTC"}`})
	}
}
