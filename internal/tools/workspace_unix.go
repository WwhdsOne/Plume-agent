//go:build unix

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// openWorkspaceFile 使用非阻塞打开，避免 Stat 后文件被替换成 FIFO 导致读取永久等待。
// 调用方仍需检查打开后的文件类型；普通文件读写不会因为该标记变成异步 I/O。
func openWorkspaceFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
func syncWorkspaceDirectory(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// configureProcessGroup 让命令建立自己的进程组，使取消覆盖通常的子进程树。
// 主动 setsid/setpgid 脱离该组的进程不在保证范围内。
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}

// killProcessGroup 使用负 PID 终止整个组；组已消失视为清理完成，而非操作失败。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
