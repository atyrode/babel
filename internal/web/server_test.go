package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/atyrode/babel/internal/transcript"
)

type fakeInspector map[string]InspectResult

func (f fakeInspector) InspectSession(_ context.Context, selector string) (InspectResult, error) {
	result, ok := f[selector]
	if !ok {
		return InspectResult{}, fmt.Errorf("%w: selector %s", ErrNotFound, selector)
	}
	return result, nil
}

type fakeArchive struct {
	verifiedDeep  bool
	fetchSelector string
	fetchSnapshot string
	fetchHost     string
	listHost      string
	listSnapshot  string
	// listErr, when set, is what a cross-host listing reports instead of
	// rows: an unreachable repository is a failure of the read, not an
	// empty archive.
	listErr error
	// status and statusErr override the default snapshot status. The
	// dashboard reads catalog lag and per-host publication times that a
	// one-host default cannot express, and it has to render a repository
	// that would not answer at all.
	status    *StatusResult
	statusErr error
}

func (f *fakeArchive) ArchiveStatus(context.Context) (StatusResult, error) {
	if f.statusErr != nil {
		return StatusResult{}, f.statusErr
	}
	if f.status != nil {
		return *f.status, nil
	}
	return StatusResult{Repository: "repo", Snapshots: 1, Hosts: []StatusHostRow{{Host: "host", Snapshots: 1}}}, nil
}

func (f *fakeArchive) ArchiveVerify(_ context.Context, deep bool) (VerifyResult, error) {
	f.verifiedDeep = deep
	return VerifyResult{Repository: "repo", Deep: deep, OK: true}, nil
}

func (f *fakeArchive) ArchiveSessions(_ context.Context, host, snapshot string) (ArchiveSessionsResult, error) {
	f.listHost, f.listSnapshot = host, snapshot
	if f.listErr != nil {
		return ArchiveSessionsResult{}, f.listErr
	}
	return ArchiveSessionsResult{
		Host:     host,
		Snapshot: snapshot,
		Sessions: []ArchiveSessionRow{
			{Harness: "claude", SourceID: "elsewhere", Selector: "claude/elsewhere", Size: 4096},
			{Harness: "omp", SourceID: "recovered", Selector: "omp/recovered", Size: 128,
				Fetched: true, FetchedPath: "/data/babel/sessions/omp-recovered"},
		},
	}, nil
}

func (f *fakeArchive) FetchSession(_ context.Context, req FetchRequest) (FetchResult, error) {
	f.fetchSelector, f.fetchSnapshot, f.fetchHost = req.Selector, req.Snapshot, req.Host
	return FetchResult{Selector: req.Selector, SnapshotID: "snapshot", Included: []string{"primary.jsonl"}}, nil
}

// fakeScanner mirrors the coordinator's contract: State never blocks and
// StartRefresh attaches to a running scan instead of starting a second one.
type fakeScanner struct {
	mu     sync.Mutex
	state  ScanState
	starts int
}

func (f *fakeScanner) State() ScanState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeScanner) StartRefresh() ScanState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Running {
		return f.state
	}
	f.starts++
	f.state = ScanState{Running: true, Total: 3, StartedAt: "2026-01-02T03:04:05Z"}
	return f.state
}

func testServer(t *testing.T, opts Options) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.listener.Close() })
	httpServer := httptest.NewServer(s.middleware(http.HandlerFunc(s.route)))
	t.Cleanup(httpServer.Close)
	return s, httpServer
}

