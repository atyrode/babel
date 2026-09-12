package reality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// recordingFiler stands in for internal/frontier. The real File writes an
// about edge in another component's tables; what these tests need to know is
// what the ledger handed it and when, which is exactly what a recorder shows
// and a real store would bury in a second database.
type recordingFiler struct {
	filed []frontier.FilingInput
	// failOn refuses the record with this ID, which is how the tests reach
	// the half-applied application §4.13's two stores make possible.
	failOn string
}

func (f *recordingFiler) File(ctx context.Context, in frontier.FilingInput) (frontier.Filing, error) {
	if f.failOn != "" && in.Record.ID == f.failOn {
		return frontier.Filing{}, errors.New("synthetic filer refusal")
	}
	f.filed = append(f.filed, in)
	return frontier.Filing{
		ID:        "fil-" + in.Record.ID,
		Record:    in.Record,
		EntityID:  in.EntityID,
		Rationale: in.Rationale,
		Author:    in.Author,
		AuthorID:  in.AuthorID,
		Heuristic: in.Heuristic,
	}, nil
}

// manifold is the plan every test in this file starts from: one repository,
// bound by its remote, seen at two worktrees, with two records that would be
// filed under it — carried by the proposal record the operator rules on.
func manifold(proposalID string, sessions int) TopicPlan {
	return TopicPlan{
		ProposalID: proposalID,
		Operation:  TopicCreate,
		Identity:   "github.com/atyrode/manifold",
		Entity: &EntityDraft{
			Subject: NewSubject{
				Kind:        EntityRepository,
				DisplayName: "manifold",
				Aliases: []AliasInput{
					{Kind: AliasRepository, Payload: AliasPayload{Value: "github.com/atyrode/manifold"}},
					{Kind: AliasPath, Payload: AliasPayload{Value: "/home/alex/manifold"}},
					{Kind: AliasPath, Payload: AliasPayload{Value: "/home/alex/wt/manifold-fix"}},
				},
			},
			Binding: []FactInput{{
				Predicate: PredicateRepositoryRemote,
				Value:     FactValue{Kind: ValueText, Text: "github.com/atyrode/manifold"},
			}},
		},
		Filings: []FilingDraft{
			{Record: frontier.Ref{Type: frontier.EntityFinding, ID: "fnd-1"},
				Rationale: "the finding is about this repository"},
			{Record: frontier.Ref{Type: frontier.EntityHypothesis, ID: "hyp-1"},
				Rationale: "the candidate is about this repository"},
		},
		Reasoning: "32 sessions in 2 checkouts cite this repository",
		Sessions:  sessions,
	}
}

// namedRepository is a create plan for a second repository, so a test can
// hold several plans without repeating the fixture.
func namedRepository(proposalID, name, remote string, sessions int) TopicPlan {
	return TopicPlan{
		ProposalID: proposalID,
		Operation:  TopicCreate,
		Identity:   remote,
		Entity: &EntityDraft{
			Subject: NewSubject{Kind: EntityRepository, DisplayName: name},
			Binding: []FactInput{{
				Predicate: PredicateRepositoryRemote,
				Value:     FactValue{Kind: ValueText, Text: remote},
			}},
		},
		Reasoning: "the sessions in it are about " + name,
		Sessions:  sessions,
	}
}

