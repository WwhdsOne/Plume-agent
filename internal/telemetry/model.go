package telemetry

import (
	"errors"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"herald-agent/internal/model"
)

// 模型调用 trace 事件（0003 §9）。事件名保持英文，便于 grep 与测试断言。
const (
	EventModelStart     = "model_start"
	EventModelCompleted = "model_completed"
	EventModelFailed    = "model_failed"
)

// ModelRecorder 记录模型调用的脱敏 trace（JSON Lines）。它的方法刻意
// 不接受密钥参数；usage 以 usage_known 布尔表达缺失，绝不把 unknown 当 0。
// run/message 关联 ID 由应用层（G1b.2）补充，本记录器只负责单次调用 span。
type ModelRecorder struct {
	logger *zap.Logger
	closer io.Closer // 非 nil 时 Close 会关闭底层 sink

	mu  sync.Mutex
	seq int
}

// NewModelRecorder 创建一个把 JSON Lines 写进 w 的记录器。
func NewModelRecorder(w io.Writer) *ModelRecorder {
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(w),
		zapcore.InfoLevel,
	)
	return &ModelRecorder{logger: zap.New(core).WithOptions(zap.WithCaller(false))}
}

// SetCloser 让 Close 负责关闭底层 sink（例如 trace 文件）。
func (r *ModelRecorder) SetCloser(c io.Closer) { r.closer = c }

// StartModel 记录一次模型调用开始，返回 span；span.End 写入唯一终态。
// callID 由调用方生成并在同一调用链内唯一。
func (r *ModelRecorder) StartModel(callID, provider, protocol, modelID string) *ModelSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	fields := []zap.Field{
		zap.String("model_call_id", callID),
		zap.String("provider", provider),
		zap.String("protocol", protocol),
		zap.String("model", modelID),
		zap.Int("step_seq", r.seq),
	}
	r.logger.Info(EventModelStart, fields...)
	return &ModelSpan{rec: r, callID: callID, start: time.Now()}
}

// ModelSpan 是一次模型调用的生命周期。End 无论成功、失败还是重复调用，
// 都只产生一个终态事件。
type ModelSpan struct {
	rec    *ModelRecorder
	callID string
	start  time.Time
	once   sync.Once
}

// End 记录终态：err == nil 视为成功（resp 提供 finish reason 与 usage），
// 否则失败（*model.Error 提供分类与供应商请求 ID；其他错误归 unknown）。
// 错误描述经截断——URL 与正文不在 trace 中展开。
func (s *ModelSpan) End(resp *model.ChatResponse, err error) {
	s.once.Do(func() {
		duration := time.Since(s.start).Milliseconds()
		fields := []zap.Field{
			zap.String("model_call_id", s.callID),
			zap.Int64("duration_ms", duration),
		}
		if err != nil {
			fields = append(fields,
				zap.String("status", "error"),
			)
			var mErr *model.Error
			if errors.As(err, &mErr) {
				fields = append(fields, zap.String("error_code", string(mErr.Code)))
				if mErr.StatusCode != 0 {
					fields = append(fields, zap.Int("status_code", mErr.StatusCode))
				}
				if mErr.RequestID != "" {
					fields = append(fields, zap.String("request_id", mErr.RequestID))
				}
				fields = append(fields, zap.String("error", mErr.Error()))
			} else {
				fields = append(fields,
					zap.String("error_code", "unknown"),
					zap.String("error", truncate(err.Error(), maxModelErrorLogRunes)),
				)
			}
			s.rec.logger.Info(EventModelFailed, fields...)
			return
		}

		fields = append(fields, zap.String("status", "ok"))
		if resp != nil {
			fields = append(fields, zap.String("finish_reason", string(resp.FinishReason)))
			fields = append(fields, zap.Bool("usage_known", resp.Usage.OK))
			if resp.Usage.OK {
				fields = append(fields,
					zap.Int64("prompt_tokens", resp.Usage.PromptTokens),
					zap.Int64("completion_tokens", resp.Usage.CompletionTokens),
					zap.Int64("total_tokens", resp.Usage.TotalTokens),
				)
			}
		}
		s.rec.logger.Info(EventModelCompleted, fields...)
	})
}

// maxModelErrorLogRunes 限制非分类错误进入 trace 的长度。
const maxModelErrorLogRunes = 512

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// Close 刷出缓冲并关闭 sink。
func (r *ModelRecorder) Close() error {
	_ = r.logger.Sync()
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}
