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
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/sharedcatalog"
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

// routeRecord dispatches the two id-bearing record routes, reporting whether
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
	Notice    string         `json:"notice,omitempty"`
	Standing  *standingView  `json:"standing,omitempty"`
	Action    *askView       `json:"action,omitempty"`
	Case      *caseView      `json:"case,omitempty"`
	Evidence  []evidenceView `json:"evidence,omitempty"`
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
// Quote is the citing record's own words about the cited bytes, which is the
// one prose field a frontier.Evidence has. It is not the bytes: recovering
// those means scanning the session log the locator names, and in this corpus
// those run to tens of megabytes with a single proposal citing nine of them,
// so a page that opened them would be the most expensive read on this surface
// by two orders of magnitude. The locator travels instead, with the route that
// opens the transcript at the cited line, which is what "expandable to the
// transcript" means.
//
// Kind is which side of the argument this citation is on, and it is not
// decoration. §4.3 and §4.5 require a record to state its counter-evidence,
// and a surface that rendered conflicting material as supporting would invert
// the record while showing every one of its words.
type evidenceView struct {
	Quote string `json:"quote,omitempty"`
	Kind  string `json:"kind"`
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
	// Contested reports recorded disagreement among Babel's reviewers: both
	// sides present, which is the one thing a tally cannot say by itself.
	Contested bool `json:"contested,omitempty"`
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
}

func (m machineryView) empty() bool {
	return m.Revision == "" && m.Digest == "" && m.Schema == 0 && m.CreatedAt == "" &&
		m.RunID == "" && m.PolicyVersion == "" && m.Host == "" && len(m.Links) == 0 &&
		len(m.Receipts) == 0 && len(m.Revisions) == 0
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
	peel.Evidence = s.resolveEvidence(ctx, core.evidence)
	peel.Standing, peel.Action = s.standing(ctx, ref)
	peel.Reception = s.reception(ctx, ref)
	machinery := s.machinery(ctx, ref, identity)
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
	peel.Evidence = s.resolveEvidence(ctx, core.evidence)
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

// resolveEvidence turns citations into rows a reader can open.
//
// The session catalog is read once for the whole record rather than once per
// citation, because it answers by enumeration: one call for nine locators is
// one pass and nine calls are nine. A catalog that could not answer costs the
// links and nothing else — the locator is what makes a claim evidence (§4.3),
// and it travels whether or not this host can open the conversation it names.
func (s *Server) resolveEvidence(ctx context.Context, cited []citedEvidence) []evidenceView {
	if len(cited) == 0 {
		return nil
	}
	sessions := s.sessionsBySourceID(ctx)
	views := make([]evidenceView, 0, len(cited))
	for _, item := range cited {
		locator := item.evidence.Locator()
		view := evidenceView{
			Quote: item.evidence.Note(),
			Kind:  item.kind,
			Path:  locator.Path,
			Line:  locator.Line,
		}
		if selector, ok := matchSession(sessions, locator.Path); ok {
			view.SessionID = selector
			if locator.Line > 0 {
				view.Event = locator.Line - 1
			}
			view.Href = sessionHref(selector, view.Event)
		}
		views = append(views, view)
	}
	return views
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

// matchSession resolves one locator path to the selector its session routes
// on.
func matchSession(index map[string][]SessionRow, locatorPath string) (string, bool) {
	if len(index) == 0 || locatorPath == "" {
		return "", false
	}
	stripped := strings.TrimSuffix(locatorPath, path.Ext(locatorPath))
	for _, row := range index[fileStem(locatorPath)] {
		if row.SourceID != "" && strings.HasSuffix(stripped, row.SourceID) {
			return row.Selector, true
		}
	}
	return "", false
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
			view.Operator, view.History = operatorStances(detail.History)
			view.Model = modelStances(detail.History, rolesByAssignment(detail.Assignments))
			counts := receptionCounts{
				Support: detail.Item.Reception.Support,
				Oppose:  detail.Item.Reception.Oppose,
				Unsure:  detail.Item.Reception.Unsure,
			}
			if !counts.empty() {
				view.Counts = &counts
			}
			view.Contested = counts.Support > 0 && counts.Oppose > 0
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
func (s *Server) machinery(ctx context.Context, ref frontier.Ref,
	identity recordIdentity) machineryView {
	view := machineryView{
		Schema:    identity.schema,
		CreatedAt: timeText(identity.createdAt),
		RunID:     identity.runID,
	}
	if chain, err := s.opts.Frontier.Revisions(ctx, ref); err == nil {
		for _, revision := range chain {
			view.Revisions = append(view.Revisions, machineryRevision{
				ID: revision.Entity.ID,
				At: timeText(revision.RecordedAt),
			})
		}
		if len(chain) > 0 {
			view.Revision = chain[len(chain)-1].Entity.ID
		}
	}
	view.Links = s.machineryLinks(ctx, ref)
	s.addEvaluationMachinery(ctx, ref, &view)
	return view
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
	record, err := s.opts.Evaluation.Operator(r.Context(), evaluation.OperatorInput{
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
	s.writeJSON(w, http.StatusOK, receptionResult{
		Stance: record.Stance,
		At:     timeText(record.CreatedAt),
	})
}
