// Package presence answers one question Babel could not answer off-host: what
// is running where, right now, from any machine in the fleet (issue #118).
//
// Everything else about a run already travels. Records, receipts, dispositions
// and the frontier all sync fleet-wide, publication is serialized by host
// leases, and cross-host dedup makes concurrent conductors safe - so an
// operator at any machine can read what any other machine produced. What they
// could not read was the interval before that: a conductor cycle or an explore
// run is invisible off-host until its receipt commits, which can be an hour of
// model work later. From another machine an entire working fleet looked
// identical to an idle one.
//
// # Rows live only in PostgreSQL, and that is deliberate
//
// Babel's Phase B discipline is object-first, database-last: a record's payload
// is sealed with internal/envelope, written to the object store, read back and
// verified, and only then does a PostgreSQL row name it. That exists because a
// hypothesis, finding or receipt exists nowhere but Babel.
//
// A presence row is the opposite kind of fact, so it takes the opposite
// treatment. It says "this run reported itself alive at this instant"; it is
// worthless minutes after the process it describes exited; and it is
// recoverable in the only sense that matters, because the run carries on
// regardless and its receipt commits through the normal protocol. An
// object-store leg would spend money and latency durably preserving a statement
// whose entire value is that it is current, and a local durable copy would
// preserve it on the one machine that never needed to be told. So this package
// writes to the shared PostgreSQL catalog and nowhere else: no object write, no
// local file, no state beyond the in-process PresenceID a writer holds. It is
// the one place in Babel where PG-only is the correct storage, and it is
// correct precisely because the data is disposable.
//
// # Presence is advisory, never fact
//
// A row records that a process said something once. It cannot record that the
// process is still there, because no observer on another machine can know that:
// a missing heartbeat means the run is slow, the network is down, the machine
// slept, or the process died, and PostgreSQL cannot tell those apart. So this
// package never reports liveness as a fact. Fleet classifies each row by the
// age of its heartbeat (see Classify) and hands back both the classification
// and the raw age, so a render surface can say "last seen 7m ago; running or
// dead, this host cannot tell" - which is more information than any confident
// answer would be.
//
// There is no reaper, and that is a decision rather than a gap. Writers never
// delete a row, nothing sweeps stale ones, and Fleet simply stops returning
// rows older than RetentionWindow. A reaper would be a process asserting a
// death it cannot observe, and it would replace an honest "last seen 40m ago"
// with the strictly weaker "nothing here".
//
// # Best-effort by construction
//
// A presence write may never block, fail, or slow the run it describes. That is
// enforced in three ways rather than promised: every statement runs under a
// short timeout (Options.Timeout), every failure goes to Options.Diag and
// nothing else - the same contract internal/sync's publisher holds - and a
// failed Announce returns an empty PresenceID, which makes every later
// Heartbeat and Finalize a no-op. An unreachable PostgreSQL therefore means the
// run proceeds exactly as it would have, invisible to the fleet, and says so in
// one diagnostic line.
//
// A nil *Store is the same answer for a machine in local mode: every method is
// a no-op. Wiring sites also check the interface for nil, because a nil *Store
// in an Announcer interface is not a nil interface.
//
// # What this package deliberately does not carry
//
// Archive-push freshness is not announced here. It is already in the shared
// catalog: `snapshots` rows carry each publication with its time and its
// host, and `instances.last_seen_at` records when an instance last registered.
// A second, weaker copy of that under a heartbeat would be a presence row
// pretending to know something the push rows already know exactly, so a fleet
// surface reads those and this package adds nothing for it.
//
// Remote control is out of scope by operator decision (#118): nothing here
// starts, stops or steers a run on another machine. This table is state to
// read, and the interconnection layer that owns cross-machine invocation is
// atyrode/manifold.
//
// No content of any kind reaches these rows - see the migration
// (internal/sharedcatalog/migrations/0009_fleet_presence.sql) for the column by
// column argument. A presence row is readable by a host holding only the
// catalog credential and no payload key (#112), which is what makes a fleet
// view work on a machine that can open none of the records it lists.
package presence

import (
	"context"
	"time"

	"github.com/atyrode/babel/internal/run"
)

