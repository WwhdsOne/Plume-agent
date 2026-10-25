package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"plume-agent/internal/store"
)

// LoadRecovery 补齐所有中断 run 的检查点展示副本，并为最后未完成 run
// 构造回填候选。较新的成功 run 只禁止旧任务回填，不得遮住已保存的旧进度。
// 展示载荷逐条累计，全部正文、工具 JSON 与条目共用最多 8MiB/10000 的
// 会话边界；调用方只能收紧。损坏/超限整次拒绝，不改 runs、不写模型历史。
func LoadRecovery(ctx context.Context, history store.Store, sessionID string, runs []store.RunRecord, limits ...store.LoadLimits) ([]store.RunRecord, *SessionRecovery, error) {
	limit := store.LoadLimits{MaxBytes: 8 << 20, MaxRecords: 10000}
	for _, requested := range limits {
		if requested.MaxBytes > 0 && requested.MaxBytes < limit.MaxBytes {
			limit.MaxBytes = requested.MaxBytes
		}
		if requested.MaxRecords > 0 && requested.MaxRecords < limit.MaxRecords {
			limit.MaxRecords = requested.MaxRecords
		}
	}
	if len(runs) > limit.MaxRecords {
		return nil, nil, store.ErrHistoryLimit
	}
	views := make([]store.RunRecord, 0, len(runs))
	bytes, records := 0, 0
	for _, run := range runs {
		if run.SessionID != sessionID {
			return nil, nil, fmt.Errorf("%w: recovery run ownership mismatch", store.ErrCorruptHistory)
		}
		view := run
		view.ToolEvents = append([]store.ToolEventRecord(nil), run.ToolEvents...)
		if run.Status == store.RunInterrupted {
			// 每次只加载一条有界检查点；旧快照不能因后续新 run 而不可见。
			checkpoint, err := history.LoadCheckpoint(ctx, run.ID)
			if err != nil && !errors.Is(err, store.ErrCheckpointNotFound) {
				return nil, nil, err
			}
			if err == nil {
				if checkpoint.RunID != run.ID || checkpoint.SessionID != sessionID {
					return nil, nil, fmt.Errorf("%w: recovery checkpoint ownership mismatch", store.ErrCorruptHistory)
				}
				// 检查点优先；缺失字段仍保留终态记录中的已知展示内容。
				if checkpoint.Payload.Reply != "" {
					view.Reply = checkpoint.Payload.Reply
				}
				if checkpoint.Payload.Reasoning != "" {
					view.Reasoning = checkpoint.Payload.Reasoning
				}
				if len(checkpoint.Payload.Tools) > 0 {
					view.ToolEvents = make([]store.ToolEventRecord, 0, len(checkpoint.Payload.Tools))
					for _, tool := range checkpoint.Payload.Tools {
						view.ToolEvents = append(view.ToolEvents, store.ToolEventRecord{Name: tool.Name, Status: tool.Status, Summary: tool.Summary})
					}
				}
			}
		}
		encodedTools, err := json.Marshal(view.ToolEvents)
		if err != nil {
			return nil, nil, err
		}
		bytes += len(view.ID) + len(view.Input) + len(view.Status) + len(view.Error) + len(view.Reply) + len(view.Reasoning) + len(encodedTools)
		records += 1 + len(view.ToolEvents)
		if bytes > limit.MaxBytes || records > limit.MaxRecords {
			return nil, nil, store.ErrHistoryLimit
		}
		views = append(views, view)
	}
	if len(views) == 0 {
		return views, nil, nil
	}
	last := views[len(views)-1]
	switch last.Status {
	case store.RunFailed, store.RunCancelled, store.RunInterrupted:
		return views, &SessionRecovery{LastRunID: last.ID, LastStatus: last.Status, ResendInput: last.Input,
			Reply: last.Reply, Reasoning: last.Reasoning, Tools: RecoveryTools(last)}, nil
	default:
		return views, nil, nil
	}
}

// RecoveryTools 把串行工具事件转成展示状态：旧历史没有 CallID，只合并
// 紧邻且同名的 running→completed/failed，不按名称全局去重。未配对的
// running 在失败/取消/中断 run 中标为结果未确认；新检查点已是每调用快照。
func RecoveryTools(run store.RunRecord) []RecoveryToolView {
	var tools []RecoveryToolView
	for _, event := range run.ToolEvents {
		tool := RecoveryToolView{Name: event.Name, Status: event.Status, Summary: event.Summary}
		if len(tools) > 0 && (event.Status == "completed" || event.Status == "failed") &&
			tools[len(tools)-1].Name == event.Name && tools[len(tools)-1].Status == "running" {
			tools[len(tools)-1] = tool
			continue
		}
		tool.Unconfirmed = event.Status == "running" && (run.Status == store.RunInterrupted || run.Status == store.RunFailed || run.Status == store.RunCancelled)
		tools = append(tools, tool)
	}
	return tools
}
