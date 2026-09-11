// Command fakeengine is the synthetic counterpart of `code engine`: it writes
// Code's runtime-info sidecar and then speaks OMP's native RPC on stdio the
// way a contained engine would, with no model behind it.
//
// It exists so internal/worker and internal/explore can be tested without
// Code, OMP, a provider or a credential, and so every misbehaviour Babel's
// supervision must survive can be produced on demand: each flag breaks exactly
// one thing while leaving the rest well behaved, which is what makes the
// corresponding obligation discriminating rather than merely present.
//
// Everything it writes is built from string constants and generic maps, never
// from Babel's own types, so the fixture stays honest about the wire: a test
// that passed because both sides shared a struct would prove only that Go can
// copy a struct. The one Babel-specific thing it reads is the `[babel-params]`
// block internal/explore embeds in its prompts, which lets a result template
// name identifiers Babel minted mid-run.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		// The Code side: what the sidecar declares.
		profileArg     = flag.String("profile-override", "", "resolve this profile instead of the one on argv")
		containment    = flag.String("containment", "full", "containment to declare: full, weak, none, missing")
		secretMeta     = flag.Bool("secret-metadata", false, "declare credential-shaped metadata")
		noRuntimeInfo  = flag.Bool("no-runtime-info", false, "never write the runtime-info file")
		noFinished     = flag.Bool("no-finished-report", false, "never rewrite the runtime-info with measurements")
		describeExit   = flag.Int("describe-exit", 0, "exit status for --describe")
		dump           = flag.String("dump", "", "write this process's argv and environment to this file")
		record         = flag.String("record", "", "append every stdin line to this file")
		promptFile     = flag.String("prompt-file", "", "write the prompt message to this file")
		grandchild     = flag.String("grandchild", "", "spawn a long-lived grandchild that writes its pid to this file")
		beGrandchild   = flag.String("be-grandchild", "", "internal: act as the grandchild")
		ignoreEOF      = flag.Bool("ignore-eof", false, "keep running after stdin closes")
		ignoreTerm     = flag.Bool("ignore-terminate", false, "ignore SIGTERM and sleep")
		exitCode       = flag.Int("exit-code", 0, "exit status after the run")
		stallAfter     = flag.String("stall-after", "", "stop writing after this point: ready, tools, prompt")
		noReady        = flag.Bool("no-ready", false, "never write the ready frame")
		readyVersions  = flag.String("ready-versions", "1,2", "supportedProtocolVersions to advertise")
		badFrame       = flag.String("bad-frame", "", "write this instead of a frame: malformed, oversized, chunk-interleaved, chunk-short")
		refuseCommand  = flag.String("refuse-command", "", "answer this command type with success:false")
		dropTools      = flag.Bool("drop-tools", false, "confirm set_host_tools without the last tool")
		chunkResponses = flag.Bool("chunk", false, "write every frame after negotiation as a v2 chunk sequence")
		localPrompt    = flag.Bool("local-prompt", false, "answer the prompt as completed locally with no model turn")
		nonTerminalEnd = flag.Bool("non-terminal-end", false, "emit an agent_end with isTerminal:false before the real one")
		noEnd          = flag.Bool("no-agent-end", false, "never emit agent_end; close stdout instead")
		extensionUI    = flag.Bool("extension-ui", false, "raise an extension UI request during the turn")
		uriRequest     = flag.Bool("uri-request", false, "raise a host URI request during the turn")
		unknownFrames  = flag.Bool("unknown-frames", false, "emit a frame type Babel does not define")
		statsExit      = flag.Bool("stats-refused", false, "refuse get_session_stats")
		accounting     = flag.Bool("accounting", false, "emit native assistant accounting and fallback events")

		// The model side: which tools the fixture calls and what it submits.
		calls          = flag.String("call", "", "comma-separated tool names to call once each, in order, before submitting")
		callUnknown    = flag.Bool("call-unknown", false, "call a tool the job never registered")
		callCount      = flag.Int("call-repeat", 1, "how many times to make each call")
		searchQuery    = flag.String("search-query", "synthetic", "the query in a corpus-search call")
		searchScope    = flag.String("search-scope", "", "the scope in a corpus-search call; empty omits it")
		searchExtra    = flag.String("search-extra", "", "KEY=VALUE added to the search arguments")
		research       = flag.Bool("research", false, "read the research catalog and fetch the first source it names")
		researchExtra  = flag.String("research-extra", "", "KEY=VALUE added to the fetch arguments")
		submitSelector = flag.String("submit-selector", "", "prompt parameter whose value selects among SELECTOR=PATH submissions")
		submitCount    = flag.Int("submit-repeat", 1, "how many times to submit the payload")
		submitInvalid  = flag.String("submit-invalid", "", "submit this raw JSON (after the valid submission) that the job should refuse")
		submitBefore   = flag.Bool("submit-invalid-first", false, "submit the invalid payload before the valid one")
		noSubmit       = flag.Bool("no-submit", false, "end the turn without submitting")
		servedFile     = flag.String("served-file", "", "append every tool result text to this file")
		linger         = flag.Duration("linger", 0, "stay alive this long after stdin closes")
		submissions    payloadFlag
	)
	flag.Var(&submissions, "submit", "PATH or SELECTOR=PATH; submit that file as the result, expanding ${param:KEY}, ${paramitem:KEY:N} and ${paramlist:KEY} from the prompt's [babel-params] block")
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

	// The engine subcommand and its flags come after this fixture's own,
	// which is how Babel composes argv: the operator's arguments first.
	engine := engineArgs(flag.Args())
	profile := engine.profile
	if *profileArg != "" {
		profile = *profileArg
	}
	report := runtimeReport(profile, *containment, *secretMeta)

	if engine.describe {
		delete(report, "containment")
		json.NewEncoder(os.Stdout).Encode(report)
		os.Exit(*describeExit)
	}

	f := &engine_{
		out:      bufio.NewWriter(os.Stdout),
		in:       bufio.NewReaderSize(os.Stdin, 1<<20),
		stall:    *stallAfter,
		refuse:   *refuseCommand,
		badFrame: *badFrame,
	}
	if *record != "" {
		file, err := os.OpenFile(*record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fail("opening record file: %v", err)
		}
		defer file.Close()
		f.record = file
	}
	if *servedFile != "" {
		file, err := os.OpenFile(*servedFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fail("opening served file: %v", err)
		}
		defer file.Close()
		f.served = file
	}
	defer f.out.Flush()

	// Code writes the sidecar before it forwards a byte, so the ready frame
	// is the first thing Babel sees and the file already exists.
	if !*noRuntimeInfo && engine.runtimeInfo != "" {
		writeReport(engine.runtimeInfo, report)
	}
	if *grandchild != "" {
		spawnGrandchild(*grandchild)
	}
	if !*noReady {
		f.emit(map[string]any{
			"type":                      "ready",
			"protocolVersion":           1,
			"supportedProtocolVersions": parseVersions(*readyVersions),
			"maxFrameBytes":             1 << 20,
			"maxReassembledFrameBytes":  64 << 20,
		})
	}
	if f.stall == "ready" {
		sleepForever()
	}

	// Commands until the prompt. An engine answers whatever arrives; this one
	// expects Babel's fixed sequence and answers each in kind.
	var prompt string
	var toolNames []string
	for prompt == "" {
		cmd, err := f.next()
		if err != nil {
			fmt.Fprintf(os.Stderr, "fakeengine: stdin closed before a prompt: %v\n", err)
			f.leave(*ignoreEOF, *linger, *exitCode)
			return
		}
		kind, _ := cmd["type"].(string)
		id, _ := cmd["id"].(string)
		switch kind {
		case "negotiate_protocol":
			f.respond(id, kind, map[string]any{"protocolVersion": 2})
			f.chunked = *chunkResponses
		case "set_host_tools":
			toolNames = toolNames[:0]
			for _, entry := range arrayOf(cmd, "tools") {
				tool, _ := entry.(map[string]any)
				name, _ := tool["name"].(string)
				toolNames = append(toolNames, name)
			}
			confirmed := toolNames
			if *dropTools && len(confirmed) > 0 {
				confirmed = confirmed[:len(confirmed)-1]
			}
			f.respond(id, kind, map[string]any{"toolNames": confirmed})
			if f.stall == "tools" {
				sleepForever()
			}
		case "prompt":
			prompt, _ = cmd["message"].(string)
			if *promptFile != "" {
				if err := os.WriteFile(*promptFile, []byte(prompt), 0o600); err != nil {
					fail("writing prompt file: %v", err)
				}
			}
			if *localPrompt {
				f.respond(id, kind, map[string]any{"agentInvoked": false})
				f.awaitClose(*ignoreEOF, *linger, *exitCode, engine.runtimeInfo, report, *noFinished)
				return
			}
			f.respond(id, kind, map[string]any{"agentInvoked": true})
		default:
			f.respond(id, kind, nil)
		}
	}
	if f.stall == "prompt" {
		sleepForever()
	}

	params := promptParams(prompt)
	// The conversation opens with what Babel asked, the way a real turn's
	// message list does: the job document is the user message.
	f.say(map[string]any{
		"role":    "user",
		"content": []any{map[string]any{"type": "text", "text": prompt}},
	})
	f.emit(map[string]any{"type": "agent_start"})
	f.emit(map[string]any{"type": "turn_start"})
	if *accounting {
		f.emit(map[string]any{"type": "message_end", "message": map[string]any{
			"role": "user", "content": "private user text", "timestamp": 1000,
		}})
		f.emit(map[string]any{"type": "retry_fallback_applied", "from": "declared/primary", "to": "gateway/actual-2", "role": "default"})
		f.emit(map[string]any{"type": "message_end", "message": map[string]any{
			"role": "assistant", "provider": "gateway", "model": "actual-2",
			"upstreamProvider": "native-provider", "upstreamModel": "concrete-2",
			"timestamp": 1001, "completedAt": 1010, "responseId": "response-2", "stopReason": "toolUse",
			"content": []any{map[string]any{"type": "text", "text": "private assistant text"},
				map[string]any{"type": "toolCall", "name": "search", "arguments": map[string]any{"query": "private tool arguments"}}},
			"usage": json.RawMessage(`{"input":1200,"output":340,"reasoningTokens":140,"cacheRead":80,"cacheWrite":20,"totalTokens":1646,"contextTokens":1300,"premiumRequests":0.5,"orchestration":{"input":1,"cacheRead":2,"output":3},"cttl":{"ephemeral5m":15,"ephemeral1h":5},"server":{"webSearch":1,"webFetch":0},"credits":{"cost":0.4,"committedCost":0.3,"acuCost":0.2},"cost":{"input":0.01,"output":0.02,"cacheRead":0.001,"cacheWrite":0.002,"total":0.033}}`),
		}})
		f.emit(map[string]any{"type": "retry_fallback_succeeded", "model": "gateway/actual-2", "role": "default"})
		// An applied fallback is not proof of success; a failed assistant
		// message may have no usage at all.
		f.emit(map[string]any{"type": "retry_fallback_applied", "from": "gateway/actual-2", "to": "last-resort", "role": "default"})
		f.emit(map[string]any{"type": "message_end", "message": map[string]any{
			"role": "assistant", "model": "last-resort", "stopReason": "error",
			"content": []any{map[string]any{"type": "text", "text": "private error text"}},
		}})
	}
	if *unknownFrames {
		f.emit(map[string]any{"type": "telemetry_sample", "value": 1})
	}
	if *extensionUI {
		f.emit(map[string]any{"type": "extension_ui_request", "id": "ui-1", "method": "confirm", "title": "Confirm", "message": "Continue?"})
		f.expectReply("extension_ui_response")
	}
	if *uriRequest {
		f.emit(map[string]any{"type": "host_uri_request", "id": "uri-1", "operation": "read", "url": "db://users/1"})
		f.expectReply("host_uri_result")
	}

	// The model's calls: evidence tools first, then the submission.
	for range *callCount {
		for _, name := range splitList(*calls) {
			f.call(name, searchArguments(name, *searchQuery, *searchScope, *searchExtra))
		}
		if *callUnknown {
			f.call("babel_never_registered", map[string]any{})
		}
	}
	if *research {
		f.runResearch(toolNames, *researchExtra)
	}
	invalidFirst := *submitBefore && *submitInvalid != ""
	if invalidFirst {
		f.callRaw("babel_submit_result", json.RawMessage(*submitInvalid))
	}
	if !*noSubmit {
		path := submissions.pick(params[*submitSelector])
		payload := json.RawMessage("{}")
		if path != "" {
			payload = loadPayload(path, params)
		}
		for range *submitCount {
			f.callRaw("babel_submit_result", payload)
		}
	}
	if *submitInvalid != "" && !invalidFirst {
		f.callRaw("babel_submit_result", json.RawMessage(*submitInvalid))
	}

	f.emit(map[string]any{"type": "turn_end"})
	if *nonTerminalEnd {
		// A non-terminal end reports the conversation so far, and the
		// terminal one below reports it again with the closing message
		// appended. That is what a real engine does, so anything persisting
		// these frames has to handle the repetition rather than the ideal
		// case of one final list.
		f.emit(map[string]any{"type": "agent_end", "messages": f.messages, "isTerminal": false})
	}
	// The turn closes with the model's own last word, which a transcript
	// keeps verbatim: it is the agent's text, not anything a facility served.
	f.say(map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "the synthetic turn is complete"}},
	})
	if *noEnd {
		f.out.Flush()
		os.Stdout.Close()
		f.awaitClose(*ignoreEOF, *linger, *exitCode, engine.runtimeInfo, report, *noFinished)
		return
	}
	f.emit(map[string]any{"type": "agent_end", "messages": f.messages, "isTerminal": true})

	// After the turn Babel asks for stats and closes stdin.
	for {
		cmd, err := f.next()
		if err != nil {
			f.awaitClose(*ignoreEOF, *linger, *exitCode, engine.runtimeInfo, report, *noFinished)
			return
		}
		kind, _ := cmd["type"].(string)
		id, _ := cmd["id"].(string)
		switch kind {
		case "get_session_stats":
			if *statsExit {
				f.emit(map[string]any{"type": "response", "id": id, "command": kind, "success": false, "error": "stats unavailable"})
				continue
			}
			if *accounting {
				f.respond(id, kind, map[string]any{
					"tokens": map[string]any{"input": 1200, "output": 340, "reasoning": 140, "cacheRead": 80, "cacheWrite": 20, "total": 1646},
					"cost":   0.033, "toolCalls": f.calls, "assistantMessages": 2,
				})
				continue
			}
			f.respond(id, kind, map[string]any{
				"tokens":            map[string]any{"input": 1200, "output": 340, "reasoning": 0, "cacheRead": 0, "cacheWrite": 0, "total": 1540},
				"cost":              0.0123,
				"toolCalls":         f.calls,
				"assistantMessages": 1,
			})
		default:
			f.respond(id, kind, nil)
		}
	}
}