// Kind names what announced. A conductor cycle and an explore run are
// different facts about the fleet - one is a loop deciding, the other is work
// happening - and a reader that could not tell them apart would render a
// scheduling tick as analysis. A single conductor cycle therefore produces two
// rows, its own and its run's, which is the honest count: the loop can be alive
// while the run it started is not.
type Kind string

// The two kinds that announce.
const (
	KindConductor Kind = "conductor"
	KindExplore   Kind = "explore"
)

func (k Kind) valid() bool {
	switch k {
	case KindConductor, KindExplore:
		return true
	}
	return false
}

// State is a row's lifecycle: whether the announced work is still going, and
// how it ended. It is not a commit state - internal/sharedcatalog's sync states
// say how far a record got through the object-first protocol, which is a claim
// about a record, while this is a claim about a process.
type State string

// The states a row can hold.
const (
	// StateRunning is the state every row is announced in. It means the
	// process said so, not that it is true now; that is what Freshness is for.
	StateRunning State = "running"
	// StateFinished is work that ended without the caller reporting a
	// failure. It says nothing about what the analysis concluded, on the same
	// reasoning conductor.OutcomeRan does: the receipt is where that lives.
	StateFinished State = "finished"
	// StateFailed is work whose caller reported an error.
	StateFailed State = "failed"
	// StateCancelled is work stopped by an operator or a deadline. It is a
	// separate state from failure because a cancelled run kept everything it
	// had already committed, and rendering it as a failure would misreport the
	// most common way a long run ends.
	StateCancelled State = "cancelled"
)

func (s State) valid() bool {
	switch s {
	case StateRunning, StateFinished, StateFailed, StateCancelled:
		return true
	}
	return false
}

// Terminal reports whether this state ends a row. A terminal row is final in
// the database too: the migration's trigger refuses any later update, so a
// zombie process cannot resurrect a run that already reported how it ended.
func (s State) Terminal() bool { return s.valid() && s != StateRunning }

// PresenceID identifies one announcement. It is minted by the announcing host
// with 128 random bits behind a prefix, the shape every other client-generated
// identifier in Babel uses, so a host can announce without coordinating for an
// identity first.
//
// The empty value is the "nothing was announced" answer, and every method that
// takes one treats it as a no-op. That is what lets a wiring site write
// `id, _ := a.Announce(...)` and then heartbeat and finalize unconditionally:
// a presence store that was unreachable at announce time costs the run one
// diagnostic line and no branches.
type PresenceID string

// Announcement is what a run says about itself when it starts.
//
// Host and deployment are deliberately absent. The Store holds both, so an
// announcement cannot claim to come from another machine or another
// deployment - the two fields a fleet reader trusts most are the two a caller
// cannot set.
type Announcement struct {
	Kind Kind

	// RunID is the run this row is about: the explore run's identity, or the
	// run identity a conductor cycle minted for the work it drew. It is
	// required, because a presence row nobody can join back to a receipt is a
	// blinking light with no referent.
	RunID string

	// Recipe is the primary cookbook recipe id the run applies, empty when it
	// names none. Singular by design: a receipt records a run's whole
	// cookbook set with versions, and presence answers the narrower question
	// of what a fleet row is for.
	Recipe string

	// PreparationID is the immutable corpus scope, empty when the run does not
	// have one yet - a conductor cycle announces before its runner has
	// prepared anything.
	PreparationID string

	// Authority is why the run is happening, in #96's vocabulary: the
	// operator command or invitation, the standing policy or duty, or the
	// declared serendipity draw. It reaches this row for the same reason it
	// reaches a receipt header - it is a kind and an identifier reference,
	// which is inside the plaintext boundary, and a fleet that could see what
	// is running but not why would be exactly as opaque as #96 objected to.
	//
	// A zero Authority is stored as absent rather than as an empty string, so
	// "unrecorded" stays distinguishable from "recorded as nothing".
	Authority run.Authority
}

func (a Announcement) validate() error {
	if !a.Kind.valid() {
		return &invalidError{what: "kind", value: string(a.Kind)}
	}
	if a.RunID == "" {
		return &invalidError{what: "run id", value: ""}
	}
	if a.Authority.Recorded() && a.Authority.Ref == "" {
		return &invalidError{what: "authority reference", value: string(a.Authority.Kind)}
	}
	return nil
}

