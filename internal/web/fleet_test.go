package web

// The fleet read surface's own properties (issue #109 item 4).
//
// The reader is a fake here, and that is deliberate rather than a shortcut. A
// real *fleet.Reader needs PostgreSQL, an object store and a keyring, none of
// which this package may open; what these tests are about is the rendering
// contract on this side of that seam — that an absent host stays absent, that a
// staged record stays visibly staged, that a record this instance cannot open
// says so, and that a machine with no shared backend answers honestly instead
// of failing. Every one of those is a property of the handler, and a fake is
// what makes each of them constructible.
//
// internal/fleet owns the resolution the fake stands in for, and its own tests
// own it: the four-case sync resolution, the host attribution join, and the
// per-record open failure are checked against a real catalog there.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The fixture's machines. localFleetHost is the one the reader sits on,
// remoteFleetHost is another that has committed, and the third group — records
// whose origin instance registered no host — has no id by definition and is
// therefore not a constant.
const (
	localFleetHost  = "host-under-test"
	remoteFleetHost = "host-elsewhere"
)

// fakeFleet is a wired FleetReader over fixed records.
//
// It remembers the filter each read received, because half of what these tests
// check is that a query parameter reached the reader rather than merely that
// the response looked plausible: a `?host=` the handler dropped would produce a
// listing that is wrong in a way no assertion about its rendered rows catches.
type fakeFleet struct {
	local   string
	records []fleet.Record
	hosts   []sharedcatalog.RecordHost
	// states answers SyncStates for the ids a test names; every other id
	// resolves to fleet.SyncLocal, which is what the real reader answers for a
	// record no remote row and no journal describes.
	states map[string]string
	// fail is returned by every read when set, so the difference between "this
	// deployment has no fleet" and "this deployment's fleet did not answer" is
	// observable from the outside.
	fail error

	recordFilters []sharedcatalog.RecordFilter
	hostFilters   []sharedcatalog.RecordFilter
	syncIDs       []string
	// journal is whatever the handler passed through, so the seam being
	// carried rather than dropped is observable.
	journal fleet.SyncJournal
}

func (f *fakeFleet) LocalHost() string { return f.local }

func (f *fakeFleet) Records(_ context.Context,
	filter sharedcatalog.RecordFilter) ([]fleet.Record, error) {
	f.recordFilters = append(f.recordFilters, filter)
	if f.fail != nil {
		return nil, f.fail
	}
	return f.selected(filter), nil
}

// RecordsWithContent returns the same rows. The fixtures carry their content
// already: what distinguishes the two calls in the real reader is whether it
// spends an object fetch per row, and this fake spends none either way.
func (f *fakeFleet) RecordsWithContent(_ context.Context,
	filter sharedcatalog.RecordFilter) ([]fleet.Record, error) {
	f.recordFilters = append(f.recordFilters, filter)
	if f.fail != nil {
		return nil, f.fail
	}
	return f.selected(filter), nil
}

func (f *fakeFleet) Hosts(_ context.Context,
	filter sharedcatalog.RecordFilter) ([]sharedcatalog.RecordHost, error) {
	f.hostFilters = append(f.hostFilters, filter)
	if f.fail != nil {
		return nil, f.fail
	}
	return f.hosts, nil
}

func (f *fakeFleet) SyncStates(_ context.Context, journal fleet.SyncJournal,
	ids []string) (map[string]string, error) {
	f.journal = journal
	f.syncIDs = append(f.syncIDs, ids...)
	if f.fail != nil {
		return nil, f.fail
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if state, found := f.states[id]; found {
			out[id] = state
			continue
		}
		out[id] = fleet.SyncLocal
	}
	return out, nil
}

// selected applies the narrowing the handlers rely on: the catalog's semantics
// reduced to what these fixtures exercise — any-of on hosts and kinds,
// committed-only unless staged output was admitted, then the page.
func (f *fakeFleet) selected(filter sharedcatalog.RecordFilter) []fleet.Record {
	out := make([]fleet.Record, 0, len(f.records))
	for _, record := range f.records {
		if len(filter.Hosts) > 0 && !contains(filter.Hosts, record.HostID) {
			continue
		}
		if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, record.Record.Kind) {
			continue
		}
		if len(filter.RunIDs) > 0 && !contains(filter.RunIDs, record.Record.RunID) {
			continue
		}
		if !filter.IncludePending && record.SyncState != sharedcatalog.SyncCommitted {
			continue
		}
		out = append(out, record)
	}
	if filter.Offset > 0 {
		out = out[min(filter.Offset, len(out)):]
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out
}

