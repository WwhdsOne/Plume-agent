// Package openai 基于 OpenAI 官方 Go SDK（openai-go）实现项目模型接口的
// openai-chat-completions 协议适配器：非流式 Generate（G1b.1）与
// 流式 Stream（G1b.3 启用）。SDK 负责 HTTP 与协议编解码；本包负责
// 请求规范化、响应/错误归一化与供应商请求 ID 提取。
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"plume-agent/internal/model"
	"plume-agent/internal/model/endpoint"
)

// Adapter 是 openai-chat-completions 协议适配器。每个模型配置构造
// 独立实例：Key 在构造时绑定，客户端构造后只读共享（0003 §4）。
type Adapter struct {
	provider string
	client   openai.Client
	deepseek bool
}

// NewAdapter 构造适配器：校验 Base URL、装配受控传输并禁用 SDK 自动
// 重试与重定向。apiKey 允许为空（本机无鉴权服务）。
func NewAdapter(provider, baseURL, apiKey string) (*Adapter, error) {
	opts, err := endpoint.Options(baseURL, apiKey)
	if err != nil {
		return nil, model.NewError(model.ErrInvalidConfig).
			WithProvider(provider, model.ProtocolOpenAIChatCompletions).
			WithSummary(err.Error())
	}
	return &Adapter{provider: provider, client: openai.NewClient(opts...)}, nil
}

// NewDeepSeekAdapter 显式选择已经验证的 DeepSeek 扩展，不从品牌字符串猜测能力。
func NewDeepSeekAdapter(baseURL, apiKey string) (*Adapter, error) {
	a, err := NewAdapter("deepseek", baseURL, apiKey)
	if err == nil {
		a.deepseek = true
	}
	return a, err
}

// requestOptions 验证控制偏好，并把供应商差异保留在协议适配器中。
func (a *Adapter) requestOptions(req model.ChatRequest) ([]option.RequestOption, error) {
	var replay []option.RequestOption
	if a.deepseek && len(req.Tools) > 0 {
		for i, msg := range req.Messages {
			if msg.Role == model.RoleAssistant {
				replay = append(replay, option.WithJSONSet(fmt.Sprintf("messages.%d.reasoning_content", i), msg.Reasoning))
			}
		}
	}
	if req.ReasoningEffort == "" {
		return replay, nil
	}
	if !a.deepseek {
		return nil, model.NewError(model.ErrUnsupported).WithSummary("reasoning control is unverified for this adapter")
	}
	if req.ReasoningEffort == model.ReasoningNone {
		return append(replay, option.WithJSONSet("thinking.type", "disabled")), nil
	}
	effort := req.ReasoningEffort
	if effort == model.ReasoningMedium {
		effort = model.ReasoningHigh
	}
	switch effort {
	case model.ReasoningLow, model.ReasoningHigh, model.ReasoningMax:
		return append(replay, option.WithJSONSet("thinking.type", "enabled"), option.WithJSONSet("reasoning_effort", string(effort))), nil
	default:
		return nil, model.NewError(model.ErrUnsupported).WithSummary("unsupported reasoning effort")
	}
}

