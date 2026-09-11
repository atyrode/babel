package web

// Naming a subject over HTTP: the creation, the refusals that keep one thing
// from becoming two subjects, and the authority the route will not write
// without.
//
// Everything here runs against the real ledger, so what is asserted is what
// the store holds afterwards rather than what the handler said: a subject the
// route reports as created is a subject a run's own resolution finds, and a
// refusal is paired with the ledger being unchanged.

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/reality"
)

// TestNamingASubjectMakesItResolvableAndBelievesNothing is the journey the
// focus page's dead end used to end: a word nothing answers to becomes a
// subject, and the same word then resolves to it.
//
// Two things are asserted together because the product claim is both of them.
// The word resolves, which is what makes the next step — stating a policy —
// reachable by the operator's own vocabulary rather than by an identifier.
// And nothing is believed: creating a subject asserts no facts, so the ledger
// holds exactly the facts it held before.
func TestNamingASubjectMakesItResolvableAndBelievesNothing(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	const term = "the minecraft thing"

	// The ledger does not know the word, which is the state the operator is
	// in when the page offers to name it.
	var before focusSubjectResult
	decodeResponse(t, h.get("/api/reality/focus/subject?subject="+url.QueryEscape(term)), &before)
	if before.Resolved {
		t.Fatalf("the fixture already answers to %q: %+v", term, before)
	}
	// Every fact the ledger holds, counted before the creation. Facts takes
	// a subject and this census is deliberately subject-independent: what
	// must be true is that naming a subject asserted nothing about
	// anything, not merely nothing about the new one.
	factsBefore, err := h.reality.RecentFacts(h.ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}

	var created subjectCreateResult
	decodeResponse(t, h.post("/api/reality/subject/create",
		`{"kind":"project","name":"Minecraft mod","notes":"the thing I keep asking about",`+
			`"aliases":[{"kind":"chat-term","value":"`+term+`"}]}`), &created)
	if created.Subject.EntityID == "" {
		t.Fatalf("creation reported no subject: %+v", created)
	}
	if created.Subject.Kind != string(reality.EntityProject) {
		t.Errorf("kind = %q, want the kind the request named", created.Subject.Kind)
	}
	if len(created.Aliases) != 1 || created.Aliases[0].Value != term {
		t.Fatalf("aliases = %+v, want the word the operator typed", created.Aliases)
	}
	if !strings.Contains(created.Believes, "believes nothing") {
		t.Errorf("the response does not say what was not created: %q", created.Believes)
	}

	// The durable record: the entity exists with the kind and the name it
	// was given, and the word reaches it through the ledger's own
	// resolution — the same call a run makes when it meets the word in a
	// transcript.
	entity, err := h.reality.Entity(h.ctx, created.Subject.EntityID)
	if err != nil {
		t.Fatalf("the created subject is not in the ledger: %v", err)
	}
	if entity.Payload.DisplayName != "Minecraft mod" || entity.Kind != reality.EntityProject {
		t.Errorf("stored entity = %+v, want the subject the request described", entity)
	}
	resolved, err := h.reality.ResolveSubject(h.ctx, term)
	if err != nil {
		t.Fatalf("the operator's own word does not resolve after naming the subject: %v", err)
	}
	if resolved != entity.ID {
		t.Errorf("%q resolves to %s, want the subject just created", term, resolved)
	}

	// Nothing is believed about it. The fact count is compared rather than
	// the new subject's own facts read, because the failure this catches is
	// a route that asserted something about *any* subject on the way.
	factsAfter, err := h.reality.RecentFacts(h.ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(factsAfter) != len(factsBefore) {
		t.Errorf("facts = %d, want the %d the ledger held before: naming a subject asserts nothing",
			len(factsAfter), len(factsBefore))
	}

	// And the journey continues from here: the focus surface resolves the
	// word to the new subject, with no policy in force, which is the state
	// that offers the operator the control he came for.
	var after focusSubjectResult
	decodeResponse(t, h.get("/api/reality/focus/subject?subject="+url.QueryEscape(term)), &after)
	if !after.Resolved || after.Subject == nil || after.Subject.EntityID != entity.ID {
		t.Fatalf("the focus surface does not find the named subject: %+v", after)
	}
	if after.Rule != nil {
		t.Errorf("rule in force = %+v, want none for a subject nobody has stated a policy about", after.Rule)
	}
}

// TestNamingASubjectRefusesAnUnattributableOperator is §4.7's attribution rule
// on the newest write here. The route records no operator on the entity — an
// entity claims nothing, so §4.8 gives it no authority field — and that is
// exactly why this test exists: the gate is the only thing keeping a session
// that cannot name an operator from writing to the ledger, so its absence
// would be invisible in the record afterwards.
func TestNamingASubjectRefusesAnUnattributableOperator(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Operator = "" })
	before, err := h.reality.Entities(h.ctx, reality.EntityQuery{})
	if err != nil {
		t.Fatal(err)
	}

	response := h.post("/api/reality/subject/create", `{"kind":"machine","name":"dev-02"}`)
	text := body(t, response)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
	}
	if !strings.Contains(text, "operator") {
		t.Errorf("refusal does not say what is missing: %q", text)
	}

	after, err := h.reality.Entities(h.ctx, reality.EntityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("entities = %d, want the %d before: an unattributed creation wrote to the ledger",
			len(after), len(before))
	}
}

