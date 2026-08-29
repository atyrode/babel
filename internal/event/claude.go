package event

import (
	"encoding/json"
	"time"
)

// Claude Code is a best-effort harness. Its record shape is close to OMP's,
// but it reports tool results as user-role records and records no exit
// status for a command, so its outcomes are strictly weaker than OMP's.
//
// Record shape. One JSON object per line with a top-level "type". "user" and
// "assistant" records carry {message:{role, content}} plus session fields;
// "system", "ai-title", "last-prompt", "pr-link", "attachment",
// "queue-operation", and "frame-link" are bookkeeping. Assistant content
// parts are "text", "thinking", and "tool_use". A tool result arrives as a
// user record whose content holds a "tool_result" part naming its
// tool_use_id, with the structured result in the sibling "toolUseResult"
// field.
//
// Classification rules:
//
//	type=user, string or text parts   -> KindUserReport
//	    The harness attributes the turn to the user. Claude Code also
//	    injects reminders and command output as user records; they are not
//	    separable from operator text by the record alone, so the harness's
//	    own attribution is what is reported.
//	type=assistant, text parts        -> KindAgentClaim
//	type=assistant, thinking parts    -> KindAgentClaim
//	    Same rule and same limitation as OMP: reasoning is a claim, emitted
//	    as its own event so it is never joined to the agent's final text.
//	type=assistant, tool_use part     -> KindToolObservation
//	    A tool invocation with its name and input. Bash input.command is
//	    where verification vocabulary is matched.
//	tool_result with toolUseResult.filePath, not is_error
//	                                  -> KindRepositoryChange
//	    Only the edit/write family reports a top-level filePath; Read
//	    reports its file under "file" instead. A tool_result marked
//	    is_error changed nothing.
//	tool_result, verifying call       -> KindVerificationEvidence
//	tool_result, otherwise            -> KindToolObservation
//	anything else                     -> KindOpaque
//
// Outcome is reported only as OutcomeError, and only when the record sets
// is_error. This schema records stdout, stderr, and an interrupted flag but
// no exit code, and is_error conflates a check that failed with a tool that
// failed, so a pass is never asserted and a fail is never distinguished from
// a tool error.

// claudeRecord is the Claude Code primary-log record.
type claudeRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult struct {
		FilePath string `json:"filePath"`
	} `json:"toolUseResult"`
}

// claudePart is one content part. Assistant parts carry a tool_use input;
// user parts carry a tool_result referencing the call by tool_use_id.
type claudePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
	// tool_use fields.
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input struct {
		Command     string `json:"command"`
		FilePath    string `json:"file_path"`
		Description string `json:"description"`
	} `json:"input"`
	// tool_result fields.
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func classifyClaude(s *scanner, rec []byte) (bool, error) {
	var r claudeRecord
	if json.Unmarshal(rec, &r) != nil {
		return false, nil
	}
	at := recordTime(r.Timestamp)
	switch r.Type {
	case "assistant":
		return classifyClaudeAssistant(s, r, at)
	case "user":
		return classifyClaudeUser(s, r, at)
	default:
		return false, nil
	}
}

// classifyClaudeAssistant walks content parts in order, grouping
// consecutive parts of the same category so prose, reasoning, and each tool
// call stay distinguishable.
func classifyClaudeAssistant(s *scanner, r claudeRecord, at *time.Time) (bool, error) {
	var parts []claudePart
	if json.Unmarshal(r.Message.Content, &parts) != nil {
		return false, nil
	}
	role := claudeRole(r)
	var claim textBuilder
	flush := func() error {
		if claim.empty() {
			return nil
		}
		text := claim.string()
		claim = textBuilder{}
		return s.push(Event{Kind: KindAgentClaim, Role: role, Time: at, Text: text})
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
		case "tool_use":
			if err := flush(); err != nil {
				return true, err
			}
			category = ""
			s.calls.put(part.ID, pendingCall{
				tool:         part.Name,
				verification: isVerificationCommand(part.Input.Command),
				path:         part.Input.FilePath,
			})
			if err := s.pushClaudeToolUse(part, role, at); err != nil {
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

// pushClaudeToolUse emits the invocation with the most specific input the
// tool records: a command, a file, or the caller's description.
func (s *scanner) pushClaudeToolUse(part claudePart, role string, at *time.Time) error {
	text := part.Input.Command
	if text == "" {
		text = part.Input.FilePath
	}
	if text == "" {
		text = part.Input.Description
	}
	return s.push(Event{Kind: KindToolObservation, Role: role, Time: at, Text: clipString(text), Tool: part.Name})
}

// classifyClaudeUser handles both operator turns and the tool results this
// harness files under the user role.
func classifyClaudeUser(s *scanner, r claudeRecord, at *time.Time) (bool, error) {
	role := claudeRole(r)
	var direct string
	if json.Unmarshal(r.Message.Content, &direct) == nil {
		return true, s.push(Event{Kind: KindUserReport, Role: role, Time: at, Text: clipString(direct)})
	}
	var parts []claudePart
	if json.Unmarshal(r.Message.Content, &parts) != nil {
		return false, nil
	}
	var report textBuilder
	flush := func() error {
		if report.empty() {
			return nil
		}
		text := report.string()
		report = textBuilder{}
		return s.push(Event{Kind: KindUserReport, Role: role, Time: at, Text: text})
	}
	handled := false
	for _, part := range parts {
		switch part.Type {
		case "text":
			report.add(part.Text)
			handled = true
		case "tool_result":
			if err := flush(); err != nil {
				return true, err
			}
			call, _ := s.calls.take(part.ToolUseID)
			text, _ := partText(part.Content, "text", "output_text")
			outcome := ""
			if part.IsError {
				outcome = OutcomeError
			}
			if r.ToolUseResult.FilePath != "" && !part.IsError {
				if err := s.push(Event{
					Kind:  KindRepositoryChange,
					Role:  role,
					Time:  at,
					Text:  text,
					Tool:  call.tool,
					Paths: []string{r.ToolUseResult.FilePath},
				}); err != nil {
					return true, err
				}
				handled = true
				continue
			}
			kind := KindToolObservation
			if call.verification {
				kind = KindVerificationEvidence
			}
			if err := s.push(Event{Kind: kind, Role: role, Time: at, Text: text, Tool: call.tool, Outcome: outcome}); err != nil {
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

// claudeRole prefers the role the message itself declares and falls back to
// the record type, which older records use as their only role signal.
func claudeRole(r claudeRecord) string {
	if r.Message.Role != "" {
		return r.Message.Role
	}
	return r.Type
}
