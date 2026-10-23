package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func memoryTestScope() MemoryScope {
	return MemoryScope{ScopeKey: "local", Kind: MemoryWorkspace, Workspace: "/w"}
}

func memoryTestWrite(scope MemoryScope, key, content string, revision int64) MemoryWrite {
	return MemoryWrite{Scope: scope, Key: key, Content: content, Source: MemorySourceUser, ExpectedRevision: revision}
}

func putTestMemory(t *testing.T, ledger LongTermMemoryStore, write MemoryWrite) MemoryRecord {
	t.Helper()
	record, err := ledger.PutMemory(context.Background(), write)
	if err != nil {
		t.Fatalf("put memory: %v", err)
	}
	return record
}

// TestLongTermMemoryVersions 守住双后端创建、重投、CAS 更新及稳定排序，
// 相同操作不刷新时间，陈旧更新不能覆盖已经确认的新版本。
func TestLongTermMemoryVersions(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		ledger := open(t).(LongTermMemoryStore)
		scope := memoryTestScope()
		first := putTestMemory(t, ledger, memoryTestWrite(scope, "b", "偏好简洁", 0))
		if first.Revision != 1 || first.CreatedAt.IsZero() || !first.CreatedAt.Equal(first.UpdatedAt) {
			t.Fatal("invalid initial memory version")
		}
		// case: 原创建重投和当前版本上的相同内容均为空操作。
		for _, revision := range []int64{0, 1} {
			got := putTestMemory(t, ledger, memoryTestWrite(scope, "b", "偏好简洁", revision))
			if !reflect.DeepEqual(got, first) {
				t.Fatal("replay changed memory")
			}
		}
		updated := putTestMemory(t, ledger, memoryTestWrite(scope, "b", "偏好详细", 1))
		if updated.Revision != 2 || !updated.CreatedAt.Equal(first.CreatedAt) || updated.UpdatedAt.Before(first.UpdatedAt) {
			t.Fatal("invalid updated memory version")
		}
		replay := putTestMemory(t, ledger, memoryTestWrite(scope, "b", "偏好详细", 1))
		if !reflect.DeepEqual(updated, replay) {
			t.Fatal("update replay changed memory")
		}
		// case: 陈旧创建和更新都拒绝，不能覆盖新版。
		for _, revision := range []int64{0, 1, 9} {
			if _, err := ledger.PutMemory(context.Background(), memoryTestWrite(scope, "b", "旧内容", revision)); !errors.Is(err, ErrMemoryConflict) {
				t.Fatalf("stale write: %v", err)
			}
		}
		putTestMemory(t, ledger, memoryTestWrite(scope, "a", "独立条目", 0))
		got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{})
		if err != nil || len(got) != 2 || got[0].Key != "a" || got[1].Content != updated.Content {
			t.Fatal("memory ordering or conflict isolation failed")
		}
		// case: 返回值为副本，修改读取结果不影响账本。
		got[1].Content = "调用方修改"
		again, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{})
		if err != nil || again[1].Content != updated.Content {
			t.Fatal("memory result aliases stored state")
		}
	})
}

