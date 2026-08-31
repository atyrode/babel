package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
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
	// Dispositions and Reviver are #87's record actions. They are two
	// fields rather than one because they are two stores: the proposed
	// actions and their ledger live beside the frontier in internal/
	// disposition, and the revive transition is the frontier's own. A
	// deployment can hold either without the other — a durable file whose
	// disposition component would not open still renders revision chains —
	// so each route names the ones it needs and the rest keep answering.
	Dispositions DispositionService
	Reviver      FrontierReviver
	// Fleet and SyncJournal are issue #109's read half: the shared catalog
	// every host in the deployment commits to, and this machine's own
	// publication journal.
	//
	// Both are optional and their absence is a state rather than a fault. A
	// nil Fleet is local mode — there is no shared backend, so there are no
	// other hosts, and the routes in fleet.go answer that plainly instead of
	// refusing. A nil SyncJournal is a build with no publication journal
	// wired, which internal/fleet resolves to "local" rather than guessing on
	// its behalf.
	//
	// They are two fields because they are two things: the catalog is
	// authoritative about what committed globally, and the journal is the only
	// thing that knows what this machine staged while the catalog was
	// unreachable. Collapsing them would lose exactly the case SPEC.md §6.5
	// requires to stay visible.
	Fleet       FleetReader
	SyncJournal fleet.SyncJournal
	// Cookbook is the loaded analysis cookbook. It is read-only by
	// construction: a *cookbook.Set exposes lookups and nothing that
	// changes an asset.
	Cookbook *cookbook.Set
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
	LinksFrom(context.Context, string) ([]frontier.Link, error)
	LinksTo(context.Context, string) ([]frontier.Link, error)
	StatusHistory(context.Context, string) ([]frontier.StatusEvent, error)
	ReviewStatus(context.Context, frontier.Ref) (frontier.ReviewStatus, error)
	Unexplored(context.Context, int) ([]frontier.Hypothesis, error)
	// Revisions and Head are #87's chain reads. They are reads in the
	// strictest sense — one is the whole append-only chain a record belongs
	// to and the other is its last entry — so they belong here rather than
	// beside the revive transition, and neither can produce a revision: a
	// chain grows only when a run or `babel revise` appends to it, and
	// neither is reachable from a browser.
	Revisions(context.Context, frontier.Ref) ([]frontier.Revision, error)
	Head(context.Context, frontier.Ref) (frontier.Ref, error)
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
// operator: retaining an answer, and the single explicit acceptance that lets a
// plan's authoritative actions touch reality. Everything that could make a
// fact authoritative by another route — AssertFact, SupersedeFact,
// MergeEntities, ImportFacts, PutFocusRules — is deliberately absent, so no
// browser request can reach authority the operator did not exercise through
// the plan the ledger recorded.
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
	RecordAnswer(context.Context, reality.AnswerInput) (reality.Answer, error)
	AcceptPlan(context.Context, reality.AcceptanceInput) (reality.Acceptance, reality.Application, error)
}

// SearchIndex is the retrieval surface behind GET /api/search, satisfied by
// *index.Index. Indexing is not listed: the index is rebuilt by preparation,
// never by a browser asking to search.
type SearchIndex interface {
	Search(context.Context, index.Query) ([]index.Hit, error)
}

// RunLister supplies the run receipts GET /api/analysis/state lists, newest
// first, bounded by the caller's page.
//
// It is an interface rather than a *run.Store because internal/run exposes no
// receipt listing — it answers Receipt(id) and Revisions(runID) — so the
// listing is assembled by whatever wired this server. A nil provider reports
// no runs rather than an error: a build with no analysis history has nothing
// to list, which is different from a failure.
type RunLister interface {
	Runs(ctx context.Context, limit, offset int) ([]RunSummary, int, error)
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
	_ ReviewService      = (*review.Service)(nil)
	_ FrontierReader     = (*frontier.Store)(nil)
	_ FrontierReviver    = (*frontier.Store)(nil)
	_ RealityService     = (*reality.Store)(nil)
	_ DispositionService = (*disposition.Store)(nil)
	_ SearchIndex        = (*index.Index)(nil)
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
	TitleProvenance   *string `json:"title_provenance"`
	Workspace         *string `json:"workspace"`
	ContinuationGrade *bool   `json:"continuation_grade"`
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