func containsKind(kinds []sharedcatalog.RecordKind, want sharedcatalog.RecordKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// fleetFixture is the deployment every phaseB server reads through: two hosts
// that have committed, one group of records with no host attribution at all,
// one record still staged, one this instance cannot open, and one kind that has
// no searchable summary by construction.
//
// text is woven into every readable string for the reason newPhaseB weaves it
// into the frontier: the malicious-content sweep is then the ordinary fixture
// rather than a second one that can drift from it.
func fleetFixture(text string) *fakeFleet {
	committed := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	older := committed.Add(-48 * time.Hour)
	return &fakeFleet{
		local:  localFleetHost,
		states: map[string]string{},
		records: []fleet.Record{
			// This machine's own committed candidate.
			fixtureCandidate("frec-local", "frun-local", localFleetHost,
				"this workstation "+text, "inst-local", &committed, text),
			// Another machine's committed candidate.
			fixtureCandidate("frec-remote", "frun-remote", remoteFleetHost,
				"the other laptop "+text, "inst-remote", &committed, text),
			// A record whose origin instance registered before hosts were
			// recorded: attributed to nobody, and reported as such.
			fixtureCandidate("frec-unattributed", "frun-legacy", "", "",
				"inst-legacy", &older, text),
			// Another host's staged output: not globally reviewable yet.
			fixtureCandidate("frec-pending", "frun-pending", remoteFleetHost,
				"the other laptop "+text, "inst-remote", nil, text),
			// A record this instance holds no key for: a row with a reason and
			// no content, which is what an operator can act on.
			fixtureUnopened("frec-sealed", "frun-remote", remoteFleetHost,
				"the other laptop "+text, "inst-remote", &committed,
				"sealed under key "+text+", which this instance does not hold"),
			// The other host's consolidation, its recorded decision, and a
			// proposal — the kinds the findings list and the review inbox merge.
			// The proposal is the awkward one: it commits, it is the review
			// inbox's main subject, and frontier.Output refuses it as
			// unsearchable, so its row has to render without a summary.
			fixtureFinding("frec-remote-finding", "frun-remote", remoteFleetHost,
				"the other laptop "+text, "inst-remote", &committed, text),
			fixtureDecision("frec-remote-decision", "frun-remote", remoteFleetHost,
				"the other laptop "+text, "inst-remote", &committed, text),
			fixtureProposal("frec-remote-proposal", "frun-remote", remoteFleetHost,
				"the other laptop "+text, "inst-remote", &committed, text),
		},
		hosts: []sharedcatalog.RecordHost{
			{
				HostID: remoteFleetHost, DisplayName: "the other laptop " + text,
				Records: 5, NewestCommit: &committed, Pending: 1,
			},
			{
				HostID: localFleetHost, DisplayName: "this workstation " + text,
				Records: 1, NewestCommit: &committed,
			},
			// The unattributed group is offered as an option rather than
			// hidden, so an operator can reach the records it holds.
			{HostID: "", Records: 1, NewestCommit: &older},
		},
	}
}

// fixtureRow is the plaintext half of a fixture record. A nil commit time is a
// staged run, which is the only way this side of the seam can produce one.
func fixtureRow(id, runID, hostID, display, instance string,
	kind sharedcatalog.RecordKind, committedAt *time.Time) sharedcatalog.FleetRecord {
	state := sharedcatalog.SyncCommitted
	if committedAt == nil {
		state = sharedcatalog.SyncPending
	}
	return sharedcatalog.FleetRecord{
		Record: sharedcatalog.AnalysisRecordRow{
			RecordID: id, RunID: runID, Kind: kind, Schema: frontier.RecordSchema,
			CreatedAt: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
		},
		HostID: hostID, HostDisplayName: display, OriginInstanceID: instance,
		SyncState: state, CommittedAt: committedAt,
	}
}

func fixtureCandidate(id, runID, hostID, display, instance string,
	committedAt *time.Time, text string) fleet.Record {
	return fleet.Record{
		FleetRecord: fixtureRow(id, runID, hostID, display, instance,
			sharedcatalog.KindHypothesis, committedAt),
		Published: &frontier.PublishedRecord{
			Schema: frontier.RecordSchema, Kind: frontier.PublishedHypothesis,
			ID: id, RootID: id, RunID: runID, Status: frontier.StatusInvestigating,
			CreatedAt: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
			Payload: mustMarshalPayload(frontier.HypothesisPayload{
				Statement: text, Novelty: 0.4, Priority: 0.4,
			}),
		},
	}
}

func fixtureUnopened(id, runID, hostID, display, instance string,
	committedAt *time.Time, reason string) fleet.Record {
	return fleet.Record{
		FleetRecord: fixtureRow(id, runID, hostID, display, instance,
			sharedcatalog.KindHypothesis, committedAt),
		Unopened: reason,
	}
}

func fixtureFinding(id, runID, hostID, display, instance string,
	committedAt *time.Time, text string) fleet.Record {
	return fleet.Record{
		FleetRecord: fixtureRow(id, runID, hostID, display, instance,
			sharedcatalog.KindFinding, committedAt),
		Published: &frontier.PublishedRecord{
			Schema: frontier.RecordSchema, Kind: frontier.PublishedFinding,
			ID: id, RootID: id, RunID: runID,
			CreatedAt: time.Date(2026, 3, 1, 8, 30, 0, 0, time.UTC),
			Payload: mustMarshalPayload(frontier.FindingPayload{
				Title:   text,
				Pattern: "the same generated pattern recurs across the synthetic corpus",
			}),
		},
	}
}

func fixtureDecision(id, runID, hostID, display, instance string,
	committedAt *time.Time, text string) fleet.Record {
	return fleet.Record{
		FleetRecord: fixtureRow(id, runID, hostID, display, instance,
			sharedcatalog.KindDisposition, committedAt),
		Published: &frontier.PublishedRecord{
			Schema: frontier.RecordSchema, Kind: frontier.PublishedReviewAnswer,
			ID: id, RootID: id, RunID: runID,
			Subject: frontier.Ref{Type: frontier.EntityHypothesis, ID: "frec-remote"},
			Answer: &frontier.PublishedAnswer{
				Decision: frontier.DispositionReject, Reviewer: "reviewer " + text,
			},
			CreatedAt: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC),
			Payload:   mustMarshalPayload(frontier.DispositionPayload{Note: text}),
		},
	}
}

