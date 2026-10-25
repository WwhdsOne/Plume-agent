package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// createMemoryLegacyV2 仅在隔离目录构造真实旧库，保留成功轮次、调用、
// 绑定和管理收据，避免用新迁移器制造假旧版夹具。
func createMemoryLegacyV2(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range append(append([]string{}, schemaStatements...), managementSchemaStatements...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := encodeTurn([]model.Message{{Role: model.RoleUser, Content: "legacy-input"}, {Role: model.RoleAssistant, Content: "legacy-reply"}})
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO sessions(id,workspace,title,model_ref,created_at,last_run_at,scope_key,title_manual) VALUES('legacy','/w','old','{}',1000,2000,'local',1)`,
		`INSERT INTO runs(id,session_id,input,status,started_at,finished_at,reply) VALUES('legacy-run','legacy','legacy-input','completed',1000,2000,'legacy-reply')`,
		`INSERT INTO model_calls(id,run_id,session_id,sequence,saved_at) VALUES('legacy-call','legacy-run','legacy',1,1000)`,
		`INSERT INTO bindings(scope_key,workspace,session_id) VALUES('local','/w','legacy')`,
		`INSERT INTO command_receipts(scope_key,workspace,request_id,fingerprint,command,payload,created_at) VALUES('local','/w','old-command','fixed','rename','{"ok":true}',1000)`,
		`PRAGMA user_version = 2`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO turns(session_id,seq,run_id,messages,created_at) VALUES('legacy',1,'legacy-run',?,2000)`, string(turn)); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLongTermMemoryMigrationAndReopen 守住 v2→v3（现随 v4 检查点表继续
// 前向迁移）保留所有旧数据类型，新记忆跨关闭重开保持相同版本/时间，
// 数据库权限仍为 0600。
func TestLongTermMemoryMigrationAndReopen(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	path := createMemoryLegacyV2(t)
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("memory schema=%d err=%v", version, err)
	}
	// case: 原会话、成功消息、未知用量、绑定和收据分别核查，不以行数代替语义。
	session, err := s.ResolveSession(ctx, Scope{Workspace: "/w"}, "legacy")
	if err != nil || session.Title != "old" || !session.TitleManual || session.TurnCount != 1 {
		t.Fatal("migration changed legacy session")
	}
	turn, next, err := s.LoadTurns(ctx, "legacy")
	if err != nil || next != 2 || len(turn) != 2 || turn[1].Content != "legacy-reply" {
		t.Fatal("migration changed legacy messages")
	}
	calls, err := s.LoadCalls(ctx, "legacy")
	if err != nil || len(calls) != 1 || calls[0].Usage != nil {
		t.Fatal("migration changed legacy usage")
	}
	if id, ok, err := s.GetBinding(ctx, Scope{Workspace: "/w"}); err != nil || !ok || id != "legacy" {
		t.Fatal("migration changed legacy binding")
	}
	if receipt, ok, err := s.GetReceipt(ctx, Scope{Workspace: "/w"}, "old-command"); err != nil || !ok || string(receipt.Payload) != `{"ok":true}` {
		t.Fatal("migration changed legacy receipt")
	}
	scope := MemoryScope{ScopeKey: "local", Kind: MemorySession, Workspace: "/w", SessionID: "legacy"}
	first := putTestMemory(t, s, memoryTestWrite(scope, "preference", "仅存库中的合成偏好", 0))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.ListMemories(ctx, scope, MemoryLimits{})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], first) || got[0].CreatedAt.Location() != time.UTC {
		t.Fatal("memory did not survive reopen")
	}
	for _, path := range []string{path, path + ".lock"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("memory database permissions changed")
		}
	}
}

