package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/worker"
)

// fakeEnginePath is the synthetic `code engine`, built once per test binary.
var fakeEnginePath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "babel-worker-fixture-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating fixture dir: %v\n", err)
		os.Exit(1)
	}
	fakeEnginePath = filepath.Join(dir, "fakeengine")
	build := exec.Command("go", "build", "-o", fakeEnginePath,
		"github.com/atyrode/babel/internal/worker/testdata/fakeengine")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "building fakeengine: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var testProfile = worker.ProfileRef{ID: "synthetic-profile", Revision: 1}

// answerSchema is the one-field result schema the fixture submits under.
var answerSchema = json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)

// searchSchema is a corpus-search argument schema: the query and nothing else.
var searchSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"scope":{"type":"string"}},"additionalProperties":false}`)

func limits() worker.Limits {
	return worker.Limits{
		HandshakeTimeout: 10 * time.Second,
		IdleTimeout:      5 * time.Second,
		ExitGrace:        2 * time.Second,
		TerminateGrace:   300 * time.Millisecond,
		DrainGrace:       300 * time.Millisecond,
	}
}

func client(t *testing.T, args []string, mutate ...func(*worker.Config)) *worker.Client {
	t.Helper()
	cfg := worker.Config{Binary: fakeEnginePath, Args: args, Limits: limits(), Authorizer: worker.AllowWithinGrant()}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := worker.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// submission writes a result file the fixture submits.
func submission(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "submit.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func job(mutate ...func(*worker.Job)) worker.Job {
	j := worker.Job{
		JobID:   "j-1",
		RunID:   "r-1",
		Profile: testProfile,
		Recipes: []worker.RecipeRef{{ID: "outcome-integrity", Version: 3}},
		Grant:   worker.Grant{Capabilities: []worker.Capability{worker.CapabilityCorpusSearch}, Disclosure: worker.DisclosureLocal},
		Sources: []worker.Source{{Kind: "session", Selector: "omp/s-1", Digest: "sha256:" + strings.Repeat("0", 64)}},
		Tools: []worker.HostTool{{
			Name: worker.ToolSearch, Capability: worker.CapabilityCorpusSearch, Parameters: searchSchema,
			Description: "search",
		}},
		Output: worker.OutputContract{Schema: "test.answer/1", JSONSchema: answerSchema, Instructions: "answer"},
		Prompt: "Answer.\n\n[babel-params]\nbabel.stage = explore\n[end]\n",
	}
	for _, m := range mutate {
		m(&j)
	}
	return j
}

func run(t *testing.T, args []string, mutate ...func(*worker.Config)) (*worker.Receipt, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client(t, args, mutate...).Run(ctx, job())
}

// TestWellBehavedRunProducesAReceipt is the whole boundary on the happy path:
// the launch, the sidecar, the containment check, tool registration, an
// evidence call served, a submission accepted, the engine's own accounting,
// the exit on EOF and Code's finished report.
func TestWellBehavedRunProducesAReceipt(t *testing.T) {
	served := filepath.Join(t.TempDir(), "served")
	recorded := filepath.Join(t.TempDir(), "stdin")
	var progress []worker.ProgressRecord
	receipt, err := run(t, []string{
		"-call", worker.ToolSearch, "-submit", submission(t, `{"answer":"forty-two"}`),
		"-served-file", served, "-record", recorded,
	}, func(cfg *worker.Config) {
		cfg.Authorizer = worker.AuthorizerFunc(func(_ context.Context, req worker.ToolRequest) worker.Decision {
			if req.Capability != worker.CapabilityCorpusSearch || req.Tool != worker.ToolSearch {
				t.Errorf("authorizer saw %s/%s", req.Capability, req.Tool)
			}
			var args struct {
				Query string `json:"query"`
			}
			if json.Unmarshal(req.Arguments, &args) != nil || args.Query != "synthetic" {
				t.Errorf("authorizer saw arguments %s", req.Arguments)
			}
			return worker.Decision{Allow: true, Reason: "served", Results: json.RawMessage(`{"hits":[{"excerpt":"a hit"}]}`)}
		})
		cfg.OnProgress = func(p worker.ProgressRecord) { progress = append(progress, p) }
	})
	if err != nil {
		t.Fatalf("Run: %v (receipt %+v)", err, receipt)
	}
	if receipt.Worker.Name != "fakeengine" || receipt.Profile != testProfile {
		t.Errorf("receipt attributes the run to %+v under %s", receipt.Worker, receipt.Profile)
	}
	if receipt.Containment.Backend != "synthetic-bwrap" || !receipt.Containment.NetworkDefaultDeny {
		t.Errorf("containment = %+v, want the declared sandbox", receipt.Containment)
	}
	if receipt.Metadata["model"] != "synthetic-1" || receipt.Cost.Currency != "USD" {
		t.Errorf("metadata/cost = %v / %+v", receipt.Metadata, receipt.Cost)
	}
	if !equalStrings(receipt.Tools, []string{worker.ToolSearch, worker.ToolSubmit}) {
		t.Errorf("registered tools = %v", receipt.Tools)
	}
	if len(receipt.ToolRequests) != 2 {
		t.Fatalf("recorded %d tool calls, want the search and the submission: %+v", len(receipt.ToolRequests), receipt.ToolRequests)
	}
	search, submit := receipt.ToolRequests[0], receipt.ToolRequests[1]
	if !search.Allowed || search.Capability != worker.CapabilityCorpusSearch || search.ArgumentsDigest == "" || search.ArgumentsBytes == 0 {
		t.Errorf("search record = %+v", search)
	}
	if !submit.Allowed || submit.Tool != worker.ToolSubmit || submit.Capability != "" {
		t.Errorf("submit record = %+v", submit)
	}
	if receipt.Result == nil || receipt.Result.Schema != "test.answer/1" || string(receipt.Result.Payload) != `{"answer":"forty-two"}` {
		t.Errorf("result = %+v", receipt.Result)
	}
	if receipt.Submissions != 1 || receipt.Result.Status != worker.StatusOK {
		t.Errorf("submissions = %d status = %q", receipt.Submissions, receipt.Result.Status)
	}
	if receipt.Usage == nil || receipt.Usage.TotalTokens != 1540 || receipt.Usage.ToolCalls != 2 {
		t.Errorf("usage = %+v, want the engine's own stats", receipt.Usage)
	}
	if receipt.Resources == nil || receipt.Resources.CPUSeconds == nil || *receipt.Resources.CPUSeconds != 0.42 {
		t.Errorf("resources = %+v, want Code's finished report", receipt.Resources)
	}
	if receipt.ExitCode != 0 || receipt.Duration <= 0 {
		t.Errorf("exit %d duration %s", receipt.ExitCode, receipt.Duration)
	}
	if receipt.Failure != nil {
		t.Errorf("a clean run recorded a failure: %+v", receipt.Failure)
	}
	if len(progress) == 0 || receipt.Progress[len(receipt.Progress)-1].Message != "turn ended" {
		t.Errorf("progress = %+v", receipt.Progress)
	}

	// The served evidence reached the model as the tool result's text, and
	// nothing of it reached the receipt.
	servedBytes, _ := os.ReadFile(served)
	if !strings.Contains(string(servedBytes), `"excerpt":"a hit"`) {
		t.Errorf("the model was served %q", servedBytes)
	}
	if encoded, _ := json.Marshal(receipt); bytes.Contains(encoded, []byte("a hit")) {
		t.Error("served evidence reached the receipt")
	}
	// The prompt was the last thing written before the turn: nothing about
	// the run travelled before the containment check.
	stdin, _ := os.ReadFile(recorded)
	lines := strings.Split(strings.TrimSpace(string(stdin)), "\n")
	var kinds []string
	for _, line := range lines {
		var msg struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(line), &msg)
		kinds = append(kinds, msg.Type)
	}
	if !equalStrings(kinds[:3], []string{"negotiate_protocol", "set_host_tools", "prompt"}) {
		t.Errorf("Babel wrote %v, want negotiate, tools, prompt first", kinds)
	}
}

// TestLaunchIsRefusedBeforeThePromptWhenCodeFallsShort covers every refusal
// made from the sidecar: a missing file, a weak sandbox, a wrong profile and
// credential-shaped metadata all end the launch with nothing written to the
// engine, and the process is given the grace to leave on EOF.
func TestLaunchIsRefusedBeforeThePromptWhenCodeFallsShort(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want error
	}{
		{"no runtime-info", []string{"-no-runtime-info"}, worker.ErrRuntimeInfo},
		{"weak containment", []string{"-containment", "weak"}, worker.ErrContainment},
		{"no containment", []string{"-containment", "missing"}, worker.ErrContainment},
		{"wrong profile", []string{"-profile-override", "other@1"}, worker.ErrProfileMismatch},
		{"secret metadata", []string{"-secret-metadata"}, worker.ErrSecretDeclared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorded := filepath.Join(t.TempDir(), "stdin")
			args := append([]string{"-record", recorded, "-submit", submission(t, `{"answer":"x"}`)}, tc.args...)
			receipt, err := run(t, args)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run error = %v, want %v", err, tc.want)
			}
			if receipt == nil || receipt.Failure == nil || receipt.Failure.Origin != worker.FailureBabel {
				t.Fatalf("receipt = %+v, want a Babel-side failure recorded", receipt)
			}
			if stdin, _ := os.ReadFile(recorded); len(bytes.TrimSpace(stdin)) != 0 {
				t.Errorf("Babel wrote to a refused engine: %s", stdin)
			}
			if receipt.Result != nil {
				t.Error("a refused launch produced a result")
			}
		})
	}
}

// TestUnsandboxedRequirementAdmitsAWeakSandbox: the requirement is the caller's
// to relax, and relaxing it is a statement rather than a default.
func TestUnsandboxedRequirementAdmitsAWeakSandbox(t *testing.T) {
	relaxed := worker.Unsandboxed()
	receipt, err := run(t, []string{"-containment", "weak", "-submit", submission(t, `{"answer":"x"}`)},
		func(cfg *worker.Config) { cfg.Requirement = &relaxed })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Containment.NetworkDefaultDeny {
		t.Error("the receipt does not record the weak sandbox that actually ran")
	}
}

// TestSubmissionRulesHold: a refused submission never replaces an accepted
// one, the model reads the refusal, and every attempt is counted.
func TestSubmissionRulesHold(t *testing.T) {
	served := filepath.Join(t.TempDir(), "served")
	ctx := context.Background()
	c := client(t, []string{
		"-submit", submission(t, `{"answer":"first"}`),
		"-submit-invalid", `{"answer":"forbidden"}`,
		"-served-file", served,
	})
	receipt, err := c.Run(ctx, job(func(j *worker.Job) {
		j.Accept = func(payload json.RawMessage) error {
			if bytes.Contains(payload, []byte("forbidden")) {
				return errors.New("that answer is forbidden")
			}
			return nil
		}
	}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Submissions != 2 || string(receipt.Result.Payload) != `{"answer":"first"}` {
		t.Errorf("submissions = %d result = %s", receipt.Submissions, receipt.Result.Payload)
	}
	servedBytes, _ := os.ReadFile(served)
	if !strings.Contains(string(servedBytes), "babel_submit_result\ttrue\tsubmission refused: that answer is forbidden") {
		t.Errorf("the model did not read the refusal: %q", servedBytes)
	}
	refused := receipt.ToolRequests[1]
	if refused.Allowed || refused.DenyCode != worker.DenyPolicy {
		t.Errorf("refused submission recorded as %+v", refused)
	}

	// An invalid submission before any valid one leaves no result until the
	// valid one lands.
	receipt, err = client(t, []string{
		"-submit", submission(t, `{"answer":"second"}`),
		"-submit-invalid", `{"answer":"forbidden"}`, "-submit-invalid-first",
	}).Run(ctx, job(func(j *worker.Job) {
		j.Accept = func(payload json.RawMessage) error {
			if bytes.Contains(payload, []byte("forbidden")) {
				return errors.New("forbidden")
			}
			return nil
		}
	}))
	if err != nil || string(receipt.Result.Payload) != `{"answer":"second"}` {
		t.Errorf("Run = %v, result %s", err, receipt.Result.Payload)
	}

	// No accepted submission is ErrNoResult with the attempts counted.
	receipt, err = client(t, []string{"-submit-invalid", `{"answer":"forbidden"}`, "-no-submit"}).Run(ctx,
		job(func(j *worker.Job) { j.Accept = func(json.RawMessage) error { return errors.New("no") } }))
	if !errors.Is(err, worker.ErrNoResult) || receipt.Result != nil || receipt.Submissions != 1 {
		t.Errorf("Run = %v, receipt %+v", err, receipt)
	}
}

// TestToolCallsAreAuthorizedInFixedOrder: an unregistered name is refused
// before policy, the policy's denial is answered as a tool error and the run
// continues, and the budget is enforced after it.
func TestToolCallsAreAuthorizedInFixedOrder(t *testing.T) {
	served := filepath.Join(t.TempDir(), "served")
	receipt, err := run(t, []string{
		"-call", worker.ToolSearch, "-call-unknown", "-submit", submission(t, `{"answer":"x"}`), "-served-file", served,
	}, func(cfg *worker.Config) { cfg.Authorizer = worker.DenyAll("policy says no") })
	if err != nil {
		t.Fatalf("a denial ended the run: %v", err)
	}
	if len(receipt.ToolRequests) != 3 {
		t.Fatalf("recorded %d calls: %+v", len(receipt.ToolRequests), receipt.ToolRequests)
	}
	if receipt.ToolRequests[0].DenyCode != worker.DenyPolicy || receipt.ToolRequests[1].DenyCode != worker.DenyUnknownTool {
		t.Errorf("deny codes = %s, %s", receipt.ToolRequests[0].DenyCode, receipt.ToolRequests[1].DenyCode)
	}
	if receipt.Denied() != 2 {
		t.Errorf("Denied() = %d", receipt.Denied())
	}
	servedBytes, _ := os.ReadFile(served)
	if !strings.Contains(string(servedBytes), "babel_corpus_search\ttrue\trefused (policy): policy says no") {
		t.Errorf("the model did not read the denial: %q", servedBytes)
	}

	// The budget: three calls against a budget of two are denied with
	// DenyLimit, and an engine that keeps asking past the slack ends the run.
	receipt, err = run(t, []string{"-call", worker.ToolSearch, "-call-repeat", "3", "-submit", submission(t, `{"answer":"x"}`)},
		func(cfg *worker.Config) { cfg.Limits.MaxToolRequests = 2 })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.ToolRequests[2].DenyCode != worker.DenyLimit {
		t.Errorf("third call = %+v, want a limit denial", receipt.ToolRequests[2])
	}
	_, err = run(t, []string{"-call", worker.ToolSearch, "-call-repeat", "40", "-no-submit"},
		func(cfg *worker.Config) { cfg.Limits.MaxToolRequests = 2 })
	if !errors.Is(err, worker.ErrToolBudget) {
		t.Errorf("Run error = %v, want the tool budget exhausted", err)
	}
}

// TestJobIsValidatedBeforeLaunch: a tool the grant does not cover, a name
// Babel does not serve, or a missing prompt never launches anything.
func TestJobIsValidatedBeforeLaunch(t *testing.T) {
	cases := map[string]func(*worker.Job){
		"ungranted tool": func(j *worker.Job) {
			j.Tools = append(j.Tools, worker.HostTool{Name: worker.ToolFetch, Capability: worker.CapabilityPublicResearch, Parameters: searchSchema})
		},
		"unserved name": func(j *worker.Job) { j.Tools[0].Name = "babel_anything" },
		"no prompt":     func(j *worker.Job) { j.Prompt = "" },
		"no schema":     func(j *worker.Job) { j.Output.JSONSchema = nil },
		"duplicate":     func(j *worker.Job) { j.Tools = append(j.Tools, j.Tools[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			receipt, err := client(t, nil).Run(context.Background(), job(mutate))
			if err == nil || receipt != nil {
				t.Errorf("Run = %+v, %v; want a refusal with no receipt", receipt, err)
			}
		})
	}
}

// TestTransportFailuresAreNamed: each way the stream can break is a distinct
// sentinel, and each ends the run with the tree reaped.
func TestTransportFailuresAreNamed(t *testing.T) {
	submit := submission(t, `{"answer":"x"}`)
	cases := []struct {
		name string
		args []string
		want error
	}{
		{"no ready", []string{"-no-ready"}, worker.ErrHandshakeTimeout},
		{"v1 only", []string{"-ready-versions", "1"}, worker.ErrProtocolMismatch},
		{"stalls", []string{"-stall-after", "tools"}, worker.ErrWorkerStalled},
		{"malformed", []string{"-bad-frame", "malformed", "-submit", submit}, worker.ErrMalformedFrame},
		{"oversized", []string{"-bad-frame", "oversized", "-submit", submit}, worker.ErrOversizedFrame},
		{"interleaved chunks", []string{"-chunk", "-bad-frame", "chunk-interleaved", "-submit", submit}, worker.ErrMalformedFrame},
		{"short chunks", []string{"-chunk", "-bad-frame", "chunk-short", "-submit", submit}, worker.ErrMalformedFrame},
		{"refuses tools", []string{"-refuse-command", "set_host_tools"}, worker.ErrCommandFailed},
		{"drops a tool", []string{"-drop-tools"}, worker.ErrCommandFailed},
		{"closes stdout", []string{"-no-agent-end", "-submit", submit}, worker.ErrEngineExited},
		{"local prompt", []string{"-local-prompt"}, worker.ErrNoResult},
		{"lingers", []string{"-ignore-eof", "-submit", submit}, worker.ErrWorkerLingered},
		{"dirty exit", []string{"-exit-code", "3", "-submit", submit}, worker.ErrDirtyExit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipt, err := run(t, tc.args, func(cfg *worker.Config) {
				cfg.Limits.HandshakeTimeout = 1500 * time.Millisecond
				cfg.Limits.IdleTimeout = 1500 * time.Millisecond
				cfg.Limits.ExitGrace = 700 * time.Millisecond
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run error = %v, want %v", err, tc.want)
			}
			if receipt == nil || receipt.Failure == nil {
				t.Fatalf("no failure recorded: %+v", receipt)
			}
		})
	}
}

// TestChunkedFramesAreReassembled: v2 framing is what makes a large tool result
// or a long stream lossless, so a fixture that chunks everything must produce
// the same receipt as one that does not.
func TestChunkedFramesAreReassembled(t *testing.T) {
	receipt, err := run(t, []string{"-chunk", "-call", worker.ToolSearch, "-submit", submission(t, `{"answer":"chunked"}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Result == nil || string(receipt.Result.Payload) != `{"answer":"chunked"}` {
		t.Errorf("result = %+v", receipt.Result)
	}
}

