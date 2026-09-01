package web

// Issue #113's read surface. The subject of every test here is what a record
// page is allowed to believe about a citation: which endpoints it may turn into
// links, which it must render inert and why, and that an edge's note is content
// rather than markup.
//
// The reference store is faked rather than opened, and that is the one place
// this file departs from phaseb_test.go's rule about real services. The edges a
// render surface must handle correctly are precisely the ones a local store
// cannot hold — a record on another host, a namespace this build has no page
// for — so a fixture that could only contain resolvable endpoints would exercise
// the easy half of the contract.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/reference"
)

// fakeReferences is a wired reference graph. It answers From and To out of two
// tables keyed by endpoint, in the order the fixture wrote them, because the
// order a store answers in is a property this surface must not disturb.
type fakeReferences struct {
	from map[reference.RecordRef][]reference.Edge
	to   map[reference.RecordRef][]reference.Edge
	err  error
}

func (f *fakeReferences) From(_ context.Context, ref reference.RecordRef) ([]reference.Edge, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.from[ref], nil
}

func (f *fakeReferences) To(_ context.Context, ref reference.RecordRef) ([]reference.Edge, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.to[ref], nil
}

var _ reference.Lister = (*fakeReferences)(nil)

// referenceEpoch is the fixture's clock. Edges are written newest first, the
// way reference.Lister documents its answer, so a test that asserts the order
// on the wire is asserting that this surface passed it through.
var referenceEpoch = time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)

// referencesFixture is the citation graph every phaseB server serves.
//
// It is wired into the harness rather than into the tests that ask for it, for
// the reason fleetFixture is: an edge note is untrusted content, so the hostile
// sweep has to see one without remembering to opt in. The endpoints deliberately
// span all four cases a record page must render — a session, a local analysis
// record, a record this host does not hold, and a namespace it has no page for —
// so the sweep also covers every reason string this file can produce.
func referencesFixture(h *phaseB, text string) *fakeReferences {
	subject := reference.RecordRef{Kind: namespaceHypothesis, ID: h.hypothesis.ID}
	edge := func(id string, kind reference.Kind, from, to reference.RecordRef, minutes int, actor, ref string) reference.Edge {
		return reference.Edge{
			ID:        id,
			Kind:      kind,
			From:      from,
			To:        to,
			ActorKind: actor,
			ActorRef:  ref,
			Note:      "recorded while absorbing evidence " + text,
			CreatedAt: referenceEpoch.Add(-time.Duration(minutes) * time.Minute),
		}
	}
	session := reference.RecordRef{Kind: namespaceSession, ID: sessionKey}
	return &fakeReferences{
		from: map[reference.RecordRef][]reference.Edge{
			subject: {
				edge("ref-1", reference.KindEvidence, subject, session, 0, "run", "run-1 "+text),
				edge("ref-2", reference.KindInspiredBy, subject,
					reference.RecordRef{Kind: namespaceFinding, ID: h.finding.ID}, 10, "run", "run-1 "+text),
				edge("ref-3", reference.KindDuplicates, subject,
					reference.RecordRef{Kind: namespaceHypothesis, ID: "hyp-on-another-host"}, 20, "operator", operatorID),
				// A namespace this build opens no page for. It was
				// "complaint" until #115 gave complaints their own
				// record page, which made that namespace resolvable
				// and this case vacuous; "disposition" is the
				// nearest namespace an edge can name and no page in
				// this build opens on its own.
				edge("ref-4", reference.KindAddresses, subject,
					reference.RecordRef{Kind: "disposition " + text, ID: "dsp-1 " + text}, 30, "system", ""),
			},
		},
		to: map[reference.RecordRef][]reference.Edge{
			subject: {
				edge("ref-5", reference.KindRefines,
					reference.RecordRef{Kind: namespaceProposal, ID: h.proposal.ID}, subject, 5, "run", "run-1 "+text),
				edge("ref-6", reference.KindEvidence,
					reference.RecordRef{Kind: namespaceObservation, ID: "obs-on-another-host"}, subject, 15, "run", "run-2 "+text),
			},
		},
	}
}

