package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/tools"
)

// failCancelledFinishStore 只阻止终态合并，保留收尾写入的真实检查点，
// 用于核对取消时不能只依靠最终 runs 记录掩盖内存快照漏更新。
type failCancelledFinishStore struct{ store.Store }

func (s *failCancelledFinishStore) FinishRun(context.Context, store.RunRecord) error {
	return errors.New("injected terminal save failure")
}

// TestCancelledToolFlushRetainsTerminalCheckpoint 守住工具返回时取消已生效
// 的窗口：收尾必须写入真实工具终态和摘要；即便终态事务失败，检查点也
// 不能退回准备执行的 running，更不能丢失已经发生副作用的说明。
func TestCancelledToolFlushRetainsTerminalCheckpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	registry, err := tools.New(tools.Definition{Name: "cancel_after_effect", Parameters: `{"type":"object"}`,
		Execute: func(context.Context, string) tools.Result {
			// 通过工具返回时直接取消确定性复现窄窗口，不依赖调度或计时。
			cancel()
			return tools.Result{OK: true, Value: "done", Summary: "side effect completed"}
		}})
	if err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "cancel-tool-call", Name: "cancel_after_effect", Arguments: `{}`}
	client := model.NewFake(model.FakeScript{Stream: []model.FakeStep{
		{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &call}},
		{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishToolCalls}},
		{Event: model.Event{Kind: model.EventStreamEnded}},
	}})
	runtime := agent.New(client, "m")
	runtime.SetTools(registry)
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	service := NewService(runtime)
	t.Cleanup(service.Close)
	service.EnablePersistence(&failCancelledFinishStore{Store: history}, "/w", store.ModelRef{}, time.Second)
	runID, err := service.Submit(ctx, "perform effect and cancel")
	if err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, service))
	if last.Kind != EventRunFailed || !errors.Is(last.Err, context.Canceled) {
		t.Fatalf("terminal = %+v, want cancelled run with failed terminal save", last)
	}
	checkpoint, err := history.LoadCheckpoint(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Sequence < 2 || len(checkpoint.Payload.Tools) != 1 {
		t.Fatalf("cancel flush checkpoint = %+v, want a newer single-tool snapshot", checkpoint)
	}
	tool := checkpoint.Payload.Tools[0]
	// 注册表发现取消后把调用标为 failed，但保留工具真实产生的摘要。
	if tool.CallID != call.ID || tool.Status != "failed" || tool.Summary != "side effect completed" {
		t.Fatalf("cancel flush tool = %+v, want failed with preserved side-effect summary", tool)
	}
	if len(client.Calls()) != 1 {
		t.Fatalf("model calls = %d, want no request after cancelled tool", len(client.Calls()))
	}
}