// engine_ is the fixture's protocol state.
type engine_ struct {
	out      *bufio.Writer
	in       *bufio.Reader
	record   *os.File
	served   *os.File
	stall    string
	refuse   string
	badFrame string
	chunked  bool
	calls    int
	hostIDs  int

	// messages is the conversation as a real engine reports it: OMP's own
	// message objects, whole, in every agent_end frame. It is accumulated
	// as generic maps like everything else the fixture writes, so a test
	// that reads a transcript out of it is reading the wire rather than a
	// struct both sides share.
	//
	// A tool result goes in verbatim, exactly as the engine received it from
	// Babel — which is what makes a served corpus excerpt reach anything
	// that persists these messages, and therefore what the redaction on that
	// path has to be proven against.
	messages []any
}

// say appends one message to the conversation the next agent_end reports.
func (f *engine_) say(message map[string]any) {
	f.messages = append(f.messages, message)
}

// emit writes one frame, honouring the negotiated framing and the one
// misbehaviour the fixture was asked for.
func (f *engine_) emit(frame map[string]any) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		fail("encoding frame: %v", err)
	}
	switch f.badFrame {
	case "malformed":
		f.writeLine([]byte("{this is not json"))
		f.badFrame = ""
		return
	case "oversized":
		f.writeLine([]byte(`{"type":"notice","text":"` + strings.Repeat("x", 2<<20) + `"}`))
		f.badFrame = ""
		return
	case "chunk-interleaved":
		f.writeChunks(encoded, "a", 2, true)
		f.badFrame = ""
		return
	case "chunk-short":
		f.writeChunks(encoded, "b", 2, false)
		f.writeLine([]byte(`{"type":"notice"}`))
		f.badFrame = ""
		return
	}
	if f.chunked {
		f.writeChunks(encoded, "", 0, false)
		return
	}
	f.writeLine(encoded)
}

