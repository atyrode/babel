// Package synth generates synthetic OMP, Codex, and Claude Code corpora on
// disk so analysis code can be exercised against the shapes real transcripts
// exhibit without ever placing real transcript bytes, paths, hostnames, or
// identities in this repository.
//
// A corpus is described by a Profile of counts and sizes only; the content is
// deliberately obvious filler. Two properties make the generated trees usable
// as test fixtures rather than as decoration:
//
// Determinism. One Profile.Seed writes one tree: the same seed produces the
// same relative paths holding the same bytes in any directory, so two
// generations can be compared byte for byte and a failure reproduces from the
// seed alone. Nothing consults the wall clock, the environment, or a global
// random source.
//
// Streaming. Records are formatted into a reused scratch buffer and written
// through a fixed-size buffered writer, so a profile whose largest primary log
// exceeds 64 MiB still generates in memory bounded by one record rather than by
// one file — which is the property the readers under test must also have.
//
// The trees match the layouts internal/adapter/{omp,codex,claude} discover,
// including the awkward cases those adapters already handle: path components
// with spaces and leading dashes, non-session files beside session logs, nested
// artifact trees, oversized single records, records that are not JSON, logs torn
// mid-write, and references that deliberately do not resolve. Every deliberate
// defect is planned before generation and reported in Corpus, so a test asserts
// against what was written instead of re-deriving it from the tree.
package synth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	// HarnessOMP, HarnessCodex, and HarnessClaude are the harness names the
	// adapters report. They are repeated here so a caller can group
	// Corpus.Sessions without importing an adapter package.
	HarnessOMP    = "omp"
	HarnessCodex  = "codex"
	HarnessClaude = "claude"

	// OversizedRecordBytes is the payload size of a deliberately oversized
	// record. It must clear two independent bars. It exceeds every Phase A
	// reader's per-record budget — Codex bounds one record at 4 MiB, Claude
	// Code at 1 MiB — so the profile knob means the same thing to every
	// harness: this record must be degraded, never parsed. A smaller value
	// would silently become an ordinary record for one harness and a defect
	// for another. It also exceeds the largest record real harness logs are
	// observed to carry, which reaches into the tens of megabytes when a tool
	// result or an embedded payload lands in one JSONL line. A fixture whose
	// extremes are smaller than production is a fixture that lets production
	// break the reader.
	OversizedRecordBytes = 16 << 20
)

const (
	// logBufferBytes is the write buffer behind every primary log. It bounds
	// the memory one generated file costs regardless of the file's size.
	logBufferBytes = 64 << 10

	// bodyFillBytes is the filler payload of one ordinary body record, chosen
	// so a small session is a handful of records and a large one is many
	// rather than a few enormous ones.
	bodyFillBytes = 512

	// pcgStreamSalt derives the second PCG word from the seed, so a caller
	// only has to choose one number and still gets both generator words.
	pcgStreamSalt = 0x9E3779B97F4A7C15

	// dirPerm and filePerm are the modes of everything generated. A corpus is
	// disposable test material, never operator data.
	dirPerm  = 0o755
	filePerm = 0o644
)

// baseTime anchors every generated timestamp. It is a fixed synthetic instant
// rather than time.Now, because a corpus that changed with the clock could not
// be compared across generations.
var baseTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// harnessOrder is the order harnesses are generated and reported in. It is
// fixed so Corpus.Sessions has a stable shape.
var harnessOrder = [...]string{HarnessOMP, HarnessCodex, HarnessClaude}

// SizeBucket is one class of primary-log size. Count sessions are placed in the
// bucket outright and the rest of the corpus is drawn across buckets in
// proportion to Weight, which is what reproduces the real world's extreme skew:
// thousands of small logs and a handful of very large ones cannot be expressed
// by a mean.
type SizeBucket struct {
	// Bytes is the target size of a primary log drawn from this bucket. The
	// written log is at least this large; it overshoots by less than one
	// record because records are whole.
	Bytes int64
	// Weight is this bucket's relative share of the sessions Count does not
	// claim. Zero excludes the bucket from the weighted draw.
	Weight int
	// Count is a number of sessions placed in this bucket regardless of
	// Weight, which is how a profile guarantees it contains one specific
	// extreme rather than hoping the draw produces it.
	Count int
}

