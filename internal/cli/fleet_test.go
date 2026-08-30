package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/restic"
)

// fleetNow is the instant every case below is measured against. Recency is
// judged against a supplied clock rather than time.Now, which is what makes the
// late and never-published cases reachable at all: a healthy deployment cannot
// produce either, so a test that could only observe production could only ever
// assert "current".
var fleetNow = time.Date(2026, 8, 30, 13, 20, 0, 0, time.UTC)

// hourlyHistory is one host publishing on the hour, count times, ending at
// newest. It is the shape the Phase A timer produces.
func hourlyHistory(host string, newest time.Time, count int) []restic.Snapshot {
	return historyEvery(host, newest, count, time.Hour)
}

// historyEvery is one host publishing at a fixed interval, ending at newest.
func historyEvery(host string, newest time.Time, count int, gap time.Duration) []restic.Snapshot {
	out := make([]restic.Snapshot, 0, count)
	for i := range count {
		out = append(out, restic.Snapshot{
			ID:   host + "-" + string(rune('a'+i%26)),
			Host: host,
			Time: newest.Add(-time.Duration(i) * gap),
		})
	}
	return out
}

// byHostState indexes a report by host id, so a case asserts the verdict for
// the host it is about rather than a row position.
func byHostState(t *testing.T, res fleetResult) map[string]fleetHostRow {
	t.Helper()
	out := make(map[string]fleetHostRow, len(res.Hosts))
	for _, h := range res.Hosts {
		out[h.Host] = h
	}
	if len(out) != len(res.Hosts) {
		t.Fatalf("report holds duplicate hosts: %+v", res.Hosts)
	}
	return out
}

// The distinction the command exists for: two hosts with identical histories
// and identical cadences, one of which stopped publishing. Before this command
// they rendered identically apart from a timestamp the operator had to compare
// by eye.
//
// The pair is the non-vacuity proof. Both hosts derive the same one-hour
// cadence from the same shape of history, so the only thing separating the
// verdicts is the age of the newest snapshot; an implementation that ignored
// age, or that judged every host the same way, cannot pass both halves.
func TestFleetRecencySeparatesALateHostFromACurrentOne(t *testing.T) {
	snapshots := append(
		hourlyHistory("workstation-linux", fleetNow.Add(-12*time.Minute), 25),
		hourlyHistory("wsl-nixos", fleetNow.Add(-6*24*time.Hour), 25)...,
	)

	rows := byHostState(t, fleetRecency(fleetNow, snapshots, nil, 0))

	if got := rows["workstation-linux"].State; got != fleetCurrent {
		t.Errorf("a host that published 12 minutes ago is %q, want %q", got, fleetCurrent)
	}
	if got := rows["wsl-nixos"].State; got != fleetLate {
		t.Errorf("a host silent for six days is %q, want %q", got, fleetLate)
	}
	// The verdict must be traceable: an operator has to be able to see the
	// cadence it was made against and that it was observed, not configured.
	late := rows["wsl-nixos"]
	if late.CadenceSource != cadenceFromHost {
		t.Errorf("late host's cadence source = %q, want %q", late.CadenceSource, cadenceFromHost)
	}
	if late.ExpectedEverySeconds == nil || *late.ExpectedEverySeconds != int64(time.Hour/time.Second) {
		t.Errorf("late host's expected cadence = %v, want 3600s", late.ExpectedEverySeconds)
	}
}

// One missed publication is not late. The Phase A timer is Persistent=true so a
// machine that was asleep at the top of the hour catches up on the next run, and
// reporting that as a fault would train the operator to ignore the command.
//
// The boundary is asserted from both sides, which is what makes it a boundary
// rather than a number: at three cadences the host is still current, and just
// past three it is late. A single-sided assertion would pass against any
// threshold at or above three hours.
func TestFleetRecencyToleratesJitterButNotTwoMissedRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		age  time.Duration
		want fleetState
	}{
		{"one missed run", 2 * time.Hour, fleetCurrent},
		{"exactly at the tolerance", 3 * time.Hour, fleetCurrent},
		{"just past the tolerance", 3*time.Hour + time.Minute, fleetLate},
		{"two missed runs", 4 * time.Hour, fleetLate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshots := hourlyHistory("host-a", fleetNow.Add(-tc.age), 25)
			rows := byHostState(t, fleetRecency(fleetNow, snapshots, nil, 0))
			if got := rows["host-a"].State; got != tc.want {
				t.Errorf("age %s is %q, want %q", tc.age, got, tc.want)
			}
		})
	}
}

