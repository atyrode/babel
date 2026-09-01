package web

// The §4.7 review surface: the queue, the two mutations an operator performs on
// it, the history they append to, and §6.7's raw export.
//
// Both mutations are structured the same way, and the shape is the §14 gate
// this file closes: validate the request's shape, resolve the operator identity,
// call the service method the CLI calls, report what the service returned.
// Nothing here writes a store, computes a review status, or decides whether a
// decision is allowed — internal/review does all three, so the browser renders
// what the service permitted rather than asserting what it wants.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// QueueItem is one row of the review queue, with an excerpt of the subject so
// the queue is readable without opening every record.
//
// The embedded fleetMark is issue #109 item 4: a row says which machine
// produced it and whether it is globally reviewable yet, so an inbox that spans
// the deployment cannot present another host's staged output as though it were
// this machine's decided work.
type QueueItem struct {
	fleetMark
	Subject       refView `json:"subject"`
	EnrolledAt    string  `json:"enrolled_at"`
	Status        string  `json:"status"`
	Decisions     int     `json:"decisions"`
	LastDecidedAt string  `json:"last_decided_at,omitempty"`
	Refinements   int     `json:"refinements"`
	Excerpt       string  `json:"excerpt"`
	// Citations is issue #113's triage signal: how many typed references
	// point out of this record and how many point at it. It is a count and
	// not the edges themselves, because a queue is read to decide what to
	// open next — "four records rest on this candidate" is what changes that
	// decision, and the citations themselves are on the record's own page.
	//
	// Absent means not counted, which happens for two reasons and says so
	// by being absent in both: this build has no reference graph wired, or
	// the graph could not answer for this row. A zero is a different claim —
	// counted, and nothing cites it — so the two are never collapsed.
	Citations *citationCounts `json:"citations,omitempty"`
}

type queueResult struct {
	syncNotice
	Items []QueueItem `json:"items"`
	Total int         `json:"total"`
}

// handleReviewQueue lists what is awaiting review, in enrolment order.
//
// The order is the service's and is not re-sorted here. §5.2 confines novelty
// and priority to ordering and forbids them from gating whether a candidate
// exists, and a queue that reordered itself by a model-produced score would
// quietly do the triage the reviewer is there to do.
//
// ?fleet=1 appends the other hosts' committed review records after the local
// page. Local first, and the two blocks are not interleaved: this machine's
// rows carry a derived review status, a decision count and a refinement count
// that a remote row cannot, and sorting the two together by time would put rows
// that answer different questions in one sequence. Total stays the local
// queue's length for the same reason — the fleet block is an attributed
// appendix, not more pages of this machine's backlog.
func (s *Server) handleReviewQueue(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Review != nil && s.opts.Frontier != nil, "the review service") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	fleetWide, ok := s.fleetRequested(w, r)
	if !ok {
		return
	}
	filter := review.QueueFilter{Limit: listScanCap}
	if value := r.URL.Query().Get("type"); value != "" {
		kind, ok := refKind(value)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "type is not a record kind")
			return
		}
		filter.Type = kind
	}
	switch value := r.URL.Query().Get("status"); value {
	case "":
		// The service default: records awaiting a first decision, which is
		// what a queue is for.
	case "all":
		filter.AllStatuses = true
	default:
		status, ok := reviewStatus(value)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "status is not a review status")
			return
		}
		filter.Status = status
	}
	items, err := s.opts.Review.Queue(r.Context(), filter)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := queueResult{Items: []QueueItem{}, Total: len(items)}
	start, end := pg.window(len(items))
	local := items[start:end]
	ids := make([]string, 0, len(local))
	for _, item := range local {
		ids = append(ids, item.Subject.ID)
	}
	// One resolution for the whole page, from internal/fleet. A per-row
	// derivation would be one query per row and a second answer to a question
	// SPEC.md §9 has exactly one. A catalog that could not answer degrades the
	// column to fleet.SyncUnknown and says so, rather than refusing this
	// machine's own inbox over another machine's outage.
	states, degraded := s.syncStates(r.Context(), r, ids)
	if degraded {
		result.syncNotice = degradedNotice()
	}
	for _, item := range local {
		excerpt, err := s.excerpt(r.Context(), item.Subject)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		result.Items = append(result.Items, QueueItem{
			fleetMark:     s.localMark(states[item.Subject.ID]),
			Subject:       viewRef(item.Subject),
			EnrolledAt:    timeText(item.EnrolledAt),
			Status:        string(item.Status),
			Decisions:     item.Decisions,
			LastDecidedAt: timeText(item.LastDecidedAt),
			Refinements:   item.Refinements,
			Excerpt:       excerpt,
			Citations:     s.citationCounts(r.Context(), frontierSubject(item.Subject)),
		})
	}
	if fleetWide {
		records, err := s.otherHosts(r.Context(), pg.limit,
			sharedcatalog.KindDisposition, sharedcatalog.KindProposal)
		if err != nil {
			s.fleetError(w, r, err)
			return
		}
		host := s.opts.Fleet.LocalHost()
		for _, record := range records {
			result.Items = append(result.Items, fleetQueueItem(record, host))
		}
	}
	s.writeJSON(w, http.StatusOK, result)
}

