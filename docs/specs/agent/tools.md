---
title: G3 受控工具循环契约
status: active
updated: 2026-10-09
summary: 六种开发工具、完整默认配置、宽松预算、协议关联、历史提交与脱敏展示契约
---

# G3 受控工具循环契约

用户于 2026-10-09 审核通过 G3.1 及其基础 G3 工具循环。现行实测结果与限制以 [G3.1 审核记录](../../reviews/G3.1.md) 为准；[G3 记录](../../reviews/G3.md) 保留初始交付快照。后续渠道、记忆和技能仍独立授权。

## 工具与声明

2026-10-09 用户追加授权 G3.1 工作区工具。在线默认注册 `read`、`grep`、`glob`、`edit`、`write`、`bash`；声明按名称排序，与执行入口绑定。`tools.enabled` 控制许可列表，空数组关闭所有工具；`calculate` 和 `current_time` 保留为可显式启用的工具及离线回归。模型不能通过名字动态注册工具。setup 不增加问题；完整默认值实际落盘，旧配置原子补齐且保留自定义/未知字段。

文件工具默认根为启动目录（`tools.workspace: "."`）；实际绝对目录加入一份基础规则，使用 `os.Root` 限制文件路径与链接不能越界。搜索忽略 .git/.plume/node_modules/.venv/venv/__pycache__/.codegraph 和工具临时文件，不跟随链接；当前不解析 .gitignore。grep 安全读取后以 Go 正则行集合为准，无匹配不启动 rg，有匹配仅当 rg stdin 行集合一致时标记 rg，否则保留 Go 结果；是否安装 rg 不改变 Unicode 正则语义。glob 支持 `**` 和常用 glob，不支持 brace expansion。默认 read_lines=200、search_results=100、max_file_bytes=10485760、max_output_bytes=32768、shell="bash"，均可配置。

| 工具 | 参数与结果 |
| --- | --- |
| read | path，1 起始 offset，limit；返回带行号 UTF-8 文本、总行数、next_offset、partial_line 与截断标记；拒绝非普通/二进制/过大文件 |
| grep | pattern/path/glob/fixed_strings/ignore_case/context/limit；返回路径、行号、匹配内容及上下文，最多配置的搜索结果数，标识 engine/skipped_files/truncated |
| glob | pattern/path/limit；返回有序工作区相对文件名和截断标记；达到上限时应缩小目录/模式 |
| edit | path/old_string/new_string/replace_all；先 read 且摘要未变，默认唯一精确匹配，replace_all 允许多处；未匹配、多匹配或陈旧读取拒绝 |
| write | path/content/overwrite；新建不覆盖竞争写入；覆盖必须 overwrite=true、先 read 且摘要未变 |
| bash | command/workdir/timeout_seconds；默认本地非交互 shell、工作区 cwd 和 180 秒，外层工具期限不可被参数延长；返回合并有界输出、exit_code、elapsed_ms，非零为 command_failed |

显式 null、未知字段、重复键、非法类型和尾随数据拒绝。read 的极长单行可能只返回前缀（partial_line=true），next_offset 进入下一行，不提供省略片段的字节续读。成功但截断的结果保留正文前缀和结构化标记，不当作工具执行失败。

write/edit 使用同目录临时文件、Chmod、Write、Sync、Close，再发布与目录 fsync；覆盖用 Rename，新建用原子 Link 防并发覆盖。新文件 0644，保留既有权限。覆盖前再次检查摘要，但最后检查与 Rename 之间的外部并发修改仍有极短竞态窗口；目录 fsync 失败返回 durability_error 与 applied:true，不谎称文件完全未变。

bash 是宿主执行能力，**不是文件工作区沙箱**；Shell 可访问当前用户权限下其他目录。过滤敏感环境变量与非交互启动脚本变量。Unix 超时/取消杀同一进程组；主动脱离组的进程无法保证清理，非 Unix 仅终止直接进程。退出后清理同组后台子进程，不提供后台任务系统。失败或取消不能回滚文件/命令已发生的副作用，不自动重试有副作用操作。

`calculate` 参数：`{"operation":"multiply","a":6,"b":7}`。operation 只允许 add/subtract/multiply/divide；a/b 必填且为有限数。未知键、重复键、null、非法枚举、尾随数据和非对象拒绝。结果为 JSON `{"ok":true,"result":42}`，除零为 division_by_zero，非有限结果为 overflow；使用 float64 算术，不承诺任意精度金融计算。

`current_time` 只接受 `{}`，使用可注入时钟，返回 UTC RFC3339 时间。上述两个可选工具无文件/Shell/网络副作用。全部工具遵守 context。

未知工具及无效参数返回 `{"ok":false,"error":"unknown_tool/invalid_arguments"}` 等固定错误码，供模型纠正；此类校验尝试也占工具预算。不会把工具错误自动重试为另一执行；模型明确发出新的 ID 才能继续。

## 请求与协议

