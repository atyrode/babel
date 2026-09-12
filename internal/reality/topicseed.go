package reality

// Seeding topics from repository identity (SPEC.md §4.13).
//
// §4.13 permits exactly this much before the triage recipe has run: a
// deployment may seed from repository identity alone — the one binding
// observable without a model — and must label what it produces as heuristic.
// So this raises *proposals* and never entities: the operator still creates
// every topic he sees, and a seeding that created subjects would be the rule
// §4.8 exists to enforce, broken by a background job.
//
// It is idempotent by construction rather than by a "seeded" marker. An
// identity that already binds a live entity is skipped, one already proposed
// is the same question and deduplicates, and one the operator declined stays
// declined until more sessions stand behind it than did when he refused. Two
// runs of the seeder over an unchanged catalog therefore raise nothing the
// first did not, and nothing here has to remember that it ran.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/atyrode/babel/internal/frontier"
)

// TopicObservation is one repository identity the session catalog observed,
// with what stands behind it.
//
// It is the catalog's vocabulary rather than the ledger's on purpose: this
// package cannot see a session row — internal/web and internal/cli own those,
// and the ledger must not import the corpus — so the caller states what it
// observed and this decides what to propose.
type TopicObservation struct {
	// Identity is the repository's own identity: the normalized remote when
	// there is one, and the common directory every worktree shares when
	// there is not.
	Identity string
	// Remote is the normalized remote, empty for a checkout with no origin.
	// It is separate from Identity because a repository with a remote is
	// bound by it *and* lives in directories, and the binding facts differ.
	Remote string
	// Name is what the operator calls it.
	Name string
	// Kind is the entity kind to propose. Empty means repository, which is
	// the only kind this deployment can observe without a model — and
	// §4.13 forbids assuming it anywhere else.
	Kind EntityKind
	// Paths are the workspaces that resolved to this identity. They are
	// evidence and become typed aliases, never the topic itself.
	Paths []string
	// Sessions and Checkouts are what the proposal says stands behind it.
	Sessions  int
	Checkouts int
	// Records are the frontier records the deployment would file under the
	// topic once it exists.
	Records []frontier.Ref
}

// SeedOutcome is why one observation did or did not become a proposal.
type SeedOutcome string

// The seeding outcomes.
const (
	// SeedRaised is a new topic question.
	SeedRaised SeedOutcome = "raised"
	// SeedBound is an identity the ledger already binds to a live entity.
	// It is the ordinary steady state, not a failure.
	SeedBound SeedOutcome = "bound"
	// SeedProposed is an identity already waiting in the inbox.
	SeedProposed SeedOutcome = "proposed"
	// SeedDeclined is an identity the operator refused, with no more
	// evidence behind it than there was when he refused.
	SeedDeclined SeedOutcome = "declined"
	// SeedAmbiguous is a name that already resolves to several entities.
	// Proposing a third subject is not the answer to an ambiguity §4.8
	// wants a human to resolve.
	SeedAmbiguous SeedOutcome = "ambiguous"
	// SeedRefused is an observation the ledger would not hold at all: its
	// identity or its binding carries credential-shaped material, which
	// §4.8 forbids outright. It is reported rather than skipped silently,
	// because a topic missing from the inbox with no reason is a topic
	// nobody can look for.
	SeedRefused SeedOutcome = "refused"
)

// SeedResult is what became of one observation.
type SeedResult struct {
	Identity   string
	Name       string
	Outcome    SeedOutcome
	QuestionID string
	// EntityID is the entity that already binds the identity, for a bound
	// outcome.
	EntityID string
	// Detail is the refusal in the ledger's words, for an outcome a reader
	// would otherwise have to guess the cause of.
	Detail string
	// Withheld counts locators the proposal did not carry because the
	// credential detector refuses them. The topic is the repository and a
	// path is only evidence for it, so a workspace under a high-entropy
	// directory costs the proposal that locator rather than costing the
	// operator the topic.
	Withheld int
}

// SeedReport is one seeding pass.
type SeedReport struct {
	Results []SeedResult
}

// Raised counts the proposals this pass added to the inbox.
func (r SeedReport) Raised() int { return r.count(SeedRaised) }

// Skipped counts the observations that produced nothing, which is what an
// idempotent second pass reports for everything.
func (r SeedReport) Skipped() int { return len(r.Results) - r.Raised() }

func (r SeedReport) count(outcome SeedOutcome) int {
	n := 0
	for _, result := range r.Results {
		if result.Outcome == outcome {
			n++
		}
	}
	return n
}

