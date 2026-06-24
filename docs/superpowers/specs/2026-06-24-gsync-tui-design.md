# gsync TUI 设计文档

> bubbletea 终端界面，用于人工配置与调试 gsync。
> 日期：2026-06-24
> 状态：设计待评审
> 依赖：核心 CLI（已实现，分支 feat/gsync-core）。TUI 复用 config/syncer/snapshot/logx，**不修改其接口**。

## 1. 目标与范围

为 gsync 提供一个交互式终端界面，覆盖「人工配置和调试」场景。定时无人值守仍走 CLI 子命令；TUI 不参与 cron。

TUI 要能做到：

- 浏览所有同步条目，看到每条的状态、上次运行、快照数、**生效的快照后端**（btrfs/hardlink）。
- 新增 / 编辑 / 删除条目（全部字段 + ignore 规则 + retention 覆盖），写回 `config.toml`。
- 手动触发同步（单条 / 全部 / dry-run），**实时**看到进度与结果。
- 浏览某条目的快照：列出时间、占用大小，支持删除、按保留策略清理、恢复。
- 全程不崩：任何核心层错误以状态行 / 弹层呈现，不 panic。

**不做（YAGNI）**：多语言、鼠标、主题切换、配置项的实时远程校验（连不连得上在运行时才知道）、并发多条同步（沿用核心层顺序执行）。

## 2. 技术选型

| 用途 | 库 |
|------|----|
| 框架（Elm 架构 Model/Update/View） | `github.com/charmbracelet/bubbletea` |
| 组件（list、textinput、textarea、table、viewport、help、key） | `github.com/charmbracelet/bubbles` |
| 样式 | `github.com/charmbracelet/lipgloss` |

这会引入新依赖（bubbletea + bubbles + lipgloss 及其传递依赖）。**全局约束「唯一第三方依赖 BurntSushi/toml」是针对核心 CLI 的**；TUI 是独立层，需放宽该约束，但仍保持 `CGO_ENABLED=0` 静态单文件、Go 1.22。这些库均为纯 Go，不破坏静态单文件目标。

> 决策 1（依赖）：默认接受 bubbletea 系列依赖。若你希望 TUI 也零额外依赖，则需手写 termbox 级渲染，成本极高，不推荐。

入口：核心 CLI 的 `main.go` 中无参数分支当前打印「TUI 未实现」。改为调用 `tui.Run(cfgPath, logDir, deps)`，进入全屏程序（`tea.NewProgram(..., tea.WithAltScreen())`）。

## 3. 模块划分

```
internal/tui/
  tui.go        Run 入口：装配、加载 config、启动 tea.Program
  app.go        顶层 Model：屏幕路由 + 全局状态（config、cfgPath、生效后端缓存、状态行）
  list.go       主菜单：条目列表 Model
  form.go       新增/编辑条目表单 Model
  run.go        手动运行屏：实时输出 Model
  snapshots.go  快照浏览屏 Model
  styles.go     lipgloss 样式集中定义
  msgs.go       自定义 tea.Msg 类型（运行进度、运行完成、IO 结果等）
```

每个屏是独立的 bubbletea sub-model，顶层 `app.go` 持有当前屏并转发 `Update`/`View`。屏之间通过返回 `tea.Msg` 或顶层方法切换，不互相直接调用内部状态。

## 4. 屏幕地图与导航

```
            ┌──────────────┐
            │  主菜单 List  │◄────────────┐
            └──────┬───────┘             │
        a/enter│ s │ S        │esc/q     │
   ┌───────────┤   ├──────────┴───┐      │
   ▼           ▼   ▼              ▼      │
┌──────┐  ┌────────┐  ┌──────────────┐  │
│ 表单 │  │ 运行屏 │  │ 快照浏览屏    │  │
│ Form │  │ Run    │  │ Snapshots    │  │
└──┬───┘  └───┬────┘  └──────┬───────┘  │
   │ 保存/取消 │ 完成/esc      │ esc      │
   └──────────┴───────────────┴──────────┘
```

全局按键（任意屏）：

