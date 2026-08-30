package codex

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Deriving a title for a Codex session.
//
// Codex records no title. `session_meta` carries an id, a parent thread id,
// a timestamp and a cwd, and nothing else that names what the session was
// for, so §3's "title unavailable" was an honest report of the format rather
// than an extraction gap. It is also, at 641 of the operator's 838 sessions,
// most of his corpus: a shared catalog row with no title is a digest a
// reader on another machine cannot identify.
//
// This file closes that gap deterministically, offline, from values the log
// already holds — never with a model, never with a network call. What it
// produces is therefore Babel's arithmetic and is labelled
// adapter.TitleDerived, never adapter.TitleRecorded.
//
// WHY THE OBVIOUS RULE IS WRONG
//
// "Take the first user-role message" produces boilerplate for most of the
// corpus. Codex prepends injected context to the model's input as ordinary
// `user` and `developer` messages: `<permissions instructions>`,
// `<environment_context>`, `<recommended_plugins>`, `<multi_agent_mode>`,
// and a repository's `# AGENTS.md instructions` block. Measured over the
// operator's 640 rollout logs, the first `user`-role `response_item` is one
// of those injected blocks in 597 of them.
//
// TWO STRUCTURAL FACTS DO THE WORK, AND NEITHER IS A STRING MATCH
//
// First, the log has two channels and only one of them carries injected
// context. `response_item` records are the model's input stream, where the
// injected blocks live. `event_msg` records are the front end's event
// stream, and a delivered turn appears there as `payload.type ==
// "user_message"`. No injected block ever appears on that channel: across
// the 640 logs, the two channels agree on the first request text in every
// one of the 349 sessions where both produce a candidate, and the event
// channel never yielded one of the five wrapper shapes above. So the
// primary rule is a channel rule — read the request from the event stream —
// which needs no knowledge of which preambles exist and does not break when
// Codex adds a sixth.
//
// Second, `session_meta.source` is a tagged union that says who opened the
// thread, and for two of its three shapes the transcript is not this
// thread's own request:
//
//   - `"vscode"` (a bare string): an interactive thread. The first delivered
//     request is what the operator typed. 40 logs.
//   - `{"subagent":{"other":"<role>"}}`: Codex opened the thread for one of
//     its own built-in roles. Its first turn is a fixed harness template —
//     all 288 such logs in the corpus are the `guardian` approval assessor,
//     and every one of them begins "The following is the Codex agent history
//     whose request action you are assessing". There is no caller request to
//     derive from, so these get no title. Keying on the union shape rather
//     than on that sentence means a future built-in role is excluded for
//     free, without Babel having to learn its template.
//   - `{"subagent":{"thread_spawn":{…}}}`: a delegated thread. Here the
//     transcript is actively misleading: 246 of the corpus's 312 spawned
//     threads open by replaying their parent's conversation, so their first
//     delivered request is the *parent's* request, and titling from it gave
//     127 sessions the same title. `agent_path` is the one value Codex
//     records about the spawned thread's own job — a per-task name like
//     "/root/node_version_audit", unique in 287 of 299 occurrences — so that
//     is what a spawn is titled from.
//
// A spawned thread with neither an `agent_path` nor an `agent_role` gets no
// title: nothing in it is known to be about this thread rather than its
// parent, and an absent title is honest where a wrong one is not.
//
// WHAT IS DELIBERATELY NOT USED
//
// `thread_goal_updated` events carry a `goal.objective` string, which reads
// like a title and appears in 15 of the 640 logs. It is not used, for two
// reasons: its text duplicates the session's first request in every case
// observed, so it buys nothing; and the log does not say whether the
// objective was typed by the operator or written by the model, so reporting
// it as recorded provenance would assert something this adapter did not
// observe.
//
// Codex Desktop also names some workspace directories after the opening
// prompt (`~/Documents/Codex/2026-07-18-yo-could-you-confirm-you-see`).
// That is a path, it is already reported as the workspace, and mining a
// title out of a filesystem name is a guess dressed as an observation.

