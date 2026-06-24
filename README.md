# gsync

通过 `ssh + rsync` 从远程服务器拉取文件夹，并在本地保留 **GFS（祖父-父-子）快照**的同步工具。
提供交互式 TUI 与脚本友好的命令行两种用法，编译产物为**零依赖的 Linux 静态单文件**。

---

## 特性

- **增量拉取**：底层用 `rsync -a --delete`，仅传输变化部分。
- **快照保留**：每次同步后基于硬链接（或 btrfs）创建一份时间戳快照，几乎不额外占用空间。
- **GFS 保留策略**：按「最近 N 份 + 每月/每半年/每年各保留最新一份」自动清理旧快照。
- **gitignore 风格忽略规则**：每行一条，自动转换为 rsync 过滤器。
- **交互式 TUI**：列表、编辑、运行、快照浏览/恢复一站式操作。
- **零依赖部署**：`CGO_ENABLED=0` 静态编译，拷贝到任意 x86_64 Linux（含 Alpine 等 musl 系统）即可运行。

> 运行**前提**：本机与远程主机都已安装 `rsync`，且本机已安装 `ssh` 客户端；btrfs 快照后端额外需要 `btrfs` 命令。

---

## 工作原理

对每个同步条目，依次执行：

1. **预检**：检查本地 `rsync`、远程 `rsync`（通过 `ssh` 探测）。
2. **拉取**：`rsync` 将 `user@host:remote_path/` 同步到本地 `local_path/current/`。
3. **快照**：把 `current/` 快照到 `local_path/snapshots/<时间戳>/`
   - 默认使用**硬链接**后端（未改动的文件与上一份共享 inode）；
   - 若 `local_path` 位于 btrfs 且系统有 `btrfs` 命令，则使用 **btrfs** 子卷快照。
4. **清理**：按保留策略删除超出范围的旧快照。

本地目录结构：

```
local_path/
├── current/                     # 与远程一致的最新镜像
└── snapshots/
    ├── 2026-06-24_030000/       # 历史快照（时间戳目录）
    └── 2026-06-24_153000/
```

---

## 编译

需要 Go 1.22+。仓库自带编译脚本 `build.sh`：

```bash
./build.sh                 # 默认输出 dist/gsync (linux/amd64)
./build.sh /usr/local/bin/gsync   # 指定输出路径
GOARCH=arm64 ./build.sh    # 交叉编译 arm64
```

脚本使用 `CGO_ENABLED=0` + `-ldflags "-s -w" -trimpath` 产出静态、精简、可复现的二进制，并自动校验 `ldd` 是否为 `not a dynamic executable`。

也可直接用 go：

```bash
CGO_ENABLED=0 go build -o gsync .
```

---

## 配置

配置文件为 TOML。路径解析顺序：

1. 命令行 `--config <path>`；
2. 否则默认取**可执行文件同目录**下的 `config.toml`。

> TUI 中新增/编辑/删除条目会自动写回该文件，通常无需手写。

### 完整示例

```toml
[defaults]
  ssh_port = 22                  # 条目未指定端口时的默认值（0 表示回退到 22）
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

## 使用

### 交互式 TUI

不带任何参数直接运行即进入 TUI：

```bash
gsync
```

**主列表按键**

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
| `q` | 退出（回车默认确认退出） |

**编辑/新增表单**

- `tab` / `shift+tab`（或 `↑`/`↓`）在字段间切换；
- 忽略规则为多行框，`↑`/`↓` 在框内移动光标，到首/末行再按则跳出到相邻字段；
- 新增条目会**预填默认值**（端口、常见忽略规则、保留策略），减少手动输入；
- 顶部「快速粘贴」框可粘贴连接串后按 `enter` 自动解析填充，支持两种格式：
  - scp 简写：`user@host:/remote/path`
  - 键值对：`name=web host=1.2.3.4 port=22 user=deploy remote=/srv local=/data`
- `空格` 切换 strict host key；`ctrl+s` 保存；`esc` 取消（有未保存改动会提示）。

**快照浏览**：`↑`/`↓` 选择，`d` 删除，`p` 按策略清理，`x` 恢复，`esc` 返回。

### 命令行

适合放进 cron / 脚本：

```bash
gsync sync                       # 同步全部条目
gsync sync --name web            # 只同步名为 web 的条目
gsync sync --server example.com  # 只同步该主机上的条目
gsync sync --dry-run             # rsync -n 预演，不写入、不快照

gsync list                       # 列出所有条目
gsync snapshots --name web       # 列出某条目的所有快照时间戳
gsync prune                      # 按保留策略清理快照（可加 --name）
gsync prune --name web

gsync version                    # 版本号
```

通用标志：`--config <path>` 指定配置文件。

退出码：任一条目失败返回非 0。

---

## 保留策略（GFS）

保留集合为以下四层的**并集**（按时间从新到旧）：

- **recent**：最近的 N 份快照；
- **monthly**：最近 N 个自然月中，每月保留最新的一份；
- **semiannual**：最近 N 个半年期中，每期保留最新的一份；
- **yearly**：最近 N 个自然年中，每年保留最新的一份。

不在保留集合中的快照会在 `sync` 末尾或 `prune` 时被删除。所有计数为 `0` 表示该层不保留。

---

## 日志

每次 `sync` / `prune` 在可执行文件同目录的 `logs/` 下生成一份运行日志，并追加一行汇总。
旧日志按 `[log]` 中的 `keep_days` / `keep_count` 自动清理。

---

## 开发

```bash
go test ./...      # 运行全部单元测试
go vet ./...
```

代码结构：`internal/config`（配置）、`internal/syncer`（同步流水线）、`internal/snapshot`（硬链接/btrfs 后端）、`internal/retention`（GFS 策略）、`internal/tui`（界面）、`internal/logx`、`internal/ignore`、`internal/execx`。
