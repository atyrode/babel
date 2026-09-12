package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/transcript"
)

var (
	// ErrBadRequest lets an injected operation reject an invalid request.
	ErrBadRequest = errors.New("bad request")
	// ErrNotFound lets an injected operation report a missing selector or snapshot.
	ErrNotFound = errors.New("not found")
	// ErrConflict lets an injected operation report unavailable configured state.
	ErrConflict = errors.New("conflict")
)

// Options supplies the server's narrow application dependencies.
type Options struct {
	Port        int
	Static      fs.FS
	Diagnostics io.Writer
	State       StateProvider
	Lister      SessionLister
	Scanner     Scanner
	Inspector   SessionInspector
	Archive     ArchiveOperations
	Transcripts TranscriptReader

	// Operator is the identity every Phase B mutation is attributed to.
	// §4.7 makes a disposition attributed guidance and §4.8 refuses an
	// anonymous answer or acceptance, so this is not a display name: it is
	// the author a durable decision records. It is empty when the launch
	// could not name an operator, and every mutating route then refuses
	// rather than defaulting, because a decision recorded against nobody
	// is worse than a decision not recorded.
	Operator string

	// The Phase B services. Each is an interface rather than the concrete
	// service type, and the method set is the whole authority this surface
	// has: the browser cannot ask for an operation that is not listed here,
	// which is what makes §14's "Reality/review mutations share the Go
	// service authorization path" a property of the type rather than a
	// promise about the handlers. Every one of them is satisfied by the
	// same service value internal/cli passes to the CLI commands, so the
	// two surfaces reach one implementation.
	Review   ReviewService
	Frontier FrontierReader
	Reality  RealityService
	Runs     RunLister
	Search   SearchIndex
	// Receipts reads run receipts whole, which is what the Watch surface's
	// run page and the record page's machinery peel need and what Runs
	// above cannot give them: a listing carries §9's plaintext half of a
	// header, and what a run searched, fetched, declined, spent and how
	// long it took is in the body. The body is sealed for the fleet and
	// plaintext on the machine that wrote it, so this is a read only a
	// local surface can perform, and it is a read — the store's appends
	// are not representable here.
	//
	// Nil is a state rather than a fault, on Complaints' terms: a build
	// with no durable store keeps every page it already served, and the
	// run detail reports that this session holds no receipts.
	Receipts RunReceiptReader
	// Launcher starts and stops the analysis this machine runs for itself
	// (§8.4's withdrawn refusal: runs are startable from the UI).
	//
	// It is the one option on this surface that spawns a process, and the
	// authority is the method set: it launches this machine's own binary
	// under the same subcommands, ceilings and profile checks the CLI
	// enforces, and it can stop a child it started. There is no method
	// that reaches another host — cross-machine invocation is explicitly
	// out of scope (#118) — and none that kills one, because a run
	// interrupted at a safe point keeps everything it committed and a
	// killed one does not.
	//
	// Nil is a state: a build with no launcher reports that this session
	// cannot start runs, and the Watch page keeps showing what is running.
	Launcher Launcher
	// Drain reports this process's last automatic publication attempt.
	//
	// It is separate from SyncJournal because the two answer different
	// questions: the journal says where one record stands, and this says
	// what the drainer this launch owns most recently achieved. A surface
	// that inferred the second from the first would be reporting a
	// publication attempt nobody observed.
	Drain DrainReader
	// Focus is §4.8's expenditure policy: the versioned mapping from ledger
	// state to what analysis may spend, and the operator's own statement of
	// intent about one subject.
	//
	// It is separate from Reality even though both are the same store,
	// exactly as Reviver is separate from Frontier. Reality is a read
	// surface plus the two acts a recorded plan gives an operator; this is
	// the one place a browser request asserts a fact of its own, so the
	// authority it carries is a named field holding a narrow type rather
	// than two more methods on the surface every reality page already
	// holds. A build can wire either without the other, and a nil Focus
	// leaves the focus routes reporting that this session has no ledger
	// while every reality read keeps answering.
	Focus FocusPolicyService
	// Subjects is §4.8's subject naming: the act that makes a ledger
	// writable at all, because every fact, Question and focus policy is
	// about an entity that already exists.
	//
	// It is a field of its own for Focus's reason rather than a widening of
	// either surface beside it. The authority a browser needs to name a
	// subject is not the authority to believe something about one, and the
	// only way to keep that true is for the two to be different types: this
	// one can create an identity and attach names to it, and has no method
	// that writes a fact. A nil Subjects leaves the two subject routes
	// reporting that this session has no ledger while every other reality
	// route keeps answering.
	Subjects SubjectNamingService
	// Dispositions and Reviver are #87's record actions. They are two
	// fields rather than one because they are two stores: the proposed
	// actions and their ledger live beside the frontier in internal/
	// disposition, and the revive transition is the frontier's own. A
	// deployment can hold either without the other — a durable file whose
	// disposition component would not open still renders revision chains —
	// so each route names the ones it needs and the rest keep answering.
	Dispositions DispositionService
	// Complaints is issue #115's operator steering: the capture box and the
	// complaint record pages.
	//
	// It is its own field on Dispositions' terms, because it is its own
	// component of the durable file: a build whose complaint component
	// would not open still renders every record page it already served.
	// Nil is therefore a state rather than a fault — the three complaint
	// routes report that this build holds no complaints, and every other
	// page keeps answering.
	Complaints ComplaintService
	Reviver    FrontierReviver
	// Evaluation is issue #219's full-lifecycle evaluation surface: what
	// Babel's own output has been reviewed, how it was received, what
	// coverage is still owed, and the operator's own decisions about all
	// three (§4.12, §5.8, §8.5).
	//
	// It is its own field rather than a widening of Review or Frontier
	// because it holds a different authority from both and reads a
	// different store. Review decides a record's disposition; the frontier
	// holds the records; this reads a rebuildable projection over the
	// deployment's evaluation records and writes exactly two things — the
	// operator's policy and the operator's own attributed criteria,
	// feedback and reconsideration decisions.
	//
	// Nil is a state rather than a fault, on Complaints' terms: a build
	// whose evaluation projection would not open keeps every page it
	// already served, and the evaluation routes report that this session
	// holds no evaluation service.
	Evaluation EvaluationService
	// Fleet and SyncJournal are issue #109's read half: the shared catalog
	// every host in the deployment commits to, and this machine's own
	// publication journal.
	//
	// A nil Fleet with no FleetError is intentional local mode. FleetError
	// preserves a failed shared-backend startup so routes cannot mistake it
	// for an unconfigured machine. A nil SyncJournal is a build with no
	// publication journal wired, which internal/fleet resolves to "local"
	// rather than guessing on its behalf.
	//
	// They are two fields because they are two things: the catalog is
	// authoritative about what committed globally, and the journal is the only
	// thing that knows what this machine staged while the catalog was
	// unreachable. Collapsing them would lose exactly the case SPEC.md §6.5
	// requires to stay visible.
	Fleet       FleetReader
	FleetError  error
	SyncJournal fleet.SyncJournal
	// Presence is issue #118's fleet-presence read: what every machine in the
	// deployment says it is running right now.
	//
	// It is separate from Fleet even though both read the shared catalog,
	// because they read different things under different rules. Fleet reads
	// committed records, which are durable truth with an object-store leg;
	// presence reads announcements, which live only in PostgreSQL because they
	// are ephemeral status. A deployment can hold either without the other — a
	// catalog whose presence table a migration has not reached still answers
	// every record read — so each route names the one it needs.
	//
	// Nil is a state rather than a fault, on Fleet's terms: a machine in local
	// mode has no presence table, and internal/web/presence.go answers that
	// with a well-formed empty body and a sentence rather than refusing. It is
	// also the honest shape of the guarantee internal/presence makes on the
	// write side — a presence failure never fails a run — carried onto the read
	// side, where a presence failure never fails a page.
	Presence PresenceReader
	// References is issue #113's typed reference graph, read-only: the
	// outgoing citations and backlinks a record surface renders beside the
	// record itself.
	//
	// It is reference.Lister and never reference.Appender, on the same terms
	// FrontierReader excludes the frontier's writers: an edge is asserted by
	// a run absorbing evidence, by a revision being minted, or by `babel`
	// itself, and a browser GET that could mint a citation would let a page
	// invent provenance nobody asserted.
	//
	// Nil is a state rather than a fault. A build with no reference store
	// keeps every record page it already served; the link section reports
	// that this session has no reference graph rather than the page failing,
	// which is the same degradation a missing fleet backend gets.
	References reference.Lister
	// Sessions resolves the durable session keys a reference edge records
	// (#113). It is separate from Lister, which serves the sessions page: one
	// answers "what does this host have" and the other "is this particular
	// durable key one of them", and only the second needs the deployment
	// identity that turns a harness and a source id into a key.
	//
	// Nil leaves every session endpoint inert with that stated as the reason,
	// which is the honest degradation: the edges are still shown, and a
	// reader is told this build cannot follow them rather than being handed a
	// link that resolves to nothing.
	Sessions SessionKeyResolver
	// Cookbook is the loaded analysis cookbook. It is read-only by
	// construction: a *cookbook.Set exposes lookups and nothing that
	// changes an asset.
	Cookbook *cookbook.Set
	// Filings, TopicQuestions and Stance are §4.13's three halves of what a
	// record is about: the frontier's filings, the ledger's unaccepted topic
	// proposals, and the operator's recorded stance toward a topic he has
	// accepted.
	//
	// They are three fields rather than one for the reason Focus and
	// Subjects are separate from Reality: they carry different authorities
	// over different stores. Filing a record asserts nothing about reality
	// and can be done by anybody who can read the record; accepting a topic
	// creates an entity, which §4.8 reserves to an attributed operator act;
	// recording a stance asserts a fact about the world. A build can wire
	// any of the three without the others, and the topics page degrades
	// field by field rather than disappearing: no filings means every post
	// is unfiled, no proposals means the page offers none, and no stance
	// reader means the topics render with their interest unset.
	Filings        FilingService
	TopicQuestions TopicQuestionService
	Stance         TopicStanceReader
	// Topics is §4.13's topic surface over the Reality Ledger: the
	// operator's stance toward a topic, and the merge, split and
	// retirement its identity can need.
	//
	// It is its own field beside Reality, Focus and Subjects for the
	// reason those three are separate from each other: it holds a
	// different authority. Reality reaches the ledger's authoritative
	// writes only through a plan the operator accepted, because a model
	// proposed their content; this one carries the operator's own acts on
	// a topic page, where nothing was proposed by anything and the person
	// clicking is the authority §4.8 requires.
	//
	// Nil is a state rather than a fault, on Complaints' terms: a build
	// whose ledger did not open keeps every page it already served, and
	// the topic routes report that this session holds no ledger.
	Topics TopicLedger
	// TopicFiler is the one frontier write the topic routes perform: the
	// records a split moves to the part they belong to (§4.13).
	//
	// It is one method for FrontierReviver's reason. Filing is otherwise a
	// record's own act and is reached from the record's routes; what a
	// split needs is the ability to complete its own operator act, because
	// a split whose records stayed under the name the operator has just
	// said was wrong would have produced an empty topic and left the
	// evidence behind. Nil leaves the split route reporting that this
	// session holds no frontier to file into, rather than performing half
	// of it.
	TopicFiler TopicFiler
}

