package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// These scenarios cover the source adapter's honesty contract. Every one of
// them is a case where the tempting behaviour is silent: an unreadable catalog
// reported as an empty fleet, a sealed object dropped from a coverage count, a
// payload whose claimed identity nobody checked. Each of those turns a gap into
// apparent completeness, which is the failure the whole feature is arranged to
// prevent.
//
// Fixtures are synthetic throughout: a temporary durable store and a fake fleet
// reader. Nothing here touches a live catalog, object store or keyring.

// fakeFleet is a FleetSource whose failures are scripted.
type fakeFleet struct {
	mu sync.Mutex
	// rows are returned by Records, page one, exhausted immediately.
	rows []fleet.Record
	// listErr is returned by Records when set.
	listErr error
	// opened maps a record id to what Open returns for it.
	opened map[string]fleet.Record
	// openErr maps a record id to a hard Open failure.
	openErr map[string]error
	// opens counts Open calls per record id, which is how the incremental
	// decode cache is observed.
	opens map[string]int
}

func (f *fakeFleet) Records(_ context.Context, filter sharedcatalog.RecordFilter) ([]fleet.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	if filter.Offset > 0 {
		return nil, nil
	}
	var out []fleet.Record
	for _, row := range f.rows {
		if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, row.Record.Kind) {
			continue
		}
		if len(filter.RecordIDs) > 0 && !containsString(filter.RecordIDs, row.Record.RecordID) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeFleet) Open(_ context.Context, rec sharedcatalog.FleetRecord) (fleet.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opens == nil {
		f.opens = map[string]int{}
	}
	f.opens[rec.Record.RecordID]++
	if err, ok := f.openErr[rec.Record.RecordID]; ok {
		return fleet.Record{}, err
	}
	if opened, ok := f.opened[rec.Record.RecordID]; ok {
		return opened, nil
	}
	return fleet.Record{FleetRecord: rec, Unopened: "no key for this record on this instance"}, nil
}

func (f *fakeFleet) openCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[id]
}

