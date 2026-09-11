package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/atyrode/babel/internal/worker"
)

// Lifecycle is the state of an attempt, not its publication state. An empty
// lifecycle is a historical completed receipt, not evidence of interruption.
type Lifecycle string

const (
	Running     Lifecycle = "running"
	Interrupted Lifecycle = "interrupted"
	Resumed     Lifecycle = "resumed"
	Closed      Lifecycle = "closed"
)

// Launch records only typed controller inputs. Executables, argv, credentials
// and provider overrides are never recovered from a receipt.
type Launch struct {
	Profile    worker.ProfileRef `json:"profile"`
	Recipes    []string          `json:"recipes"`
	Roots      []string          `json:"roots,omitempty"`
	Prior      []string          `json:"prior,omitempty"`
	ScanRoots  []string          `json:"scan_roots,omitempty"`
	Research   []string          `json:"research,omitempty"`
	Challenge  bool              `json:"challenge"`
	Synthesize bool              `json:"synthesize"`
	Develop    int               `json:"develop"`
	Retrievals int               `json:"retrievals"`
	Fetches    int               `json:"fetches"`
	Params     map[string]string `json:"params,omitempty"`
}

// Verdict distinguishes a successful attempt with warnings from a failed one.
// Nil on older checkpoints means that this explicit verdict was not recorded.
type Verdict struct {
	Failure   string `json:"failure,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

type Checkpoint struct {
	State   Lifecycle `json:"state"`
	Stage   string    `json:"stage,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Launch  *Launch   `json:"launch,omitempty"`
	Records []string  `json:"records,omitempty"`
	Verdict *Verdict  `json:"verdict,omitempty"`
	// Historical marks recovery where the old process never recorded launch
	// provenance. Missing versions/profile are unknown, not current defaults.
	Historical bool     `json:"historical,omitempty"`
	Recipes    []string `json:"known_recipes,omitempty"`
}

const leaseSchema = `CREATE TABLE IF NOT EXISTS run_lease (
 run_id TEXT PRIMARY KEY, host TEXT NOT NULL, pid INTEGER NOT NULL,
 heartbeat TEXT NOT NULL)`

var ErrAttemptOwned = errors.New("run: attempt is owned; reconcile it after the owner is lost")

const ReconcileStaleAfter = 5 * time.Minute

type ReconcileOptions struct {
	RunID string
	// Publish runs while the exact recovered lease is still held.
	Publish func(context.Context, string) error
}

type reconcileCandidate struct {
	id   string
	pid  int
	beat string
}