func request(t *testing.T, client *http.Client, method, target, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, dst any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func TestServeUsesLaunchURLAndStopsWithContext(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	// The token rides in the fragment and nowhere else, so opening the launch
	// URL transmits no credential at all (SPEC.md §146).
	fragment, err := url.ParseQuery(launch.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Hostname() != "127.0.0.1" || fragment.Get("token") != s.token || len(s.token) != 64 {
		t.Fatalf("launch URL = %q", s.URL())
	}
	if launch.Query().Get("token") != "" {
		t.Fatalf("launch URL carries the token in the query string: %q", s.URL())
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- s.Serve(ctx) }()

	// Because the fragment never leaves the browser, the bootstrap presents the
	// token as a bearer header instead; the launch URL alone authenticates
	// nothing.
	launch.Path = "/api/version"
	launch.Fragment = ""
	request, err := http.NewRequest(http.MethodGet, launch.String(), nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status = %d", response.StatusCode)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop after context cancellation")
	}
}

func TestAuthentication(t *testing.T) {
	s, httpServer := testServer(t, Options{})
	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: strings.Repeat("0", 64), status: http.StatusUnauthorized},
		{name: "right", token: s.token, status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/version", test.token)
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if got := response.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			if got := response.Header.Get("Content-Security-Policy"); got != "default-src 'self'" {
				t.Errorf("CSP = %q", got)
			}
			if got := response.Header.Get("Referrer-Policy"); got != "no-referrer" {
				t.Errorf("Referrer-Policy = %q", got)
			}
		})
	}

	// A correct token in the query string is refused. The fragment is the only
	// place a launch token belongs (SPEC.md §146): a query string reaches the
	// request line, so honouring it would invite a live credential into access
	// logs, caches, and Referer headers.
	response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/version?token="+s.token, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token status = %d, want 401", response.StatusCode)
	}
}

