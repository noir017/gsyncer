# 设计：gsyncer GUI（内嵌 Web 界面）

> 状态：设计稿，未实现。配套阅读：[gsyncer.md](gsyncer.md)（整体架构）、[INTRODUCTION.md](INTRODUCTION.md)。

## 1. 目标与选型

### 目标

- 在不破坏「零依赖 Linux 静态单文件」这一核心卖点的前提下，提供图形界面；
- 功能面与 TUI 对齐：条目管理、同步（含全部/单条、实时日志）、快照浏览/删除/清理/恢复、状态总览；
- GUI 只是第三种前端，与 TUI / CLI 共用同一份配置和同一套 `internal/` 能力，不引入第二套业务逻辑。

### 选型：内嵌 Web GUI（`gsyncer web` 子命令）

| 方案 | 结论 | 原因 |
|------|------|------|
| **内嵌 Web（选定）** | ✅ | 纯 Go stdlib `net/http` + `go:embed` 前端，`CGO_ENABLED=0` 不变，仍是单文件；gsyncer 典型部署在无显示器的服务器/NAS 上，浏览器远程访问正是最自然的 GUI 形态 |
| Fyne | ❌ | 需要 CGO + X11/GL 依赖，破坏静态单文件；headless 服务器上无法使用 |
| Wails / Tauri | ❌ | 依赖本机 webkit/webview 运行库，且引入 node 构建链；同样解决不了 headless 场景 |
| 独立桌面客户端（远程连 API） | ❌ | 两个交付物、两套发布流程，超出本项目体量 |

前端**不引入 node 构建链**：手写 HTML/CSS/vanilla JS（约 5 个页面的体量完全可控），`go:embed` 打进二进制。`./build.sh` 流程零改动。

## 2. 总体架构

```
gsyncer web [--listen 127.0.0.1:8873] [--token xxx] [--config path]
                     │
              internal/webui
              ├── server.go     http.Server 装配、路由、中间件（auth/日志）
              ├── api.go        REST handlers（薄封装，调 internal/* 现有函数）
              ├── jobs.go       同步任务管理器：job id、状态、日志环形缓冲、SSE 广播
              ├── static/       go:embed 的 index.html / app.js / app.css
              └── *_test.go
```

- 与 `internal/tui` 平级，同样只做「前端」：读写配置走 `internal/config`，同步走 `internal/syncer`，快照/恢复/清理走对应包。TUI 已经证明这些包的接口足以支撑一个前端，webui 不需要动它们。
- 同步在服务端以 **job** 形式异步执行（复用现有 per-entry 文件锁，天然防止 web、TUI、cron 三方并发同一条目）；浏览器通过 SSE 订阅日志流。
- 断线重连：日志进环形缓冲（每 job 上限约 2000 行），SSE 重连时先回放缓冲再续传，与 TUI `run.go` 的滚动日志体验对齐。

## 3. HTTP API

统一前缀 `/api`，JSON 收发。写操作一律 `POST`/`PUT`/`DELETE`，供 CSRF 防护区分。

| 方法与路径 | 作用 | 对应现有能力 |
|------------|------|--------------|
| `GET /api/status` | 各条目最近快照年龄/份数/后端 | `status.go`（已有 `--json`，直接复用其结构体） |
| `GET /api/entries` | 条目列表 | `config.Load` |
| `POST /api/entries` | 新增条目 | `config` 校验 + 写回 |
| `PUT /api/entries/{name}` | 编辑条目 | 同上 |
| `DELETE /api/entries/{name}` | 删除条目 | 同上 |
| `POST /api/entries/parse` | 「快速粘贴」解析（scp 简写 / 键值对） | 复用 TUI form 的解析函数（如需则导出到共享位置） |
| `POST /api/sync` | 发起同步，body：`{names?: [], all?: bool, dry_run?: bool}`，返回 `{job_id}` | `syncer.Run` |
| `GET /api/jobs/{id}` | job 状态（pending/running/done/failed + 每条目结果） | jobs.go |
| `GET /api/jobs/{id}/log` | SSE 实时日志流 | jobs.go |
| `GET /api/snapshots/{name}` | 快照时间戳列表 | `snapshot` |
| `DELETE /api/snapshots/{name}/{ts}` | 删除单个快照 | `snapshot` |
| `POST /api/prune` | body：`{name?, dry_run}`；`dry_run=true` 返回将删列表供确认 | `syncer/prune` |
| `POST /api/restore` | body：`{name, at|latest, to, force}` | `restore` |
| `GET /api/config/defaults` · `PUT` | defaults / log / notify 段读写 | `config` |

设计要点：

- **危险操作两段式**：prune 与 restore 前端必须先发 `dry_run` / 预检拿到「将删除 N 份」「目标已存在」等信息，用户确认后再发真实请求——把 TUI 的 `y/N` 确认语义搬到 HTTP 层，而不是只靠前端弹窗。
- **配置写回冲突**：沿用 TUI 的策略（load-modify-save 整文件写回）；webui 每次写前重新 `Load`，对同名条目做存在性校验，返回 409 时前端提示刷新。
- 错误统一 `{error: "..."}` + 恰当状态码；404 用于 name/ts 匹配不到（与 CLI「匹配不到即报错」一致）。

## 4. 页面设计

单页应用，四个视图，路由用 `location.hash`。整体信息架构与 TUI 一一对应，降低两个前端的认知/维护成本。

