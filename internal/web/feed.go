package web

// The front page (SPEC.md §8.7).
//
// Operator direction 2026-09-12, after using §8.6's surface: "where can I
// simply see the upvotes/downvotes/comment, reddit-style, with the ability to
// sort by hot/new/top (then period of time)/controversial/rising?" — and, on
// being offered the choice, *all in: the feed is Babel*. Everything §8.6 says
// still holds and is the shape of a post: one record at five depths, the
// operator's voice on the record, density as a contract, no capability
// removed.
//
// Four decisions shape this file.
//
// The ranking is over the whole deployment, before paging. §8.5 is explicit:
// ordering uses the complete eligible set the projection represents, and a
// page ranked independently would make "hot" mean "hot among the twenty-five
// rows this request happened to read". So the index below holds every post,
// the sort runs over it, and the window is cut afterwards.
//
// The index is a projection with a stated freshness, not a query. Assembling
// it reads four record enumerations, one grouped pass over the evaluation
// store, one derivation of every ruling and one pass over this host's session
// catalog; doing that per request would make the front page the most
// expensive thing this process does. It is rebuilt at most once a minute,
// under one lock so a burst of readers costs one rebuild, and every response
// says when it was built.
//
// The formulas are Reddit's and they are stated here rather than in the
// client. A client holding its own copy of "hot" would be a second answer to
// what the deployment considers current, and the two would drift the first
// time either changed. What a client names is the sort; what it receives is
// the order.
//
// A topic is a name with a count, never a directory. §8.7 is deliberate that
// the concept is wider than a repository — a mailbox, an account, a tracker
// would each be a topic — so nothing here treats a topic as a path, and a
// record whose origin this deployment cannot resolve is in the feed without a
// topic rather than hidden from it.

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// feedPost is one row of the front page: one line of claim and the few facts
// §8.6's density contract admits beside it.
//
// Author is a pointer rather than an omitted field because "this record names
// no run" is an answer. A post whose author key vanished would let a client
// decide for itself what an absent author means, and the two records that
// produce one — a question the ledger asked, and a record whose provenance
// carries no run identity — are both real states rather than gaps.
type feedPost struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Standing string `json:"standing"`
	// CreatedAt and LastActivityAt are different facts and both travel: a
	// post is ordered by the first and read for the second, and a feed that
	// carried only one of them could not say that a six-month-old finding
	// was argued about this morning.
	CreatedAt string      `json:"created_at"`
	Author    *feedAuthor `json:"author"`
	Topics    []string    `json:"topics"`
	// Score is Support minus Oppose, and all four columns are Babel's
	// reviewers and only Babel's (§8.7: "the score is Babel's reception and
	// only Babel's"). The operator has no column here because he does not
	// vote: his act on a record is a ruling, and a vote beside it would be
	// a weaker copy of it. The stances he recorded before this section are
	// still readable on the record page, and are counted nowhere.
	Score    int `json:"score"`
	Support  int `json:"support"`
	Oppose   int `json:"oppose"`
	Unsure   int `json:"unsure"`
	Comments int `json:"comments"`
	// Contested reports recorded disagreement among Babel's reviewers on
	// this record's latest revision: both sides present inside one role,
	// which is the one thing the four columns above cannot say by
	// themselves. Two supports and two opposes is a split reception if the
	// four reviewers were answering one question and four reviewers
	// answering different questions if they were not, and the score reads
	// identically either way.
	//
	// It is the record page's own rule (receptionView.Contested) read out
	// of the deployment-wide tally rather than re-derived here, so a row
	// and the page it opens cannot disagree about whether Babel argued.
	Contested bool `json:"contested,omitempty"`
	// Reviewing reports that an evaluation claim on this record is open
	// right now: claimed, unfinished, and not yet lapsed. It is work in
	// flight rather than work recorded, which is why no count above can
	// stand in for it — a record being read by three reviewers and a
	// record nobody has opened have the same reception until the first
	// vote lands.
	//
	// It is refreshed with the index rather than per request, so it is at
	// most feedFreshness stale. That is the right bound for what it says:
	// a lease outlives a minute by design, so a review in flight is still
	// in flight when the next build reads it, and a review that finished
	// thirty seconds ago is a row that stops glowing a little late.
	Reviewing bool `json:"reviewing,omitempty"`
	// Awaiting says whether this post is waiting on the operator: a record
	// whose review standing invites a ruling, or a question whose state
	// awaits him. It is a fact about the post rather than a filter state,
	// so an unfiltered feed can mark the rows that need him instead of
	// making him ask for a second list to find out.
	Awaiting bool `json:"awaiting"`
	// Why is why it is next, in five words at most, and is empty for a post
	// that awaits nothing. It is built from the post's own facts — what has
	// been ruled on it, how long it has waited, what a question is holding
	// up — because a sentence assembled from anything else would be this
	// surface explaining a queue position it did not derive.
	Why            string `json:"why"`
	LastActivityAt string `json:"last_activity_at"`
	Href           string `json:"href"`
}

// feedAuthor is the run that wrote a post. §8.7: "A run is the author of what
// it wrote: its name on a post or a vote reaches its run page."
type feedAuthor struct {
	RunID string `json:"run_id"`
	Href  string `json:"href"`
}

// feedList is GET /api/feed.
//
// The request's own parameters are echoed because the front page is a
// bookmarkable state: a client that restored a sort from its own memory
// rather than from the answer would render a bar that disagrees with the rows
// under it.
type feedList struct {
	Posts []feedPost `json:"posts"`
	Total int        `json:"total"`
	Sort  string     `json:"sort"`
	T     string     `json:"t"`
	Topic string     `json:"topic"`
	Kinds []string   `json:"kinds"`
	// Needs echoes the one thing a post can need — the operator — so a
	// client renders the filter it is actually looking at. It is empty for
	// the whole feed, which is the honest answer rather than a default a
	// reader would have to know to disbelieve.
	Needs   string `json:"needs"`
	BuiltAt string `json:"built_at"`
	Notice  string `json:"notice"`
}

// topicRow is one topic in the sidebar: a Reality Ledger entity the operator
// created, what it is bound to, how much is filed under it, and where he
// stands toward it (§4.13).
//
// It is an entity and never a name, which is the whole of §4.13's correction
// to stage 1: a topic has a global id, a kind and a binding to something real,
// so a client can open its page, and two deployments talking about one
// repository are talking about one topic. The heuristic names stage 1 derived
// from repository identity are proposals now, and travel in Proposed below.
type topicRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Binding is the real thing the topic names, and is null for an entity
	// the ledger holds no binding facts about — a concept, or a repository
	// nobody has recorded a remote or a checkout for.
	Binding *topicBinding `json:"binding"`
	// Posts counts the records filed under this topic and Awaiting how many
	// of them are waiting on the operator, because a topic with forty posts
	// and nothing waiting is a different thing to open from one with three
	// posts that all need a ruling.
	Posts    int    `json:"posts"`
	Awaiting int    `json:"awaiting"`
	LatestAt string `json:"latest_at"`
	// Interest is the operator's own stance, recorded as §4.8 facts on the
	// entity and rendered here because it is what orders the list. An
	// empty state is "nothing said", which is a different answer from
	// "not now" and is shown as one.
	Interest topicInterest `json:"interest"`
}

// topicInterest is §4.13's stance: working on it, keeping an eye, not now,
// excluded — with the reason kept verbatim and the act attributed.
type topicInterest struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	At     string `json:"at"`
	By     string `json:"by"`
}

// topicProposal is one topic proposal Babel has published and nobody has
// ruled on: the plan attached to a proposal record, in the shape the rail
// renders.
//
// It is a separate list from the topics rather than a flag on one, because a
// proposal is not a topic: nothing is created until the operator accepts it,
// and the records it names are unfiled until then. A client that rendered the
// two together would show the operator a vocabulary he never agreed to, which
// is exactly what §4.13 replaced.
//
// Every act on it is a ruling on ProposalID through the ordinary review
// route. There is no accept and no decline of a topic here — §4.13's second
// reading makes a topic change an output like any other, and the shortcut on
// the rail is a shortcut to that ruling and nothing else.
type topicProposal struct {
	ProposalID string `json:"proposal_id"`
	// Title is the proposal record's own one line, so the rail row and the
	// feed row a reader opens it from say the same thing.
	Title string `json:"title"`
	// Name and Kind describe the topic a create or a split would produce,
	// and are empty for a merge and a retirement, which name nothing new.
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Operation is one of create, split, merge and retire, and Targets are
	// the topics it acts on.
	Operation string        `json:"operation"`
	Targets   []topicTarget `json:"targets"`
	// RunID is the run that wrote the proposal.
	RunID string `json:"run_id"`
	// Posts is how many of the records the plan names this deployment
	// actually holds, which is what a ruling would file here.
	Posts int `json:"posts"`
	// Why is the plan's own sentence — "32 sessions in 3 checkouts cite
	// it" — rather than this surface's paraphrase of it.
	Why string `json:"why"`
}

// topicTarget is one topic a proposal acts on, by id and by the name the
// operator reads.
type topicTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// topicList is GET /api/topics: the topics the operator has accepted, the
// ones Babel has proposed, and how much is filed under neither.
//
// Unfiled is counted rather than named, because the records in it have nothing
// in common except that nothing has said what they are about — which is
// §4.13's honest state and the triage backlog, not a bin.
type topicList struct {
	Topics   []topicRow      `json:"topics"`
	Proposed []topicProposal `json:"proposed"`
	Unfiled  int             `json:"unfiled"`
}

// The sorts §8.7 names. They are a closed set and an unknown one is refused:
// a misspelled sort answered with the default would silently show a reader a
// different order from the one he asked for.
const (
	// sortNext is §8.5's reading order: what needs the operator, most
	// urgent first, and at equal urgency a proposal before a finding before
	// a candidate before a question, oldest first inside that. It is not a
	// score and it is not Reddit's: the other five rank a corpus by how it
	// was received, and this one ranks it by what it is waiting for.
	sortNext          = "next"
	sortHot           = "hot"
	sortNew           = "new"
	sortTop           = "top"
	sortControversial = "controversial"
	sortRising        = "rising"
)

func feedSorts() []string {
	return []string{sortNext, sortHot, sortNew, sortTop, sortControversial, sortRising}
}

// feedNeedsOperator is the only value ?needs= takes. There is one person this
// deployment can be waiting on, so the parameter names him rather than
// carrying a vocabulary with one word in it.
const feedNeedsOperator = "me"

// The windows top and controversial range over. `all` is the whole corpus and
// is the only one that is not a duration.
const windowAll = "all"

// feedWindows maps each window to its width. It is a function rather than a
// map variable so the vocabulary cannot be mutated by a caller.
func feedWindow(value string) (time.Duration, bool) {
	switch value {
	case "hour":
		return time.Hour, true
	case "day":
		return 24 * time.Hour, true
	case "week":
		return 7 * 24 * time.Hour, true
	case "month":
		return 30 * 24 * time.Hour, true
	case "year":
		return 365 * 24 * time.Hour, true
	case windowAll:
		return 0, true
	}
	return 0, false
}

func feedWindows() []string {
	return []string{"hour", "day", "week", "month", "year", windowAll}
}

