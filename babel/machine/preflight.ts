/*
  THE DETERMINISTIC SECRET PREFLIGHT (SPEC §3 step 4, §6.4; #339).

  A credential pasted into a transcript years ago is in the corpus forever, and until this module
  existed the only thing between it and a model provider was the operator's choice of Code profile
  (SPEC §3, `docs/sandbox-threat-model.md` residual 5). `machine/prepare.ts` seals the normalized
  record stream into the material lease, the hub binds that lease read-only into a session's
  sandbox, and the session sends what it reads to a provider. This runs BEFORE the seal, inside the
  same single pass over the log, and replaces every likely-secret span with a marker naming the
  class and the position of what was there.

  DETERMINISTIC MEANS DETERMINISTIC. Patterns and one entropy heuristic; no model, no network, no
  clock, and no I/O at all — this module reads nothing and writes nothing. The same bytes produce
  the same redactions on any machine, which is what lets the source digest of the redacted stream
  be the digest a citation is checked against.

  TWO CONFIDENCES, AND THE DIFFERENCE IS REPORTED. A structural detector matched something a
  credential format documents about itself: an armour label, a vendor prefix at its documented
  length, a password in a URL's userinfo, a credential-named field assigned a literal. The one
  heuristic detector guesses from shape alone. A report calling both "detected" would have thrown
  away the distinction a reviewer acts on.

  FALSE POSITIVES ARE THE EXPENSIVE FAILURE HERE. A scanner that redacts every digest, commit,
  UUID and path makes the corpus useless for analysis and gets turned off, which is worse than not
  having built it. So the heuristic rejects hex runs, UUIDs, paths, placeholders and template
  references before it judges anything, demands three of the four character classes, and is
  suppressed inside embedded payloads and public armour. The table below is the review surface:
  every entry says what it is anchored on.

  COST IS PER BYTE. Every rule is applied to one record at a time as the pass produces it, so a
  240 MB log is still read once and nothing buffers more than one record.

  DELIBERATE DIFFERENCE FROM THE GO PRODUCT (v0.4.0:internal/preflight/redact.go). Go substituted a
  truncated digest OF THE VALUE so that a reader could see one credential recur across sessions.
  That placeholder is a commitment to the value and it travelled into hosted model input, which
  makes any low-entropy password recoverable by anyone who can enumerate candidates. Here the
  marker carries the class and the LOCATOR instead: recurrence is not something #339 asks for, and
  nothing derived from the secret's bytes crosses to the hub or to a provider.

  WHAT THIS IS NOT. §6.4 also names malformed and truncated sessions, transcript size, and
  duplicate or changed inputs. Those are corpus health — they change what a run should DO rather
  than what it may SEE — and none of them is here. No detector for personal data ships here and
  none is claimed: this module detects likely credentials.
*/

/**
 * The rule set's identity, recorded on every receipt this scan writes.
 *
 * A receipt saying a preparation was scanned, without naming WHICH rules ran, ages into nothing
 * the moment a detector is added. Bump it whenever the table below changes what it matches, so a
 * reviewer comparing two preparations can tell a clean corpus from an old rule set.
 */
export const PREFLIGHT_DETECTORS = "babel.preflight.detectors/1";

/**
 * THE CLASSES BABEL LOOKS FOR. The list is the security review: a class nobody can name is a
 * class nobody agreed to, and a refusal names members of this set and nothing else.
 */
export type SecretClass =
  | "aws-access-key-id"
  | "google-api-key"
  | "github-token"
  | "gitlab-token"
  | "slack-token"
  | "model-api-key"
  | "stripe-api-key"
  | "npm-token"
  | "jwt"
  | "bearer-token"
  | "credential-assignment"
  | "connection-string"
  | "private-key-block"
  | "high-entropy-string";

/** Whether a rule matched a documented credential format or guessed from shape. */
export type Confidence = "structural" | "heuristic";

/** Structural before heuristic wherever two candidates compete; see {@link resolve}. */
const RANK: Record<Confidence, number> = { structural: 0, heuristic: 1 };

