package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
)

const hypothesesUsage = `Usage: babel hypotheses [flags]

Lists the candidate hypotheses Babel holds (SPEC.md §4.2, §5.2). Rejection
and deferral never remove a candidate, so the listing includes candidates no
run will develop further; --status narrows to one lifecycle state.

Order is creation then identifier, a total order and deliberately not
priority: §5.4 forbids a list position from reading as strength.

Every listing reports a total beside its page, so a script pages by
arithmetic rather than by discovering the end when it hits it.

Flags:
  --status S    one of untriaged, queued, investigating, deferred, rejected,
                promoted
  --leaves      show only current revisions, not superseded drafts
  --limit N     page size (default 50, maximum 500)
  --offset N    skip this many rows
  --json        emit the listing as JSON on stdout
`

const hypothesisUsage = `Usage: babel hypothesis show ID [--json]

Shows one candidate with everything recorded about it: the statement in the
model's original wording, its append-only status history, the observations
that develop it, and the typed links to and from other candidates.

Flags:
  --json    emit the record as JSON on stdout
`

const findingsUsage = `Usage: babel findings [flags]

Lists the consolidated findings (SPEC.md §4.4). A finding exists only as the
consolidation of observations, so every row names the observations behind it.

Flags:
  --leaves      show only current revisions, not superseded drafts
  --limit N     page size (default 50, maximum 500)
  --offset N    skip this many rows
  --json        emit the listing as JSON on stdout
`

const findingUsage = `Usage: babel finding show ID [--json]

Shows one finding with the observations that support it and the proposals
that cite it.

Flags:
  --json    emit the record as JSON on stdout
`

// pageFlags are the paging knobs every enumeration shares. The bounds
// themselves belong to the stores, which clamp a zero or oversized limit
// rather than honour it, so a command passes the operator's request through
// and reports what came back.
type pageFlags struct {
	limit  int
	offset int
	leaves bool
}

func (pf *pageFlags) bind(c *cmd) {
	c.fs.IntVar(&pf.limit, "limit", 0, "page size; zero selects the store's default")
	c.fs.IntVar(&pf.offset, "offset", 0, "skip this many rows")
	c.fs.BoolVar(&pf.leaves, "leaves", false, "show only current revisions")
}

func (pf *pageFlags) filter(statuses []frontier.Status) frontier.ListFilter {
	return frontier.ListFilter{
		Statuses:   statuses,
		LeavesOnly: pf.leaves,
		Limit:      pf.limit,
		Offset:     pf.offset,
	}
}

// evidenceRow is one citation in machine-readable output. The locator is
// carried whole: §4.3 makes evidence inseparable from the locator that
// recovers it, and a rendering that dropped the offset or the digest would
// leave a claim that cannot be reopened.
type evidenceRow struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	ByteOffset int64  `json:"byte_offset"`
	Digest     string `json:"digest"`
	Note       string `json:"note,omitempty"`
}

