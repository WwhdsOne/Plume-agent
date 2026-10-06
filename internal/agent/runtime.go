// Package agent 实现模型—工具循环。G1b.2 只做最小单轮：准备上下文 →
// 调用模型 → 返回 assistant 消息；工具调度、预算与多轮循环在 G3 加入。
// Runtime 只依赖 internal/model 契约，不依赖 TUI/渠道；run 编排
// （ID、串行、取消、事件）属于 internal/app。
package agent

import (
	"context"
	"time"

	"herald-agent/internal/model"
)

// Runtime 是最小模型轮次执行器。一个 Runtime 绑定一个模型配置
// （切换模型 = 重新构造，首版不做运行中热切换，0003 §4）。
type Runtime struct {
	client  model.Client
	modelID string
}

// New 构造 Runtime。client 可以是真实适配器（工厂 Build 的产物）
// 或脚本化 fake。
func New(client model.Client, modelID string) *Runtime {
	return &Runtime{client: client, modelID: modelID}
}

// ModelID 返回绑定的模型配置 ID。
func (r *Runtime) ModelID() string { return r.modelID }

// RunResult 是一次成功模型轮次的结果。
type RunResult struct {
	Message      model.Message // assistant 消息（文本与/或工具调用）
	FinishReason model.FinishReason
	Usage        model.Usage
	Duration     time.Duration
}

// Run 执行一次模型轮次：把会话历史与新输入合并为请求上下文。
// 历史由调用方（app.Service）维护与提交；本函数不修改它。
// 空输入与请求编码错误以 *model.Error 返回。
func (r *Runtime) Run(ctx context.Context, history []model.Message, input string) (*RunResult, error) {
	if input == "" {
		return nil, model.NewError(model.ErrInvalidConfig).WithSummary("empty input")
	}

	messages := make([]model.Message, 0, len(history)+1)
	messages = append(messages, history...)
	messages = append(messages, model.Message{Role: model.RoleUser, Content: input})

	start := time.Now()
	resp, err := r.client.Generate(ctx, model.ChatRequest{
		Model:    r.modelID,
		Messages: messages,
	})
	if err != nil {
		return nil, err
	}
	return &RunResult{
		Message:      resp.Message,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Duration:     time.Since(start),
	}, nil
}
