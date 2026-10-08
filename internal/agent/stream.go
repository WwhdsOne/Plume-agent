package agent

import (
	"context"
	"strings"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
)

// RunStream 准备上下文后通知模型调用起点，逐个消费增量；失败也返回已生成片段。
// 思考仅供展示，当前无工具循环时不回传给下一轮模型。
func (r *Runtime) RunStream(ctx context.Context, history []model.Message, input string, calling func(), emit func(model.Event) error) (result *RunResult, err error) {
	result = &RunResult{Message: model.Message{Role: model.RoleAssistant}, FinishReason: model.FinishUnknown}
	if input == "" {
		return result, model.NewError(model.ErrInvalidConfig).WithSummary("empty input")
	}
	messages := make([]model.Message, 0, len(history)+1)
	for _, msg := range history {
		msg.Reasoning = ""
		messages = append(messages, msg)
	}
	messages = append(messages, model.Message{Role: model.RoleUser, Content: input})
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if calling != nil {
		calling()
	}
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	var span *telemetry.ModelSpan
	if r.recorder != nil {
		span = r.recorder.StartModel(telemetry.RunID(ctx)+"/model-1", r.provider, model.ProtocolOpenAIChatCompletions, r.modelID)
		span.Reasoning(r.reasoning)
		defer func() {
			span.End(&model.ChatResponse{Message: result.Message, FinishReason: result.FinishReason, Usage: result.Usage}, err)
		}()
	}
	stream, err := r.client.Stream(ctx, model.ChatRequest{Model: r.modelID, Messages: messages})
	if err != nil {
		return result, err
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
	var text, reasoning strings.Builder
	ended, finished := false, false
	for stream.Next(ctx) {
		e := stream.Event()
		if span != nil {
			span.Observe(e)
		}
		switch e.Kind {
		case model.EventTextDelta:
			text.WriteString(e.TextDelta)
		case model.EventReasoningDelta:
			reasoning.WriteString(e.ReasoningDelta)
		case model.EventUsageUpdate:
			result.Usage = e.Usage
		case model.EventModelDone:
			result.FinishReason = e.FinishReason
			finished = true
		case model.EventStreamEnded:
			ended = true
		case model.EventToolDelta, model.EventUnsupported:
			err = model.NewError(model.ErrUnsupported).WithSummary("tool execution is not implemented")
		case model.EventStreamBroken:
			err = model.NewError(model.ErrStreamInterrupt)
		}
		if err == nil && emit != nil {
			err = emit(e)
		}
		if err != nil {
			break
		}
	}
	result.Message.Content, result.Message.Reasoning = text.String(), reasoning.String()
	if err == nil {
		err = stream.Err()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (!finished || !ended) {
		err = model.NewError(model.ErrStreamInterrupt).WithSummary("missing stream completion")
	}
	if err == nil && strings.TrimSpace(result.Message.Content) == "" {
		err = model.NewError(model.ErrInvalidResponse).WithSummary("stream returned no answer")
	}
	return result, err
}
