---
title: 项目现状审核与优化建议（2026-10-06）
status: active
updated: 2026-10-07
summary: 审核 HEAD 85f7ac6：G1b.2 当前能力、两轴缺陷、9 条离线复现与量化优化方案
---

# 项目现状审核与优化建议（2026-10-06）

审核对象为 `85f7ac6`，功能增量参考 `8a555b8...HEAD`，同时检查当前运行链路。开始审核时工作区干净。审核过程只新增本报告，未实施修复、升级依赖、改动用户配置、提交或推送；随后日终归档另见[当日日志](../daily/2026-10-06.md)。所有测试显式清空 `DEEPSEEK_LIVE_KEY`，没有真实模型请求。

总体判断：当前已经有可运行的非流式 TUI、多轮上下文、模型工厂和离线协议测试，模块划分适合继续逐单元推进。现有测试全部通过，但对错误、响应完整性、中文显示和兼容配置的判定仍有缺口。需要先修正确性，再通过基线决定性能改造。

## 1. 当前实际进度

| 单元/能力 | 当前情况 | 证据与限制 |
| --- | --- | --- |
| G0 / G1a | 已通过 | 配置、凭据、供应商/渠道注册、设置向导与 setup trace |
| G1b.1 | 已通过 | openai-go v1.12.0 非流式适配、受控端点、fake、12 个离线种子 |
| G1b.2 | 已通过 | 裸 plume/chat、离线模式、多轮会话、串行提交、取消、新会话、基础 TUI |
| 模型传输方案 | 已改为 OpenAI 官方 SDK | 0003 明确替代此前 Resty 方案；本次依据最新仓库决策审核 |
| G1b.2.1 | 契约已写，迁移未实现 | 当前仍是 Bubble Tea/huh v1 与旧键位；v2/输入历史不作为本轮缺陷 |
| G1b.3 / G3 | 未实现 | 流式调用、工具声明与循环尚未开启；接口明确返回 unsupported |
| 微信/飞书/QQ | 未接入 | 微信保留调研和预设；首版 TUI 不依赖扫码 |
| 真实聊天 trace | 尚未接线 | ModelRecorder 有实现，但 cmd/app/agent 没有调用它；run 事件目前仅供 UI 内存消费 |
| 性能/比较报告 | 尚无可复现基线 | 没有 Benchmark 函数、逐例结果文件与统一 compare 入口 |

不要把 `internal/eval` 中成功的 trace 单测描述为真实聊天全链路可追踪；当前真实聊天 trace 完整率应记 N/A。G1b.2 审核记录已经明确写出这个局限。

## 2. 本轮验证

| 检查 | 结果 |
| --- | --- |
| `DEEPSEEK_LIVE_KEY= go test ./... -count=1 -cover` | 14 个包全部通过 |
| `DEEPSEEK_LIVE_KEY= go test -race ./... -count=1` | 14 个包全部通过，无竞态报告 |
| `DEEPSEEK_LIVE_KEY= go vet ./...` | 通过 |
| `gofmt -l cmd internal` | 列出 `cmd/plume/chat_test.go`，需要格式化；未代为修改 |
| 12 个离线 smoke | 12/12 通过；这是工程回归，不是模型质量或延迟基线 |
| 现有 `-bench . -benchmem` | 没有 Benchmark 行；PASS 不代表已有性能数据 |
| 临时 overlay 的 9 条边界断言 | 全部失败，分别复现下文问题；不属于原仓库测试失败 |

当前语句覆盖率的部分结果：app 97.4%、openai 87.3%、telemetry 82.0%、config 75.9%、tui 62.7%、cmd 26.6%。覆盖率只能帮助发现未执行代码，不能证明异常语义或显示宽度正确。

诊断文件放在临时目录 `/tmp/plume-audit.3NNTg8/`，没有加入源码目录。复现命令：

```bash
rtk proxy env DEEPSEEK_LIVE_KEY= go test \
  -overlay=/tmp/plume-audit.3NNTg8/overlay.json \
  -run '^TestAudit' -count=1 -v \
  ./internal/model/openai ./internal/tui ./internal/app ./cmd/plume
```

此命令预期退出 1，因为断言表达的是应有行为；该目录只作为本次诊断证据，系统清理后需依据下列 fixture 重建。

