package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/web"
)

// TestWebServerServesCLIBackedAPI proves the wired `babel web` server end to
// end in process: static SPA bytes come from the embedded dist, and the API
// answers with the same documents the headless CLI emits, behind its session.
func TestWebServerServesCLIBackedAPI(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()

	a := &app{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	srv, services, err := a.buildWebServer(repoFlags{}, "", 0)
	if err != nil {
		t.Fatalf("build web server: %v", err)
	}
	defer services.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("server did not stop")
		}
	}()

	base, _, session := bootstrapWeb(t, srv.URL())

	get := func(path string, out any) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		authorizeWeb(req, session)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if out != nil && resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(body, out); err != nil {
				t.Fatalf("GET %s: decode %q: %v", path, body, err)
			}
		}
		return resp.StatusCode
	}

	// Unauthorized requests are rejected before any work.
	resp, err := http.Get(base + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("credential-free request got %d, want 401", resp.StatusCode)
	}

	// The SPA shell is served for the root path. It carries no credential at
	// all, because the shell is what runs the bootstrap: a browser's very
	// first request cannot be authenticated and must not need to be.
	shell, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	shellBody, _ := io.ReadAll(shell.Body)
	shell.Body.Close()
	if shell.StatusCode != http.StatusOK || !bytes.Contains(shellBody, []byte("<div id=")) {
		t.Fatalf("SPA shell: status %d body %.120q", shell.StatusCode, shellBody)
	}

	// The listing never blocks on a scan: the first request answers from the
	// catalog — cold, so empty — and reports the scan it started.
	var listing struct {
		Sessions []struct {
			Selector string `json:"selector"`
			Harness  string `json:"harness"`
		} `json:"sessions"`
		RefreshedAt string `json:"refreshed_at"`
		Scan        struct {
			Running   bool `json:"running"`
			Described int  `json:"described"`
			Total     int  `json:"total"`
		} `json:"scan"`
	}
	if code := get("/api/sessions", &listing); code != http.StatusOK {
		t.Fatalf("/api/sessions: %d", code)
	}
	if listing.RefreshedAt == "" {
		t.Fatalf("cold listing = %+v", listing)
	}
	waitForScan(t, get)
	listing.Sessions = nil
	if code := get("/api/sessions", &listing); code != http.StatusOK {
		t.Fatalf("/api/sessions after scan: %d", code)
	}
	if len(listing.Sessions) == 0 || listing.Scan.Running {
		t.Fatalf("scanned listing = %+v", listing)
	}

	sel := listing.Sessions[0].Selector
	var inspected struct {
		Selector    string `json:"selector"`
		PrimaryPath string `json:"primary_path"`
	}
	if code := get("/api/session?selector="+sel, &inspected); code != http.StatusOK {
		t.Fatalf("/api/session: %d", code)
	}
	if inspected.Selector != sel || inspected.PrimaryPath == "" {
		t.Fatalf("inspected = %+v", inspected)
	}

	var tr struct {
		Total  int `json:"total"`
		Events []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if code := get(fmt.Sprintf("/api/transcript?selector=%s&limit=5", sel), &tr); code != http.StatusOK {
		t.Fatalf("/api/transcript: %d", code)
	}
	if tr.Total == 0 || len(tr.Events) == 0 {
		t.Fatalf("transcript = %+v", tr)
	}

	// Unknown selectors are 404, and with no repository configured the
	// archive surface reports 409 rather than pretending.
	if code := get("/api/session?selector=omp/does-not-exist", nil); code != http.StatusNotFound {
		t.Fatalf("unknown selector: %d, want 404", code)
	}
	if code := get("/api/archive/status", nil); code != http.StatusConflict {
		t.Fatalf("unconfigured archive status: %d, want 409", code)
	}
}

// webClient is the caller a browser is: the session cookie that the launch
// nonce was exchanged for, presented on every request.
type webClient struct {
	t       *testing.T
	base    string
	session string
}

// serveWeb serves srv until the test ends and returns a client aimed at it.
func serveWeb(t *testing.T, srv *web.Server) *webClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("server did not stop, well past its own 5s shutdown budget")
		}
	})
	return newWebClient(t, srv.URL())
}

func newWebClient(t *testing.T, launchURL string) *webClient {
	t.Helper()
	base, _, session := bootstrapWeb(t, launchURL)
	return &webClient{t: t, base: base, session: session}
}

