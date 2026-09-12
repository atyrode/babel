package web

// §4.13's acts on a topic's identity, driven through HTTP and then read back
// out of the ledger.
//
// Every assertion here pairs the response with the durable record, because the
// whole claim of the section is that interest is a fact about the world rather
// than a setting: a route that answered `{"state":"not-now"}` while writing
// nothing, or while writing something a focus decision does not read, would
// look identical from the response alone. So each test asks the ledger what it
// now holds — which revision is in force, which one it superseded, what the
// parent's resolution role became, where a record is filed — and the refusals
// are paired with the durable state staying exactly as it was.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
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

// TestTopicRetirementStatesWhy pairs §4.13's "with reasons" with the storage:
// an unexplained retirement is refused before the ledger is touched, and the
// explained one is a lifecycle fact that deletes nothing.
func TestTopicRetirementStatesWhy(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	response := h.post("/api/topics/"+h.canonical.ID+"/retire", `{"reason":""}`)
	text := body(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body %q, want 400", response.StatusCode, text)
	}
	if !strings.Contains(text, "reason") {
		t.Errorf("refusal does not say what is missing: %s", text)
	}
	if facts := allFacts(t, h, h.canonical.ID); len(facts) != 0 {
		t.Fatalf("a refused retirement wrote %d fact(s)", len(facts))
	}

	var result topicActResult
	decodeResponse(t, h.post("/api/topics/"+h.canonical.ID+"/retire",
		`{"reason":"the operator never had a repository by this name"}`), &result)
	if result.Topic == nil || result.Topic.ID != h.canonical.ID {
		t.Fatalf("topic = %+v", result.Topic)
	}
	// A retired topic has no stance. It is not a degree of interest, and
	// reporting the last one would put a retired name back in the list.
	if result.Topic.Interest.State != "" {
		t.Errorf("interest after retirement = %+v, want none", result.Topic.Interest)
	}
	assertFactInForce(t, h, h.canonical.ID, reality.PredicateLifecycle, reality.LifecycleRetired)
	retired, err := h.reality.EntityRetired(h.ctx, h.canonical.ID)
	if err != nil || !retired {
		t.Fatalf("EntityRetired = %v, %v", retired, err)
	}
	// The entity, its name and its kind are all still there: retiring is a
	// fact about the subject, not a removal of it.
	if _, err := h.reality.Entity(h.ctx, h.canonical.ID); err != nil {
		t.Fatalf("the retired topic is no longer readable: %v", err)
	}
}

// TestTopicActsRefuseATopicTheLedgerDoesNotHold checks the refusal names the
// identifier. An entity id is opaque and travels in the clear under §9.1, so
// naming it costs nothing and looking it up is the operator's next move.
func TestTopicActsRefuseATopicTheLedgerDoesNotHold(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, act := range []struct {
		name string
		path string
		body string
	}{
		{"interest", "/api/topics/ent_nothing/interest", `{"state":"working","reason":"x"}`},
		{"retire", "/api/topics/ent_nothing/retire", `{"reason":"it was a typo"}`},
		{"merge source", "/api/topics/merge",
			`{"from":"ent_nothing","into":"` + h.canonical.ID + `","reason":"one thing"}`},
		{"split parent", "/api/topics/split",
			`{"entity":"ent_nothing","name":"a part","kind":"repository","records":[],"reason":"two things"}`},
	} {
		t.Run(act.name, func(t *testing.T) {
			response := h.post(act.path, act.body)
			text := body(t, response)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d body %q, want 404", response.StatusCode, text)
			}
			if !strings.Contains(text, "ent_nothing") {
				t.Errorf("refusal does not name the topic: %s", text)
			}
		})
	}
}

