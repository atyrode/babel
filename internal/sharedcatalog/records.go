package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/envelope"
)

// This file is the fleet read path for Phase B analysis output (SPEC.md 4.7,
// 6.5, 9; issue #109 item 3).
//
// SyncRun is how a record leaves one machine. Records is how every other
// machine sees it, and the asymmetry between the two is the point: writing is
// host-pinned - a run belongs to the instance that generated its id and no
// other instance writes it - while reading is deliberately unpinned, because a
// hypothesis that only its own workstation can review is not globally durable
// in any sense an operator cares about. The pin on a run says which host can
// rerun repository-dependent work; migrations/0003 states in as many words that
// it never restricts who may read the result.
//
// What crosses the wire in the clear is only what SPEC.md 9's Phase B allowlist
// admits, which AssertPhaseBPlaintext enforces on the schema itself: the
// record's identity, its kind and schema, its place in a run's closure, the
// reference to the sealed object, and the run's attribution and sync state.
// Content is not here and cannot be: there is no payload column. A caller that
// wants to read a record calls OpenRecord with the row, which fetches the
// sealed object, verifies its digest, and decrypts it locally. Decryption is
// the caller's, on the caller's machine, with the caller's keyring - that is
// what "decrypted client-side" means, and it is why this package hands back a
// row and an object reference rather than bytes.

// DefaultRecordLimit and MaxRecordLimit bound a fleet read.
//
// A zero limit means DefaultRecordLimit rather than everything, for the reason
// internal/frontier's ListFilter gives: a deployment's whole analysis output
// must not arrive because a caller forgot a bound. The ceiling is higher than
// the local listings' because this is the surface a host filter narrows, and an
// operator asking for one host's committed records over a week should not have
// to page through it in twenty-record slices.
const (
	DefaultRecordLimit = 100
	MaxRecordLimit     = 1000
)

// ErrRecordFilter reports a fleet read whose filter cannot be answered.
var ErrRecordFilter = errors.New("invalid record filter")

// RecordFilter bounds one fleet read.
//
// Every field narrows; none widens. An empty filter beyond DeploymentID is the
// fleet's committed output, newest commit first, one page of it.
type RecordFilter struct {
	// DeploymentID scopes the read to one shared deployment and is required.
	// It is not defaulted from the catalog: a deployment id is what makes two
	// unrelated fleets sharing one database stay unrelated, and inferring it
	// would silently widen a read the caller thought it had scoped.
	DeploymentID string

	// Hosts narrows to the machines that produced the records, resolved
	// through the origin instance's registered host (migrations/0007). It is
	// an any-of filter, and it is what the CLI's --host flag and the web host
	// chip pass.
	//
	// A host that has registered no instance matches nothing, which is an
	// answer rather than an error: it says that machine has committed no
	// analysis, and refusing the query would make an empty fleet look broken.
	Hosts []string
	// Instances narrows to the origin instances themselves. A host may have
	// published as more than one instance over its life, so the two filters
	// answer different questions and neither derives the other.
	Instances []string
	// Kinds narrows to record types.
	Kinds []RecordKind
	// RunIDs narrows to specific runs.
	RunIDs []string
	// RecordIDs narrows to specific records, which is how a caller holding
	// local record ids asks the authoritative store which of them have
	// committed remotely. Paired with IncludePending it is the honest answer
	// to "is my local record durable off this machine yet".
	RecordIDs []string

	// Since and Until bound the run's commit time, not the record's creation
	// time. Commit time is when the record became globally visible, which is
	// the question a fleet reader is asking; a record created before an outage
	// and committed after it belongs to the window it became readable in.
	// A pending run has no commit time and is therefore excluded by either
	// bound even when IncludePending is set - which is correct, because "what
	// committed in this window" and "what is still staged" are two questions.
	Since time.Time
	Until time.Time

	// IncludePending admits records whose run has not committed.
	//
	// It is off by default because SPEC.md 9 states pending-sync output is not
	// globally reviewable, and a browse surface that quietly mixed staged
	// output into committed output would make that guarantee unobservable. A
	// caller turns it on in order to SHOW that a record is pending - the
	// visible outage staging SPEC.md 6.5 requires - and never in order to
	// treat it as committed. Every returned row carries SyncState, so the
	// distinction survives into the rendering.
	IncludePending bool

	Limit  int
	Offset int
}