func containsKind(kinds []sharedcatalog.RecordKind, kind sharedcatalog.RecordKind) bool {
	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func fleetRow(id string, kind sharedcatalog.RecordKind, digest string) fleet.Record {
	return fleet.Record{FleetRecord: sharedcatalog.FleetRecord{
		Record: sharedcatalog.AnalysisRecordRow{
			RecordID:     id,
			RunID:        "run-remote",
			Kind:         kind,
			ObjectKey:    "obj/" + id,
			ObjectDigest: digest,
			CreatedAt:    time.Now().UTC(),
		},
		HostID:           "host-b",
		OriginInstanceID: "inst-b",
		SyncState:        sharedcatalog.SyncCommitted,
	}}
}

// newLocalFrontier opens a synthetic durable store holding one candidate.
func newLocalFrontier(t *testing.T) (*frontier.Store, frontier.Hypothesis) {
	t.Helper()
	store, err := frontier.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	candidate, err := store.CreateHypothesis(context.Background(), frontier.HypothesisInput{
		RunID: "run-local",
		Payload: frontier.HypothesisPayload{
			Statement:         "the release pipeline retries on a transient failure",
			ProvisionalLabels: []string{"pipeline"},
			Novelty:           0.4,
			Priority:          0.7,
		},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	return store, candidate
}

// Local analysis is enumerated without waiting for any host to publish. A
// single-machine deployment is the normal case, and an inventory that only
// listed what came back through a catalog would report an empty frontier on it.
func TestLocalAnalysisIsEnumeratedWithoutAFleet(t *testing.T) {
	ctx := context.Background()
	store, candidate := newLocalFrontier(t)

	source := NewSource(store, nil, nil)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("the local candidate must be in the inventory, got %d artifacts", len(artifacts))
	}
	got := artifacts[0]
	if got.Subject != (Subject{Kind: SubjectKindHypothesis, ID: candidate.ID}) {
		t.Fatalf("the subject must name the exact revision, got %+v", got.Subject)
	}
	if got.HeadID != candidate.ID {
		t.Fatalf("a head revision must report itself as the head, got %q", got.HeadID)
	}
	if got.RootID == "" {
		t.Fatal("an artifact must carry its chain identity")
	}
	if got.Title == "" || !strings.Contains(got.Title, "release pipeline") {
		t.Fatalf("the artifact must carry the record's own wording, got %q", got.Title)
	}
	if got.ContextVersion == "" {
		t.Fatal("an artifact must carry a material context version")
	}

	status, ok := source.(StatusSource)
	if !ok {
		t.Fatal("the source must be able to report how complete its read was")
	}
	state := status.Status()
	if !state.LocalOnly {
		t.Fatal("a nil fleet source is intentional local mode and must say so")
	}
	if state.Unavailable != "" {
		t.Fatalf("a supported local configuration must not report itself degraded, got %q",
			state.Unavailable)
	}
	// The absence of an optional store is visible per artifact rather than
	// as a deployment-wide outage.
	if len(got.Context.Unknown) == 0 {
		t.Fatal("context that could not be consulted must be listed as unknown")
	}
}

// An unreadable catalog is a reported degradation, never an empty fleet.
// Reporting no remote artifacts would make another host's never-reviewed
// observation look reviewed by absence.
func TestFleetOutageIsReportedNotEmptied(t *testing.T) {
	ctx := context.Background()
	store, candidate := newLocalFrontier(t)
	remote := &fakeFleet{listErr: errors.New("catalog unreachable")}

	source := NewSource(store, nil, remote)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("a fleet outage must not fail the local inventory: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Subject.ID != candidate.ID {
		t.Fatalf("local analysis must survive a fleet outage, got %+v", artifacts)
	}
	state := source.(StatusSource).Status()
	if state.Unavailable == "" {
		t.Fatal("a fleet outage must be reported")
	}
	if state.LocalOnly {
		t.Fatal("a failed shared read must never be reported as intentional local mode")
	}

	if _, err := source.(*babelSource).EvaluationRecords(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreadable evaluation catalog must refuse with ErrUnavailable, got %v", err)
	}
}

// A record this instance cannot open loses one record and not the read, and the
// loss is counted. A dropped record would make a coverage figure describe a
// smaller corpus than the deployment actually holds.
func TestUnopenableRecordIsCountedRatherThanDropped(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)
	row := fleetRow("rem-1", sharedcatalog.KindFinding, "digest-1")
	remote := &fakeFleet{rows: []fleet.Record{row}}

	source := NewSource(store, nil, remote)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.Subject.ID == "rem-1" {
			t.Fatal("a record that could not be opened must not appear as an artifact")
		}
	}
	state := source.(StatusSource).Status()
	if state.Unopened != 1 {
		t.Fatalf("the unopened record must be counted, got %d", state.Unopened)
	}
	if !strings.Contains(state.Unavailable, "could not be opened") {
		t.Fatalf("the read must say that records are missing from the inventory, got %q",
			state.Unavailable)
	}
}

// A payload's claimed identity is checked against the authenticated catalog
// row. Without the check, one host's publication could masquerade as another
// record and carry its coverage.
func TestPublishedIdentityMustMatchTheCatalogRow(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)

	published := frontier.PublishedRecord{
		Schema:    frontier.RecordSchema,
		Kind:      frontier.PublishedFinding,
		ID:        "some-other-record",
		RootID:    "root-rem",
		CreatedAt: time.Now().UTC(),
		Payload:   json.RawMessage(`{"title":"a pattern","pattern":"it recurs"}`),
	}
	row := fleetRow("rem-1", sharedcatalog.KindFinding, "digest-1")
	opened := row
	opened.Published = &published
	remote := &fakeFleet{
		rows:   []fleet.Record{row},
		opened: map[string]fleet.Record{"rem-1": opened},
	}

	source := NewSource(store, nil, remote)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.Subject.Kind == SubjectKindFinding {
			t.Fatalf("a record whose payload claims a different id must be refused, got %+v",
				artifact.Subject)
		}
	}
	if state := source.(StatusSource).Status(); state.Unopened != 1 {
		t.Fatalf("the mismatch must be counted as unopened, got %d", state.Unopened)
	}
}

