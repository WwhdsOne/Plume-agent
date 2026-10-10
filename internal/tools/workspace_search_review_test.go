package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWorkspaceGrepRetainsOversizedContextMatch 守住截断不丢命中：上下文超
// 预算被裁时，命中文件、行号与匹配前缀必须保留，最终 JSON 不超结果预算且仍是
// 合法 UTF-8。
func TestWorkspaceGrepRetainsOversizedContextMatch(t *testing.T) {
	for _, test := range []struct {
		name, text               string
		resultBytes, outputBytes int
	}{
		{"default", strings.Repeat("x", 100000), 65536, 32768},
		{"escaped_utf8", strings.Repeat("\x01\"羽", 20000), 65536, 32768},
		{"minimum_result", strings.Repeat("x", 1000), 256, 32768},
		{"small_output", strings.Repeat("x", 1000), 65536, 256},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			options := DefaultWorkspaceOptions()
			options.Root, options.ResultBytes, options.MaxOutputBytes = root, test.resultBytes, test.outputBytes
			r, err := NewWorkspace(options)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			lines := make([]string, 21)
			for i := range lines {
				lines[i] = test.text
			}
			lines[10] = "needle" + lines[10]
			if err := os.WriteFile(filepath.Join(root, "large-context.txt"), []byte(strings.Join(lines, "\n")), 0600); err != nil {
				t.Fatal(err)
			}
			got := r.Execute(context.Background(), "grep", `{"pattern":"needle","context":10}`)
			var result struct {
				Result struct {
					Count   int           `json:"count"`
					Matches []searchMatch `json:"matches"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(got.JSON()), &result); err != nil {
				t.Fatal(err)
			}
			if !got.OK || !got.Truncated || result.Result.Count != 1 || len(result.Result.Matches) != 1 {
				t.Fatalf("matched file lost: ok=%v count=%d truncated=%v json_bytes=%d", got.OK, result.Result.Count, got.Truncated, len(got.JSON()))
			}
			match := result.Result.Matches[0]
			if match.Path != "large-context.txt" || match.Line != 11 || len(got.JSON()) > test.resultBytes || !utf8.ValidString(got.JSON()) {
				t.Fatalf("invalid bounded match: path=%q line=%d json_bytes=%d", match.Path, match.Line, len(got.JSON()))
			}
			if test.resultBytes > 256 && !strings.HasPrefix(match.Content, "needle") {
				t.Fatalf("match prefix lost: %q", match.Content)
			}
		})
	}
}

// TestWorkspaceGrepNoMatchIsNotTruncated 守住零命中的口径：无匹配是正常结果
// （ok=true、count=0），不得与预算截断混淆而误标 truncated。
func TestWorkspaceGrepNoMatchIsNotTruncated(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "no-match.txt"), []byte(strings.Repeat("x", 100000)), 0600); err != nil {
		t.Fatal(err)
	}
	got := r.Execute(context.Background(), "grep", `{"pattern":"needle","context":10}`)
	if !got.OK || got.Truncated || !strings.Contains(got.JSON(), `"count":0`) {
		t.Fatalf("no-match status: %s", got.JSON())
	}
}

// TestWorkspaceGrepFinalStatisticsPreserveCoreMatch 守住极小预算下的取舍
// 优先级：核心命中（count、路径、行号）必须保住，skipped_files 若保留必须如实，
// 缺失不得解读为 0。
func TestWorkspaceGrepFinalStatisticsPreserveCoreMatch(t *testing.T) {
	for _, pathLength := range []int{129, 130} {
		// case: 129/130 跨越极小结果预算的临界路径长度，两侧核心命中都必须完整保留。
		t.Run("path_length="+strconv.Itoa(pathLength), func(t *testing.T) {
			testWorkspaceGrepFinalStatisticsPreserveCoreMatch(t, pathLength)
		})
	}
}

func testWorkspaceGrepFinalStatisticsPreserveCoreMatch(t *testing.T, pathLength int) {
	t.Helper()
	root := t.TempDir()
	options := DefaultWorkspaceOptions()
	options.Root, options.ResultBytes = root, 256
	r, err := NewWorkspace(options)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	path := strings.Repeat("a", pathLength)
	if err := os.WriteFile(filepath.Join(root, path), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if err := os.WriteFile(filepath.Join(root, "z"+string(rune('0'+i))), []byte{0}, 0600); err != nil {
			t.Fatal(err)
		}
	}
	got := toolCall(t, r, "grep", map[string]any{"pattern": "needle"})
	var result struct {
		Result struct {
			Count   int           `json:"count"`
			Matches []searchMatch `json:"matches"`
			Skipped *int          `json:"skipped_files"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(got.JSON()), &result); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.Truncated || result.Result.Count != 1 || len(result.Result.Matches) != 1 || result.Result.Matches[0].Path != path || result.Result.Matches[0].Line != 1 || len(got.JSON()) > 256 {
		t.Fatalf("final statistics lost core match: %s", got.JSON())
	}
	if result.Result.Skipped != nil && *result.Result.Skipped != 10 {
		t.Fatalf("incorrect skipped count: %s", got.JSON())
	}
}

// TestWorkspaceGlobClassDoesNotCrossSeparator 守住 glob 字符类的分隔符边界：
// 类定义无论否定、范围还是"任意字符"写法，都不得跨路径分隔符 /，也不得误伤
// 同层的合法文件。
func TestWorkspaceGlobClassDoesNotCrossSeparator(t *testing.T) {
	r, root := workspaceFixture(t)
	if err := os.Mkdir(filepath.Join(root, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"a/b", "a_b", "axb", "a.b", "a0b"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		pattern string
		paths   []string
	}{
		{"a[!x]b", []string{"a.b", "a0b", "a_b"}},
		{"a[^x]b", []string{"a.b", "a0b", "a_b"}},
		{"a[.-0]b", []string{"a.b", "a0b"}}, // case: 范围类涵盖 / 也不跨分隔符。
		// case: [\s\S]/[^\n] 这类"任意字符"写法同样不跨分隔符，但不误伤同层文件。
		{`a[\s\S]b`, []string{"a.b", "a0b", "a_b", "axb"}},
		{`a[^\n]b`, []string{"a.b", "a0b", "a_b", "axb"}},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			got := toolCall(t, r, "glob", map[string]any{"pattern": test.pattern})
			var result struct {
				Result struct {
					Paths []string `json:"paths"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(got.JSON()), &result); err != nil {
				t.Fatal(err)
			}
			if !got.OK || strings.Join(result.Result.Paths, ",") != strings.Join(test.paths, ",") {
				t.Fatalf("glob class crossed separator or lost valid file: %s", got.JSON())
			}
		})
	}
}
