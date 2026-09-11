package evaluation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// These scenarios cover the projection's two promises: a page is a window on one
// globally ordered set, and a snapshot is the consistency contract that survives
// publication landing mid-read. Both fail silently when they fail — a
// locally-sorted page looks like a page — so they are asserted directly.

func newTestProjection(t *testing.T) *projection {
	t.Helper()
	proj, err := openProjection(t.TempDir())
	if err != nil {
		t.Fatalf("open projection: %v", err)
	}
	t.Cleanup(func() { proj.Close() })
	return proj
}

// testSnapshot builds a snapshot of n hypotheses whose scores descend with
// their index, ranked in every sort.
func testSnapshot(id string, at time.Time, n int, lane string) snapshotInput {
	items := make([]projected, 0, n)
	for i := range n {
		subject := Subject{Kind: SubjectKindHypothesis, ID: "h" + strconv.Itoa(i)}
		items = append(items, projected{
			Artifact: Artifact{
				Subject:        subject,
				RootID:         "root-" + subject.ID,
				HeadID:         subject.ID,
				CreatedAt:      at.Add(-time.Duration(i) * time.Hour),
				Title:          "candidate " + subject.ID,
				Body:           []byte(`{"statement":"a claim"}`),
				ContextVersion: "ctx-a",
			},
			Reception: Reception{Support: n - i, Reviews: n - i},
			Roles: []RoleCoverage{{
				Role: RoleReception, State: CoverageUnreviewed,
			}},
			Required: map[string]bool{RoleReception: true},
			Coverage: CoverageUnreviewed,
			Lane:     lane,
			Score:    float64(n - i),
		})
	}
	ranks := make(map[string][]int, len(Sorts()))
	for _, sortName := range Sorts() {
		order, _ := rankAll(items, sortName)
		ranks[sortName] = order
	}
	return snapshotInput{
		ID:            id,
		CreatedAt:     at,
		InputDigest:   "in-" + id,
		PolicyVersion: PolicyVersion,
		Items:         items,
		Ranks:         ranks,
	}
}

// Pages are slices of one total order, and a pinned snapshot keeps serving that
// order while newer ones are written. A page that re-sorted its own rows would
// show one record twice across two pages and omit another.
func TestPagesAreWindowsOnOnePinnedOrder(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC()

	first := testSnapshot("snap-1", now, 6, LaneOpen)
	if err := proj.write(ctx, first); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	page, err := proj.page(ctx, Query{Sort: SortRecommended, Limit: 2})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if page.Total != 6 {
		t.Fatalf("total must be the whole eligible set, got %d", page.Total)
	}
	if len(page.Items) != 2 {
		t.Fatalf("limit must bound the page, got %d items", len(page.Items))
	}
	if page.Snapshot != "snap-1" {
		t.Fatalf("the page must name the snapshot it served, got %q", page.Snapshot)
	}

	// A second snapshot lands with a different membership while the caller
	// is still paging the first.
	second := testSnapshot("snap-2", now.Add(time.Minute), 3, LaneOpen)
	if err := proj.write(ctx, second); err != nil {
		t.Fatalf("write second snapshot: %v", err)
	}

	pinned, err := proj.page(ctx, Query{
		Sort: SortRecommended, Limit: 2, Offset: 2, Snapshot: "snap-1",
	})
	if err != nil {
		t.Fatalf("pinned page: %v", err)
	}
	if pinned.Snapshot != "snap-1" {
		t.Fatalf("a pinned snapshot must keep serving, got %q", pinned.Snapshot)
	}
	if pinned.Total != 6 {
		t.Fatalf("the pinned order's total must not change under it, got %d", pinned.Total)
	}
	if pinned.Stale {
		t.Fatal("a live pinned snapshot is not a substitution")
	}
	seen := map[string]bool{}
	for _, item := range append(page.Items, pinned.Items...) {
		if seen[item.Artifact.Subject.ID] {
			t.Fatalf("%s appeared on two pages of one order", item.Artifact.Subject.ID)
		}
		seen[item.Artifact.Subject.ID] = true
	}

	// Unpinned, the caller gets the current snapshot.
	current, err := proj.page(ctx, Query{Sort: SortRecommended})
	if err != nil {
		t.Fatalf("current page: %v", err)
	}
	if current.Snapshot != "snap-2" || current.Total != 3 {
		t.Fatalf("an unpinned read must serve the current snapshot, got %q total %d",
			current.Snapshot, current.Total)
	}
}

