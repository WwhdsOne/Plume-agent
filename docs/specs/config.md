---
title: 配置契约（schema v1）
status: active
updated: 2026-10-09
summary: 配置目录解析、目录布局、原子持久化、凭据边界与 v1 字段总览；配置目录默认 ~/.plume
---

# 配置契约（schema v1）

> 2026-10-09 从 AGENTS.md 抽出，作为配置类内容的单一来源；字段语义的详细契约在各领域 specs 文档中（见文末）。

## 1. 配置目录解析

`PLUME_HOME`（展开 `~` 与 `$VAR`）→ 否则 POSIX `~/.plume` → 否则 Windows `%LOCALAPPDATA%\plume`。

**不使用 `os.UserConfigDir()`**：macOS 上它落到 `~/Library/Application Support`（含空格，且与 Hermes/Codex 约定的家目录点目录不一致）。Hermes/Codex 分别用 `~/.hermes`(`HERMES_HOME`)、`~/.codex`(`CODEX_HOME`)。

目录布局：

```text
~/.plume/
  config.json          # 含 schema_version，非敏感
  credentials/         # 0700，文件 0600
  logs/                # 0700；setup.jsonl 等结构化 trace，文件 0600
```

开发与测试一律隔离，不要污染真实配置：测试用 `t.Setenv("PLUME_HOME", t.TempDir())`；手工试验用

```bash
PLUME_HOME=$(mktemp -d) go run ./cmd/plume config show
```

## 2. 凭据边界

`config.json` 只存 `api_key_ref` 这类引用，**永不出现密钥值**；密钥本身在 `credentials/`（目录 0700、文件 0600）。`plume config show` 只输出"已设置/未设置 (ref: …)"。token、二维码内容、登录 URL 同样不得进入日志或 trace。

`EnsureCredentialsDir` 在目录权限过宽时只**告警**，不静默收紧（`TestEnsureCredentialsDirWarnsButDoesNotTighten` 守住这条）。

## 3. 写配置的一致做法

`Save` 走 `writeFileAtomic`：同目录临时文件 → `Chmod` → `Write` → `Sync` → `Close` → `Rename` → fsync 目录。任何早期返回都 `defer os.Remove(tmp)`。**新增持久化不要直接用 `os.WriteFile` 覆盖目标文件**，否则会丢掉"写入失败保留原配置"这条保证（`TestSaveFailureKeepsExistingConfig` 守住）。

旧配置兼容原则：`Load` 首次读取时**原子补齐**缺失的默认值，保留用户自定义值与未知 JSON 字段，重复读取不改写文件。

## 4. 字段总览（schema v1）

顶层：

| 字段 | 说明 |
| --- | --- |
| `schema_version` | 当前 v1；语义变化需单独审核迁移，不静默改用户文件 |
| `default_model` | 裸 `plume` / `plume chat` 默认使用的配置 ID |
| `models[]` | 模型配置：`id`、`provider`（品牌）、`protocol`（历史适配选择器）、`base_url`、`model`（API 模型名）、`api_key_ref`、`reasoning_effort`、`context_window_tokens` |
| `tui.status_messages` | 四阶段（preparing/waiting/thinking/responding）文案 |
| `tui.status_line` | 底部状态栏：`items[]`、`row`、`priority`、`max_rows`、`separator`、`token_format`、`time_format`、`context_format`、`context_bar`、`cache_format`、`cache_scope`、`cache_bar`、`clock_refresh_ms`、`environment_refresh_ms`、`git_timeout_ms` |
| `channels[]` | 渠道记录：`type`、`enabled`、`credential_ref`、`settings` |

易混点：`provider` 是品牌、`protocol` 是适配选择器、`model` 是发给供应商的 API 模型名——三者在 `internal/provider` 工厂里映射，配置 ID（`id`）不参与请求。

字段语义的现行契约：模型与协议见 [模型运行时决策](../decisions/0003-model-runtime.md)；状态栏与上下文/缓存统计见 [状态栏契约](tui/statusline.md)；文案与思考强度见 [流式契约](tui/streaming.md)。
