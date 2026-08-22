//go:build windows

package scm

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func canonicalPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	path, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		path,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	size := uint32(512)
	for {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], size, 0)
		if err != nil {
			return "", err
		}
		if length < size {
			return trimWindowsPathPrefix(windows.UTF16ToString(buffer[:length])), nil
		}
		size = length + 1
	}
}

func trimWindowsPathPrefix(value string) string {
	if strings.HasPrefix(value, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(value, `\\?\UNC\`)
	}
	return strings.TrimPrefix(value, `\\?\`)
}
