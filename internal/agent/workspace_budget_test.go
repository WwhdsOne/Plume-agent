package agent

import (
	"context"
	"errors"
	"fmt"
	"plume-agent/internal/model"
	"plume-agent/internal/tools"
	"testing"
	"time"
)

// TestWorkspaceDefaultsAllowMoreThanEightSteps 守住默认宽松预算：
// 12 轮工具步（13 次模型调用）不被上限截断，Generate/Stream 行为一致。
func TestWorkspaceDefaultsAllowMoreThanEightSteps(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		scripts := make([]model.FakeScript, 0, 13)
		for i := range 12 {
			scripts = append(scripts, model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: fmt.Sprint(i), Name: "current_time", Arguments: `{}`}}}, FinishReason: model.FinishToolCalls}})
		}
		scripts = append(scripts, finalScript("finished"))
		r := New(model.NewFake(scripts...), "m")
		var result *RunResult
		var err error
		if streaming {
			result, err = r.RunStream(context.Background(), nil, "task", nil, nil)
		} else {
			result, err = r.Run(context.Background(), nil, "task")
		}
		if err != nil || result.ModelCalls != 13 || result.ToolCalls != 12 {
			t.Fatalf("stream=%v result=%+v error=%v", streaming, result, err)
		}
	}
}

// TestWorkspacePartialResultsUseBriefSummary 守住截断结果的事件口径：
// 工具事件上报 completed 状态与工具给出的简短摘要。
func TestWorkspacePartialResultsUseBriefSummary(t *testing.T) {
	registry, err := tools.New(tools.Definition{Name: "read", Parameters: `{"type":"object"}`, Execute: func(context.Context, string) tools.Result {
		return tools.Result{OK: true, Value: map[string]any{"content": "PRIVATE_FILE_CONTENT"}, Truncated: true, Summary: "read 1 line (truncated)"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := New(model.NewFake(toolScript("c", "read", `{}`), finalScript("ok")), "m")
	r.SetTools(registry)
	var events []ToolEvent
	_, err = r.RunStreamWithTools(context.Background(), nil, "task", nil, nil, func(_ context.Context, e ToolEvent) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Status != "completed" || events[1].Summary != "read 1 line (truncated)" {
		t.Fatalf("events=%+v", events)
	}
}

// TestWorkspaceUnlimitedRunRemainsCancellable 守住"零值表示无限"不吞掉取消：
// 关闭整体期限与次数上限后，run 仍随 context 取消及时终止。
func TestWorkspaceUnlimitedRunRemainsCancellable(t *testing.T) {
	r := New(model.NewFake(model.FakeScript{Stream: []model.FakeStep{{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "x"}, Delay: time.Hour}}}), "m")
	limits := DefaultLimits()
	limits.RunTimeout = 0
	limits.ModelCalls = 0
	limits.ToolCalls = 0
	r.SetLimits(limits)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.RunStream(ctx, nil, "task", nil, nil); done <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		var classified *model.Error
		if !errors.Is(err, context.Canceled) && (!errors.As(err, &classified) || classified.Code != model.ErrCancelled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unlimited run cannot cancel")
	}
}

// TestWorkspaceStepLimitIsIndependentOfUnlimitedTotal 守住单步工具数上限独立于总量：
// 每步 64 时 9 连发全部执行，每步 8 时整批预检拒绝、一个不执行。
func TestWorkspaceStepLimitIsIndependentOfUnlimitedTotal(t *testing.T) {
	calls := []model.ToolCall{}
	for i := range 9 {
		calls = append(calls, model.ToolCall{ID: fmt.Sprintf("c%d", i), Name: "current_time", Arguments: `{}`})
	}
	first := model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: calls}, FinishReason: model.FinishToolCalls}}
	for _, limit := range []int{64, 8} {
		r := New(model.NewFake(first, finalScript("ok")), "m")
		l := DefaultLimits()
		l.ToolCallsPerStep = limit
		r.SetLimits(l)
		result, err := r.RunStream(context.Background(), nil, "task", nil, nil)
		if limit == 64 && (err != nil || result.ToolCalls != 9) {
			t.Fatalf("nine tool batch rejected %+v %v", result, err)
		}
		if limit == 8 && (err == nil || result.ToolCalls != 0) {
			t.Fatalf("step limit not enforced %+v %v", result, err)
		}
	}
}
