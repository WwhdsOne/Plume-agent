---
title: G1b.4 审核记录：可配置底部状态栏
status: passed
updated: 2026-10-09
summary: G1b.4 已通过：默认配置落盘、上下文/缓存统计、Git/uv 与会话计时，716 项竞态与 12 个 PTY 场景通过
---

# G1b.4 审核记录：可配置底部状态栏

用户审核 [技术契约](../specs/tui/statusline.md) 后授权实现；本单元在原目录完成，已安装到 `/Users/go/bin/plume`。2026-10-09 用户确认**通过**（此前已授权补日志、提交并推送）。G3 工具循环未开始。

> **证据说明（2026-10-09）**：按「通过即删」治理（见 [TUI runbook](../runbooks/tui.md)），审核期证据已随文档重组移除。正文出现的 `evidence/G1b.4/...` 路径指向提交 `9860902`，用 `git show 9860902:<路径>` 可取回原始字节，或运行 `PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b4.py` 重新生成。

## 交付行为

底部优先一行，完整字段放不下时才使用两行；可选进度条先缩至最低三格再尝试合并。显示供应商、API 模型名、有效思考强度、上下文百分比及输入量/容量、缓存、Git/uv、会话及 run 时长；可选进度条、最近/累计用量、run ID 与工作目录。标签亮青、数值浅青、unknown 提示黄，上下文保留告警色。宽 <60 或高 <18 时限制一行，按配置优先级隐藏；优先级相同时先隐藏数组靠后的字段。输入框上下分隔线和真实终端光标随实际栏高调整，Thought 标题可见时不重复显示阶段与 run 时长。

所有 `tui.status_line` 默认字段及 14 个 item 已实际写入 config.json。官方 HTTPS DeepSeek 的 `deepseek-flash` / `deepseek-v4-pro` 缺失或 null 容量自动补写 `context_window_tokens:1000000`，保留用户手填正整数；其他模型或自定义端点保持 null。旧配置 Load 原子补齐，保留用户数组选择（包括 `[]`）、false/0、自定义标签和未知 JSON；完整配置重复读取不改写。setup 保留配置，不新增问题；offline 不加载用户文件。不支持的 schema 在读取时明确拒绝，原文件不改写。

已知 1M 容量的新会话显示 `ctx: 0.0% 0/1M`。默认 context_format:usage，固定一位小数，完成后保留最近实际 PromptTokens，去掉 `(last)`；context_format:bar 可选原进度条。未知容量显示 `capacity unknown`；生成期间保留初始零或请求前快照，收到输入统计立即更新，新调用终态仍缺输入统计才显示 `usage unknown`。没有发送前估算器。缓存初始 Go 零值为 `cache [░░░░░░░░░░]0.0% 0/0`，实际条体/百分比/计数沿用 CC/Pi 的对应输入分母；Provider 标签默认首项。两种缓存口径在生成期间同样保留请求前显示，收到新缓存统计才刷新，终态缺失按 unknown/partial 处理。初始值不计为实际调用，实际未知与命中 0 仍区分。缓存由模型适配器归一化，OpenAI/DeepSeek 字段通过 HTTP/流式 fixture。cached input 是 prompt 子集，不重复累计。app 按调用 ID 替换快照，未知调用标 partial，失败/取消的已知用量仍纳入累计。

Git/uv 在 app 异步只读采集，缓冲保留最新快照，串行执行、超时及退出取消；禁用字段停止对应采集。Git dirty 包含未跟踪文件，失败保留数据时标 stale。uv.lock、活动环境和仅存在的 .venv 分别标识，不运行 uv/Python 或任意配置脚本。会话计时含 idle，Ctrl+N 清零、Ctrl+L 保留；run 终态耗时冻结。

## 改动边界与审查

`internal/config` 定义/校验/默认持久化及 raw JSON 保留；`internal/model` 归一化缓存，telemetry 记录数值或 null；app 汇总调用统计并提供环境采集器；TUI 只消费快照和渲染；CLI 装配与 config show。未新增包目录或外部 Go 依赖，未升级 SDK。

