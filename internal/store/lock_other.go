//go:build !unix

package store

import "fmt"

// processLock 在未验证锁语义的平台（Windows）上不可用：方案约定不把
// 编译通过当成实测，未支持可靠锁的平台明确拒绝开启持久后端，不静默降级。
type processLock struct{}

func acquireLock(path string) (*processLock, error) {
	return nil, fmt.Errorf("history persistence requires a platform with verified file locking: %s", path)
}

func (l *processLock) Release() {}
