package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// StatusLineConfig 描述底部状态栏，不包含模型或终端运行时类型。
type StatusLineConfig struct {
	Enabled              bool               `json:"enabled"`
	MaxRows              int                `json:"max_rows"`
	Separator            string             `json:"separator"`
	Unknown              string             `json:"unknown"`
	ClockRefreshMS       int                `json:"clock_refresh_ms"`
	EnvironmentRefreshMS int                `json:"environment_refresh_ms"`
	GitTimeoutMS         int                `json:"git_timeout_ms"`
	TokenFormat          string             `json:"token_format"`
	TimeFormat           string             `json:"time_format"`
	ContextFormat        string             `json:"context_format"`
	ContextBar           ContextBarConfig   `json:"context_bar"`
	CacheFormat          string             `json:"cache_format"`
	CacheScope           string             `json:"cache_scope"`
	CacheBar             CacheBarConfig     `json:"cache_bar"`
	Items                []StatusItemConfig `json:"items"`
}

// ContextBarConfig 控制容量占用条的样式和告警阈值。
type ContextBarConfig struct {
	Width           int    `json:"width"`
	ShowPercent     bool   `json:"show_percent"`
	Style           string `json:"style"`
	WarningPercent  int    `json:"warning_percent"`
	CriticalPercent int    `json:"critical_percent"`
}

// CacheBarConfig 控制命中率条体，命中越高越好，不复用上下文告警阈值。
type CacheBarConfig struct {
	Width int    `json:"width"`
	Style string `json:"style"`
}

// StatusItemConfig 的数组位置即显示顺序；显式空数组不补回默认字段。
type StatusItemConfig struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Enabled  bool   `json:"enabled"`
	Row      int    `json:"row"`
	Priority int    `json:"priority"`
}

// DefaultStatusLine 每次返回独立配置，避免离线和程序化调用共享可变切片。
func DefaultStatusLine() *StatusLineConfig {
	return &StatusLineConfig{
		Enabled: true, MaxRows: 2, Separator: " │ ", Unknown: "show",
		ClockRefreshMS: 1000, EnvironmentRefreshMS: 5000, GitTimeoutMS: 500,
		TokenFormat: "compact", TimeFormat: "compact", ContextFormat: "usage", CacheFormat: "bar", CacheScope: "session",
		ContextBar: ContextBarConfig{Width: 10, ShowPercent: true, Style: "unicode", WarningPercent: 80, CriticalPercent: 95},
		CacheBar:   CacheBarConfig{Width: 10, Style: "unicode"},
		Items: []StatusItemConfig{
			{ID: "provider", Label: "Provider", Enabled: true, Row: 1, Priority: 60},
			{ID: "model", Enabled: true, Row: 1, Priority: 90},
			{ID: "reasoning", Label: "think", Enabled: true, Row: 1, Priority: 50},
			{ID: "context", Label: "ctx", Enabled: false, Row: 1, Priority: 80},
			{ID: "cache", Label: "cache", Enabled: true, Row: 1, Priority: 40},
			{ID: "git", Label: "git", Enabled: true, Row: 2, Priority: 50},
			{ID: "uv_env", Label: "env", Enabled: true, Row: 2, Priority: 30},
			{ID: "session_elapsed", Label: "session", Enabled: true, Row: 2, Priority: 60},
			{ID: "phase", Enabled: true, Row: 2, Priority: 100},
			{ID: "run_elapsed", Enabled: true, Row: 2, Priority: 100},
			{ID: "last_usage", Label: "usage(last)", Row: 1, Priority: 40},
			{ID: "session_usage", Label: "usage(session)", Row: 1, Priority: 40},
			{ID: "run_id", Label: "run", Row: 2, Priority: 20},
			{ID: "cwd", Label: "cwd", Row: 2, Priority: 20},
		},
	}
}

// UnmarshalJSON 先按具体默认值初始化，再仅覆盖出现的键，保留 false/0/空字符串。
func (c *StatusLineConfig) UnmarshalJSON(data []byte) error {
	parsed := DefaultStatusLine()
	raw, err := decodeDisplayFields(data, "tui.status_line", map[string]any{
		"enabled": &parsed.Enabled, "max_rows": &parsed.MaxRows, "separator": &parsed.Separator,
		"unknown": &parsed.Unknown, "clock_refresh_ms": &parsed.ClockRefreshMS,
		"environment_refresh_ms": &parsed.EnvironmentRefreshMS, "git_timeout_ms": &parsed.GitTimeoutMS,
		"token_format": &parsed.TokenFormat, "time_format": &parsed.TimeFormat, "context_format": &parsed.ContextFormat,
		"cache_format": &parsed.CacheFormat, "cache_scope": &parsed.CacheScope,
	})
	if err != nil {
		return err
	}
	if value, ok := raw["context_bar"]; ok {
		if _, err := decodeDisplayFields(value, "tui.status_line.context_bar", map[string]any{
			"width": &parsed.ContextBar.Width, "show_percent": &parsed.ContextBar.ShowPercent,
			"style": &parsed.ContextBar.Style, "warning_percent": &parsed.ContextBar.WarningPercent,
			"critical_percent": &parsed.ContextBar.CriticalPercent,
		}); err != nil {
			return err
		}
	}
	if value, ok := raw["cache_bar"]; ok {
		if _, err := decodeDisplayFields(value, "tui.status_line.cache_bar", map[string]any{
			"width": &parsed.CacheBar.Width, "style": &parsed.CacheBar.Style,
		}); err != nil {
			return err
		}
	}
	if value, ok := raw["items"]; ok {
		var entries []json.RawMessage
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &entries) != nil {
			return errors.New("tui.status_line.items: expected array")
		}
		parsed.Items = make([]StatusItemConfig, len(entries))
		for i, entry := range entries {
			path := fmt.Sprintf("tui.status_line.items[%d]", i)
			var id string
			if _, err := decodeDisplayFields(entry, path, map[string]any{"id": &id}); err != nil {
				return err
			}
			item, known := defaultStatusItem(id)
			if !known {
				return fmt.Errorf("%s.id: expected registered field", path)
			}
			if _, err := decodeDisplayFields(entry, path, map[string]any{
				"label": &item.Label, "enabled": &item.Enabled, "row": &item.Row, "priority": &item.Priority,
			}); err != nil {
				return err
			}
			parsed.Items[i] = item
		}
	}
	if err := parsed.Validate(); err != nil {
		return err
	}
	*c = *parsed
	return nil
}

