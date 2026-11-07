//go:build linux || darwin || freebsd || netbsd || openbsd

package metrics

import "golang.org/x/sys/unix"

// diskUsage returns the bytes available to unprivileged users and the total size of
// the file system holding path.
func diskUsage(path string) (avail, total uint64, ok bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize) //nolint:gosec // block size is never negative
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, true
}
