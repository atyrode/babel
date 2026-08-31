package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
)

// sessionsPayload is the GET /api/sessions document a browser consumes.
type sessionsPayload struct {
	Sessions []struct {
		Selector string `json:"selector"`
	} `json:"sessions"`
	RefreshedAt string    `json:"refreshed_at"`
	Scan        scanState `json:"scan"`
}

// syncBuffer collects diagnostics a background scan writes while the test
// reads them, which are different goroutines.
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

// webHarness drives a live `babel web` server the way a browser does, with
// access to the scan coordinator behind it.
type webHarness struct {
	t    *testing.T
	base string
	// nonce is the spent bootstrap nonce and session is what it was
	// exchanged for. Both are kept because the leak assertions are about
	// both: the nonce must never reach a request line or a log, and the
	// session must never leave its cookie.
	nonce       string
	session     string
	coordinator *scanCoordinator
	stderr      *syncBuffer
}

func newWebHarness(t *testing.T, f *fixture) *webHarness {
	t.Helper()
	stderr := &syncBuffer{}
	a := &app{stdout: &bytes.Buffer{}, stderr: stderr}
	srv, services, err := a.buildWebServer(repoFlags{}, "", 0)
	if err != nil {
		t.Fatalf("build web server: %v", err)
	}
	t.Cleanup(func() { services.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		// The server's own graceful shutdown budget is 5s (internal/web:
		// Serve gives httpServer.Shutdown a 5s context). Waiting exactly that
		// long here makes a correct-but-slow shutdown and this deadline fire at
		// the same instant, so the test reports a hang that never happened -
		// which is what it did under full-suite load. This bound must stay
		// strictly greater than the server's.
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("server did not stop within 20s, well past its own 5s shutdown budget")
		}
	})
	base, nonce, session := bootstrapWeb(t, srv.URL())
	return &webHarness{
		t: t, base: base, nonce: nonce, session: session,
		coordinator: scanner(f.dataDir), stderr: stderr,
	}
}

// do performs one authenticated request, decoding the response document.
func (h *webHarness) do(ctx context.Context, method, path string, out any) int {
	h.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	authorizeWeb(req, h.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, out); err != nil {
			h.t.Fatalf("%s %s: decode %q: %v", method, path, body, err)
		}
	}
	return resp.StatusCode
}

func (h *webHarness) get(path string, out any) int {
	h.t.Helper()
	return h.do(context.Background(), http.MethodGet, path, out)
}

func (h *webHarness) post(path string, out any) int {
	h.t.Helper()
	return h.do(context.Background(), http.MethodPost, path, out)
}

// describeWith replaces the describer the background scan uses, so a test
// can hold a scan open at a known point.
func (h *webHarness) describeWith(fn sessionDescribeFunc) {
	h.coordinator.mu.Lock()
	defer h.coordinator.mu.Unlock()
	h.coordinator.describeSession = fn
}

// gatedDescriber blocks every describe until the returned channel is closed,
// reporting each session it reached. It makes a scan's duration a decision of
// the test rather than of the machine it runs on.
func gatedDescriber(release <-chan struct{}) (sessionDescribeFunc, chan string, func() map[string]int) {
	reached := make(chan string, 64)
	var mu sync.Mutex
	counts := map[string]int{}
	return func(ctx context.Context, session localSession) (*adapter.Description, error) {
			mu.Lock()
			counts[session.key()]++
			mu.Unlock()
			reached <- session.key()
			<-release
			return describe(ctx, session)
		}, reached, func() map[string]int {
			mu.Lock()
			defer mu.Unlock()
			out := make(map[string]int, len(counts))
			for key, n := range counts {
				out[key] = n
			}
			return out
		}
}

