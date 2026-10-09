---
title: TUI 可配置底部状态栏（G1b.4）
status: active
updated: 2026-10-09
summary: G1b.4 状态栏契约：默认 ctx 进度条与当前/总计，cache 仅显示命中百分比
---

# TUI 可配置底部状态栏（G1b.4）

> 2026-10-08 用户审核方案后授权实施。G1b.4 于 2026-10-09 审核通过，实施顺序位于 G1b.3 与 G3 之间；G3/G3.1 此后也已通过。实现证据与限制见 [G1b.4 审核记录](../../reviews/G1b.4.md)。
>
> 关联：[第一阶段计划](../../plans/phase-01-tui.md)、[流式展示契约](streaming.md)、[Agent 上下文规划](../../plans/agent-context.md)、[模型运行时决策](../../decisions/0003-model-runtime.md)。

## 1. 默认布局与范围

底部状态栏固定在输入区下方，提供模型、用量、项目环境和计时信息。采用最多两行的字段组合，不把所有信息塞进一个不可配置的长字符串。

以下仅为样式示例，数值不代表真实调用；按用户最新要求，恢复 ctx 进度条与当前/总计，cache 仅显示百分比：

```text
Provider: deepseek │ deepseek-flash │ think:high │ ctx: [██░░░░░░░░] 200k/1M │ cache: 80%
git:main* │ env:uv/.venv(active) │ session:12m30s │ responding 6.8s
```

优先合并成一行，完整字段放不下才按 row 分成两行。默认显示模型、上下文/缓存与 Git/uv/计时。标签亮青、数值浅青、未知提示黄；context 默认 enabled:true、context_format:bar，以进度条及当前/总计展示，show_percent 默认 false。缓存默认 ratio 仅百分比；其他格式、显隐、顺序、位置、窄屏优先级及条体样式仍由 config.json 控制。

本单元实现信息采集、结构化状态快照、配置落盘和自适应渲染。它不开始 G3 工具执行、不加载 soul/记忆/skill、不连接 MCP，也不添加任意自定义 Shell 状态脚本。

## 2. 字段与数据来源

| 字段 ID | 含义与来源 | 默认 |
| --- | --- | --- |
| `provider` | 所选模型配置的供应商品牌 ID；默认标签 Provider，不把自定义 Base URL 当成品牌名称 | 显示，第 1 行首项 |
| `model` | 实际发给 API 的模型名，不是配置项 ID | 显示，第 1 行 |
| `reasoning` | provider 能力解析得到的有效强度；未验证时显示 `unverified`，none 显示 `off`；映射过的 medium 可压缩显示为 `medium→high` | 显示，第 1 行 |
| `context` | 最近实际请求输入量/已知容量，默认进度条与当前/总计，见 §3 | 显示，第 1 行 |
| `cache` | 供应商报告的缓存命中输入占比；默认会话累计，可切换最近一次调用，见 §4 | 显示，第 1 行 |
| `git` | 启动工作目录所在仓库的分支；detached HEAD 显示短 commit；`*` 表示存在工作区修改 | 显示，第 2 行 |
| `uv_env` | 启动工作目录的 uv 项目标记与可确认的虚拟环境状态，见 §5 | 显示，第 2 行 |
| `session_elapsed` | 当前应用会话从创建到现在的墙钟时长，包含等待用户输入；Ctrl+N 新会话清零 | 显示，第 2 行 |
| `phase` | 实际 preparing/waiting/thinking/responding 或 idle；文案沿用 `tui.status_messages` | 显示，第 2 行 |
| `run_elapsed` | 当前/最近 run 从接受到终态的总耗时；阶段切换不清零，终态后冻结 | 显示，第 2 行 |
| `last_usage` | 最近模型调用 input/output/total token，供应商未提供时为未知 | 可选，默认关闭 |
| `session_usage` | 本会话所有模型调用的已知用量累计，含失败/取消中已拿到的 usage；不等于上下文大小 | 可选，默认关闭 |
| `run_id` | 当前/最近 run 的显示 ID；完整 ID 仍在 trace 中 | 可选，默认关闭 |
| `cwd` | 启动工作目录，家目录缩写及按显示列裁剪 | 可选，默认关闭 |

