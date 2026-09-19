import {
  CITATION_OUTCOMES,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  MIN_CITATION_QUOTE,
  type CitationOutcome,
  type MaterialEntry,
} from "../../contract.ts";
import type { Evidence, ExploreResult } from "../../machine/results.ts";

/*
  WHAT A CITATION HAS TO SURVIVE BEFORE IT BECOMES A RECORD, in one module because the two
  properties are one seam (#343, #348).

  A citation says two things: WHERE the bytes are and WHAT they say. Until now only the first
  was asked about, and only against an index — the path had to be one the material's own
  `index.json` names and the digest had to be the one it was served at. That check is real and
  it stays: it is what makes "a model cannot cite its way to a file it was not given" true
  (`docs/sandbox-threat-model.md` §4). What it cannot see is the quote, so a fabricated span and
  a real one were the same row. Of 300 digest-verified citations in the imported corpus, 86
  carried a quote of twelve characters or more; 53 matched the cited line, 57 matched somewhere
  else in the right file and 29 matched nowhere in it (`docs/jev-case-study-audit.md`). A model
  asked to judge the same 86 scored 60.5% against a 62% majority baseline. It is string matching:
  code knows the answer, so code answers it.

  THE TWO HALVES REFUSE DIFFERENTLY, AND THAT IS THE DECISION THIS MODULE MAKES.

  A PATH IS A CAPABILITY QUESTION: may this run point here at all. It is answered by a
  whitelist — the entries the index names, under the three spellings the prompt uses — and a
  path outside it REFUSES the whole answer as `unknown-reference`, as it always has. Nothing is
  resolved, canonicalised into, or opened by a string the model wrote: {@link admitCitation}
  returns the INDEX ENTRY, and every later reader is keyed on that entry's own `file`. A
  traversal cannot reach bytes because the model's path never selects bytes.

  A QUOTE IS AN ACCURACY QUESTION: is this claim supported by the bytes it names. It is
  RECORDED, not refused ({@link CITATION_OUTCOMES}), and the verdict travels on the record's own
  evidence where the reader of the claim is. Refusing would repeat the all-or-nothing waste
  #231 and #311 measured — one bad quote discarding a paid run's whole output — and it would
  apply a rule to a corpus that predates it. Marking is reversible; a refused run is gone.

  THE NORMALISATION, AND WHY IT IS THIS ONE. A false accusation is the failure that would make
  the whole verdict worthless, so every rule here is permissive in the one direction and strict
  in the other: it may only make an honest quote match, never make a fabricated one match.

    1. THE HAYSTACK IS THE RECORD'S TEXT, NOT ITS BYTES. A material file is one canonical JSON
       record per line, and a model quotes what it READ — the decoded prose — while the line
       holds `\n`, `\"` and `\uXXXX` escapes around it. So a line is parsed and its string
       leaves are joined; a line that does not parse is used as it stands.
    2. LINE ENDINGS ARE NOT CONTENT. CRLF and CR both split as LF before anything else.
    3. WHITESPACE IS COLLAPSED on both sides — every run of it to one space, then trimmed. A
       quote re-wrapped by a model, or indented differently from the record, is the same quote.
    4. UNICODE IS COMPOSED (NFC) on both sides, and zero-width characters are dropped. A quote
       that round-tripped through a tokenizer is the same quote.
    5. CASE AND PUNCTUATION ARE CONTENT and are never folded. "the router retries twice" and
       "the router retries TWICE" are different claims about what somebody wrote.
    6. A QUOTE SHORTER THAN {@link MIN_CITATION_QUOTE} CHARACTERS IS NOT CHECKED. Below the
       study's own twelve-character threshold a span matches somewhere in almost any session, so
       a verdict either way would be noise presented as a finding.

  Nothing here reads a file, a database or a job. The bytes arrive as a function the caller
  supplies, because the hub holds no corpus: the caller is the settlement, which pulls the one
  sealed member it needs and can fail — a preparation whose lease is gone answers `unchecked`
  and the run still settles.
*/

/** What became of one citation's quoted text, as the record carries it. */
export interface CitationCheck {
  readonly outcome: CitationOutcome;
  /** One sentence a reader of the record can act on; empty only for a plain `verified`. */
  readonly detail: string;
}

