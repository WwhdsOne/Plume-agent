package main

import (
	"context"
	"fmt"
	"strings"

	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"plume-agent/internal/provider"
	"plume-agent/internal/store"
	"plume-agent/internal/telemetry"
)

// buildOfflineRuntime 每个会话获得独立工具读取证明；只使用硬编码默认配置，
// 不读取真实 config、历史或人格文件。/demo 的协议脚本仍由 model 层提供。
func buildOfflineRuntime() (*agent.Runtime, error) {
	runtime := agent.New(model.NewToolDemo(), "offline")
	defaults := config.DefaultTools()
	defaults.Enabled = append(defaults.Enabled, "calculate", "current_time")
	if err := configureRuntimeTools(runtime, &config.Config{Tools: defaults, Agent: config.DefaultAgent()}); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	return runtime, nil
}

// reasoningInfoOf 共用启动与恢复的纯策略解析，不通过付费 Probe 猜测能力。
func reasoningInfoOf(selected *config.ModelConfig) (telemetry.ReasoningInfo, string) {
	policy, _ := provider.ResolveReasoning(provider.ModelSpec{Provider: selected.Provider, Protocol: selected.Protocol,
		BaseURL: selected.BaseURL, Model: selected.Model, ReasoningEffort: selected.ReasoningEffort})
	info := telemetry.ReasoningInfo{Requested: string(policy.Requested), Source: policy.Source, Effective: string(policy.Effective), Capability: policy.Capability}
	label := ""
	if policy.Capability == "verified" {
		label = string(policy.Effective)
		if policy.Requested != policy.Effective {
			label = string(policy.Requested) + "→" + string(policy.Effective)
		}
	}
	return info, label
}

// configureServiceCommands 固定本次启动配置；恢复只允许引用指纹仍兼容的模型，
// 每次装配新的工具句柄并沿用 recorder，不重新读取配置或发送探测请求。
func configureServiceCommands(service *app.Service, cfg *config.Config, selected *config.ModelConfig, history store.Store, offline bool, workspace string, recorder *telemetry.ModelRecorder) error {
	options := app.CommandOptions{History: history, Scope: app.LocalScope(), Workspace: workspace,
		PageSize: config.DefaultSessions().PageSize, LoadLimits: historyLoadLimits(cfg)}
	if offline {
		options.ModelRef = store.ModelRef{ConfigID: "offline", Provider: "fake", Model: "offline"}
		options.Models = []app.CommandModelInfo{{ID: "offline", Provider: "fake", Model: "offline"}}
		options.DescribeModel = func(store.ModelRef) app.CommandModelInfo { return options.Models[0] }
		options.ValidateModel = func(ref store.ModelRef) error {
			if ref.Fingerprint() != options.ModelRef.Fingerprint() {
				return fmt.Errorf("resume_incompatible: offline model reference does not match")
			}
			return nil
		}
		options.BuildRuntime = func(ctx context.Context, ref store.ModelRef) (*agent.Runtime, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := options.ValidateModel(ref); err != nil {
				return nil, err
			}
			runtime, err := buildOfflineRuntime()
			if err != nil {
				return nil, err
			}
			runtime.SetRecorder(recorder, "fake", telemetry.ReasoningInfo{Source: "offline", Effective: "unknown", Capability: "scripted"})
			return runtime, nil
		}
	} else {
		options.ModelRef = modelRefOf(selected)
		options.Persistent = cfg.HistoryEnabled()
		if cfg.Sessions != nil {
			options.PageSize = cfg.Sessions.PageSize
		}
		for i := range cfg.Models {
			item := &cfg.Models[i]
			_, reasoning := reasoningInfoOf(item)
			options.Models = append(options.Models, app.CommandModelInfo{ID: item.ID, Provider: item.Provider, Model: item.Model,
				Reasoning: reasoning, ContextWindowTokens: item.ContextWindowTokens})
		}
		options.DescribeModel = func(ref store.ModelRef) app.CommandModelInfo {
			matched := matchModelRef(cfg, ref)
			if matched == nil {
				return app.CommandModelInfo{ID: ref.ConfigID, Provider: ref.Provider, Model: ref.Model}
			}
			_, reasoning := reasoningInfoOf(matched)
			return app.CommandModelInfo{ID: matched.ID, Provider: matched.Provider, Model: matched.Model,
				Reasoning: reasoning, ContextWindowTokens: matched.ContextWindowTokens}
		}
		options.ValidateModel = func(ref store.ModelRef) error {
			if matchModelRef(cfg, ref) == nil {
				return fmt.Errorf("resume_incompatible: model reference is not available in startup configuration")
			}
			return nil
		}
		options.BuildRuntime = func(ctx context.Context, ref store.ModelRef) (*agent.Runtime, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			matched := matchModelRef(cfg, ref)
			if matched == nil {
				return nil, fmt.Errorf("resume_incompatible: model reference is not available in startup configuration")
			}
			runtime, _, err := buildRuntimeFrom(cfg, matched)
			if err != nil {
				return nil, err
			}
			info, _ := reasoningInfoOf(matched)
			runtime.SetRecorder(recorder, matched.Provider, info)
			return runtime, nil
		}
	}
	return service.ConfigureCommands(options)
}

// handleTUIInput 仅为已存在的 offline 演示做显式转义；在线未知 slash 输入
// 必须继续报错，不能把任意未知指令悄悄送给模型。
func handleTUIInput(ctx context.Context, service *app.Service, offline bool, input string) app.CommandResult {
	trimmed := strings.TrimSpace(input)
	if offline && (trimmed == "/demo" || strings.HasPrefix(trimmed, "/demo ")) && !strings.ContainsAny(trimmed, "\r\n") {
		input = "/" + trimmed
	}
	return service.HandleInput(ctx, app.CommandRequest{Text: input, Scope: app.LocalScope(), Entrance: app.EntranceTUI})
}

// serviceRuntimeCloser 跟随服务已切换及退休的运行时，避免退出只关闭启动时
// 的旧指针而泄漏恢复后新建的工具句柄。
type serviceRuntimeCloser struct{ service *app.Service }

func (c serviceRuntimeCloser) Close() error { return c.service.CloseRuntimes() }
