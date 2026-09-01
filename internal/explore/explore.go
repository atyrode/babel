// Package explore is Babel's exploration control plane: the path SPEC.md §6.5
// describes from an operator's fixed scope selection to durable hypotheses,
// observations, findings, proposals, and a receipt that says what happened.
//
// Babel validates structure, never correctness. Everything in a run's output
// arrives from a worker Babel does not implement and cannot audit from
// outside, so what this package checks is shape and provenance: that a schema
// is one it implements, that an evidence locator recovers its bytes, that a
// recipe reference names an asset the run actually selected, that a finding
// stands on observations that exist, and that a stage did not exceed its
// authority. It never judges whether a claim is true, and no code path here
// reads a worker's confidence as evidence: confidence and impact are stored as
// the model's own gradings (§10 warns they never substitute for evidence) and
// no decision this package makes consults them.
//
// Five rules shape the orchestration, and each is a property a reviewer can
// check rather than a convention this code follows by habit.
//
// Preflight is deterministic and prior to inference (§6.4). It runs before a
// worker is launched, over the same selection the run will read, and its
// verdict is enforced rather than reported: a hosted disclosure class with a
// secret finding refuses to launch unless the run applies §3's redaction,
// because "we told the operator" is not a boundary.
//
// Discovery persists every candidate before anything sorts it (§5.2). The
// persistence loop is deliberately the one loop that does not check for
// cancellation: once an idea has crossed the worker boundary, recording it is
// not work a budget may cut short, so every durable write in this package runs
// on a context detached from the run's cancellation. A finite run then defers
// its remainder to the frontier, which is why cancelling mid-run leaves the
// unexplored candidates queryable instead of erased. Sorting happens
// afterwards, over already-durable records, and belongs to
// frontier.Unexplored rather than here.
//
// The development path is mandatory (§4.2). A worker proposes; Babel writes.
// A result that skips a step — a consolidation whose supporting observations
// do not exist, or one naming a record that is not a locator-backed
// observation — is refused and recorded in the receipt as a failure. It is
// never repaired, because the repair would be Babel inventing the evidence
// step the worker skipped.
//
// The challenger is a logically separate job (§5.4). It gets its own worker
// invocation, its own run identity and its own receipt, and no authority to
// create or promote a finding: a locator-backed objection becomes a
// counter-observation against the hypothesis it attacks, and an objection
// resting on a consequence, a missing check, or an alternative becomes a new
// candidate linked as contradicting its target, since §4.3 forbids an
// observation with no locator. A failed challenger leaves the exploration's
// records exactly as they were — §6.5 requires a failed independent
// exploration not to erase successful work — and only this control plane ever
// applies a promotion transition, after the structured result validates.
//
// Retrieval rank is never evidence strength (§5.4). This package is the
// consumer that could break that rule: it serves the corpus search, records
// the ranked trace, and writes observations in the order the worker emitted
// them. Hit position reaches the receipt as reproduction detail and reaches
// nothing else.
//
// Nothing here publishes. §4.6 and decision 13 put publishing, applying a
// proposal, and writing to a source repository outside Babel entirely, and
// this package has no path to any of them.
package explore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/research"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sync"
	"github.com/atyrode/babel/internal/worker"
)

// Stage is one job in a run. §5.4 makes the challenger and the synthesizer
// logically separate jobs rather than phases of one, so a stage is a unit of
// authority as much as of sequencing: what a result may contain depends on
// which stage produced it.
type Stage string

// The stages of a run. StagePreflight runs no worker: it is named so a
// deterministic failure before inference is attributable in the receipt.
const (
	StagePreflight  Stage = "preflight"
	StageExplore    Stage = "explore"
	StageChallenge  Stage = "challenge"
	StageSynthesize Stage = "synthesize"
)

// Job parameters Babel sets on every analysis job. A worker reads them to know
// which stage it is running and which durable records its brief covers; §4.7's
// durable records are identifiers, which §9's plaintext allowlist admits, so
// naming them in the job discloses nothing the job does not already carry.
const (
	// ParamStage names the stage the job is running.
	ParamStage = "babel.stage"
	// ParamBriefHypotheses, ParamBriefObservations and ParamBriefObjections
	// list, comma-separated, the durable records a challenger or synthesizer
	// is asked to examine. §5.4's challenger pass examines the developed
	// observations, and the synthesizer judges the exploration and the
	// critique together, so both need to be able to name them.
	ParamBriefHypotheses   = "babel.brief.hypotheses"
	ParamBriefObservations = "babel.brief.observations"
	ParamBriefObjections   = "babel.brief.objections"
)

// Versions of the control plane's own inputs, recorded in every receipt
// because §7 makes a run's job, prompt, result-schema, redaction and
// disclosure policy versions part of what a later re-run is compared against.
// They live here rather than in a caller's configuration because this package
// is what implements them: a caller could only misreport them.
const (
	JobVersion              = 1
	PromptVersion           = "babel.analysis-prompt/1"
	RedactionPolicyVersion  = "babel.redaction/1"
	DisclosurePolicyVersion = "babel.disclosure/1"
)

