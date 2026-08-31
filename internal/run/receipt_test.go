package run

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/worker"
)

var (
	runStart  = time.Date(2026, 8, 29, 10, 1, 0, 0, time.UTC)
	runEnd    = time.Date(2026, 8, 29, 10, 9, 30, 0, time.UTC)
	recorded  = time.Date(2026, 8, 29, 10, 9, 31, 0, time.UTC)
	amendedAt = time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
)

// credentialSentinel is a documented AWS example key, used the way SPEC.md
// §10 uses sentinels: it proves a known shape cannot reach a surface, not that
// unknown secrets cannot.
const credentialSentinel = "AKIA" + "IOSFODNN7EXAMPLE"

func testLocator(seed string, line int) event.Locator {
	return event.Locator{
		Path:       "/archive/" + seed + ".jsonl",
		Line:       line,
		ByteOffset: int64(line) * 512,
		Digest:     testDigest(seed).Hex(),
	}
}

func mustEvidence(t *testing.T, seed string, line int, note string) Evidence {
	t.Helper()
	e, err := NewEvidence(testLocator(seed, line), note)
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	return e
}

// testWorkerReceipt is the worker boundary's own record, with an allowed and a
// denied tool request so the embedding can be checked against both.
func testWorkerReceipt() *worker.Receipt {
	return &worker.Receipt{
		JobID:   "job-1",
		RunID:   "run-1",
		Profile: worker.ProfileRef{ID: "profile-a", Revision: 4},
		Recipes: []worker.RecipeRef{{ID: "outcome-integrity", Version: 1}},
		Sources: []worker.Source{{Kind: "session", Selector: "omp/session-0001",
			Digest: string(testDigest("omp-normalized")), Snapshot: "0a1b2c3d"}},
		Worker:          worker.Identity{Name: "code", Version: "1.4.2"},
		ProtocolVersion: worker.ProtocolVersion,
		Grant: worker.Grant{
			Capabilities: []worker.Capability{
				worker.CapabilityCorpusSearch,
				worker.CapabilityRepoRead,
				worker.CapabilitySandboxExec,
				worker.CapabilityPublicResearch,
			},
			Disclosure: worker.DisclosureLocal,
			ExpiresAt:  runEnd,
		},
		Privacy:              worker.Privacy{Disclosure: worker.DisclosureLocal, RedactionRequired: true},
		Cost:                 worker.Cost{Currency: "USD", InputPer1K: 0.5, OutputPer1K: 1.5, EstimatedRun: 2},
		ResolvedCapabilities: []worker.Capability{worker.CapabilityCorpusSearch},
		Metadata:             map[string]string{"provider": "local", "model": "m-1", "thinking": "high"},
		ToolRequests: []worker.ToolRecord{
			{
				Index: 0, RequestID: "t-1", Capability: worker.CapabilityCorpusSearch,
				Tool: "search", ArgumentsDigest: testDigest("args-1"), ArgumentsBytes: 42,
				Allowed: true, At: runStart, Decided: 3 * time.Millisecond,
			},
			{
				Index: 1, RequestID: "t-2", Capability: worker.CapabilitySandboxExec,
				Tool: "exec", ArgumentsDigest: testDigest("args-2"), ArgumentsBytes: 64,
				Allowed: false, DenyCode: worker.DenyPolicy, Reason: "outside the approved scope",
				At: runStart.Add(time.Second), Decided: time.Millisecond,
			},
			{
				Index: 2, RequestID: "t-3", Capability: worker.CapabilityPublicResearch,
				Tool: "fetch", ArgumentsDigest: testDigest("args-3"), ArgumentsBytes: 90,
				Allowed: false, DenyCode: worker.DenyNotGranted, Reason: "not in the grant",
				At: runStart.Add(2 * time.Second), Decided: 2 * time.Millisecond,
			},
		},
		Progress: []worker.ProgressRecord{
			{Seq: 2, Stage: "discover", Message: "reading normalized events", Fraction: 0.25, At: runStart},
		},
		ProgressDropped: 1,
		Result: &worker.ResultRecord{
			Status: worker.StatusOK, Schema: "babel.analysis-result/1",
			Payload: json.RawMessage(`{"hypotheses":2}`), At: runEnd,
		},
		Failure: &worker.FailureRecord{
			Origin: worker.FailureWorker, Code: "tool-timeout",
			Message: "the search tool did not answer", Retryable: true, At: runEnd,
		},
		Resources: &worker.Resources{
			CPUSeconds: 12.5, MaxRSSBytes: 1 << 26, SandboxBytesWritten: 4096, ToolCalls: 3,
		},
		UnknownFields: []string{"future_field"},
		StderrTail:    "worker: finished",
		ExitCode:      0,
		StartedAt:     runStart,
		FinishedAt:    runEnd,
		Duration:      runEnd.Sub(runStart),
	}
}

