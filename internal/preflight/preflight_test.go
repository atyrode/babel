package preflight

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/worker"
)

// ompLog writes an OMP primary log whose records are user messages carrying
// texts, one record each, and returns the Input naming it. Building the
// records with encoding/json is deliberate: a fixture that hand-rolled JSONL
// would test the fixture's escaping rather than the preflight.
func ompLog(t *testing.T, dir, name, sourceID string, texts ...string) Input {
	t.Helper()
	var buf bytes.Buffer
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, text := range texts {
		rec := map[string]any{
			"type":      "message",
			"timestamp": at.Format(time.RFC3339),
			"message": map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		}
		encoded, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("encode fixture record: %v", err)
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	return Input{
		Stream: event.Stream{
			Harness:       event.HarnessOMP,
			AdapterSchema: 1,
			SourceID:      sourceID,
			Path:          path,
		},
		Digest: string(digest.Bytes(buf.Bytes())),
	}
}

// localRequest is a request under the local disclosure class, which is the
// class that must never require redaction.
func localRequest(inputs ...Input) Request {
	return Request{
		Profile:    worker.ProfileRef{ID: "probe-profile", Revision: 3},
		Disclosure: worker.DisclosureLocal,
		Inputs:     inputs,
	}
}

func mustCheck(t *testing.T, req Request) *Report {
	t.Helper()
	rep, err := Check(req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return rep
}

func findingsByCategory(rep *Report, c Category) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Category == c {
			out = append(out, f)
		}
	}
	return out
}

// TestCheckIsDeterministic is the property §6.4 rests on: the preflight is a
// function of its input. Two runs over the same corpus must produce byte-equal
// reports, including finding identities and order, and the order must not
// depend on the order the inputs were passed in — otherwise two preparations
// of the same sessions would disagree for no reason a reviewer could see.
func TestCheckIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	first := ompLog(t, dir, "log-a.jsonl", "omp/probe-a",
		"an ordinary operator message about a build",
		"the token "+probeVendorToken+" was pasted here",
		"and again "+probeVendorToken+" in a later turn")
	second := ompLog(t, dir, "log-b.jsonl", "omp/probe-b",
		"a message with a connection string postgres://probe-user:"+probeURLPassword+"@catalog.invalid:5432/babel")

	runOne := mustCheck(t, localRequest(first, second))
	runTwo := mustCheck(t, localRequest(first, second))
	if !reflect.DeepEqual(runOne, runTwo) {
		t.Fatalf("two runs over one corpus disagree:\n%+v\n%+v", runOne, runTwo)
	}

	reversed := mustCheck(t, localRequest(second, first))
	if !reflect.DeepEqual(runOne, reversed) {
		t.Errorf("report depends on the order inputs were given in:\n%+v\n%+v", runOne, reversed)
	}

	firstJSON, err := json.Marshal(runOne)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	secondJSON, err := json.Marshal(runTwo)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Error("serialized reports differ across runs")
	}

	// Non-vacuity: a determinism test over an empty finding set proves
	// nothing.
	if len(runOne.Findings) < 2 {
		t.Fatalf("fixture produced %d findings; the comparison needs several", len(runOne.Findings))
	}
	// The repeated token is one finding that recurred, not two findings.
	for _, f := range findingsByCategory(runOne, CategorySecret) {
		if f.Detector == "vendor-token" && f.Occurrences != 2 {
			t.Errorf("vendor token seen twice reported %d occurrences", f.Occurrences)
		}
	}
}

// TestReportCarriesNoSecretMaterial is the acceptance §3 and §9 both demand:
// the report is the artifact an operator reads, exports, and forwards, so a
// report that copied credentials out of the corpus would have moved the
// exposure rather than reported it.
//
// It scans the serialized bytes for every eight-byte window of every fixture
// credential rather than for the whole value. A finding that leaked a prefix,
// a suffix, or a "helpfully" masked middle would pass a whole-value search.
func TestReportCarriesNoSecretMaterial(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe", probeCorpus()...)
	rep := mustCheck(t, Request{
		Profile:    worker.ProfileRef{ID: "probe-profile", Revision: 1},
		Disclosure: worker.DisclosureHosted,
		Inputs:     []Input{in},
	})

	// One finding per credential-bearing record: the two halves of the armour
	// body are one block, so they are one finding rather than two.
	secrets := findingsByCategory(rep, CategorySecret)
	if want := len(probeCorpus()) - 1; len(secrets) != want {
		t.Fatalf("%d secret findings for %d credential-bearing records: %+v", len(secrets), want, secrets)
	}

	encoded, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	for name, value := range probeSecrets {
		for _, window := range windows(value, 8) {
			if bytes.Contains(encoded, []byte(window)) {
				t.Errorf("serialized report carries %q from the %s fixture", window, name)
				break
			}
		}
	}

	// The same guarantee on the in-memory findings, field by field: JSON is
	// how a report leaves the process, but a caller that renders findings
	// directly must be equally safe.
	for _, f := range secrets {
		fields := strings.Join([]string{f.ID, f.Detector, f.Summary, f.Placeholder, f.Reference,
			string(f.Category), string(f.Confidence), f.Evidence.Locator().Digest}, "\x00")
		for name, value := range probeSecrets {
			for _, window := range windows(value, 8) {
				if strings.Contains(fields, window) {
					t.Errorf("finding %s carries %q from the %s fixture", f.ID, window, name)
					break
				}
			}
		}
	}
}

