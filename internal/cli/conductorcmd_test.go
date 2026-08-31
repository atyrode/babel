package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
)

// These cases drive issue #96's loop through the shipped command surface: the
// real settings document, the real journal, the real stores. What they check is
// the wiring and the truthfulness of the report — a scheduler that works and a
// status view that disagrees with it is the failure mode a package test cannot
// see, because the whole point of the loop is that an operator can read it.

// A conductor with no ceilings will not run, and says what to do about it.
// Autonomy is budget-bounded rather than trust-bounded, so this refusal is the
// design and has to read as one.
func TestConductorRunRefusesWithoutCeilings(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.mustExit(exitFailure, "conductor", "run", "--once")
	if stdout != "" {
		t.Errorf("a refused conductor wrote to stdout: %q", stdout)
	}
	for _, want := range []string{"budget ceilings", "babel conductor configure", "babel explore"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, stderr)
		}
	}
}

// configure stores both ceilings or refuses. Neither has a default, because a
// default ceiling is a limit nobody chose.
func TestConductorConfigureRequiresBothCeilings(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"neither", []string{"conductor", "configure"}},
		{"only per-cycle", []string{"conductor", "configure", "--per-cycle", "0.5"}},
		{"only per-day", []string{"conductor", "configure", "--per-day", "5"}},
		{"a per-cycle ceiling above the day", []string{"conductor", "configure",
			"--per-cycle", "9", "--per-day", "5"}},
		{"a negative floor", []string{"conductor", "configure",
			"--per-cycle", "1", "--per-day", "5", "--floor", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr := f.mustExit(exitUsage, tc.args...)
			if stderr == "" {
				t.Error("a rejected configuration explained nothing")
			}
		})
	}

	// Nothing above stored anything: a refused configuration leaves the
	// machine unconfigured rather than half-configured.
	settings, err := loadConductorSettings()
	if err != nil {
		t.Fatalf("loadConductorSettings: %v", err)
	}
	if settings.Ceilings != nil {
		t.Fatalf("a refused configuration stored %+v", settings.Ceilings)
	}

	stdout, stderr := f.ok("conductor", "configure", "--per-cycle", "0.50", "--per-day", "5.00",
		"--floor", "3", "--interval", "30m", "--slice-sessions", "5", "--json")
	stored := decodeJSON[conductorConfigResult](t, stdout)
	assertNoRawControls(t, "conductor configure --json", stdout, stderr)
	if stored.PerCycle != 0.50 || stored.PerDay != 5.00 || stored.Currency != "USD" {
		t.Errorf("stored ceilings = %+v", stored)
	}
	if stored.Floor != 3 || stored.IntervalSeconds != 1800 || stored.SliceSessions != 5 {
		t.Errorf("stored dials = %+v", stored)
	}

	// A second configuration changes one dial and keeps the rest, because an
	// operator adjusting a ratio has not withdrawn their ceilings.
	stdout, _ = f.ok("conductor", "configure", "--floor", "2", "--json")
	updated := decodeJSON[conductorConfigResult](t, stdout)
	if updated.Floor != 2 || updated.PerCycle != 0.50 || updated.PerDay != 5.00 {
		t.Errorf("updated configuration = %+v", updated)
	}
	if updated.IntervalSeconds != 1800 || updated.SliceSessions != 5 {
		t.Errorf("updating the floor changed the other dials: %+v", updated)
	}
}

// A configured conductor with no stored profile still will not run: a cycle
// inherits the profile the #86 ceremony stored, and the loop has no flag for
// naming one — a loop that could choose its own profile could choose its own
// spending limit.
func TestConductorRunRequiresTheStoredProfile(t *testing.T) {
	f := newFixture(t)
	f.ok("conductor", "configure", "--per-cycle", "0.50", "--per-day", "5.00", "--json")

	// With no worker at all, the missing capability is the first thing the
	// operator hears about, because it is the one that makes analysis
	// impossible rather than merely unconfigured.
	_, stderr := f.mustExit(exitFailure, "conductor", "run", "--once")
	if !strings.Contains(stderr, "Code") {
		t.Errorf("a conductor with no worker did not explain the missing capability:\n%s", stderr)
	}

	// With a worker but no stored profile, the refusal names the ceremony.
	_, stderr = f.mustExit(exitUsage, "conductor", "run", "--once", "--worker", "/nonexistent/worker")
	if !strings.Contains(stderr, "analysis profile configure") {
		t.Errorf("the refusal does not name the profile ceremony:\n%s", stderr)
	}
	if strings.Contains(stderr, "--profile") {
		t.Errorf("the conductor offered a --profile override:\n%s", stderr)
	}
}