// sessionKey is the durable key a session reference carries: a deployment-scoped
// digest, deliberately not the selector, because an endpoint publishes as a
// plaintext catalog column and a selector carries a workspace-derived path.
const sessionKey = "sk-6f1c9d2ab4e83057"

// sessionSelector is the local identity the same session's page routes on, which
// is what a resolved endpoint carries beside the key.
const sessionSelector = "claude/session-a"

// countingSessions is a durable-key resolver that counts how many times it was
// called, which is the only way to assert the one-batch-per-request rule the
// resolver exists to keep.
type countingSessions struct {
	rows  map[string]SessionRow
	calls int
	err   error
}

func (c *countingSessions) SessionsByKey(_ context.Context, keys []string) (map[string]SessionRow, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	found := make(map[string]SessionRow, len(keys))
	for _, key := range keys {
		if row, ok := c.rows[key]; ok {
			found[key] = row
		}
	}
	return found, nil
}

func (c *countingSessions) KeyForSelector(_ context.Context, selector string) (string, bool, error) {
	c.calls++
	if c.err != nil {
		return "", false, c.err
	}
	for key, row := range c.rows {
		if row.Selector == selector {
			return key, true, nil
		}
	}
	return "", false, nil
}

var _ SessionKeyResolver = (*countingSessions)(nil)

// TestRecordLinksResolveASessionSubject covers the identity translation on the
// request side. A session page routes on a selector and an edge records a
// durable key, so the route derives one from the other and echoes both; a
// selector this host has no session for is answered with the reason rather than
// with an empty graph that would read as "nothing cites this session".
func TestRecordLinksResolveASessionSubject(t *testing.T) {
	sessions := heldSessions()
	h := newPhaseB(t, "plain", func(opts *Options) {
		opts.Sessions = sessions
		opts.References = &fakeReferences{
			to: map[reference.RecordRef][]reference.Edge{
				{Kind: namespaceSession, ID: sessionKey}: {{
					ID:        "ref-into-session",
					Kind:      reference.KindEvidence,
					From:      reference.RecordRef{Kind: namespaceObservation, ID: "obs-elsewhere"},
					To:        reference.RecordRef{Kind: namespaceSession, ID: sessionKey},
					ActorKind: "run",
					CreatedAt: referenceEpoch,
				}},
			},
		}
	})

	got := h.links(t, "type=session&id="+url.QueryEscape(sessionSelector))
	if got.CitedBy.Total != 1 {
		t.Fatalf("backlinks = %d, want the one observation resting on this session: %+v", got.CitedBy.Total, got)
	}
	if got.Record.ID != sessionKey || got.Record.RouteID != sessionSelector {
		t.Errorf("record = %+v, want the durable key with the selector asked about beside it", got.Record)
	}

	// A selector this host has no session for: the subject itself is inert,
	// with the reason, and the directions are empty rather than absent.
	missing := h.links(t, "type=session&id="+url.QueryEscape("codex/never-here"))
	if !missing.Record.Inert || !strings.Contains(missing.Record.Reason, "holds no session with that selector") {
		t.Errorf("record = %+v, reason %q: want an inert subject naming the reason",
			missing.Record, missing.Record.Reason)
	}
	if len(missing.CitedBy.Edges) != 0 || missing.CitedBy.Edges == nil {
		t.Errorf("backlinks = %#v, want an empty array", missing.CitedBy.Edges)
	}
}

// heldSessions is the resolver a host that holds the fixture's session gives.
func heldSessions() *countingSessions {
	return &countingSessions{rows: map[string]SessionRow{
		sessionKey: {Harness: "claude", SourceID: "session-a", Selector: sessionSelector},
	}}
}

// links reads the route the way a record page does.
func (h *phaseB) links(t *testing.T, query string) recordReferences {
	t.Helper()
	var got recordReferences
	decodeResponse(t, h.get("/api/record/links?"+query), &got)
	return got
}

func (h *phaseB) subjectQuery() string {
	return "type=hypothesis&id=" + h.hypothesis.ID
}

