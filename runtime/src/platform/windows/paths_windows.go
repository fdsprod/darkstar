// Package windows implements DARKSTAR's Windows platform capabilities.
package windows

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fdsprod/darkstar/runtime/src/ports/platform"
	"golang.org/x/sys/windows"
)

// PathResolver resolves per-user application paths from Windows Known Folders.
type PathResolver struct {
	localAppData func() (string, error)
}

var _ platform.PathResolver = (*PathResolver)(nil)

// NewPathResolver constructs the production Windows path resolver.
func NewPathResolver() *PathResolver {
	return &PathResolver{localAppData: func() (string, error) {
		return windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	}}
}

// ResolvePaths returns absolute, cleaned paths beneath LocalAppData without
// creating directories or trusting a repository-controlled environment value.
func (r *PathResolver) ResolvePaths(ctx context.Context, request platform.PathRequest) (platform.Paths, error) {
	if err := ctx.Err(); err != nil {
		return platform.Paths{}, err
	}
	if err := validateApplicationName(request.ApplicationName); err != nil {
		return platform.Paths{}, err
	}
	if r == nil || r.localAppData == nil {
		return platform.Paths{}, errors.New("windows LocalAppData resolver is not configured")
	}
	knownFolder, err := r.localAppData()
	if err != nil {
		return platform.Paths{}, fmt.Errorf("resolve Windows LocalAppData known folder: %w", err)
	}
	if !filepath.IsAbs(knownFolder) {
		return platform.Paths{}, fmt.Errorf("windows LocalAppData known folder is not absolute: %q", knownFolder)
	}

	root := filepath.Clean(filepath.Join(knownFolder, request.ApplicationName))
	return platform.Paths{
		Config:  filepath.Join(root, "config"),
		Data:    filepath.Join(root, "data"),
		Cache:   filepath.Join(root, "cache"),
		Logs:    filepath.Join(root, "logs"),
		Runtime: filepath.Join(root, "runtime"),
	}, nil
}

func validateApplicationName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("application name is required")
	}
	if name != strings.TrimSpace(name) || strings.HasSuffix(name, ".") || hasWindowsControlCharacter(name) ||
		name == "." || name == ".." || isWindowsDeviceName(name) || filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.ContainsAny(name, `<>:"/\\|?*`) {
		return fmt.Errorf("application name must be one path segment: %q", name)
	}
	return nil
}

func isWindowsDeviceName(name string) bool {
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9'
}

func hasWindowsControlCharacter(value string) bool {
	for _, character := range value {
		if character < 32 {
			return true
		}
	}
	return false
}
