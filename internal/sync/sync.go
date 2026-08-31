// Package sync publishes Babel's durable Phase B records to the shared
// backend: object-first into the encrypted object store, PostgreSQL-last, and
// only then a local flip from pending-sync to committed (SPEC.md §6.5, §9,
// §12 Phase B).
//
// It exists because every Phase B writer already stamps its output
// pending-sync and nothing moved it. A hypothesis, a finding, a receipt or an
// operator's decision exists nowhere but Babel, the durable database is not
// under the hourly archive roots, and so until this package ran, a dead
// workstation disk lost analysis outputs and the value of a run was bound to
// the machine that happened to execute it.
//
// The package name deliberately shadows the standard library's `sync`. It is
// named for the operator-facing verb - `babel sync` - and a file that needs
// both imports the standard library's as `stdsync`. Nothing here needs it: the
// durable database has one writer per §9's local state-writer lock invariant,
// and the publisher runs on the caller's goroutine.
//
// # What this package guarantees
//
// Publication is never a write-path dependency. A writer's local commit is the
// durable event; publication is attempted immediately afterwards and every
// failure of it - an unreachable database, an object store that refuses a
// write, a missing credential, a closure that is not yet complete - leaves the
// record exactly as it was: durable, visibly pending-sync, and owed to the
// fleet. The command that produced the record still succeeds. That is not
// leniency; it is the only ordering under which an outage cannot destroy
// output, and SPEC.md §9 requires staged output to remain visibly pending
// rather than to be quietly assumed safe or refused outright.
//
// Replays are idempotent. Every Phase B record already carries a globally
// unique client-generated entity id, and each revision of a record is its own
// id, so idempotency by entity id is idempotency by (entity, revision) with no
// second key: a retry that finds a record already recorded writes no object
// and inserts no row. internal/sharedcatalog's SyncRun is where that is
// enforced against PostgreSQL; this package is what remembers, locally and
// across a crash, which records are still owed.
//
// # The two halves
//
// A Journal in the durable database records what has been staged and what has
// reached the shared catalog. A durable writer stages inside its own
// transaction, so "this record is durable locally" and "this record is known
// to be owed to the fleet" are one event rather than two with a window
// between them.
//
// A Publisher turns a declared closure into a commit. It holds the
// deployment's PostgreSQL handle, its object store and its payload keyring,
// and it performs exactly the ordering migration 0003 was written for: declare
// the run pending-sync, seal and store each missing record's payload and only
// then insert the row that names the object, and flip the run to committed
// conditional on the catalog holding the whole declared closure. The local
// flip follows the PostgreSQL commit and never precedes it.
package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// validEntityID bounds every identifier this package stages.
//
// It is internal/sharedcatalog's validRecordID, restated rather than exported
// from there, and the reason is worth stating: a staged record whose id the
// remote protocol will refuse is a journal row that can never publish, and a
// row that can never publish is worse than a refused write - it is a permanent
// pending-sync entry with no remedy. Refusing it at stage time turns that into
// a caller bug caught by the writer's own test. The two patterns must agree,
// and identity.go's ValidEntityID is what keeps them from drifting: it calls
// sharedcatalog's, and this one is only the stage-time gate.
var validEntityID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// maxPayloadBytes bounds one staged payload.
//
// A Phase B payload is a record: a claim, an operator's note, a receipt. The
// bound exists because the payload is copied into the durable database until
// publication succeeds and is held whole in memory to be sealed, so an
// unbounded one would let a single malformed record consume the machine's
// memory and the operator's durable file at once. Sixteen mebibytes is far
// above any record Babel produces and far below either hazard.
const maxPayloadBytes = 16 << 20

// ErrNotConfigured reports that shared publication is not available: local
// mode, no catalog, no object store, or no payload key. It is a condition
// rather than a fault - a local-only deployment is a supported deployment -
// so a caller reports it once and carries on writing locally.
var ErrNotConfigured = errors.New("sync: shared publication is not configured")

