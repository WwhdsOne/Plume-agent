package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ErrInvalidRef 表示凭据引用不是单个安全的路径元素。
var ErrInvalidRef = errors.New("invalid credential reference")

// CredentialsDir 返回凭据目录，即 Dir 下的子目录。
func CredentialsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials"), nil
}

// EnsureCredentialsDir 在需要时以 0700 创建凭据目录；若已存在的目录权限过宽，
// 它只**报告**，不静默收紧。
func EnsureCredentialsDir() ([]string, error) {
	dir, err := CredentialsDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create credentials directory: %w", err)
	}

	// Windows 没有有意义的 POSIX 权限位；检查会导致每次运行都告警。
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return []string{fmt.Sprintf("%s has permissions %04o, want 0700 (fix with: chmod 700 %s)", dir, perm, dir)}, nil
	}
	return nil, nil
}

// SetCredential 以文件权限 0600 把密钥存到 ref 下，并原子替换旧值。
// 空密钥会被拒绝而不是写入，避免"未设置"被误当成"已设置"。
func SetCredential(ref string, secret []byte) error {
	if err := checkCredentialRef(ref); err != nil {
		return err
	}
	if len(secret) == 0 {
		return errors.New("refusing to store an empty credential")
	}
	if _, err := EnsureCredentialsDir(); err != nil {
		return err
	}
	dir, err := CredentialsDir()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, ref), secret, 0o600)
}

// GetCredential 返回 ref 下存放的密钥。
func GetCredential(ref string) ([]byte, error) {
	if err := checkCredentialRef(ref); err != nil {
		return nil, err
	}
	dir, err := CredentialsDir()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, ref))
}

// CredentialExists 报告 ref 是否已存放密钥。
func CredentialExists(ref string) (bool, error) {
	if err := checkCredentialRef(ref); err != nil {
		return false, err
	}
	dir, err := CredentialsDir()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, ref))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// DeleteCredential 删除 ref 下存放的密钥。删除不存在的引用不算错误。
func DeleteCredential(ref string) error {
	if err := checkCredentialRef(ref); err != nil {
		return err
	}
	dir, err := CredentialsDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, ref)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func checkCredentialRef(ref string) error {
	if !ValidCredentialRef(ref) {
		return fmt.Errorf("%w: %q", ErrInvalidRef, ref)
	}
	return nil
}
