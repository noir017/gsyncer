// Package restore copies a snapshot back out to a destination directory. The
// logic is factored here (rather than living only in the TUI) so it is usable
// headlessly from `gsyncer restore` in scripts and disaster-recovery scenarios.
package restore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gsyncer/internal/execx"
	"gsyncer/internal/snapshot"
)

// SelectTime chooses one snapshot timestamp. Exactly one selector must be set:
// latest picks the newest; at matches a TSLayout string ("2006-01-02_150405")
// exactly. It returns a descriptive error (empty list, bad format, no match).
func SelectTime(times []time.Time, at string, latest bool) (time.Time, error) {
	if len(times) == 0 {
		return time.Time{}, fmt.Errorf("no snapshots found")
	}
	sorted := append([]time.Time(nil), times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].After(sorted[j]) })
	if latest {
		return sorted[0], nil
	}
	want, err := time.Parse(snapshot.TSLayout, at)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --at %q (want %s)", at, snapshot.TSLayout)
	}
	for _, t := range sorted {
		if t.Equal(want) {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("no snapshot at %q", at)
}

// SnapPath returns the on-disk path of the snapshot taken at t under localPath.
func SnapPath(localPath string, t time.Time) string {
	return filepath.Join(localPath, "snapshots", t.Format(snapshot.TSLayout))
}

// Run restores snapPath into dst with `cp -a` (preserving attributes/hardlinks
// as the TUI does). It refuses to clobber the entry's current/ directory, and
// refuses an existing dst unless force is set; with force it removes dst first
// so the copy replaces the tree rather than nesting inside it.
func Run(ctx context.Context, runner execx.Runner, localPath, snapPath, dst string, force bool) error {
	// Resolve dst to an absolute path before the current/ guard: localPath is
	// always absolute, so comparing a relative or trailing-slash dst (e.g.
	// `--to current` run from inside localPath) against it would silently miss
	// the guard and let --force RemoveAll the live current/ tree.
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}
	cur := filepath.Clean(filepath.Join(localPath, "current"))
	if absDst == cur {
		return fmt.Errorf("refusing to restore over the current/ directory; choose another --to path")
	}
	if _, err := os.Stat(absDst); err == nil {
		if !force {
			return fmt.Errorf("destination %s exists; pass --force to overwrite", absDst)
		}
		// A bare `cp -a src dst` into an existing dir nests as dst/<name>/… and
		// leaves stale files; remove first so the restore is a clean replace.
		if err := os.RemoveAll(absDst); err != nil {
			return fmt.Errorf("clear destination: %w", err)
		}
	}
	if _, err := runner.Run(ctx, "cp", "-a", snapPath, absDst); err != nil {
		return fmt.Errorf("cp -a: %w", err)
	}
	return nil
}
