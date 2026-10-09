package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
	"plume-agent/internal/tools"
)

// ToolEvent 的名称与摘要只由许可注册表和固定结果类型产生，不复制模型原始参数。
type ToolEvent struct {
	CallID, Name, Status, Summary string
	Duration                      time.Duration
}

func protocolError(summary string) error {
	return model.NewError(model.ErrInvalidResponse).WithSummary(summary)
}
func budgetError(summary string) error {
	return model.NewError(model.ErrBudgetExceeded).WithSummary(summary)
}

func (r *Runtime) runLoop(ctx context.Context, history []model.Message, input string, streaming bool, calling func(context.Context), emit func(context.Context, model.Event) error, toolEmit func(context.Context, ToolEvent) error) (result *RunResult, err error) {
	start := time.Now()
	result = &RunResult{Message: model.Message{Role: model.RoleAssistant}, FinishReason: model.FinishUnknown}
	defer func() { result.Duration = time.Since(start) }()
	if strings.TrimSpace(input) == "" {
		return result, model.NewError(model.ErrInvalidConfig).WithSummary("empty input")
	}
	ctx, cancel := runContext(ctx, r.limits.RunTimeout)
	defer cancel()
	builder := newPromptBuilder(r.modelID, r.tools, r.limits.RequestBytes)
	turn := []model.Message{{Role: model.RoleUser, Content: input}}
	ids := map[string]bool{}
	for _, msg := range history {
		for _, call := range msg.ToolCalls {
			ids[call.ID] = true
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if r.limits.ModelCalls != 0 && result.ModelCalls >= r.limits.ModelCalls {
			return result, budgetError("model call budget exhausted")
		}
		req, hash, size, buildErr := builder.Build(history, turn)
		if buildErr != nil {
			return result, buildErr
		}
		if r.recorder != nil {
			r.recorder.Prompt(telemetry.RunID(ctx), PromptVersion, hash, size, len(history), len(turn), req.Tools)
		}
		result.ModelCalls++
		result.Usage = model.Usage{}
		result.LastMessage = model.Message{Role: model.RoleAssistant}
		if calling != nil {
			calling(ctx)
		}
		callCtx, callCancel := context.WithTimeout(ctx, r.limits.ModelTimeout)
		var span *telemetry.ModelSpan
		if r.recorder != nil {
			span = r.recorder.StartModel(fmt.Sprintf("%s/model-%d", telemetry.RunID(ctx), result.ModelCalls), r.provider, model.ProtocolOpenAIChatCompletions, r.modelID)
			span.Reasoning(r.reasoning)
		}
		var response *model.ChatResponse
		if streaming {
			var callEmit func(model.Event) error
			if emit != nil {
				callEmit = func(e model.Event) error { return emit(callCtx, e) }
			}
			response, err = r.consumeStream(callCtx, req, span, callEmit)
		} else {
			response, err = r.client.Generate(callCtx, req)
			if err == nil {
				err = callCtx.Err()
			}
		}
		callCancel()
		if response != nil {
			result.LastMessage = response.Message
			result.Usage = response.Usage
			result.FinishReason = response.FinishReason
			result.Message.Content += response.Message.Content
			result.Message.Reasoning += response.Message.Reasoning
		}
		if err == nil && response == nil {
			err = protocolError("nil model response")
		}
		if err == nil {
			err = r.validateFinish(response, ids)
		}
		if span != nil {
			span.End(response, err)
		}
		if err != nil {
			return result, err
		}
		response.Message.Role = model.RoleAssistant
		turn = append(turn, cloneMessages([]model.Message{response.Message})...)
		if response.FinishReason == model.FinishStop {
			result.Messages = cloneMessages(turn)
			return result, nil
		}
		calls := response.Message.ToolCalls
		// 整批预检，避免执行工具后发现无法继续生成最终答案。
		if (r.limits.ToolCalls != 0 && len(calls) > r.limits.ToolCalls-result.ToolCalls) || (r.limits.ModelCalls != 0 && result.ModelCalls >= r.limits.ModelCalls) {
			return result, budgetError("tool continuation budget exhausted")
		}
		for _, call := range calls {
			ids[call.ID] = true
		}
		for _, call := range calls {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			name := call.Name
			if !r.tools.Known(name) {
				name = "unknown"
			}
			event := ToolEvent{CallID: call.ID, Name: name, Status: "running"}
			if toolEmit != nil {
				err = toolEmit(ctx, event)
			}
			if err != nil {
				return result, err
			}
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.ToolCalls++
			toolCtx, toolCancel := context.WithTimeout(ctx, r.limits.ToolTimeout)
			started := time.Now()
			if r.recorder != nil {
				r.recorder.Tool(telemetry.RunID(ctx), event.CallID, name, "started", "", 0, fmt.Sprintf("%s/model-%d", telemetry.RunID(ctx), result.ModelCalls))
			}
			toolResult := r.tools.Execute(toolCtx, call.Name, call.Arguments)
			toolErr := toolCtx.Err()
			toolCancel()
			event.Duration = time.Since(started)
			event.Status = "completed"
			if !toolResult.OK {
				event.Status = "failed"
			}
			if toolErr != nil {
				event.Status = "failed"
				toolResult.OK = false
				toolResult.Code = string(model.ClassifyContext(toolErr))
			}
			encoded := toolResult.JSON()
			if len(encoded) > r.limits.ResultBytes {
				toolResult = tools.Result{Code: "result_truncated", Truncated: true}
				encoded = toolResult.JSON()
			}
			event.Summary = toolResult.Code
			if toolResult.Summary != "" {
				event.Summary = toolResult.Summary
			} else if toolResult.OK {
				event.Summary = fmt.Sprint(toolResult.Value)
			}
			if toolResult.Truncated && !toolResult.OK {
				event.Status = "failed"
			}
			if r.recorder != nil {
				r.recorder.Tool(telemetry.RunID(ctx), event.CallID, name, event.Status, toolResult.Code, event.Duration, fmt.Sprintf("%s/model-%d", telemetry.RunID(ctx), result.ModelCalls))
			}
			if toolEmit != nil {
				err = toolEmit(ctx, event)
			}
			if err != nil {
				return result, err
			}
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			turn = append(turn, model.Message{Role: model.RoleTool, ToolCallID: call.ID, Content: encoded})
		}
		if response.Message.Content != "" {
			result.Message.Content += "\n\n"
			if emit != nil {
				if err = emit(ctx, model.Event{Kind: model.EventTextDelta, TextDelta: "\n\n"}); err != nil {
					return result, err
				}
			}
		}
	}
}

func (r *Runtime) validateFinish(resp *model.ChatResponse, ids map[string]bool) error {
	switch resp.FinishReason {
	case model.FinishStop:
		if len(resp.Message.ToolCalls) != 0 || strings.TrimSpace(resp.Message.Content) == "" {
			return protocolError("invalid final answer")
		}
	case model.FinishToolCalls:
		if len(resp.Message.ToolCalls) == 0 {
			return protocolError("tool finish without tool calls")
		}
		if len(resp.Message.ToolCalls) > r.limits.ToolCallsPerStep {
			return protocolError("tool batch exceeds step limit")
		}
		seen := map[string]bool{}
		for _, call := range resp.Message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || call.Arguments == "" || ids[call.ID] || seen[call.ID] || len(call.ID) > 256 || len(call.Name) > 128 {
				return protocolError("incomplete or duplicate tool call")
			}
			if len(call.Arguments) > r.limits.ArgumentBytes {
				return protocolError("tool arguments exceed byte limit")
			}
			seen[call.ID] = true
		}
	default:
		return protocolError("model did not finish with stop or tool_calls")
	}
	return nil
}

// runContext 在关闭整体期限时仍提供主动取消，不能创建零时长 deadline。
func runContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
