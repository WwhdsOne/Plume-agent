package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// canonicalDatabasePath 固定既有文件和父目录的真实绝对路径，使符号链接
// 别名共享同一锁。只创建缺失目录，不 chmod 用户已有的公共父目录。
func canonicalDatabasePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// 悬空文件符号链接不等于不存在的新文件，避免创建时改变链接目标身份。
	if _, statErr := os.Lstat(abs); statErr == nil {
		return "", fmt.Errorf("history database path is a dangling symlink: %s", abs)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// prepareDatabaseFiles 在驱动访问前安全创建/校验文件。SQLite 的新 journal
// 权限继承数据库0600；既有辅助文件先收紧且拒绝链接，避免恢复日志泄漏正文。
func prepareDatabaseFiles(path string) error {
	file, err := openPrivateFile(path, true)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		aux, err := openPrivateFile(path+suffix, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := aux.Close(); err != nil {
			return err
		}
	}
	return nil
}
