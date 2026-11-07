//go:build !(linux || darwin || freebsd || netbsd || openbsd || windows)

package metrics

// diskUsage is not supported on this platform.
func diskUsage(string) (avail, total uint64, ok bool) { return 0, 0, false }
