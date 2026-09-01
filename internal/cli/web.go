package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/reference"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/transcript"
	"github.com/atyrode/babel/internal/web"
	webdist "github.com/atyrode/babel/web"
)

const webUsage = `Usage: babel web [flags]

Serves the local web interface on 127.0.0.1 and prints a launch URL whose
fragment carries a one-time bootstrap nonce. Opening it exchanges the nonce
for a session cookie the page cannot read; the link then authenticates
nothing, so a second use needs a second launch. The page browses this
host's sessions, renders transcripts, and drives archive status, verify, and
fetch. Nothing binds beyond loopback and no command served here can delete
archived data.

Repository selection follows the usual precedence (flags, then
$BABEL_RESTIC_REPO/$BABEL_RESTIC_PASSWORD_FILE, then storage.json). Without
a repository the browser still works read-only; archive actions report
"not configured".

The analysis frontier, the review log, the Reality Ledger, the retrieval
index and the cookbook are opened for the server's lifetime when this
machine has them. A store that cannot be opened is reported on stderr and
its pages report it unavailable; the sessions and archive surfaces are
unaffected, so a machine with no analysis state still serves them.

Review and Reality decisions taken in the browser are attributed acts
(SPEC.md §4.7, §4.8) and reach the same services the headless commands do,
so they need the same operator identity. Without one the browser still
reads the frontier and the ledger; it cannot decide, answer, or accept.

Flags:
  --port N                    listen port (default: an ephemeral free port)
  --open                      also open the URL with the system browser
  --repo REPOSITORY           restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE        password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH        restic executable (default "restic" from $PATH)
  --host ID                   archive host identity for fetches
  --operator ID               operator identity (default $BABEL_OPERATOR)
`

// webCmd implements `babel web`. Every application dependency the server
// needs is an in-process invocation of the corresponding headless command
// in --json mode, so the web API cannot drift from the CLI contract: both
// surfaces share one implementation, one sanitizer, and one never-delete
// command set (SPEC.md §2.4, §8.2).
func (a *app) webCmd(ctx context.Context, args []string) error {
	c := newCmd("web", webUsage)
	var rf repoFlags
	rf.bind(c.fs)
	var of operatorFlags
	of.bind(c)
	port := c.fs.Int("port", 0, "listen port (default: ephemeral)")
	open := c.fs.Bool("open", false, "open the URL with the system browser")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	// The identity is resolved with the same helper, the same precedence and
	// the same validation `review decide` and `reality answer` use, and
	// differs from them in one way: a launch that cannot name an operator
	// still starts. Those commands do nothing but record an attributed
	// decision, so refusing is their whole job; this one mostly serves
	// reading, and taking the frontier, the ledger and the sessions listing
	// away over an identity only a mutation needs would be a worse answer
	// than letting each mutation refuse for itself.
	operator, err := of.resolve(c)
	if err != nil {
		operator = ""
		a.diagf("warning: %s; review and reality decisions will be refused\n",
			Sanitize(err.Error()))
	}

	srv, services, err := a.buildWebServer(rf, operator, *port)
	if err != nil {
		return err
	}
	// The durable handles live as long as the server does, so they are
	// released here rather than per request: Serve returns when the listener
	// is down and no handler can be running, which is the first moment
	// closing them cannot pull a database out from under a response.
	defer func() {
		if err := services.Close(); err != nil {
			a.diagf("warning: release analysis state: %s\n", Sanitize(err.Error()))
		}
	}()

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(a.stdout, "babel web listening at %s\n", srv.URL())
	// The sentence states the two properties the link actually has, and takes
	// the lifetime from the server rather than repeating it, so the printed
	// promise cannot drift from what is enforced.
	fmt.Fprintf(a.stdout,
		"open the URL once within %s; it authenticates a single browser session, then stops working. Press Ctrl-C to stop\n",
		web.BootstrapTTL)
	if *open {
		if err := openBrowser(srv.URL()); err != nil {
			a.diagf("warning: open browser: %s\n", Sanitize(err.Error()))
		}
	}
	return srv.Serve(ctx)
}

