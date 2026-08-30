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
		protocol       = flag.String("protocol", worker.ProtocolName, "protocol name to declare")
		versions       = flag.String("versions", strconv.Itoa(worker.ProtocolVersion), "comma-separated protocol versions to declare")
		modes          = flag.String("modes", worker.ModeConfigure+","+worker.ModeWorker, "comma-separated modes to declare")
		record         = flag.String("record", "", "append every line read from stdin to this file")
		containment    = flag.String("containment", "full", "containment to declare: full, weak, no-escape, none")
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
		requestCapability = flag.String("request-capability", "", "comma-separated capabilities to request once each, continuing after every decision")
		searchQuery       = flag.String("search-query", "synthetic", "the query value placed in tool-request arguments")
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
		out:            bufio.NewWriter(os.Stdout),
		in:             bufio.NewReaderSize(os.Stdin, 1<<20),
		unknownFields:  *unknownFields,
		badSeq:         *badSeq,
		argumentMarker: *argumentMarker,
		searchQuery:    *searchQuery,
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

	job, err := w.next()
	if err != nil {
		fail("reading job: %v", err)
	}
	profile := profileOf(job)
	if *wrongProfile {
		profile.Revision++
	}
	token := brokerToken(job)
	if *echoToken {
		fmt.Fprintf(os.Stderr, "fakeworker: broker token is %s\n", token)
	}

	w.emit(w.configuration(profile, *secretMeta, *containment))

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
			w.runTool(worker.CapabilityCorpusSearch, token, *echoToken)
		}
	}
	for _, capability := range splitList(*requestCapability) {
		// One request per named capability, in the order named. A denial is
		// not a termination: the fixture records the decision and carries on,
		// which is the obligation a control plane's authorization tests need
		// to be able to observe.
		w.runTool(worker.Capability(capability), token, *echoToken)
	}

	// Well-behaved paths, selected by the conformance directive the job
	// carries.
	switch directive(job) {
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
		w.runTool(worker.CapabilityCorpusSearch, token, *echoToken)
	case worker.ConformanceRequestUngranted:
		w.runTool(worker.CapabilitySandboxExec, token, *echoToken)
	case "request-unknown":
		w.runTool("teleport", token, *echoToken)
	default:
		w.emit(w.progress("discover", "synthetic progress", map[string]any{
			"cpu_seconds": 0.25, "max_rss_bytes": 4096, "sandbox_bytes_written": 0, "tool_calls": 0,
		}))
	}

	payload := map[string]any{"hypotheses": w.decisions}
	if path := resultPayload.pick(paramOf(job, *resultSelector)); path != "" {
		payload = loadPayload(path, job)
	}
	if *echoToken {
		payload["leaked"] = token
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

	// decisions records what Babel answered for each tool request, so the
	// result payload can prove the worker actually observed the decision
	// rather than assuming it was allowed.
	decisions []string
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

// configuration is the resolved-profile message both modes emit.
func (f *fake) configuration(profile worker.ProfileRef, secretMeta bool, containment string) map[string]any {
	metadata := map[string]any{
		"provider": "synthetic",
		"model":    "synthetic-model",
		"thinking": "high",
	}
	if secretMeta {
		metadata["provider_api_key"] = "should-never-be-accepted"
	}
	message := map[string]any{
		"type":         worker.MessageConfiguration,
		"seq":          f.nextSeq(),
		"time":         time.Now().UTC().Format(time.RFC3339Nano),
		"profile":      map[string]any{"id": profile.ID, "revision": profile.Revision},
		"privacy":      map[string]any{"disclosure": worker.DisclosureLocal, "redaction_required": false},
		"cost":         map[string]any{"currency": "XTS", "input_per_1k": 0, "output_per_1k": 0, "estimated_run": 0},
		"capabilities": []string{string(worker.CapabilityCorpusSearch), string(worker.CapabilityRepoRead)},
		"metadata":     metadata,
	}
	// A correct worker declares the boundary it provides. The misbehaviours
	// are separate flags so a test can distinguish "claims nothing" from
	// "claims something insufficient" from "claims containment but no escape
	// assumption" — three different operator situations.
	switch containment {
	case "none":
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

// result is the terminal success event.
func (f *fake) result(status string, payload map[string]any, leak string) map[string]any {
	msg := map[string]any{
		"type":   worker.MessageResult,
		"seq":    f.nextSeq(),
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
		"status": status,
		"schema": "babel.analysis-result/1",
	}
	if payload != nil {
		msg["payload"] = payload
	}
	if leak != "" {
		msg["schema"] = "babel.analysis-result/1 " + leak
	}
	return msg
}

// runTool makes one tool request, waits for Babel's decision, and keeps
// working either way — a denial is not a termination.
func (f *fake) runTool(capability worker.Capability, token string, echo bool) {
	requestID := fmt.Sprintf("t-%d", len(f.decisions)+1)
	arguments := map[string]any{"query": f.searchQuery}
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
		"tool":       "search",
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
	f.emit(f.progress("investigate", "continuing after decision "+verdict, nil))
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
