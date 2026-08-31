package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/review"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

const exploreUsage = `Usage: babel explore --preparation ID [flags]

Runs one exploration over a fixed corpus scope (SPEC.md §6.5). The scope is
the preparation named on the command line, so what the run read is stated
rather than implied; "babel prepare" emits one.

Analysis itself happens inside Code. Babel launches an executable speaking
the babel.analysis-worker protocol, authorizes every tool request it makes,
and records what it produced; it never chooses a provider or a model
(SPEC.md §2.6). Without a worker this command refuses to start and says so.

Progress goes to stderr while the run is in flight; the receipt identity is
reported at the end. Interrupting the run leaves everything it already
committed durable, and re-running with the same --run-id resumes rather than
duplicating it.

Nothing here publishes: no issue is opened, no source repository is written,
and no proposal is applied (SPEC.md §4.6, decision 13).

Flags:
  --preparation ID     required: the corpus scope to explore
  --recipe ID          cookbook recipe to run; repeatable. Naming one runs
                       exactly it, default-enabled or not
                       (default: the default-enabled lenses)
  --challenge          run §5.4's independent challenger pass
  --synthesize         run §5.4's synthesis pass
  --root ID            start from this existing candidate; repeatable
  --roots DIR[,DIR]    rediscover the preparation's sessions under these
                       roots instead of the adapter defaults
  --run-id ID          resume this run instead of starting a new one
  --develop N          cap the candidates developed in this pass
  --retrievals N       cap the corpus searches served
  --profile REF        Code profile reference "ID" or "ID@REVISION"
                       (default: the one "analysis profile configure" stored)
  --worker PATH        Code executable speaking babel.analysis-worker
                       (default $BABEL_ANALYSIS_WORKER, else the stored one)
  --worker-arg ARG     extra argument for the worker; repeatable
  --json               emit the outcome as JSON on stdout
`

// exploreProgressInterval throttles the stderr narration of a run. A run is
// minutes long, so it must never be silent; it must not scroll one line per
// worker heartbeat either.
const exploreProgressInterval = time.Second

// failureRow is one control-plane failure in machine-readable output.
type failureRow struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
	At      string `json:"at"`
}

// preflightRow is §6.4's deterministic verdict, summarized. The findings
// themselves are not restated here: a preflight finding names a locator into
// the corpus, and the place to read one is the report, not a run summary.
type preflightRow struct {
	Version           int    `json:"version"`
	Disclosure        string `json:"disclosure"`
	RedactionRequired bool   `json:"redaction_required"`
	Inputs            int    `json:"inputs"`
	Bytes             int64  `json:"bytes"`
	Events            int    `json:"events"`
	SecretFindings    int    `json:"secret_findings"`
	Findings          int    `json:"findings"`
}

// exploreResult is `babel explore --json`.
type exploreResult struct {
	RunID              string        `json:"run_id"`
	PreparationID      string        `json:"preparation_id"`
	ReceiptID          string        `json:"receipt_id"`
	ChallengeReceiptID string        `json:"challenge_receipt_id,omitempty"`
	SynthesisReceiptID string        `json:"synthesis_receipt_id,omitempty"`
	Profile            string        `json:"profile"`
	Recipes            []string      `json:"recipes"`
	Hypotheses         []string      `json:"hypotheses"`
	Observations       []string      `json:"observations"`
	Findings           []string      `json:"findings"`
	Proposals          []string      `json:"proposals"`
	Promoted           []string      `json:"promoted"`
	Deferred           []string      `json:"deferred"`
	Rejected           []string      `json:"rejected"`
	Objections         []string      `json:"objections"`
	Reused             int           `json:"reused"`
	Retrievals         int           `json:"retrievals"`
	Enrolled           int           `json:"enrolled_for_review"`
	Cancelled          bool          `json:"cancelled"`
	Preflight          *preflightRow `json:"preflight,omitempty"`
	Failures           []failureRow  `json:"failures,omitempty"`
}

