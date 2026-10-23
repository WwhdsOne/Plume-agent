package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"plume-agent/internal/model"
)

// TestManagementScopesAndPagination 守住目录先按权限域过滤、短 ID 不越权、
// 本地继续忽略远程，以及最新正文页按发生顺序展示的共同契约。
func TestManagementScopesAndPagination(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		at := time.Unix(1700000000, 0)
		local := Scope{Workspace: "/w"}
		for i, id := range []string{"aaaaaaaa11111111", "aaaaaaaa22222222", "bbbbbbbb33333333"} {
			record := sampleSession(id, "/w", at)
			if i == 2 {
				record.ScopeKey = "remote/user-b"
			}
			if err := s.StartRun(ctx, record, RunRecord{ID: "run-" + id, Input: "q", StartedAt: at.Add(time.Duration(i) * time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
		page, err := manager.ListSessions(ctx, local, PageRequest{Page: 1, PageSize: 1})
		if err != nil || page.Total != 2 || len(page.Sessions) != 1 || page.Sessions[0].ID != "aaaaaaaa22222222" || page.Sessions[0].RunCount != 1 {
			t.Fatalf("directory = %+v err=%v", page, err)
		}
		if _, err := manager.ResolveSession(ctx, local, "bbbbbbbb"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("invisible prefix: %v", err)
		}
		if _, err := manager.ResolveSession(ctx, local, "aaaaaaaa"); !errors.Is(err, ErrAmbiguousSessionID) {
			t.Fatalf("ambiguous prefix: %v", err)
		}
		if _, err := manager.ResolveSession(ctx, Scope{Workspace: "/other"}, "aaaaaaaa11111111"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("workspace leak: %v", err)
		}
		latest, ok, err := s.LatestSession(ctx, "/w")
		if err != nil || !ok || latest.ID != "aaaaaaaa22222222" || latest.ScopeKey != "local" {
			t.Fatalf("local latest = %+v ok=%v err=%v", latest, ok, err)
		}
		if id, ok, err := manager.GetBinding(ctx, local); err != nil || !ok || id != "aaaaaaaa22222222" {
			t.Fatalf("run start binding = %q ok=%v err=%v", id, ok, err)
		}
		for i := 1; i <= 4; i++ {
			if err := s.StartRun(ctx, sampleSession("aaaaaaaa11111111", "/w", at), RunRecord{ID: fmt.Sprintf("run-page-%d", i), Input: "q", StartedAt: at.Add(time.Duration(i) * time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}
		runs, err := manager.PageRuns(ctx, local, "aaaaaaaa11111111", PageRequest{Page: 1, PageSize: 2})
		if err != nil || runs.Total != 5 || len(runs.Runs) != 2 || runs.Runs[0].ID != "run-page-3" || runs.Runs[1].ID != "run-page-4" {
			t.Fatalf("recent run page = %+v err=%v", runs, err)
		}
		older, err := manager.PageRuns(ctx, local, "aaaaaaaa11111111", PageRequest{Page: 2, PageSize: 2})
		if err != nil || len(older.Runs) != 2 || older.Runs[0].ID != "run-page-1" || older.Runs[1].ID != "run-page-2" {
			t.Fatalf("older run page = %+v err=%v", older, err)
		}
		if _, err := manager.PageRuns(ctx, local, "bbbbbbbb33333333", PageRequest{Page: 1, PageSize: 2}); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("invisible history: %v", err)
		}
	})
}

// TestCommandReceiptAtomicReplay 守住第一次结果、绑定与手工标题同事务；
// 重投旧指令不会覆盖后续标题或把绑定改回旧会话。
func TestCommandReceiptAtomicReplay(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		scope := Scope{ScopeKey: "remote/user-a", Workspace: "/w"}
		at := time.Unix(1700000000, 0)
		record := sampleSession("cccccccc11111111", "/w", at)
		record.ScopeKey, record.Title, record.TitleManual = scope.ScopeKey, "首标题", true
		record.LastRunAt = time.Time{}
		mutation := CommandMutation{Receipt: testReceipt(scope, "request-1", "new", at), CreateSession: &record, BindSessionID: record.ID}
		first, replay, err := manager.ApplyCommand(ctx, mutation)
		if err != nil || replay {
			t.Fatalf("first command: %+v replay=%v err=%v", first, replay, err)
		}
		loaded, err := manager.ResolveSession(ctx, scope, record.ID)
		if err != nil || loaded.Title != "首标题" || !loaded.TitleManual || !loaded.LastRunAt.IsZero() {
			t.Fatalf("manual metadata = %+v err=%v", loaded, err)
		}
		second := CommandMutation{Receipt: testReceipt(scope, "request-2", "rename", at), RenameSessionID: record.ID, Title: "新标题", TitleManual: true}
		if _, _, err := manager.ApplyCommand(ctx, second); err != nil {
			t.Fatal(err)
		}
		mutation.CreateSession.Title = "重投不能写入"
		got, replay, err := manager.ApplyCommand(ctx, mutation)
		if err != nil || !replay || string(got.Payload) != string(first.Payload) {
			t.Fatalf("replay = %+v replay=%v err=%v", got, replay, err)
		}
		loaded, err = manager.ResolveSession(ctx, scope, record.ID)
		if err != nil || loaded.Title != "新标题" {
			t.Fatalf("old command overwrote rename: %+v err=%v", loaded, err)
		}
		mutation.Receipt.Fingerprint = "different"
		if _, _, err := manager.ApplyCommand(ctx, mutation); !errors.Is(err, ErrReceiptConflict) {
			t.Fatalf("fingerprint conflict: %v", err)
		}
		if id, ok, err := manager.GetBinding(ctx, scope); err != nil || !ok || id != record.ID {
			t.Fatalf("binding = %q ok=%v err=%v", id, ok, err)
		}
		if _, ok, err := manager.GetReceipt(ctx, Scope{ScopeKey: "remote/user-b", Workspace: "/w"}, "request-1"); err != nil || ok {
			t.Fatalf("receipt scope leak ok=%v err=%v", ok, err)
		}
		if _, ok, err := manager.GetBinding(ctx, Scope{ScopeKey: "remote/user-a", Workspace: "/other"}); err != nil || ok {
			t.Fatalf("binding workspace leak ok=%v err=%v", ok, err)
		}
	})
}

// TestCommandBindingCanPrecedeFirstRun 守住默认空会话不写 metadata，
// 首次 run 之后原绑定能恢复为该权限域的真实会话。
func TestCommandBindingCanPrecedeFirstRun(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		scope := Scope{ScopeKey: "remote/user-a", Workspace: "/w"}
		at := time.Unix(1700000000, 0)
		id := "dddddddd11111111"
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(scope, "r-new", "new", at), BindSessionID: id}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ResolveSession(ctx, scope, id); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("empty default session persisted: %v", err)
		}
		record := sampleSession(id, "/w", at)
		record.ScopeKey = scope.ScopeKey
		if err := s.StartRun(ctx, record, RunRecord{ID: "run-new", Input: "q", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if got, err := manager.ResolveSession(ctx, scope, id); err != nil || got.ID != id {
			t.Fatalf("first run did not restore bound session: %+v %v", got, err)
		}
		other := Scope{ScopeKey: "remote/user-b", Workspace: "/w"}
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(other, "r-bind", "resume", at), BindSessionID: id}); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("cross-scope bind: %v", err)
		}
		if _, ok, _ := manager.GetReceipt(ctx, other, "r-bind"); ok {
			t.Fatal("failed mutation stored receipt")
		}
	})
}

