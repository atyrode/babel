package explore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
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
}

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

	// Served is the payload the worker received, byte for byte. It is kept
	// verbatim so the §9 boundary is checkable rather than asserted: what
	// went over the wire is here, what an operator exports is in Step, and
	// no excerpt may appear in both.
	Served json.RawMessage
}

// retrieval is one stage's evidence broker: the Authorizer Babel supplies for
// the job, and the trace it accumulates while answering.
//
// The broker's transport is a separate Phase B gate (§14 defers the exact
// evidence-tool and public-research protocols), so what this serves is the
// retrieval itself and the durable trace of it. Nothing here invents a wire
// that does not exist: a facility this build cannot broker is denied with the
// reason, and the worker adapts, because a denial is not a termination.
type retrieval struct {
	index      *index.Index
	policy     worker.Authorizer
	harnesses  []string
	sourceIDs  []string
	redact     bool
	thresholds preflight.Thresholds
	limit      int
	now        func() time.Time

	// mu guards the trace. Authorize runs on the supervision goroutine
	// today, but a receipt whose ordering depended on that staying true
	// would be a latent bug rather than a design.
	mu     sync.Mutex
	steps  []run.RetrievalStep
	served []Retrieval
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
		return deny("repository snapshot materialization is not brokered by this build")
	case worker.CapabilitySandboxExec:
		return deny("command and test execution is not brokered by this build")
	case worker.CapabilityPublicResearch:
		return deny("the public-research broker is not available to this build")
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
	exhausted := r.limit > 0 && len(r.steps) >= r.limit
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
		Index:   len(r.steps) + 1,
		Tool:    string(worker.CapabilityCorpusSearch),
		Query:   query,
		At:      r.now(),
		Results: results,
	}
	r.steps = append(r.steps, step)
	r.served = append(r.served, Retrieval{Step: step, Hits: served, Served: encoded})
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