// hypothesisRow is one candidate. The same shape serves the listing and the
// detail view, so a script that reads one reads the other.
type hypothesisRow struct {
	ID           string   `json:"id"`
	AncestorID   string   `json:"ancestor_id,omitempty"`
	RunID        string   `json:"run_id"`
	Status       string   `json:"status"`
	ReviewStatus string   `json:"review_status"`
	CreatedAt    string   `json:"created_at"`
	Statement    string   `json:"statement"`
	Novelty      float64  `json:"novelty"`
	Priority     float64  `json:"priority"`
	OriginCues   []string `json:"origin_cues,omitempty"`
	Labels       []string `json:"provisional_labels,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

type observationRow struct {
	ID                    string        `json:"id"`
	HypothesisID          string        `json:"hypothesis_id"`
	RunID                 string        `json:"run_id"`
	Recipe                string        `json:"recipe"`
	RecipeVersion         int           `json:"recipe_version"`
	CreatedAt             string        `json:"created_at"`
	Claim                 string        `json:"claim"`
	Category              string        `json:"category,omitempty"`
	Confidence            string        `json:"confidence"`
	Impact                string        `json:"impact"`
	TemporalStatus        string        `json:"temporal_status,omitempty"`
	Evidence              []evidenceRow `json:"evidence"`
	CounterEvidence       []evidenceRow `json:"counter_evidence,omitempty"`
	CounterEvidenceAbsent bool          `json:"counter_evidence_absent"`
}

type linkRow struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	CreatedAt string `json:"created_at"`
	Note      string `json:"note,omitempty"`
}

type statusRow struct {
	Sequence int64  `json:"sequence"`
	Status   string `json:"status"`
	RunID    string `json:"run_id,omitempty"`
	// Actor is who caused the transition. It is separate from RunID because
	// #87's revive belongs to an operator and to no run, so a history that
	// only named runs would show the most consequential transitions as
	// having no author at all.
	Actor      string `json:"actor"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

type findingRow struct {
	ID                    string        `json:"id"`
	AncestorID            string        `json:"ancestor_id,omitempty"`
	RunID                 string        `json:"run_id"`
	ReviewStatus          string        `json:"review_status"`
	CreatedAt             string        `json:"created_at"`
	Title                 string        `json:"title"`
	Pattern               string        `json:"pattern"`
	Significance          string        `json:"significance,omitempty"`
	Scope                 []string      `json:"scope,omitempty"`
	Recurrence            int           `json:"recurrence,omitempty"`
	TemporalStatus        string        `json:"temporal_status,omitempty"`
	ObservationIDs        []string      `json:"observation_ids"`
	HypothesisIDs         []string      `json:"hypothesis_ids"`
	CounterEvidence       []evidenceRow `json:"counter_evidence,omitempty"`
	CounterEvidenceAbsent bool          `json:"counter_evidence_absent"`
}

type proposalRow struct {
	ID                   string        `json:"id"`
	AncestorID           string        `json:"ancestor_id,omitempty"`
	RunID                string        `json:"run_id"`
	ReviewStatus         string        `json:"review_status"`
	CreatedAt            string        `json:"created_at"`
	Title                string        `json:"title"`
	Problem              string        `json:"problem"`
	Outcome              string        `json:"outcome"`
	Applicability        string        `json:"applicability,omitempty"`
	Uncertainty          string        `json:"uncertainty,omitempty"`
	Impact               string        `json:"impact"`
	EstimatedScope       string        `json:"estimated_scope,omitempty"`
	TemporalStatus       string        `json:"temporal_status,omitempty"`
	Classification       string        `json:"classification"`
	Destinations         []string      `json:"destinations,omitempty"`
	Risks                []string      `json:"risks,omitempty"`
	OpenQuestions        []string      `json:"open_questions,omitempty"`
	Prerequisites        []string      `json:"prerequisites,omitempty"`
	VerificationCriteria []string      `json:"verification_criteria,omitempty"`
	FindingIDs           []string      `json:"finding_ids"`
	HypothesisIDs        []string      `json:"hypothesis_ids"`
	Supporting           []evidenceRow `json:"supporting,omitempty"`
	Conflicting          []evidenceRow `json:"conflicting,omitempty"`
}

// hypothesesResult is `babel hypotheses --json`. Total is the whole matching
// set and Limit/Offset the page it was cut from, so a script pages by
// arithmetic instead of by requesting until it gets nothing.
type hypothesesResult struct {
	Hypotheses []hypothesisRow `json:"hypotheses"`
	Total      int             `json:"total"`
	Limit      int             `json:"limit"`
	Offset     int             `json:"offset"`
}

type hypothesisResult struct {
	Hypothesis    hypothesisRow    `json:"hypothesis"`
	StatusHistory []statusRow      `json:"status_history"`
	Observations  []observationRow `json:"observations"`
	LinksFrom     []linkRow        `json:"links_from"`
	LinksTo       []linkRow        `json:"links_to"`
}

type findingsResult struct {
	Findings []findingRow `json:"findings"`
	Total    int          `json:"total"`
	Limit    int          `json:"limit"`
	Offset   int          `json:"offset"`
}

type findingResult struct {
	Finding      findingRow       `json:"finding"`
	Observations []observationRow `json:"observations"`
	Proposals    []proposalRow    `json:"proposals"`
}

// hypothesesCmd implements `babel hypotheses`.
func (a *app) hypothesesCmd(ctx context.Context, args []string) error {
	c := newCmd("hypotheses", hypothesesUsage)
	var pf pageFlags
	status := c.fs.String("status", "", "narrow to one lifecycle status")
	pf.bind(c)
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	var statuses []frontier.Status
	if *status != "" {
		want, err := parseStatus(c, *status)
		if err != nil {
			return err
		}
		statuses = []frontier.Status{want}
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	records, total, err := state.frontier.Hypotheses(ctx, pf.filter(statuses))
	if err != nil {
		return err
	}
	rows := make([]hypothesisRow, 0, len(records))
	for _, record := range records {
		reviewStatus, err := state.frontier.ReviewStatus(ctx,
			frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID})
		if err != nil {
			return err
		}
		rows = append(rows, renderHypothesis(record, reviewStatus))
	}

	res := hypothesesResult{Hypotheses: rows, Total: total, Limit: pf.limit, Offset: pf.offset}
	if *asJSON {
		return a.emitJSON(res)
	}
	if total == 0 {
		fmt.Fprint(a.stdout, "no hypotheses; run \"babel explore\" to produce some\n")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{row.ID, row.Status, row.ReviewStatus,
			strconv.FormatFloat(row.Priority, 'f', 2, 64), row.Statement})
	}
	if err := writeTable(a.stdout, []string{"ID", "STATUS", "REVIEW", "PRIORITY", "STATEMENT"}, table); err != nil {
		return err
	}
	return a.writePageFooter(len(rows), pf.offset, total)
}

