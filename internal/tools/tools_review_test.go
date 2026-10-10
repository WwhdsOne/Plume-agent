package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceDoesNotRequireDisabledShell(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled []string
		wantErr bool
	}{
		{name: "disabled", enabled: []string{}},
		{name: "read_only", enabled: []string{"read"}},
		{name: "builtins_only", enabled: []string{"calculate"}},
		{name: "shell_enabled", enabled: []string{"bash"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "sample.txt"), []byte("available without shell\n"), 0600); err != nil {
				t.Fatal(err)
			}
			options := DefaultWorkspaceOptions()
			options.Root = root
			options.Enabled = tc.enabled
			// 使用确实不存在的绝对路径，避免依赖运行测试的机器是否安装 bash。
			options.Shell = filepath.Join(root, "unavailable-shell")
			r, err := NewWorkspace(options)
			if tc.wantErr {
				if err == nil {
					r.Close()
					t.Fatal("enabled shell accepted unavailable executable")
				}
				return
			}
			if err != nil {
				t.Fatalf("disabled shell prevented selected tools: %v", err)
			}
			defer r.Close()
			if len(r.Declarations()) != len(tc.enabled) || r.Known("bash") {
				t.Fatalf("unexpected declarations: %+v", r.Declarations())
			}
			if r.Known("read") {
				result := r.Execute(context.Background(), "read", `{"path":"sample.txt"}`)
				if !result.OK || !strings.Contains(result.JSON(), "available without shell") {
					t.Fatalf("read without shell: %s", result.JSON())
				}
			}
		})
	}
}
