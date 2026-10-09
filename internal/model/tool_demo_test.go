package model

import (
	"context"
	"reflect"
	"testing"
)

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

func TestWorkspaceDemoDeclaresCompleteWorkflow(t *testing.T) {
	demo := NewToolDemo()
	messages := []Message{{Role: RoleUser, Content: "/demo workspace"}}
	names := []string{}
	for i := 0; i < 7; i++ {
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

func TestWorkspaceBudgetDemoTerminatesWithUnlimitedDefaults(t *testing.T) {
	demo := NewToolDemo()
	messages := []Message{{Role: RoleUser, Content: "/demo budget"}}
	for i := 0; i < 13; i++ {
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
