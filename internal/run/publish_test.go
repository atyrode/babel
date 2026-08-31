package run

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/sync"
)

// staged is one Append call as the fake hook saw it.
type staged struct {
	producedBy string
	record     sync.Record
}

// fakeHook is a sync.Hook that records what a durable write handed it.
//
// It is a fake rather than a real *sync.Publisher because a Publisher needs
// PostgreSQL, an object store and a payload keyring, and none of those are
// what this package owes the contract. What it owes is which record it
// stages, under which producing run, and whether it publishes the closure
// Append handed back - and a fake is the only way to put the store on both
// sides of the joins-a-run / is-its-own-closure split without a journal in
// the matching state.
type fakeHook struct {
	appended  []staged
	committed []sync.Closure
	// closure and ready are what Append reports back.
	closure sync.Closure
	ready   bool
	// err makes Append fail, which is the atomicity case: staging shares the
	// writer's transaction, so a failure here must take the durable write.
	err error
}

func (h *fakeHook) Append(_ context.Context, _ *sql.Tx, producedBy string,
	rec sync.Record) (sync.Closure, bool, error) {
	h.appended = append(h.appended, staged{producedBy: producedBy, record: rec})
	if h.err != nil {
		return sync.Closure{}, false, h.err
	}
	return h.closure, h.ready, nil
}

func (h *fakeHook) StageTx(context.Context, *sql.Tx, sync.Record) error { return nil }

func (h *fakeHook) DeclareTx(context.Context, *sql.Tx, sync.Closure) error { return nil }

func (h *fakeHook) CommitInline(_ context.Context, c sync.Closure) error {
	h.committed = append(h.committed, c)
	return nil
}

func syncStore(t *testing.T, h *fakeHook) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), WithSync(h))
	if err != nil {
		t.Fatalf("Open with a publication hook: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// only reports the single Append the caller expects, so every assertion after
// it is about a record that was staged exactly once.
func only(t *testing.T, h *fakeHook) sync.Record {
	t.Helper()
	if len(h.appended) != 1 {
		t.Fatalf("staged %d records, want exactly 1", len(h.appended))
	}
	return h.appended[0].record
}

// A preparation is written before any run exists, so it is its own closure of
// one: nothing produced it and nothing can still grow it, which is why it
// declares and publishes inside the write that created it.
func TestPutPreparationStagesItsOwnClosureAndPublishesIt(t *testing.T) {
	ctx := context.Background()
	prep := mustPreparation(t, preparedAt, testSelection())
	want, err := prep.MarshalCanonical()
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}
	hook := &fakeHook{closure: sync.Closure{RunID: string(prep.ID)}, ready: true}
	s := syncStore(t, hook)

	if err := s.PutPreparation(ctx, prep); err != nil {
		t.Fatalf("PutPreparation: %v", err)
	}

	if got := hook.appended[0].producedBy; got != "" {
		t.Errorf("staged under producing run %q, want none: no run produces a preparation", got)
	}
	rec := only(t, hook)
	if rec.Kind != sharedcatalog.KindPreparation {
		t.Errorf("kind %q, want %q", rec.Kind, sharedcatalog.KindPreparation)
	}
	if rec.EntityID != string(prep.ID) {
		t.Errorf("entity id %q, want %q", rec.EntityID, prep.ID)
	}
	if rec.Schema != prep.Schema {
		t.Errorf("schema %d, want %d", rec.Schema, prep.Schema)
	}
	// The stored bytes are the publication bytes. A re-encoding here would be
	// a second answer to what the record is, and a preparation's identity is
	// derived from its bytes.
	if string(rec.Payload) != string(want) {
		t.Errorf("payload is not the stored canonical form:\n got %s\nwant %s", rec.Payload, want)
	}

	if len(hook.committed) != 1 {
		t.Fatalf("published %d closures, want 1", len(hook.committed))
	}
	if hook.committed[0] != hook.closure {
		t.Errorf("published closure %+v, want the one Append declared %+v",
			hook.committed[0], hook.closure)
	}
}

// A receipt belongs to the run that produced it, and that run's closure may
// not be declared while it can still grow. So the receipt is staged under the
// run and nothing publishes here: internal/explore ends the run.
func TestPutReceiptStagesUnderTheRunAndLeavesPublicationToIt(t *testing.T) {
	ctx := context.Background()
	r := mustReceipt(t)
	hook := &fakeHook{ready: false}
	s := syncStore(t, hook)

	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}

	if got := hook.appended[0].producedBy; got != r.Header.RunID {
		t.Errorf("staged under producing run %q, want the run it records %q", got, r.Header.RunID)
	}
	rec := only(t, hook)
	if rec.Kind != sharedcatalog.KindReceipt {
		t.Errorf("kind %q, want %q", rec.Kind, sharedcatalog.KindReceipt)
	}
	if rec.EntityID != string(r.Header.ID) {
		t.Errorf("entity id %q, want %q", rec.EntityID, r.Header.ID)
	}
	if rec.Schema != r.Header.Schema {
		t.Errorf("schema %d, want %d", rec.Schema, r.Header.Schema)
	}
	if len(hook.committed) != 0 {
		t.Errorf("published %d closures while the run is still open, want 0", len(hook.committed))
	}
}

// An amendment to a run whose closure is already declared cannot join it, so
// Append makes it a closure of one linked to the run and reports it ready.
// The store writes no branch for that case; it honours the flag.
func TestPutReceiptPublishesTheClosureAppendDeclared(t *testing.T) {
	ctx := context.Background()
	r := mustReceipt(t)
	hook := &fakeHook{
		closure: sync.Closure{RunID: string(r.Header.ID), ContinuesRunID: r.Header.RunID},
		ready:   true,
	}
	s := syncStore(t, hook)

	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}

	if len(hook.committed) != 1 {
		t.Fatalf("published %d closures, want 1", len(hook.committed))
	}
	if hook.committed[0] != hook.closure {
		t.Errorf("published closure %+v, want the one Append declared %+v",
			hook.committed[0], hook.closure)
	}
}