| 键 | 行为 |
|----|------|
| `?` | 切换帮助行（bubbles/help 展开/收起） |
| `Ctrl+C` | 运行屏「运行中」时=取消当前同步（见 5.3）；其余情况=退出程序 |
| `esc` | 返回上一屏（主菜单为退出确认；表单有未保存改动时弹丢弃确认，见 5.2） |

> `Ctrl+C` 不是无条件「立即退出」：仅当运行屏正在跑同步时它先表示「取消」，取消完成后再次 `Ctrl+C` 才退出。其余所有屏 `Ctrl+C` 直接退出。

## 5. 屏幕详细设计

### 5.1 主菜单（List）

列出 `config.Sync` 全部条目。每行展示：名称、`user@host:remote → local` 摘要、快照数、生效后端、上次运行状态。

```
 gsync — 文件同步                                     config.toml
┌────────────────────────────────────────────────────────────────┐
│ ● web1-www    deploy@1.2.3.4:/var/www → /data/web1   12 snaps  btrfs   │
│ ● db-dumps    root@db01:/backups     → /data/db      6 snaps   hardlink│
│ ○ assets      cdn@5.6.7.8:/assets    → /data/assets  0 snaps   btrfs   │
└────────────────────────────────────────────────────────────────┘
 ↑/↓ 选择   enter 详情/快照   a 新增   e 编辑   d 删除   s 同步   S 全部同步   ? 帮助   q 退出
```

- **生效后端**通过 `snapshot.Detect(ctx, localPath, deps.Runner, deps.FSType).Name()` 计算并缓存（首次进入或刷新时算一次，避免每帧 statfs）。`current` 不存在时也能探测（Detect 只看 localPath 所在文件系统）。
- **快照数**来自 `snapshot.List(localPath)` 长度。
- **状态点**：上次运行成功=绿●、失败=红●、从未运行=灰○。「上次运行」状态来自本会话内运行记录（TUI 进程内存）；跨会话不持久化（YAGNI；要持久化可读 summary.log，列为可选）。

按键：

| 键 | 行为 |
|----|------|
| `↑`/`↓`/`k`/`j` | 移动选择 |
| `enter` | 进入所选条目的快照浏览屏 |
| `a` | 进入空白表单（新增） |
| `e` | 进入表单（编辑所选） |
| `d` | 删除所选（二次确认弹层） |
| `s` | 同步所选条目 → 运行屏 |
| `S` | 同步全部条目 → 运行屏 |
| `r` | 刷新（重算后端/快照数） |
| `q`/`esc` | 退出（弹确认弹层） |

> 主菜单不存在「未保存改动」：表单保存即落盘、取消即丢弃，回到主菜单时内存中的 `cfg` 与磁盘恒一致。未保存确认属于**表单屏**（见 5.2），不在此层。

### 5.2 条目表单（Form）

新增或编辑一个 `config.Sync`。用 `bubbles/textinput` 逐字段，`textarea` 编辑 ignore 规则（每行一条 gitignore 风格）。

```
 编辑条目: web1-www
┌────────────────────────────────────────────┐
│ 名称        [ web1-www              ]        │
│ 主机        [ 1.2.3.4               ]        │
│ 端口        [ 22                    ]  (空=默认)│
│ 用户        [ deploy                ]        │
│ 密钥        [ ~/.ssh/id_ed25519     ]        │
│ 远程路径    [ /var/www/             ]        │
│ 本地路径    [ /data/web1            ]        │
│ strict host [x] 严格检查 host key            │
│ ── 忽略规则 (gitignore 风格, 每行一条) ──    │
│ ┌──────────────────────────────────┐        │
│ │ *.log                            │ textarea│
│ │ cache/                           │        │
│ │ !cache/keep.txt                  │        │
│ └──────────────────────────────────┘        │
│ ── 保留覆盖 (留空=用 defaults) ──            │
│ recent [   ] monthly [   ] semi [  ] yearly[ ]│
└────────────────────────────────────────────┘
 tab/↓ 下一项   shift+tab/↑ 上一项   ctrl+s 保存   esc 取消
```

行为：