interface Detector {
  readonly name: SecretClass;
  readonly confidence: Confidence;
  /** What this class is, stated as what the rule is anchored on. */
  readonly why: string;
  /** Null for the two detectors that are a scan rather than a pattern. */
  readonly pattern: RegExp | null;
  /**
   * The capture groups holding the credential ITSELF, first non-empty winning; empty means the
   * whole match is the credential. The surrounding context — a field name, an `Authorization`
   * prefix, a URL's scheme, user and host — stays outside the redacted range, because material
   * redacted past the point of being readable is material not worth sending.
   */
  readonly value: readonly number[];
  /** Whether {@link nonCredential} may drop a match this pattern is too coarse to exclude. */
  readonly reject: boolean;
}

/**
 * THE RULE TABLE, which is why this is a file of its own: what Babel looks for is reviewable as a
 * list.
 *
 * Every pattern carries `d` so a value group's own range is known, and `g` because one record may
 * hold several. The last two entries carry no pattern: a private key is recognized from either
 * armour marker alone ({@link armourSpans}) and the heuristic is a scan over character runs
 * ({@link entropySpans}).
 */
export const SECRET_CLASSES = [
  {
    name: "aws-access-key-id",
    confidence: "structural",
    why: "a documented AWS key-type prefix followed by the exact identifier length",
    pattern: /\b(?:AKIA|ASIA|ABIA|ACCA|AIPA|ANPA|AROA|A3T[A-Z0-9])[0-9A-Z]{16}\b/dg,
    value: [],
    reject: false,
  },
  {
    name: "google-api-key",
    confidence: "structural",
    why: "Google's documented `AIza` prefix followed by the exact key length",
    pattern: /\bAIza[0-9A-Za-z_-]{35}\b/dg,
    value: [],
    reject: false,
  },
  {
    name: "github-token",
    confidence: "structural",
    why: "a GitHub personal, OAuth, app or refresh token carrying its documented prefix",
    pattern: /\b(?:gh[pousr]_[0-9A-Za-z]{20,}|github_pat_[0-9A-Za-z_]{20,})/dg,
    value: [],
    reject: false,
  },
  {
    name: "gitlab-token",
    confidence: "structural",
    why: "GitLab's documented `glpat-` prefix and minimum token length",
    pattern: /\bglpat-[0-9A-Za-z_-]{16,}/dg,
    value: [],
    reject: false,
  },
  {
    name: "slack-token",
    confidence: "structural",
    why: "Slack's documented `xox[abprs]-` token prefix",
    pattern: /\bxox[abprs]-[0-9A-Za-z-]{10,}/dg,
    value: [],
    reject: false,
  },
  {
    name: "model-api-key",
    confidence: "structural",
    why: "the `sk-` provider-key prefix OpenAI documents and Anthropic and others copied",
    pattern: /\bsk-[0-9A-Za-z_-]{20,}/dg,
    value: [],
    reject: false,
  },
  {
    name: "stripe-api-key",
    confidence: "structural",
    why: "Stripe's documented secret and restricted key prefixes, live or test",
    pattern: /\b(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,}/dg,
    value: [],
    reject: false,
  },
  {
    name: "npm-token",
    confidence: "structural",
    why: "npm's documented `npm_` prefix followed by the exact token length",
    pattern: /\bnpm_[0-9A-Za-z]{36}\b/dg,
    value: [],
    reject: false,
  },
  {
    name: "jwt",
    confidence: "structural",
    // `eyJ` is base64url for the first two bytes of `{"`, so the header segment declares itself as
    // the start of a JSON object. Three dot-separated base64url runs without it are three words,
    // which is why this is anchored on the header rather than on the shape.
    why: "a base64url header that decodes to the start of a JSON object, then a payload and a signature",
    pattern: /\beyJ[0-9A-Za-z_-]{10,}\.[0-9A-Za-z_-]{10,}\.[0-9A-Za-z_-]{10,}/dg,
    value: [],
    reject: false,
  },
  {
    name: "bearer-token",
    confidence: "structural",
    why: "a credential presented the way an Authorization header presents one",
    // Case-insensitive because a header is `Authorization` in prose, `authorization` in a
    // normalized JSON key and `AUTHORIZATION` in a shell transcript, and all three are the same
    // credential. The same is true of every field name the rule below is anchored on.
    pattern: /\bbearer\s+([0-9A-Za-z\-._~+/]{20,}={0,2})/dgi,
    value: [1],
    reject: true,
  },
  {
    name: "credential-assignment",
    confidence: "structural",
    // The FIELD NAME is the structure here: a name whose stem is a credential word, a separator,
    // and a literal long enough to be a credential. In this corpus the separator is usually
    // JSON's `":"`, because what is scanned is one canonical JSON record.
    why: "a credential-named field assigned a literal value",
    pattern:
      /\b[A-Za-z0-9_.-]{0,32}(?:api[_-]?key|apikey|access[_-]?key|secret|password|passwd|pwd|token|credential|auth[_-]?token|bearer)[A-Za-z0-9_.-]{0,16}["']?\s*(?::=|=>|::|:|=)\s*(?:"([^"\n]{8,256})"|'([^'\n]{8,256})'|([^\s"',;)\]}]{8,256}))/dgi,
    value: [1, 2, 3],
    reject: true,
  },
  {
    name: "connection-string",
    confidence: "structural",
    // Only the password is redacted. The scheme, user and host are what make the finding
    // actionable and none of them is the credential.
    why: "a URL with a password in its userinfo, which travels wherever the URL is copied",
    pattern:
      /\b[A-Za-z][A-Za-z0-9+.-]{1,20}:\/\/[^\s:/?#@[\]]{0,64}:([^\s:/?#@]{1,128})@[^\s/?#]{1,255}/dg,
    value: [1],
    reject: true,
  },
  {
    name: "private-key-block",
    confidence: "structural",
    why: "a PEM armour block whose own label says PRIVATE, which is a credential in its entirety",
    pattern: null,
    value: [],
    reject: false,
  },
  {
    name: "high-entropy-string",
    confidence: "heuristic",
    why: "a run long, mixed and dense enough to look like a credential: a guess about shape",
    pattern: null,
    value: [],
    reject: true,
  },
] as const satisfies readonly Detector[];

