package web

// The palette's lookup: what things are called, and where each one is.
//
// Decision 90's ⌘K is a locator rather than a second retrieval surface. The
// operator remembers three words of something he read and wants to be on its
// page, so this answers with names — a record's own title, a session's title,
// a subject's display name, an open question's prompt — and never with a
// passage out of a transcript. That is the whole reason it is a separate route
// from /api/search: that one is §5.4 retrieval over the archived corpus, whose
// hit is an event inside a conversation and carries a locator rather than a
// destination. Folding the two together would put two different kinds of
// answer under one name, and the palette would have to guess which it got.
//
// The path says what is searched. /api/search is the corpus and stays exactly
// what it was; /api/search/names is the index of what things are called, which
// is the question a jump box asks. It resolves from routeAPI's default branch
// for recordPathPrefix's reason: the named path above it already matched, so
// nothing here can swallow it.
//
// Every source is read live rather than through a prepared index, and the two
// exceptions to that are exceptions of the stores rather than choices. A
// proposal is not in the retrieval index at all — frontier.OutputKind has no
// proposal — and an observation is in nothing else, because the durable store
// offers no enumeration of observations and §6.7 keeps them out of the review
// queue that enumerates findings. So the three kinds that can be enumerated
// are matched against the rows themselves, which is what makes a record
// findable the moment a run writes it, and observations are matched through
// the index when this build wired one. An absent index costs the observation
// rows and says nothing about the others.
//
// A hit's title always contains what was typed, and that is a property of the
// line this file composes rather than of the match alone. Matching runs
// against the one line the palette renders — never a hidden field, and never
// past the first newline — because a row whose visible text does not contain
// the query reads as a bug in the search: the operator cannot see why it is
// there. A record's own words run to several hundred characters where a row is
// one line, so a match deep in a statement travels with the words just before
// it rather than being cut off the front. It is the same rule that makes the
// index's candidates confirmed by substring here rather than trusted — full
// text matched a word the summary may not carry.

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
)

// nameSearchPath is the palette's lookup.
const nameSearchPath = "/api/search/names"

const (
	// defaultNameHits is a palette's worth of rows, and maxNameHits is the
	// ceiling a caller may raise it to. Both are small on purpose: this
	// answers a keystroke, and a page nobody scrolls is the wrong thing to
	// spend a corpus scan on.
	defaultNameHits = 20
	maxNameHits     = 50
	// observationScan bounds the retrieval index page the observation
	// candidates come out of. It is larger than the answer because the
	// index matches full text and this file matches titles, so some of the
	// page is discarded.
	observationScan = 200
	// excerptHead is how far into a line a match may sit and still be read
	// where it is: about a row's width, so the operator sees why the row is
	// on screen without the line having to be re-cut.
	excerptHead = 96
	// excerptLeadIn is how much of the words before a deeper match travel
	// with it, so a re-cut line still reads as a sentence rather than
	// starting mid-word.
	excerptLeadIn = 40
)

// The three kinds this surface names that are not frontier records. The
// frontier's own four come from internal/frontier, so no kind on the wire is
// spelled twice.
const (
	kindSession  = "session"
	kindEntity   = "entity"
	kindQuestion = "question"
)

// routeSearch dispatches the palette's lookup, reporting whether the path was
// it.
func (s *Server) routeSearch(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != nameSearchPath {
		return false
	}
	if !s.requireMethod(w, r, http.MethodGet) {
		return true
	}
	s.handleNameSearch(w, r)
	return true
}

// nameHit is one destination the operator can name.
//
// Href is the route rather than an identifier and a kind for the client to
// reassemble, because five of the seven kinds route differently and the
// mapping is this server's knowledge. It is safe to emit for sessionHref's
// reason: every part of it is an identity resolved out of this deployment's
// own stores, and no byte of a record's text reaches it.
//
// Meta is one short fact, and which fact it is differs by kind because the
// useful one differs: a record is told apart from a similarly worded record by
// when it was made, a session by the workspace it was held in, a subject by
// what kind of thing it is, a question by how badly it is wanted.
type nameHit struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
	Href  string `json:"href"`
	Meta  string `json:"meta,omitempty"`
}

type nameSearchResult struct {
	Hits []nameHit `json:"hits"`
}

// The two ways a title matches, in the order a reader wants them. A title that
// begins with what was typed is what he was reaching for; a title that merely
// contains it is a neighbour.
const (
	matchPrefix = iota
	matchSubstring
)