// DefaultFetches is the public-research ceiling a run gets when its operator
// named none. Eight documents is enough for an investigator to read a
// specification section, two upstream changelogs and a handful of references
// while staying a number an operator can review in a receipt; a run that needs
// more says so, per run, which is the same shape §2.6 gives every other
// boundary.
const DefaultFetches = 8

// Failure codes this control plane records in a receipt. §6.5 requires the
// receipt to record failures, and a code is what makes one actionable: an
// operator fixes a redaction-required run by applying redaction and a
// development-path run by reviewing the recipe that produced it.
const (
	FailurePreflight       = "preflight"
	FailureRedaction       = "redaction-required"
	FailureNoRecipe        = "no-recipe"
	FailureWorker          = "worker"
	FailureResultSchema    = "result-schema"
	FailureDevelopmentPath = "development-path"
	FailureAuthority       = "stage-authority"
	FailureUnknownRecipe   = "unknown-recipe"
	FailureUnknownRecord   = "unknown-record"
	FailureDisposition     = "disposition"
	FailureStorage         = "storage"
	FailureCancelled       = "cancelled"
	// FailureFrontierIndex reports that the frontier's own retrieval surface
	// could not be brought up to date. It degrades dedup warnings and the
	// frontier scope of corpus search and nothing else, so it is recorded
	// rather than fatal.
	FailureFrontierIndex = "frontier-index"
	// FailureRelatedContext reports a prior output the preparation named that
	// could not be resolved into the job's refine-first context (#87).
	FailureRelatedContext = "related-context"
	// FailureSyncPublish reports that this run's closure could not be
	// declared for the shared catalog. internal/sync returns an error only
	// for a caller bug — a closure with no staged records, or one already
	// declared at another size — and never for an unreachable backend, so the
	// code names a defect to fix rather than an outage to wait out. Every
	// record stays durable and visibly pending-sync either way.
	FailureSyncPublish = "sync-publish"
)

// ErrRedactionRequired reports a run refused before launch because §6.4's
// preflight found material a hosted disclosure class may not see raw. It is
// enforcement rather than advice: the run does not start, and the operator
// either applies redaction or chooses a local profile.
var ErrRedactionRequired = errors.New("explore: hosted run requires redaction before it may proceed")

// ErrSelectionMismatch reports preflight inputs that are not the preparation's
// selection. Preflight must run over exactly what the run will read; a set
// that merely overlaps it would report a corpus nobody explored.
var ErrSelectionMismatch = errors.New("explore: preflight inputs do not match the preparation's selection")

