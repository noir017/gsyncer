package syncer

import (
	"os"
	"path/filepath"
)

// fileLock is an advisory, per-root lock held for the duration of a sync so
// that two overlapping runs (e.g. a slow cron tick still running when the next
// fires) can't both rsync --delete into the same current/ and corrupt the tree
// that then gets snapshotted.
//
// acquireLock / release are implemented per platform: Unix uses flock(2),
// Windows uses LockFileEx (see lock_unix.go / lock_windows.go).
type fileLock struct{ f *os.File }

// openLockFile creates root (if needed) and opens <root>/.gsyncer.lock for
// read/write. Shared by all platform implementations of acquireLock.
func openLockFile(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(root, ".gsyncer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
}
