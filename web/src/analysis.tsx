import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  getFleetHosts,
  getSessions,
  type EvidenceLocator,
  type EvidenceRef,
  type FleetHost,
  type FleetHostsResponse,
  type HypothesisStatus,
  type ReviewStatus,
  type RunAuthority,
  type SessionSummary,
} from "./api";
import { formatTime } from "./format";

// Shared vocabulary for the Phase B analytical areas. Three product rules from
// SPEC.md live here rather than in any one page, so every claim renders the
// same way everywhere:
//
//   - Fallibility is visible where the claim is (§1): analytical text always
//     carries its confidence, counter-evidence, and an explicit "this is an
//     interpretation" frame, never on a separate page.
//   - Evidence is one click from every claim (§4.3): a claim renders with its
//     locator, and the locator is the prominent, actionable part.
//   - Untrusted content is quoted (§3): model wording, transcript excerpts,
//     and operator answers render inside a visibly quoted frame, distinct from
//     Babel's own chrome, and are never interpreted as markup.

export type Tone = "neutral" | "cyan" | "green" | "amber" | "red" | "violet" | "blue";

// statusTone maps the §4.2 exploration lifecycle onto the interface's color
// vocabulary. Rejected is red because it must be visible, not hidden: §5.2
// says sorting never deletes a hypothesis.
export function statusTone(status: HypothesisStatus | string): Tone {
  switch (status) {
    case "investigating":
      return "cyan";
    case "promoted":
      return "green";
    case "deferred":
      return "amber";
    case "rejected":
      return "red";
    case "queued":
      return "violet";
    default:
      return "neutral";
  }
}

export function reviewTone(status: ReviewStatus | string): Tone {
  switch (status) {
    case "accepted":
      return "green";
    case "rejected":
      return "red";
    case "deferred":
      return "amber";
    case "duplicate":
      return "violet";
    case "refine-requested":
      return "cyan";
    default:
      return "neutral";
  }
}

export function Badge({ label, tone = "neutral" }: { label: string; tone?: Tone }) {
  return <span className={`badge tone-${tone}`}>{label}</span>;
}

// FallibilityNote renders nothing, and the export stays.
//
// The §1 frame is still owed to the reader: Babel's analytical output is
// creative, fallible, incomplete interpretation recorded for human review,
// never an audit or a verified fact. It is now stated once, in the shell's
// footer (App.tsx, `.app-footer`), instead of once per analytical panel. A
// record page with four panels said it four times, and a caveat repeated four
// times on one screen is read zero times.
//
// The component keeps its name and its call sites so that the callers still
// mark where analytical content begins; what changed is that the mark is no
// longer a box on the page.
export function FallibilityNote() {
  return null;
}

// unescapeWhitespace turns the server's escaped whitespace back into real
// whitespace, for display only.
//
// Every string in every API response passes through internal/web's sanitize,
// which rewrites control characters as a visible `\u{HEX}` so that no
// model-authored byte can steer a terminal or a log line. That is the right
// default and it stays: what arrives here is inert text. But a transcript
// excerpt whose line breaks read `\u{A}` and whose indentation reads `\u{9}`
// is unreadable prose, and the operator's job on these pages is to read.
//
// So exactly three escapes are undone — tab, newline, carriage return — and
// nothing else. `\x{..}` stays escaped because an invalid byte has no display
// form, and the bidi and zero-width runes stay escaped because making them
// invisible again is the attack sanitize exists to stop. The result is still
// text: it is handed to React as a string and never as markup, so restoring a
// newline cannot restore an HTML tag.
//
// A literal two-character `\n` in the source bytes is deliberately left
// alone. It is content — a shell command, a Go string, a regex — and a
// display layer that rewrote it would be editing the evidence.
export function unescapeWhitespace(text: string): string {
  // The escape is three characters minimum and most strings hold none, so the
  // common case costs one scan and no allocation.
  if (!text.includes("\\u{")) return text;
  return text.replace(/\\u\{0*([9adAD])\}/g, (match, hex: string) => {
    switch (hex.toLowerCase()) {
      case "9":
        return "\t";
      case "a":
        return "\n";
      case "d":
        return "\r";
      default:
        return match;
    }
  });
}

