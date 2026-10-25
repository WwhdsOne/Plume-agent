package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// countingCheckpointStore 计数检查点写入，供合并节流断言；也可在首次
// SaveCheckpoint 注入"提交未知"（底层已成功但返回错误）或确定失败。
type countingCheckpointStore struct {
	store.Store
	saves       atomic.Int64
	failFirst   bool
	unknowFirst bool
	failedOnce  atomic.Bool
}

func (c *countingCheckpointStore) SaveCheckpoint(ctx context.Context, runID string, sequence int64, payload store.CheckpointPayload, savedAt time.Time) error {
	c.saves.Add(1)
	if c.failFirst && c.failedOnce.CompareAndSwap(false, true) {
		return errors.New("injected deterministic checkpoint failure") // 底层确实没写进去。
	}
	err := c.Store.SaveCheckpoint(ctx, runID, sequence, payload, savedAt)
	if err != nil {
		return err
	}
	if c.unknowFirst && c.failedOnce.CompareAndSwap(false, true) {
		return context.DeadlineExceeded // 底层已提交，模拟提交结果未知。
	}
	return nil
}

// streamReply 构造一个流式两步脚本：若干正文增量后 stop。
func streamReply(deltas ...string) model.FakeScript {
	script := model.FakeScript{}
	for _, delta := range deltas {
		script.Stream = append(script.Stream, model.FakeStep{Event: model.Event{Kind: model.EventTextDelta, TextDelta: delta}})
	}
	script.Stream = append(script.Stream,
		model.FakeStep{Event: model.Event{Kind: model.EventUsageUpdate, Usage: model.Usage{OK: true, PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}}},
		model.FakeStep{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishStop}},
		model.FakeStep{Event: model.Event{Kind: model.EventStreamEnded}})
	return script
}

// TestCheckpointMergedFlushThrottles 守住合并写入：高频增量只触发首次
// 立即写与收尾强制写，不按 token 开事务；终态事务随后清理检查点。
func TestCheckpointMergedFlushThrottles(t *testing.T) {
	fake := model.NewFake(streamReply("a", "b", "c", "d"))
	svc, history := newPersistedService(t, fake)
	counting := &countingCheckpointStore{Store: history}
	svc.EnablePersistence(counting, "/ws", store.ModelRef{ConfigID: "c", Provider: "p", Protocol: "openai-chat-completions", Model: "m"}, time.Second)
	svc.mu.Lock()
	svc.checkpointInterval = time.Hour // 大间隔：中间增量全部节流。
	svc.mu.Unlock()
	defer svc.Close()

	runID, err := svc.Submit(context.Background(), "stream me")
	if err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunCompleted || last.Reply != "abcd" {
		t.Fatalf("terminal = %+v", last)
	}
	if saves := counting.saves.Load(); saves != 2 {
		t.Fatalf("checkpoint saves = %d, want 2 (first delta + finish flush)", saves)
	}
	// 成功终态同事务清理：无检查点残片。
	if _, err := history.LoadCheckpoint(context.Background(), runID); !errors.Is(err, store.ErrCheckpointNotFound) {
		t.Fatalf("checkpoint after completion err = %v", err)
	}
}

// TestCheckpointCancelFlushKeepsPartial 守住取消路径：收尾期限内强制保存
// 最新快照，终态展示并入 runs 后检查点清理由终态事务完成。
func TestCheckpointCancelFlushKeepsPartial(t *testing.T) {
	release := make(chan struct{})
	script := model.FakeScript{}
	script.Stream = append(script.Stream,
		model.FakeStep{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "partial answer "}},
		model.FakeStep{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "never shown"}, Wait: release},
		model.FakeStep{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishStop}},
		model.FakeStep{Event: model.Event{Kind: model.EventStreamEnded}})
	fake := model.NewFake(script)
	svc, history := newPersistedService(t, fake)
	defer svc.Close()

	runID, err := svc.Submit(context.Background(), "cancel me")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	waitStarted := false
	for !waitStarted {
		select {
		case ev := <-svc.Events():
			if ev.Kind == EventTextDelta {
				waitStarted = true
			}
		case <-deadline:
			t.Fatal("first delta never arrived")
		}
	}
	svc.Cancel()
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed {
		t.Fatalf("terminal = %+v", last)
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunCancelled {
		t.Fatalf("runs = %v err=%v", runs, err)
	}
	if !strings.HasPrefix(runs[0].Reply, "partial answer") {
		t.Fatalf("cancelled reply = %q", runs[0].Reply)
	}
	if _, err := history.LoadCheckpoint(context.Background(), runID); !errors.Is(err, store.ErrCheckpointNotFound) {
		t.Fatalf("checkpoint after cancel err = %v", err)
	}
	close(release)
}

