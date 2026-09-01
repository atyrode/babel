// Two-instance acceptance (SPEC.md §10): "A second independently configured
// Babel instance must browse the shared catalog, fetch a session archived by the
// first host, lose and rebuild its local SQLite cache, and recover cleanly."
//
// It is the last pre-deployment gate that can expose a design flaw in the shared
// foundation, because it is the first thing that exercises the parts a single
// instance cannot: two host identities in one catalog, one host reading another's
// archive without ever having its files, and a publication lease with a live
// owner that is somebody else (SPEC.md §9, decision 13).
//
// Each instance is genuinely independent — its own HOME, XDG configuration, data
// and cache directories, host identity, and instance id — and they share exactly
// what a real deployment shares: one restic repository and one PostgreSQL
// catalog. The catalog is reached through Babel's own configuration document, so
// the connection is TLS: shared mode rejects `sslmode=disable` outright, which is
// why the harness serves a certificate.

package e2e_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/pgtest"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// basePATH is the PATH this test binary started with, captured once so each
// activation composes from a fixed base rather than from whatever the previous
// activation left behind.
var basePATH = os.Getenv("PATH")

// baseEnv is the whole environment this test binary started with, for the one
// case that must not inherit an activated instance: building the executable.
var baseEnv = os.Environ()

const (
	// One deployment, two instances: the deployment id is what makes their
	// session identities comparable, and the instance ids are what let the
	// catalog tell them apart.
	twoInstanceDeployment = "e2e-two-instance-deployment"

	hostA = "e2ehost-a"
	hostB = "e2ehost-b"

	instanceA = "e2einstance-a"
	instanceB = "e2einstance-b"

	// A synthetic credential. Trust authentication means the server ignores
	// it, but the configuration document requires one, and every leak check in
	// this suite gets a real string to look for.
	catalogPassword = "synthetic-catalog-password"

	twoInstanceRepoPassword = "synthetic-two-instance-password\n"
)

// acceptURIEnv points the shared half of every scenario in this package at a
// PostgreSQL an operator provisioned, instead of a throwaway cluster this
// process starts. It is how the §14 gates are run against the provider a
// deployment will actually use (#20): the drill does not change, only the
// server does.
//
// Its value is a credential - it carries the catalog password - so it is read
// from the environment and never logged, never quoted in a failure, and never
// written anywhere but the mode-0600 document Babel itself writes.
//
// acceptRepoEnv is its counterpart for the archive half, with the same
// discipline; the two are independent, so a drill may address a real catalog, a
// real object store, either, or neither.
const acceptURIEnv = "BABEL_ACCEPT_PG_URI"

// acceptMaxConns is the pool ceiling each instance's document states against a
// real server.
//
// A managed plan caps connections per role rather than per pool: Clever
// Cloud's DEV PostgreSQL allows five in total (measured 2026-08-31, #20), so
// two instances at Babel's default of four cannot both be up. Two per instance
// plus one for this test's own assertion handle is exactly five, which is why
// the number is two.
const acceptMaxConns = 2

// Instance B's own session, distinct from every fixture instance A writes so a
// cross-host fetch cannot be satisfied by accident from local files.
const (
	ompProjectB = "-synthetic-e2e-second-instance"
	ompStemB    = "2026-02-03T04-05-06-789Z_00000000-0000-4000-8000-0000000000b1"
	titleB      = "Synthetic e2e second-instance session"
	workspaceB  = "/synthetic/workspace/second-instance"
)

// deployment is the shared half: one PostgreSQL catalog and one restic
// repository, both addressed by every instance.
//
// Neither half is necessarily local. The catalog is a throwaway cluster this
// process started or a real one an operator provisioned (acceptURIEnv), and the
// repository is a directory this process creates or a prefix in an operator's
// object store (acceptRepoEnv). Nothing below those two lines branches on
// which, so the drill cannot quietly prove less against the real services than
// it does locally.
type deployment struct {
	// cluster is nil in real-DSN mode, where nothing local is started and the
	// server's log belongs to the provider.
	cluster *pgtest.Cluster

	host     string
	port     int
	user     string
	password string
	database string

	// maxConns is the ceiling each instance's document states and probeConns
	// the one this test's own handle uses. Zero is Babel's default, which is
	// what a local cluster with no meaningful connection limit wants.
	maxConns   int
	probeConns int

	// db is the assertion handle, opened at most once per deployment.
	db *sql.DB

	// repo is the archive half: a local path this process creates, or a prefix
	// inside the object store an operator named (acceptRepoEnv). Scenarios
	// address it through its locator and never learn which it is, except where
	// one of them must move it (repository.moveAway).
	repo         repository
	passwordFile string
}

