// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 noir017

package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Store keeps one small JSON state file (plus a lock file) per entry in Dir.
//
// It lives beside the config rather than under each entry's local_path on
// purpose: `gsyncer tick` reads every entry's state once a minute, and on a NAS
// local_path is typically an array disk that would otherwise never get to spin
// down. Entries that are not due never touch their local_path at all.
type Store struct {
	Dir string
}

// fileKey maps an entry name to a safe file name. Every byte outside
// [A-Za-z0-9_-] is %-escaped — "." included, so a name like ".." can never
// become a path component — which keeps the mapping injective.
func fileKey(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (s Store) path(name, ext string) string {
	return filepath.Join(s.Dir, fileKey(name)+ext)
}

// Load returns the entry's state; a missing file is the zero State (a new
// entry), not an error.
func (s Store) Load(name string) (State, error) {
	b, err := os.ReadFile(s.path(name, ".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("schedule state for %q is corrupt: %w", name, err)
	}
	return st, nil
}

// Save writes the state atomically (temp file + rename), so a crash mid-write
// leaves the previous state rather than a truncated file.
func (s Store) Save(name string, st State) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path(name, ".json"))
}

// Lock is a held per-entry scheduling lock.
type Lock struct{ f *os.File }

// Lock takes a non-blocking exclusive flock on the entry's lock file. It returns
// (lock, true, nil) when acquired and (nil, false, nil) when another process
// holds it — e.g. the previous minute's tick is still syncing this entry.
//
// The lock is a separate file, not the state file itself: Save replaces the
// state file by rename, and a flock on a replaced inode protects nothing.
func (s Store) Lock(name string) (*Lock, bool, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(s.path(name, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &Lock{f: f}, true, nil
}

// Release drops the lock. Safe on a nil lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
