---
title: G1b.1 审核记录：OpenAI SDK 请求、非流式解析与可测量模型接口
status: passed
updated: 2026-10-07
summary: G1b.1 交付：模型契约与 openai-chat-completions 适配器、端点安全策略、工厂/probe、模型 trace、12 个离线种子；已通过审核
---

# G1b.1 审核记录：OpenAI SDK 请求、非流式解析与可测量模型接口

- 日期：2026-10-06。
- 依据：[决策 0003](../decisions/0003-model-runtime.md)（2026-10-06 修订为 OpenAI 官方 SDK）与[第一阶段计划](../phase-01-tui-agent.md) §5 G1b.1。
- 状态：**已通过审核**（2026-10-06 用户确认"通过"，随后启动 G1b.2）。

## 1. 本单元做了什么

锁定 `github.com/openai/openai-go` **v1.12.0**（检查时最新 GA），实现非流式模型请求闭环：中立契约 → 协议适配器 → 受控传输 → 响应/错误归一化 → trace。DeepSeek 与自定义兼容服务同走 `openai-chat-completions` 协议族的同一 SDK 适配器（0003 §7 映射表）；未来 Anthropic/Gemini 只需新增适配器实现同一消费接口。

**消费接口**（`internal/model`）：`Client.Generate`（非流式，本单元交付）与 `Client.Stream`（流式，所有实现本单元明确返回 unsupported，G1b.3 启用）。请求/响应为项目自有类型，不暴露 openai-go 类型给调用方。

## 2. 改动清单

| 位置 | 内容 |
| --- | --- |
| `go.mod` / `go.sum` | 引入 `openai-go v1.12.0`（传递依赖 tidwall/gjson 等） |
| `internal/model/types.go` | 消息/请求/响应/usage（可选值）/finish reason/流事件与 EventStream 契约/Client 接口 |
| `internal/model/errors.go` | 11 类统一错误（0003 §6）+ 单行英文格式 + 截断/脱敏工具 + context 分类 |
| `internal/model/capabilities.go` | 能力与三态（supported/unsupported/unverified）快照 |
| `internal/model/fake.go` | 脚本化 fake，与真实适配器同一 Client 接口 |
| `internal/model/endpoint/endpoint.go` | URL 安全校验（HTTPS/回环例外、拒绝 userinfo/fragment、保留路径前缀）、受控 `*http.Transport`、禁重定向、`WithMaxRetries(0)`、8 MiB Content-Length 预检（SDK middleware）、空 key 显式删鉴权头 |
| `internal/model/openai/chat.go` | Chat Completions 适配器：请求编码、单候选校验、usage 存在性、finish reason 归一化、`apierror` 状态映射、供应商请求 ID 提取 |
| `internal/model/deepseek/chat.go` | 组合兼容实现的 DeepSeek 适配器（provider 标识 deepseek） |
| `internal/provider/{factory,probe}.go` | ModelFactory（protocol→适配器映射、凭据解析、未知 ID 明确失败）、显式 Probe；`ModelSpec` 刻意不引用 config（保持 provider 零依赖，config 测试以本包做替身） |
| `internal/telemetry/model.go` | ModelRecorder：model_start + 唯一终态（completed/failed），usage_known 布尔表达缺失，错误分类入 trace |
| `eval/datasets/smoke.v1.jsonl` | 12 个离线工程回归种子（版本化数据集） |
| `internal/eval/smoke_test.go` | 数据集形状校验 + 逐例运行器（httptest 驱动） |
| 测试 | model/errors/fake、endpoint 8 项、openai 14 项、deepseek 2 项、provider 4 项、telemetry 4 项、eval 2 项 |

## 3. 行为证据（可复现命令与实际结果）

### 3.1 离线测试（默认全离线，不产生任何付费调用）

```
go test -timeout 120s ./internal/model/... ./internal/provider/... ./internal/telemetry/... ./internal/eval/...
ok  plume-agent/internal/model          (cached)
ok  plume-agent/internal/model/deepseek 1.049s
ok  plume-agent/internal/model/endpoint 2.508s
ok  plume-agent/internal/model/openai   4.947s
ok  plume-agent/internal/provider       0.625s
ok  plume-agent/internal/telemetry      (cached)
ok  plume-agent/internal/eval           5.630s
```

