package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// LoadTiming 先按可信权限域定位，再在单一读事务中汇总；不因目录有大量
// 正文而把 /status 变成完整历史读取，也不把取消/失败耗时计入完成统计。
func (s *SQLiteStore) LoadTiming(ctx context.Context, scope Scope, id string) (TimingSummary, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TimingSummary{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ? AND scope_key = ? AND workspace = ?`, id, normalizedScope(scope.ScopeKey), scope.Workspace).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return TimingSummary{}, ErrSessionNotFound
	}
	if err != nil {
		return TimingSummary{}, err
	}
	var count int
	var millis int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(MAX(0, COALESCE(finished_at, started_at) - started_at)), 0) FROM runs WHERE session_id = ? AND status = ?`, id, RunCompleted).Scan(&count, &millis)
	if err != nil {
		return TimingSummary{}, fmt.Errorf("aggregate completed run timing: %w", err)
	}
	if millis < 0 || millis > math.MaxInt64/int64(time.Millisecond) {
		return TimingSummary{}, ErrHistoryLimit
	}
	return TimingSummary{CompletedRuns: count, CompletedDuration: time.Duration(millis) * time.Millisecond}, nil
}

// LoadTiming 与 SQLite 使用相同毫秒口径，持锁读取稳定终态快照。
func (m *MemoryStore) LoadTiming(_ context.Context, scope Scope, id string) (TimingSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, exists := m.sessions[id]
	if !exists || !scopeMatches(session.record, scope) {
		return TimingSummary{}, ErrSessionNotFound
	}
	var summary TimingSummary
	var total int64
	for _, run := range m.runs {
		if run.SessionID != id || run.Status != RunCompleted {
			continue
		}
		summary.CompletedRuns++
		if run.FinishedAt.IsZero() || !run.FinishedAt.After(run.StartedAt) {
			continue
		}
		duration := run.FinishedAt.UnixMilli() - run.StartedAt.UnixMilli()
		if duration < 0 || duration > math.MaxInt64/int64(time.Millisecond)-total {
			return TimingSummary{}, ErrHistoryLimit
		}
		total += duration
	}
	summary.CompletedDuration = time.Duration(total) * time.Millisecond
	return summary, nil
}
