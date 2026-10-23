package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 记忆错误只包含固定类别；调用方不得把驱动报错、正文或裸内容哈希写入 trace。
var (
	// ErrInvalidMemory 表示身份、来源、正文或版本参数非法。
	ErrInvalidMemory = errors.New("invalid memory")
	// ErrMemoryLimit 表示条目或完整读取集合超过字节/条数边界，不能截断代替错误。
	ErrMemoryLimit = errors.New("memory exceeds limit")
	// ErrMemoryConflict 表示版本已改变；应重新读取核对，不盲目覆盖。
	ErrMemoryConflict = errors.New("memory revision conflict")
	// ErrMemoryStorage 隐藏可能带正文的数据库错误；提交报错后需读取核对结果。
	ErrMemoryStorage = errors.New("memory storage operation failed")
	// ErrCorruptMemory 表示持久条目违反账本契约；拒绝整次读取而不返回残片。
	ErrCorruptMemory = errors.New("corrupt memory")
)

// MemoryScopeKind 是同一可信主体内的可见范围，不代表工具执行权限。
type MemoryScopeKind string

const (
	// MemoryGlobal 表示同一主体跨工作区的记忆，绝不是跨主体共享。
	MemoryGlobal MemoryScopeKind = "global"
	// MemoryWorkspace 表示同一主体、同一规范工作区内的记忆。
	MemoryWorkspace MemoryScopeKind = "workspace"
	// MemorySession 表示同一主体、工作区与已登记会话内的私有记忆。
	MemorySession MemoryScopeKind = "session"
	// MemorySourceUser 仅表示显式提交，不声称是模型提取或核实过的事实。
	MemorySourceUser = "user"
)

const (
	memoryEntryBytes = 8192
	memoryReadBytes  = 65536
	memoryReadCount  = 100
)

// MemoryScope 必须由可信入口构造。global 不带工作区/会话，workspace
// 不带会话；session 必须属于已有同域会话。路径规范化由入口负责。
type MemoryScope struct {
	ScopeKey  string
	Kind      MemoryScopeKind
	Workspace string
	SessionID string
}

// MemoryWrite 按稳定键显式创建或 CAS 更新条目；ExpectedRevision=0 只创建。
// 不接受外部时间、自动来源或凭据引用，正文不参与任何日志字段。
type MemoryWrite struct {
	Scope            MemoryScope
	Key              string
	Content          string
	Source           string
	ExpectedRevision int64
}

// MemoryRecord 是私密账本快照，所有时间由存储层生成并固定为 UTC 毫秒。
// 调用方只能将正文用于授权的管理展示/请求，不能整体记录此结构。
type MemoryRecord struct {
	Scope     MemoryScope
	Key       string
	Content   string
	Source    string
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MemoryLimits 约束完整集合读取；零值采用 100 条/64KiB 默认，正值只能
// 收紧，负值或试图放宽硬上限均拒绝。这些字节不是 token 估算。
type MemoryLimits struct {
	MaxRecords int
	MaxBytes   int
}

// LongTermMemoryStore 独立于历史 Store，避免扩大既有 run 故障装饰器契约。
// 两个后端并发安全；本期只做精确作用域账本，不做任务相关性或 prompt 拼装。
type LongTermMemoryStore interface {
	// PutMemory 同版本更新或首次重投幂等；陈旧冲突保持原值。
	PutMemory(context.Context, MemoryWrite) (MemoryRecord, error)
	// ListMemories 按键稳定排序返回精确作用域全部条目；任何错误返回 nil。
	ListMemories(context.Context, MemoryScope, MemoryLimits) ([]MemoryRecord, error)
}

type memoryIdentity struct {
	Scope MemoryScope
	Key   string
}

func validMemoryIdentity(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) != "" && !strings.ContainsFunc(value, unicode.IsControl)
}

