//go:build !unix

package worker

import "os/exec"

// setProcessGroup is a no-op off Unix.
//
// Babel's release platforms are Linux and Darwin (SPEC.md §12), both Unix.
// This file exists so the package still compiles elsewhere, and it deliberately
// does not pretend to offer the process-tree guarantee: without a process
// group there is nothing portable to signal but the direct child.
func setProcessGroup(*exec.Cmd) {}

// terminateTree kills the direct child only. Anything the worker spawned
// survives, which is why this platform is unsupported rather than degraded
// quietly: a caller relying on the tree guarantee must run on Linux or Darwin.
func terminateTree(cmd *exec.Cmd, _ int, _ bool) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