// reasoningField 只读取已验证的独立字段，不从答案文本猜测思考。
func reasoningField(raw string) (string, error) {
	var data struct {
		Reasoning *string `json:"reasoning_content"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", model.NewError(model.ErrInvalidResponse).WithSummary("invalid reasoning_content field")
	}
	if data.Reasoning == nil {
		return "", nil
	}
	return *data.Reasoning, nil
}

// Generate 执行一次非流式补全并把响应归一化为项目契约。
func (a *Adapter) Generate(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	params, err := encodeRequest(req)
	if err != nil {
		return nil, a.wrap(ctx, err, nil)
	}

	opts, err := a.requestOptions(req)
	if err != nil {
		return nil, a.wrap(ctx, err, nil)
	}
	var httpResp *http.Response
	opts = append(opts, option.WithResponseInto(&httpResp))
	completion, err := a.client.Chat.Completions.New(ctx, params, opts...)
	if err != nil {
		return nil, a.wrap(ctx, err, httpResp)
	}

	resp, err := a.decodeResponse(completion, httpResp)
	if err != nil {
		return nil, a.wrap(ctx, err, httpResp)
	}
	if a.deepseek {
		resp.Message.Reasoning, err = reasoningField(completion.Choices[0].Message.RawJSON())
		if err != nil {
			return nil, a.wrap(ctx, err, httpResp)
		}
		if req.ReasoningEffort == model.ReasoningNone && resp.Message.Reasoning != "" {
			return nil, a.wrap(ctx, model.NewError(model.ErrUnsupported).WithSummary("reasoning returned while disabled"), httpResp)
		}
	}
	if resp.Message.Content == "" && len(resp.Message.ToolCalls) == 0 {
		return nil, a.wrap(ctx, model.NewError(model.ErrInvalidResponse).WithSummary("no answer text"), httpResp)
	}
	return resp, nil
}

// encodeRequest 把规范化消息与结构化工具声明编码为 SDK 参数。
func encodeRequest(req model.ChatRequest) (openai.ChatCompletionNewParams, error) {
	var params openai.ChatCompletionNewParams
	if req.Model == "" {
		return params, model.NewError(model.ErrInvalidConfig).WithSummary("model is required")
	}
	if len(req.Messages) == 0 {
		return params, model.NewError(model.ErrInvalidConfig).WithSummary("messages must not be empty")
	}
	seen := make(map[string]bool)
	for _, tool := range req.Tools {
		var schema map[string]any
		if tool.Name == "" || seen[tool.Name] || json.Unmarshal([]byte(tool.Parameters), &schema) != nil || schema == nil || schema["type"] != "object" {
			return params, model.NewError(model.ErrInvalidConfig).WithSummary("invalid tool declaration")
		}
		seen[tool.Name] = true
		params.Tools = append(params.Tools, openai.ChatCompletionToolParam{Function: shared.FunctionDefinitionParam{Name: tool.Name, Description: openai.String(tool.Description), Parameters: shared.FunctionParameters(schema)}})
	}

	params.Model = req.Model
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages))
	for i, m := range req.Messages {
		switch m.Role {
		case model.RoleSystem:
			messages = append(messages, openai.SystemMessage(m.Content))
		case model.RoleUser:
			messages = append(messages, openai.UserMessage(m.Content))
		case model.RoleAssistant:
			messages = append(messages, encodeAssistant(m))
		case model.RoleTool:
			if m.ToolCallID == "" {
				return params, model.NewError(model.ErrInvalidConfig).
					WithSummary(fmt.Sprintf("messages[%d]: tool message requires tool_call_id", i))
			}
			messages = append(messages, openai.ToolMessage(m.Content, m.ToolCallID))
		default:
			return params, model.NewError(model.ErrInvalidConfig).
				WithSummary(fmt.Sprintf("messages[%d]: unknown role %q", i, m.Role))
		}
	}
	params.Messages = messages

	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	return params, nil
}

// encodeAssistant 编码 assistant 消息：文本内容与/或工具调用历史。
func encodeAssistant(m model.Message) openai.ChatCompletionMessageParamUnion {
	param := openai.ChatCompletionAssistantMessageParam{
		Content: openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(m.Content)},
	}
	if len(m.ToolCalls) > 0 {
		param.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			param.ToolCalls = append(param.ToolCalls, openai.ChatCompletionMessageToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &param}
}

// decodeResponse 把 SDK 响应归一化：单候选校验、finish reason、可选 usage
// 与供应商请求 ID。HTTP 2xx 之后的协议错误仍是失败（0003 §4）。
func (a *Adapter) decodeResponse(completion *openai.ChatCompletion, httpResp *http.Response) (*model.ChatResponse, error) {
	provider := a.provider
	usage, err := validateResponse(completion.RawJSON(), false, a.deepseek)
	if err != nil {
		return nil, err
	}
	resp := &model.ChatResponse{
		ID:       completion.ID,
		Provider: provider,
		Protocol: model.ProtocolOpenAIChatCompletions,
	}
	if httpResp != nil {
		resp.ID = firstNonEmpty(httpResp.Header.Get("X-Request-Id"), completion.ID)
	}
	switch n := len(completion.Choices); {
	case n == 0:
		return nil, model.NewError(model.ErrInvalidResponse).
			WithProvider(provider, model.ProtocolOpenAIChatCompletions).
			WithStatus(statusOf(httpResp)).WithRequestID(resp.ID).
			WithSummary("response contains no choices")
	case n > 1:
		return nil, model.NewError(model.ErrInvalidResponse).
			WithProvider(provider, model.ProtocolOpenAIChatCompletions).
			WithStatus(statusOf(httpResp)).WithRequestID(resp.ID).
			WithSummary(fmt.Sprintf("response contains %d choices, want exactly 1", n))
	}

	choice := completion.Choices[0]
	resp.FinishReason = decodeFinishReason(choice.FinishReason)
	msg := model.Message{Role: model.RoleAssistant, Content: choice.Message.Content}
	if len(choice.Message.ToolCalls) > 0 {
		ids := make(map[string]bool, len(choice.Message.ToolCalls))
		msg.ToolCalls = make([]model.ToolCall, 0, len(choice.Message.ToolCalls))
		for _, tc := range choice.Message.ToolCalls {
			if ids[tc.ID] {
				return nil, model.NewError(model.ErrInvalidResponse).WithSummary("duplicate tool call ID")
			}
			ids[tc.ID] = true
			msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	resp.Message = msg

	// usage 只有三个必要计数均存在且合法时才已知，缺失不能补成 0。
	resp.Usage = usage
	return resp, nil
}

// decodeFinishReason 规范化完成原因；未知非空值原样保留，空值记 unknown。
func decodeFinishReason(raw string) model.FinishReason {
	if raw == "" {
		return model.FinishUnknown
	}
	switch model.FinishReason(raw) {
	case model.FinishStop, model.FinishLength, model.FinishToolCalls, model.FinishContentFilter:
		return model.FinishReason(raw)
	default:
		return model.FinishUnknown
	}
}

// wrap 把调用过程中的错误归一化：context 取消/超时优先，其次 API 错误
// 按状态分类（0003 §6），2xx 之后的响应解析失败归 invalid_response
// （协议错误仍是失败，不能只按状态码统计成功），本包/transport 层错误
// 补全 provider 信息，其余归为 transport。
func (a *Adapter) wrap(ctx context.Context, err error, httpResp *http.Response) error {
	if err == nil {
		return nil
	}
	if mErr, ok := errors.AsType[*model.Error](err); ok {
		if mErr.Provider == "" {
			mErr.WithProvider(a.provider, model.ProtocolOpenAIChatCompletions)
		}
		if mErr.StatusCode == 0 {
			mErr.WithStatus(statusOf(httpResp))
		}
		return mErr
	}

	// 取消与超时优先于响应解析问题：ctx 已结束时不再区分 HTTP 阶段。
	if code := model.ClassifyContext(err); code != "" {
		return model.NewError(code).WithProvider(a.provider, model.ProtocolOpenAIChatCompletions).
			WithSummary("request interrupted: " + ctxErrNote(ctx, err))
	}
	if ctx != nil && ctx.Err() != nil {
		code := model.ErrCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = model.ErrTimeout
		}
		return model.NewError(code).WithProvider(a.provider, model.ProtocolOpenAIChatCompletions).
			WithSummary("request interrupted: " + ctxErrNote(ctx, err))
	}

	if apiErr, ok := errors.AsType[*openai.Error](err); ok {
		return &model.Error{
			Code:       classifyStatus(apiErr.StatusCode),
			Provider:   a.provider,
			Protocol:   model.ProtocolOpenAIChatCompletions,
			StatusCode: apiErr.StatusCode,
			RequestID:  requestIDOf(apiErr.Response, httpResp),
			Summary:    model.Truncate(apiErr.Error(), maxAPIErrorSummaryRunes),
		}
	}

	// 响应已到达但 SDK 解析失败（烂 JSON、非 JSON 类型、被禁止的重定向
	// 的 3xx 响应等）：2xx 之后的协议错误仍是失败，按 invalid_response 分类。
	if httpResp != nil {
		return model.NewError(model.ErrInvalidResponse).
			WithProvider(a.provider, model.ProtocolOpenAIChatCompletions).
			WithStatus(statusOf(httpResp)).WithRequestID(requestIDOf(nil, httpResp)).
			WithSummary("unexpected response format: " + model.Truncate(err.Error(), maxAPIErrorSummaryRunes))
	}

	return model.NewError(model.ErrTransport).WithProvider(a.provider, model.ProtocolOpenAIChatCompletions).
		WithSummary(err.Error())
}

// maxAPIErrorSummaryRunes 限制 API 错误摘要长度；正文已在 SDK 侧受限于
// 响应大小，这里防止长错误描述进入 trace。
const maxAPIErrorSummaryRunes = 512

// classifyStatus 按 HTTP 状态映射错误分类（0003 §6）。3xx 在禁止重定向
// 策略下属配置/端点异常，归 invalid_response。
func classifyStatus(status int) model.ErrCode {
	switch {
	case status == 401 || status == 403:
		return model.ErrAuthentication
	case status == 429:
		return model.ErrRateLimited
	case status >= 500:
		return model.ErrUpstream
	case status >= 400:
		return model.ErrInvalidResponse
	default:
		return model.ErrUpstream
	}
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// requestIDOf 从响应头提取供应商请求 ID；API 错误自带的响应优先。
func requestIDOf(primary, fallback *http.Response) string {
	for _, resp := range []*http.Response{primary, fallback} {
		if resp == nil {
			continue
		}
		if id := resp.Header.Get("X-Request-Id"); id != "" {
			return id
		}
	}
	return ""
}

func ctxErrNote(ctx context.Context, err error) string {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err().Error()
	}
	return err.Error()
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Capabilities 表示适配器已实现的协议读取能力；控制参数的端点/模型
// 验证仍由 provider 完成，不能从品牌或任意 Base URL 推断支持。
func (a *Adapter) Capabilities() model.CapabilitySet {
	caps := model.NewCapabilitySet().Set(model.CapText, model.CapSupported).Set(model.CapStream, model.CapSupported).Set(model.CapUsage, model.CapSupported).Set(model.CapTools, model.CapSupported)
	if a.deepseek {
		caps = caps.Set(model.CapReasoningOutput, model.CapSupported)
	}
	return caps
}
