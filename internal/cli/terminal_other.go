//go:build !unix

package cli

import "os"

// isTerminal reports no terminal on a platform this build has no terminal
// test for. The only command that asks refuses without one, which is the
// correct outcome here: Babel ships for linux and darwin (flake.nix), so a
// platform that reaches this file has no supported analysis path either.
func isTerminal(*os.File) bool { return false }
