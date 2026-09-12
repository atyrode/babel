package web

// The §4.8 Reality Ledger surface: what the ledger holds and the two acts §4.8
// gives an operator over it — retaining an answer and the single explicit
// acceptance that lets a plan touch reality.
//
// The reads are listings, records and histories rather than one entity lookup,
// and that is §8.4's requirement rather than a convenience: every stored thing
// is to be reachable by moving through the interface, so the questions the
// ledger has ever asked, the subjects it knows, and a fact's own revision and
// status chain each have a route a page can arrive at. A record only a
// hand-typed identifier could reach was a record the product did not have.
//
// Both mutations call the ledger, which is the service: internal/reality owns
// the state machine, the atomic commit, and the rule that an accepted plan's
// facts are attributed to the accepting operator rather than to the
// interpretation that proposed them. No route here asserts a fact, supersedes
// one, merges an entity, or installs a focus rule; nothing a model proposed
// becomes reality except through a plan the ledger recorded and an operator
// accepted, which is what §4.8 means by no model-authorized fact mutation.
//
// One thing this file used to be the whole answer to has moved next door.
// internal/web/focus.go states an operator's own analysis policy for a
// subject, which is an attributed operator action rather than a model
// interpretation — the authority §4.8 admits, exercised deliberately, from the
// surface the operator is already looking at. It is a separate file with a
// separate service surface for that reason: what it may write is one
// predicate, and no route in this file can reach it.

import (
	"context"
	"net/http"
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
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Class           string   `json:"class"`
	State           string   `json:"state"`
	Sensitivity     string   `json:"sensitivity"`
	CreatedAt       string   `json:"created_at"`
	Prompt          string   `json:"prompt"`
	WhyAsked        string   `json:"why_asked"`
	TargetEntityIDs []string `json:"target_entity_ids"`
	// AboutNames names the same entities, positionally: index i is what a
	// reader calls target_entity_ids[i], empty where the ledger can no
	// longer name it. The identifiers stay because they are what a link
	// resolves and what a merge is argued about; the names are here
	// because "About: ent_4468a31b" is a lookup task rather than a
	// sentence, and the surface that printed it was asking the operator
	// to do the ledger's job.
	AboutNames       []string       `json:"about_name"`
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
	summary.AboutNames = s.entityNames(ctx, summary.TargetEntityIDs)
	for _, predicate := range question.TargetPredicates {
		summary.TargetPredicates = append(summary.TargetPredicates, string(predicate))
	}
	answers, err := s.opts.Reality.Answers(ctx, question.ID)
	if err != nil {
		return QuestionSummary{}, err
	}
	for _, answer := range answers {
		summary.Answers = append(summary.Answers, viewAnswer(answer))
	}
	plans, err := s.plansFor(ctx, question.ID)
	if err != nil {
		return QuestionSummary{}, err
	}
	summary.Plans = plans
	return summary, nil
}

// viewAnswer renders one retained answer. It is shared by the inbox summary
// and the question's own page so the verbatim text an operator typed is
// rendered by one function on both.
func viewAnswer(answer reality.Answer) answerView {
	return answerView{
		ID:         answer.ID,
		QuestionID: answer.QuestionID,
		Sequence:   answer.Sequence,
		Author:     answer.Author,
		At:         timeText(answer.At),
		RecordedAt: timeText(answer.RecordedAt),
		Outcome:    string(answer.Outcome),
		Text:       answer.Payload.Text,
	}
}

// plansFor reads the interpretations a question produced.
//
// This used to recover plan identifiers from the question's append-only state
// history by matching the identifier shape in an event note, because the
// ledger answered Plan(id) and nothing else. It answers Plans(questionID)
// now, and a query cannot drift the way reading a note format can.
func (s *Server) plansFor(ctx context.Context, questionID string) ([]planView, error) {
	plans, err := s.opts.Reality.Plans(ctx, questionID)
	if err != nil {
		return nil, err
	}
	views := make([]planView, 0, len(plans))
	for _, plan := range plans {
		views = append(views, viewPlan(plan))
	}
	return views, nil
}