规格及质量分别审查后修复并补 RED/GREEN 回归：

- cache 格式 both 在没有比率时保留已知命中量；统一使用配置 priority/order。
- 禁用栏或空 items 时，上滚阅读中的关键拒绝、提交错误、取消/失败解除阅读冻结并滚动可见；普通流式更新仍保持上滚位置。
- 未知/缺失 schema 明确报字段错误，避免在线启动空指针。
- 准备阶段取消未发生新模型调用时保留真实最近调用或初始零；新调用终态没有 usage 仍为 unknown，不把旧值当本次统计。
- 用户反馈后的容量定向回归先出现 20 项失败，补齐两官方模型默认容量、null 迁移、端点边界与 setup 保留语义后通过。
- Thought 可见性临界处输入增高曾导致 80×24 帧渲染 25 行；先复现再修复布局收敛和实际栏高保留，24 行、真实输入光标及连续刷新回归通过。

## 可复现验证与证据

```bash
rtk go test -race ./...
rtk go vet ./...
rtk gofmt -l .
rtk git diff --check
rtk ./scripts/build.sh
rtk proxy env PYTHONPATH=/tmp/plume-splash-preview-deps python3 scripts/pty_demo_g1b4.py
rtk ./scripts/build.sh --install
rtk proxy env PYTHONPATH=/tmp/plume-splash-preview-deps python3 scripts/pty_demo_g1b4.py --binary /Users/go/bin/plume --case truecolor-wide-220x24 --output docs/reviews/evidence/G1b.4/installed
```

PTY 依赖仅为临时验收用 pyte 0.8.2 / wcwidth 0.9.2，不属于 plume 运行依赖；其他机器可在独立 Python 环境安装它们再运行。脚本使用临时 PLUME_HOME、临时 Git/uv 目录、假凭据与本地 HTTP fixture，没有真实供应商调用。

| 项目 | 实际结果 | 变化 |
| --- | --- | --- |
| 全仓竞态测试（10-08 初次交付） | 637 项通过，14 包 | N/A |
| vet / gofmt / diff | 通过 / 无输出 / 无错误 | N/A |
| PTY 主验收 | 9/9 通过，含真彩色与自动单行 | N/A |
| 已安装程序验收 | 真彩色宽屏场景 1/1 通过 | N/A |
| 默认配置 | 12 个状态栏顶层键、5 个 context_bar 键、14 个 items；官方两模型容量 1000000 | N/A |
| 真实配置权限及幂等 | 0600；第二次读取文件字节不变 | N/A |
| 安装构建 | v0.1.0 / e118207-dirty / 2026-10-08T15:00:55Z，darwin-arm64 Go 1.27.1 | N/A |
| 发布式二进制体积 | 24,739,714 bytes（-s -w），无新增 Go 依赖 | N/A |
| 外部模型延迟/费用/命中收益 | 未测量 | N/A |

证据入口：

- 验证摘要（历史证据，已按「通过即删」移除）：`evidence/G1b.4/verification.md`。
- [9 场景结果及终态 trace 摘要](evidence/G1b.4/pty/results.json)。
- [默认完成帧](evidence/G1b.4/pty/default-120x24-pre-exit.txt)：实际 `ctx(last):[██░░░░░░░░] 20%`、`cache(last):8`、Git dirty、活动 uv 环境。
- [定制一行帧](evidence/G1b.4/pty/custom-80x24-pre-exit.txt)、[20×8 窄屏帧](evidence/G1b.4/pty/narrow-20x8-pre-exit.txt)、[未知用量帧](evidence/G1b.4/pty/unknown-40x16-pre-exit.txt)。
- [自动单行真彩色帧](evidence/G1b.4/pty/truecolor-wide-220x24-pre-exit.txt)、[实际字符颜色](evidence/G1b.4/pty/truecolor-wide-220x24-pre-exit-colors.json)：标签 `5bc8c8`、数值 `7dd3d8`；pyte 的 brightbrown 名称对应 ANSI 93 亮黄。
- [取消 trace](evidence/G1b.4/pty/cancel-80x24.jsonl)、[断连 trace](evidence/G1b.4/pty/disconnect-80x24.jsonl)，错误码分别 cancelled / stream_interrupted。
- [已安装程序结果](evidence/G1b.4/installed/truecolor-wide-220x24-result.json)。各目录包含退出前文本帧、ANSI 与持久化测试配置；密钥和输入正文不进入 trace。