// TestCommandReceiptConcurrentReplay 守住并发重复请求只提交一份 mutation；
// 返回的 JSON 是独立副本，调用方不能改写已存收据。
func TestCommandReceiptConcurrentReplay(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		manager := open(t).(ManagementStore)
		ctx := context.Background()
		scope := Scope{Workspace: "/w"}
		mutation := CommandMutation{Receipt: testReceipt(scope, "concurrent", "cancel", time.Now())}
		var wg sync.WaitGroup
		results := make(chan bool, 12)
		for range 12 {
			wg.Go(func() {
				receipt, replay, err := manager.ApplyCommand(ctx, mutation)
				if err != nil {
					t.Errorf("concurrent command: %v", err)
					return
				}
				receipt.Payload[0] = 'x'
				results <- replay
			})
		}
		wg.Wait()
		close(results)
		created := 0
		for replay := range results {
			if !replay {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("first receipts = %d", created)
		}
		receipt, ok, err := manager.GetReceipt(ctx, scope, "concurrent")
		if err != nil || !ok || !json.Valid(receipt.Payload) {
			t.Fatalf("receipt alias corrupted: %+v ok=%v err=%v", receipt, ok, err)
		}
	})
}

// TestManagementRejectsInvalidBounds 守住页码、整数溢出、收据 JSON 与正文
// 字节上界，错误不得变成无界读取或只提交部分 mutation。
func TestManagementRejectsInvalidBounds(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		scope := Scope{Workspace: "/w"}
		for _, page := range []PageRequest{{Page: 0, PageSize: 20}, {Page: 1, PageSize: 0}, {Page: 1, PageSize: 101}, {Page: int(^uint(0) >> 1), PageSize: 100}} {
			// case: 无效分页拒绝，不静默补默认或溢出为负 offset。
			if _, err := manager.ListSessions(ctx, scope, page); !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("invalid page %+v: %v", page, err)
			}
		}
		bad := testReceipt(scope, "invalid-json", "status", time.Now())
		bad.Payload = json.RawMessage("{")
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: bad}); err == nil {
			t.Fatal("invalid receipt JSON accepted")
		}
		at := time.Now()
		if err := s.StartRun(ctx, sampleSession("eeeeeeee11111111", "/w", at), RunRecord{ID: "run-big", Input: strings.Repeat("x", 8*1024*1024+1), StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.PageRuns(ctx, scope, "eeeeeeee11111111", PageRequest{Page: 1, PageSize: 20}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("unbounded page payload: %v", err)
		}
	})
}

