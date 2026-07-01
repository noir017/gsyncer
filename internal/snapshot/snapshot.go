// Package snapshot abstracts snapshot creation over hardlink and btrfs backends.
package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gsync/internal/execx"
)

// TSLayout is the timestamp format used for snapshot directory names.
const TSLayout = "2006-01-02_150405"

// BtrfsMagic is the statfs f_type for a btrfs filesystem.
const BtrfsMagic int64 = 0x9123683E

// FSTypeFunc returns the filesystem magic number for a path.
type FSTypeFunc func(path string) (int64, error)

// RealFSType returns the statfs f_type for path (Linux).
func RealFSType(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}

// Backend creates and manages snapshots under a local root directory.
type Backend interface {
	Name() string
	EnsureCurrent(ctx context.Context, root string) (currentPath string, err error)
	Create(ctx context.Context, root string, ts time.Time) (snapPath string, err error)
	Delete(ctx context.Context, snapPath string) error
	List(root string) ([]time.Time, error)
}

// List parses the timestamps of existing snapshot directories under
// root/snapshots. A missing directory yields an empty slice, not an error.
func List(root string) ([]time.Time, error) {
	dir := filepath.Join(root, "snapshots")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := time.Parse(TSLayout, e.Name())
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// nextFreeSnapshotPath returns a path under snaps whose basename does not yet
// exist. Snapshot names have one-second resolution, so two syncs of the same
// entry within a second would collide; we bump ts by whole seconds until the
// name is free. Bumping (rather than appending a suffix) keeps the name
// parseable by TSLayout, so List and retention still see the snapshot.
func nextFreeSnapshotPath(snaps string, ts time.Time) string {
	// Cap the bump so a persistent stat error (e.g. permissions) can't spin
	// forever; a full day of per-second collisions for one entry is impossible
	// in practice.
	for i := 0; i < 86400; i++ {
		dst := filepath.Join(snaps, ts.Format(TSLayout))
		_, err := os.Stat(dst)
		if errors.Is(err, fs.ErrNotExist) {
			return dst // free
		}
		if err != nil {
			// Unexpected stat error; let the caller surface it on Create.
			return dst
		}
		ts = ts.Add(time.Second)
	}
	return filepath.Join(snaps, ts.Format(TSLayout))
}

var (
	btrfsOnce sync.Once
	btrfsOK   bool
)

// btrfsAvailable reports whether the `btrfs` command works, memoizing the probe
// for the life of the process. Whether btrfs is installed is a fixed property of
// the host, but Detect runs once per entry — and with a parallel SyncMany that
// would fire many redundant `btrfs --version` execs — so we probe at most once.
// The per-path btrfs filesystem check (statfs) in Detect still runs every time.
func btrfsAvailable(ctx context.Context, r execx.Runner) bool {
	btrfsOnce.Do(func() {
		_, err := r.Run(ctx, "btrfs", "--version")
		btrfsOK = err == nil
	})
	return btrfsOK
}

// Detect chooses btrfs when root is on a btrfs filesystem and the `btrfs`
// command is available; otherwise it returns the hardlink backend.
func Detect(ctx context.Context, root string, r execx.Runner, fsType FSTypeFunc) Backend {
	if magic, err := fsType(root); err == nil && magic == BtrfsMagic {
		if btrfsAvailable(ctx, r) {
			return NewBtrfs(r)
		}
	}
	return NewHardlink(r)
}
