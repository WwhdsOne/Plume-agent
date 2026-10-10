package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func workspaceFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultWorkspaceOptions()
	options.Root = root
	r, err := NewWorkspace(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r, root
}

func toolCall(t *testing.T, r *Registry, name string, args any) Result {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return r.Execute(context.Background(), name, string(b))
}

// TestWorkspaceDefinitionsAndSelection 守住工具集装配契约：默认工作区注册
// 全部六工具并绑定根目录，Enabled 只保留指定工具，未知工具名在构造时即报错。
func TestWorkspaceDefinitionsAndSelection(t *testing.T) {
	r, root := workspaceFixture(t)
	if r.Workspace() != root || len(r.Declarations()) != 6 {
		t.Fatalf("workspace=%q declarations=%+v", r.Workspace(), r.Declarations())
	}
	for _, name := range []string{"read", "grep", "glob", "edit", "write", "bash"} {
		if !r.Known(name) {
			t.Fatalf("missing %s", name)
		}
	}
	o := DefaultWorkspaceOptions()
	o.Root = root
	o.Enabled = []string{"read"}
	selected, err := NewWorkspace(o)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if selected.Known("bash") || len(selected.Declarations()) != 1 {
		t.Fatal("selection ignored")
	}
	o.Enabled = []string{"shell"}
	if invalid, err := NewWorkspace(o); err == nil {
		invalid.Close()
		t.Fatal("unknown enabled tool accepted")
	}
}

// TestWorkspaceReadPaginationAndBounds 守住 read 的分页与安全边界：
// offset/limit 精确翻页（next_offset 指向未读行）、二进制按 not_text 拒绝、
// 越界路径与指向外部的符号链接一律失败，摘要不得携带正文。
func TestWorkspaceReadPaginationAndBounds(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\nbeta\ngamma\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := toolCall(t, r, "read", map[string]any{"path": "a.txt", "offset": 2, "limit": 1})
	if !got.OK || !got.Truncated || !strings.Contains(got.JSON(), `2: beta`) || !strings.Contains(got.JSON(), `"next_offset":3`) {
		t.Fatalf("read: %s", got.JSON())
	}
	if got.Summary == "" || strings.Contains(got.Summary, "beta") {
		t.Fatalf("unsafe summary: %q", got.Summary)
	}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{0, 255}, 0600); err != nil {
		t.Fatal(err)
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "binary"}); got.Code != "not_text" {
		t.Fatalf("binary: %s", got.JSON())
	}
	for _, path := range []string{"../outside", "/etc/passwd", "."} {
		if got := toolCall(t, r, "read", map[string]any{"path": path}); got.OK {
			t.Fatalf("unsafe path: %q %s", path, got.JSON())
		}
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "link"}); got.OK {
		t.Fatal("symlink escaped")
	}
}

