package web

// The Phase B analysis read surface: what a run recorded, what the frontier
// holds, and what retrieval can find. Everything here is a GET and none of it
// writes: the records are written by exploration through internal/explore, and
// decided through internal/review by the routes in review.go.

import (
	"context"
	"net/http"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/review"
)

// exploreRefusal is why no route starts an exploration, reported by
// GET /api/analysis/state so the surface says it plainly instead of offering a
// control that cannot work.
//
// This is a deliberate choice between the two defensible ones. A start-plus-poll
// pair was rejected on three grounds. A run takes minutes and outlives the
// request that would begin it, but it also outlives the launch session:
// SPEC.md §2.7 makes lock-and-stop revoke every session and terminate the
// listener, so the surface that started the work would routinely disappear
// while the work continued, leaving a worker process supervised by nothing and
// an operator with no way to see or cancel it. Starting a run also requires
// authority a browser must not hold — §2.6 fixes the capability grant and one
// Code profile per run, and §3 requires the disclosure class to be shown and
// consented to per run before any material is sent — so a start route would
// either carry authority the CLI would not have, or silently reuse a consent
// the operator gave for a different run. That is precisely the browser-owned
// state §14's gate exists to prevent. And poll would need a listing of runs in
// flight, which is durable state no service here holds.
//
// The results of a run are fully readable here the moment it finishes, which is
// what the web surface is for: reviewing what analysis produced, not launching
// it.
const exploreRefusal = "Exploration cannot be started from the web surface. " +
	"A run outlives the launch session that would have to supervise it — lock and stop revokes the session " +
	"and terminates the listener — and starting one requires the operator's explicit per-run scope, " +
	"capability grant, profile, and disclosure consent, which a browser cannot hold. " +
	"Run `babel explore` in the terminal; its records appear here as soon as it commits them."

// analysisState is the Phase B landing state: whether there is analysis state
// to read, whether a run can be started from here, the runs already recorded,
// and the cookbook this build carries.
type analysisState struct {
	Configured bool               `json:"configured"`
	Worker     workerAvailability `json:"worker"`
	Runs       []RunSummary       `json:"runs"`
	RunsTotal  int                `json:"runs_total"`
	Cookbook   []RecipeSummary    `json:"cookbook"`
}

// workerAvailability reports whether this surface can start an exploration, and
// why not when it cannot.
//
// Available is false in this build for the reason exploreRefusal states, and it
// is a field rather than an omission because a UI that had to infer the absence
// of a control would eventually render one. It says nothing about whether a
// Code worker exists on the machine: nothing wires worker status into this
// server, and a browser must not report facts about the host it cannot observe.
type workerAvailability struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail"`
}

