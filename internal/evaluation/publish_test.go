package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// What this file defends is the property a second instance depends on: the
// bytes this store publishes are enough to reconstruct what happened, and not
// enough to be misread.

// A non-producing instance must be able to rebuild coverage from the published
// records alone - including the assignments nobody finished and the exposures
// that produced nothing - rather than counting the assessments that happened
// to complete.
func TestPublishedRecordsRebuildCoverageOnAnotherInstance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	opened := h.claim(t, "asg_pub_open", RoleReception, testRun)
	if err := h.store.Expose(ctx, opened.ID, testRun, opened.Fence); err != nil {
		t.Fatalf("expose: %v", err)
	}

	skipped := h.claim(t, "asg_pub_skip", RoleReception, testRun)
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: skipped.ID, RunID: testRun, Fence: skipped.Fence,
		SkipReason: "no evaluator for this source",
	}); err != nil {
		t.Fatalf("skip: %v", err)
	}

	// A third assignment is granted and never touched at all: the case that
	// is invisible unless grants publish.
	granted := h.claim(t, "asg_pub_idle", RoleEvidence, testRun)

	if err := h.store.Checkpoint(ctx, time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC),
		"inventory-7", 3); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	// Everything a second instance would read: the staged payloads, decoded
	// with no access to this store's database.
	var (
		grants      = map[string]Assignment{}
		attempts    = map[string][]Attempt{}
		checkpoints []CoverageCheckpoint
		assessments int
	)
	for _, staged := range h.hook.staged {
		record, err := DecodePublished(staged.Payload, staged.EntityID, staged.Schema)
		if err != nil {
			t.Fatalf("decode published record: %v", err)
		}
		switch record.Kind {
		case KindAssignment:
			grants[record.Assignment.ID] = *record.Assignment
		case KindAttempt:
			attempts[record.Attempt.AssignmentID] = append(attempts[record.Attempt.AssignmentID],
				*record.Attempt)
		case KindCheckpoint:
			checkpoints = append(checkpoints, *record.Checkpoint)
		case KindAssessment:
			assessments++
		}
	}
	for _, id := range []string{opened.ID, skipped.ID, granted.ID} {
		grant, ok := grants[id]
		if !ok {
			t.Fatalf("assignment %s is invisible to a non-producing instance", id)
		}
		if grant.Role == "" || grant.PolicyVersion == "" || grant.InputDigest == "" {
			t.Fatalf("published grant %s lost its replay inputs: %+v", id, grant)
		}
	}
	if len(attempts[opened.ID]) != 1 || attempts[opened.ID][0].State != AttemptExposed {
		t.Fatalf("the exposure with no result is not reconstructible: %+v", attempts[opened.ID])
	}
	if len(attempts[skipped.ID]) != 1 || attempts[skipped.ID][0].State != AttemptSkipped {
		t.Fatalf("the skip is not reconstructible: %+v", attempts[skipped.ID])
	}
	if attempts[skipped.ID][0].Reason == "" {
		t.Fatalf("the skip published no reason, so the gap cannot be explained")
	}
	if len(attempts[granted.ID]) != 0 {
		t.Fatalf("an untouched assignment published %d attempts", len(attempts[granted.ID]))
	}
	if assessments != 0 {
		t.Fatalf("a skip and an exposure produced %d assessments", assessments)
	}
	if len(checkpoints) != 1 || checkpoints[0].Covered != 3 {
		t.Fatalf("coverage sweeps = %+v", checkpoints)
	}
}

