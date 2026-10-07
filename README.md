# plume-agent

用 Go 构建的个人 Agent：终端输入 `plume` 直接进入聊天 TUI（模仿 [Hermes](https://github.com/NousResearch/hermes-agent) 的零参数体验）。当前实现 TUI 多轮聊天与受控模型调用；微信 iLink Bot、飞书、QQ 属于后续渠道扩展（第二阶段，另行授权）。

项目按审核单元推进（G0 → G1a → G1b.1 → G1b.2 → …），单元边界、审核记录与指标协议见 [`docs/`](docs/)。

## 快速开始

```bash
go build ./...                     # Go 1.27.1
./scripts/build.sh                 # 构建带版本信息的 ./plume

plume setup                       # 首次设置：供应商/模型/Key（渠道可选"暂不接入"）
plume                             # 配置就绪 + TTY：直接进入聊天 TUI
plume chat --offline              # 脚本化 fake 演示：无配置/无 Key/不联网
plume chat --model <配置ID>        # 显式指定 config.json 里的模型配置
plume config show                 # 脱敏查看配置
```

聊天键位：`Enter` 发送；`Ctrl+J` 换行；`PgUp/PgDn` 滚动；`Esc` 取消当前 run；`Ctrl+C` 运行中取消、空闲时退出；`Ctrl+N` 新会话（清空上下文）。

模型传输用 [OpenAI 官方 Go SDK](https://github.com/openai/openai-go)（自动重试/重定向已显式禁用），DeepSeek 与自定义 OpenAI 兼容服务同走 Chat Completions 适配器；Anthropic/Gemini 等协议未来以新增适配器接入同一接口。配置里永不出现密钥值——`config.json` 只存引用，密钥在 `~/.plume/credentials/`（0700/0600）。

## 架构

```text
模型配置 → 注册工厂 → 协议适配器 → OpenAI SDK HTTP（端点校验）
TUI → app（会话/run）→ agent（循环/预算）→ 模型接口 / 受控工具
 └──── 结构化运行事件 ──────→ telemetry → trace / eval
```

| 目录 | 职责 |
| --- | --- |
| `cmd/plume/` | CLI 入口与命令分发 |
| `internal/config/` | 配置 schema、校验、原子持久化、凭据 |
| `internal/setup/` | 首次设置向导（只依赖 Prompter 接口） |
| `internal/provider/` | 供应商预设、模型工厂、显式 probe |
| `internal/model/` | 自有模型契约、endpoint 安全、协议适配器（openai/deepseek/fake） |
| `internal/tui/` | 聊天界面（Bubble Tea；不发送 HTTP、不执行工具） |
| `internal/app/` | 会话与 run 生命周期（串行、取消、事件） |
| `internal/agent/` | 模型—工具循环（工具在 G3） |
| `internal/telemetry/` | JSON Lines trace（setup/模型） |
| `internal/eval/` | 离线种子数据集与运行器 |

文档：[第一阶段计划](docs/phase-01-tui-agent.md) · [决策 0003 模型运行时](docs/decisions/0003-model-runtime.md) · [审核记录](docs/reviews/) · [路线图](docs/roadmap.html) · [TUI runbook](docs/runbooks/tui.md)

## 开发

```bash
go test ./...          # 全部测试（默认离线，不产生付费调用）
go test -race ./...    # 竞态检测；提交前应通过
go vet ./... && gofmt -l .
```

显式联网验证（需要自备测试 Key，只发一次最小请求）：

```bash
DEEPSEEK_LIVE_KEY=sk-… go test ./internal/provider -run TestLiveProbeDeepseek -v
```
