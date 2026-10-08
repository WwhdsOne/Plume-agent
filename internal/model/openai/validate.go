package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"plume-agent/internal/model"
)

// validateResponse 检查 SDK 保留的原始字段类型，阻止宽松转换把错误协议变成成功。
// 流分帧仍完全由 SDK 完成；这里只验证已经解码的 JSON 对象。
func validateResponse(raw string, stream bool) (model.Usage, error) {
	if !json.Valid([]byte(raw)) {
		return model.Usage{}, invalidField()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := rejectDuplicateFields(decoder); err != nil {
		return model.Usage{}, err
	}
	root, err := responseObject(json.RawMessage(raw))
	if err != nil {
		return model.Usage{}, err
	}
	if err := responseString(root["id"], false); err != nil {
		return model.Usage{}, err
	}
	choices, err := responseArray(root["choices"])
	if err != nil {
		return model.Usage{}, err
	}
	for _, rawChoice := range choices {
		choice, err := responseObject(rawChoice)
		if err != nil {
			return model.Usage{}, err
		}
		if index, present := choice["index"]; present || stream {
			n, err := responseInteger(index)
			if err != nil || n != 0 {
				return model.Usage{}, invalidField()
			}
		}
		if err := responseString(choice["finish_reason"], true); err != nil {
			return model.Usage{}, err
		}
		field := "message"
		if stream {
			field = "delta"
		}
		message, err := responseObject(choice[field])
		if err != nil {
			return model.Usage{}, err
		}
		for _, key := range []string{"content", "reasoning_content"} {
			if err := responseString(message[key], true); err != nil {
				return model.Usage{}, err
			}
		}
		if err := responseString(message["role"], false); err != nil {
			return model.Usage{}, err
		}
		if calls, present := message["tool_calls"]; present {
			if err := validateToolCalls(calls, stream); err != nil {
				return model.Usage{}, err
			}
		}
	}
	return responseUsage(root["usage"])
}

func validateToolCalls(raw json.RawMessage, stream bool) error {
	calls, err := responseArray(raw)
	if err != nil {
		return err
	}
	for _, rawCall := range calls {
		call, err := responseObject(rawCall)
		if err != nil {
			return err
		}
		if index, present := call["index"]; present || stream {
			if n, err := responseInteger(index); err != nil || n < 0 {
				return invalidField()
			}
		}
		for _, key := range []string{"id", "type"} {
			if err := responseString(call[key], false); err != nil {
				return err
			}
		}
		if rawType, present := call["type"]; present {
			var toolType string
			_ = json.Unmarshal(rawType, &toolType)
			if toolType != "function" {
				return model.NewError(model.ErrUnsupported).WithSummary("unsupported tool call type")
			}
		}
		// 流增量可只包含 ID 或参数片段；非流式工具调用必须完整。
		fnRaw, present := call["function"]
		if present || !stream {
			fn, err := responseObject(fnRaw)
			if err != nil {
				return err
			}
			for _, key := range []string{"name", "arguments"} {
				if err := responseString(fn[key], false); err != nil {
					return err
				}
				if !stream && len(fn[key]) == 0 {
					return invalidField()
				}
			}
		}
		if !stream {
			var id string
			if json.Unmarshal(call["id"], &id) != nil || id == "" {
				return invalidField()
			}
		}
	}
	return nil
}

// rejectDuplicateFields 遍历 SDK 保留的 JSON 值；Token 已解开键名转义，
// 因而同名键无法利用 SDK 与 encoding/json 的首值/末值差异绕过校验。
func rejectDuplicateFields(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return invalidField()
	}
	switch token {
	case json.Delim('{'):
		keys := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return invalidField()
			}
			keys[name] = true
			if err := rejectDuplicateFields(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return invalidField()
		}
	case json.Delim('['):
		for decoder.More() {
			if err := rejectDuplicateFields(decoder); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return invalidField()
		}
	}
	return nil
}

func responseUsage(raw json.RawMessage) (model.Usage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return model.Usage{}, nil
	}
	usage, err := responseObject(raw)
	if err != nil {
		return model.Usage{}, err
	}
	var counts [3]int64
	known := true
	for i, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		value, present := usage[key]
		if !present {
			known = false
			continue
		}
		counts[i], err = responseInteger(value)
		if err != nil || counts[i] < 0 {
			return model.Usage{}, invalidField()
		}
	}
	if !known {
		return model.Usage{}, nil
	}
	return model.Usage{OK: true, PromptTokens: counts[0], CompletionTokens: counts[1], TotalTokens: counts[2]}, nil
}

func responseObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, invalidField()
	}
	return object, nil
}

func responseArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) != nil || array == nil {
		return nil, invalidField()
	}
	return array, nil
}

func responseInteger(raw json.RawMessage) (int64, error) {
	var n int64
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &n) != nil {
		return 0, invalidField()
	}
	return n, nil
}

func responseString(raw json.RawMessage, nullable bool) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if nullable {
			return nil
		}
		return invalidField()
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return invalidField()
	}
	return nil
}

func invalidField() error {
	// 不复制响应字段或正文，避免供应商返回的敏感内容进入错误摘要。
	return model.NewError(model.ErrInvalidResponse).WithSummary("invalid response field structure or type")
}
