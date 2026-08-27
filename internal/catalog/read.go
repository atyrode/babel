package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// hostsPrefix is the key prefix enclosing every host-scoped object. It is
// derived from archive.HostPrefix so the key layout stays single-sourced in
// the archive contract (SPEC.md §6.1).
var hostsPrefix = strings.TrimSuffix(archive.HostPrefix(""), "/")

// discoverHosts returns every host ID owning at least one object under the
// hosts prefix, in ascending order. Keys that do not name a valid host ID
// are ignored: an unreadable name can never be a host this reader trusts.
func discoverHosts(ctx context.Context, st objectstore.Store) ([]string, error) {
	infos, err := st.List(ctx, hostsPrefix)
	if err != nil {
		return nil, fmt.Errorf("catalog: list hosts: %w", err)
	}
	seen := make(map[string]struct{})
	for _, in := range infos {
		rest := strings.TrimPrefix(in.Key, hostsPrefix)
		i := strings.IndexByte(rest, '/')
		if i <= 0 {
			continue
		}
		id := rest[:i]
		if !archive.ValidName(id) {
			continue
		}
		seen[id] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// commitCandidate is one parseable commit-record key of a host.
type commitCandidate struct {
	Key        string
	Generation uint64
}

// listCommitCandidates returns a host's parseable commit-record keys in
// descending canonical order — the order in which readers consider
// candidates (SPEC.md §6.1) — plus the foreign or malformed keys found
// under the commit prefix, which readers skip and verify reports.
func listCommitCandidates(ctx context.Context, st objectstore.Store, hostID string) (cands []commitCandidate, foreign []string, err error) {
	infos, err := st.List(ctx, archive.CommitPrefix(hostID))
	if err != nil {
		return nil, nil, fmt.Errorf("catalog: list commit records for %s: %w", hostID, err)
	}
	for _, in := range infos {
		gen, _, ok := archive.ParseCommitKey(in.Key)
		if !ok {
			foreign = append(foreign, in.Key)
			continue
		}
		cands = append(cands, commitCandidate{Key: in.Key, Generation: gen})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Key > cands[j].Key })
	return cands, foreign, nil
}

// generation is one committed generation of a host whose commit record,
// generation index, and every manifest segment fully digest-verify.
type generation struct {
	HostID       string
	Key          string
	Record       archive.CommitRecord
	RecordDigest archive.Digest
	Index        archive.GenerationIndex

	// Entries holds every structurally valid manifest entry of the
	// generation, in canonical partition order.
	Entries []archive.ManifestEntry
	// ByRevision indexes Entries by revision key. It is exactly the entry
	// set an append-delta chain may be resolved within.
	ByRevision map[string]archive.ManifestEntry
	// Anomalies records contract violations found inside bytes that did
	// digest-verify: an authentic publication disagreeing with itself.
	// Entries named here were excluded from Entries.
	Anomalies []string
}

// loadGeneration reads one committed generation end to end: the commit
// record verified against its write-once key, the generation index and
// every manifest segment verified by digest (archive.LoadEntries), and
// every entry validated structurally. It fails only when bytes are absent,
// unparseable, or do not match their digests — the conditions under which a
// reader must fall back to an older generation.
func loadGeneration(ctx context.Context, st objectstore.Store, hostID, key string) (*generation, error) {
	rec, recDigest, err := readCommitRecord(ctx, st, hostID, key)
	if err != nil {
		return nil, err
	}
	idx, err := readIndex(ctx, st, rec)
	if err != nil {
		return nil, err
	}
	entries, err := archive.LoadEntries(ctx, st, idx)
	if err != nil {
		return nil, err
	}

	g := &generation{
		HostID:       hostID,
		Key:          key,
		Record:       rec,
		RecordDigest: recDigest,
		Index:        idx,
		Entries:      make([]archive.ManifestEntry, 0, len(entries)),
		ByRevision:   make(map[string]archive.ManifestEntry, len(entries)),
	}
	carried := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		carried[e.SessionKey] = struct{}{}
		if verr := validateEntry(hostID, e); verr != nil {
			g.Anomalies = append(g.Anomalies, fmt.Sprintf("entry %s excluded: %v", entryLabel(e), verr))
			continue
		}
		if _, dup := g.ByRevision[e.RevisionKey]; dup {
			g.Anomalies = append(g.Anomalies, "duplicate revision key in generation: "+e.RevisionKey)
			continue
		}
		g.ByRevision[e.RevisionKey] = e
		g.Entries = append(g.Entries, e)
	}
	// The index's own totals must describe the segments it references.
	if len(entries) != idx.Revisions || len(carried) != idx.Sessions {
		g.Anomalies = append(g.Anomalies, fmt.Sprintf(
			"generation index declares %d session(s)/%d revision(s), segments carry %d/%d",
			idx.Sessions, idx.Revisions, len(carried), len(entries)))
	}
	return g, nil
}

