package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// unknownCheckpointCommitStore 在底层提交后丢弃确认，用真实后端结果
// 验证 app 的提交未知核查，不能用伪造 GetRunState 错误代替存储校验。
type unknownCheckpointCommitStore struct{ store.Store }

func (s *unknownCheckpointCommitStore) CompleteRun(ctx context.Context, run store.RunRecord, turn []model.Message) error {
	if err := s.Store.CompleteRun(ctx, run, turn); err != nil {
		return err
	}
	return context.DeadlineExceeded
}

// TestCheckpointUnknownCommitRequiresCleanup 守住提交未知后必须确认三者
// 一致：即便 completed 和成功 turn 已提交，只要检查点仍在就冻结新任务，
// 不得报告保存成功或把未确认的成功轮次追加到内存。
func TestCheckpointUnknownCommitRequiresCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	history, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close() })
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	// 外部损坏注入：忽略清理语句，制造终态/turn 完整但检查点残留的库。
	if _, err := raw.Exec("CREATE TRIGGER preserve_checkpoint BEFORE DELETE ON run_checkpoints BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	client := model.NewFake(streamReply("saved answer"))
	service := NewService(agent.New(client, "test-model"))
	t.Cleanup(service.Close)
	service.EnablePersistence(&unknownCheckpointCommitStore{Store: history}, "/w", store.ModelRef{}, time.Second)
	if _, err := service.Submit(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	last := terminal(t, drainEvents(t, service))
	if last.Kind != EventRunFailed || !errors.Is(last.Err, ErrHistoryUncertain) {
		t.Fatalf("terminal = %+v, want unknown commit with leftover checkpoint", last)
	}
	if service.Session().Turns() != 0 {
		t.Fatal("unconfirmed completion appended to in-memory history")
	}
	if _, err := service.Submit(context.Background(), "next task"); !errors.Is(err, ErrHistoryUncertain) {
		t.Fatalf("next task err = %v, want frozen admission", err)
	}
	if captured := service.CaptureExitState(); service.ExitSummary(captured, true) != "unknown" {
		t.Fatalf("exit summary = %q, want unknown", service.ExitSummary(captured, true))
	}
}
