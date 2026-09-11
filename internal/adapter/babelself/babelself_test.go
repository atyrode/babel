package babelself_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/babelself"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/transcript"
)

// servedExcerpt is the text a corpus search put on the wire. It reads like a
// transcript record because that is what it is: the point of the boundary is
// that these bytes are what a model may read and what a durable record may
// not hold.
const servedExcerpt = "synthetic corpus record: the zeppelin cache warms lazily on the third request"

// servedPayload is one served corpus-search result as the engine hands it
// back inside the turn's messages. It is written as a literal rather than
// built from internal/explore's types, so this test proves the writer
// against the wire rather than against a struct both sides share.
func servedPayload(excerpt string, locator event.Locator) string {
	payload, err := json.Marshal(map[string]any{
		"schema": "babel.corpus-search/1",
		"query":  "zeppelin cache",
		"limit":  10,
		"hits": []any{map[string]any{
			"harness":   "omp",
			"source_id": "synthetic-project/session-one",
			"index":     7,
			"excerpt":   excerpt,
			"locator":   locator,
		}},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func testLocator() event.Locator {
	return event.Locator{
		Path:       "/synthetic/sessions/omp/session-one.jsonl",
		Line:       12,
		ByteOffset: 901,
		Digest:     strings.Repeat("a1", 32),
	}
}

// locatorRedactor is the facility's half of the boundary, standing in for
// internal/explore.RedactServedResult: it answers with the locators that
// recover what it served and never with the content. The real one is proven
// against a real run in internal/explore.
func locatorRedactor(t *testing.T) babelself.Redactor {
	t.Helper()
	return func(result string) ([]event.Locator, string) {
		var payload struct {
			Hits []struct {
				Locator event.Locator `json:"locator"`
			} `json:"hits"`
		}
		if json.Unmarshal([]byte(result), &payload) != nil {
			return nil, "unreadable served payload"
		}
		locators := make([]event.Locator, 0, len(payload.Hits))
		for _, hit := range payload.Hits {
			locators = append(locators, hit.Locator)
		}
		return locators, "the served excerpts are not persisted; the locators recover the records"
	}
}

func newStore(t *testing.T, root string) *babelself.Store {
	t.Helper()
	store, err := babelself.NewStore(babelself.StoreConfig{
		Root:      root,
		Redact:    locatorRedactor(t),
		Workspace: "/synthetic/workspace",
		Profile:   "synthetic-profile@1",
		Recipes:   []string{"outcome-integrity"},
		Now:       func() time.Time { return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// turn is one agent_end message list: the job document, a tool call, the
// result Babel answered it with, and the model's own conclusion.
func turn(excerpt string) json.RawMessage {
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": "explore the synthetic corpus"}},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "the cache warming claim is worth checking"},
				map[string]any{"type": "toolCall", "id": "toolu_1", "name": "babel_corpus_search",
					"arguments": map[string]any{"query": "zeppelin cache"}},
			},
		},
		map[string]any{
			"role":       "toolResult",
			"toolCallId": "toolu_1",
			"toolName":   "babel_corpus_search",
			"isError":    false,
			"content": []any{map[string]any{
				"type": "text", "text": servedPayload(excerpt, testLocator()),
			}},
			"details": map[string]any{"echo": excerpt},
		},
		map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "the cache warms lazily, and one record says so"}},
		},
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		panic(err)
	}
	return encoded
}

// TestServedExcerptNeverReachesTheLog is the §9 guard this package exists
// around, and it matters more than the feature it guards.
//
// A retrieved excerpt reaches the model because a model that cannot read a
// record cannot form an observation about it. Written into a session log it
// would be a second plaintext copy of the archive, readable by anyone with
// access to Babel's data directory — which is exactly what §9 forbids and
// what the receipt already gives up the text to avoid. So the wire carries
// the excerpt and the log carries the locator.
//
// The whole file is searched rather than the field a leak was expected in,
// because a guard covering one field is one the next field reintroduces: the
// result arrives with its excerpt in the payload and again inside a
// tool-specific details object, and a writer that copied either has copied
// the corpus.
func TestServedExcerptNeverReachesTheLog(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	log, err := store.Open("run-20260911T120000Z", "explore")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := log.Turn(turn(servedExcerpt), time.Date(2026, 9, 11, 12, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	written, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatalf("read the written log: %v", err)
	}
	body := string(written)
	if strings.Contains(body, servedExcerpt) {
		t.Errorf("the session log quotes the served excerpt %q; the wire carries content to the model and the log carries locators only", servedExcerpt)
	}

	// Giving up the text must not have cost the record its way back to the
	// bytes: a withheld result that cannot be reopened is not a redaction,
	// it is a deletion.
	locator := testLocator()
	for _, want := range []string{locator.Path, locator.Digest, babelself.WithheldSchema} {
		if !strings.Contains(body, want) {
			t.Errorf("the session log does not carry %q, so the withheld result cannot be reopened", want)
		}
	}

	// Babel's own reasoning is the other half of the contract. The job
	// document, the model's thinking, its tool call with the query it chose,
	// and its conclusion are all Babel's or the model's own words, and a
	// transcript that dropped them would archive nothing worth reading.
	for _, want := range []string{
		"explore the synthetic corpus",
		"the cache warming claim is worth checking",
		"zeppelin cache",
		"the cache warms lazily, and one record says so",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the session log lost %q, which is Babel's own reasoning rather than anything a facility served", want)
		}
	}
}

// TestAnotherHarnessesSessionsAreNotClaimed covers the one thing the layout
// alone cannot settle.
//
// "<root>/<dir>/<stem>.jsonl" is OMP's layout as well as this one's, and
// every adapter is asked to scan a root an operator names on the command
// line ("--roots DIR"). An adapter recognizing the shape rather than its own
// files would claim OMP's sessions, report them under the harness "babel",
// and hand a corpus scan a log no Babel-harness parser can classify. The
// compound extension is what keeps the two apart without reading bytes.
func TestAnotherHarnessesSessionsAreNotClaimed(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-synthetic-workspace")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(project, "2026-01-02T03-04-05-678Z_session.jsonl")
	record := `{"type":"session","id":"00000000-0000-4000-8000-000000000001","cwd":"/synthetic"}` + "\n"
	if err := os.WriteFile(foreign, []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}

	found, err := babelself.New().Discover(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("claimed %d sessions this adapter did not write: %+v", len(found), found)
	}
}

// TestAWriterCannotBeBuiltWithoutARedactor covers the fail-closed half. The
// redaction is not a policy a caller may decline: a writer with no redactor
// is a path from the archive into a second copy of it, so there must be no
// way to construct one.
func TestAWriterCannotBeBuiltWithoutARedactor(t *testing.T) {
	if _, err := babelself.NewStore(babelself.StoreConfig{Root: t.TempDir()}); err == nil {
		t.Fatal("NewStore accepted a store with no redactor")
	}
	if _, err := babelself.NewStore(babelself.StoreConfig{Redact: locatorRedactor(t)}); err == nil {
		t.Fatal("NewStore accepted a store with no root")
	}
}

// TestRepeatedTurnsAppendOnlyWhatIsNew covers the shape of the frame the
// writer is fed: an engine reports the whole conversation with every
// agent_end, so a writer that appended the list each time would produce a
// log claiming the model said everything twice.
func TestRepeatedTurnsAppendOnlyWhatIsNew(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	log, err := store.Open("run-repeat", "explore")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	at := time.Date(2026, 9, 11, 12, 5, 0, 0, time.UTC)
	first := turn(servedExcerpt)
	if err := log.Turn(first, at); err != nil {
		t.Fatalf("first Turn: %v", err)
	}
	after := len(records(t, log.Path()))
	if err := log.Turn(first, at); err != nil {
		t.Fatalf("second Turn: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if again := len(records(t, log.Path())); again != after {
		t.Errorf("reporting the same conversation twice wrote %d records, then %d; the second report was not new", after, again)
	}
}

// TestAWrittenSessionIsDiscoveredAndParses is the round trip that makes a
// written log a session rather than a file: the adapter finds it under its
// own root, describes it from its own header, and internal/transcript renders
// its records as events instead of decoding them as unparseable.
func TestAWrittenSessionIsDiscoveredAndParses(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)
	log, err := store.Open("run-20260911T120000Z", "challenge")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := log.Turn(turn(servedExcerpt), time.Date(2026, 9, 11, 12, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx := context.Background()
	a := babelself.New()
	if a.Harness() != "babel" {
		t.Errorf("Harness() = %q, want babel", a.Harness())
	}
	found, err := a.Discover(ctx, []string{root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("discovered %d sessions, want 1: %+v", len(found), found)
	}
	if found[0].SourceID != log.SourceID() {
		t.Errorf("discovered source id %q, want %q", found[0].SourceID, log.SourceID())
	}
	if found[0].PrimaryPath != log.Path() {
		t.Errorf("discovered %q, want the written log %q", found[0].PrimaryPath, log.Path())
	}
	if !adapter.ValidSourceID(found[0].SourceID) {
		t.Errorf("source id %q is not a valid adapter identity", found[0].SourceID)
	}

	desc, err := a.Describe(ctx, found[0])
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc.Meta.Title == nil || *desc.Meta.Title == "" {
		t.Error("the description carries no title, though the writer wrote one")
	}
	if desc.Meta.TitleProvenance != adapter.TitleRecorded {
		t.Errorf("title provenance = %q, want %q", desc.Meta.TitleProvenance, adapter.TitleRecorded)
	}
	if desc.Meta.Workspace == nil || *desc.Meta.Workspace != "/synthetic/workspace" {
		t.Errorf("workspace = %v, want the recorded one", desc.Meta.Workspace)
	}
	if desc.Meta.CreatedAt == nil {
		t.Error("the description carries no creation time, though the session record holds one")
	}
	if !desc.ContinuationGrade {
		t.Error("a log that references nothing outside itself was described as an incomplete closure")
	}

	// The parser half. Without the harness case these records decode as
	// unparseable and the session view renders raw JSON.
	total, events, err := transcript.Events(found[0].PrimaryPath, a.Harness(), 0, 100)
	if err != nil {
		t.Fatalf("transcript.Events: %v", err)
	}
	if total == 0 {
		t.Fatal("the transcript holds no records")
	}
	roles := map[string]int{}
	for _, e := range events {
		if e.Kind == "message" {
			roles[e.Role]++
		}
	}
	if roles["user"] == 0 || roles["assistant"] == 0 || roles["toolResult"] == 0 {
		t.Errorf("parsed message roles %v, want the job document, the model's turns and the withheld result", roles)
	}
	for _, e := range events {
		if e.Kind == "message" && e.Time == nil {
			t.Errorf("event %d parsed without a time, so the writer's timestamps are not RFC3339", e.Index)
		}
	}
}

// TestBackupRootsCoversTheWriteRoot is the archive half. `archive push`
// snapshots exactly the union of the adapters' BackupRoots that exist on the
// host (internal/cli/archive.go existingRoots), so a transcript written
// anywhere other than under a backup root would be archived by nothing.
func TestBackupRootsCoversTheWriteRoot(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	root, ok := babelself.Root()
	if !ok {
		t.Fatal("Root() found nowhere to write with XDG_DATA_HOME set")
	}
	if want := filepath.Join(data, "babel", "analysis"); root != want {
		t.Errorf("Root() = %q, want %q", root, want)
	}

	a := babelself.New()
	backup := a.BackupRoots()
	if len(backup) == 0 {
		t.Fatal("BackupRoots() is empty, so archive push would capture no analysis session")
	}
	log, err := newStore(t, root).Open("run-archived", "explore")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	covered := false
	for _, r := range backup {
		if strings.HasPrefix(log.Path(), r+string(filepath.Separator)) {
			covered = true
		}
	}
	if !covered {
		t.Errorf("the written log %q is under none of the backup roots %v", log.Path(), backup)
	}
	// existingRoots skips a root this host does not have, so the root must
	// be a directory by the time a log exists under it.
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Errorf("the analysis root is not a directory after a log was written: %v", err)
	}
}

// records reads the log's lines, which are one JSON object each.
func records(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("record %d of %s is not one JSON object: %s", i+1, path, line)
		}
	}
	return lines
}
