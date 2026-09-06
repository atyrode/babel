package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	{Name: "describe/reports-runtime", Passed: true},
	{Name: "engine/becomes-ready", Failures: []string{"worker: engine did not become ready in time", "\x1b[31mred\x1b[0m"}},
	{Name: "engine/exits-on-eof", Passed: true},
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
		"ok    describe/reports-runtime\n",
		"ok    describe/reports-runtime\n" +
			"FAIL  engine/becomes-ready\n" +
			"        worker: engine did not become ready in time\n" +
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
	if strings.Count(final, "engine/becomes-ready") != 1 {
		t.Errorf("an obligation was reported twice; streaming must replace the closing recital, not join it:\n%s", final)
	}
	if !strings.Contains(stderr.String(), "does not yet provide") {
		t.Errorf("stderr did not point at the contract: %q", stderr.String())
	}
}

// TestConformanceReportRelaxedGradingIsStillAnnounced keeps the streamed report
// from losing the note that makes it honest: a relaxed pass reported like a
// strict one is the most misleading output this command can produce.
func TestConformanceReportRelaxedGradingIsStillAnnounced(t *testing.T) {
	var stdout, stderr bytes.Buffer
	a := &app{stdout: &stdout, stderr: &stderr}

	passed := []worker.ObligationResult{{Name: "describe/reports-runtime", Passed: true}}
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
	wantKeys := []string{"failed", "inference", "obligations", "ok", "passed", "total", "unsandboxed", "worker", "worker_args"}
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
	if first["name"] != "describe/reports-runtime" || first["passed"] != true {
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
	for _, name := range []string{"describe/reports-runtime", "engine/exits-on-eof"} {
		if !strings.Contains(during, name) {
			t.Errorf("obligation %s had settled but was not on the terminal:\n%s", name, during)
		}
	}
	if strings.Contains(during, "engine/becomes-ready") {
		t.Errorf("an obligation that has not settled was reported:\n%s", during)
	}
	if strings.Contains(during, "obligations,") {
		t.Errorf("the summary was written before the run finished:\n%s", during)
	}

	close(release)
	if err := <-done; !errors.Is(err, errReported) {
		t.Fatalf("reportConformance = %v, want errReported", err)
	}
	if !strings.Contains(stdout.String(), "FAIL  engine/becomes-ready") {
		t.Errorf("the stalled obligation's verdict never arrived:\n%s", stdout.String())
	}
}

// TestConformanceOfflineDescribesAndLaunchesNothing is the default exam: with
// no --allow-inference the suite grades what "code engine --describe" reports
// and nothing else runs, so an operator can check a Code build without a
// provider, a credential or a bill.
//
// The fake engine only opens its record file inside a job, so the file's
// absence afterwards is the proof that no job was launched.
func TestConformanceOfflineDescribesAndLaunchesNothing(t *testing.T) {
	f := newFixture(t)
	record := filepath.Join(f.root, "engine-record")

	stdout, _ := f.ok("conformance", fakeEnginePath, "--worker-arg", "-record", "--worker-arg", record,
		"--profile", "synthetic@1", "--json")
	res := decode[conformanceResult](t, stdout)
	if !res.OK || res.Inference || res.Unsandboxed || res.Total != 3 || res.Passed != 3 || res.Failed != 0 {
		t.Errorf("offline exam = %+v, want three passing obligations and no launch", res)
	}
	if res.Worker != fakeEnginePath || !slices.Equal(res.WorkerArgs, []string{"-record", record}) || res.Profile != "synthetic@1" {
		t.Errorf("the report does not name what was examined: %+v", res)
	}
	names := make([]string, 0, len(res.Obligations))
	for _, row := range res.Obligations {
		names = append(names, row.Name)
	}
	want := []string{"describe/reports-runtime", "describe/declares-no-credential", "describe/resolves-profile"}
	if !slices.Equal(names, want) {
		t.Errorf("obligations = %v, want %v", names, want)
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Error("an offline exam launched an engine job")
	}

	// Without a profile, the exam describes Code's default and the profile
	// obligation is not graded, because there is nothing to hold it to.
	stdout, _ = f.ok("conformance", fakeEnginePath, "--json")
	res = decode[conformanceResult](t, stdout)
	if !res.OK || res.Total != 2 || res.Profile != "" {
		t.Errorf("profile-less exam = %+v, want two passing obligations", res)
	}
}

// TestConformanceInferenceNeedsAProfile: a launch spends, so the exam refuses
// to launch under a profile nobody named rather than under whatever Code
// would default to.
func TestConformanceInferenceNeedsAProfile(t *testing.T) {
	f := newFixture(t)
	_, stderr := f.mustExit(exitUsage, "conformance", fakeEnginePath, "--allow-inference")
	if !strings.Contains(stderr, "--profile") {
		t.Errorf("the refusal does not name the missing flag:\n%s", stderr)
	}
}

// TestConformanceInferenceGradesOneEngineJob is the full exam against the
// fake engine: the spend is disclosed on stderr before the launch, one job
// runs under the named profile, and every engine obligation holds when the
// engine submits the nonce the prompt asked for.
func TestConformanceInferenceGradesOneEngineJob(t *testing.T) {
	f := newFixture(t)
	payload := filepath.Join(f.root, "answer.json")
	if err := os.WriteFile(payload, []byte(`{"answer":"${param:`+worker.ConformanceAnswerParam+`}"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := f.ok("conformance", fakeEnginePath, "--worker-arg", "-submit", "--worker-arg", payload,
		"--profile", "synthetic@1", "--allow-inference")
	if !strings.Contains(stderr, "launching one engine job under profile synthetic@1") {
		t.Errorf("the spend was not disclosed before the launch:\n%s", stderr)
	}
	for _, name := range []string{
		"describe/reports-runtime", "describe/declares-no-credential", "describe/resolves-profile",
		"engine/becomes-ready", "engine/declares-containment", "engine/registers-tools",
		"engine/submits-under-schema", "engine/exits-on-eof", "engine/reports-resources",
	} {
		if !strings.Contains(stdout, "ok    "+name+"\n") {
			t.Errorf("obligation %s did not pass:\n%s", name, stdout)
		}
	}
	if !strings.HasSuffix(stdout, "\n9 obligations, 9 passed, 0 failed\n") {
		t.Errorf("report did not end with the summary:\n%s", stdout)
	}

	stdout, _ = f.ok("conformance", fakeEnginePath, "--worker-arg", "-submit", "--worker-arg", payload,
		"--profile", "synthetic@1", "--allow-inference", "--json")
	res := decode[conformanceResult](t, stdout)
	if !res.OK || !res.Inference || res.Total != 9 || res.Passed != 9 {
		t.Errorf("launched exam = %+v, want nine passing obligations", res)
	}

	// An engine that ends its turn without submitting fails exactly the
	// obligation about submitting; the rest of the launch is still graded
	// on its own merits.
	stdout, _ = f.mustExit(exitFailure, "conformance", fakeEnginePath, "--worker-arg", "-no-submit",
		"--profile", "synthetic@1", "--allow-inference", "--json")
	res = decode[conformanceResult](t, stdout)
	if res.OK || res.Failed != 1 {
		t.Errorf("exam of a silent engine = %+v, want one failure", res)
	}
	for _, row := range res.Obligations {
		if row.Passed == (row.Name == "engine/submits-under-schema") {
			t.Errorf("obligation %s passed=%v: %v", row.Name, row.Passed, row.Failures)
		}
	}
}
