package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"
	"plume-agent/internal/model"
	"plume-agent/internal/model/endpoint"
)

// Stream 通过 SDK 发送请求，并使用 SDK 的 SSE 解码器读取协议事件。
// 观察解码后的结束标记，避免 SDK 隐藏 DONE 后将意外 EOF 误判为成功。
func (a *Adapter) Stream(ctx context.Context, req model.ChatRequest) (model.EventStream, error) {
	params, err := encodeRequest(req)
	if err != nil {
		return nil, a.wrap(ctx, err, nil)
	}
	opts, err := a.requestOptions(req)
	if err != nil {
		return nil, a.wrap(ctx, err, nil)
	}
	callCtx, cancel := context.WithCancel(ctx)
	var resp *http.Response
	opts = append(opts, []option.RequestOption{option.WithJSONSet("stream", true), option.WithJSONSet("stream_options.include_usage", true), option.WithResponseInto(&resp)}...)
	if err = a.client.Post(callCtx, "chat/completions", params, &resp, opts...); err != nil {
		cancel()
		return nil, a.wrap(ctx, err, resp)
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		cancel()
		_ = resp.Body.Close()
		return nil, a.wrap(ctx, model.NewError(model.ErrInvalidResponse).WithSummary("expected text/event-stream"), resp)
	}
	resp.Body = &limitedStreamBody{ReadCloser: resp.Body, remaining: endpoint.MaxStreamBytes}
	decoder := &observedDecoder{Decoder: ssestream.NewDecoder(resp)}
	return &eventStream{adapter: a, response: resp, ctx: callCtx, cancel: cancel, decoder: decoder, sdk: ssestream.NewStream[openai.ChatCompletionChunk](decoder, nil), effort: req.ReasoningEffort, tools: map[int64]string{}, toolIDs: map[string]int64{}}, nil
}

type limitedStreamBody struct {
	io.ReadCloser
	remaining int64
}

