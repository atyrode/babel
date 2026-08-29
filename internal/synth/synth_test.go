package synth_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/synth"
)

// fingerprint reduces a generated tree to the pairs that define it: every
// regular file's root-relative slash path and the digest of its bytes. Modes
// and timestamps are excluded because a corpus is defined by its content, and
// the filesystem is free to record when it was written.
func fingerprint(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel)+" "+hex.EncodeToString(h.Sum(nil)))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	slices.Sort(out)
	return out
}

func TestGenerateIsDeterministicPerSeed(t *testing.T) {
	profile := synth.DefaultProfile()

	first, second := t.TempDir(), t.TempDir()
	if _, err := synth.Generate(first, profile); err != nil {
		t.Fatalf("generate first: %v", err)
	}
	if _, err := synth.Generate(second, profile); err != nil {
		t.Fatalf("generate second: %v", err)
	}
	a, b := fingerprint(t, first), fingerprint(t, second)
	if !slices.Equal(a, b) {
		for i := range max(len(a), len(b)) {
			var x, y string
			if i < len(a) {
				x = a[i]
			}
			if i < len(b) {
				y = b[i]
			}
			if x != y {
				t.Fatalf("tree differs at entry %d:\n first: %s\nsecond: %s", i, x, y)
			}
		}
		t.Fatalf("tree differs: %d entries versus %d", len(a), len(b))
	}
	if len(a) == 0 {
		t.Fatal("generated nothing")
	}

	other := profile
	other.Seed++
	third := t.TempDir()
	if _, err := synth.Generate(third, other); err != nil {
		t.Fatalf("generate third: %v", err)
	}
	if slices.Equal(a, fingerprint(t, third)) {
		t.Fatal("a different seed produced an identical tree, so the seed does not select the corpus")
	}
}

func TestCorpusDescribesWhatWasWritten(t *testing.T) {
	root := t.TempDir()
	corpus, err := synth.Generate(root, synth.DefaultProfile())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if corpus.Root != root {
		t.Errorf("corpus root = %q, want %q", corpus.Root, root)
	}
	for _, s := range corpus.Sessions {
		info, err := os.Stat(s.Path)
		if err != nil {
			t.Errorf("%s session %s: %v", s.Harness, s.ID, err)
			continue
		}
		if info.Size() != s.Bytes {
			t.Errorf("%s session %s: on disk %d bytes, corpus claims %d", s.Harness, s.ID, info.Size(), s.Bytes)
		}
		if !strings.HasPrefix(s.Path, root) {
			t.Errorf("%s session %s: path %q escapes the corpus root", s.Harness, s.ID, s.Path)
		}
		var artifactBytes int64
		for _, a := range append(slices.Clone(s.Artifacts), s.HiddenArtifacts...) {
			info, err := os.Stat(a)
			if err != nil {
				t.Errorf("%s session %s artifact: %v", s.Harness, s.ID, err)
				continue
			}
			if slices.Contains(s.Artifacts, a) {
				artifactBytes += info.Size()
			}
		}
		if artifactBytes != s.ArtifactBytes {
			t.Errorf("%s session %s: artifacts hold %d bytes, corpus claims %d",
				s.Harness, s.ID, artifactBytes, s.ArtifactBytes)
		}
		if !slices.IsSorted(s.Artifacts) || !slices.IsSorted(s.BlobRefs) {
			t.Errorf("%s session %s: artifacts and references must be reported ascending", s.Harness, s.ID)
		}
		for _, ref := range s.UnresolvedRefs {
			if !slices.Contains(s.BlobRefs, ref) {
				t.Errorf("%s session %s: unresolved %s is not among the session's references", s.Harness, s.ID, ref)
			}
		}
	}

	for _, b := range corpus.Blobs {
		switch {
		case b.Path == "":
			continue
		case b.DigestMismatch:
			content, err := os.ReadFile(b.Path)
			if err != nil {
				t.Errorf("mismatched blob %s: %v", b.Ref, err)
				continue
			}
			sum := sha256.Sum256(content)
			if got := "blob:sha256:" + hex.EncodeToString(sum[:]); got == b.Ref {
				t.Errorf("blob %s claims a digest mismatch but its bytes hash to its name", b.Ref)
			}
		default:
			content, err := os.ReadFile(b.Path)
			if err != nil {
				t.Errorf("blob %s: %v", b.Ref, err)
				continue
			}
			sum := sha256.Sum256(content)
			// The store may name a blob with an extension; the digest, not
			// the name, is what has to match.
			if got := "blob:sha256:" + hex.EncodeToString(sum[:]); got != b.Ref {
				t.Errorf("blob at %s hashes to %s, stored as %s", b.Path, got, b.Ref)
			}
		}
	}

	files, bytes := 0, int64(0)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if files != corpus.Files || bytes != corpus.Bytes {
		t.Errorf("tree holds %d files and %d bytes, corpus claims %d and %d",
			files, bytes, corpus.Files, corpus.Bytes)
	}
	t.Logf("default profile wrote %d files and %d bytes", corpus.Files, corpus.Bytes)
}

