package preflight

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/synth"
)

// synthInputs turns a generated corpus into the inputs a preparation would
// hand the preflight: every primary log, its digest, its artifact closure, and
// the references its adapter could not resolve.
func synthInputs(t *testing.T, c *synth.Corpus) []Input {
	t.Helper()
	inputs := make([]Input, 0, len(c.Sessions))
	for i, s := range c.Sessions {
		file, err := os.Open(s.Path)
		if err != nil {
			t.Fatalf("open generated log: %v", err)
		}
		d, _, err := digest.Compute(file)
		file.Close()
		if err != nil {
			t.Fatalf("digest generated log: %v", err)
		}
		in := Input{
			Stream: event.Stream{
				Harness:       s.Harness,
				AdapterSchema: 1,
				// A synthetic corpus has no adapter to compose identity, and
				// this test needs identities that are unique and stable, not
				// realistic.
				SourceID: s.Harness + "/" + strconv.Itoa(i),
				Path:     s.Path,
			},
			Digest: string(d),
		}
		for _, artifact := range s.Artifacts {
			info, err := os.Stat(artifact)
			if err != nil {
				t.Fatalf("stat generated artifact: %v", err)
			}
			in.Attachments = append(in.Attachments, Attachment{Path: artifact, Size: info.Size()})
		}
		for _, ref := range s.UnresolvedRefs {
			in.Unresolved = append(in.Unresolved, Reference{Ref: ref, Reason: "not present in the store"})
		}
		inputs = append(inputs, in)
	}
	return inputs
}

// TestCorpusHealthMatchesWhatSynthPlanted checks the counts against the
// generator's own plan rather than against a re-derivation of the tree. Every
// defect internal/synth plants is one internal/event degrades explicitly, and
// §6.4's malformed-and-truncated check is exactly the claim that those two
// numbers survive the trip.
func TestCorpusHealthMatchesWhatSynthPlanted(t *testing.T) {
	corpus, err := synth.Generate(t.TempDir(), synth.DefaultProfile())
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}
	inputs := synthInputs(t, corpus)

	var want struct {
		records, malformed, torn, oversize int
		bytes                              int64
		artifacts, unresolved              int
	}
	for _, s := range corpus.Sessions {
		want.records += s.Records
		want.malformed += s.MalformedRecords
		want.oversize += s.OversizedRecords
		want.bytes += s.Bytes
		want.artifacts += len(s.Artifacts)
		want.unresolved += len(s.UnresolvedRefs)
		if s.TornFinalLine {
			want.torn++
		}
	}
	if want.malformed == 0 || want.torn == 0 || want.oversize == 0 {
		t.Fatalf("the default profile plants no defects, so this test is vacuous: %+v", want)
	}

	rep := mustCheck(t, localRequest(inputs...))
	stats := rep.Stats
	if stats.Inputs != len(inputs) {
		t.Errorf("checked %d inputs, want %d", stats.Inputs, len(inputs))
	}
	if stats.Records != want.records {
		t.Errorf("counted %d records, generator wrote %d", stats.Records, want.records)
	}
	if stats.Bytes != want.bytes {
		t.Errorf("counted %d bytes, generator wrote %d", stats.Bytes, want.bytes)
	}
	if stats.MalformedRecords != want.malformed {
		t.Errorf("counted %d malformed records, generator planted %d", stats.MalformedRecords, want.malformed)
	}
	if stats.TruncatedInputs != want.torn {
		t.Errorf("counted %d truncated inputs, generator planted %d", stats.TruncatedInputs, want.torn)
	}
	// The generator's oversized records are the only ones above the record
	// limit, so the counts must agree exactly: an ordinary generated record is
	// a few hundred bytes.
	if stats.OversizeRecords != want.oversize {
		t.Errorf("counted %d oversize records, generator planted %d", stats.OversizeRecords, want.oversize)
	}
	if stats.Attachments != want.artifacts {
		t.Errorf("counted %d attachments, generator wrote %d", stats.Attachments, want.artifacts)
	}
	if stats.UnresolvedReferences != want.unresolved {
		t.Errorf("counted %d unresolved references, generator planted %d", stats.UnresolvedReferences, want.unresolved)
	}

	// Every corpus-health finding is a fact about the corpus rather than a
	// judgement about its content, and each one locates the record it is
	// about.
	for _, f := range rep.Findings {
		if f.Category == CategorySecret {
			continue
		}
		if f.Confidence != ConfidenceObserved {
			t.Errorf("%s reported as %q rather than observed", f.Category, f.Confidence)
		}
		if f.Evidence.Locator().Digest == "" {
			t.Errorf("%s finding has no recoverable evidence", f.Category)
		}
	}

	// Determinism over a real corpus, not just over a hand-built fixture.
	if second := mustCheck(t, localRequest(inputs...)); !reportsEqual(rep, second) {
		t.Error("two checks of one generated corpus disagree")
	}
}

