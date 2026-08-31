package sharedcatalog_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// hostSessions is the cross-host read: every host when the filter is empty.
func hostSessions(t *testing.T, db *sql.DB, hostID string) []sharedcatalog.CatalogSessionRow {
	t.Helper()
	rows, err := sharedcatalog.HostSessions(context.Background(), db, hostID)
	if err != nil {
		t.Fatalf("HostSessions(%q): %v", hostID, err)
	}
	return rows
}

// The whole point of migrations/0004: a second instance reading the catalog sees
// something a person can read, not a list of digests. If a published row does
// not carry its title and workspace, the migration bought nothing.
func TestPublishedSessionCarriesTitleAndWorkspace(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{
			SessionUID:        "uid-1",
			Harness:           "omp",
			Title:             new("Close the catalog metadata gap"),
			Workspace:         new("/home/alex/babel"),
			ContinuationGrade: new(true),
		}})

	rows := hostSessions(t, db, "h1")
	if len(rows) != 1 {
		t.Fatalf("HostSessions reported %d rows, want 1: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.Title != "Close the catalog metadata gap" {
		t.Errorf("title = %q, want the published title", got.Title)
	}
	if got.Workspace != "/home/alex/babel" {
		t.Errorf("workspace = %q, want the published workspace", got.Workspace)
	}
	if got.ContinuationGrade == nil || !*got.ContinuationGrade {
		t.Errorf("continuation grade = %v, want a present true", got.ContinuationGrade)
	}
	if got.LatestSnapshotID != "s1" {
		t.Errorf("latest snapshot = %q, want s1", got.LatestSnapshotID)
	}
}

// migrations/0005: the title reaches another machine, and so must the answer to
// "who wrote it". Three different claims render as the same short line of text,
// and a reader on another host cannot re-derive any of them - it has this row
// and nothing else - so a title published without its provenance is a claim
// that instance has no way to check.
func TestPublishedSessionCarriesTitleProvenance(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{
			{
				SessionUID:      "uid-recorded",
				Harness:         "omp",
				Title:           new("Close the catalog metadata gap"),
				TitleProvenance: new("recorded"),
			},
			{
				SessionUID:      "uid-derived",
				Harness:         "codex",
				Title:           new("Node version audit"),
				TitleProvenance: new("derived"),
			},
			{
				SessionUID:      "uid-inferred",
				Harness:         "codex",
				Title:           new("Installing codex profiles on Ubuntu"),
				TitleProvenance: new("inferred"),
			},
			{
				// No title, so no provenance. A provenance here would name the
				// origin of nothing.
				SessionUID: "uid-untitled",
				Harness:    "codex",
			},
		})

	want := map[string]string{
		"uid-recorded": "recorded",
		"uid-derived":  "derived",
		"uid-inferred": "inferred",
		"uid-untitled": "",
	}
	rows := hostSessions(t, db, "h1")
	if len(rows) != len(want) {
		t.Fatalf("HostSessions reported %d rows, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		if got := row.TitleProvenance; got != want[row.SessionUID] {
			t.Errorf("%s: title_provenance = %q, want %q",
				row.SessionUID, got, want[row.SessionUID])
		}
	}
}

