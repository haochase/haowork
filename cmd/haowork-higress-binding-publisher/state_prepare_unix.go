//go:build !windows

package main

import (
	"os"
	"syscall"
)

func verifyStateDirectoryOwner(info os.FileInfo, uid, gid int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return errInvalidStatePreparation
	}
	return nil
}
