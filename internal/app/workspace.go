package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// WorkspaceOptions 控制固定工作目录的内置只读采集；未启用的字段不采集。
type WorkspaceOptions struct {
	Dir                string
	GitEnabled         bool
	EnvironmentEnabled bool
	RefreshInterval    time.Duration
	GitTimeout         time.Duration
}

// WorkspaceStatus 是一次本地采集快照，stale 表示保留的值已无法重新确认。
// 禁用字段为空；非适用为 —；读取失败为 unknown。
// Dir 保留规范绝对原路径作为快照身份，展示时另行净化且不写入日志。
type WorkspaceStatus struct {
	Dir      string
	Git      string
	UVEnv    string
	GitStale bool
	UVStale  bool
}

type workspaceDependencies struct {
	runGit func(context.Context, string, ...string) ([]byte, error)
	stat   func(string) (os.FileInfo, error)
	getenv func(string) string
}

type workspaceReader struct {
	opts WorkspaceOptions
	deps workspaceDependencies
}

func newWorkspaceReader(opts WorkspaceOptions, deps workspaceDependencies) workspaceReader {
	if dir, err := filepath.Abs(opts.Dir); err == nil {
		opts.Dir = dir
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = 5 * time.Second
	}
	if opts.GitTimeout <= 0 {
		opts.GitTimeout = 500 * time.Millisecond
	}
	if deps.runGit == nil {
		deps.runGit = runWorkspaceGit
	}
	if deps.stat == nil {
		deps.stat = os.Stat
	}
	if deps.getenv == nil {
		deps.getenv = os.Getenv
	}
	return workspaceReader{opts: opts, deps: deps}
}

// ReadWorkspace 同步读取一次快照；应用交互路径应使用 WatchWorkspace。
func ReadWorkspace(ctx context.Context, opts WorkspaceOptions) WorkspaceStatus {
	return newWorkspaceReader(opts, workspaceDependencies{}).read(ctx, WorkspaceStatus{})
}

// WatchWorkspace 立即异步采集，之后按间隔串行刷新。通道只保留最新快照，
// 消费者暂停不会阻塞采集；取消后终止 Git 子进程并关闭通道。
func WatchWorkspace(ctx context.Context, opts WorkspaceOptions) <-chan WorkspaceStatus {
	return watchWorkspace(ctx, newWorkspaceReader(opts, workspaceDependencies{}))
}

func watchWorkspace(ctx context.Context, reader workspaceReader) <-chan WorkspaceStatus {
	updates := make(chan WorkspaceStatus, 1)
	go func() {
		defer close(updates)
		var previous WorkspaceStatus
		collect := func() bool {
			if ctx.Err() != nil {
				return false
			}
			previous = reader.read(ctx, previous)
			if ctx.Err() != nil {
				return false
			}
			select {
			case updates <- previous:
				return true
			default:
			}
			// 本 goroutine 是唯一发送者，移除旧快照后发送一定不会阻塞。
			select {
			case <-updates:
			default:
			}
			updates <- previous
			return true
		}
		if !collect() {
			return
		}
		if !reader.opts.GitEnabled && !reader.opts.EnvironmentEnabled {
			<-ctx.Done()
			return
		}
		timer := time.NewTimer(reader.opts.RefreshInterval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if !collect() {
					return
				}
				timer.Reset(reader.opts.RefreshInterval)
			}
		}
	}()
	return updates
}

func (r workspaceReader) read(ctx context.Context, previous WorkspaceStatus) WorkspaceStatus {
	status := WorkspaceStatus{Dir: r.opts.Dir}
	if r.opts.GitEnabled {
		value, err := r.readGit(ctx)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			status.Git, status.GitStale = previous.Git, true
			if status.Git == "" {
				status.Git = "unknown"
			}
		} else if err != nil {
			status.Git = "unknown"
		} else {
			status.Git = cleanWorkspaceText(value)
		}
	}
	if r.opts.EnvironmentEnabled {
		value, err := r.readEnvironment(ctx)
		if err != nil {
			status.UVEnv, status.UVStale = previous.UVEnv, true
			if status.UVEnv == "" {
				status.UVEnv = "unknown"
			}
		} else {
			status.UVEnv = cleanWorkspaceText(value)
		}
	}
	return status
}