// The vocabulary is closed in the database and not merely in Go. Two hosts may
// run different Babel versions against one catalog, so a fourth value must cost
// a migration and a review rather than arriving because one writer had a typo.
func TestUnknownTitleProvenanceIsRefusedByTheDatabase(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO sessions
		(session_uid, host_id, harness, first_snapshot_id, latest_snapshot_id,
		 primary_size, artifact_count, blob_count, unresolved_blob_count,
		 title, title_provenance)
		VALUES ('uid-bogus', 'h1', 'codex', 's1', 's1', 1, 0, 0, 0, 'a title', 'guessed')`)
	if err == nil {
		t.Fatal("the database accepted a title_provenance outside the vocabulary")
	}
}

// A cross-host reader must be able to tell "nobody said" from "the harness
// recorded it". Every one of the 838 live rows holds NULL here until its owning
// host republishes, and a reader that defaulted NULL to "recorded" would
// attribute Babel's own derivation to the harness.
func TestAbsentTitleProvenanceIsNotRecorded(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{
			// What a binary predating migrations/0005 writes: a title, no
			// provenance. It must not read back as a harness record.
			SessionUID: "uid-legacy",
			Harness:    "omp",
			Title:      new("published before provenance existed"),
		}})
	rows := hostSessions(t, db, "h1")
	if len(rows) != 1 {
		t.Fatalf("HostSessions reported %d rows, want 1", len(rows))
	}
	if got := rows[0].TitleProvenance; got != "" {
		t.Errorf("title_provenance = %q, want empty: absence is absence", got)
	}
	if rows[0].Title == "" {
		t.Error("the title itself was lost")
	}
}

// migrations/0006: what a session cost has to survive the trip to another
// machine, and the distinction that decides whether it is honest is zero
// against NULL. A session that ran for free and a session nobody measured
// render identically if absence collapses to zero, and the reader meeting
// them is the one furthest from the transcript: it has this row and no way to
// recompute anything.
func TestPublishedSessionCarriesUsage(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{
			{
				SessionUID:  "uid-priced",
				Harness:     "omp",
				CostUSD:     new(12.5),
				TotalTokens: new(int64(1234567)),
				Turns:       new(int64(41)),
				ToolErrors:  new(int64(3)),
			},
			{
				// Measured and free: a session whose transcript recorded
				// usage blocks priced at nothing. Every value is zero and
				// every value is present, which is the case NOT NULL columns
				// would have made indistinguishable from the row below.
				SessionUID:  "uid-free",
				Harness:     "omp",
				CostUSD:     new(0.0),
				TotalTokens: new(int64(0)),
				Turns:       new(int64(0)),
				ToolErrors:  new(int64(0)),
			},
			{
				// What a Codex session, or a binary predating this
				// migration, publishes: nothing measured it.
				SessionUID: "uid-unmeasured",
				Harness:    "codex",
			},
		})

	byUID := map[string]sharedcatalog.CatalogSessionRow{}
	for _, r := range hostSessions(t, db, "h1") {
		byUID[r.SessionUID] = r
	}
	if len(byUID) != 3 {
		t.Fatalf("HostSessions reported %d rows, want 3", len(byUID))
	}

	priced := byUID["uid-priced"]
	if priced.CostUSD == nil || *priced.CostUSD != 12.5 {
		t.Errorf("cost_usd = %v, want the published 12.5", priced.CostUSD)
	}
	if priced.TotalTokens == nil || *priced.TotalTokens != 1234567 {
		t.Errorf("total_tokens = %v, want 1234567", priced.TotalTokens)
	}
	if priced.Turns == nil || *priced.Turns != 41 {
		t.Errorf("turns = %v, want 41", priced.Turns)
	}
	if priced.ToolErrors == nil || *priced.ToolErrors != 3 {
		t.Errorf("tool_errors = %v, want 3", priced.ToolErrors)
	}

	free := byUID["uid-free"]
	if free.CostUSD == nil {
		t.Error("a measured free session read back as unmeasured: zero is a measurement")
	} else if *free.CostUSD != 0 {
		t.Errorf("cost_usd = %v, want 0", *free.CostUSD)
	}
	if free.Turns == nil || *free.Turns != 0 {
		t.Errorf("turns = %v, want a present 0", free.Turns)
	}

	unmeasured := byUID["uid-unmeasured"]
	if unmeasured.CostUSD != nil {
		t.Errorf("cost_usd = %v for a session nothing measured; NULL must not become a price",
			*unmeasured.CostUSD)
	}
	if unmeasured.TotalTokens != nil || unmeasured.Turns != nil || unmeasured.ToolErrors != nil {
		t.Errorf("an unmeasured session read back with counts: %+v", unmeasured)
	}
}

// The usage columns are measures of consumption, so a negative one is not an
// unmeasured session - it is a broken writer, and two hosts may run different
// Babel versions against one catalog. The vocabulary is closed in the database
// for the same reason title_provenance's is: a bad value should cost a
// migration and a review rather than arrive because one writer had a sign
// error and then be browsed as a refund.
func TestNegativeUsageIsRefusedByTheDatabase(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, c := range []struct {
		column string
		value  string
	}{
		{"cost_usd", "-0.01"},
		{"total_tokens", "-1"},
		{"turns", "-1"},
		{"tool_errors", "-1"},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO sessions
			(session_uid, host_id, harness, first_snapshot_id, latest_snapshot_id,
			 primary_size, artifact_count, blob_count, unresolved_blob_count, `+c.column+`)
			VALUES ('uid-`+c.column+`', 'h1', 'omp', 's1', 's1', 1, 0, 0, 0, `+c.value+`)`)
		if err == nil {
			t.Errorf("the database accepted %s = %s", c.column, c.value)
		}
	}
}

