package event

import (
	"encoding/json"
	"time"
)

// OMP is the reference harness: its primary log is the highest-fidelity
// source Babel has, so its classification rules are the most specific and
// every other harness is measured against them.
//
// Record shape. One JSON object per line with a top-level "type". Only
// "message" records carry evidence; "session", "title", "title_change",
// "model_change", "thinking_level_change", "custom", and "custom_message"
// are session bookkeeping and harness-injected notices. A message record is
// {type, id, parentId, timestamp, message}, and message is one of three
// roles: "user", "assistant" (content parts of type "text", "thinking", and
// "toolCall"), and "toolResult" (with toolCallId, toolName, isError, and a
// per-tool details object).
//
// Classification rules, each justified by the evidence in the record:
//
//	role=user, text parts            -> KindUserReport
//	    The harness attributes the turn to the operator.
//	role=assistant, text parts       -> KindAgentClaim
//	    A statement the agent made about the work.
//	role=assistant, thinking parts   -> KindAgentClaim
//	    Also the agent's own assertion. The event model of §4.1 has no
//	    reasoning category and no field to mark one, so reasoning is a
//	    claim; it is emitted as its own event so it is never joined to the
//	    agent's final text.
//	role=assistant, toolCall part    -> KindToolObservation
//	    The harness recorded a tool invocation with its name and
//	    arguments. The invocation is where the command line lives, so it
//	    is also where verification vocabulary is matched; the outcome
//	    arrives with the result record and is joined by tool call id.
//	role=toolResult, edit or write   -> KindRepositoryChange
//	    The tool whose entire purpose is mutating a file reported success,
//	    and the record (details.path) or its call (arguments.path) names
//	    the file. A failed edit or write changed nothing and stays a tool
//	    observation.
//	role=toolResult, verifying call  -> KindVerificationEvidence
//	    The joined call ran a command in the verification vocabulary and
//	    this record carries its exit status.
//	role=toolResult, otherwise       -> KindToolObservation
//	anything else                    -> KindOpaque
//
// Outcome comes only from what OMP records explicitly: isError=false means
// the tool succeeded (pass), isError=true with details.exitCode means the
// command ran and failed (fail), and isError=true without an exit code
// means the tool itself failed (error).

// ompRecord is the OMP primary-log record. Fields Babel does not classify on
// are omitted rather than decoded.
type ompRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		IsError    bool            `json:"isError"`
		Details    struct {
			Path     string           `json:"path"`
			ExitCode *json.RawMessage `json:"exitCode"`
		} `json:"details"`
	} `json:"message"`
}

// ompPart is one assistant content part. A toolCall part carries the tool
// name and its arguments object.
type ompPart struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		Intent  string `json:"i"`
	} `json:"arguments"`
}

// ompMutatingTools are the OMP tools that exist to change files, so a
// successful result from one is a repository change. bash can also change
// files, but the record does not say which, so a bash result stays a tool
// observation rather than a repository change with invented paths.
var ompMutatingTools = map[string]bool{"edit": true, "write": true}

func classifyOMP(s *scanner, rec []byte) (bool, error) {
	var r ompRecord
	if json.Unmarshal(rec, &r) != nil || r.Type != "message" {
		return false, nil
	}
	at := recordTime(r.Timestamp)
	switch r.Message.Role {
	case "user":
		text, ok := partText(r.Message.Content, "text")
		if !ok {
			return false, nil
		}
		return true, s.push(Event{Kind: KindUserReport, Role: "user", Time: at, Text: text})
	case "assistant":
		return classifyOMPAssistant(s, r, at)
	case "toolResult":
		return classifyOMPToolResult(s, r, at)
	default:
		return false, nil
	}
}

// classifyOMPAssistant walks the content parts in order, grouping
// consecutive parts of the same category into one event so a record's
// prose, its reasoning, and each of its tool calls stay distinguishable.
func classifyOMPAssistant(s *scanner, r ompRecord, at *time.Time) (bool, error) {
	var parts []ompPart
	if json.Unmarshal(r.Message.Content, &parts) != nil {
		return false, nil
	}
	var claim textBuilder
	flush := func() error {
		if claim.empty() {
			return nil
		}
		text := claim.string()
		claim = textBuilder{}
		return s.push(Event{Kind: KindAgentClaim, Role: "assistant", Time: at, Text: text})
	}
	handled := false
	category := ""
	for _, part := range parts {
		switch part.Type {
		case "text", "thinking":
			if category != part.Type {
				if err := flush(); err != nil {
					return true, err
				}
				category = part.Type
			}
			if part.Type == "text" {
				claim.add(part.Text)
			} else {
				claim.add(part.Thinking)
			}
			handled = true
		case "toolCall":
			if err := flush(); err != nil {
				return true, err
			}
			category = ""
			// The invocation is where the command line lives, so the
			// verification match is made here and carried to the result,
			// which is the record that has an outcome to report.
			s.calls.put(part.ID, pendingCall{
				tool:         part.Name,
				verification: isVerificationCommand(part.Arguments.Command),
				path:         part.Arguments.Path,
			})
			text := part.Arguments.Command
			if text == "" {
				text = part.Arguments.Intent
			}
			if err := s.push(Event{Kind: KindToolObservation, Role: "assistant", Time: at, Text: clipString(text), Tool: part.Name}); err != nil {
				return true, err
			}
			handled = true
		}
	}
	if err := flush(); err != nil {
		return true, err
	}
	return handled, nil
}

// classifyOMPToolResult classifies a result by what the tool actually did,
// which the result record and its remembered call together establish.
func classifyOMPToolResult(s *scanner, r ompRecord, at *time.Time) (bool, error) {
	call, _ := s.calls.take(r.Message.ToolCallID)
	tool := r.Message.ToolName
	if tool == "" {
		tool = call.tool
	}
	text, _ := partText(r.Message.Content, "text")
	outcome := ompOutcome(r)

	if ompMutatingTools[tool] && !r.Message.IsError {
		path := r.Message.Details.Path
		if path == "" {
			path = call.path
		}
		if path != "" {
			return true, s.push(Event{
				Kind:  KindRepositoryChange,
				Role:  "toolResult",
				Time:  at,
				Text:  text,
				Tool:  tool,
				Paths: []string{path},
			})
		}
	}
	kind := KindToolObservation
	if call.verification {
		kind = KindVerificationEvidence
	}
	return true, s.push(Event{Kind: kind, Role: "toolResult", Time: at, Text: text, Tool: tool, Outcome: outcome})
}

// ompOutcome reads the explicit status OMP records on every tool result.
func ompOutcome(r ompRecord) string {
	if !r.Message.IsError {
		return OutcomePass
	}
	if r.Message.Details.ExitCode != nil {
		return OutcomeFail
	}
	return OutcomeError
}
