package explore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// servedSourceID names the fixture session these tests search.
const servedSourceID = "omp-served"

// servedProbe is the marker inside the planted credential. It says what it is
// so nobody mistakes the fixture for a leak, and it doubles as the search term
// that reaches the planted record.
const servedProbe = "PROBEONLYNOTREALSERVED"

// servedFixture is a session written for the three properties serving has that
// retrieval alone does not: a page wider than what may be served, a record
// longer than what may be excerpted, and a credential that must not cross the
// boundary intact.
//
// Fourteen records hold the word "gadget", which is four more than one served
// page. One of them is far longer than the excerpt bound. One holds a private
// key block, whose armour is assembled from parts rather than written out —
// a contiguous literal in a documented credential format makes the forge reject
// every push carrying the file, which is a remote failure with no local signal.
func servedFixture() string {
	var b strings.Builder
	b.WriteString(`{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000f002",` +
		`"timestamp":"2026-06-01T00:00:00.000Z","cwd":"/synthetic/served","title":"synthetic serving fixture"}` + "\n")

	record := func(id, parent, minute, text string) {
		parentJSON := "null"
		if parent != "" {
			parentJSON = `"` + parent + `"`
		}
		fmt.Fprintf(&b, `{"type":"message","id":%q,"parentId":%s,`+
			`"timestamp":"2026-06-01T00:%s:00.000Z",`+
			`"message":{"role":"user","content":[{"type":"text","text":%q}]}}`+"\n",
			id, parentJSON, minute, text)
	}

	prev := ""
	for i := range 12 {
		id := fmt.Sprintf("s%04d", i+1)
		record(id, prev, fmt.Sprintf("%02d", i+1), fmt.Sprintf("record %d mentions the gadget in passing", i+1))
		prev = id
	}
	// The oversized record: longer than the excerpt bound and longer than
	// what the indexer itself retains, so serving clips text the index
	// already clipped once.
	record("s0013", prev, "13", "the gadget appears in a wall of output: "+strings.Repeat("x", 6000))
	armour := "-----BEGIN" + " RSA PRIVATE KEY-----\n" + servedProbe + "\n-----END" + " RSA PRIVATE KEY-----"
	record("s0014", "s0013", "14", "the gadget config carries a key:\n"+armour)
	return b.String()
}

// newServedBroker builds the production evidence broker over servedFixture,
// with the disclosure class the caller wants to observe.
func newServedBroker(t *testing.T, redact bool) *retrieval {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "served-fixture.jsonl")
	if err := os.WriteFile(path, []byte(servedFixture()), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	idx, err := index.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	stream := event.Stream{
		Harness:       event.HarnessOMP,
		AdapterSchema: 1,
		SourceID:      servedSourceID,
		Path:          path,
	}
	if _, err := idx.IndexSession(context.Background(), stream); err != nil {
		t.Fatalf("index fixture: %v", err)
	}
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return &retrieval{
		index:      idx,
		harnesses:  []string{event.HarnessOMP},
		sourceIDs:  []string{servedSourceID},
		redact:     redact,
		thresholds: preflight.DefaultThresholds(),
		now: func() time.Time {
			at = at.Add(time.Second)
			return at
		},
	}
}

// servedResults decodes the payload one decision carried. A decision with no
// payload fails the test rather than decoding into a zero value: absent results
// and empty results are different facts, and a helper that flattened them would
// hide the one this package exists to fix.
func servedResults(t *testing.T, decision worker.Decision) SearchResults {
	t.Helper()
	if !decision.Allow {
		t.Fatalf("decision = %+v, want the search served", decision)
	}
	if len(decision.Results) == 0 {
		t.Fatal("the decision carried no results payload; this is the defect itself — evidence computed, redacted, recorded and then discarded")
	}
	var results SearchResults
	if err := json.Unmarshal(decision.Results, &results); err != nil {
		t.Fatalf("the served payload is not a SearchResults document: %v", err)
	}
	return results
}

