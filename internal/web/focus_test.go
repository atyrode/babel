package web

// The §4.8 focus surface over HTTP: reading a policy nobody installed, stating
// one, reversing it, and the two writes that are refused because the ledger
// moved under the page.
//
// Everything here runs against the real ledger. What these tests are about is
// that the route and the store agree — an allowance the page shows is the
// allowance a run's deferral would read, and a reversal the page reports is a
// superseding revision in the durable record rather than a field somebody
// edited.

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/reality"
)

// TestFocusReadsCleanlyWithNoPolicyInstalled is the deployment every operator
// starts on: facts about subjects, and no version of the mapping that turns
// them into an expenditure decision.
//
// §4.8's own answer there is "no policy is installed, so nothing is withheld",
// and this route has to be able to say it. A 404 would make a page that is
// working correctly look broken, and a rule listed as in force would claim a
// restriction no run is applying — the fixture has an `excluded` policy fact
// for a subject, and until a version is installed it withholds nothing.
func TestFocusReadsCleanlyWithNoPolicyInstalled(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var got focusResult
	decodeResponse(t, h.get("/api/reality/focus"), &got)
	if got.Installed {
		t.Fatalf("a ledger nobody installed a version on reports installed: %+v", got)
	}
	if got.Policy != nil {
		t.Errorf("policy = %+v, want none", got.Policy)
	}
	if got.Shipped != reality.DefaultFocusRules().Version {
		t.Errorf("shipped version = %d, want the version this build ships", got.Shipped)
	}
	if len(got.Rules) != 0 {
		t.Errorf("rules in force = %+v, want none: no version maps a policy fact to an allowance yet", got.Rules)
	}
	if len(got.Choices) != 0 {
		t.Errorf("choices = %+v, want none: what a policy would withhold is the installed version's answer", got.Choices)
	}
	if !strings.Contains(got.Note, "nothing is withheld") {
		t.Errorf("note does not say what this state means: %q", got.Note)
	}
	// The operator's stated intent is still reported, because the state this
	// page has to explain is exactly "I said stop and nothing stopped": an
	// answer that listed nothing would be indistinguishable from a ledger
	// where he had never said it.
	if len(got.Stated) != 1 || got.Stated[0].Subject.EntityID != h.restricted.ID {
		t.Fatalf("stated = %+v, want the policy fact nothing interprets", got.Stated)
	}
	if got.Stated[0].Policy != reality.PolicyExcluded || got.Stated[0].Fact.ID != h.policy.ID {
		t.Errorf("stated = %+v, want the operator's own excluded fact", got.Stated[0])
	}

	// The subject still resolves and its history still reads, which is what
	// lets one page offer the install instead of refusing the lookup.
	var subject focusSubjectResult
	decodeResponse(t, h.get("/api/reality/focus/subject?subject="+url.QueryEscape(h.term)), &subject)
	if !subject.Resolved || subject.Subject == nil || subject.Subject.EntityID != h.restricted.ID {
		t.Fatalf("subject = %+v", subject)
	}
	if subject.Rule != nil {
		t.Errorf("rule = %+v, want none with no version installed", subject.Rule)
	}
	if len(subject.History) != 1 || subject.History[0].ID != h.policy.ID {
		t.Errorf("history = %+v, want the one policy fact the fixture asserted", subject.History)
	}
	if !strings.Contains(subject.Reason, "nothing is withheld") {
		t.Errorf("reason does not say why no rule is in force: %q", subject.Reason)
	}
}

