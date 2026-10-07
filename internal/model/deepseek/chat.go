// Package deepseek 是 DeepSeek 的协议适配器：组合 openai 兼容实现
// （openai-chat-completions 协议族，0003 §7 映射表），不复制整套客户端。
// G1b.1 阶段尚无经过验证的供应商差异，保持组合转发；后续单元在此
// 承载已验证差异（例如响应扩展字段与流式行为），不按品牌复制客户端。
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
	inner, err := openai.NewAdapter("deepseek", baseURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &Adapter{inner: inner}, nil
}

// Generate 转发到兼容实现。
func (a *Adapter) Generate(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	return a.inner.Generate(ctx, req)
}

// Stream 在 G1b.1 明确返回 unsupported。
func (a *Adapter) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	return a.inner.Stream(ctx, req)
}
