package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/telemetry"
)

// EventKind 是 app 推送给 UI 的事件类别。一次 run 恰好一个终态事件。
type EventKind string

const (
	EventRunStarted     EventKind = "run_started"
	EventRunCompleted   EventKind = "run_completed"
	EventRunFailed      EventKind = "run_failed"
	EventRunPhase       EventKind = "run_phase"
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventUsageUpdate    EventKind = "usage_update"
)

type Phase string

const (
	PhasePreparing  Phase = "preparing"
	PhaseWaiting    Phase = "waiting"
	PhaseThinking   Phase = "thinking"
	PhaseResponding Phase = "responding"
)

// Event 是一次 run 的生命周期事件。Reply 只在 completed 时有值
// （G1b.2 非流式整段回答；流式增量由 G1b.3 的事件扩展承载）。
type Event struct {
	RunID          string
	Kind           EventKind
	Reply          string
	FinishReason   model.FinishReason
	Usage          model.Usage
	Err            error
	Duration       time.Duration
	Phase          Phase
	TextDelta      string
	ReasoningDelta string
	Reasoning      string
	Preparation    time.Duration
	FirstReasoning time.Duration
	FirstAnswer    time.Duration
	AcceptedAt     time.Time
	Stats          SessionStats
}

// ErrBusy 表示当前已有 run 在执行。首版同会话串行：再次发送被明确拒绝，
// 不创建并发模型请求或隐式队列（阶段计划 §2）。
var ErrBusy = errors.New("a run is already in progress")

// eventBuffer 是事件 channel 容量。UI 消费循环持续读取时足够吸收突发；
const eventBuffer = 8

// Service 把 agent 轮次包装成异步 run：Submit 立即返回 run ID 并接受输入，
// 事件经有界 channel 推送给 UI；Cancel 打断当前 run。失败/取消的轮次
// 不进入会话历史（0003/阶段计划 §2：只把完整提交的轮次加入历史）。
type Service struct {
	runtime *agent.Runtime
	session *Session
	events  chan Event

	mu      sync.Mutex
	busy    bool
	cancel  context.CancelFunc
	runSeq  int
	pending string // 提交时暂存的用户输入，成功后随回复一起入历史

	wg        sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once
	closed    bool
	recorder  *telemetry.ModelRecorder
}

// NewService 构造服务。
func NewService(runtime *agent.Runtime, recorders ...*telemetry.ModelRecorder) *Service {
	s := &Service{
		runtime: runtime,
		session: NewSession(),
		events:  make(chan Event, eventBuffer+1),
		done:    make(chan struct{}),
	}
	if len(recorders) > 0 {
		s.recorder = recorders[0]
	}
	return s
}

// Events 返回只读事件流。
func (s *Service) Events() <-chan Event { return s.events }

// Session 暴露会话（历史查询、重置）。
func (s *Service) Session() *Session { return s.session }

// Busy 报告是否有 run 在执行。
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Submit 提交一条用户输入，立即返回 run ID。串行约束下 busy 时返回
// ErrBusy 且不产生请求。用户输入此刻不进历史：成功轮次在完成时与回复
// 一起原子追加，失败/取消不留半截上下文。
func (s *Service) Submit(ctx context.Context, input string) (string, error) {
	if input == "" {
		return "", model.NewError(model.ErrInvalidConfig).WithSummary("empty input")
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", errors.New("service is closed")
	}
	if s.busy || len(s.events) == cap(s.events) {
		s.mu.Unlock()
		return "", ErrBusy
	}
	s.runSeq++
	runID := fmt.Sprintf("run-%06d", s.runSeq)
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = telemetry.WithRunID(runCtx, runID)
	s.busy = true
	s.cancel = cancel
	s.pending = input
	accepted := time.Now()
	if s.recorder != nil {
		s.recorder.RunStart(runID, accepted)
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		s.emit(Event{Kind: EventRunStarted, RunID: runID, Phase: PhasePreparing, AcceptedAt: accepted}, runCtx)
		phase := PhasePreparing
		var callingAt time.Time
		var preparation, firstReasoning, firstAnswer time.Duration
		result, err := s.runtime.RunStream(runCtx, s.session.History(), input, func() {
			callingAt = time.Now()
			preparation = callingAt.Sub(accepted)
			phase = PhaseWaiting
			s.session.ObserveUsage(runID+"/model-1", model.Usage{})
			s.emit(Event{Kind: EventRunPhase, RunID: runID, Phase: phase, Stats: s.session.Statistics()}, runCtx)
		}, func(e model.Event) error {
			next := phase
			ev := Event{RunID: runID}
			switch e.Kind {
			case model.EventUsageUpdate:
				s.session.ObserveUsage(runID+"/model-1", e.Usage)
				returnStatus := s.emit(Event{Kind: EventUsageUpdate, RunID: runID, Usage: e.Usage, Stats: s.session.Statistics()}, runCtx)
				if !returnStatus {
					return runCtx.Err()
				}
				return nil
			case model.EventReasoningDelta:
				if e.ReasoningDelta == "" {
					return nil
				}
				if firstReasoning == 0 {
					firstReasoning = time.Since(callingAt)
				}
				if phase != PhaseResponding {
					next = PhaseThinking
				}
				ev.Kind, ev.ReasoningDelta = EventReasoningDelta, e.ReasoningDelta
			case model.EventTextDelta:
				if e.TextDelta == "" {
					return nil
				}
				if firstAnswer == 0 {
					firstAnswer = time.Since(callingAt)
				}
				next = PhaseResponding
				ev.Kind, ev.TextDelta = EventTextDelta, e.TextDelta
			default:
				return nil
			}
			if next != phase {
				phase = next
				if !s.emit(Event{Kind: EventRunPhase, RunID: runID, Phase: phase}, runCtx) {
					return runCtx.Err()
				}
			}
			if !s.emit(ev, runCtx) {
				return runCtx.Err()
			}
			return nil
		})
		total := time.Since(accepted)
		if callingAt.IsZero() {
			preparation = total
		}
		s.finish(runCtx, runID, result, err, total, preparation, firstReasoning, firstAnswer)
	}()
	return runID, nil
}