func TestEveryAPIEndpoint(t *testing.T) {
	modified := "2026-01-02T03:04:05Z"
	archive := &fakeArchive{}
	scanning := &fakeScanner{state: ScanState{Described: 3, Total: 3, FinishedAt: modified}}
	inspector := fakeInspector{"omp/one": {
		Harness: "omp", SourceID: "one", Selector: "omp/one", PrimaryPath: "/synthetic/one.jsonl",
		AdapterMetadataSchema: 1, AdapterMetadata: json.RawMessage(`{"fixture":"yes"}`),
	}}
	s, httpServer := testServer(t, Options{
		State: StateProviderFunc(func(context.Context) (State, error) {
			return State{Configured: true, Repository: "repo", HostID: "host"}, nil
		}),
		Lister: SessionListerFunc(func(context.Context) (SessionsResult, error) {
			return SessionsResult{
				Sessions:    []SessionRow{{Harness: "omp", SourceID: "one", Selector: "omp/one", Modified: &modified}},
				RefreshedAt: modified,
				Scan:        ScanState{Described: 3, Total: 3, FinishedAt: modified},
			}, nil
		}),
		Scanner:   scanning,
		Inspector: inspector,
		Archive:   archive,
		Transcripts: TranscriptReaderFunc(func(path, harness string, offset, limit int) (int, []transcript.Event, error) {
			if path != "/synthetic/one.jsonl" || harness != "omp" || offset != 1 || limit != 2 {
				t.Errorf("transcript args = %q %q %d %d", path, harness, offset, limit)
			}
			return 4, []transcript.Event{{Index: 1, Role: "user", Kind: "message", Text: "hello"}}, nil
		}),
	})

	tests := []struct {
		name   string
		method string
		path   string
		check  func(map[string]any)
	}{
		{name: "version", method: http.MethodGet, path: "/api/version", check: func(got map[string]any) {
			for _, key := range []string{"version", "commit", "dirty", "go", "platform"} {
				if _, ok := got[key]; !ok {
					t.Errorf("version missing %q", key)
				}
			}
		}},
		{name: "state", method: http.MethodGet, path: "/api/state", check: func(got map[string]any) {
			if got["repository"] != "repo" || got["host_id"] != "host" || got["configured"] != true {
				t.Errorf("state = %#v", got)
			}
		}},
		{name: "sessions", method: http.MethodGet, path: "/api/sessions", check: func(got map[string]any) {
			scan, ok := got["scan"].(map[string]any)
			if len(got["sessions"].([]any)) != 1 || got["refreshed_at"] != modified || !ok {
				t.Errorf("sessions = %#v", got)
				return
			}
			if scan["running"] != false || scan["described"] != float64(3) {
				t.Errorf("sessions scan = %#v", scan)
			}
		}},
		{name: "scan", method: http.MethodGet, path: "/api/scan", check: func(got map[string]any) {
			if got["running"] != false || got["described"] != float64(3) || got["finished_at"] != modified {
				t.Errorf("scan = %#v", got)
			}
			if _, ok := got["harness"]; ok {
				t.Errorf("idle scan names a harness: %#v", got)
			}
		}},
		{name: "sessions refresh", method: http.MethodPost, path: "/api/sessions/refresh", check: func(got map[string]any) {
			if got["running"] != true || got["total"] != float64(3) {
				t.Errorf("refresh = %#v", got)
			}
		}},
		{name: "session", method: http.MethodGet, path: "/api/session?selector=omp%2Fone", check: func(got map[string]any) {
			if got["selector"] != "omp/one" || got["primary_path"] != "/synthetic/one.jsonl" {
				t.Errorf("session = %#v", got)
			}
		}},
		{name: "transcript", method: http.MethodGet, path: "/api/transcript?selector=omp%2Fone&offset=1&limit=2", check: func(got map[string]any) {
			if got["total"] != float64(4) || len(got["events"].([]any)) != 1 {
				t.Errorf("transcript = %#v", got)
			}
		}},
		{name: "archive status", method: http.MethodGet, path: "/api/archive/status", check: func(got map[string]any) {
			if got["snapshots"] != float64(1) {
				t.Errorf("status = %#v", got)
			}
		}},
		{name: "archive sessions", method: http.MethodGet, path: "/api/archive/sessions?host=elsewhere-host&snapshot=cdea", check: func(got map[string]any) {
			rows, ok := got["sessions"].([]any)
			if !ok || len(rows) != 2 || got["host"] != "elsewhere-host" || got["snapshot"] != "cdea" {
				t.Errorf("archive sessions = %#v", got)
				return
			}
			first := rows[0].(map[string]any)
			if first["selector"] != "claude/elsewhere" || first["size"] != float64(4096) || first["fetched"] != false {
				t.Errorf("archive row = %#v", first)
			}
			// The four fields a snapshot listing cannot know are absent
			// from the shape, not present and null: a client cannot read
			// them at all, so it cannot render them as blank cells.
			for _, key := range []string{"modified", "title", "workspace", "continuation_grade"} {
				if _, present := first[key]; present {
					t.Errorf("archive row carries %q, which a snapshot listing never observed: %#v", key, first)
				}
			}
			second := rows[1].(map[string]any)
			if second["fetched"] != true || second["fetched_path"] != "/data/babel/sessions/omp-recovered" {
				t.Errorf("fetched archive row = %#v", second)
			}
		}},
		{name: "archive verify", method: http.MethodPost, path: "/api/archive/verify?deep=1", check: func(got map[string]any) {
			if got["deep"] != true || got["ok"] != true {
				t.Errorf("verify = %#v", got)
			}
		}},
		{name: "fetch", method: http.MethodPost, path: "/api/fetch?selector=omp%2Fone&snapshot=abc1&host=elsewhere-host", check: func(got map[string]any) {
			if got["selector"] != "omp/one" || got["snapshot_id"] != "snapshot" {
				t.Errorf("fetch = %#v", got)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, httpServer.Client(), test.method, httpServer.URL+test.path, s.token)
			if response.StatusCode != http.StatusOK {
				defer response.Body.Close()
				t.Fatalf("status = %d", response.StatusCode)
			}
			var got map[string]any
			decodeResponse(t, response, &got)
			test.check(got)
		})
	}
	if !archive.verifiedDeep || archive.fetchSelector != "omp/one" || archive.fetchSnapshot != "abc1" {
		t.Fatalf("archive calls = %#v", archive)
	}
	// A selector discovered in another host's archive is only fetchable if
	// the host reaches the command that resolves it there.
	if archive.fetchHost != "elsewhere-host" {
		t.Fatalf("fetch host = %q, want the requested host", archive.fetchHost)
	}
	if archive.listHost != "elsewhere-host" || archive.listSnapshot != "cdea" {
		t.Fatalf("archive listing called with host %q snapshot %q", archive.listHost, archive.listSnapshot)
	}
	if scanning.starts != 1 {
		t.Fatalf("refresh started %d scans, want 1", scanning.starts)
	}
}

