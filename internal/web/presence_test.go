package web

// The fleet presence read surface's own properties (issue #118).
//
// The reader is a fake, for fleet_test.go's reason and one more. A real
// *presence.Store needs PostgreSQL, and what these tests are about is the
// rendering contract on this side of that seam: that the classification crosses
// the wire beside the age it was made from, that a stale row is never rendered
// as anything but stale, that a finished row carries its receipt, that a machine
// with no presence table and a machine whose catalog is down say different
// things, and that neither says it by failing. Every one of those is a property
// of the handler.
//
// The one more is that a fake is the only way to construct the cases that
// matter. A stale heartbeat, a lost one and a run that finished are exactly the
// states a healthy single-host deployment cannot produce on demand — a real
// store would need a wall clock wound forward — and they are the states the whole
// epistemic argument of #118 rests on.
//
// internal/presence owns what the fake stands in for: the announce/heartbeat/
// finalize sequence, the catalog-side age computation, and Classify's thresholds
// are checked against a real catalog there.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The fixture's machines. The presence rows deliberately reuse fleet_test.go's
// two host ids rather than inventing a third pair: presence and records are two
// reads of one deployment, and a test corpus in which the machine running a job
// is not the machine that committed anything would let a rendering bug that
// crossed the two go unnoticed.

// fakePresence is a wired PresenceReader over fixed rows.
//
// It records how many times it was read, because a page that polls is the
// expected client here and a handler that read twice per request would double
// the load on a catalog the whole fleet shares.
type fakePresence struct {
	rows []presence.Row
	// fail is returned instead of rows when set, so "this machine has no
	// presence table" and "this machine's catalog did not answer" are
	// distinguishable from the outside.
	fail  error
	reads int
}

func (f *fakePresence) Fleet(context.Context) ([]presence.Row, error) {
	f.reads++
	if f.fail != nil {
		return nil, f.fail
	}
	return f.rows, nil
}

// presenceRowFixture builds one announced run. The age is given rather than
// derived from a clock, which is the fake's whole purpose: internal/presence
// computes the age inside its query, so a test that subtracted timestamps here
// would be testing arithmetic this package deliberately does not perform.
//
// Freshness is filled by presence.Classify rather than by the caller, so a
// fixture cannot assert a classification the package would not have made — which
// would make the "never re-derived" property untestable by making the fixture
// the second deriver.
func presenceRowFixture(id, host string, local bool, kind presence.Kind, state presence.State,
	age time.Duration, text string) presence.Row {
	started := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	heartbeat := started.Add(20 * time.Minute)
	row := presence.Row{
		ID:            presence.PresenceID(id),
		Host:          host,
		Local:         local,
		Deployment:    "deployment " + text,
		Kind:          kind,
		RunID:         "run-" + id + " " + text,
		Recipe:        "recipe-" + id + " " + text,
		PreparationID: "prep-" + id + " " + text,
		Authority: run.Authority{
			Kind: run.AuthorityOperator,
			Ref:  "babel explore " + text,
		},
		State:        state,
		StartedAt:    started,
		HeartbeatAt:  heartbeat,
		HeartbeatAge: age,
	}
	if state != presence.StateRunning {
		row.FinishedAt = heartbeat
		row.ReceiptRecordID = "rcp-" + id + " " + text
	}
	row.Freshness = presence.Classify(state, age)
	return row
}

// presenceFixture is the fleet every phaseB server reads presence through: this
// machine's own fresh run, another machine's stale one, a third whose heartbeat
// is old enough to be lost, a conductor cycle another machine finished with a
// receipt, and a cancelled run — because a cancelled run is not a failed one and
// the row has to be able to say which.
//
// text is woven into every string a host chose, for fleetFixture's reason: the
// malicious-content sweep is then the ordinary fixture rather than a second one
// that can drift from it. A recipe id and an authority reference are the two
// fields another machine's operator writes, so both carry it.
func presenceFixture(text string) *fakePresence {
	return &fakePresence{rows: []presence.Row{
		presenceRowFixture("pres-local-fresh", localFleetHost, true,
			presence.KindExplore, presence.StateRunning, 12*time.Second, text),
		presenceRowFixture("pres-remote-stale", remoteFleetHost, false,
			presence.KindConductor, presence.StateRunning, 4*time.Minute, text),
		presenceRowFixture("pres-remote-lost", remoteFleetHost, false,
			presence.KindExplore, presence.StateRunning, 3*time.Hour, text),
		presenceRowFixture("pres-remote-done", remoteFleetHost, false,
			presence.KindConductor, presence.StateFinished, 90*time.Minute, text),
		presenceRowFixture("pres-local-cancelled", localFleetHost, true,
			presence.KindExplore, presence.StateCancelled, 2*time.Hour, text),
	}}
}

