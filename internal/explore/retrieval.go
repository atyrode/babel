package explore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/research"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// The two bounds on what one served retrieval discloses. Serving is bounded
// separately from indexing because the two do different jobs: indexing bounds
// a database row that never leaves the machine, while serving bounds what
// crosses a boundary to a provider. index.MaxIndexedTextBytes is the wrong
// number to reuse here for a plainer reason too — it is 16,000 bytes, and the
// largest indexed text in the operator's own 26,948-event corpus is 4,001, so
// reusing it would be a bound in name only.
const (
	// maxServedExcerptBytes bounds one hit's excerpt.
	//
	// The number is chosen against the corpus it will serve. Across those
	// 26,948 events the indexed text is 307 bytes at the median, 1,513 at the
	// 90th percentile and 2,761 at the 95th, so 2 KiB delivers better than
	// nine records in ten whole and clips the rest. That is the right place
	// to sit: an observation quotes a claim — an assistant paragraph, a
	// command and the error it produced — and 2 KiB is roughly thirty lines
	// of transcript, which is that unit with room to spare. The 7% that clip
	// are walls of tool output no model on a low thinking budget reads to the
	// end anyway, and they arrive flagged Truncated with a locator that
	// reopens the whole record.
	//
	// Every byte above what a judgement needs is disclosure bought for
	// nothing. The operator's preflight found 2,468 likely-secret values in
	// these sessions, redaction is pattern matching and therefore
	// best-effort, and residual risk scales with bytes sent — so the bound is
	// set at what the work needs rather than at what the index happens to
	// hold.
	maxServedExcerptBytes = 2048

	// maxServedHits bounds one served page, whatever page the request asked
	// for.
	//
	// index.DefaultLimit is 50 and index.MaxLimit is 500, and both were sized
	// for a page consumed inside this process — a review surface a human
	// scrolls. Neither survives contact with a page that now leaves it. Ten
	// hits is about 6 KiB of the operator's corpus at the median: a model on a
	// low thinking budget reads that and can still see a pattern across
	// sessions, where it would read none of five hundred. It is also what
	// keeps Babel inside the line budget it published to the worker — 500
	// hits at the excerpt bound would be past a megabyte, which is a
	// protocol violation Babel would be committing itself.
	//
	// Nothing is hidden by it: the applied bound travels in the payload, and
	// a worker that wants the next ten asks with Offset. The run's retrieval
	// budget bounds how many pages it can spend.
	maxServedHits = 10
)

// SearchRequest is the argument document the corpus-search tool takes. It is
// internal/index's query surface — §5.4's full-text search, structured
// filters, session and repository links, and temporal filters — and
// deliberately nothing more: §5.4 defers semantic retrieval, so there is no
// similarity, neighbourhood, or "closest match" argument to ask for.
//
// Sessions are the one filter a worker cannot widen. A request naming a source
// outside the run's preparation is denied whole rather than narrowed silently,
// because §2.6 fixes the sessions a run may read before work starts and a
// quietly reduced result set would look like an answer about the scope the
// worker asked for.
type SearchRequest struct {
	// Scope names the surface to search. Empty and ScopeCorpus are the
	// corpus; ScopeFrontier is Babel's own prior output (#87 item 4).
	//
	// It is an argument of the one search operation rather than a second
	// tool name, because the capability says which facility and the tool
	// says which operation inside it — and "search" is one operation over
	// two surfaces, both brokered by Babel, both bounded by the same
	// retrieval budget, both receipted. A second published tool name would
	// have been a second thing a separately developed worker can get wrong.
	Scope          string     `json:"scope,omitempty"`
	Query          string     `json:"query,omitempty"`
	Harnesses      []string   `json:"harnesses,omitempty"`
	SourceIDs      []string   `json:"source_ids,omitempty"`
	Kinds          []string   `json:"kinds,omitempty"`
	Tools          []string   `json:"tools,omitempty"`
	Outcomes       []string   `json:"outcomes,omitempty"`
	RepositoryPath string     `json:"repository_path,omitempty"`
	Since          *time.Time `json:"since,omitempty"`
	Until          *time.Time `json:"until,omitempty"`
	Partial        *bool      `json:"partial,omitempty"`
	Order          string     `json:"order,omitempty"`
	Limit          int        `json:"limit,omitempty"`
	Offset         int        `json:"offset,omitempty"`

	// Statuses filters candidate lifecycle state on a frontier search, and
	// is meaningless on a corpus one. It is here rather than in a separate
	// request type because the worker sends one arguments document for one
	// tool: two shapes behind one name would be a wire the far side has to
	// guess at.
	Statuses []string `json:"statuses,omitempty"`
}

