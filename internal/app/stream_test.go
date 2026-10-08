package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
)

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
