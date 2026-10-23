package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 管理错误不区分不可见与不存在，防止短 ID 查询泄露其他身份的会话。
var (
	// ErrSessionNotFound 统一隐藏不可见或不存在目标的差异。
	ErrSessionNotFound = errors.New("session not found")
	// ErrAmbiguousSessionID 要求用户提供更长前缀，不能任选首条历史。
	ErrAmbiguousSessionID = errors.New("session id is ambiguous")
	// ErrReceiptConflict 拒绝同请求 ID 携带不同命令内容再次执行。
	ErrReceiptConflict = errors.New("request id already used with a different fingerprint")
	// ErrInvalidPage 表示页码、页大小或乘法偏移超过有效范围。
	ErrInvalidPage = errors.New("invalid history page")
)

// Scope 把可信入口的权限域与规范工作区共同作为管理数据的身份；
// 空 ScopeKey 仅为 G4a.1 本地调用兼容，统一规范为 local。
type Scope struct {
	ScopeKey  string
	Workspace string
}

// PageRequest 页码从 1 开始，每页 1–100 项；正文第一页选最近 run，
// 页内再按发生顺序展示。非法参数拒绝而不静默改变用户请求。
type PageRequest struct {
	Page     int
	PageSize int
}

// SessionPage 是当前权限域与工作区内按最近 run 时间倒序的目录页。
type SessionPage struct {
	Sessions []SessionRecord
	Page     int
	PageSize int
	Total    int
}

// RunPage 是单个可见会话的正文页，包含失败/取消等展示记录；
// 这不是供模型恢复的完整 turns，不能将分页内容送入模型上下文。
type RunPage struct {
	Runs     []RunRecord
	Page     int
	PageSize int
	Total    int
}

// TimingSummary 只汇总成功完成 run 的计数和耗时，不读取聊天正文。
// 总时长以毫秒精度聚合，与 SQLite 的持久时间精度一致。
type TimingSummary struct {
	CompletedRuns     int
	CompletedDuration time.Duration
}

// Receipt 保存首次命令的结构化结果；Payload 使用独立有界 JSON，
// store 不依赖 app 类型，取消目标 run ID 或无目标由 app 固定在载荷中。
type Receipt struct {
	ScopeKey    string
	Workspace   string
	RequestID   string
	Fingerprint string
	Command     string
	Payload     json.RawMessage
	CreatedAt   time.Time
}

// CommandMutation 是与命令收据同事务发布的最小管理变更。绑定可引用
// 尚未持久化的默认空会话；命名空会话以 CreateSession 立即写元数据。
type CommandMutation struct {
	Receipt         Receipt
	CreateSession   *SessionRecord
	BindSessionID   string
	RenameSessionID string
	Title           string
	TitleManual     bool
}

// ManagementStore 扩展管理操作而不扩充 G4a.1 Store，便于既有终态
// 故障装饰器保持原契约。公共入口只能通过这些带 Scope 的方法解析 ID。
type ManagementStore interface {
	ListSessions(context.Context, Scope, PageRequest) (SessionPage, error)
	PageRuns(context.Context, Scope, string, PageRequest) (RunPage, error)
	ResolveSession(context.Context, Scope, string) (SessionRecord, error)
	LoadTiming(context.Context, Scope, string) (TimingSummary, error)
	GetBinding(context.Context, Scope) (string, bool, error)
	GetReceipt(context.Context, Scope, string) (Receipt, bool, error)
	// ApplyCommand 再次核对 request ID，返回首次收据及是否为重放；
	// fingerprint 冲突拒绝，变更失败不得留下收据或部分绑定。
	ApplyCommand(context.Context, CommandMutation) (Receipt, bool, error)
}

func normalizedScope(key string) string {
	if key == "" {
		return "local"
	}
	return key
}

func normalizeSession(record SessionRecord) SessionRecord {
	record.ScopeKey = normalizedScope(record.ScopeKey)
	record.RunCount, record.TurnCount = 0, 0
	return record
}

func scopeMatches(record SessionRecord, scope Scope) bool {
	return normalizedScope(record.ScopeKey) == normalizedScope(scope.ScopeKey) && record.Workspace == scope.Workspace
}

func pageOffset(page PageRequest) (int, error) {
	if page.Page < 1 || page.PageSize < 1 || page.PageSize > 100 || page.Page-1 > int(^uint(0)>>1)/page.PageSize {
		return 0, ErrInvalidPage
	}
	return (page.Page - 1) * page.PageSize, nil
}

// normalizeMutation 在开始事务前验证载荷上界，避免大 JSON 扩展或
// 一次半提交；数据库读取侧还要独立做同样校验，不能信任旧文件。
func normalizeMutation(mutation CommandMutation) (CommandMutation, error) {
	r := &mutation.Receipt
	r.ScopeKey = normalizedScope(r.ScopeKey)
	if r.Workspace == "" || r.RequestID == "" || r.Fingerprint == "" || r.Command == "" {
		return mutation, errors.New("command receipt identity is incomplete")
	}
	if len(r.ScopeKey)+len(r.Workspace)+len(r.RequestID)+len(r.Fingerprint)+len(r.Command) > 64*1024 {
		return mutation, ErrHistoryLimit
	}
	if len(r.Payload) > normalizeLoadLimits(nil).MaxBytes {
		return mutation, ErrHistoryLimit
	}
	if !json.Valid(r.Payload) {
		return mutation, errors.New("command receipt payload is not valid JSON")
	}
	if err := checkJSONExpansion(r.Payload); err != nil {
		return mutation, err
	}
	r.Payload = append(json.RawMessage(nil), r.Payload...)
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if mutation.CreateSession != nil {
		record := normalizeSession(*mutation.CreateSession)
		if record.ID == "" || !scopeMatches(record, Scope{ScopeKey: r.ScopeKey, Workspace: r.Workspace}) {
			return mutation, ErrSessionNotFound
		}
		if record.CreatedAt.IsZero() {
			record.CreatedAt = r.CreatedAt
		}
		mutation.CreateSession = &record
	}
	return mutation, nil
}

func copyReceipt(receipt Receipt) Receipt {
	receipt.Payload = append(json.RawMessage(nil), receipt.Payload...)
	return receipt
}

func visiblePrefix(id, input string) bool {
	return len(input) >= 8 && len(input) <= 32 && strings.HasPrefix(id, input)
}

func receiptKey(scope Scope, requestID string) string {
	// 分量长度编码防止渠道可控标识中的分隔符形成键碰撞。
	key := normalizedScope(scope.ScopeKey)
	return fmt.Sprintf("%d:%s%d:%s%s", len(key), key, len(scope.Workspace), scope.Workspace, requestID)
}