// The distinction a cross-host reader actually meets. Rows published before
// 0004, or by a binary that predates it, carry no grade at all; a reader that
// collapses that into `false` tells an operator a session cannot be continued
// when nobody has looked.
func TestCrossHostReadDistinguishesUnknownGradeFromFalse(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	for _, host := range []string{"h-graded", "h-silent"} {
		if err := sharedcatalog.Register(ctx, db, "d1", host, "inst-"+host,
			sharedcatalog.HostIdentity{}); err != nil {
			t.Fatalf("Register %s: %v", host, err)
		}
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// One host grades its sessions: a true and an explicit false.
	publishTo(t, db, "h-graded", "inst-h-graded", "s-g1", base, 1,
		sharedcatalog.CommitCommitted, []sharedcatalog.SessionRow{
			{SessionUID: "uid-yes", Harness: "omp", ContinuationGrade: new(true)},
			{SessionUID: "uid-no", Harness: "claude", ContinuationGrade: new(false)},
		})
	// The other supplies no grade, which is exactly what a writer predating
	// 0004 leaves behind.
	publishTo(t, db, "h-silent", "inst-h-silent", "s-s1", base.Add(time.Minute), 1,
		sharedcatalog.CommitCommitted, []sharedcatalog.SessionRow{
			{SessionUID: "uid-unknown", Harness: "codex"},
		})

	byUID := map[string]sharedcatalog.CatalogSessionRow{}
	for _, r := range hostSessions(t, db, "") {
		byUID[r.SessionUID] = r
	}
	if len(byUID) != 3 {
		t.Fatalf("the fleet read returned %d sessions, want 3 across both hosts", len(byUID))
	}

	yes := byUID["uid-yes"]
	if yes.ContinuationGrade == nil || !*yes.ContinuationGrade {
		t.Errorf("graded-true session read back as %v", yes.ContinuationGrade)
	}
	no := byUID["uid-no"]
	if no.ContinuationGrade == nil {
		t.Fatal("an explicit false grade read back as unknown: the two are different claims")
	}
	if *no.ContinuationGrade {
		t.Errorf("graded-false session read back as true")
	}
	unknown := byUID["uid-unknown"]
	if unknown.ContinuationGrade != nil {
		t.Errorf("an ungraded session read back as %v; unknown must stay nil, "+
			"or every unpublished grade becomes a verdict", *unknown.ContinuationGrade)
	}
	// Absent title and workspace are empty rather than a scan failure, and a
	// caller can tell them from content because content is never empty here.
	if unknown.Title != "" || unknown.Workspace != "" {
		t.Errorf("an ungraded row invented title %q / workspace %q",
			unknown.Title, unknown.Workspace)
	}
}

// A host is the authority on its own sessions across pushes: a workspace that
// moved, or a session that stopped being continuable, must be able to say so.
// Coalescing would make the first value ever published permanent.
func TestRepublishReplacesSessionMetadata(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{
			SessionUID: "uid-1", Harness: "omp",
			Title: new("first pass"), Workspace: new("/tmp/old"),
			ContinuationGrade: new(true),
		}})
	publishTo(t, db, "h1", "inst-a", "s2", base.Add(time.Minute), 2,
		sharedcatalog.CommitCommitted, []sharedcatalog.SessionRow{{
			SessionUID: "uid-1", Harness: "omp",
			Title: new("second pass"), Workspace: new("/tmp/new"),
			ContinuationGrade: new(false),
		}})

	rows := hostSessions(t, db, "h1")
	if len(rows) != 1 {
		t.Fatalf("a republished session became %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Title != "second pass" || got.Workspace != "/tmp/new" {
		t.Errorf("republished row kept title %q / workspace %q; the newest push is the authority",
			got.Title, got.Workspace)
	}
	if got.ContinuationGrade == nil || *got.ContinuationGrade {
		t.Errorf("grade = %v, want a present false: a session that stopped being "+
			"continuable must be able to say so", got.ContinuationGrade)
	}
}

