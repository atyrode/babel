package sharedcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// HostCatalogRow is what the catalog recorded for one host that has published.
//
// This is the browse surface the fleet was for: an instance holding only the
// DSN can see which hosts exist and how much each has archived without
// downloading a transcript, which is the whole reason PostgreSQL carries
// derived state at all (SPEC.md 6.2). Every field is inside the Phase A
// plaintext allowlist - host id, counts, ordering, commit state, timestamps -
// so browsing reveals nothing a selector, title, or path would.
//
// Sessions is a count and not a listing because it cannot be a listing:
// session_uid is an opaque digest, and resolving one back to something an
// operator could fetch needs the repository or a local index
// (migrations/0001_init.sql).
type HostCatalogRow struct {
	HostID string
	// Snapshots counts every catalog row for this host, pending ones included:
	// a pending row is a snapshot restic committed, so omitting it would
	// understate what the archive holds.
	Snapshots int
	// Sessions counts the distinct sessions this host has ever published. A
	// host whose rows were rebuilt from the repository listing reports 0, and
	// that 0 is observed rather than assumed: the catalog holds no session rows
	// for it, which is exactly what a rebuild leaves behind (SPEC.md 9).
	Sessions int
	// Pending counts this host's rows with no session detail, the state
	// reconciliation and rebuild leave a snapshot in.
	Pending int
	// NewestOrder is this host's maximum publication_order. The owning host
	// assigns it, and it totally orders that host's snapshots without trusting
	// clock skew between machines, so it - not a timestamp comparison - is what
	// identifies the newest row.
	NewestOrder int64
	// NewestSnapshotTime is restic's recorded time for the newest-ordered
	// snapshot, not when the catalog learned of it. The two differ by however
	// long an outage lasted: reconciliation adopts a stranded snapshot later
	// than it was taken and gives it the next order above the maximum, so the
	// newest order can carry an older time than a row beneath it
	// (ordering_test.go). Reporting the adoption time instead would claim the
	// archive is fresher than it is.
	NewestSnapshotTime time.Time
	// DisplayName is the host's newest asserted display name, empty when no
	// push has supplied one (decision 8, migrations/0004). OS and Arch are the
	// machine facts it reported alongside it, empty on the same condition.
	DisplayName string
	OS          string
	Arch        string
	// FirstSeenAt is when this catalog first held a row for the host, which is
	// its first publication: hosts.created_at, assigned by PostgreSQL and never
	// rewritten. It is not when the machine was built or configured, and the
	// catalog has no way to know either of those.
	FirstSeenAt time.Time
	// IdentityUpdatedAt is when DisplayName, OS and Arch were last asserted,
	// zero when never. A host whose last push predates a rename still shows the
	// old name, and this is what says so.
	IdentityUpdatedAt time.Time
}

// hostCatalogQuery aggregates one row per host that has published.
//
// Relations are unqualified because Open pins search_path to Babel's own
// schema, which is what keeps this query from resolving against a table a
// provider extension or an operator put in `public`.
//
// The join back to snapshots reads the newest row's time rather than
// max(snapshot_time), which would be a different snapshot whenever publication
// order and snapshot time disagree. UNIQUE (host_id, publication_order) makes
// the join match exactly one row, so the time it carries is unambiguous.
//
// The join to hosts is inner rather than outer on purpose: snapshots.host_id
// references hosts, so a snapshot row without its host row cannot exist, and
// pretending otherwise would hide a broken foreign key behind an empty name.
const hostCatalogQuery = `
	SELECT agg.host_id,
	       agg.snapshots,
	       coalesce(sess.sessions, 0),
	       agg.pending,
	       agg.newest_order,
	       newest.snapshot_time,
	       coalesce(h.display_name, ''),
	       coalesce(h.os, ''),
	       coalesce(h.arch, ''),
	       h.created_at,
	       h.identity_updated_at
	  FROM (
	        SELECT host_id,
	               count(*)                                 AS snapshots,
	               count(*) FILTER (WHERE commit_state = $1) AS pending,
	               max(publication_order)                   AS newest_order
	          FROM snapshots
	         GROUP BY host_id
	       ) agg
	  JOIN snapshots newest
	    ON newest.host_id = agg.host_id
	   AND newest.publication_order = agg.newest_order
	  JOIN hosts h
	    ON h.host_id = agg.host_id
	  LEFT JOIN (
	        SELECT host_id, count(DISTINCT session_uid) AS sessions
	          FROM sessions
	         GROUP BY host_id
	       ) sess
	    ON sess.host_id = agg.host_id
	 ORDER BY agg.host_id`

