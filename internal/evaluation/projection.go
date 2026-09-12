package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/durable"
	_ "modernc.org/sqlite"
)

// This file is evaluation's read projection: a rebuildable SQLite database
// holding the ranked, covered, grouped view that the operator's pages and the
// worker's selection both read.
//
// It is a separate file from the durable evaluation store, and that separation
// is the whole design. Records are the durable source and cannot be recomputed;
// everything here is derived from them, so losing this file costs one refresh
// and never analysis. That is also why Open may discard and recreate it on a
// schema or integrity surprise — the same licence internal/index has and the
// durable frontier deliberately does not.
//
// Four properties hold.
//
// A page read touches no payload. §E5 requires page reads to have bounded
// payload work and forbids scanning or decrypting every evaluation, so all of
// the scanning, decoding and ranking happens once per refresh and a page is one
// indexed query over stored rows plus a JSON decode per row shown.
//
// The complete eligible set is ordered before it is paged. A rank row exists
// for every item in every sort, so "page two" is positions 51-100 of one total
// order rather than a second local sort of a different set.
//
// A snapshot is the pagination contract. Each refresh writes a new snapshot id
// and the previous ones survive for a bounded window, so a client paging while
// a publication lands keeps reading the order it started with. A pinned
// snapshot that has aged out is reported as substituted rather than failing,
// because refusing to answer would be worse than answering honestly late.
//
// Nothing here is authoritative about anything. Positions, scores and coverage
// are derived, and §5.4's rule that rank is never evidence strength applies with
// force: the things being ordered are somebody's claims.

// projectionSchema is bumped whenever the stored columns or the derivations
// they hold change. A mismatch discards the file, which is safe because every
// row is derived: the alternative, migrating a cache, would be work spent to
// preserve rows one refresh reproduces exactly.
const projectionSchema = "1"

// ProjectionFileName is the projection database's name inside the evaluation
// directory. It is named so an operator can delete the cache and so diagnostics
// can report its size.
const ProjectionFileName = "evaluation-projection.db"

// snapshotRetention is how many ranked snapshots survive a refresh.
//
// Three, against one cost and one requirement. The requirement is §E5's
// pagination consistency: a client paging through a large result must not have
// its snapshot pulled out from under it by the next periodic refresh, and three
// snapshots at the default hourly cadence is a two-hour window to finish
// reading. The cost is that a snapshot is a full copy of the ranked projection,
// so retention multiplies the file size — three is the smallest number that
// survives one refresh landing mid-page and one more arriving while the client
// is still reading.
const snapshotRetention = 3

// Page size bounds. A caller that forgot a limit gets a page rather than the
// deployment, which is internal/frontier's rule restated for the same reason.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// projection is the rebuildable read database.
type projection struct {
	db   *sql.DB
	path string
}

// openProjection opens <dir>/evaluation-projection.db, creating the directory
// and schema when needed.
//
// A corrupt or incompatible file is removed and rebuilt once; only the failure
// of that clean rebuild is returned. The rebuild loses no analysis — the caller
// re-derives from the durable records, which is the same work that produced the
// discarded rows — but it does lose the ranked snapshots, so a caller that was
// paging gets the substitution notice rather than silence.
func openProjection(dir string) (*projection, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create evaluation directory: %w", err)
	}
	path := filepath.Join(dir, ProjectionFileName)
	proj, err := openProjectionFile(path)
	if err == nil {
		return proj, nil
	}
	removeProjection(path)
	proj, err = openProjectionFile(path)
	if err != nil {
		return nil, fmt.Errorf("open evaluation projection after rebuild: %w", err)
	}
	return proj, nil
}

func openProjectionFile(path string) (*projection, error) {
	db, err := durable.Open(path)
	if err != nil {
		return nil, err
	}
	proj := &projection{db: db, path: path}
	if err := proj.init(); err != nil {
		db.Close()
		return nil, err
	}
	return proj, nil
}

func removeProjection(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-shm")
	_ = os.Remove(path + "-wal")
}

// Close releases the database handle.
func (p *projection) Close() error { return p.db.Close() }

// Path reports the projection file, which an operator needs in order to delete
// the cache and diagnostics need in order to report its size.
func (p *projection) Path() string { return p.path }

// projectionDDL is the whole projection.
//
// Three shapes are worth reading twice.
//
// eval_item holds one JSON document per artifact beside the columns a query
// filters and sorts on. The document is what a page renders and the columns are
// what SQLite can use an index for; storing the document rather than thirty
// columns is deliberate, because every one of those columns would be a second
// place a derivation lives and none of them is queryable — a page filters on
// kind, lane and coverage and renders everything else.
//
// eval_rank is the complete order for each sort, materialised. It is the row
// count times the sort count, which is the price of ordering the eligible set
// before paging it; the alternative is an ORDER BY over a derived score that
// SQLite would have to recompute per page from columns that do not exist.
//
// eval_policy and eval_checkpoint are deliberately NOT snapshot-scoped. They are
// what this instance has learned about the deployment's configuration and its
// coverage sweeps, and that knowledge must survive a refresh whose source read
// failed — otherwise a catalog outage would make an independent reader forget
// the fleet's approved policy and fall back to a disabled default while
// reporting it as fact.
const projectionDDL = `
CREATE TABLE IF NOT EXISTS eval_meta(k TEXT PRIMARY KEY, v TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS eval_snapshot(
	id            TEXT PRIMARY KEY,
	created_at    TEXT NOT NULL,
	input_digest  TEXT NOT NULL,
	policy_version TEXT NOT NULL,
	unavailable   TEXT NOT NULL DEFAULT '',
	items         INTEGER NOT NULL DEFAULT 0,
	active        INTEGER NOT NULL DEFAULT 0);

CREATE TABLE IF NOT EXISTS eval_item(
	snapshot       TEXT NOT NULL,
	kind           TEXT NOT NULL,
	id             TEXT NOT NULL,
	lane           TEXT NOT NULL,
	coverage       TEXT NOT NULL,
	created_at     TEXT NOT NULL,
	score          REAL NOT NULL,
	group_key      TEXT NOT NULL DEFAULT '',
	reconsider     INTEGER NOT NULL DEFAULT 0,
	overdue        INTEGER NOT NULL DEFAULT 0,
	outstanding    INTEGER NOT NULL DEFAULT 0,
	projected_json TEXT NOT NULL,
	PRIMARY KEY(snapshot, kind, id)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS eval_role(
	snapshot TEXT NOT NULL,
	kind     TEXT NOT NULL,
	id       TEXT NOT NULL,
	role     TEXT NOT NULL,
	state    TEXT NOT NULL,
	reason   TEXT NOT NULL DEFAULT '',
	reviews  INTEGER NOT NULL DEFAULT 0,
	overdue  INTEGER NOT NULL DEFAULT 0,
	required INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(snapshot, kind, id, role)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS eval_rank(
	snapshot TEXT NOT NULL,
	sort     TEXT NOT NULL,
	position INTEGER NOT NULL,
	kind     TEXT NOT NULL,
	id       TEXT NOT NULL,
	PRIMARY KEY(snapshot, sort, position)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS eval_rank_subject ON eval_rank(snapshot, kind, id);

CREATE TABLE IF NOT EXISTS eval_history(
	snapshot   TEXT NOT NULL,
	kind       TEXT NOT NULL,
	id         TEXT NOT NULL,
	seq        INTEGER NOT NULL,
	record_json TEXT NOT NULL,
	PRIMARY KEY(snapshot, kind, id, seq)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS eval_assignment(
	snapshot        TEXT NOT NULL,
	kind            TEXT NOT NULL,
	id              TEXT NOT NULL,
	seq             INTEGER NOT NULL,
	assignment_json TEXT NOT NULL,
	PRIMARY KEY(snapshot, kind, id, seq)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS eval_attempt(
	snapshot     TEXT NOT NULL,
	seq          INTEGER NOT NULL,
	assignment_id TEXT NOT NULL,
	attempt_json TEXT NOT NULL,
	PRIMARY KEY(snapshot, seq)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS eval_attempt_assignment ON eval_attempt(snapshot, assignment_id);

CREATE TABLE IF NOT EXISTS eval_inventory(
	snapshot       TEXT NOT NULL,
	kind           TEXT NOT NULL,
	reviewable     INTEGER NOT NULL DEFAULT 0,
	reason         TEXT NOT NULL DEFAULT '',
	local          INTEGER NOT NULL DEFAULT 0,
	local_counted  INTEGER NOT NULL DEFAULT 0,
	fleet          INTEGER NOT NULL DEFAULT 0,
	fleet_counted  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(snapshot, kind)) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS eval_policy(
	record_id   TEXT PRIMARY KEY,
	created_at  TEXT NOT NULL,
	origin      TEXT NOT NULL,
	actor_kind  TEXT NOT NULL DEFAULT '',
	actor_id    TEXT NOT NULL DEFAULT '',
	policy_json TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS eval_checkpoint(
	at           TEXT PRIMARY KEY,
	input_digest TEXT NOT NULL,
	covered      INTEGER NOT NULL DEFAULT 0);

CREATE TABLE IF NOT EXISTS eval_draw(
	seq            INTEGER PRIMARY KEY AUTOINCREMENT,
	at             TEXT NOT NULL,
	run_id         TEXT NOT NULL DEFAULT '',
	seed           TEXT NOT NULL DEFAULT '',
	input_digest   TEXT NOT NULL DEFAULT '',
	policy_version TEXT NOT NULL DEFAULT '',
	lane           TEXT NOT NULL DEFAULT '',
	role           TEXT NOT NULL DEFAULT '',
	subject_kind   TEXT NOT NULL DEFAULT '',
	subject_id     TEXT NOT NULL DEFAULT '',
	assignment_id  TEXT NOT NULL DEFAULT '',
	stop_reason    TEXT NOT NULL DEFAULT '');`

