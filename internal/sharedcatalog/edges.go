package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// This file is the shared catalog's half of the typed reference graph
// (issue #113, migrations/0008): the plaintext shape of an edge on its way out,
// and the fleet read that makes the graph navigable on a host that can open
// none of the records in it (issue #112).
//
// The asymmetry with the rest of Phase B is deliberate and is the whole point.
// Every other Phase B record reaches PostgreSQL as an identity and a reference
// to a sealed object, because its content is content. An edge's content is its
// note, and its note is sealed like any other; what travels in the clear is
// only the citation itself - which relation, from which record, to which -
// because SPEC.md §763 admits kind and identifier metadata, and because a graph
// whose shape is sealed is not a graph anyone can walk.

// EdgeKind names the meaning of one edge. The vocabulary is closed and matches
// migrations/0008's CHECK exactly: a new relation kind reaching PostgreSQL is a
// migration and a review, not a string a caller invents.
//
// It mirrors internal/reference's Kind rather than importing it. This package
// is imported by internal/reference and cannot import it back, and the copy is
// not left to agree by hope: internal/reference's own test maps every kind it
// defines through EdgeKind.Valid, so a kind added there without a migration
// here fails before it can be staged.
type EdgeKind string

const (
	EdgeEvidence   EdgeKind = "evidence"
	EdgeSupersedes EdgeKind = "supersedes"
	EdgeRefines    EdgeKind = "refines"
	EdgeAddresses  EdgeKind = "addresses"
	EdgeInspiredBy EdgeKind = "inspired_by"
	EdgeDuplicates EdgeKind = "duplicates"
)

// Valid reports whether k is one of the kinds migrations/0008 admits. It is
// exported because the writer that stages an edge has to be able to ask before
// the database answers with a constraint violation.
func (k EdgeKind) Valid() bool {
	switch k {
	case EdgeEvidence, EdgeSupersedes, EdgeRefines,
		EdgeAddresses, EdgeInspiredBy, EdgeDuplicates:
		return true
	}
	return false
}

// validNamespace bounds a record namespace: the key internal/reference's
// resolver registry uses, and a plaintext column here.
//
// It is stricter than PostgreSQL needs because a namespace is a vocabulary
// value rather than data - "session", "hypothesis", "reality_fact" - and a
// column that accepted arbitrary text would be a column a future writer could
// put a sentence in.
var validNamespace = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// RecordEdge is one edge's plaintext shape: the relation and both endpoints.
//
// It travels beside a StagedRecord rather than inside its payload, and it is
// the only Phase B structure that has a plaintext form beyond an identity. The
// justification is SPEC.md §763's, not this package's: the kind and the
// endpoint references are identifier metadata, and the note is not here.
type RecordEdge struct {
	Kind EdgeKind
	// FromKind and ToKind are record namespaces; FromID and ToID are those
	// namespaces' own durable identifiers - for a session, the durable session
	// key that SessionUID derives, never a selector or a path.
	FromKind string
	FromID   string
	ToKind   string
	ToID     string
}

// Validate refuses at stage time what migrations/0008 would refuse at publish
// time, and one thing more: an endpoint id that is not a well-formed Phase B
// identifier. A staged edge that PostgreSQL will reject is a journal row that
// can never publish, which is worse than a refused write because the refusal is
// visible and the permanently pending row is not.
func (e RecordEdge) Validate() error {
	if !e.Kind.Valid() {
		return fmt.Errorf("edge kind %q is not one the shared catalog carries", string(e.Kind))
	}
	for _, part := range []struct{ what, value string }{
		{"from namespace", e.FromKind},
		{"to namespace", e.ToKind},
	} {
		if !validNamespace.MatchString(part.value) {
			return fmt.Errorf("edge %s %q must match %s", part.what, part.value, validNamespace)
		}
	}
	for _, part := range []struct{ what, value string }{
		{"from id", e.FromID},
		{"to id", e.ToID},
	} {
		if !validRecordID.MatchString(part.value) {
			return fmt.Errorf("edge %s must match %s", part.what, validRecordID)
		}
	}
	if e.FromKind == e.ToKind && e.FromID == e.ToID {
		return errors.New("an edge cannot cite the record it starts from")
	}
	return nil
}

