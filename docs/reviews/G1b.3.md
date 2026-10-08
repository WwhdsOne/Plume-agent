---
title: G1b.3 流式消费、Markdown 与思考展示审核记录
status: passed
updated: 2026-10-08
summary: G1b.3 审核通过：流式协议、Thought 与强度配置、trace、基准及终端证据；含 IME 光标与输入区分隔线反馈修复
---

# G1b.3 流式消费、Markdown 与思考展示审核记录

用户于 2026-10-08 授权执行 [技术契约](../tui-streaming.md)，同日审核通过（含审核期反馈修复）。未推进 G3。

实现于 2026-10-08 按用户要求同步到原工作目录 `/Users/Learning/AI/AGENT/Plume-agent`，当前为 main 工作区中的未提交修改。此前在 `codex/g1b3-streaming` 工作树实施并验证，原目录已有文档、依赖和开屏改动已保留；不能把整个 diff 当作本单元新改动。

## 改动与演示闭环

- **协议与模型**：`internal/model/{openai,deepseek}` 通过锁定 openai-go v1.12.0 消费流，SDK 负责 SSE 分帧；项目归一化独立答案/思考、usage、finish、tool 信息。finish 与精确 DONE 才算成功，实际读取上限 16 MiB，支持取消和幂等关闭，无自动重试。SDK 解析后的原始 JSON 再校验类型、重复键和工具身份，避免宽松转换误报成功。
- **运行时**：`internal/agent/stream.go`、`internal/app/service.go` 接通上下文准备→等待→实际思考→实际答案。队列有界并保留终态槽；取消/断流保留部分显示，但只在成功且未取消时原子提交历史。关闭阶段的取消、满队列退出和旧 run 事件都有回归覆盖。
- **配置与装配**：`internal/config/options.go`、`internal/provider/reasoning.go`、setup 与 CLI 保持 schema v1。四阶段默认文案写入新配置，旧配置首次读取时原子补齐；已验证官方 DeepSeek 模型的默认 high 同样写入。none 关闭，setup 不增加问题且保留已有值；未知端点缺省时省略参数，显式设置在 HTTP 前拒绝。`config show` 区分偏好、生效值和来源；offline 不读配置。
- **界面**：`internal/tui/{status,stream,markdown}.go` 及现有状态机支持可定制文案、三点动画、最新三行思考、首答案收起与 Ctrl+O 手动优先。用户 `>`、助手 `●` 和 `Thought` 首字符位于第 3 列，正文默认第 5 列；折行按可见宽度处理。答案 Markdown 使用 Glamour v2.0.1，首答案/终态立即刷新，其他增量 50ms 合并；已完成回复缓存，上滚冻结到手动回底。
- **证据**：`internal/telemetry/{model,stream}.go` 记录关联 run/model 的阶段、字节数、准备/首思考/模型首答案/UI 首答案；缺失值为 null，失败保留已知 usage，不记录正文、文案或凭据。新增五类人工 fixture、pty 脚本和回归测试，README、AGENTS、决策、键位、runbook、daily 与路线图同步。

Markdown 支持标题、强调、列表、引用、行内/围栏代码、基础表格和链接。链接以可读文字与地址显示，不生成 OSC 8；图片保留 alt/地址；HTML、LaTeX 与过宽表格回退源文。嵌套围栏及代码内表格不改写代码，临界宽度的表格不得截断单元格。

## 复现与实际结果

在原工作目录执行；下面使用本机 Go 的绝对路径，避免 PATH 中旧版本或缺失 Go：

```bash
rtk proxy /Users/go/go1.27.1/bin/go test ./...
rtk proxy /Users/go/go1.27.1/bin/go test -race ./...
rtk proxy /Users/go/go1.27.1/bin/go vet ./...
rtk proxy /Users/go/go1.27.1/bin/gofmt -l .
rtk git diff --check
rtk proxy /Users/go/go1.27.1/bin/go test ./internal/model/openai ./internal/tui -run '^$' -bench . -benchmem -count=1
rtk proxy env PATH=/Users/go/go1.27.1/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin ./scripts/build.sh
rtk proxy python3 scripts/pty_demo_g1b3.py
rtk proxy python3 scripts/pty_demo_g1b3.py --offline-invalid-config
```

| 验收项 | 实际结果 |
| --- | --- |
| 全仓普通测试 / `-race` | 14 个包通过 |
| `go vet` / `gofmt -l` / `git diff --check` | 通过，无输出 |
| 取消与历史提交竞态回归 | 已修复，20 次竞态回归通过 |
| 模型协议规格复核 | 通过；伪 DONE 前缀吞帧问题修复后复核通过 |
| TUI 规格复核 | 通过；表格截断与旧 tick 续订问题修复后复核通过 |
| 版本构建 | `0.1.0` / `c351746-dirty`，构建时间 `2026-10-08T03:33:29Z` |
| pty：100×32、80×24、40×24，各真彩/256/无色 | 9 个成功场景通过 |
| pty：纯答案、仅思考、断流、取消 | 4 个场景通过，各唯一终态 |
| offline + 畸形 config.json | `G1B3_OFFLINE_OK invalid_config_ignored=true` |

