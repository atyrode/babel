package presence_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/run"
)

// The whole announced shape must survive the round trip, because every field is
// something a fleet surface renders and nothing else in Babel can supply: a run
// in flight has no receipt to read this from yet, which is the gap #118 exists
// to close.
func TestAnnouncedRunIsReadableFromTheFleet(t *testing.T) {
	ctx := t.Context()
	db := newCatalog(t)
	sink := &diagSink{}
	store := newStore(t, db, sink)

	before := time.Now()
	id, err := store.Announce(ctx, presence.Announcement{
		Kind:          presence.KindExplore,
		RunID:         "run-20260831T090000Z",
		Recipe:        "improves-babel",
		PreparationID: "prep-7",
		Authority:     run.Authority{Kind: run.AuthorityPolicy, Ref: "duty-improves-babel"},
	})
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if id == "" {
		t.Fatalf("Announce returned no id against a reachable catalog; diagnostics: %s", sink.joined())
	}
	if !strings.HasPrefix(string(id), "prs_") {
		t.Errorf("id %q carries no kind prefix, so a stray one in a log says nothing", id)
	}

	rows, err := store.Fleet(ctx)
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Fleet returned %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.ID != id {
		t.Errorf("row id %q, want %q", row.ID, id)
	}
	if row.Host != testHost || row.Deployment != testDeployment {
		t.Errorf("row identity = host %q deployment %q, want %q and %q",
			row.Host, row.Deployment, testHost, testDeployment)
	}
	if row.Kind != presence.KindExplore {
		t.Errorf("kind %q, want %q", row.Kind, presence.KindExplore)
	}
	if row.RunID != "run-20260831T090000Z" || row.Recipe != "improves-babel" || row.PreparationID != "prep-7" {
		t.Errorf("scope = run %q recipe %q preparation %q", row.RunID, row.Recipe, row.PreparationID)
	}
	want := run.Authority{Kind: run.AuthorityPolicy, Ref: "duty-improves-babel"}
	if row.Authority != want {
		t.Errorf("authority = %+v, want %+v; a fleet that shows what is running but not why is the opacity #96 objected to",
			row.Authority, want)
	}
	if row.State != presence.StateRunning {
		t.Errorf("state %q, want %q", row.State, presence.StateRunning)
	}
	if !row.FinishedAt.IsZero() {
		t.Errorf("a running row carries a finish time %s", row.FinishedAt)
	}
	if row.ReceiptRecordID != "" {
		t.Errorf("a running row names receipt %q it cannot have committed yet", row.ReceiptRecordID)
	}
	if row.Freshness != presence.FreshnessFresh {
		t.Errorf("freshness %q for a row announced a moment ago, want %q", row.Freshness, presence.FreshnessFresh)
	}
	if row.HeartbeatAge < 0 || row.HeartbeatAge > time.Minute {
		t.Errorf("heartbeat age %s is not plausible for a row just announced", row.HeartbeatAge)
	}
	if !row.Local {
		t.Error("the announcing host's own row is not marked local, so a fleet view cannot say \"this host\"")
	}

	// Server time, not the client's: the row's own timestamps come from
	// PostgreSQL, so a host with a skewed clock cannot announce itself
	// permanently fresh. The only thing a test can assert cheaply is that they
	// are not the client's - a plausible window around the call is enough to
	// catch a writer that started sending time.Now.
	if row.StartedAt.Before(before.Add(-time.Hour)) || row.StartedAt.After(before.Add(time.Hour)) {
		t.Errorf("started_at %s is nowhere near the call, so it came from neither clock", row.StartedAt)
	}
	if len(sink.errs) != 0 {
		t.Errorf("a successful announce diagnosed %s", sink.joined())
	}
}