// String renders the edge for a diagnostic, in the same
// "namespace:id" form internal/reference uses.
func (e RecordEdge) String() string {
	return string(e.Kind) + " " + e.FromKind + ":" + e.FromID + " -> " + e.ToKind + ":" + e.ToID
}

// commitEdge writes an edge's plaintext row inside the transaction that is
// inserting the record row it belongs to.
//
// It shares that transaction rather than following it because the two rows are
// one fact. A record row without its edge row would be an edge whose shape no
// reader can see and no retry will supply - SyncRun skips a record the catalog
// already holds, so the missing row would never be reconsidered - and an edge
// row without its record row cannot exist at all, since the foreign key refuses
// it.
func commitEdge(ctx context.Context, tx *sql.Tx, recordID string, edge *RecordEdge) error {
	if edge == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO analysis_edges (record_id, edge_kind, from_kind, from_id, to_kind, to_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (record_id) DO NOTHING`,
		recordID, string(edge.Kind), edge.FromKind, edge.FromID,
		edge.ToKind, edge.ToID); err != nil {
		return fmt.Errorf("record edge for %s: %w", recordID, err)
	}
	return nil
}

// EdgeRef addresses one record in one namespace: the endpoint form a caller
// asks the graph about.
type EdgeRef struct {
	Kind string
	ID   string
}

func (r EdgeRef) String() string { return r.Kind + ":" + r.ID }

func (r EdgeRef) named() bool { return r.Kind != "" && r.ID != "" }

// EdgeFilter bounds one fleet edge read. Every field narrows; none widens.
type EdgeFilter struct {
	// DeploymentID scopes the read to one shared deployment and is required,
	// for the reason RecordFilter's is: inferring it would silently widen a
	// read the caller thought it had scoped.
	DeploymentID string
	// From and To narrow to one endpoint each. Both set means an edge between
	// exactly those two records; neither set means the deployment's whole
	// graph, one page of it.
	From EdgeRef
	To   EdgeRef
	// Kinds narrows to relation kinds.
	Kinds []EdgeKind
	// IncludePending admits edges whose run has not committed, on the same
	// terms as RecordFilter.IncludePending: to SHOW that an edge is staged,
	// never to treat it as committed. Every returned edge carries SyncState.
	IncludePending bool
	Limit          int
	Offset         int
}

// FleetEdge is one edge as any authorized instance sees it: the citation, who
// produced it, and whether it is globally reviewable yet.
//
// The note is absent, and absent by construction rather than by omission here:
// it is in the sealed object the record row names, and reading it needs
// OpenRecord, the object store, and a key.
type FleetEdge struct {
	RecordID string
	Kind     EdgeKind
	From     EdgeRef
	To       EdgeRef
	RunID    string
	// OriginInstanceID is the instance that committed the edge; HostID is the
	// machine it registered as, empty when that instance registered before
	// migrations/0007. Absence is reported rather than guessed.
	OriginInstanceID string
	HostID           string
	SyncState        string
	CreatedAt        time.Time
	CommittedAt      *time.Time
}

// Committed reports whether this edge is globally reviewable.
func (e FleetEdge) Committed() bool { return e.SyncState == SyncCommitted }

// edgeColumns is the projection the fleet edge read shares. Every name is an
// allowlisted plaintext column, and EdgeProjection is what lets the Phase B
// gate check that claim against the query rather than against this comment.
const edgeColumns = `
	e.record_id, e.edge_kind, e.from_kind, e.from_id, e.to_kind, e.to_id, e.created_at,
	c.run_id, r.origin_instance_id, r.sync_state, r.committed_at, i.host_id`

// EdgeProjection reports the plaintext columns the fleet edge read selects, as
// "table.column" pairs, so the Phase B plaintext gate can be pointed at what
// this file actually reads.
func EdgeProjection() map[string][]string {
	return map[string][]string{
		"analysis_edges": {
			"record_id", "edge_kind", "from_kind", "from_id", "to_kind", "to_id", "created_at",
		},
		"analysis_records": {"run_id"},
		"analysis_runs": {
			"origin_instance_id", "sync_state", "committed_at",
		},
	}
}

// RecordEdges lists the deployment's citation graph, newest first.
//
// This is what makes migrations/0008 worth its columns: an instance holding
// only the catalog credential can ask what a record cites and what cites it,
// across every host, without opening a single object. It is the same read the
// CLI and the web surface render outgoing links and backlinks from, and it
// answers with structure only - a reader wanting an edge's note fetches the
// record and decrypts it locally.
//
// Ordering is total and stable: commit time descending with pending runs last,
// then the edge's own creation time, then its record id.
func RecordEdges(ctx context.Context, db *sql.DB, filter EdgeFilter) ([]FleetEdge, error) {
	if filter.DeploymentID == "" {
		return nil, fmt.Errorf("%w: an edge read names one deployment", ErrRecordFilter)
	}
	limit, offset := edgeBounds(filter)

	where := []string{`r.deployment_id = ` + placeholder(1)}
	args := []any{filter.DeploymentID}
	next := 2

	if !filter.IncludePending {
		where = append(where, `r.sync_state = `+placeholder(next))
		args, next = append(args, SyncCommitted), next+1
	}
	for _, side := range []struct {
		ref            EdgeRef
		kindCol, idCol string
	}{
		{filter.From, `e.from_kind`, `e.from_id`},
		{filter.To, `e.to_kind`, `e.to_id`},
	} {
		if !side.ref.named() {
			continue
		}
		where = append(where, side.kindCol+` = `+placeholder(next))
		args, next = append(args, side.ref.Kind), next+1
		where = append(where, side.idCol+` = `+placeholder(next))
		args, next = append(args, side.ref.ID), next+1
	}
	kinds := make([]string, len(filter.Kinds))
	for i, kind := range filter.Kinds {
		if !kind.Valid() {
			return nil, fmt.Errorf("%w: unknown edge kind %q", ErrRecordFilter, kind)
		}
		kinds[i] = string(kind)
	}
	where, args, next = appendAnyOf(where, args, next, `e.edge_kind`, kinds)

	query := `SELECT` + edgeColumns + `
		  FROM analysis_edges e
		  JOIN analysis_records c ON c.record_id = e.record_id
		  JOIN analysis_runs r ON r.run_id = c.run_id
		  LEFT JOIN instances i ON i.instance_id = r.origin_instance_id
		 WHERE ` + strings.Join(where, " AND ") + `
		 ORDER BY r.committed_at DESC NULLS LAST, e.created_at DESC, e.record_id
		 LIMIT ` + placeholder(next) + ` OFFSET ` + placeholder(next+1)
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list fleet edges: %w", err)
	}
	defer rows.Close()

	var out []FleetEdge
	for rows.Next() {
		var (
			edge      FleetEdge
			kind      string
			host      sql.NullString
			committed sql.NullTime
		)
		if err := rows.Scan(&edge.RecordID, &kind, &edge.From.Kind, &edge.From.ID,
			&edge.To.Kind, &edge.To.ID, &edge.CreatedAt, &edge.RunID,
			&edge.OriginInstanceID, &edge.SyncState, &committed, &host); err != nil {
			return nil, fmt.Errorf("scan fleet edge: %w", err)
		}
		edge.Kind, edge.HostID = EdgeKind(kind), host.String
		if committed.Valid {
			at := committed.Time
			edge.CommittedAt = &at
		}
		out = append(out, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fleet edges: %w", err)
	}
	return out, nil
}

// edgeBounds applies the same page bounds a fleet record read uses. A zero
// limit is one page rather than the deployment's whole graph: a corpus that
// cites itself thoroughly has more edges than records.
func edgeBounds(filter EdgeFilter) (limit, offset int) {
	limit = filter.Limit
	if limit <= 0 {
		limit = DefaultRecordLimit
	}
	if limit > MaxRecordLimit {
		limit = MaxRecordLimit
	}
	if offset = filter.Offset; offset < 0 {
		offset = 0
	}
	return limit, offset
}