// BeginAttempt excludes simultaneous controllers and recovery on this local
// database. A dead owner's lease is removed only by explicit reconciliation.
func (s *Store) BeginAttempt(ctx context.Context, id string) (func(), error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO run_lease VALUES (?, ?, ?, ?) ON CONFLICT(run_id) DO NOTHING`, id, host, os.Getpid(), formatTime(time.Now()))
	if err != nil {
		return nil, fmt.Errorf("run: acquire attempt: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrAttemptOwned
	}
	release := s.holdAttempt(id, host)
	return func() {
		prior, err := s.Latest(context.Background(), id)
		unfinished := err != nil && !errors.Is(err, ErrNotFound)
		if err == nil && prior.Body.Checkpoint != nil {
			state := prior.Body.Checkpoint.State
			unfinished = state == Running || state == Resumed
		}
		// A run that reached a terminal receipt and still owes an undeclared
		// closure is not finished for the fleet, whatever its checkpoint says.
		// Judging that by receipt state alone deleted the lease of exactly the
		// runs whose records were stranded: with no lease, no receipt closure
		// and no preparation link, the only evidence left was the staged rows
		// themselves, and 1,022 records sat unpublishable because every
		// recovery path keyed on the evidence this delete removed (issue #152).
		// The lease is the cheapest evidence to keep, so it survives until the
		// closure is declared - Reconcile releases it on the next sweep, and
		// sync.Publisher.SealAbandoned reads it through RunLiveness meanwhile.
		if !unfinished {
			owed, err := s.owesClosure(context.Background(), id)
			unfinished = err != nil || owed
		}
		if unfinished {
			// A failed final receipt must not erase the attempt's owner evidence.
			// Stop heartbeats, but retain the lease for loss reconciliation.
			_ = release(&reconcileCandidate{pid: os.Getpid(), beat: formatTime(time.Now())})
			return
		}
		_ = release(nil)
	}, nil
}

// owesClosure reports whether the publication journal still holds staged
// records for id that no declared closure covers.
//
// It asks the journal's tables directly rather than through internal/sync
// because this store already shares the durable file with them and the
// question is one row's existence; opening a Journal here would be a second
// handle to the file this connection is already holding. A local-only store
// has no such tables at all - nothing was staged, so nothing is owed - which
// is what the presence check answers rather than reporting as a failure.
func (s *Store) owesClosure(ctx context.Context, id string) (bool, error) {
	var staged int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sync_record'`).Scan(&staged); err != nil {
		return false, err
	}
	if staged == 0 {
		return false, nil
	}
	var owed int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sync_record r
		WHERE r.run_id = ? AND r.sync_state = ?
		  AND NOT EXISTS (SELECT 1 FROM sync_run u WHERE u.run_id = r.run_id)`,
		id, SyncPending).Scan(&owed); err != nil {
		return false, err
	}
	return owed != 0, nil
}

func (s *Store) holdAttempt(id, host string) func(*reconcileCandidate) error {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_, _ = s.db.ExecContext(context.Background(), `UPDATE run_lease SET heartbeat = ? WHERE run_id = ? AND host = ? AND pid = ?`, formatTime(time.Now()), id, host, os.Getpid())
			}
		}
	}()
	return func(restore *reconcileCandidate) error {
		close(stop)
		<-done
		if restore != nil {
			_, err := s.db.ExecContext(context.Background(), `UPDATE run_lease SET pid = ?, heartbeat = ? WHERE run_id = ? AND host = ? AND pid = ?`, restore.pid, restore.beat, id, host, os.Getpid())
			return err
		}
		_, err := s.db.ExecContext(context.Background(), `DELETE FROM run_lease WHERE run_id = ? AND host = ? AND pid = ?`, id, host, os.Getpid())
		return err
	}
}

func (s *Store) Latest(ctx context.Context, id string) (Receipt, error) {
	revisions, err := s.Revisions(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	return revisions[len(revisions)-1], nil
}

// Transition extends the immutable receipt chain and publishes through the
// existing continuation-of-one path when the partial closure already exists.
func (s *Store) Transition(ctx context.Context, prior Receipt, state Lifecycle, reason string) (Receipt, error) {
	body := prior.Body
	if body.Checkpoint == nil {
		body.Checkpoint = &Checkpoint{}
	} else {
		cp := *body.Checkpoint
		body.Checkpoint = &cp
	}
	if body.Checkpoint.Verdict == nil {
		if state == Interrupted {
			body.Checkpoint.Verdict = &Verdict{Failure: reason}
		} else if state == Closed && body.Checkpoint.State == Interrupted {
			failure := body.Checkpoint.Reason
			if failure == "" {
				failure = "interrupted run was deliberately closed"
			}
			body.Checkpoint.Verdict = &Verdict{Failure: failure}
		}
	}
	body.Checkpoint.State, body.Checkpoint.Reason = state, reason
	body.AmendmentReason = reason
	body.Timing.FinishedAt = time.Now().UTC()
	next, err := Amend(prior, NewReceiptID(), body, time.Now().UTC())
	if err != nil {
		return Receipt{}, err
	}
	if err = s.PutReceipt(ctx, next); err != nil {
		return Receipt{}, err
	}
	return next, nil
}

func (s *Store) Interrupted(ctx context.Context) ([]Receipt, error) {
	out := []Receipt{}
	for offset := 0; ; {
		page, total, err := s.Receipts(ctx, MaxListLimit, offset)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			if r.Body.Checkpoint != nil && r.Body.Checkpoint.State == Interrupted {
				out = append(out, r)
			}
		}
		offset += len(page)
		if offset >= total || len(page) == 0 {
			return out, nil
		}
	}
}

// Reconcile only considers local ownership with a stale heartbeat AND a dead
// process. A stale heartbeat alone cannot establish that a worker stopped.
// The cause is unknown process loss, never an inferred quota or signal.
func (s *Store) Reconcile(ctx context.Context, before time.Time, opt ReconcileOptions) ([]Receipt, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, pid, heartbeat FROM run_lease WHERE host = ? AND heartbeat < ? AND (? = '' OR run_id = ?)`, host, formatTime(before), opt.RunID, opt.RunID)
	if err != nil {
		return nil, err
	}
	var candidates []reconcileCandidate
	for rows.Next() {
		var c reconcileCandidate
		if err := rows.Scan(&c.id, &c.pid, &c.beat); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Receipt{}
	for _, c := range candidates {
		recovered, err := s.reconcileCandidate(ctx, host, c, opt)
		if err != nil {
			return out, err
		}
		if recovered != nil {
			out = append(out, *recovered)
		}
	}
	return out, nil
}