// TestScanEndpoints pins the behavior a progress-reporting client depends on:
// a running scan is reported rather than restarted, a repeated refresh
// attaches to it, and the endpoints answer honestly when no scanner is wired.
func TestScanEndpoints(t *testing.T) {
	scanning := &fakeScanner{}
	s, httpServer := testServer(t, Options{Scanner: scanning})
	client := httpServer.Client()

	// Idle: nothing running, nothing described.
	var idle ScanState
	response := request(t, client, http.MethodGet, httpServer.URL+"/api/scan", s.token)
	decodeResponse(t, response, &idle)
	if idle != (ScanState{}) {
		t.Fatalf("idle scan = %+v", idle)
	}

	// Two refreshes are one scan, and both callers learn it is running.
	for attempt := range 2 {
		var started ScanState
		response := request(t, client, http.MethodPost, httpServer.URL+"/api/sessions/refresh", s.token)
		decodeResponse(t, response, &started)
		if !started.Running || started.Total != 3 {
			t.Fatalf("refresh %d = %+v", attempt, started)
		}
	}
	if scanning.starts != 1 {
		t.Fatalf("two refreshes started %d scans, want 1", scanning.starts)
	}
	var running ScanState
	response = request(t, client, http.MethodGet, httpServer.URL+"/api/scan", s.token)
	decodeResponse(t, response, &running)
	if !running.Running || running.StartedAt == "" {
		t.Fatalf("running scan = %+v", running)
	}

	// Both endpoints are behind the token and accept exactly one method.
	for _, test := range []struct {
		name   string
		method string
		path   string
		token  string
		status int
	}{
		{name: "scan unauthenticated", method: http.MethodGet, path: "/api/scan", status: http.StatusUnauthorized},
		{name: "refresh unauthenticated", method: http.MethodPost, path: "/api/sessions/refresh", status: http.StatusUnauthorized},
		{name: "scan wrong method", method: http.MethodPost, path: "/api/scan", token: s.token, status: http.StatusBadRequest},
		{name: "refresh wrong method", method: http.MethodGet, path: "/api/sessions/refresh", token: s.token, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, client, test.method, httpServer.URL+test.path, test.token)
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
		})
	}

	// With no scanner wired, the endpoints report that rather than inventing
	// an idle scan the client would trust.
	bare, bareServer := testServer(t, Options{})
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/scan"},
		{http.MethodPost, "/api/sessions/refresh"},
	} {
		response := request(t, bareServer.Client(), endpoint.method, bareServer.URL+endpoint.path, bare.token)
		defer response.Body.Close()
		if response.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s without a scanner = %d", endpoint.path, response.StatusCode)
		}
	}
}

func TestUnconfiguredArchiveReturnsConflict(t *testing.T) {
	s, httpServer := testServer(t, Options{})
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/archive/status"},
		{http.MethodGet, "/api/archive/sessions?host=elsewhere-host"},
		{http.MethodPost, "/api/archive/verify"},
		{http.MethodPost, "/api/fetch?selector=omp%2Fone"},
	} {
		response := request(t, httpServer.Client(), endpoint.method, httpServer.URL+endpoint.path, s.token)
		defer response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Errorf("%s status = %d", endpoint.path, response.StatusCode)
		}
	}
}

