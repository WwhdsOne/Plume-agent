package main

import (
	"plume-agent/internal/agent"
	"plume-agent/internal/config"
)

// configureRuntimeSoul 只解析配置路径，正文留给每个 run 的 preparing 阶段。
// config.Dir 是相对路径的基准，不能从当前工作区意外加载同名人格文件。
func configureRuntimeSoul(runtime *agent.Runtime, options *config.AgentConfig) error {
	soul := options.SoulConfig()
	path, err := soul.ResolvePath()
	if err != nil {
		return err
	}
	runtime.SetSoul(path, soul.MaxBytes)
	return nil
}
