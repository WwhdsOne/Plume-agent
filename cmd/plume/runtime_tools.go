package main

import (
	"plume-agent/internal/agent"
	"plume-agent/internal/config"
	"plume-agent/internal/tools"
	"time"
)

func limitsFromConfig(b config.BudgetConfig) agent.Limits {
	return agent.Limits{ModelCalls: b.ModelCalls, ToolCalls: b.ToolCalls, ToolCallsPerStep: b.ToolCallsPerStep, RequestBytes: b.RequestBytes, ArgumentBytes: b.ArgumentBytes, ResultBytes: b.ResultBytes,
		RunTimeout: time.Duration(b.RunTimeoutSeconds) * time.Second, ModelTimeout: time.Duration(b.ModelTimeoutSeconds) * time.Second, ToolTimeout: time.Duration(b.ToolTimeoutSeconds) * time.Second}
}

// configureRuntimeTools 只装配已有配置，不运行命令或读取文件正文。
func configureRuntimeTools(runtime *agent.Runtime, cfg *config.Config) error {
	a, t := cfg.Agent, cfg.Tools
	if a == nil {
		a = config.DefaultAgent()
	}
	if t == nil {
		t = config.DefaultTools()
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	options := tools.WorkspaceOptions{Root: t.Workspace, Enabled: append([]string{}, t.Enabled...), ReadLines: t.ReadLines, SearchResults: t.SearchResults, MaxFileBytes: t.MaxFileBytes, MaxOutputBytes: t.MaxOutputBytes, Shell: t.Shell, ResultBytes: a.Budget.ResultBytes, BashTimeoutSeconds: a.Budget.ToolTimeoutSeconds}
	registry, err := tools.NewWorkspace(options)
	if err != nil {
		return err
	}
	runtime.SetTools(registry)
	runtime.SetLimits(limitsFromConfig(a.Budget))
	return nil
}