function detectorNamed(name: SecretClass): Detector {
  const found = SECRET_CLASSES.find((candidate) => candidate.name === name);
  if (found === undefined) throw new Error(`preflight: no detector named ${name}`);
  return found;
}

const PRIVATE_KEY = detectorNamed("private-key-block");
const ENTROPY = detectorNamed("high-entropy-string");

/**
 * The shortest run the heuristic judges, and the Shannon bits per character it must reach.
 *
 * Both are the Go product's calibrated values (v0.4.0:internal/preflight/health.go): every
 * credential format worth catching that has no recognizable structure is longer than 24
 * characters, and 3.5 bits per character is above prose and identifiers in the same alphabet
 * while well below random base64, which sits near 6.
 */
const ENTROPY_MIN_LENGTH = 24;
const ENTROPY_MIN_BITS = 3.5;

/** One accepted detection: which rule fired, and the range of the credential itself. */
export interface SecretSpan {
  readonly class: SecretClass;
  readonly confidence: Confidence;
  /** Half-open character range of the value inside the record. */
  readonly start: number;
  readonly end: number;
}

/**
 * The accepted, non-overlapping, position-ordered detections in one record.
 *
 * It is the single implementation behind both the redacted text and the receipt's counts, so a
 * report can never disagree with the bytes a hosted session was given.
 */
export function findSecrets(text: string): readonly SecretSpan[] {
  if (text === "") return [];
  const regions = redactable(text);
  if (regions.length === 0) return [];
  const found: SecretSpan[] = [];
  for (const detector of SECRET_CLASSES) patternSpans(found, detector, text, regions);
  for (const region of regions) {
    const slice = text.slice(region.start, region.end);
    armourSpans(found, slice, region.start);
    entropySpans(found, slice, region.start);
  }
  return resolve(dropMarked(found, text), text.length);
}

/** A half-open character range. */
interface Region {
  readonly start: number;
  readonly end: number;
}