// fleetQueueItem renders another host's committed review record as a queue row.
//
// Four fields stay at their zero value and the emptiness is the content: a
// review status, a decision count, a last-decided time and a refinement count
// are derived from the owning host's own append-only history, which this
// machine does not hold. Reporting zeroes as facts would say the other host has
// decided nothing about a record it may have rejected twice, so a client tells
// the two apart by local_host and renders an absence for a row that is not its
// own.
func fleetQueueItem(record fleet.Record, localHost string) QueueItem {
	mark, summary := markFleetRecord(record, localHost)
	return QueueItem{fleetMark: mark, Subject: fleetQueueSubject(record), Excerpt: summary}
}

// fleetQueueSubject names the record a fleet queue row is about.
//
// A committed disposition names the record it decided, which is the row a
// reviewer wants to see; a proposal is its own subject. A record this instance
// could not open can only name itself under its catalog kind, which is the
// honest answer: this machine knows the row exists and cannot yet say what it
// says about anything else.
func fleetQueueSubject(record fleet.Record) refView {
	if record.Published != nil && record.Published.Subject.ID != "" {
		return viewRef(record.Published.Subject)
	}
	return refView{Type: string(record.Record.Kind), ID: record.Record.RecordID}
}

// reviewStatus resolves a ?status= filter against internal/frontier's derived
// review statuses.
func reviewStatus(value string) (frontier.ReviewStatus, bool) {
	switch frontier.ReviewStatus(value) {
	case frontier.ReviewNew, frontier.ReviewAccepted, frontier.ReviewRejected,
		frontier.ReviewDeferred, frontier.ReviewDuplicate, frontier.ReviewRefineRequested:
		return frontier.ReviewStatus(value), true
	}
	return "", false
}

// excerpt reads the one line of a record that says what it is about: a
// candidate's statement in the model's own wording, an observation's claim, or a
// title. It is the record's own text, never a summary, because §5.2 requires
// the original wording to survive every listing that shows it.
func (s *Server) excerpt(ctx context.Context, subject frontier.Ref) (string, error) {
	switch subject.Type {
	case frontier.EntityHypothesis:
		record, err := s.opts.Frontier.Hypothesis(ctx, subject.ID)
		return record.Payload.Statement, err
	case frontier.EntityObservation:
		record, err := s.opts.Frontier.Observation(ctx, subject.ID)
		return record.Payload.Claim, err
	case frontier.EntityFinding:
		record, err := s.opts.Frontier.Finding(ctx, subject.ID)
		return record.Payload.Title, err
	case frontier.EntityProposal:
		record, err := s.opts.Frontier.Proposal(ctx, subject.ID)
		return record.Payload.Title, err
	}
	return "", nil
}

// decideRequest is POST /api/review/decide's body.
type decideRequest struct {
	Subject       refView `json:"subject"`
	Disposition   string  `json:"disposition"`
	ContextID     string  `json:"contextId"`
	DuplicateOfID string  `json:"duplicateOfId"`
	Note          string  `json:"note"`
}

type decideResult struct {
	Status string           `json:"status"`
	Event  dispositionEvent `json:"event"`
}

type dispositionEvent struct {
	ID          string `json:"id"`
	Sequence    int64  `json:"sequence"`
	Disposition string `json:"disposition"`
	RecordedAt  string `json:"recorded_at"`
}

