//go:build linux

package snapshot

import "syscall"

// RealFSType returns the statfs f_type for path (Linux).
func RealFSType(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}
