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

// Create hardlink-copies root/current into root/snapshots/<ts>.
func (h *Hardlink) Create(ctx context.Context, root string, ts time.Time) (string, error) {
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		return "", err
	}
	dst := nextFreeSnapshotPath(snaps, ts)
	cur := filepath.Join(root, "current")
	if _, err := h.r.Run(ctx, "cp", "-al", cur, dst); err != nil {
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
