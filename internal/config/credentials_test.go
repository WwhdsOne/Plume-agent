package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCredentialRoundTrip(t *testing.T) {
	withTempDir(t)
	const secret = "sk-test-value"
	if err := SetCredential("deepseek-default", []byte(secret)); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	got, err := GetCredential("deepseek-default")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if string(got) != secret {
		t.Fatalf("GetCredential() = %q, want %q", got, secret)
	}

	exists, err := CredentialExists("deepseek-default")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("CredentialExists() = false, want true")
	}
}

func TestCredentialPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX permission bits on windows")
	}
	withTempDir(t)
	if err := SetCredential("deepseek-default", []byte("sk-test-value")); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	dir, err := CredentialsDir()
	if err != nil {
		t.Fatal(err)
	}
	dfi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dfi.Mode().Perm(); perm != 0o700 {
		t.Errorf("credentials dir mode = %04o, want 0700", perm)
	}

	fi, err := os.Stat(filepath.Join(dir, "deepseek-default"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("credential file mode = %04o, want 0600", perm)
	}
}

func TestEnsureCredentialsDirWarnsButDoesNotTighten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX permission bits on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	withTempDir(t)
	dir, err := CredentialsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	warnings, err := EnsureCredentialsDir()
	if err != nil {
		t.Fatalf("EnsureCredentialsDir() error = %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("EnsureCredentialsDir() returned no warning for a 0755 directory")
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o755 {
		t.Fatalf("EnsureCredentialsDir() silently changed permissions to %04o, want 0755 unchanged", perm)
	}
}

func TestCredentialRefRejectsUnsafeNames(t *testing.T) {
	withTempDir(t)
	unsafe := []string{"", ".", "..", "../escape", "sub/escape", `sub\escape`, ".hidden", "-leading-dash"}
	for _, ref := range unsafe {
		if err := SetCredential(ref, []byte("x")); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("SetCredential(%q) error = %v, want ErrInvalidRef", ref, err)
		}
		if _, err := GetCredential(ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("GetCredential(%q) error = %v, want ErrInvalidRef", ref, err)
		}
	}
}

func TestSetCredentialRejectsEmptySecret(t *testing.T) {
	withTempDir(t)
	if err := SetCredential("ref", nil); err == nil {
		t.Fatal("SetCredential(nil) succeeded, want error")
	}
}

func TestSetCredentialReplacesAtomically(t *testing.T) {
	withTempDir(t)
	if err := SetCredential("ref", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := SetCredential("ref", []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := GetCredential("ref")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("GetCredential() = %q, want %q", got, "second")
	}
}

func TestDeleteCredentialIsIdempotent(t *testing.T) {
	withTempDir(t)
	if err := SetCredential("ref", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCredential("ref"); err != nil {
		t.Fatalf("DeleteCredential() error = %v", err)
	}
	if err := DeleteCredential("ref"); err != nil {
		t.Fatalf("DeleteCredential() on a missing ref error = %v, want nil", err)
	}
	exists, err := CredentialExists("ref")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("CredentialExists() = true after delete")
	}
}

// TestConfigFileNeverContainsSecretValues 是 G1a 的验收检查：任何测试密钥值都不得
// 泄漏到持久化的非敏感配置里。
func TestConfigFileNeverContainsSecretValues(t *testing.T) {
	dir := withTempDir(t)
	const secret = "sk-test-DO-NOT-LEAK"
	if err := SetCredential("deepseek-default", []byte(secret)); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		SchemaVersion: SchemaVersion,
		DefaultModel:  "deepseek-default",
		Models: []ModelConfig{{
			ID: "deepseek-default", Provider: "deepseek", Protocol: "deepseek",
			BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
			APIKeyRef: "deepseek-default",
		}},
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("config.json leaked the credential value:\n%s", raw)
	}
}
