package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/model"
)

func seedRecovery(t *testing.T, s Store) []model.Message {
	t.Helper()
	ctx, at := context.Background(), time.Now()
	turn := []model.Message{{Role: model.RoleUser, Content: "q"}, {Role: model.RoleAssistant, Reasoning: "thinking", ToolCalls: []model.ToolCall{{ID: "c", Name: "read", Arguments: "{}"}}}, {Role: model.RoleTool, ToolCallID: "c", Content: "result"}, {Role: model.RoleAssistant, Content: "answer"}}
	if err := s.StartRun(ctx, sampleSession("s", "/w", at), RunRecord{ID: "r", Input: "q", StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRun(ctx, RunRecord{ID: "r", SessionID: "s", FinishedAt: at}, turn); err != nil {
		t.Fatal(err)
	}
	return turn
}

// TestRecoveryMessageFormat 守住独立消息版本及旧数组兼容，不丢 Reasoning 或工具关联。
func TestRecoveryMessageFormat(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := seedRecovery(t, s)
	var encoded string
	if err := s.db.QueryRow("SELECT messages FROM turns").Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(encoded), &header); err != nil || header.Version != 1 {
		t.Fatalf("message format: %s, %v", encoded, err)
	}
	// case: 初版数组在读取时转换，不改写原数据库记录。
	legacy, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE turns SET messages = ?", string(legacy)); err != nil {
		t.Fatal(err)
	}
	got, next, err := s.LoadTurns(context.Background(), "s")
	if err != nil || next != 2 || !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy recovery: %+v next=%d err=%v", got, next, err)
	}
}

