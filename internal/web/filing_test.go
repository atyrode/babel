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
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// topicPlans is a wired stand-in for §4.13's plan surface.
//
// It is a fixture rather than the ledger because the ledger half is another
// component's: what this package owns is the projection and the ruling that
// reaches it, so the fake answers with the shape the page renders and records
// what the handler asked for. Applying resolves to an entity the ledger really
// holds, which is what makes the ruling's answer a real read rather than an
// echo.
type topicPlans struct {
	plans []TopicPlanView
	// applied and declined keep what the handler passed, so the
	// attribution assertions read the call rather than the response.
	applied        string
	appliedBy      string
	declined       string
	declinedBy     string
	declinedReason string
	entityID       string
	// failApply is the ledger refusing after the ruling was recorded,
	// which is the state a topic ruling has to report rather than hide.
	failApply error
	err       error
}

func (p *topicPlans) OpenTopicPlans(context.Context) ([]TopicPlanView, error) {
	return p.plans, p.err
}

func (p *topicPlans) TopicPlan(_ context.Context, proposalID string) (TopicPlanView, bool, error) {
	if p.err != nil {
		return TopicPlanView{}, false, p.err
	}
	for _, plan := range p.plans {
		if plan.ProposalID == proposalID {
			return plan, true, nil
		}
	}
	return TopicPlanView{}, false, nil
}

func (p *topicPlans) ApplyTopicPlan(_ context.Context, proposalID, operator string) (
	TopicPlanOutcome, error) {
	p.applied, p.appliedBy = proposalID, operator
	outcome := TopicPlanOutcome{Operation: "create", EntityID: p.entityID, Filed: 2}
	if p.failApply != nil {
		return TopicPlanOutcome{Operation: "create"}, p.failApply
	}
	return outcome, nil
}

func (p *topicPlans) DeclineTopicPlan(_ context.Context, proposalID, operator, reason string) error {
	p.declined, p.declinedBy, p.declinedReason = proposalID, operator, reason
	return nil
}

