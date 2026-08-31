package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ParamConformance is the job parameter through which Babel's conformance
// suite tells a worker which obligation is being exercised.
//
// It is part of the contract rather than a testing hack: several obligations —
// that a denial does not end a run, that no result follows an error, that
// cancellation is prompt — cannot be observed unless the worker can be asked
// to reach that state. A production job never sets this key, and a worker must
// treat an unrecognized value as ConformanceWellBehaved.
const ParamConformance = "babel.conformance"

// Conformance directives. A worker that intends to pass Conformance must
// implement each one.
const (
	// ConformanceWellBehaved runs a minimal successful analysis: the resolved
	// configuration, at least one progress event, then a result.
	ConformanceWellBehaved = "well-behaved"

	// ConformanceEchoJob runs a well-behaved analysis and, in the terminal
	// result's payload, reports the job it decoded under the key "job":
	//
	//	"job":{"recipes":["ID@VERSION",…],
	//	       "sources":["KIND|SELECTOR|DIGEST|SNAPSHOT",…]}
	//
	// Both arrays are in the job's own order, one entry per element, and an
	// absent digest or snapshot is the empty string between its separators.
	// The encoding is deliberately flat: a worker that produces it has read
	// every subfield of every element, and Babel compares strings rather
	// than guessing at a shape the counterpart chose.
	//
	// Nothing else is asked for. The job's identifiers, profile and grant
	// are each already held to something — Babel correlates the run itself,
	// refuses a resolved profile that does not match, and denies a request
	// outside the grant — so adding them here would be a second contract
	// over material that already has one.
	//
	// It exists because nothing else in a run makes the worker's reading of
	// the job observable. Receipt.Recipes and Receipt.Sources are copied
	// from Babel's own outgoing job, so they record what Babel sent and
	// never what the counterpart understood: a worker that ignored both
	// arrays produces a receipt indistinguishable from one that honoured
	// them. The suite plants a per-run nonce in the material it asks about,
	// so a candidate cannot answer with a constant read out of this file.
	ConformanceEchoJob = "echo-job"

	// ConformanceRequestTool makes exactly one tool request for
	// CapabilityCorpusSearch, waits for the decision, records it in the
	// result payload, and finishes with a result whichever way it was
	// decided.
	ConformanceRequestTool = "request-tool"

	// ConformanceRequestUngranted makes one request for
	// CapabilitySandboxExec, which the conformance job does not grant. The
	// worker must survive the denial and still deliver a result.
	ConformanceRequestUngranted = "request-ungranted"

	// ConformanceEchoEvidence makes one corpus-search request, waits for the
	// decision, and reports in the terminal result's payload the served
	// evidence it decoded off that decision, under the key
	// "served_evidence":
	//
	//	"served_evidence":{"hits":
	//	  ["HARNESS|SOURCE_ID|INDEX|PATH|LINE|BYTE_OFFSET|DIGEST|EXCERPT",…]}
	//
	// One entry per served hit, in served order, pipe-joined, with the three
	// numbers in plain base 10 and the excerpt verbatim and last. The
	// encoding is flat for the same reason ConformanceEchoJob's is: a worker
	// that produces it has read every subfield of every hit, and Babel
	// compares strings rather than negotiating over a shape the counterpart
	// chose. The array is emitted even when it is empty, so a worker that
	// implemented the directive and decoded nothing stays distinguishable
	// from one that never implemented it.
	//
	// The key is "served_evidence" rather than "evidence" because a real
	// worker already keeps an evidence log of its requests under that name,
	// and the two are different things: one is what the worker asked for,
	// this is what Babel answered with.
	//
	// It exists because a worker that received evidence and ignored it was
	// indistinguishable from one that received none. Babel's receipt cannot
	// show the difference — it records the decision and deliberately never
	// the payload — so the worker is asked, and the material it is asked
	// about carries a per-run nonce, which is what separates a worker that
	// read this decision from one that answered out of a fixture. That gap
	// is not hypothetical: for the whole of Babel's history before this,
	// every tool decision was an adjudication with no evidence attached, and
	// the first real exploration was allowed four corpus searches, was shown
	// no byte of any of them, and wrote nothing.
	ConformanceEchoEvidence = "echo-evidence"

	// ConformanceErrorOnly emits one error event and then exits, emitting no
	// result.
	ConformanceErrorOnly = "error-only"

	// ConformanceSlow emits progress and then keeps working long enough to be
	// cancelled.
	ConformanceSlow = "slow"

	// ConformanceEchoToken asks the worker for its run-scoped broker token
	// back: it must report the credential in the terminal result's payload
	// and in the message of at least one progress event, and finish the job
	// like a well-behaved run.
	//
	// It is a trap, and it is the only directive that asks for something a
	// worker must refuse to do literally. Honouring it means emitting those
	// two events with the credential replaced by whatever placeholder the
	// worker uses for a secret — the request is answered, the token never
	// reaches a pipe. A worker with no output discipline writes it verbatim
	// instead, and run/no-credential-leak reads the bytes the worker wrote
	// before Babel touches them, so the difference is observable.
	//
	// The directive exists because that difference cannot be seen any other
	// way. Babel scrubs the token out of everything it records, so a receipt
	// from a careless worker and a receipt from a careful one are identical;
	// grading only the receipt certifies Babel's scrubbing, not the
	// counterpart's discipline.
	ConformanceEchoToken = "echo-token"

	// ConformanceUnderDeclare asks the worker to declare containment it knows
	// is insufficient: a named backend and a stated escape assumption, as
	// every declaration must carry, and none of the four properties a
	// sandboxed run requires. It must then wait to be sent the run's
	// material, read the refusal Babel writes instead, and exit.
	//
	// It is the second directive that asks for something a worker would never
	// do on its own, and it is asked for the same reason the first one is: the
	// behaviour cannot be graded otherwise. A worker that always declares
	// enough is never refused, so the refusal path — the one that decides
	// whether the run's credential travels — would be exercised by nothing.
	//
	// What is graded is what happens after the refusal, because that is the
	// worker's half: it produces no result, it emits nothing further, and it
	// exits rather than blocking forever on a read for material Babel has
	// already decided not to write. Babel's half — that the material and the
	// credential were never written — is a property of Babel's own code and
	// is tested there, against a fake worker whose stdin is captured.
	ConformanceUnderDeclare = "under-declare"
)

// conformanceToken is the synthetic run-scoped broker credential the suite
// plants in every job. Conformance asserts it never comes back in a receipt or
// an error string. It is long and non-dictionary so a substring search cannot
// match anything a worker emits by coincidence.
const conformanceToken = "BROKERTOKEN4f19c8d3a72b6e05f8341ca9"

