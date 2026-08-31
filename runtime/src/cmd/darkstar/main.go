// Command darkstar is the DARKSTAR command-line entry point and daemon host.
package main

import (
	"os"

	"github.com/fdsprod/darkstar/runtime/src/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
