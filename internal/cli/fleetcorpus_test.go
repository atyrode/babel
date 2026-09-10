package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/conductor"

	"github.com/atyrode/babel/internal/restic"
)

// fakeArchive stands in for a restic repository: recorded absolute paths with
// contents, restored by mirroring them beneath the target the way restic does,
// and a per-path failure the test can arm.
//
// It is a fake and it has to be. What these tests assert is Babel's batching
// behaviour — that a second run downloads nothing, and that one session's
// failure costs that session and not the batch — and the failure that matters
// most in practice is a transient repository lock, which a real repository
// cannot be asked to produce on demand.
type fakeArchive struct {
	mu sync.Mutex
	// files maps one recorded absolute path to its content.
	files map[string]string
	// fails maps a recorded path to the error a restore naming it returns.
	fails map[string]error
	// restores counts the restore invocations, which is what proves a resumed
	// batch went nowhere near the repository.
	restores int
	// live and peak observe how many restores overlapped, so the batch's
	// concurrency is a measurement rather than a hope.
	live, peak int
}

func (f *fakeArchive) Restore(_ context.Context, _ string, includes []string, target string) error {
	f.mu.Lock()
	f.restores++
	f.live++
	if f.live > f.peak {
		f.peak = f.live
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.live--
		f.mu.Unlock()
	}()
	// A little overlap so concurrent restores are actually concurrent rather
	// than serialized by how fast the fake returns.
	time.Sleep(time.Millisecond)

	for _, path := range includes {
		if err := f.fails[path]; err != nil {
			return err
		}
	}
	for _, path := range includes {
		body, ok := f.files[path]
		if !ok {
			continue
		}
		landed := filepath.Join(target, filepath.FromSlash(strings.TrimPrefix(path, "/")))
		if err := os.MkdirAll(filepath.Dir(landed), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(landed, []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// remoteSession is one synthetic OMP session in a snapshot taken on another
// machine: the paths are that machine's, and nothing about them exists here.
func remoteSession(project, stem string) (key string, primary string, files []string) {
	primary = "/Users/remote/.omp/agent/sessions/" + project + "/" + stem + ".jsonl"
	return "omp/" + project + "/" + stem, primary, []string{primary}
}

// fleetSnapshot is the snapshot the fetch tests restore from. Attribution comes
// from its recorded host, which is what `archive push` wrote on the machine
// that archived these sessions.
var fleetSnapshot = restic.Snapshot{
	ID:      "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff",
	ShortID: "11112222",
	Host:    "macbook",
	Time:    time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC),
}

// TestBulkFetchResumesAndSurvivesOneFailedSession is the batch contract: the
// second run over the same snapshot downloads nothing and leaves the trees
// byte-identical, and the one session whose restore fails is reported against
// its own selector while every other session is materialized.
func TestBulkFetchResumesAndSurvivesOneFailedSession(t *testing.T) {
	root := t.TempDir()
	archive := &fakeArchive{files: map[string]string{}, fails: map[string]error{}}
	var sessions []archivedSession
	var keys []string
	for _, stem := range []string{"first", "second", "third"} {
		key, primary, files := remoteSession("remote-project", stem)
		archive.files[primary] = "{\"type\":\"message\",\"text\":\"synthetic " + stem + "\"}\n"
		sessions = append(sessions, archivedSession{harness: "omp", sess: archivedFixture(key, primary, files)})
		keys = append(keys, key)
	}
	// The one failure every batch has to survive: restic reports a repository
	// lock held by another process, for one session, transiently.
	_, doomedPrimary, _ := remoteSession("remote-project", "second")
	archive.fails[doomedPrimary] = fmt.Errorf("restore: repository is already locked exclusively")

	stderr := &syncBuffer{}
	a := &app{stdout: &syncBuffer{}, stderr: stderr}
	got := a.fetchSessionSet(context.Background(), archive, root, "macbook", fleetSnapshot, sessions, 3)

	if got.Sessions != 3 || got.Fetched != 2 || got.Resumed != 0 || len(got.Failures) != 1 {
		t.Fatalf("first pass = %+v, want 3 sessions, 2 fetched, 0 resumed, 1 failure", got)
	}
	if got.Failures[0].Selector != keys[1] {
		t.Errorf("the failure is attributed to %q, want %q", got.Failures[0].Selector, keys[1])
	}
	if !strings.Contains(got.Failures[0].Error, "locked") {
		t.Errorf("the failure does not carry restic's reason: %q", got.Failures[0].Error)
	}
	if archive.peak < 2 {
		t.Errorf("a batch with --concurrency 3 never overlapped two restores (peak %d)", archive.peak)
	}
	for _, key := range []string{keys[0], keys[2]} {
		if _, _, err := treeSize(fetchTree(root, key, fleetSnapshot)); err != nil {
			t.Errorf("%s was not materialized: %v", key, err)
		}
	}
	// The failed session left nothing behind: a half-restored tree would be
	// indistinguishable from a complete one on the next pass.
	if _, _, err := treeSize(fetchTree(root, keys[1], fleetSnapshot)); err == nil {
		t.Errorf("the failed session left a tree behind at %s", fetchTree(root, keys[1], fleetSnapshot))
	}

	before := treeDigest(t, root)
	restoresAfterFirst := archive.restores
	resumed := a.fetchSessionSet(context.Background(), archive, root, "macbook", fleetSnapshot, sessions, 3)
	if resumed.Resumed != 2 || resumed.Fetched != 0 || len(resumed.Failures) != 1 {
		t.Fatalf("second pass = %+v, want 2 resumed, 0 fetched, 1 failure", resumed)
	}
	// Only the session that failed was attempted again: the two that are here
	// are recognized from their directory names, with no repository access.
	if attempts := archive.restores - restoresAfterFirst; attempts != 1 {
		t.Errorf("a resumed batch made %d restore attempts, want 1 (the failed session)", attempts)
	}
	if after := treeDigest(t, root); !reflect.DeepEqual(before, after) {
		t.Errorf("resuming rewrote the materialized trees:\nbefore %v\nafter  %v", before, after)
	}
	if !strings.Contains(stderr.String(), "already materialized") {
		t.Errorf("a resumed session was not reported on stderr:\n%s", stderr.String())
	}
}

// archivedFixture states one identified archived session without depending on
// an adapter, so a fetch test is about fetching.
func archivedFixture(key, primary string, files []string) adapter.ArchivedSession {
	return adapter.ArchivedSession{
		SourceID:    strings.TrimPrefix(key, "omp/"),
		PrimaryPath: primary,
		PrimarySize: int64(len(primary)),
		Files:       files,
	}
}

// TestFetchedCorpusCarriesTheHostThatArchivedIt walks the whole loop these
// tests exist for: sessions fetched out of another machine's snapshot are
// discovered here, under the identity that machine assigns them, attributed to
// that machine rather than to this one.
func TestFetchedCorpusCarriesTheHostThatArchivedIt(t *testing.T) {
	root := t.TempDir()
	archive := &fakeArchive{files: map[string]string{}, fails: map[string]error{}}
	key, primary, files := remoteSession("remote-project", "only")
	archive.files[primary] = "{\"type\":\"message\",\"text\":\"synthetic fetched session\"}\n"
	sessions := []archivedSession{{harness: "omp", sess: archivedFixture(key, primary, files)}}

	a := &app{stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	if got := a.fetchSessionSet(context.Background(), archive, root, "macbook", fleetSnapshot, sessions, 1); got.Fetched != 1 {
		t.Fatalf("fetch = %+v, want one session fetched", got)
	}

	found, unattributed, err := fetchedCorpus(context.Background(), root, adapters())
	if err != nil {
		t.Fatalf("fetchedCorpus: %v", err)
	}
	if unattributed != 0 {
		t.Errorf("a freshly fetched corpus reported %d unattributed trees", unattributed)
	}
	if len(found) != 1 {
		t.Fatalf("discovered %d sessions in the fetched corpus, want 1: %+v", len(found), found)
	}
	if found[0].key() != key {
		t.Errorf("the fetched session is addressed as %q, want %q", found[0].key(), key)
	}
	if found[0].origin != "macbook" {
		t.Errorf("the fetched session is attributed to %q, want the machine that archived it", found[0].origin)
	}
	if want := filepath.Join(fetchTree(root, key, fleetSnapshot), "Users/remote/.omp/agent/sessions/remote-project/only.jsonl"); found[0].src.PrimaryPath != want {
		t.Errorf("the fetched session's primary path is %q, want the local mirror %q", found[0].src.PrimaryPath, want)
	}
	// The hint keeps the path the session has on its own machine, which the
	// local mirror path cannot show.
	if found[0].src.Hint != primary {
		t.Errorf("the fetched session's hint is %q, want the origin path %q", found[0].src.Hint, primary)
	}

	// A tree with no origin record is skipped and counted rather than
	// attributed to this machine, and fetching again records it.
	if err := os.Remove(originPath(fetchTree(root, key, fleetSnapshot))); err != nil {
		t.Fatal(err)
	}
	found, unattributed, err = fetchedCorpus(context.Background(), root, adapters())
	if err != nil {
		t.Fatalf("fetchedCorpus: %v", err)
	}
	if len(found) != 0 || unattributed != 1 {
		t.Fatalf("an unattributed tree yielded %d sessions and %d unattributed, want 0 and 1", len(found), unattributed)
	}
	restores := archive.restores
	if got := a.fetchSessionSet(context.Background(), archive, root, "macbook", fleetSnapshot, sessions, 1); got.Resumed != 1 {
		t.Fatalf("re-fetch = %+v, want the session resumed", got)
	}
	if archive.restores != restores {
		t.Errorf("recording a missing origin downloaded %d times", archive.restores-restores)
	}
	if found, unattributed, err = fetchedCorpus(context.Background(), root, adapters()); err != nil ||
		len(found) != 1 || unattributed != 0 || found[0].origin != "macbook" {
		t.Fatalf("a re-fetched tree was not re-attributed: %d sessions, %d unattributed, err %v", len(found), unattributed, err)
	}
}

// TestListNamesTheMachineEachSessionCameFrom is the operator-visible half: a
// listing that covers this machine and the fetched corpus states every row's
// machine, and a fetched macbook session is never reported as this host's.
func TestListNamesTheMachineEachSessionCameFrom(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	fetched := f.plantFetched("macbook", "aabbccdd", "Users/remote", sessionSpec{
		project:   "remote-project",
		stem:      "2026-09-09T10-11-12-000Z_00000000-0000-4000-8000-00000000000f",
		id:        "00000000-0000-4000-8000-00000000000f",
		title:     "Synthetic fetched session",
		workspace: "/Users/remote/workspace",
	})

	// Without --fetched the corpus is invisible and no row claims a machine:
	// a single-machine listing has none to distinguish.
	stdout, _ := f.ok("sessions", "list", "--json")
	local := decode[sessionsResult](t, stdout)
	if len(local.Sessions) != 3 {
		t.Fatalf("a local listing holds %d sessions, want 3", len(local.Sessions))
	}
	for _, row := range local.Sessions {
		if row.Host != nil {
			t.Errorf("a local listing attributed %s to %q", row.Selector, *row.Host)
		}
	}

	stdout, _ = f.ok("sessions", "list", "--fetched", "--json")
	fleet := decode[sessionsResult](t, stdout)
	if len(fleet.Sessions) != 4 {
		t.Fatalf("a fleet listing holds %d sessions, want 4: %+v", len(fleet.Sessions), fleet.Sessions)
	}
	hosts := map[string]string{}
	for _, row := range fleet.Sessions {
		if row.Host == nil {
			t.Fatalf("%s names no machine in a fleet listing", row.Selector)
		}
		hosts[row.Selector] = *row.Host
	}
	if hosts[fetched] != "macbook" {
		t.Errorf("the fetched session is attributed to %q, want macbook", hosts[fetched])
	}
	for selector, host := range hosts {
		if selector != fetched && host != testHostID {
			t.Errorf("local session %s is attributed to %q, want %q", selector, host, testHostID)
		}
	}

	// The human listing leads with the column, so the corpus can be read by
	// machine without --json.
	stdout, _ = f.ok("sessions", "list", "--fetched")
	if !strings.HasPrefix(stdout, "HOST") || !strings.Contains(stdout, "macbook") {
		t.Errorf("the fleet listing does not lead with the machine:\n%s", stdout)
	}

	// The fetched session is addressable by selector for a full description,
	// which is what makes the corpus analysable rather than merely listed.
	stdout, _ = f.ok("sessions", "inspect", "--fetched", fetched, "--json")
	res := decode[inspectResult](t, stdout)
	if res.Selector != fetched {
		t.Errorf("inspect resolved %q, want %q", res.Selector, fetched)
	}
	if !strings.Contains(res.PrimaryPath, filepath.Join("babel", "sessions")) {
		t.Errorf("inspect read the session from %q, want Babel's fetched area", res.PrimaryPath)
	}
}

// plantFetched materializes one session inside Babel's fetched-session area
// exactly as a fetch leaves it: the origin machine's absolute path mirrored
// beneath <data>/sessions/<safe selector>/<short snapshot id>, plus the origin
// record beside the tree. It returns the session's selector.
func (f *fixture) plantFetched(host, snapshot, originHome string, spec sessionSpec) string {
	f.t.Helper()
	selector := "omp/" + spec.project + "/" + spec.stem
	tree := filepath.Join(f.dataDir, "sessions", safeSessionDir(selector), snapshot)
	// writeSession writes the OMP layout below f.sessionsDir, and a fetched
	// tree holds that same layout one origin-path deep, so the fixture's own
	// writer is pointed at it rather than duplicated.
	saved := f.sessionsDir
	f.sessionsDir = filepath.Join(tree, filepath.FromSlash(originHome), ".omp", "agent", "sessions")
	f.writeSession(spec)
	f.sessionsDir = saved
	if err := writeFetchedOrigin(tree, fetchedOrigin{
		Host:       host,
		Selector:   selector,
		SnapshotID: strings.Repeat(snapshot, 8),
		FetchedAt:  "2026-09-09T13:00:00Z",
	}); err != nil {
		f.t.Fatal(err)
	}
	return selector
}

// TestFetchAllRefusesImpossibleCombinations pins the established idiom: a
// combination that cannot mean one thing is refused by the flags that made it
// impossible, never resolved by precedence into a corpus the operator did not
// ask for.
func TestFetchAllRefusesImpossibleCombinations(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "neither host nor fleet",
			args: []string{"sessions", "fetch-all"},
			want: "--host ID or --all-hosts",
		},
		{
			name: "one host and every host",
			args: []string{"sessions", "fetch-all", "--host", "macbook", "--all-hosts"},
			want: "use one or the other",
		},
		{
			name: "a snapshot without a host",
			args: []string{"sessions", "fetch-all", "--all-hosts", "--snapshot", "abcd1234"},
			want: "--snapshot names one snapshot",
		},
		{
			name: "no concurrency at all",
			args: []string{"sessions", "fetch-all", "--all-hosts", "--concurrency", "0"},
			want: "--concurrency must be at least 1",
		},
		{
			name: "more concurrency than a repository is driven with",
			args: []string{"sessions", "fetch-all", "--all-hosts", "--concurrency", "512"},
			want: "--concurrency 512 exceeds",
		},
		{
			name: "a fetched corpus and another host's archive",
			args: []string{"sessions", "list", "--fetched", "--host", "macbook"},
			want: "--fetched lists the sessions already materialized here",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := f.mustExit(exitUsage, tc.args...)
			if stdout != "" {
				t.Errorf("a rejected invocation wrote to stdout: %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("the refusal does not name %q:\n%s", tc.want, stderr)
			}
		})
	}
}

// TestFetchAllUsageDocumentsTheCorpusSurface keeps the help honest about the
// flags the command actually defines: the help is the only documentation an
// operator has at the terminal, and prose does not compile.
func TestFetchAllUsageDocumentsTheCorpusSurface(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.ok("sessions", "fetch-all", "-h")
	if stderr != "" {
		t.Errorf("help went to stderr: %q", stderr)
	}
	for _, want := range []string{"--host ID", "--all-hosts", "--concurrency N", "--snapshot ID", "resumed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the usage text does not document %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(sessionsUsage, "fetch-all") {
		t.Errorf("the noun's own help does not list fetch-all:\n%s", sessionsUsage)
	}
}

// TestOriginRecordSurvivesAndIsReadBackWhole checks the one piece of durable
// state this feature adds: what a fetch wrote is what discovery reads, and a
// record that names no host is treated as no record rather than as a session
// belonging to nobody.
func TestOriginRecordSurvivesAndIsReadBackWhole(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "sessions", "omp-project-sess", "11112222")
	want := fetchedOrigin{Host: "wsl-nixos", Selector: "omp/project/sess", SnapshotID: "abc", FetchedAt: "2026-09-09T13:00:00Z"}
	if err := writeFetchedOrigin(tree, want); err != nil {
		t.Fatalf("writeFetchedOrigin: %v", err)
	}
	got, err := readFetchedOrigin(tree)
	if err != nil {
		t.Fatalf("readFetchedOrigin: %v", err)
	}
	if got != want {
		t.Errorf("origin round-tripped as %+v, want %+v", got, want)
	}
	blank, err := json.Marshal(fetchedOrigin{Selector: "omp/project/sess"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originPath(tree), blank, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFetchedOrigin(tree); err == nil {
		t.Error("a record naming no host was accepted as attribution")
	}
}

// TestFetchAllMaterializesAnotherMachinesCorpus drives the shipped command
// against a real repository, which is the only way to check the half the fake
// cannot reach: host selection, snapshot selection, and identifying a whole
// snapshot's sessions from its file listing.
//
// The archive is pushed under another machine's identity and the local source
// tree is then removed, so every session the fetch recovers is one this machine
// never had — the situation the fleet corpus exists for, and the one where
// attributing a session to the fetching host would be silently wrong.
func TestFetchAllMaterializesAnotherMachinesCorpus(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push", "--host", "macbook")...)
	if err := os.RemoveAll(f.sessionsDir); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := f.ok(f.with("sessions", "fetch-all", "--host", "macbook", "--json")...)
	res := decode[fetchAllResult](t, stdout)
	if len(res.Hosts) != 1 || res.Hosts[0].Host != "macbook" {
		t.Fatalf("the batch covered %+v, want one host named macbook", res.Hosts)
	}
	if res.Sessions != 3 || res.Fetched != 3 || res.Resumed != 0 || res.Failed != 0 {
		t.Fatalf("the batch = %+v, want 3 sessions all fetched", res)
	}
	if !strings.Contains(stderr, "fetching 3 sessions archived by macbook") {
		t.Errorf("the batch did not narrate its progress:\n%s", stderr)
	}

	// Every recovered session is now discoverable, under macbook's identity
	// rather than this machine's, without the caller naming a single root.
	stdout, _ = f.ok("sessions", "list", "--fetched", "--json")
	listing := decode[sessionsResult](t, stdout)
	if len(listing.Sessions) != 3 {
		t.Fatalf("the fetched corpus listed %d sessions, want 3", len(listing.Sessions))
	}
	for _, row := range listing.Sessions {
		if row.Host == nil || *row.Host != "macbook" {
			t.Errorf("%s is attributed to %v, want macbook", row.Selector, row.Host)
		}
	}

	// Running it again is a resume: nothing is downloaded and nothing moves.
	before := treeDigest(t, filepath.Join(f.dataDir, "sessions"))
	stdout, _ = f.ok(f.with("sessions", "fetch-all", "--all-hosts", "--json")...)
	again := decode[fetchAllResult](t, stdout)
	if again.Resumed != 3 || again.Fetched != 0 {
		t.Fatalf("the second batch = %+v, want 3 resumed and nothing fetched", again)
	}
	if after := treeDigest(t, filepath.Join(f.dataDir, "sessions")); !reflect.DeepEqual(before, after) {
		t.Error("resuming rewrote the materialized corpus")
	}
}

// The loop draws from the fleet's corpus, not the machine it runs on.
//
// Fetching another host's sessions only pays off if an unattended run can
// reach them: the operator's dev machine holds a small fraction of the
// sessions the fleet produced, and a conductor that drew only local ones
// would rediscover that one machine's habits every night while the fetched
// corpus sat on disk unread. The serendipity rung's depth is where that is
// observable, because it reports what the next draw may choose from.
func TestConductorDrawsFromTheFetchedCorpus(t *testing.T) {
	f := newFixture(t)
	f.writeSession(sessionSpec{project: "local-project", stem: "2026-01-02T03-04-05-000Z_" + testUUID(1)})
	f.plantFetched("macbook", "ab", "/Users/alex",
		sessionSpec{project: "remote-project", stem: "2026-01-03T03-04-05-000Z_" + testUUID(2)})

	stdout, _ := f.ok("conductor", "status", "--json")
	status := decodeJSON[conductorStatusResult](t, stdout)

	var serendipity *conductorRungRow
	for i, rung := range status.Rungs {
		if rung.Name == string(conductor.RungSerendipity) {
			serendipity = &status.Rungs[i]
		}
	}
	if serendipity == nil {
		t.Fatalf("no serendipity rung in %+v", status.Rungs)
	}
	if serendipity.Waiting != 2 {
		t.Errorf("the floor may draw from %d sessions, want the local one and the fetched one: %q",
			serendipity.Waiting, serendipity.Note)
	}
}
