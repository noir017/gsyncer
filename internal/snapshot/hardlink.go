package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"gsync/internal/execx"
)

// Hardlink implements Backend using `cp -al` hardlink copies. It works on any
// POSIX filesystem and needs no privileges.
type Hardlink struct {
	r execx.Runner
}

// NewHardlink returns a hardlink-backed Backend.
func NewHardlink(r execx.Runner) Backend { return &Hardlink{r: r} }

// Name implements Backend.
func (*Hardlink) Name() string { return "hardlink" }

// EnsureCurrent creates root/current as a plain directory.
func (h *Hardlink) EnsureCurrent(_ context.Context, root string) (string, error) {
	cur := filepath.Join(root, "current")
	return cur, os.MkdirAll(cur, 0o755)
}

// Create hardlink-copies root/current into root/snapshots/<ts>. The copy is
// built into a ".partial" sibling and atomically renamed into place, so an
// interrupted run never leaves a truncated tree under a name that List/retention
// would mistake for a complete snapshot (the ".partial" suffix is not parseable
// as a timestamp, so List skips any orphan left behind by a crash).
func (h *Hardlink) Create(ctx context.Context, root string, ts time.Time) (string, error) {
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		return "", err
	}
	dst := nextFreeSnapshotPath(snaps, ts)
	tmp := dst + ".partial"
	_ = os.RemoveAll(tmp) // clear any leftover from a previous crash
	cur := filepath.Join(root, "current")
	if _, err := h.r.Run(ctx, "cp", "-al", cur, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	return dst, nil
}

// Delete removes a snapshot directory tree.
func (h *Hardlink) Delete(_ context.Context, snapPath string) error {
	return os.RemoveAll(snapPath)
}

// List implements Backend.
func (h *Hardlink) List(root string) ([]time.Time, error) { return List(root) }