// Profile describes the shape of a corpus to generate. All fields are counts
// and sizes, never content: a profile can be committed, printed, or varied in
// a test without ever describing what a transcript says.
type Profile struct {
	// Seed selects the corpus. Everything else being equal, one seed always
	// produces one tree.
	Seed int64

	OMPSessions    int
	CodexSessions  int
	ClaudeSessions int

	// SizeBuckets is the size distribution primary logs are drawn from. It
	// must hold at least one bucket.
	SizeBuckets []SizeBucket

	// OversizedLines is the number of records exceeding the reader's line
	// budget, spread across harnesses.
	OversizedLines int
	// TornFinalLines is the number of sessions whose last record is truncated
	// mid-write, with no trailing newline — what a reader observes when it
	// reads a log a harness is still appending to.
	TornFinalLines int
	// MalformedLines is the number of records that are not valid JSON. A torn
	// final line is reported separately: a reader sees it as one more
	// unparsable record, but the two causes are worth telling apart.
	MalformedLines int

	// ArtifactsPerSession is the inclusive [min, max] number of artifact files
	// generated beside each session.
	ArtifactsPerSession [2]int

	// BlobCount is the number of blobs written to the OMP content-addressed
	// store. The last one is deliberately left unreferenced, because a real
	// store always holds more than any one session's closure.
	BlobCount int
	// UnresolvedBlobRefs is the number of references that deliberately do not
	// resolve, spread between OMP blob references and Codex attachment
	// references. The first OMP one is a stored blob whose bytes do not match
	// its name, so digest verification is exercised and not just absence.
	UnresolvedBlobRefs int
}

// DefaultProfile is a corpus covering every hard case the adapters handle,
// small enough to generate and read inside a unit test. Its bulk is the
// oversized records, which cannot be small and still be oversized.
func DefaultProfile() Profile {
	return Profile{
		Seed:           20260102,
		OMPSessions:    6,
		CodexSessions:  5,
		ClaudeSessions: 5,
		SizeBuckets: []SizeBucket{
			{Bytes: 2 << 10, Weight: 70},
			{Bytes: 16 << 10, Weight: 25},
			{Bytes: 128 << 10, Weight: 5},
			{Bytes: 512 << 10, Count: 1},
		},
		OversizedLines:      3,
		TornFinalLines:      3,
		MalformedLines:      6,
		ArtifactsPerSession: [2]int{0, 4},
		BlobCount:           8,
		UnresolvedBlobRefs:  4,
	}
}

// LargeProfile is DefaultProfile's shape with one primary log above 64 MiB, so
// a reader that quietly slurps a whole file into memory fails here instead of
// in production. It is too slow for a default test run; guard it with
// testing.Short.
func LargeProfile() Profile {
	p := DefaultProfile()
	p.Seed = 20260203
	p.OMPSessions = 3
	p.CodexSessions = 3
	p.ClaudeSessions = 3
	p.SizeBuckets = []SizeBucket{
		{Bytes: 4 << 10, Weight: 80},
		{Bytes: 1 << 20, Weight: 20},
		{Bytes: 68 << 20, Count: 1},
	}
	p.OversizedLines = 1
	p.TornFinalLines = 1
	p.MalformedLines = 2
	p.ArtifactsPerSession = [2]int{0, 2}
	p.BlobCount = 6
	p.UnresolvedBlobRefs = 2
	return p
}

// ExtremeProfile exceeds the largest artifacts real harness logs are observed
// to produce: one primary log past 320 MiB, where observed sessions reach a
// little over 300 MB, and one record at the OversizedRecordBytes ceiling. This
// is the case that kills a non-streaming implementation outright rather than
// merely slowing it, so its bounded-memory assertion is the one that matters.
// Far too slow for a default run; guard it with testing.Short.
func ExtremeProfile() Profile {
	p := LargeProfile()
	p.Seed = 20260304
	p.OMPSessions = 2
	p.CodexSessions = 1
	p.ClaudeSessions = 1
	p.SizeBuckets = []SizeBucket{
		{Bytes: 4 << 10, Weight: 100},
		{Bytes: 320 << 20, Count: 1},
	}
	p.OversizedLines = 1
	p.TornFinalLines = 1
	p.MalformedLines = 1
	p.ArtifactsPerSession = [2]int{0, 1}
	p.BlobCount = 2
	p.UnresolvedBlobRefs = 1
	return p
}

