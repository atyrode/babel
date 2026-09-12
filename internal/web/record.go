package web

// Issue #235's reading surface: one record, peeled.
//
// The operator's direction is the whole specification — "the complexity of the
// data is for Babel itself, the user really only needs the surface, and to be
// able to dig when needed" — and it decides two things about this file.
//
// One request returns the whole object. A reader who opened a proposal was
// already going to read its claim, its case, its evidence, who reviewed it and
// what it cost; issue #234 measured what it costs to assemble that from four
// pages with three vocabularies. So the assembly happens here, once, against
// the services this server already holds, and the client peels what it was
// handed rather than fetching a depth at a time.
//
// Absent is absent. Every section is a pointer or an omitempty slice, and a
// section this surface could not read does not travel as a zero. That is
// detail.go's third rule applied to the whole document rather than to the
// fallback alone: a reception block reading nought-nought-nought says three
// reviewers looked and none of them minded, and a standing of `new` on a
// record whose decision history is on another machine says nobody has ruled.
// Neither is true, and a reader cannot tell a rendered zero from an answer.
//
// The peel names what a reader wants rather than what the stores hold. A
// claim, a case, evidence, reception, machinery: five depths, in that order,
// for all four record kinds. What each depth is filled from differs by kind
// and is stated at each projection below, and a kind that has nothing to say
// at a depth says nothing rather than saying it emptily — a hypothesis is a
// bare claim with no case, an observation is evidence and therefore carries no
// standing at all (§6.7 makes it unreviewable), and only a proposal fills
// every field the case has.
//
// Deployment-wide, like the listings. A record merged from another instance is
// a row this surface's listings already show (see fleetScope), and a detail
// route that answered 404 for one read to its operator as lost work rather
// than as a store boundary. detail.go states the four rules that fallback
// keeps; this route keeps all four, and degrades in record terms: the notice
// says this record could not be read, never which machine failed to answer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/transcript"
)

// recordPathPrefix is where the peel and the operator's reception live.
//
// It is the second path-parameter route on this surface after
// proposalPathPrefix, and unlike that one it is resolved from routeAPI's
// default branch rather than before the switch. The reason is the table: six
// whole paths under /api/record/ already name actions on a record — its
// revisions, its proposed actions, its citations — and a prefix cut placed
// ahead of them would have to re-implement the switch in order not to swallow
// them. Reached from the default, every named path has already matched, so
// what is left under this prefix is a record identifier or a mistake, and this
// file can say which.
const recordPathPrefix = "/api/record/"

// receptionPathSuffix is the operator's voice on the record he is reading.
const receptionPathSuffix = "/reception"

// routeRecord dispatches the three id-bearing record routes, reporting whether
// the path was one of them.
func (s *Server) routeRecord(w http.ResponseWriter, r *http.Request) bool {
	rest, found := strings.CutPrefix(r.URL.Path, recordPathPrefix)
	if !found || rest == "" {
		return false
	}
	if id, isReception := strings.CutSuffix(rest, receptionPathSuffix); isReception {
		if s.requireMethod(w, r, http.MethodPost) {
			s.handleRecordReception(w, r, id)
		}
		return true
	}
	// The conversation under the record (§8.7). It answers two methods,
	// which is deliberate and is the same judgement /api/evaluation/policy
	// makes: it is one resource rather than two, the box an operator types
	// into is rendered from the read, and the write answers with a comment
	// of exactly the shape the read serves. A second path would let one
	// thread's read and write disagree about their own shape the first
	// time either changed.
	if id, isComments := strings.CutSuffix(rest, commentsPathSuffix); isComments {
		switch r.Method {
		case http.MethodGet:
			s.handleRecordComments(w, r, id)
		case http.MethodPost:
			s.handlePostComment(w, r, id)
		default:
			s.writeError(w, http.StatusBadRequest, "unsupported method")
		}
		return true
	}
	if strings.Contains(rest, "/") {
		return false
	}
	if s.requireMethod(w, r, http.MethodGet) {
		s.handleRecordPeel(w, r, rest)
	}
	return true
}

// kindOfRecordID resolves the record kind an identifier names.
//
// The prefix is the discriminator because it is the identifier's own: every
// record this surface can open is minted by internal/frontier with a
// three-letter family prefix, so a route that took a `type` parameter beside
// the id would be asking a client to restate something the id already says —
// and would let the two disagree, which is how /api/finding could be handed a
// proposal.
//
// It is named for the identifier rather than for the kind because fleet.go
// already owns recordKind, which answers the other direction of the same
// question: what the shared catalog calls a kind a caller named.
func kindOfRecordID(id string) (frontier.EntityType, bool) {
	prefix, _, cut := strings.Cut(id, "_")
	if !cut {
		return "", false
	}
	switch prefix {
	case "hyp":
		return frontier.EntityHypothesis, true
	case "obs":
		return frontier.EntityObservation, true
	case "fnd":
		return frontier.EntityFinding, true
	case "pro":
		return frontier.EntityProposal, true
	}
	return "", false
}

// recordPeel is GET /api/record/{id}: one record at five depths.
type recordPeel struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Title is the one line every other surface already shows for this
	// record, and Claim is the record's own sentence. They are both here
	// because a listing row and a page are different reads of one object: a
	// reader who clicked a row has to land on the words he clicked, and then
	// on the whole claim rather than the bounded version of it.
	Title string `json:"title,omitempty"`
	Claim string `json:"claim,omitempty"`
	// Notice is why part of this document is missing, in record terms. It is
	// a sentence about the record rather than about the deployment, and it is
	// this surface's own words rather than a service's, for detail.go's
	// reason: a wrapped catalog error can carry a connection string.
	Notice   string        `json:"notice,omitempty"`
	Standing *standingView `json:"standing,omitempty"`
	Action   *askView      `json:"action,omitempty"`
	Case     *caseView     `json:"case,omitempty"`
	// Origin is the conversation this record was born in, which is the one
	// thing about a record that a reader consistently wants and no store
	// holds in one place: the session a citation names, that session's own
	// title, workspace and date, and what it cost. It sits between the case
	// and the evidence because that is where it belongs in the reading — the
	// argument, then where the argument came from, then what it rests on.
	Origin   *originView    `json:"origin,omitempty"`
	Evidence []evidenceView `json:"evidence,omitempty"`
	// Related is every connection this record has that its own words do not
	// state: the other remedies for the same problem, what Babel suspects it
	// restates, the wording it replaced, and the rest of what its run wrote.
	// None of it is derivable from the record, and all of it changes what a
	// reader decides, which is why it is served rather than left to the
	// citation graph at depth five.
	Related   *relatedView   `json:"related,omitempty"`
	Reception *receptionView `json:"reception,omitempty"`
	Machinery *machineryView `json:"machinery,omitempty"`
}

// standingView is depth one's second half: where this record stands, and how
// that reads.
//
// Tone is served rather than derived in the client because the mapping is a
// judgement about Babel's vocabulary and not about colour: `deferred` is a
// warning and `duplicate` is not, and a client holding its own table would be
// deciding what a review state means.
type standingView struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
}

// The tones a standing renders in. Four rather than a boolean because a
// decided-against record and a record waiting on something are different
// states and a reader scanning a queue has to tell them apart.
const (
	toneNeutral = "neutral"
	toneGood    = "good"
	toneBad     = "bad"
	toneWarn    = "warn"
)

// The two standings this surface derives rather than reads.
//
// internal/frontier's review status answers "what was decided", and neither of
// these is that. A reopened record reads `new` there, which is true and is not
// the whole truth: a record nobody has ever ruled on and a record whose
// rejection an operator deliberately lifted are different objects to decide
// about. A superseded record has whatever status its own wording earned, and a
// reader who followed a link to it needs to know first that a later wording
// replaced it.
const (
	standingReopened   = "reopened"
	standingSuperseded = "superseded"
)

// askView is the single thing depth one asks for. There is at most one: issue
// #235 makes a record page offer the act it wants and links to everything
// else, because a page with four buttons is a page that has not decided what
// it is for.
//
// The name says "ask" rather than "action" because reality.go already owns
// actionView for a plan's proposed steps, and the two are opposite
// directions: that one is something Babel offers to do, this one is what the
// record asks of the reader.
type askView struct {
	Verb  string `json:"verb"`
	Label string `json:"label"`
}

// caseView is depth two: the argument, in prose, with no identifiers in it.
//
// The fields are §4.5's, because a proposal is the record that has a case to
// make and the other three kinds fill the subset that is true of them. Which
// field each kind fills is stated at its projection; a field a kind has no
// answer for is absent, never empty.
type caseView struct {
	Problem        string       `json:"problem,omitempty"`
	Outcome        string       `json:"outcome,omitempty"`
	Impact         string       `json:"impact,omitempty"`
	Scope          string       `json:"scope,omitempty"`
	Classification string       `json:"classification,omitempty"`
	Uncertainty    string       `json:"uncertainty,omitempty"`
	Verification   []string     `json:"verification,omitempty"`
	Risks          []string     `json:"risks,omitempty"`
	OpenQuestions  []string     `json:"open_questions,omitempty"`
	Prerequisites  []string     `json:"prerequisites,omitempty"`
	Targets        []targetView `json:"targets,omitempty"`
}

// empty reports a case with nothing in it, which is omitted rather than
// rendered: a panel of blank labels tells a reader the record is thin, and
// what it actually means is that this kind of record has no case to state.
func (c caseView) empty() bool {
	return c.Problem == "" && c.Outcome == "" && c.Impact == "" && c.Scope == "" &&
		c.Classification == "" && c.Uncertainty == "" && len(c.Verification) == 0 &&
		len(c.Risks) == 0 && len(c.OpenQuestions) == 0 && len(c.Prerequisites) == 0 &&
		len(c.Targets) == 0
}

