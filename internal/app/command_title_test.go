package app

import (
	"context"
	"errors"
	"testing"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// 首个已登记的 run 固定自动标题；默认文案、空白和模型失败都不能让后续输入改名。
func TestCommandAutomaticTitleUsesFirstAcceptedRun(t *testing.T) {
	for _, test := range []struct {
		name        string
		first       string
		firstFails  bool
		modelFails  bool
		manualTitle string
		wantTitle   string
	}{
		// case: 自动标题恰好等于默认文案，也已经完成首次定名。
		{name: "default text", first: DefaultSessionTitle, wantTitle: DefaultSessionTitle},
		// case: 非空的纯空白正文仍属于首次普通输入。
		{name: "whitespace", first: " \t\n ", firstFails: true, wantTitle: DefaultSessionTitle},
		// case: 标题在起点确定，不能等成功轮次出现才固定。
		{name: "failed default text", first: DefaultSessionTitle, firstFails: true, modelFails: true, wantTitle: DefaultSessionTitle},
		// case: 普通自动标题在失败之后保持，恢复也不能重新定名。
		{name: "failed ordinary text", first: "首个问题", firstFails: true, modelFails: true, wantTitle: "首个问题"},
		// case: 手工标题即使等于默认文案也不被首次正文覆盖。
		{name: "manual default text", first: "首个问题", manualTitle: DefaultSessionTitle, wantTitle: DefaultSessionTitle},
	} {
		t.Run(test.name, func(t *testing.T) {
			commandBackends(t, func(t *testing.T, backend store.Store) {
				answer := model.FakeScript{Response: &model.ChatResponse{
					Message: model.Message{Role: model.RoleAssistant, Content: "answer"}, FinishReason: model.FinishStop,
				}}
				first := answer
				if test.modelFails {
					first = model.FakeScript{Err: errors.New("injected model failure")}
				}
				service := commandService(t, backend, LocalScope(), model.NewFake(first, answer, answer, answer))
				if test.manualTitle != "" {
					if result := service.HandleInput(context.Background(), CommandRequest{Text: "/rename " + test.manualTitle}); result.Code != "ok" {
						t.Fatal(result)
					}
				}
				if _, err := service.Submit(context.Background(), test.first); err != nil {
					t.Fatal(err)
				}
				firstEvent := terminal(t, drainEvents(t, service))
				if (firstEvent.Kind == EventRunFailed) != test.firstFails {
					t.Fatalf("first terminal event = %+v", firstEvent)
				}
				if test.firstFails && service.Session().Turns() != 0 {
					t.Fatal("failed first run unexpectedly committed a turn")
				}
				assertCommandTitle(t, service, backend, test.wantTitle)
				originalID := service.SessionID()
				if result := service.HandleInput(context.Background(), CommandRequest{Text: "/new"}); result.Code != "ok" {
					t.Fatal(result)
				}
				if result := service.HandleInput(context.Background(), CommandRequest{Text: "/resume " + originalID}); result.Code != "ok" {
					t.Fatal(result)
				}
				if _, err := service.Submit(context.Background(), "later input must not rename"); err != nil {
					t.Fatal(err)
				}
				terminal(t, drainEvents(t, service))
				assertCommandTitle(t, service, backend, test.wantTitle)
				if result := service.HandleInput(context.Background(), CommandRequest{Text: "/new"}); result.Code != "ok" {
					t.Fatal(result)
				}
				if _, err := service.Submit(context.Background(), "fresh title"); err != nil {
					t.Fatal(err)
				}
				terminal(t, drainEvents(t, service))
				assertCommandTitle(t, service, backend, "fresh title")
				if result := service.HandleInput(context.Background(), CommandRequest{Text: "/resume " + originalID}); result.Code != "ok" {
					t.Fatal(result)
				}
				if _, err := service.Submit(context.Background(), "resumed input must not rename"); err != nil {
					t.Fatal(err)
				}
				terminal(t, drainEvents(t, service))
				assertCommandTitle(t, service, backend, test.wantTitle)
			})
		})
	}
}

// 起点未落盘不算已接受的首次输入；重试必须仍能按新正文定名。
func TestCommandAutomaticTitleRetriesAfterStartRunFailure(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		failing := &failStore{Store: backend, failStartRun: true}
		history := struct {
			store.Store
			store.ManagementStore
		}{Store: failing, ManagementStore: backend.(store.ManagementStore)}
		client := commandLoopFake()
		service := commandService(t, history, LocalScope(), client)
		if _, err := service.Submit(context.Background(), "unaccepted title"); err == nil {
			t.Fatal("StartRun failure unexpectedly accepted input")
		}
		status := service.HandleInput(context.Background(), CommandRequest{Text: "/status"})
		if status.Status == nil || status.Status.Title != DefaultSessionTitle || len(client.Calls()) != 0 {
			t.Fatalf("failed start changed title or called model: %+v", status)
		}
		failing.failStartRun = false
		if _, err := service.Submit(context.Background(), "retry title"); err != nil {
			t.Fatal(err)
		}
		terminal(t, drainEvents(t, service))
		assertCommandTitle(t, service, backend, "retry title")
	})
}