// TestCheckpointToolBoundaryPersistedBeforeSideEffect 守住关键边界：工具
// "准备执行"记录确认落盘后才执行副作用，完成摘要及时保存；后续模型调用
// 被挂住时库里已有 running→completed 的检查点。
func TestCheckpointToolBoundaryPersistedBeforeSideEffect(t *testing.T) {
	hold := make(chan struct{})
	toolCall := model.ToolCall{ID: "call-1", Name: "calculate", Arguments: `{"operation":"add","a":1,"b":2}`}
	first := model.FakeScript{}
	first.Stream = append(first.Stream, model.FakeStep{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &toolCall}},
		model.FakeStep{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishToolCalls}},
		model.FakeStep{Event: model.Event{Kind: model.EventStreamEnded}})
	second := model.FakeScript{}
	second.Stream = append(second.Stream,
		model.FakeStep{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "after tools "}, Wait: hold},
		model.FakeStep{Event: model.Event{Kind: model.EventUsageUpdate, Usage: model.Usage{OK: true, TotalTokens: 2}}},
		model.FakeStep{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishStop}},
		model.FakeStep{Event: model.Event{Kind: model.EventStreamEnded}})
	fake := model.NewFake(first, second)
	svc, history := newPersistedService(t, fake)
	defer svc.Close()

	runID, err := svc.Submit(context.Background(), "run the tool")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	sawTool := false
	for !sawTool {
		select {
		case ev := <-svc.Events():
			if ev.Kind == EventToolUpdate && ev.Tool.Status == "completed" {
				sawTool = true
			}
		case <-deadline:
			t.Fatal("tool never completed")
		}
	}
	// 第二次模型调用被挂住：此刻检查点必须已确认工具完成状态。
	checkpoint, err := history.LoadCheckpoint(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Payload.Tools) != 1 || checkpoint.Payload.Tools[0].Status != "completed" || checkpoint.Payload.Tools[0].Name != "calculate" {
		t.Fatalf("checkpoint tools = %+v", checkpoint.Payload.Tools)
	}
	svc.Cancel()
	terminal(t, drainEvents(t, svc))
	close(hold)
}

