# 福宝控制台

一个以 Pilot 左右分栏结构为视觉参考的桌面客户端基础工程。页面内容已经替换为红包监测业务入口，不包含 Pilot 的站点、对话或排期功能。

## 技术结构

- Svelte 5 + TypeScript：桌面界面
- Tauri 2 + Rust：窗口宿主、IPC 转发和 Go 进程监管
- Go sidecar：业务引擎基础协议
- NDJSON / JSON-RPC：Rust 与 Go 的本地通信格式

## 本地运行

```bash
npm install
npm run dev
```

运行桌面版本：

```bash
npm run desktop:dev
```

构建检查：

```bash
npm run check
npm run build
npm run build:engine
cd src-tauri && cargo check
```

## GitHub 构建

仓库内的 `Build Desktop Clients` 工作流支持：

- macOS Universal：同时支持 Apple Silicon 与 Intel，产出 DMG；
- Windows x64：产出 NSIS EXE 与 MSI 安装包。

可以在 GitHub Actions 中手动运行并下载构建产物。推送 `v*` 标签时，工作流还会自动创建或更新当前仓库对应版本的 GitHub Release。

## 当前范围

已完成监测总览、红包任务、浏览器实例、账号与代理四个基础入口，以及搜索、刷新、状态切换和新建监测交互。Go 引擎当前实现 `engine.ready`、`system.ping` 和 `engine.status` 协议骨架，尚未接入真实抖音业务。
