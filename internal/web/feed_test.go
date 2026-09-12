package web

// The front page (§8.7), held to what a reader observes.
//
// Two kinds of test are here and the split is deliberate. The ranks are pure
// functions over a score, an age and a handful of votes, so they are asserted
// directly: what "hot" means is a claim this file makes about the deployment's
// order, and a test that could only reach it through HTTP would be asserting
// the router. Everything else is driven through the routes against the real
// stores, because the properties that matter there — what a topic is, what a
// comment is, what a ruling is not — are about the assembly rather than the
// arithmetic.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
)

// feedText is woven through the fixtures the feed tests read, so an assertion
// is against wording the fixture chose rather than against a field being
// non-empty.
const feedText = "the feed is babel"

// feedWorkspace is the checkout the fixture's cited session happened in, and
// feedRepository is the repository that checkout belongs to. They are
// separate because §4.13 separates them: the workspace is where the work
// happened and the repository is what it was about, and only the second is a
// topic. The remote carries a capital so the lowercasing rule is asserted
// rather than assumed — the same project seen from two machines is one topic.
const feedWorkspace = "/home/operator/checkouts/wt/witty-sage-crab"
const feedRepository = "github.com/tyrode/Tyrode-Infra"

// feedTopic is what that repository files a record under.
const feedTopic = "tyrode-infra"

// TestHotPrefersTheNewerPostAtEqualScoreAndTheHigherScoreAtEqualAge is the
// whole of what hot promises.
//
// Both halves are needed because either alone is satisfiable by a degenerate
// rank: a feed sorted by age alone passes the first, and one sorted by score
// alone passes the second. Hot is the claim that a young post with two votes
// can outrank an old one with ten, and these are its two axes.
func TestHotRankPrefersTheNewerPostAtEqualScoreAndTheHigherScoreAtEqualAge(t *testing.T) {
	old := feedEpoch.Add(24 * time.Hour)
	recent := old.Add(12 * time.Hour)

	if hotRank(5, recent) <= hotRank(5, old) {
		t.Errorf("hot ranks an older post at or above a newer one with the same score")
	}
	if hotRank(100, old) <= hotRank(5, old) {
		t.Errorf("hot ranks a lower score at or above a higher one of the same age")
	}
	// The decay is what makes it a feed rather than a leaderboard: half a
	// day of youth is worth one order of magnitude of score, so a post ten
	// times better but a day older sinks.
	if hotRank(10, old) >= hotRank(1, old.Add(48*time.Hour)) {
		t.Errorf("a day-old post with ten times the score outranks a fresh one; the decay is not applied")
	}
	// A disputed record sinks below an unvoted one rather than sorting
	// beside it, which is the sign in "signed log".
	if hotRank(-3, old) >= hotRank(0, old) {
		t.Errorf("a post voted down ranks at or above one nobody voted on")
	}
}

// TestTopHonoursTheWindowItWasAskedFor is the boundary rather than the sort.
//
// The score ordering is arithmetic nobody doubts; what "top of the day" means
// is a decision about which posts are eligible at all, and a window that
// included a post from twenty-five hours ago would make the period on the sort
// bar decorative.
func TestTopRankHonoursTheWindowItWasAskedFor(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	inside := entryAt("pro_inside", now.Add(-23*time.Hour), 1)
	outside := entryAt("pro_outside", now.Add(-24*time.Hour-time.Second), 99)

	within := filterFeed([]feedEntry{inside, outside}, "", nil, sortTop, "day", now)
	if len(within) != 1 || within[0].post.ID != "pro_inside" {
		t.Fatalf("top over a day = %v, want only the post inside it", ids(within))
	}
	// The same pair over all time keeps both, which is what makes the
	// exclusion above the window's work rather than a broken fixture.
	if all := filterFeed([]feedEntry{inside, outside}, "", nil, sortTop, windowAll, now); len(all) != 2 {
		t.Fatalf("top over all time = %v, want both posts", ids(all))
	}
	// The window is top's and controversial's alone. A hot feed narrowed to
	// a day would be the same list with its older half deleted, and a
	// reader who never chose a period would be given one.
	if hot := filterFeed([]feedEntry{inside, outside}, "", nil, sortHot, "day", now); len(hot) != 2 {
		t.Fatalf("hot over a day = %v, want the window ignored", ids(hot))
	}
}

