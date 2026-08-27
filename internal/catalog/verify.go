package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// Report is the outcome of one verification run.
type Report struct {
	// Deep records which tier produced the report.
	Deep  bool
	Hosts []HostReport
}

// OK reports whether verification found no errors. Warnings describe
// anomalies that do not make committed state unusable and never clear OK.
func (r *Report) OK() bool {
	for _, h := range r.Hosts {
		if len(h.Errors) > 0 {
			return false
		}
	}
	return true
}

// HostReport is one host's verification outcome.
type HostReport struct {
	HostID string

	// Records counts commit records enumerated for the host.
	Records int
	// Generations counts commit records whose bytes verified against their
	// write-once key.
	Generations int
	// Revisions counts manifest entries verified in the exposed
	// generation.
	Revisions int
	// Objects counts distinct content-addressed objects checked.
	Objects int

	Warnings []string
	Errors   []string
}

func (h *HostReport) warnf(format string, args ...any) {
	h.Warnings = append(h.Warnings, fmt.Sprintf(format, args...))
}

func (h *HostReport) errorf(format string, args ...any) {
	h.Errors = append(h.Errors, fmt.Sprintf(format, args...))
}

// Verify checks the archive's committed state without materializing any
// bundle (SPEC.md §8, `babel archive verify`).
//
// The default tier verifies, per host: that every commit record parses and
// digest-matches its write-once key; that the exposed generation's index
// and every manifest segment digest-verify; that every entry satisfies the
// manifest contract and every append-delta chain resolves inside the
// generation; and that every referenced content-addressed object exists
// with its recorded size. Two commit records at one generation are
// anomalous rather than fatal and are reported as a warning, as is a
// `latest` hint that does not name the selected generation.
//
// The deep tier additionally reads every referenced object end to end,
// verifying its plaintext digest, and reassembles every append-delta chain,
// verifying the result against the entry's content digest. Only the deep
// tier can detect a same-size bit flip, which presence-and-size checks
// cannot see.
//
// Verify itself fails only on an unusable store; everything it finds about
// the archive is reported, so one broken host never hides the others.
func Verify(ctx context.Context, st objectstore.Store, hosts []string, deep bool) (*Report, error) {
	if st == nil {
		return nil, errors.New("catalog: nil object store")
	}
	ids, err := hostList(ctx, st, hosts)
	if err != nil {
		return nil, err
	}
	rep := &Report{Deep: deep, Hosts: make([]HostReport, 0, len(ids))}
	for _, id := range ids {
		hr := HostReport{HostID: id}
		verifyHost(ctx, st, id, deep, &hr)
		rep.Hosts = append(rep.Hosts, hr)
	}
	return rep, nil
}

func verifyHost(ctx context.Context, st objectstore.Store, hostID string, deep bool, hr *HostReport) {
	cands, foreign, err := listCommitCandidates(ctx, st, hostID)
	if err != nil {
		hr.errorf("%v", err)
		return
	}
	hr.Records = len(cands)
	for _, key := range foreign {
		hr.warnf("unrecognized object under the commit prefix: %s", key)
	}
	if len(cands) == 0 {
		hr.warnf("host has published no commit record")
		return
	}

	// Every commit record must stand on its own bytes, not only the one
	// selection happens to choose.
	perGeneration := make(map[uint64][]string, len(cands))
	for _, c := range cands {
		perGeneration[c.Generation] = append(perGeneration[c.Generation], c.Key)
		if _, _, err := readCommitRecord(ctx, st, hostID, c.Key); err != nil {
			hr.errorf("%v", err)
			continue
		}
		hr.Generations++
	}
	// Candidates descend, so the first key of a generation is its winner.
	for _, c := range cands {
		keys := perGeneration[c.Generation]
		if len(keys) > 1 && keys[0] == c.Key {
			hr.warnf("generation %d has %d commit records (concurrent publication without a lease); %s wins",
				c.Generation, len(keys), c.Key)
		}
	}

	gen, skipped, err := selectGeneration(ctx, st, hostID)
	for _, s := range skipped {
		hr.errorf("committed generation skipped: %s", s)
	}
	if err != nil {
		hr.errorf("%v", err)
		return
	}
	if gen == nil {
		return
	}
	for _, a := range gen.Anomalies {
		hr.errorf("%s g%d: %s", hostID, gen.Record.Generation, a)
	}
	verifyHint(ctx, st, hostID, gen, hr)
	verifyGeneration(ctx, st, gen, deep, hr)
}

// verifyHint cross-checks the mutable pointer against the generation
// selection actually chose. A hint is never authoritative, so every
// disagreement — absent, behind, ahead, or naming an unreadable record — is
// a warning (SPEC.md §6.1).
func verifyHint(ctx context.Context, st objectstore.Store, hostID string, gen *generation, hr *HostReport) {
	hint, err := archive.ReadLatestHint(ctx, st, hostID)
	if err != nil {
		hr.warnf("latest hint unreadable: %v", err)
		return
	}
	if hint == nil {
		hr.warnf("no usable latest hint; readers fall back to the verified-record scan")
		return
	}
	if hint.Generation == gen.Record.Generation && hint.Commit.Digest == gen.RecordDigest {
		return
	}
	hr.warnf("latest hint names generation %d commit %s, selection exposes generation %d commit %s",
		hint.Generation, hint.Commit.Digest, gen.Record.Generation, gen.RecordDigest)
	if !hint.Commit.Digest.Valid() {
		return
	}
	key := archive.CommitKey(hostID, hint.Generation, hint.Commit.Digest)
	if _, _, err := readCommitRecord(ctx, st, hostID, key); err != nil {
		hr.warnf("latest hint points at an unusable commit record: %v", err)
	}
}

