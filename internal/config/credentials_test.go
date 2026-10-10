package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCredentialRoundTrip 守住凭据存取契约：Set 后 Get 原样读回，CredentialExists 如实报告存在。
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

// TestCredentialPermissions 守住凭据权限位：credentials 目录 0700、凭据文件 0600。
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

// TestEnsureCredentialsDirWarnsButDoesNotTighten 守住"只警告不代改"：目录权限过宽时
// EnsureCredentialsDir 必须警告，但不得擅自收紧用户设置的权限。
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

// TestCredentialRefRejectsUnsafeNames 守住 ref 名安全边界：路径穿越、子目录、隐藏文件、前导横杠等名称在 Set/Get 两侧一律 ErrInvalidRef。
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

// TestSetCredentialRejectsEmptySecret 守住非空约束：空密钥拒绝写入。
func TestSetCredentialRejectsEmptySecret(t *testing.T) {
	withTempDir(t)
	if err := SetCredential("ref", nil); err == nil {
		t.Fatal("SetCredential(nil) succeeded, want error")
	}
}

// TestSetCredentialReplacesAtomically 守住覆盖语义：同名 ref 再次 Set 后读回新值。
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

// TestDeleteCredentialIsIdempotent 守住删除幂等：删不存在的 ref 不报错，删除后 Exists 如实为 false。
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