| 断言 | 实际观察 |
| --- | --- |
| 401 回显虚构测试凭据不得进入错误/trace | normalized error 和/或 ModelRecorder 输出含 `test-key-123` |
| 仅有 total_tokens 时不能宣称全部 usage 已知 | `{OK:true PromptTokens:0 CompletionTokens:0 TotalTokens:15}` |
| `{"choices":[{}]}` 必须拒绝 | 返回空 content、finish=unknown、nil error |
| 无外部 deadline 时存在模型默认超时 | HTTP 请求 context 无 deadline，client 也没有配置 Timeout |
| 错误正文读取不超过 16 KiB | 实际读了 65,560 字节，约定上限 16,384 字节 |
| 中文/emoji 折行保持有效 UTF-8 | 第一片为 `e4bda0e5`，截断一个汉字 |
| 20 列聊天行计入前缀后不溢出 | 实际显示宽度 30 |
| finish_reason=length 不提交完整会话轮次 | 已追加一轮历史 |
| 合法旧配置省略 DeepSeek base_url 可使用默认值 | config.Validate 成功，buildRuntime 报 base_url is required |

## 3. 工程规范轴

这组检查依据 AGENTS.md 的凭据/脱敏、缺失数据、端点和生命周期约定。以下是硬约定问题；没有把命名偏好或设计模式数量当作缺陷。

### S1 — P1：错误摘要没有脱敏

位置：`internal/model/openai/chat.go:239`、`internal/telemetry/model.go:97`，以及 `internal/tui/update.go:155`。

SDK 的 `Error()` 含请求 URL 和原始错误正文。当前适配器只裁剪 512 个 rune；ModelRecorder 随后记录 `mErr.Error()`，TUI 也显示这个字符串。上游 401 回显虚构凭据即可复现泄漏。当前聊天尚未接 trace，但界面错误会出现，记录器在使用时也会记录它。

违反 0003 §4/§9 的“先脱敏再裁剪”和密钥永不记录约定。建议对外只提供允许的错误分类、HTTP 状态、受控 request ID 与安全摘要，避免记录 SDK 原始 Error；再补凭据、消息正文、敏感 URL 和控制序列的回归 fixture。验收目标：这些哨兵在 UI 和 trace 的泄漏数为 0。

### S2 — P2：错误正文 16 KiB 上限没有实施

位置：`internal/model/endpoint/endpoint.go:104`、`internal/model/errors.go:29`。

目前仅预检 Content-Length 是否超过 8 MiB，`MaxErrorBodyBytes` 没有消费者。SDK 会全量读取小于此门槛的错误正文；临时 reader 实测读取 65,560 字节。

这与文档已承认的“成功非流式无 Content-Length 不逐字节截断”局限不同：错误正文的 16 KiB 约定没有落实。建议在错误响应进入 SDK 前施加有界读取，超限仍保留 HTTP 状态和稳定错误分类，关闭 body。验收指标：已读取错误字节数不超过约定上限。

### S3 — P2：部分 usage 被当作已知零

位置：`internal/model/openai/chat.go:176`。

只检查 total_tokens 是否有效就设置 `Usage.OK=true`，prompt/completion 缺失时被 SDK 默认值变为 0。它会误导状态栏、trace 和未来费用比较。

建议整组已知必须验证三个字段；或把各字段设计为独立可选值。需覆盖缺失、null、错误类型、负值和不一致总数。不能用推测出来的分项冒充供应商计量。

### S4 — P2：默认模型调用超时没有实施

位置：`internal/model/endpoint/endpoint.go:73`、`internal/model/openai/chat.go:40`、`internal/app/service.go:98`、`cmd/plume/chat.go:92`。

生产路径只有 Background/WithCancel，没有 WithTimeout、WithDeadline、SDK RequestTimeout 或 http.Client.Timeout。不会返回的端点可让 TUI 一直 busy，直到人工取消。现有超时用例是测试主动设置 100/200ms deadline，不能证明默认 120s 生效。

建议适配器施加默认 120s、与调用方更早 deadline 取最小值，并覆盖响应 body 读取阶段；run 180s 在其生命周期实现时独立施加。用可控短 timeout 测响应头等待、body 卡住、人工取消和资源退出，避免测试实际等待两分钟。

## 4. 需求符合轴

这组检查依据现行 0003 与 G1b.1/G1b.2 的已交付范围。与规范轴重叠的发现分别保留，计数不能直接相加。

