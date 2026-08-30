package sharedcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Class names one of the data classes SPEC.md 9 permits in Babel's PostgreSQL
// catalog. Every column in the shared schema must map to exactly one of them,
// and nothing outside this set may be stored: titles, filesystem paths,
// workspace names, transcript metadata, claims, operator context, and derived
// judgements about session content stay in the encrypted repository, the
// encrypted object store, and local indexes.
//
// Phase B needed no new class. SPEC.md 9's Phase B vocabulary - structured
// identifiers, entity kind and schema version, encrypted-object references,
// key ID, ciphertext size, commit/sync state, relationship IDs - lands on the
// classes Phase A already defined: an object key, a key ID, and a relationship
// ID are all opaque IDs or locators, and a sync state is a commit state. That
// the vocabulary did not have to widen is the point.
type Class string

const (
	// ClassIdentifier covers schema and version identifiers - including the
	// adapter enum, which names which schema a row follows.
	ClassIdentifier Class = "schema/version identifier"
	// ClassOpaqueID covers opaque IDs and locators: digests, restic snapshot
	// IDs, and operator-assigned deployment/instance/host IDs.
	ClassOpaqueID Class = "opaque ID or locator"
	// ClassOrdering covers ordering and fencing data.
	ClassOrdering Class = "ordering or fencing data"
	// ClassMeasure covers sizes and counts.
	ClassMeasure Class = "size or count"
	// ClassCommitState covers commit and reconciliation state.
	ClassCommitState Class = "commit state"
	// ClassTimestamp covers timestamps.
	ClassTimestamp Class = "timestamp"
)

// allowlist enumerates every column the shared schema may contain, keyed by
// table then column. It is the machine-checkable form of the contract: a column
// that reaches the database without an entry here fails Verify, so widening the
// shared catalog cannot happen by accident during a migration.
var allowlist = map[string]map[string]Class{
	"schema_migrations": {
		"version":    ClassIdentifier,
		"applied_at": ClassTimestamp,
	},
	"deployments": {
		"deployment_id":  ClassOpaqueID,
		"schema_version": ClassIdentifier,
		"created_at":     ClassTimestamp,
	},
	"instances": {
		"instance_id":   ClassOpaqueID,
		"deployment_id": ClassOpaqueID,
		"created_at":    ClassTimestamp,
		"last_seen_at":  ClassTimestamp,
	},
	"hosts": {
		"host_id":       ClassOpaqueID,
		"deployment_id": ClassOpaqueID,
		"created_at":    ClassTimestamp,
	},
	"snapshots": {
		"snapshot_id":       ClassOpaqueID,
		"host_id":           ClassOpaqueID,
		"publication_order": ClassOrdering,
		"snapshot_time":     ClassTimestamp,
		"commit_state":      ClassCommitState,
		"files_new":         ClassMeasure,
		"files_changed":     ClassMeasure,
		"files_unmodified":  ClassMeasure,
		"bytes_added":       ClassMeasure,
		"session_count":     ClassMeasure,
		"published_by":      ClassOpaqueID,
		"reconciled_at":     ClassTimestamp,
		"created_at":        ClassTimestamp,
		"updated_at":        ClassTimestamp,
	},
	"sessions": {
		"session_uid":           ClassOpaqueID,
		"host_id":               ClassOpaqueID,
		"harness":               ClassIdentifier,
		"first_snapshot_id":     ClassOpaqueID,
		"latest_snapshot_id":    ClassOpaqueID,
		"primary_size":          ClassMeasure,
		"artifact_count":        ClassMeasure,
		"blob_count":            ClassMeasure,
		"unresolved_blob_count": ClassMeasure,
		"source_modified_at":    ClassTimestamp,
		"created_at":            ClassTimestamp,
		"updated_at":            ClassTimestamp,
	},
	"host_leases": {
		"host_id":     ClassOpaqueID,
		"holder_id":   ClassOpaqueID,
		"fence":       ClassOrdering,
		"acquired_at": ClassTimestamp,
		"expires_at":  ClassTimestamp,
	},
	"idempotency_keys": {
		"idempotency_key": ClassOpaqueID,
		"instance_id":     ClassOpaqueID,
		"snapshot_id":     ClassOpaqueID,
		"created_at":      ClassTimestamp,
	},
	// Phase B analysis output (migrations/0003). The payload is absent by
	// design: a record's content is sealed into an object and PostgreSQL holds
	// only the reference, the key ID, and the size.
	"analysis_runs": {
		"run_id":             ClassOpaqueID,
		"deployment_id":      ClassOpaqueID,
		"origin_instance_id": ClassOpaqueID,
		"execution_host_id":  ClassOpaqueID,
		"continues_run_id":   ClassOpaqueID,
		"sync_state":         ClassCommitState,
		"record_count":       ClassMeasure,
		"created_at":         ClassTimestamp,
		"updated_at":         ClassTimestamp,
		"committed_at":       ClassTimestamp,
	},
	"analysis_records": {
		"record_id":       ClassOpaqueID,
		"run_id":          ClassOpaqueID,
		"kind":            ClassIdentifier,
		"record_schema":   ClassIdentifier,
		"ordinal":         ClassOrdering,
		"object_key":      ClassOpaqueID,
		"key_id":          ClassOpaqueID,
		"ciphertext_size": ClassMeasure,
		"object_digest":   ClassOpaqueID,
		"created_at":      ClassTimestamp,
	},
}

