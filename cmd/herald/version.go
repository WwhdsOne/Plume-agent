package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// 这三个变量由 scripts/build.sh 通过 -ldflags -X 注入。
// 直接用 `go build` / `go run` 构建时保持下面的默认值，`herald version` 会诚实地说
// 这是一个未打戳的构建，而不是伪装成某个版本。
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本、commit 与构建时间",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(cmd.OutOrStdout(), versionString())
			return nil
		},
	}
}

func versionString() string {
	label := version
	if label != "dev" {
		label = "v" + label
	}
	return fmt.Sprintf("herald-agent %s\ncommit  %s\nbuilt   %s\ngo      %s\nplatform %s/%s\n",
		label, commit, buildTime, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