// TestRecordLinksRenderBothDirections is the route's shape: what the record
// cites, what cites it, in the store's order, attributed to the actor that
// asserted each edge and to the host whose catalog answered.
func TestRecordLinksRenderBothDirections(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	got := h.links(t, h.subjectQuery())

	if got.Record.Kind != namespaceHypothesis || got.Record.ID != h.hypothesis.ID {
		t.Errorf("record = %+v, want the hypothesis asked about", got.Record)
	}
	if got.Host != hostUnderTest {
		t.Errorf("host = %q, want %q: a one-host view must say whose it is", got.Host, hostUnderTest)
	}
	if got.Cites.Total != 4 || got.CitedBy.Total != 2 {
		t.Fatalf("totals = %d cites / %d cited by, want 4/2", got.Cites.Total, got.CitedBy.Total)
	}

	// The far endpoint is the one that travels, and it is the far one in
	// both directions: a page standing on the candidate sees the record it
	// cited and the record that cited it, never itself.
	wantCites := []string{"ref-1", "ref-2", "ref-3", "ref-4"}
	for i, edge := range got.Cites.Edges {
		if edge.ID != wantCites[i] {
			t.Errorf("cites[%d] = %q, want %q: the store's order must survive the route", i, edge.ID, wantCites[i])
		}
		if edge.Other.ID == h.hypothesis.ID {
			t.Errorf("cites[%d] names the subject as the far endpoint", i)
		}
	}
	if got.CitedBy.Edges[0].Other.ID != h.proposal.ID {
		t.Errorf("backlink endpoint = %q, want the citing proposal %q",
			got.CitedBy.Edges[0].Other.ID, h.proposal.ID)
	}

	// Attribution is the edge's own: a run for an absorbed citation, the
	// operator for one a person asserted.
	if actor := got.Cites.Edges[0].Actor; actor.Kind != "run" || !strings.HasPrefix(actor.ID, "run-1") {
		t.Errorf("actor = %+v, want the run that absorbed the evidence", actor)
	}
	if actor := got.Cites.Edges[2].Actor; actor.Kind != "operator" || actor.ID != operatorID {
		t.Errorf("actor = %+v, want the operator who asserted the duplicate", actor)
	}
	if got.Cites.Edges[0].CreatedAt != referenceEpoch.Format(time.RFC3339) {
		t.Errorf("created_at = %q, want %q", got.Cites.Edges[0].CreatedAt, referenceEpoch.Format(time.RFC3339))
	}

	// The chip row is by kind, over the whole direction, in first-seen order
	// so the newest citation's kind leads.
	want := []referenceKindCount{
		{Kind: "evidence", Count: 1},
		{Kind: "inspired_by", Count: 1},
		{Kind: "duplicates", Count: 1},
		{Kind: "addresses", Count: 1},
	}
	if len(got.Cites.Counts) != len(want) {
		t.Fatalf("counts = %+v, want %+v", got.Cites.Counts, want)
	}
	for i, count := range got.Cites.Counts {
		if count != want[i] {
			t.Errorf("counts[%d] = %+v, want %+v", i, count, want[i])
		}
	}
}

// TestRecordLinksReportAnAbsentHostIdentity is the other half of host
// attribution. A build that cannot name its own machine leaves the field out,
// because a page that filled it in with the host it happens to be running on
// would attribute one machine's citations to another.
func TestRecordLinksReportAnAbsentHostIdentity(t *testing.T) {
	h := newPhaseB(t, "plain", withoutHost)
	if got := h.links(t, h.subjectQuery()).Host; got != "" {
		t.Errorf("host = %q, want an absence reported as an absence", got)
	}
}

