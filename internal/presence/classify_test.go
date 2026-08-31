package presence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/presence"
)

// Presence is advisory, and Classify is where that becomes a property of the
// code rather than a claim in a comment. Every threshold is asserted at its
// boundary, because the boundary is what a renderer's wording turns on: "fresh"
// invites a reader to believe the run is alive, and the whole point of the
// thresholds is that Babel stops saying that well before it stops being true.
func TestClassifyAtEveryThreshold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state presence.State
		age   time.Duration
		want  presence.Freshness
	}{
		{"just announced", presence.StateRunning, 0, presence.FreshnessFresh},
		{"one heartbeat missed", presence.StateRunning, presence.HeartbeatInterval, presence.FreshnessFresh},
		{"a moment before stale", presence.StateRunning, presence.StaleAfter - time.Nanosecond, presence.FreshnessFresh},
		{"exactly stale", presence.StateRunning, presence.StaleAfter, presence.FreshnessStale},
		{"a moment before lost", presence.StateRunning, presence.LostAfter - time.Nanosecond, presence.FreshnessStale},
		{"exactly lost", presence.StateRunning, presence.LostAfter, presence.FreshnessLost},
		{"long lost", presence.StateRunning, 100 * presence.LostAfter, presence.FreshnessLost},

		// A terminal row's age says how long ago it ended and nothing about
		// liveness, so no age may turn it into a doubt about a process that
		// already reported how it stopped.
		{"finished just now", presence.StateFinished, 0, presence.FreshnessFinished},
		{"finished long ago", presence.StateFinished, 100 * presence.LostAfter, presence.FreshnessFinished},
		{"failed long ago", presence.StateFailed, 100 * presence.LostAfter, presence.FreshnessFinished},
		{"cancelled long ago", presence.StateCancelled, 100 * presence.LostAfter, presence.FreshnessFinished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := presence.Classify(tc.state, tc.age); got != tc.want {
				t.Errorf("Classify(%s, %s) = %s, want %s", tc.state, tc.age, got, tc.want)
			}
		})
	}
}

// The thresholds are ordered, and a reordering would be silent: a StaleAfter
// larger than LostAfter would make "lost" unreachable and every dead row read
// as merely stale, which is the classification a renderer says "probably still
// working" about.
func TestThresholdsAreOrdered(t *testing.T) {
	if !(presence.HeartbeatInterval < presence.StaleAfter) {
		t.Errorf("HeartbeatInterval %s is not below StaleAfter %s: a healthy run would be born stale",
			presence.HeartbeatInterval, presence.StaleAfter)
	}
	if !(presence.StaleAfter < presence.LostAfter) {
		t.Errorf("StaleAfter %s is not below LostAfter %s: one of the two classifications is unreachable",
			presence.StaleAfter, presence.LostAfter)
	}
	if !(presence.LostAfter < presence.RetentionWindow) {
		t.Errorf("LostAfter %s is not below RetentionWindow %s: a row would leave the fleet view before it could be reported lost",
			presence.LostAfter, presence.RetentionWindow)
	}
}

// A machine with no fleet holds a nil store, and every method of it must be a
// no-op that reports success. This is what "the feature quietly absent" has to
// mean in code: not a branch at every call site, but a value that does nothing.
func TestNilStoreIsSilentlyAbsent(t *testing.T) {
	ctx := context.Background()
	var store *presence.Store

	id, err := store.Announce(ctx, presence.Announcement{
		Kind: presence.KindExplore, RunID: "run-1",
	})
	if err != nil || id != "" {
		t.Errorf("nil store Announce = (%q, %v), want (\"\", nil)", id, err)
	}
	if err := store.Heartbeat(ctx, "prs_whatever"); err != nil {
		t.Errorf("nil store Heartbeat = %v, want nil", err)
	}
	if err := store.Finalize(ctx, "prs_whatever",
		presence.Outcome{State: presence.StateFinished}); err != nil {
		t.Errorf("nil store Finalize = %v, want nil", err)
	}
	rows, err := store.Fleet(ctx)
	if err != nil || rows != nil {
		t.Errorf("nil store Fleet = (%v, %v), want (nil, nil)", rows, err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("nil store Close = %v, want nil", err)
	}
	if got := store.HostID(); got != "" {
		t.Errorf("nil store HostID = %q, want empty", got)
	}
}

// The empty id is the mechanism the best-effort contract rests on: an announce
// that did not land returns one, and every later call must then do nothing
// without the caller having to check. A store that dialled the database for an
// empty id would turn one unreachable moment into a failure at every heartbeat.
func TestEmptyIDIsANoOpWithoutTouchingTheDatabase(t *testing.T) {
	ctx := context.Background()
	// A store over a nil-safe but unusable handle: any statement would panic
	// or fail, so reaching the database at all is what this test detects.
	store, err := presence.New(presence.Options{
		DB: mustClosedDB(t), DeploymentID: testDeployment, HostID: testHost,
		Diag: func(error) { t.Error("an empty id reached the database") },
	})
	if err != nil {
		t.Fatalf("presence.New: %v", err)
	}
	if err := store.Heartbeat(ctx, ""); err != nil {
		t.Errorf("Heartbeat(\"\") = %v, want nil", err)
	}
	if err := store.Finalize(ctx, "", presence.Outcome{State: presence.StateFinished}); err != nil {
		t.Errorf("Finalize(\"\") = %v, want nil", err)
	}
}

// New refuses what would produce an unreadable row. A row scoped to no
// deployment is invisible to every fleet read, and a row naming no host is
// presence with no answer to "where", so both are configuration errors worth
// reporting at construction rather than silently writing rows nobody can find.
func TestNewRefusesAnUnreadableIdentity(t *testing.T) {
	db := mustClosedDB(t)
	for _, tc := range []struct {
		name string
		opt  presence.Options
	}{
		{"no connection", presence.Options{DeploymentID: testDeployment, HostID: testHost}},
		{"no deployment", presence.Options{DB: db, HostID: testHost}},
		{"no host", presence.Options{DB: db, DeploymentID: testDeployment}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := presence.New(tc.opt); err == nil {
				t.Error("accepted an identity that produces rows nobody can read")
			}
		})
	}
}

