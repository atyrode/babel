package conductor

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"
)

// The rungs of #96's work ladder, in precedence order. They are named constants
// because a rung name reaches the journal, `conductor status` and the dashboard:
// a renamed rung would silently break the history it is read against.
const (
	// RungInvitation is the operator's own process-further queue (#87). It is
	// rung one because the operator always outranks the loop.
	RungInvitation = "invitation"
	// RungPolicy is the attention policy of the spec's context system:
	// lifecycle, focus and fleet directing expensive investigation. This build
	// implements no policy, and the rung is present and visibly absent rather
	// than omitted — a ladder that silently lacked a rung would make the
	// serendipity floor look like the second-highest authority Babel has.
	RungPolicy = "policy"
	// RungSerendipity is the protected chaotic fraction: a random corpus slice
	// crossed with a random default-enabled recipe, with no aim.
	RungSerendipity = "serendipity"
)

// ErrNoWork reports that a rung had nothing to draw. It is the ordinary state
// of every rung above the floor, so it is a sentinel rather than an error
// condition: an empty invitation queue is a conductor that is keeping up.
var ErrNoWork = errors.New("conductor: this rung has no work")

// DrawRequest is what a rung is told about the cycle it is drawing for.
//
// The run identity is included because taking work can be a durable act: rung
// one claims an operator's invitation in the name of the run that is about to
// happen, and a claim naming no run could never be checked against what ran.
type DrawRequest struct {
	RunID string
	At    time.Time
}

// Assignment is one cycle's work: the corpus slice to run over, the recipes to
// run, the frontier roots to start from, and the authority that permits it.
//
// An empty Sessions list means every session this host can see, and an empty
// Recipes list means the cookbook's default-enabled lenses — the same defaults a
// bare `babel prepare` and `babel explore` apply, because a cycle is an ordinary
// run and a scheduler that quietly narrowed the defaults would be analysing
// something different from what an operator's own command would.
type Assignment struct {
	Rung      string
	Authority run.Authority
	// Invitation names the invitation this cycle consumed, empty on every other
	// rung. It is recorded separately from the authority reference so a resumed
	// cycle can be replayed without re-claiming what it already took.
	Invitation string
	Sessions   []string
	Recipes    []string
	Roots      []string
	// Note is one line stating what was drawn and why, in the operator's terms.
	// It is the legibility half of the draw: an authority says who allowed the
	// run, and this says what the loop actually decided.
	Note string
}

// Depth is a rung's queue as `conductor status` reports it.
type Depth struct {
	// Waiting is the work this rung could draw right now.
	Waiting int
	// Implemented is false for a rung this build names but does not implement.
	// The distinction matters: "no policy is waiting" and "this build has no
	// policies" are different answers to "why is the loop doing this", and
	// reporting the second as a zero would hide a whole missing rung.
	Implemented bool
	// Note explains the depth in one line, which is what an absent rung has
	// instead of a number.
	Note string
}

// Rung is one source of work on the ladder. The interface is small on purpose:
// a rung answers what it has and hands over one piece of work, and everything
// about how that work runs belongs to the Runner on the other side of the loop.
type Rung interface {
	Name() string
	Depth(ctx context.Context) (Depth, error)
	Draw(ctx context.Context, d DrawRequest) (Assignment, error)
}

// Invitations is the operator's process-further queue as the conductor needs it
// (#87). *disposition.Store satisfies it.
type Invitations interface {
	Invitations(ctx context.Context, filter disposition.InvitationFilter) ([]disposition.Invitation, error)
	ConsumeOne(ctx context.Context, invitationID, runID string) (disposition.Invitation, error)
}

// Origins resolves an invited record to the corpus it came out of, so a cycle
// spent on an operator's nudge reads the sessions that produced the record
// rather than the whole host.
type Origins interface {
	Origin(ctx context.Context, ref frontier.Ref) (Origin, error)
}

// Origin is where a durable record came from: the sessions its originating run
// read, and the recipe that produced it when the record names one.
//
// Both may be empty, and that is a statement rather than a failure. A record
// whose originating run left no recoverable scope is processed further over the
// host's whole corpus, because the alternative — refusing the operator's
// invitation because Babel cannot reconstruct a scope — would let a gap in
// Babel's own bookkeeping override a person's explicit request.
type Origin struct {
	Sessions []string
	Recipe   string
}

// InvitationRung is rung one: the operator's process-further queue.
type InvitationRung struct {
	queue   Invitations
	origins Origins
}