// A pinned snapshot that has aged out is substituted with a stated reason
// rather than refused. Refusing would leave a paging client with no way
// forward; substituting silently would hide that the order changed.
func TestAgedOutSnapshotIsSubstitutedHonestly(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC()

	for i := range snapshotRetention + 2 {
		snapshot := testSnapshot("snap-"+strconv.Itoa(i), now.Add(time.Duration(i)*time.Minute),
			3, LaneOpen)
		if err := proj.write(ctx, snapshot); err != nil {
			t.Fatalf("write snapshot %d: %v", i, err)
		}
	}
	if _, ok, err := proj.snapshot(ctx, "snap-0"); err != nil {
		t.Fatalf("snapshot lookup: %v", err)
	} else if ok {
		t.Fatal("snapshots past retention must be pruned")
	}

	page, err := proj.page(ctx, Query{Sort: SortRecommended, Snapshot: "snap-0"})
	if err != nil {
		t.Fatalf("substituted page: %v", err)
	}
	if !page.Stale {
		t.Fatal("a substituted page must be marked stale")
	}
	if page.Unavailable == "" {
		t.Fatal("a substitution must state that the order may have changed")
	}
	if page.Snapshot == "snap-0" {
		t.Fatal("the page must name the snapshot actually served")
	}
	if len(page.Items) == 0 {
		t.Fatal("a substitution must still answer with the current view")
	}
}

// An unbuilt projection is an honest refusal, not an empty result. An empty
// page would tell an operator the deployment has produced nothing.
func TestUnbuiltProjectionRefusesRatherThanReportingEmpty(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)

	_, err := proj.page(ctx, Query{Sort: SortRecommended})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unbuilt projection must refuse with ErrUnavailable, got %v", err)
	}
}

