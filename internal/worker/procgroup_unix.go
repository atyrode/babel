//go:build unix

package worker

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the worker the leader of its own process group.
// Everything it spawns inherits that group, which is what lets cancellation
// reach the disposable execution sandbox and not merely the process Babel
// launched (SPEC.md §2.6: "cancellation terminates the entire process tree").
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateTree signals the worker's whole process group: SIGTERM when
// graceful, SIGKILL otherwise. A negative pid addresses the group.
//
// The failure path matters. If the group signal fails — the group is already
// empty, or the child was started without its own group — the direct child is
// signalled instead, so a cancellation never silently does nothing.
func terminateTree(cmd *exec.Cmd, pgid int, graceful bool) error {
	signal := syscall.SIGKILL
	if graceful {
		signal = syscall.SIGTERM
	}
	if pgid > 0 {
		if err := syscall.Kill(-pgid, signal); err == nil {
			return nil
		}
	}
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(signal)
}
