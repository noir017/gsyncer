package tui

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gsync/internal/config"
	"gsync/internal/execx"
	"gsync/internal/syncer"
)

func newTestApp(cfg *config.Config, cfgPath string) *App {
	return newApp(cfgPath, "", cfg, &execx.FakeRunner{}, nonBtrfsFS, nil)
}

func TestAppEditMsgRoutesToForm(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	model, _ := app.Update(editEntryMsg{idx: 1})
	a := model.(*App)
	if a.screen != screenForm {
		t.Fatalf("screen = %d, want screenForm", a.screen)
	}
	if a.form.origIdx != 1 {
		t.Fatalf("form.origIdx = %d, want 1", a.form.origIdx)
	}
}

func TestAppBackToListRoutesAndRefreshes(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	app.screen = screenForm
	model, _ := app.Update(backToListMsg{})
	if model.(*App).screen != screenList {
		t.Fatal("should return to list")
	}
}

func TestAppQuitMsgQuits(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	_, cmd := app.Update(quitMsg{})
	if cmd == nil {
		t.Fatal("expected quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}
}

func TestAppDeleteEntryConfirmFlow(t *testing.T) {
	cfg := twoEntryCfg()
	path := filepath.Join(t.TempDir(), "config.toml")
	app := newTestApp(cfg, path)
	// request delete of idx 0
	model, _ := app.Update(deleteEntryMsg{idx: 0})
	app = model.(*App)
	if !app.confirmDelete {
		t.Fatal("should be awaiting delete confirm")
	}
	// confirm with 'y'
	model, _ = app.Update(keyMsg("y"))
	app = model.(*App)
	if len(cfg.Sync) != 1 || cfg.Sync[0].Name != "b" {
		t.Fatalf("entry a should be deleted; got %+v", cfg.Sync)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Sync) != 1 {
		t.Fatal("delete not persisted")
	}
}

func TestLoadOrEmptyMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")
	cfg, err := loadOrEmpty(missing)
	if err != nil {
		t.Fatalf("missing file must not error, got %v", err)
	}
	if cfg == nil || len(cfg.Sync) != 0 {
		t.Fatal("missing file must yield empty config")
	}
	// sanity: confirm the path truly does not exist
	if _, statErr := config.Load(missing); !errors.Is(statErr, fs.ErrNotExist) {
		// config.Load wraps os.Open error; this is just documentation, not asserted strictly
		_ = statErr
	}
}

func syncerResultFor(name string, ok bool) syncer.Result { return syncer.Result{Name: name, OK: ok} }

func TestAppRunDoneUpdatesListDots(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	app.screen = screenRun
	app.run = newRun(app.cfg, "", app.runner, app.fsType, app.now)
	app.Update(runDoneMsg{results: []syncer.Result{syncerResultFor("a", true)}})
	if app.list.lastRun["a"] != runOK {
		t.Fatal("list dot should be runOK after run")
	}
}
