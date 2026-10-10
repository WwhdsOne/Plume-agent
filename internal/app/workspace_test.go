package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestReadWorkspaceGitStates 守住 Git 状态快照：正确区分非仓库、unborn、detached 与脏工作区
// （跟踪/未跟踪改动都追加 *），且不误报 stale。
func TestReadWorkspaceGitStates(t *testing.T) {
	t.Setenv("VIRTUAL_ENV", "")
	tests := []struct {
		name    string
		prepare func(*testing.T, string) string
		want    string
	}{
		{name: "non repository", want: "—"},
		{name: "unborn", prepare: func(t *testing.T, dir string) string {
			workspaceGitFixture(t, dir, "init", "-b", "main")
			return "unborn"
		}},
		{name: "clean branch", prepare: func(t *testing.T, dir string) string {
			workspaceCommitFixture(t, dir)
			return "main"
		}},
		{name: "tracked modification", prepare: func(t *testing.T, dir string) string {
			workspaceCommitFixture(t, dir)
			workspaceWriteFixture(t, filepath.Join(dir, "tracked"), "modified")
			return "main*"
		}},
		{name: "untracked modification", prepare: func(t *testing.T, dir string) string {
			workspaceCommitFixture(t, dir)
			workspaceWriteFixture(t, filepath.Join(dir, "untracked"), "new")
			return "main*"
		}},
		{name: "detached", prepare: func(t *testing.T, dir string) string {
			workspaceCommitFixture(t, dir)
			workspaceGitFixture(t, dir, "checkout", "--detach", "HEAD")
			return strings.TrimSpace(workspaceGitFixture(t, dir, "rev-parse", "--short=7", "HEAD"))
		}},
		{name: "unborn dirty", prepare: func(t *testing.T, dir string) string {
			workspaceGitFixture(t, dir, "init", "-b", "main")
			workspaceWriteFixture(t, filepath.Join(dir, "untracked"), "new")
			return "unborn*"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			want := tt.want
			if tt.prepare != nil {
				want = tt.prepare(t, dir)
			}
			got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: dir, GitEnabled: true})
			if got.Git != want || got.GitStale || got.UVEnv != "" || got.Dir != dir {
				t.Fatalf("snapshot = %+v, want git %q without stale", got, want)
			}
		})
	}
}

// TestReadWorkspaceGitMissingCommandIsUnknown 守住 git 缺失降级：命令不存在时报 unknown，
// 不报错、不标记 stale。
func TestReadWorkspaceGitMissingCommandIsUnknown(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true})
	if got.Git != "unknown" || got.GitStale {
		t.Fatalf("snapshot = %+v, want unknown without stale", got)
	}
}

// TestReadWorkspaceGitExit128WithoutRepositoryErrorIsUnknown 守住失败分类：git 异常退出但
// stderr 无"非仓库"语义时归为 unknown（不得误判为非仓库），错误输出不进入快照。
func TestReadWorkspaceGitExit128WithoutRepositoryErrorIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX only")
	}
	binDir := t.TempDir()
	workspaceExecutableFixture(t, filepath.Join(binDir, "git"), "#!/bin/sh\nprintf 'private-token=secret' >&2\nexit 128\n")
	t.Setenv("PATH", binDir)
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true})
	if got.Git != "unknown" {
		t.Fatalf("command failure misclassified as non-repository: %+v", got)
	}
}

// TestReadWorkspaceDirectoryRetainsIdentity 守住目录身份：Dir 字段原样保留传入的绝对路径
// （含控制字符），净化只作用于展示文本。
func TestReadWorkspaceDirectoryRetainsIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "directory\x1b[31m\n")
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: dir})
	if got.Dir != dir {
		t.Fatalf("directory identity = %q, want original absolute path %q", got.Dir, dir)
	}
}

