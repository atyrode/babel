package web

// Issue #113's read surface: the typed reference graph as a record page shows
// it — what this record cites, and what cites it.
//
// Three rules shape this file, and each is a rule about honesty rather than
// about convenience.
//
// A link target is an identity, never an href. Every endpoint travels as the
// namespace and identifier the edge recorded, and the route that opens it is
// derived from that pair by the client's own route table. Nothing here emits a
// URL, and nothing a record's text contains can become one: an edge Note is
// prose a model wrote about someone's corpus, and prose that could name a link
// destination would make the graph an injection surface.
//
// An endpoint this host cannot open says so, in the fleet read's own terms.
// #113 publishes edge kinds and endpoint identifiers as plaintext-eligible
// metadata (SPEC §763) precisely so the graph's shape stays navigable on a host
// that holds neither the record nor the key to it, which means a correct link
// section routinely names records that are not here. Such a row is inert with a
// stated reason — the same discipline internal/web/fleet.go's Unopened column
// applies to a sealed record — and never a link that leads to a page reporting
// nothing.
//
// The edges are the store's, in the store's order. reference.Lister documents
// newest first and internal/reference bounds what it returns; this route pages
// over that answer and neither re-sorts nor filters it, for the reason
// handleRecordRevisions gives about a chain: a citation graph a surface quietly
// reordered is one nobody can audit against the store.

import (
	"context"
	"errors"
	"net/http"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
)

// Namespaces this build's record surfaces can resolve. The set is deliberately
// smaller than reference.RecordRef's open vocabulary: a namespace is listed
// here only when this package can both check that the record exists and name
// the page that opens it, and every other namespace renders inert with its name
// in the reason rather than as a link into nothing.
const (
	namespaceSession     = "session"
	namespaceHypothesis  = "hypothesis"
	namespaceObservation = "observation"
	namespaceFinding     = "finding"
	namespaceProposal    = "proposal"
)