/** The lines of one served session, or null when this hub could not read them. */
export type SessionLines = (entry: MaterialEntry) => readonly string[] | null;

// ---------------------------------------------------------------------------- the scope

/**
 * THE INDEX ENTRY A CITED PATH NAMES, or null when it names nothing this run was served.
 *
 * The admitted spellings are the three the prompt itself uses — the file alone, under
 * `sessions/`, and under the material's own root — matched EXACTLY against the index. Two
 * conveniences are allowed before the match and no more: a `./` segment and a doubled slash,
 * both of which a model writes by accident and neither of which can change which file is named.
 *
 * A `..` SEGMENT IS REFUSED WHEREVER IT APPEARS, including one that would resolve back inside
 * the material. `sessions/../sessions/0001-x.jsonl` names a served file under any resolver, and
 * admitting it would make the boundary a property of the resolver rather than of the index —
 * which is how every traversal defect in the world is written. The material has one layout and
 * one spelling per file; a locator that needs resolving is a locator Babel did not serve.
 */
export function admitCitation(
  path: string,
  sessions: readonly MaterialEntry[],
): MaterialEntry | null {
  const wanted = cleaned(path);
  if (wanted === "") return null;
  for (const entry of sessions) {
    if (entry.file === "") continue;
    if (
      wanted === entry.file ||
      wanted === `${MATERIAL_SESSIONS}/${entry.file}` ||
      wanted === `${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/${entry.file}`
    ) {
      return entry;
    }
  }
  return null;
}

/**
 * The cited path with the two harmless spellings removed, or "" for one that may not be
 * compared at all: a backslash (this material has no such separator, and admitting one would
 * mean two spellings of one name), a control character, or any `..` segment.
 */
function cleaned(path: string): string {
  if (path === "" || path.includes("\\")) return "";
  // eslint-disable-next-line no-control-regex -- a control byte in a locator is not a path.
  if (/[\u0000-\u001f]/u.test(path)) return "";
  const parts: string[] = [];
  for (const part of path.split("/")) {
    if (part === "..") return "";
    if (part === "." || part === "") continue;
    parts.push(part);
  }
  const joined = parts.join("/");
  return path.startsWith("/") ? `/${joined}` : joined;
}

/**
 * EVERY LOCATOR THIS RESULT CITES, CHECKED AGAINST WHAT BABEL ACTUALLY SERVED — which is the
 * sentence the prompt's evidence instructions promise the model, kept here so the promise is
 * true. The empty string means every one of them is admissible.
 *
 * #284 checked a locator against a served trace the host tools had recorded. There are no host
 * tools now, and there is something better: the material is an immutable selection with a source
 * digest per session, so a locator is admissible exactly when {@link admitCitation} finds its
 * entry and its `digest` is that entry's own source digest. A retyped digest, an edited path or
 * a citation of a session this run was never given is `unknown-reference` — the claim is
 * refused, its siblings are not, and nothing repairs it.
 *
 * The check is over the SELECTION rather than over the sealed bytes, and deliberately: the
 * selection is on the run row, so admitting a citation costs no read of a 12 GB corpus — which
 * is the whole economy this lane was rebuilt for (post-mortem F1). Reading bytes is what the
 * quote check does, once, and only for a file this function has already admitted.
 */
export function unservedCitation(
  result: ExploreResult,
  sessions: readonly MaterialEntry[],
): string {
  for (const evidence of citedEvidence(result)) {
    const entry = admitCitation(evidence.locator.path, sessions);
    if (entry === null) {
      return `${evidence.locator.path} is not a file this run was served`;
    }
    if (entry.sourceDigest !== evidence.locator.digest) {
      return (
        `${evidence.locator.path} was served at ${entry.sourceDigest} ` +
        `and this claim cites ${evidence.locator.digest}`
      );
    }
  }
  return "";
}

/**
 * Every citation one exploration result carries, wherever the shape allows one: an observation's
 * claim and its counter-evidence, an objection's, a finding's counter-evidence (a finding rests
 * on observations and has no evidence field of its own), and the supporting and conflicting
 * material of both kinds of proposal.
 */
