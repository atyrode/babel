package web

// GET /api/overview's own properties. The shared guard, the single method, the
// read-only property and the escaping are covered by the phaseBRoutes table
// this route is enrolled in; what is here is what only this route has: one
// document assembled from six services, each section degrading on its own, and
// no service failure reaching the client or the diagnostics stream.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
)

// overviewSessions is the catalog every overview test reads: three harnesses,
// one session the scan has not described yet, and titles from all three
// provenances, so the coverage split has something to split.
func overviewSessions() []SessionRow {
	return []SessionRow{
		{Harness: "omp", Selector: "omp/a", Title: new("newest work"), TitleProvenance: new("recorded"),
			Modified: new("2026-08-30T10:00:00Z")},
		{Harness: "omp", Selector: "omp/b", Title: new("derived name"), TitleProvenance: new("derived"),
			Modified: new("2026-08-29T10:00:00Z")},
		{Harness: "omp", Selector: "omp/c"},
		{Harness: "codex", Selector: "codex/d", Title: new("model guess"), TitleProvenance: new("inferred"),
			Modified: new("2026-08-28T10:00:00Z")},
		{Harness: "claude-code", Selector: "claude/e", Title: new("recorded name"), TitleProvenance: new("recorded"),
			Modified: new("2026-08-27T10:00:00Z")},
	}
}

func overviewWiring(opts *Options) {
	opts.State = StateProviderFunc(func(context.Context) (State, error) {
		return State{Configured: true, Repository: "sftp:archive:/srv/babel", HostID: "test-host"}, nil
	})
	opts.Lister = SessionListerFunc(func(context.Context) (SessionsResult, error) {
		return SessionsResult{
			Sessions:    overviewSessions(),
			RefreshedAt: "2026-08-31T08:00:00Z",
			Scan:        ScanState{Running: true, Described: 4, Total: 5},
		}, nil
	})
	opts.Archive = &fakeArchive{status: &StatusResult{
		Repository: "sftp:archive:/srv/babel",
		Snapshots:  3,
		Hosts: []StatusHostRow{
			{Host: "older-host", Snapshots: 1, LatestTime: "2026-08-20T09:00:00Z", LatestShortID: "aaaaaaaa"},
			{Host: "test-host", Snapshots: 2, LatestTime: "2026-08-30T09:00:00Z", LatestShortID: "bbbbbbbb"},
			{Host: "never-pushed", Snapshots: 0},
		},
		Catalog: &CatalogStatus{Reachable: true, Uncatalogued: new(1), Pending: new(2)},
	}}
	// The receipt's run identifier is the fixture frontier's own, because the
	// join between a receipt and the candidates it produced is the property
	// this row exists to carry.
	opts.Runs = runLister{{
		ReceiptID: "rcp-1", RunID: "run-1", PreparationID: "prep-1",
		Revision: 1, RecordedAt: "2026-08-30T12:00:00Z", Sync: "committed",
		Counts: RunCounts{Retrieval: 7, Deferred: 2, Failures: 1, Redactions: 3},
	}}
}

