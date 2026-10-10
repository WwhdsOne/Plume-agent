package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WorkspaceOptions 是开发工具的工作目录、许可与输出边界。
// 字节限制作用于文件/工具结果，并不代表模型 token 容量；零值由 NewWorkspace 补默认。
type WorkspaceOptions struct {
	Root string
	// Enabled 为 nil 时用默认六工具；显式空切片关闭全部工具，不依赖 Shell 可执行文件。
	Enabled                                                []string
	ReadLines, SearchResults, MaxFileBytes, MaxOutputBytes int
	ResultBytes, BashTimeoutSeconds                        int
	Shell                                                  string
}

type workspace struct {
	root    *os.Root
	path    string
	options WorkspaceOptions
	// mu 串行保护先读后改检查、文件发布与关闭；搜索和 Shell 不持有此锁。
	mu sync.Mutex
	// seen 保存本注册表内 read 取得的全文摘要，分页/截断也按全文计算；写入成功后失效。
	// 它不随 run 自动清空，因此每次覆盖仍须核对当前内容，不能把旧摘要当作文件锁。
	seen   map[string][32]byte
	closed bool
}

// DefaultWorkspaceOptions 返回独立的默认许可切片与有限文件/输出边界；Shell 默认超时为秒。
func DefaultWorkspaceOptions() WorkspaceOptions {
	return WorkspaceOptions{Root: ".", Enabled: []string{"read", "grep", "glob", "edit", "write", "bash"}, ReadLines: 200, SearchResults: 100, MaxFileBytes: 10 << 20, MaxOutputBytes: 32 << 10, ResultBytes: 64 << 10, BashTimeoutSeconds: 180, Shell: "bash"}
}

