package model

import (
	"context"
	"errors"
	"testing"
)

// 守住 Fake 的脚本语义：按序弹出、脚本错误上抛、耗尽报 ErrFakeExhausted；
// 响应补全协议标识，请求全量记录供 Calls 断言。
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

// 守住 Fake 的流式合成契约：完整响应自动拆成思考与答案增量，
// EventStreamEnded 恰好一次，正常结束无错误。
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

// 守住 fake 流的中断语义：等待中的事件可被取消打断并归类为 cancelled；
// 脚本 StreamErr 在放完既有事件后上抛；Close 可重复调用。
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

// 守住 Fake 的线程安全契约：Generate 执行期间并发调用 Calls 不得产生
// 数据竞争。
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

// 守住 LoopFake 的空响应降级：脚本响应无内容时，Stream 输出 OfflineReply
// 固定回复，离线模式始终有答案。
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
