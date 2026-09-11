package web

// The §4.8 focus surface: what analysis is allowed to spend on a subject, and
// the three acts that change it — installing the versioned mapping, stating a
// policy for a subject, and reversing one.
//
// This is the one place in this package where a browser request reaches the
// ledger's own authoritative writers, and it is worth being explicit about why
// that is not the hole internal/web/reality.go's package doc warns about.
//
// The authority is the operator's, and it is the same authority the CLI
// carries. §4.8 admits exactly two authorities and an attributed operator
// action is one of them; a fact asserted here is attributed to the session's
// operator identity, resolved by the same requireOperator every §4.7 and §4.8
// mutation on this surface uses, and a session that cannot name an operator
// writes nothing at all. What is refused is not the browser — it is an
// unattributed write, from any surface.
//
// Nothing else can be asserted. The surface this holds is
// reality.FocusPolicy, not the store: it writes the analysis-policy predicate
// and no other, it installs the rule set this build ships and not one from a
// request body, and it has no method that could merge an entity, import a
// batch, or resolve a dispute. That is a property of the type rather than a
// rule these handlers keep, which is the same reason FrontierReader lists no
// writer.
//
// Nothing is deleted and nothing is un-marked. Lifting a restriction is a
// superseding revision whose ancestor stays byte-identical and readable, so
// the operator's change of mind is a pair of facts rather than an edit — and
// the page has to render it that way, which is why the read route reports a
// lifted subject rather than dropping it from the listing.
//
// A write is refused when the ledger moved under the page. records.go states
// the rule and this file's requirePolicyInForce and requireNothingInForce are
// its analogue for facts: a focus rule has no revision chain to name, so what
// a mutation confirms is that the fact it was shown is still the fact in
// force. An operator who clicks "lift this restriction" against a fact
// somebody superseded a minute ago is deciding about a policy that is already
// gone, and recording that as a reversal would attribute an intent nobody
// stated.

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/atyrode/babel/internal/reality"
)

// focusConditionView is one requirement a rule places on the ledger.
type focusConditionView struct {
	Predicate string `json:"predicate"`
	Equals    string `json:"equals"`
}

// focusRuleView is one rule of an installed version, in the version's own
// order: rules are evaluated first-match-wins, so a listing that sorted them
// would be showing a policy that decides differently from the one stored.
type focusRuleView struct {
	Name    string               `json:"name"`
	When    []focusConditionView `json:"when"`
	Allows  string               `json:"allows"`
	Because string               `json:"because"`
	// Means is what the allowance does to the work, in the words every
	// surface uses for it (reality.Allowance.Consequences).
	Means string `json:"means"`
}

// focusPolicyView is one installed rule set version.
type focusPolicyView struct {
	Version      int             `json:"version"`
	Default      string          `json:"default"`
	DefaultMeans string          `json:"default_means"`
	Note         string          `json:"note,omitempty"`
	InstalledAt  string          `json:"installed_at"`
	Rules        []focusRuleView `json:"rules"`
}

// focusChoiceView is one policy an operator may state, and what stating it
// would withhold under the installed version.
//
// The allowance is the installed version's answer rather than this package's:
// §4.8's whole point is that no predicate value implies an expenditure, so a
// picker that labelled `excluded` with a consequence of its own would be
// asserting a mapping the stored policy might not make.
type focusChoiceView struct {
	Policy    string `json:"policy"`
	Allowance string `json:"allowance"`
	Rule      string `json:"rule,omitempty"`
	Means     string `json:"means"`
	// Withholds is false for the one choice that withholds nothing, so a
	// surface can present "lift this" as what it is rather than as a fourth
	// restriction.
	Withholds bool `json:"withholds"`
	// Conditional reports that the installed version also matches on facts
	// other than the policy, so this outcome is what the value maps to on
	// its own and the subject's own decision may differ.
	Conditional bool `json:"conditional,omitempty"`
}

// focusSubjectView identifies the thing a rule is about, by the names the
// operator actually uses for it.
//
// The aliases travel because the canonical display name is frequently not the
// word the operator would have typed: §4.8 keeps a rename, a path and a chat
// term as typed aliases of one identity precisely so that "the Minecraft
// thing" and a repository URL reach the same entity.
type focusSubjectView struct {
	EntityID    string   `json:"entity_id"`
	Kind        string   `json:"kind"`
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases"`
}