// The same authority rule read the other way: a push that supplies no title
// clears one an earlier push published. That is deliberate rather than
// incidental, and it is worth pinning because the alternative looks harmless.
// Coalescing would make the first title ever published permanent even after the
// adapter stopped reporting it, and a stale title on a session is a worse
// answer than no title. A failed describe cannot reach here: such a session is
// pruned from the local cache and is not published at all.
//
// A writer that predates migrations/0004 is a different case and is not this
// one. Its INSERT does not name these columns at all, so PostgreSQL leaves them
// untouched rather than writing NULL - which is what lets the operator's older
// hourly timer publish across the migration without erasing metadata a newer
// binary wrote.
func TestPublishWithoutMetadataClearsIt(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{
			SessionUID: "uid-1", Harness: "omp",
			Title: new("had a title"), ContinuationGrade: new(true),
		}})
	publishTo(t, db, "h1", "inst-a", "s2", base.Add(time.Minute), 2,
		sharedcatalog.CommitCommitted, []sharedcatalog.SessionRow{{
			SessionUID: "uid-1", Harness: "omp",
		}})

	rows := hostSessions(t, db, "h1")
	if len(rows) != 1 {
		t.Fatalf("republished session became %d rows, want 1", len(rows))
	}
	if rows[0].Title != "" {
		t.Errorf("title = %q; a push that reports none is the authority", rows[0].Title)
	}
	if rows[0].ContinuationGrade != nil {
		t.Errorf("grade = %v; a push that reports none leaves it unknown",
			*rows[0].ContinuationGrade)
	}
}

// Decision 8: "host display names are catalog rows where the newest value
// wins." This is what that means here - one mutable column, no history, and the
// row always holds the latest assertion.
func TestHostDisplayNameNewestWins(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	first := sharedcatalog.HostIdentity{DisplayName: "workstation", OS: "linux", Arch: "amd64"}
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a", first); err != nil {
		t.Fatalf("Register: %v", err)
	}
	name, os, arch, firstSeen, updated := hostIdentityRow(t, db, "h1")
	if name != "workstation" || os != "linux" || arch != "amd64" {
		t.Fatalf("first assertion stored %q/%q/%q", name, os, arch)
	}
	if !updated.Valid {
		t.Fatal("identity_updated_at is NULL after an identity was asserted")
	}
	firstUpdate := updated.Time

	// The newest value wins, and the timestamp says when it landed.
	renamed := sharedcatalog.HostIdentity{DisplayName: "alex's desk", OS: "linux", Arch: "arm64"}
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a", renamed); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	name, os, arch, seenAgain, updated := hostIdentityRow(t, db, "h1")
	if name != "alex's desk" || arch != "arm64" {
		t.Errorf("newest assertion did not win: name = %q, arch = %q", name, arch)
	}
	if !updated.Time.After(firstUpdate) && !updated.Time.Equal(firstUpdate) {
		t.Errorf("identity_updated_at went backwards: %s then %s", firstUpdate, updated.Time)
	}
	// No history is retained, and that is the deliberate reading of decision 8:
	// only one row exists per host, holding only the current name.
	if n := hostRowCount(t, db, "h1"); n != 1 {
		t.Errorf("host h1 has %d rows; newest-wins is an update, not an append", n)
	}

	// First-seen is not identity: it survives every rename.
	if !seenAgain.Equal(firstSeen) {
		t.Errorf("first-seen moved from %s to %s across a rename", firstSeen, seenAgain)
	}

	// An empty assertion is silence, not an erasure. A binary or an operator
	// with no name to supply must not blank one this host already published.
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("register with no identity: %v", err)
	}
	name, os, arch, _, _ = hostIdentityRow(t, db, "h1")
	if name != "alex's desk" || os != "linux" || arch != "arm64" {
		t.Errorf("an empty assertion overwrote identity: %q/%q/%q", name, os, arch)
	}
}

