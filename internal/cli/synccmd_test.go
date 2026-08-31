package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// hostileKind is a record kind and a run id no writer would mint, planted in
// the report to prove that what reaches a terminal has been rendered rather
// than trusted. The journal is a local file an operator or a half-restored
// backup can put anything in, and the shared catalog's own kind vocabulary is
// enforced remotely, so the renderer is what stands between the two
// (SPEC.md §8, §9).
const hostileKind = "\x1b[31mfinding\x1b]0;retitled\x07\u202egnidnif"

// escapeBytes are the sequences that must never reach a terminal raw: the
// CSI/OSC introducer, its C1 spelling, and a bidi override.
var escapeBytes = []string{"\x1b", "\x9b", "\u202e"}

// assertNoEscape proves neither stream carries a byte that can move a cursor
// or reorder text.
func assertNoEscape(t *testing.T, what, stdout, stderr string) {
	t.Helper()
	for _, raw := range escapeBytes {
		if strings.Contains(stdout, raw) {
			t.Errorf("%s: stdout carries %q raw:\n%s", what, raw, stdout)
		}
		if strings.Contains(stderr, raw) {
			t.Errorf("%s: stderr carries %q raw:\n%s", what, raw, stderr)
		}
	}
}

// TestSyncWithoutSharedStorageIsNotAFailure is the local-only contract.
//
// A machine with no storage.json owes the fleet nothing, so `babel sync` there
// is a report and not a refusal: it exits 0, says which absence it found, and
// keeps stdout free of prose the way every other command in this package does.
// Exiting non-zero would put a local-only deployment permanently in a state a
// wrapper script reads as broken.
func TestSyncWithoutSharedStorageIsNotAFailure(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.ok("sync")
	if stdout != "" {
		t.Errorf("a non-JSON sync with nothing to publish wrote to stdout: %q", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one diagnostic line, got %d:\n%s", len(lines), stderr)
	}
	if !strings.Contains(stderr, "local-only") {
		t.Errorf("the diagnostic does not name local mode: %q", stderr)
	}

	// --json is the same answer as one parseable document, which is what a
	// scheduled caller reads: "nothing to publish" must be a value it can
	// branch on rather than a line it has to match.
	stdout, _ = f.ok("sync", "--json")
	res := decode[syncResult](t, stdout)
	if res.Configured {
		t.Errorf("a machine with no storage.json reported itself configured: %+v", res)
	}
	if res.Reason == "" {
		t.Error("the document names no reason, so a caller cannot tell why nothing published")
	}
	if res.RunsCommitted != 0 || res.RunsPending != 0 || res.ObjectsWritten != 0 || res.Undeclared != 0 {
		t.Errorf("nothing was attempted, so every count must be zero: %+v", res)
	}
}

// TestSyncGenerateKeyWritesOnePrivateDocument covers the one write this command
// performs.
//
// Three properties matter and each has cost a real deployment somewhere: the
// document is private, the key material never reaches a stream, and a second
// invocation refuses rather than replacing it. The refusal is the load-bearing
// one - replacing the document orphans every sealed object written under the
// keys it held, and Babel never deletes a remote object, so those objects would
// stay unreadable forever.
func TestSyncGenerateKeyWritesOnePrivateDocument(t *testing.T) {
	f := newFixture(t)
	path := config.PayloadKeysPath()

	stdout, stderr := f.ok("sync", "--generate-key", "k1")
	if !strings.Contains(stdout, "k1") || !strings.Contains(stdout, path) {
		t.Errorf("the report names neither the key id nor the document: %q", stdout)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the payload key document was not written: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("payload key document mode is %#o, want 0600", mode)
	}
	keys, found, err := config.LoadPayloadKeys()
	if err != nil || !found {
		t.Fatalf("the written document does not load back: found=%v err=%v", found, err)
	}
	if keys.ActiveKeyID != "k1" || len(keys.Keys) != 1 {
		t.Fatalf("want one active key k1, got %+v", keys)
	}
	material := keys.Keys[0].Key
	if material == "" {
		t.Fatal("the document carries no key material, so nothing could seal")
	}
	assertNoMaterial(t, "generate", material, stdout, stderr)

	// A second document would be a replacement, and the remedy has to be in
	// the message: an operator who cannot see why it refused will delete the
	// file, which is the one irreversible mistake here.
	stdout, stderr = f.mustExit(exitFailure, "sync", "--generate-key", "k2")
	if !strings.Contains(stderr, path) {
		t.Errorf("the refusal does not name the document: %q", stderr)
	}
	assertNoMaterial(t, "refusal", material, stdout, stderr)
	if keys, _, err := config.LoadPayloadKeys(); err != nil || keys.ActiveKeyID != "k1" {
		t.Fatalf("the refused invocation changed the document: %+v err=%v", keys, err)
	}
}

// TestSyncGenerateKeyEmitsOneDocumentForJSON keeps `--generate-key --json`
// honest: a machine-readable invocation that printed prose would break the
// contract that stdout carries exactly one parseable document.
func TestSyncGenerateKeyEmitsOneDocumentForJSON(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.ok("sync", "--generate-key", "k1", "--json")
	res := decode[syncKeyResult](t, stdout)
	if res.KeyID != "k1" {
		t.Errorf("key id = %q, want k1", res.KeyID)
	}
	if res.Path != config.PayloadKeysPath() {
		t.Errorf("path = %q, want %q", res.Path, config.PayloadKeysPath())
	}
	keys, _, err := config.LoadPayloadKeys()
	if err != nil {
		t.Fatal(err)
	}
	assertNoMaterial(t, "generate --json", keys.Keys[0].Key, stdout, stderr)
}

// assertNoMaterial proves the key material reached neither stream. It is the
// one value in Babel that no report, diagnostic or document may carry: the id
// is admitted in plaintext beside every ciphertext, the bytes never are.
func assertNoMaterial(t *testing.T, what, material, stdout, stderr string) {
	t.Helper()
	if strings.Contains(stdout, material) {
		t.Errorf("%s: key material reached stdout", what)
	}
	if strings.Contains(stderr, material) {
		t.Errorf("%s: key material reached stderr", what)
	}
}

// TestSyncGenerateKeyRejectsAnInvalidID proves a bad id is a rejected
// invocation and not a failure, so a wrapper script can tell the operator
// mistyped something from the endpoint being down.
func TestSyncGenerateKeyRejectsAnInvalidID(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.mustExit(exitUsage, "sync", "--generate-key", "Bad Key!")
	if stdout != "" {
		t.Errorf("a rejected invocation wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Usage: babel sync") {
		t.Errorf("the rejection does not show the command's usage: %q", stderr)
	}
	if _, found, err := config.LoadPayloadKeys(); found || err != nil {
		t.Fatalf("a rejected invocation wrote a payload key document: found=%v err=%v", found, err)
	}
}

// TestSyncHelpIsServedOnStdout holds this package's help contract: -h is a
// successful request whose answer is documentation, so it goes to stdout and
// leaves stderr empty.
func TestSyncHelpIsServedOnStdout(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.ok("sync", "-h")
	if stderr != "" {
		t.Fatalf("sync help wrote diagnostics: %q", stderr)
	}
	for _, want := range []string{"Usage: babel sync", "--generate-key", "--json"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("sync usage is missing %q:\n%s", want, stdout)
		}
	}
}

// TestSyncReportRendersHostileValuesSafely drives the report with a record kind
// and a run id that carry a presentation attack.
//
// Both values are read back out of a local SQLite file and a remote endpoint's
// error text, which is exactly the class SPEC.md §8 requires to be rendered
// rather than trusted, and both the terminal report and the machine-readable
// document are checked because Sanitize applies on stdout as well as stderr.
func TestSyncReportRendersHostileValuesSafely(t *testing.T) {
	rep := babelsync.Report{
		Committed: map[sharedcatalog.RecordKind]int{
			sharedcatalog.RecordKind(hostileKind): 2,
			sharedcatalog.KindFinding:             1,
		},
		Pending: map[sharedcatalog.RecordKind]int{
			sharedcatalog.RecordKind(hostileKind): 3,
		},
		RunsCommitted:  1,
		RunsPending:    1,
		ObjectsWritten: 2,
		Undeclared:     4,
		Failures: []babelsync.RunFailure{{
			RunID: hostileKind,
			Err:   errors.New("publish " + hostileKind + ": endpoint refused"),
		}},
	}
	res := syncReport(rep)

	var stdout, stderr strings.Builder
	a := &app{stdout: &stdout, stderr: &stderr}
	if err := a.writeSync(res); err != nil {
		t.Fatalf("writeSync: %v", err)
	}
	assertNoEscape(t, "terminal report", stdout.String(), stderr.String())
	// Non-vacuity: the hostile kind really did reach the report, escaped.
	if !strings.Contains(stdout.String(), "finding") {
		t.Fatalf("the hostile kind never reached the report, so escaping it proves nothing:\n%s", stdout.String())
	}
	// The undeclared count is a state rather than a fault, so the report says
	// what it means instead of leaving four stuck records to be inferred.
	if !strings.Contains(stderr.String(), "never dropped") {
		t.Errorf("undeclared records went unexplained:\n%s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := a.emitJSON(res); err != nil {
		t.Fatalf("emitJSON: %v", err)
	}
	assertNoEscape(t, "json report", stdout.String(), stderr.String())
	got := decode[syncResult](t, stdout.String())
	if len(got.Committed) != 2 || got.Committed[0].Kind >= got.Committed[1].Kind {
		t.Errorf("committed kinds are not in sorted order, so two reports would not diff: %+v", got.Committed)
	}
	if len(got.Failures) != 1 || got.Failures[0].RunID == "" {
		t.Errorf("the failed closure is not named: %+v", got.Failures)
	}
}

// TestSyncOpenPublisherIsSilentWithoutSharedStorage pins the contract every
// durable writer and the push reconcile step rest on: an absence is a nil
// publisher and a nil error, never a failure, and the cleanup is safe to call
// on that path too.
func TestSyncOpenPublisherIsSilentWithoutSharedStorage(t *testing.T) {
	f := newFixture(t)
	d, err := babelDirs()
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	a := &app{stdout: &stdout, stderr: &stderr}
	pub, cleanup, err := a.openPublisher(context.Background(), d)
	if err != nil {
		t.Fatalf("an unconfigured deployment is not an error: %v", err)
	}
	if pub != nil {
		t.Fatal("a deployment with no storage.json built a publisher")
	}
	cleanup()
	// Calling it twice is what a caller that defers it and returns early does.
	cleanup()
	if stdout.String() != "" || stderr.String() != "" {
		t.Errorf("openPublisher reported on an absence: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	// The durable file is not touched either: opening a journal for a
	// deployment that publishes nothing would create state no reader wants.
	if _, err := os.Stat(f.dataDir); !os.IsNotExist(err) {
		t.Errorf("openPublisher created durable state for a local-only deployment: %v", err)
	}
}
