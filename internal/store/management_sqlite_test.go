package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestManagementSQLiteMigrationV1 守住 v1 数据、成功轮次与调用不因扩展
// 管理表而丢失；旧会话明确归属 local，手工标题标记默认 false。
func TestManagementSQLiteMigrationV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range schemaStatements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,workspace,title,model_ref,created_at,last_run_at) VALUES('legacy','/w','old','{}',1,2); INSERT INTO runs(id,session_id,input,status,started_at) VALUES('legacy-run','legacy','q','failed',2); PRAGMA user_version = 1;`); err != nil {
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
	record, err := s.ResolveSession(context.Background(), Scope{Workspace: "/w"}, "legacy")
	if err != nil || record.ScopeKey != "local" || record.TitleManual || record.Title != "old" || record.RunCount != 1 {
		t.Fatalf("migrated record = %+v err=%v", record, err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("schema = %d err=%v", version, err)
	}
}

// TestManagementSQLiteMigrationFailureRollsBack 守住迁移不是逐条发布；
// 后续表创建失败，前面新增列及 user_version 也必须整体回滚。
func TestManagementSQLiteMigrationFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range schemaStatements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("CREATE TABLE bindings(unexpected TEXT); PRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenSQLite(path); err == nil {
		_ = s.Close()
		t.Fatal("conflicting migration unexpectedly succeeded")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, scopeColumns int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'scope_key'").Scan(&scopeColumns); err != nil {
		t.Fatal(err)
	}
	if version != 1 || scopeColumns != 0 {
		t.Fatalf("half migrated schema version=%d scopeColumns=%d", version, scopeColumns)
	}
}

// TestManagementSQLiteReceiptFailureRollsBack 守住 metadata、rename、binding
// 与收据同事务；模拟最后一步写失败后，既有标题和绑定均不改变。
func TestManagementSQLiteReceiptFailureRollsBack(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	scope := Scope{Workspace: "/w"}
	at := time.Now()
	old := sampleSession("old-record", "/w", at)
	old.TitleManual = true
	if _, _, err := s.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(scope, "first", "new", at), CreateSession: &old, BindSessionID: old.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER deny_receipt BEFORE INSERT ON command_receipts BEGIN SELECT RAISE(ABORT, 'receipt fixture failure'); END;`); err != nil {
		t.Fatal(err)
	}
	created := sampleSession("new-record", "/w", at)
	_, _, err = s.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(scope, "failed", "new", at), CreateSession: &created, BindSessionID: created.ID, RenameSessionID: old.ID, Title: "must rollback", TitleManual: true})
	if err == nil {
		t.Fatal("receipt failure ignored")
	}
	if _, err := s.ResolveSession(ctx, scope, created.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("created metadata survived rollback: %v", err)
	}
	if id, _, err := s.GetBinding(ctx, scope); err != nil || id != old.ID {
		t.Fatalf("binding changed before receipt: %q err=%v", id, err)
	}
	if record, err := s.ResolveSession(ctx, scope, old.ID); err != nil || record.Title != old.Title {
		t.Fatalf("rename survived rollback: %+v err=%v", record, err)
	}
}

// TestManagementSQLiteReceiptRestart 守住重启仍重放首次结构化结果，
// 只读/no-target 取消收据也必须保留，不重新解析当前 run。
func TestManagementSQLiteReceiptRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{ScopeKey: "remote/user-a", Workspace: "/w"}
	receipt := testReceipt(scope, "no-target", "cancel", time.Now())
	receipt.Payload = json.RawMessage(`{"status":"cancel_requested","run_id":""}`)
	if _, _, err := s.ApplyCommand(context.Background(), CommandMutation{Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	receipt.Payload = json.RawMessage(`{"run_id":"later-run"}`)
	stored, replay, err := s.ApplyCommand(context.Background(), CommandMutation{Receipt: receipt})
	if err != nil || !replay || string(stored.Payload) != `{"status":"cancel_requested","run_id":""}` {
		t.Fatalf("cancel replay retargeted: %+v replay=%v err=%v", stored, replay, err)
	}
}

// TestManagementSQLiteStartFailurePreservesBinding 守住首 run 的绑定更新
// 与 run 起点同事务，数据库拒绝 run INSERT 时不能切走原活动会话。
func TestManagementSQLiteStartFailurePreservesBinding(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	scope := Scope{Workspace: "/w"}
	at := time.Now()
	if err := s.StartRun(ctx, sampleSession("old", "/w", at), RunRecord{ID: "old-run", Input: "q", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER deny_run BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT, 'run fixture failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.StartRun(ctx, sampleSession("new", "/w", at), RunRecord{ID: "new-run", Input: "q", StartedAt: at}); err == nil {
		t.Fatal("failed run insert ignored")
	}
	if id, ok, err := s.GetBinding(ctx, scope); err != nil || !ok || id != "old" {
		t.Fatalf("failed run changed binding: %q ok=%v err=%v", id, ok, err)
	}
	if _, err := s.ResolveSession(ctx, scope, "new"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("failed run left metadata: %v", err)
	}
}
