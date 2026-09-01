package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/synth"
	"github.com/atyrode/babel/internal/worker"
)

// The Phase B round trip, driven end to end through the shipped executable:
// generate a synthetic corpus, back it up, fix a corpus scope, explore it
// through a worker process, review what the run produced, and export it.
//
// It is one scenario rather than a set of cases because the property under
// test is that these commands compose over one machine's durable state,
// which per-command tests cannot observe. After every step the assertion is
// against that durable state — the preparation record, the receipt, the
// frontier, the append-only disposition log — and not against an exit code:
// a command that printed a plausible document and stored nothing would pass
// an exit-code test and fail this one.
//
// Nothing here needs Code, a provider credential, a network, or a real
// transcript. The worker is the synthetic protocol counterpart from
// internal/worker/testdata, and every corpus byte is generated (SPEC.md §10).

// fakeWorker builds the synthetic analysis worker once per package run. It
// is the same fixture internal/explore drives; building it here rather than
// per test keeps one compile from becoming several.
var (
	fakeWorkerOnce sync.Once
	fakeWorkerPath string
	fakeWorkerErr  error
)

func fakeWorker(t *testing.T) string {
	t.Helper()
	fakeWorkerOnce.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			fakeWorkerErr = err
			return
		}
		// Not t.TempDir(): the binary outlives the test that built it.
		dir, err := os.MkdirTemp("", "babel-fakeworker-*")
		if err != nil {
			fakeWorkerErr = err
			return
		}
		path := filepath.Join(dir, "fakeworker")
		cmd := exec.Command(goTool, "build", "-o", path,
			"github.com/atyrode/babel/internal/worker/testdata/fakeworker")
		cmd.Dir = repoRoot(t)
		// The environment this process started with: by the time a test
		// needs the worker it has already pointed HOME at a synthetic tree,
		// and `go build` would put its module cache there.
		cmd.Env = baseEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeWorkerErr = err
			t.Logf("go build fakeworker: %s", out)
			return
		}
		fakeWorkerPath = path
	})
	if fakeWorkerErr != nil {
		t.Skipf("cannot build the synthetic analysis worker: %v", fakeWorkerErr)
	}
	return fakeWorkerPath
}

// explorationOperator is the attributed identity every mutation in this
// scenario is recorded under. §4.7 refuses an anonymous one.
const explorationOperator = "synthetic-e2e-operator"

// exploreProfile is a synthetic Code profile reference. Babel stores only
// the reference (SPEC.md §2.6), and nothing behind it exists here.
const exploreProfile = "synthetic-profile@1"

// explorationSeed selects the generated corpus. One seed is one tree.
const explorationSeed = 20260830

// phaseB is one hermetic Phase B environment: the suite's isolated HOME and
// XDG roots, a generated corpus moved onto the adapters' default roots, and
// the executable under test.
type phaseB struct {
	*env
	bin    string
	corpus *synth.Corpus
}

// corpusPlacement maps the tree synth writes onto the roots the adapters
// resolve: synth writes `omp/agent/...`, `codex/`, and `claude/`, while the
// adapters look in `~/.omp/agent/...`, $CODEX_HOME, and `~/.claude`.
func (p *phaseB) corpusPlacement(staging string) [][2]string {
	return [][2]string{
		{filepath.Join(staging, synth.HarnessOMP), filepath.Join(p.home, ".omp")},
		{filepath.Join(staging, synth.HarnessCodex), p.codexHome},
		{filepath.Join(staging, synth.HarnessClaude), p.claudeHome},
	}
}

func newPhaseB(t *testing.T) *phaseB {
	t.Helper()
	e := newEnv(t)
	p := &phaseB{env: e, bin: babelBinary(t)}

	// Generated beside the home tree and then moved onto it. Moving rather
	// than copying keeps one copy of a corpus that can be large, and both
	// paths are inside the same temporary directory so the rename is cheap.
	staging := filepath.Join(e.root, "corpus")
	corpus, err := synth.Generate(staging, synth.Profile{
		Seed:                explorationSeed,
		OMPSessions:         2,
		CodexSessions:       1,
		ClaudeSessions:      1,
		SizeBuckets:         []synth.SizeBucket{{Bytes: 8 << 10, Weight: 1}},
		ArtifactsPerSession: [2]int{0, 1},
		BlobCount:           2,
	})
	if err != nil {
		t.Fatalf("generate the synthetic corpus: %v", err)
	}
	for _, move := range p.corpusPlacement(staging) {
		if err := os.MkdirAll(filepath.Dir(move[1]), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(move[0], move[1]); err != nil {
			t.Fatalf("place the corpus at %s: %v", move[1], err)
		}
	}
	// The recorded paths follow the tree, so a later locator names where the
	// file actually is rather than where it was generated.
	for i := range corpus.Sessions {
		for _, move := range p.corpusPlacement(staging) {
			rel, err := filepath.Rel(move[0], corpus.Sessions[i].Path)
			if err == nil && !strings.HasPrefix(rel, "..") {
				corpus.Sessions[i].Path = filepath.Join(move[1], rel)
				break
			}
		}
	}
	p.corpus = corpus
	return p
}

// exec drives one invocation of the built binary with this environment.
// Every command in this scenario goes through a real process, because the
// thing under test is the shipped executable rather than a library call.
func (p *phaseB) exec(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(p.bin, args...)
	cmd.Env = p.processEnv(t)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("babel %s: %v", strings.Join(args, " "), err)
		}
		code = exit.ExitCode()
	}
	return out.String(), errb.String(), code
}

