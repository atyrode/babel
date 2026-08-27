// Package publish implements Babel's resumable stable-archive publication
// pipeline (SPEC.md §6.1): it discovers local harness sessions through
// versioned adapters, stages and hashes them, uploads immutable
// content-addressed objects, manifest segments and a generation index, and
// finally writes the immutable commit record that is the publication
// boundary. Only after that record is durable and read back does the
// non-authoritative `latest` hint move.
//
// Two invariants drive the whole package. First, a generation exists
// exactly when its digest-valid commit record is readable, so readers see
// either the previous complete generation or a verified new one and never
// an uncommitted manifest. Second, every stage is idempotent: uploads are
// content-addressed, the commit key embeds the record's own digest, and
// the private local journal only ever accelerates resumption — it is never
// the ordering authority (SPEC.md decision 43).
package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// snapshotAttempts bounds how often a source that changes underneath the
// adapter is re-staged before the session is deferred. A source that may be
// changing never produces a committed manifest entry (SPEC.md §11).
const snapshotAttempts = 3

// ErrConcurrentWriter reports that another writer owns the commit record of
// the generation this push targeted: the durable bytes at the write-once
// commit key are not the bytes this push wrote. The publication is
// abandoned rather than rewritten in place; the next push derives a later
// generation from the verified head and republishes (SPEC.md §6.1).
var ErrConcurrentWriter = errors.New("publish: commit record was written by a concurrent writer")

// Config configures a Publisher. Store and Adapters are required; the
// remaining fields describe this host and its local scratch space.
type Config struct {
	// Store is the archive backend every read and write goes through.
	Store objectstore.Store
	// Adapters are the harness source adapters to scan, at most one per
	// harness.
	Adapters []adapter.Adapter
	// HostID is this host's stable archive identity; it must satisfy
	// archive.ValidName.
	HostID string
	// HostDisplayName is recorded in every commit record; the newest
	// committed value wins for catalog display (SPEC.md §6.1).
	HostDisplayName string
	// Roots overrides the source roots to scan, keyed by harness name. A
	// harness without an entry uses its adapter's DefaultRoots.
	Roots map[string][]string
	// StateDir holds the private local publication journal.
	StateDir string
	// StagingDir holds staged session copies for the duration of a push.
	StagingDir string
	// BabelVersion is recorded in commit records for provenance.
	BabelVersion string
	// Now supplies the clock; nil means time.Now.
	Now func() time.Time
}

// Publisher publishes this host's local sessions as immutable archive
// generations. It holds no mutable publication state between pushes: all
// state lives in the archive and, as a pure accelerator, in the journal.
type Publisher struct {
	store           objectstore.Store
	adapters        []adapter.Adapter
	hostID          string
	hostDisplayName string
	roots           map[string][]string
	stateDir        string
	stagingDir      string
	babelVersion    string
	now             func() time.Time
}

// Result describes one Push. Changed reports whether the push committed a
// new generation: an unchanged corpus commits nothing and reports the
// generation that remains current.
type Result struct {
	Generation        uint64
	Changed           bool
	Bootstrap         bool
	BootstrapComplete bool
	Coverage          []archive.AdapterCoverage
	Published         int
	CarriedForward    int
	Deferred          int
	Sessions          int
	Revisions         int
	CommitKey         string
	CommitDigest      archive.Digest
}

