package cli

// The terminal's half of the fleet presence view (issue #118): what every
// machine in the deployment says it is running, printed under `babel conductor
// status`.
//
// It lives beside the loop's own report rather than under a command of its own
// because the question an operator opens `conductor status` to ask is "what is
// happening", and the answer stopped being local the moment a second machine
// joined the deployment. A separate `babel fleet presence` would have made the
// most common reading — this machine's loop, and the fleet around it — two
// commands with two clocks.
//
// The separation inside the output is the part that matters, and it is why the
// rows are printed under their own heading with their own explanation rather
// than merged into the cycles table. Everything above them is this machine's own
// journal: observed directly, on this filesystem, and true. Everything here is
// another machine's claim, read out of PostgreSQL, and advisory by construction.
// A single table would have made the two indistinguishable, which is precisely
// the confusion presence is easiest to cause.
//
// Nothing here classifies. internal/presence computes the heartbeat age inside
// its query and fills the freshness before the row arrives, so this file renders
// two fields it did not derive — and renders both, because a word without an age
// cannot say "last seen 4m ago; running or dead, this host cannot tell", which
// is the only honest thing a stale row can say.

import (
	"context"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/presence"
)

// presenceReader resolves the fleet presence read surface, or reports why there
// is no fleet to read.
//
// It is the only place a presence store is constructed for a command, on
// fleetReader's terms: an already-injected reader is returned as it is, which is
// what lets the rendering below be exercised without a PostgreSQL server, and
// what makes the whole terminal surface reachable from a test.
//
// The diagnostic sink is this invocation's stderr, so a store that fails to read
// says so where every other Babel diagnostic goes. It is never the command's
// result: presence is advisory, and `conductor status` must answer about the
// local loop whatever the shared catalog does.
func (a *app) presenceReader(ctx context.Context) (presence.Reader, func(), error) {
	if a.presenceRead != nil {
		return a.presenceRead, func() {}, nil
	}
	cfg, _, err := config.Load()
	if err != nil {
		return nil, func() {}, err
	}
	host, err := localHostID()
	if err != nil {
		return nil, func() {}, err
	}
	store, err := presence.Open(ctx, cfg, host, func(e error) {
		a.diagf("warning: fleet presence: %s\n", Sanitize(e.Error()))
	})
	if err != nil {
		return nil, func() {}, err
	}
	return store, func() {
		if e := store.Close(); e != nil {
			a.diagf("warning: releasing the presence catalog connection failed: %s\n",
				Sanitize(e.Error()))
		}
	}, nil
}

