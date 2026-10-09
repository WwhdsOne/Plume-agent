package config

import (
	"bytes"
	"encoding/json"
)

// rawShape 固定已知字段边界，防止删除可选字段时把旧值误当成未知扩展保留。
type rawShape struct {
	fields  map[string]*rawShape
	element *rawShape
}

var configShape = &rawShape{fields: map[string]*rawShape{
	"schema_version": nil, "default_model": nil,
	"agent": {fields: map[string]*rawShape{
		"budget": {fields: map[string]*rawShape{
			"model_calls": nil, "tool_calls": nil, "tool_calls_per_step": nil, "request_bytes": nil, "argument_bytes": nil, "result_bytes": nil,
			"run_timeout_seconds": nil, "model_timeout_seconds": nil, "tool_timeout_seconds": nil,
		}},
		"soul": {fields: map[string]*rawShape{"enabled": nil, "path": nil, "max_bytes": nil}},
	}},
	"tools": {fields: map[string]*rawShape{"workspace": nil, "enabled": nil, "read_lines": nil, "search_results": nil, "max_file_bytes": nil, "max_output_bytes": nil, "shell": nil}},
	"models": {element: &rawShape{fields: map[string]*rawShape{
		"id": nil, "provider": nil, "protocol": nil, "base_url": nil, "model": nil,
		"api_key_ref": nil, "reasoning_effort": nil, "context_window_tokens": nil,
	}}},
	"channels": {element: &rawShape{fields: map[string]*rawShape{
		"id": nil, "type": nil, "enabled": nil, "model_ref": nil, "credential_ref": nil, "settings": nil,
	}}},
	"tui": {fields: map[string]*rawShape{
		"status_messages": nil,
		"status_line": {fields: map[string]*rawShape{
			"enabled": nil, "max_rows": nil, "separator": nil, "unknown": nil,
			"clock_refresh_ms": nil, "environment_refresh_ms": nil, "git_timeout_ms": nil,
			"token_format": nil, "time_format": nil, "context_format": nil, "cache_format": nil, "cache_scope": nil,
			"context_bar": {fields: map[string]*rawShape{
				"width": nil, "show_percent": nil, "style": nil, "warning_percent": nil, "critical_percent": nil,
			}},
			"cache_bar": {fields: map[string]*rawShape{"width": nil, "style": nil}},
			"items": {element: &rawShape{fields: map[string]*rawShape{
				"id": nil, "label": nil, "enabled": nil, "row": nil, "priority": nil,
			}}},
		}},
	}},
}}

// preserveUnknownFields 以新结构为准，只合并旧文件里的未知字段。数组按稳定 ID 配对。
func preserveUnknownFields(previous, current json.RawMessage, shape *rawShape) (json.RawMessage, error) {
	if shape == nil || len(previous) == 0 || bytes.Equal(bytes.TrimSpace(previous), []byte("null")) {
		return current, nil
	}
	if shape.element != nil {
		var oldEntries, newEntries []json.RawMessage
		if err := json.Unmarshal(previous, &oldEntries); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(current, &newEntries); err != nil {
			return nil, err
		}
		oldByID := make(map[string]json.RawMessage, len(oldEntries))
		for _, entry := range oldEntries {
			oldByID[rawID(entry)] = entry
		}
		for i, entry := range newEntries {
			updated, err := preserveUnknownFields(oldByID[rawID(entry)], entry, shape.element)
			if err != nil {
				return nil, err
			}
			newEntries[i] = updated
		}
		return json.Marshal(newEntries)
	}
	var oldFields, newFields map[string]json.RawMessage
	if err := json.Unmarshal(previous, &oldFields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(current, &newFields); err != nil {
		return nil, err
	}
	for key, value := range oldFields {
		if _, known := shape.fields[key]; !known {
			if _, present := newFields[key]; !present {
				newFields[key] = value
			}
		}
	}
	for key, value := range newFields {
		if child := shape.fields[key]; child != nil {
			updated, err := preserveUnknownFields(oldFields[key], value, child)
			if err != nil {
				return nil, err
			}
			newFields[key] = updated
		}
	}
	return json.Marshal(newFields)
}

func rawID(data json.RawMessage) string {
	var parsed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &parsed)
	return parsed.ID
}

// fillMissingRaw 保留已有标量、数组选择与未知字段，只递归添加缺失键。
func fillMissingRaw(previous, defaults json.RawMessage) (json.RawMessage, bool, error) {
	oldTrim, defaultTrim := bytes.TrimSpace(previous), bytes.TrimSpace(defaults)
	if len(oldTrim) == 0 {
		return defaults, true, nil
	}
	if len(defaultTrim) == 0 {
		return previous, false, nil
	}
	if defaultTrim[0] == '{' && (oldTrim[0] == '{' || bytes.Equal(oldTrim, []byte("null"))) {
		var oldFields, defaultFields map[string]json.RawMessage
		if err := json.Unmarshal(defaults, &defaultFields); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(previous, &oldFields); err != nil {
			return nil, false, err
		}
		changed := oldFields == nil
		if oldFields == nil {
			oldFields = make(map[string]json.RawMessage)
		}
		for key, value := range defaultFields {
			updated, added, err := fillMissingRaw(oldFields[key], value)
			if err != nil {
				return nil, false, err
			}
			if added {
				oldFields[key] = updated
				changed = true
			}
		}
		encoded, err := json.Marshal(oldFields)
		return encoded, changed, err
	}
	if defaultTrim[0] == '[' && oldTrim[0] == '[' {
		var oldEntries, defaultEntries []json.RawMessage
		if err := json.Unmarshal(previous, &oldEntries); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(defaults, &defaultEntries); err != nil {
			return nil, false, err
		}
		changed := false
		if len(oldEntries) == len(defaultEntries) {
			for i, entry := range oldEntries {
				updated, added, err := fillMissingRaw(entry, defaultEntries[i])
				if err != nil {
					return nil, false, err
				}
				if added {
					oldEntries[i] = updated
					changed = true
				}
			}
		}
		encoded, err := json.Marshal(oldEntries)
		return encoded, changed, err
	}
	return previous, false, nil
}
