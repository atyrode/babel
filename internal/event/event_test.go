package event

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// want is one expected event. Exactly one of text and textPrefix is set:
// classified events assert their normalized text exactly, while opaque
// events assert a prefix of the raw record they preserved.
type want struct {
	line       int
	kind       Kind
	role       string
	tool       string
	outcome    string
	paths      []string
	partial    bool
	text       string
	textPrefix string
}

// ompWant is the expected classification of testdata/omp-session.jsonl,
// which exercises every rule in the OMP table plus each degradation path.
var ompWant = []want{
	{line: 1, kind: KindOpaque, textPrefix: `{"type":"session"`},
	{line: 2, kind: KindUserReport, role: "user", text: "synthetic operator request one"},
	{line: 3, kind: KindAgentClaim, role: "assistant", text: "synthetic reasoning trace"},
	{line: 3, kind: KindAgentClaim, role: "assistant", text: "synthetic agent claim one"},
	{line: 3, kind: KindToolObservation, role: "assistant", tool: "bash", text: "go test ./internal/synthetic/..."},
	{line: 4, kind: KindVerificationEvidence, role: "toolResult", tool: "bash", outcome: OutcomePass, text: "synthetic test run reported success"},
	{line: 5, kind: KindToolObservation, role: "assistant", tool: "edit", text: "Editing synthetic file"},
	{line: 6, kind: KindRepositoryChange, role: "toolResult", tool: "edit", paths: []string{"synthetic/a.go"}, text: "synthetic edit applied"},
	{line: 7, kind: KindToolObservation, role: "assistant", tool: "write", text: "Writing synthetic file"},
	{line: 8, kind: KindRepositoryChange, role: "toolResult", tool: "write", paths: []string{"synthetic/b.go"}, text: "synthetic write complete"},
	{line: 9, kind: KindToolObservation, role: "assistant", tool: "bash", text: "cd /synthetic && go vet ./..."},
	{line: 10, kind: KindVerificationEvidence, role: "toolResult", tool: "bash", outcome: OutcomeFail, text: "synthetic vet reported one finding"},
	{line: 11, kind: KindToolObservation, role: "assistant", tool: "read", text: "Reading synthetic file"},
	{line: 12, kind: KindToolObservation, role: "toolResult", tool: "read", outcome: OutcomeError, text: "synthetic read failed"},
	{line: 13, kind: KindOpaque, textPrefix: `{"type":"custom"`},
	{line: 14, kind: KindOpaque, textPrefix: `{"type":"message","id":"e0000013"`},
	{line: 15, kind: KindOpaque, partial: true, textPrefix: `{"type":"message","id":"e0000014" `},
	{line: 16, kind: KindOpaque, textPrefix: `{"type":"synthetic_future_record"`},
	{line: 17, kind: KindOpaque, partial: true, textPrefix: `{"type":"message","id":"e0000015"`},
}

