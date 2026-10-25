---
title: Agent 工具沙箱兼容方案（三平台三接口）
status: active
updated: 2026-10-25
summary: 调研 Codex/Claude Code/Gemini 沙箱实现，设计可插拔 Sandbox 抽象与 macOS/Linux/Windows 三原生后端，分单元待审
---

# Agent 工具沙箱兼容方案（三平台三接口）

**状态：提案，2026-10-25 交付待用户审核；未写任何实现代码。** 用户要求调研主流 agent 沙箱技术并设计方案，使现有六工具可接入沙箱，保持可扩展性，最终覆盖 Windows/macOS/Linux 三平台、每平台一个原生接口。本方案对齐仓库审核制度：通过后按单元实施，编号建议 **G3.3**（工具域后继，接 G3/G3.1；不占用 G4a 记忆线）。

## 1. 现状与差距

当前 `internal/tools` 的安全边界（G3/G3.1 契约）：

| 边界 | 现状 | 缺口 |
| --- | --- | --- |
| 文件读写/搜索 | `os.Root` 规范工作区根，符号链接逃逸被 Go 运行时拒绝；原子写、读摘要校验 | 边界在 Go 层，仅在工具代码路径内成立 |
| Shell | 宿主直接执行；Unix 同进程组清理、超时/取消保留既有副作用 | **无 OS 级隔离**：任意命令可读 `~/.ssh`、改工作区外文件、出站联网 |
| 网络 | 无任何限制 | bash 子进程可全量访问网络 |
| 平台 | darwin 实测；Windows/Linux 未实测（G-win 前置） | 三平台行为未定义 |

差距结论：文件工具已有"弱边界"（Go 层路径校验），真正的逃逸面是 bash 及其子进程；主流方案也以 shell 为主要沙箱对象。

## 2. 主流实现调研（2026-10-25 核对）

### 2.1 OpenAI Codex CLI：OS 原语、默认开启、三平台全覆盖

