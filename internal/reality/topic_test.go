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
	// the half-applied acceptance §4.13's two stores make possible.
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

// manifold is the proposal every test in this file starts from: one
// repository, bound by its remote, seen at two worktrees, with two records
// that would be filed under it.
func manifold(sessions int) TopicProposal {
	return TopicProposal{
		Name: "manifold",
		Kind: EntityRepository,
		Aliases: []AliasInput{
			{Kind: AliasRepository, Payload: AliasPayload{Value: "github.com/atyrode/manifold"}},
			{Kind: AliasPath, Payload: AliasPayload{Value: "/home/alex/manifold"}},
			{Kind: AliasPath, Payload: AliasPayload{Value: "/home/alex/wt/manifold-fix"}},
		},
		Binding: []FactInput{{
			Predicate: PredicateRepositoryRemote,
			Value:     FactValue{Kind: ValueText, Text: "github.com/atyrode/manifold"},
		}},
		Reasoning: "32 sessions in 2 checkouts cite this repository",
		Records: []frontier.Ref{
			{Type: frontier.EntityFinding, ID: "fnd-1"},
			{Type: frontier.EntityHypothesis, ID: "hyp-1"},
		},
		Identity: "github.com/atyrode/manifold",
		Sessions: sessions,
	}
}

// TestAskTopicDedupesByIdentityAndRefusesOneAlreadyBound is §4.13's two
// refusals, which are different things and must not be one.
//
// Two runs that met the same repository raise one question however differently
// they worded it, because a topic proposal is keyed by the identity it
// proposes and not by its prose. And once the operator has accepted it, the
// identity binds an entity: a second proposal for it is refused and the error
// names the entity, because the ledger already holds the thing and what the
// caller has is a filing, not a new subject.
func TestAskTopicDedupesByIdentityAndRefusesOneAlreadyBound(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	first, err := store.AskTopic(ctx, manifold(32), Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	if first.Kind != QuestionTopic || first.State != QuestionOpen {
		t.Fatalf("question is %s/%s, want an open topic question", first.Kind, first.State)
	}
	if len(first.TargetEntityIDs) != 0 {
		t.Errorf("a topic question targets %v, want nothing: its subject does not exist yet",
			first.TargetEntityIDs)
	}

	reworded := manifold(40)
	reworded.Name = "Manifold, the thing"
	reworded.Reasoning = "another run met the same remote"
	if _, err := store.AskTopic(ctx, reworded, Provenance{RunID: "run-1"}); !isErr(err, ErrDuplicateQuestion) {
		t.Fatalf("second ask: %v, want ErrDuplicateQuestion", err)
	}

	filer := &recordingFiler{}
	acceptance, err := store.AcceptTopic(ctx, first.ID, "operator", filer)
	if err != nil {
		t.Fatalf("AcceptTopic: %v", err)
	}
	err = func() error { _, err := store.AskTopic(ctx, manifold(99), Provenance{RunID: "run-2"}); return err }()
	if !isErr(err, ErrTopicBound) {
		t.Fatalf("ask after acceptance: %v, want ErrTopicBound", err)
	}
	if !strings.Contains(err.Error(), acceptance.EntityID) {
		t.Errorf("the refusal is %q and does not name entity %s", err.Error(), acceptance.EntityID)
	}
}

// TestAskTopicRefusesAnIdentityBoundByAFactAlone is the other half of
// "already bound", and the half an alias index cannot answer.
//
// An operator who created a repository by hand and asserted where it lives has
// bound the identity without ever attaching an alias for it. A seeder that
// only consulted aliases would offer to create a second subject for a
// repository the ledger already holds, which is the duplication §4.8's merge
// history exists to undo.
func TestAskTopicRefusesAnIdentityBoundByAFactAlone(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)

	entity := mustEntity(t, store, EntityRepository, "dotfiles")
	if _, _, err := store.AssertFact(ctx, operatorFact(entity.ID, PredicateLocalPath,
		FactValue{Kind: ValueText, Text: "/home/alex/dotfiles/.git"}, clock.now())); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	proposal := manifold(3)
	proposal.Name = "dotfiles"
	proposal.Identity = "/home/alex/dotfiles/.git"
	proposal.Aliases = nil
	proposal.Binding = []FactInput{{
		Predicate: PredicateLocalPath,
		Value:     FactValue{Kind: ValueText, Text: "/home/alex/dotfiles/.git"},
	}}
	_, err := store.AskTopic(ctx, proposal, Provenance{Actor: "topic-seed"})
	if !isErr(err, ErrTopicBound) {
		t.Fatalf("AskTopic: %v, want ErrTopicBound", err)
	}
	if !strings.Contains(err.Error(), entity.ID) {
		t.Errorf("the refusal is %q and does not name entity %s", err.Error(), entity.ID)
	}
}

