package web

// Naming a subject: the §4.8 write that has to exist before any of the others
// mean anything.
//
// Every fact, every Question target and every focus policy on this surface is
// about an entity that already exists, and until this file the browser could
// create none. The focus page's own dead end said so out loud — it told the
// operator that a subject "has to exist before a policy can be stated about
// it" and then sent him to a CLI incantation — which is the defect §8.4 calls
// a stored thing the product does not have: the ledger's front door was a
// terminal.
//
// The authority is the operator's, resolved by the same requireOperator every
// other §4.7 and §4.8 mutation here uses, and a session that cannot name an
// operator creates nothing. The identity gates the write rather than landing
// in the row, and that is not an oversight to paper over: §4.8's entity
// carries no authority field because an entity asserts nothing — it is the
// subject facts are about, and the authority of a claim lives on the claim.
// What this route must not do is let an unattributable session write to the
// ledger at all, and that is what the gate is for.
//
// Creating a subject creates no facts. The entity, its first membership entry
// and its typed names are all that is written, so Babel believes exactly
// nothing about the new subject until analysis or an operator says something
// — and the response says so rather than leaving the operator to infer it
// from a page of zeroes.
//
// A name that is already taken is refused, not duplicated. The whole reason
// this route is reachable from the focus page's unresolved-name state is that
// the ledger just answered "no subject answers to that word", and the
// refusal's job is to catch the case where that answer changed underneath: a
// second subject for one thing is precisely the mistaken identity §4.8's merge
// history exists to undo, and it is cheaper never to create it. Two
// simultaneous creations of one name can still both pass — the check is a read
// and the write is a separate transaction — and the ledger already reports
// that state honestly: the word then resolves to two entities, which is §4.8's
// resolve-entity Question rather than a silent winner.
//
// Nothing here deletes or renames. A subject named by mistake is merged into
// the right one and a wrong name is retracted, both of them appends, and
// neither is reachable from this surface: internal/reality.SubjectNaming has
// no method for either.

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/atyrode/babel/internal/reality"
)

// subjectVocabularyResult is GET /api/reality/subject/vocabulary's response:
// the closed vocabularies a new subject has to be described in.
//
// It is a route rather than a constant in the client because the vocabulary is
// the ledger's. §4.8 keeps the kinds closed so that a typo is a refused write
// instead of a ninth kind, and a picker holding its own copy would offer a
// kind nothing can store, or miss one a build added — either way the operator
// would learn about it from a rejected form.
type subjectVocabularyResult struct {
	Kinds      []string `json:"kinds"`
	AliasKinds []string `json:"alias_kinds"`
}

// handleSubjectVocabulary serves the kinds a subject may be, and the kinds of
// name it may answer to.
func (s *Server) handleSubjectVocabulary(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Subjects != nil, "the reality ledger") {
		return
	}
	result := subjectVocabularyResult{}
	for _, kind := range s.opts.Subjects.Kinds() {
		result.Kinds = append(result.Kinds, string(kind))
	}
	for _, kind := range s.opts.Subjects.AliasKinds() {
		result.AliasKinds = append(result.AliasKinds, string(kind))
	}
	s.writeJSON(w, http.StatusOK, result)
}

// subjectAliasView is one typed name, as it was recorded.
type subjectAliasView struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// subjectCreateRequest is POST /api/reality/subject/create's body. The fields
// are `babel reality entity create`'s flags and nothing else: --kind, --name,
// --note and repeated --alias KIND=VALUE.
type subjectCreateRequest struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Notes is what this subject is in the operator's own words, optional
	// exactly as the CLI's --note is. It is prose kept with the entity, so
	// a future reader learns what "dev-01" was without asking.
	Notes string `json:"notes"`
	// Aliases are the typed names the subject should answer to. The page
	// that sends them has a reason to: the operator arrived at this route
	// by typing a word the ledger did not recognize, and a subject created
	// without that word as a name would leave the same word unresolvable
	// the next time he types it.
	Aliases []subjectAliasView `json:"aliases"`
}