// targetView is one suggested destination system, with §4.5's confidence and
// rationale. It stays a suggestion in the wording as well as in the schema:
// §4.6 makes a target an operator's to accept, never an automatic fact.
type targetView struct {
	System     string `json:"system"`
	Rationale  string `json:"rationale,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

// evidenceView is one citation at depth three.
//
// Excerpt is the cited bytes and Quote is the citing record's note about them,
// in that order, because that is the order they matter in. The note is a
// model's sentence about what a human said; the excerpt is what the human
// said. An operator walking the surface found the best moment in the whole
// corpus behind a link — his own words, six months old, which a proposal had
// grown out of — and the page was showing him Babel's paraphrase of them.
//
// The excerpt is read rather than scanned for, which is what makes it
// affordable. A locator carries the record's byte offset and the digest of its
// bytes, so transcript.Cited seeks, reads one line and checks the hash: a
// citation costs one seek instead of the pass over a log of tens of megabytes
// that made this section impossible before. An excerpt that could not be
// recovered — the session is not on this machine, the log was rotated, the
// bytes at that offset are no longer the bytes that were cited — is absent,
// never empty: a blank pull-quote reads as a person who said nothing.
//
// Kind is which side of the argument this citation is on, and it is not
// decoration. §4.3 and §4.5 require a record to state its counter-evidence,
// and a surface that rendered conflicting material as supporting would invert
// the record while showing every one of its words.
type evidenceView struct {
	Quote string `json:"quote,omitempty"`
	Kind  string `json:"kind"`
	// Excerpt is the cited record's own text, bounded to what a pull-quote
	// can be read as. Speaker is who said it, in the three words a reader
	// needs — a person, the model, or a tool's output — and is absent when
	// the harness named a role this surface will not translate.
	Excerpt string `json:"excerpt,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	// SessionTitle names the conversation an excerpt is quoted from, so a
	// pull-quote can say where it came from without a selector in it.
	SessionTitle string `json:"session_title,omitempty"`
	// SessionID is the selector the session page routes on, present only when
	// this host's catalog holds the session the locator names. An edge may
	// cite another machine's conversation, and a link into nothing is worse
	// than text.
	SessionID string `json:"session_id,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	// Event is the transcript position the line resolves to. internal/event
	// stamps a 1-based record line and internal/transcript numbers the same
	// records from zero, so this is the two counts meeting rather than an
	// offset that happens to look right.
	Event int    `json:"event,omitempty"`
	Href  string `json:"href,omitempty"`
}

// maxExcerpt bounds one quoted excerpt.
//
// It is a pull-quote's length rather than a record's: the cited record may be
// a whole tool output of several hundred kilobytes, and a reader checking
// evidence reads the first sentences and then follows the link. Four hundred
// characters is about the longest thing that still reads as a quotation at
// editorial measure, and the transcript is one click away for the rest.
const maxExcerpt = 400

// The three speakers an excerpt can have. A harness names roles its own way —
// `toolResult` in one, `tool` in another — and a reader does not need the
// harness's vocabulary, he needs to know whether he is reading a person, a
// model, or a machine's output.
const (
	speakerUser      = "user"
	speakerAssistant = "assistant"
	speakerTool      = "tool"
)

// originView is the conversation a record was born in.
//
// Every field of it is the session's own and none of it is derived from the
// record: the title the harness recorded or Babel derived, the workspace the
// work happened in, when it happened, and what the session cost. It is the
// first cited session rather than every cited one, because the question it
// answers is "where did this come from" and a list of nine answers to that is
// not an answer.
//
// Cost and Turns are pointers because most harnesses record no usage at all.
// A dollar figure of zero would be a measurement nobody took, which is the one
// thing a page showing money must never print.
type originView struct {
	SessionID    string   `json:"session_id"`
	SessionTitle string   `json:"session_title,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	At           string   `json:"at,omitempty"`
	CostUSD      *float64 `json:"cost_usd"`
	Turns        *int64   `json:"turns"`
	// Href opens the transcript at the cited record, which is the same route
	// the evidence rows carry and is emitted here for the same reason: the
	// route belongs to the server that resolved the selector.
	Href string `json:"href,omitempty"`
}

// relatedView is every connection a record has that its own text does not
// state.
//
// Five relations rather than one list, because they mean five different things
// and a reader acts differently on each. A competing remedy is a choice to
// make; a suspected duplicate is a comparison to perform; a supersession is a
// warning that he may be reading the wrong wording; a sibling is the rest of
// one run's thought. Flattening them into "related records" would leave the
// reader to guess which of those he was looking at.
type relatedView struct {
	// Addressing is the other remedies offered for the same claim (#114),
	// with each one's standing so a reader can see that three of the four
	// were already rejected.
	Addressing []relatedRecord `json:"addressing,omitempty"`
	// Duplicates is what Babel suspects this record restates: the overlap a
	// dedup heuristic recorded when the candidate was written, and the peers
	// a triage pass read as saying the same thing. Neither is a ruling —
	// §4.7's `duplicate` is the operator's — so both travel as a suspicion
	// with its strength attached.
	Duplicates   []relatedRecord `json:"duplicates,omitempty"`
	Supersedes   []relatedRecord `json:"supersedes,omitempty"`
	SupersededBy []relatedRecord `json:"superseded_by,omitempty"`
	// Siblings is the rest of what this record's run wrote, head revisions
	// only. It is the one relation that needs no assertion at all: the run
	// id has been on every record since the frontier's first migration, and
	// nothing until now could ask it a question.
	Siblings []relatedRecord `json:"siblings,omitempty"`
}

// relatedRecord is one record on the far side of a relation.
//
// One shape for five relations, with the fields each fills documented at the
// relation rather than repeated in five near-identical types: Addressing fills
// Standing, Duplicates fills Overlap where a heuristic recorded one, Siblings
// fills Kind, and every one of them fills the id and the line.
type relatedRecord struct {
	ID    string `json:"id"`
	Kind  string `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
	// Standing is where the far record stands, for a reader choosing between
	// remedies. It is the review status this surface already renders and not
	// a second vocabulary.
	Standing string `json:"standing,omitempty"`
	// Overlap is the recorded fraction of shared vocabulary between two
	// candidates, absent when the relation was asserted rather than measured.
	Overlap float64 `json:"overlap,omitempty"`
}

// The four citation kinds, spelled as the payload fields that hold them so a
// reader of the JSON can find the bytes it came from.
const (
	evidenceSupporting  = "supporting"
	evidenceConflicting = "conflicting"
	evidenceDirect      = "evidence"
	evidenceCounter     = "counter-evidence"
)

// receptionView is depth four: who has said what, with the operator's own
// voice kept apart from Babel's reviewers.
//
// The separation is structural rather than conventional. §4.12 keeps an
// operator's reception and a run's assessment distinct acts, and nothing here
// lets a renderer sum them: Counts tallies run-authored votes and nothing
// else, and an operator's feedback record carries no vote to add to it. The
// two blocks also speak different vocabularies on purpose — a person agrees or
// disagrees with something he chose to read, a run votes support or oppose on
// content it was served under a claim.
type receptionView struct {
	Operator *operatorStanceView `json:"operator,omitempty"`
	// History is the operator's earlier stances, newest first, excluding the
	// one Operator holds. §4.12 is append-only, so a changed mind is a second
	// record and never an edit; a surface that showed only the current stance
	// would be presenting an append-only log as a mutable field.
	History   []operatorStanceView    `json:"history,omitempty"`
	Model     []modelStanceView       `json:"model,omitempty"`
	Decisions []receptionDecisionView `json:"decisions,omitempty"`
	Counts    *receptionCounts        `json:"counts,omitempty"`
	// ByRole is the tally per §4.12 role, which is the tally that means
	// something. Four reviewers voting support is not four agreements about
	// one question: a role is what a reviewer was authorized to answer, so
	// one vote on whether the evidence holds and one on whether the record
	// matters are two answers to two questions, and summing them was how a
	// weak record with one satisfied evidence check came to read as broadly
	// supported. Disagreement within a role is the signal — that is two
	// reviewers answering the same question differently.
	ByRole []roleReceptionView `json:"by_role,omitempty"`
	// Contested reports recorded disagreement among Babel's reviewers: both
	// sides present within one role, which is the one thing a tally cannot
	// say by itself. It is per role rather than across the whole reception,
	// because support on relevance beside opposition on evidence is two
	// reviewers agreeing about different things.
	Contested bool `json:"contested,omitempty"`
}

// roleReceptionView is one role's answer to its own question.
//
// OpposingRationales carries only what the opposing votes said. A reader
// looking at a contested role needs the argument against, and the arguments
// for are already beside every reviewer's own line; repeating both here would
// make this block a second copy of the history rather than a summary of it.
type roleReceptionView struct {
	Role               string   `json:"role"`
	Support            int      `json:"support"`
	Oppose             int      `json:"oppose"`
	Unsure             int      `json:"unsure"`
	OpposingRationales []string `json:"opposing_rationales,omitempty"`
}

// operatorStanceView is one attributed operator reception.
type operatorStanceView struct {
	Stance string `json:"stance"`
	Reason string `json:"reason,omitempty"`
	At     string `json:"at,omitempty"`
}

// modelStanceView is one of Babel's reviewers, rendered beside the operator
// and never merged with him.
//
// Actor is the run identity and is deliberately not translated into a friendly
// name: there is no honest source for one, and inventing a label would be
// Babel naming its own reviewers.
type modelStanceView struct {
	Actor     string `json:"actor,omitempty"`
	Role      string `json:"role"`
	Stance    string `json:"stance,omitempty"`
	Rationale string `json:"rationale,omitempty"`
	At        string `json:"at,omitempty"`
}

// receptionDecisionView is one §4.7 ruling in the record's append-only history.
//
// It is named apart from analysis.go's decisionView because the two are
// different projections of the same events: that one belongs to the review
// page and carries its context, guidance and sequence, and this one is the
// four facts a reader weighing a record needs — what was decided, by whom,
// when, and what they said about it.
type receptionDecisionView struct {
	Disposition string `json:"disposition"`
	By          string `json:"by,omitempty"`
	At          string `json:"at,omitempty"`
	Note        string `json:"note,omitempty"`
}

// receptionCounts is the recorded tally over run-authored reception votes.
type receptionCounts struct {
	Support int `json:"support"`
	Oppose  int `json:"oppose"`
	Unsure  int `json:"unsure"`
}

func (c receptionCounts) empty() bool { return c.Support == 0 && c.Oppose == 0 && c.Unsure == 0 }

// machineryView is depth five: everything a reader debugging Babel needs and
// a reader deciding never wants to see.
type machineryView struct {
	// Revision is the head of this record's chain. A reader compares it with
	// the id above: equal means this is the current wording, different means
	// a later revision replaced it, which is also what makes the standing
	// `superseded`.
	Revision string `json:"revision,omitempty"`
	// Digest pins the sealed object this record published as, and is present
	// only when this surface read the record's catalog row — which is to say
	// for a record resolved through the shared catalog. A local read answers
	// from durable tables that hold no digest, and computing one here would
	// be a second answer to what the record hashes to.
	Digest        string `json:"digest,omitempty"`
	Schema        int    `json:"schema,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	PolicyVersion string `json:"policy_version,omitempty"`
	// Host is the machine that published a catalog-resolved record, absent
	// for a record this machine holds and for a catalog that could not
	// attribute one.
	Host      string              `json:"host,omitempty"`
	Links     []machineryLink     `json:"links,omitempty"`
	Receipts  []machineryReceipt  `json:"receipts,omitempty"`
	Revisions []machineryRevision `json:"revisions,omitempty"`
	// Cost is what producing this record cost, out of the producing run's
	// own receipt. It is the one number on this page that is about Babel
	// rather than about the corpus, which is why it is at depth five: a
	// reader deciding whether a suggestion is right does not price it, and a
	// reader asking what his machines are spending on analysis is asking a
	// different question in the same place.
	//
	// It is present only for a record this machine produced and whose
	// receipt it can still read. §9 seals the worker's accounting before it
	// leaves the host, so a record another instance published carries no
	// cost here — and an absent block says nobody here can price it rather
	// than that it was free.
	Cost *costView `json:"cost,omitempty"`
}

