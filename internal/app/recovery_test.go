package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// checkpointRecoveryService 构造两个各有检查点的中断 run；原 runs 正文很小，
// 便于区分原始记录读取边界和合并检查点后的会话展示总边界。
func checkpointRecoveryService(t *testing.T, limits store.LoadLimits) (*Service, *store.MemoryStore, store.SessionRecord) {
	t.Helper()
	ctx := context.Background()
	history := store.NewMemory()
	t.Cleanup(func() { _ = history.Close() })
	now := time.Now()
	record := store.SessionRecord{ID: "recovery-session", Workspace: "/ws", Title: "recovery", CreatedAt: now}
	for index, id := range []string{"old-interrupted", "last-interrupted"} {
		// case: 每条检查点分别合法，合并时仍须受会话总读取限额约束。
		if err := history.StartRun(ctx, record, store.RunRecord{ID: id, Input: "task", StartedAt: now.Add(time.Duration(index) * time.Second)}); err != nil {
			t.Fatal(err)
		}
		if err := history.SaveCheckpoint(ctx, id, 1, store.CheckpointPayload{Reply: strings.Repeat("x", 200),
			Tools: []store.CheckpointToolState{{CallID: id + "-tool", Name: "bash", Status: "running"}}}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := history.InterruptRunning(ctx, now); err != nil {
		t.Fatal(err)
	}
	svc := NewService(agent.New(model.NewFake(), "m"))
	t.Cleanup(svc.Close)
	if err := svc.ConfigureCommands(CommandOptions{History: history, Scope: LocalScope(), Workspace: "/ws", LoadLimits: limits,
		BuildRuntime: func(context.Context, store.ModelRef) (*agent.Runtime, error) {
			return agent.New(model.NewFake(), "m"), nil
		}}); err != nil {
		t.Fatal(err)
	}
	return svc, history, record
}

// TestPrepareSessionIncludesEveryInterruptedCheckpoint 守住公共 /resume 路径：
// 所有中断 run 的展示副本补齐检查点，尾候选仍只指向最后一个未完成任务。
func TestPrepareSessionIncludesEveryInterruptedCheckpoint(t *testing.T) {
	svc, history, record := checkpointRecoveryService(t, store.LoadLimits{})
	svc.mu.Lock()
	prepared, err := svc.prepareSessionLocked(context.Background(), record, false)
	svc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.runtime.Close() })
	if len(prepared.runs) != 2 || prepared.runs[0].Reply != strings.Repeat("x", 200) || len(prepared.runs[0].ToolEvents) != 1 {
		t.Fatalf("earlier interrupted checkpoint missing: %+v", prepared.runs)
	}
	if prepared.recovery == nil || prepared.recovery.LastRunID != "last-interrupted" {
		t.Fatalf("tail recovery = %+v", prepared.recovery)
	}
	// 展示合并不改写存储终态，也不把旧中断记录变成成功上下文。
	raw, err := history.ListRuns(context.Background(), record.ID)
	if err != nil || raw[0].Reply != "" || prepared.session.Turns() != 0 {
		t.Fatalf("display recovery changed durable/model history: runs=%+v turns=%d err=%v", raw, prepared.session.Turns(), err)
	}
}

// TestPrepareSessionBoundsCombinedCheckpointBytes 守住累计读取边界：每个
// 检查点虽分别合法，合并后的全部展示正文超限仍整次拒绝，不返回部分会话。
func TestPrepareSessionBoundsCombinedCheckpointBytes(t *testing.T) {
	svc, _, record := checkpointRecoveryService(t, store.LoadLimits{MaxBytes: 400})
	svc.mu.Lock()
	prepared, err := svc.prepareSessionLocked(context.Background(), record, false)
	svc.mu.Unlock()
	if prepared != nil {
		_ = prepared.runtime.Close()
	}
	if !errors.Is(err, store.ErrHistoryLimit) || prepared != nil {
		t.Fatalf("combined recovery limit prepared=%+v err=%v", prepared, err)
	}
}

// recoveryCheckpointStore 在读取边界注入缺失、损坏或归属错误；不修改底层
// 已保存数据，便于断言恢复整体拒绝而不是泄露部分展示结果。
type recoveryCheckpointStore struct {
	store.Store
	load func(context.Context, string) (store.CheckpointRecord, error)
}

func (s recoveryCheckpointStore) LoadCheckpoint(ctx context.Context, runID string) (store.CheckpointRecord, error) {
	return s.load(ctx, runID)
}

