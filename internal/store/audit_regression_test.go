package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// TestAuditCreatedDatabaseMode 守住首次创建的私密权限，不能依赖第二次打开补 chmod。
func TestAuditCreatedDatabaseMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("new database mode=%04o, want 0600", st.Mode().Perm())
	}
}

// TestAuditLoadRejectsInvalidAssociation 守住恢复时的完整工具关联，不把损坏数据交到下一次请求。
func TestAuditLoadRejectsInvalidAssociation(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, now := context.Background(), time.Now()
	if err := s.StartRun(ctx, sampleSession("s", "/w", now), RunRecord{ID: "r", Input: "q", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(ctx, RunRecord{ID: "r", SessionID: "s", FinishedAt: now}, []model.Message{{Role: model.RoleUser, Content: "q"}, {Role: model.RoleAssistant, Content: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE turns SET messages = ?", `[{"role":"tool","tool_call_id":"does-not-exist","content":"result"}]`); err != nil {
		t.Fatal(err)
	}
	if messages, _, err := s.LoadTurns(ctx, "s"); err == nil {
		t.Fatalf("invalid tool association silently accepted: %+v", messages)
	}
}

// TestAuditSpecialPathStaysAtConfiguredFile 守住普通文件名中的 URI 特殊字符不改变实际数据库位置。
func TestAuditSpecialPathStaysAtConfiguredFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history#test.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("configured path absent after successful open: %v", err)
	}
}

// TestAuditSymlinkAliasSharesProcessLock 守住同库别名不能取得第二份 Agent 执行权。
func TestAuditSymlinkAliasSharesProcessLock(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alias := filepath.Join(dir, "alias.db")
	if err := os.Symlink(s.path, alias); err != nil {
		t.Fatal(err)
	}
	second, err := OpenSQLite(alias)
	if err == nil {
		defer second.Close()
		t.Fatal("same database opened under alias despite process exclusive lock")
	}
}