// TestProposeTopicDedupesBySubjectMatterAndRefusesOneAlreadyBound is §4.13's
// two refusals, which are different things and must not be one.
//
// Two runs that met the same repository produce one thing for the operator to
// rule on however differently they worded their proposals, because a plan is
// keyed by the identity it would bind and not by its prose. And once the
// operator has accepted one, the identity binds an entity: a later plan for it
// is refused and the error names the entity, because the ledger already holds
// the thing and what the caller has is a filing, not a new subject.
func TestProposeTopicDedupesBySubjectMatterAndRefusesOneAlreadyBound(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if err := store.ProposeTopic(ctx, manifold("pro-1", 32)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	plan, found, err := store.TopicPlan(ctx, "pro-1")
	if err != nil || !found {
		t.Fatalf("TopicPlan: %v (found=%v)", err, found)
	}
	if plan.State != TopicPlanOpen || plan.Operation != TopicCreate {
		t.Fatalf("the plan reads %s/%s, want an open create", plan.State, plan.Operation)
	}

	reworded := manifold("pro-2", 40)
	reworded.Entity.Subject.DisplayName = "Manifold, the thing"
	reworded.Reasoning = "another run met the same remote"
	reworded.By = Provenance{RunID: "run-1"}
	if err := store.ProposeTopic(ctx, reworded); !isErr(err, ErrConflict) {
		t.Fatalf("second proposal: %v, want ErrConflict", err)
	}
	// A second plan on the same proposal record is refused too: the plan is
	// immutable, and a run that changes its mind publishes another
	// proposal.
	if err := store.ProposeTopic(ctx, manifold("pro-1", 33)); !isErr(err, ErrConflict) {
		t.Fatalf("re-planning one proposal: %v, want ErrConflict", err)
	}

	acceptance, err := store.ApplyTopicPlan(ctx, "pro-1", "operator", &recordingFiler{})
	if err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	err = store.ProposeTopic(ctx, manifold("pro-3", 99))
	if !isErr(err, ErrTopicBound) {
		t.Fatalf("proposal after acceptance: %v, want ErrTopicBound", err)
	}
	if !strings.Contains(err.Error(), acceptance.EntityID) {
		t.Errorf("the refusal is %q and does not name entity %s", err.Error(), acceptance.EntityID)
	}
}

// TestProposeTopicRefusesAnIdentityBoundByAFactAlone is the other half of
// "already bound", and the half an alias index cannot answer.
//
// An operator who created a repository by hand and asserted where it lives has
// bound the identity without ever attaching an alias for it. A run that only
// consulted aliases would offer to create a second subject for a repository
// the ledger already holds, which is the duplication §4.8's merge history
// exists to undo.
func TestProposeTopicRefusesAnIdentityBoundByAFactAlone(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	entity := mustEntity(t, store, EntityRepository, "dotfiles")
	if _, _, err := store.AssertFact(ctx, operatorFact(entity.ID, PredicateLocalPath,
		FactValue{Kind: ValueText, Text: "/home/alex/dotfiles/.git"}, clock.now())); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	plan := manifold("pro-1", 3)
	plan.Identity = "/home/alex/dotfiles/.git"
	plan.Entity = &EntityDraft{
		Subject: NewSubject{Kind: EntityRepository, DisplayName: "dotfiles"},
		Binding: []FactInput{{
			Predicate: PredicateLocalPath,
			Value:     FactValue{Kind: ValueText, Text: "/home/alex/dotfiles/.git"},
		}},
	}
	err := store.ProposeTopic(ctx, plan)
	if !isErr(err, ErrTopicBound) {
		t.Fatalf("ProposeTopic: %v, want ErrTopicBound", err)
	}
	if !strings.Contains(err.Error(), entity.ID) {
		t.Errorf("the refusal is %q and does not name entity %s", err.Error(), entity.ID)
	}
}

// TestApplyTopicPlanCreatesTheSubjectAndFilesEveryRecord is §4.13's
// acceptance: creating the entity and filing the records is one operator act.
//
// The filings' author is the plan's provenance rather than the accepting
// operator, which is the section's own distinction: what he accepted is the
// topic, not each record's membership, and a plan with no run behind it judged
// nothing — so its filings stay heuristic until the triage recipe revisits
// them.
func TestApplyTopicPlanCreatesTheSubjectAndFilesEveryRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if err := store.ProposeTopic(ctx, manifold("pro-1", 32)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	filer := &recordingFiler{}
	acceptance, err := store.ApplyTopicPlan(ctx, "pro-1", "operator", filer)
	if err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	if acceptance.Operation != TopicCreate {
		t.Errorf("the acceptance reports %s", acceptance.Operation)
	}

	entity, err := store.Entity(ctx, acceptance.EntityID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if entity.Kind != EntityRepository || entity.Payload.DisplayName != "manifold" {
		t.Errorf("entity is %s/%q, want the proposed repository", entity.Kind, entity.Payload.DisplayName)
	}

	// Every name the plan offered answers for the subject, and so does the
	// identity it is bound by — that alias is what makes the next plan for
	// this repository refusable.
	for _, name := range []string{
		"github.com/atyrode/manifold", "manifold",
		"/home/alex/manifold", "/home/alex/wt/manifold-fix",
	} {
		resolved, err := store.ResolveSubject(ctx, name)
		if err != nil {
			t.Errorf("ResolveSubject(%q): %v", name, err)
			continue
		}
		if resolved != acceptance.EntityID {
			t.Errorf("%q resolves to %s, want %s", name, resolved, acceptance.EntityID)
		}
	}

	facts, err := store.Facts(ctx, FactQuery{SubjectID: acceptance.EntityID,
		Predicate: PredicateRepositoryRemote})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("the subject holds %d repository-remote facts, want the binding", len(facts))
	}
	bound := facts[0]
	if bound.Status != FactActive {
		t.Errorf("the binding fact is %s, want active: an operator accepted it", bound.Status)
	}
	if bound.Authority.Kind != AuthorityOperator || bound.Authority.ID != "operator" {
		t.Errorf("the binding fact is attributed to %s/%s, want the accepting operator",
			bound.Authority.Kind, bound.Authority.ID)
	}
	if bound.Value.Text != "github.com/atyrode/manifold" {
		t.Errorf("the binding fact says %q", bound.Value.Text)
	}

	if len(filer.filed) != 2 {
		t.Fatalf("the filer saw %d records, want both the plan named", len(filer.filed))
	}
	for _, filing := range filer.filed {
		if filing.EntityID != acceptance.EntityID {
			t.Errorf("record %s was filed under %s, want the new topic", filing.Record.ID, filing.EntityID)
		}
		if filing.Author != frontier.FilingHeuristic || !filing.Heuristic || filing.AuthorID != "" {
			t.Errorf("record %s was filed as %s/%q (heuristic=%v), want an unattributed heuristic filing",
				filing.Record.ID, filing.Author, filing.AuthorID, filing.Heuristic)
		}
		if filing.Rationale == "" {
			t.Errorf("record %s was filed with no rationale", filing.Record.ID)
		}
	}

	applied, _, err := store.TopicPlan(ctx, "pro-1")
	if err != nil {
		t.Fatalf("TopicPlan: %v", err)
	}
	if applied.State != TopicPlanApplied || applied.EntityID != acceptance.EntityID {
		t.Errorf("the plan reads back as %s/%q after acceptance", applied.State, applied.EntityID)
	}
	if _, err := store.ApplyTopicPlan(ctx, "pro-1", "operator", filer); !isErr(err, ErrAlreadyDecided) {
		t.Errorf("second acceptance: %v, want ErrAlreadyDecided", err)
	}
	if err := store.DeclineTopicPlan(ctx, "pro-1", "operator", "changed my mind"); !isErr(err, ErrAlreadyDecided) {
		t.Errorf("decline after acceptance: %v, want ErrAlreadyDecided", err)
	}
}

// TestApplyTopicPlanRunProposalFilesAsTheRun is the other side of authorship:
// a run that proposed a topic judged that each record it named is about it, so
// the filing is the run's work and is not labelled heuristic.
func TestApplyTopicPlanRunProposalFilesAsTheRun(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	plan := manifold("pro-1", 4)
	plan.By = Provenance{RunID: "run-7", RecipeID: "babel-files-its-output"}
	if err := store.ProposeTopic(ctx, plan); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	filer := &recordingFiler{}
	if _, err := store.ApplyTopicPlan(ctx, "pro-1", "operator", filer); err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	for _, filing := range filer.filed {
		if filing.Author != frontier.FilingRun || filing.AuthorID != "run-7" || filing.Heuristic {
			t.Errorf("record %s was filed as %s/%q (heuristic=%v), want run-7's own filing",
				filing.Record.ID, filing.Author, filing.AuthorID, filing.Heuristic)
		}
	}
}

// TestApplyTopicPlanReportsAFilingFailureAndKeepsWhatItCreated pins the
// consequence of the two stores being two handles on one file.
//
// The ledger's half is one transaction and the filings follow it, so a filer
// that refuses leaves exactly this: the entity, its aliases, its binding facts
// and the ruling are durable, the records are unfiled, and the error says so
// while the returned acceptance still names what exists. That is the benign
// direction — §4.13 makes unfiled the triage backlog — and the test exists so
// the alternative is never introduced quietly.
func TestApplyTopicPlanReportsAFilingFailureAndKeepsWhatItCreated(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if err := store.ProposeTopic(ctx, manifold("pro-1", 9)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	filer := &recordingFiler{failOn: "fnd-1"}
	acceptance, err := store.ApplyTopicPlan(ctx, "pro-1", "operator", filer)
	if err == nil {
		t.Fatal("ApplyTopicPlan: no error, want the filer's refusal reported")
	}
	if acceptance.EntityID == "" {
		t.Fatal("the acceptance reports no entity; a caller cannot see what exists")
	}
	if len(filer.filed) != 0 {
		t.Errorf("the filer accepted %d records after refusing the first", len(filer.filed))
	}
	if _, err := store.Entity(ctx, acceptance.EntityID); err != nil {
		t.Errorf("the entity the acceptance created is gone: %v", err)
	}
	applied, _, err := store.TopicPlan(ctx, "pro-1")
	if err != nil {
		t.Fatalf("TopicPlan: %v", err)
	}
	if applied.State != TopicPlanApplied {
		t.Errorf("the plan is %s, want applied: the ledger's half committed", applied.State)
	}
}

// TestApplyTopicPlanSplitsATopicAndMovesItsRecords is §4.13's split: one name
// covered two things, and the records that belong to the second move with it.
//
// The parent is replaced by two parts rather than carved into, which is
// §4.8's own shape, and the new part carries the identity, the name and the
// binding the plan proposed — without which the split would have produced an
// entity nobody can resolve by the thing it names.
func TestApplyTopicPlanSplitsATopicAndMovesItsRecords(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	parent := mustEntity(t, store, EntityRepository, "manifold")
	plan := TopicPlan{
		ProposalID: "pro-split",
		Operation:  TopicSplit,
		Targets:    []string{parent.ID},
		Identity:   "github.com/atyrode/manifold-ui",
		Entity: &EntityDraft{
			Subject: NewSubject{Kind: EntityRepository, DisplayName: "manifold-ui"},
			Binding: []FactInput{{
				Predicate: PredicateRepositoryRemote,
				Value:     FactValue{Kind: ValueText, Text: "github.com/atyrode/manifold-ui"},
			}},
		},
		Filings: []FilingDraft{{
			Record:    frontier.Ref{Type: frontier.EntityFinding, ID: "fnd-ui"},
			Rationale: "the finding is about the interface, not the engine",
		}},
		Reasoning: "the interface and the engine are two projects under one name",
		By:        Provenance{RunID: "run-9"},
	}
	if err := store.ProposeTopic(ctx, plan); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}

	filer := &recordingFiler{}
	acceptance, err := store.ApplyTopicPlan(ctx, "pro-split", "operator", filer)
	if err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	if acceptance.Resolution == nil || acceptance.Resolution.Kind != ResolutionSplit {
		t.Fatalf("the acceptance recorded %+v, want a split resolution", acceptance.Resolution)
	}
	if acceptance.EntityID == "" || acceptance.EntityID == parent.ID {
		t.Fatalf("the split produced %q, want a new part", acceptance.EntityID)
	}
	created, err := store.Entity(ctx, acceptance.EntityID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if created.Payload.DisplayName != "manifold-ui" {
		t.Errorf("the new part is %q", created.Payload.DisplayName)
	}
	resolved, err := store.ResolveSubject(ctx, "github.com/atyrode/manifold-ui")
	if err != nil {
		t.Fatalf("ResolveSubject: %v", err)
	}
	if resolved != acceptance.EntityID {
		t.Errorf("the new identity resolves to %s, want the part %s", resolved, acceptance.EntityID)
	}
	binding, bound, err := store.EntityBinding(ctx, acceptance.EntityID)
	if err != nil || !bound {
		t.Fatalf("EntityBinding: %v (bound=%v)", err, bound)
	}
	if binding.Remote != "github.com/atyrode/manifold-ui" {
		t.Errorf("the part is bound to %+v", binding)
	}
	// The parent stops speaking for itself, which is what tells a reader to
	// look at the parts.
	after, err := store.Entity(ctx, parent.ID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if after.Role != RoleSplit {
		t.Errorf("the parent is %s after the split, want split", after.Role)
	}
	if len(filer.filed) != 1 || filer.filed[0].EntityID != acceptance.EntityID {
		t.Fatalf("the filer saw %+v, want the record moved to the new part", filer.filed)
	}
	if filer.filed[0].Author != frontier.FilingRun || filer.filed[0].AuthorID != "run-9" {
		t.Errorf("the moved record is attributed to %s/%q, want the run that judged it",
			filer.filed[0].Author, filer.filed[0].AuthorID)
	}
}

// TestApplyTopicPlanMergesTwoTopics is §4.13's merge: two names turn out to be
// one thing, and the filings follow without a pass over the frontier.
//
// Nothing rewrites an `about` edge, and that is the point: an edge names an
// entity id and every consumer resolves it through the merge history, so the
// records under the folded identity are the survivor's afterwards because the
// ledger says the two are one thing.
func TestApplyTopicPlanMergesTwoTopics(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	from := mustEntity(t, store, EntityRepository, "manifold-old")
	into := mustEntity(t, store, EntityRepository, "manifold")
	if err := store.ProposeTopic(ctx, TopicPlan{
		ProposalID: "pro-merge",
		Operation:  TopicMerge,
		Targets:    []string{from.ID, into.ID},
		Reasoning:  "both names are the same checkout under two remotes",
		By:         Provenance{RunID: "run-3"},
	}); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	acceptance, err := store.ApplyTopicPlan(ctx, "pro-merge", "operator", nil)
	if err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	if acceptance.Resolution == nil || acceptance.Resolution.Kind != ResolutionMerge {
		t.Fatalf("the acceptance recorded %+v, want a merge resolution", acceptance.Resolution)
	}
	if acceptance.EntityID != "" {
		t.Errorf("a merge created entity %q", acceptance.EntityID)
	}
	canonical, err := store.Resolve(ctx, from.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if canonical != into.ID {
		t.Errorf("the folded identity resolves to %s, want %s", canonical, into.ID)
	}
	topics, err := store.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if len(topics) != 1 || topics[0].Entity.ID != into.ID {
		t.Errorf("the ledger lists %d topics after the merge, want only the survivor", len(topics))
	}
}

// TestApplyTopicPlanRetiresATopic is §4.13's retirement: a name that should
// never have existed stops being a place records live, and nothing is deleted.
func TestApplyTopicPlanRetiresATopic(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	scratch := mustEntity(t, store, EntityRepository, "tmp")
	const reason = "a scratch directory is a locator, not a topic"
	if err := store.ProposeTopic(ctx, TopicPlan{
		ProposalID: "pro-retire",
		Operation:  TopicRetire,
		Targets:    []string{scratch.ID},
		Reasoning:  reason,
		By:         Provenance{RunID: "run-4"},
	}); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	acceptance, err := store.ApplyTopicPlan(ctx, "pro-retire", "operator", nil)
	if err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	if len(acceptance.Facts) != 1 || acceptance.Facts[0].Value.Enum != LifecycleRetired {
		t.Fatalf("the acceptance recorded %+v, want one retirement fact", acceptance.Facts)
	}
	if acceptance.Facts[0].Payload.Note != reason {
		t.Errorf("the retirement's reason is %q, want the plan's own words",
			acceptance.Facts[0].Payload.Note)
	}
	if acceptance.Facts[0].Authority.ID != "operator" {
		t.Errorf("the retirement is attributed to %q", acceptance.Facts[0].Authority.ID)
	}
	retired, err := store.EntityRetired(ctx, scratch.ID)
	if err != nil {
		t.Fatalf("EntityRetired: %v", err)
	}
	if !retired {
		t.Error("the topic is not retired after its proposal was accepted")
	}
	// Nothing is deleted: the entity is still readable, and the topic
	// listing simply stops offering it as a place to file.
	if _, err := store.Entity(ctx, scratch.ID); err != nil {
		t.Errorf("the retired entity is gone: %v", err)
	}
	topics, err := store.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics: %v", err)
	}
	if len(topics) != 0 {
		t.Errorf("the retired topic is still listed: %+v", topics)
	}
}

// TestApplyTopicPlanRefusesATargetTheLedgerHasMovedPast is the refusal a plan
// needs because the operator rules later than the run proposed.
//
// A topic that has been merged away or retired since the proposal was
// published is not the thing the plan reasoned about, and applying against it
// would either fail deep inside §4.8's checks or act on an identity that no
// longer speaks for anything. The refusal names the state, which is what tells
// the operator to let Babel look again.
func TestApplyTopicPlanRefusesATargetTheLedgerHasMovedPast(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	folded := mustEntity(t, store, EntityRepository, "manifold-old")
	survivor := mustEntity(t, store, EntityRepository, "manifold")
	scratch := mustEntity(t, store, EntityRepository, "tmp")

	if err := store.ProposeTopic(ctx, TopicPlan{
		ProposalID: "pro-retire-folded",
		Operation:  TopicRetire,
		Targets:    []string{folded.ID},
		Reasoning:  "this name was a mistake",
	}); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	if err := store.ProposeTopic(ctx, TopicPlan{
		ProposalID: "pro-retire-twice",
		Operation:  TopicRetire,
		Targets:    []string{scratch.ID},
		Reasoning:  "a scratch directory is not a topic",
	}); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}

	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{folded.ID}, TargetID: survivor.ID,
		Actor: "operator", Reason: "they were one repository",
	}); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}
	if err := store.RetireEntity(ctx, scratch.ID, "operator", "already handled"); err != nil {
		t.Fatalf("RetireEntity: %v", err)
	}

	merged := store.mustRefuse(ctx, t, "pro-retire-folded")
	if !strings.Contains(merged.Error(), survivor.ID) {
		t.Errorf("the refusal is %q and does not name what the target became", merged.Error())
	}
	gone := store.mustRefuse(ctx, t, "pro-retire-twice")
	if !strings.Contains(gone.Error(), "retired") {
		t.Errorf("the refusal is %q and does not say the topic was retired", gone.Error())
	}
	// Refusing is not ruling: both plans are still open, so the operator
	// can decline them once Babel has looked again.
	for _, id := range []string{"pro-retire-folded", "pro-retire-twice"} {
		plan, _, err := store.TopicPlan(ctx, id)
		if err != nil {
			t.Fatalf("TopicPlan(%s): %v", id, err)
		}
		if plan.State != TopicPlanOpen {
			t.Errorf("plan %s is %s after a refused application", id, plan.State)
		}
	}
}