// topicPlansFixture is the harness's default plan surface: one plan on the
// fixture proposal record, carrying a run's wording, whose application
// resolves to the fixture entity.
func topicPlansFixture(h *phaseB, text string) *topicPlans {
	return &topicPlans{
		entityID: h.entity.ID,
		plans: []TopicPlanView{{
			ProposalID: h.proposal.ID,
			Operation:  "create",
			Name:       "manifold " + text,
			Kind:       "repository",
			Records:    []string{h.finding.ID},
			Why:        "32 sessions in 3 checkouts cite it " + text,
			RunID:      "run-1",
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
	if len(topics.Proposed) != 1 || topics.Proposed[0].ProposalID != h.proposal.ID {
		t.Fatalf("proposed = %+v, want the plan on the published proposal", topics.Proposed)
	}
	proposal := topics.Proposed[0]
	if proposal.Operation != "create" || proposal.Name == "" {
		t.Errorf("the proposal = %+v, want the act it would perform and the topic it names", proposal)
	}
	if proposal.Title != h.proposal.Payload.Title {
		t.Errorf("the rail row reads %q and the proposal record reads %q",
			proposal.Title, h.proposal.Payload.Title)
	}
	if proposal.RunID == "" {
		t.Errorf("the proposal = %+v, want the run that wrote it", proposal)
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

// TestAcceptingATopicProposalAppliesItsPlan is §4.13's second reading seen
// from the surface: a topic change is an ordinary proposal, the operator
// rules on it where he rules on everything else, and the acceptance is what
// applies it. There is no topic accept route to call.
func TestAcceptingATopicProposalAppliesItsPlan(t *testing.T) {
	var plans *topicPlans
	h := newPhaseB(t, feedText, func(o *Options) {
		plans = o.TopicPlans.(*topicPlans)
	})

	var ruled decideResult
	decodeResponse(t, h.okPost(t, "/api/review/decide",
		`{"subject":{"type":"proposal","id":"`+h.proposal.ID+`"},"disposition":"accept"}`), &ruled)
	if ruled.Topic == nil {
		t.Fatalf("the ruling = %+v, want it to say what it did to the ledger", ruled)
	}
	if !ruled.Topic.Applied || ruled.Topic.EntityID != h.entity.ID || ruled.Topic.Error != "" {
		t.Fatalf("the ruling's topic = %+v, want the applied plan and the topic it produced", ruled.Topic)
	}
	if ruled.Topic.Filed == 0 {
		t.Errorf("the ruling's topic = %+v, want the records it filed counted", ruled.Topic)
	}
	if plans.applied != h.proposal.ID || plans.appliedBy != operatorID {
		t.Errorf("the handler applied %q as %q, want the operator ruling on the proposal",
			plans.applied, plans.appliedBy)
	}
	if plans.declined != "" {
		t.Errorf("an acceptance declined %q", plans.declined)
	}
}

// TestARulingStandsWhenTheLedgerRefusesTheAct is the half-applied case the
// two stores make possible: the disposition is appended and §4.7 does not
// un-append one, so a ledger refusal afterwards is reported beside a ruling
// that is durable rather than turned into a failed request.
func TestARulingStandsWhenTheLedgerRefusesTheAct(t *testing.T) {
	var plans *topicPlans
	h := newPhaseB(t, feedText, func(o *Options) {
		plans = o.TopicPlans.(*topicPlans)
		plans.failApply = errors.New("the topic was retired after this was proposed")
	})

	var ruled decideResult
	decodeResponse(t, h.okPost(t, "/api/review/decide",
		`{"subject":{"type":"proposal","id":"`+h.proposal.ID+`"},"disposition":"accept"}`), &ruled)
	if ruled.Status == "" || ruled.Event.ID == "" {
		t.Fatalf("the ruling = %+v, want the disposition it appended", ruled)
	}
	if ruled.Topic == nil || ruled.Topic.Applied {
		t.Fatalf("the ruling's topic = %+v, want an act that did not land", ruled.Topic)
	}
	if !strings.Contains(ruled.Topic.Error, "retired") {
		t.Errorf("the ruling reports %q, want the ledger's own refusal", ruled.Topic.Error)
	}
}

// TestRejectingATopicProposalKeepsTheReason is the other ruling: §4.13
// suppresses the same proposal until materially new evidence exists, and the
// reason is what makes that decision readable later — so a rejection with no
// note is refused before the disposition is appended.
func TestRejectingATopicProposalKeepsTheReason(t *testing.T) {
	var plans *topicPlans
	h := newPhaseB(t, feedText, func(o *Options) {
		plans = o.TopicPlans.(*topicPlans)
	})

	response := h.post("/api/review/decide",
		`{"subject":{"type":"proposal","id":"`+h.proposal.ID+`"},"disposition":"reject"}`)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("a rejection with no reason: status = %d, want 400", response.StatusCode)
	}
	if plans.declined != "" {
		t.Fatalf("the handler declined %q before checking the reason", plans.declined)
	}

	var ruled decideResult
	decodeResponse(t, h.okPost(t, "/api/review/decide",
		`{"subject":{"type":"proposal","id":"`+h.proposal.ID+
			`"},"disposition":"reject","note":"that directory is not a project"}`), &ruled)
	if ruled.Topic == nil || !ruled.Topic.Declined {
		t.Fatalf("the ruling's topic = %+v, want the plan declined", ruled.Topic)
	}
	if plans.declined != h.proposal.ID || plans.declinedBy != operatorID {
		t.Fatalf("the handler declined %q as %q, want the operator refusing the proposal",
			plans.declined, plans.declinedBy)
	}
	if plans.declinedReason != "that directory is not a project" {
		t.Errorf("the reason reached the ledger as %q, want it verbatim", plans.declinedReason)
	}
	if plans.applied != "" {
		t.Errorf("a rejection applied %q", plans.applied)
	}
}

// TestARulingOnAnOrdinaryProposalTouchesNoTopic is the other side of the
// same route: most proposals are about the corpus rather than about the
// ledger's naming, and a ruling on one must not report a topic act.
func TestARulingOnAnOrdinaryProposalTouchesNoTopic(t *testing.T) {
	var plans *topicPlans
	h := newPhaseB(t, feedText, func(o *Options) {
		plans = o.TopicPlans.(*topicPlans)
		plans.plans = nil
	})

	var ruled decideResult
	decodeResponse(t, h.okPost(t, "/api/review/decide",
		`{"subject":{"type":"proposal","id":"`+h.proposal.ID+`"},"disposition":"accept"}`), &ruled)
	if ruled.Topic != nil {
		t.Fatalf("the ruling reports %+v, want no topic act", ruled.Topic)
	}
	if plans.applied != "" {
		t.Errorf("the handler applied %q for a proposal carrying no plan", plans.applied)
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

// TestUnboundIdentitiesAreEvidenceWithTheRecordsThatCiteThem is what the
// repository derivation is for now: evidence handed to the filing run, never
// a proposal this surface minted.
//
// The propagation is the half worth asserting: only the observation cites a
// session, and the candidate it develops, the finding that consolidates it
// and the proposal that rests on that finding all reach the repository
// through the development path. That is what makes the evidence worth
// handing over — it arrives with the records a run would consider filing
// rather than with one.
func TestUnboundIdentitiesAreEvidenceWithTheRecordsThatCiteThem(t *testing.T) {
	h := newPhaseB(t, feedText, withCitedWorkspace)

	observed, err := h.server.UnboundIdentities(h.ctx)
	if err != nil {
		t.Fatalf("UnboundIdentities: %v", err)
	}
	var seen reality.TopicObservation
	for _, identity := range observed {
		if identity.Name == feedTopic {
			seen = identity
		}
	}
	if seen.Name == "" {
		t.Fatalf("the repository this host observed is not offered: %+v", observed)
	}
	if seen.Remote != feedRepository {
		t.Errorf("remote = %q, want the repository's own remote %q", seen.Remote, feedRepository)
	}
	if seen.Sessions != 1 || seen.Checkouts != 1 {
		t.Errorf("observation = %+v, want the evidence behind it", seen)
	}
	got := map[string]bool{}
	for _, ref := range seen.Records {
		got[ref.ID] = true
	}
	for _, want := range []string{h.hypothesis.ID, h.finding.ID, h.proposal.ID, h.observationID(t)} {
		if !got[want] {
			t.Errorf("%s is not among the records this identity carries; the lineage did not propagate",
				want)
		}
	}
	// It observes and never proposes: nothing is a topic, and nothing is
	// offered for a ruling, because only a run's published proposal is.
	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	for _, topic := range topics.Topics {
		if topic.Name == feedTopic {
			t.Errorf("an observed repository is a topic before anybody proposed it: %+v", topic)
		}
	}
}

// TestUnboundIdentitiesSkipWhatTheLedgerAlreadyBinds keeps the evidence
// honest: a repository the operator has already named is not something a run
// needs to propose, and offering it would produce a proposal the ledger
// refuses as bound.
func TestUnboundIdentitiesSkipWhatTheLedgerAlreadyBinds(t *testing.T) {
	h := newPhaseB(t, feedText, withCitedWorkspace)

	if _, _, err := h.reality.AssertFact(h.ctx, reality.FactInput{
		SubjectID:   h.entity.ID,
		Predicate:   reality.Predicate("repository-remote"),
		Value:       reality.FactValue{Kind: reality.ValueText, Text: feedRepository},
		ValidFrom:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		ObservedAt:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: operatorID, At: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		Confidence:  reality.ConfidenceHigh,
		Sensitivity: reality.SensitivityRoutine,
	}); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	observed, err := h.server.UnboundIdentities(h.ctx)
	if err != nil {
		t.Fatalf("UnboundIdentities: %v", err)
	}
	for _, identity := range observed {
		if identity.Remote == feedRepository {
			t.Fatalf("a bound repository is still offered as unnamed: %+v", identity)
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