// A host that has never asserted identity is the live state of the operator's
// archive right now: 838 sessions from a host registered before 0004 existed.
// It must read as unknown rather than as an empty name with a fabricated time.
func TestHostWithNoAssertedIdentityReadsAsUnknown(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	name, os, arch, firstSeen, updated := hostIdentityRow(t, db, "h1")
	if name != "" || os != "" || arch != "" {
		t.Errorf("an unasserted identity invented %q/%q/%q", name, os, arch)
	}
	if updated.Valid {
		t.Errorf("identity_updated_at is %s though nothing was asserted", updated.Time)
	}
	if firstSeen.IsZero() {
		t.Error("first-seen is unset; hosts.created_at is what carries it")
	}

	// And the browse surface reports the same absence rather than failing.
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted, nil)
	rows := hostCatalog(t, db)
	if len(rows) != 1 {
		t.Fatalf("HostCatalog reported %d rows, want 1", len(rows))
	}
	if rows[0].DisplayName != "" {
		t.Errorf("display name = %q, want empty for an unasserted identity", rows[0].DisplayName)
	}
	if !rows[0].IdentityUpdatedAt.IsZero() {
		t.Errorf("identity-updated = %s, want the zero time for never", rows[0].IdentityUpdatedAt)
	}
	if rows[0].FirstSeenAt.IsZero() {
		t.Error("HostCatalog reported no first-seen time")
	}
}

// The browse surface carries the name, which is what stops a fleet listing being
// a column of host ids nobody chose to read.
func TestHostCatalogCarriesIdentity(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{DisplayName: "the workstation", OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{SessionUID: "uid-1", Harness: "omp"}})

	rows := hostCatalog(t, db)
	if len(rows) != 1 {
		t.Fatalf("HostCatalog reported %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.DisplayName != "the workstation" || got.OS != "linux" || got.Arch != "amd64" {
		t.Errorf("identity did not reach the browse surface: %q/%q/%q",
			got.DisplayName, got.OS, got.Arch)
	}
	if got.IdentityUpdatedAt.IsZero() {
		t.Error("identity-updated is zero though identity was asserted")
	}
}

// A rebuild reconstructs snapshot rows from the repository listing and cannot
// know another machine's facts, so it must leave identity alone. Erasing it
// would make the repair path a silent downgrade of the fleet view.
func TestRebuildPreservesHostIdentity(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()
	if err := sharedcatalog.Register(ctx, db, "d1", "h1", "inst-a",
		sharedcatalog.HostIdentity{DisplayName: "named host", OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	publishTo(t, db, "h1", "inst-a", "s1", base, 1, sharedcatalog.CommitCommitted,
		[]sharedcatalog.SessionRow{{
			SessionUID: "uid-1", Harness: "omp",
			Title: new("before the rebuild"), Workspace: new("/tmp/w"),
			ContinuationGrade: new(true),
		}})
	_, _, _, firstSeen, _ := hostIdentityRow(t, db, "h1")

	if _, err := sharedcatalog.Rebuild(ctx, db, "d1", "h1", []sharedcatalog.RepoSnapshot{
		{SnapshotID: "s1", Host: "h1", Time: base},
	}); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	name, os, arch, seenAgain, updated := hostIdentityRow(t, db, "h1")
	if name != "named host" || os != "linux" || arch != "amd64" {
		t.Errorf("rebuild erased host identity: %q/%q/%q", name, os, arch)
	}
	if !updated.Valid {
		t.Error("rebuild cleared identity_updated_at")
	}
	if !seenAgain.Equal(firstSeen) {
		t.Errorf("rebuild moved first-seen from %s to %s", firstSeen, seenAgain)
	}

	// Session metadata is the opposite case, and the command's own help says so:
	// a rebuild deletes session rows and cannot reconstruct them, so it is not
	// a backfill for title, workspace or grade.
	if rows := hostSessions(t, db, "h1"); len(rows) != 0 {
		t.Errorf("rebuild left %d session rows; it discards them by design", len(rows))
	}
}

// hostIdentityRow reads the host row's identity columns directly. The browse
// surface only reports hosts that have published, and several cases above are
// about a host that has not.
func hostIdentityRow(t *testing.T, db *sql.DB, hostID string) (
	name, os, arch string, firstSeen time.Time, updated sql.NullTime) {
	t.Helper()
	var n, o, a sql.NullString
	if err := db.QueryRow(`
		SELECT display_name, os, arch, created_at, identity_updated_at
		  FROM babel.hosts WHERE host_id = $1`, hostID).
		Scan(&n, &o, &a, &firstSeen, &updated); err != nil {
		t.Fatalf("read host %s: %v", hostID, err)
	}
	return n.String, o.String, a.String, firstSeen, updated
}

func hostRowCount(t *testing.T, db *sql.DB, hostID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM babel.hosts WHERE host_id = $1`, hostID).
		Scan(&n); err != nil {
		t.Fatalf("count host rows: %v", err)
	}
	return n
}