// Defects is the deliberate damage one session carries.
type Defects struct {
	// OversizedRecords counts records whose payload is OversizedRecordBytes.
	OversizedRecords int
	// MalformedRecords counts records that are deliberately not JSON,
	// excluding any torn final line.
	MalformedRecords int
	// TornFinalLine reports a log truncated mid-record with no trailing
	// newline. Readers count it as one more record and fail to parse it.
	TornFinalLine bool
	// SparseHeader reports header records that deliberately carry no title, no
	// workspace, and no timestamp, which is what makes an adapter emit
	// completeness reasons instead of synthesizing values.
	SparseHeader bool
	// WorkspaceMoved reports a Claude Code transcript recording two distinct
	// cwd values, a conflict its adapter reports rather than resolves.
	WorkspaceMoved bool
}

// Session is one generated session as the generator planned and wrote it.
type Session struct {
	Harness string
	// ID is the harness-native session identifier written inside the log, not
	// an adapter source id: composing source identity is the adapter's
	// business, so a test matches generated sessions to discovered ones by
	// Path and never has to reimplement an adapter's id rules.
	ID string
	// Path is the absolute path of the primary log.
	Path string
	// Bytes is the primary log's size on disk; Records is how many records
	// were written to it, counting oversized, malformed, and torn ones.
	Bytes   int64
	Records int

	// Artifacts are the absolute paths of the artifact files an adapter is
	// expected to report for this session, ascending.
	Artifacts []string
	// HiddenArtifacts are dot-prefixed artifact files an adapter deliberately
	// skips, listed so a test can tell a deliberate omission from an oversight.
	HiddenArtifacts []string
	ArtifactBytes   int64

	// BlobRefs are the references this session's log and artifacts embed, in
	// the exact form its adapter reports: "blob:sha256:<hex>" for OMP,
	// "attachments/<id>" for Codex. Claude Code declares no references.
	BlobRefs []string
	// UnresolvedRefs is the subset of BlobRefs that deliberately does not
	// resolve.
	UnresolvedRefs []string

	Defects
}

// Blob is one entry planned for the OMP content-addressed store.
type Blob struct {
	// Ref is the persisted reference form, "blob:sha256:<64 hex>".
	Ref string
	// Path is where the bytes were written, or "" when the blob is
	// deliberately absent from the store.
	Path string
	Size int64
	// Referenced reports whether any session's closure names this blob.
	Referenced bool
	// DigestMismatch reports a blob stored under a name its bytes do not hash
	// to, which a verifying reader must treat as unresolved rather than as
	// content.
	DigestMismatch bool
}

// Corpus reports what Generate wrote. It is complete enough that a test never
// needs to walk the tree to learn what should be there.
type Corpus struct {
	// Root is the absolute directory the corpus was written into.
	Root string
	Seed int64

	// OMPSessionsRoot is the root to hand the OMP adapter's Discover;
	// OMPBlobStore is its sibling content-addressed store.
	OMPSessionsRoot string
	OMPBlobStore    string

	// CodexRoot is the root to hand the Codex adapter's Discover. Codex keeps
	// two host-level state files rather than per-session ones, and its adapter
	// discovers history.jsonl as one additional session.
	CodexRoot        string
	CodexHistoryPath string
	CodexIndexPath   string
	// CodexUnreferencedAttachment is an attachment directory no record names,
	// present because a real attachments tree outlives the messages that
	// referenced it.
	CodexUnreferencedAttachment string

	// ClaudeRoot is the root to hand the Claude Code adapter's Discover.
	ClaudeRoot string

	// Sessions holds every generated session, ordered by harness
	// (omp, codex, claude) then by generation order within the harness. The
	// Codex host-state session is not listed: it is not a generated session
	// but a host-level file, named by CodexHistoryPath.
	Sessions []Session
	// Blobs holds every planned blob, including the ones deliberately absent
	// from the store.
	Blobs []Blob

	// Files and Bytes total everything written, including artifacts, blobs,
	// host-state files, and the non-session files placed to be ignored.
	Files int
	Bytes int64
}

// Find returns the generated session with the given primary-log path, or nil.
// It is how a test moves from a discovered session back to what was planned.
func (c *Corpus) Find(primaryPath string) *Session {
	for i := range c.Sessions {
		if c.Sessions[i].Path == primaryPath {
			return &c.Sessions[i]
		}
	}
	return nil
}

