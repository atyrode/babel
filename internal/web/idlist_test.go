package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// A record's empty id list must reach the client as an array, never as null.
//
// A candidate proposal answers no finding, so FindingIDs is nil on a perfectly
// ordinary record. Marshalled as null it is not something a reader can count,
// and the page that counted it threw during render: React unmounted the whole
// document, so the operator saw an empty window while the server logged the
// record's own fetch returning 200. The fault was invisible from both ends.
//
// This pins the wire form rather than the page, because every consumer of
// these fields depends on it and only one of them was fixed by hand.
func TestEmptyIDListsMarshalAsArrays(t *testing.T) {
	candidate := frontier.Proposal{
		ID:            "pro_candidate",
		RunID:         "run_1",
		Form:          frontier.ProposalCandidate,
		HypothesisIDs: []string{"hyp_1"},
		// FindingIDs is deliberately absent: that is what a candidate is.
	}

	encoded, err := json.Marshal(viewProposal(candidate))
	if err != nil {
		t.Fatalf("marshal proposal view: %v", err)
	}

	var wire struct {
		FindingIDs    []string `json:"finding_ids"`
		HypothesisIDs []string `json:"hypothesis_ids"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal proposal view: %v", err)
	}
	if wire.FindingIDs == nil {
		t.Errorf("finding_ids marshalled as null, which no reader can count: %s", encoded)
	}
	if len(wire.HypothesisIDs) != 1 {
		t.Errorf("hypothesis_ids lost its entry: %s", encoded)
	}
	if strings.Contains(string(encoded), `"finding_ids":null`) {
		t.Errorf("finding_ids is literally null on the wire: %s", encoded)
	}
}