// writeChunks writes encoded as a v2 chunk sequence in segments of at most
// 256 KiB, or in `count` segments when count is set. interleave writes a
// chunk of another sequence between them; a count with no completion writes
// only the first chunk.
func (f *engine_) writeChunks(encoded []byte, suffix string, count int, interleave bool) {
	f.hostIDs++
	id := "rpc-" + strconv.Itoa(f.hostIDs) + suffix
	segment := 256 << 10
	if count > 0 {
		segment = (len(encoded) + count - 1) / count
		if segment == 0 {
			segment = 1
		}
	}
	total := (len(encoded) + segment - 1) / segment
	if total == 0 {
		total = 1
	}
	for i := range total {
		end := min((i+1)*segment, len(encoded))
		chunk := map[string]any{
			"type":       "rpc_chunk",
			"chunkId":    id,
			"index":      i,
			"count":      total,
			"byteLength": len(encoded),
			"data":       base64.StdEncoding.EncodeToString(encoded[i*segment : end]),
		}
		line, _ := json.Marshal(chunk)
		f.writeLine(line)
		if count > 0 && !interleave {
			return
		}
		if interleave && i == 0 {
			other, _ := json.Marshal(map[string]any{"type": "rpc_chunk", "chunkId": id + "-other", "index": 0, "count": 2, "byteLength": 2, "data": "e30="})
			f.writeLine(other)
		}
	}
}