// hypothesisCmd routes `babel hypothesis <verb>`.
func (a *app) hypothesisCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "hypothesis requires a subcommand", usage: hypothesisUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, hypothesisUsage)
		return nil
	case "show":
		return a.hypothesisShow(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown hypothesis subcommand %q", args[0]), usage: hypothesisUsage}
	}
}

func (a *app) hypothesisShow(ctx context.Context, args []string) error {
	c := newCmd("hypothesis show", hypothesisUsage)
	asJSON := c.fs.Bool("json", false, "emit the record as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	record, err := state.frontier.Hypothesis(ctx, id)
	if err != nil {
		return unknownRecord(err, "hypothesis", id)
	}
	reviewStatus, err := state.frontier.ReviewStatus(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: id})
	if err != nil {
		return err
	}
	history, err := state.frontier.StatusHistory(ctx, id)
	if err != nil {
		return err
	}
	observations, err := state.frontier.ObservationsFor(ctx, id)
	if err != nil {
		return err
	}
	from, err := state.frontier.LinksFrom(ctx, id)
	if err != nil {
		return err
	}
	to, err := state.frontier.LinksTo(ctx, id)
	if err != nil {
		return err
	}

	res := hypothesisResult{
		Hypothesis:    renderHypothesis(record, reviewStatus),
		StatusHistory: renderStatusHistory(history),
		Observations:  renderObservations(observations),
		LinksFrom:     renderLinks(from),
		LinksTo:       renderLinks(to),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"id", res.Hypothesis.ID},
		{"status", res.Hypothesis.Status},
		{"review", res.Hypothesis.ReviewStatus},
		{"run", res.Hypothesis.RunID},
		{"created", res.Hypothesis.CreatedAt},
		{"ancestor", orMissing(res.Hypothesis.AncestorID)},
		{"novelty", strconv.FormatFloat(res.Hypothesis.Novelty, 'f', 2, 64)},
		{"priority", strconv.FormatFloat(res.Hypothesis.Priority, 'f', 2, 64)},
		{"statement", res.Hypothesis.Statement},
		{"labels", orMissing(strings.Join(res.Hypothesis.Labels, " "))},
		{"origin cues", orMissing(strings.Join(res.Hypothesis.OriginCues, " "))},
		{"notes", orMissing(res.Hypothesis.Notes)},
	}
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nstatus history\n")
	statusTable := make([][]string, 0, len(res.StatusHistory))
	for _, e := range res.StatusHistory {
		statusTable = append(statusTable, []string{
			strconv.FormatInt(e.Sequence, 10), e.Status, e.Actor, e.RecordedAt, orMissing(e.Note),
		})
	}
	if err := writeTable(a.stdout, []string{"SEQ", "STATUS", "ACTOR", "RECORDED", "NOTE"}, statusTable); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nobservations\n")
	if err := a.writeObservations(res.Observations); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nlinks\n")
	linkTable := make([][]string, 0, len(res.LinksFrom)+len(res.LinksTo))
	for _, l := range res.LinksFrom {
		linkTable = append(linkTable, []string{"out", l.Type, l.ToID, orMissing(l.Note)})
	}
	for _, l := range res.LinksTo {
		linkTable = append(linkTable, []string{"in", l.Type, l.FromID, orMissing(l.Note)})
	}
	return writeTable(a.stdout, []string{"DIR", "TYPE", "OTHER", "NOTE"}, linkTable)
}

