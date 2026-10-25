package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"plume-agent/internal/store"
)

// defaultCheckpointInterval 是流式正文/Thought 的合并写盘目标间隔：
// 高频增量只更新内存快照槽，间隔到期或关键边界才写事务；它不是断电
// 时"最多丢这么多"的保证。
const defaultCheckpointInterval = time.Second

// checkpointCoordinator 是一个 run 内的检查点协调器。模型事件、工具边界
// 与 finish 都在同一个 run goroutine 内串行到达，因此单槽 + 单调序号即可
// 满足"最新快照、有界、不按 token 开事务"，不需要后台队列或额外 goroutine。
// 持久化校验（run 归属、running 状态、序号单调、终态互斥）由存储事务守住，
// 这里只负责何时写与写入失败后的停止语义。
type checkpointCoordinator struct {
	history  store.Store
	runID    string
	interval time.Duration
	now      func() time.Time

	reply     string
	reasoning string
	tools     []store.CheckpointToolState
	sequence  int64
	lastSave  time.Time
	started   bool
}

// observeText / observeReasoning 追加流式增量到内存快照槽。
func (c *checkpointCoordinator) observeText(delta string)      { c.reply += delta }
func (c *checkpointCoordinator) observeReasoning(delta string) { c.reasoning += delta }

// noteTool 按 CallID 更新工具阶段；后继快照始终携带此前全部工具状态。
func (c *checkpointCoordinator) noteTool(state store.CheckpointToolState) {
	for i := range c.tools {
		if c.tools[i].CallID == state.CallID {
			c.tools[i] = state
			return
		}
	}
	c.tools = append(c.tools, state)
}

// payload 构造当前完整快照（复制工具切片，写入路径不再共享底层数组）。
func (c *checkpointCoordinator) payload() store.CheckpointPayload {
	return store.CheckpointPayload{Reply: c.reply, Reasoning: c.reasoning, Tools: append([]store.CheckpointToolState(nil), c.tools...)}
}

// hasContent 报告快照是否已有可保存内容；空 run 不制造空检查点。
func (c *checkpointCoordinator) hasContent() bool {
	return c.reply != "" || c.reasoning != "" || len(c.tools) != 0
}

// flush 尝试持久化当前快照。force 用于关键工具边界与收尾刷盘；非 force
// 在首个片段立即写一次，之后按合并间隔节流。写入错误不能当确定未提交：
// 除非存储明确判定序号/状态非法，先按同 run 同序号读回核查，载荷一致
// 视为迟到确认继续执行，否则返回错误由调用方停止后续生成与工具。
func (c *checkpointCoordinator) flush(ctx context.Context, force bool) error {
	if c == nil || c.history == nil {
		return nil
	}
	now := c.now()
	if !force && c.started && now.Sub(c.lastSave) < c.interval {
		return nil
	}
	payload := c.payload()
	c.sequence++
	sequence := c.sequence
	c.lastSave, c.started = now, true
	err := c.history.SaveCheckpoint(ctx, c.runID, sequence, payload, now)
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrCheckpointStale) || errors.Is(err, store.ErrCheckpointConflict) ||
		errors.Is(err, store.ErrCheckpointFinalized) || errors.Is(err, store.ErrUnknownRun) {
		return err
	}
	confirm, queryErr := c.history.LoadCheckpoint(ctx, c.runID)
	if queryErr == nil && confirm.Sequence == sequence {
		previous, prevErr := json.Marshal(confirm.Payload)
		current, currErr := json.Marshal(payload)
		if prevErr == nil && currErr == nil && string(previous) == string(current) {
			return nil // 迟到确认：事务已提交，不能当作失败中止任务。
		}
	}
	return err
}