func reportsEqual(a, b *Report) bool {
	if a.Stats != b.Stats || len(a.Findings) != len(b.Findings) {
		return false
	}
	for i := range a.Findings {
		if a.Findings[i] != b.Findings[i] {
			return false
		}
	}
	return true
}

// TestTranscriptAndAttachmentSizeAreMeasuredAgainstTheThresholds uses the
// generated corpus's own size distribution: the profile deliberately contains
// one log far larger than the rest, which is the shape a real corpus has and
// the reason the default limit sits near the top of the distribution rather
// than in its middle.
func TestTranscriptAndAttachmentSizeAreMeasuredAgainstTheThresholds(t *testing.T) {
	corpus, err := synth.Generate(t.TempDir(), synth.DefaultProfile())
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}
	inputs := synthInputs(t, corpus)

	th := DefaultThresholds()
	th.TranscriptBytes = 256 << 10
	th.AttachmentBytes = 1 << 10

	var wantLarge, wantAttachments int
	for _, s := range corpus.Sessions {
		if s.Bytes > th.TranscriptBytes {
			wantLarge++
		}
		for _, artifact := range s.Artifacts {
			info, err := os.Stat(artifact)
			if err != nil {
				t.Fatalf("stat generated artifact: %v", err)
			}
			if info.Size() > th.AttachmentBytes {
				wantAttachments++
			}
		}
	}
	if wantLarge == 0 {
		t.Fatal("no generated log exceeds the lowered transcript limit; the test would be vacuous")
	}

	req := localRequest(inputs...)
	req.Thresholds = &th
	rep := mustCheck(t, req)

	if rep.Stats.OversizeTranscripts != wantLarge {
		t.Errorf("reported %d oversize transcripts, %d exceed the limit",
			rep.Stats.OversizeTranscripts, wantLarge)
	}
	if rep.Stats.OversizeAttachments != wantAttachments {
		t.Errorf("reported %d oversize attachments, %d exceed the limit",
			rep.Stats.OversizeAttachments, wantAttachments)
	}
	for _, f := range findingsByCategory(rep, CategoryOversizeTranscript) {
		if f.Measure <= f.Limit || f.Limit != th.TranscriptBytes {
			t.Errorf("size finding does not report what it compared: measure=%d limit=%d", f.Measure, f.Limit)
		}
	}

	// The default limits are calibrated well above this corpus, so the same
	// inputs must be quiet under them: a threshold that fired on an ordinary
	// session would be a threshold nobody reads.
	if quiet := mustCheck(t, localRequest(inputs...)); quiet.Stats.OversizeTranscripts != 0 {
		t.Errorf("default transcript limit fired on %d generated logs", quiet.Stats.OversizeTranscripts)
	}
}

// rawLog writes an exact primary log, byte for byte, and returns the Input
// naming it. Record integrity is about bytes the JSON encoder would never
// produce — a line that is not JSON, a file that stops mid-record — so these
// fixtures cannot go through ompLog.
func rawLog(t *testing.T, sourceID, body string) Input {
	t.Helper()
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write raw log: %v", err)
	}
	return Input{
		Stream: event.Stream{
			Harness:       event.HarnessOMP,
			AdapterSchema: 1,
			SourceID:      sourceID,
			Path:          path,
		},
		Digest: string(digest.Bytes([]byte(body))),
	}
}