func (m machineryView) empty() bool {
	return m.Revision == "" && m.Digest == "" && m.Schema == 0 && m.CreatedAt == "" &&
		m.RunID == "" && m.PolicyVersion == "" && m.Host == "" && len(m.Links) == 0 &&
		len(m.Receipts) == 0 && len(m.Revisions) == 0 && m.Cost == nil
}

// costView is what one run spent, as its receipt recorded it.
//
// USD is a pointer and the token counts are omitted when zero, which is the
// same rule the rest of this file applies to absence: an engine that never
// answered for its own session accounting reports nothing, and a receipt whose
// usage block is all zeros is that engine rather than a free run. The duration
// is Babel's own clock over the whole run — preparation, authorization and
// storage included — because that is the number a receipt states and a second,
// narrower one derived here would disagree with it.
type costView struct {
	USD          *float64 `json:"usd"`
	InputTokens  int64    `json:"input_tokens,omitempty"`
	OutputTokens int64    `json:"output_tokens,omitempty"`
	Model        string   `json:"model,omitempty"`
	DurationS    float64  `json:"duration_s,omitempty"`
}

func (c costView) empty() bool {
	return c.USD == nil && c.InputTokens == 0 && c.OutputTokens == 0 &&
		c.Model == "" && c.DurationS == 0
}

// machineryLink is one typed citation, as an identity and never as a URL.
// references.go states why: a link destination derived from text a record
// carries would make the citation graph an injection surface.
type machineryLink struct {
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	ID        string `json:"id"`
	// Title is the far record's own one line, present only when this host
	// holds the record. An edge naming another machine's candidate is the
	// expected case on a fleet-wide graph, and it renders as the identity it
	// is rather than as a blank line.
	Title string `json:"title,omitempty"`
}

// machineryReceipt is what reviewing this record was authorized to cost.
type machineryReceipt struct {
	ID    string `json:"id"`
	Stage string `json:"stage,omitempty"`
	// Cost is text because the three answers are not all numbers. A settled
	// attempt reports what it spent, a worker that could not price its own
	// run reports that it could not, and a grant with no result yet reports
	// what it reserved — and rendering the last two as `0.00` would say that
	// review of this record was free.
	Cost string `json:"cost,omitempty"`
	At   string `json:"at,omitempty"`
}

// machineryRevision is one entry of the record's chain, oldest first.
type machineryRevision struct {
	ID string `json:"id"`
	At string `json:"at,omitempty"`
}

// handleRecordPeel serves one record whole (#235).
func (s *Server) handleRecordPeel(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireService(w, s.opts.Frontier != nil, "the hypothesis frontier") {
		return
	}
	kind, known := kindOfRecordID(id)
	if !known {
		s.writeError(w, http.StatusBadRequest,
			"that identifier names no record kind this surface can open")
		return
	}
	wide, ok := s.fleetScope(w, r)
	if !ok {
		return
	}
	peel, err := s.peelRecord(r, wide, frontier.Ref{Type: kind, ID: id})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, peel)
}

// peelRecord assembles one record's five depths.
//
// The local store is asked first and the shared catalog only for an absence,
// which is detail.go's first rule: every other local failure is a durable
// store that broke, and a fallback that ran on one would answer a failed read
// with a record fetched from somewhere else.
func (s *Server) peelRecord(r *http.Request, wide bool, ref frontier.Ref) (recordPeel, error) {
	ctx := r.Context()
	peel := recordPeel{ID: ref.ID, Kind: string(ref.Type)}
	core, identity, err := s.localRecord(ctx, ref)
	if err != nil {
		if !errors.Is(err, frontier.ErrUnknownEntity) {
			return recordPeel{}, err
		}
		kind, convertible := catalogKind(ref.Type)
		found := catalogLookup{}
		if convertible {
			found = s.catalogRecord(r, wide, ref.ID, kind)
		}
		if !found.answerable() {
			return recordPeel{}, err
		}
		if !found.found {
			// The deployment holds the record and this surface could not
			// read it. The page says so and still names the record it
			// failed on, because a reader who clicked a row is owed an
			// answer about the row he clicked.
			peel.Notice = found.notice.SyncDetail
			return peel, nil
		}
		return s.peelCatalogRecord(ctx, ref, found)
	}
	peel.Title, peel.Claim = core.title, core.claim
	if !core.details.empty() {
		peel.Case = &core.details
	}
	// The session listing is read once for the whole document, because two
	// sections need it: an excerpt is quoted from a cited session and the
	// origin strip is about the first of them. Reading it twice would be two
	// enumerations of this host's catalog for one page.
	sessions := s.sessionsBySourceID(ctx)
	peel.Evidence = s.resolveEvidence(core.evidence, sessions)
	peel.Origin = recordOrigin(core.evidence, sessions)
	peel.Standing, peel.Action = s.standing(ctx, ref)
	peel.Reception = s.reception(ctx, ref)
	// The revision chain is read once and used twice, for the same reason:
	// depth five lists it and the related strip reads the supersession out
	// of it, and two reads of an append-only chain are two chances to render
	// a record as current in one section and replaced in another.
	chain, chainErr := s.opts.Frontier.Revisions(ctx, ref)
	if chainErr != nil {
		s.logf("record %s revision chain unread", ref.ID)
		chain = nil
	}
	peel.Related = s.related(ctx, ref, identity, chain)
	machinery := s.machinery(ctx, ref, identity, chain)
	// The deployment is asked what it holds of a record this machine already
	// answered for, which is one catalog row rather than the object: the
	// digest and the publishing host are the deployment's facts about this
	// record, and a page that carried them only for records read *through*
	// the catalog would show a reader less about his own machine's work than
	// about another machine's. A catalog that did not answer costs those two
	// fields and says so; it never costs the record.
	published, unreachable := s.catalogIdentity(r, wide, ref)
	machinery.Digest, machinery.Host = published.digest, published.host
	if unreachable {
		peel.Notice = recordLocalOnly
	}
	if !machinery.empty() {
		peel.Machinery = &machinery
	}
	return peel, nil
}

// recordLocalOnly is what the peel says when the deployment could not be
// consulted about a record this durable store already answered for.
//
// It names what is missing rather than what failed, on detail.go's terms: the
// reader asked about a record, and an outage report is an answer to a
// different question. What he loses is the published identity and what he
// keeps is every word of the record, so the sentence says both.
const recordLocalOnly = "the shared catalog did not answer, so this record's published identity " +
	"could not be read; every word below is the copy held here"

// catalogPublication is what the shared catalog says about a record this
// machine holds: the sealed object it published as, and the machine that
// published it.
type catalogPublication struct {
	digest string
	host   string
}

