package tui

import (
	"path/filepath"
	"strings"
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
}

func TestAppAppliesSizeOnSnapsEntry(t *testing.T) {
	e := makeSnaps(t, "2026-06-24_030000")
	cfg := &config.Config{Sync: []config.Sync{e}}
	app := newTestApp(cfg, "x")
	// Set a known size on the app before entering the snaps screen.
	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = model.(*App)
	if app.width != 120 {
		t.Fatalf("app.width = %d, want 120", app.width)
	}
	// Now open the snaps screen; the sub-model should be pre-sized.
	model, _ = app.Update(openSnapsMsg{idx: 0})
	app = model.(*App)
	if app.screen != screenSnaps {
		t.Fatalf("screen = %d, want screenSnaps", app.screen)
	}
	if app.snaps.tbl.Width() != 116 {
		t.Fatalf("snaps tbl.Width() = %d, want 116", app.snaps.tbl.Width())
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

func TestAppQuitConfirmFlow(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")

	// requestQuitMsg sets confirmQuit
	model, _ := app.Update(requestQuitMsg{})
	app = model.(*App)
	if !app.confirmQuit {
		t.Fatal("expected confirmQuit == true after requestQuitMsg")
	}

	// 'n' cancels, no quit cmd
	model, cmd := app.Update(keyMsg("n"))
	app = model.(*App)
	if app.confirmQuit {
		t.Fatal("expected confirmQuit == false after 'n'")
	}
	if cmd != nil {
		// cmd should be nil (no quit)
		// tea.Quit returns a non-nil cmd, so verify it's not a quit
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Fatal("'n' should not quit")
		}
	}

	// send requestQuitMsg again, then 'y' should quit
	model, _ = app.Update(requestQuitMsg{})
	app = model.(*App)
	if !app.confirmQuit {
		t.Fatal("expected confirmQuit == true after second requestQuitMsg")
	}
	_, cmd = app.Update(keyMsg("y"))
	if cmd == nil {
		t.Fatal("expected quit cmd after 'y'")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg after 'y', got %T", cmd())
	}
}

func TestAppCopyMsgRoutesToNewForm(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	model, _ := app.Update(copyEntryMsg{idx: 0})
	a := model.(*App)
	if a.screen != screenForm {
		t.Fatalf("screen = %d, want screenForm", a.screen)
	}
	if a.form.origIdx != -1 {
		t.Fatalf("copy form.origIdx = %d, want -1", a.form.origIdx)
	}
}

func TestAppQuitConfirmEnterQuits(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	model, _ := app.Update(requestQuitMsg{})
	app = model.(*App)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected quit cmd after enter (default Y)")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg after enter, got %T", cmd())
	}
}

func TestAppHelpToggle(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")

	// starts visible
	if !app.helpVisible {
		t.Fatal("helpVisible should be true on start")
	}

	// on screenList, '?' toggles off
	app.screen = screenList
	model, _ := app.Update(keyMsg("?"))
	app = model.(*App)
	if app.helpVisible {
		t.Fatal("helpVisible should be false after first '?' on list")
	}

	// again toggles back on
	model, _ = app.Update(keyMsg("?"))
	app = model.(*App)
	if !app.helpVisible {
		t.Fatal("helpVisible should be true after second '?' on list")
	}

	// on screenForm, '?' should NOT toggle (goes to form input instead)
	app.screen = screenForm
	app.form = newForm(twoEntryCfg(), "x", 0)
	before := app.helpVisible
	app.Update(keyMsg("?"))
	if app.helpVisible != before {
		t.Fatal("helpVisible should be unchanged when screenForm receives '?'")
	}
}

func TestAppViewShowsHelpWhenVisible(t *testing.T) {
	app := newTestApp(twoEntryCfg(), "x")
	app.screen = screenList

	// helpVisible true by default: view should contain help text
	view := app.View()
	if !strings.Contains(view, "退出") {
		t.Fatal("expected help text in view when helpVisible=true")
	}

	// toggle off
	model, _ := app.Update(keyMsg("?"))
	app = model.(*App)
	view = app.View()
	if strings.Contains(view, "退出") {
		t.Fatal("expected no help text in view when helpVisible=false")
	}
}