// NewInvitationRung builds rung one over the invitation queue and the record
// resolver.
func NewInvitationRung(queue Invitations, origins Origins) *InvitationRung {
	return &InvitationRung{queue: queue, origins: origins}
}

// Name reports this rung's stable name.
func (r *InvitationRung) Name() string { return RungInvitation }

// Depth reports how many invitations are waiting.
func (r *InvitationRung) Depth(ctx context.Context) (Depth, error) {
	open, err := r.queue.Invitations(ctx, disposition.InvitationFilter{})
	if err != nil {
		return Depth{}, fmt.Errorf("conductor: read the invitation queue: %w", err)
	}
	return Depth{
		Waiting:     len(open),
		Implemented: true,
		Note:        "operator invitations waiting to be processed further",
	}, nil
}

// Draw takes the oldest invitation nobody has taken.
//
// Consumption happens here, before the run, and that ordering is the whole
// safety property: internal/disposition claims an invitation with a single
// insert keyed by its identity, so a conductor that dies mid-run leaves the
// invitation consumed by a named run rather than open for a second one to take.
// The interrupted cycle is resumed under that same run identity, which is why
// losing a conductor costs at most one amended receipt and never a duplicate
// run over an operator's request.
//
// Oldest first, and deliberately not re-sorted by anything about the records:
// §5.2 confines novelty and priority to ordering the frontier, and a queue of
// operator nudges re-ranked by a model-produced score would be the loop deciding
// which of the operator's requests mattered.
func (r *InvitationRung) Draw(ctx context.Context, d DrawRequest) (Assignment, error) {
	if d.RunID == "" {
		return Assignment{}, errors.New("conductor: claiming an invitation names the run that took it")
	}
	open, err := r.queue.Invitations(ctx, disposition.InvitationFilter{Limit: 1})
	if err != nil {
		return Assignment{}, fmt.Errorf("conductor: read the invitation queue: %w", err)
	}
	if len(open) == 0 {
		return Assignment{}, ErrNoWork
	}
	invitation := open[0]
	taken, err := r.queue.ConsumeOne(ctx, invitation.ID, d.RunID)
	if err != nil {
		// Another conductor or a manual `babel explore` won the race for this
		// invitation between the read and the claim. That is not a failure of
		// this cycle: the work is being done, and the next cycle draws again.
		if errors.Is(err, disposition.ErrAlreadyConsumed) {
			return Assignment{}, ErrNoWork
		}
		return Assignment{}, fmt.Errorf("conductor: claim invitation %s: %w", invitation.ID, err)
	}
	origin, err := r.origins.Origin(ctx, taken.Record)
	if err != nil {
		return Assignment{}, fmt.Errorf("conductor: resolve the corpus behind %s: %w", taken.Record.ID, err)
	}
	a := Assignment{
		Rung:       RungInvitation,
		Authority:  run.Authority{Kind: run.AuthorityOperator, Ref: "invitation:" + taken.ID},
		Invitation: taken.ID,
		Sessions:   origin.Sessions,
		Roots:      rootsFor(taken.Record),
		Note: fmt.Sprintf("%s invited %s %s to be processed further",
			taken.By, taken.Record.Type, taken.Record.ID),
	}
	if origin.Recipe != "" {
		a.Recipes = []string{origin.Recipe}
	}
	return a, nil
}

// rootsFor points the run at the invited record when the record is a candidate
// hypothesis, which is the only kind §5.2 lets a run start from. An invitation
// on a finding or a proposal still scopes the corpus to where it came from; it
// just has no frontier root to offer, and inventing one would claim a lineage
// the record does not have.
func rootsFor(ref frontier.Ref) []string {
	if ref.Type == frontier.EntityHypothesis && ref.ID != "" {
		return []string{ref.ID}
	}
	return nil
}

// AbsentRung is a rung this build names but does not implement.
//
// It exists so the ladder's shape is the ladder #96 describes rather than the
// subset that happens to be built. `conductor status` prints it with its reason,
// so an operator reading "why did the loop do this" sees that the rung between
// their invitations and the chaos is missing rather than merely quiet — and a
// build that implements it replaces this value with a real rung and changes
// nothing else about the loop.
type AbsentRung struct {
	name   string
	reason string
}

// NewAbsentRung declares an unimplemented rung and the reason it is absent.
func NewAbsentRung(name, reason string) *AbsentRung {
	return &AbsentRung{name: name, reason: reason}
}

