/*
  DERIVING A TITLE FOR A CODEX SESSION, ported from internal/adapter/codex/title.go.

  Codex records no title. `session_meta` carries an id, a parent thread id, a timestamp and a
  cwd, and nothing that names what the session was for — and untitled sessions were most of
  the operator's Codex corpus (641 of 838), where a catalog row with no title is a digest
  nobody can identify. What follows closes that gap deterministically, offline, from values
  the log already holds: no model, no network. The result is therefore Babel's arithmetic and
  is labelled "derived", never "recorded".

  WHY THE OBVIOUS RULE IS WRONG. "Take the first user message" yields boilerplate: Codex
  prepends injected context to the model's input as ordinary user and developer messages
  (<permissions instructions>, <environment_context>, <recommended_plugins>,
  <multi_agent_mode>, a repository's "# AGENTS.md instructions" block). Measured over the
  operator's 640 rollout logs, the first user-role `response_item` was one of those in 597.

  TWO STRUCTURAL FACTS DO THE WORK, AND NEITHER IS A STRING MATCH.

  First, the log has two channels and only one carries injected context. `response_item` is
  the model's input stream, where the wrappers live; `event_msg` is the front end's stream,
  where a delivered turn appears as payload.type == "user_message". No injected block ever
  appears there: across the 640 logs the two channels agreed on the first request in all 349
  sessions where both produced a candidate, and the event channel never yielded a wrapper. The
  primary rule is therefore a channel rule, which needs no list of known preambles and does
  not break when Codex adds a sixth.

  Second, `session_meta.source` says who opened the thread, and for two of its three shapes
  the transcript is not this thread's own request: a built-in subagent role ("other") opens
  with a fixed harness template, and a spawned thread ("thread_spawn") commonly replays its
  parent's conversation — 246 of 312 spawns did, and titling from the replay gave 127 sessions
  one identical title. A spawn's own job is named by `agent_path`, unique in 287 of 299
  occurrences, so that is what a spawn is titled from; a spawn with neither an agent path nor
  an agent role gets no title, because nothing in it is known to be about this thread.
*/

/** A derived title is bounded here rather than truncated by every reader downstream. */
const MAX_TITLE_RUNES = 72;
/** The shortest text worth calling a title; it rejects punctuation, not short prompts. */
const MIN_TITLE_RUNES = 3;
/** How much of one request record is retained: a title needs the first line, not the prompt. */
export const MAX_REQUEST_BYTES = 4 << 10;
/** How many response_item user records the fallback channel examines before giving up. */
export const MAX_REQUEST_CANDIDATES = 8;

/** Which rule produced a derived title: a rule name, never transcript text. */
export const TITLE_BASES = ["agent_path", "request", "request_fallback"] as const;
export type TitleBasis = (typeof TITLE_BASES)[number];

/**
 * The decoded `session_meta.source` union. Codex writes either a bare string naming a front
 * end or an object naming the subagent mechanism that opened the thread, so it is read
 * permissively: an unrecognized shape leaves every field empty and is treated as an
 * interactive thread, the only reading that invents no classification.
 */
export interface ThreadSource {
  /** `subagent.other`: Codex's name for one of its own built-in roles. */
  role: string;
  /** Whether `subagent.thread_spawn` is present. */
  spawn: boolean;
  /** `subagent.thread_spawn.agent_path`: the per-task name of a spawned thread. */
  agentPath: string;
  /** `subagent.thread_spawn.agent_role`: Codex declaring the thread has a brief of its own. */
  agentRole: string;
}

export interface TitleEvidence {
  source: ThreadSource;
  /** The first turn delivered on the `event_msg` channel. */
  request: string;
  /** The first non-injected `response_item` user record, used only when the log emits no event. */
  requestFallback: string;
}

export const NO_THREAD_SOURCE: ThreadSource = { role: "", spawn: false, agentPath: "", agentRole: "" };

export function decodeThreadSource(raw: unknown): ThreadSource {
  if (typeof raw !== "object" || raw === null) return NO_THREAD_SOURCE;
  const subagent = (raw as Record<string, unknown>)["subagent"];
  if (typeof subagent !== "object" || subagent === null) return NO_THREAD_SOURCE;
  const fields = subagent as Record<string, unknown>;
  const other = fields["other"];
  const spawn = fields["thread_spawn"];
  const source: ThreadSource = {
    role: typeof other === "string" ? other : "",
    spawn: typeof spawn === "object" && spawn !== null,
    agentPath: "",
    agentRole: "",
  };
  if (typeof spawn === "object" && spawn !== null) {
    const spawned = spawn as Record<string, unknown>;
    const path = spawned["agent_path"];
    const role = spawned["agent_role"];
    if (typeof path === "string") source.agentPath = path;
    if (typeof role === "string") source.agentRole = role;
  }
  return source;
}

export interface DerivedTitle {
  title: string;
  basis: TitleBasis | null;
  /** Why there is no title, for the description's absence list. */
  reason: string;
}