// The receipt's durable payload column holds the body alone, so the published
// object carries the header beside it or a reader on another host cannot say
// which run or revision the bytes are. The body travels verbatim, and the
// local sync state does not travel at all.
func TestAPublishedReceiptCarriesItsHeaderAndTheStoredBody(t *testing.T) {
	ctx := context.Background()
	r := mustReceipt(t)
	body, err := r.MarshalBody()
	if err != nil {
		t.Fatalf("MarshalBody: %v", err)
	}
	hook := &fakeHook{}
	s := syncStore(t, hook)

	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}

	var published remoteReceipt
	if err := json.Unmarshal(only(t, hook).Payload, &published); err != nil {
		t.Fatalf("decode the publication payload: %v", err)
	}
	if published.Header.ID != r.Header.ID || published.Header.RunID != r.Header.RunID {
		t.Errorf("published header names %s of run %s, want %s of run %s",
			published.Header.ID, published.Header.RunID, r.Header.ID, r.Header.RunID)
	}
	if published.Header.PreparationID != r.Header.PreparationID {
		t.Errorf("published preparation %s, want %s",
			published.Header.PreparationID, r.Header.PreparationID)
	}
	if published.Header.Revision != r.Header.Revision {
		t.Errorf("published revision %d, want %d", published.Header.Revision, r.Header.Revision)
	}
	if published.Header.Counts != r.Header.Counts {
		t.Errorf("published counts %+v, want %+v", published.Header.Counts, r.Header.Counts)
	}
	// Local sync state is this machine's knowledge of where the record has
	// reached, and the catalog is the authority for a reader. A published
	// copy would be a second answer, wrong from the moment it arrived.
	if published.Header.Sync != "" {
		t.Errorf("published local sync state %q, want none", published.Header.Sync)
	}
	if string(published.Body) != string(body) {
		t.Errorf("published body is not the stored bytes:\n got %s\nwant %s",
			published.Body, body)
	}
}

// Staging shares the writer's transaction so that "durable here" and "known
// to be owed to the fleet" are one event. A record that survived a staging
// failure would be durable, invisible to the publisher and reported by
// nothing, which is the one failure mode this design cannot tolerate.
func TestAStagingFailureLeavesNothingDurable(t *testing.T) {
	ctx := context.Background()
	refused := errors.New("staging refused")

	t.Run("preparation", func(t *testing.T) {
		dir := t.TempDir()
		prep := mustPreparation(t, preparedAt, testSelection())
		s, err := Open(dir, WithSync(&fakeHook{err: refused}))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := s.PutPreparation(ctx, prep); !errors.Is(err, refused) {
			t.Fatalf("PutPreparation error %v, want the staging failure", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer reopened.Close()
		if _, err := reopened.Preparation(ctx, prep.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the preparation survived a staging failure: %v", err)
		}
	})

	t.Run("receipt", func(t *testing.T) {
		dir := t.TempDir()
		r := mustReceipt(t)
		s, err := Open(dir, WithSync(&fakeHook{err: refused}))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := s.PutReceipt(ctx, r); !errors.Is(err, refused) {
			t.Fatalf("PutReceipt error %v, want the staging failure", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer reopened.Close()
		if _, err := reopened.Receipt(ctx, r.Header.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the receipt survived a staging failure: %v", err)
		}
		// The preparation PutReceipt writes shares that transaction, so the
		// rollback takes it too. A scope left behind by a failed run write
		// would be a record nothing published and nothing referenced.
		if _, err := reopened.Preparation(ctx, r.Header.PreparationID); !errors.Is(err, ErrNotFound) {
			t.Errorf("the receipt's preparation survived a staging failure: %v", err)
		}
	})
}

// A writer stages on its own connection inside its own transaction, so the
// journal's tables have to exist on that handle before the first write. Open
// is where that happens, and only when there is something to stage for.
func TestOpenPreparesTheJournalOnlyWhenPublishing(t *testing.T) {
	withHook := syncStore(t, &fakeHook{})
	var n int
	if err := withHook.db.QueryRow(`SELECT COUNT(*) FROM sync_record`).Scan(&n); err != nil {
		t.Errorf("a publishing store has no journal on its own handle: %v", err)
	}

	local := testStore(t)
	if err := local.db.QueryRow(`SELECT COUNT(*) FROM sync_record`).Scan(&n); err == nil {
		t.Error("a local-only store carries journal tables nothing will ever read")
	}
}

// Open with no option is every caller that existed before Phase B, and it
// must write exactly what it always wrote: publication is an addition, never
// a change to the local record.
func TestALocalOnlyStoreWritesAsBeforeAndStagesNothing(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	r := mustReceipt(t)

	if err := s.PutPreparation(ctx, r.Preparation); err != nil {
		t.Fatalf("PutPreparation: %v", err)
	}
	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	if _, err := s.Preparation(ctx, r.Preparation.ID); err != nil {
		t.Errorf("the preparation is not durable: %v", err)
	}
	if _, err := s.Receipt(ctx, r.Header.ID); err != nil {
		t.Errorf("the receipt is not durable: %v", err)
	}

	// The journal table is absent in local-only mode, which is itself the
	// proof that nothing was staged; a present-but-empty table would be the
	// same answer, so both count.
	var n int
	switch err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_record`).Scan(&n); {
	case err != nil:
	case n != 0:
		t.Errorf("a local-only store staged %d records, want 0", n)
	}
}