// Outcome is how announced work ended.
//
// There is no reason or message field, and there will not be one: a failure's
// words are content, they belong in the receipt that is sealed into an object,
// and a column of them here would be readable by the managed provider and by
// anyone holding the catalog credential. What the fleet needs is which of four
// things happened and where to read the rest.
type Outcome struct {
	// State must be terminal. A caller finalizing with StateRunning is
	// reporting that the run both ended and did not.
	State State

	// ReceiptRecordID is the Phase B record id of the run's receipt, empty
	// when the run wrote none. It is the join a fleet reader follows from
	// "this run finished" to the durable account of what it did (#113), and it
	// is recorded whether or not the receipt has published yet: the receipt
	// commits through internal/sync on its own schedule, and presence must be
	// able to name it before that lands.
	ReceiptRecordID string
}

func (o Outcome) validate() error {
	if !o.State.Terminal() {
		return &invalidError{what: "outcome state", value: string(o.State)}
	}
	return nil
}

// Row is one announcement as the fleet reads it: what was announced, plus the
// two derived fields a render surface must not compute for itself.
type Row struct {
	ID PresenceID

	// Host is the opaque, operator-assigned host id - byte-identical to the
	// value `hosts.host_id` and `snapshots.host_id` carry, so a surface that
	// wants a display name joins the host vocabulary that already exists.
	// Presence stores no second copy of host identity and no system hostname.
	Host       string
	Deployment string

	Kind          Kind
	RunID         string
	Recipe        string
	PreparationID string
	Authority     run.Authority

	State     State
	StartedAt time.Time
	// HeartbeatAt is the last time the run said anything, which is also the
	// instant Finalize records: a finalize is the last thing a live process
	// says.
	HeartbeatAt time.Time
	// FinishedAt is zero exactly while State is StateRunning.
	FinishedAt      time.Time
	ReceiptRecordID string

	// HeartbeatAge is how long ago that heartbeat was, computed by PostgreSQL
	// inside the read query. It is server-derived at both ends - the heartbeat
	// is written with the server's clock and the age is taken against the same
	// clock - so neither a skewed writer nor a skewed reader can make a run
	// look fresher than it is.
	HeartbeatAge time.Duration

	// Freshness is Classify(State, HeartbeatAge), filled here so that every
	// surface in Babel classifies a row the same way. A renderer that
	// re-derived it from its own clock would drift from the CLI and from every
	// other reader the moment the thresholds moved.
	Freshness Freshness

	// Local reports that this row was announced by the host reading it, so a
	// fleet view can mark "this host" without a second query and without the
	// reader needing to know its own identity.
	Local bool
}

// Freshness is how much a row's claim to be running is still worth. It is a
// statement about the age of evidence, never about the process: FreshnessLost
// means nothing has been heard for a long time, and it is deliberately not
// named "dead".
type Freshness string

// The four classifications.
const (
	// FreshnessFresh is a running row that heartbeat recently enough that the
	// run is very likely alive.
	FreshnessFresh Freshness = "fresh"
	// FreshnessStale is a running row whose last heartbeat is old enough to
	// doubt. The run may be working, blocked, or gone; this host cannot tell,
	// and a surface must say so.
	FreshnessStale Freshness = "stale"
	// FreshnessLost is a running row nothing has been heard from for so long
	// that the process is probably gone without finalizing - which is what a
	// killed run looks like, and is exactly the case a reaper would have
	// erased.
	FreshnessLost Freshness = "lost"
	// FreshnessFinished is a row that reported how it ended. Its heartbeat age
	// says how long ago that was and nothing about liveness, so it is never
	// classified as stale.
	FreshnessFinished Freshness = "finished"
)