// A single-host archive must not imply a missing machine. The operator's real
// deployment is exactly this shape, and a command that invented an absence from
// it - or that read "one host" as "you are missing the others" - would be
// unusable on the only archive that exists.
//
// Non-vacuity: the same assertion is made with the same host named through
// --expect, so the test would fail if the roster were ignored in either
// direction - by fabricating a missing row, or by failing to match a host that
// did publish.
func TestFleetRecencySingleHostArchiveImpliesNoMissingMachine(t *testing.T) {
	snapshots := hourlyHistory("workstation-linux", fleetNow.Add(-18*time.Minute), 25)

	for _, expect := range [][]string{nil, {"workstation-linux"}} {
		res := fleetRecency(fleetNow, snapshots, expect, 0)
		if res.Counts.Hosts != 1 || res.Counts.Current != 1 {
			t.Fatalf("expect=%v: counts = %+v, want 1 host, 1 current", expect, res.Counts)
		}
		if res.Counts.Missing != 0 {
			t.Errorf("expect=%v: a single-host archive reported %d missing hosts", expect, res.Counts.Missing)
		}
		if got := res.Hosts[0].State; got != fleetCurrent {
			t.Errorf("expect=%v: the only host is %q, want %q", expect, got, fleetCurrent)
		}
	}
}

// The half the archive cannot answer on its own. A machine that never published
// has nothing in the repository to be found, so it is invisible until the
// operator names it - and once named, it must be reported as absent rather than
// as a host with zero snapshots, which would read like empty data.
func TestFleetRecencyNamesAMachineThatPublishedNothing(t *testing.T) {
	snapshots := hourlyHistory("workstation-linux", fleetNow.Add(-18*time.Minute), 25)

	res := fleetRecency(fleetNow, snapshots, []string{"macbook-air", "workstation-linux"}, 0)
	rows := byHostState(t, res)

	missing, ok := rows["macbook-air"]
	if !ok {
		t.Fatalf("a host named with --expect is absent from the report: %+v", res.Hosts)
	}
	if missing.State != fleetMissing {
		t.Errorf("a host that published nothing is %q, want %q", missing.State, fleetMissing)
	}
	// Absence must render as absence, never as a guess and never as a zero
	// that reads like a measurement.
	if missing.LastPublished != "" || missing.AgeSeconds != nil || missing.ExpectedEverySeconds != nil {
		t.Errorf("a never-published host carries invented values: %+v", missing)
	}
	// Naming an absent machine must not disturb the verdict for one that is
	// publishing normally.
	if got := rows["workstation-linux"].State; got != fleetCurrent {
		t.Errorf("the publishing host became %q once a missing one was named, want %q", got, fleetCurrent)
	}
	if res.Counts.Missing != 1 || res.Counts.Current != 1 {
		t.Errorf("counts = %+v, want 1 current and 1 missing", res.Counts)
	}
}

// A host publishing normally is reported whether or not the operator remembered
// to name it: the archive is truth about what published, so --expect adds
// machines and never hides them.
func TestFleetRecencyReportsAHostTheOperatorDidNotName(t *testing.T) {
	snapshots := append(
		hourlyHistory("workstation-linux", fleetNow.Add(-18*time.Minute), 25),
		hourlyHistory("unnamed-host", fleetNow.Add(-20*time.Minute), 25)...,
	)

	res := fleetRecency(fleetNow, snapshots, []string{"workstation-linux"}, 0)
	if _, ok := byHostState(t, res)["unnamed-host"]; !ok {
		t.Errorf("--expect hid a host that is publishing: %+v", res.Hosts)
	}
}