func newDeployment(t *testing.T) *deployment {
	t.Helper()
	// A real server replaces the local one entirely: nothing is provisioned,
	// and pgtest is not even consulted, so the drill runs on a machine without
	// initdb as long as the operator supplied an endpoint.
	var d *deployment
	if uri := os.Getenv(acceptURIEnv); uri != "" {
		d = realDeployment(t, uri)
	} else {
		d = localDeployment(t)
	}

	root := t.TempDir()
	d.repo = acceptRepository(t, filepath.Join(root, "repository"))
	d.passwordFile = filepath.Join(root, "repository-password")
	if err := os.WriteFile(d.passwordFile, []byte(twoInstanceRepoPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

// localDeployment starts a throwaway cluster and gives this run its own
// database inside it.
func localDeployment(t *testing.T) *deployment {
	t.Helper()
	pgtest.SkipOrFail(t)
	// TLS because the CLI builds its DSN from storage.json, and that document
	// cannot express an unencrypted connection.
	cluster, err := pgtest.Start(pgtest.Options{TLS: true})
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	t.Cleanup(cluster.Stop)

	d := &deployment{
		cluster:  cluster,
		host:     cluster.Host,
		port:     cluster.Port,
		user:     cluster.User,
		password: catalogPassword,
		database: "babel_two_instance",
	}

	admin, err := sharedcatalog.Open(context.Background(), cluster.BaseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v\nserver log:\n%s", err, cluster.Log())
	}
	if _, err := admin.Exec("CREATE DATABASE " + d.database); err != nil {
		admin.Close()
		t.Fatalf("create catalog database: %v", err)
	}
	admin.Close()
	return d
}

// realDeployment addresses the operator's own PostgreSQL, and isolates this
// suite inside it.
//
// A managed add-on hands out one database whose owner may not add another:
// `CREATE DATABASE` is refused on Clever Cloud's DEV plan and `pg_database` is
// not even readable (both measured against the real add-on, 2026-08-31, #20),
// so the per-run database localDeployment creates has no counterpart here.
// What Babel does own on that server is one schema - sharedcatalog.Schema -
// and that schema is the whole catalog: every table, ledger row and lease
// lives in it, by construction (see the package comment on Schema). So the
// drill drops it on arrival and again on the way out. Each run therefore
// starts from an unmigrated catalog, exactly as a local one does, and leaves
// the add-on as it was found.
func realDeployment(t *testing.T, uri string) *deployment {
	t.Helper()
	// Every failure below names the variable and never the value: the URI is a
	// credential, and a parser's echo of it would put the password in the test
	// log.
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("%s is not a valid URL", acceptURIEnv)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatalf("%s must be a postgres:// or postgresql:// URL", acceptURIEnv)
	}
	password, ok := u.User.Password()
	if u.User.Username() == "" || !ok {
		t.Fatalf("%s must carry a user and a password", acceptURIEnv)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 {
		t.Fatalf("%s must name a port", acceptURIEnv)
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" {
		t.Fatalf("%s must name a database", acceptURIEnv)
	}

	d := &deployment{
		host:     u.Hostname(),
		port:     port,
		user:     u.User.Username(),
		password: password,
		database: database,
		// The instances state a ceiling because the provider enforces one;
		// this test's own handle takes a single connection so the two
		// instances plus the assertion fit inside the role's five.
		maxConns:   acceptMaxConns,
		probeConns: 1,
	}

	reset := func() {
		db, err := sharedcatalog.Open(context.Background(), d.dsn(), sharedcatalog.WithMaxConnections(1))
		if err != nil {
			t.Fatalf("reach the acceptance catalog named by %s: %v", acceptURIEnv, err)
		}
		defer db.Close()
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + sharedcatalog.Schema + " CASCADE"); err != nil {
			t.Fatalf("drop the %s schema: %v", sharedcatalog.Schema, err)
		}
	}
	reset()
	t.Cleanup(reset)
	return d
}

// dsn addresses the shared catalog as the test itself, for the assertions that
// are only answerable in SQL and for holding a lease no instance owns. It is
// the document's own connection, so an assertion cannot pass against a server
// the instances are not using.
func (d *deployment) dsn() string { return d.catalogDoc().dsn() }

// open returns the deployment's assertion handle, opening it once.
//
// One handle, shared: against a real server the role's whole connection budget
// is five (Clever Cloud DEV, measured 2026-08-31, #20), and a per-assertion
// pool would compete for it with the two instances that are the actual subject
// of the drill.
func (d *deployment) open(t *testing.T) *sql.DB {
	t.Helper()
	if d.db != nil {
		return d.db
	}
	db, err := sharedcatalog.Open(context.Background(), d.dsn(),
		sharedcatalog.WithMaxConnections(d.probeConns))
	if err != nil {
		t.Fatalf("open shared catalog: %v", err)
	}
	d.db = db
	t.Cleanup(func() {
		db.Close()
		d.db = nil
	})
	return db
}

// instance is one independently configured Babel installation.
type instance struct {
	*env
	label      string
	hostID     string
	instanceID string
	configHome string
	dep        *deployment
}

func (d *deployment) newInstance(t *testing.T, label, hostID, instanceID string) *instance {
	t.Helper()
	root := t.TempDir()
	e := &env{
		root:         root,
		home:         filepath.Join(root, "home"),
		repository:   d.repo.locator,
		passwordFile: d.passwordFile,
		dataHome:     filepath.Join(root, "data"),
		cacheHome:    filepath.Join(root, "cache"),
	}
	e.ompSessions = filepath.Join(e.home, ".omp", "agent", "sessions")
	e.ompBlobs = filepath.Join(e.home, ".omp", "agent", "blobs")
	e.codexHome = filepath.Join(e.home, ".codex")
	e.claudeHome = filepath.Join(e.home, ".claude")
	mkdir(t, e.home)

	return &instance{
		env:        e,
		label:      label,
		hostID:     hostID,
		instanceID: instanceID,
		configHome: filepath.Join(root, "config"),
		dep:        d,
	}
}

// activate makes this instance the one the next command runs as. Every
// Babel-visible root is switched together: an instance that shared any of them
// would not be independent, and the test would prove less than it claims.
//
// BABEL_HOST_ID is deliberately cleared so host identity comes from the
// instance's own storage.json, which is how a deployed instance resolves it.
func (i *instance) activate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", i.home)
	t.Setenv("XDG_CONFIG_HOME", i.configHome)
	t.Setenv("XDG_DATA_HOME", i.dataHome)
	t.Setenv("XDG_CACHE_HOME", i.cacheHome)
	t.Setenv("CODEX_HOME", i.codexHome)
	t.Setenv("BABEL_HOST_ID", "")
	t.Setenv("BABEL_RESTIC_REPO", "")
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", "")
	// The restic directory is prepended to the PATH this process started with,
	// not to the current one: activate runs before every command, and composing
	// onto the live value would grow it by one entry per invocation.
	t.Setenv("PATH", filepath.Dir(resticBinary(t))+string(os.PathListSeparator)+basePATH)
}

func (i *instance) ok(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	i.activate(t)
	stdout, stderr, code := i.env.run(t, args...)
	if code != exitOK {
		t.Fatalf("%s: babel %s exited %d\nstdout:\n%s\nstderr:\n%s",
			i.label, strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

func (i *instance) run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	i.activate(t)
	return i.env.run(t, args...)
}

// instJSON is okJSON for one instance: activate, then hold the command to the
// same stdout-only contract the rest of the suite enforces.
func instJSON[T any](t *testing.T, i *instance, args ...string) T {
	t.Helper()
	i.activate(t)
	return okJSON[T](t, i.env, args...)
}

// catalogDoc is the catalog half of one instance's document. A scenario varies
// exactly one connection parameter - the TLS mode, the credential, the
// endpoint - and leaves the rest a working deployment's, which is what makes
// the failure it then asserts attributable.
type catalogDoc struct {
	host     string
	port     int
	user     string
	password string
	database string
	tlsMode  string
	maxConns int
}

// dsn is the connection string this catalog describes, assembled the way the
// CLI assembles its own (config.Catalog.DSN) - through net/url, because a real
// credential may carry bytes that are reserved in a URL.
//
// The result is a credential: it may be handed to a driver and to nothing
// else.
func (c catalogDoc) dsn() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.user, c.password),
		Host:     net.JoinHostPort(c.host, strconv.Itoa(c.port)),
		Path:     "/" + c.database,
		RawQuery: "sslmode=" + c.tlsMode,
	}
	return u.String()
}

