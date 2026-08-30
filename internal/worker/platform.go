package worker

import "fmt"

// Sandbox backends are Code's to build and Code's to name — Containment.Backend
// is deliberately free-form, because the set grows on the other side of the
// boundary — so Babel keeps no register of them. What Babel does keep is the
// single answer SPEC.md §10 gates analysis on: whether any backend has been
// driven through its escape scenario on this platform at all.
//
// Keeping it as a per-platform answer rather than a list of backend names is
// the point. A name list would couple a Babel release to Code's vocabulary,
// would refuse a genuinely qualified backend that arrived under a name nobody
// registered here, and would still be a claim about a mechanism Babel cannot
// inspect. Whether a platform has a passed backend, by contrast, is a fact
// about Babel's own release, established by work in the repository.
//
// The answer decides which of two different refusals an operator gets, and they
// need different work. On a platform with a qualified backend, a declaration
// short of the run's requirement is a worker to fix, and naming the missing
// properties is the remedy. On a platform with none, no declaration is
// believable and no worker change lifts the limit: analysis is disabled there
// by design, and reporting "backend X does not provide resource ceilings" would
// send the operator to repair something that is not the problem.
func platformQualified(goos string) bool {
	switch goos {
	case "linux":
		// bwrap inside a transient `systemd-run --user --scope`: unprivileged
		// user namespaces supply the filesystem boundary, the absence of any
		// interface but its own loopback, and the disposability; the scope's
		// cgroup v2 controllers supply the ceilings.
		return true
	default:
		// No other platform has a backend that has passed. §10 requires
		// analysis to be disabled wherever that is true rather than run behind
		// an unexamined boundary, so refusal is the default and a platform
		// joins the case above by passing the scenario, not by being added.
		return false
	}
}

// PlatformRefusal is the shape of §10's refusal, exported so an operator-facing
// surface can render it without asking a second source for the fact the refusal
// already carries. The platform is decided once, where the gate is applied; a
// renderer that re-derived it from its own runtime would be a second place for
// the answer to be wrong.
type PlatformRefusal interface {
	error
	// UnqualifiedPlatform names the GOOS the refusal applies to.
	UnqualifiedPlatform() string
}

// platformRefusal is §10's refusal: the host platform has no backend that has
// passed its escape scenario, so exploration is refused there whatever the
// worker declares about itself.
//
// It matches ErrContainment as well as ErrPlatformUnqualified. A caller that
// only needs to know the boundary was insufficient keeps working unchanged,
// while the operator-facing surface can recognize this case specifically and
// explain a stated platform limit instead of a broken worker.
type platformRefusal struct {
	goos    string
	backend string
}

func (e *platformRefusal) UnqualifiedPlatform() string { return e.goos }

var _ PlatformRefusal = (*platformRefusal)(nil)

func (e *platformRefusal) Error() string {
	return fmt.Sprintf("%s: exploration is refused on %s because no backend has passed its escape scenario there; the worker declared backend %q",
		ErrPlatformUnqualified, e.goos, e.backend)
}

func (e *platformRefusal) Is(target error) bool {
	return target == ErrContainment || target == ErrPlatformUnqualified
}
