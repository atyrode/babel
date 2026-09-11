package web

import (
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// TestTriageAdviceReachesThePageTheOperatorDecidesOn is the visibility half of
// the authority line.
//
// Advice that a person has to go and look for is advice that arrives after the
// decision it was written for, so the property is not that the record exists
// but that it is on the document the review page loads for a proposal — and
// that it is on the alternative's document too, because from there the advice
// is the only account of where that second record came from.
func TestTriageAdviceReachesThePageTheOperatorDecidesOn(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	advice, alternative, err := h.front.Triage().AdviseWithAlternative(h.ctx,
		frontier.TriageInput{
			ProposalID: h.proposal.ID,
			RunID:      "run-triage",
			Payload: frontier.TriageAdvicePayload{
				Rank: 1, Cohort: 2,
				Ranking:         "it names the smaller change",
				CounterArgument: "one session is thin evidence for a convention",
			},
		},
		frontier.ProposalPayload{
			Title:          "state the one constraint that was dropped",
			Problem:        "the broader wording touches every handoff",
			Outcome:        "a narrower change with the same support",
			Impact:         frontier.ImpactModerate,
			Classification: frontier.ClassificationPrivate,
		},
	)
	if err != nil {
		t.Fatalf("advise with an alternative: %v", err)
	}

	var detail proposalDetail
	decodeResponse(t, h.get("/api/proposals/"+h.proposal.ID), &detail)
	if len(detail.Triage) != 1 {
		t.Fatalf("proposal detail carries %d pieces of advice, want 1", len(detail.Triage))
	}
	shown := detail.Triage[0]
	if shown.CounterArgument != "one session is thin evidence for a convention" {
		t.Errorf("counter-argument = %q, want the pass's own words", shown.CounterArgument)
	}
	if shown.Rank != 1 || shown.Cohort != 2 {
		t.Errorf("rank = %d of %d, want 1 of 2", shown.Rank, shown.Cohort)
	}
	if shown.AlternativeID != alternative.ID {
		t.Errorf("advice names alternative %q, want %q", shown.AlternativeID, alternative.ID)
	}
	// The record it is about is still waiting. A page that showed advice
	// beside a ruling would be presenting a settled question as open, and
	// the review status is where that would show first.
	if detail.ReviewStatus != string(frontier.ReviewNew) {
		t.Errorf("advised proposal review status = %q, want %q", detail.ReviewStatus, frontier.ReviewNew)
	}

	var offered proposalDetail
	decodeResponse(t, h.get("/api/proposals/"+alternative.ID), &offered)
	if len(offered.Triage) != 1 || offered.Triage[0].ID != advice.ID {
		t.Fatalf("the alternative's page carries %+v, want the advice that offered it", offered.Triage)
	}
	if offered.Triage[0].ProposalID != h.proposal.ID {
		t.Errorf("the alternative's advice names %q as its subject, want %q",
			offered.Triage[0].ProposalID, h.proposal.ID)
	}

	// An untriaged proposal renders as one: the field is absent rather than
	// present and empty, so a client cannot show an advice block for a
	// record nothing was said about.
	plain, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-3",
		FindingIDs: []string{h.finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          "a proposal no pass has read",
			Problem:        "nothing was said about it",
			Outcome:        "it renders without an advice block",
			Impact:         frontier.ImpactLow,
			Classification: frontier.ClassificationPrivate,
		},
	})
	if err != nil {
		t.Fatalf("create an untriaged proposal: %v", err)
	}
	var untriaged proposalDetail
	decodeResponse(t, h.get("/api/proposals/"+plain.ID), &untriaged)
	if len(untriaged.Triage) != 0 {
		t.Errorf("an untriaged proposal carries %+v, want no advice", untriaged.Triage)
	}
}