// catalogIdentity reads one record's catalog row, reporting whether the
// catalog could not be consulted.
//
// It reads the row and not the content, which is the difference between this
// and catalogRecord: the record is already in hand, and opening the sealed
// object again in order to learn its digest would decrypt a payload this
// route has already rendered from durable tables.
//
// An unconfigured fleet is not a degradation. There is no second place to
// look, so there is nothing the deployment failed to say — the same rule
// catalogRecord applies to the same state.
func (s *Server) catalogIdentity(r *http.Request, wide bool, ref frontier.Ref) (catalogPublication, bool) {
	kind, convertible := catalogKind(ref.Type)
	if !wide || !convertible {
		return catalogPublication{}, false
	}
	err := s.opts.FleetError
	var rows []fleet.Record
	if err == nil {
		rows, err = s.opts.Fleet.Records(r.Context(), sharedcatalog.RecordFilter{
			RecordIDs: []string{ref.ID},
			Kinds:     []sharedcatalog.RecordKind{kind},
			Limit:     1,
		})
	}
	switch {
	case fleet.NotConfigured(err):
		return catalogPublication{}, false
	case err != nil:
		s.logf("%s %s degraded: %s", r.Method, r.URL.Path, recordLocalOnly)
		return catalogPublication{}, true
	case len(rows) == 0:
		// Staged, or published by a run that has not committed. That is a
		// state rather than a failure and the fields are simply absent:
		// SPEC.md §9 keeps uncommitted output out of every fleet-wide read,
		// and a digest shown for a record the deployment cannot see yet
		// would claim a publication that has not happened.
		return catalogPublication{}, false
	}
	return catalogPublication{digest: rows[0].Record.ObjectDigest, host: rows[0].HostID}, false
}

// peelCatalogRecord renders a record this machine has never held.
//
// Four sections are thinner than a local read's and each absence is the same
// judgement detail.go's third rule makes. The standing, the ruling history and
// the revision chain are this machine's own derivations over records it holds,
// and rendering their zero values would say that a record nobody here has read
// has been decided nothing. The citation graph is local for the same reason.
// Reception is the exception and is read exactly as it is locally: the
// evaluation projection merges what the fleet published, so the votes on
// another instance's proposal are votes this instance has genuinely seen.
func (s *Server) peelCatalogRecord(ctx context.Context, ref frontier.Ref,
	found catalogLookup) (recordPeel, error) {
	published := found.record.Published
	peel := recordPeel{ID: ref.ID, Kind: string(ref.Type)}
	core, decoded := coreFromPayload(ref.Type, published.Payload)
	if !decoded {
		peel.Notice = recordUnopened
		return peel, nil
	}
	peel.Title, peel.Claim = core.title, core.claim
	if !core.details.empty() {
		peel.Case = &core.details
	}
	// The cited sessions may well be on this machine even though the record
	// is not: an instance that published a proposal read the same archived
	// conversations. So the excerpts and the origin are resolved exactly as
	// they are locally, and what this host cannot open is simply absent.
	sessions := s.sessionsBySourceID(ctx)
	peel.Evidence = s.resolveEvidence(core.evidence, sessions)
	peel.Origin = recordOrigin(core.evidence, sessions)
	peel.Reception = s.reception(ctx, ref)
	machinery := machineryView{
		Digest:    found.record.Record.ObjectDigest,
		Schema:    published.Schema,
		CreatedAt: timeText(published.CreatedAt),
		RunID:     published.RunID,
		Host:      found.record.HostID,
	}
	s.addEvaluationMachinery(ctx, ref, &machinery)
	if !machinery.empty() {
		peel.Machinery = &machinery
	}
	return peel, nil
}

// recordIdentity is the record's own plaintext half, shared by every kind.
type recordIdentity struct {
	runID     string
	schema    int
	createdAt time.Time
}

// recordCore is the part of the peel that comes out of the record's payload:
// the two sentences at depth one and two, and the material at depths two and
// three.
type recordCore struct {
	title    string
	claim    string
	details  caseView
	evidence []citedEvidence
}

// citedEvidence is one citation before its session has been resolved.
type citedEvidence struct {
	evidence frontier.Evidence
	kind     string
}

func (s *Server) localRecord(ctx context.Context, ref frontier.Ref) (recordCore, recordIdentity, error) {
	switch ref.Type {
	case frontier.EntityHypothesis:
		record, err := s.opts.Frontier.Hypothesis(ctx, ref.ID)
		if err != nil {
			return recordCore{}, recordIdentity{}, err
		}
		return hypothesisCore(record.Payload), recordIdentity{
			runID: record.RunID, schema: record.SchemaVersion, createdAt: record.CreatedAt}, nil
	case frontier.EntityObservation:
		record, err := s.opts.Frontier.Observation(ctx, ref.ID)
		if err != nil {
			return recordCore{}, recordIdentity{}, err
		}
		return observationCore(record.Payload), recordIdentity{
			runID: record.RunID, schema: record.SchemaVersion, createdAt: record.CreatedAt}, nil
	case frontier.EntityFinding:
		record, err := s.opts.Frontier.Finding(ctx, ref.ID)
		if err != nil {
			return recordCore{}, recordIdentity{}, err
		}
		return findingCore(record.Payload), recordIdentity{
			runID: record.RunID, schema: record.SchemaVersion, createdAt: record.CreatedAt}, nil
	case frontier.EntityProposal:
		record, err := s.opts.Frontier.Proposal(ctx, ref.ID)
		if err != nil {
			return recordCore{}, recordIdentity{}, err
		}
		return proposalCore(record.Payload), recordIdentity{
			runID: record.RunID, schema: record.SchemaVersion, createdAt: record.CreatedAt}, nil
	}
	return recordCore{}, recordIdentity{}, frontier.ErrUnknownEntity
}

// coreFromPayload reads a published payload into the same projections a local
// read produces, so a record resolved through the catalog reads identically to
// one this machine holds.
func coreFromPayload(kind frontier.EntityType, payload json.RawMessage) (recordCore, bool) {
	switch kind {
	case frontier.EntityHypothesis:
		var decoded frontier.HypothesisPayload
		if json.Unmarshal(payload, &decoded) != nil {
			return recordCore{}, false
		}
		return hypothesisCore(decoded), true
	case frontier.EntityObservation:
		var decoded frontier.ObservationPayload
		if json.Unmarshal(payload, &decoded) != nil {
			return recordCore{}, false
		}
		return observationCore(decoded), true
	case frontier.EntityFinding:
		var decoded frontier.FindingPayload
		if json.Unmarshal(payload, &decoded) != nil {
			return recordCore{}, false
		}
		return findingCore(decoded), true
	case frontier.EntityProposal:
		var decoded frontier.ProposalPayload
		if json.Unmarshal(payload, &decoded) != nil {
			return recordCore{}, false
		}
		return proposalCore(decoded), true
	}
	return recordCore{}, false
}

// hypothesisCore projects §4.2's candidate.
//
// It has no case and no evidence, and that is the record rather than a gap: a
// candidate is an idea somebody had, developed by the observations that cite
// it, and a page that manufactured a case section for one would be dressing a
// guess as an argument.
func hypothesisCore(payload frontier.HypothesisPayload) recordCore {
	return recordCore{title: payload.Statement, claim: payload.Statement}
}

// observationCore projects §4.3's provenance-bearing claim: the claim itself,
// its two gradings, and the citations that make it evidence at all.
func observationCore(payload frontier.ObservationPayload) recordCore {
	core := recordCore{
		title: payload.Claim,
		claim: payload.Claim,
		details: caseView{
			Impact:         string(payload.Impact),
			Classification: payload.Category,
		},
	}
	core.evidence = appendCitations(core.evidence, payload.Evidence, evidenceDirect)
	core.evidence = appendCitations(core.evidence, payload.CounterEvidence, evidenceCounter)
	return core
}

// findingCore projects §4.4's consolidation: what recurs is the claim, why it
// matters is the impact, and where it was seen is the scope.
//
// There is no problem and no outcome, because a finding proposes nothing. The
// remedy is a separate record with its own standing, which is the split that
// lets an operator accept the truth-claim and reject the change suggested with
// it.
func findingCore(payload frontier.FindingPayload) recordCore {
	core := recordCore{
		title: payload.Title,
		claim: payload.Pattern,
		details: caseView{
			Impact: payload.Significance,
			Scope:  strings.Join(payload.Scope, ", "),
		},
	}
	core.evidence = appendCitations(core.evidence, payload.CounterEvidence, evidenceCounter)
	return core
}

// proposalCore projects §4.5's suggested change, which is the one kind that
// fills the whole case.
//
// The claim is the proposed outcome rather than the problem: depth one is what
// a reader decides whether to care about, and what a proposal asks for is the
// change it wants, not the situation it describes. The problem is the first
// line of the case beneath it.
func proposalCore(payload frontier.ProposalPayload) recordCore {
	core := recordCore{
		title: payload.Title,
		claim: payload.Outcome,
		details: caseView{
			Problem:        payload.Problem,
			Outcome:        payload.Outcome,
			Impact:         string(payload.Impact),
			Scope:          payload.EstimatedScope,
			Classification: string(payload.Classification),
			Uncertainty:    payload.Uncertainty,
			Verification:   payload.VerificationCriteria,
			Risks:          payload.Risks,
			OpenQuestions:  payload.OpenQuestions,
			Prerequisites:  payload.Prerequisites,
		},
	}
	for _, target := range payload.Targets {
		core.details.Targets = append(core.details.Targets, targetView{
			System:     target.System,
			Rationale:  target.Rationale,
			Confidence: string(target.Confidence),
		})
	}
	core.evidence = appendCitations(core.evidence, payload.Supporting, evidenceSupporting)
	core.evidence = appendCitations(core.evidence, payload.Conflicting, evidenceConflicting)
	return core
}

