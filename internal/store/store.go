// Package store 持久化会话、run 与模型调用的结构化历史：一个事务提交
// 完整成功轮次，起点先于模型请求落盘，崩溃遗留的 running 记录在下次打开时
// 标记 interrupted；管理查询按权限域分页，绑定与命令收据原子提交。
// G4a.3.1 扩展显式长期记忆账本与精确作用域有界读取，不做任务召回或
// prompt 拼装，也不承载渠道传输；数据库归属配置目录，不在工作区写隐含库。
package store

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"plume-agent/internal/model"
)

// ErrHistoryInUse 表示目标数据库已被另一个持久化聊天进程锁定；
// 首版整库进程独占，不按超时抢占仍执行工具的进程。
var ErrHistoryInUse = errors.New("history database is locked by another plume process")

// ErrSchemaNewer 表示数据库迁移版本比当前程序更新，必须拒绝写入，
// 不能删库重建或把新 schema 当空历史。
var ErrSchemaNewer = errors.New("history schema is newer than this build")

// ErrDuplicateRun 表示同一 run ID 已登记起点；调用方应复用原结果而不是重跑。
var ErrDuplicateRun = errors.New("run already recorded")

// ErrUnknownCall 表示用量快照对应的调用不存在，未知调用不得凭空补记。
var ErrUnknownCall = errors.New("model call not recorded")

// ErrUnknownRun 表示无法按唯一 ID 确认提交结果；调用方不能把它冒充未提交。
var ErrUnknownRun = errors.New("run not recorded")

// ErrCorruptHistory 表示历史结构或关联损坏；保留原数据并拒绝继续。
var ErrCorruptHistory = errors.New("corrupt history")

// ErrHistoryLimit 表示恢复数据超过读取边界；拒绝整次恢复而不截断消息组。
var ErrHistoryLimit = errors.New("history exceeds load limit")

// LoadLimits 限制恢复前允许读取的 JSON/正文总字节及数据库记录数。
// 非正值使用安全默认，显式值只能收紧默认上限，不能关闭读取边界。
type LoadLimits struct {
	MaxBytes   int
	MaxRecords int
}

// RunState 是确认终态事务是否完整提交的最小快照。Completed 只有在
// HasTurn=true 且成功轮次的角色、工具关联和 session ID 校验通过时可确认。
type RunState struct {
	SessionID string
	Status    RunStatus
	HasTurn   bool
}

// RunStatus 是 run 的持久终态。running 只存在于进程存活期间；
// 下次打开数据库时全部遗留 running 会被标记 interrupted。
type RunStatus string

// 终态枚举：completed/failed/cancelled/interrupted 互斥；成功轮次只属于 completed。
const (
	RunRunning     RunStatus = "running"
	RunCompleted   RunStatus = "completed"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunInterrupted RunStatus = "interrupted"
)

// ModelRef 是落盘的模型引用快照，供恢复时重新装配运行时并校验兼容性；
// 凭据永不落入引用，恢复时从当前安全存储解析。
type ModelRef struct {
	ConfigID string `json:"config_id"`
	Provider string `json:"provider"`
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`
}

// Fingerprint 以 provider、协议、模型 ID 与规范化 endpoint 计算兼容指纹；
// 仅 scheme/host 忽略大小写，路径与 query 必须保留大小写。任一项变化即视为不兼容，
// 不静默把旧对话发往另一个服务。
func (r ModelRef) Fingerprint() string {
	normalize := func(u string) string {
		u = strings.TrimSpace(u)
		if u == "" {
			return ""
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return u // 非法 endpoint 不作宽松归一，避免不同文本被误判兼容。
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimRight(parsed.Path, "/")
		parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
		return parsed.String()
	}
	return r.Provider + "\x1f" + r.Protocol + "\x1f" + r.Model + "\x1f" + normalize(r.BaseURL)
}

// SessionRecord 是会话元数据。默认标题且没有 run 的会话不落盘，
// 首次 run 起点事务内按 ID 幂等创建。
type SessionRecord struct {
	ID          string
	ScopeKey    string // 空值兼容旧调用，存储时规范为 local；与工作区共同界定可见集合。
	Workspace   string // 工具工作目录的规范绝对路径（EvalSymlinks 后）
	Title       string
	TitleManual bool // 手工标题不能被首次普通输入的自动标题覆盖。
	ModelRef    ModelRef
	CreatedAt   time.Time
	LastRunAt   time.Time // 最近一次 run 起点时间；仅浏览或空会话不更新
	RunCount    int       // 查询派生值，不单独持久化。
	TurnCount   int       // 完整成功轮次数，查询时从 turns 派生。
}

// RunRecord 是一次 run 的起点与终态数据。Input/Reply/Reasoning 是展示与
// 恢复用的正文；失败/取消时 Reply 保存已显示的部分内容，成功时为最终回复。
type RunRecord struct {
	ID         string
	SessionID  string
	Input      string
	Status     RunStatus
	Error      string
	Reply      string
	Reasoning  string
	StartedAt  time.Time
	FinishedAt time.Time
	ToolEvents []ToolEventRecord
}

// ToolEventRecord 是脱敏的工具执行摘要（名称/状态/固定摘要），不含模型原始
// 参数与结果正文；由 app 层从 agent.ToolEvent 转换。
type ToolEventRecord struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	DurationMS int64  `json:"duration_ms"`
}

// StoredUsage 是用量快照的持久化格式。model.Usage 没有 json 标签，
// 存储字段独立定义，避免把运行时结构体当永久格式；当前计数格式保持兼容，
// 独立消息格式的版本由 storedTurn 管理。
type StoredUsage struct {
	OK                 bool   `json:"ok"`
	PromptTokens       int64  `json:"prompt_tokens"`
	CompletionTokens   int64  `json:"completion_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	CachedPromptTokens *int64 `json:"cached_prompt_tokens,omitempty"`
}

