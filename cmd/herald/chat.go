package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"herald-agent/internal/agent"
	"herald-agent/internal/app"
	"herald-agent/internal/config"
	"herald-agent/internal/model"
	"herald-agent/internal/provider"
	"herald-agent/internal/tui"
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
			modelID := modelFlag
			if modelID == "" {
				cfg, err := config.Load()
				if err != nil {
					return fmt.Errorf("load config: %w (run `herald setup` first)", err)
				}
				if cfg.DefaultModel == "" {
					return errors.New("no default_model in config (run `herald setup`)")
				}
				modelID = cfg.DefaultModel
			}
			return startChat(out, modelID, false)
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
		return errors.New("chat needs an interactive terminal (run it inside a TTY; try `herald chat --offline` for scripted output)")
	}

	var runtime *agent.Runtime
	label := "fake/offline"
	if offline {
		runtime = agent.New(model.NewLoopFake(model.FakeScript{Response: &model.ChatResponse{}}), "offline")
	} else {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		selected, err := selectModelConfig(cfg, modelFlag)
		if err != nil {
			return err
		}
		factory := provider.NewModelFactory(provider.NewRegistry(), credentialSource{})
		client, err := factory.Build(provider.ModelSpec{
			Provider:  selected.Provider,
			Protocol:  selected.Protocol,
			BaseURL:   selected.BaseURL,
			Model:     selected.Model,
			APIKeyRef: selected.APIKeyRef,
		})
		if err != nil {
			return fmt.Errorf("build model client: %w", err)
		}
		runtime = agent.New(client, selected.ID)
		label = fmt.Sprintf("%s/%s", selected.Provider, selected.Model)
	}

	service := app.NewService(runtime)
	defer service.Close()
	ctx := context.Background()

	chatModel := tui.New(label, tui.Hooks{
		Submit:       func(input string) (string, error) { return service.Submit(ctx, input) },
		Cancel:       service.Cancel,
		ResetSession: func() { service.Session().Reset() },
	})

	// 欢迎行必须在包装 TeaModel 之前追加：AddSystemLine 改的是本地值，
	// 包装后再改不会反映进 program 持有的状态。
	chatModel.AddSystemLine(welcome(label, offline))
	program := tea.NewProgram(&tui.TeaModel{M: chatModel}, tea.WithAltScreen())
	// 事件桥：app 事件投递进 Bubble Tea 主循环。channel 无关闭约定，
	// 桥随进程退出回收（有界缓冲 + UI 持续消费）。
	go func() {
		for {
			event, ok := <-service.Events()
			if !ok {
				return
			}
			program.Send(tui.AppEvent{Event: event})
		}
	}()

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("chat ui: %w", err)
	}
	return nil
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

func welcome(label string, offline bool) string {
	if offline {
		return "Welcome to herald chat — OFFLINE mode with the scripted fake. " +
			"Esc cancels a run, Ctrl+N starts a new session, Ctrl+C quits."
	}
	return "Welcome to herald chat (" + label + "). " +
		"Esc cancels a run, Ctrl+N starts a new session, Ctrl+C quits."
}