// TestAcceptTopicCreatesTheSubjectAndFilesEveryRecord is §4.13's acceptance:
// creating the entity and filing the records is one operator act.
//
// The filings' author is the proposal's provenance rather than the accepting
// operator, which is the section's own distinction: what he accepted is the
// topic, not each record's membership, and a proposal derived from repository
// identity alone judged nothing — so its filings stay heuristic until the
// triage recipe revisits them.
func TestAcceptTopicCreatesTheSubjectAndFilesEveryRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	question, err := store.AskTopic(ctx, manifold(32), Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	filer := &recordingFiler{}
	acceptance, err := store.AcceptTopic(ctx, question.ID, "operator", filer)
	if err != nil {
		t.Fatalf("AcceptTopic: %v", err)
	}

	entity, err := store.Entity(ctx, acceptance.EntityID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if entity.Kind != EntityRepository || entity.Payload.DisplayName != "manifold" {
		t.Errorf("entity is %s/%q, want the proposed repository", entity.Kind, entity.Payload.DisplayName)
	}

	// Every name the proposal offered answers for the subject, and so does
	// the identity it is bound by — that alias is what makes the next
	// proposal of this repository refusable.
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
		t.Fatalf("the filer saw %d records, want both the proposal named", len(filer.filed))
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

	answered, err := store.Question(ctx, question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if answered.State != QuestionAnswered {
		t.Errorf("the question is %s after acceptance, want answered", answered.State)
	}
	if _, err := store.AcceptTopic(ctx, question.ID, "operator", filer); !isErr(err, ErrAlreadyDecided) {
		t.Errorf("second acceptance: %v, want ErrAlreadyDecided", err)
	}
}

// TestAcceptTopicRunProposalFilesAsTheRun is the other side of authorship: a
// run that proposed a topic judged that each record it named is about it, so
// the filing is the run's work and is not labelled heuristic.
func TestAcceptTopicRunProposalFilesAsTheRun(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	question, err := store.AskTopic(ctx, manifold(4), Provenance{RunID: "run-7", RecipeID: "babel-files-its-output"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	filer := &recordingFiler{}
	if _, err := store.AcceptTopic(ctx, question.ID, "operator", filer); err != nil {
		t.Fatalf("AcceptTopic: %v", err)
	}
	for _, filing := range filer.filed {
		if filing.Author != frontier.FilingRun || filing.AuthorID != "run-7" || filing.Heuristic {
			t.Errorf("record %s was filed as %s/%q (heuristic=%v), want run-7's own filing",
				filing.Record.ID, filing.Author, filing.AuthorID, filing.Heuristic)
		}
	}
}

// TestAcceptTopicReportsAFilingFailureAndKeepsWhatItCreated pins the
// consequence of the two stores being two handles on one file.
//
// The ledger's half is one transaction and the filings follow it, so a filer
// that refuses leaves exactly this: the entity, its aliases, its binding facts
// and the answered question are durable, the records are unfiled, and the
// error says so while the returned acceptance still names what exists. That is
// the benign direction — §4.13 makes unfiled the triage backlog — and the test
// exists so the alternative is never introduced quietly.
func TestAcceptTopicReportsAFilingFailureAndKeepsWhatItCreated(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	question, err := store.AskTopic(ctx, manifold(9), Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	filer := &recordingFiler{failOn: "fnd-1"}
	acceptance, err := store.AcceptTopic(ctx, question.ID, "operator", filer)
	if err == nil {
		t.Fatal("AcceptTopic: no error, want the filer's refusal reported")
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
	answered, err := store.Question(ctx, question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if answered.State != QuestionAnswered {
		t.Errorf("the question is %s, want answered: the ledger's half committed", answered.State)
	}
}

// TestDeclineTopicSuppressesUntilMoreEvidenceStandsBehindIt is §4.13's
// suppression in the term a topic is measured in.
//
// A refusal has to stay refused: re-asking the same proposal is the repetition
// suppression exists to stop. What lifts it is materially new evidence, and
// for a topic seeded from the catalog that is more sessions than there were
// when the operator refused — which is why the count is recorded on the
// question rather than recomputed later.
func TestDeclineTopicSuppressesUntilMoreEvidenceStandsBehindIt(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	first, err := store.AskTopic(ctx, manifold(2), Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	if err := store.DeclineTopic(ctx, first.ID, "operator", ""); !isErr(err, ErrInvalidValue) {
		t.Errorf("decline with no reason: %v, want ErrInvalidValue", err)
	}
	const reason = "two scratch sessions are not a project"
	if err := store.DeclineTopic(ctx, first.ID, "operator", reason); err != nil {
		t.Fatalf("DeclineTopic: %v", err)
	}

	if _, err := store.AskTopic(ctx, manifold(2), Provenance{Actor: "topic-seed"}); !isErr(err, ErrSuppressed) {
		t.Fatalf("re-ask with the same evidence: %v, want ErrSuppressed", err)
	}
	if _, err := store.AskTopic(ctx, manifold(1), Provenance{Actor: "topic-seed"}); !isErr(err, ErrSuppressed) {
		t.Fatalf("re-ask with less evidence: %v, want ErrSuppressed", err)
	}

	revived, err := store.AskTopic(ctx, manifold(9), Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("re-ask with more sessions: %v", err)
	}
	if revived.PromptedByID != first.ID {
		t.Errorf("the new question was prompted by %q, want the refusal it revisits", revived.PromptedByID)
	}

	// The refusal and its reason stay readable: §4.13 has the triage recipe
	// read why topics were declined as evidence for its next proposals.
	history, err := store.QuestionHistory(ctx, first.ID)
	if err != nil {
		t.Fatalf("QuestionHistory: %v", err)
	}
	var kept bool
	for _, event := range history {
		if event.State == QuestionDeclined && event.Payload.Note == reason && event.Actor == "operator" {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the decline's reason is not in the history verbatim: %+v", history)
	}
}

// TestTopicProposalsListsOnlyWhatAwaitsTheOperator keeps the inbox honest: an
// accepted proposal is an entity and a declined one is a refusal, and offering
// either again would be asking a question that has an answer.
func TestTopicProposalsListsOnlyWhatAwaitsTheOperator(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)

	accepted, err := store.AskTopic(ctx, manifold(32), Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	other := manifold(3)
	other.Name, other.Identity = "dotfiles", "github.com/atyrode/dotfiles"
	other.Aliases, other.Records = nil, nil
	other.Binding = []FactInput{{
		Predicate: PredicateRepositoryRemote,
		Value:     FactValue{Kind: ValueText, Text: "github.com/atyrode/dotfiles"},
	}}
	declined, err := store.AskTopic(ctx, other, Provenance{Actor: "topic-seed"})
	if err != nil {
		t.Fatalf("AskTopic: %v", err)
	}
	third := manifold(7)
	third.Name, third.Identity = "nixos", "github.com/atyrode/nixos"
	third.Aliases, third.Records = nil, nil
	third.Binding = []FactInput{{
		Predicate: PredicateRepositoryRemote,
		Value:     FactValue{Kind: ValueText, Text: "github.com/atyrode/nixos"},
	}}
	if _, err := store.AskTopic(ctx, third, Provenance{Actor: "topic-seed"}); err != nil {
		t.Fatalf("AskTopic: %v", err)
	}

	if _, err := store.AcceptTopic(ctx, accepted.ID, "operator", &recordingFiler{}); err != nil {
		t.Fatalf("AcceptTopic: %v", err)
	}
	if err := store.DeclineTopic(ctx, declined.ID, "operator", "not a project"); err != nil {
		t.Fatalf("DeclineTopic: %v", err)
	}

	open, err := store.TopicProposals(ctx)
	if err != nil {
		t.Fatalf("TopicProposals: %v", err)
	}
	if len(open) != 1 || open[0].Proposal.Name != "nixos" {
		t.Fatalf("the inbox offers %d proposals, want only nixos: %+v", len(open), open)
	}
	if open[0].Proposal.Sessions != 7 || open[0].Proposal.Identity != "github.com/atyrode/nixos" {
		t.Errorf("the proposal reads back as %+v", open[0].Proposal)
	}

	// The accepted one still says what it produced, which is what makes an
	// answered proposal traceable to the subject it created.
	answered, err := store.TopicQuestion(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("TopicQuestion: %v", err)
	}
	if answered.EntityID == "" {
		t.Error("the accepted proposal names no entity")
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
