package cli

// `babel topics` (SPEC.md §4.13).
//
// A topic is what a record is about, and it is a Reality Ledger entity: this
// command lists the ones the operator accepted and the plans Babel has
// published and is waiting on a ruling for. It creates nothing, and it
// proposes nothing either — `babel topics seed` is gone, because §4.13's
// second reading gives every topic change to a proposal a run published and a
// ruling the operator gave, and a command that minted proposals from a
// directory scan was Babel's naming decided by a heuristic nobody reviewed.
//
// What the scan produces now is evidence: the filing recipe reads the
// identities this host observes and the ledger does not name, and proposes
// from them with its own reasoning attached.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const topicsUsage = `Usage: babel topics [flags]

Lists the topics records are filed under: the entities the operator accepted,
with what each is bound to and his stance toward it, and the topic proposals
Babel has published that are still waiting on a ruling.

A topic is a Reality Ledger entity and nothing else (SPEC.md §4.13), and
everything about one goes through Babel: a new topic, a split, a merge and a
retirement are proposals a run publishes, and accepting the proposal is what
applies it. Rule on them with "babel review" or from the feed.

Flags:
  --json   emit the listing as JSON on stdout
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
	Proposal string   `json:"proposal_id,omitempty"`
}

// topicProposalRow is one plan awaiting a ruling. It carries the proposal
// record's identifier because that is what ruling on it takes.
type topicProposalRow struct {
	ProposalID string   `json:"proposal_id"`
	Operation  string   `json:"operation"`
	Name       string   `json:"name,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Targets    []string `json:"targets,omitempty"`
	Identity   string   `json:"identity,omitempty"`
	Sessions   int      `json:"sessions"`
	Records    int      `json:"records"`
	RunID      string   `json:"run_id,omitempty"`
	Why        string   `json:"why"`
}

type topicsResult struct {
	Topics   []topicRow         `json:"topics"`
	Proposed []topicProposalRow `json:"proposed"`
}

// topicsList lists the topics and the plans awaiting a ruling. It is the
// whole of `babel topics`: the command had one subcommand, `seed`, and
// §4.13's second reading removed it, so there is nothing left to route —
// which is why this is a leaf rather than a router with one case, and why
// -h is the flag parser's own.
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
	plans, err := store.OpenTopicPlans(ctx)
	if err != nil {
		return err
	}
	res := topicsResult{
		Topics:   make([]topicRow, 0, len(topics)),
		Proposed: make([]topicProposalRow, 0, len(plans)),
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
			Proposal: topic.ProposalID,
		})
	}
	for _, plan := range plans {
		res.Proposed = append(res.Proposed, topicProposalRow{
			ProposalID: plan.ProposalID,
			Operation:  string(plan.Operation),
			Name:       Sanitize(plan.Name()),
			Kind:       string(plan.Kind()),
			Targets:    plan.Targets,
			Identity:   Sanitize(plan.Identity),
			Sessions:   plan.Sessions,
			Records:    len(plan.Filings),
			RunID:      plan.By.RunID,
			Why:        Sanitize(plan.Reasoning),
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
	// The proposals are listed rather than counted, because the proposal
	// record's identifier is what ruling on one takes.
	fmt.Fprint(a.stdout, "\nproposed\n")
	rows := make([][]string, 0, len(res.Proposed))
	for _, proposal := range res.Proposed {
		rows = append(rows, []string{
			proposal.ProposalID, proposal.Operation,
			orDash(firstNonEmpty(proposal.Name, strings.Join(proposal.Targets, ", "))),
			strconv.Itoa(proposal.Sessions), proposal.Why,
		})
	}
	return writeTable(a.stdout, []string{"PROPOSAL", "DOES", "TOPIC", "SESSIONS", "WHY"}, rows)
}

// orDash renders an absent value as the column's dash rather than as an empty
// cell, so a reader can tell "nothing here" from a rendering accident.
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
