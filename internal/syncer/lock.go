package syncer

import (
	"os"
	"path/filepath"
	"syscall"
)

// fileLock is an advisory, per-root lock held for the duration of a sync so
// that two overlapping runs (e.g. a slow cron tick still running when the next
// fires) can't both rsync --delete into the same current/ and corrupt the tree
// that then gets snapshotted.
type fileLock struct{ f *os.File }

// acquireLock takes a non-blocking exclusive flock on <root>/.gsyncer.lock.
// It returns (lock, true, nil) when the lock is held, (nil, false, nil) when
// another process already holds it, or (nil, false, err) on an unexpected error.
func acquireLock(root string) (*fileLock, bool, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".gsyncer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil // held by another run
		}
		return nil, false, err
	}
	return &fileLock{f: f}, true, nil
}

// release drops the lock and closes the underlying file. Safe on a nil lock.
func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
