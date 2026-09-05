//go:build !windows

package filesystem

import "os"

func replacePublishedFile(source, destination string) error { return os.Rename(source, destination) }
