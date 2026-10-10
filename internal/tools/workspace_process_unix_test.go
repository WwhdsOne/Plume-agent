//go:build unix

package tools

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWorkspaceBashKillsRealChildOnTimeoutAndAfterExit(t *testing.T) {
	r, _ := workspaceFixture(t)
	for _, command := range []string{"sleep 20 & child=$!; printf '%s\\n' \"$child\"; wait", "sleep 20 >/dev/null 2>&1 & printf '%s\\n' \"$!\""} {
		got := toolCall(t, r, "bash", map[string]any{"command": command, "timeout_seconds": 1})
		var value struct {
			Result struct {
				Output string `json:"output"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(got.JSON()), &value); err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(value.Result.Output))
		if err != nil {
			t.Fatalf("child pid: %s", got.JSON())
		}
		deadline := time.Now().Add(time.Second)
		for {
			err := syscall.Kill(pid, 0)
			if err == syscall.ESRCH {
				break
			}
			status, psErr := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
			if psErr != nil || strings.HasPrefix(strings.TrimSpace(string(status)), "Z") {
				break
			}
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("child %d remained alive: %s", pid, status)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestWorkspaceReadRefusesFIFOWithoutBlocking(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := syscall.Mkfifo(root+"/pipe", 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	got := toolCall(t, r, "read", map[string]any{"path": "pipe"})
	if got.Code != "not_regular_file" || time.Since(started) > time.Second {
		t.Fatalf("FIFO: %s elapsed=%s", got.JSON(), time.Since(started))
	}
}