// The post kinds: three record kinds plus the questions Babel asks, which
// §8.7 puts in the feed beside them — a question is something the deployment
// produced and is waiting on an answer to, which is exactly what a post is.
//
// Observations are not among them (operator direction 2026-09-12, §4.13's
// last reading): *observations are evidence, not posts*. An observation is
// what a finding consolidates and what a hypothesis rests on, it carries no
// review standing and awaits nobody, and a feed that listed every one of them
// would bury the three kinds a reader acts on under the material they were
// derived from. They stay filed, searchable and reachable from the records
// that cite them; they are simply not rows here, and ?kind=observation is
// refused by name like any other kind this feed does not have.
const feedKindQuestion = "question"

func feedKinds() []string {
	return []string{
		string(frontier.EntityHypothesis),
		string(frontier.EntityFinding),
		string(frontier.EntityProposal),
		feedKindQuestion,
	}
}

// The urgency ranks sortNext groups by, before the kind decides and before
// the age does. They are the order DecidePage's queue applied on the client
// until this section folded that page into the feed, ported unchanged except
// for the lane the feed does not have: a reconsider item is not a post, and
// the nearest thing to it in one list is a record whose ruling the operator
// himself lifted.
//
// Zero is "awaiting nothing" so that the field's zero value is the safe one:
// the comparator reads urgency only for a post that awaits the operator, and
// a default of "blocked" would put every fleet record at the top of the list
// if that ever stopped being true.
const (
	urgencyNone = iota
	// urgencyBlocked is a question Babel has stopped on. It has refused to
	// guess, so nothing else here is more expensive to leave alone.
	urgencyBlocked
	// urgencyReopened is a ruling the operator lifted: he has read the
	// record before, which makes it the cheapest ruling on the list.
	urgencyReopened
	// urgencyUnruled is a record nobody has ruled on yet — the review
	// queue, which is most of what needs him.
	urgencyUnruled
	// urgencyAsked is a question that blocks nothing. §4.8 keeps it in the
	// inbox and this is the one group here that is genuinely optional.
	urgencyAsked
)

// feedKindWeight is §8.7's "a proposal before a finding before a candidate at
// equal urgency", with a question after all three.
//
// The reason is what each kind is for rather than how much it matters: a
// proposal is a remedy addressed to the operator, a finding is a pattern
// Babel has consolidated and is asking him to accept, and a candidate is
// something it is still developing on its own.
func feedKindWeight(kind string) int {
	switch kind {
	case string(frontier.EntityProposal):
		return 0
	case string(frontier.EntityFinding):
		return 1
	case string(frontier.EntityHypothesis):
		return 2
	default:
		return 3
	}
}

// topicUnfiled is the reserved topic naming the posts whose origin this
// deployment could not resolve.
//
// It is a filter value rather than only a count because the sidebar shows the
// count and a count nobody can open is a number with no page behind it. It
// cannot collide with a real topic: a workspace basename is a path element,
// and the one reserved word is checked before the names are.
const topicUnfiled = "unfiled"

// standingsUnread is what a feed narrowed to what awaits the operator says
// when this machine could not derive where its records stand. It names the
// consequence rather than the failure, on catalogUnreachable's terms: what a
// reader has to know is that records are missing from this list, not which
// query did not answer.
const standingsUnread = "where this machine's records stand could not be derived, so no record " +
	"is listed as awaiting you and Babel's questions are all that is left"

// joinNotices reports two notices as one sentence. A response carries one
// notice field because a reader reads one line, and a second fact about the
// same answer belongs beside the first rather than in place of it.
func joinNotices(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "; " + second
	}
}

// The feed's paging. Twenty-five rows is §8.6's density contract expressed in
// rows rather than pixels — about three screens at editorial measure — and a
// hundred is the ceiling a client may raise it to.
const (
	feedPageDefault = 25
	feedPageMax     = 100
)

// feedFreshness is how long one built index serves.
//
// A minute is chosen against what the index is for rather than against a
// latency target: the front page ranks a corpus that changes when a run
// commits or an operator clicks, and a reader who has just voted sees his own
// vote because the vote route answers from the durable write rather than from
// here. What a stale minute costs is a post that appeared thirty seconds ago
// being thirty seconds late to the top, which is not a cost at all.
const feedFreshness = time.Minute

// feedEpoch is the fixed origin the hot rank measures age from. It is a
// constant rather than the deployment's first record because a decay origin
// that moved would reorder the whole feed whenever the oldest record changed.
var feedEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// feedDecay is the seconds of age worth one order of magnitude of score. It
// is Reddit's 45,000 — half a day — and the number is what makes hot a feed
// rather than a leaderboard.
const feedDecay = 45000.0

// risingWindow is the recent activity a rising rank counts.
const risingWindow = 12 * time.Hour

// feedCache is the built index and the lock that makes one rebuild serve
// every reader waiting for it.
//
// One mutex held across the build is the whole single-flight: a second
// request arriving mid-rebuild waits and is then served by the index the
// first one produced, rather than starting a second pass over the same
// corpus. It is a value on the Server rather than a package variable because
// two servers in one process — which every test is — must not share a
// deployment's front page.
type feedCache struct {
	mu    sync.Mutex
	index *feedIndex
}

// feedIndex is the projection: one entry per post, plus the topics the
// deployment's filings resolve to, derived from the same pass.
type feedIndex struct {
	builtAt time.Time
	cost    time.Duration
	posts   []feedEntry
	// topics is one row per topic the ledger holds, counted from the posts
	// filed under it. It carries no interest: a stance is read per request,
	// because an operator who has just parked a topic must not have to wait
	// out this index's minute to see it parked.
	topics []topicRow
	// unfiled counts the posts no live filing names a topic for. It is the
	// feed's own notion and not the evaluation lane's: a heuristic filing
	// gives a post a topic here, because the operator accepted the entity
	// even though nothing has judged this record's membership yet, while
	// the lane's backlog counts exactly those unjudged memberships.
	unfiled int
	// notice is what the response says when the deployment could not be
	// consulted. It is the listings' own sentence, because the reader is
	// owed the same fact on the front page he is owed in a listing: these
	// rows are what this machine could reach.
	notice string
	// standingsUnread records that this build could not derive where its
	// records stand. It travels because it changes what one answer means:
	// an unfiltered feed is unaffected, and a feed narrowed to what awaits
	// the operator would silently hold nothing but questions, which reads
	// as a deployment with no backlog rather than as a derivation that
	// failed.
	standingsUnread bool
}

// feedEntry is one post as the index holds it: the wire fields plus the two
// the ranks need and a reader never sees.
type feedEntry struct {
	post feedPost
	// createdAt and lastActivity are the parsed instants the ranks and the
	// windows use. The wire carries their text.
	createdAt    time.Time
	lastActivity time.Time
	// activity is when each vote and comment landed, which is what the
	// rising rank counts inside a window that moves after this was built.
	activity []time.Time
	// topics and topicIDs are the live topics this post is filed under, by
	// name and by id. Both travel because ?topic= takes either: a reader
	// following a sidebar row has the id, and a reader who typed the name
	// has the name.
	topics   []string
	topicIDs []string
	// urgency is which of sortNext's groups this post belongs to. It is
	// read only when the post awaits the operator, because it answers "how
	// urgent is this wait" and a post nobody is waiting on has no answer.
	urgency int
}

// handleFeed serves the front page.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	query := r.URL.Query()
	sortBy := query.Get("sort")
	if sortBy == "" {
		sortBy = sortHot
	}
	if !contains(feedSorts(), sortBy) {
		s.writeError(w, http.StatusBadRequest, "there is no sort called "+strconv.Quote(sortBy))
		return
	}
	window := query.Get("t")
	if window == "" {
		window = "day"
	}
	if _, known := feedWindow(window); !known {
		s.writeError(w, http.StatusBadRequest, "there is no time window called "+strconv.Quote(window))
		return
	}
	kinds, ok := s.feedKindFilter(w, query.Get("kind"))
	if !ok {
		return
	}
	needs := query.Get("needs")
	if needs != "" && needs != feedNeedsOperator {
		s.writeError(w, http.StatusBadRequest, "a post can need the operator and nothing else; "+
			"there is no such thing as needing "+strconv.Quote(needs))
		return
	}
	limit, offset, ok := s.feedPage(w, r)
	if !ok {
		return
	}
	index, err := s.feedIndex(r)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}

	topic := query.Get("topic")
	// One instant decides both the window and the ranks. Two readings of the
	// clock a microsecond apart could put a post inside the rising window
	// for the filter and outside it for the sort, which is a row that is
	// eligible and unrankable.
	now := time.Now().UTC()
	eligible := filterFeed(index.posts, topic, kinds, sortBy, window, needs != "", now)
	sortFeed(eligible, sortBy, now)
	result := feedList{
		Posts:   []feedPost{},
		Total:   len(eligible),
		Sort:    sortBy,
		T:       window,
		Topic:   topic,
		Kinds:   kinds,
		Needs:   needs,
		BuiltAt: timeText(index.builtAt),
		Notice:  index.notice,
	}
	// The one notice this filter adds rather than inherits. A record's
	// standing is what "awaits the operator" is derived from, so a feed
	// narrowed to what needs him after that derivation failed is holding
	// back rows it cannot classify, and saying nothing would present the
	// remainder as the whole backlog.
	if needs != "" && index.standingsUnread {
		result.Notice = joinNotices(result.Notice, standingsUnread)
	}
	if result.Kinds == nil {
		result.Kinds = []string{}
	}
	for i := offset; i < len(eligible) && i < offset+limit; i++ {
		result.Posts = append(result.Posts, eligible[i].post)
	}
	s.writeJSON(w, http.StatusOK, result)
}

// feedPulse is GET /api/feed/pulse: what Babel did today, and what it is
// doing at this instant.
//
// It is a second route rather than a block on the feed because it is a
// different question with a different freshness. The feed is a ranked corpus
// rebuilt at most once a minute; this is a handful of counts and a short list
// of work in flight, and a reader watching Babel think wants the second to
// move while the first stands still. Folding them together would either make
// the counts a minute stale or make the corpus rebuild every poll.
//
// Every number here is read from a store this process already holds open, and
// each one names its own source in pulseCounts below. None of them is derived
// from another: a count assembled by summing two others is a number that goes
// wrong silently when either changes.
type feedPulse struct {
	Today pulseCounts `json:"today"`
	// Since is the instant the counts start at: midnight UTC before now.
	// It travels because a count with no window is a number a reader has
	// to guess the meaning of, and because the operator's day and this
	// machine's day are the same day only by convention — saying which one
	// was used is what makes "today" checkable.
	Since string `json:"since"`
	// Reviewing is the records under an open evaluation claim at the
	// instant this was read, oldest first. It is the one part of this
	// answer that is not a count: a reader watching a review in flight
	// wants to know which record, and a number would tell him only that
	// something is happening somewhere.
	Reviewing []pulseReview `json:"reviewing"`
}