// Record is one durable Phase B record on its way to the shared catalog.
//
// Payload is the record's canonical publication bytes in plaintext, and it is
// plaintext deliberately: sealing happens inside internal/sharedcatalog, at the
// boundary that writes remotely, so there is no path in this package that
// accepts bytes a caller merely claims are already encrypted. The bytes are
// opaque here - each writing component owns its own canonical shape, because
// the component that stores a record is the only one that knows what a faithful
// serialization of it is.
type Record struct {
	// RunID names the closure this record commits within. Append sets it, and
	// a caller that uses Append never fills it: the choice between joining a
	// producing run's closure and becoming a closure of one is a rule that
	// belongs in one place. It is set directly only by StageTx's caller, which
	// is a writer that already knows its run.
	RunID string
	// EntityID is the record's global, client-generated identifier and the
	// idempotency key for the whole protocol.
	EntityID string
	Kind     sharedcatalog.RecordKind
	// Schema is the record type's own schema version, independent of the
	// catalog's: a payload shape may evolve without telling Phase A writers to
	// stop.
	Schema  int
	Payload []byte
	// Edge is the plaintext graph shape of a reference edge (issue #113), and
	// nil for every record that is not one.
	//
	// It travels beside the payload rather than inside it because it is the
	// half of an edge SPEC.md §763 admits in the clear: the relation kind and
	// both endpoint references are identifier metadata, so the fleet-wide
	// citation graph stays navigable on a host with no payload key, while the
	// edge's note - the only content an edge carries - stays sealed in the
	// object. internal/sharedcatalog writes it into migrations/0008's columns
	// in the same transaction as the record row.
	//
	// The bytes in Payload are still the whole record. This is a projection of
	// them for a reader that cannot open them, never the authority: a host
	// holding the key reads the object and needs none of these columns.
	Edge *sharedcatalog.RecordEdge
}

// validate refuses at stage time what the remote protocol would refuse at
// publish time.
func (r Record) validate() error {
	if !validEntityID.MatchString(r.EntityID) {
		return fmt.Errorf("sync: entity id %q is not a well-formed Phase B identifier", r.EntityID)
	}
	if !validEntityID.MatchString(r.RunID) {
		return fmt.Errorf("sync: run id %q is not a well-formed Phase B identifier", r.RunID)
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("sync: %q is not a Phase B record kind the shared catalog carries", string(r.Kind))
	}
	if r.Schema <= 0 {
		return fmt.Errorf("sync: record %s has no schema version", r.EntityID)
	}
	if len(r.Payload) == 0 {
		return fmt.Errorf("sync: record %s has an empty payload", r.EntityID)
	}
	if len(r.Payload) > maxPayloadBytes {
		return fmt.Errorf("sync: record %s payload is %d bytes, over the %d-byte bound",
			r.EntityID, len(r.Payload), maxPayloadBytes)
	}
	if r.Edge != nil {
		if err := r.Edge.Validate(); err != nil {
			return fmt.Errorf("sync: record %s: %w", r.EntityID, err)
		}
	}
	return nil
}

// Closure is one run whose record set is complete.
//
// Declaring a closure is a separate act from staging its records because
// migration 0003 fixes a run's record_count at declaration and never lets it
// move: the count is what makes the flip to committed conditional on the
// catalog actually holding the whole run, so "a partial commit is not a commit"
// is a database property rather than a convention. A run therefore cannot be
// declared while it may still grow, and an exploration interrupted before it
// finished producing records leaves its records staged and its closure
// undeclared - visibly pending, and completed by resuming the run under the
// same id rather than by publishing a run that later grows.
type Closure struct {
	RunID string
	// ExecutionHostID pins repository-dependent work to the host that can
	// rerun it (SPEC.md §9). Empty means unpinned; it never restricts reads.
	ExecutionHostID string
	// ContinuesRunID links this run to the committed run it continues, so a
	// second instance's follow-on work stays attached to the first instance's
	// output rather than merely resembling it.
	ContinuesRunID string
}

