package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // 驱动注册名 "sqlite"；纯 Go 转译实现，无 CGO

	"plume-agent/internal/model"
)

// schemaVersion 是当前迁移版本，与 config schema 分开管理；只允许前向迁移，
// 遇到比程序更新的版本拒绝写入。
const schemaVersion = 2

// SQLiteStore 是本机文件后端：一个 history.db 承载会话、run、轮次与调用，
// 打开时持有整库进程独占锁直至 Close。单进程独占下不启用 WAL，事务使用
// 默认 journal 模式即可；不把数据库文件当配置文件做整文件替换。
type SQLiteStore struct {
	db        *sql.DB
	lock      *processLock
	path      string
	closeOnce sync.Once
	closeErr  error
}

// OpenSQLite 打开（或创建）数据库：先取进程独占锁，再校验迁移版本并建表。
// 目录以 0700 创建，数据库与锁文件 0600；父目录不存在时自动创建。
func OpenSQLite(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("history database path is empty")
	}
	path, err := canonicalDatabasePath(path)
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	// 先私密创建再交给 SQLite；驱动默认创建权限受 umask 影响，可能成为0644。
	if err := prepareDatabaseFiles(path); err != nil {
		lock.Release()
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		lock.Release()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// 单进程独占下的保守参数：不给并发连接留窗口，语句超时防意外挂死。
	if _, err = db.Exec("PRAGMA busy_timeout = 5000; PRAGMA foreign_keys = ON;"); err != nil {
		db.Close()
		lock.Release()
		return nil, err
	}
	s := &SQLiteStore{db: db, lock: lock, path: path}
	if err = s.migrate(); err != nil {
		db.Close()
		lock.Release()
		return nil, err
	}
	// 新 schema 拒绝必须先于任何会改写文件头的日志模式设置，WAL→DELETE
	// 即使没有业务写入也会改变数据库字节；版本边界由回归测试守住。
	if _, err = db.Exec("PRAGMA journal_mode = DELETE;"); err != nil {
		db.Close()
		lock.Release()
		return nil, err
	}
	return s, nil
}