// TestLoadRecoveryCheckpointOwnershipAndCorruption 守住逐 run 关联核对：
// 缺失可跳过，跨 run/session 或损坏载荷必须整次拒绝且不暴露部分结果。
func TestLoadRecoveryCheckpointOwnershipAndCorruption(t *testing.T) {
	_, history, session := checkpointRecoveryService(t, store.LoadLimits{})
	ctx := context.Background()
	runs, err := history.ListRuns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*store.CheckpointRecord) error
		want   error
	}{
		// case: 旧 run 没有检查点时保留原展示，继续读取后续 run。
		{"missing", func(*store.CheckpointRecord) error { return store.ErrCheckpointNotFound }, nil},
		// case: 返回其他 run 的检查点不能拼到当前任务。
		{"wrong run", func(cp *store.CheckpointRecord) error { cp.RunID = "other-run"; return nil }, store.ErrCorruptHistory},
		// case: 返回其他会话的检查点不能泄露跨会话正文。
		{"wrong session", func(cp *store.CheckpointRecord) error { cp.SessionID = "other-session"; return nil }, store.ErrCorruptHistory},
		// case: 任何旧检查点损坏都拒绝整体恢复，不能只忽略旧进度。
		{"corrupt", func(*store.CheckpointRecord) error { return store.ErrCorruptHistory }, store.ErrCorruptHistory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := recoveryCheckpointStore{Store: history, load: func(ctx context.Context, runID string) (store.CheckpointRecord, error) {
				cp, err := history.LoadCheckpoint(ctx, runID)
				if err == nil && runID == runs[0].ID {
					err = tc.mutate(&cp)
				}
				return cp, err
			}}
			views, recovery, err := LoadRecovery(ctx, wrapped, session.ID, runs)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || views != nil || recovery != nil {
					t.Fatalf("rejected recovery views=%+v candidate=%+v err=%v", views, recovery, err)
				}
				return
			}
			if err != nil || len(views) != 2 || views[0].Reply != "" || views[1].Reply == "" || recovery == nil {
				t.Fatalf("missing checkpoint recovery views=%+v candidate=%+v err=%v", views, recovery, err)
			}
		})
	}
}

// TestLoadRecoveryLimitsCannotBeRelaxed 守住会话总硬上限与精确字节边界：
// 调用方的大值/零值不能取消 8MiB/10000 限制，显式小值按完整展示裁定。
func TestLoadRecoveryLimitsCannotBeRelaxed(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		runs   []store.RunRecord
		limits store.LoadLimits
	}{
		// case: 放大调用方字节上限仍不能装入超过安全默认的正文。
		{"bytes hard cap", []store.RunRecord{{SessionID: "s", Status: store.RunCompleted, Reply: strings.Repeat("x", (8<<20)+1)}}, store.LoadLimits{MaxBytes: 64 << 20}},
		// case: 放大调用方记录上限仍不能越过默认记录数。
		{"records hard cap", make([]store.RunRecord, 10001), store.LoadLimits{MaxRecords: 20000}},
		// case: 工具条目也占展示记录预算，避免用单 run 藏入大量工具行。
		{"tool entries", []store.RunRecord{{SessionID: "s", Status: store.RunCompleted, ToolEvents: []store.ToolEventRecord{{Name: "read"}, {Name: "bash"}}}}, store.LoadLimits{MaxRecords: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			views, recovery, err := LoadRecovery(ctx, nil, "s", tc.runs, tc.limits)
			if !errors.Is(err, store.ErrHistoryLimit) || views != nil || recovery != nil {
				t.Fatalf("unsafe limit accepted: views=%d candidate=%+v err=%v", len(views), recovery, err)
			}
		})
	}
	// case: 原始 run 各正文与工具 JSON 的总字节恰好等于上限时可恢复。
	run := store.RunRecord{ID: "r", SessionID: "s", Status: store.RunCompleted, Reply: "answer"}
	exactBytes := len(run.ID) + len(run.Status) + len(run.Reply) + len("null")
	if _, _, err := LoadRecovery(ctx, nil, "s", []store.RunRecord{run}, store.LoadLimits{MaxBytes: exactBytes}); err != nil {
		t.Fatalf("exact boundary rejected: %v", err)
	}
	// case: 同一完整展示少一个字节预算时整次拒绝。
	if _, _, err := LoadRecovery(ctx, nil, "s", []store.RunRecord{run}, store.LoadLimits{MaxBytes: exactBytes - 1}); !errors.Is(err, store.ErrHistoryLimit) {
		t.Fatalf("short boundary err=%v", err)
	}
}

// TestRecoveryToolsPairsSerialEvents 守住旧事件数组的语义：同名工具可多次
// 调用，只合并紧邻的启动/终态，不能把较早已完成的 running 误报未确认。
func TestRecoveryToolsPairsSerialEvents(t *testing.T) {
	for _, status := range []store.RunStatus{store.RunFailed, store.RunCancelled, store.RunInterrupted} {
		// case: 失败/取消/中断都保留最后一次无结果调用的不确定性。
		t.Run(string(status), func(t *testing.T) {
			views := RecoveryTools(store.RunRecord{Status: status, ToolEvents: []store.ToolEventRecord{
				{Name: "bash", Status: "running"}, {Name: "bash", Status: "completed", Summary: "done"}, {Name: "bash", Status: "running"},
			}})
			if len(views) != 2 || views[0].Status != "completed" || views[0].Unconfirmed || views[0].Summary != "done" || !views[1].Unconfirmed {
				t.Fatalf("serial tool views = %+v", views)
			}
		})
	}
}