func validateMemoryScope(scope MemoryScope) error {
	if !validMemoryIdentity(scope.ScopeKey, 1024) {
		return ErrInvalidMemory
	}
	if scope.Kind == MemoryGlobal {
		if scope.Workspace != "" || scope.SessionID != "" {
			return ErrInvalidMemory
		}
		return nil
	}
	if (scope.Kind != MemoryWorkspace && scope.Kind != MemorySession) ||
		!validMemoryIdentity(scope.Workspace, 4096) || !filepath.IsAbs(scope.Workspace) || filepath.Clean(scope.Workspace) != scope.Workspace {
		return ErrInvalidMemory
	}
	if scope.Kind == MemoryWorkspace && scope.SessionID == "" {
		return nil
	}
	if scope.Kind == MemorySession && validMemoryIdentity(scope.SessionID, 256) {
		return nil
	}
	return ErrInvalidMemory
}

func validateMemoryWrite(write MemoryWrite) error {
	if err := validateMemoryScope(write.Scope); err != nil {
		return err
	}
	if !validMemoryIdentity(write.Key, 256) || write.Source != MemorySourceUser ||
		write.ExpectedRevision < 0 || write.ExpectedRevision == math.MaxInt64 ||
		!utf8.ValidString(write.Content) || strings.TrimSpace(write.Content) == "" {
		return ErrInvalidMemory
	}
	if memoryRecordSize(MemoryRecord{Scope: write.Scope, Key: write.Key, Content: write.Content, Source: write.Source}) > memoryEntryBytes {
		return ErrMemoryLimit
	}
	return nil
}

// memoryRecordSize 对所有文本的真实 UTF-8 字节和三个 int64 计量，SQL
// 必须使用 CAST AS BLOB 对齐；中文多字节及 NUL 不能按 SQLite 字符数计。
func memoryRecordSize(record MemoryRecord) int64 {
	return int64(len(record.Scope.ScopeKey) + len(record.Scope.Kind) + len(record.Scope.Workspace) +
		len(record.Scope.SessionID) + len(record.Key) + len(record.Content) + len(record.Source) + 24)
}

func normalizeMemoryLimits(limits MemoryLimits) (MemoryLimits, error) {
	if limits.MaxRecords < 0 || limits.MaxRecords > memoryReadCount || limits.MaxBytes < 0 || limits.MaxBytes > memoryReadBytes {
		return limits, ErrMemoryLimit
	}
	if limits.MaxRecords == 0 {
		limits.MaxRecords = memoryReadCount
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = memoryReadBytes
	}
	return limits, nil
}

func validateStoredMemory(record MemoryRecord) error {
	write := MemoryWrite{Scope: record.Scope, Key: record.Key, Content: record.Content, Source: record.Source}
	if err := validateMemoryWrite(write); err != nil || record.Revision < 1 || record.Revision == math.MaxInt64 ||
		record.CreatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return ErrCorruptMemory
	}
	return nil
}

// nextMemoryRecord 共用版本规则，避免内存与 SQLite 的幂等/并发行为分叉；
// 测试 TestLongTermMemoryVersions 与 ConcurrencyAndCancellation 守住此约束。
func nextMemoryRecord(write MemoryWrite, current MemoryRecord, exists bool) (MemoryRecord, error) {
	if exists {
		if err := validateStoredMemory(current); err != nil {
			return MemoryRecord{}, err
		}
		same := current.Content == write.Content && current.Source == write.Source
		if same && (write.ExpectedRevision == current.Revision || write.ExpectedRevision == current.Revision-1) {
			return current, nil
		}
		if write.ExpectedRevision != current.Revision || current.Revision >= math.MaxInt64-1 {
			return MemoryRecord{}, ErrMemoryConflict
		}
	} else if write.ExpectedRevision != 0 {
		return MemoryRecord{}, ErrMemoryConflict
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	record := MemoryRecord{Scope: write.Scope, Key: write.Key, Content: write.Content, Source: write.Source,
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	if exists {
		record.Revision = current.Revision + 1
		record.CreatedAt = current.CreatedAt
		if current.UpdatedAt.After(now) {
			record.UpdatedAt = current.UpdatedAt // 系统时钟回退不能倒退持久更新时间。
		}
	}
	return record, nil
}

func memoryStorageError(ctx context.Context, err error) error {
	// database/sql 的取消回滚可能先于 Commit，后者返回 ErrTxDone 而非
	// context 错误；必须结合操作 context，仍不能据此推断提交一定未发生。
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrSessionNotFound) {
		return err
	}
	// 不包装底层错误：SQLite 触发器/损坏数据的报错可能包含私密正文。
	return ErrMemoryStorage
}
