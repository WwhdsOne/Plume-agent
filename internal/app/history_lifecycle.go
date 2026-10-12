package app

import (
	"context"
	"errors"
	"time"

	"plume-agent/internal/store"
)

// ErrHistoryUncertain 表示提交结果无法核查；禁止新 run 与会话切换，重开后再校验磁盘。
var ErrHistoryUncertain = errors.New("history not saved: commit state unknown; reopen plume before continuing")

// resolveCommitLocked 核查成功事务的两部分，调用方持有 s.mu，不能只看 completed 标签。
// 查不到或状态不一致时冻结当前进程，不自动重试带副作用的任务。
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
