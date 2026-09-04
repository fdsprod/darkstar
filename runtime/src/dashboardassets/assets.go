// Package dashboardassets exposes the dashboard files compiled into the
// DARKSTAR binary.
package dashboardassets

import "io/fs"

// Files returns the embedded dashboard filesystem rooted at its public files.
func Files() fs.FS {
	return files()
}
