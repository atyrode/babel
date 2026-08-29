package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// Two §14 pre-deployment gates that need no provider: idempotent concurrent
// writers, and complete catalog rebuild from the repository snapshot list plus
// source rescans.
//
// Concurrency here is genuine — two operating-system processes, overlapping in
// time — rather than two sequential calls. It has to be: Babel resolves HOME and
// the XDG roots from the environment, which is process-global, so two instances
// cannot be driven concurrently in one test binary. That constraint is a
// benefit, since the thing under test is the shipped executable.

// babelBinary builds the command under test once per package run.
var (
	babelBinaryOnce sync.Once
	babelBinaryPath string
	babelBinaryErr  error
)

func babelBinary(t *testing.T) string {
	t.Helper()
	babelBinaryOnce.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			babelBinaryErr = err
			return
		}
		// Not t.TempDir(): the binary outlives the test that built it.
		dir, err := os.MkdirTemp("", "babel-bin-*")
		if err != nil {
			babelBinaryErr = err
			return
		}
		path := filepath.Join(dir, "babel")
		cmd := exec.Command(goTool, "build", "-o", path, "./cmd/babel")
		cmd.Dir = repoRoot(t)
		// The environment this binary started with, not the current one. By the
		// time a test needs the executable it has activated an instance, so HOME
		// points at a synthetic tree inside t.TempDir() - and `go build` would
		// write its module cache there, slowly and read-only, leaving the test's
		// own cleanup unable to remove it.
		cmd.Env = baseEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			babelBinaryErr = err
			t.Logf("go build: %s", out)
			return
		}
		babelBinaryPath = path
	})
	if babelBinaryErr != nil {
		t.Skipf("cannot build the babel binary: %v", babelBinaryErr)
	}
	return babelBinaryPath
}