// handleReviewDecide appends one §4.7 disposition.
//
// The handler resolves three things and then gets out of the way: the subject
// reference, the operator identity, and the disposition as the caller wrote it.
// Whether the record is reviewable, whether its state accepts this decision,
// whether the decision repeats the standing one, and whether the cited context
// exists are all review.Service.Decide's rules, checked before internal/frontier
// appends anything. The vocabulary is not filtered here either: an unknown
// disposition reaches the service and is refused by name, so there is exactly
// one place that decides what a decision may be.
func (s *Server) handleReviewDecide(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Review != nil && s.opts.Frontier != nil, "the review service") {
		return
	}
	var request decideRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	kind, ok := refKind(request.Subject.Type)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "subject type is not a record kind")
		return
	}
	if request.Subject.ID == "" {
		s.writeError(w, http.StatusBadRequest, "subject id is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	subject := frontier.Ref{Type: kind, ID: request.Subject.ID}
	event, err := s.opts.Review.Decide(r.Context(), review.Decision{
		Subject:       subject,
		Disposition:   frontier.Disposition(request.Disposition),
		By:            by,
		ContextID:     request.ContextID,
		DuplicateOfID: request.DuplicateOfID,
		Note:          request.Note,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The status is read back from the append-only history rather than
	// inferred from the decision that was just made: a derived status that
	// this handler computed could disagree with the events behind it.
	status, err := s.opts.Frontier.ReviewStatus(r.Context(), subject)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, decideResult{
		Status: string(status),
		Event: dispositionEvent{
			ID:          event.ID,
			Sequence:    event.Sequence,
			Disposition: string(event.Disposition),
			RecordedAt:  timeText(event.RecordedAt),
		},
	})
}

type contextRequest struct {
	Text string `json:"text"`
}

type contextResult struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	At     string `json:"at"`
}

// handleReviewContext records one piece of attributed operator guidance (§4.7).
//
// It is guidance and it is stored as nothing else: review.Context carries no
// locator and satisfies no evidence interface, so a decision may cite it and no
// claim can rest on it. The author is the session's operator for the same reason
// the decision's is — unattributed guidance is indistinguishable from the
// model's own prose once it is in a prompt.
func (s *Server) handleReviewContext(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Review != nil, "the review service") {
		return
	}
	var request contextRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	recorded, err := s.opts.Review.RecordContext(r.Context(), by, request.Text)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, contextResult{
		ID:     recorded.ID,
		Author: recorded.Author,
		At:     timeText(recorded.At),
	})
}

type contextView struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	At     string `json:"at"`
	Text   string `json:"text"`
}

type decisionView struct {
	ID            string       `json:"id"`
	Sequence      int64        `json:"sequence"`
	Disposition   string       `json:"disposition"`
	ReviewerID    string       `json:"reviewer_id"`
	RecordedAt    string       `json:"recorded_at"`
	DuplicateOfID string       `json:"duplicate_of_id,omitempty"`
	Note          string       `json:"note,omitempty"`
	Context       *contextView `json:"context,omitempty"`
}

type refinementRequestView struct {
	ID            string   `json:"id"`
	DispositionID string   `json:"disposition_id"`
	Subject       refView  `json:"subject"`
	CreatedAt     string   `json:"created_at"`
	Guidance      string   `json:"guidance"`
	Scope         []string `json:"scope,omitempty"`
}

type refinementOutcomeView struct {
	ID               string   `json:"id"`
	Mode             string   `json:"mode"`
	AgentID          string   `json:"agent_id"`
	RecordedAt       string   `json:"recorded_at"`
	Revision         *refView `json:"revision,omitempty"`
	MemoryProposalID string   `json:"memory_proposal_id,omitempty"`
	Rationale        string   `json:"rationale"`
	Scope            string   `json:"scope,omitempty"`
}

type refinementView struct {
	Request refinementRequestView  `json:"request"`
	Outcome *refinementOutcomeView `json:"outcome,omitempty"`
}

type historyResult struct {
	Status      string           `json:"status"`
	Decisions   []decisionView   `json:"decisions"`
	Refinements []refinementView `json:"refinements"`
}