// TestWorkspaceWriteEditReadAndStaleGuard 守住"先读后改"守卫链：新建文件
// 默认 0644、改写既有文件必须先 read、外部改动后按 stale_read 拒绝覆盖；
// edit 的歧义与缺失分别报 multiple_matches/no_match，改写保留既有权限位。
func TestWorkspaceWriteEditReadAndStaleGuard(t *testing.T) {
	r, root := workspaceFixture(t)
	got := toolCall(t, r, "write", map[string]any{"path": "nested/a.txt", "content": "hello hello\n"})
	if !got.OK {
		t.Fatalf("create: %s", got.JSON())
	}
	createdInfo, err := os.Stat(filepath.Join(root, "nested/a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if createdInfo.Mode().Perm() != 0644 {
		t.Fatalf("default mode=%v", createdInfo.Mode())
	}
	if got := toolCall(t, r, "write", map[string]any{"path": "nested/a.txt", "content": "lost"}); got.Code != "already_exists" {
		t.Fatalf("overwrite: %s", got.JSON())
	}
	if got := toolCall(t, r, "write", map[string]any{"path": "nested/a.txt", "content": "lost", "overwrite": true}); got.Code != "read_required" {
		t.Fatalf("read guard: %s", got.JSON())
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "nested/a.txt"}); !got.OK {
		t.Fatal(got.JSON())
	}
	if got := toolCall(t, r, "edit", map[string]any{"path": "nested/a.txt", "old_string": "hello", "new_string": "hi"}); got.Code != "multiple_matches" {
		t.Fatalf("ambiguous: %s", got.JSON())
	}
	if got := toolCall(t, r, "edit", map[string]any{"path": "nested/a.txt", "old_string": "absent", "new_string": "hi"}); got.Code != "no_match" {
		t.Fatalf("absent: %s", got.JSON())
	}
	if err := os.Chmod(filepath.Join(root, "nested/a.txt"), 0640); err != nil {
		t.Fatal(err)
	}
	if got := toolCall(t, r, "edit", map[string]any{"path": "nested/a.txt", "old_string": "hello", "new_string": "hi", "replace_all": true}); !got.OK {
		t.Fatal(got.JSON())
	}
	data, err := os.ReadFile(filepath.Join(root, "nested/a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "nested/a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hi hi\n" || info.Mode().Perm() != 0640 {
		t.Fatalf("data=%q mode=%v", data, info.Mode())
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "nested/a.txt"}); !got.OK {
		t.Fatal(got.JSON())
	}
	if err := os.WriteFile(filepath.Join(root, "nested/a.txt"), []byte("external"), 0640); err != nil {
		t.Fatal(err)
	}
	if got := toolCall(t, r, "write", map[string]any{"path": "nested/a.txt", "content": "stale", "overwrite": true}); got.Code != "stale_read" {
		t.Fatalf("stale: %s", got.JSON())
	}
	data, _ = os.ReadFile(filepath.Join(root, "nested/a.txt"))
	if string(data) != "external" {
		t.Fatalf("clobbered: %q", data)
	}
}

