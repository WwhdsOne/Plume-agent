---
title: G1b.4 验证摘要
status: log
updated: 2026-10-09
summary: 最终状态栏代码的竞态、静态、PTY、构建安装与真实配置默认补齐证据
---

# G1b.4 验证摘要

执行目录 `/Users/Learning/AI/AGENT/Plume-agent`，HEAD `e118207`，本单元仍未提交。以下为 2026-10-08 初次交付输出；10-09 启动修复复验见文末，不代表外部模型性能。

| 命令/检查 | 实际输出或结果 |
| --- | --- |
| `rtk go test -race ./...` | `Go test: 637 passed in 14 packages`，exit 0 |
| `rtk go vet ./...` | exit 0，无诊断 |
| `rtk gofmt -l .` | exit 0，无文件 |
| `rtk git diff --check` | exit 0，无错误 |
| `rtk ./scripts/build.sh` | v0.1.0 / e118207-dirty / 2026-10-08T14:59:43Z |
| 9 个隔离 PTY 用例 | `G1B4_PTY_OK cases=9`，exit 0，见 [results.json](pty/results.json) |
| `rtk ./scripts/build.sh --install` | 安装 `/Users/go/bin/plume`；built 2026-10-08T15:00:55Z，Go 1.27.1 darwin/arm64 |
| 已安装二进制 PTY | `G1B4_PTY_OK cases=1`，exit 0，见 [结果](installed/truecolor-wide-220x24-result.json) |
| 二进制体积 | 本地及已安装均为 24,739,714 bytes（发布式 -s -w） |

配置补齐通过已安装程序的 `config show` 触发，输出被丢弃，只检查字段集合与权限：`/Users/wwhds/.plume/config.json` 包含 status_line 的 12 个顶层键、14 个 items、context_bar 的 5 个键，1/1 已配置官方 DeepSeek 模型的 context_window_tokens 为 1000000，权限 0600。重复读取前后字节相同。未输出凭据、模型请求正文或环境变量全集。

关键修复均先观察失败，再运行同一回归通过：已知缓存零没有比率、同 priority 后项隐藏、关闭状态栏且冻结阅读的关键提示/失败、未知 schema 原文件保护、准备阶段取消与真正新调用的 last cache 区分；app 另覆盖调用去重、取消前未调用不计数、cache 指针值隔离。追加回归覆盖官方容量 1M/null 自动迁移、保留用户容量、初始 0%、未知原因和单行优先；Thought 临界处输入增高导致帧超高的回归先失败，修复后布局、光标和连续刷新通过。

规格审查、质量审查最终均通过；环境/统计的竞态定向测试 8 项、首轮质量定向回归 11 项通过；反馈修订后的质量复核包含 6 项布局/光标/刷新回归和 31 项容量用例，阈值颜色叠加检查也通过。主验收帧在退出 alt-screen 前捕获，断言精确宽高、两条分隔线、真实输入光标 Y、字段口径和 trace 脱敏。220×24 真实字符缓冲确认自动一行，标签 5bc8c8、数值 7dd3d8，未知为 ANSI 93 亮黄，见 [颜色证据](pty/truecolor-wide-220x24-pre-exit-colors.json)。取消与断连为预期失败 trace，不当作成功回答提交历史。

路线图浏览器读取正常：当前焦点 G1b.4 可配置底部状态栏，待审核 1，G3 未开始。临时 HTTP 服务器与标签已关闭。

## 2026-10-09 开屏修复复验

- RED：`rtk go test ./internal/tui -run TestSplashWaitsForValidWindowSize -count=1` 报 7 failed（父测试与六个消息顺序子测试）；真实安装版的增强 PTY 检查报 startup feather missing。旧版和今早升级版的启动帧均没有盲文，排除将问题仅归因于依赖升级。
- GREEN：在 resize 入口保留尚无有效窗口尺寸的 splash；相同定向命令 7 passed。`rtk go test -race ./...` 为 644 passed in 14 packages，exit 0；vet、gofmt、diff 检查均通过。
- `scripts/pty_demo_g1b4.py --output docs/reviews/evidence/G1b.4/startup-fix-2026-10-09` 为 G1B4_PTY_OK cases=9，exit 0；每个宽屏用例新增启动羽毛和圆角框断言。见 [结果](startup-fix-2026-10-09/results.json)。
- `scripts/build.sh --install` 安装 /Users/go/bin/plume，构建时间 2026-10-09T00:30:19Z；本地及安装版均 24,756,562 bytes。安装版真彩色 220×24 PTY 为 cases=1，exit 0，见 [结果](startup-fix-2026-10-09/installed/truecolor-wide-220x24-result.json)。启动帧可见 192 个盲文字符及完整圆角下边，两条输入分隔线和硬件光标通过；状态栏标签、数值颜色仍通过实际字符缓冲校验。
- 完整开屏使用 `scripts/preview_splash.py --cols 100 --rows 34`，真彩色与 256 色各一份。所有检查通过：版本、离线标识、Tips、盲文、闭合/对齐框、主题色与干净退出；PNG 已人工查看。见 [真彩色](startup-fix-2026-10-09/preview/splash-braille-100x34-truecolor.json)、[256 色](startup-fix-2026-10-09/preview/splash-braille-100x34-256color.json)。预览检查原先仅匹配小写版本名，已兼容宽端既有 Plume-agent 标签大小写，未改产品内容。
- 上述验收全部使用临时 PLUME_HOME 和本地 fixture/离线模式，不调用真实模型或修改真实配置；G1b.4 审核状态不变，未提交或推送。

