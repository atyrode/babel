package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/restic"
)

// lateAfterCadences is how many expected publications a host may miss before
// it is reported late.
//
// One would be useless: any jitter at all puts a periodic publication past a
// single cadence, so every host would read late half the time. Two fires on one
// missed run, which is routine rather than a fault - the Phase A timer is
// Persistent=true precisely so a machine that was asleep, offline, or busy at
// the top of the hour catches up on the next one, and treating that as a
// problem would train the operator to ignore this command. Three means the host
// has missed two consecutive publications and is into a third: a pattern, not
// an event. That is the smallest number that distinguishes "this machine is
// broken" from "this machine had a bad hour", which is the whole judgement the
// command exists to make.
const lateAfterCadences = 3

// cadenceSampleGaps bounds how much of a host's history the observed cadence is
// derived from, counted in gaps between consecutive snapshots.
//
// Twenty-four is one day of hourly publication. Fewer samples and a couple of
// missed runs could swing the median; more and a host that changed schedule
// would be judged for weeks against a cadence it no longer keeps. Only the most
// recent gaps are sampled, so the answer describes how the host publishes now.
const cadenceSampleGaps = 24

// minObservedCadence floors a derived cadence.
//
// Babel does not own the timer - dotfiles renders and enables it (SPEC.md §12),
// and nothing in Babel's configuration records its schedule - so this command
// must not claim to know the cadence. It may, however, refuse to believe an
// observation that the deployment contract rules out: SPEC.md §12 fixes
// OnCalendar=hourly as the Phase A publication schedule, so a host observed
// publishing faster than hourly is showing manual pushes from a bootstrap or
// backfill session rather than its schedule. Without this floor a machine the
// operator pushed twenty times by hand during bootstrap would derive a cadence
// of seconds and read as late an hour later, which would be a false alarm
// produced entirely by Babel's own inference.
//
// It is a floor and never a value, so it can only ever delay a late verdict,
// never bring one forward. An error in it is therefore an error in the
// conservative direction.
const minObservedCadence = time.Hour

// fleetState is one host's publication recency verdict.
type fleetState string

const (
	// fleetCurrent means the host published within its expected cadence.
	fleetCurrent fleetState = "current"
	// fleetLate means the host has missed lateAfterCadences publications.
	fleetLate fleetState = "late"
	// fleetMissing means a host the operator named with --expect has published
	// nothing into this repository at all. It is the only state that cannot be
	// derived from the archive, because nothing in an archive can attest to a
	// machine that never wrote to it.
	fleetMissing fleetState = "missing"
	// fleetUnknown means the host has published but no cadence could be
	// derived, so no verdict is available. Absence of a judgement is reported
	// as absence rather than as "current", which would be a guess dressed as
	// reassurance.
	fleetUnknown fleetState = "unknown"
)

// label renders a state for a terminal. The two states an operator must not
// scroll past are upper-cased, because this command's whole purpose is to be
// answerable at a glance and Babel emits no colour: shape is the only signal
// available in a monochrome table, and a lower-case "late" beside a lower-case
// "current" is exactly the timestamp comparison this command replaces.
func (s fleetState) label() string {
	switch s {
	case fleetLate:
		return "LATE"
	case fleetMissing:
		return "MISSING"
	default:
		return string(s)
	}
}

// Cadence sources, reported beside every expected cadence so a derived number
// is never mistaken for a configured one.
const (
	// cadenceFromFlag is an operator-supplied --every.
	cadenceFromFlag = "--every"
	// cadenceFromHost is the host's own observed publication interval.
	cadenceFromHost = "observed"
	// cadenceFromFleet is the median of the other hosts' observed intervals,
	// used for a host with too little history of its own. Defensible because
	// one deployment's machines run one dotfiles-rendered timer, and stated in
	// the output because it is an assumption about the host rather than an
	// observation of it.
	cadenceFromFleet = "fleet"
)

// fleetHostRow is one host's publication recency.
//
// Every derived number is accompanied by where it came from, and every value
// this machine could not observe is absent rather than zero: a host with no
// snapshots has no age and no cadence, and reporting either as 0 would state
// something no listing said.
type fleetHostRow struct {
	Host      string     `json:"host"`
	State     fleetState `json:"state"`
	Snapshots int        `json:"snapshots"`
	// LastPublished is restic's recorded time for the host's newest snapshot,
	// not when any catalog learned of it: adoption is later than publication,
	// so reporting it would claim the archive is fresher than it is.
	LastPublished string `json:"last_published,omitempty"`
	// AgeSeconds is how long ago that was, by this machine's clock. It is
	// absent for a host with no snapshots.
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
	// ExpectedEverySeconds is the cadence the verdict was made against, absent
	// when none could be established.
	ExpectedEverySeconds *int64 `json:"expected_every_seconds,omitempty"`
	// CadenceSource names how that cadence was arrived at.
	CadenceSource string `json:"cadence_source,omitempty"`

	// age and cadence carry the same two values for rendering, which needs
	// durations rather than the seconds the JSON contract fixes.
	age     time.Duration
	cadence time.Duration
}

