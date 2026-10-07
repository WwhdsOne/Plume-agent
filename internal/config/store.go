package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Dir 返回 plume 的配置目录，位置在仓库之外：
//
//  1. 设置了 $PLUME_HOME 时用它（展开 ~ 与 $VAR），
//  2. 否则 POSIX 下用 ~/.plume，
//  3. 否则 Windows 下用 %LOCALAPPDATA%\plume。
//
// 刻意不使用 os.UserConfigDir()：它在 macOS 上解析为
// ~/Library/Application Support，既与 Hermes/Codex 的惯例不同，路径里还带空格。
// 见 docs/decisions/0001-scope.md §2。
func Dir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("PLUME_HOME")); v != "" {
		return filepath.Clean(expandPath(v)), nil
	}

	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Join(local, "plume"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		return filepath.Join(home, "AppData", "Local", "plume"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".plume"), nil
}

// Path 返回 config.json 的绝对路径。
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load 读取并解析 config.json。文件不存在时按 fs.ErrNotExist 报错（用 errors.Is 判断），
// 这正是向导"开始首次设置"的信号。
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save 原子写入 config.json：先在同目录写临时文件，fsync 后 rename 覆盖目标。
// 因此崩溃或写入失败时，原有的配置会原封不动保留。
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeFileAtomic 用 data 替换 path，且绝不会在 path 上留下半个文件。
// 假定父目录已存在。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// rename 成功后这次删除是无害的空操作；它保证每条提前返回的路径都能清理掉临时文件。
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// fsync 目录，让 rename 本身也能扛住崩溃。
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// expandPath 展开开头的 ~ 以及所有 $VAR 引用。
func expandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}
