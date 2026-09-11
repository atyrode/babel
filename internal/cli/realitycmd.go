package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/reality"
)

// defaultInboxLimit bounds a Question inbox that did not ask for a bound.
// The Reality inbox has no store-side default — reality.InboxQuery treats
// zero as unbounded — so the surface names one rather than rendering an
// arbitrarily long ranked list into a terminal.
const defaultInboxLimit = 100

const realityUsage = `Usage: babel reality <command> [flags]

Commands:
  inbox                list the prioritized Question inbox
  entity ID            show one entity, its aliases, edges, and facts
  entity create        create one entity the ledger can hold facts about
  source register      register a trusted source and the scope it may author
  refresh              expire lapsed facts and ask about them
  answer QUESTION_ID   record an attributed answer, retained verbatim
  accept PLAN_ID       accept one Answer Interpreter plan
  import --source ID   apply one trusted source's versioned fact batch
  focus [VERSION]      show the installed expenditure policy
  focus install        install the policy version this build ships

The Reality Ledger holds what is true about the operator's world (SPEC.md
§4.8). A raw answer is a durable input; an authoritative fact requires an
explicit plan acceptance, and no model may authorize one. Answers and
acceptances are attributed acts, so both require an operator identity.

An import is the one write that is not the operator's own act: its facts are
authored on the trusted source's authority, which the ledger assigns itself.
The operator's authorization lives in the source's registration, where the
predicates and entities it may author were declared.

Seeding runs in that order: entities first, because a fact and a Question
both name one and neither creates it; then the source, whose scope may name
the kinds or the entities it may author; then the batch.

Run "babel reality <command> -h" for a command's flags.
`

const realityInboxUsage = `Usage: babel reality inbox [flags]

Lists open Questions ranked by §4.8's five factors, with the arithmetic that
ranked them: the score's terms are shown because the policy is something an
operator will want to argue with, and a bare number cannot be argued with.

Each question is listed with any interpreter plans proposed for it, because
a plan identifier is what "babel reality accept" takes.

Flags:
  --class C     narrow to one class: blocking, maintenance, curiosity
  --limit N     bound the listing (default 100, 0 means no bound)
  --json        emit the inbox as JSON on stdout
`

const realityEntityUsage = `Usage: babel reality entity ID [flags]

Shows one entity with its typed aliases, its relationships, and the facts
asserted about it. Renames and path changes are aliases rather than edits,
so the identity survives them and the history stays readable.

Flags:
  --predicate P    narrow the facts to one predicate
  --as-of TIME     facts whose valid time covers this RFC3339 instant
  --json           emit the record as JSON on stdout
`

const realityEntityCreateUsage = `Usage: babel reality entity create --kind KIND --name NAME [flags]

Creates one entity: a project, repository, machine, service, provider,
environment, organization, or other operator-defined subject the ledger can
then hold facts and Questions about. Nothing else creates one — a fact names
a subject that must already exist, and a Question names target entities the
ledger resolves rather than mints — so this is where a Reality Ledger starts.

Creating an entity is an operator's own act (§4.8): identity is something a
person asserts, not something inferred from a transcript, and no analysis run
may perform it.

An alias may be given repeatedly as KIND=VALUE. Aliases are typed because a
rename, a path change and a conversational term are different kinds of
evidence that two names mean one thing; they are how a later import or answer
finds this entity without knowing the identifier Babel minted for it.

Flags:
  --kind KIND        project, repository, machine, service, provider,
                     environment, organization, or subject
  --name NAME        the display name
  --note TEXT        what this entity is, in the operator's words
  --alias KIND=VALUE typed alias; repeatable. Kinds: name, path, repository,
                     hostname, chat-term, url, identifier
  --json             emit the entity as JSON on stdout
`

const realitySourceRegisterUsage = `Usage: babel reality source register --from-json FILE|- [--json]

Registers a trusted source and the scope it may author within, reading the
document from FILE or from stdin when FILE is "-". This is the authorization
"babel reality import" spends: the operator declares here, once, which
predicates and which entities a source may assert facts about, and every
later batch from it is refused whole if it reaches outside that scope.

A source is identified by a stable id the operator chooses, so the same
dotfiles inventory is the same source across machines and reinstalls. The
registration is immutable: the ledger refuses to rewrite a scope, because a
widened scope applied retroactively would change what past imports were
allowed to say.

The document is one JSON object. A scope may name entity kinds, specific
entity ids, or both; entities named here must already exist:

  {"id": "dotfiles-inventory",
   "version": 1,
   "description": "versioned inventory of machines and service placement",
   "predicates": ["service-placement", "deployment-state"],
   "entity_kinds": ["machine", "service"],
   "entity_ids": []}

An unrecognized field is an error rather than an ignored key: a misspelled
"predicates" would register a source authorized for nothing, and the refusal
would arrive later at the batch instead of here at the authorization.

Flags:
  --from-json FILE|-   the registration document, or "-" for stdin
  --json               emit the registration as JSON on stdout
`

const realityFocusUsage = `Usage: babel reality focus [VERSION] [--json]

Shows the versioned focus rule set that maps ledger state to what analysis
may spend on a subject (SPEC.md §4.8). With no version it shows the one this
build's consumers evaluate against.

The rules are what turns a recorded analysis-policy fact into a decision the
conductor and "babel prepare" act on: excluded permits nothing, learn-only
keeps the subject's sessions in the corpus while withholding work about the
subject itself, and no-code-investigation permits only synthesis over
material Babel already holds. No allowance ever deletes a record.

Flags:
  --json    emit the rule set as JSON on stdout
`

const realityFocusInstallUsage = `Usage: babel reality focus install [--json]

Installs the focus rule set version this build ships, which maps the stated
analysis-policy predicate — and nothing else — onto an expenditure decision.
Lifecycle and ownership carry no such meaning in it: §4.8's own example of
the failure mode is treating a dormant project as one analysis may not spend
on, so an operator who wants that mapping installs a version that says so.

Until a version is installed nothing is withheld, because no policy has been
stated. A version is immutable once installed, so this refuses to replace
one: deciding differently means storing a new version, which is what makes
two decisions over one unchanged ledger comparable.

Flags:
  --json    emit the installed rule set as JSON on stdout
`