// TestNamingASubjectRefusesANameTheLedgerAlreadyHolds is the rule that keeps
// this route from making the ledger worse than the dead end it replaces.
//
// Both halves of a request are checked, because both are names: the display
// name and each typed alias. A second subject answering to a word that already
// reaches one is the mistaken identity §4.8's merge history exists to undo,
// and the refusal has to name the subject that holds the word so the operator
// can go and use it.
func TestNamingASubjectRefusesANameTheLedgerAlreadyHolds(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	before, err := h.reality.Entities(h.ctx, reality.EntityQuery{})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		body string
	}{
		{
			// The fixture's chat term, sent as the alias an operator
			// would have typed it as.
			name: "a typed name that already reaches a subject",
			body: `{"kind":"project","name":"a second name for it",` +
				`"aliases":[{"kind":"chat-term","value":"` + h.term + `"}]}`,
		},
		{
			// The same word as the display name, which is not itself
			// an alias: the ledger would have stored this one without
			// complaint, and it is refused because an operator typing
			// a name that already means something is describing the
			// thing that has it.
			name: "a display name that already reaches a subject",
			body: `{"kind":"project","name":"` + h.term + `"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := h.post("/api/reality/subject/create", test.body)
			text := body(t, response)
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
			}
			if !strings.Contains(text, h.restricted.ID) {
				t.Errorf("the refusal does not name the subject that holds the name: %q", text)
			}
			if !strings.Contains(text, "Nothing was created") {
				t.Errorf("the refusal does not say that nothing was written: %q", text)
			}
		})
	}

	after, err := h.reality.Entities(h.ctx, reality.EntityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("entities = %d, want the %d before: a refused creation still wrote one",
			len(after), len(before))
	}
}

// TestNamingASubjectRefusesWhatTheLedgerCannotHold covers the shape the route
// checks itself, and the one the ledger checks for it.
//
// The kind and the alias kinds are resolved against §4.8's closed vocabularies
// before anything is written, on the CLI's terms: a typo in the third name
// must not leave a subject created with the first two. An empty display name
// is internal/reality's own validation reaching a status code.
func TestNamingASubjectRefusesWhatTheLedgerCannotHold(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	before, err := h.reality.Entities(h.ctx, reality.EntityQuery{})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		body   string
		status int
	}{
		{
			name:   "a subject with no name",
			body:   `{"kind":"project","name":""}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "a subject with no kind",
			body:   `{"kind":"","name":"something"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "a kind outside the ledger's vocabulary",
			body:   `{"kind":"minecraft-server","name":"something"}`,
			status: http.StatusBadRequest,
		},
		{
			name: "a name kind outside the ledger's vocabulary",
			body: `{"kind":"project","name":"something",` +
				`"aliases":[{"kind":"nickname","value":"the thing"}]}`,
			status: http.StatusBadRequest,
		},
		{
			name: "a typed name with no value",
			body: `{"kind":"project","name":"something",` +
				`"aliases":[{"kind":"name","value":""}]}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "a body carrying a field the route does not accept",
			body:   `{"kind":"project","name":"something","authority":"operator"}`,
			status: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := h.post("/api/reality/subject/create", test.body)
			text := body(t, response)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d body %q, want %d", response.StatusCode, text, test.status)
			}
		})
	}

	after, err := h.reality.Entities(h.ctx, reality.EntityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("entities = %d, want the %d before: a refused creation still wrote one",
			len(after), len(before))
	}
}

