//go:build !linux

package snapshot

// RealFSType is the non-Linux fallback. btrfs snapshots are Linux-only, so we
// report a non-btrfs filesystem (magic 0); Detect then falls back to the
// hardlink backend.
func RealFSType(path string) (int64, error) {
	return 0, nil
}