// A discarded projection file rebuilds rather than failing, which is what makes
// it a cache: the records are elsewhere and re-deriving costs one refresh.
func TestDiscardedProjectionRebuilds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	proj, err := openProjection(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := proj.write(ctx, testSnapshot("snap-1", time.Now().UTC(), 2, LaneOpen)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := proj.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A file this build cannot read must not stop the service from opening.
	if err := os.WriteFile(filepath.Join(dir, ProjectionFileName),
		[]byte("not a database"), 0o600); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	rebuilt, err := openProjection(dir)
	if err != nil {
		t.Fatalf("a corrupt projection must be discarded and rebuilt, got %v", err)
	}
	defer rebuilt.Close()

	if _, ok, err := rebuilt.current(ctx); err != nil {
		t.Fatalf("current: %v", err)
	} else if ok {
		t.Fatal("a rebuilt projection holds no snapshot until one is written")
	}
	if err := rebuilt.write(ctx, testSnapshot("snap-2", time.Now().UTC(), 2, LaneOpen)); err != nil {
		t.Fatalf("write after rebuild: %v", err)
	}
	page, err := rebuilt.page(ctx, Query{Sort: SortRecommended})
	if err != nil {
		t.Fatalf("page after rebuild: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("the rebuilt projection must answer from the new snapshot, got %d", page.Total)
	}
}

// Filters narrow the eligible set, and the total must follow. Reporting the
// snapshot's size beside a filtered page would make a one-item result claim to
// be the first of forty.
func TestFilteredTotalsDescribeTheFilteredSet(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC()

	snapshot := testSnapshot("snap-1", now, 4, LaneOpen)
	snapshot.Items[0].Lane = LaneAccepted
	snapshot.Items[1].Coverage = CoverageReviewed
	snapshot.Items[1].Roles = []RoleCoverage{{
		Role: RoleReception, State: CoverageReviewed, Reviews: 2,
	}}
	if err := proj.write(ctx, snapshot); err != nil {
		t.Fatalf("write: %v", err)
	}

	accepted, err := proj.page(ctx, Query{Lane: LaneAccepted})
	if err != nil {
		t.Fatalf("lane page: %v", err)
	}
	if accepted.Total != 1 || len(accepted.Items) != 1 {
		t.Fatalf("a lane filter must bound the total, got %d/%d",
			accepted.Total, len(accepted.Items))
	}

	unreviewed, err := proj.page(ctx, Query{Coverage: CoverageUnreviewed})
	if err != nil {
		t.Fatalf("coverage page: %v", err)
	}
	if unreviewed.Total != 3 {
		t.Fatalf("a coverage filter must bound the total, got %d", unreviewed.Total)
	}

	if _, err := proj.page(ctx, Query{Lane: "accpeted"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown lane must be refused rather than ignored, got %v", err)
	}
	if _, err := proj.page(ctx, Query{Coverage: "needs-review"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a coverage value outside the vocabulary must be refused, got %v", err)
	}
	if _, err := proj.page(ctx, Query{Role: "vibes"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown role must be refused, got %v", err)
	}
}

// Coverage counts items by obligation and role rows by role. One aggregation
// serving both would either hide unactivated gaps or report every artifact as
// under-reviewed for roles nobody asked for.
func TestCoverageSeparatesItemObligationsFromRoleGaps(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC()

	snapshot := testSnapshot("snap-1", now, 2, LaneOpen)
	snapshot.Items[0].Coverage = CoverageReviewed
	snapshot.Items[0].Roles = []RoleCoverage{
		{Role: RoleReception, State: CoverageReviewed, Reviews: 2},
		{Role: RoleEvidence, State: CoverageUnreviewed},
	}
	snapshot.Items[0].Required = map[string]bool{RoleReception: true}
	snapshot.Items[1].Roles = []RoleCoverage{
		{Role: RoleReception, State: CoverageUnreviewed, Overdue: true},
	}
	snapshot.Active = 1
	if err := proj.write(ctx, snapshot); err != nil {
		t.Fatalf("write: %v", err)
	}
	meta, ok, err := proj.current(ctx)
	if err != nil || !ok {
		t.Fatalf("current snapshot: %v ok=%v", err, ok)
	}

	coverage, err := proj.coverage(ctx, meta)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.Reviewed != 1 || coverage.Unreviewed != 1 {
		t.Fatalf("items must be counted by their obligation state: %+v", coverage.CoverageCounts)
	}
	if coverage.Overdue != 1 {
		t.Fatalf("an overdue activated obligation must be counted, got %d", coverage.Overdue)
	}
	if coverage.Active != 1 {
		t.Fatalf("claimed unsettled work must be reported as active, got %d", coverage.Active)
	}
	evidence, ok := coverage.ByRole[RoleEvidence]
	if !ok {
		t.Fatal("a role nothing activated must still appear as a visible gap")
	}
	if evidence.Unreviewed != 1 {
		t.Fatalf("the per-role view must show the unmet evidence row: %+v", evidence)
	}
	if coverage.Reason == "" {
		t.Fatal("a projection with no recorded coverage check must say so")
	}
}

// A role-scoped read reports that role's coverage, and a role a kind cannot
// receive reads as not-applicable with a named reason rather than as reviewed.
func TestRoleScopedReadReportsThatRole(t *testing.T) {
	item := projected{
		Artifact: Artifact{Subject: Subject{Kind: SubjectKindObservation, ID: "o1"}},
		Coverage: CoverageReviewed,
		Roles: []RoleCoverage{
			{Role: RoleReception, State: CoverageReviewed, Reviews: 2},
			{Role: RoleEvidence, State: CoverageUnreviewed},
		},
		Required: map[string]bool{RoleReception: true},
	}
	combined := newItem(item, "")
	if combined.Coverage != CoverageReviewed {
		t.Fatalf("the combined view reports the obligation state, got %q", combined.Coverage)
	}
	scoped := newItem(item, RoleEvidence)
	if scoped.Coverage != CoverageUnreviewed {
		t.Fatalf("a role-scoped view reports that role, got %q", scoped.Coverage)
	}
	if len(scoped.ReviewCoverage) != 2 {
		t.Fatalf("every applicable role must remain visible, got %v", scoped.ReviewCoverage)
	}
	missing := newItem(item, RoleOutcome)
	if missing.Coverage != CoverageNotApplicable {
		t.Fatalf("a role this kind cannot receive must read not_applicable, got %q",
			missing.Coverage)
	}
	if missing.CoverageReason == "" {
		t.Fatal("a not_applicable state must carry the named policy reason")
	}
}

// Replacing one item refreshes its content and leaves the pinned order alone. A
// vote landing mid-read must not reshuffle the page under the operator, and the
// row they are looking at must not be stale either.
func TestReplacingAnItemKeepsThePinnedOrder(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC()

	snapshot := testSnapshot("snap-1", now, 3, LaneOpen)
	if err := proj.write(ctx, snapshot); err != nil {
		t.Fatalf("write: %v", err)
	}
	before, err := proj.page(ctx, Query{Sort: SortRecommended})
	if err != nil {
		t.Fatalf("page: %v", err)
	}

	updated := snapshot.Items[2]
	updated.Score = 100
	updated.Lane = LaneAccepted
	updated.Coverage = CoverageReviewed
	updated.Roles = []RoleCoverage{{Role: RoleReception, State: CoverageReviewed, Reviews: 2}}
	if err := proj.replaceItem(ctx, "snap-1", updated, nil); err != nil {
		t.Fatalf("replace: %v", err)
	}

	after, err := proj.page(ctx, Query{Sort: SortRecommended})
	if err != nil {
		t.Fatalf("page after replace: %v", err)
	}
	for i := range before.Items {
		if before.Items[i].Artifact.Subject != after.Items[i].Artifact.Subject {
			t.Fatalf("position %d moved: %v became %v", i,
				before.Items[i].Artifact.Subject, after.Items[i].Artifact.Subject)
		}
	}
	if after.Items[2].Lane != LaneAccepted || after.Items[2].Coverage != CoverageReviewed {
		t.Fatalf("the replaced item's content must be current, got lane %q coverage %q",
			after.Items[2].Lane, after.Items[2].Coverage)
	}

	// An item that is not in the snapshot is not inserted: it arrived after
	// this order was captured, and adding it would change the membership of
	// a fixed order.
	fresh := snapshot.Items[0]
	fresh.Artifact.Subject = Subject{Kind: SubjectKindHypothesis, ID: "brand-new"}
	if err := proj.replaceItem(ctx, "snap-1", fresh, nil); err != nil {
		t.Fatalf("replace unknown: %v", err)
	}
	final, err := proj.page(ctx, Query{Sort: SortRecommended})
	if err != nil {
		t.Fatalf("page after unknown replace: %v", err)
	}
	if final.Total != before.Total {
		t.Fatalf("a pinned order's membership must not grow, got %d then %d",
			before.Total, final.Total)
	}
}

// A coverage check that finished is durable and visible even when no review
// happened. §E4 makes check completion and review completion separate facts, and
// a sweep that only recorded itself when it also reviewed something would make a
// budget-starved scheduler indistinguishable from a stopped one.
func TestCheckpointIsDurableIndependentOfReviewProgress(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	snapshot := testSnapshot("snap-1", now, 2, LaneOpen)
	if err := proj.write(ctx, snapshot); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := proj.rememberCheckpoint(ctx, now, snapshot.InputDigest, 0); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	at, digest, covered, ok, err := proj.lastCheckpoint(ctx)
	if err != nil || !ok {
		t.Fatalf("last checkpoint: %v ok=%v", err, ok)
	}
	if covered != 0 {
		t.Fatalf("a sweep that reviewed nothing still completed, got covered %d", covered)
	}
	if digest != snapshot.InputDigest || !at.Equal(now) {
		t.Fatalf("the checkpoint must record what it inspected and when: %q %v", digest, at)
	}

	meta, _, err := proj.current(ctx)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	coverage, err := proj.coverage(ctx, meta)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if coverage.LastCheck.IsZero() {
		t.Fatal("a completed sweep must be visible in coverage")
	}
	if coverage.Unreviewed != 2 {
		t.Fatalf("completing the inspection must not make anything reviewed, got %+v",
			coverage.CoverageCounts)
	}
}

// The recorded policy survives a refresh whose source read failed, and the
// newest one wins. Forgetting it would make an independent reader present a
// disabled default as the deployment's approved configuration.
func TestRecordedPolicyIsRememberedAcrossRefreshes(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC()

	older := DefaultPolicy()
	older.Version, older.Enabled = "eval-policy-1", true
	newer := older
	newer.Version, newer.DailyCost = "eval-policy-2", 9

	if err := proj.rememberPolicies(ctx, []policyRecord{
		{RecordID: "p1", CreatedAt: now.Add(-time.Hour), Policy: older},
		{RecordID: "p2", CreatedAt: now, Policy: newer},
	}); err != nil {
		t.Fatalf("remember policies: %v", err)
	}
	found, ok, err := proj.newestPolicy(ctx)
	if err != nil || !ok {
		t.Fatalf("newest policy: %v ok=%v", err, ok)
	}
	if found.Policy.Version != "eval-policy-2" || found.Policy.DailyCost != 9 {
		t.Fatalf("the newest recorded policy must win, got %+v", found.Policy)
	}

	// Writing a snapshot must not clear what the instance knows about the
	// deployment's configuration.
	if err := proj.write(ctx, testSnapshot("snap-1", now, 1, LaneOpen)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok, err := proj.newestPolicy(ctx); err != nil || !ok {
		t.Fatalf("a refresh must not forget the recorded policy: %v ok=%v", err, ok)
	}

	versions, err := proj.policyVersions(ctx)
	if err != nil {
		t.Fatalf("policy versions: %v", err)
	}
	if !versions["eval-policy-1"] || !versions["eval-policy-2"] {
		t.Fatalf("both recorded versions must be known as taken, got %v", versions)
	}
}

// Every draw is recorded, including the ones that produced nothing. "Why is
// nothing being reviewed" is asked exactly when there is nothing to look at.
func TestDrawsAreRecordedIncludingTheEmptyOnes(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := proj.rememberDraw(ctx, drawRecord{
		At: now, RunID: "run-1", Seed: 42, InputDigest: "in-1",
		PolicyVersion: "eval-policy-1", StopReason: "daily ceiling reached",
	}); err != nil {
		t.Fatalf("remember draw: %v", err)
	}
	if err := proj.rememberDraw(ctx, drawRecord{
		At: now.Add(time.Second), RunID: "run-1", Seed: 43, InputDigest: "in-1",
		PolicyVersion: "eval-policy-1", Lane: LaneCoverage, Role: RoleReception,
		Subject: Subject{Kind: SubjectKindHypothesis, ID: "h1"}, AssignmentID: "a1",
	}); err != nil {
		t.Fatalf("remember draw: %v", err)
	}

	draws, err := proj.draws(ctx, 10)
	if err != nil {
		t.Fatalf("draws: %v", err)
	}
	if len(draws) != 2 {
		t.Fatalf("both draws must be recorded, got %d", len(draws))
	}
	if draws[0].AssignmentID != "a1" || draws[0].Seed != 43 {
		t.Fatalf("the newest draw must come first with its seed: %+v", draws[0])
	}
	if draws[1].StopReason == "" {
		t.Fatal("a draw that produced nothing must record why it stopped")
	}
	if draws[1].Seed != 42 || draws[1].InputDigest != "in-1" || draws[1].PolicyVersion == "" {
		t.Fatalf("a recorded draw must carry seed, input digest and policy for replay: %+v",
			draws[1])
	}
}

// Assignments and attempts round-trip through the snapshot, which is what lets
// a non-producing instance account for what other hosts already claimed and
// spent without re-reading the catalog on every draw.
func TestSnapshotCarriesTheAssignmentAndAttemptJournal(t *testing.T) {
	ctx := context.Background()
	proj := newTestProjection(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	subject := Subject{Kind: SubjectKindHypothesis, ID: "h0"}
	snapshot := testSnapshot("snap-1", now, 1, LaneOpen)
	snapshot.Assignments = map[Subject][]Assignment{subject: {{
		ID: "a1", Subject: subject, Role: RoleReception, RunID: "run-remote",
		ReservedCost: 0.25, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}}}
	snapshot.Attempts = []Attempt{{
		AssignmentID: "a1", State: AttemptExposed, RecordedAt: now,
	}}
	if err := proj.write(ctx, snapshot); err != nil {
		t.Fatalf("write: %v", err)
	}

	assignments, err := proj.assignments(ctx, "snap-1")
	if err != nil {
		t.Fatalf("assignments: %v", err)
	}
	if len(assignments) != 1 || assignments[0].ID != "a1" ||
		assignments[0].ReservedCost != 0.25 {
		t.Fatalf("another host's claim must survive the projection: %+v", assignments)
	}
	attempts, err := proj.attempts(ctx, "snap-1")
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].State != AttemptExposed {
		t.Fatalf("the attempt journal must survive the projection: %+v", attempts)
	}

	detail, err := proj.detail(ctx, subject, "", "")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(detail.Assignments) != 1 {
		t.Fatalf("a subject's assignments must be reachable from its detail: %+v",
			detail.Assignments)
	}
}
