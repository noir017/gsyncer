package tui

import (
	"errors"
	"io/fs"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gsyncer/internal/config"
	"gsyncer/internal/execx"
	"gsyncer/internal/snapshot"
)

// Run starts the full-screen TUI program.
func Run(cfgPath, logDir string, runner execx.Runner, fsType snapshot.FSTypeFunc, now func() time.Time) error {
	cfg, err := loadOrEmpty(cfgPath)
	if err != nil {
		return err
	}
	app := newApp(cfgPath, logDir, cfg, runner, fsType, now)
	_, err = tea.NewProgram(app, tea.WithAltScreen()).Run()
	return err
}

// loadOrEmpty loads the config, or returns an empty one if the file is absent
// (design decision 6: TUI may start with no config and create it on first save).
func loadOrEmpty(path string) (*config.Config, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return &config.Config{}, nil
	}
	return config.Load(path)
}