// TestOverviewAggregatesTheWiredServices reads the dashboard's whole snapshot
// off a deployment where every service answers, and checks that each panel
// reports the state its owning service holds rather than a number of the
// dashboard's own invention.
func TestOverviewAggregatesTheWiredServices(t *testing.T) {
	h := newPhaseB(t, "plain", overviewWiring)
	var got overview
	decodeResponse(t, h.get("/api/overview"), &got)

	if !got.Archive.Available || !got.Archive.Configured {
		t.Fatalf("archive section = %+v", got.Archive)
	}
	if got.Archive.Snapshots != 3 || got.Archive.HostsTotal != 3 {
		t.Errorf("archive totals = %d snapshots across %d hosts", got.Archive.Snapshots, got.Archive.HostsTotal)
	}
	// Newest publication first, and the host that never published sorts last
	// rather than reading as the most recent one.
	if len(got.Archive.Hosts) != 3 || got.Archive.Hosts[0].Host != "test-host" ||
		got.Archive.Hosts[2].Host != "never-pushed" {
		t.Errorf("archive hosts = %+v", got.Archive.Hosts)
	}
	if got.Archive.LatestTime != "2026-08-30T09:00:00Z" {
		t.Errorf("latest snapshot time = %q", got.Archive.LatestTime)
	}
	if got.Archive.Uncatalogued == nil || *got.Archive.Uncatalogued != 1 ||
		got.Archive.Pending == nil || *got.Archive.Pending != 2 ||
		got.Archive.Reachable == nil || !*got.Archive.Reachable {
		t.Errorf("catalog lag = %+v", got.Archive)
	}

	if !got.Corpus.Available || got.Corpus.Sessions != 5 || got.Corpus.Titled != 4 {
		t.Errorf("corpus section = %+v", got.Corpus)
	}
	if got.Corpus.Recorded != 2 || got.Corpus.Derived != 1 || got.Corpus.Inferred != 1 {
		t.Errorf("title provenance split = recorded %d derived %d inferred %d",
			got.Corpus.Recorded, got.Corpus.Derived, got.Corpus.Inferred)
	}
	if got.Corpus.Pending != 1 || !got.Corpus.Scan.Running {
		t.Errorf("scan state = %+v pending %d", got.Corpus.Scan, got.Corpus.Pending)
	}
	if len(got.Corpus.Harnesses) != 3 || got.Corpus.Harnesses[0].Harness != "omp" ||
		got.Corpus.Harnesses[0].Sessions != 3 || got.Corpus.Harnesses[0].Titled != 2 {
		t.Errorf("harness rows = %+v", got.Corpus.Harnesses)
	}

	// The catalog's newest rows, newest first, with the undescribed session
	// last: it has no modification time, and a row that sorted it first
	// would report an unread session as the latest activity.
	if !got.Activity.Available || len(got.Activity.Rows) != overviewRows {
		t.Fatalf("activity section = %+v", got.Activity)
	}
	if got.Activity.Rows[0].Selector != "omp/a" || got.Activity.Rows[4].Selector != "omp/c" {
		t.Errorf("activity order = %+v", got.Activity.Rows)
	}
	if got.Activity.Rows[4].Title != nil || got.Activity.Rows[4].Modified != nil {
		t.Errorf("an undescribed session claimed a title or a time: %+v", got.Activity.Rows[4])
	}

	// Seven candidates exist, and the dashboard counts the deployment
	// rather than the machine. Five are this machine's — the whole frontier
	// as §5.2 keeps it, including the superseded original of the
	// operator-revised chain and the rejected candidate the revive fixture
	// needs, neither of which is enrolled for review or unexplored. Two more
	// are the fleet's committed candidates — the other laptop's and the
	// unattributed one — where the staged candidate is not globally
	// reviewable and the sealed one this instance cannot open has no status
	// to count.
	if !got.Frontier.Available || got.Frontier.Hypotheses != 7 || got.Frontier.Truncated {
		t.Errorf("frontier section = %+v", got.Frontier)
	}
	// All six exploration statuses, in §4.2 order, zeros included: a
	// distribution that dropped its empty statuses would hide "rejected".
	wantStatuses := []string{"untriaged", "queued", "investigating", "deferred", "rejected", "promoted"}
	if len(got.Frontier.Statuses) != len(wantStatuses) {
		t.Fatalf("status distribution = %+v", got.Frontier.Statuses)
	}
	for i, want := range wantStatuses {
		if got.Frontier.Statuses[i].Status != want {
			t.Errorf("status %d = %q, want %q", i, got.Frontier.Statuses[i].Status, want)
		}
	}
	// The distribution adds up to the total, and it is a distribution over
	// the deployment: this machine holds four untriaged candidates and one
	// rejected, and the two investigating ones are the fleet's, a number
	// this machine's own corpus cannot produce. A dashboard that counted
	// only local records would report a private backlog where the
	// deployment has one shared body of work.
	counts := map[string]int{}
	for _, status := range got.Frontier.Statuses {
		counts[status.Status] = status.Count
	}
	if counts["untriaged"] != 4 || counts["rejected"] != 1 || counts["investigating"] != 2 {
		t.Errorf("status distribution = %+v", got.Frontier.Statuses)
	}
	if len(got.Frontier.Rows) != 5 {
		t.Fatalf("frontier rows = %+v", got.Frontier.Rows)
	}
	// The candidate arrives in the model's own wording, whole. Which row it
	// is depends on creation order, so every row is searched rather than the
	// first two.
	var carried bool
	for _, row := range got.Frontier.Rows {
		carried = carried || strings.Contains(row.Statement, "verification may be reported rather than performed")
	}
	if !carried {
		t.Errorf("no row carries the fixture's statement: %+v", got.Frontier.Rows)
	}
	// Every row states which machine produced it. All five are this
	// machine's here: the panel shows the newest overviewRows candidates,
	// this machine's are created as the fixture runs and the fleet's are
	// dated months earlier, so the fleet's contribution to this panel is
	// the count above rather than a row. A row that claimed no attribution
	// would be the failure migrations/0007 exists to prevent, so the
	// marking is checked even when every row is local.
	for _, row := range got.Frontier.Rows {
		if !row.LocalHost || !row.HostAttributed || row.HostID != localFleetHost {
			t.Errorf("a row on this machine's frontier is misattributed: %+v", row)
		}
	}

	if !got.Review.Available || got.Review.Awaiting != 4 || len(got.Review.Rows) != 4 {
		t.Errorf("review section = %+v", got.Review)
	}
	for _, row := range got.Review.Rows {
		if row.Excerpt == "" || row.ID == "" || row.Type == "" {
			t.Errorf("review row is missing its record: %+v", row)
		}
	}
	if !got.Review.Questions.Available || got.Review.Questions.Open != 2 ||
		len(got.Review.Questions.Rows) != 2 {
		t.Errorf("question inbox = %+v", got.Review.Questions)
	}
	if got.Review.Questions.Rows[0].Prompt == "" || got.Review.Questions.Rows[0].State == "" {
		t.Errorf("question row = %+v", got.Review.Questions.Rows[0])
	}

	if !got.Runs.Available || got.Runs.Total != 1 || len(got.Runs.Rows) != 1 {
		t.Fatalf("runs section = %+v", got.Runs)
	}
	run := got.Runs.Rows[0]
	if run.ReceiptID != "rcp-1" || run.Retrievals != 7 || run.Deferred != 2 ||
		run.Failures != 1 || run.Redactions != 3 {
		t.Errorf("run row counts = %+v", run)
	}
	// Every candidate this run put on the frontier, counted from the page of
	// recent candidates the dashboard indexes by run, and the §5.1 recipe
	// read from the observation it recorded.
	if run.Hypotheses != 5 {
		t.Errorf("run hypotheses = %d, want 5", run.Hypotheses)
	}
	if len(run.Recipes) != 1 || run.Recipes[0].ID != "outcome-integrity" || run.Recipes[0].Version != 1 {
		t.Errorf("run recipes = %+v", run.Recipes)
	}
}