export function deriveTitle(evidence: TitleEvidence): DerivedTitle {
  const source = evidence.source;
  if (source.role !== "") {
    return {
      title: "",
      basis: null,
      reason:
        `codex opened this thread for its built-in ${quoteRole(source.role)} role, whose opening ` +
        "turn is a fixed harness template rather than a caller's request",
    };
  }
  if (source.spawn) {
    const segment = lastPathSegment(source.agentPath);
    if (segment !== "") {
      const title = condense(humanize(segment));
      if (runeLength(title) >= MIN_TITLE_RUNES) return { title, basis: "agent_path", reason: "" };
    }
    if (source.agentRole === "") {
      return {
        title: "",
        basis: null,
        reason:
          "codex records nothing about this spawned thread's own request: it carries no agent " +
          "path, and a spawned thread's transcript may replay its parent's conversation",
      };
    }
  }
  const delivered = titleFromRequest(evidence.request);
  if (delivered !== "") return { title: delivered, basis: "request", reason: "" };
  const fallback = titleFromRequest(evidence.requestFallback);
  if (fallback !== "") return { title: fallback, basis: "request_fallback", reason: "" };
  return { title: "", basis: null, reason: "no delivered request record exposed titleable text" };
}

/**
 * States one request record as a title, or nothing when it holds none.
 *
 * Two shapes stand between a record and a title, and both are handled by shape. An injected
 * context block is rejected outright. A composed envelope is descended into: when the operator
 * attaches files, Codex Desktop delivers the turn as a markdown document whose sections list
 * the attachments and whose last section is labelled with the request ("# Files mentioned by
 * the user:" … "## My request for Codex:"). Titling from the front of that envelope produces
 * a run of clipboard filenames, so a candidate that opens with a heading is read from its last
 * section instead. The residual risk is stated rather than hidden: a prompt that genuinely
 * opens with a markdown heading is titled from its last section, which is a milder wrong than
 * a title made of attachment paths and did not occur in the 640 logs measured.
 */
function titleFromRequest(text: string): string {
  if (text.trim() === "" || injectedBlock(text)) return "";
  const title = condense(lastSection(text));
  return runeLength(title) < MIN_TITLE_RUNES ? "" : title;
}

/** The text after the final heading line when the text opens with a heading. */
function lastSection(text: string): string {
  if (!isMarkdownHeading(firstContentLine(text))) return text;
  let last = -1;
  for (let i = 0; i < text.length; ) {
    const newline = text.indexOf("\n", i);
    const end = newline < 0 ? text.length : newline;
    if (isMarkdownHeading(text.slice(i, end).trim())) last = end;
    i = end + 1;
  }
  if (last < 0 || last >= text.length) return "";
  return text.slice(last + 1);
}

function firstContentLine(text: string): string {
  for (let i = 0; i < text.length; ) {
    const newline = text.indexOf("\n", i);
    const end = newline < 0 ? text.length : newline;
    const line = text.slice(i, end).trim();
    if (line !== "") return line;
    i = end + 1;
  }
  return "";
}

/** Renders a role name for a reason without letting the log choose the punctuation around it. */
function quoteRole(role: string): string {
  let clean = "";
  for (const character of role) {
    const code = character.codePointAt(0) ?? 0;
    if (code < 0x20 || code === 0x7f || character === '"') continue;
    clean += character;
  }
  const runes = [...clean];
  return '"' + (runes.length > 32 ? runes.slice(0, 32).join("") : clean) + '"';
}

/**
 * The final non-empty segment of an agent path. Codex nests them
 * ("/root/audit_dotfiles/research_clan_alts") and the leaf names this thread's job; the
 * ancestors name the threads that delegated it and are already reachable by parent id.
 */
function lastPathSegment(path: string): string {
  const segments = path.split("/");
  for (let i = segments.length - 1; i >= 0; i--) {
    const segment = segments[i]?.trim() ?? "";
    if (segment !== "") return segment;
  }
  return "";
}

/**
 * Turns an agent path segment into prose: separators become spaces and the first letter is
 * capitalized, matching how the other harnesses' recorded titles read. Nothing else changes —
 * "pr49_safety_review" becomes "Pr49 safety review", not a guess at what "pr49" abbreviates.
 */
function humanize(segment: string): string {
  let out = "";
  let space = false;
  for (const character of segment) {
    if (character === "_" || character === "-" || /\s/u.test(character)) {
      space = out.length > 0;
      continue;
    }
    if (space) {
      out += " ";
      space = false;
    }
    out += character;
  }
  if (out === "") return "";
  const first = String.fromCodePoint(out.codePointAt(0) ?? 0);
  const upper = first.toUpperCase();
  return upper === first ? out : upper + out.slice(first.length);
}