func appendCitations(into []citedEvidence, items []frontier.Evidence, kind string) []citedEvidence {
	for _, item := range items {
		into = append(into, citedEvidence{evidence: item, kind: kind})
	}
	return into
}

// resolveEvidence turns citations into rows a reader can open, each carrying
// the cited text where this host can still recover it.
//
// The session listing arrives already read, because the origin strip is about
// the same rows and this host's catalog answers by enumeration: one pass for a
// document rather than one per section. A catalog that could not answer costs
// the links and the excerpts and nothing else — the locator is what makes a
// claim evidence (§4.3), and it travels whether or not this host can open the
// conversation it names.
func (s *Server) resolveEvidence(cited []citedEvidence, sessions map[string][]SessionRow) []evidenceView {
	if len(cited) == 0 {
		return nil
	}
	views := make([]evidenceView, 0, len(cited))
	for _, item := range cited {
		locator := item.evidence.Locator()
		view := evidenceView{
			Quote: item.evidence.Note(),
			Kind:  item.kind,
			Path:  locator.Path,
			Line:  locator.Line,
		}
		row, ok := matchSession(sessions, locator.Path)
		if ok {
			view.SessionID = row.Selector
			if locator.Line > 0 {
				view.Event = locator.Line - 1
			}
			view.Href = sessionHref(row.Selector, view.Event)
			if row.Title != nil {
				view.SessionTitle = boundedLine(*row.Title)
			}
			view.Excerpt, view.Speaker = s.citedExcerpt(locator, row.Harness)
		}
		views = append(views, view)
	}
	return views
}

// citedExcerpt recovers the cited record's own text and who said it.
//
// It is named apart from review.go's excerpt because the two quote different
// things: that one is a record's own first line, read out of the frontier for
// a listing, and this one is the conversation the record cites, read out of a
// session log at a locator.
//
// The read is one seek because the locator says where the record starts and
// what it hashes to (transcript.Cited). What this function adds is the
// judgement about what may be shown: a record the display parser could not
// read is a raw log line, and a raw log line rendered as a pull-quote is a
// page quoting JSON at a reader who came to check a citation. So an
// unrecognized record produces no excerpt, exactly as an unreadable file does,
// and the note beside it plus the transcript link stay as they were.
//
// A failure is logged and not returned. The evidence row is still correct
// without an excerpt, and a record page that failed because one of nine cited
// logs had been rotated would be a page lost to a fact it was reporting.
func (s *Server) citedExcerpt(locator event.Locator, harnessName string) (string, string) {
	if locator.ByteOffset <= 0 && locator.Line > 1 {
		// Nothing to seek to: a zero offset is only the file's first record,
		// and a locator whose line says otherwise is one this surface
		// cannot place without the pass it exists to avoid.
		return "", ""
	}
	cited, found, err := transcript.Cited(locator.Path, harnessName, locator.ByteOffset, locator.Digest)
	if err != nil {
		s.logf("citation at %s line %d unread", fileStem(locator.Path), locator.Line)
		return "", ""
	}
	if !found || cited.Kind != transcriptMessage {
		return "", ""
	}
	text := strings.TrimSpace(cited.Text)
	if text == "" {
		return "", ""
	}
	return boundedText(text, maxExcerpt), speakerOf(cited.Role)
}

// transcriptMessage is the one event kind an excerpt may quote: a record the
// harness's display parser read as a message. internal/transcript's other kind
// is `raw`, which is a log line it could not read.
const transcriptMessage = "message"

// speakerOf translates a harness's own role name into the three a reader
// needs. An unrecognized role is absent rather than guessed: a label invented
// here would attribute the quotation to whoever the guess named.
func speakerOf(role string) string {
	switch lowered := strings.ToLower(role); {
	case lowered == speakerUser:
		return speakerUser
	case lowered == speakerAssistant:
		return speakerAssistant
	case strings.Contains(lowered, speakerTool):
		// `tool`, `toolResult`, `tool_use`: the harnesses spell a tool's
		// turn several ways and a reader needs none of the spellings.
		return speakerTool
	}
	return ""
}

// boundedText collapses one quotation to at most limit bytes, cutting on a
// rune boundary and preferring the last word boundary before it: a quotation
// cut mid-word reads as a transcription error rather than as an excerpt.
func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	if space := strings.LastIndexAny(value[:cut], " \t\n"); space > limit/2 {
		cut = space
	}
	return strings.TrimRight(value[:cut], " \t\n") + "…"
}

// recordOrigin is the conversation a record was born in: the first citation
// whose session this host holds.
//
// First rather than most-cited or newest, because the ordering it comes from
// is the record's own — appendCitations walks supporting evidence before
// conflicting, and a record's first citation is the material it was written
// from. A record whose citations all name sessions this machine has never had
// gets no origin strip at all, which is the honest answer: the conversation
// exists and this host cannot say anything about it.
func recordOrigin(cited []citedEvidence, sessions map[string][]SessionRow) *originView {
	for _, item := range cited {
		locator := item.evidence.Locator()
		row, ok := matchSession(sessions, locator.Path)
		if !ok {
			continue
		}
		view := &originView{
			SessionID: row.Selector,
			CostUSD:   row.CostUSD,
			Turns:     row.Turns,
		}
		if row.Title != nil {
			view.SessionTitle = boundedLine(*row.Title)
		}
		if row.Workspace != nil {
			view.Workspace = *row.Workspace
		}
		if row.Modified != nil {
			view.At = *row.Modified
		}
		event := 0
		if locator.Line > 0 {
			event = locator.Line - 1
		}
		view.Href = sessionHref(row.Selector, event)
		return view
	}
	return nil
}

// sessionsBySourceID indexes this host's catalog by the cited file's own name.
//
// The name is the key because it is the part of the path that survives a
// session being materialized somewhere else; the candidate's full source id is
// then checked against the path, so the key is a lookup and never the proof.
// It is the same two-step the record pages have always performed in the
// browser, moved to the one place that can resolve the route before the
// document is sent.
func (s *Server) sessionsBySourceID(ctx context.Context) map[string][]SessionRow {
	if s.opts.Lister == nil {
		return nil
	}
	listed, err := s.opts.Lister.ListSessions(ctx)
	if err != nil {
		return nil
	}
	index := make(map[string][]SessionRow, len(listed.Sessions))
	for _, row := range listed.Sessions {
		key := fileStem(row.SourceID)
		index[key] = append(index[key], row)
	}
	return index
}

// matchSession resolves one locator path to the session row it names, which
// carries both the selector the transcript routes on and the harness whose
// language the cited record is written in.
func matchSession(index map[string][]SessionRow, locatorPath string) (SessionRow, bool) {
	if len(index) == 0 || locatorPath == "" {
		return SessionRow{}, false
	}
	stripped := strings.TrimSuffix(locatorPath, path.Ext(locatorPath))
	for _, row := range index[fileStem(locatorPath)] {
		if row.SourceID != "" && strings.HasSuffix(stripped, row.SourceID) {
			return row, true
		}
	}
	return SessionRow{}, false
}

// fileStem is a path's last element without its extension.
func fileStem(value string) string {
	name := value[strings.LastIndexByte(value, '/')+1:]
	return strings.TrimSuffix(name, path.Ext(name))
}

// sessionHref is the route that opens a transcript at a cited record.
//
// It is the one place this surface emits a URL, and it is safe to for the
// reason references.go refuses to elsewhere: every part of it is an identity
// this server resolved out of its own catalog, and no byte of a record's text
// reaches it.
func sessionHref(selector string, event int) string {
	return fmt.Sprintf("#/sessions/%s?event=%d", url.PathEscape(selector), event)
}

// standing reads where a record stands and what it wants next.
//
// Both are absent for a record §6.7 does not make reviewable. An observation
// is the evidence a finding consolidates, not an artifact anybody accepts or
// rejects, and a page that offered a ruling control on one would be offering
// an act the review service refuses.
func (s *Server) standing(ctx context.Context, ref frontier.Ref) (*standingView, *askView) {
	if !reviewableRecord(ref.Type) {
		return nil, nil
	}
	status, err := s.opts.Frontier.ReviewStatus(ctx, ref)
	if err != nil {
		// The derivation failed rather than answered. A standing invented
		// here would be a ruling nobody made, so the section is absent and
		// the page renders a record whose standing it could not read.
		return nil, nil
	}
	label := string(status)
	if status == frontier.ReviewNew && s.lastRulingReopened(ctx, ref) {
		label = standingReopened
	}
	if s.supersededRecord(ctx, ref) {
		label = standingSuperseded
	}
	view := &standingView{Label: label, Tone: standingTone(label)}
	if label == string(frontier.ReviewNew) || label == standingReopened {
		return view, &askView{Verb: "rule", Label: "Rule on this"}
	}
	return view, nil
}

// related assembles the connections a record's own words do not state.
//
// Every one of them is a read of a relation somebody or something already
// asserted, and none of them is computed here. The competing remedies are
// #114's stored relation, the suspected duplicates are what a dedup heuristic
// recorded when the candidate was written and what a triage pass read as the
// same thing, the supersessions are the revision chain plus the typed links,
// and the siblings are the run id every record has carried since the
// frontier's first migration. A strip with nothing in it is absent, which is
// the honest answer for a record that stands alone.
//
// A relation this surface could not read is skipped rather than reported. The
// strip is context beside the record, so a store that would not answer costs
// one line of it; a page that failed because a duplicate warning could not be
// read would lose the record over its footnote.
func (s *Server) related(ctx context.Context, ref frontier.Ref, identity recordIdentity,
	chain []frontier.Revision) *relatedView {
	view := relatedView{
		Addressing: s.competingRemedies(ctx, ref),
		Duplicates: s.suspectedDuplicates(ctx, ref),
		Siblings:   s.runSiblings(ctx, ref, identity.runID),
	}
	view.Supersedes, view.SupersededBy = s.supersession(ctx, ref, chain)
	if len(view.Addressing) == 0 && len(view.Duplicates) == 0 && len(view.Supersedes) == 0 &&
		len(view.SupersededBy) == 0 && len(view.Siblings) == 0 {
		return nil
	}
	return &view
}