// windows returns every substring of value of exactly n bytes.
func windows(value string, n int) []string {
	if len(value) < n {
		return []string{value}
	}
	out := make([]string, 0, len(value)-n+1)
	for i := 0; i+n <= len(value); i++ {
		out = append(out, value[i:i+n])
	}
	return out
}

// TestReportRoundTripsThroughJSON proves the report survives the wire it will
// travel: a preparation record, a receipt, a web response.
func TestReportRoundTripsThroughJSON(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe", probeCorpus()...)
	rep := mustCheck(t, Request{
		Profile:    worker.ProfileRef{ID: "probe-profile", Revision: 7},
		Disclosure: worker.DisclosureHosted,
		Inputs:     []Input{in},
	})

	encoded, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded report does not validate: %v", err)
	}
	if !reflect.DeepEqual(rep, &decoded) {
		t.Errorf("round trip changed the report:\n%+v\n%+v", rep, &decoded)
	}
	reencoded, err := json.Marshal(&decoded)
	if err != nil {
		t.Fatalf("re-marshal report: %v", err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Error("re-serialized report differs")
	}

	// The round trip is also where a serializer could helpfully "restore" a
	// value from somewhere, so the sentinel scan runs on the re-encoded bytes
	// too.
	for name, value := range probeSecrets {
		for _, window := range windows(value, 8) {
			if bytes.Contains(reencoded, []byte(window)) {
				t.Errorf("round-tripped report carries %q from the %s fixture", window, name)
				break
			}
		}
	}
}

// TestEvidenceCannotBeSeparatedFromItsLocator covers §4.3's rule at the
// boundary where it is easiest to lose: construction and decoding. An
// observation cannot exist without evidence, so neither can a preflight
// finding, and a report arriving from disk must not be able to smuggle one in.
func TestEvidenceCannotBeSeparatedFromItsLocator(t *testing.T) {
	valid := event.Locator{Path: "probe.jsonl", Line: 2, ByteOffset: 40, Digest: "abc"}
	tests := []struct {
		name     string
		harness  string
		sourceID string
		locator  event.Locator
		wantErr  bool
	}{
		{name: "complete", harness: "omp", sourceID: "omp/probe", locator: valid},
		{name: "no harness", sourceID: "omp/probe", locator: valid, wantErr: true},
		{name: "no source id", harness: "omp", locator: valid, wantErr: true},
		{
			name: "no path", harness: "omp", sourceID: "omp/probe",
			locator: event.Locator{Line: 2, Digest: "abc"}, wantErr: true,
		},
		{
			name: "no digest", harness: "omp", sourceID: "omp/probe",
			locator: event.Locator{Path: "probe.jsonl", Line: 2}, wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEvidence(test.harness, test.sourceID, 0, event.KindUserReport, test.locator)
			if test.wantErr && err == nil {
				t.Fatal("NewEvidence accepted evidence that cannot recover bytes")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("NewEvidence: %v", err)
			}
		})
	}

	t.Run("decoding refuses a locator-less finding", func(t *testing.T) {
		document := `{"version":1,"findings":[{"id":"x","category":"likely-secret",` +
			`"confidence":"structural","occurrences":1,` +
			`"evidence":{"harness":"omp","source_id":"omp/probe","event_index":0,"locator":{}}}]}`
		var rep Report
		if err := json.Unmarshal([]byte(document), &rep); err == nil {
			t.Fatal("decoded a report whose finding has no recoverable evidence")
		}
	})
}

// TestCheckRefusesAnInputItCannotEvidence: an input silently skipped would be
// reported as a clean input that was never read, which is worse than a failure.
func TestCheckRefusesAnInputItCannotEvidence(t *testing.T) {
	dir := t.TempDir()
	good := ompLog(t, dir, "log.jsonl", "omp/probe", "an ordinary message")

	tests := []struct {
		name  string
		mutar func(Input) Input
	}{
		{"no digest", func(in Input) Input { in.Digest = ""; return in }},
		{"no source id", func(in Input) Input { in.Stream.SourceID = ""; return in }},
		{"no harness", func(in Input) Input { in.Stream.Harness = ""; return in }},
		{"no path", func(in Input) Input { in.Stream.Path = ""; return in }},
		{"absent log", func(in Input) Input { in.Stream.Path += ".absent"; return in }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Check(localRequest(test.mutar(good))); err == nil {
				t.Fatal("Check accepted an input it cannot evidence")
			}
		})
	}

	t.Run("unknown disclosure class", func(t *testing.T) {
		req := localRequest(good)
		req.Disclosure = "somewhere-else"
		if _, err := Check(req); err == nil {
			t.Fatal("Check accepted an unknown disclosure class")
		}
	})
}

