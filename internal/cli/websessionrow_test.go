package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// What a session cost is recorded in its own transcript and the CLI's listing
// has carried it for a while, but the browser — the primary surface — dropped
// all four numbers on the way out of webSessionRow. An operator cannot sort a
// corpus by what it cost if the corpus arrives without the cost, so this pins
// the numbers on the wire the page actually reads, not on the mapping.
//
// Null is asserted alongside the values. Most harnesses record no usage at
// all, and a browser that received 0 for them would be shown a measurement
// nobody took.
func TestWebSessionListingCarriesRecordedUsage(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	f.writePricedSession()

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

	// The first listing answers from a cold catalog and reports the scan it
	// started; the usage is only recorded once a session has been described.
	if code := get("/api/sessions", nil); code != http.StatusOK {
		t.Fatalf("/api/sessions: %d", code)
	}
	waitForScan(t, get)

	var listing struct {
		Sessions []struct {
			Selector    string   `json:"selector"`
			CostUSD     *float64 `json:"cost_usd"`
			TotalTokens *int64   `json:"total_tokens"`
			Turns       *int64   `json:"turns"`
			ToolErrors  *int64   `json:"tool_errors"`
		} `json:"sessions"`
	}
	if code := get("/api/sessions", &listing); code != http.StatusOK {
		t.Fatalf("/api/sessions after scan: %d", code)
	}

	var priced *struct {
		Selector    string   `json:"selector"`
		CostUSD     *float64 `json:"cost_usd"`
		TotalTokens *int64   `json:"total_tokens"`
		Turns       *int64   `json:"turns"`
		ToolErrors  *int64   `json:"tool_errors"`
	}
	unmeasured := 0
	for i, row := range listing.Sessions {
		if row.CostUSD == nil && row.TotalTokens == nil && row.Turns == nil && row.ToolErrors == nil {
			unmeasured++
			continue
		}
		priced = &listing.Sessions[i]
	}
	if priced == nil {
		t.Fatalf("no listed session carries usage: %+v", listing.Sessions)
	}
	if unmeasured == 0 {
		t.Fatal("every listed session carried usage, so absence was never exercised")
	}

	if priced.CostUSD == nil || *priced.CostUSD != 1.25 {
		t.Errorf("cost_usd = %v, want the 1.25 the transcript records", priced.CostUSD)
	}
	if priced.TotalTokens == nil || *priced.TotalTokens != 300 {
		t.Errorf("total_tokens = %v, want 300", priced.TotalTokens)
	}
	if priced.Turns == nil || *priced.Turns != 1 {
		t.Errorf("turns = %v, want the one assistant turn", priced.Turns)
	}
	if priced.ToolErrors == nil || *priced.ToolErrors != 1 {
		t.Errorf("tool_errors = %v, want the one failing tool result", priced.ToolErrors)
	}
}
