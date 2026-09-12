package web

// §4.13's one direct act on a topic's identity, driven through HTTP and then
// read back out of the ledger.
//
// Every assertion here pairs the response with the durable record, because the
// whole claim of the section is that interest is a fact about the world rather
// than a setting: a route that answered `{"state":"not-now"}` while writing
// nothing, or while writing something a focus decision does not read, would
// look identical from the response alone. So each test asks the ledger what it
// now holds — which revision is in force, and which one it superseded — and
// the refusals are paired with the durable state staying exactly as it was.
//
// Retiring, merging and splitting were routes here and are not any more:
// §4.13's second reading makes them rulings on a published proposal, and the
// router test below is what keeps them gone.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/reality"
)

// TestTopicInterestIsRecordedAsFacts is §4.13's central claim: the four words
// are lifecycle and analysis-policy facts, attributed to the operator, with
// the reason kept verbatim — and changing a stance supersedes rather than
// edits, so the ledger still holds what the operator said before.
func TestTopicInterestIsRecordedAsFacts(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var first topicActResult
	decodeResponse(t, h.post("/api/topics/"+h.entity.ID+"/interest",
		`{"state":"not-now","reason":"nothing here until the release ships"}`), &first)
	if first.Interest == nil || first.Interest.State != reality.InterestNotNow {
		t.Fatalf("interest = %+v", first.Interest)
	}
	if first.Interest.Reason != "nothing here until the release ships" {
		t.Errorf("reason = %q, want the operator's own words", first.Interest.Reason)
	}
	if first.Interest.By != operatorID || first.Interest.At == "" {
		t.Errorf("attribution = %+v", first.Interest)
	}
	// The stance is the pair §4.13 names, and the check is against the
	// facts a focus decision would read rather than against the response.
	assertFactInForce(t, h, h.entity.ID, reality.PredicateLifecycle, reality.LifecycleDormant)
	assertFactInForce(t, h, h.entity.ID, reality.PredicateAnalysisPolicy, reality.PolicyLearnOnly)

	var second topicActResult
	decodeResponse(t, h.post("/api/topics/"+h.entity.ID+"/interest",
		`{"state":"working","reason":"the operator picked it up again"}`), &second)
	if second.Interest == nil || second.Interest.State != reality.InterestWorking {
		t.Fatalf("second interest = %+v", second.Interest)
	}
	assertFactInForce(t, h, h.entity.ID, reality.PredicateLifecycle, reality.LifecycleActive)
	assertFactInForce(t, h, h.entity.ID, reality.PredicateAnalysisPolicy, reality.PolicyNormal)

	// Nothing was edited. Both revisions of each predicate are still
	// readable, which is what makes "the operator changed his mind" a pair
	// of facts rather than a lost one.
	facts, err := h.reality.Facts(h.ctx, reality.FactQuery{
		SubjectID: h.entity.ID,
		Predicate: reality.PredicateLifecycle,
	})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("lifecycle revisions = %d, want the dormant one and the active one", len(facts))
	}
	var dormant reality.Fact
	for _, fact := range facts {
		if fact.Value.Enum == reality.LifecycleDormant {
			dormant = fact
		}
	}
	if dormant.Status != reality.FactSuperseded {
		t.Errorf("the earlier stance is %s, want superseded and still readable", dormant.Status)
	}
	if dormant.Payload.Note != "nothing here until the release ships" {
		t.Errorf("the earlier reason was not kept verbatim: %q", dormant.Payload.Note)
	}
	if dormant.Authority.Kind != reality.AuthorityOperator || dormant.Authority.ID != operatorID {
		t.Errorf("authority = %+v, want the session's operator", dormant.Authority)
	}
}

