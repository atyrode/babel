package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
)

// This file is `babel tell`: the one command where the operator speaks first
// (issue #115).
//
// Every other Phase B verb answers something Babel produced - revise this
// candidate, decide this record, accept this plan. This one takes an
// unprompted sentence about what is going badly and makes it a record, which is
// what turns an annoyance nobody wrote down into material every later
// preparation retrieves.
//
// The verb is `tell` rather than `complain` by operator decision (2026-08-31).
// It is deliberately softer and broader: a complaint, a wish and a hunch are all
// the same shape of input - the operator saying something Babel did not ask
// for - and a verb that only welcomed grievances would leave the other two with
// nowhere to go.
//
// What it does NOT do is the load-bearing part. It opens nothing, assigns
// nothing, schedules nothing and reports no state, because a complaint is not a
// work item: GitHub holds work items and Babel holds steering pressure. The
// command's whole output is the record it wrote and what Babel already has
// touching it.

const tellUsage = `Usage: babel tell [TEXT] [flags]

Tell Babel something it did not ask about: a complaint, a wish, or a hunch.
The text is a positional argument, or the whole of stdin when none is given.

Flags:
  --operator ID   operator identity this is attributed to, else $BABEL_OPERATOR
  --about ID      an analysis record this is about; repeatable
  --amend ID      restate a complaint already told, keeping the earlier wording
  --json          emit the record and its adjacency as JSON

What Babel does with it: the complaint becomes a durable record, joins the
retrieval index, and is surfaced as context by later preparations. Records that
answer it cite it, so "was this addressed?" is a citation query on its page.

What Babel does not do with it: nothing is opened, assigned, scheduled, or
closed. A complaint has no state, and an unaddressed one stays readable.
`

// maxTellAdjacency bounds the adjacent prior material one capture reports.
//
// It is a screenful rather than a page. The list answers "does Babel already
// have something touching this", which a reader settles from the first few
// entries; a longer one would turn the answer into a search result the operator
// has to work through, at the moment they were trying to say one thing and
// move on.
const maxTellAdjacency = 8

// tellResult is one capture, machine-readable.
//
// There is no status field, no closure field and no acknowledgement field, and
// their absence is the contract rather than an omission (#115). A consumer that
// wanted to know whether a complaint had been addressed reads the record's
// citations, which is a question about work that happened rather than about a
// flag somebody set.
type tellResult struct {
	ID       string `json:"id"`
	RootID   string `json:"root_id"`
	Sequence int    `json:"sequence"`
	// Supersedes is the wording this one amends, absent for a first telling.
	Supersedes string `json:"supersedes,omitempty"`
	By         string `json:"by"`
	Host       string `json:"host"`
	Text       string `json:"text"`
	// Redacted reports that capture replaced secret-shaped material with stable
	// placeholders, so a reader does not attribute Babel's redaction to the
	// operator.
	Redacted bool   `json:"redacted"`
	At       string `json:"at"`
	// About is what the operator said the complaint is about, echoed back
	// because an edge that was refused is not in this list and an operator who
	// asked for one is entitled to notice.
	About []string `json:"about,omitempty"`
	// Adjacent is the capture-time retrieval pass: prior outputs and complaints
	// whose words touch this one. It is a prompt to compare, never a claim that
	// any of them says the same thing.
	Adjacent []tellAdjacentRow `json:"adjacent"`
	// AdjacencyNote states why the pass produced nothing, when the reason is
	// something other than "the index holds nothing like this".
	AdjacencyNote string `json:"adjacency_note,omitempty"`
}

