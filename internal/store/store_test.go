package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// runContract 对同一契约跑内存与 SQLite 双后端，保证语义逐条对齐。
func runContract(t *testing.T, test func(t *testing.T, open func(t *testing.T) Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		test(t, func(t *testing.T) Store { return NewMemory() })
	})
	t.Run("sqlite", func(t *testing.T) {
		test(t, func(t *testing.T) Store {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		})
	})
}

func sampleSession(id, workspace string, at time.Time) SessionRecord {
	return SessionRecord{ID: id, Workspace: workspace, Title: "New conversation",
		ModelRef:  ModelRef{ConfigID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek", Model: "deepseek-flash"},
		CreatedAt: at, LastRunAt: at}
}

// TestStoreStartRunAndDuplicate 守住起点契约：会话按 ID 幂等创建、run ID
// 全局唯一、重复登记返回 ErrDuplicateRun 而不是覆盖。
func TestStoreStartRunAndDuplicate(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		base := time.Now().Add(-time.Hour)
		session := sampleSession("sess-a", "/w", base)
		run := RunRecord{ID: "run-abc", Input: "hello", StartedAt: base.Add(time.Minute)}
		if err := s.StartRun(ctx, session, run); err != nil {
			t.Fatal(err)
		}
		// case: 同一会话第二个 run 起点，不重复建会话。
		second := RunRecord{ID: "run-def", Input: "again", StartedAt: base.Add(2 * time.Minute)}
		if err := s.StartRun(ctx, session, second); err != nil {
			t.Fatal(err)
		}
		if err := s.StartRun(ctx, session, run); !errors.Is(err, ErrDuplicateRun) {
			t.Fatalf("duplicate run: %v", err)
		}
		got, ok, err := s.LatestSession(ctx, "/w")
		// 存储按毫秒精度落盘，断言同口径比较。
		if err != nil || !ok || got.ID != "sess-a" || got.LastRunAt.UnixMilli() != base.Add(2*time.Minute).UnixMilli() {
			t.Fatalf("latest session: %+v ok=%v err=%v", got, ok, err)
		}
		// case: 其他工作区的会话不参与匹配。
		if _, ok, _ := s.LatestSession(ctx, "/other"); ok {
			t.Fatal("workspace isolation failed")
		}
	})
}

// TestStoreLatestSessionOrdering 守住 -c 选择口径：只按最近 run 时间排序，
// 空会话（从未登记 run）不存在于存储中，不可能抢占"最近"。
func TestStoreLatestSessionOrdering(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		base := time.Now().Add(-time.Hour)
		for i, id := range []string{"sess-old", "sess-new"} {
			run := RunRecord{ID: "run-" + id, Input: "hi", StartedAt: base.Add(time.Duration(i) * time.Minute)}
			if err := s.StartRun(ctx, sampleSession(id, "/w", base), run); err != nil {
				t.Fatal(err)
			}
		}
		got, ok, err := s.LatestSession(ctx, "/w")
		if err != nil || !ok || got.ID != "sess-new" {
			t.Fatalf("latest = %+v ok=%v err=%v", got, ok, err)
		}
	})
}

