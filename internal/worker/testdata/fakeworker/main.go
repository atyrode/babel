// Command fakeworker is a synthetic counterpart for the Code analysis-worker
// protocol. It exists so Babel's side can be tested against a real process
// with real pipes, real signals and a real process tree, without needing Code,
// OMP, a provider credential or any transcript.
//
// It speaks the wire format from string constants and generic maps rather than
// from Babel's internal structs. That is deliberate: the fake must be able to
// emit shapes Babel's decoder does not define — unknown fields, unknown message
// types, malformed lines — and building the messages by hand keeps the fixture
// honest about what actually travels over the pipe.
//
// By default it is well behaved. Each flag makes it misbehave in exactly one
// way, so a test can assert one specific failure at a time.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atyrode/babel/internal/worker"
)

// A production job never carries worker.ParamConformance; this fixture honours
// it because Conformance's obligations cannot be observed otherwise.

func main() {
	var (
		protocol    = flag.String("protocol", worker.ProtocolName, "protocol name to declare")
		versions    = flag.String("versions", strconv.Itoa(worker.ProtocolVersion), "comma-separated protocol versions to declare")
		modes       = flag.String("modes", worker.ModeConfigure+","+worker.ModeWorker, "comma-separated modes to declare")
		record      = flag.String("record", "", "append every line read from stdin to this file")
		containment = flag.String("containment", "full", "containment to declare: full, weak, no-escape, insufficient, none")

		// The staging fixtures. The job document arrives in two stages with
		// the containment declaration between them, so the two ways a worker
		// can break that ordering each need a fixture: one that will not
		// declare until it has been given the material (the version 1
		// worker), and one that writes an event where its declaration
		// belongs.
		awaitMaterial  = flag.Bool("await-material", false, "read stdin for the job material before declaring anything")
		declareLate    = flag.String("declare-late", "", "emit this event type (progress, result) in place of the declaration")
		noHello        = flag.Bool("no-hello", false, "never send hello")
		malformed      = flag.Bool("malformed", false, "emit a line that is not JSON")
		unknownEvent   = flag.Bool("unknown-event", false, "emit an event with an undefined type")
		unknownFields  = flag.Bool("unknown-fields", false, "add undefined fields to every message")
		oversized      = flag.Int("oversized", 0, "emit an event line with a payload of this many bytes")
		exitBefore     = flag.Bool("exit-before-result", false, "exit 0 after progress, without a result")
		stderrOnly     = flag.Bool("stderr-only", false, "after the job, write only to stderr and never exit")
		ignoreTerm     = flag.Bool("ignore-terminate", false, "ignore SIGTERM and sleep")
		grandchild     = flag.String("grandchild", "", "spawn a long-lived grandchild that writes its pid to this file")
		beGrandchild   = flag.String("be-grandchild", "", "internal: act as the grandchild")
		echoToken      = flag.Bool("echo-token", false, "echo the broker credential into stderr, progress, result and tool arguments")
		badSeq         = flag.Bool("bad-seq", false, "repeat a sequence number")
		dupResult      = flag.Bool("duplicate-result", false, "emit two results")
		afterResult    = flag.Bool("event-after-result", false, "emit progress after the result")
		resultAfterErr = flag.Bool("result-after-error", false, "emit a result after an error")
		secretMeta     = flag.Bool("secret-metadata", false, "declare credential-shaped metadata")
		linger         = flag.Duration("linger", 0, "stay alive this long after the terminal event")
		exitCode       = flag.Int("exit-code", 0, "exit status after the result")
		wrongProfile   = flag.Bool("wrong-profile", false, "resolve a different profile than the job named")
		argumentMarker = flag.String("argument-marker", "", "put this value in tool-request arguments")
		dump           = flag.String("dump", "", "write this process's argv and environment to this file")
		toolRequests   = flag.Int("tool-requests", 0, "make this many tool requests, ignoring every denial")
		closeStdout    = flag.Bool("close-stdout", false, "close stdout but keep running")

		// The declarations and the job reading Babel grades by name. Each
		// flag breaks exactly one of them while leaving the rest of the run
		// well behaved, which is what makes the matching obligation
		// discriminating rather than merely present.
		wrongJob           = flag.Bool("wrong-job", false, "answer the echo-job directive from the published fixture instead of the job that arrived")
		wrongEvidence      = flag.Bool("wrong-evidence", false, "answer the echo-evidence directive from a constant instead of the evidence the decision carried")
		ignoreEvidence     = flag.Bool("ignore-evidence", false, "answer the echo-evidence directive from the request it made instead of the answer it got")
		renameMetadata     = flag.Bool("rename-metadata", false, "report the resolved model under a key Babel does not read")
		driftCapability    = flag.Bool("drift-capability", false, "declare an unexercised capability under a name Babel does not define")
		hideCapability     = flag.Bool("hide-capability", false, "omit from the resolved profile the capability this run then exercises")
		unusableCost       = flag.Bool("unusable-cost", false, "report a cost Babel cannot use: a negative rate quoted in no currency")
		noResources        = flag.Bool("no-resources", false, "never report resource use, whatever containment was declared")
		untrackedRes       = flag.Bool("untracked-resources", false, "report the figures a worker with no accounting invents: -1 for unknown and no tool calls")
		ignoreUnderDeclare = flag.Bool("ignore-under-declare", false, "declare the containment -containment names even when the under-declare directive asks for less")

		// The analysis-result shapes. Babel's exploration control plane
		// requires results shaped like hypotheses, observations, findings
		// and challenger objections, and the identifiers a synthesizer
		// consolidates only exist once the run has minted them — so the
		// payload is a template the caller writes and this fixture expands,
		// rather than a shape this fixture knows. It stays honest about the
		// wire: it emits the bytes the template produced and interprets
		// none of them.
		//
		// One process is launched per job, and a caller that needs different
		// results for different jobs cannot vary argv per job — so the
		// selector names a job parameter whose value picks among the
		// prefixed entries. The fixture still knows nothing about what the
		// parameter means.
		resultPayload     payloadFlag
		resultSelector    = flag.String("result-payload-selector", "", "job param whose value selects among SELECTOR=PATH result payloads")
		resultStatus      = flag.String("result-status", worker.StatusOK, "status for the terminal result event")
		resultSchema      = flag.String("result-schema", worker.ResultSchema, "schema the terminal result declares")
		requestCapability = flag.String("request-capability", "", "comma-separated capabilities to request once each, continuing after every decision")
		searchQuery       = flag.String("search-query", "synthetic", "the query value placed in tool-request arguments")

		// The search scope this fixture asks for. Empty sends no scope key
		// at all, which is the request every worker made before the frontier
		// surface existed and must keep meaning "the corpus"; a value is
		// placed in the arguments verbatim, so an unserved scope can be
		// exercised as well as a served one.
		searchScope = flag.String("search-scope", "", "the scope value placed in tool-request arguments")

		// The tool name this fixture puts on the wire. Empty means the
		// conforming behaviour: read grant.tools out of the job and use the
		// name Babel published for the capability. A value overrides it,
		// which is how the negative case for run/published-tool-names is
		// built — "-tool-name babel_corpus_search" is, verbatim, the name a
		// real worker chose for itself while passing every other obligation.
		toolName = flag.String("tool-name", "", "request this tool name instead of the one the job published for the capability")
	)
	flag.Var(&resultPayload, "result-payload",
		"PATH or SELECTOR=PATH; emit that file as the result payload, expanding ${param:KEY}, ${paramitem:KEY:N} and ${paramlist:KEY} from the job's params")
	flag.Parse()

	if *beGrandchild != "" {
		actAsGrandchild(*beGrandchild)
		return
	}
	if *ignoreTerm {
		signal.Ignore(syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	}
	if *dump != "" {
		dumpProcessContext(*dump)
	}

	w := &fake{
		out:                bufio.NewWriter(os.Stdout),
		in:                 bufio.NewReaderSize(os.Stdin, 1<<20),
		unknownFields:      *unknownFields,
		badSeq:             *badSeq,
		argumentMarker:     *argumentMarker,
		searchQuery:        *searchQuery,
		searchScope:        *searchScope,
		toolName:           *toolName,
		schema:             *resultSchema,
		renameMetadata:     *renameMetadata,
		driftCapability:    *driftCapability,
		hideCapability:     *hideCapability,
		unusableCost:       *unusableCost,
		noResources:        *noResources,
		untrackedResources: *untrackedRes,
	}
	if *record != "" {
		file, err := os.OpenFile(*record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fail("opening record file: %v", err)
		}
		defer file.Close()
		w.record = file
	}
	defer w.out.Flush()

	if !*noHello {
		w.emit(map[string]any{
			"type":     worker.MessageHello,
			"protocol": *protocol,
			"versions": parseVersions(*versions),
			"modes":    strings.Split(*modes, ","),
			"worker":   map[string]any{"name": "fakeworker", "version": "0.0.1-synthetic"},
		})
	}

	// A worker that is never going to be accepted still has to be readable
	// while Babel decides, and it must not hang once refused.
	handshake, err := w.next()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeworker: reading handshake: %v\n", err)
		os.Exit(1)
	}
	switch handshake["type"] {
	case worker.MessageRefuse:
		fmt.Fprintf(os.Stderr, "fakeworker: refused: %v\n", handshake["reason"])
		return
	case worker.MessageAccept:
	default:
		fail("unexpected handshake reply %v", handshake["type"])
	}

	mode, _ := handshake["mode"].(string)
	switch mode {
	case worker.ModeConfigure:
		w.emit(w.configuration(worker.ProfileRef{ID: "synthetic-profile", Revision: 1}, *secretMeta, *containment))
		return
	case worker.ModeWorker:
	default:
		fail("unexpected mode %q", mode)
	}

	// The job arrives in two stages (worker.MessageJobPreamble, then
	// worker.MessageJob) with this fixture's containment declaration between
	// them. The preamble carries the profile to resolve and the run's
	// parameters; the material — recipes, grant, sources and the broker
	// credential — arrives only once Babel has accepted the declaration.
	preamble, err := w.next()
	if err != nil {
		fail("reading the job preamble: %v", err)
	}
	if got, _ := preamble["type"].(string); got != worker.MessageJobPreamble {
		fail("expected a %s, got %q", worker.MessageJobPreamble, got)
	}
	profile := profileOf(preamble)
	if *wrongProfile {
		profile.Revision++
	}
	requested := directive(preamble)

	// -await-material is the version 1 worker: it will not declare anything
	// until it holds the whole job, which is the ordering version 2 removed.
	// Babel writes nothing more until the declaration arrives, so this read
	// never returns and the run ends in the idle timeout. The fixture exists
	// so run/declares-from-the-preamble can be shown to fail for exactly that
	// reason rather than being an obligation nothing can flunk.
	if *awaitMaterial {
		if _, err := w.next(); err != nil {
			fail("reading the job material before declaring: %v", err)
		}
		fail("Babel sent the job material to a worker that had declared nothing")
	}

	// -declare-late writes something in place of the declaration it owes, and
	// nothing else: Babel refuses it there, with the material and the
	// credential unwritten. Reading that refusal before leaving is what makes
	// the refusal observable — a fixture that exited first would leave Babel's
	// half of the exchange untested.
	switch *declareLate {
	case "":
	case worker.MessageProgress:
		w.emit(w.progress("discover", "working before declaring anything", nil))
		awaitRefusal(w)
		return
	case worker.MessageResult:
		w.emit(w.result(worker.StatusOK, map[string]any{"analysis": "produced before declaring anything"}, ""))
		awaitRefusal(w)
		return
	default:
		fail("unknown -declare-late event %q", *declareLate)
	}

	// The under-declare directive asks for a declaration Babel must refuse:
	// a named backend, a stated escape assumption, and none of the properties
	// a sandboxed run requires. Honouring it is the conforming behaviour —
	// the refusal path cannot be graded against a worker that always declares
	// enough — and what is graded afterwards is that this process reads the
	// refusal and exits instead of waiting for material it will never be sent.
	//
	// -ignore-under-declare is the fixture that must fail that obligation: it
	// declares its usual boundary and is never refused, which is exactly what
	// a worker that never implemented the directive looks like.
	declaring := *containment
	if requested == worker.ConformanceUnderDeclare && !*ignoreUnderDeclare {
		declaring = "insufficient"
	}
	w.emit(w.configuration(profile, *secretMeta, declaring))

	material, err := w.next()
	if err != nil {
		fail("reading the job material: %v", err)
	}
	switch got, _ := material["type"].(string); got {
	case worker.MessageJob:
	case worker.MessageRefuse:
		// Babel refused the declaration. The material was never written, and
		// a refused worker exits rather than waiting for it.
		fmt.Fprintf(os.Stderr, "fakeworker: declaration refused: %v\n", material["reason"])
		return
	default:
		fail("expected a %s or a %s, got %q", worker.MessageJob, worker.MessageRefuse, got)
	}

	// One document from here on: the two stages are halves of one job, and
	// everything below reads it as such — the token, the published tool names,
	// the recipes and sources the echo-job directive asks about.
	job := merge(preamble, material)
	token := brokerToken(job)

	// echoing is how this fixture answers worker.ConformanceEchoToken: it
	// reports the credential where the directive asks for it — the terminal
	// result's payload and one progress message — and reports it as
	// fixtureRedaction instead of as the token. That is the conforming answer,
	// and it is what makes run/no-credential-leak discriminating: the events
	// the suite asked for exist, the job finishes, and the credential never
	// reaches a pipe.
	//
	// -echo-token is the opposite fixture and the one that obligation must
	// fail: the same channels carry the token verbatim, plus stderr, the
	// result schema and the tool arguments. Which of the two a run is, is the
	// caller's choice; the directive on its own is the well-behaved one.
	echoing := requested == worker.ConformanceEchoToken
	if *echoToken {
		fmt.Fprintf(os.Stderr, "fakeworker: broker token is %s\n", token)
	}

	switch {
	case *malformed:
		w.raw("this line is not JSON at all")
		return
	case *unknownEvent:
		w.emit(map[string]any{"type": "telemetry", "seq": w.nextSeq(), "note": "undefined type"})
		return
	case *oversized > 0:
		w.emit(map[string]any{
			"type": worker.MessageProgress, "seq": w.nextSeq(),
			"stage": "discover", "message": strings.Repeat("x", *oversized),
		})
		return
	case *badSeq:
		w.emit(w.progress("discover", "repeating a sequence number", nil))
		return
	case *stderrOnly:
		for {
			fmt.Fprintln(os.Stderr, "fakeworker: still thinking, honestly")
			time.Sleep(20 * time.Millisecond)
		}
	case *ignoreTerm, *grandchild != "":
		if *grandchild != "" {
			spawnGrandchild(*grandchild)
		}
		w.emit(w.progress("discover", "long-running work", nil))
		time.Sleep(10 * time.Minute)
		return
	case *exitBefore:
		w.emit(w.progress("discover", "stopping without a result", nil))
		return
	case *closeStdout:
		// The stream ends but the process does not: Babel must bound the wait
		// for a reap that is never coming on its own.
		w.emit(w.progress("discover", "closing stdout while still running", nil))
		_ = w.out.Flush()
		_ = os.Stdout.Close()
		time.Sleep(30 * time.Second)
		return
	case *resultAfterErr:
		w.emit(map[string]any{
			"type": worker.MessageError, "seq": w.nextSeq(),
			"code": "synthetic-failure", "message": "deliberate failure", "retryable": false,
		})
		w.emit(w.result(worker.StatusOK, nil, ""))
		return
	}
	if *toolRequests > 0 {
		// A worker that keeps asking after being denied. Babel must stop it
		// rather than let it spin.
		for range *toolRequests {
			w.runTool(job, worker.CapabilityCorpusSearch, token, *echoToken)
		}
	}
	for _, capability := range splitList(*requestCapability) {
		// One request per named capability, in the order named. A denial is
		// not a termination: the fixture records the decision and carries on,
		// which is the obligation a control plane's authorization tests need
		// to be able to observe.
		w.runTool(job, worker.Capability(capability), token, *echoToken)
	}

	// Well-behaved paths, selected by the conformance directive the job
	// carries.
	switch requested {
	case worker.ConformanceErrorOnly:
		w.emit(map[string]any{
			"type": worker.MessageError, "seq": w.nextSeq(),
			"code": "synthetic-failure", "message": "deliberate failure", "retryable": true,
		})
		return
	case worker.ConformanceSlow:
		w.emit(w.progress("investigate", "long-running synthetic work", nil))
		time.Sleep(10 * time.Minute)
		return
	case worker.ConformanceRequestTool:
		w.runTool(job, worker.CapabilityCorpusSearch, token, *echoToken)
	case worker.ConformanceRequestUngranted:
		w.runTool(job, worker.CapabilitySandboxExec, token, *echoToken)
	case worker.ConformanceEchoToken:
		// The credential is reported where the directive asks for it, and the
		// value reported is the placeholder unless -echo-token demands the
		// real thing.
		w.emit(w.progress("investigate",
			"broker credential for the investigator: "+echoed(token, *echoToken), nil))
	case worker.ConformanceEchoJob:
		// The answer travels in the terminal result's payload below; the run
		// itself is otherwise an ordinary well-behaved one.
		w.emit(w.progress("discover", "reading the job", w.resources()))
	case worker.ConformanceEchoEvidence:
		// One corpus-search request under the name the job published, then
		// the answer travels in the terminal result's payload below. The
		// request is the only way the evidence arrives, so the directive
		// cannot be answered without making it.
		w.runTool(job, worker.CapabilityCorpusSearch, token, *echoToken)
	case "request-unknown":
		w.runTool(job, "teleport", token, *echoToken)
	default:
		w.emit(w.progress("discover", "synthetic progress", w.resources()))
	}

	payload := map[string]any{"hypotheses": w.decisions}
	if requested == worker.ConformanceEchoJob {
		payload["job"] = echoJob(job, *wrongJob)
	}
	if requested == worker.ConformanceEchoEvidence {
		payload["served_evidence"] = echoServedEvidence(w.served, w.searchQuery, *wrongEvidence, *ignoreEvidence)
	}
	// A caller-supplied payload replaces everything above it, echo-job answer
	// included. That ordering is what lets a test model the commonest
	// candidate of all: a worker that produced a perfectly good result and
	// never implemented the directive it was asked to answer.
	if path := resultPayload.pick(paramOf(job, *resultSelector)); path != "" {
		payload = loadPayload(path, job)
	}
	if *echoToken || echoing {
		payload["broker_credential"] = echoed(token, *echoToken)
	}
	w.emit(w.result(*resultStatus, payload, tokenIf(*echoToken, token)))

	if *afterResult {
		w.emit(w.progress("cleanup", "after the result", nil))
	}
	if *dupResult {
		w.emit(w.result(*resultStatus, payload, ""))
	}
	if *linger > 0 {
		time.Sleep(*linger)
	}
	w.out.Flush()
	os.Exit(*exitCode)
}