// TestControversialNeedsBothSidesAndRewardsTheirBalance is §8.7's wording
// checked against the arithmetic.
//
// The zero is the half worth asserting. A record nine reviewers supported is
// not slightly controversial; it is agreed on, and a rank that returned a
// small positive number for it would put the deployment's most popular records
// at the bottom of a list nobody asked for.
func TestControversialRankNeedsBothSidesAndRewardsTheirBalance(t *testing.T) {
	if got := controversialRank(9, 0); got != 0 {
		t.Errorf("nine supports and no opposition rank %v, want 0", got)
	}
	if got := controversialRank(0, 9); got != 0 {
		t.Errorf("nine oppositions and no support rank %v, want 0", got)
	}
	if got := controversialRank(0, 0); got != 0 {
		t.Errorf("an unvoted record ranks %v, want 0", got)
	}
	if controversialRank(5, 5) <= controversialRank(9, 1) {
		t.Errorf("an even split ranks at or below a lopsided one of the same size")
	}
	// Magnitude still counts: two reviewers disagreeing is a smaller fact
	// than twenty doing it.
	if controversialRank(10, 10) <= controversialRank(1, 1) {
		t.Errorf("a bigger even split ranks at or below a smaller one")
	}
}

// TestRisingExcludesSilenceAndPrefersTheYoungerPost is the sort that is about
// a derivative rather than a total.
//
// Excluding silence is what keeps the list short: a rising feed that ranked
// every quiet record last would be a list of every record in the corpus with
// three interesting ones at the top.
func TestRisingRankExcludesSilenceAndPrefersTheYoungerPost(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	recent := []time.Time{now.Add(-time.Hour), now.Add(-2 * time.Hour)}
	stale := []time.Time{now.Add(-30 * time.Hour)}

	if got := risingRank(nil, now.Add(-time.Hour), now); got != 0 {
		t.Errorf("a post nobody touched ranks %v, want 0", got)
	}
	if got := risingRank(stale, now.Add(-48*time.Hour), now); got != 0 {
		t.Errorf("activity older than the window ranks %v, want 0", got)
	}
	young := risingRank(recent, now.Add(-3*time.Hour), now)
	old := risingRank(recent, now.Add(-72*time.Hour), now)
	if young <= old {
		t.Errorf("two posts with the same activity rank %v and %v; age is not applied", young, old)
	}

	// And the feed drops the silent ones rather than ranking them last.
	silent := entryAt("pro_silent", now.Add(-time.Hour), 0)
	loud := entryAt("pro_loud", now.Add(-time.Hour), 0)
	loud.activity = recent
	rising := filterFeed([]feedEntry{silent, loud}, "", nil, sortRising, windowAll, now)
	if len(rising) != 1 || rising[0].post.ID != "pro_loud" {
		t.Fatalf("rising = %v, want only the post with activity in the window", ids(rising))
	}
}

// TestEveryFeedSortBreaksItsTiesNewerFirst is the property that makes the feed
// an order rather than a set.
//
// Every sort here produces ties routinely — an unvoted corpus is all zeros on
// three of them — and a tie broken by map order would reshuffle the front page
// between two reads of the same deployment, which reads as records appearing
// and disappearing.
func TestEveryFeedSortBreaksItsTiesNewerFirst(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, sortBy := range feedSorts() {
		t.Run(sortBy, func(t *testing.T) {
			older := entryAt("pro_older", now.Add(-2*time.Hour), 0)
			newer := entryAt("pro_newer", now.Add(-time.Hour), 0)
			// Equal activity, so rising ties too rather than excluding
			// both.
			older.activity = []time.Time{now.Add(-time.Minute)}
			newer.activity = []time.Time{now.Add(-time.Minute)}
			posts := []feedEntry{older, newer}
			sortFeed(posts, sortBy, now)
			if posts[0].post.ID != "pro_newer" {
				t.Errorf("%s ordered %v, want the newer post first", sortBy, ids(posts))
			}
		})
	}
}

