package sync

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
)

var sqlIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Missing lists locally durable identities not yet tracked by the publication
// journal. Owners supply their fixed table and identity column, then reconstruct
// publication records themselves. Rows are closed before returning so recovery
// can start a transaction on the same single-connection database.
func Missing(ctx context.Context, db *sql.DB, table, idColumn string) ([]string, error) {
	if !sqlIdentifier.MatchString(table) || !sqlIdentifier.MatchString(idColumn) {
		return nil, fmt.Errorf("sync: invalid recovery table or column")
	}
	rows, err := db.QueryContext(ctx, `SELECT source.`+idColumn+` FROM `+table+` source WHERE NOT EXISTS (SELECT 1 FROM sync_record journal WHERE journal.record_id = source.`+idColumn+`) ORDER BY source.`+idColumn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