暂不加入费用、token/s、剩余预算、工具调用数、MCP/skill 数量：它们分别依赖定价、可靠计量或尚未实现的执行能力，待对应单元再增加字段。注册字段必须有数据消费者与验收，不显示伪造的系统清单。

## 3. 上下文占用的口径

`context` 默认显示，可关闭该项 enabled；显隐不改变 usage 采集、缓存计量或模型请求。上下文为单次实际请求的输入规模，不是多次 TotalTokens 的累计；没有发送前估算器，不通过字符数猜 token。

- 默认 context_format:bar 显示 `ctx: [██░░░░░░░░] 200k/1M`，条体后固定带当前/总计，数量遵循 token_format。show_percent:false 默认不加比例，true 可额外显示；context_format:usage 仍可显示 `ctx: 20.0% 200k/1M`。两种格式均不显示 `(last)`。
- 供应商给出本次 `PromptTokens` 且容量已知时：使用该次调用的实际输入比例；完成后保留这个快照。它是最近实际请求的输入，不是下一请求的发送前估算。
- 新草稿、成功回答追加历史、工具轨迹或上下文变化不会通过字符长度猜 token。没有估算器时保留最近已知快照；已知 1M 容量的新会话为 `ctx: [░░░░░░░░░░] 0/1M`。请求准备、等待、思考和回答期间保留初始零值或请求前快照，收到本次实际输入统计立即更新；新调用结束仍无输入统计才显示 `ctx: usage unknown`。准备阶段取消且未发起新调用时保留原值；容量未知始终显示 `ctx: capacity unknown`，`unknown:hide` 时隐藏整个字段。未来估算器若启用须带 `~` 来源标记并独立验收。
- 流式生成中的答案/思考不能用字符长度冒充新增 token。供应商的 output token 统计可能包含思考，也不直接等于未来回传历史的输入规模。
- 用户可在模型配置中提供 `context_window_tokens` 正整数，作为展示容量。官方 HTTPS DeepSeek 的 `deepseek-flash` / `deepseek-v4-pro` 缺失或 null 自动补写 `1000000`（1M），保留用户手填值；自定义端点或未知模型保持 null。该字段不修改 API 请求、不保证模型真实支持该容量、不代替执行预算。
- usage 模式超过 100% 时如实显示 `120.0% 1.2M/1M`，不裁成 100%。可选 bar 模式默认 10 格、填充向下取整、百分比最多一位小数；超过容量时条体填满但比例仍显示实际超限值。两种模式都不自动压缩或删除历史。
- 两种模式达到 context_bar 的 warning/critical 比例时使用现有提示黄/错误红，否则使用主题色。bar 的 Unicode 样式使用 `█`/`░`，ASCII 样式使用 `#`/`-`，都表示容量占用而非任务完成进度，不做等待动画。

