package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"plume-agent/internal/config"
	"plume-agent/internal/setup"
	"plume-agent/internal/telemetry"
)

func newSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "运行首次设置向导（需要交互式终端）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

// runSetup 运行首次设置向导。
//
// 它要求 stdin 是终端：非交互终端下 huh 会挂住或直接失败，所以这里先判断并给出
// 明确提示后退出，而不是等待按键（见 docs/phase-01-weixin-agent.md §2）。
func runSetup(stdin io.Reader, stdout, stderr io.Writer) error {
	inFile, isFile := stdin.(*os.File)
	if !isFile || !isTerminal(inFile) {
		path, _ := config.Path()
		return fmt.Errorf("setup needs an interactive terminal, but stdin is not a TTY; run `plume setup` in a shell (config path: %s)", path)
	}
	outFile, isOutFile := stdout.(*os.File)
	if !isOutFile {
		outFile = os.Stdout
	}

	trace, tracePath, err := openSetupTrace()
	if err != nil {
		// trace 打不开不该阻止用户完成设置，但要让用户知道这次没有 trace。
		fmt.Fprintf(stderr, "plume: 无法写入 setup trace: %v\n", err)
	}
	var recorder *telemetry.SetupRecorder
	if trace != nil {
		recorder = telemetry.NewSetupRecorder(trace)
		recorder.SetCloser(trace)
		defer func() { _ = recorder.Close() }()
	} else {
		recorder = telemetry.NewSetupRecorder(io.Discard)
	}

	existing, err := config.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	providers, channels := newRegistries()
	wizard := setup.New(providers, channels, newPrompter(inFile, outFile), recorder)

	result, runErr := wizard.Run(existing)
	switch {
	case errors.Is(runErr, setup.ErrCancelled):
		fmt.Fprintln(stdout, "\n已取消，磁盘上未写入任何内容。")
		return nil
	case errors.Is(runErr, setup.ErrChannelSkipped):
		fmt.Fprintln(stdout, "\n渠道步骤已跳过。模型配置已保存，可以先做本地调试；稍后重跑 `plume setup` 继续。")
	case runErr != nil:
		return runErr
	}

	printSetupSummary(stdout, result, tracePath)
	return nil
}

// openSetupTrace 以追加方式打开 ~/.plume/logs/setup.jsonl。
func openSetupTrace() (*os.File, string, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, "", err
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(logDir, "setup.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, "", err
	}
	return f, path, nil
}

func printSetupSummary(w io.Writer, result *setup.Result, tracePath string) {
	fmt.Fprintln(w, "\n配置完成：")
	fmt.Fprintf(w, "  模型:     %s (%s, %s)\n", result.ModelID, result.Config.Models[0].Provider, result.Config.Models[0].Model)
	fmt.Fprintf(w, "  Base URL: %s\n", result.Config.Models[0].BaseURL)

	if result.CredentialRef != "" {
		fmt.Fprintf(w, "  凭据:     已写入 credentials/%s\n", result.CredentialRef)
	} else if ref := result.Config.Models[0].APIKeyRef; ref != "" {
		fmt.Fprintf(w, "  凭据:     沿用已有 credentials/%s\n", ref)
	} else {
		fmt.Fprintln(w, "  凭据:     未设置（该服务标记为无需 API Key）")
	}

	if result.ChannelID != "" {
		fmt.Fprintf(w, "  渠道:     %s（待登录；真实扫码在 G2a.1 实现）\n", result.Config.Channels[0].Type)
	}
	if tracePath != "" {
		fmt.Fprintf(w, "  trace:    %s\n", tracePath)
	}
	fmt.Fprintln(w, "\n查看当前配置: plume config show")
}

// isTerminal 判断文件是不是真正的终端。
//
// 刻意不用 os.ModeCharDevice：/dev/null 也是字符设备，却不是一个终端，
// 用它判断会让 `plume setup < /dev/null` 进入 huh 并挂住。
func isTerminal(f *os.File) bool {
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
