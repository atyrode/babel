package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// fakeReceipts is one machine's local receipts. It is the whole store rather
// than a listing, because what the Watch surface reads is the half of a receipt
// a listing cannot carry.
type fakeReceipts struct {
	receipts []run.Receipt
	err      error
}

func (f *fakeReceipts) Receipts(_ context.Context, limit, offset int) ([]run.Receipt, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	if offset >= len(f.receipts) {
		return nil, len(f.receipts), nil
	}
	end := min(offset+limit, len(f.receipts))
	return f.receipts[offset:end], len(f.receipts), nil
}

func (f *fakeReceipts) Revisions(_ context.Context, runID string) ([]run.Receipt, error) {
	if f.err != nil {
		return nil, f.err
	}
	var chain []run.Receipt
	for _, receipt := range f.receipts {
		if receipt.Header.RunID == runID {
			chain = append(chain, receipt)
		}
	}
	return chain, nil
}

// fakeSeriesFrontier answers the per-day counts and the by-run records, and
// embeds FrontierReader so the rest of that surface stays absent: this fixture
// is about the two reads the Watch surface performs.
type fakeSeriesFrontier struct {
	FrontierReader
	days    []frontier.RecordDay
	outputs map[string][]frontier.RunOutput
	err     error
}

func (f *fakeSeriesFrontier) RecordDays(_ context.Context, _ time.Time) ([]frontier.RecordDay, error) {
	return f.days, f.err
}

func (f *fakeSeriesFrontier) OutputsOfRun(_ context.Context, runID string) ([]frontier.RunOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.outputs[runID], nil
}

// fakeLauncher is this machine's supervised children, and a record of what the
// routes asked it to do.
type fakeLauncher struct {
	children []LaunchedRun
	launched LaunchRequest
	launchOK LaunchedRun
	launchNo error
	stopped  int
	stopNo   error
}

func (f *fakeLauncher) Launched() []LaunchedRun { return f.children }

func (f *fakeLauncher) Launch(_ context.Context, req LaunchRequest) (LaunchedRun, error) {
	f.launched = req
	if f.launchNo != nil {
		return LaunchedRun{}, f.launchNo
	}
	return f.launchOK, nil
}

func (f *fakeLauncher) Stop(_ context.Context, pid int) (StopResult, error) {
	f.stopped = pid
	if f.stopNo != nil {
		return StopResult{}, f.stopNo
	}
	return StopResult{Method: "stop-file", Detail: "asked to stop at its next safe point"}, nil
}

type fakeDrain struct {
	report   DrainReport
	observed bool
}

func (f *fakeDrain) LastDrain() (DrainReport, bool) { return f.report, f.observed }

// watchRequest performs one authenticated GET against the Watch surface and
// decodes the document as a map, so a test can assert that a key is absent
// rather than zero — which is the promise this surface makes about every
// number it does not know.
func watchRequest(t *testing.T, opts Options, path string) map[string]any {
	t.Helper()
	s, httpServer := testServer(t, opts)
	session := bootstrapSession(t, s, httpServer)
	response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+path, session)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", path, response.StatusCode)
	}
	var document map[string]any
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

// eastern is a zone whose wall clock is a day ahead of UTC for part of the day.
// Timestamps are built in it so that a bucket taken from the local date rather
// than from the UTC one lands on the wrong day and the test notices.
var eastern = time.FixedZone("east", 5*60*60)