// Config is everything a controller needs that does not change between
// attempts at one scope.
//
// The worker is a launch template rather than a built *worker.Client, and that
// is deliberate. §6.5 makes tool authorization Babel's own, the policy has to
// see the run it is deciding for — its preparation scope, its retrieval budget,
// its trace — and internal/worker fixes the Authorizer when the client is
// constructed. A client handed in ready-made could only have been configured
// with a policy that knew nothing about the run, so the controller fills in
// Authorizer and OnProgress and constructs one client per job. Each stage is a
// separate process either way (§5.4).
type Config struct {
	// Preparation is the immutable corpus scope (§6.5). Every stage runs
	// over it and every receipt references it.
	Preparation run.Preparation

	// Recipes are the cookbook assets the operator selected. A stage runs
	// the subset declaring that stage, and every recipe is recorded in the
	// receipt whether or not it ran, because §7 asks which policy and lens
	// versions the run applied.
	Recipes *cookbook.Set

	// Grant is the run's capability boundary, fixed before work starts
	// (§2.6). internal/worker enforces it ahead of any policy, so nothing in
	// this package can widen it.
	Grant worker.Grant

	// Profile is the one Code profile the run uses. §2.6 applies one profile
	// to every recipe in a run.
	Profile worker.ProfileRef

	// Worker is the launch template for each stage's process.
	Worker worker.Config

	// Policy narrows the grant further, per operator negotiation. It is
	// consulted before Babel serves anything and can only deny; nil means no
	// narrowing beyond the grant.
	Policy worker.Authorizer

	// Broker locates the evidence API for the job document. §14 defers the
	// evidence-tool and public-research broker protocols, so this is
	// normally empty in this build and the retrieval a run performs is
	// recorded in its receipt's trace rather than streamed to the worker.
	Broker worker.Broker

	// Frontier, Runs and Ledger are the durable stores. All three live in
	// one file (§9's durable, pending-sync state) and are injected open so a
	// caller that already holds them does not open a second handle.
	Frontier *frontier.Store
	Runs     *run.Store
	Ledger   *Ledger

	// Dispositions is the same file's #87 component: the proposed next
	// actions a result carries, and the invitations #96 draws its first
	// ladder rung from. It is required rather than optional because a run
	// whose result proposes an action Babel silently dropped would render
	// nothing for the operator to click and report success.
	Dispositions *disposition.Store

	// Complaints is the operator's own steering input (#115), and it is
	// optional for the reason it exists: a complaint outranks the loop at the
	// invitation rung of #96's ladder and never blocks it, so a run on a
	// machine that has opened no complaint component explores exactly as it
	// did before.
	//
	// A run reads it twice and writes to it never. Its heads join the
	// retrieval index this run reconciles, so a worker's frontier search can
	// find what the operator has been saying; and a preparation that named a
	// complaint as related context is resolved through it, which is how the
	// operator's words reach a job document. Nothing a run produces amends a
	// complaint - the operator is the only author.
	Complaints *complaint.Store

	// Sync declares this run's closure to the shared catalog when the run
	// ends (§6.5, §12 Phase B). The stores above stage each record as they
	// write it, into the closure this run's id names, and none of them can
	// declare it: migration 0003 fixes a closure's record count at
	// declaration and never lets it move, so the count is only right once no
	// further record can arrive, and this control plane is the only thing
	// that knows when that is.
	//
	// Nil is local-only mode, which is a supported deployment: every method
	// of a nil *sync.Publisher is a silent no-op, and New does not require
	// this field. Publication is never a precondition for a run — §6.5 makes
	// it a step that may be completed later — and refusing a run for want of
	// a reachable shared backend would trade durable analysis for a database
	// connection.
	Sync sync.Hook

	// Index is the retrieval index corpus search is served from. It is
	// optional: a run with no index has retrieval denied with that reason
	// rather than answered with nothing.
	Index *index.Index

	// Research is the public-research broker: the facility behind
	// worker.CapabilityPublicResearch and the only path from a run to the
	// network (#75, §2.6).
	//
	// It is required exactly when the grant carries that capability and
	// forbidden when it does not, and New enforces both directions. A
	// granted capability with no broker would deny every fetch after the
	// operator authorized egress, and a broker with no grant would sit there
	// unreachable while the operator believed research was in scope — both
	// are the run silently doing something other than what was authorized,
	// which is the failure mode a capability gate exists to prevent.
	//
	// It is an interface so this package can be tested against a stand-in:
	// SPEC.md §10 requires broker tests to need no network, and the real
	// broker's refusals are proven against real sockets in internal/research.
	// Assign only a non-nil implementation — a nil *research.Broker stored in
	// this field is not a nil interface, and the check below would read it as
	// a facility that is present.
	Research ResearchBroker

	// References mints the typed reference graph's edges for the two link
	// forms only a run knows about: which session a claim's evidence came
	// from, and which injected prior outputs a record grew up beside (#113).
	//
	// Nil is the feature quietly absent, which is a supported deployment on
	// the same terms Sync's absence is: nothing is minted, no write path
	// behaves differently, and no run is refused. The graph is navigation
	// over records that are durable either way, so it is never a
	// precondition for exploring. See reference.go.
	References reference.Appender

	// SessionKey derives the durable session key of one local session, which
	// is what an evidence edge's session endpoint carries (#113). It is
	// injected rather than computed here because the key is deployment- and
	// host-scoped, and the shared catalog's identity algebra is not something
	// a run controller should hold a second copy of.
	//
	// It is a total function on purpose: an emitter must not acquire a second
	// failure mode. An empty result means this build cannot name the session,
	// and the edge is not minted - the observation's locator still recovers
	// the bytes either way. Nil is the same answer, and is what a caller that
	// wired References and nothing else gets.
	SessionKey func(harness, sourceID string) string

	// Presence announces this run to the shared catalog so that a run is
	// visible from every machine in the fleet while it is happening, instead
	// of only once its receipt commits an hour of model work later (#118).
	//
	// Nil is the feature quietly absent, on the same terms Sync and References
	// are: nothing is announced, no write path behaves differently, and no run
	// is refused. That is not a convenience - a presence write may never
	// block, fail or slow a run, so an unreachable catalog leaves the run
	// proceeding exactly as it would have and merely invisible, and
	// internal/presence is built so that this holds without a branch here.
	Presence presence.Announcer

	// Inputs are the sessions preflight checks, one per preparation entry.
	Inputs []preflight.Input

	// Prior is the preparation this run's inputs are compared against, for
	// §6.4's changed and duplicate input checks. Nil for a first
	// preparation.
	Prior *preflight.Preparation

	// Thresholds overrides preflight's calibrated limits.
	Thresholds *preflight.Thresholds

	// Redact applies §3's step 4 to everything Babel serves the worker. It
	// is what a hosted run needs in order to be allowed to start at all when
	// preflight found secrets, and it is also what makes the served excerpts
	// safe to hold in an outcome a caller may render.
	Redact bool

	// Capabilities names the build of each facility that enforced the grant
	// (§7). A granted capability whose facility carries no version is
	// refused here rather than at receipt time, because the run would have
	// been launched by then.
	Capabilities run.CapabilityVersions

	// Now is the clock, injectable so a test's receipts and status history
	// are deterministic. Nil means time.Now.
	Now func() time.Time
}

