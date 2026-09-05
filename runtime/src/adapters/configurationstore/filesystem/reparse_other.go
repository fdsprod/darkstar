//go:build !windows

package filesystem

import "os"

func isReparsePoint(os.FileInfo) bool { return false }
