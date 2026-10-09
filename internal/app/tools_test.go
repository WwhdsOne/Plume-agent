package app

import (
	"context"
	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"strings"
	"testing"
	"time"
)

func TestG3CommitsFullTurnAndCountsCalls(t *testing.T) {
	cached := int64(4)
	fake := model.NewFake(
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Reasoning: "retained", ToolCalls: []model.ToolCall{{ID: "c", Name: "calculate", Arguments: `{"operation":"add","a":1,"b":2}`}}}, FinishReason: model.FinishToolCalls, Usage: model.Usage{OK: true, PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, CachedPromptTokens: &cached}}},
		model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant, Content: "3"}, FinishReason: model.FinishStop, Usage: model.Usage{OK: true, PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22, CachedPromptTokens: &cached}}},
	)
	s := NewService(agent.New(fake, "m"))
	defer s.Close()
	_, err := s.Submit(context.Background(), "1+2")
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-s.Events():
			if e.Kind == EventRunFailed {
				t.Fatal(e.Err)
			}
			if e.Kind == EventRunCompleted {
				history := s.Session().History()
				if len(history) != 4 || history[1].ToolCalls[0].ID != "c" || history[2].ToolCallID != "c" {
					t.Fatalf("history = %+v", history)
				}
				stats := s.Session().Statistics()
				if stats.Calls != 2 || stats.PromptTokens != 30 || stats.LastUsage.PromptTokens != 20 {
					t.Fatalf("stats = %+v", stats)
				}
				history[1].ToolCalls[0].ID = "mutated"
				if s.Session().History()[1].ToolCalls[0].ID != "c" {
					t.Fatal("history shares mutable tool calls")
				}
				return
			}
		case <-timer.C:
			t.Fatal("run did not complete")
		}
	}
}

func TestG3RunDeadlineUnblocksBackpressure(t *testing.T) {
	runtime := agent.New(model.NewFake(model.FakeScript{Response: &model.ChatResponse{Message: model.Message{Content: strings.Repeat("long answer ", 100)}, FinishReason: model.FinishStop}}), "m")
	limits := agent.DefaultLimits()
	limits.RunTimeout = 20 * time.Millisecond
	runtime.SetLimits(limits)
	s := NewService(runtime)
	defer s.Close()
	if _, err := s.Submit(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(200 * time.Millisecond)
	for s.Busy() {
		select {
		case <-deadline:
			s.Cancel()
			t.Fatal("run deadline cannot escape full UI event buffer")
		case <-time.After(time.Millisecond):
		}
	}
	ended := 0
	for len(s.events) > 0 {
		e := <-s.Events()
		if e.Kind == EventRunFailed {
			ended++
			if model.ClassifyContext(e.Err) != model.ErrTimeout {
				t.Fatalf("terminal %v", e.Err)
			}
		}
	}
	if ended != 1 || s.Session().Turns() != 0 {
		t.Fatal("deadline did not produce one failed terminal and isolated history")
	}
}

func TestG3DeadlineStartsBeforeStartedEventDelivery(t *testing.T) {
	runtime := agent.New(model.NewFake(), "m")
	limits := agent.DefaultLimits()
	limits.RunTimeout = 20 * time.Millisecond
	runtime.SetLimits(limits)
	s := NewService(runtime)
	defer s.Close()
	for i := 0; i < eventBuffer; i++ {
		s.events <- Event{Kind: EventRunPhase}
	}
	if _, err := s.Submit(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(200 * time.Millisecond)
	for s.Busy() {
		select {
		case <-deadline:
			s.Cancel()
			t.Fatal("started event delivery bypasses run deadline")
		case <-time.After(time.Millisecond):
		}
	}
	if s.Session().Statistics().Calls != 0 {
		t.Fatal("model called despite expired preparation")
	}
}
