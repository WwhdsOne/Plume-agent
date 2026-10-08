package openai

import (
	"encoding/json"
	"fmt"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"
	"io"
	"net/http"
	"strings"
	"testing"
)

// BenchmarkSDKStreamNormalization 测量真实 SDK 分帧、JSON 解码和项目事件
// 归一化；排除网络等待，用固定 256 字节 chunk 提供可重现基线。
func BenchmarkSDKStreamNormalization(b *testing.B) {
	for _, size := range []int{1 << 10, 10 << 10, 100 << 10} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			var fixture strings.Builder
			for left := size; left > 0; {
				n := min(left, 256)
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": strings.Repeat("a", n)}}}})
				fixture.WriteString("data: ")
				fixture.Write(chunk)
				fixture.WriteString("\n\n")
				left -= n
			}
			fixture.WriteString(finishFrame + doneFrame)
			raw := fixture.String()
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp := &http.Response{Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(raw))}
				decoder := &observedDecoder{Decoder: ssestream.NewDecoder(resp)}
				sdk := ssestream.NewStream[openai.ChatCompletionChunk](decoder, nil)
				normalized := &eventStream{adapter: &Adapter{}, tools: map[int64]string{}}
				bytesRead := 0
				for sdk.Next() {
					if err := normalized.normalize(sdk.Current()); err != nil {
						b.Fatal(err)
					}
					for _, event := range normalized.queue {
						bytesRead += len(event.TextDelta)
					}
					normalized.queue = normalized.queue[:0]
				}
				if sdk.Err() != nil || !decoder.done || !normalized.finished || bytesRead != size {
					b.Fatalf("bytes=%d error=%v", bytesRead, sdk.Err())
				}
				_ = sdk.Close()
			}
		})
	}
}