// questionRow is one question in the ledger's own listing: the record, its
// current state, and how much has accumulated on it.
//
// It carries no score, and the omission is the point. A score is the inbox's
// ranking of what the operator should do next, and §4.8 fixes that ranking for
// the two states only a human can move; a listing of every question the ledger
// ever asked — answered, snoozed, declined — would have to invent a number for
// the rest, and a made-up rank beside a real one is worse than no rank.
type questionRow struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Class           string   `json:"class"`
	State           string   `json:"state"`
	Sensitivity     string   `json:"sensitivity"`
	CreatedAt       string   `json:"created_at"`
	Prompt          string   `json:"prompt"`
	WhyAsked        string   `json:"why_asked"`
	TargetEntityIDs []string `json:"target_entity_ids"`
	// AboutNames names those targets positionally, on QuestionSummary's
	// terms and for its reason.
	AboutNames []string `json:"about_name"`
	Answers    int      `json:"answers"`
	Plans      int      `json:"plans"`
	// Pending is whether the ledger considers this question the operator's
	// to move, which is what the ranked inbox is made of. It is here so a
	// listing row can say "this one is waiting on you" without the page
	// deciding for itself which states those are.
	Pending bool `json:"pending"`
}

// stateCount is how many questions stand in one state. The counts travel as an
// ordered list rather than an object because the order is §4.8's lifecycle,
// and a JSON object's keys have no order at all.
type stateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

type questionsResult struct {
	Items  []questionRow `json:"items"`
	Total  int           `json:"total"`
	States []stateCount  `json:"states"`
}

// handleRealityQuestions serves every question the ledger has asked, newest
// first, with the per-state census beside it.
//
// The inbox route above answers a different question and keeps answering it.
// This one exists because §8.4 requires a stored record to be reachable by
// moving through the interface: a question the operator answered last week
// leaves the inbox by design, and until this route there was no way back to it
// that did not involve typing an identifier nobody has.
//
// The census counts every state and is computed before the filter narrows the
// rows, so a page showing "answered" can still say truthfully how many are
// open.
func (s *Server) handleRealityQuestions(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	query := reality.QuestionQuery{Limit: listScanCap}
	if value := r.URL.Query().Get("class"); value != "" {
		query.Class = reality.QuestionClass(value)
	}
	listed, err := s.opts.Reality.Questions(r.Context(), query)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	census := map[reality.QuestionState]int{}
	rows := make([]questionRow, 0, len(listed))
	state := r.URL.Query().Get("state")
	for _, item := range listed {
		census[item.Question.State]++
		if state != "" && string(item.Question.State) != state {
			continue
		}
		rows = append(rows, viewQuestionRow(item))
	}
	result := questionsResult{Items: []questionRow{}, Total: len(rows)}
	for _, known := range reality.QuestionStates() {
		if count := census[known]; count > 0 {
			result.States = append(result.States, stateCount{State: string(known), Count: count})
		}
	}
	start, end := pg.window(len(rows))
	// Naming is done after the window rather than in viewQuestionRow: a
	// scan of every question the ledger ever asked would otherwise cost an
	// entity read per target on rows nobody is about to see.
	page := rows[start:end]
	for i := range page {
		page[i].AboutNames = s.entityNames(r.Context(), page[i].TargetEntityIDs)
	}
	result.Items = append(result.Items, page...)
	s.writeJSON(w, http.StatusOK, result)
}

// pendingStates are the question states the ledger's own inbox is made of.
// internal/reality decides inbox membership, so this is a rendering of that
// decision rather than a second opinion about it: a row marked pending here is
// a row that appears in Inbox.
func pending(state reality.QuestionState) bool {
	return state == reality.QuestionOpen || state == reality.QuestionPlanReady
}

func viewQuestionRow(item reality.QuestionListing) questionRow {
	question := item.Question
	row := questionRow{
		ID:              question.ID,
		Kind:            string(question.Kind),
		Class:           string(question.Class),
		State:           string(question.State),
		Sensitivity:     string(question.Sensitivity),
		CreatedAt:       timeText(question.CreatedAt),
		Prompt:          question.Payload.Prompt,
		WhyAsked:        question.Payload.WhyAsked,
		TargetEntityIDs: question.TargetEntityIDs,
		Answers:         item.Answers,
		Plans:           item.Plans,
		Pending:         pending(question.State),
	}
	if row.TargetEntityIDs == nil {
		row.TargetEntityIDs = []string{}
	}
	return row
}

