package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"plume-agent/internal/store"
	"plume-agent/internal/tui"
)

// resumedSession 是 `-c` 定位到的可恢复会话：记录、匹配的模型配置与
// 预构建的展示转录。
type resumedSession struct {
	record     store.SessionRecord
	selected   *config.ModelConfig
	transcript []tui.TranscriptLine
	limits     store.LoadLimits
}

// workspaceDir 按启动目录解析 tools.workspace，并返回符号链接规范绝对路径。
// 查询、保存与执行共用该根；解析失败必须拒绝启动，不能退回另一个工作区。
func workspaceDir(cfg *config.Config) (string, error) {
	root := config.DefaultTools().Workspace
	if cfg != nil && cfg.Tools != nil {
		root = cfg.Tools.Workspace
	}
	dir, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve tools workspace: %w", err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve tools workspace: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("tools workspace is not an accessible directory")
	}
	return filepath.Clean(dir), nil
}

// historyLoadLimits 在读取数据库正文前施加请求预算与固定展示安全上限。
// 不截断历史；超限必须提示新会话，以免把部分工具组送入下一次请求。
func historyLoadLimits(cfg *config.Config) store.LoadLimits {
	maxBytes := config.DefaultAgent().Budget.RequestBytes
	if cfg != nil && cfg.Agent != nil {
		maxBytes = cfg.Agent.Budget.RequestBytes
	}
	return store.LoadLimits{MaxBytes: min(maxBytes, 8<<20), MaxRecords: 10000}
}

// modelRefOf 从模型配置构造持久化引用快照；凭据引用不落入快照。
func modelRefOf(m *config.ModelConfig) store.ModelRef {
	if m == nil {
		return store.ModelRef{}
	}
	return store.ModelRef{ConfigID: m.ID, Provider: m.Provider, Protocol: m.Protocol, Model: m.Model, BaseURL: m.BaseURL}
}

// findResumableSession 定位当前工作区最近有 run 的会话并校验模型引用：
// 先按配置 ID 匹配，再按指纹匹配（同端点不同 ID 也算兼容）；都不行时
// 返回 resume_incompatible，不静默改用其他模型。显式 --model 必须与目标
// 会话的引用一致，不借此跨模型迁移。没有历史时返回 nil（调用方提示后开新会话）。
func findResumableSession(ctx context.Context, history store.Store, cfg *config.Config, modelFlag, workspace string) (*resumedSession, error) {
	record, ok, err := history.LatestSession(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	if !ok {
		return nil, nil
	}
	selected := matchModelRef(cfg, record.ModelRef)
	if selected == nil {
		return nil, fmt.Errorf("resume_incompatible: session %s references model %s (%s/%s), which is not configured anymore; restore the model config or start a new conversation",
			shortID(record.ID), record.ModelRef.ConfigID, record.ModelRef.Provider, record.ModelRef.Model)
	}
	if modelFlag != "" && modelFlag != selected.ID {
		return nil, errors.New("resume_incompatible: --model does not match the resumed session's model reference")
	}
	limits := historyLoadLimits(cfg)
	runs, err := history.ListRuns(ctx, record.ID, limits)
	if err != nil {
		return nil, fmt.Errorf("load runs: %w", err)
	}
	transcript := []tui.TranscriptLine{{
		Kind: tui.TranscriptSystem,
		Text: fmt.Sprintf("Resumed session %s · %d runs · model %s/%s", shortID(record.ID), len(runs), selected.Provider, selected.Model),
	}}
	for _, run := range runs {
		transcript = append(transcript, tui.TranscriptLine{Kind: tui.TranscriptUser, Text: run.Input})
		if run.Reply != "" || run.Reasoning != "" || run.Status != store.RunCompleted {
			line := tui.TranscriptLine{Kind: tui.TranscriptAssistant, Text: run.Reply, Reasoning: run.Reasoning}
			switch run.Status {
			case store.RunCancelled:
				line.Terminal = "cancelled"
			case store.RunInterrupted:
				line.Terminal = "interrupted"
			case store.RunFailed:
				line.Terminal = "failed"
			}
			transcript = append(transcript, line)
		}
	}
	return &resumedSession{record: record, selected: selected, transcript: transcript, limits: limits}, nil
}

// matchModelRef 用 ID 定位并验证 provider/协议/模型/端点指纹；同 ID 被修改
// 时立即拒绝。只有旧 ID 已不存在，才允许定位到指纹完全一致的重命名配置。
func matchModelRef(cfg *config.Config, ref store.ModelRef) *config.ModelConfig {
	for i := range cfg.Models {
		candidate := &cfg.Models[i]
		if candidate.ID == ref.ConfigID {
			if modelRefOf(candidate).Fingerprint() == ref.Fingerprint() {
				return candidate
			}
			return nil
		}
	}
	for i := range cfg.Models {
		candidate := &cfg.Models[i]
		if modelRefOf(candidate).Fingerprint() == ref.Fingerprint() {
			return candidate
		}
	}
	return nil
}

// adoptResumedSession 把恢复的会话装载进服务：成功轮次消息、用量快照与
// 元数据预灌内存。状态栏从本次打开计时，原创建时间单独保留；未知用量不补零。
func adoptResumedSession(ctx context.Context, service *app.Service, history store.Store, resume *resumedSession) error {
	messages, nextSeq, err := history.LoadTurns(ctx, resume.record.ID, resume.limits)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}
	calls, err := history.LoadCalls(ctx, resume.record.ID, resume.limits)
	if err != nil {
		return fmt.Errorf("load usage: %w", err)
	}
	usage := make(map[string]model.Usage)
	lastCall := ""
	for _, call := range calls {
		if call.Usage != nil {
			usage[call.ID] = call.Usage.Usage()
		} else {
			usage[call.ID] = model.Usage{}
		}
		lastCall = call.ID
	}
	// 存储返回下一个 seq；内存统计记录的是已完成轮数，空会话必须仍为零。
	if err := service.AdoptSession(resume.record.ID, app.RestoreSession(messages, nextSeq-1, usage, lastCall, time.Now()), resume.record.CreatedAt); err != nil {
		return fmt.Errorf("adopt history: %w", err)
	}
	return nil
}

// shortID 显示会话 ID 的稳定短前缀（≥8 位）。
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
