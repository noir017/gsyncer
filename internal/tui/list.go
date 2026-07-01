package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
)

type runStatus int

const (
	runNever runStatus = iota
	runOK
	runFail
)

type listModel struct {
	cfg    *config.Config
	cursor int
	runner execx.Runner
	fsType snapshot.FSTypeFunc

	backends map[string]string    // localPath -> backend name
	counts   map[string]int       // localPath -> snapshot count
	lastRun  map[string]runStatus // entry name -> status (this session only)
	warn     map[string]string    // entry name -> non-fatal issue (e.g. bad identity)
}

func newList(cfg *config.Config, runner execx.Runner, fsType snapshot.FSTypeFunc) listModel {
	m := listModel{
		cfg: cfg, runner: runner, fsType: fsType,
		backends: map[string]string{},
		counts:   map[string]int{},
		lastRun:  map[string]runStatus{},
		warn:     map[string]string{},
	}
	m.refresh()
	return m
}

// refresh recomputes the effective backend and snapshot count per entry.
func (m *listModel) refresh() {
	m.backends = map[string]string{}
	m.counts = map[string]int{}
	m.warn = map[string]string{}
	ctx := context.Background()
	for _, s := range m.cfg.Sync {
		be := snapshot.Detect(ctx, s.LocalPath, m.runner, m.fsType)
		m.backends[s.LocalPath] = be.Name()
		if times, err := snapshot.List(s.LocalPath); err == nil {
			m.counts[s.LocalPath] = len(times)
		}
		if msg := s.IdentityIssue(); msg != "" {
			m.warn[s.Name] = msg
		}
	}
	if m.cursor >= len(m.cfg.Sync) {
		m.cursor = max0(len(m.cfg.Sync) - 1)
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (m *listModel) setRunStatus(results []syncer.Result) {
	for _, r := range results {
		if r.OK {
			m.lastRun[r.Name] = runOK
		} else {
			m.lastRun[r.Name] = runFail
		}
	}
}

func (m listModel) Init() tea.Cmd { return nil }

func (m listModel) Update(msg tea.Msg) (listModel, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	n := len(m.cfg.Sync)
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "a":
		return m, func() tea.Msg { return editEntryMsg{idx: -1} }
	case "e":
		if n > 0 {
			idx := m.cursor
			return m, func() tea.Msg { return editEntryMsg{idx: idx} }
		}
	case "c":
		if n > 0 {
			idx := m.cursor
			return m, func() tea.Msg { return copyEntryMsg{idx: idx} }
		}
	case "d":
		if n > 0 {
			idx := m.cursor
			return m, func() tea.Msg { return deleteEntryMsg{idx: idx} }
		}
	case "enter":
		if n > 0 {
			idx := m.cursor
			return m, func() tea.Msg { return openSnapsMsg{idx: idx} }
		}
	case "s":
		if n > 0 {
			e := m.cfg.Sync[m.cursor]
			return m, func() tea.Msg { return runEntriesMsg{entries: []config.Sync{e}} }
		}
	case "S":
		if n > 0 {
			entries := append([]config.Sync(nil), m.cfg.Sync...)
			return m, func() tea.Msg { return runEntriesMsg{entries: entries} }
		}
	case "r":
		m.refresh()
	case "q", "esc":
		return m, func() tea.Msg { return requestQuitMsg{} }
	case "ctrl+c":
		return m, func() tea.Msg { return quitMsg{} }
	}
	return m, nil
}

func (m listModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("gsync — 文件同步") + "\n\n")
	if len(m.cfg.Sync) == 0 {
		b.WriteString(styleHelp.Render("（无条目）按 a 新增第一个条目\n"))
	}
	for i, s := range m.cfg.Sync {
		dot := styleDotNever.String()
		switch m.lastRun[s.Name] {
		case runOK:
			dot = styleDotOK.String()
		case runFail:
			dot = styleDotFail.String()
		}
		cursor := "  "
		if i == m.cursor {
			cursor = "▶ "
		}
		// Flag entries with a non-fatal issue (e.g. an inaccessible identity) so
		// they stand out instead of silently blocking a run later.
		mark := " "
		name := fmt.Sprintf("%-12s", s.Name)
		if _, bad := m.warn[s.Name]; bad {
			mark = styleWarn.Render("⚠")
			name = styleWarn.Render(name)
		}
		b.WriteString(fmt.Sprintf("%s%s%s %s %s@%s:%s → %s  %d snaps  %s\n",
			cursor, dot, mark, name, s.User, s.Host, s.RemotePath, s.LocalPath,
			m.counts[s.LocalPath], m.backends[s.LocalPath]))
	}
	if len(m.warn) > 0 {
		b.WriteString("\n")
		// Iterate cfg.Sync (not the map) for stable, config order.
		for _, s := range m.cfg.Sync {
			if msg, bad := m.warn[s.Name]; bad {
				b.WriteString(styleWarn.Render(fmt.Sprintf("⚠ %s: %s", s.Name, msg)) + "\n")
			}
		}
	}
	return b.String()
}
