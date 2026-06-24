package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/syncer"
)

func newTestRun() runModel {
	return newRun(&config.Config{}, "", &execx.FakeRunner{}, nonBtrfsFS, time.Now)
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

func TestSummarize(t *testing.T) {
	got := summarize([]syncer.Result{{OK: true}, {OK: false}}, 3400*time.Millisecond)
	if got != "成功 1 / 失败 1 / 耗时 3.4s" {
		t.Fatalf("got %q", got)
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