// fixtureProposal is the kind with no searchable output. Its payload is real —
// this instance opened and decoded it — and frontier.Output still refuses it,
// which is the case the review inbox has to render as a row without a summary
// rather than drop.
func fixtureProposal(id, runID, hostID, display, instance string,
	committedAt *time.Time, text string) fleet.Record {
	return fleet.Record{
		FleetRecord: fixtureRow(id, runID, hostID, display, instance,
			sharedcatalog.KindProposal, committedAt),
		Published: &frontier.PublishedRecord{
			Schema: frontier.RecordSchema, Kind: frontier.PublishedProposal,
			ID: id, RootID: id, RunID: runID,
			CreatedAt: time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC),
			Payload:   mustMarshalPayload(map[string]string{"summary": text}),
		},
	}
}

func mustMarshalPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// fleetOf reaches the wired fake, so a test can name the sync state of a record
// whose identifier the fixture only learns at run time.
func (h *phaseB) fleetOf() *fakeFleet {
	h.t.Helper()
	fake, ok := h.server.opts.Fleet.(*fakeFleet)
	if !ok {
		h.t.Fatalf("the wired fleet reader is %T, not the fixture", h.server.opts.Fleet)
	}
	return fake
}

// TestFleetRoutesAnswerWithoutASharedBackend is the load-bearing one: a machine
// in local mode has no fleet, and both routes must say so with a well-formed
// empty document and HTTP 200.
//
// The failure this prevents is a browser that presents a working local-mode
// machine as a broken one. A 409 or a 500 here would put a red banner on a page
// whose only real news is that this deployment has one host, and the honest
// notice the interface shows instead is only possible if the response is honest
// first.
func TestFleetRoutesAnswerWithoutASharedBackend(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Fleet = nil })

	response := h.get("/api/fleet/records")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("records status = %d, want 200", response.StatusCode)
	}
	var records fleetRecordList
	decodeResponse(t, response, &records)
	if records.Configured {
		t.Error("records reports a configured fleet with no reader wired")
	}
	if records.Items == nil || len(records.Items) != 0 {
		t.Errorf("records items = %#v, want an empty list", records.Items)
	}
	if records.Hosts == nil || len(records.Hosts) != 0 {
		t.Errorf("records hosts = %#v, want an empty list", records.Hosts)
	}

	response = h.get("/api/fleet/hosts")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("hosts status = %d, want 200", response.StatusCode)
	}
	var hosts fleetHostList
	decodeResponse(t, response, &hosts)
	if hosts.Configured {
		t.Error("hosts reports a configured fleet with no reader wired")
	}
	if hosts.LocalHost != "" {
		t.Errorf("hosts local_host = %q, want empty: local mode registers no host", hosts.LocalHost)
	}
	if hosts.Hosts == nil || len(hosts.Hosts) != 0 {
		t.Errorf("hosts = %#v, want an empty list", hosts.Hosts)
	}

	// The listings still render, with the one sync state a machine that
	// publishes nowhere can honestly report.
	var queue queueResult
	decodeResponse(t, h.get("/api/review/queue?status=all"), &queue)
	if len(queue.Items) == 0 {
		t.Fatal("the review queue is empty in local mode")
	}
	for _, item := range queue.Items {
		if item.Sync != fleet.SyncLocal {
			t.Errorf("local-mode queue row %s sync = %q, want %q",
				item.Subject.ID, item.Sync, fleet.SyncLocal)
		}
		if item.Host != "" || item.HostAttributed {
			t.Errorf("local-mode queue row %s claims host %q (attributed %v)",
				item.Subject.ID, item.Host, item.HostAttributed)
		}
		if !item.LocalHost {
			t.Errorf("local-mode queue row %s is not marked as this machine's", item.Subject.ID)
		}
	}
}

// TestFleetReadFailureIsNotReportedAsAnAbsentFleet keeps the two apart.
//
// "No shared backend is configured" and "your shared backend did not answer"
// call for opposite actions, and the first is what a `configured: false` body
// tells the operator. A catalog that is down must therefore never produce one:
// on the fleet routes it produces an error, because there the fleet is the
// payload and the request genuinely cannot be answered.
func TestFleetReadFailureIsNotReportedAsAnAbsentFleet(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	h.fleetOf().fail = errors.New("dial tcp 10.0.0.9:5432: connect: connection refused")

	for _, path := range []string{
		"/api/fleet/records",
		"/api/fleet/hosts",
		// The merged listings too: with ?fleet=1 the other hosts' records are
		// what was asked for, so a catalog that cannot supply them has not
		// answered the request.
		"/api/hypotheses?fleet=1",
		"/api/findings?fleet=1",
		"/api/review/queue?status=all&fleet=1",
	} {
		response := h.get(path)
		text := body(t, response)
		if response.StatusCode == http.StatusOK {
			t.Errorf("GET %s answered 200 with a catalog that is down: %s", path, text)
		}
		if strings.Contains(text, `"configured":false`) {
			t.Errorf("GET %s reported an unreachable catalog as an unconfigured fleet: %s", path, text)
		}
		// The refusal names the catalog, and quotes nothing of the error: a
		// wrapped catalog error carries a connection string.
		if !strings.Contains(text, "shared catalog could not be read") {
			t.Errorf("GET %s refusal = %s", path, text)
		}
		if strings.Contains(text, "10.0.0.9") {
			t.Errorf("GET %s echoed the catalog address: %s", path, text)
		}
	}
}