// The status view is assembled from the journal and the durable stores, so a
// planted state must read back exactly. A status view that smoothed over a
// crashed conductor or a park would be worse than none: the loop's whole claim
// is that an operator can find out what it did.
func TestConductorStatusReportsPlantedStateTruthfully(t *testing.T) {
	f := newFixture(t)
	f.ok("conductor", "configure", "--per-cycle", "0.50", "--per-day", "5.00", "--json")

	journal, err := conductor.OpenJournal(f.dataDir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	at := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	plant := []conductor.Cycle{
		{
			Seq: 1, StartedAt: at, FinishedAt: at.Add(time.Minute),
			Outcome: conductor.OutcomeRan, Rung: conductor.RungInvitation,
			Authority: runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "invitation:inv-1"},
			RunID:     "run-cyc-1", Invitation: "inv-1", ReceiptID: "rcpt-1",
			Sessions: []string{"omp/a", "omp/b"},
			Recipes:  []string{"outcome-integrity"},
			Note:     "operator-alex invited hypothesis h-1 to be processed further",
			Cost:     0.20,
			Currency: "USD",
			PID:      4242,
		},
		{
			Seq: 2, StartedAt: at.Add(2 * time.Minute), FinishedAt: at.Add(3 * time.Minute),
			Outcome: conductor.OutcomeParked,
			Reason:  "today's 5.00 USD of the 5.00 USD daily ceiling leaves 0.00 USD",
			PID:     4242,
		},
		{
			Seq: 3, StartedAt: at.Add(4 * time.Minute),
			Outcome: conductor.OutcomeRunning, Rung: conductor.RungSerendipity,
			Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:01J000000000000000000000"},
			RunID:     "run-cyc-3", Sessions: []string{"omp/c"},
			Recipes: []string{"effective-patterns"},
			Note:    "draw 01J000000000000000000000: effective-patterns over 1 session, no aim",
			// A process that does not exist: the journal says a cycle is in
			// flight, and the truth is that whoever was running it is gone.
			PID: deadProcess(t),
		},
	}
	for _, cycle := range plant {
		if err := journal.Record(cycle); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	stdout, stderr := f.ok("conductor", "status", "--json")
	assertNoRawControls(t, "conductor status --json", stdout, stderr)
	status := decodeJSON[conductorStatusResult](t, stdout)

	// A cycle recorded as running whose conductor is gone is an interruption,
	// not a working loop.
	if status.State != string(conductor.StateInterrupted) {
		t.Errorf("state = %q, want %q", status.State, conductor.StateInterrupted)
	}
	if status.Current == nil || status.Current.Seq != 3 {
		t.Fatalf("current cycle = %+v, want the interrupted one", status.Current)
	}
	if status.Current.AuthorityKind != "serendipity" ||
		status.Current.AuthorityRef != "draw:01J000000000000000000000" {
		t.Errorf("current authority = %s/%s", status.Current.AuthorityKind, status.Current.AuthorityRef)
	}
	if status.Current.Sessions != 1 || len(status.Current.Recipes) != 1 {
		t.Errorf("current cycle scope = %+v", status.Current)
	}

	// Newest first, every outcome preserved, and each cycle still says by
	// whose authority it ran.
	if len(status.Cycles) != 3 {
		t.Fatalf("status reports %d cycles, want 3", len(status.Cycles))
	}
	for i, want := range []struct {
		seq       int
		outcome   conductor.Outcome
		authority string
	}{
		{3, conductor.OutcomeRunning, "serendipity"},
		{2, conductor.OutcomeParked, ""},
		{1, conductor.OutcomeRan, "operator"},
	} {
		got := status.Cycles[i]
		if got.Seq != want.seq || got.Outcome != string(want.outcome) || got.AuthorityKind != want.authority {
			t.Errorf("cycle %d = %+v, want seq %d outcome %q authority %q",
				i, got, want.seq, want.outcome, want.authority)
		}
	}
	// The park's reason survives, which is the whole reason a park is recorded
	// rather than merely acted on.
	if !strings.Contains(status.Cycles[1].Reason, "daily ceiling") {
		t.Errorf("the park reason was lost: %q", status.Cycles[1].Reason)
	}

	// The ladder is the ladder #96 describes, with the rung this build does not
	// implement declared rather than omitted.
	if len(status.Rungs) != 3 {
		t.Fatalf("ladder = %+v", status.Rungs)
	}
	names := []string{conductor.RungInvitation, conductor.RungPolicy, conductor.RungSerendipity}
	for i, name := range names {
		if status.Rungs[i].Name != name {
			t.Errorf("rung %d = %q, want %q", i, status.Rungs[i].Name, name)
		}
	}
	if status.Rungs[1].Implemented {
		t.Error("the policy rung claims an implementation this build does not have")
	}
	if status.Rungs[1].Note == "" {
		t.Error("the absent rung gives no reason")
	}

	// Spend is read from the receipts, not from the journal — so a journal
	// claiming 0.20 while the receipts hold nothing reports both figures
	// rather than the flattering one.
	if status.Spend == nil {
		t.Fatal("a configured conductor reported no spend")
	}
	if status.Spend.Spent != 0 || status.Spend.Remaining != 5.00 {
		t.Errorf("spend = %+v, want nothing receipted", status.Spend)
	}
	if status.Spend.Journalled != 0.20 {
		t.Errorf("journalled spend = %v, want the planted 0.20", status.Spend.Journalled)
	}
	if status.Spend.PerCycle != 0.50 || status.Spend.PerDay != 5.00 {
		t.Errorf("spend ceilings = %+v", status.Spend)
	}
	if !strings.HasSuffix(status.Journal, conductor.JournalName) {
		t.Errorf("journal path = %q", status.Journal)
	}

	// The terminal rendering carries the same answers, because legibility is
	// not a --json-only feature.
	stdout, _ = f.ok("conductor", "status", "--cycles", "2")
	for _, want := range []string{"interrupted", "serendipity draw:", "not implemented", "0.50 per cycle"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("conductor status does not render %q:\n%s", want, stdout)
		}
	}
	// --cycles bounds the table without changing the state above it.
	if strings.Count(stdout, "invitation:inv-1") != 0 {
		t.Errorf("--cycles 2 showed a third cycle:\n%s", stdout)
	}
}

