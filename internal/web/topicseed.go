package web

// Seeding topic proposals from what this host observed (SPEC.md §4.13).
//
// Stage 1 filed records under a name derived from repository identity and
// labelled the filing heuristic, which is exactly what §4.13 permits until the
// triage recipe has run. This is the next step and no further: every identity
// the catalog observed becomes a *proposal* the operator can accept or
// decline, so the topics he ends up with are the ones he named — and the
// heuristic stays visible as a proposal rather than masquerading as a topic.
//
// Nothing here creates an entity, and nothing here files a record. Both are
// the acceptance's work, and the acceptance is the operator's.

import (
	"context"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/reality"
)

// seedActor attributes the questions this seeding raises. It is not a run and
// not the operator: it is this component, deriving identity from the catalog
// alone, which is why the filings its acceptance performs are heuristic.
const seedActor = "topic-seed"

// SeedTopics raises one topic proposal per observed identity the ledger does
// not already bind. It is idempotent: a second pass over an unchanged catalog
// raises nothing, because every identity is then bound, already proposed, or
// declined with no new evidence behind it.
func SeedTopics(ctx context.Context, ledger *reality.Store,
	observed []reality.TopicObservation) (reality.SeedReport, error) {
	if ledger == nil {
		return reality.SeedReport{}, nil
	}
	return ledger.SeedTopics(ctx, observed, reality.Provenance{Actor: seedActor})
}

// TopicObservationsFromSessions states what the session catalog observed as
// the topics it would propose.
//
// It groups by identity rather than by name, which is the difference between
// proposing subjects and rendering a sidebar. Two unrelated checkouts both
// called "babel" are one ambiguous name and two repositories: the feed's
// vocabulary refuses to bind the name, and rightly, while the ledger can hold
// both as entities with their own identifiers and let the operator rename or
// merge them. A session whose repository this host could not observe produces
// nothing, because a locator is not a topic.
//
// It carries no records. The record-to-topic derivation walks the development
// path back to the sessions each record cites and belongs to the corpus the
// server holds; a command line that seeds identities raises the same questions
// with nothing to file, and the triage recipe files them when it runs.
func TopicObservationsFromSessions(rows []SessionRow) []reality.TopicObservation {
	type collected struct {
		name      string
		remote    string
		paths     map[string]struct{}
		sessions  int
		checkouts map[string]struct{}
	}
	byIdentity := map[string]*collected{}
	for _, row := range rows {
		identity := repositoryIdentity(row)
		name := topicName(identity)
		if identity == "" || name == "" {
			continue
		}
		entry, seen := byIdentity[identity]
		if !seen {
			entry = &collected{
				name:      name,
				paths:     map[string]struct{}{},
				checkouts: map[string]struct{}{},
			}
			byIdentity[identity] = entry
		}
		entry.sessions++
		if row.RepositoryRemote != nil {
			if remote := strings.TrimSpace(*row.RepositoryRemote); remote != "" {
				entry.remote = remote
			}
		}
		if row.Workspace != nil {
			if workspace := strings.TrimSpace(*row.Workspace); workspace != "" {
				entry.paths[workspace] = struct{}{}
				entry.checkouts[workspace] = struct{}{}
			}
		}
	}
	out := make([]reality.TopicObservation, 0, len(byIdentity))
	for identity, entry := range byIdentity {
		out = append(out, reality.TopicObservation{
			Identity:  identity,
			Remote:    entry.remote,
			Name:      entry.name,
			Paths:     sortedKeys(entry.paths),
			Sessions:  entry.sessions,
			Checkouts: len(entry.checkouts),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}
