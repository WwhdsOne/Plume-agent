// Package endpoint 负责模型端点的安全校验与受控传输装配（0003 §4）：
// HTTPS 与回环 HTTP 例外、拒绝 userinfo/fragment、保留用户配置的路径前缀、
// 禁止自动重定向与自动重试、响应体 Content-Length 预检。
// 它只装配 HTTP，不解释模型语义。
package endpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/openai/openai-go/option"

	"plume-agent/internal/model"
)

// MaxResponseBytes 是非流式响应体声明的上限（0003 §4：8 MiB）。
const MaxResponseBytes = 8 << 20

// MaxStreamBytes 是一次 SSE 读取总上限，包含思考、答案及协议开销。
const MaxStreamBytes = 16 << 20

// Validate 校验 Base URL 并返回清理后的字符串。规则：必须 https
// （仅回环主机允许 http）、拒绝 userinfo 与 fragment、必须包含主机。
// 与 internal/config 的结构校验保持同一口径；本包负责运行时装配前的
// 最终防线，两者各自测试覆盖。
func Validate(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("not a valid URL: %w", err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("http is only allowed for loopback hosts, use https for %q", u.Hostname())
		}
	default:
		return "", fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("missing host")
	}
	if u.User != nil {
		return "", errors.New("URL userinfo is not allowed")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return "", errors.New("URL fragment is not allowed")
	}
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Options 返回 SDK 请求选项：校验后的 Base URL、受控 HTTP client
// （禁止自动重定向、Content-Length 预检）、显式禁用自动重试
// （SDK 默认重试 2 次，0003 §6 要求首版不自动重试）。
// apiKey 允许为空（本机无鉴权服务）；为空时显式删除鉴权头，
// 防止环境变量（OPENAI_API_KEY 等）带来的凭据泄漏。
func Options(baseURL, apiKey string) ([]option.RequestOption, error) {
	cleaned, err := Validate(baseURL)
	if err != nil {
		return nil, err
	}
	opts := []option.RequestOption{
		option.WithBaseURL(cleaned),
		option.WithMaxRetries(0),
		option.WithHTTPClient(&http.Client{
			// Transport 必须是 *http.Transport：net/http 的取消机制
			// 只对它生效，包装过的 RoundTripper 会让 ctx 取消/超时
			// 静默失效（请求会继续到自然结束）。
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// 不跟随重定向：把 3xx 当作最终响应，避免携带凭据转往其他地址。
				return http.ErrUseLastResponse
			},
		}),
		option.WithAPIKey(apiKey),
		option.WithMiddleware(limitResponseSize),
	}
	if apiKey == "" {
		opts = append(opts,
			option.WithHeaderDel("Authorization"),
			option.WithHeaderDel("X-Api-Key"),
		)
	}
	return opts, nil
}

// limitResponseSize 在响应进入 SDK 解析之前做 Content-Length 预检
// （0003 §4：非流式声明上限 8 MiB）。超限时关闭响应体并返回
// *model.Error，调用方 errors.As 可直接拿到分类。
func limitResponseSize(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil {
		return nil, err
	}
	limit := int64(MaxResponseBytes)
	if req.Body != nil && req.GetBody != nil {
		body, bodyErr := req.GetBody()
		if bodyErr == nil {
			var flags struct {
				Stream bool `json:"stream"`
			}
			_ = json.NewDecoder(body).Decode(&flags)
			_ = body.Close()
			if flags.Stream {
				limit = MaxStreamBytes
			}
		}
	}
	if resp.ContentLength > limit {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, &model.Error{
			Code:       model.ErrResponseTooLarge,
			StatusCode: resp.StatusCode,
			Summary:    fmt.Sprintf("declared response size %d exceeds limit %d", resp.ContentLength, limit),
		}
	}
	return resp, nil
}
