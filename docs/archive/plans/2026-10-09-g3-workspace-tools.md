---
title: G3 工作区工具与可配置预算实施计划
status: historical
updated: 2026-10-09
summary: G3.1 已完成计划归档：六种开发工具、完整默认配置与宽松预算于 2026-10-09 通过
---

# G3 工作区工具与可配置预算实施计划

用户于 2026-10-09 授权加入全部六种开发工具，并要求预算接近 Hermes。沿用原目录和现有循环，按 writing-plans、TDD 与 subagent-driven-development 完成实现、规格与质量审核。用户随后审核通过并授权提交推送；本计划归档，现行行为以[工具契约](../../specs/agent/tools.md)和[审核记录](../../reviews/G3.1.md)为准，不自动推进其他阶段。

目标：真实文件定位、读取、修改、命令与测试执行形成闭环；默认配置实际落盘，旧值和未知字段保留。

## 配置与预算

新增 `agent.budget`、`tools`；schema v1 的兼容扩展。预算字段完整落盘：model_calls=0、tool_calls=0、tool_calls_per_step=64、run_timeout_seconds=0、model_timeout_seconds=1800、tool_timeout_seconds=180、request_bytes=8388608、argument_bytes=1048576、result_bytes=65536。次数及整轮时长 0 表示无限，负数拒绝。单步工具数、字节数及单步超时必须为正。保留有限上限预检、协议完整性与取消语义；无自动重试。

`tools` 默认 workspace="."，enabled=["read","grep","glob","edit","write","bash"]，read_lines=200、search_results=100、max_file_bytes=10485760、max_output_bytes=32768、shell="bash"。启用数组允许为空；所有字段可自定义并完整落盘，setup 不新增问题。配置 null/类型错误拒绝。配置校验不 import tools/agent。CLI 装配把配置转换为工具选项及 Limits；离线仍不读配置。

参考 [Hermes 当前配置示例](https://github.com/NousResearch/hermes-agent/blob/main/cli-config.yaml.example) 与 [配置说明](https://hermes-agent.nousresearch.com/docs/user-guide/configuration/#iteration-budget)：默认轮数无限、terminal.timeout=180；1800s 单模型上限是其 API 超时量级，不等同于 Hermes 的空闲检测。Plume 字节上限和单步 64 项是本项目选择。

## 工具边界

read 支持 path/offset/limit、行号和下一页；grep 支持 pattern/path/glob/固定文本或正则/大小写/上下文及上限；glob 支持 ** 模式和结果上限。文件路径限制工作区，使用 Go os.Root 防越界链接，拒绝非普通文件、二进制、过大文件。搜索跳过 .git/.plume/node_modules/.venv 等；验收发现 rg/Go Unicode 正则存在差异，现以 Go 结果为准，无匹配不启动 rg，有匹配才校验 rg 一致性。

edit 参数 path/old_string/new_string/replace_all，未找到或多处匹配默认拒绝；write 参数 path/content/overwrite，存在文件覆盖必须明确 overwrite，并校验此前 read 的文件摘要防陈旧覆盖。写入用同目录临时文件、Sync、Rename，不半写目标；保留现有权限，创建文件默认 0644。工具内容传回模型，TUI/trace 只输出固定摘要、数量/错误码，避免整文件或命令污染状态行。

bash 参数 command/workdir/timeout_seconds；本机 shell，默认 cwd 工作区，进程不继承交互输入，合并 stdout/stderr、退出码、耗时和截断信息有界返回。取消/超时清理进程组，工具超时是最终上限。bash 不是文件工作区沙箱；shell 本身可以访问启动用户权限下其他文件。无后台任务系统；副作用失败/取消不会回滚已写文件，不自动重试。

## 实施与验收

- [x] 配置测试先失败：默认实际落盘、旧文件补齐、0/false/空列表保留、未知字段保留、非法值拒绝；实现 config schema/默认/Load/Save/config show。
- [x] 工具测试先失败：临时工作区中的 read/grep/glob、精确 edit/atomic write、链接越界、二进制、陈旧覆盖、超限分页、bash 非零/大输出/取消进程树；实现 internal/tools 各职责文件。
- [x] 预算测试先失败：超过旧 8 次可成功、无限时取消仍生效、有限预算拒绝、流 index 上限独立配置；修复 agent/app 超时和完整结果回填。
- [x] CLI 真实装配、六工具默认声明、启用空列表、工作目录规则验证；更新提示词与离线显式 /demo workspace。
- [x] 模型 fixture 驱动 glob→grep→read→edit→bash→最终回答；失败保留部分 UI、无成功历史，文件副作用如实说明；记录 trace 成功失败证据。
- [x] 规格审核再质量审核；rtk go test -race ./...、rtk go vet ./...、gofmt 与 diff 检查；scripts/build.sh --install。
- [x] 更新 AGENTS、契约、审核、每日记录、索引与路线图；2026-10-09 用户审核通过并授权提交推送，本计划归档。状态栏默认隐藏 ctx 的追加修改一并通过。