// TestTheFeedRefusesAVocabularyItDoesNotHave is the misspelled-filter rule
// every listing on this surface keeps.
//
// A sort nobody implements answered with the default order would show a reader
// a different feed from the one he asked for and say nothing; a kind nothing
// matches answered with an empty list reads as a deployment that has produced
// none of them. Both refuse, and the refusal names the value so the reader can
// see his own typo.
func TestTheFeedRefusesAVocabularyItDoesNotHave(t *testing.T) {
	h := newPhaseB(t, feedText, nil)
	for _, tc := range []struct{ name, path, wanted string }{
		{"an unknown sort", "/api/feed?sort=popular", "popular"},
		{"an unknown window", "/api/feed?sort=top&t=fortnight", "fortnight"},
		{"an unknown kind", "/api/feed?kind=proposal,rumour", "rumour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := h.get(tc.path)
			text := body(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", response.StatusCode, text)
			}
			if !strings.Contains(text, tc.wanted) {
				t.Errorf("refusal = %s, want the value named", text)
			}
		})
	}
}

// TestTheFeedIsEveryKindOfRecordTheDeploymentHasProduced is §8.7's first
// sentence.
//
// The observation is the one worth naming: it was the kind no listing could
// enumerate, reachable only through the candidate it develops, and a front
// page missing it would be the table of contents §8.6 replaced wearing a new
// sort bar.
func TestTheFeedIsEveryKindOfRecordTheDeploymentHasProduced(t *testing.T) {
	h := newPhaseB(t, feedText, nil)

	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	kinds := map[string]bool{}
	for _, post := range feed.Posts {
		kinds[post.Kind] = true
	}
	for _, want := range []string{
		string(frontier.EntityHypothesis), string(frontier.EntityObservation),
		string(frontier.EntityFinding), string(frontier.EntityProposal), feedKindQuestion,
	} {
		if !kinds[want] {
			t.Errorf("the feed carries no %s; it has %v", want, kinds)
		}
	}

	// Every row is one line of claim with a destination, an author where
	// the record names a run, and a standing in the record page's own
	// vocabulary.
	for _, post := range feed.Posts {
		if post.Title == "" || post.Href == "" || post.CreatedAt == "" {
			t.Fatalf("row = %+v, want a claim, a destination and an age", post)
		}
		if post.Kind == feedKindQuestion && !strings.HasPrefix(post.Href, "/ask/questions/") {
			t.Errorf("a question opens at %q, want its own page", post.Href)
		}
		if post.Kind == string(frontier.EntityObservation) && post.Standing != "" {
			t.Errorf("an observation stands at %q; §6.7 makes it unreviewable", post.Standing)
		}
	}

	// The kind filter narrows one list rather than naming a destination,
	// which is what §8.6 says a kind is for.
	var only feedList
	decodeResponse(t, h.ok(t, "/api/feed?kind=proposal,finding&limit=100"), &only)
	for _, post := range only.Posts {
		if post.Kind != "proposal" && post.Kind != "finding" {
			t.Errorf("a %s row survived a proposal/finding filter", post.Kind)
		}
	}
	if len(only.Kinds) != 2 {
		t.Errorf("kinds echoed = %v, want the two that were applied", only.Kinds)
	}
	// No filter echoes no filter, so a client cannot mistake the default
	// for a narrowing it did not ask for.
	if len(feed.Kinds) != 0 {
		t.Errorf("kinds echoed = %v on an unfiltered feed, want none", feed.Kinds)
	}
}