pty 脚本仅使用临时 PLUME_HOME 与本地 HTTP，不读写真实凭据，也不进行外部模型调用。可选 pyte 只用来还原帧，不是产品依赖；本次使用临时安装的 pyte 0.8.2，并补齐 CSI S/T 标准滚动指令，避免还原时出现重复行。未经终端字体实际渲染的帧不能代替人工手感检查。

## 证据路径与指标口径

- [pty 结果与各场景指标](evidence/G1b.3/pty/results.json)：13 个场景，每个关联 `.ansi` 原始终端流、`.txt` 净化流、`-frame-N.txt` 还原屏幕与 `.jsonl` 脱敏 trace。
- [80 列思考预览帧](evidence/G1b.3/pty/success-80x24-none-frame-1.txt)、[40 列折叠与答案帧](evidence/G1b.3/pty/success-40x24-none-frame-3.txt)：用于检查对齐、展开/折叠、链接地址与代码语言提示。
- [成功 trace](evidence/G1b.3/pty/success-80x24-none.jsonl)、[断流 trace](evidence/G1b.3/pty/disconnect-80x24-none.jsonl)、[取消 trace](evidence/G1b.3/pty/cancel-80x24-none.jsonl)、[仅思考 trace](evidence/G1b.3/pty/reasoning-only-80x24-none.jsonl)：分别结束为 ok、stream_interrupted、cancelled、invalid_response。仅思考/取消的首答案为 null，没有伪造 UI 首答案。
- [offline 结果](evidence/G1b.3/pty/offline-result.json)：畸形配置仍可启动并完成离线回答。
- [基准数据](evidence/G1b.3/benchmarks.json)、[验证清单](evidence/G1b.3/verification.json)：本机原始数值与执行结果。

本地 fixture 固定等待 250ms，思考分片间隔 150ms，答案分片间隔 70ms。这些延迟用于验证状态与渲染，不能用于评价供应商、生成 token/s 或宣传性能提升。各最新场景的准备/首思考/首答案/UI 可见时间见 results.json；变化列均为 N/A。

| 指标 | 实测绝对值 | 变化 / 限制 |
| --- | --- | --- |
| pty 自动检查 | 13/13；offline 补充 1/1 | N/A，人工 fixture |
| fixture 失败分类 | 3/3 符合预期 | N/A，包含刻意取消、断流和无答案 |
| 终态 trace 完整率 | 13/13，每场景唯一 run 终态 | N/A，无真实服务样本 |
| UI 首答案证据 | 10/10 成功场景；断流部分答案也有可见帧 | N/A，未将思考计为答案 |
| 取消按键→run 终态 | 50.24ms | N/A，单次 pty 样本，含独立 Esc 判定等待；时间戳精度 1ms |
| 真彩 / 256 / 无色 | 最终 ANSI 分别含真彩、仅索引色、无前景/背景色序列 | N/A |
| 成品二进制 | 24,435,682 bytes | 同机基线 12,254,706 bytes；增加 12,180,976 bytes |
| 真实模型延迟、token/s、质量收益 | N/A | 未联网，usage 数值来自人工 fixture |

二进制均经 scripts/build.sh、Go 1.27.1/darwin-arm64、`-s -w` 构建。增量包含 Glamour 及其间接依赖和本单元全部代码，不能归因于单个包，也不是性能改进指标。

## 基准与局限

环境：Apple M5 Pro、darwin/arm64、Go 1.27.1。每项是固定输入的单次操作成本，不代表一次聊天的总分配，变化列 N/A。

| 处理 | 输入 | 耗时 | B/op | allocs/op |
| --- | --- | --- | --- | --- |
| SDK 流式归一化 | 1 KiB | 28.630 µs | 60,245 | 450 |
| SDK 流式归一化 | 10 KiB | 237.539 µs | 488,723 | 3,582 |
| SDK 流式归一化 | 100 KiB | 2.320 ms | 4,774,141 | 34,912 |
| Markdown 派生渲染 | 1 KiB | 0.623 ms | 526,287 | 19,642 |
| Markdown 派生渲染 | 10 KiB | 5.737 ms | 4,972,565 | 191,928 |
| Markdown 派生渲染 | 100 KiB | 55.249 ms | 47,012,754 | 1,913,720 |
| 思考可见行换行 | 1 KiB | 16.868 µs | 12,681 | 22 |
| 思考可见行换行 | 10 KiB | 156.343 µs | 123,949 | 22 |
| 思考可见行换行 | 100 KiB | 2.009 ms | 1,281,602 | 27 |