覆盖要点：URL 路径变体（根地址//v1/尾斜线/代理前缀，不猜补 /v1）；Bearer 鉴权与空 key 不发送任何鉴权头（含环境变量泄漏防护）；**禁自动重试**（500 只发 1 次，SDK 默认重试 2 次）；**禁重定向**（301/302 只发 1 次且不转往他址）；Content-Length 超限拒绝（response_too_large）；usage 缺失记 unknown 不当 0；choices≠1 拒绝；401/403→authentication、429→rate_limited、5xx→upstream、其余 4xx→invalid_response、非 JSON/畸形 JSON→invalid_response、超时→timeout、取消→cancelled。

### 3.2 竞态与静态检查

```
go test -race -timeout 300s ./...    # 全部 11 个包 ok（含既有包回归）
go vet ./...                          # 通过
gofmt -l .                            # 空
```

### 3.3 显式联网 probe（一次真实调用，用户当日提供测试 key）

```
DEEPSEEK_LIVE_KEY=… go test -timeout 60s -v ./internal/provider -run TestLiveProbeDeepseek
probe_live_test.go:41: model=deepseek-chat finish=stop content="pong"
    usage={OK:true PromptTokens:12 CompletionTokens:2 TotalTokens:14} elapsed=6.092s
PASS
```

该测试**只在环境变量提供 key 时执行**，否则跳过；key 不入仓库、不入 trace、不出现在测试输出。这是本单元唯一一次真实付费调用（约 14 token）。

### 3.4 依赖增量（同参数实测）

| 版本 | `-s -w` 二进制大小 |
| --- | --- |
| G1a 基线（stash 后构建） | 6,718,850 B ≈ 6.41 MB |
| 本单元（含 openai-go） | 8,501,730 B ≈ 8.11 MB |
| **增量** | **+1,782,880 B ≈ +1.70 MB** |

## 4. 12 个离线种子结果（绝对值，变化列 N/A）

| case | 类别 | 行为 | 结果 |
| --- | --- | --- | --- |
| smoke-001 | normal | 单轮回答 | PASS |
| smoke-002 | normal | 多轮历史 | PASS |
| smoke-003 | normal | 工具调用历史回填 | PASS |
| smoke-004 | normal | temperature 参数传递 | PASS |
| smoke-005 | invalid_input | 空 messages → invalid_config | PASS |
| smoke-006 | invalid_input | 空 model → invalid_config | PASS |
| smoke-007 | model_error | 401 → authentication | PASS |
| smoke-008 | model_error | 429 → rate_limited | PASS |
| smoke-009 | timeout_cancel | deadline → timeout | PASS |
| smoke-010 | timeout_cancel | 用户取消 → cancelled | PASS |
| smoke-011 | trace | start+completed 唯一终态 | PASS |
| smoke-012 | trace | 失败分类终态且 trace 无 key | PASS |

汇总：12/12 通过，0 失败（基线阶段，无比较对象）。

## 5. 局限（如实记录）

1. **非流式响应体无 Content-Length 时不做逐字节截断**：仅预检声明大小（8 MiB）；无声明头时由 SDK 全量读。流式字节上限（16 MiB）在 G1b.3 经计数 reader 实现。
2. **SDK 中间件层取消不保证立即关闭底层连接**：client 侧 ctx 取消正确返回错误，但服务端可能感知不到断开（连接最终由 idle 超时回收）。测试以 release 通道兜底，不依赖该时序；流式单元的"退出时关闭 body/连接"语义在 G1b.3 重新验证。
3. `Stream` 一律返回 unsupported（G1b.3 启用）；请求携带工具声明返回 unsupported（G3）。
4. usage 缺失判定依赖 SDK 的 JSON 字段存在性（`Valid()`）；异常供应商返回全零 usage 且带字段时会被当 0 计——首批两家供应商未见此行为，留待兼容 fixture 扩充。
5. live probe 耗时 6.092s 是单次样本（含 TLS 握手），**不代表性能基线**。
6. run/message 关联 ID 与父子 span 属 G1b.2 应用层；本单元 trace 只有 model_call_id + step_seq。

## 6. 待用户检查项

1. 0003 §4/§6 的传输策略逐项是否与实现一致（§3.1 覆盖清单）。
2. live probe 证据是否认可为"真实请求闭环"（§3.3）。
3. 依赖增量 +1.70 MB 是否接受（§3.4）。
4. **本次对话中提供的 DeepSeek key 已暴露在会话记录里，验收后请到 DeepSeek 控制台轮换。**
5. 回复"通过 G1b.1"后进入 G1b.2（最小可交互 TUI 与多轮会话）。