// testAuthority is the why every receipt in these tests was run under: an
// operator command, which is what a hand-typed `babel explore` records.
func testAuthority() Authority {
	return Authority{Kind: AuthorityOperator, Ref: "command:explore"}
}

// testBody populates every field SPEC.md §7 and §6.5 require, so a test that
// walks it is walking the whole contract.
func testBody(t *testing.T) Body {
	t.Helper()
	return Body{
		Cookbook: []CookbookAsset{
			{Kind: AssetPolicy, Ref: worker.RecipeRef{ID: "temporal-checks", Version: 2}},
			{Kind: AssetLens, Ref: worker.RecipeRef{ID: "outcome-integrity", Version: 1}},
			{Kind: AssetMeta, Ref: worker.RecipeRef{ID: "cookbook-quality", Version: 1}},
		},
		Frontier: FrontierScope{
			Roots: []string{"hyp-0001", "hyp-0002"},
			Prior: []string{"hyp-0000"},
		},
		Capabilities: CapabilityVersions{
			Sandbox: "sandbox/3", Tool: "broker/2", Repository: "repo/1", PublicResearch: "research/1",
		},
		Job:    JobVersions{Job: 1, Prompt: "explore/4", Schema: "babel.analysis-result/1"},
		Policy: PolicyVersions{Redaction: "redaction/2", Disclosure: "disclosure/1"},
		Worker: testWorkerReceipt(),
		Retrieval: []RetrievalStep{
			{
				Index: 1, Tool: "corpus-search", Query: "unresolved verification", At: runStart,
				Results: []RetrievalResult{
					{Rank: 1, Evidence: mustEvidence(t, "hit-a", 12, "claimed the tests passed")},
					{Rank: 2, Evidence: mustEvidence(t, "hit-b", 40, "no test run recorded")},
				},
			},
			{Index: 2, Tool: "corpus-search", Query: "rollback", At: runStart.Add(time.Minute)},
		},
		Deferred: []Candidate{
			{ID: "hyp-0003", Reason: "budget exhausted", At: runEnd,
				Origin: []Evidence{mustEvidence(t, "hit-a", 12, "same record")}},
		},
		Rejected: []Candidate{
			{ID: "hyp-0004", Reason: "duplicate of hyp-0001", At: runEnd},
		},
		Failures: []Failure{
			{Stage: "broker", Code: "research-refused", Message: "destination is not public", At: runEnd},
		},
		Resources: Resources{
			CPUSeconds:          new(41.5),
			SandboxBytesWritten: new(int64(0)),
		},
		Timing: Timing{StartedAt: runStart, FinishedAt: runEnd},
	}
}

func mustReceipt(t *testing.T) Receipt {
	t.Helper()
	prep := mustPreparation(t, preparedAt, testSelection())
	r, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), testBody(t), recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}
	return r
}

func TestNewReceiptDerivesHeaderFromBody(t *testing.T) {
	r := mustReceipt(t)
	if r.Header.Schema != ReceiptSchema {
		t.Errorf("schema %d, want %d", r.Header.Schema, ReceiptSchema)
	}
	if r.Header.Revision != 1 || r.Header.Supersedes != "" {
		t.Errorf("first receipt is revision %d superseding %q", r.Header.Revision, r.Header.Supersedes)
	}
	if r.Header.Sync != SyncPending {
		t.Errorf("sync state %q, want %q", r.Header.Sync, SyncPending)
	}
	if r.Header.PreparationID != r.Preparation.ID {
		t.Errorf("header names preparation %s, record is %s", r.Header.PreparationID, r.Preparation.ID)
	}
	want := Counts{ToolRequests: 3, ToolsDenied: 2, Retrieval: 2, Deferred: 1, Rejected: 1, Failures: 1}
	if r.Header.Counts != want {
		t.Errorf("counts %+v, want %+v", r.Header.Counts, want)
	}
}

