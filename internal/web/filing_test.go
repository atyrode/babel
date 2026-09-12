package web

// What a record is about (SPEC.md §4.13), held to what a reader observes.
//
// The correction this section makes to §8.7's first topics is the thing under
// test: a topic is a Reality Ledger entity the operator created, a post's
// topics are the filings that put it there, and a name Babel derived from a
// repository is a proposal rather than a community. So the assertions are
// about which of the three lists a thing lands in, and about the acts that
// move it between them.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// topicProposalQuestionID is the open topic question the harness's fixture
// offers. It is a constant because the route sweep answers it by name.
const topicProposalQuestionID = "qst_topic_fixture"

// topicQuestions is a wired stand-in for §4.13's proposal surface.
//
// It is a fixture rather than the ledger because the ledger half is another
// component's: what this package owns is the projection and the two acts, so
// the fake answers with the shape the page renders and records what the
// handler asked for. Accepting resolves to an entity the ledger really holds,
// which is what makes the 201's topic row a real read rather than an echo.
type topicQuestions struct {
	proposals []TopicProposalView
	accepted  string
	entityID  string
	// acceptedBy and declined keep what the handler passed, so the
	// attribution assertions read the call rather than the response.
	acceptedBy     string
	declined       string
	declinedReason string
	err            error
}

func (q *topicQuestions) TopicProposals(context.Context) ([]TopicProposalView, error) {
	return q.proposals, q.err
}

func (q *topicQuestions) AcceptTopic(_ context.Context, questionID, operator string) (string, error) {
	q.accepted, q.acceptedBy = questionID, operator
	return q.entityID, nil
}

func (q *topicQuestions) DeclineTopic(_ context.Context, questionID, operator, reason string) error {
	q.declined, q.declinedReason = questionID, reason
	q.acceptedBy = operator
	return nil
}

// topicQuestionsFixture is the harness's default proposal surface: one open
// proposal carrying a run's wording, and an acceptance that resolves to the
// fixture entity.
func topicQuestionsFixture(h *phaseB, text string) *topicQuestions {
	return &topicQuestions{
		entityID: h.entity.ID,
		proposals: []TopicProposalView{{
			QuestionID: topicProposalQuestionID,
			Name:       "manifold " + text,
			Kind:       "repository",
			Identity:   "github.com/atyrode/manifold",
			Remote:     "github.com/atyrode/manifold",
			Paths:      []string{"/home/operator/manifold"},
			Why:        "32 sessions in 3 checkouts cite it " + text,
		}},
	}
}

// topicStanceFixture is the harness's default stance reader: the fixture
// entity is one the operator is working on, with his own reason.
func topicStanceFixture(h *phaseB, text string) TopicStanceReader {
	return TopicStanceFunc(func(_ context.Context, entityID string) (TopicInterestView, error) {
		if entityID != h.entity.ID {
			return TopicInterestView{}, nil
		}
		return TopicInterestView{
			State:  interestWorking,
			Reason: "this is the thing I am on " + text,
			At:     "2026-09-12T09:00:00Z",
			By:     operatorID,
		}, nil
	})
}

// writeFilings puts one record under the fixture topic, which is what makes a
// withdrawal possible: §4.13 refuses to unfile a record that is not filed, so
// the route sweep needs a filing to withdraw.
func (h *phaseB) writeFilings(text string) {
	h.t.Helper()
	if _, err := h.front.File(h.ctx, frontier.FilingInput{
		Record:    frontier.Ref{Type: frontier.EntityFinding, ID: h.finding.ID},
		EntityID:  h.entity.ID,
		Rationale: "the consolidation is about that project " + text,
		Author:    frontier.FilingOperator,
		AuthorID:  operatorID,
	}); err != nil {
		h.t.Fatalf("File: %v", err)
	}
}

