package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This file is the terminal's half of the fleet read path (SPEC.md §9, §14;
// issue #109 item 3): the commands that show what every host in a deployment
// has committed, and the two columns — host and sync state — that every local
// listing gained so that "globally browseable committed state and visibly
// pending-sync" is something an operator can see rather than something the
// specification claims.
//
// Nothing here reads a database. internal/fleet owns the reads, and this file
// takes a *fleet.Reader as an argument everywhere except in one resolver, so
// every rendering decision below — an unattributed host, a staged record, a
// record this instance cannot open, this machine's own rows among other
// machines' — is reachable in a test with no PostgreSQL server and no object
// store. That is deliberate: the rendering is where the honesty rules live,
// and a rule that can only be exercised against a live deployment is a rule
// nobody exercises.
//
// Two rules hold in every cell.
//
// Absence renders as absence. A record whose origin instance registered no
// host is labelled unattributed rather than filed under this machine; a
// column a fleet read cannot fill — a review status, an observation count,
// a candidate's priority, all of which live in local state this machine does
// not hold for another machine's record — shows missingValue rather than a
// zero that would read as a measurement.
//
// Every untrusted value passes through Sanitize exactly once, at row
// construction, exactly as `babel archive fleet` does it: a row is built from
// sanitized values and the renderer adds only layout, so no cell is sanitized
// twice and none is sanitized never.

const fleetUsage = `Usage: babel fleet <command> [flags]

Commands:
  records    list the deployment's committed analysis, host by host
  ingest     reconcile this machine's retrieval index against the fleet

Both commands require shared mode. In local mode there is no fleet, and both
say so rather than reporting an empty one.

Run "babel fleet <command> -h" for a command's flags.
`

const fleetRecordsUsage = `Usage: babel fleet records [flags]

Lists the Phase B analysis every host in this deployment has committed
(SPEC.md §9), newest commit first, with the machine that produced each record
and whether that record is globally reviewable yet.

Content is decrypted here, on this machine, with this machine's keyring. A
record sealed under a key this instance does not hold is still listed — its
host, kind, run and commit time are plaintext — and the reason it could not be
opened is shown in place of its summary rather than swallowed.

Committed records only, unless --pending asks for the staged ones too. Staged
output is not globally reviewable, so it is admitted in order to be seen as
staged and never in order to be read as committed.

The SYNC column carries one of four values, and they are four different claims:
"committed" is globally durable and reviewable, "pending-sync" is staged on the
producing host and not yet either, "local" means nothing claims the record is
going anywhere, and "unknown" means nothing here answered the question at all.
None of them stands in for another.

Flags:
  --host ID     only records this machine produced; repeatable, and each value
                may be a comma-separated list
  --kind K      only records of this kind; repeatable. One of hypothesis,
                observation, finding, proposal, link, disposition, context,
                preparation, receipt
  --pending     also list records whose run has not committed
  --limit N     page size (default 100, maximum 1000)
  --offset N    skip this many rows
  --json        emit the listing as JSON on stdout
`

const fleetIngestUsage = `Usage: babel fleet ingest [flags]

Reconciles this machine's retrieval index against the committed analysis of
every host in this deployment (issue #109 item 4), so that self-retrieval and
duplicate detection answer across machines instead of only about this one.

This writes to the retrieval index and to nothing else. The index is a
rebuildable local cache, which is the ground SPEC.md §14 settled local-only
decrypted indexing on: losing it costs a re-index and never data, no durable
record is written, and no record another host owns is copied into a table this
machine would then publish.

Each named host's partition is reconciled against that host's complete
committed set, so a wording a revision superseded stops being searchable. A
host the catalog no longer reports is forgotten, which is a cache eviction:
the records remain in PostgreSQL and Cellar, and ingesting that host again
restores every row. Narrowing with --host forgets nothing, because a caller
who asked about one machine has said nothing about the others.

Flags:
  --host ID     only ingest these machines; repeatable, and each value may be
                a comma-separated list
  --rebuild     drop every remote partition first, then ingest
  --json        emit the report as JSON on stdout
`

// unattributedHost labels a record whose origin instance has no registered
// host (sharedcatalog.FleetRecord.HostID).
//
// It is a label and never an identity: no host id can equal it, because
// config.ValidHostID requires an alphanumeric start and this string is one
// word an operator reads rather than one a filter matches. Substituting this
// machine instead would attribute one machine's analysis to another, which is
// the single failure the whole attribution path exists to prevent.
const unattributedHost = "unattributed"

// thisHostLabel marks the rows this machine produced, among rows every other
// machine produced. A fleet listing is the one place an operator cannot tell
// their own analysis from a neighbour's by context alone.
const (
	thisHostLabel  = "(this host)"
	thisHostSuffix = " " + thisHostLabel
)