// TestCheckpointFailureStopsRun 守住保存失败语义：首次检查点错误锁存，
// 后续生成/工具停止，run 以失败终态结束。
func TestCheckpointFailureStopsRun(t *testing.T) {
	fake := model.NewFake(streamReply("hello"))
	svc, history := newPersistedService(t, fake)
	counting := &countingCheckpointStore{Store: history, failFirst: true}
	svc.EnablePersistence(counting, "/ws", store.ModelRef{ConfigID: "c", Provider: "p", Protocol: "openai-chat-completions", Model: "m"}, time.Second)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "doomed"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed {
		t.Fatalf("terminal = %+v", last)
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunFailed {
		t.Fatalf("runs = %v err=%v", runs, err)
	}
	if saves := counting.saves.Load(); saves != 1 {
		t.Fatalf("saves = %d, want 1; first failure must latch and stop further writes", saves)
	}
}

// TestCheckpointUnknownCommitResolves 守住提交未知核查：SaveCheckpoint
// 超时错误但事务实际提交时，按同序号同载荷确认后继续，run 不失败。
func TestCheckpointUnknownCommitResolves(t *testing.T) {
	fake := model.NewFake(streamReply("fine"))
	svc, history := newPersistedService(t, fake)
	counting := &countingCheckpointStore{Store: history, unknowFirst: true}
	svc.EnablePersistence(counting, "/ws", store.ModelRef{ConfigID: "c", Provider: "p", Protocol: "openai-chat-completions", Model: "m"}, time.Second)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "resolve me"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunCompleted || last.Reply != "fine" {
		t.Fatalf("terminal = %+v", last)
	}
	if saves := counting.saves.Load(); saves < 2 {
		t.Fatalf("saves = %d", saves)
	}
}

// TestLoadRecoveryBuildsView 守住恢复信息构造：只有最后 run 未完成才提供
// 候选；检查点合并正文与"结果未确认"标记；completed 尾不提供。
func TestLoadRecoveryBuildsView(t *testing.T) {
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	ctx := context.Background()
	now := time.Now()
	session := store.SessionRecord{ID: "sess-rec", Workspace: "/ws", Title: "t", ModelRef: store.ModelRef{ConfigID: "c"}, CreatedAt: now, LastRunAt: now}
	seed := func(runID string, status store.RunStatus, finalize func(store.RunRecord) error) {
		if err := history.StartRun(ctx, session, store.RunRecord{ID: runID, Input: "input " + runID, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if finalize != nil {
			record := store.RunRecord{ID: runID, SessionID: session.ID, Input: "input " + runID, Status: status, StartedAt: now, FinishedAt: now}
			if err := finalize(record); err != nil {
				t.Fatal(err)
			}
		}
	}
	turn := []model.Message{{Role: model.RoleUser, Content: "input run-ok"}, {Role: model.RoleAssistant, Content: "done"}}
	seed("run-ok", store.RunCompleted, func(record store.RunRecord) error {
		return history.CompleteRun(ctx, record, turn)
	})
	seed("run-crash", store.RunRunning, func(record store.RunRecord) error {
		// 崩溃语义：只留检查点；run 保持 running，由下次打开的
		// InterruptRunning 标记 interrupted（检查点清理的例外路径）。
		if err := history.SaveCheckpoint(ctx, record.ID, 2, store.CheckpointPayload{Reply: "halfway",
			Tools: []store.CheckpointToolState{{CallID: "t1", Name: "bash", Status: "running"}, {CallID: "t2", Name: "read", Status: "completed", Summary: "file"}}}, now); err != nil {
			return err
		}
		_, err := history.InterruptRunning(ctx, now)
		return err
	})

	runs, err := history.ListRuns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, recovery, err := LoadRecovery(ctx, history, session.ID, runs)
	if err != nil {
		t.Fatal(err)
	}
	if recovery == nil || recovery.LastRunID != "run-crash" || recovery.LastStatus != store.RunInterrupted {
		t.Fatalf("recovery = %+v", recovery)
	}
	if recovery.ResendInput != "input run-crash" || recovery.Reply != "halfway" {
		t.Fatalf("recovery content = %+v", recovery)
	}
	if len(recovery.Tools) != 2 || !recovery.Tools[0].Unconfirmed || recovery.Tools[1].Unconfirmed {
		t.Fatalf("recovery tools = %+v", recovery.Tools)
	}

	// case: 最后 run 成功时不提供候选，不越过较新成功 run 找旧失败。
	okRun := store.RunRecord{ID: "run-ok2", SessionID: session.ID, Input: "again", StartedAt: now.Add(time.Minute)}
	if err := history.StartRun(ctx, session, okRun); err != nil {
		t.Fatal(err)
	}
	okRun.Status, okRun.FinishedAt = store.RunCompleted, now.Add(time.Minute)
	if err := history.CompleteRun(ctx, okRun, []model.Message{{Role: model.RoleUser, Content: "again"}, {Role: model.RoleAssistant, Content: "ok"}}); err != nil {
		t.Fatal(err)
	}
	runs, err = history.ListRuns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, recovery, err = LoadRecovery(ctx, history, session.ID, runs)
	if err != nil || recovery != nil {
		t.Fatalf("recovery after completed tail = %+v err=%v", recovery, err)
	}
}

// TestExitSummaryBindsLastRun 守住退出摘要绑定：结论跟随本轮 run、收尾
// 超时如实 unknown、恢复会话无新 run 不复用旧结论、空会话不出结论。
func TestExitSummaryBindsLastRun(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message: model.Message{Role: model.RoleAssistant, Content: "ok"}, FinishReason: model.FinishStop,
		Usage: model.Usage{OK: true, TotalTokens: 3},
	}})
	svc, history := newPersistedService(t, fake)
	t.Cleanup(func() { _ = history.Close() })
	if err := svc.ConfigureCommands(CommandOptions{History: history, Scope: LocalScope(), Workspace: "/ws", ModelRef: store.ModelRef{ConfigID: "c"}}); err != nil {
		t.Fatal(err)
	}

	// case: 无 run 空会话不出结论。
	captured := svc.CaptureExitState()
	if captured.HasRun || svc.ExitSummary(captured, true) != "unknown" {
		t.Fatalf("empty session snapshot = %+v", captured)
	}

	// case: 成功 run 后 saved；收尾超时 unknown。
	if _, err := svc.Submit(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	terminal(t, drainEvents(t, svc))
	captured = svc.CaptureExitState()
	if !captured.HasRun || captured.LastRunID == "" {
		t.Fatalf("snapshot after run = %+v", captured)
	}
	if status := svc.ExitSummary(captured, true); status != "saved" {
		t.Fatalf("status = %q, want saved", status)
	}
	if status := svc.ExitSummary(captured, false); status != "unknown" {
		t.Fatalf("timeout status = %q, want unknown", status)
	}

	// case: 第二个 run 立即取消后退出：结论绑定第二个 run 的终态，而不是
	// 沿用第一个 run 的 saved（同为 saved 但身份必须换绑）。
	hang := model.NewFake(model.FakeScript{Stream: []model.FakeStep{
		{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "x"}, Delay: time.Hour},
		{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishStop}},
		{Event: model.Event{Kind: model.EventStreamEnded}},
	}})
	svc.mu.Lock()
	svc.runtime = agent.New(hang, "m")
	svc.mu.Unlock()
	secondID, err := svc.Submit(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	svc.Cancel()
	terminal(t, drainEvents(t, svc))
	captured = svc.CaptureExitState()
	if captured.LastRunID != secondID {
		t.Fatalf("snapshot last run = %q, want %q", captured.LastRunID, secondID)
	}
	if status := svc.ExitSummary(captured, true); status != "saved" {
		t.Fatalf("cancelled+saved status = %q", status)
	}

	// case: 恢复会话后没有新 run：结论不沿用旧 run，正常收尾即为 saved。
	if err := svc.AdoptSession("restored-session", RestoreSession(nil, 0, nil, "", time.Now())); err != nil {
		t.Fatal(err)
	}
	captured = svc.CaptureExitState()
	if captured.LastRunID != "" {
		t.Fatalf("restored snapshot = %+v, want empty last run", captured)
	}
	if status := svc.ExitSummary(captured, true); status != "saved" {
		t.Fatalf("restored status = %q", status)
	}
}

