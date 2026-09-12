package web

// The palette's lookup, over the same throwaway deployment every other Phase B
// route is tested against. What these assert is what an operator sees after
// typing three letters: the thing he named, on the page that opens it, with the
// kinds that would otherwise crowd each other still visible.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// withSessions wires the session catalog the palette searches.
//
// Three of the rows are titled and one is not, which is the state a described
// corpus is normally in: the scan titles sessions in the background, so a
// listing routinely holds rows it has not reached. The untitled row is here to
// be absent from every answer.
func withSessions(opts *Options) {
	opts.Lister = SessionListerFunc(func(context.Context) (SessionsResult, error) {
		return SessionsResult{
			Sessions: []SessionRow{
				titledSession("omp/session-a", "verifying the harness by hand",
					"/home/alex/babel", "2026-05-01T10:00:00Z"),
				titledSession("omp/session-b", "zephyr, the first walk", "/home/alex/zephyr",
					"2026-05-02T10:00:00Z"),
				titledSession("omp/session-c", "zephyr again", "", "2026-05-03T10:00:00Z"),
				{Harness: "omp", SourceID: "session-d", Selector: "omp/session-d"},
			},
		}, nil
	})
}

func titledSession(selector, title, workspace, modified string) SessionRow {
	row := SessionRow{
		Harness:  "omp",
		SourceID: strings.TrimPrefix(selector, "omp/"),
		Selector: selector,
		Title:    &title,
		Modified: &modified,
	}
	if workspace != "" {
		row.Workspace = &workspace
	}
	return row
}

// names asks the palette's lookup and decodes its answer.
func (h *phaseB) names(query string) []nameHit {
	h.t.Helper()
	response := h.get("/api/search/names?" + query)
	if response.StatusCode != http.StatusOK {
		h.t.Fatalf("GET %s = %d: %.300s", query, response.StatusCode, body(h.t, response))
	}
	var result nameSearchResult
	decodeResponse(h.t, response, &result)
	if result.Hits == nil {
		h.t.Fatal("the lookup answered with a null hit list")
	}
	return result.Hits
}

func nameQuery(q string, limit int) string {
	values := url.Values{"q": {q}}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	return values.Encode()
}

// find reports the first hit of a kind, so an assertion can name what it wants
// rather than an index into an ordered list.
func find(hits []nameHit, kind string) (nameHit, bool) {
	for _, hit := range hits {
		if hit.Kind == kind {
			return hit, true
		}
	}
	return nameHit{}, false
}

// TestNameSearchFindsWhatThingsAreCalled is the palette's whole promise: three
// letters of a title, and the thing is in the list with the page that opens it.
//
// One query is asserted against four sources at once, because that is the
// request the palette actually makes — an operator types "verif", not "verif in
// proposals" — and the four answers have to arrive together to be useful.
func TestNameSearchFindsWhatThingsAreCalled(t *testing.T) {
	h := newPhaseB(t, "plain", withSessions)

	hits := h.names(nameQuery("verif", 0))
	proposal, ok := find(hits, string(frontier.EntityProposal))
	if !ok {
		t.Fatalf("no proposal in %+v", hits)
	}
	if proposal.ID != h.proposal.ID {
		t.Errorf("proposal id = %q, want %q", proposal.ID, h.proposal.ID)
	}
	if want := "#/r/" + h.proposal.ID; proposal.Href != want {
		t.Errorf("proposal href = %q, want %q", proposal.Href, want)
	}
	if !strings.Contains(proposal.Title, "verify independently") {
		t.Errorf("proposal title = %q, want the record's own words", proposal.Title)
	}
	session, ok := find(hits, kindSession)
	if !ok {
		t.Fatalf("no session in %+v", hits)
	}
	if session.Href != "#/sessions/omp%2Fsession-a" {
		t.Errorf("session href = %q, want the route that opens it", session.Href)
	}
	if session.Meta != "babel" {
		t.Errorf("session meta = %q, want the workspace it was held in", session.Meta)
	}

	// A title that begins with the query comes before a title that merely
	// contains it: "verification may be reported…" and "verify independently"
	// are what the operator was reaching for, and "claimed verification" is a
	// neighbour.
	finding, ok := find(hits, string(frontier.EntityFinding))
	if !ok {
		t.Fatalf("no finding in %+v", hits)
	}
	prefixes := 0
	for _, hit := range hits {
		if hit.ID == finding.ID {
			break
		}
		prefixes++
	}
	if prefixes < 2 {
		t.Errorf("only %d prefix matches precede the substring match: %+v", prefixes, hits)
	}

	// The same query in the operator's own capitalization finds the same
	// record: nobody types a model's casing.
	if loud := h.names(nameQuery("VERIFY INDEP", 0)); len(loud) == 0 {
		t.Error("an upper-case query found nothing")
	} else if loud[0].ID != h.proposal.ID {
		t.Errorf("upper-case query led with %+v, want the proposal", loud[0])
	}

	// A session the scan has not titled is not offered. Its selector is a
	// digest, and a row reading omp/session-d would be asking the operator to
	// recognize one.
	for _, hit := range h.names(nameQuery("session-d", 0)) {
		if hit.ID == "omp/session-d" {
			t.Errorf("an untitled session was offered as a destination: %+v", hit)
		}
	}
}

