# gsyncer

> 中文 · [English](#english)

## 简介

**gsyncer** 是一个带 **GFS 快照**的远程文件夹同步工具。它通过 `ssh + rsync` 把远程服务器上的目录拉取到本地，每次同步都会自动保留一份带时间戳的快照，并按祖父-父-子（GFS）保留策略自动清理旧快照。

主要通过**交互式终端界面（TUI）**操作，同时提供完整的命令行子命令，方便放进 cron 或脚本中定时运行。编译产物是**零依赖的 Linux 静态单文件**，拷贝到目标机器即可运行。

## 核心特性

- **增量拉取**：基于 `rsync -a --delete`，支持忽略规则、断点续传（`--partial`）、传输压缩与限速。
- **时间点快照**：每次同步生成一份历史快照，可随时浏览、恢复或删除。
- **省空间后端**：默认硬链接（未改动文件共享 inode）；在 CoW 文件系统上自动升级为 reflink；btrfs 上使用子卷快照。
- **GFS 保留策略**：按 recent / monthly / semiannual / yearly 四层并集自动清理，始终保留最新一份。
- **双操作模式**：TUI 交互操作 + 命令行子命令（`sync` / `status` / `prune` / `restore` 等），后者适合自动化。
- **同步钩子与通知**：支持 `pre_sync` / `post_sync` 命令以及 webhook / 命令行通知。
- **零依赖单文件**：`CGO_ENABLED=0` 静态编译，`-s -w -trimpath` 精简且可复现，支持交叉编译。

## 快速开始

```bash
./build.sh          # 编译出 dist/gsyncer（linux/amd64 静态单文件）
./gsyncer           # 无参数启动进入 TUI
./gsyncer sync      # 命令行同步全部条目（适合 cron）
```

运行前提：本机装有 `ssh`、`rsync`，远程主机装有 `rsync`。详细用法见 [README](../README.md)。

## 技术栈

Go 1.22 · [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI 框架 · TOML 配置 · `ssh` / `rsync`。

## 许可证

本项目以 **GPL** 许可证开源，全部依赖均为 MIT / BSD 宽松许可证，与 GPL 兼容。

---

<a name="english"></a>

# gsyncer

> [中文](#gsyncer) · English

## Overview

**gsyncer** is a remote folder synchronization tool with **GFS snapshots**. It pulls directories from a remote server to the local machine over `ssh + rsync`, automatically keeps a timestamped snapshot on every sync, and prunes old snapshots according to a Grandfather-Father-Son (GFS) retention policy.

It is driven primarily through an **interactive terminal UI (TUI)**, and also ships a full set of command-line subcommands for running in cron jobs or scripts. The build output is a **zero-dependency, statically linked single binary for Linux** — just copy it to the target machine and run.

## Key Features

- **Incremental pull** — built on `rsync -a --delete`, with ignore rules, resumable transfers (`--partial`), compression, and bandwidth limiting.
- **Point-in-time snapshots** — each sync produces a historical snapshot you can browse, restore, or delete at any time.
- **Space-efficient backends** — hardlink by default (unchanged files share inodes); auto-upgrades to reflink on CoW filesystems; uses subvolume snapshots on btrfs.
- **GFS retention** — automatically prunes via the union of recent / monthly / semiannual / yearly tiers, always keeping the latest snapshot.
- **Dual operation modes** — interactive TUI plus CLI subcommands (`sync` / `status` / `prune` / `restore`, etc.) suited for automation.
- **Sync hooks & notifications** — `pre_sync` / `post_sync` commands, plus webhook or command-based notifications.
- **Single static binary** — `CGO_ENABLED=0` static build, stripped and reproducible via `-s -w -trimpath`, with cross-compilation support.

## Quick Start

```bash
./build.sh          # produce dist/gsyncer (static single binary, linux/amd64)
./gsyncer           # run without args to enter the TUI
./gsyncer sync      # sync all entries from the CLI (cron-friendly)
```

Prerequisites: `ssh` and `rsync` on the local machine, `rsync` on the remote host. See the [README](../README.md) for full usage.

## Tech Stack

Go 1.22 · [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI framework · TOML config · `ssh` / `rsync`.

## License

Released under the **GPL** license. All dependencies use permissive MIT / BSD licenses, which are GPL-compatible.
