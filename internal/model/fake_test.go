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

func TestFakeStreamUnsupported(t *testing.T) {
	fake := NewFake()
	_, err := fake.Stream(context.Background(), ChatRequest{})
	var mErr *Error
	if !errors.As(err, &mErr) {
		t.Fatalf("Stream error = %v, want *model.Error", err)
	}
	if mErr.Code != ErrUnsupported {
		t.Errorf("Stream error code = %q, want unsupported", mErr.Code)
	}
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
