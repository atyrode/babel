package sharedcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Class names one of the data classes Babel's PostgreSQL catalog admits. Every
// column in the shared schema must map to exactly one of them, and nothing
// outside this set may be stored.
//
// Six of the classes are SPEC.md 9's original Phase A vocabulary. Four more
// exist because the operator widened the boundary on 2026-08-30
// (migrations/0004): a session's title, its workspace path, its continuation
// grade, and a host's display name and machine facts are now plaintext here, so
// that an instance holding only the DSN can browse the fleet and read something
// rather than a list of digests. Transcripts stay encrypted in restic.
//
// What remains outside every class is the part that decides whether this list
// is still a boundary: transcript bodies, plaintext full-text indexes,
// deterministic ciphertext, and anything else that answers "does this session
// contain X" without a key; claims, operator context, findings, review notes
// and other Phase B payloads, which are sealed into objects; session selectors
// and adapter source ids; and infrastructure identity such as a machine's
// system hostname.
//
// Phase B needed no new class. SPEC.md 9's Phase B vocabulary - structured
// identifiers, entity kind and schema version, encrypted-object references,
// key ID, ciphertext size, commit/sync state, relationship IDs - lands on the
// classes Phase A already defined: an object key, a key ID, and a relationship
// ID are all opaque IDs or locators, and a sync state is a commit state.
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
	// ClassSessionLabel covers a session's model-written title: a short
	// human-readable summary, admitted by explicit operator decision
	// (2026-08-30, migrations/0004). It is not a search oracle over transcript
	// content, and SPEC.md 9 still forbids one - a keyword set, a plaintext
	// full-text index, or a digest over transcript bytes is not this class.
	ClassSessionLabel Class = "session label"
	// ClassWorkspacePath covers a session's workspace path, admitted by the
	// same decision. It names a directory on the publishing machine, never the
	// session's selector or its adapter source id.
	ClassWorkspacePath Class = "workspace path"
	// ClassSessionGrade covers a session's continuation grade: a verdict the
	// publishing host resolves from its own local files. It is stored because
	// no other instance can recompute it, and it is nullable because absence
	// must not read as a negative verdict.
	ClassSessionGrade Class = "session grade"
	// ClassHostIdentity covers a host's operator-assigned display name and the
	// machine facts it reports about itself - operating system and
	// architecture. It does not cover a system hostname or any other
	// infrastructure identity.
	ClassHostIdentity Class = "host identity or machine fact"
	// ClassSpendMeasure covers what a session cost in money: the priced
	// total a harness recorded for its own turns, summed by Babel and
	// republished unchanged (migrations/0006). It is deliberately not
	// ClassMeasure. Sizes and counts describe how big a session is; a
	// dollar figure is a different kind of fact about the operator's
	// account, and folding it into an existing class would have widened
	// the boundary without the listing showing that anything new arrived.
	ClassSpendMeasure Class = "spend measure"
	// ClassRunState covers a run's own lifecycle state: whether a conductor
	// cycle or an explore run is still working, and how it ended
	// (migrations/0009). It is deliberately not ClassCommitState. A commit
	// state says how far a record got through the object-first protocol; this
	// says whether a process is alive, which is a claim about a machine rather
	// than about a record, and it is the only class in this list whose value
	// is expected to be wrong within minutes of being written.
	ClassRunState Class = "run lifecycle state"
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
	// host_id says which host this instance publishes as (migrations/0007).
	// It is the same opaque, operator-assigned value `snapshots.host_id` and
	// `sessions.host_id` already carry, and it is what lets a Phase B fleet
	// read attribute a committed record to the machine that produced it
	// instead of inferring one.
	"instances": {
		"instance_id":   ClassOpaqueID,
		"deployment_id": ClassOpaqueID,
		"host_id":       ClassOpaqueID,
		"created_at":    ClassTimestamp,
		"last_seen_at":  ClassTimestamp,
	},
	// created_at is also this host's first-seen time: it is set when the row is
	// first inserted and never rewritten (migrations/0004).
	"hosts": {
		"host_id":             ClassOpaqueID,
		"deployment_id":       ClassOpaqueID,
		"created_at":          ClassTimestamp,
		"display_name":        ClassHostIdentity,
		"os":                  ClassHostIdentity,
		"arch":                ClassHostIdentity,
		"identity_updated_at": ClassTimestamp,
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
		"title":                 ClassSessionLabel,
		// title_provenance is an identifier, not a label: it names which
		// derivation the title followed and ranges over three compile-time
		// constants, exactly the justification 0001_init gives for admitting
		// `harness`. It carries no session content (migrations/0005).
		"title_provenance":   ClassIdentifier,
		"workspace":          ClassWorkspacePath,
		"continuation_grade": ClassSessionGrade,
		"created_at":         ClassTimestamp,
		"updated_at":         ClassTimestamp,
		// The usage summary (migrations/0006). Three of the four are plain
		// counts of a session's own records; cost_usd is money and carries
		// its own class so this listing shows it as its own kind of fact.
		// None of them can be inverted into transcript content: they say a
		// session was long or expensive, never what it said.
		"cost_usd":     ClassSpendMeasure,
		"total_tokens": ClassMeasure,
		"turns":        ClassMeasure,
		"tool_errors":  ClassMeasure,
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
	// The typed reference graph's plaintext shape (migrations/0008). It is the
	// one Phase B table whose columns say something structural about a record
	// rather than only identifying it, and the widening is exactly as narrow as
	// SPEC.md 763 admits: a relation kind, two record namespaces, two durable
	// identifiers. The edge's note is content and is in the sealed object with
	// every other Phase B payload; there is no column for it here, and a
	// future one would fail the Phase B class gate below.
	//
	// A namespace is classed as an identifier on 0001_init's own terms for
	// `harness`: it ranges over the compile-time record kinds
	// internal/reference's resolver registry is keyed by, and names which
	// store an id belongs to rather than anything about the record. An
	// endpoint id is an opaque, client-generated identifier - for a session,
	// the same `session_uid` digest `sessions` is keyed by.
	"analysis_edges": {
		"record_id":  ClassOpaqueID,
		"edge_kind":  ClassIdentifier,
		"from_kind":  ClassIdentifier,
		"from_id":    ClassOpaqueID,
		"to_kind":    ClassIdentifier,
		"to_id":      ClassOpaqueID,
		"created_at": ClassTimestamp,
	},
	// What a proposal rests on (migrations/0010, issue #114). A proposal has
	// two lawful forms - consolidated from findings, or a candidate remedy
	// addressing hypotheses directly - and which one it is decides how much
	// authority a reader may lend it. That is relationship shape rather than
	// content, which SPEC.md 9's Phase B allowlist admits, so it travels in
	// columns while the proposal's own words stay in the sealed object.
	//
	// subject_kind is a closed two-value vocabulary naming which frontier
	// store an id belongs to, classed as an identifier on exactly the terms
	// analysis_edges.from_kind is. position is closure-style ordering,
	// preserving the order the producer asserted. There is no note, no
	// rationale and no score column, and a future one would have no class to
	// be listed under.
	"analysis_proposal_subjects": {
		"record_id":    ClassOpaqueID,
		"position":     ClassOrdering,
		"subject_kind": ClassIdentifier,
		"subject_id":   ClassOpaqueID,
		"created_at":   ClassTimestamp,
	},
	// Fleet presence (migrations/0009): what is running where, so a run is
	// visible off-host before its receipt commits. It is the one table here
	// that is neither archive metadata nor analysis output but ephemeral
	// status, and every column is an identifier, a closed vocabulary or a
	// timestamp - the same boundary, applied to a shorter-lived fact.
	//
	// recipe is classed as an identifier on 0001_init's own terms for
	// `harness`: it names which cookbook guidance a run applied and says
	// nothing about what the run read or concluded. The authority pair is
	// #96's, which internal/run already keeps to a kind and an identifier
	// reference and which the receipt header carries in the clear for the
	// same reason. There is no note, reason, or failure-text column, and a
	// future one would have no class to be listed under.
	"presence": {
		"presence_id":       ClassOpaqueID,
		"deployment_id":     ClassOpaqueID,
		"host_id":           ClassOpaqueID,
		"kind":              ClassIdentifier,
		"run_id":            ClassOpaqueID,
		"recipe":            ClassIdentifier,
		"preparation_id":    ClassOpaqueID,
		"authority_kind":    ClassIdentifier,
		"authority_ref":     ClassOpaqueID,
		"state":             ClassRunState,
		"started_at":        ClassTimestamp,
		"heartbeat_at":      ClassTimestamp,
		"finished_at":       ClassTimestamp,
		"receipt_record_id": ClassOpaqueID,
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

// phaseBTables names the tables that hold Phase B analysis output
// (migrations/0003, migrations/0008, migrations/0010). They are singled out
// because they carry a different weight from every other table here: a
// snapshot row is rebuildable from the repository, while a hypothesis,
// finding, receipt, or citation exists nowhere else, so the question of what
// may sit beside it in the clear is a narrower question than the one the
// general allowlist answers.
var phaseBTables = map[string]bool{
	"analysis_runs":              true,
	"analysis_records":           true,
	"analysis_edges":             true,
	"analysis_proposal_subjects": true,
}

// phaseBPlaintextClasses is SPEC.md 14's open item closed: which fields of a
// Phase B record are payload rather than allowlisted plaintext.
//
// The answer is a class restriction rather than a column list, because a
// column list would have to be edited by the same migration that adds the
// column it was supposed to catch. Six of the ten classes are admitted, and
// they are exactly the vocabulary SPEC.md 9's Phase B allowlist names:
// structured identifiers and entity kinds (ClassIdentifier), opaque
// identifiers including host and actor attribution and encrypted-object
// references (ClassOpaqueID), closure ordering (ClassOrdering), ciphertext
// sizes and record counts (ClassMeasure), sync state (ClassCommitState), and
// timestamps (ClassTimestamp).
//
// The four that are refused are the point. The operator widened the Phase A
// boundary on 2026-08-30 to admit a session's title, its workspace path, its
// continuation grade, and a host's machine facts (migrations/0004,
// migrations/0006), and that permission was granted for archive metadata about
// sessions a harness had already written. It does not carry over to analysis
// output: a Phase B record's content is a claim Babel produced about the
// corpus, and a column of it in the clear would be readable by the managed
// provider and by anyone holding the catalog credential, which is precisely
// what sealing the payload into an object prevents. A future migration that
// put a title-shaped, path-shaped, grade-shaped, or money-shaped column on a
// Phase B table would pass the general allowlist and fails here.
var phaseBPlaintextClasses = map[Class]bool{
	ClassIdentifier:  true,
	ClassOpaqueID:    true,
	ClassOrdering:    true,
	ClassMeasure:     true,
	ClassCommitState: true,
	ClassTimestamp:   true,
}

// PhaseBPlaintextClasses reports the classes a Phase B plaintext column may
// belong to, sorted, for diagnostics and for a writer that wants to state the
// contract it is holding itself to.
func PhaseBPlaintextClasses() []Class {
	out := make([]Class, 0, len(phaseBPlaintextClasses))
	for class := range phaseBPlaintextClasses {
		out = append(out, class)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AssertPhaseBPlaintext is the single gate every Phase B plaintext column set
// passes through: the writers in this package, the publisher that drives them,
// and Verify itself.
//
// It answers one question - may these columns of this table hold their values
// in the clear - and it answers it from the allowlist rather than from a second
// list, so there is exactly one place a Phase B column's eligibility is
// decided. A caller offering a table that holds no Phase B output is a caller
// bug and is refused rather than passed: the gate that silently approves
// whatever it does not recognize is not a gate.
//
// Every offending column is named at once, because a writer or a migration
// review wants the whole picture rather than the first failure.
func AssertPhaseBPlaintext(table string, columns ...string) error {
	if !phaseBTables[table] {
		return fmt.Errorf(
			"table %q holds no Phase B analysis output; the Phase B plaintext gate is for %s",
			table, strings.Join(sortedPhaseBTables(), " and "))
	}
	problems := phaseBProblems(table, columns)
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("Phase B plaintext eligibility (SPEC.md 9, 14):\n  %s",
		strings.Join(problems, "\n  "))
}

// phaseBProblems reports what is wrong with one table's offered columns. It is
// separate from AssertPhaseBPlaintext so Verify can accumulate findings across
// tables into the one error it already builds.
func phaseBProblems(table string, columns []string) []string {
	known := allowlist[table]
	var problems []string
	for _, column := range columns {
		class, listed := known[column]
		if !listed {
			problems = append(problems, fmt.Sprintf(
				"column %s.%s is not in the allowlist at all", table, column))
			continue
		}
		if !phaseBPlaintextClasses[class] {
			problems = append(problems, fmt.Sprintf(
				"column %s.%s is classed %q, which Phase B does not admit in plaintext: a record's content is sealed into an object and only %s may travel in the clear beside the reference to it",
				table, column, class, describeClasses(PhaseBPlaintextClasses())))
		}
	}
	return problems
}

func sortedPhaseBTables() []string {
	out := make([]string, 0, len(phaseBTables))
	for table := range phaseBTables {
		out = append(out, table)
	}
	sort.Strings(out)
	return out
}

func describeClasses(classes []Class) string {
	names := make([]string, len(classes))
	for i, class := range classes {
		names[i] = string(class)
	}
	return strings.Join(names, ", ")
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
//
// The Phase B tables are checked twice, against two different questions. The
// loop below asks whether a column is in the allowlist at all;
// phaseBProblems then asks the narrower question SPEC.md 14 left open -
// whether the class it is listed under is one Phase B admits in plaintext -
// so a column that a Phase A widening made acceptable cannot arrive on an
// analysis table without a second, explicit decision.
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
	for table := range phaseBTables {
		present, found := live[table]
		if !found {
			// A missing Phase B table is already reported as missing
			// columns above; saying it twice would only pad the list.
			continue
		}
		columns := make([]string, 0, len(present))
		for column := range present {
			columns = append(columns, column)
		}
		disallowed = append(disallowed, phaseBProblems(table, columns)...)
	}

	problems := append(disallowed, missing...)
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("shared catalog schema does not match the plaintext allowlist:\n  %s",
		strings.Join(problems, "\n  "))
}
