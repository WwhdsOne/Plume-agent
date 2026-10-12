//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

// openPrivateFile 使用 O_NOFOLLOW 拒绝检查后被替换的符号链接，并以文件
// 描述符检查常规文件及硬链接数。硬链接没有唯一规范路径，首版明确拒绝。
func openPrivateFile(path string, create bool) (*os.File, error) {
	flags := syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
	if create {
		flags |= syscall.O_CREAT
	}
	fd, err := syscall.Open(path, flags, 0o600)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		if !info.Mode().IsRegular() {
			err = fmt.Errorf("history file is not regular: %s", path)
		} else if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
			err = fmt.Errorf("history file hard links are not supported: %s", path)
		}
	}
	if err == nil {
		err = file.Chmod(0o600)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
