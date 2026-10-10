package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TUIConfig 只描述文案与布局，不包含终端库类型或模型控制参数。
type TUIConfig struct {
	StatusMessages map[string][]string `json:"status_messages,omitempty"`
	StatusLine     *StatusLineConfig   `json:"status_line"`
}

// UnmarshalJSON 保留缺省与显式配置的区别，拒绝把 null 当作关闭思考。
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var parsed plain
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	for _, name := range []string{"agent", "tools"} {
		if value, ok := root[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s: expected object", name)
		}
	}
	var raw struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for i, m := range raw.Models {
		if v, ok := m["context_window_tokens"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			var tokens int64
			if json.Unmarshal(v, &tokens) != nil || tokens < 1 || tokens > 2147483647 {
				return fmt.Errorf("models[%d].context_window_tokens: expected null or integer in 1..2147483647", i)
			}
		}
		if v, ok := m["reasoning_effort"]; ok {
			var effort string
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &effort) != nil || !validReasoningEffort(effort) {
				return fmt.Errorf("models[%d].reasoning_effort: expected none, low, medium, high, or max", i)
			}
		}
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*c = Config(parsed)
	c.rawJSON = append([]byte(nil), bytes.TrimSpace(data)...)
	return nil
}

// UnmarshalJSON 逐层校验 tui 段：未知阶段名、非字符串条目均报错；
// status_line 缺省为 nil（由调用方决定是否补默认）。
func (c *TUIConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("tui: expected object")
	}
	var phases map[string]json.RawMessage
	if v, ok := raw["status_messages"]; ok && json.Unmarshal(v, &phases) != nil {
		return fmt.Errorf("tui.status_messages: expected object")
	}
	c.StatusMessages = make(map[string][]string)
	c.StatusLine = nil
	if value, ok := raw["status_line"]; ok {
		c.StatusLine = &StatusLineConfig{}
		if err := c.StatusLine.UnmarshalJSON(value); err != nil {
			return err
		}
	}
	for phase, v := range phases {
		switch phase {
		case "preparing", "waiting", "thinking", "responding":
		default:
			return fmt.Errorf("tui.status_messages.%s: unknown phase", phase)
		}
		var entries []json.RawMessage
		if json.Unmarshal(v, &entries) != nil {
			return fmt.Errorf("tui.status_messages.%s: expected list of strings", phase)
		}
		messages := make([]string, len(entries))
		for i, entry := range entries {
			if bytes.Equal(bytes.TrimSpace(entry), []byte("null")) || json.Unmarshal(entry, &messages[i]) != nil {
				return fmt.Errorf("tui.status_messages.%s[%d]: expected string", phase, i)
			}
		}
		c.StatusMessages[phase] = messages
	}
	return c.Validate()
}

// ResolveReasoningEffort 返回偏好及其是否在文件中指定；未验证端点缺省 high 不回写。
func (m ModelConfig) ResolveReasoningEffort() (string, bool) {
	if m.ReasoningEffort == nil {
		return "high", false
	}
	return *m.ReasoningEffort, true
}

func validReasoningEffort(value string) bool {
	switch value {
	case "none", "low", "medium", "high", "max":
		return true
	}
	return false
}

// Validate 对展示参数进行本地校验，错误只包含字段路径，不回显文案。
func (c *TUIConfig) Validate() error {
	var errs []error
	errs = append(errs, c.StatusLine.Validate())
	for phase, messages := range c.StatusMessages {
		path := "tui.status_messages." + phase
		switch phase {
		case "preparing", "waiting", "thinking", "responding":
		default:
			errs = append(errs, fmt.Errorf("%s: unknown phase", path))
			continue
		}
		if len(messages) > 16 {
			errs = append(errs, fmt.Errorf("%s: at most 16 messages", path))
		}
		for i, text := range messages {
			if strings.ContainsFunc(text, unicode.IsControl) {
				errs = append(errs, fmt.Errorf("%s[%d]: control characters are not allowed", path, i))
			}
			if utf8.RuneCountInString(strings.TrimSpace(text)) > 80 {
				errs = append(errs, fmt.Errorf("%s[%d]: at most 80 code points", path, i))
			}
		}
	}
	return errors.Join(errs...)
}

// MessageOverrides 返回去空白的深拷贝；空候选由显示层回退到内置文案。
func (c *TUIConfig) MessageOverrides() map[string][]string {
	out := make(map[string][]string)
	if c == nil {
		return out
	}
	for phase, candidates := range c.StatusMessages {
		for _, candidate := range candidates {
			if text := strings.TrimSpace(candidate); text != "" {
				out[phase] = append(out[phase], text)
			}
		}
	}
	return out
}