// TestStoreUsageDedup 守住用量口径：快照按 call ID 整体替换不累加，
// 未登记的调用拒绝补记，未知调用恢复后仍是未知而非已知零。
func TestStoreUsageDedup(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		at := time.Now().Add(-time.Minute)
		session := sampleSession("sess-a", "/w", at)
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-a", Input: "q", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		call := ModelCallRecord{ID: "run-a/model-1", RunID: "run-a", SessionID: "sess-a", Sequence: 1, SavedAt: at}
		if err := s.StartModelCall(ctx, call); err != nil {
			t.Fatal(err)
		}
		if err := s.StartModelCall(ctx, call); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveUsage(ctx, "run-a/model-1", FromUsage(model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}), at); err != nil {
			t.Fatal(err)
		}
		cached := int64(4)
		if err := s.SaveUsage(ctx, "run-a/model-1", FromUsage(model.Usage{OK: true, PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28, CachedPromptTokens: &cached}), at); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveUsage(ctx, "run-a/model-9", FromUsage(model.Usage{OK: true}), at); !errors.Is(err, ErrUnknownCall) {
			t.Fatalf("unknown call: %v", err)
		}
		calls, err := s.LoadCalls(ctx, "sess-a")
		if err != nil || len(calls) != 1 {
			t.Fatalf("calls = %v err = %v", calls, err)
		}
		usage := calls[0].Usage
		if usage == nil || usage.PromptTokens != 20 || usage.CachedPromptTokens == nil || *usage.CachedPromptTokens != 4 {
			t.Fatalf("usage snapshot not replaced: %+v", usage)
		}
		// case: 从未保存快照的调用保持未知（dispatch_unknown 场景），不得补零。
		unknown := ModelCallRecord{ID: "run-a/model-2", RunID: "run-a", SessionID: "sess-a", Sequence: 2, SavedAt: at}
		if err := s.StartModelCall(ctx, unknown); err != nil {
			t.Fatal(err)
		}
		calls, err = s.LoadCalls(ctx, "sess-a")
		if err != nil || len(calls) != 2 || calls[1].Usage != nil {
			t.Fatalf("unknown call became known: %+v err=%v", calls, err)
		}
	})
}

// TestStoreCompleteRunTransaction 守住成功事务：完整消息组、终态与活动时间
// 一次提交；重复提交同一 run 幂等，turn 序号跨 run 递增。
func TestStoreCompleteRunTransaction(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		at := time.Now().Add(-time.Minute)
		session := sampleSession("sess-a", "/w", at)
		turn := []model.Message{
			{Role: model.RoleUser, Content: "hi"},
			{Role: model.RoleAssistant, Content: "hello", ToolCalls: []model.ToolCall{{ID: "t1", Name: "read", Arguments: "{}"}}},
			{Role: model.RoleTool, ToolCallID: "t1", Content: `{"ok":true}`},
			{Role: model.RoleAssistant, Content: "done"},
		}
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-a", Input: "hi", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		finished := at.Add(10 * time.Second)
		if err := s.CompleteRun(ctx, RunRecord{ID: "run-a", SessionID: "sess-a", Reply: "done", StartedAt: at, FinishedAt: finished}, turn); err != nil {
			t.Fatal(err)
		}
		// case: 重复提交同一 run 保留第一次结果，不重复计数。
		if err := s.CompleteRun(ctx, RunRecord{ID: "run-a", SessionID: "sess-a", Reply: "dup", StartedAt: at, FinishedAt: finished.Add(time.Hour)}, turn); err != nil {
			t.Fatal(err)
		}
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-b", Input: "next", StartedAt: finished}); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, RunRecord{ID: "run-b", SessionID: "sess-a", Reply: "ok", StartedAt: finished, FinishedAt: finished.Add(5 * time.Second)},
			[]model.Message{{Role: model.RoleUser, Content: "next"}, {Role: model.RoleAssistant, Content: "ok"}}); err != nil {
			t.Fatal(err)
		}
		messages, seq, err := s.LoadTurns(ctx, "sess-a")
		if err != nil || seq != 3 {
			t.Fatalf("seq=%d err=%v", seq, err)
		}
		if len(messages) != 6 || messages[1].ToolCalls[0].ID != "t1" || messages[5].Content != "ok" {
			t.Fatalf("messages = %+v", messages)
		}
		got, _, err := s.LatestSession(ctx, "/w")
		if err != nil || got.LastRunAt.UnixMilli() != finished.Add(5*time.Second).UnixMilli() {
			t.Fatalf("activity time = %v err=%v", got.LastRunAt, err)
		}
	})
}