// A remote head is decoded with its own wording, evidence and chain identity,
// and a superseded remote revision is not offered as a head. Offering both
// would make one candidate read as two and cross-host dedup answer backwards.
func TestRemoteHeadsAreDecodedAndSupersededOnesAreNot(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)

	older := frontier.PublishedRecord{
		Schema: frontier.RecordSchema, Kind: frontier.PublishedHypothesis,
		ID: "rem-1", RootID: "root-rem", CreatedAt: time.Now().UTC().Add(-time.Hour),
		Payload: json.RawMessage(`{"statement":"first wording","novelty":0.1,"priority":0.2}`),
	}
	newer := frontier.PublishedRecord{
		Schema: frontier.RecordSchema, Kind: frontier.PublishedHypothesis,
		ID: "rem-2", RootID: "root-rem", Ancestor: "rem-1", CreatedAt: time.Now().UTC(),
		Payload: json.RawMessage(`{"statement":"second wording","novelty":0.1,"priority":0.2}`),
	}
	rowOne := fleetRow("rem-1", sharedcatalog.KindHypothesis, "d1")
	rowTwo := fleetRow("rem-2", sharedcatalog.KindHypothesis, "d2")
	openedOne, openedTwo := rowOne, rowTwo
	openedOne.Published = &older
	openedTwo.Published = &newer

	remote := &fakeFleet{
		rows: []fleet.Record{rowOne, rowTwo},
		opened: map[string]fleet.Record{
			"rem-1": openedOne,
			"rem-2": openedTwo,
		},
	}
	source := NewSource(store, nil, remote)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	var heads []string
	for _, artifact := range artifacts {
		if artifact.RootID == "root-rem" {
			heads = append(heads, artifact.Subject.ID)
			if artifact.HeadID != artifact.Subject.ID {
				t.Fatalf("a head must report itself as the head, got %q", artifact.HeadID)
			}
		}
	}
	if len(heads) != 1 || heads[0] != "rem-2" {
		t.Fatalf("only the remote chain's leaf may be offered as a head, got %v", heads)
	}
}

// Another host's review answers give a remote artifact its review status. Left
// out, every remote proposal would read as `new` on a non-producing instance
// and the two machines would disagree about one record's lane.
func TestRemoteReviewAnswersResolveRemoteLanes(t *testing.T) {
	subject := Subject{Kind: SubjectKindProposal, ID: "rem-p1"}
	artifacts := []Artifact{{Subject: subject}}
	applyRemoteReviewStatus(artifacts, []remoteAnswer{{
		subject: subject, decision: string(frontier.DispositionAccept),
		createdAt: time.Unix(20, 0),
	}, {
		subject: subject, decision: string(frontier.DispositionDefer),
		createdAt: time.Unix(10, 0),
	}})
	if artifacts[0].ReviewStatus != string(frontier.ReviewAccepted) {
		t.Fatalf("the newest remote decision must win, got %q", artifacts[0].ReviewStatus)
	}

	// A local derivation is authoritative and must not be overwritten.
	local := []Artifact{{Subject: subject, ReviewStatus: string(frontier.ReviewRejected)}}
	applyRemoteReviewStatus(local, []remoteAnswer{{
		subject: subject, decision: string(frontier.DispositionAccept),
		createdAt: time.Unix(30, 0),
	}})
	if local[0].ReviewStatus != string(frontier.ReviewRejected) {
		t.Fatalf("the producing store's own derivation must win, got %q", local[0].ReviewStatus)
	}

	// Advice is not a disposition, and a refinement request alone does not
	// mint a review status.
	advice := []Artifact{{Subject: subject}}
	applyRemoteReviewStatus(advice, []remoteAnswer{{subject: subject, refine: true}})
	if advice[0].ReviewStatus != "" {
		t.Fatalf("a refinement request alone must not become a review status, got %q",
			advice[0].ReviewStatus)
	}
}