// No cadence, no verdict. One snapshot is no interval, and with no other host to
// borrow one from there is nothing to judge against - so the state is unknown
// and the age is still reported, because the age is observed even when the
// judgement is not available.
//
// Reporting this as current would be the worst available failure: a guess
// wearing the word that means "your backups are fine".
func TestFleetRecencyRefusesAVerdictWithoutACadence(t *testing.T) {
	snapshots := []restic.Snapshot{{
		ID: "only", Host: "fresh-host", Time: fleetNow.Add(-6 * 24 * time.Hour),
	}}

	res := fleetRecency(fleetNow, snapshots, nil, 0)
	row := res.Hosts[0]
	if row.State != fleetUnknown {
		t.Errorf("a host with one snapshot and no fleet cadence is %q, want %q", row.State, fleetUnknown)
	}
	if row.ExpectedEverySeconds != nil || row.CadenceSource != "" {
		t.Errorf("a cadence was invented for a host with no interval: %+v", row)
	}
	if row.AgeSeconds == nil || *row.AgeSeconds != int64(6*24*time.Hour/time.Second) {
		t.Errorf("age = %v, want six days in seconds", row.AgeSeconds)
	}
	if res.Counts.Unknown != 1 || res.Counts.Current != 0 {
		t.Errorf("counts = %+v, want 1 unknown and 0 current", res.Counts)
	}
}

// An operator-supplied cadence resolves the same host that had no verdict of its
// own, and says so: the source names the flag, so a number Babel was told is
// never confused with one it derived.
func TestFleetRecencyAcceptsAnOperatorSuppliedCadence(t *testing.T) {
	snapshots := []restic.Snapshot{{
		ID: "only", Host: "fresh-host", Time: fleetNow.Add(-6 * 24 * time.Hour),
	}}

	row := fleetRecency(fleetNow, snapshots, nil, time.Hour).Hosts[0]
	if row.State != fleetLate {
		t.Errorf("with --every 1h a six-day-old host is %q, want %q", row.State, fleetLate)
	}
	if row.CadenceSource != cadenceFromFlag {
		t.Errorf("cadence source = %q, want %q", row.CadenceSource, cadenceFromFlag)
	}
}

// A host with too little history of its own is judged against what its fleet
// does, and the borrowed cadence is labelled as borrowed. One deployment's
// machines run one rendered timer, so the assumption is defensible - but it is
// an assumption about that host rather than an observation of it, so it is named
// in the report instead of hidden inside the verdict.
func TestFleetRecencyBorrowsTheFleetCadenceAndSaysSo(t *testing.T) {
	snapshots := append(
		hourlyHistory("workstation-linux", fleetNow.Add(-12*time.Minute), 25),
		restic.Snapshot{ID: "one", Host: "wsl-nixos", Time: fleetNow.Add(-6 * 24 * time.Hour)},
	)

	rows := byHostState(t, fleetRecency(fleetNow, snapshots, nil, 0))
	borrowed := rows["wsl-nixos"]
	if borrowed.State != fleetLate {
		t.Errorf("a host with one six-day-old snapshot beside an hourly fleet is %q, want %q",
			borrowed.State, fleetLate)
	}
	if borrowed.CadenceSource != cadenceFromFleet {
		t.Errorf("cadence source = %q, want %q", borrowed.CadenceSource, cadenceFromFleet)
	}
	// The host's own history is preferred when it has one, so the borrowed
	// source must not appear on a host that could speak for itself.
	if got := rows["workstation-linux"].CadenceSource; got != cadenceFromHost {
		t.Errorf("a host with its own history used source %q, want %q", got, cadenceFromHost)
	}
}

// The cadence must survive the outage it is used to detect. A mean would be
// dragged upward by exactly the gap this command exists to notice - a host down
// for a week would derive a cadence that excuses being down for a week - so the
// median is load-bearing rather than a stylistic choice.
func TestObservedCadenceSurvivesAnOutageInTheHistory(t *testing.T) {
	// Twenty-four hourly publications, then a six-day gap, then more hourly
	// ones: the shape a machine that was off for a week leaves behind.
	times := []time.Time{}
	at := fleetNow.Add(-30 * 24 * time.Hour)
	for range 24 {
		times = append(times, at)
		at = at.Add(time.Hour)
	}
	at = at.Add(6 * 24 * time.Hour)
	for range 24 {
		times = append(times, at)
		at = at.Add(time.Hour)
	}

	got, ok := observedCadence(times)
	if !ok {
		t.Fatal("a history with 47 gaps produced no cadence")
	}
	if got != time.Hour {
		t.Errorf("observed cadence = %s, want 1h despite the outage in the history", got)
	}
}

