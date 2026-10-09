---
title: 配置契约（schema v1）
status: active
updated: 2026-10-09
summary: 配置目录解析、原子持久化、凭据边界与 v1 字段总览；含 soul.md 人格默认配置
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
  soul.md              # setup 初始化的人格模板，0600；用户可编辑
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
| `agent.budget` | 模型/工具次数、单响应工具数、请求/参数/结果字节与整体/单步超时；完整默认落盘，零次数/整体时长表示不限 |
| `agent.soul` | enabled、path、max_bytes；默认开启、配置目录相对 soul.md、65536 字节；完整默认落盘 |
| `tools` | workspace、enabled、read_lines、search_results、max_file_bytes、max_output_bytes、shell；允许空许可列表关闭工具 |

易混点：`provider` 是品牌、`protocol` 是适配选择器、`model` 是发给供应商的 API 模型名——三者在 `internal/provider` 工厂里映射，配置 ID（`id`）不参与请求。

字段语义的现行契约：模型与协议见 [模型运行时决策](../decisions/0003-model-runtime.md)；状态栏与上下文/缓存统计见 [状态栏契约](tui/statusline.md)；文案与思考强度见 [流式契约](tui/streaming.md)。

状态栏完整默认配置的 `context` 项为 `enabled:true`、`context_format:bar`、`context_bar.show_percent:false`，显示进度条与当前输入/总容量；`cache_format:ratio` 只显示缓存命中百分比。用户可自定义显隐与格式，旧配置已有选择保留，不改变 usage 采集或缓存 CC/Pi 分母。

## 5. 开发工具与预算默认值（G3.1）

以下内容由 Save 或旧配置首次 Load 实际写入 config.json；不是只存在于 Go 结构默认值。setup 不新增问题，重跑保留用户设置。已存在的零、空数组及未知字段保持不变，null 与类型错误拒绝。

```json
{
  "agent": {
    "budget": {
      "model_calls": 0,
      "tool_calls": 0,
      "tool_calls_per_step": 64,
      "request_bytes": 8388608,
      "argument_bytes": 1048576,
      "result_bytes": 65536,
      "run_timeout_seconds": 0,
      "model_timeout_seconds": 1800,
      "tool_timeout_seconds": 180
    }
  },
  "tools": {
    "workspace": ".",
    "enabled": ["read", "grep", "glob", "edit", "write", "bash"],
    "read_lines": 200,
    "search_results": 100,
    "max_file_bytes": 10485760,
    "max_output_bytes": 32768,
    "shell": "bash"
  }
}
```

model_calls/tool_calls/run_timeout_seconds 的 0 表示无限；其他数值为正，result_bytes 最小 256，所有整数最多 2147483647。没有自动重试。bash 默认超时来自 tool_timeout_seconds，请求参数可缩短但不能绕过外层期限。workspace 相对路径从启动目录解析；shell 可为 PATH 内可执行文件名或绝对路径；实际命令不是文件工具的根目录沙箱。enabled 可加 calculate/current_time，也可显式设置 []。

工具参数、截断、先读后改及宿主执行局限见 [工具契约](agent/tools.md)。

## 6. 人格默认配置（G3.2）

以下默认值由 Save 和旧配置首次 Load 实际写入，不新增 setup 问题；已有 false、自定义值和未知字段原样保留。

```json
{
  "agent": {
    "soul": {
      "enabled": true,
      "path": "soul.md",
      "max_bytes": 65536
    }
  }
}
```

path 相对配置目录解析，支持 `~`、环境变量及绝对路径；与 `tools.workspace` 从启动目录解析的规则不同。path 原值及展开后必须非空且不含 Unicode 控制字符，max_bytes 范围1..8388608（最多8MiB）。agent.soul 对象及三个已知字段缺失/null 均补默认，类型错误拒绝；显式空路径/0 不被默认替代，按非法值拒绝。enabled:false 时不初始化或读取人格文件，仍保留 path/max_bytes 设置。

Load 只补配置，不创建 soul.md。setup 保存模型成功后以无覆盖原子发布初始化默认文件，新文件0600；已有文件（包括空文件）不覆盖。在线每 run preparing 读取一次，缺失/空白跳过，读取失败或超限在首次模型调用前失败；正文不出现在 config show 或 trace。完整文件行为、快照与执行边界见 [人格契约](agent/soul.md)。
