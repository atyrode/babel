package explore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// ToolSearch is the corpus-search tool name a worker must use to reach
// Babel's retrieval. The capability says which facility; the tool says which
// operation inside it, and an unrecognized operation is denied rather than
// guessed at.
const ToolSearch = "search"

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

// Retrieval is one retrieval Babel served, with the trace step it recorded and
// the hits it produced.
//
// Position in Hits is presentation order and nothing else. §5.4 forbids
// retrieval rank from becoming evidence strength, and this package is the
// consumer that could break that rule: it never orders observations by hit
// position, and index.Hit carries no score for it to read even if it tried.
type Retrieval struct {
	Step run.RetrievalStep
	Hits []index.Hit
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
func (r *retrieval) serveSearch(ctx context.Context, req worker.ToolRequest) worker.Decision {
	if req.Tool != ToolSearch {
		return deny(fmt.Sprintf("corpus-search has no tool %q", req.Tool))
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
	if err != nil {
		// The index's own errors distinguish a rejected query from a failed
		// index, and both are the worker's to adapt to rather than Babel's
		// to hide.
		return deny(fmt.Sprintf("retrieval failed: %v", err))
	}
	if err := r.record(args.Query, hits); err != nil {
		return deny(fmt.Sprintf("the retrieval trace could not be recorded: %v", err))
	}
	return worker.Decision{Allow: true, Reason: fmt.Sprintf("served %d hits from the corpus index", len(hits))}
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
	return q, nil
}

// record appends one served retrieval to the trace.
//
// The evidence note holds identifiers only — harness, session, event position
// — and never the excerpt. The locator is what recovers the bytes, and a
// receipt that carried transcript text would have copied the corpus into the
// record an operator exports.
func (r *retrieval) record(query string, hits []index.Hit) error {
	served := make([]index.Hit, len(hits))
	results := make([]run.RetrievalResult, 0, len(hits))
	for i, hit := range hits {
		served[i] = hit
		if r.redact {
			redacted, err := preflight.RedactWith(hit.Text, r.thresholds)
			if err != nil {
				return err
			}
			served[i].Text = redacted
		}
		evidence, err := run.NewEvidence(hit.Locator,
			fmt.Sprintf("%s %s event %d", hit.Harness, hit.SourceID, hit.Index))
		if err != nil {
			return err
		}
		results = append(results, run.RetrievalResult{Rank: i + 1, Evidence: evidence})
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
	r.served = append(r.served, Retrieval{Step: step, Hits: served})
	return nil
}

// trace reports the recorded retrieval, for the receipt and the outcome.
func (r *retrieval) trace() ([]run.RetrievalStep, []Retrieval) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.steps), slices.Clone(r.served)
}
