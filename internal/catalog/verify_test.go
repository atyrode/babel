package catalog

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

func mustVerify(t *testing.T, st objectstore.Store, deep bool, hosts ...string) *Report {
	t.Helper()
	rep, err := Verify(context.Background(), st, hosts, deep)
	if err != nil {
		t.Fatalf("Verify(deep=%t): %v", deep, err)
	}
	return rep
}

func hostReport(t *testing.T, rep *Report, hostID string) HostReport {
	t.Helper()
	for _, h := range rep.Hosts {
		if h.HostID == hostID {
			return h
		}
	}
	t.Fatalf("no report for host %s", hostID)
	return HostReport{}
}

func joined(msgs []string) string { return strings.Join(msgs, "\n") }

func requireContains(t *testing.T, what string, msgs []string, want string) {
	t.Helper()
	if !strings.Contains(joined(msgs), want) {
		t.Errorf("%s do not mention %q:\n%s", what, want, joined(msgs))
	}
}

func TestVerifyCleanArchive(t *testing.T) {
	f, _, _ := threeHarnessArchive(t)

	for _, deep := range []bool{false, true} {
		rep := mustVerify(t, f.st, deep)
		if !rep.OK() {
			t.Fatalf("deep=%t: clean archive reported errors: %+v", deep, rep.Hosts)
		}
		a := hostReport(t, rep, "host-a")
		if a.Records != 2 || a.Generations != 2 {
			t.Errorf("deep=%t: host-a records=%d generations=%d, want 2 and 2", deep, a.Records, a.Generations)
		}
		if a.Revisions != 3 {
			t.Errorf("deep=%t: host-a revisions=%d, want 3", deep, a.Revisions)
		}
		// One index, at least one segment, three distinct payload objects.
		if a.Objects < 5 {
			t.Errorf("deep=%t: host-a objects=%d, too few checked", deep, a.Objects)
		}
		if len(a.Warnings) != 0 {
			t.Errorf("deep=%t: clean archive warned: %v", deep, a.Warnings)
		}
	}
}

func TestVerifyDefaultCatchesMissingObject(t *testing.T) {
	f, ompFull, _ := threeHarnessArchive(t)
	f.remove(archive.CASKey(ompFull.Object.Digest))

	rep := mustVerify(t, f.st, false)
	if rep.OK() {
		t.Fatal("missing payload object reported OK")
	}
	a := hostReport(t, rep, "host-a")
	requireContains(t, "errors", a.Errors, "payload of "+ompFull.RevisionKey)
	requireContains(t, "errors", a.Errors, "does not exist")
	if h := hostReport(t, rep, "host-b"); len(h.Errors) != 0 {
		t.Errorf("host-b implicated by host-a's damage: %v", h.Errors)
	}
}

func TestVerifyDefaultCatchesSizeMismatch(t *testing.T) {
	f, ompFull, _ := threeHarnessArchive(t)
	f.truncateObject(archive.CASKey(ompFull.Object.Digest))

	rep := mustVerify(t, f.st, false)
	if rep.OK() {
		t.Fatal("truncated payload object reported OK")
	}
	requireContains(t, "errors", hostReport(t, rep, "host-a").Errors,
		"want "+strconv.FormatInt(ompFull.Object.Size, 10))
}

func TestVerifyDefaultCatchesTamperedCommitRecord(t *testing.T) {
	f, _, _ := threeHarnessArchive(t)
	c := mustLoad(t, f.st)
	head, _ := c.Host("host-a")
	f.flip(head.CommitKey)

	rep := mustVerify(t, f.st, false)
	if rep.OK() {
		t.Fatal("tampered commit record reported OK")
	}
	a := hostReport(t, rep, "host-a")
	requireContains(t, "errors", a.Errors, "key claims")
	if a.Generations != 1 {
		t.Errorf("host-a verified generations=%d, want only the intact one", a.Generations)
	}
	// The archive stays usable at the older verified generation.
	if h, _ := mustLoad(t, f.st).Host("host-a"); h.Generation != 1 {
		t.Errorf("catalog exposes g%d after the head record was tampered, want g1", h.Generation)
	}
}

func TestVerifyWarnsOnDuplicateGenerationRecords(t *testing.T) {
	f := newFixture(t)
	e := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("alpha\n"))
	first := f.commit("host-a", 1, []archive.ManifestEntry{e})
	// A second, differently-worded record at the same generation: what
	// lease-less concurrent publication looks like on the remote.
	second := f.publish("host-a", 1, []archive.ManifestEntry{e}, publishOpts{babelVersion: "other-writer", noHint: true})
	if first == second {
		t.Fatal("fixture produced identical commit keys; the digest suffix must differ")
	}

	rep := mustVerify(t, f.st, false)
	a := hostReport(t, rep, "host-a")
	if !rep.OK() {
		t.Errorf("duplicate records must be a warning, not an error: %v", a.Errors)
	}
	if a.Records != 2 {
		t.Errorf("records=%d, want 2", a.Records)
	}
	requireContains(t, "warnings", a.Warnings, "generation 1 has 2 commit records")

	// The deterministic winner is the highest key, and readers agree on it.
	winner := max(first, second)
	requireContains(t, "warnings", a.Warnings, winner+" wins")
	if h, _ := mustLoad(t, f.st).Host("host-a"); h.CommitKey != winner {
		t.Errorf("catalog exposed %s, want the deterministic winner %s", h.CommitKey, winner)
	}
}