func (f *engine_) writeLine(line []byte) {
	f.out.Write(line)
	f.out.WriteByte('\n')
	f.out.Flush()
}

// respond answers one command. A command named by -refuse-command is refused
// instead.
func (f *engine_) respond(id, command string, data map[string]any) {
	if f.refuse == command {
		f.emit(map[string]any{"type": "response", "id": id, "command": command, "success": false, "error": "refused by fixture"})
		return
	}
	frame := map[string]any{"type": "response", "id": id, "command": command, "success": true}
	if data != nil {
		frame["data"] = data
	}
	f.emit(frame)
}

// next reads one inbound line as a generic object.
func (f *engine_) next() (map[string]any, error) {
	for {
		line, err := f.in.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) == 0 {
			if err != nil {
				return nil, err
			}
			continue
		}
		if f.record != nil {
			f.record.Write(line)
		}
		var msg map[string]any
		if jsonErr := json.Unmarshal(line, &msg); jsonErr != nil {
			return nil, fmt.Errorf("stdin line is not JSON: %v", jsonErr)
		}
		return msg, err
	}
}

// expectReply reads one inbound message and requires it to be of the type
// Babel owes for the request just raised.
func (f *engine_) expectReply(want string) map[string]any {
	msg, err := f.next()
	if err != nil {
		fail("waiting for a %s: %v", want, err)
	}
	if got, _ := msg["type"].(string); got != want {
		fail("expected a %s, got %q", want, got)
	}
	return msg
}

