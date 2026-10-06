package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runForTest(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("HERALD_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	err := run(args, &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

func TestNoArgsPrintsUsageAndSetupHint(t *testing.T) {
	out, err := runForTest(t)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"herald setup", "herald config path"} {
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
	if !strings.Contains(out, "herald-agent") {
		t.Errorf("version output = %q", out)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if _, err := runForTest(t, "bogus"); err == nil {
		t.Fatal("run(bogus) = nil, want an error")
	}
}

func TestSetupIsNotImplementedYet(t *testing.T) {
	_, err := runForTest(t, "setup")
	if err == nil {
		t.Fatal("run(setup) = nil, want an error until G1a-2 lands")
	}
	if !strings.Contains(err.Error(), "G1a-2") {
		t.Errorf("setup error should say when it arrives; got: %v", err)
	}
}

func TestConfigPathUsesHeraldHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERALD_HOME", dir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{"config", "path"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	out := stdout.String()
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

	var stdout, stderr bytes.Buffer
	if err := run([]string{"config", "show"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	out := stdout.String() + stderr.String()
	if strings.Contains(out, secret) {
		t.Fatalf("config show leaked the credential value:\n%s", out)
	}
	if !strings.Contains(out, "已设置") {
		t.Errorf("config show should report the credential as set:\n%s", out)
	}
}