// syncWriter serializes writes onto one stream from concurrent producers.
//
// `babel web` is the one command with three of them: the scan coordinator's
// background goroutine, the server's per-request logger, and the command
// goroutine's own warnings. They share a single stderr, and an io.Writer
// carries no concurrency guarantee — for os.Stderr the consequence is two
// diagnostics interleaved into one unreadable line, and for the bytes.Buffer
// a test supplies it is a data race. A garbled diagnostic is a misleading
// diagnostic, so the serialization belongs at the point where the stream
// becomes shared rather than in each producer.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// buildWebServer assembles the server against this process's CLI surface.
// Kept separate from flag handling so tests can drive a live server.
//
// The returned webServices holds every durable handle the server borrows for
// its lifetime and the caller must close it once Serve has returned. It is
// never nil on success, and closing a nil one is harmless.
func (a *app) buildWebServer(rf repoFlags, operator string, port int) (*web.Server, *webServices, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	d, err := babelDirs()
	if err != nil {
		return nil, nil, err
	}
	repository := firstNonEmpty(rf.repository, os.Getenv("BABEL_RESTIC_REPO"), cfg.Repository)
	passwordFile := firstNonEmpty(rf.passwordFile, os.Getenv("BABEL_RESTIC_PASSWORD_FILE"), cfg.PasswordFile)
	configured := repository != "" && passwordFile != ""

	// Explicitly forward the resolved repository selection so every archive
	// sub-invocation sees exactly the state the server advertised, even if
	// the environment changes while the server runs.
	var forward []string
	if configured {
		forward = append(forward, "--repo", repository, "--password-file", passwordFile)
	}
	if b := firstNonEmpty(rf.binary, cfg.ResticBinary); b != "" {
		forward = append(forward, "--restic-binary", b)
	}

	hostID := firstNonEmpty(rf.host, os.Getenv("BABEL_HOST_ID"), cfg.HostID)
	if hostID == "" {
		if name, err := os.Hostname(); err == nil {
			hostID = sanitizeHostID(name)
		}
	}

	// From here this stream has concurrent producers, so it is guarded once
	// at the point it becomes shared: the scan coordinator's background
	// goroutine, the server's per-request logger, and this command's own
	// later warnings all reach stderr through this one writer. Guarding here
	// rather than in webCmd is what keeps a caller that builds a server
	// directly — every test below does — from racing.
	a.stderr = &syncWriter{w: a.stderr}

	// One coordinator per catalog, shared by every request this process
	// serves, so concurrent listings can never multiply the scan. Its
	// diagnostics go to the process's own stderr because a scan outlives
	// the request that started it.
	sc := scanner(d.data)
	sc.attach(a.stderr)

	opts := web.Options{
		Port:        port,
		Static:      webdist.Dist(),
		Diagnostics: a.stderr,
		State: web.StateProviderFunc(func(context.Context) (web.State, error) {
			return web.State{
				Configured: configured,
				Repository: Sanitize(repository),
				HostID:     Sanitize(hostID),
			}, nil
		}),
		Lister: web.SessionListerFunc(func(context.Context) (web.SessionsResult, error) {
			// The request's context is deliberately unused: the listing is
			// answered from the catalog immediately, and the scan that keeps
			// the catalog current belongs to this process, so a browser
			// reload can never cancel work already done.
			rows, refreshedAt, state := sc.Listing(adapters(), nil)
			return web.SessionsResult{
				Sessions:    webSessionRows(rows),
				RefreshedAt: refreshedAt,
				Scan:        webScanState(state),
			}, nil
		}),
		Scanner: &webScanner{coordinator: sc},
		Inspector: web.SessionInspectorFunc(func(ctx context.Context, selector string) (web.InspectResult, error) {
			var res web.InspectResult
			err := a.runJSON(ctx, &res, "sessions", "inspect", selector, "--json")
			return res, err
		}),
		Transcripts: web.TranscriptReaderFunc(transcript.Events),
		Operator:    operator,
	}
	if configured {
		opts.Archive = &webArchive{
			app:          a,
			forward:      forward,
			host:         rf.host,
			sessionsRoot: d.sessionsRoot(),
		}
	}

	services := a.openWebServices(d)
	opts.Review = services.review()
	opts.Frontier = services.frontier()
	opts.Runs = services.runs()
	opts.Reality = services.realityService()
	opts.Search = services.search()
	opts.Dispositions = services.dispositions()
	opts.Reviver = services.reviver()
	opts.Cookbook = services.cookbook
	opts.References = services.references()
	opts.Complaints = services.complaints()

	// The two identities one session has, for the citation surfaces. A machine
	// with no deployment identity leaves this nil, and internal/web renders
	// every session endpoint inert with that stated as the reason. That is the
	// only honest answer available: a session's durable key is a digest over
	// the deployment, the host, the harness and the source id, so keys derived
	// without a deployment would match nothing any host in the fleet minted,
	// and testing an edge's endpoint against them would report a real session
	// as absent.
	//
	// Nothing is warned about here, on the same terms the fleet reader below
	// stays silent when it is simply not configured: local mode is a supported
	// deployment rather than a fault, and the page that shows the endpoint is
	// where the reason belongs.
	services.sessions = newWebSessionKeys(sc, cfg.DeploymentID, hostID)
	opts.Sessions = services.sessionKeys()

	// The fleet read surface, when this machine has one. A launch on a
	// machine in local mode, or one whose payload keys are not placed, leaves
	// it nil and every fleet view reports that honestly rather than failing:
	// the local pages are what a single machine has always served, and a
	// missing shared backend must not cost an operator their own analysis
	// (issue #109 item 3).
	//
	// Any other failure is reported and also leaves it nil. That is
	// deliberate: a launch is not the moment to refuse over a shared backend,
	// and a warning naming the reason is what an operator can act on.
	if reader, err := fleet.OpenReader(context.Background(), cfg, hostID); err == nil {
		opts.Fleet = reader
		services.fleet = reader
	} else if !fleet.NotConfigured(err) {
		a.diagf("warning: the fleet read surface is unavailable: %s\n", Sanitize(err.Error()))
	}
	// The fleet presence read surface, on exactly the same terms (#118). It is
	// a second Open rather than a field of the fleet reader because the two
	// hold different authority: internal/fleet needs the object store and this
	// machine's payload keys to open a record, and presence needs neither —
	// #112 makes a presence row metadata-only, so this succeeds on a machine
	// whose keys are not placed and the Fleet view still answers there.
	//
	// Its diagnostic sink is this process's stderr, which is where the store
	// reports a read it could not perform. Nothing about that reaches the
	// browser: internal/web answers with a sentence naming the catalog, never
	// the error.
	if store, err := presence.Open(context.Background(), cfg, hostID, func(e error) {
		a.diagf("warning: fleet presence: %s\n", Sanitize(e.Error()))
	}); err == nil {
		opts.Presence = store
		services.presence = store
	} else if !presence.NotConfigured(err) {
		a.diagf("warning: the fleet presence surface is unavailable: %s\n", Sanitize(err.Error()))
	}
	// Options.SyncJournal is deliberately left nil here. The publication
	// journal is the only thing that can tell a record staged while
	// PostgreSQL was unreachable from one that was never staged at all, and
	// a build with no journal must say so rather than guess: fleet's
	// resolution reads a nil journal as "nothing here claims this record is
	// going anywhere", which is exactly what is true.

	srv, err := web.New(opts)
	if err != nil {
		// Nothing will ever call Serve, so the handles this launch opened
		// have to be released here or they leak for the process's life.
		if e := services.Close(); e != nil {
			a.diagf("warning: release analysis state: %s\n", Sanitize(e.Error()))
		}
		return nil, nil, err
	}
	return srv, services, nil
}