| 编号 | 要求与代码位置 | 实际缺口 |
| --- | --- | --- |
| R1，P1 | 0003 §4/§9：错误正文、URL、凭据不得原样暴露；`openai/chat.go:239` | SDK 错误回显被直接裁剪并传播，见 S1 |
| R2，P2 | 0003 §4：每次模型调用默认 120s；`endpoint/endpoint.go:73` | 生产路径没有默认 deadline，见 S4 |
| R3，P2 | 0003 §3/§4：无法表达的语义拒绝，2xx 协议错误仍失败；`openai/chat.go:159` | `choices:[{}]` 被归一化成成功空回答；缺 message/无效 content 等未校验 SDK JSON 字段元数据 |
| R4，P2 | 0003 §3：usage 缺失不是 0；`openai/chat.go:176` | 只有 total_tokens 时缺失分项被宣称为已知，见 S3 |
| R5，P2 | 0003 §7：兼容旧配置；`cmd/plume/chat.go:135` | 没有调用 ModelConfig.ResolveBaseURL，config.Validate 接受的默认地址配置被工厂拒绝 |
| R6，P2 | 0003 §4：错误正文最多读取 16 KiB；`endpoint/endpoint.go:104` | 仅存在 8 MiB 声明长度预检，见 S2 |

R3 建议：先检查候选、message、角色、文本/工具字段及 finish reason 是否满足本阶段契约，明确拒绝不支持的响应。SDK 反序列化没有返回 error 不等于业务响应完整。避免对拒绝、音频或旧 function_call 等有语义的字段静默丢弃；本阶段无需实现这些能力，但应表达“不支持/不完整”。

R5 建议：在模型装配处使用现有 `ResolveBaseURL` 规则，同时保留已保存的非默认 URL。增加“config.Validate 成功 → buildRuntime 成功”的兼容 fixture，而不是只测两者各自的 happy path。

## 5. 独立的 TUI / 会话复核

### U1 — P1：按字节折行破坏中文和 emoji

位置：`internal/tui/view.go:60`，以及该文件 `renderLines` 的前缀拼接。

`len(paragraph)` 与 `paragraph[:width]` 都以字节计量。中文/emoji 被截在 UTF-8 编码中间，渲染出现替换字符或丢字；即便纯 ASCII，也没有给 `Plume >` 前缀预留宽度，20 列输入实测输出 30 列。实际窗口宽度决定问题是否出现，因此英文短文本的 pty 演示不会发现它。

建议按 grapheme 和终端列宽折行，并扣除前缀/缩进。不能仅将 byte 改 rune，因为汉字宽度、组合 emoji、变音符的显示宽度仍不同。使用当前终端栈已有宽度工具，迁移 v2 时保留同一组语义测试。

验收：中文、emoji、组合字符与 ASCII 在不同窗口宽度下均为有效 UTF-8；实际行宽不超 viewport；去掉呈现前缀并还原换行后正文不丢失；缩放正确重排。

### U2 — P2：截断回答被提交为成功会话

位置：`internal/agent/runtime.go:57`、`internal/app/service.go:122`。

适配器保留 `finish_reason=length`，Runtime 直接返回成功，Service 随即 Append 并发送 run_completed。用 fake 返回 partial/length 已复现历史增加一轮；content_filter 和 unknown 也没有成功语义检查。

违反 0003 §6 的“截断/内容阻断不当完整成功”和首阶段“只提交完整轮次”规则。这是当前非流式聊天可以触发的问题，不需要等待 G3 工具循环。

建议由 Runtime 判断本阶段允许的完成原因，UI 保留并标记不完整内容，禁止提交成功历史；保留 usage/错误及终态证据。验收：length/content_filter/异常 finish 不增加历史，不产生完整成功终态，正常 stop 仍正确提交。

## 6. 还应留意的生命周期设计

以下尚未按真实 UI 路径复现为用户故障，单列为设计隐患：