已核对 [DeepSeek 官方模型规格](https://api-docs.deepseek.com/quick_start/pricing/) 的两预设 1M 上下文，以及 [官方集成示例](https://api-docs.deepseek.com/quick_start/agent_integrations/pi_mono/) 的 `contextWindow:1000000`。容量是分母，初始用量 0 是分子；不能把未知容量理解成用量非零。

本单元不为填满百分比引入粗略 `字符数/4` 算法或新 tokenizer 依赖。首版允许未知，验收必须覆盖它；未来估算器需独立确认模型适用性、误差和依赖增量。用户配置容量同样标明来源，`config show` 区分 `configured` / `unknown`。

## 4. 缓存与累计用量

缓存字段专指供应商报告的输入 token 缓存命中，不是客户端 Markdown 缓存、内存占用、历史条数或永久缓存配额。

- 模型层 Usage 提供独立可选的 `CachedPromptTokens`；有正数输入且明确命中 `0` 时显示 `0%`，缺失显示 `unknown`。OpenAI 使用 `prompt_tokens_details.cached_tokens`，DeepSeek 适配器同时支持 `prompt_cache_hit_tokens`，由协议层校验归一化；TUI 不读取 SDK 或供应商原始 JSON。
- 默认 cache_format:ratio、cache_scope:session，显示 `cache: 40%`，不显示条体或具体命中量/总输入。CC 累计口径仍为本会话缓存读取命中总和/对应总输入总和，按 token 加权；失败/取消的有效 usage 参与，Ctrl+N 清零、Ctrl+L 保留。其他 tokens/both/bar 格式保留可选。
- `cache_scope:last_call` 提供 Pi 的最近一次调用口径：该次 cached input / 该次 prompt input × 100%。请求进行中保留最近已报告的值；收到新 usage 后更新，新调用终态仍无 usage 则显示 unknown。准备阶段取消没有产生新调用时保留真实最近调用。
- 缓存命中是 prompt token 的子集，不再次加到 prompt/total；输出 token 和上下文容量不参与分母。尚无调用的新会话以 Go 数值零值初始化，默认 ratio 为 `cache: 0%`；tokens 为 0、both 为 0 (0%)，可选 bar 为条体加比例及命中量/对应输入，Ctrl+N 恢复零值；这只是初始显示，不作为供应商 usage 或已知调用计入统计。两种 scope 在尚未收到新缓存统计时保留请求前的零值或快照，不因当前调用尚未报告 usage 提前显示 unknown/partial；收到新缓存统计后更新，终态再按实际缺失情况显示 unknown/partial。输入统计先到但缓存尚未到时，两字段分别更新。缺失分母、非法数据或累计溢出仍区分 unknown，不猜数据。
- 累计中有缺失缓存或输入统计的调用时，只用两者都已知且合法的同一组调用计算，例如默认显示 `cache: 40% (partial)`（可选 bar 模式显示对应数量）；比率、分子与分母全部来自同组已知调用。没有任何已知缓存调用则为 unknown；未知缓存不能当零命中加入分母。tokens/ratio/both 仍可选，均沿用所选 scope，部分累计保留 partial。
- cache_bar 默认 10 格 Unicode 条，可配置 width 5–30 与 style unicode/ascii，比例按命中率填充、格数向下取整。cache_bar 和 context_bar 的宽度独立；窄屏先缩到最低三格，再按 priority 隐藏字段。命中率高表示较多输入由缓存读取，不使用上下文容量的 warning/critical 告警色。
- 只解析已核对的供应商字段并用 HTTP fixture 固化，模型适配器归一化。协议没有报告缓存就显示未知，不从重复 prompt 猜测命中，不为采集状态付费探测。
- usage 流更新是同一次调用的快照替换，不是可累加 delta。按模型调用标识幂等汇总，终态重复或与已有模型 trace 同时存在时不得重复计数。
- `session_usage` 出现未知调用时标明 `≥已知值 (partial)`；若没有任何已知调用则为 `unknown`，不能把缺失数据累计为 0。尚未发起调用的会话可显示已知的 `0`。

模型价格与缓存折扣不在本单元计算；显示命中量不承诺费用节省。

外部口径依据（2026-10-09 核对）：[Claude Code 官方 prompt_cache.hit_ratio](https://code.claude.com/docs/en/statusline#prompt-cache-fields) 使用主会话累计缓存读取 / 全部输入；[Pi 官方 footer 源码](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/src/modes/interactive/components/footer.ts) 使用最近一次输入的 cacheRead / (input + cacheRead + cacheWrite)。Pi 的 input 为未缓存部分，而 Plume 的 PromptTokens 已含缓存部分，所以 Plume 不能再次相加；缓存写入不算读取命中。本项目对缺失字段额外保留 unknown/partial 的明确语义。

## 5. Git 与 uv 环境采集

采集放在应用装配/状态采集逻辑，TUI View 只渲染快照。默认每 5 秒刷新本地环境，单次 Git 读取超时 500ms；禁止在每个正文分片或动画帧运行命令。同一采集器最多一个进行中的任务，取消或退出清理子进程；旧工作目录或旧会话的采集结果不得覆盖新快照。

Git 使用无 Shell 拼接的参数化只读命令，工作目录取应用记录的 cwd；读取分支/短 commit 与 porcelain 工作区状态，包含未跟踪文件的 dirty 标记。非仓库、无初始 commit、缺少 git、读取失败分别区分 `—`、`unborn`、`unknown`，不影响模型请求；超时的旧数据必须标记 `stale`，不能继续当作刚采集的数据。

uv/虚拟环境仅检查本地元数据：

- `uv.lock` 作为 uv 项目标记，说明项目存在 uv 锁文件，不能证明当前运行的 Python 是由 uv 创建或启动。
- `VIRTUAL_ENV` 指向已存在的环境时显示其名称和 `active`；项目 `.venv` 仅存在时显示 `present`，不能标成已激活。
- 活动环境与项目 `.venv` 不同时分别标识；没有 uv 项目标记但有活动虚拟环境时只显示 `venv:<name>(active)`。
- 不执行 `uv sync`、安装依赖或激活环境，不启动项目 Python 来填状态；plume 是 Go 程序，也不将 Python 环境视为其运行前提。

禁用 git/uv 字段就停止对应采集；未启用的字段不产生后台命令。分支名、路径、环境名均净化控制序列并按 Unicode 显示列裁剪。未来任意命令插件需另行设计超时、结果协议及授权，本单元只允许内置字段。

## 6. 写入 config.json 的完整默认项

以下 `status_line` 添加到已有 `tui` 对象内，保留 `status_messages`；不是用于覆盖整个配置的片段：

```json
{
  "tui": {
    "status_line": {
      "enabled": true,
      "max_rows": 2,
      "separator": " │ ",
      "unknown": "show",
      "clock_refresh_ms": 1000,
      "environment_refresh_ms": 5000,
      "git_timeout_ms": 500,
      "token_format": "compact",
      "time_format": "compact",
      "context_format": "bar",
      "context_bar": {
        "width": 10,
        "show_percent": false,
        "style": "unicode",
        "warning_percent": 80,
        "critical_percent": 95
      },
      "cache_format": "ratio",
      "cache_scope": "session",
      "cache_bar": {"width": 10, "style": "unicode"},
      "items": [
        {"id": "provider", "label": "Provider", "enabled": true, "row": 1, "priority": 60},
        {"id": "model", "label": "", "enabled": true, "row": 1, "priority": 90},
        {"id": "reasoning", "label": "think", "enabled": true, "row": 1, "priority": 50},
        {"id": "context", "label": "ctx", "enabled": true, "row": 1, "priority": 80},
        {"id": "cache", "label": "cache", "enabled": true, "row": 1, "priority": 40},
        {"id": "git", "label": "git", "enabled": true, "row": 2, "priority": 50},
        {"id": "uv_env", "label": "env", "enabled": true, "row": 2, "priority": 30},
        {"id": "session_elapsed", "label": "session", "enabled": true, "row": 2, "priority": 60},
        {"id": "phase", "label": "", "enabled": true, "row": 2, "priority": 100},
        {"id": "run_elapsed", "label": "", "enabled": true, "row": 2, "priority": 100},
        {"id": "last_usage", "label": "usage(last)", "enabled": false, "row": 1, "priority": 40},
        {"id": "session_usage", "label": "usage(session)", "enabled": false, "row": 1, "priority": 40},
        {"id": "run_id", "label": "run", "enabled": false, "row": 2, "priority": 20},
        {"id": "cwd", "label": "cwd", "enabled": false, "row": 2, "priority": 20}
      ]
    }
  }
}
```

`models[]` 中提供展示容量字段。官方 DeepSeek 两预设默认如下；自定义服务或未知模型默认 null，其他模型字段保留：

```json
{"context_window_tokens": 1000000}
```

配置规则：

- 保持 schema v1 的增量兼容策略。新 setup/Save 写出所有默认字段；旧配置首次 Load 原子补齐缺失键，同时保留未知扩展字段和用户自定义值。重复读取不改写；setup 不新增状态栏或容量问题。
- `items` 数组的顺序就是显示顺序；缺省数组补写默认数组。用户提供数组则视为完整选择，未列出的字段不被自动加回；`[]` 表示隐藏全部字段，`enabled:false` 关闭整栏。数组中单项缺失的属性按对应字段默认值补齐并落盘。
- context 默认 enabled:true，设为 false 可隐藏；旧配置已有显式值保留，缺失属性才补默认。当前用户配置按本次要求同步，其他字段和 CC/Pi 统计保持。
- `max_rows` 为 1 或 2；`row` 为 1 或 2；`priority` 为 0–100，数字越大越晚被窄屏隐藏，优先级相同时先隐藏数组靠后的字段。标签为空时只显示值，非空时显示 `label:value`（provider/context 和非 bar 缓存为 `label: value`；bar 缓存为 `label [条体]百分比 命中量/总输入`）。context 不显示 `(last)`，partial/stale 等标记保留；缓存 scope 和自定义标签保持配置原样。
- `unknown` 为 `show` 或 `hide`，决定未知数据是否占位；已知 0 永不按未知隐藏。非适用数据使用 `—`，也受该策略控制。
- token 格式为 `compact` / `full`，控制上下文的输入量/容量、缓存和 usage 数量；compact 按十进制 k/M 缩写、最多一位小数。时间为 `compact` / `clock`；cache_format 为 `ratio`（默认）/ `tokens` / `both` / `bar`，cache_scope 为 `session`（默认，CC 口径）/ `last_call`（Pi 口径），两项独立控制；cache_bar.width/style 完整落盘。
- context_format 为 bar（默认）或 usage。context_bar.width 为5–30格，show_percent 只控制 bar 的可选百分比，默认false，当前/总计始终显示；style 为 unicode/ascii，阈值颜色沿用 warning/critical。usage 模式固定一位小数比例不受 show_percent 控制。
- `clock_refresh_ms` 为 250–5000，`environment_refresh_ms` 为 1000–60000，`git_timeout_ms` 为 100–2000。独立于现有 100ms spinner 和 50ms 正文刷新；显示字段不需要时不订阅额外时钟。
- 单项 ID 必须来自 §2，不能重复；最多 14 项。`separator` 最多 8 个显示列，label 最多 20 个显示列，均禁止控制字符与换行；未知 ID、非法类型/枚举/范围在本地校验失败，定位字段路径，不执行配置内容。
- `context_window_tokens` 允许 `null` 或 1–2147483647 的整数；null 表示自动解析，已验证官方两预设补 1000000，其余仍为 null。配置 show 显示状态栏字段的有效值和模型容量来源，不输出凭据、环境变量全集或私密路径日志。
- 在线启动读取一次，修改配置后重启生效；Ctrl+N 只重置会话统计，不重读配置。`chat --offline` 保持不读取用户配置，使用同一默认布局；定制配置通过隔离 PLUME_HOME 和 fixture 验收。

默认值在实际配置中可见，运行时 fallback 只用于 offline、程序化测试和兼容入口，不能代替在线配置的落盘行为。在线 Load 和 Save 已完成默认补齐；编辑配置后重启聊天生效。

## 7. 窄屏、通知与计时行为

- `max_rows` 是行数上限：默认最多两行，全部字段及至少三格的条体能放下一行就合并；放不下才用两行。环境/用量/时钟/滚动变化时同步重新计算栏高和输入光标；全栏关闭或所有字段关闭时占 0 行。布局必须重新计算记录区高度及真实输入光标位置，继续保留输入区上下分隔线。
- 终端宽度小于 60 列或高度小于 18 行时，最多占 1 行；用户 `max_rows:1` 同样合并两逻辑行。先按数组顺序合并，再按配置优先级隐藏字段，最后裁剪仍超宽的单字段；不让终端自然折行。
- 正常两行时按各字段 row 放置；窄屏合并后 `row:2` 也有机会保留。分隔符只出现在实际保留的相邻字段之间；空字段不产生多余分隔符。
- 上下文条在窄屏优先缩短至至少 3 格，再按字段优先级决定是否隐藏；不退回 K 数值。未知数据使用简短且明确的原因文案，不显示问号条；极窄窗口可移除整个字段。
- 默认优先保留 phase/run_elapsed、模型、会话时长，随后减少供应商、Git、思考强度、缓存、uv 等；context 默认关闭，用户主动恢复后沿用配置 priority。用户可以改优先级或关闭任意字段。
- 思考标题在可见区时仍由标题承载动画/文案/本次 run 耗时，状态栏的 phase/run_elapsed 暂时不重复；标题滚出可见区再接续。会话累计时长独立存在，不因思考区可见而隐藏。
- 输入拒绝、取消提示和错误优先于普通指标，延续现有提示语义；关闭状态栏时追加系统消息并滚动到提示，让关键拒绝立即可见。普通流式更新仍遵守上滚阅读冻结规则。
- 会话时长以可注入的单调时钟计量，idle 也增长，Ctrl+L 不清零、Ctrl+N 清零；尚无跨重启恢复，本次进程退出后不继续累计。run 耗时含请求前准备，与会话时长及供应商推理耗时分别标识。
- 单行或两行都左对齐，默认亮青标签、浅青数值；无色终端保留标签、unknown、估算和 dirty 标记。宽字符、emoji、长分支/路径、极窄/极矮窗口均按可见列和实际高度降级。

## 8. 模块边界与验收

模型/适配器提供 typed usage 和缓存字段；app 按模型调用 ID 替换快照与去重累计，提供会话起点和 Git/uv 异步采集器；CLI 装配并桥接快照。TUI 通过 Hooks/事件接收结构化状态，只做状态转换和显示，不联网、不在 View 访问文件或启动进程。

**实现范围：** `internal/config` 状态栏配置与默认持久化、`cmd/plume` 启动装配和 show、`internal/model` Usage/协议解析/fake、`internal/app` 统计与本地采集、`internal/tui` 布局/状态/光标、相应测试、runbook 与审核证据。沿用 Agent 的调用开始与 usage 事件，不新增估算器、包目录或外部依赖，不升级 SDK。

验收覆盖：

1. 默认字段及 context_bar 完整写入新/旧配置；自定义顺序、标签、显隐、条宽/样式/百分比/阈值、空数组、禁用、范围错误、未知项、模型容量 null 与原子失败保护；offline 不读取畸形真实配置。
2. 上下文零值/实际/unknown/超限、固定一位小数、输入量/容量和可选进度条，不显示 `(last)` 且不与 session 累计混用；缓存初始零/实际零/未知/子集关系/缺失尾帧、usage 更新重复和失败调用累计；切换会话不串统计。
3. 非 Git、unborn、detached、dirty、缺少命令、超时与 stale；uv 项目标记、活动/仅存在/外部 venv；禁用停止采集，慢采集不阻塞输入/取消，退出不泄漏子进程。
4. 120/80/40/20 列与 24/16/8 行布局、1/2 行配置、字段移除顺序、CJK/emoji、真实输入光标、Thought 不重复，以及全栏关闭后关键提示可见。
5. 隔离 pty 和本地模型 fixture 展示成功、失败、取消及 usage unknown；通过 Go 测试/竞态/vet/格式检查，使用 scripts/build.sh 构建，交付 `docs/reviews/G1b.4.md` 后停止等待审核。

G1b.4 已审核通过，实现记录见 [G1b.4 审核记录](../../reviews/G1b.4.md)；估算显示属于未来条件分支，当前没有发送前 token 估算能力。下一单元 G3 需另行授权。
