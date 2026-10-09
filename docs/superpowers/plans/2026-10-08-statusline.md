---
title: G1b.4 可配置状态栏实施计划
status: active
updated: 2026-10-09
summary: 按已批准的状态栏契约实施配置、缓存、环境采集、统计、渲染和交付验证
---

# G1b.4 可配置状态栏实施计划

采用 subagent-driven-development 与 TDD 按任务实施；用户已授权本单元代码执行。继续使用原工作目录，保留未提交的技术文档；不推进 G3，不在实现验收前提交或推送。

**目标：** 实现 docs/tui-statusline.md 中最多两行、单行优先的亮色可配置状态栏及上下文进度条，实际默认配置落盘。

**架构：** config 定义纯配置；模型归一化缓存 usage；app 统计与异步采集；TUI 只消费快照和渲染，CLI 装配。未知容量不显示伪造比例，不引入 tokenizer 或 SDK 升级。

**技术栈：** 现有 Go、charm.land v2、openai-go v1.12.0；不新增外部依赖。

## 任务 1：配置与原子迁移

- [x] 在 internal/config/statusline_test.go 先测试默认落盘、部分对象补齐、空 items 保留、自定义值、非法配置和 context_window_tokens null。
- [x] 执行 rtk go test ./internal/config，确认新能力缺失导致失败。
- [x] 实现 StatusLineConfig/StatusItemConfig/ContextBarConfig 与默认值、校验和 raw JSON 合并，维持未知字段与写入失败保护；同步 setup 重配置保留字段。
- [x] 同命令通过；CLI show 在主流程接线后验证脱敏输出。

## 任务 2：独立缓存用量

- [x] 在模型 HTTP/流式 fixture 测试显式缓存零、未知、命中量与 prompt 子集、缺失尾帧；先确认失败。
- [x] model.Usage 加 CachedPromptTokens *int64；协议适配器归一化官方字段并保持 SDK 版本。分别测试 OpenAI 与 DeepSeek。
- [x] 执行 rtk go test ./internal/model/... ./internal/telemetry/...，校验 trace 不含原始响应与正文。

## 任务 3：工作目录环境快照

- [x] internal/app/workspace_test.go 覆盖临时 Git 仓库 unborn/分支/dirty/detached、非仓库、uv.lock/活动或仅存在 venv、超时及取消。
- [x] 实现 WorkspaceOptions/WorkspaceStatus 与 WatchWorkspace；采集在单独 goroutine 串行执行，只运行参数化只读 Git，结果有界、退出取消，无 TUI 文件访问。
- [x] 执行 rtk go test -race ./internal/app。

## 任务 4：统计、自适应行数渲染与接线

- [x] 先测试进度条已知/unknown/last/超限、字段优先级、禁用、14 字段选择、单行优先/两行/窄屏、会话清零和 usage 去重；估算器仅预留，未实现。
- [x] app 按模型调用更新 usage 快照和会话累计；TUI 消费结构化状态、独立时钟更新会话时长、保留 Thought 不重复及关键提示。
- [x] 修改 cmd/plume/chat.go 装配选中模型容量、状态栏配置与 workspace monitor；修改 config show 输出配置。输入框布局和真实光标按状态栏实际行数计算。
- [x] 执行 rtk go test ./internal/tui ./internal/app ./cmd/plume ./internal/config；不支持的容量/估算显示 unknown，不猜 token。

## 任务 5：审查和可复现交付

- [x] 对照契约逐项审查，再做代码质量审查，修复有证据的问题。
- [x] 全仓 rtk go test -race ./...、rtk go vet ./...、格式和 diff 检查；不无故重复已通过的验证。
- [x] 使用 scripts/build.sh 构建；隔离 PLUME_HOME、本地 HTTP 与 pty 演示默认/定制/窄屏/unknown/失败/取消；更新 scripts 演示与 docs/runbooks/tui.md。
- [x] scripts/build.sh --install 安装，升级实际默认配置；只验证新增字段与权限，不展示凭据。
- [x] 更新 AGENTS、技术契约、phase/决策/roadmap、daily 与 docs/reviews/G1b.4.md（pending-review），完成后停在本单元等待用户验收。

## 用户反馈修订（同一审核单元）

- [x] 标签亮青、数值浅青、未知提示黄；宽屏优先单行，放不下才两行，保留配置 max_rows 上限和优先级降级。
- [x] 取消问号条，区分 capacity unknown 与 usage unknown；已知容量的新会话显示 0%。
- [x] 核对 DeepSeek 官方两预设 1M，默认配置和旧 null 原子迁移写入 1000000，保留用户容量及自定义端点边界。
- [x] 修复 Thought 边界输入增高的布局收敛，规格及质量复核通过；最终 637 项竞态测试、9 个 PTY 场景及已安装真彩色单行验收通过。
- [x] 2026-10-09 修复颜色/环境/时钟先于窗口尺寸时提前消耗开屏；六种消息顺序回归及真实启动羽毛/圆角框断言通过，全仓竞态复验 644 项，重新安装并验证完整开屏和配色。