// TestSessionScopeStatesAreDistinguishable pins the four answers the Sessions
// surface must be able to give about scope, because collapsing any two of them
// is what left the operator unable to tell "this is everything" from "there is
// more you cannot see".
//
// It asserts them as the server states them, and it asserts them against each
// other: the no-repository case is only meaningful as "makes no claim about an
// archive" if a configured server on the same assertions does make one. Every
// case therefore checks both what is said and what the others say instead.
func TestSessionScopeStatesAreDistinguishable(t *testing.T) {
	local := SessionListerFunc(func(context.Context) (SessionsResult, error) {
		return SessionsResult{Sessions: []SessionRow{{Harness: "omp", SourceID: "here", Selector: "omp/here"}}}, nil
	})
	state := func(configured bool) StateProviderFunc {
		return func(context.Context) (State, error) {
			return State{Configured: configured, Repository: "repo", HostID: "this-host"}, nil
		}
	}

	t.Run("no repository configured", func(t *testing.T) {
		s, httpServer := testServer(t, Options{State: state(false), Lister: local})
		client := httpServer.Client()

		// The page derives "local is the whole truth" from these two
		// answers, so both have to be unambiguous. State reports no
		// repository and names none, so there is no locator on the wire for
		// a page to render as if an archive existed — while still naming the
		// host, because whose sessions these are does not depend on whether
		// a repository was ever configured.
		var reported State
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/state", s.token), &reported)
		if reported.Configured || reported.Repository != "" {
			t.Fatalf("unconfigured state = %+v, want no repository claimed", reported)
		}
		if reported.HostID != "this-host" {
			t.Fatalf("unconfigured host id = %q, want the machine still named", reported.HostID)
		}

		// And no archive route answers with data that could be mistaken for
		// an empty archive. 409 is the distinction: it says the question
		// does not apply here, where 200 with zero hosts would say the
		// archive is empty.
		for _, path := range []string{"/api/archive/status", "/api/archive/sessions?host=elsewhere-host"} {
			response := request(t, client, http.MethodGet, httpServer.URL+path, s.token)
			defer response.Body.Close()
			if response.StatusCode != http.StatusConflict {
				t.Errorf("%s = %d, want 409 so no client can read it as an empty archive", path, response.StatusCode)
			}
		}

		// Local sessions are still served in full: an absent archive
		// narrows nothing about this host.
		var sessions SessionsResult
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/sessions", s.token), &sessions)
		if len(sessions.Sessions) != 1 {
			t.Fatalf("local sessions without a repository = %d rows, want 1", len(sessions.Sessions))
		}
	})

	t.Run("configured but unreachable", func(t *testing.T) {
		unreachable := errors.New("Fatal: unable to open repository at s3:example: connection refused")
		archive := &fakeArchive{listErr: unreachable}
		s, httpServer := testServer(t, Options{State: state(true), Lister: local, Archive: archive})
		client := httpServer.Client()

		// The repository is named even though it cannot be read, which is
		// what separates this state from the one above: something exists
		// and this machine could not reach it.
		var reported State
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/state", s.token), &reported)
		if !reported.Configured || reported.Repository != "repo" {
			t.Fatalf("configured state = %+v", reported)
		}

		// A failed read is a failed read. Not 409 — that would say the
		// question does not apply — and never 200 with no rows, which would
		// say the host published nothing.
		response := request(t, client, http.MethodGet, httpServer.URL+"/api/archive/sessions?host=elsewhere-host", s.token)
		defer response.Body.Close()
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("unreachable archive listing = %d, want 500", response.StatusCode)
		}
		var failure struct {
			Error    string `json:"error"`
			Sessions []any  `json:"sessions"`
		}
		decodeResponse(t, response, &failure)
		if !strings.Contains(failure.Error, "connection refused") {
			t.Errorf("unreachable archive error = %q, want the repository's own reason", failure.Error)
		}
		if failure.Sessions != nil {
			t.Errorf("a failed listing carried a session array: %#v", failure.Sessions)
		}
	})

	t.Run("configured and this host is the only publisher", func(t *testing.T) {
		// fakeArchive reports exactly one host. The page compares it with
		// the state's host id, so "only publisher" has to be derivable from
		// the two documents rather than asserted by a third.
		onlyPublisher := StateProviderFunc(func(context.Context) (State, error) {
			return State{Configured: true, Repository: "repo", HostID: "host"}, nil
		})
		s, httpServer := testServer(t, Options{State: onlyPublisher, Lister: local, Archive: &fakeArchive{}})
		client := httpServer.Client()

		var status StatusResult
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/archive/status", s.token), &status)
		if len(status.Hosts) != 1 || status.Hosts[0].Host != "host" {
			t.Fatalf("status hosts = %#v", status.Hosts)
		}
		var reported State
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/state", s.token), &reported)
		if reported.HostID != status.Hosts[0].Host {
			t.Fatalf("host id %q is not the only publisher %q", reported.HostID, status.Hosts[0].Host)
		}
	})

	t.Run("configured with another host publishing", func(t *testing.T) {
		s, httpServer := testServer(t, Options{State: state(true), Lister: local, Archive: &fakeArchive{}})
		client := httpServer.Client()

		// state(true) reports "this-host"; the archive reports "host". The
		// two differ, which is the whole signal that there is more to see.
		var status StatusResult
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/archive/status", s.token), &status)
		var reported State
		decodeResponse(t, request(t, client, http.MethodGet, httpServer.URL+"/api/state", s.token), &reported)
		if len(status.Hosts) != 1 || status.Hosts[0].Host == reported.HostID {
			t.Fatalf("expected a publisher other than %q, got %#v", reported.HostID, status.Hosts)
		}

		// And that other host's sessions are reachable, with rows this
		// machine's own listing does not hold.
		var listing ArchiveSessionsResult
		decodeResponse(t, request(t, client, http.MethodGet,
			httpServer.URL+"/api/archive/sessions?host="+status.Hosts[0].Host, s.token), &listing)
		if len(listing.Sessions) != 2 || listing.Sessions[0].Selector != "claude/elsewhere" {
			t.Fatalf("cross-host listing = %#v", listing)
		}
		if listing.Snapshot != "" {
			t.Errorf("snapshot = %q, want empty for a request that named none", listing.Snapshot)
		}
	})
}

