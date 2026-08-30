package cli

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/catalog"
)

// scanState is the observable state of one catalog scan. internal/web
// mirrors this shape field-for-field, so the CLI and the web interface
// report the same progress document.
type scanState struct {
	Running    bool   `json:"running"`
	Described  int    `json:"described"`
	Total      int    `json:"total"`
	Failed     int    `json:"failed"`
	Harness    string `json:"harness,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// scanRescanInterval is how long a plain listing request trusts the newest
// completed scan before starting another one. Only discovery can see a
// session created since the catalog was written, so an interactive surface
// has to repeat it — but a scan walks every source tree on the machine, so
// repeating it per poll would keep the disk busy for as long as a page stays
// open. An explicit refresh is never throttled, which is what makes waiting
// for this interval a choice rather than the only option.
const scanRescanInterval = 5 * time.Minute

// scanProgressEvery and scanProgressInterval throttle a command's progress
// lines: a cold run over thousands of sessions must never be silent, and it
// must never scroll one line per session either.
const (
	scanProgressEvery    = 25
	scanProgressInterval = time.Second
)

// scanners holds one coordinator per data directory, so every surface in
// this process shares a single background scan of a given catalog.
var (
	scannersMu sync.Mutex
	scanners   = map[string]*scanCoordinator{}
)

// scanner returns the process-wide coordinator for one data directory.
func scanner(dataDir string) *scanCoordinator {
	scannersMu.Lock()
	defer scannersMu.Unlock()
	c, ok := scanners[dataDir]
	if !ok {
		c = &scanCoordinator{
			dataDir:         dataDir,
			describeSession: describe,
			diagnostics:     io.Discard,
		}
		scanners[dataDir] = c
	}
	return c
}

// scanCoordinator owns the background catalog scans for one data directory.
// It exists because describing a large corpus takes minutes: a scan must
// therefore run on a context this process owns rather than an HTTP
// request's, must be single-flight so concurrent requests cannot multiply
// the work, and must publish honest progress while it runs.
type scanCoordinator struct {
	dataDir string

	mu              sync.Mutex
	state           scanState
	rows            []sessionRow
	refreshedAt     string
	loaded          bool
	finishedAt      time.Time
	describeSession sessionDescribeFunc
	diagnostics     io.Writer
}

// attach points the coordinator's diagnostics at a long-lived stream. A
// background scan outlives the request that started it, so it must never
// write to a per-request buffer.
func (c *scanCoordinator) attach(diagnostics io.Writer) {
	if diagnostics == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagnostics = diagnostics
}

// State reports the current scan state.
func (c *scanCoordinator) State() scanState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Listing reports the rows a request may answer with immediately, when they
// were obtained, and the scan state — having started a scan first when the
// newest one has aged out. Nothing here waits on a scan: that is the whole
// point, because describing a large corpus takes minutes.
func (c *scanCoordinator) Listing(ads []adapter.Adapter, roots []string) ([]sessionRow, string, scanState) {
	c.load()
	if c.stale() {
		c.Start(ads, roots)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows, c.refreshedAt, c.state
}

// load adopts whatever the persisted catalog already holds, so a fresh
// process serves a warm catalog without waiting for any scan. While a scan
// is running it re-reads on every call: the catalog is WAL-mode, so batches
// the scan has already committed are readable, which is what makes a cold
// listing fill in progressively instead of staying empty for minutes.
func (c *scanCoordinator) load() {
	c.mu.Lock()
	loaded, running := c.loaded, c.state.Running
	c.mu.Unlock()
	if loaded && !running {
		return
	}

	// Read outside the mutex: a scan's progress callback needs it, and a
	// catalog read must never stall progress reporting.
	startedRead := time.Now()
	rows, err := c.persisted()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = true
	if err != nil {
		c.diagnosef("warning: read session catalog: %s\n", Sanitize(err.Error()))
		return
	}
	if c.finishedAt.After(startedRead) {
		// A scan settled while this read was in flight; its rows are
		// authoritative and already stored.
		return
	}
	c.rows = rows
	c.refreshedAt = formatTime(time.Now().UTC())
}

// stale reports whether a listing request should start a background scan on
// its own: a process that has completed none knows nothing about sessions
// created since the catalog was written, and after that discovery is
// repeated only once the newest scan has aged out.
func (c *scanCoordinator) stale() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Running {
		return false
	}
	return c.finishedAt.IsZero() || time.Since(c.finishedAt) >= scanRescanInterval
}

// Start begins a scan unless one is already running. It is idempotent:
// attaching to a running scan returns that scan's state and starts nothing,
// which is what makes two concurrent refresh requests one scan.
func (c *scanCoordinator) Start(ads []adapter.Adapter, roots []string) scanState {
	c.mu.Lock()
	if c.state.Running {
		state := c.state
		c.mu.Unlock()
		return state
	}
	c.state = scanState{Running: true, StartedAt: formatTime(time.Now().UTC())}
	state := c.state
	worker := &app{stdout: io.Discard, stderr: c.diagnostics}
	describeSession := c.describeSession
	c.mu.Unlock()

	go c.run(worker, ads, roots, describeSession)
	return state
}

// run performs one scan on a context this process owns. That ownership is
// the fix for the defect this coordinator exists for: a browser reload
// cancels its HTTP request, and the sessions already described must not be
// discarded with it.
func (c *scanCoordinator) run(a *app, ads []adapter.Adapter, roots []string, describeSession sessionDescribeFunc) {
	ctx := context.Background()
	sessions, covered := a.scan(ctx, ads, roots)
	rows, err := a.listSessionRows(ctx, sessions, refreshScope(covered, roots), c.dataDir, false, describeSession, c.progress)
	c.finish(rows, err)
}

// progress publishes one describe attempt's outcome.
func (c *scanCoordinator) progress(p catalog.Progress) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Total, c.state.Described, c.state.Failed = p.Total, p.Described, p.Failed
	if p.Harness != "" {
		c.state.Harness = Sanitize(p.Harness)
	}
}

// finish records one scan's outcome. A failed scan keeps the rows the
// previous one produced: a listing that was correct a minute ago is a
// better answer than none, and the failure is reported alongside it.
func (c *scanCoordinator) finish(rows []sessionRow, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	c.state.Running = false
	c.state.FinishedAt = formatTime(now)
	c.finishedAt = now
	if err != nil {
		c.state.Error = Sanitize(err.Error())
		c.diagnosef("warning: session scan: %s\n", Sanitize(err.Error()))
		return
	}
	c.state.Error = ""
	c.rows = rows
	c.refreshedAt = c.state.FinishedAt
	c.loaded = true
}

// persisted reads the catalog as it stands, with no discovery and no
// describing, which is what lets a request be answered immediately. An
// empty scope and no refs make the refresh read-only by construction: it
// prunes nothing, describes nothing, and returns the whole catalog.
func (c *scanCoordinator) persisted() ([]sessionRow, error) {
	cache, err := catalog.Open(c.dataDir)
	if err != nil {
		return nil, err
	}
	defer cache.Close()
	never := func(catalog.Ref) (catalog.Row, bool) { return catalog.Row{}, false }
	cached, err := cache.Refresh(context.Background(), nil, nil, never, nil)
	if err != nil {
		return nil, err
	}
	// The inferred-title overlay is applied here too, and through the same
	// reader the terminal uses. The alternative — leaving it to whichever
	// surface remembered — is how the web app and `sessions list` come to show
	// two different titles for one session. A store that will not open costs
	// the overlay and not the listing: the derived titles beneath it are
	// complete on their own.
	overlay, err := readInferredOverlay(context.Background(), c.dataDir)
	if err != nil {
		c.diagnosef("warning: inferred titles unavailable: %s\n", Sanitize(err.Error()))
	}
	return decodeCatalogRows(cached, nil, overlay)
}

// diagnosef writes one coordinator diagnostic. Callers hold c.mu, which is
// also what serializes writes to the shared stream.
func (c *scanCoordinator) diagnosef(format string, args ...any) {
	if c.diagnostics == nil {
		return
	}
	(&app{stderr: c.diagnostics}).diagf(format, args...)
}

// scanProgress is a command's throttled stderr progress reporter. A cold
// listing describes every session on the machine, which takes minutes, so
// it reports what it is doing on stderr — never on stdout, which carries
// exactly one JSON document.
func (a *app) scanProgress() *scanProgressReporter {
	return &scanProgressReporter{app: a, last: time.Now()}
}

type scanProgressReporter struct {
	app       *app
	last      time.Time
	lastCount int
}

// report is a catalog progress callback. It stays silent until a run is
// large enough to be worth narrating, so a warm listing writes nothing.
func (r *scanProgressReporter) report(p catalog.Progress) {
	if p.Described == 0 {
		r.last = time.Now()
		return
	}
	if p.Described-r.lastCount < scanProgressEvery && time.Since(r.last) < scanProgressInterval {
		return
	}
	r.lastCount = p.Described
	r.last = time.Now()
	r.app.diagf("describing %d/%d (%s)...\n", p.Described, p.Total, Sanitize(p.Harness))
}