// TestFocusInstallPutsStatedPolicyInForce is the two halves of what
// `babel reality focus install` plus a hand-asserted fact used to be: after the
// install, the policy fact the operator already stated decides something, and
// the route says what it decides in the words the operator will feel.
func TestFocusInstallPutsStatedPolicyInForce(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var installed focusInstallResult
	decodeResponse(t, h.post("/api/reality/focus/install", ""), &installed)
	if installed.Policy == nil || installed.Policy.Version != reality.DefaultFocusRules().Version {
		t.Fatalf("installed = %+v", installed)
	}
	if installed.Applies == "" {
		t.Error("the response does not say what installing applies to")
	}

	var got focusResult
	decodeResponse(t, h.get("/api/reality/focus"), &got)
	if !got.Installed || got.Policy == nil || got.Policy.InstalledAt == "" {
		t.Fatalf("read after install = %+v", got)
	}
	rule := findRule(t, got.Rules, h.restricted.ID)
	if rule.Allowance != string(reality.AllowanceExcluded) || !rule.Withholds {
		t.Errorf("rule = %+v, want the excluded allowance in force", rule)
	}
	if rule.Fact.ID != h.policy.ID || rule.Policy != reality.PolicyExcluded {
		t.Errorf("rule does not derive from the operator's fact: %+v", rule.Fact)
	}
	if rule.Fact.Authority.Kind != string(reality.AuthorityOperator) || rule.Fact.Authority.ID != operatorID {
		t.Errorf("rule's fact is attributed to %+v, want the operator", rule.Fact.Authority)
	}
	if rule.Fact.Authority.At == "" {
		t.Error("the rule does not say when it was asserted")
	}
	if rule.Rule == "" || rule.Because == "" || rule.Means == "" {
		t.Errorf("rule does not explain itself: %+v", rule)
	}

	// The consequences are the ledger's own words, so the page and a
	// deferral cannot describe one allowance differently.
	if rule.Means != reality.AllowanceExcluded.Consequences() {
		t.Errorf("means = %q, want internal/reality's own sentence", rule.Means)
	}
	// Every policy an operator can state is offered with what it would
	// withhold under this version, and exactly one of them withholds
	// nothing: without that, "lift this" would look like a fourth
	// restriction.
	lifting := 0
	for _, choice := range got.Choices {
		if choice.Means == "" {
			t.Errorf("choice %+v states no consequence", choice)
		}
		if !choice.Withholds {
			lifting++
		}
	}
	if len(got.Choices) != 4 || lifting != 1 {
		t.Errorf("choices = %+v, want the predicate's four values with one that withholds nothing", got.Choices)
	}
}

