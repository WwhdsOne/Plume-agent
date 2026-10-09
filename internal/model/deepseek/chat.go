// Package deepseek 是 DeepSeek 的协议适配器：组合 openai 兼容实现
// （openai-chat-completions 协议族，0003 §7 映射表），不复制整套客户端。
// 显式选择已验证的 thinking/reasoning_content 与 prompt_cache_hit_tokens 扩展，传输和 SSE
// 解码继续复用同一 SDK 实现，不按品牌复制客户端。
package deepseek

import (
	"context"

	"plume-agent/internal/model"
	"plume-agent/internal/model/openai"
)

// Adapter 是 DeepSeek 适配器。Provider 标识固定为 deepseek，
// 供 trace 与错误上下文区分品牌。
type Adapter struct {
	inner *openai.Adapter
}

// NewAdapter 构造 DeepSeek 适配器；传输与安全策略由组合的兼容实现提供。
func NewAdapter(baseURL, apiKey string) (*Adapter, error) {
	inner, err := openai.NewDeepSeekAdapter(baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &Adapter{inner: inner}, nil
}

// Generate 转发到兼容实现。
func (a *Adapter) Generate(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	return a.inner.Generate(ctx, req)
}

// Stream 转发 SDK 流与已验证的思考扩展。
func (a *Adapter) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	return a.inner.Stream(ctx, req)
}

// Capabilities 返回扩展协议读取能力，实际控制能力须按端点/模型验证。
func (a *Adapter) Capabilities() model.CapabilitySet { return a.inner.Capabilities() }
