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
	"plume-agent/internal/provider"
	"plume-agent/internal/store"
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
			pprofAddr, _ := cmd.Flags().GetString("pprof")
			continueRecent, _ := cmd.Flags().GetBool("continue")
			if offline && modelFlag != "" {
				return errors.New("--offline and --model are mutually exclusive")
			}
			if offline && continueRecent {
				return errors.New("--offline and --continue are mutually exclusive")
			}
			opts := chatOptions{model: modelFlag, offline: offline, pprof: pprofAddr, continueRecent: continueRecent}
			return startChat(cmd.OutOrStdout(), opts)
		},
	}
	chat.Flags().Bool("offline", false, "run against the scripted fake model (no config, key, or network)")
	chat.Flags().String("model", "", "model configuration ID from config.json (not an API model name)")
	chat.Flags().String("pprof", "", "start a loopback-only pprof debug endpoint (e.g. 127.0.0.1:6060; default off)")
	chat.Flags().BoolP("continue", "c", false, "resume the most recent session in this workspace (requires persisted history)")
	return chat
}

// chatOptions 聚合 chat 启动参数，避免 startChat 变成一串位置布尔/字符串。
type chatOptions struct {
	model          string // 配置里的模型配置 ID；空表示用 default_model
	offline        bool   // 脚本化 fake：不读配置、不需要 Key、不联网
	pprof          string // pprof 监听地址；空表示关闭
	continueRecent bool   // -c：恢复当前工作区最近有 run 的本地会话
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
// opts.offline=true 时使用脚本化 fake，不读配置、不需要 Key、不联网。
func startChat(out io.Writer, opts chatOptions) (resultErr error) {
	modelFlag := opts.model
	offline := opts.offline
	if f, ok := out.(*os.File); ok && !isatty.IsTerminal(f.Fd()) && !isatty.IsCygwinTerminal(f.Fd()) {
		// 判定用 go-isatty，不用 os.ModeCharDevice（/dev/null 也是字符设备）。
		return errors.New("chat needs an interactive terminal (run it inside a TTY; try `plume chat --offline` for scripted output)")
	}
	resources := chatResources{timeout: time.Duration(config.DefaultSessions().ShutdownTimeoutMS) * time.Millisecond}
	// 单一退出入口覆盖装配失败与正常退出；每一步共享同一个截止时间。
	defer func() { resultErr = errors.Join(resultErr, resources.close()) }()

	var runtime *agent.Runtime
	var options tui.Options
	info := telemetry.ReasoningInfo{Source: "offline", Effective: "unknown", Capability: "scripted"}
	providerID := "fake"
	label := "fake/offline"
	// resume 装载恢复结果：非空时按会话保存的模型引用装配运行时。
	var resume *resumedSession
	var history store.Store
	var cfg *config.Config
	var selected *config.ModelConfig
	var workspace string
	if offline {
		var err error
		runtime, err = buildOfflineRuntime()
		if err != nil {
			return err
		}
		resources.runtime = runtime
		workspace, err = workspaceDir(nil)
		if err != nil {
			return err
		}
	} else {
		var err error
		cfg, err = config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if cfg.Sessions != nil {
			resources.timeout = time.Duration(cfg.Sessions.ShutdownTimeoutMS) * time.Millisecond
		}
		workspace, err = workspaceDir(cfg)
		if err != nil {
			return err
		}
		// 配置快照内固定规范工具根，使后续执行与历史身份共享同一目录。
		toolsCfg := config.DefaultTools()
		if cfg.Tools != nil {
			copyCfg := *cfg.Tools
			toolsCfg = &copyCfg
		}
		toolsCfg.Workspace = workspace
		cfg.Tools = toolsCfg
		if cfg.HistoryEnabled() {
			dbPath, dbErr := cfg.HistoryDatabasePath()
			if dbErr != nil {
				return dbErr
			}
			// 打开即取整库进程独占锁；被占用（ErrHistoryInUse）明确退出，
			// 不等待也不降级。遗留 running 记录在锁内标记 interrupted。
			history, dbErr = store.OpenSQLite(dbPath)
			if dbErr != nil {
				return fmt.Errorf("open session history: %w", dbErr)
			}
			resources.history = history
			if _, dbErr = history.InterruptRunning(context.Background(), time.Now()); dbErr != nil {
				return fmt.Errorf("mark interrupted runs: %w", dbErr)
			}
		}
		if opts.continueRecent {
			if history == nil {
				return errors.New("cannot --continue: session history is disabled (sessions.history.enabled=false)")
			}
			resume, err = findResumableSession(context.Background(), history, cfg, modelFlag, workspace)
			if err != nil {
				return err
			}
			if resume == nil {
				_, _ = fmt.Fprintln(out, "No resumable session in this workspace; starting a new conversation.")
			}
		}
		if resume != nil {
			selected = resume.selected
			runtime, label, err = buildRuntimeFrom(cfg, selected)
		} else {
			runtime, label, err = buildRuntime(cfg, modelFlag)
			selected, _ = selectModelConfig(cfg, modelFlag)
		}
		if err != nil {
			return err
		}
		resources.runtime = runtime
		options.StatusMessages = cfg.TUI.MessageOverrides()
		options.StatusLine = cfg.TUI.StatusLine
		providerID = selected.Provider
		info, options.Reasoning = reasoningInfoOf(selected)
		options.Provider, options.ModelID = selected.Provider, selected.Model
		options.ContextWindowTokens = selected.ContextWindowTokens
	}

	trace, err := openChatTrace()
	if err != nil {
		return fmt.Errorf("open chat trace: %w", err)
	}
	recorder := telemetry.NewModelRecorder(trace)
	recorder.SetCloser(trace)
	resources.recorder = recorder
	runtime.SetRecorder(recorder, providerID, info)
	service := app.NewService(runtime, recorder)
	resources.service = service

	// 本机调试端点：默认关闭，只有显式 --pprof 才监听，且只接受回环地址。
	// 在 TUI 接管屏幕前打印一次，便于确认实际端口（写 :0 时由内核分配）。
	pprofServer, err := telemetry.StartPprof(opts.pprof)
	if err != nil {
		return err
	}
	if pprofServer != nil {
		resources.pprof = pprofServer
		_, _ = fmt.Fprintf(out, "pprof listening on %s\n", pprofServer.URL())
	}

	ctx := context.Background()
	dir, _ := os.Getwd()
	options.Dir = dir
	options.HomeDir, _ = os.UserHomeDir()
	// 持久层绑定：此后每个 run 的起点、用量与终态先落盘再进内存。
	if history != nil {
		shutdown := time.Duration(config.DefaultSessions().ShutdownTimeoutMS) * time.Millisecond
		if cfg != nil && cfg.Sessions != nil {
			shutdown = time.Duration(cfg.Sessions.ShutdownTimeoutMS) * time.Millisecond
		}
		service.EnablePersistence(history, workspace, modelRefOf(selected), shutdown)
	}
	// `-c` 恢复：预灌成功历史与用量统计，转录进 TUI 记录区。
	if resume != nil {
		if err := adoptResumedSession(ctx, service, history, resume); err != nil {
			return err
		}
		options.InitialTranscript = resume.transcript
	}
	// 历史关闭与离线模式同样能浏览本进程会话，但从不打开已有私密数据库。
	if history == nil {
		history = store.NewMemory()
		resources.history = history
	}
	if err := configureServiceCommands(service, cfg, selected, history, offline, workspace, recorder); err != nil {
		return fmt.Errorf("configure commands: %w", err)
	}
	resources.runtime = serviceRuntimeCloser{service: service}
	options.InitialSessionID = service.SessionID()
	stats := service.Session().Statistics()
	options.InitialStats = &stats

	chatModel := tui.NewWithOptions(label, tui.Hooks{
		Command:     func(input string) app.CommandResult { return handleTUIInput(ctx, service, offline, input) },
		Cancel:      service.Cancel,
		FirstAnswer: recorder.UIAnswer,
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
	resources.bridgeDone = bridgeDone
	workspaceCtx, cancelWorkspace := context.WithCancel(ctx)
	resources.cancelWorkspace = cancelWorkspace
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
	resources.workspaceEvents = workspaceEvents
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

// chatResources 集中持有启动过程已经获得的资源。执行停止确认前，不能
// 关闭工具根、trace 或数据库来释放执行权；超时后由进程退出释放句柄和锁。
type chatResources struct {
	timeout         time.Duration
	service         *app.Service
	runtime         io.Closer
	history         io.Closer
	recorder        io.Closer
	pprof           *telemetry.PprofServer
	cancelWorkspace context.CancelFunc
	workspaceEvents <-chan app.WorkspaceStatus
	bridgeDone      chan struct{}
}

func (r *chatResources) close() error {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultSessions().ShutdownTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if r.cancelWorkspace != nil {
		r.cancelWorkspace()
	}
	if r.bridgeDone != nil {
		close(r.bridgeDone)
	}
	if r.service != nil {
		if err := r.service.CloseContext(ctx); err != nil {
			return fmt.Errorf("chat shutdown: history may be not saved/interrupted; execution ownership retained until process exit: %w", err)
		}
	}
	var result error
	// 模型/工具已停止后才按工具根→历史→调试端点→采集→trace 顺序释放。
	// 无 context 的 Close 也受同一截止约束，不能把总期限变成每项期限。
	for _, closer := range []io.Closer{r.runtime, r.history} {
		if closer == nil {
			continue
		}
		result = errors.Join(result, closeWithin(ctx, closer.Close))
		if ctx.Err() != nil {
			return fmt.Errorf("chat shutdown: history may be not saved/interrupted: %w", result)
		}
	}
	if r.pprof != nil {
		result = errors.Join(result, r.pprof.Close(ctx))
	}
	for r.workspaceEvents != nil {
		select {
		case _, ok := <-r.workspaceEvents:
			if !ok {
				r.workspaceEvents = nil
			}
		case <-ctx.Done():
			return fmt.Errorf("chat shutdown: workspace collection interrupted: %w", errors.Join(result, ctx.Err()))
		}
	}
	if r.recorder != nil {
		result = errors.Join(result, closeWithin(ctx, r.recorder.Close))
	}
	if ctx.Err() != nil {
		return fmt.Errorf("chat shutdown: history may be not saved/interrupted: %w", errors.Join(result, ctx.Err()))
	}
	return result
}

// closeWithin 为无 context 的资源关闭提供总退出期限。超时不等待该调用，
// 后台调用仅负责已有关闭动作，不启动新 run；最终由操作系统回收资源。
func closeWithin(ctx context.Context, closeFn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- closeFn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
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
	return buildRuntimeFrom(cfg, selected)
}

// buildRuntimeFrom 按已选定的模型配置装配运行时（`-c` 恢复路径复用，
// 不经过 default_model 选择）。
func buildRuntimeFrom(cfg *config.Config, selected *config.ModelConfig) (*agent.Runtime, string, error) {
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
		_ = runtime.Close()
		return nil, "", fmt.Errorf("configure soul: %w", err)
	}
	if err := configureRuntimeTools(runtime, cfg); err != nil {
		_ = runtime.Close()
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
