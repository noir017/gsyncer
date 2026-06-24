package tui

import (
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
	m, _ = m.Update(runDoneMsg{results: []syncer.Result{{Name: "a", OK: true}}})
	if m.running {
		t.Fatal("running should be false after done")
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
	// first ctrl+c -> cancel, not quit
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !cancelled {
		t.Fatal("first ctrl+c must call cancel")
	}
	if m.running {
		t.Fatal("running must be false after cancel request")
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
