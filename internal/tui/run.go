package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

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
	ok, fail := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
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

	vp        viewport.Model
	lines     []string
	running   bool
	cancelled bool
	cancel    context.CancelFunc
	ch        chan tea.Msg
	results   []syncer.Result
	title     string
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
	m.cancelled = false
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
		line := summarize(results, m.now().Sub(start))
		lg.Infof("%s", line)
		if rl != nil {
			_ = logx.AppendSummary(m.logDir, start.Format("2006-01-02 15:04:05")+" "+line)
			_ = rl.Close()
			_ = logx.Cleanup(m.logDir, m.cfg.Log.KeepDays, m.cfg.Log.KeepCount, m.now())
		}
		ch <- runDoneMsg{results: results, cancelled: ctx.Err() != nil}
	}()

	return waitForMsg(ch)
}

func (m runModel) Init() tea.Cmd { return nil }

func (m runModel) Update(msg tea.Msg) (runModel, tea.Cmd) {
	switch msg := msg.(type) {
	case logLineMsg:
		m.lines = append(m.lines, msg.text)
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		m.vp.GotoBottom()
		return m, waitForMsg(m.ch)

	case runDoneMsg:
		m.running = false
		m.results = msg.results
		summary := "✔ " + summarize(msg.results, 0)
		if msg.cancelled {
			summary = "⚠ 已取消 — " + summarize(msg.results, 0)
		}
		m.lines = append(m.lines, summary)
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		m.vp.GotoBottom()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.running {
				if m.cancel != nil {
					m.cancel()
				}
				m.running = false
				m.cancelled = true
				m.lines = append(m.lines, "⚠ 已请求取消…")
				m.vp.SetContent(strings.Join(m.lines, "\n"))
				return m, waitForMsg(m.ch) // keep draining until runDoneMsg
			}
			return m, func() tea.Msg { return quitMsg{} }
		case "enter", "esc":
			if !m.running {
				return m, func() tea.Msg { return backToListMsg{} }
			}
		}
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m runModel) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(m.title) + "\n\n")
	b.WriteString(styleBox.Render(m.vp.View()) + "\n")
	if m.running {
		b.WriteString(styleHelp.Render("(运行中) ctrl+c 中断"))
	} else {
		b.WriteString(styleHelp.Render("(完成) enter/esc 返回，ctrl+c 退出"))
	}
	return b.String()
}