// call makes one host tool call and returns the text Babel answered with.
func (f *engine_) call(name string, arguments map[string]any) (string, bool) {
	encoded, _ := json.Marshal(arguments)
	return f.callRaw(name, encoded)
}

func (f *engine_) callRaw(name string, arguments json.RawMessage) (string, bool) {
	f.calls++
	f.hostIDs++
	id := "host_" + strconv.Itoa(f.hostIDs)
	callID := "toolu_" + strconv.Itoa(f.calls)
	f.say(map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{
			"type": "toolCall", "id": callID, "name": name, "arguments": arguments,
		}},
	})
	f.emit(map[string]any{
		"type":       "tool_execution_start",
		"toolCallId": callID,
		"toolName":   name,
	})
	f.emit(map[string]any{
		"type":       "host_tool_call",
		"id":         id,
		"toolCallId": callID,
		"toolName":   name,
		"arguments":  arguments,
	})
	reply := f.expectReply("host_tool_result")
	if got, _ := reply["id"].(string); got != id {
		fail("host_tool_result for %q, want %q", got, id)
	}
	isError, _ := reply["isError"].(bool)
	text := resultText(reply)
	if f.served != nil {
		fmt.Fprintf(f.served, "%s\t%t\t%s\n", name, isError, text)
	}
	f.say(map[string]any{
		"role":       "toolResult",
		"toolCallId": callID,
		"toolName":   name,
		"isError":    isError,
		"content":    []any{map[string]any{"type": "text", "text": text}},
	})
	f.emit(map[string]any{"type": "tool_execution_end", "toolCallId": callID, "toolName": name, "isError": isError})
	return text, isError
}