// FromUsage 把运行时用量转为存储格式。
func FromUsage(u model.Usage) StoredUsage {
	s := StoredUsage{OK: u.OK, PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
	if u.CachedPromptTokens != nil {
		v := *u.CachedPromptTokens
		s.CachedPromptTokens = &v
	}
	return s
}

// Usage 还原运行时用量。
func (s StoredUsage) Usage() model.Usage {
	u := model.Usage{OK: s.OK, PromptTokens: s.PromptTokens, CompletionTokens: s.CompletionTokens, TotalTokens: s.TotalTokens}
	if s.CachedPromptTokens != nil {
		v := *s.CachedPromptTokens
		u.CachedPromptTokens = &v
	}
	return u
}

// ModelCallRecord 是一次模型调用的起点与用量快照。Usage 为 nil 表示未知
// （起点已写、调用未完成或快照保存失败），不能当作已知零。
type ModelCallRecord struct {
	ID        string // runID/model-N
	RunID     string
	SessionID string
	Sequence  int
	Usage     *StoredUsage
	SavedAt   time.Time
}

// Store 是历史持久化契约。实现必须并发安全；网络发送与本地事务无法原子化，
// 起点、快照与终态分三次提交，恢复侧按唯一 ID 幂等合并。G4a.1 只覆盖本机
// 单进程独占使用；跨进程并发所有权留给 G2b。
type Store interface {
	// StartRun 原子登记 run 起点与用户输入；会话记录按 ID 幂等创建并
	// 刷新最近活动时间。失败时调用方不得发送模型请求。run ID 重复返回
	// ErrDuplicateRun。
	StartRun(ctx context.Context, session SessionRecord, run RunRecord) error

	// StartModelCall 在发送模型请求前登记调用起点；重复 ID 幂等返回。
	// 失败时调用方取消该次请求。
	StartModelCall(ctx context.Context, call ModelCallRecord) error

	// SaveUsage 按 call ID 整体替换用量快照（天然去重不累加）。
	// 调用不存在返回 ErrUnknownCall。
	SaveUsage(ctx context.Context, callID string, usage StoredUsage, at time.Time) error

	// CompleteRun 用一个事务提交完整成功轮次、run 终态（completed）、
	// 终态用量与会话活动时间；成功后再由调用方更新内存历史。重复提交
	// 同一 run 是幂等空操作。turnSeq 从 1 起在事务内递增编号。
	CompleteRun(ctx context.Context, run RunRecord, turn []model.Message) error

	// GetRunState 查询事务结果；成功终态但缺失/损坏轮次返回错误，未找到
	// 返回 ErrUnknownRun。调用方查询也失败时必须阻止内存与磁盘分叉。
	GetRunState(ctx context.Context, runID string) (RunState, error)

	// FinishRun 提交失败/取消/中断终态与已展示内容；不写成功轮次。
	// 重复提交同一 run 是幂等空操作。
	FinishRun(ctx context.Context, run RunRecord) error

	// InterruptRunning 把全部遗留 running 记录标记 interrupted，返回受影响
	// 行数；在取得数据库锁之后、开始新会话之前调用一次。
	InterruptRunning(ctx context.Context, at time.Time) (int, error)

	// LatestSession 返回指定工作区下最近有 run 的会话；没有可继续的历史
	// 时第二个返回值为 false。只按 last_run_at 排序，不猜测用户意图。
	LatestSession(ctx context.Context, workspace string) (SessionRecord, bool, error)

	// LoadTurns 按序加载会话全部成功轮次消息，并返回下一轮次号（已提交数+1）。
	LoadTurns(ctx context.Context, sessionID string, limits ...LoadLimits) ([]model.Message, int, error)

	// ListRuns 按起点时间返回会话全部 run 记录（含失败/取消/中断），
	// 供恢复时重建展示转录；不含轮次消息正文。
	ListRuns(ctx context.Context, sessionID string, limits ...LoadLimits) ([]RunRecord, error)

	// LoadCalls 按序号加载会话全部调用记录，恢复统计时按 call ID 聚合；
	// Usage 为 nil 的调用保持未知，不补零。
	LoadCalls(ctx context.Context, sessionID string, limits ...LoadLimits) ([]ModelCallRecord, error)

	// Close 释放数据库与整库锁；重复关闭安全。
	Close() error
}