// The choreography a run performs: announce, heartbeat while it works, finalize
// with the receipt. Each step must be visible in the row, because a fleet
// surface renders exactly this sequence.
func TestAnnounceHeartbeatFinalize(t *testing.T) {
	ctx := t.Context()
	db := newCatalog(t)
	sink := &diagSink{}
	store := newStore(t, db, sink)

	id, err := store.Announce(ctx, presence.Announcement{
		Kind:      presence.KindConductor,
		RunID:     "run-cyc-1",
		Authority: run.Authority{Kind: run.AuthorityOperator, Ref: "invitation:inv-1"},
	})
	if err != nil || id == "" {
		t.Fatalf("Announce = (%q, %v)", id, err)
	}
	announced := rowByID(t, mustFleet(t, store), id)

	// The heartbeat moves the one column it is for, and moves it forward.
	// clock_timestamp advances within a session, so a second call is a later
	// instant even inside one test.
	if err := store.Heartbeat(ctx, id); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	beaten := rowByID(t, mustFleet(t, store), id)
	if !beaten.HeartbeatAt.After(announced.HeartbeatAt) {
		t.Errorf("heartbeat_at did not advance: %s then %s", announced.HeartbeatAt, beaten.HeartbeatAt)
	}
	if !beaten.StartedAt.Equal(announced.StartedAt) {
		t.Errorf("a heartbeat moved started_at from %s to %s", announced.StartedAt, beaten.StartedAt)
	}
	if beaten.State != presence.StateRunning || !beaten.FinishedAt.IsZero() {
		t.Errorf("a heartbeat ended the row: %+v", beaten)
	}

	if err := store.Finalize(ctx, id, presence.Outcome{
		State:           presence.StateFinished,
		ReceiptRecordID: "rcpt-abc",
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	done := rowByID(t, mustFleet(t, store), id)
	if done.State != presence.StateFinished {
		t.Errorf("state %q after Finalize, want %q", done.State, presence.StateFinished)
	}
	if done.FinishedAt.IsZero() {
		t.Error("a finished row carries no finish time")
	}
	if done.ReceiptRecordID != "rcpt-abc" {
		t.Errorf("receipt %q, want rcpt-abc: it is the join from \"this run finished\" to what it produced",
			done.ReceiptRecordID)
	}
	if done.Freshness != presence.FreshnessFinished {
		t.Errorf("freshness %q for a finalized row, want %q", done.Freshness, presence.FreshnessFinished)
	}
	// A finalize is the last thing a live process says, so it counts as being
	// heard from: that is what keeps the retention window one predicate over
	// one column.
	if !done.HeartbeatAt.After(beaten.HeartbeatAt) {
		t.Errorf("Finalize left heartbeat_at at %s; a finished row's age should say how long ago it ended",
			done.HeartbeatAt)
	}
	if len(sink.errs) != 0 {
		t.Errorf("the whole choreography diagnosed %s", sink.joined())
	}
}

// A finalized row is final, and both halves of that must hold: the writer's
// calls become no-ops rather than errors, and the database refuses the write
// even if something bypasses the writer. Without the second half a zombie
// process could resurrect a run that already reported how it ended.
func TestAFinalizedRowIsFinal(t *testing.T) {
	ctx := t.Context()
	db := newCatalog(t)
	sink := &diagSink{}
	store := newStore(t, db, sink)

	id, err := store.Announce(ctx, presence.Announcement{
		Kind: presence.KindExplore, RunID: "run-1",
	})
	if err != nil || id == "" {
		t.Fatalf("Announce = (%q, %v)", id, err)
	}
	if err := store.Finalize(ctx, id, presence.Outcome{State: presence.StateCancelled}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// A late heartbeat from a process that had not noticed: no error, no
	// change. This is the race a run actually produces, because the heartbeat
	// loop and the finalize are different goroutines.
	if err := store.Heartbeat(ctx, id); err != nil {
		t.Errorf("a late heartbeat failed: %v", err)
	}
	// A second finalize, which is what a retry looks like.
	if err := store.Finalize(ctx, id, presence.Outcome{State: presence.StateFinished}); err != nil {
		t.Errorf("a second finalize failed: %v", err)
	}
	row := rowByID(t, mustFleet(t, store), id)
	if row.State != presence.StateCancelled {
		t.Errorf("state %q, want %q: the first terminal answer stands", row.State, presence.StateCancelled)
	}
	if len(sink.errs) != 0 {
		t.Errorf("a no-op write diagnosed %s", sink.joined())
	}

	// And the database itself refuses, so the rule survives a writer that does
	// not know it.
	_, err = db.ExecContext(ctx,
		`UPDATE presence SET state = 'running', finished_at = NULL WHERE presence_id = $1`, string(id))
	if err == nil {
		t.Error("PostgreSQL allowed a finalized row to be reopened")
	}
}

// An announcement is fixed at announce time. This is the exception to the
// append-only discipline being kept narrow: the table admits UPDATE so that a
// heartbeat need not be a new row, and the trigger is what stops that from
// becoming a licence to rewrite who ran what, where, and under whose authority.
func TestTheDatabaseRefusesRewritingAnAnnouncement(t *testing.T) {
	ctx := t.Context()
	db := newCatalog(t)
	store := newStore(t, db, nil)

	id, err := store.Announce(ctx, presence.Announcement{
		Kind:          presence.KindExplore,
		RunID:         "run-1",
		Recipe:        "improves-babel",
		PreparationID: "prep-1",
		Authority:     run.Authority{Kind: run.AuthoritySerendipity, Ref: "draw-1"},
	})
	if err != nil || id == "" {
		t.Fatalf("Announce = (%q, %v)", id, err)
	}

	for _, tc := range []struct {
		name string
		set  string
	}{
		{"the host it ran on", `host_id = 'someoneelse'`},
		{"the deployment", `deployment_id = 'anotherfleet'`},
		{"what kind of thing it is", `kind = 'conductor'`},
		{"the run it is about", `run_id = 'run-2'`},
		{"the recipe it applied", `recipe = 'tunes-itself'`},
		{"the corpus it was scoped to", `preparation_id = 'prep-2'`},
		{"who authorized it", `authority_kind = 'operator'`},
		{"the authority's reference", `authority_ref = 'invitation:inv-9'`},
		{"when it started", `started_at = now() - interval '1 day'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx,
				`UPDATE presence SET `+tc.set+` WHERE presence_id = $1`, string(id))
			if err == nil {
				t.Errorf("PostgreSQL allowed an announcement to be rewritten: %s", tc.set)
			}
		})
	}

	// A backwards heartbeat is refused for a different reason and needs its
	// own case: a stale writer that could move heartbeat_at back would make
	// itself look fresher than a live one.
	if _, err := db.ExecContext(ctx,
		`UPDATE presence SET heartbeat_at = now() - interval '1 hour' WHERE presence_id = $1`,
		string(id)); err == nil {
		t.Error("PostgreSQL allowed heartbeat_at to move backwards")
	}
}

// Staleness is rendered, never reaped. A run whose process died leaves a row
// that says so by getting old, and that is strictly more information than the
// row's absence: "last seen 40m ago" tells an operator a machine stopped
// mid-run, while nothing at all is indistinguishable from a quiet fleet.
func TestStaleRowsAreClassifiedRatherThanDeleted(t *testing.T) {
	db := newCatalog(t)
	store := newStore(t, db, nil)

	plant(t, db, "prs_fresh", testHost, testDeployment,
		presence.KindExplore, presence.StateRunning, 5)
	plant(t, db, "prs_stale", testHost, testDeployment,
		presence.KindExplore, presence.StateRunning, presence.StaleAfter.Seconds()+60)
	plant(t, db, "prs_lost", testHost, testDeployment,
		presence.KindConductor, presence.StateRunning, presence.LostAfter.Seconds()+60)
	plant(t, db, "prs_ancient", testHost, testDeployment,
		presence.KindExplore, presence.StateRunning, presence.RetentionWindow.Seconds()+3600)

	rows := mustFleet(t, store)
	for _, tc := range []struct {
		id   presence.PresenceID
		want presence.Freshness
	}{
		{"prs_fresh", presence.FreshnessFresh},
		{"prs_stale", presence.FreshnessStale},
		{"prs_lost", presence.FreshnessLost},
	} {
		row := rowByID(t, rows, tc.id)
		if row.Freshness != tc.want {
			t.Errorf("%s classified %q, want %q (age %s)", tc.id, row.Freshness, tc.want, row.HeartbeatAge)
		}
		if row.State != presence.StateRunning {
			t.Errorf("%s state %q: a reader must not rewrite a row's own claim", tc.id, row.State)
		}
	}

	// Past the retention window the row leaves the view. It does not leave the
	// table: nothing in this package deletes, and the whole retention policy is
	// this one predicate.
	for _, row := range rows {
		if row.ID == "prs_ancient" {
			t.Error("a row past the retention window is still in the fleet view")
		}
	}
	if got := countRows(t, db); got != 4 {
		t.Errorf("the table holds %d rows, want 4: there is no reaper, by decision", got)
	}
}

// A fleet read is one deployment's, and one host's rows are marked as its own.
// Both are what make the view readable from any machine: an operator on a
// laptop asks about their fleet, sees every host, and can tell which line is
// the machine they are typing on.
func TestFleetIsDeploymentScopedAndMarksThisHost(t *testing.T) {
	db := newCatalog(t)
	store := newStore(t, db, nil)

	plant(t, db, "prs_mine", testHost, testDeployment,
		presence.KindExplore, presence.StateRunning, 1)
	plant(t, db, "prs_sibling", otherHost, testDeployment,
		presence.KindExplore, presence.StateRunning, 2)
	plant(t, db, "prs_elsewhere", testHost, "anotherfleet",
		presence.KindExplore, presence.StateRunning, 1)

	rows := mustFleet(t, store)
	if len(rows) != 2 {
		t.Fatalf("Fleet returned %d rows, want 2: another deployment's rows are not this fleet's", len(rows))
	}
	if !rowByID(t, rows, "prs_mine").Local {
		t.Error("this host's own row is not marked local")
	}
	if rowByID(t, rows, "prs_sibling").Local {
		t.Error("another host's row is marked local")
	}

	// The same table read as the sibling host inverts the marking, which is
	// what proves Local is derived from the reader rather than stored.
	sibling := newStoreFor(t, db, otherHost, nil)
	rows = mustFleet(t, sibling)
	if rowByID(t, rows, "prs_mine").Local {
		t.Error("reading as another host still marks the first host's row local")
	}
	if !rowByID(t, rows, "prs_sibling").Local {
		t.Error("reading as the sibling host does not mark its own row local")
	}
}

// Running rows come first, then the most recently heard from. A fleet view's
// first question is what is happening now, and a finished run that ended a
// minute ago must not push a live run down the page.
func TestFleetOrdersLiveWorkFirst(t *testing.T) {
	db := newCatalog(t)
	store := newStore(t, db, nil)

	plant(t, db, "prs_done_recent", testHost, testDeployment,
		presence.KindExplore, presence.StateFinished, 1)
	plant(t, db, "prs_live_old", testHost, testDeployment,
		presence.KindExplore, presence.StateRunning, 600)
	plant(t, db, "prs_live_new", testHost, testDeployment,
		presence.KindExplore, presence.StateRunning, 2)

	rows := mustFleet(t, store)
	var got []string
	for _, row := range rows {
		got = append(got, string(row.ID))
	}
	want := []string{"prs_live_new", "prs_live_old", "prs_done_recent"}
	if len(got) != len(want) {
		t.Fatalf("Fleet returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Fleet order = %v, want %v", got, want)
		}
	}
}

// An unreachable catalog costs a run nothing. This is the property the whole
// package is built for, so it is proved against the real failure rather than a
// fake: the connection is closed under the store, which is what a database
// going away looks like from here.
func TestAnUnreachableCatalogNeverFailsARun(t *testing.T) {
	ctx := t.Context()
	db := newCatalog(t)
	sink := &diagSink{}
	store := newStore(t, db, sink)

	// A row that exists, so the later calls have something they would have
	// changed if they could.
	id, err := store.Announce(ctx, presence.Announcement{
		Kind: presence.KindExplore, RunID: "run-1",
	})
	if err != nil || id == "" {
		t.Fatalf("Announce = (%q, %v)", id, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the catalog: %v", err)
	}

	blind, err := store.Announce(ctx, presence.Announcement{
		Kind: presence.KindExplore, RunID: "run-2",
	})
	if err != nil {
		t.Errorf("Announce against a dead catalog returned an error: %v", err)
	}
	if blind != "" {
		t.Errorf("Announce returned id %q against a dead catalog; a later heartbeat would keep failing on it", blind)
	}
	if err := store.Heartbeat(ctx, id); err != nil {
		t.Errorf("Heartbeat against a dead catalog returned an error: %v", err)
	}
	if err := store.Finalize(ctx, id, presence.Outcome{State: presence.StateFinished}); err != nil {
		t.Errorf("Finalize against a dead catalog returned an error: %v", err)
	}
	if len(sink.errs) != 3 {
		t.Errorf("the sink saw %d diagnostics, want 3: swallowed silently is not the contract", len(sink.errs))
	}

	// A read is the one thing that does report. Answering "nothing is running"
	// when the truth is "PostgreSQL is down" is the single dishonest thing this
	// package could do.
	if _, err := store.Fleet(ctx); err == nil {
		t.Error("Fleet reported an empty fleet against a dead catalog")
	}
}

// A cancelled run is exactly the case presence must report, so Finalize
// deliberately outlives its caller's context. Without this the row would go
// stale after a Ctrl-C and read as a machine that died, when what happened is
// that a person stopped a run and everything it committed is durable.
func TestFinalizeSurvivesACancelledContext(t *testing.T) {
	db := newCatalog(t)
	sink := &diagSink{}
	store := newStore(t, db, sink)

	ctx, cancel := context.WithCancel(t.Context())
	id, err := store.Announce(ctx, presence.Announcement{
		Kind: presence.KindExplore, RunID: "run-1",
	})
	if err != nil || id == "" {
		t.Fatalf("Announce = (%q, %v)", id, err)
	}
	cancel()

	if err := store.Finalize(ctx, id, presence.Outcome{State: presence.StateCancelled}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	row := rowByID(t, mustFleet(t, store), id)
	if row.State != presence.StateCancelled {
		t.Errorf("state %q after finalizing on a cancelled context, want %q; diagnostics: %s",
			row.State, presence.StateCancelled, sink.joined())
	}
}

// The presence table's columns are the announced fields and nothing else. A
// future `reason`, `note`, `message` or `error` column would be content, would
// be readable by the managed provider and by anyone holding the catalog
// credential, and is exactly what sealing a payload into an object prevents.
//
// internal/sharedcatalog's allowlist is the other gate on this, and two gates is
// deliberate: a column could be added there under a class a Phase A widening
// already admits, and this one is written from the writer's side, where the
// question is whether the field is something a run says about itself or
// something a run says about the corpus.
func TestPresenceHasNoColumnForContent(t *testing.T) {
	db := newCatalog(t)
	rows, err := db.QueryContext(t.Context(),
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = 'presence'`)
	if err != nil {
		t.Fatalf("reflect the presence table: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reflect the presence table: %v", err)
	}
	want := []string{
		"authority_kind", "authority_ref", "deployment_id", "finished_at",
		"heartbeat_at", "host_id", "kind", "preparation_id", "presence_id",
		"receipt_record_id", "recipe", "run_id", "started_at", "state",
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the presence table's columns changed.\n  want: %v\n  got:  %v\n"+
			"Adding a column here is a decision about the plaintext boundary: a run's own "+
			"identifiers and vocabularies are admitted, and its words are not.", want, got)
	}
}

func mustFleet(t *testing.T, store *presence.Store) []presence.Row {
	t.Helper()
	rows, err := store.Fleet(t.Context())
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	return rows
}
