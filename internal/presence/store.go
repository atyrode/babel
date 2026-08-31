package presence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// DefaultTimeout bounds one presence statement. It is short because it is a
// deadline on somebody else's run: a heartbeat that waited thirty seconds for a
// degraded database would have turned a diagnostic into a stall, and there is
// nothing a presence write is worth waiting for.
const DefaultTimeout = 3 * time.Second

// Options are what a Store needs. Only DB, DeploymentID and HostID are
// required; the rest have honest defaults.
type Options struct {
	// DB is the shared catalog pool. It is borrowed: New closes nothing, and
	// only a Store built by Open owns its connection.
	DB *sql.DB

	// DeploymentID and HostID are the identity every row this Store writes
	// carries. They are here rather than in an Announcement so that a caller
	// cannot announce as another machine.
	DeploymentID string
	HostID       string

	// Diag receives every failure. It is the whole error path: a presence
	// write reports to this and returns nil, which is the same contract
	// internal/sync's publisher holds for an unreachable catalog.
	//
	// Nil discards them. That is a supported choice for a caller with nowhere
	// to put a diagnostic, and it is the documented consequence rather than a
	// silent one: the run is unaffected either way, and the only cost is that
	// nobody is told why the fleet cannot see it.
	Diag func(error)

	// Timeout bounds each statement. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Store reads and writes fleet presence in the shared catalog. It implements
// both Announcer and Reader.
//
// A nil *Store is fully usable and does nothing: every method returns the
// zero answer. That is what a machine in local mode holds, and it means the
// wiring sites need no branch of their own. A nil *Store placed in an Announcer
// interface is still a non-nil interface, so wiring sites additionally check
// the interface value - see how internal/explore and internal/conductor hold
// theirs.
type Store struct {
	db           *sql.DB
	deploymentID string
	hostID       string
	diag         func(error)
	timeout      time.Duration

	// owned is the connection this Store must close, set only by Open. A
	// Store built by New borrows its caller's pool and closes nothing:
	// closing a long-lived process's catalog pool from underneath it is how
	// that process loses its catalog.
	owned *sql.DB
}

// New validates opt and returns a Store over a borrowed connection.
//
// The three required fields are required because a row without them is not
// readable by anyone: a fleet read is scoped by deployment, and a row that
// named no host would be presence with no answer to "where".
func New(opt Options) (*Store, error) {
	if opt.DB == nil {
		return nil, errors.New("presence: a store needs a shared catalog connection")
	}
	if opt.DeploymentID == "" {
		return nil, errors.New("presence: a row is scoped by deployment, so a deployment id is required")
	}
	if opt.HostID == "" {
		return nil, errors.New("presence: presence answers \"running where\", so a host id is required")
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Store{
		db:           opt.DB,
		deploymentID: opt.DeploymentID,
		hostID:       opt.HostID,
		diag:         opt.Diag,
		timeout:      timeout,
	}, nil
}

// Close releases the connection this Store owns, if it opened one. It is safe
// on a nil Store and on one built by New, which owns nothing.
func (s *Store) Close() error {
	if s == nil || s.owned == nil {
		return nil
	}
	db := s.owned
	s.owned = nil
	return db.Close()
}

// HostID reports the host every row this Store writes is attributed to, and the
// one Fleet marks rows Local against.
func (s *Store) HostID() string {
	if s == nil {
		return ""
	}
	return s.hostID
}

// report hands one failure to the diagnostic sink and swallows it. It is the
// single place the best-effort contract is implemented, so there is no path
// through this package that can return an I/O failure to a run.
func (s *Store) report(err error) {
	if err == nil || s.diag == nil {
		return
	}
	s.diag(err)
}

// bound applies the statement timeout. Cancellation of the caller's context is
// preserved: a run that has been cancelled has no use for a new heartbeat.
func (s *Store) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

const insertStmt = `INSERT INTO presence (
    presence_id, deployment_id, host_id, kind, run_id, recipe,
    preparation_id, authority_kind, authority_ref, state)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'running')`

// Announce records that work has started on this host.
//
// The returned id is empty on every failure, and that is the mechanism the
// whole package rests on: a caller writes `id, _ := store.Announce(...)` and
// then heartbeats and finalizes unconditionally, because an empty id makes both
// no-ops. So an unreachable catalog costs a run one diagnostic line and no
// control flow.
//
// The returned error is a caller bug - an unknown kind, a missing run id -
// never an outage, which has already gone to Diag. Wiring sites ignore it; it
// exists so that a test can prove the validation happens.
func (s *Store) Announce(ctx context.Context, a Announcement) (PresenceID, error) {
	if s == nil {
		return "", nil
	}
	if err := a.validate(); err != nil {
		wrapped := fmt.Errorf("presence: announce run %s: %w", a.RunID, err)
		s.report(wrapped)
		return "", wrapped
	}
	id, err := newID()
	if err != nil {
		s.report(fmt.Errorf("presence: announce run %s: %w", a.RunID, err))
		return "", nil
	}

	ctx, cancel := s.bound(ctx)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, insertStmt,
		string(id), s.deploymentID, s.hostID, string(a.Kind), a.RunID,
		nullable(a.Recipe), nullable(a.PreparationID),
		nullable(string(a.Authority.Kind)), nullable(authorityRef(a.Authority)),
	); err != nil {
		s.report(fmt.Errorf("presence: announce run %s on host %s: %w; the run proceeds, invisible to the fleet",
			a.RunID, s.hostID, err))
		return "", nil
	}
	return id, nil
}

// Heartbeat says the announced work is still going.
//
// It is a no-op on an empty id, and it matches only a row still in `running`,
// so a heartbeat that races a finalize updates nothing rather than fighting the
// database trigger that makes a finalized row final. A heartbeat that matches
// no row is not an error: the row was finalized, or was never announced, and
// both are outcomes this call has nothing to add to.
func (s *Store) Heartbeat(ctx context.Context, id PresenceID) error {
	if s == nil || id == "" {
		return nil
	}
	ctx, cancel := s.bound(ctx)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE presence SET heartbeat_at = now()
		  WHERE presence_id = $1 AND state = 'running'`, string(id)); err != nil {
		s.report(fmt.Errorf("presence: heartbeat %s: %w", id, err))
	}
	return nil
}

// Finalize records how the announced work ended.
//
// It runs on a context detached from the caller's cancellation, which is the
// one place this package deliberately outlives its caller's context. A
// cancelled run is the most valuable thing presence can report - it is the
// difference between a row that says "cancelled 20 seconds ago" and a row that
// goes stale and looks like a machine that died - and the timeout still bounds
// it, so a detached context cannot make a shutdown hang.
//
// heartbeat_at is advanced with the state, because a finalize is the last thing
// a live process says. That keeps the retention window a single predicate over
// one column and keeps a finished row's age meaning "how long ago did this
// end".
func (s *Store) Finalize(ctx context.Context, id PresenceID, o Outcome) error {
	if s == nil || id == "" {
		return nil
	}
	if err := o.validate(); err != nil {
		wrapped := fmt.Errorf("presence: finalize %s: %w", id, err)
		s.report(wrapped)
		return wrapped
	}
	ctx, cancel := s.bound(context.WithoutCancel(ctx))
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE presence
		    SET state = $2, finished_at = now(), heartbeat_at = now(),
		        receipt_record_id = $3
		  WHERE presence_id = $1 AND state = 'running'`,
		string(id), string(o.State), nullable(o.ReceiptRecordID)); err != nil {
		s.report(fmt.Errorf("presence: finalize %s as %s: %w", id, o.State, err))
	}
	return nil
}