// migrate 用 PRAGMA user_version 前向迁移；每步 DDL 与版本发布同事务。
// 版本比程序新时拒绝，保留文件不删库。
func (s *SQLiteStore) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("%w: database v%d > build v%d", ErrSchemaNewer, version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if version < 1 {
		for _, statement := range schemaStatements {
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if version < 2 {
		for _, statement := range managementSchemaStatements {
			if _, err = tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// StartRun 幂等建会话并登记 run 起点，全部在一个事务内。
func (s *SQLiteStore) StartRun(ctx context.Context, session SessionRecord, run RunRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM runs WHERE id = ?", run.ID).Scan(&exists); err == nil {
		return ErrDuplicateRun
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	session = normalizeSession(session)
	scope := Scope{ScopeKey: session.ScopeKey, Workspace: session.Workspace}
	if err := checkSQLiteOwner(ctx, tx, scope, session.ID, false); err != nil {
		return err
	}
	ref, err := json.Marshal(session.ModelRef)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions (id, scope_key, workspace, title, title_manual, model_ref, created_at, last_run_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET last_run_at = MAX(last_run_at, excluded.last_run_at)`,
		session.ID, session.ScopeKey, session.Workspace, session.Title, session.TitleManual, string(ref), unixMS(session.CreatedAt), unixMS(run.StartedAt)); err != nil {
		return err
	}
	tools, err := json.Marshal(run.ToolEvents)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs (id, session_id, input, status, error, started_at, reply, reasoning, tool_events)
		VALUES (?, ?, ?, 'running', '', ?, '', '', ?)`,
		run.ID, session.ID, run.Input, unixMS(run.StartedAt), string(tools)); err != nil {
		return err
	}
	// 首条普通消息也必须原子更新绑定，否则 gateway 重启会忘记当前会话。
	if err := bindSQLiteSession(ctx, tx, scope, session.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// StartModelCall 幂等登记调用起点（usage 保持 NULL 表示未知）。
func (s *SQLiteStore) StartModelCall(ctx context.Context, call ModelCallRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO model_calls (id, run_id, session_id, sequence, usage, saved_at)
		VALUES (?, ?, ?, ?, NULL, ?)
		ON CONFLICT(id) DO NOTHING`,
		call.ID, call.RunID, call.SessionID, call.Sequence, unixMS(call.SavedAt))
	return err
}

// SaveUsage 按 call ID 替换快照；调用不存在拒绝补记。
func (s *SQLiteStore) SaveUsage(ctx context.Context, callID string, usage StoredUsage, at time.Time) error {
	encoded, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE model_calls SET usage = ?, saved_at = ? WHERE id = ?", string(encoded), unixMS(at), callID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrUnknownCall
	}
	return nil
}

// CompleteRun 用一个事务提交轮次、终态与会话活动时间；重复终态为幂等空操作。
func (s *SQLiteStore) CompleteRun(ctx context.Context, run RunRecord, turn []model.Message) error {
	if err := validateTurn(turn, make(map[string]bool)); err != nil {
		return err
	}
	return s.finishTx(ctx, run, RunCompleted, turn)
}

// FinishRun 提交非成功终态；已完成事务保持第一次结果。
func (s *SQLiteStore) FinishRun(ctx context.Context, run RunRecord) error {
	return s.finishTx(ctx, run, run.Status, nil)
}

func (s *SQLiteStore) finishTx(ctx context.Context, run RunRecord, status RunStatus, turn []model.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current string
	var sessionID, input string
	if err = tx.QueryRowContext(ctx, "SELECT status, session_id, input FROM runs WHERE id = ?", run.ID).Scan(&current, &sessionID, &input); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownRun
		}
		return err
	}
	if sessionID != run.SessionID {
		return fmt.Errorf("%w: run session mismatch", ErrCorruptHistory)
	}
	if current != string(RunRunning) {
		return tx.Commit() // 幂等：保留第一次终态。
	}
	if turn != nil {
		encoded, marshalErr := encodeTurn(turn)
		if marshalErr != nil {
			return marshalErr
		}
		if turn[0].Content != input {
			return fmt.Errorf("%w: turn input mismatch", ErrCorruptHistory)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO turns (session_id, seq, run_id, messages, created_at)
			VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM turns WHERE session_id = ?), ?, ?, ?)`,
			run.SessionID, run.SessionID, run.ID, string(encoded), unixMS(run.FinishedAt)); err != nil {
			return err
		}
	}
	tools, marshalErr := json.Marshal(run.ToolEvents)
	if marshalErr != nil {
		return marshalErr
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?, finished_at = ?, reply = ?, reasoning = ?, tool_events = ? WHERE id = ?`,
		string(status), run.Error, unixMS(run.FinishedAt), run.Reply, run.Reasoning, string(tools), run.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sessions SET last_run_at = MAX(last_run_at, ?) WHERE id = ?", unixMS(run.FinishedAt), run.SessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// GetRunState 在一个读取事务内确认 run 与唯一完整成功轮次，先计量再解码，
// 不能把 completed 标记本身当成事务完整提交的证据。
func (s *SQLiteStore) GetRunState(ctx context.Context, runID string) (RunState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var state RunState
	var input string
	if err := tx.QueryRowContext(ctx, "SELECT session_id, status, input FROM runs WHERE id = ?", runID).Scan(&state.SessionID, &state.Status, &input); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, ErrUnknownRun
		}
		return state, err
	}
	if !validRunStatus(state.Status) {
		return state, fmt.Errorf("%w: invalid run status", ErrCorruptHistory)
	}
	var count, size int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(length(CAST(messages AS BLOB))), 0) FROM turns WHERE run_id = ?", runID).Scan(&count, &size); err != nil {
		return state, err
	}
	if err := checkLoadSize(count, size, normalizeLoadLimits(nil)); err != nil {
		return state, err
	}
	if count > 1 || (state.Status == RunCompleted) != (count == 1) {
		return state, fmt.Errorf("%w: run outcome and turn disagree", ErrCorruptHistory)
	}
	if count == 1 {
		var sessionID, encoded string
		var seq int
		if err := tx.QueryRowContext(ctx, "SELECT session_id, seq, messages FROM turns WHERE run_id = ?", runID).Scan(&sessionID, &seq, &encoded); err != nil {
			return state, err
		}
		messages, err := decodeTurn([]byte(encoded), make(map[string]bool))
		if err != nil {
			return state, err
		}
		if seq <= 0 || sessionID != state.SessionID || messages[0].Content != input {
			return state, fmt.Errorf("%w: turn run association mismatch", ErrCorruptHistory)
		}
		state.HasTurn = true
	}
	return state, nil
}

// boundedRead 将条数与 UTF-8 字节数预检和实际加载放进同一快照；防止
// 先通过小数据检查、随后读取变大的集合。statement 只使用包内固定 SQL。
func (s *SQLiteStore) boundedRead(ctx context.Context, statement, sessionID string, limits []LoadLimits) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var records, size int64
	if err = tx.QueryRowContext(ctx, statement, sessionID).Scan(&records, &size); err == nil {
		err = checkLoadSize(records, size, normalizeLoadLimits(limits))
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// InterruptRunning 把遗留 running 标记 interrupted；只改状态与结束时间，
// 未落盘的流式片段与 usage 保持未知，不补零。
func (s *SQLiteStore) InterruptRunning(ctx context.Context, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE runs SET status = ?, finished_at = ? WHERE status = ?", string(RunInterrupted), unixMS(at), string(RunRunning))
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rows), nil
}

// LatestSession 返回工作区最近有 run 的会话（按 last_run_at 降序取一）。
func (s *SQLiteStore) LatestSession(ctx context.Context, workspace string) (SessionRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, sessionSelect+`
		WHERE workspace = ? AND scope_key = 'local' AND EXISTS(SELECT 1 FROM runs WHERE runs.session_id = sessions.id)
		ORDER BY last_run_at DESC, (SELECT MAX(rowid) FROM runs WHERE runs.session_id = sessions.id) DESC LIMIT 1`, workspace)
	return scanSession(row.Scan)
}

// LoadTurns 按轮次序加载全部成功消息组。
func (s *SQLiteStore) LoadTurns(ctx context.Context, sessionID string, limits ...LoadLimits) ([]model.Message, int, error) {
	tx, err := s.boundedRead(ctx, "SELECT COUNT(*), COALESCE(SUM(length(CAST(messages AS BLOB))), 0) FROM turns WHERE session_id = ?", sessionID, limits)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var completed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE session_id = ? AND status = ?", sessionID, RunCompleted).Scan(&completed); err != nil {
		return nil, 0, err
	}
	if err := checkLoadSize(int64(completed), 0, normalizeLoadLimits(limits)); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT turns.seq, turns.run_id, turns.messages, runs.status, runs.session_id, runs.input FROM turns LEFT JOIN runs ON runs.id = turns.run_id WHERE turns.session_id = ? ORDER BY turns.seq`, sessionID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var messages []model.Message
	seq := 0
	seen := make(map[string]bool)
	seenRuns := make(map[string]bool)
	for rows.Next() {
		var encoded string
		var storedSeq int
		var runID string
		var status, runSession, input sql.NullString
		if err = rows.Scan(&storedSeq, &runID, &encoded, &status, &runSession, &input); err != nil {
			return nil, 0, err
		}
		turn, decodeErr := decodeTurn([]byte(encoded), seen)
		if decodeErr != nil {
			return nil, 0, decodeErr
		}
		if storedSeq != seq+1 || seenRuns[runID] || !status.Valid || status.String != string(RunCompleted) || runSession.String != sessionID || !input.Valid || input.String != turn[0].Content {
			return nil, 0, fmt.Errorf("%w: invalid turn run association", ErrCorruptHistory)
		}
		seenRuns[runID] = true
		messages = append(messages, turn...)
		seq++
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	if seq != completed {
		return nil, 0, fmt.Errorf("%w: completed run without turn", ErrCorruptHistory)
	}
	return messages, seq + 1, nil
}

// ListRuns 按起点时间返回会话全部 run 记录。
func (s *SQLiteStore) ListRuns(ctx context.Context, sessionID string, limits ...LoadLimits) ([]RunRecord, error) {
	tx, err := s.boundedRead(ctx, `SELECT COUNT(*), COALESCE(SUM(length(CAST(id AS BLOB)) + length(CAST(input AS BLOB)) + length(CAST(status AS BLOB)) + length(CAST(error AS BLOB)) + length(CAST(reply AS BLOB)) + length(CAST(reasoning AS BLOB)) + length(CAST(tool_events AS BLOB))), 0) FROM runs WHERE session_id = ?`, sessionID, limits)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, input, status, error, started_at, finished_at, reply, reasoning, tool_events
		FROM runs WHERE session_id = ? ORDER BY started_at, rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunRecord
	for rows.Next() {
		var record RunRecord
		var startedMS int64
		var finishedMS sql.NullInt64
		var tools string
		if err = rows.Scan(&record.ID, &record.Input, &record.Status, &record.Error, &startedMS, &finishedMS, &record.Reply, &record.Reasoning, &tools); err != nil {
			return nil, err
		}
		record.SessionID = sessionID
		if !validRunStatus(record.Status) {
			return nil, fmt.Errorf("%w: invalid run status", ErrCorruptHistory)
		}
		record.StartedAt = fromUnixMS(startedMS)
		if finishedMS.Valid {
			record.FinishedAt = fromUnixMS(finishedMS.Int64)
		}
		if tools != "" {
			if err := checkJSONExpansion([]byte(tools)); err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(tools), &record.ToolEvents); err != nil {
				return nil, fmt.Errorf("corrupt tool events: %w", err)
			}
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// LoadCalls 按起点时间与序号加载会话全部调用记录。
func (s *SQLiteStore) LoadCalls(ctx context.Context, sessionID string, limits ...LoadLimits) ([]ModelCallRecord, error) {
	tx, err := s.boundedRead(ctx, `SELECT COUNT(*), COALESCE(SUM(length(CAST(id AS BLOB)) + length(CAST(run_id AS BLOB)) + COALESCE(length(CAST(usage AS BLOB)), 0)), 0) FROM model_calls WHERE session_id = ?`, sessionID, limits)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, run_id, sequence, usage, saved_at
		FROM model_calls WHERE session_id = ? ORDER BY saved_at, rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelCallRecord
	for rows.Next() {
		var record ModelCallRecord
		var encoded sql.NullString
		var savedMS int64
		if err = rows.Scan(&record.ID, &record.RunID, &record.Sequence, &encoded, &savedMS); err != nil {
			return nil, err
		}
		record.SessionID = sessionID
		record.SavedAt = fromUnixMS(savedMS)
		if encoded.Valid && encoded.String != "" {
			if err := checkJSONExpansion([]byte(encoded.String)); err != nil {
				return nil, err
			}
			var usage StoredUsage
			if err = json.Unmarshal([]byte(encoded.String), &usage); err != nil {
				return nil, fmt.Errorf("corrupt usage payload: %w", err)
			}
			record.Usage = &usage
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// Close 关闭数据库并释放整库锁；重复关闭安全。
func (s *SQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.db.Close(); s.lock.Release() })
	return s.closeErr
}

type rowScanner func(dest ...any) error

func scanSession(scan rowScanner) (SessionRecord, bool, error) {
	var record SessionRecord
	var createdMS, runMS int64
	var encoded string
	if err := scan(&record.ID, &record.ScopeKey, &record.Workspace, &record.Title, &record.TitleManual, &encoded, &createdMS, &runMS, &record.RunCount, &record.TurnCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionRecord{}, false, nil
		}
		return SessionRecord{}, false, err
	}
	if err := checkLoadSize(1, int64(len(encoded)+len(record.ID)+len(record.Title)+len(record.ScopeKey)+len(record.Workspace)), normalizeLoadLimits(nil)); err != nil {
		return SessionRecord{}, false, err
	}
	if err := checkJSONExpansion([]byte(encoded)); err != nil {
		return SessionRecord{}, false, err
	}
	if err := json.Unmarshal([]byte(encoded), &record.ModelRef); err != nil {
		return SessionRecord{}, false, fmt.Errorf("corrupt model ref: %w", err)
	}
	record.CreatedAt = fromUnixMS(createdMS)
	if runMS != 0 {
		record.LastRunAt = fromUnixMS(runMS)
	}
	return record, true, nil
}

func unixMS(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().UnixMilli()
	}
	return t.UnixMilli()
}

func fromUnixMS(ms int64) time.Time { return time.UnixMilli(ms) }