// ReviewService is the §4.7 review surface the web API may reach, satisfied by
// *review.Service.
//
// The two mutations are here and the frontier's own writers are not, and that
// separation is the point: a disposition is appended by review.Service.Decide,
// which validates reviewability, the record's state, and the operator identity
// before internal/frontier appends anything. A handler that reached a store
// directly would skip exactly those checks, which is the defect §14's gate
// exists to prevent, so this surface holds no value that could.
type ReviewService interface {
	Queue(context.Context, review.QueueFilter) ([]review.QueueItem, error)
	History(context.Context, frontier.Ref) (review.History, error)
	Lineage(context.Context, review.Node) (review.Lineage, error)
	Export(context.Context, review.Node, review.ExportOptions) (review.Export, error)
	Decide(context.Context, review.Decision) (frontier.DispositionEvent, error)
	RecordContext(context.Context, review.Authority, string) (review.Context, error)
}

// EvaluationService is the §4.12/§5.8/§8.5 evaluation surface the web API may
// reach, satisfied by *evaluation.Service.
//
// Six methods, and the shape of the set is the authority: four reads, the
// operator's policy, and the operator's own attributed records. Submit, Draw,
// Review and Claim are deliberately absent, and their absence is the one
// guarantee this surface most has to make. An evaluation assessment is a
// worker's statement about content it was served under a claim, fenced and
// attributed to a run; a browser holds no claim, no fence and no run, so a
// route that could reach Submit would let a click mint a vote that reads like
// a review. That cannot be a rule a handler keeps, because a handler can be
// edited — it is a method this type does not have.
//
// Refresh is absent for a different reason. The projection is rebuilt on a
// schedule the launch owns (§8.5: page reads are bounded and do not scan the
// corpus), so a GET that rebuilt it would make one operator's page view the
// deployment's most expensive request.
//
// Configure takes the operator separately from the policy because the author
// of a configuration is the session's identity and never a field in the body;
// Operator takes an evaluation.OperatorInput whose Operator field the handler
// fills in for the same reason.
type EvaluationService interface {
	List(context.Context, evaluation.Query) (evaluation.Page, error)
	Detail(context.Context, evaluation.Subject) (evaluation.Detail, error)
	Coverage(context.Context) (evaluation.Coverage, error)
	// AssessmentDays counts the assessments recorded per UTC day, which is
	// the Watch surface's reviews-per-day series. It is a count rather
	// than a listing for the reason Coverage above is an aggregate: a
	// ninety-day series assembled by paging records would read the whole
	// store to render ninety numbers.
	AssessmentDays(context.Context, time.Time) ([]evaluation.AssessmentDay, error)
	Policy(context.Context) (evaluation.Policy, error)
	Configure(context.Context, string, evaluation.Policy) (evaluation.Record, error)
	Operator(context.Context, evaluation.OperatorInput) (evaluation.Record, error)
	// OperatorDeferred is Operator with the projection refresh handed back
	// instead of performed, and it is here because a click is not a command.
	// The stance an operator records is durable when the transaction commits;
	// what followed it inline was a refresh of a rebuildable projection that
	// reads this instance's evaluation records, assignments, attempts and
	// effective policy in order to replace one row, and on the live catalog
	// that was six of the six and a half seconds an upvote took. The route
	// answers on the write and runs the refresh after the response.
	OperatorDeferred(context.Context, evaluation.OperatorInput) (
		evaluation.Record, func(context.Context) error, error)
	// Tallies and Thread are §8.7's feed half: the deployment's reception
	// grouped by subject in one read, and one subject's conversation in
	// commit order.
	//
	// They are reads like the four above and they are here rather than
	// assembled from them because the front page asks a question the
	// per-subject reads cannot answer affordably: what does every record
	// stand at, right now. Detail answers it one subject at a time, which
	// is one query per row over a corpus of thousands.
	//
	// Thread reads the durable records rather than the projection on
	// purpose. The projection is a snapshot of the evaluable inventory,
	// and a record it has not yet swept still has a conversation under it;
	// a thread that disappeared until the next sweep would let the cache
	// decide what was said.
	Tallies(context.Context) (map[evaluation.Subject]evaluation.Tally, error)
	Thread(context.Context, evaluation.Subject) ([]evaluation.ThreadRecord, error)
}

