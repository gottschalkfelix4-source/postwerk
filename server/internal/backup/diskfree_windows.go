//go:build windows

package backup

import "golang.org/x/sys/windows"

// freeDiskSpace reports the bytes available to the current user at dir.
func freeDiskSpace(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, false
	}
	return free, true
}
