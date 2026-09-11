package web

// The detail routes' shared-catalog fallback (issue #208).
//
// The defect these cover is one an operator hit. The listings read the whole
// deployment, so a row he clicked named a record this machine's durable store
// has never held, and the detail route behind that row answered 404 — which
// reads as Babel having lost the work rather than as a store boundary. Every
// test here asks the question he asked: open the record this page has just
// offered me.
//
// The deployment is fleet_test.go's fixture, which is what makes a record
// another machine committed real without a catalog, an object store or a key.
// The local durable store is the real one, so "absent locally" is an absence
// internal/frontier reported rather than one a fake was told to report.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// detailText is woven through the fixture deployment, so every payload
// assertion below is against content the fixture chose rather than against a
// field being merely non-empty.
const detailText = "carried across the deployment"

// TestDetailRoutesOpenARecordOnlyTheCatalogHolds is the regression. Each route
// is asked for a record the fixture deployment holds and this machine's durable
// store does not, and each must render it: before the fallback every one of
// these answered 404.
func TestDetailRoutesOpenARecordOnlyTheCatalogHolds(t *testing.T) {
	h := newPhaseB(t, detailText, nil)

	t.Run("candidate", func(t *testing.T) {
		var detail hypothesisDetail
		decodeResponse(t, h.ok(t, "/api/hypothesis?id=frec-remote"), &detail)
		if detail.Hypothesis.Payload.Statement != detailText {
			t.Errorf("statement = %q, want the record's own wording %q",
				detail.Hypothesis.Payload.Statement, detailText)
		}
		if detail.Hypothesis.ID != "frec-remote" || detail.Hypothesis.RunID != "frun-remote" {
			t.Errorf("candidate = %+v", detail.Hypothesis)
		}
		if detail.Hypothesis.Status != string(frontier.StatusInvestigating) {
			t.Errorf("status = %q, want the state the record carried when it was staged",
				detail.Hypothesis.Status)
		}
		if detail.Hypothesis.SchemaVersion != frontier.RecordSchema {
			t.Errorf("schema version = %d", detail.Hypothesis.SchemaVersion)
		}
		// The derivations this machine makes over records it holds are absent
		// rather than zero: a review status rendered as a fact would say a
		// record nobody here has read is awaiting its first reading.
		if detail.Hypothesis.ReviewStatus != "" {
			t.Errorf("review status = %q, want absent", detail.Hypothesis.ReviewStatus)
		}
		if detail.SyncDegraded || detail.SyncDetail != "" {
			t.Errorf("a record that opened carries a notice: %+v", detail.syncNotice)
		}
		// Empty rather than null, because a client renders a list.
		if detail.StatusHistory == nil || detail.Observations == nil ||
			detail.Links == nil || detail.Proposals == nil ||
			detail.Lineage.Ancestors == nil || detail.Lineage.Descendants == nil {
			t.Errorf("absent material renders as null: %+v", detail)
		}
		if detail.Lineage.Node.ID != "frec-remote" {
			t.Errorf("lineage names %+v, not the record asked for", detail.Lineage.Node)
		}
	})

	t.Run("consolidation", func(t *testing.T) {
		var detail findingDetail
		decodeResponse(t, h.ok(t, "/api/finding?id=frec-remote-finding"), &detail)
		if detail.Finding.Payload.Title != detailText {
			t.Errorf("title = %q, want %q", detail.Finding.Payload.Title, detailText)
		}
		if detail.Finding.Payload.Pattern == "" {
			t.Error("the consolidation renders without the pattern it explains")
		}
		if detail.Finding.ID != "frec-remote-finding" || detail.Finding.RunID != "frun-remote" {
			t.Errorf("consolidation = %+v", detail.Finding)
		}
		if detail.Finding.ReviewStatus != "" {
			t.Errorf("review status = %q, want absent", detail.Finding.ReviewStatus)
		}
		if detail.Observations == nil || detail.Proposals == nil ||
			detail.Finding.ObservationIDs == nil || detail.Finding.HypothesisIDs == nil {
			t.Errorf("absent material renders as null: %+v", detail)
		}
	})

	t.Run("proposal", func(t *testing.T) {
		var detail proposalDetail
		decodeResponse(t, h.ok(t, "/api/proposals/frec-remote-proposal"), &detail)
		wantTitle := "retire the duplicated manifest read " + detailText
		if detail.Title != wantTitle {
			t.Errorf("title = %q, want the projection the listing row shows: %q",
				detail.Title, wantTitle)
		}
		if detail.Payload.Title != wantTitle || detail.Payload.Problem == "" {
			t.Errorf("payload = %+v, want the whole stored document", detail.Payload)
		}
		// The four fields the listing deliberately leaves empty are what a
		// reader opened this page for.
		if detail.Problem == "" || detail.Outcome == "" ||
			detail.Impact != string(frontier.ImpactModerate) ||
			detail.Classification != string(frontier.ClassificationPrivate) {
			t.Errorf("proposal row = %+v", detail.ProposalSummary)
		}
		// #114's provenance: a want rendered with a consolidation's authority
		// is the failure the split exists to prevent, so the form travels.
		if detail.Form != string(frontier.ProposalConsolidated) {
			t.Errorf("form = %q, want the form its subjects make it", detail.Form)
		}
		if len(detail.FindingIDs) != 1 || detail.FindingIDs[0] != "frec-remote-finding" {
			t.Errorf("finding ids = %#v", detail.FindingIDs)
		}
		if detail.HypothesisIDs == nil {
			t.Error("absent material renders as null")
		}
	})

	// The review surface reads the history first and renders nothing without
	// it, so a 404 here took the record's own text off the screen as well as
	// its decisions.
	t.Run("review history", func(t *testing.T) {
		for _, path := range []string{
			"/api/review/history?type=hypothesis&id=frec-remote",
			"/api/review/history?type=finding&id=frec-remote-finding",
			"/api/review/history?type=proposal&id=frec-remote-proposal",
		} {
			var history historyResult
			decodeResponse(t, h.ok(t, path), &history)
			if history.Decisions == nil || history.Refinements == nil {
				t.Errorf("%s: absent history renders as null: %+v", path, history)
			}
			if len(history.Decisions) != 0 || len(history.Refinements) != 0 {
				t.Errorf("%s: history = %+v, want none: this store holds no entry", path, history)
			}
			if history.Status != "" {
				t.Errorf("%s: status = %q, want absent rather than `new`", path, history.Status)
			}
		}
	})
}

