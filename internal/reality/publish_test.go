package reality

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// What this file defends is the property publish.go exists for: a Reality
// record that is durable on this machine is, in the same instant, known to be
// owed to the fleet — and a record that is not durable was never staged.
//
// The hook is a fake rather than a real Publisher on purpose. A real one needs
// PostgreSQL, an object store and a keyring, and none of those would make these
// assertions stronger: what is measured here is which records this store
// stages, under which catalog kind, and in which wire shape. The protocol
// itself is measured in internal/sync's own suite, against a real database.

// recordingHook records what a store stages and publishes.
type recordingHook struct {
	staged   []babelsync.Record
	runs     []string
	closures []babelsync.Closure
	declared []babelsync.Closure

	// appendErr, when set, fails the staging call. It is how the atomicity
	// property is measured: a staging failure must take the durable write
	// down with it, because the alternative is a durable record nothing will
	// ever publish and nothing saying so.
	appendErr error
}

func (h *recordingHook) Append(ctx context.Context, tx *sql.Tx, producedBy string,
	rec babelsync.Record) (babelsync.Closure, bool, error) {
	if h.appendErr != nil {
		return babelsync.Closure{}, false, h.appendErr
	}
	h.runs = append(h.runs, producedBy)
	// Reality never names a producing run, so every record is its own closure
	// of one, declared in the writer's transaction. The fake reproduces that
	// branch of internal/sync's Append rather than the one it never reaches.
	rec.RunID = rec.EntityID
	h.staged = append(h.staged, rec)
	c := babelsync.Closure{RunID: rec.EntityID}
	h.declared = append(h.declared, c)
	return c, true, nil
}

func (h *recordingHook) StageTx(ctx context.Context, tx *sql.Tx, rec babelsync.Record) error {
	if h.appendErr != nil {
		return h.appendErr
	}
	h.staged = append(h.staged, rec)
	return nil
}

func (h *recordingHook) DeclareTx(ctx context.Context, tx *sql.Tx, c babelsync.Closure) error {
	if h.appendErr != nil {
		return h.appendErr
	}
	h.declared = append(h.declared, c)
	return nil
}

func (h *recordingHook) CommitInline(ctx context.Context, c babelsync.Closure) error {
	h.closures = append(h.closures, c)
	return nil
}

// forget drops everything a test's fixture staged, so the assertions that
// follow are about the one write under test rather than about the entity and
// question it needed first.
func (h *recordingHook) forget() {
	h.staged, h.runs, h.closures, h.declared = nil, nil, nil, nil
}

// only returns the single staged record, failing when there is not exactly
// one.
func (h *recordingHook) only(t *testing.T) babelsync.Record {
	t.Helper()
	if len(h.staged) != 1 {
		t.Fatalf("staged %d records, want 1: %+v", len(h.staged), h.staged)
	}
	return h.staged[0]
}

// wellFormedID restates internal/sync's stage-time gate on a record id.
//
// It is here because the fake hook cannot enforce it and one id in this package
// is composed rather than generated: a membership entry is keyed by the
// resolution that wrote it and the identity it moved, joined by a separator. A
// real Publisher would refuse a badly joined one on the first merge of a shared
// deployment, which is the worst place to learn it.
var wellFormedID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// stagedKinds decodes every staged record and returns the wire kinds in staging
// order, checking on the way what every member of every closure must satisfy: a
// member no ingesting host can decode is as good as a member never staged.
func (h *recordingHook) stagedKinds(t *testing.T) []PublishedKind {
	t.Helper()
	kinds := make([]PublishedKind, 0, len(h.staged))
	for _, rec := range h.staged {
		if rec.Kind != sharedcatalog.KindContext {
			t.Fatalf("staged %s under catalog kind %q, want %q: 0003's vocabulary is closed",
				rec.EntityID, rec.Kind, sharedcatalog.KindContext)
		}
		if rec.Schema != RecordSchema {
			t.Fatalf("staged %s at schema %d, want %d", rec.EntityID, rec.Schema, RecordSchema)
		}
		if len(rec.Payload) == 0 {
			t.Fatalf("staged %s with an empty payload", rec.EntityID)
		}
		if !wellFormedID.MatchString(rec.EntityID) {
			t.Fatalf("staged id %q is not a well-formed Phase B identifier, so internal/sync "+
				"would refuse it", rec.EntityID)
		}
		wire, err := DecodePublishedRecord(rec.Payload)
		if err != nil {
			t.Fatalf("decode staged payload for %s: %v", rec.EntityID, err)
		}
		if wire.ID != rec.EntityID {
			t.Fatalf("staged %s carries wire id %q, so a replay would not be idempotent",
				rec.EntityID, wire.ID)
		}
		kinds = append(kinds, wire.Kind)
	}
	return kinds
}