export function citedEvidence(result: ExploreResult): readonly Evidence[] {
  const cited: Evidence[] = [];
  for (const candidate of result.candidates) {
    for (const observation of candidate.observations) {
      cited.push(...observation.claim.evidence, ...observation.claim.counter_evidence);
    }
    const remedy = candidate.remedy;
    if (remedy !== undefined)
      cited.push(...remedy.proposal.supporting, ...remedy.proposal.conflicting);
  }
  for (const objection of result.objections) {
    cited.push(...objection.claim.evidence, ...objection.claim.counter_evidence);
  }
  for (const consolidation of result.consolidations) {
    cited.push(...consolidation.finding.counter_evidence);
    const proposal = consolidation.proposal;
    if (proposal !== undefined) cited.push(...proposal.supporting, ...proposal.conflicting);
  }
  return cited;
}

// ---------------------------------------------------------------------------- the quote

/**
 * ONE QUOTE AGAINST THE SESSION IT NAMES: at the cited line, elsewhere in it, or nowhere.
 *
 * `line` is 1-based as the locator states it, and 0 means the citation named no line. A quote
 * found in a session that named no line is `verified` with the line it was actually at — the
 * claim is about bytes that exist and the reader is told where they are, which is more use than
 * an accusation about a field the model left at its default.
 */
export function checkQuote(quote: string, lines: readonly string[], line: number): CitationCheck {
  const needle = folded(quote);
  if (needle === "") return { outcome: CITATION_OUTCOMES.unquoted, detail: "" };
  if (needle.length < MIN_CITATION_QUOTE) {
    return {
      outcome: CITATION_OUTCOMES.unchecked,
      detail:
        `the quote is ${String(needle.length)} characters, under the ` +
        `${String(MIN_CITATION_QUOTE)} a span has to reach before matching one line rather ` +
        `than most of them`,
    };
  }
  const cited = line >= 1 && line <= lines.length ? (lines[line - 1] ?? "") : null;
  if (cited !== null && haystack(cited).includes(needle)) {
    return { outcome: CITATION_OUTCOMES.verified, detail: "" };
  }
  for (const [index, held] of lines.entries()) {
    if (index + 1 === line) continue;
    if (!haystack(held).includes(needle)) continue;
    const at = String(index + 1);
    return {
      outcome: CITATION_OUTCOMES.moved,
      detail:
        line === 0
          ? `the citation names no line; the quoted text is at line ${at} of this session`
          : `the quoted text is at line ${at} of this session, not at line ${String(line)}`,
    };
  }
  return {
    outcome: CITATION_OUTCOMES.absent,
    detail:
      line >= 1 && cited === null
        ? `this session holds ${String(lines.length)} lines and the citation names line ${String(line)}`
        : "the quoted text is nowhere in the session this citation names",
  };
}

/**
 * EVERY CITATION IN ONE ANSWER, CHECKED. The map is keyed on the evidence object itself, which
 * is the same object the settlement writes into the record's payload, so a verdict cannot be
 * attached to the wrong citation by a position nobody recomputed.
 *
 * `lines` is asked for a session only when a citation of it carries a quote worth checking, so
 * an answer that quoted nothing costs no read at all.
 */
export function checkCitations(
  result: ExploreResult,
  sessions: readonly MaterialEntry[],
  lines: SessionLines,
): Map<Evidence, CitationCheck> {
  const checks = new Map<Evidence, CitationCheck>();
  const read = new Map<string, readonly string[] | null>();
  for (const evidence of citedEvidence(result)) {
    if (checks.has(evidence)) continue;
    const locator = evidence.locator;
    if (folded(locator.quote) === "") {
      checks.set(evidence, { outcome: CITATION_OUTCOMES.unquoted, detail: "" });
      continue;
    }
    const entry = admitCitation(locator.path, sessions);
    if (entry === null) {
      // Unreachable through the settlement, which refuses an unserved citation before it gets
      // here; a caller checking an answer it did not admit gets the honest answer rather than a
      // verdict about a session nobody served.
      checks.set(evidence, {
        outcome: CITATION_OUTCOMES.unchecked,
        detail: `${locator.path} is not a file this run was served`,
      });
      continue;
    }
    if (!read.has(entry.file)) read.set(entry.file, lines(entry));
    const held = read.get(entry.file) ?? null;
    if (held === null) {
      checks.set(evidence, {
        outcome: CITATION_OUTCOMES.unchecked,
        detail: `this hub could not read back the bytes of ${entry.file}`,
      });
      continue;
    }
    const check = checkQuote(locator.quote, held, locator.line);
    // A citation that named no line cannot be at the wrong one, so a quote found anywhere in
    // the session it names is verified — and the detail carries the line it should have stated.
    checks.set(
      evidence,
      locator.line === 0 && check.outcome === CITATION_OUTCOMES.moved
        ? { outcome: CITATION_OUTCOMES.verified, detail: check.detail }
        : check,
    );
  }
  return checks;
}