// TestDayOneHasNoTopicsAndEverythingUnfiled is §4.13's intended starting state
// rather than a degradation.
//
// A deployment that has created no entity has no topics, because a topic is an
// entity and only an attributed operator act makes one. What it does have is
// what Babel proposed and a backlog, and the assertion is that the page says
// exactly that instead of inventing a vocabulary out of repository names.
func TestDayOneHasNoTopicsAndEverythingUnfiled(t *testing.T) {
	h := newPhaseB(t, feedText, func(o *Options) {
		// No ledger: the harness's fixture entities are what a deployment
		// on its first day does not have.
		o.Reality = nil
	})

	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	if len(topics.Topics) != 0 {
		t.Errorf("a deployment with no entities offers %d topics: %+v", len(topics.Topics), topics.Topics)
	}
	if len(topics.Proposed) != 1 || topics.Proposed[0].QuestionID != topicProposalQuestionID {
		t.Fatalf("proposed = %+v, want the seeder's one proposal", topics.Proposed)
	}
	proposal := topics.Proposed[0]
	if proposal.Binding == nil || proposal.Binding.Remote != "github.com/atyrode/manifold" {
		t.Errorf("the proposal's binding = %+v, want the repository it proposes", proposal.Binding)
	}
	if !strings.Contains(proposal.Why, "32 sessions") {
		t.Errorf("the proposal's why = %q, want the run's own sentence", proposal.Why)
	}

	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	if feed.Total == 0 {
		t.Fatal("the fixture deployment has no posts, so being unfiled asserts nothing")
	}
	for _, post := range feed.Posts {
		if len(post.Topics) != 0 {
			t.Errorf("%s carries topics %v on a deployment with none", post.ID, post.Topics)
		}
	}
	if topics.Unfiled != feed.Total {
		t.Errorf("the sidebar counts %d unfiled and the feed holds %d posts", topics.Unfiled, feed.Total)
	}
}

// TestTopicsAreTheEntitiesRecordsAreFiledUnder is the section's substitution,
// asserted end to end: the sidebar's rows are ledger entities, their counts
// come from filings, and the feed a row opens holds exactly those records.
func TestTopicsAreTheEntitiesRecordsAreFiledUnder(t *testing.T) {
	h := newPhaseB(t, feedText, nil)
	file(t, h, frontier.EntityProposal, h.proposal.ID, h.entity.ID)

	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	var filed topicRow
	for _, topic := range topics.Topics {
		if topic.ID == h.entity.ID {
			filed = topic
		}
	}
	if filed.ID == "" {
		t.Fatalf("the ledger's entity is not a topic: %+v", topics.Topics)
	}
	if filed.Name != h.entity.Payload.DisplayName {
		t.Errorf("topic name = %q, want the entity's display name %q", filed.Name, h.entity.Payload.DisplayName)
	}
	if filed.Kind != string(h.entity.Kind) {
		t.Errorf("topic kind = %q, want %q", filed.Kind, h.entity.Kind)
	}
	// The finding the harness filed plus the proposal filed above.
	if filed.Posts != 2 {
		t.Errorf("topic = %+v, want the two records filed under it", filed)
	}
	if filed.LatestAt == "" {
		t.Errorf("topic = %+v, want the newest filed post's time", filed)
	}
	if filed.Interest.State != interestWorking || filed.Interest.Reason == "" {
		t.Errorf("topic interest = %+v, want the stance the operator recorded", filed.Interest)
	}

	// The feed the row opens: the same two records, and each of them says so.
	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+h.entity.ID+"&limit=100"), &feed)
	if feed.Total != filed.Posts {
		t.Fatalf("the topic feed holds %d and the sidebar counts %d", feed.Total, filed.Posts)
	}
	got := map[string]bool{}
	for _, post := range feed.Posts {
		got[post.ID] = true
		if !contains(post.Topics, h.entity.Payload.DisplayName) {
			t.Errorf("%s is in the topic feed with topics %v", post.ID, post.Topics)
		}
	}
	if !got[h.finding.ID] || !got[h.proposal.ID] {
		t.Errorf("the topic feed holds %v, want the finding and the proposal", got)
	}
	// A record whose filing nobody made is unfiled, and the count and the
	// page it opens agree.
	var unfiled feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+topicUnfiled+"&limit=100"), &unfiled)
	if unfiled.Total != topics.Unfiled {
		t.Errorf("the unfiled feed holds %d and the sidebar counts %d", unfiled.Total, topics.Unfiled)
	}
	if unfiled.Total == 0 {
		t.Error("nothing is unfiled, so the reserved topic asserts nothing")
	}
}

