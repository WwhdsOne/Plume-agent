---
title: 文档索引
status: active
updated: 2026-10-24
summary: docs 结构的唯一入口：按意图分类的索引、当前状态速览、旧路径映射表与新增文档规则
---

# 文档索引

本文件是 `docs/` 的唯一入口。AGENTS.md 只管约定与制度，不再维护文档地图；新增文档时更新本文件的索引表，不要回填 AGENTS.md。

## 1. 目录结构

```text
docs/
├── index.md            本文件：唯一入口
├── roadmap.html        树状路线图（全部单元的状态快照）
├── specs/              现行契约——"现在怎么工作"
│   ├── config.md       配置 schema v1、目录解析、凭据与原子写入
│   ├── metrics.md      指标与比较协议（全阶段共用）
│   ├── session-commands.md  G4a.2 会话历史、公共指令与入口边界
│   ├── agent/          Agent 契约
│   │   ├── tools.md   G3 受控工具循环契约
│   │   ├── soul.md    G3.2 人格初始化与拼装契约
│   │   └── memory.md  G4a.3.1 私密记忆账本契约
│   └── tui/            TUI 契约
│       ├── keys.md        键位与平台适配（G1b.2.1）
│       ├── splash.md      开屏与主题色（G1b.2.2）
│       ├── streaming.md   流式输出与思考展示（G1b.3）
│       └── statusline.md  可配置底部状态栏（G1b.4）
├── plans/              计划——"将要做什么"
│   ├── phase-01-tui.md        第一阶段
│   ├── phase-02-channel.md    第二阶段（渠道网关，另行授权）
│   ├── agent-context.md       Agent 上下文与 tools 拼装规划
│   ├── 2026-10-10-session-history-commands.md  历史会话与统一指令设计
│   ├── 2026-10-23-g4a3-memory.md             长期记忆立项与分单元计划
│   └── 2026-10-12-g4a2-implementation.md      G4a.2 当前实施与验收步骤
├── decisions/          决策记录（ADR，编号不复用）
│   ├── 0001-scope.md
│   ├── 0002-wechat.md
│   └── 0003-model-runtime.md
├── reviews/            每个审核单元的交付记录 Gx.md（+ 审核期证据）
├── runbooks/           操作手册（setup / tui）
├── daily/              每日变更流水
└── archive/            历史沉淀，不作为现行依据
    ├── project-audit-2026-10-06.md
    └── plans/          已完成的单元实施计划（含 G3/G3.1/G3.2）
```

## 2. 按意图找文档

| 我要… | 去看 |
| --- | --- |
| 知道现在该按什么实现 | `specs/` 下对应领域；模型与入口另见 `decisions/0003-model-runtime.md` |
| 知道下一步做什么 | `plans/phase-01-tui.md` + `roadmap.html` |
| 知道某个决定的理由 | `decisions/` |
| 知道某单元做了什么、限制是什么 | `reviews/Gx.md` |
| 检查工具实现与注释的自审结论 | [tools 自审](reviews/tools-audit-2026-10-10.md) |
| 查看 G4a.1 实现复核与补充反例 | [G4a.1 独立复核](reviews/G4a.1-audit-2026-10-11.md) |
| 查看 G4a.1 缺陷修复与子 agent 复审 | [G4a.1 修复复审](reviews/G4a.1-fixes-2026-10-11.md) |
| 复现/排查本地问题 | `runbooks/` |
| 修改人格或关闭 soul.md | [人格契约](specs/agent/soul.md) + [setup 手册](runbooks/setup.md) |
| 审核历史保存、`-c`、`/resume` 及跨入口指令方案 | [历史会话与统一指令方案](plans/2026-10-10-session-history-commands.md)；G4a.1 交付状态见 [审核记录](reviews/G4a.1.md) |
| 使用公共指令、历史选择与只读分页 | [会话指令契约](specs/session-commands.md) + [TUI 手册](runbooks/tui.md)；G4a.2 交付与验证见 [审核记录](reviews/G4a.2.md) |
| 审核长期记忆的范围、迁移与首个闭环 | [立项计划](plans/2026-10-23-g4a3-memory.md)、[账本契约](specs/agent/memory.md)、[G4a.3.1 审核](reviews/G4a.3.1.md)；尚未接入聊天请求 |
| 回看某天发生了什么 | `daily/` |
| 找历史快照或已完成的计划 | `archive/` |

## 3. 状态速览（2026-10-24）

