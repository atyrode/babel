package run

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// relatedSelection is one minimal valid selection, so these tests vary only
// the #87 context.
func relatedSelection() []Selected {
	return []Selected{{
		Host:          "workstation",
		Harness:       "omp",
		SourceID:      "session-a",
		CaptureDigest: digest.Bytes([]byte("capture")),
		SourceDigest:  digest.Bytes([]byte("source")),
		Adapter:       AdapterRef{Schema: 1, Version: "1.0.0"},
	}}
}

// TestEmptyContextLeavesThePreparationIdentityUnchanged is the compatibility
// property the derivation's conditional suffix exists for. Preparations are
// durable, pending remote sync, and named on the command line by an operator
// resuming a scope; a preparation recorded before #87 has to keep verifying
// against its own stored id, which it only does if an empty context hashes to
// exactly the bytes a pre-#87 Babel wrote.
//
// The expected value is computed here from the pre-#87 encoding rather than
// from the function under test. A test that called derive to learn what derive
// should produce would pass whatever the derivation did, which is the one thing
// this test must not do.
func TestEmptyContextLeavesThePreparationIdentityUnchanged(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	selection := relatedSelection()
	p, err := NewPreparation(at, selection, PreparationContext{})
	if err != nil {
		t.Fatalf("NewPreparation: %v", err)
	}
	if want := legacyPreparationID(at, selection); string(p.ID) != want {
		t.Errorf("empty-context id = %s, want the pre-#87 derivation %s", p.ID, want)
	}
	if err := p.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// legacyPreparationID restates the derivation as it stood before #87 added the
// related outputs and the serendipity marker: domain, schema, time, then the
// selection, and nothing after it.
func legacyPreparationID(preparedAt time.Time, selection []Selected) string {
	h := sha256.New()
	u32 := func(v uint32) {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], v)
		h.Write(b[:])
	}
	lp := func(s string) {
		u32(uint32(len(s)))
		h.Write([]byte(s))
	}
	h.Write([]byte("babel/preparation/v1"))
	u32(uint32(PreparationSchema))
	lp(preparedAt.UTC().Format(time.RFC3339Nano))
	u32(uint32(len(selection)))
	for _, s := range selection {
		lp(s.Host)
		lp(s.Harness)
		lp(s.SourceID)
		lp(s.Snapshot)
		lp(string(s.CaptureDigest))
		lp(string(s.SourceDigest))
		u32(uint32(s.Adapter.Schema))
		lp(s.Adapter.Version)
		u32(uint32(len(s.Adapter.Completeness)))
		for _, r := range s.Adapter.Completeness {
			lp(r.Field)
			lp(r.Reason)
		}
	}
	return "prep-" + hex.EncodeToString(h.Sum(nil))
}