func (p *projection) init() error {
	var integrity string
	if err := p.db.QueryRow(`PRAGMA quick_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("check evaluation projection integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("check evaluation projection integrity: %s", integrity)
	}
	var journal string
	if err := p.db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&journal); err != nil {
		return fmt.Errorf("enable evaluation projection WAL: %w", err)
	}
	if _, err := p.db.Exec(durable.BusyPragma); err != nil {
		return fmt.Errorf("set evaluation projection busy timeout: %w", err)
	}
	if _, err := p.db.Exec(projectionDDL); err != nil {
		return fmt.Errorf("initialize evaluation projection schema: %w", err)
	}
	var version string
	err := p.db.QueryRow(`SELECT v FROM eval_meta WHERE k = 'schema_version'`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := p.db.Exec(
			`INSERT INTO eval_meta(k, v) VALUES('schema_version', ?)`, projectionSchema); err != nil {
			return fmt.Errorf("record evaluation projection schema: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read evaluation projection schema: %w", err)
	case version != projectionSchema:
		return fmt.Errorf("unsupported evaluation projection schema %q", version)
	}
	// Naming every expected column turns a partially created or drifted
	// table into a schema error, which openProjection resolves with the same
	// clean rebuild a version mismatch gets.
	rows, err := p.db.Query(`SELECT i.snapshot, i.kind, i.id, i.lane, i.coverage, i.created_at,
		i.score, i.group_key, i.reconsider, i.overdue, i.outstanding, i.projected_json
		FROM eval_item i LIMIT 0`)
	if err != nil {
		return fmt.Errorf("validate evaluation projection schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = p.db.Query(`SELECT r.snapshot, r.kind, r.id, r.role, r.state, r.reason,
		r.reviews, r.overdue, r.required FROM eval_role r LIMIT 0`)
	if err != nil {
		return fmt.Errorf("validate evaluation role coverage schema: %w", err)
	}
	return rows.Close()
}

// snapshotInput is one complete refresh, ready to be written.
type snapshotInput struct {
	ID            string
	CreatedAt     time.Time
	InputDigest   string
	PolicyVersion string
	// Unavailable is why this snapshot is incomplete, empty when it is not.
	// It is stored with the snapshot rather than in a variable because a
	// page served from a degraded snapshot has to say so however long after
	// the refresh it is read.
	Unavailable string
	// Active counts the assignments claimed and not yet settled at refresh
	// time, which is what tells a reader whether authorized work is running.
	Active      int
	Items       []projected
	Ranks       map[string][]int
	History     map[Subject][]Record
	Assignments map[Subject][]Assignment
	// Attempts is the whole exposure/completion/skip/failure journal this
	// refresh saw, local and remote. It is stored snapshot-scoped because a
	// draw on a non-producing instance has to account for what other hosts
	// already spent, and re-reading the fleet at draw time would make every
	// draw a network operation.
	Attempts  []Attempt
	Inventory []KindInventory
}

// snapshotMeta is one stored snapshot's identity and state.
type snapshotMeta struct {
	ID            string
	CreatedAt     time.Time
	InputDigest   string
	PolicyVersion string
	Unavailable   string
	Items         int
	Active        int
}

// write stores one snapshot and prunes the ones past retention.
//
// One transaction, so a reader never observes a half-written snapshot: the
// current-snapshot pointer moves in the same commit that inserts the rows, and
// until it does, every read is served by the previous snapshot.
func (p *projection) write(ctx context.Context, in snapshotInput) error {
	if in.ID == "" {
		return fmt.Errorf("%w: snapshot has no id", ErrInvalid)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("evaluation: begin projection write: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO eval_snapshot(
		id, created_at, input_digest, policy_version, unavailable, items, active)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET created_at = excluded.created_at,
			input_digest = excluded.input_digest, policy_version = excluded.policy_version,
			unavailable = excluded.unavailable, items = excluded.items, active = excluded.active`,
		in.ID, formatProjectionTime(in.CreatedAt), in.InputDigest, in.PolicyVersion,
		in.Unavailable, len(in.Items), in.Active); err != nil {
		return fmt.Errorf("evaluation: record snapshot: %w", err)
	}
	for _, table := range []string{"eval_item", "eval_role", "eval_rank",
		"eval_history", "eval_assignment", "eval_attempt", "eval_inventory"} {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE snapshot = ?`, in.ID); err != nil {
			return fmt.Errorf("evaluation: clear %s: %w", table, err)
		}
	}

	for i := range in.Items {
		item := &in.Items[i]
		encoded, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("evaluation: encode projected item: %w", err)
		}
		subject := item.Artifact.Subject
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_item(
			snapshot, kind, id, lane, coverage, created_at, score, group_key,
			reconsider, overdue, outstanding, projected_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, subject.Kind, subject.ID, item.Lane, item.Coverage,
			formatProjectionTime(item.Artifact.CreatedAt), item.Score, item.Group,
			boolToInt(item.Reconsider), boolToInt(item.Overdue), item.outstanding(),
			string(encoded)); err != nil {
			return fmt.Errorf("evaluation: store projected item: %w", err)
		}
		for _, role := range item.Roles {
			if _, err := tx.ExecContext(ctx, `INSERT INTO eval_role(
				snapshot, kind, id, role, state, reason, reviews, overdue, required)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				in.ID, subject.Kind, subject.ID, role.Role, role.State, role.Reason,
				role.Reviews, boolToInt(role.Overdue),
				boolToInt(item.Required[role.Role])); err != nil {
				return fmt.Errorf("evaluation: store role coverage: %w", err)
			}
		}
	}

	for sortName, order := range in.Ranks {
		for position, index := range order {
			if index < 0 || index >= len(in.Items) {
				return fmt.Errorf("%w: rank %s position %d names item %d of %d",
					ErrInvalid, sortName, position, index, len(in.Items))
			}
			subject := in.Items[index].Artifact.Subject
			if _, err := tx.ExecContext(ctx, `INSERT INTO eval_rank(
				snapshot, sort, position, kind, id) VALUES(?, ?, ?, ?, ?)`,
				in.ID, sortName, position, subject.Kind, subject.ID); err != nil {
				return fmt.Errorf("evaluation: store rank: %w", err)
			}
		}
	}

	for subject, records := range in.History {
		for seq, record := range records {
			encoded, err := json.Marshal(record)
			if err != nil {
				return fmt.Errorf("evaluation: encode history record: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO eval_history(
				snapshot, kind, id, seq, record_json) VALUES(?, ?, ?, ?, ?)`,
				in.ID, subject.Kind, subject.ID, seq, string(encoded)); err != nil {
				return fmt.Errorf("evaluation: store history: %w", err)
			}
		}
	}
	for subject, assignments := range in.Assignments {
		for seq, assignment := range assignments {
			encoded, err := json.Marshal(assignment)
			if err != nil {
				return fmt.Errorf("evaluation: encode assignment: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO eval_assignment(
				snapshot, kind, id, seq, assignment_json) VALUES(?, ?, ?, ?, ?)`,
				in.ID, subject.Kind, subject.ID, seq, string(encoded)); err != nil {
				return fmt.Errorf("evaluation: store assignment: %w", err)
			}
		}
	}
	for seq, attempt := range in.Attempts {
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return fmt.Errorf("evaluation: encode attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_attempt(
			snapshot, seq, assignment_id, attempt_json) VALUES(?, ?, ?, ?)`,
			in.ID, seq, attempt.AssignmentID, string(encoded)); err != nil {
			return fmt.Errorf("evaluation: store attempt: %w", err)
		}
	}
	for _, entry := range in.Inventory {
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_inventory(
			snapshot, kind, reviewable, reason, local, local_counted, fleet, fleet_counted)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			in.ID, entry.Kind, boolToInt(entry.Reviewable), entry.Reason,
			entry.Local, boolToInt(entry.LocalCounted),
			entry.Fleet, boolToInt(entry.FleetCounted)); err != nil {
			return fmt.Errorf("evaluation: store inventory: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO eval_meta(k, v) VALUES('current_snapshot', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, in.ID); err != nil {
		return fmt.Errorf("evaluation: move current snapshot: %w", err)
	}
	if err := pruneSnapshots(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("evaluation: commit projection write: %w", err)
	}
	return nil
}

// pruneSnapshots drops everything past retention.
//
// Newest first by creation then id, so two snapshots written in the same
// millisecond still prune deterministically. The current snapshot is always
// among the newest, so it can never be the one pruned.
func pruneSnapshots(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM eval_snapshot ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?`,
		snapshotRetention)
	if err != nil {
		return fmt.Errorf("evaluation: list stale snapshots: %w", err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("evaluation: scan stale snapshot: %w", err)
		}
		stale = append(stale, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range stale {
		for _, table := range []string{"eval_item", "eval_role", "eval_rank",
			"eval_history", "eval_assignment", "eval_attempt",
			"eval_inventory", "eval_snapshot"} {
			column := "snapshot"
			if table == "eval_snapshot" {
				column = "id"
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE `+column+` = ?`, id); err != nil {
				return fmt.Errorf("evaluation: prune %s: %w", table, err)
			}
		}
	}
	return nil
}

// current reports the newest snapshot, or false when none has been written.
func (p *projection) current(ctx context.Context) (snapshotMeta, bool, error) {
	var id string
	err := p.db.QueryRowContext(ctx,
		`SELECT v FROM eval_meta WHERE k = 'current_snapshot'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotMeta{}, false, nil
	}
	if err != nil {
		return snapshotMeta{}, false, fmt.Errorf("evaluation: read current snapshot: %w", err)
	}
	meta, ok, err := p.snapshot(ctx, id)
	return meta, ok, err
}

// claimSweep takes the deployment-local right to rebuild the projection.
//
// The in-memory mutex only serializes one process. Every `babel evaluate` is
// its own process, so N of them starting at once each found the snapshot stale
// and each paid for a full scan of the corpus - measured 2026-09-12: 32
// concurrent draws produced three engine sessions and no reviews, because the
// work was in the rebuilds rather than in the reviews.
//
// The lease is a row rather than a file lock: it expires, so a process killed
// mid-sweep does not wedge the deployment, and a caller that loses the race
// keeps serving the snapshot that already exists rather than waiting.
func (p *projection) claimSweep(ctx context.Context, now time.Time, lease time.Duration) (bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("evaluation: claim sweep: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var until string
	err = tx.QueryRowContext(ctx,
		`SELECT v FROM eval_meta WHERE k = 'sweep_lease'`).Scan(&until)
	switch {
	case err == nil:
		if at, parseErr := parseProjectionTime(until); parseErr == nil && now.Before(at) {
			return false, nil
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return false, fmt.Errorf("evaluation: read sweep lease: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO eval_meta(k, v) VALUES('sweep_lease', ?)
		 ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		formatProjectionTime(now.Add(lease))); err != nil {
		return false, fmt.Errorf("evaluation: take sweep lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("evaluation: commit sweep lease: %w", err)
	}
	return true, nil
}

// releaseSweep drops the lease so the next cadence window is not spent waiting
// for it to expire.
func (p *projection) releaseSweep(ctx context.Context) {
	_, _ = p.db.ExecContext(ctx, `DELETE FROM eval_meta WHERE k = 'sweep_lease'`)
}

// snapshot reads one snapshot's metadata.
func (p *projection) snapshot(ctx context.Context, id string) (snapshotMeta, bool, error) {
	var (
		meta      snapshotMeta
		createdAt string
	)
	err := p.db.QueryRowContext(ctx, `SELECT id, created_at, input_digest, policy_version,
		unavailable, items, active FROM eval_snapshot WHERE id = ?`, id).Scan(
		&meta.ID, &createdAt, &meta.InputDigest, &meta.PolicyVersion,
		&meta.Unavailable, &meta.Items, &meta.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshotMeta{}, false, nil
	}
	if err != nil {
		return snapshotMeta{}, false, fmt.Errorf("evaluation: read snapshot %s: %w", id, err)
	}
	meta.CreatedAt, err = parseProjectionTime(createdAt)
	if err != nil {
		return snapshotMeta{}, false, err
	}
	return meta, true, nil
}

// resolveSnapshot picks the snapshot a query will be served from.
//
// A pinned snapshot that still exists is used verbatim, which is the
// consistency contract. One that has aged out is substituted by the current
// snapshot with a stated reason, rather than refused: §E5 asks for an explicit
// pagination consistency contract, and "your snapshot expired, here is the
// current one, the order may have changed" is a contract a client can act on
// where an error is only a dead end.
func (p *projection) resolveSnapshot(ctx context.Context, pinned string) (snapshotMeta, string, error) {
	if pinned != "" {
		meta, ok, err := p.snapshot(ctx, pinned)
		if err != nil {
			return snapshotMeta{}, "", err
		}
		if ok {
			return meta, "", nil
		}
	}
	meta, ok, err := p.current(ctx)
	if err != nil {
		return snapshotMeta{}, "", err
	}
	if !ok {
		return snapshotMeta{}, "", fmt.Errorf(
			"%w: no evaluation projection has been built on this instance yet", ErrUnavailable)
	}
	if pinned != "" {
		return meta, fmt.Sprintf(
			"pinned snapshot %s has aged out after %d refreshes; serving snapshot %s instead, "+
				"so the order may have changed", pinned, snapshotRetention, meta.ID), nil
	}
	return meta, "", nil
}

// page reads one page of the ranked, filtered projection.
//
// Filters narrow the eligible set and the rank positions are then read in
// order, which is why Total is a count over the same predicate rather than the
// snapshot's item count: a filtered view is a different eligible set, and
// reporting the unfiltered total would make a one-item page claim to be the
// first of forty.
func (p *projection) page(ctx context.Context, q Query) (Page, error) {
	if err := validateQuery(q); err != nil {
		return Page{}, err
	}
	meta, substituted, err := p.resolveSnapshot(ctx, q.Snapshot)
	if err != nil {
		return Page{}, err
	}
	sortName := q.Sort
	if sortName == "" {
		sortName = SortRecommended
	}
	limit, offset := pageBounds(q)

	where := []string{`r.snapshot = ?`, `r.sort = ?`}
	args := []any{meta.ID, sortName}
	if q.Kind != "" {
		where = append(where, `i.kind = ?`)
		args = append(args, q.Kind)
	}
	if q.Lane != "" {
		where = append(where, `i.lane = ?`)
		args = append(args, q.Lane)
	}
	switch {
	case q.Coverage == "overdue":
		// Overdue is a property of a role row rather than of the item,
		// so it filters through the role table: an item may be reviewed
		// and overdue at once, which an item-level column could not say.
		clause := `EXISTS (SELECT 1 FROM eval_role rc WHERE rc.snapshot = i.snapshot
			AND rc.kind = i.kind AND rc.id = i.id AND rc.overdue = 1 AND rc.required = 1`
		if q.Role != "" {
			clause += ` AND rc.role = ?`
			args = append(args, q.Role)
		}
		where = append(where, clause+`)`)
	case q.Coverage != "" && q.Role != "":
		where = append(where, `EXISTS (SELECT 1 FROM eval_role rc WHERE rc.snapshot = i.snapshot
			AND rc.kind = i.kind AND rc.id = i.id AND rc.role = ? AND rc.state = ?)`)
		args = append(args, q.Role, q.Coverage)
	case q.Coverage != "":
		where = append(where, `i.coverage = ?`)
		args = append(args, q.Coverage)
	case q.Role != "":
		where = append(where, `EXISTS (SELECT 1 FROM eval_role rc WHERE rc.snapshot = i.snapshot
			AND rc.kind = i.kind AND rc.id = i.id AND rc.role = ?)`)
		args = append(args, q.Role)
	}
	predicate := strings.Join(where, " AND ")
	join := `FROM eval_rank r JOIN eval_item i ON i.snapshot = r.snapshot
		AND i.kind = r.kind AND i.id = r.id WHERE ` + predicate

	page := Page{
		Snapshot:  meta.ID,
		UpdatedAt: meta.CreatedAt,
		Stale:     substituted != "",
	}
	page.Unavailable = joinReasons(meta.Unavailable, substituted)

	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) `+join, args...).
		Scan(&page.Total); err != nil {
		return Page{}, fmt.Errorf("evaluation: count page: %w", err)
	}
	rows, err := p.db.QueryContext(ctx, `SELECT i.projected_json `+join+
		` ORDER BY r.position LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return Page{}, fmt.Errorf("evaluation: read page: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return Page{}, fmt.Errorf("evaluation: scan page row: %w", err)
		}
		var item projected
		if err := json.Unmarshal([]byte(encoded), &item); err != nil {
			return Page{}, fmt.Errorf("evaluation: decode projected item: %w", err)
		}
		page.Items = append(page.Items, newItem(item, q.Role))
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	page.Coverage, err = p.coverage(ctx, meta)
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// detail reads one subject with its history, assignments and alternatives.
func (p *projection) detail(ctx context.Context, subject Subject, role, pinned string) (Detail, error) {
	meta, substituted, err := p.resolveSnapshot(ctx, pinned)
	if err != nil {
		return Detail{}, err
	}
	item, ok, err := p.item(ctx, meta.ID, subject)
	if err != nil {
		return Detail{}, err
	}
	if !ok {
		return Detail{}, fmt.Errorf("%w: %s %s is not in snapshot %s",
			ErrNotFound, subject.Kind, subject.ID, meta.ID)
	}
	out := Detail{Item: newItem(item, role)}
	_ = substituted

	rows, err := p.db.QueryContext(ctx, `SELECT record_json FROM eval_history
		WHERE snapshot = ? AND kind = ? AND id = ? ORDER BY seq`,
		meta.ID, subject.Kind, subject.ID)
	if err != nil {
		return Detail{}, fmt.Errorf("evaluation: read history: %w", err)
	}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return Detail{}, fmt.Errorf("evaluation: scan history: %w", err)
		}
		var record Record
		if err := json.Unmarshal([]byte(encoded), &record); err != nil {
			rows.Close()
			return Detail{}, fmt.Errorf("evaluation: decode history record: %w", err)
		}
		out.History = append(out.History, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Detail{}, err
	}
	rows.Close()

	rows, err = p.db.QueryContext(ctx, `SELECT assignment_json FROM eval_assignment
		WHERE snapshot = ? AND kind = ? AND id = ? ORDER BY seq`,
		meta.ID, subject.Kind, subject.ID)
	if err != nil {
		return Detail{}, fmt.Errorf("evaluation: read assignments: %w", err)
	}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return Detail{}, fmt.Errorf("evaluation: scan assignment: %w", err)
		}
		var assignment Assignment
		if err := json.Unmarshal([]byte(encoded), &assignment); err != nil {
			rows.Close()
			return Detail{}, fmt.Errorf("evaluation: decode assignment: %w", err)
		}
		out.Assignments = append(out.Assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Detail{}, err
	}
	rows.Close()

	// Alternatives are the other members of this subject's group, each a
	// full Item so the comparison view shows their votes, coverage and
	// objections without merging any of them into this record.
	for _, other := range item.Alternatives {
		alternative, ok, err := p.item(ctx, meta.ID, other)
		if err != nil {
			return Detail{}, err
		}
		if !ok {
			continue
		}
		out.Alternatives = append(out.Alternatives, newItem(alternative, role))
	}
	return out, nil
}

// item reads one stored projected item.
func (p *projection) item(ctx context.Context, snapshot string, subject Subject) (projected, bool, error) {
	var encoded string
	err := p.db.QueryRowContext(ctx, `SELECT projected_json FROM eval_item
		WHERE snapshot = ? AND kind = ? AND id = ?`,
		snapshot, subject.Kind, subject.ID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return projected{}, false, nil
	}
	if err != nil {
		return projected{}, false, fmt.Errorf("evaluation: read item: %w", err)
	}
	var item projected
	if err := json.Unmarshal([]byte(encoded), &item); err != nil {
		return projected{}, false, fmt.Errorf("evaluation: decode item: %w", err)
	}
	return item, true, nil
}

// items reads every stored item of one snapshot, which is what selection needs.
//
// The whole set rather than a page: a draw reserves the oldest-due work across
// covered kinds and then samples the remainder by weight, and both are
// properties of the complete eligible set. It is the same cost as one refresh's
// worth of rows and is paid once per cycle rather than once per page.
func (p *projection) items(ctx context.Context, snapshot string) ([]projected, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT projected_json FROM eval_item
		WHERE snapshot = ? ORDER BY kind, id`, snapshot)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read snapshot items: %w", err)
	}
	defer rows.Close()
	var out []projected
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("evaluation: scan snapshot item: %w", err)
		}
		var item projected
		if err := json.Unmarshal([]byte(encoded), &item); err != nil {
			return nil, fmt.Errorf("evaluation: decode snapshot item: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// assignments reads every assignment one snapshot holds, across all subjects.
//
// It is the deployment-wide view a draw needs: another host's live claim is what
// stops this instance from drawing the same review, and the count per subject
// and role is the sample ordinal that makes the next assignment id deterministic
// everywhere. Reading it from the projection rather than from the catalog is
// what keeps a draw a local operation.
func (p *projection) assignments(ctx context.Context, snapshot string) ([]Assignment, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT assignment_json FROM eval_assignment
		WHERE snapshot = ? ORDER BY kind, id, seq`, snapshot)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read snapshot assignments: %w", err)
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("evaluation: scan snapshot assignment: %w", err)
		}
		var assignment Assignment
		if err := json.Unmarshal([]byte(encoded), &assignment); err != nil {
			return nil, fmt.Errorf("evaluation: decode snapshot assignment: %w", err)
		}
		out = append(out, assignment)
	}
	return out, rows.Err()
}

// attempts reads the exposure/completion/skip/failure journal one snapshot
// holds.
//
// It is what makes spend accounting honest on an instance that produced none of
// the work: retries do not inflate a vote, but they do cost, and a draw that
// only counted this machine's receipts would let every participant spend the
// whole shared allowance.
func (p *projection) attempts(ctx context.Context, snapshot string) ([]Attempt, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT attempt_json FROM eval_attempt
		WHERE snapshot = ? ORDER BY seq`, snapshot)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read snapshot attempts: %w", err)
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("evaluation: scan snapshot attempt: %w", err)
		}
		var attempt Attempt
		if err := json.Unmarshal([]byte(encoded), &attempt); err != nil {
			return nil, fmt.Errorf("evaluation: decode snapshot attempt: %w", err)
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

// replaceItem refreshes one subject's rows inside an existing snapshot.
//
// The rank rows are deliberately untouched. A snapshot is the pagination
// contract, so a vote landing while an operator pages must not reshuffle the
// page under them; what has to be current is the item's own content. An item
// that is not already in the snapshot is not inserted either — it arrived after
// this snapshot's input set was captured, and adding it would change the
// snapshot's membership while its order stayed fixed, which is the one thing a
// pinned order cannot survive.
func (p *projection) replaceItem(ctx context.Context, snapshot string,
	item projected, history []Record) error {
	subject := item.Artifact.Subject
	encoded, err := json.Marshal(&item)
	if err != nil {
		return fmt.Errorf("evaluation: encode projected item: %w", err)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("evaluation: begin item replace: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE eval_item SET lane = ?, coverage = ?, score = ?,
		reconsider = ?, overdue = ?, outstanding = ?, projected_json = ?
		WHERE snapshot = ? AND kind = ? AND id = ?`,
		item.Lane, item.Coverage, item.Score, boolToInt(item.Reconsider),
		boolToInt(item.Overdue), item.outstanding(), string(encoded),
		snapshot, subject.Kind, subject.ID)
	if err != nil {
		return fmt.Errorf("evaluation: replace item: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("evaluation: replace item: %w", err)
	} else if affected == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM eval_role
		WHERE snapshot = ? AND kind = ? AND id = ?`,
		snapshot, subject.Kind, subject.ID); err != nil {
		return fmt.Errorf("evaluation: clear role coverage: %w", err)
	}
	for _, role := range item.Roles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_role(
			snapshot, kind, id, role, state, reason, reviews, overdue, required)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snapshot, subject.Kind, subject.ID, role.Role, role.State, role.Reason,
			role.Reviews, boolToInt(role.Overdue),
			boolToInt(item.Required[role.Role])); err != nil {
			return fmt.Errorf("evaluation: store role coverage: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM eval_history
		WHERE snapshot = ? AND kind = ? AND id = ?`,
		snapshot, subject.Kind, subject.ID); err != nil {
		return fmt.Errorf("evaluation: clear history: %w", err)
	}
	for seq, record := range history {
		recordJSON, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("evaluation: encode history record: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_history(
			snapshot, kind, id, seq, record_json) VALUES(?, ?, ?, ?, ?)`,
			snapshot, subject.Kind, subject.ID, seq, string(recordJSON)); err != nil {
			return fmt.Errorf("evaluation: store history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("evaluation: commit item replace: %w", err)
	}
	return nil
}

// policyVersions reports the policy version strings this instance has recorded.
//
// Minting a new version needs to know which ones are taken, and asking the
// stored records rather than counting them is what keeps two saves from landing
// under one version id — which would make "the policy version a draw was taken
// under" ambiguous, and that string is load-bearing for replay.
func (p *projection) policyVersions(ctx context.Context) (map[string]bool, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT json_extract(policy_json, '$.version')
		FROM eval_policy`)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read policy versions: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var version sql.NullString
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("evaluation: scan policy version: %w", err)
		}
		if version.Valid && version.String != "" {
			out[version.String] = true
		}
	}
	return out, rows.Err()
}

