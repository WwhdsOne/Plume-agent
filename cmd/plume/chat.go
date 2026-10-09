package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"plume-agent/internal/agent"
	"plume-agent/internal/app"
	"plume-agent/internal/config"
	"plume-agent/internal/model"
	"plume-agent/internal/provider"
	"plume-agent/internal/telemetry"
	"plume-agent/internal/tui"
)

func newChatCmd() *cobra.Command {
	chat := &cobra.Command{
		Use:   "chat",
		Short: "打开终端聊天界面（TUI）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			offline, _ := cmd.Flags().GetBool("offline")
			modelFlag, _ := cmd.Flags().GetString("model")
			if offline && modelFlag != "" {
				return errors.New("--offline and --model are mutually exclusive")
			}
			out := cmd.OutOrStdout()
			if offline {
				return startChat(out, "", true)
			}
			return startChat(out, modelFlag, false)
		},
	}
	chat.Flags().Bool("offline", false, "run against the scripted fake model (no config, key, or network)")
	chat.Flags().String("model", "", "model configuration ID from config.json (not an API model name)")
	return chat
}

// credentialSource 把 config 的凭据文件适配为工厂的 Credentials 接口。
// Key 只在内存中使用，不进日志与 trace。
type credentialSource struct{}

func (credentialSource) Key(ref string) (string, error) {
	secret, err := config.GetCredential(ref)
	if err != nil {
		return "", err
	}
	return string(secret), nil
}

// startChat 装配模型客户端、app 服务与 TUI 并运行。
// offline=true 时使用脚本化 fake，不读配置、不需要 Key、不联网。
func startChat(out io.Writer, modelFlag string, offline bool) error {
	if f, ok := out.(*os.File); ok && !isatty.IsTerminal(f.Fd()) && !isatty.IsCygwinTerminal(f.Fd()) {
		// 判定用 go-isatty，不用 os.ModeCharDevice（/dev/null 也是字符设备）。
		return errors.New("chat needs an interactive terminal (run it inside a TTY; try `plume chat --offline` for scripted output)")
	}

	var runtime *agent.Runtime
	var options tui.Options
	info := telemetry.ReasoningInfo{Source: "offline", Effective: "unknown", Capability: "scripted"}
	providerID := "fake"
	label := "fake/offline"
	if offline {
		runtime = agent.New(model.NewToolDemo(), "offline")
		defaults := config.DefaultTools()
		defaults.Enabled = append(defaults.Enabled, "calculate", "current_time")
		if err := configureRuntimeTools(runtime, &config.Config{Tools: defaults, Agent: config.DefaultAgent()}); err != nil {
			return err
		}
	} else {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		runtime, label, err = buildRuntime(cfg, modelFlag)
		if err != nil {
			return err
		}
		options.StatusMessages = cfg.TUI.MessageOverrides()
		options.StatusLine = cfg.TUI.StatusLine
		selected, _ := selectModelConfig(cfg, modelFlag)
		policy, _ := provider.ResolveReasoning(provider.ModelSpec{Provider: selected.Provider, Protocol: selected.Protocol, BaseURL: selected.BaseURL, Model: selected.Model, ReasoningEffort: selected.ReasoningEffort})
		providerID = selected.Provider
		info = telemetry.ReasoningInfo{Requested: string(policy.Requested), Source: policy.Source, Effective: string(policy.Effective), Capability: policy.Capability}
		options.Provider, options.ModelID = selected.Provider, selected.Model
		options.ContextWindowTokens = selected.ContextWindowTokens
		if policy.Capability == "verified" {
			options.Reasoning = string(policy.Effective)
			if policy.Requested != policy.Effective {
				options.Reasoning = string(policy.Requested) + "→" + string(policy.Effective)
			}
		}
	}
	defer runtime.Close()

	trace, err := openChatTrace()
	if err != nil {
		return fmt.Errorf("open chat trace: %w", err)
	}
	recorder := telemetry.NewModelRecorder(trace)
	recorder.SetCloser(trace)
	defer recorder.Close()
	runtime.SetRecorder(recorder, providerID, info)
	service := app.NewService(runtime, recorder)
	defer service.Close()
	ctx := context.Background()
	dir, _ := os.Getwd()
	options.Dir = dir
	options.HomeDir, _ = os.UserHomeDir()
	stats := service.Session().Statistics()
	options.InitialStats = &stats

	chatModel := tui.NewWithOptions(label, tui.Hooks{
		Submit:       func(input string) (string, error) { return service.Submit(ctx, input) },
		Cancel:       service.Cancel,
		ResetSession: func() { service.Session().Reset() },
		FirstAnswer:  recorder.UIAnswer,
	}, options)

	// 开屏（docs/specs/tui/splash.md）：方框 + 羽毛 LOGO + 键位提示 + 真实信息，
	// 首次 resize 按真实窗口宽度渲染进记录区头部；必须包装 TeaModel 之前
	// 注入（包装后再改不会反映进 program 持有的状态）。
	chatModel.SetSplash(tui.Splash{
		Version: splashVersion(),
		Label:   label,
		Dir:     dir,
	})
	program := tea.NewProgram(&tui.TeaModel{M: chatModel})
	// 事件桥：app 事件投递进 Bubble Tea 主循环。channel 无关闭约定，
	// 桥随进程退出回收（有界缓冲 + UI 持续消费）。
	bridgeDone := make(chan struct{})
	defer close(bridgeDone)
	workspaceCtx, cancelWorkspace := context.WithCancel(ctx)
	statusCfg := options.StatusLine
	if statusCfg == nil {
		statusCfg = config.DefaultStatusLine()
	}
	var workspaceEvents <-chan app.WorkspaceStatus
	gitEnabled, envEnabled := statusItemEnabled(statusCfg, "git"), statusItemEnabled(statusCfg, "uv_env")
	if gitEnabled || envEnabled {
		workspaceEvents = app.WatchWorkspace(workspaceCtx, app.WorkspaceOptions{Dir: dir, GitEnabled: gitEnabled, EnvironmentEnabled: envEnabled,
			RefreshInterval: time.Duration(statusCfg.EnvironmentRefreshMS) * time.Millisecond, GitTimeout: time.Duration(statusCfg.GitTimeoutMS) * time.Millisecond})
	}
	defer func() {
		cancelWorkspace()
		if workspaceEvents != nil {
			for range workspaceEvents {
			}
		}
	}()
	// bridge 固定捕获通道，主退出路径可安全等待采集停止，不共享可变通道变量。
	workspaceStream := workspaceEvents
	go func() {
		for {
			select {
			case event, ok := <-service.Events():
				if !ok {
					return
				}
				program.Send(tui.AppEvent{Event: event})
			case snapshot, ok := <-workspaceStream:
				if !ok {
					workspaceStream = nil
					continue
				}
				program.Send(tui.WorkspaceEvent{Status: snapshot})
			case <-bridgeDone:
				return
			}
		}
	}()

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("chat ui: %w", err)
	}
	return nil
}