// Hook is the publication surface a durable writer holds.
//
// A nil *Publisher satisfies it and every method is a silent no-op, which is
// what local-only mode is: nothing is staged, nothing is published, and no
// writer needs a branch for it beyond the nil check it would write anyway.
type Hook interface {
	// Append stages rec inside tx and reports the closure to publish once tx
	// has committed, resolving which closure the record belongs to.
	Append(ctx context.Context, tx *sql.Tx, producedBy string, rec Record) (Closure, bool, error)
	// StageTx stages rec inside tx - the transaction that is making the
	// record durable - into the run rec names.
	StageTx(ctx context.Context, tx *sql.Tx, rec Record) error
	// DeclareTx closes a run's closure inside tx, at the records staged for
	// it so far including any staged in this same transaction.
	DeclareTx(ctx context.Context, tx *sql.Tx, c Closure) error
	// CommitInline attempts to publish a declared closure immediately, right
	// after the transaction that declared it committed.
	CommitInline(ctx context.Context, c Closure) error
}

// Publisher commits declared closures to the shared backend.
//
// It is safe to reuse across closures and is deliberately not safe for
// concurrent use: it writes the durable journal, which has one writer, and an
// instance publishes from one goroutine.
type Publisher struct {
	journal *Journal
	catalog *sql.DB
	store   sharedcatalog.ObjectStore
	ring    *envelope.Keyring

	deploymentID string
	instanceID   string

	// diag receives one publication failure at a time. It is a func rather
	// than an io.Writer because the value in the error is untrusted - it may
	// carry a remote endpoint's words - and only the command surface owns the
	// terminal-safe renderer that may put it on a terminal (SPEC.md §8).
	diag func(error)
}

// Options is everything a Publisher needs. Each dependency is injected rather
// than constructed here, so a test supplies an in-memory object store and a
// throwaway database on exactly the terms the deployment supplies Cellar and
// managed PostgreSQL, and neither choice reaches the protocol.
type Options struct {
	// Config is the validated storage configuration. Only the deployment and
	// instance identity are read from it; the catalog handle and the object
	// store are passed in already open.
	Config  config.Config
	Journal *Journal
	// Catalog is the shared PostgreSQL handle. The caller owns its lifetime:
	// a command that publishes and then reports opens one connection pool for
	// both rather than one per operation.
	Catalog *sql.DB
	Store   sharedcatalog.ObjectStore
	Keyring *envelope.Keyring
	Diag    func(error)
}

// New validates opt and returns a Publisher.
//
// Every reason publication cannot work that is knowable without I/O is
// reported here, before a writer has committed a record it will then fail to
// publish. A configuration that is simply local-only reports ErrNotConfigured,
// which a caller reads as "write locally and stage nothing".
func New(opt Options) (*Publisher, error) {
	if opt.Config.Mode != config.ModeShared {
		return nil, fmt.Errorf("sync: storage is configured in %s mode: %w",
			opt.Config.Mode, ErrNotConfigured)
	}
	if opt.Config.DeploymentID == "" || opt.Config.InstanceID == "" {
		return nil, errors.New("sync: shared mode requires deployment and instance identity")
	}
	if opt.Journal == nil {
		return nil, errors.New("sync: a durable journal is required: an unrecorded staged record is a lost one")
	}
	if opt.Catalog == nil {
		return nil, fmt.Errorf("sync: no shared catalog handle: %w", ErrNotConfigured)
	}
	if opt.Store == nil {
		return nil, fmt.Errorf("sync: no object store: %w", ErrNotConfigured)
	}
	if opt.Keyring == nil {
		return nil, fmt.Errorf("sync: no payload keyring: %w", ErrNotConfigured)
	}
	return &Publisher{
		journal:      opt.Journal,
		catalog:      opt.Catalog,
		store:        opt.Store,
		ring:         opt.Keyring,
		deploymentID: opt.Config.DeploymentID,
		instanceID:   opt.Config.InstanceID,
		diag:         opt.Diag,
	}, nil
}