// TestSubjectVocabularyIsTheLedgersOwn checks that the form's options are the
// ledger's closed vocabularies rather than a copy. The failure this prevents
// is a picker offering a kind nothing can store, which the operator would
// discover from a refused form.
func TestSubjectVocabularyIsTheLedgersOwn(t *testing.T) {
	h := newPhaseB(t, "plain", nil)

	var got subjectVocabularyResult
	decodeResponse(t, h.get("/api/reality/subject/vocabulary"), &got)

	wanted := make([]string, 0, len(reality.EntityKinds()))
	for _, kind := range reality.EntityKinds() {
		wanted = append(wanted, string(kind))
	}
	if !slices.Equal(got.Kinds, wanted) {
		t.Errorf("kinds = %v, want the ledger's %v", got.Kinds, wanted)
	}
	wantedAliases := make([]string, 0, len(reality.AliasKinds()))
	for _, kind := range reality.AliasKinds() {
		wantedAliases = append(wantedAliases, string(kind))
	}
	if !slices.Equal(got.AliasKinds, wantedAliases) {
		t.Errorf("alias kinds = %v, want the ledger's %v", got.AliasKinds, wantedAliases)
	}
}

// TestSubjectRoutesRefuseAnUnwiredLedger pairs the two new routes with the
// focus routes' answer to a build with no ledger: a stated conflict rather
// than a panic or an empty success.
func TestSubjectRoutesRefuseAnUnwiredLedger(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Subjects = nil })
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/reality/subject/vocabulary"},
		{method: http.MethodPost, path: "/api/reality/subject/create"},
	} {
		var response *http.Response
		if route.method == http.MethodPost {
			response = h.post(route.path, `{"kind":"project","name":"something"}`)
		} else {
			response = h.get(route.path)
		}
		text := body(t, response)
		if response.StatusCode != http.StatusConflict {
			t.Errorf("%s: status = %d body %q, want 409", route.path, response.StatusCode, text)
		}
	}
}

// TestSubjectNamingCarriesNoOtherAuthority is the structural half of the gate
// for this surface, and it is TestFocusSurfaceCarriesNoOtherAuthority's
// argument applied to the one write that mints a record rather than answering
// one.
//
// The property is that the concrete type is the boundary: *reality.SubjectNaming
// holds nothing but naming, so a method added to it that asserted a fact,
// merged two identities or installed a rule set fails here rather than becoming
// silently reachable from a browser. Every forbidden name is asserted to exist
// on the store, which is what keeps the check from passing vacuously.
func TestSubjectNamingCarriesNoOtherAuthority(t *testing.T) {
	surface := reflect.TypeOf((*SubjectNamingService)(nil)).Elem()
	concrete := reflect.TypeOf((*reality.SubjectNaming)(nil))

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
		t.Errorf("reality.SubjectNaming's methods = %v, want exactly the surface's %v", names, wanted)
	}

	// The ledger's writers this surface must not reach. AddAlias and
	// CreateEntity are deliberately absent from the list: they are what
	// Create performs, and they are the two writes §4.8 makes harmless on
	// their own — an identity and a name for it assert nothing about the
	// world. Everything that does assert something is here, including
	// MergeEntities and SplitEntity, which decide that two identities are
	// one thing or that one covers several: those are resolutions, and
	// §4.8 keeps them reversible precisely because they are judgements
	// rather than acts of naming.
	store := reflect.TypeOf((*reality.Store)(nil))
	for _, forbidden := range []string{
		"AssertFact", "SupersedeFact", "PutFocusRules", "DisputeFacts", "ResolveDispute",
		"MergeEntities", "SplitEntity", "UndoResolution", "ImportFacts", "RegisterTrustedSource",
		"AddRelationship", "RetractAlias", "RetractRelationship", "Ask", "RecordAnswer",
		"RecordPlan", "AcceptPlan", "RejectPlan", "SetQuestionState", "BeginInterpretation",
		"ExpireStale", "CaptureSnapshot", "Close",
	} {
		if _, ok := store.MethodByName(forbidden); !ok {
			t.Errorf("reality.Store no longer has a %s method; this test is checking a name "+
				"that does not exist and must be updated", forbidden)
		}
		if slices.Contains(names, forbidden) {
			t.Errorf("the subject-naming surface can reach reality.Store.%s", forbidden)
		}
	}
}
