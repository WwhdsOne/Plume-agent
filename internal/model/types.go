// Package model 定义 plume 的自有模型契约：消息、请求、响应、流事件、
// 能力与统一错误。它不 import 任何 SDK、TUI 或渠道类型；协议适配器
// （internal/model/openai 等）负责把各自协议转换到这里的类型，
// Agent 与 TUI 只消费本包。见 docs/decisions/0003-model-runtime.md。
package model

import "context"

// Protocol 是内部协议族标识（区别于持久化的适配选择器，见 0003 §7）。
const ProtocolOpenAIChatCompletions = "openai-chat-completions"

// Role 是一条对话消息的角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是一次工具调用：assistant 发起的请求，或经 call ID 关联的结果。
// Arguments 保持原始 JSON 文本，交由工具层校验，适配器不解释其内容。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message 是一条对话消息。消息至少支持 system/user/assistant/tool、
// 文本、tool calls 与 tool-call 关联 ID，不以单一字符串抹平工具消息。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // 仅 assistant：模型请求执行的工具调用
	ToolCallID string     `json:"tool_call_id,omitempty"` // 仅 tool：本条结果对应的调用 ID
}

// ToolDeclaration 是发给模型的工具声明。G1b.1 的非流式路径不支持发送
// 工具声明（工具执行在 G3），请求中出现时适配器明确返回 unsupported。
type ToolDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters 是工具参数的 JSON Schema 文本。
	Parameters string `json:"parameters"`
}

// ReasoningEffort 是已验证的供应商思考请求偏好；空值不发送参数。
type ReasoningEffort string

const (
	ReasoningNone   ReasoningEffort = "none"
	ReasoningLow    ReasoningEffort = "low"
	ReasoningMedium ReasoningEffort = "medium"
	ReasoningHigh   ReasoningEffort = "high"
	ReasoningMax    ReasoningEffort = "max"
)

// ChatRequest 是跨协议的规范化生成请求。
type ChatRequest struct {
	// Model 是运行时配置的模型 ID（不是供应商品牌）。
	Model           string          `json:"model"`
	ReasoningEffort ReasoningEffort `json:"reasoning_effort,omitempty"`
	// Messages 是按顺序的完整对话上下文。
	Messages []Message `json:"messages"`
	// Temperature 为 nil 表示不发送该参数（由服务端取默认值）。
	Temperature *float64 `json:"temperature,omitempty"`
	// Tools 是可选的工具声明。G1b.1 非流式路径必须为空。
	Tools []ToolDeclaration `json:"tools,omitempty"`
}

// ChatResponse 是跨协议的规范化响应。它不暴露任何 SDK 类型给调用方。
type ChatResponse struct {
	// ID 是供应商返回的请求/响应标识；取不到时为空串。
	ID string
	// Message 是完整的 assistant 消息（文本与/或工具调用）。
	Message Message
	// FinishReason 是规范化后的完成原因；协议未给出时为 FinishUnknown。
	FinishReason FinishReason
	// Usage 以可选值表达：三个主要计数未完整提供时 Usage.OK 为 false；
	// 缓存命中有独立可选字段，绝不把缺失当 0。
	Usage Usage
	// Provider 是供应商品牌 ID，Protocol 是内部协议族。
	Provider string
	Protocol string
}

// FinishReason 是完成原因。
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	// FinishUnknown 表示协议未给出完成原因，不得当作任何已知值统计。
	FinishUnknown FinishReason = "unknown"
)

// Usage 是一次模型调用的 token 快照，不是需要累加的流增量。
// OK 表示 prompt/completion/total 三项均已知；缓存命中独立表达是否已知。
type Usage struct {
	OK               bool
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	// CachedPromptTokens 是输入 token 中的缓存命中量；nil 未知，指向 0 则已知零。
	// 它是 PromptTokens 的子集，不能再次加入 prompt 或 total。
	CachedPromptTokens *int64
}

// EventKind 是规范化流事件的类别（0003 §5）；思考与答案独立输出。
type EventKind string

const (
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventToolDelta      EventKind = "tool_delta"
	EventUsageUpdate    EventKind = "usage_update"
	EventModelDone      EventKind = "model_done"
	EventUnsupported    EventKind = "unsupported"
	EventStreamEnded    EventKind = "stream_ended"
	EventStreamBroken   EventKind = "stream_broken"
)

// Event 是一条规范化流事件。文本增量、工具调用增量（含稳定 index/ID）、
// usage 更新与模型结束分别使用对应字段；usage 仍以可选值表达。
type Event struct {
	Kind           EventKind
	TextDelta      string
	ReasoningDelta string
	ToolIndex      int
	ToolCall       *ToolCall // EventToolDelta 时非 nil
	Usage          Usage
	FinishReason   FinishReason
}

// EventStream 是拉取式流事件读取器。所有权交给调用者：成功、失败、取消
// 都必须调用 Close；Close 幂等；Next 支持 ctx 取消打断阻塞读取（0003 §3）。
type EventStream interface {
	// Next 推进到下一个事件；返回 false 表示流结束（用 Err 区分成败）。
	Next(ctx context.Context) bool
	// Event 返回当前事件；仅在 Next 返回 true 后有效。
	Event() Event
	// Err 返回流失败原因；正常结束时为 nil。
	Err() error
	// Close 释放底层资源；幂等。
	Close() error
}

// Client 是模型消费接口：fake 与真实适配器实现同一接口（0003 §2）。
// 实现必须尊重 ctx 的取消与超时。
type Client interface {
	// Generate 一次完整生成（非流式）。
	Generate(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// Stream 打开拉取式流生成，协议适配器保持模型结束与流结束的区别。
	Stream(ctx context.Context, req ChatRequest) (EventStream, error)
}
