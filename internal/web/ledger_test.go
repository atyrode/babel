package web

import (
	"net/http"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/reality"
)

// TestTheLedgerIsReadableWithoutKnowingAnIdentifier is §8.4's reachability
// requirement stated as a test: what the ledger holds has to be readable by a
// surface that was given nothing to start from.
//
// Each assertion stands for one thing that was previously unreachable. A
// question that has left the inbox — answered, snoozed, declined — appears in
// the listing with its state, because the inbox is by design only what the
// operator still has to move. An entity appears with what the ledger holds
// about it, because until the listing existed the only way to an entity page
// was an identifier from somewhere else. And a fact's page shows both ends of
// its revision chain, which is the one claim §4.8's append-only rule makes that
// a reader can check: the ancestor is still there, and the page says what
// replaced it.
func TestTheLedgerIsReadableWithoutKnowingAnIdentifier(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	t.Run("every question, not only the pending ones", func(t *testing.T) {
		// The plan-ready question is declined through the store, which
		// takes it out of the inbox exactly as answering it would. The
		// route under test is the listing, so the transition is made by
		// the service rather than by a write route this surface does
		// not have.
		if err := h.reality.SetQuestionState(h.ctx, reality.QuestionStateInput{
			QuestionID: h.question.ID,
			State:      reality.QuestionDeclined,
			Actor:      operatorID,
			Note:       "not worth answering",
		}); err != nil {
			t.Fatalf("SetQuestionState: %v", err)
		}

		var inbox inboxResult
		decodeResponse(t, h.get("/api/reality/inbox"), &inbox)
		for _, item := range inbox.Items {
			if item.ID == h.question.ID {
				t.Fatal("the declined question is still in the inbox")
			}
		}

		var listed questionsResult
		decodeResponse(t, h.get("/api/reality/questions"), &listed)
		var declined *questionRow
		for i, row := range listed.Items {
			if row.ID == h.question.ID {
				declined = &listed.Items[i]
			}
		}
		if declined == nil {
			t.Fatalf("the declined question is unreachable: %+v", listed.Items)
		}
		if declined.State != string(reality.QuestionDeclined) || declined.Pending {
			t.Errorf("declined question reads as state %q, pending %v", declined.State, declined.Pending)
		}
		if declined.Prompt == "" || declined.WhyAsked == "" {
			t.Error("the listing shows a question without its prompt or its reason")
		}
		// The census is of the ledger rather than of the filtered page,
		// so a reader looking at one state can still see there is
		// another.
		var open questionsResult
		decodeResponse(t, h.get("/api/reality/questions?state=plan-ready"), &open)
		if len(open.Items) != 1 || open.Items[0].State != string(reality.QuestionPlanReady) {
			t.Fatalf("the state filter returned %+v", open.Items)
		}
		found := map[string]int{}
		for _, count := range open.States {
			found[count.State] = count.Count
		}
		if found[string(reality.QuestionDeclined)] != 1 || found[string(reality.QuestionPlanReady)] != 1 {
			t.Errorf("the census of a filtered page = %v, want both states counted", found)
		}
	})

	t.Run("one question read whole, with what it was asked about", func(t *testing.T) {
		var detail questionDetail
		decodeResponse(t, h.get("/api/reality/question?id="+h.question.ID), &detail)
		if detail.Question.ID != h.question.ID {
			t.Fatalf("question = %+v", detail.Question)
		}
		if len(detail.Targets) != 1 || detail.Targets[0].DisplayName == "" {
			t.Errorf("targets = %+v, want the entity named rather than only identified", detail.Targets)
		}
		if len(detail.History) == 0 {
			t.Error("the question's append-only state history is missing")
		}
	})

	t.Run("the ledger's subjects, with what it holds about them", func(t *testing.T) {
		var listed entitiesResult
		decodeResponse(t, h.get("/api/reality/entities"), &listed)
		var subject *entityRow
		for i, row := range listed.Items {
			if row.ID == h.restricted.ID {
				subject = &listed.Items[i]
			}
		}
		if subject == nil {
			t.Fatalf("the entity carrying a policy fact is unreachable: %+v", listed.Items)
		}
		if subject.DisplayName == "" || subject.Facts != 1 || subject.Active != 1 {
			t.Errorf("entity row = %+v, want the one asserted fact counted", subject)
		}
		if subject.Aliases != 1 {
			t.Errorf("entity row counts %d aliases, want the chat term", subject.Aliases)
		}
		if len(listed.Kinds) == 0 {
			t.Error("the listing reports no census by kind")
		}
	})

	t.Run("a fact's whole revision chain", func(t *testing.T) {
		// The policy fact is superseded through the service, which is
		// the only thing allowed to write one, and both revisions are
		// then read back through the route.
		stated := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
		revised, err := h.reality.Focus().Supersede(h.ctx, reality.FocusPolicyRevision{
			PriorID: h.policy.ID,
			FocusPolicyInput: reality.FocusPolicyInput{
				SubjectID: h.restricted.ID,
				Policy:    reality.PolicyLearnOnly,
				By:        operatorID,
				Note:      "worth a little after all",
				At:        stated,
			},
		})
		if err != nil {
			t.Fatalf("Supersede: %v", err)
		}

		var head factDetail
		decodeResponse(t, h.get("/api/reality/fact?id="+revised.ID), &head)
		if head.Fact.ID != revised.ID || head.Fact.Status != string(reality.FactActive) {
			t.Fatalf("head = %+v", head.Fact)
		}
		if head.Subject.DisplayName == "" {
			t.Error("the fact's subject is identified but not named")
		}
		if head.Supersedes == nil || head.Supersedes.ID != h.policy.ID {
			t.Fatalf("the head does not say what it replaced: %+v", head.Supersedes)
		}
		if head.SupersededBy != nil {
			t.Errorf("the head claims something replaced it: %+v", head.SupersededBy)
		}

		var ancestor factDetail
		decodeResponse(t, h.get("/api/reality/fact?id="+h.policy.ID), &ancestor)
		if ancestor.Fact.Status != string(reality.FactSuperseded) {
			t.Errorf("the ancestor reads as %q, want superseded", ancestor.Fact.Status)
		}
		if ancestor.SupersededBy == nil || ancestor.SupersededBy.ID != revised.ID {
			t.Fatalf("the ancestor does not say what replaced it: %+v", ancestor.SupersededBy)
		}
		// The status history is what proves the ancestor was marked
		// rather than rewritten: its bytes still say what they said,
		// and a second appended event says it is no longer in force.
		if len(ancestor.History) < 2 {
			t.Errorf("the ancestor's status history = %+v, want the assertion and the supersession",
				ancestor.History)
		}

		var listed factsResult
		decodeResponse(t, h.get("/api/reality/facts"), &listed)
		if len(listed.Items) < 2 {
			t.Fatalf("the fact listing = %+v, want both revisions", listed.Items)
		}
		if listed.Items[0].Fact.ID != revised.ID {
			t.Errorf("the fact listing leads with %s, want the newest revision", listed.Items[0].Fact.ID)
		}
		if listed.Items[0].Subject.DisplayName == "" {
			t.Error("the fact listing identifies a subject it does not name")
		}
	})
}

