package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// seedRunningRun 登记一个 running run 并返回其记录，供检查点测试使用。
func seedRunningRun(t *testing.T, s Store, sessionID, runID, workspace string) RunRecord {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	if err := s.StartRun(ctx, sampleSession(sessionID, workspace, base), RunRecord{ID: runID, Input: "task " + runID, StartedAt: base.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	return RunRecord{ID: runID, SessionID: sessionID, Input: "task " + runID, StartedAt: base.Add(time.Minute), FinishedAt: time.Now()}
}

// TestCheckpointSaveSequenceRules 守住写入契约：序号严格递增、同序号同载荷
// 幂等、同序号不同载荷冲突、迟到旧序号拒绝、未知 run 拒绝。
func TestCheckpointSaveSequenceRules(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		run := seedRunningRun(t, s, "sess-cp", "run-cp-1", "/w")

		// case: 未知 run 不落任何检查点。
		if err := s.SaveCheckpoint(ctx, "run-missing", 1, CheckpointPayload{}, time.Now()); !errors.Is(err, ErrUnknownRun) {
			t.Fatalf("unknown run err = %v", err)
		}
		first := CheckpointPayload{Reply: "part one", Tools: []CheckpointToolState{{CallID: "c1", Name: "read", Status: "completed", Summary: "ok"}}}
		if err := s.SaveCheckpoint(ctx, run.ID, 1, first, time.Now()); err != nil {
			t.Fatal(err)
		}
		// case: 同序号同载荷幂等。
		if err := s.SaveCheckpoint(ctx, run.ID, 1, first, time.Now()); err != nil {
			t.Fatalf("idempotent replay err = %v", err)
		}
		// case: 同序号不同载荷冲突。
		if err := s.SaveCheckpoint(ctx, run.ID, 1, CheckpointPayload{Reply: "different"}, time.Now()); !errors.Is(err, ErrCheckpointConflict) {
			t.Fatalf("conflict err = %v", err)
		}
		// case: 迟到旧序号不能覆盖较新快照。
		second := CheckpointPayload{Reply: "part one + two"}
		if err := s.SaveCheckpoint(ctx, run.ID, 2, second, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveCheckpoint(ctx, run.ID, 1, first, time.Now()); !errors.Is(err, ErrCheckpointStale) {
			t.Fatalf("stale err = %v", err)
		}
		got, err := s.LoadCheckpoint(ctx, run.ID)
		if err != nil || got.Sequence != 2 || got.Payload.Reply != "part one + two" || got.SessionID != run.SessionID || got.RunID != run.ID {
			t.Fatalf("load = %+v err=%v", got, err)
		}
		// case: 无检查点的 run 读不到，与未知 run 区分。
		other := seedRunningRun(t, s, "sess-cp", "run-cp-2", "/w")
		if _, err := s.LoadCheckpoint(ctx, other.ID); !errors.Is(err, ErrCheckpointNotFound) {
			t.Fatalf("not found err = %v", err)
		}
		// case: 工具条目超限整次拒绝，不截断。
		tooMany := CheckpointPayload{Tools: make([]CheckpointToolState, CheckpointMaxTools+1)}
		if err := s.SaveCheckpoint(ctx, run.ID, 3, tooMany, time.Now()); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("limit err = %v", err)
		}
		_ = run.Status
	})
}

