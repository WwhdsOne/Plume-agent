//go:build !unix

package tools

import (
	"os"
	"os/exec"
)

func openWorkspaceFile(root *os.Root, path string) (*os.File, error) { return root.Open(path) }

// 非 Unix 系统不提供相同的目录 fsync 与进程组保证，仅关闭文件和直接进程。
func syncWorkspaceDirectory(root *os.Root, path string) error { return nil }
func configureProcessGroup(cmd *exec.Cmd)                     { cmd.Cancel = func() error { return killProcessGroup(cmd) } }
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