// fleetStmt reads the deployment's live and recently finished rows.
//
// The heartbeat age is computed by PostgreSQL rather than by the reader, so no
// client clock enters the classification, and the retention window is a
// predicate on the same column for the same reason. It comes back as epoch
// seconds rather than an interval because a float is unambiguous across every
// driver, while an interval's Go representation is a driver's choice. The
// ordering puts running rows first and then sorts by recency, which is the
// order a fleet view asks for: what is happening, then what just happened.
const fleetStmt = `SELECT presence_id, host_id, deployment_id, kind, run_id,
       recipe, preparation_id, authority_kind, authority_ref, state,
       started_at, heartbeat_at, finished_at, receipt_record_id,
       extract(epoch from (now() - heartbeat_at))
  FROM presence
 WHERE deployment_id = $1
   AND heartbeat_at > now() - make_interval(secs => $2::double precision)
 ORDER BY (state = 'running') DESC, heartbeat_at DESC, presence_id
 LIMIT $3`

// Fleet reports what this deployment has been doing, newest first, with running
// rows ahead of finished ones.
//
// A read failure is reported to Diag and returned, unlike every write in this
// package. The asymmetry is the point: a write is a side effect on a run that
// must not be able to fail, while a read is somebody asking a question, and
// answering "nothing is running" when the truth is "PostgreSQL is down" would
// be the one dishonest thing this package could do.
func (s *Store) Fleet(ctx context.Context) ([]Row, error) {
	if s == nil {
		return nil, nil
	}
	ctx, cancel := s.bound(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, fleetStmt,
		s.deploymentID, RetentionWindow.Seconds(), MaxFleetRows)
	if err != nil {
		wrapped := fmt.Errorf("presence: read the fleet: %w", err)
		s.report(wrapped)
		return nil, wrapped
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		row, err := s.scan(rows)
		if err != nil {
			wrapped := fmt.Errorf("presence: read the fleet: %w", err)
			s.report(wrapped)
			return nil, wrapped
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		wrapped := fmt.Errorf("presence: read the fleet: %w", err)
		s.report(wrapped)
		return nil, wrapped
	}
	return out, nil
}

// scan builds one Row, filling the two derived fields here so that no surface
// downstream has to know how they are derived.
func (s *Store) scan(src interface {
	Scan(dest ...any) error
}) (Row, error) {
	var (
		row               Row
		kind, state       string
		recipe, prep      sql.NullString
		authKind, authRef sql.NullString
		receipt           sql.NullString
		finished          sql.NullTime
		ageSeconds        float64
	)
	if err := src.Scan(&row.ID, &row.Host, &row.Deployment, &kind, &row.RunID,
		&recipe, &prep, &authKind, &authRef, &state,
		&row.StartedAt, &row.HeartbeatAt, &finished, &receipt, &ageSeconds); err != nil {
		return Row{}, err
	}
	row.Kind = Kind(kind)
	row.State = State(state)
	row.Recipe = recipe.String
	row.PreparationID = prep.String
	row.Authority = run.Authority{Kind: run.AuthorityKind(authKind.String), Ref: authRef.String}
	row.ReceiptRecordID = receipt.String
	if finished.Valid {
		row.FinishedAt = finished.Time
	}
	// A clock that has just been stepped backwards can make PostgreSQL's own
	// difference negative. Reporting a negative age would render as a run
	// heartbeating from the future, so it is clamped to zero: the honest
	// reading of "heartbeat_at is not in the past" is "just now".
	age := time.Duration(ageSeconds * float64(time.Second))
	if age < 0 {
		age = 0
	}
	row.HeartbeatAge = age
	row.Freshness = Classify(row.State, age)
	row.Local = row.Host == s.hostID
	return row, nil
}

// newID mints an announcement's identity: a kind prefix and 128 random bits,
// the shape internal/frontier, internal/reality and internal/reference mint
// rather than a second convention. The width is what makes a client-generated
// id safe as a global primary key with no coordination, which is what lets a
// host announce the instant it starts working.
func newID() (PresenceID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate presence id: %w", err)
	}
	return PresenceID("prs_" + hex.EncodeToString(raw[:])), nil
}

