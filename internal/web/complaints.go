package web

// Issue #115's web half: the box an operator says something into, and the pages
// that show what they said.
//
// A complaint is a record and not a ticket, and this surface is where that
// distinction is easiest to lose. Everything else the browser can write here
// answers something Babel produced — a verdict on a candidate, a ruling on a
// proposed action, an answer to a question the ledger asked. A complaint is the
// operator speaking first, so there is nothing for it to be the state of.
//
// That is why this file holds no status control and no closure field. There is
// no route that ends a complaint, no `resolved` on any view, no assignee, no
// priority and no acknowledgement flag, and their absence is the contract
// rather than an omission: the moment a complaint acquires one, Babel has
// become a work tracker and GitHub already is one. "Was this addressed?" is
// answered by the citation graph the record pages already render (#113) — the
// backlinks from the hypotheses and proposals that took the complaint on — and
// an unanswered complaint is then visibly the absence of work rather than a
// field nobody updated.
//
// Capture runs a spend-free retrieval pass and reports what Babel already holds
// touching the text. It sends nothing anywhere and could not: this surface has
// no model client, and the pass is one FTS query against a local partition. It
// is a prompt to compare and never a claim of sameness, which is why it carries
// no score (§5.4) and why nothing about it can fail the capture — the complaint
// is already durable by the time the pass runs, and an operator told their
// sentence failed because a rebuildable cache would not answer has been told
// something untrue about the only thing they cared about.

import (
	"context"
	"errors"
	"net/http"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reference"
)

// complaintView is one wording of one complaint, with the operator's whole
// text.
//
// The full text travels only here, on the record a reader asked for by id.
// Every listing and every chain entry carries the bounded summary instead,
// because a page that shipped fifty verbatim complaints to render fifty rows
// would be sending the operator's whole steering history to draw a table.
type complaintView struct {
	ID     string `json:"id"`
	RootID string `json:"root_id"`
	// Supersedes is the wording this one amends, absent on an original. It
	// is omitted rather than empty so a client cannot render "supersedes
	// nothing" as a superseded record.
	Supersedes string `json:"supersedes,omitempty"`
	Sequence   int    `json:"sequence"`
	By         string `json:"by"`
	Host       string `json:"host"`
	Text       string `json:"text"`
	// Redacted says capture replaced secret-shaped material with
	// placeholders. It rides the record rather than the response because a
	// reader who cannot tell that a placeholder was Babel's doing would read
	// it as something the operator wrote.
	Redacted bool   `json:"redacted"`
	At       string `json:"at"`
	// Head says this wording is what the operator currently says. It is
	// always present, on complaintRevisionView.Head's terms: one page shows
	// this record and its whole chain side by side, and a field that existed
	// on the chain entries but appeared on the record only when true would
	// make a reader derive the same fact two ways on one screen.
	Head bool `json:"head"`
}

// complaintSummaryView is one listing row: the same record without its text.
type complaintSummaryView struct {
	ID         string `json:"id"`
	RootID     string `json:"root_id"`
	Supersedes string `json:"supersedes,omitempty"`
	Sequence   int    `json:"sequence"`
	By         string `json:"by"`
	Host       string `json:"host"`
	Summary    string `json:"summary"`
	Redacted   bool   `json:"redacted"`
	At         string `json:"at"`
	// Citations is the only answer this surface gives to "was this ever
	// addressed?", and it is a count of what cites the complaint rather than
	// a state somebody set. Absent means nobody counted — this build has no
	// reference graph, or the graph would not answer for this row — which is
	// a different claim from a zero, and the two are never collapsed for the
	// reason QueueItem.Citations gives.
	Citations *citationCounts `json:"citations,omitempty"`
}

// complaintRevisionView is one entry of a complaint's chain.
//
// It carries the summary and never the text: the full wording of any revision
// is read by opening that revision's own id, which is a page that exists
// precisely because an earlier wording stays readable forever (#87).
type complaintRevisionView struct {
	ID         string `json:"id"`
	Supersedes string `json:"supersedes,omitempty"`
	Sequence   int    `json:"sequence"`
	By         string `json:"by"`
	Host       string `json:"host"`
	Summary    string `json:"summary"`
	Redacted   bool   `json:"redacted"`
	At         string `json:"at"`
	// Head is which entry of the chain is current. A chain is rendered as a
	// list and every entry has to say so; an omitted false would make the
	// timeline's last entry the only one carrying the field, which reads as
	// a rendering accident rather than as a fact.
	Head bool `json:"head"`
}