// mustRefuse applies a plan expecting the ledger to refuse it as stale.
func (s *Store) mustRefuse(ctx context.Context, t *testing.T, proposalID string) error {
	t.Helper()
	_, err := s.ApplyTopicPlan(ctx, proposalID, "operator", nil)
	if !isErr(err, ErrConflict) {
		t.Fatalf("ApplyTopicPlan(%s): %v, want ErrConflict", proposalID, err)
	}
	return err
}

// TestProposeTopicRefusesAPlanThatCouldNeverBeApplied is the shape check, and
// it is a refusal at the door rather than a branch in the application: a merge
// carrying a proposed entity and a create naming an existing topic are both
// plans no acceptance could perform.
func TestProposeTopicRefusesAPlanThatCouldNeverBeApplied(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	entity := mustEntity(t, store, EntityRepository, "manifold")

	merge := TopicPlan{
		ProposalID: "pro-bad",
		Operation:  TopicMerge,
		Targets:    []string{entity.ID},
		Reasoning:  "one target is not a merge",
	}
	for _, probe := range []struct {
		name string
		plan func(TopicPlan) TopicPlan
	}{
		{"a merge with one target", func(p TopicPlan) TopicPlan { return p }},
		{"a merge that folds a topic into itself", func(p TopicPlan) TopicPlan {
			p.Targets = []string{entity.ID, entity.ID}
			return p
		}},
		{"a merge that creates an entity", func(p TopicPlan) TopicPlan {
			p.Targets = []string{entity.ID, entity.ID + "-other"}
			p.Entity = &EntityDraft{Subject: NewSubject{Kind: EntityRepository, DisplayName: "x"}}
			return p
		}},
		{"a create that names an existing topic", func(p TopicPlan) TopicPlan {
			p.Operation = TopicCreate
			p.Identity = "github.com/atyrode/x"
			p.Entity = &EntityDraft{Subject: NewSubject{Kind: EntityRepository, DisplayName: "x"}}
			return p
		}},
		{"a create with no identity", func(p TopicPlan) TopicPlan {
			p.Operation = TopicCreate
			p.Targets = nil
			p.Entity = &EntityDraft{Subject: NewSubject{Kind: EntityRepository, DisplayName: "x"}}
			return p
		}},
		{"a retirement that files records", func(p TopicPlan) TopicPlan {
			p.Operation = TopicRetire
			p.Filings = []FilingDraft{{
				Record:    frontier.Ref{Type: frontier.EntityFinding, ID: "fnd-1"},
				Rationale: "why",
			}}
			return p
		}},
		{"an operation outside the vocabulary", func(p TopicPlan) TopicPlan {
			p.Operation = "rename"
			return p
		}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			if err := store.ProposeTopic(ctx, probe.plan(merge)); !isErr(err, ErrInvalidValue) {
				t.Fatalf("ProposeTopic: %v, want ErrInvalidValue", err)
			}
		})
	}

	// A target the ledger does not hold is refused by name rather than
	// carried until the operator rules on it.
	if err := store.ProposeTopic(ctx, TopicPlan{
		ProposalID: "pro-absent",
		Operation:  TopicRetire,
		Targets:    []string{"ent_absent"},
		Reasoning:  "it should not exist",
	}); !isErr(err, ErrUnknownRecord) {
		t.Fatalf("a plan about an unknown topic: %v, want ErrUnknownRecord", err)
	}
}