- `tab`/`shift+tab` 在字段间移动；当前字段高亮。
- 保留覆盖四格：留空表示该字段不覆盖（`RetentionOverride` 对应 `*int` 为 nil）；填了数字则覆盖。
- **编辑态记录原始索引**：进入编辑屏时把所选条目在 `cfg.Sync` 中的下标 `origIdx`（新增=`-1`）存进 formModel。保存时按 `origIdx` 定位替换，**不按当前 name 查找**——否则用户在表单里改了「名称」字段后，按新名找不到旧条目会退化成追加，留下重名/重复条目。
- **dirty 跟踪**：任一字段值偏离进入时的初值即置 `dirty=true`。
- **保存（`ctrl+s`）**：
  1. 把表单值组装成 `config.Sync`。`origIdx>=0` → `cfg.Sync[origIdx] = s`（替换，支持改名）；`origIdx==-1` → `append`。
  2. 调 `config.Config.Validate()`。校验失败 → 在表单底部状态行红字显示错误（如「sync "web1-www": local_path is required」），不退出，不落盘（回滚步骤 1 对 `cfg` 的改动）。
  3. 校验通过 → `config.Save(cfgPath, cfg)`，清 `dirty`，返回主菜单并刷新。
- **取消 / 退出（`esc`，或表单内 `Ctrl+C`）**：
  - `dirty==false` → 直接返回主菜单（`esc`）/ 退出程序（`Ctrl+C`）。
  - `dirty==true` → 弹「放弃未保存的改动？(y/N)」确认弹层；确认后才丢弃。这样编辑到一半误退不会静默丢失。
- 端口输入做数字解析；非数字在保存时报错。
- 密钥路径不在表单里做存在性校验（`Validate` 会查，统一在保存时报错）。

> 决策 7（保存即全量覆写，不保留注释）：`config.Save` 用 `toml.NewEncoder` 整文件重写，会**丢失 config.toml 里的注释并规整字段顺序**。这是已接受的代价——保留注释需块级文本拼接或换无损 TOML 库，成本不值。文档在此明示，避免用户首次经 TUI 保存后困惑注释消失。

> 决策 2（校验时机）：仅在保存时整体 `Validate`，不做逐字段实时校验。简单、与核心层一致。

### 5.3 运行屏（Run）

手动触发同步后进入，实时展示进度。复用 `syncer.SyncOne`/`SyncMany`，但日志要进 TUI 而非文件——通过一个实现 `syncer.Logger` 的**通道适配器**把 `Infof/Errorf` 转成 `tea.Msg`。

```
 同步中: web1-www (1/1)                          后端: btrfs
┌────────────────────────────────────────────┐
│ [web1-www] snapshot mode: btrfs             │
│ [web1-www] pulled 134 files, 2.3 MB         │
│ [web1-www] snapshot created: .../2026-06-24_…│
│ [web1-www] pruned 2 snapshots               │
│                                             │
│ ✔ 完成: 成功 1 / 失败 0 / 耗时 3.4s          │
└────────────────────────────────────────────┘
 (运行中) ctrl+c 中断    (完成后) enter/esc 返回
```

并发模型（关键）：

- bubbletea 的 `Update` 不能阻塞。运行在后台 goroutine 里跑 `syncer.SyncMany`，goroutine 通过一个 `chan tea.Msg` 把每条日志、最终结果发回，TUI 用 `tea.Cmd` 监听该通道（经典 bubbletea「listen on channel」模式）。
- 适配 `syncer.Logger`：
  ```go
  type chanLogger struct{ ch chan<- tea.Msg }
  func (l chanLogger) Infof(f string, a ...any){ l.ch <- logLineMsg{level:"INFO", text:fmt.Sprintf(f,a...)} }
  func (l chanLogger) Errorf(f string, a ...any){ l.ch <- logLineMsg{level:"ERROR", text:fmt.Sprintf(f,a...)} }
  ```
- 用 `bubbles/viewport` 承载滚动输出。
- **同时**仍写正常的 per-run 文件日志：可在 goroutine 内额外接一个 `logx.RunLogger`（用 `io.MultiWriter` 思路，或 logger 同时转发）。
  > 决策 3（运行屏是否写文件日志）：默认**写**，与 CLI 行为一致，便于事后追溯。运行结束同样 `AppendSummary` + `Cleanup`。
