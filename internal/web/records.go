package web

// Issue #87's record actions: the append-only revision chain a record belongs
// to, the proposed next actions attached to it, and the three things an
// operator may do from a record page — authorize or decline a proposed action,
// invite the loop to process the record further, and return a resting candidate
// to the frontier.
//
// These are the browser's first writes against the frontier's own state, and
// three rules shape all of them.
//
// Accepting authorizes; it does not publish. An accepted draft-issue opens no
// issue, an accepted propose-reality-fact writes no fact, and internal/
// disposition holds no credential and no network path. What a mutation here
// records is that a person authorized an action, which is why the acceptance
// response says so in a field rather than in a comment: a client reading this
// document is exactly the reader who might otherwise assume something was
// filed.
//
// An invitation carries no instruction. #87 makes "process further" a nudge —
// refine, question, amend, or abandon is the next run's judgement — so the
// request body has no field an instruction could be written into, and adding
// one later would change what an invitation means rather than make it more
// convenient.
//
// Nothing is decided about a wording the operator did not read. Every mutation
// names the chain head the page was rendered against, and a head that has moved
// since is a 409 rather than a write. This is the one rule this file adds that
// the services do not have, and it belongs here because it is a fact about the
// page rather than about the store: internal/frontier cannot tell that the text
// under the button changed after the button was drawn, and an authorization
// attributed to an operator who was shown different words is exactly the kind
// of dishonest record #87's chain exists to prevent.

import (
	"errors"
	"net/http"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
)

// actorView is who authored a durable change: a run identified by its receipt,
// or an operator identified the way §4.7's reviewer identity is. The two are
// rendered as a pair rather than as one string because the distinction is the
// whole point of #87's attribution — a reader has to be able to tell inference
// from a person — and a renderer that flattened them would have to parse the
// distinction back out.
type actorView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func viewActor(actor frontier.Actor) actorView {
	return actorView{Kind: string(actor.Kind), ID: actor.ID}
}

// revisionView is one entry of a record's chain.
type revisionView struct {
	ID           string    `json:"id"`
	Record       refView   `json:"record"`
	RootID       string    `json:"root_id"`
	SupersedesID string    `json:"supersedes_id,omitempty"`
	Sequence     int64     `json:"sequence"`
	Actor        actorView `json:"actor"`
	RecordedAt   string    `json:"recorded_at"`
	// Reason is why this revision replaced the one before it, absent on a
	// chain's first entry because an original record supersedes nothing and
	// has no reason to give.
	Reason string `json:"reason,omitempty"`
	// Head marks the chain's current state, so a reader does not have to
	// know that the last element is special.
	Head bool `json:"head"`
}

func viewRevision(revision frontier.Revision, head bool) revisionView {
	return revisionView{
		ID:           revision.ID,
		Record:       viewRef(revision.Entity),
		RootID:       revision.RootID,
		SupersedesID: revision.SupersedesID,
		Sequence:     revision.Sequence,
		Actor:        viewActor(revision.Actor),
		RecordedAt:   timeText(revision.RecordedAt),
		Reason:       revision.Payload.Reason,
		Head:         head,
	}
}

// revisionChain is GET /api/record/revisions' response.
type revisionChain struct {
	// Record is the revision the caller asked about, which is not
	// necessarily the head: a chain reads the same from anywhere in it, and
	// an operator following a link to a superseded wording is entitled to
	// see the history that replaced it.
	Record refView `json:"record"`
	// HeadID is the record that is the chain's current state. It is the
	// value every mutation from this page sends back, so it travels with
	// the read rather than being something the client derives.
	HeadID    string         `json:"head_id"`
	Revisions []revisionView `json:"revisions"`
}

// handleRecordRevisions renders one record's append-only chain (#87).
//
// The chain is the frontier's, oldest first, and is neither re-sorted nor
// filtered here: it is a history, and a history that hid an entry a reader
// would find uninteresting would be a history nobody could audit.
func (s *Server) handleRecordRevisions(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	ref, ok := s.requireRecordRef(w, r)
	if !ok {
		return
	}
	chain, err := s.opts.Frontier.Revisions(r.Context(), ref)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := revisionChain{Record: viewRef(ref), Revisions: make([]revisionView, 0, len(chain))}
	for i, revision := range chain {
		last := i == len(chain)-1
		result.Revisions = append(result.Revisions, viewRevision(revision, last))
		if last {
			result.HeadID = revision.Entity.ID
		}
	}
	s.writeJSON(w, http.StatusOK, result)
}