// TestArchiveSessionsRequiresAHost pins that the expensive repository read is
// never performed on a guess. Defaulting the host to this machine would answer
// a question /api/sessions already answers, from the archive, over the network.
func TestArchiveSessionsRequiresAHost(t *testing.T) {
	archive := &fakeArchive{}
	s, httpServer := testServer(t, Options{Archive: archive})
	for _, path := range []string{"/api/archive/sessions", "/api/archive/sessions?host="} {
		response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+path, s.token)
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, response.StatusCode)
		}
	}
	if archive.listHost != "" {
		t.Fatalf("a hostless request still read the repository as %q", archive.listHost)
	}
	response := request(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/api/archive/sessions?host=h", s.token)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("POST to the listing = %d, want 400", response.StatusCode)
	}
}

func TestTranscriptEndpointWithHarnessFixturesAndRawRecords(t *testing.T) {
	paths := map[string]string{
		"omp/fixture": filepath.Join("..", "adapter", "omp", "testdata", "root", "agent", "sessions", "-synthetic-project",
			"2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001.jsonl"),
		"codex/fixture": filepath.Join("..", "adapter", "codex", "testdata", "root", "sessions", "2026", "01", "02",
			"rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-000000000001.jsonl"),
		"claude/fixture": filepath.Join("..", "adapter", "claude", "testdata", "session-rich.jsonl"),
	}
	inspector := fakeInspector{}
	for selector, fixturePath := range paths {
		harness, _, _ := strings.Cut(selector, "/")
		inspector[selector] = InspectResult{Harness: harness, Selector: selector, PrimaryPath: fixturePath}
	}
	s, httpServer := testServer(t, Options{Inspector: inspector, Transcripts: TranscriptReaderFunc(transcript.Events)})

	for selector := range paths {
		t.Run(selector, func(t *testing.T) {
			target := httpServer.URL + "/api/transcript?selector=" + url.QueryEscape(selector) + "&limit=100"
			response := request(t, httpServer.Client(), http.MethodGet, target, s.token)
			if response.StatusCode != http.StatusOK {
				defer response.Body.Close()
				t.Fatalf("status = %d", response.StatusCode)
			}
			var result struct {
				Total  int                `json:"total"`
				Events []transcript.Event `json:"events"`
			}
			decodeResponse(t, response, &result)
			if result.Total == 0 || len(result.Events) != result.Total {
				t.Fatalf("result = %#v", result)
			}
			seenMessage, seenRaw := false, false
			for _, event := range result.Events {
				seenMessage = seenMessage || event.Kind == "message"
				seenRaw = seenRaw || event.Kind == "raw"
			}
			if !seenMessage || !seenRaw {
				t.Fatalf("message, raw = %t, %t", seenMessage, seenRaw)
			}
		})
	}
}