// tellAdjacentRow is one piece of prior material the capture-time pass found.
// It carries no score, on the package-wide rule that retrieval rank is never
// evidence strength (§5.4).
type tellAdjacentRow struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// tellCmd implements `babel tell`.
func (a *app) tellCmd(ctx context.Context, args []string) error {
	c := newCmd("tell", tellUsage)
	var of operatorFlags
	of.bind(c)
	var about repeatedFlag
	c.fs.Var(&about, "about", "an analysis record this is about; repeatable")
	amend := c.fs.String("amend", "", "restate a complaint already told")
	asJSON := c.fs.Bool("json", false, "emit the record and its adjacency as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	text, err := a.tellBody(c)
	if err != nil {
		return err
	}
	operator, err := of.resolve(c)
	if err != nil {
		return err
	}
	host, err := localHostID()
	if err != nil {
		return err
	}
	targets, err := tellAbout(c, about)
	if err != nil {
		return err
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()
	// A refused edge is a warning here rather than a failure, which is the
	// store's contract and the right one: the operator's sentence is durable,
	// and telling them their complaint failed because a citation would not bind
	// is how an operator stops telling Babel things.
	state.diag = func(err error) {
		a.diagf("warning: %s\n", Sanitize(err.Error()))
	}

	var told complaint.Complaint
	if *amend != "" {
		told, err = state.complaints.Amend(ctx, complaint.AmendInput{
			ComplaintID: *amend, Text: text, By: operator, Host: host, Addresses: targets,
		})
	} else {
		told, err = state.complaints.Tell(ctx, complaint.TellInput{
			Text: text, By: operator, Host: host, Addresses: targets,
		})
	}
	if err != nil {
		if errors.Is(err, complaint.ErrUnknownComplaint) {
			return fmt.Errorf("no complaint %q was told on this machine", Sanitize(*amend))
		}
		return err
	}

	adjacent, note := a.tellAdjacency(ctx, d.indexDir(), state, told)
	res := tellResult{
		ID:            Sanitize(told.ID),
		RootID:        Sanitize(told.RootID),
		Sequence:      told.Sequence,
		Supersedes:    Sanitize(told.AncestorID),
		By:            Sanitize(told.By),
		Host:          Sanitize(told.Host),
		Text:          sanitizeProse(told.Text),
		Redacted:      told.Redacted,
		At:            formatTime(told.CreatedAt),
		About:         tellAboutIDs(targets),
		Adjacent:      adjacent,
		AdjacencyNote: note,
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.writeTell(res)
}

// tellBody reads the complaint's text from the argument or from stdin.
//
// A positional argument is the one-liner case and stdin is the paragraph case,
// and both are supported because they are the two ways an annoyed person
// actually writes: `babel tell "the rules keep getting ignored"` at the moment
// it happens, and a heredoc when there is more to say than fits a shell line.
//
// Stdin is read only when no argument was given, and a terminal is refused
// rather than read: a command that silently waited for EOF would look like a
// hang, and the operator would kill it and lose what they were going to say.
func (a *app) tellBody(c *cmd) (string, error) {
	switch args := c.args(); len(args) {
	case 0:
		in, isFile := a.stdin.(*os.File)
		if isFile && isTerminal(in) {
			return "", c.usagef("no text and nothing on stdin: pass the complaint as an argument, or pipe it in")
		}
		body, err := io.ReadAll(io.LimitReader(a.stdin, complaint.MaxTextBytes+1))
		if err != nil {
			return "", fmt.Errorf("read the complaint from stdin: %w", err)
		}
		return string(body), nil
	case 1:
		return args[0], nil
	default:
		// Joining them would be Babel guessing that the shell split one
		// sentence, and it is equally likely the operator forgot the quotes
		// around the first of two. Saying so costs one retry and no words.
		return "", c.usagef("a complaint is one argument: quote the whole text, or pipe it on stdin")
	}
}

// tellAbout resolves the --about identifiers into the records an edge may name.
//
// The vocabulary is the analysis records identifiers already address, resolved
// from the identifier's own prefix rather than from a flag, on refFor's
// reasoning: the operator pastes an id a listing printed, and no mistyped kind
// can make the edge point at the wrong table.
func tellAbout(c *cmd, values []string) ([]frontier.Ref, error) {
	refs := make([]frontier.Ref, 0, len(values))
	for _, value := range values {
		ref, err := refFor(c, value)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func tellAboutIDs(refs []frontier.Ref) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, Sanitize(ref.ID))
	}
	return out
}

// sanitizeProse renders a complaint's text: every line through Sanitize, joined
// back with the newlines the operator typed.
//
// Sanitize escapes newlines because it renders values and never layout, and its
// own instruction is that callers compose lines from sanitized values rather
// than sanitizing composed lines. A complaint is the one value in this package
// that is deliberately multi-line prose - somebody wrote a paragraph about what
// is going wrong - so composing it that way is what the rule asks for, and
// handing the whole thing to Sanitize would render their paragraph as one line
// of "\u{A}" escapes.
//
// Nothing else is kept. A newline is layout an operator supplied on purpose; an
// ESC, a bidi override or an invisible character in the same text is not, and
// each is still escaped exactly as it would be anywhere else.
func sanitizeProse(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = Sanitize(line)
	}
	return strings.Join(lines, "\n")
}

// tellAdjacency runs the capture-time retrieval pass: what does Babel already
// hold that touches what was just said.
//
// It is spend-free from end to end and could not be otherwise - this runs inside
// `babel tell`, which sends nothing anywhere. The index is reconciled first so
// the answer is about the deployment as it stands rather than as of whenever a
// preparation last ran, and the complaint just told is included in that
// reconcile so the next capture can find it.
//
// Every failure here is reported and none of them fails the command. The
// complaint is already durable; an operator who was told their sentence failed
// because a rebuildable cache would not open has been told something untrue
// about the only thing they cared about.
//
// The complaint's own chain is excluded from its own adjacency. Matching
// yourself is not prior material, and an amendment listing the wording it
// replaced would read as Babel having already said what the operator just did.
func (a *app) tellAdjacency(ctx context.Context, dir string, state *analysisState,
	told complaint.Complaint) ([]tellAdjacentRow, string) {
	idx, err := index.Open(dir)
	if err != nil {
		return nil, "the retrieval index would not open, so nothing adjacent could be looked up: " +
			Sanitize(err.Error())
	}
	defer idx.Close()

	outputs, err := state.frontier.Outputs(ctx)
	if err != nil {
		return nil, "the frontier would not be read, so nothing adjacent could be looked up: " +
			Sanitize(err.Error())
	}
	outputs, err = complaint.Append(ctx, state.complaints, outputs)
	if err != nil {
		return nil, "the complaints would not be read, so nothing adjacent could be looked up: " +
			Sanitize(err.Error())
	}
	if _, err := idx.IndexFrontier(ctx, outputs); err != nil {
		return nil, "the retrieval index would not reconcile, so nothing adjacent could be looked up: " +
			Sanitize(err.Error())
	}

	// One extra row of headroom: the complaint's own chain is filtered out
	// below, and asking for exactly the ceiling would let a self-match cost the
	// operator one real neighbour.
	hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{
		Match: index.MatchExpression(told.Text),
		Order: index.OrderRelevance,
		Limit: maxTellAdjacency + 1,
	})
	if err != nil {
		// An unsearchable complaint is not a failed capture. The operator wrote
		// something the tokenizer has no term for - punctuation, a single stop
		// word - which is a fact about the text rather than about the index.
		if errors.Is(err, index.ErrNoSearchableTerm) || errors.Is(err, index.ErrMatchTooLong) {
			return nil, ""
		}
		return nil, "the retrieval index would not answer, so nothing adjacent could be looked up: " +
			Sanitize(err.Error())
	}
	rows := make([]tellAdjacentRow, 0, len(hits))
	for _, hit := range hits {
		if hit.RootID == told.RootID {
			continue
		}
		if len(rows) == maxTellAdjacency {
			break
		}
		rows = append(rows, tellAdjacentRow{
			Kind:    Sanitize(string(hit.Kind)),
			ID:      Sanitize(hit.ID),
			Summary: Sanitize(hit.Summary),
		})
	}
	return rows, ""
}