// TestADetailRouteNarrowedToThisMachineRefusesACatalogRecord is the other half
// of the scope decision: `?fleet=0` asks what this machine holds, and a record
// it does not hold is legitimately absent.
func TestADetailRouteNarrowedToThisMachineRefusesACatalogRecord(t *testing.T) {
	h := newPhaseB(t, detailText, nil)
	for _, path := range []string{
		"/api/hypothesis?id=frec-remote&fleet=0",
		"/api/finding?id=frec-remote-finding&fleet=0",
		"/api/proposals/frec-remote-proposal?fleet=0",
		"/api/review/history?type=hypothesis&id=frec-remote&fleet=0",
	} {
		response := h.get(path)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, response.StatusCode)
		}
	}
	// Staged output is not what a fleet-wide listing offered, so it is not
	// what a detail route reaches for either: a record nobody can click from
	// a listing must not become reachable by identifier alone.
	response := h.get("/api/hypothesis?id=frec-pending")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("staged record status = %d, want 404", response.StatusCode)
	}
	// And a record no deployment holds is still absent.
	response = h.get("/api/hypothesis?id=hyp-nothing-holds-this")
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("unknown record status = %d, want 404", response.StatusCode)
	}
}

// TestADetailRouteDegradesWhenTheRecordCannotBeRead is the outage half.
//
// A catalog that did not answer leaves this surface unable to say whether the
// record exists, and the response says that about the record: no machine, no
// sync state, no instance. Refusing with a 500 would present an unreachable
// catalog as a broken Babel, and answering 404 would claim an absence nothing
// observed.
func TestADetailRouteDegradesWhenTheRecordCannotBeRead(t *testing.T) {
	h := newPhaseB(t, detailText, nil)
	h.fleetOf().fail = leakyError

	for _, path := range []string{
		"/api/hypothesis?id=frec-remote",
		"/api/finding?id=frec-remote-finding",
		"/api/proposals/frec-remote-proposal",
		"/api/review/history?type=hypothesis&id=frec-remote",
	} {
		response := h.get(path)
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Errorf("%s: status = %d, want 200 with a notice", path, response.StatusCode)
			continue
		}
		var notice syncNotice
		text := jsonBody(t, response, &notice)
		if !notice.SyncDegraded {
			t.Errorf("%s: an unreadable record carries no notice: %s", path, text)
		}
		assertRecordTerms(t, path, notice.SyncDetail)
		// The catalog's own error names a database this reader must never
		// publish, and §9 keeps it out of the response as well as the log.
		if strings.Contains(text, "db.example") || strings.Contains(text, "postgres://") {
			t.Errorf("%s: the response quotes the failed read: %s", path, text)
		}
	}

	// The records this machine holds keep opening. An operator whose catalog
	// is down still owns every record on this disk.
	var local hypothesisDetail
	decodeResponse(t, h.ok(t, "/api/hypothesis?id="+h.hypothesis.ID), &local)
	if local.Hypothesis.Payload.Statement == "" || local.SyncDegraded {
		t.Errorf("a record this machine holds degraded with the catalog: %+v", local)
	}
}