/**
 * WHERE A REDACTION MAY REACH, and why every span is clamped to one of these.
 *
 * The material's contract is one canonical JSON record per line (`machine/prepare.ts`) and the
 * prompt tells the model exactly that. A redaction replacing a range that crossed a `","` would
 * leave a line that no longer parses, so the material would stop being what it says it is — and
 * the deliberately over-claiming armour rule below reaches for a whole record's worth of text.
 *
 * So a redaction stays inside the JSON string it began in. A line the normalizer could not parse
 * is retained verbatim behind its `!` marker, and there the whole line after the marker is one
 * region: there is no structure left to protect.
 */
function redactable(text: string): readonly Region[] {
  if (text.startsWith("!")) return text.length > 1 ? [{ start: 1, end: text.length }] : [];
  const out: Region[] = [];
  let open = -1;
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (open < 0) {
      if (char === '"') open = i + 1;
      continue;
    }
    if (char === "\\") {
      i++;
      continue;
    }
    if (char === '"') {
      if (i > open) out.push({ start: open, end: i });
      open = -1;
    }
  }
  // An unterminated string means the record was torn mid-write; the rest of the line is still
  // content, and leaving it unscannable would make a torn capture the way past this gate.
  if (open >= 0 && open < text.length) out.push({ start: open, end: text.length });
  return out;
}

function patternSpans(
  out: SecretSpan[],
  detector: Detector,
  text: string,
  regions: readonly Region[],
): void {
  const pattern = detector.pattern;
  if (pattern === null) return;
  for (const match of text.matchAll(pattern)) {
    const range = valueRange(detector, match);
    if (range === null) continue;
    const [start, end] = range;
    const region = regions.find((candidate) => start >= candidate.start && start < candidate.end);
    if (region === undefined) continue;
    const clamped = Math.min(end, region.end);
    if (clamped <= start) continue;
    if (detector.reject && nonCredential(text.slice(start, clamped))) continue;
    out.push({ class: detector.name, confidence: detector.confidence, start, end: clamped });
  }
}

/** The credential's own range: the first non-empty declared value group, or the whole match. */
function valueRange(detector: Detector, match: RegExpExecArray): readonly [number, number] | null {
  for (const group of detector.value) {
    const range = match.indices?.[group];
    if (range !== undefined && range[1] > range[0]) return range;
  }
  if (detector.value.length > 0) return null;
  return [match.index, match.index + match[0].length];
}

const ARMOUR_BEGIN = /-----BEGIN ([A-Z0-9 ]{0,48})-----/g;
const ARMOUR_END = /-----END ([A-Z0-9 ]{0,48})-----/g;

/**
 * A private key recognized from either end of it.
 *
 * This is the detector the corpus's own shape forces. A private key is thousands of bytes, a
 * capture is crash-consistent per file (SPEC §6.1) and a very long line is split at a bounded
 * offset (`MAX_RECORD_CHARS`), so a pasted key can arrive as one record opening the armour and a
 * later one closing it. A rule demanding both markers in one record would find neither half.
 *
 * So a BEGIN claims to the matching END if this region has one and to the end of the region
 * otherwise, and an END with no BEGIN before it claims everything before it. The second rule
 * over-redacts a record that merely quotes an END marker in prose, and that is the right way to
 * be wrong: the alternative leaks the tail of every key that crosses a record boundary.
 *
 * PEM armour states its own content type, so a PUBLIC KEY, CERTIFICATE or CERTIFICATE REQUEST is
 * public by declaration rather than by guess, and is left alone.
 */
function armourSpans(out: SecretSpan[], slice: string, base: number): void {
  if (!slice.includes("-----")) return;
  const begins = [...slice.matchAll(ARMOUR_BEGIN)].filter(privateLabel);
  const ends = [...slice.matchAll(ARMOUR_END)].filter(privateLabel);
  let firstBegin = slice.length;
  for (const begin of begins) {
    firstBegin = Math.min(firstBegin, begin.index);
    const after = begin.index + begin[0].length;
    const close = ends.find((end) => end.index >= after);
    const end = close === undefined ? slice.length : close.index + close[0].length;
    out.push({ ...PRIVATE_KEY_SPAN, start: base + begin.index, end: base + end });
  }
  for (const end of ends) {
    if (end.index > firstBegin) continue;
    out.push({ ...PRIVATE_KEY_SPAN, start: base, end: base + end.index + end[0].length });
  }
}

