package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"plume-agent/internal/agent"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/telemetry"
)

// EventKind 是 app 推送给 UI 的事件类别。一次 run 恰好一个终态事件。
type EventKind string

// 事件类别值；run_completed 与 run_failed 是互斥终态，一次 run 恰好发出其一。
const (
	EventRunStarted     EventKind = "run_started"
	EventRunCompleted   EventKind = "run_completed"
	EventRunFailed      EventKind = "run_failed"
	EventRunPhase       EventKind = "run_phase"
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventUsageUpdate    EventKind = "usage_update"
	EventToolUpdate     EventKind = "tool_update"
)

// Phase 是 run 的展示阶段，驱动状态栏文案与首字计时口径（首思考/首答案
// 相对单次模型调用计时，而非整个 run）。
type Phase string

// 阶段流转：preparing（拼装请求，含读人格）→ waiting（模型调用已发出）
// → thinking/responding（首个思考/答案增量到达时切换）。
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
	Tool           agent.ToolEvent
	ModelCall      int
}

// ErrBusy 表示当前已有 run 在执行。首版同会话串行：再次发送被明确拒绝，
// 不创建并发模型请求或隐式队列（阶段计划 §2）。
var ErrBusy = errors.New("a run is already in progress")

// eventBuffer 是事件 channel 容量。UI 消费循环持续读取时足够吸收突发；
const eventBuffer = 8

// Service 把 agent 轮次包装成异步 run：Submit 立即返回 run ID 并接受输入，
// 事件经有界 channel 推送给 UI；Cancel 打断当前 run。失败/取消的轮次
// 不进入会话历史（0003/阶段计划 §2：只把完整提交的轮次加入历史）。
// 启用持久层后（EnablePersistence），run 起点、每次模型调用与终态先落盘：
// 起点失败不发请求，快照失败停止后续步骤，成功轮次与终态一个事务提交。
type Service struct {
	runtime *agent.Runtime
	session *Session
	events  chan Event

	mu               sync.Mutex
	busy             bool
	cancel           context.CancelFunc
	pending          string // 提交时暂存的用户输入，成功后随回复一起入历史
	sessionID        string
	sessionCreatedAt time.Time
	history          store.Store // nil 表示进程内存模式（offline/禁用持久化）
	workspace        string
	modelRef         store.ModelRef
	shutdownTimeout  atomic.Int64
	historyUncertain bool // 提交无法核查时冻结执行权，直到进程重开校验。
	shutdownContext  context.Context
	cancelShutdown   context.CancelFunc
	stopped          chan struct{}

	wg        sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once
	closed    bool
	recorder  *telemetry.ModelRecorder
}