// syncJournal answers what this machine still owes the shared catalog.
//
// It is declared here with one method rather than imported from the package
// that implements it, for internal/fleet's reason restated one layer up: the
// publisher depends on the read path's vocabulary and not the other way round,
// and Go's structural typing makes the seam free.
type syncJournal interface {
	SyncState(ctx context.Context, entityID string) (string, error)
}

// fleetReader resolves the shared-catalog read surface, or reports why there
// is no fleet to read.
//
// It is the only place a *fleet.Reader is constructed. An already-injected
// reader is returned as it is, which is what lets a caller — a test, or a
// command that resolved one already — drive every surface below without a
// server.
func (a *app) fleetReader(ctx context.Context) (*fleet.Reader, error) {
	if a.fleetRead != nil {
		return a.fleetRead, nil
	}
	// An unconfigured machine loads as the zero configuration, which
	// fleet.OpenReader reports as ErrNotConfigured. Branching on `found` here
	// would produce a second sentence for the same fact.
	cfg, _, err := config.Load()
	if err != nil {
		return nil, err
	}
	host, err := localHostID()
	if err != nil {
		return nil, err
	}
	return fleet.OpenReader(ctx, cfg, host)
}

// fleetUnavailable renders a fleet read that could not be attempted.
//
// The branch is fleet.NotConfigured, which is the one predicate that tells the
// two cases apart, and the distinction is the whole point. No fleet at all is
// configuration: there is nothing to read, and the answer is one sentence
// naming the command that would create one — collapsed on purpose, because an
// operator who has not configured shared mode does not need to read that a
// keyring, an object store and a catalog connection are each missing. Anything
// else is a real failure — a catalog that is down, a key document that is
// wrong — and it is surfaced as it arrived, because degrading it into "there is
// no fleet" would turn an outage into an answer.
func fleetUnavailable(err error) error {
	if fleet.NotConfigured(err) {
		return errors.New(`this machine is in local mode, so there is no fleet to read: ` +
			`configure shared mode with "babel storage configure --from-json FILE"`)
	}
	return err
}

// closeReader releases a catalog connection this invocation opened.
//
// A close that fails is a diagnostic and not the command's result: the listing
// above it already answered correctly, and reporting failure afterwards would
// make a connection teardown look like a wrong answer. A reader that borrowed
// its connection closes nothing, so this is safe on either kind.
func (a *app) closeReader(reader *fleet.Reader) {
	if err := reader.Close(); err != nil {
		a.diagf("warning: releasing the shared catalog connection failed: %s\n", Sanitize(err.Error()))
	}
}

// fleetFlags is the one flag that makes a local listing cross machines.
//
// It is opt-in and stays opt-in. A listing that silently included another
// host's records would make every count, every absence and every "nothing
// here" ambiguous about which machine it described, so an operator has to ask.
type fleetFlags struct{ fleetWide bool }

func (ff *fleetFlags) bind(c *cmd) {
	c.fs.BoolVar(&ff.fleetWide, "fleet", false,
		"also list the other hosts' committed records for this kind")
}

// fleetRecordRow is one fleet record as a terminal and a script see it.
//
// The same shape serves `babel fleet records` and the fleet half of every
// local listing, so a script that reads one reads the other, and the two
// surfaces cannot drift into two answers about which host a record came from.
type fleetRecordRow struct {
	// Host is the label to show: the operator-assigned display name if the
	// host asserted one, else the host id, else unattributedHost.
	Host string `json:"host"`
	// HostAttributed says whether Host names a machine or reports the absence
	// of one. A script filtering by host needs it, because the unattributed
	// label occupies the same field as a real id and is not one.
	HostAttributed bool `json:"host_attributed"`
	// ThisHost marks a record this machine produced, which is what the
	// terminal's "(this host)" suffix says and what a script would otherwise
	// have to rediscover by comparing Host against a host id it has to know.
	ThisHost bool `json:"this_host"`
	// Sync is the record's state in the shared vocabulary, verbatim and never
	// re-derived here: "committed", "pending-sync", "local", or "unknown". It
	// is resolved once, by fleet.Reader.SyncStates or fleet.LocalSyncStates,
	// and it is never empty — a blank in a closed vocabulary would be a fifth
	// value meaning nothing, so a state nothing answered for is "unknown".
	Sync     string `json:"sync"`
	Kind     string `json:"kind"`
	RecordID string `json:"record_id"`
	RunID    string `json:"run_id"`
	// Status is a candidate's lifecycle state at the moment it was staged for
	// publication and is absent for every other kind. It is a snapshot of a
	// remote machine's state at commit time, which is what
	// frontier.PublishedRecord documents it as and the only reading available
	// without asking the owning host.
	Status string `json:"status,omitempty"`
	// SubjectType and SubjectID name the record a review answer answers about,
	// absent for every other kind. They are what makes a fleet-wide review
	// listing joinable to the record under review.
	SubjectType string `json:"subject_type,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`
	// CommittedAt is when the record became globally reviewable, absent while
	// its run is pending.
	CommittedAt string `json:"committed_at,omitempty"`
	// Summary is the record in one line, derived by exactly the code the local
	// index uses (frontier.PublishedRecord.Output), and absent for a kind that
	// has no searchable output or a record this instance could not open.
	Summary string `json:"summary,omitempty"`
	// Unopened is why this instance could not read the record's content: a key
	// it does not hold, a payload shape from a newer build, a store failure.
	// It is carried rather than reduced to a boolean because the three call
	// for different remedies, and a row that said only "unavailable" would
	// send an operator looking in the wrong place.
	Unopened string `json:"unopened,omitempty"`
}

