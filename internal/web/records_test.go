package web

// Issue #87's record actions over HTTP, against the real stores.
//
// Four properties are worth a test each, and each of them is a claim the issue
// makes rather than a code path this file happens to have.
//
// A history is whole and attributable. A chain reads the same from anywhere in
// it, and every entry says who wrote it and why.
//
// A decision is about the wording the operator read. Every mutation confirms
// the chain head it was rendered against, and a stale one is refused with an
// explanation and writes nothing.
//
// Accepting authorizes and publishes nothing. What the store holds afterwards
// is an attributed ledger entry, and the response says in as many words that
// nothing left Babel.
//
// An invitation carries no instruction. The route has no field one could be
// written into, and a request that invents one is refused rather than accepted
// with the field dropped.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
)

// TestRecordRevisionsRenderTheWholeChain reads the chain from its superseded
// original, which is the identifier an operator following an old link holds.
// The answer is the whole chain either way: a history you need to already know
// the newest entry of in order to read is not a history.
func TestRecordRevisionsRenderTheWholeChain(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, from := range []string{h.original.ID, h.revised.ID} {
		var got revisionChain
		decodeResponse(t, h.get("/api/record/revisions?type=hypothesis&id="+from), &got)
		if got.HeadID != h.revised.ID {
			t.Fatalf("from %s: head = %q, want %q", from, got.HeadID, h.revised.ID)
		}
		if len(got.Revisions) != 2 {
			t.Fatalf("from %s: chain = %+v", from, got.Revisions)
		}
		first, second := got.Revisions[0], got.Revisions[1]
		// The original is a run's, carries no reason, and is not the head.
		if first.Record.ID != h.original.ID || first.Sequence != 1 || first.Head ||
			first.Actor.Kind != string(frontier.ActorRun) || first.Actor.ID != "run-1" || first.Reason != "" {
			t.Errorf("original revision = %+v", first)
		}
		// The revision is the operator's, states why, and links to what it
		// superseded.
		if second.Record.ID != h.revised.ID || second.Sequence != 2 || !second.Head ||
			second.Actor.Kind != string(frontier.ActorOperator) || second.Actor.ID != operatorID ||
			second.SupersedesID != h.original.ID || !strings.Contains(second.Reason, "conflated two deploy steps") {
			t.Errorf("operator revision = %+v", second)
		}
		if first.RootID != h.original.ID || second.RootID != h.original.ID {
			t.Errorf("chain roots disagree: %+v", got.Revisions)
		}
	}
}

// TestRecordDispositionsListActionsAndInvitations is the record page's read: the
// proposed actions attached to one record, each with its ledger, and the
// invitations recorded against it, all answered relative to the chain head the
// page's own mutations will confirm.
func TestRecordDispositionsListActionsAndInvitations(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	var got recordDispositions
	decodeResponse(t, h.get("/api/record/dispositions?type=hypothesis&id="+h.hypothesis.ID), &got)
	if got.HeadID != h.hypothesis.ID {
		t.Fatalf("head = %q, want the unrevised record %q", got.HeadID, h.hypothesis.ID)
	}
	if len(got.Dispositions) != 1 {
		t.Fatalf("dispositions = %+v", got.Dispositions)
	}
	action := got.Dispositions[0]
	if action.ID != h.action.ID || action.Kind != string(disposition.KindDevelopFurther) ||
		action.Status != string(disposition.StatusProposed) || len(action.Ledger) != 0 {
		t.Errorf("proposed action = %+v", action)
	}
	if action.ProposedBy.Kind != string(frontier.ActorRun) || action.ProposedBy.ID != "run-1" {
		t.Errorf("action author = %+v", action.ProposedBy)
	}
	if !strings.Contains(action.Summary, "read the two adjacent sessions") || action.Rationale == "" {
		t.Errorf("action payload = %+v", action)
	}
	// A develop-further binds to no repository and renders no draft: the
	// draft is the one thing a draft-issue has that the others must not.
	if action.Anchor != nil || action.Draft != "" {
		t.Errorf("a develop-further action carries a repository or a draft: %+v", action)
	}
	if len(got.Invitations) != 1 {
		t.Fatalf("invitations = %+v", got.Invitations)
	}
	invitation := got.Invitations[0]
	if invitation.ID != h.invitation.ID || !invitation.Open || invitation.ConsumedBy != "" ||
		invitation.By != operatorID {
		t.Errorf("invitation = %+v", invitation)
	}
}