// writeTell renders one capture for a terminal.
func (a *app) writeTell(res tellResult) error {
	rows := [][2]string{
		{"complaint", res.ID},
		{"told by", res.By},
		{"on host", res.Host},
		{"at", res.At},
	}
	if res.Supersedes != "" {
		rows = append(rows,
			[2]string{"amends", res.Supersedes},
			[2]string{"wording", fmt.Sprintf("%d of this complaint", res.Sequence)})
	}
	if len(res.About) != 0 {
		rows = append(rows, [2]string{"about", strings.Join(res.About, " ")})
	}
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	if res.Redacted {
		fmt.Fprintf(a.stdout, "\nSecret-shaped material was replaced with placeholders before "+
			"this was stored; the record is not word-for-word what you typed.\n")
	}
	fmt.Fprintf(a.stdout, "\n%s\n", res.Text)

	fmt.Fprintf(a.stdout, "\nWhat Babel already has touching this:\n")
	switch {
	case res.AdjacencyNote != "":
		fmt.Fprintf(a.stdout, "  unknown: %s\n", res.AdjacencyNote)
	case len(res.Adjacent) == 0:
		fmt.Fprintf(a.stdout, "  nothing yet. Your complaint is the first thing here about this.\n")
	default:
		table := make([][]string, 0, len(res.Adjacent))
		for _, row := range res.Adjacent {
			table = append(table, []string{row.Kind, row.ID, row.Summary})
		}
		if err := writeTable(a.stdout, []string{"KIND", "ID", "SUMMARY"}, table); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "\nAdjacency is a prompt to compare, never a claim that any of these "+
			"says what you just said.\n")
	}
	return nil
}