// catalogDoc returns the document that describes this deployment truthfully.
func (d *deployment) catalogDoc() catalogDoc {
	return catalogDoc{
		host:     d.host,
		port:     d.port,
		user:     d.user,
		password: d.password,
		database: d.database,
		tlsMode:  "require",
		maxConns: d.maxConns,
	}
}

// document renders this instance's whole storage configuration.
//
// max_connections is emitted only when the deployment states one, so the local
// drill keeps exercising the omitted-field default and the real one exercises
// the ceiling its provider requires. repository_store is emitted only when the
// repository is an object store, for the stronger reason that config refuses
// the document otherwise, in either direction: an s3 locator without the
// credential, and the credential without a store to spend it on.
func (i *instance) document(cat catalogDoc) string {
	pool := ""
	if cat.maxConns > 0 {
		pool = fmt.Sprintf(",\n    \"max_connections\": %d", cat.maxConns)
	}
	return fmt.Sprintf(`{
  "config_schema": 2,
  "mode": "shared",
  "repository": %q,
  "password_file": %q,
  "host_id": %q,
  "deployment_id": %q,
  "instance_id": %q%s,
  "catalog": {
    "host": %q,
    "port": %d,
    "database": %q,
    "user": %q,
    "password": %q,
    "tls_mode": %q%s
  }
}`, i.dep.repo.locator, i.dep.passwordFile, i.hostID, twoInstanceDeployment, i.instanceID,
		i.dep.repo.documentStore(),
		cat.host, cat.port, cat.database, cat.user, cat.password, cat.tlsMode, pool)
}

