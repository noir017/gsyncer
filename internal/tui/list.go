package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/snapshot"
	"gsyncer/internal/syncer"
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

	width, height int

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
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = sz.Width, sz.Height
		return m, nil
	}
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

// visibleRows is how many entry rows fit, reserving lines for the title block
// (2), the App-drawn status + help footer (2), the scroll hint (1), and the
// variable-height warnings block when present. A non-positive height (size not
// yet known) means "no clamp" — render all rows.
func (m listModel) visibleRows() int {
	if m.height <= 0 {
		return 0
	}
	reserve := 5
	if len(m.warn) > 0 {
		reserve += len(m.warn) + 1 // blank separator + one line per warning
	}
	return clampMin(m.height-reserve, 1)
}

// windowRange returns [start,end) of rows to render, centering the cursor so it
// is always visible; returns the full range when everything fits or size is
// unknown.
func (m listModel) windowRange(n int) (int, int) {
	avail := m.visibleRows()
	if avail <= 0 || n <= avail {
		return 0, n
	}
	start := m.cursor - avail/2
	if start < 0 {
		start = 0
	}
	if start+avail > n {
		start = n - avail
	}
	return start, start + avail
}

// truncateWidth shortens s to at most w display columns, appending an ellipsis
// when it is cut. w<=0 returns s unchanged. Trailing text is plain (paths,
// backend name), so trimming from the end never splits the leading dot's ANSI.
func truncateWidth(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// shrinkMiddle shortens s to at most w display columns by replacing the middle
// with an ellipsis, keeping both ends visible: a path's head and its last
// components carry the information, the middle matters least. w<=0 returns s
// unchanged, matching truncateWidth. Only for plain (unstyled) strings.
func shrinkMiddle(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	r := []rune(s)
	headW := w / 2 // head gets the extra column when w-1 is odd
	i, cw := 0, 0
	for i < len(r) {
		rw := lipgloss.Width(string(r[i]))
		if cw+rw > headW {
			break
		}
		cw += rw
		i++
	}
	tailW := w - 1 - cw
	j, tw := len(r), 0
	for j > i {
		rw := lipgloss.Width(string(r[j-1]))
		if tw+rw > tailW {
			break
		}
		tw += rw
		j--
	}
	return string(r[:i]) + "…" + string(r[j:])
}

// fitTwo shrinks a and b so together they fit within budget columns, splitting
// the space evenly but letting a side that already fits donate its surplus to
// the other.
func fitTwo(a, b string, budget int) (string, string) {
	wa, wb := lipgloss.Width(a), lipgloss.Width(b)
	if wa+wb <= budget {
		return a, b
	}
	half := budget / 2
	switch {
	case wa <= half:
		return a, shrinkMiddle(b, budget-wa)
	case wb <= budget-half:
		return shrinkMiddle(a, budget-wb), b
	default:
		return shrinkMiddle(a, half), shrinkMiddle(b, budget-half)
	}
}

// renderRow renders one entry row. The selected row carries a background
// highlight, so every segment (including plain spaces and padding) must be
// rendered through a style that sets that background — a raw segment would
// punch a hole in the highlight.
func (m listModel) renderRow(i int, s config.Sync) string {
	selected := i == m.cursor
	seg := lipgloss.NewStyle() // plain text segments
	dim := styleHelp           // secondary text (arrow, meta)
	nameStyle := seg           // entry name
	cursorTxt := "  "
	if selected {
		seg = seg.Background(colSelBg)
		dim = dim.Background(colSelBg)
		nameStyle = styleLabelOn.Background(colSelBg)
		cursorTxt = "▶ "
	}
	dot := styleDotNever
	switch m.lastRun[s.Name] {
	case runOK:
		dot = styleDotOK
	case runFail:
		dot = styleDotFail
	}
	if selected {
		dot = dot.Background(colSelBg)
	}
	// Flag entries with a non-fatal issue (e.g. an inaccessible identity) so
	// they stand out instead of silently blocking a run later.
	mark := seg.Render(" ")
	name := fmt.Sprintf("%-12s", s.Name)
	if _, bad := m.warn[s.Name]; bad {
		warnStyle := styleWarn
		if selected {
			warnStyle = warnStyle.Background(colSelBg)
		}
		mark = warnStyle.Render("⚠")
		name = warnStyle.Render(name)
	} else {
		name = nameStyle.Render(name)
	}
	head := nameStyle.Render(cursorTxt) + dot.String() + mark + seg.Render(" ") + name + seg.Render(" ")

	// The three tail pieces are fitted as plain text first, then styled, so the
	// width math never has to slice through an escape sequence.
	remote := fmt.Sprintf("%s@%s:%s", s.User, s.Host, s.RemotePath)
	local := s.LocalPath
	meta := fmt.Sprintf("  %d snaps  %s", m.counts[s.LocalPath], m.backends[s.LocalPath])
	plainTail := remote + " → " + local + meta
	var tail string
	switch avail := m.width - lipgloss.Width(head); {
	case m.width > 0 && avail <= 0:
		tail = ""
	case m.width > 0 && lipgloss.Width(plainTail) > avail:
		// Too narrow: keep the short meta (snap count, backend) intact and
		// shrink the two paths from the middle so both their ends stay
		// readable; if even that leaves no room for paths, fall back to plain
		// end-truncation of the whole tail.
		budget := avail - lipgloss.Width(meta) - lipgloss.Width(" → ")
		if budget >= 8 {
			r2, l2 := fitTwo(remote, local, budget)
			tail = seg.Render(r2) + dim.Render(" → ") + seg.Render(l2) + dim.Render(meta)
		} else {
			tail = seg.Render(truncateWidth(plainTail, avail))
		}
	default:
		tail = seg.Render(remote) + dim.Render(" → ") + seg.Render(local) + dim.Render(meta)
	}
	row := head + tail
	// Extend the selected row's highlight to the full terminal width.
	if selected && m.width > 0 {
		if pad := m.width - lipgloss.Width(row); pad > 0 {
			row += seg.Render(strings.Repeat(" ", pad))
		}
	}
	return row
}

func (m listModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitleChip.Render("gsyncer") +
		styleHelp.Render(fmt.Sprintf("  文件同步 · %d 条目", len(m.cfg.Sync))) + "\n")
	b.WriteString(rule(m.width) + "\n")
	if len(m.cfg.Sync) == 0 {
		b.WriteString(styleHelp.Render("（无条目）按 ") + styleHelpKey.Render("a") +
			styleHelp.Render(" 新增第一个条目") + "\n")
		return b.String()
	}
	rows := make([]string, len(m.cfg.Sync))
	for i, s := range m.cfg.Sync {
		rows[i] = m.renderRow(i, s)
	}
	start, end := m.windowRange(len(rows))
	for i := start; i < end; i++ {
		b.WriteString(rows[i] + "\n")
	}
	if start > 0 || end < len(rows) {
		b.WriteString(styleHelp.Render(fmt.Sprintf("  ── %d–%d / %d ──", start+1, end, len(rows))) + "\n")
	}
	if len(m.warn) > 0 {
		b.WriteString("\n")
		// Iterate cfg.Sync (not the map) for stable, config order.
		for _, s := range m.cfg.Sync {
			if msg, bad := m.warn[s.Name]; bad {
				// Truncate the plain text before styling so a long path cannot
				// overflow the width or split an escape sequence.
				line := fmt.Sprintf("⚠ %s: %s", s.Name, msg)
				if m.width > 0 {
					line = truncateWidth(line, m.width)
				}
				b.WriteString(styleWarn.Render(line) + "\n")
			}
		}
	}
	return b.String()
}