// FrontierReader is the read-only subset of *frontier.Store the API renders
// records from.
//
// It lists no writer at all. §5.2 and §4.7 make the frontier append-only, and
// the records this surface shows are written by exploration and decided
// through ReviewService; a GET that could create a revision would break both
// HTTP semantics and the audit story, so it is not representable here.
type FrontierReader interface {
	Hypothesis(context.Context, string) (frontier.Hypothesis, error)
	Observation(context.Context, string) (frontier.Observation, error)
	ObservationsFor(context.Context, string) ([]frontier.Observation, error)
	Finding(context.Context, string) (frontier.Finding, error)
	Proposal(context.Context, string) (frontier.Proposal, error)
	// ProposalsAddressing lists the remedies that answer one claim directly
	// (#114). It is a read of the asserted relation, so a candidate page can
	// show the competing suggestions somebody has offered against it.
	ProposalsAddressing(context.Context, string) ([]frontier.Proposal, error)
	// Proposals enumerates §4.5's review artifacts, which is what the
	// proposals listing pages. The frontier offers no other enumeration of
	// them: ProposalsAddressing answers about one claim, and Proposal
	// answers about one id.
	Proposals(context.Context, frontier.ListFilter) ([]frontier.Proposal, int, error)
	// RecordDays counts the records written per UTC day and kind, which is
	// the Watch surface's records-per-day series. It is here rather than
	// beside the enumerations because it is the same authority read a
	// different way: it decodes no payload, and it is the only read that
	// can answer for observations, which no listing above enumerates.
	RecordDays(context.Context, time.Time) ([]frontier.RecordDay, error)
	// TriageAdvice reads what a triage pass said about one proposal before
	// anybody ruled on it, which the review page shows beside the record it
	// is about. It is a read like every other name here, and that is the
	// half of the authority line this interface can carry: a pass writes its
	// advice through internal/frontier's narrow triage handle, and a browser
	// reaches neither that handle nor a disposition.
	TriageAdvice(context.Context, string) ([]frontier.TriageAdvice, error)
	// TriageAdvised answers the same question for a whole listing page and
	// answers only the presence of advice, never its rank. A row that has
	// been read can say so; the v1 cohort rank stays off a listing because
	// it is a place in one pass's pile rather than a reception, an exposure
	// or an outcome, so an order derived from it would mean none of the
	// things a reader would take it for. §8.5's ordered reading queue is
	// the evaluation surface, which states its basis and freshness with the
	// order it serves.
	TriageAdvised(context.Context, []string) (map[string]bool, error)
	LinksFrom(context.Context, string) ([]frontier.Link, error)
	LinksTo(context.Context, string) ([]frontier.Link, error)
	StatusHistory(context.Context, string) ([]frontier.StatusEvent, error)
	ReviewStatus(context.Context, frontier.Ref) (frontier.ReviewStatus, error)
	// Hypotheses enumerates §5.2's candidates with the store's own
	// aggregate beside them, which is what both the dashboard and the
	// frontier listing read. A landing page wants a distribution and the
	// few newest rows, never the corpus: tallying by reading every
	// candidate through Hypothesis cost a thirty-second response on a
	// two-thousand-record frontier and the browser abandoned it, so the
	// count is computed where the records are. It is the same enumeration
	// `babel hypotheses` pages, so neither surface's total can drift from
	// the command's or from the other's.
	//
	// It is also the only enumeration here: the listing used to union
	// internal/review's queue with the unexplored frontier, which reached
	// neither a superseded revision nor a candidate that came to rest
	// without being enrolled, so this surface no longer needs Unexplored —
	// that listing is exploration's triage order and answers a different
	// question.
	Hypotheses(context.Context, frontier.ListFilter) ([]frontier.Hypothesis, int, error)
	// Revisions and Head are #87's chain reads. They are reads in the
	// strictest sense — one is the whole append-only chain a record belongs
	// to and the other is its last entry — so they belong here rather than
	// beside the revive transition, and neither can produce a revision: a
	// chain grows only when a run or `babel revise` appends to it, and
	// neither is reachable from a browser.
	Revisions(context.Context, frontier.Ref) ([]frontier.Revision, error)
	Head(context.Context, frontier.Ref) (frontier.Ref, error)
	// OutputsOfRun lists the head revisions one run wrote, which is the one
	// connection a record carries that no page could follow: every record
	// names its run, and until now nothing could ask a run what else it
	// said. A reader who has just read a finding wants the observations it
	// consolidated and the remedy proposed beside it, and they are the same
	// pass's work rather than four unrelated rows.
	OutputsOfRun(context.Context, string) ([]frontier.RunOutput, error)
	// Observations, Findings and ReviewStandings are §8.7's feed half:
	// every record the deployment has produced, and where each of them
	// stands, read as a corpus rather than one row at a time.
	//
	// The two enumerations exist because the front page is every kind at
	// once and only two of the four could be listed. Findings were read
	// through internal/review's queue, which answers about enrolled
	// records and therefore could not see one nobody had enrolled;
	// observations could be reached only through the candidate they
	// develop, so assembling the corpus meant walking the frontier
	// candidate by candidate.
	//
	// ReviewStandings is the same widening applied to the derivation: a
	// standing per record is one query per row, and the disposition log it
	// consults holds tens of rows against a corpus of thousands. It
	// derives nothing new — both it and ReviewStatus map their ruling
	// through one rule — so the feed's standing and the record page's
	// cannot disagree.
	Observations(context.Context, frontier.ListFilter) ([]frontier.Observation, int, error)
	Findings(context.Context, frontier.ListFilter) ([]frontier.Finding, int, error)
	ReviewStandings(context.Context) (map[frontier.Ref]frontier.ReviewStanding, error)
}

