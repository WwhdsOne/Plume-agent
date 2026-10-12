package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"plume-agent/internal/model"
)

const (
	messageFormatVersion = 1
	defaultLoadBytes     = 8 << 20
	defaultLoadRecords   = 10000
)

// storedTurn 是独立于运行时 model.Message 的永久载荷；以后消息格式演进
// 单独迁移 version，不把 SQLite user_version 误当消息格式版本。
type storedTurn struct {
	Version  int             `json:"version"`
	Messages []storedMessage `json:"messages"`
}

type storedMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Reasoning  string           `json:"reasoning,omitempty"`
	ToolCalls  []storedToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type storedToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// normalizeLoadLimits 始终保留硬上限；多组约束取最严格值，0 不表示无限。
func normalizeLoadLimits(limits []LoadLimits) LoadLimits {
	out := LoadLimits{MaxBytes: defaultLoadBytes, MaxRecords: defaultLoadRecords}
	for _, limit := range limits {
		if limit.MaxBytes > 0 && limit.MaxBytes < out.MaxBytes {
			out.MaxBytes = limit.MaxBytes
		}
		if limit.MaxRecords > 0 && limit.MaxRecords < out.MaxRecords {
			out.MaxRecords = limit.MaxRecords
		}
	}
	return out
}

func checkLoadSize(records, size int64, limit LoadLimits) error {
	if records > int64(limit.MaxRecords) || size > int64(limit.MaxBytes) {
		return fmt.Errorf("%w: records=%d bytes=%d, max records=%d bytes=%d; start a new session", ErrHistoryLimit, records, size, limit.MaxRecords, limit.MaxBytes)
	}
	return nil
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunRunning, RunCompleted, RunFailed, RunCancelled, RunInterrupted:
		return true
	default:
		return false
	}
}

func encodeTurn(messages []model.Message) ([]byte, error) {
	if err := validateTurn(messages, make(map[string]bool)); err != nil {
		return nil, err
	}
	payload := storedTurn{Version: messageFormatVersion, Messages: make([]storedMessage, len(messages))}
	for i, message := range messages {
		stored := storedMessage{Role: string(message.Role), Content: message.Content, Reasoning: message.Reasoning, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			stored.ToolCalls = append(stored.ToolCalls, storedToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		payload.Messages[i] = stored
	}
	return json.Marshal(payload)
}

// decodeTurn 仅兼容初版未封装数组与当前 v1；拒绝更新格式、未知字段和
// 尾随 JSON。历史中的 system 永不转换为下一次请求的指令来源。
func decodeTurn(encoded []byte, seen map[string]bool) ([]model.Message, error) {
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) == 0 || !utf8.Valid(encoded) {
		return nil, fmt.Errorf("%w: invalid turn encoding", ErrCorruptHistory)
	}
	if err := checkJSONExpansion(encoded); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var messages []model.Message
	if encoded[0] == '[' {
		if err := decoder.Decode(&messages); err != nil {
			return nil, fmt.Errorf("%w: legacy turn: %v", ErrCorruptHistory, err)
		}
	} else {
		var payload storedTurn
		if err := decoder.Decode(&payload); err != nil {
			return nil, fmt.Errorf("%w: turn payload: %v", ErrCorruptHistory, err)
		}
		if payload.Version != messageFormatVersion {
			return nil, fmt.Errorf("%w: unsupported message format v%d", ErrCorruptHistory, payload.Version)
		}
		messages = make([]model.Message, len(payload.Messages))
		for i, stored := range payload.Messages {
			message := model.Message{Role: model.Role(stored.Role), Content: stored.Content, Reasoning: stored.Reasoning, ToolCallID: stored.ToolCallID}
			for _, call := range stored.ToolCalls {
				message.ToolCalls = append(message.ToolCalls, model.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
			}
			messages[i] = message
		}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing turn payload", ErrCorruptHistory)
	}
	if err := validateTurn(messages, seen); err != nil {
		return nil, err
	}
	return messages, nil
}

// checkJSONExpansion 先扫描数组元素数再分配 DTO；仅限单行字节仍可能让
// [null,null,...] 展开成很大的结构体切片。字符串参数保持不透明，不递归解析工具参数。
func checkJSONExpansion(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var stack []json.Delim
	records := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: invalid JSON payload: %v", ErrCorruptHistory, err)
		}
		delim, isDelim := token.(json.Delim)
		closing := isDelim && (delim == '}' || delim == ']')
		if !closing && len(stack) > 0 && stack[len(stack)-1] == '[' {
			records++
			if records > defaultLoadRecords {
				return fmt.Errorf("%w: JSON array records exceed %d", ErrHistoryLimit, defaultLoadRecords)
			}
		}
		if !isDelim {
			continue
		}
		if closing {
			stack = stack[:len(stack)-1]
		} else {
			stack = append(stack, delim)
		}
	}
}

// validateTurn 校验一个不可拆分的 user→assistant 成功轮次。工具结果必须
// 紧随完整调用组；参数正文仍由工具执行层处理，损坏 JSON 参数的失败结果也可保存。
func validateTurn(messages []model.Message, seen map[string]bool) error {
	bad := func(reason string) error { return fmt.Errorf("%w: %s", ErrCorruptHistory, reason) }
	// 写入与两后端读取共用展开上限，避免内存后端接受 SQLite 无法恢复的轮次。
	records := len(messages)
	for _, message := range messages {
		if len(message.ToolCalls) > defaultLoadRecords-records {
			return fmt.Errorf("%w: turn records exceed %d", ErrHistoryLimit, defaultLoadRecords)
		}
		records += len(message.ToolCalls)
	}
	if records > defaultLoadRecords {
		return fmt.Errorf("%w: turn records exceed %d", ErrHistoryLimit, defaultLoadRecords)
	}
	if len(messages) < 2 || messages[0].Role != model.RoleUser || messages[len(messages)-1].Role != model.RoleAssistant || len(messages[len(messages)-1].ToolCalls) != 0 {
		return bad("incomplete successful turn")
	}
	pending := make(map[string]bool)
	for i, message := range messages {
		if !utf8.ValidString(message.Content) || !utf8.ValidString(message.Reasoning) || !utf8.ValidString(message.ToolCallID) {
			return bad("invalid message UTF-8")
		}
		if message.Role == model.RoleTool {
			if len(message.ToolCalls) != 0 || message.Reasoning != "" || !pending[message.ToolCallID] {
				return bad("orphan or duplicate tool result")
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) != 0 {
			return bad("incomplete tool message group")
		}
		if message.Role != model.RoleAssistant && (i != 0 || message.Role != model.RoleUser) {
			return bad("unexpected message role")
		}
		if message.ToolCallID != "" || (message.Role != model.RoleAssistant && (len(message.ToolCalls) != 0 || message.Reasoning != "")) {
			return bad("invalid role fields")
		}
		for _, call := range message.ToolCalls {
			if !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !utf8.ValidString(call.Arguments) {
				return bad("invalid tool call UTF-8")
			}
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || seen[call.ID] {
				return bad("missing or duplicate tool call")
			}
			seen[call.ID], pending[call.ID] = true, true
		}
	}
	if len(pending) != 0 {
		return bad("incomplete tool message group")
	}
	return nil
}