// handleReviewHistory serves one record's complete review history. Every
// decision stays, in order: §4.7's log is append-only, so a record that was
// rejected and later reconsidered shows both events rather than the latest one.
func (s *Server) handleReviewHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Review != nil, "the review service") {
		return
	}
	subject, ok := s.subjectRef(w, r)
	if !ok {
		return
	}
	history, err := s.opts.Review.History(r.Context(), subject)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := historyResult{
		Status:      string(history.Status),
		Decisions:   make([]decisionView, 0, len(history.Decisions)),
		Refinements: make([]refinementView, 0, len(history.Refinements)),
	}
	for _, decision := range history.Decisions {
		view := decisionView{
			ID:            decision.Event.ID,
			Sequence:      decision.Event.Sequence,
			Disposition:   string(decision.Event.Disposition),
			ReviewerID:    decision.Event.ReviewerID,
			RecordedAt:    timeText(decision.Event.RecordedAt),
			DuplicateOfID: decision.Event.DuplicateOfID,
			Note:          decision.Event.Payload.Note,
		}
		if decision.Context != nil {
			view.Context = &contextView{
				ID:     decision.Context.ID,
				Author: decision.Context.Author,
				At:     timeText(decision.Context.At),
				Text:   decision.Context.Text,
			}
		}
		result.Decisions = append(result.Decisions, view)
	}
	for _, refinement := range history.Refinements {
		view := refinementView{Request: refinementRequestView{
			ID:            refinement.Request.ID,
			DispositionID: refinement.Request.DispositionID,
			Subject:       viewRef(refinement.Request.Subject),
			CreatedAt:     timeText(refinement.Request.CreatedAt),
			Guidance:      refinement.Request.Payload.Guidance,
			Scope:         refinement.Request.Payload.Scope,
		}}
		// A nil outcome is a normal visible state, not a gap: §4.7 lets a
		// refinement run independently of the rejection that authorized it.
		if outcome := refinement.Outcome; outcome != nil {
			view.Outcome = &refinementOutcomeView{
				ID:         outcome.ID,
				Mode:       string(outcome.Mode),
				AgentID:    outcome.AgentID,
				RecordedAt: timeText(outcome.RecordedAt),
				Rationale:  outcome.Assessment.Payload.Rationale,
				Scope:      outcome.Assessment.Payload.Scope,
			}
			if outcome.Revision != nil {
				revision := viewRef(*outcome.Revision)
				view.Outcome.Revision = &revision
			}
			if outcome.Memory != nil {
				view.Outcome.MemoryProposalID = outcome.Memory.ID
			}
		}
		result.Refinements = append(result.Refinements, view)
	}
	s.writeJSON(w, http.StatusOK, result)
}

// subjectRef reads the ?type=&id= pair the history route addresses a record
// with.
func (s *Server) subjectRef(w http.ResponseWriter, r *http.Request) (frontier.Ref, bool) {
	kind, ok := refKind(r.URL.Query().Get("type"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, "type is not a record kind")
		return frontier.Ref{}, false
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return frontier.Ref{}, false
	}
	return frontier.Ref{Type: kind, ID: id}, true
}

// handleExport renders §6.7's raw private record.
//
// Redaction is never disabled here. §3 requires exports to redact secret values
// by default and §8 makes raw bytes an explicit private reveal, so the option
// exists in the service and this surface has no way to ask for it: a browser
// request must not be able to produce the one document that carries unredacted
// credential material.
//
// Nothing about this route publishes. review.Service.Export reads, and the only
// output is this response body (§4.6, decision 13).
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Review != nil, "the review service") {
		return
	}
	kind, ok := exportKind(r.URL.Query().Get("type"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, "type is not an exportable record kind")
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "markdown" {
		s.writeError(w, http.StatusBadRequest, "format must be json or markdown")
		return
	}
	document, err := s.opts.Review.Export(r.Context(), review.Node{Kind: kind, ID: id}, review.ExportOptions{})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if format == "markdown" {
		rendered, err := document.Markdown()
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		// The bytes go out exactly as internal/review rendered them, and
		// that is the safe choice rather than the lazy one: that renderer
		// already escapes every untrusted value into inert Markdown —
		// HTML-significant characters as entities, control and bidi runes
		// as a visible "\u{HEX}", in the same vocabulary internal/cli's
		// terminal-safe renderer uses. Escaping the document again here
		// would corrupt it, because this response's own newlines are the
		// only thing distinguishing a document from one long line.
		//
		// What this route adds is what a response has to say about itself:
		// a declared non-HTML type, nosniff so a browser may not decide
		// otherwise, and an attachment disposition so a navigation saves
		// the export rather than rendering it.
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `attachment; filename="babel-export.md"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rendered)
		return
	}
	encoded, err := document.JSON()
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The export's own JSON is served whole rather than re-shaped, so it
	// round-trips into the types Babel stores; writeJSON still walks it, so
	// every string in it is escaped like any other response.
	s.writeJSON(w, http.StatusOK, json.RawMessage(encoded))
}

// exportKind resolves the exportable record kinds. The refinement-internal
// kinds internal/review can address are deliberately absent: an export is a
// record a human keeps, and the four analysis records plus the run receipt are
// what §6.7 makes one of.
func exportKind(value string) (review.Kind, bool) {
	switch review.Kind(value) {
	case review.KindHypothesis:
		return review.KindHypothesis, true
	case review.KindObservation:
		return review.KindObservation, true
	case review.KindFinding:
		return review.KindFinding, true
	case review.KindProposal:
		return review.KindProposal, true
	case review.KindRun:
		return review.KindRun, true
	}
	return "", false
}
