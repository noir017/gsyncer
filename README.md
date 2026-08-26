# gsyncer

一个带 **GFS 快照**的远程文件夹同步工具：通过 `ssh + rsync` 把远程服务器上的目录拉到本地，每次同步自动留一份带时间戳的快照，并按策略自动清理。

主要通过**交互式终端界面（TUI）**操作，也提供命令行子命令方便放进 cron / 脚本。编译产物是**零依赖的 Linux 静态单文件**，拷过去就能跑。

---

## 快速上手

### 1. 获取可执行文件

从 [Releases](../../releases) 下载对应架构的静态单文件即可，无需装 Go：

```bash
# 按需替换版本号与架构（linux-amd64 / linux-arm64 / linux-arm）
curl -LO https://github.com/noir017/gsyncer/releases/download/v0.1.0/gsyncer-v0.1.0-linux-amd64
curl -LO https://github.com/noir017/gsyncer/releases/download/v0.1.0/SHA256SUMS
sha256sum -c --ignore-missing SHA256SUMS      # 校验完整性
chmod +x gsyncer-v0.1.0-linux-amd64 && mv gsyncer-v0.1.0-linux-amd64 gsyncer
```

也可以自己编译（需要 Go 1.22+）：

```bash
./build.sh          # 产出 dist/gsyncer（linux/amd64 静态单文件）
```

把二进制拷到目标机器即可（无需安装任何依赖）。

> 运行前提：本机装有 `ssh`、`rsync`，远程主机装有 `rsync`。

### 2. 启动 TUI

不带参数直接运行就进入界面：

```bash
./gsyncer
```

首次启动条目列表是空的，界面底部会列出可用按键。

### 3. 新增一个同步条目

按 `a` 进入新增表单。表单顶部有一个**「快速粘贴」**框，可以直接粘贴连接串、回车自动拆分到各字段，省去逐项输入。比如粘贴：

```
deploy@example.com:/srv/www
```

回车后会自动填好「用户 / 主机 / 远程路径」，你只需再补上 **名称** 和 **本地路径**。也可以用键值对一次填全：

```
name=web host=example.com user=deploy remote=/srv/www local=/data/web
```

新增表单还会**预填常用默认值**（端口 22、常见忽略规则如 `node_modules/`、`__pycache__/`，以及一套保留策略），不想改就直接用。

填好后按 `ctrl+s` 保存，自动写回配置文件并返回列表。

### 4. 同步与查看快照

回到列表后：

- 选中条目按 `s` 同步（或 `S` 同步全部），运行日志实时滚动显示；
- 同步完成后按 `enter` 进入该条目的**快照列表**，可以查看历史、删除、按策略清理或恢复某一份。

一次典型流程就是：`a` 新增 → `ctrl+s` 保存 → `s` 同步 → `enter` 看快照。

---

## TUI 操作参考

### 主列表

| 键 | 作用 |
|----|------|
| `↑` / `↓` | 选择条目 |
| `enter` | 查看该条目的快照 |
| `a` | 新增条目 |
| `c` | 复制选中条目为新条目 |
| `e` | 编辑选中条目 |
| `d` | 删除选中条目 |
| `s` | 同步选中条目 |
| `S` | 同步全部条目 |
| `r` | 刷新（重新探测后端与快照数） |
| `?` | 显示/隐藏帮助 |
| `q` | 退出（回车即确认退出） |

### 新增 / 编辑表单

- `tab` / `shift+tab`（或 `↑` / `↓`）在字段间切换；
- 顶部「快速粘贴」框：粘贴后按 `enter` 自动解析填充，支持两种格式：
  - scp 简写：`user@host:/remote/path`
  - 键值对：`name=web host=1.2.3.4 port=22 user=deploy remote=/srv local=/data`
- 忽略规则是多行框，`↑` / `↓` 在框内移动光标，光标到首行 / 末行再按一次才会跳到相邻字段；
- 新增条目会预填默认端口、常见忽略规则、保留策略；
- `空格` 切换 strict host key、「归档」与「清远端」三个开关；两个数据模型开关会互相
  联动，避免拼出「镜像 + 删远端源」这个会毁掉备份的组合；
- `ctrl+s` 保存；`esc` 取消（有未保存改动会先提示）。