const (
	// maxTitleRunes bounds a derived title. It is a display value that sits
	// in a terminal column and in a plaintext catalog row, so it is bounded
	// on the way in rather than truncated by every reader.
	maxTitleRunes = 72

	// minTitleRunes is the shortest text worth calling a title. It rejects
	// the degenerate residue of a request that was punctuation or a single
	// token, not short prompts: "Go ahead" is a real request and becomes a
	// real, if unhelpful, derived title, which is what the inferred layer
	// exists to improve.
	minTitleRunes = 3

	// maxRequestBytes bounds how much of one request record is retained
	// while scanning. A title needs the first line; an operator's prompt can
	// be a hundred kilobytes, and holding all of it to read 72 runes off the
	// front would make describing a session scale with how much he typed.
	maxRequestBytes = 4 << 10

	// maxRequestCandidates bounds how many `response_item` user records the
	// fallback channel examines before giving up. The injected blocks sit at
	// the head of a log in a bounded run, so a request that has not appeared
	// within this many records is not going to.
	maxRequestCandidates = 8
)

// Markers used to skip the payload re-parse for records that cannot be
// request candidates. A rollout log is overwhelmingly `token_count` and
// `agent_reasoning` events — 198k of the corpus's 259k records — and paying
// a second JSON decode on each of them to look for a title would make
// describing a session several times more expensive for nothing.
var (
	userMessageMarker = []byte(`"user_message"`)
	roleUserMarker    = []byte(`"user"`)
)

// titleBasis names which rule produced a derived title. It is recorded in
// the adapter metadata so a reader can tell a truncated request from a
// humanized agent path without re-deriving, and it is a rule name rather
// than transcript text, which is all that document is allowed to hold.
type titleBasis string

const (
	// basisAgentPath is a spawned thread titled from the per-task name
	// Codex recorded for it.
	basisAgentPath titleBasis = "agent_path"
	// basisRequest is a thread titled from the first request delivered on
	// the event channel.
	basisRequest titleBasis = "request"
	// basisRequestFallback is basisRequest read from the `response_item`
	// channel because the log carried no `user_message` event, with the
	// injected-block guard doing the work the channel would otherwise do.
	basisRequestFallback titleBasis = "request_fallback"
)

// threadSource is the decoded `session_meta.source` union. Codex writes
// either a bare string naming a front end or an object naming the subagent
// mechanism that opened the thread, so it is decoded permissively: an
// unrecognized shape leaves every field zero and is treated as an
// interactive thread, which is the only reading that does not invent a
// classification.
type threadSource struct {
	// role is `subagent.other`, Codex's name for one of its built-in
	// roles.
	role string
	// spawn is set when `subagent.thread_spawn` is present.
	spawn bool
	// agentPath is `subagent.thread_spawn.agent_path`, the per-task name of
	// a spawned thread.
	agentPath string
	// agentRole is `subagent.thread_spawn.agent_role`. Its presence is
	// Codex declaring that the thread was opened for a distinct role with a
	// brief of its own; observed on 12 of the corpus's 13 spawns that carry
	// no agent path, and in all 12 the first delivered request was that
	// brief rather than a replay of the parent's.
	agentRole string
}

type sourceWire struct {
	Subagent *struct {
		Other       string `json:"other"`
		ThreadSpawn *struct {
			AgentPath string `json:"agent_path"`
			AgentRole string `json:"agent_role"`
		} `json:"thread_spawn"`
	} `json:"subagent"`
}

// decodeThreadSource reads the union. A string, a null, a missing field or
// an object of some future shape all yield the zero value.
func decodeThreadSource(raw json.RawMessage) threadSource {
	if len(raw) == 0 {
		return threadSource{}
	}
	var w sourceWire
	if err := json.Unmarshal(raw, &w); err != nil || w.Subagent == nil {
		return threadSource{}
	}
	src := threadSource{role: w.Subagent.Other}
	if ts := w.Subagent.ThreadSpawn; ts != nil {
		src.spawn = true
		src.agentPath = ts.AgentPath
		src.agentRole = ts.AgentRole
	}
	return src
}

// deriveTitle applies the rules above to one scanned rollout. It returns the
// title, the rule that produced it, and — when it returns none — the reason
// that becomes the description's completeness entry.
func deriveTitle(scan *scanResult) (title string, basis titleBasis, reason string) {
	src := scan.source
	if src.role != "" {
		return "", "", "codex opened this thread for its built-in " + quoteRole(src.role) +
			" role, whose opening turn is a fixed harness template rather than a caller's request"
	}
	if src.spawn {
		if seg := lastPathSegment(src.agentPath); seg != "" {
			if t := condense(humanize(seg)); runeLen(t) >= minTitleRunes {
				return t, basisAgentPath, ""
			}
		}
		if src.agentRole == "" {
			return "", "", "codex records nothing about this spawned thread's own request: " +
				"it carries no agent path, and a spawned thread's transcript may replay its parent's conversation"
		}
	}
	if t := titleFromRequest(scan.request); t != "" {
		return t, basisRequest, ""
	}
	if t := titleFromRequest(scan.requestFallback); t != "" {
		return t, basisRequestFallback, ""
	}
	return "", "", "no delivered request record exposed titleable text"
}

