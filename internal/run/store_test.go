package run

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/worker"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A preparation is content-addressed, so storage must return the same bytes it
// was given: a record that round-trips only field-for-field could re-encode
// into something that no longer derives its own ID.
func TestPreparationRoundTripsByteIdentically(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	prep := mustPreparation(t, preparedAt, testSelection())
	want, err := prep.MarshalCanonical()
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}
	if err := s.PutPreparation(ctx, prep); err != nil {
		t.Fatalf("PutPreparation: %v", err)
	}

	var stored []byte
	if err := s.db.QueryRow(`SELECT payload FROM run_preparation WHERE id = ?`,
		string(prep.ID)).Scan(&stored); err != nil {
		t.Fatalf("read stored payload: %v", err)
	}
	if string(stored) != string(want) {
		t.Errorf("stored bytes differ from the canonical form:\n got %s\nwant %s", stored, want)
	}

	loaded, err := s.Preparation(ctx, prep.ID)
	if err != nil {
		t.Fatalf("Preparation: %v", err)
	}
	again, err := loaded.MarshalCanonical()
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(again) != string(want) {
		t.Errorf("re-encoded bytes differ:\n got %s\nwant %s", again, want)
	}
	if loaded.ID != prep.ID {
		t.Errorf("loaded id %s, want %s", loaded.ID, prep.ID)
	}
}

func TestPutPreparationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	prep := mustPreparation(t, preparedAt, testSelection())
	for range 3 {
		if err := s.PutPreparation(ctx, prep); err != nil {
			t.Fatalf("PutPreparation: %v", err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM run_preparation`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("stored %d rows for one preparation, want 1", n)
	}
}

func TestPreparationNotFound(t *testing.T) {
	s := testStore(t)
	if _, err := s.Preparation(context.Background(), "prep-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v, want ErrNotFound", err)
	}
}

// SPEC.md §7 lists what every run records. A missing field here is a
// provenance hole, so each item is asserted on its own after a full storage
// round trip rather than by comparing whole structs, which would pass while
// silently agreeing about nothing.
func TestStoredReceiptCarriesEveryFieldSectionSevenRequires(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	original := mustReceipt(t)
	if err := s.PutReceipt(ctx, original); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	got, err := s.Receipt(ctx, original.Header.ID)
	if err != nil {
		t.Fatalf("Receipt: %v", err)
	}

	// 1. normalized source and capture digests.
	if len(got.Preparation.Selection) != 3 {
		t.Fatalf("preparation carries %d sources, want 3", len(got.Preparation.Selection))
	}
	for i, sel := range got.Preparation.Selection {
		want := original.Preparation.Selection[i]
		if sel.SourceDigest != want.SourceDigest || sel.SourceDigest == "" {
			t.Errorf("source %d normalized digest %q, want %q", i, sel.SourceDigest, want.SourceDigest)
		}
		if sel.CaptureDigest != want.CaptureDigest || sel.CaptureDigest == "" {
			t.Errorf("source %d capture digest %q, want %q", i, sel.CaptureDigest, want.CaptureDigest)
		}
	}

	// 2. source-adapter identity, version and metadata completeness.
	for i, sel := range got.Preparation.Selection {
		want := original.Preparation.Selection[i]
		if sel.Harness != want.Harness {
			t.Errorf("source %d adapter identity %q, want %q", i, sel.Harness, want.Harness)
		}
		if sel.Adapter.Schema != want.Adapter.Schema || sel.Adapter.Version != want.Adapter.Version {
			t.Errorf("source %d adapter %d/%q, want %d/%q", i,
				sel.Adapter.Schema, sel.Adapter.Version, want.Adapter.Schema, want.Adapter.Version)
		}
		if len(sel.Adapter.Completeness) != len(want.Adapter.Completeness) {
			t.Errorf("source %d kept %d completeness reasons, want %d", i,
				len(sel.Adapter.Completeness), len(want.Adapter.Completeness))
		}
		for j := range want.Adapter.Completeness {
			if sel.Adapter.Completeness[j] != want.Adapter.Completeness[j] {
				t.Errorf("source %d completeness %d is %+v, want %+v", i, j,
					sel.Adapter.Completeness[j], want.Adapter.Completeness[j])
			}
		}
	}

	// 3. cookbook policy and lens identities and versions.
	wantAssets := original.Body.Cookbook
	if len(got.Body.Cookbook) != len(wantAssets) {
		t.Fatalf("kept %d cookbook assets, want %d", len(got.Body.Cookbook), len(wantAssets))
	}
	kinds := map[string]bool{}
	for i, a := range got.Body.Cookbook {
		if a != wantAssets[i] {
			t.Errorf("cookbook asset %d is %+v, want %+v", i, a, wantAssets[i])
		}
		kinds[a.Kind] = true
	}
	if !kinds[AssetPolicy] || !kinds[AssetLens] {
		t.Errorf("cookbook kinds %v lack a policy or a lens", kinds)
	}

	// 4. selected frontier roots and prior-hypothesis identities.
	if strings.Join(got.Body.Frontier.Roots, ",") != "hyp-0001,hyp-0002" {
		t.Errorf("frontier roots %v", got.Body.Frontier.Roots)
	}
	if strings.Join(got.Body.Frontier.Prior, ",") != "hyp-0000" {
		t.Errorf("prior hypotheses %v", got.Body.Frontier.Prior)
	}

	// 5. Code version and profile ID/revision, read from the embedded worker
	// receipt rather than copied beside it.
	if got.Body.Worker == nil {
		t.Fatal("the embedded worker receipt is gone")
	}
	if got.Body.Worker.Worker != (worker.Identity{Name: "code", Version: "1.4.2"}) {
		t.Errorf("Code identity %+v", got.Body.Worker.Worker)
	}
	if got.Body.Worker.Profile != (worker.ProfileRef{ID: "profile-a", Revision: 4}) {
		t.Errorf("profile reference %+v", got.Body.Worker.Profile)
	}

	// 6. resolved provider/model/thinking metadata returned by Code.
	for key, want := range map[string]string{"provider": "local", "model": "m-1", "thinking": "high"} {
		if got.Body.Worker.Metadata[key] != want {
			t.Errorf("resolved metadata %q is %q, want %q", key, got.Body.Worker.Metadata[key], want)
		}
	}

	// 7. sandbox, tool, repository and public-research capability versions.
	wantCaps := CapabilityVersions{Sandbox: "sandbox/3", Tool: "broker/2",
		Repository: "repo/1", PublicResearch: "research/1"}
	if got.Body.Capabilities != wantCaps {
		t.Errorf("capability versions %+v, want %+v", got.Body.Capabilities, wantCaps)
	}

	// 8. analysis job/prompt/schema version.
	wantJob := JobVersions{Job: 1, Prompt: "explore/4", Schema: "babel.analysis-result/1"}
	if got.Body.Job != wantJob {
		t.Errorf("job versions %+v, want %+v", got.Body.Job, wantJob)
	}

	// 9. redaction and disclosure policy version.
	wantPolicy := PolicyVersions{Redaction: "redaction/2", Disclosure: "disclosure/1"}
	if got.Body.Policy != wantPolicy {
		t.Errorf("policy versions %+v, want %+v", got.Body.Policy, wantPolicy)
	}

	// §6.5's additions: retrieval and tool traces, deferred and rejected
	// candidates, failures, resource use and timing.
	if len(got.Body.Retrieval) != 2 || len(got.Body.Retrieval[0].Results) != 2 {
		t.Errorf("retrieval trace lost steps or hits: %+v", got.Body.Retrieval)
	}
	if got.Body.Retrieval[0].Results[0].Evidence.Locator() !=
		original.Body.Retrieval[0].Results[0].Evidence.Locator() {
		t.Error("a retrieval hit lost the locator that recovers it")
	}
	if len(got.Body.Worker.ToolRequests) != 3 {
		t.Errorf("tool trace kept %d requests, want 3", len(got.Body.Worker.ToolRequests))
	}
	if len(got.Body.Deferred) != 1 || got.Body.Deferred[0].ID != "hyp-0003" {
		t.Errorf("deferred candidates %+v", got.Body.Deferred)
	}
	if len(got.Body.Deferred[0].Origin) != 1 {
		t.Error("a deferred candidate lost its origin evidence")
	}
	if len(got.Body.Rejected) != 1 || got.Body.Rejected[0].ID != "hyp-0004" {
		t.Errorf("rejected candidates %+v", got.Body.Rejected)
	}
	if len(got.Body.Failures) != 1 || got.Body.Failures[0].Code != "research-refused" {
		t.Errorf("failures %+v", got.Body.Failures)
	}
	if got.Body.Worker.Failure == nil || got.Body.Worker.Failure.Code != "tool-timeout" {
		t.Errorf("the worker's own failure record is gone: %+v", got.Body.Worker.Failure)
	}
	if got.Body.Resources.CPUSeconds == nil || *got.Body.Resources.CPUSeconds != 41.5 {
		t.Errorf("resource use %+v", got.Body.Resources)
	}
	if !got.Body.Timing.StartedAt.Equal(runStart) || !got.Body.Timing.FinishedAt.Equal(runEnd) {
		t.Errorf("timing %+v", got.Body.Timing)
	}

	// The header's plaintext-eligible fields survive too, since they are what a
	// listing reads when the body is sealed.
	if got.Header.ID != original.Header.ID || got.Header.RunID != original.Header.RunID {
		t.Errorf("header identity %+v", got.Header)
	}
	if got.Header.PreparationID != original.Preparation.ID {
		t.Errorf("header preparation %q, want %q", got.Header.PreparationID, original.Preparation.ID)
	}
	if got.Header.Counts != original.Header.Counts {
		t.Errorf("counts %+v, want %+v", got.Header.Counts, original.Header.Counts)
	}
	if !got.Header.RecordedAt.Equal(recorded) || got.Header.Sync != SyncPending {
		t.Errorf("header state %+v", got.Header)
	}
}

// Absence is not zero. A resource nobody measured must stay absent through
// storage, or a later reviewer reads "the sandbox wrote nothing" where the
// record actually says "nobody counted".
func TestAbsentResourceValuesStayDistinctFromZero(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	r := mustReceipt(t)
	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	got, err := s.Receipt(ctx, r.Header.ID)
	if err != nil {
		t.Fatalf("Receipt: %v", err)
	}

	res := got.Body.Resources
	if res.SandboxBytesWritten == nil {
		t.Fatal("a measured zero came back as absent")
	}
	if *res.SandboxBytesWritten != 0 {
		t.Errorf("measured zero came back as %d", *res.SandboxBytesWritten)
	}
	if res.MaxRSSBytes != nil {
		t.Errorf("an unmeasured value came back as %d", *res.MaxRSSBytes)
	}
	if res.ToolCalls != nil {
		t.Errorf("an unmeasured count came back as %d", *res.ToolCalls)
	}
	if res.CPUSeconds == nil || *res.CPUSeconds != 41.5 {
		t.Errorf("a measured value did not survive: %+v", res.CPUSeconds)
	}

	// The distinction is visible in the stored bytes as well, which is what a
	// reader outside Go sees.
	var payload []byte
	if err := s.db.QueryRow(`SELECT payload FROM run_receipt WHERE id = ?`,
		string(r.Header.ID)).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"max_rss_bytes":null`) {
		t.Errorf("an absent measurement is not stored as null: %s", payload)
	}
	if !strings.Contains(string(payload), `"sandbox_bytes_written":0`) {
		t.Errorf("a measured zero is not stored as zero: %s", payload)
	}
}

// Amending appends. The prior revision must still be readable, unchanged, and
// linked to by its successor.
func TestAmendmentAppendsAndLeavesThePriorRevisionIntact(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	first := mustReceipt(t)
	if err := s.PutReceipt(ctx, first); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	firstStored, err := s.Receipt(ctx, first.Header.ID)
	if err != nil {
		t.Fatalf("Receipt: %v", err)
	}

	body := testBody(t)
	body.AmendmentReason = "the sandbox build was misrecorded"
	body.Capabilities.Sandbox = "sandbox/4"
	second, err := Amend(first, NewReceiptID(), body, amendedAt)
	if err != nil {
		t.Fatalf("Amend: %v", err)
	}
	if err := s.PutReceipt(ctx, second); err != nil {
		t.Fatalf("PutReceipt(amendment): %v", err)
	}

	reread, err := s.Receipt(ctx, first.Header.ID)
	if err != nil {
		t.Fatalf("re-read the prior revision: %v", err)
	}
	if reread.Body.Capabilities.Sandbox != firstStored.Body.Capabilities.Sandbox {
		t.Errorf("the prior revision changed: sandbox %q, want %q",
			reread.Body.Capabilities.Sandbox, firstStored.Body.Capabilities.Sandbox)
	}
	if reread.Body.AmendmentReason != "" {
		t.Error("the prior revision acquired an amendment reason")
	}

	chain, err := s.Revisions(ctx, "run-1")
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("run has %d revisions, want 2", len(chain))
	}
	if chain[0].Header.ID != first.Header.ID || chain[1].Header.ID != second.Header.ID {
		t.Errorf("revision order is %s then %s", chain[0].Header.ID, chain[1].Header.ID)
	}
	if chain[1].Header.Supersedes != chain[0].Header.ID {
		t.Errorf("revision 2 supersedes %q, want %q", chain[1].Header.Supersedes, chain[0].Header.ID)
	}
	if chain[1].Body.Capabilities.Sandbox != "sandbox/4" {
		t.Errorf("the amendment did not record its correction: %+v", chain[1].Body.Capabilities)
	}
	if chain[1].Body.AmendmentReason == "" {
		t.Error("the amendment does not say what it corrects")
	}
}