// repoRoot finds the module root from the test's working directory, which is the
// package directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// processEnv is this instance's environment for an out-of-process run: the same
// roots activate() sets, so an exec'd command sees exactly what an in-process
// one would.
func (i *instance) processEnv(t *testing.T) []string {
	t.Helper()
	out := []string{
		"HOME=" + i.home,
		"XDG_CONFIG_HOME=" + i.configHome,
		"XDG_DATA_HOME=" + i.dataHome,
		"XDG_CACHE_HOME=" + i.cacheHome,
		"CODEX_HOME=" + i.codexHome,
		"PATH=" + filepath.Dir(resticBinary(t)) + string(os.PathListSeparator) + basePATH,
	}
	// Preserved because restic and the Go toolchain need them; deliberately not
	// any BABEL_* variable, so identity comes from storage.json.
	for _, k := range []string{"TMPDIR", "LANG", "TERM"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// pushOutcome is one process's result.
type pushOutcome struct {
	label   string
	code    int
	stdout  string
	stderr  string
	catalog string
}

// pushConcurrently starts one `archive push` per instance as close together as
// the runtime allows and waits for all of them.
func pushConcurrently(t *testing.T, instances ...*instance) []pushOutcome {
	t.Helper()
	bin := babelBinary(t)

	type started struct {
		label string
		cmd   *exec.Cmd
		out   *strings.Builder
		errb  *strings.Builder
	}
	// A barrier so the processes overlap rather than merely being launched in a
	// loop: every command is created first, then all are started back to back.
	running := make([]started, 0, len(instances))
	for _, i := range instances {
		cmd := exec.Command(bin, "archive", "push", "--json")
		cmd.Env = i.processEnv(t)
		var out, errb strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &errb
		running = append(running, started{label: i.label, cmd: cmd, out: &out, errb: &errb})
	}
	for _, r := range running {
		if err := r.cmd.Start(); err != nil {
			t.Fatalf("%s: start push: %v", r.label, err)
		}
	}

	outcomes := make([]pushOutcome, len(running))
	var wg sync.WaitGroup
	for n, r := range running {
		wg.Add(1)
		go func(n int, r started) {
			defer wg.Done()
			err := r.cmd.Wait()
			o := pushOutcome{label: r.label, stdout: r.out.String(), stderr: r.errb.String()}
			if err != nil {
				o.code = r.cmd.ProcessState.ExitCode()
			}
			outcomes[n] = o
		}(n, r)
	}
	wg.Wait()

	for n := range outcomes {
		o := &outcomes[n]
		if o.code != exitOK {
			t.Fatalf("%s: concurrent push exited %d\nstdout:\n%s\nstderr:\n%s",
				o.label, o.code, o.stdout, o.stderr)
		}
		res := decode[pushResult](t, o.stdout)
		o.catalog = res.Catalog
	}
	return outcomes
}

// TestConcurrentPushesForOneHostSerializeSafely covers the §14 concurrent-writer
// gate for the contended case: two instances configured for the same host,
// pushing at the same time.
//
// The property is not that one is rejected. A lease serializes writers rather
// than refusing them, so both committing is legal if the first released before
// the second asked. What must hold is that no push fails, every outcome is a
// state the catalog can actually be in, and the unique constraint on
// (host, publication_order) is never violated — a violation would surface as a
// failed push, which is exactly why this asserts success first.
func TestConcurrentPushesForOneHostSerializeSafely(t *testing.T) {
	dep := newDeployment(t)

	// Same host id, different instance ids: one machine's identity claimed by
	// two processes, which is what an overlapping timer run looks like.
	first := dep.newInstance(t, "same-host-1", hostA, instanceA)
	second := dep.newInstance(t, "same-host-2", hostA, instanceB)

	first.writeOMPSession(t, ompSpec{
		project: ompProjectB, stem: ompStemB,
		id: "00000000-0000-4000-8000-0000000000b1", title: titleB, workspace: workspaceB,
	})
	second.writeOMPSession(t, ompSpec{
		project: "-synthetic-e2e-concurrent", stem: "2026-03-04T05-06-07-891Z_cccccccc-0000-4000-8000-0000000000c1",
		id: "00000000-0000-4000-8000-0000000000c1", title: "Synthetic e2e concurrent", workspace: "/synthetic/workspace/concurrent",
	})

	first.configure(t)
	first.ok(t, "storage", "migrate")
	first.ok(t, first.with("archive", "init")...)
	second.configure(t)

	outcomes := pushConcurrently(t, first, second)

	committed := 0
	for _, o := range outcomes {
		switch o.catalog {
		case "committed":
			committed++
		case "uncatalogued":
			// The loser of the race. It must say why rather than looking like an
			// unexplained non-publication.
			if !strings.Contains(o.stderr, "another instance is publishing for this host") &&
				!strings.Contains(o.stderr, "could not") {
				t.Errorf("%s deferred without explaining why:\n%s", o.label, o.stderr)
			}
		default:
			t.Errorf("%s reported catalog state %q, which is neither committed nor uncatalogued",
				o.label, o.catalog)
		}
	}
	if committed == 0 {
		t.Fatalf("neither concurrent push published: %+v", outcomes)
	}

	// Both snapshots are in the repository regardless of who won the lease: the
	// archive is written before the catalog, and contention never costs a backup.
	status := instJSON[sharedStatusResult](t, first, first.with("archive", "status", "--json")...)
	if status.Snapshots != 2 {
		t.Fatalf("the repository holds %d snapshots, want both: %+v", status.Snapshots, status)
	}

	// One sequential push closes whatever the race left open, which is the
	// self-healing claim rather than an assumption about who won.
	instJSON[pushResult](t, first, first.with("archive", "push", "--json")...)
	settled := instJSON[sharedStatusResult](t, first, first.with("archive", "status", "--json")...)
	if settled.Catalog.Uncatalogued == nil || *settled.Catalog.Uncatalogued != 0 {
		t.Fatalf("a snapshot stayed uncatalogued after a settling push: %+v", settled.Catalog)
	}
}

// TestConcurrentPushesForDifferentHostsBothPublish is the uncontended half of
// the same gate. Two hosts share one repository and one catalog and must not
// interfere: separate leases, separate publication-order sequences, and restic
// itself tolerating two simultaneous backups into one repository.
func TestConcurrentPushesForDifferentHostsBothPublish(t *testing.T) {
	dep := newDeployment(t)

	a := dep.newInstance(t, "instance-a", hostA, instanceA)
	b := dep.newInstance(t, "instance-b", hostB, instanceB)

	a.writeOMPSession(t, ompSpec{
		project: ompProjectB, stem: ompStemB,
		id: "00000000-0000-4000-8000-0000000000b1", title: titleB, workspace: workspaceB,
	})
	b.writeOMPSession(t, ompSpec{
		project: "-synthetic-e2e-concurrent", stem: "2026-03-04T05-06-07-891Z_cccccccc-0000-4000-8000-0000000000c1",
		id: "00000000-0000-4000-8000-0000000000c1", title: "Synthetic e2e concurrent", workspace: "/synthetic/workspace/concurrent",
	})

	a.configure(t)
	a.ok(t, "storage", "migrate")
	a.ok(t, a.with("archive", "init")...)
	b.configure(t)

	for _, o := range pushConcurrently(t, a, b) {
		if o.catalog != "committed" {
			t.Errorf("%s reported %q; an uncontended host's lease was not available\nstderr:\n%s",
				o.label, o.catalog, o.stderr)
		}
	}

	status := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if len(status.Catalog.Hosts) != 2 {
		t.Fatalf("the catalog holds %d hosts after concurrent pushes: %+v",
			len(status.Catalog.Hosts), status.Catalog.Hosts)
	}
	for _, h := range status.Catalog.Hosts {
		if h.Snapshots != 1 || h.Sessions != 1 || h.Pending != 0 || h.NewestOrder != 1 {
			t.Errorf("host %s did not publish exactly its own snapshot: %+v", h.Host, h)
		}
	}
}

// TestCatalogRebuildsFromTheRepository covers the §14 gate for complete catalog
// rebuild from the repository snapshot list plus source rescans.
//
// The catalog is rebuildable convenience state, never archive truth
// (decision 43). This destroys all of it — every table, for every host — and
// rebuilds from the repository plus a rescan of live sources, which is the only
// recovery path that exists: no catalog backup is assumed.
//
// What comes back is deliberately not everything. Snapshot visibility, ordering,
// and restic's counts return for every historical snapshot; current session
// identity returns from the rescan. What does not return is which sessions each
// *historical* snapshot held, because only its owning host could have written
// that at push time and it is not derivable from the listing — so those rows
// come back `catalog-pending` and stay there (SPEC.md §9).
func TestCatalogRebuildsFromTheRepository(t *testing.T) {
	dep := newDeployment(t)
	a := dep.newInstance(t, "instance-a", hostA, instanceA)
	b := dep.newInstance(t, "instance-b", hostB, instanceB)

	a.writeOMPSession(t, ompSpec{
		project: ompProjectB, stem: ompStemB,
		id: "00000000-0000-4000-8000-0000000000b1", title: titleB, workspace: workspaceB,
	})
	b.writeOMPSession(t, ompSpec{
		project: "-synthetic-e2e-rebuild", stem: "2026-04-05T06-07-08-912Z_dddddddd-0000-4000-8000-0000000000d1",
		id: "00000000-0000-4000-8000-0000000000d1", title: "Synthetic e2e rebuild", workspace: "/synthetic/workspace/rebuild",
	})

	a.configure(t)
	a.ok(t, "storage", "migrate")
	a.ok(t, a.with("archive", "init")...)
	b.configure(t)

	// Two pushes per host, so history exists to rebuild rather than one row.
	for range 2 {
		instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
		instJSON[pushResult](t, b, b.with("archive", "push", "--json")...)
	}
	before := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if before.Snapshots != 4 {
		t.Fatalf("expected four snapshots before the loss, got %d", before.Snapshots)
	}

	// Total loss of the catalog: not a truncation, the schema itself.
	db := dep.open(t)
	if _, err := db.ExecContext(context.Background(), "DROP SCHEMA babel CASCADE"); err != nil {
		t.Fatalf("destroy the catalog: %v", err)
	}

	// Recovery is the documented path and nothing more: migrate, then push.
	a.ok(t, "storage", "migrate")
	rebuiltA := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	rebuiltB := instJSON[pushResult](t, b, b.with("archive", "push", "--json")...)
	if rebuiltA.Catalog != "committed" || rebuiltB.Catalog != "committed" {
		t.Fatalf("the rebuilding pushes did not publish: %q / %q", rebuiltA.Catalog, rebuiltB.Catalog)
	}

	after := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if after.Catalog.Uncatalogued == nil || *after.Catalog.Uncatalogued != 0 {
		t.Fatalf("snapshots remain uncatalogued after the rebuild: %+v", after.Catalog)
	}
	if len(after.Catalog.Hosts) != 2 {
		t.Fatalf("the rebuilt catalog holds %d hosts, want 2: %+v",
			len(after.Catalog.Hosts), after.Catalog.Hosts)
	}
	// Six snapshots now exist: the four before the loss plus one rebuilding push
	// per host. Every one has a row, and each host's newest is its own.
	for _, h := range after.Catalog.Hosts {
		if h.Snapshots != 3 {
			t.Errorf("host %s has %d catalog rows, want all three of its snapshots: %+v",
				h.Host, h.Snapshots, h)
		}
		// The two adopted historical snapshots carry no session detail, and the
		// push that rebuilt is committed.
		if h.Pending != 2 {
			t.Errorf("host %s reports %d catalog-pending rows, want its two adopted snapshots: %+v",
				h.Host, h.Pending, h)
		}
		if h.Sessions != 1 {
			t.Errorf("host %s recovered %d session identities from its rescan, want 1: %+v",
				h.Host, h.Sessions, h)
		}
		if h.NewestOrder != 3 {
			t.Errorf("host %s newest publication order is %d, want 3: %+v", h.Host, h.NewestOrder, h)
		}
	}

	// The rebuilt catalog is a working catalog, not just rows: a second instance
	// browses it and the deployment's session identities are back.
	assertDistinctSessions(t, dep, 2)
	if _, err := sharedcatalog.Open(context.Background(), dep.dsn()); err != nil {
		t.Fatalf("the rebuilt catalog is not usable: %v", err)
	}
}
