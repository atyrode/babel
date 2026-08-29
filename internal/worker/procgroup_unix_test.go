//go:build unix

package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCancellationReapsTheWholeProcessTree is the guarantee SPEC.md §2.6 makes
// about analysis: "cancellation terminates the entire process tree", and
// analysis is never detached.
//
// The fixture spawns a grandchild that sleeps for ten minutes and inherits the
// protocol pipe, which is exactly what a disposable execution sandbox looks
// like from Babel's side. A supervisor that killed only its direct child would
// leave that process running and would never see stdout reach EOF, so this
// test fails in two distinguishable ways if the process-group handling
// regresses: the grandchild survives, or Run hangs.
func TestCancellationReapsTheWholeProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-grandchild", pidFile}
	f.limits.TerminateGrace = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The worker publishes its grandchild's pid before its first progress
	// event, so cancelling here always has a tree to tear down.
	f.progress = func(ProgressRecord) { cancel() }

	started := time.Now()
	receipt, err := f.client(t).Run(ctx, f.job)
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if receipt == nil {
		t.Fatal("no receipt after cancellation; a cancelled run still has an audit record")
	}
	if elapsed > 30*time.Second {
		t.Fatalf("Run took %s to return; the worker sleeps ten minutes, so it was waited on", elapsed)
	}

	pid := readPID(t, pidFile)
	// Non-vacuity: the grandchild really existed and really was a separate
	// process, otherwise the reaping check below would pass for nothing.
	if pid <= 0 || pid == os.Getpid() {
		t.Fatalf("grandchild pid = %d, which is not a separate process", pid)
	}

	// The grandchild is not Babel's child, so init reaps it once its parent
	// dies. Poll, bounded: it has been signalled, it still has to be scheduled.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d is still alive after the run was cancelled and reaped", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestCancellationEscalatesPastAnIgnoredSIGTERM: a worker may ignore the
// polite signal. The tree still goes down, because SIGKILL cannot be ignored
// and Babel escalates after the terminate grace.
func TestCancellationEscalatesPastAnIgnoredSIGTERM(t *testing.T) {
	f := newFixture(ConformanceWellBehaved)
	f.args = []string{"-ignore-terminate"}
	f.limits.TerminateGrace = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.progress = func(ProgressRecord) { cancel() }

	started := time.Now()
	receipt, err := f.client(t).Run(ctx, f.job)
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("Run took %s; SIGTERM was ignored and the escalation did not happen", elapsed)
	}
	if receipt.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1 for a signalled worker", receipt.ExitCode)
	}
}

// readPID waits, bounded, for the fixture's pid file and parses it.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		contents, err := os.ReadFile(path)
		if err == nil && len(contents) > 0 {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(contents)))
			if convErr != nil {
				t.Fatalf("parsing %q: %v", contents, convErr)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fixture never published a grandchild pid at %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