// processGone reports that the pid no longer exists on this machine.
//
// Only two answers are dead: the process is done, or no such process. Every
// other outcome - an unreadable pid, a signal refused because the process
// belongs to another user - reports alive, because reconciliation and sealing
// both act on death and neither may act on ignorance. A recorded pid of zero
// or less is not evidence of anything and is therefore not evidence of loss.
//
// It is one helper rather than the same five lines in two places because the
// two callers must agree: reconcileCandidate claims a dead owner's lease and
// RunLiveness decides whether a run's closure may be sealed at what it
// reached, and a probe that differed between them would let one conclude the
// owner is gone while the other waits for it forever.
func processGone(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	p.Release()
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func (s *Store) reconcileCandidate(ctx context.Context, host string, c reconcileCandidate, opt ReconcileOptions) (_ *Receipt, err error) {
	if !processGone(c.pid) {
		return nil, nil
	}
	// Compare-and-swap the exact observed lease BEFORE reading the receipt.
	// Another reconciler may have released this lease and a live resume may
	// already own its successor. Old process-loss evidence cannot claim it.
	claimed, err := s.db.ExecContext(ctx, `UPDATE run_lease SET pid=?, heartbeat=? WHERE run_id=? AND host=? AND pid=? AND heartbeat=?`, os.Getpid(), formatTime(time.Now()), c.id, host, c.pid, c.beat)
	if err != nil {
		return nil, err
	}
	count, err := claimed.RowsAffected()
	if err != nil || count == 0 {
		return nil, err
	}
	release := s.holdAttempt(c.id, host)
	defer func() {
		if err != nil {
			// Restore the exact dead-owner evidence only after heartbeats stop.
			// A failed amendment or publication must remain reconcilable.
			err = errors.Join(err, release(&c))
		} else {
			err = release(nil)
		}
	}()
	commit := context.WithoutCancel(ctx)
	prior, err := s.Latest(commit, c.id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cp := prior.Body.Checkpoint
	if cp == nil {
		return nil, nil
	}
	next := prior
	if cp.State == Running || cp.State == Resumed {
		records, _, err := s.KnownRecords(commit, c.id)
		if err != nil {
			return nil, err
		}
		copy := *cp
		copy.Records = records
		prior.Body.Checkpoint = &copy
		next, err = s.Transition(commit, prior, Interrupted, "unknown process loss (stale local heartbeat; owner no longer exists)")
		if err != nil {
			return nil, err
		}
	}
	if err := s.DeclareClosure(commit, c.id); err != nil {
		return nil, err
	}
	if opt.Publish != nil {
		if err := opt.Publish(commit, c.id); err != nil {
			return nil, err
		}
	}
	if next.Body.Checkpoint.State == Interrupted {
		return &next, nil
	}
	return nil, nil
}

// RunLiveness reports whether a run may still produce records, and when it may
// not, names the evidence that was actually missing.
//
// It is the proof sync.Publisher.SealAbandoned refuses to seal without. A
// closure declares a run's record_count and migration 0003 never lets that
// count move, so a run sealed while it could still grow would be permanently
// short of its own output - which makes "is this run over" a question that has
// to be answered from evidence rather than from the absence of evidence.
//
// Two things make a run live, and either is enough. A held lease whose process
// still exists is an attempt in flight on this machine. A latest receipt
// standing at running or resumed is a run `babel runs resume` may continue
// under the same id, and a resumed run grows - so a stale-looking receipt is
// not licence to seal; Reconcile is what turns that state into an interrupted
// receipt and a declared closure, and it is the path that may do so.
//
// why is the operator's measurement of an abandonment, not a diagnostic
// string, which is why the cases are distinguishable rather than one sentence
// with an if in it: a run whose owner was killed reads differently from one
// that finished and never declared, and differently again from one that left
// nothing behind but the records it staged. Babel should never abandon a run,
// so the cases where it had to are what tell an operator what to fix
// (issue #152).
func (s *Store) RunLiveness(ctx context.Context, runID string) (bool, string, error) {
	var pid int
	err := s.db.QueryRowContext(ctx, `SELECT pid FROM run_lease WHERE run_id = ?`, runID).Scan(&pid)
	leased := err == nil
	switch {
	case leased:
		if !processGone(pid) {
			return true, "", nil
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return false, "", fmt.Errorf("run: read the attempt lease for %s: %w", runID, err)
	}

	latest, err := s.Latest(ctx, runID)
	switch {
	case errors.Is(err, ErrNotFound):
		if leased {
			return false, "the owning process no longer exists and it never wrote a receipt", nil
		}
		// This is the state that stranded 1,022 records: no lease to
		// reconcile, no receipt to declare from, and - because the durable
		// file links a run to its preparation through the receipt alone - no
		// preparation to recover historically from either.
		return false, "no lease and no receipt survive this run, so nothing links it to a preparation " +
			"either; only the staged records remain", nil
	case err != nil:
		return false, "", err
	}
	cp := latest.Body.Checkpoint
	if cp == nil {
		// An empty lifecycle is a historical completed receipt rather than
		// evidence of interruption, so the run is over and was never
		// interrupted; it simply predates the checkpoint.
		return false, "the receipt records no lifecycle checkpoint, so this run is historical and over", nil
	}
	if cp.State == Running || cp.State == Resumed {
		return true, "", nil
	}
	if leased {
		return false, fmt.Sprintf("the owning process no longer exists; its receipt reached %s "+
			"without declaring a closure", cp.State), nil
	}
	return false, fmt.Sprintf("the receipt reached %s without declaring a closure, and no lease "+
		"survives to reconcile", cp.State), nil
}

// AttemptOwned is a conservative admission check. BeginAttempt remains the
// atomic authority if another process races this read.
func (s *Store) AttemptOwned(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM run_lease WHERE run_id=?`, id).Scan(&count)
	return count != 0, err
}

// CloseInterrupted is an operator decision, never a conclusion inferred from
// an old heartbeat. Acquiring ownership excludes a concurrent resume.
func (s *Store) CloseInterrupted(ctx context.Context, id string) (Receipt, error) {
	release, err := s.BeginAttempt(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	defer release()
	prior, err := s.Latest(ctx, id)
	if err != nil {
		return Receipt{}, err
	}
	if prior.Body.Checkpoint == nil || prior.Body.Checkpoint.State != Interrupted {
		return Receipt{}, fmt.Errorf("run: only an interrupted run can be closed")
	}
	next, err := s.Transition(ctx, prior, Closed, "operator deliberately closed the interrupted run")
	if err != nil {
		return Receipt{}, err
	}
	return next, s.DeclareClosure(ctx, id)
}

// KnownRecords reads the existing immutable resume ledger, without creating
// invented bindings when historical runs predate it.
func (s *Store) KnownRecords(ctx context.Context, id string) ([]string, string, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='explore_commit'`).Scan(&exists); err != nil {
		return nil, "", err
	}
	if exists == 0 {
		return nil, "", nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entity_id, stage FROM explore_commit WHERE run_id = ? ORDER BY recorded_at, stage, ref`, id)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	records := []string{}
	stage := ""
	seen := map[string]bool{}
	for rows.Next() {
		var record string
		if err := rows.Scan(&record, &stage); err != nil {
			return nil, "", err
		}
		if !seen[record] {
			seen[record] = true
			records = append(records, record)
		}
	}
	return records, stage, rows.Err()
}

// RecoverHistorical records only facts actually known from an old local
// presence row and its durable preparation/ledger. The caller has established
// that every announcement of this run is stale and local, with no receipt.
func (s *Store) RecoverHistorical(ctx context.Context, id string, prepID PreparationID, authority Authority, recipe string, started time.Time, opt ReconcileOptions) (*Receipt, error) {
	var owned int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM run_lease WHERE run_id=?`, id).Scan(&owned); err != nil {
		return nil, err
	}
	if owned != 0 {
		return nil, nil
	}
	if _, err := s.Latest(ctx, id); err == nil {
		return nil, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	prep, err := s.Preparation(ctx, prepID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records, stage, err := s.KnownRecords(ctx, id)
	if err != nil {
		return nil, err
	}
	// Local preparation alone may be replicated. Require locally committed
	// output as well before claiming custody of an old receipt-less run.
	if len(records) == 0 {
		return nil, nil
	}
	release, err := s.BeginAttempt(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = context.WithoutCancel(ctx)
	if _, err := s.Latest(ctx, id); err == nil {
		return nil, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	cp := &Checkpoint{State: Interrupted, Stage: stage, Records: records, Historical: true,
		Reason: "unknown process loss (stale local presence; historical launch checkpoint was never recorded)"}
	if recipe != "" {
		cp.Recipes = []string{recipe}
	}
	body := Body{Checkpoint: cp, Timing: Timing{StartedAt: started, FinishedAt: now}}
	receipt, err := NewReceipt(NewReceiptID(), id, prep, authority, body, now)
	if err != nil {
		return nil, err
	}
	if err := s.PutReceipt(ctx, receipt); err != nil {
		return nil, err
	}
	if err := s.DeclareClosure(ctx, id); err != nil {
		return nil, err
	}
	if opt.Publish != nil {
		if err := opt.Publish(ctx, id); err != nil {
			return nil, err
		}
	}
	return &receipt, nil
}