路线图已在浏览器打开核查：G1b.4 为待审核，G3 保持未开始；仅修改数据区。

## 局限与待用户检查

1. 重启 `plume` 核查亮色标签、单行优先、窄窗口、Thought 与输入光标的实际手感；kitty/输入法/Windows 仍需真实终端人工验证，PTY 不能替代。
2. 官方 DeepSeek 两预设自动填入 1M，新会话显示 0%。其他模型容量仍需核对后填正整数；它只影响显示，不改变 API 请求或执行预算。生成期间保留初始零或请求前快照，终态仍缺输入统计才显示 unknown；已知数值来自实际请求，不是发送前预测。
3. 缓存依赖供应商是否报告字段；显示 unknown 不能解释为命中 0。没有计价、自动压缩、tool/MCP/skill 状态或会话跨重启恢复。
4. 编辑 `tui.status_line.items`、label、priority、行位置及格式，重启核查配置体验；新会话仅重置统计，不热加载配置。

## 2026-10-09 启动开屏消失修复

用户反馈能聊天，但羽毛、圆角框或 UI 消失。真实 PTY 对比旧构建与今早安装版均复现：首帧没有盲文羽毛，提示文字按一列竖排。根因是状态栏触发的布局刷新可能先于 WindowSizeMsg；颜色能力、环境快照或时钟消息使 resize 在宽高为 0 时消耗 splash，后续有效窗口尺寸无法恢复开屏。

`internal/tui/update.go` 的 resize 在宽高不是正数时直接返回，保留开屏直到有效尺寸。新增六种启动消息顺序回归，先失败后通过；PTY 脚本增补宽屏启动帧的羽毛和圆角下边检查。预览脚本的版本检查兼容宽/窄布局既有大小写，显示内容不变。保留修复前已有的 Bubble Tea v2.1.0 / x/ansi v0.11.9 依赖升级，不回退依赖，不更改配置。

验证按 verification-before-completion 执行：全仓竞态 644 项、14 包通过；vet、gofmt、diff 检查通过；增强后的 PTY 9/9 通过。已安装程序真彩色宽屏 1/1 通过，首帧实际有 192 个盲文字符、圆角下边及两条输入区分隔线。100×34 真彩色与 256 色预览均核对完整 20 行羽毛、闭合对齐的圆角框和主题色；见 [本次终端证据](evidence/G1b.4/startup-fix-2026-10-09/results.json)、[安装后结果](evidence/G1b.4/startup-fix-2026-10-09/installed/truecolor-wide-220x24-result.json)、[完整开屏预览](evidence/G1b.4/startup-fix-2026-10-09/preview/splash-braille-100x34-truecolor.png)。

从原目录重新安装到 `/Users/go/bin/plume`：v0.1.0 / e118207-dirty / 2026-10-09T00:30:19Z，24,756,562 bytes。重启正在运行的旧 plume 才会使用修复。此前 10-08 的构建和证据作为历史保留，本单元仍待用户审核，未提交或推送。

## 2026-10-09 参考图立体标题调整

