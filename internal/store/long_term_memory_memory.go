package store

import (
	"context"
	"sort"
)

// PutMemory 在同一把锁下校验所属会话与版本，校验全部通过才发布更新。
func (m *MemoryStore) PutMemory(ctx context.Context, write MemoryWrite) (MemoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateMemoryWrite(write); err != nil {
		return MemoryRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return MemoryRecord{}, err
	}
	if err := m.checkMemorySession(write.Scope); err != nil {
		return MemoryRecord{}, err
	}
	id := memoryIdentity{Scope: write.Scope, Key: write.Key}
	current, exists := m.memories[id]
	record, err := nextMemoryRecord(write, current, exists)
	if err != nil {
		return MemoryRecord{}, err
	}
	m.memories[id] = record
	return record, nil
}

// ListMemories 只读一个精确作用域；先计量完整集合，超限不返回前缀。
func (m *MemoryStore) ListMemories(ctx context.Context, scope MemoryScope, limits MemoryLimits) ([]MemoryRecord, error) {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkMemorySession(scope); err != nil {
		return nil, err
	}
	var size int64
	count := 0
	for id, record := range m.memories {
		if id.Scope == scope {
			count++
			size += memoryRecordSize(record)
		}
	}
	if count > limits.MaxRecords || size > int64(limits.MaxBytes) {
		return nil, ErrMemoryLimit
	}
	var out []MemoryRecord
	for id, record := range m.memories {
		if id.Scope != scope {
			continue
		}
		if err := validateStoredMemory(record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// checkMemorySession 要求调用方持锁，不能为私有记忆偷偷创建会话元数据。
func (m *MemoryStore) checkMemorySession(scope MemoryScope) error {
	if scope.Kind != MemorySession {
		return nil
	}
	session, ok := m.sessions[scope.SessionID]
	owner := Scope{ScopeKey: scope.ScopeKey, Workspace: scope.Workspace}
	if !ok || !scopeMatches(session.record, owner) {
		return ErrSessionNotFound
	}
	return m.checkBindingOwner(owner, scope.SessionID)
}
