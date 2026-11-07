//go:build windows

package metrics

import "golang.org/x/sys/windows"

// diskUsage returns the bytes available to the caller and the total size of the volume
// holding path.
func diskUsage(path string) (avail, total uint64, ok bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var free, size, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &size, &totalFree) != nil {
		return 0, 0, false
	}
	return free, size, true
}