func (r workspaceReader) readGit(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.opts.GitTimeout)
	defer cancel()
	output, err := r.deps.runGit(ctx, r.opts.Dir, "-c", "core.fsmonitor=false", "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 128 && strings.HasPrefix(string(exitError.Stderr), "fatal: not a git repository") {
			found, metadataErr := r.hasGitMetadata()
			if metadataErr == nil && !found {
				return "—", nil
			}
		}
		return "", err
	}
	var head, oid string
	dirty := false
	for line := range strings.SplitSeq(string(output), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			head = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.oid "):
			oid = strings.TrimPrefix(line, "# branch.oid ")
		case line != "" && !strings.HasPrefix(line, "# "):
			dirty = true
		}
	}
	if head == "" || oid == "" {
		return "", errors.New("invalid git status")
	}
	if oid == "(initial)" {
		head = "unborn"
	} else if head == "(detached)" {
		if len(oid) < 7 {
			return "", errors.New("invalid git object id")
		}
		head = oid[:7]
	}
	if dirty {
		head += "*"
	}
	return head, nil
}

func (r workspaceReader) hasGitMetadata() (bool, error) {
	info, err := r.deps.stat(r.opts.Dir)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, errors.New("workspace is not a directory")
	}
	for dir := r.opts.Dir; ; dir = filepath.Dir(dir) {
		if _, err := r.deps.stat(filepath.Join(dir, ".git")); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if filepath.Dir(dir) == dir {
			return false, nil
		}
	}
}

func (r workspaceReader) readEnvironment(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	directory, err := r.deps.stat(r.opts.Dir)
	if err != nil {
		return "", err
	}
	if !directory.IsDir() {
		return "", errors.New("workspace is not a directory")
	}
	var projectEnv os.FileInfo
	var uv bool
	for dir := r.opts.Dir; ; dir = filepath.Dir(dir) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		lock, err := r.optionalStat(filepath.Join(dir, "uv.lock"))
		if err != nil {
			return "", err
		}
		project, err := r.optionalStat(filepath.Join(dir, "pyproject.toml"))
		if err != nil {
			return "", err
		}
		env, err := r.optionalStat(filepath.Join(dir, ".venv"))
		if err != nil {
			return "", err
		}
		if env != nil && !env.IsDir() {
			env = nil
		}
		uv = lock != nil && !lock.IsDir()
		if uv || project != nil && !project.IsDir() || env != nil {
			projectEnv = env
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	var activeEnv os.FileInfo
	activePath := r.deps.getenv("VIRTUAL_ENV")
	if activePath != "" {
		if !filepath.IsAbs(activePath) {
			activePath = filepath.Join(r.opts.Dir, activePath)
		}
		var err error
		activeEnv, err = r.optionalStat(activePath)
		if err != nil {
			return "", err
		}
		if activeEnv != nil && !activeEnv.IsDir() {
			activeEnv = nil
		}
	}
	var parts []string
	localActive := projectEnv != nil && activeEnv != nil && os.SameFile(projectEnv, activeEnv)
	if projectEnv != nil {
		prefix, state := "venv:", "present"
		if uv {
			prefix = "uv/"
		}
		if localActive {
			state = "active"
		}
		parts = append(parts, prefix+".venv("+state+")")
	} else if uv {
		parts = append(parts, "uv")
	}
	if activeEnv != nil && !localActive {
		parts = append(parts, "venv:"+filepath.Base(filepath.Clean(activePath))+"(active)")
	}
	if len(parts) == 0 {
		return "—", nil
	}
	return strings.Join(parts, " | "), nil
}

func (r workspaceReader) optionalStat(path string) (os.FileInfo, error) {
	info, err := r.deps.stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return info, err
}

func runWorkspaceGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// cwd 是唯一仓库来源，避免启动环境里的 Git 路径/配置覆盖将读取重定向。
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_OPTIONAL_LOCKS", "LC_ALL":
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	// 固定错误语言只用于识别非仓库，stderr 正文始终不对外暴露。
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	cmd.WaitDelay = 100 * time.Millisecond
	// 只接收 stdout，错误与 stderr 均不进入快照。
	return cmd.Output()
}

func cleanWorkspaceText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(value))
}