// codexWant is the expected classification of testdata/codex-session.jsonl.
// Outcome stays empty on the tool output because this Codex schema records
// no exit status.
var codexWant = []want{
	{line: 1, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:00:00.000Z","type":"session_meta"`},
	{line: 2, kind: KindUserReport, role: "user", text: "synthetic operator request two"},
	{line: 3, kind: KindAgentClaim, role: "assistant", text: "synthetic agent claim two"},
	{line: 4, kind: KindAgentClaim, role: "assistant", text: "synthetic reasoning summary"},
	{line: 5, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:04:00.000Z"`},
	{line: 6, kind: KindToolObservation, role: "assistant", tool: "exec_command", text: "go test ./internal/synthetic/..."},
	{line: 7, kind: KindVerificationEvidence, role: "tool", tool: "exec_command", text: "synthetic test output\nWall time: 0.1 seconds"},
	{line: 8, kind: KindToolObservation, role: "assistant", tool: "apply_patch", text: "*** Begin Patch\n*** Update File: synthetic/c.go\n@@\n-old\n+new\n*** End Patch"},
	{line: 9, kind: KindRepositoryChange, role: "tool", tool: "apply_patch", paths: []string{"/synthetic/workspace/two/a.go", "/synthetic/workspace/two/z.go"}},
	{line: 10, kind: KindToolObservation, role: "tool", tool: "apply_patch", outcome: OutcomeError},
	{line: 11, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:10:00.000Z"`},
	{line: 12, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:11:00.000Z"`},
	{line: 13, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:12:00.000Z"`},
	{line: 14, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:13:00.000Z"`},
	{line: 15, kind: KindToolObservation, role: "tool", tool: "synthetic-server/synthetic_lookup"},
	{line: 16, kind: KindToolObservation, role: "assistant", tool: "web_search", text: `{"type":"search","query":"synthetic query"}`},
	{line: 17, kind: KindOpaque, textPrefix: `{"timestamp":"2026-04-02T00:16:00.000Z","type":"turn_context"`},
	{line: 18, kind: KindOpaque, partial: true, textPrefix: `{"timestamp":"2026-04-02T00:17:00.000Z","type":"event_msg" `},
}

// claudeWant is the expected classification of testdata/claude-session.jsonl.
// Only OutcomeError appears: this harness records no exit status.
var claudeWant = []want{
	{line: 1, kind: KindUserReport, role: "user", text: "synthetic operator request three"},
	{line: 2, kind: KindAgentClaim, role: "assistant", text: "synthetic agent claim three"},
	{line: 3, kind: KindAgentClaim, role: "assistant", text: "synthetic reasoning trace three"},
	{line: 3, kind: KindAgentClaim, role: "assistant", text: "synthetic agent claim four"},
	{line: 4, kind: KindToolObservation, role: "assistant", tool: "Bash", text: "npm test --silent"},
	{line: 5, kind: KindVerificationEvidence, role: "user", tool: "Bash", text: "synthetic test output"},
	{line: 6, kind: KindToolObservation, role: "assistant", tool: "Edit", text: "/synthetic/workspace/three/d.go"},
	{line: 7, kind: KindRepositoryChange, role: "user", tool: "Edit", paths: []string{"/synthetic/workspace/three/d.go"}, text: "synthetic edit applied"},
	{line: 8, kind: KindToolObservation, role: "assistant", tool: "Read", text: "/synthetic/workspace/three/missing.go"},
	{line: 9, kind: KindToolObservation, role: "user", tool: "Read", outcome: OutcomeError, text: "synthetic read failed"},
	{line: 10, kind: KindOpaque, textPrefix: `{"type":"system"`},
	{line: 11, kind: KindOpaque, textPrefix: `{"type":"attachment"`},
	{line: 12, kind: KindUserReport, role: "user", text: "synthetic operator request four"},
	{line: 13, kind: KindOpaque, textPrefix: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"synthetic_future_part"`},
	{line: 14, kind: KindOpaque, partial: true, textPrefix: `{"type":"user","message":{"role":"user","content":"synthetic torn record`},
}

func TestScanClassifiesFixtures(t *testing.T) {
	cases := map[string]struct {
		harness string
		file    string
		want    []want
	}{
		"omp":    {harness: HarnessOMP, file: "omp-session.jsonl", want: ompWant},
		"codex":  {harness: HarnessCodex, file: "codex-session.jsonl", want: codexWant},
		"claude": {harness: HarnessClaude, file: "claude-session.jsonl", want: claudeWant},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := scanFixture(t, tc.harness, tc.file)
			if len(got) != len(tc.want) {
				for i, e := range got {
					t.Logf("got[%d] line=%d kind=%s role=%s tool=%s outcome=%s paths=%v partial=%v text=%q",
						i, e.Locator.Line, e.Kind, e.Role, e.Tool, e.Outcome, e.Paths, e.Partial, e.Text)
				}
				t.Fatalf("event count = %d, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				e := got[i]
				if e.Index != i {
					t.Errorf("event %d: Index = %d, want %d", i, e.Index, i)
				}
				if e.Locator.Line != w.line {
					t.Errorf("event %d: line = %d, want %d", i, e.Locator.Line, w.line)
				}
				if e.Kind != w.kind {
					t.Errorf("event %d: kind = %q, want %q", i, e.Kind, w.kind)
				}
				if e.Role != w.role {
					t.Errorf("event %d: role = %q, want %q", i, e.Role, w.role)
				}
				if e.Tool != w.tool {
					t.Errorf("event %d: tool = %q, want %q", i, e.Tool, w.tool)
				}
				if e.Outcome != w.outcome {
					t.Errorf("event %d: outcome = %q, want %q", i, e.Outcome, w.outcome)
				}
				if e.Partial != w.partial {
					t.Errorf("event %d: partial = %v, want %v", i, e.Partial, w.partial)
				}
				if strings.Join(e.Paths, "\x00") != strings.Join(w.paths, "\x00") {
					t.Errorf("event %d: paths = %v, want %v", i, e.Paths, w.paths)
				}
				if w.textPrefix != "" {
					if !strings.HasPrefix(e.Text, w.textPrefix) {
						t.Errorf("event %d: text = %q, want prefix %q", i, e.Text, w.textPrefix)
					}
				} else if e.Text != w.text {
					t.Errorf("event %d: text = %q, want %q", i, e.Text, w.text)
				}
			}
		})
	}
}

// TestScanKindCoverage states the acceptance requirement directly: every
// harness fixture must produce all six kinds, so no harness silently loses a
// category.
func TestScanKindCoverage(t *testing.T) {
	all := []Kind{KindUserReport, KindAgentClaim, KindToolObservation, KindRepositoryChange, KindVerificationEvidence, KindOpaque}
	cases := map[string]string{
		HarnessOMP:    "omp-session.jsonl",
		HarnessCodex:  "codex-session.jsonl",
		HarnessClaude: "claude-session.jsonl",
	}
	for harness, file := range cases {
		t.Run(harness, func(t *testing.T) {
			seen := make(map[Kind]int)
			for _, e := range scanFixture(t, harness, file) {
				seen[e.Kind]++
			}
			for _, kind := range all {
				if seen[kind] == 0 {
					t.Errorf("kind %q never produced", kind)
				}
			}
		})
	}
}

// TestScanLocatorsAddressRecords verifies the locator independently of the
// scanner: every event's byte offset must select its own line from the file,
// and its digest must equal that line's SHA-256 hashed here from the file's
// own bytes.
func TestScanLocatorsAddressRecords(t *testing.T) {
	for _, file := range []string{"omp-session.jsonl", "codex-session.jsonl", "claude-session.jsonl"} {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join("testdata", file)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			harness := strings.SplitN(file, "-", 2)[0]
			for _, e := range scanFixture(t, harness, file) {
				start, end := lineBounds(t, raw, e.Locator.Line)
				if e.Locator.ByteOffset != int64(start) {
					t.Errorf("line %d: byte offset = %d, want %d", e.Locator.Line, e.Locator.ByteOffset, start)
				}
				sum := sha256.Sum256(raw[start:end])
				if want := hex.EncodeToString(sum[:]); e.Locator.Digest != want {
					t.Errorf("line %d: digest = %s, want %s", e.Locator.Line, e.Locator.Digest, want)
				}
				if e.Locator.Path != path {
					t.Errorf("line %d: path = %q, want %q", e.Locator.Line, e.Locator.Path, path)
				}
			}
		})
	}
}

// lineBounds returns the byte range of the 1-based line, excluding its
// terminator, computed from the file bytes rather than from the scanner.
func lineBounds(t *testing.T, raw []byte, line int) (start, end int) {
	t.Helper()
	at := 1
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			if at == line {
				end = i
				if end > start && raw[end-1] == '\r' {
					end--
				}
				return start, end
			}
			at++
			start = i + 1
		}
	}
	t.Fatalf("line %d not found", line)
	return 0, 0
}

func TestScanOversizedRecordDoesNotAbort(t *testing.T) {
	oversized := `{"type":"message","timestamp":"2026-04-01T00:01:00.000Z","message":{"role":"user","content":[{"type":"text","text":"` +
		strings.Repeat("s", MaxRecordBytes+1024) + `"}]}}`
	after := `{"type":"message","timestamp":"2026-04-01T00:02:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic operator request after"}]}}`
	log := oversized + "\n" + after + "\n"

	var got []Event
	err := Scan(strings.NewReader(log), Stream{Harness: HarnessOMP, Path: "synthetic.jsonl"}, func(e Event) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("event count = %d, want 2", len(got))
	}
	if got[0].Kind != KindOpaque || !got[0].Partial {
		t.Errorf("oversized event = %q partial=%v, want opaque partial", got[0].Kind, got[0].Partial)
	}
	if n := len([]rune(got[0].Text)); n > TextRuneLimit {
		t.Errorf("oversized text = %d runes, want at most %d", n, TextRuneLimit)
	}
	// The digest must still cover every byte of the record, including the
	// tail that was never retained, or the event could not be verified
	// against the archive.
	sum := sha256.Sum256([]byte(oversized))
	if want := hex.EncodeToString(sum[:]); got[0].Locator.Digest != want {
		t.Errorf("oversized digest = %s, want %s", got[0].Locator.Digest, want)
	}
	if got[1].Kind != KindUserReport || got[1].Locator.Line != 2 {
		t.Errorf("record after oversized = %q line %d, want user-report line 2", got[1].Kind, got[1].Locator.Line)
	}
	if got[1].Locator.ByteOffset != int64(len(oversized)+1) {
		t.Errorf("record after oversized offset = %d, want %d", got[1].Locator.ByteOffset, len(oversized)+1)
	}
}

// TestScanCRLF covers the record framing corner where the CR of a CRLF
// terminator is the last byte of a read buffer: the digest must still be the
// digest of the record without its terminator.
func TestScanCRLF(t *testing.T) {
	prefix := `{"type":"message","timestamp":"2026-04-01T00:01:00.000Z","message":{"role":"user","content":[{"type":"text","text":"`
	suffix := `"}]}}`
	pad := readBufferBytes - 1 - len(prefix) - len(suffix)
	if pad <= 0 {
		t.Fatalf("read buffer too small for this fixture")
	}
	straddling := prefix + strings.Repeat("s", pad) + suffix
	if len(straddling) != readBufferBytes-1 {
		t.Fatalf("record length = %d, want %d", len(straddling), readBufferBytes-1)
	}
	short := `{"type":"message","timestamp":"2026-04-01T00:02:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic crlf record"}]}}`
	log := straddling + "\r\n" + short + "\r\n"

	var got []Event
	err := Scan(strings.NewReader(log), Stream{Harness: HarnessOMP, Path: "synthetic.jsonl"}, func(e Event) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("event count = %d, want 2", len(got))
	}
	for i, record := range []string{straddling, short} {
		if got[i].Kind != KindUserReport {
			t.Errorf("event %d kind = %q, want %q", i, got[i].Kind, KindUserReport)
		}
		sum := sha256.Sum256([]byte(record))
		if want := hex.EncodeToString(sum[:]); got[i].Locator.Digest != want {
			t.Errorf("event %d digest = %s, want %s (record without CRLF)", i, got[i].Locator.Digest, want)
		}
	}
	if got[1].Locator.ByteOffset != int64(len(straddling)+2) {
		t.Errorf("second offset = %d, want %d", got[1].Locator.ByteOffset, len(straddling)+2)
	}
}

// TestScanTrailingCarriageReturnIsContent guards the other side of the CRLF
// rule: a lone CR at end of file is record content, not a terminator, so
// dropping it would corrupt the digest.
func TestScanTrailingCarriageReturnIsContent(t *testing.T) {
	record := `{"type":"message","message":{"role":"user"}}` + "\r"
	var got []Event
	if err := Scan(strings.NewReader(record), Stream{Harness: HarnessOMP, Path: "synthetic.jsonl"}, func(e Event) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("event count = %d, want 1", len(got))
	}
	sum := sha256.Sum256([]byte(record))
	if want := hex.EncodeToString(sum[:]); got[0].Locator.Digest != want {
		t.Errorf("digest = %s, want %s", got[0].Locator.Digest, want)
	}
}

func TestScanEmptyInput(t *testing.T) {
	count := 0
	if err := Scan(strings.NewReader(""), Stream{Harness: HarnessOMP}, func(Event) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 0 {
		t.Errorf("event count = %d, want 0", count)
	}
}

func TestScanUnknownHarness(t *testing.T) {
	err := Scan(strings.NewReader("{}\n"), Stream{Harness: "synthetic", Path: "synthetic.jsonl"}, func(Event) error {
		t.Error("callback ran for an unknown harness")
		return nil
	})
	if err == nil {
		t.Fatal("Scan accepted an unknown harness")
	}
	if !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("error = %v, want it to name the unknown harness", err)
	}
}

func TestScanPropagatesCallbackError(t *testing.T) {
	sentinel := errors.New("synthetic callback failure")
	seen := 0
	err := Scan(strings.NewReader("{}\n{}\n{}\n"), Stream{Harness: HarnessOMP}, func(Event) error {
		seen++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
	if seen != 1 {
		t.Errorf("callback ran %d times, want 1", seen)
	}
}

// TestScanBoundedMemory scans a log far larger than any budget the scanner
// may use and checks two independent bounds: peak live heap stays a small
// fraction of the file, and bytes allocated per record stay constant rather
// than scaling with the file. The design target is a primary log of a third
// of a gigabyte, which real harness sessions reach; 32 MiB is the largest
// log worth generating on every test run to demonstrate the same property.
//
// The peak-heap figure is dominated by Go's minimum heap target, not by
// scanner state, which is why the fractional limit is generous while the
// per-record limit is tight.
func TestScanBoundedMemory(t *testing.T) {
	const target = 32 << 20
	path := filepath.Join(t.TempDir(), "bounded.jsonl")
	size := writeBoundedLog(t, path, target)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	count, peak := 0, uint64(0)
	err = Scan(f, Stream{Harness: HarnessOMP, Path: path}, func(e Event) error {
		count++
		if count%20000 == 0 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak {
				peak = m.HeapAlloc
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count < 100000 {
		t.Fatalf("event count = %d, want a log of many records", count)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	growth := uint64(0)
	if peak > before.HeapAlloc {
		growth = peak - before.HeapAlloc
	}
	perRecord := (after.TotalAlloc - before.TotalAlloc) / uint64(count)
	t.Logf("scanned %d bytes in %d events; peak live heap growth %d bytes (%.2f%% of file); %d bytes allocated per record",
		size, count, growth, 100*float64(growth)/float64(size), perRecord)
	if limit := uint64(size) / 4; growth > limit {
		t.Errorf("peak live heap growth = %d bytes, want at most %d (file is %d bytes)", growth, limit, size)
	}
	if perRecord > 4<<10 {
		t.Errorf("allocated %d bytes per record, want a bound independent of file size", perRecord)
	}
}

// writeBoundedLog writes a log of at least target bytes of realistically
// shaped OMP records. It is generated rather than committed: a 32 MiB
// fixture has no place in a repository.
func writeBoundedLog(t *testing.T, path string, target int) int {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	written := 0
	for i := 0; written < target; i++ {
		line := fmt.Sprintf(`{"type":"message","id":"e%08d","timestamp":"2026-04-01T00:01:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic operator request %08d with padding to a realistic record width"}]}}`, i, i)
		n, err := w.WriteString(line + "\n")
		if err != nil {
			t.Fatalf("write log: %v", err)
		}
		written += n
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush log: %v", err)
	}
	return written
}

func TestIsVerificationCommand(t *testing.T) {
	cases := map[string]bool{
		"go test ./...":                          true,
		"go test -run TestScan ./internal/event": true,
		"go vet ./internal/event/...":            true,
		"go build ./cmd/babel":                   true,
		"go run ./cmd/babel":                     false,
		"cd /synthetic && go test ./...":         true,
		"CGO_ENABLED=0 go test ./...":            true,
		"/usr/local/bin/go test ./...":           true,
		"nix develop -c bash -c 'go test ./...'": true,
		"nix-shell --run 'go test ./...'":        true,
		"env CGO_ENABLED=0 go test ./...":        true,
		"sudo make check":                        true,
		"env":                                    false,
		"bash -c":                                false,
		"make check":                             true,
		"make dev":                               false,
		"npm test --silent":                      true,
		"npm run lint":                           true,
		"npm install":                            false,
		"cargo clippy -- -D warnings":            true,
		"pytest -q":                              true,
		"cmake --build build":                    true,
		"cat notes.txt | grep 'go test'":         false,
		"echo run the tests":                     false,
		"":                                       false,
		"git commit -m 'go test'":                false,
		"bash -lc \"cd repo; gofmt -l .\"":       true,
		"docker run synthetic-image pytest":      false,
		strings.Repeat("x", commandScanLimit) + " go test ./...": false,
	}
	for command, want := range cases {
		if got := isVerificationCommand(command); got != want {
			t.Errorf("isVerificationCommand(%q) = %v, want %v", command, got, want)
		}
	}
}

// TestCallTrackerIsBounded proves the join state cannot grow with the log:
// a stream of unanswered calls forgets the oldest instead of accumulating.
func TestCallTrackerIsBounded(t *testing.T) {
	tracker := callTracker{byID: make(map[string]pendingCall)}
	total := maxPendingCalls + 10
	for i := range total {
		tracker.put(fmt.Sprintf("call-%04d", i), pendingCall{tool: "bash", verification: i%2 == 0})
	}
	if len(tracker.byID) > maxPendingCalls {
		t.Errorf("tracker holds %d calls, want at most %d", len(tracker.byID), maxPendingCalls)
	}
	if _, ok := tracker.take("call-0000"); ok {
		t.Error("oldest call was retained past the bound")
	}
	newest := fmt.Sprintf("call-%04d", total-1)
	call, ok := tracker.take(newest)
	if !ok {
		t.Fatalf("newest call %s was evicted", newest)
	}
	if call.tool != "bash" {
		t.Errorf("newest call tool = %q, want %q", call.tool, "bash")
	}
	if _, ok := tracker.take(newest); ok {
		t.Error("take did not forget the call it returned")
	}
	if _, ok := tracker.take(""); ok {
		t.Error("take returned a call for an empty id")
	}
}

// TestScanRecordsTimes checks that timestamps are parsed where present and
// left nil where the harness gives nothing parseable, rather than being
// filled with a synthesized value.
func TestScanRecordsTimes(t *testing.T) {
	log := `{"type":"message","timestamp":"2026-04-01T00:01:02Z","message":{"role":"user","content":[{"type":"text","text":"synthetic timed"}]}}` + "\n" +
		`{"type":"message","timestamp":"not-a-timestamp","message":{"role":"user","content":[{"type":"text","text":"synthetic untimed"}]}}` + "\n" +
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"synthetic undated"}]}}` + "\n"
	var got []Event
	if err := Scan(strings.NewReader(log), Stream{Harness: HarnessOMP}, func(e Event) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("event count = %d, want 3", len(got))
	}
	if got[0].Time == nil || got[0].Time.Format("2006-01-02T15:04:05Z") != "2026-04-01T00:01:02Z" {
		t.Errorf("first time = %v, want 2026-04-01T00:01:02Z", got[0].Time)
	}
	for _, i := range []int{1, 2} {
		if got[i].Time != nil {
			t.Errorf("event %d time = %v, want nil", i, got[i].Time)
		}
	}
}

func scanFixture(t *testing.T, harness, file string) []Event {
	t.Helper()
	path := filepath.Join("testdata", file)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	var got []Event
	stream := Stream{Harness: harness, AdapterSchema: 1, SourceID: "synthetic-source", Path: path}
	if err := Scan(f, stream, func(e Event) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return got
}
