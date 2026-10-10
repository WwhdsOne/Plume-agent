package model

import (
	"context"
	"reflect"
	"testing"
)

// 守住 fake 的用量快照透传契约：用量完整（OK）与否、有限脚本还是循环
// 脚本，Generate 与 Stream 的 usage update 都必须原样透传脚本快照，
// CachedPromptTokens 不得丢失或重算。
func TestFakePreservesCachedPromptUsage(t *testing.T) {
	for _, known := range []bool{true, false} {
		for _, loop := range []bool{true, false} {
			t.Run(fmtCacheCase(known, loop), func(t *testing.T) {
				cached := int64(6)
				usage := Usage{OK: known, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CachedPromptTokens: &cached}
				response := &ChatResponse{Message: Message{Content: "scripted answer"}, FinishReason: FinishStop, Usage: usage}
				newClient := func() Client {
					if loop {
						return NewLoopFake(FakeScript{Response: response})
					}
					return NewFake(FakeScript{Response: response})
				}
				generated, err := newClient().Generate(context.Background(), ChatRequest{})
				if err != nil || !reflect.DeepEqual(generated.Usage, usage) {
					t.Fatalf("Generate usage=%+v err=%v want=%+v", generated, err, usage)
				}
				stream, err := newClient().Stream(context.Background(), ChatRequest{})
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				updates := 0
				for stream.Next(context.Background()) {
					if event := stream.Event(); event.Kind == EventUsageUpdate {
						updates++
						if !reflect.DeepEqual(event.Usage, usage) {
							t.Fatalf("Stream usage=%+v want=%+v", event.Usage, usage)
						}
					}
				}
				if stream.Err() != nil || updates != 1 {
					t.Fatalf("usage updates=%d err=%v", updates, stream.Err())
				}
			})
		}
	}
}

func fmtCacheCase(known, loop bool) string {
	name := "partial"
	if known {
		name = "known"
	}
	if loop {
		return name + "-loop"
	}
	return name + "-finite"
}

// 守住 LoopFake 的降级边界：空脚本响应被替换成离线默认回复时，脚本里的
// 缓存用量（指向 0 也算已知）仍须作为 usage update 原样发出。
func TestLoopFakeDefaultAnswerPreservesCachedPromptUsage(t *testing.T) {
	cached := int64(0)
	usage := Usage{CachedPromptTokens: &cached}
	fake := NewLoopFake(FakeScript{Response: &ChatResponse{Usage: usage}})
	stream, err := fake.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var streamed Usage
	for stream.Next(context.Background()) {
		if stream.Event().Kind == EventUsageUpdate {
			streamed = stream.Event().Usage
		}
	}
	if stream.Err() != nil || !reflect.DeepEqual(streamed, usage) {
		t.Fatalf("default answer lost scripted cache usage=%+v err=%v", streamed, stream.Err())
	}
}
