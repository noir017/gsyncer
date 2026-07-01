package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"gsyncer/internal/execx"
)

// Hardlink implements Backend using `cp -al` hardlink copies. It works on any
// POSIX filesystem and needs no privileges. On copy-on-write filesystems it
// upgrades to reflink copies (see Create): reflinks give each snapshot an
// independent inode that shares data blocks with current/ via CoW, so a later
// in-place write to a file in current/ can't reach back and corrupt an existing
// snapshot — the risk hardlinks carry, since they share the inode itself. (rsync
// normally replaces files via temp+rename, which already breaks hardlink sharing
// safely, but reflink removes the sharp edge entirely where the FS supports it.)
type Hardlink struct {
	r execx.Runner
	// reflink records whether the last Create used a reflink copy. It is only
	// meaningful after Create has run and drives Mode(); each SyncOne owns its own
	// backend instance, so no synchronization is needed.
	reflink bool
}

// NewHardlink returns a hardlink-backed Backend.
func NewHardlink(r execx.Runner) Backend { return &Hardlink{r: r} }

// Name implements Backend. It reports the backend family; the concrete copy
// technique (reflink vs hardlink) chosen for the last Create is exposed via
// Mode.
func (*Hardlink) Name() string { return "hardlink" }

// Mode reports the copy technique used by the most recent Create: "reflink" on
// a CoW filesystem, otherwise "hardlink". The syncer reads this (via an optional
// interface) to surface the real mode in the run summary.
func (h *Hardlink) Mode() string {
	if h.reflink {
		return "reflink"
	}
	return "hardlink"
}

// probeReflink reports whether dir's filesystem supports reflink (CoW) copies,
// by reflink-copying a tiny throwaway file with `cp --reflink=always` (which,
// unlike =auto, fails instead of silently falling back to a full data copy — so
// a true result really means CoW). Both probe files are removed before return.
func probeReflink(ctx context.Context, r execx.Runner, dir string) bool {
	src := filepath.Join(dir, ".gsyncer-reflink-probe")
	dst := src + ".copy"
	_ = os.Remove(dst)
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		return false
	}
	defer os.Remove(src)
	defer os.Remove(dst)
	_, err := r.Run(ctx, "cp", "--reflink=always", src, dst)
	return err == nil
}

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
	// Prefer an independent reflink copy on CoW filesystems; fall back to a
	// space-sharing hardlink copy everywhere else. Probing per Create keeps it
	// correct for the btrfs->hardlink fallback path, where a fresh backend is
	// built without EnsureCurrent. (Create only runs on a real, non-dry sync, so
	// the probe never writes during a dry-run.)
	h.reflink = probeReflink(ctx, h.r, snaps)
	var cpErr error
	if h.reflink {
		_, cpErr = h.r.Run(ctx, "cp", "--reflink=always", "-a", cur, tmp)
	} else {
		_, cpErr = h.r.Run(ctx, "cp", "-al", cur, tmp)
	}
	if cpErr != nil {
		_ = os.RemoveAll(tmp)
		return "", cpErr
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