// findingsCmd implements `babel findings`.
func (a *app) findingsCmd(ctx context.Context, args []string) error {
	c := newCmd("findings", findingsUsage)
	var pf pageFlags
	pf.bind(c)
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	records, total, err := state.frontier.Findings(ctx, pf.filter(nil))
	if err != nil {
		return err
	}
	rows := make([]findingRow, 0, len(records))
	for _, record := range records {
		status, err := state.frontier.ReviewStatus(ctx, frontier.Ref{Type: frontier.EntityFinding, ID: record.ID})
		if err != nil {
			return err
		}
		rows = append(rows, renderFinding(record, status))
	}

	res := findingsResult{Findings: rows, Total: total, Limit: pf.limit, Offset: pf.offset}
	if *asJSON {
		return a.emitJSON(res)
	}
	if total == 0 {
		fmt.Fprint(a.stdout, "no findings; run \"babel explore\" to produce some\n")
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{row.ID, row.ReviewStatus, strconv.Itoa(len(row.ObservationIDs)), row.Title})
	}
	if err := writeTable(a.stdout, []string{"ID", "REVIEW", "OBSERVATIONS", "TITLE"}, table); err != nil {
		return err
	}
	return a.writePageFooter(len(rows), pf.offset, total)
}

// findingCmd routes `babel finding <verb>`.
func (a *app) findingCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "finding requires a subcommand", usage: findingUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, findingUsage)
		return nil
	case "show":
		return a.findingShow(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown finding subcommand %q", args[0]), usage: findingUsage}
	}
}