// TestStoreFinishRunKeepsFirstOutcome 守住终态竞争规则：失败/取消终态不写
// 成功轮次；已完成事务不被后续 FinishRun 改写。
func TestStoreFinishRunKeepsFirstOutcome(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		at := time.Now().Add(-time.Minute)
		session := sampleSession("sess-a", "/w", at)
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-fail", Input: "x", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, RunRecord{ID: "run-fail", SessionID: "sess-a", Status: RunCancelled, Reply: "partial", StartedAt: at, FinishedAt: at.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, RunRecord{ID: "run-fail", SessionID: "sess-a", Status: RunCompleted, StartedAt: at, FinishedAt: at.Add(2 * time.Second)}); err != nil {
			t.Fatal(err)
		}
		messages, seq, err := s.LoadTurns(ctx, "sess-a")
		if err != nil || len(messages) != 0 || seq != 1 {
			t.Fatalf("failed run wrote turns: %+v seq=%d err=%v", messages, seq, err)
		}
		// case: 成功事务提交后再收到取消终态，保留成功结果。
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-ok", Input: "y", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		turn := []model.Message{{Role: model.RoleUser, Content: "y"}, {Role: model.RoleAssistant, Content: "z"}}
		if err := s.CompleteRun(ctx, RunRecord{ID: "run-ok", SessionID: "sess-a", StartedAt: at, FinishedAt: at}, turn); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, RunRecord{ID: "run-ok", SessionID: "sess-a", Status: RunCancelled, StartedAt: at, FinishedAt: at.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		messages, _, err = s.LoadTurns(ctx, "sess-a")
		if err != nil || len(messages) != 2 {
			t.Fatalf("completed run was rewritten: %+v err=%v", messages, err)
		}
	})
}

// TestStoreInterruptRunning 守住崩溃恢复标记：遗留 running 全部标记
// interrupted，已终态记录不受影响。
func TestStoreInterruptRunning(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		at := time.Now().Add(-time.Minute)
		session := sampleSession("sess-a", "/w", at)
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-live", Input: "x", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.StartRun(ctx, session, RunRecord{ID: "run-done", Input: "y", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, RunRecord{ID: "run-done", SessionID: "sess-a", Status: RunFailed, Error: "boom", StartedAt: at, FinishedAt: at}); err != nil {
			t.Fatal(err)
		}
		count, err := s.InterruptRunning(ctx, at.Add(time.Minute))
		if err != nil || count != 1 {
			t.Fatalf("interrupt count=%d err=%v", count, err)
		}
		again, err := s.InterruptRunning(ctx, at.Add(2*time.Minute))
		if err != nil || again != 0 {
			t.Fatalf("second interrupt count=%d err=%v", again, err)
		}
	})
}

// TestSQLiteLockExclusivity 守住整库进程独占：持有期间第二个 OpenSQLite
// 被明确拒绝，Close（或崩溃）后新进程可取锁。
func TestSQLiteLockExclusivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	first, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenSQLite(path); !errors.Is(err, ErrHistoryInUse) {
		t.Fatalf("second open: %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	_ = second.Close()
}

// TestSQLiteSchemaNewerRejected 守住迁移边界：比程序新的 schema 版本拒绝
// 打开，不删库重建；旧版本前向迁移后可正常使用。
func TestSQLiteSchemaNewerRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err = OpenSQLite(path); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("newer schema: %v", err)
	}
	// 数据文件在拒绝路径上必须原样保留。
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteCorruptFileRejected 守住损坏边界：非 SQLite 文件明确报错，
// 不静默降级为空历史。
func TestSQLiteCorruptFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.db")
	if err := os.WriteFile(path, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLite(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

// TestStoredUsageRoundTrip 守住用量持久化格式：CachedPromptTokens 的
// nil（未知）与非 nil 双向还原，不把未知折叠成零。
func TestStoredUsageRoundTrip(t *testing.T) {
	cached := int64(7)
	original := model.Usage{OK: true, PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7, CachedPromptTokens: &cached}
	restored := FromUsage(original).Usage()
	if restored.CachedPromptTokens == nil || *restored.CachedPromptTokens != 7 || restored.TotalTokens != 7 {
		t.Fatalf("round trip lost cached tokens: %+v", restored)
	}
	unknown := FromUsage(model.Usage{}).Usage()
	if unknown.OK || unknown.CachedPromptTokens != nil {
		t.Fatalf("unknown usage mutated: %+v", unknown)
	}
}