// Counts summarize the body for a listing that never opens it, so they must be
// derived rather than believed.
func TestNewReceiptIgnoresSuppliedCounts(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	body.Failures = nil
	body.Retrieval = nil
	r, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}
	if r.Header.Counts.Failures != 0 || r.Header.Counts.Retrieval != 0 {
		t.Errorf("counts %+v do not describe the body", r.Header.Counts)
	}
}

// The worker receipt is embedded rather than summarized, so every authorized
// and refused request survives with the decision Babel wrote back.
func TestReceiptPreservesEveryToolDecision(t *testing.T) {
	r := mustReceipt(t)
	encoded, err := r.MarshalBody()
	if err != nil {
		t.Fatalf("MarshalBody: %v", err)
	}
	body, err := unmarshalBody(encoded)
	if err != nil {
		t.Fatalf("unmarshalBody: %v", err)
	}
	want := testWorkerReceipt().ToolRequests
	got := body.Worker.ToolRequests
	if len(got) != len(want) {
		t.Fatalf("kept %d tool requests, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tool request %d round-tripped as %+v, want %+v", i, got[i], want[i])
		}
	}
	if body.Worker.Denied() != 2 {
		t.Errorf("denied count %d, want 2", body.Worker.Denied())
	}
}

// A run that never reached the worker has no Code version, no profile and no
// resolved provider metadata, so it must say why rather than look like a run
// with nothing to report.
func TestReceiptWithoutWorkerRequiresAFailure(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	body.Worker = nil
	body.Failures = nil
	if _, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded); err == nil {
		t.Fatal("accepted a receipt with neither a worker receipt nor a failure")
	}
	body.Failures = []Failure{{Stage: "launch", Code: "code-missing",
		Message: "no compatible Code capability", At: runEnd}}
	if _, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded); err != nil {
		t.Fatalf("refused a receipt that explains its missing worker: %v", err)
	}
}

// A granted capability with no facility version is a containment question that
// can no longer be answered, so it is refused at construction.
func TestReceiptRequiresFacilityVersionsForGrantedCapabilities(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	cases := []struct {
		name  string
		clear func(c *CapabilityVersions)
	}{
		{name: "sandbox", clear: func(c *CapabilityVersions) { c.Sandbox = "" }},
		{name: "tool", clear: func(c *CapabilityVersions) { c.Tool = "" }},
		{name: "repository", clear: func(c *CapabilityVersions) { c.Repository = "" }},
		{name: "public research", clear: func(c *CapabilityVersions) { c.PublicResearch = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := testBody(t)
			tc.clear(&body.Capabilities)
			if _, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded); err == nil {
				t.Fatalf("accepted a granted %s capability with no version", tc.name)
			}
		})
	}
}

func TestReceiptRejectsIncompleteProvenance(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	cases := []struct {
		name   string
		mutate func(b *Body)
	}{
		{name: "no cookbook asset", mutate: func(b *Body) { b.Cookbook = nil }},
		{name: "unknown asset kind", mutate: func(b *Body) { b.Cookbook[0].Kind = "guideline" }},
		{name: "unversioned asset", mutate: func(b *Body) { b.Cookbook[0].Ref.Version = 0 }},
		{name: "repeated asset", mutate: func(b *Body) { b.Cookbook[1].Ref = b.Cookbook[0].Ref }},
		{name: "no job version", mutate: func(b *Body) { b.Job.Job = 0 }},
		{name: "no prompt version", mutate: func(b *Body) { b.Job.Prompt = "" }},
		{name: "no result schema", mutate: func(b *Body) { b.Job.Schema = "" }},
		{name: "no redaction policy", mutate: func(b *Body) { b.Policy.Redaction = "" }},
		{name: "no disclosure policy", mutate: func(b *Body) { b.Policy.Disclosure = "" }},
		{name: "no timing", mutate: func(b *Body) { b.Timing = Timing{} }},
		{name: "finished before started", mutate: func(b *Body) {
			b.Timing.FinishedAt = b.Timing.StartedAt.Add(-time.Second)
		}},
		{name: "frontier root with a space", mutate: func(b *Body) { b.Frontier.Roots[0] = "hyp 0001" }},
		{name: "retrieval out of sequence", mutate: func(b *Body) { b.Retrieval[1].Index = 5 }},
		{name: "retrieval rank out of order", mutate: func(b *Body) { b.Retrieval[0].Results[1].Rank = 7 }},
		{name: "retrieval without a tool", mutate: func(b *Body) { b.Retrieval[0].Tool = "" }},
		{name: "candidate without a reason", mutate: func(b *Body) { b.Deferred[0].Reason = "" }},
		{name: "candidate without an identity", mutate: func(b *Body) { b.Rejected[0].ID = "" }},
		{name: "failure without a code", mutate: func(b *Body) { b.Failures[0].Code = "" }},
		{name: "amendment reason on revision 1", mutate: func(b *Body) { b.AmendmentReason = "corrected" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := testBody(t)
			tc.mutate(&body)
			if _, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded); err == nil {
				t.Fatal("accepted a receipt with incomplete provenance")
			}
		})
	}
}