// NewService 构造服务；会话 ID 立即生成，run ID 在每次 Submit 时生成。
func NewService(runtime *agent.Runtime, recorders ...*telemetry.ModelRecorder) *Service {
	shutdownContext, cancelShutdown := context.WithCancel(context.Background())
	s := &Service{
		runtime:          runtime,
		session:          NewSession(),
		events:           make(chan Event, eventBuffer+1),
		done:             make(chan struct{}),
		sessionID:        newSessionID(),
		sessionCreatedAt: time.Now(),
		shutdownContext:  shutdownContext,
		cancelShutdown:   cancelShutdown,
		stopped:          make(chan struct{}),
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
// 一起原子追加，失败/取消不留半截上下文。持久层起点写入失败时同样返回
// 错误：不发模型请求、不执行工具、不占用执行权。
func (s *Service) Submit(ctx context.Context, input string) (string, error) {
	if input == "" {
		return "", model.NewError(model.ErrInvalidConfig).WithSummary("empty input")
	}
	if s.isClosing() {
		return "", errors.New("service is closed")
	}

	s.mu.Lock()
	if s.closed || s.isClosing() {
		s.mu.Unlock()
		return "", errors.New("service is closed")
	}
	if s.historyUncertain {
		s.mu.Unlock()
		return "", ErrHistoryUncertain
	}
	if s.busy || len(s.events) == cap(s.events) {
		s.mu.Unlock()
		return "", ErrBusy
	}
	runID := newRunID()
	accepted := time.Now()
	if s.history != nil {
		// 起点事务（含会话按 ID 幂等落盘）失败时不发请求：见方案 §4.2 顺序 1。
		start := store.RunRecord{ID: runID, Input: input, StartedAt: accepted}
		if err := s.history.StartRun(ctx, s.sessionRecordLocked(accepted), start); err != nil {
			s.mu.Unlock()
			return "", fmt.Errorf("history storage: %w", err)
		}
	}
	// 起点事务可能等待磁盘；退出已开始时不得再发起模型请求。
	if s.isClosing() {
		s.mu.Unlock()
		return "", errors.New("service is closed")
	}
	runCtx, cancel := context.WithCancel(ctx)
	if timeout := s.runtime.RunTimeout(); timeout != 0 {
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	runCtx = telemetry.WithRunID(runCtx, runID)
	s.busy = true
	s.cancel = cancel
	s.pending = input
	sessionID := s.sessionID
	if s.recorder != nil {
		s.recorder.RunStart(sessionID, runID, accepted)
	}
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		// storageErr 记录持久层故障：起点失败取消整个 run，快照失败中止
		// 后续步骤；回调与 finish 都在同一 goroutine 串行执行，无并发写。
		var storageErr error
		storageStage := ""
		var toolEvents []store.ToolEventRecord
		s.emit(Event{Kind: EventRunStarted, RunID: runID, Phase: PhasePreparing, AcceptedAt: accepted}, runCtx)
		phase := PhasePreparing
		var callingAt time.Time
		var preparation, firstReasoning, firstAnswer time.Duration
		modelCall := 0
		callID := func() string { return fmt.Sprintf("%s/model-%d", runID, modelCall) }
		result, err := s.runtime.RunStreamWithTools(runCtx, s.session.History(), input, func(stepCtx context.Context) {
			modelCall++
			callingAt = time.Now()
			if modelCall == 1 {
				preparation = callingAt.Sub(accepted)
			}
			phase = PhaseWaiting
			// 调用起点先落盘；失败则取消整个 run，模型请求不会发出。
			if s.history != nil && storageErr == nil {
				call := store.ModelCallRecord{ID: callID(), RunID: runID, SessionID: sessionID, Sequence: modelCall, SavedAt: callingAt}
				if err := s.history.StartModelCall(stepCtx, call); err != nil {
					storageErr = fmt.Errorf("history storage: %w", err)
					storageStage = "call_start"
					cancel()
					return
				}
			}
			s.session.ObserveUsage(callID(), model.Usage{})
			s.emit(Event{Kind: EventRunPhase, RunID: runID, Phase: phase, ModelCall: modelCall, Stats: s.session.Statistics()}, stepCtx)
		}, func(stepCtx context.Context, e model.Event) error {
			next := phase
			ev := Event{RunID: runID}
			switch e.Kind {
			case model.EventUsageUpdate:
				s.session.ObserveUsage(callID(), e.Usage)
				returnStatus := s.emit(Event{Kind: EventUsageUpdate, RunID: runID, Usage: e.Usage, Stats: s.session.Statistics()}, stepCtx)
				// 快照保存失败时停止后续模型/工具步骤（方案 §4.2 顺序 2）；
				// 替换语义天然去重，不影响已展示的统计。
				if s.history != nil && storageErr == nil {
					if err := s.history.SaveUsage(stepCtx, callID(), store.FromUsage(e.Usage), time.Now()); err != nil {
						storageErr = fmt.Errorf("history storage: %w", err)
						storageStage = "usage"
						return storageErr
					}
				}
				if !returnStatus {
					return stepCtx.Err()
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
				if !s.emit(Event{Kind: EventRunPhase, RunID: runID, Phase: phase}, stepCtx) {
					return stepCtx.Err()
				}
			}
			if !s.emit(ev, stepCtx) {
				return stepCtx.Err()
			}
			return nil
		}, func(stepCtx context.Context, e agent.ToolEvent) error {
			toolEvents = append(toolEvents, store.ToolEventRecord{Name: e.Name, Status: e.Status, Summary: e.Summary, DurationMS: e.Duration.Milliseconds()})
			if !s.emit(Event{Kind: EventToolUpdate, RunID: runID, Tool: e}, stepCtx) {
				return stepCtx.Err()
			}
			return nil
		})
		total := time.Since(accepted)
		if callingAt.IsZero() {
			preparation = total
		}
		s.finish(runCtx, runID, sessionID, accepted, result, err, total, preparation, firstReasoning, firstAnswer, toolEvents, storageErr, storageStage)
	}()
	return runID, nil
}

// finish 写入唯一终态事件；成功时先持久化完整轮次与终态（一个事务），
// 成功后才把 user/assistant 原子提交进内存历史。持久化失败不得伪称
// completed：事件降级为失败并标记存储错误，内存历史保持不变。
// 终态保存使用独立的有截止收尾 context，不把已取消的请求 ctx 传给数据库。
func (s *Service) finish(ctx context.Context, runID, sessionID string, accepted time.Time, result *agent.RunResult, err error, total, preparation, firstReasoning, firstAnswer time.Duration, toolEvents []store.ToolEventRecord, storageErr error, storageStage string) {
	s.mu.Lock()
	// 取消与历史提交以同一把锁确定先后，覆盖模型收完后关闭流的时间窗口。
	if err == nil {
		err = ctx.Err()
	}
	runErr := err
	if storageErr != nil {
		// 存储失败是独立的保存结果，不冒充模型失败，也不被取消掩盖。
		err = storageErr
	}
	ev := Event{
		Kind:  EventRunCompleted,
		RunID: runID,
		Err:   err, Duration: total, Preparation: preparation, FirstReasoning: firstReasoning, FirstAnswer: firstAnswer,
	}
	if result != nil {
		ev.Reply = result.LastMessage.Content
		ev.Reasoning = result.LastMessage.Reasoning
		ev.FinishReason = result.FinishReason
		ev.Usage = result.Usage
		// finish 与 usage_update 是同一个模型调用，按 ID 替换而不累加。
		if result.ModelCalls > 0 {
			s.session.observeUsage(fmt.Sprintf("%s/model-%d", runID, result.ModelCalls), result.Usage, true)
		}
	}
	ev.Stats = s.session.Statistics()
	saveStatus := "disabled"
	if s.history != nil {
		// 独立收尾期限：不把已取消的 run ctx 传给数据库；cancel 在锁释放后执行。
		saveCtx, cancelSave := s.finishContext()
		defer cancelSave()
		saveStatus = "saved"
		if err == nil {
			// 成功路径：先落盘再改内存；失败时事件降级，不伪称已保存完成。
			record := s.terminalRecord(runID, sessionID, accepted, store.RunCompleted, "", result, toolEvents)
			if serr := s.history.CompleteRun(saveCtx, record, result.Messages); serr != nil {
				// 返回错误不证明事务未提交；先核查同一 run，不重跑模型/工具。
				committed, queryErr := s.resolveCommitLocked(saveCtx, runID, sessionID)
				if queryErr != nil {
					err, saveStatus = errors.Join(ErrHistoryUncertain, serr, queryErr), "unknown"
				} else if !committed {
					err, saveStatus = fmt.Errorf("history not saved: %w", serr), "failed"
					record.Status, record.Error = store.RunFailed, "history not saved"
					// 已确认无成功轮次；尽力记录保存失败，仍不追加模型历史。
					if ferr := s.history.FinishRun(saveCtx, record); ferr != nil {
						_, qerr := s.resolveCommitLocked(saveCtx, runID, sessionID)
						if qerr != nil {
							err, saveStatus = errors.Join(err, qerr), "unknown"
						}
					}
				}
			}
		} else {
			ev.Kind = EventRunFailed
			// 失败/取消/存储失败：尽力保存展示记录，保存失败不掩盖原错误。
			status, summary := store.RunFailed, err.Error()
			if storageErr == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				status = store.RunCancelled
			}
			record := s.terminalRecord(runID, sessionID, accepted, status, summary, result, toolEvents)
			if serr := s.history.FinishRun(saveCtx, record); serr != nil {
				saveStatus = "failed"
				state, qerr := s.history.GetRunState(saveCtx, runID)
				if qerr != nil || state.SessionID != sessionID || state.Status == store.RunCompleted {
					s.historyUncertain = true
					err, saveStatus = errors.Join(err, ErrHistoryUncertain, serr), "unknown"
				} else if state.Status != store.RunRunning {
					saveStatus = "saved"
				} else {
					err = errors.Join(err, fmt.Errorf("history not saved: %w", serr))
				}
			}
		}
	} else if err != nil {
		ev.Kind = EventRunFailed
	}
	if err == nil {
		s.session.AppendTurn(result.Messages)
	}
	if err != nil {
		ev.Kind, ev.Err = EventRunFailed, err
	}
	if s.recorder != nil {
		reasoningBytes, answerBytes := len(ev.Reasoning), len(ev.Reply)
		if result != nil {
			reasoningBytes, answerBytes = len(result.Message.Reasoning), len(result.Message.Content)
		}
		s.recorder.RunEnd(sessionID, runID, telemetry.RunMetrics{Duration: total, Preparation: preparation, FirstReasoning: firstReasoning, FirstAnswer: firstAnswer, ReasoningBytes: reasoningBytes, AnswerBytes: answerBytes, SaveStatus: saveStatus, SaveErrorStage: storageStage}, runErr)
	}
	// 非终态最多占八格，最后一格保留给当前终态；取消不依赖消费者继续读取。
	s.emit(ev, context.Background())
	s.busy = false
	s.cancel = nil
	s.pending = ""
	s.mu.Unlock()
}

// terminalRecord 构造终态落盘记录；调用方须持有 s.mu。
func (s *Service) terminalRecord(runID, sessionID string, accepted time.Time, status store.RunStatus, summary string, result *agent.RunResult, toolEvents []store.ToolEventRecord) store.RunRecord {
	record := store.RunRecord{
		ID: runID, SessionID: sessionID, Status: status, Error: summary,
		StartedAt: accepted, FinishedAt: time.Now(), ToolEvents: toolEvents,
	}
	if result != nil {
		// 展示记录保留所有已显示模型步骤；后续调用开始会清空 LastMessage，
		// 只能让终态事件用它定位当前答案区，不能因此丢失早期正文/Thought。
		record.Reply = result.Message.Content
		record.Reasoning = result.Message.Reasoning
	}
	return record
}

// finishContext 提供终态保存的独立收尾期限；不携带已取消的 run context。
// 调用方负责 cancel（锁释放后执行，避免持锁阻塞收尾）。
func (s *Service) finishContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.shutdownContext, s.ShutdownTimeout())
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

// Close 按配置期限取消并等待 run；需要处理超时与资源所有权的入口应调用 CloseContext。
// 超时不代表执行已停止，调用方不得据此关闭数据库或释放执行锁。
func (s *Service) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), s.ShutdownTimeout())
	defer cancel()
	_ = s.CloseContext(ctx)
}

// Idle 供测试等待全部 run 收尾。
func (s *Service) waitIdle() { s.wg.Wait() }