// TestTheFeedRanksTheWholeDeploymentBeforeItPages is §8.5's ordering rule at
// the surface it was written for.
//
// A page ranked independently would make "new" mean "newest among the
// twenty-five rows this request happened to read", which is the defect that
// makes an ordered listing worse than an unordered one: it looks sorted.
func TestTheFeedRanksTheWholeDeploymentBeforeItPages(t *testing.T) {
	h := newPhaseB(t, feedText, nil)

	var whole feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &whole)
	if len(whole.Posts) < 4 {
		t.Fatalf("the fixture deployment produced %d posts; the test needs several", len(whole.Posts))
	}
	if whole.Total != len(whole.Posts) {
		t.Fatalf("total = %d against %d rows", whole.Total, len(whole.Posts))
	}

	var second feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=2&offset=2"), &second)
	if second.Total != whole.Total {
		t.Errorf("total = %d on an offset page, want the size of the eligible set (%d)",
			second.Total, whole.Total)
	}
	if len(second.Posts) != 2 {
		t.Fatalf("offset page = %d rows, want 2", len(second.Posts))
	}
	for i, post := range second.Posts {
		if post.ID != whole.Posts[i+2].ID {
			t.Errorf("row %d of the offset page is %s, want %s: the page was ranked on its own",
				i, post.ID, whole.Posts[i+2].ID)
		}
	}
	// Newest first, which is the one order a reader can check by eye.
	for i := 1; i < len(whole.Posts); i++ {
		if whole.Posts[i-1].CreatedAt < whole.Posts[i].CreatedAt {
			t.Fatalf("new is not newest-first at row %d: %q before %q",
				i, whole.Posts[i-1].CreatedAt, whole.Posts[i].CreatedAt)
		}
	}
	if whole.BuiltAt == "" {
		t.Error("the feed does not say when it was built")
	}
}

// TestTheFeedFilesRecordsUnderTheTopicsTheirEvidenceCameFrom is §8.7's second
// paragraph: a topic is a community, and it is where the evidence came from
// rather than anything the record says about itself.
//
// The propagation is what is being asserted. Only the observation cites a
// session; the candidate it develops, the finding that consolidates it and the
// proposal that rests on that finding all inherit the topic through the
// development path, which is the whole reason a proposal has a community at
// all.
func TestTheFeedFilesRecordsUnderTheTopicsTheirEvidenceCameFrom(t *testing.T) {
	h := newPhaseB(t, feedText, withCitedWorkspace)

	var filed feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+feedTopic+"&limit=100"), &filed)
	got := map[string]bool{}
	for _, post := range filed.Posts {
		got[post.ID] = true
		if !contains(post.Topics, feedTopic) {
			t.Errorf("%s is in the topic feed with topics %v", post.ID, post.Topics)
		}
	}
	for _, want := range []string{h.hypothesis.ID, h.finding.ID, h.proposal.ID, h.observationID(t)} {
		if !got[want] {
			t.Errorf("%s is not filed under %s; the lineage did not propagate", want, feedTopic)
		}
	}

	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	var counted topicCount
	for _, topic := range topics.Topics {
		if topic.Name == feedTopic {
			counted = topic
		}
	}
	if counted.Posts != len(filed.Posts) {
		t.Errorf("the sidebar counts %d posts under %s and the feed shows %d",
			counted.Posts, feedTopic, len(filed.Posts))
	}
	if counted.LatestAt == "" {
		t.Errorf("topic = %+v, want the newest post's time", counted)
	}
	// A record whose origin this deployment cannot resolve is in the feed
	// without a topic rather than hidden, and the sidebar says how many.
	if topics.Unfiled == 0 {
		t.Error("no post is unfiled, so the reserved topic asserts nothing")
	}
	var unfiled feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+topicUnfiled+"&limit=100"), &unfiled)
	if unfiled.Total != topics.Unfiled {
		t.Errorf("the unfiled feed holds %d and the sidebar counts %d", unfiled.Total, topics.Unfiled)
	}
	for _, post := range unfiled.Posts {
		if len(post.Topics) != 0 {
			t.Errorf("%s is in the unfiled feed with topics %v", post.ID, post.Topics)
		}
	}
}