// Decode is the gate, not a parser. Every refusal here is a record that would
// otherwise be ingested as something it is not.
func TestDecodeRefusesUnreadableAndUnauthorizedRecords(t *testing.T) {
	valid := Record{
		ID: "evr_1", Kind: KindAssessment, Subject: proposalSubject(), AssignmentID: "asg_1",
		ActorKind: ActorRun, ActorID: testRun, CreatedAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC),
		Assessment: &Assessment{Vote: VoteSupport},
	}
	encoded, err := encodeRecord(valid)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("a valid record was refused: %v", err)
	}

	t.Run("a newer schema is refused by name and not discarded", func(t *testing.T) {
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		wire["schema"] = RecordSchema + 1
		newer, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		_, err = Decode(newer)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("error = %v, want ErrUnavailable", err)
		}
	})

	t.Run("no schema version at all", func(t *testing.T) {
		bare, err := json.Marshal(map[string]any{"record": valid})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := Decode(bare); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(r *Record)
	}{
		{
			name:   "an operator-signed assessment",
			mutate: func(r *Record) { r.ActorKind = ActorOperator; r.ActorID = "alex" },
		},
		{
			name:   "a run-signed criteria record",
			mutate: func(r *Record) { r.Kind = KindCriteria; r.Assessment = nil },
		},
		{
			name:   "an assessment with no authorizing assignment",
			mutate: func(r *Record) { r.AssignmentID = "" },
		},
		{
			name:   "an unknown record kind",
			mutate: func(r *Record) { r.Kind = "endorsement" },
		},
		{
			name:   "an unknown vote",
			mutate: func(r *Record) { r.Assessment = &Assessment{Vote: "enthusiastic"} },
		},
		{
			name: "an attempt that disagrees with the assignment it names",
			mutate: func(r *Record) {
				r.Kind = KindAttempt
				r.Subject = Subject{}
				r.Assessment = nil
				r.Attempt = &Attempt{AssignmentID: "asg_other", State: AttemptExposed,
					RecordedAt: r.CreatedAt}
			},
		},
		{
			name: "a checkpoint claiming an author",
			mutate: func(r *Record) {
				r.Kind = KindCheckpoint
				r.Subject = Subject{}
				r.Assessment = nil
				r.AssignmentID = ""
				r.Checkpoint = &CoverageCheckpoint{At: r.CreatedAt, InputDigest: "inv", Covered: 1}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := valid
			tc.mutate(&record)
			// The wire form is assembled directly: encodeRecord would refuse
			// these, and what is being measured is the reader's refusal.
			bytes, err := json.Marshal(envelope{Schema: RecordSchema, Record: record})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := Decode(bytes); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// The catalog row is the authority for identity: an object filed under the
// wrong row is a refused read rather than a statement attributed to the wrong
// record.
func TestDecodePublishedChecksTheCatalogRow(t *testing.T) {
	record := Record{
		ID: "evr_1", Kind: KindAssessment, Subject: proposalSubject(), AssignmentID: "asg_1",
		ActorKind: ActorRun, ActorID: testRun, CreatedAt: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC),
		Assessment: &Assessment{Vote: VoteOppose},
	}
	encoded, err := encodeRecord(record)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := DecodePublished(encoded, "evr_1", RecordSchema); err != nil {
		t.Fatalf("a faithful row was refused: %v", err)
	}
	if _, err := DecodePublished(encoded, "evr_somethingelse", RecordSchema); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched id error = %v, want ErrInvalid", err)
	}
	if _, err := DecodePublished(encoded, "evr_1", RecordSchema+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched schema error = %v, want ErrInvalid", err)
	}
	if _, err := DecodePublished(encoded, "", RecordSchema); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a decode with no row error = %v, want ErrInvalid", err)
	}
	schema, err := DecodeSchema(encoded)
	if err != nil || schema != RecordSchema {
		t.Fatalf("DecodeSchema = %d, %v", schema, err)
	}
}

// A policy record is the local operator configuration and reads back as
// itself; with none recorded, the store falls back to the documented default
// rather than to an implied authorization.
func TestPolicyRecordRoundTripsAndFallsBackToDefault(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	fallback, err := h.store.Policy(ctx)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if fallback.Version == "" {
		t.Fatalf("the default policy carries no version, so nothing could replay a draw under it")
	}

	configured := testPolicy()
	configured.Version = "operator-2"
	configured.BatchSize = 3
	if _, err := h.store.Operator(ctx, OperatorInput{
		Kind: KindPolicy, Operator: "alex", Policy: &configured,
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	read, err := h.store.Policy(ctx)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	if read.Version != "operator-2" || read.BatchSize != 3 {
		t.Fatalf("policy = %+v", read)
	}
}

// Blinding is recorded rather than assumed, and this package cannot see what a
// worker was served - but it can see a record whose blinding claim
// contradicts the rest of itself, and a contradiction that survives the wire
// is a provenance claim every later reader trusts.
func TestBlindedProvenanceMustNotContradictTheRecord(t *testing.T) {
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	base := Record{
		ID: "evr_1", Kind: KindAssessment, Subject: proposalSubject(), AssignmentID: "asg_1",
		ActorKind: ActorRun, ActorID: testRun, CreatedAt: at,
		Assessment: &Assessment{Vote: VoteSupport},
	}
	for _, tc := range []struct {
		name   string
		mutate func(r *Record)
	}{
		{
			name: "blinded while consulting a prior evaluation",
			mutate: func(r *Record) {
				r.Provenance = Provenance{Blinded: true,
					Consulted: []Subject{{Kind: "evaluation", ID: "evr_earlier"}}}
			},
		},
		{
			name: "a correction that claims a blinded read",
			mutate: func(r *Record) {
				r.SupersedesID = "evr_0"
				r.Provenance = Provenance{Blinded: true}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			tc.mutate(&record)
			bytes, err := json.Marshal(envelope{Schema: RecordSchema, Record: record})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := Decode(bytes); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	// The one prior evaluation a blinded read may consult is the one it is
	// reviewing: §4.12's bounded meta-review is a review of an evaluation's
	// own wording, and reading the record under review is not a leak of the
	// tally the read was blinded to.
	meta := base
	meta.Subject = Subject{Kind: "evaluation", ID: "evr_earlier"}
	meta.Provenance = Provenance{Blinded: true, Consulted: []Subject{meta.Subject}}
	encoded, err := encodeRecord(meta)
	if err != nil {
		t.Fatalf("encode a bounded meta-review: %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("a bounded meta-review was refused: %v", err)
	}
	if !decoded.Provenance.Blinded || len(decoded.Provenance.Consulted) != 1 {
		t.Fatalf("the blinding claim did not survive the wire: %+v", decoded.Provenance)
	}
}
