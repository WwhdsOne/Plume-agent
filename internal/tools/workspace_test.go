package tools

import (
	"context"
	"encoding/json"
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
	json.Unmarshal([]byte(got.JSON()), &envelope)
	if envelope.Value.Next != 2 {
		t.Fatalf("long line cannot progress: %s", got.JSON()[:min(200, len(got.JSON()))])
	}
}

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