// RecipeSummary is one cookbook asset as a listing shows it. The body is
// deliberately absent: a recipe body is guidance a reviewer reads in the
// repository, and §5.1 keeps the asset tree the authority rather than a copy
// rendered through an API.
type RecipeSummary struct {
	ID           string   `json:"id"`
	Version      int      `json:"version"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Default      bool     `json:"default"`
	Scope        []string `json:"scope"`
	Stages       []string `json:"stages"`
	Capabilities []string `json:"capabilities"`
}

func (s *Server) handleAnalysisState(w http.ResponseWriter, r *http.Request) {
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	state := analysisState{
		Configured: s.opts.Review != nil && s.opts.Frontier != nil,
		Worker:     workerAvailability{Available: false, Detail: exploreRefusal},
		Runs:       []RunSummary{},
		Cookbook:   []RecipeSummary{},
	}
	if s.opts.Runs != nil {
		runs, total, err := s.opts.Runs.Runs(r.Context(), pg.limit, pg.offset)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		if runs != nil {
			state.Runs = runs
		}
		state.RunsTotal = total
	}
	if s.opts.Cookbook != nil {
		for _, recipe := range s.opts.Cookbook.All() {
			state.Cookbook = append(state.Cookbook, summarizeRecipe(recipe))
		}
	}
	s.writeJSON(w, http.StatusOK, state)
}

func summarizeRecipe(recipe *cookbook.Recipe) RecipeSummary {
	summary := RecipeSummary{
		ID:           recipe.ID,
		Version:      recipe.Version,
		Kind:         string(recipe.Kind),
		Title:        recipe.Title,
		Default:      recipe.Default,
		Scope:        make([]string, 0, len(recipe.Scope)),
		Stages:       make([]string, 0, len(recipe.Stages)),
		Capabilities: make([]string, 0, len(recipe.Capabilities)),
	}
	for _, scope := range recipe.Scope {
		summary.Scope = append(summary.Scope, string(scope))
	}
	for _, stage := range recipe.Stages {
		summary.Stages = append(summary.Stages, string(stage))
	}
	for _, capability := range recipe.Capabilities {
		summary.Capabilities = append(summary.Capabilities, string(capability))
	}
	return summary
}

// HypothesisSummary is one candidate as a listing shows it: enough to decide
// whether to open it, and the model's original wording, which §5.2 requires to
// survive sorting and classification.
type HypothesisSummary struct {
	ID                string   `json:"id"`
	RunID             string   `json:"run_id"`
	AncestorID        string   `json:"ancestor_id,omitempty"`
	CreatedAt         string   `json:"created_at"`
	Status            string   `json:"status"`
	ReviewStatus      string   `json:"review_status"`
	Statement         string   `json:"statement"`
	ProvisionalLabels []string `json:"provisional_labels,omitempty"`
	Observations      int      `json:"observations"`
}

type hypothesisList struct {
	Items []HypothesisSummary `json:"items"`
	Total int                 `json:"total"`
}

// handleHypotheses lists candidates, optionally narrowed to one exploration
// status. No filter lists every status, including rejected: nothing is deleted
// (§5.2, §4.7), so a listing that hid rejected candidates would misrepresent
// the frontier as smaller than it is.
func (s *Server) handleHypotheses(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil && s.opts.Review != nil, "the hypothesis frontier") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	status, ok := s.hypothesisStatus(w, r)
	if !ok {
		return
	}
	ids, err := s.hypothesisIDs(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := hypothesisList{Items: []HypothesisSummary{}}
	// With no status filter the identifiers are the count, so only the
	// requested page is read. With one, every candidate has to be read to
	// know whether it matches, because status lives in an append-only event
	// history rather than in a column a listing could filter on.
	if status == "" {
		result.Total = len(ids)
		start, end := pg.window(len(ids))
		ids = ids[start:end]
	}
	for _, id := range ids {
		record, err := s.opts.Frontier.Hypothesis(r.Context(), id)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		if status != "" {
			if record.Status != status {
				continue
			}
			result.Total++
			if result.Total <= pg.offset || len(result.Items) >= pg.limit {
				continue
			}
		}
		summary, err := s.summarizeHypothesis(r.Context(), record)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		result.Items = append(result.Items, summary)
	}
	s.writeJSON(w, http.StatusOK, result)
}

// hypothesisStatus resolves the ?status= filter against internal/frontier's own
// vocabulary, refusing an unknown value rather than answering it with an empty
// list: a misspelled filter that returns nothing reads as an empty frontier.
func (s *Server) hypothesisStatus(w http.ResponseWriter, r *http.Request) (frontier.Status, bool) {
	value := r.URL.Query().Get("status")
	switch frontier.Status(value) {
	case "":
		return "", true
	case frontier.StatusUntriaged, frontier.StatusQueued, frontier.StatusInvestigating,
		frontier.StatusDeferred, frontier.StatusRejected, frontier.StatusPromoted:
		return frontier.Status(value), true
	}
	s.writeError(w, http.StatusBadRequest, "status is not an exploration status")
	return "", false
}

// hypothesisIDs enumerates the candidates this server can list.
//
// It takes two queries because internal/frontier deliberately offers no
// enumeration — it answers questions about a record you name, and its one
// listing is the unexplored frontier — so the second source is internal/review's
// queue, which is the set of records exploration and review have enrolled.
// Their union is the same one `babel hypotheses` lists, so the two surfaces
// agree; a candidate that is neither unexplored nor enrolled is reachable by
// identifier and not by listing, which is a gap in the services rather than
// one this route can close.
func (s *Server) hypothesisIDs(ctx context.Context) ([]string, error) {
	items, err := s.opts.Review.Queue(ctx, review.QueueFilter{
		Type:        frontier.EntityHypothesis,
		AllStatuses: true,
		Limit:       listScanCap,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item.Subject.ID]; ok {
			continue
		}
		seen[item.Subject.ID] = struct{}{}
		ids = append(ids, item.Subject.ID)
	}
	unexplored, err := s.opts.Frontier.Unexplored(ctx, listScanCap)
	if err != nil {
		return nil, err
	}
	for _, record := range unexplored {
		if _, ok := seen[record.ID]; ok {
			continue
		}
		seen[record.ID] = struct{}{}
		ids = append(ids, record.ID)
	}
	if len(ids) > listScanCap {
		ids = ids[:listScanCap]
	}
	return ids, nil
}

func (s *Server) summarizeHypothesis(ctx context.Context, record frontier.Hypothesis) (HypothesisSummary, error) {
	observations, err := s.opts.Frontier.ObservationsFor(ctx, record.ID)
	if err != nil {
		return HypothesisSummary{}, err
	}
	status, err := s.opts.Frontier.ReviewStatus(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID})
	if err != nil {
		return HypothesisSummary{}, err
	}
	return HypothesisSummary{
		ID:                record.ID,
		RunID:             record.RunID,
		AncestorID:        record.AncestorID,
		CreatedAt:         timeText(record.CreatedAt),
		Status:            string(record.Status),
		ReviewStatus:      string(status),
		Statement:         record.Payload.Statement,
		ProvisionalLabels: record.Payload.ProvisionalLabels,
		Observations:      len(observations),
	}, nil
}

// hypothesisView is one candidate revision whole. The payload keeps
// internal/frontier's own JSON names so a reader decodes the record Babel
// stores rather than a second shape that has to be kept in step with it.
type hypothesisView struct {
	ID            string                     `json:"id"`
	AncestorID    string                     `json:"ancestor_id,omitempty"`
	RunID         string                     `json:"run_id"`
	SchemaVersion int                        `json:"schema_version"`
	CreatedAt     string                     `json:"created_at"`
	Status        string                     `json:"status"`
	ReviewStatus  string                     `json:"review_status"`
	Payload       frontier.HypothesisPayload `json:"payload"`
}

// statusEventView is one entry of a candidate's lifecycle history.
//
// Actor is beside RunID rather than derived from it. #87 makes every resting
// status revivable by an operator, and such a transition belongs to no run at
// all: a view that reported only the run identity would render an operator's
// revive as a transition with no author.
type statusEventView struct {
	ID           string    `json:"id"`
	HypothesisID string    `json:"hypothesis_id"`
	Sequence     int64     `json:"sequence"`
	Status       string    `json:"status"`
	RunID        string    `json:"run_id"`
	Actor        actorView `json:"actor"`
	RecordedAt   string    `json:"recorded_at"`
	Note         string    `json:"note,omitempty"`
}

func viewStatusEvent(event frontier.StatusEvent) statusEventView {
	return statusEventView{
		ID:           event.ID,
		HypothesisID: event.HypothesisID,
		Sequence:     event.Sequence,
		Status:       string(event.Status),
		RunID:        event.RunID,
		Actor:        viewActor(event.Actor),
		RecordedAt:   timeText(event.RecordedAt),
		Note:         event.Payload.Note,
	}
}

type observationView struct {
	ID            string                      `json:"id"`
	AncestorID    string                      `json:"ancestor_id,omitempty"`
	HypothesisID  string                      `json:"hypothesis_id"`
	RunID         string                      `json:"run_id"`
	RecipeID      string                      `json:"recipe_id"`
	RecipeVersion int                         `json:"recipe_version"`
	SchemaVersion int                         `json:"schema_version"`
	EvidenceCount int                         `json:"evidence_count"`
	CreatedAt     string                      `json:"created_at"`
	Payload       frontier.ObservationPayload `json:"payload"`
}

// linkView is one typed relationship with the far end's wording, so a link
// reads as prose rather than as an identifier a reader has to go and resolve.
type linkView struct {
	ID             string `json:"id"`
	FromID         string `json:"from_id"`
	ToID           string `json:"to_id"`
	Type           string `json:"type"`
	CreatedAt      string `json:"created_at"`
	Note           string `json:"note,omitempty"`
	OtherStatement string `json:"other_statement,omitempty"`
}

type nodeView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type edgeView struct {
	ID         string   `json:"id"`
	Relation   string   `json:"relation"`
	From       nodeView `json:"from"`
	To         nodeView `json:"to"`
	CreatedAt  string   `json:"created_at"`
	Generation int      `json:"generation"`
}

type lineageView struct {
	Node        nodeView   `json:"node"`
	Ancestors   []edgeView `json:"ancestors"`
	Descendants []edgeView `json:"descendants"`
}

type hypothesisDetail struct {
	Hypothesis    hypothesisView    `json:"hypothesis"`
	StatusHistory []statusEventView `json:"statusHistory"`
	Observations  []observationView `json:"observations"`
	Links         []linkView        `json:"links"`
	Lineage       lineageView       `json:"lineage"`
}

func (s *Server) handleHypothesis(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil && s.opts.Review != nil, "the hypothesis frontier") {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	record, err := s.opts.Frontier.Hypothesis(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	reviewStatus, err := s.opts.Frontier.ReviewStatus(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: id})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	history, err := s.opts.Frontier.StatusHistory(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	observations, err := s.opts.Frontier.ObservationsFor(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	links, err := s.links(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	lineage, err := s.opts.Review.Lineage(ctx, review.Node{Kind: review.KindHypothesis, ID: id})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	detail := hypothesisDetail{
		Hypothesis: hypothesisView{
			ID:            record.ID,
			AncestorID:    record.AncestorID,
			RunID:         record.RunID,
			SchemaVersion: record.SchemaVersion,
			CreatedAt:     timeText(record.CreatedAt),
			Status:        string(record.Status),
			ReviewStatus:  string(reviewStatus),
			Payload:       record.Payload,
		},
		StatusHistory: make([]statusEventView, 0, len(history)),
		Observations:  viewObservations(observations),
		Links:         links,
		Lineage:       viewLineage(lineage),
	}
	for _, entry := range history {
		detail.StatusHistory = append(detail.StatusHistory, viewStatusEvent(entry))
	}
	s.writeJSON(w, http.StatusOK, detail)
}

func viewObservations(records []frontier.Observation) []observationView {
	views := make([]observationView, 0, len(records))
	for _, record := range records {
		views = append(views, observationView{
			ID:            record.ID,
			AncestorID:    record.AncestorID,
			HypothesisID:  record.HypothesisID,
			RunID:         record.RunID,
			RecipeID:      record.RecipeID,
			RecipeVersion: record.RecipeVersion,
			SchemaVersion: record.SchemaVersion,
			EvidenceCount: record.EvidenceCount,
			CreatedAt:     timeText(record.CreatedAt),
			Payload:       record.Payload,
		})
	}
	return views
}

// links reads a candidate's relationships in both directions, because §4.2's
// links are queried from either side: a contradicted candidate has to be
// findable from the contradiction as well as from itself.
func (s *Server) links(ctx context.Context, id string) ([]linkView, error) {
	from, err := s.opts.Frontier.LinksFrom(ctx, id)
	if err != nil {
		return nil, err
	}
	to, err := s.opts.Frontier.LinksTo(ctx, id)
	if err != nil {
		return nil, err
	}
	views := make([]linkView, 0, len(from)+len(to))
	for _, link := range append(from, to...) {
		view := linkView{
			ID:        link.ID,
			FromID:    link.FromID,
			ToID:      link.ToID,
			Type:      string(link.Type),
			CreatedAt: timeText(link.CreatedAt),
			Note:      link.Payload.Note,
		}
		other := link.ToID
		if other == id {
			other = link.FromID
		}
		// Best effort: the far end's wording makes the link readable, and a
		// link whose far end could not be read is still a link worth
		// showing.
		if record, err := s.opts.Frontier.Hypothesis(ctx, other); err == nil {
			view.OtherStatement = record.Payload.Statement
		}
		views = append(views, view)
	}
	return views, nil
}

func viewLineage(lineage review.Lineage) lineageView {
	view := lineageView{
		Node:        nodeView{Kind: string(lineage.Node.Kind), ID: lineage.Node.ID},
		Ancestors:   viewEdges(lineage.Ancestors),
		Descendants: viewEdges(lineage.Descendants),
	}
	return view
}

func viewEdges(edges []review.Edge) []edgeView {
	views := make([]edgeView, 0, len(edges))
	for _, edge := range edges {
		views = append(views, edgeView{
			ID:         edge.ID,
			Relation:   string(edge.Relation),
			From:       nodeView{Kind: string(edge.From.Kind), ID: edge.From.ID},
			To:         nodeView{Kind: string(edge.To.Kind), ID: edge.To.ID},
			CreatedAt:  timeText(edge.CreatedAt),
			Generation: edge.Generation,
		})
	}
	return views
}

// FindingSummary is one consolidation as a listing shows it.
type FindingSummary struct {
	ID           string `json:"id"`
	RunID        string `json:"run_id"`
	CreatedAt    string `json:"created_at"`
	Title        string `json:"title"`
	Observations int    `json:"observations"`
	Hypotheses   int    `json:"hypotheses"`
	ReviewStatus string `json:"review_status"`
}

type findingList struct {
	Items []FindingSummary `json:"items"`
	Total int              `json:"total"`
}

// handleFindings lists consolidations from the review queue, which is the only
// enumeration of findings any service offers: internal/frontier answers
// Finding(id) and nothing wider.
func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil && s.opts.Review != nil, "the hypothesis frontier") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	items, err := s.opts.Review.Queue(r.Context(), review.QueueFilter{
		Type:        frontier.EntityFinding,
		AllStatuses: true,
		Limit:       listScanCap,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := findingList{Items: []FindingSummary{}, Total: len(items)}
	start, end := pg.window(len(items))
	for _, item := range items[start:end] {
		record, err := s.opts.Frontier.Finding(r.Context(), item.Subject.ID)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		result.Items = append(result.Items, FindingSummary{
			ID:           record.ID,
			RunID:        record.RunID,
			CreatedAt:    timeText(record.CreatedAt),
			Title:        record.Payload.Title,
			Observations: len(record.ObservationIDs),
			Hypotheses:   len(record.HypothesisIDs),
			ReviewStatus: string(item.Status),
		})
	}
	s.writeJSON(w, http.StatusOK, result)
}

type findingView struct {
	ID             string                  `json:"id"`
	AncestorID     string                  `json:"ancestor_id,omitempty"`
	RunID          string                  `json:"run_id"`
	SchemaVersion  int                     `json:"schema_version"`
	CreatedAt      string                  `json:"created_at"`
	ObservationIDs []string                `json:"observation_ids"`
	HypothesisIDs  []string                `json:"hypothesis_ids"`
	ReviewStatus   string                  `json:"review_status"`
	Payload        frontier.FindingPayload `json:"payload"`
}

type proposalView struct {
	ID            string                   `json:"id"`
	AncestorID    string                   `json:"ancestor_id,omitempty"`
	RunID         string                   `json:"run_id"`
	SchemaVersion int                      `json:"schema_version"`
	CreatedAt     string                   `json:"created_at"`
	FindingIDs    []string                 `json:"finding_ids"`
	HypothesisIDs []string                 `json:"hypothesis_ids"`
	ReviewStatus  string                   `json:"review_status"`
	Payload       frontier.ProposalPayload `json:"payload"`
}

type findingDetail struct {
	Finding      findingView       `json:"finding"`
	Observations []observationView `json:"observations"`
	Proposals    []proposalView    `json:"proposals"`
}

func (s *Server) handleFinding(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil && s.opts.Review != nil, "the hypothesis frontier") {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	record, err := s.opts.Frontier.Finding(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	status, err := s.opts.Frontier.ReviewStatus(ctx, frontier.Ref{Type: frontier.EntityFinding, ID: id})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	observations := make([]frontier.Observation, 0, len(record.ObservationIDs))
	for _, observationID := range record.ObservationIDs {
		observation, err := s.opts.Frontier.Observation(ctx, observationID)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		observations = append(observations, observation)
	}
	proposals, err := s.proposalsFor(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, findingDetail{
		Finding: findingView{
			ID:             record.ID,
			AncestorID:     record.AncestorID,
			RunID:          record.RunID,
			SchemaVersion:  record.SchemaVersion,
			CreatedAt:      timeText(record.CreatedAt),
			ObservationIDs: record.ObservationIDs,
			HypothesisIDs:  record.HypothesisIDs,
			ReviewStatus:   string(status),
			Payload:        record.Payload,
		},
		Observations: viewObservations(observations),
		Proposals:    proposals,
	})
}

// proposalsFor finds the proposals a finding suggested. A proposal names its
// findings and no service indexes the reverse, so the enrolled proposals are
// read and filtered; the scan is bounded by listScanCap like every other
// enumeration here.
func (s *Server) proposalsFor(ctx context.Context, findingID string) ([]proposalView, error) {
	items, err := s.opts.Review.Queue(ctx, review.QueueFilter{
		Type:        frontier.EntityProposal,
		AllStatuses: true,
		Limit:       listScanCap,
	})
	if err != nil {
		return nil, err
	}
	views := make([]proposalView, 0, len(items))
	for _, item := range items {
		record, err := s.opts.Frontier.Proposal(ctx, item.Subject.ID)
		if err != nil {
			return nil, err
		}
		if !contains(record.FindingIDs, findingID) {
			continue
		}
		views = append(views, proposalView{
			ID:            record.ID,
			AncestorID:    record.AncestorID,
			RunID:         record.RunID,
			SchemaVersion: record.SchemaVersion,
			CreatedAt:     timeText(record.CreatedAt),
			FindingIDs:    record.FindingIDs,
			HypothesisIDs: record.HypothesisIDs,
			ReviewStatus:  string(record.ReviewStatus),
			Payload:       record.Payload,
		})
	}
	return views, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// hitView is one retrieved event. There is no score, rank, or relevance field,
// and there must never be one: §5.4's rule is that retrieval rank never becomes
// evidence strength, and internal/index makes it unrepresentable rather than
// merely discouraged.
type hitView struct {
	Harness       string        `json:"harness"`
	AdapterSchema int           `json:"adapter_schema"`
	SourceID      string        `json:"source_id"`
	Selector      string        `json:"selector"`
	Index         int           `json:"index"`
	Kind          string        `json:"kind"`
	Role          string        `json:"role,omitempty"`
	Tool          string        `json:"tool,omitempty"`
	Outcome       string        `json:"outcome,omitempty"`
	Time          string        `json:"time,omitempty"`
	Paths         []string      `json:"paths,omitempty"`
	Partial       bool          `json:"partial"`
	Text          string        `json:"text"`
	Locator       event.Locator `json:"locator"`
}

type searchResult struct {
	Hits []hitView `json:"hits"`
}

// handleSearch serves §5.4 retrieval. The page bound is internal/index's own,
// because the index is what has to answer it: a caller wanting more pages asks
// with an offset, which keeps one query's memory bounded on a corpus of
// millions of events.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Search != nil, "the retrieval index") {
		return
	}
	query := r.URL.Query()
	limit, ok := queryInt(r, "limit", index.DefaultLimit)
	if !ok || limit <= 0 || limit > index.MaxLimit {
		s.writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
		return
	}
	offset, ok := queryInt(r, "offset", 0)
	if !ok || offset < 0 {
		s.writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
		return
	}
	kinds := make([]event.Kind, 0, len(query["kind"]))
	for _, value := range query["kind"] {
		kind, ok := eventKind(value)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "kind is not an event kind")
			return
		}
		kinds = append(kinds, kind)
	}
	hits, err := s.opts.Search.Search(r.Context(), index.Query{
		Match:     query.Get("q"),
		Harnesses: query["harness"],
		Kinds:     kinds,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := searchResult{Hits: make([]hitView, 0, len(hits))}
	for _, hit := range hits {
		view := hitView{
			Harness:       hit.Harness,
			AdapterSchema: hit.AdapterSchema,
			SourceID:      hit.SourceID,
			// The canonical Phase A selector, so a hit deep-links to the
			// session routes that already exist.
			Selector: hit.Harness + "/" + hit.SourceID,
			Index:    hit.Index,
			Kind:     string(hit.Kind),
			Role:     hit.Role,
			Tool:     hit.Tool,
			Outcome:  hit.Outcome,
			Paths:    hit.Paths,
			Partial:  hit.Partial,
			Text:     hit.Text,
			Locator:  hit.Locator,
		}
		if hit.Time != nil {
			view.Time = timeText(*hit.Time)
		}
		result.Hits = append(result.Hits, view)
	}
	s.writeJSON(w, http.StatusOK, result)
}

// eventKind resolves a ?kind= filter against internal/event's vocabulary. An
// unknown kind is refused rather than passed through, because a filter nothing
// matches is indistinguishable from an empty corpus.
func eventKind(value string) (event.Kind, bool) {
	for _, kind := range []event.Kind{
		event.KindUserReport, event.KindAgentClaim, event.KindToolObservation,
		event.KindRepositoryChange, event.KindVerificationEvidence, event.KindOpaque,
	} {
		if event.Kind(value) == kind {
			return kind, true
		}
	}
	return "", false
}