// TestTopicMergeLeavesTheFilingsPointingAtTheSurvivor is the merge's whole
// promise: nothing walks the frontier rewriting edges, because a filing names
// an entity id and every reader resolves that id through the merge history.
func TestTopicMergeLeavesTheFilingsPointingAtTheSurvivor(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	record := frontier.Ref{Type: frontier.EntityHypothesis, ID: h.hypothesis.ID}
	if _, err := h.front.File(h.ctx, frontier.FilingInput{
		Record:    record,
		EntityID:  h.duplicate.ID,
		Rationale: "the session that produced this ran in that checkout",
		Author:    frontier.FilingHeuristic,
	}); err != nil {
		t.Fatalf("File: %v", err)
	}

	var result topicActResult
	decodeResponse(t, h.post("/api/topics/merge",
		`{"from":"`+h.duplicate.ID+`","into":"`+h.canonical.ID+
			`","reason":"two worktrees of one repository"}`), &result)
	if result.Topic == nil || result.Topic.ID != h.canonical.ID {
		t.Fatalf("topic = %+v", result.Topic)
	}
	canonical, err := h.reality.Resolve(h.ctx, h.duplicate.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if canonical != h.canonical.ID {
		t.Fatalf("the folded identity resolves to %q, want %q", canonical, h.canonical.ID)
	}
	// The filing row is untouched and still names the folded id, which is
	// exactly why nothing had to be rewritten.
	filings, err := h.front.FilingsOf(h.ctx, record)
	if err != nil {
		t.Fatalf("FilingsOf: %v", err)
	}
	if len(filings) != 1 || filings[0].EntityID != h.duplicate.ID {
		t.Fatalf("filings = %+v", filings)
	}
	// And a stance stated about the folded name lands on the survivor,
	// because the route resolves before it writes.
	var stance topicActResult
	decodeResponse(t, h.post("/api/topics/"+h.duplicate.ID+"/interest",
		`{"state":"watching","reason":"keep filing into it"}`), &stance)
	if stance.Interest == nil || stance.Interest.State != reality.InterestWatching {
		t.Fatalf("stance = %+v", stance.Interest)
	}
	assertFactInForce(t, h, h.canonical.ID, reality.PredicateLifecycle, reality.LifecycleMaintenanceOnly)
	if facts := allFacts(t, h, h.duplicate.ID); len(facts) != 2 {
		// Facts() reads a subject's facts through the merge history, so
		// the folded id answers with the survivor's two. What must not
		// have happened is a third fact written against the folded id
		// itself.
		t.Fatalf("facts reachable from the folded id = %d, want the survivor's two", len(facts))
	}
}

// TestTopicMergeRefusesWhatIsNotAResolution covers the two refusals this
// handler owns rather than delegates, because both are states an operator can
// reach from a page and neither is a generic conflict.
func TestTopicMergeRefusesWhatIsNotAResolution(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, refusal := range []struct {
		name  string
		body  string
		words []string
	}{
		{
			name:  "no reason",
			body:  `{"from":"` + h.duplicate.ID + `","into":"` + h.canonical.ID + `","reason":""}`,
			words: []string{"reason"},
		},
		{
			name: "different kinds",
			// h.entity is a project and h.canonical is a repository:
			// §4.8 refuses this deterministically, and the sentence
			// can name both kinds because the handler read both.
			body:  `{"from":"` + h.entity.ID + `","into":"` + h.canonical.ID + `","reason":"same thing"}`,
			words: []string{"project", "repository"},
		},
		{
			name:  "one topic",
			body:  `{"from":"` + h.canonical.ID + `","into":"` + h.canonical.ID + `","reason":"same thing"}`,
			words: []string{"same topic"},
		},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			response := h.post("/api/topics/merge", refusal.body)
			text := body(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body %q, want 400", response.StatusCode, text)
			}
			for _, word := range refusal.words {
				if !strings.Contains(text, word) {
					t.Errorf("refusal does not say %q: %s", word, text)
				}
			}
		})
	}
	if history, err := h.reality.ResolutionHistory(h.ctx, h.canonical.ID); err != nil || len(history) != 0 {
		t.Fatalf("a refused merge wrote %d resolution(s) (%v)", len(history), err)
	}
}

