package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This file is the evaluation application service: the one surface the browser,
// the CLI and the worker all call.
//
// It exists so that there is exactly one implementation of every question this
// feature answers. SPEC §8.5 requires the browser and headless callers to use
// the same application services, and the alternative is what the baseline
// already showed: a coverage number computed one way for a page and another way
// for a command, disagreeing in the same window.
//
// Four boundaries are load-bearing here.
//
// Records are durable; projections are not. Every write goes to the store and
// every read comes from the rebuildable projection, so a page read costs an
// indexed query rather than a walk over the deployment's evaluation history
// (§E5), and losing the projection costs one refresh.
//
// Refresh is the only thing that scans. It is called by a periodic process and
// by the coverage sweep, never by a page request. Check is the sweep: it
// refreshes and durably records that the inspection completed, because §E4
// makes "coverage inspection completed" and "all eligible output reviewed" two
// separate facts and the first has to survive a cycle with no review budget.
//
// The effective policy is a deployment fact, not a local default. It is the
// newest operator-authored policy this instance has seen anywhere — local or
// published by another host — and when shared mode cannot be read and nothing
// has ever been seen, Policy refuses rather than presenting the disabled
// default as the deployment's configuration.
//
// Draw claims; Review exposes. Nothing else touches either. That separation is
// what lets a conductor gate a drawn review against §4.8's expenditure policy
// and hand it back without ever recording that a model saw the content.

// Query bounds one page of the operator's view.
type Query struct {
	// Kind narrows to one subject kind; empty means every covered kind.
	Kind string `json:"kind,omitempty"`
	// Lane narrows to one lifecycle lane.
	Lane string `json:"lane,omitempty"`
	// Sort names the ordering; empty means recommended.
	Sort string `json:"sort,omitempty"`
	// Role scopes coverage to one review role. It also changes what
	// Item.Coverage reports: a role-scoped page shows that role's coverage
	// rather than the item's weakest outstanding obligation.
	Role string `json:"role,omitempty"`
	// Coverage narrows to one coverage state, or to `overdue`, which is a
	// filter rather than a state because an obligation can be reviewed and
	// overdue at once.
	Coverage string `json:"coverage,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	// Snapshot pins the ranked snapshot this page is read from, which is the
	// pagination consistency contract: pass back Page.Snapshot to keep
	// reading one order while publication continues.
	Snapshot string `json:"snapshot,omitempty"`
}

// Page is one page of the ranked, filtered projection.
type Page struct {
	Items []Item `json:"items,omitempty"`
	// Total is the size of the filtered eligible set, not of the snapshot: a
	// filtered view is a different set, and reporting the unfiltered total
	// would make a one-item page claim to be the first of forty.
	Total     int       `json:"total"`
	Snapshot  string    `json:"snapshot"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Stale reports that this page was not served from the snapshot the
	// caller asked for.
	Stale bool `json:"stale,omitempty"`
	// Unavailable is why this view is incomplete: a projection never built,
	// a pinned snapshot that aged out, or fleet records this instance could
	// not open. Items may still be present in the last two cases.
	Unavailable string   `json:"unavailable,omitempty"`
	Coverage    Coverage `json:"coverage"`
}

// Item is one artifact as the operator's view shows it.
type Item struct {
	Artifact  Artifact  `json:"artifact"`
	Reception Reception `json:"reception"`
	// ReviewCoverage is one entry per applicable role, in RolesForKind
	// order, so a bare reception vote can never be rendered as a satisfied
	// evidence check.
	ReviewCoverage []RoleCoverage `json:"review_coverage,omitempty"`
	// Coverage is the combined state over activated obligations, or the
	// named role's own state when the query scoped one.
	Coverage string `json:"coverage"`
	// CoverageReason is required whenever Coverage is unsupported, blocked
	// or not_applicable.
	CoverageReason string `json:"coverage_reason,omitempty"`
	Lane           string `json:"lane"`
	// Score is the recommended ordering's value. It is not a confidence, a
	// probability or evidence strength (§5.4), and it is exposed only so a
	// reader can see that the order has a stated basis.
	Score float64 `json:"score"`
	// Reasons explain why this is recommended now. Empty is legal.
	Reasons []string `json:"reasons,omitempty"`
	// Objections are the recorded arguments against, from assessments and
	// from scoped operator feedback.
	Objections []string `json:"objections,omitempty"`
	// WouldChange is what recorded reviewers said would change their
	// conclusion. Empty means nobody recorded one, which §E5 permits: a bare
	// vote carries no rationale and none is invented for it.
	WouldChange []string `json:"would_change,omitempty"`
	// Group keys the alternatives addressing one problem, empty when this
	// subject has none.
	Group string `json:"group,omitempty"`
	// Reconsider reports an open Reconsider item: a material change on
	// decided work. The prior decision still stands.
	Reconsider bool `json:"reconsider,omitempty"`
}

// Reception is the recorded reception tally for one revision.
//
// It is counts and nothing else. §4.12: reception is not evidence strength,
// independent corroboration, or a probability that the idea is correct — and
// Skips is here precisely so that a skipped review is visibly not a vote.
type Reception struct {
	Support int `json:"support"`
	Oppose  int `json:"oppose"`
	Unsure  int `json:"unsure"`
	Reviews int `json:"reviews"`
	Skips   int `json:"skips"`
}

