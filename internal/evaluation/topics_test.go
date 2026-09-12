package evaluation

// §4.13 stage four: the topic a record is filed under is context, exactly as a
// name the producing run wrote down is.
//
// The reason this needs its own test rather than an extension of the context
// tests is that it closes a gap the operator can see. Interest is recorded as
// §4.8's lifecycle and analysis-policy facts about a topic, and until a
// record's *filing* reached this package those facts moved the draws only for
// records whose provisional labels happened to spell one of the topic's
// aliases. A model that labelled its own hypothesis "retry storm" instead of
// "manifold" would keep drawing review budget onto a project the operator had
// paused, and the operator would have no way to tell why: he did state the
// fact, and the ledger did hold it.
//
// So the corpus below is deliberately unnamed. Not one hypothesis carries a
// provisional label, and every subject reaches the context through its filing
// alone.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// topicCorpus is four identical candidates filed under four topics the
// operator has taken four different stances toward — including saying
// nothing, which is a stance the ledger can hold and the one every seeded
// topic starts in.
type topicCorpus struct {
	front    *frontier.Store
	ledger   *reality.Store
	source   Source
	working  string
	unstated string
	paused   string
	excluded string
	// The candidates, in the same order as the topics above.
	onWorking  frontier.Hypothesis
	onUnstated frontier.Hypothesis
	onPaused   frontier.Hypothesis
	onExcluded frontier.Hypothesis
}

func newTopicCorpus(t *testing.T) *topicCorpus {
	t.Helper()
	ctx := context.Background()
	ledger, err := reality.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	// The focus rules have to be installed for anything to be withheld:
	// §4.8 maps a policy to an allowance through a version, and an
	// uninstalled version decides nothing.
	if _, err := ledger.Focus().Install(ctx); err != nil {
		t.Fatalf("install focus rules: %v", err)
	}
	front, err := frontier.Open(t.TempDir(), frontier.WithEntities(ledger))
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { front.Close() })

	corpus := &topicCorpus{front: front, ledger: ledger}
	corpus.working = newTopic(t, ledger, "the service being built")
	corpus.unstated = newTopic(t, ledger, "a repository nobody has ruled on")
	corpus.paused = newTopic(t, ledger, "the project on hold")
	corpus.excluded = newTopic(t, ledger, "the client's repository")

	// Working on it, and owned: §4.8's active lifecycle plus ownership is
	// what makes a subject current work.
	setInterest(t, ledger, corpus.working, reality.InterestWorking)
	assertOwnership(t, ledger, corpus.working)
	// Not now.
	setInterest(t, ledger, corpus.paused, reality.InterestNotNow)
	// Excluded: the policy alone, with the lifecycle left alone, which is
	// what §4.13's fourth stance writes.
	setInterest(t, ledger, corpus.excluded, reality.InterestExcluded)

	created := time.Now().UTC().Add(-4 * time.Hour)
	for _, filing := range []struct {
		topic string
		into  *frontier.Hypothesis
	}{
		{corpus.working, &corpus.onWorking},
		{corpus.unstated, &corpus.onUnstated},
		{corpus.paused, &corpus.onPaused},
		{corpus.excluded, &corpus.onExcluded},
	} {
		// No provisional labels anywhere: the filing is the only thing
		// connecting these records to the ledger.
		candidate, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
			RunID: "run-local",
			Payload: frontier.HypothesisPayload{
				Statement: "the same claim, written four times",
				Novelty:   0.4,
				Priority:  0.5,
			},
		})
		if err != nil {
			t.Fatalf("create hypothesis: %v", err)
		}
		if _, err := front.File(ctx, frontier.FilingInput{
			Record:    frontier.Ref{Type: frontier.EntityHypothesis, ID: candidate.ID},
			EntityID:  filing.topic,
			Rationale: "the session that produced it ran in that checkout",
			Author:    frontier.FilingRun,
			AuthorID:  "run-local",
		}); err != nil {
			t.Fatalf("file: %v", err)
		}
		candidate.CreatedAt = created
		*filing.into = candidate
	}
	corpus.source = NewSource(front, ledger, nil)
	return corpus
}

func newTopic(t *testing.T, ledger *reality.Store, name string) string {
	t.Helper()
	entity, err := ledger.CreateEntity(context.Background(), reality.EntityInput{
		Kind:    reality.EntityRepository,
		Payload: reality.EntityPayload{DisplayName: name},
	})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	return entity.ID
}

func setInterest(t *testing.T, ledger *reality.Store, entityID, state string) {
	t.Helper()
	if err := ledger.SetInterest(context.Background(), entityID, "operator-under-test",
		state, "stated from the topic page"); err != nil {
		t.Fatalf("set interest %s: %v", state, err)
	}
}