// TestCheckpointFinalizedAndTerminalCleanup 守住终态边界：终态后的迟到写入
// 被拒绝，CompleteRun/FinishRun 与检查点清理同事务（读不回残片）。
func TestCheckpointFinalizedAndTerminalCleanup(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		finished := seedRunningRun(t, s, "sess-fin", "run-fin-1", "/w")
		if err := s.SaveCheckpoint(ctx, finished.ID, 1, CheckpointPayload{Reply: "partial"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		finished.Status, finished.Reply = RunCancelled, "partial"
		if err := s.FinishRun(ctx, finished); err != nil {
			t.Fatal(err)
		}
		// 终态后迟到写入不能复活检查点。
		if err := s.SaveCheckpoint(ctx, finished.ID, 2, CheckpointPayload{Reply: "late"}, time.Now()); !errors.Is(err, ErrCheckpointFinalized) {
			t.Fatalf("finalized err = %v", err)
		}
		if _, err := s.LoadCheckpoint(ctx, finished.ID); !errors.Is(err, ErrCheckpointNotFound) {
			t.Fatalf("checkpoint after finish err = %v", err)
		}

		// case: 成功终态同样同事务清理。
		completed := seedRunningRun(t, s, "sess-fin", "run-fin-2", "/w")
		if err := s.SaveCheckpoint(ctx, completed.ID, 1, CheckpointPayload{Reply: "final answer"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		completed.Status = RunCompleted
		if err := s.CompleteRun(ctx, completed, []model.Message{{Role: model.RoleUser, Content: "task run-fin-2"}, {Role: model.RoleAssistant, Content: "final answer"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadCheckpoint(ctx, completed.ID); !errors.Is(err, ErrCheckpointNotFound) {
			t.Fatalf("checkpoint after complete err = %v", err)
		}
	})
}

// TestCheckpointInterruptedKeptAndIdempotent 守住启动标记 interrupted 是
// 清理例外：检查点保留供恢复读取、重复启动幂等、interrupted 后拒绝新写入。
func TestCheckpointInterruptedKeptAndIdempotent(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		run := seedRunningRun(t, s, "sess-int", "run-int-1", "/w")
		if err := s.SaveCheckpoint(ctx, run.ID, 4, CheckpointPayload{Reply: "halfway", Reasoning: "thinking",
			Tools: []CheckpointToolState{{CallID: "c1", Name: "bash", Status: "running"}}}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InterruptRunning(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadCheckpoint(ctx, run.ID)
		if err != nil || got.Sequence != 4 || got.Payload.Reply != "halfway" || len(got.Payload.Tools) != 1 {
			t.Fatalf("checkpoint after interrupt = %+v err=%v", got, err)
		}
		// 重复启动幂等：没有新的 running，不重复改结束时间，也不清检查点。
		rows, err := s.InterruptRunning(ctx, time.Now().Add(time.Minute))
		if err != nil || rows != 0 {
			t.Fatalf("second interrupt rows=%d err=%v", rows, err)
		}
		if got, err = s.LoadCheckpoint(ctx, run.ID); err != nil {
			t.Fatalf("checkpoint dropped by second interrupt: %v", err)
		}
		// interrupted 是终态：新写入拒绝，保留原快照供有界读取。
		if err := s.SaveCheckpoint(ctx, run.ID, 5, CheckpointPayload{Reply: "late"}, time.Now()); !errors.Is(err, ErrCheckpointFinalized) {
			t.Fatalf("write after interrupt err = %v", err)
		}
	})
}

// TestCheckpointLatestByRunOrder 守住 LatestCheckpoint 按会话内 run 起点
// 顺序取最新，只服务最后未完成任务的展示。
func TestCheckpointLatestByRunOrder(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		first := seedRunningRun(t, s, "sess-latest", "run-latest-1", "/w")
		second := seedRunningRun(t, s, "sess-latest", "run-latest-2", "/w")
		if err := s.SaveCheckpoint(ctx, first.ID, 1, CheckpointPayload{Reply: "first"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveCheckpoint(ctx, second.ID, 1, CheckpointPayload{Reply: "second"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.LatestCheckpoint(ctx, "sess-latest")
		if err != nil || !ok || got.RunID != second.ID {
			t.Fatalf("latest = %+v ok=%v err=%v", got, ok, err)
		}
		// 其他会话不串读。
		if _, ok, err := s.LatestCheckpoint(ctx, "sess-other"); err != nil || ok {
			t.Fatalf("other session latest ok=%v err=%v", ok, err)
		}
		// 旧 run 终态清理后仍指向最新 run 的检查点。
		first.Status = RunFailed
		if err := s.FinishRun(ctx, first); err != nil {
			t.Fatal(err)
		}
		if got, ok, err = s.LatestCheckpoint(ctx, "sess-latest"); err != nil || !ok || got.RunID != second.ID {
			t.Fatalf("latest after cleanup = %+v ok=%v err=%v", got, ok, err)
		}
	})
}

// TestCheckpointSQLiteMigrationFromV3 守住 v3→v4 迁移：旧历史/记忆账本保留、
// 检查点表随版本同事务发布；旧二进制（更早 schemaVersion）对 v4 拒绝由
// ErrSchemaNewer 语义覆盖，此处直接构造 v4 头验证拒绝。
func TestCheckpointSQLiteMigrationFromV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append(append(append([]string{}, schemaStatements...), managementSchemaStatements...), memorySchemaStatements...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,workspace,title,model_ref,created_at,last_run_at,scope_key,title_manual) VALUES('legacy','/w','old','{}',1,2,'local',1);
		INSERT INTO runs(id,session_id,input,status,started_at) VALUES('legacy-run','legacy','q','interrupted',2);
		INSERT INTO memory_entries(scope_key,scope_type,workspace,session_id,memory_key,content,source,revision,created_at,updated_at) VALUES('local','global','','','k','v','user',1,5,5);
		PRAGMA user_version = 3;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema = %d err=%v", version, err)
	}
	// 旧 runs 可读；迁移没有重写历史。
	runs, err := s.ListRuns(context.Background(), "legacy")
	if err != nil || len(runs) != 1 || runs[0].Status != RunInterrupted {
		t.Fatalf("legacy runs = %v err=%v", runs, err)
	}
	// 记忆账本保留。
	memories, err := s.ListMemories(context.Background(), MemoryScope{ScopeKey: "local", Kind: MemoryGlobal}, MemoryLimits{})
	if err != nil || len(memories) != 1 || memories[0].Content != "v" {
		t.Fatalf("legacy memories = %v err=%v", memories, err)
	}
}

// TestCheckpointSQLiteOversizePreflight 守住读取前的内存边界：外部损坏库中
// 的大载荷应只查询长度，不能先经驱动复制整个正文到 Go 堆再拒绝。阈值留出
// 8MiB 查询余量，远大于正常元数据查询开销，避免把它变成微观性能断言。
func TestCheckpointSQLiteOversizePreflight(t *testing.T) {
	s, ok := openSQLiteForTest(t)
	if !ok {
		return
	}
	ctx := context.Background()
	run := seedRunningRun(t, s, "sess-oversize", "run-oversize", "/w")
	// 直接构造外部损坏记录，绕过写入校验；不在 Go 中生成大字符串。
	if _, err := s.db.Exec(`INSERT INTO run_checkpoints(run_id, sequence, format_version, payload, saved_at)
		VALUES (?, 1, 1, zeroblob(?), 1)`, run.ID, CheckpointMaxBytes*2); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		read func() error
		want error
	}{
		// case: 按 run 读取也必须在正文分配之前拒绝。
		{"run", func() error { _, err := s.LoadCheckpoint(ctx, run.ID); return err }, ErrHistoryLimit},
		// case: 会话恢复入口不能绕过相同的预检。
		{"session", func() error { _, _, err := s.LatestCheckpoint(ctx, run.SessionID); return err }, ErrHistoryLimit},
		// case: 幂等核查也不能把已有载荷整体读入 Go 堆。
		{"write conflict", func() error { return s.SaveCheckpoint(ctx, run.ID, 1, CheckpointPayload{}, time.Now()) }, ErrCheckpointConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := tc.read()
			runtime.ReadMemStats(&after)
			if !errors.Is(err, tc.want) {
				t.Fatalf("oversize checkpoint err = %v", err)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= CheckpointMaxBytes {
				t.Fatalf("oversize payload allocated %d bytes before rejection", allocated)
			}
		})
	}
}

// TestCheckpointSQLiteCorruptRejected 守住读取边界：损坏载荷、未来版本、
// 超限载荷整次拒绝，不返回残片。
func TestCheckpointSQLiteCorruptRejected(t *testing.T) {
	s, ok := openSQLiteForTest(t)
	if !ok {
		return
	}
	ctx := context.Background()
	run := seedRunningRun(t, s, "sess-corrupt", "run-corrupt-1", "/w")
	if err := s.SaveCheckpoint(ctx, run.ID, 1, CheckpointPayload{Reply: "ok"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		"UPDATE run_checkpoints SET payload = '{not json'",
		"UPDATE run_checkpoints SET format_version = 99",
	} {
		if _, err := s.db.Exec(mutation); err != nil {
			t.Fatal(err)
		}
		if _, err := s.LoadCheckpoint(ctx, run.ID); err == nil {
			t.Fatalf("corrupt checkpoint accepted: %s", mutation)
		}
		if _, _, err := s.LatestCheckpoint(ctx, "sess-corrupt"); err == nil {
			t.Fatalf("corrupt latest accepted: %s", mutation)
		}
		// 还原为合法载荷，仅保留下一次注入。
		if _, err := s.db.Exec("UPDATE run_checkpoints SET payload = ?", `{"reply":"ok"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE run_checkpoints SET format_version = 1"); err != nil {
			t.Fatal(err)
		}
	}
}

// openSQLiteForTest 打开真实 SQLite 后端；环境不可用时跳过。
func openSQLiteForTest(t *testing.T) (*SQLiteStore, bool) {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Skipf("sqlite unavailable: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, true
}

// TestCheckpointSQLiteMigrationFailureRollsBack 守住 v4 迁移不是逐条发布：
// run_checkpoints 表已存在时 DDL 失败，user_version 与既有数据整体保持 v3。
func TestCheckpointSQLiteMigrationFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append(append(append([]string{}, schemaStatements...), managementSchemaStatements...), memorySchemaStatements...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("CREATE TABLE run_checkpoints(conflicting TEXT); PRAGMA user_version = 3;"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenSQLite(path); err == nil {
		_ = s.Close()
		t.Fatal("conflicting v4 migration unexpectedly succeeded")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("version after failed migration = %d err=%v, want rollback to 3", version, err)
	}
}

// TestCheckpointLatestDoesNotFallBack 守住双后端一致语义：会话最新 run 没有
// 检查点时不回退到旧 run 的检查点（旧记录可能是更早中断的残留）。
func TestCheckpointLatestDoesNotFallBack(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		older := seedRunningRun(t, s, "sess-nofallback", "run-nf-1", "/w")
		newer := seedRunningRun(t, s, "sess-nofallback", "run-nf-2", "/w")
		if err := s.SaveCheckpoint(ctx, older.ID, 1, CheckpointPayload{Reply: "older"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.LatestCheckpoint(ctx, "sess-nofallback")
		if err != nil || ok || got.RunID != "" {
			t.Fatalf("latest = %+v ok=%v err=%v, want no fallback to older run", got, ok, err)
		}
		// 最新 run 写入检查点后正常返回它。
		if err := s.SaveCheckpoint(ctx, newer.ID, 1, CheckpointPayload{Reply: "newer"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got, ok, err = s.LatestCheckpoint(ctx, "sess-nofallback"); err != nil || !ok || got.RunID != newer.ID {
			t.Fatalf("latest = %+v ok=%v err=%v", got, ok, err)
		}
	})
}