func testReceipt(scope Scope, request, command string, at time.Time) Receipt {
	return Receipt{ScopeKey: scope.ScopeKey, Workspace: scope.Workspace, RequestID: request,
		Fingerprint: command + "-fingerprint", Command: command, Payload: json.RawMessage(`{"status":"ok"}`), CreatedAt: at}
}

// TestManagementBoundedMetadataAndReceipt 守住双后端相同的目录/收据载荷
// 边界，只有一条记录也不能因超大 title 或数组扩展绕过限制。
func TestManagementBoundedMetadataAndReceipt(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		scope := Scope{Workspace: "/w"}
		at := time.Now()
		large := sampleSession("ffffffff11111111", "/w", at)
		large.Title = strings.Repeat("x", defaultLoadBytes+1)
		if err := s.StartRun(ctx, large, RunRecord{ID: "metadata-big", Input: "q", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ListSessions(ctx, scope, PageRequest{Page: 1, PageSize: 20}); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("unbounded directory: %v", err)
		}
		if _, err := manager.ResolveSession(ctx, scope, large.ID); !errors.Is(err, ErrHistoryLimit) {
			t.Fatalf("unbounded exact metadata: %v", err)
		}
		for name, payload := range map[string]json.RawMessage{
			"bytes":   json.RawMessage(`"` + strings.Repeat("x", defaultLoadBytes) + `"`),
			"records": json.RawMessage("[" + strings.Repeat("null,", defaultLoadRecords) + "null]"),
		} {
			// case: 收据字节与数组总元素两条边界各自拒绝。
			receipt := testReceipt(scope, name, "history", at)
			receipt.Payload = payload
			if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: receipt}); !errors.Is(err, ErrHistoryLimit) {
				t.Fatalf("unbounded receipt %s: %v", name, err)
			}
		}
	})
}