// The search scopes. They are wire values, so they are named here beside the
// request that carries them rather than derived from anything.
const (
	ScopeCorpus   = "corpus"
	ScopeFrontier = "frontier"
)

// SearchResultsSchema names the shape a served corpus search travels in. It is
// versioned for the same reason worker.ResultSchema is: the payload is written
// by this repository and read by another, so a change to what a hit carries is
// a new schema rather than a surprise on the far side of a pipe.
const SearchResultsSchema = "babel.corpus-search/1"

// SearchResults is the payload one served corpus search carries to the worker.
//
// Hits is always present, empty included. A worker that received no payload at
// all and a worker whose query matched nothing have learned different things —
// the first was served by a Babel that brokers no evidence, the second was
// told the corpus is silent on the question — and they are reported as
// different gaps. Absence of the whole object says the first; an empty array
// says the second.
//
// Limit is the page bound Babel actually applied, which is not always the one
// the request asked for. It travels because without it a short page is
// ambiguous: a worker cannot otherwise tell "these are all the matches" from
// "this is the first ten of them". A full page means more may sit behind
// Offset; a short one means the matches are exhausted.
//
// There is deliberately no total, no score and no rank. index.Hit carries no
// score to forward, §5.4 forbids retrieval rank from becoming evidence
// strength, and the index returns a page rather than a count — a total here
// would be a number Babel would have to invent.
type SearchResults struct {
	Schema string      `json:"schema"`
	Query  string      `json:"query"`
	Limit  int         `json:"limit"`
	Hits   []SearchHit `json:"hits"`
}

// SearchHit is one served hit as the worker receives it.
//
// It is built from index.Hit rather than beside it, and it carries less. Five
// fields are unconditional because a hit without them is not citable: Harness
// and SourceID say which session, Index says where in that session's order,
// Locator recovers the record's bytes from the archive, and Excerpt is the text
// a model reads. A model quotes the excerpt into an observation; a human — or
// this build months later — reopens that observation by following Locator.Path
// to Locator.Line and verifying Locator.Digest against the record's bytes.
// SPEC.md §9 requires exactly that of every claim, so the locator is not
// optional decoration.
//
// Kind, Role, Tool, Outcome, Time and Partial are how the excerpt is
// interpreted rather than what it says: whether the text is a user report, an
// agent's claim or a tool's output decides what an observation drawn from it
// may assert, and a verification event's outcome is the whole subject of a
// recipe like outcome-integrity. Each is omitted when the harness recorded
// nothing, Time included — a harness that logged no timestamp gets no key
// rather than a synthesized zero, because §3 forbids inventing one.
//
// index.Hit's AdapterSchema and Paths are deliberately not here. AdapterSchema
// is the capture format's own version and says nothing to a reader of the
// text. Paths is the one field that would route disclosure around the
// redactor: preflight redacts text and only text, so a repository path list
// would reach a provider unexamined, and the excerpt already carries whatever
// the record itself said about the files it touched.
type SearchHit struct {
	Harness  string `json:"harness"`
	SourceID string `json:"source_id"`
	Index    int    `json:"index"`

	Kind    event.Kind `json:"kind,omitempty"`
	Role    string     `json:"role,omitempty"`
	Tool    string     `json:"tool,omitempty"`
	Outcome string     `json:"outcome,omitempty"`
	Time    *time.Time `json:"time,omitempty"`
	Partial bool       `json:"partial,omitempty"`

	// Excerpt is the redacted, bounded text. Truncated says it was cut, so a
	// model reading a clipped excerpt cannot honestly claim the record says
	// no more than this.
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated,omitempty"`

	Locator event.Locator `json:"locator"`
}