func TestSPAFallback(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<main>babel app</main>")},
		"asset.js":   &fstest.MapFile{Data: []byte("window.babel = true")},
	}
	_, httpServer := testServer(t, Options{Static: fs.FS(static)})
	for _, test := range []struct{ path, contains string }{
		{"/asset.js", "window.babel"},
		{"/sessions/omp%2Fone", "babel app"},
	} {
		response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+test.path, "")
		var body bytes.Buffer
		_, _ = body.ReadFrom(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), test.contains) {
			t.Errorf("%s: status %d body %q", test.path, response.StatusCode, body.String())
		}
		if response.Header.Get("Content-Security-Policy") != "default-src 'self'" {
			t.Errorf("%s: missing CSP", test.path)
		}
	}
}

func TestSanitizesErrorsAndDiagnostics(t *testing.T) {
	var diagnostics syncBuffer
	s, httpServer := testServer(t, Options{
		Diagnostics: &diagnostics,
		Lister: SessionListerFunc(func(context.Context) (SessionsResult, error) {
			return SessionsResult{}, errorsWithControl("bad\x1b[2J\nline")
		}),
	})
	response := request(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/sessions", s.token)
	var got map[string]string
	decodeResponse(t, response, &got)
	if strings.ContainsRune(got["error"], '\x1b') || strings.ContainsRune(got["error"], '\n') {
		t.Fatalf("unsanitized error %q", got["error"])
	}
	if strings.ContainsRune(diagnostics.String(), '\x1b') || strings.Contains(diagnostics.String(), s.token) {
		t.Fatalf("unsafe diagnostics %q", diagnostics.String())
	}
}

// TestLockRefusesForgeableRequests pins the guard that keeps the stop control
// from becoming a denial of service: a page the operator merely visited must
// not be able to reach it, and neither must a request that only looks like a
// state change. Each refusal is followed by proof the server is still serving,
// because a guard that stopped the server while rejecting the request would
// otherwise pass every assertion here.
func TestLockRefusesForgeableRequests(t *testing.T) {
	s, httpServer := testServer(t, Options{})
	client := httpServer.Client()

	for _, test := range []struct {
		name   string
		method string
		token  string
		origin string
		host   string
		status int
	}{
		{name: "get is not a state change", method: http.MethodGet, token: s.token, status: http.StatusBadRequest},
		{name: "cross-origin post", method: http.MethodPost, token: s.token, origin: "http://evil.example", status: http.StatusForbidden},
		{name: "rebound host", method: http.MethodPost, token: s.token, host: "evil.example", status: http.StatusForbidden},
		{name: "no credential", method: http.MethodPost, status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(test.method, httpServer.URL+"/api/lock", nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.host != "" {
				req.Host = test.host
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}

			alive := request(t, client, http.MethodGet, httpServer.URL+"/api/version", s.token)
			alive.Body.Close()
			if alive.StatusCode != http.StatusOK {
				t.Fatalf("a refused lock revoked the session anyway: /api/version = %d", alive.StatusCode)
			}
		})
	}
}

// TestLockRevokesTheLaunchSession covers the half of the contract that must
// hold while the process is still answering: the token is dead the moment the
// lock is accepted, so a tab the operator left open cannot act on it during
// the shutdown.
func TestLockRevokesTheLaunchSession(t *testing.T) {
	// A mutex-protected sink, because the access log is written by whichever
	// goroutine served a request and this test reads the same stream.
	var diagnostics syncBuffer
	s, httpServer := testServer(t, Options{Diagnostics: &diagnostics})
	client := httpServer.Client()

	response := request(t, client, http.MethodPost, httpServer.URL+"/api/lock", s.token)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	var got lockResult
	decodeResponse(t, response, &got)
	if !got.Revoked || !got.Stopping {
		t.Fatalf("lock result = %+v", got)
	}

	// The same credential that worked a moment ago is now worth nothing, and
	// the lock itself cannot be replayed.
	for _, target := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/version"},
		{http.MethodPost, "/api/lock"},
	} {
		after := request(t, client, target.method, httpServer.URL+target.path, s.token)
		after.Body.Close()
		if after.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s after lock = %d, want 401", target.method, target.path, after.StatusCode)
		}
	}

	// The flush releases the client while the handler is still running, so the
	// response is not a barrier for the handler's own work. `locked` is: it is
	// closed last, after the diagnostic is written. Without this wait the
	// assertions below race the handler and fail only under load, which is how
	// this surfaced - green locally, red on a busy CI runner, with the buffer
	// holding just the two post-lock 401s.
	<-s.locked

	// The confirmation is written and flushed before the listener is asked to
	// go away, so it does not depend on the drain. The handler reports a flush
	// it could not perform, which is the only way that regression would be
	// visible from outside.
	if strings.Contains(diagnostics.String(), "not flushed") {
		t.Fatalf("the lock confirmation was not flushed: %q", diagnostics.String())
	}
	if !strings.Contains(diagnostics.String(), "locked by operator") {
		t.Fatalf("the lock was not recorded in diagnostics: %q", diagnostics.String())
	}
}