// The staleness thresholds. They are constants rather than configuration
// because a fleet whose machines disagreed about what "stale" means would
// render the same row two ways, and there is no deployment for which the
// honest answer differs.
const (
	// HeartbeatInterval is how often a running row is refreshed. It is short
	// relative to StaleAfter so that one lost heartbeat - a slow query, a
	// blip - does not make a healthy run look doubtful.
	HeartbeatInterval = 30 * time.Second

	// StaleAfter is when a running row stops being trustworthy: four missed
	// heartbeats. A run this quiet may still be fine, which is why the
	// classification says "doubt it" rather than "it is gone".
	StaleAfter = 2 * time.Minute

	// LostAfter is when a running row is more likely a process that died than
	// a process that is quiet. It is generous on purpose: a laptop that slept
	// mid-run is a normal event on this fleet, and the row it left behind
	// should read as lost rather than be mistaken for live.
	LostAfter = 15 * time.Minute

	// RetentionWindow bounds what Fleet returns. Nothing deletes a row, so
	// this is the whole of the retention policy: a day is long enough to ask
	// "what did this machine do overnight" and short enough that a fleet view
	// is a view rather than a log.
	RetentionWindow = 24 * time.Hour
)

// MaxFleetRows bounds one Fleet read, so a surface cannot be handed an
// unbounded result by a fleet that had a busy day.
const MaxFleetRows = 500

// Classify is the one place a row's freshness is decided, for every surface
// that renders one.
//
// It takes the heartbeat age rather than a clock deliberately: the age comes
// from PostgreSQL, and a function that took time.Now would let each reader's
// own clock skew into the answer.
func Classify(state State, heartbeatAge time.Duration) Freshness {
	if state != StateRunning {
		return FreshnessFinished
	}
	switch {
	case heartbeatAge >= LostAfter:
		return FreshnessLost
	case heartbeatAge >= StaleAfter:
		return FreshnessStale
	default:
		return FreshnessFresh
	}
}

// Announcer is the write half: what a run calls to become visible.
//
// It is an interface so that a run's wiring can be exercised without a
// database, and so that a deployment without a shared catalog passes nil. Nil
// is the feature quietly absent - nothing is announced, no write path behaves
// differently, and no run is refused - on the same terms internal/explore
// already documents for its publisher and its reference appender.
//
// No method here may fail a run. An implementation reports its failures to its
// own diagnostic sink; a returned error is a caller bug, and even that is
// ignored by every wiring site, because a run must not be able to end because
// the fleet could not be told about it.
type Announcer interface {
	// Announce records that work has started, and returns the id the other
	// two methods address. The empty id is a legitimate answer, meaning the
	// announcement did not land, and it makes those methods no-ops.
	Announce(ctx context.Context, a Announcement) (PresenceID, error)

	// Heartbeat says the work is still going. It is a no-op on an unknown or
	// already-finalized id, so a heartbeat racing a finalize is harmless.
	Heartbeat(ctx context.Context, id PresenceID) error

	// Finalize records how the work ended. It runs even for a cancelled run,
	// on a context detached from the cancellation, because the moment a run
	// ends is exactly when the fleet most needs to be told.
	Finalize(ctx context.Context, id PresenceID, o Outcome) error
}

// Reader is the read half: one query, because there is one question.
//
// Nil is the feature quietly absent here too - a surface with no reader shows
// what it has locally and says why there is nothing else.
type Reader interface {
	Fleet(ctx context.Context) ([]Row, error)
}

// Beat runs the heartbeat loop for one announcement and returns the function
// that stops it.
//
// It lives here rather than in each wiring site because both sites want the
// identical thing - a ticker at HeartbeatInterval for as long as some work is
// in flight - and a second copy of it would be a second place the interval
// could drift from the thresholds it is calibrated against.
//
// The returned stop function is idempotent and waits for the loop to exit, so a
// caller may defer it. Everything is nil-safe: no announcer, or an id that
// never landed, produces a loop that never starts and a stop that does nothing.
func Beat(ctx context.Context, a Announcer, id PresenceID) (stop func()) {
	if a == nil || id == "" {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				// The work is over or being cancelled. Finalize is what
				// records that, and it runs on a detached context; there is
				// nothing left for a heartbeat to say.
				return
			case <-ticker.C:
				// The error is discarded because the implementation has
				// already diagnosed it. A heartbeat loop that logged its own
				// failures would repeat one diagnostic line every thirty
				// seconds for as long as PostgreSQL stayed down.
				_ = a.Heartbeat(ctx, id)
			}
		}
	}()
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		close(done)
		<-finished
	}
}
