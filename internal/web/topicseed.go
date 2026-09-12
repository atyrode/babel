package web

// What this host observed about repositories, as evidence (SPEC.md §4.13).
//
// Stage 1 filed records under a name derived from repository identity and
// labelled the filing heuristic; stage 2 turned each identity into a topic
// question. Neither is what happens now. §4.13's second reading is explicit:
// everything about a topic goes through Babel, and a proposal is a run's
// output rather than a row a scan minted — so what a scan of the session
// catalog produces is *evidence*, handed to the filing run, and nothing here
// proposes, creates or files anything.
//
// The derivation survives because it is the one binding observable without a
// model: a repository's own identity, which every worktree of it shares. What
// the run does with it is the run's judgement.

import (
	"context"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// TopicObservationsFromSessions states what the session catalog observed.
//
// It groups by identity rather than by name, which is the difference between
// evidence about subjects and a sidebar. Two unrelated checkouts both called
// "babel" are one ambiguous name and two repositories: the feed's vocabulary
// refuses to bind the name, and rightly, while the ledger can hold both as
// entities with their own identifiers. A session whose repository this host
// could not observe produces nothing, because a locator is not a topic.
//
// It carries no records. The record-to-identity derivation walks the
// development path back to the sessions each record cites and belongs to the
// corpus a server holds; UnboundIdentities below is the reading that has both.
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

// UnboundIdentities is what this host observes and the ledger does not name:
// one entry per repository identity, with the workspaces that resolved to it,
// how much of the catalog stands behind it, and the records whose evidence
// walks back to it.
//
// It is evidence handed to the filing run and nothing else. §4.13 leaves
// every topic to a proposal a run published and a ruling the operator gave,
// so this names no entity, raises nothing and files nothing; what it does is
// answer the one question a run cannot answer from the ledger — what this
// machine has been working in that nobody has named.
//
// A name with no binding is skipped rather than offered. An ambiguous name is
// two repositories this host cannot tell apart, and offering it as one
// identity would hand the run evidence about two things at once.
//
// The already-bound filter is over the ledger's binding facts, which is what
// an accepted topic carries. An entity bound by an alias alone still reaches
// the run as evidence, and the ledger refuses the proposal that would
// duplicate it — the refusal is the ledger's, which is where the rule belongs.
func (s *Server) UnboundIdentities(ctx context.Context) ([]reality.TopicObservation, error) {
	sessions := s.sessionsBySourceID(ctx)
	corpus, err := s.readCorpus(ctx)
	if err != nil {
		return nil, err
	}
	bindings := topicBindings(sessions)
	counts := map[string]int{}
	for _, rows := range sessions {
		for _, row := range rows {
			if name := topicOf(row); name != "" {
				counts[name]++
			}
		}
	}
	records := map[string][]frontier.Ref{}
	for id, names := range corpus.topics(sessions) {
		kind, known := kindOfRecordID(id)
		if !known {
			continue
		}
		for _, name := range names {
			records[name] = append(records[name], frontier.Ref{Type: kind, ID: id})
		}
	}
	bound := s.boundIdentities(ctx)
	out := make([]reality.TopicObservation, 0, len(bindings))
	for name, binding := range bindings {
		if binding == nil {
			continue
		}
		if _, named := bound[identityKey(binding.Identity)]; named {
			continue
		}
		refs := records[name]
		sort.Slice(refs, func(a, b int) bool { return refs[a].ID < refs[b].ID })
		out = append(out, reality.TopicObservation{
			Identity:  binding.Identity,
			Remote:    binding.Remote,
			Name:      name,
			Paths:     binding.Paths,
			Sessions:  counts[name],
			Checkouts: len(binding.Paths),
			Records:   refs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// boundIdentities is every value the ledger's live topics are bound by.
//
// A retired entity does not bind, for §4.13's reason: retiring a topic that
// should never have existed must not make the thing it named unnameable
// forever.
func (s *Server) boundIdentities(ctx context.Context) map[string]struct{} {
	bound := map[string]struct{}{}
	if s.opts.Reality == nil {
		return bound
	}
	entities, err := s.opts.Reality.Entities(ctx, reality.EntityQuery{})
	if err != nil {
		return bound
	}
	for _, listing := range entities {
		entity := listing.Entity
		if entity.CanonicalID != "" && entity.CanonicalID != entity.ID {
			continue
		}
		facts, err := s.opts.Reality.Facts(ctx, reality.FactQuery{
			SubjectID: entity.ID,
			Statuses:  []reality.FactStatus{reality.FactActive},
		})
		if err != nil {
			continue
		}
		values := make([]string, 0, len(facts))
		retired := false
		for _, fact := range facts {
			switch fact.Predicate {
			case topicRemotePredicate, reality.PredicateLocalPath:
				values = append(values, fact.Value.Text)
			case reality.PredicateLifecycle:
				retired = fact.Value.Enum == reality.LifecycleRetired
			}
		}
		if retired {
			continue
		}
		for _, value := range values {
			if key := identityKey(value); key != "" {
				bound[key] = struct{}{}
			}
		}
	}
	return bound
}

// identityKey compares two spellings of one identity the way the ledger does:
// case-folded and trimmed, because the same repository cloned as "Manifold"
// on one machine and "manifold" on another is one project (§4.13).
func identityKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
