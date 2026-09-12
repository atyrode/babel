package web

// The operator's acts on a topic itself (§4.13): what he thinks of it, and
// what its identity turns out to be.
//
// A topic is a Reality Ledger entity and nothing else, so none of the four
// routes here is a topic feature — each is a §4.8 act performed from the page
// where the operator was already looking at the thing. Stating interest writes
// the lifecycle and analysis-policy facts §4.13 spells as *working on it*,
// *keep an eye*, *not now* and *excluded*; retiring writes a lifecycle fact
// that says the name should never have existed; merging and splitting append
// the resolution history §4.8 keeps reversible. Nothing here deletes anything,
// because none of the ledger's writers can.
//
// Every one of them carries a reason except stating interest, and the
// exception is deliberate rather than an omission. §4.13 has the triage recipe
// read *why topics were retired, split and declined* as evidence for its next
// proposals, so a retirement, a merge and a split with no reason would be
// exactly the fact that teaches it nothing; a stance, on the other hand, is
// often the whole statement — an operator who says "not now" has said
// something attributable — and refusing the act for want of prose would lose
// the stance to gain nothing.
//
// The authority is the session's operator, resolved by the same
// requireOperator every other §4.7 and §4.8 mutation on this surface uses. A
// session that cannot name an operator writes nothing at all: §4.8 accepts an
// attributed operator act as authority itself, and an unattributable one is
// not one.
//
// The filing half of §4.13 is not here. Filing a record, unfiling it and the
// topic questions a run raises are internal/web/feed.go's, because they are
// acts on a *record* or on a proposal rather than on the topic's identity —
// with one exception this file does own: a split names the records that belong
// to the new part, and moving them is what makes the split mean anything, so
// it re-files them in the same request through the frontier's own filer.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// topicsPathPrefix is the third path in this router that carries a selector
// (proposalPathPrefix and recordPathPrefix are the others), and the two
// collection-level acts under it — merging and splitting — deliberately have
// no identifier, because they are about two topics and about a topic's parts
// rather than about one topic.
const topicsPathPrefix = "/api/topics/"

// routeTopics resolves §4.13's acts on a topic's identity.
//
// It answers only the four paths it owns and reports every other path under
// the prefix as unhandled, so the topic *questions* an operator accepts or
// declines stay with the surface that renders them and the bare /api/topics
// listing keeps its exact-match route. A router that swallowed the prefix
// would make this file the gatekeeper of paths it does not implement.
func (s *Server) routeTopics(w http.ResponseWriter, r *http.Request) bool {
	rest, found := strings.CutPrefix(r.URL.Path, topicsPathPrefix)
	if !found || rest == "" {
		return false
	}
	switch rest {
	case "merge":
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleTopicMerge(w, r)
		}
		return true
	case "split":
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleTopicSplit(w, r)
		}
		return true
	}
	id, action, cut := strings.Cut(rest, "/")
	if !cut || id == "" {
		return false
	}
	switch action {
	case "interest":
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleTopicInterest(w, r, id)
		}
		return true
	case "retire":
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleTopicRetire(w, r, id)
		}
		return true
	}
	return false
}

// topicStanceView is one recorded stance as a page reads it: the word, the
// operator's own reason, and the attribution of the fact that says it.
//
// An empty state is a state. It means nobody has said anything about this
// topic, which is different from every one of the four words and must not be
// rendered as one of them — so the fields are always present and the reader is
// left with nothing to infer.
type topicStanceView struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	At     string `json:"at"`
	By     string `json:"by"`
}

func viewStance(interest reality.Interest) topicStanceView {
	return topicStanceView{
		State:  interest.State,
		Reason: interest.Reason,
		At:     timeText(interest.At),
		By:     interest.By,
	}
}

// topicSubjectView is what an act on a topic answers with: the entity it
// touched and the stance now recorded about it.
//
// It carries no post count and no binding, and that is honest rather than
// thin: this route wrote a fact or a resolution and read the consequence back,
// while the counts belong to a pass over the corpus that GET /api/topics
// makes. A response that carried a stale count would be reporting something
// this request did not observe.
type topicSubjectView struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Kind     string          `json:"kind"`
	Interest topicStanceView `json:"interest"`
}

// topicActResult is every act's answer. The interest route answers with the
// stance alone, which is why the topic is omitted when it is absent rather
// than sent empty.
type topicActResult struct {
	Topic    *topicSubjectView `json:"topic,omitempty"`
	Interest *topicStanceView  `json:"interest,omitempty"`
}

// topicInterestRequest is POST /api/topics/{id}/interest's body.
type topicInterestRequest struct {
	// State is one of §4.13's four words. It is checked here against the
	// ledger's own vocabulary rather than by the store alone, because the
	// refusal an operator reads should name the four rather than report an
	// invalid value.
	State string `json:"state"`
	// Reason is the operator's own words, kept verbatim in the fact.
	Reason string `json:"reason"`
}

