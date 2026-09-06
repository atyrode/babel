package frontier

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	babelsync "github.com/atyrode/babel/internal/sync"
)

func TestRestageLocalFrontierPreservesCanonicalPublications(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	h := mustHypothesis(t, s, "run-local", "the cache races deployment")
	other := mustHypothesis(t, s, "run-local", "the cache serializes deployment")
	o, err := s.CreateObservation(ctx, ObservationInput{
		HypothesisID: h.ID, RunID: "run-local", RecipeID: "recipe-a",
		Actor: Actor{Kind: ActorRun, ID: "run-local"},
		Payload: ObservationPayload{Claim: "two writes overlap", Confidence: ConfidenceModerate,
			Impact: ImpactModerate, Evidence: []Evidence{mustEvidence(t, 12, "overlapping writes")}, CounterEvidenceAbsent: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.CreateFinding(ctx, FindingInput{
		RunID: "run-local", ObservationIDs: []string{o.ID}, Actor: Actor{Kind: ActorRun, ID: "run-local"},
		Payload: FindingPayload{Title: "cache race", Pattern: "writes overlap", CounterEvidenceAbsent: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProposal(ctx, ProposalInput{
		RunID: "run-local", FindingIDs: []string{f.ID}, Actor: Actor{Kind: ActorRun, ID: "run-local"},
		Payload: ProposalPayload{Title: "serialize writes", Problem: "writes overlap", Outcome: "ordered writes",
			Impact: ImpactModerate, Classification: ClassificationPrivate},
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.Link(ctx, LinkInput{FromID: h.ID, ToID: other.ID, Type: LinkContradicts, Note: "conflicting claims"})
	if err != nil {
		t.Fatal(err)
	}
	rejection, request, err := s.RejectAndRefine(ctx, DispositionInput{
		Subject: Ref{Type: EntityHypothesis, ID: h.ID}, ReviewerID: "operator-a", Note: "narrow the claim",
	}, RefinementPayload{Guidance: "identify the competing writers"})
	if err != nil {
		t.Fatal(err)
	}
	// Read source bytes, not decoded payloads: canonical encoders must receive
	// exactly what the local immutable tables retained.
	raw := func(table, id string) []byte {
		t.Helper()
		var payload []byte
		if err := s.db.QueryRowContext(ctx, `SELECT payload_json FROM `+table+` WHERE id = ?`, id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	var expected []babelsync.Record
	add := func(rec babelsync.Record, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, rec)
	}
	add(stagedHypothesis(h, h.ID, raw("frontier_hypothesis", h.ID)))
	add(stagedHypothesis(other, other.ID, raw("frontier_hypothesis", other.ID)))
	add(stagedObservation(o, o.ID, raw("frontier_observation", o.ID)))
	add(stagedFinding(f, f.ID, raw("frontier_finding", f.ID)))
	add(stagedProposal(p, p.ID, raw("frontier_proposal", p.ID)))
	add(stagedLink(link, raw("frontier_hypothesis_link", link.ID)))
	add(stagedDisposition(rejection, raw("frontier_disposition", rejection.ID)))
	add(stagedRefinement(request, rejection.ReviewerID, raw("frontier_refinement_request", request.ID)))
	// The latest status is a mutable projection, not the status originally
	// published with the hypothesis.
	if _, err := s.DeferFrontier(ctx, "run-local", []string{h.ID}, "later triage"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restage(ctx); err == nil {
		t.Fatal("explicit restage without a hook succeeded")
	}
	if err := babelsync.EnsureSchema(s.db); err != nil {
		t.Fatal(err)
	}
	s.sync = babelsync.NewStager()
	if n, err := s.Restage(ctx); err != nil || n != len(expected) {
		t.Fatalf("restage = %d, %v; want %d", n, err, len(expected))
	}
	for _, want := range expected {
		var payload []byte
		var runID string
		if err := s.db.QueryRowContext(ctx, `SELECT p.payload, r.run_id FROM sync_payload p
			JOIN sync_record r ON r.record_id = p.record_id WHERE p.record_id = ?`, want.EntityID).Scan(&payload, &runID); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, want.Payload) {
			t.Errorf("%s canonical payload changed\ngot %s\nwant %s", want.EntityID, payload, want.Payload)
		}
		wantRun := "run-local"
		if want.EntityID == link.ID {
			wantRun = link.ID
		} else if want.EntityID == rejection.ID || want.EntityID == request.ID {
			wantRun = rejection.ID
		}
		if runID != wantRun {
			t.Errorf("%s run = %q, want %q", want.EntityID, runID, wantRun)
		}
	}
	var subjectKind, subjectID string
	if err := s.db.QueryRowContext(ctx, `SELECT subject_kind, subject_id FROM sync_record_subject WHERE record_id = ?`, p.ID).Scan(&subjectKind, &subjectID); err != nil {
		t.Fatal(err)
	}
	if subjectKind != string(EntityFinding) || subjectID != f.ID {
		t.Fatalf("recovered proposal subject = %s %s", subjectKind, subjectID)
	}
	var recovered []byte
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM sync_payload WHERE record_id = ?`, link.ID).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	got, err := DecodePublishedRecord(recovered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Edge, &PublishedEdge{FromID: link.FromID, ToID: link.ToID, Type: link.Type}) {
		t.Fatalf("recovered link endpoints = %+v", got.Edge)
	}
	if n, err := s.Restage(ctx); err != nil || n != 0 {
		t.Fatalf("second restage = %d, %v; want 0", n, err)
	}
}