- macOS：Seatbelt（`sandbox-exec`），deny-by-default 配置 + 按工作区动态生成写许可；Apple 已废弃该接口但仍是 Codex 首选（[沙箱分析](https://agent-safehouse.dev/docs/agent-investigations/codex)、[Simon Willison 的分析](https://simonwillison.net/2025/Nov/9/codex-sandbox-investigation)）。
- Linux：**Landlock**（路径级文件访问控制，无特权）+ **seccomp**（syscall 过滤）组合，无需容器与 root（同上分析；[agent 沙箱深潜](https://pierce.dev/notes/a-deep-dive-on-agent-sandboxes)）。
- Windows：**restricted token（`WRITE_RESTRICTED` + restricted SID）**实现"用户 SID 授予基线、restricted SID 收窄写路径"的 AND 门——概念上即 Landlock 的路径写限制在 Windows 的等价物；另有网络限制（[OpenAI：构建 Codex Windows 沙箱](https://openai.com/index/building-codex-windows-sandbox)、[技术深潜](https://jonny-johnson.medium.com/a-deep-dive-into-codex-windows-sandbox-a2489bf4ae91)、[Windows 原语对照](https://agentpatterns.ai/security/windows-sandbox-primitives-coding-agents)）。
- 权限模型：`read-only` / `workspace-write` / `danger-full-access` 三档，批准策略与沙箱联动。

### 2.2 Claude Code：沙箱运行时 + 网络代理（beta）

- OS 原语：macOS Seatbelt、Linux **bubblewrap**（namespaces）；Windows 要求在 WSL2 内运行（[官方文档](https://code.claude.com/docs/en/sandboxing)）。
- 网络隔离**不用 iptables**：沙箱内仅可通过"连到沙箱外代理进程的 Unix domain socket"出站；代理做域名白名单、新域名用户确认、可插拔自定义规则（[Anthropic 工程博客](https://www.anthropic.com/research/claude-code-sandboxing)）。
- 文件隔离默认"当前目录可写"，约束覆盖命令派生的全部子进程；权限提示减少 84%。
- 覆盖面只有 bash 工具（社区批评：Read/Write 不在沙箱内，见 [issue #26616](https://github.com/anthropics/claude-code/issues/26616)）；运行时开源（`anthropic-experimental/sandbox-runtime`），可用于隔离任意进程与 MCP server。

### 2.3 Gemini CLI：多后端可选

四种方法：macOS Seatbelt（Apple Silicon 上优先于容器）、Docker/Podman 容器、Windows 原生沙箱、gVisor；`SANDBOX_FLAGS` 允许注入自定义容器参数（[官方文档](https://google-gemini.github.io/gemini-cli/docs/cli/sandbox.html)、[分析](https://agent-safehouse.dev/docs/agent-investigations/gemini-cli)）。默认 macOS 配置曾被指出"只禁写不禁读"（[默认配置问题](https://medium.com/@chewloongnian/gemini-clis-default-sandbox-lets-your-agent-read-10-of-12-secret-files-here-s-the-fix-3f51b852c6a1)）——读写要同时收紧。

### 2.4 通用原语矩阵（三平台接口的底层选择）

| 平台 | 文件系统隔离 | syscall/进程限制 | 网络限制 | 无特权可用 | Go 依赖 |
| --- | --- | --- | --- | --- | --- |
| macOS | Seatbelt（`sandbox-exec`，已废弃但可用） | 同左（profile 内） | 同左 | 是 | 标准库 exec 生成 profile |
| Linux | **Landlock**（内核 ≥5.13，路径读写执行分层） | **seccomp-bpf** | Landlock 网络（≥6.7，TCP bind/connect 禁止）；或 namespace+bubblewrap（外部二进制） | 是（Landlock/seccomp 均免特权） | `golang.org/x/sys/unix`（已有间接依赖） |
| Windows | **restricted token**（restricted SID 写路径 AND 门）；更强：AppContainer（capability SID） | Job Object（子进程/资源） | token 限制 + 首版直接禁网；WFP 留后续 | 是 | `golang.org/x/sys/windows` |
| 跨平台（可选后置） | Docker/Podman 容器、gVisor、microVM | 容器/VM 内核 | 容器网络 | 需外部运行时 | exec 驱动外部命令 |

Microsoft 侧同类参照：MXC（eXecution Containers，开源）从 AppContainer 到 Hyper-V 十级后端（[MXC 内部分析](https://www.originhq.com/research/mxc-execution-containers-internals)）。Anthropic 另有面向服务端的 code execution 容器工具与 MCP 沙箱模式（[code execution with MCP](https://www.anthropic.com/research/code-execution-with-mcp)），属于渠道/服务端形态，不直接影响本机 CLI 设计。

### 2.5 对本方案的直接启示

1. **三平台三原生后端是业界共识**：Codex（Seatbelt / Landlock+seccomp / restricted token）正是"三平台三接口"，无外部依赖、默认可用。
2. **bash 是首要对象**：文件工具可用进程内路径校验兜底（我们已有 os.Root），shell 必须进程级隔离且覆盖全部子进程。
3. **文件与网络必须同时收紧**：只禁文件不禁网络可泄密（SSH 密钥外发）；Gemini 默认"只禁写"被诟病。
4. **网络首版全禁、代理留扩展点**：Claude 的 UDS 代理模式是可扩展网络策略的最好样板，但复杂度高，不进首个单元。
5. **失败要显式**：沙箱装配失败宁可拒绝执行，不静默降级为无沙箱。

## 3. 抽象接口设计（提案）

新包 `internal/sandbox`（零业务依赖，不 import config/app；config 经选项结构注入）。接口分两层：**工厂（描述策略）**与**后端（包装执行）**，平台后端用 build tag 隔离，编译期即"三平台三接口"。

```go
// Policy 是一次 run 的隔离策略；由入口按用户配置构造，字段全部可信来源。
type Policy struct {
    Workspace   string   // 规范工作区根（可写边界，来自 tools.workspace）
    WritePaths  []string // 额外可写路径（如构建缓存），首版仅 workspace
    Network     NetworkPolicy // Disabled（首版唯一实现）/ Proxy（后续单元）
}

type NetworkPolicy string // "disabled" | "proxy"（proxy 留扩展，不实现）

// Backend 为一次 run 提供命令包装；实现必须并发安全、覆盖全部子进程。
type Backend interface {
    // Prepare 在 run 开始时装配隔离环境；失败必须返回错误（不静默降级）。
    Prepare(ctx context.Context, policy Policy) error
    // Wrap 把即将执行的命令置于沙箱内；返回错误则该工具不执行。
    Wrap(cmd *exec.Cmd) error
    // Summary 返回展示与 trace 用的固定描述（不含路径外的敏感信息）。
    Summary() string
    // Close 释放本次 run 的沙箱资源；幂等。
    Close() error
}

// Factory 按平台注册（darwin/linux/windows build tag 各一 + noop）。
type Factory func() (Backend, bool)
```

要点：

- **`Wrap(cmd)` 是唯一执行面**：`bash` 工具从直接 `exec.CommandContext` 改为经 `Backend.Wrap`；六工具中其余五个继续走 `os.Root`（进程内边界），接口预留后续把文件工具一并纳入同一 Policy 的空间（可扩展性点 1）。
- **能力协商**：`Factory` 第二返回值表达"本机是否可用"（如 Linux 内核 < 5.13 时 Landlock 不可用）；装配失败按启示 5 显式报错。探测在启动时一次，结果进 `/status` 与 trace，不做运行中热切换。
- **配置（schema v1 兼容扩展，单独审核）**：`tools.sandbox`：`mode`（`off` 默认 / `workspace-write`）、`network`（`disabled` 默认）。默认 `off` 保持现行行为，不破坏既有用户；选非 off 而后端不可用时启动明确报错。
- **与既有边界的叠加**：沙箱不替代 os.Root、参数校验、许可注册表与 G4a.2.1 检查点——层次为"许可（哪些工具）→ 参数校验（输入合法）→ os.Root（Go 路径边界）→ 沙箱（OS 进程边界）"。

### 3.1 三平台三接口（每平台一个原生后端文件）

| 文件（build tag） | 机制 | 首版范围 |
| --- | --- | --- |
| `sandbox_darwin.go` | Seatbelt：启动时生成 deny-by-default profile，仅放行 workspace 写、系统只读集、`__DATA__` 等必要段；`sandbox-exec -p` 包裹命令 | 文件读写收紧 + 全禁网 |
| `sandbox_linux.go` | Landlock（`LANDLOCK_ACCESS_FS_{READ,WRITE,EXEC}` 分层，workspace 递归可写、其余撤权）+ seccomp filter（禁 `socket`/`execve` 白名单外路径按需）+ 同进程组清理兼容 | 同上；内核 ≥5.13，探测失败显式拒绝 |
| `sandbox_windows.go` | restricted token：`CreateRestrictedToken` + `WRITE_RESTRICTED`，workspace ACL 加 restricted SID（写 AND 门）；Job Object 限制子进程；网络首版以 token/integrity level 收紧为目标 | 同上；真实行为以 G-win 实测为准 |
| `sandbox_noop.go`（全平台） | 不隔离，`mode=off` 时的显式后端 | 现行行为，测试基线 |

每个后端同包内共享 `Policy` 与逃逸测试套件（见 §5），接口只有一份——"三平台三接口"指平台实现各一，不是三套 API。

### 3.2 可扩展性设计（明确预留，不提前实现）

| 扩展点 | 机制 | 触发条件 |
| --- | --- | --- |
| 网络代理（Claude 式 UDS 代理、域名白名单/确认） | `NetworkPolicy` 枚举已留 `"proxy"`；`Backend` 不感知代理内部 | 后续授权单元 |
| 容器/microVM 后端（Docker/Podman/gVisor） | 新增 Backend 实现即插，不改接口；`Factory` 注册表支持按配置选择 | 三原生后端稳定后 |
| 文件工具纳入同一沙箱 | Policy 已是纯数据；文件工具改经后端执行器读写的 seam 在 S1 预留 | bash 后端验证后评估收益 |
| read-only / danger-full-access 档位 | Policy 结构直接表达（WritePaths 空=只读；off=无沙箱） | 需要时加配置枚举 |
| 渠道/服务端沙箱（Anthropic code execution 形态） | Backend 是本地接口，不承载远程；远程沙箱属渠道层（G2b+） | 渠道阶段另行设计 |

## 4. 拟改文件与实施单元（逐个审核，未获通过不动代码）

| 单元 | 内容 | 验收要点 |
| --- | --- | --- |
| **G3.3.1** | `internal/sandbox` 契约 + noop 后端 + bash 工具执行 seam（mode=off 行为与现在完全一致）+ `tools.sandbox` 配置骨架 + 双平台契约测试 | 默认零行为变化；装配失败显式报错；全量回归 |
| **G3.3.2** | macOS Seatbelt 后端 | PTY + 单测：工作区外写/读 `~/.ssh`/联网三类逃逸全部被拒；取消/超时/进程组清理不回退 |
| **G3.3.3** | Linux Landlock+seccomp 后端 | 同上逃逸套件；内核探测；CI 缺真机时如实 N/A |
| **G3.3.4** | Windows restricted token + Job Object 后端 | 同上；与 G-win（真实 Windows 实测）联动，不互相阻塞但都完成才算 Windows 可用 |
| **G3.3.5**（可选，另行授权） | 网络代理模式 / 容器后端 | 代理域名白名单与用户确认流 |

每个单元独立交付 `docs/reviews/G3.3.x.md` 并停下待审；`/status` 增加 sandbox 摘要属于 S1 范畴（只读展示，无新权限语义）。

## 5. 测试与验收矩阵（实施时执行）

| 场景 | 必须满足 |
| --- | --- |
| 工作区写 | 正常读写不受阻；原子写/权限保留语义不变 |
| 工作区外写（`../`、绝对路径、符号链接） | 三平台后端全部拒绝，错误可分类 |
| 敏感读（`~/.ssh`、`~/.plume` 凭据目录） | 拒绝；**凭据目录在任何模式下不可读不可写** |
| 网络出站（TCP/UDP/HTTP） | 首版全拒；错误信息不泄漏内部路径 |
| 子进程继承 | bash 派生的全部子进程同受约束（逃逸脚本反例） |
| 取消/超时/退出 | 沙箱不阻碍既有进程组清理与总退出期限；无残留进程 |
| 后端不可用（老内核/策略被拒） | 启动时显式报错，不静默无沙箱执行 |
| mode=off | 与现行行为逐字节一致（回归基线） |
| 配置兼容 | schema v1 兼容扩展；未知字段保留；setup 不新增必答项 |

指标协议：只报逃逸场景通过数与真实计时；未实测平台（Windows/Linux 真机）记 N/A，不从 darwin 结果推导。

## 6. 风险与已知限制（如实声明）

- **Seatbelt 已被 Apple 废弃**：Codex 同样面临；profile 语法随 macOS 更新可能变化，后端需按版本固测（本机 darwin 实测 + macOS 升级时重跑）。
- **Landlock 版本差异**：文件分层 ≥5.13、网络 ≥6.7；S3 只依赖文件分层，网络由 seccomp/组合实现或保持全禁。
- **Windows 复杂度最高**：restricted token/AppContainer 语义细节多，S4 只承诺"写路径 AND 门 + 子进程作业对象 + 全禁网"最小闭环；AppContainer 与 WFP 留增强。
- **沙箱不是信任边界**：与设计一致，许可注册表、参数校验、检查点与用户确认仍然必要；沙箱降低的是"模型被诱导执行破坏性命令"的爆炸半径。
- **性能**：每次 bash 调用的沙箱装配开销需在 S2-S4 实测（Seatbelt/Landlock 为每进程一次性成本，预期毫秒级，不凭预期填表）。

## 7. 与仓库现状的衔接

- 不改六工具契约（`docs/specs/agent/tools.md`）既有语义；bash 执行 seam 是实现替换，对外行为仅在 mode≠off 时收紧。
- 检查点/恢复（G4a.2.1）不感知沙箱：工具边界回调顺序不变。
- 新增依赖仅 `golang.org/x/sys`（darwin/linux/windows 均为间接现有依赖转直接，版本锁定记录在 AGENTS）；**不引入 CGO**（不选 libseccomp-golang 的 CGO 路径，seccomp filter 用纯 Go BPF 构造或 x/sys 封装）。
- 立项通过后 roadmap 增补 G3.3.x 节点；本方案文档先行，实现未开始。

## 8. 参照来源

- [OpenAI：Building Codex Windows sandbox](https://openai.com/index/building-codex-windows-sandbox)；[Codex 沙箱分析（agent-safehouse）](https://agent-safehouse.dev/docs/agent-investigations/codex)；[Simon Willison 分析](https://simonwillison.net/2025/Nov/9/codex-sandbox-investigation)；[agent 沙箱深潜（pierce.dev）](https://pierce.dev/notes/a-deep-dive-on-agent-sandboxes)；[Codex Windows 技术深潜](https://jonny-johnson.medium.com/a-deep-dive-into-codex-windows-sandbox-a2489bf4ae91)
- [Anthropic：Making Claude Code safer with sandboxing](https://www.anthropic.com/research/claude-code-sandboxing)；[Claude Code sandbox 文档](https://code.claude.com/docs/en/sandboxing)；[sandbox 环境选择](https://code.claude.com/docs/en/sandbox-environments)；[issue #26616 覆盖面讨论](https://github.com/anthropics/claude-code/issues/26616)
- [Gemini CLI sandbox 文档](https://google-gemini.github.io/gemini-cli/docs/cli/sandbox.html)；[Gemini 沙箱分析](https://agent-safehouse.dev/docs/agent-investigations/gemini-cli)；[默认配置问题](https://medium.com/@chewloongnian/gemini-clis-default-sandbox-lets-your-agent-read-10-of-12-secret-files-here-s-the-fix-3f51b852c6a1)
- [Windows 沙箱原语对照（AgentPatterns）](https://agentpatterns.ai/security/windows-sandbox-primitives-coding-agents)；[Project Zero：Restricted Token](https://projectzero.google/2020/04/you-wont-believe-what-this-one-line.html)；[MXC 内部分析](https://www.originhq.com/research/mxc-execution-containers-internals)
