package app

import (
	"context"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/store"
)

// TestExitSnapshotAfterNewSession 守住有任务会话切到未登记空会话后，
// 退出提示不能沿用旧 hasRun，承诺一个无法跨进程恢复的新会话。
func TestExitSnapshotAfterNewSession(t *testing.T) {
	commandBackends(t, func(t *testing.T, history store.Store) {
		service := commandService(t, history, LocalScope(), commandLoopFake())
		if _, err := service.Submit(context.Background(), "saved task"); err != nil {
			t.Fatal(err)
		}
		terminal(t, drainEvents(t, service))
		before := service.CaptureExitState()
		if !before.HasRun {
			t.Fatalf("registered task snapshot = %+v", before)
		}
		result := service.HandleInput(context.Background(), CommandRequest{Text: "/new", Entrance: EntranceTUI})
		if result.Code != "ok" {
			t.Fatal(result)
		}
		captured := service.CaptureExitState()
		if captured.SessionID != result.SessionID || captured.HasRun || captured.LastRunID != "" {
			t.Fatalf("new empty session snapshot = %+v, want id=%s and no run", captured, result.SessionID)
		}
		if status := service.ExitSummary(captured, true); status != "unknown" {
			t.Fatalf("empty session summary = %q, want unknown", status)
		}
	})
}

// TestExitSnapshotAfterResumeFromEmpty 守住空会话恢复已有任务会话后，
// 即便不再发送新任务，退出仍能用恢复后的身份给出保存结论。
func TestExitSnapshotAfterResumeFromEmpty(t *testing.T) {
	commandBackends(t, func(t *testing.T, history store.Store) {
		source := commandService(t, history, LocalScope(), commandLoopFake())
		if _, err := source.Submit(context.Background(), "saved task"); err != nil {
			t.Fatal(err)
		}
		terminal(t, drainEvents(t, source))
		target := commandService(t, history, LocalScope(), commandLoopFake())
		result := target.HandleInput(context.Background(), CommandRequest{Text: "/resume " + source.SessionID(), Entrance: EntranceTUI})
		if result.Code != "ok" {
			t.Fatal(result)
		}
		captured := target.CaptureExitState()
		if captured.SessionID != source.SessionID() || !captured.HasRun || captured.LastRunID != "" {
			t.Fatalf("resumed session snapshot = %+v, want stored session with a run", captured)
		}
		if status := target.ExitSummary(captured, true); status != "saved" {
			t.Fatalf("resumed session summary = %q, want saved", status)
		}
	})
}

// TestExitSnapshotAfterAdoptAndConfigure 守住 -c 的实际装配顺序：先接管、
// 后配置指令；配置查明已有任务后必须刷新退出快照，不要求再发送一次。
func TestExitSnapshotAfterAdoptAndConfigure(t *testing.T) {
	commandBackends(t, func(t *testing.T, history store.Store) {
		source := commandService(t, history, LocalScope(), commandLoopFake())
		if _, err := source.Submit(context.Background(), "saved task"); err != nil {
			t.Fatal(err)
		}
		terminal(t, drainEvents(t, source))
		messages, nextSeq, err := history.LoadTurns(context.Background(), source.SessionID())
		if err != nil {
			t.Fatal(err)
		}
		target := NewService(agent.New(commandLoopFake(), "test-model"))
		t.Cleanup(target.Close)
		if err := target.AdoptSession(source.SessionID(), RestoreSession(messages, nextSeq-1, nil, "", time.Now())); err != nil {
			t.Fatal(err)
		}
		if err := target.ConfigureCommands(CommandOptions{History: history, Scope: LocalScope(), Workspace: "/same-workspace",
			ModelRef: store.ModelRef{ConfigID: "test", Provider: "fake", Model: "test-model"}}); err != nil {
			t.Fatal(err)
		}
		captured := target.CaptureExitState()
		if captured.SessionID != source.SessionID() || !captured.HasRun || captured.LastRunID != "" {
			t.Fatalf("adopted session snapshot = %+v, want stored session with a run", captured)
		}
		if status := target.ExitSummary(captured, true); status != "saved" {
			t.Fatalf("adopted session summary = %q, want saved", status)
		}
	})
}

// TestExitSummaryRejectsChangedIdentity 守住冻结身份后不能借用别的会话、
// 重置后的 run 或缺失状态给出 saved；无新 run 的结论也必须匹配会话 ID。
func TestExitSummaryRejectsChangedIdentity(t *testing.T) {
	for _, scenario := range []string{"session_changed", "restored_session_changed", "last_run_cleared", "state_missing"} {
		// case: 所有冻结身份与当前原子状态不一致的情况都返回 unknown。
		t.Run(scenario, func(t *testing.T) {
			service := commandService(t, store.NewMemory(), LocalScope(), commandLoopFake())
			if _, err := service.Submit(context.Background(), "saved task"); err != nil {
				t.Fatal(err)
			}
			terminal(t, drainEvents(t, service))
			if scenario == "restored_session_changed" {
				if err := service.AdoptSession(service.SessionID(), RestoreSession(nil, 0, nil, "", time.Now())); err != nil {
					t.Fatal(err)
				}
			}
			captured := service.CaptureExitState()
			switch scenario {
			case "session_changed", "restored_session_changed":
				result := service.HandleInput(context.Background(), CommandRequest{Text: "/new", Entrance: EntranceTUI})
				if result.Code != "ok" {
					t.Fatal(result)
				}
			case "last_run_cleared":
				if err := service.AdoptSession(service.SessionID(), RestoreSession(nil, 0, nil, "", time.Now())); err != nil {
					t.Fatal(err)
				}
			case "state_missing":
				service.exitSnapshot.Store(nil)
			}
			if status := service.ExitSummary(captured, true); status != "unknown" {
				t.Fatalf("summary after %s = %q, want unknown for frozen %+v", scenario, status, captured)
			}
		})
	}
}
