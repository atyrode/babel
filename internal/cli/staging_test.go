package cli

import (
	"bytes"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/pgtest"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"

	_ "modernc.org/sqlite"
)

// This file is issue #137's regression suite: the wiring that attaches the
// Phase B publication hook to the stores a command writes through.
//
// The packages underneath were already covered and already correct - every
// record store stages inside its writer's own transaction, and internal/sync
// publishes what is staged - and the bug was that no production caller
// attached any of it, so a fully configured shared-mode host wrote durable
// records that nothing ever owed the fleet. What is pinned here is therefore
// the attachment and not the staging: that a shared deployment stages what it
// writes and publishes it on `babel sync`, that a local one is byte-identical
// to the deployment that existed before publication did, and that no store
// opened in internal/cli can quietly go back to publishing nothing.

const (
	stagingDeploymentID = "fixturedeployment"
	stagingInstanceID   = "fixtureinstance"
	stagingKeyID        = "fixturekey"
)

// stagingDeployment is one shared-mode fixture: a throwaway TLS PostgreSQL, a
// migrated and registered catalog, a local object root derived from the
// configured repository, and this machine's storage.json installed through the
// shipped ceremony.
//
// It installs no payload keys, and that omission is load-bearing. A deployment
// whose keys have not arrived can seal nothing and must still stage
// everything, because SPEC.md §9 requires a record owed to the fleet to be
// visibly pending rather than silently local. The key is generated later, by
// the test that needs a publication to succeed.
type stagingDeployment struct {
	f   *fixture
	cfg config.Config
}

func newStagingDeployment(t *testing.T) *stagingDeployment {
	t.Helper()
	// TLS because the commands build their DSN from storage.json, and that
	// document cannot express an unencrypted connection: shared mode always
	// encrypts.
	pgtest.SkipOrFail(t)
	cluster, err := pgtest.Start(pgtest.Options{TLS: true})
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	t.Cleanup(cluster.Stop)

	f := newFixture(t)
	cfg := config.Config{
		ConfigSchema: 2,
		Mode:         config.ModeShared,
		// The repository is never read here - no snapshot is taken - but it is
		// what internal/objectstore derives this deployment's Phase B object
		// root from, so it has to be an absolute path this process may write
		// beside.
		Repository:   f.repoDir,
		PasswordFile: f.passwordFile,
		HostID:       testHostID,
		DeploymentID: stagingDeploymentID,
		InstanceID:   stagingInstanceID,
		Catalog: &config.Catalog{
			Host:     cluster.Host,
			Port:     cluster.Port,
			Database: "postgres",
			User:     cluster.User,
			Password: catalogPassword,
			TLSMode:  config.TLSRequire,
		},
	}

	// The catalog is migrated and this instance registered before the document
	// is installed, which is the state a real host reaches through `babel
	// storage migrate` and its first `archive push`. Neither is what this
	// suite is about, so neither is driven through the CLI: what matters is
	// that the rows a published run's foreign keys need are there.
	ctx := t.Context()
	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(2))
	if err != nil {
		t.Fatalf("open the fixture catalog: %v", err)
	}
	if _, err := sharedcatalog.Migrate(ctx, db); err != nil {
		db.Close()
		t.Fatalf("migrate the fixture catalog: %v", err)
	}
	if err := sharedcatalog.Register(ctx, db, stagingDeploymentID, testHostID, stagingInstanceID,
		sharedcatalog.HostIdentity{DisplayName: testHostID, OS: "linux", Arch: "amd64"}); err != nil {
		db.Close()
		t.Fatalf("register the fixture instance: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("release the fixture catalog handle: %v", err)
	}

	f.ok("storage", "configure", "--from-json", writeDocument(t, cfg))
	return &stagingDeployment{f: f, cfg: cfg}
}

// journalSnapshot is what this machine's publication journal holds about one
// record at one moment.
type journalSnapshot struct {
	// state is the record's local publication state, and the empty string when
	// the journal has never seen the id - which is what "the record was never
	// staged" is (runbook §9.1).
	state string
	// pending counts every staged record this machine still owes, by kind.
	pending map[sharedcatalog.RecordKind]int
	// undeclared counts staged records whose producing run has not ended.
	undeclared int
}

