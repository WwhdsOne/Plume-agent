package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runForTest 在隔离的 HERALD_HOME 下执行一次 CLI，返回合并后的输出与错误。
func runForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HERALD_HOME", t.TempDir())
	return runWith(t, strings.NewReader(""), args...)
}

func runWith(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetIn(stdin)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String() + stderr.String(), err
}

func TestNoArgsPrintsUsageAndSetupHint(t *testing.T) {
	out, err := runForTest(t)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"Available Commands:", "setup", "config", "version"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage output does not mention %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "No configuration found") {
		t.Errorf("first run did not hint at setup:\n%s", out)
	}
}

func TestVersion(t *testing.T) {
	out, err := runForTest(t, "version")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"herald-agent", "commit", "built", "go", "platform"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output does not contain %q:\n%s", want, out)
		}
	}
}

// TestVersionStringReportsInjectedValues 验证 -ldflags -X 注入的三个变量会被如实打印。
func TestVersionStringReportsInjectedValues(t *testing.T) {
	origVersion, origCommit, origBuild := version, commit, buildTime
	t.Cleanup(func() { version, commit, buildTime = origVersion, origCommit, origBuild })

	version, commit, buildTime = "1.2.3", "abc1234-dirty", "2026-10-06T00:00:00Z"
	out := versionString()
	for _, want := range []string{
		"herald-agent v1.2.3",
		"abc1234-dirty",
		"2026-10-06T00:00:00Z",
		runtime.Version(),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("versionString() does not contain %q:\n%s", want, out)
		}
	}
}

// TestVersionStringIsHonestWhenUnstamped 守住：用裸 `go build` / `go run` 构建时，
// 输出必须诚实地说这是未打戳的构建，而不是伪装成某个版本号。
func TestVersionStringIsHonestWhenUnstamped(t *testing.T) {
	origVersion, origCommit, origBuild := version, commit, buildTime
	t.Cleanup(func() { version, commit, buildTime = origVersion, origCommit, origBuild })

	version, commit, buildTime = "dev", "none", "unknown"
	out := versionString()
	if strings.Contains(out, "vdev") {
		t.Errorf("未打戳时不应拼出 vdev 这种伪版本号:\n%s", out)
	}
	for _, want := range []string{"herald-agent dev", "commit  none", "built   unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("versionString() does not contain %q:\n%s", want, out)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if _, err := runForTest(t, "bogus"); err == nil {
		t.Fatal("run(bogus) = nil, want an error")
	}
}

// TestSetupRefusesNonInteractiveStdin 守住"非交互终端不等待按键输入"这条要求：
// 传入的不是 *os.File（因此不是 TTY）时必须立刻报错退出。
func TestSetupRefusesNonInteractiveStdin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERALD_HOME", dir)

	_, err := runWith(t, strings.NewReader(""), "setup")
	if err == nil {
		t.Fatal("run(setup) with a non-TTY stdin = nil, want an error")
	}
	for _, want := range []string{"interactive terminal", dir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(statErr) {
		t.Error("a refused setup must not create a config")
	}
}

// TestSetupRefusesCharDeviceThatIsNotATerminal 守住一个容易踩的坑：/dev/null 是字符
// 设备但不是终端。用 os.ModeCharDevice 判断会让 `herald setup < /dev/null` 进入 huh
// 并挂住，所以必须用真正的 isatty 判定。
func TestSetupRefusesCharDeviceThatIsNotATerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/null 是 POSIX 设备")
	}
	dir := t.TempDir()
	t.Setenv("HERALD_HOME", dir)

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()

	if _, err := runWith(t, devNull, "setup"); err == nil {
		t.Fatal("run(setup) with /dev/null stdin = nil, want an immediate error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "logs")); !os.IsNotExist(statErr) {
		t.Error("a refused setup must not create a trace file")
	}
}

func TestConfigPathUsesHeraldHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERALD_HOME", dir)

	out, err := runWith(t, strings.NewReader(""), "config", "path")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(out, filepath.Clean(dir)) {
		t.Errorf("config path output does not contain %q:\n%s", dir, out)
	}
}

func TestConfigShowWithoutConfigIsNotAnError(t *testing.T) {
	out, err := runForTest(t, "config", "show")
	if err != nil {
		t.Fatalf("run(config show) error = %v, want nil", err)
	}
	if !strings.Contains(out, "no configuration yet") {
		t.Errorf("output = %q", out)
	}
}

// TestConfigShowRedactsSecretValues 是 G1a 的验收检查：CLI 永不打印凭据值。
func TestConfigShowRedactsSecretValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERALD_HOME", dir)

	const secret = "sk-test-DO-NOT-LEAK"
	// 写一份引用了凭据的配置，以及一个取值可辨认的凭据文件。
	cfg := `{
  "schema_version": 1,
  "default_model": "deepseek-default",
  "models": [{
    "id": "deepseek-default", "provider": "deepseek", "protocol": "deepseek",
    "base_url": "https://api.deepseek.com", "model": "deepseek-flash",
    "api_key_ref": "deepseek-default"
  }]
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials", "deepseek-default"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runWith(t, strings.NewReader(""), "config", "show")
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("config show leaked the credential value:\n%s", out)
	}
	if !strings.Contains(out, "已设置") {
		t.Errorf("config show should report the credential as set:\n%s", out)
	}
}