// coverage aggregates one snapshot's coverage.
//
// Two different aggregations, deliberately. The flat counters count *items* by
// their obligation-based coverage, which is the "outstanding obligations" an
// operator acts on. ByRole counts *role rows*, including roles nothing
// activated, which is the per-role gap view §8.5 requires to stay visible. One
// aggregation serving both would either hide unactivated gaps or report every
// artifact as under-reviewed for roles nobody asked for.
func (p *projection) coverage(ctx context.Context, meta snapshotMeta) (Coverage, error) {
	out := Coverage{
		UpdatedAt: meta.CreatedAt,
		Active:    meta.Active,
		Reason:    meta.Unavailable,
		ByRole:    make(map[string]CoverageCounts),
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT coverage, COUNT(*) FROM eval_item WHERE snapshot = ? GROUP BY coverage`, meta.ID)
	if err != nil {
		return Coverage{}, fmt.Errorf("evaluation: aggregate coverage: %w", err)
	}
	for rows.Next() {
		var (
			state string
			count int
		)
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return Coverage{}, fmt.Errorf("evaluation: scan coverage: %w", err)
		}
		addCoverage(&out.CoverageCounts, state, count)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Coverage{}, err
	}
	rows.Close()

	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT kind || ':' || id) FROM eval_role
		WHERE snapshot = ? AND overdue = 1 AND required = 1`, meta.ID).
		Scan(&out.Overdue); err != nil {
		return Coverage{}, fmt.Errorf("evaluation: count overdue: %w", err)
	}

	rows, err = p.db.QueryContext(ctx, `SELECT role, state, overdue, COUNT(*)
		FROM eval_role WHERE snapshot = ? GROUP BY role, state, overdue`, meta.ID)
	if err != nil {
		return Coverage{}, fmt.Errorf("evaluation: aggregate role coverage: %w", err)
	}
	for rows.Next() {
		var (
			role    string
			state   string
			overdue int
			count   int
		)
		if err := rows.Scan(&role, &state, &overdue, &count); err != nil {
			rows.Close()
			return Coverage{}, fmt.Errorf("evaluation: scan role coverage: %w", err)
		}
		counts := out.ByRole[role]
		addCoverage(&counts, state, count)
		if overdue == 1 {
			counts.Overdue += count
		}
		out.ByRole[role] = counts
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Coverage{}, err
	}
	rows.Close()

	at, digest, _, ok, err := p.lastCheckpoint(ctx)
	if err != nil {
		return Coverage{}, err
	}
	if ok {
		out.LastCheck = at
		if digest != meta.InputDigest {
			// The last durable coverage sweep inspected a different
			// input set than this snapshot was built from. Saying so
			// is the point: §E4 makes "coverage inspection
			// completed" and "all eligible output reviewed" separate
			// facts, and a check that ran against older inputs is a
			// third fact that must not be read as either.
			out.Reason = joinReasons(out.Reason,
				"the last durable coverage check inspected a different input set than this "+
					"projection was built from")
		}
	} else {
		out.Reason = joinReasons(out.Reason,
			"no durable coverage check has been recorded on this instance yet")
	}
	return out, nil
}