### 快照浏览

`↑` / `↓` 选择，`d` 删除，`p` 按策略清理，`x` 恢复，`esc` 返回。

删除（`d`）与清理（`p`）都会先提示确认：`p` 会显示「将删除 N 份，确认？(y/N)」，按 `y` 才真正删除。

---

## 命令行用法

适合放进 cron / 脚本，无需进入界面：

```bash
gsyncer init                       # 在默认位置写一份带注释的示例配置（-force 覆盖）
gsyncer sync                       # 同步全部条目
gsyncer sync --name web            # 只同步名为 web 的条目
gsyncer sync --server example.com  # 只同步该主机上的条目
gsyncer sync --dry-run             # rsync -n 预演，不写入、不快照
gsyncer sync --jobs 4              # 并发同步条目数（覆盖 defaults.jobs）

gsyncer list                       # 列出所有条目
gsyncer snapshots --name web       # 列出某条目的所有快照时间戳
gsyncer status                     # 各条目最近快照年龄/份数/后端（监控用）
gsyncer status --json              # 机器可读输出
gsyncer status --stale-hours 26    # 任一条目超期返回退出码 3（0 关闭该行为）

gsyncer prune                      # 按保留策略清理快照（可加 --name）
gsyncer prune --name web
gsyncer prune --dry-run            # 只打印将删除的快照，不实际删除

gsyncer restore --name web --latest --to /tmp/rec        # 恢复最新快照
gsyncer restore --name web --at 2026-06-24_030000 \
              --to /tmp/rec --force                     # 恢复指定快照并覆盖目标

gsyncer check                      # 只校验配置，不同步
gsyncer version                    # 版本号
gsyncer help                       # 显示帮助（也支持 -h / --help）
```

- 通用标志：`--config <path>` 指定配置文件。
- 退出码：任一条目失败返回非 0，方便脚本判断；`status --stale-hours` 超期返回 3。
- `restore`：需 `--name` 与 `--to`，并二选一 `--at <时间戳>` 或 `--latest`；不会覆盖
  `current/` 目录，目标已存在时须加 `--force`（先清空再 `cp -a`）。

定时同步示例（crontab，每天 3:00）：

```cron
0 3 * * * /usr/local/bin/gsyncer sync >> /var/log/gsyncer.log 2>&1
```

---

## 配置文件

配置为 TOML 格式。路径解析顺序：命令行 `--config <path>` 优先，否则取**可执行文件同目录**下的 `config.toml`。

> 通过 TUI 新增 / 编辑 / 删除条目会自动写回该文件，一般不用手写。下面的说明用于了解字段含义或脚本化生成配置。

### 完整示例

```toml
[defaults]
  ssh_port = 22                  # 条目未指定端口时的默认值（0 表示回退到 22）
  jobs     = 2                   # 并发同步的条目数（0 / 省略表示默认 2）
  compress = false               # rsync 传输压缩 -z（默认关；条目可覆盖）
  bwlimit  = 0                   # rsync 限速 KB/s，0 = 不限速（条目可覆盖）
  pre_sync  = ""                 # 每个条目 rsync 前执行的命令（sh -c）
  post_sync = ""                 # 同步成功后执行的命令（sh -c）
  local_mode          = "mirror" # 本地数据模型：mirror（默认）/ archive（条目可覆盖）
  remove_source_files = false    # 同步成功后是否删除远端源文件（条目可覆盖）
  [defaults.retention]           # 条目未覆盖时的默认保留策略
    recent     = 7
    monthly    = 6
    semiannual = 2
    yearly     = 2

[log]
  keep_days  = 30                # 运行日志保留天数（0 表示不按天清理）
  keep_count = 100               # 运行日志保留份数（0 表示不按份数清理）

[notify]                         # 运行结束通知（默认全关，两个开关都为 false 时不发）
  on_failure = true              # 有条目失败时通知
  on_success = false             # 全部成功时也通知
  webhook = "https://example.com/hook"                       # 收 JSON POST
  command = "echo \"$GSYNC_SUMMARY\" | mail -s gsyncer admin@x" # 走 sh -c

[[sync]]
  name        = "web"            # 唯一名称（必填）
  host        = "example.com"    # 远程主机（必填）
  port        = 22               # 该条目 ssh 端口；省略则用 defaults
  user        = "deploy"         # 远程用户（必填）
  identity    = "~/.ssh/id_rsa"  # ssh 私钥路径；留空则用 ssh 默认
  remote_path = "/srv/www"       # 远程目录（必填）
  local_path  = "/data/web"      # 本地目录（必填）
  ignore      = ["__pycache__/", "*.pyc", "node_modules/", ".git/"]
  strict_host_key = false        # false=accept-new，true=严格校验 host key
  compress    = true             # 可选：覆盖 defaults.compress（该条目启用 -z）
  bwlimit     = 2048             # 可选：覆盖 defaults.bwlimit
  pre_sync    = ""               # 可选：覆盖 defaults.pre_sync
  post_sync   = ""               # 可选：覆盖 defaults.post_sync
  [sync.retention]               # 可选：覆盖该条目的保留策略（留空字段回退到 defaults）
    recent     = 14
    monthly    = 12
    semiannual = 4
    yearly     = 5

# 归档模式：远端是滚动窗口，本地是永久归档
[[sync]]
  name        = "cam"
  host        = "192.168.1.50"
  user        = "root"
  remote_path = "/mnt/sd/record"
  local_path  = "/data/backups/cam"
  local_mode          = "archive"  # current/ 不做 --delete，只增不减
  remove_source_files = true       # 传输成功后删除远端源文件，给远端腾出空间
```

