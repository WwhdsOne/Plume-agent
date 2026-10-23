package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// fakeGateway 只模拟已经完成鉴权和归一化的渠道入口，不连接平台或发送消息。
type fakeGateway struct {
	service *Service
	scope   Scope
}

func (g fakeGateway) input(id, text string) CommandResult {
	return g.service.HandleInput(context.Background(), CommandRequest{RequestID: id, Text: text, Scope: g.scope, Entrance: EntranceGateway})
}

func commandBackends(t *testing.T, test func(*testing.T, store.Store)) {
	t.Helper()
	for _, name := range []string{"memory", "sqlite"} {
		// case: 两个后端必须遵守同一身份、事务和回执契约。
		t.Run(name, func(t *testing.T) {
			var backend store.Store = store.NewMemory()
			if name == "sqlite" {
				var err error
				backend, err = store.OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = backend.Close() })
			test(t, backend)
		})
	}
}

func commandService(t *testing.T, backend store.Store, scope Scope, client model.Client) *Service {
	t.Helper()
	build := func(context.Context, store.ModelRef) (*agent.Runtime, error) {
		return agent.New(client, "test-model"), nil
	}
	runtime, _ := build(context.Background(), store.ModelRef{})
	service := NewService(runtime)
	t.Cleanup(func() {
		service.Close()
		if err := service.CloseRuntimes(); err != nil {
			t.Error(err)
		}
	})
	if err := service.ConfigureCommands(CommandOptions{History: backend, Scope: scope, Workspace: "/same-workspace", PageSize: 2,
		ModelRef: store.ModelRef{ConfigID: "test", Provider: "fake", Model: "test-model"}, BuildRuntime: build,
		Models: []CommandModelInfo{{ID: "test", Provider: "fake", Model: "test-model"}}}); err != nil {
		t.Fatal(err)
	}
	return service
}

func gatewayScope() Scope {
	return Scope{Channel: "fake", AccountInstance: "account", Peer: "peer", Thread: "thread", Sender: "sender"}
}

func commandLoopFake() *model.LoopFake {
	return model.NewLoopFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "answer", Reasoning: "private thought"}, FinishReason: model.FinishStop}})
}

// 同工作目录不能跨发送者、线程、账号、渠道或本地身份恢复会话。
func TestFakeGatewayScopeIsolation(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		client := commandLoopFake()
		owner := fakeGateway{service: commandService(t, backend, gatewayScope(), client), scope: gatewayScope()}
		created := owner.input("new-secret", "/new secret")
		if created.Code != "ok" {
			t.Fatalf("create: %+v", created)
		}
		for _, field := range []string{"sender", "thread", "account", "channel", "peer", "local"} {
			// case: 权限域每一项不同都先过滤，再解析已知完整 ID。
			scope := gatewayScope()
			switch field {
			case "sender":
				scope.Sender = "other"
			case "thread":
				scope.Thread = "other"
			case "account":
				scope.AccountInstance = "other"
			case "channel":
				scope.Channel = "other"
			case "peer":
				scope.Peer = "other"
			case "local":
				scope = LocalScope()
			}
			service := commandService(t, backend, scope, client)
			entrance := EntranceGateway
			if scope.IsLocal() {
				entrance = EntranceTUI
			}
			result := service.HandleInput(context.Background(), CommandRequest{Text: "/resume " + created.SessionID, Scope: scope, Entrance: entrance})
			if result.Code != "session_not_found" {
				t.Fatalf("%s leaked session: %+v", field, result)
			}
		}
		if len(client.Calls()) != 0 {
			t.Fatal("management called model")
		}
		if got := owner.service.HandleInput(context.Background(), CommandRequest{Text: "/status", Entrance: EntranceGateway}); got.Code != "invalid_identity" {
			t.Fatalf("missing identity accepted: %+v", got)
		}
	})
}

