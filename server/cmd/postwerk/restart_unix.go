//go:build unix

package main

import (
	"os"
	"syscall"
)

// reexec replaces the process with a fresh copy (same PID, so the container
// keeps running).
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