// hostCell renders the machine column.
func (r fleetRecordRow) hostCell() string {
	if r.ThisHost {
		return r.Host + thisHostSuffix
	}
	return r.Host
}

// summaryCell renders the last column: the record in one line, or the reason
// there is no line, or absence.
//
// The reason is labelled rather than printed bare. A failure to open sits in
// the same column as a model's own sentence, and an unlabelled "key kid-2 is
// not held by this instance" would read as something a record said.
func (r fleetRecordRow) summaryCell() string {
	if r.Unopened != "" {
		return "unopened: " + r.Unopened
	}
	return orMissing(r.Summary)
}

// fleetRecordRows renders one page of fleet records.
//
// It is pure, and that is the point: the records, their resolved sync states
// and this machine's host id are all arguments, so a test builds the fleet
// cases a healthy single-host deployment cannot produce and asserts what they
// render as.
//
// states comes from fleet.Reader.SyncStates or fleet.LocalSyncStates, which is
// the one place that resolution lives. Three fallbacks in order, and the last
// one is the point: a record the map does not name falls back to the run's own
// state from the catalog row, and a row that carries none falls back to
// fleet.SyncUnknown — never to one of the other three. "committed" would claim
// durability nobody verified, "pending-sync" would promise a sync in progress,
// and "local" would say nothing is carrying the record anywhere, which with
// the authority unanswered is the one thing nothing observed (SPEC.md §3: an
// absent value carries a reason rather than being replaced by a plausible one).
func fleetRecordRows(records []fleet.Record, states map[string]string, localHost string) []fleetRecordRow {
	rows := make([]fleetRecordRow, 0, len(records))
	for _, rec := range records {
		host, attributed := fleetHostLabel(rec.FleetRecord)
		state, resolved := states[rec.Record.RecordID]
		if !resolved {
			state = rec.SyncState
		}
		if state == "" {
			state = fleet.SyncUnknown
		}
		row := fleetRecordRow{
			Host:           host,
			HostAttributed: attributed,
			ThisHost:       attributed && localHost != "" && rec.HostID == localHost,
			Sync:           Sanitize(state),
			Kind:           Sanitize(string(rec.Record.Kind)),
			RecordID:       Sanitize(rec.Record.RecordID),
			RunID:          Sanitize(rec.Record.RunID),
			CommittedAt:    formatTimePtr(rec.CommittedAt),
		}
		row.Summary, row.Unopened = recordSummary(rec)
		if rec.Published != nil {
			row.Status = Sanitize(string(rec.Published.Status))
			row.SubjectType = Sanitize(string(rec.Published.Subject.Type))
			row.SubjectID = Sanitize(rec.Published.Subject.ID)
		}
		rows = append(rows, row)
	}
	return rows
}

// fleetHostLabel resolves which machine a record is filed under, and whether
// it is filed under one at all.
//
// Attribution is keyed on the host id and never on the display name: a name is
// a label the operator assigned for reading, and a record whose origin
// instance registered no host has no name to assign.
func fleetHostLabel(rec sharedcatalog.FleetRecord) (label string, attributed bool) {
	if rec.HostID == "" {
		return unattributedHost, false
	}
	if rec.HostDisplayName != "" {
		return Sanitize(rec.HostDisplayName), true
	}
	return Sanitize(rec.HostID), true
}

// recordSummary derives one record's one-line summary, or the reason it has
// none.
//
// Three outcomes, and each is a different fact. A record this instance could
// not open reports why. A record whose kind has no searchable output — a
// receipt, a preparation, a proposal, a link, an operator context note — has
// no summary and no failure, which is normal rather than exceptional and so is
// absence rather than a reason. Anything else is the summary the local
// retrieval index would show for the identical record, because it comes from
// the same derivation.
func recordSummary(rec fleet.Record) (summary, unopened string) {
	if rec.Unopened != "" {
		return "", Sanitize(rec.Unopened)
	}
	if rec.Published == nil {
		return "", ""
	}
	out, err := rec.Published.Output()
	switch {
	case errors.Is(err, frontier.ErrNotSearchable):
		return "", ""
	case err != nil:
		return "", Sanitize(err.Error())
	}
	return Sanitize(out.Summary), ""
}

