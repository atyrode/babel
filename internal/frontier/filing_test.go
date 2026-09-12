package frontier

import (
	"context"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/reference"
)

// retiredLedger is an EntityLifecycle that retires the topics a test names.
type retiredLedger struct {
	retired map[string]bool
	err     error
}

func (l retiredLedger) EntityRetired(_ context.Context, entityID string) (bool, error) {
	if l.err != nil {
		return false, l.err
	}
	return l.retired[entityID], nil
}

// filedStore opens a frontier that can be asked which topics are retired.
func filedStore(t *testing.T, ledger EntityLifecycle) *Store {
	t.Helper()
	options := []Option{}
	if ledger != nil {
		options = append(options, WithEntities(ledger))
	}
	store, err := Open(t.TempDir(), options...)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close frontier: %v", err)
		}
	})
	return store
}

// filedCandidate writes one candidate and returns its reference, which is what
// every filing in this file is about.
func filedCandidate(t *testing.T, s *Store, statement string) Ref {
	t.Helper()
	record, err := s.CreateHypothesis(context.Background(), HypothesisInput{
		RunID:   "run-topics",
		Actor:   Run("run-topics"),
		Payload: hypothesisPayload(statement, 0.5),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	return Ref{Type: EntityHypothesis, ID: record.ID}
}

func TestFileSupersedesRatherThanDuplicating(t *testing.T) {
	s := filedStore(t, nil)
	ctx := context.Background()
	record := filedCandidate(t, s, "the release pipeline retries on a stale lock")

	first, err := s.File(ctx, FilingInput{
		Record: record, EntityID: "ent_manifold", Rationale: "the run read this repository",
		Author: FilingRun, AuthorID: "run-topics",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	second, err := s.File(ctx, FilingInput{
		Record: record, EntityID: "ent_manifold", Rationale: "every cited session is a checkout of it",
		Author: FilingOperator, AuthorID: "alex",
	})
	if err != nil {
		t.Fatalf("re-file: %v", err)
	}

	if second.SupersedesID != first.ID {
		t.Errorf("the second filing supersedes %q, want the first filing %q",
			second.SupersedesID, first.ID)
	}
	history, err := s.FilingsOf(ctx, record)
	if err != nil {
		t.Fatalf("filings of: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("the history holds %d filings, want both: %+v", len(history), history)
	}
	if history[0].ID != second.ID {
		t.Errorf("the history reads %q first, want the newest filing %q", history[0].ID, second.ID)
	}
	if history[1].Rationale != "the run read this repository" {
		t.Errorf("the superseded filing reads %q, want its own rationale kept verbatim",
			history[1].Rationale)
	}
	// The topic holds the record once. Two live filings of one record under
	// one topic would make "what is in this topic" answer with a duplicate.
	filed, err := s.FiledUnder(ctx, "ent_manifold")
	if err != nil {
		t.Fatalf("filed under: %v", err)
	}
	if len(filed) != 1 || filed[0] != record {
		t.Errorf("the topic holds %+v, want exactly %+v once", filed, record)
	}
}

func TestUnfileWithdrawsAndKeepsBoth(t *testing.T) {
	s := filedStore(t, nil)
	ctx := context.Background()
	record := filedCandidate(t, s, "the archive verify pass rereads unchanged blobs")

	filing, err := s.File(ctx, FilingInput{
		Record: record, EntityID: "ent_babel", Rationale: "the claim is about the archive",
		Author: FilingOperator, AuthorID: "alex",
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	withdrawn, err := s.Unfile(ctx, record, "ent_babel", FilingOperator, "alex",
		"this is about restic, not about Babel")
	if err != nil {
		t.Fatalf("unfile: %v", err)
	}

	if !withdrawn.Withdrawn {
		t.Error("the appended row is not marked withdrawn")
	}
	if withdrawn.WithdrawReason != "this is about restic, not about Babel" {
		t.Errorf("the withdrawal reads %q, want the reason kept verbatim", withdrawn.WithdrawReason)
	}
	if withdrawn.Rationale != filing.Rationale {
		t.Errorf("the withdrawal carries rationale %q, want the one it withdrew (%q)",
			withdrawn.Rationale, filing.Rationale)
	}
	history, err := s.FilingsOf(ctx, record)
	if err != nil {
		t.Fatalf("filings of: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("the history holds %d rows, want the filing and its withdrawal", len(history))
	}
	filed, err := s.FiledUnder(ctx, "ent_babel")
	if err != nil {
		t.Fatalf("filed under: %v", err)
	}
	if len(filed) != 0 {
		t.Errorf("the topic still holds %+v after the filing was withdrawn", filed)
	}
	topics, err := s.EntitiesFiled(ctx, record)
	if err != nil {
		t.Fatalf("entities filed: %v", err)
	}
	if len(topics) != 0 {
		t.Errorf("the record still reads as filed under %v", topics)
	}
	// A withdrawal is not a filing to withdraw.
	if _, err := s.Unfile(ctx, record, "ent_babel", FilingOperator, "alex", "again"); !errors.Is(err, ErrNotFiled) {
		t.Errorf("unfiling twice: %v, want ErrNotFiled", err)
	}
}

func TestUnfiledCountsTheBacklogAndNothingElse(t *testing.T) {
	ledger := retiredLedger{retired: map[string]bool{"ent_retired": true}}
	s := filedStore(t, ledger)
	ctx := context.Background()

	untouched := filedCandidate(t, s, "one: nothing has been said about this")
	seeded := filedCandidate(t, s, "two: a heuristic guessed a topic")
	judged := filedCandidate(t, s, "three: a run filed it under a topic")
	answered := filedCandidate(t, s, "four: a run said it is about nothing in particular")
	retired := filedCandidate(t, s, "five: the topic it was filed under was retired")
	rejected := filedCandidate(t, s, "six: the operator rejected it")

	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err := s.File(ctx, FilingInput{Record: seeded, EntityID: "ent_manifold",
		Rationale: "32 sessions in 3 checkouts cite it", Author: FilingHeuristic})
	must("seed a filing", err)
	_, err = s.File(ctx, FilingInput{Record: judged, EntityID: "ent_manifold",
		Rationale: "the claim is about the router", Author: FilingRun, AuthorID: "run-triage"})
	must("file a record", err)
	_, err = s.NoTopic(ctx, answered, FilingRun, "run-triage",
		"a note about Babel's own cookbook, about no subject the operator acts on")
	must("record no topic", err)
	_, err = s.File(ctx, FilingInput{Record: retired, EntityID: "ent_retired",
		Rationale: "the run read that repository", Author: FilingRun, AuthorID: "run-triage"})
	must("file under the topic that was later retired", err)
	_, err = s.Decide(ctx, DispositionInput{Subject: rejected, Disposition: DispositionReject,
		ReviewerID: "alex", Note: "not a real pattern"})
	must("reject a candidate", err)

	backlog, err := s.Unfiled(ctx, 0)
	if err != nil {
		t.Fatalf("unfiled: %v", err)
	}
	want := []Ref{untouched, seeded, retired}
	if len(backlog) != len(want) {
		t.Fatalf("the backlog holds %+v, want %+v", backlog, want)
	}
	// Oldest first: the backlog is a queue, and one nobody drains from the
	// bottom has a permanent bottom.
	for i := range want {
		if backlog[i] != want[i] {
			t.Fatalf("the backlog is %+v, want %+v in creation order", backlog, want)
		}
	}

	// Withdrawing the judged record's filing puts it back in the backlog: an
	// unfiling is not a decision that the record is about nothing.
	_, err = s.Unfile(ctx, judged, "ent_manifold", FilingOperator, "alex", "the router is a different project")
	must("unfile", err)
	backlog, err = s.Unfiled(ctx, 0)
	if err != nil {
		t.Fatalf("unfiled after a withdrawal: %v", err)
	}
	if len(backlog) != 4 {
		t.Fatalf("the backlog holds %+v after a withdrawal, want the withdrawn record back in it", backlog)
	}

	// The limit bounds the answer without changing which records qualify.
	backlog, err = s.Unfiled(ctx, 2)
	if err != nil {
		t.Fatalf("unfiled with a limit: %v", err)
	}
	if len(backlog) != 2 || backlog[0] != untouched {
		t.Errorf("a bounded backlog is %+v, want the two oldest starting at %+v", backlog, untouched)
	}
}

func TestSupersededRevisionsAreNotBacklog(t *testing.T) {
	s := filedStore(t, nil)
	ctx := context.Background()
	original := filedCandidate(t, s, "the first wording of one claim")
	revised, err := s.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-topics",
		Actor:      Run("run-topics"),
		AncestorID: original.ID,
		Reason:     "sharper wording",
		Payload:    hypothesisPayload("the second wording of one claim", 0.5),
	})
	if err != nil {
		t.Fatalf("revise: %v", err)
	}

	backlog, err := s.Unfiled(ctx, 0)
	if err != nil {
		t.Fatalf("unfiled: %v", err)
	}
	if len(backlog) != 1 || backlog[0].ID != revised.ID {
		t.Errorf("the backlog is %+v, want only the head revision %s", backlog, revised.ID)
	}
}

func TestRetiredTopicHidesItsFilings(t *testing.T) {
	ledger := retiredLedger{retired: map[string]bool{}}
	s := filedStore(t, ledger)
	ctx := context.Background()
	record := filedCandidate(t, s, "the conductor starts a run per repository")

	if _, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_topic",
		Rationale: "the run read that repository", Author: FilingOperator, AuthorID: "alex"}); err != nil {
		t.Fatalf("file: %v", err)
	}
	topics, err := s.EntitiesFiled(ctx, record)
	if err != nil {
		t.Fatalf("entities filed: %v", err)
	}
	if len(topics) != 1 || topics[0] != "ent_topic" {
		t.Fatalf("the record reads as filed under %v, want ent_topic", topics)
	}

	ledger.retired["ent_topic"] = true
	topics, err = s.EntitiesFiled(ctx, record)
	if err != nil {
		t.Fatalf("entities filed after retirement: %v", err)
	}
	if len(topics) != 0 {
		t.Errorf("the record still reads as filed under %v after the topic was retired", topics)
	}
	// The row is not gone: retirement re-queues a filing, it does not delete
	// one, and undoing the retirement brings it back.
	history, err := s.FilingsOf(ctx, record)
	if err != nil {
		t.Fatalf("filings of: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("the filing history holds %d rows after a retirement, want the filing kept", len(history))
	}
}

func TestFilingRefusals(t *testing.T) {
	s := filedStore(t, nil)
	ctx := context.Background()
	record := filedCandidate(t, s, "a claim to file badly")

	cases := []struct {
		name string
		call func() error
	}{
		{"no topic named", func() error {
			_, err := s.File(ctx, FilingInput{Record: record, Rationale: "because",
				Author: FilingOperator, AuthorID: "alex"})
			return err
		}},
		{"no rationale", func() error {
			_, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_x",
				Author: FilingOperator, AuthorID: "alex"})
			return err
		}},
		{"a heuristic with an identity", func() error {
			_, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_x", Rationale: "seeded",
				Author: FilingHeuristic, AuthorID: "alex"})
			return err
		}},
		{"a run with no identity", func() error {
			_, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_x", Rationale: "guessed",
				Author: FilingRun})
			return err
		}},
		{"an author outside the vocabulary", func() error {
			_, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_x", Rationale: "guessed",
				Author: FilingAuthor("conductor"), AuthorID: "c"})
			return err
		}},
		{"no reason for nothing in particular", func() error {
			_, err := s.NoTopic(ctx, record, FilingOperator, "alex", "  ")
			return err
		}},
		{"no reason to unfile", func() error {
			_, err := s.Unfile(ctx, record, "ent_x", FilingOperator, "alex", "")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("refusal: %v, want ErrInvalidValue", err)
			}
		})
	}

	if _, err := s.File(ctx, FilingInput{Record: Ref{Type: EntityHypothesis, ID: "hyp_absent"},
		EntityID: "ent_x", Rationale: "about something", Author: FilingOperator, AuthorID: "alex"},
	); !errors.Is(err, ErrUnknownEntity) {
		t.Errorf("filing a record this store does not hold: %v, want ErrUnknownEntity", err)
	}
}