// FrontierReviver is the one frontier write this surface may perform, and it
// is separate from FrontierReader for exactly that reason: a reader that had
// grown a writer would make "the frontier is read-only here" a sentence in a
// comment rather than a property of a type.
//
// #87 removes the idea that a status can be an ending, so a resting candidate
// has to be able to move again, and an operator's click is one of the two
// authors that may move it. The method set is one method: reviving states where
// the candidate lands and why, and internal/frontier refuses a candidate that
// is not at rest, a landing that is itself a resting state, and a revive with
// no reason. None of those rules is restated by a handler.
type FrontierReviver interface {
	Revive(context.Context, frontier.ReviveInput) (frontier.StatusEvent, error)
}

// DispositionService is #87's actionable-output surface the web API may reach,
// satisfied by *disposition.Store.
//
// Two writes are listed and they are the two the issue gives an operator: an
// attributed answer to a proposed action, and an instruction-free invitation to
// process a record further. Neither does anything outside Babel — accepting a
// draft-issue opens no issue and this package holds no credential — so what a
// browser can reach here is the durable record that a person authorized an
// action, never the action.
//
// Propose is deliberately absent. A run proposes actions through its result
// schema and an operator may synthesize one with `babel disposition propose`;
// a browser button that minted proposals would make the surface whose job is
// authorizing them also their author. Consume and ConsumeOne are absent for the
// mirror-image reason: an invitation is taken by a run that is about to work,
// and a browser request is not one.
type DispositionService interface {
	Disposition(context.Context, string) (disposition.Disposition, error)
	List(context.Context, disposition.ListFilter) ([]disposition.Disposition, int, error)
	Ledger(context.Context, string) ([]disposition.LedgerEntry, error)
	Decide(context.Context, disposition.DecideInput) (disposition.LedgerEntry, error)
	Invite(context.Context, disposition.InviteInput) (disposition.Invitation, error)
	Invitations(context.Context, disposition.InvitationFilter) ([]disposition.Invitation, error)
}

// RealityService is the §4.8 ledger surface the web API may reach, satisfied by
// *reality.Store, which is the ledger's service layer.
//
// Exactly two mutations are listed, and they are the two §4.8 gives an
// operator over a *model's* proposals: retaining an answer, and the single
// explicit acceptance that lets an interpretation's authoritative actions
// touch reality. Everything that could make a fact authoritative by another
// route — AssertFact, SupersedeFact, MergeEntities, ImportFacts,
// PutFocusRules — is deliberately absent, so no browser request can reach
// authority the operator did not exercise through the plan the ledger
// recorded.
//
// FocusPolicyService below is the one deliberate exception, and it is a
// different type rather than a widening of this one. The distinction it keeps
// is the distinction §4.8 draws: an interpretation's fact needs an acceptance
// because a model proposed it, while an operator's own statement about what
// his machines are worth spending on is an attributed operator action — the
// authority itself. Keeping them apart is what makes this interface's promise
// still literally true.
type RealityService interface {
	Inbox(context.Context, reality.InboxQuery) ([]reality.InboxItem, error)
	Question(context.Context, string) (reality.Question, error)
	QuestionHistory(context.Context, string) ([]reality.QuestionEvent, error)
	Answers(context.Context, string) ([]reality.Answer, error)
	Plan(context.Context, string) (reality.Plan, error)
	Entity(context.Context, string) (reality.Entity, error)
	Aliases(context.Context, string) ([]reality.Alias, error)
	Relationships(context.Context, string) ([]reality.Relationship, error)
	Facts(context.Context, reality.FactQuery) ([]reality.Fact, error)
	Questions(context.Context, reality.QuestionQuery) ([]reality.QuestionListing, error)
	Plans(context.Context, string) ([]reality.Plan, error)
	Entities(context.Context, reality.EntityQuery) ([]reality.EntityListing, error)
	ResolutionHistory(context.Context, string) ([]reality.Resolution, error)
	RecentFacts(context.Context, int) ([]reality.Fact, error)
	Fact(context.Context, string) (reality.Fact, error)
	FactStatusHistory(context.Context, string) ([]reality.FactStatusEvent, error)
	DisputesFor(context.Context, string) ([]reality.Dispute, error)
	HypothesesForEntity(context.Context, string) ([]string, error)
	RecordAnswer(context.Context, reality.AnswerInput) (reality.Answer, error)
	AcceptPlan(context.Context, reality.AcceptanceInput) (reality.Acceptance, reality.Application, error)
}

// FocusPolicyService is §4.8's focus surface the web API may reach, satisfied
// by *reality.FocusPolicy.
//
// This is the only surface in this package holding a ledger write that is not
// mediated by a recorded plan, and every line of its authority is in its
// method set. Assert writes the analysis-policy predicate and no other;
// Supersede replaces an analysis-policy fact and refuses anything else;
// Install stores the rule set this build ships and cannot store one that
// arrived in a request. There is no method here that merges an entity,
// imports a batch, opens or resolves a dispute, asserts a lifecycle, creates
// an entity, or closes the store — not by convention, but because
// reality.FocusPolicy has none, which is why the concrete type exists.
//
// The reads are here rather than borrowed from RealityService because they
// are the write's own preconditions. InForce is what a mutation confirms it
// was shown, Resolve is how an operator's word becomes a subject, and Decide
// is how a consequence is stated in the same arithmetic a run's deferral came
// from; a handler that read them through a second surface could be shown one
// thing by the page and write against another.
type FocusPolicyService interface {
	Shipped() reality.FocusRuleSet
	Rules(context.Context, int) (reality.FocusRuleSet, error)
	Install(context.Context) (reality.FocusRuleSet, error)
	Decide(context.Context, reality.FocusQuery) (reality.FocusDecision, error)
	Resolve(context.Context, string) (string, error)
	Entity(context.Context, string) (reality.Entity, error)
	Aliases(context.Context, string) ([]reality.Alias, error)
	Subjects(context.Context) ([]string, error)
	InForce(context.Context, string, time.Time) (reality.Fact, bool, error)
	History(context.Context, string) ([]reality.Fact, error)
	Fact(context.Context, string) (reality.Fact, error)
	Assert(context.Context, reality.FocusPolicyInput) (reality.Fact, reality.Dispute, error)
	Supersede(context.Context, reality.FocusPolicyRevision) (reality.Fact, error)
}

// SubjectNamingService is §4.8's subject-naming surface the web API may
// reach, satisfied by *reality.SubjectNaming.
//
// It is a third reality surface rather than two more methods on either of the
// two above, and the reason is that it holds a different authority from both.
// RealityService reaches the ledger's authoritative writes only through a
// plan an operator accepted, because a model proposed their content.
// FocusPolicyService states the one predicate that carries an operator's own
// intent. This one asserts nothing at all: it mints the subject those facts
// are about, which is why CreateEntity stays forbidden on the interface every
// reality page holds and lives here instead, on a type whose whole method set
// is naming.
//
// Kinds and AliasKinds are served rather than restated by the client because
// §4.8's vocabularies are closed. A picker holding its own copy of the kinds
// would offer one the ledger cannot store, and the operator would discover it
// from a refused form.
//
// Resolve is here for the write's own precondition, on FocusPolicyService's
// terms: the page reached this surface because a word resolved to nothing, and
// the creation has to be able to check that this is still true. Reading it
// through a second surface would let the page be shown one answer and the
// write act on another.
type SubjectNamingService interface {
	Kinds() []reality.EntityKind
	AliasKinds() []reality.AliasKind
	Resolve(context.Context, string) (string, error)
	Create(context.Context, reality.NewSubject) (reality.Entity, []reality.Alias, error)
}