// fleetCounts is the one-line answer: how many machines, and how many of them
// are fine.
type fleetCounts struct {
	Hosts   int `json:"hosts"`
	Current int `json:"current"`
	Late    int `json:"late"`
	Missing int `json:"missing"`
	Unknown int `json:"unknown"`
}

// fleetResult is the machine-readable fleet recency report.
type fleetResult struct {
	Repository string `json:"repository"`
	// Now is the instant every age was measured against, so a stored report
	// stays interpretable and a reader can tell an old report from a fresh one.
	Now    string         `json:"now"`
	Counts fleetCounts    `json:"counts"`
	Hosts  []fleetHostRow `json:"hosts"`
}

// fleetRecency judges every host's publication recency from one snapshot
// listing. It is the whole decision, and it is pure: the listing, the instant
// to measure against, the operator's roster and the operator's cadence override
// are all arguments, so every case this command can reach is reachable in a
// test - including the late and never-published cases a healthy single-host
// deployment cannot produce.
//
// The repository listing is the sole authority. It carries every host that has
// published, each of their snapshot times, and therefore both halves of the
// answer: how long ago a host last published, and how often it normally does.
// The shared catalog is deliberately not consulted - it cannot hold a host the
// repository does not, because a host row is only ever written alongside a
// snapshot restic already committed - so this command needs no DSN and answers
// identically in local and shared mode.
//
// Ages are this machine's clock minus a remote host's recorded time, which is
// the only comparison the data permits: snapshot times come from the machine
// that took them, and no server-assigned time for a snapshot exists. A host
// whose clock runs ahead therefore reads as fresher than it is, and a snapshot
// dated in the future is reported as age zero rather than as a negative
// duration.
func fleetRecency(now time.Time, snapshots []restic.Snapshot, expect []string, every time.Duration) fleetResult {
	byHost := make(map[string][]time.Time)
	for _, s := range snapshots {
		byHost[s.Host] = append(byHost[s.Host], s.Time)
	}

	observed := make(map[string]time.Duration, len(byHost))
	for host, times := range byHost {
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		if d, ok := observedCadence(times); ok {
			observed[host] = d
		}
	}
	fleetWide, fleetWideOK := fleetCadence(observed)

	res := fleetResult{Now: formatTime(now)}
	for _, host := range fleetHostNames(byHost, expect) {
		times := byHost[host]
		row := fleetHostRow{Host: Sanitize(host), Snapshots: len(times)}
		if len(times) == 0 {
			row.State = fleetMissing
			res.Counts.Missing++
			res.Hosts = append(res.Hosts, row)
			continue
		}

		newest := times[len(times)-1]
		age := now.Sub(newest)
		if age < 0 {
			age = 0
		}
		row.LastPublished = formatTime(newest)
		row.age = age
		row.AgeSeconds = seconds(age)

		hostCadence, hostOK := observed[host]
		cadence, source := expectedCadence(hostCadence, hostOK, fleetWide, fleetWideOK, every)
		if source == "" {
			row.State = fleetUnknown
			res.Counts.Unknown++
			res.Hosts = append(res.Hosts, row)
			continue
		}
		row.cadence = cadence
		row.ExpectedEverySeconds = seconds(cadence)
		row.CadenceSource = source
		if age > cadence*lateAfterCadences {
			row.State = fleetLate
			res.Counts.Late++
		} else {
			row.State = fleetCurrent
			res.Counts.Current++
		}
		res.Hosts = append(res.Hosts, row)
	}
	res.Counts.Hosts = len(res.Hosts)
	return res
}

// fleetHostNames is the roster: every host the repository holds, plus every
// host the operator named that it does not, ordered by host id.
//
// The union is deliberate rather than a filter. The archive is truth about what
// published, so a host publishing normally is reported whether or not the
// operator remembered to name it; --expect only ever adds machines that could
// otherwise not be missed, because an archive cannot attest to a machine that
// never wrote to it.
func fleetHostNames(byHost map[string][]time.Time, expect []string) []string {
	names := make([]string, 0, len(byHost)+len(expect))
	for host := range byHost {
		names = append(names, host)
	}
	for _, host := range expect {
		if _, published := byHost[host]; !published {
			names = append(names, host)
		}
	}
	sort.Strings(names)
	return names
}

