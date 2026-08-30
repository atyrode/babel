package sharedcatalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/atyrode/babel/internal/envelope"
)

// These tests live inside the package so they can read the raw rows the commit
// protocol writes. What they defend is SPEC.md 6.5's object-first,
// database-last ordering: no row may name an absent object, an interrupted sync
// stays visibly pending-sync, and finishing it is idempotent.

// sentinel is a synthetic marker that must never appear anywhere outside the
// producing process: not in a PostgreSQL column, not in a stored object. It is
// the same technique SPEC.md 10 requires of the terminal and web surfaces -
// prove no known sentinel leaks - applied to the durable boundary.
const sentinel = "BABEL-SYNTHETIC-CLAIM-SENTINEL-do-not-leak"

// memStore is the in-memory object store the tests inject. Failures are
// programmable per key so the protocol's two remote failure points - the write
// and the read-back that verifies it - can be exercised separately.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	gets    int

	// failPut, if set, decides whether a put fails.
	failPut func(key string) error
	// corruptGet, if set, replaces what a read-back returns.
	corruptGet func(key string, stored []byte) []byte
	// onGet runs after a successful read-back, for tests that need to
	// interfere with the world between the object write and the row write.
	onGet func(key string)
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failPut != nil {
		if err := s.failPut(key); err != nil {
			return err
		}
	}
	s.puts++
	s.objects[key] = append([]byte(nil), data...)
	return nil
}

func (s *memStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	stored, ok := s.objects[key]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("object %q not found", key)
	}
	s.gets++
	out := append([]byte(nil), stored...)
	if s.corruptGet != nil {
		out = s.corruptGet(key, out)
	}
	hook := s.onGet
	s.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	return out, nil
}

func (s *memStore) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

