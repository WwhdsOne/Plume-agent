---
title: 文档索引
status: active
updated: 2026-10-09
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
│   └── tui/            TUI 契约
│       ├── keys.md        键位与平台适配（G1b.2.1）
│       ├── splash.md      开屏与主题色（G1b.2.2）
│       ├── streaming.md   流式输出与思考展示（G1b.3）
│       └── statusline.md  可配置底部状态栏（G1b.4）
├── plans/              计划——"将要做什么"
│   ├── phase-01-tui.md        第一阶段
│   ├── phase-02-channel.md    第二阶段（渠道网关，另行授权）
│   └── agent-context.md       G3 上下文与 tools 拼装规划
├── decisions/          决策记录（ADR，编号不复用）
│   ├── 0001-scope.md
│   ├── 0002-wechat.md
│   └── 0003-model-runtime.md
├── reviews/            每个审核单元的交付记录 Gx.md（+ 审核期证据）
├── runbooks/           操作手册（setup / tui）
├── daily/              每日变更流水
└── archive/            历史沉淀，不作为现行依据
    ├── project-audit-2026-10-06.md
    └── plans/          已完成的单元实施计划
```

## 2. 按意图找文档

| 我要… | 去看 |
| --- | --- |
| 知道现在该按什么实现 | `specs/` 下对应领域；模型与入口另见 `decisions/0003-model-runtime.md` |
| 知道下一步做什么 | `plans/phase-01-tui.md` + `roadmap.html` |
| 知道某个决定的理由 | `decisions/` |
| 知道某单元做了什么、限制是什么 | `reviews/Gx.md` |
| 复现/排查本地问题 | `runbooks/` |
| 回看某天发生了什么 | `daily/` |
| 找历史快照或已完成的计划 | `archive/` |

## 3. 状态速览（2026-10-09）

已通过：G0、G1a、G1b.1、G1b.2（.1/.2.1/.2.2）、G1b.3、G1b.4。
**当前无进行中单元**；下一单元 G3（自研工具循环与受控工具）需另行授权。

单一事实来源：单元状态只在 `reviews/Gx.md` 的 frontmatter 维护；`roadmap.html` 是可读快照，两者冲突时以 reviews 为准。`plans/` 与 `specs/` 不再重复记录单元状态。

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

- 描述"当前如何工作"的契约 → `specs/<领域>/`；TUI 之外的领域新建对应子目录，不要平铺在 `specs/` 根。
- 描述"将要做什么" → `plans/`；单元通过后把一过性实施计划移入 `archive/plans/`。
- 一次决策一条 → `decisions/`，编号递增、不复用。
- 一个审核单元一份 → `reviews/Gx.md`。
- 全部 Markdown 必须带 frontmatter（`title`/`status`/`updated`/`summary`），枚举见 AGENTS.md。