// expectedCadence resolves the cadence one host is judged against, and names
// how it was arrived at. An empty source means none could be established, and
// the caller must then report no verdict rather than assume one.
//
// The order is authority order: what the operator said, then what this host
// did, then what its fleet does. Nothing below that is a fact about the host,
// so nothing below that is invented.
func expectedCadence(host time.Duration, hostOK bool, fleetWide time.Duration, fleetWideOK bool, every time.Duration) (time.Duration, string) {
	switch {
	case every > 0:
		return every, cadenceFromFlag
	case hostOK:
		return host, cadenceFromHost
	case fleetWideOK:
		return fleetWide, cadenceFromFleet
	default:
		return 0, ""
	}
}

// observedCadence derives how often a host publishes, from the gaps between its
// most recent snapshots.
//
// The median, not the mean: the gap that matters is the ordinary one, and a
// mean is dragged upward by exactly the outages this command is trying to
// notice - a host down for a week would derive a cadence that excuses being
// down for a week. Gaps of zero or less are dropped rather than counted: two
// snapshots restic recorded at the same instant describe one publication
// attempt, and admitting them would derive a cadence of nothing.
//
// It reports false rather than a guess when the host has no observable interval
// at all, which one snapshot always is.
func observedCadence(times []time.Time) (time.Duration, bool) {
	if len(times) < 2 {
		return 0, false
	}
	gaps := make([]time.Duration, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d > 0 {
			gaps = append(gaps, d)
		}
	}
	if len(gaps) == 0 {
		return 0, false
	}
	// Sampled before sorting, so the window is the most recent gaps rather
	// than the shortest ones.
	if len(gaps) > cadenceSampleGaps {
		gaps = gaps[len(gaps)-cadenceSampleGaps:]
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	if d := medianDuration(gaps); d > minObservedCadence {
		return d, true
	}
	return minObservedCadence, true
}

// fleetCadence is the median of the hosts' own observed cadences, which is what
// a host with too little history of its own is judged against.
func fleetCadence(observed map[string]time.Duration) (time.Duration, bool) {
	if len(observed) == 0 {
		return 0, false
	}
	all := make([]time.Duration, 0, len(observed))
	for _, d := range observed {
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return medianDuration(all), true
}

// medianDuration returns the median of an ascending, non-empty slice. The even
// case interpolates as lo+(hi-lo)/2 rather than (lo+hi)/2, which cannot
// overflow.
func medianDuration(sorted []time.Duration) time.Duration {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	lo, hi := sorted[n/2-1], sorted[n/2]
	return lo + (hi-lo)/2
}

// seconds renders a duration for the JSON contract, which fixes seconds so a
// reader never has to guess a unit.
func seconds(d time.Duration) *int64 {
	v := int64(d / time.Second)
	return &v
}

// formatAge renders a duration for a human reading a column, at the precision
// the judgement is made at and no finer. Six days is "6d2h", not "146h31m9s":
// this command is answered by glancing, and a value that has to be parsed by
// eye is the problem it was written to remove.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	case d < 24*time.Hour:
		if m := int64(d/time.Minute) % 60; m != 0 {
			return fmt.Sprintf("%dh%dm", int64(d/time.Hour), m)
		}
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	default:
		if h := int64(d/time.Hour) % 24; h != 0 {
			return fmt.Sprintf("%dd%dh", int64(d/(24*time.Hour)), h)
		}
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	}
}

// archiveFleet implements `babel archive fleet`.
func (a *app) archiveFleet(ctx context.Context, args []string) error {
	c := newCmd("archive fleet", archiveFleetUsage)
	var rf repoFlags
	// Repository selection without --host: on every other archive command that
	// flag names the identity this machine publishes as, and on the one command
	// whose entire subject is other machines it would read as "only this host".
	// An accepted-and-ignored flag there would be a trap, so it is not defined.
	rf.bindRepo(c.fs)
	expect := c.fs.String("expect", "", "comma-separated host ids you expect to be publishing")
	every := c.fs.Duration("every", 0, "publication cadence to expect, overriding the observed one")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *every < 0 {
		return c.usagef("--every must be a positive duration, got %s", *every)
	}
	expected, err := expectedHosts(c, *expect)
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	repo, err := rf.open(c, d, nil)
	if err != nil {
		return err
	}
	snapshots, err := repo.Snapshots(ctx)
	if err != nil {
		return a.resticFailure("list snapshots", err)
	}

	res := fleetRecency(time.Now().UTC(), snapshots, expected, *every)
	res.Repository = Sanitize(rf.repository)
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.reportFleet(res)
}

