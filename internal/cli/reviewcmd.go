package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
)

const reviewUsage = `Usage: babel review <command> [flags]

Commands:
  queue                list the records awaiting a decision
  decide ID            record one attributed review decision
  history ID           show one record's append-only decision history

Review is append-only (SPEC.md §4.7). Rejecting a record never deletes it,
reconsidering appends another decision, and both stay readable in order.
No decision publishes anything: accepting a proposal records that a person
accepted it and nothing else (§4.6, decision 13).

Run "babel review <command> -h" for a command's flags.
`

const reviewQueueUsage = `Usage: babel review queue [flags]

Lists the records enrolled for review, oldest enrolment first. The order is
enrolment order and nothing else: §5.2 confines novelty and priority to
ordering and a queue that re-sorted itself by a model-produced score would
be doing the reviewer's job.

Every row carries its sync state (SPEC.md §9). --fleet adds the other hosts'
committed reviewable records, attributed to the machines that produced them:
a review inbox that stopped at this machine would leave the deployment's
analysis reviewable only where it was produced.

Flags:
  --type T      narrow to one kind: hypothesis, observation, finding, proposal
  --status S    narrow to one review status: new, accepted, rejected,
                deferred, duplicate, refine-requested
  --all         include records already decided
  --fleet       also list the other hosts' committed reviewable records
  --limit N     bound the listing (default 100)
  --json        emit the queue as JSON on stdout
`

const reviewDecideUsage = `Usage: babel review decide ID (--accept|--reject|--defer|--duplicate-of ID) [flags]

Records one §4.7 review decision against the record the identifier names.
The kind comes from the identifier's own prefix, so a decision cannot land
on the wrong table.

A decision is attributed: §4.7 makes operator context attributed guidance,
and an anonymous acceptance would record that something was accepted without
recording that anyone accepted it. There is no default identity.

--context records attributed operator guidance alongside the decision. It is
guidance, never evidence: it explains the decision and supports nothing.

Nothing is published, applied, or written to a source repository.

Flags:
  --accept              accept the record
  --reject              reject it; it stays readable with its whole history
  --defer               defer it
  --duplicate-of ID     mark it a duplicate of the record this names
  --context TEXT        attributed operator guidance recorded with the decision
  --note TEXT           the reviewer's own words about the decision
  --operator ID         operator identity (default $BABEL_OPERATOR)
  --json                emit the decision as JSON on stdout
`

const reviewHistoryUsage = `Usage: babel review history ID [--json]

Shows one record's derived review status, every decision recorded against
it in order with the guidance each cited, and every refinement request a
rejection authorized.

Flags:
  --json    emit the history as JSON on stdout
`

const exportUsage = `Usage: babel export ID [flags]

Renders one record as a document: the private view, whole, with its
provenance and review history intact (SPEC.md §6.7). It writes to stdout, or
to the file --output names, and stops there. Nothing is opened, published,
or sent: sanitized destination projections are Phase C and deliberately
absent, because trimming locators and smoothing uncertainty is exactly the
transformation an export must not quietly perform.

The identifier's prefix picks the record: hyp_, obs_, fnd_, pro_, or a run
receipt id.

Secret values found by preflight are redacted by default. --raw turns that
off, which §8 makes an explicit act rather than a default.

Flags:
  --format F     json or markdown (default json)
  --output FILE  write the document to this file instead of stdout
  --raw          do not redact secret values
`

// queueRow is one review-queue entry in machine-readable output.
type queueRow struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Status        string `json:"status"`
	EnrolledAt    string `json:"enrolled_at"`
	Decisions     int    `json:"decisions"`
	LastDecidedAt string `json:"last_decided_at,omitempty"`
	Refinements   int    `json:"refinements"`
	// Title is the record's own heading, resolved so a queue is readable
	// without a second command per row.
	Title string `json:"title"`
}

// queueResult is `babel review queue --json`. It carries no total: the
// queue is a filtered page of enrolled records and review.Queue reports no
// count beyond what it returned, so a field named total would be the page
// length wearing a misleading name. The frontier listings, which do have a
// real total, report one.
type queueResult struct {
	Items []queueRow `json:"items"`
	// Sync maps each listed record id to its sync state, and Fleet carries the
	// other hosts' committed reviewable records, for the reasons
	// hypothesesResult gives.
	Sync  map[string]string `json:"sync,omitempty"`
	Fleet []fleetRecordRow  `json:"fleet,omitempty"`
}

