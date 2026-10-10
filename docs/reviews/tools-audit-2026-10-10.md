---
title: tools 包自审（2026-10-10）
status: active
updated: 2026-10-10
summary: tools 规格与可维护性双轴审查：Shell 依赖、grep 截断与 glob 类边界修复
---

# tools 包自审（2026-10-10）

用户在通过 G3.2 后要求自查 tools 是否完善、注释是否足够。本次检查完整 `internal/tools`，以 `29d82f4` 为基线，结合 [工具契约](../specs/agent/tools.md) 和 AGENTS.md；规格与可维护性由两个独立审查任务检查，修复均先复现后实现。该自审不创建下一阶段，也不宣称实现“完美”。

工作区原有依赖升级、模型/测试与标准库整理另行保留；本次提交只纳入审查修复、注释和对应记录。

## Standards

- **P2 已修复：许可关闭后仍依赖 Shell。** `NewWorkspace` 曾无条件检查 shell 可执行路径；`enabled:[]`、仅 read 或仅 calculate 时也会被缺失的 bash 阻止启动。现在只有明确许可 bash 才检查 Shell。回归使用确实不存在的绝对路径，确认三类非 Shell 许可正常且 bash 许可仍拒绝。
- **已修正：过时的职责注释。** 包说明曾称“只读执行”、Definition 曾称“不提供脚本执行”，与 write/edit/bash 的实际能力矛盾。现已明确固定许可、文件工具的 os.Root 边界与宿主 Shell 权限。
- **已补齐：维护不变量。** 导出 API、Summary 不含正文、取消不回滚 Value、读取摘要的锁与生命周期、UTF-8/JSON 截断、Link/Rename 发布、durability_error 的 applied:true、FIFO 检查和进程组清理均有中文说明。注释解释边界和原因，不逐行复述代码。没有为代码气味启发式增加无实际需求的抽象。

此轴没有发现剩余应阻塞交付的标准问题。执行许可与序列化/日志职责未扩大。

## Spec

- **P2 已修复：grep 丢失真实匹配。** 长匹配行附带多行上下文时，原实现直接丢掉放不下的整条记录，可能返回成功、count:0、truncated:true。现在优先保留路径、行号和正文前缀，先移除较远上下文，再按转义后的完整 JSON 预算裁剪。覆盖默认、256 字节最小结果、较小输出和转义 UTF-8 场景；遍历结束后使用最终 skipped_files 再检查预算，必要时省略该非核心字段，缺失不表示 0。无匹配仍是明确的零结果。
- **P2 已修复：glob 字符类跨目录。** `a[!x]b` / `a[^x]b` 曾错误匹配 `a/b`，正字符范围 `[.-0]` 也可能隐含 `/`。现在从解析后的字符区间统一排除分隔符，保留现有单字符类、范围、否定类语义，不扩展 glob 语法。

这些修复对齐契约中的“成功但截断保留正文前缀及核心元数据”“常用 glob”和显式工具许可；未加入并行工具、自动重试或后台任务。

## 验证与复现

回归位于 `internal/tools/tools_review_test.go`、`internal/tools/workspace_search_review_test.go`。Shell 三个非许可场景和搜索边界已先观察失败，再确认修复后通过。复现使用临时工作区，未读取真实配置或调用供应商。

```bash
rtk go test -race ./internal/tools -count=1
rtk go test -race ./...
rtk go vet ./...
rtk proxy gofmt -l .
rtk git diff --check
```

最终验证针对 Git 暂存快照导出的代码（不含工作区另一批改动）：全仓竞态 **958 项、16 包通过**，工具包竞态 **65 项通过**，vet、格式与 diff 检查通过。最终测试记录见 [当日日志](../daily/2026-10-10.md)。当前审查只能证明覆盖场景的行为，不能由测试数量推出所有输入、操作系统和文件系统都正确。

## 仍有的边界

- bash 在宿主执行，环境键名过滤不能识别任意秘密，也不能阻止命令主动访问文件或网络。
- edit/write 最后摘要检查与 Rename 之间仍有外部写入竞争窗口；目录同步和硬链接支持依赖文件系统，取消不撤销已发生的副作用。
- Unix 清理普通进程组，主动脱离组的进程及非 Unix 清理不能作同等保证；Windows 尚未实测。
- read 极长单行只能返回前缀，next_offset 进入下一行；搜索不解析 .gitignore，不支持 brace expansion。
- 若纯路径、行号和固定结果字段本身超过配置结果预算，仍只能截断或省略该结果；缩小路径/范围或提高预算才能展示，不能承诺任意长度的元数据。