// pulseCounts is the day's work, one number per act, each from the cheapest
// store read that answers it honestly.
type pulseCounts struct {
	// SessionsRead is the distinct sessions named by the preparations of
	// the run receipts recorded today — host, harness and source id, so
	// one conversation read by three runs counts once.
	//
	// It is the receipts rather than the citations of today's records, and
	// that is the honest half as well as the cheap one: a record cites the
	// sessions its evidence came from, which is the material that survived
	// into a claim rather than the material Babel read. The receipts are
	// newest first and this stops at the first one older than the window,
	// so the cost is today's runs and not the deployment's history.
	SessionsRead int `json:"sessions_read"`
	// Records is every frontier record written today, all four kinds,
	// counted by the store's own per-day aggregate. Revisions count on the
	// day they were written, which is RecordDays' own rule: an amendment
	// is analysis somebody performed today rather than a correction to
	// yesterday's number.
	Records int `json:"records"`
	// Votes is the assessments recorded today: Babel's reviewers saying
	// what they think, counted by the evaluation store's per-day
	// aggregate. The operator's feedback is not in it, because he does not
	// vote (§8.7).
	Votes int `json:"votes"`
	// Proposals is the proposal kind of Records above, from the same read.
	// It is beside the total rather than inside it because a proposal is a
	// remedy addressed to the operator and the rest of the day's output is
	// not.
	Proposals int `json:"proposals"`
	// TopicProposals is the open topic plans whose proposal record was
	// written today: §4.13 vocabulary Babel has published and nobody has
	// ruled on. The creation date is the proposal record's, read from the
	// feed index that already holds it, because a plan has no date of its
	// own — it is the proposal it explains that was written.
	TopicProposals int `json:"topic_proposals"`
	// Ruled is the records whose newest ruling was recorded today.
	//
	// It is per record rather than per disposition, which is what the
	// deployment-wide standings read can answer in one pass: a record the
	// operator ruled on twice today counts once. Every ruling is his
	// (§4.7), so the attribution is exact even though the arithmetic is a
	// floor.
	Ruled int `json:"ruled"`
}

// pulseReview is one record a reviewer is holding right now.
type pulseReview struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Title is the record's own line, from the feed index where it is a
	// post and from the record itself where it is not: an observation is
	// evidence rather than a post (§4.13) and is still a thing Babel can
	// be reading, so dropping it would show an idle deployment during the
	// pass that reads the most.
	Title string `json:"title"`
	// Since is when the claim on this record was granted. A takeover does
	// not rewrite it, so a reviewer that died and was replaced reads as one
	// long review rather than as a fresh one.
	Since string `json:"since"`
}

// handleFeedPulse serves the front page's live signal.
func (s *Server) handleFeedPulse(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	index, err := s.feedIndex(r)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	// One instant decides the window and the claim expiry both, for
	// handleFeed's reason: two readings of the clock could put a receipt
	// inside today for one count and outside it for another.
	since := now.Truncate(24 * time.Hour)
	result := feedPulse{
		Since:     timeText(since),
		Reviewing: []pulseReview{},
	}
	result.Today.Records, result.Today.Proposals = s.pulseRecords(ctx, r, since)
	result.Today.Votes = s.pulseVotes(ctx, r, since)
	result.Today.TopicProposals = s.pulseTopicProposals(ctx, r, index, since)
	result.Today.Ruled = s.pulseRuled(ctx, r, since)
	result.Today.SessionsRead = s.pulseSessionsRead(ctx, r, since)
	result.Reviewing = s.pulseReviewing(ctx, r, index, now)
	s.writeJSON(w, http.StatusOK, result)
}

// pulseRecords counts today's frontier records and the proposals among them,
// from the store's own per-day aggregate.
//
// A store that could not be counted reports zero rather than failing the
// route, which is this surface's rule everywhere a panel is one of several: a
// pulse missing one number is still the pulse, and refusing the whole answer
// because one aggregate was unreadable would take the live signal away over
// one of its six figures.
func (s *Server) pulseRecords(ctx context.Context, r *http.Request, since time.Time) (records, proposals int) {
	rows, err := s.opts.Frontier.RecordDays(ctx, since)
	if err != nil {
		s.logf("GET %s: the frontier could not be counted by day", r.URL.Path)
		return 0, 0
	}
	day := since.Format(time.DateOnly)
	for _, row := range rows {
		if row.Day != day {
			continue
		}
		records += row.Count
		if row.Kind == string(frontier.EntityProposal) {
			proposals += row.Count
		}
	}
	return records, proposals
}

// pulseVotes counts today's assessments: Babel's reviewers voting.
func (s *Server) pulseVotes(ctx context.Context, r *http.Request, since time.Time) int {
	if s.opts.Evaluation == nil {
		return 0
	}
	days, err := s.opts.Evaluation.AssessmentDays(ctx, since)
	if err != nil {
		s.logf("GET %s: the evaluation store could not be counted by day", r.URL.Path)
		return 0
	}
	day := since.Format(time.DateOnly)
	for _, row := range days {
		if row.Day == day {
			return row.Count
		}
	}
	return 0
}

// pulseTopicProposals counts the open topic plans Babel published today.
//
// The date is the proposal record's own, taken from the index rather than
// re-read: a plan is durable ledger state with no date of its own, and the
// thing that happened today is the proposal that carries it. A plan whose
// proposal this machine does not hold is not counted, on topicProposals' own
// terms — it is another host's output, and this count is about what happened
// here.
func (s *Server) pulseTopicProposals(ctx context.Context, r *http.Request, index *feedIndex,
	since time.Time) int {
	if s.opts.TopicPlans == nil {
		return 0
	}
	plans, err := s.opts.TopicPlans.OpenTopicPlans(ctx)
	if err != nil {
		s.logf("GET %s: the ledger's topic plans are unread; the pulse counts none", r.URL.Path)
		return 0
	}
	created := make(map[string]time.Time, len(index.posts))
	for _, entry := range index.posts {
		created[entry.post.ID] = entry.createdAt
	}
	count := 0
	for _, plan := range plans {
		if at, held := created[plan.ProposalID]; held && !at.Before(since) {
			count++
		}
	}
	return count
}

// pulseRuled counts the records whose newest ruling was recorded today.
func (s *Server) pulseRuled(ctx context.Context, r *http.Request, since time.Time) int {
	standings, err := s.opts.Frontier.ReviewStandings(ctx)
	if err != nil {
		s.logf("GET %s: review standings unread; the pulse counts no rulings", r.URL.Path)
		return 0
	}
	count := 0
	for _, standing := range standings {
		if !standing.RecordedAt.Before(since) {
			count++
		}
	}
	return count
}

// pulseReceiptPage is how many receipts one page of the scan below reads.
//
// It is the run store's own default rather than its ceiling because the scan
// stops at the first receipt older than the window: a deployment that
// recorded three runs today pays for one page, and one that recorded four
// hundred pays for eight. A page of five hundred would make the common case
// the expensive one.
const pulseReceiptPage = 50

// pulseSessionsRead counts the distinct sessions today's runs read.
func (s *Server) pulseSessionsRead(ctx context.Context, r *http.Request, since time.Time) int {
	if s.opts.Receipts == nil {
		return 0
	}
	type sessionKey struct{ host, harness, sourceID string }
	seen := map[sessionKey]struct{}{}
	for offset := 0; offset < listScanCap; offset += pulseReceiptPage {
		page, total, err := s.opts.Receipts.Receipts(ctx, pulseReceiptPage, offset)
		if err != nil {
			s.logf("GET %s: run receipts unread; the pulse counts no sessions", r.URL.Path)
			return len(seen)
		}
		for _, receipt := range page {
			// Newest first, so the first receipt from before the window
			// ends the scan rather than filtering one row out of it.
			if receipt.Header.RecordedAt.Before(since) {
				return len(seen)
			}
			for _, selected := range receipt.Preparation.Selection {
				seen[sessionKey{selected.Host, selected.Harness, selected.SourceID}] = struct{}{}
			}
		}
		if len(page) < pulseReceiptPage || offset+len(page) >= total {
			break
		}
	}
	return len(seen)
}

