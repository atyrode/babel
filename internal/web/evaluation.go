package web

// Issue #219's evaluation surface: what Babel's own output has been reviewed,
// how it was received, what review is still owed, and the two writes an
// operator performs here (SPEC.md §4.12, §5.8, §8.5, decision 89).
//
// Six routes and one shape rule. Every read answers with internal/evaluation's
// own structs, serialized by Go rather than remapped into a second set of view
// types. That is a deliberate departure from the Phase B routes beside it, and
// the reason is what those view types were for: a QueueItem or a focusRuleView
// exists because the service's own type carries fields a browser must not see,
// or omits a derivation the page needs. An evaluation Page is neither. It is
// already the read model — the service built it from a bounded projection for
// exactly this reader — so a mirror of it here would be a second copy of the
// ranking contract, drifting from the first the day a field is added.
//
// Nothing on this surface can vote. internal/web.EvaluationService lists no
// Submit, Draw, Review or Claim, so the whole class of defect where a click
// mints something that reads like a worker's assessment is not a rule these
// handlers keep but a method the type does not have. What an operator does
// here is what §4.12 gives him: his own policy, and his own attributed
// criteria, feedback and reconsideration decisions. Accepting, rejecting,
// deferring and merging stay where they already are — internal/review's
// disposition surface — and the browser links to it rather than growing a
// second decision vocabulary beside it.
//
// A reconsideration decision carries its polarity as a closed-vocabulary
// field: reopening and retaining are opposite acts, so neither is ever read
// out of the operator's prose, and the record answers with the one he chose.
// Reopening is the one place this surface's write reaches a disposition, and
// it does so through internal/evaluation rather than here: that service
// records the decision and the reopened disposition in one transaction, so
// either both exist or neither does. What this route holds is the operator's
// choice; what it must not do is describe the consequence wrongly, which is
// why the answer's sentence is chosen by the act that was stored.
//
// The operator's identity is the session's, resolved by the same
// requireOperator every §4.7 and §4.8 mutation uses, and there is no field in
// either request body that could supply one. A run identity is not
// representable here at all: evaluation.Submission never reaches this package,
// so no request can attribute anything to a run.

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
)

// evaluationServiceName is what a launch with no evaluation projection calls
// the thing it is missing. It is stated once because every route on this
// surface refuses with it, and the wording is what an operator reads.
const evaluationServiceName = "the evaluation service"

// evaluationVocabulary is the closed set of values this surface's filters and
// controls may offer.
//
// It is served rather than written down in the client for §4.8's reason on the
// ledger's kinds: the vocabularies belong to internal/evaluation, which refuses
// a value outside them, so a picker holding its own copy would offer the
// operator a sort the service cannot answer and a lane that matches nothing.
// It also means a vocabulary that grows reaches the interface without a second
// edit, which is what keeps the two from drifting silently.
//
// FeedbackReasons is the one list here that is not a validation gate, and it
// is named as a suggestion for that reason: §4.12 keeps a feedback reason free
// text, because "not-now" and "wrong-remedy" are the common cases rather than
// the whole of what an operator may have to say.
type evaluationVocabulary struct {
	Sorts           []string `json:"sorts"`
	Lanes           []string `json:"lanes"`
	Coverage        []string `json:"coverage"`
	Kinds           []string `json:"kinds"`
	Roles           []string `json:"roles"`
	FeedbackReasons []string `json:"feedback_reasons"`
	// OperatorKinds is the subset of record kinds an operator may author
	// through this surface. It is deliberately short of
	// evaluation.Record's own vocabulary: assessment, reconsider,
	// assignment, attempt and checkpoint are written by a worker run or by
	// the scheduler, and the store refuses them from an operator input by
	// name.
	OperatorKinds []string `json:"operator_kinds"`
	// ReconsiderDecisions is the closed set of polarities a reconsideration
	// decision may state. It is served rather than written in the client
	// for a reason the other lists here do not have: the control that uses
	// it decides whether a rejected record is reopened, so a page holding
	// its own copy could offer an act the store refuses — or, worse, offer
	// one word and send another.
	ReconsiderDecisions []string `json:"reconsider_decisions"`
}