// TestServedPayloadCarriesWhatMakesAHitCitable is the contract one hit is held
// to. A model reads the excerpt and writes an observation; a human reopens that
// observation months later by following the locator into the archive. §9
// requires every claim to be reopenable, so a hit missing either half is not
// weaker evidence — it is evidence that cannot be used.
func TestServedPayloadCarriesWhatMakesAHitCitable(t *testing.T) {
	r := newServedBroker(t, false)
	results := servedResults(t, r.search(t, SearchRequest{Query: "gadget", Limit: 1}))

	if results.Schema != SearchResultsSchema {
		t.Errorf("payload schema = %q, want %q", results.Schema, SearchResultsSchema)
	}
	if results.Query != "gadget" {
		t.Errorf("payload query = %q, want the query as issued", results.Query)
	}
	if len(results.Hits) != 1 {
		t.Fatalf("payload carries %d hits, want the one the request asked for", len(results.Hits))
	}
	hit := results.Hits[0]
	if hit.Harness != event.HarnessOMP || hit.SourceID != servedSourceID {
		t.Errorf("hit identity = %s/%s, want the fixture session", hit.Harness, hit.SourceID)
	}
	if hit.Index == 0 {
		t.Error("hit carries no position in its session, so hits cannot be put back in conversation order")
	}
	if hit.Kind == "" {
		t.Error("hit carries no event kind, so an observation drawn from it cannot say whether it quotes a user, an agent or a tool")
	}
	if hit.Excerpt == "" {
		t.Error("hit carries no excerpt; a model with nothing to read forms no observation")
	}
	if hit.Locator.Path == "" || hit.Locator.Line < 1 || len(hit.Locator.Digest) != 64 {
		t.Fatalf("hit locator = %+v, want a path, a 1-based line and a record digest", hit.Locator)
	}

	// The locator is the authority, and the proof is that it reopens the
	// record on its own — no index, no broker, nothing but the archive.
	recovered, err := os.ReadFile(hit.Locator.Path)
	if err != nil {
		t.Fatalf("reopen the hit's session from its locator: %v", err)
	}
	lines := strings.Split(string(recovered), "\n")
	if hit.Locator.Line > len(lines) {
		t.Fatalf("locator line %d is past the end of a %d line log", hit.Locator.Line, len(lines))
	}
	if line := lines[hit.Locator.Line-1]; !strings.Contains(line, "gadget") {
		t.Errorf("locator line %d = %q, want the record the hit was drawn from", hit.Locator.Line, line)
	}
	if int64(len(recovered)) <= hit.Locator.ByteOffset {
		t.Errorf("locator byte offset %d is past the end of a %d byte log",
			hit.Locator.ByteOffset, len(recovered))
	}

	encoded, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatalf("re-encode the payload: %v", err)
	}
	t.Logf("one served hit on the wire:\n%s", encoded)
}

// TestServedPageAndExcerptAreBoundedAndSaySo covers the two bounds serving adds
// on top of the index's own, and the reason each is visible rather than silent.
//
// A hit now leaves the process, so index.DefaultLimit's fifty and
// index.MaxLimit's five hundred are the wrong sizes: both were chosen for a
// page consumed in-process. The applied bound travels in the payload because
// without it a short page cannot be told from the end of the matches.
func TestServedPageAndExcerptAreBoundedAndSaySo(t *testing.T) {
	r := newServedBroker(t, false)

	for _, asked := range []int{0, index.DefaultLimit, index.MaxLimit} {
		results := servedResults(t, r.search(t, SearchRequest{Query: "gadget", Limit: asked}))
		if len(results.Hits) != maxServedHits {
			t.Errorf("a request for a page of %d was served %d hits, want the served bound of %d",
				asked, len(results.Hits), maxServedHits)
		}
		if results.Limit != maxServedHits {
			t.Errorf("a request for a page of %d reports limit %d, want the bound Babel applied (%d); "+
				"without it the worker reads a narrowed page as the end of the corpus",
				asked, results.Limit, maxServedHits)
		}
	}

	// A page the index itself would refuse stays refused. Rounding nonsense
	// into a valid request is how a reduced page starts looking like an
	// answer about the corpus.
	if decision := r.search(t, SearchRequest{Query: "gadget", Limit: index.MaxLimit + 1}); decision.Allow {
		t.Errorf("decision = %+v, want a page past the index ceiling refused", decision)
	}

	// The oversized record: clipped, flagged, and still reopenable whole.
	results := servedResults(t, r.search(t, SearchRequest{Query: `"wall of output"`}))
	if len(results.Hits) == 0 {
		t.Fatal("the oversized record was not retrieved, so nothing here is bounded")
	}
	var clipped *SearchHit
	for i, hit := range results.Hits {
		if len(hit.Excerpt) > maxServedExcerptBytes {
			t.Errorf("hit %d serves a %d byte excerpt, past the %d byte bound",
				i, len(hit.Excerpt), maxServedExcerptBytes)
		}
		if hit.Truncated {
			clipped = &results.Hits[i]
		}
	}
	if clipped == nil {
		t.Fatalf("no served hit is flagged truncated, so a model reading a clipped excerpt cannot tell it is reading part of a record: %+v", results.Hits)
	}
	if len(clipped.Excerpt) != maxServedExcerptBytes {
		t.Errorf("the clipped excerpt is %d bytes, want the bound of %d", len(clipped.Excerpt), maxServedExcerptBytes)
	}
	if clipped.Locator.Path == "" || len(clipped.Locator.Digest) != 64 {
		t.Errorf("clipping destroyed the locator back to the whole record: %+v", clipped.Locator)
	}
}