// The decode cache opens each sealed object once. Reopening the deployment's
// whole committed history on every sweep is the whole-corpus decrypt §E5
// forbids, and it would grow without bound while the answer stayed the same.
func TestDecodingIsIncrementalByObjectDigest(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)

	published := frontier.PublishedRecord{
		Schema: frontier.RecordSchema, Kind: frontier.PublishedFinding,
		ID: "rem-1", RootID: "rem-1", CreatedAt: time.Now().UTC(),
		Payload: json.RawMessage(`{"title":"a pattern","pattern":"it recurs"}`),
	}
	row := fleetRow("rem-1", sharedcatalog.KindFinding, "digest-1")
	opened := row
	opened.Published = &published
	remote := &fakeFleet{
		rows:   []fleet.Record{row},
		opened: map[string]fleet.Record{"rem-1": opened},
	}

	source := NewSource(store, nil, remote)
	if _, err := source.Artifacts(ctx); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if got := remote.openCount("rem-1"); got != 1 {
		t.Fatalf("the first refresh must open the object once, got %d", got)
	}
	if _, err := source.Artifacts(ctx); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if got := remote.openCount("rem-1"); got != 1 {
		t.Fatalf("an unchanged object must not be reopened, got %d opens", got)
	}

	// A record whose sealed object changed is a record this instance has not
	// decoded, so it is opened again.
	remote.mu.Lock()
	changed := fleetRow("rem-1", sharedcatalog.KindFinding, "digest-2")
	changedOpened := changed
	changedOpened.Published = &published
	remote.rows = []fleet.Record{changed}
	remote.opened = map[string]fleet.Record{"rem-1": changedOpened}
	remote.mu.Unlock()

	if _, err := source.Artifacts(ctx); err != nil {
		t.Fatalf("third refresh: %v", err)
	}
	if got := remote.openCount("rem-1"); got != 2 {
		t.Fatalf("a changed sealed object must be reopened, got %d opens", got)
	}
}

// A superseded revision still resolves, and it reports the current head. §E1
// binds a vote to the exact wording read, so an assignment taken against
// revision n must still be servable after n+1 exists — and the difference
// between the two ids is how a stale result is told apart from current coverage.
func TestSupersededRevisionResolvesAndNamesTheHead(t *testing.T) {
	ctx := context.Background()
	store, first := newLocalFrontier(t)

	second, err := store.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:      "run-local",
		AncestorID: first.ID,
		Reason:     "the earlier wording conflated two failures",
		Payload: frontier.HypothesisPayload{
			Statement:         "the release pipeline retries on a transient network failure",
			ProvisionalLabels: []string{"pipeline"},
			Novelty:           0.4,
			Priority:          0.7,
		},
	})
	if err != nil {
		t.Fatalf("revise hypothesis: %v", err)
	}

	source := NewSource(store, nil, nil)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Subject.ID != second.ID {
		t.Fatalf("only the chain's leaf is in the inventory, got %+v", artifacts)
	}

	stale, err := source.Artifact(ctx, Subject{Kind: SubjectKindHypothesis, ID: first.ID})
	if err != nil {
		t.Fatalf("a superseded revision must still resolve: %v", err)
	}
	if stale.Subject.ID != first.ID {
		t.Fatalf("the resolved artifact must be the revision asked for, got %q", stale.Subject.ID)
	}
	if stale.HeadID != second.ID {
		t.Fatalf("a superseded revision must name the current head, got %q", stale.HeadID)
	}
	if !strings.Contains(stale.Title, "transient failure") {
		t.Fatalf("the superseded wording must survive verbatim, got %q", stale.Title)
	}
}

// Recorded names come from structured fields the producing run wrote, never
// from prose. Matching a model-authored sentence against entity names would be
// this package inventing the alias mapping the Reality ledger exists to own.
func TestRecordedNamesComeFromStructuredFieldsOnly(t *testing.T) {
	hypothesis := Artifact{
		Subject: Subject{Kind: SubjectKindHypothesis, ID: "h1"},
		Body: []byte(`{"statement":"babel-web is slow","provisional_labels":["  pipeline  ",
			"pipeline","deploy"],"origin_cues":["a sentence mentioning babel-web"]}`),
	}
	names := recordedNames(hypothesis)
	if len(names) != 2 || names[0] != "deploy" || names[1] != "pipeline" {
		t.Fatalf("names must be the recorded labels, trimmed, deduplicated and sorted, got %v",
			names)
	}
	for _, name := range names {
		if strings.Contains(name, "babel-web") {
			t.Fatal("a name must never be derived from the record's prose")
		}
	}

	proposal := Artifact{
		Subject: Subject{Kind: SubjectKindProposal, ID: "p1"},
		Body: []byte(`{"title":"cache the index","problem":"slow","outcome":"faster",
			"targets":[{"system":"babel","confidence":"high"}]}`),
	}
	if got := recordedNames(proposal); len(got) != 1 || got[0] != "babel" {
		t.Fatalf("a proposal's names are its suggested target systems, got %v", got)
	}

	// An observation inherits its parent candidate's labels from the batch,
	// because it names no subject of its own.
	observation := Artifact{
		Subject: Subject{Kind: SubjectKindObservation, ID: "o1"},
		Body:    []byte(`{"claim":"the retry fires twice"}`),
		Related: []Subject{{Kind: SubjectKindHypothesis, ID: "h1"}},
	}
	resolved := resolveNames([]Artifact{observation, hypothesis})
	if len(resolved[0]) != 2 {
		t.Fatalf("an observation must inherit its candidate's labels, got %v", resolved[0])
	}
	orphan := resolveNames([]Artifact{observation})
	if len(orphan[0]) != 0 {
		t.Fatalf("an observation whose parent is absent must get no names, got %v", orphan[0])
	}
}