// TestOverviewDegradesSectionBySection is the property that makes the dashboard
// usable on a partial deployment: a launch with no services answers, every
// panel says what it could not read, and nothing claims a zero it did not
// observe.
func TestOverviewDegradesSectionBySection(t *testing.T) {
	s, httpServer := testServer(t, Options{})
	response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/overview", bootstrapSession(t, s, httpServer))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.StatusCode, body(t, response))
	}
	var got overview
	decodeResponse(t, response, &got)

	for _, section := range []struct {
		name string
		got  overviewSection
	}{
		{"archive", got.Archive.overviewSection},
		{"corpus", got.Corpus.overviewSection},
		{"frontier", got.Frontier.overviewSection},
		{"review", got.Review.overviewSection},
		{"questions", got.Review.Questions.overviewSection},
		{"runs", got.Runs.overviewSection},
		{"activity", got.Activity.overviewSection},
	} {
		if section.got.Available {
			t.Errorf("%s reported itself available with no service behind it", section.name)
		}
		if section.got.Unavailable == "" {
			t.Errorf("%s is unavailable without saying so", section.name)
		}
	}
	// A section that could not be read reports no rows and no totals, and the
	// arrays are still arrays: a client must not have to distinguish an empty
	// panel from a null one.
	if got.Corpus.Sessions != 0 || got.Frontier.Hypotheses != 0 || got.Review.Awaiting != 0 ||
		got.Runs.Total != 0 || got.Archive.Snapshots != 0 {
		t.Errorf("an unreadable section claimed a count: %+v", got)
	}
	if got.Activity.Rows == nil || got.Corpus.Harnesses == nil || got.Frontier.Rows == nil ||
		got.Review.Rows == nil || got.Review.Questions.Rows == nil || got.Runs.Rows == nil ||
		got.Archive.Hosts == nil {
		t.Errorf("an unreadable section serialized a null array: %+v", got)
	}
	// The lifecycle is still described, because the panel showing it is
	// describing §4.2 rather than reporting a count.
	if len(got.Frontier.Statuses) != 6 {
		t.Errorf("status distribution = %+v", got.Frontier.Statuses)
	}
	// Absence, not zero: an unconfigured deployment has no catalog to be
	// behind, so the lag is unknown rather than nil-free.
	if got.Archive.Uncatalogued != nil || got.Archive.Pending != nil || got.Archive.Reachable != nil {
		t.Errorf("an unread catalog reported a lag: %+v", got.Archive)
	}
}

