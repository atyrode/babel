package cli

// The fleet corpus is every session the archive holds, from whichever machine
// archived it, materialized here and discoverable as sessions rather than as a
// list of directories the caller had to assemble (SPEC.md §6.3: the catalog is
// fleet-wide and a miss is a fetch, not a refusal).
//
// Two halves live in this file. Fetching records where a session came from,
// because the mirror layout cannot: a fetched tree is named after the session
// and the snapshot, and neither names a machine. Discovery reads that record
// back and re-identifies the tree, so a session fetched out of another host's
// archive is offered to analysis under the identity its own machine assigns it
// and attributed to that machine rather than to this one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/restic"
)

// originSuffix names the sidecar recording one fetched tree's origin. It sits
// beside the tree rather than inside it: the tree is a byte-for-byte mirror of
// what the snapshot held, and a file Babel wrote into it would be discovered,
// measured and reported as though the origin machine had archived it.
//
// The sidecar is still inside the session's own directory, so `sessions prune`
// removes it with the tree it describes and no stale attribution outlives the
// bytes it was about.
const originSuffix = ".origin.json"

// fetchedOrigin is the record one fetch leaves about where the bytes came from.
//
// Host is read from the snapshot restic restored, which is the identity `archive
// push` recorded on the machine that archived the session — the same authority
// `sessions list --host` and `sessions fetch --host` resolve a host against. It
// is never this machine's hostname, and never inferred from a path: a fetched
// session filed under the fetching machine would make every cross-host analysis
// claim the wrong provenance, invisibly.
type fetchedOrigin struct {
	// Host is the machine whose archive this tree came out of.
	Host string `json:"host"`
	// Selector is the session the fetch asked for, kept so a tree whose
	// contents can no longer be identified can still be named in a report.
	Selector string `json:"selector"`
	// SnapshotID is the full snapshot id the bytes were restored from; the
	// directory name carries only its short form.
	SnapshotID string `json:"snapshot_id"`
	// FetchedAt is when this machine materialized the tree, which is not when
	// the snapshot was taken.
	FetchedAt string `json:"fetched_at"`
}

// originPath is the sidecar path for one fetched tree.
func originPath(tree string) string { return tree + originSuffix }