// TestADetailRouteSaysWhenARecordCannotBeOpened covers the second unreadable
// case: the catalog answered, and the object is sealed under a key this
// instance does not hold.
//
// The reason internal/fleet gives names that key, which is custody diagnostics
// for an operator at a terminal. What reaches the page is one sentence about
// the record.
func TestADetailRouteSaysWhenARecordCannotBeOpened(t *testing.T) {
	h := newPhaseB(t, detailText, nil)

	response := h.get("/api/hypothesis?id=frec-sealed")
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("status = %d, want 200 with a notice", response.StatusCode)
	}
	var detail hypothesisDetail
	text := jsonBody(t, response, &detail)
	if !detail.SyncDegraded {
		t.Errorf("a record that could not be opened carries no notice: %s", text)
	}
	assertRecordTerms(t, "/api/hypothesis?id=frec-sealed", detail.SyncDetail)
	if strings.Contains(text, "sealed under key") {
		t.Errorf("the page repeats the custody diagnostic: %s", text)
	}
	if detail.Hypothesis.ID != "frec-sealed" {
		t.Errorf("the notice does not name the record it failed on: %+v", detail.Hypothesis)
	}
}

// TestADetailRouteDoesNotMaskAFailedLocalRead is the condition the fallback
// turns on, asserted from the outside.
//
// Only an absence falls back. A durable store that failed is reported as a
// failure even though the catalog holds a record under the same identifier:
// answering that read with the catalog's copy would turn a repairable local
// fault into a page that looks fine, which is how a broken database goes
// unnoticed for a week.
func TestADetailRouteDoesNotMaskAFailedLocalRead(t *testing.T) {
	h := newPhaseB(t, detailText, func(opts *Options) {
		opts.Frontier = brokenFrontier{}
		opts.Review = failingServices{}
	})
	for _, path := range []string{
		"/api/hypothesis?id=frec-remote",
		"/api/finding?id=frec-remote-finding",
		"/api/proposals/frec-remote-proposal",
		"/api/review/history?type=hypothesis&id=frec-remote",
	} {
		response := h.get(path)
		text := body(t, response)
		if response.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500: a failed read is not an absence",
				path, response.StatusCode)
		}
		if !strings.Contains(text, "could not be completed") {
			t.Errorf("%s: response does not name the problem: %s", path, text)
		}
	}
}

// brokenFrontier is a durable store that fails rather than reporting an
// absence, which is the one local error the fallback must not read as "this
// machine does not hold it".
type brokenFrontier struct{ FrontierReader }

func (brokenFrontier) Hypothesis(context.Context, string) (frontier.Hypothesis, error) {
	return frontier.Hypothesis{}, leakyError
}

func (brokenFrontier) Finding(context.Context, string) (frontier.Finding, error) {
	return frontier.Finding{}, leakyError
}

func (brokenFrontier) Proposal(context.Context, string) (frontier.Proposal, error) {
	return frontier.Proposal{}, leakyError
}

// assertRecordTerms is the product rule these notices are held to: a reader
// asked about a record, so the sentence he gets is about that record. Which
// machine published it, whether it has committed and what this instance holds
// are not answers to what he asked, and the vocabulary for them has no place
// on a reading surface.
func assertRecordTerms(t *testing.T, path, notice string) {
	t.Helper()
	if notice == "" {
		t.Errorf("%s: the degraded response says nothing", path)
		return
	}
	for _, word := range []string{
		"host", "machine", "sync", "instance", "local", "pending", "committed", "laptop",
	} {
		if strings.Contains(strings.ToLower(notice), word) {
			t.Errorf("%s: notice %q talks about %q rather than about the record", path, notice, word)
		}
	}
}

// ok performs the GET a reader's click makes and refuses anything but an
// answer. It takes the subtest's own t so a failure lands on the case that
// caused it rather than on the fixture.
func (h *phaseB) ok(t *testing.T, path string) *http.Response {
	t.Helper()
	response := h.get(path)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("GET %s: status = %d, want 200", path, response.StatusCode)
	}
	return response
}

// jsonBody decodes a response and keeps its bytes, because the assertions
// about an unreadable record are of both kinds: what the notice says, and what
// the document must not carry anywhere at all — a reason that leaked into a
// field no DTO declares would decode into nothing and still reach a browser.
func jsonBody(t *testing.T, response *http.Response, into any) string {
	t.Helper()
	text := body(t, response)
	if err := json.Unmarshal([]byte(text), into); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return text
}
