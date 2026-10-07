// Package telemetry 承载 plume 的可观测性：结构化事件、脱敏与（后续的）指标。
// 当前只包含首次设置向导的 setup trace。
package telemetry

import (
	"fmt"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Field 构造一个 trace 字段。它只是一个便捷包装，让调用方不必 import zap；
// **调用方必须保证 value 不含密钥、token 或二维码内容。**
func Field(key, value string) zap.Field { return zap.String(key, value) }

// 事件类型。新增事件请同步 docs/phase-01-weixin-agent.md §6 的 trace 规范。
const (
	EventSetupStart      = "setup_start"
	EventSetupStep       = "setup_step"
	EventSetupCompleted  = "setup_completed"
	EventSetupCancelled  = "setup_cancelled"
	EventSetupFailed     = "setup_failed"
	EventSetupCredential = "setup_credential"
	EventSetupSaved      = "setup_saved"
)

// SetupRecorder 记录一次首次设置运行的脱敏 trace。
//
// **它的方法刻意不接受任何密钥参数。** 密钥、token、二维码内容无法在类型层面进入
// trace，这比"记得别写进去"的约定更可靠——见 docs/phase-01-weixin-agent.md §6。
type SetupRecorder struct {
	logger *zap.Logger
	closer io.Closer // 非 nil 时 Close 会关闭底层 sink

	mu    sync.Mutex
	id    string
	start time.Time
	seq   int
	ended bool
}

// NewSetupRecorder 创建一个把 JSON Lines 写进 w 的记录器。
func NewSetupRecorder(w io.Writer) *SetupRecorder {
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(w),
		zapcore.InfoLevel,
	)
	return &SetupRecorder{logger: zap.New(core).WithOptions(zap.WithCaller(false))}
}

// SetCloser 让 Close 负责关闭底层 sink（例如 trace 文件）。
func (r *SetupRecorder) SetCloser(c io.Closer) { r.closer = c }

// Start 标记本次设置开始，返回本次运行的 setup_id。重复调用会返回同一个 ID。
func (r *SetupRecorder) Start() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.id == "" {
		r.id = newSetupID()
		r.start = time.Now()
	}
	r.logger.Info(EventSetupStart, r.baseFields("setup")...)
	return r.id
}

// ID 返回本次运行的 setup_id（Start 之前为空串）。
func (r *SetupRecorder) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

// Step 开始记录一个步骤，返回的结束函数会写入结果与耗时。用法：
//
//	done := rec.Step("provider")
//	... // 做这一步
//	done(err)
func (r *SetupRecorder) Step(step string) func(err error) {
	begin := time.Now()
	return func(err error) {
		fields := []zap.Field{
			zap.String("step", step),
			zap.String("status", statusOf(err)),
			zap.Int64("duration_ms", time.Since(begin).Milliseconds()),
		}
		if err != nil {
			fields = append(fields, zap.String("error", err.Error()))
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.seq++
		fields = append(fields, zap.Int("step_seq", r.seq))
		r.logger.Info(EventSetupStep, r.baseFields("step", fields...)...)
	}
}

// Event 记录一条自定义事件。field 只接受 zap.Field，调用方必须自己保证不含密钥值。
func (r *SetupRecorder) Event(name string, fields ...zap.Field) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger.Info(name, r.baseFields("event", fields...)...)
}

// CredentialStored 记录一次凭据落盘，只记录引用名与成败，不记录内容。
func (r *SetupRecorder) CredentialStored(ref string, err error) {
	fields := []zap.Field{
		zap.String("credential_ref", ref),
		zap.String("status", statusOf(err)),
	}
	if err != nil {
		fields = append(fields, zap.String("error", err.Error()))
	}
	r.Event(EventSetupCredential, fields...)
}

// Saved 记录一次配置写入，只记录路径与非敏感摘要。
func (r *SetupRecorder) Saved(path string, models, channels int, err error) {
	fields := []zap.Field{
		zap.String("path", path),
		zap.Int("models", models),
		zap.Int("channels", channels),
		zap.String("status", statusOf(err)),
	}
	if err != nil {
		fields = append(fields, zap.String("error", err.Error()))
	}
	r.Event(EventSetupSaved, fields...)
}

// Finish 写入本次设置的终态。重复调用只生效一次。
func (r *SetupRecorder) Finish(outcome string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended = true

	fields := []zap.Field{
		zap.String("outcome", outcome),
		zap.String("status", statusOf(err)),
		zap.Int64("duration_ms", time.Since(r.start).Milliseconds()),
	}
	if err != nil {
		fields = append(fields, zap.String("error", err.Error()))
	}
	r.logger.Info(EventSetupCompleted, r.baseFields("finish", fields...)...)
}

// Close 刷出缓冲并关闭 sink。
func (r *SetupRecorder) Close() error {
	_ = r.logger.Sync()
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

func (r *SetupRecorder) baseFields(kind string, extra ...zap.Field) []zap.Field {
	fields := make([]zap.Field, 0, len(extra)+3)
	fields = append(fields,
		zap.String("setup_id", r.id),
		zap.String("event_kind", kind),
	)
	return append(fields, extra...)
}

func statusOf(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

var (
	setupIDMu  sync.Mutex
	setupIDSeq int
)

// newSetupID 生成一个进程内唯一、可读的 setup_id。
func newSetupID() string {
	setupIDMu.Lock()
	defer setupIDMu.Unlock()
	setupIDSeq++
	return fmt.Sprintf("setup-%s-%03d", time.Now().UTC().Format("20060102T150405"), setupIDSeq)
}