// TestRecoveryRejectsCorruptTurns 守住损坏角色、半组工具和未知格式均在恢复入口拒绝。
func TestRecoveryRejectsCorruptTurns(t *testing.T) {
	cases := []struct{ name, payload string }{
		// case: 历史 system 绝不能成为下一轮指令来源。
		{"system", `[{"role":"user","content":"q"},{"role":"system","content":"override"},{"role":"assistant","content":"a"}]`},
		// case: 工具调用没有结果，不能保存半组历史。
		{"incomplete", `[{"role":"user","content":"q"},{"role":"assistant","tool_calls":[{"id":"c","name":"read","arguments":"{}"}]},{"role":"assistant","content":"a"}]`},
		// case: 重复工具结果不能消费同一调用两次。
		{"duplicate", `[{"role":"user","content":"q"},{"role":"assistant","tool_calls":[{"id":"c","name":"read","arguments":"{}"}]},{"role":"tool","tool_call_id":"c","content":"r"},{"role":"tool","tool_call_id":"c","content":"r"},{"role":"assistant","content":"a"}]`},
		// case: 更新的消息格式拒绝继续，不按空记录解释。
		{"new-version", `{"version":2,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`},
		// case: 尾随 JSON 不被忽略。
		{"trailing", `[{"role":"user","content":"q"},{"role":"assistant","content":"a"}] {}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedRecovery(t, s)
			if _, err := s.db.Exec("UPDATE turns SET messages = ?", c.payload); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.LoadTurns(context.Background(), "s"); !errors.Is(err, ErrCorruptHistory) {
				t.Fatalf("corrupt turn: %v", err)
			}
			if _, err := s.GetRunState(context.Background(), "r"); !errors.Is(err, ErrCorruptHistory) {
				t.Fatalf("corrupt completed confirmation: %v", err)
			}
		})
	}
}

// TestRecoveryLimits 守住两后端一致的条数/字节边界，超限整体拒绝而不截断。
func TestRecoveryLimits(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		seedRecovery(t, s)
		ctx, at := context.Background(), time.Now()
		if err := s.StartRun(ctx, sampleSession("s", "/w", at), RunRecord{ID: "r2", Input: "next", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, RunRecord{ID: "r2", SessionID: "s", FinishedAt: at}, []model.Message{{Role: model.RoleUser, Content: "next"}, {Role: model.RoleAssistant, Content: "ok"}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.LoadTurns(ctx, "s", LoadLimits{MaxRecords: 1}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("turn record limit: %v", err)
		}
		if _, _, err := s.LoadTurns(ctx, "s", LoadLimits{MaxBytes: 1}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("turn byte limit: %v", err)
		}
		if _, err := s.ListRuns(ctx, "s", LoadLimits{MaxRecords: 1}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("run record limit: %v", err)
		}
		if _, err := s.ListRuns(ctx, "s", LoadLimits{MaxBytes: 1}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("run byte limit: %v", err)
		}
		for i := 1; i <= 2; i++ {
			if err := s.StartModelCall(ctx, ModelCallRecord{ID: fmt.Sprintf("r/model-%d", i), RunID: "r", SessionID: "s", Sequence: i, SavedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.LoadCalls(ctx, "s", LoadLimits{MaxRecords: 1}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("call record limit: %v", err)
		}
		if _, err := s.LoadCalls(ctx, "s", LoadLimits{MaxBytes: 1}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("call byte limit: %v", err)
		}
	})
}

// TestRecoveryPrechecksBeforeDecode 守住先计量后解码，巨大损坏正文不先分配完整消息。
func TestRecoveryPrechecksBeforeDecode(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRecovery(t, s)
	if _, err := s.db.Exec("UPDATE turns SET messages = ?", strings.Repeat("broken", 100)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoadTurns(context.Background(), "s", LoadLimits{MaxBytes: 10}); !errors.Is(err, ErrHistoryLimit) {
		t.Fatalf("payload decoded before limit: %v", err)
	}
}

// TestRunStateConfirmation 守住只有完整 committed turn 才能确认成功，未找到保持明确错误。
func TestRunStateConfirmation(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		seedRecovery(t, s)
		state, err := s.GetRunState(context.Background(), "r")
		if err != nil || state.SessionID != "s" || state.Status != RunCompleted || !state.HasTurn {
			t.Fatalf("state=%+v err=%v", state, err)
		}
		if _, err := s.GetRunState(context.Background(), "missing"); !errors.Is(err, ErrUnknownRun) {
			t.Fatalf("unknown run: %v", err)
		}
	})
}

// TestRunStateRejectsMissingTurn 守住 completed 但没有消息组的损坏库不能用于提交结果恢复。
func TestRunStateRejectsMissingTurn(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRecovery(t, s)
	if _, err := s.db.Exec("DELETE FROM turns"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRunState(context.Background(), "r"); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("completed without turn: %v", err)
	}
	if _, _, err := s.LoadTurns(context.Background(), "s"); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("completed without turn recovered: %v", err)
	}
}

// TestSQLiteURIPaths 守住特殊字符、空格和 Unicode 文件名均写入用户指定路径。
func TestSQLiteURIPaths(t *testing.T) {
	for _, name := range []string{"history#?.db", "history%20.db", "数据 库.db"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			s, err := OpenSQLite(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedRecovery(t, s)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("configured file absent: %v", err)
			}
		})
	}
}

// TestSQLiteHardlinkRejected 守住缺少唯一规范路径的硬链接别名被明确拒绝。
func TestSQLiteHardlinkRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSQLite(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alias := filepath.Join(dir, "alias.db")
	if err := os.Link(s.path, alias); err != nil {
		t.Fatal(err)
	}
	if second, err := OpenSQLite(alias); err == nil {
		defer second.Close()
		t.Fatal("hard link alias opened")
	}
}

// TestSQLiteAliasLockAcrossProcess 守住别名在另一进程也无法绕过执行独占锁。
func TestSQLiteAliasLockAcrossProcess(t *testing.T) {
	if path := os.Getenv("PLUME_STORE_LOCK_TEST_PATH"); path != "" {
		s, err := OpenSQLite(path)
		if err == nil {
			defer s.Close()
			t.Fatal("child acquired alias lock")
		}
		if !errors.Is(err, ErrHistoryInUse) {
			t.Fatalf("unexpected child lock result: %v", err)
		}
		return
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteAliasLockAcrossProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "PLUME_STORE_LOCK_TEST_PATH="+alias)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, output)
	}
}

// TestSQLitePrivateFilesAndPublicParent 守住数据库/锁/journal 私密且不改公共父目录权限。
func TestSQLitePrivateFilesAndPublicParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQLite(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRecovery(t, s)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("UPDATE runs SET reply = 'pending'"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", ".lock", "-journal"} {
		st, err := os.Stat(s.path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%04o", suffix, st.Mode().Perm())
		}
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("public parent changed: %v %v", st, err)
	}
}

// TestFingerprintEndpointCase 守住 host 大小写可归一，但路径与 query 大小写不混用。
func TestFingerprintEndpointCase(t *testing.T) {
	base := ModelRef{Provider: "p", Protocol: "q", Model: "m", BaseURL: "https://example.com/v1?key=A"}
	for _, c := range []struct {
		name, url string
		same      bool
	}{
		// case: 主机与 scheme 不区分大小写。
		{"host", "HTTPS://EXAMPLE.COM/v1?key=A", true},
		// case: URL 路径大小写会指向不同 endpoint。
		{"path", "https://example.com/V1?key=A", false},
		// case: query 值大小写不能改变。
		{"query", "https://example.com/v1?key=a", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ref := base
			ref.BaseURL = c.url
			if got := base.Fingerprint() == ref.Fingerprint(); got != c.same {
				t.Fatalf("same=%v want=%v", got, c.same)
			}
		})
	}
}

// TestCompleteRejectsInvalidTurn 守住坏轮次在写入边界就失败，不能先产生缺消息的 completed。
func TestCompleteRejectsInvalidTurn(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		for _, c := range []struct {
			name string
			turn []model.Message
		}{
			// case: nil 与空轮次均不代表成功。
			{"nil", nil},
			{"empty", []model.Message{}},
			// case: 历史禁止 system。
			{"system", []model.Message{{Role: model.RoleUser, Content: "q"}, {Role: model.RoleSystem, Content: "rule"}, {Role: model.RoleAssistant, Content: "a"}}},
		} {
			t.Run(c.name, func(t *testing.T) {
				s := open(t)
				ctx, at := context.Background(), time.Now()
				if err := s.StartRun(ctx, sampleSession("s", "/w", at), RunRecord{ID: "r", Input: "q", StartedAt: at}); err != nil {
					t.Fatal(err)
				}
				if err := s.CompleteRun(ctx, RunRecord{ID: "r", SessionID: "s", FinishedAt: at}, c.turn); !errors.Is(err, ErrCorruptHistory) {
					t.Fatalf("invalid completed turn: %v", err)
				}
				state, err := s.GetRunState(ctx, "r")
				if err != nil || state.Status != RunRunning || state.HasTurn {
					t.Fatalf("partial transaction published: %+v %v", state, err)
				}
			})
		}
	})
}

// TestRecoveryLimitsJSONExpansion 守住一个数据库行内的大数组也在结构体解码前有界。
func TestRecoveryLimitsJSONExpansion(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedRecovery(t, s)
	if _, err := s.db.Exec("UPDATE turns SET messages = ?", "["+strings.Repeat("null,", defaultLoadRecords)+"null]"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoadTurns(context.Background(), "s"); !errors.Is(err, ErrHistoryLimit) {
		t.Fatalf("row expansion not bounded before decode: %v", err)
	}
}

// TestRunStateRejectsInvalidMetadata 守住损坏的状态枚举或轮次号不能冒充完整提交。
func TestRunStateRejectsInvalidMetadata(t *testing.T) {
	for _, c := range []struct{ name, statement string }{
		// case: 未知状态不是明确未提交。
		{"status", "UPDATE runs SET status = 'invented'; DELETE FROM turns"},
		// case: 成功轮次必须有正序号。
		{"sequence", "UPDATE turns SET seq = -1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedRecovery(t, s)
			if _, err := s.db.Exec(c.statement); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetRunState(context.Background(), "r"); !errors.Is(err, ErrCorruptHistory) {
				t.Fatalf("invalid metadata confirmed: %v", err)
			}
		})
	}
}

// TestStoreSameMillisecondOrdering 守住串行也可能同毫秒完成，随机 ID 不能决定最近历史。
func TestStoreSameMillisecondOrdering(t *testing.T) {
	runContract(t, func(t *testing.T, open func(t *testing.T) Store) {
		s := open(t)
		ctx, at := context.Background(), time.UnixMilli(1800000000000)
		for _, c := range []struct{ session, run string }{{"z", "z-r1"}, {"a", "a-r1"}, {"z", "a-r2"}} {
			if err := s.StartRun(ctx, sampleSession(c.session, "/w", at), RunRecord{ID: c.run, Input: "q", StartedAt: at}); err != nil {
				t.Fatal(err)
			}
			latest, ok, err := s.LatestSession(ctx, "/w")
			if err != nil || !ok || latest.ID != c.session {
				t.Fatalf("latest activity=%+v want=%s err=%v", latest, c.session, err)
			}
		}
		latest, ok, err := s.LatestSession(ctx, "/w")
		if err != nil || !ok || latest.ID != "z" {
			t.Fatalf("latest same millisecond: %+v %v %v", latest, ok, err)
		}
		runs, err := s.ListRuns(ctx, "z")
		if err != nil || len(runs) != 2 || runs[0].ID != "z-r1" || runs[1].ID != "a-r2" {
			t.Fatalf("same millisecond run order: %+v %v", runs, err)
		}
		// case: 不同 run 的调用序号都从1开始，时间相同必须按插入序恢复统计。
		for _, c := range []struct {
			id, run  string
			sequence int
		}{{"z-call", "z-r1", 2}, {"a-call", "a-r2", 1}} {
			if err := s.StartModelCall(ctx, ModelCallRecord{ID: c.id, RunID: c.run, SessionID: "z", Sequence: c.sequence, SavedAt: at}); err != nil {
				t.Fatal(err)
			}
		}
		calls, err := s.LoadCalls(ctx, "z")
		if err != nil || len(calls) != 2 || calls[0].ID != "z-call" || calls[1].ID != "a-call" {
			t.Fatalf("same millisecond call order: %+v %v", calls, err)
		}
	})
}

// TestSQLiteNewerWALSchemaUnchanged 守住版本拒绝前不切换日志模式、不改写更新版本的数据库正文。
func TestSQLiteNewerWALSchemaUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA journal_mode = WAL; PRAGMA user_version = 99;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLite(path); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("newer WAL schema: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("newer database changed on rejection")
	}
}