// bootstrapWeb performs the §2.7 bootstrap exchange a browser performs on the
// launch URL: it posts the one-time nonce from the fragment and keeps the
// `HttpOnly; SameSite=Strict` session cookie the server answers with. Both
// credentials are returned because the tests assert on both — the nonce is
// spent and must appear in no request line or log, and the session is the only
// thing that authenticates afterwards.
//
// The nonce is single-use, so this runs once per launch. A test that needs a
// second session needs a second launch.
func bootstrapWeb(t *testing.T, launchURL string) (base, nonce, session string) {
	t.Helper()
	base, nonce, ok := strings.Cut(launchURL, "/#nonce=")
	if !ok {
		t.Fatalf("launch URL %q carries no bootstrap nonce", launchURL)
	}
	response, err := http.Post(base+"/api/bootstrap", "application/json",
		strings.NewReader(`{"nonce":"`+nonce+`"}`))
	if err != nil {
		t.Fatalf("bootstrap exchange: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("bootstrap exchange: status %d body %s", response.StatusCode, body)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name != "babel_session" {
			continue
		}
		// The flags are internal/web's contract and tested there; asserting
		// the two that matter here keeps a harness that silently accepted a
		// readable credential from passing for a browser.
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("session cookie is not HttpOnly SameSite=Strict: %+v", cookie)
		}
		return base, nonce, cookie.Value
	}
	t.Fatalf("bootstrap exchange set no session cookie: %v", response.Cookies())
	return "", "", ""
}

// authorizeWeb presents the session the way a bootstrapped browser does. It is
// the only credential channel the server accepts, so a test that forgets it
// gets a 401 rather than a differently-authenticated request.
func authorizeWeb(req *http.Request, session string) {
	req.AddCookie(&http.Cookie{Name: "babel_session", Value: session})
}

// do issues one authenticated request and returns the status with the raw
// body.
//
// The body is raw rather than decoded because what these cases assert is that
// a seeded identifier is in the document the browser receives. A shape
// re-declared here could quietly agree with a listing that dropped it.
func (c *webClient) do(method, path, body string) (int, string) {
	c.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	authorizeWeb(req, c.session)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, string(payload)
}

func (c *webClient) get(path string) (int, string) { return c.do(http.MethodGet, path, "") }

// TestWebServerServesSeededPhaseBState proves the browser reaches the durable
// analysis state this machine holds.
//
// Every route here answered 409 "not available in this session" while
// internal/cli wired only Phase A, which is what made four of the six pages
// dead and put a spurious failure banner on the other two: the shell reports
// any API refusal, so an unwired review service was visible from the sessions
// page as well.
func TestWebServerServesSeededPhaseBState(t *testing.T) {
	f := newFixture(t)
	seeded := f.seed()
	const operator = "operator-under-test"

	a := &app{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	srv, services, err := a.buildWebServer(repoFlags{}, operator, 0)
	if err != nil {
		t.Fatalf("build web server: %v", err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Errorf("close web services: %v", err)
		}
	})
	client := serveWeb(t, srv)

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"hypotheses list the seeded candidate", "/api/hypotheses", seeded.hypothesis},
		{"a candidate carries its observation", "/api/hypothesis?id=" + seeded.hypothesis, seeded.observation},
		{"findings list the seeded finding", "/api/findings", seeded.finding},
		{"a finding is addressable", "/api/finding?id=" + seeded.finding, seeded.finding},
		{"the review queue holds the enrolled record", "/api/review/queue", seeded.hypothesis},
		// A record nobody has decided on yet has an empty history and the
		// standing review status, which is the document this route is for:
		// asserting the identifier back would only restate the request.
		{"review history reads a record's standing status",
			"/api/review/history?type=hypothesis&id=" + seeded.hypothesis, `"status":"new"`},
		{"the reality inbox holds the open question", "/api/reality/inbox", seeded.question},
		{"a reality entity is addressable", "/api/reality/entity?id=" + seeded.entity, seeded.entity},
		{"the analysis state lists the cookbook", "/api/analysis/state", "outcome-integrity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := client.get(tc.path)
			if code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: %.300s", tc.path, code, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("GET %s does not mention %q: %.400s", tc.path, tc.want, body)
			}
		})
	}

	// The retrieval index is a cache with nothing in it on a machine that has
	// not prepared a corpus, so the evidence that it is wired is that the
	// route answers at all instead of refusing.
	t.Run("search is wired", func(t *testing.T) {
		if code, body := client.get("/api/search?q=candidate"); code != http.StatusOK {
			t.Fatalf("GET /api/search = %d, want 200: %.300s", code, body)
		}
	})

	// The one thing only the launch can supply. A mutation with no author is
	// refused by the service rather than defaulted, so the identity being
	// wired is exactly the difference between a browser that can record a
	// §4.7 act and one that cannot.
	t.Run("a mutation records the launch operator", func(t *testing.T) {
		code, body := client.do(http.MethodPost, "/api/review/context", `{"text":"guidance"}`)
		if code != http.StatusOK {
			t.Fatalf("POST /api/review/context = %d, want 200: %.300s", code, body)
		}
		if !strings.Contains(body, operator) {
			t.Fatalf("the recorded guidance does not name the operator: %s", body)
		}
	})
}

