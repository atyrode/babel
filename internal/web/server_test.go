package web

import (
	"bytes"
	"context"
	"encoding/json"
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
}

func (*fakeArchive) ArchiveStatus(context.Context) (StatusResult, error) {
	return StatusResult{Repository: "repo", Snapshots: 1, Hosts: []StatusHostRow{{Host: "host", Snapshots: 1}}}, nil
}

func (f *fakeArchive) ArchiveVerify(_ context.Context, deep bool) (VerifyResult, error) {
	f.verifiedDeep = deep
	return VerifyResult{Repository: "repo", Deep: deep, OK: true}, nil
}

func (f *fakeArchive) FetchSession(_ context.Context, selector, snapshot string) (FetchResult, error) {
	f.fetchSelector, f.fetchSnapshot = selector, snapshot
	return FetchResult{Selector: selector, SnapshotID: "snapshot", Included: []string{"primary.jsonl"}}, nil
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
		{name: "archive verify", method: http.MethodPost, path: "/api/archive/verify?deep=1", check: func(got map[string]any) {
			if got["deep"] != true || got["ok"] != true {
				t.Errorf("verify = %#v", got)
			}
		}},
		{name: "fetch", method: http.MethodPost, path: "/api/fetch?selector=omp%2Fone&snapshot=abc1", check: func(got map[string]any) {
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
	var diagnostics bytes.Buffer
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

type errorsWithControl string

func (e errorsWithControl) Error() string { return string(e) }