// TestRecordLinksFollowComplaintEndpoints is #115's half of the citation graph.
//
// A complaint endpoint is what answers "was this ever addressed?" from the
// other direction, so it has to be followable exactly when this host can open
// the record — and inert with its own reason otherwise, on frontierRecord's
// three terms rather than on a generic refusal an operator could not act on.
func TestRecordLinksFollowComplaintEndpoints(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	subject := reference.RecordRef{Kind: namespaceHypothesis, ID: h.hypothesis.ID}
	graph := &fakeReferences{from: map[reference.RecordRef][]reference.Edge{
		subject: {
			{
				ID: "ref-c1", Kind: reference.KindAddresses, From: subject,
				To:        reference.RecordRef{Kind: namespaceComplaint, ID: h.complaint.ID},
				ActorKind: "run", ActorRef: "run-1", CreatedAt: referenceEpoch,
			},
			{
				ID: "ref-c2", Kind: reference.KindAddresses, From: subject,
				To:        reference.RecordRef{Kind: namespaceComplaint, ID: "cmp-on-another-host"},
				ActorKind: "run", ActorRef: "run-1", CreatedAt: referenceEpoch.Add(-time.Minute),
			},
		},
	}}
	serve := func(complaints ComplaintService) {
		h.server, h.http = testServer(t, Options{
			Operator: operatorID, Frontier: h.front, Complaints: complaints, References: graph,
		})
	}

	serve(h.complaints)
	edges := h.links(t, h.subjectQuery()).Cites.Edges
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want the two complaint endpoints", len(edges))
	}
	if held := edges[0].Other; held.Inert || held.Reason != "" || held.ID != h.complaint.ID {
		t.Errorf("endpoint %+v: a complaint this host holds must be followable", held)
	}
	// A complaint another machine's operator told. #112 makes the edge
	// readable here even though the record never will be.
	absent := edges[1].Other
	if !absent.Inert || !strings.Contains(absent.Reason, "holds no complaint") {
		t.Errorf("endpoint %+v, reason %q: want inert, naming the record this host does not hold",
			absent, absent.Reason)
	}

	// A build whose complaint component did not open. The row says which of
	// the two absences it is: the service, not the record.
	serve(nil)
	unwired := h.links(t, h.subjectQuery()).Cites.Edges[0].Other
	if !unwired.Inert || !strings.Contains(unwired.Reason, "operator complaints are not available") {
		t.Errorf("endpoint %+v, reason %q: want inert, naming the missing service", unwired, unwired.Reason)
	}
}

// TestRecordLinksMarkUnreachableEndpointsInert is the rule that keeps a
// fleet-visible graph honest: an endpoint this host cannot open renders as
// identified text with a stated reason, never as a link into a page that would
// report nothing.
func TestRecordLinksMarkUnreachableEndpointsInert(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Sessions = heldSessions() })
	got := h.links(t, h.subjectQuery())

	// A session this host holds, and a local finding: both followable. The
	// session also carries the local selector its page routes on, which the
	// durable key it was recorded under does not give a client.
	for _, edge := range []referenceEdgeView{got.Cites.Edges[0], got.Cites.Edges[1]} {
		if edge.Other.Inert || edge.Other.Reason != "" {
			t.Errorf("edge %s: endpoint %+v is inert, want followable", edge.ID, edge.Other)
		}
	}
	if session := got.Cites.Edges[0].Other; session.ID != sessionKey ||
		session.RouteID != sessionSelector || session.Label != sessionSelector {
		t.Errorf("session endpoint = %+v, want the durable key with the local selector to route on", session)
	}
	if record := got.Cites.Edges[1].Other; record.RouteID != "" {
		t.Errorf("finding endpoint = %+v: a record routed by its own id must carry no second identity", record)
	}

	// A candidate no local record answers for. The edge stays, because its
	// shape is exactly what #112 makes readable on a host that will never
	// hold the record; the reason says which of the four cases it is.
	absent := got.Cites.Edges[2].Other
	if !absent.Inert {
		t.Fatalf("endpoint %+v: a record this host does not hold must be inert", absent)
	}
	if !strings.Contains(absent.Reason, "holds no hypothesis") {
		t.Errorf("reason = %q, want it to name the record this host does not hold", absent.Reason)
	}

	// A namespace with no page in this build. The reason names the namespace
	// rather than reporting a generic refusal, so an operator can tell a
	// missing page from a missing record.
	unknown := got.Cites.Edges[3].Other
	if !unknown.Inert || !strings.Contains(unknown.Reason, "opens no page") {
		t.Errorf("endpoint %+v, reason %q: want an inert row naming the unknown namespace", unknown, unknown.Reason)
	}
	if !strings.Contains(unknown.Reason, "disposition") {
		t.Errorf("reason = %q, want the namespace quoted in it", unknown.Reason)
	}

	// And a backlink from a record this host does not hold is inert on the
	// same terms: the direction does not change what is knowable.
	if backlink := got.CitedBy.Edges[1].Other; !backlink.Inert ||
		!strings.Contains(backlink.Reason, "holds no observation") {
		t.Errorf("backlink endpoint %+v, reason %q: want inert with the namespace named", backlink, backlink.Reason)
	}
}

