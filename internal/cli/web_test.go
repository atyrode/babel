package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestWebServerServesCLIBackedAPI proves the wired `babel web` server end to
// end in process: static SPA bytes come from the embedded dist, and the API
// answers with the same documents the headless CLI emits, behind its token.
func TestWebServerServesCLIBackedAPI(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()

	a := &app{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	srv, err := a.buildWebServer(repoFlags{}, 0)
	if err != nil {
		t.Fatalf("build web server: %v", err)
	}

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

	base, token, ok := strings.Cut(srv.URL(), "/?token=")
	if !ok {
		t.Fatalf("launch URL %q carries no token", srv.URL())
	}

	get := func(path string, out any) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
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
		t.Fatalf("tokenless request got %d, want 401", resp.StatusCode)
	}

	// The SPA shell is served for the root path.
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	shell, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	shellBody, _ := io.ReadAll(shell.Body)
	shell.Body.Close()
	if shell.StatusCode != http.StatusOK || !bytes.Contains(shellBody, []byte("<div id=")) {
		t.Fatalf("SPA shell: status %d body %.120q", shell.StatusCode, shellBody)
	}

	var listing struct {
		Sessions []struct {
			Selector string `json:"selector"`
			Harness  string `json:"harness"`
		} `json:"sessions"`
		RefreshedAt string `json:"refreshed_at"`
	}
	if code := get("/api/sessions", &listing); code != http.StatusOK {
		t.Fatalf("/api/sessions: %d", code)
	}
	if len(listing.Sessions) == 0 || listing.RefreshedAt == "" {
		t.Fatalf("listing = %+v", listing)
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