// subjectCreatedBelievesNothing is what creating a subject did not do, stated
// once here rather than paraphrased by each surface that reports a creation.
const subjectCreatedBelievesNothing = "this subject now exists and Babel believes nothing about it: " +
	"creating it asserted no facts, opened no questions, and started no analysis"

// subjectCreateResult is what a creation answers with: the subject as every
// other §4.8 surface names one, the names it answers to, and what was not
// created with it.
type subjectCreateResult struct {
	// Subject is deliberately the same shape the focus surface renders a
	// subject in, because that is where the operator is going next: the
	// page that created a subject in order to state a policy about it can
	// hand this straight to the control that states one.
	Subject  focusSubjectView   `json:"subject"`
	Aliases  []subjectAliasView `json:"aliases"`
	Believes string             `json:"believes"`
}

// handleSubjectCreate names one subject.
//
// The shape is checked here and the rules are the ledger's. A kind outside the
// vocabulary and an alias kind outside it are refused before the ledger is
// touched, for the reason the CLI parses every alias before opening the store:
// a typo in the third name must not leave a subject created with the first
// two. Everything else — an empty display name, a value that looks like a
// credential — is internal/reality's own validation, reported as itself.
func (s *Server) handleSubjectCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Subjects != nil, "the reality ledger") {
		return
	}
	var request subjectCreateRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.Name == "" {
		s.writeError(w, http.StatusBadRequest, "a subject needs a name; name is required")
		return
	}
	kind, ok := s.requireEntityKind(w, request.Kind)
	if !ok {
		return
	}
	aliases, ok := s.requireAliases(w, request.Aliases)
	if !ok {
		return
	}
	// The operator is resolved before the name is checked and before
	// anything is written, so an unattributable session neither creates a
	// subject nor learns from this route which names the ledger holds. The
	// identity is not carried further: §4.8's entity has no authority
	// field, because an entity claims nothing.
	if _, ok := s.requireOperator(w); !ok {
		return
	}
	if !s.requireNameUnclaimed(w, r, request.Name, request.Aliases) {
		return
	}
	entity, attached, err := s.opts.Subjects.Create(r.Context(), reality.NewSubject{
		Kind:        kind,
		DisplayName: request.Name,
		Notes:       request.Notes,
		Aliases:     aliases,
	})
	if err != nil {
		// A failure after the entity exists is reported as the failure it
		// is, and the created identity is not hidden: the alternative is
		// an operator who retries and mints a second subject for one
		// thing. internal/reality's error names the entity that exists.
		s.serviceError(w, r, err)
		return
	}
	result := subjectCreateResult{
		Subject:  viewCreatedSubject(entity, attached),
		Aliases:  make([]subjectAliasView, 0, len(attached)),
		Believes: subjectCreatedBelievesNothing,
	}
	for _, alias := range attached {
		result.Aliases = append(result.Aliases, subjectAliasView{
			Kind:  string(alias.Kind),
			Value: alias.Payload.Value,
		})
	}
	s.writeJSON(w, http.StatusOK, result)
}

// viewCreatedSubject renders the subject that was just written, from the
// records the write returned rather than from a read of them.
//
// Every alias here was asserted a moment ago by this same call, so there is no
// retracted name to leave out — which is the one thing viewFocusSubject has to
// do that this does not, and the reason this does not read the ledger back: a
// creation that reported a subject it had re-read would be reporting a row
// somebody else could have moved in between, and it has nothing to add.
func viewCreatedSubject(entity reality.Entity, aliases []reality.Alias) focusSubjectView {
	view := focusSubjectView{
		EntityID:    entity.ID,
		Kind:        string(entity.Kind),
		DisplayName: entity.Payload.DisplayName,
		Aliases:     make([]string, 0, len(aliases)),
	}
	for _, alias := range aliases {
		view.Aliases = append(view.Aliases, alias.Payload.Value)
	}
	return view
}

