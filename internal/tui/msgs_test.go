package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChanLoggerInfof(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	chanLogger{ch}.Infof("pulled %d files", 7)
	msg := <-ch
	got, ok := msg.(logLineMsg)
	if !ok {
		t.Fatalf("got %T, want logLineMsg", msg)
	}
	if got.level != "INFO" || got.text != "pulled 7 files" {
		t.Fatalf("got %+v", got)
	}
}

func TestChanLoggerErrorf(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	chanLogger{ch}.Errorf("boom %s", "x")
	got := (<-ch).(logLineMsg)
	if got.level != "ERROR" || got.text != "boom x" {
		t.Fatalf("got %+v", got)
	}
}