// TestADraftIssueRendersAsTextAndFilesNothing checks the one action kind whose
// acceptance a reader is most likely to mistake for publication. The draft
// arrives as text in the response, the repository it binds to arrives with it,
// and accepting it says that nothing was published.
func TestADraftIssueRendersAsTextAndFilesNothing(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	record := frontier.Ref{Type: frontier.EntityHypothesis, ID: h.hypothesis.ID}
	drafted, err := h.actions.Propose(h.ctx, disposition.ProposeInput{
		Record:     record,
		Kind:       disposition.KindDraftIssue,
		ProposedBy: frontier.Run("run-1"),
		Ref:        "action-draft",
		Payload: disposition.Payload{
			Summary:   "re-read the manifest per deploy step",
			Rationale: "three sessions show the same stale read",
			Anchor: &disposition.Anchor{
				Workspace: "/synthetic/checkout",
				Remote:    "origin",
				URL:       "git@github.com:atyrode/babel",
				Branch:    "main",
			},
		},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	var listed recordDispositions
	decodeResponse(t, h.get("/api/record/dispositions?type=hypothesis&id="+h.hypothesis.ID), &listed)
	var draft proposedActionView
	for _, action := range listed.Dispositions {
		if action.ID == drafted.ID {
			draft = action
		}
	}
	if draft.ID == "" {
		t.Fatalf("the draft-issue action was not listed: %+v", listed.Dispositions)
	}
	if draft.Anchor == nil || draft.Anchor.URL != "git@github.com:atyrode/babel" || draft.Anchor.Remote != "origin" {
		t.Fatalf("draft anchor = %+v", draft.Anchor)
	}
	// The rendering is internal/disposition's own, so the sentence that says
	// nothing was published travels with the text an operator would paste.
	for _, want := range []string{
		"re-read the manifest per deploy step",
		"three sessions show the same stale read",
		"published nothing",
	} {
		if !strings.Contains(draft.Draft, want) {
			t.Errorf("the draft omits %q:\n%s", want, draft.Draft)
		}
	}

	var decided decideActionResult
	decodeResponse(t, h.post("/api/record/disposition/decide",
		`{"dispositionId":"`+drafted.ID+`","ruling":"accepted","headId":"`+h.hypothesis.ID+`"}`), &decided)
	if decided.Status != string(disposition.StatusAccepted) {
		t.Fatalf("status after accepting = %q", decided.Status)
	}
	if decided.Published != publishedNothing || !strings.Contains(decided.Published, "nothing") {
		t.Errorf("acceptance published = %q", decided.Published)
	}
}

// TestDecidingAProposedActionAppendsAnAttributedRuling drives an accept and
// then a reconsidering decline, and reads both back through the store.
//
// The second decision is the point: #87's ledger appends rather than updates, so
// a reconsidered answer is another entry and both stay readable in order. A
// surface that reported only the latest one would make the ledger look like a
// mutable field.
func TestDecidingAProposedActionAppendsAnAttributedRuling(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	head := `","headId":"` + h.hypothesis.ID + `"}`

	var accepted decideActionResult
	decodeResponse(t, h.post("/api/record/disposition/decide",
		`{"dispositionId":"`+h.action.ID+`","ruling":"accepted","note":"the adjacent sessions are worth reading`+head),
		&accepted)
	if accepted.Status != string(disposition.StatusAccepted) || accepted.Entry.Sequence != 1 ||
		accepted.Entry.Ruling != string(disposition.RulingAccepted) || accepted.Entry.By != operatorID {
		t.Fatalf("accepted = %+v", accepted)
	}

	var declined decideActionResult
	decodeResponse(t, h.post("/api/record/disposition/decide",
		`{"dispositionId":"`+h.action.ID+`","ruling":"declined","note":"the second session says the same thing`+head),
		&declined)
	if declined.Status != string(disposition.StatusDeclined) || declined.Entry.Sequence != 2 {
		t.Fatalf("declined = %+v", declined)
	}

	// Both entries are in the durable ledger, in order, attributed to the
	// session's operator.
	ledger, err := h.actions.Ledger(h.ctx, h.action.ID)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if len(ledger) != 2 {
		t.Fatalf("ledger = %+v", ledger)
	}
	if ledger[0].Ruling != disposition.RulingAccepted || ledger[1].Ruling != disposition.RulingDeclined {
		t.Errorf("ledger order = %+v", ledger)
	}
	for _, entry := range ledger {
		if entry.By != operatorID {
			t.Errorf("ledger entry is not attributed to the session's operator: %+v", entry)
		}
	}
	if !strings.Contains(ledger[0].Payload.Note, "worth reading") {
		t.Errorf("the operator's own words were lost: %+v", ledger[0])
	}

	// An unknown ruling is refused by the store rather than filtered by the
	// handler, so there is one place that decides what an answer may be.
	response := h.post("/api/record/disposition/decide",
		`{"dispositionId":"`+h.action.ID+`","ruling":"maybe`+head)
	text := body(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown ruling: status = %d body %q", response.StatusCode, text)
	}
}

// TestAnInvitationQueuesAndCarriesNoInstruction checks both halves of #87's
// nudge: the queued state a page shows, and the absence of any field an
// instruction could travel in.
func TestAnInvitationQueuesAndCarriesNoInstruction(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	body_ := `{"record":{"type":"finding","id":"` + h.finding.ID + `"},"headId":"` + h.finding.ID + `"}`
	var got inviteResult
	decodeResponse(t, h.post("/api/record/invite", body_), &got)
	if !got.Invitation.Open || got.Invitation.ConsumedBy != "" || got.Invitation.By != operatorID {
		t.Fatalf("invitation = %+v", got.Invitation)
	}
	if got.Invitation.Record.Type != string(frontier.EntityFinding) || got.Invitation.Record.ID != h.finding.ID {
		t.Errorf("invitation record = %+v", got.Invitation.Record)
	}
	if got.Instruction != invitationCarriesNoInstruction {
		t.Errorf("instruction = %q", got.Instruction)
	}

	// The queued state reads back from the listing, which is what the record
	// page renders after the click.
	var listed recordDispositions
	decodeResponse(t, h.get("/api/record/dispositions?type=finding&id="+h.finding.ID), &listed)
	if len(listed.Invitations) != 1 || listed.Invitations[0].ID != got.Invitation.ID || !listed.Invitations[0].Open {
		t.Fatalf("listed invitations = %+v", listed.Invitations)
	}

	// Asking twice is two invitations. The operator asked twice, and a
	// surface that deduplicated it would decide the second ask did not count.
	decodeResponse(t, h.post("/api/record/invite", body_), &got)
	decodeResponse(t, h.get("/api/record/dispositions?type=finding&id="+h.finding.ID), &listed)
	if len(listed.Invitations) != 2 {
		t.Fatalf("a second invitation was not recorded: %+v", listed.Invitations)
	}

	// A field the route does not accept is refused rather than ignored. This
	// is what keeps the instruction-free rule from being bypassed by a client
	// that sends prose and gets a 200 with the prose dropped.
	for _, smuggled := range []string{
		`{"record":{"type":"finding","id":"` + h.finding.ID + `"},"note":"reword it","headId":"` + h.finding.ID + `"}`,
		`{"record":{"type":"finding","id":"` + h.finding.ID + `"},"instruction":"reword it","headId":"` + h.finding.ID + `"}`,
	} {
		response := h.post("/api/record/invite", smuggled)
		text := body(t, response)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("smuggled instruction: status = %d body %q, want 400", response.StatusCode, text)
		}
	}
}

// TestRevivingRequiresAReasonARestingCandidateAndACandidate walks every refusal
// the transition has, then the transition itself.
//
// The reason is the load-bearing one. #87 makes every status a resting place
// rather than an ending, and that is only safe if leaving one leaves an argument
// behind: a rejected candidate quietly reappearing is indistinguishable from one
// that was never rejected.
func TestRevivingRequiresAReasonARestingCandidateAndACandidate(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, test := range []struct {
		name   string
		body   string
		status int
		says   string
	}{
		{
			name:   "no reason",
			body:   `{"record":{"type":"hypothesis","id":"` + h.resting.ID + `"},"headId":"` + h.resting.ID + `"}`,
			status: http.StatusBadRequest,
			says:   "reason is required",
		},
		{
			name: "empty reason",
			body: `{"record":{"type":"hypothesis","id":"` + h.resting.ID + `"},"reason":"",` +
				`"headId":"` + h.resting.ID + `"}`,
			status: http.StatusBadRequest,
			says:   "reason is required",
		},
		{
			name: "a finding has no lifecycle",
			body: `{"record":{"type":"finding","id":"` + h.finding.ID + `"},"reason":"worth another look",` +
				`"headId":"` + h.finding.ID + `"}`,
			status: http.StatusBadRequest,
			says:   "only a candidate",
		},
		{
			name: "a candidate already on the frontier is not at rest",
			body: `{"record":{"type":"hypothesis","id":"` + h.hypothesis.ID + `"},"reason":"worth another look",` +
				`"headId":"` + h.hypothesis.ID + `"}`,
			status: http.StatusConflict,
			says:   "not at rest",
		},
		{
			name: "reviving into a resting status would change nothing",
			body: `{"record":{"type":"hypothesis","id":"` + h.resting.ID + `"},"reason":"worth another look",` +
				`"status":"rejected","headId":"` + h.resting.ID + `"}`,
			status: http.StatusBadRequest,
			says:   "outside what",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := h.snapshot()
			response := h.post("/api/record/revive", test.body)
			text := body(t, response)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d body %q, want %d", response.StatusCode, text, test.status)
			}
			if !strings.Contains(text, test.says) {
				t.Errorf("refusal does not say why: %q", text)
			}
			if after := h.snapshot(); after != before {
				t.Fatalf("a refused revive wrote to the durable record:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}

	var got reviveResult
	decodeResponse(t, h.post("/api/record/revive",
		`{"record":{"type":"hypothesis","id":"`+h.resting.ID+`"},`+
			`"reason":"a second session shows the same first failure","headId":"`+h.resting.ID+`"}`), &got)
	if got.Record.ID != h.resting.ID || got.Event.Status != string(frontier.StatusQueued) {
		t.Fatalf("revive = %+v", got)
	}
	// The transition belongs to the operator and to no run: an operator's
	// revive borrows no run identity, and the surface renders both.
	if got.Event.Actor.Kind != string(frontier.ActorOperator) || got.Event.Actor.ID != operatorID ||
		got.Event.RunID != "" {
		t.Errorf("revive author = %+v", got.Event)
	}
	if !strings.Contains(got.Event.Note, "same first failure") {
		t.Errorf("the revive's argument was lost: %+v", got.Event)
	}

	// The argument is in the durable history beside the rejection it
	// followed, which is what makes the curation auditable.
	history, err := h.front.StatusHistory(h.ctx, h.resting.ID)
	if err != nil {
		t.Fatalf("StatusHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("status history = %+v", history)
	}
	last := history[len(history)-1]
	if last.Status != frontier.StatusQueued || last.Actor.Kind != frontier.ActorOperator ||
		last.Actor.ID != operatorID || !strings.Contains(last.Payload.Note, "same first failure") {
		t.Errorf("durable transition = %+v", last)
	}
}

// TestARevivedCandidateCanRestAndReviveAgain checks that "nothing closes"
// survives the second lap: a candidate revived once and rejected again is
// revivable again, and a surface that treated the first transition as terminal
// would reintroduce the ending #87 removed.
func TestARevivedCandidateCanRestAndReviveAgain(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	body_ := `{"record":{"type":"hypothesis","id":"` + h.resting.ID + `"},` +
		`"reason":"a second session shows the same first failure","headId":"` + h.resting.ID + `"}`
	var got reviveResult
	decodeResponse(t, h.post("/api/record/revive", body_), &got)
	if _, err := h.front.SetStatus(h.ctx, frontier.StatusInput{
		HypothesisID: h.resting.ID,
		Status:       frontier.StatusRejected,
		RunID:        "run-2",
		Note:         "the second session was the same session",
	}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	decodeResponse(t, h.post("/api/record/revive", body_), &got)
	if got.Event.Status != string(frontier.StatusQueued) || got.Event.Sequence != 5 {
		t.Fatalf("second revive = %+v", got.Event)
	}
}

// TestAStaleHeadRefusesEveryRecordAction is the confirmation contract.
//
// Each mutation is sent naming the wording that has since been superseded,
// which is exactly what a page rendered before a revision would send. The
// refusal is a 409, it names the current wording so the operator can go and
// read it, and nothing is written: an append-only ledger cannot take an entry
// back, so the check has to happen before the append rather than be corrected
// after it.
func TestAStaleHeadRefusesEveryRecordAction(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	// A proposed action and an invitation against the chain's original, so
	// each mutation has a stale target of its own. Both are legitimate
	// records: the action was proposed about the wording that was current
	// when it was proposed.
	stale, err := h.actions.Propose(h.ctx, disposition.ProposeInput{
		Record:     frontier.Ref{Type: frontier.EntityHypothesis, ID: h.original.ID},
		Kind:       disposition.KindAskQuestion,
		ProposedBy: frontier.Run("run-1"),
		Ref:        "action-stale",
		Payload:    disposition.Payload{Summary: "ask which deploy step the operator meant"},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if _, err := h.front.SetStatus(h.ctx, frontier.StatusInput{
		HypothesisID: h.original.ID,
		Status:       frontier.StatusDeferred,
		RunID:        "run-1",
		Note:         "superseded by a clearer wording",
	}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	before := h.snapshot()
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "decide",
			path: "/api/record/disposition/decide",
			body: `{"dispositionId":"` + stale.ID + `","ruling":"accepted","headId":"` + h.original.ID + `"}`,
		},
		{
			name: "invite",
			path: "/api/record/invite",
			body: `{"record":{"type":"hypothesis","id":"` + h.original.ID + `"},"headId":"` + h.original.ID + `"}`,
		},
		{
			name: "revive",
			path: "/api/record/revive",
			body: `{"record":{"type":"hypothesis","id":"` + h.original.ID + `"},` +
				`"reason":"worth another look","headId":"` + h.original.ID + `"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := h.post(test.path, test.body)
			text := body(t, response)
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
			}
			// The refusal explains itself and names where to look, because
			// "409" alone leaves an operator with a button that stopped
			// working and no way to find out why.
			for _, want := range []string{"revised after the page was rendered", h.revised.ID, "Reload"} {
				if !strings.Contains(text, want) {
					t.Errorf("refusal omits %q: %q", want, text)
				}
			}
		})
	}
	if after := h.snapshot(); after != before {
		t.Fatalf("a stale mutation wrote to the durable record:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestAMissingHeadRefusesEveryRecordAction pins the other half of the contract:
// the confirmation is required, not merely honoured when present. A client that
// could omit it would make the rule optional for every client.
func TestAMissingHeadRefusesEveryRecordAction(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	before := h.snapshot()
	for _, test := range []struct{ name, path, body string }{
		{"decide", "/api/record/disposition/decide", `{"dispositionId":"` + h.action.ID + `","ruling":"accepted"}`},
		{"invite", "/api/record/invite", `{"record":{"type":"hypothesis","id":"` + h.hypothesis.ID + `"}}`},
		{
			"revive", "/api/record/revive",
			`{"record":{"type":"hypothesis","id":"` + h.resting.ID + `"},"reason":"worth another look"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := h.post(test.path, test.body)
			text := body(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body %q, want 400", response.StatusCode, text)
			}
			if !strings.Contains(text, "headId") {
				t.Errorf("refusal does not name what is missing: %q", text)
			}
		})
	}
	if after := h.snapshot(); after != before {
		t.Fatalf("an unconfirmed mutation wrote to the durable record:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestRecordActionsRefuseAnUnknownRecord checks that a reference nobody can
// resolve is a 404 from the service rather than a 500 from a handler, and that
// the reads and the writes agree about it.
func TestRecordActionsRefuseAnUnknownRecord(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"revisions", http.MethodGet, "/api/record/revisions?type=hypothesis&id=hyp_missing", ""},
		{"dispositions", http.MethodGet, "/api/record/dispositions?type=hypothesis&id=hyp_missing", ""},
		{
			"invite", http.MethodPost, "/api/record/invite",
			`{"record":{"type":"hypothesis","id":"hyp_missing"},"headId":"hyp_missing"}`,
		},
		{
			"revive", http.MethodPost, "/api/record/revive",
			`{"record":{"type":"hypothesis","id":"hyp_missing"},"reason":"x","headId":"hyp_missing"}`,
		},
		{
			"decide", http.MethodPost, "/api/record/disposition/decide",
			`{"dispositionId":"dsp_missing","ruling":"accepted","headId":"hyp_missing"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response *http.Response
			if test.method == http.MethodGet {
				response = h.get(test.path)
			} else {
				response = h.post(test.path, test.body)
			}
			text := body(t, response)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d body %q, want 404", response.StatusCode, text)
			}
		})
	}
	// A record kind the frontier does not mint is a bad request rather than a
	// missing record: the reference is malformed, not unresolvable.
	response := h.get("/api/record/revisions?type=nonsense&id=hyp-1")
	if text := body(t, response); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind: status = %d body %q", response.StatusCode, text)
	}
}

// TestTheOverviewCountsPendingProposedActions is the dashboard's half: the
// review panel says how many proposed actions are waiting, and the count moves
// only when an operator answers one.
func TestTheOverviewCountsPendingProposedActions(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	var got overview
	decodeResponse(t, h.get("/api/overview"), &got)
	if !got.Review.Dispositions.Available || got.Review.Dispositions.Pending != 1 {
		t.Fatalf("dispositions section = %+v", got.Review.Dispositions)
	}

	decodeResponse(t, h.post("/api/record/disposition/decide",
		`{"dispositionId":"`+h.action.ID+`","ruling":"declined","headId":"`+h.hypothesis.ID+`"}`),
		&decideActionResult{})
	decodeResponse(t, h.get("/api/overview"), &got)
	if got.Review.Dispositions.Pending != 0 {
		t.Errorf("an answered action is still pending: %+v", got.Review.Dispositions)
	}

	// A build with no disposition ledger says so rather than reporting zero:
	// "nothing is waiting" and "this session cannot tell you" are different
	// facts, and a panel that showed a zero for the second would be claiming
	// an observation it never made.
	unwired := newPhaseB(t, "plain", func(opts *Options) { opts.Dispositions = nil })
	decodeResponse(t, unwired.get("/api/overview"), &got)
	if got.Review.Dispositions.Available || got.Review.Dispositions.Unavailable == "" ||
		got.Review.Dispositions.Pending != 0 {
		t.Errorf("unwired dispositions section = %+v", got.Review.Dispositions)
	}
}

// TestTheRunsSectionCarriesTheReceiptsAuthority renders why a run happened. A
// receipt recorded before authority existed carries none, and the surface passes
// the absence through rather than filling it in: the operator's page says
// "recorded before authority", which is only honest if the empty value reaches
// it as empty.
func TestTheRunsSectionCarriesTheReceiptsAuthority(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) {
		opts.Runs = runLister{
			{ReceiptID: "rcp-policy", RunID: "run-1", Sync: "committed",
				Authority: RunAuthority{Kind: "policy", Ref: "nightly-frontier"}},
			{ReceiptID: "rcp-old", RunID: "run-0", Sync: "committed"},
		}
	})
	var got overview
	decodeResponse(t, h.get("/api/overview"), &got)
	if len(got.Runs.Rows) != 2 {
		t.Fatalf("runs = %+v", got.Runs.Rows)
	}
	if got.Runs.Rows[0].Authority.Kind != "policy" || got.Runs.Rows[0].Authority.Ref != "nightly-frontier" {
		t.Errorf("authority = %+v", got.Runs.Rows[0].Authority)
	}
	if got.Runs.Rows[1].Authority != (RunAuthority{}) {
		t.Errorf("a receipt with no recorded authority claimed one: %+v", got.Runs.Rows[1].Authority)
	}
}
