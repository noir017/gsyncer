package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/snapshot"
	"gsyncer/internal/syncer"
)

// nopLogger discards syncer log output (used for prune from the snapshot screen).
type nopLogger struct{}

func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}

type snapsModel struct {
	entry    config.Sync
	defaults config.Defaults
	runner   execx.Runner
	fsType   snapshot.FSTypeFunc

	tbl     table.Model
	times   []time.Time
	sizes   map[string]string // ts string -> humanized nominal size ("" = pending)
	epoch   int               // bumped whenever the snapshot set changes
	status  string
	backend string
	busy    bool // a restore/prune/delete is running in the background

	confirmDelete    bool
	restoring        bool
	restoreInput     textinput.Model
	confirmOverwrite bool
	pendingSrc       string
	pendingDst       string
	confirmPrune     bool
	pendingPruneN    int
}

// pruneDeps builds the syncer dependencies used for both counting and pruning,
// so the confirmed count matches what PruneOne actually deletes.
func (m snapsModel) pruneDeps() syncer.Deps {
	return syncer.Deps{Runner: m.runner, FSType: m.fsType, Now: time.Now, Log: nopLogger{}}
}

func newSnaps(entry config.Sync, defaults config.Defaults, runner execx.Runner, fsType snapshot.FSTypeFunc) snapsModel {
	t := table.New(table.WithColumns([]table.Column{
		{Title: "时间", Width: 22},
		{Title: "名义大小", Width: 12},
	}), table.WithFocused(true))
	m := snapsModel{entry: entry, defaults: defaults, runner: runner, fsType: fsType,
		tbl: t, sizes: map[string]string{}, epoch: 1}
	m.restoreInput = textinput.New()
	// Only the fast listing runs synchronously; directory sizes are computed off
	// the UI goroutine (see Init/computeSizesCmd) so the list opens instantly even
	// on large snapshot trees.
	m.loadList()
	return m
}

// Init kicks off the initial (background) size computation.
func (m snapsModel) Init() tea.Cmd { return m.computeSizesCmd(m.epoch) }

// loadList refreshes the backend name and the snapshot timestamps (both cheap)
// and rebuilds the table rows, reusing any already-computed sizes. It does not
// walk the trees — that is computeSizesCmd's job.
func (m *snapsModel) loadList() {
	ctx := context.Background()
	m.backend = snapshot.Detect(ctx, m.entry.LocalPath, m.runner, m.fsType).Name()
	times, err := snapshot.List(m.entry.LocalPath)
	if err != nil {
		m.status = "列快照失败: " + err.Error()
		return
	}
	sort.Slice(times, func(i, j int) bool { return times[i].After(times[j]) })
	m.times = times
	m.rebuildRows()
	if m.tbl.Cursor() >= len(times) {
		m.tbl.SetCursor(max0(len(times) - 1))
	}
}

// rebuildRows renders the table from m.times, showing "…" for any snapshot whose
// size has not been computed yet.
func (m *snapsModel) rebuildRows() {
	rows := make([]table.Row, 0, len(m.times))
	for _, ts := range m.times {
		key := ts.Format(snapshot.TSLayout)
		size := m.sizes[key]
		if size == "" {
			size = "…"
		}
		rows = append(rows, table.Row{key, size})
	}
	m.tbl.SetRows(rows)
}

// computeSizesCmd walks each snapshot directory off the UI goroutine and reports
// the humanized sizes tagged with epoch, so a result that lands after the list
// changed can be discarded.
func (m snapsModel) computeSizesCmd(epoch int) tea.Cmd {
	root := m.entry.LocalPath
	times := append([]time.Time(nil), m.times...)
	return func() tea.Msg {
		sizes := make(map[string]string, len(times))
		for _, ts := range times {
			key := ts.Format(snapshot.TSLayout)
			if n, err := dirSize(filepath.Join(root, "snapshots", key)); err == nil {
				sizes[key] = humanSize(n)
			} else {
				sizes[key] = "?"
			}
		}
		return snapSizesMsg{epoch: epoch, sizes: sizes}
	}
}

// pruneCmd runs retention prune off the UI goroutine.
func (m snapsModel) pruneCmd() tea.Cmd {
	entry, defaults, deps := m.entry, m.defaults, m.pruneDeps()
	return func() tea.Msg {
		res := syncer.PruneOne(context.Background(), entry, defaults, deps, false)
		if res.Err != nil {
			return snapOpDoneMsg{status: "清理失败: " + res.Err.Error()}
		}
		return snapOpDoneMsg{status: fmt.Sprintf("已清理 %d 个快照", res.Pruned), reload: true}
	}
}

// deleteCmd removes one snapshot off the UI goroutine.
func (m snapsModel) deleteCmd(path string) tea.Cmd {
	root, runner, fsType := m.entry.LocalPath, m.runner, m.fsType
	return func() tea.Msg {
		be := snapshot.Detect(context.Background(), root, runner, fsType)
		if err := be.Delete(context.Background(), path); err != nil {
			return snapOpDoneMsg{status: "删除失败: " + err.Error()}
		}
		return snapOpDoneMsg{status: "已删除快照", reload: true}
	}
}

// restoreCmd copies a snapshot to dst off the UI goroutine. When overwrite is
// set the existing dst is removed first (a bare `cp -a` into an existing dir
// would nest the copy under dst/<name>/… and leave stale files behind).
func (m snapsModel) restoreCmd(src, dst string, overwrite bool) tea.Cmd {
	runner := m.runner
	return func() tea.Msg {
		if overwrite {
			if err := os.RemoveAll(dst); err != nil {
				return snapOpDoneMsg{status: "恢复失败: " + err.Error()}
			}
		}
		if _, err := runner.Run(context.Background(), "cp", "-a", src, dst); err != nil {
			return snapOpDoneMsg{status: "恢复失败: " + err.Error()}
		}
		return snapOpDoneMsg{status: "已恢复到 " + dst}
	}
}

