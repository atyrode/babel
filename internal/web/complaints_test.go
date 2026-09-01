package web

// Issue #115's web half, and the properties only this surface has.
//
// The route sweeps in phaseb_test.go already cover what every Phase B route
// shares — the session, the origin, `no-store`, one method each, and that a GET
// writes nothing. What is here is what a complaint is: heads only in a listing,
// a whole chain from any wording in a read, a capture attributed to a person and
// a machine or refused, an adjacency pass that cannot fail the capture, and a
// record with no state anywhere in it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/complaint"
)

// answered reads one 200 response twice: as the view a client decodes, and as
// the raw object the charter guard walks. Both come from the same bytes,
// because a guard that walked a re-encoded struct would be checking the Go
// type rather than what went out on the wire.
func answered(t *testing.T, response *http.Response, typed any) map[string]any {
	t.Helper()
	code := response.StatusCode
	raw := body(t, response)
	if code != http.StatusOK {
		t.Fatalf("status = %d body %q, want 200", code, raw)
	}
	if typed != nil {
		if err := json.Unmarshal([]byte(raw), typed); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return generic
}

// statusOf reads a refusal's code and discards its body, which is all a refusal
// assertion is about: the message is a fixed sentence phaseb.go owns.
func statusOf(response *http.Response) int {
	defer response.Body.Close()
	return response.StatusCode
}

// indexFrontier reconciles the retrieval index against everything this
// deployment has said, complaints included, the way `babel prepare` and
// `babel tell` do.
//
// It is a test helper rather than something the web surface can do, and that is
// the point: the capture route searches this partition and never rebuilds it,
// so a test about adjacency has to establish the partition the way the real
// writers would.
func (h *phaseB) indexFrontier() {
	h.t.Helper()
	outputs, err := h.front.Outputs(h.ctx)
	if err != nil {
		h.t.Fatalf("Outputs: %v", err)
	}
	outputs, err = complaint.Append(h.ctx, h.complaints, outputs)
	if err != nil {
		h.t.Fatalf("complaint.Append: %v", err)
	}
	if _, err := h.index.IndexFrontier(h.ctx, outputs); err != nil {
		h.t.Fatalf("IndexFrontier: %v", err)
	}
}

// tell writes one complaint straight to the store, for the fixtures a test
// needs beyond the harness's two.
func (h *phaseB) tell(text string) complaint.Complaint {
	h.t.Helper()
	told, err := h.complaints.Tell(h.ctx, complaint.TellInput{
		Text: text, By: operatorID, Host: hostUnderTest,
	})
	if err != nil {
		h.t.Fatalf("Tell: %v", err)
	}
	return told
}

// TestComplaintListingShowsCurrentWordingsOnly is the listing's whole contract:
// what the operator currently says, once each, newest first, bounded.
//
// The amended chain is what makes it assertable. A superseded wording is not
// what the operator says now, so a listing carrying it would show one complaint
// twice and read as two independent grievances.
func TestComplaintListingShowsCurrentWordingsOnly(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	long := "the repository rules are ignored and then re-explained\n" +
		strings.Repeat("and then ignored again, ", 40)
	verbose := h.tell(long)

	var listed complaintList
	answered(t, h.get("/api/complaints"), &listed)

	if listed.Total != 3 || len(listed.Items) != 3 {
		t.Fatalf("listing = %d items of %d total, want the three heads", len(listed.Items), listed.Total)
	}
	ids := make([]string, 0, len(listed.Items))
	for _, item := range listed.Items {
		ids = append(ids, item.ID)
	}
	// The superseded wording is absent and its successor is present: the
	// listing is heads, not rows.
	if !contains(ids, h.amended.ID) || contains(ids, h.superseded.ID) {
		t.Errorf("listing = %v, want the head of the amended chain and not the wording it replaced", ids)
	}
	// Newest first, which is the store's order and not this handler's: the
	// complaint written last is the row read first.
	if listed.Items[0].ID != verbose.ID {
		t.Errorf("first row = %s, want the newest complaint %s", listed.Items[0].ID, verbose.ID)
	}
	for i := 1; i < len(listed.Items); i++ {
		if listed.Items[i].At > listed.Items[i-1].At {
			t.Errorf("row %d is newer than row %d: the listing is not in store order", i, i-1)
		}
	}

	// A row carries the bounded line and never the operator's whole text. A
	// listing that shipped fifty verbatim complaints to draw fifty rows would
	// be sending the whole steering history to render a table.
	row := listed.Items[0]
	if row.Summary == long {
		t.Errorf("row summary is the whole text: %q", row.Summary)
	}
	if len(row.Summary) > 240 || strings.Contains(row.Summary, "\n") {
		t.Errorf("row summary = %q, want one bounded line", row.Summary)
	}
	if row.Sequence != 1 || row.By != operatorID || row.Host != hostUnderTest || row.RootID != row.ID {
		t.Errorf("row = %+v, want an original attributed to the session's operator and this machine", row)
	}
	// The amended head names the wording it replaced; an original names none,
	// and the field is absent rather than empty so a client cannot render
	// "supersedes nothing" as a superseded record.
	if row.Supersedes != "" {
		t.Errorf("an original reports supersedes = %q", row.Supersedes)
	}
	for _, item := range listed.Items {
		if item.ID == h.amended.ID && item.Supersedes != h.superseded.ID {
			t.Errorf("amended head = %+v, want it to name the wording it replaced", item)
		}
	}
}

// TestComplaintListingIsAnArrayWhenNothingWasTold pins the empty case on the
// wire. A client that had to tell a null from an empty list would be
// distinguishing two renderings of the same fact, and "nothing has been told
// yet" is a page state rather than an error.
func TestComplaintListingIsAnArrayWhenNothingWasTold(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) {
		empty, err := complaint.Open(t.TempDir())
		if err != nil {
			t.Fatalf("complaint.Open: %v", err)
		}
		t.Cleanup(func() { empty.Close() })
		opts.Complaints = empty
	})
	raw := body(t, h.get("/api/complaints"))
	if !strings.Contains(raw, `"items":[]`) || !strings.Contains(raw, `"total":0`) {
		t.Errorf("empty listing = %s, want an empty array and a zero total", raw)
	}
}