## 2026-10-09 立体标题验收

- 用户参考图要求转为六行实心笔画与双线轮廓：94 列完整字形与 71 列紧凑字形，主体/阴影分别引用主青和深青 token，沿用左对齐。没有图片运行时加载、新依赖或配置。
- RED：字形/宽度测试因旧标题为五行失败；GREEN：`rtk go test ./internal/tui -run 'Test(SplashRendersBoxedSplash|WordmarkShadow)' -count=1` 为 7 passed。`rtk go test -race ./internal/tui` 为 94 passed；全仓竞态为 650 passed in 14 packages。vet、gofmt、diff 检查干净。
- 三份完整开屏预览：100×34 真彩色/256 色、80×34 真彩色，版本、离线标识、Tips、盲文、闭合/对齐框、主题色及干净退出全部 true。完整/紧凑 PNG 均已查看，见 [预览目录](shadow-wordmark-2026-10-09/preview)。
- 增强 PTY 为 G1B4_PTY_OK cases=9，exit 0，见 [结果](shadow-wordmark-2026-10-09/pty/results.json)；输入光标、上下分隔线、上下文、缓存、取消与断连行为均保持。
- 安装构建时间 2026-10-09T00:41:42Z，本地与 /Users/go/bin/plume 均 24,756,610 bytes；已安装真彩色宽屏为 cases=1，exit 0，见 [结果](shadow-wordmark-2026-10-09/installed/truecolor-wide-220x24-result.json)。全部使用临时 PLUME_HOME 与离线/本地 fixture，没有修改真实配置或调用真实供应商。

## 2026-10-09 缓存占比验收

- RED：默认落盘、缺省/显式口径保留、非法口径校验的定向命令为 8 failed；缓存两种口径、加权分母与部分数据回归为 5 failed。GREEN：默认 ratio/session/cache label，新增累计缓存与配对输入快照后通过。
- 最终 `rtk go test -race ./...` 为 674 passed in 14 packages，exit 0；`rtk go vet ./...`、`rtk gofmt -l .`、`rtk git diff --check` 全部通过。覆盖已知零、字段缺失、零分母、token/both 格式、部分累计、清屏/新会话、去重/替换、非法子集及溢出。
- `scripts/pty_demo_g1b4.py --output docs/reviews/evidence/G1b.4/cache-ratio-2026-10-09/pty` 为 G1B4_PTY_OK cases=11，exit 0，见 [结果](cache-ratio-2026-10-09/pty/results.json)。两次请求首次 8/20，第二次 48/60，累计 70% 与最近 80% 分别实际显示；首帧羽毛/边框、输入光标、颜色及原九场景继续通过。
- 真实配置通过临时 Go 入口复用 Load/Save，只修改 cache_format=ratio、cache_scope=session、cache label=cache。允许字段外 JSON 与变更前相同，0600、重复 Load 字节相同；临时文件已删除，未输出凭据。状态栏默认配置现有 13 个顶层键、5 个 context_bar 键、14 个 items。
- `rtk ./scripts/build.sh --install` 安装 /Users/go/bin/plume，built 2026-10-09T00:57:34Z，24,756,658 bytes；不提交或推送、不推进 G3，所有交互验收仍是隔离 PLUME_HOME 与本地 HTTP fixture。
- 已安装程序累计、最近调用、真彩色宽屏三个场景各 1/1 通过，均 exit 0：见 [CC](cache-ratio-2026-10-09/installed/cc/results.json)、[Pi](cache-ratio-2026-10-09/installed/pi/results.json)、[颜色](cache-ratio-2026-10-09/installed/truecolor/results.json)。

## 2026-10-09 上下文格式与初始零值验收

