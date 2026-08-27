// Package catalog implements Babel's read side of the babel/v1 archive
// (SPEC.md §6.2): it merges every host's verified committed generation into
// a queryable in-memory catalog, verifies a remote in two tiers
// (`babel archive verify [--deep]`, SPEC.md §8), and materializes one
// selected immutable revision with full digest verification (SPEC.md §11).
//
// Trust model. Only digest-verified committed state is ever exposed. The
// mutable `latest` pointer is read for cross-checking only: a stale,
// dangling, or hostile hint can be reported but can never change a result,
// because selection always runs the verified-record scan (SPEC.md §6.1).
// Failure degrades explicitly — a generation whose manifest bytes do not
// verify is skipped in favour of the newest older generation that does, and
// the reason is recorded on the host rather than swallowed.
//
// Nothing here writes to the object store; catalog, verify, inspect, and
// fetch are read-only with respect to the remote (SPEC.md §8).
//
// Logs and errors carry keys, digests, counts, and paths only, never
// session content (SPEC.md §9).
package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// ErrNotFound is the sentinel behind every unresolved selector.
var ErrNotFound = errors.New("catalog: selector matches no committed revision")

// maxNearMatches bounds the suggestions attached to an unknown selector.
const maxNearMatches = 8

// Revision is one immutable session revision as committed by its host.
type Revision struct {
	// Entry is the verified manifest entry describing the revision.
	Entry archive.ManifestEntry
	// HostID is the host whose committed generation exposed the entry.
	HostID string
	// Generation is that generation's number, which is the entry set an
	// append-delta chain is resolved within. It is not necessarily the
	// generation that first published the revision
	// (see Entry.GenerationAdded).
	Generation uint64

	// entries is the generation's entry set, shared by every revision of
	// the generation and used to walk append-delta parents. A Revision
	// built outside Load carries none, so only full revisions of such a
	// value can be reassembled.
	entries map[string]archive.ManifestEntry
}

// Key returns the immutable revision key.
func (r Revision) Key() string { return r.Entry.RevisionKey }

// SessionKeyString returns the canonical session key owning the revision.
func (r Revision) SessionKeyString() string { return r.Entry.SessionKey }

// Session is one session's merged catalog row: every committed revision
// plus the display metadata of the newest one. Nullable common fields stay
// nil and are explained by Completeness; Babel never synthesizes a value to
// satisfy the shape (SPEC.md §3).
type Session struct {
	// Key is the globally unique session identity; its Harness, HostID,
	// and SourceID fields are the authoritative harness/host/source view.
	Key archive.SessionKey

	// Newest is the newest committed stable revision, the target of a bare
	// session selector (SPEC.md §6.2). It is always Revisions[0].
	Newest Revision
	// Revisions holds every committed revision, newest first.
	Revisions []Revision

	SnapshotTime time.Time
	Title        *string
	Workspace    *string
	CreatedAt    *time.Time
	ModifiedAt   *time.Time
	Lifecycle    *string
	Repo         *archive.RepoFingerprint

	Completeness      []archive.CompletenessReason
	ContinuationGrade bool
}

// RevisionCount reports how many committed revisions the session has.
func (s Session) RevisionCount() int { return len(s.Revisions) }

// HostInfo describes what one host contributes to the catalog: the
// generation actually exposed, its coverage metadata, how the mutable hint
// compares, and every anomaly observed while reading it.
type HostInfo struct {
	HostID      string
	DisplayName string

	// Generation is the exposed committed generation, 0 when the host has
	// none this reader can use (see Err).
	Generation   uint64
	CommitKey    string
	CommitDigest archive.Digest
	CommittedAt  time.Time

	Coverage          []archive.AdapterCoverage
	Bootstrap         bool
	BootstrapComplete bool
	BabelVersion      string

	// Sessions and Revisions count what this host contributed.
	Sessions  int
	Revisions int

	// HintPresent, HintGeneration, and HintCommit report the
	// non-authoritative `latest` pointer; HintStale marks a hint that does
	// not name the generation selection actually chose.
	HintPresent    bool
	HintGeneration uint64
	HintCommit     archive.Digest
	HintStale      bool

	// Skipped lists committed generations passed over because their bytes
	// did not verify, newest first. A non-empty value means the exposed
	// generation is older than the host's highest commit record.
	Skipped []string
	// Anomalies lists contract violations found inside verified bytes.
	Anomalies []string
	// Err explains why the host contributes nothing at all; empty
	// otherwise. A host with no commit record yet is not an error.
	Err string
}

// Catalog is the merged read-only view of every requested host's committed
// state. It is immutable once loaded; refreshing means loading again.
type Catalog struct {
	hosts     []HostInfo
	hostIndex map[string]int

	sessions     []Session
	sessionIndex map[string]int
	revisions    map[string]Revision
}

