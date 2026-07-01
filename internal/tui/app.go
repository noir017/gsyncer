package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/snapshot"
)

type App struct {
	cfgPath string
	logDir  string
	cfg     *config.Config
	runner  execx.Runner
	fsType  snapshot.FSTypeFunc
	now     func() time.Time

	screen screen
	list   listModel
	form   formModel
	run    runModel
	snaps  snapsModel

	status        string
	statusErr     bool
	confirmDelete bool
	deleteIdx     int
	confirmQuit   bool
	helpVisible   bool

	width, height int
}

func newApp(cfgPath, logDir string, cfg *config.Config, runner execx.Runner, fsType snapshot.FSTypeFunc, now func() time.Time) *App {
	return &App{
		cfgPath: cfgPath, logDir: logDir, cfg: cfg,
		runner: runner, fsType: fsType, now: now,
		screen:      screenList,
		list:        newList(cfg, runner, fsType),
		helpVisible: true,
	}
}

func (a *App) Init() tea.Cmd { return nil }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Store the latest size (used to seed run/snaps/form at creation) and
		// fall through to the screen router so the ACTIVE model resizes live.
		// The list is the initial screen, so it receives the startup size that
		// way; run/snaps/form are additionally seeded on creation below. We do
		// not forward to inactive sub-models here because a zero-value form's
		// textarea panics on SetWidth before it is constructed.
		a.width, a.height = msg.Width, msg.Height

	case statusMsg:
		a.status, a.statusErr = msg.text, msg.isErr
		return a, nil

	case configChangedMsg:
		a.list.refresh()
		return a, nil

	case editEntryMsg:
		a.form = newForm(a.cfg, a.cfgPath, msg.idx)
		if a.width > 0 {
			a.form, _ = a.form.Update(tea.WindowSizeMsg{Width: a.width, Height: a.height})
		}
		a.screen = screenForm
		return a, a.form.Init()

	case copyEntryMsg:
		a.form = newFormCopy(a.cfg, a.cfgPath, msg.idx)
		if a.width > 0 {
			a.form, _ = a.form.Update(tea.WindowSizeMsg{Width: a.width, Height: a.height})
		}
		a.screen = screenForm
		return a, a.form.Init()

	case openSnapsMsg:
		a.snaps = newSnaps(a.cfg.Sync[msg.idx], a.cfg.Defaults, a.runner, a.fsType)
		if a.width > 0 {
			a.snaps, _ = a.snaps.Update(tea.WindowSizeMsg{Width: a.width, Height: a.height})
		}
		a.screen = screenSnaps
		return a, a.snaps.Init()

	case deleteEntryMsg:
		a.confirmDelete = true
		a.deleteIdx = msg.idx
		return a, nil

	case requestQuitMsg:
		a.confirmQuit = true
		return a, nil

	case runEntriesMsg:
		a.run = newRun(a.cfg, a.cfgPath, a.logDir, a.runner, a.fsType, a.now)
		if a.width > 0 {
			a.run, _ = a.run.Update(tea.WindowSizeMsg{Width: a.width, Height: a.height})
		}
		a.screen = screenRun
		return a, a.run.start(msg.entries, msg.dryRun)

	case runDoneMsg:
		a.list.setRunStatus(msg.results)
		a.run, _ = a.run.Update(msg) // let run screen render its summary
		return a, nil

	case backToListMsg:
		a.screen = screenList
		a.list.refresh()
		return a, nil

	case quitMsg:
		return a, tea.Quit
	}

	// global delete-confirm dialog (overlays whatever screen requested it)
	if a.confirmDelete {
		if key, ok := msg.(tea.KeyMsg); ok {
			if key.String() == "y" || key.String() == "Y" {
				a.deleteEntry(a.deleteIdx)
			}
			a.confirmDelete = false
		}
		return a, nil
	}

	// global quit-confirm dialog (default Y: enter confirms, only n/N cancels)
	if a.confirmQuit {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "n", "N", "esc":
				a.confirmQuit = false
			case "y", "Y", "enter":
				return a, tea.Quit
			}
		}
		return a, nil
	}

	// toggle help visibility (only on screens without free-text input)
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "?" &&
		a.screen != screenForm && !(a.screen == screenSnaps && a.snaps.restoring) {
		a.helpVisible = !a.helpVisible
		return a, nil
	}

	// route to active sub-model
	var cmd tea.Cmd
	switch a.screen {
	case screenList:
		a.list, cmd = a.list.Update(msg)
	case screenForm:
		a.form, cmd = a.form.Update(msg)
	case screenRun:
		a.run, cmd = a.run.Update(msg)
	case screenSnaps:
		a.snaps, cmd = a.snaps.Update(msg)
	}
	return a, cmd
}

func (a *App) deleteEntry(idx int) {
	if idx < 0 || idx >= len(a.cfg.Sync) {
		return
	}
	cand := *a.cfg
	cand.Sync = append([]config.Sync(nil), a.cfg.Sync[:idx]...)
	cand.Sync = append(cand.Sync, a.cfg.Sync[idx+1:]...)
	if err := config.Save(a.cfgPath, &cand); err != nil {
		a.status, a.statusErr = "删除失败: "+err.Error(), true
		return
	}
	*a.cfg = cand
	a.list.refresh()
	a.status, a.statusErr = "已删除条目", false
}

func (a *App) helpLine() string {
	switch a.screen {
	case screenList:
		return "↑/↓ 选择  enter 快照  a 新增  c 复制  e 编辑  d 删除  s 同步  S 全部  r 刷新  ? 帮助  q 退出"
	case screenForm:
		return "tab/↓ 下一项  shift+tab/↑ 上一项  空格 切换 strict  enter 解析粘贴  ctrl+s 保存  esc 取消"
	case screenRun:
		if a.run.cancelling {
			return "(取消中) ctrl+c 退出"
		}
		if a.run.running {
			return "(运行中) ctrl+c 中断"
		}
		return "(完成) enter/esc 返回，ctrl+c 退出"
	case screenSnaps:
		return "↑/↓ 选择  d 删除  p 按策略清理  x 恢复  esc 返回"
	}
	return ""
}

func (a *App) View() string {
	var body string
	switch a.screen {
	case screenList:
		body = a.list.View()
	case screenForm:
		body = a.form.View()
	case screenRun:
		body = a.run.View()
	case screenSnaps:
		body = a.snaps.View()
	}
	var b strings.Builder
	b.WriteString(body)
	if a.confirmDelete {
		b.WriteString("\n" + styleErr.Render("删除选中条目？(y/N)"))
	} else if a.confirmQuit {
		b.WriteString("\n" + styleErr.Render("退出 gsync？(Y/n)"))
	} else if a.status != "" {
		st := styleStatus
		if a.statusErr {
			st = styleErr
		}
		b.WriteString("\n" + st.Render(a.status))
	}
	if a.helpVisible {
		b.WriteString("\n" + styleHelp.Render(a.helpLine()))
	}
	return b.String()
}