// pulseReviewing lists the records under an open claim at now, oldest first.
//
// Oldest first because that is the order the list is read in: the record that
// has been held longest is the one a reader wonders about, and a list ordered
// by identifier would reshuffle every poll. Ties resolve by identifier so two
// claims granted in the same instant have one order.
func (s *Server) pulseReviewing(ctx context.Context, r *http.Request, index *feedIndex,
	now time.Time) []pulseReview {
	out := []pulseReview{}
	if s.opts.Evaluation == nil {
		return out
	}
	claims, err := s.opts.Evaluation.OpenClaims(ctx, now)
	if err != nil {
		s.logf("GET %s: open evaluation claims unread; the pulse shows no review in flight", r.URL.Path)
		return out
	}
	titles := make(map[string]string, len(index.posts))
	for _, entry := range index.posts {
		titles[entry.post.ID] = entry.post.Title
	}
	for subject, claim := range claims {
		title, held := titles[subject.ID]
		if !held {
			// A subject the front page does not carry is still under
			// review: an observation is evidence rather than a post,
			// and a superseded wording is a record the reader reaches
			// from its replacement. One read each, bounded by the
			// open claims, is what it costs to name them.
			line, err := s.excerpt(ctx, frontier.Ref{
				Type: frontier.EntityType(subject.Kind),
				ID:   subject.ID,
			})
			if err != nil {
				s.logf("GET %s: %s is under review and could not be read", r.URL.Path, subject)
			}
			title = boundedLine(line)
		}
		out = append(out, pulseReview{
			ID:    subject.ID,
			Kind:  subject.Kind,
			Title: title,
			Since: timeText(claim.Since),
		})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Since != out[b].Since {
			return out[a].Since < out[b].Since
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// handleTopics serves what the deployment's records are about: the topics the
// operator accepted, the ones Babel has proposed, and the backlog of records
// nothing has filed (§4.13).
//
// The three lists are one answer because they are one decision. A reader
// deciding what to read next is choosing between a topic he is working on, a
// proposal he could accept in a click, and a backlog he could leave alone; a
// page that had to ask three times would let the three disagree about what
// exists.
//
// Until the operator has accepted anything, topics is empty, proposed carries
// whatever the seeder raised, and every post is unfiled. That is the intended
// day-one state rather than a degradation: §4.13 gives entity creation to an
// attributed operator act, so a deployment that has not performed one has no
// topics, and saying so is the honest answer.
func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	index, err := s.feedIndex(r)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := topicList{
		Topics:   s.topicStances(r, index.topics),
		Proposed: s.topicProposals(r, index),
		Unfiled:  index.unfiled,
	}
	s.writeJSON(w, http.StatusOK, result)
}

// topicStances attaches the operator's stance to each topic and puts the list
// in the order §4.13 asks for.
//
// The order is the operator's attention rather than the corpus's size: what he
// is working on, what he is keeping an eye on, what he has said nothing about,
// what he has parked, and what he excluded — and inside each group the
// busiest first. A stance nobody recorded sorts above "not now" because
// silence is not a refusal, which is the same distinction §4.12 draws about
// feedback.
//
// A stance this build cannot read leaves every topic unset rather than failing
// the page: the counts and the bindings are unaffected, and a topics page that
// refused because the ledger's interest facts were unreadable would take the
// vocabulary away over its ordering.
func (s *Server) topicStances(r *http.Request, topics []topicRow) []topicRow {
	out := make([]topicRow, 0, len(topics))
	out = append(out, topics...)
	if s.opts.Stance != nil {
		for i := range out {
			stance, err := s.opts.Stance.TopicInterest(r.Context(), out[i].ID)
			if err != nil {
				s.logf("GET %s: the stance toward topic %s is unread; it renders unset",
					r.URL.Path, out[i].ID)
				continue
			}
			out[i].Interest = topicInterest{
				State: stance.State, Reason: stance.Reason, At: stance.At, By: stance.By,
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if left, right := interestRank(out[a].Interest.State), interestRank(out[b].Interest.State); left != right {
			return left < right
		}
		if out[a].Posts != out[b].Posts {
			return out[a].Posts > out[b].Posts
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// interestRank is the reading order of §4.13's stances. An unrecognized state
// sorts with the unset ones rather than last: a stance this build does not
// know is not a stance it may read as a refusal.
func interestRank(state string) int {
	switch state {
	case interestWorking:
		return 0
	case interestWatching:
		return 1
	case interestNotNow:
		return 3
	case interestExcluded:
		return 4
	}
	return 2
}

// The stances §4.13 offers on a topic's own page. They are this surface's
// copy of the ledger's vocabulary because the sort and the route that records
// one both need to name them, and a misspelled state must be refused rather
// than silently sorted into the unset group.
const (
	interestWorking  = "working"
	interestWatching = "watching"
	interestNotNow   = "not-now"
	interestExcluded = "excluded"
)

// topicProposals reads the topic plans Babel has published and nobody has
// ruled on, joined to the proposal records that carry them.
//
// The join is what makes the rail a view of the feed rather than a second
// list: the row's title is the proposal record's own line, so the shortcut
// and the post a reader opens from it say the same thing, and a plan whose
// proposal this deployment does not hold is dropped rather than shown as a
// row with nothing behind it.
//
// The post count is over the records this deployment actually holds rather
// than over the ids the plan names, because that is what accepting it would
// file here: a plan produced against another host's corpus would otherwise
// promise a number this machine cannot deliver.
func (s *Server) topicProposals(r *http.Request, index *feedIndex) []topicProposal {
	out := []topicProposal{}
	if s.opts.TopicPlans == nil {
		return out
	}
	plans, err := s.opts.TopicPlans.OpenTopicPlans(r.Context())
	if err != nil {
		s.logf("GET %s: the ledger's topic plans are unread; the page proposes none", r.URL.Path)
		return out
	}
	held := make(map[string]feedPost, len(index.posts))
	for _, entry := range index.posts {
		held[entry.post.ID] = entry.post
	}
	for _, plan := range plans {
		post, carried := held[plan.ProposalID]
		if !carried {
			// The plan is durable and this machine does not hold the
			// proposal it explains — another host's output, or a
			// record this build could not read. A rail row the
			// operator cannot open is worse than one fewer row.
			continue
		}
		posts := 0
		for _, id := range plan.Records {
			if _, ok := held[id]; ok {
				posts++
			}
		}
		row := topicProposal{
			ProposalID: plan.ProposalID,
			Title:      post.Title,
			Name:       plan.Name,
			Kind:       plan.Kind,
			Operation:  plan.Operation,
			Targets:    []topicTarget{},
			RunID:      plan.RunID,
			Posts:      posts,
			Why:        plan.Why,
		}
		for _, target := range plan.Targets {
			row.Targets = append(row.Targets, topicTarget{ID: target.ID, Name: target.Name})
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Posts != out[b].Posts {
			return out[a].Posts > out[b].Posts
		}
		return out[a].Title < out[b].Title
	})
	return out
}

// invalidateFeed drops the built index so the next read rebuilds it.
//
// It is called by the acts that change what is filed rather than by every
// mutation, because the index's minute of staleness is affordable for
// everything else on it — a vote, a ruling — and is not affordable for the
// filing an operator has just performed and is looking at.
func (s *Server) invalidateFeed() {
	s.feed.mu.Lock()
	defer s.feed.mu.Unlock()
	s.feed.index = nil
}

// feedKindFilter resolves the ?kind= list, refusing an unknown kind rather
// than answering it with an empty feed: a misspelled kind that matched
// nothing reads as a deployment that has produced none of them.
func (s *Server) feedKindFilter(w http.ResponseWriter, value string) ([]string, bool) {
	if strings.TrimSpace(value) == "" {
		return nil, true
	}
	var kinds []string
	for _, part := range strings.Split(value, ",") {
		kind := strings.TrimSpace(part)
		if kind == "" {
			continue
		}
		if !contains(feedKinds(), kind) {
			s.writeError(w, http.StatusBadRequest, "there is no record kind called "+strconv.Quote(kind))
			return nil, false
		}
		if !contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds, true
}

// feedPage reads the window a client asked for, bounded rather than trusted.
func (s *Server) feedPage(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit, valid := queryInt(r, "limit", feedPageDefault)
	if !valid || limit <= 0 {
		s.writeError(w, http.StatusBadRequest, "limit must be a positive whole number")
		return 0, 0, false
	}
	if limit > feedPageMax {
		limit = feedPageMax
	}
	offset, valid = queryInt(r, "offset", 0)
	if !valid || offset < 0 {
		s.writeError(w, http.StatusBadRequest, "offset must be zero or a positive whole number")
		return 0, 0, false
	}
	return limit, offset, true
}

// filterFeed narrows the index to the eligible set, which is what the sort
// then orders whole.
//
// The window applies to top and controversial and to nothing else, because
// those are the two sorts that are *about* a period: "hot" over a day and
// "hot" over all time would be the same list with the older half deleted, and
// a rising post is by definition recent.
//
// needs is the queue, and it is a filter rather than a list because the queue
// was the same list twice (operator direction 2026-09-12). A post awaiting
// the operator is a fact the feed already carries, so asking for only those
// narrows one order instead of opening a second one that could disagree with
// it about what is waiting.
func filterFeed(posts []feedEntry, topic string, kinds []string, sortBy, window string,
	needs bool, now time.Time) []feedEntry {
	var since time.Time
	if sortBy == sortTop || sortBy == sortControversial {
		if width, _ := feedWindow(window); width > 0 {
			since = now.Add(-width)
		}
	}
	out := make([]feedEntry, 0, len(posts))
	for _, entry := range posts {
		if len(kinds) > 0 && !contains(kinds, entry.post.Kind) {
			continue
		}
		if needs && !entry.post.Awaiting {
			continue
		}
		if topic != "" && !entryInTopic(entry, topic) {
			continue
		}
		if !since.IsZero() && entry.createdAt.Before(since) {
			continue
		}
		if sortBy == sortRising && risingRank(entry.activity, entry.createdAt, now) == 0 {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// entryInTopic reports whether one post belongs to the topic a reader asked
// for, by the topic's name or by its id.
//
// Both are accepted because both are what a reader has. A sidebar row carries
// the entity id and is unambiguous; a name is what an operator types and what
// a link in somebody's notes holds, and refusing it would make the id the only
// way to open a topic. A name that two entities answer to opens both, which is
// the honest answer to an ambiguous question and the same one the reserved
// unfiled value gets: what matched is shown rather than one of them picked.
func entryInTopic(entry feedEntry, topic string) bool {
	if topic == topicUnfiled {
		return len(entry.topics) == 0
	}
	return contains(entry.topics, topic) || contains(entry.topicIDs, topic)
}

// sortFeed orders the eligible set. Ties resolve newer first everywhere, and
// then by identifier, so one corpus has one order rather than a different one
// per rebuild — except under next, which is the one order that is about a
// wait rather than a reception and therefore drains from the bottom.
func sortFeed(posts []feedEntry, sortBy string, now time.Time) {
	rank := make([]float64, len(posts))
	for i, entry := range posts {
		switch sortBy {
		case sortHot:
			rank[i] = hotRank(entry.post.Score, entry.createdAt)
		case sortTop:
			rank[i] = topRank(entry.post.Score)
		case sortControversial:
			rank[i] = controversialRank(entry.post.Support, entry.post.Oppose)
		case sortRising:
			rank[i] = risingRank(entry.activity, entry.createdAt, now)
		}
	}
	order := make([]int, len(posts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := order[a], order[b]
		if sortBy == sortNext {
			return nextBefore(posts[x], posts[y])
		}
		if sortBy != sortNew && rank[x] != rank[y] {
			return rank[x] > rank[y]
		}
		if !posts[x].createdAt.Equal(posts[y].createdAt) {
			return posts[x].createdAt.After(posts[y].createdAt)
		}
		return posts[x].post.ID < posts[y].post.ID
	})
	sorted := make([]feedEntry, len(posts))
	for i, from := range order {
		sorted[i] = posts[from]
	}
	copy(posts, sorted)
}

// nextBefore is §8.5's reading order, which §8.7 puts on the sort bar as
// next: urgency first, then the kind, then the oldest wait.
//
// Three keys and their order are the whole rule. Urgency, because a question
// Babel has stopped on costs more to leave than a candidate it is still
// developing. The kind at equal urgency, because a proposal is a remedy
// addressed to the operator and a candidate is not addressed to him at all.
// The oldest first inside a kind, on the ordinary grounds that a queue nobody
// drains from the bottom has a permanent bottom — which is the one place this
// order inverts every other sort in this file.
//
// A post that awaits nothing sorts after every post that does, newest first,
// so that next is a complete order over the corpus rather than a filter
// wearing a sort's name: a reader who turns the queue filter off keeps the
// same list with the rest of the deployment underneath it.
func nextBefore(left, right feedEntry) bool {
	if left.post.Awaiting != right.post.Awaiting {
		return left.post.Awaiting
	}
	if !left.post.Awaiting {
		if !left.createdAt.Equal(right.createdAt) {
			return left.createdAt.After(right.createdAt)
		}
		return left.post.ID < right.post.ID
	}
	if left.urgency != right.urgency {
		return left.urgency < right.urgency
	}
	if lw, rw := feedKindWeight(left.post.Kind), feedKindWeight(right.post.Kind); lw != rw {
		return lw < rw
	}
	if !left.createdAt.Equal(right.createdAt) {
		return left.createdAt.Before(right.createdAt)
	}
	return left.post.ID < right.post.ID
}

// hotRank is the signed log of the score plus age at a fixed decay.
//
// The sign is what makes it work on a corpus that can be voted down: a
// disputed record sinks rather than sorting beside an unvoted one, and the
// logarithm is why the tenth vote moves a post less than the second did.
func hotRank(score int, createdAt time.Time) float64 {
	magnitude := math.Log10(math.Max(math.Abs(float64(score)), 1))
	sign := 0.0
	switch {
	case score > 0:
		sign = 1
	case score < 0:
		sign = -1
	}
	age := createdAt.Sub(feedEpoch).Seconds()
	return sign*magnitude + age/feedDecay
}

// topRank is the score itself. It is a function rather than a field read so
// that every sort in this file is one named rule, and so the window that makes
// "top of the day" different from "top of all time" is visibly not part of it.
func topRank(score int) float64 { return float64(score) }

// controversialRank rewards balance times magnitude, and is zero for anything
// one-sided.
//
// The zero is the honest answer rather than a small number: §8.7 says
// controversial "needs both support and opposition", and a record nine
// reviewers supported is not slightly controversial — it is agreed on.
func controversialRank(support, oppose int) float64 {
	if support <= 0 || oppose <= 0 {
		return 0
	}
	smaller, larger := support, oppose
	if smaller > larger {
		smaller, larger = larger, smaller
	}
	return math.Pow(float64(support+oppose), float64(smaller)/float64(larger))
}

// risingRank is recent activity against age.
//
// Zero means nothing has happened in the window, and the feed excludes those
// rather than ranking them last: a rising list whose tail is every silent
// record in the corpus is a list of every record in the corpus.
func risingRank(activity []time.Time, createdAt, now time.Time) float64 {
	cutoff := now.Add(-risingWindow)
	recent := 0
	for _, at := range activity {
		if at.After(cutoff) {
			recent++
		}
	}
	if recent == 0 {
		return 0
	}
	age := now.Sub(createdAt).Hours()
	if age < 0 {
		age = 0
	}
	return float64(recent) / math.Pow(age+2, 1.5)
}

// feedIndex returns the current projection, rebuilding it when it has aged
// past feedFreshness.
func (s *Server) feedIndex(r *http.Request) (*feedIndex, error) {
	s.feed.mu.Lock()
	defer s.feed.mu.Unlock()
	if index := s.feed.index; index != nil && time.Since(index.builtAt) < feedFreshness {
		return index, nil
	}
	index, err := s.buildFeedIndex(r)
	if err != nil {
		return nil, err
	}
	s.feed.index = index
	return index, nil
}

// buildFeedIndex assembles the whole deployment's posts.
//
// The order of the passes is the order of the dependencies and nothing else.
// The ledger's topics are read once and the frontier is asked what is filed
// under each of them, because a post's topics are its filings (§4.13); the
// four enumerations are read once; the rulings and the reception are one read
// each rather than one per record, which is the difference between a front
// page and a scan.
func (s *Server) buildFeedIndex(r *http.Request) (*feedIndex, error) {
	started := time.Now()
	ctx := r.Context()
	index := &feedIndex{builtAt: started.UTC()}
	// The build's own instant is what every age in the index is measured
	// from — "waiting 3d", "asked 2h" — rather than the instant a request
	// arrives. The two differ by at most feedFreshness, and reading the
	// clock per row would let two rows of one page disagree about now.
	now := index.builtAt

	corpus, err := s.readCorpus(ctx)
	if err != nil {
		return nil, err
	}
	index.topics = s.ledgerTopics(ctx, r)
	topics := s.filedTopics(ctx, r, index.topics)

	standings, err := s.opts.Frontier.ReviewStandings(ctx)
	if err != nil {
		// A ruling this surface could not derive is a standing it does not
		// state, never a standing it invents. The feed renders without one
		// rather than telling a reader that nobody has decided.
		s.logf("GET %s: review standings unread; the feed renders without them", r.URL.Path)
		standings = nil
		index.standingsUnread = true
	}
	reception := s.feedReception(ctx, r, now)

	for _, record := range corpus.hypotheses {
		index.add(s.feedRecord(frontier.EntityHypothesis, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Statement, topics[record.ID], standings, reception, now))
	}
	// No observation pass, and it is deliberate: §4.13's last reading makes
	// observations evidence rather than posts. They are still read above —
	// the topic derivation walks them — and they are still filed and
	// searchable; they are simply not rows on the front page.
	for _, record := range corpus.findings {
		index.add(s.feedRecord(frontier.EntityFinding, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Title, topics[record.ID], standings, reception, now))
	}
	for _, record := range corpus.proposals {
		index.add(s.feedRecord(frontier.EntityProposal, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Title, topics[record.ID], standings, reception, now))
	}
	for _, entry := range s.feedQuestions(ctx, r, now) {
		index.add(entry)
	}
	// The other machines' committed records, on the merged listings' terms:
	// what this deployment produced is deployment state, and a front page
	// answering "what did this laptop write" would present a shared body of
	// work as though each machine owned a private one. A catalog that did
	// not answer costs those rows and says so.
	//
	// A machine in local mode has no other hosts by definition, which is
	// fleetScope's own rule and is why the guard is a wired reader rather
	// than a request parameter: there is no second place to look, so there
	// is nothing the deployment failed to say. A shared backend that failed
	// to start is the other case and is not local mode — it degrades, which
	// is what FleetError carries.
	if s.opts.Fleet != nil || s.opts.FleetError != nil {
		merged, degraded := s.mergeOtherHosts(r, listScanCap, sharedcatalog.KindHypothesis,
			sharedcatalog.KindFinding, sharedcatalog.KindProposal)
		if degraded {
			index.notice = catalogUnreachable
		}
		for _, record := range merged {
			if entry, ok := feedFleetRecord(record, reception); ok {
				index.add(entry)
			}
		}
	}

	index.countTopics()
	index.cost = time.Since(started)
	return index, nil
}

// add enrols one post, skipping a record whose claim this build could not
// read: a row whose line is blank is an identifier in a list, which is the
// surface §8.6 replaced.
func (i *feedIndex) add(entry feedEntry) {
	if entry.post.ID == "" || entry.post.Title == "" {
		return
	}
	i.posts = append(i.posts, entry)
}

// topicMembership is the topics one record is filed under, by name and by id.
// Both are carried because the wire shows names and the filter takes either.
type topicMembership struct {
	names []string
	ids   []string
}

// ledgerTopics reads the topics the operator has created (§4.13): every entity
// the Reality Ledger holds, with what it is bound to.
//
// Every entity is a topic, not only the ones something is filed under. §4.13
// makes a topic a ledger entity and nothing else, so a project the operator
// named and Babel has written nothing about yet is an empty topic rather than
// an absent one — which is what makes it possible to file the first record
// under it.
//
// A merged-away identity is skipped, because the entity that speaks for it is
// already in the list and showing both would offer the operator two names for
// one thing (§4.8's merge is exactly the statement that they are one). A
// retired entity is skipped for §4.13's own reason: retiring re-queues its
// filings, so it is no longer a place records live.
//
// A ledger this build cannot read leaves the deployment with no topics, which
// is the same shape as a deployment that has created none: every post is
// unfiled and the page says so.
func (s *Server) ledgerTopics(ctx context.Context, r *http.Request) []topicRow {
	if s.opts.Reality == nil {
		return nil
	}
	entities, err := s.opts.Reality.Entities(ctx, reality.EntityQuery{})
	if err != nil {
		s.logf("GET %s: the ledger's entities are unread; the feed renders with no topics", r.URL.Path)
		return nil
	}
	rows := make([]topicRow, 0, len(entities))
	for _, listing := range entities {
		entity := listing.Entity
		if entity.CanonicalID != "" && entity.CanonicalID != entity.ID {
			continue
		}
		binding, retired := s.topicBindingOf(ctx, r, entity)
		if retired {
			continue
		}
		rows = append(rows, topicRow{
			ID:      entity.ID,
			Name:    entity.Payload.DisplayName,
			Kind:    string(entity.Kind),
			Binding: binding,
		})
	}
	return rows
}

// topicBindingOf reads what one topic is bound to, and whether the operator
// has retired it.
//
// The binding is derived from the ledger's own facts rather than from the
// session catalog stage 1 read: the remote and the checkouts are what the
// operator accepted when he created the entity, and re-deriving them from what
// this host happens to hold would let the page show a binding the ledger does
// not hold. Facts this build could not read leave the topic unbound, which is
// the same answer a topic with no binding facts gets.
func (s *Server) topicBindingOf(ctx context.Context, r *http.Request, entity reality.Entity) (
	*topicBinding, bool) {
	facts, err := s.opts.Reality.Facts(ctx, reality.FactQuery{
		SubjectID: entity.ID,
		Statuses:  []reality.FactStatus{reality.FactActive},
	})
	if err != nil {
		s.logf("GET %s: the facts about topic %s are unread; it renders unbound",
			r.URL.Path, entity.ID)
		return nil, false
	}
	var (
		remote  string
		paths   []string
		retired bool
	)
	for _, fact := range facts {
		switch fact.Predicate {
		case topicRemotePredicate:
			remote = fact.Value.Text
		case reality.PredicateLocalPath:
			paths = append(paths, fact.Value.Text)
		case reality.PredicateLifecycle:
			retired = fact.Value.Enum == reality.LifecycleRetired
		}
	}
	if retired {
		return nil, true
	}
	if remote == "" && len(paths) == 0 {
		return nil, false
	}
	sort.Strings(paths)
	identity := remote
	if identity == "" {
		identity = paths[0]
	}
	return &topicBinding{
		Kind:     string(entity.Kind),
		Identity: identity,
		Remote:   remote,
		Paths:    paths,
	}, false
}

// topicRemotePredicate is the ledger predicate holding a repository's remote.
//
// It is spelled here rather than imported because it is the ledger's
// vocabulary and this surface only reads it; the value is the one
// internal/reality registers, and a mismatch shows up as a repository topic
// that renders with its checkouts and no remote rather than as a failure.
const topicRemotePredicate = reality.Predicate("repository-remote")

// filedTopics asks the frontier what is filed under each topic and inverts the
// answer into the membership each record carries.
//
// The direction is the store's: FiledUnder answers "what is in this topic",
// which is one indexed read per topic, where the other direction would be one
// read per record. A topic whose filings could not be read contributes
// nothing, so the records it holds render unfiled — the honest degradation,
// because a post that claims no topic is a post nobody has to un-believe.
func (s *Server) filedTopics(ctx context.Context, r *http.Request, topics []topicRow) map[string]topicMembership {
	filed := map[string]topicMembership{}
	if s.opts.Filings == nil {
		return filed
	}
	for _, topic := range topics {
		records, err := s.opts.Filings.FiledUnder(ctx, topic.ID)
		if err != nil {
			s.logf("GET %s: what is filed under topic %s is unread; those posts render unfiled",
				r.URL.Path, topic.ID)
			continue
		}
		for _, record := range records {
			membership := filed[record.ID]
			membership.names = append(membership.names, topic.Name)
			membership.ids = append(membership.ids, topic.ID)
			filed[record.ID] = membership
		}
	}
	// A record's topics are a set, and a set rendered in the order the
	// entities happened to be listed would reshuffle between two reads.
	for id, membership := range filed {
		sort.Strings(membership.names)
		sort.Strings(membership.ids)
		filed[id] = membership
	}
	return filed
}

// countTopics counts the posts under each topic from the posts themselves, so
// a topic's count and the feed it opens cannot disagree.
//
// Awaiting is counted beside the total because §4.13 puts the operator's
// attention on the topic page: a topic with forty posts and nothing waiting is
// a different thing to open from one with three that all need a ruling.
func (i *feedIndex) countTopics() {
	posts := map[string]int{}
	awaiting := map[string]int{}
	latest := map[string]time.Time{}
	for _, entry := range i.posts {
		if len(entry.topicIDs) == 0 {
			i.unfiled++
			continue
		}
		for _, id := range entry.topicIDs {
			posts[id]++
			if entry.post.Awaiting {
				awaiting[id]++
			}
			if entry.createdAt.After(latest[id]) {
				latest[id] = entry.createdAt
			}
		}
	}
	for at, topic := range i.topics {
		i.topics[at].Posts = posts[topic.ID]
		i.topics[at].Awaiting = awaiting[topic.ID]
		i.topics[at].LatestAt = timeText(latest[topic.ID])
	}
}

// feedRecord projects one local record into a post.
func (s *Server) feedRecord(kind frontier.EntityType, id, runID string, createdAt time.Time,
	claim string, topics topicMembership, standings map[frontier.Ref]frontier.ReviewStanding,
	reception feedReception, now time.Time) feedEntry {
	standing := feedStanding(kind, frontier.Ref{Type: kind, ID: id}, standings)
	entry := feedEntry{
		post: feedPost{
			ID:        id,
			Kind:      string(kind),
			Title:     boundedLine(claim),
			Standing:  standing,
			CreatedAt: timeText(createdAt),
			Topics:    idList(topics.names),
			Href:      recordHref(id),
		},
		createdAt: createdAt,
		topics:    topics.names,
		topicIDs:  topics.ids,
	}
	if runID != "" {
		entry.post.Author = &feedAuthor{RunID: runID, Href: runHref(runID)}
	}
	awaitRecord(&entry, standing, createdAt, now)
	reception.apply(&entry, evaluation.Subject{Kind: string(kind), ID: id}, createdAt)
	return entry
}

// awaitRecord decides whether a record is waiting on the operator, and says
// why in five words.
//
// Two standings await a ruling and they are the record page's own two: `new`,
// which is a record nobody has decided, and `reopened`, which is one whose
// ruling an operator deliberately lifted. Those are exactly the two the peel
// offers "Rule on this" against, so the feed and the record cannot disagree
// about what needs him. Every other standing is a ruling that was made —
// accepted, rejected, deferred, duplicate, refine-requested — and a deferral
// is a decision rather than a postponement of one. A record with no standing
// at all is not reviewable (§6.7) and awaits nobody.
//
// The sentence is built from the record's own two facts, which is all a row
// carries: which of the two standings it is at, and how long it has been
// there. The ruling and refinement counts DecidePage put in this sentence
// came from the review queue's per-record derivation, and the front page does
// not perform one per row — so the count is absent rather than guessed, and
// what replaces it is the distinction that actually changes the act: a record
// nobody has ruled on, against one whose ruling came back.
func awaitRecord(entry *feedEntry, standing string, createdAt, now time.Time) {
	switch standing {
	case string(frontier.ReviewNew):
		entry.urgency = urgencyUnruled
		entry.post.Why = feedWhy("never ruled on", "waiting "+ageWord(now.Sub(createdAt)))
	case standingReopened:
		entry.urgency = urgencyReopened
		entry.post.Why = feedWhy("reopened", "waiting "+ageWord(now.Sub(createdAt)))
	default:
		return
	}
	entry.post.Awaiting = true
}

// feedWhy joins the two halves of a why with §8.6's own separator, dropping
// an absent half rather than rendering a gap beside it.
func feedWhy(head, tail string) string {
	if head == "" {
		return tail
	}
	if tail == "" {
		return head
	}
	return head + " · " + tail
}

// ageWord is how long ago something happened, in one word.
//
// One word rather than "3 days" is what keeps §8.7's five-word budget
// spendable on the reason: "never ruled on · waiting 3d" is five words and
// says both halves, where the same sentence with the unit spelled out is six
// and says no more. A future instant reads as `now` for presence.go's reason:
// "waiting -4m" is not a fact about anything.
func ageWord(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/(24*7))) + "w"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/(24*30))) + "mo"
	default:
		return strconv.Itoa(int(d.Hours()/(24*365))) + "y"
	}
}

// feedStanding reads where a record stands, in the vocabulary the record page
// uses.
//
// A record §6.7 does not make reviewable has no standing at all rather than a
// blank one, which is the peel's own rule: an observation is the evidence a
// finding consolidates, not an artifact anybody accepts or rejects. A record
// nobody has ruled on stands at `new`; one whose ruling was lifted stands at
// `reopened`, because a record nobody decided and a record whose rejection an
// operator deliberately reversed are different things to look at.
func feedStanding(kind frontier.EntityType, ref frontier.Ref,
	standings map[frontier.Ref]frontier.ReviewStanding) string {
	if !reviewableRecord(kind) || standings == nil {
		return ""
	}
	standing, ruled := standings[ref]
	if !ruled {
		return string(frontier.ReviewNew)
	}
	if standing.Last == frontier.DispositionReopen {
		return standingReopened
	}
	return string(standing.Status)
}

// applyTally folds one subject's reception into a post.
//
// The columns are Babel's reviewers and the score is theirs alone, which is
// §8.7's arithmetic after the operator stopped voting: "the score is Babel's
// reception and only Babel's". Nothing here reads an operator stance, so
// there is no arithmetic in which his click could become a model's
// observation — the two are not added because one of them is not a term.
func applyTally(entry *feedEntry, tally evaluation.Tally, createdAt time.Time) {
	entry.post.Support, entry.post.Oppose, entry.post.Unsure = tally.Support, tally.Oppose, tally.Unsure
	entry.post.Score = entry.post.Support - entry.post.Oppose
	entry.post.Contested = tally.Contested
	entry.post.Comments = tally.Comments
	entry.activity = tally.Activity
	entry.lastActivity = tally.LastActivity
	if entry.lastActivity.Before(createdAt) {
		entry.lastActivity = createdAt
	}
	entry.post.LastActivityAt = timeText(entry.lastActivity)
}

// feedReception is what the evaluation store says about the whole deployment,
// read once per build: how every subject was received, and which subjects a
// reviewer is holding right now.
//
// The two travel together because they are one answer about one row — what
// Babel has said about this record, and whether it is saying something about
// it at this moment — and because a build that read one of them and not the
// other would render a post that is being reviewed as a post nobody has
// opened.
type feedReception struct {
	tallies map[evaluation.Subject]evaluation.Tally
	claims  map[evaluation.Subject]evaluation.OpenClaim
}

// apply folds one subject's reception and its review in flight into a post.
func (f feedReception) apply(entry *feedEntry, subject evaluation.Subject, createdAt time.Time) {
	applyTally(entry, f.tallies[subject], createdAt)
	entry.post.Reviewing = f.claims[subject].Count > 0
}

// feedReception reads both in two grouped passes, reporting nothing rather
// than failing when the evaluation store could not answer.
//
// A feed whose scores could not be read is still the feed: every claim, every
// topic and every comment count beside it is unaffected, and a front page
// that refused because one store was down would take the corpus away over its
// scoreboard. The claims degrade separately and on the same terms — a row
// that does not say it is under review is the ordinary row, and there is no
// state a reader has to un-believe.
func (s *Server) feedReception(ctx context.Context, r *http.Request, now time.Time) feedReception {
	if s.opts.Evaluation == nil {
		return feedReception{}
	}
	var out feedReception
	tallies, err := s.opts.Evaluation.Tallies(ctx)
	if err != nil {
		s.logf("GET %s: evaluation tallies unread; the feed renders unscored", r.URL.Path)
	} else {
		out.tallies = tallies
	}
	claims, err := s.opts.Evaluation.OpenClaims(ctx, now)
	if err != nil {
		s.logf("GET %s: open evaluation claims unread; the feed shows no review in flight", r.URL.Path)
	} else {
		out.claims = claims
	}
	return out
}

// feedQuestions projects the ledger's questions, which §8.7 puts in the feed
// beside the records.
//
// A question carries no run author: the ledger records what was asked and why,
// and the run that provoked it is not part of the question. Its standing is
// its own state, which is the vocabulary its page shows.
func (s *Server) feedQuestions(ctx context.Context, r *http.Request, now time.Time) []feedEntry {
	if s.opts.Reality == nil {
		return nil
	}
	listed, err := s.opts.Reality.Questions(ctx, reality.QuestionQuery{Limit: listScanCap})
	if err != nil {
		s.logf("GET %s: the ledger's questions are unread; the feed renders without them", r.URL.Path)
		return nil
	}
	out := make([]feedEntry, 0, len(listed))
	for _, item := range listed {
		question := item.Question
		entry := feedEntry{
			post: feedPost{
				ID:        question.ID,
				Kind:      feedKindQuestion,
				Title:     boundedLine(question.Payload.Prompt),
				Standing:  string(question.State),
				CreatedAt: timeText(question.CreatedAt),
				Topics:    []string{},
				Comments:  item.Answers,
				Href:      questionHref(question.ID),
			},
			createdAt:    question.CreatedAt,
			lastActivity: question.CreatedAt,
		}
		entry.post.LastActivityAt = timeText(question.CreatedAt)
		awaitQuestion(&entry, question, now)
		out = append(out, entry)
	}
	return out
}

// awaitQuestion decides whether a question is waiting on the operator, and
// says why in five words.
//
// Three of §4.8's states await him and the rest do not. `open` is the
// ordinary one: Babel asked and nobody has answered. `answered-uninterpreted`
// is his answer sitting with no plan drawn from it, which is a question that
// has stopped moving rather than one that is done. `plan-ready` is an
// interpretation waiting for the single acceptance §4.8 requires of him.
// `interpreting` is Babel's own work in flight, `snoozed` is a wait he chose,
// and answered, declined, obsolete and superseded are finished.
//
// Urgency is the question's class and not its state, which is §4.8's own rule
// and DecidePage's: a blocking question has stopped a run, and every other
// class is in the inbox on the same terms — "a class-dominated ranking would
// bury a security-relevant curiosity question under every blocking one" is
// about the score, and this is about what Babel cannot proceed without.
func awaitQuestion(entry *feedEntry, question reality.Question, now time.Time) {
	var head string
	switch question.State {
	case reality.QuestionOpen:
		switch question.Class {
		case reality.ClassBlocking:
			head = "blocks a run"
		case reality.ClassMaintenance:
			head = "upkeep"
		default:
			head = "curiosity"
		}
	case reality.QuestionAnsweredUninterpreted:
		head = "no plan yet"
	case reality.QuestionPlanReady:
		head = "plan ready"
	default:
		return
	}
	entry.post.Awaiting = true
	entry.post.Why = feedWhy(head, "asked "+ageWord(now.Sub(question.CreatedAt)))
	entry.urgency = urgencyAsked
	if question.Class == reality.ClassBlocking {
		entry.urgency = urgencyBlocked
	}
}

// feedFleetRecord projects another machine's committed record.
//
// Four fields are thinner than a local record's and each absence is the
// merged listings' own judgement. The standing is this machine's derivation
// over records it holds, so a remote record carries none rather than a `new`
// that would say nobody has ruled on it; the topics are resolved through this
// host's session catalog, which by definition does not hold another machine's
// conversations. The reception is the exception and is read exactly as it is
// locally, because the evaluation projection merges what the fleet published:
// a vote on another instance's proposal is a vote this instance has genuinely
// seen. A claim is not merged and does not need to be — a lease is one
// machine's coordination state, so a remote record is under review here only
// when this machine's own reviewers hold it.
func feedFleetRecord(record fleet.Record, reception feedReception) (feedEntry, bool) {
	kind, ok := feedKindOfCatalog(record.Record.Kind)
	if !ok {
		return feedEntry{}, false
	}
	summary, err := record.Summary()
	if err != nil {
		return feedEntry{}, false
	}
	createdAt := record.Record.CreatedAt
	if record.Published != nil && !record.Published.CreatedAt.IsZero() {
		createdAt = record.Published.CreatedAt
	}
	entry := feedEntry{
		post: feedPost{
			ID:        record.Record.RecordID,
			Kind:      string(kind),
			Title:     boundedLine(summary),
			CreatedAt: timeText(createdAt),
			Topics:    []string{},
			Href:      recordHref(record.Record.RecordID),
		},
		createdAt: createdAt,
	}
	if record.Record.RunID != "" {
		entry.post.Author = &feedAuthor{RunID: record.Record.RunID, Href: runHref(record.Record.RunID)}
	}
	reception.apply(&entry, evaluation.Subject{Kind: string(kind), ID: record.Record.RecordID}, createdAt)
	return entry, true
}

// feedKindOfCatalog maps the shared catalog's vocabulary onto the frontier's.
// Only the three post kinds map; a link, a receipt or a disposition is
// machinery rather than something anybody reads, and an observation is
// evidence rather than a post (§4.13), so another machine's observations are
// skipped exactly as this machine's are.
func feedKindOfCatalog(kind sharedcatalog.RecordKind) (frontier.EntityType, bool) {
	switch kind {
	case sharedcatalog.KindHypothesis:
		return frontier.EntityHypothesis, true
	case sharedcatalog.KindFinding:
		return frontier.EntityFinding, true
	case sharedcatalog.KindProposal:
		return frontier.EntityProposal, true
	}
	return "", false
}

// recordHref is the page one record opens on, and questionHref the page a
// question is answered on. They are the two destinations a feed row has, and
// both are built from an identity this server resolved out of its own store:
// no byte of a record's text reaches either.
func recordHref(id string) string { return "/r/" + url.PathEscape(id) }

func questionHref(id string) string { return "/ask/questions/" + url.PathEscape(id) }

// runHref is the run page a post's author reaches (§8.7).
func runHref(runID string) string { return "/watch/runs/" + url.PathEscape(runID) }

// feedCorpus is every record this machine holds, read once so that the
// lineage a topic propagates through can be walked without a second pass.
type feedCorpus struct {
	hypotheses   []frontier.Hypothesis
	observations []frontier.Observation
	findings     []frontier.Finding
	proposals    []frontier.Proposal
}

// readCorpus enumerates the four record kinds, paging each to the store's own
// bound, and keeps the head revision of every chain.
func (s *Server) readCorpus(ctx context.Context) (feedCorpus, error) {
	hypotheses, err := scanFeed(ctx, s.opts.Frontier.Hypotheses)
	if err != nil {
		return feedCorpus{}, err
	}
	observations, err := scanFeed(ctx, s.opts.Frontier.Observations)
	if err != nil {
		return feedCorpus{}, err
	}
	findings, err := scanFeed(ctx, s.opts.Frontier.Findings)
	if err != nil {
		return feedCorpus{}, err
	}
	proposals, err := scanFeed(ctx, s.opts.Frontier.Proposals)
	if err != nil {
		return feedCorpus{}, err
	}
	return feedCorpus{
		hypotheses: heads(hypotheses, func(r frontier.Hypothesis) (string, string) {
			return r.ID, r.AncestorID
		}),
		observations: heads(observations, func(r frontier.Observation) (string, string) {
			return r.ID, r.AncestorID
		}),
		findings: heads(findings, func(r frontier.Finding) (string, string) {
			return r.ID, r.AncestorID
		}),
		proposals: heads(proposals, func(r frontier.Proposal) (string, string) {
			return r.ID, r.AncestorID
		}),
	}, nil
}

// heads keeps the revisions no later wording replaced.
//
// A superseded wording is a record the reader reaches from the one that
// replaced it (§8.6's fifth depth); a feed that listed both would show one
// claim twice and let a reader vote on the wording nobody is using.
//
// It is resolved here rather than by the store's own LeavesOnly filter, and
// the reason is measured rather than stylistic: that filter is a correlated
// NOT EXISTS over the same table, one ancestor column of which this schema
// does not index, and on the operator's own store it cost about four seconds
// per page against three thousand observations. This pass has every record in
// hand already, so the same answer is a map lookup.
func heads[T any](records []T, identity func(T) (id, ancestor string)) []T {
	replaced := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, ancestor := identity(record); ancestor != "" {
			replaced[ancestor] = struct{}{}
		}
	}
	if len(replaced) == 0 {
		return records
	}
	out := make([]T, 0, len(records))
	for _, record := range records {
		id, _ := identity(record)
		if _, superseded := replaced[id]; superseded {
			continue
		}
		out = append(out, record)
	}
	return out
}

// scanFeed pages one enumeration to listScanCap.
//
// The store bounds a single call at frontier.MaxListLimit, which is the right
// bound for a listing route and the wrong one for a projection that has to
// represent the eligible set before it is ranked. So this pages, and stops at
// the same cap every other enumeration on this surface stops at: a corpus
// past it is a deployment that has outgrown an in-memory front page, which is
// a different problem from an unbounded request.
func scanFeed[T any](ctx context.Context,
	list func(context.Context, frontier.ListFilter) ([]T, int, error)) ([]T, error) {
	var out []T
	for offset := 0; offset < listScanCap; offset += frontier.MaxListLimit {
		page, total, err := list(ctx, frontier.ListFilter{
			Limit:  frontier.MaxListLimit,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < frontier.MaxListLimit || len(out) >= total {
			break
		}
	}
	return out, nil
}

// topics files every record under the workspaces its evidence came from.
//
// The propagation is the development path read backwards, because that is
// where the citations are. An observation cites sessions directly; a candidate
// is filed under the observations that develop it; a finding under the
// observations it consolidates and the candidates behind them; a proposal
// under the findings it rests on and the candidates it answers. A record whose
// citations name no session this host holds is unfiled, which is the honest
// answer rather than a guess: the conversation exists and this machine cannot
// say where it happened.
func (c feedCorpus) topics(sessions map[string][]SessionRow) map[string][]string {
	byObservation := make(map[string]map[string]struct{}, len(c.observations))
	byHypothesis := make(map[string]map[string]struct{}, len(c.hypotheses))
	for _, record := range c.observations {
		names := map[string]struct{}{}
		citedTopics(record.Payload.Evidence, sessions, names)
		citedTopics(record.Payload.CounterEvidence, sessions, names)
		byObservation[record.ID] = names
		if record.HypothesisID == "" {
			continue
		}
		into, seen := byHypothesis[record.HypothesisID]
		if !seen {
			into = map[string]struct{}{}
			byHypothesis[record.HypothesisID] = into
		}
		mergeTopics(into, names)
	}
	out := make(map[string][]string, len(c.observations)+len(c.hypotheses)+
		len(c.findings)+len(c.proposals))
	for id, names := range byObservation {
		out[id] = topicNames(names)
	}
	for _, record := range c.hypotheses {
		out[record.ID] = topicNames(byHypothesis[record.ID])
	}
	byFinding := make(map[string]map[string]struct{}, len(c.findings))
	for _, record := range c.findings {
		names := map[string]struct{}{}
		citedTopics(record.Payload.CounterEvidence, sessions, names)
		for _, id := range record.ObservationIDs {
			mergeTopics(names, byObservation[id])
		}
		for _, id := range record.HypothesisIDs {
			mergeTopics(names, byHypothesis[id])
		}
		byFinding[record.ID] = names
		out[record.ID] = topicNames(names)
	}
	for _, record := range c.proposals {
		names := map[string]struct{}{}
		citedTopics(record.Payload.Supporting, sessions, names)
		citedTopics(record.Payload.Conflicting, sessions, names)
		for _, id := range record.FindingIDs {
			mergeTopics(names, byFinding[id])
		}
		for _, id := range record.HypothesisIDs {
			mergeTopics(names, byHypothesis[id])
		}
		out[record.ID] = topicNames(names)
	}
	return out
}

// citedTopics resolves one citation set to the topics it came from.
func citedTopics(items []frontier.Evidence, sessions map[string][]SessionRow, into map[string]struct{}) {
	for _, item := range items {
		row, ok := matchSession(sessions, item.Locator().Path)
		if !ok {
			continue
		}
		if name := topicOf(row); name != "" {
			into[name] = struct{}{}
		}
	}
}

func mergeTopics(into, from map[string]struct{}) {
	for name := range from {
		into[name] = struct{}{}
	}
}

// topicNames is the sorted, deduplicated vocabulary a record is filed under.
// Sorted because a record's topics are a set and a set rendered in map order
// would reshuffle between two reads of the same row.
func topicNames(names map[string]struct{}) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// commentsPathSuffix is the conversation under a record.
const commentsPathSuffix = "/comments"

// commentThread is GET /api/record/{id}/comments.
//
// Comments and acts are two lists rather than one stream, and that is §8.7's
// line rather than a rendering preference: a ruling is the moderator's log and
// renders as the act it is, attributed and dated, never as a comment. A reader
// who could not tell them apart would read "reject" as somebody's opinion.
type commentThread struct {
	Comments []commentView `json:"comments"`
	Acts     []actView     `json:"acts"`
	Total    int           `json:"total"`
}

// commentView is one thing somebody said under a record.
type commentView struct {
	ID     string        `json:"id"`
	Kind   string        `json:"kind"`
	Author commentAuthor `json:"author"`
	Role   string        `json:"role"`
	Text   string        `json:"text"`
	At     string        `json:"at"`
	// RelatedID is what this comment is about, which is how the thread
	// nests: a refinement names the statement it revises, a reconsideration
	// names the item it answers. It stays on the wire beside the nesting so
	// a client can follow a relation to a record the thread does not hold.
	RelatedID string        `json:"related_id"`
	Replies   []commentView `json:"replies"`
}

// commentAuthor attributes one comment. Href is empty for the operator, who
// has no page: he is the person reading, and a link to himself would be the
// surface inventing a profile.
type commentAuthor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Href string `json:"href"`
}

// actView is one §4.7 ruling in the moderator's log.
type actView struct {
	ID     string `json:"id"`
	Act    string `json:"act"`
	By     string `json:"by"`
	At     string `json:"at"`
	Reason string `json:"reason"`
}

// The comment kinds §8.7 names, as the thread renders them.
//
// commentQuestion is the one the operator can ask for. It is §8.7's `ask`:
// "a question to Babel about this record, recorded as a comment Babel's next
// review of the record must answer". It reads differently from his other
// prose because it is a different act — a comment says something and a
// question asks for something — and a thread that rendered them alike would
// leave the reader unable to see what is still owed an answer.
const (
	commentContribution    = "contribution"
	commentRefinement      = "refinement"
	commentReason          = "reason"
	commentQuestion        = "question"
	commentAnswer          = "answer"
	commentReconsideration = "reconsideration"
)

// commentAsk is the vocabulary POST /api/record/{id}/comments accepts, which
// is narrower than what the thread renders: the five other kinds are acts
// runs and the ledger perform, and a request that could name one would be
// the operator authoring a reviewer's contribution.
const (
	askComment  = "comment"
	askQuestion = "question"
)

func commentAsks() []string { return []string{askComment, askQuestion} }

// commentRequest is POST /api/record/{id}/comments: the operator's own words,
// and whether they are a statement or a question.
//
// There is no stance field and that is the act rather than an omission: the
// operator does not vote (§8.7), so the acts a record offers him are the
// rulings and this box. Kind carries the one distinction the box itself needs
// — saying something, or asking Babel something a later review must answer —
// and absent means a comment, because that is what a box with no marker on it
// has always recorded.
type commentRequest struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// commentResult confirms what was recorded.
type commentResult struct {
	Comment commentView `json:"comment"`
}

// handleRecordComments serves the conversation under one record.
func (s *Server) handleRecordComments(w http.ResponseWriter, r *http.Request, id string) {
	thread := commentThread{Comments: []commentView{}, Acts: []actView{}}
	var comments []commentView
	if question, ok := s.questionComments(r.Context(), id); ok {
		comments = question
	} else {
		kind, known := kindOfRecordID(id)
		if !known {
			s.writeError(w, http.StatusBadRequest,
				"that identifier names no record kind this surface can open")
			return
		}
		comments = s.evaluationComments(r, evaluation.Subject{Kind: string(kind), ID: id})
		thread.Acts = s.recordActs(r.Context(), frontier.Ref{Type: kind, ID: id})
	}
	thread.Total = len(comments)
	thread.Comments = threadComments(comments)
	s.writeJSON(w, http.StatusOK, thread)
}

// handlePostComment records the operator's own words under a record: what he
// said about it, or what he is asking Babel about it.
//
// Both are one operator-authored feedback record with a reason and no stance,
// which §4.12 already admits and §8.7 asks for by name. Nothing about the
// authority boundary moves to make room for either: OperatorKinds still
// excludes an assessment, so this write cannot mint what reads as a model's
// observation, and it sets no disposition — saying something is not deciding
// anything, and neither is asking.
//
// A question is the same record carrying a marker rather than a record of its
// own, because the marker is what a later review of this record reads to find
// what it owes an answer to. Two families would mean two reads.
func (s *Server) handlePostComment(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	kind, known := kindOfRecordID(id)
	if !known {
		s.writeError(w, http.StatusBadRequest,
			"that identifier names no record kind this surface can open")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	var request commentRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	ask := request.Kind
	if ask == "" {
		ask = askComment
	}
	if !contains(commentAsks(), ask) {
		s.writeError(w, http.StatusBadRequest, "a box under a record records a comment or a question, "+
			"and there is no such thing as a "+strconv.Quote(ask))
		return
	}
	if strings.TrimSpace(request.Text) == "" {
		if ask == askQuestion {
			s.writeError(w, http.StatusBadRequest, "a question asks something; this one is empty")
			return
		}
		s.writeError(w, http.StatusBadRequest, "a comment says something; this one is empty")
		return
	}
	record, refresh, err := s.opts.Evaluation.OperatorDeferred(r.Context(), evaluation.OperatorInput{
		Subject:  evaluation.Subject{Kind: string(kind), ID: id},
		Kind:     evaluation.KindFeedback,
		Operator: by.ID(),
		Reason:   request.Text,
		Question: ask == askQuestion,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.refreshEvaluation(record.Subject, refresh)
	s.writeJSON(w, http.StatusCreated, commentResult{Comment: commentView{
		ID:        record.ID,
		Kind:      operatorCommentKind(record),
		Author:    commentAuthor{Kind: evaluation.ActorOperator, ID: record.ActorID},
		Text:      record.Reason,
		At:        timeText(record.CreatedAt),
		RelatedID: record.RelatedID,
		Replies:   []commentView{},
	}})
}

// operatorCommentKind reads what the stored record says it is, so the 201 and
// the thread cannot describe one write in two vocabularies.
func operatorCommentKind(record evaluation.Record) string {
	if record.Question {
		return commentQuestion
	}
	return commentReason
}

// evaluationComments reads what Babel's reviewers and the operator said about
// one record.
//
// A bare vote is not here and that is §4.12's distinction kept: a support with
// no prose is a reviewer's position, which the score already carries, and
// rendering it as an empty comment would put a row in the conversation that
// says nothing.
func (s *Server) evaluationComments(r *http.Request, subject evaluation.Subject) []commentView {
	if s.opts.Evaluation == nil {
		return nil
	}
	records, err := s.opts.Evaluation.Thread(r.Context(), subject)
	if err != nil {
		s.logf("GET %s: the evaluation thread could not be read", r.URL.Path)
		return nil
	}
	var out []commentView
	for _, entry := range records {
		record := entry.Record
		switch record.Kind {
		case evaluation.KindAssessment:
			if record.Assessment == nil || record.ActorKind != evaluation.ActorRun {
				continue
			}
			for position, contribution := range record.Assessment.Contributions {
				text := strings.TrimSpace(contribution.Text)
				if text == "" {
					continue
				}
				kind := commentContribution
				if contribution.Kind == evaluation.ContributionRefinement {
					kind = commentRefinement
				}
				out = append(out, commentView{
					ID:     contributionID(record.ID, position),
					Kind:   kind,
					Author: commentAuthor{Kind: evaluation.ActorRun, ID: record.ActorID, Href: runHref(record.ActorID)},
					Role:   entry.Role,
					Text:   text,
					At:     timeText(record.CreatedAt),
					// A contribution is about the statement it was
					// recorded with, so a correction's prose nests under
					// the prose it revises rather than beside it.
					RelatedID: record.SupersedesID,
					Replies:   []commentView{},
				})
			}
		case evaluation.KindFeedback:
			if strings.TrimSpace(record.Reason) == "" {
				continue
			}
			out = append(out, commentView{
				ID:        record.ID,
				Kind:      operatorCommentKind(record),
				Author:    commentAuthor{Kind: evaluation.ActorOperator, ID: record.ActorID},
				Text:      record.Reason,
				At:        timeText(record.CreatedAt),
				RelatedID: record.RelatedID,
				Replies:   []commentView{},
			})
		case evaluation.KindReconsider, evaluation.KindReconsiderDecision:
			if strings.TrimSpace(record.Reason) == "" {
				continue
			}
			author := commentAuthor{Kind: record.ActorKind, ID: record.ActorID}
			if record.ActorKind == evaluation.ActorRun {
				author.Href = runHref(record.ActorID)
			}
			out = append(out, commentView{
				ID:        record.ID,
				Kind:      commentReconsideration,
				Author:    author,
				Text:      record.Reason,
				At:        timeText(record.CreatedAt),
				RelatedID: record.RelatedID,
				Replies:   []commentView{},
			})
		}
	}
	return out
}

// contributionID names one contribution inside the assessment that carried
// it. A contribution has no identity of its own in the store, and a thread
// whose rows shared one id could not nest or be replied to.
func contributionID(recordID string, position int) string {
	return recordID + "#" + strconv.Itoa(position)
}

// questionComments reads the answers under a question, reporting whether the
// identifier named one at all.
//
// An answer is a comment for §8.7's reason — it is prose somebody wrote under
// something Babel published — and it is the operator's, kept verbatim, which
// is what §4.8 requires of it.
func (s *Server) questionComments(ctx context.Context, id string) ([]commentView, bool) {
	if !strings.HasPrefix(id, questionIDPrefix) {
		return nil, false
	}
	if s.opts.Reality == nil {
		return nil, true
	}
	answers, err := s.opts.Reality.Answers(ctx, id)
	if err != nil {
		return nil, true
	}
	out := make([]commentView, 0, len(answers))
	for _, answer := range answers {
		out = append(out, commentView{
			ID:      answer.ID,
			Kind:    commentAnswer,
			Author:  commentAuthor{Kind: evaluation.ActorOperator, ID: answer.Author},
			Text:    answer.Payload.Text,
			At:      timeText(answer.At),
			Replies: []commentView{},
		})
	}
	return out, true
}

// questionIDPrefix is the family prefix internal/reality mints a question
// under. The comments route resolves it here rather than widening
// kindOfRecordID, which answers a different question: which of the frontier's
// four kinds a record page may open.
const questionIDPrefix = "qst_"

// recordActs reads the moderator's log: §4.7's append-only rulings, as the
// attributed acts they are.
func (s *Server) recordActs(ctx context.Context, ref frontier.Ref) []actView {
	// Empty is a list, not null: a client renders "no rulings" from a
	// length and should not have to guard against a missing field.
	if s.opts.Review == nil || !reviewableRecord(ref.Type) {
		return []actView{}
	}
	history, err := s.opts.Review.History(ctx, ref)
	if err != nil {
		return []actView{}
	}
	acts := make([]actView, 0, len(history.Decisions))
	for _, entry := range history.Decisions {
		acts = append(acts, actView{
			ID:     entry.Event.ID,
			Act:    string(entry.Event.Disposition),
			By:     entry.Event.ReviewerID,
			At:     timeText(entry.Event.RecordedAt),
			Reason: entry.Event.Payload.Note,
		})
	}
	// Newest first, like the comments beside them: a reader opening a
	// thread is looking for what happened last.
	for left, right := 0, len(acts)-1; left < right; left, right = left+1, right-1 {
		acts[left], acts[right] = acts[right], acts[left]
	}
	return acts
}

// threadComments nests replies under what they are about and orders every
// level newest first.
//
// A comment whose related record is not in this thread stays at the top
// level rather than being dropped: it is still something somebody said about
// this record, and hiding it because its parent lives elsewhere would lose
// prose to a relation the reader never asked about.
func threadComments(flat []commentView) []commentView {
	if len(flat) == 0 {
		return []commentView{}
	}
	index := make(map[string]int, len(flat))
	for position, comment := range flat {
		index[comment.ID] = position
		// A contribution's parent may be named by the record it belongs to
		// rather than by the contribution itself, which is the only
		// identity a correction carries.
		if base, _, cut := strings.Cut(comment.ID, "#"); cut {
			if _, taken := index[base]; !taken {
				index[base] = position
			}
		}
	}
	nested := make([]commentView, len(flat))
	copy(nested, flat)
	roots := make([]int, 0, len(nested))
	children := make(map[int][]int, len(nested))
	for position, comment := range nested {
		parent, found := index[comment.RelatedID]
		if comment.RelatedID == "" || !found || parent == position {
			roots = append(roots, position)
			continue
		}
		children[parent] = append(children[parent], position)
	}
	// The walk carries a visited set rather than trusting the relations to
	// be a forest. They are, today: a correction chain is a line the store's
	// own unique index keeps linear, and a record always names one written
	// before it. But the relations come out of a durable log this process
	// did not write, and a reader that could be sent into unbounded
	// recursion by a cycle in it is a page that crashes the launch.
	seen := make(map[int]struct{}, len(nested))
	var assemble func(position int) commentView
	assemble = func(position int) commentView {
		comment := nested[position]
		comment.Replies = []commentView{}
		seen[position] = struct{}{}
		kids := children[position]
		sortComments(nested, kids)
		for _, child := range kids {
			if _, walked := seen[child]; walked {
				continue
			}
			comment.Replies = append(comment.Replies, assemble(child))
		}
		return comment
	}
	sortComments(nested, roots)
	out := make([]commentView, 0, len(roots))
	for _, root := range roots {
		if _, walked := seen[root]; walked {
			continue
		}
		out = append(out, assemble(root))
	}
	return out
}

// sortComments orders one level newest first, ties by identifier so a thread
// reads the same way twice.
func sortComments(comments []commentView, positions []int) {
	sort.SliceStable(positions, func(a, b int) bool {
		left, right := comments[positions[a]], comments[positions[b]]
		if left.At != right.At {
			return left.At > right.At
		}
		return left.ID < right.ID
	})
}