// FleetRecord is one Phase B record as any authorized instance sees it: the
// plaintext row, the attribution that says whose analysis it is, and the sync
// state that says whether it is globally reviewable yet.
//
// Content is absent by construction. Record.ObjectKey and Record.ObjectDigest
// name and pin the sealed object; OpenRecord is what turns them into bytes,
// locally, with a key this struct does not carry.
type FleetRecord struct {
	Record AnalysisRecordRow

	// HostID is the machine that produced the record, resolved from the
	// origin instance's registration (migrations/0007). It is empty when that
	// instance registered before the column existed, and a renderer must show
	// the absence rather than substitute a host: attributing one machine's
	// analysis to another is the failure this field exists to prevent.
	HostID string
	// HostDisplayName is the operator-assigned name for HostID, empty when
	// the host has asserted none (migrations/0004). It is a label for reading,
	// never an identity to match on.
	HostDisplayName string
	// OriginInstanceID is the actor: the instance that generated the run's id
	// and committed its records. It is always present.
	OriginInstanceID string
	// ExecutionHostID is the host repository-dependent work is pinned to,
	// empty when the run is unpinned. It is deliberately separate from HostID:
	// a pin constrains where the work can be rerun, and conflating it with
	// authorship is exactly what migrations/0007 exists to stop.
	ExecutionHostID string
	// ContinuesRunID links a second instance's follow-on work to the
	// committed run it continues.
	ContinuesRunID string

	// SyncState is the run's state: SyncCommitted, or SyncPending when the
	// filter admitted staged output.
	SyncState string
	// CommittedAt is when the record became globally reviewable, nil while
	// its run is pending.
	CommittedAt *time.Time
}

// Committed reports whether this record is globally reviewable. It is a method
// rather than a comparison at every call site because a renderer asking the
// question in nine places must not be the place the vocabulary drifts.
func (r FleetRecord) Committed() bool { return r.SyncState == SyncCommitted }

// recordColumns is the projection every fleet read shares. Each name is an
// allowlisted plaintext column; AssertPhaseBPlaintext is what keeps that true
// as the schema moves, and TestFleetReadProjectionIsPlaintextEligible is what
// keeps this list honest about what it selects.
const recordColumns = `
	c.record_id, c.run_id, c.kind, c.record_schema, c.ordinal,
	c.object_key, c.object_digest, c.key_id, c.ciphertext_size, c.created_at,
	r.origin_instance_id, r.execution_host_id, r.continues_run_id,
	r.sync_state, r.committed_at,
	i.host_id, h.display_name`

// RecordProjection reports the plaintext columns a fleet read selects, as
// "table.column" pairs.
//
// It exists so the Phase B plaintext gate can be pointed at what this code
// actually reads rather than at what a comment claims it reads. A projection
// that grew a content column would pass every query test and fail this one.
func RecordProjection() map[string][]string {
	return map[string][]string{
		"analysis_records": {
			"record_id", "run_id", "kind", "record_schema", "ordinal",
			"object_key", "object_digest", "key_id", "ciphertext_size", "created_at",
		},
		"analysis_runs": {
			"origin_instance_id", "execution_host_id", "continues_run_id",
			"sync_state", "committed_at",
		},
	}
}

// Records lists Phase B records from every host in a deployment, newest commit
// first (SPEC.md 4.7, 9; issue #109 item 3).
//
// The joins are what make the read fleet-wide rather than merely global. A
// record names its run; the run names its origin instance; the instance names
// the host it registered as; the host may carry a display name. All four are
// plaintext by the allowlist, so an instance holding only the catalog
// credential can render "host workstation-linux, instance workstation,
// committed at 14:02" without a decryption key - and still cannot read a word
// of what the record says.
//
// The join to instances and hosts is LEFT on purpose. analysis_runs references
// instances, so the instance row exists; its host_id does not, for any instance
// that registered before migrations/0007, and a host row may exist with no
// display name. An inner join would silently drop exactly the records whose
// attribution is incomplete, which is the opposite of reporting it.
//
// Ordering is total and stable: commit time descending, then run id, then the
// record's ordinal within its run. Pending runs have no commit time and sort
// last as a group under NULLS LAST, which is also how they read - staged output
// after the committed output it will join.
func Records(ctx context.Context, db *sql.DB, filter RecordFilter) ([]FleetRecord, error) {
	limit, offset, err := filter.bounds()
	if err != nil {
		return nil, err
	}

	where := []string{`r.deployment_id = ` + placeholder(1)}
	args := []any{filter.DeploymentID}
	next := 2

	if !filter.IncludePending {
		where = append(where, `r.sync_state = `+placeholder(next))
		args, next = append(args, SyncCommitted), next+1
	}
	where, args, next = appendAnyOf(where, args, next, `i.host_id`, filter.Hosts)
	where, args, next = appendAnyOf(where, args, next, `r.origin_instance_id`, filter.Instances)
	where, args, next = appendAnyOf(where, args, next, `r.run_id`, filter.RunIDs)
	where, args, next = appendAnyOf(where, args, next, `c.record_id`, filter.RecordIDs)
	kinds := make([]string, len(filter.Kinds))
	for i, kind := range filter.Kinds {
		if !kind.valid() {
			return nil, fmt.Errorf("%w: unknown record kind %q", ErrRecordFilter, kind)
		}
		kinds[i] = string(kind)
	}
	where, args, next = appendAnyOf(where, args, next, `c.kind`, kinds)
	if !filter.Since.IsZero() {
		where = append(where, `r.committed_at >= `+placeholder(next))
		args, next = append(args, filter.Since.UTC()), next+1
	}
	if !filter.Until.IsZero() {
		where = append(where, `r.committed_at <= `+placeholder(next))
		args, next = append(args, filter.Until.UTC()), next+1
	}

	query := `SELECT` + recordColumns + `
		  FROM analysis_records c
		  JOIN analysis_runs r ON r.run_id = c.run_id
		  LEFT JOIN instances i ON i.instance_id = r.origin_instance_id
		  LEFT JOIN hosts h ON h.host_id = i.host_id
		 WHERE ` + strings.Join(where, " AND ") + `
		 ORDER BY r.committed_at DESC NULLS LAST, r.run_id, c.ordinal
		 LIMIT ` + placeholder(next) + ` OFFSET ` + placeholder(next+1)
	args = append(args, limit, offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list fleet analysis records: %w", err)
	}
	defer rows.Close()

	var out []FleetRecord
	for rows.Next() {
		rec, err := scanFleetRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan fleet analysis record: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fleet analysis records: %w", err)
	}
	return out, nil
}

