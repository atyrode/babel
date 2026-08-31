package web

// The fleet presence read surface: what every machine in the deployment says it
// is running right now (SPEC.md §4.7, §9; issue #118).
//
// This is the one route on this server whose subject is neither durable nor
// local, and both halves of that shape everything below.
//
// Not durable. A presence row is an announcement a run made about itself into
// the shared PostgreSQL catalog, and internal/presence deliberately keeps it out
// of the object store: it is ephemeral status rather than truth, so it has no
// object-first leg and no local copy. Nothing on this route reads the frontier,
// the review log or a receipt body, and nothing here can write.
//
// Not local. Every row but this machine's arrived from a host this one does not
// control, so hostile content is the normal case: a recipe id, an authority
// reference and a run id are all strings another machine chose. They travel
// through the same writeJSON sanitizer every other route uses and are rendered
// as identified text.
//
// Three rules hold in this file, and they are the reason it is not simply
// fleet.go with a different noun.
//
// Presence is advisory, never fact. A row says a run was alive at its last
// heartbeat and says nothing whatsoever about now. So the response carries two
// things rather than one: the classification internal/presence made, and the raw
// heartbeat age it made it from. A client must be able to render "last seen 4m
// ago — running or dead, this host cannot tell", which is only possible if the
// age crosses the wire beside the word. A green dot derived from `freshness`
// alone would be this surface asserting liveness it did not observe.
//
// The classification is read, never derived. presence.Classify is the only place
// a duration becomes a word, and the age it classifies is computed by the
// catalog query rather than by this process — so no web-server clock enters the
// judgement, and a handler that compared timestamps would be a second answer to
// the question the package exists to answer once. The thresholds travel in the
// envelope for the same reason: a page that hard-coded two minutes would
// eventually disagree with the server that classified the row.
//
// "This host cannot tell" is an answer, not a failure. A machine in local mode
// has no presence table, an unreachable catalog cannot be asked, and neither is
// a broken request — so both answer HTTP 200 with `available: false` and a
// sentence, exactly as GET /api/overview's sections do. Refusing would make a
// working local-mode machine look broken, and would make a catalog outage
// indistinguishable from a bug in the page. It also mirrors the write side:
// internal/presence guarantees a presence failure never fails a run, and a
// presence failure must not fail a page either.

import (
	"context"
	"net/http"
	"time"

	"github.com/atyrode/babel/internal/presence"
)

// PresenceReader is the fleet-presence read surface the Fleet view renders.
//
// One method, and it is the whole authority this surface has. internal/presence
// also exposes an Announcer — Announce, Heartbeat, Finalize — and none of it is
// representable here: a presence row is written by the run it describes, and a
// browser GET that could announce would let a page claim a run this machine is
// not performing.
type PresenceReader interface {
	Fleet(ctx context.Context) ([]presence.Row, error)
}

// The real store satisfies it, asserted here rather than at the wiring site,
// for the reason types.go gives for the others: a reader method that changed
// shape is a compile failure in this package instead of a second
// implementation growing beside it.
var _ PresenceReader = (*presence.Store)(nil)

