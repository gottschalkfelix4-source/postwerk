//go:build !(linux || darwin || freebsd || netbsd || openbsd || windows)

package backup

func freeDiskSpace(string) (uint64, bool) { return 0, false }
