package review

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// maxLineageDepth bounds a lineage walk. The edges are append-only and every
// one points from a newer record to an older one, so a cycle would mean a
// corrupt database rather than a legitimate shape; the bound turns that into a
// reported error instead of a query that never returns.
const maxLineageDepth = 256

// lineageEdge is one edge a write is about to record. It is a named type so
// the set of edges a refinement produces reads as a list rather than as a
// repeated anonymous struct literal.
type lineageEdge struct {
	relation Relation
	from, to Node
}

// Edge is one typed lineage relationship between two records.
type Edge struct {
	ID        string
	Relation  Relation
	From      Node
	To        Node
	CreatedAt time.Time
	// Generation is how many edges away from the queried record this one
	// was found: 1 for a direct parent or child, 2 for its parent or child
	// in turn. It is what makes "two refinement generations" a value a
	// caller can read rather than a shape it has to reconstruct.
	Generation int
}

// Lineage is one record's place in the refinement history, walked in both
// directions.
//
// Both directions are populated because §4.7's lineage is asked from either
// end. A reviewer looking at the newest revision needs to know what it
// corrects and why; a reviewer looking at a record they rejected months ago
// needs to know what became of it. An edge set that could only be walked one
// way would answer one of those and silently lose the other.
type Lineage struct {
	Node Node
	// Ancestors are the edges reached by following relations toward older
	// records, nearest generation first.
	Ancestors []Edge
	// Descendants are the edges reached by following them toward newer
	// records, nearest generation first.
	Descendants []Edge
}

// link records one lineage edge. Recording the same edge twice is not an
// error: the relationship is a fact about two records, and asserting it again
// asserts the same fact.
func (s *Service) link(ctx context.Context, tx *sql.Tx, relation Relation, from, to Node) error {
	if !relation.valid() {
		return fmt.Errorf("%w: lineage relation %q", ErrInvalidValue, relation)
	}
	if !from.Kind.valid() || from.ID == "" {
		return fmt.Errorf("%w: lineage edge source %q %q", ErrInvalidValue, from.Kind, from.ID)
	}
	if !to.Kind.valid() || to.ID == "" {
		return fmt.Errorf("%w: lineage edge target %q %q", ErrInvalidValue, to.Kind, to.ID)
	}
	id, err := newID("lin")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO review_lineage(
		id, relation, from_kind, from_id, to_kind, to_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(from_kind, from_id, to_kind, to_id, relation) DO NOTHING`,
		id, string(relation), string(from.Kind), from.ID, string(to.Kind), to.ID,
		formatTime(s.now())); err != nil {
		return fmt.Errorf("review: record lineage edge: %w", err)
	}
	return nil
}

// Lineage walks a record's refinement lineage in both directions.
//
// The walk is transitive, so two refinement generations — a proposal, the
// revision that refined it, and the revision that refined that — are one
// query from any of the three, and the Generation field says how far each edge
// was found.
func (s *Service) Lineage(ctx context.Context, of Node) (Lineage, error) {
	if !of.Kind.valid() || of.ID == "" {
		return Lineage{}, fmt.Errorf("%w: lineage node %q %q", ErrInvalidValue, of.Kind, of.ID)
	}
	ancestors, err := s.walk(ctx, of, true)
	if err != nil {
		return Lineage{}, err
	}
	descendants, err := s.walk(ctx, of, false)
	if err != nil {
		return Lineage{}, err
	}
	return Lineage{Node: of, Ancestors: ancestors, Descendants: descendants}, nil
}

// walk performs a breadth-first traversal in one direction, so edges come back
// in generation order and each record is visited once.
func (s *Service) walk(ctx context.Context, from Node, up bool) ([]Edge, error) {
	seen := map[Node]bool{from: true}
	frontierNodes := []Node{from}
	var found []Edge
	for generation := 1; generation <= maxLineageDepth && len(frontierNodes) > 0; generation++ {
		var next []Node
		for _, current := range frontierNodes {
			edges, err := s.edgesAt(ctx, current, up)
			if err != nil {
				return nil, err
			}
			for _, edge := range edges {
				other := edge.To
				if !up {
					other = edge.From
				}
				if seen[other] {
					continue
				}
				seen[other] = true
				edge.Generation = generation
				found = append(found, edge)
				next = append(next, other)
			}
		}
		frontierNodes = next
		if generation == maxLineageDepth && len(next) > 0 {
			return nil, fmt.Errorf("review: lineage walk from %s %q exceeded %d generations",
				from.Kind, from.ID, maxLineageDepth)
		}
	}
	return found, nil
}

// edgesAt reads the edges leaving a record (up) or arriving at it (down), in
// insertion order so a lineage listing is stable across reads.
func (s *Service) edgesAt(ctx context.Context, at Node, up bool) ([]Edge, error) {
	query := `SELECT id, relation, from_kind, from_id, to_kind, to_id, created_at
		FROM review_lineage WHERE to_kind = ? AND to_id = ? ORDER BY rowid`
	if up {
		query = `SELECT id, relation, from_kind, from_id, to_kind, to_id, created_at
			FROM review_lineage WHERE from_kind = ? AND from_id = ? ORDER BY rowid`
	}
	rows, err := s.db.QueryContext(ctx, query, string(at.Kind), at.ID)
	if err != nil {
		return nil, fmt.Errorf("review: read lineage: %w", err)
	}
	defer rows.Close()
	var edges []Edge
	for rows.Next() {
		var (
			edge     Edge
			relation string
			fromKind string
			toKind   string
			created  string
		)
		if err := rows.Scan(&edge.ID, &relation, &fromKind, &edge.From.ID,
			&toKind, &edge.To.ID, &created); err != nil {
			return nil, fmt.Errorf("review: read lineage: %w", err)
		}
		edge.Relation = Relation(relation)
		edge.From.Kind = Kind(fromKind)
		edge.To.Kind = Kind(toKind)
		if edge.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}