- **中断（`ctrl+c`）——两段式**：
  - 运行中第一次 `ctrl+c`：`cancel()`（`context.WithCancel`），rsync 子进程随 `exec.CommandContext` 被杀；当前条目记为失败，停止后续条目；输出区追加「⚠ 已请求取消…」。运行屏进入「已完成/已取消」态。
  - 取消完成后（或同步自然跑完后）再次 `ctrl+c`：退出程序。
  - 即：运行中 `ctrl+c` 只取消、不退出；只有非运行态的 `ctrl+c` 才退出。这与 §4 全局表的「运行中=取消，其余=退出」一致。
  - 运行态用一个 `running bool` 标记区分两段；`cancel` 调用后置 `running=false`。

### 5.4 快照浏览屏（Snapshots）

对选中条目，用 `bubbles/table` 列出 `snapshots/` 下所有快照。

```
 快照: web1-www                                 后端: btrfs
┌──────────────────────┬──────────┐
│ 时间                 │ 占用      │
├──────────────────────┼──────────┤
│ 2026-06-24_030000    │ 2.3 MB   │
│ 2026-06-23_030000    │ 1.1 MB   │
│ 2026-05-24_030000    │ 0.4 MB   │
└──────────────────────┴──────────┘
 ↑/↓ 选择   d 删除   p 按策略清理   x 恢复   esc 返回
```

- 列表来自 `snapshot.List(localPath)`，降序。
- **占用大小**：
  - hardlink 模式：硬链接快照的「独占」大小用 `du` 不直观；MVP 显示该快照目录的 apparent size（`du -sh --apparent-size` 或 Go 递归 `os.Lstat` 求和），并在表头注明「名义大小」。
  - btrfs 模式：同样先用名义大小；精确的 CoW 独占大小需 `btrfs filesystem du`，列为可选增强。
  > 决策 4（大小口径）：MVP 用名义大小（递归文件大小求和），表头标注。精确独占大小为后续增强。
- 按键：
  - `d` 删除所选快照：二次确认 → `backend.Delete(ctx, path)`（btrfs `subvolume delete` / hardlink `rm -rf`）。
  - `p` 按保留策略清理：调 `syncer.PruneOne(ctx, entry, defaults, deps)`，结果以状态行汇报「pruned N」，刷新表格。
  - `x` 恢复所选快照（见下）。
  - `esc` 返回主菜单。

恢复（restore）语义——**安全优先**：

- 默认行为：把所选快照内容**导出到一个新目录**，不碰 `current/`。弹出输入框填目标路径，默认 `<local_path>/restore-<ts>`，用 `cp -a`（经 `execx.Runner`）拷贝。
- 覆盖 `current/`（用快照内容替换当前镜像）属高危操作：**不在 MVP 默认提供**；若提供，必须强确认（输入条目名确认）。
  > 决策 5（恢复语义）：MVP 仅「导出到新目录」，绝不自动覆盖 current。覆盖 current 作为后续带强确认的增强。

## 6. 顶层 Model 与状态

```go
type screen int
const (screenList screen; screenForm; screenRun; screenSnaps)

type App struct {
    cfgPath string
    logDir  string
    cfg     *config.Config
    runner  execx.Runner         // execx.Real{}
    fsType  snapshot.FSTypeFunc  // snapshot.RealFSType
    now     func() time.Time     // time.Now
    screen  screen
    list    listModel
    form    formModel
    run     runModel
    snaps   snapsModel
    status  string               // 全局状态行（错误/提示）
    help    help.Model
    width, height int
}
```

- **不持有完整 `syncer.Deps`**：`syncer.Deps` 含必填的 `Log Logger`，而 chanLogger 只在一次 run 期间存在。所以 `App` 只存无状态的 `runner`/`fsType`/`now` 三件（真实实现：`execx.Real{}`、`snapshot.RealFSType`、`time.Now`）。
  - 探测后端 / 列快照 / 删除单快照不需要 logger：`snapshot.Detect(ctx, path, app.runner, app.fsType)`、`snapshot.List(path)` 直接用这三件。
  - 进运行屏时才临时组装 `syncer.Deps{Runner: app.runner, FSType: app.fsType, Now: app.now, Log: chanLogger}` 传给 `SyncMany`/`PruneOne`。
  - 这意味着 TUI 调的就是真同步、真快照、真删除，与 CLI 相同。
