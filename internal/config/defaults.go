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

// fillDefaults 只补缺失项；用户已有候选、显式强度与手填容量保持原样。
func (c *Config) fillDefaults() bool {
	changed := false
	if c.Agent == nil {
		c.Agent = DefaultAgent()
		changed = true
	}
	if c.Tools == nil {
		c.Tools = DefaultTools()
		changed = true
	}
	if c.TUI == nil {
		c.TUI = &TUIConfig{}
		changed = true
	}
	if c.TUI.StatusMessages == nil {
		c.TUI.StatusMessages = make(map[string][]string)
	}
	if c.TUI.StatusLine == nil {
		c.TUI.StatusLine = DefaultStatusLine()
		changed = true
	}
	for phase, candidates := range DefaultStatusMessages() {
		if len(c.TUI.MessageOverrides()[phase]) == 0 {
			c.TUI.StatusMessages[phase] = candidates
			changed = true
		}
	}
	for i := range c.Models {
		m := &c.Models[i]
		if !verifiedDeepSeekPreset(*m) {
			continue
		}
		if m.ReasoningEffort == nil {
			high := "high"
			m.ReasoningEffort = &high
			changed = true
		}
		if m.ContextWindowTokens == nil {
			// 官方两预设的 1M 容量只用于显示，不参与请求或预算。
			tokens := int64(1000000)
			m.ContextWindowTokens = &tokens
			changed = true
		}
	}
	return changed
}

// 只有已核实的官方端点与两预设才可补显式 high 和上下文容量。
func verifiedDeepSeekPreset(m ModelConfig) bool {
	if m.Provider != "deepseek" || m.Protocol != "deepseek" || (m.Model != "deepseek-flash" && m.Model != "deepseek-v4-pro") {
		return false
	}
	u, err := url.Parse(m.BaseURL)
	return err == nil && u.Scheme == "https" && u.Host == "api.deepseek.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/")
}