// TestTheTopicFilterTakesANameOrAnID is the one ambiguity a reader meets: the
// sidebar hands out ids and a person types names, and both have to open the
// same page.
func TestTheTopicFilterTakesANameOrAnID(t *testing.T) {
	h := newPhaseB(t, feedText, nil)

	var byID, byName feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+h.entity.ID+"&limit=100"), &byID)
	decodeResponse(t, h.ok(t, "/api/feed?topic="+url.QueryEscape(h.entity.Payload.DisplayName)+"&limit=100"), &byName)
	if byID.Total == 0 {
		t.Fatal("the topic holds nothing by id, so the name half asserts nothing")
	}
	if byName.Total != byID.Total {
		t.Fatalf("the name opens %d posts and the id opens %d", byName.Total, byID.Total)
	}
	for i := range byID.Posts {
		if byID.Posts[i].ID != byName.Posts[i].ID {
			t.Fatalf("row %d differs: %s by id, %s by name", i, byID.Posts[i].ID, byName.Posts[i].ID)
		}
	}
}

// TestFilingAndUnfilingAreTwoAppendsAndOneHistory drives §4.13's two operator
// acts through the routes and reads the durable history back.
func TestFilingAndUnfilingAreTwoAppendsAndOneHistory(t *testing.T) {
	h := newPhaseB(t, feedText, nil)
	record := frontier.Ref{Type: frontier.EntityHypothesis, ID: h.hypothesis.ID}

	var filed filingResult
	decodeResponse(t, h.createdPost(t, "/api/record/"+h.hypothesis.ID+"/file",
		`{"entity":"`+h.entity.ID+`","rationale":"the claim is about that project"}`), &filed)
	if filed.Filing.Topic != h.entity.ID || filed.Filing.Author != string(frontier.FilingOperator) {
		t.Fatalf("filing = %+v, want the operator's own filing under the entity", filed.Filing)
	}
	if filed.Filing.Heuristic {
		t.Error("a filing the operator performed is marked heuristic")
	}
	if filed.Filing.TopicName != h.entity.Payload.DisplayName {
		t.Errorf("filing topic name = %q, want %q", filed.Filing.TopicName, h.entity.Payload.DisplayName)
	}

	var withdrawn filingResult
	decodeResponse(t, h.okPost(t, "/api/record/"+h.hypothesis.ID+"/unfile",
		`{"entity":"`+h.entity.ID+`","reason":"it is about the deployment, not the project"}`), &withdrawn)
	if !withdrawn.Filing.Withdrawn || withdrawn.Filing.WithdrawReason == "" {
		t.Fatalf("withdrawal = %+v, want a withdrawn filing carrying its reason", withdrawn.Filing)
	}

	// Both rows survive: §4.13's history of where a record was filed and why
	// is only readable if a correction leaves its predecessor alone.
	history, err := h.front.FilingsOf(h.ctx, record)
	if err != nil {
		t.Fatalf("FilingsOf: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("the record's filing history holds %d rows, want the filing and its withdrawal", len(history))
	}
	// And the feed agrees: the post is unfiled again.
	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+topicUnfiled+"&limit=100"), &feed)
	found := false
	for _, post := range feed.Posts {
		found = found || post.ID == h.hypothesis.ID
	}
	if !found {
		t.Error("the withdrawn record is not back in the unfiled feed")
	}
}