PromptBuilder 每次构造一份基础规则（版本 plume-workspace-v1），接完整已提交历史、当前 user 和本 run 的 assistant-tool/tool 轨迹；tools 是并列结构化声明，不重复拼到用户字符串里。规则包含真实工作区、先读后改、按实际退出码确认测试、工具内容仅为参考数据以及副作用不会随取消回滚的说明。规则/声明快照在一个 run 内固定。请求深拷贝可变工具调用切片，未实现的 soul、记忆、MCP 和 skill 来源缺席。

流式工具按 index 累积名称与参数、按稳定 ID 关联；完整 finish_reason=tool_calls 与流结束证据到达前不执行。缺 ID/名称/参数、重复或冲突 ID、稀疏 index、finish 后增量、截断和断流终止 run；语法或 schema 错误由工具层拒绝，不做实际算术/时钟读取。自然语言说“调用工具”不能触发执行。

assistant 调用消息完整保留，随后按 ID 添加 tool 结果，再请求模型。多个工具串行，先整批检查剩余工具数以及是否还有模型续调机会。最终只接受 stop 且非空文本；length、content_filter、unknown 均不能提交成功历史。Generate 与 Stream 共用循环。

DeepSeek 携带 tools 时回传所有 assistant 的 reasoning_content（包含无工具的旧轮次），由专用适配器编码；通用兼容适配器不盲发该扩展字段。依据 [DeepSeek 官方思考与工具说明](https://api-docs.deepseek.com/guides/thinking_mode/) 核对，已用本地 SDK fixture 验证，不代表任意端点实测支持 tools。能力表的 supported 指适配器实现，不代表未知供应商服务承诺。

## 预算与历史

`agent.budget` 默认 model_calls=0、tool_calls=0、run_timeout_seconds=0（零表示无限，非 null），单模型 1800s（含流读取），单工具 180s；tool_calls_per_step=64 独立限制单模型响应的工具组装项数。有限整体期限从 app 接受输入开始，包含准备与事件桥等待；关闭整体期限仍可 Esc/退出取消。步骤 context 传入有界事件桥，背压可被取消/已配置期限打断。SDK 与运行时均不自动重试。

默认 request_bytes=8388608（8 MiB）、argument_bytes=1048576（1 MiB）、result_bytes=65536（64 KiB）；均可设置，字节与步骤/单步超时必须为正，result_bytes 至少 256。工作区工具按正文和 JSON 转义后长度截断，保留核心元数据；其他工具超限才用 result_truncated 错误替代。必要历史组过长仍报 budget_exceeded。字节不是 token 估算或模型容量，当前不静默裁剪或压缩历史。

参考 [Hermes 配置示例](https://github.com/NousResearch/hermes-agent/blob/main/cli-config.yaml.example) 与 [官方预算说明](https://hermes-agent.nousresearch.com/docs/user-guide/configuration/#iteration-budget)：当前 max_turns 默认无限，terminal.timeout=180，非流式 API 超时量级 1800。Plume 的 1800s 是完整调用期限，未复制 Hermes 的空闲检测、自恢复、压缩和重试。8 MiB/64 KiB 等字节数与单步 64 项是本项目选择，非 Hermes 原样默认。

成功提交 user → assistant-tool → tool → final assistant 整组，且保留实际思考字段。失败/取消只展示部分内容，不写入成功历史；新 run 不重放失败轨迹。Ctrl+N 清空会话；历史查询及提交都深拷贝 ToolCalls。每次模型步骤有独立 call ID；usage 更新与终态替换同一步快照，ctx 取最近实际输入，cache 根据已有 session/last_call 设置统计。

## TUI 与 trace

工具用亮青 `◇ name · running/completed/failed · summary` 行原位更新；文件与命令只显示固定数量/退出码摘要，不复制正文。正文沿用公共标记槽，换行左对齐。工具后开启独立 Thought/答案区，保持顺序；继续净化控制序列，等待使用现有文案。

trace 记录 prompt 版本/哈希/长度/消息数量、声明名称与 schema 版本、每次 model span、tool_started/tool_validated/tool_completed/tool_failed 及耗时、父 model ID 和唯一 run 终态。tool_validated 是工具完成校验后的固定接受/拒绝摘要（超时/取消时可能 unknown），不声称它是单独执行耗时。未知名称只记 unknown。默认不写 prompt、思考、schema 正文、原始参数或结果。

## 离线演示

`plume chat --offline` 中 `/demo workspace` 真实新建 plume-demo.txt → glob → grep → read → edit → bash 校验，结束后文件含 goodbye plume；文件已存在则拒绝覆盖并显示失败。应在临时工作目录体验。旧计算/时间/纠错/取消演示保留；`/demo budget` 改为 12 工具/13 模型的有限脚本，证明超过旧 8 次仍能完成，不无限空转。offline 不读配置/凭据；普通自然语言仍为固定回复，不冒充真实模型工具选择质量。