// titleFromRequest states one request record as a title, or returns empty
// when nothing in it is titleable.
//
// Two shapes stand between a record and a title, and both are handled by
// shape rather than by name. An injected context block is rejected outright
// (injectedBlock). A *composed envelope* is descended into: when the
// operator attaches files, Codex Desktop delivers the turn as a markdown
// document whose sections list the attachments and whose last section is
// labelled with the operator's own request — four logs in the corpus, all of
// the form "# Files mentioned by the user:" … "## My request for Codex:".
// Titling from the front of that envelope produces the corpus's worst
// titles, a run of clipboard filenames and attachment paths, so a candidate
// that opens with a markdown heading is read from its last section instead.
//
// Two of those four logs have an empty last section, because the request
// itself was the attachment — and they correctly end up with no title, since
// the attachment's bytes are archived but its text is not this adapter's to
// read.
//
// The residual risk is stated rather than hidden: an operator prompt that
// genuinely opens with a markdown heading is titled from its last section
// rather than its first line. That is a milder wrong than a title made of
// attachment paths, and it did not occur in the 640 logs measured.
func titleFromRequest(text string) string {
	if strings.TrimSpace(text) == "" || injectedBlock(text) {
		return ""
	}
	t := condense(lastSection(text))
	if runeLen(t) < minTitleRunes {
		return ""
	}
	return t
}

// lastSection returns the text after the final markdown heading line when
// text opens with a heading, and text unchanged otherwise.
func lastSection(text string) string {
	if !isMarkdownHeading(firstContentLine(text)) {
		return text
	}
	last := -1
	for i := 0; i < len(text); {
		end := len(text)
		if j := strings.IndexByte(text[i:], '\n'); j >= 0 {
			end = i + j
		}
		if isMarkdownHeading(strings.TrimSpace(text[i:end])) {
			last = end
		}
		i = end + 1
	}
	if last < 0 || last >= len(text) {
		return ""
	}
	return text[last+1:]
}

// firstContentLine returns the first non-blank line, trimmed.
func firstContentLine(text string) string {
	for i := 0; i < len(text); {
		end := len(text)
		if j := strings.IndexByte(text[i:], '\n'); j >= 0 {
			end = i + j
		}
		if line := strings.TrimSpace(text[i:end]); line != "" {
			return line
		}
		i = end + 1
	}
	return ""
}

// quoteRole renders a role name for a completeness reason without letting
// the log choose the punctuation around it.
func quoteRole(role string) string {
	role = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' {
			return -1
		}
		return r
	}, role)
	if runeLen(role) > 32 {
		role = string([]rune(role)[:32])
	}
	return `"` + role + `"`
}

// lastPathSegment returns the final non-empty "/"-separated segment of an
// agent path. Codex nests them ("/root/audit_dotfiles/research_clan_alts"),
// and the leaf is the one that names this thread's job; the ancestors name
// the threads that delegated it and are already reachable by parent id.
func lastPathSegment(p string) string {
	for i := len(p) - 1; ; {
		j := strings.LastIndexByte(p[:i+1], '/')
		seg := strings.TrimSpace(p[j+1 : i+1])
		if seg != "" {
			return seg
		}
		if j <= 0 {
			return ""
		}
		i = j - 1
	}
}

// humanize turns an agent path segment into prose: separators become spaces
// and the first letter is capitalized, matching how the other harnesses'
// recorded titles read. Nothing else is changed — "pr49_safety_review"
// becomes "Pr49 safety review" and not a guess at what "pr49" abbreviates.
func humanize(seg string) string {
	var b strings.Builder
	b.Grow(len(seg))
	space := false
	for _, r := range seg {
		switch {
		case r == '_' || r == '-' || unicode.IsSpace(r):
			space = b.Len() > 0
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return ""
	}
	first, size := utf8.DecodeRuneInString(out)
	if upper := unicode.ToUpper(first); upper != first {
		return string(upper) + out[size:]
	}
	return out
}

// condense states one text as a single-line bounded title: whitespace runs
// collapse to one space, and an over-long result is cut at a word boundary
// and marked with an ellipsis so a reader can see it was cut rather than
// that the operator stopped mid-sentence.
func condense(text string) string {
	flat := collapseSpace(text)
	if runeLen(flat) <= maxTitleRunes {
		return flat
	}
	runes := []rune(flat)[:maxTitleRunes]
	if i := lastSpace(runes); i >= maxTitleRunes/2 {
		runes = runes[:i]
	}
	cut := strings.TrimRight(string(runes), " ,;:.-—–")
	if cut == "" {
		return ""
	}
	return cut + "\u2026"
}

func lastSpace(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == ' ' {
			return i
		}
	}
	return -1
}

