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

	width, height int
}

func newApp(cfgPath, logDir string, cfg *config.Config, runner execx.Runner, fsType snapshot.FSTypeFunc, now func() time.Time) *App {
	return &App{
		cfgPath: cfgPath, logDir: logDir, cfg: cfg,
		runner: runner, fsType: fsType, now: now,
		screen: screenList,
		list:   newList(cfg, runner, fsType),
	}
}

func (a *App) Init() tea.Cmd { return nil }

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height

	case statusMsg:
		a.status, a.statusErr = msg.text, msg.isErr
		return a, nil

	case configChangedMsg:
		a.list.refresh()
		return a, nil

	case editEntryMsg:
		a.form = newForm(a.cfg, a.cfgPath, msg.idx)
		a.screen = screenForm
		return a, a.form.Init()

	case openSnapsMsg:
		a.snaps = newSnaps(a.cfg.Sync[msg.idx], a.cfg.Defaults, a.runner, a.fsType)
		a.screen = screenSnaps
		return a, a.snaps.Init()

	case deleteEntryMsg:
		a.confirmDelete = true
		a.deleteIdx = msg.idx
		return a, nil

	case runEntriesMsg:
		a.run = newRun(a.cfg, a.logDir, a.runner, a.fsType, a.now)
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
	} else if a.status != "" {
		st := styleStatus
		if a.statusErr {
			st = styleErr
		}
		b.WriteString("\n" + st.Render(a.status))
	}
	return b.String()
}
