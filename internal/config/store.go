package config

import (
	"bytes"
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
	if cfg.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("schema_version is %d, want %d", cfg.SchemaVersion, SchemaVersion)
	}
	if err := cfg.validateDisplayOptions(); err != nil {
		return nil, err
	}
	cfg.fillDefaults()
	updated, changed, err := mergeDefaultsIntoRaw(raw, &cfg)
	if err != nil {
		return nil, fmt.Errorf("encode config defaults: %w", err)
	}
	if changed {
		if err := writeFileAtomic(path, updated, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
		cfg.rawJSON = append([]byte(nil), bytes.TrimSpace(updated)...)
	}
	return &cfg, nil
}

// 合并已有 JSON 而非重建整个结构，避免读取旧配置时丢弃未知扩展字段。
func mergeDefaultsIntoRaw(raw []byte, cfg *Config) ([]byte, bool, error) {
	defaults, err := json.Marshal(cfg)
	if err != nil {
		return nil, false, err
	}
	merged, changed, err := fillMissingRaw(raw, defaults)
	if err != nil {
		return nil, false, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(merged, &root); err != nil {
		return nil, false, err
	}
	// 容量 null 表示自动解析；仅已验证预设可覆盖它，其他标量仍由 fillMissingRaw 保留。
	if len(cfg.Models) > 0 {
		var models []map[string]json.RawMessage
		if err := json.Unmarshal(root["models"], &models); err != nil {
			return nil, false, err
		}
		modelsChanged := false
		for i, m := range cfg.Models {
			if m.ContextWindowTokens == nil || !verifiedDeepSeekPreset(m) {
				continue
			}
			if !bytes.Equal(bytes.TrimSpace(models[i]["context_window_tokens"]), []byte("null")) {
				continue
			}
			models[i]["context_window_tokens"], err = json.Marshal(m.ContextWindowTokens)
			if err != nil {
				return nil, false, err
			}
			modelsChanged = true
		}
		if modelsChanged {
			root["models"], err = json.Marshal(models)
			if err != nil {
				return nil, false, err
			}
			changed = true
		}
	}
	var tui map[string]json.RawMessage
	if len(root["tui"]) > 0 && string(root["tui"]) != "null" {
		if err := json.Unmarshal(root["tui"], &tui); err != nil {
			return nil, false, err
		}
	}
	if tui == nil {
		tui = make(map[string]json.RawMessage)
	}
	status, err := json.Marshal(cfg.TUI.StatusMessages)
	if err != nil {
		return nil, false, err
	}
	var previousMessages map[string][]string
	if err := json.Unmarshal(tui["status_messages"], &previousMessages); err != nil {
		return nil, false, err
	}
	previousStatus, err := json.Marshal(previousMessages)
	if err != nil {
		return nil, false, err
	}
	changed = changed || !bytes.Equal(previousStatus, status)
	tui["status_messages"] = status
	root["tui"], err = json.Marshal(tui)
	if err != nil {
		return nil, false, err
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	return append(updated, '\n'), changed, err
}

// Save 原子写入 config.json：先在同目录写临时文件，fsync 后 rename 覆盖目标。
// 因此崩溃或写入失败时，原有的配置会原封不动保留。
func Save(cfg *Config) error {
	if cfg.SchemaVersion == SchemaVersion {
		cfg.fillDefaults()
	}
	if err := cfg.validateDisplayOptions(); err != nil {
		return err
	}
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if len(cfg.rawJSON) > 0 {
		raw, err = preserveUnknownFields(cfg.rawJSON, raw, configShape)
		if err != nil {
			return fmt.Errorf("preserve decoded config extensions: %w", err)
		}
	}
	if previous, err := os.ReadFile(path); err == nil {
		raw, err = preserveUnknownFields(previous, raw, configShape)
		if err != nil {
			return fmt.Errorf("preserve config extensions: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s before save: %w", path, err)
	}
	raw, err = json.MarshalIndent(json.RawMessage(raw), "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	cfg.rawJSON = append([]byte(nil), bytes.TrimSpace(raw)...)
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