// TestFocusAssertTakesEffect drives the write an operator has today no browser
// path to at all: stating that a subject is not worth spending on.
//
// The three things asserted are the three that make it a fact rather than a
// preference. It is authoritative (`active`, not `proposed`), it is attributed
// to the launch session's operator, and the allowance the route reports is the
// one the ledger's own evaluation produces for that subject afterwards.
func TestFocusAssertTakesEffect(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	install(t, h)

	var wrote focusWriteResult
	decodeResponse(t, h.post("/api/reality/focus/assert",
		`{"subjectId":"`+h.entity.ID+`","policy":"learn-only",`+
			`"note":"keep its sessions, stop working on it"}`), &wrote)
	if wrote.Fact.ID == "" || wrote.Fact.Status != string(reality.FactActive) {
		t.Fatalf("fact = %+v, want an active revision", wrote.Fact)
	}
	if wrote.Fact.Authority.Kind != string(reality.AuthorityOperator) || wrote.Fact.Authority.ID != operatorID {
		t.Errorf("fact authority = %+v, want the session's operator", wrote.Fact.Authority)
	}
	if wrote.Fact.Predicate != string(reality.PredicateAnalysisPolicy) {
		t.Errorf("fact predicate = %q, want the analysis policy", wrote.Fact.Predicate)
	}
	if wrote.DisputeID != "" {
		t.Errorf("the assertion opened dispute %q; the subject had no policy to contradict", wrote.DisputeID)
	}
	if wrote.Rule == nil || wrote.Rule.Allowance != string(reality.AllowanceLearnOnly) {
		t.Fatalf("rule after the write = %+v", wrote.Rule)
	}
	if !strings.Contains(wrote.Rule.Means, "corpus") {
		t.Errorf("learn-only does not say what it keeps: %q", wrote.Rule.Means)
	}

	// The durable effect, read through the ledger rather than out of the
	// response: an allowance that only appeared in the reply would be a
	// claim about a decision rather than a decision a run would take.
	decision, err := h.reality.EvaluateFocus(h.ctx, reality.FocusQuery{
		EntityID:       h.entity.ID,
		RuleSetVersion: reality.DefaultFocusRules().Version,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus: %v", err)
	}
	if decision.Allowance != reality.AllowanceLearnOnly {
		t.Fatalf("the ledger decides %q, want learn-only", decision.Allowance)
	}

	// And it is listed, most restrictive first: the excluded fixture leads
	// the learn-only subject just written.
	var got focusResult
	decodeResponse(t, h.get("/api/reality/focus"), &got)
	if len(got.Rules) != 2 || got.Rules[0].Subject.EntityID != h.restricted.ID {
		t.Fatalf("rules = %+v, want the excluded subject first", got.Rules)
	}
	if !slices.Contains(got.Rules[1].Subject.Aliases, "the same project plain") {
		t.Errorf("the subject is not listed under the names it is known by: %+v", got.Rules[1].Subject)
	}
}

// TestFocusSupersedeReversesWithoutDeleting is how the operator changes his
// mind, and the reason the ledger is append-only.
//
// Lifting a restriction produces a second revision rather than removing the
// first: the subject stops being withheld, the fact that withheld it keeps its
// bytes and becomes `superseded`, and both revisions stay readable in order. A
// surface that deleted the first would leave nothing to explain why a run
// deferred that subject last week.
func TestFocusSupersedeReversesWithoutDeleting(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	install(t, h)

	var wrote focusWriteResult
	decodeResponse(t, h.post("/api/reality/focus/supersede",
		`{"priorFactId":"`+h.policy.ID+`","policy":"normal","note":"it is worth looking at again"}`), &wrote)
	if wrote.Fact.Supersedes != h.policy.ID {
		t.Fatalf("revision = %+v, want it to supersede the fact in force", wrote.Fact)
	}
	if wrote.Rule == nil || wrote.Rule.Allowance != string(reality.AllowanceFull) || wrote.Rule.Withholds {
		t.Fatalf("rule after the reversal = %+v, want nothing withheld", wrote.Rule)
	}

	prior, err := h.reality.Fact(h.ctx, h.policy.ID)
	if err != nil {
		t.Fatalf("Fact: %v", err)
	}
	if prior.Status != reality.FactSuperseded {
		t.Errorf("the prior revision is %q, want superseded", prior.Status)
	}
	if prior.Value.Enum != reality.PolicyExcluded {
		t.Errorf("the prior revision's value changed to %q; a supersession must not edit it", prior.Value.Enum)
	}

	// The reversal is visible as history on the subject, which is the thing
	// an editable field could not have kept.
	var subject focusSubjectResult
	decodeResponse(t, h.get("/api/reality/focus/subject?subject="+url.QueryEscape(h.term)), &subject)
	if len(subject.History) != 2 {
		t.Fatalf("history = %+v, want both revisions", subject.History)
	}
	if subject.History[0].Status != string(reality.FactSuperseded) ||
		subject.History[1].Status != string(reality.FactActive) {
		t.Errorf("history statuses = %q then %q", subject.History[0].Status, subject.History[1].Status)
	}
	if subject.Rule == nil || subject.Rule.Withholds {
		t.Errorf("the subject still withholds something: %+v", subject.Rule)
	}

	// Nothing is in force that withholds anything, so the listing no longer
	// shows the restriction — while the fact that stated it is still there
	// to read.
	var got focusResult
	decodeResponse(t, h.get("/api/reality/focus"), &got)
	rule := findRule(t, got.Rules, h.restricted.ID)
	if rule.Withholds || rule.Allowance != string(reality.AllowanceFull) {
		t.Errorf("rule = %+v, want a lifted subject", rule)
	}
	if rule.Fact.ID == h.policy.ID {
		t.Error("the listing still derives from the superseded revision")
	}
}

// TestFocusRefusesAWriteAgainstAStaleView is records.go's seen-head rule in
// the shape a fact can carry it.
//
// A focus rule has no revision chain to name, so what a mutation confirms is
// that the state it was shown is still the state in force. Both directions are
// refused: an assertion for a subject that acquired a policy since the page
// was drawn, and a reversal of a fact something else has already replaced.
// Neither refusal is cosmetic — the first would record a contradiction that
// puts neither policy in force, and the second would attribute a reversal of a
// restriction nobody is applying.
func TestFocusRefusesAWriteAgainstAStaleView(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	install(t, h)

	t.Run("an assertion for a subject that is already decided", func(t *testing.T) {
		response := h.post("/api/reality/focus/assert",
			`{"subjectId":"`+h.restricted.ID+`","policy":"learn-only","note":"n"}`)
		text := body(t, response)
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
		}
		if !strings.Contains(text, h.policy.ID) {
			t.Errorf("the refusal does not name the fact to revise: %q", text)
		}
		if !strings.Contains(text, "revise") {
			t.Errorf("the refusal does not say what to do instead: %q", text)
		}
		facts, err := h.reality.Facts(h.ctx, reality.FactQuery{
			SubjectID: h.restricted.ID,
			Predicate: reality.PredicateAnalysisPolicy,
		})
		if err != nil {
			t.Fatalf("Facts: %v", err)
		}
		if len(facts) != 1 {
			t.Fatalf("the refused assertion wrote %d facts, want none added", len(facts)-1)
		}
	})

	t.Run("a reversal of a fact that has been replaced", func(t *testing.T) {
		// Something else moves the subject on: another surface, another
		// machine's record arriving, or the operator's own second browser
		// tab. The page still holds the fact it rendered.
		if _, err := h.reality.SupersedeFact(h.ctx, reality.SupersedeInput{
			PriorID: h.policy.ID,
			Fact: reality.FactInput{
				SubjectID:   h.restricted.ID,
				Predicate:   reality.PredicateAnalysisPolicy,
				Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: reality.PolicyLearnOnly},
				ValidFrom:   h.policy.ValidFrom.Add(1),
				ObservedAt:  h.policy.ObservedAt.Add(1),
				Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: operatorID, At: h.policy.ObservedAt.Add(1)},
				Confidence:  reality.ConfidenceHigh,
				Sensitivity: reality.SensitivityRoutine,
			},
		}); err != nil {
			t.Fatalf("SupersedeFact: %v", err)
		}

		response := h.post("/api/reality/focus/supersede",
			`{"priorFactId":"`+h.policy.ID+`","policy":"normal","note":"n"}`)
		text := body(t, response)
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
		}
		if !strings.Contains(text, "Reload") {
			t.Errorf("the refusal does not tell the operator what to do: %q", text)
		}
		if !strings.Contains(text, reality.PolicyLearnOnly) {
			t.Errorf("the refusal does not name what is in force now: %q", text)
		}
	})

	t.Run("a reversal of a fact that is not a policy at all", func(t *testing.T) {
		lifecycle, _, err := h.reality.AssertFact(h.ctx, reality.FactInput{
			SubjectID:   h.entity.ID,
			Predicate:   reality.PredicateLifecycle,
			Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: reality.LifecycleActive},
			ValidFrom:   h.policy.ValidFrom,
			ObservedAt:  h.policy.ObservedAt,
			Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: operatorID, At: h.policy.ObservedAt},
			Confidence:  reality.ConfidenceHigh,
			Sensitivity: reality.SensitivityRoutine,
		})
		if err != nil {
			t.Fatalf("AssertFact: %v", err)
		}
		// It is refused before the seen-state check even applies: the
		// surface cannot revise a lifecycle, so the subject has no policy
		// in force and the request names a fact this route does not own.
		response := h.post("/api/reality/focus/supersede",
			`{"priorFactId":"`+lifecycle.ID+`","policy":"normal"}`)
		text := body(t, response)
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
		}
		after, err := h.reality.Fact(h.ctx, lifecycle.ID)
		if err != nil {
			t.Fatalf("Fact: %v", err)
		}
		if after.Status != reality.FactActive {
			t.Errorf("the lifecycle fact is now %q; the focus surface touched it", after.Status)
		}
	})
}

