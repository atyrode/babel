package disposition

import (
	"context"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// harness opens a frontier and the disposition component above it in one
// temporary directory, which is also the arrangement production uses: both
// components share §9's single durable file.
type harness struct {
	t     *testing.T
	ctx   context.Context
	front *frontier.Store
	store *Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() {
		if err := front.Close(); err != nil {
			t.Errorf("close frontier: %v", err)
		}
	})
	store, err := Open(dir, front)
	if err != nil {
		t.Fatalf("open dispositions: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close dispositions: %v", err)
		}
	})
	return &harness{t: t, ctx: context.Background(), front: front, store: store}
}

// hypothesis creates one candidate to attach actions and invitations to.
func (h *harness) hypothesis(statement string) frontier.Ref {
	h.t.Helper()
	record, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:   "run-1",
		Payload: frontier.HypothesisPayload{Statement: statement, Novelty: 0.3, Priority: 0.3},
	})
	if err != nil {
		h.t.Fatalf("create hypothesis: %v", err)
	}
	return frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID}
}

// TestLedgerAttributesEveryDecision covers #88's evidence source. What the
// ledger has to survive is not "was it accepted" but "who decided, when, and
// what did they decide before" — an acceptance rate computed from a
// last-write-wins column would silently discard every reconsideration.
func TestLedgerAttributesEveryDecision(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("handoffs drop constraints")

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindDevelopFurther,
		ProposedBy: frontier.Run("run-1"),
		Ref:        "act-1",
		Payload:    Payload{Summary: "search the other repositories for the same handoff"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if action.Status != StatusProposed {
		t.Fatalf("a fresh action is %q, want proposed", action.Status)
	}

	declined, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingDeclined, By: "alex", Note: "not now",
	})
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	accepted, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingAccepted, By: "alex", Note: "the pattern recurred",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if declined.Sequence != 1 || accepted.Sequence != 2 {
		t.Fatalf("ledger sequences = %d, %d; want 1, 2", declined.Sequence, accepted.Sequence)
	}

	ledger, err := h.store.Ledger(h.ctx, action.ID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(ledger) != 2 {
		t.Fatalf("ledger has %d entries, want 2; reconsidering appends", len(ledger))
	}
	for i, entry := range ledger {
		if entry.By != "alex" {
			t.Errorf("entry %d attributed to %q, want alex", i, entry.By)
		}
		if entry.RecordedAt.IsZero() {
			t.Errorf("entry %d has no timestamp", i)
		}
	}
	if ledger[0].Ruling != RulingDeclined || ledger[1].Ruling != RulingAccepted {
		t.Errorf("ledger rulings = %q, %q; want declined then accepted", ledger[0].Ruling, ledger[1].Ruling)
	}
	if ledger[0].Payload.Note != "not now" {
		t.Errorf("the declining note was lost: %q", ledger[0].Payload.Note)
	}

	reread, err := h.store.Disposition(h.ctx, action.ID)
	if err != nil {
		t.Fatalf("reread action: %v", err)
	}
	if reread.Status != StatusAccepted {
		t.Errorf("derived status = %q, want accepted", reread.Status)
	}
}

// TestADecisionIsRefusedWithoutAnOperator pins the one thing "suggestions,
// never side effects" rests on: an authorization nobody signed.
func TestADecisionIsRefusedWithoutAnOperator(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")
	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindAskQuestion,
		ProposedBy: frontier.Operator("alex"),
		Payload:    Payload{Summary: "who owns the deploy pipeline?"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingAccepted,
	}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unattributed decision: got %v, want ErrInvalidValue", err)
	}
	if _, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: "dis_absent", Ruling: RulingAccepted, By: "alex",
	}); !errors.Is(err, ErrUnknownDisposition) {
		t.Fatalf("decision on an absent action: got %v, want ErrUnknownDisposition", err)
	}
	ledger, err := h.store.Ledger(h.ctx, action.ID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(ledger) != 0 {
		t.Fatalf("refused decisions appended %d entries", len(ledger))
	}
}