/**
 * States one text as a single-line bounded title: whitespace runs collapse to one space, and
 * an over-long result is cut at a word boundary and marked with an ellipsis, so a reader can
 * see it was cut rather than that the operator stopped mid-sentence.
 */
function condense(text: string): string {
  const flat = collapseSpace(text);
  const runes = [...flat];
  if (runes.length <= MAX_TITLE_RUNES) return flat;
  let kept = runes.slice(0, MAX_TITLE_RUNES);
  const lastSpace = kept.lastIndexOf(" ");
  if (lastSpace >= MAX_TITLE_RUNES / 2) kept = kept.slice(0, lastSpace);
  const cut = kept.join("").replace(/[ ,;:.\-—–]+$/u, "");
  return cut === "" ? "" : cut + "\u2026";
}

/**
 * Replaces every run of whitespace with one space and trims the ends. Control characters that
 * are not whitespace are left alone: sanitizing them is the renderer's job, and doing it twice
 * in two vocabularies is how the two come to disagree.
 */
function collapseSpace(text: string): string {
  let out = "";
  let space = false;
  for (const character of text) {
    if (/\s/u.test(character)) {
      space = out.length > 0;
      continue;
    }
    if (space) {
      out += " ";
      space = false;
    }
    out += character;
  }
  return out;
}

function runeLength(text: string): number {
  let count = 0;
  for (const _ of text) count++;
  return count;
}

/** Cuts text to at most `bytes` UTF-8 bytes without splitting a character. */
export function truncateUtf8(text: string, bytes: number): string {
  if (Buffer.byteLength(text) <= bytes) return text;
  const buffer = Buffer.from(text, "utf8");
  let end = bytes;
  // Back off over a continuation byte so the cut lands on a character boundary.
  while (end > 0 && (buffer[end] ?? 0) >= 0x80 && (buffer[end] ?? 0) < 0xc0) end--;
  return buffer.subarray(0, end).toString("utf8");
}

/**
 * The text of one `response_item` message or `user_message` event, bounded. Codex writes
 * content either as a bare string or as a list of typed parts, and a part type this adapter
 * does not know contributes nothing rather than failing the record.
 */
export function messageText(payload: Record<string, unknown>): string {
  const message = payload["message"];
  if (typeof message === "string" && message !== "") return truncateUtf8(message, MAX_REQUEST_BYTES);
  const content = payload["content"];
  if (typeof content === "string") return truncateUtf8(content, MAX_REQUEST_BYTES);
  if (!Array.isArray(content)) return "";
  let out = "";
  for (const part of content) {
    if (typeof part !== "object" || part === null) continue;
    const text = (part as Record<string, unknown>)["text"];
    if (typeof text !== "string" || text === "") continue;
    if (out.length > 0) out += "\n";
    out += text;
    if (Buffer.byteLength(out) >= MAX_REQUEST_BYTES) break;
  }
  return truncateUtf8(out, MAX_REQUEST_BYTES);
}

/**
 * Reports whether a text is context Codex injected rather than something a caller said, by
 * shape rather than by name: every wrapper is a wrapped document — optionally one leading
 * markdown heading, then an XML-ish open tag on its own line. An operator's prompt does not
 * open that way.
 *
 * The guard is load-bearing on the `response_item` fallback channel: the three corpus logs
 * that emit no `user_message` event hold exactly one user-role record each, and it is
 * <recommended_plugins>. Without it those three would be titled from a plugin list.
 */
export function injectedBlock(text: string): boolean {
  let headings = 0;
  for (let i = 0; i < text.length; ) {
    const newline = text.indexOf("\n", i);
    const end = newline < 0 ? text.length : newline;
    const line = text.slice(i, end).trim();
    i = end + 1;
    if (line === "") continue;
    if (headings === 0 && isMarkdownHeading(line)) {
      headings++;
      continue;
    }
    return opensTag(line);
  }
  // Whitespace only: nothing titleable, so it takes the caller's single rejection path.
  return true;
}

function isMarkdownHeading(line: string): boolean {
  let hashes = 0;
  while (hashes < line.length && line[hashes] === "#") hashes++;
  if (hashes < 1 || hashes > 6 || hashes >= line.length) return false;
  const after = line[hashes];
  return after === " " || after === "\t";
}

/**
 * Whether a line begins with a complete XML-ish open tag: "<name>" or "<name attributes>".
 * The name alphabet admits the space in "<permissions instructions>", which is not well-formed
 * XML but is what Codex writes.
 */
function opensTag(line: string): boolean {
  if (line.length < 3 || line[0] !== "<") return false;
  const rest = line.slice(1);
  const lead = rest[0];
  if (lead === "/" || lead === "!" || lead === "?") return false;
  const end = rest.indexOf(">");
  if (end <= 0) return false;
  const name = rest.slice(0, end);
  if (name.includes("<")) return false;
  const first = String.fromCodePoint(name.codePointAt(0) ?? 0);
  return first === "_" || /\p{L}/u.test(first);
}
