package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var _ ManagementStore = (*SQLiteStore)(nil)

// 固定 SQL 片段只由包内拼接；所有身份、前缀与分页参数均经绑定传值。
const sessionSelect = `SELECT id, scope_key, workspace, title, title_manual, model_ref, created_at, last_run_at,
	(SELECT COUNT(*) FROM runs WHERE runs.session_id = sessions.id),
	(SELECT COUNT(*) FROM turns WHERE turns.session_id = sessions.id) FROM sessions `

const sessionSize = `length(CAST(id AS BLOB)) + length(CAST(scope_key AS BLOB)) + length(CAST(workspace AS BLOB)) + length(CAST(title AS BLOB)) + length(CAST(model_ref AS BLOB))`

const runSize = `length(CAST(id AS BLOB)) + length(CAST(input AS BLOB)) + length(CAST(status AS BLOB)) + length(CAST(error AS BLOB)) + length(CAST(reply AS BLOB)) + length(CAST(reasoning AS BLOB)) + length(CAST(tool_events AS BLOB))`

// ListSessions 在同一读事务内计数、预检页载荷并枚举目录，防止检测后
// 数据变化；目录只使用当前域，不把其他身份的总数或 ID 交给调用方。
func (s *SQLiteStore) ListSessions(ctx context.Context, scope Scope, page PageRequest) (SessionPage, error) {
	out := SessionPage{Page: page.Page, PageSize: page.PageSize}
	offset, err := pageOffset(page)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	key := normalizedScope(scope.ScopeKey)
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE scope_key = ? AND workspace = ?", key, scope.Workspace).Scan(&out.Total); err != nil {
		return out, err
	}
	pageSQL := ` FROM sessions WHERE scope_key = ? AND workspace = ? ORDER BY last_run_at DESC, created_at DESC, id DESC LIMIT ? OFFSET ?`
	var count, size int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM (SELECT `+sessionSize+` AS size`+pageSQL+`)`, key, scope.Workspace, page.PageSize, offset).Scan(&count, &size); err != nil {
		return out, err
	}
	if err := checkLoadSize(count, size, normalizeLoadLimits(nil)); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, sessionSelect+` WHERE scope_key = ? AND workspace = ? ORDER BY last_run_at DESC, created_at DESC, id DESC LIMIT ? OFFSET ?`, key, scope.Workspace, page.PageSize, offset)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		record, _, err := scanSession(rows.Scan)
		if err != nil {
			return SessionPage{}, err
		}
		out.Sessions = append(out.Sessions, record)
	}
	return out, rows.Err()
}

// ResolveSession 优先精确 ID，再在已过滤可见域内解析至少八位前缀；
// 不使用 LIKE，前缀中的 %/_ 不能扩大结果集合。
func (s *SQLiteStore) ResolveSession(ctx context.Context, scope Scope, input string) (SessionRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()
	key := normalizedScope(scope.ScopeKey)
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE scope_key = ? AND workspace = ? AND id = ?`, key, scope.Workspace, input).Scan(&id); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return SessionRecord{}, err
		}
		if len(input) < 8 || len(input) > 32 {
			return SessionRecord{}, ErrSessionNotFound
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM sessions WHERE scope_key = ? AND workspace = ? AND substr(id, 1, length(?)) = ? LIMIT 2`, key, scope.Workspace, input, input)
		if err != nil {
			return SessionRecord{}, err
		}
		count := 0
		for rows.Next() {
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return SessionRecord{}, err
			}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return SessionRecord{}, err
		}
		if count == 0 {
			return SessionRecord{}, ErrSessionNotFound
		}
		if count > 1 {
			return SessionRecord{}, ErrAmbiguousSessionID
		}
	}
	var size int64
	if err := tx.QueryRowContext(ctx, `SELECT `+sessionSize+` FROM sessions WHERE scope_key = ? AND workspace = ? AND id = ?`, key, scope.Workspace, id).Scan(&size); err != nil {
		return SessionRecord{}, err
	}
	if err := checkLoadSize(1, size, normalizeLoadLimits(nil)); err != nil {
		return SessionRecord{}, err
	}
	record, _, err := scanSession(tx.QueryRowContext(ctx, sessionSelect+` WHERE scope_key = ? AND workspace = ? AND id = ?`, key, scope.Workspace, id).Scan)
	return record, err
}

// PageRuns 只取一页最近 run，读取前先统计该页 UTF-8 字节；外层再
// 按发生顺序排列，避免为翻页加载整段会话或打乱失败/取消记录。
func (s *SQLiteStore) PageRuns(ctx context.Context, scope Scope, sessionID string, page PageRequest) (RunPage, error) {
	out := RunPage{Page: page.Page, PageSize: page.PageSize}
	offset, err := pageOffset(page)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkSQLiteOwner(ctx, tx, scope, sessionID, true); err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE session_id = ?", sessionID).Scan(&out.Total); err != nil {
		return out, err
	}
	var count, size int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM (SELECT `+runSize+` AS size FROM runs WHERE session_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ? OFFSET ?)`, sessionID, page.PageSize, offset).Scan(&count, &size); err != nil {
		return out, err
	}
	if err := checkLoadSize(count, size, normalizeLoadLimits(nil)); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, input, status, error, started_at, finished_at, reply, reasoning, tool_events FROM (
		SELECT rowid AS run_order, * FROM runs WHERE session_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ? OFFSET ?
	) ORDER BY started_at, run_order`, sessionID, page.PageSize, offset)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var run RunRecord
		var started int64
		var finished sql.NullInt64
		var tools string
		if err := rows.Scan(&run.ID, &run.Input, &run.Status, &run.Error, &started, &finished, &run.Reply, &run.Reasoning, &tools); err != nil {
			return RunPage{}, err
		}
		if err := checkRunPayload(run, []byte(tools)); err != nil {
			return RunPage{}, err
		}
		if tools != "" {
			if err := json.Unmarshal([]byte(tools), &run.ToolEvents); err != nil {
				return RunPage{}, fmt.Errorf("%w: invalid tool events: %v", ErrCorruptHistory, err)
			}
		}
		run.SessionID, run.StartedAt = sessionID, fromUnixMS(started)
		if finished.Valid {
			run.FinishedAt = fromUnixMS(finished.Int64)
		}
		out.Runs = append(out.Runs, run)
	}
	return out, rows.Err()
}

// GetBinding 仅返回该域当前绑定；尚无 metadata 的空会话由 app 决定恢复策略。
func (s *SQLiteStore) GetBinding(ctx context.Context, scope Scope) (string, bool, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT session_id FROM bindings WHERE scope_key = ? AND workspace = ?", normalizedScope(scope.ScopeKey), scope.Workspace).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// GetReceipt 在同一快照中先预检大小再读载荷，历史文件中的恶意超大
// JSON 不能绕过写入端边界；正常只读指令也使用同一收据命名空间。
func (s *SQLiteStore) GetReceipt(ctx context.Context, scope Scope, requestID string) (Receipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	return loadSQLiteReceipt(ctx, tx, scope, requestID)
}

func loadSQLiteReceipt(ctx context.Context, tx *sql.Tx, scope Scope, requestID string) (Receipt, bool, error) {
	key := normalizedScope(scope.ScopeKey)
	var size int64
	err := tx.QueryRowContext(ctx, `SELECT length(CAST(payload AS BLOB)) FROM command_receipts WHERE scope_key = ? AND workspace = ? AND request_id = ?`, key, scope.Workspace, requestID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	if err := checkLoadSize(1, size, normalizeLoadLimits(nil)); err != nil {
		return Receipt{}, false, err
	}
	r := Receipt{ScopeKey: key, Workspace: scope.Workspace, RequestID: requestID}
	var payload string
	var at int64
	if err := tx.QueryRowContext(ctx, `SELECT fingerprint, command, payload, created_at FROM command_receipts WHERE scope_key = ? AND workspace = ? AND request_id = ?`, key, scope.Workspace, requestID).Scan(&r.Fingerprint, &r.Command, &payload, &at); err != nil {
		return Receipt{}, false, err
	}
	r.Payload, r.CreatedAt = json.RawMessage(payload), fromUnixMS(at)
	if !json.Valid(r.Payload) {
		return Receipt{}, false, ErrCorruptHistory
	}
	if err := checkJSONExpansion(r.Payload); err != nil {
		return Receipt{}, false, err
	}
	return r, true, nil
}

// ApplyCommand 在同一个写事务内查重、验证权限并发布变更+收据。数据库
// 只保留一个连接，所以并发请求在事务入口串行，唯一键仍作为持久化兜底。
func (s *SQLiteStore) ApplyCommand(ctx context.Context, mutation CommandMutation) (Receipt, bool, error) {
	mutation, err := normalizeMutation(mutation)
	if err != nil {
		return Receipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	r := mutation.Receipt
	scope := Scope{ScopeKey: r.ScopeKey, Workspace: r.Workspace}
	stored, found, err := loadSQLiteReceipt(ctx, tx, scope, r.RequestID)
	if err != nil {
		return Receipt{}, false, err
	}
	if found {
		if stored.Fingerprint != r.Fingerprint {
			return Receipt{}, false, ErrReceiptConflict
		}
		return stored, true, nil
	}
	if record := mutation.CreateSession; record != nil {
		if err := checkSQLiteOwner(ctx, tx, scope, record.ID, false); err != nil {
			return Receipt{}, false, err
		}
		ref, err := json.Marshal(record.ModelRef)
		if err != nil {
			return Receipt{}, false, err
		}
		var lastRun int64
		if !record.LastRunAt.IsZero() {
			lastRun = record.LastRunAt.UnixMilli()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,scope_key,workspace,title,title_manual,model_ref,created_at,last_run_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, record.ID, record.ScopeKey, record.Workspace, record.Title, record.TitleManual, string(ref), unixMS(record.CreatedAt), lastRun); err != nil {
			return Receipt{}, false, err
		}
	}
	if mutation.RenameSessionID != "" {
		if err := checkSQLiteOwner(ctx, tx, scope, mutation.RenameSessionID, true); err != nil {
			return Receipt{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET title = ?, title_manual = ? WHERE id = ? AND scope_key = ? AND workspace = ?`, mutation.Title, mutation.TitleManual, mutation.RenameSessionID, r.ScopeKey, r.Workspace); err != nil {
			return Receipt{}, false, err
		}
	}
	if mutation.BindSessionID != "" {
		if err := checkSQLiteOwner(ctx, tx, scope, mutation.BindSessionID, false); err != nil {
			return Receipt{}, false, err
		}
		if err := bindSQLiteSession(ctx, tx, scope, mutation.BindSessionID); err != nil {
			return Receipt{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO command_receipts(scope_key,workspace,request_id,fingerprint,command,payload,created_at) VALUES(?,?,?,?,?,?,?)`, r.ScopeKey, r.Workspace, r.RequestID, r.Fingerprint, r.Command, string(r.Payload), unixMS(r.CreatedAt)); err != nil {
		return Receipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, false, err
	}
	return r, false, nil
}

// checkSQLiteOwner 既检查已有会话也检查尚未落盘空会话的绑定；
// 其他权限域不能抢占一个已绑定但尚无 metadata 的随机 ID。
func checkSQLiteOwner(ctx context.Context, tx *sql.Tx, scope Scope, id string, required bool) error {
	var owner Scope
	err := tx.QueryRowContext(ctx, "SELECT scope_key, workspace FROM sessions WHERE id = ?", id).Scan(&owner.ScopeKey, &owner.Workspace)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if required {
			return ErrSessionNotFound
		}
	} else if owner.ScopeKey != normalizedScope(scope.ScopeKey) || owner.Workspace != scope.Workspace {
		return ErrSessionNotFound
	}
	var foreignBindings int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bindings WHERE session_id = ? AND (scope_key != ? OR workspace != ?)", id, normalizedScope(scope.ScopeKey), scope.Workspace).Scan(&foreignBindings); err != nil {
		return err
	}
	if foreignBindings != 0 {
		return ErrSessionNotFound
	}
	return nil
}

func bindSQLiteSession(ctx context.Context, tx *sql.Tx, scope Scope, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO bindings(scope_key,workspace,session_id) VALUES(?,?,?) ON CONFLICT(scope_key,workspace) DO UPDATE SET session_id = excluded.session_id`, normalizedScope(scope.ScopeKey), scope.Workspace, id)
	return err
}
