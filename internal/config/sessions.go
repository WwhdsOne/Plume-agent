package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// HistoryConfig 是 sessions.history 段：持久化开关与数据库位置。
// Enabled 为 nil 表示默认开启；显式 false 保留并改用进程内存后端，
// 不删除已有数据。Path 相对配置目录，允许明确绝对路径。
type HistoryConfig struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Path    string `json:"path,omitempty"`
}

// SessionsConfig 是 sessions 段：历史持久化、目录/正文分页与退出收尾期限。
// 反序列化先落默认再覆盖；缺失/null 补默认，未知字段原样保留。
type SessionsConfig struct {
	History           *HistoryConfig `json:"history,omitempty"`
	PageSize          int            `json:"page_size,omitempty"`
	ShutdownTimeoutMS int            `json:"shutdown_timeout_ms,omitempty"`
}

// DefaultSessions 返回 G4a.1 约定的默认值：持久化开启、数据库在配置目录
// sessions/history.db、每页 20 项、退出收尾 5000ms。
func DefaultSessions() *SessionsConfig {
	enabled := true
	return &SessionsConfig{History: &HistoryConfig{Enabled: &enabled, Path: "sessions/history.db"}, PageSize: 20, ShutdownTimeoutMS: 5000}
}

// UnmarshalJSON 先落默认再按字段覆盖；history 为 null 时保留默认，
// 最后统一 Validate。
func (c *SessionsConfig) UnmarshalJSON(data []byte) error {
	*c = *DefaultSessions()
	raw, err := decodeSessionFields(data, "sessions", map[string]any{
		"page_size": &c.PageSize, "shutdown_timeout_ms": &c.ShutdownTimeoutMS,
	})
	if err != nil {
		return err
	}
	if value, ok := raw["history"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, c.History); err != nil {
			return err
		}
	}
	return c.Validate()
}

// UnmarshalJSON 先落默认再覆盖；enabled 显式 false 保留，缺失/null 补默认 true。
func (h *HistoryConfig) UnmarshalJSON(data []byte) error {
	*h = *DefaultSessions().History
	raw, err := decodeSessionFields(data, "sessions.history", map[string]any{"path": &h.Path})
	if err != nil {
		return err
	}
	if value, ok := raw["enabled"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			return fmt.Errorf("sessions.history.enabled: invalid type")
		}
		h.Enabled = &enabled
	}
	return h.Validate()
}

// decodeSessionFields 只对 sessions 的已知标量采用 null=缺失；其他配置段
// 的严格 null 语义继续由 decodeDisplayFields 守住，未知字段不参与解码。
func decodeSessionFields(data []byte, path string, fields map[string]any) (map[string]json.RawMessage, error) {
	raw, err := decodeDisplayFields(data, path, nil)
	if err != nil {
		return nil, err
	}
	for name, target := range fields {
		if value, ok := raw[name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			if err := json.Unmarshal(value, target); err != nil {
				return nil, fmt.Errorf("%s.%s: invalid type", path, name)
			}
		}
	}
	return raw, nil
}

// dropNullSessionFields 仅清除 sessions 已知 null 标量，交给缺失字段合并
// 实际写入默认值；显式 false、有效用户值与未知扩展（包括 null）都保留。
func dropNullSessionFields(data []byte) ([]byte, bool, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, err
	}
	var section map[string]json.RawMessage
	if err := json.Unmarshal(root["sessions"], &section); err != nil || section == nil {
		return data, false, nil
	}
	changed := false
	for _, name := range []string{"page_size", "shutdown_timeout_ms"} {
		if bytes.Equal(bytes.TrimSpace(section[name]), []byte("null")) {
			delete(section, name)
			changed = true
		}
	}
	var history map[string]json.RawMessage
	if err := json.Unmarshal(section["history"], &history); err == nil && history != nil {
		historyChanged := false
		for _, name := range []string{"enabled", "path"} {
			if bytes.Equal(bytes.TrimSpace(history[name]), []byte("null")) {
				delete(history, name)
				historyChanged = true
			}
		}
		if historyChanged {
			encoded, err := json.Marshal(history)
			if err != nil {
				return nil, false, err
			}
			section["history"] = encoded
			changed = true
		}
	}
	if !changed {
		return data, false, nil
	}
	encoded, err := json.Marshal(section)
	if err != nil {
		return nil, false, err
	}
	root["sessions"] = encoded
	encoded, err = json.Marshal(root)
	return encoded, true, err
}

// Validate 校验路径与数值范围；越界报配置错误，不静默改成默认。
func (c *SessionsConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	if c.History == nil {
		errs = append(errs, errors.New("sessions.history: required"))
	} else if err := c.History.Validate(); err != nil {
		errs = append(errs, err)
	}
	if c.PageSize < 1 || c.PageSize > 100 {
		errs = append(errs, errors.New("sessions.page_size: expected integer in 1..100"))
	}
	if c.ShutdownTimeoutMS < 1000 || c.ShutdownTimeoutMS > 60000 {
		errs = append(errs, errors.New("sessions.shutdown_timeout_ms: expected integer in 1000..60000"))
	}
	return errors.Join(errs...)
}

// Validate 校验 history 段自身：路径非空、不含控制字符、不指向目录本身。
func (h *HistoryConfig) Validate() error {
	if h == nil {
		return errors.New("sessions.history: required")
	}
	var errs []error
	if h.Enabled == nil {
		enabled := true
		h.Enabled = &enabled
	}
	if strings.TrimSpace(h.Path) == "" || strings.ContainsFunc(h.Path, unicode.IsControl) {
		errs = append(errs, errors.New("sessions.history.path: expected nonempty path without control characters"))
	} else if strings.HasSuffix(strings.TrimSpace(h.Path), string(filepath.Separator)) || strings.TrimSpace(h.Path) == "." {
		errs = append(errs, errors.New("sessions.history.path: expected file path, not a directory"))
	}
	return errors.Join(errs...)
}

// HistoryEnabled 报告持久化是否开启；nil 段按默认开启处理。
func (c *Config) HistoryEnabled() bool {
	if c == nil || c.Sessions == nil || c.Sessions.History == nil || c.Sessions.History.Enabled == nil {
		return true
	}
	return *c.Sessions.History.Enabled
}

// HistoryDatabasePath 把 sessions.history.path 解析为绝对路径：相对路径
// 基于配置目录（PLUME_HOME），绝不在工作区写隐含数据库。
func (c *Config) HistoryDatabasePath() (string, error) {
	if c == nil || c.Sessions == nil || c.Sessions.History == nil || c.Sessions.History.Path == "" {
		return "", errors.New("sessions.history.path: not configured")
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	path := c.Sessions.History.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path), nil
}