// TestTheScoreCountsTheOperatorAmongBabelsReviewers is §8.7's arithmetic and
// §4.12's boundary in one assertion.
//
// The number has to be one number — "the score is support minus oppose across
// all of them, because a vote is a vote and the operator is one voter among
// Babel's reviewers" — and the breakdown has to keep him separable, which is
// what `you` is for. A feed that summed them without saying which was whose
// would make a person's click indistinguishable from a model's observation.
func TestTheFeedScoreCountsTheOperatorAmongBabelsReviewers(t *testing.T) {
	subject := evaluation.Subject{Kind: "proposal", ID: "pro_scored"}
	fake := &fakeEvaluation{tallies: map[evaluation.Subject]evaluation.Tally{
		subject: {Support: 3, Oppose: 1, Unsure: 1, Stance: evaluation.StanceDisagree},
	}}
	h := newPhaseB(t, feedText, func(o *Options) { o.Evaluation = fake })

	// The projection is asserted directly, because the fixture frontier
	// holds no record with that identifier and the arithmetic is what is
	// under test.
	var entry feedEntry
	applyTally(&entry, fake.tallies[subject], time.Now().UTC())
	if entry.post.Support != 3 || entry.post.Oppose != 2 || entry.post.Unsure != 1 {
		t.Fatalf("breakdown = %+v, want the operator's disagreement counted", entry.post)
	}
	if entry.post.Score != entry.post.Support-entry.post.Oppose {
		t.Errorf("score %d is not support minus oppose", entry.post.Score)
	}
	if entry.post.You != evaluation.StanceDisagree {
		t.Errorf("you = %q, want the operator's own stance kept separable", entry.post.You)
	}

	// And a real row carries the same identity, so a client can check it.
	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	for _, post := range feed.Posts {
		if post.Score != post.Support-post.Oppose {
			t.Fatalf("row %s: score %d is not support minus oppose", post.ID, post.Score)
		}
	}
}

// TestTheCommentThreadKeepsRulingsOutOfTheConversation is §8.7's fourth
// paragraph.
//
// A ruling is the moderator's log and renders as the act it is; a reader who
// could not tell an accept from an opinion would read Babel's decision history
// as somebody's argument. The two travel in separate lists so that no client
// can flatten them by accident.
func TestTheCommentThreadKeepsRulingsOutOfTheConversation(t *testing.T) {
	var service *evaluation.Service
	h := newPhaseB(t, feedText, func(o *Options) {
		service = realEvaluation(t, o.Frontier.(*frontier.Store))
		o.Evaluation = service
	})
	if _, err := h.review.Decide(h.ctx, review.Decision{
		Subject:     frontier.Ref{Type: frontier.EntityProposal, ID: h.proposal.ID},
		Disposition: frontier.DispositionDefer,
		By:          h.authority,
		Note:        "not now; the corpus is three sessions wide " + feedText,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	path := "/api/record/" + h.proposal.ID + "/comments"

	// The operator says something, which is a feedback record carrying a
	// reason and no polarity change.
	var posted commentResult
	response := h.post(path, `{"text":"the verification criterion is the part I care about"}`)
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		t.Fatalf("POST %s: status = %d", path, response.StatusCode)
	}
	decodeResponse(t, response, &posted)
	if posted.Comment.Kind != commentReason || posted.Comment.Author.Kind != evaluation.ActorOperator {
		t.Fatalf("comment = %+v", posted.Comment)
	}

	var thread commentThread
	decodeResponse(t, h.ok(t, path), &thread)
	if thread.Total != 1 || len(thread.Comments) != 1 {
		t.Fatalf("thread = %+v, want the one thing he said", thread)
	}
	if !strings.Contains(thread.Comments[0].Text, "verification criterion") {
		t.Errorf("comment = %q, want his words kept verbatim", thread.Comments[0].Text)
	}
	if len(thread.Acts) != 1 || thread.Acts[0].Act != string(frontier.DispositionDefer) {
		t.Fatalf("acts = %+v, want the deferral as an act", thread.Acts)
	}
	if thread.Acts[0].By != operatorID {
		t.Errorf("act by %q, want the operator who recorded it", thread.Acts[0].By)
	}
	for _, comment := range thread.Comments {
		if comment.Kind == string(frontier.DispositionDefer) {
			t.Error("a ruling rendered as a comment")
		}
	}

	// The stance is untouched by the comment, which is what "no polarity
	// change" means and the one thing a reader would not forgive being
	// wrong: a box he typed into must not vote for him.
	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.proposal.ID), &peel)
	if peel.Reception != nil && peel.Reception.Operator != nil {
		t.Errorf("commenting recorded a stance: %+v", peel.Reception.Operator)
	}
}

