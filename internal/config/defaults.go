package config

import "net/url"

// DefaultStatusMessages 返回可直接写入配置文件的完整阶段文案。
func DefaultStatusMessages() map[string][]string {
	return map[string][]string{
		"preparing":  {"getting ready", "gathering thoughts"},
		"waiting":    {"waiting", "brewing", "connecting dots"},
		"thinking":   {"Thought"},
		"responding": {"responding"},
	}
}

// fillDefaults 只补缺失项；用户已有候选和显式强度保持原样。
func (c *Config) fillDefaults() bool {
	changed := false
	if c.TUI == nil {
		c.TUI = &TUIConfig{}
		changed = true
	}
	if c.TUI.StatusMessages == nil {
		c.TUI.StatusMessages = make(map[string][]string)
	}
	for phase, candidates := range DefaultStatusMessages() {
		if len(c.TUI.MessageOverrides()[phase]) == 0 {
			c.TUI.StatusMessages[phase] = candidates
			changed = true
		}
	}
	for i := range c.Models {
		m := &c.Models[i]
		if m.ReasoningEffort == nil && defaultHighSupported(*m) {
			high := "high"
			m.ReasoningEffort = &high
			changed = true
		}
	}
	return changed
}

// 未验证的模型不能把默认 high 写成显式要求，否则启动能力校验会拒绝它。
func defaultHighSupported(m ModelConfig) bool {
	if m.Provider != "deepseek" || m.Protocol != "deepseek" || (m.Model != "deepseek-flash" && m.Model != "deepseek-v4-pro") {
		return false
	}
	u, err := url.Parse(m.BaseURL)
	return err == nil && u.Scheme == "https" && u.Host == "api.deepseek.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/")
}
