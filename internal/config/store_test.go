package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// withTempDir 把 PLUME_HOME 指向一个全新目录，确保没有测试会碰到开发者真实的 ~/.plume。
func withTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PLUME_HOME", dir)
	return dir
}

func TestDirHonorsPlumeHome(t *testing.T) {
	dir := withTempDir(t)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if got != filepath.Clean(dir) {
		t.Fatalf("Dir() = %q, want %q", got, dir)
	}
}

func TestDirExpandsTildeInPlumeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	t.Setenv("PLUME_HOME", "~/plume-test-home")
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if want := filepath.Join(home, "plume-test-home"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDirExpandsEnvVarInPlumeHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLUME_TEST_BASE", dir)
	t.Setenv("PLUME_HOME", "$PLUME_TEST_BASE/sub")
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() error = %v", err)
	}
	if want := filepath.Join(dir, "sub"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempDir(t)
	in := &Config{
		SchemaVersion: SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
			APIKeyRef: "deepseek-default",
		}},
		Channels: []ChannelConfig{{
			ID: "weixin-1", Type: "weixin", Enabled: true, ModelRef: "deepseek-default",
		}},
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	out, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch:\n in = %+v\nout = %+v", in, out)
	}
}

func TestLoadMissingFileReportsNotExist(t *testing.T) {
	withTempDir(t)
	if _, err := Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	dir := withTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded on malformed JSON, want error")
	}
}

func TestSaveWritesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX permission bits on windows")
	}
	withTempDir(t)
	if err := Save(&Config{SchemaVersion: SchemaVersion}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config.json mode = %04o, want 0600", perm)
	}
}

func TestSaveLeavesNoTemporaryFilesBehind(t *testing.T) {
	dir := withTempDir(t)
	if err := Save(&Config{SchemaVersion: SchemaVersion}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestSaveFailureKeepsExistingConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced the same way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	dir := withTempDir(t)
	if err := Save(&Config{SchemaVersion: SchemaVersion, DefaultModel: "keep"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // 让 t.TempDir 能完成清理

	if err := Save(&Config{SchemaVersion: SchemaVersion, DefaultModel: "replaced"}); err == nil {
		t.Fatal("Save() succeeded on a read-only directory, want failure")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("config changed after a failed save:\nbefore = %s\nafter  = %s", before, after)
	}
}

func TestWriteFileAtomicFailureLeavesNoTemporaryFiles(t *testing.T) {
	dir := withTempDir(t)
	target := filepath.Join(dir, "config.json")
	// 目标位置放一个非空目录，让最后的 rename 失败。
	if err := os.MkdirAll(filepath.Join(target, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("x"), 0o600); err == nil {
		t.Fatal("writeFileAtomic() succeeded, want failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}