// ComplaintService is issue #115's operator-steering surface the web API may
// reach, satisfied by *complaint.Store.
//
// One write is listed, and it is the one thing this surface exists to do: take
// an operator's own unprompted sentence about what is going badly and make it a
// record. It needs no authorization step of its own — a complaint is authored
// by the operator, so it is authorized at birth as steering under #86's
// intentionality principle — but it does need the identity §4.7 requires of
// every attributed write, which is why the route resolves an operator and a
// host before calling this at all.
//
// Amend is deliberately absent. No record page on this surface edits a
// record's wording: a hypothesis page cannot revise a hypothesis either, and
// restating a complaint is `babel tell --amend`, where the operator can see
// the wording they are replacing. A browser box that silently replaced the
// text the page was showing would be a different feature from the one #115's
// web half asks for, and it would be reachable by anyone who could reach the
// capture box.
//
// Outputs and Output are absent for a different reason: they flatten
// complaints for the retrieval index, which is preparation's job and `babel
// tell`'s, never a page view's. A GET that reconciled a shared cache would be
// the writer §14's gate keeps off this surface.
type ComplaintService interface {
	Tell(context.Context, complaint.TellInput) (complaint.Complaint, error)
	Complaint(context.Context, string) (complaint.Complaint, error)
	Revisions(context.Context, string) ([]complaint.Complaint, error)
	Heads(context.Context) ([]complaint.Complaint, error)
}

// SearchIndex is the retrieval surface behind GET /api/search and behind
// #115's capture-time adjacency pass, satisfied by *index.Index.
//
// It answers two questions because the deployment holds two corpora: Search
// reads the sessions Babel archived, and FrontierSearch reads what Babel
// itself has said about them — a hypothesis, a finding, a review answer, an
// operator's earlier complaint. A capture asks the second one, because "what
// does Babel already have touching this" is a question about Babel's own
// output rather than about the transcripts underneath it.
//
// Neither writer is listed. IndexSession and IndexFrontier are reconciled by
// preparation and by `babel tell`, never by a browser request, so a capture
// searches the partition as it stands rather than rebuilding it: a shared
// rebuildable cache written by a page view is exactly the writer §14's gate
// keeps off this surface.
type SearchIndex interface {
	Search(context.Context, index.Query) ([]index.Hit, error)
	FrontierSearch(context.Context, index.FrontierQuery) ([]index.FrontierHit, error)
}

// RunLister supplies the run receipts GET /api/analysis/state lists, newest
// first, bounded by the caller's page.
//
// It is an interface rather than a *run.Store because the listing's shape is
// this package's: the store enumerates receipts whole, and a listing carries
// only §9's plaintext half of each header, so the mapping between the two
// lives at the wiring site. A nil provider reports no runs rather than an
// error: a build with no analysis history has nothing to list, which is
// different from a failure.
type RunLister interface {
	Runs(ctx context.Context, limit, offset int) ([]RunSummary, int, error)
}

// RunReceiptReader reads run receipts whole, which is what a run page and a
// record's machinery peel are: the header plus the half of the record that
// never reaches the wire — what the run searched, what it fetched, what it
// declined, what it spent and how long it took.
//
// It is satisfied by *run.Store, and the method set is the whole authority:
// two reads. PutReceipt, Amend and the sync markers are not representable
// here, so a browser request cannot append to a run's history or claim one
// published; §7 makes receipts append-only and the only writer is the run
// itself.
//
// Reading a body is a local read by construction. §9 seals a receipt's body
// before it leaves the machine, so this surface can open one exactly because
// it is the machine that wrote it, and never for another host's run.
type RunReceiptReader interface {
	Receipts(ctx context.Context, limit, offset int) ([]run.Receipt, int, error)
	Revisions(ctx context.Context, runID string) ([]run.Receipt, error)
}

// RunSummary is one run receipt as a listing shows it: the plaintext-eligible
// half of run.Header (§9's allowlist) and nothing from the sealed body.
type RunSummary struct {
	ReceiptID     string    `json:"receipt_id"`
	RunID         string    `json:"run_id"`
	PreparationID string    `json:"preparation_id"`
	Revision      int       `json:"revision"`
	RecordedAt    string    `json:"recorded_at"`
	Sync          string    `json:"sync"`
	Counts        RunCounts `json:"counts"`
	// Authority is why this run happened: an operator's command or
	// invitation, a conductor policy, or a serendipity draw. It is a
	// listing-level field rather than a payload one because "why did Babel
	// spend a token" is the question a receipt strip is read for, and a
	// listing that could only answer it by opening a sealed body would not
	// answer it at all.
	//
	// The zero value means the receipt was written before receipts recorded
	// one. That is an absence rather than an operator authority, and the
	// surfaces that render it say so instead of filling the gap in.
	Authority RunAuthority `json:"authority"`
	// Host is the machine that produced the run, and HostAttributed says
	// whether the shared catalog could name one at all (issue #109 item 4).
	//
	// They are filled in by the server rather than by the RunLister, because
	// the answer is not in the receipt: a receipt records the run, and which
	// host that run's origin instance registered as is the shared catalog's
	// fact. A run the catalog cannot attribute — an instance registered before
	// migrations/0007, or a local-mode machine with no catalog at all — leaves
	// both at their zero value, and a listing renders that absence rather than
	// naming the machine it happens to be running on.
	Host           string `json:"host"`
	HostAttributed bool   `json:"host_attributed"`
}

// RunAuthority mirrors the authority a run receipt's header carries. Kind is
// "operator", "policy" or "serendipity", and Ref names the command, invitation,
// policy or draw behind it; both are empty on a receipt recorded before the
// field existed.
type RunAuthority struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// RunCounts mirrors run.Counts field-for-field. A non-zero Redactions is the
// audit signal §9 wants visible from a listing rather than from a payload.
type RunCounts struct {
	ToolRequests int `json:"tool_requests"`
	ToolsDenied  int `json:"tools_denied"`
	Retrieval    int `json:"retrieval"`
	Deferred     int `json:"deferred"`
	Rejected     int `json:"rejected"`
	Failures     int `json:"failures"`
	Redactions   int `json:"redactions"`
}

// The real services satisfy these interfaces, asserted here rather than
// discovered at the wiring site. It is what makes the §14 property checkable:
// the surface the browser reaches is the service internal/cli holds, so a
// service method that changed shape is a compile failure here instead of a
// second implementation growing beside it.
var (
	_ ReviewService        = (*review.Service)(nil)
	_ FrontierReader       = (*frontier.Store)(nil)
	_ FrontierReviver      = (*frontier.Store)(nil)
	_ RealityService       = (*reality.Store)(nil)
	_ TopicLedger          = (*reality.Store)(nil)
	_ TopicFiler           = (*frontier.Store)(nil)
	_ FocusPolicyService   = (*reality.FocusPolicy)(nil)
	_ SubjectNamingService = (*reality.SubjectNaming)(nil)
	_ DispositionService   = (*disposition.Store)(nil)
	_ ComplaintService     = (*complaint.Store)(nil)
	_ SearchIndex          = (*index.Index)(nil)
	_ RunReceiptReader     = (*run.Store)(nil)
	_ EvaluationService    = (*evaluation.Service)(nil)
)