// PolicyRung is rung two as this build has it: declared, unimplemented, and
// saying so. The spec's attention policy — lifecycle, focus, fleet — has no
// implementation yet, and the standing duties above the floor (#88's
// self-evaluation, #94's mechanization audit) have no recipes to run, so there
// is nothing here to draw from and nothing is pretended.
func PolicyRung() *AbsentRung {
	return NewAbsentRung(RungPolicy,
		"no attention policy or standing duty is implemented in this build (#96 rung two)")
}

// Name reports this rung's stable name.
func (r *AbsentRung) Name() string { return r.name }

// Depth reports that the rung is not implemented, which is not a depth of zero.
func (r *AbsentRung) Depth(context.Context) (Depth, error) {
	return Depth{Implemented: false, Note: r.reason}, nil
}

// Draw never has work: an unimplemented rung that could produce an assignment
// would be a run with no nameable authority.
func (r *AbsentRung) Draw(context.Context, DrawRequest) (Assignment, error) {
	return Assignment{}, ErrNoWork
}

// Corpus is the host's local session inventory, which is what a serendipity draw
// slices. Scanning source adapters belongs to the command layer, so the engine
// is told what exists rather than discovering it.
type Corpus interface {
	// Sessions reports every session selector this host can see, in a stable
	// order. Order matters: a seeded draw over an unstable list would not be
	// reproducible, and a draw nobody can reproduce is not a declared draw.
	Sessions(ctx context.Context) ([]string, error)
}

// Recipes is the cookbook as the floor needs it.
type Recipes interface {
	// Defaults reports the default-enabled recipe ids, which is the set a run
	// applies when the operator names none.
	Defaults(ctx context.Context) ([]string, error)
}

// SerendipityRung is the floor: a random corpus slice crossed with a random
// default-enabled recipe, no aim.
//
// It is the one rung whose output is not derived from anything anyone asked for,
// which is the point. §5.2's frontier ordering is attention triage over what
// already exists; this is the opposite, and it is protected precisely because
// every incentive in the loop pushes towards dutifulness.
type SerendipityRung struct {
	corpus  Corpus
	recipes Recipes
	rng     *rand.Rand
	max     int
}

// DefaultSliceSessions bounds a serendipity draw when no bound was configured.
// A slice, not the corpus: the floor is meant to look somewhere arbitrary and
// small, and a draw that regularly took the whole host would be an expensive
// full sweep wearing the word "random".
const DefaultSliceSessions = 3

// NewSerendipityRung builds the floor. The generator is supplied so a draw is
// reproducible from a seed: a declared draw an operator cannot replay is a
// weaker claim than it looks.
func NewSerendipityRung(corpus Corpus, recipes Recipes, rng *rand.Rand, maxSessions int) *SerendipityRung {
	if maxSessions <= 0 {
		maxSessions = DefaultSliceSessions
	}
	return &SerendipityRung{corpus: corpus, recipes: recipes, rng: rng, max: maxSessions}
}

// Name reports this rung's stable name.
func (r *SerendipityRung) Name() string { return RungSerendipity }

// Depth reports what the floor could draw from. It is a floor rather than a
// queue, so the number is the corpus it would slice — the thing that runs out.
func (r *SerendipityRung) Depth(ctx context.Context) (Depth, error) {
	sessions, err := r.corpus.Sessions(ctx)
	if err != nil {
		return Depth{}, fmt.Errorf("conductor: read the local corpus: %w", err)
	}
	defaults, err := r.recipes.Defaults(ctx)
	if err != nil {
		return Depth{}, fmt.Errorf("conductor: read the cookbook defaults: %w", err)
	}
	return Depth{
		Waiting:     len(sessions),
		Implemented: true,
		Note: fmt.Sprintf("%d %s and %d default-enabled %s to draw from",
			len(sessions), plural(len(sessions), "session", "sessions"),
			len(defaults), plural(len(defaults), "recipe", "recipes")),
	}, nil
}

