// Command herald 是本地入口：终端输入 `herald` 直接打开聊天 TUI
// （模仿 Hermes 的零参数体验）；`herald setup` 首次配置；G2a 起它还会
// 承载微信网关命令 gateway setup/start/status。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"herald-agent/internal/channel"
	"herald-agent/internal/config"
	"herald-agent/internal/provider"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "herald: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "herald",
		Short: "herald-agent：终端聊天优先的个人 Agent（输入 herald 直接进入聊天）",
		// 错误由 main 统一打印一次；业务失败不该顺带打印整篇用法说明。
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			cfg, err := config.Load()
			switch {
			case errors.Is(err, os.ErrNotExist):
				// 还没配置过：给用法和向导提示。
				if herr := cmd.Help(); herr != nil {
					return herr
				}
				hintIfUnconfigured(out)
				return nil
			case err != nil:
				return fmt.Errorf("load config: %w", err)
			}
			if cfg.DefaultModel == "" {
				return errors.New("config has no default_model; run `herald setup`")
			}
			if f, ok := out.(*os.File); ok && !isatty.IsTerminal(f.Fd()) && !isatty.IsCygwinTerminal(f.Fd()) {
				return errors.New("herald needs an interactive terminal; try `herald chat --offline` for scripted output")
			}
			// 配置就绪且在 TTY：模仿 Hermes，零参数直接进入聊天。
			return startChat(out, "", false)
		},
	}
	root.AddCommand(newSetupCmd(), newChatCmd(), newConfigCmd(), newVersionCmd())
	return root
}

// hintIfUnconfigured 把首次使用者引向向导，而不是只留一段干巴巴的用法说明。
func hintIfUnconfigured(w io.Writer) {
	if _, err := config.Load(); errors.Is(err, os.ErrNotExist) {
		if path, perr := config.Path(); perr == nil {
			fmt.Fprintf(w, "\nNo configuration found at %s. Run `herald setup` to create one.\n", path)
		}
	}
}

// newRegistries 构建静态注册表。它们今天是只读数据，但把构建集中在一处，意味着注入
// 测试用的供应商/渠道无需改动命令分发。
func newRegistries() (*provider.Registry, *channel.Registry) {
	return provider.NewRegistry(), channel.NewRegistry()
}