// journal reads the journal and releases it again, rather than holding it for
// the test's life: the commands under test open the same durable file, and a
// handle kept open across them would be measuring the fixture's locking rather
// than the wiring.
func (d *stagingDeployment) journal(t *testing.T, id string) journalSnapshot {
	t.Helper()
	journal, err := babelsync.OpenJournal(d.f.dataDir)
	if err != nil {
		t.Fatalf("open the publication journal: %v", err)
	}
	defer journal.Close()

	ctx := t.Context()
	var snap journalSnapshot
	if snap.state, err = journal.SyncState(ctx, id); err != nil {
		t.Fatalf("read the journal state of %s: %v", id, err)
	}
	if snap.pending, err = journal.PendingByKind(ctx); err != nil {
		t.Fatalf("read the pending records: %v", err)
	}
	if snap.undeclared, err = journal.UndeclaredRecords(ctx); err != nil {
		t.Fatalf("read the undeclared records: %v", err)
	}
	return snap
}

// catalog opens the assertion handle. It addresses the catalog through the
// installed document's own connection parameters, so an assertion cannot pass
// against a server the commands are not using.
func (d *stagingDeployment) catalog(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sharedcatalog.Open(t.Context(), d.cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(1))
	if err != nil {
		t.Fatalf("open the shared catalog: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSharedModeStagesATellAndSyncPublishesIt is issue #137's acceptance, end
// to end and through the shipped commands: on a shared-mode deployment `babel
// tell` then `babel sync` publishes exactly one record, and `babel fleet
// records` shows it committed.
//
// Every step is one of the things that were dark. The tell stages, which is the
// hook being attached at all. It stages with no payload key document present,
// which is the §9 requirement that a deployment one command short of
// publishing still records what it owes. The sync publishes, which is the
// journal being the handoff between a writer that never dials PostgreSQL and
// the command whose whole job is to. And the fleet listing reports
// `committed`, which is the record having reached the shared catalog rather
// than merely having been counted locally.
func TestSharedModeStagesATellAndSyncPublishesIt(t *testing.T) {
	d := newStagingDeployment(t)

	stdout, _ := d.f.ok("tell", "the same three files get rewritten every session",
		"--operator", "alex", "--json")
	told := decode[tellResult](t, stdout)
	if told.ID == "" {
		t.Fatalf("tell reported no record: %+v", told)
	}

	staged := d.journal(t, told.ID)
	if staged.state != sharedcatalog.SyncPending {
		t.Fatalf("the told complaint's journal state is %q, want %q: a shared-mode write that stages "+
			"nothing is owed to the fleet by nobody", staged.state, sharedcatalog.SyncPending)
	}
	if want := map[sharedcatalog.RecordKind]int{sharedcatalog.KindComplaint: 1}; !equalKindCounts(staged.pending, want) {
		t.Fatalf("the journal holds %v, want %v", staged.pending, want)
	}
	// A complaint is its own closure of one, declared in the transaction that
	// staged it, so nothing is left waiting for a run to end first.
	if staged.undeclared != 0 {
		t.Fatalf("the journal holds %d undeclared records, want 0", staged.undeclared)
	}

	// The reporting half of the same fact. A listing's SYNC column resolves a
	// local record against this journal, and it has to say pending-sync where
	// it used to say local: "local" promises that nothing will carry the
	// record, and something now will (runbook §9.1).
	var out, errOut bytes.Buffer
	column, err := (&app{stdout: &out, stderr: &errOut}).syncColumn(t.Context(), nil, []string{told.ID})
	if err != nil {
		t.Fatalf("resolve the sync column: %v", err)
	}
	if column[told.ID] != sharedcatalog.SyncPending {
		t.Fatalf("a listing would render %s as %q, want %q", told.ID, column[told.ID], sharedcatalog.SyncPending)
	}

	// The keys arrive after the record, which is the order the first real
	// deployment hit (#112's rollout) and the order that proves staging never
	// waited for them.
	d.f.ok("sync", "--generate-key", stagingKeyID, "--json")

	stdout, _ = d.f.ok("sync", "--json")
	res := decode[syncResult](t, stdout)
	switch {
	case !res.Configured:
		t.Fatalf("sync reported this deployment unconfigured: %+v", res)
	case len(res.Failures) != 0:
		t.Fatalf("sync reported failures: %+v", res.Failures)
	case res.RunsCommitted != 1:
		t.Fatalf("sync committed %d runs, want exactly 1: %+v", res.RunsCommitted, res)
	case syncTotal(res.Committed) != 1:
		t.Fatalf("sync committed %d records, want exactly 1: %+v", syncTotal(res.Committed), res)
	case syncTotal(res.Pending) != 0 || res.RunsPending != 0:
		t.Fatalf("sync left %d records in %d runs pending, want none: %+v",
			syncTotal(res.Pending), res.RunsPending, res)
	}

	// The catalog is the durable half of that claim, read on this test's own
	// connection rather than taken from the command's own report.
	states, err := sharedcatalog.RecordSyncStates(t.Context(), d.catalog(t), []string{told.ID})
	if err != nil {
		t.Fatalf("read the remote sync state: %v", err)
	}
	if states[told.ID] != sharedcatalog.SyncCommitted {
		t.Fatalf("the shared catalog reports %s as %q, want %q",
			told.ID, states[told.ID], sharedcatalog.SyncCommitted)
	}

	// And this is the surface the operator was reading when they found the bug.
	stdout, _ = d.f.ok("fleet", "records", "--json")
	listing := decode[fleetRecordsResult](t, stdout)
	if len(listing.Records) != 1 {
		t.Fatalf("the fleet listing holds %d records, want exactly 1: %+v",
			len(listing.Records), listing.Records)
	}
	row := listing.Records[0]
	if row.RecordID != told.ID || row.Sync != sharedcatalog.SyncCommitted || !row.ThisHost {
		t.Fatalf("the fleet listing reports %+v, want %s committed and produced by this host", row, told.ID)
	}
}

// TestSharedModeStagesAWriteThroughTheAppLayer pins the value every command's
// state carries, which is also the value an exploration is handed.
//
// internal/explore's own suite proves what a run does with a hook - the
// closure it declares, the receipt that ends it, the local-only run that
// declares nothing - and none of that is reachable from here, because an
// exploration needs a worker process this package's fixtures do not build. So
// what is checked is the thing that was actually missing: that the state a
// command opens carries a hook at all in shared mode, that a write through it
// stages, and - in the source guard below - that the config `babel explore`
// builds is built from that same state.
func TestSharedModeStagesAWriteThroughTheAppLayer(t *testing.T) {
	d := newStagingDeployment(t)

	state, err := openAnalysisState()
	if err != nil {
		t.Fatalf("open the analysis state: %v", err)
	}
	defer state.Close()
	if state.sync == nil {
		t.Fatal("a shared-mode analysis state carries no publication hook, so every record it mints " +
			"- and every exploration it configures - is owed to the fleet by nobody")
	}

	told, err := state.complaints.Tell(t.Context(), complaint.TellInput{
		Text: "the deploy script re-reads a stale manifest",
		By:   "alex",
		Host: testHostID,
	})
	if err != nil {
		t.Fatalf("tell through the analysis state: %v", err)
	}
	// The state's own handles are still open, so this reads the durable file
	// beside them, which is what every reporting surface does.
	if state := d.journal(t, told.ID).state; state != sharedcatalog.SyncPending {
		t.Fatalf("a write through the analysis state left %s at %q, want %q",
			told.ID, state, sharedcatalog.SyncPending)
	}

	// The ledger a Reality command opens on its own resolves the same hook, so
	// it opens without error and creates the journal tables it stages into.
	ledger, err := openReality()
	if err != nil {
		t.Fatalf("open the reality ledger: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("release the reality ledger: %v", err)
	}
}

// TestLocalModeStagesNothing is the compatibility half, and it is why the hook
// is resolved from the stored configuration rather than always attached.
//
// A machine with no storage.json is a supported deployment and not a degraded
// one, so `babel tell` there must behave exactly as it did before publication
// existed: the same durable record, and no journal at all rather than an empty
// one. The absence of the tables is what makes that checkable, because a store
// opened with a hook creates them at Open whether or not it then stages.
func TestLocalModeStagesNothing(t *testing.T) {
	f := newFixture(t)

	stdout, _ := f.ok("tell", "the retry budget is spent before the first backoff",
		"--operator", "alex", "--json")
	told := decode[tellResult](t, stdout)
	if told.ID == "" {
		t.Fatalf("tell reported no record: %+v", told)
	}

	db, err := sql.Open("sqlite", filepath.Join(f.dataDir, babelsync.DatabaseName))
	if err != nil {
		t.Fatalf("open the durable database: %v", err)
	}
	defer db.Close()
	for _, table := range []string{"sync_record", "sync_run", "sync_payload"} {
		var name string
		switch err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); {
		case err == nil:
			t.Errorf("a local-mode tell created the %s table, so local mode is no longer identical "+
				"to the deployment that had no publication at all", table)
		case err != sql.ErrNoRows:
			t.Fatalf("read the durable schema: %v", err)
		}
	}
}

// TestEveryDurableStoreInThisPackageOpensWithTheHook is the drift guard the
// issue asked for, and it reads this package's own source because that is the
// only place the property lives: a store opened without WithSync compiles,
// runs, writes durable records and publishes nothing, on every deployment,
// silently. That is exactly what happened.
//
// The rule it enforces has no exception for a call site that only reads today.
// A hook is attached for the life of the handle, so "this caller performs no
// write" is a judgement the next caller inherits without knowing it was made.
func TestEveryDurableStoreInThisPackageOpensWithTheHook(t *testing.T) {
	// The record stores whose WithSync option is what makes a durable write
	// publishable, keyed by the import name this package spells them with.
	stores := []string{"frontier", "runstore", "disposition", "complaint", "reference", "reality"}

	opened := 0
	for _, file := range packageFiles(t) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calls := packageCalls(fn.Body)
			for _, pkg := range stores {
				if !calls[pkg+".Open"] {
					continue
				}
				opened++
				if !calls[pkg+".WithSync"] {
					t.Errorf("%s opens a %s store without %s.WithSync, so every record written "+
						"through that handle is durable and owed to the fleet by nobody",
						fn.Name.Name, pkg, pkg)
				}
			}
			// An exploration's records reach the stores above, but the closure
			// they publish under is declared through the config's own hook: a
			// config built without one leaves a run's whole output staged
			// under a closure nothing ever declares.
			for _, lit := range configLiterals(fn.Body, "explore", "Config") {
				if !hasField(lit, "Sync") {
					t.Errorf("%s builds an explore.Config with no Sync field, so an exploration's "+
						"receipt declares no closure and its records never publish", fn.Name.Name)
				}
			}
		}
	}
	if opened == 0 {
		t.Fatal("no durable store is opened anywhere in this package, so this guard would " +
			"vacuously pass; the openers have moved and it has to follow them")
	}
}

// packageFiles parses every non-test source file in this package.
func packageFiles(t *testing.T) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("this package has no source files, so every source-reading guard would vacuously pass")
	}
	return files
}

// packageCalls collects every "pkg.Func" call in body, at any depth, so a
// store opened inside a branch and one whose options travel in a slice are
// found on the same terms as one opened in a single expression.
func packageCalls(body *ast.BlockStmt) map[string]bool {
	found := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok {
				found[pkg.Name+"."+sel.Sel.Name] = true
			}
		}
		return true
	})
	return found
}

// configLiterals returns every "pkg.Name{...}" composite literal in body.
func configLiterals(body *ast.BlockStmt, pkg, name string) []*ast.CompositeLit {
	var found []*ast.CompositeLit
	ast.Inspect(body, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
			found = append(found, lit)
		}
		return true
	})
	return found
}

// hasField reports whether a keyed composite literal sets one field.
func hasField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
			return true
		}
	}
	return false
}

// equalKindCounts compares two per-kind counts, which maps.Equal cannot be
// asked for here without naming both type parameters at every call site.
func equalKindCounts(got, want map[sharedcatalog.RecordKind]int) bool {
	if len(got) != len(want) {
		return false
	}
	for kind, n := range want {
		if got[kind] != n {
			return false
		}
	}
	return true
}
