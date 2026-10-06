package model

import (
	"context"
	"errors"
	"sync"
)

// FakeScript 是 Fake 的一次脚本化响应：弹出时返回 Response 或 Err。
type FakeScript struct {
	Response *ChatResponse
	Err      error
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

// Stream 在 G1b.1 明确返回 unsupported；流式能力由 G1b.3 的实现开启。
func (f *Fake) Stream(_ context.Context, _ ChatRequest) (EventStream, error) {
	return nil, NewError(ErrUnsupported).WithProvider("fake", ProtocolOpenAIChatCompletions).
		WithSummary("streaming is not enabled until G1b.3")
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