// selectGeneration returns the newest committed generation of a host that
// this reader can expose. Selection starts from the frozen head rule
// (archive.VerifiedHead), which validates commit records and segment
// presence, and then descends the canonical candidate order until a
// generation's manifest bytes also fully digest-verify. Every skipped
// candidate is returned with its reason so callers can surface the damage
// instead of silently presenting older data.
//
// It returns (nil, nil, nil) when the host has published no commit record
// at all, and an error when records exist but none is usable.
func selectGeneration(ctx context.Context, st objectstore.Store, hostID string) (*generation, []string, error) {
	head, err := archive.VerifiedHead(ctx, st, hostID)
	if err != nil {
		return nil, nil, err
	}
	if head == nil {
		return nil, nil, nil
	}
	cands, _, err := listCommitCandidates(ctx, st, hostID)
	if err != nil {
		return nil, nil, err
	}

	var skipped []string
	started := false
	for _, c := range cands {
		if !started {
			if c.Key != head.Key {
				continue // newer than the frozen head rule accepted
			}
			started = true
		}
		g, err := loadGeneration(ctx, st, hostID, c.Key)
		if err == nil {
			return g, skipped, nil
		}
		skipped = append(skipped, fmt.Sprintf("%s: %v", c.Key, err))
	}
	return nil, skipped, fmt.Errorf("catalog: no committed generation of %s verifies (%d candidate(s) skipped)", hostID, len(skipped))
}

// readCommitRecord reads a commit record by key and verifies it against the
// write-once key contract: the key's digest is the digest of the record's
// canonical bytes, and the record must claim exactly the host and
// generation its key names (SPEC.md §6.1).
func readCommitRecord(ctx context.Context, st objectstore.Store, hostID, key string) (archive.CommitRecord, archive.Digest, error) {
	var rec archive.CommitRecord
	gen, want, ok := archive.ParseCommitKey(key)
	if !ok {
		return rec, "", fmt.Errorf("catalog: malformed commit key %q", key)
	}
	raw, err := readKey(ctx, st, key)
	if err != nil {
		return rec, "", fmt.Errorf("catalog: read commit record %s: %w", key, err)
	}
	if got := archive.DigestBytes(raw); got != want {
		return rec, "", fmt.Errorf("catalog: commit record %s has digest %s, key claims %s", key, got, want)
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, "", fmt.Errorf("catalog: parse commit record %s: %w", key, err)
	}
	if rec.CommitSchema != archive.CommitSchemaVersion {
		return rec, "", fmt.Errorf("catalog: commit record %s has unsupported schema %d", key, rec.CommitSchema)
	}
	if rec.HostID != hostID || rec.Generation != gen {
		return rec, "", fmt.Errorf("catalog: commit record %s claims host %q generation %d", key, rec.HostID, rec.Generation)
	}
	return rec, want, nil
}

// readIndex reads and digest-verifies the generation index referenced by a
// commit record.
func readIndex(ctx context.Context, st objectstore.Store, rec archive.CommitRecord) (archive.GenerationIndex, error) {
	var idx archive.GenerationIndex
	raw, err := readVerified(ctx, st, rec.Index)
	if err != nil {
		return idx, fmt.Errorf("catalog: generation index of %s g%d: %w", rec.HostID, rec.Generation, err)
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return idx, fmt.Errorf("catalog: parse generation index of %s g%d: %w", rec.HostID, rec.Generation, err)
	}
	if idx.IndexSchema != archive.IndexSchemaVersion {
		return idx, fmt.Errorf("catalog: generation index of %s g%d has unsupported schema %d", rec.HostID, rec.Generation, idx.IndexSchema)
	}
	if idx.HostID != rec.HostID || idx.Generation != rec.Generation {
		return idx, fmt.Errorf("catalog: generation index claims host %q generation %d", idx.HostID, idx.Generation)
	}
	return idx, nil
}

