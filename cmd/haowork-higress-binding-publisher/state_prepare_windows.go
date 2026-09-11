//go:build windows

package main

import "os"

func verifyStateDirectoryOwner(_ os.FileInfo, _, _ int) error {
	return nil
}
