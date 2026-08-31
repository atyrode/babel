//go:build !linux

package cli

import (
	"os"
	"testing"
)

// terminalPair stands in on a platform this suite has no pty helper for. The
// cases that need a real terminal skip rather than pass: a ceremony that was
// never driven is not evidence that it works.
type terminalPair struct {
	slave *os.File
}

func openTerminal(t *testing.T) *terminalPair {
	t.Helper()
	t.Skip("the terminal-handing cases need a pty; this suite only opens one on linux")
	return nil
}

func (p *terminalPair) collect(t *testing.T) string {
	t.Helper()
	t.Fatal("no terminal was opened")
	return ""
}
