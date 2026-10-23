package store

import (
	"context"
	"encoding/json"
	"sort"
)

var _ ManagementStore = (*MemoryStore)(nil)

// ListSessions 只枚举同域同工作区记录；空会话只有显式命名落盘后可见。
func (m *MemoryStore) ListSessions(ctx context.Context, scope Scope, page PageRequest) (SessionPage, error) {
	out := SessionPage{Page: page.Page, PageSize: page.PageSize}
	offset, err := pageOffset(page)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var records []SessionRecord
	for _, session := range m.sessions {
		if scopeMatches(session.record, scope) {
			records = append(records, m.sessionSummary(session))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if !records[i].LastRunAt.Equal(records[j].LastRunAt) {
			return records[i].LastRunAt.After(records[j].LastRunAt)
		}
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.After(records[j].CreatedAt)
		}
		return records[i].ID > records[j].ID
	})
	out.Total = len(records)
	if offset < len(records) {
		end := offset + min(page.PageSize, len(records)-offset)
		out.Sessions = records[offset:end]
	}
	var size int64
	for _, record := range out.Sessions {
		recordBytes, err := sessionPayloadSize(record)
		if err != nil {
			return SessionPage{}, err
		}
		size += recordBytes
	}
	if err := checkLoadSize(int64(len(out.Sessions)), size, normalizeLoadLimits(nil)); err != nil {
		return SessionPage{}, err
	}
	return out, nil
}