// deadProcess returns a process id that has certainly exited, so a test can
// plant a journal entry whose conductor is gone without guessing a number.
func deadProcess(t *testing.T) int {
	t.Helper()
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot start a throwaway process: %v", err)
	}
	if _, err := proc.Wait(); err != nil {
		t.Skipf("cannot reap a throwaway process: %v", err)
	}
	return proc.Pid
}

// --until accepts the three things an operator plausibly means by it. Guessing
// wrong about which would stop the loop at the wrong time.
func TestParseUntilReadsEveryFormItAccepts(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{
		{"2026-08-31T18:30:00Z", time.Date(2026, 8, 31, 18, 30, 0, 0, time.UTC)},
		{"2h30m", now.Add(2*time.Hour + 30*time.Minute)},
		{"18:30", time.Date(2026, 8, 31, 18, 30, 0, 0, time.UTC)},
		// A wall clock that has already passed means tomorrow, because an
		// operator asking to stop at 08:00 in the evening means the morning.
		{"08:00", time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)},
	} {
		got, err := parseUntil(now, tc.value)
		if err != nil {
			t.Errorf("parseUntil(%q): %v", tc.value, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parseUntil(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
	for _, bad := range []string{"", "tomorrow", "-1h", "25:00", "next tuesday"} {
		if _, err := parseUntil(now, bad); err == nil {
			t.Errorf("parseUntil(%q) accepted an unreadable time", bad)
		}
	}
}