func statusItemEnabled(cfg *config.StatusLineConfig, id string) bool {
	if !cfg.Enabled {
		return false
	}
	for _, item := range cfg.Items {
		if item.ID == id && item.Enabled {
			return true
		}
	}
	return false
}

func openChatTrace() (*os.File, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(dir, "logs")
	if err = os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("chat-%s-%d.jsonl", time.Now().UTC().Format("20060102T150405.000000000"), os.Getpid())
	return os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

// buildRuntime 从已加载配置装配 agent 与状态栏标签。
// 关键约定：agent 绑定的是 **API 模型名**（config.Model.Model，如
// deepseek-flash），不是配置 ID（config.Model.ID，如 deepseek-default）——
// 两者混用会被供应商以 400 invalid_request_error 拒绝（G1b.2 实测踩坑）。
func buildRuntime(cfg *config.Config, modelFlag string) (*agent.Runtime, string, error) {
	selected, err := selectModelConfig(cfg, modelFlag)
	if err != nil {
		return nil, "", err
	}
	factory := provider.NewModelFactory(provider.NewRegistry(), credentialSource{})
	client, err := factory.Build(provider.ModelSpec{
		Provider:        selected.Provider,
		Protocol:        selected.Protocol,
		BaseURL:         selected.BaseURL,
		Model:           selected.Model,
		APIKeyRef:       selected.APIKeyRef,
		ReasoningEffort: selected.ReasoningEffort,
	})
	if err != nil {
		return nil, "", fmt.Errorf("build model client: %w", err)
	}
	runtime := agent.New(client, selected.Model)
	if err := configureRuntimeSoul(runtime, cfg.Agent); err != nil {
		return nil, "", fmt.Errorf("configure soul: %w", err)
	}
	if err := configureRuntimeTools(runtime, cfg); err != nil {
		return nil, "", fmt.Errorf("configure tools: %w", err)
	}
	return runtime, fmt.Sprintf("%s/%s", selected.Provider, selected.Model), nil
}

// selectModelConfig 解析要用的模型配置：显式 --model 只接受已保存的 ID，
// 不通过参数传 Key；默认用 default_model。
func selectModelConfig(cfg *config.Config, modelFlag string) (*config.ModelConfig, error) {
	id := modelFlag
	if id == "" {
		id = cfg.DefaultModel
	}
	if id == "" {
		return nil, errors.New("no model selected: pass --model <config id> or set default_model via setup")
	}
	for i := range cfg.Models {
		if cfg.Models[i].ID == id {
			return &cfg.Models[i], nil
		}
	}
	return nil, fmt.Errorf("model config %q not found in config.json", id)
}

// splashVersion 组装开屏版本段；未打戳的构建如实显示 dev (none)。
func splashVersion() string {
	label := version
	if label != "dev" {
		label = "v" + label
	}
	return fmt.Sprintf("%s (%s)", label, commit)
}