// A cadence observed faster than hourly is a bootstrap artefact, not a schedule.
// Without the floor a machine the operator pushed twenty times by hand would
// read as late an hour later - a false alarm produced entirely by Babel's own
// inference, against a machine that is working.
func TestObservedCadenceFloorsABootstrapBurstAtHourly(t *testing.T) {
	var times []time.Time
	at := fleetNow.Add(-2 * time.Hour)
	for range 20 {
		times = append(times, at)
		at = at.Add(30 * time.Second)
	}

	got, ok := observedCadence(times)
	if !ok {
		t.Fatal("a burst of twenty pushes produced no cadence")
	}
	if got != minObservedCadence {
		t.Errorf("observed cadence = %s, want the %s floor", got, minObservedCadence)
	}

	// The floor must be visible in the verdict, not just in the derivation:
	// this machine published two hours ago and is working.
	rows := byHostState(t, fleetRecency(fleetNow, historyEvery("burst-host", fleetNow.Add(-2*time.Hour), 20, 30*time.Second), nil, 0))
	if got := rows["burst-host"].State; got != fleetCurrent {
		t.Errorf("a host that bootstrapped by hand two hours ago is %q, want %q", got, fleetCurrent)
	}
}

// One snapshot is no interval, and repeated snapshots at one instant are one
// publication attempt rather than a cadence of nothing.
func TestObservedCadenceRefusesHistoryWithNoInterval(t *testing.T) {
	if _, ok := observedCadence([]time.Time{fleetNow}); ok {
		t.Error("one snapshot produced a cadence")
	}
	same := []time.Time{fleetNow, fleetNow, fleetNow}
	if d, ok := observedCadence(same); ok {
		t.Errorf("three snapshots at one instant produced a cadence of %s", d)
	}
}

// A host whose clock runs ahead must not produce a negative age or a duration
// that reads as absurd. The comparison is unavoidable - snapshot times come from
// the machine that took them and no server-assigned time exists - so the only
// choice is how the skew renders, and it renders as "just published".
func TestFleetRecencyClampsASnapshotDatedInTheFuture(t *testing.T) {
	snapshots := hourlyHistory("skewed-host", fleetNow.Add(2*time.Hour), 25)

	row := fleetRecency(fleetNow, snapshots, nil, 0).Hosts[0]
	if row.AgeSeconds == nil || *row.AgeSeconds != 0 {
		t.Errorf("age for a future-dated snapshot = %v, want 0", row.AgeSeconds)
	}
	if row.State != fleetCurrent {
		t.Errorf("a future-dated host is %q, want %q", row.State, fleetCurrent)
	}
}

// The rendered report is the deliverable: the operator's answer is the first
// line, and the states that need attention must be distinguishable from the
// state that does not without comparing timestamps. Pinning the exact output
// keeps a reordered column or a softened label from quietly turning the answer
// back into a timestamp comparison.
func TestReportFleetRendersTheAnswerAtAGlance(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	snapshots := append(
		hourlyHistory("workstation-linux", fleetNow.Add(-12*time.Minute), 25),
		hourlyHistory("wsl-nixos", fleetNow.Add(-6*24*time.Hour-2*time.Hour), 25)...,
	)
	res := fleetRecency(fleetNow, snapshots, []string{"macbook-air"}, 0)
	res.Repository = "s3:example/babel"
	if err := a.reportFleet(res); err != nil {
		t.Fatalf("reportFleet: %v", err)
	}

	const want = "fleet: 3 hosts, 1 current, 1 late, 1 missing\n\n" +
		"HOST               STATE    LAST PUBLISHED        AGE   EXPECTED EVERY  SNAPSHOTS\n" +
		"macbook-air        MISSING  -                     -     -               0\n" +
		"workstation-linux  current  2026-08-30T13:08:00Z  12m   1h (observed)   25\n" +
		"wsl-nixos          LATE     2026-08-24T11:20:00Z  6d2h  1h (observed)   25\n"
	if got := stdout.String(); got != want {
		t.Errorf("rendered report:\n%s\nwant:\n%s", got, want)
	}

	// Each row that is not current explains itself, restating the rule it
	// failed: a derived verdict the operator cannot check is one he has to
	// take on faith.
	diags := stderr.String()
	for _, want := range []string{
		"wsl-nixos last published 6d2h ago, more than 3 times its own observed cadence of 1h",
		"babel-archive.timer",
		"macbook-air was named with --expect but has published nothing into this repository",
		"publishing under a different host id",
	} {
		if !strings.Contains(diags, want) {
			t.Errorf("diagnostics missing %q; got:\n%s", want, diags)
		}
	}
	// A current host earns no note. Explaining every row would bury the two
	// that matter.
	if strings.Contains(diags, "workstation-linux") {
		t.Errorf("a current host was explained anyway:\n%s", diags)
	}
}

