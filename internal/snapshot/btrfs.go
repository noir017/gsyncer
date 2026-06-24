package snapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gsync/internal/execx"
)

// ErrCurrentNotSubvolume means root/current exists but is not a btrfs subvolume,
// so native snapshots are impossible and the caller should fall back.
var ErrCurrentNotSubvolume = errors.New("current exists but is not a btrfs subvolume")

// Btrfs implements Backend using native read-only subvolume snapshots.
type Btrfs struct {
	r execx.Runner
}

// NewBtrfs returns a btrfs-backed Backend.
func NewBtrfs(r execx.Runner) Backend { return &Btrfs{r: r} }

// Name implements Backend.
func (*Btrfs) Name() string { return "btrfs" }

// EnsureCurrent creates root/current as a subvolume, or verifies an existing one.
func (b *Btrfs) EnsureCurrent(ctx context.Context, root string) (string, error) {
	cur := filepath.Join(root, "current")
	_, statErr := os.Stat(cur)
	if errors.Is(statErr, fs.ErrNotExist) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
		if _, err := b.r.Run(ctx, "btrfs", "subvolume", "create", cur); err != nil {
			return "", err
		}
		return cur, nil
	}
	if statErr != nil {
		return "", statErr
	}
	if _, err := b.r.Run(ctx, "btrfs", "subvolume", "show", cur); err != nil {
		return "", ErrCurrentNotSubvolume
	}
	return cur, nil
}

// Create makes a read-only subvolume snapshot.
func (b *Btrfs) Create(ctx context.Context, root string, ts time.Time) (string, error) {
	snaps := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snaps, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(snaps, ts.Format(TSLayout))
	cur := filepath.Join(root, "current")
	if _, err := b.r.Run(ctx, "btrfs", "subvolume", "snapshot", "-r", cur, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Delete removes a snapshot subvolume.
func (b *Btrfs) Delete(ctx context.Context, snapPath string) error {
	_, err := b.r.Run(ctx, "btrfs", "subvolume", "delete", snapPath)
	return err
}

// List implements Backend.
func (b *Btrfs) List(root string) ([]time.Time, error) { return List(root) }