// TestLongTermMemoryScopeIsolation 守住主体、工作区和会话隔离；global
// 只在同一主体显式选择时可读，不会自动混入其他精确作用域。
func TestLongTermMemoryScopeIsolation(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		ledger := s.(LongTermMemoryStore)
		ctx := context.Background()
		for _, id := range []string{"session-a", "session-b"} {
			record := sampleSession(id, "/w", time.Now())
			if _, _, err := s.(ManagementStore).ApplyCommand(ctx, CommandMutation{
				Receipt: testReceipt(Scope{Workspace: "/w"}, id, "new", time.Now()), CreateSession: &record,
			}); err != nil {
				t.Fatal(err)
			}
		}
		scopes := []MemoryScope{
			// case: 相同键在五种不同主体/范围内分别独立保存。
			memoryTestScope(),
			{ScopeKey: "local", Kind: MemoryGlobal},
			{ScopeKey: "other", Kind: MemoryGlobal},
			{ScopeKey: "other", Kind: MemoryWorkspace, Workspace: "/w"},
			{ScopeKey: "local", Kind: MemoryWorkspace, Workspace: "/other"},
			{ScopeKey: "local", Kind: MemorySession, Workspace: "/w", SessionID: "session-a"},
		}
		for i, scope := range scopes {
			putTestMemory(t, ledger, memoryTestWrite(scope, "same-key", fmt.Sprintf("合成偏好 %d", i), 0))
		}
		for i, scope := range scopes {
			got, err := ledger.ListMemories(ctx, scope, MemoryLimits{})
			if err != nil || len(got) != 1 || got[0].Content != fmt.Sprintf("合成偏好 %d", i) {
				t.Fatalf("scope %d leaked or lost data: %v", i, err)
			}
		}
		// case: 同工作区另一个会话看不到 session-a 的私有项。
		other := MemoryScope{ScopeKey: "local", Kind: MemorySession, Workspace: "/w", SessionID: "session-b"}
		if got, err := ledger.ListMemories(ctx, other, MemoryLimits{}); err != nil || len(got) != 0 {
			t.Fatal("session memory leaked")
		}
		// case: 不存在与跨主体/工作区目标都返回相同会话错误，读写都不能抢占。
		for _, scope := range []MemoryScope{
			{ScopeKey: "other", Kind: MemorySession, Workspace: "/w", SessionID: "session-a"},
			{ScopeKey: "local", Kind: MemorySession, Workspace: "/other", SessionID: "session-a"},
			{ScopeKey: "local", Kind: MemorySession, Workspace: "/w", SessionID: "missing"},
		} {
			if _, err := ledger.PutMemory(ctx, memoryTestWrite(scope, "same-key", "越界", 0)); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("cross-session write: %v", err)
			}
			if got, err := ledger.ListMemories(ctx, scope, MemoryLimits{}); got != nil || !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("cross-session read: %v", err)
			}
		}
	})
}

// TestLongTermMemoryByteBoundaries 用中文/NUL 正文验证真实字节计量，
// 单条恰好达到上限可写，完整集合超限时不返回任意前缀。
func TestLongTermMemoryByteBoundaries(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		ledger := open(t).(LongTermMemoryStore)
		scope := memoryTestScope()
		// case: 独立计算协议规定的字段开销，恰好 8192 字节必须接受。
		overhead := len("local") + len("workspace") + len("/w") + len("edge") + len("user") + 24
		content := "中\x00" + strings.Repeat("x", 8192-overhead-len("中\x00"))
		putTestMemory(t, ledger, memoryTestWrite(scope, "edge", content, 0))
		if got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{MaxBytes: 8192}); err != nil || len(got) != 1 || got[0].Content != content {
			t.Fatal("exact UTF-8 byte boundary was rejected")
		}
		if got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{MaxBytes: 8191}); got != nil || !errors.Is(err, ErrMemoryLimit) {
			t.Fatal("UTF-8 or NUL bypassed read limit")
		}
		if _, err := ledger.PutMemory(context.Background(), memoryTestWrite(scope, "edge", content+"x", 1)); !errors.Is(err, ErrMemoryLimit) {
			t.Fatal("oversized single record accepted")
		}
		// case: 未到条数上限，集合已经超过 64KiB，仍整次拒绝。
		for i := range 10 {
			putTestMemory(t, ledger, memoryTestWrite(scope, fmt.Sprintf("big-%02d", i), strings.Repeat("界", 2000), 0))
		}
		if got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{}); got != nil || !errors.Is(err, ErrMemoryLimit) {
			t.Fatal("aggregate byte overflow returned partial memory")
		}
	})
}