// anchorView is the verified repository a draft-issue binds to (#88).
type anchorView struct {
	Workspace string `json:"workspace"`
	Remote    string `json:"remote"`
	URL       string `json:"url"`
	Branch    string `json:"branch,omitempty"`
}

// rulingView is one attributed operator answer to a proposed action.
type rulingView struct {
	ID         string `json:"id"`
	Sequence   int64  `json:"sequence"`
	Ruling     string `json:"ruling"`
	By         string `json:"by"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

func viewRuling(entry disposition.LedgerEntry) rulingView {
	return rulingView{
		ID:         entry.ID,
		Sequence:   entry.Sequence,
		Ruling:     string(entry.Ruling),
		By:         entry.By,
		RecordedAt: timeText(entry.RecordedAt),
		Note:       entry.Payload.Note,
	}
}

// proposedActionView is one disposition.Disposition on the wire: a next action
// a run proposed against a record revision, its derived state, and every
// operator answer recorded about it.
//
// The Go name says "proposed action" because this package already renders a
// §4.7 review verdict as dispositionEvent, and the two are different things
// about the same records — a verdict on a record versus a decision about an
// action outside Babel. The wire keeps #87's own vocabulary, so a reader of the
// JSON sees what the CLI prints.
type proposedActionView struct {
	ID         string    `json:"id"`
	Record     refView   `json:"record"`
	Kind       string    `json:"kind"`
	Status     string    `json:"status"`
	ProposedBy actorView `json:"proposed_by"`
	// Ref is the reference the proposing run emitted this action under,
	// empty for one an operator synthesized.
	Ref       string       `json:"ref,omitempty"`
	CreatedAt string       `json:"created_at"`
	Summary   string       `json:"summary"`
	Rationale string       `json:"rationale,omitempty"`
	Anchor    *anchorView  `json:"anchor,omitempty"`
	Ledger    []rulingView `json:"ledger"`
	// Draft is the issue text a draft-issue action renders to, absent for
	// every other kind. It travels in the response and reaches a browser as
	// escaped text: Babel drafts, the operator files, and nothing here
	// opens a link or a network connection to anywhere.
	Draft string `json:"draft,omitempty"`
}

// recordDispositions is GET /api/record/dispositions' response: the proposed
// actions attached to one record revision, the invitations recorded against it,
// and the chain head both are answered relative to.
type recordDispositions struct {
	Record refView `json:"record"`
	// HeadID is the chain's current state, which is what a mutation from
	// this page confirms it saw. It is here rather than left to a second
	// request because a page that listed actions without knowing which
	// wording is current could not tell an operator that the text moved.
	HeadID       string               `json:"head_id"`
	Dispositions []proposedActionView `json:"dispositions"`
	Invitations  []invitationView     `json:"invitations"`
}

// invitationView is one instruction-free "process this further" (#87).
type invitationView struct {
	ID        string  `json:"id"`
	Record    refView `json:"record"`
	By        string  `json:"by"`
	CreatedAt string  `json:"created_at"`
	// ConsumedBy is the run that took this invitation into its scope, empty
	// while it is still queued.
	ConsumedBy string `json:"consumed_by,omitempty"`
	ConsumedAt string `json:"consumed_at,omitempty"`
	Open       bool   `json:"open"`
}

func viewInvitation(invitation disposition.Invitation) invitationView {
	return invitationView{
		ID:         invitation.ID,
		Record:     viewRef(invitation.Record),
		By:         invitation.By,
		CreatedAt:  timeText(invitation.CreatedAt),
		ConsumedBy: invitation.ConsumedBy,
		ConsumedAt: timeText(invitation.ConsumedAt),
		Open:       invitation.Open(),
	}
}

// handleRecordDispositions lists what a record's proposed next actions are and
// what has been decided about each.
//
// Every action is listed, including the ones already accepted or declined.
// Nothing in #87 closes: a declined action stays readable and may be
// reconsidered, so a listing that showed only the undecided ones would make the
// ledger look like a queue that empties.
func (s *Server) handleRecordDispositions(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Dispositions != nil && s.opts.Frontier != nil, "the disposition ledger") {
		return
	}
	ref, ok := s.requireRecordRef(w, r)
	if !ok {
		return
	}
	head, err := s.opts.Frontier.Head(r.Context(), ref)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	actions, _, err := s.opts.Dispositions.List(r.Context(), disposition.ListFilter{
		Record: ref,
		Limit:  disposition.MaxListLimit,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := recordDispositions{
		Record:       viewRef(ref),
		HeadID:       head.ID,
		Dispositions: make([]proposedActionView, 0, len(actions)),
		Invitations:  []invitationView{},
	}
	for _, action := range actions {
		view, err := s.viewProposedAction(r, action)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		result.Dispositions = append(result.Dispositions, view)
	}
	// All is set: the queue a run already took is the honest state of a
	// record an operator invited, and a page that dropped consumed
	// invitations would show a record as never invited the moment a cycle
	// picked it up.
	queue, err := s.opts.Dispositions.Invitations(r.Context(), disposition.InvitationFilter{
		Record: ref,
		All:    true,
		Limit:  listScanCap,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	for _, invitation := range queue {
		result.Invitations = append(result.Invitations, viewInvitation(invitation))
	}
	s.writeJSON(w, http.StatusOK, result)
}

// viewProposedAction renders one action with its ledger and, for a draft-issue,
// the text the operator would paste.
func (s *Server) viewProposedAction(r *http.Request, action disposition.Disposition) (proposedActionView, error) {
	ledger, err := s.opts.Dispositions.Ledger(r.Context(), action.ID)
	if err != nil {
		return proposedActionView{}, err
	}
	view := proposedActionView{
		ID:         action.ID,
		Record:     viewRef(action.Record),
		Kind:       string(action.Kind),
		Status:     string(action.Status),
		ProposedBy: viewActor(action.ProposedBy),
		Ref:        action.Ref,
		CreatedAt:  timeText(action.CreatedAt),
		Summary:    action.Payload.Summary,
		Rationale:  action.Payload.Rationale,
		Ledger:     make([]rulingView, 0, len(ledger)),
	}
	for _, entry := range ledger {
		view.Ledger = append(view.Ledger, viewRuling(entry))
	}
	if anchor := action.Payload.Anchor; anchor != nil {
		view.Anchor = &anchorView{
			Workspace: anchor.Workspace,
			Remote:    anchor.Remote,
			URL:       anchor.URL,
			Branch:    anchor.Branch,
		}
	}
	if action.Kind == disposition.KindDraftIssue {
		// The renderer is internal/disposition's own, so what a browser
		// shows is byte-for-byte what `babel disposition show` prints. It
		// is a pure function of the stored action: rendering a draft
		// reads nothing, writes nothing, and files nothing.
		draft, err := disposition.Draft(action)
		if err != nil {
			return proposedActionView{}, err
		}
		view.Draft = draft
	}
	return view, nil
}

// decideActionRequest is POST /api/record/disposition/decide's body.
type decideActionRequest struct {
	DispositionID string `json:"dispositionId"`
	// Ruling is "accepted" or "declined" as internal/disposition spells
	// them. It is not validated here: an unknown ruling reaches the store
	// and is refused by name, so there is one place that decides what an
	// answer may be.
	Ruling string `json:"ruling"`
	Note   string `json:"note"`
	// HeadID is the chain head the page was rendered against. See
	// requireSeenHead.
	HeadID string `json:"headId"`
}

type decideActionResult struct {
	Entry  rulingView `json:"entry"`
	Status string     `json:"status"`
	// Published states what happened outside Babel, which is nothing. It is
	// a field rather than a comment for the reason the CLI's is: a client
	// reading this response is exactly the reader who might otherwise
	// assume an accepted draft-issue was filed.
	Published string `json:"published"`
}

// publishedNothing is what every acceptance reports about the world outside
// Babel. The sentence is fixed and identical to the CLI's claim, so the two
// surfaces cannot come to describe the same act differently.
const publishedNothing = "nothing; Babel authorized this action and performed none of it"

// handleDecideDisposition records one attributed operator answer to a proposed
// action (#87).
//
// The handler resolves the identity, confirms the operator was shown the
// current wording, and then gets out of the way. Whether the ruling is a
// ruling, whether the action exists, and what the derived status becomes are
// all internal/disposition's, and it appends rather than updates: reconsidering
// an answer is another entry, and both stay readable in order.
func (s *Server) handleDecideDisposition(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Dispositions != nil && s.opts.Frontier != nil, "the disposition ledger") {
		return
	}
	var request decideActionRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.DispositionID == "" {
		s.writeError(w, http.StatusBadRequest, "dispositionId is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	// The action is read before anything is written, because the record it
	// was proposed against is what the freshness check is about and only the
	// action knows which record that is.
	action, err := s.opts.Dispositions.Disposition(r.Context(), request.DispositionID)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if !s.requireSeenHead(w, r, action.Record, request.HeadID) {
		return
	}
	entry, err := s.opts.Dispositions.Decide(r.Context(), disposition.DecideInput{
		DispositionID: request.DispositionID,
		Ruling:        disposition.Ruling(request.Ruling),
		By:            by.ID(),
		Note:          request.Note,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The status is read back from the ledger rather than inferred from the
	// answer just given, for the reason handleReviewDecide reads its own
	// back: a derived state this handler computed could disagree with the
	// entries behind it.
	decided, err := s.opts.Dispositions.Disposition(r.Context(), request.DispositionID)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, decideActionResult{
		Entry:     viewRuling(entry),
		Status:    string(decided.Status),
		Published: publishedNothing,
	})
}

// inviteRequest is POST /api/record/invite's body.
//
// There is no note, no scope, and no priority, and their absence is the
// invariant rather than an omission: #87's nudge says a record deserves
// attention and deliberately does not say what to do about it.
type inviteRequest struct {
	Record refView `json:"record"`
	HeadID string  `json:"headId"`
}

type inviteResult struct {
	Invitation invitationView `json:"invitation"`
	// Instruction is what the invitation tells the next run to do, which is
	// nothing. It is stated because a queued invitation looks like a task,
	// and the difference between a nudge and a brief is the whole reason
	// this record has no text.
	Instruction string `json:"instruction"`
}

// invitationCarriesNoInstruction is the fixed sentence every invitation
// response returns, matching the CLI's own.
const invitationCarriesNoInstruction = "none; what to do with it is the next run's judgement"

// handleInviteRecord records an instruction-free invitation to process a record
// further.
//
// Inviting the same record twice is two invitations rather than an error. The
// operator asked twice, which is a fact about how much attention they think the
// record deserves, and a surface that deduplicated it would be deciding that
// the second ask did not count.
func (s *Server) handleInviteRecord(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Dispositions != nil && s.opts.Frontier != nil, "the disposition ledger") {
		return
	}
	var request inviteRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	record, ok := s.requestRecordRef(w, request.Record)
	if !ok {
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	if !s.requireSeenHead(w, r, record, request.HeadID) {
		return
	}
	invitation, err := s.opts.Dispositions.Invite(r.Context(), disposition.InviteInput{
		Record: record,
		By:     by.ID(),
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, inviteResult{
		Invitation:  viewInvitation(invitation),
		Instruction: invitationCarriesNoInstruction,
	})
}

// reviveRequest is POST /api/record/revive's body.
type reviveRequest struct {
	Record refView `json:"record"`
	// Reason is why the candidate deserves to move again. It is required by
	// internal/frontier and refused here too, before an identity is even
	// resolved: a revive whose argument is missing is a request that has not
	// been made rather than one that failed.
	Reason string `json:"reason"`
	// Status is where the candidate lands, empty for the frontier's own
	// default of queued. It is passed through unvalidated: reviving into a
	// resting state is refused by internal/frontier, which is the one place
	// that knows which states rest.
	Status string `json:"status"`
	HeadID string `json:"headId"`
}

type reviveResult struct {
	Record refView         `json:"record"`
	Event  statusEventView `json:"event"`
}

// handleReviveRecord returns a resting candidate to the frontier (#87).
//
// Only a candidate has a lifecycle, so only a candidate can be revived, and
// that refusal is here rather than in the store because it is a fact about the
// request's shape: a finding has no status column for a transition to append
// to, and the frontier would report a missing hypothesis instead of the reason.
func (s *Server) handleReviveRecord(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Reviver != nil && s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	var request reviveRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	record, ok := s.requestRecordRef(w, request.Record)
	if !ok {
		return
	}
	if record.Type != frontier.EntityHypothesis {
		s.writeError(w, http.StatusBadRequest,
			"only a candidate carries a lifecycle status, so only a candidate has one to revive")
		return
	}
	if request.Reason == "" {
		s.writeError(w, http.StatusBadRequest,
			"a revive states why the candidate deserves to move again; reason is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	if !s.requireSeenHead(w, r, record, request.HeadID) {
		return
	}
	event, err := s.opts.Reviver.Revive(r.Context(), frontier.ReviveInput{
		HypothesisID: record.ID,
		Status:       frontier.Status(request.Status),
		Actor:        frontier.Operator(by.ID()),
		Reason:       request.Reason,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, reviveResult{
		Record: viewRef(record),
		Event:  viewStatusEvent(event),
	})
}

// requireRecordRef reads the ?type=&id= pair a record read names.
func (s *Server) requireRecordRef(w http.ResponseWriter, r *http.Request) (frontier.Ref, bool) {
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

// requestRecordRef resolves the record a mutation's body names.
func (s *Server) requestRecordRef(w http.ResponseWriter, named refView) (frontier.Ref, bool) {
	kind, ok := refKind(named.Type)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "record type is not a record kind")
		return frontier.Ref{}, false
	}
	if named.ID == "" {
		s.writeError(w, http.StatusBadRequest, "record id is required")
		return frontier.Ref{}, false
	}
	return frontier.Ref{Type: kind, ID: named.ID}, true
}

// requireSeenHead confirms a mutation is acting on the wording its operator was
// shown.
//
// Every record read on this surface reports the head of the record's chain, and
// every mutation sends it back. If the chain has grown since — a run revised the
// candidate, or `babel revise` did — the click was made against text that has
// been replaced, and the refusal says so and names the current wording instead
// of recording an authorization nobody gave. It is a 409 rather than a 400
// because the request was well formed and the state moved underneath it, which
// is the same distinction the reality ledger's replay refusal draws.
//
// A missing head is a 400 rather than a skipped check. A client that could omit
// the confirmation would make the honesty rule optional, and the one thing an
// append-only ledger cannot do is take an entry back.
func (s *Server) requireSeenHead(w http.ResponseWriter, r *http.Request, record frontier.Ref, seen string) bool {
	if seen == "" {
		s.writeError(w, http.StatusBadRequest,
			"a record action confirms the revision it was shown; headId is required")
		return false
	}
	head, err := s.opts.Frontier.Head(r.Context(), record)
	if err != nil {
		s.serviceError(w, r, err)
		return false
	}
	if head.ID != seen {
		s.logf("%s %s refused: the record was revised since the page was rendered", r.Method, r.URL.Path)
		s.writeError(w, http.StatusConflict, "this record was revised after the page was rendered: "+
			"the current wording is "+head.ID+", and authorizing an action against the wording it replaced "+
			"would record a decision about text nobody read. Reload the record and decide again.")
		return false
	}
	return true
}

// classifyRecordAction maps internal/disposition's and the revive
// transition's sentinels onto a status and a fixed message, on the same terms
// classifyService maps every other service's: nothing from the error's own text
// travels, because a wrapped store error can quote a path or corpus prose.
//
// It is a separate function called first by classifyService rather than more
// cases inside it, because these two components arrived with #87 and their
// sentinels are about actions on records rather than about records.
func classifyRecordAction(err error) (int, string, bool) {
	switch {
	case errors.Is(err, disposition.ErrUnknownDisposition):
		return http.StatusNotFound, "no proposed action with that identifier", true
	case errors.Is(err, disposition.ErrUnknownInvitation):
		return http.StatusNotFound, "no invitation with that identifier", true
	case errors.Is(err, disposition.ErrAlreadyConsumed):
		return http.StatusConflict, "a run has already taken this invitation into its scope", true
	case errors.Is(err, disposition.ErrAnchorRequired):
		return http.StatusBadRequest, "a draft-issue action binds to a verified repository", true
	case errors.Is(err, frontier.ErrNotResting):
		return http.StatusConflict, "this candidate is not at rest, so it is already on the frontier", true
	case errors.Is(err, frontier.ErrSuperseded):
		return http.StatusConflict, "this record has already been superseded; the chain's head is the current wording", true
	case errors.Is(err, disposition.ErrInvalidValue):
		return http.StatusBadRequest, "a value in the request is outside what this action accepts", true
	}
	return 0, "", false
}