// TestLockStopsTheListener drives the real listener, because the second half of
// the contract is a process that stops accepting connections and exits
// successfully. Serve's return is the synchronization point: it happens only
// after the graceful drain completes, so nothing here waits on a clock.
func TestLockStopsTheListener(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A safety net only: the assertions below require Serve to have returned
	// before this runs, so cancellation never explains a passing test.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- s.Serve(ctx) }()

	launch, err := url.Parse(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	launch.Fragment = ""
	base := launch.String()

	response := request(t, http.DefaultClient, http.MethodPost, base+"api/lock", s.token)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var got lockResult
	decodeResponse(t, response, &got)
	if !got.Revoked || !got.Stopping {
		t.Fatalf("lock result = %+v", got)
	}

	select {
	case err := <-result:
		// The operator asked for this, so it is a success, not a failure.
		if err != nil {
			t.Fatalf("Serve after lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the lock did not stop the listener")
	}

	// The port is no longer served at all, which is the difference between a
	// revoked session and a stopped server.
	if _, err := http.Get(base + "api/version"); err == nil {
		t.Fatal("the listener still accepts connections after the lock")
	}
}

type errorsWithControl string

func (e errorsWithControl) Error() string { return string(e) }

// syncBuffer is a diagnostics sink safe for the concurrent writes a running
// server makes. The access log is emitted by whichever goroutine served the
// request, so a test that both serves requests and reads the stream cannot use
// a bare bytes.Buffer: that is a data race whether or not it is detected on a
// given run.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestLockStopsTheListenerUnderContention defends the lock's exit status
// against the interleaving that decides it.
//
// shutdown closes the listener, then http.Server.Shutdown closes the
// listeners it still tracks — which is the same one, unless Serve noticed
// the closed listener first and untracked it on its way out. Whichever wins
// is scheduling, and when Shutdown wins it reports closing an already-closed
// listener as its error. That is not a failure: it is the state the operator
// asked for.
//
// One iteration cannot pin this down, because on an idle machine Serve
// almost always wins and the bug is invisible. Repetition is the mechanism:
// the loser changes under contention, and this failed in CI on a runner with
// four concurrent jobs while forty consecutive local runs passed. Each
// iteration is a fresh server on an ephemeral port and costs microseconds,
// so the loop is cheap insurance rather than a stress test.
func TestLockStopsTheListenerUnderContention(t *testing.T) {
	for i := range 200 {
		s, err := New(Options{})
		if err != nil {
			t.Fatalf("iteration %d: New: %v", i, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- s.Serve(ctx) }()

		launch, err := url.Parse(s.URL())
		if err != nil {
			cancel()
			t.Fatalf("iteration %d: %v", i, err)
		}
		launch.Fragment = ""
		base := launch.String()

		response := request(t, http.DefaultClient, http.MethodPost, base+"api/lock", s.token)
		response.Body.Close()

		select {
		case err := <-result:
			if err != nil {
				cancel()
				t.Fatalf("iteration %d: Serve after lock returned %v; a lock the operator asked for is a success, and an already-closed listener is the state it asked for", i, err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatalf("iteration %d: the lock did not stop the listener", i)
		}
		cancel()
	}
}
