//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// processLock 是整库进程独占锁。锁文件与数据库分离：SQLite 自身使用 POSIX
// 记录锁（fcntl），与 BSD flock 在同一文件上混用是平台相关的未定义行为，
// 因此独占语义落在独立的 <db>.lock 文件上，由操作系统在进程退出或崩溃后释放。
type processLock struct {
	file *os.File
}

// acquireLock 以非阻塞方式获取锁文件；被占用返回 ErrHistoryInUse，
// 不按超时抢占仍执行工具的进程。
func acquireLock(path string) (*processLock, error) {
	file, err := openPrivateFile(path, true)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
			return nil, fmt.Errorf("%w: %s", ErrHistoryInUse, path)
		}
		return nil, err
	}
	return &processLock{file: file}, nil
}

// Release 释放锁但保留锁文件 inode；删除后重建会让两个进程各持一份锁。
func (l *processLock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}