// complaintList is GET /api/complaints' response.
type complaintList struct {
	Items []complaintSummaryView `json:"items"`
	Total int                    `json:"total"`
}

// complaintDetail is GET /api/complaint's response.
type complaintDetail struct {
	// Complaint is the wording the caller asked about, which is not
	// necessarily the head: a chain reads the same from anywhere in it, and
	// an operator following a citation to a superseded wording is entitled
	// to see what they said then.
	Complaint complaintView `json:"complaint"`
	// HeadID is what the operator currently says, so a page showing an
	// older wording can say so without deriving it from the chain.
	HeadID    string                  `json:"head_id"`
	Revisions []complaintRevisionView `json:"revisions"`
}

// adjacentOutputView is one piece of prior material the capture-time pass
// found: something Babel already said that touches what the operator just did.
//
// There is no score field and there never will be. §5.4 keeps retrieval rank
// out of everything a reader could mistake for evidence strength, and this list
// is read at exactly the moment somebody is deciding whether Babel already
// covered their complaint — a number beside a row would answer that question
// for them, with a bm25 weight.
type adjacentOutputView struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// captureResult is POST /api/complaint/tell's response.
type captureResult struct {
	Complaint complaintView        `json:"complaint"`
	Adjacent  []adjacentOutputView `json:"adjacent"`
	// AdjacencyNote says why the pass could not run, and is absent when it
	// ran and matched nothing. The two are different facts: "Babel holds
	// nothing about this" is an answer, and "nothing could be looked up" is
	// the absence of one, and a client rendering an empty list has to be
	// able to tell them apart.
	AdjacencyNote string `json:"adjacency_note,omitempty"`
	Steering      string `json:"steering"`
}

// captureOpenedNothing is what every capture reports Babel did about the
// complaint, which is nothing. The sentence is fixed and sits beside
// publishedNothing and invitationCarriesNoInstruction for their reason: a
// client reading this response is exactly the reader who might otherwise assume
// something was filed, and two surfaces describing the act differently would be
// two claims about what Babel does with steering.
const captureOpenedNothing = "nothing; a complaint is steering pressure, and Babel opened, " +
	"assigned and scheduled none of it"

// maxCaptureAdjacency bounds the prior material one capture reports, on
// `babel tell`'s maxTellAdjacency reasoning and at the same number: the list
// answers "does Babel already have something touching this", which a reader
// settles from the first few rows, and a longer one would turn the answer into
// a search result to work through at the moment the operator was trying to say
// one thing and move on.
const maxCaptureAdjacency = 8

// tellRequest is POST /api/complaint/tell's body.
//
// One field, and unknown ones are refused by decodeBody. A capture is
// append-only and cannot be corrected afterwards, so a misspelled field that
// was quietly ignored would leave the operator believing they had said
// something the record does not contain, with no way to take it back.
//
// There is deliberately no `about` or `addresses` field. `babel tell --about`
// exists for the operator who already knows which record annoyed them, and it
// is a CLI affordance for somebody holding an id; a browser field for it would
// invite the capture box to become a form, and #115 is explicit that this is
// not one.
type tellRequest struct {
	Text string `json:"text"`
}

