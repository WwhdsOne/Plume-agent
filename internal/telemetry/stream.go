package telemetry

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"plume-agent/internal/model"
)

type runIDKey struct{}

// WithRunID 把 run ID 放入 ctx，供 run 内各处 trace 关联同一 run。
func WithRunID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runIDKey{}, id)
}

// RunID 取回 ctx 中的 run ID；未设置返回空串。
func RunID(ctx context.Context) string { id, _ := ctx.Value(runIDKey{}).(string); return id }

// ReasoningInfo 仅包含本地枚举与能力来源，不接受提示词和显示文案。
type ReasoningInfo struct{ Requested, Source, Effective, Capability string }

// Reasoning 写一条 model_reasoning 事件；effective 缺省记 unknown，
// 只记枚举与来源，不接受提示词或显示文案。
func (s *ModelSpan) Reasoning(info ReasoningInfo) {
	if info.Effective == "" {
		info.Effective = "unknown"
	}
	s.rec.logger.Info("model_reasoning", zap.String("model_call_id", s.callID), zap.String("requested", info.Requested), zap.String("source", info.Source), zap.String("effective", info.Effective), zap.String("capability", info.Capability))
}

// Observe 顺带观察流事件：记录首思考/首答案相对 span 起点的延迟并累计
// 字节数；只计数不看内容。
func (s *ModelSpan) Observe(e model.Event) {
	if e.Kind == model.EventReasoningDelta && e.ReasoningDelta != "" {
		if s.firstReasoning == 0 {
			s.firstReasoning = time.Since(s.start)
		}
		s.reasoningBytes += len(e.ReasoningDelta)
	}
	if e.Kind == model.EventTextDelta && e.TextDelta != "" {
		if s.firstAnswer == 0 {
			s.firstAnswer = time.Since(s.start)
		}
		s.answerBytes += len(e.TextDelta)
	}
}

// RunMetrics 汇总一次 run 的耗时与首字/字节指标，不含任何正文。
type RunMetrics struct {
	Duration, Preparation, FirstReasoning, FirstAnswer time.Duration
	ReasoningBytes, AnswerBytes                        int
	SaveStatus                                         string // saved/failed/unknown/disabled；枚举由 app 判定，不接受正文。
	SaveErrorStage                                     string // call_start/usage；终态已保存仍须保留先前存储故障。
}

// RunStart 记录 run 被接受（Submit 返回 ID）的时刻；sessionID 是随机
// 生成的持久会话标识，用于跨重启关联同一会话的 run。
func (r *ModelRecorder) RunStart(sessionID, id string, at time.Time) {
	r.logger.Info("run_started", zap.String("session_id", sessionID), zap.String("run_id", id), zap.Time("accepted_at", at))
}

// RunPhase 记录 run 的阶段切换（与 app.Phase 一一对应，此处只记字符串）。
func (r *ModelRecorder) RunPhase(id, phase string) {
	r.logger.Info("run_phase", zap.String("run_id", id), zap.String("phase", phase))
}

// RunEnd 记录终态：err 非空时附 error_code（模型 Error 优先，
// 其次按 ctx 分类，均无则 unknown）。
func (r *ModelRecorder) RunEnd(sessionID, id string, m RunMetrics, err error) {
	status, code := "ok", ""
	runStatus := "completed"
	if err != nil {
		status = "error"
		runStatus = "failed"
		code = "unknown"
		if e, ok := errors.AsType[*model.Error](err); ok {
			code = string(e.Code)
		} else if c := model.ClassifyContext(err); c != "" {
			code = string(c)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			runStatus = "cancelled"
		}
	}
	// 保存状态只接受有限枚举；旧调用方没有后端时明确记 disabled，不猜测已落盘。
	saveCode := ""
	switch m.SaveStatus {
	case "", "disabled":
		m.SaveStatus = "disabled"
	case "saved":
	case "failed":
		saveCode = "history_storage"
	case "unknown":
		saveCode = "history_commit_unknown"
	default:
		m.SaveStatus, saveCode = "unknown", "history_commit_unknown"
	}
	// save_status 描述终态落盘；起点/快照失败单独保留阶段，避免终态 saved
	// 掩盖先前故障。仅接受固定阶段枚举，绝不把错误正文或路径写进 trace。
	saveStage := ""
	switch m.SaveErrorStage {
	case "call_start", "usage":
		saveStage = m.SaveErrorStage
		if saveCode == "" {
			saveCode = "history_storage"
		}
		code = saveCode
	}
	// status 是整轮结果，run_status 单独保留执行结果；保存失败不能混进成功率。
	if saveCode != "" {
		status = "error"
		if code == "" {
			code = saveCode
		}
	}
	r.logger.Info("run_ended", zap.String("session_id", sessionID), zap.String("run_id", id), zap.String("status", status), zap.String("run_status", runStatus), zap.String("save_status", m.SaveStatus), zap.String("save_error_code", saveCode), zap.String("save_error_stage", saveStage), zap.String("error_code", code), zap.Float64("duration_ms", float64(m.Duration)/float64(time.Millisecond)), zap.Float64("preparation_ms", float64(m.Preparation)/float64(time.Millisecond)), optionalMillis("first_reasoning_ms", m.FirstReasoning), optionalMillis("first_answer_ms", m.FirstAnswer), zap.Int("reasoning_bytes", m.ReasoningBytes), zap.Int("answer_bytes", m.AnswerBytes))
}

// UIAnswer 记录 UI 侧首答案延迟（从用户提交到首段答案渲染）。
func (r *ModelRecorder) UIAnswer(id string, latency time.Duration) {
	r.logger.Info("ui_first_answer", zap.String("run_id", id), zap.Float64("latency_ms", float64(latency)/float64(time.Millisecond)))
}
func optionalMillis(key string, d time.Duration) zap.Field {
	if d == 0 {
		return zap.Any(key, nil)
	}
	return zap.Float64(key, float64(d)/float64(time.Millisecond))
}