// competingRemedies lists the other proposals offered for the same claim.
//
// A candidate's remedies are read directly; a proposal's competitors are the
// remedies of the claims it addresses, minus itself. That asymmetry is #114's:
// the relation is stored from remedy to claim, so "what else answers this
// problem" is one query from a hypothesis and one query per addressed
// hypothesis from a proposal.
//
// A finding has none, and that is the split §4.4 draws rather than a gap: a
// finding proposes nothing, so nothing competes with it. The remedies are
// reached from the candidates its observations developed, which is the
// candidates' own page.
func (s *Server) competingRemedies(ctx context.Context, ref frontier.Ref) []relatedRecord {
	var claims []string
	switch ref.Type {
	case frontier.EntityHypothesis:
		claims = []string{ref.ID}
	case frontier.EntityProposal:
		record, err := s.opts.Frontier.Proposal(ctx, ref.ID)
		if err != nil {
			return nil
		}
		claims = record.HypothesisIDs
	default:
		return nil
	}
	seen := make(map[string]struct{}, len(claims))
	var out []relatedRecord
	for _, claim := range claims {
		remedies, err := s.opts.Frontier.ProposalsAddressing(ctx, claim)
		if err != nil {
			s.logf("record %s competing remedies unread", ref.ID)
			continue
		}
		for _, remedy := range remedies {
			if remedy.ID == ref.ID {
				continue
			}
			if _, dup := seen[remedy.ID]; dup {
				continue
			}
			seen[remedy.ID] = struct{}{}
			out = append(out, relatedRecord{
				ID:       remedy.ID,
				Kind:     string(frontier.EntityProposal),
				Title:    boundedLine(remedy.Payload.Title),
				Standing: string(remedy.ReviewStatus),
			})
		}
	}
	return out
}

// suspectedDuplicates lists what Babel suspects this record restates.
//
// Two sources, because two different things in Babel form the suspicion. A
// candidate carries the overlap its dedup heuristic measured when it was
// written, stored beside it and — until now — served to nobody; a proposal
// carries the cluster a triage pass read as saying the same thing, which is a
// judgement rather than a measurement and therefore travels without a number.
// Neither is a ruling: §4.7's `duplicate` disposition is the operator's, and
// this is the comparison being put in front of him.
func (s *Server) suspectedDuplicates(ctx context.Context, ref frontier.Ref) []relatedRecord {
	switch ref.Type {
	case frontier.EntityHypothesis:
		record, err := s.opts.Frontier.Hypothesis(ctx, ref.ID)
		if err != nil {
			return nil
		}
		out := make([]relatedRecord, 0, len(record.Duplicates))
		for _, warning := range record.Duplicates {
			out = append(out, relatedRecord{
				ID:      warning.DuplicateOf,
				Kind:    string(frontier.EntityHypothesis),
				Title:   s.titleOf(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: warning.DuplicateOf}),
				Overlap: warning.Overlap,
			})
		}
		return out
	case frontier.EntityProposal:
		advice, err := s.opts.Frontier.TriageAdvice(ctx, ref.ID)
		if err != nil {
			s.logf("record %s triage advice unread", ref.ID)
			return nil
		}
		seen := make(map[string]struct{})
		var out []relatedRecord
		for _, entry := range advice {
			for _, peer := range entry.Cluster {
				if peer == ref.ID {
					continue
				}
				if _, dup := seen[peer]; dup {
					continue
				}
				seen[peer] = struct{}{}
				out = append(out, relatedRecord{
					ID:    peer,
					Kind:  string(frontier.EntityProposal),
					Title: s.titleOf(ctx, frontier.Ref{Type: frontier.EntityProposal, ID: peer}),
				})
			}
		}
		return out
	}
	return nil
}

// supersession reports the wording this record replaced and the wording that
// replaced it.
//
// The revision chain is the source for every kind, because that is where #87
// puts supersession: a chain has one leaf, the entry before this record is
// what it revised and the entry after is what revised it. A candidate's typed
// links add the cross-chain case the chain cannot carry — one investigator
// asserting that a different candidate supersedes this one — and they are
// unioned rather than preferred, since a record can be both revised and
// replaced by another line of thought.
func (s *Server) supersession(ctx context.Context, ref frontier.Ref,
	chain []frontier.Revision) (before, after []relatedRecord) {
	for i, entry := range chain {
		if entry.Entity.ID != ref.ID {
			continue
		}
		if i > 0 {
			before = append(before, s.relatedRef(ctx, chain[i-1].Entity))
		}
		if i+1 < len(chain) {
			after = append(after, s.relatedRef(ctx, chain[i+1].Entity))
		}
		break
	}
	if ref.Type != frontier.EntityHypothesis {
		return before, after
	}
	if links, err := s.opts.Frontier.LinksFrom(ctx, ref.ID); err == nil {
		for _, link := range links {
			if link.Type == frontier.LinkSupersedes {
				before = append(before, s.relatedRef(ctx,
					frontier.Ref{Type: frontier.EntityHypothesis, ID: link.ToID}))
			}
		}
	}
	if links, err := s.opts.Frontier.LinksTo(ctx, ref.ID); err == nil {
		for _, link := range links {
			if link.Type == frontier.LinkSupersedes {
				after = append(after, s.relatedRef(ctx,
					frontier.Ref{Type: frontier.EntityHypothesis, ID: link.FromID}))
			}
		}
	}
	return before, after
}