const PRIVATE_KEY_SPAN = { class: PRIVATE_KEY.name, confidence: PRIVATE_KEY.confidence } as const;
const ENTROPY_SPAN = { class: ENTROPY.name, confidence: ENTROPY.confidence } as const;

/** Whether an armour label names a private key. Three callers must agree on this, because the
 *  same label decides a detection, a lone END marker's claim, and whether a complete block is
 *  public material the heuristic must stay out of. */
function privateLabel(match: RegExpExecArray): boolean {
  return (match[1] ?? "").includes("PRIVATE");
}

/**
 * The unstructured-credential heuristic: the rule for a secret with no documented shape at all.
 *
 * A candidate is a maximal run of credential-alphabet characters. It fires when the run is long,
 * uses at least three of the four character classes, and carries enough Shannon entropy per
 * character to be denser than prose or an identifier in the same alphabet. Separators are
 * deliberately outside the alphabet, so `name=value` is two candidates rather than one: a field
 * name merged into its value would defeat both the length and the class test.
 */
function entropySpans(out: SecretSpan[], slice: string, base: number): void {
  const masks = masked(slice);
  for (let i = 0; i < slice.length;) {
    if (!candidateChar(slice.charCodeAt(i))) {
      i++;
      continue;
    }
    let j = i;
    while (j < slice.length && candidateChar(slice.charCodeAt(j))) j++;
    if (qualifies(slice.slice(i, j)) && !overlapsAny(masks, i, j)) {
      out.push({ ...ENTROPY_SPAN, start: base + i, end: base + j });
    }
    i = j;
  }
}

/** Base64, base64url and hex all live inside this alphabet: `A-Za-z0-9`, `+`, `/`, `_` and `-`. */
function candidateChar(code: number): boolean {
  return (
    (code >= 97 && code <= 122) ||
    (code >= 65 && code <= 90) ||
    (code >= 48 && code <= 57) ||
    code === 43 ||
    code === 47 ||
    code === 95 ||
    code === 45
  );
}

/**
 * One candidate, decided. The rejections are the whole value of the rule: a detector firing on
 * every digest, identifier and path in a transcript is a detector an operator turns off.
 */
function qualifies(run: string): boolean {
  if (run.length < ENTROPY_MIN_LENGTH) return false;
  // A digest, a commit, a UUID: high-entropy by construction and public by purpose. Content
  // addressing means this corpus is full of them — Babel's own transcripts most of all, where
  // every id is a NAME AND A DIGEST: `prep-<64 hex>`, `run_<uuid>`, `hyp_<hex>`. A digest with a
  // label in front of it is still a digest, so one leading label is stripped before the test.
  // This is the same judgement as rejecting a bare digest rather than a wider one: a hex run is
  // indistinguishable from a content address either way, and a hex credential assigned to a
  // named field is caught structurally by `credential-assignment`.
  const stem = run.replace(/^[A-Za-z][A-Za-z0-9]{0,15}[-_]/, "");
  if (HEX.test(run) || UUID.test(run) || HEX.test(stem) || UUID.test(stem)) return false;
  if (nonCredential(run)) return false;
  if (run.includes("/")) {
    // A path is short segments joined by slashes; a base64 payload is long ones. Requiring one
    // qualifying segment keeps long paths quiet while still redacting the whole run when a
    // segment does qualify, so a matched blob is removed contiguously.
    return run.split("/").some(dense);
  }
  return dense(run);
}

/** The three floors, together: long enough, mixed enough, dense enough. */
function dense(value: string): boolean {
  return (
    value.length >= ENTROPY_MIN_LENGTH &&
    classes(value) >= 3 &&
    shannonBits(value) >= ENTROPY_MIN_BITS
  );
}

/**
 * How many of lower, upper, digit and symbol appear. Prose, snake_case identifiers and lowercase
 * hex reach two; random base64 reaches three or four. This one test removes most of what an
 * entropy floor alone would report.
 */