// TestAnEmptyLedgerReadsAsEmpty pins what a fresh machine sees. Every listing
// answers with an empty collection rather than a null, because a page that
// receives null for a list either crashes or renders nothing while claiming to
// be loading — and "Babel knows nothing about your systems yet" is a state
// worth rendering honestly rather than a failure.
func TestAnEmptyLedgerReadsAsEmpty(t *testing.T) {
	ledger, err := reality.Open(t.TempDir())
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	s, httpServer := testServer(t, Options{Operator: operatorID, Reality: ledger})
	session := bootstrapSession(t, s, httpServer)

	for _, route := range []struct {
		path  string
		empty func(*testing.T, *http.Response)
	}{
		{
			path: "/api/reality/questions",
			empty: func(t *testing.T, response *http.Response) {
				var got questionsResult
				decodeResponse(t, response, &got)
				if got.Items == nil || len(got.Items) != 0 || got.Total != 0 || len(got.States) != 0 {
					t.Errorf("questions on an empty ledger = %+v", got)
				}
			},
		},
		{
			path: "/api/reality/entities",
			empty: func(t *testing.T, response *http.Response) {
				var got entitiesResult
				decodeResponse(t, response, &got)
				if got.Items == nil || len(got.Items) != 0 || got.Total != 0 || len(got.Kinds) != 0 {
					t.Errorf("entities on an empty ledger = %+v", got)
				}
			},
		},
		{
			path: "/api/reality/facts",
			empty: func(t *testing.T, response *http.Response) {
				var got factsResult
				decodeResponse(t, response, &got)
				if got.Items == nil || len(got.Items) != 0 || got.Total != 0 || len(got.Statuses) != 0 {
					t.Errorf("facts on an empty ledger = %+v", got)
				}
			},
		},
		{
			path: "/api/reality/inbox",
			empty: func(t *testing.T, response *http.Response) {
				var got inboxResult
				decodeResponse(t, response, &got)
				if got.Items == nil || len(got.Items) != 0 || got.Total != 0 {
					t.Errorf("the inbox on an empty ledger = %+v", got)
				}
			},
		},
	} {
		request, err := http.NewRequest(http.MethodGet, httpServer.URL+route.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		authorize(request, session)
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d on an empty ledger, want 200", route.path, response.StatusCode)
		}
		route.empty(t, response)
	}

	// A record the ledger does not hold is a 404 on every detail route,
	// which is what lets a page say "this is gone" instead of showing a
	// spinner forever.
	for _, path := range []string{
		"/api/reality/question?id=qst_absent",
		"/api/reality/entity?id=ent_absent",
		"/api/reality/fact?id=fct_absent",
	} {
		request, err := http.NewRequest(http.MethodGet, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		authorize(request, session)
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, response.StatusCode)
		}
	}
}