// configureFrom installs a catalog document through the shipped command, which
// validates identity, TLS, credential privileges, and schema compatibility
// before writing the mode-0600 file - so a document that cannot work never
// displaces one that does.
//
// The rendered file is removed either way: it holds the credential in
// cleartext, and only storage.json may keep it. The outcome is returned rather
// than asserted, because scenarios exist whose subject is the refusal.
func (i *instance) configureFrom(t *testing.T, cat catalogDoc) (stdout, stderr string, code int) {
	t.Helper()
	path := filepath.Join(i.root, "storage-configure.json")
	if err := os.WriteFile(path, []byte(i.document(cat)), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}()
	return i.run(t, "storage", "configure", "--from-json", path)
}

// configure installs this instance's real configuration and requires it to be
// accepted.
func (i *instance) configure(t *testing.T) {
	t.Helper()
	stdout, stderr, code := i.configureFrom(t, i.dep.catalogDoc())
	if code != exitOK {
		t.Fatalf("%s: storage configure exited %d\nstdout:\n%s\nstderr:\n%s",
			i.label, code, stdout, stderr)
	}
}

// The machine-readable shapes shared mode adds. Decoding with
// DisallowUnknownFields makes this a consumer-side contract test of them.

type catalogHostRow struct {
	Host           string `json:"host"`
	Snapshots      int    `json:"snapshots"`
	Sessions       int    `json:"sessions"`
	Pending        int    `json:"pending"`
	NewestOrder    int64  `json:"newest_order"`
	NewestSnapshot string `json:"newest_snapshot"`
}

type catalogStatusDoc struct {
	Reachable bool `json:"reachable"`
	// Absent rather than zero when the catalog could not be read: a count this
	// command did not observe is unknown, and 0 would be a claim.
	Uncatalogued *int             `json:"uncatalogued"`
	Pending      *int             `json:"pending"`
	Hosts        []catalogHostRow `json:"hosts"`
}

