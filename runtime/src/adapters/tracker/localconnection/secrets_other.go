//go:build !windows

package localconnection

import "os"

func protectSecret(value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

func unprotectSecret(value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

func protectStoredFile(file *os.File) error {
	return file.Chmod(0o600)
}

func verifyStoredFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return os.ErrPermission
	}
	return nil
}

func replacePublishedFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncStorageDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
