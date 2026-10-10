package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
)

// blockingFake 先阻塞到 release 再返回，模拟慢模型。
type blockingFake struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	calls   []model.ChatRequest
}

func newBlockingFake() *blockingFake {
	return &blockingFake{started: make(chan struct{}), release: make(chan struct{})}
}

func (f *blockingFake) Generate(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	close(f.started)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "late"}}, nil
}

func (f *blockingFake) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	resp, err := f.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.FinishReason = model.FinishStop
	return model.NewFake(model.FakeScript{Response: resp}).Stream(ctx, req)
}

func (f *blockingFake) releaseNow() {
	select {
	case <-f.release:
	default:
		close(f.release)
	}
}

// drainEvents 读取事件直到出现终态或超时。
func drainEvents(t *testing.T, svc *Service) []Event {
	t.Helper()
	var events []Event
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-svc.Events():
			events = append(events, ev)
			if ev.Kind == EventRunCompleted || ev.Kind == EventRunFailed {
				return events
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal event, got %v", events)
		}
	}
}

// TestServiceHappyPathCommitsHistory 守住成功闭环：事件序列 started→completed、终态后回到
// 空闲，历史提交后下一轮请求携带完整上下文（system+历史+新输入）。
func TestServiceHappyPathCommitsHistory(t *testing.T) {
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "answer one"}}},
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "answer two"}}},
	)
	svc := NewService(agent.New(fake, "m"))
	defer svc.Close()
	ctx := context.Background()

	runID, err := svc.Submit(ctx, "hello")
	if err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	if runID == "" {
		t.Fatal("run id must not be empty")
	}
	events := drainEvents(t, svc)
	if events[0].Kind != EventRunStarted || events[len(events)-1].Kind != EventRunCompleted {
		t.Fatalf("events = %v, want started then completed", events)
	}
	if events[len(events)-1].Reply != "answer one" {
		t.Errorf("reply = %q", events[len(events)-1].Reply)
	}
	if svc.Busy() {
		t.Error("service must be idle after terminal event")
	}

	// 连续第二轮必须引用上一轮：fake 收到的 messages 含第一轮 user+assistant。
	if _, err := svc.Submit(ctx, "again"); err != nil {
		t.Fatalf("second Submit error = %v", err)
	}
	drainEvents(t, svc)
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("call count = %d", len(calls))
	}
	if len(calls[1].Messages) != 4 { // system+user+assistant+user("again")
		t.Errorf("second call messages = %d, want 4 (history carried over)", len(calls[1].Messages))
	}
	if svc.Session().Turns() != 2 {
		t.Errorf("turns = %d, want 2", svc.Session().Turns())
	}
}

// TestServiceRejectsConcurrentSubmit 守住同会话串行契约：run 执行中再次 Submit 被拒绝为
// ErrBusy，不产生新请求。
func TestServiceRejectsConcurrentSubmit(t *testing.T) {
	blocking := newBlockingFake()
	svc := NewService(agent.New(blocking, "m"))
	defer func() {
		blocking.releaseNow()
		svc.Close()
	}()

	if _, err := svc.Submit(context.Background(), "first"); err != nil {
		t.Fatalf("first Submit error = %v", err)
	}
	<-blocking.started

	if _, err := svc.Submit(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent Submit error = %v, want ErrBusy", err)
	}
	blocking.releaseNow()
	drainEvents(t, svc)
}

// TestServiceFailureKeepsHistoryClean 守住失败隔离：模型报错的 run 以失败终态透出模型错误码，
// 不写入会话历史。
func TestServiceFailureKeepsHistoryClean(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Err: model.NewError(model.ErrRateLimited)})
	svc := NewService(agent.New(fake, "m"))
	defer svc.Close()

	if _, err := svc.Submit(context.Background(), "hello"); err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	events := drainEvents(t, svc)
	last := events[len(events)-1]
	if last.Kind != EventRunFailed {
		t.Fatalf("terminal event = %v, want failed", last.Kind)
	}
	var mErr *model.Error
	if !errors.As(last.Err, &mErr) || mErr.Code != model.ErrRateLimited {
		t.Errorf("failure = %v, want rate_limited", last.Err)
	}
	if got := svc.Session().Turns(); got != 0 {
		t.Errorf("failed run must not enter history, turns = %d", got)
	}
}

// TestServiceCancelDoesNotCommit 守住取消不提交：被取消的 run 以取消错误收尾，不进入会话历史。
func TestServiceCancelDoesNotCommit(t *testing.T) {
	blocking := newBlockingFake()
	svc := NewService(agent.New(blocking, "m"))
	defer func() {
		blocking.releaseNow()
		svc.Close()
	}()

	if _, err := svc.Submit(context.Background(), "slow"); err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	<-blocking.started
	svc.Cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-svc.Events():
			if ev.Kind == EventRunFailed {
				if !strings.Contains(ev.Err.Error(), "cancel") {
					t.Errorf("failure = %v, want cancellation", ev.Err)
				}
				if svc.Session().Turns() != 0 {
					t.Errorf("cancelled run must not enter history")
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for cancelled terminal event")
		}
	}
}

// TestServiceResetSessionClearsContext 守住会话重置语义：Reset 后下一轮只携带基础规则与
// 当前输入，不串旧上下文。
func TestServiceResetSessionClearsContext(t *testing.T) {
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "a"}}},
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "b"}}},
	)
	svc := NewService(agent.New(fake, "m"))
	defer svc.Close()
	ctx := context.Background()

	_, _ = svc.Submit(ctx, "one")
	drainEvents(t, svc)
	svc.Session().Reset()

	_, _ = svc.Submit(ctx, "two")
	drainEvents(t, svc)
	calls := fake.Calls()
	// 重置后第二轮只有新输入，不串上下文。
	if len(calls[1].Messages) != 2 {
		t.Errorf("after reset messages = %d, want 2 (base rules and current input)", len(calls[1].Messages))
	}
}
