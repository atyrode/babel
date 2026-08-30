package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/reality"
)

// `babel reality import` is the §4.8 trusted inventory import: the one Reality
// write whose facts are not the operator's own assertion. These cases cover the
// three things that make it safe to expose at all — that a success reports which
// facts it authorized, that a bad document authorizes none of them, and that the
// ledger's flat prohibition on credential material reaches the operator legibly
// rather than being swallowed into a generic failure.
//
// Every fixture here is synthetic. Nothing is derived from a real inventory,
// transcript, host, or credential (SPEC.md §10).

// importSourceID is the trusted source every case in this file imports as.
const importSourceID = "synthetic-cli-inventory"

// importDigest stands in for the sha256 of the inventory record a fact was
// read from. reality.FactInput requires a provenance path and digest from any
// non-operator authority, and the value's only job is to be present.
const importDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// probeImportToken is credential-shaped and assembled from parts.
//
// A test for a credential-format detector has to supply a string in that
// format, and a contiguous literal in one of the formats this repository's push
// protection recognizes makes the forge reject every push carrying the file.
// Splitting the literal leaves the assembled constant byte-identical for the
// detector while the source never contains the matching sequence. The
// repository-wide check lives in internal/preflight/fixtureshape_test.go.
const probeImportToken = "gh" + "p_" + "PROBEONLYNOTREALCLIIMPORT1"

// inventory is the ledger state an import needs: a service to author about,
// the machine a placement fact points at, and a registered source.
type inventory struct {
	service string
	machine string
}