func evaluationVocabularies() evaluationVocabulary {
	return evaluationVocabulary{
		Sorts:           evaluation.Sorts(),
		Lanes:           evaluation.Lanes(),
		Coverage:        evaluation.CoverageFilters(),
		Kinds:           evaluation.Kinds(),
		Roles:           evaluation.Roles(),
		FeedbackReasons: evaluation.FeedbackReasonSuggestions(),
		OperatorKinds: []string{
			evaluation.KindCriteria,
			evaluation.KindFeedback,
			evaluation.KindReconsiderDecision,
		},
		ReconsiderDecisions: evaluation.ReconsiderDecisions(),
	}
}

// evaluationQueryView echoes the query the service was actually asked, so a
// page can tell what it is looking at from the answer rather than from what it
// believes it sent.
//
// It matters most for the two values the caller does not fully control. An
// empty sort is the service's default order rather than "no order", and a
// pinned snapshot that has since been pruned is answered from the current one
// with Stale set — so a client that rendered its own request back would label
// the page with a snapshot nobody served it from.
type evaluationQueryView struct {
	Kind string `json:"kind"`
	Lane string `json:"lane"`
	Sort string `json:"sort"`
	// Role narrows coverage to one review role. It is a separate axis from
	// Coverage rather than more values in it, because §4.12 makes coverage
	// role-specific: a reception vote does not satisfy an evidence check,
	// so "unreviewed" is a question about a role and the two have to be
	// nameable independently.
	Role     string `json:"role"`
	Coverage string `json:"coverage"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
	Snapshot string `json:"snapshot"`
}

// evaluationListResult is GET /api/evaluation/list's response: the service's
// own page, the query it answered, and the vocabularies the filters offer.
type evaluationListResult struct {
	evaluation.Page
	Query      evaluationQueryView  `json:"query"`
	Vocabulary evaluationVocabulary `json:"vocabulary"`
}

// handleEvaluationList serves one page of the ranked eligible set.
//
// The window is the shared ?limit=&offset= pair every Phase B listing takes,
// refused rather than clamped, and it is handed to the service rather than
// applied here. §8.5 requires the complete represented set to be ordered
// before paging: a route that read everything and sliced would rank each page
// independently, and would also be the whole-corpus read that section forbids.
//
// No filter value is checked here. Kind, lane, sort, role and coverage are
// internal/evaluation's closed vocabularies, and an unknown one reaches the
// service and is refused by name, on the same terms handleReviewDecide passes
// an unknown disposition through: there is exactly one place that decides what
// a value may be, so the two cannot disagree about it.
func (s *Server) handleEvaluationList(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	window, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	query := evaluation.Query{
		Kind:     values.Get("kind"),
		Lane:     values.Get("lane"),
		Sort:     values.Get("sort"),
		Role:     values.Get("role"),
		Coverage: values.Get("coverage"),
		Snapshot: values.Get("snapshot"),
		Limit:    window.limit,
		Offset:   window.offset,
	}
	page, err := s.opts.Evaluation.List(r.Context(), query)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, evaluationListResult{
		Page: page,
		Query: evaluationQueryView{
			Kind:     query.Kind,
			Lane:     query.Lane,
			Sort:     query.Sort,
			Role:     query.Role,
			Coverage: query.Coverage,
			Limit:    query.Limit,
			Offset:   query.Offset,
			// The snapshot served, not the snapshot asked for.
			Snapshot: page.Snapshot,
		},
		Vocabulary: evaluationVocabularies(),
	})
}

// evaluationDetailResult is GET /api/evaluation/detail's response.
//
// The vocabularies travel with it because the controls that need them live on
// this page: a feedback reason, a criterion resolution and a reconsideration
// decision are all offered where their subject is read (§8.5), and a page that
// had to fetch the listing to learn what a feedback reason may be would be
// holding a second copy of the contract.
type evaluationDetailResult struct {
	evaluation.Detail
	Vocabulary evaluationVocabulary `json:"vocabulary"`
	// Decisions names where this record's disposition is decided. It is a
	// pointer to another surface rather than a control of its own: §4.12
	// keeps reception and operator authority separate, and the accept,
	// reject, defer and refine vocabulary already belongs to
	// internal/review. Empty for a subject kind that carries no review
	// disposition, which is how a page knows not to offer the link.
	Decisions evaluationDecisionLink `json:"decisions"`
}

// evaluationDecisionLink names the existing review surface for a subject, by
// the identity that surface routes on rather than by a URL assembled here.
type evaluationDecisionLink struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// handleEvaluationDetail serves one subject whole: its item, its evaluation
// history, the assignments that produced it, and the alternatives it is read
// beside.
//
// The subject is a kind and an id, and it is the exact revision rather than a
// chain head. §4.12 binds a vote to the wording that was read, so a route that
// resolved a root to its newest revision would hand back a history whose votes
// are about text this page is not showing.
func (s *Server) handleEvaluationDetail(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	kind, ok := s.requireID(w, r, "kind")
	if !ok {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	detail, err := s.opts.Evaluation.Detail(r.Context(), evaluation.Subject{Kind: kind, ID: id})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, evaluationDetailResult{
		Detail:     detail,
		Vocabulary: evaluationVocabularies(),
		Decisions:  evaluationDecisions(detail.Item.Artifact.Subject),
	})
}

// evaluationDecisions resolves a subject onto internal/review's own record
// identity, or reports none.
//
// The mapping is internal/frontier's vocabulary rather than a table of
// strings: a kind the review surface has no page for answers empty, so a
// client offers no link rather than one that 404s.
func evaluationDecisions(subject evaluation.Subject) evaluationDecisionLink {
	kind, ok := refKind(subject.Kind)
	if !ok || !reviewableRef(kind) {
		return evaluationDecisionLink{}
	}
	return evaluationDecisionLink{Type: subject.Kind, ID: subject.ID}
}

// reviewableRef restates §6.7's reviewable kinds for the link above. An
// observation is evidence a finding consolidates rather than something an
// operator accepts or rejects, so the review surface has no page for it and
// offering a link there would send a reader to a refusal.
func reviewableRef(kind frontier.EntityType) bool {
	switch kind {
	case frontier.EntityHypothesis, frontier.EntityFinding, frontier.EntityProposal:
		return true
	}
	return false
}

// evaluationCoverageResult is GET /api/evaluation/coverage's response: the
// inventory, and the kinds and roles it spans.
//
// The vocabularies are here because §8.5 requires coverage to be readable by
// applicable review role and across every covered output kind, including the
// ones with no items at all. A page that derived its kind list from the rows
// it received would silently stop showing a kind the moment nothing in it was
// due, which is the exact opposite of what a coverage inventory is for.
type evaluationCoverageResult struct {
	Coverage evaluation.Coverage `json:"coverage"`
	Kinds    []string            `json:"kinds"`
	Roles    []string            `json:"roles"`
}

// handleEvaluationCoverage serves the shared coverage inventory.
//
// It is a route of its own rather than a field on the listing because the two
// answer different questions and one of them has to be answerable when the
// other is empty: a filter that matches nothing still has a coverage summary,
// and §8.5's "the check finished, and work is still overdue" is a statement
// about the sweep rather than about a page of rows.
func (s *Server) handleEvaluationCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	coverage, err := s.opts.Evaluation.Coverage(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, evaluationCoverageResult{
		Coverage: coverage,
		Kinds:    evaluation.Kinds(),
		Roles:    evaluation.Roles(),
	})
}

// The four things §8.5 requires this surface to be able to say about
// authorized evaluation work. They are a closed vocabulary because the page
// renders each one differently and an unrecognized fifth would render as
// nothing.
const (
	// evaluationRunning is work actually in flight: at least one
	// assignment is claimed and neither completed nor expired. It is never
	// inferred from an enabled policy — a policy that permits a draw is not
	// a draw.
	evaluationRunning = "running"
	// evaluationScheduled is enabled with nothing in flight: the next draw
	// happens when the schedule reaches it.
	evaluationScheduled = "scheduled"
	// evaluationPaused is the policy disabled. Nothing is drawn, and the
	// backlog stays visible rather than being hidden by the pause.
	evaluationPaused = "paused"
	// evaluationUnavailable is the projection unreadable. It is not the
	// same claim as paused, and the page must not merge them: paused means
	// Babel is not reviewing, unavailable means Babel cannot say.
	evaluationUnavailable = "unavailable"
)

// evaluationSavingStartsNothing is the sentence a saved policy answers with.
//
// §8.5 states it as a requirement — "saving a policy is not permission to
// launch compute" — and this is where an operator is most likely to assume
// otherwise, because the form he just submitted is the one with the budget in
// it. It is served rather than written in the client for the reason every
// consequence sentence on the focus surface is: the operator has to be told
// what he did and what he did not do, in the words of the surface that did it.
const evaluationSavingStartsNothing = "the policy is stored. It bounds what an authorized draw may spend " +
	"on the schedule this deployment already runs; saving it starts no run, launches no compute, and " +
	"reviews nothing by itself."

// evaluationPolicyResult is what both policy routes answer with: the stored
// policy, the coverage it is being judged against, and what the deployment is
// actually doing about it.
type evaluationPolicyResult struct {
	Policy   evaluation.Policy   `json:"policy"`
	Coverage evaluation.Coverage `json:"coverage"`
	Status   string              `json:"status"`
	// Detail is the sentence behind Status, and it is the server's rather
	// than the page's because the two halves — what the policy says and
	// what the projection observed — are only both in hand here.
	Detail string `json:"detail"`
	// NextDraw is when the schedule is next due, empty when the policy is
	// disabled or the projection cannot say. An absent time is a real
	// state: a deployment that has never run a coverage check has no
	// cadence to count from, and printing a computed instant would be this
	// surface inventing a schedule.
	NextDraw string `json:"next_draw,omitempty"`
	// Saving is the fixed sentence above. It travels on the read as well
	// as on the write, so the form can state the consequence before the
	// operator commits to it rather than after.
	Saving string `json:"saving"`
	// Record is the policy record a write appended, and is absent on a
	// read. It is the durable proof the operator's configuration was
	// recorded as an attributed record rather than as a settings blob.
	Record *evaluation.Record `json:"record,omitempty"`
}

// handleEvaluationPolicy serves the stored policy and what is happening under
// it.
func (s *Server) handleEvaluationPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	policy, err := s.opts.Evaluation.Policy(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.evaluationPolicyView(r, policy, nil))
}

// handleEvaluationConfigure stores the operator's policy.
//
// The body is an evaluation.Policy decoded directly, unknown fields refused.
// It is the service's own struct rather than a request type mirroring it
// because a mirror is how a knob silently stops being saved: a field added to
// the policy and forgotten here would not fail to build, it would arrive as a
// zero budget. Nothing in the body is validated here either — the ranges, the
// shares that must sum, and the version admission are policy.go's, checked
// before anything is appended.
//
// The author is the session's operator and cannot be anything else: Configure
// takes the identity as its own argument, so there is no field in the decoded
// policy that could carry one.
func (s *Server) handleEvaluationConfigure(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	var policy evaluation.Policy
	if !s.decodeBody(w, r, &policy) {
		return
	}
	record, err := s.opts.Evaluation.Configure(r.Context(), by.ID(), policy)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The stored policy is read back rather than echoed from the request.
	// A version is minted by the service and conservative defaults are
	// filled in there, so answering with what was sent would show the
	// operator a configuration that is not the one in force.
	stored, err := s.opts.Evaluation.Policy(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.evaluationPolicyView(r, stored, &record))
}

// evaluationPolicyView assembles a policy answer, including the one derivation
// this surface performs: which of §8.5's four states the deployment is in.
//
// Every input to that derivation is observed. Running means the projection
// counted claimed work that has neither finished nor expired; scheduled means
// the policy permits a draw and none is in flight; paused means it does not;
// unavailable means the coverage read could not answer, which is a different
// claim from either. Nothing here concludes that work is happening because a
// policy allows it.
func (s *Server) evaluationPolicyView(r *http.Request, policy evaluation.Policy, record *evaluation.Record) evaluationPolicyResult {
	result := evaluationPolicyResult{
		Policy: policy,
		Saving: evaluationSavingStartsNothing,
		Record: record,
	}
	coverage, err := s.opts.Evaluation.Coverage(r.Context())
	if err != nil {
		// A coverage read that failed is reported as not knowing, and
		// the failure's own text stays out of it (§9): the page needs
		// to know that the state is unobserved, not what the database
		// said about it.
		s.logf("%s %s: evaluation coverage refused: %s", r.Method, r.URL.Path, evaluationRefusal(err))
		result.Status = evaluationUnavailable
		result.Detail = "the evaluation projection could not be read, so this session cannot say whether " +
			"authorized review work is running. The stored policy above is what it would run under."
		return result
	}
	result.Coverage = coverage
	switch {
	case coverage.Reason != "":
		result.Status = evaluationUnavailable
		result.Detail = "the coverage inventory is degraded, so what is running cannot be stated from it: " +
			coverage.Reason
	case !policy.Enabled:
		result.Status = evaluationPaused
		result.Detail = "authorized evaluation work is paused. Nothing is drawn and nothing is spent; " +
			"the overdue and never-reviewed counts above keep accruing and stay visible."
	case coverage.Active > 0:
		result.Status = evaluationRunning
		// The count is stated, so the verb has to agree with it: "1
		// assignments are in flight" is the kind of detail that makes
		// an operator distrust the number beside it.
		claimed := "assignments are"
		if coverage.Active == 1 {
			claimed = "assignment is"
		}
		result.Detail = fmt.Sprintf("%d evaluation %s claimed and still in flight.", coverage.Active, claimed)
	default:
		result.Status = evaluationScheduled
		result.Detail = "authorized evaluation work is enabled with nothing in flight: the next draw " +
			"happens when the schedule reaches it."
	}
	if policy.Enabled && !coverage.NextDraw.IsZero() {
		result.NextDraw = timeText(coverage.NextDraw)
	}
	return result
}

// evaluationOperatorRequest is POST /api/evaluation/operator's body: the
// operator's own attributed statement about one subject.
//
// Four fields are deliberately absent, and each absence is an authority this
// surface does not hold.
//
// Operator is absent because the author is the launch session's identity. A
// body that could name one would let any local process record a decision
// against somebody else's name, and unknown fields are refused, so an attempt
// to send one is a 400 rather than a silently ignored field.
//
// Policy is absent because configuring the policy is its own route, reaching
// Service.Configure and its validation. A second path to the same record kind
// would be a second implementation of the one thing §5.8 requires to be
// bounded.
//
// Context is absent because an evaluation context is derived from the Reality
// ledger's recorded work, pain and allowances, which have their own operator
// surface with their own acceptance rules (§4.8). An operator correcting a
// mistaken context assumption corrects the fact, and the recommendation
// changes because the input did — not because a form overwrote the reasoning.
//
// SupersedesID is absent because a correction of an evaluation record is
// Store.Correct's, which corrects a record its own instance authored. Nothing
// a browser sends may rewrite a producer's immutable record.
type evaluationOperatorRequest struct {
	Subject evaluation.Subject `json:"subject"`
	Kind    string             `json:"kind"`
	Reason  string             `json:"reason"`
	// Criteria carries a criteria record's acceptance criteria. It is a
	// list rather than a single criterion because §4.12 versions the set:
	// criteria resolved after acceptance are identifiable as a later
	// decision precisely because the whole set is restated and linked,
	// rather than one line being edited into the earlier one.
	Criteria []evaluation.Criterion `json:"criteria"`
	// RelatedID scopes the statement to the record it answers: the
	// reconsider item a decision resolves, or the operator decision a
	// feedback reason accompanies. §5.8 requires the reason to accompany a
	// disposition rather than to create one, and this is the field that
	// keeps the two linked without conflating them.
	RelatedID string `json:"related_id"`
	// Decision is the polarity of a reconsideration decision: reopen what
	// was decided, or retain it. It is a field of its own rather than
	// something read out of Reason because the two are opposite acts and
	// Reason is prose: a route that inferred the act from the wording would
	// let "not reopening, the benchmark is irrelevant" reopen a rejected
	// record, and would hand any model-authored or pasted sentence the
	// choice. Empty on every other kind, and required by the store on this
	// one, so a decision with no stated polarity is refused rather than
	// defaulted to either act.
	Decision string `json:"decision"`
}

// evaluationOperatorResult is what an operator write answers with.
type evaluationOperatorResult struct {
	Record evaluation.Record `json:"record"`
	// Decided is the sentence about what this write did and did not do. It
	// is here for the reason #115's capture result carries one: the
	// control an operator just used sits beside real decision controls,
	// and the honest thing to tell him is which of the two he pressed.
	Decided string `json:"decided"`
}

// The three sentences an operator write answers with.
//
// They are three rather than one because one of them would be false about the
// others. A criteria or feedback record decides nothing at all. A retain
// decides that the earlier ruling stands, and moves no disposition. A reopen
// does move one: internal/evaluation records the decision and the reopened
// disposition in one transaction, so when this route answers, the record is
// reopened and the review surface reads it as undecided. Telling an operator
// that his reopen changed nothing would be the same defect in the other
// direction as telling a retain that it reopened something.
const (
	evaluationOperatorDecided = "this is your own attributed statement about the record. " +
		"It accepts, rejects, defers, reopens and merges nothing: a disposition is recorded on the review " +
		"surface that owns the vocabulary, and reopening a decided record stays an explicit act there."
	evaluationReopenRecorded = "your decision to reopen this is recorded, and the record was reopened " +
		"with it: the decision you reopened keeps its place in the history, and the review surface now " +
		"reads this record as undecided, so it can be decided again on its merits. The two are one act — " +
		"had either failed, neither would exist."
	evaluationRetainRecorded = "your decision to retain the earlier ruling is recorded, attributed to you " +
		"and scoped to the change it answers. Nothing was reopened and nothing was re-decided; the " +
		"reconsider item stays readable beside the decision that answered it."
)

// evaluationDecided is the sentence for the act that was recorded, read from
// the stored record rather than from the request: what an operator is told he
// did is what the store accepted, so a polarity the service rewrote or refused
// cannot be reported back as the one he sent.
func evaluationDecided(record evaluation.Record) string {
	if record.Kind != evaluation.KindReconsiderDecision {
		return evaluationOperatorDecided
	}
	switch record.Decision {
	case evaluation.ReconsiderReopen:
		return evaluationReopenRecorded
	case evaluation.ReconsiderRetain:
		return evaluationRetainRecorded
	default:
		// A polarity this build has no sentence for. The general
		// statement is true of every operator record, so it is what a
		// vocabulary that grew ahead of this file answers with.
		return evaluationOperatorDecided
	}
}

// handleEvaluationOperator records one operator statement: acceptance
// criteria, a scoped feedback reason, or a reconsideration decision.
//
// The kind is not filtered here. internal/evaluation's store accepts exactly
// the operator-authored subset and refuses the run-authored kinds by name, so
// a request naming `assessment` is refused by the one place that knows why
// rather than by a second list here that could fall behind it.
func (s *Server) handleEvaluationOperator(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	var request evaluationOperatorRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	record, err := s.opts.Evaluation.Operator(r.Context(), evaluation.OperatorInput{
		Subject:   request.Subject,
		Kind:      request.Kind,
		Operator:  by.ID(),
		Reason:    request.Reason,
		Decision:  request.Decision,
		Criteria:  request.Criteria,
		RelatedID: request.RelatedID,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, evaluationOperatorResult{
		Record:  record,
		Decided: evaluationDecided(record),
	})
}

// evaluationRefusal names the rule that refused, for the diagnostics stream
// only. It reuses classifyService's own classification so a logged refusal and
// the client's message cannot describe different things, and it carries none
// of the error's text for the reason serviceError carries none.
func evaluationRefusal(err error) string {
	_, message := classifyService(err)
	return message
}

// classifyEvaluation maps internal/evaluation's sentinels onto a status and a
// fixed message. It is a function of its own, called from classifyService, so
// the evaluation vocabulary stays readable as one block rather than dissolving
// into a switch that already spans five packages.
//
// ErrNoWork is deliberately unmapped. It is the sampler's answer to a draw,
// and this surface cannot draw — a case for it here would be dead code
// claiming to handle a refusal no route can provoke.
func classifyEvaluation(err error) (int, string, bool) {
	switch {
	case errors.Is(err, evaluation.ErrNotFound):
		return http.StatusNotFound, "no evaluation subject or record with that identifier", true
	case errors.Is(err, evaluation.ErrInvalid):
		return http.StatusBadRequest, "a value in the request is outside what the evaluation service accepts", true
	case errors.Is(err, evaluation.ErrConflict):
		return http.StatusConflict, "the evaluation record this acts on has already moved", true
	case errors.Is(err, evaluation.ErrBudget):
		return http.StatusConflict, "the authorized evaluation budget for this period is exhausted", true
	case errors.Is(err, evaluation.ErrUnavailable):
		// 503 rather than the 409 an unwired service gets, because the
		// two are different states: a build with no evaluation store
		// will never answer this route, and a projection that has not
		// been built yet will answer it after the next refresh.
		return http.StatusServiceUnavailable,
			"the evaluation projection is not readable in this session", true
	default:
		return 0, "", false
	}
}