function classes(value: string): number {
  let lower = false;
  let upper = false;
  let digit = false;
  let other = false;
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code >= 97 && code <= 122) lower = true;
    else if (code >= 65 && code <= 90) upper = true;
    else if (code >= 48 && code <= 57) digit = true;
    else other = true;
  }
  return [lower, upper, digit, other].filter(Boolean).length;
}

/**
 * Shannon entropy of the candidate's own character distribution, in bits per character. It is
 * computed over the candidate rather than against a corpus model, so the rule stays local and
 * deterministic.
 */
function shannonBits(value: string): number {
  const freq = new Map<string, number>();
  for (const char of value) freq.set(char, (freq.get(char) ?? 0) + 1);
  let bits = 0;
  for (const count of freq.values()) {
    const p = count / value.length;
    bits -= p * Math.log2(p);
  }
  return bits;
}

const HEX = /^[0-9a-fA-F]+$/;
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

/**
 * The values that appear where a credential would and are documentation instead: placeholders,
 * shell and template references, and the words an unset or already-redacted field is filled with.
 * A detector that reports these teaches an operator to ignore it.
 *
 * Every word alternative demands the WHOLE value, and the ones that may carry a suffix demand a
 * separator before it. That is the difference between a rejection and a hole: a rule dropping any
 * value merely beginning with a documentation word would reject every real credential that
 * happens to start with one.
 */
const NON_CREDENTIAL =
  /^(?:x{3,}|\*{3,}|\.{3,}|-{3,}|_{3,}|<[^>]*>|\{\{[^}]*\}\}|\$\{?[a-z_][a-z0-9_]*\}?|%[a-z0-9_]+%|(?:your|our|my)(?:[-_][a-z0-9_-]*)?|(?:example|sample|dummy|placeholder|fake|test|changeme|replaceme|insert|redacted)(?:[-_][a-z0-9_-]*|[0-9]{0,4})?|hidden|omitted|elided|none|null|nil|true|false|empty|unset|undefined|todo|fixme|password|passwd|secret|token|apikey|api[-_]key|credential)$/i;

/**
 * Documentation rather than a credential. A path is included on purpose: `password_file=/run/keys/pw`
 * names where a credential lives, and reporting the path as the secret would both miss the secret
 * and cry wolf.
 */
function nonCredential(value: string): boolean {
  if (value === "") return true;
  if ("/$%<{".includes(value.slice(0, 1))) return true;
  if (value.startsWith("./") || value.startsWith("../") || value.startsWith("~/")) return true;
  if (NON_CREDENTIAL.test(value)) return true;
  // One character repeated is a mask, not a key.
  return value.split(value.slice(0, 1)).length - 1 === value.length;
}

/**
 * The length at which an unbroken base64 run is an embedded payload rather than a credential.
 *
 * No documented credential format reaches a kilobyte in one unbroken run: PEM armour wraps its
 * body at about sixty characters a line, and every vendor token, JWT and access key is far
 * shorter. The consequence is stated rather than hidden — an unwrapped private key body pasted as
 * one run longer than this is masked, and only the heuristic would have caught it anyway.
 */
const PAYLOAD_RUN_CHARS = 1 << 10;

/** An inline payload that declares itself. Length is irrelevant here: `data:…;base64,` says what
 *  follows is encoded content, so a small inline image is as clearly not a credential as a large
 *  one. */