// A run's history is a chain. Two amendments of the same revision would leave
// no answer to what the run says now, so the second one loses.
func TestStoreRejectsForkedAndBrokenRevisionChains(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	first := mustReceipt(t)
	if err := s.PutReceipt(ctx, first); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	body := testBody(t)
	body.AmendmentReason = "first correction"
	second, err := Amend(first, NewReceiptID(), body, amendedAt)
	if err != nil {
		t.Fatalf("Amend: %v", err)
	}
	if err := s.PutReceipt(ctx, second); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}

	fork, err := Amend(first, NewReceiptID(), body, amendedAt)
	if err != nil {
		t.Fatalf("Amend: %v", err)
	}
	if err := s.PutReceipt(ctx, fork); !errors.Is(err, ErrAmendConflict) {
		t.Errorf("forking the chain returned %v, want ErrAmendConflict", err)
	}

	if err := s.PutReceipt(ctx, first); !errors.Is(err, ErrExists) {
		t.Errorf("re-storing a receipt returned %v, want ErrExists", err)
	}

	orphan := second
	orphan.Header.ID = NewReceiptID()
	orphan.Header.Supersedes = NewReceiptID()
	if err := s.PutReceipt(ctx, orphan); !errors.Is(err, ErrAmendConflict) {
		t.Errorf("an amendment of an unstored revision returned %v, want ErrAmendConflict", err)
	}

	skipped := second
	skipped.Header.ID = NewReceiptID()
	skipped.Header.Revision = 4
	if err := s.PutReceipt(ctx, skipped); !errors.Is(err, ErrAmendConflict) {
		t.Errorf("a skipped revision returned %v, want ErrAmendConflict", err)
	}

	if _, err := s.Revisions(ctx, "run-unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Revisions for an unknown run returned %v, want ErrNotFound", err)
	}
}

