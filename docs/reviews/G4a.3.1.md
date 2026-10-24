---
title: G4a.3.1 长期记忆账本审核
status: passed
updated: 2026-10-24
summary: G4a.3.1 账本已通过并获推送授权：双后端幂等/CAS、范围隔离、有界读取；尚未接入请求
---

# G4a.3.1 长期记忆账本审核

首轮按用户无人值守授权完成立项与首个可运行子单元，并仅本地提交为 `6ef124b`。2026-10-24 用户明确要求“提交并且推送这个”，本单元按审核制度记录为通过并获推送授权；随后只编写 TUI 指令显示与 skills 方案，待审核后实施。G4a.3.2/.3 不自动推进。基线为 G4a.2 已通过提交 `31c5f21`，不回退 G4a.1/G4a.2；指定计划日期为 2026-10-23，实际交付跨至 2026-10-24。

立项与分单元目标见 [计划](../plans/2026-10-23-g4a3-memory.md)，当前行为见 [契约](../specs/agent/memory.md)。本次最小闭环为存储层显式创建→重投→版本更新→关闭重开→精确作用域读取；通过独立脚本运行，还没有 TUI `/memory` 指令或聊天记忆召回。

## 改动与边界

- internal/store 增加 LongTermMemoryStore，SQLite 与内存后端实现同一 PutMemory/ListMemories 契约；global/workspace/session 都绑定非空可信主体，session 必须是同主体、工作区已有会话，pending 空绑定不自动建元数据。
- 稳定键精确去重，ExpectedRevision=0 只创建，正数 CAS 更新；同操作首次重投返回原快照，相同内容不更新时间。并发重复创建只产生一个版本，冲突更新不覆盖。
- user 来源与 UTF-8 正文显式校验；单条全部文本字段加24字节最多8192字节，完整读取默认/硬上限100条、65536字节，可以收紧。读事务先计量，再加载校验；超限 nil 结果，不用 LIMIT 或截断。
- SQLite user_version 2→3，与配置 schema v1 分开。新表/约束和版本同事务，复用主键索引、私密权限与整库锁；旧历史、用量未知值、绑定与收据保留。旧二进制拒绝 v3，回退须恢复升级前备份。
- 驱动错误统一固定类别，正文/键/凭据/内容裸哈希不进日志或 trace。没有新依赖、新包、配置字段或 plume 命令；PromptBuilder、soul.md 和工具运行边界保持原契约。
- 同步 AGENTS、上下文规划、配置契约、索引、阶段计划、每日流水与 ROADMAP 数据；G4a.3.1 已由 review 翻转为 passed，记忆管理入口与请求召回仍待另行授权。

## 复现与实际验证

环境：Go 1.27.1，darwin/arm64。以下命令均已实际执行；RTK proxy 原样转发命令，退出码均为0。

| 命令 | 实际结果 |
| --- | --- |
| `rtk proxy go build ./...` | 通过，仅编译校验，未发布/安装 |
| `rtk proxy go vet ./...` | 无输出 |
| `rtk proxy gofmt -l .` | 无输出 |
| `rtk proxy go tool golangci-lint run` | 0 issues |
| `rtk proxy go test ./...` | 17个包全部通过 |
| `rtk proxy go test -race ./...` | 17个包全部通过，无竞态报告 |
| `rtk proxy go test ./internal/store -run '^TestLongTermMemory' -count=1 -v` | 13条顶层测试、含子测试66项通过 |
| `rtk proxy go vet scripts/demo_g4a31_memory.go` | 无输出；脚本显式编译运行通过 |
| `rtk git diff --check` | 无输出 |
| 路线图静态范围检查 | 渲染代码与 HEAD 一致，ID唯一，G4a.3.1=review |
| 路线图浏览器检查 | 未完成：IAB不可用，浏览器列表为空；未将静态检查当作视觉验收 |

发布/安装仍须 scripts/build.sh，本次没有安装到 GOBIN，也不调用付费模型。普通测试与竞态的包结果不换算成性能或质量收益。

首轮验收输出和脱敏演示曾保存在 `docs/reviews/evidence/G4a.3.1/`，已按通过即删规则随通过提交删除；原产物可从提交 `6ef124b` 取回。2026-10-24 推送前重新执行普通与竞态测试，各 1704 项、17 个包通过，并将演示复跑到临时文件，仍为 9 步成功核查（含 3 项预期拒绝）。先前静态检查与专项验证记录保留于上表，不冒充本轮重新执行。

首轮测试先因缺少 MemoryScope 等实现编译失败（退出1）；实现后 SQLite 重投快照测试发现 UTC/本地时区表示不一致。根因是沿用历史 fromUnixMS 返回的本地时区；仅在记忆反序列化处固定 UTC 后，原测试及重开测试通过。未修改既有历史时间语义。

按 requesting-code-review 技能完成独立只读审查，发现并修复两项 Important，复核未发现遗留 Critical/Important/Minor：