// Journal reports the journal this publisher writes, so a caller that already
// holds a Publisher can render per-record sync state without opening the
// durable file a second time.
func (p *Publisher) Journal() *Journal {
	if p == nil {
		return nil
	}
	return p.journal
}

// StageTx stages rec inside tx.
//
// It shares the writer's transaction rather than following it, which is the
// whole reason the method takes a *sql.Tx: a record that committed locally
// while its journal row did not would be durable, invisible to the publisher,
// and reported by nothing.
func (p *Publisher) StageTx(ctx context.Context, tx *sql.Tx, rec Record) error {
	if p == nil {
		return nil
	}
	return p.journal.stage(ctx, tx, rec)
}

// Append stages rec inside tx and reports the closure to publish once tx has
// committed.
//
// It is the one call a durable writer needs, and it exists because choosing a
// record's publication closure is a rule rather than a judgement, and a rule
// restated at every write site is a rule that eventually differs at one of
// them.
//
// producedBy names the run that produced the record, and is empty for a record
// no run produced - an operator's decision, a Reality fact an operator
// answered for, a preparation. Two outcomes:
//
//   - The producing run's closure is still open. The record joins it and
//     nothing publishes yet, because a run's closure may not be declared while
//     it can still grow. The run declares itself when it ends, which for an
//     exploration is when its receipt is written.
//   - There is no producing run, or its closure is already declared. The
//     record becomes its own closure of one, declared inside this same
//     transaction so no crash can leave it staged with nobody to declare it,
//     and linked to the run it came after so lineage survives. This is what an
//     operator's later decision about an old finding is, and what an amended
//     receipt is: not part of a closed run's output, but attached to it.
//
// The record's own entity id is the run id of a closure of one. Run ids and
// record ids live in different tables, so there is no collision, and using the
// record's own id says exactly what the row means: this commit is this record.
//
// producedBy is reduced to the run whose closure it belongs to; see
// publicationRun.
func (p *Publisher) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec Record) (Closure, bool, error) {
	if p == nil {
		return Closure{}, false, nil
	}
	producedBy = publicationRun(producedBy)
	if producedBy != "" {
		open, err := p.journal.closureOpen(ctx, tx, producedBy)
		if err != nil {
			return Closure{}, false, err
		}
		if open {
			rec.RunID = producedBy
			if err := p.journal.stage(ctx, tx, rec); err != nil {
				return Closure{}, false, err
			}
			return Closure{}, false, nil
		}
	}
	rec.RunID = rec.EntityID
	if err := p.journal.stage(ctx, tx, rec); err != nil {
		return Closure{}, false, err
	}
	c := Closure{RunID: rec.EntityID, ContinuesRunID: producedBy}
	if _, err := p.journal.declareTx(ctx, tx, c); err != nil {
		return Closure{}, false, err
	}
	return c, true, nil
}

// publicationRun reduces a producing job's local run identity to the run whose
// closure it belongs to: everything before the first separator.
//
// SPEC.md §5.4 makes the challenger and the synthesizer logically separate jobs
// with their own run identity and their own receipt, and internal/explore spells
// those identities `<run>/<stage>`. Two things follow, and both need this
// reduction rather than tolerating the compound id.
//
// It could not be a publication run id. The shared catalog holds run ids to the
// same bare-identifier shape it holds record ids to, and a value with a path
// separator is refused - so a challenger's records would fail to stage rather
// than publish.
//
// And it should not be one even if it could. A stage is a job within a run, not
// a run: SPEC.md §6.5 makes a run reviewable once its whole closure has
// committed, and an exploration's closure is its own records plus the records
// and receipts of the separate jobs it launched. Splitting them into three
// closures would make one exploration become globally reviewable in three
// unrelated pieces, and a reader asking what a run concluded would have to know
// to look for two more.
//
// The reduction is collision-free because the parent is itself a valid
// identifier and the separator is the one internal/explore introduced: no bare
// run id can contain it.
func publicationRun(runID string) string {
	for i := range len(runID) {
		if runID[i] == '/' {
			return runID[:i]
		}
	}
	return runID
}