// resultText reads the text content out of a host_tool_result.
func resultText(reply map[string]any) string {
	result, _ := reply["result"].(map[string]any)
	var b strings.Builder
	for _, entry := range arrayOf(result, "content") {
		block, _ := entry.(map[string]any)
		text, _ := block["text"].(string)
		b.WriteString(text)
	}
	return b.String()
}

// runResearch exercises the public-research broker the way the facility
// requires: read the catalog Babel serves, then fetch a source by the opaque
// identifier that catalog gave it.
func (f *engine_) runResearch(tools []string, extra string) {
	if !contains(tools, "babel_research_sources") || !contains(tools, "babel_research_fetch") {
		f.emit(map[string]any{"type": "notice", "text": "public research published no catalog or no fetch"})
		return
	}
	text, isError := f.call("babel_research_sources", map[string]any{})
	if isError {
		return
	}
	var catalog struct {
		Sources []struct {
			ID string `json:"id"`
		} `json:"sources"`
	}
	if json.Unmarshal([]byte(text), &catalog) != nil || len(catalog.Sources) == 0 {
		return
	}
	arguments := map[string]any{"source": catalog.Sources[0].ID}
	if key, value, ok := strings.Cut(extra, "="); ok {
		arguments[key] = value
	}
	f.call("babel_research_fetch", arguments)
}