// A caller bug is refused and reported; it is not written. The vocabularies are
// closed in the database too, so an unknown kind would fail at the CHECK - but
// failing there would spend a round trip and land in the diagnostic stream as an
// unreadable constraint violation instead of a sentence naming the mistake.
func TestAnnounceRefusesWhatCannotBeAnnounced(t *testing.T) {
	ctx := context.Background()
	sink := &diagSink{}
	store, err := presence.New(presence.Options{
		DB: mustClosedDB(t), DeploymentID: testDeployment, HostID: testHost,
		Diag: sink.report,
	})
	if err != nil {
		t.Fatalf("presence.New: %v", err)
	}
	for _, tc := range []struct {
		name string
		ann  presence.Announcement
	}{
		{"no kind", presence.Announcement{RunID: "run-1"}},
		{"a kind this build does not implement", presence.Announcement{Kind: "daemon", RunID: "run-1"}},
		{"no run id", presence.Announcement{Kind: presence.KindExplore}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := store.Announce(ctx, tc.ann)
			if err == nil {
				t.Fatal("accepted an announcement this build cannot represent")
			}
			if !errors.Is(err, presence.ErrInvalid) {
				t.Errorf("error %v does not match ErrInvalid, so a caller cannot tell it from an outage", err)
			}
			if id != "" {
				t.Errorf("returned id %q for a refused announcement", id)
			}
		})
	}
	if len(sink.errs) != 3 {
		t.Errorf("the sink saw %d diagnostics, want 3: a refused announcement is still worth telling somebody about", len(sink.errs))
	}
}

// Finalizing as running is reporting that the work both ended and did not.
func TestFinalizeRefusesANonTerminalState(t *testing.T) {
	store, err := presence.New(presence.Options{
		DB: mustClosedDB(t), DeploymentID: testDeployment, HostID: testHost,
	})
	if err != nil {
		t.Fatalf("presence.New: %v", err)
	}
	err = store.Finalize(context.Background(), "prs_something",
		presence.Outcome{State: presence.StateRunning})
	if !errors.Is(err, presence.ErrInvalid) {
		t.Errorf("Finalize with a running state = %v, want ErrInvalid", err)
	}
}

// Beat is nil-safe in both directions, and stopping it twice is not a panic.
// Both matter because it is deferred at every wiring site: a stop that could
// panic on a second call would take down a run that had already finished.
func TestBeatIsNilSafeAndStopsIdempotently(t *testing.T) {
	ctx := context.Background()

	stop := presence.Beat(ctx, nil, "prs_1")
	stop()
	stop()

	fake := &fakeAnnouncer{}
	stop = presence.Beat(ctx, fake, "")
	stop()
	stop()
	if fake.heartbeats != 0 {
		t.Errorf("an empty id produced %d heartbeats, want 0", fake.heartbeats)
	}

	stop = presence.Beat(ctx, fake, "prs_1")
	stop()
	stop()
}

// A cancelled context ends the loop rather than leaving a goroutine
// heartbeating for a run that is over. Stop still returns, which is what a
// deferred call depends on.
func TestBeatStopsWhenTheRunsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := presence.Beat(ctx, &fakeAnnouncer{}, "prs_1")
	cancel()
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the heartbeat loop did not exit after its context was cancelled")
	}
}