> 提示：`gsyncer init` 会生成一份带上述注释的起始配置，改好后用 `gsyncer check` 校验。

### 字段说明

| 字段 | 必填 | 说明 |
|------|:----:|------|
| `name` | ✓ | 条目唯一标识 |
| `host` | ✓ | 远程主机名 / IP |
| `user` | ✓ | 远程登录用户 |
| `remote_path` | ✓ | 远程源目录 |
| `local_path` | ✓ | 本地目标目录（快照存放处） |
| `port` | | ssh 端口，默认 `defaults.ssh_port` 或 22 |
| `identity` | | ssh 私钥；填写时该文件必须存在 |
| `ignore` | | gitignore 风格忽略规则，每行一条 |
| `strict_host_key` | | `true` 严格校验，`false` 自动接受新主机（默认） |
| `compress` | | 覆盖 `defaults.compress`：`true` 启用 rsync 压缩 `-z`，`false` 关闭 |
| `bwlimit` | | rsync 限速 KB/s（0 = 不限速）；覆盖 `defaults.bwlimit` |
| `pre_sync` | | rsync 前执行的命令；失败则跳过该条目（覆盖 `defaults.pre_sync`） |
| `post_sync` | | 同步成功后执行的命令；失败仅告警（覆盖 `defaults.post_sync`） |
| `local_mode` | | 本地数据模型：`"mirror"`（默认，`current/` 恒等于远端）或 `"archive"`（`current/` 只增不减）；覆盖 `defaults.local_mode` |
| `remove_source_files` | | 同步成功后是否删除远端源文件；覆盖 `defaults.remove_source_files` |
| `retention` | | 覆盖默认保留策略，未填字段回退到 `defaults.retention` |

`pre_sync` / `post_sync` 经 `sh -c` 执行，条目信息以环境变量传入：`GSYNC_NAME`、
`GSYNC_HOST`、`GSYNC_USER`、`GSYNC_REMOTE_PATH`、`GSYNC_LOCAL_PATH`、`GSYNC_PHASE`；
`post_sync` 另有 `GSYNC_SNAPSHOT` / `GSYNC_FILES` / `GSYNC_BYTES`。预演（`--dry-run`）不执行钩子。

> ⚠️ 钩子**不是事务性**的：`post_sync` 只在同步全程成功后运行。若 `pre_sync` 成功
> 后中途失败（rsync/快照报错），`post_sync` 不会执行。因此若用 `pre_sync` 停服务、
> `post_sync` 重启，中途失败会让服务停着。请让 `pre_sync` 的副作用可自恢复（例如用
> systemd 的 `RuntimeMaxSec` 自动重启），或把停/启逻辑放在 gsyncer 之外统一编排。

### 归档模式（`local_mode` / `remove_source_files`）

这两个字段互相**正交**：一个描述本地侧语义，一个描述远端侧行为。