用户直接提供 HERMES-AGENT 图片，要求 PLUME-AGENT 模仿其立体像素字形。标题改为六行实心笔画与双线阴影，沿用左对齐及雾青主题：主青主体、深青轮廓。94 列以上使用 94 列完整版，80–93 列使用 71 列同风格紧凑版，低于 80 列保持既有羽毛/文本布局。开屏总高由 27 行变为 28 行，记录区不足时继续滚动；不新增依赖或配置。

字形与宽度定向测试先因旧五行字形失败，替换后 7 项通过；TUI 竞态 94 项、全仓竞态 650 项（14 包）通过，vet、gofmt、diff 检查通过。增强 PTY 9/9 通过，100×34 真彩色/256 色与 80×34 真彩色三份实际开屏检查全部通过，PNG 已查看。证据见 [完整字形](evidence/G1b.4/shadow-wordmark-2026-10-09/preview/splash-braille-100x34-truecolor.png)、[紧凑字形](evidence/G1b.4/shadow-wordmark-2026-10-09/preview/splash-braille-80x34-truecolor.png)、[PTY 结果](evidence/G1b.4/shadow-wordmark-2026-10-09/pty/results.json)。

原目录重新安装 `/Users/go/bin/plume`：v0.1.0 / e118207-dirty / 2026-10-09T00:41:42Z，24,756,610 bytes；[已安装真彩色宽屏验收](evidence/G1b.4/shadow-wordmark-2026-10-09/installed/truecolor-wide-220x24-result.json) 1/1 通过。旧证据作为历史保留，当前仍待用户审核，未提交或推送。

## 2026-10-09 缓存占比及 CC/Pi 配置

用户要求显示改为 `cache: x%`，并指定默认 CC 会话累计、Pi 最近一次口径可在设置开启。默认 `tui.status_line.cache_format:ratio`、`cache_scope:session`、cache item 的 `label:cache`；把 cache_scope 改为 last_call 就切到最近调用，重启生效。格式 tokens/both 仍可选，scope 与格式独立；旧配置首次读取只补缺失项，保留显式自定义，setup 不加问题。

会话累计是缓存读取命中总和 / 对应请求总输入总和，按 token 加权，不平均各次百分比；最近模式是最新调用的缓存读取命中 / 输入。输出与上下文容量不参与计算，PromptTokens 已含缓存，不重复相加。app 同次调用快照替换，终态不重复累计，失败/取消中已取得的数据保留；累计缺数据时只计算缓存和输入均已知的同组调用并标 partial，全未知/零分母/溢出为 unknown。Ctrl+N 清零、Ctrl+L 保留。

配置定向回归先出现 8 failed，显示/口径回归先出现 5 failed；实现后全仓竞态 674 项、14 包通过，vet/gofmt/diff 检查通过。PTY 增补两次请求场景：首次 8/20=40%，第二次 48/60=80%，默认累计 56/80=70%，Pi 模式为 80%；原 9 场景及两模式共 11/11 通过。见 [完整结果](evidence/G1b.4/cache-ratio-2026-10-09/pty/results.json)、[累计帧](evidence/G1b.4/cache-ratio-2026-10-09/pty/cache-session-80x24-pre-exit.txt)、[最近调用帧](evidence/G1b.4/cache-ratio-2026-10-09/pty/cache-last-call-80x24-pre-exit.txt)。

