import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  getFleetHosts,
  type EvidenceRef,
  type FleetHost,
  type FleetHostsResponse,
  type FleetMark,
  type HypothesisStatus,
  type ReviewStatus,
  type RunAuthority,
  type SyncState,
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

// ---------------------------------------------------------------------------
// Fleet attribution (issue #109 item 4).
//
// Three rules from SPEC.md live here rather than in any one page, so a row
// reads the same way on the review inbox, the frontier and the receipt strip.
//
//   - Staged output is visibly staged (§6.5, §9). A pending-sync row is marked
//     on the row itself and not only in a badge's text, because "not yet
//     reviewable anywhere else" is a property of the record a reader has to be
//     able to see while scanning.
//   - Absence is absence (§3). A record whose origin instance registered no
//     host renders as "unattributed", never as this machine. Attributing one
//     machine's analysis to another is the failure this whole vocabulary
//     exists to prevent.
//   - Unknown is not a state of the record. When the shared catalog could not
//     be reached, a row's sync state is "unknown" — this machine did not find
//     out — which is a different claim from "local" and reads differently.
// ---------------------------------------------------------------------------

// SYNC_TONES colours the four frozen sync states. Committed is green because
// the record is globally durable and reviewable; pending-sync is amber because
// it is not yet, and that is a state to notice rather than an error; local is
// neutral because a machine that publishes nowhere is not in a lesser state;
// unknown is violet because it is about this machine's reach and not about the
// record at all.
const SYNC_TONES: Record<string, Tone> = {
  committed: "green",
  "pending-sync": "amber",
  local: "neutral",
  unknown: "violet",
};

// SYNC_TITLES say what each state means, because the words alone do not. An
// operator reading "local" has to be able to learn that nothing is going to
// carry the record anywhere, which is the one thing that distinguishes it from
// "pending-sync".
const SYNC_TITLES: Record<string, string> = {
  committed: "Committed to the shared catalog: globally durable and reviewable from any host.",
  "pending-sync":
    "Staged, not yet committed to the shared catalog. It is not reviewable from another host yet.",
  local:
    "Held only on this machine. No shared catalog row and nothing staged for one, so nothing is going to carry it anywhere.",
  unknown:
    "The shared catalog could not be reached, so whether this record has committed globally is not known.",
};

// SyncBadge renders one row's sync state. An absent value renders as an
// absence rather than as a guess: a row whose state the server did not send is
// not a local row.
export function SyncBadge({ sync }: { sync: SyncState | string | undefined }) {
  if (!sync) return <span className="muted">—</span>;
  return (
    <span className="sync-mark" title={SYNC_TITLES[sync] ?? "This record's sync state."}>
      <Badge label={sync} tone={SYNC_TONES[sync] ?? "neutral"} />
    </span>
  );
}

// syncRowClass marks a row beyond its badge's text. A reviewer scanning an
// inbox reads rows, not badges, and §6.5 asks for staged output to be visible
// rather than merely reported.
export function syncRowClass(mark: FleetMark): string {
  const classes: string[] = [];
  if (mark.sync === "pending-sync") classes.push("row-pending-sync");
  if (mark.sync === "unknown") classes.push("row-sync-unknown");
  if (mark.local_host === false) classes.push("row-remote-host");
  return classes.join(" ");
}

// UNATTRIBUTED is the one word this interface uses for a record no host can be
// named for. It is exported so a test asserts the string the UI renders rather
// than a copy of it.
export const UNATTRIBUTED = "unattributed";

// HostLabel renders which machine a row came from.
//
// Three cases and they read differently on purpose. This machine's own row says
// so, because an operator scanning a fleet-wide list needs to find his own work
// without comparing identifiers. Another machine's row names it. And a row with
// no host at all says "unattributed" in a muted style — the absence stated,
// never filled in with the local machine.
export function HostLabel({ mark }: { mark: FleetMark }) {
  if (!mark.host_attributed) {
    return (
      <span
        className="host-label muted unattributed-host"
        title="This record's origin instance registered no host, so which machine produced it is not recorded. It is not attributed to this one."
      >
        {UNATTRIBUTED}
      </span>
    );
  }
  return (
    <span className={mark.local_host ? "host-label local-host" : "host-label"}>
      <span className="mono untrusted-inline">{mark.host}</span>
      {mark.local_host && <span className="secondary">this host</span>}
    </span>
  );
}

// UnopenedNote says why a row has no content. The reasons call for different
// responses — a key to install, a binary to update, a store to check — so the
// server's own reason is rendered rather than a generic "unavailable".
export function UnopenedNote({ reason }: { reason: string | undefined }) {
  if (!reason) return null;
  return (
    <span className="unopened-note untrusted-inline" title="This machine could not open the record's content.">
      {reason}
    </span>
  );
}