// expectedHosts parses --expect. A malformed id is rejected rather than
// reported missing: "MISSING" must mean "this machine did not publish", and a
// typo that cannot be a host id at all would make it mean "you mistyped
// something", which is a different answer wearing the same word.
func expectedHosts(c *cmd, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !validHostID(part) {
			return nil, c.usagef("invalid --expect host %q: host ids are 1-%d characters of [a-z0-9._-] starting alphanumeric", part, maxHostIDLen)
		}
		if _, dup := seen[part]; dup {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out, nil
}

// reportFleet writes the recency report for a terminal.
//
// The summary comes first and answers the question by itself, so the table
// below it is confirmation rather than work. Per-host explanations go to stderr
// with the rest of Babel's diagnostics: a note is prose about one row, and
// folding it into the table would push the columns apart for the one host that
// needed a sentence.
func (a *app) reportFleet(res fleetResult) error {
	if len(res.Hosts) == 0 {
		fmt.Fprint(a.stdout, "no host has published into this repository yet\n")
		a.diagf("note: name the machines you expect with --expect HOST[,HOST...] to see which of them have not published\n")
		return nil
	}
	fmt.Fprintf(a.stdout, "fleet: %s\n\n", fleetSummary(res.Counts))
	rows := make([][]string, 0, len(res.Hosts))
	for _, h := range res.Hosts {
		rows = append(rows, []string{
			h.Host,
			h.State.label(),
			orMissing(h.LastPublished),
			fleetAgeCell(h),
			fleetCadenceCell(h),
			fmt.Sprint(h.Snapshots),
		})
	}
	if err := writeTable(a.stdout,
		[]string{"HOST", "STATE", "LAST PUBLISHED", "AGE", "EXPECTED EVERY", "SNAPSHOTS"},
		rows); err != nil {
		return err
	}
	a.fleetNotes(res)
	return nil
}

// fleetAgeCell renders how long ago a host published, or absence for one that
// never did.
func fleetAgeCell(h fleetHostRow) string {
	if h.AgeSeconds == nil {
		return missingValue
	}
	return formatAge(h.age)
}

// fleetCadenceCell renders the cadence a verdict was made against together with
// where it came from, because an operator must be able to see that a number
// Babel inferred is inferred. A host with no cadence shows absence, not a
// default.
func fleetCadenceCell(h fleetHostRow) string {
	if h.CadenceSource == "" {
		return missingValue
	}
	return formatAge(h.cadence) + " (" + h.CadenceSource + ")"
}

// fleetSummary is the one line that answers "did all my machines back up".
// Zero counts are omitted, so the states present are the states named, and an
// entirely healthy fleet says so in two words instead of four numbers the
// reader has to check are zero.
func fleetSummary(c fleetCounts) string {
	head := fmt.Sprintf("%d %s", c.Hosts, plural(c.Hosts, "host", "hosts"))
	if c.Current == c.Hosts {
		return head + ", all current"
	}
	parts := make([]string, 0, 4)
	for _, part := range [...]struct {
		n     int
		label string
	}{
		{c.Current, "current"},
		{c.Late, "late"},
		{c.Missing, "missing"},
		{c.Unknown, "cadence unknown"},
	} {
		if part.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.n, part.label))
		}
	}
	return head + ", " + strings.Join(parts, ", ")
}

// fleetNotes explains each row that is not current, naming the rule it failed
// and the thing to go and look at.
//
// The rule is restated inline rather than printed once as a preamble, because a
// verdict an operator cannot check is a verdict he has to trust, and this one is
// derived rather than observed. Host ids arrive from the repository already
// sanitized by fleetRecency; the sentences around them are layout.
func (a *app) fleetNotes(res fleetResult) {
	for _, h := range res.Hosts {
		switch h.State {
		case fleetLate:
			a.diagf("note: %s last published %s ago, more than %d times %s; check the archive timer on that host (babel-archive.timer on the Linux rollout)\n",
				h.Host, formatAge(h.age), lateAfterCadences, cadencePhrase(h.CadenceSource, h.cadence))
		case fleetMissing:
			a.diagf("note: %s was named with --expect but has published nothing into this repository; it has either never run \"babel archive push\" or is publishing under a different host id\n",
				h.Host)
		case fleetUnknown:
			a.diagf("note: no publication cadence could be observed for %s from its %d %s, and no other host supplies one; pass --every DURATION to say what you expect\n",
				h.Host, h.Snapshots, plural(h.Snapshots, "snapshot", "snapshots"))
		}
	}
}

// cadencePhrase names the cadence a verdict was made against as prose.
//
// The three sources need three different sentences, not one template with the
// source substituted into it: "its --every cadence" attributes the operator's
// own instruction to the host, and "its observed cadence" claims an observation
// of a host whose cadence was actually borrowed from its neighbours. A verdict
// is only checkable if the sentence explaining it is true.
func cadencePhrase(source string, cadence time.Duration) string {
	switch source {
	case cadenceFromFlag:
		return "the " + formatAge(cadence) + " cadence given with --every"
	case cadenceFromFleet:
		return "the fleet's observed cadence of " + formatAge(cadence)
	default:
		return "its own observed cadence of " + formatAge(cadence)
	}
}
