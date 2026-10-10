package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var errStopSearch = errors.New("search limit reached")

func ignoredSearchPath(path string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		switch part {
		case ".git", ".plume", "node_modules", ".venv", "venv", "__pycache__", ".codegraph":
			return true
		}
		if strings.HasPrefix(part, ".plume-write-") {
			return true
		}
	}
	return false
}

// globPattern 将常见 glob 编成正则；**/ 同时支持零层与多层目录。
func globPattern(pattern string) (*regexp.Regexp, error) {
	if _, err := workspacePath(pattern); err != nil {
		return nil, err
	}
	pattern = filepath.ToSlash(pattern)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				return nil, errors.New("invalid glob")
			}
			part := pattern[i+1 : i+1+end]
			if part == "" || strings.Contains(part, "/") {
				return nil, errors.New("invalid glob")
			}
			if strings.HasPrefix(part, "!") {
				part = "^" + part[1:]
			}
			class, err := syntax.Parse("["+part+"]", syntax.Perl)
			if err != nil {
				return nil, errors.New("invalid glob")
			}
			// syntax 会将单字符类和全范围类简化；先恢复为区间，保留既有匹配语义。
			switch class.Op {
			case syntax.OpLiteral:
				class.Rune = []rune{class.Rune[0], class.Rune[0]}
			case syntax.OpAnyChar:
				class.Rune = []rune{0, utf8.MaxRune}
			case syntax.OpAnyCharNotNL:
				class.Rune = []rune{0, '\n' - 1, '\n' + 1, utf8.MaxRune}
			}
			// 否定类和 [.-0] 之类的范围都可能隐含 /；统一从字符区间移除它。
			// 字符类与 *、? 一样只匹配一个路径段，不扩展现有 glob 语法。
			var runes []rune
			for j := 0; j < len(class.Rune); j += 2 {
				lo, hi := class.Rune[j], class.Rune[j+1]
				if hi < '/' || lo > '/' {
					runes = append(runes, lo, hi)
					continue
				}
				if lo < '/' {
					runes = append(runes, lo, '/'-1)
				}
				if hi > '/' {
					runes = append(runes, '/'+1, hi)
				}
			}
			class.Op, class.Rune = syntax.OpCharClass, runes
			b.WriteString(class.String())
			i += end + 1
		default:
			_, size := utf8.DecodeRuneInString(pattern[i:])
			b.WriteString(regexp.QuoteMeta(pattern[i : i+size]))
			i += size - 1
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func (w *workspace) walkFiles(ctx context.Context, path string, visit func(string) error) error {
	return fs.WalkDir(w.root.FS(), filepath.ToSlash(path), func(name string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if ignoredSearchPath(name) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// 不跟随链接，防止重复搜索及链接到工作区以外。
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return visit(filepath.FromSlash(name))
	})
}

func (w *workspace) glob(ctx context.Context, raw string) Result {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   *int   `json:"limit"`
	}
	if decodeArguments(raw, &args) != nil {
		return Result{Code: "invalid_arguments"}
	}
	pattern, err := globPattern(args.Pattern)
	if err != nil {
		return Result{Code: "invalid_arguments"}
	}
	if args.Path == "" {
		args.Path = "."
	}
	path, err := workspacePath(args.Path)
	if err != nil {
		return Result{Code: "invalid_arguments"}
	}
	limit := w.options.SearchResults
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit < 1 || limit > w.options.SearchResults {
		return Result{Code: "invalid_arguments"}
	}
	paths := []string{}
	truncated := false
	err = w.walkFiles(ctx, path, func(file string) error {
		rel, err := filepath.Rel(path, file)
		if err != nil {
			return err
		}
		if !pattern.MatchString(filepath.ToSlash(rel)) {
			return nil
		}
		if len(paths) >= limit {
			truncated = true
			return errStopSearch
		}
		candidate := append(paths, filepath.ToSlash(file))
		b, _ := json.Marshal(candidate)
		if len(b) > w.textLimit() {
			truncated = true
			return errStopSearch
		}
		paths = candidate
		return nil
	})
	if err != nil && !errors.Is(err, errStopSearch) {
		return fileFailure(err)
	}
	sort.Strings(paths)
	return Result{OK: true, Value: map[string]any{"paths": paths, "count": len(paths)}, Truncated: truncated, Summary: fmt.Sprintf("found %d files", len(paths))}
}

