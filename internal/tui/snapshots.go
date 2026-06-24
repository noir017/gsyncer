package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
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
	status  string
	backend string

	confirmDelete bool
	restoring     bool
	restoreInput  textinput.Model
}

func newSnaps(entry config.Sync, defaults config.Defaults, runner execx.Runner, fsType snapshot.FSTypeFunc) snapsModel {
	t := table.New(table.WithColumns([]table.Column{
		{Title: "时间", Width: 22},
		{Title: "名义大小", Width: 12},
	}), table.WithFocused(true))
	m := snapsModel{entry: entry, defaults: defaults, runner: runner, fsType: fsType, tbl: t}
	m.restoreInput = textinput.New()
	m.reload()
	return m
}

func (m *snapsModel) reload() {
	ctx := context.Background()
	m.backend = snapshot.Detect(ctx, m.entry.LocalPath, m.runner, m.fsType).Name()
	times, err := snapshot.List(m.entry.LocalPath)
	if err != nil {
		m.status = "列快照失败: " + err.Error()
		return
	}
	sort.Slice(times, func(i, j int) bool { return times[i].After(times[j]) })
	m.times = times
	rows := make([]table.Row, 0, len(times))
	for _, ts := range times {
		p := filepath.Join(m.entry.LocalPath, "snapshots", ts.Format(snapshot.TSLayout))
		size := "?"
		if n, err := dirSize(p); err == nil {
			size = humanSize(n)
		}
		rows = append(rows, table.Row{ts.Format(snapshot.TSLayout), size})
	}
	m.tbl.SetRows(rows)
	if m.tbl.Cursor() >= len(rows) {
		m.tbl.SetCursor(max0(len(rows) - 1))
	}
}

func (m snapsModel) snapPath(i int) string {
	return filepath.Join(m.entry.LocalPath, "snapshots", m.times[i].Format(snapshot.TSLayout))
}

func (m snapsModel) Init() tea.Cmd { return nil }

func (m snapsModel) Update(msg tea.Msg) (snapsModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	if m.confirmDelete {
		if key.String() == "y" || key.String() == "Y" {
			i := m.tbl.Cursor()
			if i >= 0 && i < len(m.times) {
				be := snapshot.Detect(context.Background(), m.entry.LocalPath, m.runner, m.fsType)
				if err := be.Delete(context.Background(), m.snapPath(i)); err != nil {
					m.status = "删除失败: " + err.Error()
				} else {
					m.status = "已删除快照"
					m.reload()
				}
			}
		}
		m.confirmDelete = false
		return m, nil
	}

	if m.restoring {
		switch key.String() {
		case "enter":
			dst := strings.TrimSpace(m.restoreInput.Value())
			src := m.snapPath(m.tbl.Cursor())
			if _, err := m.runner.Run(context.Background(), "cp", "-a", src, dst); err != nil {
				m.status = "恢复失败: " + err.Error()
			} else {
				m.status = "已恢复到 " + dst
			}
			m.restoring = false
			return m, nil
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
		res := syncer.PruneOne(context.Background(), m.entry, m.defaults,
			syncer.Deps{Runner: m.runner, FSType: m.fsType, Now: time.Now, Log: nopLogger{}})
		if res.Err != nil {
			m.status = "清理失败: " + res.Err.Error()
		} else {
			m.status = fmt.Sprintf("已清理 %d 个快照", res.Pruned)
			m.reload()
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
	} else if m.confirmDelete {
		b.WriteString("\n" + styleErr.Render("删除该快照？(y/N)"))
	} else if m.status != "" {
		b.WriteString("\n" + styleStatus.Render(m.status))
	}
	b.WriteString("\n" + styleHelp.Render("↑/↓ 选择  d 删除  p 按策略清理  x 恢复  esc 返回"))
	return b.String()
}
