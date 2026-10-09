package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// boundedOutput 始终接受写入，但只保留前缀；不让大量 stdout/stderr 占满内存。
type boundedOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := len(p)
	keep := min(count, max(0, b.limit-len(b.data)))
	b.data = append(b.data, p[:keep]...)
	if keep < count {
		b.truncated = true
	}
	return count, nil
}
func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := string(b.data)
	for len(text) > 0 && !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	return text
}

func filteredEnvironment() []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL") || strings.Contains(upper, "PRIVATE_KEY") || upper == "BASH_ENV" || upper == "ENV" || strings.HasPrefix(upper, "BASH_FUNC_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

func (w *workspace) bash(ctx context.Context, raw string) Result {
	var args struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
		Timeout *int   `json:"timeout_seconds"`
	}
	if decodeArguments(raw, &args) != nil || strings.TrimSpace(args.Command) == "" || strings.ContainsRune(args.Command, 0) {
		return Result{Code: "invalid_arguments"}
	}
	if args.Workdir == "" {
		args.Workdir = "."
	}
	path, err := workspacePath(args.Workdir)
	if err != nil {
		return Result{Code: "invalid_arguments"}
	}
	timeout := w.options.BashTimeoutSeconds
	if args.Timeout != nil {
		timeout = *args.Timeout
	}
	if timeout < 1 || int64(timeout) > int64((1<<63-1)/int64(time.Second)) {
		return Result{Code: "invalid_arguments"}
	}
	// 固定工作目录必须实际留在工作区；Shell 本身拥有用户权限，不构成沙箱。
	dir, err := w.root.OpenRoot(path)
	if err != nil {
		return fileFailure(err)
	}
	dir.Close()
	working, err := resolvedWorkspaceDir(w.path, path)
	if err != nil {
		return Result{Code: "invalid_arguments"}
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	shellArgs := []string{"-c", args.Command}
	switch filepath.Base(w.options.Shell) {
	case "bash":
		shellArgs = append([]string{"--noprofile", "--norc"}, shellArgs...)
	case "zsh":
		shellArgs = append([]string{"-f"}, shellArgs...)
	}
	cmd := exec.CommandContext(commandCtx, w.options.Shell, shellArgs...)
	cmd.Dir = working
	cmd.Env = filteredEnvironment()
	configureProcessGroup(cmd)
	cmd.WaitDelay = 200 * time.Millisecond
	output := &boundedOutput{limit: w.options.MaxOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	started := time.Now()
	err = cmd.Run()
	_ = killProcessGroup(cmd)
	text, cut := clipText(output.String(), w.textLimit())
	truncated := output.truncated || cut
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			exitCode = exit.ExitCode()
		}
	}
	value := map[string]any{"output": text, "exit_code": exitCode, "elapsed_ms": time.Since(started).Milliseconds()}
	result := Result{OK: true, Value: value, Truncated: truncated, Summary: fmt.Sprintf("bash exit %d", exitCode)}
	if ctx.Err() != nil {
		result.OK = false
		result.Code = "cancelled"
	} else if commandCtx.Err() != nil {
		result.OK = false
		result.Code = "timeout"
	} else if err != nil {
		var exit *exec.ExitError
		result.OK = false
		if !errors.As(err, &exit) {
			result.Code = "shell_error"
		} else {
			result.Code = "command_failed"
		}
	}
	return result
}