// Read 在限额内透传；耗尽后仍尝试多读一字节，用于区分"恰好用尽"（返回
// 底层 EOF）与"确实超限"（返回 ErrResponseTooLarge）。
func (b *limitedStreamBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, model.NewError(model.ErrResponseTooLarge).WithSummary("stream exceeds 16 MiB limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// observedDecoder 不切分网络字节；只观察 SDK 已分帧的事件。
type observedDecoder struct {
	ssestream.Decoder
	done bool
	err  error
}

// Next 只放行 message 帧：error 帧与未知类型转为模型错误；观察到
// DONE 置 done，伪 DONE 前缀直接拒绝（见上方说明）。
func (d *observedDecoder) Next() bool {
	if d.done || d.err != nil {
		return false
	}
	for d.Decoder.Next() {
		switch d.Decoder.Event().Type {
		case "", "message":
		case "error":
			d.err = model.NewError(model.ErrUpstream).WithSummary("upstream stream error event")
			return false
		default:
			d.err = model.NewError(model.ErrUnsupported).WithSummary("unsupported stream event type")
			return false
		}
		data := bytes.TrimSpace(d.Decoder.Event().Data)
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			d.done = true
		} else if bytes.HasPrefix(data, []byte("[DONE]")) {
			// SDK 以此前缀结束解析；先拒绝伪标记，避免静默吞掉后续帧。
			d.err = model.NewError(model.ErrInvalidResponse).WithSummary("invalid stream end marker")
			return false
		}
		return true
	}
	return false
}

func (d *observedDecoder) Err() error {
	if d.err != nil {
		return d.err
	}
	return d.Decoder.Err()
}

type eventStream struct {
	effort    model.ReasoningEffort
	adapter   *Adapter
	response  *http.Response
	ctx       context.Context
	cancel    context.CancelFunc
	decoder   *observedDecoder
	sdk       *ssestream.Stream[openai.ChatCompletionChunk]
	queue     []model.Event
	current   model.Event
	tools     map[int64]string
	toolIDs   map[string]int64
	finished  bool
	hasAnswer bool
	hasTool   bool
	ended     bool
	mu        sync.Mutex
	err       error
	closeOnce sync.Once
	closeErr  error
}

// Next 由一个消费方调用；Close 可并发打断阻塞读取。
func (s *eventStream) Next(ctx context.Context) bool {
	if s.ended {
		return false
	}
	if ctx.Err() != nil {
		s.fail(s.adapter.wrap(ctx, ctx.Err(), s.response))
		return false
	}
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	for len(s.queue) == 0 {
		if !s.sdk.Next() {
			if err := s.sdk.Err(); err != nil {
				s.fail(s.streamError(ctx, err))
				return false
			}
			if s.ctx.Err() != nil {
				s.fail(s.adapter.wrap(s.ctx, s.ctx.Err(), s.response))
				return false
			}
			if !s.finished || !s.decoder.done {
				s.fail(s.adapter.wrap(ctx, model.NewError(model.ErrStreamInterrupt).WithSummary("stream ended without finish and DONE evidence"), s.response))
				return false
			}
			if !s.hasAnswer && !s.hasTool {
				s.fail(s.adapter.wrap(ctx, model.NewError(model.ErrInvalidResponse).WithSummary("no answer text"), s.response))
				return false
			}
			s.queue = append(s.queue, model.Event{Kind: model.EventStreamEnded})
			s.ended = true
			_ = s.Close()
			break
		}
		if err := s.normalize(s.sdk.Current()); err != nil {
			s.fail(s.adapter.wrap(ctx, err, s.response))
			return false
		}
	}
	s.current = s.queue[0]
	s.queue = s.queue[1:]
	return true
}

func (s *eventStream) normalize(chunk openai.ChatCompletionChunk) error {
	usage, err := validateResponse(chunk.RawJSON(), true, s.adapter.deepseek)
	if err != nil {
		return err
	}
	if len(chunk.Choices) > 1 {
		return model.NewError(model.ErrInvalidResponse).WithSummary("stream contains multiple choices")
	}
	if len(chunk.Choices) == 1 {
		c := chunk.Choices[0]
		if c.Index != 0 {
			return model.NewError(model.ErrInvalidResponse).WithSummary("stream choice index must be zero")
		}
		if s.finished {
			return model.NewError(model.ErrInvalidResponse).WithSummary("choice received after model finish")
		}
		if s.adapter.deepseek {
			reasoning, err := reasoningField(c.Delta.RawJSON())
			if err != nil {
				return err
			}
			if reasoning != "" {
				if s.effort == model.ReasoningNone {
					return model.NewError(model.ErrUnsupported).WithSummary("reasoning returned while disabled")
				}
				s.queue = append(s.queue, model.Event{Kind: model.EventReasoningDelta, ReasoningDelta: reasoning})
			}
		}
		if c.Delta.Content != "" {
			s.hasAnswer = true
			s.queue = append(s.queue, model.Event{Kind: model.EventTextDelta, TextDelta: c.Delta.Content})
		}
		for _, tc := range c.Delta.ToolCalls {
			id := s.tools[tc.Index]
			if tc.Index < 0 || (id != "" && tc.ID != "" && id != tc.ID) {
				return model.NewError(model.ErrInvalidResponse).WithSummary("unstable tool call index or ID")
			}
			if tc.ID != "" {
				if index, exists := s.toolIDs[tc.ID]; exists && index != tc.Index {
					return model.NewError(model.ErrInvalidResponse).WithSummary("tool call ID reused at a different index")
				}
				id = tc.ID
				s.tools[tc.Index] = id
				s.toolIDs[id] = tc.Index
			}
			if id == "" {
				return model.NewError(model.ErrInvalidResponse).WithSummary("tool delta has no stable ID")
			}
			s.hasTool = true
			s.queue = append(s.queue, model.Event{Kind: model.EventToolDelta, ToolIndex: int(tc.Index), ToolCall: &model.ToolCall{ID: id, Name: tc.Function.Name, Arguments: tc.Function.Arguments}})
		}
		if c.FinishReason != "" {
			s.finished = true
			s.queue = append(s.queue, model.Event{Kind: model.EventModelDone, FinishReason: decodeFinishReason(c.FinishReason)})
		}
	}
	if usage.OK || usage.CachedPromptTokens != nil {
		s.queue = append(s.queue, model.Event{Kind: model.EventUsageUpdate, Usage: usage})
	}
	return nil
}
func (s *eventStream) streamError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return s.adapter.wrap(ctx, ctx.Err(), s.response)
	}
	if s.ctx.Err() != nil {
		return s.adapter.wrap(s.ctx, s.ctx.Err(), s.response)
	}
	if e, ok := errors.AsType[*model.Error](err); ok {
		return s.adapter.wrap(ctx, e, s.response)
	}
	// SDK 的错误事件可能含响应正文，摘要只保留分类，不复制原文。
	var syntax *json.SyntaxError
	var valueType *json.UnmarshalTypeError
	code := model.ErrStreamInterrupt
	if errors.As(err, &syntax) || errors.As(err, &valueType) {
		code = model.ErrInvalidResponse
	}
	if strings.HasPrefix(err.Error(), "received error while streaming:") {
		code = model.ErrUpstream
	}
	return s.adapter.wrap(ctx, model.NewError(code).WithSummary("invalid stream frame or read failure"), s.response)
}
func (s *eventStream) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.ended = true
	_ = s.Close()
}
func (s *eventStream) Event() model.Event { return s.current }

// Err 返回终态错误；与 Close/fail 并发安全。
func (s *eventStream) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

// Close 幂等：取消 HTTP 请求并关闭 SDK 流，可从任意 goroutine 调用。
func (s *eventStream) Close() error {
	s.closeOnce.Do(func() { s.cancel(); s.closeErr = s.sdk.Close() })
	return s.closeErr
}