// TestFocusRefusesMalformedWrites checks the two things a handler decides for
// itself — that a request named a subject and named a policy — and leaves
// everything else to the ledger, whose vocabulary check is the one place that
// decides what a policy may be.
func TestFocusRefusesMalformedWrites(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	install(t, h)
	for _, test := range []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{
			name: "an assertion naming no subject", path: "/api/reality/focus/assert",
			body: `{"subjectId":"","policy":"excluded"}`, status: http.StatusBadRequest,
		},
		{
			name: "an assertion naming no policy", path: "/api/reality/focus/assert",
			body: `{"subjectId":"` + "ent_absent" + `","policy":""}`, status: http.StatusBadRequest,
		},
		{
			name: "an assertion about a subject the ledger does not hold", path: "/api/reality/focus/assert",
			body: `{"subjectId":"ent_absent","policy":"excluded"}`, status: http.StatusNotFound,
		},
		{
			name: "a policy outside the predicate's vocabulary", path: "/api/reality/focus/assert",
			body: `{"subjectId":"` + h.entity.ID + `","policy":"stop-it"}`, status: http.StatusBadRequest,
		},
		{
			name: "a revision naming no prior fact", path: "/api/reality/focus/supersede",
			body: `{"priorFactId":"","policy":"normal"}`, status: http.StatusBadRequest,
		},
		{
			name: "a revision of a fact that does not exist", path: "/api/reality/focus/supersede",
			body: `{"priorFactId":"fct_absent","policy":"normal"}`, status: http.StatusNotFound,
		},
		{
			name: "a body carrying a field the route does not accept", path: "/api/reality/focus/assert",
			body:   `{"subjectId":"` + h.entity.ID + `","policy":"excluded","authority":"operator"}`,
			status: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := h.post(test.path, test.body)
			text := body(t, response)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d body %q, want %d", response.StatusCode, text, test.status)
			}
		})
	}

	// A second install is a conflict rather than a silent no-op: a version
	// is immutable once stored, so an operator who clicked twice learns
	// which click did something.
	response := h.post("/api/reality/focus/install", "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("reinstall status = %d, want 409", response.StatusCode)
	}
}

