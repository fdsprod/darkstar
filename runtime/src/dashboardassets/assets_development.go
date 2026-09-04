//go:build !dashboard

package dashboardassets

import (
	"embed"
	"io/fs"
)

// The development fallback keeps ordinary Go tests and direct development
// builds independent from Node. Release builds use assets_production.go.
//
//go:embed fallback/*
var developmentFiles embed.FS

func files() fs.FS {
	assets, err := fs.Sub(developmentFiles, "fallback")
	if err != nil {
		panic(err)
	}
	return assets
}
