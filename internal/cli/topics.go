package cli

// `babel topics` and `babel topics seed` (SPEC.md §4.13).
//
// A topic is what a record is about, and it is a Reality Ledger entity: this
// command lists the ones the operator accepted and proposes the ones the
// catalog can observe. It creates nothing. Seeding raises questions, the
// operator answers them from here or from the topic page, and both routes
// reach the same acts — which is what keeps the browser from being the only
// place Babel can be told something.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/web"
)

const topicsUsage = `Usage: babel topics [seed] [flags]

Lists the topics records are filed under: the entities the operator accepted,
with what each is bound to and his stance toward it, and the proposals still
waiting in the Reality Inbox.

Commands:
  (none)   list accepted topics and open proposals
  seed     propose one topic per repository identity this host observed

A topic is a Reality Ledger entity and nothing else (SPEC.md §4.13), so
nothing here creates one: seeding raises topic questions, and accepting one
is an attributed operator act performed with "babel reality" or from the
topic page.

Flags:
  --json   emit the listing as JSON on stdout
`

const topicsSeedUsage = `Usage: babel topics seed [flags]

Raises one topic question per repository identity this host observed that the
ledger does not already bind to an entity. The binding is the repository's
own: its remote when it has one, and the common directory every worktree
shares when it has not.

It is idempotent. An identity already bound to an entity is skipped, one
already proposed deduplicates against the open question, and one the operator
declined stays declined until more sessions stand behind it than there were
when he refused it.

The proposals it raises name no records: the record-to-topic derivation walks
the development path back to the sessions each record cites, which the web
index holds and a command line does not. Accepting a proposal seeded here
creates the topic; the filing recipe files the records into it.

Flags:
  --harness H   narrow the scan to one harness
  --root DIR    scan this directory instead of the harness defaults
  --json        emit the pass as JSON on stdout
`

// topicRow is one accepted topic as the terminal and --json show it.
type topicRow struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Identity string   `json:"identity,omitempty"`
	Remote   string   `json:"remote,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Interest string   `json:"interest,omitempty"`
	Reason   string   `json:"interest_reason,omitempty"`
	Question string   `json:"question_id,omitempty"`
}

// topicProposalRow is one open proposal. It carries the question identifier
// because that is what accepting or declining it takes.
type topicProposalRow struct {
	QuestionID string   `json:"question_id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Identity   string   `json:"identity"`
	Sessions   int      `json:"sessions"`
	Records    int      `json:"records"`
	Paths      []string `json:"paths,omitempty"`
	Why        string   `json:"why"`
}

type topicsResult struct {
	Topics   []topicRow         `json:"topics"`
	Proposed []topicProposalRow `json:"proposed"`
}

// seedRow is what became of one observed identity.
type seedRow struct {
	Name       string `json:"name"`
	Identity   string `json:"identity"`
	Outcome    string `json:"outcome"`
	QuestionID string `json:"question_id,omitempty"`
	EntityID   string `json:"entity_id,omitempty"`
	// Withheld counts the workspaces the proposal could not carry because
	// the ledger refuses credential-shaped material. The topic is still
	// proposed: a path is evidence about a repository, not the repository.
	Withheld int `json:"withheld,omitempty"`
}

type seedResult struct {
	Observed int       `json:"observed"`
	Raised   int       `json:"raised"`
	Skipped  int       `json:"skipped"`
	Results  []seedRow `json:"results"`
}

// topicsCmd routes `babel topics [seed]`.
func (a *app) topicsCmd(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprint(a.stdout, topicsUsage)
			return nil
		case "seed":
			return a.topicsSeed(ctx, args[1:])
		}
	}
	return a.topicsList(ctx, args)
}