// TestCheckpointToolGateBlocksSideEffectOnFailure 守住门禁本义：工具
// "准备执行"检查点写入确定失败时，工具不得执行（事件流只有 running
// 阶段，没有 completed 结果），run 以失败终态结束。
func TestCheckpointToolGateBlocksSideEffectOnFailure(t *testing.T) {
	toolCall := model.ToolCall{ID: "gate-1", Name: "calculate", Arguments: `{"operation":"add","a":1,"b":2}`}
	first := model.FakeScript{}
	first.Stream = append(first.Stream, model.FakeStep{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &toolCall}},
		model.FakeStep{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishToolCalls}},
		model.FakeStep{Event: model.Event{Kind: model.EventStreamEnded}})
	fake := model.NewFake(first)
	svc, history := newPersistedService(t, fake)
	counting := &countingCheckpointStore{Store: history, failFirst: true}
	svc.EnablePersistence(counting, "/ws", store.ModelRef{ConfigID: "c", Provider: "p", Protocol: "openai-chat-completions", Model: "m"}, time.Second)
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "gate the tool"); err != nil {
		t.Fatal(err)
	}
	var events []Event
	deadline := time.After(3 * time.Second)
	collected := false
	for !collected {
		select {
		case ev := <-svc.Events():
			events = append(events, ev)
			if ev.Kind == EventRunCompleted || ev.Kind == EventRunFailed {
				collected = true
			}
		case <-deadline:
			t.Fatal("terminal event missing")
		}
	}
	last := events[len(events)-1]
	if last.Kind != EventRunFailed {
		t.Fatalf("terminal = %+v, want failed", last)
	}
	for _, ev := range events {
		if ev.Kind == EventToolUpdate && ev.Tool.Status != "running" {
			t.Fatalf("tool executed despite checkpoint failure: %+v", ev.Tool)
		}
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunFailed {
		t.Fatalf("runs = %v err=%v", runs, err)
	}
	for _, event := range runs[0].ToolEvents {
		if event.Status == "completed" {
			t.Fatalf("completed tool leaked into terminal record: %+v", event)
		}
	}
}

