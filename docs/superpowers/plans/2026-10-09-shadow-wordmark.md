---
title: 开屏立体标题实施计划
status: active
updated: 2026-10-09
summary: 按用户参考图将 PLUME-AGENT 改为六行实心笔画与双线轮廓标题，保留雾青和左对齐
---

# 开屏立体标题实施计划

用户直接提供 HERMES-AGENT 参考图并要求 PLUME-AGENT 模仿该字形，本轮授权包含实现。使用 executing-plans 在原目录完成，不创建工作树，不自动提交或推进 G3。

**目标：** 六行立体像素标题，以实心笔画配双线轮廓模拟参考图右下阴影；雾青主体、深青轮廓。沿用当前左对齐。

**架构：** splash.go 保存两份纯字符标题：完整 94 列、紧凑 71 列；宽度至少 94 列使用完整版，80–93 列使用紧凑版，更窄继续既有羽毛/文本布局。ANSI 样式仍引用 theme.go token，以 raw splash 行进入 viewport。

**技术栈：** 现有 Go、lipgloss v2；不新增依赖、配置或图片加载。

## 任务 1：标题与宽度验证

- [x] 修改 internal/tui/splash_test.go：80/93 列验证六行紧凑字形，94/100/160 列验证完整字形；各行含实心笔画和轮廓、不溢出，保持左对齐，主体与轮廓都含有效主题色。
- [x] 运行 `rtk go test ./internal/tui -run 'Test(SplashRendersBoxedSplash|WordmarkShadow)' -count=1`，先观察旧五行标题不符合新样式的失败。
- [x] 修改 internal/tui/splash.go：保存六行完整/紧凑标题；按实际可见宽度选择，逐笔画引用 themePrimary / themeDark 渲染，替换原五行平面字。
- [x] 同一测试命令通过，并运行 `rtk go test -race ./internal/tui` 验证已有开屏、布局和光标。

## 任务 2：实际预览和交付

- [x] `rtk ./scripts/build.sh`；用 scripts/preview_splash.py 对 100×34 真彩/256 色和 80×34 真彩捕获完整开屏，查看 PNG，确认字形层次、羽毛及闭合边框。
- [x] 全仓竞态、vet、gofmt、diff 检查；运行增强后的 G1b.4 PTY 九场景，保持输入区与状态栏行为。
- [x] `rtk ./scripts/build.sh --install`，已安装程序执行真彩宽屏 PTY 验收；原配置与 SDK 保持现状。
- [x] 更新 docs/tui-splash.md、AGENTS、当日日志及 G1b.4 审核补充：六行标题，宽端开屏总高 28 行。交付后停下，用户可直接审核实际预览。