// explore implements `babel explore`.
func (a *app) explore(ctx context.Context, args []string) error {
	c := newCmd("explore", exploreUsage)
	var wf workerFlags
	var recipes, roots repeatedFlag
	var sf scanFlags
	wf.bind(c.fs)
	preparation := c.fs.String("preparation", "", "the corpus scope to explore")
	c.fs.Var(&recipes, "recipe", "cookbook recipe to run; repeatable")
	c.fs.Var(&roots, "root", "existing candidate to start from; repeatable")
	sf.bindRoots(c)
	challenge := c.fs.Bool("challenge", false, "run the independent challenger pass")
	synthesize := c.fs.Bool("synthesize", false, "run the synthesis pass")
	runID := c.fs.String("run-id", "", "resume this run instead of starting a new one")
	develop := c.fs.Int("develop", 0, "cap the candidates developed in this pass")
	retrievals := c.fs.Int("retrievals", 0, "cap the corpus searches served")
	profile := c.fs.String("profile", "", `Code profile reference "ID" or "ID@REVISION"`)
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *preparation == "" {
		return c.usagef("explore requires --preparation ID; run \"babel prepare\" to fix a corpus scope")
	}

	settings, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	wcfg, ok := wf.resolve(settings)
	if !ok {
		return a.reportNoWorker()
	}
	profileRef, err := resolveProfile(c, *profile, settings)
	if err != nil {
		return err
	}
	set, err := selectRecipes(c, recipes)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	defer state.Close()

	prep, err := state.runs.Preparation(ctx, runstore.PreparationID(*preparation))
	if err != nil {
		if errors.Is(err, runstore.ErrNotFound) {
			return fmt.Errorf("no preparation %q is recorded; run \"babel prepare\" to fix a corpus scope", *preparation)
		}
		return fmt.Errorf("read preparation %s: %w", *preparation, err)
	}

	inputs, err := a.preflightInputs(ctx, prep, sf.rootList())
	if err != nil {
		return err
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	idx, err := index.Open(d.indexDir())
	if err != nil {
		return err
	}
	defer idx.Close()
	ledger, err := explore.OpenLedger(state.dir)
	if err != nil {
		return err
	}
	defer ledger.Close()

	wcfg.Diagnostics = &sanitizingWriter{w: a.stderr, prefix: "worker: "}
	controller, err := explore.New(explore.Config{
		Preparation: prep,
		Recipes:     set,
		// Corpus search is the one facility this build brokers: §14 defers
		// the evidence-tool and public-research broker protocols, and a
		// sandbox or repository grant would name a facility with no version
		// to record. Granting only what Babel can actually serve is what
		// keeps the receipt's containment answer true.
		Grant: worker.Grant{
			Capabilities: []worker.Capability{worker.CapabilityCorpusSearch},
			Disclosure:   worker.DisclosureLocal,
		},
		Profile:      profileRef,
		Worker:       wcfg,
		Frontier:     state.frontier,
		Runs:         state.runs,
		Ledger:       ledger,
		Dispositions: state.dispositions,
		Index:        idx,
		Inputs:       inputs,
		Capabilities: runstore.CapabilityVersions{Tool: "babel/" + readBuildIdentity().Version},
	})
	if err != nil {
		return err
	}

	id := *runID
	if id == "" {
		id = newRunID()
	}
	// A run is interruptible on purpose: §5.2 defers the unexplored frontier
	// rather than erasing it, so Ctrl-C stops the pass and leaves everything
	// already committed queryable.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	a.diagf("exploring %s over %d %s as run %s...\n",
		Sanitize(string(prep.ID)), len(prep.Selection),
		plural(len(prep.Selection), "session", "sessions"), Sanitize(id))
	reporter := &exploreReporter{app: a, last: time.Now()}
	outcome, runErr := controller.Explore(ctx, explore.Options{
		RunID:      id,
		Roots:      roots,
		Challenge:  *challenge,
		Synthesize: *synthesize,
		Budget:     explore.Budget{Develop: *develop, Retrievals: *retrievals},
		OnRecord:   reporter.record,
		OnProgress: reporter.progress,
	})
	if outcome == nil {
		return a.reportWorkerFailure(wcfg.Binary, runErr)
	}

	// Enrolment is the wiring step §6.7 needs and internal/explore does not
	// perform: internal/frontier exposes no enumeration, so a produced
	// record that is never enrolled is invisible to the review queue. It
	// runs even for a failed run, because what a degraded run did produce is
	// exactly what a reviewer has to look at.
	enrolled := a.enrol(ctx, state.review, outcome)

	res := exploreOutcome(prep, profileRef, set, outcome, enrolled)
	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else {
		a.writeExplore(res)
	}
	if runErr != nil {
		return a.reportWorkerFailure(wcfg.Binary, runErr)
	}
	return nil
}