// conformanceT is the part of *testing.T that the obligations use. It exists
// so one set of assertions can be driven both by `go test` and by a plain
// program, which is what lets an implementation outside this module sit the
// same exam: Go forbids importing internal/, so a foreign test suite can never
// call Conformance itself.
type conformanceT interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
}

// conformanceTarget is the implementation under examination: the executable,
// the arguments that put it into worker mode, and the containment the run
// demands of it. The arguments matter because a worker need not speak the
// protocol at argv[0] — Code, for one, is an interactive program that speaks it
// under a subcommand — and needing an argument is not the same as being a
// different protocol.
//
// The requirement matters for a different reason. A worker that declares
// honestly weak containment fails every obligation that reaches worker mode
// with the same containment error, which hides whether it implements the rest
// of the protocol at all. Grading it against a relaxed requirement separates
// "has no sandbox yet" from "does not speak the protocol" — two findings that
// need different work. A nil requirement means the strict default, so the
// relaxation is only ever an explicit choice.
type conformanceTarget struct {
	binary      string
	args        []string
	requirement *Requirement
}

// conformanceObligation is one contract item: the name it is known by and the
// assertions that decide it.
type conformanceObligation struct {
	name string
	run  func(t conformanceT)
}

// Conformance drives the worker executable at workerPath, launched with args,
// through every obligation the analysis-worker protocol places on the
// counterpart, one subtest per obligation.
//
// It is exported because it is the contract, not a private test: Babel's own
// tests run it against the fake worker, so the suite cannot rot into asserting
// nothing. Code — or any other implementation of this protocol — reaches the
// very same obligations through the "babel conformance WORKER" command, which
// runs this list against any binary; an external repository cannot import this
// package, so the command is the seam, not an import.
//
// Each obligation name is the contract item:
//
//	handshake/accept                hello first, then a configuration, then exit
//	handshake/refuse                a refused worker exits without a job
//	run/well-behaved                configuration, progress, a readable result,
//	                                exit 0
//	run/declares-from-the-preamble  the containment declaration is produced
//	                                from the preamble alone, before the
//	                                material and the credential are written
//	run/refused-before-credentials  a worker whose declaration is refused
//	                                produces nothing and exits
//	run/declares-containment        the worker states the sandbox it ran in
//	run/declares-profile            the resolved configuration carries the
//	                                metadata, capabilities and cost Babel reads
//	run/decodes-the-job             the worker reports back the recipes and
//	                                sources it was actually given
//	run/forward-compatible-job      unknown fields in either job stage are
//	                                ignored, not fatal
//	run/tool-allow                  a tool request blocks for its decision
//	run/published-tool-names        every tool name requested is one the job
//	                                published for that capability
//	run/consumes-served-evidence    the evidence a decision carried reaches the
//	                                worker's own reading of it
//	run/tool-denial-continues       a denial does not end the run
//	run/grant-boundary              an ungranted capability is denied
//	run/reports-resources           self-reported resource use is honest about
//	                                what the run demonstrably did
//	run/error-is-terminal           no result may follow an error
//	run/cancellation                cancellation ends the run promptly
//	run/no-credential-leak          a worker asked for the broker token back
//	                                finishes the job and writes it nowhere
//
// It requires no network, no credential and no transcript.
func Conformance(t *testing.T, workerPath string, args ...string) {
	t.Helper()
	for _, obligation := range conformanceObligations(conformanceTarget{binary: workerPath, args: args}) {
		t.Run(obligation.name, func(t *testing.T) { obligation.run(t) })
	}
}