func assertOwnership(t *testing.T, ledger *reality.Store, entityID string) {
	t.Helper()
	at := time.Now().UTC()
	if _, _, err := ledger.AssertFact(context.Background(), reality.FactInput{
		SubjectID:   entityID,
		Predicate:   reality.PredicateOwnership,
		Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: reality.OwnershipOwned},
		ValidFrom:   at,
		ObservedAt:  at,
		Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: "operator-under-test", At: at},
		Confidence:  reality.ConfidenceHigh,
		Sensitivity: reality.SensitivityRoutine,
	}); err != nil {
		t.Fatalf("assert ownership: %v", err)
	}
}

func (c *topicCorpus) contexts(t *testing.T) map[string]Context {
	t.Helper()
	artifacts, err := c.source.Artifacts(context.Background())
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	out := make(map[string]Context, len(artifacts))
	for _, artifact := range artifacts {
		out[artifact.Subject.ID] = artifact.Context
	}
	return out
}

// TestTheTopicARecordIsFiledUnderIsItsContext is the stage-four claim itself:
// the operator's stance toward a topic reaches every record filed under it,
// with no name in the record resolving to anything.
func TestTheTopicARecordIsFiledUnderIsItsContext(t *testing.T) {
	corpus := newTopicCorpus(t)
	contexts := corpus.contexts(t)

	working := contexts[corpus.onWorking.ID]
	if !working.CurrentWork {
		t.Errorf("a record filed under the operator's current work is not current work: %+v", working)
	}
	if working.Priority <= 0 {
		t.Errorf("priority = %d, want the active-and-owned topic to raise it", working.Priority)
	}
	if working.Blocked || working.Allowance != string(reality.AllowanceFull) {
		t.Errorf("working topic allowance = %q blocked = %v", working.Allowance, working.Blocked)
	}

	// A topic nobody has ruled on decides nothing, which is what makes the
	// three stated stances mean something: none of these values is a
	// default this package applied.
	unstated := contexts[corpus.onUnstated.ID]
	if unstated.CurrentWork || unstated.Priority != 0 || unstated.Blocked ||
		unstated.Allowance != string(reality.AllowanceFull) {
		t.Errorf("an unruled topic decided something: %+v", unstated)
	}

	// Not now is dormant plus learn-only, and learn-only is §4.8's
	// deferral list: the record is still read and still kept, and
	// subject-specific work is withheld — exactly as it is for a record
	// whose recorded name resolves to a dormant entity.
	paused := contexts[corpus.onPaused.ID]
	if paused.Allowance != string(reality.AllowanceLearnOnly) || !paused.Blocked {
		t.Errorf("paused topic context = %+v, want learn-only and withheld", paused)
	}
	if paused.CurrentWork {
		t.Error("a record filed under a dormant topic must not read as current work")
	}
	if paused.Priority >= working.Priority {
		t.Errorf("paused priority %d is not below working priority %d", paused.Priority, working.Priority)
	}

	excluded := contexts[corpus.onExcluded.ID]
	if excluded.Allowance != string(reality.AllowanceExcluded) || !excluded.Blocked {
		t.Errorf("excluded topic context = %+v, want the work withheld", excluded)
	}

	// The gap note about a record naming nothing must not fire: these
	// records name nothing and are still about something.
	for id, recorded := range contexts {
		for _, unknown := range recorded.Unknown {
			if unknown == "this record carries no recorded label, scope or target and is filed "+
				"under no topic, so no Reality subject could be consulted" {
				t.Errorf("%s is filed under a topic and still reported as having no subject", id)
			}
		}
	}
}