// webServices are the durable Phase B handles one served session borrows for
// its lifetime. They are opened once rather than per request because a
// browser's concurrent reads would otherwise each pay SQLite's setup cost and
// hold their own handle on the same durable file, and because every value
// here is the one the headless commands hold: the two surfaces reach one
// implementation, so a mutation cannot bypass the service checks §14 relies
// on (SPEC.md §14).
//
// Each is independently optional. None of them is a launch prerequisite: a
// machine with no analysis state, or one whose durable file a running
// exploration is committing to, still serves sessions, transcripts and the
// archive, and the pages above a store that would not open keep reporting it
// unavailable rather than the whole server failing to start.
type webServices struct {
	analysis *analysisState
	reality  *reality.Store
	index    *index.Index
	cookbook *cookbook.Set
	// fleet is the shared-catalog reader, nil on a machine with no fleet.
	// It is held here rather than only on Options because it owns a
	// PostgreSQL pool: a launch that fails after opening it, and a served
	// session that ends, both have to release it or it leaks for the
	// process's life.
	fleet *fleet.Reader
	// presence is the fleet presence reader, nil on a machine with no
	// deployment. It is held here for fleet's reason and no other: it owns a
	// PostgreSQL pool of its own, so a launch that fails after opening it and
	// a served session that ends both have to release it.
	presence *presence.Store
	// sessions resolves the durable session keys #113's edges record, nil on a
	// machine with no deployment identity. It holds no handle of its own: it
	// reads the scan coordinator's listing, which is the same listing the
	// Lister option serves, so the sessions page and a citation's endpoint can
	// never disagree about which sessions this host has.
	sessions *webSessionKeys
}

