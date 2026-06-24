# Task 8 Report — Terminal Resize Support

## Status: COMPLETE

## Changes Made

### run.go
- Added `clampMin(n, lo int) int` helper (single definition; reused from snapshots.go via shared package).
- Added `case tea.WindowSizeMsg:` in `runModel.Update` before the viewport fallthrough.
  Sets `vp.Width = clampMin(Width-4, 10)` and `vp.Height = clampMin(Height-9, 3)`, refreshes content.

### snapshots.go
- Added `tea.WindowSizeMsg` guard at the top of `snapsModel.Update` (before the KeyMsg-only early return).
  Calls `m.tbl.SetWidth(clampMin(Width-4, 20))` and `m.tbl.SetHeight(clampMin(Height-10, 3))`.
  Reuses `clampMin` from run.go (same package — no redefinition).

### app.go
- `openSnapsMsg` case: after `newSnaps(...)`, if `a.width > 0`, feeds `tea.WindowSizeMsg` to `a.snaps`.
- `runEntriesMsg` case: after `newRun(...)`, if `a.width > 0`, feeds `tea.WindowSizeMsg` to `a.run` BEFORE calling `a.run.start(...)`.

## Table Getter Availability (bubbles v0.20.0)

`table.Model` exposes both `Width() int` and `Height() int` getters.

**Important**: `Height()` returns `m.viewport.Height`, which equals `SetHeight(h) - lipgloss.Height(headersView())`.
For a standard single-row header, `Height()` returns `h - 1`.
So `SetHeight(30)` → `Height() == 29`.

Test assertion uses `29` (not `30`) with an explanatory comment.

## Test Results

### RED (before implementation)
```
--- FAIL: TestRunResizesViewport   vp.Width = 60, want 116
--- FAIL: TestSnapsResizesTable    tbl.Width() = 0, want 116
--- FAIL: TestAppAppliesSizeOnSnapsEntry  snaps tbl.Width() = 0, want 116
```

### GREEN (after implementation)
```
--- PASS: TestRunResizesViewport    (vp.Width==116, vp.Height==31)
--- PASS: TestSnapsResizesTable     (tbl.Width()==116, tbl.Height()==29)
--- PASS: TestAppAppliesSizeOnSnapsEntry (tbl.Width()==116)
```

Full suite: all packages PASS. `go vet` clean. `CGO_ENABLED=0 go build` clean.

## Assertions

- `TestRunResizesViewport`: Width=120-4=116 ✓, Height=40-9=31 ✓
- `TestSnapsResizesTable`: Width=120-4=116 ✓, Height=SetHeight(40-10=30)→Height()=29 ✓
- `TestAppAppliesSizeOnSnapsEntry`: WindowSizeMsg first sets app.width=120, openSnapsMsg pre-sizes snaps → tbl.Width()==116 ✓