100 KiB Markdown 完整重绘超过 50ms 合并间隔，仍有较大累计分配；当前方案缓存完成回复、限制刷新频率，但持续长文输出可能影响输入响应。后续若出现实际瓶颈，再单独设计分块渲染，不能把本次结果表述为恒定 20fps 保证。

能力验证仅覆盖契约中的官方 DeepSeek 端点与模型，第三方代理不能因为品牌相同就声称支持思考开关。旧程序降级后再保存配置可能丢弃新增字段。工具事件只归一化，不执行工具；G3、Windows 实测与真实终端 Shift+Enter 人工检查仍待单独授权/验收。

## 待用户检查

- 在自己的终端检查 Thought 三点动画、默认三行预览、首答案折叠与 Ctrl+O 手动优先。
- 检查中文/emoji 的列宽、40 列换行、上滚时增量不拉回底部，以及 Markdown 的阅读效果。
- 真实模型使用时确认 high/none 与 config show 的能力状态一致；本单元没有调用真实模型。
- 路线图已同步 G1b.3=review/date=2026-10-08，并做静态数据校验。内置浏览器安全策略拒绝 `file://` 本地预览，未绕过限制，页面视觉检查仍需人工完成。

可在原工作目录运行 `./plume chat --offline`；配置就绪后运行 `./plume` 体验真实流式交互。实现保持待审，不自动提交、推送或推进下一单元。

2026-10-08 安装补充：用户反馈原目录重装未见 stream，确认其安装来源仍是旧代码。已从 G1b.3 工作树执行 `scripts/build.sh --install`，`/Users/go/bin/plume` 的构建时间为 `2026-10-08T04:01:27Z`，体积 24,435,682 bytes；已安装二进制通过本地 HTTP + pty 流式验证（125 reasoning bytes / 138 answer bytes）。上面的完整证据仍对应 03:33:29Z 构建；安装仅改变构建时间，未改变实现或提交状态。

随后按用户要求同步回原目录：70 个文件同步比对一致，证据目录保留原始字节；原目录全仓竞态检测、vet、gofmt、diff 检查通过。本地 plume 构建时间 `2026-10-08T04:08:30Z`，安装版构建时间 `2026-10-08T04:08:55Z`；安装版再次通过隔离流式验证，含真实 thinking/responding 阶段、UI 首答案与唯一终态。补充证据见 [原目录构建后的成功 trace](evidence/G1b.3/original-directory/success-80x24-none.jsonl)。现在可直接从原目录构建/安装，代码仍为 main 工作区未提交修改。

用户截图反馈的 Markdown 原始标记已定位并修复：未闭合代码围栏曾导致整条回复回退为原文，代码中的 `$`/HTML 样式文本也触发相同问题。现在未闭合围栏交由 Glamour 渲染，围栏外仍保留原有安全回退；标题、加粗及代码内容在增量和完成状态下可读。回归测试覆盖截图同结构与聊天区终态；本节新增修复不更改模型协议或 trace 指标。

该修复已在原目录通过全仓测试、TUI 竞态、vet 和格式检查，并重新安装（构建时间 `2026-10-08T04:18:34Z`）。已安装二进制的本地 HTTP + pty 场景 `G1B3_MARKDOWN_OK open_fence_rendered=true`：在 [最终屏幕帧](evidence/G1b.3/pty/markdown-open-fence-80x24-none-frame-3.txt) 中，加粗、标题和代码围栏的原始标记均未出现，代码正文仍可读；[场景 trace](evidence/G1b.3/pty/markdown-open-fence-80x24-none.jsonl) 为唯一成功终态。这是人工 fixture，真实模型的排版仍需用户实际检查。

用户随后反馈两个输入区问题并已修复（2026-10-08）：

- **输入法候选窗出现在输入框右侧**：textarea 默认用字符反色的虚拟光标，未声明 `View.Cursor` 时硬件光标停在帧尾，IME 候选窗跟随硬件光标。现改用真实终端光标（`SetVirtualCursor(false)`），`TeaModel.View` 按 `textarea.Cursor()` 平移到输入区在帧内的行列，X 按 `ColumnOffset` 修正双宽字符。pty 实测光标随输入移动到插入点（"hi" 输入后第 8 列、退格回第 6 列），不再停在右侧。
- **输入框上下主题色横线**：新增 `inputSeparator`，输入区上下各一条与终端同宽的 `─` 横线（`themePrimary`），记录区高度相应减 2 行。

新增 `internal/tui/view_test.go` 覆盖分隔线布局与光标行列（含中文双宽列宽）；全仓 402 项测试、关键包竞态、vet、gofmt 通过，`scripts/pty_demo_g1b3.py` 13/13 场景不回归，已重新构建安装。这两项修复不改变模型协议、事件与 trace 口径。