// Coverage is the deployment's review coverage as of one snapshot.
//
// The embedded counters count items by their obligation-based coverage, which
// is what an operator acts on. ByRole counts role rows including roles nothing
// activated, which is the per-role gap view §8.5 requires to stay visible.
type Coverage struct {
	CoverageCounts

	// Active is how many assignments were claimed and unsettled at snapshot
	// time, which is what says whether authorized work is running now. An
	// expired claim is not active.
	Active int `json:"active"`
	// LastCheck is when a coverage sweep last completed. It advances even
	// when no review budget remained, because completing the inspection and
	// completing the reviews are different facts.
	LastCheck time.Time `json:"last_check,omitzero"`
	// NextDraw is when the next scheduled draw is due, zero when evaluation
	// is disabled or when no sweep has ever run. Zero means unknown and
	// never "now".
	NextDraw  time.Time `json:"next_draw,omitzero"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Reason is why this coverage is degraded or incomplete, empty when it
	// is neither.
	Reason string                    `json:"reason,omitempty"`
	ByRole map[string]CoverageCounts `json:"by_role,omitempty"`
}

// Detail is one subject with its history, assignments and alternatives.
type Detail struct {
	Item Item `json:"item"`
	// History is every record about this subject in order: assessments,
	// operator criteria and feedback, reconsider items and decisions, and
	// the assignment/attempt journal. It is append-only, so a rejected then
	// reconsidered record shows both.
	History     []Record     `json:"history,omitempty"`
	Assignments []Assignment `json:"assignments,omitempty"`
	// Alternatives are the other remedies addressing the same problem, each
	// with its own votes, coverage and decisions. Nothing is merged.
	Alternatives []Item `json:"alternatives,omitempty"`
}

// ReviewInput is what a worker is served for one assignment.
//
// There is no reception tally on it and there will not be one. §E3 requires the
// served content and the brokered reads to withhold existing tallies, ranks and
// earlier evaluations for an initial assessment, and the way to guarantee that
// is for the type not to carry them.
type ReviewInput struct {
	Assignment Assignment `json:"assignment"`
	Artifact   Artifact   `json:"artifact"`
	// Previous is nil for every role in BlindedRoles() and populated only
	// for challenge and comparison, where the disagreement is the question.
	// A reveal is attributed: the records carry their authors.
	Previous []Record `json:"previous,omitempty"`
	// Alternatives are populated only for the comparison role: the other
	// remedies for one problem, as artifacts rather than as ranked items, so
	// a comparison cannot see either side's tally.
	Alternatives []Artifact `json:"alternatives,omitempty"`
	// Corrects names the record this assignment was drawn to supersede,
	// empty when it supersedes nothing.
	//
	// It is populated only for a revealed role and only when the drawing run
	// already has a recorded assessment on this exact subject. Stating it
	// here rather than leaving a worker to recognise its own earlier output
	// keeps one implementation of "is this a correction": a worker that
	// guessed wrong would either replace a statement that should have been
	// preserved, or append a second active vote for one assignment.
	Corrects string `json:"corrects,omitempty"`
}

// Service is the evaluation application service.
type Service struct {
	dir   string
	store *Store
	src   Source
	proj  *projection

	// refresh serializes projection rebuilds. Two concurrent refreshes would
	// both be correct and one would be wasted, and the source's decode cache
	// is cheaper to share than to duplicate.
	refresh sync.Mutex
}

// NewService opens the projection beside the store and binds the source.
//
// dir is where the rebuildable projection lives. It is passed rather than
// derived from the store so a caller can keep the derived cache on a different
// filesystem from the durable records, which is the split §9 draws between the
// half that may be discarded and the half that may not.
func NewService(dir string, store *Store, source Source) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: evaluation service needs a store", ErrInvalid)
	}
	if source == nil {
		return nil, fmt.Errorf("%w: evaluation service needs a source", ErrInvalid)
	}
	proj, err := openProjection(dir)
	if err != nil {
		return nil, err
	}
	return &Service{dir: dir, store: store, src: source, proj: proj}, nil
}

// Close releases the projection. The store and source are borrowed and are the
// caller's to close.
func (s *Service) Close() error { return s.proj.Close() }

// ProjectionPath reports the projection file, which an operator needs in order
// to delete the cache.
func (s *Service) ProjectionPath() string { return s.proj.Path() }

// Refresh rebuilds the read projection from the durable records.
//
// It is the only operation that scans, and it is called by the periodic
// lifecycle and by Check — never by a page request. A page served during a
// refresh reads the previous snapshot, because the current-snapshot pointer
// moves in the same commit that writes the new rows.
//
// A degraded source does not fail the refresh. What this instance could see is
// projected and the degradation is stored with the snapshot, so a page says so
// however long afterwards it is read; the alternative would let one unopenable
// object on one remote host hide every artifact on this one.
func (s *Service) Refresh(ctx context.Context) error {
	_, err := s.rebuild(ctx, false)
	return err
}

// Check performs one coverage sweep: it refreshes and durably records that the
// inspection completed.
//
// The checkpoint is written whether or not any review happened and whether or
// not the source was complete, because §E4 requires check completion to be
// visible alongside overdue and unsupported work. A sweep that refused to
// record itself because the budget was exhausted would make an operator unable
// to tell a working scheduler from a stopped one.
//
// The checkpoint carries no actor. It attests that a sweep finished, and the
// publishing instance is attributed by the catalog row rather than by a claimed
// author.
func (s *Service) Check(ctx context.Context) (Coverage, error) {
	now := time.Now().UTC()
	meta, fresh, err := s.sweptWithin(ctx, now)
	if err != nil {
		return Coverage{}, err
	}
	if fresh {
		return s.proj.coverage(ctx, meta)
	}

	taken, err := s.proj.claimSweep(ctx, now, SweepLease)
	if err != nil {
		return Coverage{}, err
	}
	if !taken {
		if meta.ID != "" {
			// Another process is scanning. Serving the snapshot it is
			// about to replace is what a coverage read is for; paying
			// for a second identical scan is not.
			return s.proj.coverage(ctx, meta)
		}
		waited, ok, err := s.awaitSnapshot(ctx)
		if err != nil {
			return Coverage{}, err
		}
		if ok {
			return s.proj.coverage(ctx, waited)
		}
		// The holder never produced one, so this caller does the work
		// rather than reporting an inventory nobody built.
	}
	defer s.proj.releaseSweep(ctx)

	meta, err = s.rebuild(ctx, true)
	if err != nil {
		return Coverage{}, err
	}
	return s.proj.coverage(ctx, meta)
}

// sweptWithin reports the current snapshot and whether it is young enough to
// answer for a sweep under the deployment's configured cadence.
//
// The cadence has to be read from the projection rather than remembered in
// memory: every `babel evaluate` is its own process, so an in-memory "last
// swept" is always zero at startup and every process rebuilds. Measured
// 2026-09-12 on 4,692 records: 32 concurrent draws, three engine sessions, no
// reviews - the spend was in duplicated scans.
func (s *Service) sweptWithin(ctx context.Context, now time.Time) (snapshotMeta, bool, error) {
	meta, ok, err := s.proj.current(ctx)
	if err != nil || !ok {
		return snapshotMeta{}, false, err
	}
	policy, _, err := s.effectivePolicy(ctx)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return meta, false, err
	}
	if errors.Is(err, ErrUnavailable) {
		policy = DefaultPolicy()
	}
	cadence := time.Duration(policy.CadenceSeconds) * time.Second
	if cadence <= 0 {
		cadence = time.Duration(DefaultPolicy().CadenceSeconds) * time.Second
	}
	age := now.Sub(meta.CreatedAt)
	return meta, age >= 0 && age < cadence, nil
}

// awaitSnapshot waits briefly for the lease holder's first snapshot, for the
// one case where nothing exists to serve yet.
func (s *Service) awaitSnapshot(ctx context.Context) (snapshotMeta, bool, error) {
	deadline := time.Now().Add(SweepLease)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return snapshotMeta{}, false, ctx.Err()
		case <-time.After(time.Second):
		}
		meta, ok, err := s.proj.current(ctx)
		if err != nil {
			return snapshotMeta{}, false, err
		}
		if ok {
			return meta, true, nil
		}
	}
	return snapshotMeta{}, false, nil
}

// rebuild does the work of Refresh and Check.
func (s *Service) rebuild(ctx context.Context, checkpoint bool) (snapshotMeta, error) {
	s.refresh.Lock()
	defer s.refresh.Unlock()

	now := time.Now().UTC()
	artifacts, err := s.src.Artifacts(ctx)
	if err != nil {
		return snapshotMeta{}, err
	}
	local, err := s.store.Events(ctx)
	if err != nil {
		return snapshotMeta{}, fmt.Errorf("evaluation: read local records: %w", err)
	}
	remote, remoteErr := s.src.EvaluationRecords(ctx)
	records := mergeRecords(local, remote)

	assignments, err := s.collectAssignments(ctx, records)
	if err != nil {
		return snapshotMeta{}, err
	}
	attempts, err := s.collectAttempts(ctx, records)
	if err != nil {
		return snapshotMeta{}, err
	}

	if err := s.proj.rememberPolicies(ctx, policyRecords(records)); err != nil {
		return snapshotMeta{}, err
	}
	if latest, ok := latestCheckpoint(records); ok {
		if err := s.proj.rememberCheckpoint(ctx, latest.At, latest.InputDigest, latest.Covered); err != nil {
			return snapshotMeta{}, err
		}
	}

	policy, _, err := s.effectivePolicy(ctx)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return snapshotMeta{}, err
	}
	if errors.Is(err, ErrUnavailable) {
		// The projection still has to be built: a reader that cannot see
		// the deployment's policy must still be able to browse what
		// exists. The coverage arithmetic uses the safe default and the
		// snapshot records that the policy was unreadable, so nothing
		// presents the default as the deployment's configuration.
		policy = DefaultPolicy()
	}

	items, history := s.project(artifacts, records, assignments, attempts, policy, now)
	groupAlternatives(items)
	for i := range items {
		items[i].Score = score(&items[i], policy)
		items[i].Coverage, items[i].CoverageReason = deriveCoverage(&items[i])
		items[i].Lane = deriveLane(&items[i])
		sortSubjects(items[i].Alternatives)
	}

	ranks := make(map[string][]int, len(Sorts()))
	for _, sortName := range Sorts() {
		order, err := rankAll(items, sortName)
		if err != nil {
			return snapshotMeta{}, err
		}
		ranks[sortName] = order
	}

	inventory, inventoryErr := s.inventoryOf(ctx)
	digest := inputDigest(artifacts, records, policy)
	snapshot := snapshotInput{
		ID:            "eval-s-" + digestOf(digest, formatProjectionTime(now)),
		CreatedAt:     now,
		InputDigest:   digest,
		PolicyVersion: policy.Version,
		Active:        activeClaims(assignments, attempts, now),
		Items:         items,
		Ranks:         ranks,
		History:       history,
		Assignments:   assignmentsBySubject(assignments),
		Attempts:      attempts,
		Inventory:     inventory,
	}
	if status, ok := s.src.(StatusSource); ok {
		state := status.Status()
		snapshot.Unavailable = joinReasons(snapshot.Unavailable, state.Unavailable)
		if state.Unattributed > 0 {
			snapshot.Unavailable = joinReasons(snapshot.Unavailable, fmt.Sprintf(
				"%d record(s) have no registered producing host and are counted unattributed",
				state.Unattributed))
		}
	}
	if remoteErr != nil {
		snapshot.Unavailable = joinReasons(snapshot.Unavailable, remoteErr.Error())
	}
	if inventoryErr != nil {
		snapshot.Unavailable = joinReasons(snapshot.Unavailable, inventoryErr.Error())
	}
	if errors.Is(err, ErrUnavailable) {
		snapshot.Unavailable = joinReasons(snapshot.Unavailable, err.Error())
	}
	if err := s.proj.write(ctx, snapshot); err != nil {
		return snapshotMeta{}, err
	}
	if checkpoint {
		covered := 0
		for i := range items {
			if items[i].Coverage == CoverageReviewed {
				covered++
			}
		}
		if err := s.proj.rememberCheckpoint(ctx, now, digest, covered); err != nil {
			return snapshotMeta{}, err
		}
		if err := s.store.Checkpoint(ctx, now, digest, covered); err != nil {
			return snapshotMeta{}, fmt.Errorf("evaluation: record coverage checkpoint: %w", err)
		}
	}
	meta, ok, err := s.proj.snapshot(ctx, snapshot.ID)
	if err != nil {
		return snapshotMeta{}, err
	}
	if !ok {
		return snapshotMeta{}, fmt.Errorf("%w: snapshot %s vanished after write",
			ErrUnavailable, snapshot.ID)
	}
	return meta, nil
}

// project turns records into one projected item per artifact.
//
// The grouping is by exact subject, which is what binds a vote to the wording
// it was cast against: a record naming revision n never contributes to revision
// n+1's coverage, and n+1's reception starts empty with its predecessor's
// history reachable through the chain.
func (s *Service) project(artifacts []Artifact, records []Record, assignments []Assignment,
	attempts []Attempt, policy Policy, now time.Time) ([]projected, map[Subject][]Record) {
	roleOf := make(map[string]string, len(assignments))
	subjectOf := make(map[string]Subject, len(assignments))
	for _, assignment := range assignments {
		roleOf[assignment.ID] = assignment.Role
		subjectOf[assignment.ID] = assignment.Subject
	}
	attemptsByAssignment := make(map[string][]Attempt, len(attempts))
	for _, attempt := range attempts {
		attemptsByAssignment[attempt.AssignmentID] = append(
			attemptsByAssignment[attempt.AssignmentID], attempt)
	}

	type bucket struct {
		assessments []roledAssessment
		feedback    []feedbackNote
		outcomes    []outcomeState
		reconsider  []Record
		// decided maps a reconsider record id to the operator's explicit
		// decision value. It is a string rather than a boolean because
		// resolving a reconsider item and reopening the work are
		// different acts: retaining says the change does not move the
		// prior decision, and only `reopen` puts attention back on it.
		decided  map[string]string
		criteria []Record
		history  []Record
		skips    map[string]int
	}
	buckets := make(map[Subject]*bucket, len(artifacts))
	at := func(subject Subject) *bucket {
		found, ok := buckets[subject]
		if !ok {
			found = &bucket{decided: map[string]string{}, skips: map[string]int{}}
			buckets[subject] = found
		}
		return found
	}
	// A correction supersedes an earlier statement by naming its record id,
	// and the correction is a NEW assignment: a paid second pass reserves its
	// own budget before any compute, so it cannot reuse the first
	// assignment's id. The suppression therefore has to be global by record
	// id rather than per assignment — grouping by assignment would let one
	// run's corrected statement and its correction both count as effective
	// assessments, which is two active votes from one independent review.
	//
	// The superseded record is still history. §E1 preserves the earlier
	// statement and §4.7 never deletes, so it stays in the chain a reader
	// can open; what it stops being is an effective assessment.
	superseded := make(map[string]bool, len(records))
	for _, record := range records {
		if record.SupersedesID != "" {
			superseded[record.SupersedesID] = true
		}
	}
	for _, record := range records {
		if record.Subject.ID == "" {
			continue
		}
		holder := at(record.Subject)
		holder.history = append(holder.history, record)
		switch record.Kind {
		case KindAssessment:
			if record.Assessment == nil {
				continue
			}
			role := roleOf[record.AssignmentID]
			if role == "" {
				// An assessment whose assignment this instance
				// cannot see is still a record and still
				// history, but it cannot be credited to a role:
				// crediting it to reception by default is how a
				// bare vote would come to satisfy an evidence
				// check.
				continue
			}
			if superseded[record.ID] {
				continue
			}
			holder.assessments = append(holder.assessments,
				roledAssessment{Record: record, Role: role})
			if record.Assessment.Outcome != "" {
				holder.outcomes = append(holder.outcomes, outcomeState{
					RecordID:    record.ID,
					Outcome:     record.Assessment.Outcome,
					CriteriaID:  record.Assessment.CriteriaID,
					Environment: record.Assessment.Environment,
					Uncertainty: record.Assessment.Uncertainty,
					AsOf:        assessmentTime(record),
				})
			}
		case KindFeedback:
			holder.feedback = append(holder.feedback, feedbackNote{
				RecordID:  record.ID,
				Subject:   record.Subject,
				Operator:  record.ActorID,
				Reason:    record.Reason,
				At:        record.CreatedAt,
				RelatedID: record.RelatedID,
			})
		case KindReconsider:
			holder.reconsider = append(holder.reconsider, record)
		case KindReconsiderDecision:
			// The decision closes the reconsider item it names and
			// nothing else. It never reverses the original
			// disposition: §E6 keeps the prior decision and makes
			// reopening an explicit operator act, and this record is
			// that act or its refusal.
			//
			// The polarity is read from the closed Decision value and
			// never from the reason text. A free-text reason cannot
			// distinguish "reopen" from "I looked and I stand by it",
			// and guessing would either nag an operator who already
			// answered or silently discard a reopening they asked
			// for.
			if record.RelatedID != "" {
				holder.decided[record.RelatedID] = record.Decision
			}
		case KindCriteria:
			holder.criteria = append(holder.criteria, record)
		}
	}

	// Skips and failures are attributed from the merged attempt journal
	// rather than from the attempt *records*, because the journal is the
	// union of this instance's own receipts and the fleet's published ones:
	// counting only the records would miss a local attempt that has not
	// published yet, and a subject would look freshly drawable while a
	// worker had already given up on it three times.
	//
	// They are counted and never converted into a signal about the record.
	// §E4: repeated skips consume bounded attention and stay visible as
	// gaps rather than receiving negative votes.
	for id, journal := range attemptsByAssignment {
		subject, ok := subjectOf[id]
		if !ok {
			continue
		}
		role := roleOf[id]
		if role == "" {
			continue
		}
		holder := at(subject)
		for _, attempt := range journal {
			switch attempt.State {
			case AttemptSkipped, AttemptFailed:
				holder.skips[role]++
			}
		}
	}

	alternativeCounts := countAlternatives(artifacts)
	items := make([]projected, 0, len(artifacts))
	history := make(map[Subject][]Record, len(artifacts))
	for _, artifact := range artifacts {
		holder := at(artifact.Subject)
		// An operator's criteria act is the authority an outcome is
		// judged against; the proposal's own verification criteria are a
		// suggestion. §E6 forbids Babel rewriting its target and
		// verifying itself against the replacement, so the adopted
		// record's id travels with the artifact and stays empty when
		// nobody has adopted one — including for every historical record
		// written before criteria acts existed. An empty CriteriaID is
		// "no criteria authority", never "use the suggestion".
		if adopted, ok := adoptedCriteria(holder.criteria); ok {
			artifact.CriteriaID = adopted.ID
			if len(adopted.Criteria) > 0 {
				artifact.Criteria = adopted.Criteria
			}
		}
		// The reconsider state is resolved before coverage, because an
		// explicit reopening makes review attention due again and the
		// coverage derivation has to see it.
		var (
			reconsiderOpen bool
			reopened       bool
			reconsiderAt   time.Time
		)
		for _, record := range holder.reconsider {
			decision, resolved := holder.decided[record.ID]
			switch {
			case !resolved:
				reconsiderOpen = true
			case decision == ReconsiderReopen:
				reopened = true
			}
			if record.CreatedAt.After(reconsiderAt) {
				reconsiderAt = record.CreatedAt
			}
		}

		item := projected{
			Artifact:     artifact,
			Outcomes:     markContrary(holder.outcomes),
			Feedback:     holder.feedback,
			Names:        recordedNames(artifact),
			Required:     map[string]bool{},
			Assessments:  len(holder.assessments),
			Reconsider:   reconsiderOpen,
			ReconsiderAt: reconsiderAt,
			Reopened:     reopened,
		}
		// A descendant wording with no assessments of its own is the
		// material change §E1 and §E4 both care about: the candidate has
		// a review history and this exact wording has none, so no
		// endorsement may move to it and attention is restored.
		//
		// It is derived from the chain identity rather than from counting
		// the predecessors' records, because the predecessors are not in
		// this batch — the inventory carries head revisions — and their
		// records name subject ids whose chain this instance would have
		// to resolve one at a time. The chain identity is already on the
		// artifact and answers the question that matters: is this the
		// original wording, or one that replaced something.
		item.StaleRevision = artifact.RootID != "" &&
			artifact.RootID != artifact.Subject.ID &&
			len(holder.assessments) == 0

		coverage, required := deriveRoleCoverage(coverageInput{
			Artifact:         artifact,
			Assessments:      holder.assessments,
			Superseded:       item.StaleRevision,
			Reopened:         reopened,
			Skips:            holder.skips,
			AlternativeCount: alternativeCounts[artifact.Subject],
			Policy:           policy,
			Now:              now,
		})
		item.Roles, item.Required = coverage, required
		item.Reception = tally(holder.assessments, holder.skips)
		item.ContextChanged = contextMoved(coverageInput{
			Artifact:    artifact,
			Assessments: holder.assessments,
			Reopened:    reopened,
		})
		item.StrengthenedAt = strengthenedAt(holder.assessments, holder.criteria, holder.outcomes)
		item.LastReviewAt = newestReview(coverage)
		item.DueAt, item.Overdue = dueFrom(coverage, required, artifact, policy)

		assessed := make([]Record, 0, len(holder.assessments))
		for _, entry := range holder.assessments {
			assessed = append(assessed, entry.Record)
		}
		explainWouldChange(&item, assessed)
		if len(holder.criteria) > 0 {
			item.Reasons = append(item.Reasons, fmt.Sprintf(
				"%d operator-authored criteria record(s) apply; an outcome is judged against "+
					"the criteria version it names", len(holder.criteria)))
		}
		items = append(items, item)
		history[artifact.Subject] = holder.history
	}
	return items, history
}

// adoptedCriteria reports the operator's newest criteria act, if any.
//
// Newest by creation then record id, so two acts recorded in one instant
// resolve the same way on every instance. Determinism matters because this id
// is what an outcome assessment names as the thing it was judged against: two
// readers disagreeing about which criteria version is current would be two
// readers disagreeing about whether a verification is valid.
//
// Criteria resolved after acceptance stay identifiable as later decisions
// because the record carries its own timestamp and actor — nothing here
// backdates an adoption onto the acceptance that preceded it.
func adoptedCriteria(records []Record) (Record, bool) {
	var (
		newest Record
		found  bool
	)
	for _, record := range records {
		if record.Kind != KindCriteria {
			continue
		}
		if !found || record.CreatedAt.After(newest.CreatedAt) ||
			(record.CreatedAt.Equal(newest.CreatedAt) && record.ID > newest.ID) {
			newest, found = record, true
		}
	}
	return newest, found
}

// tally counts the recorded reception.
//
// Only reception-role assessments with a vote count, which is the rule §4.12
// states twice over: a contribution may exist without a vote, and a skip or a
// failure is not a vote. Skips are reported beside the tally rather than folded
// into it, so an item nobody could review is visibly that rather than
// unpopular.
func tally(assessments []roledAssessment, skips map[string]int) Reception {
	var out Reception
	for _, assessed := range assessments {
		if assessed.Role != RoleReception || assessed.Record.Assessment == nil {
			continue
		}
		switch assessed.Record.Assessment.Vote {
		case VoteSupport:
			out.Support++
		case VoteOppose:
			out.Oppose++
		case VoteUnsure:
			out.Unsure++
		default:
			// A contribution with no vote. It is a real review and a
			// real record, and it is deliberately not a tally entry.
			continue
		}
		out.Reviews++
	}
	for _, count := range skips {
		out.Skips += count
	}
	return out
}

// markContrary flags the outcomes that contradict an earlier one.
//
// Nothing is removed and nothing wins. §E6 requires contrary evidence to be
// displayed beside earlier verification with its scope and date rather than
// resolved by a last-writer-wins success badge, so both remain and the later
// one is marked as contrary to what came before.
func markContrary(outcomes []outcomeState) []outcomeState {
	if len(outcomes) < 2 {
		return outcomes
	}
	ordered := append([]outcomeState{}, outcomes...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].AsOf.Equal(ordered[j].AsOf) {
			return ordered[i].AsOf.Before(ordered[j].AsOf)
		}
		return ordered[i].RecordID < ordered[j].RecordID
	})
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Outcome != ordered[i-1].Outcome {
			ordered[i].Contrary = true
		}
	}
	return ordered
}

// countAlternatives reports how many artifacts address each problem.
//
// It is computed before grouping so the comparison role's activation and the
// grouping itself agree about what an alternative is.
func countAlternatives(artifacts []Artifact) map[Subject]int {
	groups := make(map[string][]Subject)
	for _, artifact := range artifacts {
		key := groupKey(projected{Artifact: artifact})
		groups[key] = append(groups[key], artifact.Subject)
	}
	out := make(map[Subject]int, len(artifacts))
	for _, members := range groups {
		for _, subject := range members {
			out[subject] = len(members)
		}
	}
	return out
}

// strengthenedAt reports when substantive material last arrived.
//
// A bare vote never moves it, which is §8.5's definition of the sort: a
// contribution with argument or new evidence, an operator criteria record, and
// an outcome assessment each strengthen a record; another support vote does not.
func strengthenedAt(assessments []roledAssessment, criteria []Record,
	outcomes []outcomeState) time.Time {
	var newest time.Time
	advance := func(at time.Time) {
		if at.After(newest) {
			newest = at
		}
	}
	for _, assessed := range assessments {
		if assessed.Record.Assessment == nil {
			continue
		}
		for _, contribution := range assessed.Record.Assessment.Contributions {
			if strings.TrimSpace(contribution.Text) != "" || len(contribution.Evidence) > 0 {
				advance(assessmentTime(assessed.Record))
				break
			}
		}
	}
	for _, record := range criteria {
		advance(record.CreatedAt)
	}
	for _, outcome := range outcomes {
		advance(outcome.AsOf)
	}
	return newest
}

// newestReview reports the most recent recorded assessment across roles.
func newestReview(coverage []RoleCoverage) time.Time {
	var newest time.Time
	for _, role := range coverage {
		if role.LastReviewed.After(newest) {
			newest = role.LastReviewed
		}
	}
	return newest
}

// dueFrom reports when the earliest activated obligation became due, and
// whether any has passed the overdue threshold.
func dueFrom(coverage []RoleCoverage, required map[string]bool,
	artifact Artifact, policy Policy) (time.Time, bool) {
	var (
		earliest time.Time
		overdue  bool
	)
	for _, role := range coverage {
		if !required[role.Role] {
			continue
		}
		if role.State != CoverageUnreviewed && role.State != CoverageDue {
			continue
		}
		since := role.LastReviewed
		if since.IsZero() {
			since = artifact.CreatedAt
		}
		if earliest.IsZero() || since.Before(earliest) {
			earliest = since
		}
		overdue = overdue || role.Overdue
	}
	return earliest, overdue
}

// mergeRecords unions the local journal with the fleet's, local winning.
//
// The local durable row is the better copy of a record this machine produced —
// it is current where a published record is a snapshot — which is the same
// tie-break internal/index applies when a fleet read hands a machine its own
// records back.
func mergeRecords(local, remote []Record) []Record {
	out := make([]Record, 0, len(local)+len(remote))
	seen := make(map[string]struct{}, len(local)+len(remote))
	for _, record := range local {
		if _, ok := seen[record.ID]; ok {
			continue
		}
		seen[record.ID] = struct{}{}
		out = append(out, record)
	}
	for _, record := range remote {
		if _, ok := seen[record.ID]; ok {
			continue
		}
		seen[record.ID] = struct{}{}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// collectAssignments unions this instance's assignments with the fleet's.
//
// Both halves are needed for two different reasons. An assignment another host
// claimed is what stops this one from drawing the same review, and the count of
// assignments per subject and role is the sample ordinal that makes the next
// assignment id deterministic across the deployment.
func (s *Service) collectAssignments(ctx context.Context, records []Record) ([]Assignment, error) {
	local, err := s.store.Assignments(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read local assignments: %w", err)
	}
	seen := make(map[string]struct{}, len(local))
	out := make([]Assignment, 0, len(local))
	for _, assignment := range local {
		seen[assignment.ID] = struct{}{}
		out = append(out, assignment)
	}
	for _, record := range records {
		if record.Kind != KindAssignment || record.Assignment == nil {
			continue
		}
		if _, ok := seen[record.Assignment.ID]; ok {
			continue
		}
		seen[record.Assignment.ID] = struct{}{}
		out = append(out, *record.Assignment)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// collectAttempts unions this instance's attempt journal with the fleet's.
func (s *Service) collectAttempts(ctx context.Context, records []Record) ([]Attempt, error) {
	local, err := s.store.Attempts(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read local attempts: %w", err)
	}
	out := append([]Attempt{}, local...)
	seen := make(map[string]struct{}, len(local))
	for _, attempt := range local {
		seen[attemptKey(attempt)] = struct{}{}
	}
	for _, record := range records {
		if record.Kind != KindAttempt || record.Attempt == nil {
			continue
		}
		if _, ok := seen[attemptKey(*record.Attempt)]; ok {
			continue
		}
		seen[attemptKey(*record.Attempt)] = struct{}{}
		out = append(out, *record.Attempt)
	}
	sort.Slice(out, func(i, j int) bool { return attemptKey(out[i]) < attemptKey(out[j]) })
	return out, nil
}

// attemptKey identifies one attempt across instances.
//
// Assignment, state and instant: a retry of one assignment is a second attempt
// at a different instant, and the same attempt published by its producer and
// read back through the catalog is one row. Without the instant, retries would
// collapse; with the record id instead, one attempt would count twice.
func attemptKey(attempt Attempt) string {
	return attempt.AssignmentID + "|" + attempt.State + "|" +
		formatProjectionTime(attempt.RecordedAt)
}

// activeClaims counts the assignments claimed and unsettled right now.
//
// Expired claims are excluded from "active" because they are not running — but
// their reserved cost is still accounted as spend (see spend), since §E4's
// conservative accounting refuses to pretend an unobserved expenditure was zero.
func activeClaims(assignments []Assignment, attempts []Attempt, now time.Time) int {
	settled := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		switch attempt.State {
		case AttemptCompleted, AttemptSkipped, AttemptFailed:
			settled[attempt.AssignmentID] = true
		}
	}
	count := 0
	for _, assignment := range assignments {
		if settled[assignment.ID] || !assignment.ExpiresAt.After(now) {
			continue
		}
		count++
	}
	return count
}

// policyRecords extracts every operator-authored policy publication.
func policyRecords(records []Record) []policyRecord {
	var out []policyRecord
	for _, record := range records {
		if record.Kind != KindPolicy || record.Policy == nil {
			continue
		}
		out = append(out, policyRecord{
			RecordID:  record.ID,
			CreatedAt: record.CreatedAt,
			Origin:    record.ActorKind,
			ActorKind: record.ActorKind,
			ActorID:   record.ActorID,
			Policy:    *record.Policy,
		})
	}
	return out
}

// latestCheckpoint reports the newest coverage sweep any instance published.
func latestCheckpoint(records []Record) (CoverageCheckpoint, bool) {
	var (
		newest CoverageCheckpoint
		found  bool
	)
	for _, record := range records {
		if record.Kind != KindCheckpoint || record.Checkpoint == nil {
			continue
		}
		if !found || record.Checkpoint.At.After(newest.At) {
			newest, found = *record.Checkpoint, true
		}
	}
	return newest, found
}

// assignmentsBySubject indexes assignments for the detail view.
func assignmentsBySubject(assignments []Assignment) map[Subject][]Assignment {
	out := make(map[Subject][]Assignment)
	for _, assignment := range assignments {
		out[assignment.Subject] = append(out[assignment.Subject], assignment)
	}
	return out
}

// inputDigest identifies the captured input set one snapshot was built from.
//
// It is deliberately NOT a context version and must not be confused with one.
// This digest covers the whole projection's inputs — every artifact revision,
// every record, the effective policy — so a refresh can tell whether anything
// at all moved. A context version covers one subject's *material* context, and
// nothing else, so that publishing this sweep's own assignment, attempt and
// checkpoint bookkeeping does not read as changed reality on every artifact and
// does not restore cooled-down items as fresh.
func inputDigest(artifacts []Artifact, records []Record, policy Policy) string {
	parts := make([]string, 0, len(artifacts)+len(records)+2)
	parts = append(parts, "policy:"+policy.Version, strconv.FormatBool(policy.Enabled))
	for _, artifact := range artifacts {
		parts = append(parts, "a:"+artifact.Subject.Kind+":"+artifact.Subject.ID+":"+
			artifact.ContextVersion+":"+artifact.ReviewStatus)
	}
	for _, record := range records {
		parts = append(parts, "r:"+record.ID)
	}
	sort.Strings(parts)
	return "in-" + digestOf(parts...)
}

// inventoryOf reads the produced-kind inventory when the source can report one.
//
// A source that cannot is not an error: the inventory is additional honesty
// about kinds evaluation does not review, and a build without it projects the
// reviewable kinds and says the rest are uncounted.
func (s *Service) inventoryOf(ctx context.Context) ([]KindInventory, error) {
	source, ok := s.src.(Inventory)
	if !ok {
		out := make([]KindInventory, 0, len(NonReviewableKinds()))
		for _, kind := range NonReviewableKinds() {
			out = append(out, KindInventory{Kind: kind, Reason: KindUnreviewableReason(kind)})
		}
		return out, nil
	}
	return source.Produced(ctx)
}

// List reads one page of the operator's view.
func (s *Service) List(ctx context.Context, q Query) (Page, error) {
	return s.proj.page(ctx, q)
}

// Detail reads one subject with its history, assignments and alternatives.
func (s *Service) Detail(ctx context.Context, subject Subject) (Detail, error) {
	return s.proj.detail(ctx, subject, "", "")
}

// Record reads one durable record by id, which a caller correcting a statement
// needs in order to name the run that authored it.
func (s *Service) Record(ctx context.Context, id string) (Record, error) {
	return s.store.Record(ctx, id)
}

// Coverage reports the deployment's review coverage.
//
// NextDraw is filled in here rather than stored, because it is a function of
// the effective policy and the last sweep and both can change without the
// projection moving. It stays zero when evaluation is disabled or when no sweep
// has ever completed: zero means unknown, never now.
func (s *Service) Coverage(ctx context.Context) (Coverage, error) {
	meta, _, err := s.proj.resolveSnapshot(ctx, "")
	if err != nil {
		return Coverage{}, err
	}
	coverage, err := s.proj.coverage(ctx, meta)
	if err != nil {
		return Coverage{}, err
	}
	policy, cached, policyErr := s.effectivePolicy(ctx)
	switch {
	case errors.Is(policyErr, ErrUnavailable):
		coverage.Reason = joinReasons(coverage.Reason, policyErr.Error())
		return coverage, nil
	case policyErr != nil:
		return Coverage{}, policyErr
	}
	if cached {
		coverage.Reason = joinReasons(coverage.Reason,
			"the effective policy is this instance's last known copy; the shared catalog "+
				"could not be read on the last refresh")
	}
	if policy.Enabled && !coverage.LastCheck.IsZero() {
		coverage.NextDraw = coverage.LastCheck.Add(
			time.Duration(policy.CadenceSeconds) * time.Second)
	}
	return coverage, nil
}

// Inventory reports every produced record kind and what evaluation does with
// it, including the kinds it does not review.
func (s *Service) Inventory(ctx context.Context) ([]KindInventory, error) {
	meta, _, err := s.proj.resolveSnapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	return s.proj.inventory(ctx, meta.ID)
}

// Policy reports the deployment's effective policy.
//
// The effective policy is the newest operator-authored policy this instance has
// seen anywhere, local or published by another host — not the local store's
// default. An independent reader has to see the fleet's actual approval and
// limits, and reading only the local row would make every non-producing instance
// believe evaluation was unconfigured.
//
// When nothing has ever been seen and the shared source could not be read, this
// refuses with ErrUnavailable rather than returning DefaultPolicy. The default
// is a safe startup value, not a statement about the deployment, and there is no
// status wrapper on this API in which to say which one a caller is holding.
func (s *Service) Policy(ctx context.Context) (Policy, error) {
	policy, cached, err := s.effectivePolicy(ctx)
	if err != nil {
		return Policy{}, err
	}
	_ = cached
	return policy, nil
}

// effectivePolicy resolves the deployment policy and reports whether it came
// from this instance's cache rather than from a complete read.
func (s *Service) effectivePolicy(ctx context.Context) (Policy, bool, error) {
	local, err := s.store.Policy(ctx)
	if err != nil {
		return Policy{}, false, fmt.Errorf("evaluation: read local policy: %w", err)
	}
	newest, ok, err := s.proj.newestPolicy(ctx)
	if err != nil {
		return Policy{}, false, err
	}
	degraded := s.sourceDegraded()

	switch {
	case ok && newest.CreatedAt.After(time.Time{}):
		// A recorded operator policy exists somewhere in the deployment.
		// It is a fact whether or not the catalog is reachable right
		// now; `cached` says which.
		return newest.Policy, degraded, nil
	case localConfigured(local):
		return local, degraded, nil
	case degraded:
		return Policy{}, false, fmt.Errorf(
			"%w: no operator policy is recorded on this instance and the shared catalog "+
				"could not be read, so the deployment's policy is unknown", ErrUnavailable)
	}
	// Shared state was read successfully and nobody has configured a policy.
	// The default is then the truth about this deployment rather than a
	// stand-in for an unknown.
	return local, false, nil
}

// localConfigured reports whether the local store returned a recorded operator
// policy rather than the built-in default.
//
// The comparison is against DefaultPolicy by value, which is exact: Store.Policy
// returns DefaultPolicy() when no operator record exists, so an operator who
// deliberately saved the default settings is indistinguishable from one who
// saved nothing — and that is the right answer, because the two configurations
// are the same configuration.
func localConfigured(policy Policy) bool { return policy != DefaultPolicy() }

// sourceDegraded reports whether the last source read was incomplete.
func (s *Service) sourceDegraded() bool {
	status, ok := s.src.(StatusSource)
	if !ok {
		return false
	}
	state := status.Status()
	return state.Unavailable != ""
}

// Configure records an operator policy change.
//
// It validates before recording, because an invalid policy stored as a record
// would be a durable, published instruction nothing can honour. Saving does not
// start compute: the record is the configuration, and a scheduler reads it on
// its next cycle.
func (s *Service) Configure(ctx context.Context, operator string, p Policy) (Record, error) {
	if strings.TrimSpace(operator) == "" {
		return Record{}, fmt.Errorf("%w: a policy change needs an operator", ErrInvalid)
	}
	previous, _, err := s.effectivePolicy(ctx)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return Record{}, err
	}
	// The inbound Version is the version the caller read, not the version to
	// store. Treating it as the latter would put two saves under one version
	// id, and that string is what a draw records for replay — so an
	// ambiguous one makes a historical draw unreproducible. Used as a seen
	// version instead, it is free optimistic concurrency: an operator saving
	// a form built from a policy someone else has since replaced is told so
	// rather than silently overwriting it.
	if seen := strings.TrimSpace(p.Version); seen != "" && seen != previous.Version {
		return Record{}, fmt.Errorf(
			"%w: this change was composed against policy version %q but the deployment's "+
				"effective policy is now %q; re-read it and apply the change again",
			ErrConflict, seen, previous.Version)
	}
	version, err := s.nextPolicyVersion(ctx)
	if err != nil {
		return Record{}, err
	}
	p.Version = version
	if err := ValidatePolicy(p); err != nil {
		return Record{}, err
	}
	record, err := s.store.Operator(ctx, OperatorInput{
		Kind:     KindPolicy,
		Operator: operator,
		Policy:   &p,
		Reason:   policyChangeReason(p, previous),
	})
	if err != nil {
		return Record{}, err
	}
	if err := s.proj.rememberPolicies(ctx, []policyRecord{{
		RecordID:  record.ID,
		CreatedAt: record.CreatedAt,
		Origin:    ActorOperator,
		ActorKind: record.ActorKind,
		ActorID:   record.ActorID,
		Policy:    p,
	}}); err != nil {
		return Record{}, err
	}
	// A policy change invalidates derived work and nothing durable. The
	// records keep the version they were made under, so a historical draw
	// stays replayable; what has to be recomputed is the projection that
	// used the old settings as an input.
	if p.Invalidates(previous).Changed() {
		if err := s.Refresh(ctx); err != nil {
			return record, err
		}
	}
	return record, nil
}

// policyChangeReason states what a policy edit changed, for the record.
func policyChangeReason(next, previous Policy) string {
	invalidation := next.Invalidates(previous)
	if len(invalidation.Reasons) == 0 {
		return "policy re-saved with no setting changed"
	}
	return "changed: " + strings.Join(invalidation.Reasons, ", ")
}

// nextPolicyVersion mints an unused policy version.
//
// The form is `eval-policy-<n>`, readable and orderable by eye, and the number
// is one past however many versions this instance has recorded — then advanced
// until it is free. Checking rather than counting is what guarantees
// uniqueness: two saves must not land under one id, because Assignment,
// Submission and Record all carry the version a decision was made under, and a
// version naming two different settings would make a replay ambiguous.
//
// The version this instance mints is unique here and is not coordinated with
// other hosts, which is correct: a policy is an operator act on the instance
// they are using, and Service.Policy resolves the newest one across the
// deployment by record time. Two hosts minting the same number would be two
// different operator decisions, distinguishable by their record ids and ordered
// by when they were made.
func (s *Service) nextPolicyVersion(ctx context.Context) (string, error) {
	taken, err := s.proj.policyVersions(ctx)
	if err != nil {
		return "", err
	}
	for n := len(taken) + 1; ; n++ {
		candidate := "eval-policy-" + strconv.Itoa(n)
		if !taken[candidate] {
			return candidate, nil
		}
	}
}

// Operator records an operator-authored act: criteria, feedback, a reconsider
// decision, or a policy.
//
// The kind is passed through unfiltered and the store refuses what an operator
// may not author. That is deliberate: one refusal, in the place that owns the
// vocabulary, rather than a second filter here that could drift from it.
//
// The projection is refreshed before this returns, which is what a command
// wants: `babel evaluation feedback` prints the queue it just changed, and a
// listing drawn from a projection that has not caught up would show the
// operator his own act missing. A caller that has to answer first uses
// OperatorDeferred.
func (s *Service) Operator(ctx context.Context, in OperatorInput) (Record, error) {
	record, refresh, err := s.OperatorDeferred(ctx, in)
	if err != nil {
		return Record{}, err
	}
	if err := refresh(ctx); err != nil {
		return record, err
	}
	return record, nil
}

// OperatorDeferred records the same act and hands back the projection refresh
// it owes rather than performing it.
//
// It exists because the two halves of Operator have nothing in common but
// their order. The durable write is the authority — one insert, one
// transaction, and the operator's stance is a fact the moment it commits — and
// the refresh is bookkeeping over a rebuildable cache that reads this
// instance's evaluation records, its assignments, its attempts, the subject's
// artifact and the effective policy in order to replace one row. On a browser
// click the second one is the whole latency: an operator agreeing with a
// record waited on work that had nothing to do with recording that he agreed,
// measured at 6.5 seconds on a live catalog under concurrent lane writes.
//
// The refresh is still owed and the caller still owes it. It is a closure
// rather than a flag so that the obligation is visible at the call site, and
// it takes its own context because the caller running it after a response has
// no request context left to run it under. Skipping it entirely is not a
// corruption — the projection is rebuilt from the durable records on the
// launch's own schedule — but it is a projection that lags, so a caller that
// drops it is choosing a stale read for as long as that takes.
func (s *Service) OperatorDeferred(ctx context.Context, in OperatorInput) (
	Record, func(context.Context) error, error) {
	record, err := s.store.Operator(ctx, in)
	if err != nil {
		return Record{}, nil, err
	}
	if in.Kind == KindPolicy && in.Policy != nil {
		// The policy memo is part of the write rather than of the refresh:
		// it is what makes the recorded policy readable at all, and a
		// caller deferring the refresh is not deferring that.
		if err := s.proj.rememberPolicies(ctx, []policyRecord{{
			RecordID:  record.ID,
			CreatedAt: record.CreatedAt,
			Origin:    ActorOperator,
			ActorKind: record.ActorKind,
			ActorID:   record.ActorID,
			Policy:    *in.Policy,
		}}); err != nil {
			return Record{}, nil, err
		}
	}
	subject := in.Subject
	return record, func(ctx context.Context) error { return s.reproject(ctx, subject) }, nil
}

// Submit records a worker's assessment, skip or failure.
func (s *Service) Submit(ctx context.Context, in Submission) (Record, error) {
	record, err := s.store.Submit(ctx, in)
	if err != nil {
		return Record{}, err
	}
	assignment, assignErr := s.store.Assignment(ctx, in.AssignmentID)
	if assignErr == nil {
		if err := s.reproject(ctx, assignment.Subject); err != nil {
			return record, err
		}
	}
	return record, nil
}

// Correct appends a linked correction to a record this run already wrote.
//
// It is a real second pass rather than an edit: §E1 keeps one active vote per
// assignment and preserves the earlier statement, so a revealed role that
// changes its mind produces two readable records joined by a link, not one
// rewritten one. The caller passes the id ReviewInput.Corrects named and
// nothing else, which is what stops a worker from correcting a record it did
// not author.
func (s *Service) Correct(ctx context.Context, id string, in Submission) (Record, error) {
	if strings.TrimSpace(id) == "" {
		return Record{}, fmt.Errorf("%w: a correction names no record", ErrInvalid)
	}
	record, err := s.store.Correct(ctx, id, in)
	if err != nil {
		return Record{}, err
	}
	assignment, assignErr := s.store.Assignment(ctx, in.AssignmentID)
	if assignErr == nil {
		if err := s.reproject(ctx, assignment.Subject); err != nil {
			return record, err
		}
	}
	return record, nil
}

// Recover settles the claims a crash or a cancellation left mid-flight, then
// rebuilds the projection.
//
// The store's recovery is what makes the release durable: a lapsed lease is
// already excluded from Coverage.Active and already drawable again, because
// both read the lease rather than a mutable flag, but the attempt journal has
// to record what became of the work or the reservation stays outstanding
// forever.
//
// The reserved cost of a lapsed claim is not zeroed by recovering it. §E4's
// conservative accounting refuses to pretend an unobserved expenditure was
// zero, and a recovery that cleared it would let a crashed worker's spend be
// drawn twice from one allowance; a settled attempt replaces the reservation
// with the cost that was actually observed, which is a different thing from
// erasing it.
//
// The rebuild is the second half and is why this is not just Store.Recover: a
// caller after a cancellation needs the coverage view to stop reporting the
// work as running, and that view is derived.
func (s *Service) Recover(ctx context.Context) error {
	if _, err := s.store.Recover(ctx); err != nil {
		return fmt.Errorf("evaluation: recover in-flight claims: %w", err)
	}
	return s.Refresh(ctx)
}

// reproject refreshes one subject's rows inside the current snapshot.
//
// The snapshot's *order* is deliberately not recomputed. A snapshot is the
// pagination contract, so a vote landing while an operator pages must not
// reshuffle the page under them; what has to be current is the item's own
// content — its tally, its coverage, its lane — which is what a reader is
// looking at when they refresh one row. The next periodic refresh reorders.
//
// A zero subject is a no-op, which is what an operator act with no subject —
// a policy change — legitimately is.
func (s *Service) reproject(ctx context.Context, subject Subject) error {
	if subject.ID == "" {
		return nil
	}
	meta, ok, err := s.proj.current(ctx)
	if err != nil || !ok {
		return err
	}
	artifact, err := s.src.Artifact(ctx, subject)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
			return nil
		}
		return err
	}
	records, err := s.store.Events(ctx)
	if err != nil {
		return fmt.Errorf("evaluation: read local records: %w", err)
	}
	assignments, err := s.collectAssignments(ctx, records)
	if err != nil {
		return err
	}
	attempts, err := s.collectAttempts(ctx, records)
	if err != nil {
		return err
	}
	policy, _, policyErr := s.effectivePolicy(ctx)
	if policyErr != nil && !errors.Is(policyErr, ErrUnavailable) {
		return policyErr
	}
	if errors.Is(policyErr, ErrUnavailable) {
		policy = DefaultPolicy()
	}
	items, history := s.project([]Artifact{artifact}, records, assignments, attempts,
		policy, time.Now().UTC())
	if len(items) == 0 {
		return nil
	}
	items[0].Score = score(&items[0], policy)
	items[0].Coverage, items[0].CoverageReason = deriveCoverage(&items[0])
	items[0].Lane = deriveLane(&items[0])
	return s.proj.replaceItem(ctx, meta.ID, items[0], history[subject])
}

// Draw reserves the next review and claims it.
//
// The whole selection happens here and the claim happens through the store, so
// no caller ever calls Store.Claim itself: a draw that reserved locally and
// claimed elsewhere would be two different decisions about one allowance.
//
// The stopping reason is recorded whether or not an assignment was produced,
// because "why is nothing being reviewed" is the question an operator asks
// exactly when there is nothing to look at. ErrNoWork and ErrBudget are distinct
// so a scheduler can idle on the first and park on the second.
func (s *Service) Draw(ctx context.Context, runID string, seed uint64) (Assignment, error) {
	policy, _, err := s.effectivePolicy(ctx)
	if err != nil {
		return Assignment{}, err
	}
	meta, _, err := s.proj.resolveSnapshot(ctx, "")
	if err != nil {
		return Assignment{}, err
	}
	items, err := s.proj.items(ctx, meta.ID)
	if err != nil {
		return Assignment{}, err
	}
	assignments, attempts, active, today, cycle, err := s.spendState(ctx, policy)
	if err != nil {
		return Assignment{}, err
	}

	now := time.Now().UTC()
	result, drawErr := selectDraw(drawInput{
		Policy:       policy,
		Items:        items,
		Assignments:  assignments,
		Attempts:     attempts,
		InputDigest:  meta.InputDigest,
		SpentToday:   today,
		SpentCycle:   cycle,
		ActiveClaims: active,
		Now:          now,
	}, runID, seed)

	record := drawRecord{
		At:            now,
		RunID:         runID,
		Seed:          seed,
		InputDigest:   meta.InputDigest,
		PolicyVersion: policy.Version,
		Lane:          result.Lane,
		Role:          result.Assignment.Role,
		Subject:       result.Assignment.Subject,
		AssignmentID:  result.Assignment.ID,
		StopReason:    result.StopReason,
	}
	if drawErr != nil {
		if err := s.proj.rememberDraw(ctx, record); err != nil {
			return Assignment{}, err
		}
		return Assignment{}, drawErr
	}

	claimed, err := s.store.Claim(ctx, result.Assignment, policy)
	if err != nil {
		record.StopReason = err.Error()
		if writeErr := s.proj.rememberDraw(ctx, record); writeErr != nil {
			return Assignment{}, writeErr
		}
		return Assignment{}, err
	}
	record.AssignmentID = claimed.ID
	if err := s.proj.rememberDraw(ctx, record); err != nil {
		return Assignment{}, err
	}
	return claimed, nil
}

// Correction claims a follow-up review whose purpose is to supersede one
// statement this run already made.
//
// It exists because a correction cannot arrive through Draw. Draw's assignments
// are independent reviews: a blinded role is served without prior evaluations,
// so a worker handed one has no way to know it is revisiting its own statement
// and no authority to replace it. Naming the target up front is what makes the
// follow-up reachable at all — and it is what keeps the reveal narrow, because
// this is the only assignment for which a blinded role sees anything prior, and
// what it sees is its own record.
//
// Four refusals, and each is an authority rule rather than a convenience:
//
//   - The target must be an assessment. Nothing else is a statement to correct.
//   - The target must be this run's own. §E3 refuses same-run self-boosting of
//     newly authored alternatives, and correcting somebody else's assessment
//     would be the same failure pointed the other way: a run rewriting a
//     judgement it did not make.
//   - The target must still be active. A record another correction already
//     superseded is history; correcting it again would fork the chain and leave
//     two effective statements from one review.
//   - The spend must be admissible. A correction is paid work with its own
//     reservation, claimed before any compute, through exactly the bounds every
//     other claim passes.
func (s *Service) Correction(ctx context.Context, id, runID string, seed uint64) (Assignment, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(runID) == "" {
		return Assignment{}, fmt.Errorf("%w: a correction needs a target record and a run",
			ErrInvalid)
	}
	policy, _, err := s.effectivePolicy(ctx)
	if err != nil {
		return Assignment{}, err
	}
	if !policy.Enabled {
		return Assignment{}, fmt.Errorf("%w: policy %s has authorized evaluation disabled",
			ErrNoWork, policy.Version)
	}
	target, err := s.store.Record(ctx, id)
	if err != nil {
		return Assignment{}, err
	}
	if target.Kind != KindAssessment || target.Assessment == nil {
		return Assignment{}, fmt.Errorf(
			"%w: record %s is a %s and carries no assessment to supersede",
			ErrInvalid, id, target.Kind)
	}
	if target.Provenance.RunID != runID {
		return Assignment{}, fmt.Errorf(
			"%w: record %s was authored by run %s, so run %s may not correct it",
			ErrInvalid, id, target.Provenance.RunID, runID)
	}
	original, err := s.store.Assignment(ctx, target.AssignmentID)
	if err != nil {
		return Assignment{}, err
	}

	records, err := s.store.Events(ctx)
	if err != nil {
		return Assignment{}, fmt.Errorf("evaluation: read local records: %w", err)
	}
	remote, _ := s.src.EvaluationRecords(ctx)
	for _, record := range mergeRecords(records, remote) {
		if record.SupersedesID == id {
			return Assignment{}, fmt.Errorf(
				"%w: record %s was already superseded by %s", ErrConflict, id, record.ID)
		}
	}

	artifact, err := s.src.Artifact(ctx, target.Subject)
	if err != nil {
		return Assignment{}, err
	}
	_, _, active, today, cycle, err := s.spendState(ctx, policy)
	if err != nil {
		return Assignment{}, err
	}
	reserved, reason, err := admitSpend(policy, active, cycle, today)
	if err != nil {
		now := time.Now().UTC()
		if writeErr := s.proj.rememberDraw(ctx, drawRecord{
			At: now, RunID: runID, Seed: seed, PolicyVersion: policy.Version,
			Role: original.Role, Subject: target.Subject, StopReason: reason,
		}); writeErr != nil {
			return Assignment{}, writeErr
		}
		return Assignment{}, err
	}
	now := time.Now().UTC()
	assignment := Assignment{
		ID:             correctionID(id, policy.Version, artifact.ContextVersion),
		Subject:        target.Subject,
		RunID:          runID,
		Role:           original.Role,
		PolicyVersion:  policy.Version,
		ContextVersion: artifact.ContextVersion,
		Seed:           seed,
		InputDigest:    target.ID,
		CreatedAt:      now,
		ExpiresAt:      now.Add(time.Duration(policy.LeaseSeconds) * time.Second),
		ReservedCost:   reserved,
		Lane:           original.Lane,
		Subjects:       recordedNames(artifact),
		Corrects:       id,
	}
	claimed, err := s.store.Claim(ctx, assignment, policy)
	record := drawRecord{
		At: now, RunID: runID, Seed: seed, InputDigest: target.ID,
		PolicyVersion: policy.Version, Lane: assignment.Lane, Role: assignment.Role,
		Subject: target.Subject, AssignmentID: assignment.ID,
	}
	if err != nil {
		record.StopReason = err.Error()
		if writeErr := s.proj.rememberDraw(ctx, record); writeErr != nil {
			return Assignment{}, writeErr
		}
		return Assignment{}, err
	}
	record.AssignmentID = claimed.ID
	if err := s.proj.rememberDraw(ctx, record); err != nil {
		return Assignment{}, err
	}
	return claimed, nil
}

// spendState reads the assignment and attempt journal and the spend it implies.
//
// It is shared by Draw and Correction because both have to admit against the
// same accounting: two readings of what the deployment has spent would let one
// path claim work the other had already paid for.
func (s *Service) spendState(ctx context.Context, policy Policy) (
	[]Assignment, []Attempt, int, float64, float64, error) {
	meta, _, err := s.proj.resolveSnapshot(ctx, "")
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	assignments, err := s.proj.assignments(ctx, meta.ID)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	local, err := s.store.Assignments(ctx)
	if err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("evaluation: read local assignments: %w", err)
	}
	assignments = mergeAssignments(assignments, local)

	attempts, err := s.proj.attempts(ctx, meta.ID)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	localAttempts, err := s.store.Attempts(ctx)
	if err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("evaluation: read local attempts: %w", err)
	}
	attempts = mergeAttempts(attempts, localAttempts)

	now := time.Now().UTC()
	cycleStart := now.Add(-time.Duration(policy.CadenceSeconds) * time.Second)
	if at, _, _, ok, err := s.proj.lastCheckpoint(ctx); err != nil {
		return nil, nil, 0, 0, 0, err
	} else if ok && at.After(cycleStart) {
		cycleStart = at
	}
	today, cycle := spend(assignments, attempts, now, cycleStart)
	return assignments, attempts, activeClaims(assignments, attempts, now), today, cycle, nil
}

// spend reports the authorized cost already accounted for today and this cycle.
//
// Two sources, and both are required by §E4's conservative accounting. Recorded
// attempt costs are the observed spend. Reservations on claims that have not
// settled — including claims whose lease expired without a receipt — are counted
// too, because an expired reservation cannot pretend the unobserved expenditure
// was zero; the alternative would let a crashed worker's spend be drawn again
// from the same allowance.
func spend(assignments []Assignment, attempts []Attempt, now, cycleStart time.Time) (today, cycle float64) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	settled := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		switch attempt.State {
		case AttemptCompleted, AttemptSkipped, AttemptFailed:
			settled[attempt.AssignmentID] = true
		}
		if attempt.Cost == 0 {
			continue
		}
		if !attempt.RecordedAt.Before(day) {
			today += attempt.Cost
		}
		if !attempt.RecordedAt.Before(cycleStart) {
			cycle += attempt.Cost
		}
	}
	for _, assignment := range assignments {
		if settled[assignment.ID] || assignment.ReservedCost == 0 {
			continue
		}
		if !assignment.CreatedAt.Before(day) {
			today += assignment.ReservedCost
		}
		if !assignment.CreatedAt.Before(cycleStart) {
			cycle += assignment.ReservedCost
		}
	}
	return today, cycle
}

// mergeAssignments unions two assignment lists by id, the second winning.
//
// The second is the local store's, and it wins because a local row is
// authoritative about a claim this instance holds where a projected copy is a
// snapshot of it.
func mergeAssignments(projected, local []Assignment) []Assignment {
	index := make(map[string]Assignment, len(projected)+len(local))
	for _, assignment := range projected {
		index[assignment.ID] = assignment
	}
	for _, assignment := range local {
		index[assignment.ID] = assignment
	}
	out := make([]Assignment, 0, len(index))
	for _, assignment := range index {
		out = append(out, assignment)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// mergeAttempts unions two attempt lists.
func mergeAttempts(projected, local []Attempt) []Attempt {
	index := make(map[string]Attempt, len(projected)+len(local))
	for _, attempt := range projected {
		index[attemptKey(attempt)] = attempt
	}
	for _, attempt := range local {
		index[attemptKey(attempt)] = attempt
	}
	out := make([]Attempt, 0, len(index))
	for _, attempt := range index {
		out = append(out, attempt)
	}
	sort.Slice(out, func(i, j int) bool { return attemptKey(out[i]) < attemptKey(out[j]) })
	return out
}

// Review serves one assignment's read context.
//
// It validates the claim, records the exposure, and then assembles what the role
// is entitled to see. The order matters: a worker that cannot prove a valid
// claim is refused before any content is read, and the exposure is recorded
// before the content is returned, so "this model saw this record" is durable
// even if the worker then crashes. §E3 calls that procedural blinding rather
// than erased memory, and an unrecorded exposure would make the audit it exists
// for impossible.
func (s *Service) Review(ctx context.Context, a Assignment) (ReviewInput, error) {
	if a.ID == "" || a.RunID == "" {
		return ReviewInput{}, fmt.Errorf("%w: review needs an assignment and a run", ErrInvalid)
	}
	if err := s.store.ValidateClaim(ctx, a.ID, a.RunID, a.Fence); err != nil {
		return ReviewInput{}, err
	}
	stored, err := s.store.Assignment(ctx, a.ID)
	if err != nil {
		return ReviewInput{}, err
	}
	if err := s.store.Expose(ctx, stored.ID, a.RunID, a.Fence); err != nil {
		return ReviewInput{}, err
	}
	artifact, err := s.src.Artifact(ctx, stored.Subject)
	if err != nil {
		return ReviewInput{}, err
	}
	// Source resolves the record; the criteria authority is a record family
	// the store owns, so the projection is where the two are already joined.
	// Overlaying it here rather than re-deriving keeps one answer to "which
	// criteria version is current", and an artifact the projection has not
	// seen keeps an empty CriteriaID — an outcome role then has no authority
	// to judge against and must say so rather than inventing one.
	artifact = s.withCriteriaAuthority(ctx, artifact)
	out := ReviewInput{Assignment: stored, Artifact: blind(artifact, stored.Role), Corrects: stored.Corrects}
	// A correction is the one assignment for which a blinded role sees
	// anything prior, and what it is entitled to see is bounded by the claim
	// rather than by the role: the assignment names the statement it was
	// drawn to supersede, so the reveal is this run's own record and the
	// other assessments on the same subject. An ordinary reception
	// assignment carries no Corrects and stays blinded, which is what keeps
	// this from becoming a way to ask for prior votes before an initial one.
	if Blinded(stored.Role) && stored.Corrects == "" {
		return out, nil
	}

	// Challenge and comparison are answers about the disagreement, so the
	// prior records are revealed — attributed, and only for these roles.
	records, err := s.store.Events(ctx)
	if err != nil {
		return ReviewInput{}, fmt.Errorf("evaluation: read local records: %w", err)
	}
	remote, _ := s.src.EvaluationRecords(ctx)
	for _, record := range mergeRecords(records, remote) {
		if record.Subject != stored.Subject || record.Kind != KindAssessment {
			continue
		}
		out.Previous = append(out.Previous, record)
		// This run's own earlier statement about this subject is what a
		// second pass supersedes. Naming it here is what keeps the
		// correction decision out of the worker: Previous is ordered
		// oldest first, so the last match is the newest statement, and a
		// record authored by a different run is never a correction
		// candidate however similar it reads.
		//
		// A claim that already named its target wins. Correction()
		// validated that record's authorship and that nothing had
		// superseded it, and a derivation that could move the target
		// afterwards would let a correction supersede something the
		// claim was not admitted for.
		if stored.Corrects == "" && record.Provenance.RunID == stored.RunID &&
			record.Provenance.RunID != "" {
			out.Corrects = record.ID
		}
	}
	if stored.Role != RoleComparison {
		return out, nil
	}
	meta, _, err := s.proj.resolveSnapshot(ctx, "")
	if err != nil {
		return out, nil
	}
	item, ok, err := s.proj.item(ctx, meta.ID, stored.Subject)
	if err != nil {
		return ReviewInput{}, err
	}
	if !ok {
		return out, nil
	}
	for _, other := range item.Alternatives {
		alternative, err := s.src.Artifact(ctx, other)
		if err != nil {
			continue
		}
		// Alternatives are served as artifacts, never as ranked items:
		// an Item carries a reception tally, and handing one to a
		// comparison would leak both sides' votes into the read §E3
		// requires to withhold them.
		out.Alternatives = append(out.Alternatives, alternative)
	}
	return out, nil
}

// gradingKeys are the producer-supplied grading fields withheld from every
// served review.
//
// They are the model's own estimates about its own output — novelty, priority,
// confidence, impact, term overlap — and §10 warns that confidence never
// substitutes for evidence. A reviewer shown "confidence: high" beside a claim
// is being shown the author's self-assessment as if it were content, which is
// the same failure as showing a tally: it answers the question the reviewer was
// asked to answer.
//
// The set also covers reception-shaped keys. Nothing in a frontier payload
// carries them today, but this is the audit of the actual served bytes that §E3
// requires, and an audit that only covered the fields that happen to exist now
// would pass every version of the code except the one that breaks it.
var gradingKeys = map[string]bool{
	"novelty": true, "priority": true, "confidence": true, "impact": true,
	"overlap": true, "score": true, "rank": true, "reception": true,
	"support": true, "oppose": true, "unsure": true, "reviews": true, "votes": true,
}

// withholdGradings removes every grading key from a payload, at any depth.
//
// Recursion is required rather than tidy. A proposal's suggested targets are an
// array of objects each carrying its own confidence, and a top-level key sweep
// would leave them in place — so a build that only deleted the two fields a
// hypothesis happens to have at the top level would serve graded content for
// every other kind while reporting itself as blinded.
//
// It returns the payload unchanged when the bytes are not a JSON object or
// array, and it never invents a field. A withheld grading is an absence, and an
// absence is not a vote: the reviewer is told in Context.Unknown that gradings
// were withheld, and nothing downstream reads the gap as an opinion.
func withholdGradings(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return raw, false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return raw, false
	}
	stripped, removed := stripGradings(decoded)
	if !removed {
		return raw, false
	}
	encoded, err := json.Marshal(stripped)
	if err != nil {
		return raw, false
	}
	return encoded, true
}

// stripGradings walks a decoded payload, deleting grading keys.
func stripGradings(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		removed := false
		for key := range typed {
			if gradingKeys[strings.ToLower(key)] {
				delete(typed, key)
				removed = true
			}
		}
		for key, nested := range typed {
			cleaned, nestedRemoved := stripGradings(nested)
			typed[key] = cleaned
			removed = removed || nestedRemoved
		}
		return typed, removed
	case []any:
		removed := false
		for i, nested := range typed {
			cleaned, nestedRemoved := stripGradings(nested)
			typed[i] = cleaned
			removed = removed || nestedRemoved
		}
		return typed, removed
	}
	return value, false
}

// blind prepares one artifact for a served review.
//
// It is the audit of the actual read rather than of the prompt, which is what
// §E3 asks for, and it makes two removals and no substitutions.
//
// Producer gradings are withheld from every role. They are not review context
// under any question a reviewer is asked, and withholding them unconditionally
// is also what keeps the served body acceptable to a caller that refuses to
// launch on graded content — including for the revealed roles, where the prior
// assessments arrive as attributed records instead.
//
// The recorded operator context is withheld from blinded roles, because
// priority, current work and pain are exactly the signals the recommended
// ordering is built from. The exception is relevance, whose entire question is
// whether this matters to the recorded work; withholding it there would not be
// blinding, it would make the role unperformable.
//
// Everything substantive survives untouched: the subject and revision binding,
// the record's own wording, its evidence and counter-evidence, and its criteria.
// A withheld field is stated as withheld and never becomes a vote, a default, or
// a missing-content excuse for an assessment.
func blind(artifact Artifact, role string) Artifact {
	body, withheld := withholdGradings(artifact.Body)
	artifact.Body = body

	if !Blinded(role) {
		if withheld {
			artifact.Context.Unknown = append(artifact.Context.Unknown,
				"the producing run's own gradings are withheld from every served review; "+
					"their absence is not an assessment")
		}
		return artifact
	}
	unknown := []string{}
	if withheld {
		unknown = append(unknown,
			"the producing run's own gradings are withheld from every served review; "+
				"their absence is not an assessment")
	}
	if role == RoleRelevance {
		artifact.Context.Unknown = append(artifact.Context.Unknown, unknown...)
		return artifact
	}
	artifact.Context = Context{
		Version: artifact.Context.Version,
		Unknown: append(unknown, "recorded priority, current work and pain are withheld from "+
			"a blinded review so a rank cannot be read as content"),
	}
	return artifact
}

// Draws reports the recorded selection decisions, newest first.
//
// It is the replay surface: each row carries the seed, the captured input
// digest, the policy version, the role, the lane and the stopping reason, which
// is what §E4 requires beyond a seed for a draw to be re-derivable.
func (s *Service) Draws(ctx context.Context, limit int) ([]drawRecord, error) {
	return s.proj.draws(ctx, limit)
}

// withCriteriaAuthority attaches the operator-adopted criteria version to an
// artifact, when one has been resolved.
//
// It reads the projection because that is where the record's own suggested
// criteria and the operator's criteria act are already joined, and a second
// derivation would be a second answer to which version is current. When the
// projection has not seen the subject — a revision published since the last
// refresh, or a projection not yet built — the artifact keeps whatever the
// source gave it and CriteriaID stays empty.
//
// Empty is a real answer and must not be papered over. An outcome assessment
// names the criteria version it was judged against; with none adopted there is
// no authority to judge against, and §E6 is explicit that Babel may suggest
// criteria but cannot verify itself against a target it chose.
func (s *Service) withCriteriaAuthority(ctx context.Context, artifact Artifact) Artifact {
	meta, _, err := s.proj.resolveSnapshot(ctx, "")
	if err != nil {
		return artifact
	}
	item, ok, err := s.proj.item(ctx, meta.ID, artifact.Subject)
	if err != nil || !ok {
		return artifact
	}
	artifact.CriteriaID = item.Artifact.CriteriaID
	if len(item.Artifact.Criteria) > 0 {
		artifact.Criteria = item.Artifact.Criteria
	}
	return artifact
}

// digestOf hashes a length-prefixed sequence of parts.
//
// Length-prefixed because concatenation is ambiguous: "ab"+"c" and "a"+"bc"
// hash identically without it, which would make two different subjects share a
// criterion id or two different inputs share a digest. It is the same reason
// internal/run length-prefixes a preparation identity.
func digestOf(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(hash, "%d:", len(part))
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))[:32]
}

// maxTitleBytes bounds an artifact's one-line title.
//
// It is internal/frontier's bound restated rather than shared, because that
// package's is unexported and a title that wrapped in one surface while staying
// on one line in another would be a listing whose rows disagree about how tall
// a row is.
const maxTitleBytes = 240

// summarizeLine reduces one field to a bounded single line.
//
// A newline becomes a space rather than a truncation point: a statement whose
// first line is a noun phrase and whose second carries the verb would otherwise
// be summarized into something that says nothing. The cut never splits a rune,
// because half a rune is invalid UTF-8 and would reach a JSON encoder as a
// substitution character in the middle of a record's own wording.
func summarizeLine(text string) string {
	collapsed := strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if len(collapsed) <= maxTitleBytes {
		return collapsed
	}
	cut := maxTitleBytes
	for cut > 0 && !utf8Boundary(collapsed[cut]) {
		cut--
	}
	return strings.TrimSpace(collapsed[:cut])
}

// utf8Boundary reports whether a byte starts a rune.
func utf8Boundary(b byte) bool { return b&0xC0 != 0x80 }