// Draw picks a slice and a recipe, and declares the draw.
//
// The identity is a ULID: time-ordered so a listing of draws reads in the order
// they happened, and random enough that two conductors cannot mint the same one.
// It is derived from the same generator as the choices, so a seed reproduces the
// whole draw — the slice, the recipe and the name it was recorded under.
func (r *SerendipityRung) Draw(ctx context.Context, d DrawRequest) (Assignment, error) {
	sessions, err := r.corpus.Sessions(ctx)
	if err != nil {
		return Assignment{}, fmt.Errorf("conductor: read the local corpus: %w", err)
	}
	if len(sessions) == 0 {
		return Assignment{}, ErrNoWork
	}
	defaults, err := r.recipes.Defaults(ctx)
	if err != nil {
		return Assignment{}, fmt.Errorf("conductor: read the cookbook defaults: %w", err)
	}
	if len(defaults) == 0 {
		return Assignment{}, ErrNoWork
	}

	bound := min(r.max, len(sessions))
	size := 1 + r.rng.IntN(bound)
	slice := slices.Clone(sessions)
	// A partial shuffle: the first size entries are a uniform sample without
	// replacement, and nothing beyond them is touched.
	for i := range size {
		j := i + r.rng.IntN(len(slice)-i)
		slice[i], slice[j] = slice[j], slice[i]
	}
	slice = slice[:size]
	recipe := defaults[r.rng.IntN(len(defaults))]
	draw := newDrawID(d.At, r.rng)

	return Assignment{
		Rung:      RungSerendipity,
		Authority: run.Authority{Kind: run.AuthoritySerendipity, Ref: "draw:" + draw},
		Sessions:  slice,
		Recipes:   []string{recipe},
		Note: fmt.Sprintf("draw %s: %s over %d %s, no aim", draw, recipe,
			len(slice), plural(len(slice), "session", "sessions")),
	}, nil
}

