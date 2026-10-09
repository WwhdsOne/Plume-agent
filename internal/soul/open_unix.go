//go:build unix

package soul

import (
	"os"
	"syscall"
)

// openSoulFile 防止常规文件在路径检查后被替换成无 writer 的 FIFO，导致
// Open 永久等待。常规文件忽略 O_NONBLOCK；实际类型仍由 Read 的 Stat 拒绝。
func openSoulFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