// decisionRow is one recorded disposition.
type decisionRow struct {
	ID            string `json:"id"`
	Sequence      int64  `json:"sequence"`
	Type          string `json:"type"`
	SubjectID     string `json:"subject_id"`
	Disposition   string `json:"disposition"`
	ReviewerID    string `json:"reviewer_id"`
	ContextID     string `json:"context_id,omitempty"`
	ContextText   string `json:"context_text,omitempty"`
	DuplicateOfID string `json:"duplicate_of_id,omitempty"`
	Note          string `json:"note,omitempty"`
	RecordedAt    string `json:"recorded_at"`
}

type refinementRow struct {
	ID            string   `json:"id"`
	DispositionID string   `json:"disposition_id"`
	CreatedAt     string   `json:"created_at"`
	Guidance      string   `json:"guidance"`
	Scope         []string `json:"scope,omitempty"`
	// Outcome is absent until a refinement worker reported one, which §4.7
	// makes a normal visible state rather than a gap.
	OutcomeID string `json:"outcome_id,omitempty"`
}

type decideResult struct {
	Decision decisionRow `json:"decision"`
	Status   string      `json:"status"`
}

type historyResult struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Decisions   []decisionRow   `json:"decisions"`
	Refinements []refinementRow `json:"refinements"`
}

// review routes `babel review <verb>`.
func (a *app) review(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "review requires a subcommand", usage: reviewUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, reviewUsage)
		return nil
	case "queue":
		return a.reviewQueue(ctx, args[1:])
	case "decide":
		return a.reviewDecide(ctx, args[1:])
	case "history":
		return a.reviewHistory(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown review subcommand %q", args[0]), usage: reviewUsage}
	}
}