// NewWorkspace 固定工作区根句柄与许可名单；仅启用 bash 时要求 Shell 可执行。
// 文件路径经 os.Root 阻止越界，Shell 执行仍拥有宿主权限；调用者结束使用后须 Close。
func NewWorkspace(options WorkspaceOptions) (*Registry, error) {
	defaults := DefaultWorkspaceOptions()
	if options.Root == "" {
		options.Root = defaults.Root
	}
	if options.Enabled == nil {
		options.Enabled = defaults.Enabled
	}
	if options.ReadLines == 0 {
		options.ReadLines = defaults.ReadLines
	}
	if options.SearchResults == 0 {
		options.SearchResults = defaults.SearchResults
	}
	if options.MaxFileBytes == 0 {
		options.MaxFileBytes = defaults.MaxFileBytes
	}
	if options.MaxOutputBytes == 0 {
		options.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if options.Shell == "" {
		options.Shell = defaults.Shell
	}
	if options.ResultBytes == 0 {
		options.ResultBytes = defaults.ResultBytes
	}
	if options.BashTimeoutSeconds == 0 {
		options.BashTimeoutSeconds = defaults.BashTimeoutSeconds
	}
	if options.ReadLines < 1 || options.SearchResults < 1 || options.MaxFileBytes < 1 || options.MaxOutputBytes < 1 || options.ResultBytes < 256 || options.BashTimeoutSeconds < 1 || int64(options.BashTimeoutSeconds) > int64((1<<63-1)/int64(time.Second)) {
		return nil, errors.New("invalid workspace options")
	}
	if slices.Contains(options.Enabled, "bash") {
		if _, err := exec.LookPath(options.Shell); err != nil {
			return nil, errors.New("workspace shell is not executable")
		}
	}
	abs, err := filepath.Abs(options.Root)
	if err != nil {
		return nil, errors.New("invalid workspace root")
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, errors.New("cannot resolve workspace root")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, errors.New("cannot open workspace root")
	}
	w := &workspace{root: root, path: abs, options: options, seen: make(map[string][32]byte)}
	available := map[string]Definition{
		"read":  {Name: "read", Description: "Read UTF-8 text from a regular workspace file with 1-based line numbers. Read before editing or overwriting; truncated results have next_offset. Paths must remain inside workspace.", Parameters: `{"type":"object","properties":{"path":{"type":"string","description":"Workspace-relative file path."},"offset":{"type":"integer","minimum":1,"description":"First line (1-based), default 1."},"limit":{"type":"integer","minimum":1,"description":"Maximum lines; default configured read_lines."}},"required":["path"],"additionalProperties":false}`, Execute: w.read},
		"grep":  {Name: "grep", Description: "Search workspace UTF-8 files using a Go-style regular expression or fixed text. Ignores .git, .plume, node_modules, .venv and symlinks. Returns matched lines plus optional context and explicit truncation.", Parameters: `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string","description":"Workspace-relative file or directory, default ."},"glob":{"type":"string","description":"Optional file filter, supports **."},"fixed_strings":{"type":"boolean"},"ignore_case":{"type":"boolean"},"context":{"type":"integer","minimum":0,"maximum":10},"limit":{"type":"integer","minimum":1,"description":"Maximum matching lines, default search_results."}},"required":["pattern"],"additionalProperties":false}`, Execute: w.grep},
		"glob":  {Name: "glob", Description: "List workspace-relative regular file paths matching a glob with **. Ignores generated/private directories and symlinks; results are sorted and explicitly truncated.", Parameters: `{"type":"object","properties":{"pattern":{"type":"string","description":"Glob relative to path; ** matches nested directories."},"path":{"type":"string","description":"Workspace-relative directory, default ."},"limit":{"type":"integer","minimum":1}},"required":["pattern"],"additionalProperties":false}`, Execute: w.glob},
		"edit":  {Name: "edit", Description: "Atomically replace exact text in a previously read workspace UTF-8 file. Default requires exactly one match. replace_all permits multiple matches. Rejects stale file content and preserves permissions.", Parameters: `{"type":"object","properties":{"path":{"type":"string"},"old_string":{"type":"string","minLength":1},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["path","old_string","new_string"],"additionalProperties":false}`, Execute: w.edit},
		"write": {Name: "write", Description: "Atomically create a workspace UTF-8 file, creating parent directories. Existing files require overwrite=true and a prior read of unchanged content. Preserves existing permissions and rejects symlink destinations.", Parameters: `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"overwrite":{"type":"boolean","description":"Default false. Existing content must have been read and remain unchanged."}},"required":["path","content"],"additionalProperties":false}`, Execute: w.write},
		"bash":  {Name: "bash", Description: "Execute an explicitly supplied foreground command using the configured local non-interactive shell (default bash). cwd is workspace or its subdirectory. This is not a shell sandbox: commands can access the host. Sensitive environment variables are removed. Returns combined bounded output, exit_code, elapsed_ms and truncation; cancellation/timeout kills the process group on Unix.", Parameters: fmt.Sprintf(`{"type":"object","properties":{"command":{"type":"string","minLength":1},"workdir":{"type":"string","description":"Workspace-relative directory, default ."},"timeout_seconds":{"type":"integer","minimum":1,"default":%d,"description":"Positive command timeout; outer tool timeout still applies."}},"required":["command"],"additionalProperties":false}`, options.BashTimeoutSeconds), Execute: w.bash},
	}
	for name, d := range Builtins(time.Now).definitions {
		available[name] = d
	}
	var definitions []Definition
	seen := map[string]bool{}
	for _, name := range options.Enabled {
		d, ok := available[name]
		if !ok || seen[name] {
			root.Close()
			return nil, errors.New("unknown or duplicate workspace tool")
		}
		seen[name] = true
		execute := d.Execute
		d.Execute = func(ctx context.Context, args string) Result { return w.boundResult(execute(ctx, args)) }
		definitions = append(definitions, d)
	}
	r, err := New(definitions...)
	if err != nil {
		root.Close()
		return nil, err
	}
	r.workspace = w
	return r, nil
}

// boundResult 最后核对完整 JSON（含路径与统计），不能只按正文原始字节计算预算。
// 优先保留正文前缀或搜索子集；连元数据也放不下时以 output_omitted 标识，并保留截断状态。
func (w *workspace) boundResult(result Result) Result {
	budget := w.options.ResultBytes
	if len(result.JSON()) <= budget {
		return result
	}
	result.Truncated = true
	value, ok := result.Value.(map[string]any)
	if !ok {
		result.Value = nil
		return result
	}
	if path, ok := value["path"].(string); ok {
		clipped, _ := clipText(path, budget/4)
		value["path"] = clipped
	}
	for _, field := range []string{"content", "output"} {
		text, ok := value[field].(string)
		if !ok || len(result.JSON()) <= budget {
			continue
		}
		value[field] = ""
		available := budget - len(result.JSON()) + 2
		text, _ = clipText(text, max(0, available))
		value[field] = text
	}
	if matches, ok := value["matches"].([]searchMatch); ok {
		for len(matches) > 0 && len(result.JSON()) > budget {
			matches = matches[:len(matches)-1]
			value["matches"] = matches
			value["count"] = len(matches)
		}
	}
	if paths, ok := value["paths"].([]string); ok {
		for len(paths) > 0 && len(result.JSON()) > budget {
			paths = paths[:len(paths)-1]
			value["paths"] = paths
			value["count"] = len(paths)
		}
	}
	if len(result.JSON()) > budget {
		result.Value = map[string]any{"output_omitted": true}
	}
	return result
}

// Close 释放根目录句柄；重复关闭安全，Builtins 无资源可释放。
func (r *Registry) Close() error {
	if r == nil || r.workspace == nil {
		return nil
	}
	w := r.workspace
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.root.Close()
}

// Workspace 返回实际解析后的绝对工作目录；无工作区的注册表返回空字符串。
func (r *Registry) Workspace() string {
	if r == nil || r.workspace == nil {
		return ""
	}
	return r.workspace.path
}

// workspacePath 拒绝绝对/卷标/上级路径；真正的链接与目录越界防护仍由 os.Root 执行。
func workspacePath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", errors.New("invalid path")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid path")
	}
	return clean, nil
}