// questionEventView is one entry in a question's append-only state history.
type questionEventView struct {
	ID         string `json:"id"`
	Sequence   int    `json:"sequence"`
	State      string `json:"state"`
	Actor      string `json:"actor"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

// questionDetail is one question read whole: the record, what it was asked
// about, the answers it has, the interpretations they produced, the facts that
// prompted it, and every transition it has been through.
//
// The answers and plans are here because §8.4's second requirement is that a
// record's decisions are offered where the record is read. The answer route
// and the plan acceptance are the two decisions a question admits, and this is
// the page they belong beside.
type questionDetail struct {
	Question      questionRow         `json:"question"`
	Targets       []entityRef         `json:"targets"`
	Predicates    []string            `json:"predicates"`
	Evidence      []string            `json:"material_evidence"`
	Answers       []answerView        `json:"answers"`
	Plans         []planView          `json:"plans"`
	History       []questionEventView `json:"history"`
	ExistingFacts []factView          `json:"existing_facts"`
	ConflictFacts []factView          `json:"conflict_facts"`
}

func (s *Server) handleRealityQuestion(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	question, err := s.opts.Reality.Question(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	answers, err := s.opts.Reality.Answers(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	plans, err := s.plansFor(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	history, err := s.opts.Reality.QuestionHistory(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	detail := questionDetail{
		Question: viewQuestionRow(reality.QuestionListing{
			Question: question,
			Answers:  len(answers),
			Plans:    len(plans),
		}),
		Targets:       make([]entityRef, 0, len(question.TargetEntityIDs)),
		Predicates:    make([]string, 0, len(question.TargetPredicates)),
		Evidence:      question.MaterialEvidence,
		Answers:       make([]answerView, 0, len(answers)),
		Plans:         plans,
		History:       make([]questionEventView, 0, len(history)),
		ExistingFacts: []factView{},
		ConflictFacts: []factView{},
	}
	if detail.Evidence == nil {
		detail.Evidence = []string{}
	}
	for _, target := range question.TargetEntityIDs {
		detail.Targets = append(detail.Targets, s.entityRef(ctx, target))
	}
	// The row carries the names too, so a question rendered from the
	// listing and one rendered from its own page say the same thing about
	// what it is about.
	detail.Question.AboutNames = s.entityNames(ctx, detail.Question.TargetEntityIDs)
	for _, predicate := range question.TargetPredicates {
		detail.Predicates = append(detail.Predicates, string(predicate))
	}
	for _, answer := range answers {
		detail.Answers = append(detail.Answers, viewAnswer(answer))
	}
	for _, event := range history {
		detail.History = append(detail.History, questionEventView{
			ID:         event.ID,
			Sequence:   event.Sequence,
			State:      string(event.State),
			Actor:      event.Actor,
			RecordedAt: timeText(event.RecordedAt),
			Note:       event.Payload.Note,
		})
	}
	// The facts a question names are why it was asked: the revision
	// suspected of drift, or the two that contradict each other. A page
	// that showed the prompt without them would be showing the question
	// and hiding its evidence.
	if detail.ExistingFacts, err = s.factsByID(ctx, question.ExistingFactIDs); err != nil {
		s.serviceError(w, r, err)
		return
	}
	if detail.ConflictFacts, err = s.factsByID(ctx, question.ConflictFactIDs); err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.nameFactObjects(ctx, detail.ExistingFacts)
	s.nameFactObjects(ctx, detail.ConflictFacts)
	s.writeJSON(w, http.StatusOK, detail)
}

// factsByID reads the facts a record names. A fact the ledger no longer holds
// is an error rather than a gap: these identifiers are foreign keys the ledger
// enforced when the record was written, so one that does not resolve means the
// file changed underneath rather than that the reference was optional.
func (s *Server) factsByID(ctx context.Context, ids []string) ([]factView, error) {
	views := make([]factView, 0, len(ids))
	for _, id := range ids {
		fact, err := s.opts.Reality.Fact(ctx, id)
		if err != nil {
			return nil, err
		}
		views = append(views, viewFact(fact))
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

// entityRef names an entity a record points at, with the display name that
// makes it readable.
//
// Every surface in this file that mentions an entity mentions it this way — an
// edge's two ends, a question's target, a fact's subject and its object — so a
// page can render "the repository Babel" wherever the ledger stored an
// identifier. The name is best effort: an identifier that no longer resolves
// still reads as an identifier rather than failing the record it appears on.
type entityRef struct {
	ID          string `json:"id"`
	Kind        string `json:"kind,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type relationshipView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	CreatedAt string    `json:"created_at"`
	From      entityRef `json:"from"`
	To        entityRef `json:"to"`
	Note      string    `json:"note,omitempty"`
}

