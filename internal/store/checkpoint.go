package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 检查点写入与读取的固定错误语义。写入失败后调用方按"提交未知"处理：
// 不能把超时当确定未提交，必须用 LoadCheckpoint 按序号核查。
var (
	// ErrCheckpointStale 表示迟到快照的序号小于已保存序号；不能覆盖较新快照。
	ErrCheckpointStale = errors.New("checkpoint sequence is stale")
	// ErrCheckpointConflict 表示同序号携带不同载荷；不是幂等重放，必须拒绝。
	ErrCheckpointConflict = errors.New("checkpoint sequence conflicts with stored payload")
	// ErrCheckpointFinalized 表示 run 已到终态；迟到写入不能复活检查点。
	ErrCheckpointFinalized = errors.New("run already finalized")
	// ErrCheckpointNotFound 表示目标 run 没有已保存的检查点。
	ErrCheckpointNotFound = errors.New("checkpoint not found")
)

// CheckpointFormatVersion 是检查点载荷 DTO 的当前版本；读取遇到更大的
// 版本必须拒绝，不能按当前格式猜解未来字段。
const CheckpointFormatVersion = 1

// 检查点载荷边界：单条快照 UTF-8 JSON 总量与工具条目上限。超限整次拒绝，
// 不截断冒充完整记录；与恢复读取边界（LoadLimits）分开治理。
const (
	CheckpointMaxBytes = 8 << 20
	CheckpointMaxTools = 10000
)

// CheckpointToolState 是快照里一个工具调用的执行阶段。running 表示
// "准备执行/执行中、结果未确认"——恢复展示时不能猜成未执行或已成功。
type CheckpointToolState struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // running/completed/failed
	Summary string `json:"summary,omitempty"`
}

// CheckpointPayload 是版本化快照载荷：run 已产生的答案正文、Thought 与
// 工具调用阶段。它只服务崩溃后的展示与用户核对，不进入模型上下文。
type CheckpointPayload struct {
	Reply     string                `json:"reply"`
	Reasoning string                `json:"reasoning"`
	Tools     []CheckpointToolState `json:"tools,omitempty"`
}

// CheckpointRecord 是一条已保存检查点的完整读取结果。会话归属不单独
// 复制，始终以 runs 关联的 session 为准，防止调用方指定第二份事实。
type CheckpointRecord struct {
	RunID         string
	SessionID     string
	Sequence      int64
	FormatVersion int
	Payload       CheckpointPayload
	SavedAt       time.Time
}

// validateCheckpoint 在写入前校验载荷边界与阶段值；JSON 编码同时作为
// 幂等比较的规范化形式（同序号同载荷按字节比较）。
func validateCheckpoint(sequence int64, payload CheckpointPayload) (string, error) {
	if sequence <= 0 {
		return "", errors.New("checkpoint sequence must be positive")
	}
	if len(payload.Tools) > CheckpointMaxTools {
		return "", ErrHistoryLimit
	}
	for _, tool := range payload.Tools {
		switch tool.Status {
		case "running", "completed", "failed":
		default:
			return "", fmt.Errorf("invalid checkpoint tool status %q", tool.Status)
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if len(encoded) > CheckpointMaxBytes {
		return "", ErrHistoryLimit
	}
	return string(encoded), nil
}

// decodeCheckpointPayload 校验版本与载荷后解码；损坏、超限或未来版本
// 都拒绝整条记录，不返回部分片段。
func decodeCheckpointPayload(formatVersion int, encoded []byte) (CheckpointPayload, error) {
	if formatVersion <= 0 || formatVersion > CheckpointFormatVersion {
		return CheckpointPayload{}, fmt.Errorf("%w: unsupported checkpoint format version %d", ErrCorruptHistory, formatVersion)
	}
	if len(encoded) > CheckpointMaxBytes {
		return CheckpointPayload{}, ErrHistoryLimit
	}
	if err := checkJSONExpansion(encoded); err != nil {
		return CheckpointPayload{}, err
	}
	var payload CheckpointPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return CheckpointPayload{}, fmt.Errorf("%w: corrupt checkpoint payload: %v", ErrCorruptHistory, err)
	}
	if len(payload.Tools) > CheckpointMaxTools {
		return CheckpointPayload{}, ErrHistoryLimit
	}
	for _, tool := range payload.Tools {
		switch tool.Status {
		case "running", "completed", "failed":
		default:
			return CheckpointPayload{}, fmt.Errorf("%w: invalid checkpoint tool status %q", ErrCorruptHistory, tool.Status)
		}
	}
	return payload, nil
}

// CheckpointStore 扩展运行检查点契约。写入路径必须在存储事务内校验
// run 归属与 running 状态、序号单调与终态互斥，不能只依赖进程内协调。
type CheckpointStore interface {
	// SaveCheckpoint 原子保存 run 的最新快照：序号严格递增；同序号同载荷
	// 幂等、同序号不同载荷冲突；较小序号与终态后的写入都拒绝。run 不存在
	// 返回 ErrUnknownRun。
	SaveCheckpoint(ctx context.Context, runID string, sequence int64, payload CheckpointPayload, savedAt time.Time) error

	// LoadCheckpoint 读取 run 的最新检查点；run 无检查点返回
	// ErrCheckpointNotFound，载荷损坏/超限/未来版本整次拒绝。
	LoadCheckpoint(ctx context.Context, runID string) (CheckpointRecord, error)

	// LatestCheckpoint 返回会话最新 run（按起点顺序）的检查点，供恢复时
	// 展示最后未完成任务的已落盘进度；没有时第二个返回值为 false。
	LatestCheckpoint(ctx context.Context, sessionID string) (CheckpointRecord, bool, error)
}