// focusRuleInForceView is one subject's standing decision: what is withheld,
// which rule decided, and the operator fact it derives from.
type focusRuleInForceView struct {
	Subject   focusSubjectView `json:"subject"`
	Allowance string           `json:"allowance"`
	Means     string           `json:"means"`
	Withholds bool             `json:"withholds"`
	// Rule is the rule in the installed version that matched, empty when
	// none did and the version's default applied.
	Rule    string `json:"rule,omitempty"`
	Because string `json:"because"`
	// Policy is the analysis-policy value the operator stated.
	Policy string `json:"policy"`
	// Fact is the revision the rule derives from, whole: who asserted it,
	// when, and why. It is the fact currently in force, which is the one a
	// run's own deferral would have read.
	Fact factView `json:"fact"`
	// Contested reports that the decision rests on a stale or disputed
	// fact. §4.8 gives the challenger exactly this check, so it is surfaced
	// rather than resolved quietly.
	Contested        bool     `json:"contested"`
	ContestedFactIDs []string `json:"contested_fact_ids,omitempty"`
}

// focusStatedView is one policy the operator has stated that no installed
// version interprets.
//
// It exists because the two halves of §4.8's mapping fail independently, and
// the failure an operator actually hits is this one: he states that a project
// is excluded, nothing happens, and no surface tells him why. The intent is
// recorded and is not a decision, so it is reported as what it is — a
// statement with nothing to interpret it — rather than as a rule in force or
// as nothing at all.
type focusStatedView struct {
	Subject focusSubjectView `json:"subject"`
	Policy  string           `json:"policy"`
	Fact    factView         `json:"fact"`
}

// focusResult is GET /api/reality/focus' response.
type focusResult struct {
	// Installed is false on a deployment where nothing has installed a rule
	// set version. It is a state rather than an error: until a version
	// exists §4.8's mapping has no artifact, so nothing is withheld
	// whatever the ledger says about a subject, and a page has to be able
	// to say so.
	Installed bool `json:"installed"`
	// Shipped is the version this build's consumers evaluate against, which
	// is the version an install would store.
	Shipped int               `json:"shipped_version"`
	Policy  *focusPolicyView  `json:"policy"`
	Choices []focusChoiceView `json:"choices"`
	// Rules are the standing decisions, most restrictive first and then by
	// subject, so what is being withheld leads. It is empty on a deployment
	// with no installed version, where nothing is in force to list.
	Rules []focusRuleInForceView `json:"rules"`
	// Stated is the policies an operator has stated that no version
	// interprets, and it is populated only when none is installed. Once a
	// version exists every one of them is a rule above.
	Stated []focusStatedView `json:"stated"`
	// Note is the one sentence a reader needs about the state of the policy
	// itself, and it is the server's wording rather than the page's so the
	// CLI and the browser cannot come to explain the same state
	// differently.
	Note string `json:"note"`
}

const (
	focusNotInstalled = "No focus policy version is installed, so nothing is withheld: " +
		"analysis spends on every subject it reaches, whatever this ledger says about it. " +
		"Installing the version this build ships is what puts a stated policy in force."
	focusNothingInForce = "No subject has a stated analysis policy, so nothing is withheld."
)