// TestComplaintReadReturnsTheWholeChain is #87's rule on this record: amending
// appends, so an earlier wording stays readable at its own identifier and the
// chain is what says which of them is current.
func TestComplaintReadReturnsTheWholeChain(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var head complaintDetail
	answered(t, h.get("/api/complaint?id="+h.amended.ID), &head)
	if head.HeadID != h.amended.ID || !head.Complaint.Head {
		t.Fatalf("head read = %+v, want the current wording reporting itself as the head", head)
	}
	if len(head.Revisions) != 2 {
		t.Fatalf("chain = %d entries, want the two wordings", len(head.Revisions))
	}
	// Oldest first, as the store gives it, neither re-sorted nor filtered: a
	// history a surface reordered is one nobody could audit against the store.
	if head.Revisions[0].ID != h.superseded.ID || head.Revisions[1].ID != h.amended.ID {
		t.Errorf("chain = %+v, want the superseded wording first", head.Revisions)
	}
	if head.Revisions[0].Sequence != 1 || head.Revisions[1].Sequence != 2 {
		t.Errorf("chain sequences = %d, %d, want 1, 2", head.Revisions[0].Sequence, head.Revisions[1].Sequence)
	}
	heads := 0
	for _, entry := range head.Revisions {
		if entry.Head {
			heads++
		}
		if entry.Summary == "" {
			t.Errorf("chain entry %s carries no summary", entry.ID)
		}
	}
	if heads != 1 {
		t.Errorf("chain reports %d heads, want exactly one", heads)
	}

	// The superseded wording opens at its own id, with its own text, and
	// reports the chain's head rather than itself. A surface that redirected
	// to the head would make a citation of this wording point at something the
	// citing record never saw.
	var older complaintDetail
	answered(t, h.get("/api/complaint?id="+h.superseded.ID), &older)
	if older.Complaint.ID != h.superseded.ID || older.Complaint.Text != h.superseded.Text {
		t.Fatalf("superseded read = %+v, want the wording asked for", older.Complaint)
	}
	if older.Complaint.Head {
		t.Errorf("a superseded wording reports itself as the head")
	}
	if older.HeadID != h.amended.ID {
		t.Errorf("head_id = %q, want the chain's current wording %q", older.HeadID, h.amended.ID)
	}
	// The full text travels on the record and never in the chain: any
	// wording's whole text is read by opening that wording's own id.
	if strings.Contains(body(t, h.get("/api/complaint?id="+h.amended.ID)), `"text":"`+h.superseded.Text+`"`) {
		t.Errorf("the chain carries a superseded wording's full text")
	}
}

