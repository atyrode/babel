package web

// The §4.8 Reality Ledger surface: the question inbox, an entity's current
// reality, and the two acts §4.8 gives an operator — retaining an answer and
// the single explicit acceptance that lets a plan touch reality.
//
// Both mutations call the ledger, which is the service: internal/reality owns
// the state machine, the atomic commit, and the rule that an accepted plan's
// facts are attributed to the accepting operator rather than to the
// interpretation that proposed them. No route here asserts a fact, supersedes
// one, merges an entity, or installs a focus rule; those are reachable only
// through a plan the ledger recorded and an operator accepted, which is what
// §4.8 means by no model-authorized fact mutation.

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/atyrode/babel/internal/reality"
)

// QuestionSummary is one ranked inbox question with its answers and the
// interpretations they produced.
//
// The score's terms travel with the score because §4.8's five factors are a
// policy an operator will want to argue with, and a bare number cannot be
// argued with.
type QuestionSummary struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	Class            string         `json:"class"`
	State            string         `json:"state"`
	Sensitivity      string         `json:"sensitivity"`
	CreatedAt        string         `json:"created_at"`
	Prompt           string         `json:"prompt"`
	WhyAsked         string         `json:"why_asked"`
	TargetEntityIDs  []string       `json:"target_entity_ids"`
	TargetPredicates []string       `json:"target_predicates,omitempty"`
	Score            int            `json:"score"`
	Terms            map[string]int `json:"terms,omitempty"`
	Answers          []answerView   `json:"answers"`
	Plans            []planView     `json:"plans"`
}

type answerView struct {
	ID         string `json:"id"`
	QuestionID string `json:"question_id"`
	Sequence   int    `json:"sequence"`
	Author     string `json:"author"`
	At         string `json:"at"`
	RecordedAt string `json:"recorded_at"`
	Outcome    string `json:"outcome"`
	Text       string `json:"text"`
}

type planView struct {
	ID                 string       `json:"id"`
	QuestionID         string       `json:"question_id"`
	AnswerID           string       `json:"answer_id"`
	InterpreterVersion int          `json:"interpreter_version"`
	CreatedAt          string       `json:"created_at"`
	State              string       `json:"state"`
	Summary            string       `json:"summary"`
	Actions            []actionView `json:"actions"`
}

type actionView struct {
	ID        string                `json:"id"`
	Position  int                   `json:"position"`
	Kind      string                `json:"kind"`
	State     string                `json:"state"`
	ResultID  string                `json:"result_id,omitempty"`
	AppliedAt string                `json:"applied_at,omitempty"`
	Payload   reality.ActionPayload `json:"payload"`
}

type inboxResult struct {
	Items []QuestionSummary `json:"items"`
	Total int               `json:"total"`
}