// fleetRecordCounts is the one-line answer to "what does the fleet hold".
type fleetRecordCounts struct {
	// Hosts counts the attributed machines on this page. The unattributed
	// group is deliberately not one of them: it is not a machine.
	Hosts   int `json:"hosts"`
	Records int `json:"records"`
	// Pending counts records staged but not yet globally reviewable, which is
	// always zero unless --pending admitted them.
	Pending int `json:"pending"`
	// Unattributed counts records whose origin instance registered no host.
	// The remedy is one push from the owning machine, and an operator cannot
	// ask for it without knowing the number.
	Unattributed int `json:"unattributed"`
	// Unopened counts records this instance could not decrypt or decode.
	Unopened int `json:"unopened"`
	// Unknown counts records whose sync state nothing answered for, which is
	// reported rather than folded into the others because it is the one state
	// that says the page is incomplete about itself.
	Unknown int `json:"unknown"`
}

func countFleetRecords(rows []fleetRecordRow) fleetRecordCounts {
	var counts fleetRecordCounts
	hosts := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		counts.Records++
		if row.HostAttributed {
			hosts[row.Host] = struct{}{}
		} else {
			counts.Unattributed++
		}
		switch row.Sync {
		case sharedcatalog.SyncPending:
			counts.Pending++
		case fleet.SyncUnknown:
			counts.Unknown++
		}
		if row.Unopened != "" {
			counts.Unopened++
		}
	}
	counts.Hosts = len(hosts)
	return counts
}

// fleetRecordsSummary is the line that answers the question by itself, so the
// table below it is confirmation rather than work. Zero counts are omitted,
// which is fleetSummary's rule: the states present are the states named, and a
// wholly healthy page says so instead of printing three numbers a reader has
// to check are zero.
func fleetRecordsSummary(c fleetRecordCounts) string {
	head := fmt.Sprintf("%d %s, %d %s",
		c.Hosts, plural(c.Hosts, "host", "hosts"),
		c.Records, plural(c.Records, "record", "records"))
	parts := make([]string, 0, 4)
	for _, part := range [...]struct {
		n     int
		label string
	}{
		{c.Pending, sharedcatalog.SyncPending},
		{c.Unknown, fleet.SyncUnknown + " sync state"},
		{c.Unattributed, unattributedHost},
		{c.Unopened, "unopened"},
	} {
		if part.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.n, part.label))
		}
	}
	if len(parts) == 0 {
		return head + ", all committed and readable"
	}
	return head + ", " + strings.Join(parts, ", ")
}

// fleetRecordsResult is `babel fleet records --json`.
type fleetRecordsResult struct {
	Records []fleetRecordRow  `json:"records"`
	Counts  fleetRecordCounts `json:"counts"`
	// LocalHost names the machine this listing was rendered on, so that
	// this_host stays interpretable in a stored document: a mark meaning
	// "mine" is only readable beside the identity it referred to.
	LocalHost string `json:"local_host"`
	// Limit is the page size that was actually applied rather than the one
	// asked for, because a zero request means the store's default and a
	// document echoing zero would not describe the page it carries.
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// fleetCmd routes `babel fleet <verb>`.
func (a *app) fleetCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "fleet requires a subcommand", usage: fleetUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, fleetUsage)
		return nil
	case "records":
		return a.fleetRecords(ctx, args[1:])
	case "ingest":
		return a.fleetIngest(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown fleet subcommand %q", args[0]), usage: fleetUsage}
	}
}

// fleetRecords implements `babel fleet records`.
func (a *app) fleetRecords(ctx context.Context, args []string) error {
	c := newCmd("fleet records", fleetRecordsUsage)
	// --host is a filter here, which is the opposite of what it means on every
	// other command: `babel archive fleet` deliberately defines no --host at
	// all (see the comment on archiveFleet) because on the archive commands
	// that flag names the identity this machine publishes as, and a filter
	// wearing that spelling would be a trap. This command is new, so the
	// spelling carries no prior meaning to contradict, and the thing an
	// operator wants to narrow a fleet read by is exactly a host.
	var hosts, kinds repeatedFlag
	c.fs.Var(&hosts, "host", "only records this machine produced; repeatable, comma-separated accepted")
	c.fs.Var(&kinds, "kind", "only records of this kind; repeatable")
	pending := c.fs.Bool("pending", false, "also list records whose run has not committed")
	var pf pageFlags
	pf.bindPage(c)
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	hostIDs, err := parseHostIDs(c, "--host", hosts)
	if err != nil {
		return err
	}
	recordKinds, err := parseRecordKinds(c, kinds)
	if err != nil {
		return err
	}

	reader, err := a.fleetReader(ctx)
	if err != nil {
		return fleetUnavailable(err)
	}
	defer a.closeReader(reader)

	filter := sharedcatalog.RecordFilter{
		Hosts:          hostIDs,
		Kinds:          recordKinds,
		IncludePending: *pending,
		Limit:          pf.limit,
		Offset:         pf.offset,
	}
	records, err := reader.RecordsWithContent(ctx, filter)
	if err != nil {
		return err
	}
	ids := recordIDs(records)
	states, err := reader.SyncStates(ctx, a.journal, ids)
	if err != nil {
		return err
	}
	rows := fleetRecordRows(records, states, reader.LocalHost())

	res := fleetRecordsResult{
		Records:   rows,
		Counts:    countFleetRecords(rows),
		LocalHost: Sanitize(reader.LocalHost()),
		Limit:     effectiveRecordLimit(pf.limit),
		Offset:    pf.offset,
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.reportFleetRecords(res)
}

// recordIDs is the id list a sync-state resolution is asked about, in the
// order the records arrived.
func recordIDs(records []fleet.Record) []string {
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.Record.RecordID)
	}
	return ids
}