func (a *app) reviewQueue(ctx context.Context, args []string) error {
	c := newCmd("review queue", reviewQueueUsage)
	var ff fleetFlags
	kind := c.fs.String("type", "", "narrow to one record kind")
	status := c.fs.String("status", "", "narrow to one review status")
	all := c.fs.Bool("all", false, "include records already decided")
	ff.bind(c)
	limit := c.fs.Int("limit", review.DefaultQueueLimit, "bound the listing")
	asJSON := c.fs.Bool("json", false, "emit the queue as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	filter := review.QueueFilter{AllStatuses: *all, Limit: *limit}
	if *kind != "" {
		t, err := parseEntityType(c, *kind)
		if err != nil {
			return err
		}
		filter.Type = t
	}
	if *status != "" {
		s, err := parseReviewStatus(c, *status)
		if err != nil {
			return err
		}
		filter.Status = s
	}
	reader, release, err := a.fleetListingReader(ctx, ff.fleetWide)
	if err != nil {
		return fleetUnavailable(err)
	}
	defer release()

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	items, err := state.review.Queue(ctx, filter)
	if err != nil {
		return err
	}
	rows := make([]queueRow, 0, len(items))
	ids := make([]string, 0, len(items))
	for _, item := range items {
		title, err := recordTitle(ctx, state, item.Subject)
		if err != nil {
			return err
		}
		row := queueRow{
			Type:        Sanitize(string(item.Subject.Type)),
			ID:          Sanitize(item.Subject.ID),
			Status:      Sanitize(string(item.Status)),
			EnrolledAt:  formatTime(item.EnrolledAt),
			Decisions:   item.Decisions,
			Refinements: item.Refinements,
			Title:       title,
		}
		if !item.LastDecidedAt.IsZero() {
			row.LastDecidedAt = formatTime(item.LastDecidedAt)
		}
		rows = append(rows, row)
		ids = append(ids, item.Subject.ID)
	}
	sync, err := a.syncColumn(ctx, reader, ids)
	if err != nil {
		return err
	}

	res := queueResult{Items: rows, Sync: sync}
	var fetched int
	if ff.fleetWide {
		// The queue's own --type vocabulary decides which kinds cross: an
		// operator who narrowed the local queue to findings did not ask to see
		// every other machine's candidates.
		res.Fleet, fetched, err = a.fleetListingRows(ctx, reader,
			reviewableRecordKinds(filter.Type), ids, *limit)
		if err != nil {
			return err
		}
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(rows) == 0 && len(res.Fleet) == 0 {
		fmt.Fprint(a.stdout, "nothing awaiting review\n")
		return nil
	}
	header := []string{"SYNC", "TYPE", "ID", "STATUS", "DECISIONS", "TITLE"}
	table := make([][]string, 0, len(rows)+len(res.Fleet))
	for _, row := range rows {
		table = append(table, []string{syncCell(sync, row.ID), row.Type, row.ID, row.Status,
			strconv.Itoa(row.Decisions), row.Title})
	}
	if ff.fleetWide {
		header = append([]string{"HOST"}, header...)
		for i, row := range table {
			table[i] = append([]string{localHostCell(reader)}, row...)
		}
		for _, row := range res.Fleet {
			// A remote record's review status and decision count are this
			// machine's review log's, and this machine's review log holds no
			// decisions about another machine's record until someone here makes
			// one. Absent, therefore, rather than "new" with zero decisions —
			// which would claim the record has been triaged nowhere.
			table = append(table, []string{row.hostCell(), row.Sync, orMissing(row.Kind),
				row.RecordID, missingValue, missingValue, row.summaryCell()})
		}
	}
	if err := writeTable(a.stdout, header, table); err != nil {
		return err
	}
	if ff.fleetWide {
		a.fleetListingNote(res.Fleet, fetched, *limit)
	}
	return nil
}

func (a *app) reviewDecide(ctx context.Context, args []string) error {
	c := newCmd("review decide", reviewDecideUsage)
	var of operatorFlags
	of.bind(c)
	accept := c.fs.Bool("accept", false, "accept the record")
	reject := c.fs.Bool("reject", false, "reject the record")
	defer_ := c.fs.Bool("defer", false, "defer the record")
	duplicateOf := c.fs.String("duplicate-of", "", "mark the record a duplicate of this one")
	contextText := c.fs.String("context", "", "attributed operator guidance recorded with the decision")
	note := c.fs.String("note", "", "the reviewer's own words about the decision")
	asJSON := c.fs.Bool("json", false, "emit the decision as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	subject, err := refFor(c, id)
	if err != nil {
		return err
	}
	disposition, err := pickDisposition(c, *accept, *reject, *defer_, *duplicateOf)
	if err != nil {
		return err
	}
	// Attribution is resolved before anything is opened, so a decision that
	// nobody signed never reaches the store at all.
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}
	by, err := authorityFor(operator)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	decision := review.Decision{
		Subject:       subject,
		Disposition:   disposition,
		By:            by,
		DuplicateOfID: *duplicateOf,
		Note:          *note,
	}
	if *contextText != "" {
		recorded, err := state.review.RecordContext(ctx, by, *contextText)
		if err != nil {
			return err
		}
		decision.ContextID = recorded.ID
	}
	event, err := state.review.Decide(ctx, decision)
	if err != nil {
		if errors.Is(err, review.ErrUnknownRecord) {
			return fmt.Errorf("no %s %q is recorded", subject.Type, id)
		}
		return err
	}
	status, err := state.frontier.ReviewStatus(ctx, subject)
	if err != nil {
		return err
	}

	res := decideResult{Decision: renderDecision(review.ReviewEvent{Event: event}), Status: Sanitize(string(status))}
	if *contextText != "" {
		res.Decision.ContextText = Sanitize(*contextText)
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"decision", res.Decision.ID},
		{"subject", res.Decision.Type + " " + res.Decision.SubjectID},
		{"disposition", res.Decision.Disposition},
		{"reviewer", res.Decision.ReviewerID},
		{"recorded", res.Decision.RecordedAt},
		{"status", res.Status},
		{"published", "nothing; review records a decision and stops (SPEC.md §4.6)"},
	})
}

