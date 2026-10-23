package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// 完成耗时只统计成功run，失败/取消不计；权限检查与正文分页保持同一边界。
func TestLoadTimingOnlyCountsCompletedVisibleRuns(t *testing.T) {
	for _, name := range []string{"memory", "sqlite"} {
		// case: 两个后端以持久化毫秒精度给出同一汇总。
		t.Run(name, func(t *testing.T) {
			var backend Store = NewMemory()
			if name == "sqlite" {
				var err error
				backend, err = OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = backend.Close() })
			management := backend.(ManagementStore)
			at := time.Unix(1700000000, 0)
			session := SessionRecord{ID: "timing-session", ScopeKey: "owner", Workspace: "/w", CreatedAt: at, Title: "timing"}
			for i, status := range []RunStatus{RunCompleted, RunFailed, RunCancelled, RunCompleted} {
				run := RunRecord{ID: string(rune('a' + i)), SessionID: session.ID, Input: "q", StartedAt: at.Add(time.Duration(i) * time.Second)}
				if err := backend.StartRun(context.Background(), session, run); err != nil {
					t.Fatal(err)
				}
				run.Status, run.FinishedAt, run.Reply = status, run.StartedAt.Add(1500*time.Millisecond), "a"
				var err error
				if status == RunCompleted {
					err = backend.CompleteRun(context.Background(), run, []model.Message{{Role: model.RoleUser, Content: "q"}, {Role: model.RoleAssistant, Content: "a"}})
				} else {
					err = backend.FinishRun(context.Background(), run)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := management.LoadTiming(context.Background(), Scope{ScopeKey: "owner", Workspace: "/w"}, session.ID)
			if err != nil || got.CompletedRuns != 2 || got.CompletedDuration != 3*time.Second {
				t.Fatalf("timing=%+v err=%v", got, err)
			}
			if _, err := management.LoadTiming(context.Background(), Scope{ScopeKey: "other", Workspace: "/w"}, session.ID); err != ErrSessionNotFound {
				t.Fatal(err)
			}
		})
	}
}