// idList renders a set of record identifiers for a document whose consumer
// indexes it. It is sanitizeAll's counterpart for a field that is always
// present: an empty run produced no hypotheses, and a script iterating the
// list should see an empty list rather than a null it has to special-case.
func idList(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, Sanitize(id))
	}
	return out
}

// newRunID mints a run identity. It is derived from the clock rather than
// randomly so that a listing of receipts sorts in the order the runs
// happened, and it is prefixed so a stray identifier in a log says what it
// is.
func newRunID() string {
	return "run-" + time.Now().UTC().Format("20060102T150405Z")
}

// resolveProfile fixes the Code profile reference a run applies. §2.6 applies
// one profile to every recipe in a run, so this is one value and not a set.
func resolveProfile(c *cmd, flagValue string, s analysisSettings) (worker.ProfileRef, error) {
	if flagValue == "" {
		if s.Profile == nil {
			return worker.ProfileRef{}, c.usagef(
				"no Code analysis profile is stored and none was given; run \"babel analysis profile configure\" or pass --profile ID@REVISION")
		}
		return s.Profile.ref(), nil
	}
	id, revision, ok := strings.Cut(flagValue, "@")
	if id == "" {
		return worker.ProfileRef{}, c.usagef("--profile needs a profile id")
	}
	if !ok {
		return worker.ProfileRef{ID: id, Revision: 1}, nil
	}
	n, err := strconv.Atoi(revision)
	if err != nil || n < 1 {
		return worker.ProfileRef{}, c.usagef("--profile revision %q is not a positive integer", revision)
	}
	return worker.ProfileRef{ID: id, Revision: n}, nil
}

// selectRecipes narrows the embedded cookbook to the named recipes, or to
// the default-enabled lenses when none were named.
//
// Narrowing is the whole point of the function and not a convenience: the
// returned Set is what the run's receipt attests to and what each stage
// runs, so a Set wider than the operator's request would make the receipt
// claim recipes that never looked at the corpus.
//
// An explicitly named recipe always runs, default-enabled or not. §5.5 ships
// three lenses as reviewable drafts, and default-enablement answers "what
// should a run do when the operator said nothing", not "what may a run do".
// Intersecting an explicit --recipe with the defaults would be this same bug
// wearing a quieter costume: the operator naming a draft by id is the
// authorization to run it.
func selectRecipes(c *cmd, chosen []string) (*cookbook.Set, error) {
	full, err := cookbook.Embedded()
	if err != nil {
		return nil, err
	}
	ids := chosen
	if len(ids) == 0 {
		defaults := full.Defaults()
		// A build whose cookbook default-enables nothing has no implicit
		// selection, and running every recipe instead would be a scope the
		// operator never asked for. Refuse, and name the flag that makes
		// the choice explicit.
		if len(defaults) == 0 {
			return nil, errors.New(
				"this build's cookbook default-enables no recipe; name one with --recipe ID (\"babel cookbook list\" shows every id)")
		}
		ids = make([]string, 0, len(defaults))
		for _, r := range defaults {
			ids = append(ids, r.ID)
		}
	}
	set, err := full.Select(ids)
	if err != nil {
		var unknown *cookbook.UnknownRecipeError
		if errors.As(err, &unknown) {
			return nil, c.usagef("unknown --recipe %q; the cookbook holds %s",
				unknown.ID, strings.Join(unknown.Available, " "))
		}
		return nil, err
	}
	return set, nil
}

