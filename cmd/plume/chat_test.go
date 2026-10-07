package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"plume-agent/internal/config"
)

// TestBuildRuntimeBindsAPIModelNameNotConfigID 守住 G1b.2 踩过的坑：
// agent 必须绑定 API 模型名（Model.Model），而不是配置 ID（Model.ID）。
// 混用会被供应商以 400 invalid_request_error 拒绝。
func TestBuildRuntimeBindsAPIModelNameNotConfigID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		SchemaVersion: config.SchemaVersion,
		DefaultModel:  "deepseek-default", // 配置 ID
		Models: []config.ModelConfig{{
			ID:       "deepseek-default",
			Provider: "deepseek",
			Protocol: "deepseek",
			BaseURL:  srv.URL,          // 回环 httptest，Build 只装配不请求
			Model:    "deepseek-flash", // API 模型名
		}},
	}

	runtime, label, err := buildRuntime(cfg, "")
	if err != nil {
		t.Fatalf("buildRuntime error = %v", err)
	}
	if runtime.ModelID() != "deepseek-flash" {
		t.Errorf("runtime model = %q, want API model name deepseek-flash (not the config id)", runtime.ModelID())
	}
	if label != "deepseek/deepseek-flash" {
		t.Errorf("label = %q, want deepseek/deepseek-flash", label)
	}
}

func TestBuildRuntimeRejectsUnknownModelFlag(t *testing.T) {
	cfg := &config.Config{SchemaVersion: config.SchemaVersion, DefaultModel: "a"}
	if _, _, err := buildRuntime(cfg, "nope"); err == nil {
		t.Fatal("unknown --model must be rejected")
	}
}