// addCoverage folds one state's count into the flat counters.
//
// An unrecognised state is counted as unsupported rather than dropped, because
// a state this build does not know is a gap in this build and dropping it would
// make the counters silently disagree with the row count they summarise.
func addCoverage(counts *CoverageCounts, state string, count int) {
	switch state {
	case CoverageUnreviewed:
		counts.Unreviewed += count
	case CoverageReviewed:
		counts.Reviewed += count
	case CoverageDue:
		counts.Due += count
	case CoverageBlocked:
		counts.Blocked += count
	case CoverageNotApplicable:
		counts.NotApplicable += count
	default:
		counts.Unsupported += count
	}
}

// inventory reads one snapshot's produced-kind inventory.
func (p *projection) inventory(ctx context.Context, snapshot string) ([]KindInventory, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT kind, reviewable, reason, local, local_counted,
		fleet, fleet_counted FROM eval_inventory WHERE snapshot = ? ORDER BY kind`, snapshot)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read inventory: %w", err)
	}
	defer rows.Close()
	var out []KindInventory
	for rows.Next() {
		var (
			entry                     KindInventory
			reviewable, local, fleet_ int
		)
		if err := rows.Scan(&entry.Kind, &reviewable, &entry.Reason, &entry.Local,
			&local, &entry.Fleet, &fleet_); err != nil {
			return nil, fmt.Errorf("evaluation: scan inventory: %w", err)
		}
		entry.Reviewable = reviewable == 1
		entry.LocalCounted = local == 1
		entry.FleetCounted = fleet_ == 1
		out = append(out, entry)
	}
	return out, rows.Err()
}

// policyRecord is one operator-authored policy publication this instance has
// seen, local or remote.
type policyRecord struct {
	RecordID  string
	CreatedAt time.Time
	Origin    string
	ActorKind string
	ActorID   string
	Policy    Policy
}

// rememberPolicies stores every policy publication this refresh saw.
//
// The table is never cleared and is not snapshot-scoped, which is the whole
// point: it is what this instance knows about the deployment's approved
// configuration, and that knowledge must survive a refresh whose fleet read
// failed. Otherwise a catalog outage would make an independent reader forget the
// fleet's policy and present a disabled default as the deployment's actual
// settings — which §E5 forbids and which would read as "evaluation is off"
// rather than "I cannot see the policy".
func (p *projection) rememberPolicies(ctx context.Context, records []policyRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("evaluation: begin policy write: %w", err)
	}
	defer tx.Rollback()
	for _, record := range records {
		encoded, err := json.Marshal(record.Policy)
		if err != nil {
			return fmt.Errorf("evaluation: encode policy: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO eval_policy(
			record_id, created_at, origin, actor_kind, actor_id, policy_json)
			VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(record_id) DO UPDATE SET created_at = excluded.created_at,
				origin = excluded.origin, actor_kind = excluded.actor_kind,
				actor_id = excluded.actor_id, policy_json = excluded.policy_json`,
			record.RecordID, formatProjectionTime(record.CreatedAt), record.Origin,
			record.ActorKind, record.ActorID, string(encoded)); err != nil {
			return fmt.Errorf("evaluation: store policy record: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("evaluation: commit policy write: %w", err)
	}
	return nil
}

