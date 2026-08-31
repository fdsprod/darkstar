// Package cli implements the runtime's human- and automation-facing command-line boundary.
package cli

import (
	"fmt"
	"io"
)

const usage = `DARKSTAR

Usage:
  darkstar [command]

Commands:
  help       Show this help
  version    Show version information
`

// Version is replaced by release builds through -ldflags.
var Version = "dev"

// Run executes the command line and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(stdout, "darkstar %s\n", Version)
		return 0
	}

	fmt.Fprintf(stderr, "darkstar: unknown command %q\n", args[0])
	fmt.Fprintln(stderr, "Run 'darkstar help' for usage.")
	return 2
}