| 字段 | 取值 | 含义 |
|------|------|------|
| `local_mode` | `"mirror"`（默认） | rsync 带 `--delete`，`current/` 恒等于远端；**远端是数据主体** |
| | `"archive"` | rsync 不带 `--delete`，`current/` 只增不减；**本地是数据主体** |
| `remove_source_files` | `false`（默认） | 不动远端 |
| | `true` | 传输成功后删除远端源文件（用 rsync 自己的 `--remove-source-files`） |

四种组合里三种有意义，第四种会被**配置校验直接拒绝**：

| 组合 | 结果 |
|------|------|
| `mirror` + 不删远端 | 经典镜像（默认行为） |
| `archive` + 不删远端 | 本地累积历史，远端原样不动 |
| `archive` + 删远端 | **远端滚动窗口 + 本地永久归档** |
| `mirror` + 删远端 | ❌ 加载时报错 |

最后一种不是"不推荐"，而是会**安静地毁掉备份**：第一轮同步把远端清空，第二轮的
`--delete` 就忠实地把这份"空"镜像回 `current/`，此时只剩快照里还有数据，而保留策略
会按计划把它们一一老化删除——每个部件都在按文档行事，备份却把自己删干净了。所以它
是一条加载期的硬错误，而不是文档里的一句提醒。

**删远端用的是 rsync 自己的 `--remove-source-files`**：rsync 只删*确认传输成功*的
文件，这个判定它自己最清楚，比在外部用"文件清单 + 时间戳"反推准确得多，也不需要在
被备份的机器上额外部署脚本。

**空目录清理**：rsync 只删文件不删目录，所以 `archive` + 删远端的场景下远端会累积空
目录（典型如每天一个日期目录）。gsyncer 会在**传输确实成功后**补一次清理，只删已经
为空的目录，且 `-mindepth 1` 保证 `remote_path` 本身不会被删掉；因此任何没传走的文件
（被忽略规则排除、或传输失败）都会撑住它的父目录，不会被误删。清理失败只告警，不影响
备份结果（数据已经在本地了）。

**预演（`--dry-run`）不删任何东西**：rsync 在 `-n` 下 `--remove-source-files` 本身就是
空操作，而空目录清理会被整个跳过——不存在"安全版的删除"。

#### 归档模式下 GFS 保留策略恢复了正常语义

这是本次改动最大的收益，值得单独说清楚：

- **镜像模式**下，`current/` 只是远端的副本，历史**只存在于快照里**。要长期留存就只能
  把 `retention` 拉到极大值，GFS 策略实际上等于废掉——谁改一下配置就真的丢数据。
- **归档模式**下，`current/` 本身就是数据主体，快照只是"某个时间点的视图"。
  **删掉一份快照不丢任何数据**，被删的文件仍然在 `current/` 里。

所以在归档模式下，请按正常的 GFS 思路配置 `retention`（例如默认的 `recent=7` /
`monthly=6`），**不要**再把它拉满。快照在这里的作用是"回到某天的目录状态"，而不是
"唯一的数据副本"。

### `[notify]` 字段
| 字段 | 说明 |
|------|------|
| `on_failure` | 有条目失败时通知（默认 `false`） |
| `on_success` | 全部成功时也通知（默认 `false`） |
| `webhook` | 通知地址；收到 JSON POST（须 `http://` / `https://`） |
| `command` | 通知命令，经 `sh -c` 执行 |

通知命令可用环境变量：`GSYNC_STATUS`（`success`/`failure`）、`GSYNC_OK`、
`GSYNC_FAILED`、`GSYNC_SKIPPED`、`GSYNC_SUMMARY`（一句话摘要）、`GSYNC_JSON`（完整 JSON）。
webhook 的 JSON 含每条目的 `host`/`ok`/`error`/`files`/`bytes`/`duration_sec`。

---

## 工作原理

对每个同步条目，依次执行：