// Allowlist returns the contract as a sorted, flattened listing of
// "table.column: class" lines, for documentation and diagnostics.
func Allowlist() []string {
	var lines []string
	for table, columns := range allowlist {
		for column, class := range columns {
			lines = append(lines, fmt.Sprintf("%s.%s: %s", table, column, class))
		}
	}
	sort.Strings(lines)
	return lines
}

// Verify reads the live schema and reports every column that is not in the
// allowlist, and every allowlisted column that is missing. It is the gate that
// makes SPEC.md 9 enforceable rather than aspirational: an instance can run it
// against a real deployment, and the test suite runs it after migrating.
//
// It reads Babel's own schema (see Schema), not whatever schema the connection
// would otherwise default to. That is what lets an unknown table be a hard
// failure: a managed provider may pre-install extensions that put their own
// relations in `public`, and rejecting those would be wrong while ignoring them
// would blind the gate to a Babel migration adding an unlisted table.
//
// Errors name every discrepancy at once rather than the first, because a
// migration review wants the whole picture. An unknown table is named once
// rather than once per column it happens to have.
func Verify(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		ORDER BY table_name, column_name`)
	if err != nil {
		return fmt.Errorf("read live schema: %w", err)
	}
	defer rows.Close()

	live := make(map[string]map[string]bool)
	unknownTables := make(map[string]bool)
	var disallowed []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return fmt.Errorf("scan live schema: %w", err)
		}
		if live[table] == nil {
			live[table] = make(map[string]bool)
		}
		live[table][column] = true

		columns, known := allowlist[table]
		if !known {
			if !unknownTables[table] {
				unknownTables[table] = true
				disallowed = append(disallowed,
					fmt.Sprintf("table %q is not in the allowlist", table))
			}
			continue
		}
		if _, ok := columns[column]; !ok {
			disallowed = append(disallowed, fmt.Sprintf(
				"column %s.%s is not in the allowlist: every shared-catalog column must be a schema identifier, opaque ID, ordering/fencing value, size/count, commit state, or timestamp (SPEC.md 9)",
				table, column))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read live schema: %w", err)
	}

	var missing []string
	for table, columns := range allowlist {
		for column := range columns {
			if !live[table][column] {
				missing = append(missing, fmt.Sprintf("column %s.%s is allowlisted but absent", table, column))
			}
		}
	}

	problems := append(disallowed, missing...)
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("shared catalog schema does not match the plaintext allowlist:\n  %s",
		strings.Join(problems, "\n  "))
}
