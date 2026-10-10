---
title: G3.2 人格初始化与上下文拼装实施计划
status: historical
updated: 2026-10-10
summary: G3.2 已于 2026-10-10 通过；保留人格初始化和每 run 快照的完成步骤
---

# G3.2 人格初始化与上下文拼装实施计划

> 历史完成计划：用户于 2026-10-09 授权实现，2026-10-10 审核通过；现行行为以人格契约为准。本文保留实施步骤，不作为重新开工或推进记忆/MCP/skill 的授权。

**目标：** setup 生成可编辑的 `soul.md`，在线 Agent 每次 run 准备时读取一次，并在同一工具循环内复用人格快照。

**架构：** 新建 `internal/soul`，负责默认模板、无覆盖初始化和有界读取；config 管字段、默认补齐和路径解析，setup 调用初始化，agent 在唯一 system 消息中拼装人格。TUI、模型适配器与工具执行器不读取人格文件。

**技术栈：** 现有 Go 标准库、schema v1 原子配置持久化、现有 Agent/模型契约；不引入依赖。

关联：[人格契约](../../specs/agent/soul.md)、[上下文规划](../../plans/agent-context.md)、[第一阶段计划](../../plans/phase-01-tui.md)、[模型决策](../../decisions/0003-model-runtime.md)。

## 1. 配置与路径

**文件：** `internal/config/agent_tools.go`、`internal/config/defaults.go`、`internal/config/raw.go`、新建 `internal/config/soul.go` 与 `internal/config/soul_test.go`、`cmd/plume/config.go` 及相关测试。

- [x] 先写测试：Save 实际写入三个默认字段，旧配置 Load 原子补齐；`enabled:false`、自定义路径/上限及未知字段不被覆盖；完整文件重复 Load 字节不变。
- [x] 使用下列默认对象扩展 `agent`，保留 schema v1；对象及字段缺失/null 补默认，拒绝非法类型、空/含 Unicode 控制字符路径和范围1..8388608之外的上限，环境展开后仍校验路径。

```json
{"agent":{"soul":{"enabled":true,"path":"soul.md","max_bytes":65536}}}
```

- [x] 相对路径从 `config.Dir()` 解析，支持 `~`、环境变量与绝对路径；验证设置 `PLUME_HOME` 后不会意外从工作区读取同名文件。
- [x] `config show` 显示启用状态、路径和上限，不读取或打印人格正文。
- [x] 运行 `rtk go test ./internal/config ./cmd/plume`，确认配置/展示回归通过。

## 2. 默认模板、初始化与读取

**文件：** 新建 `internal/soul/{soul,open_unix,open_other,soul_test,soul_unix_test}.go`、`internal/soul/default.md`。

- [x] 先写初始化测试：不存在时生成 0600 文件；重复调用保留原字节；已有空文件、用户编辑文件、并发创建均不覆盖；失败不留下部分目标文件。
- [x] 默认 Markdown 模板仅描述 Plume 的身份、回答语气和协作风格；不将它作为工具许可、记忆或密钥存储。
- [x] 初始化按同目录临时文件 → Chmod → Write → Sync → Close → Link 无覆盖发布 → 尽力目录 fsync；已有普通文件及指向普通文件的链接原样保留，非普通/悬空链接拒绝。
- [x] 先写读取测试：禁用不访问文件，缺失/空/空白跳过，UTF-8 正文按上限读取；非法 UTF-8、非普通文件、读失败与超限返回不含正文的错误。
- [x] 实现有界读取；不得静默截断人格或估算 token。
- [x] 运行 `rtk go test -race ./internal/soul`，确认幂等与读取边界通过。

## 3. setup 初始化

**文件：** `internal/setup/wizard.go`、新建 `internal/setup/soul_test.go`、`cmd/plume/setup.go`、`cmd/plume/runtime_soul_test.go`。

- [x] 先写测试：模型保存成功后、渠道选择前初始化；模型失败/取消不生成；禁用不生成；重复 setup、渠道取消保留已保存模型和既有人格；不新增 Prompter 问题。
- [x] 初始化失败终止后续流程并返回脱敏错误，已成功保存的模型配置仍保留，不能称为跨文件事务回滚。
- [x] `setup.Result` 带回人格路径与是否新建；CLI 总结显示 `Soul: <path> (created|preserved)` 或 `Soul: disabled`，不读正文。
- [x] 运行 `rtk go test ./internal/setup`，确认现有向导路径和新初始化行为通过。

## 4. 每 run 快照与 system 拼装

**文件：** `internal/agent/{runtime,loop,prompt}.go`、新建 `internal/agent/soul_test.go`、`cmd/plume/chat.go`、`cmd/plume/runtime_soul.go`、`cmd/plume/runtime_soul_test.go`。

- [x] 先写 fake/工具循环测试：第一模型请求包含人格且只含一个 system；工具前后修改文件不改变当前 run，下一 run 使用新内容；Generate/Stream 得到相同消息语义。
- [x] 在线装配解析好的文件设置；`--offline` 分支不读配置或 soul。
- [x] 在 preparing 阶段、首次模型调用前读取一次，再构造 PromptBuilder。文本顺序固定如下，工具声明仍走 `ChatRequest.Tools`：

```text
system = {base_rules}{soul.md}{runtime}{memory}{skill}
messages = system → 已提交历史 → 当前 user → 本 run assistant/tool 轨迹
tools = {tool} + {mcp}
```

- [x] 将 prompt 版本更新为 `plume-workspace-v2`。人格区块标明身份/风格来源及执行许可边界；无正文时不发送空标题/占位符。人格参与完整请求字节预算与原有 hash，不单独记录正文或人格裸哈希。
- [x] 用超限/非法文件测试确认首次模型调用数为零、成功历史不变、run 为 invalid_config 失败终态；取消保留 context 错误，完整请求超限仍为 budget_exceeded。
- [x] 运行 `rtk go test -race ./internal/agent ./internal/app ./cmd/plume`。

## 5. 交付与审核

- [x] 同步 `AGENTS.md`、`docs/index.md`、上下文/配置/工具契约、setup runbook、README、阶段计划与决策中“人格未实现”的现行说明。
- [x] 新建 `docs/reviews/G3.2.md`（pending-review），在 roadmap 数据增加 G3.2；当日记录追加到 `docs/daily/2026-10-09.md`，保留原日志。
- [x] 执行 `rtk go test -race ./...`、`rtk go vet ./...`、`rtk proxy gofmt -l .`、`rtk git diff --check` 和 `rtk proxy ./scripts/build.sh`，保存真实结果。
- [x] 在临时 `PLUME_HOME` 验证 setup 默认资产、用户修改与再次 setup、在线请求 fake 成功/读取失败 trace；`--offline` 不依赖人格。记录实际命令、路径与结果，不用本地 fixture 推断真实供应商性能。
- [x] 交付后按用户授权提交推送，2026-10-10 用户审核通过；计划归档，审核窗口证据清理，不推进下一来源。