- RED：上下文格式/标签及两种缓存口径初始零值的定向回归为 11 failed；默认持久化及 context_format 枚举/类型校验为 4 failed。GREEN：固定一位小数的 `ctx: percent used/capacity`、无 `(last)`、初始缓存 0% 后通过；可选 bar 及真实缺失统计语义同时覆盖。
- `rtk go test -race ./...` 为 685 passed in 14 packages，exit 0；vet/gofmt/diff 检查通过。`scripts/pty_demo_g1b4.py --output docs/reviews/evidence/G1b.4/context-display-2026-10-09/pty` 为 G1B4_PTY_OK cases=12，exit 0，见 [结果](context-display-2026-10-09/pty/results.json)。包含 1M 首帧、初始缓存零、实际回复、超限、窄屏、两种缓存口径、自定义条体及逐字符颜色检查。
- 已安装程序 config show 的 Load 原子添加 context_format:usage。其他 JSON 与变更前相同，0600、第二次读取字节相同；默认状态栏现为 14 个顶层键、5 个 context_bar 键、14 个 items，不输出凭据。
- 重新安装 /Users/go/bin/plume：built 2026-10-09T01:14:44Z，24,756,690 bytes；本轮交互验收仍为临时 PLUME_HOME、本地 fixture，没有真实供应商调用。
- 安装版 1M 上下文、真彩色、Pi 最近调用三个场景各 1/1 通过，均 exit 0：见 [1M](context-display-2026-10-09/installed/context-1m/results.json)、[真彩色](context-display-2026-10-09/installed/truecolor/results.json)、[Pi](context-display-2026-10-09/installed/pi/results.json)。

## 2026-10-09 Provider 与缓存条验收

- 用户明确缓存比率与计数采用 CC/Pi 的对应输入分母。Provider 为默认首项标签，cache_format:bar、cache_bar:{width:10,style:unicode}，初始 `cache [░░░░░░░░░░]0.0% 0/0`；不写成模型容量分母。
- RED：默认配置/校验为 6 failed、Provider/条体定向为 5 failed。GREEN：全仓竞态 694 passed in 14 packages，exit 0；vet/gofmt/diff 通过。覆盖两 scope、初始零、配对子集/partial、ASCII、独立条宽、窄屏先缩条及原格式保留。
- `scripts/pty_demo_g1b4.py --output docs/reviews/evidence/G1b.4/cache-bar-2026-10-09/pty` 为 G1B4_PTY_OK cases=12，exit 0；见 [结果](cache-bar-2026-10-09/pty/results.json)。实际累计条 70.0% 56/80 与最近条 80.0% 48/60，标签/条体配色通过真实字符缓冲断言。
- 真实配置经 Load/Save 原子补齐及调整，只变更 cache_format、provider label 和缺失的 cache_bar 默认键，其他 JSON 保留、0600、重复读取字节相同。15 个状态栏顶层键、2 个 cache_bar 键、14 个 items，Provider 首项；临时入口已删除。
- 安装 /Users/go/bin/plume：built 2026-10-09T01:28:43Z，24,773,298 bytes；无真实供应商调用、新依赖、新命令或新包。

## 2026-10-09 生成期间保留统计快照验收

- RED：`rtk go test ./internal/tui -run 'Test(StatusUsage|ContextUnknownExplainsMissingSource)'` 为 23 failed，exit 1；覆盖首次零值、后续快照、输入先于缓存、无统计终态和准备阶段取消。旧安装版运行增强 PTY 的 context-1m-80x24 用例 exit 1，实际生成帧为 `ctx: usage unknown │ cache unknown`，见 [修复前帧](usage-retain-2026-10-09/before/context-1m-80x24-call-1-before-usage.txt)。
- GREEN：相同定向命令 23 passed；`rtk go test -race ./...` 为 716 passed in 14 packages，exit 0。vet/gofmt/diff 均通过。展示初始零不计为实际 usage；缓存 scope/format 与 unknown/partial 的真实统计语义保留。
- `scripts/pty_demo_g1b4.py --output docs/reviews/evidence/G1b.4/usage-retain-2026-10-09/pty` 为 G1B4_PTY_OK cases=12，exit 0，见 [结果](usage-retain-2026-10-09/pty/results.json)。增强 fixture 在正文到达后阻塞 usage，终端实际显示初始零/上一轮值，随后才放行统计，避免只检查完成帧漏掉生成阶段。
- 第一轮生成 `ctx: 0.0% 0/100`、`cache [░░░░░░░░░░]0.0% 0/0`；第二轮生成保留 `ctx: 20.0% 20/100`、`cache [████░░░░░░]40.0% 8/20`，没有提前 unknown/partial。收到统计后 CC 为 70.0% 56/80，Pi 为 80.0% 48/60。1M 场景初始与生成均为 0/1M，统计后为 20/1M。
- 原目录 `rtk ./scripts/build.sh --install` 安装 /Users/go/bin/plume，v0.1.0 / e118207-dirty / 2026-10-09T01:42:21Z，24,773,378 bytes。安装版 [CC](usage-retain-2026-10-09/installed/cc/cache-session-80x24-result.json)、[Pi](usage-retain-2026-10-09/installed/pi/cache-last-call-80x24-result.json)、[1M](usage-retain-2026-10-09/installed/context-1m/context-1m-80x24-result.json) 各 1/1 通过；真实配置未改动，无真实供应商调用。