// Immutability that depends on this package never writing the wrong statement
// is not immutability. The database refuses the edit and the delete itself.
func TestStoredRecordsCannotBeEditedOrDeleted(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	r := mustReceipt(t)
	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}

	if _, err := s.db.Exec(`UPDATE run_receipt SET payload = ? WHERE id = ?`,
		[]byte(`{}`), string(r.Header.ID)); err == nil {
		t.Error("the database accepted an edit of a stored receipt")
	}
	if _, err := s.db.Exec(`DELETE FROM run_receipt WHERE id = ?`, string(r.Header.ID)); err == nil {
		t.Error("the database accepted a deletion of a stored receipt")
	}
	if _, err := s.db.Exec(`UPDATE run_preparation SET payload = ? WHERE id = ?`,
		[]byte(`{}`), string(r.Preparation.ID)); err == nil {
		t.Error("the database accepted an edit of a stored preparation")
	}
	if _, err := s.db.Exec(`DELETE FROM run_preparation WHERE id = ?`,
		string(r.Preparation.ID)); err == nil {
		t.Error("the database accepted a deletion of a stored preparation")
	}
	if _, err := s.Receipt(ctx, r.Header.ID); err != nil {
		t.Fatalf("the receipt did not survive the attempts: %v", err)
	}
}