1. SQLite INTEGER 亲和性允许BLOB/文本/小数，可能绕过按24字节计量的数值列预算。新增三列实际类型的DDL约束和**正文读取前** typeof 预检，List同时预检单条最大载荷；九组数值损坏反例在修复前失败，修复后读取/更新均于预检拒绝。
2. 事务内取消时 database/sql 自动回滚可先完成，使Commit返回ErrTxDone。错误转换现在结合操作ctx保留Canceled/DeadlineExceeded身份；用真实事务复现该分支，修复前失败、修复后通过，不据此宣称所有提交错误一定回滚。

关键反例由真实后端验证：

| 测试 | 守住的行为 |
| --- | --- |
| TestLongTermMemoryVersions / ConcurrencyAndCancellation | 创建/更新重投、陈旧冲突、并发单版本、取消不污染 |
| ScopeIsolation / LiteralIdentityAndPendingSession | 主体/工作区/会话精确隔离，global非公共，SQL字面量、空绑定拒绝 |
| RejectsInvalidWrites / ReadLimits / ByteBoundaries | 非法来源/UTF-8、字段/单条/集合边界，中文和NUL，超限零残片 |
| MigrationAndReopen | 真实v2旧库全类记录保留，记忆重开相同，库/锁0600 |
| MigrationRollback | 第一条DDL成功后注入SQL错误，新表和版本回滚；旧数据不变、锁释放 |
| StorageFailureKeepsData | 实际BEFORE UPDATE触发器拒绝，旧版本及成功历史保持，敏感驱动错误不回显 |
| CorruptRead | 外部修改正文/来源/版本后整次拒绝，即使含合法条目也不返回前缀 |
| NumericTypesPreflight / CancelledTransactionError | 大BLOB/文本/小数不绕过整数计量，事务取消的ErrTxDone仍保留标准取消/超时身份 |

## 成功与失败 trace 证据

复现保存到一个**新文件名**，脚本拒绝覆盖旧证据：

```bash
rtk proxy go run scripts/demo_g4a31_memory.go --output /tmp/plume-g4a31-demo-review.jsonl
```

首轮演示文件 `demo.jsonl` 为0600、19行，已随通过提交删除；复跑命令生成相同协议的新样本，不保证临时文件长期保留。成功和失败均来自真实 SQLite 操作与返回值断言，不是预先写好的日志：

- 成功：步骤1 create、2 idempotent_replay、3 update、7 owner_isolation、8 session_isolation、9 reopen_and_verify；共6项 completed。
- 预期失败：步骤4 stale_update → memory_conflict；步骤5 oversized_write、步骤6 bounded_read → memory_limit；共3项 rejected。每项都有 memory_operation_started / memory_operation_ended，起止9/9闭合。
- 末条 memory_demo_completed：steps=9，model_calls=0，tool_calls=0。末次重开核对原成功版本，证明前面拒绝未污染既有数据。

解析19行 JSONL 后检查正文哨兵 MEMORY-PRIVATE-SENTINEL 未出现，日志只含固定操作名、步骤、状态和错误码。脚本在仓库下创建临时 PLUME_HOME 与私密数据库，退出后删除，数据库及正文未作为证据提交。证据是账本演示生命周期，不冒充 Agent/model span。

首轮按用户要求保留非 TUI 成功/失败 trace。2026-10-24 已通过，两个待审产物随通过提交删除，复现脚本与正文摘要保留，遵循[证据治理](../runbooks/tui.md#证据治理通过即删)。历史取回：`git show 6ef124b:docs/reviews/evidence/G4a.3.1/demo.jsonl`。

## 指标、限制与待检查项

| 指标 | 实际绝对值 | 变化 |
| --- | --- | --- |
| 账本演示检查 | 9/9，含3项预期拒绝 | N/A |
| 账本测试 | 顶层13条，含子测试66项通过 | N/A |
| 演示生命周期闭合 | 起点9、终点9 | N/A |
| 演示模型/工具调用 | 0/0 | N/A |
| token、费用、召回质量、延迟分位数 | N/A，未测 | N/A |

账本不是完整的用户记忆体验：尚无删除/遗忘、公共指令、按任务相关性召回、来源注入、自动学习或配置开关，当前聊天不使用记忆。后续严格按 G4a.3.2/.3 独立授权；不做 MCP、skill、自动压缩或跨设备同步。Windows/Linux实机与终端视觉体验均未验证；本次无UI改动，不以Go竞态证明平台体验。

可信主体和真实规范工作区由入口保证，账本本身不是OS用户认证。正文不做加密、凭据检测或安全擦除；不能识别任意粘贴秘密。普通SQL失败通过真实触发器验证了回滚，但提交返回错误仍可能具有不确定结果，调用方须重新读取核对，不盲重试。

通过后的限制与后续范围：

- 首单元粒度为账本与独立演示；管理和请求召回留待后续，不因本次通过而上线。
- 作用域、稳定键、CAS 和100条/64KiB边界保持本契约。
- 迁移回滚限制、未完成的路线图浏览器视觉检查仍保留；自动测试不代替人工平台验收。
- 用户下一步只授权设计交互与 skills 文档；G4a.3.2/.3 未获实施授权。

首轮本地提交 `6ef124b`；用户已于 2026-10-24 追加推送授权。原未跟踪 PNG 不纳入提交，不改真实 ~/.plume。
