// Package soul 管理用户的人格文件。它只初始化和读取文本，不解析 Markdown、
// 不授予工具权限，也不负责把人格拼入模型请求。
package soul

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DefaultTemplate 是首次 setup 使用的简短人格，用户可直接编辑生成的文件。
//
//go:embed default.md
var DefaultTemplate string

// Ensure 仅在 path 不存在时初始化人格文件。既有文件（包括空文件和指向
// 常规文件的符号链接）原样保留；目录、设备或悬空链接不能当作人格文件。
// 返回 created=true 表示本次发布了完整模板，而非只创建了临时文件。
func Ensure(path string) (created bool, err error) {
	if path == "" {
		return false, errors.New("soul path is empty")
	}
	if exists, err := existingRegular(path); err != nil || exists {
		return false, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create soul directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".soul-tmp-*")
	if err != nil {
		return false, fmt.Errorf("create soul temporary file: %w", err)
	}
	// 每条失败路径和并发失败发布都清理临时文件；既有目标从不被覆盖。
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	if err := tmp.Chmod(0o600); err != nil {
		return false, fmt.Errorf("set soul file permissions: %w", err)
	}
	if _, err := tmp.WriteString(DefaultTemplate); err != nil {
		return false, fmt.Errorf("write soul template: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return false, fmt.Errorf("sync soul template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close soul template: %w", err)
	}
	// 与 Rename 不同，Link 在目标已存在时失败。并发 setup 或用户刚刚创建
	// 文件时，只会保留先发布者的内容，不会把个性化文本换成默认模板。
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			_, existingErr := existingRegular(path)
			return false, existingErr
		}
		return false, fmt.Errorf("publish soul template: %w", err)
	}
	_ = os.Remove(tmp.Name())
	// 与配置原子写入一致：尽力同步目录；部分平台不支持目录 Sync。
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return true, nil
}

// Read 读取一次人格快照。空路径表示关闭；缺失、空文件或纯空白返回空文本。
// 只接受 UTF-8 常规文件，并最多读取 maxBytes+1 字节来检测超限；原始格式
// 保留给拼装层使用。错误不包含文件正文，ctx 在读取前后均会检查。
func Read(ctx context.Context, path string, maxBytes int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if maxBytes <= 0 {
		return "", errors.New("soul byte limit must be positive")
	}
	exists, err := existingRegular(path)
	if err != nil || !exists {
		return "", err
	}
	file, err := openSoulFile(path)
	if err != nil {
		return "", fmt.Errorf("open soul file: %w", err)
	}
	defer func() { _ = file.Close() }()
	// 再核对实际打开的文件类型，避免路径检查后替换成目录或设备。
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect open soul file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("soul path must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", fmt.Errorf("read soul file: %w", err)
	}
	if len(data) > maxBytes {
		return "", errors.New("soul file exceeds configured byte limit")
	}
	if !utf8.Valid(data) {
		return "", errors.New("soul file must contain valid UTF-8")
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", nil
	}
	return string(data), nil
}

// existingRegular 同时检查链接本身与目标；缺文件可跳过，悬空链接不可覆盖。
func existingRegular(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect soul file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return false, errors.New("soul symlink must point to a regular file")
		}
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("soul path must be a regular file")
	}
	return true, nil
}
