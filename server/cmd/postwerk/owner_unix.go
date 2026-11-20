//go:build unix

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// chownLike gives everything below root the owner of ref. CLI commands run
// via "docker exec" as root, while the server runs as PUID:PGID; files they
// create must stay readable for the server.
func chownLike(root, ref string) {
	if os.Geteuid() != 0 {
		return
	}
	info, err := os.Stat(ref)
	if err != nil {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	uid, gid := int(st.Uid), int(st.Gid)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Lchown(p, uid, gid)
		}
		return nil
	})
}