// TestReadWorkspaceEnvironmentStates 守住环境状态组合：按 uv.lock、项目 .venv 与外部
// VIRTUAL_ENV 的有无/激活组合给出正确状态文本，且不误报 stale。
func TestReadWorkspaceEnvironmentStates(t *testing.T) {
	tests := []struct {
		name                                                  string
		uv, local, activeLocal, activeExternal, missingActive bool
		want                                                  string
	}{
		{name: "none", want: "—"},
		{name: "lock alone", uv: true, want: "uv"},
		{name: "uv environment present", uv: true, local: true, want: "uv/.venv(present)"},
		{name: "uv environment active", uv: true, local: true, activeLocal: true, want: "uv/.venv(active)"},
		{name: "venv present without uv", local: true, want: "venv:.venv(present)"},
		{name: "venv active without uv", local: true, activeLocal: true, want: "venv:.venv(active)"},
		{name: "external active without uv", activeExternal: true, want: "venv:external(active)"},
		{name: "uv external active", uv: true, activeExternal: true, want: "uv | venv:external(active)"},
		{name: "both environments", uv: true, local: true, activeExternal: true, want: "uv/.venv(present) | venv:external(active)"},
		{name: "nonexistent active environment", missingActive: true, want: "—"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("VIRTUAL_ENV", "")
			if tt.uv {
				workspaceWriteFixture(t, filepath.Join(dir, "uv.lock"), "version = 1")
			}
			if tt.local {
				workspaceMkdirFixture(t, filepath.Join(dir, ".venv"))
			}
			if tt.activeLocal {
				t.Setenv("VIRTUAL_ENV", filepath.Join(dir, ".venv"))
			}
			if tt.activeExternal {
				external := filepath.Join(t.TempDir(), "external")
				workspaceMkdirFixture(t, external)
				t.Setenv("VIRTUAL_ENV", external)
			}
			if tt.missingActive {
				t.Setenv("VIRTUAL_ENV", filepath.Join(dir, "missing"))
			}
			got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: dir, EnvironmentEnabled: true})
			if got.UVEnv != tt.want || got.UVStale || got.Git != "" {
				t.Fatalf("snapshot = %+v, want environment %q without stale", got, tt.want)
			}
		})
	}
}

// TestReadWorkspaceEnvironmentFindsProjectFromSubdirectory 守住向上查找：从子目录仍能发现
// 父项目根的环境，快照 Dir 保持子目录身份。
func TestReadWorkspaceEnvironmentFindsProjectFromSubdirectory(t *testing.T) {
	t.Setenv("VIRTUAL_ENV", "")
	dir := t.TempDir()
	workspaceWriteFixture(t, filepath.Join(dir, "uv.lock"), "version = 1")
	workspaceMkdirFixture(t, filepath.Join(dir, ".venv"))
	subdir := filepath.Join(dir, "src", "nested")
	workspaceMkdirFixture(t, subdir)
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: subdir, EnvironmentEnabled: true})
	if got.UVEnv != "uv/.venv(present)" || got.Dir != subdir {
		t.Fatalf("snapshot = %+v", got)
	}
}

// TestReadWorkspaceEnvironmentStopsAtNearestProject 守住查找边界：向上查找在最近的项目根
// 停止，子项目不继承外层环境。
func TestReadWorkspaceEnvironmentStopsAtNearestProject(t *testing.T) {
	t.Setenv("VIRTUAL_ENV", "")
	dir := t.TempDir()
	workspaceWriteFixture(t, filepath.Join(dir, "uv.lock"), "version = 1")
	workspaceMkdirFixture(t, filepath.Join(dir, ".venv"))
	subdir := filepath.Join(dir, "nested-project")
	workspaceMkdirFixture(t, subdir)
	workspaceWriteFixture(t, filepath.Join(subdir, "pyproject.toml"), "[project]")
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: subdir, EnvironmentEnabled: true})
	if got.UVEnv != "—" {
		t.Fatalf("nested project inherited parent environment: %+v", got)
	}
}

// TestReadWorkspaceEnvironmentActiveSymlinkMatchesProject 守住激活匹配：VIRTUAL_ENV 为
// 符号链接时按解析后的真实路径与项目 .venv 匹配。
func TestReadWorkspaceEnvironmentActiveSymlinkMatchesProject(t *testing.T) {
	dir := t.TempDir()
	workspaceWriteFixture(t, filepath.Join(dir, "uv.lock"), "version = 1")
	workspaceMkdirFixture(t, filepath.Join(dir, ".venv"))
	link := filepath.Join(t.TempDir(), "active-link")
	if err := os.Symlink(filepath.Join(dir, ".venv"), link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	t.Setenv("VIRTUAL_ENV", link)
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: dir, EnvironmentEnabled: true})
	if got.UVEnv != "uv/.venv(active)" {
		t.Fatalf("symlink active environment = %+v", got)
	}
}

// TestReadWorkspaceEnvironmentFileIsNotAnActiveEnvironment 守住激活判定：VIRTUAL_ENV 指向
// 普通文件不算活动环境。
func TestReadWorkspaceEnvironmentFileIsNotAnActiveEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-an-environment")
	workspaceWriteFixture(t, path, "file")
	t.Setenv("VIRTUAL_ENV", path)
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: dir, EnvironmentEnabled: true})
	if got.UVEnv != "—" {
		t.Fatalf("regular file marked active: %+v", got)
	}
}