// newestPolicy reports the most recent operator-authored policy this instance
// has seen anywhere in the deployment.
//
// Newest by the record's creation time, then by record id so two policies
// written in the same instant resolve the same way on every instance. That
// determinism matters more than the tie-break itself: two readers disagreeing
// about which policy is effective would spend two different budgets.
func (p *projection) newestPolicy(ctx context.Context) (policyRecord, bool, error) {
	var (
		record    policyRecord
		createdAt string
		encoded   string
	)
	err := p.db.QueryRowContext(ctx, `SELECT record_id, created_at, origin, actor_kind, actor_id,
		policy_json FROM eval_policy ORDER BY created_at DESC, record_id DESC LIMIT 1`).Scan(
		&record.RecordID, &createdAt, &record.Origin, &record.ActorKind,
		&record.ActorID, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return policyRecord{}, false, nil
	}
	if err != nil {
		return policyRecord{}, false, fmt.Errorf("evaluation: read newest policy: %w", err)
	}
	if record.CreatedAt, err = parseProjectionTime(createdAt); err != nil {
		return policyRecord{}, false, err
	}
	if err := json.Unmarshal([]byte(encoded), &record.Policy); err != nil {
		return policyRecord{}, false, fmt.Errorf("evaluation: decode stored policy: %w", err)
	}
	return record, true, nil
}