// awaitClose is the engine's tail: read until stdin closes, then dispose and
// exit, writing Code's finished report on the way out.
func (f *engine_) awaitClose(ignoreEOF bool, linger time.Duration, code int, runtimeInfo string, report map[string]any, noFinished bool) {
	for {
		if _, err := f.next(); err != nil {
			break
		}
	}
	f.out.Flush()
	if !noFinished && runtimeInfo != "" {
		finished := map[string]any{}
		for k, v := range report {
			finished[k] = v
		}
		finished["finished"] = true
		finished["exit_code"] = code
		finished["resources"] = map[string]any{"cpu_seconds": 0.42, "max_rss_bytes": 123456789}
		finished["resources_provenance"] = "synthetic"
		writeReport(runtimeInfo, finished)
	}
	f.leave(ignoreEOF, linger, code)
}

func (f *engine_) leave(ignoreEOF bool, linger time.Duration, code int) {
	if ignoreEOF {
		sleepForever()
	}
	if linger > 0 {
		time.Sleep(linger)
	}
	os.Exit(code)
}

// engineArgv is what Babel put after the fixture's own flags.
type engineArgv struct {
	profile     string
	runtimeInfo string
	describe    bool
}

func engineArgs(args []string) engineArgv {
	var out engineArgv
	if len(args) == 0 || args[0] != "engine" {
		fail("expected the engine subcommand, got %v", args)
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--profile":
			i++
			out.profile = args[i]
		case "--runtime-info":
			i++
			out.runtimeInfo = args[i]
		case "--describe":
			out.describe = true
		default:
			fail("unexpected engine argument %q", args[i])
		}
	}
	return out
}

// runtimeReport is the code.runtime/1 document for one launch.
func runtimeReport(profile, containment string, secretMeta bool) map[string]any {
	id, revision := "synthetic-profile", 1
	if profile != "" {
		if name, rev, ok := strings.Cut(profile, "@"); ok {
			id = name
			revision, _ = strconv.Atoi(rev)
		} else {
			id = profile
		}
	}
	metadata := map[string]any{"provider": "synthetic", "model": "synthetic-1", "thinking": "low"}
	if secretMeta {
		metadata["api_key"] = "sk-synthetic-should-never-be-stored"
	}
	report := map[string]any{
		"schema":   "code.runtime/1",
		"worker":   map[string]any{"name": "fakeengine", "version": "0.0.1-synthetic"},
		"profile":  map[string]any{"id": id, "revision": revision},
		"privacy":  map[string]any{"disclosure": "local", "redaction_required": false},
		"cost":     map[string]any{"currency": "USD", "input_per_1k": 0.001, "output_per_1k": 0.002, "estimated_run": 0.05},
		"metadata": metadata,
		"finished": false,
	}
	switch containment {
	case "full":
		report["containment"] = map[string]any{"backend": "synthetic-bwrap", "filesystem_isolation": true,
			"network_default_deny": true, "resource_ceilings": true, "disposable": true, "escape": "none modelled"}
	case "weak":
		report["containment"] = map[string]any{"backend": "synthetic-bwrap", "filesystem_isolation": true,
			"network_default_deny": false, "resource_ceilings": false, "disposable": true, "escape": "network is open"}
	case "none":
		report["containment"] = map[string]any{"backend": "", "escape": ""}
	case "missing":
	default:
		fail("unknown -containment %q", containment)
	}
	return report
}

