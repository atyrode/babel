package frontier

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// These tests defend the one property the fleet read path is built on (issue
// #109 item 4): a record another host committed must flatten to exactly the
// Output the same record flattens to when it is read locally.
//
// If it does not, one search over two machines ranks and displays the same
// record two ways, and an operator comparing a local candidate against a remote
// one is comparing two derivations rather than two ideas.

// TestPublishedRecordFlattensIdenticallyToTheLocalPath is that property,
// asserted against the durable store rather than against a fixture: the local
// Output comes from Store.Outputs, the remote one comes from a wire record
// carrying the same row, and the two must agree field for field.
func TestPublishedRecordFlattensIdenticallyToTheLocalPath(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	hyp, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("the release pipeline skips the integration suite it claims to run", 0.7),
		Actor:   Run("run-1"),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	obs, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: hyp.ID,
		RunID:        "run-1",
		RecipeID:     "recipe-1",
		Payload: observationPayload("four sessions claim a verified deployment with no test run",
			mustEvidence(t, 3, "the deploy step logs success with no suite output")),
		Actor: Run("run-1"),
	})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	fnd, err := store.CreateFinding(ctx, FindingInput{
		ObservationIDs: []string{obs.ID},
		RunID:          "run-1",
		Payload:        findingPayload("deployment verification gaps"),
		Actor:          Run("run-1"),
	})
	if err != nil {
		t.Fatalf("create finding: %v", err)
	}
	if _, err := store.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityFinding, ID: fnd.ID},
		Disposition: DispositionReject,
		ReviewerID:  "operator",
		Note:        "too broad to act on as written",
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}

	local, err := store.Outputs(ctx)
	if err != nil {
		t.Fatalf("Outputs: %v", err)
	}
	if len(local) != 4 {
		t.Fatalf("the frontier offered %d outputs, want 4", len(local))
	}

	for _, want := range local {
		payload, answer := payloadFor(t, store, want)
		wire := PublishedRecord{
			Schema:    RecordSchema,
			Kind:      PublishedKind(want.Kind),
			ID:        want.ID,
			RootID:    want.RootID,
			Subject:   want.Subject,
			Answer:    answer,
			RunID:     want.RunID,
			Status:    want.Status,
			CreatedAt: want.CreatedAt,
			Payload:   payload,
		}
		sealed, err := wire.Marshal()
		if err != nil {
			t.Fatalf("marshal %s %s: %v", want.Kind, want.ID, err)
		}
		decoded, err := DecodePublishedRecord(sealed)
		if err != nil {
			t.Fatalf("decode %s %s: %v", want.Kind, want.ID, err)
		}
		got, err := decoded.Output()
		if err != nil {
			t.Fatalf("flatten %s %s: %v", want.Kind, want.ID, err)
		}
		if got.Kind != want.Kind || got.ID != want.ID || got.RootID != want.RootID ||
			got.Subject != want.Subject || got.RunID != want.RunID || got.Status != want.Status ||
			!got.CreatedAt.Equal(want.CreatedAt) {
			t.Errorf("%s %s: structural fields differ\n  remote: %+v\n  local:  %+v",
				want.Kind, want.ID, got, want)
		}
		if got.Summary != want.Summary {
			t.Errorf("%s %s: summary differs\n  remote: %q\n  local:  %q",
				want.Kind, want.ID, got.Summary, want.Summary)
		}
		if got.Text != want.Text {
			t.Errorf("%s %s: indexed text differs\n  remote: %q\n  local:  %q",
				want.Kind, want.ID, got.Text, want.Text)
		}
	}
}