/** One count per outcome, every key present: a run whose citations were all sound and a run
 *  nothing looked at must not read the same way. */
export function citationTally(
  checks: ReadonlyMap<Evidence, CitationCheck>,
): Record<string, number> {
  const tally: Record<string, number> = {};
  for (const outcome of Object.values(CITATION_OUTCOMES)) tally[outcome] = 0;
  for (const check of checks.values()) tally[check.outcome] = (tally[check.outcome] ?? 0) + 1;
  return tally;
}

/**
 * WHAT A CYCLE'S REPORT SAYS ABOUT ONE ANSWER'S QUOTES, and it says nothing at all when they
 * all held. One line per run, not one per citation: the verdict a reader acts on is on the
 * record, and a report that listed every citation would bury the runs that went wrong among
 * the ones that did not.
 */
export function citationNotes(checks: ReadonlyMap<Evidence, CitationCheck>): readonly string[] {
  const tally = citationTally(checks);
  const moved = tally[CITATION_OUTCOMES.moved] ?? 0;
  const absent = tally[CITATION_OUTCOMES.absent] ?? 0;
  if (moved + absent === 0) return [];
  const said: string[] = [];
  if (moved > 0) said.push(`${String(moved)} at another line of the session named`);
  if (absent > 0) said.push(`${String(absent)} nowhere in the session named`);
  return [
    `of ${String(checks.size)} citations, ${said.join(" and ")}; ` +
      `the verdict is on the records, which stand`,
  ];
}

// ---------------------------------------------------------------------------- normalisation

/** Zero-width and bidirectional marks, which carry no content and survive a copy. */
const INVISIBLE = /[\u00ad\u200b-\u200f\u2028\u2029\u2060\ufeff]/gu;

/** One string as both sides of the comparison are held: composed, stripped of invisibles, every
 *  run of whitespace one space, trimmed. */
function folded(text: string): string {
  return text.normalize("NFC").replace(INVISIBLE, "").replace(/\s+/gu, " ").trim();
}

/** The searchable text of one material line: the record's own strings when it is the canonical
 *  JSON `prepare` writes, and the line itself when it is not. */
function haystack(line: string): string {
  const cached = HAYSTACKS.get(line);
  if (cached !== undefined) return cached;
  const built = folded(strings(line));
  // Bounded so a long session cannot make the check's own memory grow with the corpus; the
  // cache exists for the second citation of one line, not for the file.
  if (HAYSTACKS.size > MAX_CACHED_LINES) HAYSTACKS.clear();
  HAYSTACKS.set(line, built);
  return built;
}

const MAX_CACHED_LINES = 4096;
const HAYSTACKS = new Map<string, string>();

/** Every string a canonical record holds, in the order it holds them. A model quotes what it
 *  read, and what it read is the decoded prose rather than the JSON around it. */
function strings(line: string): string {
  const trimmed = line.trim();
  if (trimmed === "" || (!trimmed.startsWith("{") && !trimmed.startsWith("["))) return line;
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return line;
  }
  const out: string[] = [];
  const walk = (value: unknown): void => {
    if (typeof value === "string") {
      out.push(value);
      return;
    }
    if (Array.isArray(value)) {
      for (const item of value) walk(item);
      return;
    }
    if (typeof value === "object" && value !== null) {
      for (const item of Object.values(value)) walk(item);
    }
  };
  walk(parsed);
  return out.length === 0 ? line : out.join("\n");
}

/** One material file as lines, with the line ending normalised away and the trailing newline
 *  not counted as a line. It is here rather than at the caller because "what line 12 is" has to
 *  be one answer, and the settlement, the tests and any later reader all need the same one. */
export function materialLines(text: string): readonly string[] {
  const body = text.replace(/\r\n?/gu, "\n");
  const lines = body.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}