// TestTheProposalsListingMarksAdviceWithoutReRankingTheQueue is the other half
// of surfacing advice: a reader scanning the pile can see which rows have been
// read, and the pile is still in the order it was in.
//
// The two properties are one test because they are in tension. Advice is only
// useful in a listing if it is visible there, and the moment a listing knows a
// rank it can sort on one — at which point Babel has chosen what the operator
// reads first rather than suggested it. So the row carries presence and no
// number, and the order is asserted against the store's own.
func TestTheProposalsListingMarksAdviceWithoutReRankingTheQueue(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	_, alternative, err := h.front.Triage().AdviseWithAlternative(h.ctx,
		frontier.TriageInput{
			ProposalID: h.proposal.ID,
			RunID:      "run-triage",
			Payload: frontier.TriageAdvicePayload{
				Rank: 2, Cohort: 2,
				CounterArgument: "the outcome restates the problem",
			},
		},
		frontier.ProposalPayload{
			Title:          "the smaller change that would settle it",
			Problem:        "the broader wording touches every handoff",
			Outcome:        "one constraint, stated once",
			Impact:         frontier.ImpactLow,
			Classification: frontier.ClassificationPrivate,
		},
	)
	if err != nil {
		t.Fatalf("advise with an alternative: %v", err)
	}
	plain, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-3",
		FindingIDs: []string{h.finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          "a proposal no pass has read",
			Problem:        "nothing was said about it",
			Outcome:        "its row carries no mark",
			Impact:         frontier.ImpactLow,
			Classification: frontier.ClassificationPrivate,
		},
	})
	if err != nil {
		t.Fatalf("create an untriaged proposal: %v", err)
	}

	// Decoded loosely on purpose. The question is what is on the wire: an
	// absent key and a false one render identically through a Go struct,
	// and "no advice" must not arrive as a field at all.
	//
	// ?fleet=0 because the merged listing appends other hosts' rows after
	// this machine's, and advice about those records is held by the host
	// that wrote it. The order this test is about is the local queue's.
	var wire struct {
		Items []map[string]any `json:"items"`
	}
	decodeResponse(t, h.get("/api/proposals?fleet=0"), &wire)
	marks := make(map[string]any, len(wire.Items))
	order := make([]string, 0, len(wire.Items))
	for _, item := range wire.Items {
		id, _ := item["id"].(string)
		order = append(order, id)
		if advised, ok := item["advised"]; ok {
			marks[id] = advised
		}
		if _, ok := item["rank"]; ok {
			t.Errorf("row %s serves a triage rank, which is the field a listing would sort on", id)
		}
	}
	if marks[h.proposal.ID] != true {
		t.Errorf("the advised proposal's row is marked %v, want true", marks[h.proposal.ID])
	}
	// The alternative is marked too, because its page shows the advice that
	// offered it. A mark that disagreed with the page it opens onto is a
	// mark a reader learns to ignore.
	if marks[alternative.ID] != true {
		t.Errorf("the alternative's row is marked %v, want true", marks[alternative.ID])
	}
	if _, present := marks[plain.ID]; present {
		t.Errorf("the untriaged proposal's row carries an advised field, want none")
	}

	stored, _, err := h.front.Proposals(h.ctx, frontier.ListFilter{})
	if err != nil {
		t.Fatalf("read proposals: %v", err)
	}
	want := make([]string, 0, len(stored))
	for _, record := range stored {
		want = append(want, record.ID)
	}
	if len(order) != len(want) {
		t.Fatalf("listing served %d rows, want the store's %d", len(order), len(want))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("listing row %d is %s, want %s: advice has reordered the queue",
				i, order[i], want[i])
		}
	}

	// And the mark is only a mark: the argument itself is still a page away,
	// so nothing a listing renders can read as a ruling on a row.
	raw := body(t, h.get("/api/proposals?fleet=0"))
	for _, field := range []string{"counter_argument", "cluster", "ranking"} {
		if strings.Contains(raw, field) {
			t.Errorf("the proposals listing ships %q; the argument belongs on the record's own page", field)
		}
	}

	// A row another host committed carries no mark. This machine does not
	// hold that host's advice, and a blank where an absence belongs is the
	// same answer the merged listing already gives for a review status.
	var merged struct {
		Items []map[string]any `json:"items"`
	}
	decodeResponse(t, h.get("/api/proposals"), &merged)
	marked := 0
	for _, item := range merged.Items {
		if item["advised"] == true {
			marked++
		}
	}
	if marked != 2 {
		t.Errorf("the merged listing marks %d rows, want the 2 this machine advised", marked)
	}
}