// TestOverviewLeaksNoServiceFailure drives each failing dependency and checks
// both channels a failure could escape through. §9 keeps credentials, paths and
// corpus text out of what a client is told and out of the diagnostics stream,
// and a per-section note is read by the same rule as a refusal: the section
// says it could not be read, and the reason stays in the process.
func TestOverviewLeaksNoServiceFailure(t *testing.T) {
	const secret = "sftp:archive:/srv/babel password=CREDENTIALSENTINEL"
	for _, deployment := range []struct {
		name  string
		build func(*Options)
	}{
		{
			name: "storage state",
			build: func(opts *Options) {
				opts.State = StateProviderFunc(func(context.Context) (State, error) {
					return State{}, fmt.Errorf("read storage.json: %s", secret)
				})
			},
		},
		{
			name: "repository and catalog",
			build: func(opts *Options) {
				opts.State = StateProviderFunc(func(context.Context) (State, error) {
					return State{Configured: true, Repository: "repo", HostID: "host"}, nil
				})
				opts.Archive = &fakeArchive{statusErr: errors.New("restic: " + secret)}
				opts.Lister = SessionListerFunc(func(context.Context) (SessionsResult, error) {
					return SessionsResult{}, errors.New("open catalog: " + secret)
				})
			},
		},
	} {
		t.Run(deployment.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			opts := Options{Diagnostics: &diagnostics}
			deployment.build(&opts)
			s, httpServer := testServer(t, opts)
			response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/overview", bootstrapSession(t, s, httpServer))
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			text := body(t, response)
			if strings.Contains(text, "CREDENTIALSENTINEL") || strings.Contains(text, "restic:") ||
				strings.Contains(text, "storage.json") || strings.Contains(text, "open catalog") {
				t.Errorf("the response carries a service failure's own text: %s", text)
			}
			if strings.Contains(diagnostics.String(), "CREDENTIALSENTINEL") {
				t.Errorf("the diagnostics stream carries the secret: %s", diagnostics.String())
			}
			// Non-vacuity: the failing section really did fail, and said so.
			var got overview
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("decode overview: %v", err)
			}
			if got.Archive.Available || got.Archive.Unavailable == "" {
				t.Errorf("archive section = %+v", got.Archive)
			}
			// The refusal is reported to the operator's own stream, so a
			// failure is not silent either.
			if !strings.Contains(diagnostics.String(), "/api/overview") {
				t.Errorf("no diagnostic names the refused read: %q", diagnostics.String())
			}
		})
	}
}

// countingFrontier is a frontier large enough that how the dashboard reads it
// is the difference between a page and a timeout. It answers both of the
// enumerations this package can reach — the store's aggregate listing and the
// unexplored queue the listing routes assemble identifiers from — and counts
// every single-record read it is asked for, so which one the dashboard chose
// is observable rather than inferred from a stopwatch.
type countingFrontier struct {
	FrontierReader
	// records are ordered by creation ascending, the order Hypotheses
	// returns: §5.4 keeps a list position from reading as strength, so the
	// store offers no newest-first enumeration and the newest candidates
	// are the last page.
	records []frontier.Hypothesis
	reads   atomic.Int64
}

func (f *countingFrontier) Hypothesis(_ context.Context, id string) (frontier.Hypothesis, error) {
	f.reads.Add(1)
	for _, record := range f.records {
		if record.ID == id {
			return record, nil
		}
	}
	return frontier.Hypothesis{}, frontier.ErrUnknownEntity
}

func (f *countingFrontier) Unexplored(_ context.Context, limit int) ([]frontier.Hypothesis, error) {
	return f.records[:min(limit, len(f.records))], nil
}

func (f *countingFrontier) ObservationsFor(context.Context, string) ([]frontier.Observation, error) {
	return nil, nil
}

// Hypotheses applies the narrowing and the bounds the store applies, because
// both are what the dashboard's panel is built out of: a status count is the
// total beside a one-record page, and the rows come from the last page.
func (f *countingFrontier) Hypotheses(_ context.Context,
	filter frontier.ListFilter) ([]frontier.Hypothesis, int, error) {
	matched := make([]frontier.Hypothesis, 0, len(f.records))
	for _, record := range f.records {
		if len(filter.Statuses) == 0 || slices.Contains(filter.Statuses, record.Status) {
			matched = append(matched, record)
		}
	}
	limit := frontier.DefaultListLimit
	if filter.Limit > 0 {
		limit = min(filter.Limit, frontier.MaxListLimit)
	}
	start := min(filter.Offset, len(matched))
	return matched[start:min(start+limit, len(matched))], len(matched), nil
}