// Load reads each host's committed generation and merges every manifest
// entry into a queryable catalog (SPEC.md §6.2). When hosts is empty the
// host set is discovered by listing the archive's hosts prefix.
//
// Load fails only on an unusable store or an invalid requested host name.
// A host whose commit records are absent, corrupt, or unreadable is
// recorded on its HostInfo and contributes nothing: one damaged host never
// hides the rest of the archive.
func Load(ctx context.Context, st objectstore.Store, hosts []string) (*Catalog, error) {
	if st == nil {
		return nil, errors.New("catalog: nil object store")
	}
	ids, err := hostList(ctx, st, hosts)
	if err != nil {
		return nil, err
	}

	c := &Catalog{
		hostIndex:    make(map[string]int, len(ids)),
		sessionIndex: make(map[string]int),
		revisions:    make(map[string]Revision),
	}
	grouped := make(map[string][]Revision)

	for _, id := range ids {
		info := HostInfo{HostID: id}

		// The hint is a cross-check only: selection never reads through it.
		if hint, err := archive.ReadLatestHint(ctx, st, id); err != nil {
			info.Anomalies = append(info.Anomalies, "latest hint unreadable: "+err.Error())
		} else if hint != nil {
			info.HintPresent = true
			info.HintGeneration = hint.Generation
			info.HintCommit = hint.Commit.Digest
		}

		gen, skipped, err := selectGeneration(ctx, st, id)
		info.Skipped = skipped
		switch {
		case err != nil:
			info.Err = err.Error()
		case gen == nil: // host has published no commit record yet
		default:
			c.mergeGeneration(&info, gen, grouped)
		}

		c.hostIndex[id] = len(c.hosts)
		c.hosts = append(c.hosts, info)
	}

	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	c.sessions = make([]Session, 0, len(keys))
	for _, k := range keys {
		c.sessionIndex[k] = len(c.sessions)
		c.sessions = append(c.sessions, newSession(grouped[k]))
	}
	return c, nil
}

// mergeGeneration folds one host's committed generation into the catalog.
func (c *Catalog) mergeGeneration(info *HostInfo, gen *generation, grouped map[string][]Revision) {
	info.Generation = gen.Record.Generation
	info.DisplayName = gen.Record.HostDisplayName
	info.CommitKey = gen.Key
	info.CommitDigest = gen.RecordDigest
	info.CommittedAt = gen.Record.CreatedAt
	info.Coverage = gen.Record.Coverage
	info.Bootstrap = gen.Record.Bootstrap
	info.BootstrapComplete = gen.Record.BootstrapComplete
	info.BabelVersion = gen.Record.BabelVersion
	info.Anomalies = append(info.Anomalies, gen.Anomalies...)
	if info.HintPresent && (info.HintGeneration != gen.Record.Generation || info.HintCommit != gen.RecordDigest) {
		info.HintStale = true
	}

	sessions := make(map[string]struct{}, len(gen.Entries))
	for _, e := range gen.Entries {
		if _, dup := c.revisions[e.RevisionKey]; dup {
			info.Anomalies = append(info.Anomalies, "revision key already claimed by another host: "+e.RevisionKey)
			continue
		}
		rev := Revision{Entry: e, HostID: gen.HostID, Generation: gen.Record.Generation, entries: gen.ByRevision}
		c.revisions[e.RevisionKey] = rev
		grouped[e.SessionKey] = append(grouped[e.SessionKey], rev)
		sessions[e.SessionKey] = struct{}{}
		info.Revisions++
	}
	info.Sessions = len(sessions)
}

// newSession orders a session's revisions newest first and derives its
// display row from the newest one.
func newSession(revs []Revision) Session {
	sort.Slice(revs, func(i, j int) bool { return newerRevision(revs[i].Entry, revs[j].Entry) })
	newest := revs[0]
	e := newest.Entry
	// The session key was validated when the entry was accepted.
	key, _ := archive.ParseSessionKey(e.SessionKey)
	return Session{
		Key:               key,
		Newest:            newest,
		Revisions:         revs,
		SnapshotTime:      e.SnapshotTime,
		Title:             e.Title,
		Workspace:         e.Workspace,
		CreatedAt:         e.CreatedAt,
		ModifiedAt:        e.ModifiedAt,
		Lifecycle:         e.Lifecycle,
		Repo:              e.Repo,
		Completeness:      e.Completeness,
		ContinuationGrade: e.ContinuationGrade,
	}
}

