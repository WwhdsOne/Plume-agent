package provider

import (
	"context"

	"plume-agent/internal/model"
)

// Probe 向配置端点发送一次最小请求，验证连通性、鉴权与响应解析。
//
// 它只在用户显式触发时调用（0003 §4：可选联网 probe）——配置保存、
// TUI 启动与离线测试绝不调用本函数，因为真实请求可能产生费用。
// 离线测试通过注入指向 httptest 服务器的 ModelSpec 来驱动 Probe 自身。
func (f *ModelFactory) Probe(ctx context.Context, spec ModelSpec) (*model.ChatResponse, error) {
	client, err := f.Build(spec)
	if err != nil {
		return nil, err
	}
	return client.Generate(ctx, model.ChatRequest{
		Model: spec.Model,
		Messages: []model.Message{
			{Role: model.RoleUser, Content: "Reply with the single word: pong"},
		},
	})
}