// TestManagementQueriesUseBoundParameters 守住权限域、工作区、请求 ID
// 及前缀中的 SQL 片段与通配符全部被当作普通文本，不改变过滤集合。
func TestManagementQueriesUseBoundParameters(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		at := time.Now()
		scope := Scope{ScopeKey: "remote/' OR 1=1 --", Workspace: "/w'; DROP TABLE sessions; --"}
		record := sampleSession("abcdefgh%_11111111", scope.Workspace, at)
		record.ScopeKey = scope.ScopeKey
		if err := s.StartRun(ctx, record, RunRecord{ID: "run-safe", Input: "q", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ResolveSession(ctx, scope, "abcdefgh%_"); err != nil {
			t.Fatalf("literal prefix should match: %v", err)
		}
		if _, err := manager.ResolveSession(ctx, Scope{Workspace: scope.Workspace}, "abcdefgh%_"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("SQL identity escaped scope: %v", err)
		}
		receipt := testReceipt(scope, "r'; DROP TABLE bindings; --", "status", at)
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: receipt}); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := manager.GetReceipt(ctx, scope, receipt.RequestID); err != nil || !ok {
			t.Fatalf("literal request ID lost: ok=%v err=%v", ok, err)
		}
		if _, err := manager.ResolveSession(ctx, scope, "abcdefgh__"); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("prefix wildcard expanded: %v", err)
		}
	})
}

// TestManagementPendingBindingOwnership 守住没有 metadata 的绑定仍归属
// 首个权限域，其他入口不能抢 ID 或通过首 run 改写其所有权。
func TestManagementPendingBindingOwnership(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		at := time.Now()
		owner := Scope{ScopeKey: "remote/a", Workspace: "/w"}
		other := Scope{ScopeKey: "remote/b", Workspace: "/w"}
		id := "pending-session"
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(owner, "new", "new", at), BindSessionID: id}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(other, "hijack", "resume", at), BindSessionID: id}); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("pending binding hijacked: %v", err)
		}
		record := sampleSession(id, "/w", at)
		record.ScopeKey = other.ScopeKey
		if err := s.StartRun(ctx, record, RunRecord{ID: "run-hijack", Input: "q", StartedAt: at}); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("pending binding run hijacked: %v", err)
		}
		if _, ok, err := manager.GetBinding(ctx, other); err != nil || ok {
			t.Fatalf("failed start published binding: ok=%v err=%v", ok, err)
		}
		if _, _, err := s.LatestSession(ctx, "/w"); err != nil {
			t.Fatal(err)
		}
	})
}

// TestManagementManualTitleAndSummary 守住命名空会话不会抢占 -c，
// 首个 run 不覆盖手工标题，目录轮数从真实成功事务派生。
func TestManagementManualTitleAndSummary(t *testing.T) {
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		manager := s.(ManagementStore)
		ctx := context.Background()
		scope := Scope{Workspace: "/w"}
		at := time.Unix(1700000000, 0)
		old := sampleSession("old-session", "/w", at)
		if err := s.StartRun(ctx, old, RunRecord{ID: "old-run", Input: "q", StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		named := sampleSession("named-session", "/w", at.Add(time.Hour))
		named.Title, named.TitleManual, named.LastRunAt = "手工标题", true, time.Time{}
		if _, _, err := manager.ApplyCommand(ctx, CommandMutation{Receipt: testReceipt(scope, "named", "new", at), CreateSession: &named}); err != nil {
			t.Fatal(err)
		}
		if latest, _, err := s.LatestSession(ctx, "/w"); err != nil || latest.ID != old.ID {
			t.Fatalf("named empty session stole latest: %+v err=%v", latest, err)
		}
		automatic := named
		automatic.Title, automatic.TitleManual = "首次输入自动标题", false
		started := at.Add(2 * time.Hour)
		if err := s.StartRun(ctx, automatic, RunRecord{ID: "named-run", Input: "q", StartedAt: started}); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, RunRecord{ID: "named-run", SessionID: named.ID, FinishedAt: started.Add(time.Second)}, []model.Message{{Role: model.RoleUser, Content: "q"}, {Role: model.RoleAssistant, Content: "a"}}); err != nil {
			t.Fatal(err)
		}
		got, err := manager.ResolveSession(ctx, scope, named.ID)
		if err != nil || got.Title != named.Title || !got.TitleManual || got.RunCount != 1 || got.TurnCount != 1 {
			t.Fatalf("manual title or counts changed: %+v err=%v", got, err)
		}
	})
}
