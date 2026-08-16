//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || zos

package changes

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func readFileNoFollow(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return nil, errors.New("secure read requires an absolute path")
	}
	directoryFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(cleanPath, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(components)-1 {
			flags |= unix.O_DIRECTORY
		}
		fileFD, openErr := unix.Openat(directoryFD, component, flags, 0)
		closeErr := unix.Close(directoryFD)
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) {
				return nil, errRefusingSymbolicLink
			}
			return nil, openErr
		}
		if closeErr != nil {
			_ = unix.Close(fileFD)
			return nil, closeErr
		}
		directoryFD = fileFD
	}
	file := os.NewFile(uintptr(directoryFD), path)
	if file == nil {
		_ = unix.Close(directoryFD)
		return nil, errors.New("open secure file descriptor")
	}
	defer file.Close()
	return io.ReadAll(file)
}