// resolutionView is one merge, split, or reversal in an identity's history.
// §8.2 names alias merge/split history as part of what Reality shows, and this
// is the record that holds it: an identity folded into another, or one pulled
// back apart, with the operator who decided and the reasoning they gave.
type resolutionView struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	Actor      string      `json:"actor"`
	RecordedAt string      `json:"recorded_at"`
	ReversesID string      `json:"reverses_id,omitempty"`
	Sources    []entityRef `json:"sources"`
	Results    []entityRef `json:"results"`
	Reason     string      `json:"reason,omitempty"`
}

type factValueView struct {
	Kind     string `json:"kind"`
	Enum     string `json:"enum,omitempty"`
	Text     string `json:"text,omitempty"`
	ObjectID string `json:"object_id,omitempty"`
	// ObjectName is what the reader calls the object, filled in by the
	// handler rather than by viewFact: the ledger stores the identifier
	// and the name is a second read, so the renderer stays a pure
	// projection of one record.
	ObjectName string `json:"object_name,omitempty"`
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

// entityRow is one subject in the ledger's census: the identity, and the size
// of what the ledger holds about it.
type entityRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Role        string `json:"role"`
	CanonicalID string `json:"canonical_id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
	Aliases     int    `json:"aliases"`
	Facts       int    `json:"facts"`
	Active      int    `json:"active_facts"`
	LatestFact  string `json:"latest_fact,omitempty"`
}

// kindCount is how many entities the ledger holds of one kind, in §4.8's own
// order rather than an object's key order, for stateCount's reason.
type kindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type entitiesResult struct {
	Items []entityRow `json:"items"`
	Total int         `json:"total"`
	Kinds []kindCount `json:"kinds"`
}

// handleRealityEntities serves the ledger's subjects, newest first.
//
// Until this route the only way to an entity was its identifier, which meant
// the ledger's own contents were reachable from a focus rule, from an edge on
// another entity, or from a URL the operator had memorized — and from nothing
// else. §8.4 calls that not being in the product. The census by kind travels
// with the listing so a filtered page can still say what else is there.
func (s *Server) handleRealityEntities(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	query := reality.EntityQuery{Limit: listScanCap}
	if value := r.URL.Query().Get("kind"); value != "" {
		query.Kind = reality.EntityKind(value)
	}
	listed, err := s.opts.Reality.Entities(r.Context(), query)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The census is of the whole ledger rather than of the filtered page,
	// so the kind a reader is not looking at is still countable. It
	// therefore costs a second read when a filter is in force, which is
	// the honest price of the filter being able to offer anything else.
	census := listed
	if query.Kind != "" {
		if census, err = s.opts.Reality.Entities(r.Context(), reality.EntityQuery{Limit: listScanCap}); err != nil {
			s.serviceError(w, r, err)
			return
		}
	}
	counts := map[reality.EntityKind]int{}
	for _, item := range census {
		counts[item.Entity.Kind]++
	}
	result := entitiesResult{Items: []entityRow{}, Total: len(listed)}
	for _, known := range reality.EntityKinds() {
		if count := counts[known]; count > 0 {
			result.Kinds = append(result.Kinds, kindCount{Kind: string(known), Count: count})
		}
	}
	start, end := pg.window(len(listed))
	for _, item := range listed[start:end] {
		result.Items = append(result.Items, entityRow{
			ID:          item.Entity.ID,
			Kind:        string(item.Entity.Kind),
			Role:        string(item.Entity.Role),
			CanonicalID: item.Entity.CanonicalID,
			DisplayName: item.Entity.Payload.DisplayName,
			CreatedAt:   timeText(item.Entity.CreatedAt),
			Aliases:     item.Aliases,
			Facts:       item.Facts,
			Active:      item.Active,
			LatestFact:  timeText(item.LatestFact),
		})
	}
	s.writeJSON(w, http.StatusOK, result)
}

// candidateRow is one frontier candidate that was scoped to a subject, named
// by what it says rather than by its identifier.
//
// It is the only link there is between a subject and the analysis that
// concerns it: a run resolves a hypothesis to the entities it is about and
// records that resolution in a context snapshot, and nothing else in Babel
// writes down which subject a record is about. Findings and proposals reach a
// subject only through the candidates they develop, which is what the record
// page's own related section already walks — so this stops at the candidate
// and lets the record page continue from there rather than guessing at a
// transitive set here.
type candidateRow struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type entityDetail struct {
	Entity        entityView         `json:"entity"`
	Aliases       []aliasView        `json:"aliases"`
	Relationships []relationshipView `json:"relationships"`
	Facts         []factView         `json:"facts"`
	Resolutions   []resolutionView   `json:"resolutions"`
	// Candidates are the frontier records scoped to this subject, newest
	// first, capped. Absent rather than empty when the frontier is not
	// configured, so a page can tell "nothing was scoped here" from "this
	// deployment cannot answer that".
	Candidates []candidateRow `json:"candidates"`
}

// candidateCap bounds the candidate list a subject's page carries. A subject
// Babel has been exploring for months has more candidates than anyone reads
// in one sitting, and the ones worth reading are the recent ones; the whole
// set is the Read surface's job.
const candidateCap = 40

// handleRealityEntity serves one entity's current reality: its identity, the
// names it is known by, its edges, its facts, and the merges and splits it has
// been through.
//
// Every fact status is included, superseded revisions and proposals alike,
// because reviewing what was proposed is a real need and a revision chain that
// showed only its head would hide how reality was corrected. The resolution
// history is here for the same reason one step up: it is how the operator sees
// that two names were judged one thing, who judged it, and that the judgement
// can be reversed.
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
	resolutions, err := s.opts.Reality.ResolutionHistory(ctx, id)
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
		Resolutions:   make([]resolutionView, 0, len(resolutions)),
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
			From:      s.entityRef(ctx, relationship.FromID),
			To:        s.entityRef(ctx, relationship.ToID),
			Note:      relationship.Payload.Note,
		})
	}
	for _, fact := range facts {
		detail.Facts = append(detail.Facts, viewFact(fact))
	}
	s.nameFactObjects(ctx, detail.Facts)
	for _, resolution := range resolutions {
		view := resolutionView{
			ID:         resolution.ID,
			Kind:       string(resolution.Kind),
			Actor:      resolution.Actor,
			RecordedAt: timeText(resolution.RecordedAt),
			ReversesID: resolution.ReversesID,
			Sources:    make([]entityRef, 0, len(resolution.SourceIDs)),
			Results:    make([]entityRef, 0, len(resolution.ResultIDs)),
			Reason:     resolution.Payload.Reason,
		}
		for _, source := range resolution.SourceIDs {
			view.Sources = append(view.Sources, s.entityRef(ctx, source))
		}
		for _, result := range resolution.ResultIDs {
			view.Results = append(view.Results, s.entityRef(ctx, result))
		}
		detail.Resolutions = append(detail.Resolutions, view)
	}
	if detail.Candidates, err = s.candidatesFor(ctx, id); err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// candidatesFor reads the frontier candidates a subject was scoped to.
//
// A candidate the ledger names and the frontier no longer holds is skipped
// rather than failed: the two are separate durable components and a snapshot
// is append-only, so a hypothesis that was revised away leaves a snapshot
// pointing at a record this store will not answer for. That is the ledger
// working, and it must not take the subject's page down with it.
func (s *Server) candidatesFor(ctx context.Context, entityID string) ([]candidateRow, error) {
	if s.opts.Frontier == nil {
		return nil, nil
	}
	ids, err := s.opts.Reality.HypothesesForEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	rows := make([]candidateRow, 0, min(len(ids), candidateCap))
	for _, id := range ids {
		if len(rows) == candidateCap {
			break
		}
		record, err := s.opts.Frontier.Hypothesis(ctx, id)
		if err != nil {
			continue
		}
		rows = append(rows, candidateRow{
			ID:        record.ID,
			Statement: record.Payload.Statement,
			Status:    string(record.Status),
			CreatedAt: timeText(record.CreatedAt),
		})
	}
	return rows, nil
}

// viewFact renders one immutable revision whole, every field the ledger
// stores and nothing derived. It is shared with the focus surface, which names
// the fact a restriction derives from: two renderers over one record would
// eventually disagree about which timestamp "asserted" means.
func viewFact(fact reality.Fact) factView {
	return factView{
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
	}
}

// entityRef resolves an identifier to the name a reader knows it by, best
// effort. An identity the ledger no longer holds keeps its identifier and
// loses only its name: a record must not become unreadable because something
// it points at went missing.
func (s *Server) entityRef(ctx context.Context, id string) entityRef {
	if id == "" {
		return entityRef{}
	}
	ref := entityRef{ID: id}
	if entity, err := s.opts.Reality.Entity(ctx, id); err == nil {
		ref.Kind = string(entity.Kind)
		ref.DisplayName = entity.Payload.DisplayName
	}
	return ref
}

// entityNames names a list of identifiers positionally, for the surfaces that
// already ship the identifiers and cannot change their shape.
//
// A name the ledger cannot produce comes back as the empty string rather than
// as the identifier repeated, so a page can tell "this is called X" from
// "this has no name left" and print the identifier itself only where that is
// the whole truth. Repeats are looked up once: a question with the same
// target twice, or a page of questions about one subject, is the common case
// and each lookup is a row read.
func (s *Server) entityNames(ctx context.Context, ids []string) []string {
	names := make([]string, len(ids))
	if len(ids) == 0 {
		return names
	}
	seen := make(map[string]string, len(ids))
	for i, id := range ids {
		name, known := seen[id]
		if !known {
			if entity, err := s.opts.Reality.Entity(ctx, id); err == nil {
				name = entity.Payload.DisplayName
			}
			seen[id] = name
		}
		names[i] = name
	}
	return names
}

// nameFactObjects fills in the display name of every entity-valued fact in a
// list. An entity-valued fact asserts "contains the repository Babel", and a
// surface that printed its object identifier was asking the reader to go and
// look up the claim it had just shown them.
func (s *Server) nameFactObjects(ctx context.Context, facts []factView) {
	named := map[string]string{}
	for i := range facts {
		id := facts[i].Value.ObjectID
		if id == "" {
			continue
		}
		name, known := named[id]
		if !known {
			name = s.entityRef(ctx, id).DisplayName
			named[id] = name
		}
		facts[i].Value.ObjectName = name
	}
}

// nameFactObject is the same for one revision reached through a pointer,
// which is how a chain's two neighbours travel.
func (s *Server) nameFactObject(ctx context.Context, fact *factView) {
	if fact == nil || fact.Value.ObjectID == "" {
		return
	}
	fact.Value.ObjectName = s.entityRef(ctx, fact.Value.ObjectID).DisplayName
}

// factRow is one revision in a listing, with the subject it is about named
// rather than merely identified.
type factRow struct {
	Fact    factView  `json:"fact"`
	Subject entityRef `json:"subject"`
}

// statusCount is how many of the listed revisions hold one status.
type statusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type factsResult struct {
	Items    []factRow     `json:"items"`
	Total    int           `json:"total"`
	Statuses []statusCount `json:"statuses"`
}

// factListCap bounds the newest-first window the fact listing reads.
//
// It is a window rather than a page over the whole ledger, and the number says
// so: the ledger's Facts query answers about a subject because that is what
// analysis asks, and this listing exists for the reader who does not know
// which subject to ask about yet. Once he does, the entity's own page holds
// every revision about it.
const factListCap = 200

// handleRealityFacts serves what Babel has most recently concluded, whatever
// it is about.
//
// This is the answer to "what does Babel believe" for a reader who has not
// picked a subject, and it is the one listing on this surface that is
// deliberately not exhaustive. A fact's whole context — its subject's other
// facts, its revision chain — is one click away on the two pages this one
// leads to.
func (s *Server) handleRealityFacts(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	facts, err := s.opts.Reality.RecentFacts(r.Context(), factListCap)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	counts := map[reality.FactStatus]int{}
	shown := make([]reality.Fact, 0, len(facts))
	for _, fact := range facts {
		counts[fact.Status]++
		if status != "" && string(fact.Status) != status {
			continue
		}
		shown = append(shown, fact)
	}
	result := factsResult{Items: []factRow{}, Total: len(shown)}
	for _, known := range reality.FactStatuses() {
		if count := counts[known]; count > 0 {
			result.Statuses = append(result.Statuses, statusCount{Status: string(known), Count: count})
		}
	}
	start, end := pg.window(len(shown))
	// Every entity a row mentions — the subject it is about and the object
	// an entity-valued fact points at — is resolved once: a listing of
	// twenty revisions about one entity is one read, not twenty.
	subjects := map[string]entityRef{}
	for _, fact := range shown[start:end] {
		ref, known := subjects[fact.SubjectID]
		if !known {
			ref = s.entityRef(r.Context(), fact.SubjectID)
			subjects[fact.SubjectID] = ref
		}
		view := viewFact(fact)
		if view.Value.ObjectID != "" {
			object, seen := subjects[view.Value.ObjectID]
			if !seen {
				object = s.entityRef(r.Context(), view.Value.ObjectID)
				subjects[view.Value.ObjectID] = object
			}
			view.Value.ObjectName = object.DisplayName
		}
		result.Items = append(result.Items, factRow{Fact: view, Subject: ref})
	}
	s.writeJSON(w, http.StatusOK, result)
}

// factStatusEventView is one entry in a fact's append-only status history.
type factStatusEventView struct {
	ID         string `json:"id"`
	Sequence   int    `json:"sequence"`
	Status     string `json:"status"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

// disputeView is one recorded contradiction a fact is party to.
type disputeView struct {
	ID        string   `json:"id"`
	SubjectID string   `json:"subject_id"`
	Predicate string   `json:"predicate"`
	CreatedAt string   `json:"created_at"`
	State     string   `json:"state"`
	FactIDs   []string `json:"fact_ids"`
	Reason    string   `json:"reason,omitempty"`
}

// factDetail is one revision read whole, with the chain it sits in.
//
// The chain is the point of the page. §4.8 has no update path — a correction
// is a new revision whose ancestor keeps its bytes — and that only means
// anything if a reader can see both ends of it: what this revision replaced,
// and what replaced it. The status history beside it is what proves expiry
// marked rather than deleted, and the disputes are what proves a contradiction
// was recorded rather than resolved by whoever wrote last.
type factDetail struct {
	Fact         factView              `json:"fact"`
	Subject      entityRef             `json:"subject"`
	Object       *entityRef            `json:"object,omitempty"`
	Supersedes   *factView             `json:"supersedes,omitempty"`
	SupersededBy *factView             `json:"superseded_by,omitempty"`
	History      []factStatusEventView `json:"history"`
	Disputes     []disputeView         `json:"disputes"`
}

func (s *Server) handleRealityFact(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reality != nil, "the reality ledger") {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	ctx := r.Context()
	fact, err := s.opts.Reality.Fact(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	history, err := s.opts.Reality.FactStatusHistory(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	disputes, err := s.opts.Reality.DisputesFor(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	detail := factDetail{
		Fact:     viewFact(fact),
		Subject:  s.entityRef(ctx, fact.SubjectID),
		History:  make([]factStatusEventView, 0, len(history)),
		Disputes: make([]disputeView, 0, len(disputes)),
	}
	if fact.Value.ObjectID != "" {
		object := s.entityRef(ctx, fact.Value.ObjectID)
		detail.Object = &object
		detail.Fact.Value.ObjectName = object.DisplayName
	}
	if fact.Supersedes != "" {
		prior, err := s.opts.Reality.Fact(ctx, fact.Supersedes)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		view := viewFact(prior)
		detail.Supersedes = &view
	}
	// The forward half of the chain is found rather than stored: a fact
	// names what it replaced, and the ledger's unique index on that column
	// is what makes the successor at most one. Reading the subject's facts
	// is how it is located, which also means a successor asserted about an
	// identity since merged into this subject is still found.
	siblings, err := s.opts.Reality.Facts(ctx, reality.FactQuery{SubjectID: fact.SubjectID})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	for _, sibling := range siblings {
		if sibling.Supersedes == fact.ID {
			view := viewFact(sibling)
			detail.SupersededBy = &view
			break
		}
	}
	s.nameFactObject(ctx, detail.Supersedes)
	s.nameFactObject(ctx, detail.SupersededBy)
	for _, event := range history {
		detail.History = append(detail.History, factStatusEventView{
			ID:         event.ID,
			Sequence:   event.Sequence,
			Status:     string(event.Status),
			RecordedAt: timeText(event.RecordedAt),
			Note:       event.Payload.Note,
		})
	}
	for _, dispute := range disputes {
		detail.Disputes = append(detail.Disputes, disputeView{
			ID:        dispute.ID,
			SubjectID: dispute.SubjectID,
			Predicate: string(dispute.Predicate),
			CreatedAt: timeText(dispute.CreatedAt),
			State:     string(dispute.State),
			FactIDs:   dispute.FactIDs,
			Reason:    dispute.Payload.Reason,
		})
	}
	s.writeJSON(w, http.StatusOK, detail)
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
