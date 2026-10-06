// Package openai 基于 OpenAI 官方 Go SDK（openai-go）实现项目模型接口的
// openai-chat-completions 协议适配器：非流式 Generate（G1b.1）与
// 流式 Stream（G1b.3 启用）。SDK 负责 HTTP 与协议编解码；本包负责
// 请求规范化、响应/错误归一化与供应商请求 ID 提取。
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"herald-agent/internal/model"
	"herald-agent/internal/model/endpoint"
)

// Adapter 是 openai-chat-completions 协议适配器。每个模型配置构造
// 独立实例：Key 在构造时绑定，客户端构造后只读共享（0003 §4）。
type Adapter struct {
	provider string
	client   openai.Client
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

// Generate 执行一次非流式补全并把响应归一化为项目契约。
func (a *Adapter) Generate(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	params, err := encodeRequest(req)
	if err != nil {
		return nil, a.wrap(ctx, err, nil)
	}

	var httpResp *http.Response
	opts := append(reqOptionParams(), option.WithResponseInto(&httpResp))
	completion, err := a.client.Chat.Completions.New(ctx, params, opts...)
	if err != nil {
		return nil, a.wrap(ctx, err, httpResp)
	}

	resp, err := decodeResponse(a.provider, completion, httpResp)
	if err != nil {
		return nil, a.wrap(ctx, err, httpResp)
	}
	return resp, nil
}

// Stream 在 G1b.1 明确返回 unsupported；流式能力在 G1b.3 启用，
// 不用整包响应冒充流（0003 §3）。
func (a *Adapter) Stream(_ context.Context, _ model.ChatRequest) (model.EventStream, error) {
	return nil, model.NewError(model.ErrUnsupported).
		WithProvider(a.provider, model.ProtocolOpenAIChatCompletions).
		WithSummary("streaming is not enabled until G1b.3")
}

// reqOptionParams 返回每次调用固定的请求级选项。G1b.1 无额外参数，
// 保留独立函数便于后续单元注入 trace 中间件。
func reqOptionParams() []option.RequestOption { return nil }

// encodeRequest 把规范化请求编码为 SDK 参数。G1b.1 不发送工具声明
// （工具执行在 G3），出现即明确拒绝，不静默忽略（0003 §3）。
func encodeRequest(req model.ChatRequest) (openai.ChatCompletionNewParams, error) {
	var params openai.ChatCompletionNewParams
	if req.Model == "" {
		return params, model.NewError(model.ErrInvalidConfig).WithSummary("model is required")
	}
	if len(req.Messages) == 0 {
		return params, model.NewError(model.ErrInvalidConfig).WithSummary("messages must not be empty")
	}
	if len(req.Tools) > 0 {
		return params, model.NewError(model.ErrUnsupported).WithSummary("tool declarations are not supported until G3")
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
func decodeResponse(provider string, completion *openai.ChatCompletion, httpResp *http.Response) (*model.ChatResponse, error) {
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
		msg.ToolCalls = make([]model.ToolCall, 0, len(choice.Message.ToolCalls))
		for _, tc := range choice.Message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	resp.Message = msg

	// usage 以可选值表达：SDK 用 JSON 字段存在性记录缺失，缺失即 unknown，
	// 绝不当 0（0003 §3）。
	if completion.Usage.JSON.TotalTokens.Valid() {
		resp.Usage = model.Usage{
			OK:               true,
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
		}
	}
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
	var mErr *model.Error
	if errors.As(err, &mErr) {
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

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
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
