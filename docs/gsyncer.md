设计：gsyncer（暂名）—— 基于 rsync/ssh 的多服务器拉取+快照工具

1. 总体架构与模块（Go，零依赖静态单文件）

main.go            CLI 入口、参数解析、装配
internal/config    TOML 读写、校验、默认值合并
internal/syncer    单条目编排：预检 → rsync 拉取 → 快照 → 保留清理
internal/snapshot  后端抽象接口 + hardlink 后端 + btrfs 后端 + 后端探测
internal/retention GFS 保留算法（纯函数，易测）
internal/ignore    gitignore 风格 → rsync filter 翻译
internal/logx      每次运行日志、单行汇总、旧日志清理
internal/tui       bubbletea 界面（主菜单/编辑/运行/快照浏览）

外部依赖仅两个：charmbracelet/bubbletea（TUI）、BurntSushi/toml。CGO_ENABLED=0 go build 产出零依赖静态单文件，GOOS/GOARCH 一行交叉编译。rsync/ssh 仍是被调用的外部二进制（符合「单文件」指你的程序本身无依赖）。

2. 配置（TOML，与可执行文件同目录 config.toml）

[defaults]
ssh_port = 22
[defaults.retention]        # 四层保留默认值，条目可覆盖
recent     = 7              # 最近 N 份
monthly    = 6              # 每月各留 1，留最近 6 个月
semiannual = 4              # 每半年各留 1，留最近 4 个半年
yearly     = 3              # 每年各留 1，留最近 3 年

[[sync]]
name        = "web1-www"            # 唯一 ID，--name 用它
host        = "1.2.3.4"
port        = 22
user        = "deploy"
identity    = "~/.ssh/id_ed25519"
remote_path = "/var/www/"
local_path  = "/data/backups/web1-www"   # 本条目根，下设 current/ 与 snapshots/
ignore      = ["*.log", "cache/", "!cache/keep.txt"]
[sync.retention]                    # 可选，覆盖默认
recent = 14

每个条目在 local_path/ 下生成 current/（最新镜像）与 snapshots/YYYY-MM-DD_HHMMSS/。

3. 同步 + 快照流程（每条目，互相隔离）

1. 预检：本地 command -v rsync；远程 ssh ... command -v rsync。任一缺失 → 记错误 + 打印该系统安装命令（识别 apt/dnf/yum/apk/pacman），跳过本条目并计为失败。
2. 拉取：rsync -a --delete --info=stats2 --filter=<翻译后规则> -e "ssh -p PORT -i KEY -o BatchMode=yes" user@host:remote_path/ local_path/current/。单向拉取镜像；--delete 让 current 精确等于远程（被删的文件在 current 消失，但仍存在于旧快照——这正是备份价值）。
3. 快照（仅 rsync 成功后）：btrfs 后端 btrfs subvolume snapshot -r current snapshots/<ts>；hardlink 后端 cp -al current snapshots/<ts>。
4. 保留清理：对该条目 snapshots 目录跑 GFS 算法（见 §4）。
5. 记录：写入该条目的传输统计、快照结果、清理结果。

后端探测：对 local_path 做 statfs，若文件系统为 btrfs 且 btrfs 命令可用 → btrfs 后端（首次把 current 建为 subvolume），否则 hardlink。生效后端在日志和 TUI 显著标明（snapshot mode: btrfs native / hardlink）。同一路径混用后端会告警。

4. 保留算法（GFS 四层并集）

解析快照目录名得时间戳，计算保留集：
- 最近层：按时间降序，留最新 recent 份。
- 月层：按 (年,月) 分桶，取最近 monthly 个桶，每桶留最新一份。
- 半年层：按 (年, 上/下半年) 分桶，取最近 semiannual 个桶，每桶留最新。
- 年层：按 年 分桶，取最近 yearly 个桶，每桶留最新。

保留 = 四层并集；其余删除（btrfs subvolume delete / hardlink rm -rf）。一份快照可同时满足多层。纯函数实现，单测覆盖。

5. CLI 与 TUI

CLI（cron 用，无需确认）
gsyncer                      进入 TUI（人工调试）
gsyncer sync                 同步全部条目（cron 默认）
gsyncer sync --name web1-www 仅该条目
gsyncer sync --server 1.2.3.4 仅该服务器全部条目
gsyncer sync --dry-run       rsync -n，不建快照
gsyncer list                 打印条目
gsyncer snapshots --name X   列出快照（非 TUI）
gsyncer prune [--name X]     仅跑保留清理
全局: --config PATH  --verbose  --log-level
默认 config 与 logs 都定位到「可执行文件所在目录」（os.Executable()）。

TUI（bubbletea）：主菜单列出所有条目（状态/上次运行/快照数/生效后端）→ 增删改条目表单（含 ignore 编辑、retention 覆盖）→ 手动运行并实时看输出 → 快照浏览面板（按条目列快照、时间、占用大小、手动删除/恢复/触发清理）。

6. 日志与错误处理

- 每次运行写 logs/YYYY-MM-DD_HHMMSS.log：各条目起止、rsync 统计摘要（文件数/字节数）、快照与清理结果、错误详情。
- 运行结束在主汇总日志追加单行：成功 N / 失败 M / 耗时 Ts。
- rsync 逐文件输出默认不入日志，仅 --verbose / TUI 调试时显示。
- 旧日志按数量或天数清理（可配置）。
- 条目级隔离：一条失败不影响其余；任一失败则进程退出码非 0（便于 cron 监控）。

7. 其余决定

- 方向：仅远程→本地单向拉取镜像。
- SSH：走系统 ssh，cron 下 BatchMode=yes 不交互；host key 默认沿用用户 known_hosts，可选每条目 strict_host_key 开关。
- 忽略规则：支持 gitignore 实用子集（通配、目录尾斜杠、前导斜杠锚定、! 取反），翻译为 rsync --filter 有序规则；文档说明映射边界。
- 并发：条目默认顺序执行（避免带宽/IO 争抢、日志清晰），并行留作未来 --parallel。