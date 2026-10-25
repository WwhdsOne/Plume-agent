package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"plume-agent/internal/model"
	"plume-agent/internal/store"
)

// 会话与 run 的 ID 都是随机生成的稳定完整值；显示层可截短前缀，
// 持久层只存完整值，不用进程内计数当数据库身份（重启会重复）。

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败不可恢复（系统熵源故障）；宁可启动失败也不退化成
		// 可预测 ID。
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(buf)
}

// newSessionID 生成 32 位十六进制会话 ID；短前缀唯一性由显示层保证。
func newSessionID() string { return randomHex(16) }

// newRunID 生成跨进程唯一的 run ID；call ID 仍为 runID/model-N。
func newRunID() string { return "run-" + randomHex(8) }

// defaultShutdownTimeout 是终态保存与资源关闭的收尾期限默认值。
const defaultShutdownTimeout = 5000 * time.Millisecond

// RestoreSession 从持久层记录重建内存会话：成功轮次消息、轮次号、用量
// 快照与起始时间。失败/取消记录不进入模型历史（模型历史只含成功 turns），
// 用量按已保存 call ID 聚合，未知调用保持未知。
func RestoreSession(messages []model.Message, turnSeq int, usage map[string]model.Usage, lastCall string, startedAt time.Time) *Session {
	if usage == nil {
		usage = make(map[string]model.Usage)
	}
	for id, u := range usage {
		usage[id] = cloneUsage(u)
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Session{messages: messages, turnSeq: turnSeq, startedAt: startedAt, usage: usage, lastCall: lastCall}
}

// EnablePersistence 绑定持久层与工作区上下文：此后每个 run 的起点、用量
// 与终态先落盘再进入内存。workspace 是工具工作目录的规范绝对路径；
// shutdownTimeout 限制取消后的收尾保存，零值取默认 5000ms。
// offline 与 sessions.history.enabled=false 的进程不调用本方法。
func (s *Service) EnablePersistence(history store.Store, workspace string, ref store.ModelRef, shutdownTimeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if history == nil {
		return
	}
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	s.history = history
	s.workspace = workspace
	s.modelRef = ref
	s.shutdownTimeout.Store(int64(shutdownTimeout))
}

// AdoptSession 接管一个从持久层恢复的会话（`-c` 路径）：替换内存会话与
// 会话 ID，恢复的用量与轮次直接参与后续统计与提交。
func (s *Service) AdoptSession(id string, session *Session, createdAt ...time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.isClosing() {
		return fmt.Errorf("service is closed")
	}
	if s.busy {
		return ErrBusy
	}
	if s.historyUncertain {
		return ErrHistoryUncertain
	}
	if id == "" || session == nil {
		return fmt.Errorf("invalid resumed session")
	}
	if err := s.runtime.ResetSessionState(); err != nil {
		return err
	}
	s.session = session
	s.sessionID = id
	s.sessionCreatedAt = session.startedAt
	s.recordExitSession()
	if len(createdAt) > 0 && !createdAt[0].IsZero() {
		s.sessionCreatedAt = createdAt[0]
	}
	return nil
}

// SessionID 返回当前会话 ID。
func (s *Service) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// NewSession 建立新会话：新的随机 ID 与空内存历史，旧会话保留在持久层，
// 不会被新会话复活（Ctrl+N 的 G4a.1 语义）。busy 或已关闭时拒绝。
func (s *Service) NewSession() error {
	// G4a.2 装配后键位与 /new 共用事务和启动模型；旧入口保持兼容。
	s.mu.Lock()
	configured := s.commands != nil
	s.mu.Unlock()
	if configured {
		result := s.HandleInput(context.Background(), CommandRequest{Text: "/new", Entrance: EntranceTUI})
		if result.Code != "ok" {
			return fmt.Errorf("%s: %s", result.Code, result.Message)
		}
		return nil
	}
	if s.isClosing() {
		return fmt.Errorf("service is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.isClosing() {
		return fmt.Errorf("service is closed")
	}
	if s.busy {
		return ErrBusy
	}
	if s.historyUncertain {
		return ErrHistoryUncertain
	}
	// 清摘要必须先于会话替换；关闭句柄等失败时完整保留原 ID 和历史。
	if err := s.runtime.ResetSessionState(); err != nil {
		return err
	}
	s.session = NewSession()
	s.sessionID = newSessionID()
	s.sessionCreatedAt = time.Now()
	return nil
}

// sessionRecordLocked 构造当前会话的落盘记录；调用方须持有 s.mu。
// 标题取公共指令状态；未手工命名且没有 run 的默认空会话不落盘。
func (s *Service) sessionRecordLocked(now time.Time) store.SessionRecord {
	createdAt := s.sessionCreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	record := store.SessionRecord{
		ID:        s.sessionID,
		Workspace: s.workspace,
		Title:     "New conversation",
		ModelRef:  s.modelRef,
		CreatedAt: createdAt,
		LastRunAt: now,
	}
	if s.commands != nil {
		record.ScopeKey = s.commands.key
		record.Title, record.TitleManual = s.commands.title, s.commands.manualTitle
	}
	return record
}
