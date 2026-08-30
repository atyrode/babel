package reality

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
)

// baseTime is the fixed instant every test's clock starts from. A fixed clock
// is not a convenience here: freshness, expiry, and inbox staleness are all
// time-dependent, and a test that could not control the clock would have to
// sleep for a predicate's TTL to observe expiry at all.
var baseTime = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// testClock advances a millisecond per read so that every record in one test
// gets a distinct recorded_at, which is what makes ordering assertions exact.
type testClock struct{ at time.Time }

func newClock() *testClock { return &testClock{at: baseTime} }

func (c *testClock) now() time.Time {
	c.at = c.at.Add(time.Millisecond)
	return c.at
}

func (c *testClock) set(at time.Time) { c.at = at }

// newStore opens a ledger over a throwaway directory. Nothing here needs a real
// transcript, a credential, or a configured backend.
func newStore(t *testing.T, opts ...Option) (*Store, *testClock) {
	t.Helper()
	clock := newClock()
	opts = append([]Option{WithClock(clock.now)}, opts...)
	store, err := Open(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, clock
}

func mustEntity(t *testing.T, s *Store, kind EntityKind, name string) Entity {
	t.Helper()
	record, err := s.CreateEntity(context.Background(), EntityInput{
		Kind:    kind,
		Payload: EntityPayload{DisplayName: name},
	})
	if err != nil {
		t.Fatalf("CreateEntity(%s, %q): %v", kind, name, err)
	}
	return record
}

// operatorFact builds an attributed operator assertion with an open valid time,
// which is the shape most of §4.8's intent predicates take.
func operatorFact(subject string, predicate Predicate, value FactValue, observed time.Time) FactInput {
	return FactInput{
		SubjectID:   subject,
		Predicate:   predicate,
		Value:       value,
		ValidFrom:   observed,
		ObservedAt:  observed,
		Authority:   Authority{Kind: AuthorityOperator, ID: "operator", At: observed},
		Confidence:  ConfidenceHigh,
		Sensitivity: SensitivityRoutine,
	}
}

func enum(value string) FactValue { return FactValue{Kind: ValueEnum, Enum: value} }

// syntheticLocator is a provenance locator over invented bytes. §3's locator
// needs a path and a digest to recover evidence; nothing in these tests reads
// the bytes back, and nothing here names a real path.
func syntheticLocator(index int) *event.Locator {
	return &event.Locator{
		Path:       fmt.Sprintf("synthetic/session-%d.jsonl", index),
		Line:       index + 1,
		ByteOffset: int64(index * 128),
		Digest:     strings.Repeat("ab", 32),
	}
}

// rowSnapshot renders every column of one row as a comparable string. It is how
// the immutability tests check that a record is byte-identical after a later
// write rather than merely equal in the fields the reader happens to decode.
func rowSnapshot(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		t.Fatalf("row snapshot query: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("row snapshot columns: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("row snapshot: no row for %q", query)
	}
	cells := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range cells {
		pointers[i] = &cells[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatalf("row snapshot scan: %v", err)
	}
	var out strings.Builder
	for i, name := range columns {
		fmt.Fprintf(&out, "%s=%v\n", name, cells[i])
	}
	return out.String()
}

// TestDurableComponentsShareOneFile protects the wave 2 arrangement: the
// Reality Ledger joins internal/frontier's and internal/run's tables in one
// durable file under its own component key and table prefix. Opening both
// against one directory in either order is the whole of the contract.
func TestDurableComponentsShareOneFile(t *testing.T) {
	dir := t.TempDir()

	realityStore, err := Open(dir)
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	defer realityStore.Close()

	sink, cleanup := openFrontier(t, dir)
	defer cleanup()

	// Each must still work with the other's tables present.
	if _, err := realityStore.CreateEntity(context.Background(), EntityInput{
		Kind:    EntityProject,
		Payload: EntityPayload{DisplayName: "synthetic project"},
	}); err != nil {
		t.Errorf("reality write with frontier tables present: %v", err)
	}
	id, err := sink.RecordHypothesis(context.Background(), HypothesisDraft{
		RunID:     "run-1",
		Statement: "synthetic candidate",
	})
	if err != nil {
		t.Errorf("frontier write with reality tables present: %v", err)
	}
	if id == "" {
		t.Error("frontier returned an empty hypothesis id")
	}
	if got := realityStore.Path(); !strings.HasSuffix(got, "durable.db") {
		t.Errorf("reality store path %q is not the durable file", got)
	}

	// And the reverse open order, since nothing orders them.
	dir2 := t.TempDir()
	_, cleanup2 := openFrontier(t, dir2)
	defer cleanup2()
	second, err := Open(dir2)
	if err != nil {
		t.Fatalf("reality.Open after frontier: %v", err)
	}
	second.Close()
}

// TestSchemaVersionFromTheFutureIsRefused checks that an unfamiliar schema
// stops the store rather than being resolved by discarding the file: these rows
// are the only copy of what an operator answered for.
func TestSchemaVersionFromTheFutureIsRefused(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE schema_migration SET version = ? WHERE component = ?`,
		len(migrations)+7, component); err != nil {
		t.Fatalf("bump schema version: %v", err)
	}
	store.Close()

	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted a schema version this build does not know")
	} else if !strings.Contains(err.Error(), "newer than this build supports") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestEntityRowIsImmutableAndNeverDeleted checks the triggers rather than the
// package's own SQL: immutability that depends on nobody writing the wrong
// statement is not immutability.
func TestEntityRowIsImmutableAndNeverDeleted(t *testing.T) {
	store, _ := newStore(t)
	entity := mustEntity(t, store, EntityRepository, "synthetic repository")

	if _, err := store.db.Exec(`UPDATE reality_entity SET kind = ? WHERE id = ?`,
		string(EntityMachine), entity.ID); err == nil {
		t.Error("an entity row accepted an update")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_entity WHERE id = ?`, entity.ID); err == nil {
		t.Error("an entity row accepted a delete")
	}
}

// TestAliasResolutionReportsAmbiguityRatherThanGuessing covers §4.8's reason for
// making alias resolution a Question: two entities can answer to one term, and
// picking one is the mistaken resolution the merge history exists to reverse.
func TestAliasResolutionReportsAmbiguityRatherThanGuessing(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	first := mustEntity(t, store, EntityRepository, "first repository")
	second := mustEntity(t, store, EntityRepository, "second repository")

	for _, id := range []string{first.ID, second.ID} {
		if _, err := store.AddAlias(ctx, AliasInput{
			EntityID: id,
			Kind:     AliasChatTerm,
			Payload:  AliasPayload{Value: "The Deploy Thing"},
		}); err != nil {
			t.Fatalf("AddAlias: %v", err)
		}
	}

	if _, err := store.ResolveAlias(ctx, AliasChatTerm, "the deploy thing"); err == nil {
		t.Fatal("an ambiguous alias resolved to one entity")
	} else if !isErr(err, ErrAmbiguousAlias) {
		t.Errorf("want ErrAmbiguousAlias, got %v", err)
	}

	// A retraction leaves the alias row in place and removes it from
	// resolution, which is how a wrong alias is corrected without deleting
	// the record of it.
	aliases, err := store.Aliases(ctx, second.ID)
	if err != nil {
		t.Fatalf("Aliases: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("want 1 alias, got %d", len(aliases))
	}
	if err := store.RetractAlias(ctx, aliases[0].ID, "it meant the other one"); err != nil {
		t.Fatalf("RetractAlias: %v", err)
	}
	resolved, err := store.ResolveAlias(ctx, AliasChatTerm, "the deploy thing")
	if err != nil {
		t.Fatalf("ResolveAlias after retraction: %v", err)
	}
	if resolved != first.ID {
		t.Errorf("resolved to %q, want %q", resolved, first.ID)
	}
	after, err := store.Aliases(ctx, second.ID)
	if err != nil {
		t.Fatalf("Aliases after retraction: %v", err)
	}
	if len(after) != 1 || after[0].State != StateRetracted {
		t.Errorf("retracted alias is not still present as retracted: %+v", after)
	}
}

// openFrontier opens the durable hypothesis frontier over the same directory
// and wraps it in the sink a plan's retained candidates go to. It proves the
// adapter is real rather than a shape a test filled in.
func openFrontier(t *testing.T, dir string) (FrontierSink, func()) {
	t.Helper()
	store, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	return FrontierSink{Store: store}, func() { store.Close() }
}

// isErr reports whether err wraps target.
func isErr(err, target error) bool { return errors.Is(err, target) }

// countRows is a small helper for the atomicity assertions, where the point is
// that a table is empty.
func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0
		}
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