// openWebServices opens what this machine has, reporting what it does not.
//
// Every store is opened exactly the way its own commands open it —
// openAnalysisState for the frontier, the receipts and the review log above
// them, openReality for the ledger, index.Open on the cache directory for
// retrieval, and the embedded cookbook — so nothing here is a second way to
// reach durable state.
func (a *app) openWebServices(d dirs) *webServices {
	s := &webServices{}
	if state, err := openAnalysisState(); err != nil {
		a.diagf("warning: analysis state unavailable, so the frontier, findings and review pages will report it: %s\n",
			Sanitize(err.Error()))
	} else {
		s.analysis = state
		// The citation pages read this state and need no resolver, so a
		// smaller registry than this machine's stores could support costs
		// them nothing. It is still said, because the same state is what a
		// write would be validated against.
		if state.resolverNote != "" {
			a.diagf("note: %s\n", state.resolverNote)
		}
	}
	if store, err := openReality(); err != nil {
		a.diagf("warning: reality ledger unavailable, so the reality pages will report it: %s\n",
			Sanitize(err.Error()))
	} else {
		s.reality = store
	}
	if idx, err := index.Open(d.indexDir()); err != nil {
		a.diagf("warning: retrieval index unavailable, so search will report it: %s\n",
			Sanitize(err.Error()))
	} else {
		s.index = idx
	}
	// The cookbook is assets rather than state: it holds no handle, so it is
	// here only because the analysis page lists it beside the runs.
	if set, err := cookbook.Embedded(); err != nil {
		a.diagf("warning: cookbook unavailable, so the analysis page will list no recipes: %s\n",
			Sanitize(err.Error()))
	} else {
		s.cookbook = set
	}
	return s
}

// The accessors below return a typed nil-free interface value or nil,
// which is the whole point: web.Options tests each service for nil to decide
// whether its routes can answer, and a non-nil interface wrapping a nil
// pointer would pass that test and then panic inside a handler.
func (s *webServices) review() web.ReviewService {
	if s.analysis == nil {
		return nil
	}
	return s.analysis.review
}

func (s *webServices) frontier() web.FrontierReader {
	if s.analysis == nil {
		return nil
	}
	return s.analysis.frontier
}

// dispositions and reviver are #87's record actions. They are two accessors
// over one open analysis state because internal/web takes them as two fields:
// the proposed actions live in their own component of the durable file and the
// revive transition is the frontier's own write.
func (s *webServices) dispositions() web.DispositionService {
	if s.analysis == nil {
		return nil
	}
	return s.analysis.dispositions
}

func (s *webServices) reviver() web.FrontierReviver {
	if s.analysis == nil {
		return nil
	}
	return s.analysis.frontier
}

