//go:build windows

package changes

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readFileNoFollow(path string) ([]byte, error) {
	objectName, err := windows.NewNTUnicodeString(ntObjectPath(path))
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:     uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	// OBJ_DONT_REPARSE rejects every reparse point encountered during name lookup.
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return nil, mapWindowsOpenError(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("open secure file handle")
	}
	defer file.Close()
	return io.ReadAll(file)
}

func mapWindowsOpenError(err error) error {
	if errors.Is(err, windows.STATUS_REPARSE_POINT_ENCOUNTERED) {
		return fmt.Errorf("%w or reparse point", errRefusingSymbolicLink)
	}
	return err
}

func ntObjectPath(path string) string {
	switch {
	case strings.HasPrefix(path, `\\?\UNC\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\?\UNC\`)
	case strings.HasPrefix(path, `\\?\`):
		return `\??\` + strings.TrimPrefix(path, `\\?\`)
	case strings.HasPrefix(path, `\\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	default:
		return `\??\` + path
	}
}
