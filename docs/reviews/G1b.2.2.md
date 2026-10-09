---
title: G1b.2.2 审核交付：TUI 开屏与主题色
status: passed
updated: 2026-10-07
summary: 盲文羽毛点阵、雾青三色与开屏排版；190 项测试及 pty 验证通过，2026-10-07 用户确认提交并推送
---

# G1b.2.2 审核交付：TUI 开屏与主题色

日期：2026-10-07。用户提供雾青羽毛参考图，随后明确指定使用盲文点阵显示羽毛，并在最终预览后确认“提交并且推送吧”，本单元通过。现行契约见 [TUI 开屏与主题色](../specs/tui/splash.md)。

> **证据说明（2026-10-09）**：按「通过即删」治理（见 [TUI runbook](../runbooks/tui.md)），审核期证据已随文档重组移除。正文出现的 `evidence/G1b.2.2/...` 路径指向提交 `9860902`，用 `git show 9860902:<路径>` 可取回原始字节，或运行 `python3 scripts/preview_splash.py` 重新生成。

## 1. 最终改动

- 羽毛从参考图等比例采样为 **64×80 点位**，以每字符 **2×4 点位的 Unicode 盲文**显示为 **32 列×20 行**；保留羽轴斜向留白、右上碎羽和左下细茎。沿用深青/主青/浅青三个主题 token；逐字符设置前景色，不填背景。矩阵保存在 `featherBraille`，运行时不加载图片。
- `PLUME-AGENT` 标题仍为 5 行、65 列，网格占位点转换为空格，按可见列宽居中，修复原先按 UTF-8 字节长度计算造成的偏移。
- 圆角框宽 `min(终端宽度−2, 88)`，四边完整；羽毛左右各留白 2 列，分隔线贯穿框内。右栏快捷键浅青、说明灰色，9 行提示统一对齐，补全三种换行方式；Alt/Option 名称来自 KeyMap，退出提示注明空输入条件。
- 版本、模型与目录净化为单行文本并按终端列宽裁剪、补齐，长文本和中文路径不会撑破边框。32–79 列采用羽毛竖排与折行提示，32 列以下采用 `PLUME` 小标题和文本。
- 宽端总高仍为 **27 行**。开屏是记录区初始内容，只出现一次，Ctrl+N 后不复活；窗口变化不重复插入。版本来自构建注入值，不展示未实现的工具/技能信息。

本次视觉优化涉及 `internal/tui/splash.go`、`splash_test.go`、`scripts/preview_splash.py`，并同步契约、AGENTS.md、每日流水和本审核记录。此前同单元的 theme/model/view/update 与 CLI 装配改动保留。没有新增 Go 依赖。

## 2. 可复现命令与实际结果

在仓库根目录运行：

```bash
rtk go test -race ./...           # 190 个测试，14 包全部通过
rtk go vet ./...                  # 退出 0，无诊断
rtk proxy gofmt -l .              # 空输出
rtk proxy ./scripts/build.sh      # 构建成功，v0.1.0 / 4995641-dirty
rtk proxy env PLUME_HOME=/tmp/plume-g1b222-key-demo python3 scripts/pty_demo_g1b221.py
# TUI_DEMO_OK returncode = 0
```

开屏测试覆盖标题居中/无占位点、宽端内容/三色/完整边框、窄端降级、只渲染一次，以及长版本、中文/emoji 路径、换行/制表/ANSI 输入下的屏幕宽度限制。聊天演示复跑普通发送、Alt+Enter 多行、反斜线续行、Esc 空闲与双击 Ctrl+C 退出。

### pty 屏幕帧

新增脚本使用真实离线二进制、独立临时 `PLUME_HOME` 和 pyte 终端模拟器捕获画面。PNG 根据捕获的字符、前景/背景颜色还原；每张图同时保存纯文本 `.txt`、原始 `.ansi` 和检查 `.json`。普通字符采用 SF Mono，盲文按捕获字符的八位点位掩码还原圆点，单元格宽高比 1:2；真实终端的点形与点距由字体及回退字体决定。