// handleRealityInbox serves the prioritized question inbox.
//
// The ranking is the ledger's, not this route's: §4.8 fixes the factors, and a
// surface that re-sorted the inbox would be substituting its own policy for the
// one whose arithmetic it is showing.
func (s *Server) handleRealityInbox(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	query := reality.InboxQuery{Limit: listScanCap}
	if value := r.URL.Query().Get("class"); value != "" {
		query.Class = reality.QuestionClass(value)
	}
	items, err := s.opts.Reality.Inbox(r.Context(), query)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := inboxResult{Items: []QuestionSummary{}, Total: len(items)}
	start, end := pg.window(len(items))
	for _, item := range items[start:end] {
		summary, err := s.summarizeQuestion(r.Context(), item)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		result.Items = append(result.Items, summary)
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) summarizeQuestion(ctx context.Context, item reality.InboxItem) (QuestionSummary, error) {
	question := item.Question
	summary := QuestionSummary{
		ID:              question.ID,
		Kind:            string(question.Kind),
		Class:           string(question.Class),
		State:           string(question.State),
		Sensitivity:     string(question.Sensitivity),
		CreatedAt:       timeText(question.CreatedAt),
		Prompt:          question.Payload.Prompt,
		WhyAsked:        question.Payload.WhyAsked,
		TargetEntityIDs: question.TargetEntityIDs,
		Score:           item.Score,
		Terms:           item.Terms,
		Answers:         []answerView{},
		Plans:           []planView{},
	}
	if summary.TargetEntityIDs == nil {
		summary.TargetEntityIDs = []string{}
	}
	for _, predicate := range question.TargetPredicates {
		summary.TargetPredicates = append(summary.TargetPredicates, string(predicate))
	}
	answers, err := s.opts.Reality.Answers(ctx, question.ID)
	if err != nil {
		return QuestionSummary{}, err
	}
	for _, answer := range answers {
		summary.Answers = append(summary.Answers, answerView{
			ID:         answer.ID,
			QuestionID: answer.QuestionID,
			Sequence:   answer.Sequence,
			Author:     answer.Author,
			At:         timeText(answer.At),
			RecordedAt: timeText(answer.RecordedAt),
			Outcome:    string(answer.Outcome),
			Text:       answer.Payload.Text,
		})
	}
	plans, err := s.plansFor(ctx, question.ID)
	if err != nil {
		return QuestionSummary{}, err
	}
	summary.Plans = plans
	return summary, nil
}

// planID matches the ledger's plan identifier shape.
var planID = regexp.MustCompile(`\bpln_[0-9A-Za-z]+\b`)

// plansFor finds the interpretations a question produced.
//
// internal/reality exposes no plans-by-question query — it answers Plan(id) —
// so the question's own append-only state history is read for the identifiers it
// names, and each candidate is resolved through Plan, which is authoritative.
// That inversion is what makes reading a note safe: a hint that matches nothing
// yields no plan rather than a wrong one, so the worst outcome of a changed note
// format is an empty list, and an operator who needs the plan can still reach it
// through the CLI. A plans-by-question listing on the ledger would replace this
// entirely.
func (s *Server) plansFor(ctx context.Context, questionID string) ([]planView, error) {
	history, err := s.opts.Reality.QuestionHistory(ctx, questionID)
	if err != nil {
		return nil, err
	}
	views := []planView{}
	seen := map[string]struct{}{}
	for i := len(history) - 1; i >= 0; i-- {
		for _, candidate := range planID.FindAllString(history[i].Payload.Note, -1) {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			plan, err := s.opts.Reality.Plan(ctx, candidate)
			if err != nil {
				continue
			}
			if plan.QuestionID != questionID {
				continue
			}
			views = append(views, viewPlan(plan))
		}
	}
	return views, nil
}

func viewPlan(plan reality.Plan) planView {
	view := planView{
		ID:                 plan.ID,
		QuestionID:         plan.QuestionID,
		AnswerID:           plan.AnswerID,
		InterpreterVersion: plan.InterpreterVersion,
		CreatedAt:          timeText(plan.CreatedAt),
		State:              string(plan.State),
		Summary:            plan.Payload.Summary,
		Actions:            make([]actionView, 0, len(plan.Actions)),
	}
	for _, action := range plan.Actions {
		view.Actions = append(view.Actions, actionView{
			ID:        action.ID,
			Position:  action.Position,
			Kind:      string(action.Kind),
			State:     string(action.State),
			ResultID:  action.ResultID,
			AppliedAt: timeText(action.AppliedAt),
			Payload:   action.Payload,
		})
	}
	return view
}

type entityView struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
	Role          string `json:"role"`
	CanonicalID   string `json:"canonical_id"`
	DisplayName   string `json:"display_name"`
	Notes         string `json:"notes,omitempty"`
}

