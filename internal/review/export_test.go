package review_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/review"
)

// The planted sentinels are synthetic and say so in their own bytes. A
// detector for a credential *format* can only be exercised against a string in
// that format, and a contiguous literal in that format makes this repository's
// push protection reject every push carrying the file — so each is assembled
// from parts. The source never holds the matching byte sequence; the assembled
// constant is byte-identical for the detector under test. See the same note in
// internal/preflight/secret_test.go.
const (
	plantedVendorToken = "ghp_" + "PROBEONLYNOTREALREVIEWEXPORT"
	plantedKeyID       = "AKIA" + "PROBEONLYNOTREAL"
)

// TestExportRoundTripsEveryField encodes an export, decodes it, and encodes it
// again. A document that survives that has lost nothing: §6.7's Phase B export
// is the private view whole, and a field silently dropped on the way out would
// make an export look complete while being less than the record.
func TestExportRoundTripsEveryField(t *testing.T) {
	h := newHarness(t)
	prop := h.richProposal()
	subject := frontier.Ref{Type: frontier.EntityProposal, ID: prop.ID}
	guidance, err := h.svc.RecordContext(h.ctx, h.op, "the second run showed the opposite")
	if err != nil {
		t.Fatalf("RecordContext: %v", err)
	}
	if _, err := h.svc.Decide(h.ctx, review.Decision{
		Subject:     subject,
		Disposition: frontier.DispositionDefer,
		By:          h.op,
		ContextID:   guidance.ID,
		Note:        "waiting on the next run",
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	doc, err := h.svc.Export(h.ctx, review.Node{Kind: review.KindProposal, ID: prop.ID}, review.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	encoded, err := doc.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var decoded review.Export
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	again, err := decoded.JSON()
	if err != nil {
		t.Fatalf("re-encode export: %v", err)
	}
	if !bytes.Equal(encoded, again) {
		t.Fatalf("export did not round-trip:\nfirst:\n%s\nsecond:\n%s", encoded, again)
	}

	// And the exported record is the stored record, field for field.
	if decoded.Proposal == nil {
		t.Fatal("decoded export holds no proposal")
	}
	// The whole payload survives, not a sample of it: the fixture sets
	// every optional field, and a single dropped one fails here.
	if !reflect.DeepEqual(decoded.Proposal.Payload, prop.Payload) {
		t.Errorf("payload changed on the way out:\n got %+v\nwant %+v",
			decoded.Proposal.Payload, prop.Payload)
	}
	if got, want := decoded.Proposal.ID, prop.ID; got != want {
		t.Errorf("id = %q, want %q", got, want)
	}
	if !decoded.Proposal.CreatedAt.Equal(prop.CreatedAt) {
		t.Errorf("created = %s, want %s", decoded.Proposal.CreatedAt, prop.CreatedAt)
	}
	if !reflect.DeepEqual(decoded.Proposal.FindingIDs, prop.FindingIDs) ||
		!reflect.DeepEqual(decoded.Proposal.HypothesisIDs, prop.HypothesisIDs) {
		t.Errorf("lineage changed on the way out: %+v / %+v",
			decoded.Proposal.FindingIDs, decoded.Proposal.HypothesisIDs)
	}
	if decoded.Proposal.ReviewStatus != frontier.ReviewDeferred {
		t.Errorf("review status = %q, want deferred", decoded.Proposal.ReviewStatus)
	}
	if decoded.Review == nil || len(decoded.Review.Decisions) != 1 {
		t.Fatalf("review state = %+v, want one decision", decoded.Review)
	}
	if decoded.Review.Decisions[0].Context == nil ||
		decoded.Review.Decisions[0].Context.Author != "operator-1" {
		t.Errorf("attributed context lost on the way out: %+v", decoded.Review.Decisions[0].Context)
	}
}

// TestExportCarriesTheFallibilityStatement. §1 and the README are explicit that
// analytical output is fallible rather than an audit, and this is the artifact
// that leaves Babel.
func TestExportCarriesTheFallibilityStatement(t *testing.T) {
	h := newHarness(t)
	prop := h.chain("verify independently")
	doc, err := h.svc.Export(h.ctx, review.Node{Kind: review.KindProposal, ID: prop.ID}, review.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.Notice != review.Notice {
		t.Fatalf("notice = %q, want the package constant", doc.Notice)
	}
	for _, phrase := range []string{"not an audit", "fallible", "for human review", "untrusted evidence"} {
		if !strings.Contains(doc.Notice, phrase) {
			t.Errorf("notice does not say %q: %q", phrase, doc.Notice)
		}
	}
	encoded, err := doc.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !bytes.Contains(encoded, []byte("not an audit")) {
		t.Error("the JSON export does not carry the notice")
	}
	markdown, err := doc.Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !bytes.Contains(markdown, []byte("not an audit")) {
		t.Error("the Markdown export does not carry the notice")
	}
	// The notice comes before any record content.
	noticeAt := bytes.Index(markdown, []byte("not an audit"))
	recordAt := bytes.Index(markdown, []byte("## Proposal"))
	if noticeAt < 0 || recordAt < 0 || noticeAt > recordAt {
		t.Errorf("the notice is not ahead of the record: notice at %d, record at %d", noticeAt, recordAt)
	}
}

// TestExportRedactsPlantedSecretsAndPreservesLocators is §3's export rule and
// §4.3's traceability rule at the same time, because they pull in opposite
// directions: a blanket redaction of every string would hide the credential
// and destroy the digests that make the claim reopenable.
func TestExportRedactsPlantedSecretsAndPreservesLocators(t *testing.T) {
	h := newHarness(t)
	hyp := h.hypothesis("verification may be reported rather than performed")
	evidence := h.evidence("session-a", 12, "the log line read "+plantedVendorToken)
	obs := h.observation(hyp.ID, "the deploy log printed "+plantedKeyID+" before it exited", evidence)

	doc, err := h.svc.Export(h.ctx, review.Node{Kind: review.KindObservation, ID: obs.ID}, review.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	encoded, err := doc.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	markdown, err := doc.Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	for name, planted := range map[string]string{
		"claim sentinel":    plantedKeyID,
		"evidence sentinel": plantedVendorToken,
	} {
		if bytes.Contains(encoded, []byte(planted)) {
			t.Errorf("the JSON export leaked the %s", name)
		}
		if bytes.Contains(markdown, []byte(planted)) {
			t.Errorf("the Markdown export leaked the %s", name)
		}
	}
	if doc.Redaction.Values < 2 {
		t.Errorf("redaction count = %d, want at least 2", doc.Redaction.Values)
	}
	if !doc.Redaction.Applied || doc.Redaction.Policy != review.RedactionPolicy {
		t.Errorf("redaction = %+v, want applied under %q", doc.Redaction, review.RedactionPolicy)
	}
	if !strings.Contains(doc.Observation.Payload.Claim, preflight.Placeholder(plantedKeyID)) {
		t.Errorf("the claim does not carry the stable placeholder: %q", doc.Observation.Payload.Claim)
	}

	// The locators are byte-identical to the ones the record stored, in the
	// record and in the collected list.
	want := locator("session-a", 12)
	if got := doc.Observation.Payload.Evidence[0].Locator(); got != want {
		t.Errorf("evidence locator = %+v, want %+v", got, want)
	}
	if len(doc.Locators) != 1 || doc.Locators[0] != want {
		t.Errorf("collected locators = %+v, want one %+v", doc.Locators, want)
	}
	if !bytes.Contains(encoded, []byte(want.Digest)) {
		t.Error("the export does not carry the evidence digest")
	}
	if !bytes.Contains(encoded, []byte(obs.ID)) {
		t.Error("the export does not carry the record identifier")
	}

	// The note beside the redacted locator survives apart from the
	// credential, so the citation still says what it shows.
	if !strings.Contains(doc.Observation.Payload.Evidence[0].Note(), "the log line read ") {
		t.Errorf("redaction removed the note's context: %q", doc.Observation.Payload.Evidence[0].Note())
	}
}

// TestRawExportIsAnExplicitAct. §3 makes redaction the default and §8 makes raw
// bytes an explicit private export, so the zero-value option redacts.
func TestRawExportIsAnExplicitAct(t *testing.T) {
	h := newHarness(t)
	hyp := h.hypothesis("the deploy log printed " + plantedKeyID + " before it exited")

	redacted, err := h.svc.Export(h.ctx, review.Node{Kind: review.KindHypothesis, ID: hyp.ID}, review.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.Contains(redacted.Hypothesis.Payload.Statement, plantedKeyID) {
		t.Fatal("the default export leaked the sentinel")
	}
	raw, err := h.svc.Export(h.ctx, review.Node{Kind: review.KindHypothesis, ID: hyp.ID}, review.ExportOptions{Raw: true})
	if err != nil {
		t.Fatalf("Export raw: %v", err)
	}
	if !strings.Contains(raw.Hypothesis.Payload.Statement, plantedKeyID) {
		t.Fatal("the explicit raw export redacted anyway")
	}
	if raw.Redaction.Applied || raw.Redaction.Values != 0 {
		t.Errorf("raw redaction = %+v, want not applied", raw.Redaction)
	}
}

// TestExportOfARunReceipt covers §6.7's other raw record kind.
func TestExportOfARunReceipt(t *testing.T) {
	h := newHarness(t)
	receipt := h.receipt("search for claimed verification", "the worker never started")

	doc, err := h.svc.Export(h.ctx,
		review.Node{Kind: review.KindRun, ID: string(receipt.Header.ID)}, review.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.Run == nil {
		t.Fatal("the export holds no run receipt")
	}
	if doc.Run.Header.ID != receipt.Header.ID || doc.Run.Header.RunID != receipt.Header.RunID {
		t.Errorf("header = %+v, want %+v", doc.Run.Header, receipt.Header)
	}
	if len(doc.Run.Preparation.Selection) != 1 {
		t.Fatalf("preparation selection = %d entries, want 1", len(doc.Run.Preparation.Selection))
	}
	// The preparation is content-addressed, so an export must not have
	// rewritten a byte of it.
	if err := doc.Run.Preparation.Verify(); err != nil {
		t.Errorf("the exported preparation no longer derives its id: %v", err)
	}
	if got, want := len(doc.Locators), 2; got != want {
		t.Errorf("collected locators = %d, want %d", got, want)
	}
	if doc.Review != nil {
		t.Errorf("a run receipt carries review state: %+v", doc.Review)
	}
	if _, err := doc.Markdown(); err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	encoded, err := doc.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var decoded review.Export
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	again, err := decoded.JSON()
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if !bytes.Equal(encoded, again) {
		t.Error("a run export did not round-trip")
	}
}

// TestExportRefusesWhatItCannotRender keeps an export from claiming to hold a
// record it never read.
func TestExportRefusesWhatItCannotRender(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		name    string
		node    review.Node
		wantErr error
	}{
		{"an unknown proposal", review.Node{Kind: review.KindProposal, ID: "prp_missing"}, review.ErrUnknownRecord},
		{"an unknown run", review.Node{Kind: review.KindRun, ID: "rcpt-missing"}, review.ErrUnknownRecord},
		{"a review-owned kind", review.Node{Kind: review.KindAssessment, ID: "asm_1"}, review.ErrInvalidValue},
		{"no identifier", review.Node{Kind: review.KindProposal}, review.ErrInvalidValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.svc.Export(h.ctx, tc.node, review.ExportOptions{}); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Export error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// richProposal builds a proposal with every optional field populated, so the
// round-trip test is about the whole record rather than the fields a minimal
// fixture happens to set.
func (h *harness) richProposal() frontier.Proposal {
	h.t.Helper()
	hyp := h.hypothesis("verification may be reported rather than performed")
	obs := h.observation(hyp.ID, "the agent claimed the tests passed without running them")
	fnd := h.finding([]string{obs.ID}, "claimed verification")
	record, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-1",
		FindingIDs: []string{fnd.ID},
		Payload: frontier.ProposalPayload{
			Title:          "verify independently of the reported outcome",
			Problem:        "the verification step is skipped when the previous step reports success",
			Outcome:        "require a cited command output before a step is considered verified",
			Applicability:  "every repository using the shared verification helper",
			TemporalStatus: frontier.TemporalStillApplicable,
			Supporting: []frontier.Evidence{
				h.evidence("session-a", 12, "the claim was made here"),
				h.evidence("session-b", 44, "and repeated here"),
			},
			Conflicting:    []frontier.Evidence{h.evidence("session-c", 7, "one run did cite output")},
			Uncertainty:    "three sessions is not a pattern",
			Impact:         frontier.ImpactHigh,
			EstimatedScope: "one helper and its callers",
			Targets: []frontier.Target{{
				System:     "the shared verification helper",
				Confidence: frontier.ConfidenceModerate,
				Rationale:  "every affected session went through it",
			}},
			Risks:                []string{"the check may be noisy on generated code"},
			OpenQuestions:        []string{"does the helper have a quiet mode"},
			Prerequisites:        []string{"the helper must be able to capture output"},
			VerificationCriteria: []string{"a run with no cited output fails"},
			Classification:       frontier.ClassificationRedactionRequired,
			Destinations:         []frontier.Destination{frontier.DestinationIssue, frontier.DestinationBrief},
		},
	})
	if err != nil {
		h.t.Fatalf("CreateProposal: %v", err)
	}
	return record
}