// finish 写入唯一终态事件；成功时把 user/assistant 原子提交进历史。
func (s *Service) finish(ctx context.Context, runID string, result *agent.RunResult, err error, total, preparation, firstReasoning, firstAnswer time.Duration) {
	s.mu.Lock()
	// 取消与历史提交以同一把锁确定先后，覆盖模型收完后关闭流的时间窗口。
	if err == nil {
		err = ctx.Err()
	}
	input := s.pending
	ev := Event{
		Kind:  EventRunCompleted,
		RunID: runID,
		Err:   err, Duration: total, Preparation: preparation, FirstReasoning: firstReasoning, FirstAnswer: firstAnswer,
	}
	if result != nil {
		ev.Reply = result.Message.Content
		ev.Reasoning = result.Message.Reasoning
		ev.FinishReason = result.FinishReason
		ev.Usage = result.Usage
		// finish 与 usage_update 是同一个模型调用，按 ID 替换而不累加。
		s.session.observeUsage(runID+"/model-1", result.Usage, true)
	}
	ev.Stats = s.session.Statistics()
	if s.recorder != nil {
		s.recorder.RunEnd(runID, telemetry.RunMetrics{Duration: total, Preparation: preparation, FirstReasoning: firstReasoning, FirstAnswer: firstAnswer, ReasoningBytes: len(ev.Reasoning), AnswerBytes: len(ev.Reply)}, err)
	}
	if err != nil {
		ev.Kind = EventRunFailed
	} else {
		s.session.Append(model.Message{Role: model.RoleUser, Content: input}, result.Message)
	}
	// 非终态最多占八格，最后一格保留给当前终态；取消不依赖消费者继续读取。
	s.emit(ev, context.Background())
	s.busy = false
	s.cancel = nil
	s.pending = ""
	s.mu.Unlock()
}

// Cancel 打断当前 run。没有 run 在执行时是无害空操作。
// 用户重试是新的 run；已取消的轮次不写入历史。
func (s *Service) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// emit 发送事件；channel 满时丢弃（仅发生在 UI 已停止消费的退出路径）。
func (s *Service) emit(event Event, ctx context.Context) bool {
	if s.recorder != nil && (event.Kind == EventRunStarted || event.Kind == EventRunPhase) {
		s.recorder.RunPhase(event.RunID, string(event.Phase))
	}
	if event.Kind != EventRunCompleted && event.Kind != EventRunFailed {
		for len(s.events) >= eventBuffer {
			timer := time.NewTimer(time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-s.done:
				timer.Stop()
				return false
			case <-timer.C:
			}
		}
	}
	select {
	case s.events <- event:
		return true
	case <-ctx.Done():
		return false
	case <-s.done:
		return false
	}
}

// Close 取消进行中的 run 并等待其收尾，保证退出时不残留 goroutine。
// 事件在 UI 停止消费时可能被丢弃（见 eventBuffer）。
func (s *Service) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
}

// Idle 供测试等待全部 run 收尾。
func (s *Service) waitIdle() { s.wg.Wait() }