type searchLine struct {
	Line    int    `json:"line"`
	Content string `json:"content"`
}
type searchMatch struct {
	Path    string       `json:"path"`
	Line    int          `json:"line"`
	Content string       `json:"content"`
	Before  []searchLine `json:"before,omitempty"`
	After   []searchLine `json:"after,omitempty"`
}

// fitSearchMatch 优先保留匹配路径、行号和正文，再移除最远上下文、剪裁正文。
// 完整结果含 JSON 转义和固定元数据；极小文本限额仍允许结果预算内的纯定位信息。
func (w *workspace) fitSearchMatch(matches []searchMatch, match searchMatch, skipped *int, engine string) (searchMatch, bool, bool) {
	fits := func(candidate searchMatch) bool {
		all := append(matches, candidate)
		encoded, _ := json.Marshal(all)
		if len(encoded) > w.textLimit() && (candidate.Content != "" || len(candidate.Before) > 0 || len(candidate.After) > 0) {
			return false
		}
		result := grepResult(all, skipped, engine, true)
		return len(result.JSON()) <= w.options.ResultBytes
	}
	if fits(match) {
		return match, false, true
	}
	for len(match.Before)+len(match.After) > 0 {
		if len(match.Before) >= len(match.After) {
			match.Before = match.Before[1:]
		} else {
			match.After = match.After[:len(match.After)-1]
		}
		if fits(match) {
			return match, true, true
		}
	}
	text := match.Content
	match.Content = ""
	if !fits(match) {
		return match, true, false
	}
	encoded, _ := json.Marshal(text)
	lo, hi := 0, len(encoded)
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		match.Content, _ = clipText(text, mid)
		if fits(match) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	match.Content, _ = clipText(text, lo)
	return match, true, true
}

func grepResult(matches []searchMatch, skipped *int, engine string, truncated bool) Result {
	value := map[string]any{"matches": matches, "count": len(matches), "engine": engine}
	if skipped != nil {
		value["skipped_files"] = *skipped
	}
	return Result{OK: true, Value: value, Truncated: truncated, Summary: fmt.Sprintf("matched %d lines", len(matches))}
}