// -c 在命令装配前接管历史；即使只有失败 run 且标题仍为默认文案，也不能再次定名。
func TestCommandAutomaticTitlePreservesRestoredFailedRun(t *testing.T) {
	commandBackends(t, func(t *testing.T, backend store.Store) {
		original := commandService(t, backend, LocalScope(), model.NewFake(model.FakeScript{Err: errors.New("injected model failure")}))
		if _, err := original.Submit(context.Background(), DefaultSessionTitle); err != nil {
			t.Fatal(err)
		}
		if last := terminal(t, drainEvents(t, original)); last.Kind != EventRunFailed {
			t.Fatalf("first run was not failed: %+v", last)
		}
		restored := NewService(agent.New(commandLoopFake(), "test-model"))
		t.Cleanup(restored.Close)
		// 失败 run 无成功消息和用量；这里只重建 -c 在 ConfigureCommands 前的接管顺序。
		if err := restored.AdoptSession(original.SessionID(), NewSession()); err != nil {
			t.Fatal(err)
		}
		if err := restored.ConfigureCommands(original.commands.options); err != nil {
			t.Fatal(err)
		}
		if _, err := restored.Submit(context.Background(), "restored input must not rename"); err != nil {
			t.Fatal(err)
		}
		terminal(t, drainEvents(t, restored))
		assertCommandTitle(t, restored, backend, DefaultSessionTitle)
	})
}

// assertCommandTitle 同时检查当前状态、目录和后端，避免内存标题与已保存元数据分叉。
func assertCommandTitle(t *testing.T, service *Service, backend store.Store, want string) {
	t.Helper()
	status := service.HandleInput(context.Background(), CommandRequest{Text: "/status"})
	if status.Status == nil {
		t.Fatalf("status missing: %+v", status)
	}
	if status.Status.Title != want {
		t.Errorf("status title = %q, want %q", status.Status.Title, want)
	}
	list := service.HandleInput(context.Background(), CommandRequest{Text: "/sessions"})
	found := false
	if list.Sessions != nil {
		for _, item := range list.Sessions.Items {
			if item.ID == service.SessionID() {
				found = true
				if item.Title != want {
					t.Errorf("directory title = %q, want %q", item.Title, want)
				}
			}
		}
	}
	if !found {
		t.Errorf("current session missing from directory: %+v", list)
	}
	record, err := backend.(store.ManagementStore).ResolveSession(context.Background(), store.Scope{ScopeKey: LocalScopeKey, Workspace: "/same-workspace"}, service.SessionID())
	if err != nil || record.Title != want {
		t.Errorf("stored title = %q, error = %v, want %q", record.Title, err, want)
	}
}