// Quoted renders untrusted text: model wording, transcript excerpts, operator
// answers. React escapes the text, and the frame makes the trust boundary
// visible — a reader can always tell quoted material from Babel's own chrome.
// The label names the speaker; the body is verbatim bytes shown as text.
//
// The body's whitespace escapes are undone here, at the one place every page
// quotes model wording, so a claim's paragraphs are paragraphs. It remains a
// string handed to a <pre>: nothing about restoring a newline makes it markup.
export function Quoted({
  label,
  text,
  children,
}: {
  label: string;
  text: string;
  children?: ReactNode;
}) {
  return (
    <figure className="quoted">
      <figcaption className="quoted-label">{label}</figcaption>
      <pre className="quoted-text">{unescapeWhitespace(text)}</pre>
      {children}
    </figure>
  );
}

// GradingLine shows the model's own three-valued gradings as words. Never a
// bar, never a percentage: §10 warns that confidence never substitutes for
// evidence, and a visual meter would invite exactly that reading.
export function GradingLine({
  confidence,
  impact,
  temporal,
}: {
  confidence: string;
  impact: string;
  temporal?: string;
}) {
  return (
    <p className="grading-line">
      <span>
        confidence <strong>{confidence || "unstated"}</strong>
      </span>
      <span>
        impact <strong>{impact || "unstated"}</strong>
      </span>
      {temporal && (
        <span>
          temporal <strong>{temporal}</strong>
        </span>
      )}
      <span className="muted">model-graded, not verified</span>
    </p>
  );
}

// ---------------------------------------------------------------------------
// Following a locator.
//
// "Follow the evidence locators before believing a claim" is the sentence this
// interface repeats on every analytical page, and until now it could not be
// obeyed by clicking: a locator rendered as inert text naming an absolute path
// nobody can open from a browser.
//
// A locator names a file and a 1-based record line. A route names a session
// selector and a transcript position. The catalog is what connects them, and it
// is the only thing consulted here: every described session carries the source
// id its selector is built from, and a cited file's path ends in that source id.
// No harness directory layout is parsed and no selector is assembled from a
// path, so a file the catalog holds no session for resolves to nothing rather
// than to a plausible guess that would 404.
// ---------------------------------------------------------------------------

// SessionIndex maps a cited file's own name onto the sessions that could own
// it. The name is the key because it is the one part of the path that survives
// materializing a session somewhere else; the candidate's full source id is
// then checked against the path, so the key is a lookup and never the proof.
type SessionIndex = Map<string, SessionSummary[]>;

function fileKey(path: string): string {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(0, dot) : name;
}

function withoutExtension(path: string): string {
  const dot = path.lastIndexOf(".");
  return dot > path.lastIndexOf("/") ? path.slice(0, dot) : path;
}

// One catalog read serves every locator on a page, and it is shared across
// components rather than fetched per evidence list: a finding with four
// observations renders a dozen locators, and a dozen identical requests for the
// same listing would be this page's largest cost.
let sessionIndexRead: Promise<SessionIndex> | null = null;

function loadSessionIndex(): Promise<SessionIndex> {
  if (!sessionIndexRead) {
    sessionIndexRead = getSessions()
      .then((response) => {
        const index: SessionIndex = new Map();
        for (const session of response.sessions) {
          const key = fileKey(session.source_id);
          const bucket = index.get(key);
          if (bucket) bucket.push(session);
          else index.set(key, [session]);
        }
        return index;
      })
      .catch((reason) => {
        // A failed read is not cached. Locators render as text until the
        // catalog answers, and the next claim on the page retries; caching the
        // failure would make one slow moment permanent for the session.
        sessionIndexRead = null;
        throw reason;
      });
  }
  return sessionIndexRead;
}

