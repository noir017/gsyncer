package tui

import (
	"time"

	"gsync/internal/execx"
	"gsync/internal/snapshot"
)

// Run starts the TUI. Real implementation lands in Task 7.
func Run(cfgPath, logDir string, runner execx.Runner, fsType snapshot.FSTypeFunc, now func() time.Time) error {
	_ = cfgPath
	_ = logDir
	_ = runner
	_ = fsType
	_ = now
	return nil
}
