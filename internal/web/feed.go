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
	// Score is Support minus Oppose and every one of these counts the
	// operator's own stance beside Babel's reviewers, because §8.7 makes him
	// one voter among them for the purpose of the number. The separation
	// §4.12 requires is kept by You, which names his stance: a breakdown
	// subtracts it to render Babel's own reception on its own.
	Score          int    `json:"score"`
	Support        int    `json:"support"`
	Oppose         int    `json:"oppose"`
	Unsure         int    `json:"unsure"`
	You            string `json:"you"`
	Comments       int    `json:"comments"`
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
	Posts   []feedPost `json:"posts"`
	Total   int        `json:"total"`
	Sort    string     `json:"sort"`
	T       string     `json:"t"`
	Topic   string     `json:"topic"`
	Kinds   []string   `json:"kinds"`
	BuiltAt string     `json:"built_at"`
	Notice  string     `json:"notice"`
}

// topicCount is one community in the sidebar: what it is called, how much is
// in it, and — since topics became repository-bound (§4.13) — what the name
// is actually bound to and how the filing was produced.
type topicCount struct {
	Name     string `json:"name"`
	Posts    int    `json:"posts"`
	LatestAt string `json:"latest_at"`
	// Binding is the real thing the name names, and is null only when this
	// deployment cannot bind the name to one repository.
	Binding *topicBinding `json:"binding"`
	// Heuristic is true for every topic this deployment seeds, and stays
	// true until §4.13's triage recipe has run: these filings come from
	// repository identity alone, with no model and no operator act behind
	// them, and the section requires them to say so rather than to read as
	// entities somebody created.
	Heuristic bool `json:"heuristic"`
}

// topicList is GET /api/topics. Unfiled is counted rather than named, because
// the records in it have nothing in common except that this deployment could
// not resolve where they came from.
type topicList struct {
	Topics  []topicCount `json:"topics"`
	Unfiled int          `json:"unfiled"`
}

// The sorts §8.7 names. They are a closed set and an unknown one is refused:
// a misspelled sort answered with the default would silently show a reader a
// different order from the one he asked for.
const (
	sortHot           = "hot"
	sortNew           = "new"
	sortTop           = "top"
	sortControversial = "controversial"
	sortRising        = "rising"
)

func feedSorts() []string {
	return []string{sortHot, sortNew, sortTop, sortControversial, sortRising}
}

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

// The post kinds. Four record kinds plus the questions Babel asks, which §8.7
// puts in the feed beside them: a question is something the deployment
// produced and is waiting on an answer to, which is exactly what a post is.
const feedKindQuestion = "question"