// conductorFleetRow is one announced run as the terminal shows it.
//
// One line per run is the whole layout rule. An operator reading this is
// answering "is anything running, and where", and a two-line row per run turns a
// busy fleet into a page nobody scans; every value here therefore has to fit a
// column, which is what bounds the fields this shape carries.
//
// Age is a rendered string rather than a number because the column is read, not
// computed against. AgeSeconds is carried beside it for --json, where a caller
// that wants to threshold has to be handed the number rather than made to parse
// "4m".
type conductorFleetRow struct {
	Host string `json:"host"`
	// Local marks this machine's own announcement. A fleet listing is the one
	// place an operator cannot tell their own run from a neighbour's by
	// context alone.
	Local bool   `json:"local"`
	Kind  string `json:"kind"`
	RunID string `json:"run_id"`
	// Recipe and PreparationID are absent rather than empty when the run
	// announced none: a cycle that has not resolved an assignment yet has no
	// recipe, and printing one would invent it.
	Recipe        string `json:"recipe,omitempty"`
	PreparationID string `json:"preparation_id,omitempty"`
	AuthorityKind string `json:"authority_kind,omitempty"`
	AuthorityRef  string `json:"authority_ref,omitempty"`
	State         string `json:"state"`
	// Freshness is internal/presence's own classification, never re-derived
	// here.
	Freshness  string `json:"freshness"`
	AgeSeconds int64  `json:"heartbeat_age_seconds"`
	Age        string `json:"heartbeat_age"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	// Receipt is the record the run committed when it finalized, absent until
	// then. It is where a finished announcement stops being presence and
	// becomes durable analysis.
	Receipt string `json:"receipt_record_id,omitempty"`
}

// presenceRows renders what the fleet announced, or the one sentence saying why
// this machine cannot see it.
//
// The two results are exclusive and both are answers. A note with no rows is not
// an error: local mode has no presence table, and an unreachable catalog means
// the fleet may be perfectly busy and this host cannot tell. Returning an error
// instead would make `conductor status` fail over a shared backend while the
// local loop it was asked about is running fine.
func (a *app) presenceRows(ctx context.Context) ([]conductorFleetRow, string) {
	reader, release, err := a.presenceReader(ctx)
	if err != nil {
		return nil, presenceNote(err)
	}
	defer release()
	rows, err := reader.Fleet(ctx)
	if err != nil {
		return nil, presenceNote(err)
	}
	out := make([]conductorFleetRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, presenceRow(row))
	}
	return out, ""
}

// presenceRow renders one row. Every string a remote machine chose passes
// through Sanitize, because a recipe id and an authority reference are values
// another host's operator typed and this one is about to print to a terminal.
func presenceRow(row presence.Row) conductorFleetRow {
	out := conductorFleetRow{
		Host:          Sanitize(row.Host),
		Local:         row.Local,
		Kind:          Sanitize(string(row.Kind)),
		RunID:         Sanitize(row.RunID),
		Recipe:        Sanitize(row.Recipe),
		PreparationID: Sanitize(row.PreparationID),
		AuthorityKind: Sanitize(string(row.Authority.Kind)),
		AuthorityRef:  Sanitize(row.Authority.Ref),
		State:         Sanitize(string(row.State)),
		Freshness:     Sanitize(string(row.Freshness)),
		AgeSeconds:    presenceAgeSeconds(row.HeartbeatAge),
		Age:           formatAge(max(row.HeartbeatAge, 0)),
		StartedAt:     formatTime(row.StartedAt),
		FinishedAt:    formatTime(row.FinishedAt),
		Receipt:       Sanitize(row.ReceiptRecordID),
	}
	return out
}

// presenceAgeSeconds refuses to report a negative age.
//
// It is reachable rather than theoretical: PostgreSQL subtracts a heartbeat
// another machine wrote from its own clock, so a writer running ahead puts a
// heartbeat in the catalog's future. Zero rather than a negative number, on the
// terms `archive fleet` already resolves the same skew: "last seen -4m ago" is
// not a fact about anything.
func presenceAgeSeconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}

// presenceNote turns a failed presence read into the sentence the report prints
// instead of rows.
//
// Three cases and three sentences, because they call for three different
// actions. Local mode is configuration and names the ceremony that changes it.
// An unreachable catalog is an outage that resolves itself, and the sentence
// says what the silence does not mean: runs elsewhere are unaffected. A catalog
// that answered and refused is a misconfiguration that does not resolve itself,
// and it must not read as an outage.
//
// The error's own text is deliberately absent from all three. A wrapped catalog
// error can carry a connection string, and the presence store already reports
// its own failures to stderr through the diagnostic sink presenceReader gave it.
func presenceNote(err error) string {
	switch {
	case presence.NotConfigured(err):
		return "this machine is in local mode, so no host announces presence to it; " +
			`configure shared mode with "babel storage configure --from-json FILE"`
	case presence.Unreachable(err):
		return "the shared catalog could not be reached, so this host cannot see what the " +
			"fleet is running; runs elsewhere are unaffected and their receipts still commit"
	default:
		return "the shared catalog refused this read, so this host cannot see what the fleet " +
			"is running; the presence table may be missing or unreadable by this machine's " +
			"catalog credential"
	}
}

// writePresenceRows prints the fleet section: the explanation, then one line per
// announced run.
//
// The explanation is printed above the table and not once at the top of the
// report, because it is what makes the rows readable and an operator scrolling
// to the bottom of a long status is exactly who needs it. It is two sentences:
// what these rows are, and what a heartbeat age does and does not establish.
//
// A stale or lost row's age carries the sentence rather than a colour, because
// Babel emits no colour and because the sentence is the honest content: "4m0s
// ago — running or dead, this host cannot tell" is a claim about evidence, where
// a red dot would be a claim about a process nobody observed.
func (a *app) writePresenceRows(rows []conductorFleetRow, note string) {
	fmt.Fprintf(a.stdout, "\nfleet presence\n")
	if note != "" {
		fmt.Fprintf(a.stdout, "  %s\n", note)
		return
	}
	if len(rows) == 0 {
		// The window is named, because an empty list means "nothing inside it"
		// and not "nothing ever". And this machine's own runs are named,
		// because the one reading most likely to be wrong is "presence is not
		// wired here" — it is, and the deployment is simply idle.
		fmt.Fprintf(a.stdout, "  no host has announced a run within the retention window (%s). "+
			"This machine's own\n  runs announce here too, so an idle list means an idle "+
			"deployment rather than unwired presence.\n", formatAge(presence.RetentionWindow))
		return
	}
	fmt.Fprintf(a.stdout, "  what every machine says it is running, read from the shared catalog. "+
		"A row is a claim,\n  not an observation: an age says when a run last spoke, never "+
		"whether it is alive now.\n")
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{
			presenceHostCell(row),
			row.Kind,
			orMissing(row.RunID),
			orMissing(row.Recipe),
			authorityLabel(row.AuthorityKind, row.AuthorityRef),
			row.State,
			presenceSeenCell(row),
			orMissing(row.Receipt),
		})
	}
	writeTable(a.stdout, []string{
		"HOST", "KIND", "RUN", "RECIPE", "AUTHORITY", "STATE", "LAST SEEN", "RECEIPT",
	}, table)
}

// presenceHostCell names the machine, marking this one. A host id is opaque and
// two machines' ids can look alike at a glance, so the marker is a word rather
// than left to the operator's memory of which id is his.
func presenceHostCell(row conductorFleetRow) string {
	if row.Local {
		return orMissing(row.Host) + thisHostSuffix
	}
	return orMissing(row.Host)
}

// presenceSeenCell is the one cell that must not lie.
//
// A finished row's age is simply how long ago it ended, which is a fact, so it
// renders as an age and nothing more. A running row's age is evidence about a
// process on another machine, so past the staleness threshold the cell says what
// the evidence does and does not establish. The threshold is not applied here:
// the freshness came from internal/presence, and this only chooses the sentence
// that matches the word.
func presenceSeenCell(row conductorFleetRow) string {
	switch row.Freshness {
	case string(presence.FreshnessStale), string(presence.FreshnessLost):
		return row.Age + " ago — running or dead, this host cannot tell"
	case string(presence.FreshnessFinished):
		return row.Age + " ago"
	default:
		return row.Age + " ago"
	}
}