func TestVerifyWarnsOnStaleHint(t *testing.T) {
	f, _, _ := threeHarnessArchive(t)
	f.writeHint("host-a", 7, archive.ObjectRef{Digest: archive.DigestBytes([]byte("nope")), Size: 3})

	rep := mustVerify(t, f.st, false)
	a := hostReport(t, rep, "host-a")
	if !rep.OK() {
		t.Errorf("a bad hint must not make the archive invalid: %v", a.Errors)
	}
	requireContains(t, "warnings", a.Warnings, "latest hint names generation 7")
	requireContains(t, "warnings", a.Warnings, "unusable commit record")
}

func TestVerifyDeepCatchesBitFlipDefaultMisses(t *testing.T) {
	f, ompFull, _ := threeHarnessArchive(t)
	// Same size, different bytes: presence and size checks cannot see it.
	f.flip(archive.CASKey(ompFull.Object.Digest))

	if rep := mustVerify(t, f.st, false); !rep.OK() {
		t.Fatalf("default tier is presence-and-size only, but reported: %+v", rep.Hosts)
	}
	rep := mustVerify(t, f.st, true)
	if rep.OK() {
		t.Fatal("deep tier missed a bit-flipped payload")
	}
	a := hostReport(t, rep, "host-a")
	requireContains(t, "errors", a.Errors, "has digest sha256:")
	// The delta revision's reassembly is broken by its damaged ancestor.
	requireContains(t, "errors", a.Errors, "reassembled content has digest")
	if h := hostReport(t, rep, "host-b"); len(h.Errors) != 0 {
		t.Errorf("host-b implicated: %v", h.Errors)
	}
}

func TestVerifyReportsUnresolvableChain(t *testing.T) {
	f := newFixture(t)
	base := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("alpha-base\n"))
	delta := f.delta(base, 2, []byte("alpha-tail\n"))
	// Publishing the delta without its parent leaves a chain that cannot be
	// resolved; it must be reported, never partially reassembled.
	f.commit("host-a", 2, []archive.ManifestEntry{delta})

	rep := mustVerify(t, f.st, false)
	if rep.OK() {
		t.Fatal("orphaned append-delta revision reported OK")
	}
	requireContains(t, "errors", hostReport(t, rep, "host-a").Errors, "is absent from its generation")
}

func TestVerifyReportsForeignEntryAndUnsafeArtifactPath(t *testing.T) {
	f := newFixture(t)
	mine := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("alpha\n"))
	mine.Artifacts = []archive.FileRef{f.artifact("../escape.md", []byte("escape\n"))}
	foreign := f.full("omp", "host-b", "sessions/0002-beta", 1, []byte("beta\n"))
	f.commit("host-a", 1, []archive.ManifestEntry{mine, foreign})

	rep := mustVerify(t, f.st, false)
	a := hostReport(t, rep, "host-a")
	requireContains(t, "errors", a.Errors, "does not own this generation")
	requireContains(t, "warnings", a.Warnings, "cannot be materialized")

	// A host publishing another host's session key never leaks into the
	// catalog.
	c := mustLoad(t, f.st)
	if _, ok := c.Session(foreign.SessionKey); ok {
		t.Error("foreign session merged into the catalog")
	}
	if h, _ := c.Host("host-a"); h.Revisions != 1 || len(h.Anomalies) == 0 {
		t.Errorf("host-a = %d revisions, anomalies %v; want 1 revision and a recorded anomaly", h.Revisions, h.Anomalies)
	}
}

func TestVerifyReportsMiscountedIndex(t *testing.T) {
	f := newFixture(t)
	e := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("alpha\n"))
	f.publish("host-a", 1, []archive.ManifestEntry{e}, publishOpts{skewIndex: 1})

	rep := mustVerify(t, f.st, false)
	if rep.OK() {
		t.Fatal("index disagreeing with its own segments reported OK")
	}
	requireContains(t, "errors", hostReport(t, rep, "host-a").Errors, "generation index declares")

	// The generation's bytes still verify, so it stays readable.
	if h, _ := mustLoad(t, f.st).Host("host-a"); h.Generation != 1 || len(h.Anomalies) == 0 {
		t.Errorf("host-a = g%d anomalies %v, want g1 with a recorded anomaly", h.Generation, h.Anomalies)
	}
}