// The sentence explaining a late verdict must be true of the host it explains.
// One template with the source name substituted in produces two falsehoods: "its
// --every cadence" attributes the operator's own instruction to the machine, and
// "its observed cadence" claims an observation of a host whose cadence was
// borrowed from its neighbours. A verdict is only checkable if its explanation
// is accurate, and this was caught rendering the real archive rather than in
// review.
func TestCadencePhraseAttributesEachCadenceHonestly(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   string
	}{
		{cadenceFromHost, "its own observed cadence of 1h"},
		{cadenceFromFleet, "the fleet's observed cadence of 1h"},
		{cadenceFromFlag, "the 1h cadence given with --every"},
	} {
		if got := cadencePhrase(tc.source, time.Hour); got != tc.want {
			t.Errorf("cadencePhrase(%q) = %q, want %q", tc.source, got, tc.want)
		}
	}

	// The borrowed case end to end, because that is the one a template gets
	// wrong while still reading plausibly.
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	snapshots := append(
		hourlyHistory("workstation-linux", fleetNow.Add(-12*time.Minute), 25),
		restic.Snapshot{ID: "one", Host: "wsl-nixos", Time: fleetNow.Add(-6 * 24 * time.Hour)},
	)
	if err := a.reportFleet(fleetRecency(fleetNow, snapshots, nil, 0)); err != nil {
		t.Fatalf("reportFleet: %v", err)
	}
	if want := "more than 3 times the fleet's observed cadence of 1h"; !strings.Contains(stderr.String(), want) {
		t.Errorf("borrowed-cadence note missing %q; got:\n%s", want, stderr.String())
	}
}

// An entirely healthy fleet says so in two words rather than four counts the
// reader has to check are all zero.
func TestReportFleetSummarisesAHealthyFleetWithoutCounts(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	res := fleetRecency(fleetNow, hourlyHistory("workstation-linux", fleetNow.Add(-18*time.Minute), 25), nil, 0)
	if err := a.reportFleet(res); err != nil {
		t.Fatalf("reportFleet: %v", err)
	}
	if got, want := strings.SplitN(stdout.String(), "\n", 2)[0], "fleet: 1 host, all current"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("a healthy fleet produced diagnostics: %q", stderr.String())
	}
}