// TestUnresolvableSyncStateDegradesRatherThanRefusing is the other half, and it
// is the one that matters more.
//
// This machine's durable store is local. A shared-catalog outage that stopped an
// operator reading his own hypotheses, findings and review inbox would make the
// fleet feature a liability rather than an addition, so the local listings
// render. What they must not do is answer the sync question anyway: an
// unresolved row is fleet.SyncUnknown, never "local" — nothing observed that
// nothing is carrying it — and the response says why so that "unknown" is a
// state rather than a shrug.
func TestUnresolvableSyncStateDegradesRatherThanRefusing(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	h.fleetOf().fail = errors.New("dial tcp 10.0.0.9:5432: connect: connection refused")

	var hypotheses hypothesisList
	decodeResponse(t, h.get("/api/hypotheses"), &hypotheses)
	if len(hypotheses.Items) == 0 {
		t.Fatal("the frontier listing lost its rows to another machine's outage")
	}
	if !hypotheses.SyncDegraded {
		t.Error("the frontier listing reports resolved sync state it could not resolve")
	}
	if !strings.Contains(hypotheses.SyncDetail, "shared catalog could not be reached") {
		t.Errorf("degraded detail = %q", hypotheses.SyncDetail)
	}
	if strings.Contains(hypotheses.SyncDetail, "10.0.0.9") {
		t.Errorf("the degraded detail echoed the catalog address: %q", hypotheses.SyncDetail)
	}
	for _, item := range hypotheses.Items {
		if item.Sync != fleet.SyncUnknown {
			t.Errorf("candidate %s sync = %q, want %q", item.ID, item.Sync, fleet.SyncUnknown)
		}
	}

	var findings findingList
	decodeResponse(t, h.get("/api/findings"), &findings)
	if !findings.SyncDegraded {
		t.Error("the findings listing does not report its degraded sync state")
	}
	for _, item := range findings.Items {
		if item.Sync != fleet.SyncUnknown {
			t.Errorf("finding %s sync = %q, want %q", item.ID, item.Sync, fleet.SyncUnknown)
		}
	}

	var queue queueResult
	decodeResponse(t, h.get("/api/review/queue?status=all"), &queue)
	if len(queue.Items) == 0 {
		t.Fatal("the review inbox lost its rows to another machine's outage")
	}
	if !queue.SyncDegraded {
		t.Error("the review inbox does not report its degraded sync state")
	}
	for _, item := range queue.Items {
		if item.Sync != fleet.SyncUnknown {
			t.Errorf("queue row %s sync = %q, want %q",
				item.Subject.ID, item.Sync, fleet.SyncUnknown)
		}
	}

	// The receipt strip is this machine's own record of its own runs, so it
	// renders too — unattributed, and saying that the attribution is unknown
	// rather than leaving an absence that reads like a registration gap.
	var state analysisState
	decodeResponse(t, h.get("/api/analysis/state"), &state)
	if !state.SyncDegraded {
		t.Error("the receipt strip does not report that attribution is unknown")
	}
	for _, run := range state.Runs {
		if run.HostAttributed {
			t.Errorf("run %s was attributed to %q by an unreachable catalog", run.RunID, run.Host)
		}
	}
}

// TestSyncVocabularyIsFourValues pins the strings the CLI and the web surface
// share. Each is a different fact and none may stand in for another: "local"
// says nothing is carrying the record anywhere, "pending-sync" promises
// something will, and "unknown" says this machine could not find out.
func TestSyncVocabularyIsFourValues(t *testing.T) {
	frozen := []string{
		sharedcatalog.SyncCommitted,
		sharedcatalog.SyncPending,
		fleet.SyncLocal,
		fleet.SyncUnknown,
	}
	want := []string{"committed", "pending-sync", "local", "unknown"}
	if !reflect.DeepEqual(frozen, want) {
		t.Fatalf("the sync vocabulary is %#v, want %#v", frozen, want)
	}
	seen := map[string]bool{}
	for _, value := range frozen {
		if seen[value] {
			t.Errorf("%q appears twice in the vocabulary", value)
		}
		seen[value] = true
	}
}

