// Package app 承载会话与 run 的生命周期：消息历史、串行提交、取消，
// 以及把模型轮次结果转换为 UI 可消费的事件。它不渲染、不发渠道消息；
// 未来微信适配器复用同一 Service。
package app

import (
	"sync"

	"herald-agent/internal/model"
)

// Session 是一个进程内聊天会话：有序消息历史与单调递增的轮次号。
// 只追加完整提交的轮次；失败/取消的 run 不进入历史（0003/阶段计划 §2）。
type Session struct {
	mu       sync.Mutex
	messages []model.Message
	turnSeq  int
}

// NewSession 创建空会话。
func NewSession() *Session { return &Session{} }

// Append 提交一轮完整对话（用户输入 + assistant 回复）。只有成功结束的
// run 才应调用；历史追加是原子的，失败轮次不会留下半截上下文。
func (s *Session) Append(user, assistant model.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnSeq++
	s.messages = append(s.messages, user, assistant)
}

// History 返回历史的副本，调用方可安全修改。
func (s *Session) History() []model.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Message, len(s.messages))
	copy(out, s.messages)
	return out
}

// Turns 返回已提交的轮次数。
func (s *Session) Turns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnSeq
}

// Reset 清空上下文（Ctrl+N 新建会话的语义）。
func (s *Session) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.turnSeq = 0
}