// TestARunsProposalIsIdempotentUnderItsRef covers §6.5's resume rule one table
// over from the records. A replayed result must not double every button the
// operator sees.
func TestARunsProposalIsIdempotentUnderItsRef(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")
	in := ProposeInput{
		Record:     record,
		Kind:       KindStoreMemory,
		ProposedBy: frontier.Run("run-1"),
		Ref:        "act-1",
		Payload:    Payload{Summary: "remember that deploys are Friday-frozen"},
	}
	first, err := h.store.Propose(h.ctx, in)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	second, err := h.store.Propose(h.ctx, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("a replayed proposal minted %s beside %s", second.ID, first.ID)
	}
	_, total, err := h.store.List(h.ctx, ListFilter{Record: record})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Fatalf("the record carries %d actions after a replay, want 1", total)
	}

	// A different run reusing the same ref is a different proposal: refs are
	// unique within one result, not across runs.
	other := in
	other.ProposedBy = frontier.Run("run-2")
	third, err := h.store.Propose(h.ctx, other)
	if err != nil {
		t.Fatalf("second run's proposal: %v", err)
	}
	if third.ID == first.ID {
		t.Fatal("two runs' proposals collapsed into one")
	}
}

// TestProposalRefusesAnIncoherentAuthor keeps the resume key honest at both
// ends: a run without its ref cannot be deduplicated, and an operator with one
// would be claiming a resume identity no replay will ever present.
func TestProposalRefusesAnIncoherentAuthor(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")
	base := ProposeInput{Record: record, Kind: KindAskQuestion, Payload: Payload{Summary: "who owns this?"}}

	runWithoutRef := base
	runWithoutRef.ProposedBy = frontier.Run("run-1")
	if _, err := h.store.Propose(h.ctx, runWithoutRef); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("run without a ref: got %v, want ErrInvalidValue", err)
	}
	operatorWithRef := base
	operatorWithRef.ProposedBy = frontier.Operator("alex")
	operatorWithRef.Ref = "act-1"
	if _, err := h.store.Propose(h.ctx, operatorWithRef); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("operator with a ref: got %v, want ErrInvalidValue", err)
	}
	unknownKind := base
	unknownKind.ProposedBy = frontier.Operator("alex")
	unknownKind.Kind = "publish-issue"
	if _, err := h.store.Propose(h.ctx, unknownKind); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unknown kind: got %v, want ErrInvalidValue", err)
	}
	danglingRecord := base
	danglingRecord.ProposedBy = frontier.Operator("alex")
	danglingRecord.Record = frontier.Ref{Type: frontier.EntityHypothesis, ID: "hyp_absent"}
	if _, err := h.store.Propose(h.ctx, danglingRecord); !errors.Is(err, frontier.ErrUnknownEntity) {
		t.Fatalf("dangling record: got %v, want ErrUnknownEntity", err)
	}
}