// rememberCheckpoint records that a coverage sweep completed.
//
// It is stored here as well as published as a record because the question "when
// did a sweep last finish" has to be answerable on an instance whose fleet read
// is failing — that is precisely when an operator asks it.
func (p *projection) rememberCheckpoint(ctx context.Context, at time.Time,
	inputDigest string, covered int) error {
	if _, err := p.db.ExecContext(ctx, `INSERT INTO eval_checkpoint(at, input_digest, covered)
		VALUES(?, ?, ?) ON CONFLICT(at) DO UPDATE SET input_digest = excluded.input_digest,
			covered = excluded.covered`,
		formatProjectionTime(at), inputDigest, covered); err != nil {
		return fmt.Errorf("evaluation: record coverage checkpoint: %w", err)
	}
	return nil
}

// lastCheckpoint reports the newest recorded coverage sweep.
func (p *projection) lastCheckpoint(ctx context.Context) (time.Time, string, int, bool, error) {
	var (
		at      string
		digest  string
		covered int
	)
	err := p.db.QueryRowContext(ctx, `SELECT at, input_digest, covered FROM eval_checkpoint
		ORDER BY at DESC LIMIT 1`).Scan(&at, &digest, &covered)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", 0, false, nil
	}
	if err != nil {
		return time.Time{}, "", 0, false, fmt.Errorf("evaluation: read coverage checkpoint: %w", err)
	}
	parsed, err := parseProjectionTime(at)
	if err != nil {
		return time.Time{}, "", 0, false, err
	}
	return parsed, digest, covered, true, nil
}

// drawRecord is one recorded selection decision, whether or not it produced an
// assignment.
//
// §E4 requires the policy version, the random seed, the captured input identity,
// the assignment and role, the spend and the stopping reason to be persisted,
// and states outright that a seed alone is not sufficient. This is that row —
// including the draws that produced nothing, because "why did the sampler stop"
// is exactly the question an operator asks when no review is happening.
type drawRecord struct {
	At            time.Time
	RunID         string
	Seed          uint64
	InputDigest   string
	PolicyVersion string
	Lane          string
	Role          string
	Subject       Subject
	AssignmentID  string
	StopReason    string
}

// rememberDraw stores one selection decision.
func (p *projection) rememberDraw(ctx context.Context, draw drawRecord) error {
	if _, err := p.db.ExecContext(ctx, `INSERT INTO eval_draw(at, run_id, seed, input_digest,
		policy_version, lane, role, subject_kind, subject_id, assignment_id, stop_reason)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatProjectionTime(draw.At), draw.RunID, strconv.FormatUint(draw.Seed, 10),
		draw.InputDigest, draw.PolicyVersion, draw.Lane, draw.Role,
		draw.Subject.Kind, draw.Subject.ID, draw.AssignmentID, draw.StopReason); err != nil {
		return fmt.Errorf("evaluation: record draw: %w", err)
	}
	return nil
}

// draws reads the recorded selection decisions, newest first.
//
// It is the replay surface: given these rows and the records they name, a draw
// can be re-derived, which is what makes "replayable draws" a checkable claim
// rather than an intention.
func (p *projection) draws(ctx context.Context, limit int) ([]drawRecord, error) {
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	rows, err := p.db.QueryContext(ctx, `SELECT at, run_id, seed, input_digest, policy_version,
		lane, role, subject_kind, subject_id, assignment_id, stop_reason
		FROM eval_draw ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read draws: %w", err)
	}
	defer rows.Close()
	var out []drawRecord
	for rows.Next() {
		var (
			draw  drawRecord
			at    string
			seed  string
			kind  string
			subID string
		)
		if err := rows.Scan(&at, &draw.RunID, &seed, &draw.InputDigest, &draw.PolicyVersion,
			&draw.Lane, &draw.Role, &kind, &subID, &draw.AssignmentID,
			&draw.StopReason); err != nil {
			return nil, fmt.Errorf("evaluation: scan draw: %w", err)
		}
		if draw.At, err = parseProjectionTime(at); err != nil {
			return nil, err
		}
		draw.Seed, _ = strconv.ParseUint(seed, 10, 64)
		draw.Subject = Subject{Kind: kind, ID: subID}
		out = append(out, draw)
	}
	return out, rows.Err()
}

// newItem renders one stored projected item as the public Item.
//
// role, when named, overrides the combined coverage with that role's own row.
// That is what makes a role-scoped view honest: a page filtered to `evidence`
// shows each item's evidence coverage rather than its weakest obligation, and a
// page with no role shows the obligation an operator would act on.
func newItem(item projected, role string) Item {
	out := Item{
		Artifact:       item.Artifact,
		Reception:      item.Reception,
		ReviewCoverage: item.Roles,
		Coverage:       item.Coverage,
		CoverageReason: item.CoverageReason,
		Lane:           item.Lane,
		Score:          item.Score,
		Reasons:        item.Reasons,
		Objections:     item.Objections,
		WouldChange:    item.WouldChange,
		Group:          item.Group,
		Reconsider:     item.Reconsider,
	}
	if role == "" {
		return out
	}
	for _, coverage := range item.Roles {
		if coverage.Role != role {
			continue
		}
		out.Coverage, out.CoverageReason = coverage.State, coverage.Reason
		if out.CoverageReason == "" && requiresReason(coverage.State) {
			out.CoverageReason = RoleUnsupportedReason(item.Artifact.Subject.Kind, role)
		}
		return out
	}
	// The role is not applicable to this kind at all, which is a named
	// policy decision rather than an absence of review.
	out.Coverage = CoverageNotApplicable
	out.CoverageReason = RoleUnsupportedReason(item.Artifact.Subject.Kind, role)
	return out
}