// 假平台发送失败后仅重送已存结果；旧重命名也不能覆盖后续更改。
func TestFakeGatewayReceiptRetryDoesNotRepeatActions(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		client := commandLoopFake()
		gateway := fakeGateway{commandService(t, backend, gatewayScope(), client), gatewayScope()}
		first := gateway.input("new-1", "/new first")
		second := gateway.input("new-2", "/new second")
		resend := gateway.input("new-1", "/new first")
		if !resend.Replayed || resend.SessionID != first.SessionID || gateway.service.SessionID() != second.SessionID {
			t.Fatalf("duplicate new changed binding: %+v", resend)
		}
		if got := gateway.input("rename-1", "/rename one"); got.Code != "ok" {
			t.Fatal(got)
		}
		if got := gateway.input("rename-2", "/rename two"); got.Code != "ok" {
			t.Fatal(got)
		}
		if got := gateway.input("rename-1", "/rename one"); !got.Replayed {
			t.Fatal(got)
		}
		status := gateway.input("status", "/status")
		if status.Status == nil || status.Status.Title != "two" {
			t.Fatalf("old rename overwrote title: %+v", status)
		}
		for _, text := range []string{"/clear", "/exit", "/quit"} {
			if got := gateway.input(text, text); got.Code != "unsupported_entrance" {
				t.Fatal(got)
			}
		}
		if len(client.Calls()) != 0 {
			t.Fatal("receipt retry called model")
		}
	})
}

// 空闲取消和旧 run 取消的重投不能作用于后来启动的 run。
func TestFakeGatewayCancelReceiptLocksFirstTarget(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		wait := make(chan struct{})
		client := model.NewFake(
			model.FakeScript{Stream: []model.FakeStep{{Wait: wait, Event: model.Event{Kind: model.EventTextDelta, TextDelta: "late"}}}},
			model.FakeScript{Stream: []model.FakeStep{{Wait: wait, Event: model.Event{Kind: model.EventTextDelta, TextDelta: "later"}}}},
		)
		gateway := fakeGateway{commandService(t, backend, gatewayScope(), client), gatewayScope()}
		idle := gateway.input("idle-cancel", "/cancel")
		if idle.Code != "no_active_run" {
			t.Fatal(idle)
		}
		first := gateway.input("message-1", "first")
		if first.Code != "ok" || first.RunID == "" {
			t.Fatal(first)
		}
		if got := gateway.input("idle-cancel", "/cancel"); !got.Replayed || got.RunID != "" {
			t.Fatal(got)
		}
		if !gateway.service.Busy() {
			t.Fatal("old idle cancel stopped new run")
		}
		if got := gateway.input("busy-new", "/new"); got.Code != "busy" {
			t.Fatal(got)
		}
		cancel := gateway.input("active-cancel", "/cancel")
		if cancel.Code != "cancel_requested" || cancel.RunID != first.RunID {
			t.Fatal(cancel)
		}
		terminal(t, drainEvents(t, gateway.service))
		gateway.service.waitIdle()
		second := gateway.input("message-2", "second")
		if second.Code != "ok" || second.RunID == first.RunID {
			t.Fatal(second)
		}
		if got := gateway.input("active-cancel", "/cancel"); !got.Replayed || got.RunID != first.RunID {
			t.Fatal(got)
		}
		if !gateway.service.Busy() {
			t.Fatal("old targeted cancel stopped new run")
		}
		gateway.service.Cancel()
		terminal(t, drainEvents(t, gateway.service))
	})
}

// 平台展示历史不主动外发思考、工具详情或当前运行时的内部地址。
func TestFakeGatewayHistoryAndStatusRespectDisplayBoundary(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		gateway := fakeGateway{commandService(t, backend, gatewayScope(), commandLoopFake()), gatewayScope()}
		if result := gateway.input("question", "title from question"); result.Code != "ok" {
			t.Fatal(result)
		}
		terminal(t, drainEvents(t, gateway.service))
		gateway.service.waitIdle()
		history := gateway.input("history", "/history")
		if history.History == nil || len(history.History.Runs) != 1 || history.History.Runs[0].Reasoning != "" || len(history.History.Runs[0].ToolEvents) != 0 {
			t.Fatal(history)
		}
		encoded, err := json.Marshal(gateway.input("status", "/status"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "/same-workspace") || strings.Contains(string(encoded), "BaseURL") {
			t.Fatalf("status exposed private metadata: %s", encoded)
		}
		if result := gateway.input("rename", "/rename manual"); result.Code != "ok" {
			t.Fatal(result)
		}
		if result := gateway.input("q2", "do not override"); result.Code != "ok" {
			t.Fatal(result)
		}
		terminal(t, drainEvents(t, gateway.service))
		gateway.service.waitIdle()
		if status := gateway.input("status2", "/status"); status.Status.Title != "manual" {
			t.Fatal(status)
		}
	})
}