// TestFleetRecordsReportAttributionAndSync walks the whole fixture: the frozen
// sync vocabulary, the absent host reported as absent, the local host marked as
// local and nothing else marked with it, and the unopenable record carrying its
// reason instead of a blank row.
func TestFleetRecordsReportAttributionAndSync(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var result fleetRecordList
	decodeResponse(t, h.get("/api/fleet/records?pending=1"), &result)
	if !result.Configured {
		t.Fatal("records reports no configured fleet with a reader wired")
	}
	byID := map[string]fleetRecordView{}
	for _, item := range result.Items {
		byID[item.RecordID] = item
		switch item.Sync {
		case sharedcatalog.SyncCommitted, sharedcatalog.SyncPending, fleet.SyncLocal:
		default:
			t.Errorf("record %s sync = %q, which is outside the frozen vocabulary",
				item.RecordID, item.Sync)
		}
	}

	local, found := byID["frec-local"]
	if !found {
		t.Fatalf("the local host's record is missing: %#v", result.Items)
	}
	if !local.LocalHost {
		t.Error("this machine's own record is not marked as this machine's")
	}
	if local.Host != "this workstation plain" || !local.HostAttributed {
		t.Errorf("local record host = %q (attributed %v)", local.Host, local.HostAttributed)
	}
	if local.Sync != sharedcatalog.SyncCommitted || local.CommittedAt == "" {
		t.Errorf("local record sync = %q committed_at = %q", local.Sync, local.CommittedAt)
	}
	if local.Summary != "plain" {
		t.Errorf("local record summary = %q, want the record's own wording", local.Summary)
	}
	if local.Actor != "inst-local" {
		t.Errorf("local record actor = %q", local.Actor)
	}
	if local.Kind != string(sharedcatalog.KindHypothesis) {
		t.Errorf("local record kind = %q", local.Kind)
	}

	remote := byID["frec-remote"]
	if remote.LocalHost {
		t.Error("another host's record is marked as this machine's")
	}
	if remote.Host != "the other laptop plain" || !remote.HostAttributed {
		t.Errorf("remote record host = %q (attributed %v)", remote.Host, remote.HostAttributed)
	}

	// The absence is the assertion: an unattributed record names no host and
	// says so, rather than borrowing the local machine's identity.
	unattributed := byID["frec-unattributed"]
	if unattributed.Host != "" || unattributed.HostAttributed {
		t.Errorf("unattributed record host = %q (attributed %v), want an absence",
			unattributed.Host, unattributed.HostAttributed)
	}
	if unattributed.LocalHost {
		t.Error("an unattributed record was marked as this machine's")
	}

	pending := byID["frec-pending"]
	if pending.Sync != sharedcatalog.SyncPending {
		t.Errorf("staged record sync = %q, want %q", pending.Sync, sharedcatalog.SyncPending)
	}
	if pending.CommittedAt != "" {
		t.Errorf("staged record committed_at = %q, want an absence", pending.CommittedAt)
	}

	sealed := byID["frec-sealed"]
	if !strings.Contains(sealed.Unopened, "sealed under key") {
		t.Errorf("unopened reason = %q, want the reader's own reason", sealed.Unopened)
	}
	if sealed.Summary != "" {
		t.Errorf("an unopened record carries a summary: %q", sealed.Summary)
	}
	// The row is still a row: an operator has to see that the record exists,
	// which machine holds it, and that it is committed.
	if sealed.Host != "the other laptop plain" || sealed.Kind == "" ||
		sealed.Sync != sharedcatalog.SyncCommitted {
		t.Errorf("unopened record renders as %#v", sealed)
	}

	// A kind with no searchable output is a row with no summary and no fault.
	// It is not an unopened record: this instance read it, and this build
	// derives no one-line summary for it.
	proposal := byID["frec-remote-proposal"]
	if proposal.RecordID == "" {
		t.Fatalf("the proposal was dropped from the listing: %#v", result.Items)
	}
	if proposal.Summary != "" || proposal.Unopened != "" {
		t.Errorf("proposal row summary = %q unopened = %q, want both absent",
			proposal.Summary, proposal.Unopened)
	}
	if proposal.Kind != string(sharedcatalog.KindProposal) || proposal.Host == "" {
		t.Errorf("proposal row renders as %#v", proposal)
	}

	if result.Pending != 1 {
		t.Errorf("pending count = %d, want 1", result.Pending)
	}
}

// TestFleetRecordsHonourTheirFilters checks the parameters reach the reader. A
// dropped filter produces a listing that is wrong in a way no assertion about
// the rendered rows would catch, so the filter itself is the subject.
func TestFleetRecordsHonourTheirFilters(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	fake := h.fleetOf()

	var result fleetRecordList
	decodeResponse(t, h.get("/api/fleet/records?host="+remoteFleetHost+
		"&kind=hypothesis&kind=finding&pending=1&limit=2&offset=1"), &result)

	filter := fake.recordFilters[0]
	if !reflect.DeepEqual(filter.Hosts, []string{remoteFleetHost}) {
		t.Errorf("hosts filter = %#v", filter.Hosts)
	}
	want := []sharedcatalog.RecordKind{sharedcatalog.KindHypothesis, sharedcatalog.KindFinding}
	if !reflect.DeepEqual(filter.Kinds, want) {
		t.Errorf("kinds filter = %#v, want %#v", filter.Kinds, want)
	}
	if !filter.IncludePending {
		t.Error("pending=1 did not admit staged records")
	}
	if filter.Limit != 2 || filter.Offset != 1 {
		t.Errorf("page = limit %d offset %d, want 2/1", filter.Limit, filter.Offset)
	}
	if len(result.Items) > 2 {
		t.Errorf("limit=2 returned %d rows", len(result.Items))
	}
	for _, item := range result.Items {
		if item.LocalHost {
			t.Errorf("host=%s returned this machine's row %s", remoteFleetHost, item.RecordID)
		}
	}

	// The vocabulary is read without the host narrowing, so the chips beside a
	// narrowed list still offer every machine. A filter that narrowed its own
	// options would leave an operator no way back.
	vocabulary := fake.hostFilters[len(fake.hostFilters)-1]
	if len(vocabulary.Hosts) != 0 {
		t.Errorf("the host vocabulary was narrowed by the host filter: %#v", vocabulary.Hosts)
	}
	if vocabulary.Limit != 0 || vocabulary.Offset != 0 {
		t.Errorf("the host vocabulary was paged: limit %d offset %d",
			vocabulary.Limit, vocabulary.Offset)
	}
	if len(result.Hosts) != len(fake.hosts) {
		t.Errorf("host vocabulary = %d entries, want %d", len(result.Hosts), len(fake.hosts))
	}
}