// writeReport writes the sidecar atomically at mode 0600, as Code does.
func writeReport(path string, report map[string]any) {
	encoded, err := json.Marshal(report)
	if err != nil {
		fail("encoding runtime-info: %v", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		fail("writing runtime-info: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		fail("renaming runtime-info: %v", err)
	}
}

// promptParams reads the `[babel-params]` block internal/explore embeds in a
// prompt: one `key = value` per line until a blank line or `[end]`.
func promptParams(prompt string) map[string]string {
	params := map[string]string{}
	_, rest, ok := strings.Cut(prompt, "[babel-params]\n")
	if !ok {
		return params
	}
	for _, line := range strings.Split(rest, "\n") {
		if line == "" || line == "[end]" {
			break
		}
		key, value, ok := strings.Cut(line, " = ")
		if ok {
			params[key] = value
		}
	}
	return params
}

// searchArguments composes one evidence call's arguments.
func searchArguments(tool, query, scope, extra string) map[string]any {
	arguments := map[string]any{}
	if tool == "babel_corpus_search" {
		arguments["query"] = query
		if scope != "" {
			arguments["scope"] = scope
		}
	}
	if key, value, ok := strings.Cut(extra, "="); ok {
		arguments[key] = value
	}
	return arguments
}

// loadPayload reads a submission template and expands the prompt's params
// into it.
//
// Three expansions, all of them needed by a control plane whose durable
// identifiers are minted mid-run. ${param:KEY} substitutes a param's value
// inside a JSON string. ${paramitem:KEY:N} substitutes the Nth entry of a
// comma-separated value, which is how a template names one record out of a
// brief. ${paramlist:KEY} replaces the token — quotes and all — with a JSON
// array of every entry.
func loadPayload(path string, params map[string]string) json.RawMessage {
	raw, err := os.ReadFile(path)
	if err != nil {
		fail("reading submission %s: %v", path, err)
	}
	text := string(raw)
	for key, value := range params {
		text = strings.ReplaceAll(text, "${param:"+key+"}", value)
		entries := splitList(value)
		for i, entry := range entries {
			text = strings.ReplaceAll(text, fmt.Sprintf("${paramitem:%s:%d}", key, i), entry)
		}
		encoded, _ := json.Marshal(entries)
		text = strings.ReplaceAll(text, "${paramlist:"+key+"}", string(encoded))
	}
	if i := strings.Index(text, "${param"); i >= 0 {
		fail("submission %s has an unexpanded token at byte %d", path, i)
	}
	if !json.Valid([]byte(text)) {
		fail("submission %s is not JSON after expansion", path)
	}
	return json.RawMessage(text)
}

// payloadFlag collects the repeated -submit values. An entry is
// SELECTOR=PATH, or a bare PATH that applies to every job.
type payloadFlag struct {
	bySelector map[string]string
	fallback   string
}

func (p *payloadFlag) String() string { return p.fallback }

func (p *payloadFlag) Set(value string) error {
	selector, path, prefixed := strings.Cut(value, "=")
	if !prefixed || strings.Contains(selector, string(filepath.Separator)) {
		p.fallback = value
		return nil
	}
	if p.bySelector == nil {
		p.bySelector = map[string]string{}
	}
	p.bySelector[selector] = path
	return nil
}

func (p *payloadFlag) pick(selector string) string {
	if path, ok := p.bySelector[selector]; ok {
		return path
	}
	return p.fallback
}

func arrayOf(obj map[string]any, key string) []any {
	values, _ := obj[key].([]any)
	return values
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func splitList(list string) []string {
	var out []string
	for _, entry := range strings.Split(list, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

func parseVersions(list string) []int {
	var out []int
	for _, entry := range splitList(list) {
		v, err := strconv.Atoi(entry)
		if err != nil {
			fail("bad version %q", entry)
		}
		out = append(out, v)
	}
	return out
}

func sleepForever() { time.Sleep(10 * time.Minute) }

// spawnGrandchild starts a copy of this binary that outlives the fixture, so a
// test can prove Babel's teardown reaches the whole process tree.
func spawnGrandchild(pidFile string) {
	self, err := os.Executable()
	if err != nil {
		fail("locating self: %v", err)
	}
	cmd := exec.Command(self, "-be-grandchild", pidFile)
	if err := cmd.Start(); err != nil {
		fail("spawning grandchild: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
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
// can read them unfiltered.
func dumpProcessContext(path string) {
	lines := append([]string{"argv: " + strings.Join(os.Args, " ")}, os.Environ()...)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		fail("writing process dump: %v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fakeengine: "+format+"\n", args...)
	os.Exit(2)
}