// nameCandidate is one hit with what it takes to order it.
type nameCandidate struct {
	hit  nameHit
	rank int
	// at is the recency tiebreak within a rank: the record's creation, the
	// session's modification, the subject's last fact. Zero where the
	// source does not carry one, which sorts it last rather than oldest.
	at time.Time
}

// handleNameSearch serves the palette's lookup.
//
// An empty query answers with an empty list rather than with the corpus. The
// palette opens before anything is typed and asks as soon as it is, so the
// first request is routinely the one with nothing in it, and the honest answer
// to "find things called nothing" is none of them.
func (s *Server) handleNameSearch(w http.ResponseWriter, r *http.Request) {
	searchable := s.opts.Frontier != nil || s.opts.Reality != nil || s.opts.Lister != nil
	if !s.requireService(w, searchable, "a store this deployment names things in") {
		return
	}
	limit, ok := queryInt(r, "limit", defaultNameHits)
	if !ok || limit <= 0 || limit > maxNameHits {
		s.writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
		return
	}
	want := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if want == "" {
		s.writeJSON(w, http.StatusOK, nameSearchResult{Hits: []nameHit{}})
		return
	}

	candidates, err := s.frontierNames(r.Context(), want, nil)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	candidates = s.observationNames(r.Context(), want, candidates)
	candidates, err = s.realityNames(r.Context(), want, candidates)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	candidates, err = s.sessionNames(r.Context(), want, candidates)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, nameSearchResult{Hits: pick(candidates, limit)})
}

// frontierNames matches the three record kinds a store enumerates.
//
// Each enumeration asks for head revisions only, because a palette offers
// destinations and a superseded wording is not one: the record's current words
// are on the page the head's row opens, with the chain readable at depth five.
// Every scan is bounded by listScanCap, like every other list route on this
// surface.
func (s *Server) frontierNames(ctx context.Context, want string,
	into []nameCandidate) ([]nameCandidate, error) {
	if s.opts.Frontier == nil {
		return into, nil
	}
	scan := frontier.ListFilter{LeavesOnly: true, Limit: listScanCap}
	candidates, _, err := s.opts.Frontier.Hypotheses(ctx, scan)
	if err != nil {
		return into, err
	}
	for _, record := range candidates {
		into = consider(into, record.Payload.Statement, want, record.CreatedAt,
			recordHit(frontier.EntityHypothesis, record.ID, day(record.CreatedAt)))
	}
	proposals, _, err := s.opts.Frontier.Proposals(ctx, scan)
	if err != nil {
		return into, err
	}
	for _, record := range proposals {
		into = consider(into, record.Payload.Title, want, record.CreatedAt,
			recordHit(frontier.EntityProposal, record.ID, day(record.CreatedAt)))
	}
	if s.opts.Review == nil {
		return into, nil
	}
	// The review queue is the only enumeration of findings any service
	// offers, which handleFindings states; a consolidation nobody enrolled
	// is unreachable from this route for the same reason it is unreachable
	// from that listing.
	enrolled, err := s.opts.Review.Queue(ctx, review.QueueFilter{
		Type:        frontier.EntityFinding,
		AllStatuses: true,
		Limit:       listScanCap,
	})
	if err != nil {
		return into, err
	}
	for _, item := range enrolled {
		record, err := s.opts.Frontier.Finding(ctx, item.Subject.ID)
		if err != nil {
			return into, err
		}
		into = consider(into, record.Payload.Title, want, record.CreatedAt,
			recordHit(frontier.EntityFinding, record.ID, day(record.CreatedAt)))
	}
	return into, nil
}

// observationNames matches the one record kind no store enumerates.
//
// It is best effort in the strict sense: a build with no retrieval index
// contributes no observations, and an expression the index refuses costs the
// same. Neither is reported, because an observation is evidence a finding
// consolidates and the palette's other six kinds are unaffected — degrading
// the whole answer over it would make a missing index look like an empty
// frontier.
//
// The index is asked for a prefix on every word, which is the widest candidate
// set its grammar expresses, and every candidate is then confirmed against the
// summary the hit would render as. The confirmation is what keeps this row
// honest beside the other six: full text matched somewhere in the record, and
// the palette shows one line of it.
func (s *Server) observationNames(ctx context.Context, want string,
	into []nameCandidate) []nameCandidate {
	if s.opts.Search == nil {
		return into
	}
	match := prefixExpression(want)
	if match == "" {
		return into
	}
	hits, err := s.opts.Search.FrontierSearch(ctx, index.FrontierQuery{
		Match: match,
		Kinds: []frontier.OutputKind{frontier.OutputObservation},
		Order: index.OrderNewest,
		Limit: observationScan,
	})
	if err != nil {
		return into
	}
	for _, hit := range hits {
		into = consider(into, hit.Summary, want, hit.CreatedAt,
			recordHit(frontier.EntityObservation, hit.ID, day(hit.CreatedAt)))
	}
	return into
}