func (s *webServices) runs() web.RunLister {
	if s.analysis == nil {
		return nil
	}
	return webRuns{store: s.analysis.runs}
}

func (s *webServices) realityService() web.RealityService {
	if s.reality == nil {
		return nil
	}
	return s.reality
}

func (s *webServices) search() web.SearchIndex {
	if s.index == nil {
		return nil
	}
	return s.index
}

// references is #113's edge graph, read-only. The nil test reaches inside the
// analysis state as well as at it: a launch whose durable file opened but
// whose edge store did not would otherwise hand internal/web an interface
// holding a nil *reference.Store, which passes the option's nil test and then
// answers reads from nothing.
func (s *webServices) references() reference.Lister {
	if s.analysis == nil || s.analysis.references == nil {
		return nil
	}
	return s.analysis.references
}

// complaints is #115's operator steering. The nil test reaches inside the
// analysis state as well as at it, on references' reasoning: a launch whose
// durable file opened but whose complaint component did not would otherwise
// hand internal/web an interface holding a nil *complaint.Store, which passes
// the option's nil test and then answers from nothing.
func (s *webServices) complaints() web.ComplaintService {
	if s.analysis == nil || s.analysis.complaints == nil {
		return nil
	}
	return s.analysis.complaints
}

func (s *webServices) sessionKeys() web.SessionKeyResolver {
	if s.sessions == nil {
		return nil
	}
	return s.sessions
}

// Close releases every handle this launch opened, in the reverse of the order
// it opened them; analysisState.Close already encodes that order for the
// three it owns. The first failure is reported and the rest are still closed,
// because a handle left open by an early error is a file lock the next
// command would wait on.
//
// A nil receiver closes nothing, so a caller that could not build a server
// can defer this unconditionally.
func (s *webServices) Close() error {
	if s == nil {
		return nil
	}
	var err error
	// The fleet reader closes first because it is the only handle reaching a
	// remote service: a launch that ends with a PostgreSQL pool still open
	// holds server-side connections a managed provider counts, where the
	// local SQLite handles cost only this process a file lock.
	if s.fleet != nil {
		err = s.fleet.Close()
	}
	// The presence store is the second remote handle, closed beside the first
	// for the same reason: it owns its own PostgreSQL pool, so leaving it open
	// holds connections a managed provider counts.
	if s.presence != nil {
		if e := s.presence.Close(); err == nil {
			err = e
		}
	}
	if s.index != nil {
		if e := s.index.Close(); err == nil {
			err = e
		}
	}
	if s.reality != nil {
		if e := s.reality.Close(); err == nil {
			err = e
		}
	}
	if s.analysis != nil {
		if e := s.analysis.Close(); err == nil {
			err = e
		}
	}
	return err
}

// webRuns lists run receipts for GET /api/analysis/state.
//
// This is the one Phase B service no concrete value satisfies outright:
// internal/run enumerates receipts and internal/web declares the listing
// shape, so the mapping between them lives at the wiring site. It carries the
// header and nothing from the sealed body, which is exactly what §9's
// plaintext allowlist permits a listing to read.
type webRuns struct{ store *runstore.Store }

