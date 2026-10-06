package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"herald-agent/internal/agent"
	"herald-agent/internal/model"
)

// EventKind 是 app 推送给 UI 的事件类别。一次 run 恰好一个终态事件。
type EventKind string

const (
	EventRunStarted   EventKind = "run_started"
	EventRunCompleted EventKind = "run_completed"
	EventRunFailed    EventKind = "run_failed"
)

// Event 是一次 run 的生命周期事件。Reply 只在 completed 时有值
// （G1b.2 非流式整段回答；流式增量由 G1b.3 的事件扩展承载）。
type Event struct {
	RunID        string
	Kind         EventKind
	Reply        string
	FinishReason model.FinishReason
	Usage        model.Usage
	Err          error
	Duration     time.Duration
}

// ErrBusy 表示当前已有 run 在执行。首版同会话串行：再次发送被明确拒绝，
// 不创建并发模型请求或隐式队列（阶段计划 §2）。
var ErrBusy = errors.New("a run is already in progress")

// eventBuffer 是事件 channel 容量。UI 消费循环持续读取时足够吸收突发；
// 满时新事件被丢弃——只有 UI 已停止消费（退出中）才会发生，此时丢事件
// 无害且好过让 run 收尾永久阻塞。
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

	wg sync.WaitGroup
}

// NewService 构造服务。
func NewService(runtime *agent.Runtime) *Service {
	return &Service{
		runtime: runtime,
		session: NewSession(),
		events:  make(chan Event, eventBuffer),
	}
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
	if s.busy {
		s.mu.Unlock()
		return "", ErrBusy
	}
	s.runSeq++
	runID := fmt.Sprintf("run-%06d", s.runSeq)
	runCtx, cancel := context.WithCancel(ctx)
	s.busy = true
	s.cancel = cancel
	s.pending = input
	s.mu.Unlock()

	s.wg.Go(func() {
		defer cancel()
		s.emit(Event{Kind: EventRunStarted, RunID: runID})
		result, err := s.runtime.Run(runCtx, s.session.History(), input)
		s.finish(runID, result, err)
	})
	return runID, nil
}

// finish 写入唯一终态事件；成功时把 user/assistant 原子提交进历史。
func (s *Service) finish(runID string, result *agent.RunResult, err error) {
	s.mu.Lock()
	s.busy = false
	s.cancel = nil
	input := s.pending
	s.pending = ""
	s.mu.Unlock()

	if err != nil {
		s.emit(Event{Kind: EventRunFailed, RunID: runID, Err: err})
		return
	}
	s.session.Append(
		model.Message{Role: model.RoleUser, Content: input},
		result.Message,
	)
	s.emit(Event{
		Kind:         EventRunCompleted,
		RunID:        runID,
		Reply:        result.Message.Content,
		FinishReason: result.FinishReason,
		Usage:        result.Usage,
		Duration:     result.Duration,
	})
}

// Cancel 打断当前 run。没有 run 在执行时是无害空操作。
// 用户重试是新的 run；已取消的轮次不写入历史。
func (s *Service) Cancel() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// emit 发送事件；channel 满时丢弃（仅发生在 UI 已停止消费的退出路径）。
func (s *Service) emit(event Event) {
	select {
	case s.events <- event:
	default:
	}
}

// Close 取消进行中的 run 并等待其收尾，保证退出时不残留 goroutine。
// 事件在 UI 停止消费时可能被丢弃（见 eventBuffer）。
func (s *Service) Close() {
	s.Cancel()
	s.wg.Wait()
}

// Idle 供测试等待全部 run 收尾。
func (s *Service) waitIdle() { s.wg.Wait() }
