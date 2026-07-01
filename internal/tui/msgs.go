// Package tui implements the interactive terminal UI for gsyncer.
package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
	"gsyncer/internal/syncer"
)

// screen identifies which sub-model is currently active.
type screen int

const (
	screenList screen = iota
	screenForm
	screenRun
	screenSnaps
)

// logLineMsg is one line of run output produced by the sync goroutine.
type logLineMsg struct {
	level string // "INFO" | "ERROR"
	text  string
}

// runDoneMsg signals the sync goroutine finished (cancelled true if interrupted).
type runDoneMsg struct {
	results   []syncer.Result
	cancelled bool
	dur       time.Duration
}

// tickMsg drives the run screen's spinner + elapsed-time animation while a sync
// is in progress. It is re-armed each second only while the run is running.
type tickMsg struct{}

// editEntryMsg asks App to open the form. idx == -1 means a new entry.
type editEntryMsg struct{ idx int }

// copyEntryMsg asks App to open the form as a new entry pre-filled from entry idx.
type copyEntryMsg struct{ idx int }

// openSnapsMsg asks App to open the snapshot browser for entry idx.
type openSnapsMsg struct{ idx int }

// runEntriesMsg asks App to open the run screen for the given entries.
type runEntriesMsg struct {
	entries []config.Sync
	dryRun  bool
}

// snapSizesMsg carries the nominal sizes computed off the UI goroutine for the
// snapshot browser, keyed by timestamp string. epoch guards a stale computation
// from overwriting rows after the list changed (a delete/prune bumps the epoch).
type snapSizesMsg struct {
	epoch int
	sizes map[string]string
}

// snapOpDoneMsg reports that a background snapshot op (restore/prune/delete)
// finished. status is the line to show; reload asks the browser to re-list.
type snapOpDoneMsg struct {
	status string
	reload bool
}

// backToListMsg returns to the main menu.
type backToListMsg struct{}

// quitMsg asks App to terminate the program.
type quitMsg struct{}

// statusMsg sets the global status line.
type statusMsg struct {
	text  string
	isErr bool
}

// configChangedMsg tells App the config was saved; rebuild list + clear caches.
type configChangedMsg struct{}

// deleteEntryMsg asks App to delete entry idx (App shows the confirm dialog).
type deleteEntryMsg struct{ idx int }

// requestQuitMsg asks App to show the quit-confirm dialog.
type requestQuitMsg struct{}

// chanLogger adapts syncer.Logger onto a tea.Msg channel so run output streams
// into the UI.
type chanLogger struct{ ch chan<- tea.Msg }

// Infof implements syncer.Logger.
func (l chanLogger) Infof(format string, a ...any) {
	l.ch <- logLineMsg{level: "INFO", text: fmt.Sprintf(format, a...)}
}

// Errorf implements syncer.Logger.
func (l chanLogger) Errorf(format string, a ...any) {
	l.ch <- logLineMsg{level: "ERROR", text: fmt.Sprintf(format, a...)}
}
