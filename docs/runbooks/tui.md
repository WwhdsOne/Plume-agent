---
title: TUI 聊天 runbook
status: active
updated: 2026-10-10
summary: plume chat 的启动、流式思考和状态栏配置、键位、隔离演示与 trace 排查
---

# TUI 聊天 runbook

G1b.2.1 起使用 v2 终端栈；G1b.3 流式与思考展示已通过审核；G1b.4 可配置状态栏已于 2026-10-09 通过。

## 启动方式

```bash
plume                        # 交互终端无配置自动 setup；配置就绪后进入聊天（默认模型）
plume chat                   # 显式入口，同上
plume chat --model deepseek-default   # 指定 config.json 里的模型配置 ID
plume chat --offline         # 脚本化 fake：无配置/无 Key/不联网
plume setup                  # 首次设置；渠道步骤可选「暂不接入渠道」
```

首次无配置时，裸 `plume` 仅在 stdin/stdout 都是 TTY 时自动进入 setup；设置正常结束后返回终端，并提示 `plume`、`plume setup`、`plume config show`。再次运行 `plume` 开始聊天，`plume setup` 可随时重新设置；无配置的非 TTY 调用只显示帮助与提示。

隔离试验（不污染真实配置）：

```bash
PLUME_HOME=$(mktemp -d) go run ./cmd/plume setup
PLUME_HOME=$(mktemp -d) go run ./cmd/plume chat --offline
```

## 开屏（G1b.2.2）

启动后聊天记录区顶部先渲染 `PLUME-AGENT` **立体像素字**（6 行实心笔画与双线阴影，主青主体、深青轮廓，左对齐；94 列以上显示完整字形，80–93 列显示同风格紧凑字形），下方是**四边完整**的圆角方框（雾青主色描边，宽上限 88 列）：左栏 **64×80 点位的盲文羽毛**（每字符 2×4 点位，显示为 32 列×20 行，右上深青→左下浅青三色渐变，保留羽轴留白与碎羽），与右栏之间主色分隔竖线，右栏版本行、9 行对齐的 `Tips for getting started` 键位提示、模型标签与工作目录。开屏总高 28 行，随记录滚动，只出现一次（Ctrl+N 后不复活）；终端 32–79 列降级为羽毛竖排（无大标题无方框），32 列以下显示小标题与折行文本。长版本、模型和中文路径按终端列宽裁剪。契约见 `docs/specs/tui/splash.md`，色值集中在 `internal/tui/theme.go`。

## 键位（契约 docs/specs/tui/keys.md §3）