func feedKinds() []string {
	return []string{
		string(frontier.EntityHypothesis),
		string(frontier.EntityObservation),
		string(frontier.EntityFinding),
		string(frontier.EntityProposal),
		feedKindQuestion,
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

// feedIndex is the projection: one entry per post, plus the topic vocabulary
// derived from the same pass.
type feedIndex struct {
	builtAt time.Time
	cost    time.Duration
	posts   []feedEntry
	topics  []topicCount
	unfiled int
	// notice is what the response says when the deployment could not be
	// consulted. It is the listings' own sentence, because the reader is
	// owed the same fact on the front page he is owed in a listing: these
	// rows are what this machine could reach.
	notice string
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
	topics   []string
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
	eligible := filterFeed(index.posts, topic, kinds, sortBy, window, now)
	sortFeed(eligible, sortBy, now)
	result := feedList{
		Posts:   []feedPost{},
		Total:   len(eligible),
		Sort:    sortBy,
		T:       window,
		Topic:   topic,
		Kinds:   kinds,
		BuiltAt: timeText(index.builtAt),
		Notice:  index.notice,
	}
	if result.Kinds == nil {
		result.Kinds = []string{}
	}
	for i := offset; i < len(eligible) && i < offset+limit; i++ {
		result.Posts = append(result.Posts, eligible[i].post)
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleTopics serves the sidebar's vocabulary: what a record's evidence came
// from, with how much of it there is.
func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	index, err := s.feedIndex(r)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := topicList{Topics: []topicCount{}, Unfiled: index.unfiled}
	result.Topics = append(result.Topics, index.topics...)
	s.writeJSON(w, http.StatusOK, result)
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
func filterFeed(posts []feedEntry, topic string, kinds []string, sortBy, window string,
	now time.Time) []feedEntry {
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

func entryInTopic(entry feedEntry, topic string) bool {
	if topic == topicUnfiled {
		return len(entry.topics) == 0
	}
	return contains(entry.topics, topic)
}

// sortFeed orders the eligible set. Ties resolve newer first everywhere, and
// then by identifier, so one corpus has one order rather than a different one
// per rebuild.
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
// The session catalog is read once because the topics of every record resolve
// through it; the four enumerations are read once because the lineage that
// files a proposal under a topic runs backwards through them; the rulings and
// the reception are one read each rather than one per record, which is the
// difference between a front page and a scan.
func (s *Server) buildFeedIndex(r *http.Request) (*feedIndex, error) {
	started := time.Now()
	ctx := r.Context()
	index := &feedIndex{builtAt: started.UTC()}

	sessions := s.sessionsBySourceID(ctx)
	corpus, err := s.readCorpus(ctx)
	if err != nil {
		return nil, err
	}
	topics := corpus.topics(sessions)

	standings, err := s.opts.Frontier.ReviewStandings(ctx)
	if err != nil {
		// A ruling this surface could not derive is a standing it does not
		// state, never a standing it invents. The feed renders without one
		// rather than telling a reader that nobody has decided.
		s.logf("GET %s: review standings unread; the feed renders without them", r.URL.Path)
		standings = nil
	}
	tallies := s.feedTallies(ctx, r)

	for _, record := range corpus.hypotheses {
		index.add(s.feedRecord(frontier.EntityHypothesis, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Statement, topics[record.ID], standings, tallies))
	}
	for _, record := range corpus.observations {
		index.add(s.feedRecord(frontier.EntityObservation, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Claim, topics[record.ID], standings, tallies))
	}
	for _, record := range corpus.findings {
		index.add(s.feedRecord(frontier.EntityFinding, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Title, topics[record.ID], standings, tallies))
	}
	for _, record := range corpus.proposals {
		index.add(s.feedRecord(frontier.EntityProposal, record.ID, record.RunID, record.CreatedAt,
			record.Payload.Title, topics[record.ID], standings, tallies))
	}
	for _, entry := range s.feedQuestions(ctx, r) {
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
			sharedcatalog.KindObservation, sharedcatalog.KindFinding, sharedcatalog.KindProposal)
		if degraded {
			index.notice = catalogUnreachable
		}
		for _, record := range merged {
			if entry, ok := feedFleetRecord(record, tallies); ok {
				index.add(entry)
			}
		}
	}

	index.countTopics(sessions)
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

// countTopics derives the sidebar from the posts themselves, so a topic's
// count and the feed it opens cannot disagree, and binds each name to the
// repository it came from (§4.13).
//
// The counts come from the posts and the bindings from the catalog, because
// the two answer different questions: how much is filed here, and what this
// name actually is. A topic with posts and no binding is a name this host
// cannot resolve to one repository, never a name it made up.
func (i *feedIndex) countTopics(sessions map[string][]SessionRow) {
	counts := map[string]int{}
	latest := map[string]time.Time{}
	for _, entry := range i.posts {
		if len(entry.topics) == 0 {
			i.unfiled++
			continue
		}
		for _, topic := range entry.topics {
			counts[topic]++
			if entry.createdAt.After(latest[topic]) {
				latest[topic] = entry.createdAt
			}
		}
	}
	bindings := topicBindings(sessions)
	i.topics = make([]topicCount, 0, len(counts))
	for name, count := range counts {
		i.topics = append(i.topics, topicCount{
			Name: name, Posts: count, LatestAt: timeText(latest[name]),
			Binding: bindings[name], Heuristic: true,
		})
	}
	// Busiest first, then by name: a sidebar that reshuffled two equal
	// topics between two reads would be unreadable as a list.
	sort.Slice(i.topics, func(a, b int) bool {
		if i.topics[a].Posts != i.topics[b].Posts {
			return i.topics[a].Posts > i.topics[b].Posts
		}
		return i.topics[a].Name < i.topics[b].Name
	})
}

// feedRecord projects one local record into a post.
func (s *Server) feedRecord(kind frontier.EntityType, id, runID string, createdAt time.Time,
	claim string, topics []string, standings map[frontier.Ref]frontier.ReviewStanding,
	tallies map[evaluation.Subject]evaluation.Tally) feedEntry {
	entry := feedEntry{
		post: feedPost{
			ID:        id,
			Kind:      string(kind),
			Title:     boundedLine(claim),
			Standing:  feedStanding(kind, frontier.Ref{Type: kind, ID: id}, standings),
			CreatedAt: timeText(createdAt),
			Topics:    idList(topics),
			Href:      recordHref(id),
		},
		createdAt: createdAt,
		topics:    topics,
	}
	if runID != "" {
		entry.post.Author = &feedAuthor{RunID: runID, Href: runHref(runID)}
	}
	applyTally(&entry, tallies[evaluation.Subject{Kind: string(kind), ID: id}], createdAt)
	return entry
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
// The operator's stance is added to the columns rather than kept beside them,
// which is §8.7's own arithmetic: "the score is support minus oppose across
// all of them, because a vote is a vote and the operator is one voter among
// Babel's reviewers". What keeps §4.12's boundary is You: it names his own
// position, so a breakdown can render Babel's reception without him and never
// presents his click as a model's observation.
func applyTally(entry *feedEntry, tally evaluation.Tally, createdAt time.Time) {
	entry.post.Support, entry.post.Oppose, entry.post.Unsure = tally.Support, tally.Oppose, tally.Unsure
	entry.post.You = tally.Stance
	switch tally.Stance {
	case evaluation.StanceAgree:
		entry.post.Support++
	case evaluation.StanceDisagree:
		entry.post.Oppose++
	case evaluation.StanceUnsure:
		entry.post.Unsure++
	}
	entry.post.Score = entry.post.Support - entry.post.Oppose
	entry.post.Comments = tally.Comments
	entry.activity = tally.Activity
	entry.lastActivity = tally.LastActivity
	if entry.lastActivity.Before(createdAt) {
		entry.lastActivity = createdAt
	}
	entry.post.LastActivityAt = timeText(entry.lastActivity)
}

// feedTallies reads the deployment's reception in one grouped pass, reporting
// nothing rather than failing when the evaluation store could not answer.
//
// A feed whose scores could not be read is still the feed: every claim, every
// topic and every comment count beside it is unaffected, and a front page
// that refused because one store was down would take the corpus away over its
// scoreboard.
func (s *Server) feedTallies(ctx context.Context, r *http.Request) map[evaluation.Subject]evaluation.Tally {
	if s.opts.Evaluation == nil {
		return nil
	}
	tallies, err := s.opts.Evaluation.Tallies(ctx)
	if err != nil {
		s.logf("GET %s: evaluation tallies unread; the feed renders unscored", r.URL.Path)
		return nil
	}
	return tallies
}

// feedQuestions projects the ledger's questions, which §8.7 puts in the feed
// beside the records.
//
// A question carries no run author: the ledger records what was asked and why,
// and the run that provoked it is not part of the question. Its standing is
// its own state, which is the vocabulary its page shows.
func (s *Server) feedQuestions(ctx context.Context, r *http.Request) []feedEntry {
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
		out = append(out, entry)
	}
	return out
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
// seen.
func feedFleetRecord(record fleet.Record,
	tallies map[evaluation.Subject]evaluation.Tally) (feedEntry, bool) {
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
	applyTally(&entry, tallies[evaluation.Subject{Kind: string(kind), ID: record.Record.RecordID}], createdAt)
	return entry, true
}

// feedKindOfCatalog maps the shared catalog's vocabulary onto the frontier's.
// Only the four analysis kinds are posts; a link, a receipt or a disposition
// is machinery rather than something anybody reads.
func feedKindOfCatalog(kind sharedcatalog.RecordKind) (frontier.EntityType, bool) {
	switch kind {
	case sharedcatalog.KindHypothesis:
		return frontier.EntityHypothesis, true
	case sharedcatalog.KindObservation:
		return frontier.EntityObservation, true
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

// The comment kinds §8.7 names.
const (
	commentContribution    = "contribution"
	commentRefinement      = "refinement"
	commentReason          = "reason"
	commentAnswer          = "answer"
	commentReconsideration = "reconsideration"
)

// commentRequest is POST /api/record/{id}/comments: the operator's own words,
// and nothing else.
//
// There is no stance field and that is the act rather than an omission: §8.7
// gives the operator a box that "records a feedback record carrying a reason
// and no polarity change". His arrows are the reception route; a comment that
// could also move his vote would make one gesture do two things, and a reader
// could no longer tell which of them he meant.
type commentRequest struct {
	Text string `json:"text"`
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

// handlePostComment records the operator's own words under a record.
//
// It is an operator-authored feedback record with a reason and no stance,
// which §4.12 already admits and §8.7 asks for by name. Nothing about the
// authority boundary moves to make room for it: OperatorKinds still excludes
// an assessment, so this write cannot mint what reads as a model's
// observation, and it sets no disposition — saying something is not deciding
// anything.
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
	if strings.TrimSpace(request.Text) == "" {
		s.writeError(w, http.StatusBadRequest, "a comment says something; this one is empty")
		return
	}
	record, refresh, err := s.opts.Evaluation.OperatorDeferred(r.Context(), evaluation.OperatorInput{
		Subject:  evaluation.Subject{Kind: string(kind), ID: id},
		Kind:     evaluation.KindFeedback,
		Operator: by.ID(),
		Reason:   request.Text,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.refreshReception(record.Subject, refresh)
	s.writeJSON(w, http.StatusCreated, commentResult{Comment: commentView{
		ID:        record.ID,
		Kind:      commentReason,
		Author:    commentAuthor{Kind: evaluation.ActorOperator, ID: record.ActorID},
		Text:      record.Reason,
		At:        timeText(record.CreatedAt),
		RelatedID: record.RelatedID,
		Replies:   []commentView{},
	}})
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
				Kind:      commentReason,
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
