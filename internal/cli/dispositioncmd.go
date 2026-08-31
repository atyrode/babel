package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
)

// The commands in this file are issue #87's operator surface: the revision
// chain a record moves along, the resting statuses it can be revived out of,
// the proposed next actions attached to it, and the instruction-free
// invitations that put it in front of the next run.
//
// They are kind-agnostic verbs rather than a new noun, matching `babel export
// ID`: the record's kind comes from the identifier's own prefix, so an
// operator pastes what a listing printed and no --type flag can send a command
// at the wrong table. `revise` and `revive` are the two exceptions, and both
// say why in their usage: statuses exist only on candidates, and an operator
// can only retype the one payload that is a single sentence.

const revisionsUsage = `Usage: babel revisions ID [--json]

Shows one record's append-only revision chain (issue #87): every revision from
the original to the current one, with who made it and why.

The identifier may name any revision in the chain. The answer is the same
chain either way, so a history is readable from whichever identifier was
pasted rather than only from the newest one.

Records are immutable, so a revision is a whole new record that names its
ancestor; nothing is rewritten and nothing is a diff. The last row is the
current state.

Flags:
  --json    emit the chain as JSON on stdout
`

const reviseUsage = `Usage: babel revise ID --statement TEXT --reason TEXT [flags]

Appends an operator revision to a candidate: a new immutable record carrying
the new wording, linked to the one it supersedes, with the operator and the
reason recorded on the chain (issue #87).

Only candidates are revisable by hand. An observation, a finding, and a
proposal each carry structured payloads — evidence locators, supporting
record sets, targets — that a run assembles and a flag could not; those are
revised by the runs that produce them.

--reason is required. A revision with no argument behind it produces exactly
the history #87 exists to prevent: the current wording is visible and the case
for replacing the previous one is not.

The ancestor is left byte-identical and stays readable at its own identifier.
Revising a record something already supersedes is refused, naming the head.

Flags:
  --statement TEXT   the candidate's new wording
  --reason TEXT      why this revision supersedes its ancestor
  --notes TEXT       investigator notes carried onto the new revision
  --operator ID      operator identity (default $BABEL_OPERATOR)
  --json             emit the new revision as JSON on stdout
`

const reviveUsage = `Usage: babel revive ID --reason TEXT [flags]

Returns a resting candidate to the frontier (issue #87). Nothing in Babel
closes: deferred, rejected and promoted are resting states, and this is the
transition out of each of them.

The transition is refused for a candidate that is untriaged, queued, or under
investigation. The first two are already on the frontier, and the third
belongs to a running exploration, so reviving there would either mean nothing
or rewrite a live run's lifecycle from outside it.

--reason is required and attributed. A rejected candidate that reappeared with
no argument behind it would be indistinguishable from one nobody rejected.

Flags:
  --reason TEXT     why the candidate deserves to move again
  --status S        where it lands: queued (default), untriaged, investigating
  --operator ID     operator identity (default $BABEL_OPERATOR)
  --json            emit the transition as JSON on stdout
`

const inviteUsage = `Usage: babel invite ID [flags]

Records an instruction-free invitation to process a record further (issue
#87). It says a record deserves another look and deliberately says nothing
about what to do with it: refine, question, amend, or abandon stays the
model's judgement.

There is no flag for a hint, and there is no column to store one in. An
invitation that could carry a brief would stop being a nudge within one
release.

The queue is rung one of the conductor's work ladder (issue #96): an operator
invitation outranks any policy the loop chose for itself. A run consumes each
invitation once.

Flags:
  --operator ID     operator identity (default $BABEL_OPERATOR)
  --json            emit the invitation as JSON on stdout
`

const invitationsUsage = `Usage: babel invitations [flags]

Lists the process-further queue, oldest first (issue #87, #96).

Order is invitation order and nothing else. A queue of operator nudges
re-sorted by a model-produced score would be the loop deciding which of the
operator's requests mattered.

Flags:
  --record ID   narrow to one record's invitations
  --all         include invitations a run already consumed
  --limit N     bound the listing (default 100, maximum 500)
  --json        emit the queue as JSON on stdout
`