const realityRefreshUsage = `Usage: babel reality refresh [--as-of TIME] [--json]

Marks every fact whose refresh expectation has lapsed as stale, and asks one
maintenance Question about each. §4.8 gives each predicate an expectation —
where a service runs is worth doubting after a month, whether it is deployed
after a week — and a stale fact nobody is told about is a belief the ledger
quietly keeps holding.

This is the one Question producer that needs no model and no operator: it
derives what to ask from facts Babel already holds. It writes no fact. A
Question is a request for someone else to authorize something, which is why
it may be raised this way and answered only by the authority the fact names.

Asking is idempotent. A stale fact whose subject and predicate already have a
live question adds nothing, and one an operator declined stays declined until
newer evidence arrives, so this is safe to run on a schedule.

Flags:
  --as-of TIME   evaluate expectations at this RFC3339 instant, not now
  --json         emit the pass as JSON on stdout
`

const realityAnswerUsage = `Usage: babel reality answer QUESTION_ID --text T [flags]

Records one answer to a Question. The text is retained exactly as supplied:
§4.8 requires verbatim retention, so nothing is trimmed, normalized, or
rendered on the way in.

An answer is a durable input, not authority. It becomes fact only through an
Answer Interpreter plan that an operator explicitly accepts, which is what
"babel reality accept" is for.

The answer is attributed and there is no default identity.

Flags:
  --text T         the answer, retained verbatim (required)
  --outcome O      answered, unknown, or declined (default answered)
  --operator ID    operator identity (default $BABEL_OPERATOR)
  --json           emit the outcome as JSON on stdout
`

const realityAcceptUsage = `Usage: babel reality accept PLAN_ID [flags]

Accepts one Answer Interpreter plan. This is the single explicit operator
act §4.8 requires before an interpretation touches reality, and it is
atomic: every action in the plan applies together or none does.

Acceptance is attributed and there is no default identity.

Flags:
  --note TEXT      the operator's own words about the acceptance
  --operator ID    operator identity (default $BABEL_OPERATOR)
  --json           emit the application as JSON on stdout
`

const realityImportUsage = `Usage: babel reality import --source SOURCE_ID --from-json FILE|- [--json]

Applies one versioned fact batch from a registered trusted source, reading the
document from FILE or from stdin when FILE is "-". This is §4.8's trusted
inventory import: the source declared the predicates and entities it may author
when it was registered, and a batch reaching outside that scope is refused
whole rather than in part.

Atomicity and idempotency are the ledger's guarantees, not this command's:
every fact in the batch lands or none does, and replaying a batch key is
refused as a duplicate instead of importing the same facts twice.

There is deliberately no --operator flag. Every imported fact is attributed to
the source, and the ledger assigns that authority rather than reading one from
the document: an operator identity on a batch would record that the operator
personally authorized facts §4.8 attributes to the source. The source must
already be registered with the scope it may author within, and that
registration is where the operator's authorization lives.

The document is one JSON object. "batch_key" is the source's own idempotency
key for the batch; each fact names its subject entity, its predicate, a typed
value, its valid time, when the source observed the claim, and the provenance
locator §4.8 requires of every non-operator authority:

  {"batch_key": "inventory-2026-08-30",
   "facts": [{"subject_id": "ENTITY_ID",
              "predicate": "service-placement",
              "value": {"kind": "entity", "object_id": "ENTITY_ID"},
              "valid_from": "2026-08-30T00:00:00Z",
              "observed_at": "2026-08-30T00:00:00Z",
              "confidence": "high",
              "sensitivity": "routine",
              "provenance": {"path": "PATH", "digest": "SHA256_HEX"},
              "note": "why the source asserts this"}]}

An unrecognized field is an error rather than an ignored key, because a
misspelled "valid_until" would otherwise import an open-ended fact the source
never asserted. The document is never echoed back: an inventory names hosts and
paths, and a diagnostic quoting it would move them into a log.

Flags:
  --source SOURCE_ID   the registered trusted source this batch comes from
  --from-json FILE|-   the batch document, or "-" for stdin
  --json               emit the imported facts as JSON on stdout
`

// questionRow is one Question in machine-readable output.
type questionRow struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Class       string         `json:"class"`
	State       string         `json:"state"`
	Sensitivity string         `json:"sensitivity"`
	CreatedAt   string         `json:"created_at"`
	Prompt      string         `json:"prompt"`
	WhyAsked    string         `json:"why_asked,omitempty"`
	Entities    []string       `json:"target_entity_ids,omitempty"`
	Predicates  []string       `json:"target_predicates,omitempty"`
	Score       int            `json:"score"`
	Terms       map[string]int `json:"score_terms,omitempty"`
	// Plans are the interpretations proposed for this question, oldest
	// first. They are listed here because a plan identifier is what
	// "babel reality accept" takes, and §4.8's one explicit acceptance is
	// unreachable if the identifier can only be found in the database.
	Plans []planRow `json:"plans,omitempty"`
}

// planRow is one Answer Interpreter plan awaiting the operator acceptance
// §4.8 requires before an interpretation touches reality.
type planRow struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	InterpreterVersion int    `json:"interpreter_version"`
	CreatedAt          string `json:"created_at"`
	Actions            int    `json:"actions"`
}

// inboxResult is `babel reality inbox --json`. Like the review queue it
// carries no total: reality.Inbox ranks and bounds, and reports no count
// beyond the page it returned.
type inboxResult struct {
	Items []questionRow `json:"items"`
}

type entityRow struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Role        string `json:"role"`
	CanonicalID string `json:"canonical_id"`
	CreatedAt   string `json:"created_at"`
	DisplayName string `json:"display_name"`
	Notes       string `json:"notes,omitempty"`
}

type aliasRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
	Note      string `json:"note,omitempty"`
}

type relationshipRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	Note      string `json:"note,omitempty"`
}

type factRow struct {
	ID          string       `json:"id"`
	SubjectID   string       `json:"subject_id"`
	Predicate   string       `json:"predicate"`
	ValueKind   string       `json:"value_kind"`
	Value       string       `json:"value"`
	ObjectID    string       `json:"object_id,omitempty"`
	Status      string       `json:"status"`
	Confidence  string       `json:"confidence"`
	Sensitivity string       `json:"sensitivity"`
	Authority   string       `json:"authority"`
	ValidFrom   string       `json:"valid_from,omitempty"`
	ValidUntil  string       `json:"valid_until,omitempty"`
	ObservedAt  string       `json:"observed_at,omitempty"`
	RecordedAt  string       `json:"recorded_at"`
	ExpiresAt   string       `json:"expires_at,omitempty"`
	Supersedes  string       `json:"supersedes,omitempty"`
	Note        string       `json:"note,omitempty"`
	Provenance  *evidenceRow `json:"provenance,omitempty"`
}

