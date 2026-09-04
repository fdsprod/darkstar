//go:build dashboard

package dashboardassets

import (
	"embed"
	"io/fs"
)

// dist is populated from dashboard/dist by scripts/Build.ps1 before the Go
// compiler runs. Its contents become part of the standalone executable.
//
//go:embed dist
var productionFiles embed.FS

func files() fs.FS {
	assets, err := fs.Sub(productionFiles, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