// payloadFor reads the payload bytes a publisher would stage for one output,
// straight out of the row, plus the review-answer fields that live in columns
// rather than in the payload.
//
// It reads the raw column rather than re-encoding a decoded struct, because
// re-encoding is exactly the lossy step the wire form exists to avoid: the
// bytes a publisher seals are the bytes the row holds.
func payloadFor(t *testing.T, store *Store, out Output) ([]byte, *PublishedAnswer) {
	t.Helper()
	ctx := context.Background()
	switch out.Kind {
	case OutputHypothesis:
		return column(t, store, `SELECT payload_json FROM frontier_hypothesis WHERE id = ?`, out.ID), nil
	case OutputObservation:
		return column(t, store, `SELECT payload_json FROM frontier_observation WHERE id = ?`, out.ID), nil
	case OutputFinding:
		return column(t, store, `SELECT payload_json FROM frontier_finding WHERE id = ?`, out.ID), nil
	case OutputReviewAnswer:
		var decision, reviewer string
		err := store.db.QueryRowContext(ctx,
			`SELECT disposition, reviewer_id FROM frontier_disposition WHERE id = ?`, out.ID).
			Scan(&decision, &reviewer)
		if err != nil {
			// The other form a review answer takes: a refinement request,
			// which carries no decision and no reviewer column.
			return column(t, store,
				`SELECT payload_json FROM frontier_refinement_request WHERE id = ?`, out.ID), &PublishedAnswer{}
		}
		return column(t, store, `SELECT payload_json FROM frontier_disposition WHERE id = ?`, out.ID),
			&PublishedAnswer{Decision: Disposition(decision), Reviewer: reviewer}
	}
	t.Fatalf("no payload source for kind %q", out.Kind)
	return nil, nil
}

func column(t *testing.T, store *Store, query, id string) []byte {
	t.Helper()
	var payload []byte
	if err := store.db.QueryRowContext(context.Background(), query, id).Scan(&payload); err != nil {
		t.Fatalf("read payload for %s: %v", id, err)
	}
	return payload
}

// A refinement request is the review answer whose decision is absent, and §4.7
// makes that absence meaningful: there is no standalone `refine` disposition,
// so an empty decision is what says "a rejection authorized this".
func TestPublishedRefinementAnswerFlattensIdentically(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	hyp, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("scheduled deployments report success without verification", 0.6),
		Actor:   Run("run-1"),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	_, refinement, err := store.RejectAndRefine(ctx,
		DispositionInput{
			Subject:    Ref{Type: EntityHypothesis, ID: hyp.ID},
			ReviewerID: "operator",
			Note:       "narrow it to the scheduled path",
		},
		RefinementPayload{
			Guidance: "restrict the claim to timer-triggered deployments",
			Scope:    []string{"scheduling"},
		})
	if err != nil {
		t.Fatalf("RejectAndRefine: %v", err)
	}

	answers, err := store.ReviewAnswers(ctx, Ref{Type: EntityHypothesis, ID: hyp.ID})
	if err != nil {
		t.Fatalf("ReviewAnswers: %v", err)
	}
	var want Output
	for _, a := range answers {
		if a.ID == refinement.ID {
			want = a
		}
	}
	if want.ID == "" {
		t.Fatalf("the refinement request is not among %d answers", len(answers))
	}

	payload, err := json.Marshal(RefinementPayload{
		Guidance: "restrict the claim to timer-triggered deployments",
		Scope:    []string{"scheduling"},
	})
	if err != nil {
		t.Fatalf("marshal refinement payload: %v", err)
	}
	wire := PublishedRecord{
		Schema: RecordSchema, Kind: PublishedReviewAnswer, ID: want.ID, RootID: want.RootID,
		Subject: want.Subject, Answer: &PublishedAnswer{}, CreatedAt: want.CreatedAt,
		Payload: payload,
	}
	sealed, err := wire.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := DecodePublishedRecord(sealed)
	if err != nil {
		t.Fatalf("DecodePublishedRecord: %v", err)
	}
	got, err := decoded.Output()
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if got.Summary != want.Summary || got.Text != want.Text {
		t.Errorf("a refinement answer flattens differently across hosts\n  remote: %q / %q\n  local:  %q / %q",
			got.Summary, got.Text, want.Summary, want.Text)
	}
}

