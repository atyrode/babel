// The §14 outage and TLS acceptance gates (issue #20, third bullet), run
// against whatever PostgreSQL the deployment harness addresses: a throwaway
// local cluster, or the operator's own add-on when BABEL_ACCEPT_PG_URI names
// one. The scenarios do not branch on which, so a verdict recorded against the
// real service is the verdict this code actually produces there.
//
// Two of the three bullets can only be simulated, and each simulation is chosen
// to be indistinguishable from the real event at the boundary Babel observes:
//
//   - A tenant cannot stop a managed PostgreSQL, so a database outage is an
//     endpoint that refuses the dial. A stopped server and a closed port hand
//     the client the same errno; what is under test is the classification Babel
//     makes of it (sharedcatalog.Unreachable) and what each command then does.
//   - A provider that cannot create roles cannot revoke one either (#37,
//     confirmed live 2026-08-31), so a revoked credential is the same endpoint
//     with a credential the server refuses. That is the server answering and
//     saying no - the case that must never be reported as an outage, because an
//     outage claims to resolve itself and a rejected credential does not.
//
// The repository outage is not simulated: the repository is a local path this
// suite owns, and it is moved out from under a running push.

package e2e_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// catalogVerified is `storage verify --json`. Decoding it with unknown fields
// rejected makes this a consumer-side contract test of that shape.
type catalogVerified struct {
	Endpoint string `json:"endpoint"`
	TLSMode  string `json:"tls_mode"`
	TLS      struct {
		Active   bool   `json:"active"`
		Protocol string `json:"protocol"`
	} `json:"tls"`
	SchemaVersion    int  `json:"schema_version"`
	SchemaCompatible bool `json:"schema_compatible"`
	PendingMigration bool `json:"pending_migration"`
	Application      struct {
		Level         string `json:"level"`
		Superuser     bool   `json:"superuser"`
		CanCreateRole bool   `json:"can_create_role"`
		CanCreateDB   bool   `json:"can_create_db"`
		CanDDL        bool   `json:"can_ddl"`
		User          string `json:"user"`
	} `json:"application"`
	RoleSeparation bool `json:"role_separation"`
}

