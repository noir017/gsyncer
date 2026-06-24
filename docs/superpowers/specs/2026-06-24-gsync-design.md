# gsync 设计文档

> 基于 rsync/ssh 的多服务器文件拉取 + 快照工具
> 日期：2026-06-24
> 状态：已批准设计，待实现计划

## 1. 目标与约束

构建一个命令行工具，通过 ssh + rsync 把**多个远程服务器**上的指定文件夹**单向拉取**到本地，并维护**带保留策略的历史快照**。

硬性约束：

- **单文件运行**：产出零运行时依赖的静态二进制，兼容精简 Linux 系统。
- **可定时无人值守**：带参数运行即可，无需任何交互确认；TUI 仅用于人工配置/调试。
- **配置人类可读**：配置文件以 TOML 保存在可执行文件同目录。
- rsync / ssh 作为外部二进制被调用（"单文件" 指本程序自身无依赖，不含打包 rsync）。

实现语言：**Go**（`CGO_ENABLED=0` 静态单文件，交叉编译简单，调用外部命令顺手，bubbletea TUI 成熟）。

外部依赖（编译期，静态链接进单文件）：

- `github.com/charmbracelet/bubbletea`（+ bubbles / lipgloss）—— TUI
- `github.com/BurntSushi/toml` —— 配置读写

## 2. 模块划分

```
main.go            CLI 入口、参数解析、装配
internal/config    TOML 读写、校验、默认值合并
internal/syncer    单条目编排：预检 → rsync 拉取 → 快照 → 保留清理
internal/snapshot  后端抽象接口 + hardlink 后端 + btrfs 后端 + 后端探测
internal/retention GFS 保留算法（纯函数，易测）
internal/ignore    gitignore 风格 → rsync filter 翻译
internal/logx      每次运行日志、单行汇总、旧日志清理
internal/tui       bubbletea 界面（主菜单 / 编辑 / 运行 / 快照浏览）
```

设计原则：每个模块单一职责、通过明确接口通信、可独立测试。`snapshot` 的后端抽象是关键边界，使 hardlink 与 btrfs 两种实现可互换且各自可测；`retention` 为纯函数，输入快照时间戳列表与策略、输出保留/删除集合。

## 3. 配置格式（TOML）

默认路径：可执行文件所在目录下的 `config.toml`（通过 `os.Executable()` 定位）。可用 `--config PATH` 覆盖。

```toml
[defaults]
ssh_port = 22

[defaults.retention]        # 四层保留默认值，单条目可覆盖
recent     = 7              # 最近 N 份（不论日期）
monthly    = 6              # 每月留 1，留最近 6 个月
semiannual = 4              # 每半年留 1，留最近 4 个半年
yearly     = 3              # 每年留 1，留最近 3 年

[log]
keep_days  = 30             # 旧日志保留天数（与 keep_count 取其一/并用）
keep_count = 100            # 旧日志保留文件数

[[sync]]
name        = "web1-www"            # 唯一 ID，--name 选择用它
host        = "1.2.3.4"
port        = 22                    # 可省略则用 defaults.ssh_port
user        = "deploy"
identity    = "~/.ssh/id_ed25519"   # ssh 私钥路径，支持 ~ 展开
remote_path = "/var/www/"
local_path  = "/data/backups/web1-www"   # 本条目根，下设 current/ 与 snapshots/
ignore      = ["*.log", "cache/", "!cache/keep.txt"]
strict_host_key = false             # 可选，映射 ssh StrictHostKeyChecking

[sync.retention]                    # 可选，覆盖 defaults.retention 中提供的字段
recent = 14
```

布局：每个条目在 `local_path/` 下生成

```
local_path/
  current/                       # rsync 目标，永远是远程最新镜像
  snapshots/
    2026-06-24_030000/           # 历史快照（硬链接或 btrfs subvolume）
    2026-05-24_030000/
```

校验规则：`name` 唯一且非空；`host`/`user`/`remote_path`/`local_path` 必填；`identity` 文件存在；retention 各值为非负整数。校验失败给出明确的字段级错误。

## 4. 同步 + 快照流程

对每个被选中的条目，互相隔离地依次执行：

1. **预检**
   - 本地 `command -v rsync`。
   - 远程 `ssh <opts> user@host command -v rsync`。
   - 任一缺失：记录错误 + 打印该系统的安装命令（识别 apt / dnf / yum / apk / pacman），跳过本条目并计为失败。**不自动安装、不碰权限**（轻量级策略）。

2. **拉取**

   ```
   rsync -a --delete --info=stats2 \
     --filter='<由 ignore 翻译的有序规则>' \
     -e 'ssh -p <port> -i <identity> -o BatchMode=yes [-o StrictHostKeyChecking=...]' \
     <user>@<host>:<remote_path>/ <local_path>/current/
   ```

   - 单向拉取镜像；`--delete` 使 `current/` 精确等于远程：远程删除的文件在 `current/` 消失，但仍存在于旧快照中——这正是备份价值所在。
   - `BatchMode=yes` 保证 cron 下不交互。
   - 从 `--info=stats2` 解析传输统计（文件数、字节数）。

3. **快照**（仅当 rsync 成功）
   - 命名 `snapshots/<YYYY-MM-DD_HHMMSS>`。
   - btrfs 后端：`btrfs subvolume snapshot -r current snapshots/<ts>`。
   - hardlink 后端：`cp -al current snapshots/<ts>`（未变文件为指向 current 的硬链接，零额外占用）。