// verifyGeneration checks the exposed generation's entries, chains, and
// referenced objects.
func verifyGeneration(ctx context.Context, st objectstore.Store, gen *generation, deep bool, hr *HostReport) {
	hr.Revisions = len(gen.Entries)
	// The index and every segment were read and digest-verified while the
	// generation was loaded.
	hr.Objects = 1 + len(gen.Index.Segments)

	objects := newObjectSet()
	objects.add(gen.Entries, hr)

	for _, e := range gen.Entries {
		chain, err := chainOf(e, gen.ByRevision)
		if err != nil {
			hr.errorf("%v", err)
			continue
		}
		if !deep {
			continue
		}
		if err := verifyChainContent(ctx, st, chain, e.Content); err != nil {
			hr.errorf("revision %s: %v", e.RevisionKey, err)
		}
	}

	for _, ref := range objects.refs {
		if err := ctx.Err(); err != nil {
			hr.errorf("%v", err)
			return
		}
		label := objects.labels[ref.Digest]
		if deep {
			if err := verifyObjectContent(ctx, st, ref); err != nil {
				hr.errorf("%s: %v", label, err)
				continue
			}
			hr.Objects++
			continue
		}
		in, err := st.Stat(ctx, archive.CASKey(ref.Digest))
		if err != nil {
			hr.errorf("%s: %v", label, err)
			continue
		}
		if in.Size != ref.Size {
			hr.errorf("%s: object %s has size %d, want %d", label, ref.Digest, in.Size, ref.Size)
			continue
		}
		hr.Objects++
	}
}

// objectSet accumulates the distinct content-addressed objects a generation
// references, in first-seen order, and keeps one log-safe label per object.
type objectSet struct {
	labels map[archive.Digest]string
	sizes  map[archive.Digest]int64
	refs   []archive.ObjectRef
}

func newObjectSet() *objectSet {
	return &objectSet{labels: make(map[archive.Digest]string), sizes: make(map[archive.Digest]int64)}
}

// add collects every object referenced by a generation's entries: payload
// objects (a full bundle or an append-delta tail), declared artifacts, and
// referenced blobs. Two entries referencing one digest with disagreeing
// sizes is itself a contract violation and is reported.
func (o *objectSet) add(entries []archive.ManifestEntry, hr *HostReport) {
	for _, e := range entries {
		o.one(e.Object, "payload of "+e.RevisionKey, hr)
		for _, a := range e.Artifacts {
			o.one(archive.ObjectRef{Digest: a.Digest, Size: a.Size}, "artifact of "+e.RevisionKey, hr)
			if err := validRelPath(a.Path); err != nil {
				hr.warnf("revision %s declares an artifact path that cannot be materialized: %v", e.RevisionKey, err)
			}
		}
		for _, b := range e.Blobs {
			o.one(b, "blob of "+e.RevisionKey, hr)
		}
		for _, u := range e.UnresolvedBlobRefs {
			hr.warnf("revision %s references unresolved blob %s (continuation grade %t)", e.RevisionKey, u, e.ContinuationGrade)
		}
	}
}

func (o *objectSet) one(ref archive.ObjectRef, label string, hr *HostReport) {
	if size, ok := o.sizes[ref.Digest]; ok {
		if size != ref.Size {
			hr.errorf("%s: object %s recorded with sizes %d and %d", label, ref.Digest, size, ref.Size)
		}
		return
	}
	o.sizes[ref.Digest] = ref.Size
	o.labels[ref.Digest] = label
	o.refs = append(o.refs, ref)
}

// verifyObjectContent streams one content-addressed object and verifies its
// size and plaintext digest without retaining its bytes.
func verifyObjectContent(ctx context.Context, st objectstore.Store, ref archive.ObjectRef) error {
	rc, err := st.Read(ctx, archive.CASKey(ref.Digest))
	if err != nil {
		return err
	}
	defer rc.Close()
	got, n, err := archive.ComputeDigest(rc)
	if err != nil {
		return err
	}
	if n != ref.Size {
		return fmt.Errorf("object %s has size %d, want %d", ref.Digest, n, ref.Size)
	}
	if got != ref.Digest {
		return fmt.Errorf("object %s has digest %s", ref.Digest, got)
	}
	return nil
}

// verifyChainContent streams a revision's append-delta chain in application
// order and verifies the reassembled plaintext against the entry's content
// reference. Bytes are hashed, never retained, so verification cost is
// independent of transcript size.
func verifyChainContent(ctx context.Context, st objectstore.Store, chain []archive.ManifestEntry, want archive.ObjectRef) error {
	h := sha256.New()
	var total int64
	for _, link := range chain {
		rc, err := st.Read(ctx, archive.CASKey(link.Object.Digest))
		if err != nil {
			return fmt.Errorf("chain link %s: %w", link.RevisionKey, err)
		}
		n, err := io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("chain link %s: %w", link.RevisionKey, err)
		}
		if n != link.Object.Size {
			return fmt.Errorf("chain link %s payload has size %d, want %d", link.RevisionKey, n, link.Object.Size)
		}
		total += n
	}
	if total != want.Size {
		return fmt.Errorf("reassembled content has size %d, want %d", total, want.Size)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	if got := archive.NewDigest(sum); got != want.Digest {
		return fmt.Errorf("reassembled content has digest %s, want %s", got, want.Digest)
	}
	return nil
}
