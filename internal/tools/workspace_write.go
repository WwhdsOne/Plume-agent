package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// checkedExisting 要求调用者持有 w.mu，核对最后一次 read 的全文摘要且拒绝直接写入符号链接。
// 摘要只验证内容，不锁住外部进程；发布前仍须再次核对，原文件权限用于新临时文件。
func (w *workspace) checkedExisting(ctx context.Context, path string) ([]byte, os.FileMode, Result) {
	info, err := w.root.Lstat(path)
	if err != nil {
		return nil, 0, fileFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, Result{Code: "symlink_destination"}
	}
	data, info, status := w.loadText(ctx, path)
	if !status.OK {
		return nil, 0, status
	}
	seen, ok := w.seen[path]
	if !ok {
		return nil, 0, Result{Code: "read_required"}
	}
	if sha256.Sum256(data) != seen {
		return nil, 0, Result{Code: "stale_read"}
	}
	return data, info.Mode().Perm(), Result{OK: true}
}

func (w *workspace) write(ctx context.Context, raw string) Result {
	var args struct {
		Path      string  `json:"path"`
		Content   *string `json:"content"`
		Overwrite bool    `json:"overwrite"`
	}
	if decodeArguments(raw, &args) != nil || args.Content == nil || !utf8.ValidString(*args.Content) || strings.ContainsRune(*args.Content, 0) {
		return Result{Code: "invalid_arguments"}
	}
	path, err := workspacePath(args.Path)
	if err != nil || path == "." {
		return Result{Code: "invalid_arguments"}
	}
	if len(*args.Content) > w.options.MaxFileBytes {
		return Result{Code: "file_too_large"}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.root.Lstat(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fileFailure(err)
	}
	mode := os.FileMode(0644)
	if exists {
		if !args.Overwrite {
			return Result{Code: "already_exists"}
		}
		_, existingMode, status := w.checkedExisting(ctx, path)
		if !status.OK {
			return status
		}
		mode = existingMode
	}
	if result := w.atomicWrite(ctx, path, []byte(*args.Content), mode, exists); !result.OK {
		return result
	}
	delete(w.seen, path)
	return Result{OK: true, Value: map[string]any{"path": filepath.ToSlash(path), "bytes": len(*args.Content), "created": !exists}, Summary: fmt.Sprintf("wrote %d bytes", len(*args.Content))}
}

func (w *workspace) edit(ctx context.Context, raw string) Result {
	var args struct {
		Path string  `json:"path"`
		Old  *string `json:"old_string"`
		New  *string `json:"new_string"`
		All  bool    `json:"replace_all"`
	}
	if decodeArguments(raw, &args) != nil || args.Old == nil || *args.Old == "" || args.New == nil || !utf8.ValidString(*args.Old) || !utf8.ValidString(*args.New) || strings.ContainsRune(*args.New, 0) {
		return Result{Code: "invalid_arguments"}
	}
	path, err := workspacePath(args.Path)
	if err != nil || path == "." {
		return Result{Code: "invalid_arguments"}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	data, mode, status := w.checkedExisting(ctx, path)
	if !status.OK {
		return status
	}
	count := strings.Count(string(data), *args.Old)
	if count == 0 {
		return Result{Code: "no_match"}
	}
	if count > 1 && !args.All {
		return Result{Code: "multiple_matches"}
	}
	// 先算替换后的长度，避免大替换文本放大到输出限制以外。
	if int64(len(data))+int64(count)*int64(len(*args.New)-len(*args.Old)) > int64(w.options.MaxFileBytes) {
		return Result{Code: "file_too_large"}
	}
	text := strings.ReplaceAll(string(data), *args.Old, *args.New)
	if result := w.atomicWrite(ctx, path, []byte(text), mode, true); !result.OK {
		return result
	}
	delete(w.seen, path)
	return Result{OK: true, Value: map[string]any{"path": filepath.ToSlash(path), "replacements": count, "bytes": len(text)}, Summary: fmt.Sprintf("edited %d matches", count)}
}

// atomicWrite 要求调用者持有 w.mu；同目录临时文件完整同步后再发布，早期失败由 defer 清理。
// 新建用原子 Link 防止覆盖竞争文件；覆盖用 Rename，最后摘要检查与发布间仍有外部修改窗口。
// 目录 fsync 在发布后执行，因此 durability_error 必须携带 applied:true，不能声称没有副作用。
func (w *workspace) atomicWrite(ctx context.Context, path string, data []byte, mode os.FileMode, replace bool) Result {
	if ctx.Err() != nil {
		return Result{Code: "cancelled"}
	}
	dir := filepath.Dir(path)
	if err := w.root.MkdirAll(dir, 0700); err != nil {
		return fileFailure(err)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Result{Code: "file_error"}
	}
	temporary := filepath.Join(dir, ".plume-write-"+hex.EncodeToString(token[:]))
	f, err := w.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fileFailure(err)
	}
	// 临时文件是尽力清理：失败也由上层在下次写入时重建，不改变本次结果。
	defer func() { _ = w.root.Remove(temporary) }()
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fileFailure(err)
	}
	if ctx.Err() != nil {
		return Result{Code: "cancelled"}
	}
	if replace {
		// 发布前再次核对，尽量缩短外部修改与 Rename 之间的窗口。
		if _, _, status := w.checkedExisting(ctx, path); !status.OK {
			return status
		}
		err = w.root.Rename(temporary, path)
	} else {
		err = w.root.Link(temporary, path)
		if errors.Is(err, os.ErrExist) {
			return Result{Code: "already_exists"}
		}
	}
	if err != nil {
		return fileFailure(err)
	}
	if err = syncWorkspaceDirectory(w.root, dir); err != nil {
		return Result{Code: "durability_error", Value: map[string]any{"applied": true}}
	}
	return Result{OK: true}
}