// TestFilingRefusesAnEntityTheLedgerDoesNotHold is §4.8's rule at the filing
// boundary: only an attributed operator act creates an entity, so a name
// nobody created is a refusal rather than a new topic.
func TestFilingRefusesAnEntityTheLedgerDoesNotHold(t *testing.T) {
	h := newPhaseB(t, feedText, nil)

	response := h.post("/api/record/"+h.hypothesis.ID+"/file",
		`{"entity":"a project nobody created","rationale":"it feels related"}`)
	text := body(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d body %q, want 404", response.StatusCode, text)
	}
	if !strings.Contains(text, "a project nobody created") {
		t.Errorf("the refusal does not name what failed to resolve: %q", text)
	}

	// The two bodies a filing cannot have: no topic, and no reason.
	for _, body := range []string{
		`{"entity":"","rationale":"it feels related"}`,
		`{"entity":"` + h.entity.ID + `","rationale":"  "}`,
	} {
		response := h.post("/api/record/"+h.hypothesis.ID+"/file", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", body, response.StatusCode)
		}
	}
	// Unfiling something that is not filed is a conflict rather than a
	// silent success: nothing was withdrawn.
	response = h.post("/api/record/"+h.hypothesis.ID+"/unfile",
		`{"entity":"`+h.entity.ID+`","reason":"it was never about that"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Errorf("unfiling an unfiled record: status = %d, want 409", response.StatusCode)
	}
}

// TestAcceptingAProposalMakesItATopic is §4.13's one-act acceptance seen from
// the surface: the proposal leaves the proposed list and the topic it becomes
// is what the route answers with.
func TestAcceptingAProposalMakesItATopic(t *testing.T) {
	var questions *topicQuestions
	h := newPhaseB(t, feedText, func(o *Options) {
		questions = o.TopicQuestions.(*topicQuestions)
	})

	var accepted topicResult
	decodeResponse(t, h.createdPost(t, "/api/topics/accept",
		`{"question_id":"`+topicProposalQuestionID+`"}`), &accepted)
	if accepted.Topic.ID != h.entity.ID {
		t.Fatalf("accepted topic = %+v, want the entity the acceptance created", accepted.Topic)
	}
	if accepted.Topic.Posts == 0 {
		t.Errorf("accepted topic = %+v, want the records it files counted", accepted.Topic)
	}
	if questions.accepted != topicProposalQuestionID || questions.acceptedBy != operatorID {
		t.Errorf("the handler accepted %q as %q, want the operator answering the proposal",
			questions.accepted, questions.acceptedBy)
	}
	// An acceptance records no reason, because the entity it creates is the
	// reason; a decline records nothing else.
	refused := h.post("/api/topics/accept",
		`{"question_id":"`+topicProposalQuestionID+`","reason":"because"}`)
	refused.Body.Close()
	if refused.StatusCode != http.StatusBadRequest {
		t.Errorf("an acceptance carrying a reason: status = %d, want 400", refused.StatusCode)
	}
}

// TestDecliningAProposalKeepsTheReason is the other answer: §4.8 suppresses
// the same proposal until materially new evidence exists, and the reason is
// what makes that decision readable later.
func TestDecliningAProposalKeepsTheReason(t *testing.T) {
	var questions *topicQuestions
	h := newPhaseB(t, feedText, func(o *Options) {
		questions = o.TopicQuestions.(*topicQuestions)
	})

	response := h.post("/api/topics/decline", `{"question_id":"`+topicProposalQuestionID+`","reason":""}`)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("a decline with no reason: status = %d, want 400", response.StatusCode)
	}
	if questions.declined != "" {
		t.Fatalf("the handler declined %q before checking the reason", questions.declined)
	}

	accepted := h.okPost(t, "/api/topics/decline",
		`{"question_id":"`+topicProposalQuestionID+`","reason":"that directory is not a project"}`)
	accepted.Body.Close()
	if questions.declined != topicProposalQuestionID {
		t.Fatalf("the handler declined %q, want the proposal", questions.declined)
	}
	if questions.declinedReason != "that directory is not a project" {
		t.Errorf("the reason reached the ledger as %q, want it verbatim", questions.declinedReason)
	}
}

// TestTopicsSortByTheOperatorsInterestThenBySize is §4.13's reading order,
// which is the one thing the topics list decides on its own.
//
// The unset group between watching and not-now is the assertion worth having:
// silence is not a refusal, so a topic nobody has taken a position on outranks
// one he deliberately parked.
func TestTopicsSortByTheOperatorsInterestThenBySize(t *testing.T) {
	stances := map[string]string{}
	h := newPhaseB(t, feedText, func(o *Options) {
		o.Stance = TopicStanceFunc(func(_ context.Context, entityID string) (TopicInterestView, error) {
			return TopicInterestView{State: stances[entityID]}, nil
		})
	})
	parked := h.newEntity(t, "a parked project")
	watched := h.newEntity(t, "a watched project")
	excluded := h.newEntity(t, "an excluded project")
	stances[h.entity.ID] = interestWorking
	stances[parked] = interestNotNow
	stances[watched] = interestWatching
	stances[excluded] = interestExcluded

	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	var order []string
	for _, topic := range topics.Topics {
		switch topic.ID {
		case h.entity.ID, parked, watched, excluded:
			order = append(order, topic.Interest.State)
		}
	}
	want := []string{interestWorking, interestWatching, interestNotNow, interestExcluded}
	if len(order) != len(want) {
		t.Fatalf("the four stated topics render as %v", order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("topics order = %v, want %v", order, want)
		}
	}
	// The topics with no stance sort between watching and not-now, which is
	// where silence belongs.
	var states []string
	for _, topic := range topics.Topics {
		states = append(states, topic.Interest.State)
	}
	unset, notNow := -1, -1
	for i, state := range states {
		if state == "" && unset < 0 {
			unset = i
		}
		if state == interestNotNow {
			notNow = i
		}
	}
	if unset < 0 || notNow < 0 || unset > notNow {
		t.Errorf("states = %v, want the unset topics above the parked one", states)
	}
}

// TestSeedTopicsProposesRepositoriesWithTheRecordsThatCiteThem is what stage
// 1's derivation is for now.
//
// The propagation is the half worth asserting: only the observation cites a
// session, and the candidate it develops, the finding that consolidates it and
// the proposal that rests on that finding all reach the repository through the
// development path. That is what makes a topic question worth raising — it
// arrives with the records it would file rather than with one.
func TestSeedTopicsProposesRepositoriesWithTheRecordsThatCiteThem(t *testing.T) {
	h := newPhaseB(t, feedText, withCitedWorkspace)

	seeds, err := h.server.seedTopics(h.ctx)
	if err != nil {
		t.Fatalf("seedTopics: %v", err)
	}
	var seeded seedTopic
	for _, seed := range seeds {
		if seed.Name == feedTopic {
			seeded = seed
		}
	}
	if seeded.Name == "" {
		t.Fatalf("the repository this host observed is not proposed: %+v", seeds)
	}
	if seeded.Remote != feedRepository {
		t.Errorf("seed remote = %q, want the repository's own remote %q", seeded.Remote, feedRepository)
	}
	if seeded.Sessions != 1 || seeded.Checkouts != 1 {
		t.Errorf("seed = %+v, want the evidence it was proposed on", seeded)
	}
	got := map[string]bool{}
	for _, ref := range seeded.Records {
		got[ref.ID] = true
	}
	for _, want := range []string{h.hypothesis.ID, h.finding.ID, h.proposal.ID, h.observationID(t)} {
		if !got[want] {
			t.Errorf("%s is not among the records the seed would file; the lineage did not propagate", want)
		}
	}
	// Seeding proposes and never files: until the operator accepts, the
	// records are unfiled and the topic does not exist.
	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	for _, topic := range topics.Topics {
		if topic.Name == feedTopic {
			t.Errorf("the seeder's name is a topic before anybody accepted it: %+v", topic)
		}
	}
}

// createdPost performs the POST that mints something and refuses anything but
// the 201 that says it did.
func (h *phaseB) createdPost(t *testing.T, path, body string) *http.Response {
	t.Helper()
	response := h.post(path, body)
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		t.Fatalf("POST %s: status = %d", path, response.StatusCode)
	}
	return response
}

// newEntity creates one more ledger entity, which the ordering test needs
// several of.
func (h *phaseB) newEntity(t *testing.T, name string) string {
	t.Helper()
	entity, err := h.reality.CreateEntity(h.ctx, reality.EntityInput{
		Kind:    reality.EntityProject,
		Payload: reality.EntityPayload{DisplayName: name},
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	return entity.ID
}

// file performs one operator filing through the route, which is how a test
// arranges a topic that has something in it.
func file(t *testing.T, h *phaseB, kind frontier.EntityType, id, entityID string) {
	t.Helper()
	response := h.post("/api/record/"+id+"/file",
		`{"entity":"`+entityID+`","rationale":"the `+string(kind)+` is about it"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("file %s: status = %d body %q", id, response.StatusCode, body(t, response))
	}
}