type entityResult struct {
	Entity        entityRow         `json:"entity"`
	Aliases       []aliasRow        `json:"aliases"`
	Relationships []relationshipRow `json:"relationships"`
	Facts         []factRow         `json:"facts"`
}

type answerResult struct {
	AnswerID   string `json:"answer_id"`
	QuestionID string `json:"question_id"`
	Sequence   int    `json:"sequence"`
	Author     string `json:"author"`
	Outcome    string `json:"outcome"`
	RecordedAt string `json:"recorded_at"`
	State      string `json:"state"`
}

type acceptResult struct {
	AcceptanceID  string   `json:"acceptance_id"`
	PlanID        string   `json:"plan_id"`
	Actor         string   `json:"actor"`
	RecordedAt    string   `json:"recorded_at"`
	FactIDs       []string `json:"fact_ids"`
	DisputeIDs    []string `json:"dispute_ids,omitempty"`
	ResolutionIDs []string `json:"resolution_ids,omitempty"`
	FocusVersions []int    `json:"focus_rule_versions,omitempty"`
	QuestionState string   `json:"question_state"`
}

// importDocument is one trusted source's fact batch as an operator supplies
// it, and it is deliberately not reality.ImportInput.
//
// The source identity comes from --source rather than from the document, so a
// document cannot claim to come from a source the invocation never named, and
// there is no authority field at all: the ledger assigns the source's own
// authority to every imported fact (SPEC.md §4.8).
type importDocument struct {
	// BatchKey is the source's own idempotency key. It belongs in the
	// document rather than in a flag because it identifies the batch and not
	// the invocation: re-submitting the same document has to be the same
	// batch, which is what makes a retried import safe.
	BatchKey string               `json:"batch_key"`
	Facts    []importFactDocument `json:"facts"`
}

// importFactDocument is one fact in a batch. Every field reality.FactInput
// requires of a non-operator authority is here and nothing else is: identity,
// status, and authority are the ledger's to assign, so a document that named
// them would be describing a fact it does not get to author.
type importFactDocument struct {
	SubjectID string            `json:"subject_id"`
	Predicate reality.Predicate `json:"predicate"`
	Value     reality.FactValue `json:"value"`
	ValidFrom time.Time         `json:"valid_from"`
	// ValidUntil is optional; an absent one is the open-ended valid time
	// §4.8 expects of a fact that is still true.
	ValidUntil  time.Time           `json:"valid_until"`
	ObservedAt  time.Time           `json:"observed_at"`
	Confidence  reality.Confidence  `json:"confidence"`
	Sensitivity reality.Sensitivity `json:"sensitivity"`
	// Provenance is required rather than optional: reality.FactInput refuses
	// a non-operator authority that will not say where it observed the
	// claim, because that is an unattributable claim wearing a name.
	Provenance *event.Locator `json:"provenance"`
	Note       string         `json:"note"`
}

// importResult is `babel reality import --json`. It reports the facts that
// landed rather than a success flag: an import authorizes facts, and "ok" does
// not tell an operator which ones were authorized on its behalf.
type importResult struct {
	SourceID string    `json:"source_id"`
	BatchKey string    `json:"batch_key"`
	Facts    []factRow `json:"facts"`
}

// sourceDocument is `babel reality source register --from-json`. The scope is
// declared as data rather than as flags because it is the durable half of the
// authorization: an operator reviews this document, and the same document
// registers the same source on another machine.
type sourceDocument struct {
	ID          string   `json:"id"`
	Version     int      `json:"version"`
	Description string   `json:"description"`
	Note        string   `json:"note"`
	Predicates  []string `json:"predicates"`
	EntityKinds []string `json:"entity_kinds"`
	EntityIDs   []string `json:"entity_ids"`
}

// sourceResult is what the ledger stored, not what the document asked for.
type sourceResult struct {
	ID           string   `json:"id"`
	Version      int      `json:"version"`
	RegisteredAt string   `json:"registered_at"`
	Description  string   `json:"description,omitempty"`
	Predicates   []string `json:"predicates"`
	EntityKinds  []string `json:"entity_kinds,omitempty"`
	EntityIDs    []string `json:"entity_ids,omitempty"`
}

// entityCreateResult reports the identifier the ledger minted, which is the
// one a later fact or Question has to name.
type entityCreateResult struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	DisplayName string     `json:"display_name"`
	CreatedAt   string     `json:"created_at"`
	Aliases     []aliasRow `json:"aliases,omitempty"`
}

// refreshResult is `babel reality refresh --json`. The three counts are
// separate because a pass that expires ten facts and asks nothing is a
// working pass, not a failed one: the questions were already waiting.
type refreshResult struct {
	Expired    int           `json:"expired"`
	Existing   int           `json:"already_open"`
	Suppressed int           `json:"suppressed"`
	Questions  []questionRow `json:"questions"`
}

// reality routes `babel reality <verb>`.
func (a *app) reality(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "reality requires a subcommand", usage: realityUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, realityUsage)
		return nil
	case "inbox":
		return a.realityInbox(ctx, args[1:])
	case "entity":
		// "entity create" is a write and "entity ID" is a read, so the verb
		// is disambiguated here rather than by a flag: an identifier that
		// happened to be spelled "create" is not a thing this ledger mints.
		if len(args) > 1 && args[1] == "create" {
			return a.realityEntityCreate(ctx, args[2:])
		}
		return a.realityEntity(ctx, args[1:])
	case "source":
		if len(args) > 1 && args[1] == "register" {
			return a.realitySourceRegister(ctx, args[2:])
		}
		return &usageError{msg: "reality source requires the register subcommand", usage: realityUsage}
	case "answer":
		return a.realityAnswer(ctx, args[1:])
	case "accept":
		return a.realityAccept(ctx, args[1:])
	case "refresh":
		return a.realityRefresh(ctx, args[1:])
	case "import":
		return a.realityImport(ctx, args[1:])
	case "focus":
		return a.realityFocus(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown reality subcommand %q", args[0]), usage: realityUsage}
	}
}