// TestTruncatedIsDistinguishedFromMalformed is the distinction §6.1 draws and
// §6.4 reports. A torn final record is a capture taken while the harness was
// still appending, which the next hourly snapshot supersedes; an unparsable
// record in the middle of a log is corruption that will still be there
// tomorrow. Collapsing them into one count would hide the difference behind a
// number that never changes.
func TestTruncatedIsDistinguishedFromMalformed(t *testing.T) {
	valid := `{"type":"message","timestamp":"2026-01-02T03:04:05Z","message":{"role":"user","content":[{"type":"text","text":"an ordinary message"}]}}`
	tests := []struct {
		name          string
		body          string
		wantRecords   int
		wantMalformed int
		wantTruncated int
	}{
		{
			name:        "clean log",
			body:        valid + "\n" + valid + "\n",
			wantRecords: 2,
		},
		{
			name:          "malformed record in the middle",
			body:          valid + "\n" + "this line is not json\n" + valid + "\n",
			wantRecords:   3,
			wantMalformed: 1,
		},
		{
			name:          "torn final record",
			body:          valid + "\n" + `{"type":"message","timesta`,
			wantRecords:   2,
			wantTruncated: 1,
		},
		{
			name:          "malformed final record with a terminator",
			body:          valid + "\n" + "this line is not json\n",
			wantRecords:   2,
			wantMalformed: 1,
		},
		{
			name:          "both causes in one log",
			body:          valid + "\nthis line is not json\n" + valid + "\n{\"type\":\"mes",
			wantRecords:   4,
			wantMalformed: 1,
			wantTruncated: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rep := mustCheck(t, localRequest(rawLog(t, "omp/probe", test.body)))
			if rep.Stats.Records != test.wantRecords {
				t.Errorf("counted %d records, want %d", rep.Stats.Records, test.wantRecords)
			}
			if rep.Stats.MalformedRecords != test.wantMalformed {
				t.Errorf("counted %d malformed records, want %d", rep.Stats.MalformedRecords, test.wantMalformed)
			}
			if rep.Stats.TruncatedInputs != test.wantTruncated {
				t.Errorf("counted %d truncated inputs, want %d", rep.Stats.TruncatedInputs, test.wantTruncated)
			}
			for _, f := range findingsByCategory(rep, CategoryTruncatedInput) {
				if f.Evidence.Locator().Line != test.wantRecords {
					t.Errorf("truncation reported on line %d, want the last record (%d)",
						f.Evidence.Locator().Line, test.wantRecords)
				}
			}
		})
	}
}

// TestOversizeRecordIsReportedWithoutBeingCalledDamage: a record above the
// review limit is an embedded payload or a whole-file result, and the top
// percentile of records carries a large share of a corpus's bytes. It needs a
// chunking decision, not a corruption count.
func TestOversizeRecordIsReportedWithoutBeingCalledDamage(t *testing.T) {
	big := strings.Repeat("filler payload ", 1000)
	in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", "an ordinary message", big)

	th := DefaultThresholds()
	th.RecordBytes = 4 << 10
	req := localRequest(in)
	req.Thresholds = &th
	rep := mustCheck(t, req)

	oversize := findingsByCategory(rep, CategoryOversizeRecord)
	if len(oversize) != 1 {
		t.Fatalf("%d oversize-record findings, want 1", len(oversize))
	}
	if oversize[0].Measure <= th.RecordBytes || oversize[0].Limit != th.RecordBytes {
		t.Errorf("finding does not report what it compared: %+v", oversize[0])
	}
	if oversize[0].Evidence.Locator().Line != 2 {
		t.Errorf("oversize record located on line %d, want 2", oversize[0].Evidence.Locator().Line)
	}
	if rep.Stats.MalformedRecords != 0 || rep.Stats.TruncatedInputs != 0 {
		t.Errorf("a large record was reported as damage: %+v", rep.Stats)
	}
}