// presenceRow is one announced run as the Fleet view shows it.
//
// It carries identifiers, times and one classification. It carries no content
// at all — no statement, no note, no outcome prose — because internal/presence
// stores none: #112 makes presence rows metadata-only so they stay readable on a
// machine holding no payload keys, and a DTO with a text field would eventually
// be filled from somewhere.
type presenceRow struct {
	ID string `json:"id"`
	// Host is the machine's opaque host id, byte-identical to the one the
	// archive and the shared catalog use. It is deliberately not a display
	// name: internal/presence stores no second copy of host identity, and this
	// surface does not synthesize one — a client that wants a label joins the
	// host vocabulary GET /api/fleet/hosts already serves.
	Host string `json:"host"`
	// LocalHost marks this machine's own row. It is the store's answer rather
	// than a comparison made here, and it is what lets the operator find his
	// own runs in a fleet-wide list without reading identifiers.
	LocalHost bool `json:"local_host"`
	// Kind is "conductor" or "explore": a scheduled cycle, or a run somebody
	// asked for. Both announce, because "what is this fleet doing" is
	// unanswerable if half the work is invisible.
	Kind  string `json:"kind"`
	RunID string `json:"run_id"`
	// Recipe and PreparationID are absent rather than empty when the run
	// announced none — a conductor cycle that has not yet resolved an
	// assignment has no recipe, and printing one would invent it.
	Recipe        string `json:"recipe,omitempty"`
	PreparationID string `json:"preparation_id,omitempty"`
	// Authority is #96's vocabulary, unchanged: why this run is happening.
	// The zero value is an absence and is reported as one, on RunSummary's
	// terms — a row that named an authority nobody recorded would invent
	// provenance.
	Authority RunAuthority `json:"authority"`
	// State is the run's own last word about itself: running, finished, failed
	// or cancelled. It is a claim, not an observation, which is exactly why
	// Freshness exists beside it.
	State string `json:"state"`
	// The three times, UTC RFC3339, absent when zero. FinishedAt is absent
	// for a row that is still running, and printing year one would make an
	// in-flight run look finished at the dawn of time.
	StartedAt   string `json:"started_at,omitempty"`
	HeartbeatAt string `json:"heartbeat_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	// HeartbeatAgeSeconds is how long ago the row was last touched, computed
	// by the catalog rather than by this process. It is the honest number the
	// interface renders as prose, and it travels even when Freshness reads
	// "fresh": a reader has to be able to say how long ago, not merely which
	// bucket.
	HeartbeatAgeSeconds int64 `json:"heartbeat_age_seconds"`
	// Freshness is presence.Classify's word for that age: fresh, stale, lost,
	// or finished. It is the package's answer verbatim, never re-derived here.
	Freshness string `json:"freshness"`
	// ReceiptRecordID is the shared-catalog record the run committed when it
	// finalized, empty until then. It is where a finished row stops being
	// presence and becomes durable analysis, which is the one link out of this
	// surface worth following.
	ReceiptRecordID string `json:"receipt_record_id,omitempty"`
}

// presenceFleet is GET /api/fleet/presence.
type presenceFleet struct {
	overviewSection
	// Configured distinguishes the two unavailable cases, which call for
	// different actions. A machine in local mode has no presence table and
	// never will until shared mode is configured; a configured machine whose
	// catalog did not answer needs the catalog looked at. Collapsing them into
	// one flag would tell an operator whose PostgreSQL is down to configure
	// something he configured months ago.
	Configured bool          `json:"configured"`
	Rows        []presenceRow `json:"rows"`
	// Running counts the rows whose own state is "running", whatever their
	// freshness. It is a count of claims rather than of live processes, and the
	// caption the interface renders says so.
	Running int `json:"running"`
	// The thresholds presence.Classify judged by, in seconds, so the sentence a
	// page renders is the sentence the server classified by. A client that
	// carried its own copy would eventually contradict the badge beside it.
	StaleAfterSeconds int64 `json:"stale_after_seconds"`
	LostAfterSeconds  int64 `json:"lost_after_seconds"`
	// RetentionSeconds is how far back the read reaches. A finished run older
	// than this is simply not here, and a page that did not know the window
	// would render an empty list as an idle fleet.
	RetentionSeconds int64 `json:"retention_seconds"`
}

// The three things this route can say instead of rows. They are three sentences
// rather than one because they call for three different actions, and a single
// "presence is unavailable" would send an operator whose PostgreSQL is down to
// configure something he configured months ago.
const (
	// presenceAbsent is a machine with no presence table at all: there is no
	// fleet to be present in, which is configuration rather than a fault.
	presenceAbsent = "this machine is in local mode, so no host announces presence to it; " +
		"configure shared mode to see what the fleet is running"

	// presenceUnreachable is a configured machine whose catalog never answered.
	// It names the catalog rather than the failure, because the catalog is what
	// an operator can check, and it says what the silence does and does not
	// mean: the fleet may be perfectly busy and this machine cannot see it.
	presenceUnreachable = "the shared catalog could not be reached, so this machine cannot see " +
		"what the fleet is running; runs elsewhere are unaffected and their receipts still commit"

	// presenceRefused is a catalog that answered and said no. That is a
	// different fact from an outage and resolves differently: a presence table
	// a migration has not reached, or a catalog role this machine's credential
	// is not permitted to read it with. It is stated as its own sentence
	// because an outage fixes itself and a misconfiguration does not.
	presenceRefused = "the shared catalog refused this read, so this machine cannot see what " +
		"the fleet is running; the presence table may be missing or unreadable by this " +
		"machine's catalog credential"
)

// handleFleetPresence serves every run the deployment has announced inside the
// retention window, in the reader's own order.
//
// The order is kept rather than re-derived: internal/presence returns running
// rows before finished ones and newest heartbeat first, which is the order the
// question is asked in, and a second sort here would be a second answer to it.
//
// Every path answers HTTP 200, including the failing ones, and that is this
// route's own judgement rather than an inherited convention. The status code
// answers whether the request was well formed, and it always was; whether this
// host can see the fleet is what the body is for. It is also the read-side
// mirror of the guarantee internal/presence makes on the write side — a presence
// failure never fails a run — and a 500 would take away the one surface that
// exists to say "this host cannot tell", replacing an informative answer with
// a banner that reads as a bug in the page.
func (s *Server) handleFleetPresence(w http.ResponseWriter, r *http.Request) {
	result := presenceFleet{
		Rows:              []presenceRow{},
		StaleAfterSeconds: int64(presence.StaleAfter / time.Second),
		LostAfterSeconds:  int64(presence.LostAfter / time.Second),
		RetentionSeconds:  int64(presence.RetentionWindow / time.Second),
	}
	if s.opts.Presence == nil {
		result.overviewSection = sectionMissing(presenceAbsent)
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	rows, err := s.opts.Presence.Fleet(r.Context())
	if err != nil {
		// Nothing from the error's own text reaches the client, and nothing
		// reaches the diagnostics stream either, for serviceError's reason: a
		// wrapped catalog error can carry a connection string. The predicates
		// are internal/presence's own, so the three cases are told apart by the
		// package that produced them rather than by matching on message text.
		switch {
		case presence.NotConfigured(err):
			result.overviewSection = sectionMissing(presenceAbsent)
		case presence.Unreachable(err):
			result.Configured = true
			result.overviewSection = sectionMissing(presenceUnreachable)
			s.logf("fleet presence: the shared catalog could not be reached")
		default:
			result.Configured = true
			result.overviewSection = sectionMissing(presenceRefused)
			s.logf("fleet presence: the shared catalog refused the read")
		}
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	result.Configured = true
	result.overviewSection = sectionReady()
	for _, row := range rows {
		if row.State == presence.StateRunning {
			result.Running++
		}
		result.Rows = append(result.Rows, viewPresenceRow(row))
	}
	s.writeJSON(w, http.StatusOK, result)
}

// viewPresenceRow renders one announced run.
//
// Freshness is the row's own field and the age is the row's own field; neither
// is computed here. That is the whole point of the two travelling together from
// the catalog: the age was measured by the machine that holds the rows, so a web
// server whose clock is wrong cannot turn a live run into a lost one.
func viewPresenceRow(row presence.Row) presenceRow {
	return presenceRow{
		ID:            string(row.ID),
		Host:          row.Host,
		LocalHost:     row.Local,
		Kind:          string(row.Kind),
		RunID:         row.RunID,
		Recipe:        row.Recipe,
		PreparationID: row.PreparationID,
		Authority: RunAuthority{
			Kind: string(row.Authority.Kind),
			Ref:  row.Authority.Ref,
		},
		State:               string(row.State),
		StartedAt:           timeText(row.StartedAt),
		HeartbeatAt:         timeText(row.HeartbeatAt),
		FinishedAt:          timeText(row.FinishedAt),
		HeartbeatAgeSeconds: ageSeconds(row.HeartbeatAge),
		Freshness:           string(row.Freshness),
		ReceiptRecordID:     row.ReceiptRecordID,
	}
}

// ageSeconds renders a heartbeat age for the wire, and refuses to render a
// negative one.
//
// A negative age is reachable: the catalog subtracts a timestamp another machine
// wrote from its own clock, and a writer whose clock runs ahead produces a
// heartbeat in the catalog's future. internal/fleet's snapshot ages resolve the
// same skew the same way — the future reads as zero — because "last seen -4m
// ago" is not a fact about anything, where "just now" at least states what the
// row's own claim amounts to. The classification is unaffected: it was made
// against the same duration by the package that owns the thresholds.
func ageSeconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}
