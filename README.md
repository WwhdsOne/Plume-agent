# plume-agent

用 Go 构建的个人 Agent：终端输入 `plume`，首次没有配置时自动进入 setup，配置就绪后直接进入聊天 TUI（模仿 [Hermes](https://github.com/NousResearch/hermes-agent) 的零参数体验）。当前实现多轮流式聊天、Markdown、独立思考、可配置状态栏与 read/grep/glob/edit/write/bash 工具循环（G3/G3.1 于 2026-10-09 审核通过）。微信 iLink Bot、飞书、QQ 属于后续渠道扩展。

项目按审核单元推进（G0 → G1a → G1b.1 → G1b.2 → …），单元边界、审核记录与指标协议见 [`docs/`](docs/)。

## 快速开始

```bash
go build ./...                     # Go 1.27.1
./scripts/build.sh                 # 构建带版本信息的 ./plume

plume                             # 交互终端首次自动 setup；配置就绪后进入聊天 TUI
plume setup                       # 再次设置：供应商/模型/Key（渠道可选"暂不接入"）
plume chat --offline              # 脚本化 fake 演示：无配置/无 Key/不联网
plume chat --model <配置ID>        # 显式指定 config.json 里的模型配置
plume config show                 # 脱敏查看配置
```

setup 正常结束后返回终端，并打印开始聊天、再次设置和查看配置的命令提示；输入 `plume setup` 可随时重新配置。缺配置的非交互调用只显示帮助与提示，不启动向导。

setup 默认在配置目录生成 `soul.md`，不增加问题、不会覆盖已有内容。编辑它可调整 Plume 的身份、语气和风格；在线每条输入准备时读取一次，工具循环内保持快照，下一条输入采用新内容。`agent.soul` 的 enabled/path/max_bytes 默认 true/"soul.md"/65536，完整写入 config.json；设 enabled:false 可关闭。旧配置仅补字段，运行 `plume setup` 才生成模板。人格不改变工具许可，详见 [人格契约](docs/specs/agent/soul.md) 与 [G3.2 审核](docs/reviews/G3.2.md)。

聊天键位：`Enter` 发送；`Shift+Enter`、`Alt/Option+Enter` 或行尾 `\`+`Enter` 换行；`PgUp/PgDn` 滚动；`Esc` 取消；`Ctrl+C` 运行中取消、空闲有草稿时清空、空草稿时两次退出；`Ctrl+D` 空输入退出；`Ctrl+L` 清屏；`Ctrl+N` 新会话；`Ctrl+O` 展开/收起思考。

模型级 `reasoning_effort` 省略时默认偏好 `high`，`none` 关闭思考，setup 不增加问题。支持范围由端点和模型能力共同验证，未验证服务不会自动发送 high 参数；`plume config show` 显示来源与实际生效状态。四阶段文案可在 `tui.status_messages` 中自定义，详见 [流式契约](docs/specs/tui/streaming.md)。

底部状态栏支持配置字段顺序、显隐、两行布局与窄屏优先级，默认项完整写入 `tui.status_line`，详见 [状态栏契约](docs/specs/tui/statusline.md)（G1b.4 已通过）。优先单行，完整字段放不下才分两行。默认显示 `ctx: [██░░░░░░░░] 200k/1M` 与 `cache: 25%`；上下文为进度条及实际输入/容量，缓存只显示命中率，仍默认 CC 会话累计、可选 Pi 最近调用。两者格式、显隐、条宽及主题配色均可配置。

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
| `internal/app/` | 会话与 run 生命周期、用量快照去重累计、Git/uv 异步环境采集 |
| `internal/agent/` | 统一请求拼装、模型—工具循环与预算 |
| `internal/tools/` | 工作区文件读写/搜索、本地命令、参数校验与许可注册表 |
| `internal/soul/` | 默认人格模板、无覆盖初始化与有界 UTF-8 读取 |
| `internal/telemetry/` | 脱敏 JSON Lines trace（setup/模型/run/UI 首答案） |
| `internal/eval/` | 离线种子数据集与运行器 |

文档：[第一阶段计划](docs/plans/phase-01-tui.md) · [决策 0003 模型运行时](docs/decisions/0003-model-runtime.md) · [审核记录](docs/reviews/) · [路线图](docs/roadmap.html) · [TUI runbook](docs/runbooks/tui.md)

默认工具为 `read`、`grep`、`glob`、`edit`、`write`、`bash`，模型按结构化字段调用，TUI 用 `◇` 显示简短状态。`tools` 与 `agent.budget` 完整默认值写入 config；默认不限制总模型/工具次数与整轮时长，单模型 1800 秒、单工具 180 秒，可自行修改。bash 是宿主命令执行能力。

在临时工作目录运行 `plume chat --offline` 并输入 `/demo workspace`，会真实新建 plume-demo.txt、查找/搜索/读取/修改并执行命令校验；文件已存在则拒绝覆盖。旧计算/时间/纠错/取消演示保留，`/demo budget` 完成 12 次工具调用。offline 不读配置，普通输入仍为固定回复，脚本结果不代表真实模型质量。边界见 [工具契约](docs/specs/agent/tools.md)，完整默认配置见 [配置契约](docs/specs/config.md)。

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