// TestRedactionSurvivesOntoTheWire is the disclosure boundary. Redaction
// already ran before the hits were discarded; now that they travel, the thing
// worth testing is that no path from an index.Hit to the pipe skips it.
//
// The test proves it is not vacuous by running the identical fixture and query
// with redaction off. If the planted credential appears there and not here, the
// difference is the redactor rather than a pattern nothing detects — and the
// unredacted run is not a leak but §3's local-disclosure case, where evidence
// keeps its bytes because nothing leaves the machine.
//
// The query reaches the planted record without naming the credential, so the
// whole payload can be searched for it. A query that was the secret would put
// it in the payload's own "query" field, which discloses nothing — the worker
// wrote it — but would make the strong check unusable.
func TestRedactionSurvivesOntoTheWire(t *testing.T) {
	query := SearchRequest{Query: `"config carries a key"`}

	local := servedResults(t, newServedBroker(t, false).search(t, query))
	if len(local.Hits) == 0 {
		t.Fatal("the planted credential was not retrievable at all, so this test could not distinguish redaction from an empty result")
	}
	planted := false
	for _, hit := range local.Hits {
		if strings.Contains(hit.Excerpt, servedProbe) {
			planted = true
		}
	}
	if !planted {
		t.Fatal("no served excerpt carries the planted credential even with redaction off; the fixture, not the redactor, is what this test would be measuring")
	}

	hosted := servedResults(t, newServedBroker(t, true).search(t, query))
	if len(hosted.Hits) != len(local.Hits) {
		t.Errorf("redaction changed the result set: %d hits against %d", len(hosted.Hits), len(local.Hits))
	}
	if len(hosted.Hits) == 0 {
		t.Fatal("the redacted run served no hits")
	}
	// The whole payload is searched, not the excerpts alone. A credential
	// that reached any other field would be disclosure just the same.
	if wire := string(newServedBrokerWire(t, hosted)); strings.Contains(wire, servedProbe) {
		t.Errorf("the planted credential reached the wire verbatim: %s", wire)
	}
	substituted := false
	for _, hit := range hosted.Hits {
		if strings.Contains(hit.Excerpt, preflight.Placeholder(servedProbe)) ||
			strings.Contains(hit.Excerpt, "babel-redacted") {
			substituted = true
		}
		// Redaction replaces the credential, not the path back to it: local
		// evidence has to recover exactly what was hidden (§3).
		if hit.Locator.Path == "" || len(hit.Locator.Digest) != 64 {
			t.Errorf("redaction destroyed the locator back to the original bytes: %+v", hit.Locator)
		}
	}
	if !substituted {
		t.Error("no served excerpt carries a redaction placeholder; a recurring credential must stay visible as a recurrence without being visible as a value")
	}
}

// newServedBrokerWire re-encodes a decoded payload so a search covers every
// field it carries rather than the ones a reader happened to name.
func newServedBrokerWire(t *testing.T, results SearchResults) []byte {
	t.Helper()
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("re-encode the served payload: %v", err)
	}
	return encoded
}