// handleFocus serves the installed policy and every rule in force.
//
// A deployment with no installed version answers 200 with installed=false.
// That is the honest shape of §4.8's own behaviour — every consultation
// answers "no policy is installed, so nothing is withheld" — and a 404 here
// would make a page that is working correctly look broken.
//
// Every allowance is the ledger's own evaluation, one EvaluateFocus per
// subject, rather than this route reading the policy value and drawing its own
// conclusion. A surface that mapped `excluded` to "nothing" itself would be
// the second implementation of §4.8's mapping, and the one an operator reads
// while a run obeys the other.
func (s *Server) handleFocus(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Focus != nil, "the reality ledger") {
		return
	}
	ctx := r.Context()
	shipped := s.opts.Focus.Shipped()
	result := focusResult{
		Shipped: shipped.Version,
		Choices: []focusChoiceView{},
		Rules:   []focusRuleInForceView{},
		Stated:  []focusStatedView{},
	}
	rules, err := s.opts.Focus.Rules(ctx, shipped.Version)
	installed := !errors.Is(err, reality.ErrUnknownRecord)
	if err != nil && installed {
		s.serviceError(w, r, err)
		return
	}
	if installed {
		result.Installed = true
		result.Policy = viewFocusPolicy(rules)
		result.Choices = viewFocusChoices(rules)
	} else {
		result.Note = focusNotInstalled
	}

	subjects, err := s.opts.Focus.Subjects(ctx)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	for _, subject := range subjects {
		fact, held, err := s.opts.Focus.InForce(ctx, subject, time.Time{})
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		if !held {
			// Every revision of this subject's policy has been
			// superseded or is still a proposal, so nothing about it is
			// in force. The history stays readable through the subject
			// lookup; a row here would claim a standing decision that no
			// run would act on.
			continue
		}
		if !installed {
			// A stated intent with no mapping to interpret it. It is
			// listed rather than dropped because "I said to stop and
			// nothing stopped" is the state this page exists to explain.
			view, err := s.viewFocusSubject(r, subject)
			if err != nil {
				s.serviceError(w, r, err)
				return
			}
			result.Stated = append(result.Stated, focusStatedView{
				Subject: view,
				Policy:  fact.Value.Enum,
				Fact:    viewFact(fact),
			})
			continue
		}
		view, held, err := s.viewRuleInForce(r, subject, rules.Version)
		if err != nil {
			s.serviceError(w, r, err)
			return
		}
		if held {
			result.Rules = append(result.Rules, view)
		}
	}
	// Most restrictive first: the reason to open this page is to see what
	// Babel is not doing, and a lifted subject is bookkeeping. The order is
	// internal/reality's own ranking, so it cannot disagree with the one a
	// hypothesis touching several subjects is decided by.
	slices.SortFunc(result.Rules, func(a, b focusRuleInForceView) int {
		left, right := reality.Allowance(a.Allowance), reality.Allowance(b.Allowance)
		switch {
		case left.MoreRestrictiveThan(right):
			return -1
		case right.MoreRestrictiveThan(left):
			return 1
		case a.Subject.DisplayName != b.Subject.DisplayName:
			if a.Subject.DisplayName < b.Subject.DisplayName {
				return -1
			}
			return 1
		}
		return 0
	})
	if installed && len(result.Rules) == 0 {
		result.Note = focusNothingInForce
	}
	s.writeJSON(w, http.StatusOK, result)
}

// viewRuleInForce renders one subject's standing decision. The second return
// is false when no policy fact is in force for the subject.
func (s *Server) viewRuleInForce(r *http.Request, subjectID string, version int) (focusRuleInForceView, bool, error) {
	ctx := r.Context()
	fact, held, err := s.opts.Focus.InForce(ctx, subjectID, time.Time{})
	if err != nil || !held {
		return focusRuleInForceView{}, false, err
	}
	decision, err := s.opts.Focus.Decide(ctx, reality.FocusQuery{
		EntityID:       subjectID,
		RuleSetVersion: version,
	})
	if err != nil {
		return focusRuleInForceView{}, false, err
	}
	subject, err := s.viewFocusSubject(r, subjectID)
	if err != nil {
		return focusRuleInForceView{}, false, err
	}
	return focusRuleInForceView{
		Subject:          subject,
		Allowance:        string(decision.Allowance),
		Means:            decision.Allowance.Consequences(),
		Withholds:        decision.Allowance != reality.AllowanceFull,
		Rule:             decision.RuleName,
		Because:          decision.Because,
		Policy:           fact.Value.Enum,
		Fact:             viewFact(fact),
		Contested:        decision.Contested,
		ContestedFactIDs: decision.ContestedFactIDs,
	}, true, nil
}