// TestReadWorkspaceEnvironmentNameIsSanitized 守住展示净化：外部环境名中的控制序列不得进入
// 状态文本。
func TestReadWorkspaceEnvironmentNameIsSanitized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("control characters in filename require POSIX filesystem")
	}
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "external\x1b[31m\n")
	workspaceMkdirFixture(t, external)
	t.Setenv("VIRTUAL_ENV", external)
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: dir, EnvironmentEnabled: true})
	if got.UVEnv != "venv:external(active)" {
		t.Fatalf("unsanitized environment name = %q", got.UVEnv)
	}
}

// TestReadWorkspaceDisabledFieldsStayEmpty 守住禁用语义：未启用的采集项保持空字符串（区别于
// unknown 占位），不产生 stale 标记。
func TestReadWorkspaceDisabledFieldsStayEmpty(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("VIRTUAL_ENV", t.TempDir())
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: t.TempDir()})
	if got.Git != "" || got.UVEnv != "" || got.GitStale || got.UVStale {
		t.Fatalf("disabled snapshot = %+v", got)
	}
}

// TestWatchWorkspaceImmediateSnapshotAndClose 守住 Watch 启动语义：立即发出首帧快照（不等
// 刷新间隔），取消后关闭通道且不再发布。
func TestWatchWorkspaceImmediateSnapshotAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updates := WatchWorkspace(ctx, WorkspaceOptions{Dir: t.TempDir(), RefreshInterval: time.Hour})
	select {
	case _, ok := <-updates:
		if !ok {
			t.Fatal("channel closed before initial snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("initial snapshot waits for refresh interval")
	}
	cancel()
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("received a snapshot after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancellation")
	}
}

// TestWorkspaceDisabledFieldsDoNotInvokeDependencies 守住零开销禁用：未启用的采集项不调用
// git/stat/getenv，并把旧值清空。
func TestWorkspaceDisabledFieldsDoNotInvokeDependencies(t *testing.T) {
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir()}, workspaceDependencies{
		runGit: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("disabled Git was invoked")
			return nil, nil
		},
		stat: func(string) (os.FileInfo, error) {
			t.Fatal("disabled environment was inspected")
			return nil, nil
		},
		getenv: func(string) string {
			t.Fatal("disabled environment variable was read")
			return ""
		},
	})
	got := reader.read(context.Background(), WorkspaceStatus{Git: "old", UVEnv: "old"})
	if got.Git != "" || got.UVEnv != "" {
		t.Fatalf("disabled snapshot = %+v", got)
	}
}

// TestWorkspaceGitTimeoutRetainsPreviousAndRecovers 守住超时降级：git 超时保留上次成功值并
// 标记 stale，恢复成功后清除 stale。
func TestWorkspaceGitTimeoutRetainsPreviousAndRecovers(t *testing.T) {
	calls := 0
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true, GitTimeout: 10 * time.Millisecond}, workspaceDependencies{
		runGit: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			calls++
			if calls == 2 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return workspaceStatusFixture(fmt.Sprintf("branch%d", calls)), nil
		},
	})
	first := reader.read(context.Background(), WorkspaceStatus{})
	stale := reader.read(context.Background(), first)
	if stale.Git != "branch1" || !stale.GitStale {
		t.Fatalf("timeout snapshot = %+v, want previous value marked stale", stale)
	}
	recovered := reader.read(context.Background(), stale)
	if recovered.Git != "branch3" || recovered.GitStale {
		t.Fatalf("recovered snapshot = %+v", recovered)
	}
}

// TestWorkspaceGitTimeoutWithoutPreviousIsUnknown 守住首采超时：没有上次值可退化时报 unknown
// 并标记 stale。
func TestWorkspaceGitTimeoutWithoutPreviousIsUnknown(t *testing.T) {
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true, GitTimeout: time.Millisecond}, workspaceDependencies{
		runGit: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	got := reader.read(context.Background(), WorkspaceStatus{})
	if got.Git != "unknown" || !got.GitStale {
		t.Fatalf("initial timeout = %+v", got)
	}
}