// presenceOf reaches the wired fake so a test can replace its rows or its
// failure without rebuilding the whole harness.
func (h *phaseB) presenceOf() *fakePresence {
	h.t.Helper()
	fake, ok := h.server.opts.Presence.(*fakePresence)
	if !ok {
		h.t.Fatalf("the wired presence reader is %T, not the fixture", h.server.opts.Presence)
	}
	return fake
}

func (h *phaseB) presence() presenceFleet {
	h.t.Helper()
	var result presenceFleet
	decodeResponse(h.t, h.get("/api/fleet/presence"), &result)
	return result
}

// TestPresenceCarriesTheAgeBesideTheClassification is the assertion the whole
// route exists for.
//
// A word on its own is not enough. "stale" tells a reader that a threshold was
// crossed and nothing about how long ago, and the interface #118 asks for has to
// be able to say "last seen 4m ago — running or dead, this host cannot tell".
// That sentence is only constructible if the raw age travels beside the word, so
// a response that carried one without the other would make the honest rendering
// impossible no matter how the page was written.
//
// The thresholds travel too, and are asserted against internal/presence's own
// constants rather than against numbers copied here: a page that hard-coded two
// minutes would eventually contradict the server that classified the row, and a
// test that hard-coded them would not notice.
func TestPresenceCarriesTheAgeBesideTheClassification(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	result := h.presence()

	if !result.Available || !result.Configured {
		t.Fatalf("wired presence = available %t configured %t", result.Available, result.Configured)
	}
	if got, want := len(result.Rows), 5; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	if got, want := result.StaleAfterSeconds, int64(presence.StaleAfter/time.Second); got != want {
		t.Errorf("stale_after_seconds = %d, want %d", got, want)
	}
	if got, want := result.LostAfterSeconds, int64(presence.LostAfter/time.Second); got != want {
		t.Errorf("lost_after_seconds = %d, want %d", got, want)
	}
	if got, want := result.RetentionSeconds, int64(presence.RetentionWindow/time.Second); got != want {
		t.Errorf("retention_seconds = %d, want %d", got, want)
	}
	// Three running rows and two that are not. The count is of claims rather
	// than of live processes, which is why the lost row counts: its host said
	// it was running and nothing has said otherwise.
	if got, want := result.Running, 3; got != want {
		t.Errorf("running = %d, want %d", got, want)
	}

	for _, want := range []struct {
		id        string
		freshness string
		age       int64
		state     string
		local     bool
		kind      string
		receipt   bool
	}{
		{id: "pres-local-fresh", freshness: "fresh", age: 12, state: "running",
			local: true, kind: "explore"},
		{id: "pres-remote-stale", freshness: "stale", age: 240, state: "running",
			kind: "conductor"},
		{id: "pres-remote-lost", freshness: "lost", age: 10800, state: "running",
			kind: "explore"},
		{id: "pres-remote-done", freshness: "finished", age: 5400, state: "finished",
			kind: "conductor", receipt: true},
		{id: "pres-local-cancelled", freshness: "finished", age: 7200, state: "cancelled",
			local: true, kind: "explore", receipt: true},
	} {
		row, found := presenceByID(result.Rows, want.id)
		if !found {
			t.Errorf("no row %q in %+v", want.id, result.Rows)
			continue
		}
		if row.Freshness != want.freshness {
			t.Errorf("%s freshness = %q, want %q", want.id, row.Freshness, want.freshness)
		}
		if row.HeartbeatAgeSeconds != want.age {
			t.Errorf("%s heartbeat_age_seconds = %d, want %d",
				want.id, row.HeartbeatAgeSeconds, want.age)
		}
		if row.State != want.state {
			t.Errorf("%s state = %q, want %q", want.id, row.State, want.state)
		}
		if row.LocalHost != want.local {
			t.Errorf("%s local_host = %t, want %t", want.id, row.LocalHost, want.local)
		}
		if row.Kind != want.kind {
			t.Errorf("%s kind = %q, want %q", want.id, row.Kind, want.kind)
		}
		if (row.ReceiptRecordID != "") != want.receipt {
			t.Errorf("%s receipt_record_id = %q, want present %t",
				want.id, row.ReceiptRecordID, want.receipt)
		}
		// A finished row has a finish time and a running one does not. Year
		// one in that field would make an in-flight run read as finished at
		// the dawn of time, which is the absence this omission protects.
		if finished := row.State != "running"; finished != (row.FinishedAt != "") {
			t.Errorf("%s state %q with finished_at %q", want.id, row.State, row.FinishedAt)
		}
	}
}