// nullable stores an absent optional field as SQL NULL, so "the run named no
// recipe" is absent rather than an empty string a later reader has to know to
// treat as absence.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// authorityRef returns the reference only for an authority that was recorded,
// so an unrecorded authority stores two NULLs rather than a kind with no
// reference or a reference with no kind.
func authorityRef(a run.Authority) string {
	if !a.Recorded() {
		return ""
	}
	return a.Ref
}

// invalidError is a caller bug: a vocabulary value this build does not
// implement, or a required identifier that is missing. It is a type rather than
// a formatted string so that a test can assert the class of failure without
// matching prose.
type invalidError struct {
	what  string
	value string
}

func (e *invalidError) Error() string {
	if e.value == "" {
		return "presence: no " + e.what
	}
	return fmt.Sprintf("presence: %s %q is not one this build implements", e.what, e.value)
}

// ErrInvalid is what every caller bug in this package matches, so a wiring site
// or a test can tell "you asked for something impossible" from "the database
// was unreachable" without reading the message.
var ErrInvalid = errors.New("presence: invalid announcement")

func (e *invalidError) Is(target error) bool { return target == ErrInvalid }

// Unreachable reports whether an error means PostgreSQL could not be reached,
// as opposed to answered. It forwards to internal/sharedcatalog so that a
// surface has one predicate for the whole catalog rather than one per package.
func Unreachable(err error) bool { return sharedcatalog.Unreachable(err) }
