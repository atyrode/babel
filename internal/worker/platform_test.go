package worker

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// qualifiedDeclaration is the declaration a backend that has passed its escape
// scenario makes: every property the strict requirement asks for, plus a named
// mechanism and a stated residual risk.
func qualifiedDeclaration() Containment {
	return Containment{
		Backend:             "bwrap+systemd-scope",
		FilesystemIsolation: true,
		NetworkDefaultDeny:  true,
		ResourceCeilings:    true,
		Disposable:          true,
		Escape: "egress reaches the resolved provider endpoint through a host-side " +
			"CONNECT proxy; the provider credential is inside the sandbox; isolation " +
			"rests on unprivileged user namespaces; no seccomp filter is applied",
	}
}

// TestQualifiedPlatformAcceptsAFullDeclaration is the case that must keep
// working: on a platform with a backend that has passed its escape scenario, a
// declaration meeting the strict requirement launches. The §10 gate is a limit
// on unqualified platforms, and a gate that also stopped the qualified one would
// have disabled the product rather than bounded it.
func TestQualifiedPlatformAcceptsAFullDeclaration(t *testing.T) {
	if err := qualifiedDeclaration().satisfiesOn(SandboxedRun(), "linux"); err != nil {
		t.Fatalf("a qualified linux declaration was refused: %v", err)
	}
}

// TestUnqualifiedPlatformIsRefusedForTheStatedReason is §10 itself. On a
// platform where no backend has passed, no declaration is believable: the
// refusal has to hold whatever the worker claims, and it has to be attributable
// to the platform rather than to the worker, because that is the difference
// between a stated limit and an executable to debug.
func TestUnqualifiedPlatformIsRefusedForTheStatedReason(t *testing.T) {
	tests := []struct {
		name    string
		declare Containment
	}{
		{
			// What an honest worker on an unqualified platform says: it ran the
			// process, it contains nothing, and it says so.
			name: "honest declaration claiming nothing",
			declare: Containment{
				Backend: "process",
				Escape:  "no filesystem, network, resource or lifetime boundary is applied",
			},
		},
		{
			// What a mistaken or dishonest worker says. Babel cannot inspect the
			// mechanism, so the claim is exactly what §10 declines to take on
			// faith: an unqualified platform must refuse this too, or the gate
			// is worth nothing.
			name: "declaration claiming every property",
			declare: func() Containment {
				c := qualifiedDeclaration()
				c.Backend = "sandbox-exec"
				return c
			}(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.declare.satisfiesOn(SandboxedRun(), "darwin")
			if err == nil {
				t.Fatal("an unqualified platform launched the run")
			}
			if !errors.Is(err, ErrPlatformUnqualified) {
				t.Errorf("refusal does not carry the platform reason: %v", err)
			}
			// The platform case is one way containment is insufficient, so a
			// caller that only matches the general failure must still see it.
			if !errors.Is(err, ErrContainment) {
				t.Errorf("refusal is invisible to errors.Is(err, ErrContainment): %v", err)
			}
			if !strings.Contains(err.Error(), "darwin") {
				t.Errorf("refusal does not name the platform it applies to: %v", err)
			}
			// The operator-facing surface renders the platform from the
			// refusal rather than from its own runtime, so the refusal has
			// to be reachable as a PlatformRefusal through the wrapping a
			// caller adds.
			var refusal PlatformRefusal
			if !errors.As(fmt.Errorf("explore: explore job: %w", err), &refusal) {
				t.Fatalf("refusal is not reachable as a PlatformRefusal: %v", err)
			}
			if got := refusal.UnqualifiedPlatform(); got != "darwin" {
				t.Errorf("refusal reports platform %q, want darwin", got)
			}
		})
	}
}

// TestUnqualifiedPlatformRefusalDoesNotBlameTheDeclaration guards the
// information the two refusals carry. Telling an operator on an unqualified
// platform which properties are missing would name a remedy that does not
// exist: there is no worker change that qualifies the platform.
func TestUnqualifiedPlatformRefusalDoesNotBlameTheDeclaration(t *testing.T) {
	err := Containment{
		Backend: "process",
		Escape:  "nothing is contained",
	}.satisfiesOn(SandboxedRun(), "darwin")
	if err == nil {
		t.Fatal("an unqualified platform launched the run")
	}
	for _, property := range []string{
		"filesystem isolation", "network default-deny",
		"resource ceilings", "disposable environment",
	} {
		if strings.Contains(err.Error(), property) {
			t.Errorf("the platform refusal offers %q as a remedy, which no worker change delivers: %v",
				property, err)
		}
	}
}

// TestShortDeclarationOnAQualifiedPlatformKeepsThePropertyDiagnostic is the
// other half of the distinction. Here the platform is fine and the worker is
// not, the remedy is in the worker, and collapsing this into the platform
// message would both lose the property list and tell the operator a falsehood
// about their machine.
func TestShortDeclarationOnAQualifiedPlatformKeepsThePropertyDiagnostic(t *testing.T) {
	short := qualifiedDeclaration()
	short.ResourceCeilings = false
	short.Disposable = false

	err := short.satisfiesOn(SandboxedRun(), "linux")
	if err == nil {
		t.Fatal("a declaration short of the strict requirement launched")
	}
	if !errors.Is(err, ErrContainment) {
		t.Fatalf("refusal = %v, want ErrContainment", err)
	}
	if errors.Is(err, ErrPlatformUnqualified) {
		t.Errorf("a worker that declares too little was reported as a platform limit: %v", err)
	}
	for _, property := range []string{"resource ceilings", "disposable environment"} {
		if !strings.Contains(err.Error(), property) {
			t.Errorf("refusal does not name the missing %q: %v", property, err)
		}
	}
	if strings.Contains(err.Error(), "filesystem isolation") {
		t.Errorf("refusal names a property the worker did declare: %v", err)
	}
}

// TestRelaxedRunIsNotSubjectToThePlatformGate keeps the gate scoped to what it
// is about. A run that demands no boundary — the configuration-only probe, where
// nothing executes, or a run the operator relaxed — is not relying on a sandbox,
// so refusing it for the platform's sake would disable `babel analysis profile
// configure` everywhere Babel does not explore. The named-backend rule still
// applies, because an unnamed mechanism cannot be assessed at all.
func TestRelaxedRunIsNotSubjectToThePlatformGate(t *testing.T) {
	declared := Containment{
		Backend: "process",
		Escape:  "nothing is contained",
	}
	if err := declared.satisfiesOn(Unsandboxed(), "darwin"); err != nil {
		t.Errorf("a run demanding no containment was refused on an unqualified platform: %v", err)
	}
	if err := (Containment{Escape: "nothing"}).satisfiesOn(Unsandboxed(), "darwin"); err == nil {
		t.Error("an unnamed backend was accepted; relaxing the requirement does not waive naming")
	}
}