// An empty repository is a new deployment, not a fleet of missing machines. It
// gets the one instruction that makes the command able to answer anything at
// all, rather than a table claiming to show a fleet.
func TestReportFleetOnAnEmptyRepositoryOffersTheRoster(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	if err := a.reportFleet(fleetRecency(fleetNow, nil, nil, 0)); err != nil {
		t.Fatalf("reportFleet: %v", err)
	}
	if got, want := stdout.String(), "no host has published into this repository yet\n"; got != want {
		t.Errorf("empty report = %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "--expect") {
		t.Errorf("the empty report does not name --expect: %q", stderr.String())
	}
}

// The JSON is the machine-readable half of the same contract, and absence means
// absence throughout: a host that published nothing carries no age, no cadence
// and no timestamp, because filling any of them would state something no listing
// said.
func TestFleetResultJSONKeepsUnobservedValuesAbsent(t *testing.T) {
	res := fleetRecency(fleetNow, hourlyHistory("workstation-linux", fleetNow.Add(-12*time.Minute), 3),
		[]string{"macbook-air"}, 0)
	res.Repository = "s3:example/babel"

	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"repository":"s3:example/babel","now":"2026-08-30T13:20:00Z",` +
		`"counts":{"hosts":2,"current":1,"late":0,"missing":1,"unknown":0},` +
		`"hosts":[{"host":"macbook-air","state":"missing","snapshots":0},` +
		`{"host":"workstation-linux","state":"current","snapshots":3,` +
		`"last_published":"2026-08-30T13:08:00Z","age_seconds":720,` +
		`"expected_every_seconds":3600,"cadence_source":"observed"}]}`
	if string(encoded) != want {
		t.Errorf("fleet JSON =\n%s\nwant\n%s", encoded, want)
	}
}

// A hostile host id cannot move the cursor or push the table apart: host names
// come from the repository, which any machine sharing it can write.
func TestFleetRecencySanitizesHostIDsFromTheRepository(t *testing.T) {
	snapshots := hourlyHistory("evil\x1b[2Jhost", fleetNow.Add(-12*time.Minute), 3)

	row := fleetRecency(fleetNow, snapshots, nil, 0).Hosts[0]
	if strings.ContainsRune(row.Host, 0x1b) {
		t.Errorf("host id reached the report unescaped: %q", row.Host)
	}
	if want := `evil\u{1B}[2Jhost`; row.Host != want {
		t.Errorf("host id = %q, want %q", row.Host, want)
	}
}

// Ages are rendered at the precision the judgement is made at and no finer.
// "146h31m9s" is the timestamp comparison this command was written to remove.
func TestFormatAgeReadsAtAGlance(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{-time.Hour, "<1m"},
		{0, "<1m"},
		{59 * time.Second, "<1m"},
		{12 * time.Minute, "12m"},
		{time.Hour, "1h"},
		{3*time.Hour + 18*time.Minute, "3h18m"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d"},
		{6*24*time.Hour + 2*time.Hour + 31*time.Minute, "6d2h"},
		{30 * 24 * time.Hour, "30d"},
	} {
		if got := formatAge(tc.in); got != tc.want {
			t.Errorf("formatAge(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --expect rejects an id that cannot be a host id rather than reporting it
// missing. "MISSING" must mean "this machine did not publish"; letting a typo
// produce it would make the word mean "you mistyped something" as well, and the
// operator could not tell which answer he was reading.
func TestExpectedHostsRejectsWhatCannotBeAHostID(t *testing.T) {
	c := newCmd("archive fleet", archiveFleetUsage)

	got, err := expectedHosts(c, " workstation-linux , wsl-nixos ,, workstation-linux ")
	if err != nil {
		t.Fatalf("expectedHosts: %v", err)
	}
	if want := []string{"workstation-linux", "wsl-nixos"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parsed --expect = %v, want %v", got, want)
	}

	if _, err := expectedHosts(c, "Not A Host"); err == nil {
		t.Error("an id that cannot be a host id was accepted")
	}
	if got, err := expectedHosts(c, "  "); err != nil || got != nil {
		t.Errorf("empty --expect = %v, %v; want nil, nil", got, err)
	}
}

// The subcommand is routed and self-documenting. A capability an operator
// cannot discover from "babel archive -h" is one he will not find.
func TestArchiveFleetIsDiscoverable(t *testing.T) {
	for _, usage := range []string{rootUsage, archiveUsage} {
		if !strings.Contains(usage, "fleet") {
			t.Errorf("usage does not mention the fleet command:\n%s", usage)
		}
	}
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}
	if err := a.archive(t.Context(), []string{"fleet", "-h"}); err != errHelp {
		t.Fatalf("archive fleet -h = %v, want errHelp", err)
	}
	for _, want := range []string{"--expect", "--every", "MISSING", "observed", "fleet"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("fleet usage does not document %q", want)
		}
	}
}

// --host is deliberately not defined here: on every other archive command it
// names the identity this machine publishes as, and on a report about other
// machines it would read as a filter. An accepted-and-ignored flag would be a
// trap on exactly the command whose subject is hosts.
func TestArchiveFleetDefinesNoHostFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	err := a.archive(t.Context(), []string{"fleet", "--host", "workstation-linux"})
	var usage *usageError
	if !asUsageError(err, &usage) {
		t.Fatalf("archive fleet --host = %v, want a usage error", err)
	}
	if !strings.Contains(usage.msg, "not defined") {
		t.Errorf("rejection = %q, want it to name the undefined flag", usage.msg)
	}
}

// asUsageError is errors.As specialised, kept local so the assertion above
// reads as one line.
func asUsageError(err error, target **usageError) bool {
	u, ok := err.(*usageError)
	if ok {
		*target = u
	}
	return ok
}