原始 `.ansi` 含终端控制序列和回车字节，`.gitattributes` 将审核证据中的这类文件标记为二进制，保留捕获字节，避免文本行尾转换或合并改写 trace。

本机使用 Codex 已有 Python/Pillow，pyte 0.8.2 临时安装到 `/tmp/plume-splash-preview-deps`，未加入项目运行依赖：

```bash
PREVIEW_PY=/Users/wwhds/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3
rtk proxy "$PREVIEW_PY" -m pip install --target /tmp/plume-splash-preview-deps pyte==0.8.2
rtk proxy env PYTHONPATH=/tmp/plume-splash-preview-deps "$PREVIEW_PY" scripts/preview_splash.py
rtk proxy env PYTHONPATH=/tmp/plume-splash-preview-deps "$PREVIEW_PY" scripts/preview_splash.py --cols 80
rtk proxy env PYTHONPATH=/tmp/plume-splash-preview-deps "$PREVIEW_PY" scripts/preview_splash.py --cols 40
rtk proxy env PYTHONPATH=/tmp/plume-splash-preview-deps "$PREVIEW_PY" scripts/preview_splash.py --color-term ''
```

| 屏幕帧 | 实际结果 |
| --- | --- |
| [100×32 真彩色](evidence/G1b.2.2/splash-braille-100x32-truecolor.png) | 版本/模型/提示/盲文点阵可见，三色 ANSI 存在，20 行侧边与上下边框对齐，退出 0 |
| [80×32 真彩色](evidence/G1b.2.2/splash-braille-80x32-truecolor.png) | 最小方框宽度下内容和边框检查全部通过，退出 0 |
| [40×32 真彩色](evidence/G1b.2.2/splash-braille-40x32-truecolor.png) | 无方框，点阵羽毛/版本/模型/折行提示可见，退出 0 |
| [100×32 256 色](evidence/G1b.2.2/splash-braille-100x32-256color.png) | 256 色 ANSI 存在，点阵与布局检查全部通过，退出 0 |

### 失败路径证据

```bash
rtk proxy env PLUME_HOME=/tmp/plume-g1b222-non-tty ./plume chat --offline
```

无 TTY 时实际退出 **1**，立即报告 `chat needs an interactive terminal`，没有进入交互等待。完整输出见 [non-tty.txt](evidence/G1b.2.2/non-tty.txt)。本次不调用真实模型；模型成功/失败 trace 不属于视觉优化证据。

## 3. 指标

| 指标 | 绝对值 | 变化 |
| --- | --- | --- |
| 竞态测试 | 190 项 / 14 包通过；tui 包 23 项 | N/A |
| 羽毛采样点位 | 64×80，显示 32 列×20 行 | N/A |
| 宽端开屏总高 | 27 行 | N/A |
| 验证屏幕帧 | 4 组，全部检查通过 | N/A |
| 构建产物大小 | 12,237,874 bytes，约 11.67 MiB（`-s -w`） | N/A |
| 新增 Go 依赖 | 0 | N/A |
| 模型性能/费用/质量 | 未测量 | N/A |

## 4. 局限与待检查项

- 点阵是离散采样，渐变为三段。字体单元格比例、盲文点距和终端调色板会影响观感；人工检查自己的终端仍有必要。
- 盲文字符依赖字体或回退字体支持。`NO_COLOR` 下遵循用户设置，三色变为单色；预览脚本为了核对颜色，显式移除宿主的颜色覆盖变量。
- 低矮终端可滚动回看开屏；窗口缩小时已有 art 不重新绘制，沿用既有一次渲染契约。

用户已确认提交并推送，本单元通过；下一单元 G1b.3 另行授权，本次只交付与发布当前改动。