// handleComplaints lists what the operator currently says, newest first.
//
// Heads only, in the store's order, neither re-sorted nor filtered here. There
// is no status filter and no filter of any other kind, because there is no
// state to filter on: every complaint on this list is equally unanswered until
// something cites it, and a control that narrowed the list would be inventing
// the categories #115 refuses to give a complaint.
func (s *Server) handleComplaints(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Complaints != nil, "operator complaints") {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	heads, err := s.opts.Complaints.Heads(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// Total is the whole head count and not the page's length, the way
	// handleHypotheses reports its own: a client that could not tell a short
	// page from the end of the list would page forever or stop early.
	result := complaintList{Items: []complaintSummaryView{}, Total: len(heads)}
	start, end := pg.window(len(heads))
	for _, head := range heads[start:end] {
		row := viewComplaintSummary(head)
		// Counted against this wording's own id rather than the chain's
		// root, because that is the identifier a record addressing this
		// complaint recorded and the identifier this row's page opens.
		row.Citations = s.citationCounts(r.Context(),
			reference.RecordRef{Kind: namespaceComplaint, ID: head.ID})
		result.Items = append(result.Items, row)
	}
	s.writeJSON(w, http.StatusOK, result)
}

// handleComplaint renders one wording and the whole chain it belongs to.
//
// Any wording of a chain answers, and a superseded one answers with its own
// text rather than being redirected to the head. An amendment is the same
// complaint said again (#87), so the earlier wording is history rather than
// error: it stays readable at its own id, it stays cited by whatever cited it,
// and a surface that quietly showed the newer words instead would make a
// citation point at something the citing record never saw.
//
// The chain is the store's, oldest first, neither re-sorted nor filtered, for
// the reason handleRecordRevisions gives about its own: a history that hid an
// entry a reader would find uninteresting would be a history nobody could
// audit.
func (s *Server) handleComplaint(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Complaints != nil, "operator complaints") {
		return
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return
	}
	record, err := s.opts.Complaints.Complaint(r.Context(), id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	chain, err := s.opts.Complaints.Revisions(r.Context(), id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result := complaintDetail{Revisions: make([]complaintRevisionView, 0, len(chain))}
	if len(chain) != 0 {
		result.HeadID = chain[len(chain)-1].ID
	}
	for _, entry := range chain {
		result.Revisions = append(result.Revisions,
			viewComplaintRevision(entry, entry.ID == result.HeadID))
	}
	result.Complaint = viewComplaint(record, record.ID == result.HeadID)
	s.writeJSON(w, http.StatusOK, result)
}

// handleTellComplaint captures one thing the operator wanted to say (#115).
//
// The identity is resolved before anything is written and the refusal is
// review.NewAuthority's, exactly as #87's and §4.8's mutations do it: a
// complaint outranks the conductor's own policy at the invitation rung of #96's
// ladder, so an unattributed one would be a way of borrowing operator authority
// without an operator.
//
// There is no seen-head confirmation, and its absence is not an oversight. The
// #87 mutations name the chain head the page was rendered against because they
// change the state of a record the operator was shown; a capture mints a new
// record, so there is nothing the operator was shown that can have moved
// underneath them.
func (s *Server) handleTellComplaint(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Complaints != nil, "operator complaints") {
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	host, ok := s.requireHost(w, r)
	if !ok {
		return
	}
	var request tellRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	// Nothing about the text is checked here. Empty words, prose past
	// MaxTextBytes and invalid UTF-8 are internal/complaint's three refusals,
	// and a handler that restated one would be a second place the rule could
	// change.
	told, err := s.opts.Complaints.Tell(r.Context(), complaint.TellInput{
		Text: request.Text,
		By:   by.ID(),
		Host: host,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	adjacent, note := s.captureAdjacency(r.Context(), told)
	if adjacent == nil {
		// Always an array on the wire. A client that had to distinguish a
		// null from an empty list would be distinguishing two renderings of
		// the same fact.
		adjacent = []adjacentOutputView{}
	}
	s.writeJSON(w, http.StatusOK, captureResult{
		// A capture always mints an original, which is always its chain's
		// head, so the record this response describes is what the operator
		// currently says by construction.
		Complaint:     viewComplaint(told, true),
		Adjacent:      adjacent,
		AdjacencyNote: note,
		Steering:      captureOpenedNothing,
	})
}

// requireHost names the machine a capture is being recorded on, refusing when
// the launch cannot.
//
// complaint.TellInput.Host is required provenance: it answers "where was I when
// I said this", and internal/complaint refuses a capture without it. A surface
// that filled in "web" would be recording where a sentence was said without
// knowing, which is worse than not recording it — every complaint told through
// a browser would then claim to come from a machine called web.
//
// It is a 409 on requireOperator's terms rather than a 500: the route exists
// and the request is well formed, and what is missing is state this launch
// never had.
func (s *Server) requireHost(w http.ResponseWriter, r *http.Request) (string, bool) {
	host := s.hostIdentity(r.Context())
	if host == "" {
		s.writeError(w, http.StatusConflict,
			"this session cannot name the machine it is running on, and a complaint records the host it was captured on")
		return "", false
	}
	return host, true
}

// captureAdjacency reports what Babel already holds touching what was just
// said, and why it could not look, when it could not.
//
// The pass does not reconcile the retrieval index, which is where it differs
// from `babel tell`'s. That partition is rebuilt by preparation and by the CLI
// capture, and a browser POST that rebuilt a shared cache would be exactly the
// writer §14's gate keeps off this surface: the search runs against the
// deployment as the last reconcile left it, which is a weaker answer than the
// CLI's and an honest one.
//
// Nothing here fails the capture. The complaint is durable before this runs, so
// every path returns a note instead of an error.
func (s *Server) captureAdjacency(ctx context.Context, told complaint.Complaint) ([]adjacentOutputView, string) {
	if s.opts.Search == nil {
		return nil, "this build has no retrieval index, so nothing adjacent could be looked up"
	}
	// One extra row of headroom: the complaint's own chain is filtered out
	// below, and asking for exactly the ceiling would let a self-match cost
	// the operator one real neighbour.
	hits, err := s.opts.Search.FrontierSearch(ctx, index.FrontierQuery{
		Match: index.MatchExpression(told.Text),
		Order: index.OrderRelevance,
		Limit: maxCaptureAdjacency + 1,
	})
	if err != nil {
		// An unsearchable complaint is not a failed capture and not a
		// failed pass either. The operator wrote something the tokenizer
		// has no term for — punctuation, a single stop word — which is a
		// fact about the text rather than about the index, so there is
		// nothing to report and the empty list says it.
		if errors.Is(err, index.ErrNoSearchableTerm) || errors.Is(err, index.ErrMatchTooLong) {
			return nil, ""
		}
		// The note is fixed and carries nothing from the error. This is
		// where this surface deliberately differs from the CLI, which
		// sanitizes the store's own text and shows it to the operator at
		// their own terminal: a wrapped store error can quote a database
		// path or corpus prose, and §9 keeps both out of a response and out
		// of the diagnostics stream.
		s.logf("capture-time adjacency refused")
		return nil, "the retrieval index would not answer, so nothing adjacent could be looked up"
	}
	rows := make([]adjacentOutputView, 0, len(hits))
	for _, hit := range hits {
		// The complaint's own chain is not prior material. Matching
		// yourself says nothing, and an amendment listing the wording it
		// replaced would read as Babel having already said what the
		// operator just did.
		if hit.RootID == told.RootID {
			continue
		}
		if len(rows) == maxCaptureAdjacency {
			break
		}
		rows = append(rows, adjacentOutputView{
			Kind:    string(hit.Kind),
			ID:      hit.ID,
			Summary: hit.Summary,
		})
	}
	return rows, ""
}

func viewComplaint(c complaint.Complaint, head bool) complaintView {
	return complaintView{
		ID:         c.ID,
		RootID:     c.RootID,
		Supersedes: c.AncestorID,
		Sequence:   c.Sequence,
		By:         c.By,
		Host:       c.Host,
		Text:       c.Text,
		Redacted:   c.Redacted,
		At:         timeText(c.CreatedAt),
		Head:       head,
	}
}

// viewComplaintSummary and viewComplaintRevision both take the bounded line
// from complaint.Summary rather than deriving one here, so a listing row and a
// chain entry cannot come to disagree about the same wording — and neither can
// disagree with the search hit internal/index shows for it.
func viewComplaintSummary(c complaint.Complaint) complaintSummaryView {
	return complaintSummaryView{
		ID:         c.ID,
		RootID:     c.RootID,
		Supersedes: c.AncestorID,
		Sequence:   c.Sequence,
		By:         c.By,
		Host:       c.Host,
		Summary:    c.Summary(),
		Redacted:   c.Redacted,
		At:         timeText(c.CreatedAt),
	}
}

func viewComplaintRevision(c complaint.Complaint, head bool) complaintRevisionView {
	return complaintRevisionView{
		ID:         c.ID,
		Supersedes: c.AncestorID,
		Sequence:   c.Sequence,
		By:         c.By,
		Host:       c.Host,
		Summary:    c.Summary(),
		Redacted:   c.Redacted,
		At:         timeText(c.CreatedAt),
		Head:       head,
	}
}