func (s *memStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

func newKeyring(t *testing.T) *envelope.Keyring {
	t.Helper()
	key, err := envelope.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ring, err := envelope.NewKeyring("test-key-1", key)
	if err != nil {
		t.Fatalf("new keyring: %v", err)
	}
	return ring
}

// sampleClosure builds a run whose payloads all carry the sentinel, so any
// leak of plaintext into either store is detectable.
func sampleClosure(runID string, kinds ...RecordKind) RunClosure {
	c := RunClosure{
		RunID:            runID,
		DeploymentID:     "d1",
		OriginInstanceID: "inst-a",
		RecordCount:      len(kinds),
	}
	for i, kind := range kinds {
		c.Records = append(c.Records, StagedRecord{
			RecordID: fmt.Sprintf("%s-rec-%d", runID, i),
			Kind:     kind,
			Schema:   1,
			Ordinal:  int64(i),
			Payload: []byte(fmt.Sprintf(
				`{"claim":%q,"index":%d}`, sentinel, i)),
		})
	}
	return c
}

func mustSync(t *testing.T, db *sql.DB, store ObjectStore, ring *envelope.Keyring, c RunClosure) SyncResult {
	t.Helper()
	res, err := SyncRun(context.Background(), db, store, ring, c)
	if err != nil {
		t.Fatalf("sync run %s: %v", c.RunID, err)
	}
	return res
}

// seedPhaseB registers the deployment, hosts, and instances a run's foreign
// keys require, exactly as Register does on a real machine.
func seedPhaseB(t *testing.T, db *sql.DB) {
	t.Helper()
	seedHost(t, db, "h1")
	seedHost(t, db, "h2")
}

// The whole reason PostgreSQL may hold Phase B rows at all is that they carry
// no payload (SPEC.md 9). This scans every text-shaped value in Babel's schema
// rather than the columns the test happens to know about, so a future column
// that carries plaintext fails here even if nobody updated this test.
func TestSealedPayloadNeverReachesPostgres(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)

	c := sampleClosure("run-seal", KindHypothesis, KindObservation)
	mustSync(t, db, store, ring, c)

	for _, hit := range scanSchemaForText(t, db, sentinel) {
		t.Errorf("plaintext sentinel reached PostgreSQL in %s", hit)
	}

	// Positive control: the scan's silence means nothing unless the scan
	// actually reads row values. A marker written into an allowlisted opaque
	// id must be found by the same call that found no sentinel.
	const control = "SCAN-CONTROL-MARKER"
	if _, err := db.Exec(
		`INSERT INTO hosts (host_id, deployment_id) VALUES ($1, 'd1')`, control); err != nil {
		t.Fatalf("seed control marker: %v", err)
	}
	if hits := scanSchemaForText(t, db, control); len(hits) == 0 {
		t.Fatal("the plaintext scan did not find a marker written straight into a column; " +
			"it is not reading row values, so its verdict above is vacuous")
	}

	// And it is not merely absent from PostgreSQL: the object that left the
	// machine is ciphertext too.
	rows, err := AnalysisRecords(context.Background(), db, c.RunID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("listed %d records, want 2", len(rows))
	}
	for _, r := range rows {
		object, err := store.Get(context.Background(), r.ObjectKey)
		if err != nil {
			t.Fatalf("read object: %v", err)
		}
		if bytes.Contains(object, []byte(sentinel)) {
			t.Errorf("record %s: the stored object contains plaintext", r.RecordID)
		}
		if r.CiphertextSize <= 0 {
			t.Errorf("record %s: ciphertext size %d", r.RecordID, r.CiphertextSize)
		}
		if r.KeyID != ring.ActiveKeyID() {
			t.Errorf("record %s: key id %q, want %q", r.RecordID, r.KeyID, ring.ActiveKeyID())
		}
		// The digest must be over the sealed bytes: a plaintext digest would
		// be the deterministic search oracle SPEC.md 9 forbids.
		if r.ObjectDigest != digestOf(object) {
			t.Errorf("record %s: recorded digest does not match the stored object", r.RecordID)
		}
	}

	// Two records with identical payloads must not produce identical stored
	// bytes; randomized sealing is what stops an observer matching rows.
	same := RunClosure{
		RunID: "run-twins", DeploymentID: "d1", OriginInstanceID: "inst-a",
		RecordCount: 2,
		Records: []StagedRecord{
			{RecordID: "twin-0", Kind: KindFinding, Schema: 1, Ordinal: 0, Payload: []byte(sentinel)},
			{RecordID: "twin-1", Kind: KindFinding, Schema: 1, Ordinal: 1, Payload: []byte(sentinel)},
		},
	}
	mustSync(t, db, store, ring, same)
	twins, err := AnalysisRecords(context.Background(), db, same.RunID)
	if err != nil {
		t.Fatalf("list twins: %v", err)
	}
	if twins[0].ObjectDigest == twins[1].ObjectDigest {
		t.Error("identical payloads produced identical ciphertext digests")
	}
}