// Generate writes a corpus described by p into root, creating it if needed, and
// reports what it wrote. Existing files are not removed: callers pass a fresh
// directory.
func Generate(root string, p Profile) (*Corpus, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("synth: resolve root %s: %w", root, err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("synth: create root %s: %w", abs, err)
	}

	g := &generator{
		p:   p,
		rng: rand.New(rand.NewPCG(uint64(p.Seed), uint64(p.Seed)^pcgStreamSalt)),
	}
	g.corpus.Root = abs
	g.corpus.Seed = p.Seed
	g.corpus.OMPSessionsRoot = filepath.Join(abs, HarnessOMP, "agent", "sessions")
	g.corpus.OMPBlobStore = filepath.Join(abs, HarnessOMP, "agent", "blobs")
	g.corpus.CodexRoot = filepath.Join(abs, HarnessCodex)
	g.corpus.CodexHistoryPath = filepath.Join(g.corpus.CodexRoot, "history.jsonl")
	g.corpus.CodexIndexPath = filepath.Join(g.corpus.CodexRoot, "session_index.jsonl")
	g.corpus.ClaudeRoot = filepath.Join(abs, HarnessClaude)

	g.plan()
	if err := g.writeBlobs(); err != nil {
		return nil, err
	}
	if err := g.generateOMP(); err != nil {
		return nil, err
	}
	if err := g.generateCodex(); err != nil {
		return nil, err
	}
	if err := g.generateClaude(); err != nil {
		return nil, err
	}
	return &g.corpus, nil
}

// validate rejects a profile that cannot be honoured, rather than silently
// generating something else: a fixture that quietly differs from its profile
// is worse than no fixture.
func (p Profile) validate() error {
	total := p.OMPSessions + p.CodexSessions + p.ClaudeSessions
	switch {
	case p.OMPSessions < 0 || p.CodexSessions < 0 || p.ClaudeSessions < 0:
		return fmt.Errorf("synth: negative session count (omp=%d codex=%d claude=%d)",
			p.OMPSessions, p.CodexSessions, p.ClaudeSessions)
	case total == 0:
		return fmt.Errorf("synth: profile generates no sessions")
	case len(p.SizeBuckets) == 0:
		return fmt.Errorf("synth: profile has no size buckets")
	case p.OversizedLines < 0 || p.TornFinalLines < 0 || p.MalformedLines < 0:
		return fmt.Errorf("synth: negative defect count")
	case p.TornFinalLines > total:
		return fmt.Errorf("synth: %d torn final lines exceeds %d sessions", p.TornFinalLines, total)
	case p.ArtifactsPerSession[0] < 0 || p.ArtifactsPerSession[1] < p.ArtifactsPerSession[0]:
		return fmt.Errorf("synth: artifacts per session %v is not an ascending non-negative range", p.ArtifactsPerSession)
	case p.BlobCount < 0 || p.UnresolvedBlobRefs < 0:
		return fmt.Errorf("synth: negative blob count")
	}

	exact, weight := 0, 0
	for i, b := range p.SizeBuckets {
		if b.Bytes <= 0 {
			return fmt.Errorf("synth: size bucket %d has non-positive size %d", i, b.Bytes)
		}
		if b.Weight < 0 || b.Count < 0 {
			return fmt.Errorf("synth: size bucket %d has a negative weight or count", i)
		}
		exact += b.Count
		weight += b.Weight
	}
	if exact > total {
		return fmt.Errorf("synth: size buckets claim %d sessions outright but the profile has %d", exact, total)
	}
	if exact < total && weight == 0 {
		return fmt.Errorf("synth: %d sessions must be drawn by weight but every bucket weight is zero", total-exact)
	}
	return nil
}

// slot is one planned session, filled in before any byte is written so the plan
// can be reported and asserted independently of the writing order.
type slot struct {
	harness   string
	ordinal   int
	target    int64
	artifacts int
	defects   Defects
	// blobs indexes Corpus.Blobs for the references this session resolves.
	blobs []int
	// missing holds references that deliberately do not resolve, already in
	// the form the harness's adapter reports.
	missing []string
}

type generator struct {
	p      Profile
	rng    *rand.Rand
	slots  []*slot
	corpus Corpus
}

