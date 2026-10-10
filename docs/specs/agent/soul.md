---
title: G3.2 人格初始化与上下文拼装契约
status: active
updated: 2026-10-10
summary: soul.md 默认配置、setup 无覆盖初始化、每 run 快照、读取失败与人格执行边界
---

# G3.2 人格初始化与上下文拼装契约

用户于 2026-10-09 明确授权在 setup 初始化 `soul.md`，并接入上下文拼装，2026-10-10 审核通过。本契约限定 G3.2 范围；交付与审核状态见 [G3.2](../../reviews/G3.2.md)，实施步骤见 [归档计划](../../archive/plans/2026-10-09-soul-context.md)。不包含长期记忆、MCP、skill 或工作区人格文件发现。

## 配置与文件

schema v1 在 `agent` 下增加 `soul`，默认值全部实际写入 `config.json`：

```json
{
  "agent": {
    "soul": {
      "enabled": true,
      "path": "soul.md",
      "max_bytes": 65536
    }
  }
}
```

- `enabled` 控制 setup 初始化及在线读取；false 时均不访问人格文件。
- `path` 相对配置目录（`PLUME_HOME` / 默认 `~/.plume`）解析，不是项目工作目录。支持 `~`、环境变量与绝对路径；原值和展开后的空值/Unicode 控制字符均拒绝。
- `max_bytes` 是读取文件的最大字节数，范围 1..8388608（最多 8 MiB）。默认 64 KiB，不是 token 预算或上下文容量。

Save 完整落盘，旧配置首次 Load 原子补齐 agent.soul 对象及已知字段的缺失/null，保留 false、自定义值及未知 JSON 字段。非法类型、显式空路径和非正上限拒绝，不把它们视为缺省。完整文件重复读取不改写。Load 只补配置，不自动创建人格文件；旧用户运行 `plume setup` 后才会初始化。`config show` 展示字段，永不打印人格正文。

默认路径为 `$PLUME_HOME/soul.md`；未设置 PLUME_HOME 的 POSIX 系统为 `~/.plume/soul.md`。人格是可编辑的 Markdown 文本，默认模板描述 Plume 身份、简洁明确的表达和协作风格，不含密钥、长期记忆或工具执行权限。

## setup 无覆盖初始化

模型配置原子保存成功后、进入渠道步骤前初始化，setup 不新增人格问题。配置禁用则跳过；模型保存失败或此前取消不生成文件。渠道取消不撤销已经保存的模型和人格，流程不承诺跨文件事务。

`internal/soul` 创建同目录临时文件，设 0600，写入/Sync/Close 后以 Link 无覆盖发布，再尽力 fsync 目录（与 config 同样不返回目录同步错误）。已有普通文件，包括空文件、用户自定义内容、指向普通文件的符号链接和竞争创建的目标，均不覆盖、不收紧权限；目录、设备和悬空链接明确拒绝。再次 setup 只补不存在的默认文件；发布前失败不留下部分目标。文件发布的原子性不等于断电后的完整持久性保证。

初始化路径或 IO 错误返回明确错误并停止后续 setup；此前已经落盘的模型配置继续有效。不把正文、文件内容或秘密写入错误/trace。

设置总结显示 `Soul: <path> (created)`、`Soul: <path> (preserved)` 或 `Soul: disabled`，说明资产位置与本次是否创建；不打印正文，不新增向导问题。

## 准备阶段读取与快照

在线每个 run 的 preparing 阶段读取一次，然后构造 PromptBuilder；当前 run 的所有模型续调复用同一人格快照。用户在工具循环中修改文件只影响下一 run，不重复追加人格区块。运行配置沿用现有会话装配时机；文件正文在下一 run 更新。

缺失、零字节或只有空白的文件不加入人格区块；不会自动补默认正文，也不发送占位符或空标题。禁用不读文件。`plume chat --offline` 不读取用户配置或人格文件，仍可运行既有演示。

读取要求普通 UTF-8 文本文件，以 `max_bytes + 1` 的有界读取判断超限，不静默截断。非普通文件、非法 UTF-8、权限/IO 失败和超限都在首次模型请求前以 invalid_config 明确失败；不发送人格残片，不提交成功历史。错误只说明类别，不包含正文。取消保留 context 错误；完整请求超出 agent.budget.request_bytes 时仍为 budget_exceeded，与人格文件读取上限分开。

## 消息拼装与权限边界

固定模板与 `prompt.go` 顶部拼装蓝图一致：

```text
system = {base_rules}{soul.md}{runtime}{memory}{skill}
messages = system → 已提交历史 → 当前 user → 本 run assistant/tool 轨迹
tools = {tool} + {mcp}
```

只产生一个 system 消息。基础规则优先，人格区块明确标识 `soul.md` 来源，影响身份、语气和风格；运行环境接在后面。memory/skill/MCP 尚未接入，文本槽仍为空、结构化 Tools 只含已许可本地工具。人格不是另一套工具 schema，也不拼到用户输入。

人格不能授予删除、推送、发布、发消息等执行权限，不能修改工具 allowlist、预算或取消行为。来源标记不保证模型绝对服从，因此执行许可仍由现有代码与用户指令约束；人格文件不作为普通工具结果数据混入。

Generate 与 Stream 共用准备和循环，消息语义一致。提示词版本更新为 `plume-workspace-v2`；人格正文计入完整请求的 JSON 字节预算与现有请求 hash。只延续原有版本/hash/bytes/消息数 trace，不增加人格正文或单独人格裸哈希日志。

## 验收与限制

必须覆盖完整默认配置与兼容补齐、初始化幂等/并发无覆盖、0600、新旧空文件保留、禁用/缺失/空白跳过、路径解析、UTF-8/普通文件/超限/读错误、单 system、每 run 快照及下一 run 更新、Generate/Stream 一致、读取失败零模型调用和历史隔离、trace 脱敏与 offline 不访问用户文件。

不创建记忆库、MCP 客户端或 skill 加载器；不递归查找项目中的 SOUL.md/soul.md，不支持多个人格文件或运行中配置热切换，不做自动摘要、token 估算或人格内容质量评测。文件读取不是与外部修改者隔离的事务，应避免在实际读取期间原地改写；正常编辑后下一 run 采用新正文。Unix 打开时使用非阻塞标志并核对实际文件类型，避免路径检查后被换成 FIFO 导致等待；其他平台常规打开仍有该竞态局限。普通文件 IO 的 context 在读取前后检查，不提供文件系统层的中途读取消。无覆盖发布依赖文件系统支持硬链接；目录同步尽力而为，Windows 未实测。本地 fake/fixture 只验证拼装与失败策略，不代表供应商对人格的遵循程度。