func (a *app) realityInbox(ctx context.Context, args []string) error {
	c := newCmd("reality inbox", realityInboxUsage)
	class := c.fs.String("class", "", "narrow to one question class")
	limit := c.fs.Int("limit", defaultInboxLimit, "bound the listing; 0 means no bound")
	asJSON := c.fs.Bool("json", false, "emit the inbox as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	query := reality.InboxQuery{Limit: *limit}
	if *class != "" {
		parsed, err := parseQuestionClass(c, *class)
		if err != nil {
			return err
		}
		query.Class = parsed
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	items, err := store.Inbox(ctx, query)
	if err != nil {
		return err
	}
	rows := make([]questionRow, 0, len(items))
	for _, item := range items {
		row := renderQuestion(item.Question)
		row.Score = item.Score
		row.Terms = item.Terms
		plans, err := store.Plans(ctx, item.Question.ID)
		if err != nil {
			return err
		}
		for _, plan := range plans {
			row.Plans = append(row.Plans, planRow{
				ID:                 Sanitize(plan.ID),
				State:              Sanitize(string(plan.State)),
				InterpreterVersion: plan.InterpreterVersion,
				CreatedAt:          formatTime(plan.CreatedAt),
				Actions:            len(plan.Actions),
			})
		}
		rows = append(rows, row)
	}

	res := inboxResult{Items: rows}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(rows) == 0 {
		fmt.Fprint(a.stdout, "no open questions\n")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{row.ID, row.Class, row.State, strconv.Itoa(row.Score), row.Prompt})
	}
	if err := writeTable(a.stdout, []string{"ID", "CLASS", "STATE", "SCORE", "PROMPT"}, table); err != nil {
		return err
	}
	// The plans are listed under the questions rather than in the table,
	// because a plan identifier is what the next command takes and a
	// truncated table cell would not survive being copied.
	for _, row := range rows {
		for _, plan := range row.Plans {
			fmt.Fprintf(a.stdout, "  %s  plan %s  %s  %d %s\n",
				row.ID, plan.ID, plan.State, plan.Actions,
				plural(plan.Actions, "action", "actions"))
		}
	}
	return nil
}

func (a *app) realityEntity(ctx context.Context, args []string) error {
	c := newCmd("reality entity", realityEntityUsage)
	predicate := c.fs.String("predicate", "", "narrow the facts to one predicate")
	asOf := c.fs.String("as-of", "", "facts whose valid time covers this RFC3339 instant")
	asJSON := c.fs.Bool("json", false, "emit the record as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	query := reality.FactQuery{SubjectID: id}
	if *predicate != "" {
		parsed, err := parsePredicate(c, *predicate)
		if err != nil {
			return err
		}
		query.Predicate = parsed
	}
	if *asOf != "" {
		at, err := time.Parse(time.RFC3339, *asOf)
		if err != nil {
			return c.usagef("--as-of %q is not an RFC3339 timestamp", *asOf)
		}
		query.AsOf = at
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	entity, err := store.Entity(ctx, id)
	if err != nil {
		return fmt.Errorf("read entity %s: %w", id, err)
	}
	aliases, err := store.Aliases(ctx, id)
	if err != nil {
		return err
	}
	relationships, err := store.Relationships(ctx, id)
	if err != nil {
		return err
	}
	facts, err := store.Facts(ctx, query)
	if err != nil {
		return err
	}

	res := entityResult{
		Entity: entityRow{
			ID:          Sanitize(entity.ID),
			Kind:        Sanitize(string(entity.Kind)),
			Role:        Sanitize(string(entity.Role)),
			CanonicalID: Sanitize(entity.CanonicalID),
			CreatedAt:   formatTime(entity.CreatedAt),
			DisplayName: Sanitize(entity.Payload.DisplayName),
			Notes:       Sanitize(entity.Payload.Notes),
		},
		Aliases:       make([]aliasRow, 0, len(aliases)),
		Relationships: make([]relationshipRow, 0, len(relationships)),
		Facts:         make([]factRow, 0, len(facts)),
	}
	for _, al := range aliases {
		res.Aliases = append(res.Aliases, aliasRow{
			ID:        Sanitize(al.ID),
			Kind:      Sanitize(string(al.Kind)),
			State:     Sanitize(string(al.State)),
			Value:     Sanitize(al.Payload.Value),
			CreatedAt: formatTime(al.CreatedAt),
			Note:      Sanitize(al.Payload.Note),
		})
	}
	for _, rel := range relationships {
		res.Relationships = append(res.Relationships, relationshipRow{
			ID:        Sanitize(rel.ID),
			Kind:      Sanitize(string(rel.Kind)),
			FromID:    Sanitize(rel.FromID),
			ToID:      Sanitize(rel.ToID),
			State:     Sanitize(string(rel.State)),
			CreatedAt: formatTime(rel.CreatedAt),
			Note:      Sanitize(rel.Payload.Note),
		})
	}
	for _, f := range facts {
		res.Facts = append(res.Facts, renderFact(f))
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if err := writeDetail(a.stdout, [][2]string{
		{"id", res.Entity.ID},
		{"kind", res.Entity.Kind},
		{"role", res.Entity.Role},
		{"canonical", res.Entity.CanonicalID},
		{"created", res.Entity.CreatedAt},
		{"name", res.Entity.DisplayName},
		{"notes", orMissing(res.Entity.Notes)},
	}); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\naliases\n")
	aliasTable := make([][]string, 0, len(res.Aliases))
	for _, al := range res.Aliases {
		aliasTable = append(aliasTable, []string{al.Kind, al.State, al.Value})
	}
	if err := writeTable(a.stdout, []string{"KIND", "STATE", "VALUE"}, aliasTable); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nrelationships\n")
	relTable := make([][]string, 0, len(res.Relationships))
	for _, rel := range res.Relationships {
		other := rel.ToID
		direction := "out"
		if rel.ToID == res.Entity.ID {
			other, direction = rel.FromID, "in"
		}
		relTable = append(relTable, []string{direction, rel.Kind, other, rel.State})
	}
	if err := writeTable(a.stdout, []string{"DIR", "KIND", "OTHER", "STATE"}, relTable); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nfacts\n")
	factTable := make([][]string, 0, len(res.Facts))
	for _, f := range res.Facts {
		factTable = append(factTable, []string{f.Predicate, f.Value, f.Status, f.Confidence, f.Authority, f.RecordedAt})
	}
	return writeTable(a.stdout, []string{"PREDICATE", "VALUE", "STATUS", "CONFIDENCE", "AUTHORITY", "RECORDED"}, factTable)
}

func (a *app) realityAnswer(ctx context.Context, args []string) error {
	c := newCmd("reality answer", realityAnswerUsage)
	var of operatorFlags
	of.bind(c)
	text := c.fs.String("text", "", "the answer, retained verbatim")
	outcome := c.fs.String("outcome", string(reality.OutcomeAnswered), "answered, unknown, or declined")
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	questionID, err := c.oneSelector()
	if err != nil {
		return err
	}
	parsedOutcome, err := parseAnswerOutcome(c, *outcome)
	if err != nil {
		return err
	}
	if *text == "" && parsedOutcome == reality.OutcomeAnswered {
		return c.usagef("reality answer requires --text; use --outcome unknown or --outcome declined to record that there is no answer")
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	recorded, err := store.RecordAnswer(ctx, reality.AnswerInput{
		QuestionID: questionID,
		Author:     operator,
		At:         time.Now().UTC(),
		Outcome:    parsedOutcome,
		Text:       *text,
	})
	if err != nil {
		return fmt.Errorf("record answer to %s: %w", questionID, err)
	}
	question, err := store.Question(ctx, questionID)
	if err != nil {
		return err
	}

	res := answerResult{
		AnswerID:   Sanitize(recorded.ID),
		QuestionID: Sanitize(recorded.QuestionID),
		Sequence:   recorded.Sequence,
		Author:     Sanitize(recorded.Author),
		Outcome:    Sanitize(string(recorded.Outcome)),
		RecordedAt: formatTime(recorded.RecordedAt),
		State:      Sanitize(string(question.State)),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"answer", res.AnswerID},
		{"question", res.QuestionID},
		{"author", res.Author},
		{"outcome", res.Outcome},
		{"recorded", res.RecordedAt},
		{"state", res.State},
		{"authority", "none yet; a fact needs an accepted interpreter plan (SPEC.md §4.8)"},
	})
}

func (a *app) realityAccept(ctx context.Context, args []string) error {
	c := newCmd("reality accept", realityAcceptUsage)
	var of operatorFlags
	of.bind(c)
	note := c.fs.String("note", "", "the operator's own words about the acceptance")
	asJSON := c.fs.Bool("json", false, "emit the application as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	planID, err := c.oneSelector()
	if err != nil {
		return err
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	acceptance, application, err := store.AcceptPlan(ctx, reality.AcceptanceInput{
		PlanID: planID,
		Actor:  operator,
		Note:   *note,
	})
	if err != nil {
		return fmt.Errorf("accept plan %s: %w", planID, err)
	}

	res := acceptResult{
		AcceptanceID:  Sanitize(acceptance.ID),
		PlanID:        Sanitize(acceptance.PlanID),
		Actor:         Sanitize(acceptance.Actor),
		RecordedAt:    formatTime(acceptance.RecordedAt),
		FactIDs:       sanitizeAll(application.FactIDs),
		DisputeIDs:    sanitizeAll(application.DisputeIDs),
		ResolutionIDs: sanitizeAll(application.ResolutionIDs),
		FocusVersions: application.FocusVersions,
		QuestionState: Sanitize(string(application.QuestionState)),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"acceptance", res.AcceptanceID},
		{"plan", res.PlanID},
		{"actor", res.Actor},
		{"recorded", res.RecordedAt},
		{"facts", strconv.Itoa(len(res.FactIDs))},
		{"disputes", strconv.Itoa(len(res.DisputeIDs))},
		{"question", res.QuestionState},
	})
}

// realityEntityCreate mints one entity.
//
// Nothing else in Babel does. A fact's subject and a Question's targets are
// both required to exist already, so an empty ledger cannot be written into
// by any path — which is why the ledger has held zero rows: the storage and
// the lifecycle were complete and the front door was missing.
func (a *app) realityEntityCreate(ctx context.Context, args []string) error {
	c := newCmd("reality entity create", realityEntityCreateUsage)
	kind := c.fs.String("kind", "", "the entity kind")
	name := c.fs.String("name", "", "the display name")
	note := c.fs.String("note", "", "what this entity is, in the operator's words")
	var aliases stringList
	c.fs.Var(&aliases, "alias", "typed alias as KIND=VALUE; repeatable")
	asJSON := c.fs.Bool("json", false, "emit the entity as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *kind == "" {
		return c.usagef("reality entity create requires --kind KIND")
	}
	if *name == "" {
		return c.usagef("reality entity create requires --name NAME")
	}
	entityKind, err := parseEntityKind(c, *kind)
	if err != nil {
		return err
	}
	// Every alias is parsed before the ledger is opened, so a typo in the
	// third one does not leave an entity created with the first two.
	parsed := make([]reality.AliasInput, 0, len(aliases))
	for _, spec := range aliases {
		alias, err := parseAliasSpec(c, spec)
		if err != nil {
			return err
		}
		parsed = append(parsed, alias)
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	entity, err := store.CreateEntity(ctx, reality.EntityInput{
		Kind:    entityKind,
		Payload: reality.EntityPayload{DisplayName: *name, Notes: *note},
	})
	if err != nil {
		return fmt.Errorf("create entity: %w", err)
	}
	res := entityCreateResult{
		ID:          Sanitize(entity.ID),
		Kind:        Sanitize(string(entity.Kind)),
		DisplayName: Sanitize(entity.Payload.DisplayName),
		CreatedAt:   formatTime(entity.CreatedAt),
	}
	// An alias that fails after the entity exists is reported against the
	// entity rather than rolled back into it: the identity is real and
	// naming it again would mint a second one.
	for _, alias := range parsed {
		alias.EntityID = entity.ID
		added, err := store.AddAlias(ctx, alias)
		if err != nil {
			return fmt.Errorf("entity %s created; add alias %s: %w", entity.ID, alias.Kind, err)
		}
		res.Aliases = append(res.Aliases, aliasRow{
			Kind:  Sanitize(string(added.Kind)),
			Value: Sanitize(added.Payload.Value),
		})
	}

	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"entity", res.ID},
		{"kind", res.Kind},
		{"name", res.DisplayName},
		{"created", res.CreatedAt},
	}
	for _, alias := range res.Aliases {
		rows = append(rows, [2]string{"alias", alias.Kind + " " + alias.Value})
	}
	return writeDetail(a.stdout, rows)
}

// realitySourceRegister records what a trusted source may author.
//
// This is the authorization an import spends, and it is deliberately a
// separate act from the import: §4.8 puts the operator's decision here, once,
// rather than in every batch, so a source that later ships a fact outside its
// declared scope is refused by a rule the operator wrote earlier.
func (a *app) realitySourceRegister(ctx context.Context, args []string) error {
	c := newCmd("reality source register", realitySourceRegisterUsage)
	fromJSON := c.fs.String("from-json", "", "the registration document, or \"-\" for stdin")
	asJSON := c.fs.Bool("json", false, "emit the registration as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *fromJSON == "" {
		return c.usagef("reality source register requires --from-json FILE|-")
	}
	doc, err := decodeOneJSON[sourceDocument](a, *fromJSON, "registration document")
	if err != nil {
		return err
	}
	if doc.ID == "" {
		return c.usagef("the registration document needs an \"id\"")
	}
	if len(doc.Predicates) == 0 {
		return c.usagef("the registration document needs at least one predicate in \"predicates\"")
	}

	kinds := make([]reality.EntityKind, 0, len(doc.EntityKinds))
	for _, raw := range doc.EntityKinds {
		kind, err := parseEntityKind(c, raw)
		if err != nil {
			return err
		}
		kinds = append(kinds, kind)
	}
	predicates := make([]reality.Predicate, 0, len(doc.Predicates))
	for _, raw := range doc.Predicates {
		predicate, err := parsePredicate(c, raw)
		if err != nil {
			return err
		}
		predicates = append(predicates, predicate)
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	source, err := store.RegisterTrustedSource(ctx, reality.TrustedSourceInput{
		ID:          doc.ID,
		Version:     doc.Version,
		Predicates:  predicates,
		EntityIDs:   doc.EntityIDs,
		EntityKinds: kinds,
		Payload: reality.TrustedSourcePayload{
			Description: doc.Description,
			Note:        doc.Note,
		},
	})
	if err != nil {
		return fmt.Errorf("register trusted source %s: %w", doc.ID, err)
	}

	res := sourceResult{
		ID:           Sanitize(source.ID),
		Version:      source.Version,
		RegisteredAt: formatTime(source.RegisteredAt),
		Description:  Sanitize(source.Payload.Description),
	}
	for _, p := range source.Predicates {
		res.Predicates = append(res.Predicates, Sanitize(string(p)))
	}
	for _, k := range source.EntityKinds {
		res.EntityKinds = append(res.EntityKinds, Sanitize(string(k)))
	}
	for _, id := range source.EntityIDs {
		res.EntityIDs = append(res.EntityIDs, Sanitize(id))
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"source", res.ID},
		{"version", strconv.Itoa(res.Version)},
		{"registered", res.RegisteredAt},
		{"description", res.Description},
		{"predicates", strings.Join(res.Predicates, ", ")},
	}
	if len(res.EntityKinds) > 0 {
		rows = append(rows, [2]string{"entity kinds", strings.Join(res.EntityKinds, ", ")})
	}
	if len(res.EntityIDs) > 0 {
		rows = append(rows, [2]string{"entities", strings.Join(res.EntityIDs, ", ")})
	}
	return writeDetail(a.stdout, rows)
}

// realityFocus routes `babel reality focus`.
func (a *app) realityFocus(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "install" {
		return a.realityFocusInstall(ctx, args[1:])
	}
	return a.realityFocusShow(ctx, args)
}

// realityFocusInstall installs the focus rule set version this build ships.
//
// Until something installs one, §4.8's mapping has no artifact and every
// consultation answers "no policy is installed, so nothing is withheld" —
// which is correct and also means an operator who recorded an analysis
// policy against a subject would watch it have no effect. This is the act
// that turns the predicate into an expenditure decision, and it is the
// operator's own: the mapping from a stated policy to what analysis may
// spend is exactly the thing §4.8 refuses to leave implied.
//
// A version is immutable once installed, so this refuses rather than
// replaces. Deciding differently means a new version, which is what makes
// two decisions over one unchanged ledger comparable.
func (a *app) realityFocusInstall(ctx context.Context, args []string) error {
	c := newCmd("reality focus install", realityFocusInstallUsage)
	asJSON := c.fs.Bool("json", false, "emit the installed rule set as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	rules, err := store.PutFocusRules(ctx, reality.DefaultFocusRules())
	if err != nil {
		return fmt.Errorf("install focus rule set: %w", err)
	}
	return a.emitFocusRules(rules, *asJSON)
}

// realityFocusShow reads one installed policy version back.
func (a *app) realityFocusShow(ctx context.Context, args []string) error {
	c := newCmd("reality focus", realityFocusUsage)
	asJSON := c.fs.Bool("json", false, "emit the rule set as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	version := reality.DefaultFocusRules().Version
	switch rest := c.args(); len(rest) {
	case 0:
	case 1:
		parsed, err := strconv.Atoi(rest[0])
		if err != nil || parsed <= 0 {
			return c.usagef("a focus rule set version is a positive integer, not %q", rest[0])
		}
		version = parsed
	default:
		return c.usagef("reality focus takes at most one version")
	}
	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	rules, err := store.FocusRules(ctx, version)
	if errors.Is(err, reality.ErrUnknownRecord) {
		return fmt.Errorf("focus rule set version %d is not installed; "+
			"install the version this build ships with: babel reality focus install", version)
	}
	if err != nil {
		return err
	}
	return a.emitFocusRules(rules, *asJSON)
}

func (a *app) emitFocusRules(rules reality.FocusRuleSet, asJSON bool) error {
	res := focusRulesResult{
		Version:     rules.Version,
		Default:     Sanitize(string(rules.Default)),
		Note:        Sanitize(rules.Note),
		InstalledAt: formatTime(rules.CreatedAt),
	}
	for _, rule := range rules.Rules {
		row := focusRuleRow{
			Name:    Sanitize(rule.Name),
			Allows:  Sanitize(string(rule.Then)),
			Because: Sanitize(rule.Because),
		}
		for _, cond := range rule.When {
			row.When = append(row.When,
				Sanitize(string(cond.Predicate))+"="+Sanitize(cond.Equals))
		}
		res.Rules = append(res.Rules, row)
	}
	if asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"version", strconv.Itoa(res.Version)},
		{"default", res.Default},
		{"installed", res.InstalledAt},
	}
	if res.Note != "" {
		rows = append(rows, [2]string{"note", res.Note})
	}
	for _, rule := range res.Rules {
		rows = append(rows, [2]string{"rule",
			fmt.Sprintf("%s: %s -> %s", rule.Name, strings.Join(rule.When, " and "), rule.Allows)})
	}
	return writeDetail(a.stdout, rows)
}

// focusRulesResult is `babel reality focus --json`.
type focusRulesResult struct {
	Version     int            `json:"version"`
	Default     string         `json:"default"`
	Note        string         `json:"note,omitempty"`
	InstalledAt string         `json:"installed_at"`
	Rules       []focusRuleRow `json:"rules"`
}

type focusRuleRow struct {
	Name    string   `json:"name"`
	When    []string `json:"when,omitempty"`
	Allows  string   `json:"allows"`
	Because string   `json:"because"`
}

// realityRefresh runs the one Question producer that needs nobody.
//
// Every other way a Question could come into existence needs an author: an
// operator typing one, or an interpreter plan proposing a follow-up. This one
// reads the ledger's own aging and asks about it, which is why the inbox
// could be built, tested and shipped and still hold nothing.
func (a *app) realityRefresh(ctx context.Context, args []string) error {
	c := newCmd("reality refresh", realityRefreshUsage)
	asOf := c.fs.String("as-of", "", "evaluate expectations at this RFC3339 instant")
	asJSON := c.fs.Bool("json", false, "emit the pass as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	var at time.Time
	if *asOf != "" {
		parsed, err := time.Parse(time.RFC3339, *asOf)
		if err != nil {
			return c.usagef("--as-of %q is not an RFC3339 timestamp", *asOf)
		}
		at = parsed
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	pass, err := store.RefreshStale(ctx, at)
	if err != nil {
		return fmt.Errorf("refresh stale facts: %w", err)
	}

	res := refreshResult{
		Expired:    len(pass.ExpiredFactIDs),
		Existing:   pass.Existing,
		Suppressed: pass.Suppressed,
		Questions:  make([]questionRow, 0, len(pass.Asked)),
	}
	for _, question := range pass.Asked {
		res.Questions = append(res.Questions, renderQuestion(question))
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if err := writeDetail(a.stdout, [][2]string{
		{"expired", strconv.Itoa(res.Expired) + " " + plural(res.Expired, "fact", "facts")},
		{"asked", strconv.Itoa(len(res.Questions))},
		{"already open", strconv.Itoa(res.Existing)},
		{"suppressed", strconv.Itoa(res.Suppressed)},
	}); err != nil {
		return err
	}
	if len(res.Questions) == 0 {
		return nil
	}
	// The questions are listed rather than counted, because the identifier
	// is what "babel reality answer" takes.
	fmt.Fprint(a.stdout, "\nquestions\n")
	table := make([][]string, 0, len(res.Questions))
	for _, question := range res.Questions {
		table = append(table, []string{
			question.ID, question.Kind, question.Class, question.Prompt,
		})
	}
	return writeTable(a.stdout, []string{"QUESTION", "KIND", "CLASS", "PROMPT"}, table)
}

func (a *app) realityImport(ctx context.Context, args []string) error {
	c := newCmd("reality import", realityImportUsage)
	source := c.fs.String("source", "", "the registered trusted source this batch comes from")
	fromJSON := c.fs.String("from-json", "", "the batch document, or \"-\" for stdin")
	asJSON := c.fs.Bool("json", false, "emit the imported facts as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *source == "" {
		return c.usagef("reality import requires --source SOURCE_ID")
	}
	if *fromJSON == "" {
		return c.usagef("reality import requires --from-json FILE|-")
	}

	// The whole document is decoded before the ledger is opened, so a
	// malformed batch is refused without a transaction ever starting.
	doc, err := decodeOneJSON[importDocument](a, *fromJSON, "import document")
	if err != nil {
		return err
	}
	facts := make([]reality.FactInput, 0, len(doc.Facts))
	for _, fact := range doc.Facts {
		facts = append(facts, reality.FactInput{
			SubjectID:   fact.SubjectID,
			Predicate:   fact.Predicate,
			Value:       fact.Value,
			ValidFrom:   fact.ValidFrom,
			ValidUntil:  fact.ValidUntil,
			ObservedAt:  fact.ObservedAt,
			Confidence:  fact.Confidence,
			Sensitivity: fact.Sensitivity,
			Provenance:  fact.Provenance,
			Note:        fact.Note,
			// Authority is left zero on purpose: reality.ImportFacts
			// overwrites it with the source's own authority, so anything
			// set here would be a value the caller does not get to choose.
		})
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	// All-or-nothing is reality.ImportFacts's guarantee rather than this
	// command's: the batch runs in one transaction, and a fact refused for
	// scope, vocabulary, or credential material rolls back the facts that
	// preceded it along with the import row itself. So there is nothing to
	// undo here, and nothing partial to report.
	imported, err := store.ImportFacts(ctx, reality.ImportInput{
		SourceID: *source,
		BatchKey: doc.BatchKey,
		Facts:    facts,
	})
	if err != nil {
		return fmt.Errorf("import batch %s from source %s: %w", doc.BatchKey, *source, err)
	}

	res := importResult{
		SourceID: Sanitize(*source),
		BatchKey: Sanitize(doc.BatchKey),
		Facts:    make([]factRow, 0, len(imported)),
	}
	for _, fact := range imported {
		res.Facts = append(res.Facts, renderFact(fact))
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if err := writeDetail(a.stdout, [][2]string{
		{"source", res.SourceID},
		{"batch", res.BatchKey},
		{"imported", strconv.Itoa(len(res.Facts)) + " " + plural(len(res.Facts), "fact", "facts")},
	}); err != nil {
		return err
	}
	// The facts are listed rather than counted: an operator who imported a
	// batch needs the identifiers to inspect or dispute what it authorized.
	fmt.Fprint(a.stdout, "\nfacts\n")
	table := make([][]string, 0, len(res.Facts))
	for _, fact := range res.Facts {
		table = append(table, []string{
			fact.ID, fact.SubjectID, fact.Predicate, fact.Value, fact.Status, fact.Authority,
		})
	}
	return writeTable(a.stdout,
		[]string{"FACT", "SUBJECT", "PREDICATE", "VALUE", "STATUS", "AUTHORITY"}, table)
}

// decodeOneJSON reads exactly one document of type T from a file or stdin.
//
// Trailing data is rejected, and so is an unrecognized field: a misspelled
// "valid_until" that was silently dropped would import an open-ended fact the
// source never asserted, and a misspelled "predicates" would register a
// source authorized for nothing. The document is never echoed back, because
// an inventory names hosts and paths.
func decodeOneJSON[T any](a *app, from, what string) (T, error) {
	var doc T
	in := a.stdin
	if from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return doc, fmt.Errorf("open %s %s: %w", what, from, err)
		}
		defer f.Close()
		in = f
	}

	dec := json.NewDecoder(in)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		var zero T
		return zero, fmt.Errorf("decode %s: %w", what, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		var zero T
		return zero, fmt.Errorf("decode %s: %w", what, err)
	}
	return doc, nil
}

func renderQuestion(q reality.Question) questionRow {
	row := questionRow{
		ID:          Sanitize(q.ID),
		Kind:        Sanitize(string(q.Kind)),
		Class:       Sanitize(string(q.Class)),
		State:       Sanitize(string(q.State)),
		Sensitivity: Sanitize(string(q.Sensitivity)),
		CreatedAt:   formatTime(q.CreatedAt),
		Prompt:      Sanitize(q.Payload.Prompt),
		WhyAsked:    Sanitize(q.Payload.WhyAsked),
		Entities:    sanitizeAll(q.TargetEntityIDs),
	}
	for _, p := range q.TargetPredicates {
		row.Predicates = append(row.Predicates, Sanitize(string(p)))
	}
	return row
}

func renderFact(f reality.Fact) factRow {
	row := factRow{
		ID:          Sanitize(f.ID),
		SubjectID:   Sanitize(f.SubjectID),
		Predicate:   Sanitize(string(f.Predicate)),
		ValueKind:   Sanitize(string(f.Value.Kind)),
		Value:       Sanitize(factValue(f.Value)),
		ObjectID:    Sanitize(f.Value.ObjectID),
		Status:      Sanitize(string(f.Status)),
		Confidence:  Sanitize(string(f.Confidence)),
		Sensitivity: Sanitize(string(f.Sensitivity)),
		Authority:   Sanitize(string(f.Authority.Kind) + " " + f.Authority.ID),
		ValidFrom:   formatTime(f.ValidFrom),
		ValidUntil:  formatTime(f.ValidUntil),
		ObservedAt:  formatTime(f.ObservedAt),
		RecordedAt:  formatTime(f.RecordedAt),
		ExpiresAt:   formatTime(f.ExpiresAt),
		Supersedes:  Sanitize(f.Supersedes),
		Note:        Sanitize(f.Payload.Note),
	}
	if loc := f.Payload.Provenance; loc != nil {
		provenance := renderLocator(*loc, "")
		row.Provenance = &provenance
	}
	return row
}

// factValue renders whichever of a typed value's fields Kind selects. The
// zero value of the others is not a value, so nothing else is displayed.
func factValue(v reality.FactValue) string {
	switch v.Kind {
	case reality.ValueEnum:
		return v.Enum
	case reality.ValueEntity:
		return v.ObjectID
	default:
		return v.Text
	}
}

func parseQuestionClass(c *cmd, value string) (reality.QuestionClass, error) {
	known := []reality.QuestionClass{
		reality.ClassBlocking, reality.ClassMaintenance, reality.ClassCuriosity,
	}
	if slices.Contains(known, reality.QuestionClass(value)) {
		return reality.QuestionClass(value), nil
	}
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	return "", c.usagef("unknown --class %q (want one of %s)", value, strings.Join(names, ", "))
}

func parseAnswerOutcome(c *cmd, value string) (reality.AnswerOutcome, error) {
	known := []reality.AnswerOutcome{
		reality.OutcomeAnswered, reality.OutcomeUnknown, reality.OutcomeDeclined,
	}
	if slices.Contains(known, reality.AnswerOutcome(value)) {
		return reality.AnswerOutcome(value), nil
	}
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	return "", c.usagef("unknown --outcome %q (want one of %s)", value, strings.Join(names, ", "))
}

func parsePredicate(c *cmd, value string) (reality.Predicate, error) {
	known := reality.Predicates()
	if slices.Contains(known, reality.Predicate(value)) {
		return reality.Predicate(value), nil
	}
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	return "", c.usagef("unknown --predicate %q (want one of %s)", value, strings.Join(names, ", "))
}

func parseEntityKind(c *cmd, value string) (reality.EntityKind, error) {
	known := reality.EntityKinds()
	if slices.Contains(known, reality.EntityKind(value)) {
		return reality.EntityKind(value), nil
	}
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	return "", c.usagef("unknown entity kind %q (want one of %s)", value, strings.Join(names, ", "))
}

// parseAliasSpec reads one KIND=VALUE alias.
//
// The value keeps every "=" after the first, because a URL alias is a normal
// thing to record and splitting on all of them would silently truncate one.
func parseAliasSpec(c *cmd, spec string) (reality.AliasInput, error) {
	rawKind, value, ok := strings.Cut(spec, "=")
	if !ok || rawKind == "" || value == "" {
		return reality.AliasInput{}, c.usagef("--alias %q is not KIND=VALUE", spec)
	}
	known := reality.AliasKinds()
	if !slices.Contains(known, reality.AliasKind(rawKind)) {
		names := make([]string, 0, len(known))
		for _, k := range known {
			names = append(names, string(k))
		}
		return reality.AliasInput{}, c.usagef("unknown alias kind %q (want one of %s)",
			rawKind, strings.Join(names, ", "))
	}
	return reality.AliasInput{
		Kind:    reality.AliasKind(rawKind),
		Payload: reality.AliasPayload{Value: value},
	}, nil
}