已通过：G0、G1a、G1b.1、G1b.2（.1/.2.1/.2.2）、G1b.3、G1b.4。
**G3/G3.1 于 2026-10-09 审核通过**，见 [循环审核](reviews/G3.md) 与 [开发工具审核](reviews/G3.1.md)。已完成计划归档为 [G3 初始计划](archive/plans/2026-10-09-g3-tools.md)、[G3.1 开发工具计划](archive/plans/2026-10-09-g3-workspace-tools.md)，现行契约为 `specs/agent/tools.md`；状态栏最新定制恢复 ctx 进度条加当前/总计，cache 仅百分比。审核窗口证据已按 runbook 删除，重跑方法保留在审核记录。后续阶段仍待另行授权。

**G3.2 人格初始化与拼装于 2026-10-10 审核通过**，见 [审核记录](reviews/G3.2.md)。默认配置、文件与每 run 快照行为见 [现行契约](specs/agent/soul.md)，完成步骤归档为 [实施计划](archive/plans/2026-10-09-soul-context.md)。审核窗口证据已删除，可从提交 29d82f4 或复现脚本取回；不推进记忆、MCP、skill 或渠道。

**G4a.1 持久历史与显式继续含全部修复于 2026-10-12 审核通过并提交推送 `97568ec`**，见 [审核记录](reviews/G4a.1.md)：SQLite 持久层（`internal/store`）、`plume -c` 恢复最近会话、Ctrl+N 新会话、崩溃中断标记与整库进程锁；复现脚本 `scripts/pty_demo_g4a1.py`。审核窗口证据按 runbook 随通过提交删除，原始数值与复现命令保留在审核记录及 [修复复审](reviews/G4a.1-fixes-2026-10-11.md)。

**G4a.2 公共指令与 TUI 历史选择于 2026-10-23 通过并获提交授权**：公共路由、store v2 管理事务、TUI 补全/选择/预览和 fake gateway 已交付；全仓普通/竞态各 1638 项、两组共 12 个 PTY 场景通过。当前行为见 [契约](specs/session-commands.md)，完成步骤见 [实施计划](plans/2026-10-12-g4a2-implementation.md)，结果与限制见 [审核记录](reviews/G4a.2.md)，收尾与修复见 [2026-10-23 流水](daily/2026-10-23.md)。一次性审核证据按 runbook 随通过提交删除，复现脚本保留。后续明确授权的长期记忆见下一段；真实 gateway、压缩和 MCP/skill 加载仍未启动。

**G4a.3.1 长期记忆账本于 2026-10-24 交付待审**：剩余长期记忆范围已经[立项](plans/2026-10-23-g4a3-memory.md)，首单元交付私密历史库 v3 的显式账本、CAS/幂等、作用域隔离、有界读取与可运行演示，见 [契约](specs/agent/memory.md) 和 [审核](reviews/G4a.3.1.md)。没有管理指令、自动学习或请求召回，G4a.3.2 起待单独授权；MCP/skill/压缩仍未实现。

单一事实来源：已交付单元状态只在 `reviews/Gx.md` 的 frontmatter 维护；`roadmap.html` 是可读快照，两者冲突时以 reviews 为准。未开工提案在 `plans/` 中明确待批准，不为它虚建实现审核记录；`specs/` 仅描述现行行为。

## 4. 旧路径映射（2026-10-09 重组）

| 旧路径 | 新路径 |
| --- | --- |
| `docs/tui-keys.md` | `docs/specs/tui/keys.md` |
| `docs/tui-splash.md` | `docs/specs/tui/splash.md` |
| `docs/tui-streaming.md` | `docs/specs/tui/streaming.md` |
| `docs/tui-statusline.md` | `docs/specs/tui/statusline.md` |
| `docs/phase-01-tui-agent.md` | `docs/plans/phase-01-tui.md` |
| `docs/phase-02-channel-gateway.md` | `docs/plans/phase-02-channel.md` |
| `docs/agent-context.md` | `docs/plans/agent-context.md` |
| `docs/reviews/project-audit-2026-10-06.md` | `docs/archive/project-audit-2026-10-06.md` |
| `docs/superpowers/plans/*.md` | `docs/archive/plans/*.md` |
| 阶段计划 §7（指标协议） | `docs/specs/metrics.md` |

`docs/daily/` 是流水，保留当时的路径写法，不随重组回改；查旧文档时用本表。

## 5. 新增文档放哪

- 描述"当前如何工作"的契约 → `specs/<领域>/`；跨模块的配置、指标、会话与公共指令契约放在 `specs/` 根，单一领域的新契约放对应子目录。
- 描述"将要做什么" → `plans/`；单元通过后把一过性实施计划移入 `archive/plans/`。
- 一次决策一条 → `decisions/`，编号递增、不复用。
- 一个审核单元一份 → `reviews/Gx.md`。
- 全部 Markdown 必须带 frontmatter（`title`/`status`/`updated`/`summary`），枚举见 AGENTS.md。
