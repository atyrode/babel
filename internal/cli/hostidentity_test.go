package cli

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// The display name a push asserts is decision 8's payload, and what it must
// never be matters as much as what it is.
//
// It is never the system hostname. reconcile.go refuses to adopt snapshots
// recorded under one, because a hostname is infrastructure identity rather than
// the operator-assigned identity the catalog keys on, and the plaintext decision
// of 2026-08-30 did not cover it. A fallback to os.Hostname() here would put it
// in the catalog by the back door.
func TestHostIdentityNeverReportsTheSystemHostname(t *testing.T) {
	t.Setenv("BABEL_HOST_DISPLAY_NAME", "")
	system, err := os.Hostname()
	if err != nil || system == "" {
		t.Skip("this machine reports no system hostname to check against")
	}
	// The host id a push publishes under is deliberately different from the
	// system hostname, which is the arrangement on the operator's machine.
	got := hostIdentity("operator-chosen-host")
	if got.DisplayName == system {
		t.Fatalf("display name = %q, the system hostname; infrastructure identity "+
			"must not reach the shared catalog", got.DisplayName)
	}
	if got.DisplayName != "operator-chosen-host" {
		t.Errorf("display name = %q, want the host id this push publishes under",
			got.DisplayName)
	}
}

// A column that only ever holds NULL is not an improvement, so an unconfigured
// machine still asserts something true: the host id, which is already the
// primary key of the row being written.
func TestHostIdentityDefaultsToTheHostID(t *testing.T) {
	t.Setenv("BABEL_HOST_DISPLAY_NAME", "")
	got := hostIdentity("workstation-linux")
	if got.DisplayName != "workstation-linux" {
		t.Errorf("display name = %q, want the host id", got.DisplayName)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("machine facts = %q/%q, want this binary's platform %q/%q",
			got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
}

// The operator's chosen name wins over the default, which is the whole point of
// having a display name distinct from the id.
func TestHostIdentityHonoursTheOperatorsName(t *testing.T) {
	t.Setenv("BABEL_HOST_DISPLAY_NAME", "  Alex's workstation  ")
	got := hostIdentity("workstation-linux")
	if got.DisplayName != "Alex's workstation" {
		t.Errorf("display name = %q, want the trimmed environment value", got.DisplayName)
	}
}

// Truncation is by rune. Cutting a multi-byte sequence in half would produce
// invalid UTF-8, which PostgreSQL's text type rejects outright - so a long name
// would fail the whole push rather than being shortened.
func TestHostIdentityTruncatesByRune(t *testing.T) {
	long := strings.Repeat("é", maxHostDisplayNameLen+10)
	t.Setenv("BABEL_HOST_DISPLAY_NAME", long)
	got := hostIdentity("workstation-linux")
	if n := len([]rune(got.DisplayName)); n != maxHostDisplayNameLen {
		t.Fatalf("display name is %d runes, want %d", n, maxHostDisplayNameLen)
	}
	if !isValidUTF8(got.DisplayName) {
		t.Errorf("truncation produced invalid UTF-8: %q", got.DisplayName)
	}
}

// isValidUTF8 reports whether every rune decoded cleanly. A byte-sliced string
// decodes as U+FFFD at the seam, which is exactly the failure to catch.
func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
