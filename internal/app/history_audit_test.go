package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/tools"
)

// 故障注入：提交已经成功，调用方却收到不确定的提交错误。
type committedErrorStore struct{ store.Store }

func (s *committedErrorStore) CompleteRun(ctx context.Context, r store.RunRecord, m []model.Message) error {
	if err := s.Store.CompleteRun(ctx, r, m); err != nil {
		return err
	}
	return errors.New("commit acknowledgement unavailable")
}

// 审核复现：提交结果不确定时应查明成功或阻止继续，不能分叉历史。
func TestAuditCommitUncertaintyCannotForkHistory(t *testing.T) {
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "a1"}, FinishReason: model.FinishStop}},
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "a2"}, FinishReason: model.FinishStop}},
	)
	svc, base := newPersistedService(t, fake)
	defer svc.Close()
	svc.EnablePersistence(&committedErrorStore{base}, "/ws", store.ModelRef{}, time.Second)
	if _, err := svc.Submit(context.Background(), "q1"); err != nil {
		t.Fatal(err)
	}
	drainEvents(t, svc)
	svc.waitIdle()
	saved, _, err := base.LoadTurns(context.Background(), svc.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("invalid fixture: saved %d messages", len(saved))
	}
	if _, err := svc.Submit(context.Background(), "q2"); err != nil {
		return
	}
	drainEvents(t, svc)
	svc.waitIdle()
	requests := fake.Calls()
	if len(requests) != 2 {
		t.Fatalf("expected two requests, got %d", len(requests))
	}
	for _, m := range requests[1].Messages {
		if m.Content == "q1" {
			return
		}
	}
	t.Fatal("database has q1/a1 but accepted q2 without q1 history; no reconciliation or block")
}

// 审核复现：新会话必须重置先读后改缓存，不能继承上个会话的实际读取证明。
func TestAuditNewSessionRequiresFreshRead(t *testing.T) {
	o := tools.DefaultWorkspaceOptions()
	o.Root = t.TempDir()
	registry, err := tools.NewWorkspace(o)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	ctx := context.Background()
	if got := registry.Execute(ctx, "write", `{"path":"a.txt","content":"old"}`); !got.OK {
		t.Fatalf("create fixture: %s", got.JSON())
	}
	if got := registry.Execute(ctx, "read", `{"path":"a.txt"}`); !got.OK {
		t.Fatalf("read fixture: %s", got.JSON())
	}
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "write-new", Name: "write", Arguments: `{"path":"a.txt","content":"new","overwrite":true}`}}}, FinishReason: model.FinishToolCalls}},
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "done"}, FinishReason: model.FinishStop}},
	)
	runtime := agent.New(fake, "fake")
	runtime.SetTools(registry)
	svc := NewService(runtime)
	defer svc.Close()
	if err := svc.NewSession(); err != nil {
		t.Fatal(err)
	}
	result, err := svc.runtime.Run(ctx, svc.Session().History(), "overwrite the file")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range result.Messages {
		if m.Role == model.RoleTool && m.ToolCallID == "write-new" {
			if !strings.Contains(m.Content, "read_required") {
				t.Fatalf("new session reused old read proof: %s", m.Content)
			}
			return
		}
	}
	t.Fatal("fixture produced no write result")
}

// unverifiableStore 注入提交与核查均失败，验证执行权冻结而非猜测未提交。
type unverifiableStore struct{ store.Store }

func (s *unverifiableStore) CompleteRun(context.Context, store.RunRecord, []model.Message) error {
	return errors.New("commit outcome unavailable")
}

func (s *unverifiableStore) GetRunState(context.Context, string) (store.RunState, error) {
	return store.RunState{}, errors.New("lookup unavailable")
}

// TestHistoryUnknownCommitFreezesRunAndSession 守住核查失败后的运行和切换准入。
func TestHistoryUnknownCommitFreezesRunAndSession(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "done"}, FinishReason: model.FinishStop}})
	svc, base := newPersistedService(t, fake)
	defer svc.Close()
	svc.EnablePersistence(&unverifiableStore{base}, "/ws", store.ModelRef{}, time.Second)
	originalID := svc.SessionID()
	if _, err := svc.Submit(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	event := terminal(t, drainEvents(t, svc))
	svc.waitIdle()
	if event.Kind != EventRunFailed || !errors.Is(event.Err, ErrHistoryUncertain) {
		t.Fatalf("unconfirmed event=%+v", event)
	}
	if _, err := svc.Submit(context.Background(), "another"); !errors.Is(err, ErrHistoryUncertain) {
		t.Fatalf("Submit error=%v", err)
	}
	if err := svc.NewSession(); !errors.Is(err, ErrHistoryUncertain) {
		t.Fatalf("NewSession error=%v", err)
	}
	if err := svc.AdoptSession("new", NewSession()); !errors.Is(err, ErrHistoryUncertain) {
		t.Fatalf("AdoptSession error=%v", err)
	}
	if svc.SessionID() != originalID || svc.Session().Turns() != 0 || len(fake.Calls()) != 1 {
		t.Fatal("uncertain run changed memory or sent another request")
	}
}