func (m snapsModel) snapPath(i int) string {
	return filepath.Join(m.entry.LocalPath, "snapshots", m.times[i].Format(snapshot.TSLayout))
}

func (m snapsModel) Update(msg tea.Msg) (snapsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.tbl.SetWidth(clampMin(msg.Width-4, 20))
		m.tbl.SetHeight(clampMin(msg.Height-10, 3))
		return m, nil

	case snapSizesMsg:
		// Ignore results from a superseded computation (list changed meanwhile).
		if msg.epoch == m.epoch {
			m.sizes = msg.sizes
			m.rebuildRows()
		}
		return m, nil

	case snapOpDoneMsg:
		m.busy = false
		m.status = msg.status
		if msg.reload {
			m.loadList()
			m.epoch++ // supersede any in-flight size computation
			return m, m.computeSizesCmd(m.epoch)
		}
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// While a background op runs, accept only leave/quit so the UI never blocks
	// and we never launch a second concurrent op.
	if m.busy {
		switch key.String() {
		case "esc", "q":
			return m, func() tea.Msg { return backToListMsg{} }
		case "ctrl+c":
			return m, func() tea.Msg { return quitMsg{} }
		}
		return m, nil
	}

	if m.confirmDelete {
		m.confirmDelete = false
		if key.String() == "y" || key.String() == "Y" {
			i := m.tbl.Cursor()
			if i >= 0 && i < len(m.times) {
				m.busy = true
				m.status = "删除中…"
				return m, m.deleteCmd(m.snapPath(i))
			}
		}
		return m, nil
	}

	if m.confirmPrune {
		m.confirmPrune = false
		if key.String() == "y" || key.String() == "Y" {
			m.busy = true
			m.status = "清理中…"
			return m, m.pruneCmd()
		}
		m.status = "已取消清理"
		return m, nil
	}

	if m.confirmOverwrite {
		m.confirmOverwrite = false
		if key.String() == "y" || key.String() == "Y" {
			m.busy = true
			m.status = "恢复中…"
			return m, m.restoreCmd(m.pendingSrc, m.pendingDst, true)
		}
		m.status = "已取消恢复"
		return m, nil
	}

	if m.restoring {
		switch key.String() {
		case "enter":
			i := m.tbl.Cursor()
			if i < 0 || i >= len(m.times) {
				m.restoring = false
				return m, nil
			}
			dst := strings.TrimSpace(m.restoreInput.Value())
			if dst == "" {
				m.status = "目标路径不能为空"
				return m, nil
			}
			cur := filepath.Clean(filepath.Join(m.entry.LocalPath, "current"))
			if filepath.Clean(dst) == cur {
				m.status = "不能覆盖 current 目录，请换一个目标路径"
				return m, nil
			}
			src := m.snapPath(i)
			if _, err := os.Stat(dst); err == nil {
				// destination exists → confirm before clobbering
				m.pendingSrc, m.pendingDst = src, dst
				m.confirmOverwrite = true
				m.restoring = false
				return m, nil
			}
			// does not exist (or stat error) → proceed directly
			m.restoring = false
			m.busy = true
			m.status = "恢复中…"
			return m, m.restoreCmd(src, dst, false)
		case "esc":
			m.restoring = false
			return m, nil
		default:
			var cmd tea.Cmd
			m.restoreInput, cmd = m.restoreInput.Update(msg)
			return m, cmd
		}
	}

	switch key.String() {
	case "d":
		if len(m.times) > 0 {
			m.confirmDelete = true
		}
		return m, nil
	case "p":
		// Count first (cheap: list + retention partition) and confirm before
		// deleting; the actual prune runs off the UI goroutine (see confirmPrune).
		n, err := syncer.CountPrunable(context.Background(), m.entry, m.defaults, m.pruneDeps())
		if err != nil {
			m.status = "清理失败: " + err.Error()
		} else if n == 0 {
			m.status = "无可清理的快照"
		} else {
			m.pendingPruneN = n
			m.confirmPrune = true
		}
		return m, nil
	case "x":
		if len(m.times) > 0 {
			i := m.tbl.Cursor()
			def := filepath.Join(m.entry.LocalPath, "restore-"+m.times[i].Format(snapshot.TSLayout))
			m.restoreInput.SetValue(def)
			m.restoreInput.Focus()
			m.restoring = true
		}
		return m, nil
	case "esc", "q":
		return m, func() tea.Msg { return backToListMsg{} }
	case "ctrl+c":
		return m, func() tea.Msg { return quitMsg{} }
	}

	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(msg)
	return m, cmd
}

func (m snapsModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("快照: "+m.entry.Name) +
		styleHelp.Render("   后端: "+m.backend) + "\n\n")
	b.WriteString(m.tbl.View() + "\n")
	if m.restoring {
		b.WriteString("\n恢复到: " + m.restoreInput.View() + styleHelp.Render("  (enter 确认, esc 取消)"))
	} else if m.confirmPrune {
		b.WriteString("\n" + styleErr.Render(fmt.Sprintf("将删除 %d 份，确认？(y/N)", m.pendingPruneN)))
	} else if m.confirmOverwrite {
		b.WriteString("\n" + styleErr.Render("目标已存在，覆盖？(y/N)"))
	} else if m.confirmDelete {
		b.WriteString("\n" + styleErr.Render("删除该快照？(y/N)"))
	} else if m.status != "" {
		b.WriteString("\n" + styleStatus.Render(m.status))
	}
	return b.String()
}