// TestWorkspaceGitFailureDoesNotExposeError 守住脱敏：git 失败只报 unknown，错误内容（可能含
// 敏感信息）不进入快照。
func TestWorkspaceGitFailureDoesNotExposeError(t *testing.T) {
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true}, workspaceDependencies{
		runGit: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("private-token=secret")
		},
	})
	got := reader.read(context.Background(), WorkspaceStatus{})
	if got.Git != "unknown" || got.GitStale {
		t.Fatalf("read failure = %+v, want unknown without error content", got)
	}
}

// TestWorkspaceEnvironmentReadFailureRetainsStale 守住环境探测降级：读取失败保留上次值并
// 标记 stale。
func TestWorkspaceEnvironmentReadFailureRetainsStale(t *testing.T) {
	t.Setenv("VIRTUAL_ENV", "")
	dir := t.TempDir()
	workspaceWriteFixture(t, filepath.Join(dir, "uv.lock"), "version = 1")
	fail := false
	reader := newWorkspaceReader(WorkspaceOptions{Dir: dir, EnvironmentEnabled: true}, workspaceDependencies{
		stat: func(path string) (os.FileInfo, error) {
			if fail {
				return nil, os.ErrPermission
			}
			return os.Stat(path)
		},
	})
	first := reader.read(context.Background(), WorkspaceStatus{})
	fail = true
	got := reader.read(context.Background(), first)
	if got.UVEnv != "uv" || !got.UVStale {
		t.Fatalf("environment read failure = %+v", got)
	}
}

// TestReadWorkspaceMissingDirectoryIsUnknown 守住缺失目录降级：目录不存在时采集项报 unknown
// 并标记 stale，不报错。
func TestReadWorkspaceMissingDirectoryIsUnknown(t *testing.T) {
	t.Setenv("VIRTUAL_ENV", "")
	got := ReadWorkspace(context.Background(), WorkspaceOptions{Dir: filepath.Join(t.TempDir(), "missing"), GitEnabled: true, EnvironmentEnabled: true})
	if got.Git != "unknown" || got.UVEnv != "unknown" || !got.UVStale {
		t.Fatalf("missing directory = %+v", got)
	}
}

// TestWorkspaceTextIsSanitized 守住文本净化：分支名中的终端控制序列（含 OSC 52 剪贴板注入）
// 必须剥除后才能进入快照。
func TestWorkspaceTextIsSanitized(t *testing.T) {
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true}, workspaceDependencies{
		runGit: func(context.Context, string, ...string) ([]byte, error) {
			return workspaceStatusFixture("main\x1b]52;c;secret\x07\x1b[31m\r"), nil
		},
	})
	got := reader.read(context.Background(), WorkspaceStatus{})
	if got.Git != "main" {
		t.Fatalf("unsanitized branch = %q", got.Git)
	}
}

// TestWatchWorkspaceLatestBufferNeverBlocksCollection 守住最新值缓冲：通道容量为 1、只保留
// 最新完成的快照，消费者停滞不阻塞后台采集。
func TestWatchWorkspaceLatestBufferNeverBlocksCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fourthStarted := make(chan struct{})
	calls := 0
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true, RefreshInterval: time.Millisecond, GitTimeout: time.Second}, workspaceDependencies{
		runGit: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			calls++
			if calls == 4 {
				close(fourthStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return workspaceStatusFixture(fmt.Sprintf("branch%d", calls)), nil
		},
	})
	updates := watchWorkspace(ctx, reader)
	select {
	case <-fourthStarted:
	case <-time.After(time.Second):
		t.Fatal("unread buffer blocked later collection")
	}
	if cap(updates) != 1 {
		t.Fatalf("buffer capacity = %d", cap(updates))
	}
	got := <-updates
	if got.Git != "branch3" {
		t.Fatalf("buffer = %+v, want latest completed snapshot", got)
	}
	cancel()
	workspaceAwaitClose(t, updates)
}

// TestWatchWorkspaceAlreadyCanceledDoesNotCollect 守住竞态起点：传入已取消的上下文时不做
// 任何采集即关闭通道。
func TestWatchWorkspaceAlreadyCanceledDoesNotCollect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true, EnvironmentEnabled: true}, workspaceDependencies{
		runGit: func(context.Context, string, ...string) ([]byte, error) {
			t.Error("already canceled collector invoked Git")
			return nil, nil
		},
		stat: func(string) (os.FileInfo, error) {
			t.Error("already canceled collector inspected environment")
			return nil, nil
		},
	})
	workspaceAwaitClose(t, watchWorkspace(ctx, reader))
}