// TestFleetRoutesRefuseAnUnanswerableFilter refuses rather than answering with
// an empty list: a misspelled kind that returned nothing is indistinguishable
// from a deployment that has produced none of them.
func TestFleetRoutesRefuseAnUnanswerableFilter(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, path := range []string{
		"/api/fleet/records?kind=not-a-kind",
		"/api/fleet/records?pending=yes",
		"/api/fleet/hosts?kind=not-a-kind",
		"/api/fleet/hosts?pending=2",
		"/api/hypotheses?fleet=maybe",
		"/api/findings?fleet=2",
		"/api/review/queue?fleet=true",
	} {
		response := h.get(path)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want 400", path, response.StatusCode)
		}
	}
}

// TestFleetHostsOfferTheWholeVocabulary checks the filter's options, including
// the group with no host at all: a silently dropped group looks like records
// that do not exist, and an operator who cannot select it cannot reach them.
func TestFleetHostsOfferTheWholeVocabulary(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var result fleetHostList
	decodeResponse(t, h.get("/api/fleet/hosts?pending=1"), &result)
	if !result.Configured {
		t.Fatal("hosts reports no configured fleet")
	}
	if result.LocalHost != localFleetHost {
		t.Errorf("local_host = %q, want %q", result.LocalHost, localFleetHost)
	}
	if len(result.Hosts) != 3 {
		t.Fatalf("hosts = %#v, want three entries", result.Hosts)
	}
	var unattributed *fleetHostView
	for i, host := range result.Hosts {
		if !host.Attributed {
			unattributed = &result.Hosts[i]
		}
		if host.HostID == remoteFleetHost && host.Pending != 1 {
			t.Errorf("%s pending = %d, want 1: a host's outage must be visible in the filter",
				host.HostID, host.Pending)
		}
	}
	if unattributed == nil {
		t.Fatal("the unattributed group is not offered as a filter option")
	}
	if unattributed.Host != "" || unattributed.HostID != "" {
		t.Errorf("the unattributed group names a host: %#v", unattributed)
	}
	if unattributed.Records != 1 {
		t.Errorf("the unattributed group holds %d records, want 1", unattributed.Records)
	}
}