// fake is the synthetic worker's protocol state.
type fake struct {
	out            *bufio.Writer
	in             *bufio.Reader
	record         *os.File
	seq            int
	unknownFields  bool
	badSeq         bool
	argumentMarker string
	searchQuery    string
	// searchScope is the surface this fixture asks its search to read. Empty
	// omits the key, which is what a worker that predates the frontier
	// surface sends.
	searchScope string

	// toolName overrides the tool name this fixture requests. Empty is the
	// conforming behaviour — the name the job published for the capability —
	// and a value is how the fixture models the worker that named the tool
	// itself, which is the drift run/published-tool-names exists to catch and
	// which no other obligation can see.
	toolName string

	// schema is the schema the terminal result declares. It is a dial so a
	// test can present a result Babel cannot read: the schema is shared wire
	// surface between two repositories, and a worker that gets it wrong while
	// getting everything else right is the drift the conformance suite has to
	// catch.
	schema string

	// The declaration dials. Each one breaks a single thing Babel reads by
	// name out of the resolved configuration, so a test can show that the
	// obligation guarding it fails for that reason and nothing else.
	renameMetadata  bool
	driftCapability bool
	hideCapability  bool
	unusableCost    bool

	// The resource-accounting dials: a worker that reports nothing at all,
	// and one that reports the figures an implementation with no accounting
	// invents rather than admitting it has none.
	noResources        bool
	untrackedResources bool

	// decisions records what Babel answered for each tool request, so the
	// result payload can prove the worker actually observed the decision
	// rather than assuming it was allowed.
	decisions []string

	// served is the "results" object off the most recent tool decision that
	// carried one: the evidence Babel attached to an allowed request. It is
	// kept as the generic map the wire produced rather than decoded into a
	// struct, for the reason echoJob is built the same way — the answer has to
	// be evidence that the bytes were parsed.
	//
	// A decision with no "results" leaves it untouched, so an answer built
	// from it reports what was actually received rather than an empty object
	// invented afterwards.
	served map[string]any
}