// TestEachPublishedWritePathStagesOneRecord measures the seven write paths
// publish.go names as closures of one, one at a time.
//
// Each is checked for the same four things, because each is a separate chance
// to get one of them wrong: exactly one record staged, under the one catalog
// kind migration 0003's closed vocabulary admits, keyed by the record's own id
// so a replay is idempotent, and published exactly once after the transaction
// committed. The payload is decoded back through the wire form, which is also
// the assertion that a record this store stages is one an ingesting host can
// read.
func TestEachPublishedWritePathStagesOneRecord(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		kind PublishedKind
		// write performs the fixture, forgets it, and returns the id of the
		// record whose publication is under test.
		write func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string
	}{
		{
			name: "create entity",
			kind: PublishedEntity,
			write: func(t *testing.T, s *Store, _ *testClock, h *recordingHook) string {
				h.forget()
				return mustEntity(t, s, EntityService, "a service").ID
			},
		},
		{
			name: "assert fact",
			kind: PublishedFact,
			write: func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string {
				service := mustEntity(t, s, EntityService, "a service")
				h.forget()
				record, _, err := s.AssertFact(ctx, operatorFact(service.ID,
					PredicateLifecycle, enum(LifecycleActive), clock.at))
				if err != nil {
					t.Fatalf("AssertFact: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "supersede fact",
			kind: PublishedFact,
			write: func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string {
				service := mustEntity(t, s, EntityService, "a service")
				prior, _, err := s.AssertFact(ctx, operatorFact(service.ID,
					PredicateLifecycle, enum(LifecycleActive), clock.at))
				if err != nil {
					t.Fatalf("AssertFact: %v", err)
				}
				h.forget()
				record, err := s.SupersedeFact(ctx, SupersedeInput{
					PriorID: prior.ID,
					Fact: operatorFact(service.ID, PredicateLifecycle,
						enum(LifecycleDormant), clock.at),
				})
				if err != nil {
					t.Fatalf("SupersedeFact: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "record answer",
			kind: PublishedAnswer,
			write: func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string {
				service := mustEntity(t, s, EntityService, "a service")
				question, err := s.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
				if err != nil {
					t.Fatalf("Ask: %v", err)
				}
				h.forget()
				record, err := s.RecordAnswer(ctx, AnswerInput{
					QuestionID: question.ID,
					Author:     "operator",
					At:         clock.at,
					Outcome:    OutcomeAnswered,
					Text:       "it was decommissioned last week",
				})
				if err != nil {
					t.Fatalf("RecordAnswer: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "attach context",
			kind: PublishedContext,
			write: func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string {
				h.forget()
				record, err := s.AttachContext(ctx, ContextInput{
					Author: "operator",
					At:     clock.at,
					Text:   "treat the staging fleet as disposable",
				})
				if err != nil {
					t.Fatalf("AttachContext: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "dispute facts",
			kind: PublishedDispute,
			write: func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string {
				service := mustEntity(t, s, EntityService, "a service")
				first, _, err := s.AssertFact(ctx, operatorFact(service.ID,
					PredicateLifecycle, enum(LifecycleActive), clock.at))
				if err != nil {
					t.Fatalf("AssertFact: %v", err)
				}
				second, _, err := s.AssertFact(ctx, operatorFact(service.ID,
					PredicateOwnership, enum(OwnershipOwned), clock.at))
				if err != nil {
					t.Fatalf("AssertFact: %v", err)
				}
				h.forget()
				// Two predicates rather than two values, so the
				// deterministic check finds nothing and the dispute is
				// only the operator's judgement — which is the one that
				// travels.
				record, err := s.DisputeFacts(ctx, DisputeInput{
					FactIDs: []string{first.ID, second.ID},
					Actor:   "operator",
					Reason:  "a retired service cannot still be owned",
				})
				if err != nil {
					t.Fatalf("DisputeFacts: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "record plan",
			kind: PublishedPlan,
			write: func(t *testing.T, s *Store, clock *testClock, h *recordingHook) string {
				subject, question, answer := interpretable(t, s, clock)
				h.forget()
				record, _, err := s.RecordPlan(ctx, lifecyclePlan(question, answer,
					subject.ID, clock.now()))
				if err != nil {
					t.Fatalf("RecordPlan: %v", err)
				}
				return record.ID
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := &recordingHook{}
			store, clock := newStore(t, WithSync(hook))
			id := tc.write(t, store, clock, hook)

			rec := hook.only(t)
			if rec.EntityID != id {
				t.Fatalf("staged record id %q, want the record's own id %q", rec.EntityID, id)
			}
			if rec.Kind != sharedcatalog.KindContext {
				t.Fatalf("staged kind %q, want %q: 0003's vocabulary is closed and this is the "+
					"only value in it a Reality record may take", rec.Kind, sharedcatalog.KindContext)
			}
			if rec.Schema != RecordSchema {
				t.Fatalf("staged schema %d, want %d", rec.Schema, RecordSchema)
			}
			if len(rec.Payload) == 0 {
				t.Fatal("staged an empty payload")
			}
			if len(hook.runs) != 1 || hook.runs[0] != "" {
				t.Fatalf("staged under runs %q, want one record with no producing run: nobody "+
					"resumes an operator's action", hook.runs)
			}
			if len(hook.closures) != 1 {
				t.Fatalf("published %d closures, want exactly one after the commit", len(hook.closures))
			}
			if hook.closures[0].RunID != id {
				t.Fatalf("published closure %q, want the record's own id %q",
					hook.closures[0].RunID, id)
			}

			wire, err := DecodePublishedRecord(rec.Payload)
			if err != nil {
				t.Fatalf("decode staged payload: %v", err)
			}
			if wire.Kind != tc.kind {
				t.Fatalf("wire kind %q, want %q: the catalog row can only say %q, so the "+
					"record's own type has to survive inside the envelope",
					wire.Kind, tc.kind, sharedcatalog.KindContext)
			}
			if wire.ID != id {
				t.Fatalf("wire id %q, want %q", wire.ID, id)
			}
			if wire.RecordedAt.IsZero() {
				t.Fatal("wire record does not say when it was recorded")
			}
		})
	}
}

// TestStagingFailureLeavesNothingDurable is the atomicity property, measured by
// re-reading rather than by trusting the returned error.
//
// A ledger that kept a record its journal never learned about would hold a fact
// an operator answered for, owe it to the fleet, and have nothing anywhere that
// says so. Rolling the durable write back with the staging failure is the only
// ordering under which that state cannot exist.
func TestStagingFailureLeavesNothingDurable(t *testing.T) {
	ctx := context.Background()
	staging := errors.New("the journal refused the record")

	t.Run("entity", func(t *testing.T) {
		hook := &recordingHook{appendErr: staging}
		store, _ := newStore(t, WithSync(hook))
		if _, err := store.CreateEntity(ctx, EntityInput{
			Kind:    EntityService,
			Payload: EntityPayload{DisplayName: "a service"},
		}); !errors.Is(err, staging) {
			t.Fatalf("CreateEntity error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_entity"); n != 0 {
			t.Fatalf("%d entity rows survived a staging failure, want none", n)
		}
		if n := countRows(t, store, "reality_entity_membership"); n != 0 {
			t.Fatalf("%d membership rows survived a staging failure, want none", n)
		}
	})

	t.Run("fact", func(t *testing.T) {
		hook := &recordingHook{}
		store, clock := newStore(t, WithSync(hook))
		service := mustEntity(t, store, EntityService, "a service")
		hook.appendErr = staging

		_, _, err := store.AssertFact(ctx, operatorFact(service.ID,
			PredicateLifecycle, enum(LifecycleActive), clock.at))
		if !errors.Is(err, staging) {
			t.Fatalf("AssertFact error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_fact"); n != 0 {
			t.Fatalf("%d fact rows survived a staging failure, want none", n)
		}
		if n := countRows(t, store, "reality_fact_status"); n != 0 {
			t.Fatalf("%d status rows survived a staging failure, want none", n)
		}
		// The entity that was already durable stays durable: a staging
		// failure rolls back the transaction it happened in and nothing else.
		if _, err := store.Entity(ctx, service.ID); err != nil {
			t.Fatalf("Entity after a failed fact assertion: %v", err)
		}
	})

	t.Run("answer", func(t *testing.T) {
		hook := &recordingHook{}
		store, clock := newStore(t, WithSync(hook))
		service := mustEntity(t, store, EntityService, "a service")
		question, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
		hook.appendErr = staging

		if _, err := store.RecordAnswer(ctx, AnswerInput{
			QuestionID: question.ID,
			Author:     "operator",
			At:         clock.at,
			Outcome:    OutcomeAnswered,
			Text:       "it was decommissioned last week",
		}); !errors.Is(err, staging) {
			t.Fatalf("RecordAnswer error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_answer"); n != 0 {
			t.Fatalf("%d answer rows survived a staging failure, want none", n)
		}
		// The question's state moves in the same transaction as the answer,
		// so it has to be back where it started rather than answered by an
		// answer that no longer exists.
		reread, err := store.Question(ctx, question.ID)
		if err != nil {
			t.Fatalf("Question after a failed answer: %v", err)
		}
		if reread.State != QuestionOpen {
			t.Fatalf("question state %q after a failed answer, want %q", reread.State, QuestionOpen)
		}
	})

	t.Run("context", func(t *testing.T) {
		hook := &recordingHook{appendErr: staging}
		store, clock := newStore(t, WithSync(hook))
		if _, err := store.AttachContext(ctx, ContextInput{
			Author: "operator",
			At:     clock.at,
			Text:   "treat the staging fleet as disposable",
		}); !errors.Is(err, staging) {
			t.Fatalf("AttachContext error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_context"); n != 0 {
			t.Fatalf("%d guidance rows survived a staging failure, want none", n)
		}
	})

	t.Run("dispute", func(t *testing.T) {
		hook := &recordingHook{}
		store, clock := newStore(t, WithSync(hook))
		service := mustEntity(t, store, EntityService, "a service")
		first, _, err := store.AssertFact(ctx, operatorFact(service.ID,
			PredicateLifecycle, enum(LifecycleActive), clock.at))
		if err != nil {
			t.Fatalf("AssertFact: %v", err)
		}
		second, _, err := store.AssertFact(ctx, operatorFact(service.ID,
			PredicateOwnership, enum(OwnershipOwned), clock.at))
		if err != nil {
			t.Fatalf("AssertFact: %v", err)
		}
		hook.appendErr = staging

		if _, err := store.DisputeFacts(ctx, DisputeInput{
			FactIDs: []string{first.ID, second.ID},
			Actor:   "operator",
			Reason:  "a retired service cannot still be owned",
		}); !errors.Is(err, staging) {
			t.Fatalf("DisputeFacts error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_dispute"); n != 0 {
			t.Fatalf("%d dispute rows survived a staging failure, want none", n)
		}
		if n := countRows(t, store, "reality_dispute_member"); n != 0 {
			t.Fatalf("%d dispute member rows survived a staging failure, want none", n)
		}
		// The member facts were marked disputed in the same transaction, so
		// they have to be in force again rather than disputed by a dispute
		// that no longer exists.
		for _, id := range []string{first.ID, second.ID} {
			reread, err := store.Fact(ctx, id)
			if err != nil {
				t.Fatalf("Fact after a failed dispute: %v", err)
			}
			if reread.Status != FactActive {
				t.Fatalf("fact %s is %s after a failed dispute, want %s",
					id, reread.Status, FactActive)
			}
		}
	})

	t.Run("plan", func(t *testing.T) {
		hook := &recordingHook{}
		store, clock := newStore(t, WithSync(hook))
		subject, question, answer := interpretable(t, store, clock)
		hook.appendErr = staging

		if _, _, err := store.RecordPlan(ctx, lifecyclePlan(question, answer,
			subject.ID, clock.now())); !errors.Is(err, staging) {
			t.Fatalf("RecordPlan error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_plan"); n != 0 {
			t.Fatalf("%d plan rows survived a staging failure, want none", n)
		}
		if n := countRows(t, store, "reality_plan_action"); n != 0 {
			t.Fatalf("%d plan action rows survived a staging failure, want none", n)
		}
		// The question's state moved with the plan, so it is back with the
		// interpreter rather than holding a plan that does not exist.
		reread, err := store.Question(ctx, question.ID)
		if err != nil {
			t.Fatalf("Question after a failed plan: %v", err)
		}
		if reread.State != QuestionInterpreting {
			t.Fatalf("question state %q after a failed plan, want %q",
				reread.State, QuestionInterpreting)
		}
	})
}

// TestLocalOnlyStoreStagesNothing is the other half of the same contract:
// publication is never a write-path dependency, and a deployment with no shared
// backend is a supported deployment rather than a degraded one.
//
// The journal tables are checked as well as the writes. A store with no hook
// must not create them, because doing so would put a component's schema in the
// shared durable file on behalf of a component that is not running.
func TestLocalOnlyStoreStagesNothing(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	service := mustEntity(t, store, EntityService, "a service")
	fact, _, err := store.AssertFact(ctx, operatorFact(service.ID,
		PredicateLifecycle, enum(LifecycleActive), clock.at))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	if _, err := store.SupersedeFact(ctx, SupersedeInput{
		PriorID: fact.ID,
		Fact:    operatorFact(service.ID, PredicateLifecycle, enum(LifecycleDormant), clock.at),
	}); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	guidance, err := store.AttachContext(ctx, ContextInput{
		Author: "operator",
		At:     clock.at,
		Text:   "treat the staging fleet as disposable",
	})
	if err != nil {
		t.Fatalf("AttachContext: %v", err)
	}
	if _, err := store.Context(ctx, guidance.ID); err != nil {
		t.Fatalf("Context: %v", err)
	}
	if n := countRows(t, store, "reality_fact"); n != 2 {
		t.Fatalf("%d fact rows, want the original and its successor", n)
	}

	var tables int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name LIKE 'sync_%'`).Scan(&tables); err != nil {
		t.Fatalf("read the durable file's table list: %v", err)
	}
	if tables != 0 {
		t.Fatalf("a local-only store created %d journal tables, want none", tables)
	}
}

// TestOpenWithASyncHookCreatesTheJournalTables covers the step that has no
// other symptom until it is too late.
//
// A durable writer stages inside its own transaction, on its own connection, so
// internal/sync's tables have to exist on this store's handle before the first
// write opens one. A real Publisher would otherwise fail on the very first
// operator answer with a missing-table error, and the fake hook the rest of
// this file uses cannot see the omission because it never touches SQL.
func TestOpenWithASyncHookCreatesTheJournalTables(t *testing.T) {
	store, _ := newStore(t, WithSync(&recordingHook{}))
	for _, table := range []string{"sync_run", "sync_record", "sync_payload"} {
		var name string
		err := store.db.QueryRow(`SELECT name FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("journal table %s is absent from the durable file: %v", table, err)
		}
	}
}

// publishedFact is a wire record that passes validate, so a refusal test can
// break exactly one rule and know that is the rule it measured.
func publishedFact() PublishedRecord {
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedFact,
		ID:         "fct_0123456789abcdef",
		RecordedAt: at,
		Claim: &PublishedClaim{
			SubjectID:     "ent_0123456789abcdef",
			Predicate:     PredicateLifecycle,
			ValidFrom:     at,
			ObservedAt:    at,
			AuthorityKind: AuthorityOperator,
			AuthorityID:   "operator",
			AuthorityAt:   at,
			Confidence:    ConfidenceHigh,
			Sensitivity:   SensitivityRoutine,
		},
		Payload: json.RawMessage(`{"value":{"kind":"enum","enum":"active"}}`),
	}
}

// TestPublishedRecordRoundTrips checks that what leaves this machine is what an
// ingesting host reads back, field for field.
//
// The optional instant matters most here. An absent ValidUntil is open-ended
// and must arrive absent rather than as the zero time, because a reader
// comparing the zero instant against a valid-time range would find every
// operator-intent fact long expired.
func TestPublishedRecordRoundTrips(t *testing.T) {
	original := publishedFact()
	original.Claim.Supersedes = "fct_fedcba9876543210"

	encoded, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := DecodePublishedRecord(encoded)
	if err != nil {
		t.Fatalf("DecodePublishedRecord: %v", err)
	}
	if decoded.Kind != original.Kind || decoded.ID != original.ID ||
		decoded.Schema != original.Schema {
		t.Fatalf("decoded head %+v, want %+v", decoded, original)
	}
	if !decoded.RecordedAt.Equal(original.RecordedAt) {
		t.Fatalf("decoded recorded-at %s, want %s", decoded.RecordedAt, original.RecordedAt)
	}
	if *decoded.Claim != *original.Claim {
		t.Fatalf("decoded claim %+v, want %+v", *decoded.Claim, *original.Claim)
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Fatalf("decoded payload %s, want the stored bytes %s", decoded.Payload, original.Payload)
	}
	if !decoded.Claim.ValidUntil.IsZero() {
		t.Fatalf("an open-ended valid time decoded as %s, want absent", decoded.Claim.ValidUntil)
	}
}

// TestPublishedRecordRefusesMalformedShapes breaks one rule per case.
//
// Both directions are covered on purpose. A record missing what its type needs
// is unreadable on arrival; a record carrying a field its type has no use for
// is worse, because it is readable and means two things. Either would become an
// immutable remote row nothing can correct, so both are refused before the
// bytes leave.
func TestPublishedRecordRefusesMalformedShapes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *PublishedRecord)
	}{
		{
			name:   "a fact with no subject",
			mutate: func(p *PublishedRecord) { p.Claim.SubjectID = "" },
		},
		{
			name:   "a fact with no claim at all",
			mutate: func(p *PublishedRecord) { p.Claim = nil },
		},
		{
			name: "a fact attributed to nobody",
			mutate: func(p *PublishedRecord) {
				p.Claim.AuthorityID = ""
			},
		},
		{
			name: "a fact carrying an entity kind",
			mutate: func(p *PublishedRecord) {
				p.EntityKind = EntityService
			},
		},
		{
			name: "a fact carrying an operator attribution",
			mutate: func(p *PublishedRecord) {
				p.Author = "operator"
			},
		},
		{
			name: "guidance carrying a claim",
			mutate: func(p *PublishedRecord) {
				p.Kind = PublishedContext
				p.Author = "operator"
				p.AuthoredAt = p.RecordedAt
			},
		},
		{
			name: "an answer with no question",
			mutate: func(p *PublishedRecord) {
				p.Kind = PublishedAnswer
				p.Claim = nil
				p.Author = "operator"
				p.AuthoredAt = p.RecordedAt
				p.Response = &PublishedResponse{Outcome: OutcomeAnswered}
			},
		},
		{
			name: "an entity with no kind",
			mutate: func(p *PublishedRecord) {
				p.Kind = PublishedEntity
				p.Claim = nil
			},
		},
		{
			name: "guidance nobody is attributed with",
			mutate: func(p *PublishedRecord) {
				p.Kind = PublishedContext
				p.Claim = nil
			},
		},
		{
			name:   "a payload that is a JSON null",
			mutate: func(p *PublishedRecord) { p.Payload = json.RawMessage(`null`) },
		},
		{
			name:   "a schema this build does not read",
			mutate: func(p *PublishedRecord) { p.Schema = RecordSchema + 1 },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := publishedFact()
			tc.mutate(&record)
			if _, err := record.Marshal(); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("Marshal accepted %s: %v", tc.name, err)
			}
		})
	}
}

// interpretable builds the state a plan needs and nothing else: a subject, an
// answered question, and the question sent to the interpreter.
func interpretable(t *testing.T, s *Store, clock *testClock) (Entity, Question, Answer) {
	t.Helper()
	ctx := context.Background()
	subject := mustEntity(t, s, EntityService, "a service")
	question, err := s.Ask(ctx, refreshQuestion(subject.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, err := s.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID,
		Author:     "operator",
		At:         clock.now(),
		Outcome:    OutcomeAnswered,
		Text:       "it is dormant now",
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if err := s.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}
	return subject, question, answer
}

// lifecyclePlan is the smallest plan with something to accept: one fact
// assertion, no hypothesis, no follow-up. It keeps a publication test's staged
// set to what the path under test produced.
func lifecyclePlan(question Question, answer Answer, subject string, observed time.Time) PlanInput {
	return PlanInput{
		QuestionID:         question.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 3,
		Summary:            "record the dormancy the operator stated",
		Kinds:              []ActionKind{ActionAssertFact},
		Actions: []ActionPayload{{
			Rationale: "the operator stated the service is dormant",
			Fact: &FactInput{
				SubjectID:   subject,
				Predicate:   PredicateLifecycle,
				Value:       enum(LifecycleDormant),
				ValidFrom:   observed,
				ObservedAt:  observed,
				Confidence:  ConfidenceHigh,
				Sensitivity: SensitivityRoutine,
			},
		}},
	}
}

// inventoryPlacement is a trusted source's placement claim. It carries a
// provenance locator because §4.8 accepts a non-operator authority only with
// one.
func inventoryPlacement(subject, machine string, observed time.Time) FactInput {
	return FactInput{
		SubjectID:   subject,
		Predicate:   PredicateServicePlacement,
		Value:       FactValue{Kind: ValueEntity, ObjectID: machine},
		ValidFrom:   observed,
		ObservedAt:  observed,
		Confidence:  ConfidenceHigh,
		Sensitivity: SensitivityRoutine,
		Provenance:  syntheticLocator(1),
		Note:        "declared placement from the inventory document",
	}
}

// TestEachPublishedSetStagesOneClosure measures the five write paths whose
// publishable output is a set rather than a record.
//
// One property is checked four ways, because each way is a separate chance to
// lose it: every member of the set is staged, every member carries the anchor's
// run id, the closure is declared exactly once under that id, and it is
// published exactly once after the transaction committed. A set that failed any
// one of them would let a fleet reader see half of it — an acceptance without
// the facts it authorized, a merge without the entry saying what the folded
// identity now resolves to — and migration 0003 gives the read side no way to
// repair that.
//
// The expected kinds are in staging order, which is the order the transaction
// wrote the rows in and therefore the order of the closure's ordinals.
func TestEachPublishedSetStagesOneClosure(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		kinds []PublishedKind
		// write opens a store with the hook attached, performs the fixture,
		// forgets it, does the write under test, and returns the id of the
		// record that authorizes the set. Each case opens its own store
		// because an acceptance needs a question, an answer and a plan behind
		// it, which is a different fixture rather than a longer one.
		write func(t *testing.T, h *recordingHook) string
	}{
		{
			name:  "import facts",
			kinds: []PublishedKind{PublishedImport, PublishedFact, PublishedFact},
			write: func(t *testing.T, h *recordingHook) string {
				s, clock := newStore(t, WithSync(h))
				source := registerInventory(t, s)
				machine := mustEntity(t, s, EntityMachine, "a machine")
				first := mustEntity(t, s, EntityService, "the first service")
				second := mustEntity(t, s, EntityService, "the second service")
				h.forget()
				imported, err := s.ImportFacts(ctx, ImportInput{
					SourceID: source.ID,
					BatchKey: "batch-1",
					Facts: []FactInput{
						inventoryPlacement(first.ID, machine.ID, clock.now()),
						inventoryPlacement(second.ID, machine.ID, clock.now()),
					},
				})
				if err != nil {
					t.Fatalf("ImportFacts: %v", err)
				}
				// The batch row is the anchor, and every imported fact
				// names it, so the facts agree about which commit they
				// belong to.
				if imported[0].ImportID != imported[1].ImportID {
					t.Fatalf("one batch produced two import ids, %q and %q",
						imported[0].ImportID, imported[1].ImportID)
				}
				return imported[0].ImportID
			},
		},
		{
			name:  "merge entities",
			kinds: []PublishedKind{PublishedResolution, PublishedMembership},
			write: func(t *testing.T, h *recordingHook) string {
				s, _ := newStore(t, WithSync(h))
				target := mustEntity(t, s, EntityProject, "a project")
				folded := mustEntity(t, s, EntityProject, "the same project renamed")
				h.forget()
				record, err := s.MergeEntities(ctx, MergeInput{
					SourceIDs: []string{folded.ID},
					TargetID:  target.ID,
					Actor:     "operator",
					Reason:    "confirmed by the operator's answer",
				})
				if err != nil {
					t.Fatalf("MergeEntities: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "split entity",
			kinds: []PublishedKind{
				PublishedEntity, PublishedEntity, PublishedResolution, PublishedMembership,
			},
			write: func(t *testing.T, h *recordingHook) string {
				s, _ := newStore(t, WithSync(h))
				parent := mustEntity(t, s, EntityRepository, "one repository, apparently")
				h.forget()
				record, parts, err := s.SplitEntity(ctx, SplitInput{
					ParentID: parent.ID,
					Actor:    "operator",
					Reason:   "the operator says these were always two repositories",
					Parts: []EntityInput{
						{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "the first"}},
						{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "the second"}},
					},
				})
				if err != nil {
					t.Fatalf("SplitEntity: %v", err)
				}
				if len(parts) != 2 {
					t.Fatalf("split created %d parts, want 2", len(parts))
				}
				return record.ID
			},
		},
		{
			name:  "undo resolution",
			kinds: []PublishedKind{PublishedResolution, PublishedMembership},
			write: func(t *testing.T, h *recordingHook) string {
				s, _ := newStore(t, WithSync(h))
				target := mustEntity(t, s, EntityProject, "a project")
				folded := mustEntity(t, s, EntityProject, "the same project renamed")
				merge, err := s.MergeEntities(ctx, MergeInput{
					SourceIDs: []string{folded.ID},
					TargetID:  target.ID,
					Actor:     "operator",
					Reason:    "confirmed by the operator's answer",
				})
				if err != nil {
					t.Fatalf("MergeEntities: %v", err)
				}
				h.forget()
				record, err := s.UndoResolution(ctx, UndoInput{
					ResolutionID: merge.ID,
					Actor:        "operator",
					Reason:       "the operator was wrong about the rename",
				})
				if err != nil {
					t.Fatalf("UndoResolution: %v", err)
				}
				return record.ID
			},
		},
		{
			name: "accept plan",
			kinds: []PublishedKind{
				PublishedAcceptance, PublishedFact, PublishedResolution, PublishedMembership,
			},
			write: func(t *testing.T, h *recordingHook) string {
				fixture := newPlanFixture(t, WithSync(h))
				h.forget()
				record, _, err := fixture.store.AcceptPlan(ctx, AcceptanceInput{
					PlanID: fixture.plan.ID,
					Actor:  "operator",
					Note:   "accepting",
				})
				if err != nil {
					t.Fatalf("AcceptPlan: %v", err)
				}
				return record.ID
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hook := &recordingHook{}
			id := tc.write(t, hook)

			kinds := hook.stagedKinds(t)
			if len(kinds) != len(tc.kinds) {
				t.Fatalf("staged %v, want the whole set %v", kinds, tc.kinds)
			}
			for i, want := range tc.kinds {
				if kinds[i] != want {
					t.Fatalf("staged %v, want %v in the order the rows were written",
						kinds, tc.kinds)
				}
			}
			for _, rec := range hook.staged {
				if rec.RunID != id {
					t.Fatalf("staged %s under run %q, want the anchor %q: a member in "+
						"another closure is a member the fleet can see without the rest",
						rec.EntityID, rec.RunID, id)
				}
			}
			if len(hook.declared) != 1 || hook.declared[0].RunID != id {
				t.Fatalf("declared %+v, want exactly one closure under the anchor %q: 0003 "+
					"fixes a run's record count at declaration", hook.declared, id)
			}
			if len(hook.closures) != 1 || hook.closures[0].RunID != id {
				t.Fatalf("published %+v, want exactly one closure under %q after the commit",
					hook.closures, id)
			}
			if len(hook.runs) != 0 {
				t.Fatalf("a set path went through the single-record Append %q, which would "+
					"declare a closure of one per member", hook.runs)
			}
		})
	}
}

// TestSetStagingFailureLeavesNothingDurable is the atomicity property for the
// set paths, measured by re-reading every member rather than by trusting the
// returned error.
//
// It matters more here than for a single record. A half-applied set is not
// merely unpublished: an acceptance whose facts rolled back would leave a
// question answered by an authority over nothing, and a merge whose membership
// entries rolled back would leave two identities pointing at each other.
// Rolling the whole transaction back with the staging failure is the only
// ordering under which none of that can exist.
func TestSetStagingFailureLeavesNothingDurable(t *testing.T) {
	ctx := context.Background()
	staging := errors.New("the journal refused the record")

	t.Run("import", func(t *testing.T) {
		hook := &recordingHook{}
		store, clock := newStore(t, WithSync(hook))
		source := registerInventory(t, store)
		machine := mustEntity(t, store, EntityMachine, "a machine")
		service := mustEntity(t, store, EntityService, "a service")
		hook.appendErr = staging

		if _, err := store.ImportFacts(ctx, ImportInput{
			SourceID: source.ID,
			BatchKey: "batch-1",
			Facts: []FactInput{
				inventoryPlacement(service.ID, machine.ID, clock.now()),
			},
		}); !errors.Is(err, staging) {
			t.Fatalf("ImportFacts error %v, want the staging failure", err)
		}
		for _, table := range []string{"reality_import", "reality_fact", "reality_fact_status"} {
			if n := countRows(t, store, table); n != 0 {
				t.Fatalf("%d %s rows survived a staging failure, want none: half a batch is "+
					"worse than none", n, table)
			}
		}
	})

	t.Run("merge", func(t *testing.T) {
		hook := &recordingHook{}
		store, _ := newStore(t, WithSync(hook))
		target := mustEntity(t, store, EntityProject, "a project")
		folded := mustEntity(t, store, EntityProject, "the same project renamed")
		hook.appendErr = staging

		if _, err := store.MergeEntities(ctx, MergeInput{
			SourceIDs: []string{folded.ID},
			TargetID:  target.ID,
			Actor:     "operator",
		}); !errors.Is(err, staging) {
			t.Fatalf("MergeEntities error %v, want the staging failure", err)
		}
		for _, table := range []string{"reality_resolution", "reality_resolution_member"} {
			if n := countRows(t, store, table); n != 0 {
				t.Fatalf("%d %s rows survived a staging failure, want none", n, table)
			}
		}
		// The membership append is the member that would be hardest to see
		// gone, so it is checked through the reader that depends on it.
		resolved, err := store.Resolve(ctx, folded.ID)
		if err != nil {
			t.Fatalf("Resolve after a failed merge: %v", err)
		}
		if resolved != folded.ID {
			t.Fatalf("identity %s resolves to %s after a failed merge, want itself",
				folded.ID, resolved)
		}
	})

	t.Run("split", func(t *testing.T) {
		hook := &recordingHook{}
		store, _ := newStore(t, WithSync(hook))
		parent := mustEntity(t, store, EntityRepository, "one repository, apparently")
		hook.appendErr = staging

		if _, _, err := store.SplitEntity(ctx, SplitInput{
			ParentID: parent.ID,
			Actor:    "operator",
			Parts: []EntityInput{
				{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "the first"}},
				{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "the second"}},
			},
		}); !errors.Is(err, staging) {
			t.Fatalf("SplitEntity error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_resolution"); n != 0 {
			t.Fatalf("%d resolution rows survived a staging failure, want none", n)
		}
		// The parts are members of the set too, so a failure leaves the
		// parent alone in the ledger rather than beside identities no
		// resolution explains.
		if n := countRows(t, store, "reality_entity"); n != 1 {
			t.Fatalf("%d entity rows after a failed split, want only the parent", n)
		}
	})

	t.Run("undo", func(t *testing.T) {
		hook := &recordingHook{}
		store, _ := newStore(t, WithSync(hook))
		target := mustEntity(t, store, EntityProject, "a project")
		folded := mustEntity(t, store, EntityProject, "the same project renamed")
		merge, err := store.MergeEntities(ctx, MergeInput{
			SourceIDs: []string{folded.ID},
			TargetID:  target.ID,
			Actor:     "operator",
		})
		if err != nil {
			t.Fatalf("MergeEntities: %v", err)
		}
		hook.appendErr = staging

		if _, err := store.UndoResolution(ctx, UndoInput{
			ResolutionID: merge.ID,
			Actor:        "operator",
		}); !errors.Is(err, staging) {
			t.Fatalf("UndoResolution error %v, want the staging failure", err)
		}
		if n := countRows(t, store, "reality_resolution"); n != 1 {
			t.Fatalf("%d resolution rows after a failed undo, want only the merge", n)
		}
		// A reversal that half happened would be the worst of the three:
		// nothing is rewritten by an undo, so the only thing that says the
		// merge is still in force is the membership entry it did not replace.
		resolved, err := store.Resolve(ctx, folded.ID)
		if err != nil {
			t.Fatalf("Resolve after a failed undo: %v", err)
		}
		if resolved != target.ID {
			t.Fatalf("identity %s resolves to %s after a failed undo, want the merge target %s",
				folded.ID, resolved, target.ID)
		}
		// The unique index on reverses_id admits exactly one undo per
		// resolution, so a rolled-back one has to leave the merge reversible
		// once the journal is reachable again.
		hook.appendErr = nil
		if _, err := store.UndoResolution(ctx, UndoInput{
			ResolutionID: merge.ID,
			Actor:        "operator",
		}); err != nil {
			t.Fatalf("the merge is no longer reversible after a failed undo: %v", err)
		}
	})

	t.Run("accept plan", func(t *testing.T) {
		hook := &recordingHook{}
		fixture := newPlanFixture(t, WithSync(hook))
		store := fixture.store
		hook.appendErr = staging

		if _, _, err := store.AcceptPlan(ctx, AcceptanceInput{
			PlanID: fixture.plan.ID,
			Actor:  "operator",
			Note:   "accepting",
		}); !errors.Is(err, staging) {
			t.Fatalf("AcceptPlan error %v, want the staging failure", err)
		}
		for _, table := range []string{
			"reality_plan_acceptance", "reality_fact", "reality_resolution",
		} {
			if n := countRows(t, store, table); n != 0 {
				t.Fatalf("%d %s rows survived a staging failure, want none: an acceptance "+
					"without its facts is an authority over nothing", n, table)
			}
		}
		// The plan is still awaiting acceptance, so the operator can accept
		// it again once the journal is reachable.
		reread, err := store.Question(ctx, fixture.question.ID)
		if err != nil {
			t.Fatalf("Question after a failed acceptance: %v", err)
		}
		if reread.State != QuestionPlanReady {
			t.Fatalf("question state %q after a failed acceptance, want %q",
				reread.State, QuestionPlanReady)
		}
	})
}

// TestLocalOnlySetPathsStageNothing is the other half of the set contract: a
// deployment with no shared backend runs every one of these paths and creates
// no journal at all.
//
// It is one store on purpose. Each path here writes into what the last one
// left, which is also the case where an accidental staging call would be
// easiest to miss.
func TestLocalOnlySetPathsStageNothing(t *testing.T) {
	ctx := context.Background()
	fixture := newPlanFixture(t)
	store, clock := fixture.store, fixture.clock

	if _, _, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID,
		Actor:  "operator",
		Note:   "accepting",
	}); err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}
	machine := mustEntity(t, store, EntityMachine, "a machine")
	spare := mustEntity(t, store, EntityMachine, "the same machine renamed")
	merge, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{spare.ID},
		TargetID:  machine.ID,
		Actor:     "operator",
	})
	if err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}
	if _, err := store.UndoResolution(ctx, UndoInput{
		ResolutionID: merge.ID,
		Actor:        "operator",
	}); err != nil {
		t.Fatalf("UndoResolution: %v", err)
	}
	parent := mustEntity(t, store, EntityRepository, "one repository, apparently")
	if _, _, err := store.SplitEntity(ctx, SplitInput{
		ParentID: parent.ID,
		Actor:    "operator",
		Parts: []EntityInput{
			{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "the first"}},
			{Kind: EntityRepository, Payload: EntityPayload{DisplayName: "the second"}},
		},
	}); err != nil {
		t.Fatalf("SplitEntity: %v", err)
	}
	source := registerInventory(t, store)
	service := mustEntity(t, store, EntityService, "a service")
	if _, err := store.ImportFacts(ctx, ImportInput{
		SourceID: source.ID,
		BatchKey: "batch-1",
		Facts:    []FactInput{inventoryPlacement(service.ID, machine.ID, clock.now())},
	}); err != nil {
		t.Fatalf("ImportFacts: %v", err)
	}
	first, _, err := store.AssertFact(ctx, operatorFact(service.ID,
		PredicateLifecycle, enum(LifecycleActive), clock.at))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	second, _, err := store.AssertFact(ctx, operatorFact(service.ID,
		PredicateOwnership, enum(OwnershipOwned), clock.at))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	if _, err := store.DisputeFacts(ctx, DisputeInput{
		FactIDs: []string{first.ID, second.ID},
		Actor:   "operator",
		Reason:  "a retired service cannot still be owned",
	}); err != nil {
		t.Fatalf("DisputeFacts: %v", err)
	}

	// Every path landed, and none of them put a journal in the shared durable
	// file on behalf of a component that is not running.
	if n := countRows(t, store, "reality_resolution"); n != 4 {
		t.Fatalf("%d resolution rows, want the accepted plan's merge, the merge, its undo "+
			"and the split", n)
	}
	var tables int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name LIKE 'sync_%'`).Scan(&tables); err != nil {
		t.Fatalf("read the durable file's table list: %v", err)
	}
	if tables != 0 {
		t.Fatalf("a local-only store created %d journal tables, want none", tables)
	}
}

// The wire records below pass validate, so a refusal test can break exactly one
// rule and know that is the rule it measured. Each is the shape its write path
// stages, which is why the import batch and the membership entry carry no
// payload: their rows have no payload column for one.
func publishedImport() PublishedRecord {
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedImport,
		ID:         "imp_0123456789abcdef",
		RecordedAt: baseTime,
		Batch: &PublishedBatch{
			SourceID:  "synthetic-inventory",
			BatchKey:  "batch-1",
			FactCount: 2,
		},
	}
}

func publishedMembership() PublishedRecord {
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedMembership,
		ID:         "res_0123456789abcdef.ent_0123456789abcdef",
		RecordedAt: baseTime,
		Membership: &PublishedMembershipEntry{
			EntityID:     "ent_0123456789abcdef",
			Role:         RoleMerged,
			CanonicalID:  "ent_fedcba9876543210",
			ResolutionID: "res_0123456789abcdef",
		},
	}
}

func publishedResolution() PublishedRecord {
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedResolution,
		ID:         "res_0123456789abcdef",
		RecordedAt: baseTime,
		Identity: &PublishedIdentity{
			Kind:      ResolutionMerge,
			SourceIDs: []string{"ent_0123456789abcdef"},
			ResultIDs: []string{"ent_fedcba9876543210"},
		},
		Author:  "operator",
		Payload: json.RawMessage(`{"reason":"the operator confirmed the rename"}`),
	}
}

func publishedDispute() PublishedRecord {
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedDispute,
		ID:         "dsp_0123456789abcdef",
		RecordedAt: baseTime,
		Contradiction: &PublishedContradiction{
			SubjectID: "ent_0123456789abcdef",
			Predicate: PredicateLifecycle,
			FactIDs:   []string{"fct_0123456789abcdef", "fct_fedcba9876543210"},
		},
		Author:  "operator",
		Payload: json.RawMessage(`{"reason":"a retired service cannot still be owned"}`),
	}
}

func publishedPlan() PublishedRecord {
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedPlan,
		ID:         "pln_0123456789abcdef",
		RecordedAt: baseTime,
		Interpretation: &PublishedInterpretation{
			QuestionID:         "qst_0123456789abcdef",
			AnswerID:           "ans_0123456789abcdef",
			InterpreterVersion: 3,
			Actions: []PublishedAction{{
				ID:       "act_0123456789abcdef",
				Position: 0,
				Kind:     ActionNone,
				Payload:  json.RawMessage(`{"rationale":"nothing in the answer needs acting on"}`),
			}},
		},
		Payload: json.RawMessage(`{"summary":"record the dormancy the operator stated"}`),
	}
}

func publishedAcceptance() PublishedRecord {
	return PublishedRecord{
		Schema:     RecordSchema,
		Kind:       PublishedAcceptance,
		ID:         "acc_0123456789abcdef",
		RecordedAt: baseTime,
		Approval: &PublishedApproval{
			PlanID:    "pln_0123456789abcdef",
			ContextID: "ctx_0123456789abcdef",
		},
		Author:  "operator",
		Payload: json.RawMessage(`{"note":"accepting"}`),
	}
}

// TestPublishedSetRecordsRoundTrip checks that every kind a set publishes
// survives the trip, by re-encoding what came back and comparing the bytes: a
// field dropped on the way in is missing from the second encoding, which is the
// failure a field-by-field comparison is most likely to miss.
//
// The payload-less kinds are the second assertion's point. Their rows have no
// payload column, so their envelope must arrive with the field absent rather
// than empty or null — a reader that met `null` there would have to decide
// whether the record was malformed, and this is the wire format saying it never
// has to.
func TestPublishedSetRecordsRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		record  PublishedRecord
		payload bool
	}{
		{"an import batch", publishedImport(), false},
		{"a membership entry", publishedMembership(), false},
		{"a resolution", publishedResolution(), true},
		{"a dispute", publishedDispute(), true},
		{"a plan", publishedPlan(), true},
		{"an acceptance", publishedAcceptance(), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := tc.record.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			decoded, err := DecodePublishedRecord(encoded)
			if err != nil {
				t.Fatalf("DecodePublishedRecord: %v", err)
			}
			again, err := decoded.Marshal()
			if err != nil {
				t.Fatalf("re-encode the decoded record: %v", err)
			}
			if string(again) != string(encoded) {
				t.Fatalf("decoded and re-encoded to %s, want the bytes that left: %s",
					again, encoded)
			}
			if decoded.Kind != tc.record.Kind || decoded.ID != tc.record.ID {
				t.Fatalf("decoded head %s %s, want %s %s",
					decoded.Kind, decoded.ID, tc.record.Kind, tc.record.ID)
			}
			if !decoded.RecordedAt.Equal(tc.record.RecordedAt) {
				t.Fatalf("decoded recorded-at %s, want %s", decoded.RecordedAt, tc.record.RecordedAt)
			}
			if carries := len(decoded.Payload) != 0; carries != tc.payload {
				t.Fatalf("decoded payload %q, want carried = %v", decoded.Payload, tc.payload)
			}
			if !tc.payload && bytes.Contains(encoded, []byte(`"payload"`)) {
				t.Fatalf("a %s record put a payload field on the wire: %s", decoded.Kind, encoded)
			}
		})
	}
}

// TestPublishedSetRecordRefusesMalformedShapes breaks one rule per case over the
// kinds a set publishes, in both directions.
//
// A record missing what its type needs is unreadable on arrival; a record
// carrying a field its type has no use for is worse, because it is readable and
// means two things. Either would become an immutable remote row nothing can
// correct, so both are refused before the bytes leave.
func TestPublishedSetRecordRefusesMalformedShapes(t *testing.T) {
	cases := []struct {
		name   string
		base   func() PublishedRecord
		mutate func(p *PublishedRecord)
	}{
		{
			name: "an import carrying a payload its row has no column for",
			base: publishedImport,
			mutate: func(p *PublishedRecord) {
				p.Payload = json.RawMessage(`{"note":"anything at all"}`)
			},
		},
		{
			name: "a membership entry carrying a payload its row has no column for",
			base: publishedMembership,
			mutate: func(p *PublishedRecord) {
				p.Payload = json.RawMessage(`{"note":"anything at all"}`)
			},
		},
		{
			name:   "an import naming no source",
			base:   publishedImport,
			mutate: func(p *PublishedRecord) { p.Batch.SourceID = "" },
		},
		{
			name:   "an import naming no batch key",
			base:   publishedImport,
			mutate: func(p *PublishedRecord) { p.Batch.BatchKey = "" },
		},
		{
			name:   "an import declaring no facts",
			base:   publishedImport,
			mutate: func(p *PublishedRecord) { p.Batch.FactCount = 0 },
		},
		{
			name:   "an import with no batch at all",
			base:   publishedImport,
			mutate: func(p *PublishedRecord) { p.Batch = nil },
		},
		{
			name:   "an import attributed to an operator",
			base:   publishedImport,
			mutate: func(p *PublishedRecord) { p.Author = "operator" },
		},
		{
			name:   "a resolution with nothing on one of its sides",
			base:   publishedResolution,
			mutate: func(p *PublishedRecord) { p.Identity.ResultIDs = nil },
		},
		{
			name:   "a merge that claims to reverse something",
			base:   publishedResolution,
			mutate: func(p *PublishedRecord) { p.Identity.ReversesID = "res_fedcba9876543210" },
		},
		{
			name:   "an undo that reverses nothing",
			base:   publishedResolution,
			mutate: func(p *PublishedRecord) { p.Identity.Kind = ResolutionUndo },
		},
		{
			name:   "a resolution nobody performed",
			base:   publishedResolution,
			mutate: func(p *PublishedRecord) { p.Author = "" },
		},
		{
			name:   "a resolution claiming an instant its row never stored",
			base:   publishedResolution,
			mutate: func(p *PublishedRecord) { p.AuthoredAt = p.RecordedAt },
		},
		{
			name:   "a membership entry naming no identity",
			base:   publishedMembership,
			mutate: func(p *PublishedRecord) { p.Membership.EntityID = "" },
		},
		{
			name:   "a membership entry that does not say what it resolves to",
			base:   publishedMembership,
			mutate: func(p *PublishedRecord) { p.Membership.CanonicalID = "" },
		},
		{
			name:   "a membership entry no resolution wrote",
			base:   publishedMembership,
			mutate: func(p *PublishedRecord) { p.Membership.ResolutionID = "" },
		},
		{
			name: "a membership entry carrying a claim",
			base: publishedMembership,
			mutate: func(p *PublishedRecord) {
				p.Claim = publishedFact().Claim
			},
		},
		{
			name:   "a dispute over one fact",
			base:   publishedDispute,
			mutate: func(p *PublishedRecord) { p.Contradiction.FactIDs = p.Contradiction.FactIDs[:1] },
		},
		{
			name:   "a dispute about no subject",
			base:   publishedDispute,
			mutate: func(p *PublishedRecord) { p.Contradiction.SubjectID = "" },
		},
		{
			name:   "a dispute nobody judged",
			base:   publishedDispute,
			mutate: func(p *PublishedRecord) { p.Author = "" },
		},
		{
			name:   "a plan proposing nothing",
			base:   publishedPlan,
			mutate: func(p *PublishedRecord) { p.Interpretation.Actions = nil },
		},
		{
			name: "a plan action that disagrees with its own position",
			base: publishedPlan,
			mutate: func(p *PublishedRecord) {
				p.Interpretation.Actions[0].Position = 3
			},
		},
		{
			name: "a plan action with no payload",
			base: publishedPlan,
			mutate: func(p *PublishedRecord) {
				p.Interpretation.Actions[0].Payload = nil
			},
		},
		{
			name:   "a plan interpreting no answer",
			base:   publishedPlan,
			mutate: func(p *PublishedRecord) { p.Interpretation.AnswerID = "" },
		},
		{
			name:   "a plan from no interpreter version",
			base:   publishedPlan,
			mutate: func(p *PublishedRecord) { p.Interpretation.InterpreterVersion = 0 },
		},
		{
			name:   "a plan attributed to an operator",
			base:   publishedPlan,
			mutate: func(p *PublishedRecord) { p.Author = "operator" },
		},
		{
			name:   "an acceptance naming no plan",
			base:   publishedAcceptance,
			mutate: func(p *PublishedRecord) { p.Approval.PlanID = "" },
		},
		{
			name:   "an acceptance nobody made",
			base:   publishedAcceptance,
			mutate: func(p *PublishedRecord) { p.Author = "" },
		},
		{
			name:   "an acceptance with no accepted plan at all",
			base:   publishedAcceptance,
			mutate: func(p *PublishedRecord) { p.Approval = nil },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := tc.base()
			tc.mutate(&record)
			if _, err := record.Marshal(); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("Marshal accepted %s: %v", tc.name, err)
			}
		})
	}
}