// TestNameSearchBoundsWhatItAnswers pins the two bounds a keystroke-driven
// route needs: the caller's limit, and the share that keeps one crowded kind
// from filling the list.
func TestNameSearchBoundsWhatItAnswers(t *testing.T) {
	h := newPhaseB(t, "plain", withSessions)

	if hits := h.names(nameQuery("verif", 1)); len(hits) != 1 {
		t.Errorf("limit=1 answered %d hits: %+v", len(hits), hits)
	}
	if hits := h.names(nameQuery("", 0)); len(hits) != 0 {
		t.Errorf("an empty query answered %d hits", len(hits))
	}
	for _, bad := range []string{"limit=0", "limit=51", "limit=-1", "limit=x"} {
		response := h.get("/api/search/names?q=verif&" + bad)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", bad, response.StatusCode)
		}
	}

	// Four proposals and one session all begin with the same word, and the
	// proposals are newer, so an unbounded ordering would answer three
	// proposals and hide the session. The share is what makes the palette
	// still worth typing into on a corpus whose kinds are lopsided.
	for _, title := range []string{"zephyr one", "zephyr two", "zephyr three", "zephyr four"} {
		if _, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
			RunID:      "run-1",
			FindingIDs: []string{h.finding.ID},
			Payload: frontier.ProposalPayload{
				Title:          title,
				Problem:        "the walk found nothing",
				Outcome:        "walk again",
				Impact:         frontier.ImpactLow,
				Classification: frontier.ClassificationPrivate,
			},
		}); err != nil {
			t.Fatalf("CreateProposal(%q): %v", title, err)
		}
	}
	hits := h.names(nameQuery("zephyr", 3))
	if len(hits) != 3 {
		t.Fatalf("limit=3 answered %d hits: %+v", len(hits), hits)
	}
	if _, ok := find(hits, kindSession); !ok {
		t.Errorf("four proposals crowded the session out: %+v", hits)
	}
	if _, ok := find(hits, string(frontier.EntityProposal)); !ok {
		t.Errorf("the proposals themselves are missing: %+v", hits)
	}
}

// TestNameSearchNamesTheLedgersSubjects covers the two ledger sources and the
// one state filter they carry: a subject is named by what it is called, and a
// question is offered while the deployment is still waiting for its answer.
func TestNameSearchNamesTheLedgersSubjects(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	entity, ok := find(h.names(nameQuery("a repository", 0)), kindEntity)
	if !ok {
		t.Fatal("the ledger's repository subject was not found by its display name")
	}
	if want := "#/ask/entities/" + entity.ID; entity.Href != want {
		t.Errorf("entity href = %q, want %q", entity.Href, want)
	}
	if entity.Meta != "repository" {
		t.Errorf("entity meta = %q, want the kind of thing it is", entity.Meta)
	}

	open, ok := find(h.names(nameQuery("is this project", 0)), kindQuestion)
	if !ok {
		t.Fatal("an open question was not found by its prompt")
	}
	if open.ID != h.question.ID {
		t.Errorf("question id = %q, want %q", open.ID, h.question.ID)
	}
	if want := "#/ask/questions/" + h.question.ID; open.Href != want {
		t.Errorf("question href = %q, want %q", open.Href, want)
	}
	if open.Meta != "blocking" {
		t.Errorf("question meta = %q, want how badly the answer is wanted", open.Meta)
	}

	// The answered question is history the subject's page carries, not
	// something the deployment is waiting for, so it is not a destination the
	// palette offers.
	if _, ok := find(h.names(nameQuery("is the other name", 0)), kindQuestion); ok {
		t.Error("an answered question was offered as an open one")
	}
}