// SPEC.md §4.3: a claim about the corpus cannot exist without the locator that
// recovers it. The locator is unexported, so the only way to hold evidence is
// to have passed a valid one — including when it arrives from storage.
func TestEvidenceCannotExistWithoutALocator(t *testing.T) {
	cases := []struct {
		name    string
		locator event.Locator
	}{
		{name: "no path", locator: event.Locator{Line: 1, Digest: testDigest("x").Hex()}},
		{name: "no line", locator: event.Locator{Path: "p", Digest: testDigest("x").Hex()}},
		{name: "negative offset", locator: event.Locator{Path: "p", Line: 1, ByteOffset: -1,
			Digest: testDigest("x").Hex()}},
		{name: "no digest", locator: event.Locator{Path: "p", Line: 1}},
		{name: "prefixed digest", locator: event.Locator{Path: "p", Line: 1,
			Digest: string(testDigest("x"))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewEvidence(tc.locator, "note"); err == nil {
				t.Fatal("built evidence that cannot recover its bytes")
			}
			var e Evidence
			encoded, err := json.Marshal(evidenceWire{Locator: tc.locator, Note: "note"})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.UnmarshalJSON(encoded); err == nil {
				t.Fatal("decoded evidence that cannot recover its bytes")
			}
		})
	}

	var zero Evidence
	if _, err := json.Marshal(struct{ E Evidence }{zero}); err != nil {
		t.Fatalf("marshalling a zero Evidence: %v", err)
	}
	if err := validLocator(zero.Locator()); err == nil {
		t.Error("the zero Evidence claims a usable locator")
	}
}

// Retrieval rank orders results and nothing else (SPEC.md §5.4). The testable
// form of "rank never becomes evidence strength" is that rank carries no
// payload: reversing the order moves the numbers and nothing else, so no
// downstream reader can recover a confidence from where a hit sat.
func TestRetrievalRankCarriesNothingButOrder(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	hits := body.Retrieval[0].Results
	if len(hits) != 2 {
		t.Fatalf("expected two hits, got %d", len(hits))
	}
	if hits[0].Evidence.Locator() == hits[1].Evidence.Locator() {
		t.Fatal("the fixture's two hits are the same record")
	}

	reversed := testBody(t)
	reversed.Retrieval[0].Results = []RetrievalResult{
		{Rank: 1, Evidence: hits[1].Evidence},
		{Rank: 2, Evidence: hits[0].Evidence},
	}
	forward, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}
	backward, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), reversed, recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}

	for i, want := range []Evidence{hits[1].Evidence, hits[0].Evidence} {
		got := backward.Body.Retrieval[0].Results[i]
		if got.Rank != i+1 {
			t.Errorf("reversed hit %d has rank %d", i, got.Rank)
		}
		if got.Evidence.Locator() != want.Locator() || got.Evidence.Note() != want.Note() {
			t.Errorf("reversing the order altered hit %d: %+v", i, got.Evidence)
		}
	}
	// The two receipts differ in ordering alone, so their summaries — the part
	// a reviewer reads without opening the trace — are identical.
	if forward.Header.Counts != backward.Header.Counts {
		t.Errorf("reordering changed the receipt's summary: %+v vs %+v",
			forward.Header.Counts, backward.Header.Counts)
	}
}

