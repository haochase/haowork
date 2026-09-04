package transferhost

import (
	"errors"
	"os"
)

func ValidateOwnerOnlyFile(path string) error {
	if path == "" {
		return errors.New("owner-only file path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("owner-only path must be a regular non-link file")
	}
	return validateOwnerOnly(path, info)
}

func ProtectOwnerOnlyFile(path string) error {
	if path == "" {
		return errors.New("owner-only file path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("owner-only path must be a regular non-link file")
	}
	if err := secureOwnerOnly(path); err != nil {
		return err
	}
	return ValidateOwnerOnlyFile(path)
}