// TestLongTermMemoryLiteralIdentityAndPendingSession 守住绑定尚未落元数据
// 的空会话不能暗中持久化记忆，身份和键中的 SQL 字符也只是普通文本。
func TestLongTermMemoryLiteralIdentityAndPendingSession(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		s := open(t)
		ledger := s.(LongTermMemoryStore)
		ctx := context.Background()
		owner := Scope{ScopeKey: "local", Workspace: "/w"}
		if _, _, err := s.(ManagementStore).ApplyCommand(ctx, CommandMutation{
			Receipt: testReceipt(owner, "new", "new", time.Now()), BindSessionID: "pending",
		}); err != nil {
			t.Fatal(err)
		}
		// case: pending binding 不等于已有 session metadata，拒绝不创建孤儿记录。
		private := MemoryScope{ScopeKey: "local", Kind: MemorySession, Workspace: "/w", SessionID: "pending"}
		if _, err := ledger.PutMemory(ctx, memoryTestWrite(private, "k", "合成偏好", 0)); !errors.Is(err, ErrSessionNotFound) {
			t.Fatal("pending session silently registered memory")
		}
		// case: SQL 元字符与通配符必须精确过滤，不能改变集合范围。
		scope := MemoryScope{ScopeKey: "owner' OR 1=1 --", Kind: MemoryWorkspace, Workspace: "/w'; DROP TABLE sessions; --"}
		putTestMemory(t, ledger, memoryTestWrite(scope, "key'_%", "合成字面值", 0))
		got, err := ledger.ListMemories(ctx, scope, MemoryLimits{})
		if err != nil || len(got) != 1 || got[0].Key != "key'_%" {
			t.Fatal("literal memory identity changed")
		}
		if got, err := ledger.ListMemories(ctx, memoryTestScope(), MemoryLimits{}); err != nil || len(got) != 0 {
			t.Fatal("literal identity escaped its scope")
		}
	})
}

// TestLongTermMemoryRejectsInvalidWrites 守住非法输入在写入前拒绝，既有
// 条目不变；错误文本只提供固定类别，不回显可能敏感的内容。
func TestLongTermMemoryRejectsInvalidWrites(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		ledger := open(t).(LongTermMemoryStore)
		scope := memoryTestScope()
		first := putTestMemory(t, ledger, memoryTestWrite(scope, "stable", "原始合成偏好", 0))
		cases := []struct {
			name string
			edit func(*MemoryWrite)
			want error
		}{
			// case: 各必填字段、类型、UTF-8 与身份格式独立拒绝。
			{"empty-owner", func(w *MemoryWrite) { w.Scope.ScopeKey = "" }, ErrInvalidMemory},
			{"unknown-scope", func(w *MemoryWrite) { w.Scope.Kind = "unknown" }, ErrInvalidMemory},
			{"global-path", func(w *MemoryWrite) { w.Scope.Kind = MemoryGlobal }, ErrInvalidMemory},
			{"relative-workspace", func(w *MemoryWrite) { w.Scope.Workspace = "." }, ErrInvalidMemory},
			{"unclean-workspace", func(w *MemoryWrite) { w.Scope.Workspace = "/w/../w" }, ErrInvalidMemory},
			{"unexpected-session", func(w *MemoryWrite) { w.Scope.SessionID = "s" }, ErrInvalidMemory},
			{"empty-key", func(w *MemoryWrite) { w.Key = "" }, ErrInvalidMemory},
			{"control-key", func(w *MemoryWrite) { w.Key = "a\x00b" }, ErrInvalidMemory},
			{"blank-content", func(w *MemoryWrite) { w.Content = " \n\t" }, ErrInvalidMemory},
			{"invalid-utf8", func(w *MemoryWrite) { w.Content = string([]byte{0xff}) }, ErrInvalidMemory},
			{"automatic-source", func(w *MemoryWrite) { w.Source = "model" }, ErrInvalidMemory},
			{"negative-revision", func(w *MemoryWrite) { w.ExpectedRevision = -1 }, ErrInvalidMemory},
			{"oversized", func(w *MemoryWrite) { w.Content = strings.Repeat("私", 3000) }, ErrMemoryLimit},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				write := memoryTestWrite(scope, "stable", "SENSITIVE-MEMORY-SENTINEL", 1)
				tc.edit(&write)
				if _, err := ledger.PutMemory(context.Background(), write); !errors.Is(err, tc.want) || strings.Contains(err.Error(), "SENSITIVE") {
					t.Fatalf("invalid write error: %v", err)
				}
			})
		}
		got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{})
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], first) {
			t.Fatal("rejected writes polluted existing memory")
		}
	})
}