// processEnv is this environment's roots for an out-of-process run.
func (p *phaseB) processEnv(t *testing.T) []string {
	t.Helper()
	out := []string{
		"HOME=" + p.home,
		"XDG_CONFIG_HOME=" + p.configHome,
		"XDG_DATA_HOME=" + p.dataHome,
		"XDG_CACHE_HOME=" + p.cacheHome,
		"CODEX_HOME=" + p.codexHome,
		"BABEL_HOST_ID=" + hostID,
		// Deliberately empty: an operator identity and a worker must come
		// from the command line here, so their absence stays testable.
		"BABEL_OPERATOR=",
		"BABEL_ANALYSIS_WORKER=",
		"PATH=" + filepath.Dir(resticBinary(t)) + string(os.PathListSeparator) + basePATH,
	}
	for _, k := range []string{"TMPDIR", "LANG", "TERM"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// okExec drives one invocation that must succeed.
func (p *phaseB) okExec(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, code := p.exec(t, args...)
	if code != exitOK {
		t.Fatalf("babel %s exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

// execJSON drives one successful --json invocation and decodes the single
// document it wrote to stdout, holding it to the stream contract.
func execJSON[T any](t *testing.T, p *phaseB, args ...string) T {
	t.Helper()
	stdout, stderr := p.okExec(t, args...)
	assertNoJSONOnStderr(t, strings.Join(args, " "), stderr)
	return decode[T](t, stdout)
}

// durableDir is where the Phase B stores live, so a step's assertion can be
// made against the database rather than against the command's own report.
func (p *phaseB) durableDir() string { return filepath.Join(p.dataHome, "babel") }

// Machine-readable Phase B shapes, mirrored here for the same consumer-side
// reason as the Phase A shapes: decoding with DisallowUnknownFields makes
// this suite a contract test of the documents the CLI emits.

type prepareSessionRow struct {
	Harness       string `json:"harness"`
	SourceID      string `json:"source_id"`
	Selector      string `json:"selector"`
	Host          string `json:"host"`
	CaptureDigest string `json:"capture_digest"`
	SourceDigest  string `json:"source_digest"`
	Bytes         int64  `json:"bytes"`
	Records       int    `json:"records"`
	Events        int    `json:"events"`
}

type prepareDoc struct {
	PreparationID string              `json:"preparation_id"`
	PreparedAt    string              `json:"prepared_at"`
	Host          string              `json:"host"`
	Sessions      []prepareSessionRow `json:"sessions"`
	IndexedEvents int                 `json:"indexed_events"`
	// FrontierRecords, SalientTerms and Related are #87's mechanical
	// injection: how much of Babel's own output the index holds, the terms
	// the scope produced, and the prior records those terms found. A first
	// preparation on a fresh machine finds none of the last, which is a real
	// answer and the one this round trip observes.
	FrontierRecords int                 `json:"frontier_records"`
	SalientTerms    []string            `json:"salient_terms"`
	Related         []prepareRelatedRow `json:"related"`
	Serendipitous   bool                `json:"serendipitous"`
	Database        string              `json:"database"`
	Index           string              `json:"index"`
}

type prepareRelatedRow struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Status  string `json:"status"`
}

type preflightDoc struct {
	Version           int    `json:"version"`
	Disclosure        string `json:"disclosure"`
	RedactionRequired bool   `json:"redaction_required"`
	Inputs            int    `json:"inputs"`
	Bytes             int64  `json:"bytes"`
	Events            int    `json:"events"`
	SecretFindings    int    `json:"secret_findings"`
	Findings          int    `json:"findings"`
}

type failureDoc struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
	At      string `json:"at"`
}

type exploreDoc struct {
	RunID              string         `json:"run_id"`
	PreparationID      string         `json:"preparation_id"`
	ReceiptID          string         `json:"receipt_id"`
	ChallengeReceiptID string         `json:"challenge_receipt_id,omitempty"`
	SynthesisReceiptID string         `json:"synthesis_receipt_id,omitempty"`
	Profile            string         `json:"profile"`
	Recipes            []string       `json:"recipes"`
	Hypotheses         []string       `json:"hypotheses"`
	Observations       []string       `json:"observations"`
	Findings           []string       `json:"findings"`
	Proposals          []string       `json:"proposals"`
	Promoted           []string       `json:"promoted"`
	Deferred           []string       `json:"deferred"`
	Rejected           []string       `json:"rejected"`
	Objections         []string       `json:"objections"`
	Reused             int            `json:"reused"`
	Retrievals         int            `json:"retrievals"`
	Enrolled           int            `json:"enrolled_for_review"`
	Cancelled          bool           `json:"cancelled"`
	Preflight          *preflightDoc  `json:"preflight,omitempty"`
	Failures           []failureDoc   `json:"failures,omitempty"`
	Duplicates         []duplicateDoc `json:"duplicates,omitempty"`
}

type duplicateDoc struct {
	Hypothesis  string  `json:"hypothesis"`
	DuplicateOf string  `json:"duplicate_of"`
	Overlap     float64 `json:"overlap"`
}

type hypothesisDoc struct {
	ID           string   `json:"id"`
	AncestorID   string   `json:"ancestor_id,omitempty"`
	RunID        string   `json:"run_id"`
	Status       string   `json:"status"`
	ReviewStatus string   `json:"review_status"`
	CreatedAt    string   `json:"created_at"`
	Statement    string   `json:"statement"`
	Novelty      float64  `json:"novelty"`
	Priority     float64  `json:"priority"`
	OriginCues   []string `json:"origin_cues,omitempty"`
	Labels       []string `json:"provisional_labels,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

type hypothesesDoc struct {
	Hypotheses []hypothesisDoc `json:"hypotheses"`
	// Sync is the per-record sync state SPEC.md line 646 requires a listing
	// to keep rendering, keyed by record id (issue #109 item 3). It rides a
	// map beside the rows rather than a field on each row because the row
	// shape also serves the detail views, which resolve no sync state and
	// would carry an unexplained blank.
	//
	// On this machine every value is "local": the fixture runs with no shared
	// catalog and no publication journal, so nothing claims these records are
	// going anywhere - which is the honest answer and deliberately not
	// "pending-sync".
	Sync   map[string]string `json:"sync,omitempty"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

type evidenceDoc struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	ByteOffset int64  `json:"byte_offset"`
	Digest     string `json:"digest"`
	Note       string `json:"note,omitempty"`
}

type findingDoc struct {
	ID                    string        `json:"id"`
	AncestorID            string        `json:"ancestor_id,omitempty"`
	RunID                 string        `json:"run_id"`
	ReviewStatus          string        `json:"review_status"`
	CreatedAt             string        `json:"created_at"`
	Title                 string        `json:"title"`
	Pattern               string        `json:"pattern"`
	Significance          string        `json:"significance,omitempty"`
	Scope                 []string      `json:"scope,omitempty"`
	Recurrence            int           `json:"recurrence,omitempty"`
	TemporalStatus        string        `json:"temporal_status,omitempty"`
	ObservationIDs        []string      `json:"observation_ids"`
	HypothesisIDs         []string      `json:"hypothesis_ids"`
	CounterEvidence       []evidenceDoc `json:"counter_evidence,omitempty"`
	CounterEvidenceAbsent bool          `json:"counter_evidence_absent"`
}

// The three listings below carry the same per-record sync map for the same
// reason hypothesesDoc does; see its comment.
type findingsDoc struct {
	Findings []findingDoc      `json:"findings"`
	Sync     map[string]string `json:"sync,omitempty"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

type queueItemDoc struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Status        string `json:"status"`
	EnrolledAt    string `json:"enrolled_at"`
	Decisions     int    `json:"decisions"`
	LastDecidedAt string `json:"last_decided_at,omitempty"`
	Refinements   int    `json:"refinements"`
	Title         string `json:"title"`
}

type queueDoc struct {
	Items []queueItemDoc    `json:"items"`
	Sync  map[string]string `json:"sync,omitempty"`
}

type decisionDoc struct {
	ID            string `json:"id"`
	Sequence      int64  `json:"sequence"`
	Type          string `json:"type"`
	SubjectID     string `json:"subject_id"`
	Disposition   string `json:"disposition"`
	ReviewerID    string `json:"reviewer_id"`
	ContextID     string `json:"context_id,omitempty"`
	ContextText   string `json:"context_text,omitempty"`
	DuplicateOfID string `json:"duplicate_of_id,omitempty"`
	Note          string `json:"note,omitempty"`
	RecordedAt    string `json:"recorded_at"`
}

type decideDoc struct {
	Decision decisionDoc `json:"decision"`
	Status   string      `json:"status"`
}

// The issue #87 surface, as a script reads it: the proposed next actions a
// run attached to its records, the ledger of what the operator decided, the
// process-further queue, and the revision chain a record moves along.
type dispositionDoc struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	RecordID   string     `json:"record_id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	ProposedBy string     `json:"proposed_by"`
	Ref        string     `json:"ref,omitempty"`
	CreatedAt  string     `json:"created_at"`
	Summary    string     `json:"summary"`
	Rationale  string     `json:"rationale,omitempty"`
	Anchor     *anchorDoc `json:"anchor,omitempty"`
}

type anchorDoc struct {
	Workspace string `json:"workspace"`
	Remote    string `json:"remote"`
	URL       string `json:"url"`
	Branch    string `json:"branch,omitempty"`
}

type dispositionsDoc struct {
	Dispositions []dispositionDoc  `json:"dispositions"`
	Sync         map[string]string `json:"sync,omitempty"`
	Total        int               `json:"total"`
	Limit        int               `json:"limit"`
	Offset       int               `json:"offset"`
}

type ledgerEntryDoc struct {
	ID         string `json:"id"`
	Sequence   int64  `json:"sequence"`
	Ruling     string `json:"ruling"`
	By         string `json:"by"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

type dispositionDetailDoc struct {
	Disposition dispositionDoc   `json:"disposition"`
	Ledger      []ledgerEntryDoc `json:"ledger"`
	Draft       string           `json:"draft,omitempty"`
}

type decideDispositionDoc struct {
	Entry     ledgerEntryDoc `json:"entry"`
	Status    string         `json:"status"`
	Published string         `json:"published"`
}

type invitationDoc struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	RecordID   string `json:"record_id"`
	By         string `json:"by"`
	CreatedAt  string `json:"created_at"`
	ConsumedBy string `json:"consumed_by,omitempty"`
	ConsumedAt string `json:"consumed_at,omitempty"`
	Open       bool   `json:"open"`
}

type inviteDoc struct {
	Invitation invitationDoc `json:"invitation"`
}

type invitationsDoc struct {
	Invitations []invitationDoc `json:"invitations"`
}

type revisionDoc struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	RecordID     string `json:"record_id"`
	RootID       string `json:"root_id"`
	SupersedesID string `json:"supersedes_id,omitempty"`
	Sequence     int64  `json:"sequence"`
	Actor        string `json:"actor"`
	RecordedAt   string `json:"recorded_at"`
	Reason       string `json:"reason,omitempty"`
	Head         bool   `json:"head"`
}

type revisionsDoc struct {
	Type      string        `json:"type"`
	ID        string        `json:"id"`
	HeadID    string        `json:"head_id"`
	Revisions []revisionDoc `json:"revisions"`
}

type statusDoc struct {
	Sequence   int64  `json:"sequence"`
	Status     string `json:"status"`
	RunID      string `json:"run_id,omitempty"`
	Actor      string `json:"actor"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

type reviveDoc struct {
	Type   string    `json:"type"`
	ID     string    `json:"id"`
	Status statusDoc `json:"status"`
}

type reviseDocument struct {
	Revision   revisionDoc `json:"revision"`
	Supersedes string      `json:"supersedes"`
	Statement  string      `json:"statement"`
}

// TestSyntheticExplorationRoundTrip is the whole Phase B path from a real
// binary invocation, asserting durable state after every step.
func TestSyntheticExplorationRoundTrip(t *testing.T) {
	p := newPhaseB(t)
	ctx := context.Background()

	// The corpus is fingerprinted before anything runs. §4.6 and decision 13
	// put publishing, applying a proposal, and writing to a source outside
	// Babel entirely, so the source tree must be byte-identical at the end.
	before := fingerprintTree(t, p.home)

	// --- archive the corpus ------------------------------------------
	p.bootstrapRepo(t)
	push := execJSON[pushResult](t, p, p.with("archive", "push", "--json")...)
	if push.SnapshotID == "" || push.FilesProcessed == 0 {
		t.Fatalf("push = %+v, want a snapshot over captured files", push)
	}
	status := execJSON[statusResult](t, p, p.with("archive", "status", "--json")...)
	if len(status.Hosts) != 1 || status.Hosts[0].LatestID != push.SnapshotID {
		t.Fatalf("archive status = %+v, want the snapshot the push reported", status.Hosts)
	}

	// --- exploration is refused before a worker exists ----------------
	// This is the state a correctly installed Babel is in today, because
	// Code does not implement the worker protocol yet, so the round trip
	// asserts the refusal before configuring its way past it.
	stdout, stderr, code := p.exec(t, "explore", "--preparation", "prep-none")
	if code != exitFailure {
		t.Fatalf("explore without a worker exited %d, want %d", code, exitFailure)
	}
	if stdout != "" {
		t.Errorf("the refusal wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "no Code analysis worker is available") {
		t.Fatalf("the refusal does not name the missing capability:\n%s", stderr)
	}
	for _, forbidden := range []string{"panic", "goroutine", "runtime error"} {
		if strings.Contains(stderr, forbidden) {
			t.Errorf("the refusal reads like a crash, it contains %q:\n%s", forbidden, stderr)
		}
	}

	// --- prepare a selection -------------------------------------------
	prepared := execJSON[prepareDoc](t, p, "prepare", "--harness", "omp", "--json")
	if len(prepared.Sessions) != 2 {
		t.Fatalf("prepared %d sessions, want the two generated OMP sessions", len(prepared.Sessions))
	}
	if prepared.IndexedEvents == 0 {
		t.Fatal("the preparation indexed nothing, so a run would have no corpus to search")
	}
	// Durable: the preparation is a content-addressed record, not a value
	// the command printed once.
	runs, err := runstore.Open(p.durableDir())
	if err != nil {
		t.Fatalf("open the durable run store: %v", err)
	}
	stored, err := runs.Preparation(ctx, runstore.PreparationID(prepared.PreparationID))
	if err == nil {
		err = stored.Verify()
	}
	if err != nil {
		runs.Close()
		t.Fatalf("the reported preparation is not a verified record in the store: %v", err)
	}
	if len(stored.Selection) != 2 {
		runs.Close()
		t.Fatalf("the stored selection holds %d sessions, want 2", len(stored.Selection))
	}
	for _, sel := range stored.Selection {
		if sel.Host != hostID || sel.Harness != synth.HarnessOMP {
			t.Errorf("selection entry %+v is not this host's OMP session", sel)
		}
	}
	runs.Close()

	// --- explore it through the synthetic worker ------------------------
	payload := writeDiscoveryPayload(t, p)
	run := execJSON[exploreDoc](t, p,
		"explore", "--preparation", prepared.PreparationID,
		"--worker", fakeWorker(t),
		"--worker-arg", "-result-payload-selector", "--worker-arg", explore.ParamStage,
		"--worker-arg", "-result-payload", "--worker-arg", string(explore.StageExplore)+"="+payload,
		// #87 item 4: the synthetic worker asks corpus-search for the
		// frontier scope. The frontier is empty at this point in the round
		// trip, so what this exercises is the whole path — served, bounded,
		// receipted with its scope — answering honestly that Babel has said
		// nothing yet, which is a different fact from not having looked.
		"--worker-arg", "-request-capability", "--worker-arg", "corpus-search",
		"--worker-arg", "-search-scope", "--worker-arg", explore.ScopeFrontier,
		"--profile", exploreProfile,
		"--json")
	if len(run.Failures) != 0 {
		t.Fatalf("the run recorded failures: %+v", run.Failures)
	}
	if run.ReceiptID == "" {
		t.Fatal("the run reported no receipt identity")
	}
	if run.PreparationID != prepared.PreparationID {
		t.Errorf("the run names preparation %q, want %q", run.PreparationID, prepared.PreparationID)
	}
	if len(run.Hypotheses) != 2 || len(run.Observations) != 1 {
		t.Fatalf("the run produced %d hypotheses and %d observations, want 2 and 1",
			len(run.Hypotheses), len(run.Observations))
	}
	if len(run.Findings) != 1 || len(run.Proposals) != 1 {
		t.Fatalf("the run produced %d findings and %d proposals, want one of each",
			len(run.Findings), len(run.Proposals))
	}
	if len(run.Deferred) != 1 {
		t.Errorf("the run deferred %d candidates, want the one the worker left speculative",
			len(run.Deferred))
	}
	// Two hypotheses plus the finding plus the proposal; an observation is
	// not reviewable (§6.7), so it is not enrolled.
	if run.Enrolled != 4 {
		t.Errorf("the run enrolled %d records for review, want 4", run.Enrolled)
	}
	if run.Preflight == nil || run.Preflight.Inputs != 2 {
		t.Fatalf("preflight = %+v, want a report over both selected sessions", run.Preflight)
	}
	proposalID := run.Proposals[0]
	findingID := run.Findings[0]
	if run.Retrievals != 1 {
		t.Errorf("the run served %d retrievals, want the frontier search the worker asked for",
			run.Retrievals)
	}

	// Durable: the receipt is stored and names the preparation, and the
	// frontier holds the records the run reported.
	runs, err = runstore.Open(p.durableDir())
	if err != nil {
		t.Fatalf("reopen the durable run store: %v", err)
	}
	receipt, err := runs.Receipt(ctx, runstore.ReceiptID(run.ReceiptID))
	if err != nil {
		runs.Close()
		t.Fatalf("the reported receipt is not in the store: %v", err)
	}
	if string(receipt.Header.PreparationID) != prepared.PreparationID {
		t.Errorf("the receipt names preparation %q, want %q",
			receipt.Header.PreparationID, prepared.PreparationID)
	}
	if receipt.Body.Worker == nil {
		t.Error("the receipt embeds no worker boundary record")
	}
	if receipt.Body.Worker != nil && receipt.Body.Worker.Profile != (worker.ProfileRef{ID: "synthetic-profile", Revision: 1}) {
		t.Errorf("the receipt records profile %+v, want the reference the command was given",
			receipt.Body.Worker.Profile)
	}
	runs.Close()

	front, err := frontier.Open(p.durableDir())
	if err != nil {
		t.Fatalf("open the durable frontier: %v", err)
	}
	proposal, err := front.Proposal(ctx, proposalID)
	if err != nil {
		front.Close()
		t.Fatalf("the reported proposal is not in the frontier: %v", err)
	}
	if len(proposal.FindingIDs) != 1 || proposal.FindingIDs[0] != findingID {
		front.Close()
		t.Fatalf("the proposal cites %v, want the run's finding", proposal.FindingIDs)
	}
	// §4.5 derives a proposal's lineage rather than accepting an assertion,
	// so this is what makes hypothesis → observation → finding → proposal a
	// fact about the store instead of a fact about the payload.
	if len(proposal.HypothesisIDs) == 0 {
		t.Error("the proposal carries no derived hypothesis lineage")
	}
	unexplored, err := front.Unexplored(ctx, 0)
	if err != nil {
		front.Close()
		t.Fatalf("read the frontier: %v", err)
	}
	if len(unexplored) != 1 || unexplored[0].ID != run.Deferred[0] {
		front.Close()
		t.Fatalf("the frontier holds %d unexplored candidates, want exactly the deferred one",
			len(unexplored))
	}
	front.Close()

	// The listings see what the run wrote, with a total to page against.
	listed := execJSON[hypothesesDoc](t, p, "hypotheses", "--json")
	if listed.Total != 2 || len(listed.Hypotheses) != 2 {
		t.Errorf("hypotheses = %+v, want both candidates", listed)
	}
	// Every listed record carries its sync state, which is what SPEC.md line
	// 646 requires of a listing and what issue #109 item 3 keeps rendering
	// once the fleet read path exists.
	//
	// On this fixture the answer is "local" for all of them, and that exact
	// word is the point. This machine has no shared catalog and no
	// publication journal, so nothing has observed that these records are
	// owed anywhere; rendering them "pending-sync" would promise a sync that
	// no process here intends to perform, which is the one thing SPEC.md 6.5's
	// visible staging must not say.
	for _, row := range listed.Hypotheses {
		state, named := listed.Sync[row.ID]
		if !named {
			t.Errorf("candidate %s was listed with no sync state", row.ID)
			continue
		}
		if state != "local" {
			t.Errorf("candidate %s reports sync %q on a machine with no shared backend, want %q",
				row.ID, state, "local")
		}
	}
	findings := execJSON[findingsDoc](t, p, "findings", "--json")
	if findings.Total != 1 || findings.Findings[0].ID != findingID {
		t.Errorf("findings = %+v, want the run's finding", findings.Findings)
	}
	if len(findings.Findings[0].ObservationIDs) != 1 {
		t.Errorf("the finding names %d observations, want the one behind it",
			len(findings.Findings[0].ObservationIDs))
	}

	// --- the actionable outputs of #87 ----------------------------------
	// What the run proposed, what the operator decided about it, what the
	// operator invited, and what the revision chain records. Every step is a
	// real binary invocation against the same durable file.
	actions := execJSON[dispositionsDoc](t, p, "dispositions", "--json")
	if actions.Total != 1 || len(actions.Dispositions) != 1 {
		t.Fatalf("dispositions = %+v, want the one the run proposed", actions.Dispositions)
	}
	action := actions.Dispositions[0]
	if action.RecordID != proposalID {
		t.Errorf("the action is attached to %q, want the proposal %q", action.RecordID, proposalID)
	}
	if action.Kind != "develop-further" || action.Status != "proposed" {
		t.Errorf("action = %+v, want an undecided develop-further", action)
	}
	if action.Ref != "d-1" {
		t.Errorf("the action does not carry the run's own ref: %+v", action)
	}
	if !strings.HasPrefix(action.ProposedBy, "run ") {
		t.Errorf("the action is attributed to %q, want the run", action.ProposedBy)
	}

	accepted := execJSON[decideDispositionDoc](t, p, "disposition", "accept", action.ID,
		"--operator", explorationOperator, "--note", "worth another pass", "--json")
	if accepted.Status != "accepted" || accepted.Entry.By != explorationOperator {
		t.Fatalf("acceptance = %+v, want an accepted entry attributed to the operator", accepted)
	}
	if !strings.Contains(accepted.Published, "nothing") {
		t.Errorf("the acceptance does not state that nothing was published: %q", accepted.Published)
	}
	detail := execJSON[dispositionDetailDoc](t, p, "disposition", "show", action.ID, "--json")
	if len(detail.Ledger) != 1 || detail.Ledger[0].Ruling != "accepted" {
		t.Fatalf("ledger = %+v, want the one recorded decision", detail.Ledger)
	}

	// An invitation is instruction-free and queued until a run takes it.
	invited := execJSON[inviteDoc](t, p, "invite", run.Deferred[0],
		"--operator", explorationOperator, "--json")
	if !invited.Invitation.Open || invited.Invitation.RecordID != run.Deferred[0] {
		t.Fatalf("invitation = %+v, want an open invitation against the deferred candidate", invited.Invitation)
	}
	invitationQueue := execJSON[invitationsDoc](t, p, "invitations", "--json")
	if len(invitationQueue.Invitations) != 1 || invitationQueue.Invitations[0].ID != invited.Invitation.ID {
		t.Fatalf("queue = %+v, want the invitation just recorded", invitationQueue.Invitations)
	}

	// Nothing closes: the deferred candidate is at rest, and reviving it is
	// an attributed transition that leaves the deferral in the history.
	revived := execJSON[reviveDoc](t, p, "revive", run.Deferred[0],
		"--reason", "the operator invited another pass", "--operator", explorationOperator, "--json")
	if revived.Status.Status != "queued" {
		t.Fatalf("revive left the candidate %q, want queued", revived.Status.Status)
	}
	if revived.Status.Actor != "operator "+explorationOperator {
		t.Errorf("the revive is attributed to %q", revived.Status.Actor)
	}

	// The revision chain is readable, and an operator revision joins it
	// without touching the wording it supersedes.
	reworded := execJSON[reviseDocument](t, p, "revise", run.Deferred[0],
		"--statement", "a second candidate, restated after the invitation",
		"--reason", "the original wording was too broad to investigate",
		"--operator", explorationOperator, "--json")
	chain := execJSON[revisionsDoc](t, p, "revisions", run.Deferred[0], "--json")
	if len(chain.Revisions) != 2 {
		t.Fatalf("chain = %+v, want the original and the operator's revision", chain.Revisions)
	}
	if chain.HeadID != reworded.Revision.RecordID {
		t.Errorf("head = %q, want the new revision %q", chain.HeadID, reworded.Revision.RecordID)
	}
	if chain.Revisions[0].Actor == chain.Revisions[1].Actor {
		t.Errorf("the run's revision and the operator's are indistinguishable: %+v", chain.Revisions)
	}
	if chain.Revisions[1].SupersedesID != run.Deferred[0] {
		t.Errorf("the revision supersedes %q, want %q", chain.Revisions[1].SupersedesID, run.Deferred[0])
	}

	// --- review the proposal --------------------------------------------
	queue := execJSON[queueDoc](t, p, "review", "queue", "--json")
	var queued *queueItemDoc
	for i, item := range queue.Items {
		if item.ID == proposalID {
			queued = &queue.Items[i]
		}
	}
	if queued == nil {
		t.Fatalf("the run's proposal is not on the review queue: %+v", queue.Items)
	}
	if queued.Status != string(frontier.ReviewNew) || queued.Decisions != 0 {
		t.Errorf("queued proposal = %+v, want an undecided row", *queued)
	}

	// §4.7: a disposition with no author is refused, not defaulted.
	_, stderr, code = p.exec(t, "review", "decide", proposalID, "--accept")
	if code == exitOK {
		t.Fatal("an unattributed decision was accepted")
	}
	if !strings.Contains(stderr, "--operator") {
		t.Errorf("the refusal does not name the remedy:\n%s", stderr)
	}

	decided := execJSON[decideDoc](t, p, "review", "decide", proposalID, "--accept",
		"--operator", explorationOperator,
		"--context", "reviewed against the synthetic corpus", "--json")
	if decided.Status != string(frontier.ReviewAccepted) {
		t.Fatalf("the decision left the proposal %q, want accepted", decided.Status)
	}
	if decided.Decision.ReviewerID != explorationOperator {
		t.Errorf("the decision is attributed to %q, want %q",
			decided.Decision.ReviewerID, explorationOperator)
	}

	// Durable: the derived status comes from the append-only log, so reading
	// it back from the store is reading the events themselves.
	front, err = frontier.Open(p.durableDir())
	if err != nil {
		t.Fatalf("reopen the durable frontier: %v", err)
	}
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: proposalID}
	reviewStatus, err := front.ReviewStatus(ctx, subject)
	if err != nil {
		front.Close()
		t.Fatalf("read the review status: %v", err)
	}
	history, err := front.DispositionHistory(ctx, subject)
	if err != nil {
		front.Close()
		t.Fatalf("read the disposition history: %v", err)
	}
	front.Close()
	if reviewStatus != frontier.ReviewAccepted {
		t.Fatalf("durable review status is %q, want accepted", reviewStatus)
	}
	if len(history) != 1 || history[0].ReviewerID != explorationOperator || history[0].ContextID == "" {
		t.Fatalf("disposition history = %+v, want one attributed decision citing its guidance", history)
	}

	// --- export it ------------------------------------------------------
	jsonPath := filepath.Join(p.root, "proposal.json")
	markdownPath := filepath.Join(p.root, "proposal.md")
	p.okExec(t, "export", proposalID, "--format", "json", "--output", jsonPath)
	p.okExec(t, "export", proposalID, "--format", "markdown", "--output", markdownPath)

	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var document review.Export
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the exported JSON does not decode: %v", err)
	}
	if document.Proposal == nil || document.Proposal.ID != proposalID {
		t.Fatalf("the export does not carry the reviewed proposal: %+v", document)
	}
	if document.Review == nil || len(document.Review.Decisions) != 1 {
		t.Error("the export omits the review history that judged the record")
	}
	if len(document.Locators) == 0 {
		t.Error("the export collected no locators, so its claims cannot be reopened")
	}
	if document.Notice == "" {
		t.Error("the export dropped its fallibility notice")
	}
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), proposalID) {
		t.Error("the Markdown export does not name the record it renders")
	}

	// --- nothing published ------------------------------------------------
	if after := fingerprintTree(t, p.home); after != before {
		t.Error("the source tree changed during the round trip; no Phase B command may write to a source (SPEC.md §4.6)")
	}
}

// shippedRecipeVersion is what the embedded cookbook currently ships for id.
// The run selects recipes at their shipped versions and refuses a claim
// citing any other, so a fixture that pins a number rots on every recipe
// bump - #114's bump caught a pinned @2 here. The version is derived; the
// refusal behaviour itself is someone else's test.
func shippedRecipeVersion(t *testing.T, id string) int {
	t.Helper()
	set, err := cookbook.Embedded()
	if err != nil {
		t.Fatalf("cookbook.Embedded() = %v", err)
	}
	r, ok := set.ByID(id)
	if !ok {
		t.Fatalf("the embedded cookbook ships no recipe %q", id)
	}
	return r.Version
}

// writeDiscoveryPayload builds the structured result the synthetic worker
// emits: two candidates, one developed into an observation citing real
// corpus bytes, consolidated into a finding and a proposal, with the second
// candidate deferred rather than dropped.
//
// The citation matters. Babel validates that an evidence locator recovers
// its bytes, so the payload cites a record scanned out of the generated
// corpus; a fabricated locator would be refused, which is the behaviour that
// makes a proposal reopenable at all (§4.3).
func writeDiscoveryPayload(t *testing.T, p *phaseB) string {
	t.Helper()
	var omp *synth.Session
	for i := range p.corpus.Sessions {
		if p.corpus.Sessions[i].Harness == synth.HarnessOMP {
			omp = &p.corpus.Sessions[i]
			break
		}
	}
	if omp == nil {
		t.Fatal("the generated corpus holds no OMP session")
	}
	evidence, err := frontier.NewEvidence(firstLocator(t, omp.Path),
		"the first intact record of a generated session")
	if err != nil {
		t.Fatalf("build the citation: %v", err)
	}
	result := explore.Result{
		Candidates: []explore.Candidate{
			{
				Ref: "c-1",
				Hypothesis: frontier.HypothesisPayload{
					Statement: "the generated corpus repeats one synthetic shape",
					Novelty:   0.5,
					Priority:  0.9,
				},
				Observations: []explore.Observation{{
					Ref:    "o-1",
					Recipe: worker.RecipeRef{ID: "outcome-integrity", Version: shippedRecipeVersion(t, "outcome-integrity")},
					Claim: frontier.ObservationPayload{
						Claim:                 "the first record of this session states the shape",
						Category:              "outcome",
						Confidence:            frontier.ConfidenceModerate,
						Impact:                frontier.ImpactLow,
						Evidence:              []frontier.Evidence{evidence},
						CounterEvidenceAbsent: true,
					},
				}},
			},
			{
				Ref: "c-2",
				Hypothesis: frontier.HypothesisPayload{
					Statement: "a second candidate the pass leaves speculative",
					Priority:  0.1,
				},
			},
		},
		Consolidations: []explore.Consolidation{{
			Ref:          "con-1",
			Observations: []string{"o-1"},
			Finding: frontier.FindingPayload{
				Title:                 "the synthetic shape recurs",
				Pattern:               "every generated session opens with the same record kind",
				Significance:          "it exercises the whole development path",
				CounterEvidenceAbsent: true,
			},
			Proposal: &frontier.ProposalPayload{
				Title:          "record the synthetic shape once",
				Problem:        "the shape is restated per session",
				Outcome:        "state it once and cite the sessions",
				Impact:         frontier.ImpactLow,
				Classification: frontier.ClassificationPrivate,
				// §4.5 carries a proposal's backing as locator-bearing
				// citations so it can be reopened rather than believed,
				// which is what the export then collects.
				Supporting: []frontier.Evidence{evidence},
			},
			// #87: the run proposes a next action alongside what it wrote.
			// It attaches to the proposal rather than the finding, because
			// the proposal is what §4.5 makes reviewable.
			Dispositions: []explore.ProposedAction{{
				Ref:       "d-1",
				Kind:      disposition.KindDevelopFurther,
				Summary:   "look for the same shape in the other harnesses",
				Rationale: "the generated corpus only covers one",
			}},
		}},
		Deferred: []explore.Disposal{{
			Hypothesis: "c-2",
			Reason:     "the pass did not develop it",
		}},
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("encode the result payload: %v", err)
	}
	path := filepath.Join(p.root, "discovery.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// firstLocator returns the locator of a session's first intact record, which
// is what makes the synthetic observation's evidence recover real bytes
// rather than assert that it could.
func firstLocator(t *testing.T, path string) event.Locator {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stream := event.Stream{Harness: synth.HarnessOMP, AdapterSchema: 1, SourceID: "unused", Path: path}
	var found event.Locator
	stop := errors.New("enough")
	err = event.Scan(file, stream, func(e event.Event) error {
		if e.Partial || e.Text == "" {
			return nil
		}
		found = e.Locator
		return stop
	})
	if err != nil && !errors.Is(err, stop) {
		t.Fatalf("scan %s: %v", path, err)
	}
	if found.Digest == "" {
		t.Fatalf("%s yielded no usable record locator", path)
	}
	return found
}

// fingerprintTree digests every regular file under root, path and content
// together, so a comparison detects an added, removed, or rewritten file.
func fingerprintTree(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		sum := sha256.New()
		if _, err := io.Copy(sum, file); err != nil {
			return err
		}
		entries = append(entries, rel+" "+hex.EncodeToString(sum.Sum(nil)))
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprint %s: %v", root, err)
	}
	sort.Strings(entries)
	whole := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(whole[:])
}