// TestWorkspaceSearchGlobAndIgnore 守住搜索过滤契约：glob 过滤与
// ignore_case/fixed_strings 生效，.git 与 node_modules 默认不入结果，
// 非法正则报 invalid_pattern，limit 触发截断必须如实标记。
func TestWorkspaceSearchGlobAndIgnore(t *testing.T) {
	r, root := workspaceFixture(t)
	for path, text := range map[string]string{"a.go": "before\nNeedle.one\nafter\nneedle two\n", "src/b.go": "needle three\n", "src/b.txt": "needle four\n", ".git/hidden.go": "needle hidden\n", "node_modules/x.go": "needle hidden\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got := toolCall(t, r, "grep", map[string]any{"pattern": "needle", "ignore_case": true, "glob": "**/*.go", "context": 1, "limit": 1})
	if !got.OK || !got.Truncated || !strings.Contains(got.JSON(), "Needle.one") || !strings.Contains(got.JSON(), "before") || strings.Contains(got.JSON(), "hidden") {
		t.Fatalf("grep: %s", got.JSON())
	}
	got = toolCall(t, r, "grep", map[string]any{"pattern": "Needle.one", "fixed_strings": true})
	if !got.OK || !strings.Contains(got.JSON(), "Needle.one") {
		t.Fatalf("fixed grep: %s", got.JSON())
	}
	if got := toolCall(t, r, "grep", map[string]any{"pattern": "["}); got.Code != "invalid_pattern" {
		t.Fatalf("regexp: %s", got.JSON())
	}
	got = toolCall(t, r, "glob", map[string]any{"pattern": "**/*.go", "limit": 1})
	if !got.OK || !got.Truncated || !strings.Contains(got.JSON(), "a.go") || strings.Contains(got.JSON(), "hidden") {
		t.Fatalf("glob: %s", got.JSON())
	}
}

// TestWorkspaceBashOutputExitEnvironmentAndTimeout 守住 bash 的执行边界：
// 非零退出码如实上报、工作目录是根目录、stderr 被捕获，密钥类环境变量脱敏而
// 白名单变量可见；输出超预算截断，超时与取消必须及时返回（含后台子进程）。
func TestWorkspaceBashOutputExitEnvironmentAndTimeout(t *testing.T) {
	r, root := workspaceFixture(t)
	t.Setenv("EXAMPLE_API_KEY", "secret-marker")
	t.Setenv("EXAMPLE_SECRET", "secret-marker")
	t.Setenv("PLUME_TEST_VISIBLE", "visible-marker")
	got := toolCall(t, r, "bash", map[string]any{"command": "printf '%s\\n' \"$PWD\" \"$EXAMPLE_API_KEY\" \"$EXAMPLE_SECRET\" \"$PLUME_TEST_VISIBLE\"; printf stderr >&2; exit 7"})
	if got.OK || got.Code != "command_failed" || !strings.Contains(got.JSON(), `"exit_code":7`) || !strings.Contains(got.JSON(), root) || strings.Contains(got.JSON(), "secret-marker") || !strings.Contains(got.JSON(), "visible-marker") || !strings.Contains(got.JSON(), "stderr") {
		t.Fatalf("bash: %s", got.JSON())
	}
	got = toolCall(t, r, "bash", map[string]any{"command": "while :; do printf '01234567890123456789'; done", "timeout_seconds": 1})
	if got.Code != "timeout" || !got.Truncated || len(got.JSON()) > 65536 {
		t.Fatalf("bounded timeout: size=%d %s", len(got.JSON()), got.Code)
	}
	started := time.Now()
	got = toolCall(t, r, "bash", map[string]any{"command": "sleep 20 & wait", "timeout_seconds": 1})
	if got.Code != "timeout" || time.Since(started) > 4*time.Second {
		t.Fatalf("child timeout: %s elapsed=%s", got.JSON(), time.Since(started))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got = r.Execute(ctx, "bash", `{"command":"sleep 20 & wait"}`)
	if got.Code != "timeout" {
		t.Fatalf("cancel: %s", got.JSON())
	}
}

// TestRegistryCancellationKeepsCompletedOutput 守住取消语义的部分结果保留：
// 执行中发现上下文已取消时结果改标 cancelled，但工具已产出的输出与摘要
// 必须原样保留给上层。
func TestRegistryCancellationKeepsCompletedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, err := New(Definition{Name: "partial", Parameters: `{"type":"object"}`, Execute: func(context.Context, string) Result {
		cancel()
		return Result{OK: true, Value: "partial output", Truncated: true, Summary: "partial result"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Execute(ctx, "partial", `{}`)
	if got.OK || got.Code != "cancelled" || got.Value != "partial output" || !got.Truncated || got.Summary != "partial result" {
		t.Fatalf("lost cancelled result: %+v", got)
	}
}

// TestWorkspaceStrictArgumentsAndOutputBudget 守住工作区工具的严格校验：
// 多余字段、越界取值（负 limit、逃出根目录的 glob、timeout_seconds=0 等）与
// 缺失必填一律 invalid_arguments；read 超预算文件仍要在结果预算内截断返回。
func TestWorkspaceStrictArgumentsAndOutputBudget(t *testing.T) {
	r, root := workspaceFixture(t)
	for name, args := range map[string]string{"read": `{"path":"x","extra":1}`, "grep": `{"pattern":"x","limit":-1}`, "glob": `{"pattern":"../*"}`, "edit": `{"path":"x","old_string":"","new_string":"x"}`, "write": `{"path":"x"}`, "bash": `{"command":"true","timeout_seconds":0}`} {
		if got := r.Execute(context.Background(), name, args); got.Code != "invalid_arguments" {
			t.Fatalf("%s: %s", name, got.JSON())
		}
	}
	if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("\x01\"", 50000)), 0600); err != nil {
		t.Fatal(err)
	}
	got := toolCall(t, r, "read", map[string]any{"path": "large"})
	if !got.OK || !got.Truncated || len(got.JSON()) > 65536 {
		t.Fatalf("read budget: size=%d %s", len(got.JSON()), got.Code)
	}
}

// TestWorkspaceReadEscapingPaginationAndLongLineProgress 守住分页推进：
// 控制字符转义后 next_offset 与已交付行数严格一致（不跳行不丢行），
// 超长单行被截也必须能推进到下一行而非原地卡死。
func TestWorkspaceReadEscapingPaginationAndLongLineProgress(t *testing.T) {
	r, root := workspaceFixture(t)
	text := strings.Repeat(strings.Repeat("\x01", 500)+"\n", 100)
	if err := os.WriteFile(filepath.Join(root, "escape"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	got := toolCall(t, r, "read", map[string]any{"path": "escape"})
	var envelope struct {
		Value struct {
			Content string `json:"content"`
			Next    int    `json:"next_offset"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(got.JSON()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Value.Next != strings.Count(envelope.Value.Content, "\n")+1 {
		t.Fatalf("pagination skips unseen lines: next=%d complete=%d", envelope.Value.Next, strings.Count(envelope.Value.Content, "\n"))
	}
	if err := os.WriteFile(filepath.Join(root, "long"), []byte(strings.Repeat("x", 100000)+"\nnext\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got = toolCall(t, r, "read", map[string]any{"path": "long"})
	if err := json.Unmarshal([]byte(got.JSON()), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Value.Next != 2 {
		t.Fatalf("long line cannot progress: %s", got.JSON()[:min(200, len(got.JSON()))])
	}
}

// TestWorkspaceConfigurableLimitsAndAbsoluteShell 守住自定义配置的可用性：
// 放宽的各限额与绝对路径 Shell 照常生效，bash 输出仍受 JSON 总预算约束截断，
// 显式 timeout_seconds 可覆盖默认值。
func TestWorkspaceConfigurableLimitsAndAbsoluteShell(t *testing.T) {
	root := t.TempDir()
	options := DefaultWorkspaceOptions()
	options.Root = root
	options.ReadLines = 20000
	options.SearchResults = 20000
	options.MaxFileBytes = 101 << 20
	options.MaxOutputBytes = 128 << 10
	options.Shell = "/bin/bash"
	r, err := NewWorkspace(options)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := toolCall(t, r, "bash", map[string]any{"command": "i=0; while [ $i -lt 10000 ]; do printf '\"&'; i=$((i+1)); done"})
	if !got.OK || len(got.JSON()) > 65536 || !got.Truncated {
		t.Fatalf("JSON budget: %s size=%d", got.Code, len(got.JSON()))
	}
	if got := toolCall(t, r, "bash", map[string]any{"command": "printf ok", "timeout_seconds": 600}); !got.OK {
		t.Fatalf("explicit timeout: %s", got.JSON())
	}
}

// TestWorkspaceNullArgumentsAndCancelledMutation 守住 null 参数与取消副作用：
// 参数显式传 null 与省略不同，一律按 invalid_arguments 拒绝；已取消的上下文
// 让写操作短路返回 cancelled，不留任何文件。
func TestWorkspaceNullArgumentsAndCancelledMutation(t *testing.T) {
	r, root := workspaceFixture(t)
	for name, args := range map[string]string{"read": `{"path":"x","offset":null}`, "grep": `{"pattern":"x","ignore_case":null}`, "glob": `{"pattern":"*","limit":null}`, "edit": `{"path":"x","old_string":"a","new_string":"b","replace_all":null}`, "write": `{"path":"x","content":"a","overwrite":null}`, "bash": `{"command":"true","timeout_seconds":null}`} {
		if got := r.Execute(context.Background(), name, args); got.Code != "invalid_arguments" {
			t.Fatalf("%s null: %s", name, got.JSON())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := r.Execute(ctx, "write", `{"path":"cancelled","content":"no"}`); got.Code != "cancelled" {
		t.Fatal(got.JSON())
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled")); !os.IsNotExist(err) {
		t.Fatalf("cancelled write created file: %v", err)
	}
}

// TestWorkspaceSearchFallbackAndFileLimits 守住引擎回退与文件限额：
// PATH 里没有 rg 时 grep 回退 Go 引擎且结果不缩水，MaxFileBytes 同时约束
// 读与写，NUL 字节按 not_text 拒绝。
func TestWorkspaceSearchFallbackAndFileLimits(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("first\nneedle\nlast\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// 保留可执行 Shell，但 PATH 中不放 rg，让回退在真实环境中运行。
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	shell, err = filepath.Abs(shell)
	if err != nil {
		t.Fatal(err)
	}
	o := DefaultWorkspaceOptions()
	o.Root = root
	o.Shell = shell
	o.MaxFileBytes = 24
	small, err := NewWorkspace(o)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	t.Setenv("PATH", t.TempDir())
	got := toolCall(t, small, "grep", map[string]any{"pattern": "n.e+dle", "context": 1})
	if !got.OK || !strings.Contains(got.JSON(), `"engine":"go"`) || !strings.Contains(got.JSON(), "needle") || !strings.Contains(got.JSON(), "last") {
		t.Fatalf("fallback: %s", got.JSON())
	}
	if got := toolCall(t, small, "write", map[string]any{"path": "large", "content": strings.Repeat("x", 25)}); got.Code != "file_too_large" {
		t.Fatal(got.JSON())
	}
	if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", 25)), 0600); err != nil {
		t.Fatal(err)
	}
	if got := toolCall(t, small, "read", map[string]any{"path": "large"}); got.Code != "file_too_large" {
		t.Fatal(got.JSON())
	}
	if err := os.WriteFile(filepath.Join(root, "nul"), []byte{'x', 0}, 0600); err != nil {
		t.Fatal(err)
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "nul"}); got.Code != "not_text" {
		t.Fatal(got.JSON())
	}
}

// TestWorkspaceFailedWritesLeaveNoTemporaryFiles 守住失败写的零残留：
// 写失败不得留下临时文件或部分产物，越界路径与经符号链接的逃逸
// （含 bash workdir）一律拒绝，工作区外零改动。
func TestWorkspaceFailedWritesLeaveNoTemporaryFiles(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "parent"), []byte("obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	got := toolCall(t, r, "write", map[string]any{"path": "parent/child", "content": "lost"})
	if got.OK {
		t.Fatal("write through non-directory succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "parent" {
		t.Fatalf("failed write leaked: %+v", entries)
	}
	for path := range map[string]bool{"../outside": true, "/tmp/outside": true} {
		if got := toolCall(t, r, "write", map[string]any{"path": path, "content": "lost"}); got.OK {
			t.Fatalf("unsafe write: %s", got.JSON())
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip(err)
	}
	if got := toolCall(t, r, "write", map[string]any{"path": "escape/child", "content": "lost"}); got.OK {
		t.Fatalf("symlink write escaped: %s", got.JSON())
	}
	if got := toolCall(t, r, "bash", map[string]any{"command": "true", "workdir": "escape"}); got.OK {
		t.Fatalf("symlink workdir escaped: %s", got.JSON())
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !os.IsNotExist(err) {
		t.Fatalf("outside mutated: %v", err)
	}
}

// TestWorkspaceResultBudgetIncludesLongPathMetadata 守住预算口径：结果预算
// 按最终 JSON 字节数计算，长路径等元数据同样计入——极小预算下 read/glob/grep
// 都必须截断且不得超标。
func TestWorkspaceResultBudgetIncludesLongPathMetadata(t *testing.T) {
	root := t.TempDir()
	options := DefaultWorkspaceOptions()
	options.Root = root
	options.ResultBytes = 256
	r, err := NewWorkspace(options)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	path := strings.Repeat("dir/", 100) + "file.txt"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte("useful prefix\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read", "glob", "grep"} {
		args := map[string]any{"path": path}
		if name == "glob" {
			args = map[string]any{"pattern": "**"}
		}
		if name == "grep" {
			args["pattern"] = "useful"
		}
		got := toolCall(t, r, name, args)
		if !got.OK || len(got.JSON()) > 256 || !got.Truncated {
			t.Fatalf("%s exceeds result budget: %d %s", name, len(got.JSON()), got.JSON())
		}
	}
}

// TestWorkspaceJSONEscapedLengthMatchesMarshal 守住 jsonEscapedLen 与
// encoding/json 默认 HTML 转义的逐字节等价：全字节域、多字节、无效 UTF-8
// 与确定性随机混合序列的 marshal 字节数必须完全一致，clipText 依赖该等价。
func TestWorkspaceJSONEscapedLengthMatchesMarshal(t *testing.T) {
	corpus := []string{
		"", "plain ascii", `quote"backslash\`, "<html>&</html>",
		"\x00\x01\x02\x1f\x7f", "\n\r\t", "héllo 羽毛 \U0001F426",
		"\ufffd", "a\xc3b", "mixed 引号\"<>&\x01\xffend",
		string([]byte{0x80, 0x81}), string([]byte{0xc3, 0x28}),
		string([]byte{0xe0, 0x80, 0x80}), string([]byte{0xed, 0xa0, 0x80}),
		string([]byte{0xf5, 0x90}), string([]byte{0xc2}),
	}
	for i := range 256 {
		corpus = append(corpus, string([]byte{byte(i)}))
	}
	// 确定性伪随机混合序列：固定种子保证可复现，覆盖随机边界组合。
	state := uint64(0x9E3779B97F4A7C15)
	for range 512 {
		state = state*6364136223846793005 + 1442695040888963407
		raw := make([]byte, int(state%97)+1)
		for j := range raw {
			state = state*6364136223846793005 + 1442695040888963407
			raw[j] = byte(state >> 33)
		}
		corpus = append(corpus, string(raw))
	}
	for _, text := range corpus {
		encoded, err := json.Marshal(text)
		if err != nil {
			t.Fatalf("marshal %q: %v", text, err)
		}
		if got, want := jsonEscapedLen(text), len(encoded); got != want {
			t.Fatalf("jsonEscapedLen(%q) = %d, want marshal bytes %d", text, got, want)
		}
	}
}

// referenceRead 逐行重建候选串并经 clipText 探测预算，逐字复刻增量改造前的
// read 循环；增量实现必须与之输出完全一致，作为等价性基准。
func referenceRead(lines []string, offset, limit, limitBytes int) (content string, next int, truncated, partialLine bool) {
	end := offset - 1 + min(len(lines)-(offset-1), limit)
	var b strings.Builder
	next = offset
	for i := offset - 1; i < end; i++ {
		piece := fmt.Sprintf("%d: %s\n", i+1, lines[i])
		candidate := b.String() + piece
		_, cut := clipText(candidate, limitBytes)
		if cut {
			truncated = true
			if b.Len() == 0 {
				clipped, _ := clipText(piece, limitBytes)
				b.WriteString(clipped)
				next = i + 2
				partialLine = true
			}
			break
		}
		b.WriteString(piece)
		next = i + 2
	}
	truncated = truncated || next <= len(lines)
	return b.String(), next, truncated, partialLine
}

// TestWorkspaceReadMatchesRebuildReference 守住 read 增量预算探测与旧式
// "逐行重建候选串"的等价：content/next_offset/partial_line/truncated 在
// 对抗用例与确定性随机组合下必须逐字段一致。
func TestWorkspaceReadMatchesRebuildReference(t *testing.T) {
	runCase := func(t *testing.T, lines []string, offset, limit, maxOutput int) {
		t.Helper()
		root := t.TempDir()
		options := DefaultWorkspaceOptions()
		options.Root, options.MaxOutputBytes = root, maxOutput
		r, err := NewWorkspace(options)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		// 期望口径以文件实际往返为准：尾部空行经 splitLines 会并入行尾，不单列。
		data := []byte(strings.Join(lines, "\n"))
		fileLines := splitLines(data)
		if offset > len(fileLines)+1 {
			args := fmt.Sprintf(`{"path":"f.txt","offset":%d,"limit":%d}`, offset, limit)
			if result := r.workspace.read(context.Background(), args); result.OK {
				t.Fatalf("offset %d beyond %d lines should fail", offset, len(fileLines))
			}
			return
		}
		if err := os.WriteFile(filepath.Join(root, "f.txt"), data, 0600); err != nil {
			t.Fatal(err)
		}
		args := fmt.Sprintf(`{"path":"f.txt","offset":%d,"limit":%d}`, offset, limit)
		result := r.workspace.read(context.Background(), args)
		if !result.OK {
			t.Fatalf("read failed: %s", result.JSON())
		}
		value, _ := result.Value.(map[string]any)
		if value == nil {
			t.Fatal("read result has no value map")
		}
		wantContent, wantNext, wantTruncated, wantPartial := referenceRead(fileLines, offset, limit, r.workspace.textLimit())
		if got := value["content"]; got != wantContent {
			t.Fatalf("content mismatch:\ngot  %q\nwant %q", got, wantContent)
		}
		if got := value["next_offset"]; got != wantNext {
			t.Fatalf("next_offset = %v, want %v", got, wantNext)
		}
		if got := value["total_lines"]; got != len(fileLines) {
			t.Fatalf("total_lines = %v, want %d", got, len(fileLines))
		}
		if result.Truncated != wantTruncated || value["partial_line"] != wantPartial {
			t.Fatalf("truncated=%v partial_line=%v, want %v/%v", result.Truncated, value["partial_line"], wantTruncated, wantPartial)
		}
	}

	midLines := make([]string, 24)
	for i := range midLines {
		midLines[i] = strings.Repeat("y", 30)
	}
	for _, test := range []struct {
		name                     string
		lines                    []string
		offset, limit, maxOutput int
	}{
		{"plain_fits", []string{"alpha", "beta", "gamma"}, 1, 200, 32768},
		// case: 预算在中途耗尽，break 丢弃当行。
		{"overflow_mid", midLines, 1, 200, 200},
		// case: 转义密集的超长首行触发 partial_line（转义预算先于原始字节耗尽）。
		{"oversize_first_escaped", []string{strings.Repeat(`"`, 500), "tail"}, 1, 200, 200},
		// case: 原始字节先于转义耗尽的超长首行。
		{"oversize_first_raw", []string{strings.Repeat("x", 500), "tail"}, 1, 200, 100},
		{"cjk_emoji_escape", []string{"羽毛🐦引号\"<>&", strings.Repeat("羽", 80), "\x01ctrl", ""}, 1, 200, 150},
		{"empty_file", nil, 1, 200, 32768},
		{"single_line", []string{"only"}, 1, 200, 32768},
		{"middle_offset", midLines, 15, 3, 32768},
		// case: offset 指向尾后空页，next_offset 不前进。
		{"tail_page", midLines, 25, 200, 32768},
		{"limit_one", midLines, 2, 1, 64},
		// case: 极小预算下转义与原始预算同时吃紧。
		{"tiny_budget", []string{`a"b\c<d>e&f`, "second"}, 1, 200, 24},
	} {
		t.Run(test.name, func(t *testing.T) {
			runCase(t, test.lines, test.offset, test.limit, test.maxOutput)
		})
	}

	// 确定性随机组合：固定种子覆盖随机边界（字符池含转义、控制符与多字节）。
	alphabet := []string{"x", `"`, `\`, "<", ">", "&", "\x01", "羽", "🐦", ""}
	state := uint64(0x2545F4914F6CDD1D)
	pick := func(n int) int {
		state = state*6364136223846793005 + 1442695040888963407
		return int(state>>33) % n
	}
	outputs := []int{20, 40, 100, 512, 32768}
	for iteration := range 300 {
		lines := make([]string, pick(25))
		for i := range lines {
			lines[i] = ""
			for range pick(51) {
				lines[i] += alphabet[pick(len(alphabet))]
			}
		}
		offset, limit := 1+pick(len(lines)+1), 1+pick(10)
		name := fmt.Sprintf("random/%d", iteration)
		t.Run(name, func(t *testing.T) {
			runCase(t, lines, offset, limit, outputs[pick(len(outputs))])
		})
	}
}

// TestWorkspaceUnicodeGlobAndSearchFilter 守住 Unicode 路径的匹配：中文目录下
// glob 模式（通配、字符类、** 递归）与 grep 的 glob 过滤都能正确命中。
func TestWorkspaceUnicodeGlobAndSearchFilter(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "文档"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "文档", "a.go"), []byte("needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"文档/*.go", "文档/?.go", "文档/[ab].go", "**/a.go"} {
		t.Run("glob/"+pattern, func(t *testing.T) {
			got := toolCall(t, r, "glob", map[string]any{"pattern": pattern})
			if !got.OK || !strings.Contains(got.JSON(), "文档/a.go") {
				t.Fatalf("Unicode glob: %s", got.JSON())
			}
		})
		t.Run("grep/"+pattern, func(t *testing.T) {
			got := toolCall(t, r, "grep", map[string]any{"pattern": "needle", "glob": pattern})
			if !got.OK || !strings.Contains(got.JSON(), "文档/a.go") {
				t.Fatalf("Unicode grep filter: %s", got.JSON())
			}
		})
	}
}

// TestWorkspaceRegexUsesGoSemanticsWithAndWithoutRG 守住正则语义的权威来源：
// 一律以 Go 正则为准（\b 认 Unicode 字母边界、\d 只认 ASCII 数字），
// rg 与 Go 回退引擎的结果必须一致。
func TestWorkspaceRegexUsesGoSemanticsWithAndWithoutRG(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "unicode.txt"), []byte("文a文\n١\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		pattern string
		count   int
	}{{`\ba\b`, 1}, {`\d`, 0}} {
		for _, fallback := range []bool{false, true} {
			t.Run(test.pattern+"/fallback="+strconv.FormatBool(fallback), func(t *testing.T) {
				if fallback {
					t.Setenv("PATH", t.TempDir())
				}
				got := toolCall(t, r, "grep", map[string]any{"pattern": test.pattern})
				var envelope struct {
					Result struct {
						Count int `json:"count"`
					} `json:"result"`
				}
				if err := json.Unmarshal([]byte(got.JSON()), &envelope); err != nil {
					t.Fatal(err)
				}
				if !got.OK || envelope.Result.Count != test.count {
					t.Fatalf("regex authority: %s expected count=%d", got.JSON(), test.count)
				}
			})
		}
	}
}

// TestToolArgumentsRejectCaseAliases 守住参数键的大小写敏感：首字母大写等
// 别名写法必须当作未知字段拒绝，不得与规范键混淆或静默生效。
func TestToolArgumentsRejectCaseAliases(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]string{
		"read":  `{"path":"absent","Path":"a.txt"}`,
		"grep":  `{"pattern":"absent","Pattern":"needle"}`,
		"glob":  `{"pattern":"absent","Pattern":"*"}`,
		"edit":  `{"path":"a.txt","old_string":"needle","new_string":"x","Replace_All":false}`,
		"write": `{"path":"new.txt","content":"a","Overwrite":false}`,
		"bash":  `{"command":"true","Workdir":"."}`,
	} {
		if got := r.Execute(context.Background(), name, args); got.Code != "invalid_arguments" {
			t.Fatalf("%s accepted alias: %s", name, got.JSON())
		}
	}
	if got := Builtins(time.Now).Execute(context.Background(), "calculate", `{"operation":"add","a":1,"b":2,"Operation":"multiply"}`); got.Code != "invalid_arguments" {
		t.Fatalf("calculate accepted alias: %s", got.JSON())
	}
	if got := Builtins(time.Now).Execute(context.Background(), "current_time", `{"Timezone":"UTC"}`); got.Code != "invalid_arguments" {
		t.Fatalf("clock accepted alias: %s", got.JSON())
	}
}
