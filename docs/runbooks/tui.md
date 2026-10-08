---
title: TUI 聊天 runbook
status: active
updated: 2026-10-08
summary: plume chat 的启动、流式思考配置、键位、隔离演示与 trace 排查
---

# TUI 聊天 runbook

G1b.2.1 起使用 v2 终端栈；G1b.3 流式与思考展示已通过审核。

## 启动方式

```bash
plume                        # 配置就绪 + TTY：直接进入聊天（默认模型）
plume chat                   # 显式入口，同上
plume chat --model deepseek-default   # 指定 config.json 里的模型配置 ID
plume chat --offline         # 脚本化 fake：无配置/无 Key/不联网
plume setup                  # 首次设置；渠道步骤可选「暂不接入渠道」
```

隔离试验（不污染真实配置）：

```bash
PLUME_HOME=$(mktemp -d) go run ./cmd/plume setup
PLUME_HOME=$(mktemp -d) go run ./cmd/plume chat --offline
```

## 开屏（G1b.2.2）

启动后聊天记录区顶部先渲染 `PLUME-AGENT` **像素块字**（主色，5 行，按可见列宽居中），下方是**四边完整**的圆角方框（雾青主色描边，宽上限 88 列）：左栏 **64×80 点位的盲文羽毛**（每字符 2×4 点位，显示为 32 列×20 行，右上深青→左下浅青三色渐变，保留羽轴留白与碎羽），与右栏之间主色分隔竖线，右栏版本行、9 行对齐的 `Tips for getting started` 键位提示、模型标签与工作目录。开屏总高 27 行，随记录滚动，只出现一次（Ctrl+N 后不复活）；终端 32–79 列降级为羽毛竖排（无大标题无方框），32 列以下显示小标题与折行文本。长版本、模型和中文路径按终端列宽裁剪。契约见 `docs/tui-splash.md`，色值集中在 `internal/tui/theme.go`。

## 键位（契约 docs/tui-keys.md §3）

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

可选 `tui.status_messages` 使用 preparing/waiting/thinking/responding 四个键，每个值为字符串数组。空值回退默认文案，thinking 默认显示 `Thought`。配置启动时读取一次，示例见 [流式契约](../tui-streaming.md)。

在线聊天事件写入 `$PLUME_HOME/logs/chat-UTC-pid.jsonl`（默认 `~/.plume/logs/`），关联 run/model ID，记录阶段、字节数、首思考、模型首答案和 UI 首答案；缺失值为 null，不记录正文或凭据。离线启动不读取配置。

带版本构建后可复跑本地 HTTP fixture + pty 演示：

```bash
./scripts/build.sh
python3 scripts/pty_demo_g1b3.py
# 期望：G1B3_PTY_OK cases=13
```

脚本仅使用临时 PLUME_HOME、临时凭据与本地服务器，覆盖三个尺寸、三种颜色模式及纯答案、仅思考、断流、取消。证据位于 `docs/reviews/evidence/G1b.3/pty/`，本地 fixture 不能代表真实模型延迟。

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

## 常见问题复现

- **`chat needs an interactive terminal`**：stdout 不是 TTY（管道/CI）。这是刻意行为；纯管道场景请看 `internal/tui` 的 Update 测试。
- **`plume needs an interactive terminal`**：裸 `plume` 在非 TTY 下调用，同样明确退出。
- **`config has no default_model`**：配置存在但没选默认模型，重跑 `plume setup`。
- **`model config "x" not found`**：`--model` 只接受 config.json 里已保存的配置 ID，不是 API 模型名。
- **怀疑配置问题**：`plume config show`（脱敏），或用 `PLUME_HOME=$(mktemp -d)` 隔离复现。
- **trace**：`~/.plume/logs/setup.jsonl`（setup 过程）。聊天 run 事件当前经内存事件驱动 UI，模型 trace 落盘扩展随后续单元交付。
