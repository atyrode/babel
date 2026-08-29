package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/restic"
)

// archivedSession is one session identified inside a snapshot, paired with the
// harness that recognized it.
//
// It is the cross-host counterpart of localSession: same key shape, so an
// operator uses one selector vocabulary whether the session's files are on this
// machine or only in the archive.
type archivedSession struct {
	harness string
	sess    adapter.ArchivedSession
}

// key is the session's canonical selector, identical to the key a local scan
// would assign the same session.
func (s archivedSession) key() string { return s.harness + "/" + s.sess.SourceID }

// identifyArchived lists the sessions a snapshot holds, across every adapter
// that can recognize its own layout.
//
// It reads only the snapshot's file listing: no transcript bytes are
// downloaded, which is what makes enumerating another machine's archive cheap.
// An adapter that recognizes nothing contributes nothing rather than failing,
// because one snapshot legitimately holds only some harnesses' trees.
func identifyArchived(ctx context.Context, repo *restic.Repo, snapshotID string) ([]archivedSession, error) {
	entries, err := repo.Ls(ctx, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("list snapshot %s: %w", shortSnapshotID(snapshotID), err)
	}
	files := make([]adapter.ArchivedFile, 0, len(entries))
	for _, e := range entries {
		if !e.IsFile() {
			continue
		}
		files = append(files, adapter.ArchivedFile{Path: e.Path, Size: e.Size})
	}

	var out []archivedSession
	for _, ad := range adapters() {
		id, ok := ad.(adapter.SnapshotIdentifier)
		if !ok {
			continue
		}
		found, err := id.IdentifyArchived(files)
		if err != nil {
			return nil, fmt.Errorf("identify %s sessions in snapshot %s: %w",
				ad.Harness(), shortSnapshotID(snapshotID), err)
		}
		for _, s := range found {
			out = append(out, archivedSession{harness: ad.Harness(), sess: s})
		}
	}
	slices.SortFunc(out, func(a, b archivedSession) int {
		return strings.Compare(a.key(), b.key())
	})
	return out, nil
}

// archivedListing resolves which snapshot a cross-host read consults and
// enumerates the sessions it holds.
//
// Snapshot selection precedes identification, the opposite order from a local
// read: the archive rather than this machine is the authority on what the named
// host has, so nothing here scans a source tree or opens the local catalog.
func (a *app) archivedListing(ctx context.Context, c *cmd, rf *repoFlags, snapshot string) (restic.Snapshot, []archivedSession, error) {
	d, err := babelDirs()
	if err != nil {
		return restic.Snapshot{}, nil, err
	}
	repo, err := rf.open(c, d, nil)
	if err != nil {
		return restic.Snapshot{}, nil, err
	}
	snapshots, err := repo.Snapshots(ctx)
	if err != nil {
		return restic.Snapshot{}, nil, fmt.Errorf("list snapshots: %w", err)
	}
	hostSnapshots, err := snapshotsForHost(c, snapshots, rf.host)
	if err != nil {
		return restic.Snapshot{}, nil, err
	}
	chosen, err := pickSnapshot(c, hostSnapshots, snapshot)
	if err != nil {
		return restic.Snapshot{}, nil, err
	}
	sessions, err := identifyArchived(ctx, repo, chosen.ID)
	if err != nil {
		return restic.Snapshot{}, nil, err
	}
	return chosen, sessions, nil
}

// archivedRows narrows an archived listing to the selected harnesses and states
// each surviving session as a listing row.
//
// The harness filter is applied here rather than inside identification because
// one pass over the snapshot's file listing serves every adapter at once:
// restricting the adapters would not read less of the snapshot, only less of
// what has already been read. Order is identification's, which is by selector,
// so a cross-host listing sorts exactly as a local one does.
func archivedRows(sessions []archivedSession, ads []adapter.Adapter) []sessionRow {
	keep := make(map[string]struct{}, len(ads))
	for _, ad := range ads {
		keep[ad.Harness()] = struct{}{}
	}
	rows := make([]sessionRow, 0, len(sessions))
	for _, s := range sessions {
		if _, ok := keep[s.harness]; !ok {
			continue
		}
		rows = append(rows, rowFromArchived(s))
	}
	return rows
}

// resolveArchivedSelector picks one archived session, using the same three-tier
// matching as a local selector: exact key, then a segment-aligned suffix, then
// any suffix. Ambiguity within a tier is an error naming the candidates, so a
// short selector never silently fetches the wrong session.
func resolveArchivedSelector(c *cmd, sessions []archivedSession, selector string) (archivedSession, error) {
	if selector == "" {
		return archivedSession{}, c.usagef("empty selector")
	}
	var exact, aligned, loose []archivedSession
	for _, s := range sessions {
		switch k := s.key(); {
		case k == selector:
			exact = append(exact, s)
		case strings.HasSuffix(k, "/"+selector):
			aligned = append(aligned, s)
		case strings.HasSuffix(k, selector):
			loose = append(loose, s)
		}
	}
	for _, tier := range [][]archivedSession{exact, aligned, loose} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			keys := make([]string, 0, len(tier))
			for _, s := range tier {
				keys = append(keys, s.key())
			}
			return archivedSession{}, c.usagef("selector %q is ambiguous in this snapshot, it matches %d sessions: %s",
				selector, len(tier), strings.Join(keys, " "))
		}
	}
	return archivedSession{}, fmt.Errorf("no session in the selected snapshot matches selector %q", selector)
}

// snapshotsForHost narrows a snapshot list to one host.
//
// Attribution comes from the snapshot's recorded host, which is the identity
// `archive push` passed to restic - not this machine's hostname. Selecting a
// host that has published nothing is an error naming the hosts that have, since
// the alternative is silently fetching from the wrong machine.
func snapshotsForHost(c *cmd, snapshots []restic.Snapshot, host string) ([]restic.Snapshot, error) {
	var kept []restic.Snapshot
	seen := map[string]bool{}
	for _, s := range snapshots {
		seen[s.Host] = true
		if s.Host == host {
			kept = append(kept, s)
		}
	}
	if len(kept) > 0 {
		return kept, nil
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	slices.Sort(hosts)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("the repository holds no snapshots")
	}
	return nil, c.usagef("no snapshots for host %q; the repository holds snapshots for: %s",
		host, strings.Join(sanitizeAll(hosts), " "))
}

// shortSnapshotID abbreviates an id for messages without pretending to be
// restic's own short id, which only a snapshot record carries.
func shortSnapshotID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
