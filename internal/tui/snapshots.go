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

	confirmDelete   bool
	restoring       bool
	restoreInput    textinput.Model
	confirmOverwrite bool
	pendingSrc      string
	pendingDst      string
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
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.tbl.SetWidth(clampMin(sz.Width-4, 20))
		m.tbl.SetHeight(clampMin(sz.Height-10, 3))
		return m, nil
	}

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

	if m.confirmOverwrite {
		if key.String() == "y" || key.String() == "Y" {
			if _, err := m.runner.Run(context.Background(), "cp", "-a", m.pendingSrc, m.pendingDst); err != nil {
				m.status = "恢复失败: " + err.Error()
			} else {
				m.status = "已恢复到 " + m.pendingDst
			}
		} else {
			m.status = "已取消恢复"
		}
		m.confirmOverwrite = false
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
	} else if m.confirmOverwrite {
		b.WriteString("\n" + styleErr.Render("目标已存在，覆盖？(y/N)"))
	} else if m.confirmDelete {
		b.WriteString("\n" + styleErr.Render("删除该快照？(y/N)"))
	} else if m.status != "" {
		b.WriteString("\n" + styleStatus.Render(m.status))
	}
	return b.String()
}