const dispositionsUsage = `Usage: babel dispositions [flags]

Lists the proposed next actions attached to records (issue #87): what a run
suggested could be done with a candidate, a finding, or a proposal, and what
the operator decided about each.

Every one is a proposal. Accepting a draft-issue opens no issue and accepting
a propose-reality-fact writes no fact; the ledger records that a person
authorized the action, and the action itself remains the operator's own, taken
through the surface that owns it under their own credentials (SPEC.md §4.6).

Flags:
  --record ID   narrow to one record's proposed actions
  --kind K      narrow to one kind: draft-issue, propose-reality-fact,
                store-memory, ask-question, develop-further
  --status S    narrow to one state: proposed, accepted, declined
  --limit N     page size (default 100, maximum 500)
  --offset N    skip this many rows
  --json        emit the listing as JSON on stdout
`

const dispositionUsage = `Usage: babel disposition <command> [flags]

Commands:
  show ID           show one proposed action with its ledger
  propose RECORD    attach a proposed action to a record by hand
  accept ID         record that the operator authorized the action
  decline ID        record that the operator declined it

The ledger is append-only. Declining leaves the action readable, reconsidering
appends another entry, and both stay in order — which is what later
self-evaluation reads back as an acceptance rate (issue #88).

Run "babel disposition <command> -h" for a command's flags.
`

const dispositionShowUsage = `Usage: babel disposition show ID [--json]

Shows one proposed action, its derived state, and every operator decision
recorded against it in order. A draft-issue also shows the repository it is
bound to and renders the draft, which Babel writes to stdout and nowhere else.

Flags:
  --json    emit the action and its ledger as JSON on stdout
`

const dispositionProposeUsage = `Usage: babel disposition propose RECORD_ID --kind K --summary TEXT [flags]

Attaches a proposed next action to a record by hand. A run proposes actions
through its result schema; this is the operator-synthesized path, for an
action a person saw and a run did not.

--repo is required for draft-issue and refused for every other kind. The
directory must be a git checkout with an origin remote, and the anchor is read
out of that checkout's own configuration (issue #88): a repository nobody can
point at on this machine is structurally impossible to bind a draft to, and
Babel never asks GitHub, holds no credential, and publishes nothing.

Flags:
  --kind K          one of draft-issue, propose-reality-fact, store-memory,
                    ask-question, develop-further
  --summary TEXT    the action in one line
  --rationale TEXT  why the action fits this record
  --repo DIR        local checkout a draft-issue binds to
  --operator ID     operator identity (default $BABEL_OPERATOR)
  --json            emit the proposed action as JSON on stdout
`

const dispositionDecideUsage = `Usage: babel disposition (accept|decline) ID [flags]

Records one attributed operator decision about a proposed action.

Accepting authorizes nothing outside Babel by itself: no issue is opened, no
fact is written, no memory is stored. The entry is the durable, attributable
record that the operator authorized the action, and it is the evidence later
self-evaluation reads (issues #88, #94).

Flags:
  --note TEXT       the operator's own words about the decision
  --operator ID     operator identity (default $BABEL_OPERATOR)
  --json            emit the ledger entry as JSON on stdout
`

// revisionRow is one entry of a record's chain in machine-readable output.
type revisionRow struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	RecordID     string `json:"record_id"`
	RootID       string `json:"root_id"`
	SupersedesID string `json:"supersedes_id,omitempty"`
	Sequence     int64  `json:"sequence"`
	Actor        string `json:"actor"`
	RecordedAt   string `json:"recorded_at"`
	Reason       string `json:"reason,omitempty"`
	// Head marks the current state, so a reader of the JSON does not have to
	// know that the last element is special.
	Head bool `json:"head"`
}

