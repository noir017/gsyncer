// Package snapshot abstracts snapshot creation over hardlink and btrfs backends.
package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
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

// Detect (backend selection) is added in Task 9, once both backends exist; it
// is the only thing in this package that imports execx.