func (a *app) reviewHistory(ctx context.Context, args []string) error {
	c := newCmd("review history", reviewHistoryUsage)
	asJSON := c.fs.Bool("json", false, "emit the history as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	subject, err := refFor(c, id)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	history, err := state.review.History(ctx, subject)
	if err != nil {
		if errors.Is(err, review.ErrUnknownRecord) {
			return fmt.Errorf("no %s %q is recorded", subject.Type, id)
		}
		return err
	}

	res := historyResult{
		Type:   Sanitize(string(subject.Type)),
		ID:     Sanitize(subject.ID),
		Status: Sanitize(string(history.Status)),
	}
	for _, d := range history.Decisions {
		res.Decisions = append(res.Decisions, renderDecision(d))
	}
	for _, r := range history.Refinements {
		row := refinementRow{
			ID:            Sanitize(r.Request.ID),
			DispositionID: Sanitize(r.Request.DispositionID),
			CreatedAt:     formatTime(r.Request.CreatedAt),
			Guidance:      Sanitize(r.Request.Payload.Guidance),
			Scope:         sanitizeAll(r.Request.Payload.Scope),
		}
		if r.Outcome != nil {
			row.OutcomeID = Sanitize(r.Outcome.ID)
		}
		res.Refinements = append(res.Refinements, row)
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if err := writeDetail(a.stdout, [][2]string{
		{"subject", res.Type + " " + res.ID},
		{"status", res.Status},
	}); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\ndecisions\n")
	table := make([][]string, 0, len(res.Decisions))
	for _, d := range res.Decisions {
		table = append(table, []string{strconv.FormatInt(d.Sequence, 10), d.Disposition,
			d.ReviewerID, d.RecordedAt, orMissing(d.Note)})
	}
	if err := writeTable(a.stdout, []string{"SEQ", "DISPOSITION", "REVIEWER", "RECORDED", "NOTE"}, table); err != nil {
		return err
	}
	if len(res.Refinements) == 0 {
		return nil
	}
	fmt.Fprint(a.stdout, "\nrefinement requests\n")
	refTable := make([][]string, 0, len(res.Refinements))
	for _, r := range res.Refinements {
		refTable = append(refTable, []string{r.ID, r.CreatedAt, orMissing(r.OutcomeID), r.Guidance})
	}
	return writeTable(a.stdout, []string{"ID", "CREATED", "OUTCOME", "GUIDANCE"}, refTable)
}

// exportCmd implements `babel export`.
func (a *app) exportCmd(ctx context.Context, args []string) error {
	c := newCmd("export", exportUsage)
	format := c.fs.String("format", "json", "json or markdown")
	output := c.fs.String("output", "", "write the document to this file instead of stdout")
	raw := c.fs.Bool("raw", false, "do not redact secret values")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	node, err := exportNode(c, id)
	if err != nil {
		return err
	}
	switch *format {
	case "json", "markdown":
	default:
		return c.usagef("unknown --format %q (want json or markdown)", *format)
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	doc, err := state.review.Export(ctx, node, review.ExportOptions{Raw: *raw})
	if err != nil {
		if errors.Is(err, review.ErrUnknownRecord) {
			return fmt.Errorf("no %s %q is recorded", node.Kind, id)
		}
		return err
	}
	var rendered []byte
	if *format == "json" {
		rendered, err = doc.JSON()
	} else {
		rendered, err = doc.Markdown()
	}
	if err != nil {
		return fmt.Errorf("render %s %s: %w", node.Kind, id, err)
	}

	if *output != "" {
		// A private export lands mode 0600 like every other file Babel
		// writes: it carries the whole record, including locators into the
		// corpus (SPEC.md §9).
		if err := ensureDir(filepath.Dir(*output)); err != nil {
			return err
		}
		if err := os.WriteFile(*output, rendered, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", *output, err)
		}
		a.diagf("wrote %s\n", Sanitize(*output))
		return nil
	}
	// The document is written to stdout byte for byte. It is not passed
	// through Sanitize: an export is a file a person reads and re-parses,
	// and escaping its contents would corrupt the JSON a caller decodes and
	// the Markdown a caller renders. --output is the ordinary path, and a
	// terminal-safe view of the same record is what `hypothesis show` and
	// `finding show` are for.
	_, err = a.stdout.Write(rendered)
	return err
}

// exportNode resolves an identifier to the record export addresses. Run
// receipts are exportable too, and they carry their own prefix, so the same
// resolution serves both without a --type flag to get wrong.
func exportNode(c *cmd, id string) (review.Node, error) {
	if strings.HasPrefix(id, "rcpt-") {
		return review.Node{Kind: review.KindRun, ID: id}, nil
	}
	prefix, _, _ := strings.Cut(id, "_")
	kind, known := recordKinds[prefix]
	if !known {
		return review.Node{}, c.usagef(
			"%q does not name an exportable record; identifiers start with fnd_, hyp_, obs_, pro_, or rcpt-", id)
	}
	return review.Node{Kind: review.Kind(kind), ID: id}, nil
}

// pickDisposition maps the mutually exclusive decision flags onto §4.7's
// closed vocabulary. Two decisions in one invocation is a rejected
// invocation rather than a precedence rule: there is no sensible winner
// between "accept" and "reject".
func pickDisposition(c *cmd, accept, reject, defer_ bool, duplicateOf string) (frontier.Disposition, error) {
	chosen := make([]string, 0, 4)
	var d frontier.Disposition
	if accept {
		chosen, d = append(chosen, "--accept"), frontier.DispositionAccept
	}
	if reject {
		chosen, d = append(chosen, "--reject"), frontier.DispositionReject
	}
	if defer_ {
		chosen, d = append(chosen, "--defer"), frontier.DispositionDefer
	}
	if duplicateOf != "" {
		chosen, d = append(chosen, "--duplicate-of"), frontier.DispositionDuplicate
	}
	switch len(chosen) {
	case 1:
		return d, nil
	case 0:
		return "", c.usagef("review decide requires one of --accept, --reject, --defer, or --duplicate-of ID")
	default:
		return "", c.usagef("review decide takes exactly one decision, got %s", strings.Join(chosen, " "))
	}
}

// parseEntityType validates a --type value against the kinds §6.7 makes
// reviewable. An observation is not among them: it is read through the
// hypothesis it develops or the finding it supports, never decided alone.
func parseEntityType(c *cmd, value string) (frontier.EntityType, error) {
	known := []frontier.EntityType{
		frontier.EntityHypothesis, frontier.EntityFinding, frontier.EntityProposal,
	}
	if slices.Contains(known, frontier.EntityType(value)) {
		return frontier.EntityType(value), nil
	}
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	return "", c.usagef("unknown --type %q (want one of %s)", value, strings.Join(names, ", "))
}

// parseReviewStatus validates a --status value against §4.5's derived states.
func parseReviewStatus(c *cmd, value string) (frontier.ReviewStatus, error) {
	known := []frontier.ReviewStatus{
		frontier.ReviewNew, frontier.ReviewAccepted, frontier.ReviewRejected,
		frontier.ReviewDeferred, frontier.ReviewDuplicate, frontier.ReviewRefineRequested,
	}
	if slices.Contains(known, frontier.ReviewStatus(value)) {
		return frontier.ReviewStatus(value), nil
	}
	names := make([]string, 0, len(known))
	for _, k := range known {
		names = append(names, string(k))
	}
	return "", c.usagef("unknown --status %q (want one of %s)", value, strings.Join(names, ", "))
}

// recordTitle resolves the heading a queue row shows. A record kind that has
// no title shows its claim or statement instead, because a queue whose rows
// are only identifiers cannot be triaged.
func recordTitle(ctx context.Context, state *analysisState, ref frontier.Ref) (string, error) {
	switch ref.Type {
	case frontier.EntityHypothesis:
		record, err := state.frontier.Hypothesis(ctx, ref.ID)
		if err != nil {
			return "", err
		}
		return Sanitize(record.Payload.Statement), nil
	case frontier.EntityObservation:
		record, err := state.frontier.Observation(ctx, ref.ID)
		if err != nil {
			return "", err
		}
		return Sanitize(record.Payload.Claim), nil
	case frontier.EntityFinding:
		record, err := state.frontier.Finding(ctx, ref.ID)
		if err != nil {
			return "", err
		}
		return Sanitize(record.Payload.Title), nil
	case frontier.EntityProposal:
		record, err := state.frontier.Proposal(ctx, ref.ID)
		if err != nil {
			return "", err
		}
		return Sanitize(record.Payload.Title), nil
	default:
		return "", fmt.Errorf("unreviewable record kind %q", ref.Type)
	}
}

func renderDecision(e review.ReviewEvent) decisionRow {
	row := decisionRow{
		ID:            Sanitize(e.Event.ID),
		Sequence:      e.Event.Sequence,
		Type:          Sanitize(string(e.Event.Subject.Type)),
		SubjectID:     Sanitize(e.Event.Subject.ID),
		Disposition:   Sanitize(string(e.Event.Disposition)),
		ReviewerID:    Sanitize(e.Event.ReviewerID),
		ContextID:     Sanitize(e.Event.ContextID),
		DuplicateOfID: Sanitize(e.Event.DuplicateOfID),
		Note:          Sanitize(e.Event.Payload.Note),
		RecordedAt:    formatTime(e.Event.RecordedAt),
	}
	if e.Context != nil {
		row.ContextText = Sanitize(e.Context.Text)
	}
	return row
}