// emptyQueue is the review surface with nothing enrolled. It is here so the
// frontier is the only thing the dashboard could enumerate candidates from,
// which is what makes the read count below attributable.
type emptyQueue struct{ ReviewService }

func (emptyQueue) Queue(context.Context, review.QueueFilter) ([]review.QueueItem, error) {
	return nil, nil
}

// TestOverviewTalliesTheFrontierWithoutReadingTheCorpus is the property that
// keeps the dashboard answerable on a real deployment. The panel needs a
// distribution and the newest overviewRows rows; reading every candidate to
// produce them cost thirty-one seconds on a two-thousand-candidate frontier,
// past the browser's own timeout, so the whole document was discarded and the
// dashboard rendered empty. The count of single-record reads is therefore a
// property of this route and not a performance note: it must not grow with the
// corpus.
func TestOverviewTalliesTheFrontierWithoutReadingTheCorpus(t *testing.T) {
	const corpus = 500
	statuses := []frontier.Status{
		frontier.StatusUntriaged, frontier.StatusQueued, frontier.StatusRejected,
	}
	fake := &countingFrontier{records: make([]frontier.Hypothesis, corpus)}
	base := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	for i := range fake.records {
		fake.records[i] = frontier.Hypothesis{
			ID:        fmt.Sprintf("hyp_%03d", i),
			RunID:     "run-1",
			Status:    statuses[i%len(statuses)],
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
			Payload:   frontier.HypothesisPayload{Statement: fmt.Sprintf("candidate %d", i)},
		}
	}
	s, httpServer := testServer(t, Options{
		Frontier: fake,
		Review:   emptyQueue{},
		Runs: runLister{{
			ReceiptID: "rcp-1", RunID: "run-1", RecordedAt: "2026-04-02T08:00:00Z",
		}},
	})
	response := request(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/api/overview", bootstrapSession(t, s, httpServer))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.StatusCode, body(t, response))
	}
	var got overview
	decodeResponse(t, response, &got)

	// No candidate is read on its own. The panel is six aggregates and one
	// page, so this is zero rather than a small number, and a read count
	// that tracked the corpus would be the defect returning.
	if reads := fake.reads.Load(); reads > overviewRows {
		t.Errorf("the dashboard read %d candidates one at a time across a corpus of %d", reads, corpus)
	}
	// The aggregates are the store's, and they still describe the whole
	// corpus: 167 untriaged, 167 queued and 166 rejected out of 500, with
	// the statuses nothing holds reported as the zeros they are.
	if !got.Frontier.Available || got.Frontier.Hypotheses != corpus || got.Frontier.Truncated {
		t.Fatalf("frontier section = %+v", got.Frontier)
	}
	counted := 0
	for _, status := range got.Frontier.Statuses {
		counted += status.Count
	}
	if counted != corpus {
		t.Errorf("status distribution sums to %d over a corpus of %d: %+v",
			counted, corpus, got.Frontier.Statuses)
	}
	if got.Frontier.Statuses[0].Count != 167 || got.Frontier.Statuses[1].Count != 167 ||
		got.Frontier.Statuses[4].Count != 166 || got.Frontier.Statuses[2].Count != 0 {
		t.Errorf("status distribution = %+v", got.Frontier.Statuses)
	}
	// The rows are the newest candidates, newest first, which is the page
	// the panel shows and the order it shows it in.
	if len(got.Frontier.Rows) != overviewRows {
		t.Fatalf("frontier rows = %+v", got.Frontier.Rows)
	}
	for i, row := range got.Frontier.Rows {
		want := fmt.Sprintf("hyp_%03d", corpus-1-i)
		if row.ID != want {
			t.Errorf("row %d = %q, want %q", i, row.ID, want)
		}
	}
	// The runs panel still gets the run-to-candidate index it annotates its
	// rows from, off the same page rather than off a per-record walk.
	if !got.Runs.Available || len(got.Runs.Rows) != 1 {
		t.Fatalf("runs section = %+v", got.Runs)
	}
	if got.Runs.Rows[0].Hypotheses != corpus {
		t.Errorf("run candidates = %d, want %d", got.Runs.Rows[0].Hypotheses, corpus)
	}
}