// TestDuplicateAndChangedInputsAreReportedAgainstAPriorPreparation covers
// §6.4's last corpus-health check and §7's incremental behavior: re-running an
// unchanged scope is allowed, so the preflight has to say which inputs are new,
// which grew, and which are the same evidence arriving twice.
func TestDuplicateAndChangedInputsAreReportedAgainstAPriorPreparation(t *testing.T) {
	dir := t.TempDir()
	first := ompLog(t, dir, "first.jsonl", "omp/probe-a", "an ordinary operator message")
	// Same bytes, different identity: what a session copied or re-fetched
	// under a second selector looks like.
	second := ompLog(t, dir, "second.jsonl", "omp/probe-b", "an ordinary operator message")
	grown := ompLog(t, dir, "grown.jsonl", "omp/probe-c", "an ordinary operator message", "and one more turn")

	t.Run("no prior preparation", func(t *testing.T) {
		rep := mustCheck(t, localRequest(first, grown))
		if rep.Stats.NewInputs != 2 || rep.Stats.UnchangedInputs != 0 || rep.Stats.ChangedInputs != 0 {
			t.Errorf("first preparation reported %+v", rep.Stats)
		}
		if len(findingsByCategory(rep, CategoryChangedInput)) != 0 {
			t.Error("a first preparation cannot have changed inputs")
		}
	})

	t.Run("unchanged input", func(t *testing.T) {
		req := localRequest(first)
		req.Prior = &Preparation{ID: "prep-1", Inputs: []PriorInput{
			{SourceID: "omp/probe-a", Digest: first.Digest},
		}}
		rep := mustCheck(t, req)
		if rep.Stats.UnchangedInputs != 1 || rep.Stats.NewInputs != 0 {
			t.Errorf("stats = %+v", rep.Stats)
		}
		if len(rep.Findings) != 0 {
			t.Errorf("an unchanged input produced findings: %+v", rep.Findings)
		}
	})

	t.Run("changed input", func(t *testing.T) {
		req := localRequest(grown)
		req.Prior = &Preparation{ID: "prep-1", Inputs: []PriorInput{
			{SourceID: "omp/probe-c", Digest: first.Digest},
		}}
		rep := mustCheck(t, req)
		if rep.Stats.ChangedInputs != 1 {
			t.Errorf("stats = %+v", rep.Stats)
		}
		changed := findingsByCategory(rep, CategoryChangedInput)
		if len(changed) != 1 {
			t.Fatalf("%d changed-input findings", len(changed))
		}
		if changed[0].Reference != first.Digest {
			t.Errorf("finding does not name the digest it differs from: %q", changed[0].Reference)
		}
		if changed[0].Evidence.Locator().Digest != grown.Digest {
			t.Errorf("finding is not evidenced by the input it describes")
		}
	})

	t.Run("duplicate within one request", func(t *testing.T) {
		rep := mustCheck(t, localRequest(first, second))
		if rep.Stats.DuplicateInputs != 1 {
			t.Errorf("stats = %+v", rep.Stats)
		}
		duplicates := findingsByCategory(rep, CategoryDuplicateInput)
		if len(duplicates) != 1 {
			t.Fatalf("%d duplicate findings", len(duplicates))
		}
		if duplicates[0].Reference != "omp/probe-a" {
			t.Errorf("duplicate does not name the input it matched: %q", duplicates[0].Reference)
		}
	})

	t.Run("duplicate of a prior preparation's input", func(t *testing.T) {
		req := localRequest(second)
		req.Prior = &Preparation{ID: "prep-1", Inputs: []PriorInput{
			{SourceID: "omp/probe-a", Digest: first.Digest},
		}}
		rep := mustCheck(t, req)
		duplicates := findingsByCategory(rep, CategoryDuplicateInput)
		if len(duplicates) != 1 || duplicates[0].Reference != "omp/probe-a" {
			t.Fatalf("prior duplicate not reported: %+v", duplicates)
		}
		if rep.Stats.NewInputs != 1 {
			t.Errorf("a new identity carrying prepared content is still a new input: %+v", rep.Stats)
		}
	})
}

// TestUnresolvedClosureIsReportedNotRecomputed: closure belongs to the adapter
// that discovered the session (§3), and an unresolved reference is a
// completeness fact the run should know before it starts (§2.5), not an error.
func TestUnresolvedClosureIsReportedNotRecomputed(t *testing.T) {
	in := ompLog(t, t.TempDir(), "log.jsonl", "omp/probe", "an ordinary operator message")
	in.Unresolved = []Reference{
		{Ref: "blob:sha256:" + strings.Repeat("ab", 32), Reason: "absent from the store"},
		{Ref: "blob:sha256:" + strings.Repeat("cd", 32), Reason: "bytes do not match the name"},
	}
	rep := mustCheck(t, localRequest(in))

	unresolved := findingsByCategory(rep, CategoryUnresolvedReference)
	if len(unresolved) != 2 {
		t.Fatalf("%d unresolved-reference findings, want 2", len(unresolved))
	}
	for _, f := range unresolved {
		if !strings.HasPrefix(f.Reference, "blob:sha256:") {
			t.Errorf("finding does not name the reference: %q", f.Reference)
		}
		if !strings.Contains(f.Summary, "do not match") && !strings.Contains(f.Summary, "absent") {
			t.Errorf("finding drops the adapter's reason: %q", f.Summary)
		}
		if f.Evidence.Locator().Digest != in.Digest {
			t.Error("closure finding is not evidenced by its input")
		}
	}
	if rep.Stats.UnresolvedReferences != 2 {
		t.Errorf("stats = %+v", rep.Stats)
	}
}