// A proposal's own verification criteria are suggestions with stable ids and no
// authority. §E6 forbids Babel rewriting its target and verifying itself against
// the replacement, so the id must be derived rather than asserted, and the same
// suggestion must keep the same id across refreshes.
func TestSuggestedCriteriaAreStableAndNotAuthority(t *testing.T) {
	first := suggestedCriteria("p1", []string{"latency drops below 200ms", "  ", "no new errors"})
	if len(first) != 2 {
		t.Fatalf("blank suggestions must be dropped, got %+v", first)
	}
	second := suggestedCriteria("p1", []string{"latency drops below 200ms", "no new errors"})
	if first[0].ID != second[0].ID {
		t.Fatal("the same suggestion must keep the same id across refreshes")
	}
	other := suggestedCriteria("p2", []string{"latency drops below 200ms"})
	if other[0].ID == first[0].ID {
		t.Fatal("two proposals' suggestions must not share an id")
	}
	if adopted, ok := adoptedCriteria(nil); ok || adopted.ID != "" {
		t.Fatal("with no operator criteria act there is no criteria authority")
	}
	newest, ok := adoptedCriteria([]Record{
		{ID: "c1", Kind: KindCriteria, CreatedAt: time.Unix(10, 0)},
		{ID: "c2", Kind: KindCriteria, CreatedAt: time.Unix(20, 0)},
		{ID: "x1", Kind: KindFeedback, CreatedAt: time.Unix(30, 0)},
	})
	if !ok || newest.ID != "c2" {
		t.Fatalf("the newest criteria act is the authority, got %+v ok=%v", newest, ok)
	}
}

// Evidence survives the trip into an artifact with its locator intact. An
// artifact whose evidence lost its locator would be a claim whose provenance
// evaporated, which internal/frontier's type system exists to prevent.
func TestObservationEvidenceSurvivesWithItsLocator(t *testing.T) {
	ctx := context.Background()
	store, candidate := newLocalFrontier(t)

	evidence, err := frontier.NewEvidence(event.Locator{
		Path: "sessions/a.jsonl", Digest: "sha256:abc", Line: 12,
	}, "the retry is logged twice")
	if err != nil {
		t.Fatalf("new evidence: %v", err)
	}
	if _, err := store.CreateObservation(ctx, frontier.ObservationInput{
		RunID:         "run-local",
		HypothesisID:  candidate.ID,
		RecipeID:      "effective-patterns",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:                 "the retry fires twice",
			Confidence:            frontier.ConfidenceModerate,
			Impact:                frontier.ImpactModerate,
			Evidence:              []frontier.Evidence{evidence},
			CounterEvidenceAbsent: true,
		},
	}); err != nil {
		t.Fatalf("create observation: %v", err)
	}

	source := NewSource(store, nil, nil)
	artifacts, err := source.Artifacts(ctx)
	if err != nil {
		t.Fatalf("artifacts: %v", err)
	}
	var observation *Artifact
	for i := range artifacts {
		if artifacts[i].Subject.Kind == SubjectKindObservation {
			observation = &artifacts[i]
		}
	}
	if observation == nil {
		t.Fatal("an observation must be in the coverage inventory")
	}
	if len(observation.Evidence) != 1 {
		t.Fatalf("the observation's evidence must travel with it, got %d", len(observation.Evidence))
	}
	if got := observation.Evidence[0].Locator(); got.Path != "sessions/a.jsonl" ||
		got.Digest != "sha256:abc" {
		t.Fatalf("evidence must keep its locator, got %+v", got)
	}
	if observation.ReviewStatus != "" {
		t.Fatal("an observation is not an artifact an operator accepts or rejects, " +
			"so it must carry no review status")
	}
}