// TestDeclineTopicPlanSuppressesUntilMoreEvidenceStandsBehindIt is §4.13's
// suppression in the term a topic is measured in.
//
// A refusal has to stay refused: re-proposing the same thing is the repetition
// suppression exists to stop. What lifts it is materially new evidence, and
// for a repository that is more sessions than there were when the operator
// refused — which is why the count is recorded on the plan rather than
// recomputed later.
func TestDeclineTopicPlanSuppressesUntilMoreEvidenceStandsBehindIt(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if err := store.ProposeTopic(ctx, manifold("pro-1", 2)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	if err := store.DeclineTopicPlan(ctx, "pro-1", "operator", ""); !isErr(err, ErrInvalidValue) {
		t.Errorf("decline with no reason: %v, want ErrInvalidValue", err)
	}
	const reason = "two scratch sessions are not a project"
	if err := store.DeclineTopicPlan(ctx, "pro-1", "operator", reason); err != nil {
		t.Fatalf("DeclineTopicPlan: %v", err)
	}

	if err := store.ProposeTopic(ctx, manifold("pro-2", 2)); !isErr(err, ErrSuppressed) {
		t.Fatalf("re-proposal with the same evidence: %v, want ErrSuppressed", err)
	}
	if err := store.ProposeTopic(ctx, manifold("pro-3", 1)); !isErr(err, ErrSuppressed) {
		t.Fatalf("re-proposal with less evidence: %v, want ErrSuppressed", err)
	}
	if err := store.ProposeTopic(ctx, manifold("pro-4", 9)); err != nil {
		t.Fatalf("re-proposal with more sessions: %v", err)
	}

	// The refusal and its reason stay readable: §4.13 has the triage recipe
	// read why topics were declined as evidence for its next proposals.
	declined, err := store.DeclinedTopicPlans(ctx, 0)
	if err != nil {
		t.Fatalf("DeclinedTopicPlans: %v", err)
	}
	if len(declined) != 1 || declined[0].ProposalID != "pro-1" {
		t.Fatalf("the declined plans are %+v, want the one he refused", declined)
	}
	if declined[0].Reason != reason || declined[0].RuledBy != "operator" {
		t.Errorf("the refusal reads back as %q by %q", declined[0].Reason, declined[0].RuledBy)
	}
	if declined[0].State != TopicPlanDeclined {
		t.Errorf("the declined plan is %s", declined[0].State)
	}
}

// TestOpenTopicPlansListsOnlyWhatAwaitsTheOperator keeps the rail honest: an
// applied plan is an entity and a declined one is a refusal, and offering
// either again would be asking for a ruling that has been given.
func TestOpenTopicPlansListsOnlyWhatAwaitsTheOperator(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	if err := store.ProposeTopic(ctx, manifold("pro-accept", 32)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	if err := store.ProposeTopic(ctx,
		namedRepository("pro-decline", "dotfiles", "github.com/atyrode/dotfiles", 3)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}
	if err := store.ProposeTopic(ctx,
		namedRepository("pro-open", "nixos", "github.com/atyrode/nixos", 7)); err != nil {
		t.Fatalf("ProposeTopic: %v", err)
	}

	acceptance, err := store.ApplyTopicPlan(ctx, "pro-accept", "operator", &recordingFiler{})
	if err != nil {
		t.Fatalf("ApplyTopicPlan: %v", err)
	}
	if err := store.DeclineTopicPlan(ctx, "pro-decline", "operator", "not a project"); err != nil {
		t.Fatalf("DeclineTopicPlan: %v", err)
	}

	open, err := store.OpenTopicPlans(ctx)
	if err != nil {
		t.Fatalf("OpenTopicPlans: %v", err)
	}
	if len(open) != 1 || open[0].Name() != "nixos" {
		t.Fatalf("the rail offers %d plans, want only nixos: %+v", len(open), open)
	}
	if open[0].Sessions != 7 || open[0].Identity != "github.com/atyrode/nixos" {
		t.Errorf("the plan reads back as %+v", open[0])
	}
	if open[0].ProposalID != "pro-open" {
		t.Errorf("the plan is keyed on %q, want the proposal record", open[0].ProposalID)
	}

	// The applied one still says what it produced, which is what makes an
	// accepted proposal traceable to the subject it created.
	topics, err := store.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics: %v", err)
	}
	var named string
	for _, topic := range topics {
		if topic.Entity.ID == acceptance.EntityID {
			named = topic.ProposalID
		}
	}
	if named != "pro-accept" {
		t.Errorf("the created topic names proposal %q, want the one he accepted", named)
	}
}

// TestTopicsListsSubjectsInStanceOrder is the topic page's own order, which is
// what the operator is doing rather than when Babel recorded things.
func TestTopicsListsSubjectsInStanceOrder(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	working := mustEntity(t, store, EntityRepository, "alpha")
	watching := mustEntity(t, store, EntityRepository, "beta")
	unsaid := mustEntity(t, store, EntityRepository, "gamma")
	notNow := mustEntity(t, store, EntityRepository, "delta")
	excluded := mustEntity(t, store, EntityRepository, "epsilon")
	retired := mustEntity(t, store, EntityRepository, "zeta")

	for _, set := range []struct {
		id    string
		state string
	}{
		{working.ID, InterestWorking},
		{watching.ID, InterestWatching},
		{notNow.ID, InterestNotNow},
		{excluded.ID, InterestExcluded},
	} {
		if err := store.SetInterest(ctx, set.id, "operator", set.state, "because"); err != nil {
			t.Fatalf("SetInterest(%s): %v", set.state, err)
		}
	}
	if err := store.RetireEntity(ctx, retired.ID, "operator", "created by mistake"); err != nil {
		t.Fatalf("RetireEntity: %v", err)
	}

	topics, err := store.Topics(ctx)
	if err != nil {
		t.Fatalf("Topics: %v", err)
	}
	var order []string
	for _, topic := range topics {
		order = append(order, topic.Entity.Payload.DisplayName)
	}
	want := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("topics read %v, want %v with the retired one absent", order, want)
	}
	// An unsaid stance sits in the middle rather than at the bottom, and it
	// reads as unsaid rather than as a weak yes.
	interest, err := store.EntityInterest(ctx, unsaid.ID)
	if err != nil {
		t.Fatalf("EntityInterest: %v", err)
	}
	if interest.State != "" {
		t.Errorf("a subject nobody has spoken about reads as %q", interest.State)
	}
}