func TestAmendLinksToThePriorRevision(t *testing.T) {
	first := mustReceipt(t)
	body := testBody(t)
	body.AmendmentReason = "the sandbox version was recorded from the wrong build"
	body.Capabilities.Sandbox = "sandbox/4"

	second, err := Amend(first, NewReceiptID(), body, amendedAt)
	if err != nil {
		t.Fatalf("Amend: %v", err)
	}
	if second.Header.Revision != 2 {
		t.Errorf("revision %d, want 2", second.Header.Revision)
	}
	if second.Header.Supersedes != first.Header.ID {
		t.Errorf("supersedes %q, want %q", second.Header.Supersedes, first.Header.ID)
	}
	if second.Header.RunID != first.Header.RunID {
		t.Errorf("run id %q, want %q", second.Header.RunID, first.Header.RunID)
	}
	if second.Header.PreparationID != first.Header.PreparationID {
		t.Error("an amendment changed the run's corpus scope")
	}
	if first.Header.Revision != 1 || first.Body.Capabilities.Sandbox != "sandbox/3" {
		t.Error("amending altered the prior revision in memory")
	}

	if _, err := Amend(first, NewReceiptID(), testBody(t), amendedAt); err == nil {
		t.Error("accepted an amendment that does not say what it corrects")
	}
	if _, err := Amend(Receipt{}, NewReceiptID(), body, amendedAt); err == nil {
		t.Error("accepted an amendment of a receipt that was never recorded")
	}
}

// The credential rule is absolute: not in the receipt, not in an error, not in
// a log line. The sentinel is pushed through every worker-controlled string in
// the record and then looked for in the serialized bytes and in every error
// the package produced along the way.
func TestCredentialSentinelReachesNoStoredFieldOrError(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	body.Worker.Metadata["provider"] = credentialSentinel
	body.Worker.Metadata["api_key"] = "a-value-with-no-recognizable-shape"
	body.Worker.StderrTail = "worker: authorization: Bearer " + credentialSentinel
	body.Worker.ToolRequests[1].Reason = "refused: token=" + credentialSentinel
	body.Worker.Progress[0].Message = "connecting with " + credentialSentinel
	body.Worker.Result.Payload = json.RawMessage(`{"note":"aws_secret_access_key=` + credentialSentinel + `"}`)
	body.Worker.Failure.Message = "login failed for postgres://babel:" + credentialSentinel + "@db:5432/babel"
	body.Failures[0].Message = "broker refused " + credentialSentinel
	body.Retrieval[0].Query = "search for " + credentialSentinel
	body.Deferred[0].Reason = "deferred after seeing " + credentialSentinel
	body.Rejected[0].Reason = "password: " + credentialSentinel

	r, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err != nil {
		if strings.Contains(err.Error(), credentialSentinel) {
			t.Fatal("an error carried the credential sentinel")
		}
		t.Fatalf("NewReceipt: %v", err)
	}
	if r.Header.Counts.Redactions == 0 {
		t.Error("redactions were not counted, so an operator would never see that one happened")
	}

	encoded, err := r.MarshalBody()
	if err != nil {
		t.Fatalf("MarshalBody: %v", err)
	}
	if strings.Contains(string(encoded), credentialSentinel) {
		t.Errorf("the stored receipt carries the credential sentinel:\n%s", encoded)
	}
	if !strings.Contains(string(encoded), redactedMarker) {
		t.Error("nothing was marked as redacted")
	}
	if _, err := unmarshalBody(encoded); err != nil {
		t.Fatalf("redaction left the stored body unreadable: %v", err)
	}

	// The redacted payload must still be the structured result it claims to
	// be, not bytes that merely no longer contain a credential.
	if !json.Valid(r.Body.Worker.Result.Payload) {
		t.Errorf("redaction broke the worker's structured result: %s", r.Body.Worker.Result.Payload)
	}
	// A key that names a credential loses its value on the key alone, without
	// waiting for the value to look like one.
	if got := r.Body.Worker.Metadata["api_key"]; got != redactedMarker {
		t.Errorf("credential-named metadata kept its value %q", got)
	}
}