func (a *app) findingShow(ctx context.Context, args []string) error {
	c := newCmd("finding show", findingUsage)
	asJSON := c.fs.Bool("json", false, "emit the record as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	record, err := state.frontier.Finding(ctx, id)
	if err != nil {
		return unknownRecord(err, "finding", id)
	}
	status, err := state.frontier.ReviewStatus(ctx, frontier.Ref{Type: frontier.EntityFinding, ID: id})
	if err != nil {
		return err
	}
	observations := make([]frontier.Observation, 0, len(record.ObservationIDs))
	for _, obsID := range record.ObservationIDs {
		obs, err := state.frontier.Observation(ctx, obsID)
		if err != nil {
			return fmt.Errorf("read observation %s: %w", obsID, err)
		}
		observations = append(observations, obs)
	}
	proposals, err := proposalsCiting(ctx, state, id)
	if err != nil {
		return err
	}

	res := findingResult{
		Finding:      renderFinding(record, status),
		Observations: renderObservations(observations),
		Proposals:    proposals,
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"id", res.Finding.ID},
		{"review", res.Finding.ReviewStatus},
		{"run", res.Finding.RunID},
		{"created", res.Finding.CreatedAt},
		{"title", res.Finding.Title},
		{"pattern", res.Finding.Pattern},
		{"significance", orMissing(res.Finding.Significance)},
		{"scope", orMissing(strings.Join(res.Finding.Scope, " "))},
		{"recurrence", strconv.Itoa(res.Finding.Recurrence)},
		{"temporal", orMissing(res.Finding.TemporalStatus)},
		{"counter-evidence", counterEvidenceLabel(res.Finding.CounterEvidence, res.Finding.CounterEvidenceAbsent)},
	}
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nobservations\n")
	if err := a.writeObservations(res.Observations); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nproposals\n")
	table := make([][]string, 0, len(res.Proposals))
	for _, p := range res.Proposals {
		table = append(table, []string{p.ID, p.ReviewStatus, p.Classification, p.Title})
	}
	return writeTable(a.stdout, []string{"ID", "REVIEW", "CLASS", "TITLE"}, table)
}

// writeObservations renders a claim table with its evidence count. Locators
// are not put in the table: they are long, they are the part a reviewer
// copies verbatim, and a truncated locator recovers nothing.
func (a *app) writeObservations(rows []observationRow) error {
	table := make([][]string, 0, len(rows))
	for _, o := range rows {
		table = append(table, []string{o.ID, o.Confidence, o.Impact,
			strconv.Itoa(len(o.Evidence)), o.Claim})
	}
	if err := writeTable(a.stdout, []string{"ID", "CONFIDENCE", "IMPACT", "EVIDENCE", "CLAIM"}, table); err != nil {
		return err
	}
	for _, o := range rows {
		for _, e := range o.Evidence {
			fmt.Fprintf(a.stdout, "  %s  %s:%d  %s\n", o.ID, e.Path, e.Line, e.Digest)
		}
	}
	return nil
}

// writePageFooter states where a terminal listing sits in the whole set.
// The machine-readable document carries the same three numbers as fields;
// this is the human's copy of them, and it is omitted when the page is the
// whole set because a footer that always fires is one nobody reads.
func (a *app) writePageFooter(shown, offset, total int) error {
	if offset == 0 && shown == total {
		return nil
	}
	_, err := fmt.Fprintf(a.stdout, "\nrows %d-%d of %d\n", offset+1, offset+shown, total)
	return err
}