// preflightInputs reconstructs §6.4's inputs for a recorded scope.
//
// A preparation records identity and digests, never a path: the same scope
// is legitimately explored on a machine whose files sit elsewhere. Explore
// therefore rediscovers each selected session locally, and a session the
// preparation names that this machine cannot see is an error naming it
// rather than a quietly smaller run.
func (a *app) preflightInputs(ctx context.Context, prep runstore.Preparation, roots []string) ([]preflight.Input, error) {
	sessions, _ := a.scan(ctx, adapters(), roots)
	byKey := make(map[string]localSession, len(sessions))
	for _, s := range sessions {
		byKey[s.key()] = s
	}
	inputs := make([]preflight.Input, 0, len(prep.Selection))
	for _, sel := range prep.Selection {
		key := sel.Harness + "/" + sel.SourceID
		s, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("preparation %s names session %s, which this machine cannot see; fetch it or prepare a scope this host holds",
				prep.ID, key)
		}
		desc, err := describe(ctx, s)
		if err != nil {
			return nil, err
		}
		stream := event.Stream{
			Harness:       sel.Harness,
			AdapterSchema: s.owner.Schema(),
			SourceID:      sel.SourceID,
			Path:          s.src.PrimaryPath,
		}
		capture, _, _, err := streamDigests(stream)
		if err != nil {
			return nil, err
		}
		if capture != sel.CaptureDigest {
			// Reported, not refused. §7 makes a changed input a fact the
			// receipt records; refusing here would make an appended session
			// unexplorable until someone prepared the scope again.
			a.diagf("warning: %s changed since the preparation was fixed\n", Sanitize(key))
		}
		inputs = append(inputs, preflight.Input{
			Stream:      stream,
			Digest:      string(capture),
			Attachments: attachmentsOf(desc),
			Unresolved:  unresolvedOf(desc),
		})
	}
	return inputs, nil
}

func attachmentsOf(desc *adapter.Description) []preflight.Attachment {
	if desc == nil {
		return nil
	}
	out := make([]preflight.Attachment, 0, len(desc.Artifacts)+len(desc.Blobs))
	for _, f := range desc.Artifacts {
		out = append(out, preflight.Attachment{Path: f.SourcePath, Size: f.Size})
	}
	for _, b := range desc.Blobs {
		out = append(out, preflight.Attachment{Path: b.SourcePath, Size: b.Size, Digest: string(b.Digest)})
	}
	return out
}

func unresolvedOf(desc *adapter.Description) []preflight.Reference {
	if desc == nil {
		return nil
	}
	out := make([]preflight.Reference, 0, len(desc.UnresolvedBlobRefs))
	for _, ref := range desc.UnresolvedBlobRefs {
		out = append(out, preflight.Reference{Ref: ref, Reason: "blob content is absent from the local store"})
	}
	return out
}

// exploreReporter narrates a run on stderr. Record events are never
// throttled — each one is a durable write and the frontier growing is the
// thing an operator is waiting to see — while worker progress is, because a
// chatty worker must not turn the diagnostic stream into a scroll.
type exploreReporter struct {
	app  *app
	last time.Time
}

func (r *exploreReporter) record(e explore.RecordEvent) {
	verb := "recorded"
	if e.Reused {
		verb = "reused"
	}
	r.app.diagf("%s %s %s (%s)\n", verb, Sanitize(string(e.Type)), Sanitize(e.ID), Sanitize(string(e.Stage)))
}

func (r *exploreReporter) progress(stage explore.Stage, p worker.ProgressRecord) {
	if time.Since(r.last) < exploreProgressInterval {
		return
	}
	r.last = time.Now()
	r.app.diagf("%s: %s\n", Sanitize(string(stage)), Sanitize(p.Message))
}

// enrol puts every reviewable record a run produced on the review queue and
// reports how many were enrolled.
//
// Observations are deliberately absent: §6.7 makes hypotheses, findings, and
// proposals the reviewable kinds, and an observation is read through the
// hypothesis it develops or the finding it supports rather than decided on
// its own. A record that cannot be enrolled is a warning rather than a
// failure: the record is already durable, and losing the run's report over a
// queue row would be the wrong trade.
func (a *app) enrol(ctx context.Context, svc *review.Service, outcome *explore.Outcome) int {
	enrolled := 0
	kinds := []struct {
		kind frontier.EntityType
		ids  []string
	}{
		{frontier.EntityHypothesis, outcome.Hypotheses},
		{frontier.EntityFinding, outcome.Findings},
		{frontier.EntityProposal, outcome.Proposals},
	}
	for _, group := range kinds {
		for _, id := range group.ids {
			if _, err := svc.Enroll(ctx, frontier.Ref{Type: group.kind, ID: id}); err != nil {
				a.diagf("warning: enrol %s for review: %s\n", Sanitize(id), Sanitize(err.Error()))
				continue
			}
			enrolled++
		}
	}
	return enrolled
}