// TestRecordLinksReportUnresolvableSessions covers the session endpoint's three
// answers, which are three different facts and must not collapse into one.
func TestRecordLinksReportUnresolvableSessions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolver SessionKeyResolver
		want     string
	}{
		{
			name: "no resolver wired",
			want: "cannot resolve durable session keys",
		},
		{
			name:     "catalog holds no session with that key",
			resolver: &countingSessions{rows: map[string]SessionRow{"sk-other": {Selector: "codex/other"}}},
			want:     "holds no session with that durable key",
		},
		{
			name:     "catalog could not be read",
			resolver: &countingSessions{err: errors.New("catalog is being rebuilt")},
			want:     "could not read its session catalog",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPhaseB(t, "plain", func(opts *Options) { opts.Sessions = tc.resolver })
			endpoint := h.links(t, h.subjectQuery()).Cites.Edges[0].Other
			if !endpoint.Inert {
				t.Fatalf("endpoint %+v, want inert", endpoint)
			}
			if !strings.Contains(endpoint.Reason, tc.want) {
				t.Errorf("reason = %q, want it to contain %q", endpoint.Reason, tc.want)
			}
		})
	}
}

// TestRecordLinksResolveSessionKeysInOneBatch defends the resolver's whole
// reason for existing. A citation graph names one session from many edges, and
// the catalog matches a durable key by deriving keys for the rows it has, so a
// page of edges must not become a pass per edge.
func TestRecordLinksResolveSessionKeysInOneBatch(t *testing.T) {
	// A synthetic subject, because the route resolves the endpoints an edge
	// names and never the record the page is standing on: what is under test
	// is twelve citations of one session, not the candidate citing them.
	subject := reference.RecordRef{Kind: namespaceHypothesis, ID: "hyp-many-citations"}
	edges := make([]reference.Edge, 0, 12)
	for i := range 12 {
		edges = append(edges, reference.Edge{
			ID:        "ref-many-" + strconv.Itoa(i),
			Kind:      reference.KindEvidence,
			From:      subject,
			To:        reference.RecordRef{Kind: namespaceSession, ID: sessionKey},
			ActorKind: "run",
			CreatedAt: referenceEpoch,
		})
	}
	sessions := heldSessions()
	h := newPhaseB(t, "plain", func(opts *Options) {
		opts.Sessions = sessions
		opts.References = &fakeReferences{
			from: map[reference.RecordRef][]reference.Edge{subject: edges},
		}
	})
	got := h.links(t, "type="+subject.Kind+"&id="+subject.ID+"&limit=12")
	if len(got.Cites.Edges) != 12 {
		t.Fatalf("edges = %d, want 12", len(got.Cites.Edges))
	}
	if sessions.calls != 1 {
		t.Errorf("catalog passes = %d, want 1 for twelve edges naming one session", sessions.calls)
	}
	for _, edge := range got.Cites.Edges {
		if edge.Other.Inert {
			t.Fatalf("edge %s: endpoint %+v is inert, want the held session followable", edge.ID, edge.Other)
		}
	}
}

// TestRecordLinksPageWithoutHidingTheTotals checks the bound. A direction is
// cut to the caller's window while its counts stay over the whole direction, so
// a chip row summarizing a graph does not shrink as a reader pages through it.
func TestRecordLinksPageWithoutHidingTheTotals(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	got := h.links(t, h.subjectQuery()+"&limit=2&offset=1")
	if len(got.Cites.Edges) != 2 {
		t.Fatalf("edges = %d, want the 2 the window names", len(got.Cites.Edges))
	}
	if got.Cites.Edges[0].ID != "ref-2" {
		t.Errorf("first edge = %q, want ref-2 at offset 1", got.Cites.Edges[0].ID)
	}
	if got.Cites.Total != 4 || got.Cites.Limit != 2 || got.Cites.Offset != 1 {
		t.Errorf("page = %d total, limit %d, offset %d, want 4/2/1",
			got.Cites.Total, got.Cites.Limit, got.Cites.Offset)
	}
	if len(got.Cites.Counts) != 4 {
		t.Errorf("counts = %+v, want all four kinds despite the window", got.Cites.Counts)
	}
	// An offset past the end is an empty page rather than a refusal, and the
	// array is still an array: a client must not have to distinguish null.
	empty := h.links(t, h.subjectQuery()+"&offset=99")
	if empty.Cites.Edges == nil || len(empty.Cites.Edges) != 0 {
		t.Errorf("edges = %#v, want an empty array", empty.Cites.Edges)
	}
	if code := h.get("/api/record/links?" + h.subjectQuery() + "&limit=9000").StatusCode; code != http.StatusBadRequest {
		t.Errorf("oversized limit status = %d, want 400", code)
	}
}