1. **预检**：检查本地 `rsync` 是否可用（远程 `rsync` 不再单独探测，省一次 ssh 握手；若远程缺失，rsync 自身会以 127/"command not found" 失败并给出安装提示）。
2. **拉取**：`rsync -a --delete` 把 `user@host:remote_path/` 同步到本地 `local_path/current/`，并应用忽略规则。传输恒带 `--partial`（配合 `--partial-dir=.gsyncer-partial` 断点续传、半成品不落入 `current/`）与 `--numeric-ids`（按数字保留 uid/gid）；`compress` 开启时附加 `-z`。归档模式（`local_mode = "archive"`）下**不带** `--delete`，`current/` 只增不减；`remove_source_files = true` 时附加 `--remove-source-files`，并在传输成功后清理远端遗留的空目录。
3. **快照**：把 `current/` 快照到 `local_path/snapshots/<时间戳>/`
   - 默认用**硬链接**后端：未改动的文件与上一份共享 inode，几乎不额外占空间；在支持 reflink 的 CoW 文件系统（如 xfs reflink、bcachefs）上自动升级为 **reflink** 拷贝——每份快照有独立 inode、数据块按需写时复制，既省空间又不会因就地改写 `current/` 里的文件而牵连旧快照（run 汇总里模式会显示 `reflink`）；
   - 若 `local_path` 在 btrfs 上且系统有 `btrfs` 命令，则用 **btrfs** 子卷快照。
4. **清理**：按 GFS 保留策略删除超出范围的旧快照。

本地目录结构：

```
local_path/
├── current/                     # 镜像模式：与远程一致的最新镜像
│                                # 归档模式：累积至今的全部历史（数据主体）
└── snapshots/
    ├── 2026-06-24_030000/       # 历史快照（时间戳目录）
    └── 2026-06-24_153000/
```

### 保留策略（GFS）

保留集合为以下四层的**并集**（按时间从新到旧）：

- **recent**：最近的 N 份快照；
- **monthly**：在**含有快照**的自然月中取最近的 N 个月，每月保留最新的一份（无快照的月份不计入 N）；
- **semiannual**：在**含有快照**的半年期（1–6 月 / 7–12 月）中取最近的 N 个期，每期保留最新的一份（无快照的半年期不计入 N）；
- **yearly**：在**含有快照**的自然年中取最近的 N 个年，每年保留最新的一份（无快照的年份不计入 N）。

这里的「最近」是相对于**已有快照集合**而言（并非按当前日期回溯的自然周期），且**最新的一份快照始终保留**（安全下限，避免清空刚创建的快照）。不在保留集合中的快照会在 `sync` 末尾或 `prune` 时删除。某层计数为 `0` 表示不保留该层。

保留策略的**后果取决于 `local_mode`**：