// exploreOutcome states one attempt as the machine-readable document.
func exploreOutcome(prep runstore.Preparation, profile worker.ProfileRef, set *cookbook.Set,
	outcome *explore.Outcome, enrolled int) exploreResult {
	res := exploreResult{
		RunID:         Sanitize(outcome.RunID),
		PreparationID: string(prep.ID),
		Profile:       Sanitize(profile.String()),
		Hypotheses:    idList(outcome.Hypotheses),
		Observations:  idList(outcome.Observations),
		Findings:      idList(outcome.Findings),
		Proposals:     idList(outcome.Proposals),
		Promoted:      idList(outcome.Promoted),
		Deferred:      idList(outcome.Deferred),
		Rejected:      idList(outcome.Rejected),
		Objections:    idList(outcome.Objections),
		Reused:        outcome.Reused,
		Retrievals:    len(outcome.Retrieval),
		Enrolled:      enrolled,
		Cancelled:     outcome.Cancelled,
	}
	for _, r := range set.All() {
		res.Recipes = append(res.Recipes, Sanitize(r.ID)+"@"+strconv.Itoa(r.Version))
	}
	if outcome.Receipt != nil {
		res.ReceiptID = string(outcome.Receipt.Header.ID)
	}
	if outcome.Challenge != nil {
		res.ChallengeReceiptID = string(outcome.Challenge.Header.ID)
	}
	if outcome.Synthesis != nil {
		res.SynthesisReceiptID = string(outcome.Synthesis.Header.ID)
	}
	if p := outcome.Preflight; p != nil {
		res.Preflight = &preflightRow{
			Version:           p.Version,
			Disclosure:        Sanitize(p.Disclosure.Disclosure),
			RedactionRequired: p.Disclosure.RedactionRequired,
			Inputs:            p.Stats.Inputs,
			Bytes:             p.Stats.Bytes,
			Events:            p.Stats.Events,
			SecretFindings:    p.Stats.SecretFindings,
			Findings:          len(p.Findings),
		}
	}
	for _, f := range outcome.Failures {
		res.Failures = append(res.Failures, failureRow{
			Stage:   Sanitize(f.Stage),
			Code:    Sanitize(f.Code),
			Message: Sanitize(f.Message),
			At:      formatTime(f.At),
		})
	}
	return res
}

// writeExplore renders one attempt for a terminal, ending on the receipt
// identity: a run's durable answer to "what happened" is its receipt, and an
// operator needs to be able to copy it straight into `babel export`.
func (a *app) writeExplore(res exploreResult) {
	rows := [][2]string{
		{"run", res.RunID},
		{"preparation", res.PreparationID},
		{"profile", res.Profile},
		{"hypotheses", strconv.Itoa(len(res.Hypotheses))},
		{"observations", strconv.Itoa(len(res.Observations))},
		{"findings", strconv.Itoa(len(res.Findings))},
		{"proposals", strconv.Itoa(len(res.Proposals))},
		{"promoted", strconv.Itoa(len(res.Promoted))},
		{"deferred", strconv.Itoa(len(res.Deferred))},
		{"objections", strconv.Itoa(len(res.Objections))},
		{"reused", strconv.Itoa(res.Reused)},
		{"retrievals", strconv.Itoa(res.Retrievals)},
		{"enrolled", strconv.Itoa(res.Enrolled)},
	}
	if res.Preflight != nil {
		rows = append(rows, [2]string{"preflight",
			fmt.Sprintf("%s, %d %s, %d %s",
				res.Preflight.Disclosure,
				res.Preflight.Inputs, plural(res.Preflight.Inputs, "input", "inputs"),
				res.Preflight.SecretFindings,
				plural(res.Preflight.SecretFindings, "secret finding", "secret findings"))})
	}
	if res.Cancelled {
		rows = append(rows, [2]string{"cancelled", "yes; committed records are durable and the frontier keeps the rest"})
	}
	writeDetail(a.stdout, rows)
	for _, f := range res.Failures {
		fmt.Fprintf(a.stdout, "failure  %s/%s: %s\n", f.Stage, f.Code, f.Message)
	}
	fmt.Fprintf(a.stdout, "\nreceipt %s\n", orMissing(res.ReceiptID))
	if res.ChallengeReceiptID != "" {
		fmt.Fprintf(a.stdout, "challenge receipt %s\n", res.ChallengeReceiptID)
	}
	if res.SynthesisReceiptID != "" {
		fmt.Fprintf(a.stdout, "synthesis receipt %s\n", res.SynthesisReceiptID)
	}
	if len(res.Proposals) > 0 {
		fmt.Fprintf(a.stdout, "review them with: babel review queue\n")
	}
}
