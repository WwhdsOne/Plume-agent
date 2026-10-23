package store

import (
	"context"
	"database/sql"
	"errors"
)

// 所有身份参数都绑定，不用拼接 SQL；精确全局范围也必须带主体过滤。
const memoryWhere = ` WHERE scope_key = ? AND scope_type = ? AND workspace = ? AND session_id = ?`
const memoryColumns = `memory_key, content, source, revision, created_at, updated_at`
const memorySizeSQL = `length(CAST(scope_key AS BLOB)) + length(CAST(scope_type AS BLOB)) +
	length(CAST(workspace AS BLOB)) + length(CAST(session_id AS BLOB)) +
	length(CAST(memory_key AS BLOB)) + length(CAST(content AS BLOB)) + length(CAST(source AS BLOB)) + 24`

// SQLite INTEGER 亲和性不保证磁盘实际类型；BLOB/文本/小数不能按8字节
// 计量后直接加载。先返回类型判定值，避免扫描大BLOB才发现损坏。
const memoryInvalidNumberSQL = `(typeof(revision) <> 'integer' OR typeof(created_at) <> 'integer' OR typeof(updated_at) <> 'integer')`

func memoryScopeArgs(scope MemoryScope) []any {
	return []any{scope.ScopeKey, string(scope.Kind), scope.Workspace, scope.SessionID}
}

func checkMemorySQLiteSession(ctx context.Context, tx *sql.Tx, scope MemoryScope) error {
	if scope.Kind != MemorySession {
		return nil
	}
	return checkSQLiteOwner(ctx, tx, Scope{ScopeKey: scope.ScopeKey, Workspace: scope.Workspace}, scope.SessionID, true)
}

// PutMemory 在同一事务内验证会话、已有条目与 CAS 版本。只有 Commit
// 成功才返回快照；错误隐藏驱动文本，调用方需读取核对不明提交结果。
func (s *SQLiteStore) PutMemory(ctx context.Context, write MemoryWrite) (MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateMemoryWrite(write); err != nil {
		return MemoryRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryRecord{}, memoryStorageError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkMemorySQLiteSession(ctx, tx, write.Scope); err != nil {
		return MemoryRecord{}, memoryStorageError(ctx, err)
	}
	args := append(memoryScopeArgs(write.Scope), write.Key)
	// 先检查现有单条大小再读取正文，不能信任外部修改过的历史库。
	var size int64
	var invalidNumbers bool
	err = tx.QueryRowContext(ctx, `SELECT `+memorySizeSQL+`, `+memoryInvalidNumberSQL+` FROM memory_entries`+memoryWhere+` AND memory_key = ?`, args...).Scan(&size, &invalidNumbers)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return MemoryRecord{}, memoryStorageError(ctx, err)
	}
	var current MemoryRecord
	if exists {
		if invalidNumbers {
			return MemoryRecord{}, ErrCorruptMemory
		}
		if size > memoryEntryBytes {
			return MemoryRecord{}, ErrMemoryLimit
		}
		current, err = scanMemory(ctx, write.Scope, tx.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memory_entries`+memoryWhere+` AND memory_key = ?`, args...).Scan)
		if err != nil {
			return MemoryRecord{}, err
		}
	}
	record, err := nextMemoryRecord(write, current, exists)
	if err != nil {
		return MemoryRecord{}, err
	}
	if !exists || record.Revision != current.Revision {
		_, err = tx.ExecContext(ctx, `INSERT INTO memory_entries
			(scope_key, scope_type, workspace, session_id, memory_key, content, source, revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(scope_key, scope_type, workspace, session_id, memory_key) DO UPDATE SET
			content=excluded.content, source=excluded.source, revision=excluded.revision, updated_at=excluded.updated_at`,
			write.Scope.ScopeKey, string(write.Scope.Kind), write.Scope.Workspace, write.Scope.SessionID,
			record.Key, record.Content, record.Source, record.Revision, unixMS(record.CreatedAt), unixMS(record.UpdatedAt))
		if err != nil {
			return MemoryRecord{}, memoryStorageError(ctx, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return MemoryRecord{}, memoryStorageError(ctx, err)
	}
	return record, nil
}

// ListMemories 将计量和正文读取固定在一个快照，中文按 UTF-8 字节计，
// 任何读取/校验/取消错误都丢弃整个结果，绝不把 LIMIT 前缀当完整记忆。
func (s *SQLiteStore) ListMemories(ctx context.Context, scope MemoryScope, limits MemoryLimits) ([]MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMemoryScope(scope); err != nil {
		return nil, err
	}
	limits, err := normalizeMemoryLimits(limits)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, memoryStorageError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkMemorySQLiteSession(ctx, tx, scope); err != nil {
		return nil, memoryStorageError(ctx, err)
	}
	var count, size, invalidNumbers, largest int64
	args := memoryScopeArgs(scope)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(`+memorySizeSQL+`),0),
		COALESCE(SUM(`+memoryInvalidNumberSQL+`),0), COALESCE(MAX(`+memorySizeSQL+`),0)
		FROM memory_entries`+memoryWhere, args...).Scan(&count, &size, &invalidNumbers, &largest); err != nil {
		return nil, memoryStorageError(ctx, err)
	}
	if invalidNumbers != 0 {
		return nil, ErrCorruptMemory
	}
	if count > int64(limits.MaxRecords) || size > int64(limits.MaxBytes) || largest > memoryEntryBytes {
		return nil, ErrMemoryLimit
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_entries`+memoryWhere+` ORDER BY memory_key COLLATE BINARY`, args...)
	if err != nil {
		return nil, memoryStorageError(ctx, err)
	}
	defer rows.Close()
	var out []MemoryRecord
	for rows.Next() {
		record, err := scanMemory(ctx, scope, rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, memoryStorageError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func scanMemory(ctx context.Context, scope MemoryScope, scan func(...any) error) (MemoryRecord, error) {
	record := MemoryRecord{Scope: scope}
	var created, updated int64
	if err := scan(&record.Key, &record.Content, &record.Source, &record.Revision, &created, &updated); err != nil {
		return MemoryRecord{}, memoryStorageError(ctx, err)
	}
	// 账本契约固定 UTC，不能沿用历史记录的本地时区表示，否则首次
	// 写入与重投读回的快照不一致（TestLongTermMemoryVersions）。
	record.CreatedAt, record.UpdatedAt = fromUnixMS(created).UTC(), fromUnixMS(updated).UTC()
	if err := validateStoredMemory(record); err != nil {
		return MemoryRecord{}, err
	}
	return record, nil
}