// TestWebServerStartsWithoutUsableDurableState proves Phase B state is not a
// launch prerequisite.
//
// A machine with no analysis state, or one whose durable file cannot be
// opened, must keep its sessions and archive surfaces: taking them away would
// make the primary interface unusable over state it does not need. Each
// unopenable store is reported and left unwired, so its pages keep the honest
// refusal rather than rendering as an empty frontier.
func TestWebServerStartsWithoutUsableDurableState(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	// A directory where the durable database belongs is the cheapest way to
	// make every store above it fail for a reason that is not this test's own
	// invention: it is what a half-restored backup or a wrong mount looks
	// like, and SQLite reports it rather than creating a file beside it.
	if err := os.MkdirAll(filepath.Join(f.dataDir, "durable.db"), 0o700); err != nil {
		t.Fatal(err)
	}

	stderr := &bytes.Buffer{}
	a := &app{stdout: &bytes.Buffer{}, stderr: stderr}
	srv, services, err := a.buildWebServer(repoFlags{}, "operator-under-test", 0)
	if err != nil {
		t.Fatalf("babel web refused to start without durable state: %v", err)
	}
	t.Cleanup(func() {
		if err := services.Close(); err != nil {
			t.Errorf("close web services: %v", err)
		}
	})
	client := serveWeb(t, srv)

	// The first listing starts the scan the catalog needs; the rows arrive
	// once it settles. This is the Phase A path that must survive unusable
	// durable state, so it is exercised the way the browser drives it.
	decode := func(path string, out any) int {
		code, body := client.get(path)
		if out != nil && code == http.StatusOK {
			if err := json.Unmarshal([]byte(body), out); err != nil {
				t.Fatalf("GET %s: decode %q: %v", path, body, err)
			}
		}
		return code
	}
	if code := decode("/api/sessions", nil); code != http.StatusOK {
		t.Fatalf("cold GET /api/sessions = %d, want 200", code)
	}
	waitForScan(t, decode)
	var listing struct {
		Sessions []struct {
			Selector string `json:"selector"`
		} `json:"sessions"`
	}
	code, body := client.get("/api/sessions")
	if code != http.StatusOK {
		t.Fatalf("GET /api/sessions = %d, want 200: %.300s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Sessions) == 0 {
		t.Fatalf("the sessions page lost its rows with no durable state: %.300s", body)
	}

	for _, tc := range []struct{ path, want string }{
		{"/api/hypotheses", "the hypothesis frontier is not available in this session"},
		{"/api/findings", "the hypothesis frontier is not available in this session"},
		{"/api/review/queue", "the review service is not available in this session"},
		{"/api/reality/inbox", "the reality ledger is not available in this session"},
	} {
		code, body := client.get(tc.path)
		if code != http.StatusConflict {
			t.Errorf("GET %s = %d, want 409: %.300s", tc.path, code, body)
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("GET %s body %q does not carry %q", tc.path, body, tc.want)
		}
	}

	// The refusal an operator sees in the browser is only actionable if the
	// launch said why, so the reason is on the diagnostics stream the server
	// already logs to.
	diagnostics := stderr.String()
	for _, want := range []string{"analysis state unavailable", "reality ledger unavailable"} {
		if !strings.Contains(diagnostics, want) {
			t.Errorf("the launch diagnostics do not mention %q: %q", want, diagnostics)
		}
	}
}

// TestWebServicesCloseReleasesStores proves the release itself, portably: each
// service the launch wired stops answering once the handles are closed, so
// nothing behind the interface values the server held kept a usable database.
func TestWebServicesCloseReleasesStores(t *testing.T) {
	f := newFixture(t)
	seeded := f.seed()
	ctx := context.Background()

	a := &app{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	d, err := babelDirs()
	if err != nil {
		t.Fatal(err)
	}
	services := a.openWebServices(d)

	// Each reader works first, so what the assertions below observe is the
	// close and not a service that never opened.
	reads := map[string]func() error{
		"frontier": func() error {
			_, err := services.frontier().Hypothesis(ctx, seeded.hypothesis)
			return err
		},
		"reality ledger": func() error {
			_, err := services.realityService().Entity(ctx, seeded.entity)
			return err
		},
		"run receipts": func() error {
			_, _, err := services.runs().Runs(ctx, 10, 0)
			return err
		},
		"retrieval index": func() error {
			_, err := services.search().Search(ctx, index.Query{Match: "candidate"})
			return err
		},
	}
	for name, read := range reads {
		if err := read(); err != nil {
			t.Fatalf("the wired %s does not answer: %v", name, err)
		}
	}

	if err := services.Close(); err != nil {
		t.Fatalf("close web services: %v", err)
	}
	for name, read := range reads {
		if err := read(); err == nil {
			t.Errorf("the %s still answers after the launch's handles were closed", name)
		}
	}
}

// heldDatabases reports this process's open descriptors on the fixture's
// durable state and retrieval index.
//
// This is what a leaked store actually is: not a nil field somewhere, but a
// file this process still holds, which is what would keep a later `babel
// explore` waiting on §9's local state-writer lock.
func heldDatabases(t *testing.T, f *fixture) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("this platform exposes no descriptor table at /proc/self/fd: %v", err)
	}
	root := f.root
	if resolved, err := filepath.EvalSymlinks(f.root); err == nil {
		root = resolved
	}
	var held []string
	for _, entry := range entries {
		// A descriptor can close between the listing and the readlink,
		// including the one this loop is reading through, so an unreadable
		// entry is not a held file.
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil || !strings.HasPrefix(target, root) {
			continue
		}
		// The -wal and -shm siblings count: they are the same handle's files
		// and they are what a reader keeps mapped.
		if strings.Contains(target, "durable.db") || strings.Contains(target, index.FileName) {
			held = append(held, target)
		}
	}
	return held
}