// effectiveRecordLimit reports the page size a fleet read actually applied.
// The bounds belong to sharedcatalog, which clamps rather than honours an
// out-of-range request, so this mirrors that clamp instead of imposing a
// second one.
func effectiveRecordLimit(limit int) int {
	switch {
	case limit <= 0:
		return sharedcatalog.DefaultRecordLimit
	case limit > sharedcatalog.MaxRecordLimit:
		return sharedcatalog.MaxRecordLimit
	}
	return limit
}

// reportFleetRecords writes the fleet listing for a terminal.
//
// The summary comes first and the per-row prose goes to stderr, which is how
// `babel archive fleet` reports a fleet: a note is a sentence about a
// condition, and folding it into the table would push the columns apart for
// the one row that needed explaining.
func (a *app) reportFleetRecords(res fleetRecordsResult) error {
	if len(res.Records) == 0 {
		fmt.Fprint(a.stdout, "no committed record in this deployment matches this filter\n")
		a.diagf("note: --pending also lists records that are staged but not yet globally reviewable\n")
		return nil
	}
	fmt.Fprintf(a.stdout, "fleet: %s\n\n", fleetRecordsSummary(res.Counts))
	rows := make([][]string, 0, len(res.Records))
	for _, row := range res.Records {
		rows = append(rows, []string{
			row.hostCell(),
			// Never orMissing: fleetRecordRows guarantees a value from the
			// closed vocabulary, and a dash here would be a fifth state.
			row.Sync,
			orMissing(row.Kind),
			orMissing(row.RecordID),
			orMissing(row.RunID),
			orMissing(row.CommittedAt),
			row.summaryCell(),
		})
	}
	if err := writeTable(a.stdout,
		[]string{"HOST", "SYNC", "KIND", "RECORD", "RUN", "COMMITTED", "SUMMARY"},
		rows); err != nil {
		return err
	}
	a.fleetRecordNotes(res)
	return nil
}

// fleetRecordNotes explains the conditions the table can only name.
//
// Each note states the rule rather than repeating the row. The SYNC column
// already says which records are staged; what an operator needs beside it is
// what "pending-sync" costs them, which is that the record is not globally
// reviewable yet (SPEC.md §9). Naming the rule once is deliberate: a sentence
// per staged row would be a hundred identical sentences on a page of a hundred
// staged records, and a diagnostic that always fires is one nobody reads.
func (a *app) fleetRecordNotes(res fleetRecordsResult) {
	if res.Counts.Pending > 0 {
		a.diagf("note: %d %s marked %s %s staged on the producing host and not yet globally reviewable (SPEC.md §9)\n",
			res.Counts.Pending, plural(res.Counts.Pending, "record", "records"),
			sharedcatalog.SyncPending, plural(res.Counts.Pending, "is", "are"))
	}
	if res.Counts.Unknown > 0 {
		a.diagf("note: %d %s sync state reads %s: nothing here answered whether %s durable off this machine, so the page is incomplete about %s rather than guessing\n",
			res.Counts.Unknown, plural(res.Counts.Unknown, "record's", "records'"),
			fleet.SyncUnknown,
			plural(res.Counts.Unknown, "it is", "they are"),
			plural(res.Counts.Unknown, "it", "them"))
	}
	if res.Counts.Unattributed > 0 {
		a.diagf("note: %d %s %s %s: the instance that produced %s registered no host, so this machine will not file %s under any host id; the owning machine's next publication names it\n",
			res.Counts.Unattributed, plural(res.Counts.Unattributed, "record", "records"),
			plural(res.Counts.Unattributed, "reads", "read"), unattributedHost,
			plural(res.Counts.Unattributed, "it", "them"),
			plural(res.Counts.Unattributed, "it", "them"))
	}
	if res.Counts.Unopened > 0 {
		a.diagf("note: %d %s could not be opened on this machine; the SUMMARY column carries each reason, which is a key to install, a binary to update, or an object store to check\n",
			res.Counts.Unopened, plural(res.Counts.Unopened, "record", "records"))
	}
	if len(res.Records) >= res.Limit {
		a.diagf("note: this page is full at %d %s; pass --offset %d for the next one\n",
			res.Limit, plural(res.Limit, "row", "rows"), res.Offset+len(res.Records))
	}
}