// DeclareTx closes a run's closure inside tx.
//
// A writer whose whole output is one record - an operator's decision, a
// Reality fact, a run receipt - declares in the same transaction that stages
// it, because the alternative leaves a window in which a crash produces a
// staged record whose closure nothing will ever declare: nobody resumes an
// operator's decision, so that record would stay pending forever with no
// remedy. An exploration declares in the transaction that writes its receipt,
// which is the act that ends the run.
func (p *Publisher) DeclareTx(ctx context.Context, tx *sql.Tx, c Closure) error {
	if p == nil {
		return nil
	}
	if _, err := p.journal.declareTx(ctx, tx, c); err != nil {
		return err
	}
	return nil
}

// CommitInline attempts to publish one closure now.
//
// It is called right after the transaction that declared the closure
// committed, and it is best-effort by contract. A failure to reach PostgreSQL
// or the object store, a closure the catalog does not yet hold in full, an
// unregistered instance - all of them leave every record durable and visibly
// pending-sync, hand one line to the diagnostic sink, and return nil. The
// command that produced the record succeeds either way, because SPEC.md §6.5
// makes publication a step that may be completed later and never a step a
// local write depends on.
//
// A returned error is a caller bug and never a transient condition: a closure
// with no staged records, a record whose id or kind the shared catalog cannot
// carry, or a closure already declared at a different size. A write path
// reports it and continues; a test must not ignore it.
func (p *Publisher) CommitInline(ctx context.Context, c Closure) error {
	if p == nil {
		return nil
	}
	run, err := p.journal.declare(ctx, c)
	if err != nil {
		if callerBug(err) {
			return err
		}
		p.report(err)
		return nil
	}
	if _, _, err := p.publish(ctx, run); err != nil {
		p.report(err)
	}
	return nil
}

// callerBug reports whether err describes a malformed request rather than a
// condition a retry can resolve. The distinction is what decides whether a
// write path is told about it or merely diagnoses it: a closure declared twice
// at two sizes is a bug in the writer, while an unreachable database is
// Tuesday.
func callerBug(err error) bool {
	return errors.Is(err, ErrClosureConflict) ||
		errors.Is(err, ErrRecordConflict) ||
		errors.Is(err, ErrRunNotStaged)
}

// Report is what one Retry achieved, and what remains.
//
// It reports both halves on purpose. A sync that says only what it committed
// leaves "and what is still stuck" to be inferred from silence, which is the
// opposite of SPEC.md §9's requirement that staged output be visibly pending.
type Report struct {
	// RunsCommitted counts closures that reached committed this attempt.
	RunsCommitted int
	// RunsPending counts declared closures still pending after it.
	RunsPending int
	// ObjectsWritten counts sealed objects this attempt put and verified. A
	// retry that finds every record already recorded writes none, which is
	// what makes a repeated sync cheap rather than merely harmless.
	ObjectsWritten int
	// Committed counts, per kind, the records this attempt saw through to
	// committed.
	Committed map[sharedcatalog.RecordKind]int
	// Pending counts, per kind, the records still owed to the fleet.
	Pending map[sharedcatalog.RecordKind]int
	// Undeclared counts staged records whose run has declared no closure.
	// They are deliberately unpublishable; see Journal.UndeclaredRecords.
	Undeclared int
	// Failures names each closure that did not publish and why. It is a slice
	// rather than one error because one unreachable record must not hide the
	// nine that published.
	Failures []RunFailure
}

// RunFailure is one closure that did not publish.
type RunFailure struct {
	RunID string
	Err   error
}

