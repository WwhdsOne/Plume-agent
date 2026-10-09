package model

import (
	"context"
	"reflect"
	"testing"
)

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
