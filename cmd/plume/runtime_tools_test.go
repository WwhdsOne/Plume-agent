package main

import (
	"context"
	"encoding/json"
	"plume-agent/internal/agent"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceRuntimeUsesConfigDeclarationsAndLimits(t *testing.T) {
	for _, enabled := range [][]string{{"read", "grep", "glob", "edit", "write", "bash"}, {}} {
		t.Run(strings.Join(enabled, "-"), func(t *testing.T) {
			cfg := &config.Config{Tools: config.DefaultTools(), Agent: config.DefaultAgent()}
			cfg.Tools.Workspace = t.TempDir()
			cfg.Tools.Enabled = enabled
			cfg.Agent.Budget.RunTimeoutSeconds = 60
			fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "ok"}, FinishReason: model.FinishStop}})
			runtime := agent.New(fake, "m")
			if err := configureRuntimeTools(runtime, cfg); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Close() })
			if runtime.RunTimeout() != time.Minute {
				t.Fatal("config limits not consumed")
			}
			if _, err := runtime.Run(context.Background(), nil, "task"); err != nil {
				t.Fatal(err)
			}
			request := fake.Calls()[0]
			names := []string{}
			for _, decl := range request.Tools {
				names = append(names, decl.Name)
			}
			expected := append([]string{}, enabled...)
			slices.Sort(expected)
			if !slices.Equal(names, expected) {
				t.Fatalf("tools=%v want=%v", names, expected)
			}
			if !strings.Contains(request.Messages[0].Content, cfg.Tools.Workspace) {
				t.Fatal("workspace absent from prompt")
			}
		})
	}
}

func TestWorkspaceDefaultBudgetMatchesRuntime(t *testing.T) {
	cfg := config.DefaultAgent().Budget
	got := limitsFromConfig(cfg)
	want := agent.DefaultLimits()
	if got != want {
		t.Fatalf("runtime default=%+v persisted=%+v", want, got)
	}
	// 解码显式有限上限，避免仅展示在 config 中而未影响循环。
	var parsed config.Config
	if err := json.Unmarshal([]byte(`{"schema_version":1,"agent":{"budget":{"model_calls":1}},"tools":{"enabled":[]}}`), &parsed); err != nil {
		t.Fatal(err)
	}
	r := agent.New(model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "c", Name: "read", Arguments: `{"path":"a"}`}}}, FinishReason: model.FinishToolCalls}}), "m")
	if err := configureRuntimeTools(r, &parsed); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	result, err := r.Run(context.Background(), nil, "task")
	if err == nil || result.ToolCalls != 0 {
		t.Fatalf("finite model limit ignored: %+v %v", result, err)
	}
}
