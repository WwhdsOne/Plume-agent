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

// reviewTrackedClient 记录流的实际关闭，以区别模型资源释放和 producer 退出。
type reviewTrackedClient struct {
	model.Client
	closed chan struct{}
}

func (c reviewTrackedClient) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	stream, err := c.Client.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &reviewTrackedStream{EventStream: stream, closed: c.closed}, nil
}

type reviewTrackedStream struct {
	model.EventStream
	closed chan struct{}
	once   sync.Once
}

func (s *reviewTrackedStream) Close() error {
	err := s.EventStream.Close()
	s.once.Do(func() { close(s.closed) })
	return err
}

// TestReviewCancelFullQueueTerminatesProducer 守住满队列下的取消：producer 不依赖 UI
// 消费即可退出并关闭模型流，终态占用预留槽位、已入队事件不丢失，取消的 run 不提交历史。
func TestReviewCancelFullQueueTerminatesProducer(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message: model.Message{Content: strings.Repeat("a", 10000)}, FinishReason: model.FinishStop,
	}})
	closed := make(chan struct{})
	s := NewService(agent.New(reviewTrackedClient{Client: fake, closed: closed}, "m"))
	defer s.Close()
	runID, err := s.Submit(context.Background(), "input")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(s.events) < eventBuffer && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.events) != eventBuffer {
		t.Fatal("nonterminal queue did not fill")
	}
	s.Cancel()
	idle := make(chan struct{})
	go func() { s.waitIdle(); close(idle) }()
	select {
	case <-idle:
	case <-time.After(time.Second):
		t.Fatal("cancelled producer did not exit without UI consumption")
	}
	select {
	case <-closed:
	default:
		t.Fatal("producer exited without closing model stream")
	}
	if s.Busy() {
		t.Fatal("cancelled run remains busy")
	}
	if len(s.events) != cap(s.events) {
		t.Fatal("cancelled terminal did not occupy the reserved slot")
	}
	if _, err := s.Submit(context.Background(), "retry"); !errors.Is(err, ErrBusy) {
		t.Fatalf("full queue accepted another run: %v", err)
	}
	if len(fake.Calls()) != 1 {
		t.Fatal("rejected retry called model")
	}
	events := drainEvents(t, s)
	if len(events) != eventBuffer+1 {
		t.Fatalf("queued nonterminal events were lost: got %d", len(events))
	}
	var text string
	for i, event := range events {
		if event.RunID != runID {
			t.Fatalf("event %d belongs to another run: %q", i, event.RunID)
		}
		text += event.TextDelta
		if i < len(events)-1 && (event.Kind == EventRunFailed || event.Kind == EventRunCompleted) {
			t.Fatal("terminal overtook accepted events")
		}
	}
	terminal := events[len(events)-1]
	code := model.ClassifyContext(terminal.Err)
	if modelErr, ok := errors.AsType[*model.Error](terminal.Err); ok {
		code = modelErr.Code
	}
	if terminal.Kind != EventRunFailed || code != model.ErrCancelled {
		t.Fatalf("wrong cancellation terminal: %+v", terminal)
	}
	if text == "" || !strings.HasPrefix(terminal.Reply, text) {
		t.Fatal("accepted answer deltas were lost or reordered")
	}
	if len(s.events) != 0 || s.Session().Turns() != 0 {
		t.Fatal("cancelled run has extra events or committed history")
	}
}

// TestReviewCancelledBeforeCallReportsPreparation 守住模型调用前的取消：不发起模型请求，
// 终态仍报告 preparing 阶段耗时，且不出现首思考/首答案等模型内容延迟。
func TestReviewCancelledBeforeCallReportsPreparation(t *testing.T) {
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{
		Message: model.Message{Content: "answer"}, FinishReason: model.FinishStop,
	}})
	s := NewService(agent.New(fake, "m"))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Submit(ctx, "input"); err != nil {
		t.Fatal(err)
	}
	events := drainEvents(t, s)
	terminal := events[len(events)-1]
	if len(fake.Calls()) != 0 {
		t.Fatal("called model after cancellation")
	}
	if terminal.Kind != EventRunFailed || terminal.Preparation <= 0 || terminal.Preparation != terminal.Duration {
		t.Fatalf("pre-call cancellation lost preparing duration: %+v", terminal)
	}
	if terminal.FirstAnswer != 0 || terminal.FirstReasoning != 0 {
		t.Fatal("pre-call cancellation reported model content latency")
	}
}