// Retry publishes every declared closure the journal still holds as pending.
//
// It is `babel sync` and the reconcile step after an archive push, and it is
// idempotent: a closure the catalog already holds in full costs one presence
// check per record and one conditional flip. Closures are attempted in
// declaration order - the one owed longest first, and a continuation after
// what it continues - and a failure on one does not stop the rest, because
// they are independent commits and a single unreachable object must not strand
// output that would have published.
//
// The returned error is a failure to read the journal at all. Everything a
// closure can fail at is in the Report.
func (p *Publisher) Retry(ctx context.Context) (Report, error) {
	rep := Report{
		Committed: map[sharedcatalog.RecordKind]int{},
		Pending:   map[sharedcatalog.RecordKind]int{},
	}
	if p == nil {
		return rep, nil
	}
	runs, err := p.journal.pendingRuns(ctx)
	if err != nil {
		return rep, err
	}
	for _, run := range runs {
		res, offered, err := p.publish(ctx, run)
		rep.ObjectsWritten += res.ObjectsWritten
		if err != nil {
			rep.Failures = append(rep.Failures, RunFailure{RunID: run.runID, Err: err})
			p.report(err)
			continue
		}
		rep.RunsCommitted++
		for _, rec := range offered {
			rep.Committed[rec.Kind]++
		}
	}

	// The remainder is read after the attempt rather than derived from it: a
	// concurrent writer may have staged more while this ran, and a report that
	// subtracted what it committed from what it started with would understate
	// what is still owed.
	if rep.Pending, err = p.journal.PendingByKind(ctx); err != nil {
		return rep, err
	}
	if rep.Undeclared, err = p.journal.UndeclaredRecords(ctx); err != nil {
		return rep, err
	}
	remaining, err := p.journal.pendingRuns(ctx)
	if err != nil {
		return rep, err
	}
	rep.RunsPending = len(remaining)
	return rep, nil
}

// publish performs the protocol for one declared closure and, only if
// PostgreSQL reported the run committed, flips the local journal.
//
// The ordering is the contract and it is one-directional. internal/sharedcatalog
// writes each missing record's sealed object, reads it back to prove it is
// there, and inserts the row that names it; then it flips the run row, which is
// the visibility boundary, conditional on the whole declared closure being
// present. Only after that transaction has committed does the local journal
// move - so a crash between the two leaves a committed remote run and a local
// journal that still says pending, which the next attempt resolves by finding
// every record already recorded, writing no object, and flipping only.
//
// A local flip that fails after a remote commit is reported and left alone.
// Nothing is lost by it: the remote state is the durable one, and the next
// sync re-derives the same conclusion.
func (p *Publisher) publish(ctx context.Context, run stagedRun) (sharedcatalog.SyncResult, []sharedcatalog.StagedRecord, error) {
	records, err := p.journal.pendingRecords(ctx, run.runID)
	if err != nil {
		return sharedcatalog.SyncResult{}, nil, err
	}
	res, err := sharedcatalog.SyncRun(ctx, p.catalog, p.store, p.ring, sharedcatalog.RunClosure{
		RunID:            run.runID,
		DeploymentID:     p.deploymentID,
		OriginInstanceID: p.instanceID,
		ExecutionHostID:  run.executionHostID,
		ContinuesRunID:   run.continuesRunID,
		RecordCount:      run.recordCount,
		Records:          records,
	})
	if err != nil {
		return res, records, fmt.Errorf("sync: publish run %s: %w", run.runID, err)
	}
	if res.State != sharedcatalog.SyncCommitted {
		return res, records, fmt.Errorf("sync: publish run %s: catalog reports %s", run.runID, res.State)
	}
	if err := p.journal.commit(ctx, run.runID); err != nil {
		return res, records, err
	}
	return res, records, nil
}

// report hands one failure to the diagnostic sink, if there is one. A
// Publisher built without one is silent on purpose: a test asserts on the
// journal rather than on stderr, and a caller that wants the line supplies the
// sink.
func (p *Publisher) report(err error) {
	if p.diag != nil {
		p.diag(err)
	}
}
