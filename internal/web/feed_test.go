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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/run"
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

	within := filterFeed([]feedEntry{inside, outside}, "", nil, sortTop, "day", false, now)
	if len(within) != 1 || within[0].post.ID != "pro_inside" {
		t.Fatalf("top over a day = %v, want only the post inside it", ids(within))
	}
	// The same pair over all time keeps both, which is what makes the
	// exclusion above the window's work rather than a broken fixture.
	if all := filterFeed([]feedEntry{inside, outside}, "", nil, sortTop, windowAll, false, now); len(all) != 2 {
		t.Fatalf("top over all time = %v, want both posts", ids(all))
	}
	// The window is top's and controversial's alone. A hot feed narrowed to
	// a day would be the same list with its older half deleted, and a
	// reader who never chose a period would be given one.
	if hot := filterFeed([]feedEntry{inside, outside}, "", nil, sortHot, "day", false, now); len(hot) != 2 {
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
	rising := filterFeed([]feedEntry{silent, loud}, "", nil, sortRising, windowAll, false, now)
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

// TestTheFeedIsEveryKindOfRecordThatIsAPost is §8.7's first sentence as
// §4.13's last reading leaves it.
//
// The observation is the one worth naming, and it is named by its absence:
// *observations are evidence, not posts* (operator direction 2026-09-12), so
// the kind a run produces most of is reachable from the records that cite it
// and is not a row here. Asking for it by name is refused like any other kind
// this feed does not have, which is what stops "it is filtered out" from
// being indistinguishable from "this deployment produced none".
func TestTheFeedIsEveryKindOfRecordThatIsAPost(t *testing.T) {
	h := newPhaseB(t, feedText, nil)

	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	kinds := map[string]bool{}
	for _, post := range feed.Posts {
		kinds[post.Kind] = true
	}
	for _, want := range []string{
		string(frontier.EntityHypothesis), string(frontier.EntityFinding),
		string(frontier.EntityProposal), feedKindQuestion,
	} {
		if !kinds[want] {
			t.Errorf("the feed carries no %s; it has %v", want, kinds)
		}
	}
	if kinds[string(frontier.EntityObservation)] {
		t.Errorf("the feed lists observations; they are evidence rather than posts")
	}
	refused := h.get("/api/feed?kind=observation")
	text := body(t, refused)
	if refused.StatusCode != http.StatusBadRequest || !strings.Contains(text, "observation") {
		t.Errorf("asking for observations: status = %d body %q, want a 400 naming the kind",
			refused.StatusCode, text)
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

// TestTheFeedShowsOnlyTheTopicsSomebodyFiled is §4.13's correction to §8.7's
// first topics, asserted where a reader meets it.
//
// Stage 1 filed a record under the repository its evidence came from, which
// made a name Babel derived look exactly like a community somebody agreed to.
// It does not any more: a post's topics are the ledger entities it has been
// filed under, so a deployment whose repositories nobody has accepted shows
// the records unfiled and the repositories as proposals. The fixture here has
// a cited workspace precisely so that the derivation has something to offer
// and the feed still refuses to file on it.
func TestTheFeedShowsOnlyTheTopicsSomebodyFiled(t *testing.T) {
	h := newPhaseB(t, feedText, withCitedWorkspace)

	var derived feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+feedTopic+"&limit=100"), &derived)
	if derived.Total != 0 {
		t.Errorf("%d posts are filed under the repository name %s that nobody accepted",
			derived.Total, feedTopic)
	}

	// What is filed is what the operator filed: the harness's own filing of
	// the finding under the ledger entity.
	var topics topicList
	decodeResponse(t, h.ok(t, "/api/topics"), &topics)
	var counted topicRow
	for _, topic := range topics.Topics {
		if topic.ID == h.entity.ID {
			counted = topic
		}
		if topic.Name == feedTopic {
			t.Errorf("a repository name is a topic before anybody accepted it: %+v", topic)
		}
	}
	var filed feedList
	decodeResponse(t, h.ok(t, "/api/feed?topic="+h.entity.ID+"&limit=100"), &filed)
	if counted.Posts != filed.Total || counted.Posts == 0 {
		t.Errorf("the sidebar counts %d posts under %s and the feed shows %d",
			counted.Posts, counted.Name, filed.Total)
	}
	if counted.LatestAt == "" {
		t.Errorf("topic = %+v, want the newest post's time", counted)
	}
	// A record nothing has filed is in the feed without a topic rather than
	// hidden, and the sidebar says how many.
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

// TestTheFeedScoreIsBabelsReviewersAndNobodyElses is §8.7's "Babel votes; the
// operator rules", checked where the number is assembled.
//
// The operator's stance is recorded, durable and readable on the record page,
// and it reaches no column here: he does not vote, because his act on a
// record is a ruling and a vote beside it would be a weaker copy of it. So
// the assertion is a subtraction that does not happen — the score after he
// has said what he thinks is the score before it.
func TestTheFeedScoreIsBabelsReviewersAndNobodyElses(t *testing.T) {
	var service *evaluation.Service
	h := newPhaseB(t, feedText, func(o *Options) {
		service = realEvaluation(t, o.Frontier.(*frontier.Store))
		o.Evaluation = service
	})
	subject := evaluation.Subject{Kind: "proposal", ID: h.proposal.ID}
	if _, err := service.Operator(h.ctx, evaluation.OperatorInput{
		Subject:  subject,
		Kind:     evaluation.KindFeedback,
		Operator: operatorID,
		Stance:   evaluation.StanceDisagree,
		Reason:   "the benchmark lands first " + feedText,
	}); err != nil {
		t.Fatalf("record a stance: %v", err)
	}

	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	var scored *feedPost
	for i, post := range feed.Posts {
		if post.Score != post.Support-post.Oppose {
			t.Fatalf("row %s: score %d is not support minus oppose", post.ID, post.Score)
		}
		if post.ID == h.proposal.ID {
			scored = &feed.Posts[i]
		}
	}
	if scored == nil {
		t.Fatalf("the proposal he disagreed with is not in the feed: %d rows", len(feed.Posts))
	}
	// No run has assessed it, so Babel's reception is empty — and his
	// disagreement did not make it minus one.
	if scored.Support != 0 || scored.Oppose != 0 || scored.Unsure != 0 || scored.Score != 0 {
		t.Errorf("row = %+v, want an unscored record: the only voice on it is his", *scored)
	}
	// What he said is under the post, which is where §8.7 puts it.
	if scored.Comments != 1 {
		t.Errorf("comments = %d, want the reason he left", scored.Comments)
	}

	// The arithmetic over a reception that does exist, asserted directly:
	// the columns are the tally's and nothing is added to them.
	var entry feedEntry
	applyTally(&entry, evaluation.Tally{Support: 3, Oppose: 1, Unsure: 1}, time.Now().UTC())
	if entry.post.Support != 3 || entry.post.Oppose != 1 || entry.post.Unsure != 1 {
		t.Fatalf("breakdown = %+v, want the reviewers' own votes", entry.post)
	}
	if entry.post.Score != 2 {
		t.Errorf("score = %d, want support minus oppose", entry.post.Score)
	}
}

// TestTheQueueIsTheFeedNarrowedToWhatAwaitsHim is the direction this section
// carries out: home and the queue were the same list twice, so the queue
// became a filter over the feed.
//
// What the filter has to get right is what "awaits him" means, and the two
// halves of that are asserted against records whose standing this test moved:
// a record somebody ruled on is out, and a question nobody has answered is
// in. The rest is the property that makes the filter honest — every row it
// keeps says why it is there, and every row it drops says nothing at all
// rather than a reason nobody asked for.
func TestTheQueueIsTheFeedNarrowedToWhatAwaitsHim(t *testing.T) {
	h := newPhaseB(t, feedText, nil)
	// The ruling lands before the first read, because the index is built
	// once a minute and a test that ruled afterwards would be asserting
	// against a projection of the state before it.
	if _, err := h.review.Decide(h.ctx, review.Decision{
		Subject:     frontier.Ref{Type: frontier.EntityProposal, ID: h.proposal.ID},
		Disposition: frontier.DispositionDefer,
		By:          h.authority,
		Note:        "not now; the corpus is three sessions wide " + feedText,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	var queue feedList
	decodeResponse(t, h.ok(t, "/api/feed?needs=me&sort=next&limit=100"), &queue)
	if queue.Needs != "me" || queue.Sort != sortNext {
		t.Fatalf("the answer describes a different request: %+v", queue)
	}
	if len(queue.Posts) == 0 {
		t.Fatal("nothing awaits the operator in a deployment with four enrolled records")
	}
	for _, post := range queue.Posts {
		if post.ID == h.proposal.ID {
			t.Errorf("a deferred record is still awaiting a ruling: %+v", post)
		}
		if !post.Awaiting {
			t.Errorf("row %s is in the queue and awaits nothing", post.ID)
		}
		if post.Why == "" {
			t.Errorf("row %s awaits him and does not say why", post.ID)
		}
		if words := len(strings.Fields(strings.ReplaceAll(post.Why, " · ", " "))); words > 5 {
			t.Errorf("why = %q on %s, which is %d words; §8.7 allows five", post.Why, post.ID, words)
		}
	}
	// The blocking question the ledger asked is the case the filter exists
	// for: nothing else in the deployment can answer it.
	var asked bool
	for _, post := range queue.Posts {
		if post.ID != h.question.ID {
			continue
		}
		asked = true
		if post.Why != "blocks a run · asked now" {
			t.Errorf("why = %q on a blocking question asked a moment ago", post.Why)
		}
	}
	if !asked {
		t.Errorf("the open blocking question is not awaiting him: %v", postIDs(queue.Posts))
	}

	// And the ruled record is still in the feed — the ruling took it out of
	// the queue, not out of the deployment — carrying no reason to act.
	var whole feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=hot&limit=100"), &whole)
	var ruled *feedPost
	for i, post := range whole.Posts {
		if post.ID == h.proposal.ID {
			ruled = &whole.Posts[i]
		}
	}
	if ruled == nil {
		t.Fatalf("the deferred proposal left the feed: %v", postIDs(whole.Posts))
	}
	if ruled.Awaiting || ruled.Why != "" {
		t.Errorf("a ruled record = %+v, want awaiting false and no why", *ruled)
	}
	if ruled.Standing != string(frontier.ReviewDeferred) {
		t.Errorf("standing = %q, want the ruling that was made", ruled.Standing)
	}
	if whole.Needs != "" {
		t.Errorf("needs = %q on the whole feed, want none", whole.Needs)
	}
	// A record nobody has ruled on says so, in the two facts a row carries.
	for _, post := range whole.Posts {
		if post.ID != h.finding.ID {
			continue
		}
		if !post.Awaiting || post.Why != "never ruled on · waiting now" {
			t.Errorf("the unruled finding = %+v", post)
		}
	}

	// A vocabulary refusal, because ?needs=everyone answered with the whole
	// feed would show a reader a list he did not ask for.
	refused := h.get("/api/feed?needs=everyone")
	text := body(t, refused)
	if refused.StatusCode != http.StatusBadRequest || !strings.Contains(text, "everyone") {
		t.Fatalf("status = %d body %q, want 400 naming the value", refused.StatusCode, text)
	}
}

// TestAQueueThatCannotSeeStandingsSaysSo is the honest-absence rule at the one
// place its absence is invisible.
//
// "Awaits a ruling" is derived from where a record stands, so a machine that
// cannot derive that holds back every record from this list — and a short
// list of questions with no explanation reads as a deployment with nothing
// pending, which is the opposite of true. The feed itself is unaffected and
// says nothing extra, because none of its rows depended on the derivation.
func TestAQueueThatCannotSeeStandingsSaysSo(t *testing.T) {
	h := newPhaseB(t, feedText, func(o *Options) {
		o.Frontier = standingsUnreadable{FrontierReader: o.Frontier}
	})

	var queue feedList
	decodeResponse(t, h.ok(t, "/api/feed?needs=me&limit=100"), &queue)
	if !strings.Contains(queue.Notice, "awaiting you") {
		t.Errorf("notice = %q, want the queue to say what it could not classify", queue.Notice)
	}
	for _, post := range queue.Posts {
		if post.Kind != feedKindQuestion {
			t.Errorf("row %s survived with no standing to derive: %+v", post.ID, post)
		}
	}

	// The whole feed is the whole feed, and carries no notice it did not
	// earn: every claim, topic and count in it is unaffected.
	var whole feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &whole)
	if whole.Notice != "" {
		t.Errorf("notice = %q on the unfiltered feed, want none", whole.Notice)
	}
	if len(whole.Posts) <= len(queue.Posts) {
		t.Errorf("the feed holds %d rows and the queue %d; the records did not survive",
			len(whole.Posts), len(queue.Posts))
	}
	for _, post := range whole.Posts {
		if post.Kind == string(frontier.EntityProposal) && post.Standing != "" {
			t.Errorf("a standing was invented for %s: %q", post.ID, post.Standing)
		}
	}
}

// standingsUnreadable is a frontier whose disposition log cannot be derived,
// which is the only way to observe what the queue does without one.
type standingsUnreadable struct{ FrontierReader }

func (standingsUnreadable) ReviewStandings(context.Context) (map[frontier.Ref]frontier.ReviewStanding, error) {
	return nil, errors.New("the disposition log could not be read")
}

// TestNextOrdersByUrgencyThenKindThenTheLongestWait is §8.5's reading order,
// ported off the client that used to compute it.
//
// Four keys and one property each. Urgency first, so a finding whose ruling
// came back outranks a proposal nobody has looked at yet — the reverse of
// what the kind alone would say, which is what makes the first key a key.
// The kind at equal urgency, so a remedy addressed to the operator comes
// before a pattern Babel is still consolidating. The oldest first inside
// that, because a queue nobody drains from the bottom has a permanent bottom.
// And everything that awaits nothing after everything that does, newest
// first, so that turning the filter off keeps the same list and adds to it.
func TestNextOrdersByUrgencyThenKindThenTheLongestWait(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	blocked := awaitingAt("qst_blocking", feedKindQuestion, now.Add(-time.Minute), urgencyBlocked)
	reopened := awaitingAt("fnd_reopened", "finding", now.Add(-time.Hour), urgencyReopened)
	proposal := awaitingAt("prp_new", "proposal", now.Add(-2*time.Hour), urgencyUnruled)
	finding := awaitingAt("fnd_new", "finding", now.Add(-3*time.Hour), urgencyUnruled)
	older := awaitingAt("prp_older", "proposal", now.Add(-4*time.Hour), urgencyUnruled)
	quiet := entryAt("prp_ruled", now, 0)

	posts := []feedEntry{quiet, finding, proposal, reopened, older, blocked}
	sortFeed(posts, sortNext, now)
	want := []string{"qst_blocking", "fnd_reopened", "prp_older", "prp_new", "fnd_new", "prp_ruled"}
	if got := ids(posts); !equalIDs(got, want) {
		t.Fatalf("next ordered %v, want %v", got, want)
	}
}

// TestWhyIsBuiltFromThePostsOwnFactsAndIsEmptyOtherwise is the sentence's
// contract: it explains a wait, and a post nobody is waiting on has nothing
// to explain.
//
// A why on a settled record would be this surface narrating a queue position
// that does not exist, which is exactly the invented urgency §4.12 refuses to
// manufacture.
func TestWhyIsBuiltFromThePostsOwnFactsAndIsEmptyOtherwise(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		standing string
		age      time.Duration
		awaiting bool
		why      string
		urgency  int
	}{
		{"never ruled on", string(frontier.ReviewNew), 3 * 24 * time.Hour, true,
			"never ruled on · waiting 3d", urgencyUnruled},
		{"reopened", standingReopened, 90 * time.Minute, true, "reopened · waiting 1h", urgencyReopened},
		{"accepted", string(frontier.ReviewAccepted), time.Hour, false, "", urgencyNone},
		{"deferred", string(frontier.ReviewDeferred), time.Hour, false, "", urgencyNone},
		{"refine requested", string(frontier.ReviewRefineRequested), time.Hour, false, "", urgencyNone},
		{"unreviewable", "", time.Hour, false, "", urgencyNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entry feedEntry
			awaitRecord(&entry, tc.standing, now.Add(-tc.age), now)
			if entry.post.Awaiting != tc.awaiting {
				t.Errorf("awaiting = %v, want %v", entry.post.Awaiting, tc.awaiting)
			}
			if entry.post.Why != tc.why {
				t.Errorf("why = %q, want %q", entry.post.Why, tc.why)
			}
			if entry.urgency != tc.urgency {
				t.Errorf("urgency = %d, want %d", entry.urgency, tc.urgency)
			}
		})
	}

	// The three question states that await him, and the one that does not.
	for _, tc := range []struct {
		name     string
		state    reality.QuestionState
		class    reality.QuestionClass
		awaiting bool
		why      string
		urgency  int
	}{
		{"open and blocking", reality.QuestionOpen, reality.ClassBlocking, true,
			"blocks a run · asked 2h", urgencyBlocked},
		{"open upkeep", reality.QuestionOpen, reality.ClassMaintenance, true,
			"upkeep · asked 2h", urgencyAsked},
		{"open curiosity", reality.QuestionOpen, reality.ClassCuriosity, true,
			"curiosity · asked 2h", urgencyAsked},
		{"answered but not interpreted", reality.QuestionAnsweredUninterpreted, reality.ClassMaintenance,
			true, "no plan yet · asked 2h", urgencyAsked},
		{"a plan waiting for him", reality.QuestionPlanReady, reality.ClassMaintenance, true,
			"plan ready · asked 2h", urgencyAsked},
		{"being interpreted", reality.QuestionInterpreting, reality.ClassBlocking, false, "", urgencyNone},
		{"snoozed", reality.QuestionSnoozed, reality.ClassBlocking, false, "", urgencyNone},
		{"answered", reality.QuestionAnswered, reality.ClassBlocking, false, "", urgencyNone},
		{"declined", reality.QuestionDeclined, reality.ClassBlocking, false, "", urgencyNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var entry feedEntry
			awaitQuestion(&entry, reality.Question{
				State: tc.state, Class: tc.class, CreatedAt: now.Add(-2 * time.Hour),
			}, now)
			if entry.post.Awaiting != tc.awaiting {
				t.Errorf("awaiting = %v, want %v", entry.post.Awaiting, tc.awaiting)
			}
			if entry.post.Why != tc.why {
				t.Errorf("why = %q, want %q", entry.post.Why, tc.why)
			}
			if entry.urgency != tc.urgency {
				t.Errorf("urgency = %d, want %d", entry.urgency, tc.urgency)
			}
		})
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

// TestAskingBabelSomethingIsAQuestionInTheThread is §8.7's fifth act: "ask,
// which is a question to Babel about this record, recorded as a comment
// Babel's next review of the record must answer".
//
// The distinction is the whole test. A question and a comment are the same
// family — his prose, verbatim, deciding nothing — so what has to be true is
// that the thread can still tell them apart: a review that could not find
// what it owes an answer to would leave the operator asking into a log.
func TestAskingBabelSomethingIsAQuestionInTheThread(t *testing.T) {
	h := newPhaseB(t, feedText, func(o *Options) {
		o.Evaluation = realEvaluation(t, o.Frontier.(*frontier.Store))
	})
	path := "/api/record/" + h.proposal.ID + "/comments"

	var asked commentResult
	question := h.post(path, `{"text":"what would this cost on the whole corpus?","kind":"question"}`)
	if question.StatusCode != http.StatusCreated {
		defer question.Body.Close()
		t.Fatalf("POST %s: status = %d", path, question.StatusCode)
	}
	decodeResponse(t, question, &asked)
	if asked.Comment.Kind != commentQuestion {
		t.Fatalf("the answer calls it a %q, want a question", asked.Comment.Kind)
	}

	var said commentResult
	comment := h.post(path, `{"text":"the verification criterion is the part I care about"}`)
	if comment.StatusCode != http.StatusCreated {
		defer comment.Body.Close()
		t.Fatalf("POST %s: status = %d", path, comment.StatusCode)
	}
	decodeResponse(t, comment, &said)
	if said.Comment.Kind != commentReason {
		t.Errorf("a comment with no kind reads as %q, want the plain reason", said.Comment.Kind)
	}

	var thread commentThread
	decodeResponse(t, h.ok(t, path), &thread)
	if thread.Total != 2 {
		t.Fatalf("thread = %+v, want the two things he wrote", thread.Comments)
	}
	kinds := map[string]string{}
	for _, comment := range thread.Comments {
		kinds[comment.Kind] = comment.Text
	}
	if kinds[commentQuestion] != "what would this cost on the whole corpus?" {
		t.Errorf("the question reads %q in the thread", kinds[commentQuestion])
	}
	if kinds[commentReason] != "the verification criterion is the part I care about" {
		t.Errorf("the comment reads %q in the thread", kinds[commentReason])
	}

	// A kind nothing records is refused by name rather than stored as a
	// comment, because a question filed as an opinion is never answered.
	refused := h.post(path, `{"text":"is this still true?","kind":"complaint"}`)
	text := body(t, refused)
	if refused.StatusCode != http.StatusBadRequest || !strings.Contains(text, "complaint") {
		t.Fatalf("status = %d body %q, want 400 naming the value", refused.StatusCode, text)
	}
	// And an empty question is refused as a question, not as a comment: the
	// refusal names the act he was performing.
	empty := h.post(path, `{"text":"  ","kind":"question"}`)
	emptyText := body(t, empty)
	if empty.StatusCode != http.StatusBadRequest || !strings.Contains(emptyText, "question") {
		t.Fatalf("status = %d body %q, want 400 about an empty question", empty.StatusCode, emptyText)
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

// TestTheFeedMarksWhatBabelArguedOverAndWhatItIsReadingNow is the two facts
// §8.7's front page carries that the four vote columns cannot.
//
// They are asserted together because they fail the same way: both are a bit
// attached to one subject, and the plausible bug is that the bit lands on
// every row, or on the wrong one, because the lookup keyed on something the
// rows share. So the fixture puts each mark on a different record and the
// assertion is that every other row carries neither.
//
// Contested is the one that earns its place beside the score. A four-four
// split and a four-nil agreement both score the same number, and the
// difference between "Babel argued about this" and "Babel agreed about this"
// is exactly what a reader is choosing between.
func TestTheFeedMarksWhatBabelArguedOverAndWhatItIsReadingNow(t *testing.T) {
	started := time.Now().UTC()
	h := newPhaseB(t, feedText, nil)
	fake := h.server.opts.Evaluation.(*fakeEvaluation)
	claimedAt := started.Add(-90 * time.Second)
	fake.tallies = map[evaluation.Subject]evaluation.Tally{
		{Kind: string(frontier.EntityProposal), ID: h.proposal.ID}: {
			Support: 2, Oppose: 2, Contested: true,
		},
		{Kind: string(frontier.EntityFinding), ID: h.finding.ID}: {Support: 4},
	}
	fake.claims = map[evaluation.Subject]evaluation.OpenClaim{
		{Kind: string(frontier.EntityFinding), ID: h.finding.ID}: {Count: 1, Since: claimedAt},
	}

	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	var argued, reading *feedPost
	for i, post := range feed.Posts {
		switch post.ID {
		case h.proposal.ID:
			argued = &feed.Posts[i]
		case h.finding.ID:
			reading = &feed.Posts[i]
		default:
			if post.Contested || post.Reviewing {
				t.Errorf("row %s carries a mark nothing was recorded against it: %+v", post.ID, post)
			}
		}
	}
	if argued == nil || reading == nil {
		t.Fatalf("the two marked records are not in the feed: %v", postIDs(feed.Posts))
	}
	if !argued.Contested || argued.Reviewing {
		t.Errorf("the split proposal = %+v, want contested and not under review", *argued)
	}
	if reading.Contested || !reading.Reviewing {
		t.Errorf("the claimed finding = %+v, want under review and not contested", *reading)
	}
	// The scores are why the marks exist: an even split reads as zero and a
	// one-sided reception reads as four, and neither number says whether
	// anybody disagreed or whether anybody is still reading.
	if argued.Score != 0 || reading.Score != 4 {
		t.Errorf("scores = %d and %d, want the split at zero and the agreement at four",
			argued.Score, reading.Score)
	}

	// The claim ends — finished or lapsed, which internal/evaluation makes
	// one absence — and the next build stops saying the record is being
	// read. The disagreement is durable and stays.
	fake.claims = nil
	h.server.invalidateFeed()
	var after feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &after)
	for _, post := range after.Posts {
		if post.Reviewing {
			t.Errorf("row %s is still under review after the claim ended: %+v", post.ID, post)
		}
		if post.ID == h.proposal.ID && !post.Contested {
			t.Errorf("the split proposal stopped being contested when a claim elsewhere ended: %+v", post)
		}
	}
	// The build asked what was open at its own instant. A lapse judged
	// against a clock this process never read would leave every abandoned
	// review glowing for the life of the deployment.
	if fake.lastClaimAt.Before(started) || fake.lastClaimAt.After(time.Now().UTC()) {
		t.Errorf("the build asked for the claims open at %s, which is not an instant it lived through",
			fake.lastClaimAt)
	}
}

// TestThePulseCountsTodayAndNamesWhatIsUnderReviewNow is the front page's live
// signal held to the two things it claims: the numbers are today's, and the
// list is now's.
//
// "Today" is the assertion that needs a fixture rather than a reading, because
// a count with no window is satisfied by every number the store holds. So the
// deployment is given another day's work in every source the pulse reads — a
// hundred and thirty-nine records, forty votes, twenty-five rulings — and the
// answer has to contain none of it.
func TestThePulseCountsTodayAndNamesWhatIsUnderReviewNow(t *testing.T) {
	now := time.Now().UTC()
	dayStart := now.Truncate(24 * time.Hour)
	otherDay := dayStart.AddDate(0, 0, -1)
	claimedAt := now.Add(-3 * time.Minute)
	h := newPhaseB(t, feedText, func(o *Options) {
		o.Frontier = anotherDaysWork{FrontierReader: o.Frontier, day: otherDay}
		// Newest first, which is the order the store answers in and the
		// order the scan stops on. Two runs today share one conversation
		// and one of them read a second; yesterday's run read a third.
		o.Receipts = &fakeReceipts{receipts: []run.Receipt{
			readingReceipt("run_today_late", now, "session-a"),
			readingReceipt("run_today_early", dayStart.Add(time.Second), "session-a", "session-b"),
			readingReceipt("run_yesterday", otherDay.Add(time.Hour), "session-c"),
		}}
		fake := o.Evaluation.(*fakeEvaluation)
		fake.assessmentDays = []evaluation.AssessmentDay{
			{Day: otherDay.Format(time.DateOnly), Count: 40},
			{Day: dayStart.Format(time.DateOnly), Count: 3},
		}
	})
	fake := h.server.opts.Evaluation.(*fakeEvaluation)
	fake.claims = map[evaluation.Subject]evaluation.OpenClaim{
		{Kind: string(frontier.EntityFinding), ID: h.finding.ID}: {Count: 1, Since: claimedAt},
	}
	// A second topic plan, on another host's proposal from March. It is a
	// plan nobody has ruled on, exactly like the fixture's own, and the only
	// thing that keeps it out of today's count is the date of the proposal
	// it explains.
	plans := h.server.opts.TopicPlans.(*topicPlans)
	plans.plans = append(plans.plans, TopicPlanView{
		ProposalID: "frec-remote-proposal",
		Operation:  "create",
		Name:       "the march project " + feedText,
		Kind:       "repository",
		RunID:      "frun-remote",
		Why:        "it was proposed in March " + feedText,
	})
	// One ruling today, so "today's rulings" is a number the fixture
	// actually produced rather than a zero that would pass the
	// only-today assertion by holding nothing at all.
	if _, err := h.review.Decide(h.ctx, review.Decision{
		Subject:     frontier.Ref{Type: frontier.EntityProposal, ID: h.proposal.ID},
		Disposition: frontier.DispositionDefer,
		By:          h.authority,
		Note:        "not until the benchmark lands " + feedText,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	var feed feedList
	decodeResponse(t, h.ok(t, "/api/feed?sort=new&limit=100"), &feed)
	if !contains(postIDs(feed.Posts), "frec-remote-proposal") {
		t.Fatalf("the March proposal is not in the feed, so the older plan is excluded for the "+
			"wrong reason: %v", postIDs(feed.Posts))
	}

	var pulse feedPulse
	decodeResponse(t, h.ok(t, "/api/feed/pulse"), &pulse)
	if want := timeText(dayStart); pulse.Since != want {
		t.Errorf("since = %q, want the start of the day the counts are over (%s)", pulse.Since, want)
	}
	if pulse.Today.Votes != 3 {
		t.Errorf("votes = %d, want today's three rather than yesterday's forty", pulse.Today.Votes)
	}
	if pulse.Today.Proposals != 1 {
		t.Errorf("proposals = %d, want the one this deployment wrote today", pulse.Today.Proposals)
	}
	if pulse.Today.Records < 4 || pulse.Today.Records >= otherDayRecords {
		t.Errorf("records = %d, want the handful written today and none of yesterday's %d",
			pulse.Today.Records, otherDayRecords)
	}
	if pulse.Today.Ruled < 1 || pulse.Today.Ruled >= otherDayRulings {
		t.Errorf("ruled = %d, want today's rulings and none of yesterday's %d",
			pulse.Today.Ruled, otherDayRulings)
	}
	// Two conversations, not three reads of them: one run read both and the
	// other re-read one, and yesterday's third is outside the window.
	if pulse.Today.SessionsRead != 2 {
		t.Errorf("sessions read = %d, want the two distinct conversations today's runs opened",
			pulse.Today.SessionsRead)
	}
	if pulse.Today.TopicProposals != 1 {
		t.Errorf("topic proposals = %d, want the one published today and not the March one",
			pulse.Today.TopicProposals)
	}

	// What is under review right now, by the record's own line.
	if len(pulse.Reviewing) != 1 {
		t.Fatalf("reviewing = %+v, want the one record under claim", pulse.Reviewing)
	}
	row := pulse.Reviewing[0]
	if row.ID != h.finding.ID || row.Kind != string(frontier.EntityFinding) {
		t.Errorf("reviewing row = %+v, want the claimed finding", row)
	}
	if row.Title != boundedLine(h.finding.Payload.Title) {
		t.Errorf("title = %q, want the record's own line", row.Title)
	}
	if row.Since != timeText(claimedAt) {
		t.Errorf("since = %q, want when the claim was granted (%s)", row.Since, timeText(claimedAt))
	}

	// And the counts move with the day's work: one more proposal written
	// and one more record ruled on, each moving exactly its own number.
	if _, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-1",
		FindingIDs: []string{h.finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          "measure the deploy step " + feedText,
			Problem:        "the step is not measured " + feedText,
			Outcome:        "measure it " + feedText,
			Uncertainty:    "one corpus " + feedText,
			Impact:         frontier.ImpactModerate,
			Classification: frontier.ClassificationPrivate,
		},
	}); err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	if _, err := h.review.Decide(h.ctx, review.Decision{
		Subject:     frontier.Ref{Type: frontier.EntityFinding, ID: h.finding.ID},
		Disposition: frontier.DispositionAccept,
		By:          h.authority,
		Note:        "the pattern holds " + feedText,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	h.server.invalidateFeed()
	var moved feedPulse
	decodeResponse(t, h.ok(t, "/api/feed/pulse"), &moved)
	if moved.Today.Records != pulse.Today.Records+1 || moved.Today.Proposals != pulse.Today.Proposals+1 {
		t.Errorf("after one proposal: records %d -> %d, proposals %d -> %d; want one more of each",
			pulse.Today.Records, moved.Today.Records, pulse.Today.Proposals, moved.Today.Proposals)
	}
	if moved.Today.Ruled != pulse.Today.Ruled+1 {
		t.Errorf("ruled = %d after one more ruling, want %d", moved.Today.Ruled, pulse.Today.Ruled+1)
	}
}

// The work another day holds, which is what "today" has to exclude. The
// numbers are large enough that a pulse containing any of them is obvious in
// the failure message rather than arithmetically close to the right answer.
const (
	otherDayRecords = 99
	otherDayRulings = 25
)

// anotherDaysWork is the frontier with a previous day's output added to the
// two reads the pulse counts from.
//
// It wraps the real store rather than replacing it, because the assertion is
// about the boundary between two days rather than about the counts in
// isolation: today's rows have to be the fixture's own, written by the real
// frontier, or "only today" is satisfied by a fixture that holds nothing else.
type anotherDaysWork struct {
	FrontierReader
	day time.Time
}

func (a anotherDaysWork) RecordDays(ctx context.Context, since time.Time) ([]frontier.RecordDay, error) {
	rows, err := a.FrontierReader.RecordDays(ctx, since)
	if err != nil {
		return nil, err
	}
	day := a.day.Format(time.DateOnly)
	return append(rows,
		frontier.RecordDay{Day: day, Kind: string(frontier.EntityHypothesis), Count: otherDayRecords - 40},
		frontier.RecordDay{Day: day, Kind: string(frontier.EntityProposal), Count: 40},
	), nil
}

func (a anotherDaysWork) ReviewStandings(ctx context.Context) (
	map[frontier.Ref]frontier.ReviewStanding, error) {
	standings, err := a.FrontierReader.ReviewStandings(ctx)
	if err != nil {
		return nil, err
	}
	for i := range otherDayRulings {
		standings[frontier.Ref{Type: frontier.EntityHypothesis, ID: fmt.Sprintf("hyp_older_%d", i)}] =
			frontier.ReviewStanding{
				Status:     frontier.ReviewAccepted,
				Last:       frontier.DispositionAccept,
				RecordedAt: a.day,
			}
	}
	return standings, nil
}

// readingReceipt is one run that read the named sessions, which is what the
// pulse counts a session read from.
func readingReceipt(runID string, at time.Time, sessions ...string) run.Receipt {
	receipt := run.Receipt{
		Header: run.Header{
			ID: run.ReceiptID("rcpt-" + runID), RunID: runID, Revision: 1, RecordedAt: at,
			Authority: run.Authority{Kind: run.AuthorityOperator, Ref: "command:explore"},
		},
		Body: run.Body{Timing: run.Timing{StartedAt: at, FinishedAt: at}},
	}
	for _, session := range sessions {
		receipt.Preparation.Selection = append(receipt.Preparation.Selection, run.Selected{
			Host: hostUnderTest, Harness: "omp", SourceID: session,
		})
	}
	return receipt
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

// awaitingAt is one synthetic post that awaits the operator, for the next
// order. The why is not set, because what the order reads is the urgency and
// the kind: a sentence here would be a fixture asserting itself.
func awaitingAt(id, kind string, at time.Time, urgency int) feedEntry {
	return feedEntry{
		post: feedPost{
			ID: id, Kind: kind, Title: id, CreatedAt: timeText(at), Awaiting: true,
		},
		createdAt: at,
		urgency:   urgency,
	}
}

func ids(entries []feedEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.post.ID)
	}
	return out
}

func postIDs(posts []feedPost) []string {
	out := make([]string, 0, len(posts))
	for _, post := range posts {
		out = append(out, post.ID)
	}
	return out
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
