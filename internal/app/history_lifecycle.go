package app

import (
	"context"
	"errors"
	"time"

	"plume-agent/internal/store"
)

// ErrHistoryUncertain 表示提交结果无法核查；禁止新 run 与会话切换，重开后再校验磁盘。
var ErrHistoryUncertain = errors.New("history not saved: commit state unknown; reopen plume before continuing")

// resolveCommitLocked 核查成功终态与完整轮次，检查点清理由 GetRunState
// 在同一读取快照内验证。调用方持有 s.mu；查不到或三者不一致时冻结当前
// 进程，不自动重试带副作用的任务，不能只看 completed 标签。
func (s *Service) resolveCommitLocked(ctx context.Context, runID, sessionID string) (bool, error) {
	state, err := s.history.GetRunState(ctx, runID)
	if err != nil || state.SessionID != sessionID || (state.Status == store.RunCompleted) != state.HasTurn {
		s.historyUncertain = true
		return false, errors.Join(ErrHistoryUncertain, err)
	}
	return state.Status == store.RunCompleted && state.HasTurn, nil
}

// ShutdownTimeout 返回配置的总收尾期限；原子读取避免被正在写盘的终态锁拖住。
func (s *Service) ShutdownTimeout() time.Duration {
	if timeout := time.Duration(s.shutdownTimeout.Load()); timeout > 0 {
		return timeout
	}
	return defaultShutdownTimeout
}

// isClosing 通过只关闭一次的通道查询退出状态，不等待持久化正在持有的互斥锁。
func (s *Service) isClosing() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// exitState 是退出摘要的原子快照：会话、是否已有已登记 run、最后 run
// 及其保存结论。收尾超时后退出路径不得再等待 Service.mu 或存储调用，
// 摘要只读冻结值；身份随 Submit/切换/接管在 mu 下同步进本快照。
type exitState struct {
	sessionID     string
	hasRun        bool
	lastRunID     string
	lastRunStatus string
}

// recordExitRun 在 mu 下记录最后 run 的身份与保存结论；同时刷新会话身份
// 与 hasRun（Submit 登记起点时写 running，finish 用实际 saveStatus 覆盖）。
// 调用点必须在 StartRun 成功之后立即执行，保证退出摘要绑定本轮 run。
func (s *Service) recordExitRun(runID, status string) {
	hasRun := true
	if s.commands != nil {
		hasRun = s.commands.hasRun
	}
	s.exitSnapshot.Store(&exitState{sessionID: s.sessionID, hasRun: hasRun, lastRunID: runID, lastRunStatus: status})
}

// recordExitSession 在切换/接管会话时重置快照：新会话没有本轮 run，
// 不能把旧会话的保存结论带到新身份上。
func (s *Service) recordExitSession() {
	hasRun := false
	if s.commands != nil {
		hasRun = s.commands.hasRun
	}
	s.exitSnapshot.Store(&exitState{sessionID: s.sessionID, hasRun: hasRun})
}

// ExitSnapshot 是退出收尾前冻结的会话身份；必须在 Close 之前捕获，
// 打印摘要阶段不再触碰任何可阻塞状态。
type ExitSnapshot struct {
	SessionID string
	LastRunID string
	HasRun    bool
}

// CaptureExitState 冻结当前会话身份与最后已登记 run。它只读原子快照、
// 不等待 Service.mu：run 正在持锁写盘时退出路径也能立即取得快照并进入
// 期限收尾。
func (s *Service) CaptureExitState() ExitSnapshot {
	snapshot := ExitSnapshot{}
	if state := s.exitSnapshot.Load(); state != nil {
		snapshot.SessionID, snapshot.HasRun, snapshot.LastRunID = state.sessionID, state.hasRun, state.lastRunID
	}
	return snapshot
}

// ExitSummary 给出最后已登记 run 的保存结论。收尾超时（shutdownOK=false）
// 或结论与冻结身份不一致时如实 unknown；本进程没有新 run 时历史库已按期
// 关闭、既有终态完好，结论为 saved。结论绑定本轮 run，不复用上一轮结果。
func (s *Service) ExitSummary(snapshot ExitSnapshot, shutdownOK bool) string {
	if !shutdownOK {
		return "unknown"
	}
	if !snapshot.HasRun {
		return "unknown" // 调用方不应为空会话打印恢复提示；防御性返回。
	}
	state := s.exitSnapshot.Load()
	// 无新 run 的 saved 也必须来自同一冻结会话；先核对完整身份，不能
	// 把切换后的空 lastRunID 或缺失状态当成原任务已确认保存。
	if state == nil || state.sessionID != snapshot.SessionID || state.lastRunID != snapshot.LastRunID {
		return "unknown"
	}
	if state.lastRunID == "" {
		return "saved"
	}
	switch state.lastRunStatus {
	case "saved":
		return "saved"
	case "failed", "unknown":
		return state.lastRunStatus
	default:
		return "unknown" // running：终态保存未确认。
	}
}

// CloseContext 停止准入、请求取消，并在调用方的总期限内等待执行停止。
// 超时仍保持 closed/执行锁所有权；只有返回 nil 才能安全关闭 runtime 与历史库。
// 后台等待用于兼容不遵守取消契约的清理操作，生产入口到期后直接结束进程。
func (s *Service) CloseContext(ctx context.Context) error {
	stop := context.AfterFunc(ctx, s.cancelShutdown)
	defer stop()
	s.closeOnce.Do(func() {
		close(s.done)
		go func() {
			s.mu.Lock()
			s.closed = true
			if s.cancel != nil {
				s.cancel()
			}
			s.mu.Unlock()
			s.wg.Wait()
			s.cancelShutdown()
			close(s.stopped)
		}()
	})
	select {
	case <-s.stopped:
		return nil
	case <-ctx.Done():
		s.cancelShutdown()
		return ctx.Err()
	}
}