// TestLongTermMemoryReadLimits 守住条数和真实 UTF-8 字节双边界，超限
// 返回 nil 而非部分数据；非法上限不被静默放宽。
func TestLongTermMemoryReadLimits(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		ledger := open(t).(LongTermMemoryStore)
		scope := memoryTestScope()
		putTestMemory(t, ledger, memoryTestWrite(scope, "a", "中文边界", 0))
		for _, limits := range []MemoryLimits{
			// case: 单条字节超限、负数和试图放宽硬上限都必须拒绝。
			{MaxBytes: 1}, {MaxBytes: -1}, {MaxRecords: -1}, {MaxRecords: 101}, {MaxBytes: 65537},
		} {
			if got, err := ledger.ListMemories(context.Background(), scope, limits); got != nil || !errors.Is(err, ErrMemoryLimit) {
				t.Fatalf("invalid read limits accepted: %v", err)
			}
		}
		putTestMemory(t, ledger, memoryTestWrite(scope, "b", "另一个条目", 0))
		if got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{MaxRecords: 1}); got != nil || !errors.Is(err, ErrMemoryLimit) {
			t.Fatal("record overflow returned partial memory")
		}
		// case: 默认 100 条硬边界不能变成静默 LIMIT 100。
		for i := 2; i < 101; i++ {
			putTestMemory(t, ledger, memoryTestWrite(scope, fmt.Sprintf("key-%03d", i), "合成内容", 0))
		}
		if got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{}); got != nil || !errors.Is(err, ErrMemoryLimit) {
			t.Fatal("default record limit silently truncated")
		}
	})
}

// TestLongTermMemoryConcurrencyAndCancellation 守住并发首次写入只产生
// 一个版本，竞争更新只有一个获胜；已取消操作不写入、不返回读取残片。
func TestLongTermMemoryConcurrencyAndCancellation(t *testing.T) {
	t.Setenv("PLUME_HOME", t.TempDir())
	runContract(t, func(t *testing.T, open func(*testing.T) Store) {
		ledger := open(t).(LongTermMemoryStore)
		scope := memoryTestScope()
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				if _, err := ledger.PutMemory(context.Background(), memoryTestWrite(scope, "a", "同一内容", 0)); err != nil {
					t.Errorf("concurrent replay: %v", err)
				}
			})
		}
		wg.Wait()
		results := make(chan error, 2)
		for _, content := range []string{"更新一", "更新二"} {
			wg.Go(func() {
				_, err := ledger.PutMemory(context.Background(), memoryTestWrite(scope, "a", content, 1))
				results <- err
			})
		}
		wg.Wait()
		close(results)
		conflicts := 0
		for err := range results {
			if errors.Is(err, ErrMemoryConflict) {
				conflicts++
			} else if err != nil {
				t.Fatal(err)
			}
		}
		if conflicts != 1 {
			t.Fatal("CAS did not serialize competing updates")
		}
		// case: 取消发生在操作前，两个后端都保持原有版本。
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ledger.PutMemory(ctx, memoryTestWrite(scope, "a", "取消后写入", 2)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write: %v", err)
		}
		if got, err := ledger.ListMemories(ctx, scope, MemoryLimits{}); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read: %v", err)
		}
		got, err := ledger.ListMemories(context.Background(), scope, MemoryLimits{})
		if err != nil || len(got) != 1 || got[0].Revision != 2 {
			t.Fatal("cancelled operation changed memory")
		}
	})
}
