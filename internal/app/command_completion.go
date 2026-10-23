package app

import (
	"context"
	"strings"
)

// CompletionSource 标注候选来源；未来来源经接口注入，不代表已实现加载能力。
type CompletionSource string

// CompletionSourceBuiltin 表示内置管理指令目录。
const CompletionSourceBuiltin CompletionSource = "builtin"

// CompletionRequest 只携带建议需要的输入和入口能力，不携带会话权限或执行参数。
type CompletionRequest struct {
	Text     string
	Entrance Entrance
}

// CompletionCandidate 的 Replacement 是候选选中后的完整输入，不是执行请求。
type CompletionCandidate struct {
	Replacement string
	Description string
	Source      CompletionSource
}

// CompletionProvider 是候选来源边界；生成建议不执行命令，也不创建模型 run。
type CompletionProvider interface {
	Complete(context.Context, CompletionRequest) ([]CompletionCandidate, error)
}

// BuiltinCompletionProvider 使用与帮助相同的指令目录，不独立维护入口白名单。
type BuiltinCompletionProvider struct{}

// Complete 只补全尚未含参数的指令名，转义正文和多行输入不进入建议。
func (BuiltinCompletionProvider) Complete(ctx context.Context, request CompletionRequest) ([]CompletionCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input := strings.TrimLeft(request.Text, " \t")
	if !strings.HasPrefix(input, "/") || strings.HasPrefix(input, "//") {
		return nil, nil
	}
	prefix := input[1:]
	for _, character := range prefix {
		if character < 'a' || character > 'z' {
			return nil, nil
		}
	}
	leading := request.Text[:len(request.Text)-len(input)]
	var candidates []CompletionCandidate
	for _, definition := range Commands(request.Entrance) {
		spellings := append([]string{string(definition.Name)}, definition.Aliases...)
		for _, spelling := range spellings {
			if strings.HasPrefix(spelling, prefix) {
				candidates = append(candidates, CompletionCandidate{
					Replacement: leading + "/" + spelling,
					Description: definition.Description,
					Source:      CompletionSourceBuiltin,
				})
			}
		}
	}
	return candidates, nil
}