// TestDisclosureReportsRatherThanDecides covers §6.4's last check and §3's
// step 4. A hosted profile with a secret finding must be reported as requiring
// redaction, and must name the findings that force it; a local profile with
// the same corpus must not, because local evidence keeps its locators to the
// original.
func TestDisclosureReportsRatherThanDecides(t *testing.T) {
	dir := t.TempDir()
	withSecret := ompLog(t, dir, "secret.jsonl", "omp/probe-secret",
		"a message pasting "+probeVendorToken+" into the transcript")
	clean := ompLog(t, dir, "clean.jsonl", "omp/probe-clean",
		"an ordinary operator message about a build")

	tests := []struct {
		name        string
		disclosure  string
		input       Input
		wantForcing bool
	}{
		{"hosted with a secret", worker.DisclosureHosted, withSecret, true},
		{"hosted without a secret", worker.DisclosureHosted, clean, false},
		{"local with a secret", worker.DisclosureLocal, withSecret, false},
		{"local without a secret", worker.DisclosureLocal, clean, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rep := mustCheck(t, Request{
				Profile:    worker.ProfileRef{ID: "probe-profile", Revision: 2},
				Disclosure: test.disclosure,
				Inputs:     []Input{test.input},
			})
			if rep.Disclosure.Disclosure != test.disclosure {
				t.Errorf("reported class %q, want %q", rep.Disclosure.Disclosure, test.disclosure)
			}
			if rep.Disclosure.Profile.Revision != 2 {
				t.Errorf("profile revision %d not carried into the report", rep.Disclosure.Profile.Revision)
			}
			if got := rep.Disclosure.RedactionRequired; got != test.wantForcing {
				t.Errorf("redaction required = %v, want %v", got, test.wantForcing)
			}
			if got := len(rep.Disclosure.Forcing) > 0; got != test.wantForcing {
				t.Errorf("forcing findings listed = %v, want %v", got, test.wantForcing)
			}
			// A local run still finds the secret; what differs is whether the
			// class forces redaction before the run may proceed.
			if test.input.Stream.SourceID == "omp/probe-secret" &&
				len(findingsByCategory(rep, CategorySecret)) == 0 {
				t.Error("no secret finding, so the disclosure assertion is vacuous")
			}
			for _, id := range rep.Disclosure.Forcing {
				var found bool
				for _, f := range rep.Findings {
					if f.ID == id && f.Category == CategorySecret {
						found = true
					}
				}
				if !found {
					t.Errorf("forcing id %s is not a secret finding in the report", id)
				}
			}
		})
	}
}

// TestFindingCapIsReportedRatherThanHidden: the top percentile of a real
// corpus holds a large share of its bytes, so one pathological input could
// otherwise decide the size of the whole report. A truncated report must say
// so.
func TestFindingCapIsReportedRatherThanHidden(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe", probeCorpus()...)

	th := DefaultThresholds()
	th.MaxFindingsPerInput = 2
	req := localRequest(in)
	req.Thresholds = &th
	rep := mustCheck(t, req)

	if len(rep.Findings) != 2 {
		t.Fatalf("%d findings under a cap of 2", len(rep.Findings))
	}
	if rep.Stats.FindingsOmitted == 0 {
		t.Error("findings were dropped without being counted")
	}
	if rep.Thresholds.MaxFindingsPerInput != 2 {
		t.Error("the report does not carry the thresholds it applied")
	}
}

// TestThresholdsAreRefusedRatherThanCorrected: a non-positive size limit fires
// on everything and an impossible entropy floor fires on nothing, so either
// would make the report a lie rather than a stricter check.
func TestThresholdsAreRefusedRatherThanCorrected(t *testing.T) {
	dir := t.TempDir()
	in := ompLog(t, dir, "log.jsonl", "omp/probe", "an ordinary message")

	tests := []struct {
		name  string
		mutar func(Thresholds) Thresholds
	}{
		{"zero transcript limit", func(th Thresholds) Thresholds { th.TranscriptBytes = 0; return th }},
		{"negative record limit", func(th Thresholds) Thresholds { th.RecordBytes = -1; return th }},
		{"zero attachment limit", func(th Thresholds) Thresholds { th.AttachmentBytes = 0; return th }},
		{"zero finding cap", func(th Thresholds) Thresholds { th.MaxFindingsPerInput = 0; return th }},
		{"short entropy candidate", func(th Thresholds) Thresholds { th.EntropyMinLength = 4; return th }},
		{"impossible entropy floor", func(th Thresholds) Thresholds { th.EntropyMinBits = 9; return th }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			th := test.mutar(DefaultThresholds())
			req := localRequest(in)
			req.Thresholds = &th
			if _, err := Check(req); err == nil {
				t.Fatal("Check accepted thresholds that cannot describe a corpus")
			}
		})
	}
}