4. **保留清理**：对该条目 `snapshots/` 跑 GFS 算法（见 §5）。

5. **记录**：写入本条目传输统计、快照创建结果、清理结果。

### 后端探测

对 `local_path` 做 `statfs`，若文件系统魔数为 btrfs（`0x9123683E`）且 `btrfs` 命令可用，则选 **btrfs 后端**（首次运行把 `current` 建为 subvolume）；否则 **hardlink 后端**。

- 生效后端在**日志和 TUI 中显著标明**：`snapshot mode: btrfs native` / `snapshot mode: hardlink`。
- 若 `current` 已作为普通目录存在于 btrfs 上（例如先前用 hardlink 模式建立），无法直接 snapshot：发出告警并回退 hardlink，避免静默混用。

## 5. 保留算法（GFS 四层并集）

输入：快照目录名解析出的时间戳列表 + 策略 `{recent, monthly, semiannual, yearly}`。输出：保留集合与删除集合。

- **最近层**：按时间降序，保留最新 `recent` 份。
- **月层**：按 `(年, 月)` 分桶，取最近 `monthly` 个桶，每桶保留最新一份。
- **半年层**：按 `(年, 上/下半年)`（月 ≤ 6 为上半年）分桶，取最近 `semiannual` 个桶，每桶保留最新一份。
- **年层**：按 `年` 分桶，取最近 `yearly` 个桶，每桶保留最新一份。

最终**保留 = 四层并集**；其余删除。一份快照可同时满足多层。删除操作：btrfs `btrfs subvolume delete`；hardlink `rm -rf`。

实现为纯函数，单元测试覆盖典型与边界场景（空列表、同月多份坍缩、跨年、各层为 0）。

> 说明（非缺陷）：快照每次 cron 运行都打，若 cron 频率很高，「最近层」与「月层」之间可能出现覆盖空档。通过按实际 cron 频率调大 `recent` 解决。算法语义如上明确。

## 6. CLI 与 TUI

### CLI（定时/无人值守）

```
gsync                        进入 TUI（人工调试）
gsync sync                   同步全部条目（cron 默认）
gsync sync --name web1-www   仅该条目
gsync sync --server 1.2.3.4  仅该服务器下全部条目
gsync sync --dry-run         rsync -n，不建快照
gsync list                   打印条目概览
gsync snapshots --name X     列出该条目快照（非 TUI）
gsync prune [--name X]       仅执行保留清理
```

全局参数：`--config PATH`、`--verbose`、`--log-level`。

退出码：全部成功为 0；任一条目失败为非 0（便于 cron 监控）。

### TUI（bubbletea）

- **主菜单**：列出所有条目（状态 / 上次运行 / 快照数 / 生效后端）。
- **编辑/新增条目**：表单含全部字段 + ignore 规则编辑 + retention 覆盖。
- **手动运行**：触发同步并实时显示输出/进度。
- **快照浏览**：按条目列出快照（时间、占用大小），支持手动删除、恢复、手动触发保留清理。
- 顶部显著显示当前条目生效的快照后端（btrfs native / hardlink）。

## 7. 日志

- 路径：可执行文件所在目录下的 `logs/`。
- 每次运行写一份 `logs/<YYYY-MM-DD_HHMMSS>.log`：各条目起止、rsync 统计摘要（文件数/字节数）、快照与清理结果、错误详情。
- 运行结束在主汇总日志追加单行：`成功 N / 失败 M / 耗时 Ts`。
- rsync 逐文件输出默认**不入日志**（避免冗余），仅 `--verbose` / TUI 调试时显示。
- 旧日志按 `log.keep_days` / `log.keep_count` 清理。

## 8. 其余设计决定

- **方向**：仅远程 → 本地单向拉取镜像。
- **SSH**：调用系统 ssh；cron 下 `BatchMode=yes` 不交互；host key 默认沿用用户 `known_hosts`，可选每条目 `strict_host_key` 开关。
- **忽略规则**：支持 gitignore 实用子集——通配符、目录尾斜杠、前导斜杠锚定、`!` 取反——翻译为 rsync `--filter` 有序规则。文档需说明映射边界（gitignore 自底向上 vs rsync 首匹配优先的差异处理）。
- **条目并发**：默认顺序执行（避免带宽/IO 争抢、日志清晰）；并行留作未来 `--parallel N`。
- **rsync 自动配置**：仅检测 + 提示安装命令，不自动安装（轻量级策略）。
- **构建**：`CGO_ENABLED=0 go build` 产出静态单文件；`GOOS`/`GOARCH` 交叉编译。

## 9. 需求覆盖对照

| 需求 | 覆盖 |
|------|------|
| 1 以远程文件夹为单位配置（ip/端口/用户/密钥/远程路径/本地路径/忽略规则） | §3 配置 `[[sync]]` |
| 2 带参运行、无需确认、TUI 仅调试 | §6 CLI / TUI |
| 3 月/半年/年快照保留 + 最近层 | §5 保留算法 |
| 4 单文件、兼容精简 Linux、可读配置同目录 | §1 约束、§3 配置 |
| 5 rsync 自动配置（可选） | §4 预检（轻量级检测+提示） |
| 6 日志详细高效、放 logs/、不冗余 | §7 日志 |
