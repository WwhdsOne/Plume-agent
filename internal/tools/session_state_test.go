package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestResetSessionStateRequiresFreshRead 守住真实文件守卫：新会话清除旧摘要，
// edit/write 都必须重新 read；许可、根目录和既有文件保持不变。
func TestResetSessionStateRequiresFreshRead(t *testing.T) {
	r, root := workspaceFixture(t)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "a.txt"}); !got.OK {
		t.Fatal(got.JSON())
	}
	before := r.Declarations()
	if err := r.ResetSessionState(); err != nil {
		t.Fatal(err)
	}
	if r.Workspace() != root || !reflect.DeepEqual(before, r.Declarations()) {
		t.Fatal("reset changed workspace or permissions")
	}
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		// case: 覆盖不能沿用旧会话的 read。
		{"write", map[string]any{"path": "a.txt", "content": "new", "overwrite": true}},
		// case: 精确替换也必须重新读取。
		{"edit", map[string]any{"path": "a.txt", "old_string": "old", "new_string": "new"}},
	} {
		if got := toolCall(t, r, test.name, test.args); got.OK || got.Code != "read_required" {
			t.Fatalf("%s guard lost after reset: %s", test.name, got.JSON())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("rejected tools changed file: %q, %v", data, err)
	}
	if got := toolCall(t, r, "read", map[string]any{"path": "a.txt"}); !got.OK {
		t.Fatal(got.JSON())
	}
	if got := toolCall(t, r, "edit", map[string]any{"path": "a.txt", "old_string": "old", "new_string": "new"}); !got.OK {
		t.Fatal(got.JSON())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.ResetSessionState(); err == nil {
		t.Fatal("closed workspace accepted session reset")
	}
}