// resolvedWorkspaceDir 给 exec.Cmd 提供实际目录并再次检查链接范围，不能据此宣称 Shell 沙箱化。
func resolvedWorkspaceDir(root, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, path))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if _, err := workspacePath(rel); err != nil {
		return "", err
	}
	return resolved, nil
}

func fileFailure(err error) Result {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Result{Code: "cancelled"}
	}
	if errors.Is(err, os.ErrNotExist) {
		return Result{Code: "not_found"}
	}
	if errors.Is(err, os.ErrPermission) {
		return Result{Code: "permission_denied"}
	}
	return Result{Code: "file_error"}
}

// loadText 在打开前后检查普通文件，限制内存并拒绝二进制；Unix 非阻塞打开避免 FIFO 换入后阻塞。
func (w *workspace) loadText(ctx context.Context, path string) ([]byte, os.FileInfo, Result) {
	if ctx.Err() != nil {
		return nil, nil, Result{Code: "cancelled"}
	}
	info, err := w.root.Stat(path)
	if err != nil {
		return nil, nil, fileFailure(err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, Result{Code: "not_regular_file"}
	}
	if info.Size() > int64(w.options.MaxFileBytes) {
		return nil, nil, Result{Code: "file_too_large"}
	}
	f, err := openWorkspaceFile(w.root, path)
	if err != nil {
		return nil, nil, fileFailure(err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, nil, fileFailure(err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, Result{Code: "not_regular_file"}
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(w.options.MaxFileBytes)+1))
	if err != nil {
		return nil, nil, fileFailure(err)
	}
	if ctx.Err() != nil {
		return nil, nil, Result{Code: "cancelled"}
	}
	if len(data) > w.options.MaxFileBytes {
		return nil, nil, Result{Code: "file_too_large"}
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return nil, nil, Result{Code: "not_text"}
	}
	return data, info, Result{OK: true}
}

// clipText 同时限制原始字节和 JSON 转义字节，截断保持 UTF-8 完整。
func clipText(text string, limit int) (string, bool) {
	if limit < 0 {
		limit = 0
	}
	fits := func(s string) bool { b, _ := json.Marshal(s); return len(s) <= limit && len(b) <= limit }
	if fits(text) {
		return text, false
	}
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(text[:mid]) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && !utf8.ValidString(text[:lo]) {
		lo--
	}
	return text[:lo], true
}
func (w *workspace) textLimit() int {
	limit := min(w.options.MaxOutputBytes, w.options.ResultBytes-512)
	if limit < 1 {
		limit = 1
	}
	return limit
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func (w *workspace) read(ctx context.Context, raw string) Result {
	var args struct {
		Path   string `json:"path"`
		Offset *int   `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	if decodeArguments(raw, &args) != nil {
		return Result{Code: "invalid_arguments"}
	}
	path, err := workspacePath(args.Path)
	if err != nil {
		return Result{Code: "invalid_arguments"}
	}
	offset, limit := 1, w.options.ReadLines
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	if offset < 1 || limit < 1 || limit > w.options.ReadLines {
		return Result{Code: "invalid_arguments"}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	data, _, status := w.loadText(ctx, path)
	if !status.OK {
		return status
	}
	lines := splitLines(data)
	if offset > len(lines)+1 {
		return Result{Code: "invalid_arguments"}
	}
	end := offset - 1 + min(len(lines)-(offset-1), limit)
	var b strings.Builder
	next := offset
	truncated := false
	partialLine := false
	for i := offset - 1; i < end; i++ {
		if ctx.Err() != nil {
			return Result{Code: "cancelled"}
		}
		piece := fmt.Sprintf("%d: %s\n", i+1, lines[i])
		candidate := b.String() + piece
		_, cut := clipText(candidate, w.textLimit())
		if cut {
			truncated = true
			if b.Len() == 0 {
				clipped, _ := clipText(piece, w.textLimit())
				b.WriteString(clipped)
				next = i + 2
				// 极长首行只能返回前缀，next_offset 跳到下一行；没有字节级续读，须明确标记。
				partialLine = true
			}
			break
		}
		b.WriteString(piece)
		next = i + 2
	}
	content := b.String()
	truncated = truncated || next <= len(lines)
	w.seen[path] = sha256.Sum256(data)
	return Result{OK: true, Value: map[string]any{"path": filepath.ToSlash(path), "content": content, "next_offset": next, "total_lines": len(lines), "partial_line": partialLine}, Truncated: truncated, Summary: fmt.Sprintf("read %d lines", max(0, next-offset))}
}