// referenceRefView is one endpoint of an edge on the wire: the namespace and
// identifier the edge recorded, plus what this host can say about opening it.
//
// There is no URL field, and its absence is the invariant. The client maps
// namespace and identifier onto its own route — the same mapping the lineage
// panel already performs — so a destination is always derived from an identity
// this server resolved and never from text a record carries.
type referenceRefView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// RouteID is the identifier this build's page for the record is reached
	// by, present only when it differs from ID.
	//
	// It exists for exactly one namespace, and the reason is worth stating
	// because it looks like duplication. A session's durable key is a
	// deployment-scoped digest (issue #113: an endpoint publishes as a
	// plaintext catalog column, and a selector carries a workspace-derived
	// path, so the key is what an edge may record). The session page routes
	// on the local selector. Both are true identities of the same session,
	// the edge owns the first and this host resolved the second, so the row
	// carries both rather than a client guessing either.
	RouteID string `json:"route_id,omitempty"`
	// Label is a short human identity for the record when this host could
	// open it — a session's selector, and nothing at all for the analysis
	// records, whose identifiers are already what a reader recognizes.
	// Untrusted content: it is derived from a source path.
	Label string `json:"label,omitempty"`
	// Inert marks an endpoint that must render as identified text rather than
	// as a link, with Reason saying which of the four cases it is: a namespace
	// this build opens no page for, a service this session did not wire, a
	// record this host does not hold, or a check this host could not complete.
	//
	// Inert is never inferred from an empty Reason and Reason never travels
	// without Inert: a row that said "not followable" without saying why would
	// send an operator looking for a missing key when the answer is a missing
	// service.
	Inert  bool   `json:"inert,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// referenceEdgeView is one edge as the record that was asked about sees it.
//
// Only the far endpoint travels. A page already knows which record it is, and
// carrying both endpoints would make every row restate the subject and invite a
// client to re-derive the direction it was handed.
type referenceEdgeView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Other is the record at the far side of the edge: the cited record in
	// the outgoing direction, the citing one in the backlink direction.
	Other referenceRefView `json:"other"`
	// Actor is who asserted the link. reference.Edge records an operator, a
	// run, or the system, and no machine at all, so an edge is attributed to
	// its author here and never to a host: the host this listing was read
	// from is stated once for the whole document instead.
	Actor actorView `json:"actor"`
	// Note is why the link exists, in the words of whoever asserted it.
	// Content bytes: it reaches the client through the same sanitizer every
	// other string on this surface does, and the page renders it as
	// attributed untrusted text.
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

// referenceKindCount is one edge kind's share of a direction.
type referenceKindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// referenceDirection is one half of a record's citations.
//
// Counts are over the whole direction and Edges over the page cut from it, so a
// chip row summarizing "three evidence, one supersedes" stays true while a
// reader is paging through the rows beneath it.
type referenceDirection struct {
	Edges  []referenceEdgeView  `json:"edges"`
	Counts []referenceKindCount `json:"counts"`
	// Total is how many edges the store answered with for this direction,
	// which is not necessarily how many exist: internal/reference bounds its
	// own answer, so this is the ceiling on what any page of this response
	// can show rather than a census of the graph.
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// recordReferences is GET /api/record/links.
type recordReferences struct {
	Record referenceRefView `json:"record"`
	// Available is false on a build with no reference graph wired, and the
	// two directions are then empty.
	//
	// It answers 200 rather than refusing, on internal/web/fleet.go's terms
	// for an unconfigured fleet: a record page's citation section is one
	// panel among several, and a refusal would put an error banner over a
	// page whose every other read succeeded. "This build records no
	// citations" is a true statement about the deployment, and a section that
	// says it is not a section that failed.
	Available bool `json:"available"`
	// Host is the machine whose catalog these edges were read from, empty
	// when this instance has no host identity. Every edge in this document is
	// that machine's own record: the reference store is local, so the listing
	// is one host's view of the graph and says so rather than presenting
	// itself as the deployment's.
	Host    string             `json:"host,omitempty"`
	Cites   referenceDirection `json:"cites"`
	CitedBy referenceDirection `json:"cited_by"`
}

// emptyDirection is a direction nothing was read for: an array rather than a
// null, so a client never has to distinguish "no citations" from "no field".
func emptyDirection(pg page) referenceDirection {
	return referenceDirection{
		Edges:  []referenceEdgeView{},
		Counts: []referenceKindCount{},
		Limit:  pg.limit,
		Offset: pg.offset,
	}
}

// handleRecordLinks renders one record's outgoing citations and its backlinks
// (#113).
//
// Both directions are answered in one request because they are one section of
// one page: an operator reading a candidate asks "what is this built on, and
// what rests on it", and two routes would let a client render half the answer
// while the other half was still in flight.
func (s *Server) handleRecordLinks(w http.ResponseWriter, r *http.Request) {
	named, ok := s.requireReferenceRef(w, r)
	if !ok {
		return
	}
	pg, ok := s.requirePage(w, r)
	if !ok {
		return
	}
	subject, reason := s.referenceSubject(r.Context(), named)
	if s.opts.References == nil || reason != "" {
		record := referenceRefView{Kind: named.Kind, ID: named.ID}
		if reason != "" {
			record = inert(record, reason)
		}
		s.writeJSON(w, http.StatusOK, recordReferences{
			Record:    record,
			Available: s.opts.References != nil,
			Cites:     emptyDirection(pg),
			CitedBy:   emptyDirection(pg),
		})
		return
	}
	cites, err := s.opts.References.From(r.Context(), subject)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}
	citedBy, err := s.opts.References.To(r.Context(), subject)
	if err != nil {
		s.serviceError(w, r, err)
		return
	}

	// The session keys on this page are resolved before any row is, so the
	// batch covers both directions at once: a candidate and the records
	// citing it routinely rest on the same session.
	resolver := s.newReferenceResolver(r.Context())
	resolver.primeSessions(cites, citedBy)
	result := recordReferences{
		// The record echoed back is the one the caller named, with the
		// durable identity the edges were looked up under beside it when the
		// two differ: a page asked about a session by selector and is
		// entitled to see the key its citations are recorded against.
		Record:    s.referenceSubjectView(named, subject),
		Available: true,
		Host:      s.hostIdentity(r.Context()),
		Cites:     resolver.direction(cites, pg, outgoing),
		CitedBy:   resolver.direction(citedBy, pg, incoming),
	}
	s.writeJSON(w, http.StatusOK, result)
}

// requireReferenceRef reads the ?type=&id= pair a record read names, on the
// same two parameters the revision and disposition reads on the same page take.
//
// The namespace is not checked against a closed list, and that is deliberate:
// reference.RecordRef's vocabulary is whatever the stores register themselves
// as, so a namespace this build does not resolve is a record with no edges and
// an inert endpoint, not a bad request. The one closed vocabulary on this
// surface belongs to the edge kinds, and internal/reference owns it.
func (s *Server) requireReferenceRef(w http.ResponseWriter, r *http.Request) (reference.RecordRef, bool) {
	kind, ok := s.requireID(w, r, "type")
	if !ok {
		return reference.RecordRef{}, false
	}
	id, ok := s.requireID(w, r, "id")
	if !ok {
		return reference.RecordRef{}, false
	}
	return reference.RecordRef{Kind: kind, ID: id}, true
}

// referenceSubject turns the record a caller named into the identity the
// citation graph records it under, or states why it cannot.
//
// It is the identity translation, and it exists for one namespace. A session is
// named by selector everywhere an operator can type or click one, and an edge
// records the durable key instead; so a session subject is derived here and
// every other namespace passes through unchanged, because for those two the
// identifier is the same string.
//
// A selector this host has no session for is not an error. It is the answer:
// the citations of a session this machine does not hold are not readable from
// this machine's graph, and saying so is better than reporting an empty one.
func (s *Server) referenceSubject(ctx context.Context, named reference.RecordRef) (reference.RecordRef, string) {
	if named.Kind != namespaceSession {
		return named, ""
	}
	if s.opts.Sessions == nil {
		return named, "this build cannot resolve durable session keys, so it cannot say what cites this session"
	}
	key, ok, err := s.opts.Sessions.KeyForSelector(ctx, named.ID)
	switch {
	case err != nil:
		return named, "this host could not read its session catalog, so it cannot say what cites this session"
	case !ok:
		return named, "this host's catalog holds no session with that selector, so it records no citations for it"
	}
	return reference.RecordRef{Kind: named.Kind, ID: key}, ""
}

// referenceSubjectView echoes the record asked about, carrying the durable
// identity beside it when the caller named the record by another one.
func (s *Server) referenceSubjectView(named, subject reference.RecordRef) referenceRefView {
	view := referenceRefView{Kind: subject.Kind, ID: subject.ID}
	if named.ID != subject.ID {
		view.RouteID = named.ID
		view.Label = named.ID
	}
	return view
}

// citationCounts is one record's citation degree, in both directions.
type citationCounts struct {
	Cites   int `json:"cites"`
	CitedBy int `json:"cited_by"`
}

// citationCounts counts one record's typed references for a listing row.
//
// A listing pays two indexed reads per row for this, which is the same order of
// cost the fleet listing pays to open one summary per row, and it buys the
// question an inbox is actually read for: which of these records is load-bearing
// for others. The subject is a frontier.Ref because that is what the review
// queue holds, and its type is the namespace an edge records.
//
// Nothing here can fail a page. No graph means no counts, and a graph that
// refused means no counts for that row and a line in the diagnostics: an inbox
// that would not render because a citation index was locked would cost an
// operator their whole backlog over a column.
func (s *Server) citationCounts(ctx context.Context, subject frontier.Ref) *citationCounts {
	if s.opts.References == nil {
		return nil
	}
	ref := reference.RecordRef{Kind: string(subject.Type), ID: subject.ID}
	cites, err := s.opts.References.From(ctx, ref)
	if err != nil {
		s.logf("citation counts for %s refused", ref)
		return nil
	}
	citedBy, err := s.opts.References.To(ctx, ref)
	if err != nil {
		s.logf("citation counts for %s refused", ref)
		return nil
	}
	return &citationCounts{Cites: len(cites), CitedBy: len(citedBy)}
}

// hostIdentity names the machine this listing was read from, and reports an
// absence as an absence: a state provider that could not answer leaves the
// field out rather than letting a page attribute one host's graph to another.
func (s *Server) hostIdentity(ctx context.Context) string {
	if s.opts.State == nil {
		return ""
	}
	state, err := s.opts.State.WebState(ctx)
	if err != nil {
		return ""
	}
	return state.HostID
}

// direction is which endpoint of an edge the page is standing on.
type direction bool

const (
	outgoing direction = false
	incoming direction = true
)

// referenceResolver answers "can this host open the record this edge names?"
// once per request.
//
// It exists for two reasons that both come from the shape of a citation graph.
// A record is routinely at the far side of several edges — four observations
// citing one session, a chain of revisions all superseding the same original —
// so the answers are memoized and a page of edges costs one lookup per distinct
// record rather than one per row. And sessions are matched by durable key
// against a catalog that enumerates rather than indexes on one, so every session
// key on the page is resolved in a single batch before any row is rendered: a
// page of citations must not become a page of catalog scans.
type referenceResolver struct {
	server *Server
	ctx    context.Context

	resolved map[reference.RecordRef]referenceRefView

	// sessions maps the durable keys this page named onto the local sessions
	// they name, and sessionsErr records a catalog that could not answer.
	// Both are set by primeSessions, which runs before any row is resolved.
	sessions    map[string]SessionRow
	sessionsErr error
}

func (s *Server) newReferenceResolver(ctx context.Context) *referenceResolver {
	return &referenceResolver{
		server:   s,
		ctx:      ctx,
		resolved: make(map[reference.RecordRef]referenceRefView),
	}
}

// primeSessions resolves every durable session key this page names, in one
// call.
//
// It is a pre-pass rather than a lazy lookup because the resolver's cost model
// is per call and not per key: a catalog answers by walking its rows, so one
// call for twenty keys is one walk and twenty calls are twenty. A page with no
// session endpoint makes no call at all.
func (r *referenceResolver) primeSessions(directions ...[]reference.Edge) {
	var keys []string
	seen := make(map[string]struct{})
	for _, edges := range directions {
		for _, edge := range edges {
			for _, ref := range [2]reference.RecordRef{edge.From, edge.To} {
				if ref.Kind != namespaceSession {
					continue
				}
				if _, ok := seen[ref.ID]; ok {
					continue
				}
				seen[ref.ID] = struct{}{}
				keys = append(keys, ref.ID)
			}
		}
	}
	if len(keys) == 0 || r.server.opts.Sessions == nil {
		return
	}
	rows, err := r.server.opts.Sessions.SessionsByKey(r.ctx, keys)
	if err != nil {
		r.sessionsErr = err
		return
	}
	r.sessions = rows
}

// direction renders one half of a record's citations: the counts over the whole
// half, and the rows of the page cut from it.
func (r *referenceResolver) direction(edges []reference.Edge, pg page, dir direction) referenceDirection {
	out := referenceDirection{
		Edges:  []referenceEdgeView{},
		Counts: countEdgeKinds(edges),
		Total:  len(edges),
		Limit:  pg.limit,
		Offset: pg.offset,
	}
	start, end := pg.window(len(edges))
	for _, edge := range edges[start:end] {
		other := edge.To
		if dir == incoming {
			other = edge.From
		}
		out.Edges = append(out.Edges, referenceEdgeView{
			ID:        edge.ID,
			Kind:      string(edge.Kind),
			Other:     r.endpoint(other),
			Actor:     actorView{Kind: edge.ActorKind, ID: edge.ActorRef},
			Note:      edge.Note,
			CreatedAt: timeText(edge.CreatedAt),
		})
	}
	return out
}

// countEdgeKinds tallies a direction by edge kind, in the order the kinds were
// first seen. First-seen order rather than alphabetical is what makes the chip
// row track the listing beneath it: the store answers newest first, so the kind
// of the most recent citation is the leftmost chip.
func countEdgeKinds(edges []reference.Edge) []referenceKindCount {
	counts := make([]referenceKindCount, 0, 4)
	at := make(map[string]int, 4)
	for _, edge := range edges {
		kind := string(edge.Kind)
		if i, ok := at[kind]; ok {
			counts[i].Count++
			continue
		}
		at[kind] = len(counts)
		counts = append(counts, referenceKindCount{Kind: kind, Count: 1})
	}
	return counts
}

// endpoint resolves one far endpoint into what this host can say about it.
func (r *referenceResolver) endpoint(ref reference.RecordRef) referenceRefView {
	if view, ok := r.resolved[ref]; ok {
		return view
	}
	view := r.resolve(ref)
	r.resolved[ref] = view
	return view
}

func (r *referenceResolver) resolve(ref reference.RecordRef) referenceRefView {
	view := referenceRefView{Kind: ref.Kind, ID: ref.ID}
	switch ref.Kind {
	case namespaceSession:
		return r.session(view)
	case namespaceHypothesis, namespaceObservation, namespaceFinding, namespaceProposal:
		return r.frontierRecord(view)
	default:
		// A namespace this build has no page for. The row is still a row:
		// #113 makes an edge's shape plaintext-eligible so a host can see
		// where a record sits in the graph without holding the record, and a
		// surface that dropped these would hide exactly the citations the
		// fleet-wide graph exists to show.
		view.Inert = true
		view.Reason = "this build opens no page for the " + quoted(ref.Kind) +
			" namespace, so the reference is recorded here but not followable from here"
		return view
	}
}

// frontierRecord checks one analysis record against the local frontier.
//
// The read is by identifier and costs one indexed lookup, which is what makes
// it affordable per row; the fleet listing pays the same price per row to open a
// summary. A record that is not here is the expected case rather than an error:
// an edge may name another host's candidate, and #112 makes that edge readable
// on a machine that will never hold the record.
func (r *referenceResolver) frontierRecord(view referenceRefView) referenceRefView {
	reader := r.server.opts.Frontier
	if reader == nil {
		return inert(view, "the hypothesis frontier is not available in this session, so this reference cannot be resolved here")
	}
	var err error
	switch view.Kind {
	case namespaceHypothesis:
		_, err = reader.Hypothesis(r.ctx, view.ID)
	case namespaceObservation:
		_, err = reader.Observation(r.ctx, view.ID)
	case namespaceFinding:
		_, err = reader.Finding(r.ctx, view.ID)
	case namespaceProposal:
		_, err = reader.Proposal(r.ctx, view.ID)
	}
	switch {
	case err == nil:
		return view
	case errors.Is(err, frontier.ErrUnknownEntity):
		return inert(view, "this host holds no "+view.Kind+" with that identifier: the edge's shape is visible here, the record it names is not")
	default:
		// The store failed rather than answered. Reported in the row and
		// never as the page's failure, because "one endpoint could not be
		// checked" is a truthful answer about a graph and a refused page is
		// not.
		return inert(view, "this host could not check whether that "+view.Kind+" exists, so the reference is left unfollowed")
	}
}

// session matches one durable session key against this host's catalog.
//
// The key is what an edge records and it is not what a page routes on. #113
// publishes an endpoint as a plaintext catalog column, and a session's selector
// carries a workspace-derived path, so the durable identity is a
// deployment-scoped digest; the session page has always routed on the local
// selector. A resolved session therefore carries both, and an unresolved one
// carries only the key it was recorded under — which is the honest rendering of
// another host's session, whose selector this machine has no way to know.
func (r *referenceResolver) session(view referenceRefView) referenceRefView {
	if r.server.opts.Sessions == nil {
		return inert(view, "this build cannot resolve durable session keys, so this reference cannot be followed here")
	}
	if r.sessionsErr != nil {
		return inert(view, "this host could not read its session catalog, so the reference is left unfollowed")
	}
	row, ok := r.sessions[view.ID]
	if !ok {
		return inert(view, "this host's catalog holds no session with that durable key: the edge's shape is visible here, the session it names is not")
	}
	view.RouteID = row.Selector
	view.Label = row.Selector
	return view
}

func inert(view referenceRefView, reason string) referenceRefView {
	view.Inert = true
	view.Reason = reason
	return view
}

// quoted puts a namespace this build does not know inside quotation marks, so a
// reason reads as naming a value rather than as containing one. The value has
// already passed through nothing at all at this point — it is an identifier a
// remote host recorded — and the response sanitizer is what makes it safe to
// repeat, exactly as it is for every other string on this surface.
func quoted(value string) string { return `"` + value + `"` }