type aliasView struct {
	ID        string `json:"id"`
	EntityID  string `json:"entity_id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	Value     string `json:"value"`
	Note      string `json:"note,omitempty"`
}

type relationshipEnd struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type relationshipView struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	State     string          `json:"state"`
	CreatedAt string          `json:"created_at"`
	From      relationshipEnd `json:"from"`
	To        relationshipEnd `json:"to"`
	Note      string          `json:"note,omitempty"`
}

type factValueView struct {
	Kind     string `json:"kind"`
	Enum     string `json:"enum,omitempty"`
	Text     string `json:"text,omitempty"`
	ObjectID string `json:"object_id,omitempty"`
}

type factAuthorityView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	At   string `json:"at"`
}

type factView struct {
	ID          string            `json:"id"`
	SubjectID   string            `json:"subject_id"`
	Predicate   string            `json:"predicate"`
	Value       factValueView     `json:"value"`
	ValidFrom   string            `json:"valid_from"`
	ValidUntil  string            `json:"valid_until,omitempty"`
	ObservedAt  string            `json:"observed_at"`
	RecordedAt  string            `json:"recorded_at"`
	ExpiresAt   string            `json:"expires_at,omitempty"`
	Authority   factAuthorityView `json:"authority"`
	Confidence  string            `json:"confidence"`
	Sensitivity string            `json:"sensitivity"`
	Status      string            `json:"status"`
	Supersedes  string            `json:"supersedes,omitempty"`
	Note        string            `json:"note,omitempty"`
}

type entityDetail struct {
	Entity        entityView         `json:"entity"`
	Aliases       []aliasView        `json:"aliases"`
	Relationships []relationshipView `json:"relationships"`
	Facts         []factView         `json:"facts"`
}

// handleRealityEntity serves one entity's current reality: its identity, the
// names it is known by, its edges, and its facts.
//
// Every fact status is included, superseded revisions and proposals alike,
// because reviewing what was proposed is a real need and a revision chain that
// showed only its head would hide how reality was corrected.
func (s *Server) handleRealityEntity(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	entity, err := s.opts.Reality.Entity(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	aliases, err := s.opts.Reality.Aliases(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	relationships, err := s.opts.Reality.Relationships(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	facts, err := s.opts.Reality.Facts(ctx, reality.FactQuery{SubjectID: id})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	detail := entityDetail{
		Entity: entityView{
			ID:            entity.ID,
			Kind:          string(entity.Kind),
			SchemaVersion: entity.SchemaVersion,
			CreatedAt:     timeText(entity.CreatedAt),
			Role:          string(entity.Role),
			CanonicalID:   entity.CanonicalID,
			DisplayName:   entity.Payload.DisplayName,
			Notes:         entity.Payload.Notes,
		},
		Aliases:       make([]aliasView, 0, len(aliases)),
		Relationships: make([]relationshipView, 0, len(relationships)),
		Facts:         make([]factView, 0, len(facts)),
	}
	for _, alias := range aliases {
		detail.Aliases = append(detail.Aliases, aliasView{
			ID:        alias.ID,
			EntityID:  alias.EntityID,
			Kind:      string(alias.Kind),
			State:     string(alias.State),
			CreatedAt: timeText(alias.CreatedAt),
			Value:     alias.Payload.Value,
			Note:      alias.Payload.Note,
		})
	}
	for _, relationship := range relationships {
		detail.Relationships = append(detail.Relationships, relationshipView{
			ID:        relationship.ID,
			Kind:      string(relationship.Kind),
			State:     string(relationship.State),
			CreatedAt: timeText(relationship.CreatedAt),
			From:      s.relationshipEnd(ctx, relationship.FromID, entity),
			To:        s.relationshipEnd(ctx, relationship.ToID, entity),
			Note:      relationship.Payload.Note,
		})
	}
	for _, fact := range facts {
		detail.Facts = append(detail.Facts, factView{
			ID:        fact.ID,
			SubjectID: fact.SubjectID,
			Predicate: string(fact.Predicate),
			Value: factValueView{
				Kind:     string(fact.Value.Kind),
				Enum:     fact.Value.Enum,
				Text:     fact.Value.Text,
				ObjectID: fact.Value.ObjectID,
			},
			ValidFrom:  timeText(fact.ValidFrom),
			ValidUntil: timeText(fact.ValidUntil),
			ObservedAt: timeText(fact.ObservedAt),
			RecordedAt: timeText(fact.RecordedAt),
			ExpiresAt:  timeText(fact.ExpiresAt),
			Authority: factAuthorityView{
				Kind: string(fact.Authority.Kind),
				ID:   fact.Authority.ID,
				At:   timeText(fact.Authority.At),
			},
			Confidence:  string(fact.Confidence),
			Sensitivity: string(fact.Sensitivity),
			Status:      string(fact.Status),
			Supersedes:  fact.Supersedes,
			Note:        fact.Payload.Note,
		})
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// relationshipEnd names one end of an edge, resolving the far entity's display
// name best effort so an edge reads as prose. The requested entity is already
// in hand, so only the other end costs a read.
func (s *Server) relationshipEnd(ctx context.Context, id string, known reality.Entity) relationshipEnd {
	if id == known.ID {
		return relationshipEnd{ID: id, DisplayName: known.Payload.DisplayName}
	}
	end := relationshipEnd{ID: id}
	if other, err := s.opts.Reality.Entity(ctx, id); err == nil {
		end.DisplayName = other.Payload.DisplayName
	}
	return end
}

type answerRequest struct {
	QuestionID string `json:"questionId"`
	Text       string `json:"text"`
	Outcome    string `json:"outcome"`
}

type answerResult struct {
	AnswerID string `json:"answerId"`
	State    string `json:"state"`
}

// handleRealityAnswer retains one operator answer verbatim (§4.8).
//
// The text is passed to the ledger exactly as it arrived. §4.8 requires
// verbatim retention, so nothing here trims, normalizes, or renders it, and
// nothing here reads it either: the answer is provenance, and the only thing
// permitted to interpret it is the versioned Answer Interpreter, with the
// question and context snapshot alongside it. What comes back out of any read
// route is escaped like every other untrusted value.
//
// The resulting state is read from the ledger rather than predicted from the
// outcome, because the question's state machine is the ledger's and a surface
// that guessed it could report a transition that did not happen.
func (s *Server) handleRealityAnswer(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	var request answerRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.QuestionID == "" {
		s.writeError(w, http.StatusBadRequest, "questionId is required")
		return
	}
	outcome := reality.OutcomeAnswered
	switch request.Outcome {
	case "", string(reality.OutcomeAnswered):
	case string(reality.OutcomeUnknown):
		outcome = reality.OutcomeUnknown
	case string(reality.OutcomeDeclined):
		outcome = reality.OutcomeDeclined
	default:
		s.writeError(w, http.StatusBadRequest, "outcome is not an answer outcome")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	// The question is resolved before the write. It is not a second
	// authorization — the ledger enforces the state machine either way — but a
	// question the ledger does not hold fails the answer row's foreign key
	// rather than a named rule, and "no record with that identifier" is the
	// answer a caller can act on instead of "something failed".
	if _, err := s.opts.Reality.Question(r.Context(), request.QuestionID); err != nil {
		s.serviceError(w, r, err)
		return
	}
	answer, err := s.opts.Reality.RecordAnswer(r.Context(), reality.AnswerInput{
		QuestionID: request.QuestionID,
		Author:     by.ID(),
		// The answer's own instant is this machine's clock: §4.8 separates
		// when an operator answered from when the ledger recorded it, and
		// the ledger stamps the second one itself.
		At:      time.Now().UTC(),
		Outcome: outcome,
		Text:    request.Text,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	question, err := s.opts.Reality.Question(r.Context(), request.QuestionID)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, answerResult{AnswerID: answer.ID, State: string(question.State)})
}

type planAcceptRequest struct {
	PlanID string `json:"planId"`
}

type appliedView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type planAcceptResult struct {
	Applied []appliedView `json:"applied"`
	State   string        `json:"state"`
}

// handleRealityPlanAccept performs §4.8's one explicit operator acceptance.
//
// Everything that makes this safe belongs to the ledger and is reached, not
// reimplemented: the acceptance, the plan's mutations, and the question's
// disposition are one transaction; a unique index makes a double-click a
// refusal rather than a second application; and every applied fact is
// attributed to the accepting operator and the acceptance instant, whatever the
// interpretation proposed, so agent interpretation cannot become authoritative
// reality by passing through a browser.
//
// Nothing here applies a proposal or publishes anything: an accepted plan
// changes Babel's own ledger and nothing outside it (§4.6, decision 13).
func (s *Server) handleRealityPlanAccept(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	var request planAcceptRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.PlanID == "" {
		s.writeError(w, http.StatusBadRequest, "planId is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	_, application, err := s.opts.Reality.AcceptPlan(r.Context(), reality.AcceptanceInput{
		PlanID: request.PlanID,
		Actor:  by.ID(),
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := planAcceptResult{Applied: []appliedView{}, State: string(application.QuestionState)}
	for _, id := range application.FactIDs {
		result.Applied = append(result.Applied, appliedView{Kind: "fact", ID: id})
	}
	for _, id := range application.DisputeIDs {
		result.Applied = append(result.Applied, appliedView{Kind: "dispute", ID: id})
	}
	for _, id := range application.ResolutionIDs {
		result.Applied = append(result.Applied, appliedView{Kind: "resolution", ID: id})
	}
	for _, version := range application.FocusVersions {
		result.Applied = append(result.Applied, appliedView{Kind: "focus", ID: focusVersion(version)})
	}
	s.writeJSON(w, http.StatusOK, result)
}

// focusVersion identifies an installed focus rule set by its version, which is
// the only identity a rule set has: §4.8 installs a version rather than editing
// one, because a version's bytes are what makes a past decision explainable.
func focusVersion(version int) string {
	return strconv.Itoa(version)
}