// requireEntityKind resolves a requested kind against the ledger's closed
// vocabulary, naming the whole vocabulary in the refusal exactly as the CLI's
// own parser does: a form that was rejected without being told what is
// acceptable is a form the operator has to guess at.
func (s *Server) requireEntityKind(w http.ResponseWriter, value string) (reality.EntityKind, bool) {
	if value == "" {
		s.writeError(w, http.StatusBadRequest,
			"a subject is one of the ledger's kinds; kind is required, one of "+
				kindList(s.opts.Subjects.Kinds()))
		return "", false
	}
	if slices.Contains(s.opts.Subjects.Kinds(), reality.EntityKind(value)) {
		return reality.EntityKind(value), true
	}
	s.writeError(w, http.StatusBadRequest,
		"that is not one of the ledger's entity kinds; want one of "+kindList(s.opts.Subjects.Kinds()))
	return "", false
}

// requireAliases resolves every requested name before any of them is written,
// on the CLI's terms: a typo in the third alias must not leave a subject
// created with the first two.
func (s *Server) requireAliases(w http.ResponseWriter, requested []subjectAliasView) ([]reality.AliasInput, bool) {
	out := make([]reality.AliasInput, 0, len(requested))
	for _, alias := range requested {
		if alias.Value == "" {
			s.writeError(w, http.StatusBadRequest, "a name with no value cannot be recorded; "+
				"leave the name out or give it a value")
			return nil, false
		}
		if !slices.Contains(s.opts.Subjects.AliasKinds(), reality.AliasKind(alias.Kind)) {
			s.writeError(w, http.StatusBadRequest,
				"that is not one of the ledger's name kinds; want one of "+
					aliasKindList(s.opts.Subjects.AliasKinds()))
			return nil, false
		}
		out = append(out, reality.AliasInput{
			Kind:    reality.AliasKind(alias.Kind),
			Payload: reality.AliasPayload{Value: alias.Value},
		})
	}
	return out, true
}

// requireNameUnclaimed refuses a creation whose name the ledger already
// resolves.
//
// This is the focus surface's requireNothingInForce for identity rather than
// for policy: the control that reaches this route is offered only after the
// ledger answered that nothing resolves to the word, so a name that resolves
// now means the ledger moved after the page was drawn. The refusal names the
// subject that has the word, because looking that subject up is the action the
// operator actually wanted.
//
// Every word the request would make resolvable is checked, the display name
// included. A display name is not itself an alias, so it does not make the
// subject findable on its own — but an operator who types a name that already
// belongs to something is describing the thing that exists, and creating a
// second subject with the same visible name is how a ledger becomes two
// ledgers.
func (s *Server) requireNameUnclaimed(w http.ResponseWriter, r *http.Request,
	name string, aliases []subjectAliasView) bool {
	words := make([]string, 0, len(aliases)+1)
	words = append(words, name)
	for _, alias := range aliases {
		words = append(words, alias.Value)
	}
	for _, word := range words {
		existing, err := s.opts.Subjects.Resolve(r.Context(), word)
		switch {
		case err == nil:
			s.logf("%s %s refused: a subject already answers to one of the names", r.Method, r.URL.Path)
			// The word stays out of the message and out of the log for
			// §9's reason: a name an operator uses for a machine is
			// ledger content. The identifier is what the page needs to
			// send him to the subject that has it.
			s.writeError(w, http.StatusConflict, "one of those names already belongs to a subject in "+
				"this ledger: "+existing+". Nothing was created. Look that name up again and state the "+
				"policy on the subject that exists, or name this one differently — two subjects for one "+
				"thing have to be merged back together afterwards.")
			return false
		case errors.Is(err, reality.ErrAmbiguousAlias):
			s.logf("%s %s refused: one of the names already means several subjects", r.Method, r.URL.Path)
			s.writeError(w, http.StatusConflict, "one of those names already means more than one "+
				"subject in this ledger. Nothing was created. Open the subject you mean from the "+
				"Subjects listing, because adding a third thing with that name would make the "+
				"confusion worse.")
			return false
		case errors.Is(err, reality.ErrUnknownRecord):
			continue
		default:
			s.serviceError(w, r, err)
			return false
		}
	}
	return true
}

func kindList(kinds []reality.EntityKind) string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return joinWords(names)
}

func aliasKindList(kinds []reality.AliasKind) string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return joinWords(names)
}

// joinWords lists a closed vocabulary for a reader rather than for a parser,
// which is why it is not strings.Join: the refusals above are sentences an
// operator reads in a browser.
func joinWords(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