func TestFilingPublishesAndRoundTrips(t *testing.T) {
	h := newRecordingHook()
	s := openStoreWithHook(t, h)
	ctx := context.Background()
	record := filedCandidate(t, s, "a claim worth publishing a filing for")
	h.staged = nil

	filing, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_manifold",
		Rationale: "the run read this repository", Author: FilingRun, AuthorID: "run-topics"})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	staged := h.only(t)
	if staged.EntityID != filing.ID {
		t.Errorf("staged entity %q, want the filing %q", staged.EntityID, filing.ID)
	}
	decoded := wire(t, staged)
	if decoded.Kind != PublishedFiling {
		t.Fatalf("staged kind %q, want %q", decoded.Kind, PublishedFiling)
	}
	if decoded.Subject != record {
		t.Errorf("the published filing files %+v, want %+v", decoded.Subject, record)
	}
	if decoded.Filing == nil {
		t.Fatal("the published filing carries no topic, author or state")
	}
	if decoded.Filing.EntityID != "ent_manifold" {
		t.Errorf("the published topic is %q, want ent_manifold", decoded.Filing.EntityID)
	}
	if decoded.Filing.Author != FilingRun || decoded.Filing.AuthorID != "run-topics" {
		t.Errorf("the published filing is attributed to %s/%s, want run/run-topics",
			decoded.Filing.Author, decoded.Filing.AuthorID)
	}
	if decoded.Filing.Withdrawn {
		t.Error("a filing published as a withdrawal")
	}

	// A withdrawal publishes too: a fleet reader that saw only filings would
	// hold a corpus in which nothing was ever unfiled.
	h.staged = nil
	if _, err := s.Unfile(ctx, record, "ent_manifold", FilingOperator, "alex",
		"the claim is about the deployment, not the repository"); err != nil {
		t.Fatalf("unfile: %v", err)
	}
	withdrawal := wire(t, h.only(t))
	if withdrawal.Filing == nil || !withdrawal.Filing.Withdrawn {
		t.Fatalf("the withdrawal published as %+v, want a withdrawn filing", withdrawal.Filing)
	}
	if withdrawal.Filing.Supersedes != filing.ID {
		t.Errorf("the published withdrawal supersedes %q, want %q",
			withdrawal.Filing.Supersedes, filing.ID)
	}
}