- 后端探测结果按 localPath 缓存于 `App`（`map[string]string`），`r` 刷新时清空。
- 全局 `status` 行在所有屏底部上方渲染，承载校验错误、删除/清理结果、IO 失败等。

## 7. 错误处理

- 所有核心层调用的 error 都转成 `status` 行红字或确认弹层文本，**绝不 panic**、绝不静默吞掉。
- 文件级 IO（保存配置、删除快照、恢复拷贝）失败：状态行显示原始 error，停在当前屏。
- 运行屏的同步失败：每条目的失败在输出区以红色 `[name] ...failed...` 行呈现，最终汇总行显示「失败 M」。
- 加载阶段（`tui.Run` 起手）：`config.Load` 失败则不进 TUI，回退到 stderr 报错 + 非零退出（与 CLI 一致）。配置文件不存在时，进入空列表（允许从零新增条目并保存创建文件）。
  > 决策 6（无配置启动）：允许 TUI 在 config.toml 不存在时启动为空列表，首次保存即创建文件。

## 8. 测试策略

bubbletea 的 Model 是纯函数式的（`Update(msg) (Model, Cmd)`），可不起终端直接单测：

- **list**：构造带 N 条目的 model，发 `tea.KeyMsg` 验证选择移动、按 `a/e/d/s` 产生正确的屏切换 msg / 状态。
- **form**：填充字段 → 发保存键 → 断言产出的 `config.Sync` 字段正确、`Validate` 失败时 status 被设置、成功时触发 save msg。保留覆盖留空→nil、填值→*int。
- **snapshots**：mock 出快照目录，验证表格行数、删除键产生删除 msg、清理键调用 PruneOne 路径。
- **chanLogger**：验证 Infof/Errorf 推出正确 msg。
- **大小计算**：临时目录造文件，验证名义大小求和。
- 渲染（`View()`）做 golden/快照断言可选；核心是 Update 逻辑。

外部命令（cp/btrfs/rsync）一律经 `execx.FakeRunner`，与核心层测试一致；文件系统操作用 `t.TempDir()`。

## 9. 与核心层的契约（只读复用，不改接口）

| 用途 | 调用 |
|------|------|
| 读配置 | `config.Load(cfgPath)` |
| 存配置 | `config.Save(cfgPath, cfg)` + `cfg.Validate()` |
| 探测后端 | `snapshot.Detect(ctx, localPath, deps.Runner, deps.FSType).Name()` |
| 列快照 | `snapshot.List(localPath)` |
| 同步 | `syncer.SyncOne/SyncMany(ctx, …, deps, dryRun)`，日志走 chanLogger |
| 清理 | `syncer.PruneOne(ctx, entry, defaults, deps)` |
| 删除单快照 | 经 `snapshot.Detect(...).Delete(ctx, path)` |
| 写运行日志 | `logx.NewRunLogger/AppendSummary/Cleanup` |

唯一可能需要的核心层**新增**（非破坏）：当前 `snapshot.Backend` 无「按时间戳构造路径」的方法，TUI 删除单快照时要 `filepath.Join(local,"snapshots",ts.Format(TSLayout))`——与 syncer 现状一致，可直接复用同样写法，无需改接口。

## 10. 待评审决策清单

1. 接受 bubbletea/bubbles/lipgloss 依赖（放宽「仅 toml」约束至 TUI 层）。
2. 表单仅保存时整体校验。
3. 运行屏同时写文件日志 + summary + cleanup。
4. 快照大小用名义大小（求和），精确独占大小为后续。
5. 恢复 = 导出到新目录；不覆盖 current（覆盖作为带强确认的后续增强）。
6. config.toml 不存在时允许空列表启动，首存即创建。
7. 保存采用 `toml` 全量覆写，**不保留注释**、字段顺序会被规整（已接受的代价，文档明示）。

确认/修改以上后，我据此写 `docs/superpowers/plans/2026-06-24-gsync-tui.md` 实现计划。
