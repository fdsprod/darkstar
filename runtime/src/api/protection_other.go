//go:build !windows

package api

import (
	"fmt"
	"os"
)

func protectFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return verifyProtectedFile(path)
}

func verifyProtectedFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		return fmt.Errorf("permissions are %o, want 600", permissions)
	}
	return nil
}