// prefixExpression turns a typed query into the index's match grammar.
//
// Every word becomes a prefix term, because three letters of a title are three
// letters of a word rather than a word. The grammar's own operators are
// dropped rather than passed through: a leading `-` excludes and a quote opens
// a phrase, so a half-typed query carrying either would silently search for
// something other than what is on screen. An expression with nothing left in
// it is empty, and the caller asks the index nothing.
func prefixExpression(want string) string {
	var terms []string
	for _, word := range strings.Fields(want) {
		word = strings.Trim(word, `-"*`)
		if word == "" {
			continue
		}
		terms = append(terms, word+"*")
	}
	return strings.Join(terms, " ")
}

// realityNames matches the ledger's subjects and the questions it is waiting
// on.
//
// Only open questions are offered. §4.8's answered and interpreted states are
// history a reader reaches from the subject they are about, while an open
// question is something the deployment is actually waiting for from the
// operator — which is what makes it a destination worth typing three letters
// at.
func (s *Server) realityNames(ctx context.Context, want string,
	into []nameCandidate) ([]nameCandidate, error) {
	if s.opts.Reality == nil {
		return into, nil
	}
	entities, err := s.opts.Reality.Entities(ctx, reality.EntityQuery{Limit: listScanCap})
	if err != nil {
		return into, err
	}
	for _, item := range entities {
		id, kind := item.Entity.ID, string(item.Entity.Kind)
		into = consider(into, item.Entity.Payload.DisplayName, want, item.LatestFact,
			func(title string) nameHit {
				return nameHit{
					Kind:  kindEntity,
					ID:    id,
					Title: title,
					Href:  "#/ask/entities/" + url.PathEscape(id),
					Meta:  kind,
				}
			})
	}
	questions, err := s.opts.Reality.Questions(ctx, reality.QuestionQuery{
		States: []reality.QuestionState{reality.QuestionOpen},
		Limit:  listScanCap,
	})
	if err != nil {
		return into, err
	}
	for _, item := range questions {
		id, class := item.Question.ID, string(item.Question.Class)
		into = consider(into, item.Question.Payload.Prompt, want, item.Question.CreatedAt,
			func(title string) nameHit {
				return nameHit{
					Kind:  kindQuestion,
					ID:    id,
					Title: title,
					Href:  "#/ask/questions/" + url.PathEscape(id),
					Meta:  class,
				}
			})
	}
	return into, nil
}

// sessionNames matches the conversations this machine's catalog has described.
//
// A row with no title is skipped rather than named by its identifier. A
// session's title is either the harness's own, Babel's derivation from the
// records inside it, or a model's inference (§9), and a session the scan has
// not reached yet has none of the three; a palette row reading `codex/01J8…`
// would be offering the operator a digest to recognize.
//
// The workspace is the fact beside it because it is what tells two
// similarly-titled conversations apart, and its last element is the project
// the operator would name. It is not a machine: which host held the session is
// not a dimension of this surface.
func (s *Server) sessionNames(ctx context.Context, want string,
	into []nameCandidate) ([]nameCandidate, error) {
	if s.opts.Lister == nil {
		return into, nil
	}
	listed, err := s.opts.Lister.ListSessions(ctx)
	if err != nil {
		return into, err
	}
	for _, row := range listed.Sessions {
		if row.Title == nil || *row.Title == "" {
			continue
		}
		selector, meta := row.Selector, workspaceName(row.Workspace)
		into = consider(into, *row.Title, want, modifiedTime(row.Modified),
			func(title string) nameHit {
				return nameHit{
					Kind:  kindSession,
					ID:    selector,
					Title: title,
					Href:  "#/sessions/" + url.PathEscape(selector),
					Meta:  meta,
				}
			})
	}
	return into, nil
}