// HostCatalog reports what the shared catalog holds, one row per host that has
// published, ordered by host id.
//
// It reads and nothing more: a host with no snapshots is absent rather than
// reported as empty, because a registered host that never pushed has nothing in
// the catalog to describe. An empty catalog is therefore no rows and no error -
// a new deployment is a normal state, not a malfunction.
func HostCatalog(ctx context.Context, db *sql.DB) ([]HostCatalogRow, error) {
	// A second instance may be pointed at a database nothing has migrated yet,
	// and a raw "relation does not exist" from whichever aggregate ran first
	// tells its operator nothing. The gate names the command that fixes it, and
	// refuses a schema a newer Babel wrote rather than reading columns this
	// binary may misunderstand.
	if err := EnsureCompatible(ctx, db); err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, hostCatalogQuery, CommitPending)
	if err != nil {
		return nil, fmt.Errorf("read host catalog: %w", err)
	}
	defer rows.Close()

	var out []HostCatalogRow
	for rows.Next() {
		var r HostCatalogRow
		// identity_updated_at is NULL until a push asserts identity, and a
		// zero time is how that reaches a caller: "never reported" rather than
		// a scan failure or an invented instant.
		var identityUpdated sql.NullTime
		if err := rows.Scan(&r.HostID, &r.Snapshots, &r.Sessions,
			&r.Pending, &r.NewestOrder, &r.NewestSnapshotTime,
			&r.DisplayName, &r.OS, &r.Arch, &r.FirstSeenAt, &identityUpdated); err != nil {
			return nil, fmt.Errorf("scan host catalog row: %w", err)
		}
		r.IdentityUpdatedAt = identityUpdated.Time
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read host catalog: %w", err)
	}
	return out, nil
}

// CatalogSessionRow is one session as the shared catalog holds it, including
// the metadata migrations/0004 admits.
//
// This is what makes a cross-host listing readable: before 0004 a second
// instance could count another machine's sessions but could only name them by
// digest. It still cannot fetch one from these fields alone - the selector is
// deliberately absent - so a fetch goes through the repository or a local index.
//
// Every optional field distinguishes absent from present, because a reader on
// another machine meets absence routinely: only the publishing host resolves
// these values, and rows published before 0004, or by a binary that predates
// it, carry none of them.
type CatalogSessionRow struct {
	SessionUID string
	HostID     string
	Harness    string
	// Title is the short summary the publishing host resolved, empty when no
	// push has supplied one. It may be a title the harness recorded, one Babel
	// derived from the session's records, or one a model wrote; TitleProvenance
	// is the only thing that says which, and a reader that shows the title
	// without it is presenting all three as the same kind of fact.
	Title string
	// TitleProvenance is "recorded", "derived", "inferred", or empty when the
	// publishing host supplied none - notably for every row written before
	// migrations/0005. Empty means unknown and never "recorded".
	TitleProvenance string
	// Workspace is the directory the session ran in on the publishing machine,
	// empty when no push has supplied one.
	Workspace string
	// ContinuationGrade is nil when unknown, which is the case a cross-host
	// reader must not collapse into false. The grade is resolved from the
	// publishing host's own files - artifact closure and unresolved blobs - so
	// no other instance can recompute it, and "not gradeable" and "nobody has
	// graded this" are different statements about a session.
	ContinuationGrade   *bool
	PrimarySize         int64
	ArtifactCount       int
	BlobCount           int
	UnresolvedBlobCount int
	SourceModifiedAt    *time.Time
	// LatestSnapshotID is the newest snapshot this session was published in,
	// which is the restic snapshot a fetch would read it from.
	LatestSnapshotID string
}

// hostSessionsQuery lists sessions, optionally narrowed to one host. An empty
// $1 selects every host, so one statement serves the fleet view and the
// per-host view without assembling SQL from a condition.
//
// The order is total: host then uid, both non-null, so paging or diffing two
// reads is well defined.
const hostSessionsQuery = `
	SELECT session_uid, host_id, harness,
	       coalesce(title, ''), coalesce(title_provenance, ''),
	       coalesce(workspace, ''), continuation_grade,
	       primary_size, artifact_count, blob_count, unresolved_blob_count,
	       source_modified_at, latest_snapshot_id
	  FROM sessions
	 WHERE $1::text = '' OR host_id = $1
	 ORDER BY host_id, session_uid`

// HostSessions lists the sessions the catalog holds for one host, or for every
// host when hostID is empty, in a total order.
//
// A host the catalog knows nothing about is no rows and no error: absence is a
// legitimate answer here, and it is the same answer a host whose rows were
// rebuilt from the repository listing gives, because a rebuild leaves no
// session rows at all (SPEC.md 9).
func HostSessions(ctx context.Context, db *sql.DB, hostID string) ([]CatalogSessionRow, error) {
	// Same gate as HostCatalog: a second instance may be pointed at an
	// unmigrated database, and a raw "column does not exist" would tell its
	// operator nothing about which command fixes it.
	if err := EnsureCompatible(ctx, db); err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, hostSessionsQuery, hostID)
	if err != nil {
		return nil, fmt.Errorf("read catalog sessions: %w", err)
	}
	defer rows.Close()

	var out []CatalogSessionRow
	for rows.Next() {
		var r CatalogSessionRow
		var grade sql.NullBool
		var modified sql.NullTime
		if err := rows.Scan(&r.SessionUID, &r.HostID, &r.Harness,
			&r.Title, &r.TitleProvenance, &r.Workspace, &grade,
			&r.PrimarySize, &r.ArtifactCount, &r.BlobCount, &r.UnresolvedBlobCount,
			&modified, &r.LatestSnapshotID); err != nil {
			return nil, fmt.Errorf("scan catalog session row: %w", err)
		}
		// A NULL grade stays nil rather than becoming false. This is the whole
		// reason the column is a nullable boolean.
		if grade.Valid {
			g := grade.Bool
			r.ContinuationGrade = &g
		}
		if modified.Valid {
			t := modified.Time
			r.SourceModifiedAt = &t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read catalog sessions: %w", err)
	}
	return out, nil
}