// The commit state is the one thing that may change after insert: it records
// where the record now exists, not what it says.
func TestCommitStateMovesWithoutTouchingContent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	r := mustReceipt(t)
	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	before, err := s.Receipt(ctx, r.Header.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := s.SyncState(ctx, r.Header.ID); err != nil || state != SyncPending {
		t.Fatalf("initial sync state %q (%v), want %q", state, err, SyncPending)
	}

	if err := s.MarkReceiptCommitted(ctx, r.Header.ID); err != nil {
		t.Fatalf("MarkReceiptCommitted: %v", err)
	}
	if err := s.MarkPreparationCommitted(ctx, r.Preparation.ID); err != nil {
		t.Fatalf("MarkPreparationCommitted: %v", err)
	}
	if state, err := s.SyncState(ctx, r.Header.ID); err != nil || state != SyncCommitted {
		t.Fatalf("sync state %q (%v), want %q", state, err, SyncCommitted)
	}

	after, err := s.Receipt(ctx, r.Header.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := before.MarshalBody()
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := after.MarshalBody()
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Error("recording the commit state changed the record")
	}

	if err := s.MarkReceiptCommitted(ctx, "rcpt-missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("committing an unstored receipt returned %v, want ErrNotFound", err)
	}
}

// No credential reaches the durable file, whichever worker-controlled string
// carried it in.
func TestStoredBytesNeverCarryTheCredentialSentinel(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	prep := mustPreparation(t, preparedAt, testSelection())
	body := testBody(t)
	body.Worker.Metadata["provider"] = credentialSentinel
	body.Worker.StderrTail = "authorization: Bearer " + credentialSentinel
	body.Worker.Failure.Message = "postgres://babel:" + credentialSentinel + "@db:5432/babel"
	body.Failures[0].Message = "broker refused " + credentialSentinel
	body.Retrieval[0].Query = credentialSentinel

	r, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}
	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, name := range []string{DatabaseName, DatabaseName + "-wal", DatabaseName + "-shm"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(raw), credentialSentinel) {
			t.Errorf("%s contains the credential sentinel", name)
		}
	}
}

// The durable file is shared with the hypothesis frontier, so this component's
// migration must register beside another's rather than assuming the file is
// its own.
func TestOpenCoexistsWithAnotherComponentInTheSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DatabaseName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migration(
		component TEXT PRIMARY KEY, version INTEGER NOT NULL);
		INSERT INTO schema_migration(component, version) VALUES('frontier', 3);
		CREATE TABLE frontier_hypothesis(id TEXT PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open beside another component: %v", err)
	}
	defer s.Close()
	if err := s.PutPreparation(context.Background(),
		mustPreparation(t, preparedAt, testSelection())); err != nil {
		t.Fatalf("PutPreparation: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT version FROM schema_migration WHERE component = 'frontier'`).
		Scan(&version); err != nil {
		t.Fatalf("the other component's migration row is gone: %v", err)
	}
	if version != 3 {
		t.Errorf("the other component's version is %d, want 3", version)
	}
}

// Losing a retrieval index costs a re-index; losing a receipt loses analysis.
// So an unfamiliar schema stops the store rather than rebuilding the file,
// which is the opposite of what internal/catalog's cache does.
func TestUnknownSchemaVersionIsRefusedRatherThanRebuilt(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	prep := mustPreparation(t, preparedAt, testSelection())
	if err := s.PutPreparation(context.Background(), prep); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE schema_migration SET version = 99 WHERE component = ?`,
		storeComponent); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("opened a durable database written by an unsupported schema")
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM run_preparation`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the refused open discarded %d durable rows", 1-n)
	}
}

func TestPutReceiptRejectsAMismatchedPreparation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	r := mustReceipt(t)
	other := mustPreparation(t, preparedAt.Add(time.Hour), testSelection())
	r.Preparation = other
	if err := s.PutReceipt(ctx, r); err == nil {
		t.Fatal("stored a receipt whose header names a different scope than it carries")
	}
	if _, err := s.Receipt(ctx, r.Header.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the rejected receipt was stored anyway: %v", err)
	}
}
