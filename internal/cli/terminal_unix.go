//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal reports whether f is a terminal.
//
// The question is asked with an ioctl only a terminal answers rather than
// from the file's mode, because /dev/null is a character device too: a
// mode-based check calls a redirected stream a terminal, and the one command
// that needs this answer would then hand Code's interactive configuration to
// something nobody is watching.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}
