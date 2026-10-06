# 0001 范围、供应商预设与配置边界（G0）

- 状态：已确认（2026-10-06 G0 通过），作为 G1a/G1b 的输入
- 用户已确认：目录用 `~/.herald/`；首批只启用 2 个预设（DeepSeek + 自定义兼容服务，百炼/Qwen 延后）；单次联网评测无费用上限；本机（含 TUN 代理）可用于评测
- 日期：2026-10-06
- 关联：`docs/phase-01-weixin-agent.md`（阶段计划）、`docs/decisions/0002-wechat.md`（微信接入）
- 复核口径：所有 Base URL 于 2026-10-06 现场复核，来源见下表；Eino 适配路径以 `cloudwego/eino-ext` 仓库 `main` 的目录清单为准，具体版本在 G1b 编译验证后锁定。

## 0. 本决策回答什么

G0 需要冻结四件事，作为 G1a/G1b 的输入：

1. 首批模型供应商预设（协议类型、默认 Base URL、凭据要求、可用的 Eino 适配路径）。
2. 配置与凭据边界（保存位置、字段、权限、原子性）。
3. 评测预算口径（哪些离线、联网评测如何计量）。
4. 运行环境与本机出站网络。

未在本文件冻结的内容一律视为“待定”，不得在代码里写死。

## 1. 首批模型供应商预设

预设是产品概念、必须保留：每个供应商有自己的品牌、默认 Base URL、凭据要求和 Eino 组件。不做“把所有供应商折叠成同一个 `openai` 组件、只换 Base URL”的处理——那样既不能暴露各家特有能力，也无法诚实声明支持范围。

**已确认（2026-10-06）：首批只启用 2 个预设，各自绑定自己的 Eino 组件；`protocol` 保存适配器 ID。百炼/Qwen 及其他供应商本次明确不做，后续再补。**

| 预设 ID | 显示名称 | `protocol`（适配器 ID） | Eino 组件 | 默认 Base URL（可编辑） | 凭据要求 |
| --- | --- | --- | --- | --- | --- |
| `deepseek` | DeepSeek | `deepseek` | `components/model/deepseek` | `https://api.deepseek.com` | API Key + 用户输入模型 ID |
| `custom-openai` | 自定义兼容服务 | `openai-compatible` | `components/model/openai` | 无预填值，由用户提供 | Base URL + 模型 ID；默认要求 API Key，本地无鉴权服务可显式选择“无需 Key” |

`custom-openai` 是无预填值的通用逃生通道：不新增预设也能接其他 OpenAI 兼容服务或本机服务。代价是工厂多一个分支、向导多一条路径，需与 `deepseek` 分别测试。

### 已核实的组件事实（2026-10-06，读各组件 README）

| 组件 | 默认/示例 BaseURL | 特有配置面 | 首批 |
| --- | --- | --- | --- |
| `components/model/deepseek` | 默认 `https://api.deepseek.com/`，path 默认 `chat/completions` | `GetReasoningContent`、前缀续写（`/beta`）、`ResponseFormatType`、`LogProbs`/`TopLogProbs` | ✅ |
| `components/model/openai` | 无默认，用户填 | 通用 OpenAI 兼容配置面 | ✅ |
| `components/model/qwen` | BaseURL 必填，官方示例 `https://dashscope.aliyuncs.com/compatible-mode/v1` | `EnableThinking`、`Seed`、`LogitBias`、`User` | ⏸ 延后 |

**向导的“预填/可编辑”逻辑要兼容两种形态：** `deepseek` 组件有默认 BaseURL（不填即用默认），`openai` 组件**必须**用户提供 BaseURL。

**诚实结论（首批两家线协议都是 OpenAI 兼容 HTTP）：** 差异在**适配器**与其暴露的供应商特有配置面。区分点是适配器，不是“换 Base URL”——不能仅凭能换地址就宣称支持任意供应商。未来引入真正非 OpenAI 兼容的原生协议供应商时，新增适配器 + 工厂即可，不改向导主流程。

### 来源与复核

| 预设/组件 | 来源 | 复核结论 |
| --- | --- | --- |
| DeepSeek | https://api-docs.deepseek.com/ | 文档标明 OpenAI 兼容，`base_url (OpenAI) = https://api.deepseek.com`（另有 Anthropic 端点 `/anthropic`，首版不用） |
| 组件事实 | `cloudwego/eino-ext` 各组件 README（`main`） | 见上「已核实的组件事实」 |

### 后续补充（本阶段明确不做，先保留已核实资料）

