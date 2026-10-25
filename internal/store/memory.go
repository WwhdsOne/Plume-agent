package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"plume-agent/internal/model"
)

// MemoryStore 是进程内实现，供 offline 演示、sessions.history.enabled=false
// 与契约测试使用。语义与 SQLite 后端逐条对齐；退出即丢弃，不落盘。
type MemoryStore struct {
	mu          sync.Mutex
	sessions    map[string]*memorySession
	runs        map[string]*RunRecord
	calls       map[string]*ModelCallRecord
	callOrder   []string // 调用起点插入序，LoadCalls 按此稳定输出
	runOrder    []string // run 起点插入序，解决串行调用也可能同毫秒的 tie-break。
	bindings    map[string]string
	receipts    map[string]Receipt
	memories    map[memoryIdentity]MemoryRecord // 长期记忆契约的易失后端，不自动学习历史。
	checkpoints map[string]*CheckpointRecord    // 运行检查点易失后端；终态清理与 SQLite 同规则。
}

type memorySession struct {
	record     SessionRecord
	turns      [][]model.Message
	turnRunIDs []string // 与 turns 同索引，供提交结果确认与关联校验。
	turnSeq    int
}

// NewMemory 构造内存后端。
func NewMemory() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]*memorySession), runs: make(map[string]*RunRecord), calls: make(map[string]*ModelCallRecord), bindings: make(map[string]string), receipts: make(map[string]Receipt), memories: make(map[memoryIdentity]MemoryRecord), checkpoints: make(map[string]*CheckpointRecord)}
}

// StartRun 幂等创建会话并登记起点；活动时间取起点与现存值的较大者。
func (m *MemoryStore) StartRun(_ context.Context, session SessionRecord, run RunRecord) (err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[run.ID]; ok {
		return ErrDuplicateRun
	}
	session = normalizeSession(session)
	scope := Scope{ScopeKey: session.ScopeKey, Workspace: session.Workspace}
	if err := m.checkBindingOwner(scope, session.ID); err != nil {
		return err
	}
	if run.Status != RunRunning {
		run.Status = RunRunning
	}
	s, ok := m.sessions[session.ID]
	if ok && !scopeMatches(s.record, scope) {
		return ErrSessionNotFound
	}
	if !ok {
		if session.CreatedAt.IsZero() {
			session.CreatedAt = run.StartedAt
		}
		s = &memorySession{record: session}
		m.sessions[session.ID] = s
	}
	if run.StartedAt.After(s.record.LastRunAt) {
		s.record.LastRunAt = run.StartedAt
	}
	run.SessionID = session.ID
	m.runs[run.ID] = &run
	m.runOrder = append(m.runOrder, run.ID)
	// 起点与当前绑定在同一把锁下发布，起点失败不会切走原绑定。
	m.bindings[receiptKey(scope, "")] = session.ID
	return nil
}

// StartModelCall 幂等登记调用起点。
func (m *MemoryStore) StartModelCall(_ context.Context, call ModelCallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.calls[call.ID]; ok {
		return nil
	}
	if call.Usage != nil {
		usage := *call.Usage
		call.Usage = &usage
	}
	m.calls[call.ID] = &call
	m.callOrder = append(m.callOrder, call.ID)
	return nil
}

// SaveUsage 按 ID 替换快照；未登记的调用拒绝补记。
func (m *MemoryStore) SaveUsage(_ context.Context, callID string, usage StoredUsage, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	call, ok := m.calls[callID]
	if !ok {
		return ErrUnknownCall
	}
	stored := usage
	call.Usage = &stored
	call.SavedAt = at
	return nil
}

// CompleteRun 在锁内一次提交轮次与终态，保证无半组消息可见。
func (m *MemoryStore) CompleteRun(_ context.Context, run RunRecord, turn []model.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.runs[run.ID]
	if !ok {
		return ErrUnknownRun
	}
	if run.SessionID != existing.SessionID {
		return fmt.Errorf("%w: run session mismatch", ErrCorruptHistory)
	}
	if existing.Status != RunRunning {
		return nil // 幂等：重复提交同一终态 run 保留第一次结果。
	}
	s, ok := m.sessions[run.SessionID]
	if !ok {
		return ErrUnknownCall
	}
	if _, err := encodeTurn(turn); err != nil {
		return err
	}
	if turn[0].Content != existing.Input {
		return fmt.Errorf("%w: turn input mismatch", ErrCorruptHistory)
	}
	s.turnSeq++
	s.turns = append(s.turns, copyMessages(turn))
	s.turnRunIDs = append(s.turnRunIDs, run.ID)
	m.markFinished(existing, run, RunCompleted)
	if run.FinishedAt.After(s.record.LastRunAt) {
		s.record.LastRunAt = run.FinishedAt
	}
	return nil
}

