//go:build !windows

package syncer

import "syscall"

// acquireLock takes a non-blocking exclusive flock on <root>/.gsyncer.lock.
// It returns (lock, true, nil) when the lock is held, (nil, false, nil) when
// another process already holds it, or (nil, false, err) on an unexpected error.
func acquireLock(root string) (*fileLock, bool, error) {
	f, err := openLockFile(root)
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