// viewFocusSubject names one entity by its identity and the words the operator
// calls it. A retracted alias is left out: it is a name the operator took back,
// and offering it as a way to recognize the subject would resurrect it.
func (s *Server) viewFocusSubject(r *http.Request, id string) (focusSubjectView, error) {
	entity, err := s.opts.Focus.Entity(r.Context(), id)
	if err != nil {
		return focusSubjectView{}, err
	}
	view := focusSubjectView{
		EntityID:    entity.ID,
		Kind:        string(entity.Kind),
		DisplayName: entity.Payload.DisplayName,
		Aliases:     []string{},
	}
	aliases, err := s.opts.Focus.Aliases(r.Context(), id)
	if err != nil {
		return focusSubjectView{}, err
	}
	for _, alias := range aliases {
		if alias.State != reality.StateAsserted {
			continue
		}
		view.Aliases = append(view.Aliases, alias.Payload.Value)
	}
	return view, nil
}

func viewFocusPolicy(rules reality.FocusRuleSet) *focusPolicyView {
	view := &focusPolicyView{
		Version:      rules.Version,
		Default:      string(rules.Default),
		DefaultMeans: rules.Default.Consequences(),
		Note:         rules.Note,
		InstalledAt:  timeText(rules.CreatedAt),
		Rules:        make([]focusRuleView, 0, len(rules.Rules)),
	}
	for _, rule := range rules.Rules {
		row := focusRuleView{
			Name:    rule.Name,
			When:    make([]focusConditionView, 0, len(rule.When)),
			Allows:  string(rule.Then),
			Because: rule.Because,
			Means:   rule.Then.Consequences(),
		}
		for _, cond := range rule.When {
			row.When = append(row.When, focusConditionView{
				Predicate: string(cond.Predicate),
				Equals:    cond.Equals,
			})
		}
		view.Rules = append(view.Rules, row)
	}
	return view
}

func viewFocusChoices(rules reality.FocusRuleSet) []focusChoiceView {
	outcomes := rules.PolicyOutcomes()
	out := make([]focusChoiceView, 0, len(outcomes))
	for _, outcome := range outcomes {
		out = append(out, focusChoiceView{
			Policy:      outcome.Policy,
			Allowance:   string(outcome.Allowance),
			Rule:        outcome.Rule,
			Means:       outcome.Allowance.Consequences(),
			Withholds:   outcome.Allowance != reality.AllowanceFull,
			Conditional: outcome.Conditional,
		})
	}
	return out
}

// focusSubjectResult is GET /api/reality/focus/subject's response: what a word
// the operator typed refers to, and what is already decided about it.
type focusSubjectResult struct {
	// Term is the word that was resolved, echoed so a page can report which
	// question this answers.
	Term string `json:"term"`
	// Resolved is false when the ledger has no entity by that name, or more
	// than one. Neither is an error: a name Babel does not know is the
	// ordinary case for an operator's first guess, and a name that means two
	// things is §4.8's own resolve-entity question rather than a failure.
	Resolved bool `json:"resolved"`
	// Nameable distinguishes the two ways a lookup fails to resolve, which
	// are opposite states and used to be one flag and a sentence.
	//
	// It is true when the word reaches nothing at all: no subject has it,
	// so naming one is the operator's next step and the surface that told
	// him the word is unknown is the surface that should offer it. It is
	// false when the word already means several subjects, because a third
	// thing answering to it would deepen exactly the confusion §4.8 raises
	// a resolve-entity Question about — the operator has to pick the
	// identity he meant instead.
	Nameable bool `json:"nameable"`
	// Via says how the term was recognized — "alias" for a name in the
	// ledger's alias table, "id" for a canonical entity identifier.
	Via     string            `json:"via,omitempty"`
	Subject *focusSubjectView `json:"subject"`
	// Rule is the subject's standing decision, absent when it has none in
	// force. Its presence is what tells a page whether stating a policy is
	// an assertion or a revision.
	Rule   *focusRuleInForceView `json:"rule"`
	Reason string                `json:"reason,omitempty"`
	// History is every analysis-policy revision about this subject, oldest
	// first, superseded ones included: how the operator changed his mind is
	// the thing an append-only ledger is for.
	History []factView `json:"history"`
}

