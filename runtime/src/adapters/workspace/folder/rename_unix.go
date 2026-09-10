//go:build unix

package workspace

import (
	"golang.org/x/sys/unix"
	"os"
)

func renameFile(root *os.Root, oldName, newName string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return unix.Renameat(int(directory.Fd()), oldName, int(directory.Fd()), newName)
}