type sharedStatusResult struct {
	Repository string            `json:"repository"`
	Snapshots  int               `json:"snapshots"`
	Hosts      []statusHostRow   `json:"hosts"`
	Catalog    *catalogStatusDoc `json:"catalog"`
}

func (r sharedStatusResult) catalogHost(t *testing.T, host string) catalogHostRow {
	t.Helper()
	if r.Catalog == nil {
		t.Fatalf("status reported no catalog at all: %+v", r)
	}
	for _, h := range r.Catalog.Hosts {
		if h.Host == host {
			return h
		}
	}
	t.Fatalf("the catalog holds no rows for host %q: %+v", host, r.Catalog.Hosts)
	return catalogHostRow{}
}

// TestTwoInstanceAcceptance is the §10 gate, run as one scenario rather than
// independent cases: the property under test is that two instances compose over
// one repository and one catalog, which per-command tests cannot observe.
func TestTwoInstanceAcceptance(t *testing.T) {
	dep := newDeployment(t)

	a := dep.newInstance(t, "instance-a", hostA, instanceA)
	b := dep.newInstance(t, "instance-b", hostB, instanceB)

	// Instance A holds the three-harness fixture set. Instance B holds one
	// session of its own, so neither can satisfy a cross-host fetch locally.
	srcA := a.writeSources(t)
	bPrimary := b.writeOMPSession(t, ompSpec{
		project:   ompProjectB,
		stem:      ompStemB,
		id:        "00000000-0000-4000-8000-0000000000b1",
		title:     titleB,
		workspace: workspaceB,
		artifacts: map[string]string{"Notes.jsonl": "{\"type\":\"message\",\"text\":\"second instance\"}\n"},
	})
	selectorB := "omp/" + ompProjectB + "/" + ompStemB
	sourceBytesA := readFile(t, srcA.richPrimary)
	sourceBytesB := readFile(t, bPrimary)

	// --- Both instances configure independently, and only one migrates. ---
	//
	// A migration is a deployment-wide act, not a per-instance one: the second
	// instance must find a schema it can use without being told to migrate.
	a.configure(t)
	a.ok(t, "storage", "migrate")
	a.ok(t, a.with("archive", "init")...)
	b.configure(t)
	if _, stderr := b.ok(t, "storage", "verify"); strings.Contains(stderr, "migrate") {
		t.Fatalf("the second instance was told to migrate an already-migrated catalog: %s", stderr)
	}

	// --- A archives and publishes. ---
	pushA := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if pushA.Catalog != "committed" {
		t.Fatalf("instance A push catalog state = %q, want committed: %+v", pushA.Catalog, pushA)
	}
	if pushA.SessionsPublished == 0 {
		t.Fatalf("instance A published no session rows: %+v", pushA)
	}
	if pushA.Host != hostA {
		t.Fatalf("instance A archived under host %q, want %q", pushA.Host, hostA)
	}

	// --- B browses the shared catalog: the first §10 requirement. ---
	//
	// B has never pushed, holds none of A's files, and is asking the catalog
	// what the fleet has. This is the assertion that fails if shared mode is
	// write-only.
	statusB := instJSON[sharedStatusResult](t, b, b.with("archive", "status", "--json")...)
	if statusB.Catalog == nil || !statusB.Catalog.Reachable {
		t.Fatalf("instance B could not read the shared catalog: %+v", statusB.Catalog)
	}
	rowA := statusB.catalogHost(t, hostA)
	if rowA.Snapshots != 1 || rowA.Sessions != pushA.SessionsPublished || rowA.Pending != 0 {
		t.Fatalf("instance B's view of host A is wrong: %+v (push published %d sessions)",
			rowA, pushA.SessionsPublished)
	}
	if rowA.NewestOrder != 1 || rowA.NewestSnapshot == "" {
		t.Fatalf("instance B's view of host A carries no publication order or time: %+v", rowA)
	}

	// --- B archives its own sources, and the catalog holds two hosts. ---
	pushB := instJSON[pushResult](t, b, b.with("archive", "push", "--json")...)
	if pushB.Catalog != "committed" || pushB.SessionsPublished != 1 {
		t.Fatalf("instance B push did not publish its one session: %+v", pushB)
	}
	if pushB.Host != hostB {
		t.Fatalf("instance B archived under host %q, want %q", pushB.Host, hostB)
	}

	statusA := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if got := len(statusA.Catalog.Hosts); got != 2 {
		t.Fatalf("the catalog holds %d hosts, want 2: %+v", got, statusA.Catalog.Hosts)
	}
	if !slices.IsSortedFunc(statusA.Catalog.Hosts, func(x, y catalogHostRow) int {
		return strings.Compare(x.Host, y.Host)
	}) {
		t.Fatalf("catalog host rows are not ordered by host: %+v", statusA.Catalog.Hosts)
	}
	if rowB := statusA.catalogHost(t, hostB); rowB.Sessions != 1 {
		t.Fatalf("instance A's view of host B is wrong: %+v", rowB)
	}
	// Two hosts, same deployment, and A's fixture set includes no session of
	// B's: identity is per host, so the distinct total is the sum.
	assertDistinctSessions(t, dep, pushA.SessionsPublished+1)

	// --- B lists what A archived, then fetches one of A's sessions. ---
	//
	// The selector vocabulary is the point: B addresses A's session by the same
	// key A's own listing would use, resolved from the snapshot's file tree
	// because no source path is ever stored in PostgreSQL (SPEC.md §6.2).
	listed := instJSON[archiveListResult](t, b,
		b.with("sessions", "list", "--host", hostA, "--json")...)
	if !slices.Contains(selectorsOf(listed.Sessions), srcA.richSelector) {
		t.Fatalf("instance B's listing of host A omits %q: %v",
			srcA.richSelector, selectorsOf(listed.Sessions))
	}

	fetched := instJSON[fetchResult](t, b,
		b.with("sessions", "fetch", srcA.richSelector, "--host", hostA, "--json")...)
	if fetched.SnapshotID != pushA.SnapshotID {
		t.Fatalf("instance B fetched snapshot %q, want A's %q", fetched.SnapshotID, pushA.SnapshotID)
	}
	if len(fetched.Missing) != 0 {
		t.Fatalf("instance B's fetch reported missing closure paths: %v", fetched.Missing)
	}
	// Byte-exact, and inside B's own store: a fetch that landed in A's data
	// directory would pass a weaker assertion while proving nothing.
	target := filepath.Join(fetched.Target, srcA.richPrimary)
	if got := readFile(t, target); !bytes.Equal(got, sourceBytesA) {
		t.Fatalf("instance B restored %d bytes, want A's %d", len(got), len(sourceBytesA))
	}
	if !strings.HasPrefix(fetched.Target, b.dataHome) {
		t.Fatalf("instance B materialized into %q, which is not under its own data home %q",
			fetched.Target, b.dataHome)
	}

	// Symmetry: neither instance is privileged. A fetches B's session the same
	// way, which is what makes this a fleet rather than a primary and a reader.
	fetchedB := instJSON[fetchResult](t, a,
		a.with("sessions", "fetch", selectorB, "--host", hostB, "--json")...)
	if got := readFile(t, filepath.Join(fetchedB.Target, bPrimary)); !bytes.Equal(got, sourceBytesB) {
		t.Fatal("instance A did not restore instance B's session byte-exactly")
	}

	// --- B loses its local SQLite cache and recovers. ---
	//
	// The cache is derived state: every row in it comes from live local sources
	// or from a describe that can be run again. Losing it must cost time, not
	// answers.
	cachePath := filepath.Join(b.dataHome, "babel", "catalog.db")
	localBefore := instJSON[sessionsResult](t, b, "sessions", "list", "--json")
	if len(localBefore.Sessions) != 1 {
		t.Fatalf("instance B's local listing is not its own single session: %+v", localBefore.Sessions)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("instance B's local catalog was never created at %s: %v", cachePath, err)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}

	localAfter := instJSON[sessionsResult](t, b, "sessions", "list", "--json")
	if !sessionsEqual(localBefore.Sessions, localAfter.Sessions) {
		t.Fatalf("instance B's listing changed when its cache was rebuilt:\nbefore: %+v\nafter:  %+v",
			localBefore.Sessions, localAfter.Sessions)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("instance B did not rebuild its local catalog: %v", err)
	}
	// The shared view and the already-materialized session are unaffected: one
	// is another machine's truth, the other is retained data rather than cache.
	statusAfter := instJSON[sharedStatusResult](t, b, b.with("archive", "status", "--json")...)
	if got := statusAfter.catalogHost(t, hostA); got != rowA {
		t.Fatalf("cache loss changed instance B's view of host A:\nbefore: %+v\nafter:  %+v", rowA, got)
	}
	again := instJSON[fetchResult](t, b,
		b.with("sessions", "fetch", srcA.richSelector, "--host", hostA, "--json")...)
	if !again.AlreadyPresent {
		t.Fatalf("cache loss discarded a materialized session: %+v", again)
	}

	// --- A publication lease with a live owner refuses the other claimant. ---
	//
	// SPEC.md §9: another instance may browse and fetch committed data but
	// cannot claim a host's publication lease while a valid owner exists. The
	// test holds host A's lease as a third registered instance, so A's own next
	// push meets a live owner that is not itself.
	db := dep.open(t)
	ctx := context.Background()
	const phantom = "e2einstance-phantom"
	if err := sharedcatalog.Register(ctx, db, twoInstanceDeployment, hostA, phantom, sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("register the lease holder: %v", err)
	}
	lease, err := sharedcatalog.AcquireHostLease(ctx, db, hostA, phantom, time.Minute)
	if err != nil {
		t.Fatalf("hold host A's lease: %v", err)
	}

	// Contention is not misconfiguration: the snapshot is durable, the operator
	// has nothing to fix, and the next push carries it. So this exits 0 and
	// reports the snapshot uncatalogued rather than failing.
	stdout, stderr, code := a.run(t, a.with("archive", "push", "--json")...)
	if code != exitOK {
		t.Fatalf("a contended push failed instead of deferring: exit %d\nstderr:\n%s", code, stderr)
	}
	deferred := decode[pushResult](t, stdout)
	if deferred.Catalog != "uncatalogued" {
		t.Fatalf("a contended push reported catalog %q, want uncatalogued: %+v", deferred.Catalog, deferred)
	}
	if deferred.SnapshotID == "" {
		t.Fatalf("a contended push produced no snapshot: %+v", deferred)
	}
	if deferred.SessionsPublished != 0 {
		t.Fatalf("a contended push claimed to publish %d session rows", deferred.SessionsPublished)
	}
	if !strings.Contains(stderr, "another instance is publishing for this host") {
		t.Fatalf("a contended push did not say why it deferred: %s", stderr)
	}

	// The archive is intact and the catalog says so: one snapshot exists that
	// it has no row for.
	contended := instJSON[sharedStatusResult](t, b, b.with("archive", "status", "--json")...)
	if contended.Catalog.Uncatalogued == nil || *contended.Catalog.Uncatalogued != 1 {
		t.Fatalf("the stranded snapshot is not reported as uncatalogued: %+v", contended.Catalog)
	}

	// --- The next uncontended push adopts it. ---
	if err := sharedcatalog.ReleaseHostLease(ctx, db, lease); err != nil {
		t.Fatalf("release the held lease: %v", err)
	}
	recovered := instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	if recovered.Catalog != "committed" {
		t.Fatalf("the recovery push did not publish: %+v", recovered)
	}

	final := instJSON[sharedStatusResult](t, b, b.with("archive", "status", "--json")...)
	if final.Catalog.Uncatalogued == nil || *final.Catalog.Uncatalogued != 0 {
		t.Fatalf("a snapshot is still uncatalogued after recovery: %+v", final.Catalog)
	}
	// Adopted from the repository listing, so restic's counts are restored but
	// the record of which sessions it held was never written and is not
	// derivable: that row stays catalog-pending, and no shipped command resolves
	// it - §12 Phase C's restore-and-rescan is what would (SPEC.md §9).
	if final.Catalog.Pending == nil || *final.Catalog.Pending != 1 {
		t.Fatalf("the adopted snapshot is not reported catalog-pending: %+v", final.Catalog)
	}
	finalA := final.catalogHost(t, hostA)
	if finalA.Snapshots != 3 || finalA.Pending != 1 {
		t.Fatalf("host A's catalog rows after recovery are wrong: %+v", finalA)
	}
	// Publication order is monotonic across the adoption: a reader selecting
	// the newest row must not land on the older, stranded snapshot.
	if finalA.NewestOrder != 3 {
		t.Fatalf("host A's newest publication order is %d, want 3: %+v", finalA.NewestOrder, finalA)
	}

	// Nothing in any of this printed the catalog password.
	assertNoCatalogCredential(t, a, b)
}