// TestPresenceClassificationIsThePackagesOwn checks the property the file's
// header claims: nothing in internal/web decides what an age means.
//
// It is asserted by contradiction rather than by comparison. The fake is handed
// a row whose Freshness disagrees with its own age — "fresh" at three hours,
// which presence.Classify would never produce — and the response must carry the
// disagreement through. A handler that had recomputed the word would silently
// correct it, and would then be the second answer to a question internal/presence
// exists to answer once.
//
// The point is not that a lying row should be rendered. It is that there is
// exactly one classifier in the deployment, and the way to keep it that way is
// for this side of the seam to have no opinion at all.
func TestPresenceClassificationIsThePackagesOwn(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	fake := h.presenceOf()
	fake.rows = []presence.Row{{
		ID:           "pres-disagreeing",
		Host:         remoteFleetHost,
		Kind:         presence.KindExplore,
		RunID:        "run-disagreeing",
		State:        presence.StateRunning,
		HeartbeatAge: 3 * time.Hour,
		Freshness:    presence.FreshnessFresh,
	}}

	result := h.presence()
	if got, want := len(result.Rows), 1; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	if got := result.Rows[0].Freshness; got != string(presence.FreshnessFresh) {
		t.Errorf("freshness = %q; the handler re-derived it instead of carrying the reader's", got)
	}
	if got, want := result.Rows[0].HeartbeatAgeSeconds, int64(10800); got != want {
		t.Errorf("heartbeat_age_seconds = %d, want %d", got, want)
	}
}

// TestPresenceAgeNeverRendersNegative covers the skew case, which is reachable
// rather than theoretical: the catalog subtracts a timestamp another machine
// wrote from its own clock, so a writer running ahead puts a heartbeat in the
// catalog's future.
//
// Zero rather than a negative number, on internal/fleet's terms for snapshot
// ages: "last seen -4m ago" is not a fact about anything. The classification is
// untouched, because the package made it against the duration it actually had.
func TestPresenceAgeNeverRendersNegative(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	fake := h.presenceOf()
	fake.rows = []presence.Row{{
		ID:           "pres-skewed",
		Host:         remoteFleetHost,
		Kind:         presence.KindExplore,
		RunID:        "run-skewed",
		State:        presence.StateRunning,
		HeartbeatAge: -90 * time.Second,
		Freshness:    presence.Classify(presence.StateRunning, -90*time.Second),
	}}

	result := h.presence()
	if got, want := result.Rows[0].HeartbeatAgeSeconds, int64(0); got != want {
		t.Errorf("heartbeat_age_seconds = %d, want %d", got, want)
	}
	if got := result.Rows[0].Freshness; got != string(presence.FreshnessFresh) {
		t.Errorf("freshness = %q, want the package's own answer for a future heartbeat", got)
	}
}

// TestPresenceReportsAnEmptyFleetAsAnAnswer separates the two things an empty
// list can mean, which this route must not conflate.
//
// A configured machine whose deployment is idle answers `available: true` with no
// rows: it asked, and the answer is that nothing is running. That has to read
// differently from a machine that could not ask, and the flag is what lets a page
// say "nothing is running" in the first case rather than "this host cannot tell".
func TestPresenceReportsAnEmptyFleetAsAnAnswer(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	fake := h.presenceOf()
	fake.rows = nil

	result := h.presence()
	if !result.Available || !result.Configured {
		t.Fatalf("idle fleet = available %t configured %t", result.Available, result.Configured)
	}
	if result.Rows == nil {
		t.Errorf("rows is null; an empty list must be a list, not an absence a client has to guard")
	}
	if got := len(result.Rows); got != 0 {
		t.Errorf("rows = %d, want 0", got)
	}
	if result.Running != 0 {
		t.Errorf("running = %d, want 0", result.Running)
	}
	if result.Unavailable != "" {
		t.Errorf("idle fleet carries a note: %q", result.Unavailable)
	}
}