// TestFleetListingsMergeTheOtherHostsOnRequest is issue #109 item 4's merge:
// with ?fleet=1 the review inbox, the frontier and the findings list carry the
// other hosts' committed records, attributed, after this machine's own — and
// without it they carry only this machine's.
func TestFleetListingsMergeTheOtherHostsOnRequest(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var localOnly hypothesisList
	decodeResponse(t, h.get("/api/hypotheses"), &localOnly)
	for _, item := range localOnly.Items {
		if !item.LocalHost {
			t.Errorf("candidate %s is another host's without ?fleet=1", item.ID)
		}
	}

	var merged hypothesisList
	decodeResponse(t, h.get("/api/hypotheses?fleet=1"), &merged)
	if len(merged.Items) <= len(localOnly.Items) {
		t.Fatalf("?fleet=1 added no rows: %d vs %d", len(merged.Items), len(localOnly.Items))
	}
	// Local first, and the local block is unchanged: a merge that reordered
	// this machine's frontier would make an operator's own backlog move under
	// him whenever another host committed.
	for i := range localOnly.Items {
		if merged.Items[i].ID != localOnly.Items[i].ID {
			t.Fatalf("row %d is %s merged and %s local", i, merged.Items[i].ID, localOnly.Items[i].ID)
		}
	}
	// The frontier's own count is this machine's, unchanged by the appendix.
	if merged.Total != localOnly.Total {
		t.Errorf("total = %d merged and %d local", merged.Total, localOnly.Total)
	}
	remote := merged.Items[len(localOnly.Items):]
	byID := map[string]HypothesisSummary{}
	for _, item := range remote {
		byID[item.ID] = item
		if item.LocalHost {
			t.Errorf("the fleet block carries this machine's row %s", item.ID)
		}
		if item.Sync != sharedcatalog.SyncCommitted {
			t.Errorf("fleet row %s sync = %q: only committed records are globally reviewable",
				item.ID, item.Sync)
		}
		if item.ReviewStatus != "" {
			t.Errorf("fleet row %s claims review status %q, which is the owning host's derivation",
				item.ID, item.ReviewStatus)
		}
	}
	// The other host's staged candidate is absent: SPEC.md §9 makes staged
	// output not globally reviewable, so it has no business on this machine's
	// frontier at all.
	if _, found := byID["frec-pending"]; found {
		t.Error("the fleet block carries another host's staged candidate")
	}
	attributed, found := byID["frec-remote"]
	if !found {
		t.Fatalf("the other host's candidate is missing: %#v", remote)
	}
	if attributed.Host != "the other laptop plain" || !attributed.HostAttributed {
		t.Errorf("fleet candidate host = %q (attributed %v)", attributed.Host, attributed.HostAttributed)
	}
	if attributed.Statement != "plain" {
		t.Errorf("fleet candidate statement = %q, want the record's own wording", attributed.Statement)
	}
	if attributed.Status != string(frontier.StatusInvestigating) {
		t.Errorf("fleet candidate status = %q, want the state it carried when it was staged",
			attributed.Status)
	}
	// A candidate no host can be named for still reaches the frontier, and
	// still names nobody. Dropping it would hide records that exist; guessing
	// its host would file one machine's analysis under another's.
	unattributed, found := byID["frec-unattributed"]
	if !found {
		t.Fatalf("the unattributed candidate was dropped: %#v", remote)
	}
	if unattributed.Host != "" || unattributed.HostAttributed {
		t.Errorf("unattributed fleet candidate host = %q (attributed %v)",
			unattributed.Host, unattributed.HostAttributed)
	}
	// And a candidate this instance cannot open is a row with a reason rather
	// than a row with a blank statement.
	sealed, found := byID["frec-sealed"]
	if !found {
		t.Fatalf("the unopenable candidate was dropped: %#v", remote)
	}
	if sealed.Statement != "" || !strings.Contains(sealed.Unopened, "sealed under key") {
		t.Errorf("unopenable fleet candidate renders as %#v", sealed)
	}

	// The findings list merges the other host's consolidation.
	var findings findingList
	decodeResponse(t, h.get("/api/findings?fleet=1"), &findings)
	var remoteFindings []FindingSummary
	for _, item := range findings.Items {
		if !item.LocalHost {
			remoteFindings = append(remoteFindings, item)
		}
	}
	if len(remoteFindings) != 1 {
		t.Fatalf("the findings list carries %d fleet rows, want one: %#v",
			len(remoteFindings), findings.Items)
	}
	if remoteFindings[0].Title != "plain" || remoteFindings[0].Host != "the other laptop plain" {
		t.Errorf("fleet finding renders as %#v", remoteFindings[0])
	}

	// The review inbox merges the other host's recorded decision and its
	// proposal. The proposal is the one that must not vanish: it is the inbox's
	// main subject and it has no searchable summary at all.
	var queue queueResult
	decodeResponse(t, h.get("/api/review/queue?status=all&fleet=1"), &queue)
	remoteRows := map[string]QueueItem{}
	for _, item := range queue.Items {
		if item.LocalHost {
			continue
		}
		remoteRows[item.Subject.ID] = item
		if item.Host != "the other laptop plain" || !item.HostAttributed {
			t.Errorf("fleet queue row host = %q (attributed %v)", item.Host, item.HostAttributed)
		}
		if item.Status != "" || item.Decisions != 0 || item.Refinements != 0 {
			t.Errorf("fleet queue row %s reports a derived review state this machine does not hold: %#v",
				item.Subject.ID, item)
		}
	}
	// The decision names the record it decided, not its own identifier.
	decision, found := remoteRows["frec-remote"]
	if !found {
		t.Fatalf("the merged decision does not name the record it decided: %#v", remoteRows)
	}
	if decision.Excerpt == "" {
		t.Error("the merged decision carries no excerpt")
	}
	proposal, found := remoteRows["frec-remote-proposal"]
	if !found {
		t.Fatalf("the merged proposal is missing from the review inbox: %#v", remoteRows)
	}
	if proposal.Subject.Type != string(sharedcatalog.KindProposal) {
		t.Errorf("merged proposal subject = %#v", proposal.Subject)
	}
	if proposal.Excerpt != "" || proposal.Unopened != "" {
		t.Errorf("merged proposal excerpt = %q unopened = %q, want an unsummarized row",
			proposal.Excerpt, proposal.Unopened)
	}
}

