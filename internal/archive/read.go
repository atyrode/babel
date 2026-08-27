package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/atyrode/babel/internal/objectstore"
)

// Head is a host's highest fully verified committed generation.
type Head struct {
	Key          string
	Commit       CommitRecord
	CommitDigest Digest
	Index        GenerationIndex
}

// VerifiedHead selects a host's current committed generation per the
// frozen reader rule (SPEC.md §6.1): enumerate commit records in canonical
// key order, verify candidates from the highest down, and return the first
// that fully verifies. It returns (nil, nil) when the host has no commit
// records at all, and an error when records exist but none verifies —
// corruption and infrastructure failure are indistinguishable here and
// must never silently yield an older generation... except older VERIFIED
// generations, which are exactly the fallback the contract requires.
//
// The latest hint is intentionally not consulted: it is a non-authoritative
// optimization, and the scan is the semantics. Callers wanting the fast
// path may check the hint first and fall back here.
func VerifiedHead(ctx context.Context, st objectstore.Store, hostID string) (*Head, error) {
	infos, err := st.List(ctx, CommitPrefix(hostID))
	if err != nil {
		return nil, fmt.Errorf("archive: list commit records for %s: %w", hostID, err)
	}
	keys := make([]string, 0, len(infos))
	for _, in := range infos {
		if _, _, ok := ParseCommitKey(in.Key); ok {
			keys = append(keys, in.Key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	var firstErr error
	for _, key := range keys {
		h, err := verifyCandidate(ctx, st, hostID, key)
		if err == nil {
			return h, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("archive: candidate %s: %w", key, err)
		}
	}
	return nil, fmt.Errorf("archive: no verifiable commit record among %d for %s (first failure: %w)", len(keys), hostID, firstErr)
}

func verifyCandidate(ctx context.Context, st objectstore.Store, hostID, key string) (*Head, error) {
	gen, wantDigest, ok := ParseCommitKey(key)
	if !ok {
		return nil, errors.New("malformed key")
	}
	raw, err := readAll(ctx, st, key)
	if err != nil {
		return nil, err
	}
	if got := DigestBytes(raw); got != wantDigest {
		return nil, fmt.Errorf("record digest %s does not match key", got)
	}
	var rec CommitRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("parse commit record: %w", err)
	}
	if rec.CommitSchema != CommitSchemaVersion {
		return nil, fmt.Errorf("unsupported commit schema %d", rec.CommitSchema)
	}
	if rec.HostID != hostID || rec.Generation != gen {
		return nil, fmt.Errorf("commit record identity mismatch (host %q gen %d)", rec.HostID, rec.Generation)
	}
	idx, err := loadIndex(ctx, st, rec)
	if err != nil {
		return nil, err
	}
	for _, seg := range idx.Segments {
		in, err := st.Stat(ctx, CASKey(seg.Object.Digest))
		if err != nil {
			return nil, fmt.Errorf("segment %s: %w", seg.Partition, err)
		}
		if in.Size != seg.Object.Size {
			return nil, fmt.Errorf("segment %s size %d, want %d", seg.Partition, in.Size, seg.Object.Size)
		}
	}
	return &Head{Key: key, Commit: rec, CommitDigest: wantDigest, Index: *idx}, nil
}

func loadIndex(ctx context.Context, st objectstore.Store, rec CommitRecord) (*GenerationIndex, error) {
	raw, err := readVerified(ctx, st, rec.Index)
	if err != nil {
		return nil, fmt.Errorf("generation index: %w", err)
	}
	var idx GenerationIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("parse generation index: %w", err)
	}
	if idx.IndexSchema != IndexSchemaVersion {
		return nil, fmt.Errorf("unsupported index schema %d", idx.IndexSchema)
	}
	if idx.HostID != rec.HostID || idx.Generation != rec.Generation {
		return nil, errors.New("generation index identity mismatch")
	}
	return &idx, nil
}

// LoadSegment reads and fully digest-verifies one manifest segment.
func LoadSegment(ctx context.Context, st objectstore.Store, ref SegmentRef) (*Segment, error) {
	raw, err := readVerified(ctx, st, ref.Object)
	if err != nil {
		return nil, fmt.Errorf("archive: segment %s: %w", ref.Partition, err)
	}
	var seg Segment
	if err := json.Unmarshal(raw, &seg); err != nil {
		return nil, fmt.Errorf("archive: parse segment %s: %w", ref.Partition, err)
	}
	if seg.SegmentSchema != SegmentSchemaVersion {
		return nil, fmt.Errorf("archive: unsupported segment schema %d", seg.SegmentSchema)
	}
	if seg.Partition != ref.Partition || len(seg.Entries) != ref.Entries {
		return nil, fmt.Errorf("archive: segment %s does not match its reference", ref.Partition)
	}
	return &seg, nil
}

// LoadEntries reads and verifies every segment of a generation, returning
// all manifest entries in canonical partition order.
func LoadEntries(ctx context.Context, st objectstore.Store, idx GenerationIndex) ([]ManifestEntry, error) {
	entries := make([]ManifestEntry, 0, idx.Revisions)
	for _, ref := range idx.Segments {
		seg, err := LoadSegment(ctx, st, ref)
		if err != nil {
			return nil, err
		}
		entries = append(entries, seg.Entries...)
	}
	return entries, nil
}

// ReadLatestHint reads a host's pointer hint. Absent, malformed, or
// foreign hints return (nil, nil): the hint is never authoritative and a
// bad hint simply routes callers to the verified scan.
func ReadLatestHint(ctx context.Context, st objectstore.Store, hostID string) (*LatestHint, error) {
	raw, err := readAll(ctx, st, LatestKey(hostID))
	if errors.Is(err, objectstore.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h LatestHint
	if err := json.Unmarshal(raw, &h); err != nil || h.HintSchema != HintSchemaVersion || h.HostID != hostID {
		return nil, nil
	}
	return &h, nil
}

// readVerified reads a content-addressed object and verifies digest+size.
func readVerified(ctx context.Context, st objectstore.Store, ref ObjectRef) ([]byte, error) {
	raw, err := readAll(ctx, st, CASKey(ref.Digest))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != ref.Size {
		return nil, fmt.Errorf("object size %d, want %d", len(raw), ref.Size)
	}
	if got := DigestBytes(raw); got != ref.Digest {
		return nil, fmt.Errorf("object digest %s, want %s", got, ref.Digest)
	}
	return raw, nil
}

func readAll(ctx context.Context, st objectstore.Store, key string) ([]byte, error) {
	rc, err := st.Read(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