// validateEntry checks one manifest entry against the invariants a reader
// must not assume away: supported schema and encoding, host ownership
// (a host publishes only sessions namespaced to itself, SPEC.md §3),
// session-key composition, and the revision key's derivation from the
// reassembled-content digest.
func validateEntry(hostID string, e archive.ManifestEntry) error {
	if e.ManifestSchema != archive.ManifestSchemaVersion {
		return fmt.Errorf("unsupported manifest schema %d", e.ManifestSchema)
	}
	if hostID != "" && e.HostID != hostID {
		return fmt.Errorf("entry host %q does not own this generation", e.HostID)
	}
	key, err := archive.ParseSessionKey(e.SessionKey)
	if err != nil {
		return err
	}
	if key.Harness != e.Harness || key.HostID != e.HostID || key.SourceID != e.SourceID {
		return errors.New("session key disagrees with its harness/host/source fields")
	}
	if !e.Content.Digest.Valid() || !e.Object.Digest.Valid() {
		return errors.New("invalid content or object digest")
	}
	if e.Content.Size < 0 || e.Object.Size < 0 {
		return errors.New("negative object size")
	}
	if e.RevisionKey != key.Revision(e.Content.Digest) {
		return errors.New("revision key disagrees with its content digest")
	}
	switch e.Encoding {
	case archive.EncodingFull:
		if e.Object != e.Content {
			return errors.New("full revision payload differs from its content")
		}
	case archive.EncodingAppendDelta:
		if e.ParentRevision == "" {
			return errors.New("append-delta revision names no parent revision")
		}
		if _, _, err := archive.ParseRevisionKey(e.ParentRevision); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported encoding %q", e.Encoding)
	}
	for _, a := range e.Artifacts {
		if !a.Digest.Valid() || a.Size < 0 {
			return errors.New("artifact reference is invalid")
		}
	}
	for _, b := range e.Blobs {
		if !b.Digest.Valid() || b.Size < 0 {
			return errors.New("blob reference is invalid")
		}
	}
	return nil
}

// entryLabel renders a log-safe identity for an entry. Only keys and
// digests are ever named; titles, workspaces, and payload bytes are
// session content and never appear in logs or errors (SPEC.md §9).
func entryLabel(e archive.ManifestEntry) string {
	if e.RevisionKey != "" {
		return e.RevisionKey
	}
	if e.SessionKey != "" {
		return e.SessionKey
	}
	return "<unidentified>"
}

// chainOf resolves the append-delta chain of target within the entry set of
// one generation: it walks ParentRevision to the nearest full ancestor and
// returns the chain in application order, the full revision first. The
// reassembled plaintext is that full payload followed by every tail in the
// returned order (SPEC.md §6.1). Depth is bounded by archive.MaxChainDepth,
// and a chain that cannot be fully resolved is an error rather than a
// partial reassembly (SPEC.md §11).
func chainOf(target archive.ManifestEntry, byRevision map[string]archive.ManifestEntry) ([]archive.ManifestEntry, error) {
	chain := []archive.ManifestEntry{target}
	cur := target
	for cur.Encoding == archive.EncodingAppendDelta {
		if len(chain) >= archive.MaxChainDepth {
			return nil, fmt.Errorf("catalog: append-delta chain of %s exceeds the %d-revision bound", target.RevisionKey, archive.MaxChainDepth)
		}
		if cur.ParentRevision == "" {
			return nil, fmt.Errorf("catalog: append-delta revision %s names no parent", cur.RevisionKey)
		}
		parent, ok := byRevision[cur.ParentRevision]
		if !ok {
			return nil, fmt.Errorf("catalog: parent revision %s of %s is absent from its generation", cur.ParentRevision, cur.RevisionKey)
		}
		if parent.SessionKey != cur.SessionKey {
			return nil, fmt.Errorf("catalog: parent revision %s of %s belongs to another session", cur.ParentRevision, cur.RevisionKey)
		}
		chain = append(chain, parent)
		cur = parent
	}
	if cur.Encoding != archive.EncodingFull {
		return nil, fmt.Errorf("catalog: revision %s has unsupported encoding %q", cur.RevisionKey, cur.Encoding)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// readVerified reads a content-addressed object and verifies its size and
// plaintext digest before returning any bytes.
func readVerified(ctx context.Context, st objectstore.Store, ref archive.ObjectRef) ([]byte, error) {
	raw, err := readKey(ctx, st, archive.CASKey(ref.Digest))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != ref.Size {
		return nil, fmt.Errorf("object %s has size %d, want %d", ref.Digest, len(raw), ref.Size)
	}
	if got := archive.DigestBytes(raw); got != ref.Digest {
		return nil, fmt.Errorf("object %s has digest %s", ref.Digest, got)
	}
	return raw, nil
}

func readKey(ctx context.Context, st objectstore.Store, key string) ([]byte, error) {
	rc, err := st.Read(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