**百炼（阿里云 Model Studio）/ Qwen 适配器**：延后，理由——首批只做 DeepSeek 与自定义兼容，缩小验证面。纳入时用 `components/model/qwen`（BaseURL 必填）。

- 更正记录：`qwen` 组件**不是** DashScope 原生 `/api/v1`；其 README 示例 BaseURL 即 `.../compatible-mode/v1`，且注明参数「correspond to OpenAI's chat completion API parameters」、`ResponseFormat` 直接用 `openai.ChatCompletionResponseFormat`。（2026-10-06 修正，曾写错。）
- 地域/业务空间域清单（纳入时使用；同一地址不适用于全部账号，Key 按区域、混用返回 401）：
  - 共享 DashScope 域：北京 `dashscope.aliyuncs.com`、新加坡 `dashscope-intl.aliyuncs.com`、美国弗吉尼亚 `dashscope-us.aliyuncs.com`、中国香港 `cn-hongkong.dashscope.aliyuncs.com`，路径均为 `/compatible-mode/v1`。
  - 业务空间专用域：`https://{WorkspaceId}.{region}.maas.aliyuncs.com/compatible-mode/v1`。
  - 试用域：`https://trial.{region}.maas.aliyuncs.com/compatible-mode/v1`。
  - 来源：https://help.aliyun.com/en/model-studio/base-url （页面更新于 2026-09-16）。
- 切换地域/业务空间时按切换供应商处理：重新应用默认值并清空旧 Key 引用，不复用上一家的凭据。

### 模型 ID

两家都由用户输入模型 ID，不依赖模型列表 API（可能不可用）。示例（**不写死**）：DeepSeek 文档当前列出 `deepseek-flash` 与 `deepseek-v4-pro`，旧名 `deepseek-v4-flash*` 仍被接受但已退役。模型 ID 属于用户配置，进入实验 manifest，但不算协议常量。

### 适配器边界与能力记录

- 首批只用 `components/model/deepseek` 与 `components/model/openai`；`eino-ext` 里的 `qwen`、`ark`、`claude`、`gemini`、`ollama`、`openrouter`、`qianfan`、`agentic*` 等本阶段都不纳入。
- 每个预设的能力（流式、工具调用、usage、推理内容）分别标记“支持/不支持/未验证”，在 G1b 用 mock HTTP 逐项验证；能普通对话不等于通过工具调用验收。
- 组件版本在 G1b 编译验证后锁定并写入 `go.mod`。

## 2. 配置与凭据边界

### 2.1 目录位置（已确认）

采用 Hermes/Codex 式的**单一家目录点目录**，不使用 `os.UserConfigDir()`（macOS 上会落到含空格的 `~/Library/Application Support`）：

解析顺序：

1. 环境变量 `HERALD_HOME` 非空 → 用它（展开 `~` 与 `$VAR`）。
2. 否则 POSIX：`~/.herald`。
3. 否则 Windows：`%LOCALAPPDATA%\herald`（无 `LOCALAPPDATA` 时回退 `%USERPROFILE%\AppData\Local\herald`）。

对齐依据：Hermes 默认 `~/.hermes`（`HERMES_HOME` 覆盖，见其 `hermes_constants.py:_get_platform_default_hermes_home()`），Codex 默认 `~/.codex`（`CODEX_HOME` 覆盖）。命名用 `.herald` 与命令名 `herald` 一致。

目录布局（全部内容集中在此，便于备份与清理）：

```text
~/.herald/
  config.json          # 非敏感配置，含 schema_version
  credentials/         # 0700
    <...>              # 凭据文件 0600
  # 后续：SQLite 运行数据、同步游标、context_token 等也在此目录下
```

- 非敏感配置：带 `schema_version` 的 `config.json`。
- 敏感凭据：同目录 `credentials/`，目录权限 `0700`、文件权限 `0600`（类 Unix）；明文 Key 不混入普通配置、不进入日志与 trace。
- 后续允许用命令行标志显式指定目录（覆盖 `HERALD_HOME`）；首版只实现环境变量与默认值。
- 首次运行创建目录时按上述权限设置；已存在但权限过宽时告警，不静默放宽。

字段（首版）：

- 模型配置：`id`、`provider`（预设 ID）、`protocol`（Eino 适配器 ID，首批取值 `deepseek` / `openai-compatible`）、`base_url`、`model`、`api_key_ref`；顶层 `default_model` 引用一个配置 ID。首版向导先建一个默认配置，结构允许以后新增命名配置。`protocol` 与 `provider` 分开存，便于同一供应商将来切换适配器而不改品牌。
- 渠道配置：`id`、`type`、`enabled`、`model_ref`、`credential_ref` 及渠道专有设置；首版启用一个微信实例，`model_ref` 默认指向默认模型。
- 通用调度只读稳定 ID 与模型引用；微信专有字段（如 `context_token`）由微信适配器自管。