// GetRunState 在同一把锁内确认终态、完整轮次与检查点清理，不能把损坏
// completed 当成已保存；interrupted 按崩溃恢复契约允许保留检查点。
func (m *MemoryStore) GetRunState(_ context.Context, runID string) (RunState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return RunState{}, ErrUnknownRun
	}
	state := RunState{SessionID: run.SessionID, Status: run.Status}
	if !validRunStatus(state.Status) {
		return state, fmt.Errorf("%w: invalid run status", ErrCorruptHistory)
	}
	if state.Status != RunRunning && state.Status != RunInterrupted {
		if _, hasCheckpoint := m.checkpoints[runID]; hasCheckpoint {
			return state, fmt.Errorf("%w: terminal run retains checkpoint", ErrCorruptHistory)
		}
	}
	if s, ok := m.sessions[run.SessionID]; ok {
		for i, id := range s.turnRunIDs {
			if id != runID {
				continue
			}
			if state.HasTurn || i >= len(s.turns) {
				return state, fmt.Errorf("%w: duplicate or missing run turn", ErrCorruptHistory)
			}
			encoded, err := encodeTurn(s.turns[i])
			if err != nil {
				return state, err
			}
			if err := checkLoadSize(1, int64(len(encoded)), normalizeLoadLimits(nil)); err != nil {
				return state, err
			}
			if s.turns[i][0].Content != run.Input {
				return state, fmt.Errorf("%w: turn input mismatch", ErrCorruptHistory)
			}
			state.HasTurn = true
		}
	}
	if (state.Status == RunCompleted) != state.HasTurn {
		return state, fmt.Errorf("%w: run outcome and turn disagree", ErrCorruptHistory)
	}
	return state, nil
}

// FinishRun 提交非成功终态；已完成事务不受影响。
func (m *MemoryStore) FinishRun(_ context.Context, run RunRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.runs[run.ID]
	if !ok {
		return ErrUnknownCall
	}
	if existing.Status != RunRunning {
		return nil
	}
	m.markFinished(existing, run, run.Status)
	if s, ok := m.sessions[run.SessionID]; ok && run.FinishedAt.After(s.record.LastRunAt) {
		s.record.LastRunAt = run.FinishedAt
	}
	return nil
}

func (m *MemoryStore) markFinished(existing *RunRecord, run RunRecord, status RunStatus) {
	existing.Status = status
	existing.Error = run.Error
	existing.Reply = run.Reply
	existing.Reasoning = run.Reasoning
	existing.ToolEvents = run.ToolEvents
	existing.FinishedAt = run.FinishedAt
	if existing.FinishedAt.IsZero() {
		existing.FinishedAt = time.Now()
	}
	// 终态与检查点清理在同一把锁内生效，与 SQLite 终态事务对齐。
	delete(m.checkpoints, run.ID)
}

// SaveCheckpoint 按同一契约保存易失快照：序号单调、同序号幂等、终态拒绝。
func (m *MemoryStore) SaveCheckpoint(_ context.Context, runID string, sequence int64, payload CheckpointPayload, savedAt time.Time) error {
	encoded, err := validateCheckpoint(sequence, payload)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return ErrUnknownRun
	}
	if run.Status != RunRunning {
		return ErrCheckpointFinalized
	}
	if existing, ok := m.checkpoints[runID]; ok {
		switch {
		case existing.Sequence > sequence:
			return ErrCheckpointStale
		case existing.Sequence == sequence:
			previous, err := json.Marshal(existing.Payload)
			if err != nil {
				return err
			}
			if string(previous) != encoded {
				return ErrCheckpointConflict
			}
			return nil // 幂等：同序号同载荷的重放。
		}
	}
	tools := append([]CheckpointToolState(nil), payload.Tools...)
	m.checkpoints[runID] = &CheckpointRecord{RunID: runID, SessionID: run.SessionID, Sequence: sequence,
		FormatVersion: CheckpointFormatVersion, Payload: CheckpointPayload{Reply: payload.Reply, Reasoning: payload.Reasoning, Tools: tools}, SavedAt: savedAt}
	return nil
}

// LoadCheckpoint 返回 run 的最新易失检查点；不存在时 ErrCheckpointNotFound。
func (m *MemoryStore) LoadCheckpoint(_ context.Context, runID string) (CheckpointRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return CheckpointRecord{}, ErrUnknownRun
	}
	existing, ok := m.checkpoints[runID]
	if !ok {
		return CheckpointRecord{}, ErrCheckpointNotFound
	}
	record := *existing
	record.Payload.Tools = append([]CheckpointToolState(nil), existing.Payload.Tools...)
	if record.SessionID == "" {
		record.SessionID = run.SessionID
	}
	return record, nil
}

// LatestCheckpoint 返回会话最新 run 的检查点；插入序即起点序（runOrder）。
func (m *MemoryStore) LatestCheckpoint(_ context.Context, sessionID string) (CheckpointRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.runOrder) - 1; i >= 0; i-- {
		runID := m.runOrder[i]
		if run := m.runs[runID]; run.SessionID != sessionID {
			continue
		}
		if existing, ok := m.checkpoints[runID]; ok {
			record := *existing
			record.Payload.Tools = append([]CheckpointToolState(nil), existing.Payload.Tools...)
			return record, true, nil
		}
		return CheckpointRecord{}, false, nil
	}
	return CheckpointRecord{}, false, nil
}

