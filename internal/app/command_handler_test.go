package app

import (
	"context"
	"testing"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// 管理操作及用法错误不能成为模型消息，也不能消耗模型轮数。
func TestCommandsNeverBecomeModelTurns(t *testing.T) {
	history := store.NewMemory()
	runtime := agent.New(model.NewFake(), "test")
	service := NewService(runtime)
	t.Cleanup(service.Close)
	if err := service.ConfigureCommands(CommandOptions{History: history, Scope: LocalScope(), Workspace: t.TempDir(), ModelRef: store.ModelRef{ConfigID: "test", Model: "test"}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/help", "/", "/status", "/model", "/history", "/sessions", "/cancel", "/unknown", "/model other", "/new\ntext"} {
		// case: 所有管理及错误路径都保留空上下文和零调用。
		result := service.HandleInput(context.Background(), CommandRequest{Text: text, Entrance: EntranceTUI})
		if result.Code == "" {
			t.Fatalf("%q has no status", text)
		}
		if service.Session().Turns() != 0 || service.Session().Statistics().Calls != 0 || service.Busy() {
			t.Fatalf("command %q reached the model", text)
		}
	}
}

// 重投旧/new 指令只返回原收据，不把当前绑定再切回旧目标。
func TestCommandReplayPreservesLaterSession(t *testing.T) {
	service := NewService(agent.New(model.NewFake(), "test"))
	t.Cleanup(service.Close)
	if err := service.ConfigureCommands(CommandOptions{History: store.NewMemory(), Scope: LocalScope(), Workspace: t.TempDir(), BuildRuntime: func(context.Context, store.ModelRef) (*agent.Runtime, error) {
		return agent.New(model.NewFake(), "test"), nil
	}}); err != nil {
		t.Fatal(err)
	}
	request := CommandRequest{Text: "/new first", RequestID: "message-1", Entrance: EntranceTUI}
	first := service.HandleInput(context.Background(), request)
	second := service.HandleInput(context.Background(), CommandRequest{Text: "/new second", RequestID: "message-2", Entrance: EntranceTUI})
	replayed := service.HandleInput(context.Background(), request)
	if first.Code != "ok" || second.Code != "ok" || !replayed.Replayed || first.SessionID != replayed.SessionID || service.SessionID() != second.SessionID {
		t.Fatalf("unexpected replay: first=%+v second=%+v replay=%+v current=%s", first, second, replayed, service.SessionID())
	}
	request.Text = "/new forged"
	if got := service.HandleInput(context.Background(), request); got.Code != "request_conflict" {
		t.Fatalf("changed duplicate: %+v", got)
	}
}