// scanSchemaForText reads every character-typed column of every table in
// Babel's schema and reports where needle appears.
func scanSchemaForText(t *testing.T, db *sql.DB, needle string) []string {
	t.Helper()
	ctx := context.Background()
	cols, err := db.QueryContext(ctx, `
		SELECT table_name, column_name
		  FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND data_type IN ('text', 'character varying', 'character', 'json', 'jsonb', 'bytea')
		 ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("list text columns: %v", err)
	}
	type ref struct{ table, column string }
	var refs []ref
	for cols.Next() {
		var r ref
		if err := cols.Scan(&r.table, &r.column); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		refs = append(refs, r)
	}
	cols.Close()
	if err := cols.Err(); err != nil {
		t.Fatalf("list text columns: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("no text columns found; this scan would pass vacuously")
	}

	var hits []string
	for _, r := range refs {
		var n int
		// Identifiers come from information_schema, not from a caller, and are
		// quoted by PostgreSQL's own format().
		var stmt string
		if err := db.QueryRowContext(ctx,
			`SELECT format('SELECT count(*) FROM %I WHERE %I::text LIKE $1', $1::text, $2::text)`,
			r.table, r.column).Scan(&stmt); err != nil {
			t.Fatalf("render scan statement: %v", err)
		}
		if err := db.QueryRowContext(ctx, stmt, "%"+needle+"%").Scan(&n); err != nil {
			t.Fatalf("scan %s.%s: %v", r.table, r.column, err)
		}
		if n > 0 {
			hits = append(hits, fmt.Sprintf("%s.%s (%d rows)", r.table, r.column, n))
		}
	}
	return hits
}

// Object first, database last: if the object store refuses the write, or
// returns something other than what was written, no row may name it. A row
// pointing at an absent object is a dangling claim that no later sync can
// repair, because the plaintext that produced it is gone.
func TestObjectFailureLeavesNoRowNamingAMissingObject(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*memStore, string)
		want  error
	}{
		{
			name: "the store refuses the write",
			setup: func(s *memStore, failFor string) {
				s.failPut = func(key string) error {
					if strings.Contains(key, failFor) {
						return errors.New("object store unavailable")
					}
					return nil
				}
			},
		},
		{
			name: "the store returns different bytes",
			setup: func(s *memStore, failFor string) {
				s.corruptGet = func(key string, stored []byte) []byte {
					if strings.Contains(key, failFor) {
						return append(stored, ' ')
					}
					return stored
				}
			},
			want: ErrObjectVerification,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newInternalDB(t)
			seedPhaseB(t, db)
			store, ring := newMemStore(), newKeyring(t)
			ctx := context.Background()

			c := sampleClosure("run-objfail", KindHypothesis, KindObservation, KindFinding)
			tc.setup(store, c.Records[1].RecordID)

			res, err := SyncRun(ctx, db, store, ring, c)
			if err == nil {
				t.Fatal("SyncRun succeeded despite an object-store failure")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if res.State != SyncPending {
				t.Errorf("state = %q, want %q", res.State, SyncPending)
			}

			// The failing record has no row, and the record after it was never
			// attempted - the protocol stops rather than skipping ahead.
			rows, err := AnalysisRecords(ctx, db, c.RunID)
			if err != nil {
				t.Fatalf("list records: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("catalog holds %d record rows, want 1", len(rows))
			}
			// Every row the catalog does hold names an object that is present
			// and matches its digest. This is the invariant, checked directly.
			for _, r := range rows {
				if !store.has(r.ObjectKey) {
					t.Fatalf("row %s names object %q, which the store does not hold",
						r.RecordID, r.ObjectKey)
				}
				object, err := store.Get(ctx, r.ObjectKey)
				if err != nil {
					t.Fatalf("read object: %v", err)
				}
				if digestOf(object) != r.ObjectDigest {
					t.Errorf("row %s names an object with a different digest", r.RecordID)
				}
			}

			run, err := AnalysisRun(ctx, db, c.RunID)
			if err != nil {
				t.Fatalf("read run: %v", err)
			}
			if run.SyncState != SyncPending {
				t.Errorf("run state = %q, want %q", run.SyncState, SyncPending)
			}
			if run.CommittedAt != nil {
				t.Error("a pending run carries a commit time")
			}
		})
	}
}

// A database failure after a successful object write is the case SPEC.md 6.5
// names explicitly: the run stays visibly pending-sync, and an idempotent later
// sync completes it. The failure here is real rather than injected - the
// context is cancelled while the last object is being verified, so the insert
// that follows meets a cancelled connection.
func TestDatabaseFailureAfterObjectWriteLeavesPendingSyncAndResumes(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)

	c := sampleClosure("run-outage", KindHypothesis, KindObservation, KindFinding)
	last := c.Records[len(c.Records)-1].RecordID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.onGet = func(key string) {
		if strings.Contains(key, last) {
			cancel()
		}
	}

	res, err := SyncRun(ctx, db, store, ring, c)
	if err == nil {
		t.Fatal("SyncRun succeeded despite the database going away mid-sync")
	}
	if res.State != SyncPending {
		t.Errorf("state = %q, want %q", res.State, SyncPending)
	}
	store.onGet = nil

	// The object for the interrupted record is durable; its row is not.
	fresh := context.Background()
	rows, err := AnalysisRecords(fresh, db, c.RunID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(rows) != len(c.Records)-1 {
		t.Fatalf("catalog holds %d record rows, want %d", len(rows), len(c.Records)-1)
	}
	if store.putCount() != len(c.Records) {
		t.Fatalf("store holds %d objects, want %d: the last object must be durable",
			store.putCount(), len(c.Records))
	}

	// The run is visible, and visibly not committed. An instance browsing the
	// deployment must not see it among reviewable output.
	run, err := AnalysisRun(fresh, db, c.RunID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.SyncState != SyncPending {
		t.Fatalf("run state = %q, want %q", run.SyncState, SyncPending)
	}
	if run.RecordsPresent != len(c.Records)-1 || run.RecordCount != len(c.Records) {
		t.Errorf("run reports %d of %d records, want %d of %d",
			run.RecordsPresent, run.RecordCount, len(c.Records)-1, len(c.Records))
	}
	committed, err := CommittedAnalysisRuns(fresh, db, c.DeploymentID)
	if err != nil {
		t.Fatalf("list committed runs: %v", err)
	}
	if len(committed) != 0 {
		t.Errorf("a pending run appeared among %d committed runs", len(committed))
	}

	// Reconnection completes it, and completes it exactly once: the records
	// that already landed are neither re-sealed nor re-uploaded.
	putsBefore := store.putCount()
	again := mustSync(t, db, store, ring, c)
	if again.State != SyncCommitted {
		t.Fatalf("state after resync = %q, want %q", again.State, SyncCommitted)
	}
	if again.ObjectsWritten != 1 || again.RecordsCommitted != 1 {
		t.Errorf("resync wrote %d objects and %d rows, want 1 and 1",
			again.ObjectsWritten, again.RecordsCommitted)
	}
	if got := store.putCount() - putsBefore; got != 1 {
		t.Errorf("resync put %d objects, want 1", got)
	}
	if n := countRows(t, db, "analysis_records"); n != len(c.Records) {
		t.Errorf("catalog holds %d record rows, want %d", n, len(c.Records))
	}
	if n := countRows(t, db, "analysis_runs"); n != 1 {
		t.Errorf("catalog holds %d run rows, want 1", n)
	}
}

// A repeated commit of an already-committed run is a no-op, not a second row
// and not a second object. This is what makes an interrupted push safe to
// retry blindly (SPEC.md 7).
func TestRepeatedCommitIsANoOp(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)

	c := sampleClosure("run-idem", KindProposal, KindReceipt)
	first := mustSync(t, db, store, ring, c)
	if first.State != SyncCommitted || first.RecordsCommitted != 2 {
		t.Fatalf("first sync = %+v", first)
	}
	before, err := AnalysisRecords(context.Background(), db, c.RunID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	puts := store.putCount()

	for i := range 2 {
		res := mustSync(t, db, store, ring, c)
		if res.State != SyncCommitted {
			t.Fatalf("repeat %d: state = %q", i, res.State)
		}
		if res.RecordsCommitted != 0 || res.ObjectsWritten != 0 {
			t.Errorf("repeat %d wrote %d rows and %d objects, want none",
				i, res.RecordsCommitted, res.ObjectsWritten)
		}
	}
	if store.putCount() != puts {
		t.Errorf("repeats put %d further objects", store.putCount()-puts)
	}
	if n := countRows(t, db, "analysis_records"); n != 2 {
		t.Errorf("catalog holds %d record rows, want 2", n)
	}

	// Idempotency is keyed on the global ids, not on content: a repeat seals
	// nothing new, so the objects a committed row names are untouched.
	after, err := AnalysisRecords(context.Background(), db, c.RunID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("record %s changed across a repeated commit:\n  %+v\n  %+v",
				before[i].RecordID, before[i], after[i])
		}
	}
}

// A run commits only with its complete closure. Offering part of it leaves the
// run pending: a partial commit is not a commit (SPEC.md 6.5).
func TestIncompleteClosureIsRefusedAsACommit(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	full := sampleClosure("run-closure", KindHypothesis, KindObservation, KindFinding)
	partial := full
	partial.Records = full.Records[:2]

	res, err := SyncRun(ctx, db, store, ring, partial)
	if !errors.Is(err, ErrIncompleteClosure) {
		t.Fatalf("error = %v, want ErrIncompleteClosure", err)
	}
	if res.State != SyncPending {
		t.Errorf("state = %q, want %q", res.State, SyncPending)
	}
	if res.RecordsPresent != 2 {
		t.Errorf("records present = %d, want 2", res.RecordsPresent)
	}

	run, err := AnalysisRun(ctx, db, full.RunID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run.SyncState != SyncPending || run.CommittedAt != nil {
		t.Errorf("run = %q committed at %v, want pending and uncommitted", run.SyncState, run.CommittedAt)
	}

	// The records that did land are kept, which is what makes completing the
	// closure cheap rather than a full redo.
	putsBefore := store.putCount()
	done := mustSync(t, db, store, ring, full)
	if done.State != SyncCommitted {
		t.Fatalf("state = %q, want %q", done.State, SyncCommitted)
	}
	if got := store.putCount() - putsBefore; got != 1 {
		t.Errorf("completing the closure put %d objects, want 1", got)
	}
	if n := countRows(t, db, "analysis_records"); n != 3 {
		t.Errorf("catalog holds %d record rows, want 3", n)
	}
}

// Global identity is the point of committing to a shared backend: a second
// instance, on another host and another connection, reads what the first
// committed, recovers its content, and continues from it (SPEC.md 4.7).
func TestSecondInstanceReadsAndContinuesACommittedRun(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	first := sampleClosure("run-first", KindHypothesis)
	first.ExecutionHostID = "h1"
	mustSync(t, db, store, ring, first)

	// A genuinely separate connection, as a second machine would open.
	second := reconnect(t, db)

	runs, err := CommittedAnalysisRuns(ctx, second, "d1")
	if err != nil {
		t.Fatalf("browse committed runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != first.RunID {
		t.Fatalf("browse returned %+v, want the one committed run", runs)
	}
	if runs[0].OriginInstanceID != "inst-a" {
		t.Errorf("origin = %q, want inst-a", runs[0].OriginInstanceID)
	}

	rows, err := AnalysisRecords(ctx, second, first.RunID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("listed %d records, want 1", len(rows))
	}
	plaintext, err := OpenRecord(ctx, store, ring, rows[0])
	if err != nil {
		t.Fatalf("open record: %v", err)
	}
	if !bytes.Contains(plaintext, []byte(sentinel)) {
		t.Fatalf("recovered payload does not match what was sealed")
	}

	// Continuing: instance B commits its own run, linked to the one it read.
	follow := sampleClosure("run-second", KindObservation, KindFinding)
	follow.OriginInstanceID = "inst-b"
	follow.ContinuesRunID = first.RunID
	if _, err := SyncRun(ctx, second, store, ring, follow); err != nil {
		t.Fatalf("continue from a committed run: %v", err)
	}

	linked, err := AnalysisRun(ctx, db, follow.RunID)
	if err != nil {
		t.Fatalf("read continued run: %v", err)
	}
	if linked.ContinuesRunID != first.RunID {
		t.Errorf("continues = %q, want %q", linked.ContinuesRunID, first.RunID)
	}
	if linked.OriginInstanceID != "inst-b" {
		t.Errorf("origin = %q, want inst-b", linked.OriginInstanceID)
	}

	// The first instance's run is untouched by any of it.
	original, err := AnalysisRun(ctx, db, first.RunID)
	if err != nil {
		t.Fatalf("re-read the original run: %v", err)
	}
	if original.SyncState != SyncCommitted || original.RecordsPresent != 1 {
		t.Errorf("original run changed: %+v", original)
	}
}

// A host pin records which machine can rerun repository-dependent work. It must
// not become an access control: the reason the output is committed globally is
// that any instance can read it (SPEC.md 4.7, 9).
func TestHostPinnedRunStaysReadableEverywhere(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	pinned := sampleClosure("run-pinned", KindObservation)
	pinned.ExecutionHostID = "h2"
	mustSync(t, db, store, ring, pinned)

	unpinned := sampleClosure("run-unpinned", KindObservation)
	mustSync(t, db, store, ring, unpinned)

	other := reconnect(t, db)
	run, err := AnalysisRun(ctx, other, pinned.RunID)
	if err != nil {
		t.Fatalf("read pinned run from another instance: %v", err)
	}
	if run.ExecutionHostID != "h2" {
		t.Errorf("execution host = %q, want h2", run.ExecutionHostID)
	}
	rows, err := AnalysisRecords(ctx, other, pinned.RunID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("records = %v, err = %v; a pin must not restrict reads", rows, err)
	}
	if _, err := OpenRecord(ctx, store, ring, rows[0]); err != nil {
		t.Fatalf("open a pinned run's record from elsewhere: %v", err)
	}

	free, err := AnalysisRun(ctx, other, unpinned.RunID)
	if err != nil {
		t.Fatalf("read unpinned run: %v", err)
	}
	if free.ExecutionHostID != "" {
		t.Errorf("unpinned run reports host %q", free.ExecutionHostID)
	}
}

// The allowlist gate must keep working for Phase B: a table or column that
// arrives without an entry is exactly the accident it exists to catch.
func TestVerifyRefusesAnUnlistedPhaseBTable(t *testing.T) {
	db := newInternalDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`CREATE TABLE analysis_notes (record_id text PRIMARY KEY, note text)`); err != nil {
		t.Fatalf("create stray table: %v", err)
	}
	err := Verify(ctx, db)
	if err == nil {
		t.Fatal("Verify accepted a Phase B table that is not on the allowlist")
	}
	if !strings.Contains(err.Error(), "analysis_notes") {
		t.Fatalf("error must name the unlisted table, got: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE analysis_notes`); err != nil {
		t.Fatalf("drop stray table: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE analysis_records ADD COLUMN claim text`); err != nil {
		t.Fatalf("add stray column: %v", err)
	}
	err = Verify(ctx, db)
	if err == nil {
		t.Fatal("Verify accepted a plaintext column on a Phase B table")
	}
	if !strings.Contains(err.Error(), "analysis_records.claim") {
		t.Fatalf("error must name the unlisted column, got: %v", err)
	}
}

// Global ids mean global: the same id may not describe two different runs or
// belong to two different runs, because every instance resolves lineage through
// them.
func TestGlobalIdentityConflictsAreRefused(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	c := sampleClosure("run-identity", KindHypothesis)
	mustSync(t, db, store, ring, c)

	redeclared := c
	redeclared.RecordCount = 2
	redeclared.Records = append([]StagedRecord(nil), c.Records...)
	if _, err := SyncRun(ctx, db, store, ring, redeclared); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("error = %v, want ErrRunConflict", err)
	}

	stolen := sampleClosure("run-thief", KindHypothesis)
	stolen.Records[0].RecordID = c.Records[0].RecordID
	if _, err := SyncRun(ctx, db, store, ring, stolen); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("error = %v, want ErrRecordConflict", err)
	}
}