// TestFleetListingsReportEveryFrozenSyncState pins the vocabulary the CLI and
// the web surface share. The three states are three different facts, and the
// one that must never be confused with another is "local": nothing is going to
// carry that record anywhere, where "pending-sync" promises something will.
func TestFleetListingsReportEveryFrozenSyncState(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	// The ids come from the page the listing actually renders rather than from
	// the fixture's own records: a state named for a candidate this route does
	// not list would assert nothing.
	var listed hypothesisList
	decodeResponse(t, h.get("/api/hypotheses"), &listed)
	if len(listed.Items) < 3 {
		t.Fatalf("the frontier lists %d candidates, want at least three", len(listed.Items))
	}
	fake := h.fleetOf()
	fake.states[listed.Items[0].ID] = sharedcatalog.SyncCommitted
	fake.states[listed.Items[1].ID] = sharedcatalog.SyncPending
	// The third is deliberately absent from the map, so it resolves the way a
	// record no remote row and no journal describes does.
	want := map[string]string{
		listed.Items[0].ID: sharedcatalog.SyncCommitted,
		listed.Items[1].ID: sharedcatalog.SyncPending,
		listed.Items[2].ID: fleet.SyncLocal,
	}

	var result hypothesisList
	decodeResponse(t, h.get("/api/hypotheses"), &result)
	for _, item := range result.Items {
		if state, named := want[item.ID]; named && item.Sync != state {
			t.Errorf("candidate %s sync = %q, want %q", item.ID, item.Sync, state)
		}
		if !item.LocalHost {
			t.Errorf("candidate %s is not marked as this machine's", item.ID)
		}
	}
	// The resolution was asked about the rows the page rendered, and the
	// journal seam was carried through rather than dropped.
	if len(fake.syncIDs) < len(result.Items) {
		t.Errorf("sync resolution saw %d ids for %d rows", len(fake.syncIDs), len(result.Items))
	}
}

// TestFleetRunsAreAttributedByTheCatalog checks the receipt strip's host comes
// from the shared catalog's registration rather than from the machine that
// happens to be rendering. A run the catalog cannot attribute renders as
// unattributed, which is an absence and not a default.
func TestFleetRunsAreAttributedByTheCatalog(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) {
		opts.Runs = runLister{
			{ReceiptID: "rcp-attributed", RunID: "frun-remote", RecordedAt: "2026-03-01T12:00:00Z"},
			{ReceiptID: "rcp-unknown", RunID: "frun-nowhere", RecordedAt: "2026-03-01T12:00:00Z"},
		}
	})

	var state analysisState
	decodeResponse(t, h.get("/api/analysis/state"), &state)
	if len(state.Runs) != 2 {
		t.Fatalf("runs = %#v", state.Runs)
	}
	if state.Runs[0].Host != "the other laptop plain" || !state.Runs[0].HostAttributed {
		t.Errorf("attributed run host = %q (attributed %v)",
			state.Runs[0].Host, state.Runs[0].HostAttributed)
	}
	if state.Runs[1].Host != "" || state.Runs[1].HostAttributed {
		t.Errorf("a run the catalog does not hold was attributed to %q (attributed %v)",
			state.Runs[1].Host, state.Runs[1].HostAttributed)
	}
	// The attribution read is by run id and admits staged runs, because a run
	// that has not committed still belongs to the machine that produced it.
	fake := h.fleetOf()
	last := fake.recordFilters[len(fake.recordFilters)-1]
	if !last.IncludePending {
		t.Error("the run attribution read excluded staged runs")
	}
	if !reflect.DeepEqual(last.RunIDs, []string{"frun-remote", "frun-nowhere"}) {
		t.Errorf("the run attribution read named %#v", last.RunIDs)
	}
}

// TestFleetReaderSurfaceHoldsNoWriter is the §14 property for issue #109's read
// path: the whole authority a browser reaches over the fleet is these five
// reads. Ingest is the one that matters — it writes this machine's retrieval
// index, and a GET route that could reach it would make the busiest writer in
// the process a read.
func TestFleetReaderSurfaceHoldsNoWriter(t *testing.T) {
	surface := reflect.TypeOf((*FleetReader)(nil)).Elem()
	permitted := map[string]bool{
		"LocalHost": true, "Records": true, "RecordsWithContent": true,
		"Hosts": true, "SyncStates": true,
	}
	for i := range surface.NumMethod() {
		if name := surface.Method(i).Name; !permitted[name] {
			t.Errorf("FleetReader exposes %s, which is outside the read contract", name)
		}
	}
	if surface.NumMethod() != len(permitted) {
		t.Errorf("FleetReader has %d methods, want %d", surface.NumMethod(), len(permitted))
	}
	concrete := reflect.TypeOf((*fleet.Reader)(nil))
	for _, forbidden := range []string{"Ingest"} {
		if _, found := concrete.MethodByName(forbidden); !found {
			t.Fatalf("*fleet.Reader no longer has %s, so this assertion checks nothing", forbidden)
		}
		if _, found := surface.MethodByName(forbidden); found {
			t.Errorf("FleetReader exposes %s", forbidden)
		}
	}
}

// TestFleetRecordKindVocabularyMatchesTheCatalog keeps the ?kind= filter's
// vocabulary the catalog's own. The catalog's validator is unexported, so this
// is what stops the handler's list from drifting from the kinds a fleet read can
// actually return.
func TestFleetRecordKindVocabularyMatchesTheCatalog(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, kind := range []sharedcatalog.RecordKind{
		sharedcatalog.KindHypothesis, sharedcatalog.KindObservation,
		sharedcatalog.KindFinding, sharedcatalog.KindProposal,
		sharedcatalog.KindLink, sharedcatalog.KindDisposition,
		sharedcatalog.KindContext, sharedcatalog.KindPreparation,
		sharedcatalog.KindReceipt,
	} {
		response := h.get(fmt.Sprintf("/api/fleet/records?kind=%s", kind))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("kind=%s status = %d, want 200", kind, response.StatusCode)
		}
	}
}