func (w webRuns) Runs(ctx context.Context, limit, offset int) ([]web.RunSummary, int, error) {
	receipts, total, err := w.store.Receipts(ctx, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	// Identifiers, the sync state and the timestamp are all Babel's own
	// minting, and they reach a React tree that escapes text, so unlike the
	// CLI's own rows these are not run through the terminal-safe renderer:
	// escaping for a terminal here would make this listing render
	// differently from every other Phase B view for no gain.
	out := make([]web.RunSummary, 0, len(receipts))
	for _, receipt := range receipts {
		h := receipt.Header
		out = append(out, web.RunSummary{
			ReceiptID:     string(h.ID),
			RunID:         h.RunID,
			PreparationID: string(h.PreparationID),
			Revision:      h.Revision,
			RecordedAt:    formatTime(h.RecordedAt),
			Sync:          h.Sync,
			Authority:     web.RunAuthority{Kind: string(h.Authority.Kind), Ref: h.Authority.Ref},
			Counts: web.RunCounts{
				ToolRequests: h.Counts.ToolRequests,
				ToolsDenied:  h.Counts.ToolsDenied,
				Retrieval:    h.Counts.Retrieval,
				Deferred:     h.Counts.Deferred,
				Rejected:     h.Counts.Rejected,
				Failures:     h.Counts.Failures,
				Redactions:   h.Counts.Redactions,
			},
		})
	}
	return out, total, nil
}

// webScanner exposes the process-wide background scanner to the server.
type webScanner struct {
	coordinator *scanCoordinator
}

func (w *webScanner) State() web.ScanState { return webScanState(w.coordinator.State()) }

func (w *webScanner) StartRefresh() web.ScanState {
	return webScanState(w.coordinator.Start(adapters(), nil))
}

// webScanState maps the CLI's scan state onto its web mirror.
func webScanState(state scanState) web.ScanState {
	return web.ScanState{
		Running:    state.Running,
		Described:  state.Described,
		Total:      state.Total,
		Failed:     state.Failed,
		Harness:    state.Harness,
		StartedAt:  state.StartedAt,
		FinishedAt: state.FinishedAt,
		Error:      state.Error,
	}
}

// webSessionRows maps the CLI's listing rows onto their web mirrors. Both
// shapes are already sanitized; this is the same document the headless
// `sessions list --json` emits, restated in the server's types.
func webSessionRows(rows []sessionRow) []web.SessionRow {
	out := make([]web.SessionRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, webSessionRow(row))
	}
	return out
}

// webSessionRow maps one row. It exists because the citation surfaces answer
// with individual rows picked out of the same listing, and two spellings of
// this mapping would be two answers to "what does the browser know about this
// session".
func webSessionRow(row sessionRow) web.SessionRow {
	return web.SessionRow{
		Harness:           row.Harness,
		SourceID:          row.SourceID,
		Selector:          row.Selector,
		Size:              row.Size,
		Modified:          row.Modified,
		Title:             row.Title,
		TitleProvenance:   row.TitleProvenance,
		Workspace:         row.Workspace,
		ContinuationGrade: row.Continuous,
	}
}

// webSessionKeys translates between a session's two identities for #113's
// citation surfaces: the durable key an edge records, and the selector every
// page routes on.
//
// It answers from the scan coordinator's listing rather than from the session
// catalog directly, and that is deliberate: the sessions page is served from
// exactly that listing, so a citation resolving to a session the page does not
// show - or failing to resolve one it does - is made impossible rather than
// merely unlikely. Nothing here starts a scan of its own; Listing is the same
// non-blocking read the Lister option performs.
//
// The digest is taken over the listing's harness and source id, which are the
// terminal-safe rendering of the catalog identities the publish path digests.
// That is exact rather than approximate: adapter.ValidSourceID admits only
// "/"-separated [A-Za-z0-9._-] segments and harness names are lowercase words,
// so Sanitize is the identity on both, and webArchive.materialization already
// rests on the same fact. A key derived from an escaped value would match
// nothing this deployment ever published.
type webSessionKeys struct {
	coordinator  *scanCoordinator
	deploymentID string
	hostID       string
}

// newWebSessionKeys builds the resolver, or nil when this machine cannot
// derive a durable session key at all. Nil is a state internal/web renders as
// a stated reason, so it is returned rather than a resolver that would answer
// every lookup under an identity no other host shares.
func newWebSessionKeys(sc *scanCoordinator, deploymentID, hostID string) *webSessionKeys {
	if sc == nil || deploymentID == "" || hostID == "" {
		return nil
	}
	return &webSessionKeys{coordinator: sc, deploymentID: deploymentID, hostID: hostID}
}