// newerRevision reports whether a precedes b in newest-first order: higher
// GenerationAdded, then later snapshot time, then higher revision key. The
// revision-key tie-break makes the newest committed stable revision a total
// function of committed bytes, so every reader agrees (SPEC.md §6.1).
func newerRevision(a, b archive.ManifestEntry) bool {
	if a.GenerationAdded != b.GenerationAdded {
		return a.GenerationAdded > b.GenerationAdded
	}
	if !a.SnapshotTime.Equal(b.SnapshotTime) {
		return a.SnapshotTime.After(b.SnapshotTime)
	}
	return a.RevisionKey > b.RevisionKey
}

// hostList resolves the host set to read: the requested names, validated
// and deduplicated, or every discoverable host when none is requested.
func hostList(ctx context.Context, st objectstore.Store, hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		return discoverHosts(ctx, st)
	}
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if !archive.ValidName(h) {
			return nil, fmt.Errorf("catalog: invalid host ID %q", h)
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// Sessions returns every merged session in canonical session-key order.
// The returned values are read-only views of the loaded catalog.
func (c *Catalog) Sessions() []Session {
	out := make([]Session, len(c.sessions))
	copy(out, c.sessions)
	return out
}

// Session returns one session by canonical session key.
func (c *Catalog) Session(key string) (Session, bool) {
	if i, ok := c.sessionIndex[key]; ok {
		return c.sessions[i], true
	}
	return Session{}, false
}

// Hosts returns every requested host's coverage and generation info, in
// ascending host-ID order.
func (c *Catalog) Hosts() []HostInfo {
	out := make([]HostInfo, len(c.hosts))
	copy(out, c.hosts)
	return out
}

// Host returns one host's coverage and generation info.
func (c *Catalog) Host(hostID string) (HostInfo, bool) {
	if i, ok := c.hostIndex[hostID]; ok {
		return c.hosts[i], true
	}
	return HostInfo{}, false
}

// UnknownSelectorError reports a selector that names no committed revision,
// with the closest session or revision keys as suggestions. It wraps
// ErrNotFound.
type UnknownSelectorError struct {
	Selector    string
	NearMatches []string
}

func (e *UnknownSelectorError) Error() string {
	if len(e.NearMatches) == 0 {
		return fmt.Sprintf("catalog: no committed revision for %q", e.Selector)
	}
	return fmt.Sprintf("catalog: no committed revision for %q; near matches: %s",
		e.Selector, strings.Join(e.NearMatches, ", "))
}

func (e *UnknownSelectorError) Unwrap() error { return ErrNotFound }

// Resolve turns a selector into one immutable revision (SPEC.md §6.2). A
// bare canonical session key resolves to that session's newest committed
// stable revision; an exact "SESSION@sha256:<hex>" selector resolves that
// revision reproducibly and never drifts.
func (c *Catalog) Resolve(selector string) (Revision, error) {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return Revision{}, &UnknownSelectorError{Selector: selector}
	}
	if strings.ContainsRune(sel, '@') {
		if _, _, err := archive.ParseRevisionKey(sel); err != nil {
			return Revision{}, fmt.Errorf("catalog: %w: %v", ErrNotFound, err)
		}
		if rev, ok := c.revisions[sel]; ok {
			return rev, nil
		}
		return Revision{}, &UnknownSelectorError{Selector: sel, NearMatches: c.nearMatches(sel)}
	}
	if s, ok := c.Session(sel); ok {
		return s.Newest, nil
	}
	return Revision{}, &UnknownSelectorError{Selector: sel, NearMatches: c.nearMatches(sel)}
}

// nearMatches suggests keys for an unresolved selector. A revision selector
// whose session exists suggests that session's revision keys; otherwise
// sessions are matched by source-id suffix, then by source-id substring.
// Only keys are suggested: titles and workspaces are session content.
func (c *Catalog) nearMatches(selector string) []string {
	sel := selector
	if i := strings.LastIndexByte(sel, '@'); i >= 0 {
		sel = sel[:i]
		if s, ok := c.Session(sel); ok {
			out := make([]string, 0, len(s.Revisions))
			for _, r := range s.Revisions {
				if len(out) == maxNearMatches {
					break
				}
				out = append(out, r.Entry.RevisionKey)
			}
			return out
		}
	}
	if sel == "" {
		return nil
	}
	var suffix, substr []string
	for _, s := range c.sessions {
		switch {
		case strings.HasSuffix(s.Key.SourceID, sel) || strings.HasSuffix(s.Key.String(), sel):
			suffix = append(suffix, s.Key.String())
		case strings.Contains(s.Key.SourceID, sel):
			substr = append(substr, s.Key.String())
		}
	}
	out := append(suffix, substr...)
	if len(out) > maxNearMatches {
		out = out[:maxNearMatches]
	}
	return out
}
