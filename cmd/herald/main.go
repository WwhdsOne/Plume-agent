// Command herald 是本地入口。从 G2a 起它还会承载微信网关命令：
// gateway setup/start/status。
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"herald-agent/internal/channel"
	"herald-agent/internal/config"
	"herald-agent/internal/provider"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "herald: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		usage(stdout)
		hintIfUnconfigured(stdout)
		return nil
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "herald-agent (%s)\n", runtime.Version())
		return nil
	case "config":
		return runConfig(args[1:], stdout)
	case "setup":
		// 与 setup trace 一起在 G1a-2 交付。
		return errors.New("setup is not implemented yet (planned for G1a-2)")
	case "help", "-h", "--help":
		usage(stdout)
		return nil
	default:
		usage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `herald-agent

Usage:
  herald setup              run the first-time setup wizard (not implemented yet)
  herald config path        print the configuration directory and file path
  herald config show        print the saved configuration with secrets redacted
  herald version            print the version

Configuration lives in $HERALD_HOME, or ~/.herald by default.
`)
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