// TestRecordLinksWithoutAGraphDegrade is the nil-injection rule: a build with no
// reference store keeps every page it already served, and the section says the
// build records no citations rather than the page reporting a failure.
//
// It answers 200 and not 409 deliberately. A refusal on this surface reaches the
// client's global error banner, so a record page on a machine with no reference
// store would carry an error about a panel while every other panel on it loaded:
// the fleet routes answer an unconfigured deployment the same way and for the
// same reason.
func TestRecordLinksWithoutAGraphDegrade(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.References = nil })
	response := h.get("/api/record/links?" + h.subjectQuery())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body %q, want 200", response.StatusCode, body(t, response))
	}
	var got recordReferences
	decodeResponse(t, h.get("/api/record/links?"+h.subjectQuery()), &got)
	if got.Available {
		t.Errorf("available = true with no graph wired")
	}
	if got.Cites.Total != 0 || len(got.Cites.Edges) != 0 || got.Cites.Edges == nil {
		t.Errorf("cites = %+v, want an empty array and no total", got.Cites)
	}
	if got.Record.ID != h.hypothesis.ID {
		t.Errorf("record = %+v, want the record asked about echoed back", got.Record)
	}
	// The record page's other reads still answer, which is the property that
	// makes the section absent rather than the page broken.
	if code := h.get("/api/record/revisions?" + h.subjectQuery()).StatusCode; code != http.StatusOK {
		t.Errorf("revisions status = %d, want the rest of the page to keep answering", code)
	}
}

// TestRecordLinksRefuseAnUnnamedRecord covers the two required parameters, and
// that an unknown namespace on the *subject* is an empty graph rather than a
// rejection: the namespaces are whatever the stores register themselves as, so
// this route holds no second copy of that vocabulary.
func TestRecordLinksRefuseAnUnnamedRecord(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, query := range []string{"", "type=hypothesis", "id=" + h.hypothesis.ID} {
		if code := h.get("/api/record/links?" + query).StatusCode; code != http.StatusBadRequest {
			t.Errorf("%q status = %d, want 400", query, code)
		}
	}
	got := h.links(t, "type=complaint&id=cmp-404")
	if got.Cites.Total != 0 || got.CitedBy.Total != 0 {
		t.Errorf("totals = %d/%d, want an empty graph", got.Cites.Total, got.CitedBy.Total)
	}
}

// TestRecordLinksReportAStoreRefusal keeps a broken graph from being rendered as
// an empty one. "This record cites nothing" and "the store could not say" are
// different answers, and only the first is safe to draw.
func TestRecordLinksReportAStoreRefusal(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) {
		opts.References = &fakeReferences{err: errors.New("durable catalog is locked")}
	})
	response := h.get("/api/record/links?" + h.subjectQuery())
	text := body(t, response)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d body %q, want 500", response.StatusCode, text)
	}
	if strings.Contains(text, "durable catalog is locked") {
		t.Errorf("body = %q quotes the store's own error", text)
	}
}

