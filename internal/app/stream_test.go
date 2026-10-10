package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
)

// TestStreamBackpressureKeepsEveryDeltaAndTerminal 守住背压不丢事件：UI 消费慢于生产时，
// 每个增量与终态都必须完整送达。
func TestStreamBackpressureKeepsEveryDeltaAndTerminal(t *testing.T) {
	want := strings.Repeat("羽毛", 200)
	f := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Content: want, Reasoning: "thought"}, FinishReason: model.FinishStop}})
	s := NewService(agent.New(f, "m"))
	defer s.Close()
	_, _ = s.Submit(context.Background(), "input")
	time.Sleep(20 * time.Millisecond)
	var got string
	for _, e := range drainEvents(t, s) {
		got += e.TextDelta
	}
	if got != want {
		t.Fatalf("received %d bytes, expected %d", len(got), len(want))
	}
}

// TestCloseReleasesBlockedProducer 守住退出路径：producer 被满队列阻塞时 Close 必须释放它，
// 且 Close 可重复调用（幂等）。
func TestCloseReleasesBlockedProducer(t *testing.T) {
	f := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Content: strings.Repeat("a", 10000)}, FinishReason: model.FinishStop}})
	s := NewService(agent.New(f, "m"))
	_, _ = s.Submit(context.Background(), "input")
	done := make(chan struct{})
	go func() { s.Close(); s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close blocked on full event queue")
	}
}
