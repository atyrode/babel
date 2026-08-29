package event

import (
	"encoding/json"
	"sort"
	"time"
)

// Codex is a best-effort harness: its rollout log is a faithful record of the
// provider conversation plus a parallel stream of UI events, and it records
// less about what tools did than OMP does.
//
// Record shape. One JSON object per line, {timestamp, type, payload}. The
// types that matter are "response_item" (the provider stream: message,
// reasoning, function_call, function_call_output, custom_tool_call,
// custom_tool_call_output, web_search_call) and "event_msg" (the UI stream:
// user_message, agent_message, patch_apply_end, token_count, task_started,
// task_complete, mcp_tool_call_end). "session_meta", "turn_context", and
// "compacted" are bookkeeping.
//
// Classification rules:
//
//	response_item/message role=user      -> KindUserReport
//	    The provider stream labels the turn as the user's. Codex also wraps
//	    operator text in harness-supplied environment context, so the text
//	    is the turn as sent, not only what the operator typed.
//	response_item/message role=assistant -> KindAgentClaim
//	response_item/message role=developer -> KindOpaque
//	    Harness-authored instructions: neither an operator report nor an
//	    agent claim.
//	response_item/reasoning              -> KindAgentClaim when the summary
//	    holds text, else KindOpaque. Codex usually stores only
//	    encrypted_content, which is opaque by construction.
//	response_item/function_call          -> KindToolObservation
//	response_item/custom_tool_call       -> KindToolObservation
//	response_item/web_search_call        -> KindToolObservation
//	    Tool invocations. exec_command's arguments embed the command line
//	    as JSON, which is where verification vocabulary is matched.
//	*_output                             -> KindVerificationEvidence when
//	    the joined call was a verification command, else
//	    KindToolObservation. Outcome is always empty: this Codex schema
//	    records no exit status for a command, only its combined output, so
//	    an outcome would have to be scraped out of tool text.
//	event_msg/patch_apply_end success     -> KindRepositoryChange
//	    The harness itself lists the changed paths in payload.changes. A
//	    failed apply changed nothing and stays a tool observation.
//	event_msg/mcp_tool_call_end           -> KindToolObservation
//	event_msg/user_message, agent_message -> KindOpaque
//	    These duplicate the canonical response_item/message for the same
//	    turn. Classifying both would double-count every turn, so the echo
//	    is preserved as opaque with its own locator instead.
//	anything else                         -> KindOpaque

// codexRecord is the Codex rollout record.
type codexRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Summary json.RawMessage `json:"summary"`
		// Tool call and output fields.
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Output    json.RawMessage `json:"output"`
		Action    json.RawMessage `json:"action"`
		// patch_apply_end fields.
		Success *bool                      `json:"success"`
		Changes map[string]json.RawMessage `json:"changes"`
		// mcp_tool_call_end fields.
		Invocation struct {
			Server string `json:"server"`
			Tool   string `json:"tool"`
		} `json:"invocation"`
	} `json:"payload"`
}

// codexExecArguments is the JSON document Codex nests inside a function
// call's arguments string. Only the command line matters here.
type codexExecArguments struct {
	Cmd     string `json:"cmd"`
	Command string `json:"command"`
}

func classifyCodex(s *scanner, rec []byte) (bool, error) {
	var r codexRecord
	if json.Unmarshal(rec, &r) != nil {
		return false, nil
	}
	at := recordTime(r.Timestamp)
	switch r.Type {
	case "response_item":
		return classifyCodexResponseItem(s, r, at)
	case "event_msg":
		return classifyCodexEventMsg(s, r, at)
	default:
		return false, nil
	}
}

func classifyCodexResponseItem(s *scanner, r codexRecord, at *time.Time) (bool, error) {
	p := r.Payload
	switch p.Type {
	case "message":
		kind := KindAgentClaim
		switch p.Role {
		case "user":
			kind = KindUserReport
		case "assistant":
			kind = KindAgentClaim
		default:
			return false, nil
		}
		text, ok := partText(p.Content, "input_text", "output_text", "text")
		if !ok {
			return false, nil
		}
		return true, s.push(Event{Kind: kind, Role: p.Role, Time: at, Text: text})
	case "reasoning":
		text, ok := partText(p.Summary, "summary_text", "text")
		if !ok {
			return false, nil
		}
		return true, s.push(Event{Kind: KindAgentClaim, Role: "assistant", Time: at, Text: text})
	case "function_call", "custom_tool_call":
		arguments := p.Arguments
		if arguments == "" {
			arguments = p.Input
		}
		command := codexCommand(arguments)
		s.calls.put(p.CallID, pendingCall{tool: p.Name, verification: isVerificationCommand(command)})
		text := command
		if text == "" {
			text = arguments
		}
		return true, s.push(Event{Kind: KindToolObservation, Role: "assistant", Time: at, Text: clipString(text), Tool: p.Name})
	case "function_call_output", "custom_tool_call_output":
		call, _ := s.calls.take(p.CallID)
		kind := KindToolObservation
		if call.verification {
			kind = KindVerificationEvidence
		}
		text, _ := codexOutputText(p.Output)
		return true, s.push(Event{Kind: kind, Role: "tool", Time: at, Text: text, Tool: call.tool})
	case "web_search_call":
		return true, s.push(Event{Kind: KindToolObservation, Role: "assistant", Time: at, Text: clipString(string(p.Action)), Tool: "web_search"})
	default:
		return false, nil
	}
}

func classifyCodexEventMsg(s *scanner, r codexRecord, at *time.Time) (bool, error) {
	p := r.Payload
	switch p.Type {
	case "patch_apply_end":
		if p.Success == nil || !*p.Success {
			return true, s.push(Event{Kind: KindToolObservation, Role: "tool", Time: at, Tool: "apply_patch", Outcome: OutcomeError})
		}
		paths := make([]string, 0, len(p.Changes))
		for path := range p.Changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		if len(paths) == 0 {
			return false, nil
		}
		return true, s.push(Event{Kind: KindRepositoryChange, Role: "tool", Time: at, Tool: "apply_patch", Paths: paths})
	case "mcp_tool_call_end":
		tool := p.Invocation.Tool
		if p.Invocation.Server != "" && tool != "" {
			tool = p.Invocation.Server + "/" + tool
		}
		return true, s.push(Event{Kind: KindToolObservation, Role: "tool", Time: at, Tool: tool})
	default:
		return false, nil
	}
}

// codexCommand recovers the command line from a function call's arguments,
// which Codex stores as a JSON document inside a JSON string. An argument
// document that is not a command yields "", so unrelated tools are never
// matched against the verification vocabulary.
func codexCommand(arguments string) string {
	if arguments == "" {
		return ""
	}
	var args codexExecArguments
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	if args.Cmd != "" {
		return args.Cmd
	}
	return args.Command
}

// codexOutputText normalizes a tool output, which this schema writes as a
// plain string and occasionally as content parts.
func codexOutputText(raw json.RawMessage) (string, bool) {
	return partText(raw, "output_text", "text", "input_text")
}