- **镜像模式**：`current/` 只是远端的副本，历史只存在于快照里，因此删快照 = 丢历史；
- **归档模式**：`current/` 才是数据主体，**删掉一份快照不丢任何数据**，只是少了一个
  可回溯的时间点。这一模式下 GFS 恢复了它本来的语义，按常规值配置即可，
  不需要把 `retention` 拉满。详见 [归档模式](#归档模式local_mode--remove_source_files)。

### 日志

每次 `sync` / `prune` 在可执行文件同目录的 `logs/` 下生成一份运行日志，并追加一行汇总；旧日志按 `[log]` 的 `keep_days` / `keep_count` 自动清理。

---

## 编译细节与开发

```bash
./build.sh                        # 默认 dist/gsyncer (linux/amd64)
./build.sh /usr/local/bin/gsyncer   # 指定输出路径
GOARCH=arm64 ./build.sh           # 交叉编译 arm64
VERSION=1.2.3 ./build.sh          # 把版本号写进 `gsyncer version`
```

脚本用 `CGO_ENABLED=0` + `-ldflags "-s -w" -trimpath` 产出静态、精简、可复现的二进制，并自动校验 `ldd` 为 `not a dynamic executable`。也可直接 `CGO_ENABLED=0 go build -o gsyncer .`。

`VERSION` 通过 `-X main.version=` 注入，不填就用源码里的默认值——发版由 CI 自动填入标签名，本地构建不用管。

### 持续集成与发版

仓库同时带了 Gitea（`.gitea/workflows/`）和 GitHub（`.github/workflows/`）两套流水线，检查项一致。

**`ci.yml`** — 推分支或提 PR 时跑：`gofmt` 格式门禁 → `go mod verify` → `go vet ./...` → 静态编译 → `go test -race -count=1 ./...`。产物会作为 artifact 保留 14 天，便于从任意提交取一份能跑的二进制。

**`release.yml`** — 推 `v*` 标签时跑：先用 `workflow_call` 完整复用一遍 `ci.yml`，**测试不过就不会有 Release**；随后交叉编译 `linux/amd64`、`linux/arm64`、`linux/arm`（armv7），把标签名注入版本号，冒烟测试 amd64 产物（确认静态链接且版本号正确），最后连同 `SHA256SUMS` 一起发到 GitHub Releases。

```bash
git tag v0.1.0 && git push origin v0.1.0     # 触发发版
```

标签名带 `-` 的（如 `v0.2.0-rc1`）自动标记为预发布，不会顶掉 latest。若某次 Release 创建失败，可用 workflow_dispatch 指定已有标签重跑，产物会覆盖上传而不动已有的说明文字。

### 运行测试

日常测试不依赖任何服务器，在项目根目录一条命令即可，每行前显示 `ok` 即全部通过：

```bash
go test ./...              # 运行全部单元测试
go test -cover ./...       # 顺便看每个包的覆盖率
go test -v ./...           # 显示每个用例的名字与结果
go test ./internal/syncer  # 只跑某一个包
```

想和 CI 完全一致（额外开启数据竞争检测）：

```bash
go test -race -count=1 ./...
```

> `go test` 会缓存结果，未改动的包显示 `(cached)` 直接跳过；加 `-count=1` 可强制重跑。
> 提交前也可跑一遍 `go vet ./...` 做静态检查（CI 里同样会跑）。

### 端到端（e2e）测试

端到端测试放在 **`e2e/` 子目录**（独立的 `e2e` 包），用 `e2e` 构建标签隔离，默认的 `go test ./...` **不会**跑它——它需要真实的 `ssh`/`rsync` 与可登录的服务器，跑完整的「拉取 → 快照 → 恢复 → 清理」流水线并断言磁盘结果。这些用例是黑盒的：从模块根目录编译出二进制再执行，不依赖任何内部包。

服务器通过**本地配置文件**提供，仓库里不含任何主机名或凭据。把示例复制一份填上自己的服务器即可：

```bash
cp e2e/e2e.config.example.toml e2e/e2e.config.toml   # 编辑填入你的服务器
go test -tags e2e ./e2e -v
```

`e2e/e2e.config.toml` 已在 `.gitignore` 中（不会提交）；也可用 `GSYNC_E2E_CONFIG` 指向别处的配置。配置全部可选：

- `[[server]]`：装有 rsync 的主机，可写**零个或多个**，流水线对每台各跑一遍；一个都没有则 `TestE2E` 跳过。
- `[[no_rsync_server]]`：**未装** rsync 的主机，可写零个或多个，用于验证失败路径退出非 0；没有则该测试跳过。

字段：`host`（必填）、`user`（必填）、`port`（默认 22）、`identity`（可选，留空则用 ssh 自身的 `~/.ssh/config`）、`remote_base`（可选，测试临时目录的父路径，默认 `/tmp`）。每个用例在远端建独立的临时目录、结束时自动删除；不可达的主机会被单独跳过，所以部分配置也能跑。

代码结构：`internal/config`（配置）、`internal/syncer`（同步流水线）、`internal/snapshot`（硬链接 / btrfs 后端）、`internal/retention`（GFS 策略）、`internal/tui`（界面）、`internal/logx`、`internal/ignore`、`internal/execx`。

---

## 许可证

Copyright (C) 2026 noir017

本项目采用 **GNU 通用公共许可证第 3 版或任意更新版本**（GPL-3.0-or-later）授权。

本程序是自由软件：你可以依照自由软件基金会发布的 GNU 通用公共许可证的条款重新发布和/或修改它，可以选择使用该许可证的第 3 版，或（由你选择）任何更新的版本。

发布本程序是希望它能派上用场，但**不作任何担保**；甚至不包含对**适销性**或**特定用途适用性**的默示担保。详见 GNU 通用公共许可证。

你应当已随本程序收到一份 GNU 通用公共许可证的副本（见仓库中的 [LICENSE](LICENSE) 文件）。如果没有，请查阅 <https://www.gnu.org/licenses/>。