// proposalsCiting finds the proposals that name one finding.
//
// The frontier answers "which findings does this proposal cite" and not the
// reverse, so the proposals are enumerated and filtered. Enumeration is
// paged rather than asked for whole: the store clamps a page at
// MaxListLimit, so a caller that wants every row walks the pages instead of
// silently receiving the first five hundred.
func proposalsCiting(ctx context.Context, state *analysisState, findingID string) ([]proposalRow, error) {
	var out []proposalRow
	for offset := 0; ; {
		page, total, err := state.frontier.Proposals(ctx,
			frontier.ListFilter{Limit: frontier.MaxListLimit, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, record := range page {
			if slices.Contains(record.FindingIDs, findingID) {
				out = append(out, renderProposal(record))
			}
		}
		offset += len(page)
		if len(page) == 0 || offset >= total {
			return out, nil
		}
	}
}

// parseStatus validates a --status value against §4.2's closed vocabulary.
func parseStatus(c *cmd, value string) (frontier.Status, error) {
	if value == "" {
		return "", nil
	}
	known := []frontier.Status{
		frontier.StatusUntriaged, frontier.StatusQueued, frontier.StatusInvestigating,
		frontier.StatusDeferred, frontier.StatusRejected, frontier.StatusPromoted,
	}
	if slices.Contains(known, frontier.Status(value)) {
		return frontier.Status(value), nil
	}
	names := make([]string, 0, len(known))
	for _, s := range known {
		names = append(names, string(s))
	}
	return "", c.usagef("unknown --status %q (want one of %s)", value, strings.Join(names, ", "))
}

// unknownRecord turns an unknown identifier into a message naming what was
// looked for, rather than the store's internal sentinel.
func unknownRecord(err error, kind, id string) error {
	if errors.Is(err, frontier.ErrUnknownEntity) {
		return fmt.Errorf("no %s %q is recorded", kind, id)
	}
	return fmt.Errorf("read %s %s: %w", kind, id, err)
}

func renderHypothesis(h frontier.Hypothesis, status frontier.ReviewStatus) hypothesisRow {
	return hypothesisRow{
		ID:           Sanitize(h.ID),
		AncestorID:   Sanitize(h.AncestorID),
		RunID:        Sanitize(h.RunID),
		Status:       Sanitize(string(h.Status)),
		ReviewStatus: Sanitize(string(status)),
		CreatedAt:    formatTime(h.CreatedAt),
		Statement:    Sanitize(h.Payload.Statement),
		Novelty:      h.Payload.Novelty,
		Priority:     h.Payload.Priority,
		OriginCues:   sanitizeAll(h.Payload.OriginCues),
		Labels:       sanitizeAll(h.Payload.ProvisionalLabels),
		Notes:        Sanitize(h.Payload.Notes),
	}
}

func renderStatusHistory(events []frontier.StatusEvent) []statusRow {
	out := make([]statusRow, 0, len(events))
	for _, e := range events {
		out = append(out, statusRow{
			Sequence:   e.Sequence,
			Status:     Sanitize(string(e.Status)),
			RunID:      Sanitize(e.RunID),
			Actor:      renderActor(e.Actor),
			RecordedAt: formatTime(e.RecordedAt),
			Note:       Sanitize(e.Payload.Note),
		})
	}
	return out
}

// renderActor writes an attributable author as one terminal-safe cell. The
// kind is kept in front of the identity because "operator alex" and "run alex"
// are different claims and an identity alone would not distinguish them.
func renderActor(a frontier.Actor) string {
	if a.ID == "" {
		return ""
	}
	return Sanitize(string(a.Kind)) + " " + Sanitize(a.ID)
}

func renderObservations(records []frontier.Observation) []observationRow {
	out := make([]observationRow, 0, len(records))
	for _, o := range records {
		out = append(out, observationRow{
			ID:                    Sanitize(o.ID),
			HypothesisID:          Sanitize(o.HypothesisID),
			RunID:                 Sanitize(o.RunID),
			Recipe:                Sanitize(o.RecipeID),
			RecipeVersion:         o.RecipeVersion,
			CreatedAt:             formatTime(o.CreatedAt),
			Claim:                 Sanitize(o.Payload.Claim),
			Category:              Sanitize(o.Payload.Category),
			Confidence:            Sanitize(string(o.Payload.Confidence)),
			Impact:                Sanitize(string(o.Payload.Impact)),
			TemporalStatus:        Sanitize(string(o.Payload.TemporalStatus)),
			Evidence:              renderEvidence(o.Payload.Evidence),
			CounterEvidence:       renderEvidence(o.Payload.CounterEvidence),
			CounterEvidenceAbsent: o.Payload.CounterEvidenceAbsent,
		})
	}
	return out
}

func renderLinks(links []frontier.Link) []linkRow {
	out := make([]linkRow, 0, len(links))
	for _, l := range links {
		out = append(out, linkRow{
			ID:        Sanitize(l.ID),
			Type:      Sanitize(string(l.Type)),
			FromID:    Sanitize(l.FromID),
			ToID:      Sanitize(l.ToID),
			CreatedAt: formatTime(l.CreatedAt),
			Note:      Sanitize(l.Payload.Note),
		})
	}
	return out
}

func renderFinding(f frontier.Finding, status frontier.ReviewStatus) findingRow {
	return findingRow{
		ID:                    Sanitize(f.ID),
		AncestorID:            Sanitize(f.AncestorID),
		RunID:                 Sanitize(f.RunID),
		ReviewStatus:          Sanitize(string(status)),
		CreatedAt:             formatTime(f.CreatedAt),
		Title:                 Sanitize(f.Payload.Title),
		Pattern:               Sanitize(f.Payload.Pattern),
		Significance:          Sanitize(f.Payload.Significance),
		Scope:                 sanitizeAll(f.Payload.Scope),
		Recurrence:            f.Payload.Recurrence,
		TemporalStatus:        Sanitize(string(f.Payload.TemporalStatus)),
		ObservationIDs:        sanitizeAll(f.ObservationIDs),
		HypothesisIDs:         sanitizeAll(f.HypothesisIDs),
		CounterEvidence:       renderEvidence(f.Payload.CounterEvidence),
		CounterEvidenceAbsent: f.Payload.CounterEvidenceAbsent,
	}
}

func renderProposal(p frontier.Proposal) proposalRow {
	row := proposalRow{
		ID:                   Sanitize(p.ID),
		AncestorID:           Sanitize(p.AncestorID),
		RunID:                Sanitize(p.RunID),
		ReviewStatus:         Sanitize(string(p.ReviewStatus)),
		CreatedAt:            formatTime(p.CreatedAt),
		Title:                Sanitize(p.Payload.Title),
		Problem:              Sanitize(p.Payload.Problem),
		Outcome:              Sanitize(p.Payload.Outcome),
		Applicability:        Sanitize(p.Payload.Applicability),
		Uncertainty:          Sanitize(p.Payload.Uncertainty),
		Impact:               Sanitize(string(p.Payload.Impact)),
		EstimatedScope:       Sanitize(p.Payload.EstimatedScope),
		TemporalStatus:       Sanitize(string(p.Payload.TemporalStatus)),
		Classification:       Sanitize(string(p.Payload.Classification)),
		Risks:                sanitizeAll(p.Payload.Risks),
		OpenQuestions:        sanitizeAll(p.Payload.OpenQuestions),
		Prerequisites:        sanitizeAll(p.Payload.Prerequisites),
		VerificationCriteria: sanitizeAll(p.Payload.VerificationCriteria),
		FindingIDs:           sanitizeAll(p.FindingIDs),
		HypothesisIDs:        sanitizeAll(p.HypothesisIDs),
		Supporting:           renderEvidence(p.Payload.Supporting),
		Conflicting:          renderEvidence(p.Payload.Conflicting),
	}
	for _, d := range p.Payload.Destinations {
		row.Destinations = append(row.Destinations, Sanitize(string(d)))
	}
	return row
}

func renderEvidence(list []frontier.Evidence) []evidenceRow {
	if len(list) == 0 {
		return nil
	}
	out := make([]evidenceRow, 0, len(list))
	for _, e := range list {
		out = append(out, renderLocator(e.Locator(), e.Note()))
	}
	return out
}

func renderLocator(loc event.Locator, note string) evidenceRow {
	return evidenceRow{
		Path:       Sanitize(loc.Path),
		Line:       loc.Line,
		ByteOffset: loc.ByteOffset,
		Digest:     Sanitize(loc.Digest),
		Note:       Sanitize(note),
	}
}

// counterEvidenceLabel states §4.4's exactly-one rule in a terminal cell:
// an empty list and an unasked question are different facts.
func counterEvidenceLabel(rows []evidenceRow, absent bool) string {
	switch {
	case len(rows) > 0:
		return fmt.Sprintf("%d %s", len(rows), plural(len(rows), "citation", "citations"))
	case absent:
		return "none found, explicitly"
	default:
		return missingValue
	}
}