// runSiblings lists the rest of what this record's run wrote.
//
// Head revisions only and this record excluded, both from the store's own
// query: a siblings list that included the record a reader is looking at would
// be offering him a link to the page he is on.
func (s *Server) runSiblings(ctx context.Context, ref frontier.Ref, runID string) []relatedRecord {
	if runID == "" {
		return nil
	}
	outputs, err := s.opts.Frontier.OutputsOfRun(ctx, runID)
	if err != nil {
		s.logf("record %s run siblings unread", ref.ID)
		return nil
	}
	out := make([]relatedRecord, 0, len(outputs))
	for _, output := range outputs {
		if output.ID == ref.ID {
			continue
		}
		out = append(out, relatedRecord{
			ID:    output.ID,
			Kind:  string(output.Kind),
			Title: boundedLine(output.Title),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// relatedRef is one record named by identity alone, with its own line read
// where this host holds it.
func (s *Server) relatedRef(ctx context.Context, ref frontier.Ref) relatedRecord {
	return relatedRecord{ID: ref.ID, Kind: string(ref.Type), Title: s.titleOf(ctx, ref)}
}

// titleOf is one record's own bounded line, empty when this host holds no such
// record — which on a fleet-wide graph is the expected case rather than a
// failure, exactly as it is for a link at depth five.
func (s *Server) titleOf(ctx context.Context, ref frontier.Ref) string {
	core, _, err := s.localRecord(ctx, ref)
	if err != nil {
		return ""
	}
	return boundedLine(core.title)
}

// reviewableRecord mirrors §6.7's reviewable set. It is spelled here rather
// than asked of internal/review because this is a read: asking the service
// would cost a store round trip to learn a fact about a kind.
func reviewableRecord(kind frontier.EntityType) bool {
	switch kind {
	case frontier.EntityHypothesis, frontier.EntityFinding, frontier.EntityProposal:
		return true
	}
	return false
}

func standingTone(label string) string {
	switch label {
	case string(frontier.ReviewAccepted):
		return toneGood
	case string(frontier.ReviewRejected):
		return toneBad
	case string(frontier.ReviewDeferred), string(frontier.ReviewRefineRequested), standingReopened:
		return toneWarn
	default:
		return toneNeutral
	}
}

// lastRulingReopened reports whether this record is undecided because somebody
// lifted a decision rather than because nobody has made one.
func (s *Server) lastRulingReopened(ctx context.Context, ref frontier.Ref) bool {
	if s.opts.Review == nil {
		return false
	}
	history, err := s.opts.Review.History(ctx, ref)
	if err != nil || len(history.Decisions) == 0 {
		return false
	}
	last := history.Decisions[len(history.Decisions)-1]
	return last.Event.Disposition == frontier.DispositionReopen
}

// supersededRecord reports that a later revision replaced the wording being
// read.
func (s *Server) supersededRecord(ctx context.Context, ref frontier.Ref) bool {
	chain, err := s.opts.Frontier.Revisions(ctx, ref)
	if err != nil || len(chain) == 0 {
		return false
	}
	return chain[len(chain)-1].Entity.ID != ref.ID
}

// reception assembles depth four.
//
// Every part of it is read from internal/evaluation and internal/review rather
// than derived here, and the two are not merged: the rulings are §4.7's
// append-only authority and the votes are §4.12's reception, which that
// section keeps separate precisely because a tally is not a decision.
func (s *Server) reception(ctx context.Context, ref frontier.Ref) *receptionView {
	view := receptionView{}
	if s.opts.Evaluation != nil {
		subject := evaluation.Subject{Kind: string(ref.Type), ID: ref.ID}
		if detail, err := s.opts.Evaluation.Detail(ctx, subject); err == nil {
			roles := rolesByAssignment(detail.Assignments)
			view.Operator, view.History = operatorStances(detail.History)
			view.Model = modelStances(detail.History, roles)
			counts := receptionCounts{
				Support: detail.Item.Reception.Support,
				Oppose:  detail.Item.Reception.Oppose,
				Unsure:  detail.Item.Reception.Unsure,
			}
			if !counts.empty() {
				view.Counts = &counts
			}
			view.ByRole = roleReceptions(detail.History, roles)
			view.Contested = contestedRoles(view.ByRole)
		}
	}
	if s.opts.Review != nil && reviewableRecord(ref.Type) {
		if history, err := s.opts.Review.History(ctx, ref); err == nil {
			for _, entry := range history.Decisions {
				view.Decisions = append(view.Decisions, receptionDecisionView{
					Disposition: string(entry.Event.Disposition),
					By:          entry.Event.ReviewerID,
					At:          timeText(entry.Event.RecordedAt),
					Note:        entry.Event.Payload.Note,
				})
			}
		}
	}
	if view.Operator == nil && len(view.History) == 0 && len(view.Model) == 0 &&
		len(view.Decisions) == 0 && view.Counts == nil {
		return nil
	}
	return &view
}

// roleReceptions groups Babel's reviewers by what each was asked.
//
// The join is §4.12's own: a vote's authority comes from the grant that drew
// it, so the role is read off the assignment rather than off the assessment,
// and a vote whose grant this instance cannot see is left out of the grouping
// rather than credited to a role it might not have held. Its own line still
// renders beside the others, roleless, in the reviewer list above.
//
// The order is the roles' first appearance in the history, which is the order
// the reviews were drawn in. Sorting alphabetically would put `challenge`
// before `evidence` and read as a ranking of questions.
func roleReceptions(history []evaluation.Record, roles map[string]string) []roleReceptionView {
	index := make(map[string]int, len(roles))
	var out []roleReceptionView
	for _, record := range history {
		if record.Kind != evaluation.KindAssessment || record.Assessment == nil {
			continue
		}
		if record.ActorKind != evaluation.ActorRun {
			continue
		}
		role := roles[record.AssignmentID]
		if role == "" {
			continue
		}
		position, seen := index[role]
		if !seen {
			position = len(out)
			index[role] = position
			out = append(out, roleReceptionView{Role: role})
		}
		switch record.Assessment.Vote {
		case voteSupport:
			out[position].Support++
		case voteOppose:
			out[position].Oppose++
			if rationale := assessmentRationale(*record.Assessment); rationale != "" {
				out[position].OpposingRationales = append(out[position].OpposingRationales, rationale)
			}
		case voteUnsure:
			out[position].Unsure++
		}
	}
	return out
}

// The three votes §4.12 gives a reviewer, spelled here because this file
// groups by them. internal/evaluation owns the vocabulary and refuses anything
// outside it; a vote this build does not recognize is counted in no column
// rather than counted as support.
const (
	voteSupport = "support"
	voteOppose  = "oppose"
	voteUnsure  = "unsure"
)

// contestedRoles reports disagreement where it means something: one role with
// both a support and an opposition in it, which is two reviewers answering the
// same question differently. Support on one question beside opposition on
// another is two reviewers agreeing about different things, and calling that
// contested would make almost every reviewed record contested.
func contestedRoles(byRole []roleReceptionView) bool {
	for _, role := range byRole {
		if role.Support > 0 && role.Oppose > 0 {
			return true
		}
	}
	return false
}

// operatorStances splits the operator's reception into the current one and the
// ones it replaced.
//
// Current is the newest stance and not the newest feedback record: §4.12 lets
// a scoped reason carry no position at all — "not now, the benchmark lands
// first" takes no side — and a later reason without one does not withdraw an
// earlier agreement. Only another stance does that, and the earlier stance
// stays readable because nothing here is ever rewritten.
func operatorStances(history []evaluation.Record) (*operatorStanceView, []operatorStanceView) {
	var stances []operatorStanceView
	for _, record := range history {
		if record.Kind != evaluation.KindFeedback || record.ActorKind != evaluation.ActorOperator {
			continue
		}
		if record.Stance == "" {
			continue
		}
		stances = append(stances, operatorStanceView{
			Stance: record.Stance,
			Reason: record.Reason,
			At:     timeText(record.CreatedAt),
		})
	}
	if len(stances) == 0 {
		return nil, nil
	}
	// internal/evaluation answers a subject's history oldest first, so the
	// current stance is the last of them and the rest reverse into
	// newest-first order behind it.
	current := stances[len(stances)-1]
	earlier := make([]operatorStanceView, 0, len(stances)-1)
	for i := len(stances) - 2; i >= 0; i-- {
		earlier = append(earlier, stances[i])
	}
	if len(earlier) == 0 {
		return &current, nil
	}
	return &current, earlier
}

// modelStances renders Babel's own reviewers.
//
// Only run-authored assessments appear, which is the authority boundary made
// visible: an operator cannot mint one (OperatorKinds excludes the kind) and
// this projection would not show it as a model's if he could.
//
// The role comes off the grant rather than off the assessment, because that
// is where §4.12 puts it: a role is what a reviewer was authorized to answer,
// not something the answer claims about itself. An assessment whose grant
// this instance cannot see is still a record and still renders, with no role
// — crediting it to reception by default is exactly how a bare vote would
// come to look like a satisfied evidence check.
func modelStances(history []evaluation.Record, roles map[string]string) []modelStanceView {
	var out []modelStanceView
	for _, record := range history {
		if record.Kind != evaluation.KindAssessment || record.Assessment == nil {
			continue
		}
		if record.ActorKind != evaluation.ActorRun {
			continue
		}
		out = append(out, modelStanceView{
			Actor:     record.ActorID,
			Role:      roles[record.AssignmentID],
			Stance:    record.Assessment.Vote,
			Rationale: assessmentRationale(*record.Assessment),
			At:        timeText(record.CreatedAt),
		})
	}
	return out
}

// rolesByAssignment indexes what each grant authorized its worker to answer.
func rolesByAssignment(assignments []evaluation.Assignment) map[string]string {
	roles := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		roles[assignment.ID] = assignment.Role
	}
	return roles
}

// assessmentRationale is what a reviewer said, in its own words.
//
// A bare vote carries none and none is invented for it: §E5 permits a
// reception with no rationale, and a surface that filled the gap would be
// writing the reviewer's argument for him.
func assessmentRationale(assessment evaluation.Assessment) string {
	for _, contribution := range assessment.Contributions {
		if text := strings.TrimSpace(contribution.Text); text != "" {
			return text
		}
	}
	return ""
}

// machinery assembles depth five for a record this machine holds.
//
// The revision chain arrives already read, because the related strip reads the
// supersession out of the same chain: one read of an append-only history
// cannot disagree with itself.
func (s *Server) machinery(ctx context.Context, ref frontier.Ref,
	identity recordIdentity, chain []frontier.Revision) machineryView {
	view := machineryView{
		Schema:    identity.schema,
		CreatedAt: timeText(identity.createdAt),
		RunID:     identity.runID,
	}
	for _, revision := range chain {
		view.Revisions = append(view.Revisions, machineryRevision{
			ID: revision.Entity.ID,
			At: timeText(revision.RecordedAt),
		})
	}
	if len(chain) > 0 {
		view.Revision = chain[len(chain)-1].Entity.ID
	}
	view.Links = s.machineryLinks(ctx, ref)
	view.Cost = s.runCost(ctx, identity.runID)
	s.addEvaluationMachinery(ctx, ref, &view)
	return view
}

// runCost reads what producing this record cost, out of the producing run's
// own receipt.
//
// It is the newest revision of the receipt because a receipt is amended rather
// than edited: revision 2 exists to correct revision 1, and a page showing the
// first figure would show the number its own store has already superseded.
//
// Everything here is local by construction. §9 seals the worker's accounting
// before a receipt leaves the machine, so the body this reads is plaintext
// only on the host that wrote it — which is also the only host that can be
// asked what its own analysis cost. A record from elsewhere, a receipt this
// build cannot open, a run that never reached the worker: all three produce no
// cost block, because none of them is a free run.
func (s *Server) runCost(ctx context.Context, runID string) *costView {
	if s.opts.Receipts == nil || runID == "" {
		return nil
	}
	receipts, err := s.opts.Receipts.Revisions(ctx, runID)
	if err != nil || len(receipts) == 0 {
		return nil
	}
	body := receipts[len(receipts)-1].Body
	view := costView{}
	if seconds := body.Timing.Duration().Seconds(); seconds > 0 {
		view.DurationS = seconds
	}
	if body.Worker != nil {
		// The model is the resolved one Code reported rather than the one a
		// profile asked for (§7): what a reader wants to know is which model
		// wrote this, and a profile names a preference.
		view.Model = body.Worker.Metadata["model"]
		if usage := body.Worker.Usage; usage != nil {
			view.InputTokens, view.OutputTokens = usage.InputTokens, usage.OutputTokens
			if usage.Cost > 0 {
				// Zero is the engine declining to price its own session,
				// which every receipt in this corpus with no usage report
				// records as a zero — so a zero renders as unknown and
				// never as free.
				cost := usage.Cost
				view.USD = &cost
			}
		}
	}
	if view.empty() {
		return nil
	}
	return &view
}

// machineryLinks renders the typed citation graph as a depth-five index.
//
// It is the graph's shape rather than the citation browser: /api/record/links
// answers that question with its own paging and its own inert reasons. What
// this block is for is a reader who is debugging and wants to see, in one
// place, every identity this record is attached to.
func (s *Server) machineryLinks(ctx context.Context, ref frontier.Ref) []machineryLink {
	if s.opts.References == nil {
		return nil
	}
	subject := frontierSubject(ref)
	cites, err := s.opts.References.From(ctx, subject)
	if err != nil {
		s.logf("record %s links unread", ref.ID)
		return nil
	}
	citedBy, err := s.opts.References.To(ctx, subject)
	if err != nil {
		s.logf("record %s backlinks unread", ref.ID)
		return nil
	}
	titles := make(map[reference.RecordRef]string, len(cites)+len(citedBy))
	links := make([]machineryLink, 0, len(cites)+len(citedBy))
	for _, edge := range cites {
		links = append(links, s.machineryLink(ctx, edge, edge.To, "to", titles))
	}
	for _, edge := range citedBy {
		links = append(links, s.machineryLink(ctx, edge, edge.From, "from", titles))
	}
	return links
}

// machineryLink resolves one endpoint, memoizing titles: a record at the far
// side of four edges costs one lookup rather than four.
func (s *Server) machineryLink(ctx context.Context, edge reference.Edge, other reference.RecordRef,
	direction string, titles map[reference.RecordRef]string) machineryLink {
	title, resolved := titles[other]
	if !resolved {
		title = s.recordTitle(ctx, other)
		titles[other] = title
	}
	return machineryLink{
		Kind:      string(edge.Kind),
		Direction: direction,
		ID:        other.ID,
		Title:     title,
	}
}

// recordTitle is the far record's one bounded line, empty when this host holds
// no such record. An edge naming another machine's candidate is the expected
// case on a fleet-wide graph (references.go), not a failure.
//
// It is bounded where the record's own claim above is not, and the two are
// different things: a reader opened this record to read its words, and the
// link index is a list of other records. An observation's claim runs to
// several hundred characters, this block routinely holds hundreds of rows,
// and a depth nobody has expanded must not be the largest part of the
// document.
func (s *Server) recordTitle(ctx context.Context, other reference.RecordRef) string {
	kind, known := refKind(other.Kind)
	if !known {
		return ""
	}
	core, _, err := s.localRecord(ctx, frontier.Ref{Type: kind, ID: other.ID})
	if err != nil {
		return ""
	}
	return boundedLine(core.title)
}

// maxLinkTitle bounds one line of an index. It is internal/reference's own
// summary bound, which is what makes a link row here the height of the same
// record's row in the citation listing beside it.
const maxLinkTitle = 240

// boundedLine collapses model-authored prose to one line of at most
// maxLinkTitle bytes. The cut lands on a rune boundary because half a rune is
// not a character, and the ellipsis says the line was cut rather than that the
// record trailed off.
func boundedLine(value string) string {
	line := strings.TrimSpace(value)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if len(line) <= maxLinkTitle {
		return line
	}
	cut := maxLinkTitle
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return strings.TrimRight(line[:cut], " ") + "…"
}

// addEvaluationMachinery fills in what reviewing this record was authorized
// under and what it was authorized to cost.
//
// The policy version is the newest grant's rather than the deployment's
// current one, because the question depth five answers is which settings this
// record's reviews were drawn under — and the deployment's current policy is
// already readable on the settings surface.
func (s *Server) addEvaluationMachinery(ctx context.Context, ref frontier.Ref, into *machineryView) {
	if s.opts.Evaluation == nil {
		return
	}
	detail, err := s.opts.Evaluation.Detail(ctx, evaluation.Subject{Kind: string(ref.Type), ID: ref.ID})
	if err != nil {
		return
	}
	settled := settledAttempts(detail.History)
	for _, assignment := range detail.Assignments {
		into.Receipts = append(into.Receipts, machineryReceipt{
			ID:    assignment.ID,
			Stage: assignment.Role,
			Cost:  receiptCost(assignment, settled[assignment.ID]),
			At:    timeText(assignment.CreatedAt),
		})
		if assignment.PolicyVersion != "" {
			into.PolicyVersion = assignment.PolicyVersion
		}
	}
}

// settledAttempts indexes what each grant actually spent, by assignment.
func settledAttempts(history []evaluation.Record) map[string]evaluation.Attempt {
	settled := make(map[string]evaluation.Attempt)
	for _, record := range history {
		if record.Attempt == nil {
			continue
		}
		settled[record.Attempt.AssignmentID] = *record.Attempt
	}
	return settled
}

// receiptCost states what a grant cost, distinguishing the three answers a
// number alone would flatten.
func receiptCost(assignment evaluation.Assignment, attempt evaluation.Attempt) string {
	switch {
	case attempt.Unpriced:
		return "unpriced"
	case attempt.State != "":
		return fmt.Sprintf("$%.2f", attempt.Cost)
	default:
		return fmt.Sprintf("$%.2f reserved", assignment.ReservedCost)
	}
}

// receptionRequest is POST /api/record/{id}/reception's body: the operator's
// position, and optionally why.
//
// The stance is a closed vocabulary and the reason is free text, which is the
// same split §4.12 draws everywhere else: a position a reader would have to
// infer from prose is a position each reader infers differently, and a reason
// Babel classified would no longer be the operator's own words.
type receptionRequest struct {
	Stance string `json:"stance"`
	Reason string `json:"reason"`
}

// receptionResult confirms what was recorded and when.
//
// It echoes the stored record's stance rather than the request's, so an act
// the service refused or rewrote cannot be reported back as the one that was
// sent.
type receptionResult struct {
	Stance string `json:"stance"`
	At     string `json:"at"`
}

// handleRecordReception records the operator's own reception of a record
// (#235 §3).
//
// It is an operator-authored feedback record and nothing else. §4.12's
// authority boundary does not move to make room for it: OperatorKinds still
// excludes an assessment, so this write cannot mint what reads as a model's
// observation, and it sets no disposition — agreeing is not accepting, and the
// accept/reject/defer vocabulary stays behind internal/review's confirmation.
//
// Recording a second stance is a second record. §4.12 is append-only, so a
// changed mind leaves both readable in order, which is why the peel's
// reception carries a history beside the current position.
func (s *Server) handleRecordReception(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireService(w, s.opts.Evaluation != nil, evaluationServiceName) {
		return
	}
	kind, known := kindOfRecordID(id)
	if !known {
		s.writeError(w, http.StatusBadRequest,
			"that identifier names no record kind this surface can open")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	var request receptionRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	// The stance is passed through unchecked, on handleEvaluationOperator's
	// terms: internal/evaluation owns the vocabulary and refuses a value
	// outside it, and a second gate here is a second place for the two to
	// come to disagree about what an operator may say.
	record, refresh, err := s.opts.Evaluation.OperatorDeferred(r.Context(), evaluation.OperatorInput{
		Subject:  evaluation.Subject{Kind: string(kind), ID: id},
		Kind:     evaluation.KindFeedback,
		Operator: by.ID(),
		Reason:   request.Reason,
		Stance:   request.Stance,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	// The refresh is started before the response and waited on only briefly.
	// A reader who agrees and then reloads must see his own stance, and a
	// reader whose machine is busy must not wait on bookkeeping to find out
	// that it was recorded; those are both true of a refresh that gets a
	// short head start and then stops being his problem.
	s.refreshReception(record.Subject, refresh)
	s.writeJSON(w, http.StatusOK, receptionResult{
		Stance: record.Stance,
		At:     timeText(record.CreatedAt),
	})
}

// refreshReception brings the evaluation projection up to date with a stance
// that has already been recorded, and gives the response a deadline rather
// than the work.
//
// The two halves of the write are separated because only one of them is the
// act. The durable record is the operator's position the instant its
// transaction commits; the projection is a rebuildable cache, and refreshing
// it reads this instance's evaluation records, assignments, attempts, the
// subject's artifact and the effective policy in order to replace one row —
// six of the six and a half seconds an upvote took on the live catalog, spent
// after the thing the operator asked for was already true.
//
// So the refresh runs on its own goroutine, under a context of its own, and
// the handler waits for it for as long as an instant lasts. That ordering is
// what keeps both properties: a reload right after clicking shows the stance,
// because on an idle machine the refresh finishes in tens of milliseconds and
// the response waits for it; and a machine with three lanes writing does not
// make the operator watch a projection catch up, because the wait expires and
// the work continues without him.
//
// The work is never cancelled by the wait expiring. A refresh abandoned
// halfway is the one outcome worse than a refresh that is late, so the
// goroutine holds a context detached from the request — the request's is
// cancelled the moment the handler returns — with a deadline long enough to
// be nobody's latency and short enough that a wedged store leaks one
// goroutine rather than one per click.
//
// A failure is logged and nothing else. The projection is rebuilt from the
// durable records on the launch's own schedule, so what a failed refresh costs
// is a listing that lags until then — never the stance, which is already
// durable, and never the request, which is answered either way.
func (s *Server) refreshReception(subject evaluation.Subject, refresh func(context.Context) error) {
	if refresh == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()),
			receptionRefreshTimeout)
		defer cancel()
		if err := refresh(ctx); err != nil {
			s.logf("reception of %s %s recorded; evaluation projection not refreshed",
				subject.Kind, subject.ID)
		}
	}()
	select {
	case <-done:
	case <-time.After(receptionRefreshBudget):
	}
}

// receptionRefreshBudget is how long a click waits for the projection to catch
// up with it. It is the width of an instant rather than a service level: past
// it the operator is watching a progress indicator for work he did not ask
// for, and the work is no more correct for being waited on.
const receptionRefreshBudget = 150 * time.Millisecond

// receptionRefreshTimeout bounds the refresh itself. It is long because by
// then the refresh is not in anybody's way, and finite so that a store which
// has stopped answering does not accumulate goroutines for the life of the
// launch.
const receptionRefreshTimeout = time.Minute
