package app

import (
	"context"
	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"testing"
	"time"
)

type reviewCloseGateClient struct {
	model.Client
	entered, release chan struct{}
}

func (c reviewCloseGateClient) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	st, err := c.Client.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &reviewCloseGateStream{EventStream: st, entered: c.entered, release: c.release}, nil
}

type reviewCloseGateStream struct {
	model.EventStream
	entered, release chan struct{}
}

func (s *reviewCloseGateStream) Close() error {
	close(s.entered)
	<-s.release
	return s.EventStream.Close()
}
func TestQualityCancelDuringStreamClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fake := model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Content: "answer"}, FinishReason: model.FinishStop}})
	svc := NewService(agent.New(reviewCloseGateClient{Client: fake, entered: entered, release: release}, "m"))
	defer svc.Close()
	if _, err := svc.Submit(context.Background(), "input"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Close did not begin")
	}
	svc.Cancel()
	close(release)
	events := drainEvents(t, svc)
	last := events[len(events)-1]
	if last.Kind != EventRunFailed || svc.Session().Turns() != 0 {
		t.Fatalf("cancelled during stream Close but got terminal=%s err=%v turns=%d", last.Kind, last.Err, svc.Session().Turns())
	}
}