// TestLongTermMemoryMigrationRollback 在首条 DDL 成功后注入真实 SQL
// 错误，确认整次事务回滚新表与版本，旧库仍可使用。
func TestLongTermMemoryMigrationRollback(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	path := createMemoryLegacyV2(t)
	original := memorySchemaStatements
	// case: 故障点在新表已创建之后；本测试不并行，结束恢复迁移语句。
	memorySchemaStatements = append(append([]string{}, original...), `CREATE TABLE sessions(id TEXT)`)
	t.Cleanup(func() { memorySchemaStatements = original })
	if s, err := OpenSQLite(path); err == nil {
		_ = s.Close()
		t.Fatal("injected migration failure was ignored")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, tables, runs int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("failed migration published version=%d err=%v", version, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'memory_entries'").Scan(&tables); err != nil || tables != 0 {
		t.Fatal("failed migration left memory table")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM runs WHERE status='completed'").Scan(&runs); err != nil || runs != 1 {
		t.Fatal("failed migration changed old data")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	memorySchemaStatements = original
	// case: 移除故障后正常重开成功，证明失败已释放整库锁。
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
}

// TestLongTermMemoryStorageFailureKeepsData 用 SQLite 触发器拒绝实际
// 写入，旧版本与历史保持不变，含敏感字样的驱动错误不可出现在返回错误。
func TestLongTermMemoryStorageFailureKeepsData(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	s, err := OpenSQLite(createMemoryLegacyV2(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	scope := memoryTestScope()
	first := putTestMemory(t, s, memoryTestWrite(scope, "stable", "原值", 0))
	if _, err := s.db.Exec(`CREATE TRIGGER reject_memory_update BEFORE UPDATE ON memory_entries BEGIN SELECT RAISE(ABORT, 'PRIVATE-CONTENT-SENTINEL'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutMemory(context.Background(), memoryTestWrite(scope, "stable", "不应写入的新值", 1)); !errors.Is(err, ErrMemoryStorage) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("storage error was not sanitized: %v", err)
	}
	got, err := s.ListMemories(context.Background(), scope, MemoryLimits{})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], first) {
		t.Fatal("failed storage update polluted memory")
	}
	if _, next, err := s.LoadTurns(context.Background(), "legacy"); err != nil || next != 2 {
		t.Fatal("failed memory write changed successful history")
	}
}

// TestLongTermMemoryCorruptRead 守住外部修改的库仍须在返回前校验；
// 超大正文在读取前拒绝，损坏来源或 UTF-8 不会混入正常条目返回。
func TestLongTermMemoryCorruptRead(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	for _, tc := range []struct {
		name, statement string
		value           any
	}{
		// case: 同时存在合法条目时也必须整次拒绝，不返回合法前缀。
		{"source", "UPDATE memory_entries SET source=? WHERE memory_key='bad'", "model"},
		{"utf8", "UPDATE memory_entries SET content=? WHERE memory_key='bad'", string([]byte{0xff})},
		{"bytes", "UPDATE memory_entries SET content=? WHERE memory_key='bad'", strings.Repeat("x", memoryReadBytes+1)},
		{"revision", "UPDATE memory_entries SET revision=? WHERE memory_key='bad'", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			putTestMemory(t, s, memoryTestWrite(memoryTestScope(), "a", "合法值", 0))
			putTestMemory(t, s, memoryTestWrite(memoryTestScope(), "bad", "待损坏值", 0))
			if _, err := s.db.Exec("PRAGMA ignore_check_constraints=ON"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(tc.statement, tc.value); err != nil {
				t.Fatal(err)
			}
			if got, err := s.ListMemories(context.Background(), memoryTestScope(), MemoryLimits{}); got != nil || err == nil {
				t.Fatal("corrupt read returned partial memory")
			}
		})
	}
}

// TestLongTermMemoryNumericTypesPreflight 守住 SQLite 非严格类型的损坏
// 边界：数值列存 BLOB/文本/小数时必须在读取正文前拒绝，不能先扫描大载荷。
func TestLongTermMemoryNumericTypesPreflight(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	for _, column := range []string{"revision", "created_at", "updated_at"} {
		// case: 三个固定计24字节的字段分别验证，不能只检查版本列。
		for name, value := range map[string]any{"blob": make([]byte, 1024*1024), "text": "not-an-integer", "real": 1.5} {
			t.Run(column+"/"+name, func(t *testing.T) {
				s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				scope := memoryTestScope()
				putTestMemory(t, s, memoryTestWrite(scope, "bad", "合成正文", 0))
				// case: 正常写库的类型约束先挡住非法值；读侧还须防绕过约束。
				if _, err := s.db.Exec("UPDATE memory_entries SET "+column+"=? WHERE memory_key='bad'", value); err == nil {
					t.Fatal("numeric schema constraint accepted non-integer")
				}
				// 外部工具可以关闭CHECK，读取侧仍须独立验证实际存储类型。
				if _, err := s.db.Exec("PRAGMA ignore_check_constraints=ON"); err != nil {
					t.Fatal(err)
				}
				// column 仅来自本测试固定白名单，不接受任何外部输入。
				if _, err := s.db.Exec("UPDATE memory_entries SET "+column+"=? WHERE memory_key='bad'", value); err != nil {
					t.Fatal(err)
				}
				if got, err := s.ListMemories(context.Background(), scope, MemoryLimits{}); got != nil || !errors.Is(err, ErrCorruptMemory) {
					t.Fatalf("numeric type bypassed read preflight: %v", err)
				}
				if _, err := s.PutMemory(context.Background(), memoryTestWrite(scope, "bad", "覆盖", 1)); !errors.Is(err, ErrCorruptMemory) {
					t.Fatalf("numeric type bypassed update preflight: %v", err)
				}
			})
		}
	}
}

// TestLongTermMemoryCancelledTransactionError 复现 database/sql 在取消
// 后先回滚、Commit返回ErrTxDone的分支，取消类别不能退化为存储故障。
func TestLongTermMemoryCancelledTransactionError(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, deadline := range []bool{false, true} {
		// case: 用户取消和期限耗尽均需保留标准错误身份，不回显驱动错误。
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if deadline {
			<-ctx.Done()
		} else {
			cancel()
		}
		_ = tx.Rollback()
		commitErr := tx.Commit()
		if !errors.Is(commitErr, sql.ErrTxDone) {
			cancel()
			t.Fatalf("transaction did not enter rolled back state: %v", commitErr)
		}
		got := memoryStorageError(ctx, commitErr)
		want := ctx.Err()
		cancel()
		if !errors.Is(got, want) {
			t.Fatalf("transaction cancellation identity lost: %v", got)
		}
	}
}