// Controller runs explorations over one preparation.
//
// It is reusable across attempts on purpose: resuming an interrupted run is
// calling Explore again with the same run identity, and the controller's
// durable state — the frontier, the receipts, the resume ledger — is what makes
// the second attempt recognize the first one's work.
type Controller struct {
	cfg    Config
	assets []run.CookbookAsset
	now    func() time.Time

	// sessions resolves an evidence locator's path to the identity of the
	// session it points into, which the locator itself does not carry. It is
	// derived once because the scope is fixed for the controller's whole life
	// (§2.6); see reference.go.
	sessions map[string]sessionSource
}

// New validates cfg and returns a controller. It performs no I/O and launches
// nothing: every reason a run cannot start that is knowable from
// configuration alone is reported here, before an operator has waited for a
// worker to fail.
func New(cfg Config) (*Controller, error) {
	if err := cfg.Preparation.Verify(); err != nil {
		return nil, err
	}
	if cfg.Recipes == nil || len(cfg.Recipes.All()) == 0 {
		return nil, fmt.Errorf("explore: no cookbook recipes selected")
	}
	if cfg.Frontier == nil || cfg.Runs == nil || cfg.Ledger == nil || cfg.Dispositions == nil {
		return nil, fmt.Errorf("explore: the frontier, receipt, disposition and ledger stores are all required")
	}
	if cfg.Worker.Binary == "" {
		return nil, fmt.Errorf("explore: no analysis worker binary configured")
	}
	switch cfg.Grant.Disclosure {
	case worker.DisclosureLocal, worker.DisclosureHosted:
	default:
		return nil, fmt.Errorf("explore: unknown disclosure class %q", cfg.Grant.Disclosure)
	}
	if err := validateFacilities(cfg.Grant, cfg.Capabilities); err != nil {
		return nil, err
	}
	if err := validateResearchGrant(cfg.Grant, cfg.Research); err != nil {
		return nil, err
	}
	if err := validateSelection(cfg.Preparation, cfg.Inputs); err != nil {
		return nil, err
	}
	assets, err := cookbookAssets(cfg.Recipes)
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Controller{
		cfg:      cfg,
		assets:   assets,
		now:      func() time.Time { return now().UTC() },
		sessions: sessionSources(cfg.Inputs),
	}, nil
}

// validateFacilities refuses a grant whose facilities carry no version. The
// receipt would refuse it later; refusing it now means a containment question
// cannot become unanswerable after the fact.
func validateFacilities(grant worker.Grant, versions run.CapabilityVersions) error {
	if len(grant.Capabilities) > 0 && versions.Tool == "" {
		return fmt.Errorf("explore: granted capabilities without a tool capability version")
	}
	for _, c := range grant.Capabilities {
		var version string
		switch c {
		case worker.CapabilityCorpusSearch:
			continue
		case worker.CapabilitySandboxExec:
			version = versions.Sandbox
		case worker.CapabilityRepoRead:
			version = versions.Repository
		case worker.CapabilityPublicResearch:
			version = versions.PublicResearch
		default:
			return fmt.Errorf("explore: unknown capability %q in the grant", c)
		}
		if version == "" {
			return fmt.Errorf("explore: capability %q granted without its facility version", c)
		}
	}
	return nil
}

// ResearchBroker is the public-research facility as this package uses it: the
// run's fixed source set, and one fetch by opaque identifier.
//
// It is declared here rather than in internal/research because it is the
// consumer's view. Everything that makes the facility safe — the refusals, the
// address policy, the redirect ceiling — belongs to the implementation and is
// not this package's to restate; what this package needs is the two calls it
// makes and the provenance it records.
type ResearchBroker interface {
	// Catalog is what this run may reach.
	Catalog() research.Catalog
	// Fetch reads one fixed source by the identifier the catalog gave it.
	Fetch(ctx context.Context, id string) (research.Document, error)
}

// validateResearchGrant refuses a run whose research grant and research broker
// disagree in either direction. See Config.Research for why both are refusals
// rather than one being a tolerable default.
func validateResearchGrant(grant worker.Grant, broker ResearchBroker) error {
	granted := grant.Allows(worker.CapabilityPublicResearch)
	switch {
	case granted && broker == nil:
		return fmt.Errorf("explore: public research is granted with no broker behind it")
	case !granted && broker != nil:
		return fmt.Errorf("explore: a public-research broker was supplied for a run that does not grant it")
	}
	return nil
}

// validateSelection requires preflight to cover exactly the preparation.
func validateSelection(prep run.Preparation, inputs []preflight.Input) error {
	want := make(map[string]struct{}, len(prep.Selection))
	for _, sel := range prep.Selection {
		want[sel.Harness+"/"+sel.SourceID] = struct{}{}
	}
	got := make(map[string]struct{}, len(inputs))
	for _, in := range inputs {
		key := in.Stream.Harness + "/" + in.Stream.SourceID
		if _, ok := want[key]; !ok {
			return fmt.Errorf("%w: %s is not in the preparation", ErrSelectionMismatch, key)
		}
		got[key] = struct{}{}
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			return fmt.Errorf("%w: %s has no preflight input", ErrSelectionMismatch, key)
		}
	}
	return nil
}