// TestPresenceUnavailableCasesReadDifferently is the degradation contract.
//
// Four ways this route can fail to have rows, three sentences, and every one of
// them HTTP 200. The status is the deliberate part: refusing would make a
// working local-mode machine look broken, and would take away the one surface
// whose whole job is to say "this host cannot tell".
//
// What distinguishes the cases is `configured` and the sentence, and the three
// sentences exist because they call for three different actions. Local mode is
// configuration. An unreachable catalog is an outage that resolves itself. A
// catalog that answered and refused is a misconfiguration that does not — a
// missing migration, or a credential without the privilege — and telling that
// operator to configure shared mode would send him to the wrong place entirely.
//
// The predicates are internal/presence's own, so the fixtures wrap the sentinels
// that package classifies by rather than inventing message text a matcher would
// then have to parse.
func TestPresenceUnavailableCasesReadDifferently(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*Options)
		fail       error
		configured bool
		phrase     string
	}{
		{
			name:   "no reader wired",
			mutate: func(o *Options) { o.Presence = nil },
			phrase: "local mode",
		},
		{
			name:   "reader reports local mode",
			fail:   fmt.Errorf("open presence: %w", presence.ErrNotConfigured),
			phrase: "local mode",
		},
		{
			name: "catalog was never reached",
			fail: fmt.Errorf("presence: read fleet: %w: dial tcp: connect: connection refused",
				sharedcatalog.ErrUnreachable),
			configured: true,
			phrase:     "could not be reached",
		},
		{
			// A catalog that answered and said no. It must not read as an
			// outage: nothing here resolves itself, and the operator has a
			// migration to run or a grant to make.
			name:       "catalog refused the read",
			fail:       errors.New(`pq: relation "fleet_presence" does not exist`),
			configured: true,
			phrase:     "refused this read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPhaseB(t, "plain", tc.mutate)
			if tc.fail != nil {
				h.presenceOf().fail = tc.fail
			}
			response := h.get("/api/fleet/presence")
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			var result presenceFleet
			decodeResponse(t, response, &result)
			if result.Available {
				t.Errorf("available = true for %s", tc.name)
			}
			if result.Configured != tc.configured {
				t.Errorf("configured = %t, want %t", result.Configured, tc.configured)
			}
			if !strings.Contains(result.Unavailable, tc.phrase) {
				t.Errorf("note = %q, want it to mention %q", result.Unavailable, tc.phrase)
			}
			if result.Rows == nil {
				t.Errorf("rows is null; a client must be able to iterate the empty case")
			}
			// The thresholds are stated even when there are no rows, so a page
			// can explain what the classification would have meant.
			if result.StaleAfterSeconds == 0 || result.LostAfterSeconds == 0 {
				t.Errorf("thresholds absent from a degraded answer: %+v", result)
			}
		})
	}
}

// TestPresenceErrorTextNeverReachesTheClient checks the one thing a failure must
// not do. The fixtures are shaped like the errors a real catalog produces: one
// carries a whole connection string with a password in it, which is what a
// wrapped pgx error routinely quotes, and one is a server-side refusal naming a
// relation. §9 keeps credentials out of what a client is told, so the response
// carries a fixed sentence and the diagnostics stream carries no error text
// either.
//
// Both classes are driven, because they take different branches in the handler
// and a leak added to one would not be caught by covering the other.
func TestPresenceErrorTextNeverReachesTheClient(t *testing.T) {
	const secret = "postgres://babel:hunter2@catalog.internal:5432/babel"
	for _, tc := range []struct {
		name   string
		fail   error
		phrase string
	}{
		{
			name: "unreachable",
			fail: fmt.Errorf("presence: read fleet: %s: %w", secret,
				sharedcatalog.ErrUnreachable),
			phrase: "could not be reached",
		},
		{
			name:   "refused",
			fail:   fmt.Errorf(`presence: read fleet: %s: pq: permission denied`, secret),
			phrase: "refused this read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics strings.Builder
			h := newPhaseB(t, "plain", func(o *Options) { o.Diagnostics = &diagnostics })
			h.presenceOf().fail = tc.fail

			text := body(t, h.get("/api/fleet/presence"))
			for _, forbidden := range []string{secret, "hunter2", "catalog.internal"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("response quotes %q: %s", forbidden, text)
				}
				if strings.Contains(diagnostics.String(), forbidden) {
					t.Errorf("diagnostics quote %q: %s", forbidden, diagnostics.String())
				}
			}
			if !strings.Contains(text, tc.phrase) {
				t.Errorf("response does not say what happened: %s", text)
			}
		})
	}
}

