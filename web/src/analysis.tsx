import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import type { EvidenceRef, HypothesisStatus, ReviewStatus } from "./api";
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

// FallibilityNote is the §1 frame, rendered beside analytical content rather
// than on an "about" page: creative, fallible, incomplete interpretation for
// human review — never an audit or a verified fact.
export function FallibilityNote() {
  return (
    <p className="fallibility-note">
      <span aria-hidden="true">≈</span>
      Fallible interpretation, not established fact — Babel's analytical output is creative and
      incomplete, recorded for human review. Follow the evidence locators before believing a claim.
    </p>
  );
}

// Quoted renders untrusted text: model wording, transcript excerpts, operator
// answers. React escapes the text, and the frame makes the trust boundary
// visible — a reader can always tell quoted material from Babel's own chrome.
// The label names the speaker; the body is verbatim bytes shown as text.
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
      <pre className="quoted-text">{text}</pre>
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

// EvidenceItems renders locator-bearing citations. The locator is the point:
// an observation without its locator is not evidence (§4.3), so the path,
// line, and digest render with every claim, and when the server resolved the
// locator to a catalogued session the excerpt links straight to it.
export function EvidenceItems({ items, kind }: { items: EvidenceRef[]; kind: "supporting" | "counter" }) {
  if (!items.length) return null;
  return (
    <ul className={kind === "counter" ? "evidence-list counter" : "evidence-list"}>
      {items.map((item, index) => (
        <li key={`${item.locator.path}-${item.locator.line}-${index}`}>
          <span className="evidence-locator mono">
            {item.locator.path}
            {item.locator.line > 0 ? `:${item.locator.line}` : ""}
            <span className="evidence-digest" title={item.locator.digest}>
              {item.locator.digest.slice(0, 12) || "no digest"}
            </span>
          </span>
          {item.note && <span className="evidence-note">{item.note}</span>}
          {item.selector && (
            <Link className="evidence-open" to={`/sessions/${encodeURIComponent(item.selector)}`}>
              Open source session →
            </Link>
          )}
        </li>
      ))}
    </ul>
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
