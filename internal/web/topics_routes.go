package web

// The operator's one direct act on a topic itself (§4.13): what he thinks of
// it.
//
// A topic is a Reality Ledger entity and nothing else, so the route here is
// not a topic feature — it is a §4.8 act performed from the page where the
// operator was already looking at the thing. Stating interest writes the
// lifecycle and analysis-policy facts §4.13 spells as *working on it*, *keep
// an eye*, *not now* and *excluded*. Nothing here deletes anything, because
// none of the ledger's writers can.
//
// Retiring, merging and splitting used to live here and deliberately do not
// any more (operator direction 2026-09-12, second reading). Everything about
// a topic goes through Babel: those three are one output kind — a topic
// proposal a run publishes, reviewed and ruled on like every other proposal —
// and the ruling is what applies them. A button that retired a topic directly
// would be a change Babel did not see, cannot explain and cannot learn from,
// which is exactly what the section refuses.
//
// Interest is the exception the section states: *interest is a fact about the
// world, not a preference knob*, and it is the operator's own stance rather
// than anything Babel proposed. It carries no required reason, and that is
// deliberate rather than an omission — a stance is often the whole statement,
// and refusing the act for want of prose would lose the stance to gain
// nothing.
//
// The authority is the session's operator, resolved by the same
// requireOperator every other §4.7 and §4.8 mutation on this surface uses. A
// session that cannot name an operator writes nothing at all: §4.8 accepts an
// attributed operator act as authority itself, and an unattributable one is
// not one.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/atyrode/babel/internal/reality"
)

// topicsPathPrefix carries the selector the one act under it needs
// (proposalPathPrefix and recordPathPrefix are the others).
const topicsPathPrefix = "/api/topics/"

// routeTopics resolves §4.13's one act on a topic's identity.
//
// It answers only the path it owns and reports every other path under the
// prefix as unhandled, so the bare /api/topics listing keeps its exact-match
// route. A router that swallowed the prefix would make this file the
// gatekeeper of paths it does not implement — and the paths it used to
// implement are gone: a retirement, a merge and a split are rulings on a
// published proposal now, and this router answers them with the same
// unhandled it gives any other unknown path.
func (s *Server) routeTopics(w http.ResponseWriter, r *http.Request) bool {
	rest, found := strings.CutPrefix(r.URL.Path, topicsPathPrefix)
	if !found || rest == "" {
		return false
	}
	id, action, cut := strings.Cut(rest, "/")
	if !cut || id == "" {
		return false
	}
	if action == "interest" {
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleTopicInterest(w, r, id)
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

// topicActResult is the act's answer: the stance now in force, read back from
// the ledger rather than echoed from the request.
//
// It carries no topic, no post count and no binding, and that is honest
// rather than thin: this route wrote one fact and read the consequence back,
// while the counts belong to the pass over the corpus that GET /api/topics
// makes. A response that carried a stale count would be reporting something
// this request did not observe.
type topicActResult struct {
	Interest *topicStanceView `json:"interest,omitempty"`
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
