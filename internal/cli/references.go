package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/reference/resolve"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The citation section every record-describing command grew for issue #113.
//
// It is here rather than in frontiercmd.go or sessions.go because the same
// section appears on a hypothesis, a finding and a session, and the three have
// nothing else in common: a candidate is addressed by the identifier the
// frontier minted, a session by a digest this machine derives, and a section
// spelled once in each file would be three answers to "what does this record
// cite".
//
// internal/frontier's own links are a different relation and keep their own
// renderer. Those edges join two records inside one store and are typed by
// that store's vocabulary; these join records across every store Babel has and
// carry an actor and a note. Showing them in one table would merge two claims
// a reader has to be able to tell apart.

// noCitationGraph is what a build with no edge store prints in place of a
// record's citations.
//
// It states the absence of the graph and not the absence of links, because
// "nothing cites this record" is a claim about the corpus that a machine
// without the graph is in no position to make.
const noCitationGraph = "this build records no citations, so none are shown"

// errNoSessionKey reports that this machine cannot name its own sessions in
// the reference graph.
//
// A session endpoint is the sharedcatalog.SessionUID digest over the
// deployment, the host, the harness and the source id (SPEC.md §9), so a
// machine with no deployment identity has no key to look up. Deriving one under
// the empty string would produce a key no host ever published and then report
// every real citation as absent, which is worse than saying so.
var errNoSessionKey = errors.New(
	"this machine has no deployment identity, so its sessions have no durable key for a citation to name")