- `Service.finish` 在 Append 前设 busy=false（`service.go:116`）；若其他调用方此时 Submit/Reset，下一轮可能在前一轮历史提交前开始。未来渠道复用前，应把“提交结果/历史 → 发布终态 → 允许下一轮”的顺序写成契约并测试。
- `cmd/plume/chat.go:106` 的事件桥没有关闭信号，Service.Close 也不关闭事件 channel。CLI 退出会回收整个进程，但若后续支持同进程重开聊天，应保证 bridge 退出并等待其结束。
- `Service.emit` 无条件丢弃满队列事件。当前 TUI 每轮只有两个事件且阻止重复发送，尚未复现正常路径终态丢失；流式上线前必须保证错误/终态不因文本增量拥塞被丢弃，不能沿用“满时直接丢”的假设。

## 7. 三个具体、可量化的改进

### E-A：补强评测的判定力

现状：`internal/eval/smoke_test.go:105` 的成功服务器返回固定结果，不读取请求。smoke-002/003/004 声称覆盖历史、工具历史和 temperature，但仅这组 smoke 的 PASS 无法证明字段被正确发送。适配器单测另外覆盖了 temperature/tool history，因此这是 runner 证据不足，不是完全没有相关测试。

改造：数据集增加预期请求字段和次数，server 对方法、路径、鉴权、模型、消息顺序、参数进行断言；记录 case ID、输入/实际请求、结果、数据集哈希与 trace 引用。

验证：分别删除历史、tool_calls、temperature 做三个受控变异，目标对应 case 均失败；原实现 12/12 继续通过。指标是变异检出率、请求断言覆盖、失败原因是否正确，而不是单纯增加测试数量。

### E-B：把 trace 接到真实聊天

现状：真实聊天没有模型/run 落盘 trace，完整率和 trace 开销没有生产入口基线。记录器只在测试中使用，不能满足用户最初“每一步 trace”的展示目标。

改造：把 app run、模型调用和会话关联接到文件 sink，统一 run/span ID 与终态；模型/工具不要另起不相关的记录体系。成功/失败/取消使用同一计量入口，费用缺失继续 unknown。

实验：fake 分别运行成功、失败、取消各 20 次，再做一次强制崩溃；正常结束的 run 应全部通过关联/唯一终态检查，崩溃留下的不完整链必须进入分母。交错运行采集开/关至少 200 次，报告 p50/p95、分配、事件字节数和采集开销。

护栏：凭据哨兵泄漏 0；错误不被当作成功；unknown 不填 0；不能剔除缺日志 run 提高完整率。本轮只提出方案，没有测得任何改造收益。

### E-C：建立长聊天渲染基线，再决定缓存

现状：每追加一行都会全量 renderLines，再复制到 viewport；历史越长，刷新成本越高。当前没有 Benchmark 函数，也没有记录长中文聊天的运行数据。

实验：先修 U1，再固定 10/100/1000 轮、每轮 500 字、中文/emoji 与 ASCII、80/120 列窗口，测 ns/op、B/op、allocs/op；比较全量重排与“按宽度缓存已折行内容/合并刷新”。可以预注册“1000 轮场景分配量或渲染耗时下降至少 20%”作为假设。

护栏：正文零丢失、行宽不越界、缩放重排正确、错误和终态不丢；流式上线后首文本/完成延迟不得恶化超过事先门槛。20% 是实验目标，不是本轮收益。

## 8. 文档快照与推进建议

AGENTS.md 的开头仍说“当前代码仅完成 G0/G1a”；第一阶段计划开头仍说“新增功能均未实现”“没有模型 HTTP/TUI/runner”；0003 仍有 CLI 文案/渠道步骤待改的旧描述，均与当前 G1b.2 通过状态冲突。G1b.2 审核还说 bubbles 已提升为直接依赖，但 go.mod 仍标 indirect。建议下一次文档维护统一现状入口，保留历史记录时明确标历史，不让后续 Agent 用旧快照重新开发。

建议开一个可独立审核的“当前正确性补强”单元，先覆盖本报告的脱敏、Unicode、默认超时、响应完成语义、usage 和配置兼容，再继续已立项的终端迁移/流式。真实聊天 trace 与性能基线应明确归入可交付单元；每次优化都按相同 case 和环境列出改前/改后/绝对差/相对变化，并报告退步。

规范轴 4 项，最严重为错误泄露；需求轴 6 项，最严重同为错误传播未脱敏；独立 UI/会话复核 2 项，其中中文显示为高优先级。两轴包含重叠，合计为 8 组不同问题，9 条边界断言已复现。后续流式、工具、网关、v2 迁移的未实现状态没有被算作缺陷。本轮没有修改实现或代替用户推进审核单元。