// TestWebCommandReleasesDurableHandlesOnStop drives the real command rather
// than its builder, because the thing under test is where shutdown is hooked:
// the handles are opened for the server's lifetime, so they have to be
// released after Serve returns and before the command does.
func TestWebCommandReleasesDurableHandlesOnStop(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	seeded := f.seed()
	t.Setenv("BABEL_OPERATOR", "operator-under-test")

	if held := heldDatabases(t, f); len(held) != 0 {
		t.Fatalf("the fixture already holds durable descriptors: %v", held)
	}

	stdout := &syncBuffer{}
	a := &app{stdout: stdout, stderr: &syncBuffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.webCmd(ctx, []string{"--port", "0"}) }()

	launchURL := waitForLaunchURL(t, stdout)
	client := newWebClient(t, launchURL)
	code, body := client.get("/api/hypotheses")
	if code != http.StatusOK {
		t.Fatalf("GET /api/hypotheses = %d, want 200: %.300s", code, body)
	}
	if !strings.Contains(body, seeded.hypothesis) {
		t.Fatalf("`babel web` does not list the seeded candidate: %.400s", body)
	}

	// Non-vacuity for the assertion after the stop: the serving command really
	// is holding the durable file open, so releasing it is observable.
	if held := heldDatabases(t, f); len(held) == 0 {
		t.Fatal("the serving command holds no durable descriptor, so releasing one would prove nothing")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("babel web: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("babel web did not return after its context was canceled")
	}

	if held := heldDatabases(t, f); len(held) != 0 {
		t.Fatalf("babel web leaked %d descriptor(s) on stop: %v", len(held), held)
	}
}

// waitForLaunchURL reads the URL from the command's own startup line, which is
// the only place a caller learns the ephemeral port and the launch token.
func waitForLaunchURL(t *testing.T, stdout *syncBuffer) string {
	t.Helper()
	const marker = "listening at "
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out := stdout.String()
		if _, after, ok := strings.Cut(out, marker); ok {
			if line, _, done := strings.Cut(after, "\n"); done {
				return strings.TrimSpace(line)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("babel web printed no launch line: %q", stdout.String())
	return ""
}