规则：

- 原子写入；取消或写入失败保留原配置。渠道配置失败不回滚已保存的模型配置；模型配置更新失败不覆盖原有效配置。
- 状态输出只显示“已设置/未设置”与脱敏标识，不回显 Key。
- 默认 HTTPS；本机回环服务允许 HTTP。
- API Key 隐藏输入，不作为命令行参数传递（避免进入 shell 历史或进程列表）。
- 模型配置变更在网关重启后生效，首版不做热切换。

## 3. 评测预算（已确认：无上限）

用户于 2026-10-06 确认：**单次联网评测不设费用上限**（理由是评测就是请求 API，成本可控、可归因）。

| 项 | 首版口径 |
| --- | --- |
| 离线评测（G0/G1a/G1b/回归） | 零模型费用。G1b 的 12 个种子用例与 G1a 向导测试全部用 scripted fake / fixture。 |
| 可选连通性检查（G1b） | 单次一个短请求；用户显式选择才发起，说明可能产生少量费用。 |
| 联网批量评测 | **无费用上限**，可运行。仍按 §3 的审核单元逐个显式发起（保持可归因与可复现），不因“无上限”自动开跑或与离线回归混跑。 |
| 真实模型重复次数 | 按 §7 协议默认每例 3 次。 |
| 费用计量 | 报告注明币种、价格日期、缓存计价；usage 缺失标 `unknown`，估算值带 estimator/version，不当作计费真值。 |
| 单价上限 | 无（用户确认）。 |

**保留的不变量：** `无上限 ≠ 无约束`。费用仍须逐实验计量并写入 `eval/reports/<experiment-id>/manifest.json`；报告必须能让每个数字回溯到实验报告。若某次实验消耗异常（例如单实验显著超出同类基线），在单元审核中说明原因，不静默继续。

## 4. 运行环境与本机出站网络

本机实测（2026-10-06）：

| 项 | 值 |
| --- | --- |
| 平台 | darwin / arm64 |
| Go | go1.27.1（与 `go.mod` 一致） |
| GOPROXY | `https://goproxy.cn,direct` |
| HTTP 代理环境变量 | 无（`http_proxy`/`https_proxy`/`no_proxy` 均未设置） |

出站连通性实测（`curl -m 8`，2026-10-06）：

| 目标 | 结果 |
| --- | --- |
| `ilinkai.weixin.qq.com` | HTTP 404 可达，TLS 校验通过（`ssl_verify_result=0`） |
| `api.deepseek.com` | HTTP 401 可达（未带 Key），TLS 通过 |
| `dashscope.aliyuncs.com` | HTTP 404 可达，TLS 通过（百炼，延后，仅记录网络事实） |
| `proxy.golang.org` | HTTP 200 |
| `goproxy.cn` | HTTP 200 |

观察与风险：

- 五个域名都解析到 `198.18.x.x`（`198.18.0.0/15`）。这是 TUN/fake-IP 式本地代理的典型表现：DNS 被代理接管，但连接与 TLS 正常。
- 风险：该模式下长连接可能滞留 socket，触发 `EMFILE`。Hermes 参考实现正是为此在**连续失败满一批后回收轮询会话**（见 `0002-wechat.md` §2.4）。本项目在 G2a 实现轮询时沿用该策略，本节只记录环境事实。
- 无需公网回调域名；iLink 走长轮询，本机出站即可。
- **已确认：本机（含上述 TUN 代理）可作为评测环境**，用户理由“反正都是请求 API”。不要求为评测专门切换直连。报告里注明该代理环境，便于与其他环境对比时识别差异。
- 该代理对**延迟分位数**可能有影响（多一跳）；若某实验的主指标是延迟，需在实验卡里说明代理是否恒定，否则结论标注“环境相关”。

## 5. 确认状态（G0 关闭条件）

- [x] 冻结首批 2 个供应商预设（`deepseek` / `custom-openai`）：分别绑定 `components/model/deepseek` 与 `components/model/openai`，`protocol` 存适配器 ID；百炼/Qwen 及其他供应商延后。（2026-10-06 用户确认）
- [x] 配置目录：`HERALD_HOME` → `~/.herald`（POSIX）/ `%LOCALAPPDATA%\herald`（Windows）。（2026-10-06 用户确认）
- [x] 评测预算：单次联网评测无费用上限，本机（含 TUN 代理）可作评测环境。（2026-10-06 用户确认）
- [x] 用户审核通过 G0，进入 G1a。（2026-10-06）