// TestFocusSubjectResolvesWithoutACanonicalName is the reason this surface is
// usable at all: an operator reading a candidate about "the Minecraft thing"
// has a word, not an entity identifier.
//
// The two ways a word can fail are answers rather than errors. A name the
// ledger does not know is the ordinary case for a first guess, and a name
// meaning two entities is §4.8's own resolve-entity question — guessing one
// would bury exactly the confusion the ledger raises a question about.
func TestFocusSubjectResolvesWithoutACanonicalName(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	install(t, h)

	t.Run("a chat term", func(t *testing.T) {
		var got focusSubjectResult
		decodeResponse(t, h.get("/api/reality/focus/subject?subject="+url.QueryEscape(h.term)), &got)
		if !got.Resolved || got.Via != "alias" || got.Subject.EntityID != h.restricted.ID {
			t.Fatalf("resolution = %+v", got)
		}
		if got.Rule == nil || got.Rule.Allowance != string(reality.AllowanceExcluded) {
			t.Fatalf("rule = %+v", got.Rule)
		}
	})

	t.Run("an entity identifier", func(t *testing.T) {
		var got focusSubjectResult
		decodeResponse(t, h.get("/api/reality/focus/subject?subject="+h.restricted.ID), &got)
		if !got.Resolved || got.Via != "id" {
			t.Fatalf("resolution = %+v", got)
		}
	})

	t.Run("a word nothing answers to", func(t *testing.T) {
		var got focusSubjectResult
		decodeResponse(t, h.get("/api/reality/focus/subject?subject=nothing-by-this-name"), &got)
		if got.Resolved || got.Subject != nil || got.Rule != nil {
			t.Fatalf("resolution = %+v, want an unresolved answer", got)
		}
		if !strings.Contains(got.Reason, "has to exist") {
			t.Errorf("reason does not explain what is missing: %q", got.Reason)
		}
	})

	t.Run("a word that means two things", func(t *testing.T) {
		// Two entities, one word, filed under two alias kinds — which is
		// precisely the state §4.8 raises a resolve-entity question about.
		for _, kind := range []reality.AliasKind{reality.AliasHostname, reality.AliasRepository} {
			entity, err := h.reality.CreateEntity(h.ctx, reality.EntityInput{
				Kind:    reality.EntityService,
				Payload: reality.EntityPayload{DisplayName: "a service under " + string(kind)},
			})
			if err != nil {
				t.Fatalf("CreateEntity: %v", err)
			}
			if _, err := h.reality.AddAlias(h.ctx, reality.AliasInput{
				EntityID: entity.ID,
				Kind:     kind,
				Payload:  reality.AliasPayload{Value: "dev-01"},
			}); err != nil {
				t.Fatalf("AddAlias: %v", err)
			}
		}
		var got focusSubjectResult
		decodeResponse(t, h.get("/api/reality/focus/subject?subject=dev-01"), &got)
		if got.Resolved {
			t.Fatalf("an ambiguous name resolved to %+v", got.Subject)
		}
		if !strings.Contains(got.Reason, "more than one thing") {
			t.Errorf("reason does not say the name is ambiguous: %q", got.Reason)
		}
	})

	t.Run("a lookup naming no subject", func(t *testing.T) {
		response := h.get("/api/reality/focus/subject")
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
	})
}