// ResolveSession 必须先筛可见集合再做前缀；精确 ID 兼容早期非 hex 测试。
func (m *MemoryStore) ResolveSession(ctx context.Context, scope Scope, input string) (SessionRecord, error) {
	if err := ctx.Err(); err != nil {
		return SessionRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.sessions[input]; ok && scopeMatches(session.record, scope) {
		if err := checkSessionPayload(session.record); err != nil {
			return SessionRecord{}, err
		}
		return m.sessionSummary(session), nil
	}
	var found *memorySession
	for id, session := range m.sessions {
		if scopeMatches(session.record, scope) && visiblePrefix(id, input) {
			if found != nil {
				return SessionRecord{}, ErrAmbiguousSessionID
			}
			found = session
		}
	}
	if found == nil {
		return SessionRecord{}, ErrSessionNotFound
	}
	if err := checkSessionPayload(found.record); err != nil {
		return SessionRecord{}, err
	}
	return m.sessionSummary(found), nil
}

func (m *MemoryStore) sessionSummary(session *memorySession) SessionRecord {
	record := session.record
	record.TurnCount = len(session.turns)
	for _, run := range m.runs {
		if run.SessionID == record.ID {
			record.RunCount++
		}
	}
	return record
}

// PageRuns 在锁内截取最新页并累计载荷上界；不能先解锁后读可变正文。
func (m *MemoryStore) PageRuns(ctx context.Context, scope Scope, sessionID string, page PageRequest) (RunPage, error) {
	out := RunPage{Page: page.Page, PageSize: page.PageSize}
	offset, err := pageOffset(page)
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok || !scopeMatches(session.record, scope) {
		return out, ErrSessionNotFound
	}
	var runs []*RunRecord
	for _, id := range m.runOrder {
		if run := m.runs[id]; run.SessionID == sessionID {
			runs = append(runs, run)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
	out.Total = len(runs)
	if offset >= len(runs) {
		return out, nil
	}
	end := len(runs) - offset
	start := max(0, end-page.PageSize)
	var size int64
	for _, run := range runs[start:end] {
		tools, err := json.Marshal(run.ToolEvents)
		if err != nil {
			return out, err
		}
		size += runPayloadSize(*run, tools)
		if err := checkLoadSize(int64(len(out.Runs)+1), size, normalizeLoadLimits(nil)); err != nil {
			return RunPage{}, err
		}
		if err := checkRunPayload(*run, tools); err != nil {
			return RunPage{}, err
		}
		copied := *run
		copied.ToolEvents = append([]ToolEventRecord(nil), run.ToolEvents...)
		out.Runs = append(out.Runs, copied)
	}
	return out, nil
}

// GetBinding 可返回尚未持久化的空会话 ID；调用方恢复时必须再校验 metadata。
func (m *MemoryStore) GetBinding(ctx context.Context, scope Scope) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.bindings[receiptKey(scope, "")]
	return id, ok, nil
}

// GetReceipt 返回独立载荷副本，重放不得把当前会话状态拼入旧结果。
func (m *MemoryStore) GetReceipt(ctx context.Context, scope Scope, requestID string) (Receipt, bool, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	receipt, ok := m.receipts[receiptKey(scope, requestID)]
	return copyReceipt(receipt), ok, nil
}

// ApplyCommand 先验证所有目标再发布任何状态，保证失败无半个内存事务。
func (m *MemoryStore) ApplyCommand(ctx context.Context, mutation CommandMutation) (Receipt, bool, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, false, err
	}
	mutation, err := normalizeMutation(mutation)
	if err != nil {
		return Receipt{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := mutation.Receipt
	scope := Scope{ScopeKey: r.ScopeKey, Workspace: r.Workspace}
	key := receiptKey(scope, r.RequestID)
	if stored, ok := m.receipts[key]; ok {
		if stored.Fingerprint != r.Fingerprint {
			return Receipt{}, false, ErrReceiptConflict
		}
		return copyReceipt(stored), true, nil
	}
	if mutation.CreateSession != nil {
		if existing, ok := m.sessions[mutation.CreateSession.ID]; ok && !scopeMatches(existing.record, scope) {
			return Receipt{}, false, ErrSessionNotFound
		}
		if err := m.checkBindingOwner(scope, mutation.CreateSession.ID); err != nil {
			return Receipt{}, false, err
		}
	}
	for _, id := range []string{mutation.BindSessionID, mutation.RenameSessionID} {
		if id == "" {
			continue
		}
		if existing, ok := m.sessions[id]; ok {
			if !scopeMatches(existing.record, scope) {
				return Receipt{}, false, ErrSessionNotFound
			}
		} else if id == mutation.RenameSessionID && (mutation.CreateSession == nil || mutation.CreateSession.ID != id) {
			return Receipt{}, false, ErrSessionNotFound
		}
		if err := m.checkBindingOwner(scope, id); err != nil {
			return Receipt{}, false, err
		}
	}
	if record := mutation.CreateSession; record != nil {
		if _, exists := m.sessions[record.ID]; !exists {
			m.sessions[record.ID] = &memorySession{record: *record}
		}
	}
	if mutation.RenameSessionID != "" {
		record := &m.sessions[mutation.RenameSessionID].record
		record.Title, record.TitleManual = mutation.Title, mutation.TitleManual
	}
	if mutation.BindSessionID != "" {
		m.bindings[receiptKey(scope, "")] = mutation.BindSessionID
	}
	m.receipts[key] = copyReceipt(r)
	return copyReceipt(r), false, nil
}

func (m *MemoryStore) checkBindingOwner(scope Scope, id string) error {
	key := receiptKey(scope, "")
	for owner, boundID := range m.bindings {
		if boundID == id && owner != key {
			return ErrSessionNotFound
		}
	}
	return nil
}

func runPayloadSize(run RunRecord, tools []byte) int64 {
	return int64(len(run.ID) + len(run.Input) + len(run.Status) + len(run.Error) + len(run.Reply) + len(run.Reasoning) + len(tools))
}

func checkRunPayload(run RunRecord, tools []byte) error {
	if !validRunStatus(run.Status) {
		return ErrCorruptHistory
	}
	return checkJSONExpansion(tools)
}

func sessionPayloadSize(record SessionRecord) (int64, error) {
	ref, err := json.Marshal(record.ModelRef)
	return int64(len(record.ID) + len(record.ScopeKey) + len(record.Workspace) + len(record.Title) + len(ref)), err
}

func checkSessionPayload(record SessionRecord) error {
	size, err := sessionPayloadSize(record)
	if err != nil {
		return err
	}
	return checkLoadSize(1, size, normalizeLoadLimits(nil))
}