// State is the non-secret subset of persistent storage configuration exposed
// by GET /api/state.
type State struct {
	Configured bool   `json:"configured"`
	Repository string `json:"repository"`
	HostID     string `json:"host_id"`
}

// StateProvider supplies current storage state without exposing password data.
type StateProvider interface {
	WebState(context.Context) (State, error)
}

// StateProviderFunc adapts a function to StateProvider.
type StateProviderFunc func(context.Context) (State, error)

func (f StateProviderFunc) WebState(ctx context.Context) (State, error) { return f(ctx) }

// SessionRow mirrors internal/cli sessionRow field-for-field. Every nullable
// field stays nullable across the mirror, including the continuation grade:
// the CLI leaves it absent when no transcript was read, and this shape must be
// able to say so rather than reporting a grade nothing observed.
type SessionRow struct {
	Harness  string  `json:"harness"`
	SourceID string  `json:"source_id"`
	Selector string  `json:"selector"`
	Size     int64   `json:"size"`
	Modified *string `json:"modified"`
	Title    *string `json:"title"`
	// TitleProvenance is null exactly when Title is, and otherwise names where
	// the title came from: "recorded" by the harness, "derived" by Babel from
	// the session's own records, or "inferred" by a model. The web app is the
	// primary surface (decision 1), so this is the one place the distinction
	// has to reach a human: three different kinds of claim render as the same
	// short line of text, and a reader who cannot tell them apart is being
	// shown Babel's arithmetic as if the harness had recorded it.
	TitleProvenance *string `json:"title_provenance"`
	Workspace       *string `json:"workspace"`
	// RepositoryIdentity, RepositoryRemote and RepositoryReason are the
	// repository the workspace belongs to, as this host observed it during
	// the scan. They are what the feed's topics are derived from: a
	// workspace path says where the work happened, and SPEC.md §4.13 is
	// explicit that this is not what a record is about — two worktrees of
	// one repository are one topic, and a directory under /tmp is none.
	//
	// RepositoryIdentity is the absolute git common directory, which every
	// worktree of a repository shares; RepositoryRemote is the origin as
	// host/owner/repo, absent when the checkout has no origin;
	// RepositoryReason is present exactly when the identity is absent and
	// says why nothing was observed.
	RepositoryIdentity *string `json:"repository_identity"`
	RepositoryRemote   *string `json:"repository_remote"`
	RepositoryReason   *string `json:"repository_reason"`
	ContinuationGrade  *bool   `json:"continuation_grade"`
	// CostUSD, TotalTokens, Turns and ToolErrors are what the harness itself
	// recorded about the model work in this session, summed by the adapter
	// over the raw transcript. The CLI's listing has carried them since the
	// usage columns landed and `sessions list --json` emits them; the browser
	// is the surface an operator actually sorts a corpus on, so they cross
	// the wire here too.
	//
	// Null is not zero, exactly as in the CLI's row: a session that cost
	// nothing and a session whose adapter extracted no usage are different
	// answers, and only the first one is a statement about the session. A
	// page that rendered the second as $0.00 would be reporting a
	// measurement nobody took.
	CostUSD     *float64 `json:"cost_usd"`
	TotalTokens *int64   `json:"total_tokens"`
	Turns       *int64   `json:"turns"`
	ToolErrors  *int64   `json:"tool_errors"`
}

