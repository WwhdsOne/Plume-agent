package model

import (
	"context"
	"errors"
	"testing"
)

func TestFakePlaysScriptsInOrder(t *testing.T) {
	fake := NewFake(
		FakeScript{Response: &ChatResponse{Message: Message{Role: RoleAssistant, Content: "first"}}},
		FakeScript{Err: NewError(ErrRateLimited)},
	)

	ctx := context.Background()
	resp, err := fake.Generate(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if resp.Message.Content != "first" {
		t.Fatalf("first response content = %q, want first", resp.Message.Content)
	}
	if resp.Protocol != ProtocolOpenAIChatCompletions {
		t.Errorf("fake should fill protocol, got %q", resp.Protocol)
	}

	if _, err := fake.Generate(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err == nil {
		t.Fatal("second call should surface scripted error")
	}

	if _, err := fake.Generate(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser}}}); !errors.Is(err, ErrFakeExhausted) {
		t.Fatalf("exhausted fake error = %v, want ErrFakeExhausted", err)
	}

	if calls := fake.Calls(); len(calls) != 3 {
		t.Errorf("fake recorded %d calls, want 3", len(calls))
	}
}

func TestFakeStreamPlaysDeltasAndFailure(t *testing.T) {
	fake := NewFake(FakeScript{Response: &ChatResponse{Message: Message{Content: "答案", Reasoning: "思考"}, FinishReason: FinishStop, Usage: Usage{OK: true, TotalTokens: 2}}})
	stream, err := fake.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var answer, reasoning string
	ended := 0
	for stream.Next(context.Background()) {
		e := stream.Event()
		answer += e.TextDelta
		reasoning += e.ReasoningDelta
		if e.Kind == EventStreamEnded {
			ended++
		}
	}
	if stream.Err() != nil || answer != "答案" || reasoning != "思考" || ended != 1 {
		t.Fatalf("answer=%q reasoning=%q ended=%d err=%v", answer, reasoning, ended, stream.Err())
	}
}

func TestFakeStreamCancellationAndPartialFailure(t *testing.T) {
	gate := make(chan struct{})
	fake := NewFake(FakeScript{Stream: []FakeStep{{Event: Event{Kind: EventTextDelta, TextDelta: "partial"}}, {Wait: gate, Event: Event{Kind: EventTextDelta, TextDelta: "late"}}}})
	stream, err := fake.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Next(context.Background()) {
		t.Fatal("missing partial")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if stream.Next(ctx) {
		t.Fatal("cancel delivered event")
	}
	var e *Error
	if !errors.As(stream.Err(), &e) || e.Code != ErrCancelled {
		t.Fatalf("err=%v", stream.Err())
	}
	fake = NewFake(FakeScript{Stream: []FakeStep{{Event: Event{Kind: EventTextDelta, TextDelta: "partial"}}}, StreamErr: NewError(ErrStreamInterrupt)})
	stream, err = fake.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Next(context.Background()) || stream.Next(context.Background()) {
		t.Fatal("wrong scripted events")
	}
	if !errors.As(stream.Err(), &e) || e.Code != ErrStreamInterrupt {
		t.Fatalf("err=%v", stream.Err())
	}
	_ = stream.Close()
	_ = stream.Close()
}

func TestFakeConcurrentGenerate(t *testing.T) {
	// Generate 与 Calls 的并发访问必须安全（-race 验收覆盖）。
	fake := NewFake(FakeScript{Response: &ChatResponse{}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = fake.Generate(context.Background(), ChatRequest{})
	}()
	_ = fake.Calls()
	<-done
}

func TestLoopFakeEmptyResponseUsesOfflineAnswer(t *testing.T) {
	f := NewLoopFake(FakeScript{Response: &ChatResponse{}})
	s, err := f.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var answer string
	for s.Next(context.Background()) {
		answer += s.Event().TextDelta
	}
	if s.Err() != nil || answer != OfflineReply {
		t.Fatalf("answer=%q err=%v", answer, s.Err())
	}
}