// Validation is one function called in both directions, so a record refused on
// the way in would have been refused on the way out. These are the refusals
// that matter, and each is a thing a real deployment can produce.
func TestPublishedRecordValidation(t *testing.T) {
	payload := json.RawMessage(`{"statement":"a candidate"}`)
	base := func() PublishedRecord {
		return PublishedRecord{
			Schema: RecordSchema, Kind: PublishedHypothesis, ID: "hyp-1", RootID: "hyp-1",
			CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), Payload: payload,
		}
	}

	if _, err := base().Marshal(); err != nil {
		t.Fatalf("a well-formed record was refused: %v", err)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(*PublishedRecord)
		wantErr error
	}{
		{"no id", func(p *PublishedRecord) { p.ID = "" }, ErrInvalidValue},
		{"no schema", func(p *PublishedRecord) { p.Schema = 0 }, ErrInvalidValue},
		{"a schema this build cannot read",
			func(p *PublishedRecord) { p.Schema = RecordSchema + 1 }, ErrInvalidValue},
		{"no payload", func(p *PublishedRecord) { p.Payload = nil }, ErrInvalidValue},
		{"a kind outside the publication vocabulary",
			func(p *PublishedRecord) { p.Kind = "gossip" }, ErrInvalidValue},
		{"a link with no endpoints",
			func(p *PublishedRecord) { p.Kind = PublishedLink }, ErrInvalidValue},
		{"a link missing one endpoint",
			func(p *PublishedRecord) {
				p.Kind = PublishedLink
				p.Edge = &PublishedEdge{FromID: "hyp-1", Type: LinkSupersedes}
			}, ErrInvalidValue},
		{"a link with a type outside the vocabulary",
			func(p *PublishedRecord) {
				p.Kind = PublishedLink
				p.Edge = &PublishedEdge{FromID: "hyp-1", ToID: "hyp-2", Type: "resembles"}
			}, ErrInvalidValue},
		{"a candidate carrying link endpoints",
			func(p *PublishedRecord) {
				p.Edge = &PublishedEdge{FromID: "hyp-1", ToID: "hyp-2", Type: LinkSupersedes}
			}, ErrInvalidValue},
		{"a review answer with no decision or reviewer",
			func(p *PublishedRecord) {
				p.Kind = PublishedReviewAnswer
				p.Subject = Ref{Type: EntityFinding, ID: "fnd-1"}
			}, ErrInvalidValue},
		{"a review answer with no subject",
			func(p *PublishedRecord) {
				p.Kind = PublishedReviewAnswer
				p.Answer = &PublishedAnswer{Decision: DispositionReject, Reviewer: "operator"}
			}, ErrInvalidValue},
		{"a candidate carrying a review answer",
			func(p *PublishedRecord) {
				p.Answer = &PublishedAnswer{Decision: DispositionReject, Reviewer: "operator"}
			}, ErrInvalidValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := base()
			tc.mutate(&rec)
			if _, err := rec.Marshal(); !errors.Is(err, tc.wantErr) {
				t.Errorf("Marshal gave %v, want %v", err, tc.wantErr)
			}
			// The same rule must refuse it on the way in, or a record could
			// arrive that no writer could have produced.
			raw, err := json.Marshal(rec)
			if err != nil {
				t.Fatalf("encode for the decode direction: %v", err)
			}
			if _, err := DecodePublishedRecord(raw); !errors.Is(err, tc.wantErr) {
				t.Errorf("DecodePublishedRecord gave %v, want %v", err, tc.wantErr)
			}
		})
	}

	// Publishable and unsearchable are different questions, and the refusal
	// belongs to the second one. A proposal and a link must reach the shared
	// catalog - migrations/0003's closed vocabulary carries both, and SPEC.md
	// 4.7 makes lineage globally durable - and neither has a flattened
	// searchable view, so Marshal accepts them and Output declines them.
	for _, tc := range []struct {
		name string
		rec  func() PublishedRecord
	}{
		{"a proposal", func() PublishedRecord {
			r := base()
			r.Kind = PublishedProposal
			// #114 gives a proposal two forms and RestsOn is what tells
			// them apart, so a proposal that names nothing it rests on is
			// neither form and is refused before it becomes an object.
			r.RestsOn = []PublishedSubject{{Kind: EntityFinding, ID: "fnd-1"}}
			return r
		}},
		{"a link", func() PublishedRecord {
			r := base()
			r.Kind = PublishedLink
			r.Edge = &PublishedEdge{FromID: "hyp-1", ToID: "hyp-2", Type: LinkSupersedes}
			return r
		}},
	} {
		t.Run(tc.name+" publishes and does not search", func(t *testing.T) {
			rec := tc.rec()
			sealed, err := rec.Marshal()
			if err != nil {
				t.Fatalf("%s was refused publication: %v", tc.name, err)
			}
			decoded, err := DecodePublishedRecord(sealed)
			if err != nil {
				t.Fatalf("%s was refused on the way in: %v", tc.name, err)
			}
			if _, err := decoded.Output(); !errors.Is(err, ErrNotSearchable) {
				t.Errorf("Output for %s gave %v, want ErrNotSearchable", tc.name, err)
			}
		})
	}

	// Bytes that are not a published record at all fail as a decode rather than
	// yielding a record with empty fields.
	if _, err := DecodePublishedRecord([]byte(`not json`)); err == nil {
		t.Error("arbitrary bytes decoded as a published record")
	}
	if _, err := DecodePublishedRecord([]byte(`{"kind":"hypothesis"}`)); err == nil {
		t.Error("a record with no id, schema or payload decoded")
	}

	// A chain identity is defaulted to the record's own id, so a reader never
	// has to know which kinds have one.
	rec := base()
	rec.RootID = ""
	sealed, err := rec.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := DecodePublishedRecord(sealed)
	if err != nil {
		t.Fatalf("DecodePublishedRecord: %v", err)
	}
	out, err := decoded.Output()
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if out.RootID != rec.ID {
		t.Errorf("RootID = %q, want the record's own id %q", out.RootID, rec.ID)
	}
}