// TestRecordLinksNoteIsContent checks the one field on this surface that carries
// somebody's prose. A note is why a link exists, in the words of whoever
// asserted it, so it is neutralized on the way out exactly as a model claim is —
// and the shape of a link is never taken from it.
func TestRecordLinksNoteIsContent(t *testing.T) {
	h := newPhaseB(t, malicious, nil)
	text := body(t, h.get("/api/record/links?"+h.subjectQuery()))
	for _, forbidden := range []string{"\x1b", "\u202e", "\u202c", "\u200b"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("response carries %q unescaped: %s", forbidden, text)
		}
	}
	if strings.Contains(text, "<script>") {
		t.Errorf("response carries an executable tag: %s", text)
	}
	// The note keeps its own words, hostile ones included: it is a record of
	// why somebody asserted a link, and a surface that dropped the sentence
	// because it contained a colon would be editing the corpus.
	var got recordReferences
	decodeResponse(t, h.get("/api/record/links?"+h.subjectQuery()), &got)
	if !strings.Contains(got.Cites.Edges[0].Note, "recorded while absorbing evidence") {
		t.Errorf("note = %q, want the asserted sentence retained", got.Cites.Edges[0].Note)
	}

	// And no field in the document is a destination. The endpoint travels as
	// a namespace and an identifier the client routes on itself, so there is
	// no key a note's text could ever be promoted into — which is the
	// property under test, not the absence of colons in prose.
	var tree map[string]any
	decodeResponse(t, h.get("/api/record/links?"+h.subjectQuery()), &tree)
	for _, key := range keysOf(tree) {
		switch key {
		case "url", "href", "route", "path", "link", "target":
			t.Errorf("the document carries a %q key: a link target must be an identity, not an href", key)
		}
	}
}

// keysOf collects every object key in a decoded document, at any depth.
func keysOf(value any) []string {
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key, entry := range value {
			keys = append(keys, key)
			keys = append(keys, keysOf(entry)...)
		}
		return keys
	case []any:
		var keys []string
		for _, entry := range value {
			keys = append(keys, keysOf(entry)...)
		}
		return keys
	}
	return nil
}

// TestReviewQueueCountsCitations covers the inbox column. A queue is read to
// decide what to open next, so a row says how load-bearing its record is; and a
// build with no graph shows no column rather than a row of zeroes, because
// "nothing cites this" and "nobody counted" are different claims.
func TestReviewQueueCitationCounts(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	var queue queueResult
	decodeResponse(t, h.get("/api/review/queue?status=all"), &queue)
	var counted, seen int
	for _, item := range queue.Items {
		if item.Subject.ID != h.hypothesis.ID {
			continue
		}
		seen++
		if item.Citations == nil {
			t.Fatalf("row %s carries no citation counts", item.Subject.ID)
		}
		if item.Citations.Cites != 4 || item.Citations.CitedBy != 2 {
			t.Errorf("citations = %+v, want 4 out and 2 in", *item.Citations)
		}
		counted++
	}
	if seen == 0 {
		t.Fatalf("the candidate is not in the queue: %+v", queue.Items)
	}
	if counted != seen {
		t.Errorf("%d of %d rows counted", counted, seen)
	}
	// A row whose record has no edges is counted and reports zero, which is
	// what makes the absent column below mean something.
	for _, item := range queue.Items {
		if item.Subject.ID == h.finding.ID {
			if item.Citations == nil || item.Citations.Cites != 0 {
				t.Errorf("finding row citations = %v, want a counted zero", item.Citations)
			}
		}
	}

	// No graph, no column: the inbox keeps every row it already served.
	bare := newPhaseB(t, "plain", func(opts *Options) { opts.References = nil })
	var without queueResult
	decodeResponse(t, bare.get("/api/review/queue?status=all"), &without)
	if len(without.Items) != len(queue.Items) {
		t.Fatalf("rows = %d, want the same %d the wired build served", len(without.Items), len(queue.Items))
	}
	for _, item := range without.Items {
		if item.Citations != nil {
			t.Errorf("row %s counts citations with no graph wired: %+v", item.Subject.ID, *item.Citations)
		}
	}

	// A graph that refused is not a queue that refuses. The row loses its
	// counts and the page keeps its rows.
	broken := newPhaseB(t, "plain", func(opts *Options) {
		opts.References = &fakeReferences{err: errors.New("citation index is locked")}
	})
	response := broken.get("/api/review/queue?status=all")
	var refused queueResult
	decodeResponse(t, response, &refused)
	if len(refused.Items) == 0 {
		t.Fatalf("a locked citation index emptied the inbox")
	}
	for _, item := range refused.Items {
		if item.Citations != nil {
			t.Errorf("row %s reports counts a refusing graph never gave: %+v", item.Subject.ID, *item.Citations)
		}
	}
}