// InterruptRunning 把内存中的 running 标记 interrupted。
func (m *MemoryStore) InterruptRunning(_ context.Context, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, run := range m.runs {
		if run.Status == RunRunning {
			run.Status = RunInterrupted
			run.FinishedAt = at
			count++
		}
	}
	return count, nil
}

// LatestSession 返回工作区最近有 run 的会话。
func (m *MemoryStore) LatestSession(_ context.Context, workspace string) (SessionRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *SessionRecord
	for i := len(m.runOrder) - 1; i >= 0; i-- {
		s := m.sessions[m.runs[m.runOrder[i]].SessionID]
		if s.record.Workspace != workspace || normalizedScope(s.record.ScopeKey) != "local" {
			continue
		}
		if best == nil || s.record.LastRunAt.After(best.LastRunAt) {
			record := s.record
			best = &record
		}
	}
	if best == nil {
		return SessionRecord{}, false, nil
	}
	return *best, true, nil
}

// LoadTurns 返回按序拼接的全部成功轮次消息。
func (m *MemoryStore) LoadTurns(_ context.Context, sessionID string, limits ...LoadLimits) ([]model.Message, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, 1, nil
	}
	var messages []model.Message
	completed := 0
	for _, run := range m.runs {
		if run.SessionID == sessionID && run.Status == RunCompleted {
			completed++
		}
	}
	if completed != len(s.turns) || s.turnSeq != len(s.turns) {
		return nil, 0, fmt.Errorf("%w: completed run without turn", ErrCorruptHistory)
	}
	limit := normalizeLoadLimits(limits)
	var size int64
	seen := make(map[string]bool)
	seenRuns := make(map[string]bool)
	for i, turn := range s.turns {
		encoded, err := encodeTurn(turn)
		if err != nil {
			return nil, 0, err
		}
		size += int64(len(encoded))
		if err := checkLoadSize(int64(i+1), size, limit); err != nil {
			return nil, 0, err
		}
		if err := validateTurn(turn, seen); err != nil {
			return nil, 0, err
		}
		if i >= len(s.turnRunIDs) {
			return nil, 0, fmt.Errorf("%w: missing turn run", ErrCorruptHistory)
		}
		run, ok := m.runs[s.turnRunIDs[i]]
		if !ok || seenRuns[run.ID] || run.Status != RunCompleted || run.SessionID != sessionID || run.Input != turn[0].Content {
			return nil, 0, fmt.Errorf("%w: invalid turn run association", ErrCorruptHistory)
		}
		seenRuns[run.ID] = true
		messages = append(messages, copyMessages(turn)...)
	}
	return messages, s.turnSeq + 1, nil
}

// ListRuns 按起点时间返回会话全部 run 记录，时间相同保留插入序。
func (m *MemoryStore) ListRuns(_ context.Context, sessionID string, limits ...LoadLimits) ([]RunRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []RunRecord
	limit := normalizeLoadLimits(limits)
	var size int64
	for _, id := range m.runOrder {
		run := m.runs[id]
		if run.SessionID != sessionID {
			continue
		}
		if !validRunStatus(run.Status) {
			return nil, fmt.Errorf("%w: invalid run status", ErrCorruptHistory)
		}
		tools, err := json.Marshal(run.ToolEvents)
		if err != nil {
			return nil, err
		}
		size += int64(len(run.ID) + len(run.Input) + len(run.Status) + len(run.Error) + len(run.Reply) + len(run.Reasoning) + len(tools))
		if err := checkLoadSize(int64(len(out)+1), size, limit); err != nil {
			return nil, err
		}
		copied := *run
		copied.ToolEvents = append([]ToolEventRecord(nil), run.ToolEvents...)
		out = append(out, copied)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

// LoadCalls 按插入序返回会话的全部调用记录。
func (m *MemoryStore) LoadCalls(_ context.Context, sessionID string, limits ...LoadLimits) ([]ModelCallRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ModelCallRecord
	limit := normalizeLoadLimits(limits)
	var size int64
	for _, id := range m.callOrder {
		call := m.calls[id]
		if call.SessionID != sessionID {
			continue
		}
		usage, err := json.Marshal(call.Usage)
		if err != nil {
			return nil, err
		}
		size += int64(len(call.ID) + len(call.RunID) + len(usage))
		if err := checkLoadSize(int64(len(out)+1), size, limit); err != nil {
			return nil, err
		}
		copied := *call
		if call.Usage != nil {
			usage := *call.Usage
			copied.Usage = &usage
		}
		out = append(out, copied)
	}
	return out, nil
}

// Close 是空操作，保持契约完整。
func (m *MemoryStore) Close() error { return nil }

func copyMessages(messages []model.Message) []model.Message {
	out := append([]model.Message(nil), messages...)
	for i := range out {
		out[i].ToolCalls = append([]model.ToolCall(nil), out[i].ToolCalls...)
	}
	return out
}
