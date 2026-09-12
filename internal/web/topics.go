package web

// Topics stage 1: a topic is bound by repository identity, never by path
// (SPEC.md §4.13).
//
// What this replaces is worth stating, because the surface it produced was
// the operator's evidence for the section. Filing by workspace basename gave
// the feed a topic called "tmp" — every session run in a scratch directory,
// filed together because they shared a locator — and a topic per generated
// worktree name, so one repository read as "witty-sage-crab", "bold-gold-
// koala" and itself. Neither names a thing anybody thinks about. The
// repository's own identity does, and every worktree of it shares exactly
// one, which is why the collapse is a consequence of the binding rather than
// a special case in the naming.
//
// Nothing here creates an entity. §4.13 leaves that to an attributed operator
// act, and stage 1 is the seeding the same section permits until the triage
// recipe runs: filings derived from repository identity alone, the one
// binding observable without a model, and labelled heuristic so the recipe
// knows to revisit them.

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/frontier"
)

// topicBinding is what a topic name is bound to: the real thing a reader can
// act on, beside the locators that were evidence for it.
//
// Paths are the workspaces that resolved to this identity, and they are
// evidence rather than the topic — the distinction §4.13 draws between where
// work happened and what it was about. They are carried because the operator
// deciding whether Babel filed correctly needs to see what it looked at.
type topicBinding struct {
	// Kind is "repository" for every binding this deployment can observe.
	// It is stated rather than assumed because §4.13's topics are a machine,
	// a service or a concept just as legitimately, and a reader must not
	// have to infer the kind from the shape of the identity.
	Kind string `json:"kind"`
	// Identity is the remote as host/owner/repo when the repository has one,
	// and the absolute git common directory otherwise. The remote is
	// preferred because it is the identity that survives the same repository
	// being cloned on another machine; the common directory is the identity
	// that survives worktrees, which is what this host can always observe.
	Identity string `json:"identity"`
	// Remote is the repository's remote as host/owner/repo when it has one,
	// and empty otherwise. It is beside Identity rather than inferred from
	// it because the two answer different questions — which repository this
	// is, and where the copies of it come from — and a reader looking at a
	// topic bound only by a common directory is owed the absence rather
	// than a value he has to recognize as a path.
	Remote string   `json:"remote,omitempty"`
	Paths  []string `json:"paths"`
}

const topicKindRepository = "repository"

// topicOf is the topic one session files a record under: the repository the
// work was in, never the directory it happened in.
//
// The name is the repository's last path element — the remote's repository
// name when there is a remote, and the basename of the checkout the common
// directory belongs to when there is not. A session whose workspace this host
// could not resolve to a repository files nothing, and that is the honest
// answer rather than a guess: the session's row already carries the reason,
// and a record with no resolvable origin belongs in the feed unfiled rather
// than under a name invented for it.
func topicOf(row SessionRow) string {
	return topicName(repositoryIdentity(row))
}

// repositoryIdentity is the session's observed repository, preferring the
// remote. It returns "" for a session with no observed repository.
func repositoryIdentity(row SessionRow) string {
	if row.RepositoryRemote != nil {
		if remote := strings.TrimSpace(*row.RepositoryRemote); remote != "" {
			return remote
		}
	}
	if row.RepositoryIdentity != nil {
		if identity := strings.TrimSpace(*row.RepositoryIdentity); identity != "" {
			return identity
		}
	}
	return ""
}

// topicName reduces one repository identity to the word the operator calls it
// by.
//
// For a remote (github.com/atyrode/manifold) that is the repository element.
// For a common directory it is the checkout the directory belongs to:
// /home/alex/manifold/.git is the repository "manifold", and a linked
// worktree reports that same directory, which is exactly how every worktree
// of a repository arrives at one name. A bare repository has no parent
// checkout, so its own directory name without the ".git" suffix is the name.
//
// Lowercased, because the same repository cloned as "Manifold" on one machine
// and "manifold" on another is one project and §4.13 requires it to be one
// topic.
func topicName(identity string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(identity), "/")
	if trimmed == "" {
		return ""
	}
	base := path.Base(trimmed)
	if base == ".git" {
		parent := path.Base(path.Dir(trimmed))
		if parent != "." && parent != "/" {
			base = parent
		}
	}
	base = strings.ToLower(strings.TrimSuffix(base, ".git"))
	if base == "" || base == "." || base == "/" || base == topicUnfiled {
		return ""
	}
	return base
}