// crockford is ULID's alphabet: base32 without the letters that misread as
// digits, so a draw identity survives being read off a terminal and typed back.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newDrawID mints a ULID-shaped identity: 48 bits of millisecond timestamp then
// 80 bits from the draw's own generator, rendered as 26 Crockford base32
// characters.
func newDrawID(at time.Time, rng *rand.Rand) string {
	ms := uint64(at.UTC().UnixMilli())
	var raw [16]byte
	for i := range 6 {
		raw[5-i] = byte(ms >> (8 * i))
	}
	entropy := rng.Uint64()
	for i := range 8 {
		raw[6+i] = byte(entropy >> (8 * i))
	}
	tail := rng.Uint64()
	raw[14], raw[15] = byte(tail), byte(tail>>8)

	// 128 bits render as 26 base32 characters with 2 bits of padding at the
	// front, which is exactly ULID's encoding.
	var out [26]byte
	var acc uint32
	bits := 0
	pos := 26
	for i := 15; i >= 0; i-- {
		acc |= uint32(raw[i]) << bits
		bits += 8
		for bits >= 5 {
			pos--
			out[pos] = crockford[acc&0x1f]
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		pos--
		out[pos] = crockford[acc&0x1f]
	}
	for pos > 0 {
		pos--
		out[pos] = crockford[0]
	}
	return string(out[:])
}

// DefaultLadder assembles the ladder this build has: the operator's invitations,
// the declared-but-absent policy rung, and the serendipity floor.
//
// The order is the precedence, and the last rung is the floor. Callers assemble
// their own only to plant one in a test; a build that adds a rung adds it here,
// which is also where its absence would otherwise have to be explained twice.
func DefaultLadder(invitations *InvitationRung, floor *SerendipityRung) []Rung {
	return []Rung{invitations, PolicyRung(), floor}
}

// Floor is the protected fraction of cycles the serendipity rung is guaranteed.
type Floor struct {
	// OneIn guarantees at least one serendipity cycle in every OneIn
	// consecutive cycles. Zero means DefaultFloor. One means every cycle is
	// chaotic, which is a legitimate thing to configure and a strange thing to
	// want.
	OneIn int
}

// DefaultFloor is the guaranteed serendipity fraction when none was configured:
// one cycle in four is chaotic even while invitations are queueing. The ratio is
// the operator's dial; what is not negotiable is that it exists.
const DefaultFloor = 4

func (f Floor) oneIn() int {
	if f.OneIn <= 0 {
		return DefaultFloor
	}
	return f.OneIn
}

func (f Floor) validate() error {
	if f.OneIn < 0 {
		return errors.New("conductor: a serendipity floor cannot be negative")
	}
	return nil
}

// plural renders a count's unit without inventing a word for zero, matching
// internal/cli's rule so one phrasing serves both surfaces.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// selectorsOf renders a preparation's selection as the session selectors the
// rest of Babel addresses sessions by, deduplicated and ordered so a draw over
// them is reproducible.
func selectorsOf(selection []run.Selected) []string {
	out := make([]string, 0, len(selection))
	for _, s := range selection {
		if s.Harness == "" || s.SourceID == "" {
			continue
		}
		out = append(out, s.Harness+"/"+s.SourceID)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// RecordOrigins resolves a durable record to the corpus its originating run
// read, over the frontier and the run receipts.
//
// It walks records rather than guessing: the record names the run that made it,
// the run's receipt names the preparation it read, and the preparation names the
// sessions. Every step is a stored fact, which is what makes "process this
// further" mean the same corpus rather than a fresh guess at what the operator
// was looking at.
type RecordOrigins struct {
	frontier Frontier
	runs     Runs
}

// Frontier is the durable record store as the conductor reads it.
// *frontier.Store satisfies it.
type Frontier interface {
	Hypothesis(ctx context.Context, id string) (frontier.Hypothesis, error)
	Observation(ctx context.Context, id string) (frontier.Observation, error)
	Finding(ctx context.Context, id string) (frontier.Finding, error)
	Proposal(ctx context.Context, id string) (frontier.Proposal, error)
}

// Runs is the receipt store as the conductor reads it. *run.Store satisfies it.
type Runs interface {
	Revisions(ctx context.Context, runID string) ([]run.Receipt, error)
	Receipts(ctx context.Context, limit, offset int) ([]run.Receipt, int, error)
}

// NewRecordOrigins builds the resolver.
func NewRecordOrigins(front Frontier, runs Runs) *RecordOrigins {
	return &RecordOrigins{frontier: front, runs: runs}
}

// Origin reports the sessions and recipe behind one record.
func (o *RecordOrigins) Origin(ctx context.Context, ref frontier.Ref) (Origin, error) {
	var (
		runID  string
		recipe string
	)
	switch ref.Type {
	case frontier.EntityHypothesis:
		record, err := o.frontier.Hypothesis(ctx, ref.ID)
		if err != nil {
			return Origin{}, err
		}
		runID = record.RunID
	case frontier.EntityObservation:
		record, err := o.frontier.Observation(ctx, ref.ID)
		if err != nil {
			return Origin{}, err
		}
		runID, recipe = record.RunID, record.RecipeID
	case frontier.EntityFinding:
		record, err := o.frontier.Finding(ctx, ref.ID)
		if err != nil {
			return Origin{}, err
		}
		runID = record.RunID
	case frontier.EntityProposal:
		record, err := o.frontier.Proposal(ctx, ref.ID)
		if err != nil {
			return Origin{}, err
		}
		runID = record.RunID
	default:
		return Origin{}, fmt.Errorf("conductor: %q is not a record kind this build resolves", ref.Type)
	}
	if runID == "" {
		return Origin{Recipe: recipe}, nil
	}
	chain, err := o.runs.Revisions(ctx, runID)
	if err != nil {
		// A record whose originating run left no receipt is still a record the
		// operator pointed at. The cycle runs over the whole host rather than
		// refusing their invitation over a gap in Babel's own bookkeeping.
		if errors.Is(err, run.ErrNotFound) {
			return Origin{Recipe: recipe}, nil
		}
		return Origin{}, err
	}
	if len(chain) == 0 {
		return Origin{Recipe: recipe}, nil
	}
	return Origin{
		Sessions: selectorsOf(chain[len(chain)-1].Preparation.Selection),
		Recipe:   recipe,
	}, nil
}

// Describe renders a ladder's queues for `conductor status`, in ladder order.
func Describe(ctx context.Context, ladder []Rung) ([]RungStatus, error) {
	out := make([]RungStatus, 0, len(ladder))
	for _, rung := range ladder {
		depth, err := rung.Depth(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, RungStatus{Name: rung.Name(), Depth: depth})
	}
	return out, nil
}

// RungStatus is one rung's line in a status view.
type RungStatus struct {
	Name  string
	Depth Depth
}

// String renders a rung's queue for a terminal.
func (r RungStatus) String() string {
	if !r.Depth.Implemented {
		return fmt.Sprintf("%s: not implemented — %s", r.Name, r.Depth.Note)
	}
	return fmt.Sprintf("%s: %d — %s", r.Name, r.Depth.Waiting, r.Depth.Note)
}

// TrimNote bounds a note that reaches a durable journal. Notes are composed by
// this package from record identifiers and operator ids, so this is a belt on
// top of the stores' own bounds rather than the only one.
func TrimNote(s string) string {
	const maxNote = 512
	s = strings.TrimSpace(s)
	if len(s) > maxNote {
		return s[:maxNote]
	}
	return s
}
