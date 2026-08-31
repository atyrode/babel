package disposition

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/sync"
)

// errStagingRefused is what a hook that cannot stage returns. Its identity is
// what the atomicity test follows back out of a write path.
var errStagingRefused = errors.New("staging refused")

// fakeHook is a sync.Hook that records what a write path staged.
//
// It stands in for a Publisher because a real one holds a PostgreSQL handle, an
// encrypted object store and a payload keyring, and none of the three is needed
// to prove what this package stages, under which closure, and when it asks for
// a commit. Whether the shared catalog accepts the bytes is internal/sync's own
// suite's question, against a real database.
type fakeHook struct {
	t *testing.T

	// staged is every record Append received, in order, and producedBy is the
	// producing run each was offered under — the field that decides whether a
	// record joins a run's closure or becomes one of its own.
	staged     []sync.Record
	producedBy []string
	// committed is every closure CommitInline was asked to publish.
	committed []sync.Closure

	// publish is what Append reports. False is the shape of a record joining a
	// run whose closure is still open: staged now, published when the run ends.
	publish bool
	// appendErr makes staging fail, which is the atomicity case — the durable
	// write has to roll back with it.
	appendErr error
}

func (h *fakeHook) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec sync.Record) (sync.Closure, bool, error) {
	h.t.Helper()
	if tx == nil {
		h.t.Errorf("record %s was staged outside the writer's transaction", rec.EntityID)
	}
	if h.appendErr != nil {
		return sync.Closure{}, false, h.appendErr
	}
	h.staged = append(h.staged, rec)
	h.producedBy = append(h.producedBy, producedBy)
	// A closure of one is named by the record's own entity id, which is what
	// the journal does for a record no open run produced.
	return sync.Closure{RunID: rec.EntityID}, h.publish, nil
}

// StageTx and DeclareTx are part of the Hook surface and are deliberately unused
// here: every write path in this package goes through Append, which is what
// chooses the closure. Reaching either would mean a write site made that choice
// for itself, which is the drift Append exists to prevent.
func (h *fakeHook) StageTx(ctx context.Context, tx *sql.Tx, rec sync.Record) error {
	h.t.Errorf("StageTx was called for %s; this package stages through Append", rec.EntityID)
	return nil
}

func (h *fakeHook) DeclareTx(ctx context.Context, tx *sql.Tx, c sync.Closure) error {
	h.t.Errorf("DeclareTx was called for %s; Append declares a closure of one itself", c.RunID)
	return nil
}

func (h *fakeHook) CommitInline(ctx context.Context, c sync.Closure) error {
	h.committed = append(h.committed, c)
	return nil
}