// New validates cfg and returns a Publisher.
func New(cfg Config) (*Publisher, error) {
	if cfg.Store == nil {
		return nil, errors.New("publish: no object store configured")
	}
	if len(cfg.Adapters) == 0 {
		return nil, errors.New("publish: no adapters configured")
	}
	if !archive.ValidName(cfg.HostID) {
		return nil, fmt.Errorf("publish: invalid host id %q", cfg.HostID)
	}
	if cfg.StateDir == "" {
		return nil, errors.New("publish: no state directory configured")
	}
	if cfg.StagingDir == "" {
		return nil, errors.New("publish: no staging directory configured")
	}
	seen := make(map[string]struct{}, len(cfg.Adapters))
	for _, a := range cfg.Adapters {
		h := a.Harness()
		if !archive.ValidName(h) {
			return nil, fmt.Errorf("publish: invalid harness name %q", h)
		}
		if _, dup := seen[h]; dup {
			return nil, fmt.Errorf("publish: duplicate adapter for harness %q", h)
		}
		seen[h] = struct{}{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Publisher{
		store:           cfg.Store,
		adapters:        slices.Clone(cfg.Adapters),
		hostID:          cfg.HostID,
		hostDisplayName: cfg.HostDisplayName,
		roots:           cfg.Roots,
		stateDir:        cfg.StateDir,
		stagingDir:      cfg.StagingDir,
		babelVersion:    cfg.BabelVersion,
		now:             now,
	}, nil
}

// Push runs one complete publication cycle and returns what it committed.
//
// The stages are exactly those of SPEC.md §6.1, each idempotent and
// journaled after its remote read-back: resume any durable commit left by a
// previous run, read the verified committed state, stage and hash every
// discovered source, plan the new full entry set, upload bundle objects,
// then manifest segments, then the generation index, then the commit
// record, and only then replace the latest hint. Interrupting any stage
// leaves the prior generation current or the new one discoverable by the
// verified-record scan; re-running converges.
func (p *Publisher) Push(ctx context.Context) (*Result, error) {
	j := loadJournal(p.stateDir, p.hostID)
	if err := p.resumeCommitted(ctx, j); err != nil {
		return nil, err
	}

	head, err := archive.VerifiedHead(ctx, p.store, p.hostID)
	if err != nil {
		return nil, fmt.Errorf("publish: read committed head: %w", err)
	}
	var prior []archive.ManifestEntry
	gen := uint64(1)
	bootstrap := true
	bootstrapComplete := false
	if head != nil {
		bootstrap = false
		bootstrapComplete = head.Commit.BootstrapComplete
		gen = head.Commit.Generation + 1
		if prior, err = archive.LoadEntries(ctx, p.store, head.Index); err != nil {
			return nil, fmt.Errorf("publish: load generation %d entries: %w", head.Commit.Generation, err)
		}
	}
	if j == nil || j.Generation != gen {
		j = &journal{HostID: p.hostID, Generation: gen}
	}

	stagingRoot := filepath.Join(p.stagingDir, archive.GenerationKey(gen))
	defer os.RemoveAll(stagingRoot)

	sc, err := p.scan(ctx, stagingRoot)
	if err != nil {
		return nil, err
	}
	pl, err := p.plan(ctx, gen, prior, sc)
	if err != nil {
		return nil, err
	}
	coverage := sc.finish(pl)

	built, err := archive.BuildSegments(pl.entries)
	if err != nil {
		return nil, fmt.Errorf("publish: build manifest segments: %w", err)
	}
	sessions, revisions := archive.CountEntries(pl.entries)

	res := &Result{
		Coverage:       coverage,
		Published:      pl.publishedTotal,
		CarriedForward: pl.carriedTotal,
		Deferred:       sc.deferredTotal(),
		Sessions:       sessions,
		Revisions:      revisions,
	}

	// An unchanged corpus reuses every segment by digest and therefore has
	// nothing to commit: the current generation stays current. Only the
	// hint is repaired, which is how a push after a crash between commit
	// read-back and pointer replacement finishes the previous publication.
	if head != nil && sameSegments(built, head.Index.Segments) {
		if err := p.ensureHint(ctx, head.Commit.Generation, head.Key, head.CommitDigest); err != nil {
			return nil, err
		}
		// Nothing is in flight, so no resumption state should survive.
		p.clearJournal()
		res.Generation = head.Commit.Generation
		res.Bootstrap = head.Commit.Bootstrap
		res.BootstrapComplete = head.Commit.BootstrapComplete
		res.CommitKey = head.Key
		res.CommitDigest = head.CommitDigest
		res.Sessions = head.Index.Sessions
		res.Revisions = head.Index.Revisions
		return res, nil
	}

	j.Stage, j.Entries = stageStaged, pl.staged
	if err := j.save(p.stateDir, p.now()); err != nil {
		return nil, err
	}

	if err := p.uploadObjects(ctx, pl.uploads); err != nil {
		return nil, err
	}
	j.Stage = stageObjects
	if err := j.save(p.stateDir, p.now()); err != nil {
		return nil, err
	}

	for _, b := range built {
		if err := p.putBytes(ctx, b.Ref.Object, b.Bytes); err != nil {
			return nil, fmt.Errorf("publish: manifest segment %s: %w", b.Ref.Partition, err)
		}
	}
	j.Stage = stageSegments
	if err := j.save(p.stateDir, p.now()); err != nil {
		return nil, err
	}

	now := p.now().UTC()
	idx := archive.GenerationIndex{
		IndexSchema: archive.IndexSchemaVersion,
		HostID:      p.hostID,
		Generation:  gen,
		CreatedAt:   now,
		Segments:    make([]archive.SegmentRef, len(built)),
		Sessions:    sessions,
		Revisions:   revisions,
	}
	for i, b := range built {
		idx.Segments[i] = b.Ref
	}
	idxBytes, err := archive.MarshalCanonical(idx)
	if err != nil {
		return nil, fmt.Errorf("publish: marshal generation index: %w", err)
	}
	idxRef := archive.ObjectRef{Digest: archive.DigestBytes(idxBytes), Size: int64(len(idxBytes))}
	if err := p.putBytes(ctx, idxRef, idxBytes); err != nil {
		return nil, fmt.Errorf("publish: generation index: %w", err)
	}
	j.Stage = stageIndex
	if err := j.save(p.stateDir, p.now()); err != nil {
		return nil, err
	}

	allComplete := true
	for _, cov := range coverage {
		if !cov.Complete {
			allComplete = false
		}
	}
	rec := archive.CommitRecord{
		CommitSchema:    archive.CommitSchemaVersion,
		HostID:          p.hostID,
		HostDisplayName: p.hostDisplayName,
		Generation:      gen,
		CreatedAt:       now,
		Index:           idxRef,
		Coverage:        coverage,
		Bootstrap:       bootstrap,
		// Bootstrap completeness is monotone: the backfill is complete once
		// some generation scanned every adapter without a deferral.
		BootstrapComplete: bootstrapComplete || allComplete,
		BabelVersion:      p.babelVersion,
	}
	commitKey, commitDigest, err := p.commit(ctx, rec)
	if err != nil {
		return nil, err
	}
	j.Stage, j.CommitKey, j.CommitDigest = stageCommitted, commitKey, commitDigest
	if err := j.save(p.stateDir, p.now()); err != nil {
		return nil, err
	}

	if err := p.ensureHint(ctx, gen, commitKey, commitDigest); err != nil {
		return nil, err
	}
	j.Stage = stagePointer
	if err := j.save(p.stateDir, p.now()); err != nil {
		return nil, err
	}

	res.Generation = gen
	res.Changed = true
	res.Bootstrap = rec.Bootstrap
	res.BootstrapComplete = rec.BootstrapComplete
	res.CommitKey = commitKey
	res.CommitDigest = commitDigest
	return res, nil
}

// resumeCommitted finishes a publication whose commit record was already
// written and read back before the process stopped. That record is durable
// publication, so the only outstanding work is the latest hint. The
// short-circuit is advisory: the journal claim is re-verified against the
// archive, and anything unconvincing is simply ignored — the verified-head
// scan below reaches the same state, only more slowly.
func (p *Publisher) resumeCommitted(ctx context.Context, j *journal) error {
	if j == nil || j.Stage != stageCommitted || j.CommitKey == "" || !j.CommitDigest.Valid() {
		return nil
	}
	raw, err := p.readAll(ctx, j.CommitKey)
	if err != nil {
		return nil
	}
	if archive.DigestBytes(raw) != j.CommitDigest {
		return nil
	}
	if err := p.ensureHint(ctx, j.Generation, j.CommitKey, j.CommitDigest); err != nil {
		return err
	}
	j.Stage = stagePointer
	return j.save(p.stateDir, p.now())
}

// stagedSession is one session staged and hashed for this push.
type stagedSession struct {
	harness string
	schema  int
	key     archive.SessionKey
	snap    *adapter.Snapshot
	// content describes the reassembled plaintext, which for a staged
	// source is simply the staged primary log.
	content archive.ObjectRef
}

// scanResult is the outcome of the discovery and staging stage.
type scanResult struct {
	sessions []stagedSession
	order    []string
	coverage map[string]*archive.AdapterCoverage
	deferred map[string]bool
}

func (s *scanResult) deferSession(harness, sourceID, reason string) {
	cov := s.coverage[harness]
	if cov == nil {
		return
	}
	cov.Deferred++
	cov.Complete = false
	cov.DeferredReasons = append(cov.DeferredReasons, sourceID+": "+reason)
}

func (s *scanResult) deferredTotal() int {
	n := 0
	for _, cov := range s.coverage {
		n += cov.Deferred
	}
	return n
}

// finish folds the planning counts into per-adapter coverage and returns it
// in canonical harness order.
func (s *scanResult) finish(pl *planned) []archive.AdapterCoverage {
	out := make([]archive.AdapterCoverage, 0, len(s.order))
	for _, h := range s.order {
		cov := s.coverage[h]
		cov.Published = pl.published[h]
		cov.CarriedForward = pl.carried[h]
		slices.Sort(cov.DeferredReasons)
		out = append(out, *cov)
	}
	slices.SortFunc(out, func(a, b archive.AdapterCoverage) int { return strings.Compare(a.Harness, b.Harness) })
	return out
}

// scan discovers and stages every locally available session. Per-session
// failures degrade into deferrals with a reason; they never abort the push,
// because a deferred source must stay visible and retried rather than
// silently omitted (SPEC.md §6.1, §11).
func (p *Publisher) scan(ctx context.Context, stagingRoot string) (*scanResult, error) {
	sc := &scanResult{
		coverage: make(map[string]*archive.AdapterCoverage, len(p.adapters)),
		deferred: map[string]bool{},
	}
	for _, a := range p.adapters {
		harness := a.Harness()
		cov := &archive.AdapterCoverage{Harness: harness, AdapterSchema: a.Schema(), Complete: true}
		sc.coverage[harness] = cov
		sc.order = append(sc.order, harness)

		roots := p.roots[harness]
		if len(roots) == 0 {
			roots = a.DefaultRoots()
		}
		srcs, err := a.Discover(ctx, roots)
		if err != nil {
			// A scan that could not enumerate its roots is incomplete, not
			// empty: nothing may be inferred about missing sessions.
			cov.Complete = false
			cov.DeferredReasons = append(cov.DeferredReasons, "discover: "+err.Error())
			continue
		}
		cov.Scanned = len(srcs)
		srcs = slices.Clone(srcs)
		slices.SortFunc(srcs, func(x, y adapter.SourceSession) int { return strings.Compare(x.SourceID, y.SourceID) })

		seen := make(map[string]struct{}, len(srcs))
		for _, src := range srcs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if src.Harness != "" && src.Harness != harness {
				sc.deferSession(harness, src.SourceID, "adapter reported foreign harness "+src.Harness)
				continue
			}
			if !archive.ValidSourceID(src.SourceID) {
				sc.deferSession(harness, src.SourceID, "invalid source identity")
				continue
			}
			if _, dup := seen[src.SourceID]; dup {
				sc.deferSession(harness, src.SourceID, "duplicate source identity in one scan")
				continue
			}
			seen[src.SourceID] = struct{}{}

			key := archive.SessionKey{Harness: harness, HostID: p.hostID, SourceID: src.SourceID}
			snap, err := p.snapshot(ctx, a, src, stagingRoot)
			if err != nil {
				sc.deferSession(harness, src.SourceID, err.Error())
				sc.deferred[key.String()] = true
				continue
			}
			digest, size, err := digestFile(snap.StagedPrimary)
			if err != nil {
				sc.deferSession(harness, src.SourceID, "hash staged log: "+err.Error())
				sc.deferred[key.String()] = true
				continue
			}
			sc.sessions = append(sc.sessions, stagedSession{
				harness: harness,
				schema:  a.Schema(),
				key:     key,
				snap:    snap,
				content: archive.ObjectRef{Digest: digest, Size: size},
			})
		}
	}
	slices.SortFunc(sc.sessions, func(x, y stagedSession) int {
		return strings.Compare(x.key.String(), y.key.String())
	})
	return sc, nil
}

// snapshot stages one session, retrying a bounded number of times when the
// adapter reports the source changed underneath it. The staging directory
// is recreated for every attempt so a partial copy is never published.
func (p *Publisher) snapshot(ctx context.Context, a adapter.Adapter, src adapter.SourceSession, stagingRoot string) (*adapter.Snapshot, error) {
	dir := sessionStagingDir(stagingRoot, a.Harness(), src.SourceID)
	var last error
	for attempt := 1; attempt <= snapshotAttempts; attempt++ {
		if err := os.RemoveAll(dir); err != nil {
			return nil, fmt.Errorf("clear staging directory: %w", err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create staging directory: %w", err)
		}
		snap, err := a.Snapshot(ctx, src, dir)
		switch {
		case err == nil && (snap == nil || snap.StagedPrimary == ""):
			return nil, errors.New("adapter staged no primary log")
		case err == nil:
			return snap, nil
		case !errors.Is(err, adapter.ErrUnstable):
			return nil, fmt.Errorf("snapshot failed: %w", err)
		}
		last = err
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
	}
	return nil, fmt.Errorf("unstable after %d attempts: %w", snapshotAttempts, last)
}

// sessionStagingDir gives every session a private, deterministic staging
// directory. The source identity is hashed rather than embedded so no
// adapter-defined name can escape the staging root.
func sessionStagingDir(stagingRoot, harness, sourceID string) string {
	sum := sha256.Sum256([]byte(sourceID))
	return filepath.Join(stagingRoot, harness, hex.EncodeToString(sum[:8]))
}

// pendingObject is one content-addressed object to upload. The reader is
// opened lazily so a push never holds a handle per staged file.
type pendingObject struct {
	ref  archive.ObjectRef
	open func() (io.ReadCloser, error)
}

// planned is the new generation's full entry set plus the objects that must
// exist before it can be committed.
type planned struct {
	entries        []archive.ManifestEntry
	uploads        []pendingObject
	staged         map[string]string
	published      map[string]int
	carried        map[string]int
	publishedTotal int
	carriedTotal   int
}

// deferErr marks a per-session defect that defers the session instead of
// failing the publication.
type deferErr struct{ err error }

func (e deferErr) Error() string { return e.err.Error() }
func (e deferErr) Unwrap() error { return e.err }

func deferf(format string, args ...any) error {
	return deferErr{err: fmt.Errorf(format, args...)}
}

// plan builds the new generation's complete entry set: every prior entry
// carried forward unchanged plus one new immutable revision per changed
// session. A manifest generation describes all revisions Babel knows, not
// only the ones this run touched, so entries whose local source disappeared
// survive (SPEC.md §6.1).
func (p *Publisher) plan(ctx context.Context, gen uint64, prior []archive.ManifestEntry, sc *scanResult) (*planned, error) {
	byRevision := make(map[string]archive.ManifestEntry, len(prior))
	newest := make(map[string]archive.ManifestEntry, len(prior))
	priorHarness := make(map[string]string, len(prior))
	for _, e := range prior {
		byRevision[e.RevisionKey] = e
		priorHarness[e.SessionKey] = e.Harness
		if cur, ok := newest[e.SessionKey]; !ok || newerRevision(e, cur) {
			newest[e.SessionKey] = e
		}
	}

	pl := &planned{
		entries:   slices.Clone(prior),
		staged:    make(map[string]string, len(sc.sessions)),
		published: map[string]int{},
		carried:   map[string]int{},
	}
	publishedSessions := make(map[string]bool, len(sc.sessions))
	uploaded := map[archive.Digest]bool{}

	for _, s := range sc.sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		revKey := s.key.Revision(s.content.Digest)
		pl.staged[revKey] = string(s.content.Digest)
		if _, committed := byRevision[revKey]; committed {
			// Byte-identical to a committed revision: carry it forward
			// rather than republishing identical content.
			continue
		}
		entry, uploads, err := p.planSession(ctx, gen, s, revKey, byRevision, newest[s.key.String()])
		var de deferErr
		if errors.As(err, &de) {
			sc.deferSession(s.harness, s.key.SourceID, de.Error())
			sc.deferred[s.key.String()] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, u := range uploads {
			if uploaded[u.ref.Digest] {
				continue
			}
			uploaded[u.ref.Digest] = true
			pl.uploads = append(pl.uploads, u)
		}
		pl.entries = append(pl.entries, *entry)
		publishedSessions[s.key.String()] = true
		pl.published[s.harness]++
		pl.publishedTotal++
	}

	// Everything committed before that this push neither republished nor
	// deferred is carried forward: an unchanged session, or one whose local
	// source is gone.
	for sessionKey, harness := range priorHarness {
		if publishedSessions[sessionKey] || sc.deferred[sessionKey] {
			continue
		}
		pl.carried[harness]++
		pl.carriedTotal++
	}
	return pl, nil
}

// planSession turns one staged session into a new immutable manifest entry
// and the objects it needs. It chooses append-delta encoding only when the
// prior newest committed revision's plaintext is an exact byte prefix of
// the staged content and the chain stays inside its bound; every other
// case — fork, rewrite, truncation, exhausted chain, unverifiable parent —
// publishes a full revision (SPEC.md decision 40).
func (p *Publisher) planSession(ctx context.Context, gen uint64, s stagedSession, revKey string, byRevision map[string]archive.ManifestEntry, parent archive.ManifestEntry) (*archive.ManifestEntry, []pendingObject, error) {
	encoding := archive.EncodingFull
	object := s.content
	parentRevision := ""
	chainDepth := 0
	payloadOffset := int64(0)

	if parent.RevisionKey != "" && parent.ChainDepth < archive.MaxChainDepth && parent.Content.Size < s.content.Size {
		if chain, err := chainFor(byRevision, parent.RevisionKey); err == nil {
			ok, err := isPrefix(ctx, p.store, chain, parent.Content, s.snap.StagedPrimary)
			if err != nil {
				return nil, nil, fmt.Errorf("publish: compare %s with its parent revision: %w", s.key, err)
			}
			if ok {
				digest, size, err := digestTail(s.snap.StagedPrimary, parent.Content.Size)
				if err != nil {
					return nil, nil, deferf("hash appended bytes: %v", err)
				}
				encoding = archive.EncodingAppendDelta
				object = archive.ObjectRef{Digest: digest, Size: size}
				parentRevision = parent.RevisionKey
				chainDepth = parent.ChainDepth + 1
				payloadOffset = parent.Content.Size
			}
		}
	}

	path := s.snap.StagedPrimary
	uploads := []pendingObject{{
		ref: object,
		open: func() (io.ReadCloser, error) {
			if payloadOffset == 0 {
				return os.Open(path)
			}
			return openAt(path, payloadOffset)
		},
	}}

	artifacts, artifactUploads, err := planArtifacts(s.snap.Artifacts)
	if err != nil {
		return nil, nil, err
	}
	uploads = append(uploads, artifactUploads...)

	blobs, blobUploads, err := p.planBlobs(ctx, s.snap.Blobs)
	if err != nil {
		return nil, nil, err
	}
	uploads = append(uploads, blobUploads...)

	meta := s.snap.Meta
	completeness := slices.Clone(meta.Completeness)
	adapterMetadata, err := archive.CanonicalRawMessage(s.snap.AdapterMetadata)
	adapterSchema := s.snap.AdapterMetadataSchema
	if err != nil {
		// Degrade explicitly instead of dropping the session or publishing
		// non-canonical bytes (SPEC.md §3).
		adapterMetadata, adapterSchema = nil, 0
		completeness = append(completeness, archive.CompletenessReason{
			Field:  "adapter_metadata",
			Reason: "adapter metadata was not canonical JSON",
		})
	}

	unresolved := slices.Clone(s.snap.UnresolvedBlobRefs)
	slices.Sort(unresolved)

	entry := &archive.ManifestEntry{
		ManifestSchema:  archive.ManifestSchemaVersion,
		Harness:         s.harness,
		AdapterSchema:   s.schema,
		HostID:          p.hostID,
		SourceID:        s.key.SourceID,
		SessionKey:      s.key.String(),
		RevisionKey:     revKey,
		GenerationAdded: gen,
		SnapshotTime:    s.snap.SnapshotTime.UTC(),

		Encoding:       encoding,
		Content:        s.content,
		Object:         object,
		ParentRevision: parentRevision,
		ChainDepth:     chainDepth,

		Title:      meta.Title,
		Workspace:  meta.Workspace,
		CreatedAt:  utcTime(meta.CreatedAt),
		ModifiedAt: utcTime(meta.ModifiedAt),
		Lifecycle:  meta.Lifecycle,
		Repo:       meta.Repo,

		Completeness: completeness,

		Artifacts:          artifacts,
		Blobs:              blobs,
		UnresolvedBlobRefs: unresolved,
		// An unresolved reference means the closure is incomplete, so the
		// revision cannot be continuation grade whatever the adapter says.
		ContinuationGrade: s.snap.ContinuationGrade && len(unresolved) == 0,

		AdapterMetadataSchema: adapterSchema,
		AdapterMetadata:       adapterMetadata,
	}
	return entry, uploads, nil
}

// planArtifacts hashes a session's declared artifact closure into file
// references, sorted by path so canonical segment bytes are stable.
func planArtifacts(files []adapter.StagedFile) ([]archive.FileRef, []pendingObject, error) {
	if len(files) == 0 {
		return nil, nil, nil
	}
	refs := make([]archive.FileRef, 0, len(files))
	uploads := make([]pendingObject, 0, len(files))
	for _, f := range files {
		digest, size, err := digestFile(f.StagedPath)
		if err != nil {
			return nil, nil, deferf("hash artifact %s: %v", f.RelPath, err)
		}
		refs = append(refs, archive.FileRef{Path: f.RelPath, Digest: digest, Size: size})
		path := f.StagedPath
		uploads = append(uploads, pendingObject{
			ref:  archive.ObjectRef{Digest: digest, Size: size},
			open: func() (io.ReadCloser, error) { return os.Open(path) },
		})
	}
	slices.SortFunc(refs, func(a, b archive.FileRef) int { return strings.Compare(a.Path, b.Path) })
	return refs, uploads, nil
}

// planBlobs turns a session's resolved blob references into object
// references. A blob already present in the archive is accepted on
// presence and size (SPEC.md decision 44); otherwise its source is hashed
// and must match the digest the adapter declared, because the archive is
// content-addressed and an unverified digest would poison it.
func (p *Publisher) planBlobs(ctx context.Context, blobs []adapter.BlobRef) ([]archive.ObjectRef, []pendingObject, error) {
	if len(blobs) == 0 {
		return nil, nil, nil
	}
	refs := make([]archive.ObjectRef, 0, len(blobs))
	var uploads []pendingObject
	for _, b := range blobs {
		if !b.Digest.Valid() {
			return nil, nil, deferf("blob %s has an invalid digest", b.SourcePath)
		}
		ref := archive.ObjectRef{Digest: b.Digest, Size: b.Size}
		switch in, err := p.store.Stat(ctx, archive.CASKey(b.Digest)); {
		case err == nil:
			if in.Size != b.Size {
				return nil, nil, deferf("blob %s is stored with size %d, declared %d", b.Digest, in.Size, b.Size)
			}
			refs = append(refs, ref)
			continue
		case !errors.Is(err, objectstore.ErrNotExist):
			return nil, nil, fmt.Errorf("publish: stat blob %s: %w", b.Digest, err)
		}
		digest, size, err := digestFile(b.SourcePath)
		if err != nil {
			return nil, nil, deferf("hash blob %s: %v", b.Digest, err)
		}
		if digest != b.Digest || size != b.Size {
			return nil, nil, deferf("blob %s does not match its source content", b.Digest)
		}
		path := b.SourcePath
		refs = append(refs, ref)
		uploads = append(uploads, pendingObject{
			ref:  ref,
			open: func() (io.ReadCloser, error) { return os.Open(path) },
		})
	}
	slices.SortFunc(refs, func(a, b archive.ObjectRef) int { return strings.Compare(string(a.Digest), string(b.Digest)) })
	return refs, uploads, nil
}

// newerRevision orders two revisions of one session. Generation dominates;
// snapshot time and the immutable revision key break ties deterministically
// so every host agrees on a session's newest committed revision.
func newerRevision(a, b archive.ManifestEntry) bool {
	if a.GenerationAdded != b.GenerationAdded {
		return a.GenerationAdded > b.GenerationAdded
	}
	if !a.SnapshotTime.Equal(b.SnapshotTime) {
		return a.SnapshotTime.After(b.SnapshotTime)
	}
	return a.RevisionKey > b.RevisionKey
}

func utcTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// sameSegments reports whether a freshly built entry set is byte-identical
// to a committed generation: same partitions, same segment digests. It is
// the change test, and it is exact because canonical segment bytes are a
// pure function of their entries.
func sameSegments(built []archive.BuiltSegment, committed []archive.SegmentRef) bool {
	if len(built) != len(committed) {
		return false
	}
	for i, b := range built {
		if b.Ref.Partition != committed[i].Partition || b.Ref.Object.Digest != committed[i].Object.Digest {
			return false
		}
	}
	return true
}

// uploadObjects publishes every content-addressed object of the new
// generation before any manifest references it.
func (p *Publisher) uploadObjects(ctx context.Context, uploads []pendingObject) error {
	for _, u := range uploads {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.putObject(ctx, u.ref, u.open); err != nil {
			return err
		}
	}
	return nil
}

// putObject uploads one content-addressed object. A first upload is fully
// read back and digest-verified; an object already present is verified by
// presence and size instead of re-reading its bytes (SPEC.md decision 44).
func (p *Publisher) putObject(ctx context.Context, ref archive.ObjectRef, open func() (io.ReadCloser, error)) error {
	key := archive.CASKey(ref.Digest)
	rc, err := open()
	if err != nil {
		return fmt.Errorf("publish: open %s: %w", key, err)
	}
	created, size, err := p.store.Put(ctx, key, rc)
	closeErr := rc.Close()
	if err != nil {
		return fmt.Errorf("publish: upload %s: %w", key, err)
	}
	if closeErr != nil {
		return fmt.Errorf("publish: close source of %s: %w", key, closeErr)
	}
	if size != ref.Size {
		return fmt.Errorf("publish: uploaded %s with %d bytes, expected %d", key, size, ref.Size)
	}
	if !created {
		in, err := p.store.Stat(ctx, key)
		if err != nil {
			return fmt.Errorf("publish: stat %s: %w", key, err)
		}
		if in.Size != ref.Size {
			return fmt.Errorf("publish: %s is stored with %d bytes, expected %d", key, in.Size, ref.Size)
		}
		return nil
	}
	return p.verifyObject(ctx, ref)
}

// putBytes uploads a small in-memory canonical document as a
// content-addressed object.
func (p *Publisher) putBytes(ctx context.Context, ref archive.ObjectRef, raw []byte) error {
	return p.putObject(ctx, ref, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	})
}

// verifyObject reads a freshly uploaded object back in full and checks its
// digest and size, streaming so a large bundle is never buffered.
func (p *Publisher) verifyObject(ctx context.Context, ref archive.ObjectRef) error {
	key := archive.CASKey(ref.Digest)
	rc, err := p.store.Read(ctx, key)
	if err != nil {
		return fmt.Errorf("publish: read back %s: %w", key, err)
	}
	defer rc.Close()
	digest, size, err := archive.ComputeDigest(rc)
	if err != nil {
		return fmt.Errorf("publish: read back %s: %w", key, err)
	}
	if size != ref.Size || digest != ref.Digest {
		return fmt.Errorf("publish: read back %s as %s (%d bytes), expected %s (%d bytes)", key, digest, size, ref.Digest, ref.Size)
	}
	return nil
}

// commit writes the immutable commit record that is the publication
// boundary and reads it back in full, comparing the exact bytes. The key
// embeds the record's own digest, so identical bytes are idempotent and
// different bytes cannot clobber one another; a read-back that does not
// match, or a refused immutable write, means another writer owns this
// generation and this publication must be abandoned (SPEC.md §6.1).
func (p *Publisher) commit(ctx context.Context, rec archive.CommitRecord) (string, archive.Digest, error) {
	raw, err := archive.MarshalCanonical(rec)
	if err != nil {
		return "", "", fmt.Errorf("publish: marshal commit record: %w", err)
	}
	digest := archive.DigestBytes(raw)
	key := archive.CommitKey(p.hostID, rec.Generation, digest)

	if _, _, err := p.store.Put(ctx, key, bytes.NewReader(raw)); err != nil {
		if errors.Is(err, objectstore.ErrImmutableConflict) {
			return "", "", fmt.Errorf("%w: %s: %w", ErrConcurrentWriter, key, err)
		}
		return "", "", fmt.Errorf("publish: write commit record %s: %w", key, err)
	}
	got, err := p.readAll(ctx, key)
	if err != nil {
		return "", "", fmt.Errorf("publish: read back commit record %s: %w", key, err)
	}
	if !bytes.Equal(got, raw) {
		return "", "", fmt.Errorf("%w: %s holds different bytes", ErrConcurrentWriter, key)
	}
	return key, digest, nil
}

// ensureHint points the host's latest hint at a committed generation. The
// hint is a non-authoritative optimization, so it is only rewritten when it
// does not already name that commit.
func (p *Publisher) ensureHint(ctx context.Context, gen uint64, commitKey string, commitDigest archive.Digest) error {
	in, err := p.store.Stat(ctx, commitKey)
	if err != nil {
		return fmt.Errorf("publish: stat commit record %s: %w", commitKey, err)
	}
	hint := archive.LatestHint{
		HintSchema: archive.HintSchemaVersion,
		HostID:     p.hostID,
		Generation: gen,
		Commit:     archive.ObjectRef{Digest: commitDigest, Size: in.Size},
	}
	if cur, err := archive.ReadLatestHint(ctx, p.store, p.hostID); err == nil && cur != nil && *cur == hint {
		return nil
	}
	raw, err := archive.MarshalCanonical(hint)
	if err != nil {
		return fmt.Errorf("publish: marshal latest hint: %w", err)
	}
	if err := p.store.ReplacePointer(ctx, archive.LatestKey(p.hostID), raw); err != nil {
		return fmt.Errorf("publish: replace latest hint: %w", err)
	}
	return nil
}

// readAll reads a whole object; used only for small documents.
func (p *Publisher) readAll(ctx context.Context, key string) ([]byte, error) {
	rc, err := p.store.Read(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