// Retrieval is one retrieval Babel served, with the trace step it recorded, the
// hits it produced, and the payload the worker received.
//
// Position in Hits is presentation order and nothing else. §5.4 forbids
// retrieval rank from becoming evidence strength, and this package is the
// consumer that could break that rule: it never orders observations by hit
// position, and index.Hit carries no score for it to read even if it tried.
type Retrieval struct {
	Step run.RetrievalStep

	// Hits is what the worker was served, with Text already redacted and
	// clipped to the served bound — not the index's page. The two used to be
	// the same thing because nothing left the process.
	Hits []index.Hit

	// FrontierHits is what a frontier-scope search served, redacted and
	// clipped the same way. It is a separate field rather than a widened
	// Hits because the two are different things — a corpus record with a
	// locator, and one of Babel's own records with an id — and Step.Scope
	// says which of them this retrieval was.
	FrontierHits []index.FrontierHit

	// Document is what a brokered research fetch served, with its
	// provenance. It is a third field rather than a widened Hits for the
	// reason FrontierHits is: a fetched document is addressed by URL and
	// digest, not by a corpus locator or a record id, and Step.Scope says
	// which of the three this retrieval was.
	Document *research.Document

	// Served is the payload the worker received, byte for byte. It is kept
	// verbatim so the §9 boundary is checkable rather than asserted: what
	// went over the wire is here, what an operator exports is in Step, and
	// no excerpt may appear in both.
	Served json.RawMessage
}

// retrieval is one stage's evidence broker: the Authorizer Babel supplies for
// the job, and the trace it accumulates while answering.
//
// The evidence-tool transport is still a separate Phase B gate (§14 defers the
// exact protocol), so what this serves is the retrieval itself and the durable
// trace of it. Nothing here invents a wire that does not exist: a facility
// this build cannot broker is denied with the reason, and the worker adapts,
// because a denial is not a termination.
type retrieval struct {
	index      *index.Index
	policy     worker.Authorizer
	harnesses  []string
	sourceIDs  []string
	redact     bool
	thresholds preflight.Thresholds
	limit      int
	now        func() time.Time

	// research is the public-research broker, nil on a run that was not
	// granted the capability or that fixed no sources. Nil is denied with
	// that reason rather than answered.
	research ResearchBroker

	// fetches bounds how many documents one pass may fetch. Egress is
	// budgeted separately from corpus retrieval because the two spend
	// different things: a corpus search reads a local index, and a fetch is
	// an observable external effect (§1) against someone else's host.
	fetches int

	// mu guards the trace and the two spend counters. Authorize runs on the
	// supervision goroutine today, but a receipt whose ordering depended on
	// that staying true would be a latent bug rather than a design.
	mu sync.Mutex
	// searched and fetched are what the two budgets are counted against.
	// len(steps) cannot serve for either: the trace holds both kinds, so a
	// run that fetched twice would look to the corpus budget like a run that
	// had searched twice.
	searched int
	fetched  int
	steps    []run.RetrievalStep
	served   []Retrieval
}

// Authorize decides one tool request and serves it when it is Babel's to
// serve.
//
// The order is fixed. The run's capability grant has already been enforced by
// internal/worker before this runs and cannot be widened here. An operator
// policy narrows next, so a run whose scope was negotiated cannot be reopened
// by this package. Only then is the facility consulted, and a facility this
// build does not broker is denied with a reason naming it rather than answered
// with fabricated evidence.
func (r *retrieval) Authorize(ctx context.Context, req worker.ToolRequest) worker.Decision {
	if r.policy != nil {
		if decision := r.policy.Authorize(ctx, req); !decision.Allow {
			return decision
		}
	}
	switch req.Capability {
	case worker.CapabilityCorpusSearch:
		return r.serveSearch(ctx, req)
	case worker.CapabilityRepoRead:
		return deny("repo-read is granted, but repository snapshot materialization is not brokered by this build")
	case worker.CapabilitySandboxExec:
		return deny("sandbox-exec is granted, but command and test execution is not brokered by this build")
	case worker.CapabilityPublicResearch:
		return r.serveResearch(ctx, req)
	default:
		return deny(fmt.Sprintf("capability %q has no facility behind it", req.Capability))
	}
}

// deny refuses one request with a reason the worker can adapt to. The count of
// denials is not tracked here: internal/worker's receipt already records every
// decision, and a second tally could only disagree with it.
func deny(reason string) worker.Decision {
	return worker.Decision{Reason: reason}
}