// The inventory enumerates the kinds evaluation does not review, with a named
// reason and an explicit statement about what this build could count. A silent
// omission would leave an operator unable to tell an exempt kind from a
// forgotten one.
func TestInventoryEnumeratesNonReviewableKinds(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)

	source := NewSource(store, nil, nil)
	inventory, ok := source.(Inventory)
	if !ok {
		t.Fatal("the source must be able to enumerate produced kinds")
	}
	entries, err := inventory.Produced(ctx)
	if err != nil {
		t.Fatalf("produced: %v", err)
	}
	byKind := make(map[string]KindInventory, len(entries))
	for _, entry := range entries {
		byKind[entry.Kind] = entry
	}
	for _, kind := range Kinds() {
		entry, ok := byKind[kind]
		if !ok {
			t.Fatalf("reviewable kind %s must be in the inventory", kind)
		}
		if !entry.Reviewable || entry.Reason != "" {
			t.Fatalf("%s is reviewable and must carry no exemption: %+v", kind, entry)
		}
	}
	for _, kind := range NonReviewableKinds() {
		entry, ok := byKind[kind]
		if !ok {
			t.Fatalf("non-reviewable kind %s must still be enumerated", kind)
		}
		if entry.Reviewable {
			t.Fatalf("%s must not be marked reviewable", kind)
		}
		if entry.Reason == "" {
			t.Fatalf("%s must carry a named policy reason", kind)
		}
	}
	if byKind[SubjectKindHypothesis].Local != 1 {
		t.Fatalf("the local candidate must be counted, got %+v", byKind[SubjectKindHypothesis])
	}
	if byKind["receipt"].LocalCounted {
		t.Fatal("a kind this build cannot count locally must say so rather than report zero")
	}
	if byKind[SubjectKindHypothesis].FleetCounted {
		t.Fatal("with no fleet wired, nothing may claim a fleet count")
	}
}

// A subject kind this package owns is not resolvable as a frontier record, and
// an unknown kind is refused rather than interpreted. Casting a subject kind
// into an entity type would hand a store a table it does not have.
func TestSubjectResolutionRefusesWhatItCannotAnswer(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)
	source := NewSource(store, nil, nil)

	if _, err := source.Artifact(ctx, Subject{Kind: SubjectKindEvaluation, ID: "rec-1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an evaluation record is resolved as a record, not an artifact, got %v", err)
	}
	if _, err := source.Artifact(ctx, Subject{Kind: "sessions", ID: "s1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown subject kind must be refused, got %v", err)
	}
	if _, err := source.Artifact(ctx, Subject{Kind: SubjectKindHypothesis, ID: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a subject naming no record must be refused, got %v", err)
	}
	if _, err := source.Artifact(ctx, Subject{Kind: SubjectKindHypothesis, ID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a record this deployment does not hold must be ErrNotFound, got %v", err)
	}
}

// An evaluation record lookup distinguishes "the fleet does not hold it" from
// "I could not look". Recording the second as the first would let a criteria
// record be refused for naming a publication that exists.
func TestRecordResolverDistinguishesAbsenceFromOutage(t *testing.T) {
	ctx := context.Background()
	store, _ := newLocalFrontier(t)

	local := NewSource(store, nil, nil).(RecordResolver)
	if _, err := local.EvaluationRecord(ctx, "rec-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("in local mode a record is definitely not published, got %v", err)
	}

	failing := NewSource(store, nil, &fakeFleet{listErr: errors.New("catalog unreachable")})
	if _, err := failing.(RecordResolver).EvaluationRecord(ctx, "rec-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a failed lookup must never read as absence, got %v", err)
	}

	empty := NewSource(store, nil, &fakeFleet{})
	if _, err := empty.(RecordResolver).EvaluationRecord(ctx, "rec-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a successful read that found nothing is absence, got %v", err)
	}
}