// conformanceObligations builds the suite for one worker executable. The
// obligations are values rather than inline subtests so that RunConformance
// can execute the identical assertions without a testing.T in reach.
func conformanceObligations(target conformanceTarget) []conformanceObligation {
	var obligations []conformanceObligation
	add := func(name string, run func(t conformanceT)) {
		obligations = append(obligations, conformanceObligation{name: name, run: run})
	}

	add("handshake/accept", func(t conformanceT) {
		client := conformanceClient(t, target, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cfg, err := client.Configure(ctx)
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		if cfg.Profile.ID == "" {
			t.Error("configuration reports no profile ID; Babel has nothing stable to persist")
		}
		if cfg.Profile.Revision <= 0 {
			t.Errorf("profile revision = %d, want a positive revision", cfg.Profile.Revision)
		}
		switch cfg.Privacy.Disclosure {
		case DisclosureLocal, DisclosureHosted:
		default:
			t.Errorf("privacy disclosure = %q, want %q or %q",
				cfg.Privacy.Disclosure, DisclosureLocal, DisclosureHosted)
		}
		if len(cfg.Metadata) == 0 {
			t.Error("configuration reports no resolved metadata; a receipt requires provider/model metadata")
		}
		if cfg.Worker.Name == "" {
			t.Error("hello reports no worker name; a run cannot be attributed to a build")
		}
		if cfg.ProtocolVersion != ProtocolVersion {
			t.Errorf("negotiated version = %d, want %d", cfg.ProtocolVersion, ProtocolVersion)
		}
	})

	add("handshake/refuse", func(t conformanceT) {
		// Babel offers a version no worker can support, so the worker must be
		// refused. The refusal is written to it, and it must exit rather than
		// wait for a job that will never arrive.
		client := conformanceClient(t, target, func(cfg *Config) {
			cfg.Versions = []int{ProtocolVersion + 1_000_000}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		started := time.Now()
		_, err := client.Configure(ctx)
		if !errors.Is(err, ErrVersionMismatch) {
			t.Fatalf("Configure error = %v, want ErrVersionMismatch", err)
		}
		if errors.Is(err, ErrWorkerLingered) {
			t.Error("the refused worker did not exit; it must not wait for a job it will never be sent")
		}
		if elapsed := time.Since(started); elapsed > 20*time.Second {
			t.Errorf("refusal took %s; a refused worker must exit promptly", elapsed)
		}
	})

	add("run/well-behaved", func(t conformanceT) {
		receipt, err := conformanceRun(t, target, ConformanceWellBehaved, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("run produced no result record")
		}
		if receipt.Result.Status != StatusOK && receipt.Result.Status != StatusPartial {
			t.Errorf("result status = %q", receipt.Result.Status)
		}
		// A worker that passes every other obligation while declaring a
		// schema Babel cannot read has produced a run whose output is
		// unusable: the control plane refuses such a payload rather than
		// parsing it hopefully, so the run would be graded 14/14 and still
		// deliver nothing. The declaration is part of the contract, not an
		// implementation detail of whoever reads the payload.
		if receipt.Result.Schema != ResultSchema {
			t.Errorf("result schema = %q, want %q; Babel refuses a payload it cannot read",
				receipt.Result.Schema, ResultSchema)
		}
		if receipt.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0 after a result", receipt.ExitCode)
		}
		if len(receipt.Metadata) == 0 {
			t.Error("receipt carries no resolved provider metadata")
		}
		if receipt.Profile.ID == "" || receipt.ProtocolVersion == 0 {
			t.Errorf("receipt is missing its profile or protocol version: %+v", receipt.Profile)
		}
		if receipt.Duration <= 0 {
			t.Error("receipt records no duration")
		}
		if len(receipt.Progress) == 0 {
			t.Error("no progress event was recorded; Babel cannot keep an interface responsive without them")
		}
		// Babel's own bookkeeping, not the worker's: these two fields are
		// copied from the outgoing job, so they say what Babel authorized
		// and nothing about what the counterpart read. run/decodes-the-job
		// is what grades the worker's side of the same material.
		if len(receipt.Recipes) == 0 || len(receipt.Sources) == 0 {
			t.Error("the receipt does not record which recipes ran over which sources")
		}
		if receipt.Failure != nil {
			t.Errorf("a successful run recorded a failure: %+v", receipt.Failure)
		}
	})

	add("run/declares-from-the-preamble", func(t conformanceT) {
		// The job document arrives in two stages and this worker's own
		// declaration is what separates them. Stage one carries the run's
		// identity, the profile to resolve and the run's parameters; the
		// recipes, the grant, the sources and the run-scoped broker
		// credential are stage two, and Babel writes them only after the
		// declaration has satisfied the run's requirement.
		//
		// So a worker must be able to resolve and declare from the preamble
		// alone. One that waits for material first — which is what every
		// version 1 worker did, because version 1 sent all of it at once —
		// waits for a write Babel is not going to make, and the run dies in
		// the idle timeout with nothing said about why. That failure is
		// indistinguishable from a hung analysis unless an obligation names
		// it, which is what this one is for.
		receipt, err := conformanceRun(t, target, ConformanceWellBehaved, AllowWithinGrant())
		if err != nil {
			if errors.Is(err, ErrWorkerStalled) || errors.Is(err, ErrNoResult) {
				t.Fatalf("the worker produced no configuration from the job preamble: %v. Babel writes the job in two stages — a preamble carrying the run's identity, the profile and the parameters, then the recipes, grant, sources and broker token — and it writes the second only after this worker's containment declaration has been accepted. A worker waiting for sources, a grant or a credential before it declares is waiting for a write that will never come; version %d is that change",
					err, ProtocolVersion)
			}
			t.Fatalf("Run: %v", err)
		}
		if receipt.ProtocolVersion != ProtocolVersion {
			t.Errorf("negotiated version = %d, want %d; the staged job document is what that version is",
				receipt.ProtocolVersion, ProtocolVersion)
		}
		if receipt.Containment.Backend == "" {
			t.Error("the run completed with no containment declaration recorded, so nothing was ever declared for the material to be conditional on")
		}
		if receipt.Result == nil {
			t.Fatal("the run produced no result, so nothing shows the worker went on to use material that arrived after its declaration")
		}
	})

	add("run/refused-before-credentials", func(t conformanceT) {
		// The refusal path, which is the whole point of staging the document:
		// a declaration short of the run's requirement is answered with a
		// refusal instead of the material, and the run's broker credential is
		// never written. Babel's half of that is a property of Babel's own
		// code and is tested there, against a fake worker whose stdin is
		// captured. This grades the worker's half, which is what it does when
		// it is refused: it produces no result, asks for nothing, and exits
		// instead of blocking forever on a read.
		//
		// The declaration is refused because the directive asks the worker to
		// under-declare on purpose. Nothing else could produce a refusal
		// reliably: a worker that declares honestly is refused only on a
		// platform §10 has not qualified, and grading a path that exists only
		// on some machines would be grading the machine.
		//
		// The requirement is the strict one whatever the grading was relaxed
		// to. --unsandboxed exists so a worker with no sandbox can still be
		// told which parts of the protocol it implements, and under a relaxed
		// requirement the declaration this directive asks for would be
		// accepted — leaving nothing refused and this obligation passing
		// while grading nothing at all.
		strict := SandboxedRun()
		client := conformanceClient(t, target, func(cfg *Config) {
			cfg.Authorizer = AllowWithinGrant()
			cfg.Requirement = &strict
		})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		started := time.Now()
		receipt, err := client.Run(ctx, conformanceJob(ConformanceUnderDeclare))
		elapsed := time.Since(started)

		if !errors.Is(err, ErrContainment) {
			t.Fatalf("Run under the under-declare directive = %v, want a containment refusal. The directive asks the worker to declare a named backend, a stated escape assumption, and none of the four properties a sandboxed run requires; a run that was not refused means the worker declared something else",
				err)
		}
		if errors.Is(err, ErrWorkerLingered) {
			t.Error("the refused worker did not exit. Babel wrote it a refusal instead of the job material, so nothing more is coming: a worker still blocked on that read has to be killed, and its own teardown never runs")
		}
		if elapsed > 30*time.Second {
			t.Errorf("the refusal took %s; a worker that has been refused exits promptly", elapsed)
		}
		if receipt == nil {
			t.Fatal("no receipt after a refused declaration; a refused run still has an audit record")
		}
		if receipt.Result != nil {
			t.Errorf("the refused run produced a result: %+v. There was no material to analyse — no sources, no grant, no broker credential — so a result here is a claim about nothing",
				receipt.Result)
		}
		if len(receipt.ToolRequests) != 0 {
			t.Errorf("the refused worker made %d tool request(s) after being refused; the run was over and its grant was never sent",
				len(receipt.ToolRequests))
		}
	})

	add("run/declares-containment", func(t conformanceT) {
		// Babel does not implement the sandbox (decision 53), so a worker's
		// declaration is the only thing Babel can hold it to before the run
		// starts. An implementation that declares nothing is refused, which
		// makes this obligation the difference between a stated boundary and
		// an assumed one.
		receipt, err := conformanceRun(t, target, ConformanceWellBehaved, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		got := receipt.Containment
		if got.Backend == "" {
			t.Error("worker declared no sandbox backend; an unnamed mechanism cannot be assessed")
		}
		if got.Escape == "" {
			t.Error("worker declared no escape assumption; a sandbox with no stated residual risk has not been thought about")
		}
		if err := got.Satisfies(SandboxedRun()); err != nil {
			t.Errorf("declared containment does not satisfy a sandboxed run: %v", err)
		}
	})

	add("run/declares-profile", func(t conformanceT) {
		// The resolved configuration is not free-form. Babel's own consumers
		// read named keys out of it, so a worker that renames one satisfies
		// every other obligation and then produces receipts nobody can read
		// — the same shape of drift that let a divergent result schema live
		// on both sides of this boundary unnoticed.
		//
		// Only what Babel actually reads is required. Legislating a profile
		// schema here would be Babel dictating across a boundary Code owns
		// (SPEC.md §2.6), and a requirement no consumer has is a fabricated
		// contract that a candidate would have to satisfy for nobody.
		//
		// The run makes one allowed tool request, because one of the three
		// declarations below can only be graded against something the worker
		// demonstrably did rather than against Babel's opinion of it.
		receipt, err := conformanceRun(t, target, ConformanceRequestTool, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("the run produced no result, so nothing was ever resolved to grade")
		}

		// SPEC.md §6.5 makes "resolved provider/model/thinking metadata
		// returned by Code" part of every receipt, and internal/run reads
		// those three keys by name (see the receipt-body assertions in
		// internal/run's store tests). A worker renaming "model" to
		// "model_name" writes a receipt whose model is empty, which is a
		// durable record of the wrong thing rather than a missing nicety.
		// Nothing beyond these three is required: a key Babel does not read
		// is the profile's own business.
		for _, key := range requiredMetadata {
			if strings.TrimSpace(receipt.Metadata[key]) == "" {
				t.Errorf("resolved metadata names no %q; SPEC.md §6.5 makes provider, model and thinking part of the receipt and Babel reads them under exactly those keys, so a renamed one is recorded as absent",
					key)
			}
		}

		// Capabilities are the worker's claim about what the profile can do.
		// Babel stores and displays that claim, so the names in it have to be
		// names Babel defines: a capability this build cannot name is one it
		// can never grant (DenyUnknownCapability), so declaring it tells an
		// operator the profile can do something no run will authorize.
		for _, capability := range receipt.ResolvedCapabilities {
			if !capability.Known() {
				t.Errorf("resolved profile declares capability %q, which Babel does not define; a name Babel has no boundary for can never be granted, so the claim is unusable",
					capability)
			}
		}
		// The claim must at least cover what the worker actually did. Babel
		// allowed this request and answered it, so the run itself is the
		// evidence that the resolved profile can do it.
		for _, request := range receipt.ToolRequests {
			if request.Allowed && !slices.Contains(receipt.ResolvedCapabilities, request.Capability) {
				t.Errorf("the worker exercised %q but its resolved profile declares only %v; a profile that omits what the run did is not the profile that ran",
					request.Capability, receipt.ResolvedCapabilities)
			}
		}

		// Cost is the profile's own estimate and never a measurement (see
		// Cost), so Babel has nothing to compare a figure against and grades
		// only the two things that are wrong on their face: a price that is
		// negative, and a price with no unit. A profile that costs nothing
		// reports zeros in a named currency and passes — requiring a
		// positive rate would be graded by whoever writes the constant.
		cost := receipt.Cost
		if cost.InputPer1K < 0 || cost.OutputPer1K < 0 || cost.EstimatedRun < 0 {
			t.Errorf("resolved cost carries a negative figure: %+v; a cost guard would read it as a discount", cost)
		}
		if cost.Currency == "" && (cost.InputPer1K != 0 || cost.OutputPer1K != 0 || cost.EstimatedRun != 0) {
			t.Errorf("resolved cost quotes %+v in no currency; Babel shows an estimate only when it has a unit for it, so an unnamed currency drops the whole figure rather than displaying it wrongly",
				cost)
		}
	})

	add("run/decodes-the-job", func(t conformanceT) {
		// Everything else about a run is consistent with a worker that read
		// the profile, ignored the rest of the job, and analysed whatever it
		// felt like. The receipt cannot show the difference: its Recipes and
		// Sources are copied from Babel's outgoing job, so they are Babel
		// quoting itself.
		//
		// So the worker is asked, and the answer is graded — the same
		// mechanism every other unobservable state uses. The material it is
		// asked about carries a per-run nonce, which is what separates a
		// worker that decoded this job from one that hardcoded the published
		// conformance fixture.
		nonce := rand.Text()
		var sent Job
		receipt, err := conformanceRun(t, target, ConformanceEchoJob, AllowWithinGrant(),
			func(job *Job) {
				job.Recipes = []RecipeRef{
					{ID: "outcome-integrity", Version: 1},
					{ID: "evidence-" + nonce, Version: 7},
				}
				// Two sources, one archived and one not, so a worker that
				// drops "snapshot" is caught by the first and a worker that
				// invents one is caught by the second.
				job.Sources = []Source{{
					Kind:     "session",
					Selector: "omp/synthetic-" + nonce,
					Digest:   "sha256:" + strings.Repeat("0", 64),
					Snapshot: "snapshot-" + nonce,
				}, {
					Kind:     "repository",
					Selector: "synthetic/repository-" + nonce,
					Digest:   "sha256:" + strings.Repeat("1", 64),
				}}
				sent = *job
			})
		if err != nil {
			t.Fatalf("Run under the echo-job directive: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("no terminal result under the echo-job directive; a worker that produces nothing has reported no reading of the job")
		}

		var payload struct {
			Job *jobEcho `json:"job"`
		}
		if err := json.Unmarshal(receipt.Result.Payload, &payload); err != nil {
			t.Fatalf("the result payload is not a JSON object: %v", err)
		}
		if payload.Job == nil {
			t.Fatal(`the result payload carries no "job" object; the echo-job directive asks the worker to report the job it decoded, and a worker that cannot has not shown it read one`)
		}
		want := echoOfJob(sent)
		got := *payload.Job
		compareEcho(t, "recipe", got.Recipes, want.Recipes)
		compareEcho(t, "source", got.Sources, want.Sources)
	})

	add("run/forward-compatible-job", func(t conformanceT) {
		// A newer Babel adds a field; an older worker must ignore it rather
		// than fail. Nothing else about the run changes.
		//
		// One unknown field per stage, because the job is two messages and a
		// worker that tolerates one is not thereby tolerating the other. The
		// preamble is the more likely of the two to grow: it is the message
		// the policy surface lives in, and it is the one a worker parses
		// before it has declared anything, so a build that hard-failed on an
		// unrecognized field there would refuse the run at the earliest
		// possible moment for the least possible reason.
		receipt, err := conformanceRun(t, target, ConformanceWellBehaved, AllowWithinGrant(),
			func(job *Job) {
				job.PreambleExtra = map[string]json.RawMessage{
					"x-babel-future-preamble": json.RawMessage(`{"unknown":"to this worker"}`),
				}
				job.Extra = map[string]json.RawMessage{
					"x-babel-future": json.RawMessage(`{"unknown":"to this worker"}`),
				}
			})
		if err != nil {
			t.Fatalf("Run with an unknown field in each job stage: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("an unknown job field prevented a result")
		}
	})

	add("run/tool-allow", func(t conformanceT) {
		receipt, err := conformanceRun(t, target, ConformanceRequestTool, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(receipt.ToolRequests) != 1 {
			t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
		}
		request := receipt.ToolRequests[0]
		if !request.Allowed {
			t.Fatalf("request was denied: %s %s", request.DenyCode, request.Reason)
		}
		if request.RequestID == "" {
			t.Error("tool request carries no request_id, so it could not have been answered")
		}
		if request.ArgumentsDigest == "" {
			t.Error("tool request recorded no argument digest")
		}
		if receipt.Result == nil {
			t.Fatal("run produced no result after an allowed tool request")
		}
		if !strings.Contains(string(receipt.Result.Payload), decisionAllow) {
			t.Errorf("result payload does not report the decision the worker received: %s",
				receipt.Result.Payload)
		}
	})

	add("run/published-tool-names", func(t conformanceT) {
		// The job publishes, per granted capability, the tool names some
		// facility in this build actually serves. This grades the worker
		// against that publication rather than against a name written down in
		// either repository.
		//
		// It is the obligation that was missing. A worker that requested
		// "babel_corpus_search" — a name it chose for itself, which existed
		// nowhere in Babel — passed all fourteen other obligations and was
		// then denied on every request of the first real exploration there has
		// been, producing no evidence at all. Nothing in the exam could see
		// it, because the suite's authorizer never inspected a tool name while
		// production's always had.
		//
		// A worker that read grant.tools and one that guessed a published name
		// both pass, deliberately: the contract is about the name that
		// travels, not about how the worker arrived at it. What fails is a
		// name Babel never published — and the request is separately denied
		// for it, so a candidate sees the same verdict here that a real run
		// would give it.
		var sent Job
		receipt, err := conformanceRun(t, target, ConformanceRequestTool, AllowWithinGrant(),
			func(job *Job) { sent = *job })
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		published := publishedTools(sent.Grant)
		if len(published) == 0 {
			t.Fatal("the conformance job published no tool names for any granted capability, so this obligation could not fail whatever the worker asked for; the fixture, not the candidate, is broken")
		}
		if len(receipt.ToolRequests) == 0 {
			t.Fatal("the worker made no tool request under the request-tool directive, so it never named a tool at all and nothing here is graded")
		}
		for _, request := range receipt.ToolRequests {
			if !sent.Grant.Allows(request.Capability) {
				// An out-of-grant capability is denied before its tool name is
				// ever looked at, so the name carries no meaning there. The
				// deliberate out-of-grant probe run/grant-boundary demands must
				// not be failed by this obligation.
				continue
			}
			if ServesTool(request.Capability, request.Tool) {
				continue
			}
			if names := published[request.Capability]; len(names) > 0 {
				t.Errorf("the worker requested tool %q under capability %q, and the job published %q for it. The name is not the worker's to choose: Babel denies an unpublished one on every request, which looks from the worker's side exactly like a corpus with nothing in it",
					request.Tool, request.Capability, names)
				continue
			}
			t.Errorf("the worker requested tool %q under capability %q, which the job published no tool names for. A granted capability absent from grant.tools is one no facility in this build serves, and a worker must request nothing under it rather than fall back to a name of its own",
				request.Tool, request.Capability)
		}
	})

	add("run/consumes-served-evidence", func(t conformanceT) {
		// Every other obligation about a tool request grades the
		// adjudication: that the worker blocked for a decision, that it
		// survived a denial, that the name it asked under was one Babel
		// published. None of them can see whether the evidence attached to an
		// allowed decision was read, and Babel's receipt cannot either — it
		// records the decision and deliberately never the payload, because
		// §9 forbids the durable record becoming a plaintext store of archive
		// content.
		//
		// So the worker is asked, the same way run/decodes-the-job asks about
		// the job document, and the answer is graded against material
		// carrying a per-run nonce. A worker that echoes it has demonstrably
		// read bytes only this decision could have given it; a worker that
		// answers out of a fixture, or that reports the request it made
		// instead of the answer it got, fails.
		//
		// Two hits are served, under two different harnesses, so a worker
		// that reads the first and stops — or that hardcodes a harness — is
		// caught rather than credited.
		nonce := rand.Text()
		evidence := conformanceEvidence(nonce)
		encoded, err := json.Marshal(evidence)
		if err != nil {
			t.Fatalf("the suite could not encode its own synthetic evidence: %v; the fixture, not the candidate, is broken", err)
		}

		receipt, err := conformanceRun(t, target, ConformanceEchoEvidence, serveEvidence(encoded))
		if err != nil {
			t.Fatalf("Run under the echo-evidence directive: %v", err)
		}
		if len(receipt.ToolRequests) == 0 {
			t.Fatal("the worker made no tool request under the echo-evidence directive, so nothing was served to it and nothing here is graded")
		}
		if receipt.Result == nil {
			t.Fatal("no terminal result under the echo-evidence directive; a worker that produces nothing has reported no reading of the evidence")
		}

		var payload struct {
			Evidence *evidenceEcho `json:"served_evidence"`
		}
		if err := json.Unmarshal(receipt.Result.Payload, &payload); err != nil {
			t.Fatalf("the result payload is not a JSON object: %v", err)
		}
		if payload.Evidence == nil {
			t.Fatal(`the result payload carries no "served_evidence" object; the echo-evidence directive asks the worker to report the evidence a decision served it, and a worker that cannot has not shown it read any`)
		}
		compareEcho(t, "served hit", payload.Evidence.Hits, echoOfEvidence(evidence))

		// Babel's own half of the same boundary, checked here for the reason
		// run/no-credential-leak checks the receipt for the token: the durable
		// record is what must not become a plaintext store of archive content
		// readable by anyone with catalog access (§9).
		//
		// The scope is Babel's own writing about the request — the tool
		// records — and deliberately not the whole receipt. A worker's result
		// payload is where observations live, and an observation quotes the
		// claim it is about, so corpus text there is the product working
		// rather than a leak. What must never carry it is the record Babel
		// authors, whose one route to a payload is the reason string a
		// facility returns beside it.
		var records strings.Builder
		for _, request := range receipt.ToolRequests {
			fmt.Fprintf(&records, "%+v\n", request)
		}
		rendered := records.String()
		for _, hit := range evidence.Hits {
			if strings.Contains(rendered, hit.Excerpt) {
				t.Errorf("the excerpt served for %s %s reached Babel's own tool record: %s. The wire carries content to the worker; the record Babel writes carries the decision, the argument digest and locators only",
					hit.Harness, hit.SourceID, rendered)
			}
		}
	})

	add("run/tool-denial-continues", func(t conformanceT) {
		receipt, err := conformanceRun(t, target, ConformanceRequestTool,
			DenyAll("conformance: policy denies this request"))
		if err != nil {
			t.Fatalf("Run after a denial: %v; a denial must not end the run", err)
		}
		if len(receipt.ToolRequests) != 1 {
			t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
		}
		request := receipt.ToolRequests[0]
		if request.Allowed {
			t.Fatal("the request was allowed; the injected policy denies everything")
		}
		if request.DenyCode != DenyPolicy {
			t.Errorf("deny code = %q, want %q", request.DenyCode, DenyPolicy)
		}
		if receipt.Result == nil {
			t.Fatal("no result after a denial; the worker must adapt, not abort")
		}
		if !strings.Contains(string(receipt.Result.Payload), decisionDeny) {
			t.Errorf("result payload does not report the denial: %s", receipt.Result.Payload)
		}
	})

	add("run/grant-boundary", func(t conformanceT) {
		// The policy allows everything inside the grant, and the request is
		// outside it. The grant must win: it is the boundary, the policy is
		// only allowed to narrow it.
		receipt, err := conformanceRun(t, target, ConformanceRequestUngranted, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(receipt.ToolRequests) != 1 {
			t.Fatalf("recorded %d tool requests, want 1", len(receipt.ToolRequests))
		}
		request := receipt.ToolRequests[0]
		if request.Allowed {
			t.Fatal("an ungranted capability was allowed")
		}
		if request.DenyCode != DenyNotGranted {
			t.Errorf("deny code = %q, want %q", request.DenyCode, DenyNotGranted)
		}
		if receipt.Result == nil {
			t.Fatal("no result after an out-of-grant denial")
		}
	})

	add("run/reports-resources", func(t conformanceT) {
		// Resource use is the one part of a receipt Babel copies from the
		// counterpart's word (SPEC.md §6.5 asks for it "where observable"),
		// so grading it has to stay inside what Babel can actually check.
		// Two things are checkable and the rest is not:
		//
		//   - Babel answered the tool requests itself, so it knows a floor
		//     for tool_calls that does not depend on trusting the worker.
		//   - A negative counter is not a small measurement, it is a wrong
		//     one. "Unknown" is reported by omitting the object, so a
		//     sentinel like -1 lands in a receipt as a fact nobody measured.
		//
		// cpu_seconds, max_rss_bytes and sandbox_bytes_written are graded for
		// sign and nothing else, deliberately. Babel measures none of them
		// and cannot from outside the process; demanding a positive number
		// would be satisfied by whoever writes the constant, which is worse
		// than an honest zero because it looks like data.
		//
		// Monotonicity would be the other checkable property — these are
		// cumulative counters and a running maximum, so none of them may
		// fall over a run — and it is not graded because it is not visible
		// from here. ProgressRecord carries no resources, and a receipt keeps
		// only the latest self-report, so the suite sees one figure rather
		// than a series. Grading the series would mean recording it, which is
		// a change to what Babel stores rather than to what it demands of a
		// worker.
		receipt, err := conformanceRun(t, target, ConformanceRequestTool, AllowWithinGrant())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if receipt.Result == nil {
			t.Fatal("the run produced no result, so there is no completed run to account for")
		}
		allowed := 0
		for _, request := range receipt.ToolRequests {
			if request.Allowed {
				allowed++
			}
		}
		if allowed == 0 {
			t.Fatal("the run brokered no allowed tool request, so nothing independently known is left to grade the worker's self-report against")
		}

		resources := receipt.Resources
		if resources == nil {
			// Reporting nothing is honest for a worker that measures
			// nothing, and Babel records nil as unknown rather than zero. It
			// stops being honest once the worker has declared that it bounds
			// its own resources: a ceiling is enforced by comparing usage
			// against it, so a worker that can hold the line can say where
			// the line was.
			if receipt.Containment.ResourceCeilings {
				t.Error("the worker declared resource ceilings and then reported no resource use at all; a bound is enforced by measuring what it bounds, so this is one of the two claims that is untrue")
			} else {
				t.Logf("the worker reported no resource use and claims no resource ceilings; nil is unknown rather than zero, so there is nothing here to grade")
			}
			return
		}
		if resources.CPUSeconds < 0 || resources.MaxRSSBytes < 0 ||
			resources.SandboxBytesWritten < 0 || resources.ToolCalls < 0 {
			t.Errorf("self-reported resource use carries a negative figure: %+v; unknown is reported by omitting the object, never by a sentinel a receipt would store as a measurement",
				*resources)
		}
		if resources.ToolCalls < allowed {
			t.Errorf("self-reported tool_calls = %d, but Babel authorized and answered %d tool request(s) in this run; the run made at least that many",
				resources.ToolCalls, allowed)
		}
	})

	add("run/error-is-terminal", func(t conformanceT) {
		receipt, err := conformanceRun(t, target, ConformanceErrorOnly, AllowWithinGrant())
		if !errors.Is(err, ErrWorkerReported) {
			t.Fatalf("Run error = %v, want the worker's own reported error", err)
		}
		if errors.Is(err, ErrResultAfterError) {
			t.Fatal("the worker emitted a result after its error; an error is terminal")
		}
		var reported *WorkerError
		if !errors.As(err, &reported) || reported.Code == "" {
			t.Fatalf("error event carried no code: %v", err)
		}
		if receipt.Result != nil {
			t.Error("a result was recorded after an error")
		}
		if receipt.Failure == nil || receipt.Failure.Origin != FailureWorker {
			t.Errorf("failure record = %+v, want one attributed to the worker", receipt.Failure)
		}
	})

	add("run/cancellation", func(t conformanceT) {
		client := conformanceClient(t, target, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel as soon as the worker is demonstrably working, so the run is
		// interrupted mid-flight rather than before it starts.
		client.cfg.OnProgress = func(ProgressRecord) { cancel() }

		job := conformanceJob(ConformanceSlow)
		started := time.Now()
		receipt, err := client.Run(ctx, job)
		elapsed := time.Since(started)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		if elapsed > 30*time.Second {
			t.Errorf("Run returned %s after cancellation; it must not wait for the worker's own schedule", elapsed)
		}
		if receipt == nil {
			t.Fatal("no receipt after cancellation; a cancelled run still has an audit record")
		}
	})

	add("run/no-credential-leak", func(t conformanceT) {
		// This obligation grades the worker's own output discipline, and it
		// passes only on the conjunction of three facts. Each one closes a
		// way the check used to certify nothing.
		//
		//  1. The run reached a terminal result under ConformanceEchoToken.
		//     Without this, "wrote the credential everywhere" and "wrote
		//     nothing at all" are the same verdict: a program that exits
		//     immediately satisfies any absence trivially, which is how
		//     /bin/true used to pass this one obligation while failing the
		//     other ten.
		//  2. The token appears nowhere in the bytes the worker itself
		//     wrote, on either stream, captured before Babel touches them.
		//     This is the worker's obligation and the reason the directive
		//     exists: the suite asks for the token back, and a worker with
		//     output discipline still never puts it on a pipe. Grading this
		//     from anything Babel stores is impossible — Babel scrubs the
		//     token on the way in, so a scrubbed record looks identical
		//     whether the worker was careful or careless.
		//  3. The token appears nowhere in the stored receipt. That is
		//     Babel's own guarantee rather than the worker's, and it is
		//     checked here as defence in depth: the durable audit record is
		//     the thing that must never carry a credential, whatever the
		//     counterpart did.
		//
		// No implementation-specific placeholder is looked for. How a worker
		// keeps the token out of its output is its own business; that it
		// does is the contract.
		raw := &tail{limit: rawTranscriptBytes}
		defer raw.discard()

		client := conformanceClient(t, target, func(cfg *Config) {
			cfg.Authorizer = AllowWithinGrant()
			cfg.rawTranscript = raw
		})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		receipt, err := client.Run(ctx, conformanceJob(ConformanceEchoToken))
		if err != nil {
			t.Fatalf("Run under the echo-token directive: %v; the worker must keep the broker token out of its output and still finish the job", err)
		}
		if receipt == nil || receipt.Result == nil {
			t.Fatal("no terminal result under the echo-token directive; a worker that produces nothing has not demonstrated output discipline, it has demonstrated silence")
		}
		if written := raw.String(); strings.Contains(written, conformanceToken) {
			t.Errorf("the worker wrote the run-scoped broker credential to its own stdout or stderr: %s",
				strings.ReplaceAll(written, conformanceToken, "<TOKEN>"))
		}
		if rendered := renderReceipt(receipt); strings.Contains(rendered, conformanceToken) {
			t.Errorf("the run-scoped broker credential reached Babel's receipt: %s", rendered)
		}
	})

	return obligations
}

// requiredMetadata is the resolved-configuration metadata Babel reads by name.
//
// It is exactly the set SPEC.md §6.5 names — "resolved provider/model/thinking
// metadata returned by Code" — and no more. The three keys are load-bearing
// literals in Babel's own receipt consumers, so a worker that spells one
// differently writes a durable record with an empty provider or an empty
// model. Every other key is the profile's to name: Code owns the profile
// (SPEC.md §2.6), and a requirement no consumer has would be Babel inventing
// a schema for someone else's data.
var requiredMetadata = []string{"provider", "model", "thinking"}

// jobEcho is the answer ConformanceEchoJob demands: the job the worker says it
// decoded, in the flat form the directive specifies.
//
// The recipes and sources are strings rather than objects on purpose. A worker
// that produces "ID@VERSION" has read both halves of a recipe reference, one
// that produces "KIND|SELECTOR|DIGEST|SNAPSHOT" has read all four parts of a
// source, and Babel compares text instead of negotiating over a nested shape
// the counterpart would have to guess at.
type jobEcho struct {
	Recipes []string `json:"recipes"`
	Sources []string `json:"sources"`
}

// echoOfJob renders the answer a worker that decoded job must produce. It is
// computed from the job the obligation actually sent rather than written out
// beside it, so the expectation cannot drift from the fixture.
func echoOfJob(job Job) jobEcho {
	echo := jobEcho{
		Recipes: make([]string, 0, len(job.Recipes)),
		Sources: make([]string, 0, len(job.Sources)),
	}
	for _, recipe := range job.Recipes {
		echo.Recipes = append(echo.Recipes, fmt.Sprintf("%s@%d", recipe.ID, recipe.Version))
	}
	for _, source := range job.Sources {
		echo.Sources = append(echo.Sources,
			strings.Join([]string{source.Kind, source.Selector, source.Digest, source.Snapshot}, "|"))
	}
	return echo
}

// evidenceEcho is the answer ConformanceEchoEvidence demands: the served
// evidence the worker says it decoded, one flat string per hit.
//
// Flat for jobEcho's reason, and flatter than it looks: a worker that produces
// "HARNESS|SOURCE_ID|INDEX|PATH|LINE|BYTE_OFFSET|DIGEST|EXCERPT" has read the
// hit's identity, its position in its session, all four parts of the locator
// that reopens it, and the text a model would quote. Those are exactly the
// fields a hit must carry to be citable, so the echo and the contract are the
// same list.
type evidenceEcho struct {
	Hits []string `json:"hits"`
}

// conformanceLocator, conformanceHit and conformanceResults are the suite's
// own rendering of a served corpus search.
//
// They are defined here rather than imported from the facility that produces
// them in a real run. Go forbids internal/explore from being reachable by an
// external candidate anyway, but the reason is not the import graph: the suite
// grades the protocol, and a suite built out of the producer's structs would
// certify that Babel can marshal its own types rather than that the shape on
// the wire is the shape the contract documents. It is the discipline the fake
// worker follows in the other direction, and for the same reason.
type conformanceLocator struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	ByteOffset int64  `json:"byte_offset"`
	Digest     string `json:"digest"`
}

type conformanceHit struct {
	Harness  string             `json:"harness"`
	SourceID string             `json:"source_id"`
	Index    int                `json:"index"`
	Kind     string             `json:"kind"`
	Excerpt  string             `json:"excerpt"`
	Locator  conformanceLocator `json:"locator"`
}

type conformanceResults struct {
	Schema string           `json:"schema"`
	Query  string           `json:"query"`
	Limit  int              `json:"limit"`
	Hits   []conformanceHit `json:"hits"`
}

// conformanceEvidence builds the synthetic payload one echo-evidence run
// serves, with nonce woven through every field the obligation grades.
//
// The nonce reaches the source identity, the locator's path, the record digest
// and the excerpt, so no part of the answer can be a constant. It does not
// reach the harness, because a harness name is a closed vocabulary a worker may
// reasonably recognize and a nonsense value would be grading the worker's
// tolerance rather than its reading; two real and different harness names do
// that job instead, and catch a worker that hardcodes one.
//
// The digests are real sha256 hex because the locator's digest is what verifies
// a reopened record, and a candidate is entitled to reject a locator that could
// not identify anything.
func conformanceEvidence(nonce string) conformanceResults {
	first := sha256.Sum256([]byte("babel/conformance/evidence/1\x00" + nonce))
	second := sha256.Sum256([]byte("babel/conformance/evidence/2\x00" + nonce))
	return conformanceResults{
		Schema: "babel.corpus-search/1",
		Query:  "synthetic " + nonce,
		Limit:  10,
		Hits: []conformanceHit{{
			Harness:  "omp",
			SourceID: "omp-" + nonce,
			Index:    42,
			Kind:     "agent-claim",
			Excerpt:  "the synthetic record " + nonce + " claims the cache warms on startup",
			Locator: conformanceLocator{
				Path:       "/synthetic/" + nonce + "/omp.jsonl",
				Line:       12,
				ByteOffset: 3456,
				Digest:     hex.EncodeToString(first[:]),
			},
		}, {
			Harness:  "codex",
			SourceID: "codex-" + nonce,
			Index:    7,
			Kind:     "tool-observation",
			Excerpt:  "a second synthetic record for " + nonce + " reporting exit status 1",
			Locator: conformanceLocator{
				Path:       "/synthetic/" + nonce + "/codex.jsonl",
				Line:       77,
				ByteOffset: 98765,
				Digest:     hex.EncodeToString(second[:]),
			},
		}},
	}
}

// echoOfEvidence renders the answer a worker that read results must produce.
// Like echoOfJob it is computed from what the obligation actually served, so
// the expectation cannot drift from the fixture.
func echoOfEvidence(results conformanceResults) []string {
	echo := make([]string, 0, len(results.Hits))
	for _, hit := range results.Hits {
		echo = append(echo, strings.Join([]string{
			hit.Harness,
			hit.SourceID,
			strconv.Itoa(hit.Index),
			hit.Locator.Path,
			strconv.Itoa(hit.Locator.Line),
			strconv.FormatInt(hit.Locator.ByteOffset, 10),
			hit.Locator.Digest,
			hit.Excerpt,
		}, "|"))
	}
	return echo
}

// serveEvidence is AllowWithinGrant with a payload attached to every allowed
// corpus-search decision: the suite standing in for the facility a real run
// installs behind that capability.
//
// It is a separate policy rather than a change to AllowWithinGrant because
// AllowWithinGrant is a grant-shaped policy and nothing more. A permissive
// policy that also fabricated corpus evidence would be serving material no
// facility produced, into every obligation that uses it, and the obligations
// that grade a denial would be grading a lie.
func serveEvidence(results json.RawMessage) Authorizer {
	within := AllowWithinGrant()
	return AuthorizerFunc(func(ctx context.Context, req ToolRequest) Decision {
		decision := within.Authorize(ctx, req)
		if !decision.Allow || req.Capability != CapabilityCorpusSearch {
			return decision
		}
		decision.Results = results
		return decision
	})
}

// compareEcho grades one echoed array against what Babel sent, naming every
// entry that differs rather than the first. A worker that misread material
// usually misread all of it the same way, and one report of the whole gap is
// one round of work instead of several.
//
// It is shared by the obligations that ask the worker to report back something
// unobservable — the job it decoded, the evidence a decision served it — so
// the wording says "Babel sent" rather than naming either.
func compareEcho(t conformanceT, subject string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("worker reports %d %s entries, Babel sent %d: %q, want %q",
			len(got), subject, len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("worker reports %s %d as %q, Babel sent %q",
				subject, i, got[i], want[i])
		}
	}
}

// conformanceClient builds a Client for one obligation, with budgets tight
// enough to fail fast and loose enough not to be flaky on a busy machine.
func conformanceClient(t conformanceT, target conformanceTarget, adjust func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Binary:      target.binary,
		Args:        target.args,
		Requirement: target.requirement,
		Limits: Limits{
			HandshakeTimeout: 15 * time.Second,
			IdleTimeout:      20 * time.Second,
			ExitGrace:        10 * time.Second,
		},
	}
	if adjust != nil {
		adjust(&cfg)
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// renderReceipt flattens a receipt into one searchable document, so a search
// for a credential covers every string it carries rather than the ones a
// reader happened to name.
//
// It encodes rather than formatting with %+v deliberately. A receipt's result,
// failure and resources are pointers, and %+v renders a pointer as an address:
// a search over that text silently skips the result payload, which is the
// first place a leaking worker puts the token. Encoding walks the whole tree.
// A receipt holds nothing unencodable — its payload was validated as JSON on
// arrival — so an encoding error means the receipt itself is malformed, and
// the message says so instead of returning text that looks searched.
func renderReceipt(receipt *Receipt) string {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Sprintf("unencodable receipt (%v): %+v %+v", err, *receipt, receipt.Result)
	}
	return string(encoded)
}

// conformanceJob is the job every run obligation uses: one recipe, a grant
// covering corpus search and repository reads but deliberately not sandbox
// execution, and a synthetic broker credential.
func conformanceJob(directive string) Job {
	return Job{
		JobID:   "conformance-job",
		RunID:   "conformance-run",
		Profile: ProfileRef{ID: "synthetic-profile", Revision: 1},
		Recipes: []RecipeRef{{ID: "outcome-integrity", Version: 1}},
		Grant: Grant{
			Capabilities: []Capability{CapabilityCorpusSearch, CapabilityRepoRead},
			Disclosure:   DisclosureLocal,
		},
		Sources: []Source{{
			Kind:     "session",
			Selector: "omp/synthetic-session",
			Digest:   "sha256:" + strings.Repeat("0", 64),
		}},
		Broker: Broker{Endpoint: "http://127.0.0.1:1/evidence", Token: conformanceToken},
		Params: map[string]string{ParamConformance: directive},
	}
}

// conformanceRun executes one directive and returns its receipt.
func conformanceRun(t conformanceT, target conformanceTarget, directive string, policy Authorizer, adjust ...func(*Job)) (*Receipt, error) {
	t.Helper()
	client := conformanceClient(t, target, func(cfg *Config) { cfg.Authorizer = policy })
	job := conformanceJob(directive)
	for _, apply := range adjust {
		apply(&job)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	receipt, err := client.Run(ctx, job)
	if receipt == nil {
		t.Fatalf("Run returned no receipt (error %v)", err)
	}
	return receipt, err
}
