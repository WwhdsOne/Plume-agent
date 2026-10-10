//go:build darwin || linux

package soul

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// FIFO 不是常规人格文件：Read/Ensure 必须在不打开它的前提下拒绝，避免无 writer 时永久阻塞。
func TestReadAndEnsureRejectFIFOWithoutOpeningIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soul.md")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), path, 100); err == nil {
		t.Fatal("FIFO accepted as personality source")
	}
	if created, err := Ensure(path); err == nil || created {
		t.Fatal("FIFO accepted or overwritten during initialization")
	}
}

// 路径检查后可能被换成 FIFO。直接测试打开候选文件这一步，确保没有
// writer 时也能返回给 Stat 检查；超时只杀测试子进程，不留下阻塞 goroutine。
func TestOpenSoulFileRejectsReplacedFIFOWithoutBlocking(t *testing.T) {
	const marker = "PLUME_SOUL_FIFO_TEST_PATH"
	if path := os.Getenv(marker); path != "" {
		file, err := openSoulFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Mode().IsRegular() {
			t.Fatalf("replacement must remain distinguishable from regular file: %v", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "replacement.md")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOpenSoulFileRejectsReplacedFIFOWithoutBlocking$")
	cmd.Env = append(os.Environ(), marker+"="+path)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("candidate open blocked on a FIFO without a writer: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("replacement probe failed: %v: %s", err, output)
	}
}
