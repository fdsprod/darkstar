//go:build !windows && !unix

package workspace

import (
	"errors"
	"os"
)

func renameFile(_ *os.Root, _, _ string) error {
	return errors.New("atomic workspace replacement is unsupported on this operating system")
}