// cookbookAssets renders the selected cookbook for the receipt.
func cookbookAssets(set *cookbook.Set) ([]run.CookbookAsset, error) {
	assets := make([]run.CookbookAsset, 0, len(set.All()))
	for _, recipe := range set.All() {
		var kind string
		switch recipe.Kind {
		case cookbook.KindPolicy:
			kind = run.AssetPolicy
		case cookbook.KindLens:
			kind = run.AssetLens
		case cookbook.KindMeta:
			kind = run.AssetMeta
		default:
			return nil, fmt.Errorf("explore: recipe %q has unknown kind %q", recipe.ID, recipe.Kind)
		}
		assets = append(assets, run.CookbookAsset{
			Kind: kind,
			Ref:  worker.RecipeRef{ID: recipe.ID, Version: recipe.Version},
		})
	}
	return assets, nil
}

// Budget bounds what one pass does, never what may exist. §5.2 is explicit
// that resource limits choose what is explored now, not which ideas are
// permitted: a candidate past the budget is persisted and deferred, and the
// resumed run finds it on the frontier.
type Budget struct {
	// Develop caps the candidates whose observations this pass writes. Zero
	// develops everything the worker emitted.
	Develop int
	// Retrievals caps the corpus searches the run serves. Zero is unbounded
	// within the worker's own tool-request limit.
	Retrievals int
	// Fetches caps the public documents the run's research broker fetches.
	// Zero is DefaultFetches rather than unbounded, which is the one place
	// this type treats absence as a number: a corpus search reads a local
	// index, and every fetch is an observable external effect against
	// someone else's host (§1), so a run whose operator named no ceiling
	// gets the conservative one instead of none.
	Fetches int
}

// RecordEvent reports one durable record as it is written, so an operator's
// interface can show the frontier growing while a run is in flight (§2.6 keeps
// Babel's own interface responsive). It runs on the run's goroutine and must
// not block.
type RecordEvent struct {
	Stage Stage
	Type  frontier.EntityType
	// Ref is the reference the worker emitted the item under, empty for
	// records Babel derived rather than received.
	Ref string
	ID  string
	// Reused reports a record recognized from a prior attempt at this run
	// rather than written now, which is what resumption looks like from the
	// outside.
	Reused bool
}

// Options is one attempt at an exploration.
type Options struct {
	// RunID identifies the exploration. Calling Explore again with the same
	// RunID resumes it: committed records are recognized through the resume
	// ledger rather than written a second time, and the receipt is amended
	// rather than replaced (§6.5, §7).
	RunID string

	// Authority is why this run is happening (#96): the operator command or
	// invitation, the standing policy, or the declared serendipity draw that
	// authorized it. It is required, because "no nameable authority, no run"
	// applies to the caller that summons an exploration as much as to the
	// loop that schedules one, and it reaches durable state only through the
	// receipt: nothing about scheduling may change what a run is allowed to
	// do, so no grant, no policy version and no frontier transition reads it.
	Authority run.Authority

	// Roots and Prior are the run's starting position on the durable
	// frontier (§5.2). Both may be empty: broad discovery starts from no
	// root, and recording that is a different statement from recording roots
	// nobody selected.
	Roots []string
	Prior []string

	// Challenge and Synthesize request §5.4's separate passes. Each is its
	// own job, its own worker process and its own receipt.
	Challenge  bool
	Synthesize bool

	Budget Budget

	// Params are extra job parameters merged into every stage's job, after
	// the parameters this package owns.
	Params map[string]string

	OnRecord   func(RecordEvent)
	OnProgress func(Stage, worker.ProgressRecord)
}

// Outcome is what one attempt did. It is returned even when the attempt
// failed, on the same reasoning as internal/worker's receipt: the record of a
// failed run is exactly when the record is needed.
type Outcome struct {
	RunID string

	// Preflight is §6.4's deterministic report. It is not written to the
	// frontier: a preflight finding develops no hypothesis, and inventing
	// one to satisfy §4.2's path would fabricate a candidate no investigator
	// proposed.
	Preflight *preflight.Report

	// Receipt is the exploration's receipt; Challenge and Synthesis are the
	// separate passes' own. Each embeds exactly one worker boundary, which
	// is why a logically separate job gets a separate receipt rather than
	// being blended into one that could not say which boundary failed.
	Receipt   *run.Receipt
	Challenge *run.Receipt
	Synthesis *run.Receipt

	// The durable records this attempt committed, in write order.
	Hypotheses   []string
	Observations []string
	Findings     []string
	Proposals    []string
	Promoted     []string
	Deferred     []string
	Rejected     []string
	// Objections are the challenger's criticisms as they were recorded:
	// counter-observations where they carried locators, contradicting
	// candidates where they did not.
	Objections []string

	// Dispositions are the proposed next actions the run recorded against
	// those records (#87). They are proposals waiting for an operator's
	// click, so a run that emitted them changed nothing outside the store.
	Dispositions []string

	// Reused counts records recognized from a prior attempt rather than
	// written again — the number that would be duplicates if resumption did
	// not work.
	Reused int

	// Duplicates are the near-duplicate warnings this attempt recorded
	// against the candidates it wrote (#87 item 4). They are reported
	// because a warning nobody surfaces is a warning nobody answers: the
	// candidates themselves were all persisted, and this is the list of the
	// ones an operator should compare against something that already exists.
	Duplicates []frontier.DuplicateWarning

	// Retrieval is what the run's corpus search served, in service order.
	Retrieval []Retrieval

	// Failures mirrors what the receipts record, so a caller does not have
	// to open a receipt body to learn the run degraded.
	Failures []run.Failure

	// Cancelled reports that the run stopped because its context was
	// cancelled. Everything emitted before that point is durable.
	Cancelled bool
}

