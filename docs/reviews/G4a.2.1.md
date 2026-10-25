---
title: G4a.2.1 对话检查点与中断恢复审核
status: passed
updated: 2026-10-25
summary: G4a.2.1 按用户授权代理验收通过：修复恢复与确认边界，普通/竞态各1810项、PTY 19/19通过
---

# G4a.2.1 对话检查点与中断恢复审核

2026-10-24 按[已通过方案](../plans/2026-10-24-tui-recovery.md)交付；2026-10-25 用户授权“进行验收，合格后提交并推送”。本轮以 `855e544` 为基线，对工作区全部实现（含新增文件）进行规范与方案双轴审查、反例修复和正式复验，结论为通过。通过依据是本轮实际执行结果，不将方案通过或前一轮测试当作实现验收。

## 交付行为

- store：`run_checkpoints` 每 run 最新快照；SQLite v3→v4 前向迁移，DDL/版本同事务，旧历史和记忆保留。序号递增、同载荷幂等、冲突与迟到拒绝；正常终态与成功 turns/检查点清理同事务，interrupted 保留例外。8MiB/10000 边界、读取前大小预检、损坏/未来版本整体拒绝，内存后端同契约。
- app：run 内单协调器，正文/Thought 首片段立即写，此后有新事件且满1秒时合并；工具准备确认后才放行副作用，完成摘要同步保存。写失败锁存停止，确认未知核对同序号同载荷；取消先更新内存工具状态，收尾按原期限刷盘。提交未知同时核对终态、成功 turns 与检查点清理。
- 恢复：`-c`、`/resume` 与选择器共用展示转换，所有仍保存的中断检查点均可见，后续任务不遮住旧片段；合并总量按调用方限额收紧，精确核对归属。模型仍只收到完整成功 turns，恢复和回填都不请求模型或执行工具。
- TUI：最后 run 未完成才提供一个 Up 候选；候选不自动填框，Down 返回原草稿，Enter 创建新 run。净化并去掉首尾空白后仅转义整段开头的 `/`，行中及后续行不变形；最终4096字符上限，超限保留草稿/游标。未配对 running 工具标记结果未确认，存在工具记录时提醒先核对已有结果。
- CLI：正常退出后输出会话、保存结论和恢复方法；原子快照绑定当前会话及最后登记 run，身份变化或收尾超时为 unknown，空会话/offline/禁历史不承诺持久恢复。无新增依赖、配置字段或 plume 指令。

## 双轴审查与修复

### 规范轴

| 已确认问题 | 修复与反例 |
| --- | --- |
| LatestCheckpoint 先读正文后预检；SaveCheckpoint 比较也整体读取旧载荷 | 长度通过后才读取；幂等比较留在 SQLite 内。16MiB 外部损坏载荷的反例原先分配约32MiB，修后在8MiB余量内拒绝；普通与竞态均验证 |
| 历史回填扫描行中指令造成正文变形，空白首部路径又未转义 | 与真实发送入口对齐，仅转义净化/去空白后的顶层首字符；ASCII/Tab/Unicode空白、多行、已知指令和4096边界反例先红后绿 |
| 会话切换和 `-c` 配置完成后退出快照未及时更新 | hasRun/身份来自新会话后再刷新；覆盖有run→新空会话、空→恢复、有历史启动后不发新任务直接退出 |
| 冻结摘要后身份变化或原子状态缺失可误报 saved | 先核对会话与run身份，缺失/不匹配返回unknown；四组反例先红后绿 |

修复后未见新增包边界、私密正文进入 trace、资源所有权或无界队列的阻塞问题。转录类型映射的少量重复属于非阻塞建议，本单元未扩展重构。

### 方案轴

| 已确认问题 | 修复与反例 |
| --- | --- |
| 新任务后重开会遮住旧 interrupted 检查点 | 逐run构造有界展示副本；真实SQLite跨任务与两恢复入口验证旧正文/Thought/工具可见，成功尾无旧候选 |
| 提交未知仅核对终态和turns，没有核对检查点清理 | GetRunState 在同事务/同锁拒绝非interrupted终态残留检查点；真实SQLite触发器故障+丢确认反例验证unknown及冻结 |
| 取消中的工具完成回调未更新内存快照，收尾可能刷旧running | 先更新内存，跳过已取消ctx写库；工具返回时取消且终态保存失败的反例直接验证检查点保留真实终态/摘要 |
| 既有PTY脚本断言v2，默认fixture异常却未验证成功回复 | 更新v4断言，默认fixture返回完整成功流，字面斜杠场景必须两个completed及两条turns；新增三项退出/失败场景 |

上述问题均先用失败反例确认，再修复验证；无以“已知边界”替代核心验收要求的遗留阻塞项。

## 实际验证

环境：Go 1.27.1，darwin/arm64；本地HTTP fixture、隔离 PLUME_HOME、临时私密库，未联网调用真实模型。

