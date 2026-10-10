package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkWorkspaceReadLargeFile 测量 read 全链路（分页拼装、逐行预算
// 探测、boundResult 收尾），代表大文件读取的常态成本。
func BenchmarkWorkspaceReadLargeFile(b *testing.B) {
	root := b.TempDir()
	options := DefaultWorkspaceOptions()
	options.Root = root
	r, err := NewWorkspace(options)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = strings.Repeat("x", 80) + " 引号\"与<>&"
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Join(lines, "\n")), 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := r.Execute(context.Background(), "read", `{"path":"large.txt"}`)
		if !result.OK || result.Truncated {
			b.Fatalf("read failed: %s", result.JSON())
		}
	}
}

// BenchmarkBoundResultDropMatches 测量超预算 grep 结果的丢弃路径：
// matches 数量逼近结果预算时逐步截断的成本。
func BenchmarkBoundResultDropMatches(b *testing.B) {
	w := &workspace{options: DefaultWorkspaceOptions()}
	w.options.ResultBytes = 8192
	matches := make([]searchMatch, 100)
	for i := range matches {
		matches[i] = searchMatch{Path: "dir/file.txt", Line: i + 1, Content: strings.Repeat("x", 400)}
	}
	value := map[string]any{"matches": matches, "count": len(matches), "engine": "go"}
	result := Result{OK: true, Value: value}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// boundResult 原地收缩 value，每次迭代前恢复满量输入。
		value["matches"] = matches
		value["count"] = len(matches)
		result.Truncated = false
		if out := w.boundResult(result); len(out.JSON()) > w.options.ResultBytes {
			b.Fatal("budget exceeded")
		}
	}
}

// BenchmarkClipTextTruncate 测量超限文本的二分裁剪，正文含转义字符与
// 多字节 UTF-8，覆盖原始字节与转义字节双重预算。
func BenchmarkClipTextTruncate(b *testing.B) {
	text := strings.Repeat(`line with "quotes" <html> 羽毛 and \backslash\ `+"\x01\n", 600)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clipped, cut := clipText(text, 16384)
		if !cut || len(clipped) == 0 {
			b.Fatal("clip lost truncation")
		}
	}
}