// TestWatchSeriesBucketsByUTCDayAndOmitsUnknownSpend is the series' two
// promises: a day is a UTC day whoever asks, and a day whose receipts recorded
// no cost has no cost rather than a zero one.
func TestWatchSeriesBucketsByUTCDayAndOmitsUnknownSpend(t *testing.T) {
	// Yesterday at 20:00 UTC, which is 01:00 tomorrow in the fixture's zone:
	// a bucket read off the local date would file it a day late.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Truncate(24 * time.Hour)
	spentAt := yesterday.Add(20 * time.Hour).In(eastern)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	unpricedAt := today.Add(9 * time.Hour)

	// The catalog records a session's time in whatever zone the harness
	// reported, so this one carries an offset whose wall-clock date is the
	// day after the instant's UTC date.
	sessionModified := yesterday.Add(21 * time.Hour).In(eastern).Format(time.RFC3339)
	unknownModified := ""
	opts := Options{
		Frontier: &fakeSeriesFrontier{days: []frontier.RecordDay{
			{Day: yesterday.Format(time.DateOnly), Kind: "hypothesis", Count: 3},
			{Day: yesterday.Format(time.DateOnly), Kind: "observation", Count: 7},
			{Day: today.Format(time.DateOnly), Kind: "proposal", Count: 1},
			// A day outside the window is dropped rather than folded into
			// the nearest bucket.
			{Day: "2020-01-01", Kind: "finding", Count: 99},
		}},
		Lister: SessionListerFunc(func(context.Context) (SessionsResult, error) {
			return SessionsResult{Sessions: []SessionRow{
				{Selector: "omp/a", Modified: &sessionModified},
				{Selector: "omp/b", Modified: &sessionModified},
				// A row the catalog has no time for belongs to no day.
				{Selector: "omp/c", Modified: nil},
				// An unparseable time is not today either.
				{Selector: "omp/d", Modified: &unknownModified},
			}}, nil
		}),
		Receipts: &fakeReceipts{receipts: []run.Receipt{
			pricedReceipt("run_paid", spentAt, 1.25),
			unpricedReceipt("run_free", unpricedAt),
		}},
	}

	document := watchRequest(t, opts, "/api/watch/series?days=3")
	days, ok := document["days"].([]any)
	if !ok || len(days) != 3 {
		t.Fatalf("the series holds %v, want three whole days", document["days"])
	}
	byDay := map[string]map[string]any{}
	for _, day := range days {
		row := day.(map[string]any)
		byDay[row["day"].(string)] = row
	}

	priced := byDay[yesterday.Format(time.DateOnly)]
	if priced == nil {
		t.Fatalf("the series has no row for %s: %v", yesterday.Format(time.DateOnly), byDay)
	}
	if got := priced["spend_usd"]; got != json.Number("1.25") && got != 1.25 {
		t.Errorf("spend on the day the run started = %v, want 1.25 — the bucket is the UTC day", got)
	}
	records := priced["records"].(map[string]any)
	if records["hypothesis"] != 3.0 || records["observation"] != 7.0 {
		t.Errorf("records on that day = %v, want 3 hypotheses and 7 observations", records)
	}
	// A kind that produced nothing is a measured zero, because the store
	// answered for every day in the window.
	if records["finding"] != 0.0 {
		t.Errorf("findings on that day = %v, want a counted zero", records["finding"])
	}
	if priced["sessions"] != 2.0 {
		t.Errorf("sessions on that day = %v, want the two the catalog dated", priced["sessions"])
	}

	unpriced := byDay[today.Format(time.DateOnly)]
	if unpriced == nil {
		t.Fatalf("the series has no row for today: %v", byDay)
	}
	if _, present := unpriced["spend_usd"]; present {
		t.Errorf("a day whose receipt recorded no usage reports spend %v, want the key absent",
			unpriced["spend_usd"])
	}
	if unpriced["sessions"] != 0.0 {
		t.Errorf("sessions today = %v, want a counted zero", unpriced["sessions"])
	}

	// The sources say who answered, which is what keeps an unopened store
	// from reading as a fortnight of inactivity.
	sources := document["sources"].(map[string]any)
	for _, name := range []string{"records", "sessions", "spend"} {
		section := sources[name].(map[string]any)
		if section["available"] != true {
			t.Errorf("the %s series reports unavailable: %v", name, section)
		}
	}
	reviews := sources["reviews"].(map[string]any)
	if reviews["available"] != false || !strings.Contains(reviews["unavailable"].(string), "evaluation store") {
		t.Errorf("a session with no evaluation store says %v, want it named", reviews)
	}
	if _, present := byDay[today.Format(time.DateOnly)]["reviews"]; present {
		t.Error("a series with no evaluation store reports a review count anyway")
	}
}

// TestWatchSeriesCapsTheWindow keeps one request from becoming a scan of the
// whole history: ninety days is the most any caller gets, whatever it asks for.
func TestWatchSeriesCapsTheWindow(t *testing.T) {
	document := watchRequest(t, Options{}, "/api/watch/series?days=3650")
	if got := document["days_requested"]; got != float64(watchSeriesMaxDays) {
		t.Errorf("a request for ten years was answered with %v days, want %d", got, watchSeriesMaxDays)
	}
	if days := document["days"].([]any); len(days) != watchSeriesMaxDays {
		t.Errorf("the series holds %d days, want %d", len(days), watchSeriesMaxDays)
	}
}