// TestZeroHitsStillServeAPayload is the distinction a worker reports as two
// different gaps. Absent results means Babel brokered no evidence at all — an
// older build, a denial, a capability nothing serves. An empty hits array means
// Babel looked and the corpus is silent. Collapsing them is how "I was told
// nothing" and "there is nothing" become the same sentence.
func TestZeroHitsStillServeAPayload(t *testing.T) {
	r := newServedBroker(t, false)
	decision := r.search(t, SearchRequest{Query: "marsupial photosynthesis tuba"})
	results := servedResults(t, decision)
	if results.Hits == nil {
		t.Error("the payload omits the hits array on a query that matched nothing; an absent array is the shape reserved for no payload at all")
	}
	if len(results.Hits) != 0 {
		t.Errorf("a query nothing matches served %d hits", len(results.Hits))
	}
	if !strings.Contains(string(decision.Results), `"hits":[]`) {
		t.Errorf("the encoded payload does not carry an empty hits array: %s", decision.Results)
	}

	// A query with nothing a tokenizer can match is the same fact about the
	// corpus, and it is served rather than refused.
	results = servedResults(t, r.search(t, SearchRequest{Query: "*** --- ///"}))
	if len(results.Hits) != 0 {
		t.Errorf("an unmatched query served %d hits", len(results.Hits))
	}
}

// TestTheTraceNeverCarriesAServedExcerpt is the §9 split at the point it is
// made. One function produces both outputs, so the guard belongs to it: the
// payload carries the excerpt, the trace carries the locator, and nothing the
// trace records may quote what the payload sent.
//
// The proof that it is not vacuous is in this test rather than beside it. A
// second trace is recorded with an excerpt deliberately written into the
// evidence note, and the same check has to catch it.
func TestTheTraceNeverCarriesAServedExcerpt(t *testing.T) {
	r := newServedBroker(t, false)
	results := servedResults(t, r.search(t, SearchRequest{Query: "gadget"}))
	steps, served := r.trace()
	if len(steps) != 1 || len(served) != 1 {
		t.Fatalf("recorded %d steps and %d served retrievals, want 1 of each", len(steps), len(served))
	}
	if len(results.Hits) == 0 {
		t.Fatal("nothing was served, so no excerpt exists to look for")
	}

	excerpts := make([]string, 0, len(results.Hits))
	for _, hit := range results.Hits {
		excerpts = append(excerpts, hit.Excerpt)
	}
	if found := quotedExcerpts(t, steps, excerpts); len(found) != 0 {
		t.Errorf("the retrieval trace quotes served excerpts %q; the trace reaches the receipt an operator exports, and §9 forbids it becoming a plaintext store of archive content",
			found)
	}
	// Every step still recovers its bytes: the trace gives up the text and
	// keeps the only thing that reopens the record.
	for _, result := range steps[0].Results {
		locator := result.Evidence.Locator()
		if locator.Path == "" || len(locator.Digest) != 64 {
			t.Errorf("trace result %d cannot recover its bytes: %+v", result.Rank, locator)
		}
	}

	// Non-vacuity. The leak this guards against is a payload arriving in the
	// trace through the note beside a locator, which is the one field of a
	// step that takes free text about a hit. Planting it there must fail.
	leaked := poisonedTrace(t, steps[0], excerpts[0])
	if found := quotedExcerpts(t, leaked, excerpts); len(found) == 0 {
		t.Fatal("the check passed a trace with an excerpt written into it, so it proves nothing about the clean one")
	}
}

// quotedExcerpts reports which excerpts appear anywhere in the encoded trace.
// It encodes rather than reading named fields: a leak that only a reader's
// chosen field list covers is a leak the next field reintroduces.
func quotedExcerpts(t *testing.T, steps []run.RetrievalStep, excerpts []string) []string {
	t.Helper()
	encoded, err := json.Marshal(steps)
	if err != nil {
		t.Fatalf("encode the retrieval trace: %v", err)
	}
	document := string(encoded)
	var found []string
	for _, excerpt := range excerpts {
		if excerpt != "" && strings.Contains(document, excerpt) {
			found = append(found, excerpt)
		}
	}
	return found
}

// poisonedTrace is one trace step with an excerpt written into the note beside
// its first locator: the leak quotedExcerpts must catch.
func poisonedTrace(t *testing.T, step run.RetrievalStep, excerpt string) []run.RetrievalStep {
	t.Helper()
	if len(step.Results) == 0 {
		t.Fatal("the step has no result to poison")
	}
	evidence, err := run.NewEvidence(step.Results[0].Evidence.Locator(), excerpt)
	if err != nil {
		t.Fatalf("build the poisoned evidence: %v", err)
	}
	poisoned := step
	poisoned.Results = []run.RetrievalResult{{Rank: 1, Evidence: evidence}}
	return []run.RetrievalStep{poisoned}
}