// TestSessionIdentityFollowsThePublishingHost pins which host a session's
// catalog identity is derived from.
//
// The snapshot is written under the resolved host — the flag, else the
// environment, else storage.json — while session identity used to be derived
// from storage.json alone. Any override made the catalog attribute a host's
// sessions to an identity that never published them, silently, and two hosts
// archiving the same source tree would have collided on one digest instead of
// producing two (decision 9).
func TestSessionIdentityFollowsThePublishingHost(t *testing.T) {
	dep := newDeployment(t)
	i := dep.newInstance(t, "instance-a", hostA, instanceA)
	i.writeOMPSession(t, ompSpec{
		project:   ompProjectB,
		stem:      ompStemB,
		id:        "00000000-0000-4000-8000-0000000000b1",
		title:     titleB,
		workspace: workspaceB,
	})

	i.configure(t)
	i.ok(t, "storage", "migrate")
	i.ok(t, i.with("archive", "init")...)

	// Once as the configured host, once with --host overriding it. One session
	// on disk, two publishing identities.
	first := instJSON[pushResult](t, i, i.with("archive", "push", "--json")...)
	if first.Host != hostA || first.SessionsPublished != 1 {
		t.Fatalf("first push: %+v", first)
	}
	second := instJSON[pushResult](t, i,
		i.with("archive", "push", "--host", hostB, "--json")...)
	if second.Host != hostB {
		t.Fatalf("--host did not change the archived identity: %+v", second)
	}
	if second.SessionsPublished != 1 {
		t.Fatalf("the overriding push published %d session rows, want 1: %+v",
			second.SessionsPublished, second)
	}

	// Two identities for one source session. Deriving from storage.json would
	// yield one digest here, and this count would be 1.
	assertDistinctSessions(t, dep, 2)
}