// TestRelatedContextIsCanonicalAndPartOfTheIdentity pins the three things a
// stored injection has to be: canonically ordered so a retrieval's rank cannot
// leak into the record, deduplicated, and part of the content the id is derived
// from — because a preparation is the record of an act, and two acts that found
// different prior work are two different statements of scope.
func TestRelatedContextIsCanonicalAndPartOfTheIdentity(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	bare, err := NewPreparation(at, relatedSelection(), PreparationContext{})
	if err != nil {
		t.Fatalf("NewPreparation: %v", err)
	}

	// Offered in retrieval order, with one repeat.
	withContext, err := NewPreparation(at, relatedSelection(), PreparationContext{
		Related: []RelatedOutput{
			{Kind: "hypothesis", ID: "hyp-2"},
			{Kind: "finding", ID: "fnd-1"},
			{Kind: "hypothesis", ID: "hyp-1"},
			{Kind: "hypothesis", ID: "hyp-2"},
		},
	})
	if err != nil {
		t.Fatalf("NewPreparation with context: %v", err)
	}
	want := []RelatedOutput{
		{Kind: "finding", ID: "fnd-1"},
		{Kind: "hypothesis", ID: "hyp-1"},
		{Kind: "hypothesis", ID: "hyp-2"},
	}
	if len(withContext.Related) != len(want) {
		t.Fatalf("related = %+v, want %+v", withContext.Related, want)
	}
	for i, r := range withContext.Related {
		if r != want[i] {
			t.Errorf("related[%d] = %+v, want %+v", i, r, want[i])
		}
	}
	if withContext.ID == bare.ID {
		t.Error("naming prior outputs did not change the preparation identity")
	}
	if err := withContext.Verify(); err != nil {
		t.Errorf("Verify: %v", err)
	}

	// The same records in a different order are the same statement of scope,
	// so they are the same preparation.
	reordered, err := NewPreparation(at, relatedSelection(), PreparationContext{
		Related: []RelatedOutput{
			{Kind: "hypothesis", ID: "hyp-1"},
			{Kind: "hypothesis", ID: "hyp-2"},
			{Kind: "finding", ID: "fnd-1"},
		},
	})
	if err != nil {
		t.Fatalf("NewPreparation reordered: %v", err)
	}
	if reordered.ID != withContext.ID {
		t.Error("the same prior outputs in another order derived a different id")
	}

	// The serendipity marker is content too: the same records mean something
	// different when the scope was drawn rather than assembled.
	serendipitous, err := NewPreparation(at, relatedSelection(), PreparationContext{
		Related:       want,
		Serendipitous: true,
	})
	if err != nil {
		t.Fatalf("NewPreparation serendipitous: %v", err)
	}
	if serendipitous.ID == withContext.ID {
		t.Error("the serendipity marker did not change the preparation identity")
	}

	// A marker with no related records still changes the identity, because
	// the encoding has to distinguish "nothing was asked of the frontier"
	// from "the frontier was asked and had nothing to say about a draw".
	markerOnly, err := NewPreparation(at, relatedSelection(), PreparationContext{Serendipitous: true})
	if err != nil {
		t.Fatalf("NewPreparation marker only: %v", err)
	}
	if markerOnly.ID == bare.ID {
		t.Error("a serendipity marker alone derived the empty-context id")
	}
}

// TestRelatedContextIsBoundedAndResolvable covers the two refusals. A reference
// naming nothing could not be resolved by the run that received it, and a list
// past the ceiling is a run reading Babel's back catalogue instead of the
// corpus.
func TestRelatedContextIsBoundedAndResolvable(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	if _, err := NewPreparation(at, relatedSelection(), PreparationContext{
		Related: []RelatedOutput{{Kind: "hypothesis"}},
	}); err == nil {
		t.Error("a related output with no id was accepted")
	}
	if _, err := NewPreparation(at, relatedSelection(), PreparationContext{
		Related: []RelatedOutput{{ID: "hyp-1"}},
	}); err == nil {
		t.Error("a related output with no kind was accepted")
	}

	tooMany := make([]RelatedOutput, 0, MaxRelatedOutputs+1)
	for i := range MaxRelatedOutputs + 1 {
		tooMany = append(tooMany, RelatedOutput{Kind: "hypothesis", ID: "hyp-" + strings.Repeat("x", i+1)})
	}
	if _, err := NewPreparation(at, relatedSelection(), PreparationContext{Related: tooMany}); err == nil {
		t.Errorf("%d related outputs was accepted past the ceiling of %d",
			len(tooMany), MaxRelatedOutputs)
	}
	// Exactly the ceiling is fine, and the duplicate that would push a list
	// over it is collapsed before the bound is checked rather than counted
	// against it.
	atCeiling := append(tooMany[:MaxRelatedOutputs:MaxRelatedOutputs], tooMany[0])
	if _, err := NewPreparation(at, relatedSelection(), PreparationContext{Related: atCeiling}); err != nil {
		t.Errorf("a list of %d with one repeat was refused: %v", len(atCeiling), err)
	}
}

// TestFrontierSelfRetrievalIsReceipted is the receipt half of #87's on-demand
// search: a frontier retrieval is recorded like a corpus one, and it records
// the record identifiers it disclosed rather than evidence, because a frontier
// record is addressed by id and has no locator to cite.
func TestFrontierSelfRetrievalIsReceipted(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	body := Body{
		Retrieval: []RetrievalStep{{
			Index:   1,
			Tool:    "corpus-search",
			Scope:   "frontier",
			Query:   "release pipeline",
			At:      at,
			Records: []string{"hyp-1", "fnd-1"},
		}},
		Resources: Resources{},
	}
	if err := validateTrace(body); err != nil {
		t.Fatalf("a frontier step with no hits was refused: %v", err)
	}
	body.Retrieval[0].Records = []string{""}
	if err := validateTrace(body); err == nil {
		t.Error("a disclosed record with no identity was accepted")
	}
}
