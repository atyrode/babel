package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/worker"
)

// conformanceVerdicts is the report a fake grading run produces: one passing
// obligation, one failing obligation whose messages quote a worker, and one
// more pass after the failure, so a reader can tell a streamed line from a
// batched one by where it lands.
//
// The failure carries an ANSI escape because a failure message is the one part
// of this output a worker writes, and streaming it as it settles must not be a
// way around the sanitizer.
var conformanceVerdicts = []worker.ObligationResult{
	{Name: "handshake/accept", Passed: true},
	{Name: "run/well-behaved", Failures: []string{"worker: handshake timed out: no hello within 15s", "\x1b[31mred\x1b[0m"}},
	{Name: "run/cancellation", Passed: true},
}

// TestConformanceReportStreamsEachVerdictAsItSettles grades the command's own
// half of issue #78: the runner hands over each verdict as it is decided, and
// the human report must put it on the terminal then rather than collect the lot.
//
// The fake runner reads stdout between deliveries, so the assertion is what an
// operator would have seen at that moment: after two verdicts, exactly two
// obligations and no summary.
func TestConformanceReportStreamsEachVerdictAsItSettles(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	var seen []string
	grade := func(settled func(worker.ObligationResult)) []worker.ObligationResult {
		for _, verdict := range conformanceVerdicts {
			seen = append(seen, stdout.String())
			settled(verdict)
		}
		return conformanceVerdicts
	}

	err := a.reportConformance(conformanceResult{Worker: "/bin/true"}, false, grade)
	if !errors.Is(err, errReported) {
		t.Fatalf("reportConformance = %v, want errReported for a failed obligation", err)
	}

	// What stdout held before each verdict was delivered: nothing before the
	// first, and every earlier obligation before the ones after it.
	want := []string{
		"",
		"ok    handshake/accept\n",
		"ok    handshake/accept\n" +
			"FAIL  run/well-behaved\n" +
			"        worker: handshake timed out: no hello within 15s\n" +
			"        \\u{1B}[31mred\\u{1B}[0m\n",
	}
	if !slices.Equal(seen, want) {
		t.Errorf("stdout as the run proceeded =\n%q\nwant\n%q", seen, want)
	}
	if strings.Contains(stdout.String(), "\x1b") {
		t.Error("a streamed failure message reached the terminal unsanitized")
	}

	// The summary is the one thing that cannot be streamed, because it counts
	// obligations that have not been graded yet.
	final := stdout.String()
	if !strings.HasSuffix(final, "\n3 obligations, 2 passed, 1 failed\n") {
		t.Errorf("report did not end with the summary:\n%s", final)
	}
	if strings.Count(final, "run/well-behaved") != 1 {
		t.Errorf("an obligation was reported twice; streaming must replace the closing recital, not join it:\n%s", final)
	}
	if !strings.Contains(stderr.String(), "does not yet implement") {
		t.Errorf("stderr did not point at the contract: %q", stderr.String())
	}
}

// TestConformanceReportRelaxedGradingIsStillAnnounced keeps the streamed report
// from losing the note that makes it honest: a relaxed pass reported like a
// strict one is the most misleading output this command can produce.
func TestConformanceReportRelaxedGradingIsStillAnnounced(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	passed := []worker.ObligationResult{{Name: "handshake/accept", Passed: true}}
	grade := func(settled func(worker.ObligationResult)) []worker.ObligationResult {
		settled(passed[0])
		return passed
	}

	if err := a.reportConformance(conformanceResult{Worker: "/bin/true", Unsandboxed: true}, false, grade); err != nil {
		t.Fatalf("reportConformance = %v, want nil for a passing report", err)
	}
	if !strings.Contains(stdout.String(), "relaxed containment") {
		t.Errorf("a relaxed pass was reported without saying so:\n%s", stdout.String())
	}
}

