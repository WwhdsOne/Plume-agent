package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// TestRunStateRejectsTerminalCheckpoint 守住提交未知核查必须覆盖终态、
// 成功 turns 和检查点清理；即便 completed 与完整 turn 一致，残留检查点
// 仍表示事务结果不完整，不能确认已保存或放行后续任务。
func TestRunStateRejectsTerminalCheckpoint(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		for _, status := range []RunStatus{RunCompleted, RunFailed, RunCancelled} {
			// case: 正常终态都必须清理检查点，不能只检查成功状态。
			t.Run(string(status), func(t *testing.T) {
				s := open(t)
				ctx := context.Background()
				run := seedRunningRun(t, s, "confirm-session", "confirm-run", "/w")
				run.Status = status
				var err error
				if status == RunCompleted {
					err = s.CompleteRun(ctx, run, []model.Message{{Role: model.RoleUser, Content: run.Input}, {Role: model.RoleAssistant, Content: "answer"}})
				} else {
					err = s.FinishRun(ctx, run)
				}
				if err != nil {
					t.Fatal(err)
				}
				if state, err := s.GetRunState(ctx, run.ID); err != nil || state.Status != status || state.HasTurn != (status == RunCompleted) {
					t.Fatalf("valid terminal state = %+v err=%v", state, err)
				}

				// 外部损坏模拟终态和 turn 已出现、检查点却未清理的部分结果。
				// 只插入最小合法载荷，错误必须来自终态/清理不一致。
				switch backend := s.(type) {
				case *MemoryStore:
					backend.mu.Lock()
					backend.checkpoints[run.ID] = &CheckpointRecord{RunID: run.ID, SessionID: run.SessionID,
						Sequence: 1, FormatVersion: CheckpointFormatVersion, Payload: CheckpointPayload{Reply: "partial"}, SavedAt: time.Now()}
					backend.mu.Unlock()
				case *SQLiteStore:
					if _, err := backend.db.ExecContext(ctx, "INSERT INTO run_checkpoints (run_id, sequence, format_version, payload, saved_at) VALUES (?, ?, ?, ?, ?)",
						run.ID, 1, CheckpointFormatVersion, `{"reply":"partial"}`, time.Now().UnixMilli()); err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unexpected backend %T", s)
				}
				if state, err := s.GetRunState(ctx, run.ID); !errors.Is(err, ErrCorruptHistory) {
					t.Fatalf("terminal with leftover checkpoint confirmed: state=%+v err=%v", state, err)
				}
			})
		}
	})
}

// TestRunStateAllowsInterruptedCheckpoint 守住崩溃恢复例外：启动标记
// interrupted 后保留检查点是完整状态，核查不得误报损坏或清掉展示片段。
func TestRunStateAllowsInterruptedCheckpoint(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		ctx := context.Background()
		run := seedRunningRun(t, s, "interrupted-session", "interrupted-run", "/w")
		if err := s.SaveCheckpoint(ctx, run.ID, 1, CheckpointPayload{Reply: "saved progress"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InterruptRunning(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		state, err := s.GetRunState(ctx, run.ID)
		if err != nil || state.SessionID != run.SessionID || state.Status != RunInterrupted || state.HasTurn {
			t.Fatalf("interrupted state = %+v err=%v", state, err)
		}
		if checkpoint, err := s.LoadCheckpoint(ctx, run.ID); err != nil || checkpoint.Payload.Reply != "saved progress" {
			t.Fatalf("interrupted checkpoint = %+v err=%v", checkpoint, err)
		}
	})
}
