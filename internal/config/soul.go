package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// SoulConfig 控制人格文件的读取；正文和文件操作由 soul 包负责，配置只保存路径。
type SoulConfig struct {
	Enabled  bool   `json:"enabled"`
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes"`
}

// DefaultSoul 返回独立的默认配置，Save/Load 会将三个字段实际写入 config.json。
func DefaultSoul() *SoulConfig {
	return &SoulConfig{Enabled: true, Path: "soul.md", MaxBytes: 64 << 10}
}

// SoulConfig 返回人格设置；缺失的旧配置与 nil Agent 使用同一套默认值。
func (c *AgentConfig) SoulConfig() *SoulConfig {
	if c == nil || c.Soul == nil {
		return DefaultSoul()
	}
	return c.Soul
}

// UnmarshalJSON 只把缺失/null 视作默认；false 和手填路径、大小必须保留。
func (c *SoulConfig) UnmarshalJSON(data []byte) error {
	*c = *DefaultSoul()
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return errors.New("agent.soul: expected object")
	}
	for name, target := range map[string]any{"enabled": &c.Enabled, "path": &c.Path, "max_bytes": &c.MaxBytes} {
		value, ok := raw[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		if json.Unmarshal(value, target) != nil {
			return fmt.Errorf("agent.soul.%s: invalid type", name)
		}
	}
	return c.Validate()
}

// Validate 只校验配置形态，不读取路径、不检查文件是否已初始化。
func (c *SoulConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	if strings.TrimSpace(c.Path) == "" || strings.ContainsFunc(c.Path, unicode.IsControl) {
		errs = append(errs, errors.New("agent.soul.path: expected nonempty path without control characters"))
	}
	if c.MaxBytes < 1 || c.MaxBytes > 8<<20 {
		errs = append(errs, errors.New("agent.soul.max_bytes: expected integer in 1..8388608"))
	}
	return errors.Join(errs...)
}

// ResolvePath 展开 ~/环境变量后，以配置目录解释相对路径，再规范为绝对路径。
// 禁用时立即返回空串，因此不会解析目录或读取人格文件。
func (c *SoulConfig) ResolvePath() (string, error) {
	if c == nil {
		c = DefaultSoul()
	}
	if !c.Enabled {
		return "", nil
	}
	if err := c.Validate(); err != nil {
		return "", err
	}
	path := expandPath(os.ExpandEnv(c.Path))
	if strings.TrimSpace(path) == "" || strings.ContainsFunc(path, unicode.IsControl) {
		return "", errors.New("agent.soul.path: expanded path is empty or contains control characters")
	}
	if !filepath.IsAbs(path) {
		dir, err := Dir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, path)
	}
	return filepath.Abs(path)
}

// dropNullSoulFields 仅清除人格的已知 null 标量，让既有缺失字段合并补入默认。
// 其他配置的 null 语义和人格未知扩展保持原样，避免扩大迁移范围。
func dropNullSoulFields(data []byte) ([]byte, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, err
	}
	var agent map[string]json.RawMessage
	if err := json.Unmarshal(root["agent"], &agent); err != nil || agent == nil {
		return data, false, nil
	}
	var soul map[string]json.RawMessage
	if err := json.Unmarshal(agent["soul"], &soul); err != nil || soul == nil {
		return data, false, nil
	}
	changed := false
	for _, name := range []string{"enabled", "path", "max_bytes"} {
		if bytes.Equal(bytes.TrimSpace(soul[name]), []byte("null")) {
			delete(soul, name)
			changed = true
		}
	}
	if !changed {
		return data, false, nil
	}
	encoded, err := json.Marshal(soul)
	if err != nil {
		return nil, false, err
	}
	agent["soul"] = encoded
	encoded, err = json.Marshal(agent)
	if err != nil {
		return nil, false, err
	}
	root["agent"] = encoded
	encoded, err = json.Marshal(root)
	return encoded, true, err
}