// TestConformanceReportHoldsJSONUntilTheEnd covers the exemption: --json output
// is a single parseable document, so it subscribes to no verdict stream at all
// and the document's shape is unchanged by streaming.
func TestConformanceReportHoldsJSONUntilTheEnd(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	grade := func(settled func(worker.ObligationResult)) []worker.ObligationResult {
		// Nothing listening is how the runner is told not to stream: a
		// --json invocation that subscribed would write partial lines
		// around a document that is supposed to be the whole output.
		if settled != nil {
			t.Error("--json subscribed to the verdict stream")
		}
		return conformanceVerdicts
	}

	res := conformanceResult{Worker: "/bin/true", WorkerArgs: []string{"worker"}}
	if err := a.reportConformance(res, true, grade); !errors.Is(err, errReported) {
		t.Fatalf("reportConformance = %v, want errReported for a failed obligation", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("stdout was not one JSON document: %v\n%s", err, stdout.String())
	}
	wantKeys := []string{"failed", "obligations", "ok", "passed", "total", "unsandboxed", "worker", "worker_args"}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("document keys = %q, want %q", keys, wantKeys)
	}
	if doc["ok"] != false || doc["total"] != 3.0 || doc["passed"] != 2.0 || doc["failed"] != 1.0 {
		t.Errorf("counts = %v", doc)
	}

	obligations, ok := doc["obligations"].([]any)
	if !ok || len(obligations) != 3 {
		t.Fatalf("obligations = %v, want three rows", doc["obligations"])
	}
	// A passing row carries no messages, and a failing one carries every
	// message that decided it, sanitized.
	first, _ := obligations[0].(map[string]any)
	if _, present := first["failures"]; present {
		t.Errorf("a passing obligation carried failures: %v", first)
	}
	if first["name"] != "handshake/accept" || first["passed"] != true {
		t.Errorf("first row = %v", first)
	}
	second, _ := obligations[1].(map[string]any)
	failures, _ := second["failures"].([]any)
	if len(failures) != 2 {
		t.Fatalf("second row = %v, want both messages", second)
	}
	if got, want := failures[1], "\\u{1B}[31mred\\u{1B}[0m"; got != want {
		t.Errorf("failure = %q, want %q", got, want)
	}
}

// TestConformanceReportNamesTheObligationThatStalls is the operator's question
// in issue #78: a suite that has gone quiet has gone quiet inside one
// obligation, and the report must already have named the ones that settled.
//
// The fake runner is held open in the last obligation until the test has read
// stdout, so the two earlier lines are observed while the run provably has not
// finished — the same shape as a worker that never says hello, without paying
// its handshake budget to watch.
func TestConformanceReportNamesTheObligationThatStalls(t *testing.T) {
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	a := &app{stdout: stdout, stderr: stderr}

	stalled := make(chan struct{})
	release := make(chan struct{})
	grade := func(settled func(worker.ObligationResult)) []worker.ObligationResult {
		settled(conformanceVerdicts[0])
		settled(conformanceVerdicts[2])
		close(stalled)
		<-release
		settled(conformanceVerdicts[1])
		return []worker.ObligationResult{conformanceVerdicts[0], conformanceVerdicts[2], conformanceVerdicts[1]}
	}

	done := make(chan error, 1)
	go func() { done <- a.reportConformance(conformanceResult{Worker: "/bin/cat"}, false, grade) }()

	<-stalled
	during := stdout.String()
	for _, name := range []string{"handshake/accept", "run/cancellation"} {
		if !strings.Contains(during, name) {
			t.Errorf("obligation %s had settled but was not on the terminal:\n%s", name, during)
		}
	}
	if strings.Contains(during, "run/well-behaved") {
		t.Errorf("an obligation that has not settled was reported:\n%s", during)
	}
	if strings.Contains(during, "obligations,") {
		t.Errorf("the summary was written before the run finished:\n%s", during)
	}

	close(release)
	if err := <-done; !errors.Is(err, errReported) {
		t.Fatalf("reportConformance = %v, want errReported", err)
	}
	if !strings.Contains(stdout.String(), "FAIL  run/well-behaved") {
		t.Errorf("the stalled obligation's verdict never arrived:\n%s", stdout.String())
	}
}
