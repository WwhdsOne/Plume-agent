package app

import (
	"context"
	"testing"
)

// TestBuiltinCommandCompletionUsesCatalog 守住前缀仅产生建议，不改变输入解析与入口支持。
func TestBuiltinCommandCompletionUsesCatalog(t *testing.T) {
	provider := BuiltinCompletionProvider{}
	tests := []struct {
		name, text string
		entrance   Entrance
		want       []string
	}{
		// case: 正式名和别名均按小写前缀建议，保留输入开头空白。
		{"single", "/res", EntranceTUI, []string{"/resume"}},
		{"multiple", "/st", EntranceTUI, []string{"/status", "/stop"}},
		{"alias", "/qu", EntranceTUI, []string{"/quit"}},
		{"leading space", " \t/re", EntranceTUI, []string{" \t/resume", " \t/rename"}},
		// case: gateway 不建议本地 clear/exit/quit。
		{"gateway clear", "/cl", EntranceGateway, nil},
		{"gateway quit", "/qu", EntranceGateway, nil},
		// case: 普通消息、转义、多行、参数及大小写错误不被补全接管。
		{"ordinary", "resume", EntranceTUI, nil},
		{"escaped", "//res", EntranceTUI, nil},
		{"multiline", "/res\n", EntranceTUI, nil},
		{"with argument", "/resume abc", EntranceTUI, nil},
		{"finished name", "/resume ", EntranceTUI, nil},
		{"uppercase", "/RES", EntranceTUI, nil},
		{"unknown entrance", "/re", "unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := provider.Complete(context.Background(), CompletionRequest{Text: tt.text, Entrance: tt.entrance})
			if err != nil || len(got) != len(tt.want) {
				t.Fatalf("Complete() = %#v error=%v, want=%v", got, err, tt.want)
			}
			for i, candidate := range got {
				if candidate.Replacement != tt.want[i] || candidate.Description == "" || candidate.Source != CompletionSourceBuiltin {
					t.Fatalf("candidate[%d] = %#v, want replacement=%q", i, candidate, tt.want[i])
				}
			}
		})
	}
	all, err := provider.Complete(context.Background(), CompletionRequest{Text: "/", Entrance: EntranceTUI})
	if err != nil || len(all) != 13 {
		t.Fatalf("bare slash suggestions = %#v, %v", all, err)
	}
}

// TestBuiltinCompletionHonorsCancellation 守住候选来源共享可取消调用约定，不为未来来源创建加载器。
func TestBuiltinCompletionHonorsCancellation(t *testing.T) {
	var provider CompletionProvider = BuiltinCompletionProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := provider.Complete(ctx, CompletionRequest{Text: "/", Entrance: EntranceTUI}); err != context.Canceled || len(got) != 0 {
		t.Fatalf("cancelled completion = %#v, %v", got, err)
	}
}
