---
title: G4a.3.1 长期记忆账本契约
status: active
updated: 2026-10-24
summary: 私密历史库 v3 的显式记忆账本、版本幂等、作用域隔离及完整集合字节边界；尚未接入请求
---

# G4a.3.1 长期记忆账本契约

本契约描述已通过的 G4a.3.1 记忆账本，状态见 [审核记录](../../reviews/G4a.3.1.md)，后续范围见 [立项计划](../../plans/2026-10-23-g4a3-memory.md)。G4a.1/G4a.2 保持已通过；此处不代表后续单元获实施授权。

## 存储与身份

internal/store 的 LongTermMemoryStore 提供 PutMemory 和 ListMemories，SQLiteStore 与 MemoryStore 均实现。SQLite 使用现有私密 history.db、整库独占锁和权限治理，user_version=3；没有新数据库路径、依赖、CLI 指令或配置字段。内存实现仅为易失后端和契约验证，不会自动向现有聊天启用记忆。

MemoryScope 必须由可信调用方构造，ScopeKey 非空，global/workspace/session 显式选择：

- global：同一主体跨工作区；Workspace/SessionID 都为空。
- workspace：同主体的规范绝对 Workspace；SessionID 为空。
- session：同主体、工作区与已有会话元数据的精确 ID；不存在、其他主体或工作区均返回 ErrSessionNotFound。只有 binding 而尚未落元数据的空会话也拒绝。

账本校验绝对 clean 路径，不替入口解析符号链接；未来入口须沿用工具的真实规范根。它不是 OS 用户鉴权模块。身份各字段不能包含控制字符，ScopeKey 最多1024字节、Workspace4096、SessionID/Key256，字段精确区分大小写，不自动 trim、模糊合并或展开路径。

memory_entries 主键为 `(scope_key, scope_type, workspace, session_id, memory_key)`，该主键索引同时服务精确范围读取，不另建冗余索引。记录还含 content、source、revision、created_at、updated_at。content 必须非空白 UTF-8，source 只接受 user（显式提交）；没有模型提取、正文可信度推断或自动去重。

## 写入、版本与错误

MemoryWrite 的 ExpectedRevision=0 只创建，初版 revision=1；正数表示基于该版本更新。已有内容和来源相同，且 expected 等于当前版本或当前版本减一时返回原快照，时间和版本不变。内容改变只接受 expected=当前版本，版本加一；其他情况返回 ErrMemoryConflict，不覆盖旧值。不存在记录上的非零版本也冲突。不存在删除接口，因此不会发生删除后旧创建重投复活的问题。

创建和更新时间由存储层生成，UTC 毫秒；创建时间不变、更新时间不倒退。两个后端在同一事务/互斥锁内完成归属与版本检查，并发同一创建幂等，竞争版本更新只接受一个。

校验失败返回 ErrInvalidMemory/ErrMemoryLimit；磁盘内容违反契约返回 ErrCorruptMemory；数据库失败返回固定 ErrMemoryStorage，不包装可能包含正文的驱动报错。标准 context 取消/期限身份保留。读取有错误时结果必须为 nil。写入提交返回错误可能具有不确定结果，需要同域重新读取确认；不要把所有 IO 错误理解成确定回滚。

## 长度和读取

没有 token 估算器。单条上限8192字节，口径为全部文本字段 UTF-8 字节之和加 revision/两个时间共24字节。单次读取默认和硬上限100条、65536字节；MemoryLimits 零值用默认，正值只能收紧，负值或放宽上限返回 ErrMemoryLimit。

ListMemories 只查一个精确作用域，按键二进制升序返回全集。SQLite 在同一个读事务中先 COUNT/SUM（文本 CAST AS BLOB），再读取并校验；内存后端同锁计量。条数或字节超限整次拒绝，不使用 LIMIT，不返回前缀，不静默截断。中文、NUL 和元数据均不能绕过预算。修改已有条目前也先预检旧条目大小，不能靠覆盖掩盖损坏。

数值列必须实际为SQLite integer类型，DDL CHECK和正文读取前typeof校验同时约束；BLOB/文本/小数不能按固定24字节误计后加载。集合预检也检查单条最大载荷，坏条目不因集合仍小于64KiB而被提前读入。事务内取消即使转为ErrTxDone，也结合操作context保留取消/期限身份。

这是有界账本读取，尚未做按任务召回、全局/工作区/会话自动合并或 PromptBuilder 注入。现有 plume-workspace-v2、soul.md 每 run 快照和 tools 许可行为保持当前契约，记忆槽仍为空。不估算 token、不做自动压缩，不实现 MCP/skill。

## 迁移与隐私

v0/v1/v2 通过现有前向迁移链升到 v3；新增表、约束与 user_version 同事务，失败回滚。旧会话、run、turn、usage、binding 和 receipt 保留。更高版本继续拒绝；旧程序会拒绝 v3，回退旧程序须先停进程并恢复升级前备份，不能自动降级或删用户库。

复用新目录0700、库/辅助/锁文件0600、已有父目录权限不变及非 Unix 拒绝持久化约定。正文只进入私密历史库和授权读取结果，不进入日志/trace；不读凭据文件，不生成可猜测内容裸哈希。演示仅使用合成条目，记录固定操作名、状态和错误码，临时库结束删除。接口不能识别任意用户粘贴文本中的秘密；凭据不应被提交为记忆。

## 可复现闭环

```bash
rtk proxy go test ./internal/store -run '^TestLongTermMemory' -count=1
rtk proxy go run scripts/demo_g4a31_memory.go
```

演示在仓库下创建临时 PLUME_HOME，结束删除。`--output <新文件>` 保存0600脱敏 JSONL，拒绝覆盖已有证据。成功路径为创建→重投→更新→关闭重开核对；失败路径包括陈旧版本、写入超限、读取超限；另验证主体和会话隔离。没有运行模型、工具或真实配置。

目前只提供存储层与独立演示，TUI 中还没有 `/memory`，聊天不会自动写入/召回记忆；删除、管理收据与请求接入均为后续独立审核范围。