| 键 | 行为 |
| --- | --- |
| `Enter` | 发送输入（空输入不产生请求） |
| `Shift+Enter` | 换行（kitty 协议终端：iTerm2 3.5+、WezTerm、Ghostty、kitty、Alacritty） |
| `Option/Alt+Enter` | 换行（终端把 Option 设为 Meta 时） |
| `\` + `Enter` | 换行兜底：行尾 `\` 被去掉并换行，全部终端可用 |
| `Up` / `Down` | 边缘导航：光标在输入第一行时 Up 翻草稿历史、最后一行时 Down 翻回；多行输入中间则先移动光标（回到最新恢复编辑中草稿） |
| `PgUp` / `PgDn` | 滚动聊天记录 |
| `Esc` | 取消当前 run（取消的轮次不进入历史） |
| `Ctrl+C` | 三段：运行中取消；空闲有草稿清空草稿；空草稿 1s 内两次退出 |
| `Ctrl+D` | 退出（仅空输入时；有草稿时忽略） |
| `Ctrl+L` | 清屏并回到聊天记录底部 |
| `Ctrl+N` | 新会话（清空上下文与草稿历史；运行中会先被拒绝并提示） |
| `Ctrl+O` | 展开/收起当前轮或空闲时最近一轮的思考；无内容时忽略 |
| 鼠标滚轮 | 滚动聊天记录（与终端文本选择冲突时按住 Shift 选择） |

输入区高度随换行/折行自动长高，一次最多可见 4 行，超出在输入框内滚动。

同一会话串行：运行中再次 Enter 被拒绝并提示，不排队、不并发。

## 流式输出与配置（G1b.3，已通过）

用户以 `>`、助手以 `●` 显示，`Thought` 标题与二者同列，三点动画占标题左侧固定槽。思考默认预览最新三行，首答案自动折叠；Ctrl+O 手动选择优先。答案增量按 50ms 合并渲染，首答案和终态立即刷新；主动上滚暂停跟随，手动回到底部恢复。失败或取消保留屏幕上的部分内容，下一轮上下文只包含成功轮次。

模型配置可添加 `"reasoning_effort": "none"` 关闭思考，省略字段默认偏好 `high`；setup 不询问强度。仅已验证的官方 DeepSeek 端点和模型映射参数，未验证服务缺省时省略参数，显式设置则启动报错。`plume config show` 显示请求值、生效值、来源和能力状态；不把 unknown 显示成已生效的 high。

可选 `tui.status_messages` 使用 preparing/waiting/thinking/responding 四个键，每个值为字符串数组。空值回退默认文案，thinking 默认显示 `Thought`。配置启动时读取一次，示例见 [流式契约](../specs/tui/streaming.md)。

在线聊天事件写入 `$PLUME_HOME/logs/chat-UTC-pid.jsonl`（默认 `~/.plume/logs/`），关联 run/model ID，记录阶段、字节数、首思考、模型首答案和 UI 首答案；缺失值为 null，不记录正文或凭据。离线启动不读取配置。

带版本构建后可复跑本地 HTTP fixture + pty 演示：

```bash
./scripts/build.sh
python3 scripts/pty_demo_g1b3.py
# 期望：G1B3_PTY_OK cases=13
```

脚本仅使用临时 PLUME_HOME、临时凭据与本地服务器，覆盖三个尺寸、三种颜色模式及纯答案、仅思考、断流、取消。证据位于 `docs/reviews/evidence/G1b.3/pty/`，本地 fixture 不能代表真实模型延迟。

## 底部状态栏与配置（G1b.4，已通过）

默认优先一行，完整字段放不下才分两行；亮青标签与浅青数值显示模型/思考/上下文/缓存和 Git/uv/会话/run 计时，ctx 默认启用；小于 60 列或 18 行合并为一行，按配置优先级隐藏字段。`plume config show` 可查看完整生效配置，`plume config path` 给出配置文件路径。首次读取旧文件会原子补齐默认值，保留现有自定义；setup 不新增问题。修改后重启聊天生效。

在现有 `tui.status_line` 下可修改 `items`（数组顺序就是显示顺序），例如只显示缓存、模型、会话时长：

```json
{
  "max_rows": 1,
  "items": [
    {"id": "cache", "label": "cache", "enabled": true, "row": 1, "priority": 100},
    {"id": "model", "label": "", "enabled": true, "row": 1, "priority": 90},
    {"id": "session_elapsed", "label": "session", "enabled": true, "row": 1, "priority": 60}
  ]
}
```

这是 `status_line` 内的片段，保留其余默认项；它不用于替换整个 config.json。`enabled:false` 或 `items:[]` 隐藏整栏并停止相应环境采集。字段顺序、行位置、标签、优先级、数字/时间格式、上下文条宽/ASCII 样式/阈值都可配置，完整字段见 [技术契约](../specs/tui/statusline.md)。关闭状态栏时关键拒绝与错误会作为系统/错误记录滚动到可见位置。

`context` 项默认 `enabled:true`、context_format:bar、context_bar.show_percent:false，显示 `ctx: [██░░░░░░░░] 200k/1M`。条体后固定带当前输入/总容量，token_format 控制 k/M 或完整数量；show_percent:true 可额外显示百分比，context_format:usage 保留百分比加数量格式。官方 DeepSeek 两预设默认容量1M，新会话显示 `ctx: [░░░░░░░░░░] 0/1M`；容量未知/终态缺统计仍明确 unknown，生成期间保留请求前快照，不估算 token，不显示 `(last)`。

最左默认 `Provider: deepseek`，标签可自定义。缓存默认 cache_format:ratio、cache_scope:session，仅显示 `cache: 40%`；初始与 Ctrl+N 后为 `cache: 0%`。默认 CC 会话累计，last_call 为 Pi 最近调用，分母仍为对应总输入。tokens/both/bar 保留可选，partial/unknown 与等待期间快照保留逻辑不变；缓存默认不显示条体或命中量/总输入数字。

隔离验收（需要 Python 的 `pyte`，不加入 Go 运行依赖）：

```bash
./scripts/build.sh
python3 scripts/pty_demo_g1b4.py --output docs/reviews/evidence/G1b.4/pty
```

脚本只使用临时 PLUME_HOME、临时 Git/uv 目录、测试凭据与本地 HTTP fixture。真实容量和真实供应商性能仍需用户核对；`chat --offline` 不加载配置，继续使用默认布局。

## 受控工具循环（G3/G3.1，已通过）

在线默认声明 `read/grep/glob/edit/write/bash`，许可列表来自 tools.enabled；计算器/时间可以显式启用。TUI 亮青 `◇` 行原位显示状态与数量/退出码，正文回传模型。根目录默认启动目录，文件修改要求先读且摘要未变；bash 本地执行，不是沙箱。完整语义见 [工具契约](../specs/agent/tools.md)。

离线输入以下指令可演示同一真实工具循环：

```text
/demo calculate    # 6 × 7 = 42
/demo time         # 当前 UTC 时间
/demo error        # 除零失败 → 模型脚本纠正 → 0.5
/demo budget       # 默认不限次数，可完成 12 工具/13 模型步骤
/demo workspace    # 新建 plume-demo.txt → glob/grep/read/edit → bash 校验
/demo cancel       # 工具结束后延迟回答，按 Esc 取消
```

脚本在真实工作目录执行；workspace 演示应从临时目录启动，已有 plume-demo.txt 拒绝覆盖。普通离线输入为固定回复，offline 不读写配置。agent.budget 默认次数与整体时长0（无限），模型1800s/工具180s；默认配置和修改方式见 [配置契约](../specs/config.md)。不自动重试，成功整组提交，失败/取消不提交历史；已发生文件/命令副作用保留。ctx/cache 续调新统计到达前保留上一显示值。

隔离验收：

```bash
rtk ./scripts/build.sh --install
rtk go test ./internal/agent ./internal/tools ./internal/app ./internal/eval
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-tui-regression-deps python3 scripts/pty_demo_g3.py --binary /Users/go/bin/plume
# 期望：G3_PTY_OK cases=8；pyte 依赖目录可按本机安装位置替换
```

trace 增加 prompt_built 与 tool_started/tool_validated/tool_completed/tool_failed，并关联 parent_model_call_id；不落盘 prompt、思考、命令、工具参数或结果。用户于 2026-10-09 审核通过 G3/G3.1，默认隐藏 ctx 的追加状态栏修改一并通过。原 `docs/reviews/evidence/G3/` 与 `docs/reviews/evidence/G3.1/` 已依下述规则删除；这些产物未提交，没有 Git 历史副本，可通过 `scripts/pty_demo_g3.py` 和 `internal/app/workspace_tools_test.go` 重跑当前实现。

## pty 演示脚本

`--offline` 的完整演示需要在 pty 中跑（管道直连会被 TTY 判定拒绝，这是刻意行为）。
G1b.2.1 起用仓库内脚本（覆盖 v1 判据复跑 + 新键位）：

```bash
PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b221.py
# 期望输出：TUI_DEMO_OK returncode = 0
```

脚本流程：普通发送 → Alt+Enter 换行提交 → `\`+Enter 续行提交 → Esc 空闲无副作用 → 单击 Ctrl+C 不退出 → 双击 Ctrl+C 干净退出。

判据：输出包含各次输入回显、offline 固定回答，进程干净退出。kitty 协议的
Shift+Enter（CSI u `\x1b[13;2u`）已在 G1b.2.1 证据中单独验证；真实终端里按
iTerm2/WezTerm 手测（Terminal.app 不支持 kitty，走层级 2/3）。

## 证据治理：通过即删

pty 与终端验收的产物（`.ansi`/`.txt`/`-frame-N.txt`/`.jsonl`）**只在单元待审期间**放在 `docs/reviews/evidence/<单元>/`，供用户对照审核记录核查；**单元通过的提交里一并删除**。非 TUI 部分用普通 Go 测试覆盖，不产出这类证据。

理由：这些文件是脚本在固定输入下的一次性录像，可随时重新生成；删掉不会丢失信息，而 912 个 / 5.4 MB 的累积会污染每次文档检索。审核记录正文已摘录关键数值，结论不依赖原始文件。

复现任意一个已删场景：

```bash
# 1) 重跑脚本（单元待审时按当前代码重新生成到 docs/reviews/evidence/<单元>/）
PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b3.py     # 流式/思考/Markdown
PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b4.py     # 状态栏
python3 scripts/preview_splash.py                            # 开屏帧
# 2) 或从历史提交取回当时的原始字节
git show 9860902:docs/reviews/evidence/G1b.4/pty/results.json
```

**只是做回归检查时**（当前单元不待审），把输出指到临时目录，避免在仓库里留下产物：

```bash
PLUME_HOME=$(mktemp -d) python3 scripts/pty_demo_g1b3.py --output /tmp/pty-check
```

`.gitattributes` 对 `docs/reviews/evidence/**` 的规则只在审核窗口内生效。

## 运行时 profile（pprof）

要看内存、协程或 CPU 的实际占用时，用显式 flag 打开本机调试端点。**默认关闭**，且**只接受回环地址**——profile 会暴露堆内容、协程栈和命令行参数，因此不接受网络暴露：

```bash
plume chat --pprof=127.0.0.1:6060            # 需要 / 不需要 Key 都可用
plume chat --offline --pprof=127.0.0.1:6060  # 纯离线排查
```

裸 `plume` 不带这个 flag（入口没有该开关），要抓 profile 就用 `plume chat --pprof=…`。启动时会在 TUI 接管屏幕前打印一行 `pprof listening on http://127.0.0.1:6060`；写 `:0` 时端口由内核分配，以该行为准。然后在另一个终端：

```bash
go tool pprof -top http://127.0.0.1:6060/debug/pprof/heap        # 内存占用排名
go tool pprof -top http://127.0.0.1:6060/debug/pprof/goroutine   # 协程堆积
go tool pprof -top http://127.0.0.1:6060/debug/pprof/profile     # CPU（默认采样 30s）
curl -s http://127.0.0.1:6060/debug/pprof/                        # 列出全部可用 profile
```

非回环地址（`0.0.0.0:6060`、`:6060`、内网 IP、域名）会启动失败并说明原因，不会降级监听。退出聊天即关闭端点。

这是**开发者按需抓取**的入口，不是常驻指标接口；日常盯内存用 `ps -o rss=` 或活动监视器即可（见本文件末尾的排查命令）。

## 常见问题复现

- **`chat needs an interactive terminal`**：stdout 不是 TTY（管道/CI）。这是刻意行为；纯管道场景请看 `internal/tui` 的 Update 测试。
- **`plume needs an interactive terminal`**：裸 `plume` 在非 TTY 下调用，同样明确退出。
- **`config has no default_model`**：配置存在但没选默认模型，重跑 `plume setup`。
- **`model config "x" not found`**：`--model` 只接受 config.json 里已保存的配置 ID，不是 API 模型名。
- **怀疑配置问题**：`plume config show`（脱敏），或用 `PLUME_HOME=$(mktemp -d)` 隔离复现。
- **trace**：`~/.plume/logs/setup.jsonl`（setup 过程）与 `~/.plume/logs/chat-<UTC时间戳>-<PID>.jsonl`（每次聊天一个文件，含 run 阶段与准备/首思考/首答案/完成耗时；不写正文与凭据）。聊天 trace 不会打屏，排查时直接读文件。