按用户授权更新真实配置的三项缓存显示设置，走项目 Load/Save 原子保存，确认其他设置相同、0600、重复读取字节相同；临时入口已删除。原目录安装版本 v0.1.0 / e118207-dirty / 2026-10-09T00:57:34Z，24,756,658 bytes。只有缓存显示设置变更，没有供应商调用、新 Go 依赖或新用户命令。外部口径依据与切换方法见 [技术契约 §4](../specs/tui/statusline.md#4-缓存与累计用量)。此前构建/验收数字保留为历史。

安装后两种口径及真彩色宽屏三场景各 1/1 通过：见 [累计模式](evidence/G1b.4/cache-ratio-2026-10-09/installed/cc/results.json)、[最近模式](evidence/G1b.4/cache-ratio-2026-10-09/installed/pi/results.json)、[真彩色](evidence/G1b.4/cache-ratio-2026-10-09/installed/truecolor/results.json)。

## 2026-10-09 上下文格式与缓存初始零值

用户要求去掉 ctx(last) 的括号后缀，显示 `ctx: x.x% x/1M`，缓存一开始使用 0。默认新增并落盘 context_format:usage，固定一位小数及输入量/容量，数量沿用 compact/full；例如 `ctx: 12.3% 123.5k/1M`。context_format:bar 保留进度条，两种格式都去掉 `(last)`，共用警告/临界颜色。缓存两种 scope 的新会话都是 `cache: 0%`，tokens/both 同样初始化为零；Ctrl+N 恢复，初始零不伪装成供应商已返回的 usage。请求后缺失统计依然明确为 unknown/partial。

定向上下文/初始缓存回归先报 11 failed，默认持久化/非法格式校验先报 4 failed；实现后全仓竞态 685 项、14 包通过，vet/gofmt/diff 检查通过。12 个隔离 PTY 场景全部通过，新增 1M 容量场景确认首帧 `ctx: 0.0% 0/1M`、`cache: 0%`，回复后 `ctx: 0.0% 20/1M`；真彩色逐字符检查初始 0% 与实际值着色。证据见 [12 场景结果](evidence/G1b.4/context-display-2026-10-09/pty/results.json)、[1M 首帧](evidence/G1b.4/context-display-2026-10-09/pty/context-1m-80x24-startup.txt)、[1M 回复后](evidence/G1b.4/context-display-2026-10-09/pty/context-1m-80x24-pre-exit.txt)。

真实配置通过安装版 config show 的 Load 原子补齐 context_format:usage，仅新增这个默认键，其他 JSON 相同、0600、重复读取字节相同。原目录重新安装 /Users/go/bin/plume：v0.1.0 / e118207-dirty / 2026-10-09T01:14:44Z，24,756,690 bytes。没有新依赖、包目录或用户命令；本单元仍待审核，未推进 G3、提交或推送。

安装版 1M 上下文、真彩色宽屏及 Pi 最近调用场景各 1/1 通过：见 [1M](evidence/G1b.4/context-display-2026-10-09/installed/context-1m/results.json)、[颜色](evidence/G1b.4/context-display-2026-10-09/installed/truecolor/results.json)、[Pi](evidence/G1b.4/context-display-2026-10-09/installed/pi/results.json)。

## 2026-10-09 Provider 标签与缓存命中率条

默认首项标签为 Provider，显示 `Provider: deepseek`。cache_format 默认 bar，新增完整落盘的 cache_bar:{width:10,style:unicode}，例如 `cache [████░░░░░░]40.0% 8/20`，不加冒号，固定一位小数。用户在分母澄清中明确沿用 CC/Pi 命中率，末尾为命中输入/对应总输入，不能用模型 1M 容量作分母；新会话为 `0.0% 0/0`。session/last_call 和 tokens/ratio/both 仍可选。缓存条宽独立于上下文条，窄屏先缩条再隐藏字段，命中率不使用上下文的高占用告警色。

定向默认配置/校验先报 6 failed，标签/条体先报 5 failed；实现后全仓竞态 694 项、14 包通过，vet/gofmt/diff 检查通过。12 个隔离 PTY 场景全部通过，实际字符颜色确认 Provider/cache 标签与条体/百分比，两次调用累计 `70.0% 56/80`、最近 `80.0% 48/60`。见 [结果](evidence/G1b.4/cache-bar-2026-10-09/pty/results.json)、[真彩色帧](evidence/G1b.4/cache-bar-2026-10-09/pty/truecolor-wide-220x24-pre-exit.txt)。

真实配置 Load 原子补齐 cache_bar，再复用 Save 设置 cache_format:bar 和 provider label:Provider；其他 JSON 相同、0600、重复读取字节相同。临时入口已删除。默认状态栏为 15 个顶层键、2 个 cache_bar 键、14 个 items，provider 位于首项。原目录重新安装 /Users/go/bin/plume：v0.1.0 / e118207-dirty / 2026-10-09T01:28:43Z，24,773,298 bytes。未新增依赖、命令或包目录，不推进 G3、提交或推送。

完成本单元后停止，等待用户通过 G1b.4，再另行授权推进 G3。

## 2026-10-09 生成期间 ctx/cache 提前 unknown 修复

用户确认生成期间保留初始零或上一轮统计，收到本次 usage 再刷新。根因有两处：startRun 清掉了初始 contextTokens；cache 使用实时调用数，首次调用计入但 usage 尚未报告时提前退出零值分支，后续累计模式也会提前加 partial。修复后 context 不在请求开始清空，cache 保留独立的请求前展示快照；输入与缓存分别在对应字段报告时更新，实际调用和用量统计继续消费真实事件。新调用终态仍缺输入统计时清除旧 ctx，缓存按实际数据显示 unknown/partial；准备阶段取消且没有调用时保留原值，Ctrl+N 恢复零值。

定向回归先报 23 failed，修复后 23 passed；旧安装版真实终端同样复现，生成中显示 `ctx: usage unknown │ cache unknown`，见 [修复前帧](evidence/G1b.4/usage-retain-2026-10-09/before/context-1m-80x24-call-1-before-usage.txt)。全仓竞态 716 项、14 包通过，vet/gofmt/diff 检查通过。

PTY fixture 在正文已经发送后阻塞 usage，先检查生成帧再放行统计；12 个场景通过，包含取消、断流、未知字段、颜色、窄屏及 CC/Pi 两次调用。两种口径第一轮生成均为 0，第二轮生成均保留 40.0% 8/20，统计到达后分别更新为 70.0% 56/80 与 80.0% 48/60。见 [完整结果](evidence/G1b.4/usage-retain-2026-10-09/pty/results.json)、[1M 生成帧](evidence/G1b.4/usage-retain-2026-10-09/pty/context-1m-80x24-call-1-before-usage.txt)。

已从原目录重新安装 `/Users/go/bin/plume`：built 2026-10-09T01:42:21Z，24,773,378 bytes。安装版 [CC](evidence/G1b.4/usage-retain-2026-10-09/installed/cc/cache-session-80x24-result.json)、[Pi](evidence/G1b.4/usage-retain-2026-10-09/installed/pi/cache-last-call-80x24-result.json)、[1M](evidence/G1b.4/usage-retain-2026-10-09/installed/context-1m/context-1m-80x24-result.json) 三个场景各 1/1 通过。配置结构及真实配置不变，验收均为临时 PLUME_HOME 和本地 fixture；审核状态保持待审核。

## 2026-10-09 按用户要求默认隐藏 ctx

用户要求状态栏删除 ctx、保留 cache。默认配置的 context 项改为 enabled:false，真实配置同步关闭该项，其余配置字段语义不变；用户可显式设 true 恢复。缓存格式、CC/Pi 口径、用量采集和布局算法沿用原行为。现行默认示例见[状态栏契约](../specs/tui/statusline.md)。

config/TUI/CLI 竞态 343 项通过，定向 vet、gofmt、diff 检查通过；原目录重新安装 /Users/go/bin/plume，built 2026-10-09T12:58:05Z。安装版状态栏 PTY 12/12 通过，覆盖默认隐藏、缓存配色、显式恢复 ctx、1M/窄屏、取消/断流和两种缓存口径。隔离报告 /tmp/plume-ctx-hidden-pty.u6QPxl/results.json，仅本地 fixture，无付费请求。复现运行 scripts/pty_demo_g1b4.py --binary /Users/go/bin/plume --output <临时目录>；按证据治理规则不把本次临时帧纳入已通过单元的仓库证据。既有审核状态保持 passed，本次未提交或推送。