// TestWatchRunDetailCarriesTheBodyNobodyCouldSee is the run page's reason to
// exist: what the run searched, what it fetched, what it declined and what it
// spent, none of which a receipt listing can show.
func TestWatchRunDetailCarriesTheBodyNobodyCouldSee(t *testing.T) {
	opts := Options{
		Receipts: &fakeReceipts{receipts: []run.Receipt{tracedReceipt(t, "run_traced")}},
		Frontier: &fakeSeriesFrontier{outputs: map[string][]frontier.RunOutput{
			"run_traced": {{
				Kind:  frontier.EntityProposal,
				ID:    "prp_1",
				Title: "write the script the operator asked for",
			}},
		}},
	}

	document := watchRequest(t, opts, "/api/watch/runs/run_traced")

	steps := document["retrieval"].([]any)
	if len(steps) != 2 {
		t.Fatalf("the run's trace holds %d steps, want the corpus search and the fetch", len(steps))
	}
	corpus := steps[0].(map[string]any)
	if corpus["query"] != "copy paste multi line commands" {
		t.Errorf("the first step's query = %v, want the query as issued", corpus["query"])
	}
	if corpus["result_count"] != 1.0 {
		t.Errorf("the first step reports %v results, want one", corpus["result_count"])
	}
	hit := corpus["results"].([]any)[0].(map[string]any)
	if hit["path"] != "/sessions/omp/a/primary.jsonl" || hit["line"] != 346.0 {
		t.Errorf("the hit locates %v:%v, want the locator the receipt recorded", hit["path"], hit["line"])
	}
	if hit["note"] != "the operator's own words" {
		t.Errorf("the hit's note = %v, want the note recorded beside the locator", hit["note"])
	}

	research := document["research"].([]any)
	if len(research) != 1 {
		t.Fatalf("the run fetched %d public sources, want one", len(research))
	}
	source := research[0].(map[string]any)
	if source["url"] != "https://example.test/doc" || source["media_type"] != "text/html" {
		t.Errorf("the fetch reports %v, want the source the receipt recorded", source)
	}
	if source["truncated"] != true || source["bytes"] != 4096.0 {
		t.Errorf("the fetch reports %v bytes truncated=%v, want 4096 and true",
			source["bytes"], source["truncated"])
	}

	candidates := document["candidates"].([]any)
	if len(candidates) != 2 {
		t.Fatalf("the run surfaced %d candidates, want the deferred and the rejected one", len(candidates))
	}
	dispositions := []string{
		candidates[0].(map[string]any)["disposition"].(string),
		candidates[1].(map[string]any)["disposition"].(string),
	}
	if dispositions[0] != "deferred" || dispositions[1] != "rejected" {
		t.Errorf("the candidates are %v, want each to say which it was", dispositions)
	}

	failures := document["failures"].([]any)
	if len(failures) != 1 || failures[0].(map[string]any)["code"] != "broker_refused" {
		t.Errorf("the run's failures are %v, want the one the control plane recorded", failures)
	}

	usage := document["usage"].(map[string]any)
	if usage["cost_usd"] != 0.42 || usage["model"] != "synthetic-1" {
		t.Errorf("the usage reports %v, want the engine's own accounting", usage)
	}
	if usage["input_tokens"] != 1000.0 || usage["output_tokens"] != 250.0 {
		t.Errorf("the usage reports tokens %v/%v, want the counted ones",
			usage["input_tokens"], usage["output_tokens"])
	}

	timing := document["timing"].(map[string]any)
	if timing["duration_s"] != 90.0 {
		t.Errorf("the run lasted %v, want the 90 seconds between its own timestamps", timing["duration_s"])
	}

	resources := document["resources"].(map[string]any)
	if resources["tool_calls"] != 12.0 {
		t.Errorf("the resources report %v tool calls, want the counted 12", resources["tool_calls"])
	}
	// Nobody measured the sandbox's writes, so nothing is reported for them.
	if _, present := resources["sandbox_bytes_written"]; present {
		t.Error("an unmeasured resource is reported as a measurement")
	}

	outputs := document["outputs"].([]any)
	if len(outputs) != 1 || outputs[0].(map[string]any)["id"] != "prp_1" {
		t.Fatalf("the run's own records are %v, want the proposal it wrote", outputs)
	}
	if outputs[0].(map[string]any)["kind"] != "proposal" {
		t.Errorf("the output's kind = %v, want the record's own kind", outputs[0])
	}

	if document["kind"] != "explore" {
		t.Errorf("the run's kind = %v, want it derived from the authority reference", document["kind"])
	}
	versions := document["versions"].(map[string]any)
	if versions["capability"].(map[string]any)["sandbox"] != "sbx-3" {
		t.Errorf("the versions report %v, want the build that enforced the grant", versions)
	}
}