func TestFilingMintsAnAboutEdge(t *testing.T) {
	s, appender, warnings := referencedStore(t)
	ctx := context.Background()
	record := filedCandidate(t, s, "a claim with a topic worth citing")

	if _, err := s.File(ctx, FilingInput{Record: record, EntityID: "ent_manifold",
		Rationale: "the run read this repository", Author: FilingRun, AuthorID: "run-topics"}); err != nil {
		t.Fatalf("file: %v", err)
	}

	edges := appender.of(reference.KindAbout)
	if len(edges) != 1 {
		t.Fatalf("minted %d about edges, want one: %+v", len(edges), edges)
	}
	edge := edges[0]
	if edge.From != (reference.RecordRef{Kind: string(EntityHypothesis), ID: record.ID}) {
		t.Errorf("the edge starts at %s, want the filed record", edge.From)
	}
	if edge.To != (reference.RecordRef{Kind: "reality_entity", ID: "ent_manifold"}) {
		t.Errorf("the edge ends at %s, want the ledger entity", edge.To)
	}
	if edge.ActorKind != string(ActorRun) || edge.ActorRef != "run-topics" {
		t.Errorf("the edge is attributed to %s/%s, want run/run-topics", edge.ActorKind, edge.ActorRef)
	}
	if edge.Note != "" {
		t.Errorf("the edge carries the note %q; the rationale lives on the filing", edge.Note)
	}

	// A heuristic is machinery rather than a reviewer, and says so.
	other := filedCandidate(t, s, "a claim a heuristic filed")
	if _, err := s.File(ctx, FilingInput{Record: other, EntityID: "ent_manifold",
		Rationale: "32 sessions in 3 checkouts cite it", Author: FilingHeuristic}); err != nil {
		t.Fatalf("seed a filing: %v", err)
	}
	edges = appender.of(reference.KindAbout)
	if len(edges) != 2 || edges[1].ActorKind != "system" {
		t.Fatalf("the seeded filing minted %+v, want a system-attributed about edge", edges)
	}

	// Withdrawing mints nothing: an edge cannot be unasserted, and the
	// withdrawal is published as a record instead.
	before := len(appender.of(reference.KindAbout))
	if _, err := s.Unfile(ctx, record, "ent_manifold", FilingOperator, "alex", "wrong topic"); err != nil {
		t.Fatalf("unfile: %v", err)
	}
	if after := len(appender.of(reference.KindAbout)); after != before {
		t.Errorf("a withdrawal minted %d edges, want none", after-before)
	}
	if len(*warnings) != 0 {
		t.Errorf("emission warnings: %v", *warnings)
	}
}

func TestNoTopicFilesNothingAndAnswersTheBacklog(t *testing.T) {
	s, appender, _ := referencedStore(t)
	ctx := context.Background()
	record := filedCandidate(t, s, "a note about Babel's own prose")

	filing, err := s.NoTopic(ctx, record, FilingRun, "run-triage",
		"a remark about the cookbook's wording, about no subject the operator acts on")
	if err != nil {
		t.Fatalf("no topic: %v", err)
	}
	if filing.EntityID != "" {
		t.Errorf("a no-topic filing names topic %q", filing.EntityID)
	}
	if len(appender.of(reference.KindAbout)) != 0 {
		t.Error("a no-topic filing minted an edge to nothing")
	}
	backlog, err := s.Unfiled(ctx, 0)
	if err != nil {
		t.Fatalf("unfiled: %v", err)
	}
	if len(backlog) != 0 {
		t.Errorf("the backlog still holds %+v after the record was answered", backlog)
	}
}