// TestOperatorRecordsPublishAsTheirOwnClosure covers all three durable records
// this package owes the fleet, and the closure rule each publishes under.
//
// An operator's proposed action, their decision on one, and their invitation are
// produced by no run. Each is therefore its own closure of one and publishes as
// soon as its transaction commits: attaching any of them to a run would try to
// join a closure that run declared when it ended, and migration 0003 fixes
// record_count at declaration and never lets it move.
func TestOperatorRecordsPublishAsTheirOwnClosure(t *testing.T) {
	hook := &fakeHook{t: t, publish: true}
	h := newHarness(t, WithSync(hook))
	record := h.hypothesis("the deploy script re-reads a stale manifest")

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindAskQuestion,
		ProposedBy: frontier.Operator("alex"),
		Payload:    Payload{Summary: "who owns the manifest?"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	entry, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingAccepted, By: "alex", Note: "ask in the weekly",
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	invitation, err := h.store.Invite(h.ctx, InviteInput{Record: record, By: "alex"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}

	want := []struct {
		id   string
		kind sharedcatalog.RecordKind
	}{
		{action.ID, sharedcatalog.KindDisposition},
		{entry.ID, sharedcatalog.KindDisposition},
		{invitation.ID, sharedcatalog.KindContext},
	}
	if len(hook.staged) != len(want) {
		t.Fatalf("staged %d records, want %d", len(hook.staged), len(want))
	}
	for i, expect := range want {
		staged := hook.staged[i]
		switch {
		case staged.EntityID != expect.id:
			t.Errorf("record %d staged as %q, want %q", i, staged.EntityID, expect.id)
		case staged.Kind != expect.kind:
			t.Errorf("record %s staged as kind %q, want %q", staged.EntityID, staged.Kind, expect.kind)
		case staged.Schema != RecordSchema:
			t.Errorf("record %s staged at schema %d, want %d", staged.EntityID, staged.Schema, RecordSchema)
		case len(staged.Payload) == 0:
			t.Errorf("record %s staged with no payload", staged.EntityID)
		case hook.producedBy[i] != "":
			t.Errorf("record %s was staged as produced by run %q; an operator's act belongs to no run's closure",
				staged.EntityID, hook.producedBy[i])
		}
	}

	if len(hook.committed) != len(want) {
		t.Fatalf("asked to publish %d closures, want %d", len(hook.committed), len(want))
	}
	for i, expect := range want {
		if hook.committed[i].RunID != expect.id {
			t.Errorf("closure %d published as %q, want the record's own id %q",
				i, hook.committed[i].RunID, expect.id)
		}
	}
}

// TestARunsProposalWaitsForTheRunsClosure defends the other half of the closure
// rule. A run's proposed action is part of that run's output, so it joins the
// run's still-open closure and nothing publishes yet — the run declares itself
// when its receipt ends it. Publishing here would declare a closure that is
// still growing, which 0003 refuses permanently.
func TestARunsProposalWaitsForTheRunsClosure(t *testing.T) {
	// publish stays false, which is what Append reports for a record joining a
	// closure that is still open.
	hook := &fakeHook{t: t}
	h := newHarness(t, WithSync(hook))
	record := h.hypothesis("the retry budget is spent before the first backoff")

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindDevelopFurther,
		ProposedBy: frontier.Run("run-1"),
		Ref:        "act-1",
		Payload:    Payload{Summary: "read the backoff helper's callers"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if len(hook.staged) != 1 {
		t.Fatalf("staged %d records, want 1", len(hook.staged))
	}
	if hook.staged[0].EntityID != action.ID {
		t.Errorf("staged %q, want %q", hook.staged[0].EntityID, action.ID)
	}
	if hook.producedBy[0] != "run-1" {
		t.Errorf("staged as produced by %q, want run-1: a run's proposal is part of that run's output",
			hook.producedBy[0])
	}
	if len(hook.committed) != 0 {
		t.Errorf("published %d closures; a run's closure may not be declared while it can still grow",
			len(hook.committed))
	}
}

// TestStagedPayloadCarriesTheWholeRecord defends what a reader on another host
// cannot otherwise recover.
//
// disposition_proposal.payload_json holds a summary and a rationale;
// disposition_ledger's holds a note. Identity, subject, actor and time live in
// sibling plaintext columns, so staging the payload column alone would publish
// prose nobody could attribute to a record, an author, or a moment. It also
// checks the converse: nothing derived travels, because a derived field on the
// wire is a second answer to a question the reader can already compute.
func TestStagedPayloadCarriesTheWholeRecord(t *testing.T) {
	hook := &fakeHook{t: t, publish: true}
	h := newHarness(t, WithSync(hook))
	record := h.hypothesis("the manifest cache outlives the deploy")

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindStoreMemory,
		ProposedBy: frontier.Operator("alex"),
		Payload:    Payload{Summary: "remember the cache lifetime", Rationale: "it recurs every release"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	entry, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingDeclined, By: "alex", Note: "already in the runbook",
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	invitation, err := h.store.Invite(h.ctx, InviteInput{Record: record, By: "alex"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if len(hook.staged) != 3 {
		t.Fatalf("staged %d records, want 3", len(hook.staged))
	}

	var published publishedDisposition
	if err := json.Unmarshal(hook.staged[0].Payload, &published); err != nil {
		t.Fatalf("decode published proposed action: %v", err)
	}
	switch {
	case published.ID != action.ID:
		t.Errorf("published id %q, want %q", published.ID, action.ID)
	case published.RecordType != record.Type || published.RecordID != record.ID:
		t.Errorf("published subject %s/%s, want %s/%s",
			published.RecordType, published.RecordID, record.Type, record.ID)
	case published.Kind != KindStoreMemory:
		t.Errorf("published kind %q, want %q", published.Kind, KindStoreMemory)
	case published.ProposerKind != frontier.ActorOperator || published.ProposerID != "alex":
		t.Errorf("published proposer %s/%s, want operator/alex", published.ProposerKind, published.ProposerID)
	case published.CreatedAt != formatTime(action.CreatedAt):
		t.Errorf("published time %q, want %q", published.CreatedAt, formatTime(action.CreatedAt))
	}
	var carried Payload
	if err := json.Unmarshal(published.Payload, &carried); err != nil {
		t.Fatalf("decode carried payload: %v", err)
	}
	if carried != action.Payload {
		t.Errorf("carried payload %+v, want the stored %+v", carried, action.Payload)
	}

	// Status is derived from the ledger and the schema version lives in
	// analysis_records.record_schema, so neither belongs in the bytes.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(hook.staged[0].Payload, &fields); err != nil {
		t.Fatalf("decode published proposed action as fields: %v", err)
	}
	for _, derived := range []string{"status", "schema_version"} {
		if _, present := fields[derived]; present {
			t.Errorf("published proposed action carries %q, which the reader can already answer for itself", derived)
		}
	}

	var decision publishedLedgerEntry
	if err := json.Unmarshal(hook.staged[1].Payload, &decision); err != nil {
		t.Fatalf("decode published decision: %v", err)
	}
	switch {
	case decision.ID != entry.ID:
		t.Errorf("published decision id %q, want %q", decision.ID, entry.ID)
	case decision.DispositionID != action.ID:
		t.Errorf("published decision answers %q, want %q", decision.DispositionID, action.ID)
	case decision.Sequence != entry.Sequence:
		t.Errorf("published decision at sequence %d, want %d", decision.Sequence, entry.Sequence)
	case decision.Ruling != RulingDeclined:
		t.Errorf("published ruling %q, want %q", decision.Ruling, RulingDeclined)
	case decision.OperatorID != "alex":
		t.Errorf("published operator %q, want alex", decision.OperatorID)
	case decision.RecordedAt != formatTime(entry.RecordedAt):
		t.Errorf("published time %q, want %q", decision.RecordedAt, formatTime(entry.RecordedAt))
	}
	var note LedgerPayload
	if err := json.Unmarshal(decision.Payload, &note); err != nil {
		t.Fatalf("decode carried note: %v", err)
	}
	if note != entry.Payload {
		t.Errorf("carried note %+v, want the stored %+v", note, entry.Payload)
	}

	var nudge publishedInvitation
	if err := json.Unmarshal(hook.staged[2].Payload, &nudge); err != nil {
		t.Fatalf("decode published invitation: %v", err)
	}
	switch {
	case nudge.ID != invitation.ID:
		t.Errorf("published invitation id %q, want %q", nudge.ID, invitation.ID)
	case nudge.RecordType != record.Type || nudge.RecordID != record.ID:
		t.Errorf("published invitation subject %s/%s, want %s/%s",
			nudge.RecordType, nudge.RecordID, record.Type, record.ID)
	case nudge.OperatorID != "alex":
		t.Errorf("published invitation operator %q, want alex", nudge.OperatorID)
	case nudge.CreatedAt != formatTime(invitation.CreatedAt):
		t.Errorf("published invitation time %q, want %q", nudge.CreatedAt, formatTime(invitation.CreatedAt))
	}
	// #87's invitation carries no operator words, and the published shape must
	// have nowhere to put them either.
	var nudgeFields map[string]json.RawMessage
	if err := json.Unmarshal(hook.staged[2].Payload, &nudgeFields); err != nil {
		t.Fatalf("decode published invitation as fields: %v", err)
	}
	if _, present := nudgeFields["payload"]; present {
		t.Error("a published invitation carries a payload field; there is nowhere an instruction may appear")
	}
}

// TestAFailedStagingLeavesNothingDurable is the atomicity property, and it is
// the reason staging shares the writer's transaction rather than following it.
//
// A record that committed locally while its journal row did not would be
// durable, invisible to the publisher, and reported by nothing — the one
// failure this design cannot tolerate. So a refused staging rolls the durable
// write back with it, and every read afterwards has to show the write never
// happened.
func TestAFailedStagingLeavesNothingDurable(t *testing.T) {
	hook := &fakeHook{t: t, publish: true}
	h := newHarness(t, WithSync(hook))
	record := h.hypothesis("an idea")

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindAskQuestion,
		ProposedBy: frontier.Operator("alex"),
		Payload:    Payload{Summary: "who owns this?"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	hook.appendErr = errStagingRefused

	if _, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindStoreMemory,
		ProposedBy: frontier.Operator("alex"),
		Payload:    Payload{Summary: "remember the manifest"},
	}); !errors.Is(err, errStagingRefused) {
		t.Fatalf("propose under a refusing hook: got %v, want errStagingRefused", err)
	}
	actions, total, err := h.store.List(h.ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list proposed actions: %v", err)
	}
	if total != 1 || len(actions) != 1 || actions[0].ID != action.ID {
		t.Fatalf("after a refused staging the store holds %d proposed actions, want only %s", total, action.ID)
	}

	if _, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingAccepted, By: "alex",
	}); !errors.Is(err, errStagingRefused) {
		t.Fatalf("decide under a refusing hook: got %v, want errStagingRefused", err)
	}
	ledger, err := h.store.Ledger(h.ctx, action.ID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(ledger) != 0 {
		t.Fatalf("after a refused staging the ledger holds %d entries, want none", len(ledger))
	}

	if _, err := h.store.Invite(h.ctx, InviteInput{Record: record, By: "alex"}); !errors.Is(err, errStagingRefused) {
		t.Fatalf("invite under a refusing hook: got %v, want errStagingRefused", err)
	}
	invitations, err := h.store.Invitations(h.ctx, InvitationFilter{})
	if err != nil {
		t.Fatalf("read invitations: %v", err)
	}
	if len(invitations) != 0 {
		t.Fatalf("after a refused staging the queue holds %d invitations, want none", len(invitations))
	}

	// The refused attempts must not have reached the publisher either: there is
	// nothing to publish when there is nothing durable.
	if len(hook.committed) != 1 {
		t.Errorf("published %d closures, want only the one that committed", len(hook.committed))
	}
}

// TestALocalOnlyStoreStagesNothing is the compatibility property. A store opened
// without WithSync is the store every existing caller opens, and it must behave
// exactly as it did before publication existed: the same rows, and no journal.
func TestALocalOnlyStoreStagesNothing(t *testing.T) {
	h := newHarness(t)
	record := h.hypothesis("an idea")

	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record:     record,
		Kind:       KindAskQuestion,
		ProposedBy: frontier.Operator("alex"),
		Payload:    Payload{Summary: "who owns this?"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := h.store.Decide(h.ctx, DecideInput{
		DispositionID: action.ID, Ruling: RulingAccepted, By: "alex",
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	invitation, err := h.store.Invite(h.ctx, InviteInput{Record: record, By: "alex"})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := h.store.Invitation(h.ctx, invitation.ID); err != nil {
		t.Fatalf("read back the invitation: %v", err)
	}

	// Open creates the journal tables only for a store that stages, so their
	// absence is what "stages nothing" looks like from the durable file.
	var name string
	err = h.store.db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'sync_run'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a store opened without WithSync created the sync journal: %v, %q", err, name)
	}
}

func validPublishedDisposition() publishedDisposition {
	return publishedDisposition{
		ID:           "dis_01",
		RecordType:   frontier.EntityHypothesis,
		RecordID:     "hyp_01",
		Kind:         KindAskQuestion,
		ProposerKind: frontier.ActorOperator,
		ProposerID:   "alex",
		CreatedAt:    formatTime(time.Unix(0, 0)),
		Payload:      json.RawMessage(`{"summary":"who owns this?"}`),
	}
}

func validPublishedLedgerEntry() publishedLedgerEntry {
	return publishedLedgerEntry{
		ID:            "dld_01",
		DispositionID: "dis_01",
		Sequence:      1,
		Ruling:        RulingAccepted,
		OperatorID:    "alex",
		RecordedAt:    formatTime(time.Unix(0, 0)),
		Payload:       json.RawMessage(`{"note":"asked in the weekly"}`),
	}
}

func validPublishedInvitation() publishedInvitation {
	return publishedInvitation{
		ID:         "inv_01",
		RecordType: frontier.EntityHypothesis,
		RecordID:   "hyp_01",
		OperatorID: "alex",
		CreatedAt:  formatTime(time.Unix(0, 0)),
	}
}

// TestPublishedRecordsRoundTrip proves each published shape survives the trip a
// second host makes it take. The bytes are what a reader on another machine gets
// and the only copy of the record it will ever have, so a field that encodes and
// does not decode is a field that machine loses silently.
func TestPublishedRecordsRoundTrip(t *testing.T) {
	t.Run("a proposed action", func(t *testing.T) {
		original := validPublishedDisposition()
		original.EmittedRef = "act-1"
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var decoded publishedDisposition
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if string(decoded.Payload) != string(original.Payload) {
			t.Errorf("payload %s, want %s", decoded.Payload, original.Payload)
		}
		again, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if string(again) != string(encoded) {
			t.Errorf("re-encoded to %s, want %s", again, encoded)
		}
	})

	t.Run("a decision", func(t *testing.T) {
		original := validPublishedLedgerEntry()
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var decoded publishedLedgerEntry
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if string(decoded.Payload) != string(original.Payload) {
			t.Errorf("payload %s, want %s", decoded.Payload, original.Payload)
		}
		again, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		if string(again) != string(encoded) {
			t.Errorf("re-encoded to %s, want %s", again, encoded)
		}
	})

	t.Run("an invitation", func(t *testing.T) {
		original := validPublishedInvitation()
		encoded, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		var decoded publishedInvitation
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if decoded != original {
			t.Errorf("decoded %+v, want %+v", decoded, original)
		}
	})
}

// TestPublishedRecordsRefuseWhatAReaderCannotUse covers one refusal per rule in
// each shape's validate, through the marshaller that calls it.
//
// The marshaller is where the check has to bite. These bytes become an
// immutable, sealed object in a shared catalog whose analysis_records is
// insert-only, so a malformed one cannot be corrected there — the only place a
// refusal costs nothing is before the transaction that stages it.
func TestPublishedRecordsRefuseWhatAReaderCannotUse(t *testing.T) {
	t.Run("a proposed action", func(t *testing.T) {
		for _, tc := range []struct {
			rule   string
			mutate func(*publishedDisposition)
		}{
			{"no id", func(p *publishedDisposition) { p.ID = "" }},
			{"no record type", func(p *publishedDisposition) { p.RecordType = "" }},
			{"no record id", func(p *publishedDisposition) { p.RecordID = "" }},
			{"a kind outside the vocabulary", func(p *publishedDisposition) { p.Kind = "publish-it" }},
			{"no proposer kind", func(p *publishedDisposition) { p.ProposerKind = "" }},
			{"no proposer id", func(p *publishedDisposition) { p.ProposerID = "" }},
			{"no payload", func(p *publishedDisposition) { p.Payload = nil }},
			{"an unparseable time", func(p *publishedDisposition) { p.CreatedAt = "yesterday" }},
		} {
			t.Run(tc.rule, func(t *testing.T) {
				record := validPublishedDisposition()
				tc.mutate(&record)
				if _, err := json.Marshal(record); !errors.Is(err, ErrInvalidValue) {
					t.Fatalf("got %v, want ErrInvalidValue", err)
				}
			})
		}
	})

	t.Run("a decision", func(t *testing.T) {
		for _, tc := range []struct {
			rule   string
			mutate func(*publishedLedgerEntry)
		}{
			{"no id", func(p *publishedLedgerEntry) { p.ID = "" }},
			{"no proposed action", func(p *publishedLedgerEntry) { p.DispositionID = "" }},
			{"no position in the ledger", func(p *publishedLedgerEntry) { p.Sequence = 0 }},
			{"a ruling outside the vocabulary", func(p *publishedLedgerEntry) { p.Ruling = "maybe" }},
			{"no operator", func(p *publishedLedgerEntry) { p.OperatorID = "" }},
			{"no payload", func(p *publishedLedgerEntry) { p.Payload = nil }},
			{"an unparseable time", func(p *publishedLedgerEntry) { p.RecordedAt = "yesterday" }},
		} {
			t.Run(tc.rule, func(t *testing.T) {
				record := validPublishedLedgerEntry()
				tc.mutate(&record)
				if _, err := json.Marshal(record); !errors.Is(err, ErrInvalidValue) {
					t.Fatalf("got %v, want ErrInvalidValue", err)
				}
			})
		}
	})

	t.Run("an invitation", func(t *testing.T) {
		for _, tc := range []struct {
			rule   string
			mutate func(*publishedInvitation)
		}{
			{"no id", func(p *publishedInvitation) { p.ID = "" }},
			{"no record type", func(p *publishedInvitation) { p.RecordType = "" }},
			{"no record id", func(p *publishedInvitation) { p.RecordID = "" }},
			{"no operator", func(p *publishedInvitation) { p.OperatorID = "" }},
			{"an unparseable time", func(p *publishedInvitation) { p.CreatedAt = "yesterday" }},
		} {
			t.Run(tc.rule, func(t *testing.T) {
				record := validPublishedInvitation()
				tc.mutate(&record)
				if _, err := json.Marshal(record); !errors.Is(err, ErrInvalidValue) {
					t.Fatalf("got %v, want ErrInvalidValue", err)
				}
			})
		}
	})
}