// handleTopicInterest records the operator's stance toward one topic.
//
// The stance is read back from the ledger rather than echoed from the request,
// on writeFocusResult's terms: what the page shows afterwards has to be what
// the ledger now says, and a handler that reported its own input could show a
// stance a concurrent write had already replaced.
func (s *Server) handleTopicInterest(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireService(w, s.opts.Topics != nil, "the reality ledger") {
		return
	}
	var request topicInterestRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if !topicStateIsKnown(request.State) {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"interest is one of %s; %q is not one of them",
			joinWords(reality.InterestStates()), request.State))
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	topic, ok := s.requireTopic(w, r, id)
	if !ok {
		return
	}
	if err := s.opts.Topics.SetInterest(r.Context(), topic.ID, by.ID(),
		request.State, request.Reason); err != nil {
		s.serviceError(w, r, err)
		return
	}
	stance, ok := s.readStance(w, r, topic.ID)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, topicActResult{Interest: &stance})
}

func topicStateIsKnown(state string) bool {
	for _, known := range reality.InterestStates() {
		if state == known {
			return true
		}
	}
	return false
}

// topicReasonRequest is the body of the three acts that state why.
type topicReasonRequest struct {
	Reason string `json:"reason"`
}

// handleTopicRetire records that a topic should never have existed.
//
// It re-files nothing and deletes nothing. The entity keeps its identity, its
// aliases and its facts, every filing under it keeps its bytes, and what
// changes is what reads of the ledger conclude: §4.13 has those filings return
// to the triage backlog, which is a property of how the backlog is computed
// rather than of anything this request rewrote.
func (s *Server) handleTopicRetire(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireService(w, s.opts.Topics != nil, "the reality ledger") {
		return
	}
	var request topicReasonRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.Reason == "" {
		s.writeError(w, http.StatusBadRequest,
			"retiring a topic states why it should never have existed; reason is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	topic, ok := s.requireTopic(w, r, id)
	if !ok {
		return
	}
	if err := s.opts.Topics.RetireEntity(r.Context(), topic.ID, by.ID(), request.Reason); err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeTopic(w, r, topic)
}

// topicMergeRequest is POST /api/topics/merge's body: the identity that turns
// out to be the other one, and why the operator says so.
type topicMergeRequest struct {
	From   string `json:"from"`
	Into   string `json:"into"`
	Reason string `json:"reason"`
}

// handleTopicMerge folds one topic into another.
//
// The filings follow without being rewritten. An `about` edge names an entity
// id, and every consumer resolves that id through the merge history, so the
// records filed under the folded identity are the target's afterwards because
// the ledger says the two are one thing — not because a pass over the frontier
// edited a column.
//
// Two refusals are this handler's rather than the store's, and both exist so
// that the operator is told what he is looking at instead of being handed a
// generic conflict: a topic the ledger does not hold, and two topics of
// different kinds. §4.8 refuses a cross-kind merge deterministically because a
// repository folded into a machine is a resolution mistake rather than a
// judgement call, and the sentence that says so can name both kinds.
func (s *Server) handleTopicMerge(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Topics != nil, "the reality ledger") {
		return
	}
	var request topicMergeRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.From == "" || request.Into == "" {
		s.writeError(w, http.StatusBadRequest, "a merge names the topic to fold and the topic to keep")
		return
	}
	if request.Reason == "" {
		s.writeError(w, http.StatusBadRequest,
			"merging two topics states why they are one thing; reason is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	from, ok := s.requireTopic(w, r, request.From)
	if !ok {
		return
	}
	into, ok := s.requireTopic(w, r, request.Into)
	if !ok {
		return
	}
	if from.ID == into.ID {
		s.writeError(w, http.StatusBadRequest, "these two names are already the same topic")
		return
	}
	if from.Kind != into.Kind {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"%q is a %s and %q is a %s; merging them would record one identity for two kinds of thing",
			from.Payload.DisplayName, from.Kind, into.Payload.DisplayName, into.Kind))
		return
	}
	if _, err := s.opts.Topics.MergeEntities(r.Context(), reality.MergeInput{
		SourceIDs: []string{from.ID},
		TargetID:  into.ID,
		Actor:     by.ID(),
		Reason:    request.Reason,
	}); err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeTopic(w, r, into)
}

// topicSplitRequest is POST /api/topics/split's body: the topic that names two
// things, the second thing, the records that belong to it, and why.
type topicSplitRequest struct {
	Entity  string   `json:"entity"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Records []string `json:"records"`
	Reason  string   `json:"reason"`
}

// handleTopicSplit says that one topic covered two subjects, and moves the
// records that belong to the second.
//
// §4.8's split creates the parts rather than carving one out, so the parent is
// replaced by two: the remainder, which keeps the parent's own name and kind
// because that is what is left when the new thing is taken out of it, and the
// new topic the operator named. The parent keeps its facts and its history —
// they were asserted about the identity as it was then understood, and
// reattributing them would rewrite history — and it stops speaking for itself,
// which is what a reader needs in order to know to look at the parts.
//
// The named records are re-filed under the new part on the operator's
// authority in the same request, and that is the whole point of naming them: a
// split whose records stayed where they were would have produced an empty
// topic and left the evidence for it under the name the operator has just said
// was wrong. The filings are the operator's own act rather than a heuristic,
// because he named these records individually.
func (s *Server) handleTopicSplit(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Topics != nil, "the reality ledger") {
		return
	}
	var request topicSplitRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.Entity == "" || request.Name == "" {
		s.writeError(w, http.StatusBadRequest, "a split names the topic to divide and the new topic")
		return
	}
	if request.Reason == "" {
		s.writeError(w, http.StatusBadRequest,
			"splitting a topic states what the two things are; reason is required")
		return
	}
	kind, ok := s.requireEntityKind(w, request.Kind)
	if !ok {
		return
	}
	records, ok := s.requireRecordRefs(w, request.Records)
	if !ok {
		return
	}
	// The filer is required only when there are records to move. A split
	// that names none is a lawful act — the operator has said the name
	// covers two things and will file the evidence afterwards — and a
	// deployment holding a ledger and no frontier can perform it.
	if len(records) > 0 && !s.requireService(w, s.opts.TopicFiler != nil, "the frontier's filings") {
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	parent, ok := s.requireTopic(w, r, request.Entity)
	if !ok {
		return
	}
	_, parts, err := s.opts.Topics.SplitEntity(r.Context(), reality.SplitInput{
		ParentID: parent.ID,
		Parts: []reality.EntityInput{
			{Kind: parent.Kind, Payload: reality.EntityPayload{
				DisplayName: parent.Payload.DisplayName,
				Notes:       parent.Payload.Notes,
			}},
			{Kind: kind, Payload: reality.EntityPayload{DisplayName: request.Name}},
		},
		Actor:  by.ID(),
		Reason: request.Reason,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The parts come back in the order they were asked for, so the new
	// topic is the second. Reading it by name would be looking up something
	// this call already knows.
	created := parts[len(parts)-1]
	for _, record := range records {
		if _, err := s.opts.TopicFiler.File(r.Context(), frontier.FilingInput{
			Record:   record,
			EntityID: created.ID,
			Rationale: fmt.Sprintf("split out of %s: %s",
				parent.Payload.DisplayName, request.Reason),
			Author:   frontier.FilingOperator,
			AuthorID: by.ID(),
		}); err != nil {
			s.serviceError(w, r, err)
			return
		}
	}
	s.writeTopicStatus(w, r, created, http.StatusCreated)
}

// requireRecordRefs resolves the records a split moves before anything is
// written.
//
// Every identifier is resolved first for handleSubjectCreate's reason: a typo
// in the third record must not leave a topic created and two records filed
// under it, and an append-only log cannot be corrected afterwards. The kind
// comes from the identifier's own family prefix, which is what every other
// record route on this surface reads it from.
func (s *Server) requireRecordRefs(w http.ResponseWriter, ids []string) ([]frontier.Ref, bool) {
	refs := make([]frontier.Ref, 0, len(ids))
	for _, id := range ids {
		kind, ok := kindOfRecordID(id)
		if !ok {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"%q does not name a record this deployment can file", id))
			return nil, false
		}
		refs = append(refs, frontier.Ref{Type: kind, ID: id})
	}
	return refs, true
}

// requireTopic resolves the topic an act names, through the merge history.
//
// Resolving is what makes a stance stated about a folded identity land on the
// entity that now speaks for it, which is the same rule every other consumer
// of an entity id follows. A word the ledger does not hold is a 404 naming the
// identifier, because the operator's next action is to look it up: an entity
// id is opaque and travels in the clear under §9.1, so naming it in the
// refusal reveals nothing the request did not already carry.
func (s *Server) requireTopic(w http.ResponseWriter, r *http.Request, id string) (reality.Entity, bool) {
	canonical, err := s.opts.Topics.Resolve(r.Context(), id)
	if errors.Is(err, reality.ErrUnknownRecord) {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("no topic in this ledger has the id %q", id))
		return reality.Entity{}, false
	}
	if err != nil {
		s.serviceError(w, r, err)
		return reality.Entity{}, false
	}
	entity, err := s.opts.Topics.Entity(r.Context(), canonical)
	if err != nil {
		s.serviceError(w, r, err)
		return reality.Entity{}, false
	}
	return entity, true
}

// readStance reads the stance now in force, which is what every act answers
// with.
func (s *Server) readStance(w http.ResponseWriter, r *http.Request, entityID string) (topicStanceView, bool) {
	interest, err := s.opts.Topics.EntityInterest(r.Context(), entityID)
	if err != nil {
		s.serviceError(w, r, err)
		return topicStanceView{}, false
	}
	return viewStance(interest), true
}

func (s *Server) writeTopic(w http.ResponseWriter, r *http.Request, entity reality.Entity) {
	s.writeTopicStatus(w, r, entity, http.StatusOK)
}

func (s *Server) writeTopicStatus(w http.ResponseWriter, r *http.Request,
	entity reality.Entity, status int) {
	stance, ok := s.readStance(w, r, entity.ID)
	if !ok {
		return
	}
	s.writeJSON(w, status, topicActResult{Topic: &topicSubjectView{
		ID:       entity.ID,
		Name:     entity.Payload.DisplayName,
		Kind:     string(entity.Kind),
		Interest: stance,
	}})
}
