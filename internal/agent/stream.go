package agent

import (
	"context"
	"strings"

	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
)

// RunStream 保持普通流消费者接口；工具执行状态由 RunStreamWithTools 单独传递。
func (r *Runtime) RunStream(ctx context.Context, history []model.Message, input string, calling func(), emit func(model.Event) error) (*RunResult, error) {
	var onCalling func(context.Context)
	var onEvent func(context.Context, model.Event) error
	if calling != nil {
		onCalling = func(context.Context) { calling() }
	}
	if emit != nil {
		onEvent = func(_ context.Context, e model.Event) error { return emit(e) }
	}
	return r.RunStreamWithTools(ctx, history, input, onCalling, onEvent, nil)
}

// RunStreamWithTools 将步骤 context 传给有界事件桥，deadline 也能中断背压等待。
func (r *Runtime) RunStreamWithTools(ctx context.Context, history []model.Message, input string, calling func(context.Context), emit func(context.Context, model.Event) error, toolEmit func(context.Context, ToolEvent) error) (*RunResult, error) {
	return r.runLoop(ctx, history, input, true, calling, emit, toolEmit)
}

// consumeStream 只汇总模型事件；流和模型结束证据都齐备才返回完整消息。
func (r *Runtime) consumeStream(ctx context.Context, req model.ChatRequest, span *telemetry.ModelSpan, emit func(model.Event) error) (resp *model.ChatResponse, err error) {
	resp = &model.ChatResponse{Message: model.Message{Role: model.RoleAssistant}, FinishReason: model.FinishUnknown}
	stream, err := r.client.Stream(ctx, req)
	if err != nil {
		return resp, err
	}
	defer func() {
		closeErr := stream.Close()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && closeErr != nil {
			err = model.NewError(model.ErrTransport).WithSummary("close stream failed")
		}
	}()
	calls := map[int]*model.ToolCall{}
	var text, reasoning strings.Builder
	ended, finished := false, false
	for stream.Next(ctx) {
		e := stream.Event()
		if span != nil {
			span.Observe(e)
		}
		if ended {
			err = protocolError("event after stream end")
			break
		}
		switch e.Kind {
		case model.EventTextDelta:
			if finished {
				err = protocolError("text after model finish")
			} else {
				text.WriteString(e.TextDelta)
			}
		case model.EventReasoningDelta:
			if finished {
				err = protocolError("reasoning after model finish")
			} else {
				reasoning.WriteString(e.ReasoningDelta)
			}
		case model.EventUsageUpdate:
			resp.Usage = e.Usage
		case model.EventModelDone:
			if finished {
				err = protocolError("duplicate model finish")
			} else {
				resp.FinishReason = e.FinishReason
				finished = true
			}
		case model.EventStreamEnded:
			ended = true
		case model.EventToolDelta:
			if finished || e.ToolCall == nil || e.ToolIndex < 0 || e.ToolIndex >= r.limits.ToolCallsPerStep {
				err = protocolError("invalid tool delta index")
				break
			}
			call := calls[e.ToolIndex]
			if call == nil {
				call = &model.ToolCall{}
				calls[e.ToolIndex] = call
			}
			delta := e.ToolCall
			if delta.ID != "" {
				if call.ID != "" && call.ID != delta.ID {
					err = protocolError("conflicting tool call ID")
					break
				}
				call.ID = delta.ID
			}
			call.Name += delta.Name
			call.Arguments += delta.Arguments
			if len(call.Arguments) > r.limits.ArgumentBytes || len(call.Name) > 128 || len(call.ID) > 256 {
				err = protocolError("tool delta exceeds byte limit")
			}
		case model.EventUnsupported:
			err = model.NewError(model.ErrUnsupported)
		case model.EventStreamBroken:
			err = model.NewError(model.ErrStreamInterrupt)
		default:
			err = protocolError("unknown model event")
		}
		if err == nil && emit != nil {
			err = emit(e)
		}
		if err != nil {
			break
		}
	}
	resp.Message.Content, resp.Message.Reasoning = text.String(), reasoning.String()
	for i := 0; i < len(calls); i++ {
		call := calls[i]
		if call == nil {
			if err == nil {
				err = protocolError("non-contiguous tool index")
			}
			break
		}
		resp.Message.ToolCalls = append(resp.Message.ToolCalls, *call)
	}
	if err == nil {
		err = stream.Err()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (!finished || !ended) {
		err = model.NewError(model.ErrStreamInterrupt).WithSummary("missing stream completion")
	}
	return resp, err
}
