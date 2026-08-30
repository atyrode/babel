package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
  answer QUESTION_ID   record an attributed answer, retained verbatim
  accept PLAN_ID       accept one Answer Interpreter plan

The Reality Ledger holds what is true about the operator's world (SPEC.md
§4.8). A raw answer is a durable input; an authoritative fact requires an
explicit plan acceptance, and no model may authorize one. Answers and
acceptances are attributed acts, so both require an operator identity.

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
		return a.realityEntity(ctx, args[1:])
	case "answer":
		return a.realityAnswer(ctx, args[1:])
	case "accept":
		return a.realityAccept(ctx, args[1:])
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