// TestWatchRunDetailReportsAnUnknownRun keeps a mistyped id from reading as a
// run that did nothing.
func TestWatchRunDetailReportsAnUnknownRun(t *testing.T) {
	s, httpServer := testServer(t, Options{Receipts: &fakeReceipts{}})
	session := bootstrapSession(t, s, httpServer)
	response := request(t, httpServer.Client(), http.MethodGet,
		httpServer.URL+"/api/watch/runs/run_absent", session)
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown run answered %d, want 404", response.StatusCode)
	}
}

// TestWatchLiveListsWhatIsRunningAndWhatCanStopIt is the control room's own
// claim: a child this server started carries the pid a stop needs, and a
// machine that cannot see the fleet says so instead of reporting an idle one.
func TestWatchLiveListsWhatIsRunningAndWhatCanStopIt(t *testing.T) {
	started := time.Now().UTC().Add(-90 * time.Second)
	opts := Options{
		Launcher: &fakeLauncher{children: []LaunchedRun{{
			PID: 4242, Kind: "conductor", StartedAt: started, LogPath: "/data/babel/launch/c.log",
		}}},
		Drain: &fakeDrain{observed: true, report: DrainReport{
			At: started, Published: 4, Sealed: 1, Pending: 2,
		}},
	}

	document := watchRequest(t, opts, "/api/watch/live")
	runs := document["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("the live strip holds %v, want the one child this server started", runs)
	}
	row := runs[0].(map[string]any)
	if row["pid"] != 4242.0 || row["stoppable"] != true {
		t.Errorf("the row reports pid %v stoppable %v, want the pid and true", row["pid"], row["stoppable"])
	}
	if row["source"] != "launched" || row["kind"] != "conductor" {
		t.Errorf("the row reports %v, want a launched conductor", row)
	}
	// No host field reaches this surface at all: decision 90 keeps the
	// machine off the reading path.
	for _, forbidden := range []string{"host", "host_id", "local_host"} {
		if _, present := row[forbidden]; present {
			t.Errorf("the live row carries %q, which is not a dimension of this surface", forbidden)
		}
	}
	// A run with no receipt has no spend, and says nothing rather than zero.
	if _, present := row["spend_usd"]; present {
		t.Errorf("a run with no receipt reports spend %v", row["spend_usd"])
	}

	presence := document["presence"].(map[string]any)
	if presence["available"] != false || !strings.Contains(presence["unavailable"].(string), "local mode") {
		t.Errorf("a machine with no presence table says %v, want the local-mode sentence", presence)
	}

	drain := document["drain"].(map[string]any)
	if drain["published"] != 4.0 || drain["pending"] != 2.0 || drain["sealed"] != 1.0 {
		t.Errorf("the last publication attempt reports %v, want 4 published, 1 sealed, 2 pending", drain)
	}
}

// TestWatchLiveOmitsAnAttemptThatHasNotHappened is the same rule one level up:
// a server that has published nothing yet has no attempt to report, which is
// not an attempt that published nothing.
func TestWatchLiveOmitsAnAttemptThatHasNotHappened(t *testing.T) {
	document := watchRequest(t, Options{Drain: &fakeDrain{}}, "/api/watch/live")
	if _, present := document["drain"]; present {
		t.Errorf("a server that has not drained reports %v", document["drain"])
	}
	launcher := document["launcher"].(map[string]any)
	if launcher["available"] != false {
		t.Errorf("a session with no launcher says %v, want it unavailable", launcher)
	}
}