// validateQuery refuses a query naming a vocabulary value this build does not
// have.
//
// It refuses rather than ignoring, because a browser that asked for lane
// "accpeted" and got the unfiltered list would show an operator a page that
// answers a different question than the one on screen. Limit and offset are
// clamped rather than refused: those are bounds, and a caller asking for a
// thousand rows wants as many as it may have.
func validateQuery(q Query) error {
	if q.Kind != "" && !ValidKind(q.Kind) {
		return fmt.Errorf("%w: kind %q", ErrInvalid, q.Kind)
	}
	if q.Lane != "" && !ValidLane(q.Lane) {
		return fmt.Errorf("%w: lane %q", ErrInvalid, q.Lane)
	}
	if q.Sort != "" && !ValidSort(q.Sort) {
		return fmt.Errorf("%w: sort %q", ErrInvalid, q.Sort)
	}
	if q.Role != "" && !ValidRole(q.Role) {
		return fmt.Errorf("%w: role %q", ErrInvalid, q.Role)
	}
	if q.Coverage != "" && !ValidCoverageFilter(q.Coverage) {
		return fmt.Errorf("%w: coverage %q", ErrInvalid, q.Coverage)
	}
	return nil
}

// pageBounds clamps a query's limit and offset.
func pageBounds(q Query) (limit, offset int) {
	limit = q.Limit
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	if offset = q.Offset; offset < 0 {
		offset = 0
	}
	return limit, offset
}

// projectionTimeLayout is RFC 3339 in UTC with a fixed nine-digit fraction, so
// text order is time order. It is internal/frontier's layout and for its exact
// reason: time.RFC3339Nano trims trailing zeros, which makes a lexicographic
// ORDER BY over timestamps subtly wrong precisely when two events land within a
// tenth of a second of each other.
const projectionTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatProjectionTime(at time.Time) string {
	return at.UTC().Format(projectionTimeLayout)
}

func parseProjectionTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("evaluation: parse projection timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// sortSubjects puts a subject list in a total order, so a stored alternatives
// list and a rebuilt one agree.
func sortSubjects(subjects []Subject) {
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Kind != subjects[j].Kind {
			return subjects[i].Kind < subjects[j].Kind
		}
		return subjects[i].ID < subjects[j].ID
	})
}

// coverageInput is one subject's recorded evaluation history, ready to be
// turned into role coverage.
//
// It is a value rather than a set of store calls so the derivation is a pure
// function: the regressions exercise bare votes versus role coverage, stale
// revisions and budget stops without a database, and one refresh over unchanged
// inputs produces a byte-identical projection.
type coverageInput struct {
	Artifact Artifact
	// Assessments are the completed assessments bound to this exact
	// revision, each paired with the role its assignment carried.
	Assessments []roledAssessment
	// Superseded reports that another revision of this chain carries
	// recorded assessments. It is what makes a revised candidate visibly
	// unreviewed rather than inheriting its predecessor's reception: §E1
	// binds a vote to the exact wording read, so the endorsement must not
	// move to n+1, and the new wording needs attention precisely because the
	// old one had it.
	Superseded bool
	// Reopened reports an explicit operator reopening. It makes review
	// attention due again; a retained reconsider decision does not, because
	// retaining is the operator saying the change does not move them.
	Reopened bool
	// Skips counts the skipped and failed attempts recorded against this
	// subject, which stay visible as gaps and never become negative votes.
	Skips map[string]int
	// AlternativeCount is how many members this subject's alternative group
	// has, which is what activates the comparison role.
	AlternativeCount int
	Policy           Policy
	Now              time.Time
}

// roledAssessment is one completed assessment with the role it answered.
//
// The role lives on the assignment rather than on the record, and joining them
// here is what keeps a bare reception vote from ever satisfying an evidence
// obligation: the two are different rows because they answered different
// questions, and the only thing that knows which question was asked is the
// assignment.
type roledAssessment struct {
	Record Record
	Role   string
}

// deriveRoleCoverage computes one subject's per-role coverage and which of
// those roles something actually activated.
//
// There is one row per applicable role, always, so a page can show a
// role-specific gap; and Required marks only the rows that are obligations. The
// distinction is the product decision: supported roles are not an obligation to
// run every role on every item, so a bare support vote genuinely satisfies
// initial reception review and the reserved coverage lane moves on instead of
// demanding five more passes on the same record.
func deriveRoleCoverage(in coverageInput) ([]RoleCoverage, map[string]bool) {
	kind := in.Artifact.Subject.Kind
	roles := RolesForKind(kind)
	out := make([]RoleCoverage, 0, len(roles))
	required := make(map[string]bool, len(roles))
	if len(roles) == 0 {
		return out, required
	}
	overdue := time.Duration(in.Policy.OverdueSeconds) * time.Second

	for _, role := range roles {
		coverage := RoleCoverage{Role: role}
		for _, assessed := range in.Assessments {
			if assessed.Role != role {
				continue
			}
			coverage.Reviews++
			at := assessmentTime(assessed.Record)
			if at.After(coverage.LastReviewed) {
				coverage.LastReviewed = at
			}
		}
		activated, why := roleActivation(role, in)
		required[role] = activated

		switch {
		case !RoleApplies(kind, role):
			// Unreachable through RolesForKind today; kept because a
			// projection written by an older build can hold a role a
			// newer registry no longer admits, and reporting that as
			// reviewed would be the one lie this state exists to
			// prevent.
			coverage.State = CoverageNotApplicable
			coverage.Reason = RoleUnsupportedReason(kind, role)
		case in.Artifact.Context.Blocked || in.Artifact.Context.Allowance == "excluded":
			coverage.State = CoverageBlocked
			coverage.Reason = blockedReason(in.Artifact.Context)
		case coverage.Reviews == 0:
			coverage.State = CoverageUnreviewed
			coverage.Reason = why
		case coverage.Reviews < in.Policy.InitialReviews:
			coverage.State = CoverageUnreviewed
			coverage.Reason = fmt.Sprintf("%d of %d independent assessments recorded",
				coverage.Reviews, in.Policy.InitialReviews)
		case contextMoved(in):
			coverage.State = CoverageDue
			coverage.Reason = "the recorded work or reality behind this changed after its " +
				"newest assessment, so the existing assessments describe a different situation"
		default:
			coverage.State = CoverageReviewed
		}
		if !activated && coverage.Reason == "" {
			// A role nothing currently activates keeps its trigger on the
			// row even when the recorded assessments already satisfy it,
			// so a reader can see why no further work is scheduled.
			coverage.Reason = why
		}

		// Overdue is measured from when the obligation arose, not from
		// the record's age: an obligation that became due yesterday
		// because context moved is not overdue merely because the
		// artifact is old.
		if activated && (coverage.State == CoverageUnreviewed || coverage.State == CoverageDue) {
			since := coverage.LastReviewed
			if since.IsZero() {
				since = in.Artifact.CreatedAt
			}
			coverage.Overdue = overdue > 0 && in.Now.Sub(since) > overdue
		}
		if skips := in.Skips[role]; skips > 0 {
			coverage.Reason = joinReasons(coverage.Reason, fmt.Sprintf(
				"%d skipped or failed attempt(s) recorded; skips are not votes", skips))
		}
		out = append(out, coverage)
	}
	return out, required
}

