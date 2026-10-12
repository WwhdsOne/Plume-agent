//go:build !unix

package store

import (
	"fmt"
	"os"
)

// 持久化只支持已验证的 unix 文件身份与进程锁；其他平台不弱化安全约束。
func openPrivateFile(path string, _ bool) (*os.File, error) {
	return nil, fmt.Errorf("history persistence requires verified file identity: %s", path)
}