// writeFetchedOrigin records where a fetched tree came from, replacing any
// earlier record.
//
// It is written on every fetch, including one that found the tree already
// materialized, which is what lets a resumed bulk fetch attribute a corpus that
// was downloaded before this record existed: the directory name is derived from
// the session key and the snapshot's short id, so a tree that is already here
// under that name came from exactly this snapshot and the record is a statement
// about the same bytes either way.
func writeFetchedOrigin(tree string, origin fetchedOrigin) error {
	data, err := json.MarshalIndent(origin, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the origin of %s: %w", tree, err)
	}
	path := originPath(tree)
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// readFetchedOrigin reads one fetched tree's origin record. An absent record is
// reported as fs.ErrNotExist, which is the state of every tree fetched by a
// build older than this one, and a record naming no host is treated the same
// way: neither says which machine the session belongs to, and both are repaired
// by fetching again rather than by guessing.
func readFetchedOrigin(tree string) (fetchedOrigin, error) {
	data, err := os.ReadFile(originPath(tree))
	if err != nil {
		return fetchedOrigin{}, err
	}
	var origin fetchedOrigin
	if err := json.Unmarshal(data, &origin); err != nil {
		return fetchedOrigin{}, fmt.Errorf("decode %s: %w", originPath(tree), err)
	}
	if origin.Host == "" {
		return fetchedOrigin{}, fmt.Errorf("%s names no origin host: %w", originPath(tree), fs.ErrNotExist)
	}
	return origin, nil
}

// materialized is where one session's closure landed and what it cost.
type materialized struct {
	dir   string
	files int
	bytes int64
	// resumed reports that the tree was already complete, so nothing was
	// downloaded and nothing was overwritten.
	resumed bool
}

// sessionRestorer is the one repository capability materializing a session
// needs. It is an interface rather than *restic.Repo because the behaviour a
// bulk fetch has to guarantee — resuming over what is already here, and losing
// one session rather than the whole batch when a restore fails — must be
// checkable without a repository, and because a test that drove real restic
// would be asserting restic's behaviour instead of Babel's.
type sessionRestorer interface {
	Restore(ctx context.Context, snapshotID string, includes []string, target string) error
}

// fetchTree is where one session restored from one snapshot lives.
//
// It is derived from the session key and the snapshot's short id alone, with no
// repository access, which is what makes a fetch idempotent and a bulk fetch
// resumable: the same pair always resolves to the same directory, so a tree
// that is already there is the same tree this call would have produced.
func fetchTree(root, key string, snap restic.Snapshot) string {
	return filepath.Join(root, safeSessionDir(key), shortID(snap))
}

// materialize restores one session's closure out of a snapshot and records
// which machine's archive it came from.
//
// A tree that is already complete is left exactly as it is. The restore itself
// runs in a sibling ".partial" directory and is renamed on success, so the
// target exists only when its contents are complete — which is what makes that
// idempotence check a statement about complete trees rather than about whichever
// files an interrupted download happened to leave behind.
func materialize(ctx context.Context, r sessionRestorer, root, key string,
	snap restic.Snapshot, includes []string) (materialized, error) {
	if len(includes) == 0 {
		return materialized{}, fmt.Errorf("session %s has no restorable file closure", key)
	}
	dir := fetchTree(root, key, snap)
	if files, bytes, err := treeSize(dir); err == nil {
		if err := recordOrigin(dir, key, snap); err != nil {
			return materialized{}, err
		}
		return materialized{dir: dir, files: files, bytes: bytes, resumed: true}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return materialized{}, err
	}

	staging := dir + ".partial"
	if err := os.RemoveAll(staging); err != nil {
		return materialized{}, fmt.Errorf("clear %s: %w", staging, err)
	}
	if err := ensureDir(staging); err != nil {
		return materialized{}, err
	}
	if err := r.Restore(ctx, snap.ID, includes, staging); err != nil {
		os.RemoveAll(staging)
		return materialized{}, fmt.Errorf("restore %s: %w", key, err)
	}
	files, bytes, err := treeSize(staging)
	if err != nil {
		os.RemoveAll(staging)
		return materialized{}, err
	}
	if files == 0 {
		os.RemoveAll(staging)
		return materialized{}, fmt.Errorf("restore %s: snapshot %s holds none of the session's files", key, shortID(snap))
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging)
		return materialized{}, fmt.Errorf("publish %s: %w", dir, err)
	}
	if err := recordOrigin(dir, key, snap); err != nil {
		return materialized{}, err
	}
	return materialized{dir: dir, files: files, bytes: bytes}, nil
}

// recordOrigin states where a materialized tree came from, and leaves a record
// that already says so exactly as it is.
//
// Both halves matter. Writing it on a resumed fetch is what attributes a corpus
// materialized by a build that kept no such record — re-fetching downloads
// nothing and repairs the attribution. Leaving a correct one untouched is what
// keeps a resumed fetch a genuine no-op: a record rewritten on every pass would
// make "nothing changed" unobservable, and its timestamp would claim the bytes
// arrived on the pass that only looked at them.
//
// The time is the tree's own modification time rather than the clock. On a
// fresh restore those are the same instant; on a tree fetched last week they
// differ, and the mtime is when the bytes actually landed.
//
// A record that cannot be read is treated as one that is not there. The sidecar
// is derived state whose only authority is the snapshot it was written from, so
// a truncated or half-written one is repaired here rather than failing a fetch
// that has the answer in hand.
func recordOrigin(tree, key string, snap restic.Snapshot) error {
	want := fetchedOrigin{Host: snap.Host, Selector: key, SnapshotID: snap.ID}
	if got, err := readFetchedOrigin(tree); err == nil &&
		got.Host == want.Host && got.Selector == want.Selector && got.SnapshotID == want.SnapshotID {
		return nil
	}
	info, err := os.Stat(tree)
	if err != nil {
		return err
	}
	want.FetchedAt = formatTime(info.ModTime().UTC())
	return writeFetchedOrigin(tree, want)
}

