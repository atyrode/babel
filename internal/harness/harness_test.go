package harness_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/harness"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/transcript"
)

// joined is a harness that exists nowhere in Babel but in this one
// registration: no adapter, no classifier, no parser, no validator case.
// It is registered at package initialization, as every harness registration
// belongs (SPEC.md §6.8), so the assertions below see the set an ordinary
// process would.
const joined = "fixture-harness"

var joinErr = harness.Register(harness.Harness{Name: joined, Format: harness.FormatOMP})

// sample is one user record per declared record language. A declared format
// with no sample fails the walk below rather than being skipped: a format
// this test cannot exercise is a format whose readers nothing proves exist.
var sample = map[harness.Format]string{
	harness.FormatOMP: `{"type":"message","id":"r1","timestamp":"2026-04-01T00:01:00Z",` +
		`"message":{"role":"user","content":[{"type":"text","text":"declared harness request"}]}}`,
	harness.FormatCodex: `{"timestamp":"2026-04-02T00:01:00Z","type":"response_item",` +
		`"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"declared harness request"}]}}`,
	harness.FormatClaude: `{"type":"user","timestamp":"2026-04-03T00:01:00Z",` +
		`"message":{"role":"user","content":"declared harness request"},"uuid":"u1"}`,
}

// TestDeclaredHarnessIsScannableParseableAndPreparable is the whole claim of
// the single declaration (SPEC.md §6.8).
//
// Adding Babel's own harness once took five unrelated edits — the scanner's
// classifier switch, the transcript view's parser switch, the preparation
// validator's name list, the CLI's adapter list, and the port's
// documentation — and a missed one failed at run time, not at build time:
// that is how `unknown harness "babel"` reached a running conductor cycle
// and ended it. So this walks every harness the declaration holds,
// including one registered only here, and requires all three readers to
// accept it. Against the five-place arrangement each reader carried its own
// list of names, and every assertion below fails for the registered
// harness.
func TestDeclaredHarnessIsScannableParseableAndPreparable(t *testing.T) {
	if joinErr != nil {
		t.Fatalf("register %s: %v", joined, joinErr)
	}

	registered := false
	for _, h := range harness.All() {
		if h.Name == joined {
			registered = true
		}
		t.Run(h.Name, func(t *testing.T) {
			record, ok := sample[h.Format]
			if !ok {
				t.Fatalf("harness %s declares format %q, which this test has no record of: "+
					"nothing proves a reader for it exists", h.Name, h.Format)
			}
			assertScannable(t, h.Name, record)
			assertParseable(t, h.Name, record)
			assertPreparable(t, h.Name)
		})
	}
	if !registered {
		t.Errorf("All() omitted %s, which was registered before any read", joined)
	}
}

// assertScannable requires the analysis scanner to classify the record
// rather than degrade it. An opaque event would mean the harness was
// accepted and then read by nobody, which is the failure the name check
// alone would not catch.
func assertScannable(t *testing.T, name, record string) {
	t.Helper()
	var got []event.Event
	err := event.Scan(strings.NewReader(record+"\n"), event.Stream{
		Harness:       name,
		AdapterSchema: 1,
		SourceID:      "session-0001",
		Path:          name + ".jsonl",
	}, func(e event.Event) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("event.Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("event.Scan produced %d events, want 1", len(got))
	}
	if got[0].Kind != event.KindUserReport {
		t.Errorf("event kind = %q, want %q: the record was accepted but not classified",
			got[0].Kind, event.KindUserReport)
	}
	if got[0].Locator.Digest == "" {
		t.Error("event carries no locator digest, so the record is unrecoverable from the archive")
	}
}

// assertParseable requires the session view to render the record as a
// message. A raw event is what an unreadable harness produces, so it is the
// observable signature of a missing display parser.
func assertParseable(t *testing.T, name, record string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".jsonl")
	if err := os.WriteFile(path, []byte(record+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	total, events, err := transcript.Events(path, name, 0, 10)
	if err != nil {
		t.Fatalf("transcript.Events: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("transcript.Events returned %d of %d events, want 1 of 1", len(events), total)
	}
	if events[0].Kind != "message" {
		t.Errorf("transcript kind = %q, want \"message\": the record rendered as raw JSON",
			events[0].Kind)
	}
	if events[0].Role != "user" {
		t.Errorf("transcript role = %q, want \"user\"", events[0].Role)
	}
}

// assertPreparable requires a preparation to accept a session of the
// harness. A run that cannot name the session is a harness Babel can read
// and never analyse.
func assertPreparable(t *testing.T, name string) {
	t.Helper()
	prepared, err := run.NewPreparation(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), []run.Selected{{
		Host:          "test-host",
		Harness:       name,
		SourceID:      "session-0001",
		CaptureDigest: digest.Bytes([]byte("capture")),
		SourceDigest:  digest.Bytes([]byte("source")),
		Adapter:       run.AdapterRef{Schema: 1, Version: "harness-test/1"},
	}}, run.PreparationContext{})
	if err != nil {
		t.Fatalf("run.NewPreparation: %v", err)
	}
	if len(prepared.Selection) != 1 || prepared.Selection[0].Harness != name {
		t.Errorf("preparation selection = %+v, want one entry for %s", prepared.Selection, name)
	}
}

// TestRegisterRefusesWhatWouldMakeTheSetAmbiguous covers the two conditions
// that would silently weaken the declaration: a second registration of a
// name, which would make the record language a harness speaks depend on
// registration order, and a format with no reader, which would produce a
// harness accepted everywhere and read nowhere.
func TestRegisterRefusesWhatWouldMakeTheSetAmbiguous(t *testing.T) {
	if err := harness.Register(harness.Harness{Name: harness.OMP, Format: harness.FormatCodex}); err == nil {
		t.Error("Register accepted a second declaration of omp")
	}
	if _, ok := harness.Lookup(harness.OMP); !ok {
		t.Fatal("the refused duplicate removed omp from the set")
	}
	if h, _ := harness.Lookup(harness.OMP); h.Format != harness.FormatOMP {
		t.Errorf("omp format = %q after a refused duplicate, want %q", h.Format, harness.FormatOMP)
	}
	if err := harness.Register(harness.Harness{Name: "gemini", Format: "gemini"}); err == nil {
		t.Error("Register accepted a harness whose record language no reader is written against")
	}
	if _, ok := harness.Lookup("gemini"); ok {
		t.Error("a refused registration joined the set anyway")
	}
}