// FleetNotice is what a machine with no shared backend says on a fleet-wide
// surface. It is a statement about the deployment rather than an empty state
// that reads like a bug, in ScopeNotice's style: the operator is told what the
// list is, whose it is, and that there is nothing else to see.
export function FleetNotice() {
  return (
    <div className="state-card scope-notice fleet-notice">
      <strong>This machine has no shared backend configured</strong>
      <span>
        Only its own records are shown, and there are no other hosts for Babel to read. That is
        this deployment's shape, not a failure to load anything.
      </span>
      <span className="secondary">
        Run <code>babel storage configure</code> to join a shared deployment.
      </span>
    </div>
  );
}

// SyncDegradedNotice is what a listing says when the shared catalog could not
// be reached. The rows are still this machine's own durable records and still
// render; what is missing is whether they have committed anywhere else, and
// saying so is what keeps their "unknown" badges meaningful.
export function SyncDegradedNotice({ detail }: { detail: string | undefined }) {
  return (
    <div className="state-card scope-notice sync-degraded-notice" role="status">
      <strong>Global sync state is not known for these records</strong>
      <span>{detail || SYNC_TITLES.unknown}</span>
      <span className="secondary">
        These are this machine's own records and they are shown in full. Only whether they have
        committed to the shared catalog is unknown.
      </span>
    </div>
  );
}

// HostScope is what a host chip row selects.
//
// `fleet` is false for this machine alone, which is the default: the server's
// fleet-wide read is opt-in, and a list that silently became deployment-wide
// would make an operator's own backlog look like someone else's work. `host` is
// the machine an already-merged list is narrowed to — null for every machine,
// and the empty string for the group with no host attribution, which is a real
// selection rather than the absence of one.
export interface HostScope {
  fleet: boolean;
  host: string | null;
}

export const LOCAL_SCOPE: HostScope = { fleet: false, host: null };

// inHostScope narrows an already-merged list. Narrowing happens here rather
// than on the server because the merge is what the request asked for: a chip
// that re-fetched would make the operator wait to hide rows he already has.
//
// The match is on host identity, never on the display name (see FleetMark).
export function inHostScope(mark: FleetMark, scope: HostScope): boolean {
  if (!scope.fleet || scope.host === null) return true;
  if (scope.host === "") return !mark.host_attributed;
  return mark.host_id === scope.host;
}

// HostChips is the host filter: this machine, every machine, then one chip per
// machine that holds records.
//
// The vocabulary is the server's rather than the current page's, so the options
// do not change as the operator pages through a list, and the unattributed group
// gets a chip of its own whenever it holds anything — a group with no chip is a
// group whose records cannot be reached.
export function HostChips({
  hosts,
  scope,
  localHost,
  onSelect,
}: {
  hosts: FleetHost[];
  scope: HostScope;
  localHost: string | undefined;
  onSelect: (scope: HostScope) => void;
}) {
  if (hosts.length === 0) return null;
  return (
    <div className="filter-chips host-chips" aria-label="Filter by host">
      <button
        type="button"
        className={scope.fleet ? "chip" : "chip active"}
        onClick={() => onSelect(LOCAL_SCOPE)}
        title="Only this machine's own records, which is what every other Babel listing shows."
      >
        This host
      </button>
      <button
        type="button"
        className={scope.fleet && scope.host === null ? "chip active" : "chip"}
        onClick={() => onSelect({ fleet: true, host: null })}
      >
        All hosts
      </button>
      {hosts.map((host) => (
        <button
          type="button"
          className={scope.fleet && scope.host === host.host_id ? "chip active" : "chip"}
          onClick={() => onSelect({ fleet: true, host: host.host_id })}
          key={host.host_id || UNATTRIBUTED}
          title={
            host.attributed
              ? `${host.records} record${host.records === 1 ? "" : "s"}${host.pending > 0 ? `, ${host.pending} staged` : ""}`
              : "Records whose origin instance registered no host. Which machine produced them is not recorded."
          }
        >
          <span className="untrusted-inline">
            {host.attributed ? host.host || host.host_id : UNATTRIBUTED}
          </span>
          {host.attributed && host.host_id === localHost && (
            <span className="secondary">this host</span>
          )}
          {host.pending > 0 && <span className="chip-pending">{host.pending} staged</span>}
        </button>
      ))}
    </div>
  );
}

// useFleetHosts loads the host filter's vocabulary once per mount.
//
// It never fails a page. A machine with no shared backend answers
// `configured: false`, which the page states as a fact about the deployment; a
// catalog that did not answer leaves `configured` unknown and the chips absent,
// and the page's own rows still render. The filter is chrome; the records are
// the content, and losing the chrome must not lose them.
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