// nextSeq returns the next event sequence number, or repeats one when the
// fixture is asked to violate the rule.
func (f *fake) nextSeq() int {
	if f.badSeq && f.seq > 0 {
		return f.seq
	}
	f.seq++
	return f.seq
}

// emit writes one message as a single JSON line.
func (f *fake) emit(msg map[string]any) {
	if f.unknownFields {
		msg["x-fakeworker-extension"] = map[string]any{"note": "a field Babel does not define"}
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		fail("encoding %v: %v", msg["type"], err)
	}
	f.raw(string(encoded))
}

// raw writes one line verbatim, which is how the malformed-line fixture works.
func (f *fake) raw(line string) {
	if _, err := f.out.WriteString(line + "\n"); err != nil {
		fail("writing: %v", err)
	}
	if err := f.out.Flush(); err != nil {
		fail("flushing: %v", err)
	}
}

// next reads one line from Babel and records it when recording is on.
func (f *fake) next() (map[string]any, error) {
	line, err := f.in.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if f.record != nil {
		if _, err := f.record.WriteString(line); err != nil {
			return nil, err
		}
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// merge assembles the two job stages into the one document the rest of this
// fixture reads. The stages share job_id and run_id and define no other field
// twice, so the later stage's identity simply confirms the earlier one; a
// mismatch is a pairing from two different runs and is refused rather than
// merged, because analysing one run's material under another's identity is the
// one confusion two messages can create that one could not.
func merge(preamble, material map[string]any) map[string]any {
	for _, key := range []string{"job_id", "run_id"} {
		if stringOf(material[key]) != stringOf(preamble[key]) {
			fail("the job material names %s %q, the preamble named %q",
				key, stringOf(material[key]), stringOf(preamble[key]))
		}
	}
	job := make(map[string]any, len(preamble)+len(material))
	for key, value := range preamble {
		job[key] = value
	}
	for key, value := range material {
		job[key] = value
	}
	// The assembled document is the material message's type: it is the one
	// that arrived last, and nothing downstream reads the field anyway.
	job["type"] = worker.MessageJob
	return job
}

// awaitRefusal reads the refusal Babel writes to a worker whose declaration it
// would not accept, and reports anything else. A worker that is refused exits;
// this fixture returns to main, which flushes and leaves.
func awaitRefusal(f *fake) {
	msg, err := f.next()
	if err != nil {
		fail("waiting for the refusal: %v", err)
	}
	if got, _ := msg["type"].(string); got != worker.MessageRefuse {
		fail("expected a %s after an unacceptable declaration, got %q", worker.MessageRefuse, got)
	}
	fmt.Fprintf(os.Stderr, "fakeworker: declaration refused: %v\n", msg["reason"])
}

// configuration is the resolved-profile message both modes emit.
func (f *fake) configuration(profile worker.ProfileRef, secretMeta bool, containment string) map[string]any {
	metadata := map[string]any{
		"provider": "synthetic",
		"model":    "synthetic-model",
		"thinking": "high",
	}
	if f.renameMetadata {
		// The rename Babel's receipt consumers cannot follow: the value is
		// still here, under a key nobody reads.
		delete(metadata, "model")
		metadata["model_name"] = "synthetic-model"
	}
	if secretMeta {
		metadata["provider_api_key"] = "should-never-be-accepted"
	}
	capabilities := []string{string(worker.CapabilityCorpusSearch), string(worker.CapabilityRepoRead)}
	if f.driftCapability {
		// One facility under a vocabulary that drifted away from Babel's: a
		// name Babel has no boundary for and can never grant. The capability
		// this run exercises is left correct on purpose, so the only thing
		// wrong here is the name of a capability nothing in the run uses.
		capabilities = []string{string(worker.CapabilityCorpusSearch), "repo_read"}
	}
	if f.hideCapability {
		// A profile that omits what the run is about to do. Every name here
		// is one Babel defines; the claim is simply not the profile that ran.
		capabilities = []string{string(worker.CapabilityRepoRead)}
	}
	cost := map[string]any{"currency": "XTS", "input_per_1k": 0, "output_per_1k": 0, "estimated_run": 0}
	if f.unusableCost {
		// One misbehaviour, two symptoms: a cost report nobody can act on.
		// A negative rate reads as a discount and an unnamed currency makes
		// Babel drop the estimate entirely instead of showing it wrongly.
		cost = map[string]any{"currency": "", "input_per_1k": -0.5, "output_per_1k": 1.5, "estimated_run": 2}
	}
	message := map[string]any{
		"type":         worker.MessageConfiguration,
		"seq":          f.nextSeq(),
		"time":         time.Now().UTC().Format(time.RFC3339Nano),
		"profile":      map[string]any{"id": profile.ID, "revision": profile.Revision},
		"privacy":      map[string]any{"disclosure": worker.DisclosureLocal, "redaction_required": false},
		"cost":         cost,
		"capabilities": capabilities,
		"metadata":     metadata,
	}
	// A correct worker declares the boundary it provides. The misbehaviours
	// are separate flags so a test can distinguish "claims nothing" from
	// "claims something insufficient" from "claims containment but no escape
	// assumption" — four different operator situations.
	switch containment {
	case "none":
	case "insufficient":
		// Everything a declaration must state and none of what a sandboxed
		// run requires: this is the shape the under-declare directive asks
		// for, so the refusal it is graded on is about the properties rather
		// than about a missing backend or a missing escape assumption.
		message["containment"] = map[string]any{
			"backend":              "synthetic-none",
			"filesystem_isolation": false,
			"network_default_deny": false,
			"resource_ceilings":    false,
			"disposable":           false,
			"escape":               "synthetic: this fixture contains nothing at all, by request",
		}
	case "weak":
		message["containment"] = map[string]any{
			"backend":              "synthetic-weak",
			"filesystem_isolation": true,
			"network_default_deny": false,
			"resource_ceilings":    false,
			"disposable":           true,
			"escape":               "synthetic: egress is not restricted",
		}
	case "no-escape":
		message["containment"] = map[string]any{
			"backend":              "synthetic",
			"filesystem_isolation": true,
			"network_default_deny": true,
			"resource_ceilings":    true,
			"disposable":           true,
		}
	default:
		message["containment"] = map[string]any{
			"backend":              "synthetic",
			"filesystem_isolation": true,
			"network_default_deny": true,
			"resource_ceilings":    true,
			"disposable":           true,
			"escape":               "synthetic: a fixture contains nothing; it only declares",
		}
	}
	return message
}

// progress is one progress event.
func (f *fake) progress(stage, message string, resources map[string]any) map[string]any {
	msg := map[string]any{
		"type":     worker.MessageProgress,
		"seq":      f.nextSeq(),
		"time":     time.Now().UTC().Format(time.RFC3339Nano),
		"stage":    stage,
		"message":  message,
		"fraction": 0.5,
	}
	if resources != nil {
		msg["resources"] = resources
	}
	return msg
}

// resources is this fixture's self-reported resource use, or nil when it is
// asked to report none. Babel reads an absent object as unknown rather than
// zero, so "reports nothing" and "reports zeros" are different claims and this
// fixture can make either.
//
// The tool-call count is the one figure Babel can check against its own
// record, so it is the one figure this fixture derives from what actually
// happened rather than from a constant.
func (f *fake) resources() map[string]any {
	if f.noResources {
		return nil
	}
	if f.untrackedResources {
		// A worker with no accounting that reports anyway: -1 standing in
		// for "unknown", and a tool-call count it never kept.
		return map[string]any{
			"cpu_seconds": -1, "max_rss_bytes": -1,
			"sandbox_bytes_written": -1, "tool_calls": 0,
		}
	}
	return map[string]any{
		"cpu_seconds": 0.25, "max_rss_bytes": 4096,
		"sandbox_bytes_written": 0, "tool_calls": len(f.decisions),
	}
}

// result is the terminal success event.
func (f *fake) result(status string, payload map[string]any, leak string) map[string]any {
	msg := map[string]any{
		"type":   worker.MessageResult,
		"seq":    f.nextSeq(),
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
		"status": status,
		"schema": f.schema,
	}
	// The terminal report is the run's total, so it is the one Babel keeps:
	// a progress event's figures are a point in time and the last one wins.
	if resources := f.resources(); resources != nil {
		msg["resources"] = resources
	}
	if payload != nil {
		msg["payload"] = payload
	}
	if leak != "" {
		msg["schema"] = f.schema + " " + leak
	}
	return msg
}

// runTool makes one tool request, waits for Babel's decision, and keeps
// working either way — a denial is not a termination.
//
// The tool name comes out of the job, not out of this file. That is the
// conforming behaviour and the reason it is written this way here: a fixture
// that hardcoded the name would pass the obligation for the wrong reason,
// which is exactly how a real worker's invented name survived fourteen of
// them.
func (f *fake) runTool(job map[string]any, capability worker.Capability, token string, echo bool) {
	requestID := fmt.Sprintf("t-%d", len(f.decisions)+1)
	arguments := map[string]any{"query": f.searchQuery}
	if f.searchScope != "" {
		arguments["scope"] = f.searchScope
	}
	if f.argumentMarker != "" {
		arguments["marker"] = f.argumentMarker
	}
	if echo {
		arguments["credential"] = token
	}
	f.emit(map[string]any{
		"type":       worker.MessageToolRequest,
		"seq":        f.nextSeq(),
		"time":       time.Now().UTC().Format(time.RFC3339Nano),
		"request_id": requestID,
		"capability": string(capability),
		"tool":       f.toolFor(job, capability),
		"arguments":  arguments,
		"reason":     "synthetic evidence request",
	})
	decision, err := f.next()
	if err != nil {
		fail("reading tool decision: %v", err)
	}
	if decision["type"] != worker.MessageToolDecision {
		fail("expected a tool-decision, got %v", decision["type"])
	}
	if decision["request_id"] != requestID {
		fail("tool-decision for %v, expected %v", decision["request_id"], requestID)
	}
	verdict, _ := decision["decision"].(string)
	code, _ := decision["code"].(string)
	f.decisions = append(f.decisions, strings.TrimSuffix(verdict+":"+code, ":"))
	// The evidence the decision carried, if it carried any. A conforming
	// worker reads it here: the payload is the only thing that tells it what
	// the corpus holds, and a worker that discards it has been served
	// evidence and learned nothing.
	if results, ok := decision["results"].(map[string]any); ok {
		f.served = results
	}
	f.emit(f.progress("investigate", "continuing after decision "+verdict, nil))
}

// toolFor picks the tool name to request for capability: the override when the
// fixture was given one, otherwise the first name the job published for it.
//
// The fallback matters and is not a guess. Nothing published means Babel said
// no facility in this build serves that capability, and a conforming worker
// requests nothing at all under it — but the obligations that reach here with
// such a capability (an ungranted one, an undefined one) are grading the
// capability boundary, which is decided before the name is ever inspected. So
// the fixture names the one operation this protocol defines and lets the
// earlier check win, rather than inventing a name and muddying which check
// produced the denial.
func (f *fake) toolFor(job map[string]any, capability worker.Capability) string {
	if f.toolName != "" {
		return f.toolName
	}
	if published := publishedToolNames(job, capability); len(published) > 0 {
		return published[0]
	}
	return worker.ToolSearch
}

// publishedToolNames reads grant.tools out of the job: the tool names Babel
// says it serves for one capability.
//
// It reads the generic map the wire produced rather than any struct Babel
// defines, for the same reason echoJob does — the answer has to be evidence
// that the bytes were parsed. A missing key means nothing serves that
// capability; a missing "tools" object entirely would mean a Babel that never
// published one, and those are different facts even though this fixture treats
// both as "no name available".
func publishedToolNames(job map[string]any, capability worker.Capability) []string {
	grant, _ := job["grant"].(map[string]any)
	tools, _ := grant["tools"].(map[string]any)
	published, _ := tools[string(capability)].([]any)
	names := make([]string, 0, len(published))
	for _, entry := range published {
		if name, ok := entry.(string); ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

// directive reads the conformance directive out of the job's params.
func directive(job map[string]any) string {
	return paramOf(job, worker.ParamConformance)
}

// paramOf reads one job parameter, or "" when the key is absent.
func paramOf(job map[string]any, key string) string {
	if key == "" {
		return ""
	}
	params, ok := job["params"].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := params[key].(string)
	return value
}

// echoJob answers worker.ConformanceEchoJob: the job this fixture decoded, in
// the flat form the directive specifies.
//
// It reads the generic map the wire produced rather than any struct Babel
// defines, which is the whole point of the directive — the answer is evidence
// that the bytes were parsed, so building it from a shared type would prove
// only that Go can copy a struct.
//
// -wrong-job is the fixture that must fail the obligation: it answers from the
// published conformance job instead of the one that arrived. The two are
// indistinguishable until Babel plants a per-run nonce in the material, which
// is exactly the property the obligation depends on.
func echoJob(job map[string]any, wrong bool) map[string]any {
	if wrong {
		return map[string]any{
			"recipes": []string{"outcome-integrity@1"},
			"sources": []string{"session|omp/synthetic-session|sha256:" + strings.Repeat("0", 64) + "|"},
		}
	}
	recipes := []string{}
	for _, entry := range arrayOf(job, "recipes") {
		item, _ := entry.(map[string]any)
		id, _ := item["id"].(string)
		version, _ := item["version"].(float64)
		recipes = append(recipes, fmt.Sprintf("%s@%d", id, int(version)))
	}
	sources := []string{}
	for _, entry := range arrayOf(job, "sources") {
		item, _ := entry.(map[string]any)
		parts := make([]string, 0, 4)
		for _, field := range []string{"kind", "selector", "digest", "snapshot"} {
			value, _ := item[field].(string)
			parts = append(parts, value)
		}
		sources = append(sources, strings.Join(parts, "|"))
	}
	return map[string]any{
		"recipes": recipes,
		"sources": sources,
	}
}

// echoServedEvidence answers worker.ConformanceEchoEvidence: the served
// evidence this fixture decoded off the tool decision, in the flat form the
// directive specifies.
//
// It reads the generic map the wire produced for echoJob's reason. Here the
// reason is sharper still: the whole obligation is whether the payload on the
// decision reached the worker's reading of it, and an answer assembled from
// anything else — a struct, a cache, the request the worker sent — would be the
// exact failure being graded.
//
// The array is returned even when nothing was served, so "the directive is
// implemented and the payload was empty" stays a different answer from "the
// directive was never implemented".
//
// Two fixtures must fail the obligation, and they fail it in the two ways a
// real worker does. -wrong-evidence answers from a constant, which is the
// worker that hardcoded a shape it read in a document. -ignore-evidence answers
// from the query it sent, which is the worker that reports what it asked for
// and never looked at what came back — and that is the failure that has
// actually happened, since before this field existed a request was all a worker
// ever had.
func echoServedEvidence(served map[string]any, query string, wrong, ignore bool) map[string]any {
	switch {
	case wrong:
		return map[string]any{"hits": []string{
			"omp|omp-published-fixture|42|/synthetic/published/omp.jsonl|12|3456|" +
				strings.Repeat("0", 64) + "|a constant this fixture was written with",
		}}
	case ignore:
		return map[string]any{"hits": []string{query}}
	}
	hits := []string{}
	values, _ := served["hits"].([]any)
	for _, entry := range values {
		hit, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		locator, _ := hit["locator"].(map[string]any)
		hits = append(hits, strings.Join([]string{
			stringOf(hit["harness"]),
			stringOf(hit["source_id"]),
			integerOf(hit["index"]),
			stringOf(locator["path"]),
			integerOf(locator["line"]),
			integerOf(locator["byte_offset"]),
			stringOf(locator["digest"]),
			stringOf(hit["excerpt"]),
		}, "|"))
	}
	return map[string]any{"hits": hits}
}

// stringOf and integerOf read one decoded JSON value. A JSON number arrives as
// a float64, and the fields it is read for — an event's position in its
// session, a locator's line and byte offset — are integers, so the rendering
// has to be integral rather than whatever %v makes of a float.
func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

func integerOf(value any) string {
	number, ok := value.(float64)
	if !ok {
		return ""
	}
	return strconv.FormatInt(int64(number), 10)
}

// arrayOf reads one top-level job array, or nothing when the field is absent.
func arrayOf(job map[string]any, key string) []any {
	values, _ := job[key].([]any)
	return values
}

// payloadFlag collects the repeated -result-payload values. An entry is
// SELECTOR=PATH, or a bare PATH that applies to every job.
type payloadFlag struct {
	bySelector map[string]string
	fallback   string
}

func (p *payloadFlag) String() string { return p.fallback }

func (p *payloadFlag) Set(value string) error {
	selector, path, prefixed := strings.Cut(value, "=")
	if !prefixed {
		p.fallback = value
		return nil
	}
	if p.bySelector == nil {
		p.bySelector = map[string]string{}
	}
	p.bySelector[selector] = path
	return nil
}

// pick returns the payload for one selector value, falling back to the
// unprefixed entry and then to nothing.
func (p *payloadFlag) pick(selector string) string {
	if path, ok := p.bySelector[selector]; ok {
		return path
	}
	return p.fallback
}

// splitList turns a comma-separated flag into its non-empty entries.
func splitList(list string) []string {
	var out []string
	for _, entry := range strings.Split(list, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// loadPayload reads a result-payload template and expands the job's params
// into it.
//
// Three expansions, all of them needed by a control plane whose durable
// identifiers are minted mid-run. ${param:KEY} substitutes a param's value
// inside a JSON string. ${paramitem:KEY:N} substitutes the Nth entry of a
// comma-separated value, which is how a template names one record out of a
// brief. ${paramlist:KEY} replaces the token — quotes and all — with a JSON
// array of every entry, so a caller writes
// `"observations": ${paramlist:babel.brief.observations}` and gets the
// identifiers Babel actually assigned, without this fixture knowing anything
// about what a consolidation is.
func loadPayload(path string, job map[string]any) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		fail("reading result payload %s: %v", path, err)
	}
	params, _ := job["params"].(map[string]any)
	text := string(raw)
	for key, value := range params {
		text = strings.ReplaceAll(text, "${param:"+key+"}", fmt.Sprint(value))
		entries := splitList(fmt.Sprint(value))
		for i, entry := range entries {
			text = strings.ReplaceAll(text, fmt.Sprintf("${paramitem:%s:%d}", key, i), entry)
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			fail("encoding param list %s: %v", key, err)
		}
		text = strings.ReplaceAll(text, "${paramlist:"+key+"}", string(encoded))
	}
	// Any token left over names a param the job did not carry, which is a
	// broken test rather than a shape worth emitting.
	if i := strings.Index(text, "${param"); i >= 0 {
		fail("result payload %s has an unexpanded token at byte %d", path, i)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		fail("result payload %s is not a JSON object after expansion: %v", path, err)
	}
	return payload
}

// profileOf reads the profile the job named. A worker must resolve exactly
// this one.
func profileOf(job map[string]any) worker.ProfileRef {
	profile, ok := job["profile"].(map[string]any)
	if !ok {
		fail("job carries no profile")
	}
	id, _ := profile["id"].(string)
	revision, _ := profile["revision"].(float64)
	return worker.ProfileRef{ID: id, Revision: int(revision)}
}

// brokerToken reads the run-scoped evidence credential. A real worker hands it
// to the OMP controller; this one only uses it to prove Babel scrubs it.
func brokerToken(job map[string]any) string {
	broker, ok := job["broker"].(map[string]any)
	if !ok {
		return ""
	}
	token, _ := broker["token"].(string)
	return token
}

func tokenIf(echo bool, token string) string {
	if echo {
		return token
	}
	return ""
}

// fixtureRedaction is this fixture's own placeholder for a credential it was
// asked to report. It is deliberately not Babel's redaction marker: the
// conformance obligation must grade a worker's output discipline without
// knowing which placeholder that worker happens to use, so a fixture that
// borrowed Babel's marker would let the grader pass for the wrong reason.
const fixtureRedaction = "<credential withheld by fakeworker>"

// echoed is the value this fixture reports where a credential was asked for:
// the credential itself when the caller wants a worker with no output
// discipline, and the placeholder otherwise.
func echoed(token string, verbatim bool) string {
	if verbatim {
		return token
	}
	return fixtureRedaction
}

// parseVersions turns the flag's comma-separated list into wire integers.
func parseVersions(list string) []int {
	var versions []int
	for _, field := range strings.Split(list, ",") {
		if field = strings.TrimSpace(field); field == "" {
			continue
		}
		value, err := strconv.Atoi(field)
		if err != nil {
			fail("bad version %q: %v", field, err)
		}
		versions = append(versions, value)
	}
	return versions
}

// spawnGrandchild starts a process that outlives a naive kill of the direct
// child, inheriting the protocol pipe on purpose: a supervisor that kills only
// its child would both leak this process and never see stdout reach EOF.
func spawnGrandchild(pidFile string) {
	self, err := os.Executable()
	if err != nil {
		fail("locating self: %v", err)
	}
	cmd := exec.Command(self, "-be-grandchild", pidFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fail("spawning grandchild: %v", err)
	}
	// Deliberately not waited on: the fixture models a sandbox the worker
	// leaves running. Its pid file is waited for, though, so a test that
	// cancels this run always has a pid to check.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	fail("grandchild never published its pid")
}

// actAsGrandchild publishes its pid and then sleeps long enough that only a
// real process-tree kill can end it.
func actAsGrandchild(pidFile string) {
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fail("writing pid file: %v", err)
	}
	time.Sleep(10 * time.Minute)
}

// dumpProcessContext writes this process's argv and environment where a test
// can read them unfiltered. It is how the "no secret on argv or in the
// environment" claim is checked against the real process rather than against
// Babel's own scrubbed diagnostics.
func dumpProcessContext(path string) {
	lines := append([]string{"argv: " + strings.Join(os.Args, " ")}, os.Environ()...)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		fail("writing process dump: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakeworker: "+format+"\n", args...)
	os.Exit(2)
}