// TestComplaintCaptureRecordsWhoSaidItAndWhere is the capture's durable half.
// The response is not the proof — a handler that answered without writing would
// return the same document — so the record is read back through the store.
func TestComplaintCaptureRecordsWhoSaidItAndWhere(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	const said = "I am having a hard time enforcing my repository rules"

	var captured captureResult
	generic := answered(t, h.post("/api/complaint/tell", `{"text":`+mustJSON(t, said)+`}`), &captured)

	if captured.Complaint.Text != said {
		t.Errorf("captured text = %q, want it verbatim", captured.Complaint.Text)
	}
	if !captured.Complaint.Head || captured.Complaint.Sequence != 1 ||
		captured.Complaint.RootID != captured.Complaint.ID {
		t.Errorf("captured = %+v, want an original that is its own chain's head", captured.Complaint)
	}
	// The fixed sentence, identical every time. It is what stops a reader of
	// this response assuming something was opened, assigned or scheduled.
	if captured.Steering != captureOpenedNothing {
		t.Errorf("steering = %q, want the fixed sentence", captured.Steering)
	}
	if _, ok := generic["adjacent"].([]any); !ok {
		t.Errorf("adjacent = %v, want an array", generic["adjacent"])
	}

	durable, err := h.complaints.Complaint(h.ctx, captured.Complaint.ID)
	if err != nil {
		t.Fatalf("Complaint: %v", err)
	}
	if durable.By != operatorID {
		t.Errorf("durable complaint by = %q, want the session's operator", durable.By)
	}
	if durable.Host != hostUnderTest {
		t.Errorf("durable complaint host = %q, want the machine the launch named", durable.Host)
	}
	if durable.Text != said {
		t.Errorf("durable complaint text = %q, want what was posted", durable.Text)
	}
}