// ScanState mirrors internal/cli scanState field-for-field. It reports the
// background catalog scan that describes sessions: describing a large corpus
// takes minutes, so every listing surface reports its progress rather than
// waiting on it.
type ScanState struct {
	Running    bool   `json:"running"`
	Described  int    `json:"described"`
	Total      int    `json:"total"`
	Failed     int    `json:"failed"`
	Harness    string `json:"harness,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Scanner reports and starts the background catalog scan behind
// GET /api/scan and POST /api/sessions/refresh. Both methods return
// immediately: a scan is owned by the server process, never by the request
// that asked for it, so a canceled request never discards described
// sessions and concurrent requests share one scan.
type Scanner interface {
	State() ScanState
	StartRefresh() ScanState
}

// SessionsResult extends internal/cli sessionsResult with the cache refresh
// time and the scan state required by GET /api/sessions. The rows are
// whatever the catalog already holds, which is why they are served without
// waiting for the scan the request may have started.
type SessionsResult struct {
	Sessions    []SessionRow `json:"sessions"`
	RefreshedAt string       `json:"refreshed_at"`
	Scan        ScanState    `json:"scan"`
}

// SessionLister supplies the cached local session listing.
type SessionLister interface {
	ListSessions(context.Context) (SessionsResult, error)
}

// SessionListerFunc adapts a function to SessionLister.
type SessionListerFunc func(context.Context) (SessionsResult, error)

func (f SessionListerFunc) ListSessions(ctx context.Context) (SessionsResult, error) { return f(ctx) }

// SessionKeyResolver translates between the two identities one session has
// (#113): the durable key a reference edge records, and the selector every
// Babel surface routes on.
//
// Both exist because the two answer different questions. An endpoint publishes
// as a plaintext catalog column, so an edge records a digest of the deployment,
// the host, the harness and the source id — a selector carries a
// workspace-derived path and cannot travel there. A page, a CLI argument and a
// route have always named a session by selector.
//
// SessionsByKey is a batch call because the catalog answers by enumeration:
// matching keys means deriving them for the rows this machine has, so one call
// for a page of endpoints is one pass and one call per endpoint is a pass per
// endpoint. A key with no local session is absent from the result rather than an
// error — an edge naming another host's session is the expected case on a
// deployment whose graph is fleet-wide, not a failure.
type SessionKeyResolver interface {
	SessionsByKey(ctx context.Context, keys []string) (map[string]SessionRow, error)
	// KeyForSelector derives the durable key of one local session, reporting
	// false for a selector this host has no session for.
	KeyForSelector(ctx context.Context, selector string) (string, bool, error)
}

// CompletenessRow mirrors internal/cli completenessRow field-for-field.
type CompletenessRow struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// RepoRow mirrors internal/cli repoRow field-for-field.
type RepoRow struct {
	Remote string `json:"remote,omitempty"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// FileRow mirrors internal/cli fileRow field-for-field.
type FileRow struct {
	RelPath    string `json:"rel_path"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

// BlobRow mirrors internal/cli blobRow field-for-field.
type BlobRow struct {
	Digest     string `json:"digest"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

// InspectResult mirrors internal/cli inspectResult field-for-field.
type InspectResult struct {
	Harness     string `json:"harness"`
	SourceID    string `json:"source_id"`
	Selector    string `json:"selector"`
	PrimaryPath string `json:"primary_path"`
	PrimarySize int64  `json:"primary_size"`
	DescribedAt string `json:"described_at"`
	Hint        string `json:"hint,omitempty"`

	Title           *string           `json:"title"`
	TitleProvenance *string           `json:"title_provenance"`
	Workspace       *string           `json:"workspace"`
	CreatedAt       *string           `json:"created_at"`
	ModifiedAt      *string           `json:"modified_at"`
	Lifecycle       *string           `json:"lifecycle"`
	Repo            *RepoRow          `json:"repo"`
	Completeness    []CompletenessRow `json:"completeness,omitempty"`

	AdapterMetadataSchema int             `json:"adapter_metadata_schema"`
	AdapterMetadata       json.RawMessage `json:"adapter_metadata,omitempty"`

	Artifacts          []FileRow `json:"artifacts,omitempty"`
	Blobs              []BlobRow `json:"blobs,omitempty"`
	UnresolvedBlobRefs []string  `json:"unresolved_blob_refs,omitempty"`
	ContinuationGrade  bool      `json:"continuation_grade"`
}

// SessionInspector resolves and describes one selector.
type SessionInspector interface {
	InspectSession(context.Context, string) (InspectResult, error)
}

// SessionInspectorFunc adapts a function to SessionInspector.
type SessionInspectorFunc func(context.Context, string) (InspectResult, error)

func (f SessionInspectorFunc) InspectSession(ctx context.Context, selector string) (InspectResult, error) {
	return f(ctx, selector)
}

// StatusHostRow mirrors internal/cli statusHostRow field-for-field.
type StatusHostRow struct {
	Host          string   `json:"host"`
	Snapshots     int      `json:"snapshots"`
	LatestTime    string   `json:"latest_time"`
	LatestID      string   `json:"latest_id"`
	LatestShortID string   `json:"latest_short_id"`
	Tags          []string `json:"tags,omitempty"`
}

// StatusResult mirrors internal/cli statusResult field-for-field.
type StatusResult struct {
	Repository string          `json:"repository"`
	Snapshots  int             `json:"snapshots"`
	Hosts      []StatusHostRow `json:"hosts"`
	// Catalog is absent in local mode, exactly as the CLI reports it:
	// there is no shared catalog to be behind.
	Catalog *CatalogStatus `json:"catalog,omitempty"`
}

// CatalogStatus mirrors the scalar half of internal/cli catalogStatus: whether
// the shared catalog answered, and how far behind the repository it is.
//
// The counts are pointers for the reason the CLI's are: an unreachable catalog
// does not make them zero, it makes them unknown, and reporting no
// uncatalogued snapshots is a claim this surface did not observe.
//
// The CLI's per-host catalog rows are deliberately not mirrored. The archive
// surface shows the repository's own host rows, which are what a snapshot
// listing observed; a second per-host table from the catalog would put two
// almost-identical host lists on one page with different meanings.
type CatalogStatus struct {
	Reachable    bool `json:"reachable"`
	Uncatalogued *int `json:"uncatalogued,omitempty"`
	Pending      *int `json:"pending,omitempty"`
}

// VerifyResult mirrors internal/cli verifyResult field-for-field.
type VerifyResult struct {
	Repository string `json:"repository"`
	Deep       bool   `json:"deep"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// FetchResult mirrors internal/cli fetchResult field-for-field. The mirror is
// what keeps this decodable from the CLI's own --json document: a field the
// CLI reports and this shape omits does not fail to build, it silently
// arrives as a zero value in the web interface.
type FetchResult struct {
	Selector        string   `json:"selector"`
	SnapshotID      string   `json:"snapshot_id"`
	SnapshotShortID string   `json:"snapshot_short_id"`
	SnapshotTime    string   `json:"snapshot_time"`
	Target          string   `json:"target"`
	Files           int      `json:"files"`
	Bytes           int64    `json:"bytes"`
	Included        []string `json:"included"`
	Restored        []string `json:"restored"`
	Missing         []string `json:"missing,omitempty"`
	AlreadyPresent  bool     `json:"already_present"`
}

// ArchiveSessionRow is one session as another host's snapshot listing can
// describe it. It is deliberately not a SessionRow.
//
// A cross-host listing reads only the snapshot's file listing — no transcript
// bytes are downloaded, which is what makes browsing another machine's archive
// cheap — so title, workspace, modification time, and continuation grade are
// unknowable here. SessionRow can represent them as null, and a shape that
// *can* carry a title invites a client to render an absent one as a blank cell
// that reads like empty data. This shape cannot carry them at all, so a client
// consuming it has no choice but to say what it actually knows.
//
// Fetched is the one thing this listing knows about the local machine, and it
// is why fetching leads somewhere visible: a materialization of this selector
// under Babel's own data directory means the operator already recovered this
// session here.
type ArchiveSessionRow struct {
	Harness     string `json:"harness"`
	SourceID    string `json:"source_id"`
	Selector    string `json:"selector"`
	Size        int64  `json:"size"`
	Fetched     bool   `json:"fetched"`
	FetchedPath string `json:"fetched_path,omitempty"`
}

// ArchiveSessionsResult is one host's archived session listing.
//
// Snapshot echoes the snapshot selector the request asked for and is empty
// when the request named none, which means that host's newest snapshot. It is
// not filled in with a resolved id: `sessions list --host` reports the rows it
// read and not the snapshot it read them from, and inventing an id here would
// state a fact this surface did not observe (SPEC.md §3).
type ArchiveSessionsResult struct {
	Host     string              `json:"host"`
	Snapshot string              `json:"snapshot"`
	Sessions []ArchiveSessionRow `json:"sessions"`
}

// FetchRequest is one materialization request. Host is the cross-host
// resolution `sessions fetch --host` performs: with it the selector is
// resolved inside that host's snapshot listing rather than against local
// files, which is the only way to fetch a session this machine never had.
// Empty means the launch-time host selection, exactly as the CLI's flag
// precedence gives it.
type FetchRequest struct {
	Selector string
	Snapshot string
	Host     string
}

// ArchiveOperations is the read/restore-only repository surface. Deliberately
// no forget or prune operation is representable here.
type ArchiveOperations interface {
	ArchiveStatus(context.Context) (StatusResult, error)
	ArchiveVerify(context.Context, bool) (VerifyResult, error)
	ArchiveSessions(ctx context.Context, host, snapshot string) (ArchiveSessionsResult, error)
	FetchSession(context.Context, FetchRequest) (FetchResult, error)
}

// TranscriptReader turns an inspected primary log into display events.
type TranscriptReader interface {
	Events(path, harness string, offset, limit int) (int, []transcript.Event, error)
}

// TranscriptReaderFunc adapts a function to TranscriptReader.
type TranscriptReaderFunc func(string, string, int, int) (int, []transcript.Event, error)

func (f TranscriptReaderFunc) Events(path, harness string, offset, limit int) (int, []transcript.Event, error) {
	return f(path, harness, offset, limit)
}

// FilingService is §4.13's filing surface: what a record is about, as the
// frontier stores it. It is satisfied by *frontier.Store.
//
// Both halves are here, unlike FrontierReader and FrontierReviver, because
// unlike a disposition a filing has no service in front of it and needs none:
// the rules a filing has to satisfy — the record exists, the rationale is
// stated, the author is named, a re-filing supersedes — are the store's own
// and are enforced there. What this type bounds is the authority, and the
// authority a browser has over filings is exactly these five acts: file,
// unfile, record that a record is about nothing in particular, and read.
//
// Unfiled is deliberately absent. It is the evaluation lane's backlog read,
// answered over the whole corpus, and a page that could ask for it would be a
// page that could scan the deployment on a click.
type FilingService interface {
	File(context.Context, frontier.FilingInput) (frontier.Filing, error)
	Unfile(ctx context.Context, record frontier.Ref, entityID string,
		author frontier.FilingAuthor, authorID, reason string) (frontier.Filing, error)
	NoTopic(ctx context.Context, record frontier.Ref, author frontier.FilingAuthor,
		authorID, reason string) (frontier.Filing, error)
	FilingsOf(context.Context, frontier.Ref) ([]frontier.Filing, error)
	FiledUnder(ctx context.Context, entityID string) ([]frontier.Ref, error)
}

// TopicProposalView is one topic question as the topics page renders it: the
// entity a run proposed, what it would be bound to, and why it thinks the
// topic exists.
//
// It is this package's own shape rather than the ledger's, and the reason is
// the boundary rather than convenience. A proposal is an unaccepted question:
// it has no entity id, no facts and no aliases, so rendering it through the
// entity types would mean rendering a subject that does not exist. Stating the
// six fields a page shows keeps the ledger free to hold whatever a topic
// question needs, and keeps this surface unable to show anything else.
type TopicProposalView struct {
	QuestionID string
	Name       string
	Kind       string
	// Identity is the dedup key the proposal was raised under — a
	// normalized remote for a repository, a slug for a concept — and
	// Remote and Paths are the binding it proposes.
	Identity string
	Remote   string
	Paths    []string
	// Records are the frontier record ids the proposal would file under the
	// entity if the operator accepted it.
	Records []string
	// Why is the proposal's own sentence: "32 sessions in 3 checkouts cite
	// it".
	Why string
}

// TopicQuestionService is §4.13's proposal half: what Babel has proposed as a
// topic, and the operator's two answers to it.
//
// The three methods are the whole authority: a browser can read the open
// proposals, accept one — which creates the entity and files the records it
// named, in one act — and decline one with a reason. It cannot create an
// entity directly, which is §4.8's rule and the reason this is not a widening
// of SubjectNamingService.
//
// It is satisfied by an adapter over the ledger rather than by the store
// itself, because accepting a topic commits into two stores at once: the
// ledger's entity and the frontier's filings. The adapter is where those are
// joined, and this surface names only the act.
type TopicQuestionService interface {
	TopicProposals(context.Context) ([]TopicProposalView, error)
	// AcceptTopic creates the proposed entity and files the records the
	// proposal named, returning the entity id the operator now owns.
	AcceptTopic(ctx context.Context, questionID, operator string) (string, error)
	DeclineTopic(ctx context.Context, questionID, operator, reason string) error
}

// TopicInterestView is the operator's recorded stance toward one topic, as the
// topics page shows it: working on it, keeping an eye, not now, excluded, or
// nothing said (§4.13).
//
// The empty state is a real answer and the page renders it as one — a topic
// nobody has taken a position on is not the same as one deliberately parked —
// which is why this is four strings rather than a bool and a reason.
type TopicInterestView struct {
	State  string
	Reason string
	At     string
	By     string
}

// TopicStanceReader reads the operator's stance toward one topic.
//
// It is read-only and separate from the routes that record a stance, on
// Focus's terms: the topics listing needs to show what the operator said, and
// the authority to say something new belongs to the route that asks for a
// reason.
type TopicStanceReader interface {
	TopicInterest(ctx context.Context, entityID string) (TopicInterestView, error)
}

// TopicStanceFunc adapts a function to TopicStanceReader, which is how the
// ledger's own interest read is wired without this package importing its
// types.
type TopicStanceFunc func(context.Context, string) (TopicInterestView, error)

func (f TopicStanceFunc) TopicInterest(ctx context.Context, entityID string) (TopicInterestView, error) {
	return f(ctx, entityID)
}

// TopicLedger is §4.13's topic surface over the Reality Ledger, satisfied by
// *reality.Store.
//
// A topic is a Reality entity and nothing else, so every act a topic page
// offers is a §4.8 act: the operator's stance toward the subject, the merge
// that says two names meant one thing, the split that says one name meant
// two, and the retirement that says a name should never have existed. This is
// the fourth reality surface in this file, and it is a fourth type rather than
// four more methods on any of the other three because it holds a fourth
// authority.
//
// RealityService reaches the ledger's authoritative writes only through a plan
// an operator accepted, because a model proposed their content. The acts here
// were proposed by nothing: the operator opened a topic he is looking at and
// said what he thinks about it, which is the attributed operator action §4.8
// takes as authority itself — the same judgement FocusPolicyService makes
// about an operator stating what his machines are worth spending on.
//
// SetInterest and RetireEntity do assert facts, and the vocabulary is what
// makes that safe rather than a promise a handler keeps. §4.13 records a
// stance as §4.8's lifecycle and analysis-policy facts, so these two write
// exactly those two predicates and only the values the four stances and a
// retirement spell; there is no method here that takes a predicate, a value or
// an authority from a request, which is why AssertFact stays forbidden on
// every surface in this file including this one.
//
// Ask, AcceptPlan and CreateEntity are absent, and their absence is the line
// §4.13 draws between the two halves of the feature. Babel proposes identity
// and only the operator creates it: a topic *question* is raised by a run and
// accepted through the Reality Inbox, which is RealityService's AcceptPlan
// under an operator's explicit acceptance of a displayed plan. A route that
// could mint an entity from a topic page would be the browser doing what the
// section reserves for that acceptance — except through SplitEntity, which
// creates the parts of an identity the operator has just said covers two
// things, and which cannot be used to name a new subject out of nothing
// because it needs a parent that already exists.
//
// The three reads are the writes' own preconditions, on FocusPolicyService's
// terms. Entity is how a refusal names the topic it is refusing about,
// EntityInterest is what the page was showing before the click and what it
// shows after, and Resolve follows the merge history so a stance stated about
// a folded identity lands on the entity that now speaks for it.
type TopicLedger interface {
	Entity(ctx context.Context, id string) (reality.Entity, error)
	EntityInterest(ctx context.Context, entityID string) (reality.Interest, error)
	MergeEntities(ctx context.Context, in reality.MergeInput) (reality.Resolution, error)
	Resolve(ctx context.Context, id string) (string, error)
	RetireEntity(ctx context.Context, entityID, operator, reason string) error
	SetInterest(ctx context.Context, entityID, operator, state, reason string) error
	SplitEntity(ctx context.Context, in reality.SplitInput) (reality.Resolution, []reality.Entity, error)
}

// TopicFiler is §4.13's filing write the topic routes may perform, satisfied
// by *frontier.Store.
//
// One method, and it is the one a split cannot finish without: the records the
// operator named as belonging to the new part have to move with it, in the
// same request, or the act he authorized happened only halfway. Unfile,
// NoTopic and the filing reads are absent here because they are the record
// surface's rather than the topic's, and this type exists so that a handler
// holding a filer for one purpose cannot quietly grow the others.
type TopicFiler interface {
	File(ctx context.Context, in frontier.FilingInput) (frontier.Filing, error)
}