// state is one attempt's working set.
type state struct {
	ctx    context.Context
	commit context.Context
	opt    Options
	out    *Outcome

	// hypotheses and observations resolve a reference — a ref this run's
	// results emitted, or a durable identifier a brief listed — to a durable
	// record.
	hypotheses   map[string]string
	observations map[string]string

	// touched lists every hypothesis this attempt created or reused, in
	// order, which is the set a finite run defers its remainder from.
	touched []string
	seen    map[string]bool

	// promoted and rejected are the lifecycle transitions this attempt has
	// already applied, so a second consolidation over the same candidate
	// does not append a duplicate transition.
	promoted map[string]bool
	rejected map[string]bool

	// deferReasons is the reason a candidate was left undeveloped, keyed by
	// durable identifier: the worker's own reason where it gave one, and the
	// budget's where the budget decided.
	deferReasons map[string]string

	// undeveloped names the observation references this pass's budget left
	// unwritten, so a consolidation over them waits for the resumed run
	// instead of being reported as an unresolvable result.
	undeveloped map[string]bool
	// deferredRecords and rejectedRecords are the receipt's account of the
	// candidates this finite run did not develop (§6.5).
	deferredRecords []run.Candidate
	rejectedRecords []run.Candidate

	// written names the stages whose own receipt was stored, so a stage that
	// never got one does not lose its failures: the exploration's receipt
	// records them instead.
	written map[Stage]bool

	// injected names the prior outputs the refine-first context actually
	// carried into this attempt's jobs, deduplicated, in resolution order
	// (#87 item 4). It is what the inspired-by edges of #113 point at, and
	// it holds what was injected rather than what the preparation named: a
	// reference that no longer resolves reaches no worker, so recording an
	// edge to it would assert an injection that did not happen.
	injected     []reference.RecordRef
	injectedSeen map[string]bool

	// failures are partitioned by stage, because each stage writes its own
	// receipt and a challenger's failure recorded in the exploration's
	// receipt would misattribute which boundary degraded.
	failures map[Stage][]run.Failure
	err      error
}

func (s *state) note(h string) {
	if !s.seen[h] {
		s.seen[h] = true
		s.touched = append(s.touched, h)
	}
}

// fail records one control-plane failure and remembers the first error, so
// Explore can report that the run degraded without losing the receipt.
func (s *state) fail(stage Stage, code string, at time.Time, err error) {
	s.failures[stage] = append(s.failures[stage], run.Failure{
		Stage:   string(stage),
		Code:    code,
		Message: err.Error(),
		At:      at,
	})
	if s.err == nil {
		s.err = err
	}
}

// warn records one degradation that is not the run's verdict.
//
// It is fail without the memory: the failure reaches the receipt and the
// outcome, and st.err is left alone, so the run's own success or failure is
// decided by the analysis rather than by a facility beside it. §6.5 already
// draws that line for publication - a closure that failed to publish is a
// recorded failure and never the run's outcome - and #113's edges sit on the
// same side of it: a shadow of a durable record.
func (s *state) warn(stage Stage, code string, at time.Time, err error) {
	s.failures[stage] = append(s.failures[stage], run.Failure{
		Stage:   string(stage),
		Code:    code,
		Message: err.Error(),
		At:      at,
	})
}

// inject records one prior output the refine-first context resolved, so the
// same record injected into two stages is one endpoint rather than two.
func (s *state) inject(ref reference.RecordRef) {
	key := ref.String()
	if s.injectedSeen[key] {
		return
	}
	if s.injectedSeen == nil {
		s.injectedSeen = map[string]bool{}
	}
	s.injectedSeen[key] = true
	s.injected = append(s.injected, ref)
}

// failuresFor collects the failures the named stages recorded, in stage order.
func (s *state) failuresFor(stages ...Stage) []run.Failure {
	var out []run.Failure
	for _, stage := range stages {
		out = append(out, s.failures[stage]...)
	}
	return out
}

// allFailures flattens every stage's failures for the outcome.
func (s *state) allFailures() []run.Failure {
	return s.failuresFor(StagePreflight, StageExplore, StageChallenge, StageSynthesize)
}

// record accounts for one durable record this attempt reached, whether it was
// written now or recognized from a prior attempt.
//
// It is the controller's rather than the state's because it is the one funnel
// every record kind passes through, which makes it the place the inspired-by
// edges of #113 belong: a new emission site for a new record kind would
// otherwise be a site that forgets them.
func (c *Controller) record(st *state, e RecordEvent) {
	if e.Reused {
		st.out.Reused++
	}
	c.mintInspiredBy(st, e.Stage, e.Type, e.ID)
	if st.opt.OnRecord != nil {
		st.opt.OnRecord(e)
	}
}