// serveSearch answers one corpus-search request out of the retrieval index and
// records what it served.
//
// The tool name is not this package's to define. The capability says which
// facility; the tool says which operation inside it, and the set of operations
// is internal/worker's published wire vocabulary — the same mapping the job
// document carried to the worker, and the same predicate the conformance
// suite's authorizer applies. This package holds it rather than restating it,
// because a name spelled out here as well as there is a divergence waiting to
// happen: it already did, and cost an entire exploration its evidence.
func (r *retrieval) serveSearch(ctx context.Context, req worker.ToolRequest) worker.Decision {
	if !worker.ServesTool(worker.CapabilityCorpusSearch, req.Tool) {
		return worker.DenyUnservedTool(worker.CapabilityCorpusSearch, req.Tool)
	}
	if r.index == nil {
		return deny("no retrieval index is available to this run")
	}
	r.mu.Lock()
	exhausted := r.limit > 0 && r.searched >= r.limit
	r.mu.Unlock()
	if exhausted {
		return deny("the run's retrieval budget is exhausted")
	}

	var args SearchRequest
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return deny("the search arguments are not a search request")
		}
	}
	// The scope decides which surface answers, and an unknown one is denied
	// rather than defaulted to the corpus. A worker that asked for a surface
	// this build does not have must learn that; served corpus hits under a
	// name it did not ask for would look to it like an answer about the
	// frontier, which is the failure mode the published tool-name discipline
	// exists to prevent one level up.
	switch args.Scope {
	case "", ScopeCorpus:
	case ScopeFrontier:
		return r.serveFrontier(ctx, args)
	default:
		return deny(fmt.Sprintf("search scope %q is not a surface this build serves", args.Scope))
	}
	query, err := r.query(args)
	if err != nil {
		return deny(err.Error())
	}
	hits, err := r.index.Search(ctx, query)
	switch {
	case errors.Is(err, index.ErrNoSearchableTerm), errors.Is(err, index.ErrMatchTooLong):
		// A query with nothing in it a tokenizer could match is a question
		// the corpus answers with "none", not a facility that broke. It is
		// served — recorded, budgeted, and answered with zero hits — so the
		// worker learns the difference between "the corpus does not hold
		// this" and "Babel would not look", and so the receipt shows the
		// retrieval it spent.
		hits = nil
	case err != nil:
		// Anything else is the index itself, and it is the worker's to adapt
		// to rather than Babel's to hide. A caller's text cannot reach here:
		// internal/index builds the FTS5 expression out of quoted literals,
		// so no query compiles into a database error.
		return deny(fmt.Sprintf("retrieval failed: %v", err))
	}
	results, err := r.serve(args.Query, query.Limit, hits)
	if err != nil {
		return deny(fmt.Sprintf("the retrieval trace could not be recorded: %v", err))
	}
	// The payload travels whichever way the search went. Zero hits is an
	// answer about the corpus, and a worker that can tell it apart from
	// having been served nothing at all reports the right gap.
	if len(hits) == 0 {
		return worker.Decision{
			Allow:   true,
			Reason:  "the corpus holds no record matching that query",
			Results: results,
		}
	}
	return worker.Decision{
		Allow:   true,
		Reason:  fmt.Sprintf("served %d hits from the corpus index", len(hits)),
		Results: results,
	}
}

// query turns a worker's request into an index query bounded by the run's
// preparation.
func (r *retrieval) query(args SearchRequest) (index.Query, error) {
	for _, id := range args.SourceIDs {
		if !slices.Contains(r.sourceIDs, id) {
			return index.Query{}, fmt.Errorf("session %q is outside this run's preparation", id)
		}
	}
	for _, h := range args.Harnesses {
		if !slices.Contains(r.harnesses, h) {
			return index.Query{}, fmt.Errorf("harness %q is outside this run's preparation", h)
		}
	}
	q := index.Query{
		Match:          args.Query,
		Harnesses:      args.Harnesses,
		SourceIDs:      args.SourceIDs,
		Tools:          args.Tools,
		Outcomes:       args.Outcomes,
		RepositoryPath: args.RepositoryPath,
		Partial:        args.Partial,
		Order:          index.Order(args.Order),
		Limit:          args.Limit,
		Offset:         args.Offset,
	}
	// An unrestricted request is restricted to the preparation rather than
	// left open: the run's sessions were fixed before it started (§2.6).
	if len(q.SourceIDs) == 0 {
		q.SourceIDs = slices.Clone(r.sourceIDs)
	}
	for _, k := range args.Kinds {
		q.Kinds = append(q.Kinds, event.Kind(k))
	}
	if args.Since != nil {
		q.Since = *args.Since
	}
	if args.Until != nil {
		q.Until = *args.Until
	}
	if q.Order == "" {
		// Relevance needs a match expression; a structural browse is ordered
		// by time instead of being refused for asking nothing of the text.
		if q.Match != "" {
			q.Order = index.OrderRelevance
		} else {
			q.Order = index.OrderNewest
		}
	}
	// A limit the index would refuse is refused here rather than rounded into
	// a valid one: a request for a page that cannot exist is malformed, and
	// answering it with a smaller page would read to the worker as the end of
	// the matches.
	if q.Limit < 0 || q.Limit > index.MaxLimit {
		return index.Query{}, fmt.Errorf("a page of %d is outside the retrieval ceiling of %d",
			q.Limit, index.MaxLimit)
	}
	// Anything inside the ceiling is narrowed to what serving bounds, and the
	// bound Babel applied travels in the payload so the narrowing is visible
	// rather than silent. The clamp lives here, in the one function that
	// builds the query, so no later path can widen the page after the fact.
	if q.Limit == 0 || q.Limit > maxServedHits {
		q.Limit = maxServedHits
	}
	return q, nil
}

