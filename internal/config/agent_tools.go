package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// BudgetConfig 的次数与整轮时长允许零表示不限；字节与步骤上限必须为正。
type BudgetConfig struct {
	ModelCalls          int `json:"model_calls"`
	ToolCalls           int `json:"tool_calls"`
	ToolCallsPerStep    int `json:"tool_calls_per_step"`
	RequestBytes        int `json:"request_bytes"`
	ArgumentBytes       int `json:"argument_bytes"`
	ResultBytes         int `json:"result_bytes"`
	RunTimeoutSeconds   int `json:"run_timeout_seconds"`
	ModelTimeoutSeconds int `json:"model_timeout_seconds"`
	ToolTimeoutSeconds  int `json:"tool_timeout_seconds"`
}
type AgentConfig struct {
	Budget BudgetConfig `json:"budget"`
	Soul   *SoulConfig  `json:"soul"`
}
type ToolsConfig struct {
	Workspace      string   `json:"workspace"`
	Enabled        []string `json:"enabled"`
	ReadLines      int      `json:"read_lines"`
	SearchResults  int      `json:"search_results"`
	MaxFileBytes   int      `json:"max_file_bytes"`
	MaxOutputBytes int      `json:"max_output_bytes"`
	Shell          string   `json:"shell"`
}

func DefaultAgent() *AgentConfig {
	return &AgentConfig{Budget: BudgetConfig{
		ModelCalls: 0, ToolCalls: 0, ToolCallsPerStep: 64, RequestBytes: 8 << 20, ArgumentBytes: 1 << 20, ResultBytes: 64 << 10,
		RunTimeoutSeconds: 0, ModelTimeoutSeconds: 1800, ToolTimeoutSeconds: 180,
	}, Soul: DefaultSoul()}
}
func DefaultTools() *ToolsConfig {
	return &ToolsConfig{Workspace: ".", Enabled: []string{"read", "grep", "glob", "edit", "write", "bash"}, ReadLines: 200, SearchResults: 100, MaxFileBytes: 10 << 20, MaxOutputBytes: 32 << 10, Shell: "bash"}
}

func (c *AgentConfig) UnmarshalJSON(data []byte) error {
	*c = *DefaultAgent()
	raw, err := decodeDisplayFields(data, "agent", map[string]any{"budget": &c.Budget})
	if err != nil {
		return err
	}
	if value, ok := raw["soul"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, c.Soul); err != nil {
			return err
		}
	}
	return c.Validate()
}
func (c *BudgetConfig) UnmarshalJSON(data []byte) error {
	*c = DefaultAgent().Budget
	_, err := decodeDisplayFields(data, "agent.budget", map[string]any{
		"model_calls": &c.ModelCalls, "tool_calls": &c.ToolCalls, "tool_calls_per_step": &c.ToolCallsPerStep,
		"request_bytes": &c.RequestBytes, "argument_bytes": &c.ArgumentBytes, "result_bytes": &c.ResultBytes,
		"run_timeout_seconds": &c.RunTimeoutSeconds, "model_timeout_seconds": &c.ModelTimeoutSeconds, "tool_timeout_seconds": &c.ToolTimeoutSeconds,
	})
	if err != nil {
		return err
	}
	return (&AgentConfig{Budget: *c}).Validate()
}
func (c *ToolsConfig) UnmarshalJSON(data []byte) error {
	*c = *DefaultTools()
	_, err := decodeDisplayFields(data, "tools", map[string]any{"workspace": &c.Workspace, "enabled": &c.Enabled, "read_lines": &c.ReadLines, "search_results": &c.SearchResults, "max_file_bytes": &c.MaxFileBytes, "max_output_bytes": &c.MaxOutputBytes, "shell": &c.Shell})
	if err != nil {
		return err
	}
	return c.Validate()
}
func (c *AgentConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	errs = append(errs, c.Soul.Validate())
	b := c.Budget
	for name, value := range map[string]int{"model_calls": b.ModelCalls, "tool_calls": b.ToolCalls, "run_timeout_seconds": b.RunTimeoutSeconds} {
		if value < 0 || value > 2147483647 {
			errs = append(errs, fmt.Errorf("agent.budget.%s: expected integer in 0..2147483647", name))
		}
	}
	for name, value := range map[string]int{"tool_calls_per_step": b.ToolCallsPerStep, "request_bytes": b.RequestBytes, "argument_bytes": b.ArgumentBytes, "result_bytes": b.ResultBytes, "model_timeout_seconds": b.ModelTimeoutSeconds, "tool_timeout_seconds": b.ToolTimeoutSeconds} {
		if value < 1 || value > 2147483647 {
			errs = append(errs, fmt.Errorf("agent.budget.%s: expected positive integer up to 2147483647", name))
		}
	}
	if b.ResultBytes < 256 {
		errs = append(errs, errors.New("agent.budget.result_bytes: expected at least 256"))
	}
	return errors.Join(errs...)
}
func (c *ToolsConfig) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	for name, value := range map[string]string{"workspace": c.Workspace, "shell": c.Shell} {
		if strings.TrimSpace(value) == "" || strings.ContainsFunc(value, unicode.IsControl) {
			errs = append(errs, fmt.Errorf("tools.%s: expected nonempty path without control characters", name))
		}
	}
	for name, value := range map[string]int{"read_lines": c.ReadLines, "search_results": c.SearchResults, "max_file_bytes": c.MaxFileBytes, "max_output_bytes": c.MaxOutputBytes} {
		if value < 1 || value > 2147483647 {
			errs = append(errs, fmt.Errorf("tools.%s: expected positive integer up to 2147483647", name))
		}
	}
	if c.Enabled == nil {
		errs = append(errs, errors.New("tools.enabled: expected array"))
	}
	known := map[string]bool{"read": true, "grep": true, "glob": true, "edit": true, "write": true, "bash": true, "calculate": true, "current_time": true}
	seen := map[string]bool{}
	for _, name := range c.Enabled {
		if !known[name] || seen[name] {
			errs = append(errs, errors.New("tools.enabled: unknown or duplicate tool"))
		}
		seen[name] = true
	}
	return errors.Join(errs...)
}
