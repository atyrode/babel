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
const hostCatalogQuery = `
	SELECT agg.host_id,
	       agg.snapshots,
	       coalesce(sess.sessions, 0),
	       agg.pending,
	       agg.newest_order,
	       newest.snapshot_time
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
		if err := rows.Scan(&r.HostID, &r.Snapshots, &r.Sessions,
			&r.Pending, &r.NewestOrder, &r.NewestSnapshotTime); err != nil {
			return nil, fmt.Errorf("scan host catalog row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read host catalog: %w", err)
	}
	return out, nil
}
