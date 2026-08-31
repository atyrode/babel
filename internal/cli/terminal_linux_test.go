//go:build linux

package cli

import (
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// terminalPair is a real pty, opened so that the one command that requires a
// terminal can be driven as an operator drives it. A fake that merely reports
// "yes, I am a terminal" would test the fake: the refusal this command exists
// for is decided by an ioctl, and handing a child a terminal it can put into
// raw mode is the whole behavior under test.
type terminalPair struct {
	master *os.File
	// slave is what the invocation and the worker it launches see as their
	// standard streams.
	slave *os.File
	read  chan string
}

// openTerminal returns a pty pair. Everything written to the terminal is
// drained in the background, because a pty has a small kernel buffer and a
// writer that fills it blocks — which would deadlock the invocation rather
// than fail it.
func openTerminal(t *testing.T) *terminalPair {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("this host has no usable /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlocking the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("resolving the pty: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("this host does not expose the pty slave: %v", err)
	}
	t.Cleanup(func() { slave.Close() })

	pair := &terminalPair{master: master, slave: slave, read: make(chan string, 1)}
	go func() {
		// The master reads until every slave descriptor is closed, which is
		// what collect arranges; the error is that closure, not a fault.
		data, _ := io.ReadAll(master)
		pair.read <- string(data)
	}()
	return pair
}

// collect closes the terminal and returns everything that was displayed on it.
func (p *terminalPair) collect(t *testing.T) string {
	t.Helper()
	if err := p.slave.Close(); err != nil {
		t.Fatalf("closing the terminal: %v", err)
	}
	select {
	case out := <-p.read:
		return out
	case <-time.After(30 * time.Second):
		t.Fatal("reading what was displayed on the terminal timed out")
		return ""
	}
}