// TestNameSearchFindsObservationsThroughTheIndex covers the one kind no store
// enumerates, and the confirmation that keeps its rows honest beside the rest.
func TestNameSearchFindsObservationsThroughTheIndex(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	outputs, err := h.front.Outputs(h.ctx)
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if _, err := h.index.IndexFrontier(h.ctx, outputs); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}

	observation, ok := find(h.names(nameQuery("the agent claimed", 0)),
		string(frontier.EntityObservation))
	if !ok {
		t.Fatal("an indexed observation was not found by its claim")
	}
	if !strings.HasPrefix(observation.Href, "#/r/obs") {
		t.Errorf("observation href = %q, want the record page", observation.Href)
	}

	// "outcome" is the observation's category: the index matches it because the
	// category is indexed text, and the palette must not offer a row whose
	// visible line does not contain what was typed.
	for _, hit := range h.names(nameQuery("outcome", 0)) {
		if !strings.Contains(strings.ToLower(hit.Title), "outcome") {
			t.Errorf("a hit's title does not contain the query: %+v", hit)
		}
	}
}

// TestNameSearchShowsWhereTheMatchIs pins the line a row renders for a record
// whose own words are longer than a row.
//
// A candidate's statement routinely runs to several hundred characters, and
// the operator types a word from the middle of one. A title cut from the front
// would put that row on screen with none of the words that put it there, which
// is what a broken search looks like, so the line is taken from just before
// the match instead.
func TestNameSearchShowsWhereTheMatchIs(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	statement := strings.Repeat("the corpus repeats itself at length. ", 12) +
		"and only then does it mention the zephyr."
	if _, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:   "run-1",
		Payload: frontier.HypothesisPayload{Statement: statement, Novelty: 0.5, Priority: 0.5},
	}); err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}

	hit, ok := find(h.names(nameQuery("zephyr", 0)), string(frontier.EntityHypothesis))
	if !ok {
		t.Fatal("a word late in a long statement was not found")
	}
	if !strings.Contains(hit.Title, "zephyr") {
		t.Errorf("the row does not show the match: %q", hit.Title)
	}
	if !strings.HasPrefix(hit.Title, "…") {
		t.Errorf("a re-cut line does not say the statement started earlier: %q", hit.Title)
	}
	if strings.Index(hit.Title, "zephyr") > 64 {
		t.Errorf("the match is past the width of a row: %q", hit.Title)
	}
}

// TestNameSearchRefusesLikeItsNeighbours keeps the new route inside the
// guarantees every route on this surface makes: the session decides the
// request, a read is a GET, and a deployment with nothing to search says so
// rather than answering that nothing matches.
func TestNameSearchRefusesLikeItsNeighbours(t *testing.T) {
	h := newPhaseB(t, "plain", withSessions)

	unauthorized := request(t, h.http.Client(), http.MethodGet,
		h.http.URL+"/api/search/names?q=verif", "")
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", unauthorized.StatusCode)
	}

	wrongMethod := h.post("/api/search/names?q=verif", "{}")
	wrongMethod.Body.Close()
	if wrongMethod.StatusCode != http.StatusBadRequest {
		t.Errorf("POST status = %d, want 400", wrongMethod.StatusCode)
	}

	bare := newPhaseB(t, "plain", func(opts *Options) {
		opts.Frontier, opts.Reality, opts.Lister, opts.Search = nil, nil, nil, nil
	})
	response := bare.get("/api/search/names?q=verif")
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Errorf("unwired status = %d, want 409", response.StatusCode)
	}
}