// TestExitRunBoundBeforeCompletion 守住退出绑定时机：起点登记成功后快照
// 立即指向本轮 run（running），即使执行尚未完成也不沿用上一轮的结论。
func TestExitRunBoundBeforeCompletion(t *testing.T) {
	hang := model.NewFake(model.FakeScript{Stream: []model.FakeStep{
		{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "x"}, Delay: time.Hour},
		{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishStop}},
		{Event: model.Event{Kind: model.EventStreamEnded}},
	}})
	svc, history := newPersistedService(t, hang)
	if err := svc.ConfigureCommands(CommandOptions{History: history, Scope: LocalScope(), Workspace: "/ws", ModelRef: store.ModelRef{ConfigID: "c"}}); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	runID, err := svc.Submit(context.Background(), "still running")
	if err != nil {
		t.Fatal(err)
	}
	// 不等待完成：快照已绑定本轮 run，未确认终态时结论为 unknown。
	captured := svc.CaptureExitState()
	if captured.LastRunID != runID || !captured.HasRun {
		t.Fatalf("captured = %+v, want run %s", captured, runID)
	}
	if status := svc.ExitSummary(captured, true); status != "unknown" {
		t.Fatalf("running status = %q, want unknown", status)
	}
	svc.Cancel()
	terminal(t, drainEvents(t, svc))
}

// TestCaptureExitStateDoesNotBlockOnMutex 守住退出捕获不等待 Service.mu：
// 持锁窗口（模拟慢盘写）内 CaptureExitState 必须立即返回。
func TestCaptureExitStateDoesNotBlockOnMutex(t *testing.T) {
	svc, history := newPersistedService(t, model.NewFake(model.FakeScript{}))
	if err := svc.ConfigureCommands(CommandOptions{History: history, Scope: LocalScope(), Workspace: "/ws", ModelRef: store.ModelRef{ConfigID: "c"}}); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.mu.Lock() // 模拟 run/管理路径持锁等存储
	done := make(chan ExitSnapshot, 1)
	go func() { done <- svc.CaptureExitState() }()
	select {
	case snapshot := <-done:
		if snapshot.SessionID == "" {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("CaptureExitState blocked on service mutex")
	}
	svc.mu.Unlock()
}

// TestCancelledToolCompletionNotStorageFailure 守住取消分类：工具执行中
// 取消后，工具完成回调不再用已取消 ctx 写库，run 终态是 cancelled 而
// 不是 history storage 失败。
func TestCancelledToolCompletionNotStorageFailure(t *testing.T) {
	toolCall := model.ToolCall{ID: "cancel-1", Name: "calculate", Arguments: `{"operation":"add","a":1,"b":2}`}
	first := model.FakeScript{}
	first.Stream = append(first.Stream, model.FakeStep{Event: model.Event{Kind: model.EventToolDelta, ToolIndex: 0, ToolCall: &toolCall}},
		model.FakeStep{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishToolCalls}},
		model.FakeStep{Event: model.Event{Kind: model.EventStreamEnded}})
	second := model.FakeScript{Stream: []model.FakeStep{
		{Event: model.Event{Kind: model.EventTextDelta, TextDelta: "after "}, Delay: time.Hour},
		{Event: model.Event{Kind: model.EventModelDone, FinishReason: model.FinishStop}},
		{Event: model.Event{Kind: model.EventStreamEnded}},
	}}
	svc, history := newPersistedService(t, model.NewFake(first, second))
	defer svc.Close()
	if _, err := svc.Submit(context.Background(), "cancel during tool run"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	sawTool := false
	for !sawTool {
		select {
		case ev := <-svc.Events():
			if ev.Kind == EventToolUpdate && ev.Tool.Status == "completed" {
				sawTool = true
			}
		case <-deadline:
			t.Fatal("tool never completed")
		}
	}
	svc.Cancel()
	last := terminal(t, drainEvents(t, svc))
	if last.Kind != EventRunFailed || last.Err == nil || !errors.Is(last.Err, context.Canceled) {
		t.Fatalf("terminal = %+v, want cancellation without storage failure", last)
	}
	runs, err := history.ListRuns(context.Background(), svc.SessionID())
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunCancelled {
		t.Fatalf("runs = %v err=%v, want cancelled", runs, err)
	}
	// 收尾 flush 用独立期限：取消后的工具终态摘要已进入终态记录。
	found := false
	for _, event := range runs[0].ToolEvents {
		if event.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cancelled run lost completed tool summary: %+v", runs[0].ToolEvents)
	}
}