// waitForScan polls the scan endpoint until the background scan settles.
func waitForScan(t *testing.T, get func(string, any) int) scanState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var state scanState
		if code := get("/api/scan", &state); code != http.StatusOK {
			t.Fatalf("/api/scan: %d", code)
		}
		if !state.Running && state.FinishedAt != "" {
			if state.Error != "" {
				t.Fatalf("scan reported %q", state.Error)
			}
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan did not settle: %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSessionsEndpointAnswersWhileScanning is the defect this coordinator
// exists for: the listing endpoint used to run the whole describe pass inside
// the request, so a cold browser sat on a spinner with nothing to report.
func TestSessionsEndpointAnswersWhileScanning(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	h := newWebHarness(t, f)
	release := make(chan struct{})
	describer, reached, counts := gatedDescriber(release)
	h.describeWith(describer)

	var cold sessionsPayload
	start := time.Now()
	if code := h.get("/api/sessions", &cold); code != http.StatusOK {
		t.Fatalf("/api/sessions: %d", code)
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("cold listing took %s, want an immediate answer", elapsed)
	}
	if !cold.Scan.Running || cold.RefreshedAt == "" {
		t.Fatalf("cold listing = %+v", cold)
	}

	// With one describe held open, the poll target still answers at once and
	// reports honest progress instead of an indefinite wait.
	<-reached
	var state scanState
	start = time.Now()
	if code := h.get("/api/scan", &state); code != http.StatusOK {
		t.Fatalf("/api/scan: %d", code)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("/api/scan took %s while scanning", elapsed)
	}
	if !state.Running || state.Total != 3 {
		t.Fatalf("in-flight scan state = %+v", state)
	}

	close(release)
	settled := waitForScan(t, h.get)
	if settled.Described != 3 || settled.Failed != 0 || settled.Total != 3 {
		t.Fatalf("settled scan state = %+v", settled)
	}
	var warm sessionsPayload
	if code := h.get("/api/sessions", &warm); code != http.StatusOK {
		t.Fatalf("/api/sessions after scan: %d", code)
	}
	if len(warm.Sessions) != 3 || warm.Scan.Running {
		t.Fatalf("warm listing = %+v", warm)
	}
	for selector, n := range counts() {
		if n != 1 {
			t.Fatalf("%s described %d times, want 1", selector, n)
		}
	}
}

// TestCanceledRequestKeepsScanning proves the scan runs on a context this
// process owns: a browser reload cancels its request, and the sessions
// already described must survive it.
func TestCanceledRequestKeepsScanning(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	h := newWebHarness(t, f)
	release := make(chan struct{})
	describer, reached, counts := gatedDescriber(release)
	h.describeWith(describer)

	requestCtx, abandon := context.WithCancel(context.Background())
	var started scanState
	if code := h.do(requestCtx, http.MethodPost, "/api/sessions/refresh", &started); code != http.StatusOK {
		t.Fatalf("/api/sessions/refresh: %d", code)
	}
	if !started.Running {
		t.Fatalf("refresh state = %+v", started)
	}

	// The client goes away mid-scan.
	<-reached
	abandon()
	http.DefaultClient.CloseIdleConnections()
	time.Sleep(50 * time.Millisecond)

	var state scanState
	if code := h.get("/api/scan", &state); code != http.StatusOK {
		t.Fatalf("/api/scan: %d", code)
	}
	if !state.Running {
		t.Fatalf("abandoning the request stopped the scan: %+v", state)
	}

	close(release)
	settled := waitForScan(t, h.get)
	if settled.Described != 3 || settled.Failed != 0 {
		t.Fatalf("settled scan state = %+v", settled)
	}
	var warm sessionsPayload
	if code := h.get("/api/sessions", &warm); code != http.StatusOK {
		t.Fatalf("/api/sessions: %d", code)
	}
	if len(warm.Sessions) != 3 {
		t.Fatalf("listing after abandoned request = %+v", warm)
	}
	if total := describeTotal(counts()); total != 3 {
		t.Fatalf("describes = %d, want 3", total)
	}
}

// TestConcurrentRefreshesRunOneScan proves the coordinator is single-flight:
// two clients asking at once must not double the describe work.
func TestConcurrentRefreshesRunOneScan(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	h := newWebHarness(t, f)
	release := make(chan struct{})
	describer, reached, counts := gatedDescriber(release)
	h.describeWith(describer)

	var wg sync.WaitGroup
	states := make([]scanState, 2)
	for i := range states {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if code := h.post("/api/sessions/refresh", &states[i]); code != http.StatusOK {
				t.Errorf("refresh %d: %d", i, code)
			}
		}(i)
	}
	wg.Wait()
	for i, state := range states {
		if !state.Running {
			t.Fatalf("refresh %d state = %+v", i, state)
		}
	}

	<-reached
	close(release)
	waitForScan(t, h.get)
	described := counts()
	if total := describeTotal(described); total != 3 {
		t.Fatalf("two refreshes performed %d describes, want 3: %+v", total, described)
	}
	for selector, n := range described {
		if n != 1 {
			t.Fatalf("%s described %d times, want 1", selector, n)
		}
	}
}

func describeTotal(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// TestSessionsListReportsProgressToStderr keeps the scripting surface
// synchronous and complete while making a cold run narrate itself: stdout
// stays exactly one document and the progress goes to stderr.
func TestSessionsListReportsProgressToStderr(t *testing.T) {
	f := newFixture(t)
	const count = 30
	for i := range count {
		f.writeSession(sessionSpec{
			project:   "synthetic-progress",
			stem:      fmt.Sprintf("2026-01-05T00-00-%02dZ_00000000-0000-4000-8000-%012d", i, i),
			id:        fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			title:     fmt.Sprintf("Synthetic progress session %d", i),
			workspace: "/synthetic/workspace/progress",
		})
	}

	stdout, stderr := f.ok("sessions", "list", "--json")
	res := decode[sessionsResult](t, stdout)
	if len(res.Sessions) != count {
		t.Fatalf("cold listing returned %d sessions, want %d", len(res.Sessions), count)
	}
	if !strings.Contains(stderr, fmt.Sprintf("describing %d/%d (omp)", scanProgressEvery, count)) {
		t.Fatalf("cold listing stderr = %q", stderr)
	}

	// A warm listing is silent: progress narrates work, not requests.
	stdout, stderr = f.ok("sessions", "list", "--json")
	if warm := decode[sessionsResult](t, stdout); len(warm.Sessions) != count {
		t.Fatalf("warm listing returned %d sessions, want %d", len(warm.Sessions), count)
	}
	if stderr != "" {
		t.Fatalf("warm listing stderr = %q", stderr)
	}
}
