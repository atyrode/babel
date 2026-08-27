package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// PartitionOf maps a canonical session-key string to its manifest
// partition: the first byte of sha256(session_key) in lowercase hex,
// yielding at most 256 partitions. Every revision of a session lives in
// the session's partition, so an unchanged partition produces
// byte-identical canonical segment bytes across generations and is reused
// by digest.
func PartitionOf(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return hex.EncodeToString(sum[:1])
}

// BuiltSegment pairs a segment with its canonical bytes and reference.
type BuiltSegment struct {
	Segment Segment
	Bytes   []byte
	Ref     SegmentRef
}

// BuildSegments partitions entries, sorts each partition by
// (SessionKey, RevisionKey), and produces canonical segment bytes plus
// content-addressed references, ordered by partition.
func BuildSegments(entries []ManifestEntry) ([]BuiltSegment, error) {
	byPartition := make(map[string][]ManifestEntry)
	for _, e := range entries {
		p := PartitionOf(e.SessionKey)
		byPartition[p] = append(byPartition[p], e)
	}
	partitions := make([]string, 0, len(byPartition))
	for p := range byPartition {
		partitions = append(partitions, p)
	}
	sort.Strings(partitions)

	built := make([]BuiltSegment, 0, len(partitions))
	for _, p := range partitions {
		es := byPartition[p]
		sort.Slice(es, func(i, j int) bool {
			if es[i].SessionKey != es[j].SessionKey {
				return es[i].SessionKey < es[j].SessionKey
			}
			return es[i].RevisionKey < es[j].RevisionKey
		})
		for i := 1; i < len(es); i++ {
			if es[i].RevisionKey == es[i-1].RevisionKey {
				return nil, fmt.Errorf("archive: duplicate revision key %q", es[i].RevisionKey)
			}
		}
		seg := Segment{SegmentSchema: SegmentSchemaVersion, Partition: p, Entries: es}
		b, err := MarshalCanonical(seg)
		if err != nil {
			return nil, fmt.Errorf("archive: marshal segment %s: %w", p, err)
		}
		built = append(built, BuiltSegment{
			Segment: seg,
			Bytes:   b,
			Ref: SegmentRef{
				Partition: p,
				Object:    ObjectRef{Digest: DigestBytes(b), Size: int64(len(b))},
				Entries:   len(es),
			},
		})
	}
	return built, nil
}

// CountEntries returns the distinct session count and total revision count
// of a full entry set, for generation-index totals.
func CountEntries(entries []ManifestEntry) (sessions, revisions int) {
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		seen[e.SessionKey] = struct{}{}
	}
	return len(seen), len(entries)
}