// TestACommentWithNothingInItIsRefused is the empty-click rule internal/
// evaluation already keeps, asserted at the route that could have widened it.
//
// An empty feedback record is a click with no content, and a surface that
// stored one would be inflating attention: the record would say the operator
// engaged with a proposal he only scrolled past.
func TestACommentWithNothingInItIsRefused(t *testing.T) {
	var service *evaluation.Service
	h := newPhaseB(t, feedText, func(o *Options) {
		service = realEvaluation(t, o.Frontier.(*frontier.Store))
		o.Evaluation = service
	})
	path := "/api/record/" + h.proposal.ID + "/comments"

	for _, tc := range []struct{ name, body string }{
		{"nothing at all", `{"text":""}`},
		{"whitespace", `{"text":"   \n  "}`},
		{"no field", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := h.post(path, tc.body)
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
		})
	}

	var thread commentThread
	decodeResponse(t, h.ok(t, path), &thread)
	if thread.Total != 0 {
		t.Fatalf("a refused comment was recorded anyway: %+v", thread.Comments)
	}
}

// TestTheCommentThreadCarriesAnswersUnderAQuestion is §8.7's "the answer to a
// question" clause.
//
// A question is a post like any other and the operator's answer to it is what
// somebody said underneath — kept verbatim, attributed, and reached from the
// same route the records use, so a client renders one thread rather than two.
func TestTheCommentThreadCarriesAnswersUnderAQuestion(t *testing.T) {
	h := newPhaseB(t, feedText, nil)

	// The fixture's answered question rather than its open one: what is
	// under test is that an answer renders as a comment, and the open
	// question is the one nobody has answered yet.
	var thread commentThread
	decodeResponse(t, h.ok(t, "/api/record/"+h.answer.QuestionID+"/comments"), &thread)
	if thread.Total == 0 {
		t.Fatalf("the question carries no answers: %+v", thread)
	}
	if thread.Comments[0].Kind != commentAnswer {
		t.Errorf("comment kind = %q, want an answer", thread.Comments[0].Kind)
	}
	if thread.Comments[0].Author.ID == "" {
		t.Error("the answer is unattributed")
	}
	// A question has no ruling history: §4.7's dispositions are about
	// records, and an answered question was answered rather than accepted.
	if len(thread.Acts) != 0 {
		t.Errorf("acts = %+v, want none on a question", thread.Acts)
	}
}

