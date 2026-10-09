// Package agent 实现受控模型—工具循环与请求拼装。
// Runtime 只依赖模型和工具契约，不依赖 TUI/渠道；run 编排
// （ID、串行、取消、事件）属于 internal/app。
package agent

import (
	"context"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
	"plume-agent/internal/tools"
)

// Runtime 是受控工具循环执行器。一个 Runtime 绑定一个模型配置
// （切换模型 = 重新构造，首版不做运行中热切换，0003 §4）。
type Runtime struct {
	client    model.Client
	modelID   string
	recorder  *telemetry.ModelRecorder
	provider  string
	reasoning telemetry.ReasoningInfo
	tools     *tools.Registry
	limits    Limits
}

// New 构造 Runtime。client 可以是真实适配器（工厂 Build 的产物）
// 或脚本化 fake。
func New(client model.Client, modelID string) *Runtime {
	return &Runtime{client: client, modelID: modelID, tools: tools.Builtins(time.Now), limits: DefaultLimits()}
}

// SetTools 在启动时固定允许声明与执行的工具集合；nil 关闭工具。
func (r *Runtime) SetTools(registry *tools.Registry) { r.tools = registry }

// Close 在 app 的运行任务全部退出后释放工作区句柄。
func (r *Runtime) Close() error { return r.tools.Close() }

// ModelID 返回绑定的模型配置 ID。
func (r *Runtime) ModelID() string { return r.modelID }

// SetRecorder 在启动装配时注入脱敏记录器；运行期间不替换。
func (r *Runtime) SetRecorder(rec *telemetry.ModelRecorder, provider string, info telemetry.ReasoningInfo) {
	r.recorder, r.provider, r.reasoning = rec, provider, info
}

// RunResult 是 run 结果；失败返回部分展示，但 Messages 仅在成功时填充。
type RunResult struct {
	Message      model.Message // assistant 消息（文本与/或工具调用）
	FinishReason model.FinishReason
	Usage        model.Usage
	Duration     time.Duration
	Messages     []model.Message // 当前完整成功轮次，供 app 原子提交
	ModelCalls   int
	ToolCalls    int
	LastMessage  model.Message // 最近模型步骤，UI 在工具后开启独立答案区
}

// Run 执行非流式工具循环，与 RunStream 使用同一预算和消息语义。
// 历史由调用方（app.Service）维护与提交；本函数不修改它。
// 空输入与请求编码错误以 *model.Error 返回。
func (r *Runtime) Run(ctx context.Context, history []model.Message, input string) (*RunResult, error) {
	return r.runLoop(ctx, history, input, false, nil, nil, nil)
}