// parseHostIDs validates a host-id flag: repeatable, and each value may itself
// be a comma-separated list, which is unambiguous because config.ValidHostID
// admits no comma.
//
// A malformed id is rejected rather than reported as a host that holds
// nothing: an empty result must mean "that machine has committed no analysis",
// and a typo that cannot be a host id at all would make it mean "you mistyped
// something", which is a different answer wearing the same words.
func parseHostIDs(c *cmd, flagName string, values []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !validHostID(part) {
				return nil, c.usagef("invalid %s host %q: host ids are 1-%d characters of [a-z0-9._-] starting alphanumeric",
					flagName, part, maxHostIDLen)
			}
			if _, dup := seen[part]; dup {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out, nil
}

// recordKindVocabulary is the closed set of Phase B record kinds the shared
// catalog carries. The values come from the package that owns them, so a
// renamed kind cannot drift past the compiler; migrations/0003's CHECK is what
// keeps the set closed, so a kind added there costs an entry here and the
// review that comes with editing this list.
var recordKindVocabulary = []sharedcatalog.RecordKind{
	sharedcatalog.KindHypothesis, sharedcatalog.KindObservation,
	sharedcatalog.KindFinding, sharedcatalog.KindProposal,
	sharedcatalog.KindLink, sharedcatalog.KindDisposition,
	sharedcatalog.KindContext, sharedcatalog.KindPreparation,
	sharedcatalog.KindReceipt,
}

// parseRecordKinds validates --kind against that vocabulary, reading the
// vocabulary from the package that owns it so the rejection message cannot
// drift from what the catalog accepts.
func parseRecordKinds(c *cmd, values []string) ([]sharedcatalog.RecordKind, error) {
	var out []sharedcatalog.RecordKind
	for _, value := range values {
		kind := sharedcatalog.RecordKind(value)
		if !slices.Contains(recordKindVocabulary, kind) {
			names := make([]string, 0, len(recordKindVocabulary))
			for _, known := range recordKindVocabulary {
				names = append(names, string(known))
			}
			return nil, c.usagef("unknown --kind %q (want one of %s)", value, strings.Join(names, ", "))
		}
		if slices.Contains(out, kind) {
			continue
		}
		out = append(out, kind)
	}
	return out, nil
}

// fleetIngestHostRow is what one host's index partition became.
type fleetIngestHostRow struct {
	Host string `json:"host"`
	// Records is how many of that host's records were offered to the index,
	// which is Added plus Updated plus Skipped and is reported so a reader can
	// see that the three account for the whole set.
	Records int `json:"records"`
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
	Skipped int `json:"skipped"`
	// Foreign counts records this pass declined because another origin already
	// holds them, which is the normal outcome for this machine's own published
	// records coming back from the catalog.
	Foreign int `json:"foreign"`
}

// fleetIngestResult is `babel fleet ingest --json`.
type fleetIngestResult struct {
	Hosts []fleetIngestHostRow `json:"hosts"`
	// Forgotten names the partitions dropped because the catalog no longer
	// reports records for those hosts. A cache eviction, never a loss.
	Forgotten []string `json:"forgotten,omitempty"`
	// Unattributed counts committed records skipped because their origin
	// instance registered no host, so there is no partition to file them
	// under.
	Unattributed int `json:"unattributed"`
	// Unopened carries one reason per record whose content could not be read.
	// Each costs one record and never the ingest.
	Unopened []string `json:"unopened,omitempty"`
	Rebuilt  bool     `json:"rebuilt"`
}

// fleetIngest implements `babel fleet ingest`.
func (a *app) fleetIngest(ctx context.Context, args []string) error {
	c := newCmd("fleet ingest", fleetIngestUsage)
	var hosts repeatedFlag
	c.fs.Var(&hosts, "host", "only ingest these machines; repeatable, comma-separated accepted")
	rebuild := c.fs.Bool("rebuild", false, "drop every remote partition first, then ingest")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	hostIDs, err := parseHostIDs(c, "--host", hosts)
	if err != nil {
		return err
	}

	reader, err := a.fleetReader(ctx)
	if err != nil {
		return fleetUnavailable(err)
	}
	defer a.closeReader(reader)

	d, err := babelDirs()
	if err != nil {
		return err
	}
	idx, err := index.Open(d.indexDir())
	if err != nil {
		return err
	}
	defer idx.Close()

	report, err := reader.Ingest(ctx, idx, fleet.IngestOptions{Hosts: hostIDs, Rebuild: *rebuild})
	if err != nil {
		return err
	}
	res := fleetIngestResult{
		Hosts:        make([]fleetIngestHostRow, 0, len(report.Hosts)),
		Forgotten:    sanitizeAll(report.Forgotten),
		Unattributed: report.Unattributed,
		Unopened:     sanitizeAll(report.Unopened),
		Rebuilt:      *rebuild,
	}
	for _, host := range sortedUnique(ingestHostNames(report)) {
		result := report.Hosts[host]
		res.Hosts = append(res.Hosts, fleetIngestHostRow{
			Host:    Sanitize(host),
			Records: result.Records,
			Added:   result.Added,
			Updated: result.Updated,
			Removed: result.Removed,
			Skipped: result.Skipped,
			Foreign: result.Foreign,
		})
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.reportFleetIngest(res)
}

// ingestHostNames is the report's host keys, which a map does not order.
func ingestHostNames(report fleet.IngestReport) []string {
	names := make([]string, 0, len(report.Hosts))
	for host := range report.Hosts {
		names = append(names, host)
	}
	return names
}

// reportFleetIngest writes the ingest report for a terminal.
//
// The closing sentence is part of the report rather than a diagnostic, because
// it is the fact that makes the command safe to run at any time and an
// operator reading a rebuild's output is exactly the person who needs to know
// that nothing was lost (SPEC.md §14).
func (a *app) reportFleetIngest(res fleetIngestResult) error {
	if len(res.Hosts) == 0 {
		fmt.Fprint(a.stdout, "no other host in this deployment has committed searchable analysis\n")
	} else {
		rows := make([][]string, 0, len(res.Hosts))
		for _, host := range res.Hosts {
			rows = append(rows, []string{
				host.Host,
				fmt.Sprint(host.Records), fmt.Sprint(host.Added), fmt.Sprint(host.Updated),
				fmt.Sprint(host.Removed), fmt.Sprint(host.Skipped), fmt.Sprint(host.Foreign),
			})
		}
		if err := writeTable(a.stdout,
			[]string{"HOST", "RECORDS", "ADDED", "UPDATED", "REMOVED", "SKIPPED", "FOREIGN"},
			rows); err != nil {
			return err
		}
	}
	detail := [][2]string{
		{"unattributed", fmt.Sprint(res.Unattributed)},
		{"unopened", fmt.Sprint(len(res.Unopened))},
	}
	if len(res.Forgotten) > 0 {
		detail = append(detail, [2]string{"forgotten", strings.Join(res.Forgotten, " ")})
	}
	fmt.Fprint(a.stdout, "\n")
	if err := writeDetail(a.stdout, detail); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nthis reconciled the local retrieval index and nothing else: "+
		"no durable record was written, and losing this cache costs a re-index and never data\n")
	for _, reason := range res.Unopened {
		a.diagf("note: %s\n", reason)
	}
	if res.Unattributed > 0 {
		a.diagf("note: %d committed %s %s %s and could not be filed under any host, so %s not searchable here; the owning machine's next publication names its host\n",
			res.Unattributed, plural(res.Unattributed, "record", "records"),
			plural(res.Unattributed, "reads", "read"), unattributedHost,
			plural(res.Unattributed, "it is", "they are"))
	}
	return nil
}

// fleetListingReader resolves the reader a local listing renders with, and the
// release that hands its connection back. The reader is nil in the ordinary
// case and a real one when the operator asked to cross machines; the release is
// always safe to call.
//
// A plain listing deliberately does not dial the shared catalog. `babel
// hypotheses` has to answer with PostgreSQL unreachable — that is exactly when
// an operator asks what this machine holds — and a sync column that made the
// listing fail would be a column that cost more than it told. With --fleet the
// operator has asked for the fleet, so the catalog is the authority and its
// absence is the answer.
//
// Only a reader this call opened is closed. An injected one belongs to whoever
// injected it, and closing a caller's pool from underneath it is how a
// long-lived process loses its catalog.
func (a *app) fleetListingReader(ctx context.Context, fleetWide bool) (*fleet.Reader, func(), error) {
	if a.fleetRead != nil {
		return a.fleetRead, func() {}, nil
	}
	if !fleetWide {
		return nil, func() {}, nil
	}
	reader, err := a.fleetReader(ctx)
	if err != nil {
		return nil, func() {}, err
	}
	return reader, func() { a.closeReader(reader) }, nil
}

// syncColumn resolves the per-record sync state a local listing renders
// (SPEC.md §9), keyed by the sanitized record id the listing prints so that
// the column and the rows agree on one identity.
//
// With no reader the answer is the journal's alone, which is what local mode
// can honestly say: a record nothing claims is going anywhere is "local", and
// never "pending-sync", because nothing is going to carry it.
func (a *app) syncColumn(ctx context.Context, reader *fleet.Reader, ids []string) (map[string]string, error) {
	var states map[string]string
	var err error
	if reader == nil {
		states, err = fleet.LocalSyncStates(ctx, a.journal, ids)
	} else {
		states, err = reader.SyncStates(ctx, a.journal, ids)
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(states))
	for id, state := range states {
		out[Sanitize(id)] = Sanitize(state)
	}
	return out, nil
}

// syncCell renders one row's sync state from that column.
//
// An id the resolution did not answer for reads as fleet.SyncUnknown and never
// as absence. The SYNC column's vocabulary is closed and every value in it is a
// claim; a dash there would be a fifth value meaning nothing, and picking any
// of the other three would state something nothing observed.
func syncCell(states map[string]string, id string) string {
	if state := states[id]; state != "" {
		return state
	}
	return fleet.SyncUnknown
}

// fleetListingRows fetches the other hosts' committed records of the given
// kinds, for a local listing that was asked to cross machines. It reports the
// page it fetched alongside the rows it kept, so a caller can say whether more
// exist.
//
// This machine's own rows are skipped, and so is any record the local half
// already rendered. Both exclusions are one rule from two directions: a record
// appears once, and where both copies exist the durable store is the better
// one — it is current where a published record is a snapshot, which is the same
// reason internal/index breaks an origin tie in favour of the local partition.
func (a *app) fleetListingRows(ctx context.Context, reader *fleet.Reader,
	kinds []sharedcatalog.RecordKind, shown []string, limit int) (rows []fleetRecordRow, fetched int, err error) {
	records, err := reader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{
		Kinds: kinds,
		Limit: limit,
	})
	if err != nil {
		return nil, 0, err
	}
	local := reader.LocalHost()
	seen := make(map[string]struct{}, len(shown))
	for _, id := range shown {
		seen[id] = struct{}{}
	}
	keep := make([]fleet.Record, 0, len(records))
	for _, rec := range records {
		if local != "" && rec.HostID == local {
			continue
		}
		if _, dup := seen[rec.Record.RecordID]; dup {
			continue
		}
		keep = append(keep, rec)
	}
	states, err := reader.SyncStates(ctx, a.journal, recordIDs(keep))
	if err != nil {
		return nil, 0, err
	}
	return fleetRecordRows(keep, states, local), len(records), nil
}