// plan decides every session's size, artifact count, defects, and references
// before generation starts. Planning first is what lets Corpus describe the
// corpus exactly instead of approximately.
func (g *generator) plan() {
	counts := map[string]int{
		HarnessOMP:    g.p.OMPSessions,
		HarnessCodex:  g.p.CodexSessions,
		HarnessClaude: g.p.ClaudeSessions,
	}
	total := 0
	for _, h := range harnessOrder {
		total += counts[h]
	}
	sizes := g.planSizes(total)

	g.slots = make([]*slot, 0, total)
	for _, h := range harnessOrder {
		for i := range counts[h] {
			g.slots = append(g.slots, &slot{harness: h, ordinal: i})
		}
	}
	for i, s := range g.slots {
		s.target = sizes[i]
		span := g.p.ArtifactsPerSession[1] - g.p.ArtifactsPerSession[0]
		s.artifacts = g.p.ArtifactsPerSession[0] + g.rng.IntN(span+1)
	}

	// Defect classes start at staggered positions in the harness-interleaved
	// order, so even a small profile produces sessions carrying one defect,
	// sessions carrying several, and at least one of each defect per harness.
	order := g.interleaved()
	for i := range g.p.TornFinalLines {
		order[i%len(order)].defects.TornFinalLine = true
	}
	for i := range g.p.OversizedLines {
		order[(i+1)%len(order)].defects.OversizedRecords++
	}
	for i := range g.p.MalformedLines {
		order[(i+2)%len(order)].defects.MalformedRecords++
	}

	// The first session of a harness that has more than one is starved of
	// header metadata, so every harness's completeness reporting is exercised
	// while the harness still has a fully populated session too.
	for _, h := range harnessOrder {
		if counts[h] > 1 {
			g.byHarness(h)[0].defects.SparseHeader = true
		}
	}
	if claude := g.byHarness(HarnessClaude); len(claude) > 1 {
		claude[1].defects.WorkspaceMoved = true
	}

	g.planBlobs()
}

// planSizes draws one target size per session: the buckets that claim sessions
// outright are satisfied first, the rest are drawn by weight, and the result is
// shuffled so the extremes are not all attributed to one harness.
func (g *generator) planSizes(total int) []int64 {
	out := make([]int64, 0, total)
	for _, b := range g.p.SizeBuckets {
		for range b.Count {
			out = append(out, b.Bytes)
		}
	}
	weight := 0
	for _, b := range g.p.SizeBuckets {
		weight += b.Weight
	}
	for len(out) < total {
		pick := g.rng.IntN(weight)
		for _, b := range g.p.SizeBuckets {
			pick -= b.Weight
			if pick < 0 {
				out = append(out, b.Bytes)
				break
			}
		}
	}
	g.rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// interleaved returns the slots in round-robin harness order, which is how
// defects are spread: taking the first n of this order always touches every
// harness before it touches any harness twice.
func (g *generator) interleaved() []*slot {
	out := make([]*slot, 0, len(g.slots))
	for round := 0; len(out) < len(g.slots); round++ {
		for _, h := range harnessOrder {
			if s := g.byHarness(h); round < len(s) {
				out = append(out, s[round])
			}
		}
	}
	return out
}

func (g *generator) byHarness(harness string) []*slot {
	var out []*slot
	for _, s := range g.slots {
		if s.harness == harness {
			out = append(out, s)
		}
	}
	return out
}

// planBlobs fills the OMP content-addressed store and hands out both resolvable
// and deliberately unresolvable references.
func (g *generator) planBlobs() {
	for i := range g.p.BlobCount {
		content := blobContent(i)
		sum := sha256.Sum256(content)
		g.corpus.Blobs = append(g.corpus.Blobs, Blob{
			Ref:  "blob:sha256:" + hex.EncodeToString(sum[:]),
			Size: int64(len(content)),
		})
	}

	// The last blob stays unreferenced: a real store holds bytes no live
	// session names any more, and a closure must not claim them.
	ompSlots := g.byHarness(HarnessOMP)
	referenced := g.p.BlobCount
	if referenced > 1 {
		referenced--
	}
	if len(ompSlots) > 0 {
		for i := range referenced {
			s := ompSlots[i%len(ompSlots)]
			s.blobs = append(s.blobs, i)
			g.corpus.Blobs[i].Referenced = true
		}
	}

	codexSlots := g.byHarness(HarnessCodex)
	for j := range g.p.UnresolvedBlobRefs {
		// Alternate harnesses so both the OMP blob store's digest check and
		// Codex's attachment-directory lookup report an unresolved reference.
		toOMP := j%2 == 0
		if len(ompSlots) == 0 {
			toOMP = false
		}
		if len(codexSlots) == 0 {
			toOMP = true
		}
		switch {
		case toOMP && len(ompSlots) > 0:
			s := ompSlots[j%len(ompSlots)]
			blob := Blob{Ref: "blob:sha256:" + absentBlobHex(j), Referenced: true}
			if j == 0 {
				// Exactly one unresolvable reference is present in the store
				// under a name its bytes do not hash to, so digest
				// verification is what rejects it rather than absence.
				blob.DigestMismatch = true
				blob.Size = int64(len(mismatchedBlobContent()))
			}
			g.corpus.Blobs = append(g.corpus.Blobs, blob)
			s.missing = append(s.missing, blob.Ref)
		case len(codexSlots) > 0:
			s := codexSlots[j%len(codexSlots)]
			s.missing = append(s.missing, "attachments/"+absentAttachmentID(j))
		}
	}
}

// blobContent is the deterministic payload of blob i.
func blobContent(i int) []byte {
	return fmt.Appendf(nil, "synthetic-blob-payload-%04d\n%s\n", i, fillerSlice(64+i%7*16))
}

// mismatchedBlobContent is the payload written under a name it does not hash
// to. There is only ever one such blob, so it takes no index.
func mismatchedBlobContent() []byte {
	return []byte("synthetic-corrupted-blob-payload\n")
}

func absentBlobHex(j int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "synthetic-absent-blob-%04d", j))
	return hex.EncodeToString(sum[:])
}

