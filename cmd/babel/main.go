// Command babel is Babel's operator entry point (SPEC.md §8). All behavior
// lives in internal/cli so the whole command surface is drivable in-process
// by tests; main only wires the process's streams and exit code.
package main

import (
	"os"

	"github.com/atyrode/babel/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