// SeedTopics raises one topic proposal per unbound repository identity.
//
// The binding it proposes is the repository's own: the remote as a
// repository-remote fact when there is one, the common directory as a
// local-path fact when there is not, and every observed workspace as a typed
// path alias. The paths are aliases rather than facts deliberately — a
// repository with three worktrees has three locators and one local path in the
// ledger's sense, and three local-path facts about one subject would be three
// claims that contradict one another (§4.8's dispute), which is not what a
// worktree is.
func (s *Store) SeedTopics(ctx context.Context, observed []TopicObservation,
	by Provenance) (SeedReport, error) {
	sorted := make([]TopicObservation, 0, len(observed))
	sorted = append(sorted, observed...)
	// Sorted so two passes over one catalog raise proposals in one order,
	// and a report is comparable with the last one.
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Identity < sorted[j].Identity })

	var report SeedReport
	for _, observation := range sorted {
		if observation.Identity == "" || observation.Name == "" {
			// A session whose repository this host could not observe
			// is not a topic with a missing name; it is a session
			// about something nobody has said yet, and §4.13 leaves
			// it unfiled rather than inventing a name for it.
			continue
		}
		result := SeedResult{Identity: observation.Identity, Name: observation.Name}
		proposal, withheld := observation.proposal()
		result.Withheld = withheld
		question, err := s.AskTopic(ctx, proposal, by)
		switch {
		case err == nil:
			result.Outcome, result.QuestionID = SeedRaised, question.ID
		case errors.Is(err, ErrTopicBound):
			result.Outcome = SeedBound
			result.Detail = err.Error()
			if bound, boundErr := s.EntityBoundTo(ctx, observation.Identity); boundErr == nil {
				result.EntityID = bound
			}
		case errors.Is(err, ErrDuplicateQuestion):
			result.Outcome, result.Detail = SeedProposed, err.Error()
		case errors.Is(err, ErrSuppressed):
			result.Outcome, result.Detail = SeedDeclined, err.Error()
		case errors.Is(err, ErrAmbiguousAlias):
			result.Outcome, result.Detail = SeedAmbiguous, err.Error()
		case errors.Is(err, ErrCredentialMaterial):
			result.Outcome, result.Detail = SeedRefused, err.Error()
		default:
			return report, fmt.Errorf("reality: seed topic %s: %w", observation.Name, err)
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}

// proposal states one observation as the entity it would create, and reports
// how many locators it had to leave out.
//
// A workspace path can be credential-shaped — a checkout under a high-entropy
// temporary directory is the ordinary case — and §4.8 forbids the ledger to
// hold such a value at all. Dropping the locator is the right refusal to make
// here rather than dropping the topic: §4.13 is explicit that the workspace is
// evidence and the repository is the subject, so a proposal that loses one
// path is still a proposal about the right thing, while a repository the
// operator never gets to name because one of its worktrees sits under
// /tmp/nix-shell.XXXX is a topic lost to a detector.
func (in TopicObservation) proposal() (TopicProposal, int) {
	kind := in.Kind
	if kind == "" {
		kind = EntityRepository
	}
	withheld := 0
	aliases := make([]AliasInput, 0, len(in.Paths)+1)
	if in.Remote != "" {
		aliases = append(aliases, AliasInput{
			Kind:    AliasRepository,
			Payload: AliasPayload{Value: in.Remote},
		})
	}
	for _, path := range in.Paths {
		if checkNoCredential("workspace", path) != nil {
			withheld++
			continue
		}
		aliases = append(aliases, AliasInput{
			Kind:    AliasPath,
			Payload: AliasPayload{Value: path, Note: "a workspace this repository was seen at"},
		})
	}
	var binding []FactInput
	if in.Remote != "" {
		binding = append(binding, FactInput{
			Predicate: PredicateRepositoryRemote,
			Value:     FactValue{Kind: ValueText, Text: in.Remote},
			Note:      "the repository's published identity, observed from the checkout's origin",
		})
	} else {
		binding = append(binding, FactInput{
			Predicate: PredicateLocalPath,
			Value:     FactValue{Kind: ValueText, Text: in.Identity},
			Note:      "the common directory every worktree of this repository shares",
		})
	}
	return TopicProposal{
		Name:      in.Name,
		Kind:      kind,
		Aliases:   aliases,
		Binding:   binding,
		Reasoning: in.reasoning(),
		Records:   in.Records,
		Identity:  in.Identity,
		Sessions:  in.Sessions,
	}, withheld
}

// reasoning is what the proposal says for itself. It counts rather than
// argues, because counting is all a heuristic derived from repository identity
// honestly did: the sessions are the evidence, and the operator is the one who
// decides whether the thing behind them is worth naming.
func (in TopicObservation) reasoning() string {
	sessions, cite := "sessions", "cite"
	if in.Sessions == 1 {
		sessions, cite = "session", "cites"
	}
	if in.Checkouts <= 1 {
		return fmt.Sprintf("%d %s %s this repository", in.Sessions, sessions, cite)
	}
	return fmt.Sprintf("%d %s in %d checkouts %s this repository",
		in.Sessions, sessions, in.Checkouts, cite)
}