// TestTheThreadNestsARefinementUnderWhatItRevises is the threading rule.
//
// §8.7 says comments are "threaded by what they relate to", and a correction
// is the case that makes it mean something: a reviewer revising his own
// contribution is answering himself, and a flat list would show the two
// wordings as two independent opinions.
func TestTheCommentThreadNestsARefinementUnderWhatItRevises(t *testing.T) {
	subject := evaluation.Subject{Kind: "proposal", ID: "pro_threaded"}
	original := evaluation.Record{
		ID: "evr_first", Kind: evaluation.KindAssessment, Subject: subject,
		ActorKind: evaluation.ActorRun, ActorID: "run-a",
		CreatedAt: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		Assessment: &evaluation.Assessment{Vote: evaluation.VoteSupport, Contributions: []evaluation.Contribution{
			{Kind: evaluation.ContributionComment, Text: "the scope reads wider than the evidence"},
		}},
	}
	correction := evaluation.Record{
		ID: "evr_second", Kind: evaluation.KindAssessment, Subject: subject,
		ActorKind: evaluation.ActorRun, ActorID: "run-a", SupersedesID: "evr_first",
		CreatedAt: time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC),
		Assessment: &evaluation.Assessment{Vote: evaluation.VoteSupport, Contributions: []evaluation.Contribution{
			{Kind: evaluation.ContributionRefinement, Text: "narrowing it to the deploy step would fix that"},
		}},
	}
	fake := &fakeEvaluation{thread: map[evaluation.Subject][]evaluation.ThreadRecord{
		subject: {
			{Record: original, Role: evaluation.RoleEvidence},
			{Record: correction, Role: evaluation.RoleEvidence},
		},
	}}
	h := newPhaseB(t, feedText, func(o *Options) { o.Evaluation = fake })

	var thread commentThread
	decodeResponse(t, h.ok(t, "/api/record/pro_threaded/comments"), &thread)
	if thread.Total != 2 {
		t.Fatalf("thread = %+v, want both wordings", thread)
	}
	if len(thread.Comments) != 1 {
		t.Fatalf("top level = %d comments, want the original with its revision nested", len(thread.Comments))
	}
	root := thread.Comments[0]
	if root.Kind != commentContribution || root.Role != evaluation.RoleEvidence {
		t.Errorf("root = %+v, want the contribution with the role its grant authorized", root)
	}
	if root.Author.Kind != evaluation.ActorRun || root.Author.Href != "/watch/runs/run-a" {
		t.Errorf("author = %+v, want the run's name reaching its run page", root.Author)
	}
	if len(root.Replies) != 1 || root.Replies[0].Kind != commentRefinement {
		t.Fatalf("replies = %+v, want the refinement nested under what it revises", root.Replies)
	}
	// A bare vote is not a comment: the score already carries it, and an
	// empty row in the conversation says nothing.
	for _, comment := range append([]commentView{root}, root.Replies...) {
		if strings.TrimSpace(comment.Text) == "" {
			t.Error("a vote with no prose rendered as a comment")
		}
	}
}

// withCitedWorkspace gives the harness a session catalog that holds the
// conversation the fixture observation cites, so the topic the record is filed
// under is a fact this host resolved rather than a constant the test supplied.
func withCitedWorkspace(o *Options) {
	workspace := feedWorkspace
	remote := feedRepository
	identity := "/home/operator/checkouts/tyrode-infra/.git"
	title := "the deploy step " + feedText
	o.Lister = SessionListerFunc(func(context.Context) (SessionsResult, error) {
		return SessionsResult{Sessions: []SessionRow{{
			Harness:            "omp",
			SourceID:           "session-a",
			Selector:           "omp/session-a",
			Title:              &title,
			Workspace:          &workspace,
			RepositoryIdentity: &identity,
			RepositoryRemote:   &remote,
		}}}, nil
	})
}

// entryAt is one synthetic post with a time and a score, for the rank tests.
func entryAt(id string, at time.Time, score int) feedEntry {
	return feedEntry{
		post:      feedPost{ID: id, Kind: "proposal", Title: id, Score: score, CreatedAt: timeText(at)},
		createdAt: at,
	}
}

func ids(entries []feedEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.post.ID)
	}
	return out
}