// TestAcceptPlanWithCreatesTheSubjectAndFilesUnderIt is the same act reached
// through §4.8's answer plan rather than through a topic proposal.
//
// It matters that the two paths agree. An interpretation that reads "this is a
// thing, and these records are about it" proposes create-entity and
// file-records; nothing happens until the operator accepts, and then the
// subject is his and so are the filings. A build where only the topic page
// could create a topic would make the inbox the second-class surface §4.8's
// plan vocabulary exists to prevent.
func TestAcceptPlanWithCreatesTheSubjectAndFilesUnderIt(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	subject := mustEntity(t, store, EntityProject, "a project")

	question, err := store.Ask(ctx, QuestionInput{
		Kind:              KindResolveEntity,
		Class:             ClassMaintenance,
		Sensitivity:       SensitivityRoutine,
		ExpectedAuthority: AuthorityOperator,
		TargetEntityIDs:   []string{subject.ID},
		MaterialEvidence:  []string{"observation-1"},
		Payload: QuestionPayload{
			Prompt:   "what is the repository these sessions keep citing?",
			WhyAsked: "a run met a name the ledger does not hold",
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID,
		Author:     "operator",
		At:         clock.now(),
		Outcome:    OutcomeAnswered,
		Text:       "that is manifold, my own repository",
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}
	plan, _, err := store.RecordPlan(ctx, PlanInput{
		QuestionID:         question.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 1,
		Summary:            "name the repository and file the records about it",
		Kinds:              []ActionKind{ActionCreateEntity, ActionFileRecords},
		Actions: []ActionPayload{
			{
				Rationale: "the operator named it",
				Entity: &EntityDraft{
					Subject: NewSubject{
						Kind:        EntityRepository,
						DisplayName: "manifold",
						Notes:       "the operator named it in his answer",
						Aliases: []AliasInput{{
							Kind:    AliasIdentifier,
							Payload: AliasPayload{Value: "github.com/atyrode/manifold"},
						}},
					},
					Binding: []FactInput{{
						Predicate: PredicateRepositoryRemote,
						Value:     FactValue{Kind: ValueText, Text: "github.com/atyrode/manifold"},
					}},
				},
			},
			{
				Rationale: "these are the records the answer was about",
				Filings: []FilingDraft{{
					Record:    frontier.Ref{Type: frontier.EntityFinding, ID: "fnd-9"},
					Rationale: "the finding is about that repository",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	filer := &recordingFiler{}
	_, application, err := store.AcceptPlanWith(ctx,
		AcceptanceInput{PlanID: plan.ID, Actor: "operator"}, filer)
	if err != nil {
		t.Fatalf("AcceptPlanWith: %v", err)
	}
	if len(application.EntityIDs) != 1 {
		t.Fatalf("the acceptance created %d entities, want the one it proposed", len(application.EntityIDs))
	}
	created := application.EntityIDs[0]
	resolved, err := store.ResolveSubject(ctx, "github.com/atyrode/manifold")
	if err != nil {
		t.Fatalf("ResolveSubject: %v", err)
	}
	if resolved != created {
		t.Errorf("the identity resolves to %s, want the created subject %s", resolved, created)
	}
	if len(application.FactIDs) != 1 {
		t.Errorf("the acceptance applied %d facts, want the binding", len(application.FactIDs))
	}
	if len(filer.filed) != 1 || filer.filed[0].EntityID != created {
		t.Fatalf("the filer saw %+v, want the record filed under the new subject", filer.filed)
	}
	// The operator accepted the plan, so a filing it carried without an
	// author is his act — unlike a topic proposal's filings, which belong
	// to whatever proposed them.
	if filer.filed[0].Author != frontier.FilingOperator || filer.filed[0].AuthorID != "operator" {
		t.Errorf("the filing is attributed to %s/%q, want the accepting operator",
			filer.filed[0].Author, filer.filed[0].AuthorID)
	}
	if len(application.Filings) != 1 {
		t.Errorf("the application reports %d filings", len(application.Filings))
	}
}
