package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"plume-agent/internal/model"
	"testing"
)

// 守住：G3 工具声明原样下发，工具历史回放不丢 assistant 的 reasoning_content 与 tool 结果的 tool_call_id。
func TestG3ToolDeclarationsAndReasoningReplay(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","choices":[{"index":0,"message":{"role":"assistant","content":"42","reasoning_content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	a, err := NewDeepSeekAdapter(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Generate(context.Background(), model.ChatRequest{Model: "m", Tools: []model.ToolDeclaration{{Name: "calculate", Description: "Arithmetic", Parameters: `{"type":"object"}`}}, Messages: []model.Message{
		{Role: model.RoleUser, Content: "6*7"},
		{Role: model.RoleAssistant, Reasoning: "retained", ToolCalls: []model.ToolCall{{ID: "c", Name: "calculate", Arguments: `{}`}}},
		{Role: model.RoleTool, ToolCallID: "c", Content: `{"ok":true,"result":42}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(body["tools"].([]any)) != 1 {
		t.Fatalf("missing tool declarations: %v", body)
	}
	messages := body["messages"].([]any)
	if messages[1].(map[string]any)["reasoning_content"] != "retained" {
		t.Fatal("reasoning lost on tool replay")
	}
	if messages[2].(map[string]any)["tool_call_id"] != "c" {
		t.Fatal("tool result ID lost")
	}
}