const DATA_URI =
  /data:[A-Za-z0-9!#$&^_.+-]{0,64}\/?[A-Za-z0-9!#$&^_.+-]{0,64};base64,[A-Za-z0-9+/=]+/g;
const PUBLIC_ARMOUR = /-----BEGIN ([A-Z0-9 ]{0,48})-----[\s\S]*?-----END [A-Z0-9 ]{0,48}-----/g;

/**
 * Where the heuristic is suppressed: embedded payloads and public armour blocks.
 *
 * Masks bind the GUESS only. A structural detector matched a documented format and is trusted
 * inside a payload as much as outside one; suppressing it there would let a credential hide by
 * being pasted next to an image.
 */
function masked(slice: string): readonly Region[] {
  const out: Region[] = [];
  for (const match of slice.matchAll(DATA_URI)) {
    out.push({ start: match.index, end: match.index + match[0].length });
  }
  for (let i = 0, start = -1; i <= slice.length; i++) {
    if (i < slice.length && base64Char(slice.charCodeAt(i))) {
      if (start < 0) start = i;
      continue;
    }
    if (start >= 0) {
      if (i - start >= PAYLOAD_RUN_CHARS) out.push({ start, end: i });
      start = -1;
    }
  }
  for (const match of slice.matchAll(PUBLIC_ARMOUR)) {
    if (privateLabel(match)) continue;
    out.push({ start: match.index, end: match.index + match[0].length });
  }
  return out.sort((a, b) => a.start - b.start);
}

/** Base64 proper, which unlike the heuristic's alphabet includes `=` padding and excludes the
 *  base64url pair: a payload run is what this recognizes, and padding is part of one. */
function base64Char(code: number): boolean {
  return (
    (code >= 97 && code <= 122) ||
    (code >= 65 && code <= 90) ||
    (code >= 48 && code <= 57) ||
    code === 43 ||
    code === 47 ||
    code === 61
  );
}

function overlapsAny(regions: readonly Region[], start: number, end: number): boolean {
  for (const region of regions) {
    if (start < region.end && region.start < end) return true;
    if (region.start >= end) break;
  }
  return false;
}

/**
 * THE MARKER THE MATERIAL CARRIES IN PLACE OF A VALUE: the class, and the locator of the original.
 *
 * `[[babel-redacted:<class>@<line>:<offset>+<length>]]`. Every character of it is safe inside a
 * JSON string, which is what keeps a redacted record a parseable record. `line` is the record's
 * 1-based ordinal in the session's normalized stream — the same line number the material's own
 * file has, so a reader comparing the two is comparing one record — and `offset` and `length`
 * address the value inside that record BEFORE redaction. `machine/prepare.ts`'s
 * `resolveRedaction` is what turns that back into bytes, and only over the capture's own bytes,
 * which a job holding the archive binding fetches: nothing recoverable crosses to the hub.
 */
export function redactionMarker(
  secretClass: SecretClass,
  line: number,
  offset: number,
  length: number,
): string {
  return `[[babel-redacted:${secretClass}@${String(line)}:${String(offset)}+${String(length)}]]`;
}

/** Recognizes a marker this module already wrote. Redaction skips those regions, which is what
 *  makes it idempotent by construction rather than by the accident that no detector happens to
 *  match its own output — and Babel's own transcripts hold redacted material. */
const MARKER = /\[\[babel-redacted:[a-z0-9-]{1,40}@\d{1,12}:\d{1,12}\+\d{1,12}\]\]/g;

/**
 * Drops every span touching a marker already substituted, for every detector.
 *
 * A marker is Babel's own output rather than attacker-controlled bytes, so nothing can hide
 * there, while leaving detectors free to match it costs idempotence: `token=[[babel-redacted:…`
 * reads as an assignment whose value is a long literal, and a second pass would re-redact the
 * marker and destroy the locator inside it. Overlap rather than containment is the test, because
 * a captured value can begin inside a marker and end outside it.
 */
function dropMarked(spans: readonly SecretSpan[], text: string): readonly SecretSpan[] {
  const markers = [...text.matchAll(MARKER)].map((match) => ({
    start: match.index,
    end: match.index + match[0].length,
  }));
  if (markers.length === 0) return spans;
  return spans.filter((candidate) => !overlapsAny(markers, candidate.start, candidate.end));
}

/**
 * Overlapping candidates turned into the accepted set.
 *
 * Structural matches are considered before heuristic ones regardless of position, so a field name
 * merged into its value cannot let a guess displace a format match. Within a confidence the
 * earlier and then the longer span wins, and the class name breaks the last tie, so the result
 * does not depend on the order the table happens to be in.
 */
function resolve(spans: readonly SecretSpan[], length: number): readonly SecretSpan[] {
  const ordered = [...spans].sort(
    (a, b) =>
      RANK[a.confidence] - RANK[b.confidence] ||
      a.start - b.start ||
      b.end - a.end ||
      (a.class < b.class ? -1 : a.class > b.class ? 1 : 0),
  );
  const accepted: SecretSpan[] = [];
  for (const candidate of ordered) {
    if (candidate.start < 0 || candidate.end > length) continue;
    if (accepted.some((taken) => candidate.start < taken.end && taken.start < candidate.end)) {
      continue;
    }
    accepted.push(candidate);
  }
  return accepted.sort(
    (a, b) => a.start - b.start || (a.class < b.class ? -1 : a.class > b.class ? 1 : 0),
  );
}

/** One redacted span, as the machine that holds the session can find it again. */
export interface RedactionSite {
  readonly class: SecretClass;
  /** 1-based ordinal of the record in the session's normalized stream. */
  readonly line: number;
  /** Character offset of the value inside that record, before redaction. */
  readonly offset: number;
  /** How many characters the value was. The length is evidence; the bytes are not. */
  readonly length: number;
}

/**
 * What one session's pass found. The counts are complete; the sites are a bounded sample, so a
 * receipt cannot grow with the corpus.
 */
export interface ScanReport {
  readonly records: number;
  readonly redactions: number;
  /** Sorted by class, so two scans of the same corpus produce the same document. */
  readonly classes: readonly { readonly class: SecretClass; readonly redactions: number }[];
  readonly sites: readonly RedactionSite[];
  readonly sitesOmitted: number;
}

/** How many sites one session's report keeps. More than this is already more than a reviewer
 *  reads for one session, and the rest are counted rather than dropped silently. */
const MAX_SITES = 64;

/**
 * One session's scan: the records it redacted, and what it found.
 *
 * It is stateful because it is driven from inside the single pass over a log
 * (`machine/prepare.ts`'s `digests`), one record at a time, and the report is the fold over that
 * pass.
 */
export interface SecretScan {
  /** The record the material may hold, with every likely-secret span replaced by a marker. */
  redact(record: string, line: number): string;
  report(): ScanReport;
}

export function secretScan(): SecretScan {
  let records = 0;
  let redactions = 0;
  let sitesOmitted = 0;
  const sites: RedactionSite[] = [];
  const counts = new Map<SecretClass, number>();
  return {
    redact: (record, line) => {
      records += 1;
      // The trailing newline is the stream's structure rather than the record's content, and no
      // span may eat it: the material is one record per LINE.
      const ends = record.endsWith("\n");
      const body = ends ? record.slice(0, -1) : record;
      const spans = findSecrets(body);
      if (spans.length === 0) return record;
      let out = "";
      let prev = 0;
      for (const found of spans) {
        const length = found.end - found.start;
        out += body.slice(prev, found.start);
        out += redactionMarker(found.class, line, found.start, length);
        prev = found.end;
        redactions += 1;
        counts.set(found.class, (counts.get(found.class) ?? 0) + 1);
        if (sites.length < MAX_SITES) {
          sites.push({ class: found.class, line, offset: found.start, length });
        } else sitesOmitted += 1;
      }
      return ends ? `${out}${body.slice(prev)}\n` : out + body.slice(prev);
    },
    report: () => ({
      records,
      redactions,
      classes: [...counts.entries()]
        .map(([secretClass, count]) => ({ class: secretClass, redactions: count }))
        .sort((a, b) => (a.class < b.class ? -1 : a.class > b.class ? 1 : 0)),
      sites: [...sites],
      sitesOmitted,
    }),
  };
}

/**
 * WHAT A REFUSED PREPARATION SAYS, and the one thing it must never say.
 *
 * `secret preflight refused <selector>: <class> (<n>)[, …]; values are never named`
 *
 * The classes are named and counted because that is what the operator decides on: an AWS access
 * key id in a transcript is a different decision from a high-entropy guess. The value is absent
 * because a refusal is written into a receipt, into a run row, into whatever the operator pastes
 * it into — a message quoting the secret would have leaked it into a log, which is precisely what
 * the repository forbids. The classes are sorted, so the same corpus refuses with the same
 * sentence.
 */
export function refusalMessage(selector: string, report: ScanReport): string {
  const named = report.classes.map((row) => `${row.class} (${String(row.redactions)})`).join(", ");
  return `secret preflight refused ${selector}: ${named}; values are never named`;
}