// The append-only guarantee is the database's, not this package's: SPEC.md 4.7
// says rejection never deletes a record, and these rows are the only copy.
func TestAnalysisRowsCannotBeRewrittenOrDeleted(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)

	c := sampleClosure("run-immutable", KindFinding)
	mustSync(t, db, store, ring, c)

	for _, tc := range []struct{ name, stmt string }{
		{"delete a record", `DELETE FROM analysis_records`},
		{"rewrite a record", `UPDATE analysis_records SET object_key = 'elsewhere'`},
		{"delete a run", `DELETE FROM analysis_runs`},
		{"redeclare a closure", `UPDATE analysis_runs SET record_count = 9`},
		{"uncommit a run", `UPDATE analysis_runs SET sync_state = 'pending-sync', committed_at = NULL`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(tc.stmt); err == nil {
				t.Fatalf("the database accepted %q", tc.stmt)
			}
		})
	}
	if n := countRows(t, db, "analysis_records"); n != 1 {
		t.Errorf("catalog holds %d record rows, want 1", n)
	}
}

// Validation refuses a closure that could not be committed correctly, before
// anything is sealed or written. Identifier shape is included because these ids
// are spliced into object-store keys.
func TestClosureValidationRefusesUnsafeInput(t *testing.T) {
	base := func() RunClosure { return sampleClosure("run-valid", KindHypothesis) }

	for _, tc := range []struct {
		name  string
		mutex func(*RunClosure)
		want  string
	}{
		{"empty run id", func(c *RunClosure) { c.RunID = "" }, "run id"},
		{"traversal in a run id", func(c *RunClosure) { c.RunID = "../escape" }, "run id"},
		{"slash in a record id", func(c *RunClosure) { c.Records[0].RecordID = "a/b" }, "record id"},
		{"no deployment", func(c *RunClosure) { c.DeploymentID = "" }, "deployment id"},
		{"self continuation", func(c *RunClosure) { c.ContinuesRunID = c.RunID }, "continue itself"},
		{"empty closure", func(c *RunClosure) { c.RecordCount = 0 }, "at least one record"},
		{"more records than declared", func(c *RunClosure) {
			c.Records = append(c.Records, c.Records[0])
			c.Records[1].RecordID = "extra"
			c.Records[1].Ordinal = 1
		}, "were offered"},
		{"unknown kind", func(c *RunClosure) { c.Records[0].Kind = "verdict" }, "unknown kind"},
		{"no record schema", func(c *RunClosure) { c.Records[0].Schema = 0 }, "schema version"},
		{"ordinal outside the closure", func(c *RunClosure) { c.Records[0].Ordinal = 7 }, "outside a closure"},
		{"empty payload", func(c *RunClosure) { c.Records[0].Payload = nil }, "empty payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mutex(&c)
			err := c.validate()
			if err == nil {
				t.Fatalf("validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	if err := base().validate(); err != nil {
		t.Fatalf("a well-formed closure was refused: %v", err)
	}
}

// An envelope bound to one record must not open under another's identity. This
// is what stops a sealed object being moved between rows to make a record claim
// something it never held.
func TestSealedObjectIsBoundToItsRecord(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	c := sampleClosure("run-bound", KindHypothesis, KindProposal)
	mustSync(t, db, store, ring, c)
	rows, err := AnalysisRecords(ctx, db, c.RunID)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}

	swapped := rows[0]
	swapped.ObjectKey, swapped.ObjectDigest = rows[1].ObjectKey, rows[1].ObjectDigest
	if _, err := OpenRecord(ctx, store, ring, swapped); !errors.Is(err, envelope.ErrAuthentication) {
		t.Fatalf("error = %v, want an authentication failure", err)
	}

	tampered := rows[0]
	tampered.ObjectDigest = strings.Repeat("0", len(tampered.ObjectDigest))
	if _, err := OpenRecord(ctx, store, ring, tampered); !errors.Is(err, ErrObjectVerification) {
		t.Fatalf("error = %v, want ErrObjectVerification", err)
	}
}

// reconnect opens a separate connection to the same database, which is how a
// second Babel instance reaches the shared catalog.
func reconnect(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	var database string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("read database name: %v", err)
	}
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse base DSN: %v", err)
	}
	u.Path = "/" + database
	other, err := Open(t.Context(), u.String())
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	t.Cleanup(func() { other.Close() })
	return other
}