// TestFocusRoutesRefuseAnUnwiredLedger is the degradation every service on
// this surface gets: a build with no reality ledger reports that rather than
// failing, and every other page keeps answering.
func TestFocusRoutesRefuseAnUnwiredLedger(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Focus = nil })
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/reality/focus"},
		{http.MethodGet, "/api/reality/focus/subject?subject=x"},
		{http.MethodPost, "/api/reality/focus/install"},
		{http.MethodPost, "/api/reality/focus/assert"},
		{http.MethodPost, "/api/reality/focus/supersede"},
	} {
		var response *http.Response
		if route.method == http.MethodGet {
			response = h.get(route.path)
		} else {
			response = h.post(route.path, `{"subjectId":"x","policy":"excluded"}`)
		}
		text := body(t, response)
		if response.StatusCode != http.StatusConflict {
			t.Errorf("%s: status = %d body %q, want 409", route.path, response.StatusCode, text)
		}
	}
}

// TestFocusSurfaceCarriesNoOtherAuthority is the structural half of the gate,
// and it is the check mutation_test.go's table cannot express.
//
// Every other surface in that table is a narrow interface over a wide store,
// so the property is "the interface omits these methods". This one is a narrow
// interface over a type that has nothing else: reality.FocusPolicy's whole
// method set is the surface, so a widening — a new method that asserted a
// lifecycle, merged two entities, or installed rules from a request body —
// fails here rather than being silently reachable from a browser.
func TestFocusSurfaceCarriesNoOtherAuthority(t *testing.T) {
	surface := reflect.TypeOf((*FocusPolicyService)(nil)).Elem()
	concrete := reflect.TypeOf((*reality.FocusPolicy)(nil))

	names := make([]string, concrete.NumMethod())
	for i := range names {
		names[i] = concrete.Method(i).Name
	}
	wanted := make([]string, surface.NumMethod())
	for i := range wanted {
		wanted[i] = surface.Method(i).Name
	}
	slices.Sort(names)
	slices.Sort(wanted)
	if !slices.Equal(names, wanted) {
		t.Errorf("reality.FocusPolicy's methods = %v, want exactly the surface's %v", names, wanted)
	}

	// The ledger's own writers, named so a rename cannot make this test
	// vacuous: each must exist on the store and none may be reachable here.
	store := reflect.TypeOf((*reality.Store)(nil))
	for _, forbidden := range []string{
		"AssertFact", "SupersedeFact", "PutFocusRules", "DisputeFacts", "ResolveDispute",
		"MergeEntities", "SplitEntity", "UndoResolution", "ImportFacts", "RegisterTrustedSource",
		"CreateEntity", "AddAlias", "AddRelationship", "RetractAlias", "RetractRelationship",
		"Ask", "RecordAnswer", "RecordPlan", "AcceptPlan", "RejectPlan", "SetQuestionState",
		"BeginInterpretation", "ExpireStale", "CaptureSnapshot", "Close",
	} {
		if _, ok := store.MethodByName(forbidden); !ok {
			t.Errorf("reality.Store no longer has a %s method; this test is checking a name "+
				"that does not exist and must be updated", forbidden)
		}
		if slices.Contains(names, forbidden) {
			t.Errorf("the focus surface can reach reality.Store.%s", forbidden)
		}
	}
}

// install puts the shipped version in force through the route, which is what
// an operator's first visit to the page does.
func install(t *testing.T, h *phaseB) {
	t.Helper()
	response := h.post("/api/reality/focus/install", "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("install status = %d", response.StatusCode)
	}
}

// findRule reads one subject's row out of a listing, failing rather than
// returning a zero value: a missing row and a row with no allowance are
// different defects and would otherwise report the same way.
func findRule(t *testing.T, rules []focusRuleInForceView, entityID string) focusRuleInForceView {
	t.Helper()
	for _, rule := range rules {
		if rule.Subject.EntityID == entityID {
			return rule
		}
	}
	t.Fatalf("no rule in force for %s in %+v", entityID, rules)
	return focusRuleInForceView{}
}