// localHostCell labels a row this machine's own durable store produced, for a
// listing that grew a HOST column because it crossed machines.
//
// A reader carrying no host id still describes this machine, so the row is
// marked as this machine's rather than attributed to an id nothing here knows.
func localHostCell(reader *fleet.Reader) string {
	if reader == nil {
		return thisHostLabel
	}
	if id := reader.LocalHost(); id != "" {
		return Sanitize(id) + thisHostSuffix
	}
	return thisHostLabel
}

// fleetListingNote states that a listing crossed machines, and how much of it
// did.
//
// The HOST column already says which rows are whose. This says the set is not
// this machine's alone, because the failure mode is an operator who read past
// the header and took a neighbour's analysis for local work.
func (a *app) fleetListingNote(rows []fleetRecordRow, fetched, limit int) {
	if len(rows) == 0 {
		a.diagf("note: --fleet found no committed record on any other host in this deployment\n")
		return
	}
	a.diagf("note: %d of the rows above came from other hosts in this deployment, marked in the HOST column; every column this machine cannot fill for another machine's record reads as absent\n",
		len(rows))
	if effective := effectiveRecordLimit(limit); fetched >= effective {
		a.diagf("note: the fleet half of this listing is full at %d %s; narrow it with --host or page the local half with --offset\n",
			effective, plural(effective, "row", "rows"))
	}
}

// reviewableRecordKinds maps a review listing's own type vocabulary onto the
// shared catalog's.
//
// The two vocabularies coincide by construction rather than by coincidence:
// both name §4's record kinds, and frontier.EntityType and
// sharedcatalog.RecordKind hold the same strings for the kinds §6.7 makes
// reviewable. The mapping is written out anyway, so a divergence is a compile
// error here instead of an empty listing somewhere else.
func reviewableRecordKinds(entity frontier.EntityType) []sharedcatalog.RecordKind {
	switch entity {
	case frontier.EntityHypothesis:
		return []sharedcatalog.RecordKind{sharedcatalog.KindHypothesis}
	case frontier.EntityObservation:
		return []sharedcatalog.RecordKind{sharedcatalog.KindObservation}
	case frontier.EntityFinding:
		return []sharedcatalog.RecordKind{sharedcatalog.KindFinding}
	case frontier.EntityProposal:
		return []sharedcatalog.RecordKind{sharedcatalog.KindProposal}
	}
	return []sharedcatalog.RecordKind{
		sharedcatalog.KindHypothesis, sharedcatalog.KindFinding, sharedcatalog.KindProposal,
	}
}
