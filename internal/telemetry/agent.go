package telemetry

import (
	"time"

	"go.uber.org/zap"
	"plume-agent/internal/model"
)

// Prompt 只记录拼装摘要；不接收 messages、原始参数或工具结果。
func (r *ModelRecorder) Prompt(runID, version, hash string, bytes, history, turn int, declarations []model.ToolDeclaration) {
	names := make([]string, 0, len(declarations))
	for _, d := range declarations {
		names = append(names, d.Name)
	}
	r.logger.Info("prompt_built", zap.String("run_id", runID), zap.String("prompt_version", version), zap.String("prompt_hash", hash), zap.Int("request_bytes", bytes), zap.Int("history_messages", history), zap.Int("turn_messages", turn), zap.Strings("tools", names), zap.String("schema_version", "tools-v1"))
}

// Tool 只接受许可名称和固定状态/错误码，不落盘工具参数或结果。
func (r *ModelRecorder) Tool(runID, callID, name, status, code string, duration time.Duration, parentCalls ...string) {
	parent := ""
	if len(parentCalls) > 0 {
		parent = parentCalls[0]
	}
	fields := []zap.Field{zap.String("run_id", runID), zap.String("parent_model_call_id", parent), zap.String("tool_call_id", callID), zap.String("tool", name), zap.String("status", status), zap.String("error_code", code), zap.Float64("duration_ms", float64(duration)/float64(time.Millisecond))}
	if status == "completed" || status == "failed" {
		validation := "accepted"
		switch code {
		case "unknown_tool", "invalid_arguments":
			validation = "rejected"
		case "cancelled", "timeout":
			validation = "unknown"
		}
		r.logger.Info("tool_validated", append(fields, zap.String("validation_status", validation))...)
	}
	r.logger.Info("tool_"+status, fields...)
}