// seedInventory registers §4.8's example source — a versioned inventory that
// may author service placement about services, and nothing else — plus the two
// entities its batches refer to. extra widens the declared predicate scope, so
// a case can import a predicate whose value is free text.
//
// The store is opened and closed here rather than held, because the command
// under test opens the same ledger itself: what the tests exercise is the
// shipped wiring, not a store handle a test lent it.
func (f *fixture) seedInventory(extra ...reality.Predicate) inventory {
	f.t.Helper()
	ctx := context.Background()
	store, err := reality.Open(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	defer store.Close()

	service, err := store.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityService,
		Payload: reality.EntityPayload{DisplayName: "synthetic service"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	machine, err := store.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityMachine,
		Payload: reality.EntityPayload{DisplayName: "synthetic machine"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := store.RegisterTrustedSource(ctx, reality.TrustedSourceInput{
		ID:          importSourceID,
		Version:     1,
		Predicates:  append([]reality.Predicate{reality.PredicateServicePlacement}, extra...),
		EntityKinds: []reality.EntityKind{reality.EntityService},
		Payload: reality.TrustedSourcePayload{
			Description: "synthetic inventory of services and their placement",
		},
	}); err != nil {
		f.t.Fatal(err)
	}
	return inventory{service: service.ID, machine: machine.ID}
}

// placementFact renders one service-placement fact in the document shape
// realityImportUsage documents. The JSON is written out by hand rather than
// marshalled from importFactDocument, because the shape an operator copies out
// of the help text is the contract: a renamed field has to fail here.
func placementFact(subject, machine string) string {
	return fmt.Sprintf(`{"subject_id": %q,
		"predicate": "service-placement",
		"value": {"kind": "entity", "object_id": %q},
		"valid_from": "2026-08-01T00:00:00Z",
		"observed_at": "2026-08-01T00:00:00Z",
		"confidence": "high",
		"sensitivity": "routine",
		"provenance": {"path": "synthetic/inventory.jsonl", "line": 1, "byte_offset": 0, "digest": %q},
		"note": "declared placement from the synthetic inventory"}`,
		subject, machine, importDigest)
}

// batch wraps facts in a batch document under one idempotency key.
func batch(key string, facts ...string) string {
	return fmt.Sprintf(`{"batch_key": %q, "facts": [%s]}`, key, strings.Join(facts, ", "))
}

// runStdin drives one invocation whose document arrives on stdin, which is the
// path "-" selects and the one a managed handoff uses (SPEC.md §8).
func (f *fixture) runStdin(document string, args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(document), &out, &errOut)
	return out.String(), errOut.String(), code
}

// TestRealityImportReportsTheFactsItAuthorized covers the success path and the
// reason the command reports facts instead of a verdict: an import authorizes
// facts on a source's authority, and "imported successfully" does not tell an
// operator which claims now stand in the ledger or who is answerable for them.
func TestRealityImportReportsTheFactsItAuthorized(t *testing.T) {
	f := newFixture(t)
	inv := f.seedInventory()

	stdout, stderr, code := f.runStdin(batch("inventory-1", placementFact(inv.service, inv.machine)),
		"reality", "import", "--source", importSourceID, "--from-json", "-", "--json")
	if code != exitOK {
		t.Fatalf("import exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("a successful import wrote diagnostics: %q", stderr)
	}
	res := decodeJSON[importResult](t, stdout)
	if res.SourceID != importSourceID {
		t.Errorf("source = %q, want %q", res.SourceID, importSourceID)
	}
	if res.BatchKey != "inventory-1" {
		t.Errorf("batch = %q, want the document's own key", res.BatchKey)
	}
	if len(res.Facts) != 1 {
		t.Fatalf("reported %d facts, want the one the batch carried", len(res.Facts))
	}
	fact := res.Facts[0]
	if fact.ID == "" {
		t.Error("the report names no fact identifier, so nothing it imported can be inspected")
	}
	if fact.SubjectID != inv.service {
		t.Errorf("subject = %q, want %q", fact.SubjectID, inv.service)
	}
	if fact.Predicate != string(reality.PredicateServicePlacement) {
		t.Errorf("predicate = %q, want %q", fact.Predicate, reality.PredicateServicePlacement)
	}
	if fact.Value != inv.machine {
		t.Errorf("value = %q, want the machine the placement names", fact.Value)
	}
	// §4.8: the source authors on its own authority. The command must not be
	// able to report the operator as the author of an imported fact, and the
	// document does not get to name an authority at all.
	if !strings.Contains(fact.Authority, string(reality.AuthorityTrustedSource)) ||
		!strings.Contains(fact.Authority, importSourceID) {
		t.Errorf("authority = %q, want the trusted source's own", fact.Authority)
	}
	if fact.Status != string(reality.FactActive) {
		t.Errorf("status = %q, want %q: a registered source's import is authorized",
			fact.Status, reality.FactActive)
	}

	// Durable, and the same fact: the identifier the report handed back is the
	// one an operator can now inspect.
	entityOut, _ := f.ok("reality", "entity", inv.service, "--json")
	entity := decodeJSON[entityResult](t, entityOut)
	if len(entity.Facts) != 1 || entity.Facts[0].ID != fact.ID {
		t.Fatalf("the ledger holds %+v, want the fact the import reported", entity.Facts)
	}

	// The same document from a file, under a second key. The human-readable
	// view has to list what landed too: an operator reading a terminal is the
	// one who most needs to see which facts an import authorized.
	path := filepath.Join(t.TempDir(), "batch.json")
	document := batch("inventory-2", placementFact(inv.service, inv.machine))
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	human, stderr := f.ok("reality", "import", "--source", importSourceID, "--from-json", path)
	if stderr != "" {
		t.Errorf("a successful import wrote diagnostics: %q", stderr)
	}
	for _, want := range []string{
		importSourceID, "inventory-2", "1 fact",
		inv.service, string(reality.PredicateServicePlacement), inv.machine,
		string(reality.AuthorityTrustedSource),
	} {
		if !strings.Contains(human, want) {
			t.Errorf("the terminal report omits %q:\n%s", want, human)
		}
	}
	assertNoRawControls(t, "reality import", human, stderr)
}

// TestRealityImportOfABadDocumentImportsNothing is the property that makes the
// command safe to hand a generated document: every rejection is total.
//
// Partial application is the failure that matters here. A batch that had its
// first fact applied and its second refused would leave the operator diffing
// the ledger against the source to learn what landed, so the refusal covers the
// whole document. Atomicity is reality.ImportFacts's own guarantee — one
// transaction per batch, rolled back with the import row — and this asserts the
// command relies on it rather than applying facts one at a time itself.
func TestRealityImportOfABadDocumentImportsNothing(t *testing.T) {
	f := newFixture(t)
	inv := f.seedInventory()
	good := placementFact(inv.service, inv.machine)

	// A fact about the machine: the source declared the service kind and
	// nothing else, so this is outside its scope. It is placed *second*, after
	// a fact that would otherwise be accepted, which is what makes the case
	// about atomicity rather than about validation.
	outside := placementFact(inv.machine, inv.machine)

	cases := []struct {
		name     string
		document string
	}{
		{"truncated json", `{"batch_key": "bad-1", "facts": [`},
		{"trailing second document", batch("bad-2", good) + ` {"batch_key": "bad-2b", "facts": []}`},
		{"misspelled required field", strings.Replace(batch("bad-3", good),
			`"valid_from"`, `"vaild_from"`, 1)},
		// A misspelled *optional* field is the case that needs the decoder
		// to reject unknown keys: with the key silently dropped this
		// document imports cleanly, and the fact it authorizes is
		// open-ended rather than the bounded one the source wrote.
		{"misspelled optional field", strings.Replace(batch("bad-3b", good),
			`"observed_at"`, `"vaild_until": "2026-09-01T00:00:00Z", "observed_at"`, 1)},
		{"no facts at all", `{"batch_key": "bad-4", "facts": []}`},
		{"a good fact beside one outside the declared scope", batch("bad-5", good, outside)},
		{"a value outside the predicate's type", strings.Replace(batch("bad-6", good),
			`{"kind": "entity", "object_id": "`+inv.machine+`"}`, `{"kind": "text", "text": "somewhere"}`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := f.runStdin(tc.document,
				"reality", "import", "--source", importSourceID, "--from-json", "-", "--json")
			if code != exitFailure {
				t.Fatalf("exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
					code, exitFailure, stdout, stderr)
			}
			// A --json invocation that failed must not have written a
			// document: a caller parsing stdout would read a partial
			// success out of it.
			if stdout != "" {
				t.Errorf("a refused import wrote to stdout: %q", stdout)
			}
			if stderr == "" {
				t.Error("a refused import explained nothing on stderr")
			}
			assertNoRawControls(t, "reality import "+tc.name, stdout, stderr)
		})
	}

	// Nothing from any of those documents reached the ledger, the accepted-
	// looking first fact of the mixed batch included.
	entityOut, _ := f.ok("reality", "entity", inv.service, "--json")
	entity := decodeJSON[entityResult](t, entityOut)
	if len(entity.Facts) != 0 {
		t.Errorf("%d facts landed from documents that were all refused: %+v",
			len(entity.Facts), entity.Facts)
	}

	// An unregistered source has no declared scope, so it may author nothing.
	// The refusal has to name the source, because a typo in --source is the
	// likely cause and an operator cannot see it in a generated document.
	_, stderr, code := f.runStdin(batch("bad-7", good),
		"reality", "import", "--source", "never-registered", "--from-json", "-")
	if code != exitFailure {
		t.Fatalf("importing as an unregistered source exited %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "never-registered") {
		t.Errorf("the refusal does not name the source it could not find: %q", stderr)
	}

	// The zero above is a rollback and not a document that was never
	// importable: the very fact the mixed batch led with lands when it is not
	// standing beside one outside the source's scope.
	if _, stderr, code := f.runStdin(batch("good-1", good),
		"reality", "import", "--source", importSourceID, "--from-json", "-"); code != exitOK {
		t.Fatalf("the rolled-back fact does not import on its own either, so the "+
			"atomicity assertion above proves nothing: exit %d\nstderr:\n%s", code, stderr)
	}
	entityOut, _ = f.ok("reality", "entity", inv.service, "--json")
	if got := len(decodeJSON[entityResult](t, entityOut).Facts); got != 1 {
		t.Errorf("the ledger holds %d facts after the one accepted import, want 1", got)
	}
}

// TestRealityImportSurfacesTheCredentialRefusal covers §4.8's flat prohibition
// reaching the operator.
//
// The store refuses credential material rather than redacting it, and the
// command's job is to make that refusal legible: an import that failed for this
// reason and one that failed for a scope violation are different problems, and
// an operator who cannot tell them apart will retry the wrong fix. The refusal
// must also not echo the credential, which would move the exposure into
// whatever collects Babel's stderr.
func TestRealityImportSurfacesTheCredentialRefusal(t *testing.T) {
	f := newFixture(t)
	// local-path is the text-valued predicate, which is what lets a batch
	// carry credential-shaped prose at all: the enum predicates have closed
	// vocabularies and would refuse the value before the detector saw it.
	inv := f.seedInventory(reality.PredicateLocalPath)

	localPath := func(note string) string {
		return fmt.Sprintf(`{"subject_id": %q,
			"predicate": "local-path",
			"value": {"kind": "text", "text": "/synthetic/checkout/service"},
			"valid_from": "2026-08-01T00:00:00Z",
			"observed_at": "2026-08-01T00:00:00Z",
			"confidence": "high",
			"sensitivity": "routine",
			"provenance": {"path": "synthetic/inventory.jsonl", "line": 1, "byte_offset": 0, "digest": %q},
			"note": %q}`, inv.service, importDigest, note)
	}

	// The clean version of the same fact imports, so the case that follows is
	// refused for the credential and not for anything else about the document.
	if _, _, code := f.runStdin(batch("clean-1", localPath("read from the inventory")),
		"reality", "import", "--source", importSourceID, "--from-json", "-"); code != exitOK {
		t.Fatalf("the same fact without credential material exited %d, want 0", code)
	}

	stdout, stderr, code := f.runStdin(batch("credential-1", localPath("rotate "+probeImportToken)),
		"reality", "import", "--source", importSourceID, "--from-json", "-", "--json")
	if code != exitFailure {
		t.Fatalf("importing credential material exited %d, want %d\nstdout:\n%s",
			code, exitFailure, stdout)
	}
	if stdout != "" {
		t.Errorf("a refused import wrote to stdout: %q", stdout)
	}
	// Legible: the operator has to learn that the batch was refused for
	// credential material and which field carried it, not merely that an
	// import failed.
	for _, want := range []string{"credential", "fact note"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, probeImportToken) {
		t.Error("the refusal echoed the credential into stderr")
	}
	assertNoRawControls(t, "reality import credential refusal", stdout, stderr)

	// The refused batch changed nothing: the clean fact is still the only one.
	entityOut, _ := f.ok("reality", "entity", inv.service, "--predicate", "local-path", "--json")
	entity := decodeJSON[entityResult](t, entityOut)
	if len(entity.Facts) != 1 {
		t.Errorf("the ledger holds %d local-path facts, want only the clean one", len(entity.Facts))
	}
	for _, fact := range entity.Facts {
		if strings.Contains(fact.Note, probeImportToken) {
			t.Error("a credential landed in the ledger")
		}
	}
}

// TestRealityImportRejectsAnIncompleteInvocation keeps the two required flags
// required. Defaulting either one would be a guess about which source
// authorized a batch or which document it came from, and both are exactly the
// things an import must not guess.
func TestRealityImportRejectsAnIncompleteInvocation(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"reality", "import"},
		{"reality", "import", "--source", importSourceID},
		{"reality", "import", "--from-json", "-"},
		{"reality", "import", "--source", importSourceID, "--from-json", "-", "STRAY"},
	} {
		stdout, stderr := f.mustExit(exitUsage, args...)
		if stdout != "" {
			t.Errorf("babel %s wrote to stdout: %q", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stderr, "Usage: babel reality import") {
			t.Errorf("babel %s did not print its usage: %q", strings.Join(args, " "), stderr)
		}
	}
}