// useSessionIndex reads the catalog once per mount and never fails a page: an
// unreachable listing leaves locators as the text they have always been.
function useSessionIndex(): SessionIndex | null {
  const [index, setIndex] = useState<SessionIndex | null>(null);
  useEffect(() => {
    let live = true;
    loadSessionIndex()
      .then((value) => {
        if (live) setIndex(value);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);
  return index;
}

// LocatorTarget is a resolved citation: the session it lives in, and the
// transcript position to land on.
interface LocatorTarget {
  to: string;
  title: string;
}

// locatorTarget resolves one locator against the catalog.
//
// The event index is the line minus one. internal/event stamps a locator with
// a 1-based record line and internal/transcript numbers the same records from
// zero, one record per line in both, so this is the two counts meeting rather
// than an offset that happens to look right.
function locatorTarget(
  locator: EvidenceLocator,
  selector: string | undefined,
  index: SessionIndex | null,
): LocatorTarget | null {
  let session: SessionSummary | undefined;
  if (!selector) {
    const candidates = index?.get(fileKey(locator.path));
    const stripped = withoutExtension(locator.path);
    session = candidates?.find((candidate) => stripped.endsWith(candidate.source_id));
    if (!session) return null;
  }
  const target = selector ?? session?.selector ?? "";
  const event = locator.line > 0 ? locator.line - 1 : 0;
  const name = session?.title || target;
  return {
    to: `/sessions/${encodeURIComponent(target)}?event=${event}`,
    title: locator.line > 0 ? `${name} · line ${locator.line}` : name,
  };
}

// EvidenceItems renders locator-bearing citations. The locator is the point:
// an observation without its locator is not evidence (§4.3), so the line and
// the digest render with every claim, and a citation this catalog can open
// renders as the link to the conversation it came from.
//
// A citation that cannot be opened renders as text, not as a link that would
// fail, and the reason is stated once for the list rather than once per row:
// what the reader needs to know is that the record still carries enough to
// reopen it from the archive, and that is one fact about the citations, not a
// property of each one.
// A record that cites nothing sends a JSON null rather than an empty list, so
// the list is read as possibly absent: a claim with no evidence is a real
// state, and it must not take its page down.
export function EvidenceItems({
  items,
  kind,
}: {
  items: EvidenceRef[] | null | undefined;
  kind: "supporting" | "counter";
}) {
  const index = useSessionIndex();
  const citations = items ?? [];
  if (citations.length === 0) return null;
  const targets = citations.map((item) => locatorTarget(item.locator, item.selector, index));
  const unopened = targets.reduce((count, target) => (target ? count : count + 1), 0);
  return (
    <>
      <ul className={kind === "counter" ? "evidence-list counter" : "evidence-list"}>
        {citations.map((item, position) => {
          const target = targets[position];
          return (
            <li key={`${item.locator.path}-${item.locator.line}-${position}`}>
              {target ? (
                <Link
                  className="evidence-locator link-target"
                  to={target.to}
                  title={`${item.locator.path}${item.locator.line > 0 ? `:${item.locator.line}` : ""}`}
                >
                  <span className="untrusted-inline">{target.title}</span>
                  <span className="evidence-open">open the cited line →</span>
                </Link>
              ) : (
                <span className="evidence-locator mono">
                  {item.locator.path}
                  {item.locator.line > 0 ? `:${item.locator.line}` : ""}
                </span>
              )}
              <span className="evidence-digest mono" title={item.locator.digest}>
                {item.locator.digest.slice(0, 12) || "no digest"}
              </span>
              {item.note && (
                <span className="evidence-note untrusted-inline">{unescapeWhitespace(item.note)}</span>
              )}
            </li>
          );
        })}
      </ul>
      {unopened > 0 && (
        <p className="secondary evidence-unopened">
          {unopened === citations.length
            ? "No session in the catalog matches the files these citations name"
            : `${unopened} of these citations name a file no session in the catalog matches`}
          {" — the run read them, nothing describes them here, so there is no conversation to " +
            "open. Each still carries its path, line and content digest, which is what reopening " +
            "it against the archive needs."}
        </p>
      )}
    </>
  );
}

// CounterEvidence renders §4.3's "explicit counter-evidence or absence
// thereof". Exactly one of the two is set, and both states are shown: an
// empty section would read as an unasked question, which is the one thing
// the record structure exists to prevent.
export function CounterEvidence({
  items,
  absent,
}: {
  items?: EvidenceRef[];
  absent?: boolean;
}) {
  if (items?.length) {
    return (
      <div className="counter-evidence">
        <h4 className="counter-heading">Counter-evidence</h4>
        <EvidenceItems items={items} kind="counter" />
      </div>
    );
  }
  if (absent) {
    return (
      <p className="counter-evidence-absent">
        Counter-evidence: none found — declared absent by the worker, not left unexamined.
      </p>
    );
  }
  return <p className="counter-evidence-absent unstated">Counter-evidence not stated.</p>;
}

// AppendOnlyTimeline renders an append-only history: newest state last, and a
// framing line saying so, because "this can only grow" is the legibility §4.7
// asks for rather than an implementation detail.
export function TimelineEntry({
  badge,
  tone,
  at,
  children,
}: {
  badge: string;
  tone: Tone;
  at: string;
  children?: ReactNode;
}) {
  const time = formatTime(at);
  return (
    <li className="timeline-entry">
      <Badge label={badge} tone={tone} />
      <div className="timeline-body">
        {children}
        {time && (
          <span className="secondary" title={time.absolute}>
            {time.relative} · {time.absolute}
          </span>
        )}
      </div>
    </li>
  );
}

// AUTHORITY_TONES colours the three authorities a run can have (#96's ladder).
// Operator is violet because it is the one a person exercised; policy and
// serendipity are the conductor's own, and telling them apart at a glance is
// the point of rendering the authority at all.
const AUTHORITY_TONES: Record<string, Tone> = {
  operator: "violet",
  policy: "blue",
  serendipity: "cyan",
};

// AuthorityMark renders why a run happened, wherever a receipt is listed.
//
// An unrecorded authority is stated rather than filled in. Receipts written
// before the field existed carry none, and every one of them was in fact
// started by an operator's own command — but "operator" and "operator, as far
// as anyone can tell from when this was written" are different claims, and a
// badge that made the second look like the first would be this interface
// inventing provenance. It also says nothing about whether the run's findings
// are true: authority is why Babel spent the tokens, not evidence about what
// it produced.
export function AuthorityMark({ authority }: { authority: RunAuthority | undefined }) {
  if (!authority || !authority.kind) {
    return (
      <span
        className="receipt-authority not-observed"
        title="This receipt was recorded before receipts carried an authority. Runs then were started by an operator's own command, which is what the label says and all it says."
      >
        operator (recorded before authority)
      </span>
    );
  }
  return (
    <span className="receipt-authority">
      <Badge label={authority.kind} tone={AUTHORITY_TONES[authority.kind] ?? "neutral"} />
      {authority.ref && <span className="mono untrusted-inline">{authority.ref}</span>}
    </span>
  );
}

// PartialListNotice says a listing may be short.
//
// The listings answer with the rows they could reach and mark the response
// when part of the catalog did not. Saying nothing would present an incomplete
// list as the whole of Babel's work — the one thing a reader cannot check for
// himself — and saying it in terms of publication state would describe
// plumbing instead of the list he is reading. So this is about the list: some
// records are missing from it, the ones shown are unaffected, and a reload is
// the retry.
export function PartialListNotice() {
  return (
    <div className="surface state-note scope-notice" role="status">
      <strong>This list may be incomplete</strong>
      <span>
        Part of the catalog did not answer, so records it holds are missing here. Everything
        shown is a real record; reload to try the rest again.
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The host vocabulary below serves the fleet diagnostic only.
//
// Nothing in the reading path renders which instance produced a record, or
// whether it has published anywhere: the catalog is one body of work, and a
// reader asking "what has Babel found" is not asking about computers. The
// remaining hook exists because the fleet page's subject genuinely is the
// machines, and it names them there.
// ---------------------------------------------------------------------------

// useFleetHosts loads the deployment's host vocabulary once per mount.
//
// It never fails a page. A deployment with no shared backend answers
// `configured: false`, which the page states as a fact; a catalog that did not
// answer leaves `configured` unknown, and the page's own rows still render.
export function useFleetHosts(): {
  hosts: FleetHost[];
  localHost: string | undefined;
  configured: boolean | null;
} {
  const [state, setState] = useState<FleetHostsResponse | null>(null);
  useEffect(() => {
    let live = true;
    getFleetHosts({ pending: true })
      .then((value) => {
        if (live) setState(value);
      })
      // The transport already publishes the failure to the page's error
      // banner, so this only keeps the chips from taking the list down.
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);
  return {
    hosts: state?.hosts ?? [],
    localHost: state?.local_host,
    configured: state === null ? null : state.configured,
  };
}