// topicBindings is the vocabulary of the whole catalog: for every topic name
// this host can observe, what it is bound to and which workspaces resolved to
// it.
//
// It reads every session rather than only the cited ones, because the binding
// answers "what is this topic" and not "what is in this topic". A reader
// looking at the repository behind a name is owed every checkout of it this
// machine holds, including the ones no record happens to cite.
//
// A name that two different repositories answer to — two unrelated checkouts
// both called "babel" — gets no binding. Both are real and the name is
// ambiguous, and a binding that picked one would tell the operator this topic
// is a repository it is not. The count still stands; the binding is absent,
// which is the same honesty the rest of this surface applies to a value it
// cannot determine.
func topicBindings(sessions map[string][]SessionRow) map[string]*topicBinding {
	type collected struct {
		identities map[string]struct{}
		paths      map[string]struct{}
	}
	byName := map[string]*collected{}
	for _, rows := range sessions {
		for _, row := range rows {
			identity := repositoryIdentity(row)
			name := topicName(identity)
			if name == "" {
				continue
			}
			entry, seen := byName[name]
			if !seen {
				entry = &collected{
					identities: map[string]struct{}{},
					paths:      map[string]struct{}{},
				}
				byName[name] = entry
			}
			entry.identities[identity] = struct{}{}
			if row.Workspace != nil {
				if workspace := strings.TrimSpace(*row.Workspace); workspace != "" {
					entry.paths[workspace] = struct{}{}
				}
			}
		}
	}
	out := make(map[string]*topicBinding, len(byName))
	for name, entry := range byName {
		if len(entry.identities) != 1 {
			out[name] = nil
			continue
		}
		binding := &topicBinding{Kind: topicKindRepository, Paths: sortedKeys(entry.paths)}
		for identity := range entry.identities {
			binding.Identity = identity
			// A remote is an identity with no leading slash: the observed
			// values are either host/owner/repo or an absolute common
			// directory, and the distinction decides which binding fact the
			// seeder proposes. It is read off the identity rather than off
			// the row a second time because repositoryIdentity already
			// preferred the remote, and asking the rows again would let the
			// two answers differ for a session whose remote and directory
			// disagree.
			if !strings.HasPrefix(identity, "/") {
				binding.Remote = identity
			}
		}
		out[name] = binding
	}
	return out
}

// sortedKeys states a set as a stable list, so two reads of one topic render
// the same paths in the same order.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// seedTopic is one repository this host can observe, with the records stage 1
// would file under it (§4.13).
//
// It is the seeder's input and nothing else's. The feed no longer files
// anything by repository identity — a post's topics are the ledger entities it
// is filed under — so what this derivation is for is raising the topic
// questions the operator answers: the identity Babel proposes, the binding it
// proposes for it, and the evidence it is proposing on.
type seedTopic struct {
	// Identity is the repository's own identity, preferring the remote, and
	// is the dedup key a topic question is raised under.
	Identity string
	Remote   string
	Name     string
	// Kind is left empty here: this derivation observes repositories, and
	// the entity kind a question proposes belongs to the ledger's own
	// vocabulary rather than to a projection over the session catalog.
	Kind  string
	Paths []string
	// Sessions is how many of this host's sessions carry the identity, and
	// Checkouts how many workspaces resolved to it. Both are the proposal's
	// evidence weight: §4.13 lifts a declined proposal's suppression on
	// materially new evidence, which for a repository is more sessions than
	// when it was declined.
	Sessions  int
	Checkouts int
	// Records are the records stage 1 would file under the topic: the ones
	// whose evidence walks back to a session in this repository.
	Records []frontier.Ref
}

// seedTopics derives what §4.13's seeding would propose: one entry per
// repository this host observes, with the records that cite it.
//
// It is the one caller of the heuristic derivation left. The feed reads
// filings, the topics page reads the ledger, and this reads the session
// catalog — which is the only one of the three that can observe a repository
// nobody has created an entity for, and therefore the only one that can
// propose one.
//
// A name with no binding is skipped rather than proposed. An ambiguous name is
// two repositories this host cannot tell apart, and proposing an entity for it
// would ask the operator to accept a topic that is not one thing.
func (s *Server) seedTopics(ctx context.Context) ([]seedTopic, error) {
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
	out := make([]seedTopic, 0, len(bindings))
	for name, binding := range bindings {
		if binding == nil {
			continue
		}
		refs := records[name]
		sort.Slice(refs, func(a, b int) bool { return refs[a].ID < refs[b].ID })
		out = append(out, seedTopic{
			Identity:  binding.Identity,
			Remote:    binding.Remote,
			Name:      name,
			Paths:     binding.Paths,
			Sessions:  counts[name],
			Checkouts: len(binding.Paths),
			Records:   refs,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}
