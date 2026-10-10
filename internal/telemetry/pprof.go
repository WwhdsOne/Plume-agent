package telemetry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// PprofServer 是仅本机可达的调试端点。它默认不存在：只有显式请求地址才启动，
// 用于在真实会话中抓取 heap、goroutine、CPU profile 等运行数据。
type PprofServer struct {
	server   *http.Server
	listener net.Listener
	url      string
}

// StartPprof 在回环地址上启动 net/http/pprof 端点。
//
// 安全边界：只接受回环地址（`127.0.0.1`、`::1`、`localhost` 或其他回环 IP）。
// profile 会暴露进程内部状态（堆内容、协程栈、命令行参数），因此非回环地址
// 一律拒绝，不会"尽力而为"地监听 0.0.0.0。addr 为空表示显式关闭，返回 (nil, nil)。
//
// 端点挂在独立 mux 上，不注册到 DefaultServeMux，避免污染全局状态。
func StartPprof(addr string) (*PprofServer, error) {
	if addr == "" {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("pprof address %q: %w", addr, err)
	}
	if !isLoopbackHost(host) {
		return nil, fmt.Errorf("pprof address %q must be loopback (127.0.0.1, ::1 or localhost)", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen %q: %w", addr, err)
	}
	s := &PprofServer{
		server:   &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		listener: listener,
		url:      "http://" + listener.Addr().String(),
	}
	go func() { _ = s.server.Serve(listener) }()
	return s, nil
}

// isLoopbackHost 判断主机部分是否只指向本机；空主机（如 ":6060"）表示所有接口，必须拒绝。
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// URL 返回实际监听地址；端口为 0 时这里是内核分配的端口。
func (s *PprofServer) URL() string { return s.url }

// Close 停止监听。已在进行的 profile 抓取会被中断，这是调试端点的预期行为。
func (s *PprofServer) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}