// citationRow is one edge as a record surface shows it: the other endpoint,
// the relation, who asserted it, and why.
//
// The endpoint is carried as its two parts rather than as the composed
// "kind:id" the table prints, because a caller reading the document wants the
// namespace to branch on and a reader wants one cell.
type citationRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	OtherKind string `json:"other_kind"`
	OtherID   string `json:"other_id"`
	Actor     string `json:"actor"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

// citationCounts summarises each direction by edge kind, which is the question
// a reader asks before reading any row: is this record standing on evidence,
// or being corrected by something later.
type citationCounts struct {
	Cites   map[string]int `json:"cites,omitempty"`
	CitedBy map[string]int `json:"cited_by,omitempty"`
}

// citations is the reference graph immediately around one record: what it
// cites, what cites it, and the shape of both.
type citations struct {
	Cites   []citationRow  `json:"cites"`
	CitedBy []citationRow  `json:"cited_by"`
	Counts  citationCounts `json:"counts"`
}

// citationsFor reads the citations around one record in both directions.
//
// The store is taken as the concrete type rather than as reference.Lister
// because the two absences a surface has to distinguish are "this build has no
// edge store" and "this record has no citations", and a nil *reference.Store
// assigned to an interface is not nil. The concrete pointer makes the first
// absence a real test.
//
// A failure is returned rather than swallowed, and the caller states it. A
// section that went quiet because a read failed would be indistinguishable
// from a record nobody has cited, and only one of those is something Babel
// observed.
func citationsFor(ctx context.Context, store *reference.Store, ref reference.RecordRef) (*citations, error) {
	if store == nil {
		return nil, nil
	}
	out, err := store.From(ctx, ref)
	if err != nil {
		return nil, err
	}
	in, err := store.To(ctx, ref)
	if err != nil {
		return nil, err
	}
	c := &citations{
		Cites:   renderCitations(out, reference.DirectionFrom),
		CitedBy: renderCitations(in, reference.DirectionTo),
	}
	c.Counts.Cites = countCitationKinds(c.Cites)
	c.Counts.CitedBy = countCitationKinds(c.CitedBy)
	return c, nil
}

// renderCitations states one direction's edges as terminal-safe rows.
//
// Every field passes through the renderer, the closed vocabularies included.
// The edge kind and the endpoint namespace are checked when an edge is written,
// but these rows are read back out of a local file an operator or a
// half-restored backup can put anything in, and a value trusted because a
// writer once validated it is a value nobody is rendering.
//
// Which endpoint is "the other one" depends on the direction the listing
// answered, so it is taken from the query rather than guessed from the row.
func renderCitations(edges []reference.Edge, dir reference.Direction) []citationRow {
	out := make([]citationRow, 0, len(edges))
	for _, e := range edges {
		other := e.To
		if dir == reference.DirectionTo {
			other = e.From
		}
		out = append(out, citationRow{
			ID:        Sanitize(e.ID),
			Kind:      Sanitize(string(e.Kind)),
			OtherKind: Sanitize(other.Kind),
			OtherID:   Sanitize(other.ID),
			Actor:     renderCitationActor(e.ActorKind, e.ActorRef),
			Note:      Sanitize(e.Note),
			CreatedAt: formatTime(e.CreatedAt),
		})
	}
	return out
}

// renderCitationActor writes who asserted one citation as a single cell.
//
// It is not renderActor. A frontier actor is an identity whose kind qualifies
// it, so that renderer keys on the identity being present; an edge always
// names an actor kind and only sometimes a reference — reference.ActorSystem
// carries none — and keying on the reference would render Babel's own
// assertions as having no author at all.
func renderCitationActor(kind, ref string) string {
	if ref == "" {
		return Sanitize(kind)
	}
	return Sanitize(kind) + " " + Sanitize(ref)
}

// countCitationKinds counts one direction by edge kind.
//
// It counts the rendered kind rather than the stored one, so the counts and
// the rows are keyed by the same strings: a summary line naming a kind no row
// shows would be a summary of a different table.
func countCitationKinds(rows []citationRow) map[string]int {
	if len(rows) == 0 {
		return nil
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Kind]++
	}
	return counts
}

// summary is the counts line the section prints above the table: both
// directions, each broken down by kind. It is what a reader needs when a
// record has accumulated more citations than fit on a screen.
func (c *citations) summary() string {
	return "cites: " + citationKindCell(c.Counts.Cites) +
		"  cited by: " + citationKindCell(c.Counts.CitedBy)
}

// citationKindCell renders one direction's breakdown, sorted by kind so two
// runs over the same record print the same line.
func citationKindCell(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(counts))
	for _, kind := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, kind+" "+strconv.Itoa(counts[kind]))
	}
	return strings.Join(parts, ", ")
}

// writeCitations renders the references section of a record view.
//
// A record whose citations could not be read still prints its section, saying
// why. The command succeeds either way: the citations are an addition to a
// record view, and a broken edge store must not take a healthy hypothesis with
// it.
func (a *app) writeCitations(refs *citations, readErr error) error {
	fmt.Fprint(a.stdout, "\nreferences\n")
	switch {
	case readErr != nil:
		fmt.Fprintf(a.stdout, "no citations are shown: %s\n", Sanitize(readErr.Error()))
		return nil
	case refs == nil:
		fmt.Fprint(a.stdout, noCitationGraph+"\n")
		return nil
	}
	fmt.Fprint(a.stdout, refs.summary()+"\n")
	table := make([][]string, 0, len(refs.Cites)+len(refs.CitedBy))
	for _, row := range refs.Cites {
		table = append(table, row.cells("cites"))
	}
	for _, row := range refs.CitedBy {
		table = append(table, row.cells("cited by"))
	}
	return writeTable(a.stdout, []string{"DIR", "KIND", "OTHER", "ACTOR", "NOTE"}, table)
}

// cells states one row for the table. The endpoint is composed here because
// "<namespace>:<id>" is layout, which the renderer deliberately refuses to
// carry inside a value.
func (r citationRow) cells(dir string) []string {
	return []string{dir, r.Kind, r.OtherKind + ":" + r.OtherID, orMissing(r.Actor), orMissing(r.Note)}
}

// noteUnreadCitations warns on stderr about a fault the terminal section would
// have stated on stdout, for a caller reading the machine-readable document
// instead.
//
// The document omits the field rather than carrying an empty citations object,
// because an empty object asserts that this record cites nothing and a failed
// read is precisely Babel not knowing.
//
// A machine with no deployment identity is exempt, because that is not a
// fault. It is the permanent condition of every local-mode install, and a
// warning every `--json` invocation emits is noise rather than information.
// The terminal section still says it: a reader looking at an empty section
// needs the reason, and a script reading a document without the field is not
// being told anything untrue.
func (a *app) noteUnreadCitations(err error) {
	if err == nil || errors.Is(err, errNoSessionKey) {
		return
	}
	a.diagf("warning: no citations are shown: %s\n", Sanitize(err.Error()))
}

// openRecordReferences opens the edge store alone, for a surface that reads
// citations without opening the analysis state.
//
// It attaches no resolver registry, and that is not a shortcut: a registry
// gates Append, and a command that lists a record's citations validates no
// endpoint. Building one here would open the frontier, the receipts and the
// session catalog to answer a question this command never asks.
//
// It does attach the deployment's publication hook, which is the one thing this
// opener does not get to leave out (#137). A registry is a gate on writes this
// caller never performs; a hook is what makes a write publishable at all, and
// the difference matters because the next caller of this function may write.
func openRecordReferences() (*reference.Store, error) {
	d, err := babelDirs()
	if err != nil {
		return nil, err
	}
	hook, err := stagingHook()
	if err != nil {
		return nil, err
	}
	return reference.Open(d.durableDir(), reference.WithSync(hook))
}

// sessionRecordRef addresses one local session in the reference graph.
//
// The harness and source id must be the adapter's own strings rather than the
// rendered ones a listing row carries: the digest is what the publish path
// computes over the catalog's identities, and a key derived from an escaped
// value would name a session no host in the fleet has.
func sessionRecordRef(harness, sourceID string) (reference.RecordRef, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return reference.RecordRef{}, err
	}
	if cfg.DeploymentID == "" {
		return reference.RecordRef{}, errNoSessionKey
	}
	host, err := localHostID()
	if err != nil {
		return reference.RecordRef{}, err
	}
	return reference.RecordRef{
		Kind: resolve.NamespaceSession,
		ID:   sharedcatalog.SessionUID(cfg.DeploymentID, host, harness, sourceID),
	}, nil
}

// sessionCitations reads the citations around one local session.
//
// The handle's lifetime is the section's rather than the command's: `sessions
// inspect` describes a session, and the citations are an addition to that
// description. The close error is deliberately not consulted - nothing was
// written, so a handle that fails to close costs this process a file lock and
// says nothing about the citations already read.
func sessionCitations(ctx context.Context, harness, sourceID string) (*citations, error) {
	ref, err := sessionRecordRef(harness, sourceID)
	if err != nil {
		return nil, err
	}
	store, err := openRecordReferences()
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return citationsFor(ctx, store, ref)
}