func absentAttachmentID(j int) string {
	return fmt.Sprintf("ffffffff-0000-4000-8000-%012d", j)
}

// writeBlobs materializes the planned store. Every fifth blob is stored under
// an extension-suffixed name, which the OMP store also does and which its
// reader must resolve by digest rather than by name.
func (g *generator) writeBlobs() error {
	for i := range g.corpus.Blobs {
		b := &g.corpus.Blobs[i]
		want := b.Ref[len("blob:sha256:"):]
		switch {
		case b.DigestMismatch:
			b.Path = filepath.Join(g.corpus.OMPBlobStore, want)
			if err := g.writeFile(b.Path, mismatchedBlobContent()); err != nil {
				return err
			}
		case i < g.p.BlobCount:
			name := want
			if i%5 == 4 {
				name += ".webp"
			}
			b.Path = filepath.Join(g.corpus.OMPBlobStore, name)
			if err := g.writeFile(b.Path, blobContent(i)); err != nil {
				return err
			}
		}
	}
	if g.p.BlobCount == 0 && g.p.OMPSessions > 0 {
		// A session tree always has a store beside it: an empty store and a
		// missing one are different observations, and the OMP adapter reports
		// which one it saw.
		if err := os.MkdirAll(g.corpus.OMPBlobStore, dirPerm); err != nil {
			return fmt.Errorf("synth: create blob store: %w", err)
		}
	}
	return nil
}

// refsOf returns the resolvable references planned for a slot, ascending.
func (g *generator) refsOf(s *slot) []string {
	out := make([]string, 0, len(s.blobs))
	for _, i := range s.blobs {
		out = append(out, g.corpus.Blobs[i].Ref)
	}
	return out
}

// sessionTime spaces sessions apart deterministically. The step is coprime with
// a day so a harness's sessions land on several dates without ever depending on
// the clock.
func sessionTime(harnessIndex, ordinal int) time.Time {
	return baseTime.Add(time.Duration(harnessIndex)*7*time.Hour + time.Duration(ordinal)*11*time.Hour)
}

// writeFile writes one small file and accounts for it.
func (g *generator) writeFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("synth: create directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, filePerm); err != nil {
		return fmt.Errorf("synth: write %s: %w", path, err)
	}
	g.corpus.Files++
	g.corpus.Bytes += int64(len(content))
	return nil
}

// account folds a streamed log into the corpus totals.
func (g *generator) account(n int64) {
	g.corpus.Files++
	g.corpus.Bytes += n
}

// addSession appends a finished session, keeping Corpus.Sessions in the
// documented order.
func (g *generator) addSession(s Session) {
	slices.Sort(s.BlobRefs)
	s.BlobRefs = slices.Compact(s.BlobRefs)
	slices.Sort(s.UnresolvedRefs)
	slices.Sort(s.Artifacts)
	slices.Sort(s.HiddenArtifacts)
	g.corpus.Sessions = append(g.corpus.Sessions, s)
}