func (w *workspace) grep(ctx context.Context, raw string) Result {
	var args struct {
		Pattern *string `json:"pattern"`
		Path    string  `json:"path"`
		Glob    string  `json:"glob"`
		Fixed   bool    `json:"fixed_strings"`
		Ignore  bool    `json:"ignore_case"`
		Context int     `json:"context"`
		Limit   *int    `json:"limit"`
	}
	if decodeArguments(raw, &args) != nil || args.Pattern == nil || args.Context < 0 || args.Context > 10 {
		return Result{Code: "invalid_arguments"}
	}
	if args.Path == "" {
		args.Path = "."
	}
	path, err := workspacePath(args.Path)
	if err != nil {
		return Result{Code: "invalid_arguments"}
	}
	limit := w.options.SearchResults
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit < 1 || limit > w.options.SearchResults {
		return Result{Code: "invalid_arguments"}
	}
	pattern := *args.Pattern
	if args.Fixed {
		pattern = regexp.QuoteMeta(pattern)
	}
	if args.Ignore {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{Code: "invalid_pattern"}
	}
	var filter *regexp.Regexp
	if args.Glob != "" {
		filter, err = globPattern(args.Glob)
		if err != nil {
			return Result{Code: "invalid_arguments"}
		}
	}
	matches := []searchMatch{}
	truncated := false
	skipped := 0
	statistics := &skipped
	engine := "go"
	rg, _ := exec.LookPath("rg")
	err = w.walkFiles(ctx, path, func(file string) error {
		if filter != nil {
			rel, _ := filepath.Rel(path, file)
			candidate := filepath.ToSlash(rel)
			if !strings.Contains(args.Glob, "/") {
				candidate = filepath.Base(file)
			}
			if !filter.MatchString(candidate) {
				return nil
			}
		}
		data, _, status := w.loadText(ctx, file)
		if !status.OK {
			if status.Code == "cancelled" {
				return ctx.Err()
			}
			skipped++
			return nil
		}
		lines := splitLines(data)
		var numbers []int
		// Go 正则是声明的语义来源；rg 的 Unicode 类与词边界可能不同。
		for i, line := range lines {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if re.MatchString(line) {
				numbers = append(numbers, i)
				if len(numbers) > limit {
					break
				}
			}
		}
		if rg != "" && len(numbers) > 0 {
			rgLimit := limit
			if rgLimit < int(^uint(0)>>1) {
				rgLimit++
			}
			if found, ok := rgMatchingLines(ctx, rg, data, *args.Pattern, args.Fixed, args.Ignore, rgLimit); ok && slices.Equal(found, numbers) {
				engine = "rg"
			} else {
				engine = "go"
			}
		}
		for _, i := range numbers {
			if i < 0 || i >= len(lines) {
				continue
			}
			if len(matches) >= limit {
				truncated = true
				return errStopSearch
			}
			text, cut := clipText(lines[i], w.textLimit()/2)
			match := searchMatch{Path: filepath.ToSlash(file), Line: i + 1, Content: text}
			truncated = truncated || cut
			for j := max(0, i-args.Context); j < i; j++ {
				text, cut := clipText(lines[j], w.textLimit()/max(4, 2*args.Context+2))
				truncated = truncated || cut
				match.Before = append(match.Before, searchLine{Line: j + 1, Content: text})
			}
			for j := i + 1; j < min(len(lines), i+args.Context+1); j++ {
				text, cut := clipText(lines[j], w.textLimit()/max(4, 2*args.Context+2))
				truncated = truncated || cut
				match.After = append(match.After, searchLine{Line: j + 1, Content: text})
			}
			match, cut, fits := w.fitSearchMatch(matches, match, statistics, engine)
			truncated = truncated || cut
			if !fits && statistics != nil {
				// 纯定位信息能容纳但统计字段挤占预算时，省略统计而不谎报零匹配。
				statistics = nil
				match, _, fits = w.fitSearchMatch(matches, match, statistics, engine)
			}
			if !fits {
				truncated = true
				return errStopSearch
			}
			matches = append(matches, match)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStopSearch) {
		return fileFailure(err)
	}
	result := grepResult(matches, statistics, engine, truncated)
	if len(result.JSON()) <= w.options.ResultBytes {
		return result
	}
	// 遍历后 skipped_files 的位数可能增加，必须用最终统计再核对完整 JSON。
	// 先裁上下文和正文；极小预算允许省略非核心跳过统计，缺失不能解读为 0。
	kept := []searchMatch{}
	for _, match := range matches {
		match, _, fits := w.fitSearchMatch(kept, match, statistics, engine)
		if !fits && statistics != nil {
			statistics = nil
			match, _, fits = w.fitSearchMatch(kept, match, statistics, engine)
		}
		if !fits {
			break // 纯定位信息本身超限时，也不能突破结果预算。
		}
		kept = append(kept, match)
	}
	return grepResult(kept, statistics, engine, true)
}

// rg 只接收经 os.Root 安全读取的 stdin，不接收文件路径；不可用或输出过大时回退 Go。
func rgMatchingLines(ctx context.Context, rg string, data []byte, pattern string, fixed, ignore bool, limit int) ([]int, bool) {
	args := []string{"--no-config", "--line-number", "--only-matching", "--replace", "", "--color", "never", "--max-count", strconv.Itoa(limit)}
	if fixed {
		args = append(args, "--fixed-strings")
	}
	if ignore {
		args = append(args, "--ignore-case")
	}
	args = append(args, "-e", pattern)
	cmd := exec.CommandContext(ctx, rg, args...)
	configureProcessGroup(cmd)
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Stdin = strings.NewReader(string(data))
	cmd.Env = filteredEnvironment()
	output := &boundedOutput{limit: 32 << 10}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	_ = killProcessGroup(cmd)
	if ctx.Err() != nil || output.truncated {
		return nil, false
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return []int{}, true
		}
		return nil, false
	}
	numbers := []int{}
	previous := -1
	for line := range strings.SplitSeq(output.String(), "\n") {
		prefix, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < 1 {
			return nil, false
		}
		number--
		if number != previous {
			numbers = append(numbers, number)
			previous = number
		}
	}
	return numbers, true
}