// TestWatchWorkspaceSlowCollectionIsSerialAndCancelable 守住串行采集：启动不阻塞调用方，
// 上一轮未完成不开启下一轮、不发布中间快照，取消后立即收尾。
func TestWatchWorkspaceSlowCollectionIsSerialAndCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan struct{})
	var active, maximum, calls atomic.Int32
	reader := newWorkspaceReader(WorkspaceOptions{Dir: t.TempDir(), GitEnabled: true, RefreshInterval: time.Millisecond, GitTimeout: time.Second}, workspaceDependencies{
		runGit: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			calls.Add(1)
			current := active.Add(1)
			maximum.Store(current)
			defer active.Add(-1)
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	before := time.Now()
	updates := watchWorkspace(ctx, reader)
	if time.Since(before) > 100*time.Millisecond {
		t.Fatal("watcher blocked its caller on collection")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start")
	}
	select {
	case got := <-updates:
		t.Fatalf("unfinished collection published %+v", got)
	case <-time.After(10 * time.Millisecond):
	}
	cancel()
	workspaceAwaitClose(t, updates)
	if calls.Load() != 1 || maximum.Load() != 1 || active.Load() != 0 {
		t.Fatalf("calls=%d maximum=%d active=%d", calls.Load(), maximum.Load(), active.Load())
	}
}

// TestRunWorkspaceGitUsesFixedDirectoryArgumentsAndNoLocks 守住子进程契约：git 固定以目标
// 目录为工作目录、参数不经 shell 解释，并禁用可选锁、清掉继承的 GIT_DIR。
func TestRunWorkspaceGitUsesFixedDirectoryArgumentsAndNoLocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX only")
	}
	binDir, dir := t.TempDir(), t.TempDir()
	workspaceExecutableFixture(t, filepath.Join(binDir, "git"), "#!/bin/sh\npwd -P\nprintf '%s\\n' \"$GIT_OPTIONAL_LOCKS\" \"$GIT_DIR\" \"$@\"\n")
	t.Setenv("PATH", binDir)
	t.Setenv("GIT_DIR", "/private/other-repository")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	output, err := runWorkspaceGit(context.Background(), dir, "space argument", "literal;argument")
	if err != nil {
		t.Fatal(err)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := realDir + "\n0\n\nspace argument\nliteral;argument\n"
	if string(output) != want {
		t.Fatalf("child output = %q, want %q", output, want)
	}
}

// TestWatchWorkspaceCancellationReapsGitProcess 守住进程回收：取消 Watch 时杀掉仍在运行的
// git 子进程，不留孤儿。
func TestWatchWorkspaceCancellationReapsGitProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture is POSIX only")
	}
	binDir, dir := t.TempDir(), t.TempDir()
	pidPath := filepath.Join(dir, "git.pid")
	workspaceExecutableFixture(t, filepath.Join(binDir, "git"), "#!/bin/sh\nprintf '%s' \"$$\" > git.pid\nwhile :; do :; done\n")
	t.Setenv("PATH", binDir)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	updates := WatchWorkspace(ctx, WorkspaceOptions{Dir: dir, GitEnabled: true, GitTimeout: time.Second})
	deadline := time.Now().Add(time.Second)
	var pidBytes []byte
	for time.Now().Before(deadline) {
		var err error
		pidBytes, err = os.ReadFile(pidPath)
		if err == nil && len(pidBytes) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(pidBytes) == 0 {
		t.Fatal("Git subprocess did not start")
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	workspaceAwaitClose(t, updates)
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := process.Release(); err != nil {
			t.Errorf("release process handle: %v", err)
		}
	}()
	if err := process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Git process %d still exists: %v", pid, err)
	}
}

func workspaceStatusFixture(branch string) []byte {
	return []byte("# branch.oid 0123456789abcdef0123456789abcdef01234567\n# branch.head " + branch + "\n")
}

func workspaceAwaitClose(t *testing.T, updates <-chan WorkspaceStatus) {
	t.Helper()
	select {
	case _, ok := <-updates:
		if ok {
			t.Fatal("canceled collector published a snapshot")
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not close after cancellation")
	}
}

func workspaceExecutableFixture(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func workspaceGitFixture(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git fixture %v failed: %v: %s", args, err, output)
	}
	return string(output)
}

func workspaceCommitFixture(t *testing.T, dir string) {
	t.Helper()
	workspaceGitFixture(t, dir, "init", "-b", "main")
	workspaceWriteFixture(t, filepath.Join(dir, "tracked"), "initial")
	workspaceGitFixture(t, dir, "add", "tracked")
	workspaceGitFixture(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
}

func workspaceWriteFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func workspaceMkdirFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}