// serve produces both things one served retrieval yields: the payload the
// worker receives and the trace step the receipt keeps.
//
// The two come out of one function on purpose, because that is what makes the
// redaction unbypassable. There is exactly one path from an index.Hit to bytes
// a provider could see, it runs through preflight here, and no caller holds a
// hit and a way to build a payload at the same time.
//
// The split between the outputs is absolute and is SPEC.md §9. The payload
// carries the excerpt, because a model that cannot read a record cannot form an
// observation about it. The evidence note holds identifiers only — harness,
// session, event position — and never the excerpt: the locator is what recovers
// the bytes, and a receipt carrying transcript text would have copied the
// corpus into the record an operator exports, readable by anyone with catalog
// access.
func (r *retrieval) serve(query string, limit int, hits []index.Hit) (json.RawMessage, error) {
	served := make([]index.Hit, len(hits))
	results := make([]run.RetrievalResult, 0, len(hits))
	payload := SearchResults{
		Schema: SearchResultsSchema,
		Query:  query,
		Limit:  limit,
		Hits:   make([]SearchHit, 0, len(hits)),
	}
	for i, hit := range hits {
		served[i] = hit
		// Redaction runs over the record's whole indexed text before the
		// excerpt is cut, never after. A credential straddling the cut would
		// otherwise be halved, and half a value is something a detector can
		// miss while the remaining bytes are still disclosure.
		if r.redact {
			redacted, err := preflight.RedactWith(hit.Text, r.thresholds)
			if err != nil {
				return nil, err
			}
			served[i].Text = redacted
		}
		excerpt, truncated := clip(served[i].Text)
		served[i].Text = excerpt
		payload.Hits = append(payload.Hits, SearchHit{
			Harness:   hit.Harness,
			SourceID:  hit.SourceID,
			Index:     hit.Index,
			Kind:      hit.Kind,
			Role:      hit.Role,
			Tool:      hit.Tool,
			Outcome:   hit.Outcome,
			Time:      hit.Time,
			Partial:   hit.Partial,
			Excerpt:   excerpt,
			Truncated: truncated,
			Locator:   hit.Locator,
		})
		evidence, err := run.NewEvidence(hit.Locator,
			fmt.Sprintf("%s %s event %d", hit.Harness, hit.SourceID, hit.Index))
		if err != nil {
			return nil, err
		}
		results = append(results, run.RetrievalResult{Rank: i + 1, Evidence: evidence})
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step := run.RetrievalStep{
		Index: len(r.steps) + 1,
		Tool:  string(worker.CapabilityCorpusSearch),
		Query: query,
		At:    r.now(),
		// Scope stays empty for a corpus search. Empty already means the
		// corpus in every receipt written before the frontier surface
		// existed, so writing the word would change the stored bytes of the
		// one case that never needed clarifying.
		Results: results,
	}
	r.searched++
	r.steps = append(r.steps, step)
	r.served = append(r.served, Retrieval{Step: step, Hits: served, Served: encoded})
	return encoded, nil
}

// FrontierResultsSchema names the shape a served frontier search travels in.
// It is a different schema from SearchResultsSchema rather than a variant of
// it: a corpus hit carries a locator that recovers bytes from the archive and
// a frontier hit carries a record id, and a worker that read one shape as the
// other would either cite a locator that does not exist or discard the only
// address the record has.
const FrontierResultsSchema = "babel.frontier-search/1"

// FrontierResults is the payload one served frontier search carries.
//
// Note repeats the refine-first framing the job document opened with, and it
// is not decoration. A search happens in the middle of a run, possibly long
// after the job was read, and the whole risk of serving Babel's own output to
// a model is that the model treats it as established. The sentence that says
// otherwise travels with every page of it, from the same constant the job
// document uses, so the two cannot drift apart.
type FrontierResults struct {
	Schema string              `json:"schema"`
	Query  string              `json:"query"`
	Limit  int                 `json:"limit"`
	Note   string              `json:"note"`
	Hits   []FrontierSearchHit `json:"hits"`
}

// FrontierSearchHit is one prior output as the worker receives it.
//
// ID is what makes the whole facility useful: it is the handle a refinement,
// a revival or an amendment names, so a run that finds its idea already
// recorded has something to point at instead of a reason to restate it. Root
// distinguishes two wordings of one candidate from two candidates, and Status
// is why #87 removed the idea of a terminal status — a rejected candidate a
// run rediscovers is a candidate to argue about, not a record to re-mint.
//
// There is no locator and no evidence. A prior output's support is the
// observations under it, which a reader reaches through the record; putting a
// borrowed locator here would let a claim be cited as if the citation were
// this hit's own.
type FrontierSearchHit struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Root string `json:"root,omitempty"`
	// Origin is the machine whose analysis this is, empty for this one
	// (#109 item 4).
	//
	// It travels because withholding it would be inference presented as fact.
	// The whole reason the frontier now answers across hosts is that two
	// conductors on two machines must not silently duplicate one another - and
	// the conductor is the party being told. A worker handed "this idea already
	// exists" with no attribution would reasonably read it as this machine's
	// own prior work, which changes what it should do: its own superseded
	// wording is a revision to make, and another host's committed candidate is
	// a record to argue with or defer to.
	//
	// It is a host id and nothing else - an opaque, operator-assigned
	// identifier §9 admits in plaintext. No display name, no machine facts, and
	// nothing about that host's configuration reaches a provider through it.
	Origin  string `json:"origin,omitempty"`
	Status  string `json:"status,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Summary is the record in one line and Text is what was matched, both
	// redacted under the run's disclosure class. Babel's own output quotes
	// the corpus, so it is redacted on the way out exactly as a transcript
	// excerpt is: an evidence note carrying a credential is a credential.
	Summary   string `json:"summary"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// serveFrontier answers one frontier-scope search out of the same index the
// corpus search is served from, and records what it disclosed.
//
// It is budgeted, denied, and receipted exactly like a corpus search, because
// it is the same kind of act: Babel deciding what a provider gets to see. The
// receipt records the record identifiers rather than evidence, since a
// frontier record is addressed by id — see run.RetrievalStep.Records.
func (r *retrieval) serveFrontier(ctx context.Context, args SearchRequest) worker.Decision {
	query := index.FrontierQuery{
		Match:  args.Query,
		Order:  index.Order(args.Order),
		Limit:  args.Limit,
		Offset: args.Offset,
	}
	for _, kind := range args.Kinds {
		query.Kinds = append(query.Kinds, frontier.OutputKind(kind))
	}
	for _, status := range args.Statuses {
		query.Statuses = append(query.Statuses, frontier.Status(status))
	}
	if query.Order == "" {
		if query.Match != "" {
			query.Order = index.OrderRelevance
		} else {
			query.Order = index.OrderNewest
		}
	}
	if query.Limit < 0 || query.Limit > index.MaxLimit {
		return deny(fmt.Sprintf("a page of %d is outside the retrieval ceiling of %d",
			query.Limit, index.MaxLimit))
	}
	// The served page is bounded by the same number a corpus page is, and
	// the applied bound travels in the payload, so a short page reads as
	// "the frontier is exhausted" rather than as "this is all Babel would
	// show you".
	if query.Limit == 0 || query.Limit > maxServedHits {
		query.Limit = maxServedHits
	}
	hits, err := r.index.FrontierSearch(ctx, query)
	switch {
	case errors.Is(err, index.ErrNoSearchableTerm), errors.Is(err, index.ErrMatchTooLong):
		// Served with zero hits, on the same reasoning a corpus search is:
		// the worker learns the frontier holds nothing for that wording,
		// and the receipt shows the retrieval it spent.
		hits = nil
	case err != nil:
		return deny(fmt.Sprintf("frontier retrieval failed: %v", err))
	}
	results, err := r.serveFrontierHits(args.Query, query.Limit, hits)
	if err != nil {
		return deny(fmt.Sprintf("the retrieval trace could not be recorded: %v", err))
	}
	if len(hits) == 0 {
		return worker.Decision{
			Allow:   true,
			Reason:  "the frontier holds no prior output matching that query",
			Results: results,
		}
	}
	return worker.Decision{
		Allow: true,
		Reason: fmt.Sprintf("served %d prior Babel outputs; they are candidate ideas, not evidence",
			len(hits)),
		Results: results,
	}
}

// serveFrontierHits builds the payload and the trace step of one frontier
// search, in one function for the reason serve is one function: there is
// exactly one path from a stored record to bytes a provider could see, and it
// runs through the redactor here.
func (r *retrieval) serveFrontierHits(query string, limit int, hits []index.FrontierHit) (json.RawMessage, error) {
	served := make([]index.FrontierHit, len(hits))
	records := make([]string, 0, len(hits))
	payload := FrontierResults{
		Schema: FrontierResultsSchema,
		Query:  query,
		Limit:  limit,
		Note:   FramingRefine,
		Hits:   make([]FrontierSearchHit, 0, len(hits)),
	}
	for i, hit := range hits {
		served[i] = hit
		summary, text := hit.Summary, hit.Text
		if r.redact {
			redactedSummary, err := preflight.RedactWith(summary, r.thresholds)
			if err != nil {
				return nil, err
			}
			redactedText, err := preflight.RedactWith(text, r.thresholds)
			if err != nil {
				return nil, err
			}
			summary, text = redactedSummary, redactedText
		}
		excerpt, truncated := clip(text)
		served[i].Summary, served[i].Text = summary, excerpt
		payload.Hits = append(payload.Hits, FrontierSearchHit{
			Kind:      string(hit.Kind),
			ID:        hit.ID,
			Root:      hit.RootID,
			Origin:    hit.Origin,
			Status:    string(hit.Status),
			RunID:     hit.RunID,
			Subject:   hit.Subject.ID,
			Summary:   summary,
			Text:      excerpt,
			Truncated: truncated || hit.Truncated,
		})
		records = append(records, hit.ID)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step := run.RetrievalStep{
		Index:   len(r.steps) + 1,
		Tool:    string(worker.CapabilityCorpusSearch),
		Scope:   ScopeFrontier,
		Query:   query,
		At:      r.now(),
		Records: records,
	}
	r.searched++
	r.steps = append(r.steps, step)
	r.served = append(r.served, Retrieval{Step: step, FrontierHits: served, Served: encoded})
	return encoded, nil
}

// FetchRequest is the whole argument document worker.ToolFetch takes: the
// opaque identifier of one source this run fixed.
//
// One field, and unknown fields are refused. That is the disclosure boundary
// in the shape of a struct — §2.6 makes URL, query, header and body disclosure
// sinks, and an argument document with room in it is a channel whatever the
// broker does with the extra keys. A worker that sends more is told so rather
// than quietly served, because the alternative is a build where a field
// nobody reads is nonetheless a field a model can write.
type FetchRequest struct {
	Source string `json:"source"`
}

// serveResearch answers one public-research request.
//
// The tool name discipline is internal/worker's, exactly as it is for corpus
// search: the capability says which facility, the tool says which operation,
// and an operation this build did not publish is denied rather than guessed
// at.
func (r *retrieval) serveResearch(ctx context.Context, req worker.ToolRequest) worker.Decision {
	if !worker.ServesTool(worker.CapabilityPublicResearch, req.Tool) {
		return worker.DenyUnservedTool(worker.CapabilityPublicResearch, req.Tool)
	}
	if r.research == nil {
		return deny("no public-research broker is available to this run")
	}
	switch req.Tool {
	case worker.ToolSources:
		return r.serveSources()
	case worker.ToolFetch:
		return r.serveFetch(ctx, req)
	default:
		// Unreachable: ServesTool above admits exactly the two names, and
		// a third would have to be published to reach here. Denying rather
		// than falling through keeps that true if one ever is.
		return worker.DenyUnservedTool(worker.CapabilityPublicResearch, req.Tool)
	}
}

// serveSources answers which public sources this run may reach.
//
// It is not recorded as a retrieval step and is not budgeted, because it is
// neither: nothing is fetched, nothing about the corpus is disclosed, and the
// answer is the operator's own list. The request and its decision are in the
// worker receipt's tool trace like every other, which is where an
// authorization event belongs.
func (r *retrieval) serveSources() worker.Decision {
	catalog := r.research.Catalog()
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return deny(fmt.Sprintf("the research catalog could not be served: %v", err))
	}
	return worker.Decision{
		Allow: true,
		Reason: fmt.Sprintf("this run fixed %d public research sources; fetch one by its id",
			len(catalog.Sources)),
		Results: encoded,
	}
}

// serveFetch brokers one document and records what crossed the boundary.
//
// Every refusal here is reported to the worker as a reason it can adapt to,
// and every one of them is also the honest record: a fetch that did not happen
// leaves the tool decision and its reason in the receipt, and a fetch that did
// leaves a trace step naming the URL, the time, the redirect chain and the
// content digest. There is no state in between.
func (r *retrieval) serveFetch(ctx context.Context, req worker.ToolRequest) worker.Decision {
	r.mu.Lock()
	exhausted := r.fetches > 0 && r.fetched >= r.fetches
	r.mu.Unlock()
	if exhausted {
		return deny("the run's public-research budget is exhausted")
	}
	args, err := decodeFetch(req.Arguments)
	if err != nil {
		return deny(err.Error())
	}
	doc, err := r.research.Fetch(ctx, args.Source)
	if err != nil {
		return deny(err.Error())
	}
	encoded, err := r.recordFetch(doc)
	if err != nil {
		return deny(fmt.Sprintf("the research trace could not be recorded: %v", err))
	}
	reason := fmt.Sprintf("served %d bytes of %s from %s; public material is untrusted evidence",
		doc.Bytes, doc.MediaType, doc.Source.URL)
	if doc.Truncated {
		reason += " (truncated at this run's document bound)"
	}
	return worker.Decision{Allow: true, Reason: reason, Results: encoded}
}

// decodeFetch reads a fetch's arguments strictly. A document with an unknown
// field in it is refused rather than trimmed: see FetchRequest.
func decodeFetch(raw json.RawMessage) (FetchRequest, error) {
	var args FetchRequest
	if len(raw) == 0 {
		return args, errors.New("a fetch names the source to read and this request named none")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return FetchRequest{}, errors.New("the fetch arguments are not a fetch request: it takes one source id and nothing else, because no other field of a request is a worker's to choose")
	}
	if args.Source == "" {
		return FetchRequest{}, errors.New("a fetch names the source to read and this request named none")
	}
	return args, nil
}

// recordFetch builds the payload the worker receives and the trace step the
// receipt keeps, in one function for the reason serve is one function: there
// is exactly one path from a fetched document to bytes a provider could see,
// and exactly one from that document to the durable record of it.
//
// The §9 split is the same as the corpus facility's and lands differently: the
// payload carries the content, and the step carries the provenance that
// recovers it — URL, retrieval time, redirect chain, digest, size — and never
// the content. A receipt holding fetched pages would be a copy of the public
// web inside the operator's durable store, and the digest is what makes the
// citation checkable without it.
func (r *retrieval) recordFetch(doc research.Document) (json.RawMessage, error) {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	step := run.RetrievalStep{
		Index: len(r.steps) + 1,
		Tool:  string(worker.CapabilityPublicResearch),
		Scope: run.ScopeResearch,
		// The query is the identifier the worker named. It is the whole of
		// what the worker chose about this request, so recording it records
		// its whole choice.
		Query: doc.Source.ID,
		At:    doc.RetrievedAt,
		Research: &run.ResearchSource{
			SourceID:    doc.Source.ID,
			URL:         doc.Source.URL,
			Redirects:   doc.Redirects,
			MediaType:   doc.MediaType,
			Digest:      doc.Digest,
			Bytes:       doc.Bytes,
			Truncated:   doc.Truncated,
			RetrievedAt: doc.RetrievedAt,
		},
	}
	r.fetched++
	r.steps = append(r.steps, step)
	r.served = append(r.served, Retrieval{Step: step, Document: &doc, Served: encoded})
	return encoded, nil
}

// clip bounds one excerpt at maxServedExcerptBytes without splitting a rune,
// because a half rune is invalid UTF-8 and a JSON encoder would replace it with
// a substitution character in the middle of the operator's evidence.
func clip(text string) (string, bool) {
	if len(text) <= maxServedExcerptBytes {
		return text, false
	}
	cut := maxServedExcerptBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// trace reports the recorded retrieval, for the receipt and the outcome.
func (r *retrieval) trace() ([]run.RetrievalStep, []Retrieval) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.steps), slices.Clone(r.served)
}