// TestPresenceReadsTheCatalogOnce guards the cost of the page that polls this
// route. A handler that read twice — once for the rows and once for a count it
// could have derived — would double the load every browser in the fleet puts on
// one shared PostgreSQL.
func TestPresenceReadsTheCatalogOnce(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	fake := h.presenceOf()
	h.presence()
	if got, want := fake.reads, 1; got != want {
		t.Errorf("catalog reads per request = %d, want %d", got, want)
	}
}

// TestPresencePreservesTheReadersOrder checks that this package does not sort.
//
// internal/presence returns running rows before finished ones, newest heartbeat
// first, which is the order the question is asked in. A handler that re-sorted
// would be a second answer to it, and the two would drift the moment either
// changed — so the fixture is handed in a deliberately awkward order and must
// come back in exactly that order.
func TestPresencePreservesTheReadersOrder(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	fake := h.presenceOf()
	want := []string{}
	for _, row := range fake.rows {
		want = append(want, string(row.ID))
	}

	result := h.presence()
	got := []string{}
	for _, row := range result.Rows {
		got = append(got, row.ID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestPresenceHostileFieldsStaySanitized drives the hostile fixture straight at
// this route and reads the wire bytes.
//
// The escaping sweep in phaseb_test.go already covers every enrolled route, and
// this is not a duplicate of it: the sweep asserts that nothing dangerous
// survives, and this asserts that the specific fields another machine's operator
// chooses — a recipe id and an authority reference — are the ones carrying the
// hostile text, so a future refactor that dropped those fields from the DTO
// could not quietly pass the sweep by rendering less.
func TestPresenceHostileFieldsStaySanitized(t *testing.T) {
	h := newPhaseB(t, malicious, nil)
	text := body(t, h.get("/api/fleet/presence"))
	for _, forbidden := range []string{"\x1b", "\u202e", "\u202c", "\u200b", "<script"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("response carries %q unescaped: %s", forbidden, text)
		}
	}

	result := h.presence()
	row, found := presenceByID(result.Rows, "pres-remote-stale")
	if !found {
		t.Fatalf("no stale row in %+v", result.Rows)
	}
	// The fields are still populated. A sanitizer that emptied them would pass
	// every escaping assertion above and leave the operator unable to see which
	// recipe another machine is running.
	for name, value := range map[string]string{
		"recipe":         row.Recipe,
		"preparation_id": row.PreparationID,
		"run_id":         row.RunID,
		"authority ref":  row.Authority.Ref,
	} {
		if value == "" {
			t.Errorf("%s is empty; hostile content was dropped rather than escaped", name)
		}
		if strings.Contains(value, "\x1b") {
			t.Errorf("%s carries a raw control sequence: %q", name, value)
		}
	}
	if row.Authority.Kind != string(run.AuthorityOperator) {
		t.Errorf("authority kind = %q, want %q", row.Authority.Kind, run.AuthorityOperator)
	}
}

// TestPresenceRejectsAMutation states the guarantee the type system already
// enforces, at the surface a client can actually reach: internal/web holds no
// presence.Announcer, so there is no route to write one — and this route itself
// refuses anything but a GET rather than treating a POST as a read.
func TestPresenceRejectsAMutation(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	response := h.post("/api/fleet/presence", `{"state":"running"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("POST status = %d, want 400", response.StatusCode)
	}
	if got := h.presenceOf().reads; got != 0 {
		t.Errorf("a refused POST reached the reader %d times", got)
	}
}

func presenceByID(rows []presenceRow, id string) (presenceRow, bool) {
	for _, row := range rows {
		if strings.HasPrefix(row.ID, id) {
			return row, true
		}
	}
	return presenceRow{}, false
}