// assertDistinctSessions counts session identities the whole deployment holds.
// It is asked in SQL because the digest is opaque by design: no command exposes
// it, and no per-host count can distinguish two identities from one shared row.
func assertDistinctSessions(t *testing.T, dep *deployment, want int) {
	t.Helper()
	db := dep.open(t)
	var got int
	if err := db.QueryRow(`SELECT count(DISTINCT session_uid) FROM sessions`).Scan(&got); err != nil {
		t.Fatalf("count session identities: %v", err)
	}
	if got != want {
		t.Fatalf("the catalog holds %d distinct session identities, want %d", got, want)
	}
}

// sessionsEqual compares two local listings field for field, so a rebuilt cache
// that quietly dropped a nullable value fails rather than passing on length.
func sessionsEqual(before, after []sessionRow) bool {
	return slices.EqualFunc(before, after, func(x, y sessionRow) bool {
		return x.Harness == y.Harness && x.SourceID == y.SourceID &&
			x.Selector == y.Selector && x.Size == y.Size &&
			ptrEqual(x.Modified, y.Modified) && ptrEqual(x.Title, y.Title) &&
			ptrEqual(x.Workspace, y.Workspace) && x.Continuous == y.Continuous
	})
}

func ptrEqual[T comparable](x, y *T) bool {
	if x == nil || y == nil {
		return x == nil && y == nil
	}
	return *x == *y
}

// assertNoCatalogCredential re-runs the commands that touch the catalog and
// proves the password never reaches an output stream or a local file. The
// configuration document holds it at mode 0600; nothing else may.
func assertNoCatalogCredential(t *testing.T, instances ...*instance) {
	t.Helper()
	for _, i := range instances {
		for _, args := range [][]string{
			i.with("archive", "status"),
			i.with("archive", "status", "--json"),
			{"storage", "status"},
			{"storage", "verify"},
		} {
			stdout, stderr := i.ok(t, args...)
			if strings.Contains(stdout, i.dep.password) || strings.Contains(stderr, i.dep.password) {
				t.Fatalf("%s: babel %s emitted the catalog password",
					i.label, strings.Join(args, " "))
			}
		}
	}
}