// installDocument replaces this instance's storage.json in place, without the
// shipped command's live checks.
//
// Bypassing them is the point. `storage configure` validates the catalog before
// it writes, so it cannot install a document whose endpoint is down - correctly,
// because a deployment should not be configured against a catalog nobody can
// reach. An outage happens after configuration: the document is right and the
// server is gone. Writing the file directly is the only way to reach that
// state, and the mode matches what config.Save writes.
func (i *instance) installDocument(t *testing.T, cat catalogDoc) {
	t.Helper()
	dir := filepath.Join(i.configHome, "babel")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "storage.json"), []byte(i.document(cat)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// closedPort is a loopback port nothing is listening on. The kernel picks a
// free one and it is released before use, so a dial is refused immediately -
// unlike a firewalled address, which would hang and turn a fast assertion into
// a timeout.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// assertNoPassword proves an output stream never carried the catalog
// credential. It matters most on the failure paths in this file: driver errors
// are where a DSN leaks, and a failing command's output is exactly what an
// operator pastes into a ticket.
//
// The offending stream is deliberately not printed. It holds the password.
func assertNoPassword(t *testing.T, dep *deployment, streams ...string) {
	t.Helper()
	for _, s := range streams {
		if strings.Contains(s, dep.password) {
			t.Fatal("a command emitted the catalog password")
		}
	}
}

// outageInstance is one configured, migrated, initialized instance with sources
// to archive: the state every scenario in this file starts from.
func outageInstance(t *testing.T, dep *deployment, label string) *instance {
	t.Helper()
	i := dep.newInstance(t, label, hostA, instanceA)
	i.writeSources(t)
	i.configure(t)
	i.ok(t, "storage", "migrate")
	i.ok(t, i.with("archive", "init")...)
	return i
}

// TestCatalogOutageDefersPublicationAndRecovers is the database-outage gate.
//
// The promise under test is SPEC.md §9's: the archive is restic's, the catalog
// is derived, so an outage costs a deferred row and nothing else. A push during
// one must exit 0 - the operator has nothing to fix and the snapshot is durable
// - and the next push after it must adopt what was stranded.
func TestCatalogOutageDefersPublicationAndRecovers(t *testing.T) {
	dep := newDeployment(t)
	a := outageInstance(t, dep, "outage-a")

	first := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if first.Catalog != "committed" {
		t.Fatalf("the first push did not publish: %+v", first)
	}

	// The outage. The document keeps describing a correct deployment; the
	// endpoint simply stops answering.
	down := dep.catalogDoc()
	down.host, down.port = "127.0.0.1", closedPort(t)
	a.installDocument(t, down)

	// Status stays useful, and stays honest: the repository half is real, and
	// the catalog half reports unknown rather than zero, because a count this
	// command could not observe would be a claim.
	status := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if status.Snapshots != 1 {
		t.Fatalf("status lost the repository's snapshot during a catalog outage: %+v", status)
	}
	if status.Catalog == nil || status.Catalog.Reachable {
		t.Fatalf("an unreachable catalog reported itself reachable: %+v", status.Catalog)
	}
	if status.Catalog.Uncatalogued != nil || status.Catalog.Pending != nil {
		t.Fatalf("an unreachable catalog reported counts it cannot know: %+v", status.Catalog)
	}

	// The push defers. Exit 0, a durable snapshot, no published rows, and a
	// diagnostic that says what happens next.
	stdout, stderr, code := a.run(t, a.with("archive", "push", "--json")...)
	if code != exitOK {
		t.Fatalf("a push during a catalog outage exited %d instead of deferring", code)
	}
	assertNoPassword(t, dep, stdout, stderr)
	deferred := decode[pushResult](t, stdout)
	if deferred.Catalog != "uncatalogued" {
		t.Fatalf("a push during an outage reported catalog %q, want uncatalogued: %+v",
			deferred.Catalog, deferred)
	}
	if deferred.SnapshotID == "" {
		t.Fatalf("a push during an outage produced no snapshot: %+v", deferred)
	}
	if deferred.SessionsPublished != 0 {
		t.Fatalf("a push during an outage claimed to publish %d session rows", deferred.SessionsPublished)
	}
	if !strings.Contains(stderr, "the snapshot is durable") {
		t.Fatalf("a deferred push did not say the snapshot survived: %s", stderr)
	}

	// Local reading is unaffected: what this instance holds comes from its own
	// sources and its own SQLite cache, never from PostgreSQL.
	local := instJSON[sessionsResult](t, a, "sessions", "list", "--json")
	if len(local.Sessions) == 0 {
		t.Fatal("a catalog outage emptied the local session listing")
	}

	// The outage ends. The stranded snapshot is adopted from the repository's
	// own listing rather than republished, and it stays catalog-pending because
	// which sessions it held was never recorded and is not derivable (SPEC.md
	// §9).
	a.installDocument(t, dep.catalogDoc())
	recovered := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if recovered.Catalog != "committed" {
		t.Fatalf("the recovery push did not publish: %+v", recovered)
	}
	after := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if after.Catalog == nil || !after.Catalog.Reachable {
		t.Fatalf("the catalog is still unreachable after the outage: %+v", after.Catalog)
	}
	if after.Catalog.Uncatalogued == nil || *after.Catalog.Uncatalogued != 0 {
		t.Fatalf("a snapshot is still uncatalogued after recovery: %+v", after.Catalog)
	}
	if after.Catalog.Pending == nil || *after.Catalog.Pending != 1 {
		t.Fatalf("the adopted snapshot is not reported catalog-pending: %+v", after.Catalog)
	}
	row := after.catalogHost(t, hostA)
	if row.Snapshots != 3 || row.Pending != 1 || row.NewestOrder != 3 {
		t.Fatalf("host rows after outage recovery are wrong: %+v", row)
	}
}

// TestARefusedCatalogCredentialFailsRatherThanDefers is the per-instance
// revocation gate as it exists on a provider that cannot revoke: what a
// credential the server refuses does to a push.
//
// The distinction is the whole test. An outage defers, because reconciliation
// finishes the job; a refused credential must fail, because reporting a state
// that appears to resolve itself would hide a misconfiguration an operator has
// to act on. A server that accepts any credential cannot express this, and says
// so rather than passing vacuously.
func TestARefusedCatalogCredentialFailsRatherThanDefers(t *testing.T) {
	dep := newDeployment(t)
	a := outageInstance(t, dep, "credential-a")

	first := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if first.Catalog != "committed" {
		t.Fatalf("the first push did not publish: %+v", first)
	}

	revoked := dep.catalogDoc()
	revoked.password = dep.password + "-revoked"
	probe, err := sharedcatalog.Open(context.Background(), revoked.dsn(),
		sharedcatalog.WithMaxConnections(1))
	if err == nil {
		probe.Close()
		t.Skip("this server accepts any credential, so it cannot refuse one")
	}
	if sharedcatalog.Unreachable(err) {
		t.Fatalf("a server that answered and refused reads as an outage: %v", err)
	}

	a.installDocument(t, revoked)

	// The push fails. The snapshot it took is still durable - the backup runs
	// before the catalog is touched - which is why the failure is safe to
	// report as one.
	stdout, stderr, code := a.run(t, a.with("archive", "push", "--json")...)
	if code == exitOK {
		t.Fatal("a push with a credential the server refuses exited 0")
	}
	assertNoPassword(t, dep, stdout, stderr)
	if !strings.Contains(stderr, "shared catalog") {
		t.Fatalf("the failure does not name the catalog: %s", stderr)
	}

	// `storage verify` is the command an operator reaches for next, and it must
	// fail too: this is a configuration answer, not a transient one.
	vstdout, vstderr, vcode := a.run(t, "storage", "verify")
	if vcode == exitOK {
		t.Fatal("storage verify passed with a credential the server refuses")
	}
	assertNoPassword(t, dep, vstdout, vstderr)

	// Status still answers, because the repository half of it is real.
	status := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if status.Snapshots != 2 {
		t.Fatalf("status lost a durable snapshot: %+v", status)
	}
	if status.Catalog == nil || status.Catalog.Reachable {
		t.Fatalf("a refused credential reported the catalog as readable: %+v", status.Catalog)
	}

	// The credential is restored, and the snapshot the refusal stranded is
	// adopted by the next push.
	a.installDocument(t, dep.catalogDoc())
	recovered := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if recovered.Catalog != "committed" {
		t.Fatalf("the recovery push did not publish: %+v", recovered)
	}
	after := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if after.Catalog.Uncatalogued == nil || *after.Catalog.Uncatalogued != 0 {
		t.Fatalf("a snapshot is still uncatalogued after the credential was restored: %+v", after.Catalog)
	}
}

// TestRepositoryOutageFailsThePushAndLeavesTheCatalogUntouched is the
// repository-outage gate.
//
// The asymmetry with a catalog outage is deliberate and is what this asserts. A
// missing catalog costs a deferred row; a missing repository means there is no
// archive, so the push must fail - and it must fail without leaving the catalog
// claiming a snapshot that does not exist, because the catalog is derived state
// that no later rescan could correct downward.
func TestRepositoryOutageFailsThePushAndLeavesTheCatalogUntouched(t *testing.T) {
	dep := newDeployment(t)
	a := outageInstance(t, dep, "repo-outage-a")

	first := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if first.Catalog != "committed" || first.SessionsPublished == 0 {
		t.Fatalf("the first push did not publish: %+v", first)
	}

	// A moved directory is what a lost mount, an unreachable object store, or a
	// deleted bucket looks like from restic's side.
	away := dep.repoDir + "-away"
	if err := os.Rename(dep.repoDir, away); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := a.run(t, a.with("archive", "push", "--json")...)
	if code == exitOK {
		t.Fatal("a push into a repository that is gone exited 0")
	}
	assertNoPassword(t, dep, stdout, stderr)
	// It must read as a repository failure. An operator sent to PostgreSQL by
	// this message loses an afternoon.
	if !strings.Contains(stderr, dep.repoDir) && !strings.Contains(strings.ToLower(stderr), "repository") {
		t.Fatalf("a repository outage did not name the repository: %s", stderr)
	}

	// Nothing reached the catalog: no session identity, no publication order.
	assertDistinctSessions(t, dep, first.SessionsPublished)

	// The repository comes back and the next push is ordinary. Nothing had to
	// be repaired, which is the recovery property.
	if err := os.Rename(away, dep.repoDir); err != nil {
		t.Fatal(err)
	}
	back := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if back.Catalog != "committed" {
		t.Fatalf("the push after the repository returned did not publish: %+v", back)
	}
	after := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if after.Catalog.Uncatalogued == nil || *after.Catalog.Uncatalogued != 0 {
		t.Fatalf("a snapshot is uncatalogued after a repository outage: %+v", after.Catalog)
	}
	if row := after.catalogHost(t, hostA); row.Snapshots != 2 || row.Pending != 0 {
		t.Fatalf("the catalog's host rows are wrong after a repository outage: %+v", row)
	}
}

// TestTLSIsActiveAndVerifyFullIsRefusedWhereUnsatisfiable is the TLS gate.
//
// Both halves are properties of the server, not of the document. The positive
// half reads pg_stat_ssl, so it reports the connection the deployment actually
// has rather than the mode it asked for. The negative half tightens to
// verify-full, which authenticates the server: the local harness's self-signed
// certificate has no chain to a system root, and a shared managed cluster's
// certificate carries no name that matches its per-tenant host - measured on
// Clever Cloud's DEV plan, where the handshake fails with "certificate is not
// valid for any names" (2026-08-31, #20).
//
// What must hold in both is that the tightening is refused *before* it replaces
// a working document. An operator who tries to harden TLS and gets it wrong
// must still have a configured instance.
func TestTLSIsActiveAndVerifyFullIsRefusedWhereUnsatisfiable(t *testing.T) {
	dep := newDeployment(t)
	a := dep.newInstance(t, "tls-a", hostA, instanceA)
	a.configure(t)
	a.ok(t, "storage", "migrate")

	got := instJSON[catalogVerified](t, a, "storage", "verify", "--json")
	if got.TLSMode != "require" {
		t.Fatalf("the deployment is not on the mode it configured: %+v", got)
	}
	if !got.TLS.Active {
		t.Fatalf("the catalog connection is not encrypted: %+v", got.TLS)
	}
	if got.TLS.Protocol == "" {
		t.Fatalf("the server reported no TLS protocol: %+v", got.TLS)
	}

	strict := dep.catalogDoc()
	strict.tlsMode = "verify-full"
	stdout, stderr, code := a.configureFrom(t, strict)
	if code == exitOK {
		t.Fatal("verify-full was accepted against a certificate that cannot satisfy it")
	}
	assertNoPassword(t, dep, stdout, stderr)
	if !readsAsTLSFailure(stderr) {
		t.Fatalf("the refusal does not read as a TLS failure: %s", stderr)
	}

	// The working document survived the refusal.
	again := instJSON[catalogVerified](t, a, "storage", "verify", "--json")
	if again.TLSMode != "require" || !again.TLS.Active {
		t.Fatalf("a refused document displaced the working one: %+v", again)
	}
}

// readsAsTLSFailure reports whether a diagnostic sends an operator to the
// certificate rather than to the credential or the network. The vocabulary
// differs by cause - an unknown authority locally, an unmatched name on a
// shared managed cluster - so the marker set covers the ways Go's TLS stack and
// PostgreSQL name the same class of problem.
func readsAsTLSFailure(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, marker := range []string{"certificate", "x509", "tls", "ssl"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