// TestComplaintCaptureRefusesAnUnattributableOperator is §4.7's attribution
// rule at this boundary. The durable state is compared before and after,
// because a refusal that had already written would be indistinguishable from
// success in the status alone.
func TestComplaintCaptureRefusesAnUnattributableOperator(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Operator = "" })
	before := h.heads()

	if got := statusOf(h.post("/api/complaint/tell", `{"text":"the rules keep getting ignored"}`)); got != http.StatusConflict {
		t.Errorf("anonymous capture status = %d, want 409", got)
	}
	if after := h.heads(); after != before {
		t.Errorf("an anonymous capture wrote: %d complaints before, %d after", before, after)
	}

	// The same request with no session at all is the middleware's refusal and
	// must also write nothing: the session check happens before the service
	// call rather than beside it.
	authorized := newPhaseB(t, "plain", nil)
	unauthenticated, err := http.NewRequest(http.MethodPost,
		authorized.http.URL+"/api/complaint/tell", strings.NewReader(`{"text":"the rules keep getting ignored"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := authorized.http.Client().Do(unauthenticated)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(response); got != http.StatusUnauthorized {
		t.Errorf("unauthenticated capture status = %d, want 401", got)
	}
	if after := authorized.heads(); after != 2 {
		t.Errorf("an unauthenticated capture wrote: %d complaints, want the two fixtures", after)
	}
}

// heads counts what the operator currently says, which is the cheapest durable
// fact a refusal assertion can compare across a request.
func (h *phaseB) heads() int {
	h.t.Helper()
	heads, err := h.complaints.Heads(h.ctx)
	if err != nil {
		h.t.Fatalf("Heads: %v", err)
	}
	return len(heads)
}

// TestComplaintCaptureRefusesWithoutAHostIdentity is the other half of
// provenance. complaint.TellInput.Host answers "where was I when I said this",
// and a surface that filled in "web" would be recording where a sentence was
// said without knowing it.
func TestComplaintCaptureRefusesWithoutAHostIdentity(t *testing.T) {
	h := newPhaseB(t, "plain", withoutHost)
	before := h.heads()
	if got := statusOf(h.post("/api/complaint/tell", `{"text":"the rules keep getting ignored"}`)); got != http.StatusConflict {
		t.Errorf("hostless capture status = %d, want 409", got)
	}
	if after := h.heads(); after != before {
		t.Errorf("a hostless capture wrote: %d complaints before, %d after", before, after)
	}
	// The two reads still answer. A machine that cannot name itself can still
	// show what it holds; only the write that would record a false provenance
	// is refused.
	if got := statusOf(h.get("/api/complaints")); got != http.StatusOK {
		t.Errorf("listing status without a host = %d, want 200", got)
	}
}

// TestComplaintCaptureReportsAdjacentPriorMaterial is the capture-time
// retrieval pass: what Babel already holds touching what was just said.
//
// It is a prompt to compare and never a claim of sameness, which is why the
// assertions are about what a row carries — a kind, an identifier and a bounded
// line — and about what it must never carry.
func TestComplaintCaptureReportsAdjacentPriorMaterial(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	h.indexFrontier()

	var captured captureResult
	generic := answered(t, h.post("/api/complaint/tell",
		`{"text":"verification keeps being reported rather than performed"}`), &captured)

	if captured.AdjacencyNote != "" {
		t.Fatalf("adjacency note = %q, want none: the pass ran", captured.AdjacencyNote)
	}
	if len(captured.Adjacent) == 0 {
		t.Fatalf("adjacent = %+v, want the prior material this text matches", captured.Adjacent)
	}
	kinds := map[string]bool{}
	for _, row := range captured.Adjacent {
		if row.ID == "" || row.Summary == "" || row.Kind == "" {
			t.Errorf("adjacent row = %+v, want a kind, an identifier and a line", row)
		}
		kinds[row.Kind] = true
	}
	// Both corpora Babel's own output spans: a record a run minted, and an
	// earlier thing the operator said. A pass that only found one of them
	// would be searching half the question.
	if !kinds["hypothesis"] || !kinds["complaint"] {
		t.Errorf("adjacent kinds = %v, want the frontier's records and the operator's own", kinds)
	}

	// No score, ever. §5.4 keeps retrieval rank out of anything a reader could
	// mistake for evidence strength, and this list is read at exactly the
	// moment somebody is deciding whether Babel already covered them. Asserted
	// on the raw keys, because a score absent from the Go type but present in
	// the JSON is the failure this is about.
	rows, ok := generic["adjacent"].([]any)
	if !ok {
		t.Fatalf("adjacent = %v, want an array", generic["adjacent"])
	}
	for _, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("adjacent row = %v, want an object", row)
		}
		for key := range object {
			if key != "kind" && key != "id" && key != "summary" {
				t.Errorf("adjacent row carries %q; a capture reports what Babel has, not how well it ranked", key)
			}
		}
	}
}

// TestComplaintCaptureExcludesItsOwnChain is asserted against the pass rather
// than through the route, and deliberately so: a capture mints a record the
// index has never seen, so the route can never produce a self-match to filter.
// The case the filter exists for is an amendment, whose chain is indexed under
// the wording it replaced — and matching yourself is not prior material.
func TestComplaintCaptureExcludesItsOwnChain(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	h.indexFrontier()

	rows, note := h.server.captureAdjacency(h.ctx, h.amended)
	if note != "" {
		t.Fatalf("note = %q, want none: the pass ran", note)
	}
	if len(rows) == 0 {
		t.Fatalf("adjacent = %+v, want the material this wording matches", rows)
	}
	for _, row := range rows {
		if row.ID == h.amended.ID || row.ID == h.superseded.ID {
			t.Errorf("adjacent row %+v is the complaint's own chain", row)
		}
	}
}

// TestComplaintCaptureBoundsItsAdjacency keeps the answer a screenful. The list
// settles "does Babel already have something touching this" from its first few
// rows; a longer one would turn the answer into a search result to work through
// at the moment the operator was trying to say one thing and move on.
func TestComplaintCaptureBoundsItsAdjacency(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	// A rare token so the match is exactly these and the bound is what cuts
	// the list rather than the corpus happening to be small.
	for i := range maxCaptureAdjacency + 2 {
		h.tell(fmt.Sprintf("quibbleflux is broken again, occurrence %d", i))
	}
	h.indexFrontier()

	var captured captureResult
	answered(t, h.post("/api/complaint/tell", `{"text":"quibbleflux"}`), &captured)
	if len(captured.Adjacent) != maxCaptureAdjacency {
		t.Errorf("adjacent = %d rows, want the %d-row bound", len(captured.Adjacent), maxCaptureAdjacency)
	}
}

// TestComplaintCaptureSurvivesAMissingRetrievalIndex is the rule that makes the
// pass safe to run at all: the complaint is durable before the search happens,
// so a rebuildable cache that is absent costs a note and never the capture.
func TestComplaintCaptureSurvivesAMissingRetrievalIndex(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Search = nil })

	var captured captureResult
	generic := answered(t, h.post("/api/complaint/tell", `{"text":"the rules keep getting ignored"}`), &captured)
	if captured.Complaint.ID == "" {
		t.Fatalf("capture = %+v, want the record", captured)
	}
	if captured.AdjacencyNote == "" {
		t.Errorf("adjacency note is absent; a pass that could not run is not a pass that matched nothing")
	}
	// Still an array, so a client renders "nothing found" from the same shape
	// it always does and reads the note beside it.
	if rows, ok := generic["adjacent"].([]any); !ok || len(rows) != 0 {
		t.Errorf("adjacent = %v, want an empty array", generic["adjacent"])
	}
	if _, err := h.complaints.Complaint(h.ctx, captured.Complaint.ID); err != nil {
		t.Errorf("the complaint is not durable: %v", err)
	}
}

// TestComplaintRoutesRefuseWhatTheyCannotAccept is the refusal set, each one
// travelling from the rule that owns it: the store's three validations, the
// decoder's unknown-field rule, and the wiring check.
func TestComplaintRoutesRefuseWhatTheyCannotAccept(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, tc := range []struct {
		name string
		body string
	}{
		// internal/complaint's own refusals. The handler restates none of
		// them, so each of these had to travel from the store to a status.
		{"empty text", `{"text":""}`},
		{"whitespace only", `{"text":"   \n  "}`},
		{"past the bound", `{"text":` + mustJSON(t, strings.Repeat("a", complaint.MaxTextBytes+1)) + `}`},
		// A misspelled field is refused rather than ignored: an append-only
		// record cannot be corrected afterwards, so an operator who believed
		// they had said something the record does not contain has no recourse.
		{"unknown field", `{"text":"the rules are ignored","about":"hyp-1"}`},
		{"not an object", `["the rules are ignored"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := h.heads()
			if got := statusOf(h.post("/api/complaint/tell", tc.body)); got != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", got)
			}
			if after := h.heads(); after != before {
				t.Errorf("a refused capture wrote: %d complaints before, %d after", before, after)
			}
		})
	}

	// An identifier this host holds no complaint for. Nothing here is ever
	// deleted, so it always means the reference was wrong rather than that the
	// complaint went away.
	if got := statusOf(h.get("/api/complaint?id=cmp-404")); got != http.StatusNotFound {
		t.Errorf("unknown complaint status = %d, want 404", got)
	}
	if got := statusOf(h.get("/api/complaint")); got != http.StatusBadRequest {
		t.Errorf("missing id status = %d, want 400", got)
	}

	// A build whose complaint component did not open. All three routes report
	// it and every other page keeps answering, which is the degradation a nil
	// service gets everywhere on this surface.
	unwired := newPhaseB(t, "plain", func(opts *Options) { opts.Complaints = nil })
	for _, probe := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/complaints", ""},
		{http.MethodGet, "/api/complaint?id=cmp-1", ""},
		{http.MethodPost, "/api/complaint/tell", `{"text":"the rules are ignored"}`},
	} {
		var response *http.Response
		if probe.method == http.MethodPost {
			response = unwired.post(probe.path, probe.body)
		} else {
			response = unwired.get(probe.path)
		}
		if got := statusOf(response); got != http.StatusConflict {
			t.Errorf("%s %s without the service = %d, want 409", probe.method, probe.path, got)
		}
	}
}