// consider adds one row when its rendered line matches, and builds nothing
// when it does not.
//
// The hit arrives as a constructor rather than as a value because the caller
// is in a loop over every record the frontier holds: on this corpus that is
// five thousand rows per keystroke, and a search that composed five thousand
// destinations to discard all but twenty of them would spend its whole budget
// on strings nobody reads.
func consider(into []nameCandidate, text, want string, at time.Time,
	build func(title string) nameHit) []nameCandidate {
	// The line the row will render, which is where the match has to be found:
	// a title is one line, so a match below the first newline would be a row
	// whose visible words do not contain what was typed.
	line := strings.TrimSpace(text)
	if end := strings.IndexAny(line, "\r\n"); end >= 0 {
		line = strings.TrimSpace(line[:end])
	}
	if line == "" {
		return into
	}
	folded := strings.ToLower(line)
	found := strings.Index(folded, want)
	if found < 0 {
		return into
	}
	rank := matchSubstring
	if found == 0 {
		rank = matchPrefix
	}
	// An offset into the folded copy is an offset into the line only while
	// folding kept the length, which a few runes do not (İ lowers to two
	// runes, the Kelvin sign to one byte). Where they disagree the line is
	// rendered from its front rather than windowed against an offset that
	// names a different byte.
	if len(folded) != len(line) {
		return append(into, nameCandidate{hit: build(boundedLine(line)), rank: rank, at: at})
	}
	return append(into, nameCandidate{hit: build(excerpt(line, found)), rank: rank, at: at})
}

// excerpt is the one line a row renders, positioned so the match is in the
// part of it a reader sees.
//
// A match near the front is left where it is, because the record's own opening
// words are the best thing to show and re-cutting them would put an ellipsis
// on every row that does not begin with the query. A match deeper in a long
// statement arrives with the words just before it, so the row explains itself
// rather than showing four hundred characters of preamble and cutting off
// before the reason it is on screen.
func excerpt(line string, found int) string {
	if found <= excerptHead {
		return boundedLine(line)
	}
	start := found - excerptLeadIn
	// Begin on a word so the line reads as prose, and on a rune boundary
	// because half a rune is not a character.
	if space := strings.IndexByte(line[start:found], ' '); space >= 0 {
		start += space + 1
	}
	for start < found && !utf8.RuneStart(line[start]) {
		start++
	}
	return "…" + boundedLine(line[start:])
}

// recordHit is the constructor the four frontier kinds share: one record, one
// page, reached by identity (§8.6).
func recordHit(kind frontier.EntityType, id, meta string) func(string) nameHit {
	return func(title string) nameHit {
		return nameHit{
			Kind:  string(kind),
			ID:    id,
			Title: title,
			Href:  "#/r/" + url.PathEscape(id),
			Meta:  meta,
		}
	}
}

// pick orders the matches and bounds what any one kind may take.
//
// The order is the rank, then recency, which is the contract: a title starting
// with what was typed before a title containing it, and the newer of two
// equally good matches first.
//
// The share is what makes the list readable on a real corpus. This deployment
// holds two thousand candidates against two hundred proposals, so three
// letters that match both fill every row with candidates and the proposal the
// operator was looking for is on a page the palette does not have. Each kind
// therefore takes at most a quarter of the list in the first pass, and the
// second pass fills whatever the caps left over — so a query that only matches
// one kind still answers with a full list rather than with five rows and empty
// space.
func pick(candidates []nameCandidate, limit int) []nameHit {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].at.After(candidates[j].at)
	})
	share := limit / 4
	if share < 2 {
		share = 2
	}
	hits := make([]nameHit, 0, limit)
	taken := make([]bool, len(candidates))
	perKind := make(map[string]int, 8)
	for _, capped := range []bool{true, false} {
		for i := range candidates {
			if len(hits) >= limit {
				return hits
			}
			if taken[i] {
				continue
			}
			kind := candidates[i].hit.Kind
			if capped && perKind[kind] >= share {
				continue
			}
			taken[i], perKind[kind] = true, perKind[kind]+1
			hits = append(hits, candidates[i].hit)
		}
	}
	return hits
}

// day is the calendar date a record was made, which is the fact that tells two
// similar titles apart. It is the date alone rather than timeText's instant: a
// palette row is one line, and the hour a run wrote something is on the page.
func day(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.DateOnly)
}

// workspaceName is the project a session was held in, as the operator would
// name it: the last element of the path, or nothing when the catalog recorded
// no workspace.
func workspaceName(workspace *string) string {
	if workspace == nil {
		return ""
	}
	trimmed := strings.TrimRight(*workspace, "/")
	if trimmed == "" {
		return ""
	}
	if cut := strings.LastIndex(trimmed, "/"); cut >= 0 {
		return trimmed[cut+1:]
	}
	return trimmed
}

// modifiedTime reads a listing row's modification time for ordering only. A
// row whose time the catalog did not record, or recorded in a form this cannot
// parse, sorts last within its rank rather than claiming an instant.
func modifiedTime(modified *string) time.Time {
	if modified == nil {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339, *modified)
	if err != nil {
		return time.Time{}
	}
	return at
}