// decodeDisplayFields 的错误只含固定字段路径，不回显用户的标签、分隔符或枚举。
func decodeDisplayFields(data []byte, path string, fields map[string]any) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return nil, fmt.Errorf("%s: expected object", path)
	}
	for name, target := range fields {
		if value, ok := raw[name]; ok {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, target) != nil {
				return nil, fmt.Errorf("%s.%s: invalid type", path, name)
			}
		}
	}
	return raw, nil
}

func defaultStatusItem(id string) (StatusItemConfig, bool) {
	for _, item := range DefaultStatusLine().Items {
		if item.ID == id {
			return item, true
		}
	}
	return StatusItemConfig{}, false
}

// Validate 校验显示列、范围和注册字段，显式零优先级合法。
func (c *StatusLineConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	rangeCheck := func(path string, value, min, max int) {
		if value < min || value > max {
			errs = append(errs, fmt.Errorf("tui.status_line.%s: expected integer in %d..%d", path, min, max))
		}
	}
	enumCheck := func(path, value string, allowed ...string) {
		if slices.Contains(allowed, value) {
			return
		}
		errs = append(errs, fmt.Errorf("tui.status_line.%s: invalid enum", path))
	}
	textCheck := func(path, value string, max int) {
		if strings.ContainsFunc(value, func(r rune) bool {
			return unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
		}) {
			errs = append(errs, fmt.Errorf("tui.status_line.%s: control characters are not allowed", path))
		}
		if ansi.StringWidth(value) > max {
			errs = append(errs, fmt.Errorf("tui.status_line.%s: at most %d display columns", path, max))
		}
	}
	rangeCheck("max_rows", c.MaxRows, 1, 2)
	rangeCheck("clock_refresh_ms", c.ClockRefreshMS, 250, 5000)
	rangeCheck("environment_refresh_ms", c.EnvironmentRefreshMS, 1000, 60000)
	rangeCheck("git_timeout_ms", c.GitTimeoutMS, 100, 2000)
	enumCheck("unknown", c.Unknown, "show", "hide")
	enumCheck("token_format", c.TokenFormat, "compact", "full")
	enumCheck("time_format", c.TimeFormat, "compact", "clock")
	enumCheck("context_format", c.ContextFormat, "usage", "bar")
	enumCheck("cache_format", c.CacheFormat, "tokens", "ratio", "both", "bar")
	enumCheck("cache_scope", c.CacheScope, "session", "last_call")
	rangeCheck("cache_bar.width", c.CacheBar.Width, 5, 30)
	enumCheck("cache_bar.style", c.CacheBar.Style, "unicode", "ascii")
	textCheck("separator", c.Separator, 8)
	rangeCheck("context_bar.width", c.ContextBar.Width, 5, 30)
	enumCheck("context_bar.style", c.ContextBar.Style, "unicode", "ascii")
	rangeCheck("context_bar.warning_percent", c.ContextBar.WarningPercent, 1, 99)
	rangeCheck("context_bar.critical_percent", c.ContextBar.CriticalPercent, 2, 100)
	if c.ContextBar.WarningPercent >= c.ContextBar.CriticalPercent {
		errs = append(errs, errors.New("tui.status_line.context_bar.warning_percent: must be less than critical_percent"))
	}
	if c.Items == nil {
		errs = append(errs, errors.New("tui.status_line.items: expected array"))
	} else if len(c.Items) > 14 {
		errs = append(errs, errors.New("tui.status_line.items: at most 14 fields"))
	}
	seen := make(map[string]bool, len(c.Items))
	for i, item := range c.Items {
		path := fmt.Sprintf("items[%d]", i)
		if _, known := defaultStatusItem(item.ID); !known {
			errs = append(errs, fmt.Errorf("tui.status_line.%s.id: expected registered field", path))
		} else if seen[item.ID] {
			errs = append(errs, fmt.Errorf("tui.status_line.%s.id: duplicate field", path))
		}
		seen[item.ID] = true
		rangeCheck(path+".row", item.Row, 1, 2)
		rangeCheck(path+".priority", item.Priority, 0, 100)
		textCheck(path+".label", item.Label, 20)
	}
	return errors.Join(errs...)
}