// handleFocusSubject resolves an operator's word to a subject and reports what
// is decided about it.
//
// It resolves through the ledger's aliases, which is the whole point: an
// operator reading a hypothesis about "the Minecraft mod" has a word, not a
// canonical entity identifier, and requiring the identifier would make the
// browser surface strictly harder to use than the CLI it replaces. An
// identifier is accepted too, because the page that has one — an entity page
// linking here — should not have to invent a name for it.
func (s *Server) handleFocusSubject(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Focus != nil, "the reality ledger") {
		return
	}
	term, ok := s.requireID(w, r, "subject")
	if !ok {
		return
	}
	ctx := r.Context()
	result := focusSubjectResult{Term: term, History: []factView{}}

	id, via, reason, nameable, err := s.resolveFocusSubject(r, term)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if id == "" {
		result.Reason, result.Nameable = reason, nameable
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	result.Resolved, result.Via = true, via

	subject, err := s.viewFocusSubject(r, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	result.Subject = &subject

	history, err := s.opts.Focus.History(ctx, id)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	for _, fact := range history {
		result.History = append(result.History, viewFact(fact))
	}

	// The standing decision needs an installed version to be a decision at
	// all. Without one the subject still resolves and its history still
	// reads, which is what lets the page say "nothing is in force" and offer
	// the install rather than refusing the lookup.
	rules, err := s.opts.Focus.Rules(ctx, s.opts.Focus.Shipped().Version)
	if errors.Is(err, reality.ErrUnknownRecord) {
		result.Reason = focusNotInstalled
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	rule, held, err := s.viewRuleInForce(r, id, rules.Version)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if held {
		result.Rule = &rule
	}
	s.writeJSON(w, http.StatusOK, result)
}

// resolveFocusSubject turns a word into a canonical entity. An empty id with no
// error is "the ledger does not recognize this", the reason says which of the
// two ways it failed to, and nameable says whether naming a subject for the
// word is the sensible next act or the wrong one.
func (s *Server) resolveFocusSubject(r *http.Request, term string) (id, via, reason string, nameable bool, err error) {
	ctx := r.Context()
	resolved, err := s.opts.Focus.Resolve(ctx, term)
	switch {
	case err == nil:
		return resolved, "alias", "", false, nil
	case errors.Is(err, reality.ErrAmbiguousAlias):
		// §4.8 raises a resolve-entity question about exactly this, and
		// guessing an entity here would bury it. The count is reported and
		// the names are not: an alias value is operator vocabulary that
		// belongs in the ledger rather than in an error.
		//
		// Naming a subject is not offered here, and that is the one place
		// this surface withholds the act it otherwise leads with: a third
		// thing answering to a word that already means two would make the
		// resolution the operator owes the ledger harder, not easier.
		return "", "", "That name means more than one thing in this ledger. " +
			"Open the entity you mean from the Subjects listing and set its policy there, " +
			"so the choice is recorded against the identity you intended.", false, nil
	case !errors.Is(err, reality.ErrUnknownRecord):
		return "", "", "", false, err
	}
	// No alias answers to it. An entity identifier is the other thing an
	// operator can be holding, and it resolves to itself.
	entity, err := s.opts.Focus.Entity(ctx, term)
	if errors.Is(err, reality.ErrUnknownRecord) {
		return "", "", "Nothing in the ledger answers to that name. " +
			"A subject has to exist before a policy can be stated about it, " +
			"so name it here first: that records the identity and the words you call it by, " +
			"and nothing else — Babel believes nothing about a subject until you or an " +
			"analysis says something about it.", true, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return entity.CanonicalID, "id", "", false, nil
}

// focusInstallResult is POST /api/reality/focus/install's response.
type focusInstallResult struct {
	Policy *focusPolicyView `json:"policy"`
	// Applies states what installing changed about work already recorded,
	// which is nothing retroactive: a decision names the version it was
	// taken under, so installing a version decides future consultations and
	// rewrites no past one.
	Applies string `json:"applies"`
}

const focusInstallApplies = "future consultations only; a past decision names the version it was taken under, " +
	"and installing a version never re-decides one"

// handleFocusInstall installs the focus rule set version this build ships.
//
// It takes no body. The rules are the build's, so a browser cannot author
// policy in a request: §4.8's argument for versioning is that a past decision
// is explainable from the version's own bytes, and bytes that arrived in a
// POST would make that explanation unreviewable. Installing an already
// installed version is a conflict rather than a silent no-op, because a
// version is immutable once stored and an operator who clicked twice should
// learn which of the two clicks did something.
func (s *Server) handleFocusInstall(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Focus != nil, "the reality ledger") {
		return
	}
	if _, ok := s.requireOperator(w); !ok {
		return
	}
	rules, err := s.opts.Focus.Install(r.Context())
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, focusInstallResult{
		Policy:  viewFocusPolicy(rules),
		Applies: focusInstallApplies,
	})
}

// focusAssertRequest is POST /api/reality/focus/assert's body.
type focusAssertRequest struct {
	// SubjectID is the canonical entity, as GET
	// /api/reality/focus/subject resolved it. A name is deliberately not
	// accepted here: resolving a word is a read with two failure modes an
	// operator has to see before a fact is written in his name.
	SubjectID string `json:"subjectId"`
	// Policy is an analysis-policy value. It is not checked here — the
	// predicate's own closed vocabulary refuses an unknown one by name — so
	// there is one place that decides what a policy may be.
	Policy string `json:"policy"`
	// Note is the operator's reason, kept in the fact.
	Note string `json:"note"`
}

// focusWriteResult is what an assert or a revision answers with: the fact that
// was written, and the decision that is now in force because of it.
type focusWriteResult struct {
	Fact factView `json:"fact"`
	// Rule is the standing decision after the write, absent when no policy
	// version is installed and therefore nothing is in force yet.
	Rule *focusRuleInForceView `json:"rule"`
	// DisputeID names the §4.8 dispute this assertion opened, if it
	// contradicted a fact the ledger already held. It is reported rather
	// than swallowed: a dispute means neither side is in force, so a page
	// that ignored it would show a decision that is not being applied.
	DisputeID string `json:"dispute_id,omitempty"`
	Note      string `json:"note,omitempty"`
}

// handleFocusAssert states an operator's analysis policy for a subject.
//
// The fact carries operator authority, which is what makes it reality rather
// than a proposal — and it is the operator's own authority, resolved from the
// launch session, never a name this package invented. §4.8's rule that a model
// interpretation cannot become authoritative reality is untouched: nothing
// here is reachable by a run, and the only thing that decides what is asserted
// is the request an operator's browser sent.
//
// A subject that already has a policy in force is refused. Asserting a second
// active policy would contradict the first, which §4.8 turns into a dispute
// where neither side decides anything — so the honest answer is that changing
// a policy is a revision of the fact that states it, and the refusal names the
// fact to revise.
func (s *Server) handleFocusAssert(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Focus != nil, "the reality ledger") {
		return
	}
	var request focusAssertRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.SubjectID == "" {
		s.writeError(w, http.StatusBadRequest, "subjectId is required")
		return
	}
	if request.Policy == "" {
		s.writeError(w, http.StatusBadRequest, "policy is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	if !s.requireNothingInForce(w, r, request.SubjectID) {
		return
	}
	fact, dispute, err := s.opts.Focus.Assert(r.Context(), reality.FocusPolicyInput{
		SubjectID: request.SubjectID,
		Policy:    request.Policy,
		By:        by.ID(),
		Note:      request.Note,
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeFocusResult(w, r, fact, dispute.ID)
}

// focusSupersedeRequest is POST /api/reality/focus/supersede's body.
type focusSupersedeRequest struct {
	// PriorFactID is the fact the page showed as in force. It is both the
	// revision being replaced and the confirmation that the operator was
	// looking at the current one; see requirePolicyInForce.
	PriorFactID string `json:"priorFactId"`
	Policy      string `json:"policy"`
	Note        string `json:"note"`
}

// handleFocusSupersede replaces a subject's policy with a later revision.
//
// This is how the operator reverses himself, including all the way back to
// `normal`, which withholds nothing. Nothing is deleted by it: the prior
// revision keeps its bytes and gains a superseded status event, so the record
// of having excluded a subject survives the decision to stop excluding it.
//
// The prior fact must still be the one in force. That check is this file's
// version of records.go's seen-head rule, and it is not redundant with the
// store's own refusal to fork a chain: a policy can stop being in force
// because a *newer* fact about the subject took over, and the operator would
// then be reversing a restriction that is no longer the one being applied.
func (s *Server) handleFocusSupersede(w http.ResponseWriter, r *http.Request) {
	if !s.requireService(w, s.opts.Focus != nil, "the reality ledger") {
		return
	}
	var request focusSupersedeRequest
	if !s.decodeBody(w, r, &request) {
		return
	}
	if request.PriorFactID == "" {
		s.writeError(w, http.StatusBadRequest,
			"a revision names the fact it replaces; priorFactId is required")
		return
	}
	if request.Policy == "" {
		s.writeError(w, http.StatusBadRequest, "policy is required")
		return
	}
	by, ok := s.requireOperator(w)
	if !ok {
		return
	}
	if !s.requirePolicyInForce(w, r, request.PriorFactID) {
		return
	}
	fact, err := s.opts.Focus.Supersede(r.Context(), reality.FocusPolicyRevision{
		PriorID: request.PriorFactID,
		FocusPolicyInput: reality.FocusPolicyInput{
			Policy: request.Policy,
			By:     by.ID(),
			Note:   request.Note,
		},
	})
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	s.writeFocusResult(w, r, fact, "")
}

// writeFocusResult answers a write with the fact and the decision now in
// force.
//
// The decision is read back from the ledger rather than predicted from the
// policy that was just written, on the same terms handleRealityAnswer reads a
// question's state back: the mapping belongs to the installed version, and a
// handler that computed the consequence itself could report a restriction the
// version does not impose.
func (s *Server) writeFocusResult(w http.ResponseWriter, r *http.Request, fact reality.Fact, disputeID string) {
	result := focusWriteResult{Fact: viewFact(fact), DisputeID: disputeID}
	rules, err := s.opts.Focus.Rules(r.Context(), s.opts.Focus.Shipped().Version)
	if errors.Is(err, reality.ErrUnknownRecord) {
		result.Note = focusNotInstalled
		s.writeJSON(w, http.StatusOK, result)
		return
	}
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	rule, held, err := s.viewRuleInForce(r, fact.SubjectID, rules.Version)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	if held {
		result.Rule = &rule
	}
	s.writeJSON(w, http.StatusOK, result)
}

// requireNothingInForce refuses an assertion about a subject whose policy
// somebody has already stated.
//
// The 409 is the page's honesty rule rather than the store's: the assert
// control is only offered for a subject the page rendered as unrestricted, so
// a policy in force means the ledger moved after the page was drawn. It names
// the fact to revise, because that is the action the operator actually wanted.
func (s *Server) requireNothingInForce(w http.ResponseWriter, r *http.Request, subjectID string) bool {
	current, held, err := s.opts.Focus.InForce(r.Context(), subjectID, time.Time{})
	if err != nil {
		s.serviceError(w, r, err)
		return false
	}
	if !held {
		return true
	}
	s.logf("%s %s refused: a policy was already in force for the subject", r.Method, r.URL.Path)
	s.writeError(w, http.StatusConflict, "this subject already has an analysis policy in force: "+
		current.Value.Enum+", stated in fact "+current.ID+". Asserting a second one would contradict it "+
		"rather than change it, and a contradiction puts neither in force. Reload and revise that fact instead.")
	return false
}

// requirePolicyInForce refuses a revision of a fact that is no longer the one
// being applied.
func (s *Server) requirePolicyInForce(w http.ResponseWriter, r *http.Request, priorID string) bool {
	prior, err := s.opts.Focus.Fact(r.Context(), priorID)
	if err != nil {
		s.serviceError(w, r, err)
		return false
	}
	current, held, err := s.opts.Focus.InForce(r.Context(), prior.SubjectID, time.Time{})
	if err != nil {
		s.serviceError(w, r, err)
		return false
	}
	if held && current.ID == priorID {
		return true
	}
	s.logf("%s %s refused: the policy moved since the page was rendered", r.Method, r.URL.Path)
	message := "this subject's analysis policy changed after the page was rendered: " +
		"nothing is in force for it now, so there is no restriction left to revise. Reload and decide again."
	if held {
		message = "this subject's analysis policy changed after the page was rendered: " +
			current.Value.Enum + " is now in force, stated in fact " + current.ID +
			", and revising the fact it replaced would record a reversal of a policy nobody is applying. " +
			"Reload and decide again."
	}
	s.writeError(w, http.StatusConflict, message)
	return false
}