### 4.1 仪表盘（主视图，对应 TUI 主列表）

```
┌ gsyncer ────────────────────────────────── [设置] [全部同步] [＋新增] ┐
│                                                                      │
│  名称    主机              最近快照        份数  后端      状态       │
│  ─────────────────────────────────────────────────────────────────  │
│  web     example.com       2 小时前        23   hardlink  ● 空闲      │
│  db      10.0.0.5          3 天前 ⚠        41   btrfs     ● 同步中 ▶ │
│  logs    example.com       从未同步        —    —         ● 空闲      │
│                                                                      │
│  行内操作：[同步] [快照] [编辑] [复制] [删除]                          │
└──────────────────────────────────────────────────────────────────────┘
```

- 数据来自 `GET /api/status` + `GET /api/entries`，每 5s 轮询一次（有 running job 时加密度）；
- 「最近快照」超过 `--stale-hours` 语义的阈值时标黄 ⚠，把 `status` 命令的监控价值可视化；
- 「同步中」行显示进度指示并可点开跳到运行日志视图；
- 删除条目弹确认框，明确说明「仅移除配置，不删除本地已有快照数据」（与现有行为一致）。

### 4.2 条目表单（新增/编辑，对应 TUI form）

- 顶部保留**快速粘贴框**：粘贴 `user@host:/path` 或键值对，前端调 `/api/entries/parse` 回填各字段——这是 TUI 的招牌交互，GUI 必须保留；
- 字段分组：基本（name/host/port/user/identity）、路径（remote/local）、忽略规则（textarea，每行一条）、传输（compress/bwlimit）、钩子（pre/post_sync，旁边加「经 sh -c 执行」的提示）、保留策略（四个数字输入，留空回退 defaults，占位符显示当前 defaults 值）；
- 新增时预填默认端口/常见忽略规则/保留策略，与 TUI 相同；
- 保存前先调 `POST /api/check`（或复用 entries 接口的校验错误），字段级错误就地标红。

### 4.3 运行日志视图（对应 TUI run）

- 布局：左侧本次 job 涉及的条目列表（含各自 ✓/✗/进行中），右侧日志滚动区（SSE）；
- 自动滚动 + 用户上翻即暂停跟随（与 TUI 行为一致）；
- 顶部显示 job 汇总：开始时间、耗时、成功/失败数；失败条目一键「重试该条目」。

### 4.4 快照浏览（对应 TUI snapshots）

```
┌ 快照：web（23 份 · hardlink） ───────────── [按策略清理] [返回] ┐
│  2026-07-02_030000   ← 最新                 [恢复] [删除]      │
│  2026-07-01_030000                          [恢复] [删除]      │
│  2026-06-30_030000                          [恢复] [删除]      │
│  ...                                                           │
└────────────────────────────────────────────────────────────────┘
```

- 「按策略清理」先调 `dry_run` 展示将删除的具体时间戳列表，确认后执行；
- 「恢复」弹对话框：目标路径输入 + `force` 勾选；目标已存在且未勾 force 时把服务端 409 的提示原样展示。

### 4.5 设置视图

defaults（ssh_port/jobs/compress/bwlimit/hooks/retention）、log（keep_days/keep_count）、notify（开关/webhook/command）三段表单，读写 `/api/config/defaults`。

## 5. 安全

GUI 能改配置、能执行 `pre_sync`/`post_sync`（即任意命令），必须按「拿到界面 ≈ 拿到 shell」对待：

- **默认只监听 `127.0.0.1`**，远程使用推荐 ssh 端口转发（`ssh -L 8873:127.0.0.1:8873 host`），与工具的 ssh 中心工作流一致；
- `--listen` 指定非回环地址时，**强制要求 `--token`**（或 `GSYNCER_WEB_TOKEN`），否则拒绝启动；token 经 cookie（`HttpOnly` + `SameSite=Strict`）校验，首次访问 `/login` 输入；
- 写操作校验 `Origin`/`Sec-Fetch-Site` 头防 CSRF（配合 SameSite 双保险）;
- 不做 TLS（单文件工具自签证书体验差），文档明确：公网暴露请置于反代之后。

## 6. 实现分期

| 阶段 | 内容 | 备注 |
|------|------|------|
| M1 | `webui` 骨架：server + auth + `GET status/entries` + 仪表盘只读展示 | 打通 embed/路由/轮询 |
| M2 | jobs.go + `POST /api/sync` + SSE 日志 + 运行视图 | 核心价值点，优先于表单 |
| M3 | 条目 CRUD + 快速粘贴解析 + 表单校验 | 解析函数从 tui 提出共享 |
| M4 | 快照浏览/删除/清理/恢复（两段式确认） | |
| M5 | 设置视图 + 文档（README 增补「Web 界面」一节） | |

每阶段独立可交付；M1–M2 完成后即可日常使用（配置仍可用 TUI/手改）。

## 7. 对现有代码的影响

- 新增依赖：**零**（stdlib `net/http`、`embed` 足够；SSE 手写约 30 行）；
- `main.go` 增加 `web` 子命令分发；
- TUI form 的快速粘贴解析函数需提升为可共享（移到 `internal/config` 或新建 `internal/parse`），TUI 与 webui 共用，行为由现有 form_test 保障；
- `status.go` 的 JSON 结构体如在 main 包，提出到可被 webui 引用的位置；
- 构建：`build.sh` 无需改动，前端文件随 `go:embed` 自动进入产物。
