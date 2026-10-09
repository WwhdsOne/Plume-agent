package soul

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEnsureCreatesPrivateCompleteTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "soul.md")
	created, err := Ensure(path)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != DefaultTemplate || !strings.Contains(string(data), "Plume") {
		t.Fatalf("template incomplete: err=%v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private mode: info=%v err=%v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: count=%d err=%v", len(entries), err)
	}
}

func TestEnsurePreservesExistingAndLinkedFiles(t *testing.T) {
	for _, content := range []string{"", "custom personality sentinel"} {
		t.Run(map[bool]string{true: "empty", false: "custom"}[content == ""], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "soul.md")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				created, err := Ensure(path)
				if err != nil || created {
					t.Fatalf("existing file changed: created=%v err=%v", created, err)
				}
			}
			data, err := os.ReadFile(path)
			info, statErr := os.Stat(path)
			if err != nil || statErr != nil || string(data) != content || info.Mode().Perm() != 0o644 {
				t.Fatalf("existing content or permissions changed: read=%v stat=%v", err, statErr)
			}
		})
	}
	dir := t.TempDir()
	target, path := filepath.Join(dir, "personal.md"), filepath.Join(dir, "soul.md")
	if err := os.WriteFile(target, []byte("linked soul"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	created, err := Ensure(path)
	if err != nil || created {
		t.Fatalf("link overwritten: created=%v err=%v", created, err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink not preserved: %v", err)
	}
}

func TestEnsureConcurrentPublishDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	var created atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			published, err := Ensure(path)
			if published {
				created.Add(1)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if created.Load() != 1 {
		t.Fatalf("publish count=%d", created.Load())
	}
	data, err := os.ReadFile(path)
	entries, listErr := os.ReadDir(filepath.Dir(path))
	if err != nil || listErr != nil || string(data) != DefaultTemplate || len(entries) != 1 {
		t.Fatalf("atomic publish: read=%v list=%v entries=%d", err, listErr, len(entries))
	}
}

func TestEnsureRejectsInvalidTargetAndCleansFailure(t *testing.T) {
	dir := t.TempDir()
	if created, err := Ensure(dir); err == nil || created {
		t.Fatal("directory accepted as personality file")
	}
	blocker := filepath.Join(dir, "parent")
	if err := os.WriteFile(blocker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err := Ensure(filepath.Join(blocker, "soul.md")); err == nil || created {
		t.Fatal("unwritable destination accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failure left artifacts: entries=%d err=%v", len(entries), err)
	}
	link := filepath.Join(dir, "broken.md")
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if created, err := Ensure(link); err == nil || created {
		t.Fatal("broken symlink accepted or overwritten")
	}
	if created, err := Ensure(""); err == nil || created {
		t.Fatal("empty destination accepted")
	}
}

func TestReadSkipsAbsentEmptyAndDisabled(t *testing.T) {
	for _, content := range []string{"", " \n\t"} {
		path := filepath.Join(t.TempDir(), "soul.md")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := Read(context.Background(), path, 1024); got != "" || err != nil {
			t.Fatalf("empty soul got=%q err=%v", got, err)
		}
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing.md")} {
		if got, err := Read(context.Background(), path, 1024); got != "" || err != nil {
			t.Fatalf("absent soul got=%q err=%v", got, err)
		}
	}
}

func TestReadBoundedUTF8SnapshotAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	for _, tt := range []struct {
		name string
		data []byte
		max  int
		want string
		fail bool
	}{
		{"exact_limit", []byte("你好"), 6, "你好", false},
		{"preserve_format", []byte("# Identity\n\nPlume\n"), 100, "# Identity\n\nPlume\n", false},
		{"too_large", []byte("private-sentinel"), 4, "", true},
		{"invalid_utf8", []byte{0xff, 0xfe}, 100, "", true},
		{"invalid_limit", []byte("Plume"), 0, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Read(context.Background(), path, tt.max)
			if got != tt.want || (err != nil) != tt.fail {
				t.Fatalf("read got=%q err=%v", got, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-sentinel") {
				t.Fatal("personality body leaked in error")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Read(ctx, path, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation err=%v", err)
	}
	if _, err := Read(context.Background(), filepath.Dir(path), 100); err == nil {
		t.Fatal("directory accepted")
	}
}
