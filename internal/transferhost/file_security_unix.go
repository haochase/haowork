//go:build !windows

package transferhost

import (
	"errors"
	"os"
	"syscall"
)

func validateOwnerOnly(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("owner-only file grants group or other permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("owner-only file is not owned by the current user")
	}
	return nil
}

func secureOwnerOnly(path string) error {
	return os.Chmod(path, 0o600)
}