// Explore runs one attempt: preflight, discovery and development, then §5.4's
// challenger and synthesizer when they are asked for, then the receipts.
//
// The returned Outcome is never nil once the attempt started. An error means
// the attempt degraded — it was cancelled, a worker failed, or a structured
// result was refused — and the receipts say which.
func (c *Controller) Explore(ctx context.Context, opt Options) (*Outcome, error) {
	if opt.RunID == "" {
		return nil, fmt.Errorf("explore: a run id is required, because resuming one is naming it again")
	}
	// Refused here rather than at the receipt, so a run nobody can account for
	// never reaches a worker: the receipt would refuse it afterwards, and by
	// then the model spend and whatever the run wrote have already happened.
	if !opt.Authority.Recorded() {
		return nil, fmt.Errorf("explore: a run records why it happened; no nameable authority, no run")
	}
	started := c.now()
	st := &state{
		// Cancellation stops exploration; it never stops recording what
		// exploration already produced. §5.2 requires every emitted
		// candidate to be persisted, so every durable write below runs on a
		// context detached from the run's.
		ctx:          ctx,
		commit:       context.WithoutCancel(ctx),
		opt:          opt,
		out:          &Outcome{RunID: opt.RunID},
		hypotheses:   map[string]string{},
		observations: map[string]string{},
		seen:         map[string]bool{},
		promoted:     map[string]bool{},
		rejected:     map[string]bool{},
		deferReasons: map[string]string{},
		undeveloped:  map[string]bool{},
		written:      map[Stage]bool{},
		failures:     map[Stage][]run.Failure{},
	}

	// The run becomes visible to the fleet before the first worker starts and
	// stops being claimed as running the moment this call returns, whichever
	// way it returns - so the deferred finalize is the whole reason this is
	// structured as a defer rather than a call at each exit. A refused
	// preflight returns early below, and a run that announced itself and never
	// finalized would sit on every other machine's fleet view going stale, for
	// a run that ended cleanly in a fraction of a second.
	//
	// The heartbeat runs for the whole attempt rather than only inside worker
	// supervision, because that is where the time goes and because the
	// stages are not the only slow part: persisting a large result and
	// declaring a closure both take long enough to matter at these thresholds.
	presenceID := c.announce(st)
	stopBeat := presence.Beat(ctx, c.cfg.Presence, presenceID)
	defer func() {
		// Stopped before finalizing so the last thing the fleet hears about
		// this run is how it ended, not a heartbeat that raced past it.
		stopBeat()
		c.finalize(st, presenceID)
	}()

	// The report is part of the outcome whether or not it let the run
	// proceed: an operator who has just been refused needs to read the
	// findings that refused them.
	report, err := c.runPreflight(st, started)
	st.out.Preflight = report
	if err != nil {
		st.out.Receipt = c.writeReceipt(st, opt.RunID, nil, nil,
			st.failuresFor(StagePreflight, StageExplore), started)
		// A refused run publishes too: the refusal's receipt is a durable
		// record, and the closure naming it has to be declared by somebody.
		c.publishRun(st)
		st.out.Failures = st.allFailures()
		return st.out, err
	}

	// The frontier's own retrieval surface is brought up to date once, after
	// preflight has cleared the run and before any job reads it, so the
	// refine-first context, the dedup warnings and the frontier scope of
	// corpus search all answer questions about the same frontier (#87).
	c.refreshFrontier(st)

	exploration := c.runStage(st, StageExplore, opt.RunID, nil)
	if exploration != nil && exploration.result != nil {
		c.persist(st, StageExplore, opt.RunID, exploration.result)
	}

	if st.ctx.Err() != nil {
		st.out.Cancelled = true
		st.fail(StageExplore, FailureCancelled, c.now(),
			fmt.Errorf("explore: run cancelled: %w", context.Cause(st.ctx)))
	}

	// §5.4's passes are skipped after cancellation rather than attempted and
	// killed: the exploration's records are already durable and a half-run
	// challenger would add nothing but a failure.
	if !st.out.Cancelled && opt.Challenge {
		st.out.Challenge = c.runSeparateJob(st, StageChallenge)
	}
	if !st.out.Cancelled && opt.Synthesize {
		st.out.Synthesis = c.runSeparateJob(st, StageSynthesize)
	}

	c.deferRemainder(st)

	var (
		workerReceipt *worker.Receipt
		steps         []run.RetrievalStep
	)
	if exploration != nil {
		workerReceipt, steps = exploration.receipt, exploration.steps
	}
	st.out.Receipt = c.writeReceipt(st, opt.RunID, workerReceipt, steps, c.runFailures(st), started)
	// The run's own verdict is fixed before publication is attempted. A
	// closure that failed to publish is a recorded failure and never the
	// run's outcome: §6.5 makes publication a step that may be completed
	// later, so whether a catalog was reachable must not decide whether an
	// exploration succeeded.
	outcomeErr := st.err
	c.publishRun(st)
	st.out.Failures = st.allFailures()
	return st.out, outcomeErr
}