// TestInvitationsAreConsumedOnce is #96's rung-one guarantee: an operator's
// nudge buys exactly one run's attention, so a second cycle cannot spend the
// budget again on the same request, and a resumed run cannot either.
func TestInvitationsAreConsumedOnce(t *testing.T) {
	h := newHarness(t)
	first := h.hypothesis("the first candidate")
	second := h.hypothesis("the second candidate")

	a, err := h.store.Invite(h.ctx, InviteInput{Record: first, By: "alex"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	b, err := h.store.Invite(h.ctx, InviteInput{Record: second, By: "alex"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if !a.Open() || !b.Open() {
		t.Fatal("a fresh invitation is not open")
	}

	queue, err := h.store.Invitations(h.ctx, InvitationFilter{})
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("queue has %d entries, want 2", len(queue))
	}
	if queue[0].ID != a.ID {
		t.Errorf("queue is not oldest first: %s came before %s", queue[0].ID, a.ID)
	}

	taken, err := h.store.Consume(h.ctx, "run-9", 0)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(taken) != 2 {
		t.Fatalf("the run took %d invitations, want 2", len(taken))
	}
	again, err := h.store.Consume(h.ctx, "run-10", 0)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("a second run took %d already-consumed invitations", len(again))
	}
	// A resumed run reaching for the same invitation is the same refusal.
	if _, err := h.store.ConsumeOne(h.ctx, a.ID, "run-9"); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("re-consuming: got %v, want ErrAlreadyConsumed", err)
	}

	// The consumed invitation stays readable with the run that took it: it
	// is the provenance of why that run looked at the record.
	open, err := h.store.Invitations(h.ctx, InvitationFilter{})
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("the open queue still has %d entries", len(open))
	}
	all, err := h.store.Invitations(h.ctx, InvitationFilter{All: true})
	if err != nil {
		t.Fatalf("read whole queue: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("the whole queue has %d entries, want 2", len(all))
	}
	for _, invitation := range all {
		if invitation.ConsumedBy != "run-9" {
			t.Errorf("invitation %s was consumed by %q, want run-9", invitation.ID, invitation.ConsumedBy)
		}
		if invitation.Open() {
			t.Errorf("invitation %s still reads as open", invitation.ID)
		}
		if invitation.ConsumedAt.IsZero() {
			t.Errorf("invitation %s records no consumption time", invitation.ID)
		}
	}
}

// TestAnInvitationIsAttributedAndPointsAtARealRecord covers the two refusals
// that keep the queue trustworthy: an invitation borrows operator authority,
// and rung one must not contain work that names nothing.
func TestAnInvitationIsAttributedAndPointsAtARealRecord(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")

	if _, err := h.store.Invite(h.ctx, InviteInput{Record: record}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unattributed invitation: got %v, want ErrInvalidValue", err)
	}
	if _, err := h.store.Invite(h.ctx, InviteInput{
		Record: frontier.Ref{Type: frontier.EntityHypothesis, ID: "hyp_absent"}, By: "alex",
	}); !errors.Is(err, frontier.ErrUnknownEntity) {
		t.Fatalf("dangling invitation: got %v, want ErrUnknownEntity", err)
	}
	if _, err := h.store.ConsumeOne(h.ctx, "inv_absent", "run-1"); !errors.Is(err, ErrUnknownInvitation) {
		t.Fatalf("consuming an absent invitation: got %v, want ErrUnknownInvitation", err)
	}
	queue, err := h.store.Invitations(h.ctx, InvitationFilter{All: true})
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("refused invitations wrote %d rows", len(queue))
	}
}

// TestListNarrowsByKindAndDerivedStatus checks the filter an operator triages
// with. Status is derived rather than stored, so the interesting case is that
// filtering on it still reports a total a caller can page against.
func TestListNarrowsByKindAndDerivedStatus(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")

	kinds := []Kind{KindDevelopFurther, KindAskQuestion, KindStoreMemory}
	var ids []string
	for i, kind := range kinds {
		action, err := h.store.Propose(h.ctx, ProposeInput{
			Record:     record,
			Kind:       kind,
			ProposedBy: frontier.Run("run-1"),
			Ref:        string(rune('a' + i)),
			Payload:    Payload{Summary: "action " + string(kind)},
		})
		if err != nil {
			t.Fatalf("propose %s: %v", kind, err)
		}
		ids = append(ids, action.ID)
	}
	if _, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: ids[0], Ruling: RulingAccepted, By: "alex",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}

	byKind, total, err := h.store.List(h.ctx, ListFilter{Kinds: []Kind{KindAskQuestion}})
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if total != 1 || len(byKind) != 1 || byKind[0].Kind != KindAskQuestion {
		t.Fatalf("kind filter returned %d of %d, first %+v", len(byKind), total, byKind)
	}
	byStatus, total, err := h.store.List(h.ctx, ListFilter{Statuses: []Status{StatusAccepted}})
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if total != 1 || len(byStatus) != 1 || byStatus[0].ID != ids[0] {
		t.Fatalf("status filter returned %d of %d", len(byStatus), total)
	}
	proposed, total, err := h.store.List(h.ctx, ListFilter{Statuses: []Status{StatusProposed}})
	if err != nil {
		t.Fatalf("list proposed: %v", err)
	}
	if total != 2 || len(proposed) != 2 {
		t.Fatalf("proposed filter returned %d of %d, want 2", len(proposed), total)
	}
}