// Redaction must copy rather than edit in place: a caller that still holds the
// body it handed over must not be able to observe the receipt's contents, nor
// change them afterwards.
func TestNewReceiptCopiesTheBodyItIsGiven(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	r, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}

	body.Worker.ToolRequests[0].Allowed = false
	body.Worker.Metadata["model"] = "swapped"
	body.Frontier.Roots[0] = "hyp-9999"
	body.Failures[0].Message = "rewritten"
	*body.Resources.CPUSeconds = 999

	if !r.Body.Worker.ToolRequests[0].Allowed {
		t.Error("a caller mutated a stored tool decision")
	}
	if r.Body.Worker.Metadata["model"] != "m-1" {
		t.Error("a caller mutated stored provider metadata")
	}
	if r.Body.Frontier.Roots[0] != "hyp-0001" {
		t.Error("a caller mutated a stored frontier root")
	}
	if r.Body.Failures[0].Message == "rewritten" {
		t.Error("a caller mutated a stored failure message")
	}
	if *r.Body.Resources.CPUSeconds != 41.5 {
		t.Error("a caller mutated a stored measurement")
	}
}

func TestReceiptIDIsUnique(t *testing.T) {
	seen := make(map[ReceiptID]struct{}, 128)
	for range 128 {
		id := NewReceiptID()
		if !validIdentifier(string(id)) {
			t.Fatalf("minted an unusable id %q", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("minted the same id twice: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestTimingDurationIsDerived(t *testing.T) {
	r := mustReceipt(t)
	if got, want := r.Body.Timing.Duration(), runEnd.Sub(runStart); got != want {
		t.Errorf("duration %s, want %s", got, want)
	}
}

func TestNewReceiptRejectsBadIdentityAndScope(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	if _, err := NewReceipt("", "run-1", prep, testAuthority(), testBody(t), recorded); err == nil {
		t.Error("accepted a receipt with no id")
	}
	if _, err := NewReceipt(NewReceiptID(), "run 1", prep, testAuthority(), testBody(t), recorded); err == nil {
		t.Error("accepted a run id with a space in it")
	}
	if _, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), testBody(t), time.Time{}); err == nil {
		t.Error("accepted a receipt with no recording time")
	}
	broken := prep
	broken.Selection = append([]Selected(nil), prep.Selection...)
	broken.Selection[0].Host = "elsewhere"
	if _, err := NewReceipt(NewReceiptID(), "run-1", broken, testAuthority(), testBody(t), recorded); err == nil {
		t.Error("accepted a preparation whose content does not derive its id")
	}
}

func TestUnmarshalBodyRejectsUnknownFields(t *testing.T) {
	r := mustReceipt(t)
	encoded, err := r.MarshalBody()
	if err != nil {
		t.Fatalf("MarshalBody: %v", err)
	}
	extended := strings.Replace(string(encoded), `{"cookbook"`, `{"surprise":1,"cookbook"`, 1)
	if _, err := unmarshalBody([]byte(extended)); err == nil {
		t.Error("decoded a receipt body carrying a field outside the schema")
	}
}

// "No credentials, ever" covers the rejection path too: a receipt that fails
// validation must not describe the offending record back to whoever reads the
// error.
func TestRejectionErrorsCarryNoValues(t *testing.T) {
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	body.Failures[0].Message = "broker refused " + credentialSentinel
	body.Worker.StderrTail = "authorization: Bearer " + credentialSentinel
	body.Retrieval[0].Query = credentialSentinel
	// Break the record so construction has to refuse it.
	body.Retrieval[1].Index = 9

	_, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err == nil {
		t.Fatal("accepted a receipt with an out-of-sequence retrieval trace")
	}
	if strings.Contains(err.Error(), credentialSentinel) {
		t.Errorf("the rejection error carried the credential sentinel: %v", err)
	}

	// The same rule for a preparation whose entries are invalid.
	selection := testSelection()
	selection[0].SourceID = credentialSentinel + "/../escape"
	if _, err := NewPreparation(preparedAt, selection); err == nil {
		t.Error("accepted an invalid source id")
	} else if strings.Contains(err.Error(), credentialSentinel) {
		t.Errorf("the rejection error echoed the selector: %v", err)
	}
}