// TestWatchLaunchCarriesTheRefusalVerbatim is why the refusals are the CLI's:
// the operator meets a stated boundary with a remedy, in the words the command
// would have used, rather than a status code.
func TestWatchLaunchCarriesTheRefusalVerbatim(t *testing.T) {
	const ceilings = "the conductor has no budget ceilings, so it will not run. " +
		"babel conductor configure --per-cycle 0.50 --per-day 5.00"
	launcher := &fakeLauncher{launchNo: refusedForTest(ceilings)}
	s, httpServer := testServer(t, Options{Launcher: launcher, Operator: "alex"})
	session := bootstrapSession(t, s, httpServer)

	response := postWatch(t, httpServer, session, "/api/watch/launch",
		`{"kind":"conductor","args":{"until":"60m"}}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("a refused launch answered %d, want 409", response.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != ceilings {
		t.Errorf("the refusal reached the browser as %q, want the command's own words", body["error"])
	}
	// The request reached the launcher as the kind and args the page sent,
	// so the flags an operator typed are the flags the command gets.
	if launcher.launched.Kind != "conductor" || launcher.launched.Args.Until != "60m" {
		t.Errorf("the launcher was asked for %+v, want the conductor until 60m", launcher.launched)
	}
}

// TestWatchLaunchNeedsAnOperator keeps a run from being started by nobody: the
// child spends a budget and records an authority, and both belong to a person.
func TestWatchLaunchNeedsAnOperator(t *testing.T) {
	s, httpServer := testServer(t, Options{Launcher: &fakeLauncher{}})
	session := bootstrapSession(t, s, httpServer)

	response := postWatch(t, httpServer, session, "/api/watch/launch", `{"kind":"conductor"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("an unattributed launch answered %d, want 409", response.StatusCode)
	}
}

// TestWatchStopReportsHowItAsked is what makes the button honest: a stop is a
// request honoured at the next safe point, and the answer says which mechanism
// carried it.
func TestWatchStopReportsHowItAsked(t *testing.T) {
	launcher := &fakeLauncher{}
	s, httpServer := testServer(t, Options{Launcher: launcher, Operator: "alex"})
	session := bootstrapSession(t, s, httpServer)

	response := postWatch(t, httpServer, session, "/api/watch/stop", `{"pid":4242}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("a stop answered %d, want 200", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if launcher.stopped != 4242 {
		t.Errorf("the launcher was asked to stop %d, want the pid the page sent", launcher.stopped)
	}
	if body["method"] != "stop-file" || !strings.Contains(body["detail"].(string), "safe point") {
		t.Errorf("the stop reported %v, want the mechanism and when it takes effect", body)
	}
}

// TestWatchRunsListingAddsWhatTheBodyKnows is the recent-runs table's reason to
// move off the analysis listing: cost, duration and the run's own records, each
// absent rather than zero where the receipt does not say.
func TestWatchRunsListingAddsWhatTheBodyKnows(t *testing.T) {
	at := time.Now().UTC().Add(-2 * time.Hour)
	opts := Options{Receipts: &fakeReceipts{receipts: []run.Receipt{
		pricedReceipt("run_paid", at, 2.5),
		unpricedReceipt("run_free", at),
	}}}

	document := watchRequest(t, opts, "/api/watch/runs?limit=10")
	runs := document["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("the listing holds %d runs, want both", len(runs))
	}
	paid := runs[0].(map[string]any)
	if paid["cost_usd"] != 2.5 {
		t.Errorf("the priced run reports %v, want its receipt's cost", paid["cost_usd"])
	}
	if paid["duration_s"] != 60.0 {
		t.Errorf("the priced run lasted %v, want the minute between its timestamps", paid["duration_s"])
	}
	free := runs[1].(map[string]any)
	if _, present := free["cost_usd"]; present {
		t.Errorf("a run whose engine reported no usage reports a cost: %v", free["cost_usd"])
	}
	if _, present := free["outputs"]; present {
		t.Error("a session whose frontier cannot answer by run reports an output count")
	}
	if document["total"] != 2.0 {
		t.Errorf("the listing reports %v runs in total, want 2", document["total"])
	}
}

// postWatch performs one authenticated POST against the Watch surface, which
// is how both of its writes are reached.
func postWatch(t *testing.T, httpServer *httptest.Server, session, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	authorize(req, session)
	response, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// refusedForTest is a launcher refusal carrying a configuration message, which
// is what the CLI's own refusals are.
func refusedForTest(text string) error { return testRefusal{text: text} }

type testRefusal struct{ text string }

func (r testRefusal) Error() string { return r.text }

func (r testRefusal) Is(target error) bool { return errors.Is(target, ErrConflict) }

// pricedReceipt is one finished run whose engine reported what it cost.
func pricedReceipt(runID string, startedAt time.Time, cost float64) run.Receipt {
	return run.Receipt{
		Header: run.Header{
			ID:         run.ReceiptID("rcpt-" + runID),
			RunID:      runID,
			Revision:   1,
			RecordedAt: startedAt.Add(time.Minute),
			Authority:  run.Authority{Kind: run.AuthorityOperator, Ref: "command:explore"},
			Sync:       "committed",
		},
		Body: run.Body{
			Timing: run.Timing{StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute)},
			Worker: &worker.Receipt{Usage: &worker.Usage{Cost: cost}},
		},
	}
}

// unpricedReceipt is one run whose engine never answered with its accounting,
// which is the case every "spend is absent, not zero" assertion rests on.
func unpricedReceipt(runID string, startedAt time.Time) run.Receipt {
	return run.Receipt{
		Header: run.Header{
			ID:         run.ReceiptID("rcpt-" + runID),
			RunID:      runID,
			Revision:   1,
			RecordedAt: startedAt.Add(time.Minute),
			Authority:  run.Authority{Kind: run.AuthorityPolicy, Ref: "duty:babel-triages-the-queue"},
			Sync:       "pending",
		},
		Body: run.Body{
			Timing: run.Timing{StartedAt: startedAt, FinishedAt: startedAt.Add(time.Minute)},
			Worker: &worker.Receipt{},
		},
	}
}

// tracedReceipt is one run with the body the run page exists to show: a corpus
// search with its hit, a brokered fetch, both candidate outcomes, a
// control-plane failure, and the engine's own accounting.
func tracedReceipt(t *testing.T, runID string) run.Receipt {
	t.Helper()
	evidence, err := run.NewEvidence(event.Locator{
		Path:       "/sessions/omp/a/primary.jsonl",
		Line:       346,
		ByteOffset: 128,
		Digest:     strings.Repeat("ab", 32),
	}, "the operator's own words")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	return run.Receipt{
		Header: run.Header{
			ID:            run.ReceiptID("rcpt-" + runID),
			RunID:         runID,
			PreparationID: "prep_1",
			Revision:      1,
			RecordedAt:    at.Add(90 * time.Second),
			Authority:     run.Authority{Kind: run.AuthorityOperator, Ref: "command:explore"},
			Sync:          "committed",
			Counts:        run.Counts{Retrieval: 2, Deferred: 1, Rejected: 1, Failures: 1},
		},
		Body: run.Body{
			Timing:       run.Timing{StartedAt: at, FinishedAt: at.Add(90 * time.Second)},
			Capabilities: run.CapabilityVersions{Sandbox: "sbx-3", Tool: "tool-2"},
			Job:          run.JobVersions{Job: 4, Prompt: "explore-7", Schema: "result-2"},
			Policy:       run.PolicyVersions{Redaction: "red-1", Disclosure: "managed"},
			Resources:    run.Resources{ToolCalls: new(12)},
			Retrieval: []run.RetrievalStep{
				{
					Index: 1, Tool: "corpus_search", At: at.Add(time.Second),
					Query:   "copy paste multi line commands",
					Results: []run.RetrievalResult{{Rank: 1, Evidence: evidence}},
				},
				{
					Index: 2, Tool: "research_fetch", At: at.Add(2 * time.Second),
					Query: "https://example.test/doc", Scope: run.ScopeResearch,
					Research: &run.ResearchSource{
						SourceID: "src_1", URL: "https://example.test/doc",
						RetrievedAt: at.Add(2 * time.Second), MediaType: "text/html",
						Digest: digest.Digest("sha256:" + strings.Repeat("cd", 32)),
						Bytes:  4096, Truncated: true,
					},
				},
			},
			Deferred: []run.Candidate{{ID: "hyp_2", Reason: "out of budget", At: at.Add(time.Minute)}},
			Rejected: []run.Candidate{{ID: "hyp_3", Reason: "already answered", At: at.Add(time.Minute)}},
			Failures: []run.Failure{{
				Stage: "research", Code: "broker_refused",
				Message: "the source was not in the fixed set", At: at.Add(3 * time.Second),
			}},
			Worker: &worker.Receipt{
				Profile:  worker.ProfileRef{ID: "p-1", Revision: 2},
				Worker:   worker.Identity{Name: "code", Version: "1.2.3"},
				Cost:     worker.Cost{Currency: "USD"},
				Metadata: map[string]string{"model": "synthetic-1", "provider": "synthetic"},
				Usage: &worker.Usage{
					InputTokens: 1000, OutputTokens: 250, TotalTokens: 1250, Cost: 0.42,
				},
			},
		},
	}
}