// RecordHost is one machine that has committed Phase B analysis, with how much
// of it and how recently.
//
// It is what a host filter is built from. Offering the chip a list of every
// registered host would show machines that have published archives and no
// analysis, and offering it the distinct hosts of the current page would make
// the filter's options change as the operator pages through it.
type RecordHost struct {
	// HostID is empty for the group whose origin instances registered before
	// migrations/0007 and therefore have no host attribution. It is reported
	// rather than hidden: an operator seeing "unattributed, 12 records" knows
	// to expect those machines' next registration to name them, whereas a
	// silently dropped group looks like records that do not exist.
	HostID      string
	DisplayName string
	// Records counts this host's records inside the filter's other bounds.
	Records int
	// NewestCommit is the newest commit time among them, nil when every one
	// of them is still pending.
	NewestCommit *time.Time
	// Pending counts how many of them are staged rather than committed. It is
	// always zero unless the filter admitted pending output, and it is what
	// makes a host's outage visible in the filter itself.
	Pending int
}

// RecordHosts reports the hosts that hold records matching filter, ordered by
// newest commit first, then host id.
//
// Limit and Offset are ignored: this is the filter's own vocabulary, and
// paginating it would make a chip's options depend on which page of records the
// operator happens to be reading.
func RecordHosts(ctx context.Context, db *sql.DB, filter RecordFilter) ([]RecordHost, error) {
	filter.Limit, filter.Offset = 0, 0
	if filter.DeploymentID == "" {
		return nil, fmt.Errorf("%w: deployment id is required", ErrRecordFilter)
	}

	where := []string{`r.deployment_id = ` + placeholder(1)}
	args := []any{filter.DeploymentID}
	next := 2
	if !filter.IncludePending {
		where = append(where, `r.sync_state = `+placeholder(next))
		args, next = append(args, SyncCommitted), next+1
	}
	kinds := make([]string, len(filter.Kinds))
	for i, kind := range filter.Kinds {
		if !kind.valid() {
			return nil, fmt.Errorf("%w: unknown record kind %q", ErrRecordFilter, kind)
		}
		kinds[i] = string(kind)
	}
	where, args, _ = appendAnyOf(where, args, next, `c.kind`, kinds)

	rows, err := db.QueryContext(ctx, `
		SELECT coalesce(i.host_id, ''), coalesce(h.display_name, ''),
		       count(*), max(r.committed_at),
		       count(*) FILTER (WHERE r.sync_state <> `+placeholder(len(args)+1)+`)
		  FROM analysis_records c
		  JOIN analysis_runs r ON r.run_id = c.run_id
		  LEFT JOIN instances i ON i.instance_id = r.origin_instance_id
		  LEFT JOIN hosts h ON h.host_id = i.host_id
		 WHERE `+strings.Join(where, " AND ")+`
		 GROUP BY coalesce(i.host_id, ''), coalesce(h.display_name, '')
		 ORDER BY max(r.committed_at) DESC NULLS LAST, 1`,
		append(args, SyncCommitted)...)
	if err != nil {
		return nil, fmt.Errorf("list fleet analysis hosts: %w", err)
	}
	defer rows.Close()

	var out []RecordHost
	for rows.Next() {
		var (
			host   RecordHost
			newest sql.NullTime
		)
		if err := rows.Scan(&host.HostID, &host.DisplayName,
			&host.Records, &newest, &host.Pending); err != nil {
			return nil, fmt.Errorf("scan fleet analysis host: %w", err)
		}
		if newest.Valid {
			t := newest.Time
			host.NewestCommit = &t
		}
		out = append(out, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list fleet analysis hosts: %w", err)
	}
	return out, nil
}

// RecordSyncStates maps every named record id to its sync state, omitting ids
// the catalog does not hold at all.
//
// This is how a local listing renders per-record sync state (SPEC.md 6.5's
// visible staging, issue #109 item 3) without keeping a second answer to the
// question. The shared catalog is the authority on whether a record has
// committed remotely - that is what committing remotely means - so a local
// journal claiming otherwise would be a claim, and an absent id here is the
// honest "not yet", not a lookup failure.
//
// An empty ids slice queries nothing and returns an empty map: a caller with
// nothing to ask about must not accidentally read the whole deployment.
func RecordSyncStates(ctx context.Context, db *sql.DB, ids []string) (map[string]string, error) {
	states := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return states, nil
	}
	where, args, _ := appendAnyOf(nil, nil, 1, `c.record_id`, ids)
	rows, err := db.QueryContext(ctx, `
		SELECT c.record_id, r.sync_state
		  FROM analysis_records c
		  JOIN analysis_runs r ON r.run_id = c.run_id
		 WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, fmt.Errorf("read analysis record sync states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, fmt.Errorf("scan analysis record sync state: %w", err)
		}
		states[id] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read analysis record sync states: %w", err)
	}
	return states, nil
}

func (f RecordFilter) bounds() (limit, offset int, err error) {
	if f.DeploymentID == "" {
		return 0, 0, fmt.Errorf("%w: deployment id is required", ErrRecordFilter)
	}
	if f.Offset < 0 {
		return 0, 0, fmt.Errorf("%w: offset %d is negative", ErrRecordFilter, f.Offset)
	}
	if f.Limit < 0 {
		return 0, 0, fmt.Errorf("%w: limit %d is negative", ErrRecordFilter, f.Limit)
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && f.Until.Before(f.Since) {
		return 0, 0, fmt.Errorf("%w: until %s precedes since %s",
			ErrRecordFilter, f.Until.UTC().Format(time.RFC3339), f.Since.UTC().Format(time.RFC3339))
	}
	limit = f.Limit
	if limit == 0 {
		limit = DefaultRecordLimit
	}
	if limit > MaxRecordLimit {
		limit = MaxRecordLimit
	}
	return limit, f.Offset, nil
}

// appendAnyOf adds an any-of condition over values, numbering placeholders from
// next and reporting the next free number.
//
// An empty values slice adds nothing rather than an always-false condition:
// these filters narrow, and a caller passing no hosts is asking for every host.
// Values are bound as parameters, never interpolated, so a host id or record id
// that arrived from a URL query cannot reach the parser as SQL.
func appendAnyOf(where []string, args []any, next int, column string, values []string) ([]string, []any, int) {
	if len(values) == 0 {
		return where, args, next
	}
	marks := make([]string, len(values))
	for i, value := range values {
		marks[i] = placeholder(next)
		args, next = append(args, value), next+1
	}
	return append(where, column+` IN (`+strings.Join(marks, ", ")+`)`), args, next
}

func placeholder(n int) string { return "$" + strconv.Itoa(n) }

func scanFleetRecord(s scanner) (FleetRecord, error) {
	var (
		out                       FleetRecord
		kind, keyID               string
		execHost, continues       sql.NullString
		instanceHost, displayName sql.NullString
		committed                 sql.NullTime
	)
	if err := s.Scan(
		&out.Record.RecordID, &out.Record.RunID, &kind, &out.Record.Schema, &out.Record.Ordinal,
		&out.Record.ObjectKey, &out.Record.ObjectDigest, &keyID,
		&out.Record.CiphertextSize, &out.Record.CreatedAt,
		&out.OriginInstanceID, &execHost, &continues,
		&out.SyncState, &committed,
		&instanceHost, &displayName); err != nil {
		return FleetRecord{}, err
	}
	out.Record.Kind, out.Record.KeyID = RecordKind(kind), envelope.KeyID(keyID)
	out.ExecutionHostID, out.ContinuesRunID = execHost.String, continues.String
	out.HostID, out.HostDisplayName = instanceHost.String, displayName.String
	if committed.Valid {
		t := committed.Time
		out.CommittedAt = &t
	}
	return out, nil
}
