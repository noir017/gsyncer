package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/syncer"
)

// fillRun resizes to a small viewport and pushes enough lines to overflow it.
func fillRun(t *testing.T) runModel {
	t.Helper()
	m := newTestRun()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for i := 0; i < 50; i++ {
		m.ch = make(chan tea.Msg, 1)
		m, _ = m.Update(logLineMsg{level: "INFO", text: fmt.Sprintf("line %d", i)})
	}
	return m
}

func TestRunPreservesScrollWhenNotAtBottom(t *testing.T) {
	m := fillRun(t)
	m.vp.GotoTop()
	if m.vp.AtBottom() {
		t.Fatal("precondition: viewport must be scrollable and not at bottom")
	}
	off := m.vp.YOffset
	m.ch = make(chan tea.Msg, 1)
	m, _ = m.Update(logLineMsg{level: "INFO", text: "arrived while scrolled up"})
	if m.vp.YOffset != off {
		t.Fatalf("scroll position moved: YOffset %d -> %d", off, m.vp.YOffset)
	}
}

func TestRunFollowsTailWhenAtBottom(t *testing.T) {
	m := fillRun(t)
	if !m.vp.AtBottom() {
		t.Fatal("precondition: should be following at bottom after filling")
	}
	m.ch = make(chan tea.Msg, 1)
	m, _ = m.Update(logLineMsg{level: "INFO", text: "another"})
	if !m.vp.AtBottom() {
		t.Fatal("must stay pinned to bottom while following")
	}
}

func newTestRun() runModel {
	return newRun(&config.Config{}, "", "", &execx.FakeRunner{}, nonBtrfsFS, time.Now)
}

func TestRunAppendsLogLines(t *testing.T) {
	m := newTestRun()
	m.ch = make(chan tea.Msg, 1)
	m, _ = m.Update(logLineMsg{level: "INFO", text: "hello"})
	if len(m.lines) != 1 || m.lines[0] != "hello" {
		t.Fatalf("lines = %v", m.lines)
	}
}

func TestRunDoneClearsRunning(t *testing.T) {
	m := newTestRun()
	m.running = true
	m.cancelling = true
	m, _ = m.Update(runDoneMsg{results: []syncer.Result{{Name: "a", OK: true}}})
	if m.running {
		t.Fatal("running should be false after done")
	}
	if m.cancelling {
		t.Fatal("cancelling should be false after done")
	}
	if len(m.results) != 1 {
		t.Fatal("results not stored")
	}
}

func TestRunFirstCtrlCCancelsSecondQuits(t *testing.T) {
	m := newTestRun()
	m.running = true
	m.ch = make(chan tea.Msg, 1)
	cancelled := false
	m.cancel = func() { cancelled = true }
	// first ctrl+c -> cancel requested, running stays true, keeps listening
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !cancelled {
		t.Fatal("first ctrl+c must call cancel")
	}
	if !m.cancelling {
		t.Fatal("cancelling must be true after first ctrl+c")
	}
	if !m.running {
		t.Fatal("running must stay true until runDoneMsg")
	}
	if cmd == nil {
		t.Fatal("must keep listening for trailing runDoneMsg")
	}
	// second ctrl+c -> quit
	_, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := cmd2().(quitMsg); !ok {
		t.Fatal("second ctrl+c must emit quitMsg")
	}
}

func TestRunEscBlockedWhileCancelling(t *testing.T) {
	m := newTestRun()
	m.running = true
	m.ch = make(chan tea.Msg, 1)
	m.cancel = func() {}
	// trigger cancelling state via first ctrl+c
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.cancelling || !m.running {
		t.Fatal("precondition: must be running and cancelling")
	}
	// esc while still running/cancelling must NOT emit backToListMsg
	m.ch = make(chan tea.Msg, 1) // re-arm channel so Update doesn't block
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		result := cmd()
		if _, ok := result.(backToListMsg); ok {
			t.Fatal("esc must not emit backToListMsg while running/cancelling")
		}
	}
	// after runDoneMsg, running and cancelling must both be false
	m, _ = m.Update(runDoneMsg{results: []syncer.Result{{Name: "a", OK: true}}, cancelled: true})
	if m.running {
		t.Fatal("running must be false after runDoneMsg")
	}
	if m.cancelling {
		t.Fatal("cancelling must be false after runDoneMsg")
	}
	// now esc must emit backToListMsg
	_, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd2 == nil {
		t.Fatal("esc after done must emit backToListMsg")
	}
	if _, ok := cmd2().(backToListMsg); !ok {
		t.Fatalf("esc after done must emit backToListMsg, got %T", cmd2())
	}
}

func TestRunEnterAfterDoneEmitsBack(t *testing.T) {
	m := newTestRun()
	m.running = false
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := cmd().(backToListMsg); !ok {
		t.Fatal("enter after done must emit backToListMsg")
	}
}

func TestRunResizesViewport(t *testing.T) {
	m := newTestRun()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.vp.Width != 116 {
		t.Fatalf("vp.Width = %d, want 116", m.vp.Width)
	}
	if m.vp.Height != 31 {
		t.Fatalf("vp.Height = %d, want 31", m.vp.Height)
	}
}

func TestSummarize(t *testing.T) {
	got := summarize([]syncer.Result{{OK: true}, {OK: false}}, 3400*time.Millisecond)
	if got != "成功 1 / 失败 1 / 耗时 3.4s" {
		t.Fatalf("got %q", got)
	}
}

func TestRunTickReArmsWhileRunningStopsWhenDone(t *testing.T) {
	m := newTestRun()
	m.running = true
	// while running, a tick advances the spinner and re-arms itself
	before := m.spinner
	m, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("tick must re-arm while running")
	}
	if m.spinner != before+1 {
		t.Fatalf("spinner = %d, want %d", m.spinner, before+1)
	}
	// once not running, a stray tick is a no-op and does NOT re-arm
	m.running = false
	m, cmd = m.Update(tickMsg{})
	if cmd != nil {
		t.Fatal("tick must not re-arm after run finished")
	}
}

func TestFmtElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		0:                          "00:00",
		5 * time.Second:            "00:05",
		75 * time.Second:           "01:15",
		(2*60 + 3) * time.Second:   "02:03",
		(100*60 + 9) * time.Second: "100:09",
	}
	for d, want := range cases {
		if got := fmtElapsed(d); got != want {
			t.Fatalf("fmtElapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestRunDoneSummaryUsesDuration(t *testing.T) {
	m := newTestRun()
	m.running = true
	m, _ = m.Update(runDoneMsg{
		results: []syncer.Result{{OK: true}},
		dur:     3400 * time.Millisecond,
	})
	if len(m.lines) == 0 {
		t.Fatal("no lines appended")
	}
	last := m.lines[len(m.lines)-1]
	if !strings.Contains(last, "耗时 3.4s") {
		t.Fatalf("duration not in summary: %q", last)
	}
	if !strings.Contains(last, "成功 1 / 失败 0") {
		t.Fatalf("counts not in summary: %q", last)
	}
}
