package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"plume-agent/internal/model"
	"plume-agent/internal/tools"
)

// TestRuntimeResetSessionStateClearsWorkspaceReads 验证 Runtime 的公开边界
// 实际清除其内部注册表摘要，而非仅重建测试用的另一个注册表。
func TestRuntimeResetSessionStateClearsWorkspaceReads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	options := tools.DefaultWorkspaceOptions()
	options.Root = root
	options.Enabled = []string{"read", "write"}
	registry, err := tools.NewWorkspace(options)
	if err != nil {
		t.Fatal(err)
	}
	call := func(id, name, args string) model.FakeScript {
		return model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: id, Name: name, Arguments: args}}}, FinishReason: model.FinishToolCalls}}
	}
	fake := model.NewFake(
		call("read-old", "read", `{"path":"a.txt"}`), finalScript("read done"),
		call("overwrite-old", "write", `{"path":"a.txt","content":"new","overwrite":true}`), finalScript("rejected"),
		call("read-new", "read", `{"path":"a.txt"}`), call("overwrite-new", "write", `{"path":"a.txt","content":"new","overwrite":true}`), finalScript("updated"),
	)
	r := New(fake, "m")
	r.SetTools(registry)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := r.Run(context.Background(), nil, "read the file"); err != nil {
		t.Fatal(err)
	}
	if err := r.ResetSessionState(); err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), nil, "overwrite without rereading")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 4 || !strings.Contains(result.Messages[2].Content, `"error":"read_required"`) {
		t.Fatalf("runtime retained old read: %+v", result.Messages)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("rejected overwrite changed file: %q, %v", data, err)
	}
	if _, err := r.Run(context.Background(), nil, "reread then overwrite"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("fresh read did not authorize overwrite: %q, %v", data, err)
	}
}