// collapseSpace replaces every run of whitespace with a single space and
// trims the ends. Control characters other than whitespace are left alone:
// sanitizing them is the renderer's job and doing it twice, in two
// vocabularies, is how the two come to disagree.
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// InjectedContext reports whether one Codex message text is context the
// harness injected rather than something a caller said. It is the same shape
// test the title derivation uses, exported because anything that reads Codex
// messages meets the same five wrapper shapes — the inferred-title path
// assembles material to send to a model, and paying a provider to summarize
// `<permissions instructions>` would be both useless and a disclosure of
// nothing.
//
// Codex knowledge stays in the Codex adapter: the alternative was a second
// copy of the rule in the command that needs it, and two copies of a rule
// about someone else's file format is one copy too many.
func InjectedContext(text string) bool { return injectedBlock(text) }

// messagePayload is one `response_item` message or `user_message` event,
// decoded only as far as its text. Codex writes content either as a bare
// string or as a list of typed parts, and a part type this adapter does not
// know contributes nothing rather than failing the record.
type messagePayload struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Message string          `json:"message"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Text string `json:"text"`
}

// text returns the payload's message text, bounded by maxRequestBytes.
func (p *messagePayload) text() string {
	if p.Message != "" {
		return truncateBytes(p.Message, maxRequestBytes)
	}
	if len(p.Content) == 0 {
		return ""
	}
	var single string
	if json.Unmarshal(p.Content, &single) == nil {
		return truncateBytes(single, maxRequestBytes)
	}
	var parts []contentPart
	if json.Unmarshal(p.Content, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(part.Text)
		if b.Len() >= maxRequestBytes {
			break
		}
	}
	return truncateBytes(b.String(), maxRequestBytes)
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// injectedBlock reports whether text is one of Codex's injected context
// blocks, by shape rather than by name. Every one of them is a wrapped
// document: optionally one leading markdown heading, then an XML-ish open
// tag on its own line. An operator's prompt does not open that way.
//
// This guard exists for the `response_item` fallback channel, where it is
// load-bearing: the three corpus logs that emit no `user_message` event
// contain exactly one user-role record each, and it is `<recommended_plugins>`.
// Without the guard those three sessions would be titled "<recommended_plugins>
// Here is a list of plugins that are available but not installed…", which is
// the failure mode this whole file exists to avoid.
func injectedBlock(text string) bool {
	// One markdown heading may precede the wrapper: a repository's AGENTS.md
	// block is injected as "# AGENTS.md instructions" followed by
	// "<INSTRUCTIONS>".
	headings := 0
	for rest := text; rest != ""; {
		line := rest
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			rest = ""
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case headings == 0 && isMarkdownHeading(line):
			headings++
		default:
			return opensTag(line)
		}
	}
	// Whitespace only. Nothing titleable, so treating it as injected keeps
	// the caller's single rejection path.
	return true
}

func isMarkdownHeading(line string) bool {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && n < len(line) && (line[n] == ' ' || line[n] == '\t')
}

// opensTag reports whether line begins with a complete XML-ish open tag:
// "<name>" or "<name attributes>". The name alphabet admits the space in
// "<permissions instructions>", which is not well-formed XML but is what
// Codex writes.
func opensTag(line string) bool {
	if len(line) < 3 || line[0] != '<' {
		return false
	}
	rest := line[1:]
	if rest[0] == '/' || rest[0] == '!' || rest[0] == '?' {
		return false
	}
	end := strings.IndexByte(rest, '>')
	if end <= 0 {
		return false
	}
	name := rest[:end]
	if strings.ContainsAny(name, "<") {
		return false
	}
	first, _ := utf8.DecodeRuneInString(name)
	if !unicode.IsLetter(first) && first != '_' {
		return false
	}
	return true
}