// managementFailure 在保持原有 run 持久化契约的同时注入管理事务失败。
type managementFailure struct {
	store.Store
	store.ManagementStore
	commitError bool
	lookupError bool
}

func (f *managementFailure) ApplyCommand(ctx context.Context, mutation store.CommandMutation) (store.Receipt, bool, error) {
	if f.commitError {
		_, _, err := f.ManagementStore.ApplyCommand(ctx, mutation)
		if err != nil {
			return store.Receipt{}, false, err
		}
	}
	return store.Receipt{}, false, errors.New("injected commit failure")
}

func (f *managementFailure) GetReceipt(ctx context.Context, scope store.Scope, id string) (store.Receipt, bool, error) {
	if f.lookupError {
		return store.Receipt{}, false, errors.New("injected query failure")
	}
	return f.ManagementStore.GetReceipt(ctx, scope, id)
}

// 管理事务失败保持当前会话；已提交但应答丢失则核查后安装同一结果。
func TestCommandCommitFailureAndAcknowledgementLoss(t *testing.T) {
	for _, committed := range []bool{false, true} {
		// case: 未提交保持原会话，已提交核查后同步内存，不重新建第二个会话。
		t.Run(map[bool]string{false: "not_committed", true: "committed"}[committed], func(t *testing.T) {
			base := store.NewMemory()
			backend := &managementFailure{Store: base, ManagementStore: base, commitError: committed}
			service := commandService(t, backend, LocalScope(), commandLoopFake())
			before := service.SessionID()
			result := service.HandleInput(context.Background(), CommandRequest{Text: "/new named", Entrance: EntranceTUI})
			if committed && (result.Code != "ok" || service.SessionID() == before) {
				t.Fatal(result)
			}
			if !committed && (result.Code != "storage_error" || service.SessionID() != before) {
				t.Fatal(result)
			}
		})
	}
}

// 所有事件固定准入时的会话身份；切换以后不能混入新会话的显示状态。
func TestRunEventsCarryAcceptedSessionID(t *testing.T) {
	service := commandService(t, store.NewMemory(), LocalScope(), commandLoopFake())
	id := service.SessionID()
	result := service.HandleInput(context.Background(), CommandRequest{Text: "hello", Entrance: EntranceTUI})
	if result.Code != "ok" {
		t.Fatal(result)
	}
	for _, event := range drainEvents(t, service) {
		if event.SessionID != id {
			t.Fatalf("event lost session identity: %+v", event)
		}
	}
	service.waitIdle()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if result := service.HandleInput(ctx, CommandRequest{Text: "/new", Entrance: EntranceTUI}); result.Code != "ok" {
		t.Fatal(result)
	}
}

// 独立审核发现：已保存的失败诊断也不能经成功的远端 /history 泄露本机路径。
func TestFakeGatewayHistoryHidesInternalFailureDiagnostics(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		scope := gatewayScope()
		service := commandService(t, backend, scope, commandLoopFake())
		key, _ := scope.Key()
		at := time.Now()
		record := store.SessionRecord{ID: service.SessionID(), ScopeKey: key, Workspace: "/same-workspace", CreatedAt: at, Title: "failure"}
		run := store.RunRecord{ID: "private-failure", SessionID: record.ID, Input: "hello", StartedAt: at}
		if err := backend.StartRun(context.Background(), record, run); err != nil {
			t.Fatal(err)
		}
		run.Status, run.FinishedAt, run.Error = store.RunFailed, at.Add(time.Second), "open /private/credentials/private-key: permission denied"
		if err := backend.FinishRun(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		result := (fakeGateway{service, scope}).input("history", "/history")
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if result.Code != "ok" || result.History == nil || strings.Contains(string(encoded), "/private/") || strings.Contains(string(encoded), "private-key") {
			t.Fatalf("internal diagnostics escaped: %s", encoded)
		}
		stored, err := backend.ListRuns(context.Background(), record.ID)
		if err != nil || len(stored) != 1 || !strings.Contains(stored[0].Error, "/private/") {
			t.Fatal("local diagnostic history was modified")
		}
	})
}