// TestTopicInterestRefusesAnUnknownState keeps the vocabulary closed at the
// boundary and names it in the refusal: a form rejected without being told
// what is acceptable is a form the operator has to guess at. Nothing is
// written, which is the half a status code alone would not show.
func TestTopicInterestRefusesAnUnknownState(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	response := h.post("/api/topics/"+h.entity.ID+"/interest", `{"state":"maybe","reason":"later"}`)
	text := body(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body %q, want 400", response.StatusCode, text)
	}
	for _, word := range reality.InterestStates() {
		if !strings.Contains(text, word) {
			t.Errorf("refusal does not offer %q: %s", word, text)
		}
	}
	if facts := allFacts(t, h, h.entity.ID); len(facts) != 0 {
		t.Fatalf("a refused stance wrote %d fact(s)", len(facts))
	}
}

// TestTopicInterestRefusesATopicTheLedgerDoesNotHold checks the refusal names
// the identifier. An entity id is opaque and travels in the clear under §9.1,
// so naming it costs nothing and looking it up is the operator's next move.
func TestTopicInterestRefusesATopicTheLedgerDoesNotHold(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	response := h.post("/api/topics/ent_nothing/interest", `{"state":"working","reason":"x"}`)
	text := body(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d body %q, want 404", response.StatusCode, text)
	}
	if !strings.Contains(text, "ent_nothing") {
		t.Errorf("refusal does not name the topic: %s", text)
	}
}

// TestTopicRoutesAnswerOneMethodAndOneShape pins the router's own contract:
// interest is POST-only, an unknown act under the prefix is a 404 rather than
// a route that swallowed it, and the bare listing path is untouched by the
// prefix branch.
//
// Retiring, merging and splitting are enrolled among the unknown acts on
// purpose. §4.13's second reading removed them, and a surface that answered
// them again — by a handler quietly returning, or by the router growing a
// case back — would be the direct button the section refuses; this is the
// test that fails if one comes back.
func TestTopicRoutesAnswerOneMethodAndOneShape(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	if response := h.get("/api/topics/" + h.entity.ID + "/interest"); response.StatusCode != http.StatusBadRequest {
		response.Body.Close()
		t.Errorf("GET an act status = %d, want 400", response.StatusCode)
	} else {
		response.Body.Close()
	}
	for _, path := range []string{
		"/api/topics/" + h.entity.ID + "/nonsense",
		"/api/topics/" + h.entity.ID,
		"/api/topics/",
		"/api/topics/" + h.entity.ID + "/retire",
		"/api/topics/merge",
		"/api/topics/split",
		"/api/topics/accept",
		"/api/topics/decline",
		"/api/topics/rule",
	} {
		response := h.post(path, `{}`)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s status = %d, want 404", path, response.StatusCode)
		}
	}
	// The listing is an exact match and is still a read.
	var listing topicList
	decodeResponse(t, h.get("/api/topics"), &listing)
	if listing.Topics == nil {
		t.Fatal("the topic listing did not answer")
	}
}

// assertFactInForce checks which revision of a predicate a focus decision
// would read for this subject. It goes through EntityInterest's own reader so
// that a test and the page cannot disagree about what "in force" means.
func assertFactInForce(t *testing.T, h *phaseB, entityID string, predicate reality.Predicate, want string) {
	t.Helper()
	facts, err := h.reality.Facts(h.ctx, reality.FactQuery{SubjectID: entityID, Predicate: predicate})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	var current reality.Fact
	for _, fact := range facts {
		if fact.Status == reality.FactActive && (current.ID == "" || fact.ObservedAt.After(current.ObservedAt)) {
			current = fact
		}
	}
	if current.ID == "" {
		t.Fatalf("%s holds no %s fact in force", entityID, predicate)
	}
	if current.Value.Enum != want {
		t.Errorf("%s %s = %q, want %q", entityID, predicate, current.Value.Enum, want)
	}
}

func allFacts(t *testing.T, h *phaseB, entityID string) []reality.Fact {
	t.Helper()
	facts, err := h.reality.Facts(h.ctx, reality.FactQuery{SubjectID: entityID})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	return facts
}
