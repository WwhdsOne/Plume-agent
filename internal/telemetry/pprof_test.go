package telemetry

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 空地址表示显式关闭：不监听、不报错，调用方无需分支判断。
func TestPprofDisabledWhenAddressEmpty(t *testing.T) {
	s, err := StartPprof("")
	if s != nil || err != nil {
		t.Fatalf("StartPprof(\"\") = (%v, %v), want (nil, nil)", s, err)
	}
	// 关闭 nil 端点必须安全，否则调用方要写条件判断。
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("nil server Close: %v", err)
	}
}

// profile 会泄漏堆内容与协程栈，非回环地址必须拒绝而不是降级监听。
func TestPprofRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{
		"0.0.0.0:6060", // 所有接口
		":6060",        // 空主机等价于所有接口
		"192.168.7.7:6060",
		"10.0.0.1:6060",
		"example.com:6060",
		"not-an-address",
	} {
		s, err := StartPprof(addr)
		if err == nil {
			_ = s.Close(context.Background())
			t.Fatalf("StartPprof(%q) 应被拒绝，却监听在 %s", addr, s.URL())
		}
		if !strings.Contains(err.Error(), "loopback") && addr != "not-an-address" {
			t.Errorf("StartPprof(%q) 错误信息应说明回环要求，得到 %v", addr, err)
		}
	}
}

// 回环地址可抓取标准 profile；Close 后端口不再可用。
func TestPprofLoopbackServesProfilesAndStops(t *testing.T) {
	s, err := StartPprof("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartPprof: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/heap?debug=1"} {
		resp, err := client.Get(s.URL() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("GET %s 返回空响应体", path)
		}
	}

	index, err := client.Get(s.URL() + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET index: %v", err)
	}
	page, _ := io.ReadAll(index.Body)
	_ = index.Body.Close()
	if !strings.Contains(string(page), "Types of profiles") {
		t.Errorf("index 页不含 profile 列表，得到：%.120s", page)
	}

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if resp, err := client.Get(s.URL() + "/debug/pprof/"); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("Close 后仍可访问 %s（status %d）", s.URL(), resp.StatusCode)
	}
}
