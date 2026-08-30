package cli

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/worker"
)

// unqualifiedPlatform stands in for the refusal internal/worker produces when
// the host platform has no backend that has passed its escape scenario. The real
// one is only constructible on such a host, and the platform under test must not
// be whichever machine runs the suite — that is exactly the coupling this test
// exists to keep out of the report.
type unqualifiedPlatform struct{ goos string }

func (p unqualifiedPlatform) UnqualifiedPlatform() string { return p.goos }

func (p unqualifiedPlatform) Error() string {
	return fmt.Sprintf("%s: exploration is refused on %s because no backend has passed its escape scenario there; the worker declared backend %q",
		worker.ErrPlatformUnqualified, p.goos, "process")
}

func (p unqualifiedPlatform) Is(target error) bool {
	return target == worker.ErrContainment || target == worker.ErrPlatformUnqualified
}

// reportedFailure runs the operator-facing failure report for err and returns
// what an operator would read on stderr.
func reportedFailure(t *testing.T, err error) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	if got := a.reportWorkerFailure("/opt/code/code", err); !errors.Is(got, errReported) {
		t.Fatalf("reportWorkerFailure returned %v, want errReported", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("a refusal wrote to stdout, which carries machine-readable output: %q", stdout.String())
	}
	return stderr.String()
}

// TestPlatformRefusalReachesTheOperator is the wiring that makes §10's limit
// legible. `babel explore` reports a failed run through reportWorkerFailure, so
// unless the platform case is routed there the operator reads a heading that
// blames the worker and a remedy that does not exist.
//
// The assertions are about which account the operator gets, not its wording: the
// platform report must attribute the refusal to the platform the refusal names,
// must not present the worker as the thing that failed, and must say what the
// machine still does.
func TestPlatformRefusalReachesTheOperator(t *testing.T) {
	// The shape internal/explore hands up: the stage wraps the worker's error,
	// so the report has to see through the wrapping. The platform is a name no
	// host can have, so any appearance of the running machine's GOOS below is
	// the renderer having consulted itself instead of the refusal.
	const refused = "unqualified-test-platform"
	err := fmt.Errorf("explore: explore job: %w", unqualifiedPlatform{goos: refused})
	out := reportedFailure(t, err)

	if !strings.Contains(out, refused) {
		t.Errorf("the refusal does not name the platform it applies to:\n%s", out)
	}
	if strings.Contains(out, runtime.GOOS) {
		t.Errorf("the report named the machine it is running on (%s) rather than the platform the refusal is about:\n%s",
			runtime.GOOS, out)
	}
	if strings.Contains(out, "the Code analysis worker could not run") {
		t.Errorf("a platform limit was reported as a worker fault:\n%s", out)
	}
	// Refusing exploration is not refusing Babel; an operator who cannot
	// explore here still needs to know the archive works.
	for _, works := range []string{"archive", "review", "sessions", "web"} {
		if !strings.Contains(out, works) {
			t.Errorf("the refusal does not say that %s still works here:\n%s", works, out)
		}
	}
	if !strings.Contains(out, "exploration is refused") {
		t.Errorf("the refusal does not say that exploration is what is refused:\n%s", out)
	}
}

// TestWorkerShortfallIsNotReportedAsAPlatformLimit is the other half. A worker
// that declares too little on a qualified platform has a remedy in the worker,
// and reporting it as a platform limit would tell the operator their machine
// cannot explore at all — false, and it would hide the property list that names
// the actual fix.
func TestWorkerShortfallIsNotReportedAsAPlatformLimit(t *testing.T) {
	err := fmt.Errorf("explore: explore job: %w: backend %q does not provide resource ceilings",
		worker.ErrContainment, "bwrap+systemd-scope")
	out := reportedFailure(t, err)

	if !strings.Contains(out, "resource ceilings") {
		t.Errorf("the property-level diagnostic was lost:\n%s", out)
	}
	if !strings.Contains(out, "the Code analysis worker could not run") {
		t.Errorf("a worker shortfall was not attributed to the worker:\n%s", out)
	}
	if strings.Contains(out, "no qualified") || strings.Contains(out, "has passed its escape scenario") {
		t.Errorf("a fixable worker was reported as an unqualified platform:\n%s", out)
	}
}