type revisionsResult struct {
	Type      string        `json:"type"`
	ID        string        `json:"id"`
	HeadID    string        `json:"head_id"`
	Revisions []revisionRow `json:"revisions"`
}

type reviseResult struct {
	Revision revisionRow `json:"revision"`
	// Supersedes is the record the new revision replaced, kept beside the
	// chain entry because it is what an operator checks first.
	Supersedes string `json:"supersedes"`
	Statement  string `json:"statement"`
}

type reviveResult struct {
	Type   string    `json:"type"`
	ID     string    `json:"id"`
	Status statusRow `json:"status"`
}

type invitationRow struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	RecordID   string `json:"record_id"`
	By         string `json:"by"`
	CreatedAt  string `json:"created_at"`
	ConsumedBy string `json:"consumed_by,omitempty"`
	ConsumedAt string `json:"consumed_at,omitempty"`
	Open       bool   `json:"open"`
}

type invitationsResult struct {
	Invitations []invitationRow `json:"invitations"`
}

type inviteResult struct {
	Invitation invitationRow `json:"invitation"`
}

type anchorRow struct {
	Workspace string `json:"workspace"`
	Remote    string `json:"remote"`
	URL       string `json:"url"`
	Branch    string `json:"branch,omitempty"`
}

type dispositionRow struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	RecordID   string     `json:"record_id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	ProposedBy string     `json:"proposed_by"`
	Ref        string     `json:"ref,omitempty"`
	CreatedAt  string     `json:"created_at"`
	Summary    string     `json:"summary"`
	Rationale  string     `json:"rationale,omitempty"`
	Anchor     *anchorRow `json:"anchor,omitempty"`
}

type ledgerRow struct {
	ID         string `json:"id"`
	Sequence   int64  `json:"sequence"`
	Ruling     string `json:"ruling"`
	By         string `json:"by"`
	RecordedAt string `json:"recorded_at"`
	Note       string `json:"note,omitempty"`
}

type dispositionsResult struct {
	Dispositions []dispositionRow `json:"dispositions"`
	Total        int              `json:"total"`
	Limit        int              `json:"limit"`
	Offset       int              `json:"offset"`
}

type dispositionResult struct {
	Disposition dispositionRow `json:"disposition"`
	Ledger      []ledgerRow    `json:"ledger"`
	// Draft is the rendered issue draft for a draft-issue action, absent
	// for every other kind. It is rendered onto stdout and nowhere else:
	// Babel drafts, the operator files.
	Draft string `json:"draft,omitempty"`
}

type decideDispositionResult struct {
	Entry  ledgerRow `json:"entry"`
	Status string    `json:"status"`
	// Published states what happened outside Babel, which is nothing. It is
	// a field rather than a comment because a script reading this document
	// is exactly the reader who might otherwise assume an accepted
	// draft-issue was filed.
	Published string `json:"published"`
}

// revisionsCmd implements `babel revisions ID`.
func (a *app) revisionsCmd(ctx context.Context, args []string) error {
	c := newCmd("revisions", revisionsUsage)
	asJSON := c.fs.Bool("json", false, "emit the chain as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	ref, err := refFor(c, id)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	chain, err := state.frontier.Revisions(ctx, ref)
	if err != nil {
		return unknownRecord(err, string(ref.Type), id)
	}
	res := revisionsResult{Type: Sanitize(string(ref.Type)), ID: Sanitize(id), Revisions: renderRevisions(chain)}
	if len(res.Revisions) > 0 {
		res.HeadID = res.Revisions[len(res.Revisions)-1].RecordID
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := make([][]string, 0, len(res.Revisions))
	for _, r := range res.Revisions {
		marker := ""
		if r.Head {
			marker = "head"
		}
		rows = append(rows, []string{
			strconv.FormatInt(r.Sequence, 10), r.RecordID, r.Actor, r.RecordedAt, orMissing(r.Reason), marker,
		})
	}
	return writeTable(a.stdout, []string{"SEQ", "RECORD", "ACTOR", "RECORDED", "REASON", ""}, rows)
}

// reviseCmd implements `babel revise ID`.
func (a *app) reviseCmd(ctx context.Context, args []string) error {
	c := newCmd("revise", reviseUsage)
	var of operatorFlags
	of.bind(c)
	statement := c.fs.String("statement", "", "the candidate's new wording")
	reason := c.fs.String("reason", "", "why this revision supersedes its ancestor")
	notes := c.fs.String("notes", "", "investigator notes carried onto the new revision")
	asJSON := c.fs.Bool("json", false, "emit the new revision as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	ref, err := refFor(c, id)
	if err != nil {
		return err
	}
	if ref.Type != frontier.EntityHypothesis {
		return c.usagef("only a candidate is revisable by hand; %s %q carries structured payloads a run assembles",
			ref.Type, id)
	}
	if *statement == "" {
		return c.usagef("a revision states the new wording; pass --statement TEXT")
	}
	if *reason == "" {
		return c.usagef("a revision states why it supersedes its ancestor; pass --reason TEXT")
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	ancestor, err := state.frontier.Hypothesis(ctx, id)
	if err != nil {
		return unknownRecord(err, "hypothesis", id)
	}
	// The new revision inherits the sorting signals and the labels rather
	// than resetting them: §5.2 confines novelty and priority to ordering,
	// and an operator rewording a sentence has not restated where the
	// candidate belongs in the queue.
	payload := ancestor.Payload
	payload.Statement = *statement
	if *notes != "" {
		payload.Notes = *notes
	}
	record, err := state.frontier.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:      ancestor.RunID,
		AncestorID: ancestor.ID,
		Status:     ancestor.Status,
		Actor:      frontier.Operator(operator),
		Reason:     *reason,
		Payload:    payload,
	})
	if err != nil {
		if errors.Is(err, frontier.ErrSuperseded) {
			return err
		}
		return err
	}
	chain, err := state.frontier.Revisions(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID})
	if err != nil {
		return err
	}
	rendered := renderRevisions(chain)
	res := reviseResult{
		Revision:   rendered[len(rendered)-1],
		Supersedes: Sanitize(ancestor.ID),
		Statement:  Sanitize(record.Payload.Statement),
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"revision", res.Revision.ID},
		{"record", res.Revision.RecordID},
		{"supersedes", res.Supersedes},
		{"sequence", strconv.FormatInt(res.Revision.Sequence, 10)},
		{"actor", res.Revision.Actor},
		{"reason", res.Revision.Reason},
		{"statement", res.Statement},
		{"ancestor", "unchanged and still readable at " + res.Supersedes},
	})
}

// reviveCmd implements `babel revive ID`.
func (a *app) reviveCmd(ctx context.Context, args []string) error {
	c := newCmd("revive", reviveUsage)
	var of operatorFlags
	of.bind(c)
	reason := c.fs.String("reason", "", "why the candidate deserves to move again")
	status := c.fs.String("status", "", "where the candidate lands")
	asJSON := c.fs.Bool("json", false, "emit the transition as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	ref, err := refFor(c, id)
	if err != nil {
		return err
	}
	if ref.Type != frontier.EntityHypothesis {
		return c.usagef("only a candidate has a lifecycle status; %s %q has none to revive", ref.Type, id)
	}
	if *reason == "" {
		return c.usagef("a revive states why; pass --reason TEXT")
	}
	landing := frontier.Status("")
	if *status != "" {
		landing, err = parseStatus(c, *status)
		if err != nil {
			return err
		}
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	event, err := state.frontier.Revive(ctx, frontier.ReviveInput{
		HypothesisID: id,
		Status:       landing,
		Actor:        frontier.Operator(operator),
		Reason:       *reason,
	})
	if err != nil {
		return unknownRecord(err, "hypothesis", id)
	}
	res := reviveResult{
		Type:   string(frontier.EntityHypothesis),
		ID:     Sanitize(id),
		Status: renderStatusHistory([]frontier.StatusEvent{event})[0],
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"hypothesis", res.ID},
		{"status", res.Status.Status},
		{"sequence", strconv.FormatInt(res.Status.Sequence, 10)},
		{"actor", res.Status.Actor},
		{"recorded", res.Status.RecordedAt},
		{"reason", res.Status.Note},
	})
}

// inviteCmd implements `babel invite ID`.
func (a *app) inviteCmd(ctx context.Context, args []string) error {
	c := newCmd("invite", inviteUsage)
	var of operatorFlags
	of.bind(c)
	asJSON := c.fs.Bool("json", false, "emit the invitation as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	ref, err := refFor(c, id)
	if err != nil {
		return err
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	invitation, err := state.dispositions.Invite(ctx, disposition.InviteInput{Record: ref, By: operator})
	if err != nil {
		return unknownRecord(err, string(ref.Type), id)
	}
	res := inviteResult{Invitation: renderInvitation(invitation)}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"invitation", res.Invitation.ID},
		{"record", res.Invitation.Type + " " + res.Invitation.RecordID},
		{"by", res.Invitation.By},
		{"created", res.Invitation.CreatedAt},
		{"instruction", "none; what to do with it is the next run's judgement"},
	})
}

// invitationsCmd implements `babel invitations`.
func (a *app) invitationsCmd(ctx context.Context, args []string) error {
	c := newCmd("invitations", invitationsUsage)
	record := c.fs.String("record", "", "narrow to one record's invitations")
	all := c.fs.Bool("all", false, "include invitations a run already consumed")
	limit := c.fs.Int("limit", 0, "bound the listing")
	asJSON := c.fs.Bool("json", false, "emit the queue as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	filter := disposition.InvitationFilter{All: *all, Limit: *limit}
	if *record != "" {
		ref, err := refFor(c, *record)
		if err != nil {
			return err
		}
		filter.Record = ref
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	queue, err := state.dispositions.Invitations(ctx, filter)
	if err != nil {
		return err
	}
	res := invitationsResult{Invitations: make([]invitationRow, 0, len(queue))}
	for _, invitation := range queue {
		res.Invitations = append(res.Invitations, renderInvitation(invitation))
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := make([][]string, 0, len(res.Invitations))
	for _, i := range res.Invitations {
		consumed := "open"
		if !i.Open {
			consumed = i.ConsumedBy
		}
		rows = append(rows, []string{i.ID, i.Type, i.RecordID, i.By, i.CreatedAt, consumed})
	}
	return writeTable(a.stdout, []string{"ID", "TYPE", "RECORD", "BY", "CREATED", "CONSUMED"}, rows)
}

// dispositionsCmd implements `babel dispositions`.
func (a *app) dispositionsCmd(ctx context.Context, args []string) error {
	c := newCmd("dispositions", dispositionsUsage)
	record := c.fs.String("record", "", "narrow to one record's proposed actions")
	kind := c.fs.String("kind", "", "narrow to one kind")
	status := c.fs.String("status", "", "narrow to one state")
	var pf pageFlags
	pf.bind(c)
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	filter := disposition.ListFilter{Limit: pf.limit, Offset: pf.offset}
	if *record != "" {
		ref, err := refFor(c, *record)
		if err != nil {
			return err
		}
		filter.Record = ref
	}
	if *kind != "" {
		parsed, err := parseDispositionKind(c, *kind)
		if err != nil {
			return err
		}
		filter.Kinds = []disposition.Kind{parsed}
	}
	if *status != "" {
		parsed, err := parseDispositionStatus(c, *status)
		if err != nil {
			return err
		}
		filter.Statuses = []disposition.Status{parsed}
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	actions, total, err := state.dispositions.List(ctx, filter)
	if err != nil {
		return err
	}
	res := dispositionsResult{
		Dispositions: make([]dispositionRow, 0, len(actions)),
		Total:        total,
		Limit:        pf.limit,
		Offset:       pf.offset,
	}
	for _, action := range actions {
		res.Dispositions = append(res.Dispositions, renderDisposition(action))
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := make([][]string, 0, len(res.Dispositions))
	for _, d := range res.Dispositions {
		rows = append(rows, []string{d.ID, d.Kind, d.Status, d.Type, d.RecordID, d.Summary})
	}
	if err := writeTable(a.stdout, []string{"ID", "KIND", "STATE", "TYPE", "RECORD", "SUMMARY"}, rows); err != nil {
		return err
	}
	return a.writePageFooter(len(res.Dispositions), res.Offset, res.Total)
}

// dispositionCmd routes `babel disposition <verb>`.
func (a *app) dispositionCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "disposition requires a subcommand", usage: dispositionUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, dispositionUsage)
		return nil
	case "show":
		return a.dispositionShow(ctx, args[1:])
	case "propose":
		return a.dispositionPropose(ctx, args[1:])
	case "accept":
		return a.dispositionDecide(ctx, args[1:], disposition.RulingAccepted)
	case "decline":
		return a.dispositionDecide(ctx, args[1:], disposition.RulingDeclined)
	default:
		return &usageError{msg: fmt.Sprintf("unknown disposition subcommand %q", args[0]), usage: dispositionUsage}
	}
}

func (a *app) dispositionShow(ctx context.Context, args []string) error {
	c := newCmd("disposition show", dispositionShowUsage)
	asJSON := c.fs.Bool("json", false, "emit the action and its ledger as JSON")
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

	action, err := state.dispositions.Disposition(ctx, id)
	if err != nil {
		return unknownDisposition(err, id)
	}
	ledger, err := state.dispositions.Ledger(ctx, id)
	if err != nil {
		return err
	}
	res := dispositionResult{Disposition: renderDisposition(action), Ledger: renderLedger(ledger)}
	if action.Kind == disposition.KindDraftIssue {
		draft, err := disposition.Draft(action)
		if err != nil {
			return err
		}
		res.Draft = sanitizeBlock(draft)
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"id", res.Disposition.ID},
		{"kind", res.Disposition.Kind},
		{"state", res.Disposition.Status},
		{"record", res.Disposition.Type + " " + res.Disposition.RecordID},
		{"proposed by", res.Disposition.ProposedBy},
		{"created", res.Disposition.CreatedAt},
		{"summary", res.Disposition.Summary},
		{"rationale", orMissing(res.Disposition.Rationale)},
	}
	if anchor := res.Disposition.Anchor; anchor != nil {
		rows = append(rows,
			[2]string{"repository", anchor.URL},
			[2]string{"branch", orMissing(anchor.Branch)},
			[2]string{"verified against", anchor.Workspace},
		)
	}
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	fmt.Fprint(a.stdout, "\nledger\n")
	table := make([][]string, 0, len(res.Ledger))
	for _, e := range res.Ledger {
		table = append(table, []string{strconv.FormatInt(e.Sequence, 10), e.Ruling, e.By, e.RecordedAt, orMissing(e.Note)})
	}
	if err := writeTable(a.stdout, []string{"SEQ", "RULING", "BY", "RECORDED", "NOTE"}, table); err != nil {
		return err
	}
	if res.Draft != "" {
		fmt.Fprint(a.stdout, "\ndraft\n")
		fmt.Fprintln(a.stdout, res.Draft)
	}
	return nil
}

func (a *app) dispositionPropose(ctx context.Context, args []string) error {
	c := newCmd("disposition propose", dispositionProposeUsage)
	var of operatorFlags
	of.bind(c)
	kind := c.fs.String("kind", "", "the proposed action's kind")
	summary := c.fs.String("summary", "", "the action in one line")
	rationale := c.fs.String("rationale", "", "why the action fits this record")
	repo := c.fs.String("repo", "", "local checkout a draft-issue binds to")
	asJSON := c.fs.Bool("json", false, "emit the proposed action as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	ref, err := refFor(c, id)
	if err != nil {
		return err
	}
	if *kind == "" {
		return c.usagef("a proposed action states its kind; pass --kind K")
	}
	parsedKind, err := parseDispositionKind(c, *kind)
	if err != nil {
		return err
	}
	if *summary == "" {
		return c.usagef("a proposed action states what it proposes; pass --summary TEXT")
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	payload := disposition.Payload{Summary: *summary, Rationale: *rationale}
	switch {
	case parsedKind == disposition.KindDraftIssue && *repo == "":
		return c.usagef("a draft-issue binds to a verified repository; pass --repo DIR")
	case parsedKind != disposition.KindDraftIssue && *repo != "":
		return c.usagef("a %s action binds to no repository, so --repo has nothing to verify", parsedKind)
	case parsedKind == disposition.KindDraftIssue:
		anchor, err := disposition.VerifyAnchor(*repo)
		if err != nil {
			return err
		}
		payload.Anchor = &anchor
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	action, err := state.dispositions.Propose(ctx, disposition.ProposeInput{
		Record:     ref,
		Kind:       parsedKind,
		ProposedBy: frontier.Operator(operator),
		Payload:    payload,
	})
	if err != nil {
		return unknownRecord(err, string(ref.Type), id)
	}
	row := renderDisposition(action)
	if *asJSON {
		return a.emitJSON(dispositionResult{Disposition: row})
	}
	rows := [][2]string{
		{"id", row.ID},
		{"kind", row.Kind},
		{"state", row.Status},
		{"record", row.Type + " " + row.RecordID},
		{"proposed by", row.ProposedBy},
		{"summary", row.Summary},
	}
	if row.Anchor != nil {
		rows = append(rows, [2]string{"repository", row.Anchor.URL})
	}
	rows = append(rows, [2]string{"acted on", "nothing; a proposed action waits for an operator's decision"})
	return writeDetail(a.stdout, rows)
}

func (a *app) dispositionDecide(ctx context.Context, args []string, ruling disposition.Ruling) error {
	c := newCmd("disposition "+string(ruling), dispositionDecideUsage)
	var of operatorFlags
	of.bind(c)
	note := c.fs.String("note", "", "the operator's own words about the decision")
	asJSON := c.fs.Bool("json", false, "emit the ledger entry as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	id, err := c.oneSelector()
	if err != nil {
		return err
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	entry, err := state.dispositions.Decide(ctx, disposition.DecideInput{
		DispositionID: id,
		Ruling:        ruling,
		By:            operator,
		Note:          *note,
	})
	if err != nil {
		return unknownDisposition(err, id)
	}
	action, err := state.dispositions.Disposition(ctx, id)
	if err != nil {
		return err
	}
	res := decideDispositionResult{
		Entry:     renderLedgerEntry(entry),
		Status:    Sanitize(string(action.Status)),
		Published: "nothing; Babel records that the operator authorized the action and stops (SPEC.md §4.6)",
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"entry", res.Entry.ID},
		{"disposition", Sanitize(id)},
		{"ruling", res.Entry.Ruling},
		{"by", res.Entry.By},
		{"recorded", res.Entry.RecordedAt},
		{"state", res.Status},
		{"published", res.Published},
	})
}

// sanitizeBlock renders a multi-line document Babel composed itself.
//
// Sanitize escapes newlines on purpose: it renders values and never layout, so
// handing it a whole document turns the document into one unreadable line. A
// draft is Babel's own layout wrapped around the model's words, so each line
// goes through the one terminal-safe renderer and the line breaks — which no
// untrusted value contributed, because Sanitize removed any it did — are put
// back by the code that owns them.
func sanitizeBlock(document string) string {
	lines := strings.Split(document, "\n")
	for i, line := range lines {
		lines[i] = Sanitize(line)
	}
	return strings.Join(lines, "\n")
}

func renderRevisions(chain []frontier.Revision) []revisionRow {
	out := make([]revisionRow, 0, len(chain))
	for i, r := range chain {
		out = append(out, revisionRow{
			ID:           Sanitize(r.ID),
			Type:         Sanitize(string(r.Entity.Type)),
			RecordID:     Sanitize(r.Entity.ID),
			RootID:       Sanitize(r.RootID),
			SupersedesID: Sanitize(r.SupersedesID),
			Sequence:     r.Sequence,
			Actor:        renderActor(r.Actor),
			RecordedAt:   formatTime(r.RecordedAt),
			Reason:       Sanitize(r.Payload.Reason),
			Head:         i == len(chain)-1,
		})
	}
	return out
}

func renderInvitation(i disposition.Invitation) invitationRow {
	row := invitationRow{
		ID:         Sanitize(i.ID),
		Type:       Sanitize(string(i.Record.Type)),
		RecordID:   Sanitize(i.Record.ID),
		By:         Sanitize(i.By),
		CreatedAt:  formatTime(i.CreatedAt),
		ConsumedBy: Sanitize(i.ConsumedBy),
		Open:       i.Open(),
	}
	if !i.Open() {
		row.ConsumedAt = formatTime(i.ConsumedAt)
	}
	return row
}

func renderDisposition(d disposition.Disposition) dispositionRow {
	row := dispositionRow{
		ID:         Sanitize(d.ID),
		Type:       Sanitize(string(d.Record.Type)),
		RecordID:   Sanitize(d.Record.ID),
		Kind:       Sanitize(string(d.Kind)),
		Status:     Sanitize(string(d.Status)),
		ProposedBy: renderActor(d.ProposedBy),
		Ref:        Sanitize(d.Ref),
		CreatedAt:  formatTime(d.CreatedAt),
		Summary:    Sanitize(d.Payload.Summary),
		Rationale:  Sanitize(d.Payload.Rationale),
	}
	if a := d.Payload.Anchor; a != nil {
		row.Anchor = &anchorRow{
			Workspace: Sanitize(a.Workspace),
			Remote:    Sanitize(a.Remote),
			URL:       Sanitize(a.URL),
			Branch:    Sanitize(a.Branch),
		}
	}
	return row
}

func renderLedger(entries []disposition.LedgerEntry) []ledgerRow {
	out := make([]ledgerRow, 0, len(entries))
	for _, e := range entries {
		out = append(out, renderLedgerEntry(e))
	}
	return out
}

func renderLedgerEntry(e disposition.LedgerEntry) ledgerRow {
	return ledgerRow{
		ID:         Sanitize(e.ID),
		Sequence:   e.Sequence,
		Ruling:     Sanitize(string(e.Ruling)),
		By:         Sanitize(e.By),
		RecordedAt: formatTime(e.RecordedAt),
		Note:       Sanitize(e.Payload.Note),
	}
}

// parseDispositionKind validates a --kind value against #87's closed
// vocabulary, reading the vocabulary from the package that owns it so the
// message cannot drift from what the store accepts.
func parseDispositionKind(c *cmd, value string) (disposition.Kind, error) {
	kinds := disposition.Kinds()
	for _, kind := range kinds {
		if value == string(kind) {
			return kind, nil
		}
	}
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return "", c.usagef("%q is not a disposition kind; one of %s", value, strings.Join(names, ", "))
}

func parseDispositionStatus(c *cmd, value string) (disposition.Status, error) {
	switch disposition.Status(value) {
	case disposition.StatusProposed:
		return disposition.StatusProposed, nil
	case disposition.StatusAccepted:
		return disposition.StatusAccepted, nil
	case disposition.StatusDeclined:
		return disposition.StatusDeclined, nil
	}
	return "", c.usagef("%q is not a disposition state; one of proposed, accepted, declined", value)
}

// unknownDisposition turns the store's sentinel into a message naming what was
// looked for, matching unknownRecord's treatment of a frontier identifier.
func unknownDisposition(err error, id string) error {
	if errors.Is(err, disposition.ErrUnknownDisposition) {
		return fmt.Errorf("no proposed action %q is recorded", Sanitize(id))
	}
	return err
}