// roleActivation reports whether a role is currently an obligation, and the
// sentence that says what would make it one.
//
// The sentence is returned for both answers, which is the point: a role nothing
// activated is a visible gap with a stated trigger rather than either an
// invisible omission or a mandatory task. An operator reading "no recorded
// uncertainty activates an evidence check" knows both that nobody checked the
// evidence and that Babel is not going to spend a review on it yet.
func roleActivation(role string, in coverageInput) (bool, string) {
	switch role {
	case RoleReception:
		// Reception is the initial coverage default for an OPEN idea, and
		// a bare support, opposition or uncertainty vote is a complete
		// reception review — comments, evidence and refinement are
		// optional (docs §1).
		//
		// Once the operator has ruled, generic reception voting stops
		// being the useful question (SPEC §5.8): an accepted proposal
		// needs implementation and outcome assessment, and a rejected or
		// duplicate one has an answer that another vote does not revisit.
		// An explicit reopening puts it back in play, which is the only
		// thing that does.
		if decided(in.Artifact.ReviewStatus) && !in.Reopened {
			return false, "not currently required: the operator has ruled on this, so a " +
				"further reception vote revisits nothing; an explicit reopening puts it " +
				"back in play"
		}
		return true, "initial reception review; a bare support, opposition or uncertainty " +
			"vote satisfies it"
	case RoleEvidence:
		if in.Artifact.Context.Blocked {
			return false, "recorded policy withholds subject-specific work here"
		}
		if recordedUncertainty(in) {
			return true, "recorded uncertainty or conflicting material activates an evidence check"
		}
		return false, "not currently required: an evidence check is activated by recorded " +
			"uncertainty in an assessment or by conflicting material on the record"
	case RoleChallenge:
		if disagreement(in) {
			return true, "persistent disagreement activates one bounded diagnostic challenge"
		}
		return false, "not currently required: a challenge is activated by persistent " +
			"disagreement, not by the absence of consensus"
	case RoleComparison:
		if in.AlternativeCount > 1 {
			return true, fmt.Sprintf("%d alternatives address the same problem, which "+
				"activates a comparison", in.AlternativeCount)
		}
		return false, "not currently required: a comparison is activated when two or more " +
			"alternatives address one problem"
	case RoleOutcome:
		if in.Artifact.ReviewStatus == "accepted" {
			return true, "the operator accepted this, so an implementation or outcome " +
				"assessment becomes useful"
		}
		return false, "not currently required: an outcome assessment follows operator " +
			"acceptance and cannot precede it"
	case RoleRelevance:
		ctx := in.Artifact.Context
		if ctx.CurrentWork || ctx.Pain > 0 {
			return true, "recorded current work or unresolved questions make personal " +
				"relevance worth assessing"
		}
		return false, "not currently required: relevance is activated by recorded current " +
			"work or unresolved questions about the subject"
	}
	return false, RoleUnsupportedReason(in.Artifact.Subject.Kind, role)
}

// decided reports whether an operator has ruled on an artifact.
//
// An empty status and `new` are both "no ruling recorded", which is the state
// an observation or a freshly emitted candidate is in. Everything else is a
// recorded disposition — including `deferred` and `duplicate`, which are
// answers rather than absences: §4.7 keeps the record and the decision, and a
// vote after either revisits a question the operator already closed.
//
// It reads the derived review status rather than the disposition history
// because that derivation is internal/frontier's and is the same one the lane
// vocabulary uses; a second reading of the same events could disagree with the
// lane an operator is looking at.
func decided(reviewStatus string) bool {
	switch reviewStatus {
	case "", "new":
		return false
	}
	return true
}

// recordedUncertainty reports whether anything on the record or in its
// assessments states uncertainty or conflict.
//
// Both sources count, and neither is inferred. An assessment that recorded
// uncertainty said so; a record carrying counter-evidence or conflicting
// material said so when it was written. What is deliberately not here is a
// guess from a low confidence grading: §10 warns that confidence never
// substitutes for evidence, and activating an evidence check because a model
// graded itself "low" would be the same substitution in reverse.
func recordedUncertainty(in coverageInput) bool {
	for _, assessed := range in.Assessments {
		if assessed.Record.Assessment == nil {
			continue
		}
		if strings.TrimSpace(assessed.Record.Assessment.Uncertainty) != "" {
			return true
		}
		for _, result := range assessed.Record.Assessment.Results {
			if !result.Satisfied || strings.TrimSpace(result.Uncertainty) != "" {
				return true
			}
		}
	}
	return artifactStatesConflict(in.Artifact)
}

// artifactStatesConflict reports whether the record itself recorded
// counter-evidence or conflicting material.
func artifactStatesConflict(artifact Artifact) bool {
	switch artifact.Subject.Kind {
	case SubjectKindObservation:
		var payload struct {
			CounterEvidence       []json.RawMessage `json:"counter_evidence"`
			CounterEvidenceAbsent bool              `json:"counter_evidence_absent"`
		}
		if json.Unmarshal(artifact.Body, &payload) == nil {
			return len(payload.CounterEvidence) > 0
		}
	case SubjectKindFinding:
		var payload struct {
			CounterEvidence []json.RawMessage `json:"counter_evidence"`
		}
		if json.Unmarshal(artifact.Body, &payload) == nil {
			return len(payload.CounterEvidence) > 0
		}
	case SubjectKindProposal:
		var payload struct {
			Conflicting   []json.RawMessage `json:"conflicting"`
			Uncertainty   string            `json:"uncertainty"`
			OpenQuestions []string          `json:"open_questions"`
		}
		if json.Unmarshal(artifact.Body, &payload) == nil {
			return len(payload.Conflicting) > 0 ||
				strings.TrimSpace(payload.Uncertainty) != "" ||
				len(payload.OpenQuestions) > 0
		}
	}
	return false
}

// disagreement reports persistent, substantive disagreement in the recorded
// reception.
//
// A split tally alone is not one. SPEC §5.8 says so directly - "a tally alone
// does not establish a substantive dispute" - and the difference matters here
// because this function spends money: activating a challenge on one support
// and one opposition would send a bounded diagnostic task after every pair of
// honest first impressions. So both sides have to be present after the initial
// reviews AND something beyond the count has to be on the record: a stated
// uncertainty, an objection, or an argument a reviewer actually wrote.
//
// It is bounded work rather than an obligation to reach consensus: the
// challenge role is activated once and the per-revision review cap is what
// stops it repeating.
func disagreement(in coverageInput) bool {
	var support, oppose, unsure, reviews int
	substantive := false
	for _, assessed := range in.Assessments {
		if assessed.Role != RoleReception || assessed.Record.Assessment == nil {
			continue
		}
		statement := assessed.Record.Assessment
		switch statement.Vote {
		case VoteSupport:
			support++
		case VoteOppose:
			oppose++
		case VoteUnsure:
			unsure++
		default:
			continue
		}
		reviews++
		if strings.TrimSpace(statement.Uncertainty) != "" {
			substantive = true
		}
		for _, contribution := range statement.Contributions {
			switch contribution.Kind {
			case ContributionObjection, ContributionArgument, ContributionEvidence:
				substantive = true
			}
		}
	}
	if reviews < in.Policy.InitialReviews || !substantive {
		return false
	}
	return (support > 0 && oppose > 0) || unsure*2 > reviews
}

// contextMoved reports that the recorded context changed after the newest
// assessment, or that a predecessor revision holds the only recorded opinion.
//
// Either makes a reassessment due rather than making the item unreviewed: the
// reviews happened, and they described a situation or a wording that has since
// changed. Reporting it as never-reviewed would erase work that was really done.
func contextMoved(in coverageInput) bool {
	if in.Superseded || in.Reopened {
		return true
	}
	current := in.Artifact.ContextVersion
	if current == "" {
		return false
	}
	for _, assessed := range in.Assessments {
		recorded := assessed.Record.Provenance.ContextVersion
		if recorded == "" && assessed.Record.Assessment != nil {
			recorded = assessed.Record.Assessment.ContextVersion
		}
		if recorded != "" && recorded != current {
			return true
		}
	}
	return false
}

// blockedReason renders why recorded policy withholds work on a subject.
//
// It names the allowance and the recorded reasons behind it, because a coverage
// row reading "blocked" with no explanation is indistinguishable from a bug —
// and the store requires a reason for this state for exactly that reason.
func blockedReason(ctx Context) string {
	allowance := ctx.Allowance
	if allowance == "" {
		allowance = "unspecified"
	}
	reason := fmt.Sprintf("recorded work allowance %q withholds subject-specific review", allowance)
	if len(ctx.Reasons) > 0 {
		reason += ": " + strings.Join(ctx.Reasons, "; ")
	}
	return reason
}

// assessmentTime is when an assessment observed what it reports.
//
// The assessment's own AsOf when it stated one, and the record's creation time
// otherwise. The distinction matters for cooldowns and for overdue arithmetic:
// a review of last week's state recorded today is a week-old opinion, and
// treating it as current would rest an obligation that has not actually been met
// recently.
func assessmentTime(record Record) time.Time {
	if record.Assessment != nil && !record.Assessment.AsOf.IsZero() {
		return record.Assessment.AsOf
	}
	return record.CreatedAt
}