// TestComplaintSurfaceCarriesNoLifecycleField is #115's charter guard written
// as an assertion rather than as a promise.
//
// A complaint is steering pressure and never a ticket, so the moment one of
// these keys appears anywhere in these documents Babel has become a work
// tracker and GitHub already is one. The check walks the decoded JSON at every
// depth rather than reading the Go types, because the failure it prevents is a
// field arriving on the wire — from an embedded struct, a nested view, or a
// service value rendered straight through.
func TestComplaintSurfaceCarriesNoLifecycleField(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	h.indexFrontier()

	documents := map[string]map[string]any{
		"listing": answered(t, h.get("/api/complaints"), nil),
		"record":  answered(t, h.get("/api/complaint?id="+h.amended.ID), nil),
		"capture": answered(t, h.post("/api/complaint/tell",
			`{"text":"verification is reported rather than performed"}`), nil),
	}
	for name, document := range documents {
		walkForLifecycleKeys(t, name, document)
	}
}

// lifecycleKeys are the fields a work tracker has and a complaint does not.
// "Was this addressed?" is the cited_by direction of the citation graph and
// nothing else.
var lifecycleKeys = []string{
	"status", "state", "closed", "resolved", "resolved_at", "closed_at",
	"assignee", "priority", "due", "acknowledged",
}

func walkForLifecycleKeys(t *testing.T, where string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			for _, forbidden := range lifecycleKeys {
				if key == forbidden {
					t.Errorf("%s carries %q at %s: a complaint has no lifecycle to report", where, key, where)
				}
			}
			walkForLifecycleKeys(t, where+"."+key, nested)
		}
	case []any:
		for i, nested := range typed {
			walkForLifecycleKeys(t, fmt.Sprintf("%s[%d]", where, i), nested)
		}
	}
}