// The wire form carries no derived text. Shipping a producer's summary would
// create a second answer to a question describePayload already answers, and the
// two would drift the first time a summary's phrasing changed.
func TestPublishedRecordCarriesNoDerivedText(t *testing.T) {
	rec := PublishedRecord{
		Schema: RecordSchema, Kind: PublishedHypothesis, ID: "hyp-1", RootID: "hyp-1",
		Status:    StatusUntriaged,
		CreatedAt: time.Now().UTC(),
		Payload:   json.RawMessage(`{"statement":"a candidate about deployment"}`),
	}
	sealed, err := rec.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(sealed, &wire); err != nil {
		t.Fatalf("decode the wire object: %v", err)
	}
	for _, derived := range []string{"summary", "text"} {
		if _, present := wire[derived]; present {
			t.Errorf("the wire form carries %q, which the reader derives", derived)
		}
	}
	// And the fields a reader cannot recover from a plaintext row ARE carried,
	// because migrations/0003 has no column for either.
	for _, structural := range []string{"root_id", "status"} {
		if _, present := wire[structural]; !present {
			t.Errorf("the wire form omits %q, which no plaintext column holds", structural)
		}
	}
	// An empty ancestor and an empty subject stay off the wire rather than
	// travelling as zero values a reader would have to interpret.
	if _, present := wire["ancestor"]; present {
		t.Error("an absent ancestor was written as a field")
	}
	if _, present := wire["subject"]; present {
		t.Error("an absent subject was written as a field")
	}
	// Payload bytes travel unchanged: the reason Payload is json.RawMessage is
	// that nothing re-encodes model-authored text on the way out or in.
	if !strings.Contains(string(sealed), `"statement":"a candidate about deployment"`) {
		t.Errorf("the payload was re-encoded on the way out: %s", sealed)
	}
}