// TestEveryTableRefusesUpdateAndDelete checks the append-only rule where it
// survives a future caller: in the engine. #88 reads this ledger back as
// provenance, and provenance that a later statement can rewrite is not
// provenance.
func TestEveryTableRefusesUpdateAndDelete(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")
	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record: record, Kind: KindDevelopFurther, ProposedBy: frontier.Run("run-1"), Ref: "act-1",
		Payload: Payload{Summary: "keep going"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingAccepted, By: "alex",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	invitation, err := h.store.Invite(h.ctx, InviteInput{Record: record, By: "alex"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := h.store.ConsumeOne(h.ctx, invitation.ID, "run-9"); err != nil {
		t.Fatalf("consume: %v", err)
	}

	cases := map[string]struct{ set, where string }{
		"disposition_proposal":               {`kind = 'draft-issue'`, `id = '` + action.ID + `'`},
		"disposition_ledger":                 {`ruling = 'declined'`, `disposition_id = '` + action.ID + `'`},
		"disposition_invitation":             {`operator_id = 'forged'`, `id = '` + invitation.ID + `'`},
		"disposition_invitation_consumption": {`run_id = 'forged'`, `invitation_id = '` + invitation.ID + `'`},
	}
	tables := componentTables(t, h.store)
	if len(tables) != len(cases) {
		t.Fatalf("the component has tables %v, and %d are checked", tables, len(cases))
	}
	for _, table := range tables {
		tc, ok := cases[table]
		if !ok {
			t.Fatalf("table %s is not checked for immutability", table)
		}
		t.Run(table, func(t *testing.T) {
			var before int
			if err := h.store.db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE ` + tc.where).Scan(&before); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if before == 0 {
				t.Fatalf("no %s row matches, so the statements below would abort nothing", table)
			}
			if _, err := h.store.db.Exec(`UPDATE ` + table + ` SET ` + tc.set + ` WHERE ` + tc.where); err == nil {
				t.Errorf("%s accepted an UPDATE", table)
			}
			if _, err := h.store.db.Exec(`DELETE FROM ` + table + ` WHERE ` + tc.where); err == nil {
				t.Errorf("%s accepted a DELETE", table)
			}
			var after int
			if err := h.store.db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE ` + tc.where).Scan(&after); err != nil {
				t.Fatalf("recount %s: %v", table, err)
			}
			if after != before {
				t.Errorf("%s went from %d rows to %d", table, before, after)
			}
		})
	}
}

// componentTables lists this component's tables as the file actually carries
// them, so a table added later without triggers fails the check above rather
// than quietly joining the schema unprotected.
func componentTables(t *testing.T, store *Store) []string {
	t.Helper()
	rows, err := store.db.Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'disposition_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("list tables: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return tables
}

// TestPlaintextColumnsMatchAllowlist pins the §9 split, and pins the one
// deliberate departure from it: the invitation table has no payload column,
// because an instruction-free nudge has nothing to seal and nowhere for an
// instruction to appear later.
func TestPlaintextColumnsMatchAllowlist(t *testing.T) {
	h := newHarness(t)
	want := map[string][]string{
		"disposition_proposal": {
			"id", "record_type", "record_id", "kind", "proposer_kind", "proposer_id",
			"emitted_ref", "schema_version", "created_at",
		},
		"disposition_ledger": {
			"id", "disposition_id", "seq", "ruling", "operator_id", "schema_version", "recorded_at",
		},
		"disposition_invitation":             {"id", "record_type", "record_id", "operator_id", "created_at"},
		"disposition_invitation_consumption": {"invitation_id", "run_id", "consumed_at"},
	}
	payloadFree := map[string]bool{
		"disposition_invitation":             true,
		"disposition_invitation_consumption": true,
	}
	for _, table := range componentTables(t, h.store) {
		expected, ok := want[table]
		if !ok {
			t.Fatalf("table %s is not described in the plaintext allowlist", table)
		}
		rows, err := h.store.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("read %s columns: %v", table, err)
		}
		var plaintext []string
		payloads := 0
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatalf("read %s columns: %v", table, err)
			}
			if column == "payload_json" {
				payloads++
				continue
			}
			plaintext = append(plaintext, column)
		}
		rows.Close()
		if payloadFree[table] && payloads != 0 {
			t.Fatalf("%s carries %d payload columns; it holds no operator words", table, payloads)
		}
		if !payloadFree[table] && payloads != 1 {
			t.Fatalf("%s has %d payload columns, want exactly 1", table, payloads)
		}
		if len(plaintext) != len(expected) {
			t.Fatalf("%s plaintext columns = %v, want %v", table, plaintext, expected)
		}
		for i := range plaintext {
			if plaintext[i] != expected[i] {
				t.Fatalf("%s plaintext columns = %v, want %v", table, plaintext, expected)
			}
		}
	}
}