// runPreflight runs §6.4 and enforces its disclosure verdict.
func (c *Controller) runPreflight(st *state, at time.Time) (*preflight.Report, error) {
	report, err := preflight.Check(preflight.Request{
		Profile:    c.cfg.Profile,
		Disclosure: c.cfg.Grant.Disclosure,
		Inputs:     c.cfg.Inputs,
		Prior:      c.cfg.Prior,
		Thresholds: c.cfg.Thresholds,
	})
	if err != nil {
		wrapped := fmt.Errorf("explore: preflight: %w", err)
		st.fail(StagePreflight, FailurePreflight, at, wrapped)
		return nil, wrapped
	}
	if report.Disclosure.RedactionRequired && !c.cfg.Redact {
		wrapped := fmt.Errorf("%w: %d finding(s) force redaction under disclosure class %q",
			ErrRedactionRequired, len(report.Disclosure.Forcing), c.cfg.Grant.Disclosure)
		st.fail(StagePreflight, FailureRedaction, at, wrapped)
		return report, wrapped
	}
	return report, nil
}

// thresholds are the preflight limits the run applies, so redaction and the
// report it accompanies are produced by the same rules.
func (c *Controller) thresholds() preflight.Thresholds {
	if c.cfg.Thresholds != nil {
		return *c.cfg.Thresholds
	}
	return preflight.DefaultThresholds()
}

// fetchBudget is the public-research ceiling one stage runs under. Absence is
// DefaultFetches rather than "unbounded" — see Budget.Fetches — and it is
// resolved here rather than in the broker so that the number the stage
// enforced is the number this package documents.
//
// It is per stage, deliberately. The challenger and the synthesizer are
// logically separate jobs (§5.4), and a challenger that could not check a
// source because the exploration had spent the last fetch would be criticizing
// a conclusion it was denied the evidence to test.
func (c *Controller) fetchBudget(st *state) int {
	if st.opt.Budget.Fetches > 0 {
		return st.opt.Budget.Fetches
	}
	return DefaultFetches
}

// runFailures are the failures the exploration's receipt records: its own,
// preflight's, and any separate pass that never got a receipt of its own —
// because a failure recorded nowhere is a failure the run does not have.
func (c *Controller) runFailures(st *state) []run.Failure {
	stages := []Stage{StagePreflight, StageExplore}
	for _, stage := range []Stage{StageChallenge, StageSynthesize} {
		if !st.written[stage] {
			stages = append(stages, stage)
		}
	}
	return st.failuresFor(stages...)
}

// publishRun declares this run's closure and attempts to publish it.
//
// The records a run produces are staged into its closure by the stores that
// write them, publishing nothing, because a closure may not be declared while
// it can still grow. Ending the run is what completes it, and no store can
// observe that: a frontier record and a receipt look identical whether the run
// is over or merely between writes. So this call is the one place a run's
// closure is declared, and every record the run staged publishes together or
// stays visibly pending together.
//
// It runs on st.commit, the context detached from the run's cancellation, for
// exactly the reason every other durable write in this package does: once
// output exists, recording and publishing it is not work a budget may cut
// short. A cancelled run has candidates and a receipt; declaring their closure
// on the cancelled context would fail on its first query, and a cancellation
// would then cost the fleet the work rather than only the remainder.
//
// A refused or failed run publishes too. Its receipt is a durable record, §6.5
// requires a failed exploration not to erase successful work, and a run whose
// closure is never declared leaves its records staged and visibly pending
// forever with nobody left to complete them. That is the failure this call
// prevents, not one it risks.
//
// Only a returned error is recorded, because internal/sync documents that as a
// caller bug rather than a transient condition: an unreachable catalog reports
// its own single diagnostic line, leaves every record durable and pending-sync,
// and returns nil, so there is nothing here to record and nothing to fail. The
// failure reaches the outcome rather than the receipt, and it cannot reach the
// receipt: the receipt is the record this publication was declaring, so a
// failure to publish it can never also be inside it. It is attributed to
// StageExplore on the same reasoning writeReceipt's storage failures are —
// this control plane degraded, not a worker boundary.
//
// It is recorded with warn rather than fail because §6.5 keeps publication out
// of the run's verdict: a run that produced everything it was asked for did not
// fail because a catalog was unreachable. Explore's returned error is
// snapshotted before this call either way, so the distinction was invisible
// until #118 read the run's verdict again afterwards to finalize its presence
// row - and a fleet row reading "failed" for a complete run would be worse than
// no row at all.
func (c *Controller) publishRun(st *state) {
	if c.cfg.Sync == nil {
		return
	}
	if err := c.cfg.Sync.CommitInline(st.commit, sync.Closure{RunID: st.opt.RunID}); err != nil {
		st.warn(StageExplore, FailureSyncPublish, c.now(),
			fmt.Errorf("explore: declare the shared-catalog closure for run %s: %w", st.opt.RunID, err))
	}
}