func (a *app) topicsList(ctx context.Context, args []string) error {
	c := newCmd("topics", topicsUsage)
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
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

	topics, err := store.Topics(ctx)
	if err != nil {
		return err
	}
	proposals, err := store.TopicProposals(ctx)
	if err != nil {
		return err
	}
	res := topicsResult{
		Topics:   make([]topicRow, 0, len(topics)),
		Proposed: make([]topicProposalRow, 0, len(proposals)),
	}
	for _, topic := range topics {
		res.Topics = append(res.Topics, topicRow{
			ID:       topic.Entity.ID,
			Name:     Sanitize(topic.Entity.Payload.DisplayName),
			Kind:     string(topic.Entity.Kind),
			Identity: Sanitize(topic.Binding.Identity),
			Remote:   Sanitize(topic.Binding.Remote),
			Paths:    sanitizeAll(topic.Binding.Paths),
			Interest: topic.Interest.State,
			Reason:   Sanitize(topic.Interest.Reason),
			Question: topic.QuestionID,
		})
	}
	for _, proposal := range proposals {
		res.Proposed = append(res.Proposed, topicProposalRow{
			QuestionID: proposal.Question.ID,
			Name:       Sanitize(proposal.Proposal.Name),
			Kind:       string(proposal.Proposal.Kind),
			Identity:   Sanitize(proposal.Proposal.Identity),
			Sessions:   proposal.Proposal.Sessions,
			Records:    len(proposal.Proposal.Records),
			Paths:      proposalPaths(proposal.Proposal),
			Why:        Sanitize(proposal.Proposal.Reasoning),
		})
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(res.Topics) == 0 {
		fmt.Fprint(a.stdout, "no topics\n")
	} else {
		rows := make([][]string, 0, len(res.Topics))
		for _, topic := range res.Topics {
			rows = append(rows, []string{
				topic.ID, topic.Name, topic.Kind,
				orDash(topic.Interest), orDash(topic.Identity),
			})
		}
		if err := writeTable(a.stdout,
			[]string{"TOPIC", "NAME", "KIND", "INTEREST", "BOUND TO"}, rows); err != nil {
			return err
		}
	}
	if len(res.Proposed) == 0 {
		return nil
	}
	// The proposals are listed rather than counted, because the question
	// identifier is what accepting or declining one takes.
	fmt.Fprint(a.stdout, "\nproposed\n")
	rows := make([][]string, 0, len(res.Proposed))
	for _, proposal := range res.Proposed {
		rows = append(rows, []string{
			proposal.QuestionID, proposal.Name, proposal.Kind,
			strconv.Itoa(proposal.Sessions), proposal.Why,
		})
	}
	return writeTable(a.stdout, []string{"QUESTION", "NAME", "KIND", "SESSIONS", "WHY"}, rows)
}

// proposalPaths states the workspaces a proposal offers as evidence. They are
// the path aliases it would attach, which is where §4.13 puts a locator: as
// evidence about the topic, never as the topic.
func proposalPaths(proposal reality.TopicProposal) []string {
	out := make([]string, 0, len(proposal.Aliases))
	for _, alias := range proposal.Aliases {
		if alias.Kind == reality.AliasPath {
			out = append(out, Sanitize(alias.Payload.Value))
		}
	}
	return out
}

func (a *app) topicsSeed(ctx context.Context, args []string) error {
	c := newCmd("topics seed", topicsSeedUsage)
	var sf scanFlags
	sf.bindHarness(c)
	sf.bindRoots(c)
	asJSON := c.fs.Bool("json", false, "emit the pass as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	ads, err := sf.selected(c)
	if err != nil {
		return err
	}
	dirs, err := babelDirs()
	if err != nil {
		return err
	}

	// The listing is the cached one `babel sessions list` serves: a warm
	// catalog answers from its rows and only an undescribed session costs a
	// read, because seeding must not be a reason to re-describe a corpus.
	sessions, covered := a.scanCorpus(ctx, ads, sf.rootList(), false)
	rows, err := a.listSessionRows(ctx, sessions, refreshScope(covered, sf.rootList()),
		dirs.data, false, describe, a.scanProgress().report)
	if err != nil {
		return err
	}

	store, err := openReality()
	if err != nil {
		return err
	}
	defer store.Close()

	observed := web.TopicObservationsFromSessions(webSessionRows(rows))
	report, err := web.SeedTopics(ctx, store, observed)
	if err != nil {
		return err
	}
	res := seedResult{
		Observed: len(observed),
		Raised:   report.Raised(),
		Skipped:  report.Skipped(),
		Results:  make([]seedRow, 0, len(report.Results)),
	}
	for _, result := range report.Results {
		res.Results = append(res.Results, seedRow{
			Name:       Sanitize(result.Name),
			Identity:   Sanitize(result.Identity),
			Outcome:    string(result.Outcome),
			QuestionID: result.QuestionID,
			EntityID:   result.EntityID,
			Withheld:   result.Withheld,
		})
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if err := writeDetail(a.stdout, [][2]string{
		{"observed", strconv.Itoa(res.Observed) + " " + plural(res.Observed, "identity", "identities")},
		{"raised", strconv.Itoa(res.Raised)},
		{"skipped", strconv.Itoa(res.Skipped)},
	}); err != nil {
		return err
	}
	if len(res.Results) == 0 {
		return nil
	}
	fmt.Fprint(a.stdout, "\n")
	table := make([][]string, 0, len(res.Results))
	for _, result := range res.Results {
		table = append(table, []string{
			result.Name, result.Outcome,
			orDash(firstNonEmpty(result.QuestionID, result.EntityID)),
			result.Identity,
		})
	}
	return writeTable(a.stdout, []string{"NAME", "OUTCOME", "RECORD", "IDENTITY"}, table)
}

// orDash renders an absent value as the column's dash rather than as an empty
// cell, so a reader can tell "nothing here" from a rendering accident.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