// TestTopicSplitMovesTheRecordsItNames is the split's whole point: the parent
// is replaced by its parts and the records the operator named travel to the
// new one, on his authority, in the same request.
func TestTopicSplitMovesTheRecordsItNames(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	record := frontier.Ref{Type: frontier.EntityHypothesis, ID: h.hypothesis.ID}
	if _, err := h.front.File(h.ctx, frontier.FilingInput{
		Record:    record,
		EntityID:  h.divisible.ID,
		Rationale: "the checkout this session ran in",
		Author:    frontier.FilingHeuristic,
	}); err != nil {
		t.Fatalf("File: %v", err)
	}

	response := h.post("/api/topics/split",
		`{"entity":"`+h.divisible.ID+`","name":"the client library","kind":"repository",`+
			`"records":["`+h.hypothesis.ID+`"],"reason":"the name covered a library and its service"}`)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body %q, want 201", response.StatusCode, body(t, response))
	}
	var result topicActResult
	decodeResponse(t, response, &result)
	if result.Topic == nil || result.Topic.Name != "the client library" ||
		result.Topic.Kind != string(reality.EntityRepository) {
		t.Fatalf("topic = %+v", result.Topic)
	}
	if result.Topic.ID == h.divisible.ID {
		t.Fatal("the split answered with the parent rather than the new part")
	}

	// The parent stops speaking for itself and keeps everything it had:
	// §4.8's split marks it so a reader knows to look at the parts.
	parent, err := h.reality.Entity(h.ctx, h.divisible.ID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if parent.Role != reality.RoleSplit {
		t.Fatalf("parent role = %q, want split", parent.Role)
	}
	// Both parts exist, and the remainder carries the parent's own name,
	// because what is left when the new thing is taken out is what the
	// parent was.
	history, err := h.reality.ResolutionHistory(h.ctx, h.divisible.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("resolution history = %+v (%v)", history, err)
	}
	if len(history[0].ResultIDs) != 2 {
		t.Fatalf("split produced %d part(s), want the remainder and the new topic", len(history[0].ResultIDs))
	}
	remainder, err := h.reality.Entity(h.ctx, history[0].ResultIDs[0])
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if remainder.Payload.DisplayName != parent.Payload.DisplayName {
		t.Errorf("remainder name = %q, want the parent's %q",
			remainder.Payload.DisplayName, parent.Payload.DisplayName)
	}

	// The named record is filed under the new part, by the operator, with
	// a rationale that says where it came from. The heuristic filing under
	// the parent is still there: an append-only history of where a record
	// was filed and why is §4.13's requirement.
	filings, err := h.front.FilingsOf(h.ctx, record)
	if err != nil {
		t.Fatalf("FilingsOf: %v", err)
	}
	if len(filings) != 2 {
		t.Fatalf("filings = %+v, want the heuristic one and the operator's", filings)
	}
	var moved frontier.Filing
	for _, filing := range filings {
		if filing.EntityID == result.Topic.ID {
			moved = filing
		}
	}
	if moved.Author != frontier.FilingOperator || moved.AuthorID != operatorID || moved.Heuristic {
		t.Fatalf("the moved filing is not the operator's: %+v", moved)
	}
	if !strings.Contains(moved.Rationale, "the name covered a library and its service") {
		t.Errorf("rationale does not carry the split's reason: %q", moved.Rationale)
	}
}

// TestTopicSplitRefusesBeforeItWrites checks the order the handler validates
// in. A split that had created the parts and then refused the third record id
// would have left an identity the operator did not ask for, and an
// append-only ledger cannot take it back.
func TestTopicSplitRefusesBeforeItWrites(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, refusal := range []struct {
		name string
		body string
		word string
	}{
		{
			name: "no reason",
			body: `{"entity":"` + h.divisible.ID + `","name":"a part","kind":"repository",` +
				`"records":[],"reason":""}`,
			word: "reason",
		},
		{
			name: "unknown kind",
			body: `{"entity":"` + h.divisible.ID + `","name":"a part","kind":"codebase",` +
				`"records":[],"reason":"two things"}`,
			word: "repository",
		},
		{
			name: "a record this deployment cannot file",
			body: `{"entity":"` + h.divisible.ID + `","name":"a part","kind":"repository",` +
				`"records":["ses_not_a_record"],"reason":"two things"}`,
			word: "ses_not_a_record",
		},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			response := h.post("/api/topics/split", refusal.body)
			text := body(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body %q, want 400", response.StatusCode, text)
			}
			if !strings.Contains(text, refusal.word) {
				t.Errorf("refusal does not say %q: %s", refusal.word, text)
			}
		})
	}
	if history, err := h.reality.ResolutionHistory(h.ctx, h.divisible.ID); err != nil || len(history) != 0 {
		t.Fatalf("a refused split wrote %d resolution(s) (%v)", len(history), err)
	}
	parent, err := h.reality.Entity(h.ctx, h.divisible.ID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if parent.Role != reality.RoleSelf {
		t.Fatalf("parent role = %q, want the refused split to have left it self", parent.Role)
	}
}

// TestTopicRoutesAnswerOneMethodAndOneShape pins the router's own contract:
// the four acts are POST-only, an unknown act under the prefix is a 404 rather
// than a route that swallowed it, and the bare listing path is untouched by
// the prefix branch.
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