// TestARetiredTopicStopsBeingContext pairs the stance with §4.13's retirement:
// a topic the operator says should never have existed stops deciding anything
// about the records filed under it, and the record's context says it has no
// subject again rather than staying withheld by a subject that is no longer
// one.
func TestARetiredTopicStopsBeingContext(t *testing.T) {
	corpus := newTopicCorpus(t)
	if before := corpus.contexts(t)[corpus.onExcluded.ID]; !before.Blocked {
		t.Fatalf("the excluded topic was not withholding work to begin with: %+v", before)
	}
	if err := corpus.ledger.RetireEntity(context.Background(), corpus.excluded,
		"operator-under-test", "this name never described one repository"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	after := corpus.contexts(t)[corpus.onExcluded.ID]
	if after.Blocked || after.Allowance != string(reality.AllowanceFull) {
		t.Fatalf("a retired topic still withholds work: %+v", after)
	}
	// Nothing was deleted: the filing row is still there and still names
	// the retired topic, which is what makes the retirement reversible.
	filings, err := corpus.front.FilingsOf(context.Background(),
		frontier.Ref{Type: frontier.EntityHypothesis, ID: corpus.onExcluded.ID})
	if err != nil {
		t.Fatalf("filings: %v", err)
	}
	if len(filings) != 1 || filings[0].EntityID != corpus.excluded {
		t.Fatalf("the filing under the retired topic was not kept: %+v", filings)
	}
}

// TestTheWeightedLaneFollowsTheOperatorsStance is the consequence §4.13 asks
// for, measured rather than asserted. Over a seeded sample of draws:
//
//   - the record filed under the topic the operator is working on is drawn
//     more often than the identical record filed under a topic nobody has
//     ruled on, because current work and priority are read off the topic's
//     facts;
//   - the record filed under the paused topic is never drawn and stays a
//     reported gap naming the allowance that withheld it, because §4.13's
//     *not now* is `dormant` with `learn-only` and §4.8's learn-only refuses
//     subject-specific work — which is exactly what a paused entity named in
//     a record's own labels does today;
//   - the excluded one is never drawn either.
//
// Neither withheld record is deleted, moved or unfiled, which is the
// difference between "not interested" and a deletion.
func TestTheWeightedLaneFollowsTheOperatorsStance(t *testing.T) {
	corpus := newTopicCorpus(t)
	contexts := corpus.contexts(t)
	now := time.Now().UTC()
	created := now.Add(-4 * time.Hour)

	ids := []string{corpus.onWorking.ID, corpus.onUnstated.ID, corpus.onPaused.ID, corpus.onExcluded.ID}
	items := make([]projected, 0, len(ids))
	for _, id := range ids {
		artifact := hypothesisArtifact(id, created)
		artifact.Context = contexts[id]
		artifact.ContextVersion = contexts[id].Version
		items = append(items, projected{
			Artifact: artifact,
			Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
			Required: map[string]bool{RoleReception: true},
		})
	}

	policy := viewPolicy()
	drawn := make(map[string]int, len(ids))
	withheld := make(map[string]bool, 2)
	weighted := 0
	for seed := uint64(1); seed <= 2000; seed++ {
		result, err := selectDraw(drawInput{Policy: policy, Items: items, Now: now}, "run-1", seed)
		if err != nil {
			t.Fatalf("draw %d: %v", seed, err)
		}
		for _, gap := range result.Gaps {
			for _, id := range []string{corpus.onPaused.ID, corpus.onExcluded.ID} {
				if strings.Contains(gap, id) && strings.Contains(gap, "withholds") {
					withheld[id] = true
				}
			}
		}
		if result.Lane != LaneWeighted {
			// The reserved lanes pick by age and the exploration lane
			// picks uniformly, by design: neither is where a recorded
			// stance is supposed to show up.
			continue
		}
		weighted++
		drawn[result.Assignment.Subject.ID]++
	}
	// The margin is the mechanism rather than a coin flip. Current work
	// multiplies the weight by 1.5 and the topic's priority by a further
	// 1.75, so the operator's topic should take about 72% of the weighted
	// lane against an identical record whose topic states nothing; the
	// assertion is 60% over two thousand seeded draws, which the sampling
	// spread cannot reach by accident and a lost stance cannot reach at
	// all — with the facts ignored the two records are indistinguishable
	// and the share is a half.
	if weighted == 0 || 5*drawn[corpus.onWorking.ID] < 3*weighted {
		t.Fatalf("the operator's current work took %d of %d weighted draws: %v",
			drawn[corpus.onWorking.ID], weighted, drawn)
	}
	if drawn[corpus.onUnstated.ID] == 0 {
		t.Fatalf("the unruled topic's record was never drawn, so nothing was being compared: %v", drawn)
	}
	for _, id := range []string{corpus.onPaused.ID, corpus.onExcluded.ID} {
		if drawn[id] != 0 {
			t.Errorf("a withheld record was drawn %d time(s): %s", drawn[id], id)
		}
		if !withheld[id] {
			t.Errorf("the withheld record %s was never reported as a gap naming the allowance", id)
		}
	}
	// Nothing was removed to achieve any of that: both withheld records are
	// still filed exactly where they were.
	for _, filed := range []struct {
		record string
		topic  string
	}{
		{corpus.onPaused.ID, corpus.paused},
		{corpus.onExcluded.ID, corpus.excluded},
	} {
		entities, err := corpus.front.EntitiesFiled(context.Background(),
			frontier.Ref{Type: frontier.EntityHypothesis, ID: filed.record})
		if err != nil {
			t.Fatalf("filed topics: %v", err)
		}
		if len(entities) != 1 || entities[0] != filed.topic {
			t.Errorf("%s is filed under %v, want the topic it started under", filed.record, entities)
		}
	}
}