// fetchedCorpus discovers the sessions this machine has fetched out of the
// fleet's archives, each attributed to the machine that archived it.
//
// This is what makes the fetched corpus addressable as a corpus. The trees are
// scattered by design — one directory per session and snapshot, each mirroring
// another machine's absolute paths — so the alternative is a caller assembling
// hundreds of source roots by hand and still not knowing which host any of them
// came from.
//
// A tree with no origin record is counted and skipped rather than attributed
// here. Guessing would mean filing another machine's session under this one,
// which is the one error a fleet-wide analysis cannot detect afterwards; and the
// remedy is cheap, because fetching again over a materialized tree downloads
// nothing and writes the record.
func fetchedCorpus(ctx context.Context, root string, ads []adapter.Adapter) (sessions []localSession, unattributed int, err error) {
	trees, unattributed, err := fetchedTrees(root)
	if err != nil {
		return nil, 0, err
	}
	for _, tree := range trees {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		found, err := identifyFetched(tree.dir, tree.origin, ads)
		if err != nil {
			return nil, 0, err
		}
		sessions = append(sessions, found...)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].key() < sessions[j].key() })
	return sessions, unattributed, nil
}

// fetchedTree is one materialized session tree and the origin it recorded.
type fetchedTree struct {
	dir    string
	origin fetchedOrigin
}

// fetchedTrees enumerates the complete, attributed trees under Babel's fetched
// session area, and counts the ones that are not.
//
// The layout is fixed by the fetch naming rule: one directory per session
// selector, holding one directory per snapshot it was fetched from. A
// ".partial" sibling is an interrupted restore rather than a session, and an
// absent area is an empty corpus rather than a failure — nothing has been
// fetched yet on a machine that has never fetched.
func fetchedTrees(root string) (trees []fetchedTree, unattributed int, err error) {
	selectors, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("read %s: %w", root, err)
	}
	for _, selector := range selectors {
		if !selector.IsDir() {
			continue
		}
		dir := filepath.Join(root, selector.Name())
		snapshots, err := os.ReadDir(dir)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", dir, err)
		}
		for _, snapshot := range snapshots {
			if !snapshot.IsDir() || strings.HasSuffix(snapshot.Name(), ".partial") {
				continue
			}
			tree := filepath.Join(dir, snapshot.Name())
			origin, err := readFetchedOrigin(tree)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					unattributed++
					continue
				}
				return nil, 0, err
			}
			trees = append(trees, fetchedTree{dir: tree, origin: origin})
		}
	}
	return trees, unattributed, nil
}

// identifyFetched reads one fetched tree back into discovered sessions.
//
// restic recreates each recorded absolute path beneath the target, so stripping
// the tree's own prefix off every file recovers the path the snapshot holds, and
// the adapters' snapshot identification — the same layout rule a local scan
// applies to directory entries, proven equal to it in each adapter's tests —
// names the sessions those paths carry. Two things follow, and both are why
// identification is used here rather than a re-rooted directory scan: a fetched
// session keeps the identity its own machine assigns it, and this code needs to
// know nothing about where any harness keeps its root.
//
// Description then reads the local mirror, because every adapter resolves a
// session's closure relative to its primary log rather than to an ambient home
// directory. So a fetched session is described, digested and indexed exactly as
// a local one is.
func identifyFetched(tree string, origin fetchedOrigin, ads []adapter.Adapter) ([]localSession, error) {
	var files []adapter.ArchivedFile
	err := filepath.WalkDir(tree, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tree, path)
		if err != nil {
			return err
		}
		// The snapshot recorded an absolute path and restic dropped its
		// leading separator to nest it under the target, so it is restored
		// here to recover the path identification has to match.
		files = append(files, adapter.ArchivedFile{Path: "/" + filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the fetched tree %s: %w", tree, err)
	}

	var out []localSession
	for _, ad := range ads {
		identifier, ok := ad.(adapter.SnapshotIdentifier)
		if !ok {
			continue
		}
		found, err := identifier.IdentifyArchived(files)
		if err != nil {
			return nil, fmt.Errorf("identify %s sessions in %s: %w", ad.Harness(), tree, err)
		}
		for _, s := range found {
			out = append(out, localSession{
				owner: ad,
				src: adapter.SourceSession{
					Harness:     ad.Harness(),
					SourceID:    s.SourceID,
					PrimaryPath: filepath.Join(tree, filepath.FromSlash(strings.TrimPrefix(s.PrimaryPath, "/"))),
					// The hint is the path the session has on its own
					// machine, which is the one thing about a fetched
					// session that the local mirror path cannot show.
					Hint: s.PrimaryPath,
				},
				origin: origin.Host,
			})
		}
	}
	return out, nil
}