func TestEveryPlannedDefectIsPlaced(t *testing.T) {
	profile := synth.DefaultProfile()
	corpus, err := synth.Generate(t.TempDir(), profile)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var oversized, malformed, torn, sparse, moved, unresolved int
	perHarness := map[string]int{}
	for _, s := range corpus.Sessions {
		oversized += s.OversizedRecords
		malformed += s.MalformedRecords
		unresolved += len(s.UnresolvedRefs)
		if s.TornFinalLine {
			torn++
		}
		if s.SparseHeader {
			sparse++
		}
		if s.WorkspaceMoved {
			moved++
		}
		if s.OversizedRecords+s.MalformedRecords > 0 || s.TornFinalLine {
			perHarness[s.Harness]++
		}
	}

	for _, tc := range []struct {
		name      string
		got, want int
	}{
		{"sessions", len(corpus.Sessions), profile.OMPSessions + profile.CodexSessions + profile.ClaudeSessions},
		{"oversized records", oversized, profile.OversizedLines},
		{"malformed records", malformed, profile.MalformedLines},
		{"torn final lines", torn, profile.TornFinalLines},
		{"unresolved references", unresolved, profile.UnresolvedBlobRefs},
		{"sparse headers", sparse, 3},
		{"moved workspaces", moved, 1},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: placed %d, profile asks for %d", tc.name, tc.got, tc.want)
		}
	}
	// Defects that all landed on one harness would leave two adapters
	// untested, which is the failure this spread exists to prevent.
	for _, harness := range []string{synth.HarnessOMP, synth.HarnessCodex, synth.HarnessClaude} {
		if perHarness[harness] == 0 {
			t.Errorf("no %s session carries a defect", harness)
		}
	}
}

func TestGenerateRejectsProfilesItCannotHonour(t *testing.T) {
	valid := synth.DefaultProfile()
	for _, tc := range []struct {
		name   string
		mutate func(*synth.Profile)
		want   string
	}{
		{"no sessions", func(p *synth.Profile) { p.OMPSessions, p.CodexSessions, p.ClaudeSessions = 0, 0, 0 }, "no sessions"},
		{"negative sessions", func(p *synth.Profile) { p.CodexSessions = -1 }, "negative session count"},
		{"no buckets", func(p *synth.Profile) { p.SizeBuckets = nil }, "no size buckets"},
		{"empty bucket", func(p *synth.Profile) { p.SizeBuckets = []synth.SizeBucket{{Bytes: 0, Weight: 1}} }, "non-positive size"},
		{"unweighted buckets", func(p *synth.Profile) { p.SizeBuckets = []synth.SizeBucket{{Bytes: 1024}} }, "every bucket weight is zero"},
		{"over-claimed buckets", func(p *synth.Profile) { p.SizeBuckets = []synth.SizeBucket{{Bytes: 1024, Count: 999}} }, "claim 999 sessions"},
		{"too many torn lines", func(p *synth.Profile) { p.TornFinalLines = 999 }, "exceeds"},
		{"negative defects", func(p *synth.Profile) { p.MalformedLines = -1 }, "negative defect count"},
		{"descending artifact range", func(p *synth.Profile) { p.ArtifactsPerSession = [2]int{4, 1} }, "ascending"},
		{"negative blobs", func(p *synth.Profile) { p.BlobCount = -1 }, "negative blob count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.mutate(&p)
			dir := t.TempDir()
			if _, err := synth.Generate(dir, p); err == nil {
				t.Fatal("generate accepted an impossible profile")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("a rejected profile wrote %d entries (err %v)", len(entries), err)
			}
		})
	}
}

// TestLargeProfileGeneratesInBoundedMemory is the honest form of the streaming
// claim: a generator that assembled files in memory would allocate at least as
// much as it wrote. Total allocation is measured rather than heap occupancy,
// because occupancy alone could be explained by a garbage collection that
// happened to run at the right moment.
func TestLargeProfileGeneratesInBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("generating a corpus above 64 MiB is too slow for -short")
	}
	profile := synth.LargeProfile()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	corpus, err := synth.Generate(t.TempDir(), profile)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	runtime.ReadMemStats(&after)

	var largest int64
	for _, s := range corpus.Sessions {
		largest = max(largest, s.Bytes)
	}
	if largest <= 64<<20 {
		t.Fatalf("largest primary log is %d bytes; the large profile must exceed 64 MiB", largest)
	}

	allocated := int64(after.TotalAlloc - before.TotalAlloc)
	// One sixteenth is far above what streaming costs and far below what
	// buffering a single 68 MiB log would.
	if limit := corpus.Bytes / 16; allocated > limit {
		t.Errorf("generating %d bytes allocated %d, above the %d streaming budget",
			corpus.Bytes, allocated, limit)
	}
	t.Logf("generated %d files, %d bytes, largest log %d bytes, allocated %d bytes",
		corpus.Files, corpus.Bytes, largest, allocated)
}

// TestExtremeProfileGeneratesInBoundedMemory is the same property at the size
// that actually matters. Real harness sessions reach a little over 300 MB in
// one file, and this profile exceeds that, so an implementation that buffers a
// whole log dies here rather than in production. The allocation budget is
// absolute rather than proportional: streaming costs a fixed handful of
// buffers no matter how large the corpus is, so a limit that grew with the
// corpus would pass a design that scales with file size.
func TestExtremeProfileGeneratesInBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("generating a corpus above 320 MiB is far too slow for -short")
	}
	profile := synth.ExtremeProfile()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	corpus, err := synth.Generate(t.TempDir(), profile)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	runtime.ReadMemStats(&after)

	var largest int64
	for _, s := range corpus.Sessions {
		largest = max(largest, s.Bytes)
	}
	if largest <= 320<<20 {
		t.Fatalf("largest primary log is %d bytes; the extreme profile must exceed 320 MiB", largest)
	}

	allocated := int64(after.TotalAlloc - before.TotalAlloc)
	if limit := int64(8 << 20); allocated > limit {
		t.Errorf("generating %d bytes allocated %d, above the %d absolute streaming budget",
			corpus.Bytes, allocated, limit)
	}
	t.Logf("generated %d files, %d bytes, largest log %d bytes, allocated %d bytes",
		corpus.Files, corpus.Bytes, largest, allocated)
}
