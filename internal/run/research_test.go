package run

import (
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

var fetchedAt = time.Date(2026, 8, 29, 10, 4, 0, 0, time.UTC)

// testResearchStep is one brokered public fetch as internal/explore records
// it: the four fields SPEC.md §2.6 requires a fetch to return, and no content.
func testResearchStep(index int) RetrievalStep {
	return RetrievalStep{
		Index: index,
		Tool:  "public-research",
		Scope: ScopeResearch,
		Query: "res-9f2c1a4b7e30",
		At:    fetchedAt,
		Research: &ResearchSource{
			SourceID:    "res-9f2c1a4b7e30",
			URL:         "https://example.com/spec/section-2",
			RetrievedAt: fetchedAt,
			Redirects:   []string{"https://www.example.com/spec/section-2"},
			MediaType:   "text/html",
			Digest:      digest.Bytes([]byte("the fetched document")),
			Bytes:       20,
		},
	}
}

// TestResearchTraceSurvivesRecording checks that a brokered fetch reaches the
// durable record whole. The provenance is the whole point of the facility: a
// receipt that recorded "a fetch happened" and lost the URL and the digest
// would leave the operator unable to say what their run read.
func TestResearchTraceSurvivesRecording(t *testing.T) {
	body := testBody(t)
	body.Retrieval = append(body.Retrieval, testResearchStep(len(body.Retrieval)+1))
	prep := mustPreparation(t, preparedAt, testSelection())

	receipt, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded)
	if err != nil {
		t.Fatalf("NewReceipt refused a brokered fetch: %v", err)
	}
	stored := receipt.Body.Retrieval[len(receipt.Body.Retrieval)-1]
	want := testResearchStep(stored.Index)
	if stored.Scope != ScopeResearch || stored.Research == nil {
		t.Fatalf("stored step = %+v, want a research step with its source", stored)
	}
	got := *stored.Research
	if got.URL != want.Research.URL || got.SourceID != want.Research.SourceID {
		t.Errorf("stored source = %+v, want %+v", got, *want.Research)
	}
	if got.Digest != want.Research.Digest || got.Bytes != want.Research.Bytes {
		t.Errorf("stored digest/size = %s/%d, want %s/%d",
			got.Digest, got.Bytes, want.Research.Digest, want.Research.Bytes)
	}
	if len(got.Redirects) != 1 || got.Redirects[0] != want.Research.Redirects[0] {
		t.Errorf("stored redirects = %v, want the chain that was followed", got.Redirects)
	}
	if !got.RetrievedAt.Equal(fetchedAt) {
		t.Errorf("stored retrieval time = %s, want %s", got.RetrievedAt, fetchedAt)
	}
	if receipt.Header.Counts.Retrieval != len(body.Retrieval) {
		t.Errorf("counted %d retrievals, want %d: a fetch is a retrieval",
			receipt.Header.Counts.Retrieval, len(body.Retrieval))
	}
}

// TestResearchTraceRefusesAnUnfollowableFetch covers the pairing and the
// required fields. Each case is a record that would say a run reached the
// public internet while making it impossible to check what it read.
func TestResearchTraceRefusesAnUnfollowableFetch(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(step *RetrievalStep)
	}{
		{name: "a research scope with no source",
			corrupt: func(s *RetrievalStep) { s.Research = nil }},
		{name: "a source recorded under another scope",
			corrupt: func(s *RetrievalStep) { s.Scope = "" }},
		{name: "a source under the frontier scope",
			corrupt: func(s *RetrievalStep) { s.Scope = "frontier" }},
		{name: "no identity",
			corrupt: func(s *RetrievalStep) { s.Research.SourceID = "" }},
		{name: "no URL",
			corrupt: func(s *RetrievalStep) { s.Research.URL = "" }},
		{name: "a URL that is not one value",
			corrupt: func(s *RetrievalStep) { s.Research.URL = "https://example.com/a b" }},
		{name: "a URL carrying a newline",
			corrupt: func(s *RetrievalStep) { s.Research.URL = "https://example.com/a\nb" }},
		{name: "no retrieval time",
			corrupt: func(s *RetrievalStep) { s.Research.RetrievedAt = time.Time{} }},
		{name: "no media type",
			corrupt: func(s *RetrievalStep) { s.Research.MediaType = "" }},
		{name: "no digest",
			corrupt: func(s *RetrievalStep) { s.Research.Digest = "" }},
		{name: "a digest that is not one",
			corrupt: func(s *RetrievalStep) { s.Research.Digest = "sha256:not-a-digest" }},
		{name: "a negative size",
			corrupt: func(s *RetrievalStep) { s.Research.Bytes = -1 }},
		{name: "a redirect nobody can follow",
			corrupt: func(s *RetrievalStep) { s.Research.Redirects = []string{"https://example.com/a b"} }},
	}
	prep := mustPreparation(t, preparedAt, testSelection())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := testBody(t)
			step := testResearchStep(len(body.Retrieval) + 1)
			tc.corrupt(&step)
			body.Retrieval = append(body.Retrieval, step)
			if _, err := NewReceipt(NewReceiptID(), "run-1", prep, testAuthority(), body, recorded); err == nil {
				t.Error("the receipt accepted a fetch record nobody could check")
			}
		})
	}
}

// TestRecordedResearchCannotBeEditedAfterwards is the immutability property
// applied to the one field of a step that is a pointer. A caller that keeps
// the document it handed over must not be able to rewrite the URL a receipt
// says was read.
func TestRecordedResearchCannotBeEditedAfterwards(t *testing.T) {
	body := testBody(t)
	step := testResearchStep(len(body.Retrieval) + 1)
	body.Retrieval = append(body.Retrieval, step)

	receipt, err := NewReceipt(NewReceiptID(), "run-1", prepFor(t), testAuthority(), body, recorded)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}
	step.Research.URL = "https://elsewhere.example/other"
	step.Research.Redirects[0] = "https://elsewhere.example/hop"

	stored := receipt.Body.Retrieval[len(receipt.Body.Retrieval)-1].Research
	if stored.URL != "https://example.com/spec/section-2" {
		t.Errorf("the stored URL followed the caller's edit: %s", stored.URL)
	}
	if stored.Redirects[0] != "https://www.example.com/spec/section-2" {
		t.Errorf("the stored redirect chain followed the caller's edit: %v", stored.Redirects)
	}
}

func prepFor(t *testing.T) Preparation {
	t.Helper()
	return mustPreparation(t, preparedAt, testSelection())
}