// TestEngineSideChannelsAreAnswered: an extension dialog is cancelled, a host
// URI request is refused, a non-terminal agent_end is not the end, an unknown
// frame is recorded by type, and a refused stats command costs no result.
func TestEngineSideChannelsAreAnswered(t *testing.T) {
	receipt, err := run(t, []string{
		"-extension-ui", "-uri-request", "-non-terminal-end", "-unknown-frames", "-stats-refused",
		"-submit", submission(t, `{"answer":"x"}`),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if receipt.Result == nil {
		t.Fatal("the run lost its result")
	}
	if !equalStrings(receipt.UnknownFrames, []string{"telemetry_sample"}) {
		t.Errorf("unknown frames = %v", receipt.UnknownFrames)
	}
	if receipt.Usage != nil {
		t.Error("a refused stats command produced usage")
	}
}

// TestCancellationAbortsAndReapsTheTree: cancelling the context aborts the
// engine and kills every descendant, grandchild included.
func TestCancellationAbortsAndReapsTheTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	receipt, err := client(t, []string{"-grandchild", pidFile, "-stall-after", "prompt", "-ignore-terminate"}).Run(ctx, job())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	if receipt == nil || receipt.Failure == nil || receipt.Failure.Code != "cancelled" {
		t.Errorf("receipt = %+v", receipt)
	}
	data, _ := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid == 0 {
		t.Fatal("the grandchild never published its pid")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("grandchild %d survived the teardown", pid)
}

// TestNoSecretReachesArgvOrEnvironment: the child sees the profile and the
// sidecar path on argv and a minimal environment, and nothing else.
func TestNoSecretReachesArgvOrEnvironment(t *testing.T) {
	t.Setenv("PROVIDER_API_KEY", "sk-should-not-travel")
	dump := filepath.Join(t.TempDir(), "dump")
	if _, err := run(t, []string{"-dump", dump, "-submit", submission(t, `{"answer":"x"}`)}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(dump)
	if strings.Contains(string(data), "sk-should-not-travel") {
		t.Error("the parent's environment leaked into the engine")
	}
	if !strings.Contains(string(data), "engine --profile synthetic-profile@1 --runtime-info ") {
		t.Errorf("argv = %s", strings.SplitN(string(data), "\n", 2)[0])
	}
}

// TestConfigureDescribesWithoutLaunching: Configure runs --describe, reads the
// runtime document, refuses credential metadata and a profile mismatch.
func TestConfigureDescribesWithoutLaunching(t *testing.T) {
	ctx := context.Background()
	cfg, err := client(t, nil).Configure(ctx, &testProfile)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if cfg.Profile != testProfile || cfg.Worker.Name != "fakeengine" || cfg.Cost.Currency != "USD" || cfg.Metadata["model"] != "synthetic-1" {
		t.Errorf("configuration = %+v", cfg)
	}
	if _, err := client(t, []string{"-secret-metadata"}).Configure(ctx, nil); !errors.Is(err, worker.ErrSecretDeclared) {
		t.Errorf("secret metadata: %v", err)
	}
	other := worker.ProfileRef{ID: "other", Revision: 2}
	if _, err := client(t, []string{"-profile-override", "synthetic-profile@1"}).Configure(ctx, &other); !errors.Is(err, worker.ErrProfileMismatch) {
		t.Errorf("profile mismatch: %v", err)
	}
	if _, err := client(t, []string{"-describe-exit", "1"}).Configure(ctx, nil); !errors.Is(err, worker.ErrDirtyExit) {
		t.Errorf("describe failure: %v", err)
	}
}

// TestConformanceGradesOfflineAndWithInference: without inference only the
// describe obligations run and nothing is launched; with it, the fixture's
// engine passes every obligation, and a fixture that submits the wrong answer
// fails exactly the submission one.
func TestConformanceGradesOfflineAndWithInference(t *testing.T) {
	ctx := context.Background()
	recorded := filepath.Join(t.TempDir(), "stdin")
	offline := worker.StreamConformance(ctx, worker.ConformanceOptions{
		Binary: fakeEnginePath, Args: []string{"-record", recorded}, Profile: &testProfile, Limits: limits(),
	}, nil)
	if names := resultNames(offline); !equalStrings(names, []string{"describe/reports-runtime", "describe/declares-no-credential", "describe/resolves-profile"}) {
		t.Errorf("offline obligations = %v", names)
	}
	for _, r := range offline {
		if !r.Passed {
			t.Errorf("%s failed offline: %v", r.Name, r.Failures)
		}
	}
	if _, err := os.Stat(recorded); err == nil {
		t.Error("an offline grading launched an engine")
	}

	answer := filepath.Join(t.TempDir(), "answer.json")
	os.WriteFile(answer, []byte(`{"answer":"${param:`+worker.ConformanceAnswerParam+`}"}`), 0o600)
	var settled []string
	full := worker.StreamConformance(ctx, worker.ConformanceOptions{
		Binary: fakeEnginePath, Args: []string{"-submit", answer}, Profile: &testProfile, Inference: true, Limits: limits(),
	}, func(r worker.ObligationResult) { settled = append(settled, r.Name) })
	if !equalStrings(settled, worker.ConformanceNames(worker.ConformanceOptions{Profile: &testProfile, Inference: true})) {
		t.Errorf("settled %v", settled)
	}
	for _, r := range full {
		if !r.Passed {
			t.Errorf("%s failed: %v", r.Name, r.Failures)
		}
	}

	wrong := worker.StreamConformance(ctx, worker.ConformanceOptions{
		Binary: fakeEnginePath, Args: []string{"-submit", submission(t, `{"answer":"not the nonce"}`)},
		Profile: &testProfile, Inference: true, Limits: limits(),
	}, nil)
	for _, r := range wrong {
		if r.Name == "engine/submits-under-schema" && r.Passed {
			t.Error("a wrong answer passed the submission obligation")
		}
		if r.Name == "engine/registers-tools" && !r.Passed {
			t.Errorf("a wrong answer failed an unrelated obligation: %v", r.Failures)
		}
	}
}

func resultNames(results []worker.ObligationResult) []string {
	var names []string
	for _, r := range results {
		names = append(names, r.Name)
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