// SessionsByKey returns the rows among this host's sessions whose durable key
// was asked for.
//
// One pass answers the whole batch. A key is a digest and cannot be inverted,
// so a lookup means deriving the key of every local session; doing that per key
// would make a page of citations cost a pass over the corpus per endpoint. A
// key with no local session is absent from the result rather than an error,
// which is what an edge naming another host's session is.
//
// The request's context is unused for the reason the Lister option gives: the
// listing is answered from what the catalog already holds, and the scan that
// keeps it current belongs to this process rather than to any request.
func (w *webSessionKeys) SessionsByKey(_ context.Context, keys []string) (map[string]web.SessionRow, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	rows, _, _ := w.coordinator.Listing(adapters(), nil)
	out := make(map[string]web.SessionRow, len(wanted))
	for _, row := range rows {
		key := sharedcatalog.SessionUID(w.deploymentID, w.hostID, row.Harness, row.SourceID)
		if _, ok := wanted[key]; !ok {
			continue
		}
		out[key] = webSessionRow(row)
	}
	return out, nil
}

// KeyForSelector derives the durable key of one local session, reporting false
// for a selector this host has no session for.
//
// The selector is rebuilt from the row's own harness and source id rather than
// compared against the row's selector field, so the value that identifies the
// session and the value that is digested are the same two strings.
func (w *webSessionKeys) KeyForSelector(_ context.Context, selector string) (string, bool, error) {
	rows, _, _ := w.coordinator.Listing(adapters(), nil)
	for _, row := range rows {
		if row.Harness+"/"+row.SourceID != selector {
			continue
		}
		return sharedcatalog.SessionUID(w.deploymentID, w.hostID, row.Harness, row.SourceID), true, nil
	}
	return "", false, nil
}

// webArchive drives the archive commands in process. Only status, verify,
// listing, and fetch exist here; the CLI exposes no deletion, so neither can
// the web.
type webArchive struct {
	app *app
	// forward is the resolved repository selection every sub-invocation
	// receives: --repo, --password-file, and --restic-binary.
	forward []string
	// host is `babel web --host`, the launch-time archive host identity. It
	// is not folded into forward because it is not part of the repository
	// selection: `archive status` and `archive verify` bind the flag and
	// never read it, while `sessions list` and `sessions fetch` give it a
	// meaning — read that host's snapshot instead of this machine — that a
	// per-request value must be able to override.
	host string
	// sessionsRoot is where fetched session trees live. Resolved once at
	// launch rather than per request, because the data directory does not
	// move while a server runs.
	sessionsRoot string
}

func (w *webArchive) ArchiveStatus(ctx context.Context) (web.StatusResult, error) {
	var res web.StatusResult
	err := w.app.runJSON(ctx, &res, append([]string{"archive", "status", "--json"}, w.forward...)...)
	return res, err
}

func (w *webArchive) ArchiveVerify(ctx context.Context, deep bool) (web.VerifyResult, error) {
	args := []string{"archive", "verify", "--json"}
	if deep {
		args = append(args, "--deep")
	}
	var res web.VerifyResult
	err := w.app.runJSON(ctx, &res, append(args, w.forward...)...)
	// A failed verification still emits its result document (exit 1 with
	// ok=false); surface the parsed report rather than a bare error.
	if err != nil && res.Repository != "" {
		return res, nil
	}
	return res, err
}

// ArchiveSessions runs `babel sessions list --host`, the command that reads a
// snapshot's file listing instead of this machine's source trees.
//
// Nothing here reimplements that listing: the flag combinations the command
// refuses — --roots and --no-cache against --host, --snapshot without one —
// stay refused because this builds an invocation rather than calling past it,
// and a refusal arrives as the web bad-request sentinel through runJSON.
func (w *webArchive) ArchiveSessions(ctx context.Context, host, snapshot string) (web.ArchiveSessionsResult, error) {
	args := []string{"sessions", "list", "--host", host, "--json"}
	if snapshot != "" {
		args = append(args, "--snapshot", snapshot)
	}
	var listing sessionsResult
	if err := w.app.runJSON(ctx, &listing, append(args, w.forward...)...); err != nil {
		return web.ArchiveSessionsResult{}, err
	}
	return web.ArchiveSessionsResult{
		Host:     Sanitize(host),
		Snapshot: Sanitize(snapshot),
		Sessions: w.archiveRows(listing.Sessions),
	}, nil
}