| 命令/检查 | 2026-10-25 结果 |
| --- | --- |
| `rtk go test -count=1 ./...` | 17包，1810项通过、1项真实探测跳过 |
| `rtk go test -race -count=1 ./...` | 同上，无竞态报告 |
| `rtk go build ./...` / `rtk go vet ./...` | 退出码0，vet无输出 |
| `rtk proxy go tool golangci-lint run` | 0 issues |
| `rtk proxy gofmt -l .` / `rtk git diff --check` | 无输出 |
| `rtk proxy ./scripts/build.sh` | 成功；v0.1.0 / 855e544-dirty / 2026-10-25T04:07:20Z |
| G4a.2.1 PTY | 7/7通过 |
| G4a.1/G4a.2 PTY回归 | 各6/6通过，共19/19 |

测试以 `-json` 收集并按终态统计，计数包含子测试；跳过项为 `TestLiveProbeDeepseek`，不将本地fixture结果当成真实供应商验证。

| G4a.2.1 场景 | 实际核查 |
| --- | --- |
| cancel-exit-resume | cancelled/saved；重开与Up回填无额外请求 |
| kill-midstream | 确认序号3检查点后SIGKILL；重开interrupted，保留22字符正文与工具状态，turns=0 |
| slash-literal-backfill | 字面 `/exit` 回填重发仍为普通消息，两个completed、两条成功turns |
| empty-exit-no-summary | 无持久恢复摘要，不新增run |
| 断流失败后恢复 | failed/stream_interrupted，turns=0；重开/回填零新增请求 |
| 运行中 `/exit` | 1000ms总期限配置，实际50.05ms退出，按落盘终态/保存结论核对；重开零请求 |
| sigterm-exit-resume | 以实际终态核对；本轮cancelled，退出码0，实际50.02ms，重开一致，turns=0 |

成功 trace：`model_completed` → `run_ended(completed,saved)`；失败 trace：`model_failed(stream_interrupted)` → `run_ended(failed,saved)`；取消同样分别记录cancelled与saved。G4a.2.1共7个run_started、6个run_ended，8个model_start、3个model_completed、4个model_failed、1组tool_started/tool_completed；少的一条run/model终态来自刻意SIGKILL，不能称崩溃下trace完全闭合。库中completed=2、cancelled=3、failed=1、interrupted=1，成功turns=2、仅中断任务检查点保留1条。

恢复阶段额外模型/工具调用为0（脚本核对本地fixture计数）；上述8次模型、1次工具来自明确发送的测试任务，不是恢复副作用。G4a.2回归的offline fake另有7个model_start和6个工具事件，与在线fixture分开统计。已知测试正文/凭据的trace扫描无命中，不推导为任意数据下的全面脱敏证明。

## 复现与证据治理

```bash
rtk go test -count=1 ./...
rtk go test -race -count=1 ./...
rtk go vet ./...
rtk proxy go tool golangci-lint run
rtk proxy gofmt -l .
rtk proxy ./scripts/build.sh
rtk proxy python3 -m pip install --target /tmp/plume-g4a21-pty-deps pyte==0.8.2 wcwidth==0.2.13
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-g4a21-pty-deps python3 scripts/pty_demo_g4a21.py --output /tmp/plume-g4a21-pty
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-g4a21-pty-deps python3 scripts/pty_demo_g4a1.py --output /tmp/plume-g4a1-pty
rtk proxy env PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=/tmp/plume-g4a21-pty-deps python3 scripts/pty_demo_g4a2.py --output /tmp/plume-g4a2-pty
```

本轮最终结果位于 `/tmp/plume-pty-final-20261025T040720/`，含各组results.json/run.log、manifest.json及verification-summary.json；临时文件不保证长期保留。冻结验收二进制SHA256为 `0c0b389bb9b28bab4898193f01e7728737e99195d9711b1d0a943a44c97c9721`。原待审目录 `docs/reviews/evidence/G4a.2.1/` 按[通过即删规则](../runbooks/tui.md#证据治理通过即删)随本提交删除；该目录原未提交，无Git历史副本，复现入口是仓库脚本与正式测试。

## 指标与局限

基线阶段只报绝对值，变化均N/A：PTY19/19，恢复额外模型/工具调用0；退出计时为本地单样本，检查点合并写次数由定向测试验证，真实供应商延迟、费用、性能分位数未测，记N/A。

- 合并写由事件驱动，静默期不会另写；强退可能丢失最后未确认片段，不能续接原生成。
- 检查点只作展示；重提是新任务，可能再次产生副作用，历史Shell子进程不会自动接管或回滚。
- `-c`找当前工作区最近会话；精确恢复用完整ID的 `/resume`，未发送草稿不恢复。
- v4不能原地降级；旧程序拒绝打开，回退需升级前备份。
- 真实关终端/SIGHUP、kitty手感、Windows/Linux及真实模型仍为N/A；darwin本地PTY通过不能替代这些验证。

本单元验收通过并提交推送后停止。下一步按用户既定顺序先写 TUI 对话框/指令显示与 skills 设计文档，待审核后再实施；不自动推进记忆管理/召回、渠道或MCP。
