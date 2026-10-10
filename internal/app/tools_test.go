package app

import (
	"context"
	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"strings"
	"testing"
	"time"
)

// TestG3CommitsFullTurnAndCountsCalls 守住工具轮次的原子提交：整轮消息（含工具调用与结果）
// 进入历史、用量按调用去重累计，且 History 返回副本、外部改动不污染会话。
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

// TestG3RunDeadlineUnblocksBackpressure 守住 run 期限：到期必须能突破被填满的事件队列自行
// 终结，恰好产生一个超时失败终态且不提交历史。
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

// TestG3DeadlineStartsBeforeStartedEventDelivery 守住期限起点：计时从 run 启动开始、早于
// 事件投递，队列阻塞不推迟期限，过期的准备阶段不再调用模型。
func TestG3DeadlineStartsBeforeStartedEventDelivery(t *testing.T) {
	runtime := agent.New(model.NewFake(), "m")
	limits := agent.DefaultLimits()
	limits.RunTimeout = 20 * time.Millisecond
	runtime.SetLimits(limits)
	s := NewService(runtime)
	defer s.Close()
	for range eventBuffer {
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