// archiveRows narrows the command's listing rows to what a snapshot listing
// actually observed, and adds the one local fact this view needs.
//
// The four nullable fields are dropped rather than forwarded as nulls. They
// are null by construction here — rowFromArchived fills in only the identity,
// the selector, and the primary log's recorded size — so carrying them would
// hand a client a title field that is always absent and invite it to render
// the absence as an empty cell.
func (w *webArchive) archiveRows(rows []sessionRow) []web.ArchiveSessionRow {
	out := make([]web.ArchiveSessionRow, 0, len(rows))
	for _, row := range rows {
		archived := web.ArchiveSessionRow{
			Harness:  row.Harness,
			SourceID: row.SourceID,
			Selector: row.Selector,
			Size:     row.Size,
		}
		if dir, ok := w.materialization(row.Selector); ok {
			archived.Fetched, archived.FetchedPath = true, Sanitize(dir)
		}
		out = append(out, archived)
	}
	return out
}

// materialization reports whether this machine already holds a fetched copy of
// one archived session, and where.
//
// The answer is a directory test rather than a repository read, because the
// fetch naming rule is deterministic from the selector alone — that is what
// lets local prune find a fetched tree without opening the archive. The test
// is per selector and not per snapshot: the operator's question is whether
// this session was recovered here at all, and a session fetched from an older
// snapshot is still a session this machine holds.
//
// The selector arrives sanitized, which is exact here rather than approximate:
// adapter source ids are [A-Za-z0-9._-] segments and harness names are
// lowercase words, so the terminal-safe renderer is the identity on both.
func (w *webArchive) materialization(selector string) (string, bool) {
	if w.sessionsRoot == "" || selector == "" {
		return "", false
	}
	dir := filepath.Join(w.sessionsRoot, safeSessionDir(selector))
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

// FetchSession runs `babel sessions fetch`. A request that names no host
// inherits the launch selection, which is the same precedence the CLI applies
// to a flag against its configured default.
func (w *webArchive) FetchSession(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
	args := []string{"sessions", "fetch", req.Selector, "--json"}
	if req.Snapshot != "" {
		args = append(args, "--snapshot", req.Snapshot)
	}
	if host := firstNonEmpty(req.Host, w.host); host != "" {
		args = append(args, "--host", host)
	}
	var res web.FetchResult
	err := w.app.runJSON(ctx, &res, append(args, w.forward...)...)
	return res, err
}

// runJSON executes one headless command in process, decoding its --json
// stdout into out. Usage rejections map to the web bad-request sentinel and
// unmatched selectors to not-found, so the HTTP layer reports honest codes.
func (a *app) runJSON(ctx context.Context, out any, args ...string) error {
	var stdout, stderr bytes.Buffer
	scratch := &app{stdout: &stdout, stderr: &stderr}
	runErr := scratch.dispatch(ctx, args)

	var decodeErr error
	if stdout.Len() > 0 {
		decodeErr = json.Unmarshal(stdout.Bytes(), out)
	}
	if runErr != nil {
		var ue *usageError
		switch {
		case errors.As(runErr, &ue):
			return fmt.Errorf("%w: %s", web.ErrBadRequest, runErr.Error())
		case strings.Contains(runErr.Error(), "no local session matches"),
			strings.Contains(runErr.Error(), "no snapshot"):
			return fmt.Errorf("%w: %s", web.ErrNotFound, runErr.Error())
		}
		if decodeErr == nil && stdout.Len() > 0 {
			// The command failed after emitting its result document (e.g.
			// a failed verify); the caller decides what to surface.
			return runErr
		}
		return runErr
	}
	if stdout.Len() == 0 {
		return fmt.Errorf("command %q produced no output", strings.Join(args, " "))
	}
	return decodeErr
}

// openBrowser best-effort launches the system browser.
func openBrowser(url string) error {
	candidates := [][]string{{"xdg-open", url}, {"open", url}}
	var firstErr error
	for _, c := range candidates {
		path, err := exec.LookPath(c[0])
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return exec.Command(path, c[1:]...).Start()
	}
	return firstErr
}
