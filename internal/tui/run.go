package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/logx"
	"gsync/internal/snapshot"
	"gsync/internal/syncer"
)

// teeLogger forwards every log call to two loggers (UI channel + file).
type teeLogger struct{ a, b syncer.Logger }

func (t teeLogger) Infof(f string, x ...any)  { t.a.Infof(f, x...); t.b.Infof(f, x...) }
func (t teeLogger) Errorf(f string, x ...any) { t.a.Errorf(f, x...); t.b.Errorf(f, x...) }

func summarize(results []syncer.Result, dur time.Duration) string {
	ok, fail, skip := 0, 0, 0
	for _, r := range results {
		switch {
		case r.OK:
			ok++
		case r.Skipped:
			skip++
		default:
			fail++
		}
	}
	if skip > 0 {
		return fmt.Sprintf("成功 %d / 失败 %d / 跳过 %d / 耗时 %.1fs", ok, fail, skip, dur.Seconds())
	}
	return fmt.Sprintf("成功 %d / 失败 %d / 耗时 %.1fs", ok, fail, dur.Seconds())
}

// waitForMsg blocks on the channel inside a tea.Cmd (the classic listen pattern).
func waitForMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

type runModel struct {
	cfg    *config.Config
	logDir string
	runner execx.Runner
	fsType snapshot.FSTypeFunc
	now    func() time.Time

	vp         viewport.Model
	lines      []string
	running    bool
	cancelling bool
	cancel     context.CancelFunc
	ch         chan tea.Msg
	results    []syncer.Result
	title      string
}

func newRun(cfg *config.Config, logDir string, runner execx.Runner, fsType snapshot.FSTypeFunc, now func() time.Time) runModel {
	return runModel{
		cfg: cfg, logDir: logDir, runner: runner, fsType: fsType, now: now,
		vp: viewport.New(60, 12),
	}
}

// start launches the sync goroutine and returns the first listen command.
func (m *runModel) start(entries []config.Sync, dryRun bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.running = true
	m.cancelling = false
	m.lines = nil
	m.results = nil
	m.ch = make(chan tea.Msg, 64)
	m.title = fmt.Sprintf("同步中: %d 条", len(entries))
	ch := m.ch

	go func() {
		start := m.now()
		rl, _ := logx.NewRunLogger(m.logDir, start) // best-effort file log
		var lg syncer.Logger = chanLogger{ch}
		if rl != nil {
			lg = teeLogger{a: chanLogger{ch}, b: rl}
		}
		deps := syncer.Deps{Runner: m.runner, FSType: m.fsType, Now: m.now, Log: lg}
		results := syncer.SyncMany(ctx, entries, m.cfg.Defaults, deps, dryRun)
		dur := m.now().Sub(start)
		line := summarize(results, dur)
		if rl != nil {
			rl.Infof("%s", line)
			_ = logx.AppendSummary(m.logDir, start.Format("2006-01-02 15:04:05")+" "+line)
			_ = rl.Close()
			_ = logx.Cleanup(m.logDir, m.cfg.Log.KeepDays, m.cfg.Log.KeepCount, m.now())
		}
		ch <- runDoneMsg{results: results, cancelled: ctx.Err() != nil, dur: dur}
	}()

	return waitForMsg(ch)
}

// refreshContent re-renders the log buffer into the viewport, soft-wrapping
// each line to the viewport width so long paths never run off-screen.
func (m *runModel) refreshContent() {
	content := strings.Join(m.lines, "\n")
	if m.vp.Width > 0 {
		content = lipgloss.NewStyle().Width(m.vp.Width).Render(content)
	}
	m.vp.SetContent(content)
}

// clampMin returns n if n >= lo, otherwise lo.
func clampMin(n, lo int) int {
	if n < lo {
		return lo
	}
	return n
}

func (m runModel) Init() tea.Cmd { return nil }

func (m runModel) Update(msg tea.Msg) (runModel, tea.Cmd) {
	switch msg := msg.(type) {
	case logLineMsg:
		m.lines = append(m.lines, msg.text)
		m.refreshContent()
		m.vp.GotoBottom()
		return m, waitForMsg(m.ch)

	case runDoneMsg:
		m.running = false
		m.cancelling = false
		m.results = msg.results
		summary := "✔ " + summarize(msg.results, msg.dur)
		if msg.cancelled {
			summary = "⚠ 已取消 — " + summarize(msg.results, msg.dur)
		}
		m.lines = append(m.lines, summary)
		m.refreshContent()
		m.vp.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.running && !m.cancelling {
				if m.cancel != nil {
					m.cancel()
				}
				m.cancelling = true
				m.lines = append(m.lines, "⚠ 已请求取消…")
				m.refreshContent()
				m.vp.GotoBottom()
				return m, waitForMsg(m.ch) // keep draining until runDoneMsg
			}
			return m, func() tea.Msg { return quitMsg{} }
		case "enter", "esc":
			if !m.running {
				return m, func() tea.Msg { return backToListMsg{} }
			}
		}

	case tea.WindowSizeMsg:
		m.vp.Width = clampMin(msg.Width-4, 10)
		m.vp.Height = clampMin(msg.Height-9, 3)
		m.refreshContent()
		return m, nil
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m runModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(m.title) + "\n\n")
	b.WriteString(styleBox.Render(m.vp.View()) + "\n")
	return b.String()
}
