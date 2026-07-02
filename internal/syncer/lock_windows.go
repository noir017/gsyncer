//go:build windows

package syncer

import "golang.org/x/sys/windows"

// acquireLock takes a non-blocking exclusive lock on <root>/.gsyncer.lock using
// LockFileEx. It returns (lock, true, nil) when the lock is held,
// (nil, false, nil) when another process already holds it, or
// (nil, false, err) on an unexpected error. The lock is released when the
// underlying handle is closed.
func acquireLock(root string) (*fileLock, bool, error) {
	f, err := openLockFile(root)
	if err != nil {
		return nil, false, err
	}
	// EXCLUSIVE | FAIL_IMMEDIATELY == non-blocking exclusive lock.
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
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
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol)
	_ = l.f.Close()
}
