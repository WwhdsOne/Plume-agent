package model

import (
	"context"
	"errors"
	"sync"
	"time"
)

// FakeScript 是 Fake 的一次脚本化响应：弹出时返回 Response 或 Err。
type FakeScript struct {
	Response  *ChatResponse
	Err       error
	Stream    []FakeStep
	StreamErr error
}

// Fake 是脚本化模型实现，与真实适配器实现同一 Client 接口，
// 供 TUI/app/agent 的离线测试使用。它只测本地运行时/协议/界面，
// 不推断真实模型质量（0003 §7 指标协议）。
type Fake struct {
	mu      sync.Mutex
	scripts []FakeScript
	calls   []ChatRequest
}

// NewFake 按顺序构造脚本。调用次数超过脚本数时报错终止。
func NewFake(scripts ...FakeScript) *Fake {
	return &Fake{scripts: scripts}
}

// Generate 弹出下一个脚本并记录请求。脚本耗尽时返回 ErrFakeExhausted。
func (f *Fake) Generate(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.scripts) == 0 {
		return nil, ErrFakeExhausted
	}
	script := f.scripts[0]
	f.scripts = f.scripts[1:]
	if script.Err != nil {
		return nil, script.Err
	}
	resp := script.Response
	if resp != nil && resp.Protocol == "" {
		resp.Protocol = ProtocolOpenAIChatCompletions
	}
	return resp, nil
}

// Stream 弹出一个可取消的拉取式脚本；未提供事件时从完整响应生成增量。
func (f *Fake) Stream(ctx context.Context, req ChatRequest) (EventStream, error) {
	if ctx.Err() != nil {
		return nil, NewError(ClassifyContext(ctx.Err()))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.scripts) == 0 {
		return nil, ErrFakeExhausted
	}
	script := f.scripts[0]
	f.scripts = f.scripts[1:]
	return newFakeStream(ctx, script)
}

// Calls 返回已收到的请求副本，供测试断言。
func (f *Fake) Calls() []ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ChatRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// ErrFakeExhausted 是 Fake 脚本耗尽时的哨兵错误。
var ErrFakeExhausted = errors.New("fake model has no scripted responses left")

// LoopFake 无限重复同一脚本响应，供 `plume chat --offline` 演示与
// 长时间 UI 测试使用：与 Fake 一样只测本地运行时/协议/界面。
type LoopFake struct {
	mu     sync.Mutex
	script FakeScript
	calls  []ChatRequest
}

// NewLoopFake 构造一个无限循环的 fake。响应缺省时补全协议标识。
func NewLoopFake(script FakeScript) *LoopFake {
	if script.Response != nil && script.Response.Protocol == "" {
		script.Response.Protocol = ProtocolOpenAIChatCompletions
	}
	return &LoopFake{script: script}
}

// OfflineReply 是 --offline 模式的默认固定回答。
const OfflineReply = "[offline] This is a scripted reply from the fake model. " +
	"No network, no key, no cost. Press Esc to cancel, Ctrl+N for a new session."

// Generate 记录请求并返回同一脚本响应。
func (f *LoopFake) Generate(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	resp := f.script.Response
	if resp == nil {
		resp = &ChatResponse{}
	}
	out := *resp
	out.Message = Message{Role: RoleAssistant, Content: OfflineReply}
	out.FinishReason = FinishStop
	if out.Usage.OK {
		out.Usage = Usage{OK: true, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	}
	return &out, nil
}

// Stream 无限重复脚本；无完整响应时使用离线固定回复。
func (f *LoopFake) Stream(ctx context.Context, req ChatRequest) (EventStream, error) {
	if ctx.Err() != nil {
		return nil, NewError(ClassifyContext(ctx.Err()))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	script := f.script
	if script.Stream == nil && (script.Response == nil || (script.Response.Message.Content == "" && script.Response.Message.Reasoning == "")) {
		script.Response = &ChatResponse{Message: Message{Role: RoleAssistant, Content: OfflineReply}, FinishReason: FinishStop}
	}
	return newFakeStream(ctx, script)
}

// Calls 返回已收到的请求副本。
func (f *LoopFake) Calls() []ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ChatRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// FakeStep 是独立事件及可选等待；Wait 供测试用通道准确控制时序。
type FakeStep struct {
	Event Event
	Wait  <-chan struct{}
	Delay time.Duration
}

type fakeStream struct {
	ctx      context.Context
	cancel   context.CancelFunc
	steps    []FakeStep
	index    int
	current  Event
	finalErr error
	mu       sync.Mutex
	err      error
	ended    bool
}

func newFakeStream(ctx context.Context, script FakeScript) (EventStream, error) {
	if script.Err != nil {
		return nil, script.Err
	}
	steps := append([]FakeStep(nil), script.Stream...)
	if script.Stream == nil && script.Response != nil {
		r := script.Response
		appendText := func(kind EventKind, text string) {
			runes := []rune(text)
			for len(runes) > 0 {
				n := min(len(runes), 8)
				e := Event{Kind: kind}
				if kind == EventReasoningDelta {
					e.ReasoningDelta = string(runes[:n])
				} else {
					e.TextDelta = string(runes[:n])
				}
				steps = append(steps, FakeStep{Event: e})
				runes = runes[n:]
			}
		}
		appendText(EventReasoningDelta, r.Message.Reasoning)
		appendText(EventTextDelta, r.Message.Content)
		if r.Usage.OK {
			steps = append(steps, FakeStep{Event: Event{Kind: EventUsageUpdate, Usage: r.Usage}})
		}
		finish := r.FinishReason
		if finish == "" {
			finish = FinishStop
		}
		steps = append(steps, FakeStep{Event: Event{Kind: EventModelDone, FinishReason: finish}}, FakeStep{Event: Event{Kind: EventStreamEnded}})
	}
	streamCtx, cancel := context.WithCancel(ctx)
	return &fakeStream{ctx: streamCtx, cancel: cancel, steps: steps, finalErr: script.StreamErr}, nil
}
func (s *fakeStream) Next(ctx context.Context) bool {
	if s.ended {
		return false
	}
	setError := func(err error) bool {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		s.ended = true
		s.cancel()
		return false
	}
	if ctx.Err() != nil {
		return setError(NewError(ClassifyContext(ctx.Err())))
	}
	if s.ctx.Err() != nil {
		return setError(NewError(ClassifyContext(s.ctx.Err())))
	}
	if s.index >= len(s.steps) {
		s.ended = true
		s.mu.Lock()
		s.err = s.finalErr
		s.mu.Unlock()
		s.cancel()
		return false
	}
	step := s.steps[s.index]
	if step.Wait != nil {
		select {
		case <-step.Wait:
		case <-ctx.Done():
			return setError(NewError(ClassifyContext(ctx.Err())))
		case <-s.ctx.Done():
			return setError(NewError(ClassifyContext(s.ctx.Err())))
		}
	}
	if step.Delay > 0 {
		timer := time.NewTimer(step.Delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return setError(NewError(ClassifyContext(ctx.Err())))
		case <-s.ctx.Done():
			return setError(NewError(ClassifyContext(s.ctx.Err())))
		}
	}
	s.current = step.Event
	s.index++
	return true
}
func (s *fakeStream) Event() Event { return s.current }
func (s *fakeStream) Err() error   { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *fakeStream) Close() error { s.cancel(); return nil }
