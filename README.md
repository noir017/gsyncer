# gsync

一个带 **GFS 快照**的远程文件夹同步工具：通过 `ssh + rsync` 把远程服务器上的目录拉到本地，每次同步自动留一份带时间戳的快照，并按策略自动清理。

主要通过**交互式终端界面（TUI）**操作，也提供命令行子命令方便放进 cron / 脚本。编译产物是**零依赖的 Linux 静态单文件**，拷过去就能跑。

---

## 快速上手

### 1. 获取可执行文件

需要 Go 1.22+，用仓库自带脚本编译：

```bash
./build.sh          # 产出 dist/gsync（linux/amd64 静态单文件）
```

把 `dist/gsync` 拷到目标机器即可（无需安装任何依赖）。

> 运行前提：本机装有 `ssh`、`rsync`，远程主机装有 `rsync`。

### 2. 启动 TUI

不带参数直接运行就进入界面：

```bash
./gsync
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
- `空格` 切换 strict host key；`ctrl+s` 保存；`esc` 取消（有未保存改动会先提示）。

### 快照浏览

`↑` / `↓` 选择，`d` 删除，`p` 按策略清理，`x` 恢复，`esc` 返回。

---

## 命令行用法

适合放进 cron / 脚本，无需进入界面：

```bash
gsync sync                       # 同步全部条目
gsync sync --name web            # 只同步名为 web 的条目
gsync sync --server example.com  # 只同步该主机上的条目
gsync sync --dry-run             # rsync -n 预演，不写入、不快照
gsync sync --jobs 4              # 并发同步条目数（覆盖 defaults.jobs）

gsync list                       # 列出所有条目
gsync snapshots --name web       # 列出某条目的所有快照时间戳
gsync prune                      # 按保留策略清理快照（可加 --name）
gsync prune --name web

gsync version                    # 版本号
```

- 通用标志：`--config <path>` 指定配置文件。
- 退出码：任一条目失败返回非 0，方便脚本判断。

定时同步示例（crontab，每天 3:00）：

```cron
0 3 * * * /usr/local/bin/gsync sync >> /var/log/gsync.log 2>&1
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
  [defaults.retention]           # 条目未覆盖时的默认保留策略
    recent     = 7
    monthly    = 6
    semiannual = 2
    yearly     = 2

[log]
  keep_days  = 30                # 运行日志保留天数（0 表示不按天清理）
  keep_count = 100               # 运行日志保留份数（0 表示不按份数清理）

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
  [sync.retention]               # 可选：覆盖该条目的保留策略（留空字段回退到 defaults）
    recent     = 14
    monthly    = 12
    semiannual = 4
    yearly     = 5
```

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
| `retention` | | 覆盖默认保留策略，未填字段回退到 `defaults.retention` |

---

## 工作原理

对每个同步条目，依次执行：

1. **预检**：检查本地 `rsync`、远程 `rsync`（通过 `ssh` 探测）是否可用。
2. **拉取**：`rsync -a --delete` 把 `user@host:remote_path/` 同步到本地 `local_path/current/`，并应用忽略规则。
3. **快照**：把 `current/` 快照到 `local_path/snapshots/<时间戳>/`
   - 默认用**硬链接**后端：未改动的文件与上一份共享 inode，几乎不额外占空间；
   - 若 `local_path` 在 btrfs 上且系统有 `btrfs` 命令，则用 **btrfs** 子卷快照。
4. **清理**：按 GFS 保留策略删除超出范围的旧快照。

本地目录结构：

```
local_path/
├── current/                     # 与远程一致的最新镜像
└── snapshots/
    ├── 2026-06-24_030000/       # 历史快照（时间戳目录）
    └── 2026-06-24_153000/
```

### 保留策略（GFS）

保留集合为以下四层的**并集**（按时间从新到旧）：

- **recent**：最近的 N 份快照；
- **monthly**：最近 N 个自然月中，每月保留最新的一份；
- **semiannual**：最近 N 个半年期中，每期保留最新的一份；
- **yearly**：最近 N 个自然年中，每年保留最新的一份。

不在保留集合中的快照会在 `sync` 末尾或 `prune` 时删除。某层计数为 `0` 表示不保留该层。

### 日志

每次 `sync` / `prune` 在可执行文件同目录的 `logs/` 下生成一份运行日志，并追加一行汇总；旧日志按 `[log]` 的 `keep_days` / `keep_count` 自动清理。

---

## 编译细节与开发

```bash
./build.sh                        # 默认 dist/gsync (linux/amd64)
./build.sh /usr/local/bin/gsync   # 指定输出路径
GOARCH=arm64 ./build.sh           # 交叉编译 arm64
```

脚本用 `CGO_ENABLED=0` + `-ldflags "-s -w" -trimpath` 产出静态、精简、可复现的二进制，并自动校验 `ldd` 为 `not a dynamic executable`。也可直接 `CGO_ENABLED=0 go build -o gsync .`。

```bash
go test ./...      # 运行全部单元测试
go vet ./...
```

代码结构：`internal/config`（配置）、`internal/syncer`（同步流水线）、`internal/snapshot`（硬链接 / btrfs 后端）、`internal/retention`（GFS 策略）、`internal/tui`（界面）、`internal/logx`、`internal/ignore`、`internal/execx`。
