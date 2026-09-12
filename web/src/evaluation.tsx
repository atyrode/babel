import { Link } from "react-router-dom";
import type {
  EvaluationCoverage,
  EvaluationItem,
  EvaluationReception,
  EvaluationRoleCoverage,
  EvaluationSubject,
} from "./api";
import { Badge, type Tone } from "./analysis";
import { formatTime } from "./format";

// Shared vocabulary for issue #219's evaluation surface (SPEC.md §4.12, §5.8,
// §8.5). Five product rules live here rather than in any one page, so no view
// can quietly disagree with another about them:
//
//   - A bare vote is a complete review. Reception renders the counts it was
//     given and never invents a rationale, a percentage, or a verdict from
//     them.
//   - Absence is not zero. An artifact nobody reviewed says so; it never
//     renders as unopposed, and a missing evaluator is a named gap rather
//     than a satisfied obligation.
//   - Coverage is role-specific. A reception vote does not discharge an
//     evidence check, so every coverage rendering is per role or says which
//     role it is about.
//   - Reception is not authority. Nothing in this module renders a review
//     disposition, and nothing here can record one.
//   - An operator's act is read from its own field. A reconsideration
//     decision is labelled from its recorded polarity, never from the reason
//     he wrote beside it, so reopening and retaining cannot be confused by
//     wording — his, a model's, or a pasted quotation's.
//
// The vocabularies themselves are the server's: the label tables below map
// values internal/evaluation defines, and an unrecognized value falls back to
// its own identifier rather than being dropped. A page that dropped a value it
// did not recognize would hide exactly the state a newly added one describes.

// humanize is the fallback for a vocabulary value this build has no label for.
// It is deliberately not clever: the identifier is shown with its separators
// turned into spaces, so a reader sees the value the service actually sent and
// can look it up, rather than seeing nothing at all.
function humanize(value: string): string {
  const text = value.replaceAll("_", " ").replaceAll("-", " ").trim();
  if (text === "") return "—";
  return text.charAt(0).toUpperCase() + text.slice(1);
}

// The sorts of §8.5, in the wording the section uses. "Recently strengthened"
// is named exactly because it is the one an operator is most likely to confuse
// with "New": it ranks by substantive contribution or new supporting material,
// and another bare vote never moves an item up it.
const SORT_LABELS: Record<string, string> = {
  recommended: "Recommended",
  recent: "New",
  strengthened: "Recently strengthened",
  contested: "Contested",
  unreviewed: "Under-reviewed",
  overdue: "Overdue",
  reconsider: "Reconsider",
};

// What each sort orders by, said in one line beside the control. §8.5 requires
// every ordering to name its basis, and a sort whose basis is only in a design
// document is a number the operator cannot argue with.
const SORT_BASIS: Record<string, string> = {
  recommended:
    "Your next useful decision: recorded priority, current work and pain, Reality context and permitted spend, then reception.",
  recent: "Newest revisions first. Nothing about how they were received.",
  strengthened:
    "Most recent substantive contribution or new supporting material first. Another bare vote does not move an item up this order.",
  contested:
    "Unresolved disagreement first, distinguishing mixed reception from an evidenced objection.",
  unreviewed: "Least exposure first — how little has been looked at, not how little it was liked.",
  overdue: "Oldest initial review still owed first.",
  reconsider: "Earlier decisions where the evidence has since changed.",
};

// The lifecycle lanes. The disposition vocabulary is preserved rather than
// replaced: duplicate and refine-requested are lanes of their own precisely
// because §8.5 requires them to stay reachable with their relationships.
const LANE_LABELS: Record<string, string> = {
  open: "Open",
  accepted: "Accepted — awaiting implementation",
  deferred: "Deferred",
  rejected: "Rejected",
  duplicate: "Duplicate",
  "refine-requested": "Refine requested",
  implemented: "Implemented — awaiting verification",
  verified: "Verified outcome",
  partial: "Partially implemented",
  contradicted: "Contradicted by later evidence",
  unverifiable: "Unverifiable",
  reconsider: "Reconsider",
};

const LANE_TONES: Record<string, Tone> = {
  open: "cyan",
  accepted: "green",
  deferred: "amber",
  rejected: "red",
  duplicate: "neutral",
  "refine-requested": "violet",
  implemented: "blue",
  verified: "green",
  partial: "amber",
  contradicted: "red",
  unverifiable: "amber",
  reconsider: "violet",
};

// The canonical coverage states. "No evaluator" is spelled out rather than
// left as `unsupported` because it is the one an operator will misread as a
// verdict: nothing reviewed this because nothing can, which is a gap and not
// a pass.
const COVERAGE_LABELS: Record<string, string> = {
  unreviewed: "Never reviewed",
  reviewed: "Reviewed",
  due: "Reassessment due",
  unsupported: "No evaluator",
  blocked: "Blocked",
  not_applicable: "Not applicable (named policy)",
  overdue: "Overdue",
};

// Tones for coverage. Reviewed is the only green one, and `not_applicable` is
// deliberately neutral rather than green: an intentional exemption is not an
// obligation met.
const COVERAGE_TONES: Record<string, Tone> = {
  unreviewed: "amber",
  reviewed: "green",
  due: "amber",
  unsupported: "red",
  blocked: "red",
  not_applicable: "neutral",
  overdue: "red",
};

const ROLE_LABELS: Record<string, string> = {
  reception: "Reception",
  evidence: "Evidence check",
  challenge: "Challenge",
  comparison: "Comparison",
  outcome: "Outcome verification",
  relevance: "Relevance",
};

// What each role is responsible for. It is shown because §4.12's whole point
// is that these answer different questions, and a coverage table of five words
// teaches nobody why an artifact with four votes is still uncovered.
const ROLE_BASIS: Record<string, string> = {
  reception: "Whether reviewers support, oppose or are unsure about the idea as written.",
  evidence: "Whether the citations actually show what the record says they show.",
  challenge: "A deliberate argument against, recorded as its own obligation.",
  comparison: "How this reads beside the other remedies for the same problem.",
  outcome: "Whether the promised result was observed, against the accepted criteria.",
  relevance: "Whether this matters for the work and pain the operator recorded.",
};

const KIND_LABELS: Record<string, string> = {
  hypothesis: "Hypothesis",
  observation: "Observation",
  finding: "Finding",
  proposal: "Proposal",
  evaluation: "Evaluation",
};

// The record kinds an evaluation history can hold. Each is a different kind of
// event and the history renders it as one: an assignment is work handed out, an
// attempt is what became of it, a checkpoint is a sweep that finished.
const RECORD_KIND_LABELS: Record<string, string> = {
  assessment: "Assessment",
  criteria: "Acceptance criteria",
  feedback: "Operator feedback",
  reconsider: "Reconsider raised",
  reconsider_decision: "Reconsideration decision",
  policy: "Policy change",
  assignment: "Assignment",
  attempt: "Attempt",
  checkpoint: "Coverage check",
};

const RECORD_KIND_TONES: Record<string, Tone> = {
  assessment: "cyan",
  criteria: "blue",
  feedback: "violet",
  reconsider: "amber",
  reconsider_decision: "violet",
  policy: "neutral",
  assignment: "neutral",
  attempt: "neutral",
  checkpoint: "neutral",
};

// The two acts a reconsideration decision can be.
//
// They are labelled by what happened, because the two differ in consequence
// and not only in wording: recording a reopen reopens the record — the
// decision and the reopened disposition are written in one transaction — and
// recording a retain moves no disposition at all. Neither label is ever
// derived from the operator's reason.
const RECONSIDER_DECISION_LABELS: Record<string, string> = {
  reopen: "Reopened",
  retain: "Earlier decision retained",
};

// The same two acts as a control offers them: an imperative a person can
// choose, rather than the past tense a history entry reads in. One value, two
// renderings, because "Reopened" is a bad thing to ask somebody to press and
// "Reopen" is a bad thing to call a record that already was.
const RECONSIDER_DECISION_ACTIONS: Record<string, string> = {
  reopen: "Reopen it",
  retain: "Keep the earlier decision",
};

// What choosing each act will do, for the control that offers it. The history
// sentences below are past tense and describe what happened; a chooser needs
// the consequence before he acts, which is a different sentence rather than
// the same one reworded.
const RECONSIDER_DECISION_MEANS: Record<string, string> = {
  reopen:
    "returns this record to undecided. The decision you reopen keeps its place in its history, and nothing is accepted or rejected by reopening it.",
  retain:
    "leaves the earlier decision exactly as it stands. Your reason is recorded beside the change it answers.",
};

// What each act did, in one sentence, shown beside the label. The retain
// sentence is the one that matters most: a reader scanning a history must not
// have to interpret the operator's prose to learn that nothing was reopened.
const RECONSIDER_DECISION_BASIS: Record<string, string> = {
  reopen:
    "the operator reopened this. The decision he reopened keeps its place in the history, and the record is undecided again until it is decided on its merits.",
  retain:
    "the operator read the change and kept the earlier decision. Nothing was reopened and nothing was re-decided.",
};

const RECONSIDER_DECISION_TONES: Record<string, Tone> = {
  reopen: "amber",
  retain: "neutral",
};

export const sortLabel = (value: string): string => SORT_LABELS[value] ?? humanize(value);
export const sortBasis = (value: string): string => SORT_BASIS[value] ?? "";
export const laneLabel = (value: string): string => LANE_LABELS[value] ?? humanize(value);
export const laneTone = (value: string): Tone => LANE_TONES[value] ?? "neutral";
export const coverageLabel = (value: string): string => COVERAGE_LABELS[value] ?? humanize(value);
export const coverageTone = (value: string): Tone => COVERAGE_TONES[value] ?? "neutral";
export const roleLabel = (value: string): string => ROLE_LABELS[value] ?? humanize(value);
export const roleBasis = (value: string): string => ROLE_BASIS[value] ?? "";
export const kindLabel = (value: string): string => KIND_LABELS[value] ?? humanize(value);
export const recordKindLabel = (value: string): string =>
  RECORD_KIND_LABELS[value] ?? humanize(value);
export const recordKindTone = (value: string): Tone => RECORD_KIND_TONES[value] ?? "neutral";

// A decision this build has no label for renders as its own identifier rather
// than as nothing: an unlabelled polarity is still a polarity, and dropping it
// would leave the entry reading as a reason with no act.
export const reconsiderDecisionLabel = (value: string): string =>
  RECONSIDER_DECISION_LABELS[value] ?? humanize(value);
export const reconsiderDecisionAction = (value: string): string =>
  RECONSIDER_DECISION_ACTIONS[value] ?? humanize(value);
export const reconsiderDecisionMeans = (value: string): string =>
  RECONSIDER_DECISION_MEANS[value] ?? "";
export const reconsiderDecisionBasis = (value: string): string =>
  RECONSIDER_DECISION_BASIS[value] ?? "";
export const reconsiderDecisionTone = (value: string): Tone =>
  RECONSIDER_DECISION_TONES[value] ?? "neutral";

// The route that opens one record. One record is one page, so the kind is not
// part of the destination any more: it is served with the record. The route is
// still built by this module's own table rather than from anything a record
// carries, on references.tsx's terms — nothing a model wrote may become a link
// destination.
export function recordRoute(subject: EvaluationSubject): string {
  return `/r/${encodeURIComponent(subject.id)}`;
}

// SubjectLink names a subject and opens its record. The identifier is shown
// beside the title because a title is a model's wording and two records may
// carry the same one; the id is what an operator quotes.
export function SubjectLink({ subject, title }: { subject: EvaluationSubject; title: string }) {
  return (
    <Link className="evaluation-subject-link" to={recordRoute(subject)}>
      {title ? (
        <strong className="untrusted-inline">{title}</strong>
      ) : (
        <span className="muted no-summary">no title recorded</span>
      )}
      <span className="secondary mono">{subject.id}</span>
    </Link>
  );
}


// Reception renders what was said about a revision, and refuses to say
// anything else.
//
// The three counts are separate numbers and never a ratio, a bar or a verdict:
// §4.12 makes reception the count of who said what, explicitly not evidence
// strength, independent corroboration, or a probability the idea is correct.
//
// Zero reviews renders as "nobody has reviewed this", never as three zeroes.
// A row of zeroes reads as unanimous absence of opposition, which is the exact
// falsehood the coverage vocabulary exists to prevent.
export function Reception({ reception }: { reception: EvaluationReception }) {
  if (reception.reviews === 0) {
    return (
      <span className="evaluation-reception none muted">
        no reviews yet
        {reception.skips > 0 && (
          <span className="secondary"> · {reception.skips} skipped</span>
        )}
      </span>
    );
  }
  return (
    <span className="evaluation-reception">
      <span className="vote support" title="Support votes">+{reception.support}</span>
      <span className="vote oppose" title="Opposing votes">−{reception.oppose}</span>
      <span className="vote unsure" title="Uncertain votes">?{reception.unsure}</span>
      <span className="secondary" title="Completed reviews; a skip is not a review">
        {reception.reviews} {reception.reviews === 1 ? "review" : "reviews"}
      </span>
      {reception.skips > 0 && (
        <span className="secondary" title="Reviews that could not be completed. A skip is not a vote.">
          {reception.skips} skipped
        </span>
      )}
    </span>
  );
}

// RoleCoverageTable is the role-by-role inventory for one artifact.
//
// Every applicable role has a row, including the ones nothing has looked at,
// because the question this answers is what is still owed. A role with an
// `unsupported`, `blocked` or `not_applicable` state carries the server's own
// reason, rendered verbatim: "not applicable" without a named policy behind it
// is the claim §4.12 refuses to let anything make.
//
// A role can be supported and not yet required — an evidence check is owed
// once somebody records an uncertainty, an outcome once the operator accepts.
// Those rows read as never reviewed with the server's sentence saying what
// would activate them, which is why the caption below distinguishes a gap from
// an obligation instead of letting every unreviewed row read as overdue work.
export function RoleCoverageTable({ rows }: { rows: EvaluationRoleCoverage[] | null }) {
  if (!rows || rows.length === 0) {
    return (
      <p className="muted">
        No review role applies to this kind of record, so nothing is owed and nothing is claimed
        to have been checked.
      </p>
    );
  }
  return (
    <table className="evaluation-roles">
      <caption className="secondary">
        Every role this kind of record supports, whether or not anything has looked. A role can
        be supported without being required yet; where that is so, the standing says what would
        make it due. Nothing here is inferred from an absence of reviews.
      </caption>
      <thead>
        <tr>
          <th>Role</th>
          <th>Standing</th>
          <th className="numeric">Reviews</th>
          <th>Last reviewed</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const last = formatTime(row.last_reviewed);
          return (
            <tr key={row.role}>
              <td>
                <strong>{roleLabel(row.role)}</strong>
                <span className="secondary">{roleBasis(row.role)}</span>
              </td>
              <td>
                <Badge label={coverageLabel(row.state)} tone={coverageTone(row.state)} />
                {row.overdue && <Badge label="Overdue" tone="red" />}
                {row.reason && <span className="secondary untrusted-inline">{row.reason}</span>}
              </td>
              <td className="numeric mono">{row.reviews}</td>
              <td>
                {last ? (
                  <span title={last.absolute}>{last.relative}</span>
                ) : (
                  <span className="muted">never</span>
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

// CoverageSummary is the deployment-wide inventory, plus the two facts §8.5
// requires beside it: when the last sweep finished, and whether anything is
// still owed. They are separate lines because they are separate facts — a
// check can complete while work stays overdue, and merging them would let a
// finished sweep read as a covered corpus.
export function CoverageSummary({ coverage }: { coverage: EvaluationCoverage }) {
  const checked = formatTime(coverage.last_check);
  const updated = formatTime(coverage.updated_at);
  const counts: Array<[string, number]> = [
    ["unreviewed", coverage.unreviewed],
    ["due", coverage.due],
    ["overdue", coverage.overdue],
    ["blocked", coverage.blocked],
    ["unsupported", coverage.unsupported],
    ["not_applicable", coverage.not_applicable],
    ["reviewed", coverage.reviewed],
  ];
  return (
    <div className="surface coverage-block">
      <div className="evaluation-coverage-counts">
        {counts.map(([state, count]) => (
          <div className="evaluation-coverage-count" key={state}>
            <span className="count-value mono">{count.toLocaleString()}</span>
            <span className="count-label">{coverageLabel(state)}</span>
          </div>
        ))}
      </div>
      {/* Records, by their weakest outstanding obligation — not role rows.
          A record with two reception votes and no activated evidence check
          counts as reviewed here, and the per-role table beside it is where
          a role that is supported but not yet required is still visible. */}
      <p className="secondary">Records, counted by the strongest obligation still outstanding.</p>
      <p className="muted">
        {checked ? (
          <>
            Last coverage check completed <span title={checked.absolute}>{checked.relative}</span>.
          </>
        ) : (
          <>No coverage check has completed on this deployment yet.</>
        )}{" "}
        A completed check and a covered corpus are different facts: the counts above are what
        the last completed sweep left owed.
        {updated && (
          <> Projection built <span title={updated.absolute}>{updated.relative}</span>.</>
        )}
      </p>
      {coverage.reason && (
        <p className="inline-warning untrusted-inline" role="status">{coverage.reason}</p>
      )}
    </div>
  );
}

// WhyHere renders §8.5's three explanations: why now, what still argues
// against acting, and what would change the recommendation.
//
// An empty list renders as nothing at all rather than as an empty heading. A
// bare vote has no rationale and §4.12 forbids inventing one, so "no reason
// recorded" is the honest rendering — and the section that would have held it
// simply does not appear, which is what keeps a page of bare votes from
// looking like a page of missing data.
export function WhyHere({ item }: { item: EvaluationItem }) {
  const reasons = item.reasons ?? [];
  const objections = item.objections ?? [];
  const wouldChange = item.would_change ?? [];
  if (reasons.length === 0 && objections.length === 0 && wouldChange.length === 0) {
    return (
      <p className="muted">
        No explanation is recorded for this position. Bare votes stay bare: nothing here generates
        a rationale that nobody wrote.
      </p>
    );
  }
  return (
    <div className="evaluation-why">
      {reasons.length > 0 && (
        <section>
          <h4 className="counter-heading">Why now</h4>
          <ul>
            {reasons.map((reason) => (
              <li className="untrusted-inline" key={reason}>{reason}</li>
            ))}
          </ul>
        </section>
      )}
      {objections.length > 0 && (
        <section className="counter-evidence">
          <h4 className="counter-heading">What still argues against acting</h4>
          <ul>
            {objections.map((objection) => (
              <li className="untrusted-inline" key={objection}>{objection}</li>
            ))}
          </ul>
        </section>
      )}
      {wouldChange.length > 0 && (
        <section>
          <h4 className="counter-heading">What would change this</h4>
          <ul>
            {wouldChange.map((change) => (
              <li className="untrusted-inline" key={change}>{change}</li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

// StaleNotice reports a projection that is not current, and never withholds
// the page for it. §8.5 requires a stale or unavailable projection to be
// readable and labelled: a reader who is told the ordering is old can still
// use it, and a reader shown nothing cannot.
export function StaleNotice({ stale, unavailable }: { stale: boolean; unavailable: string }) {
  if (!stale && !unavailable) return null;
  return (
    <div className="surface state-note evaluation-stale" role="status">
      <strong>{stale ? "This ordering is not current." : "Part of this reading is missing."}</strong>
      {unavailable && <span className="untrusted-inline">{unavailable}</span>}
      <span className="muted">
        The rows below are what the projection last held. They are shown rather than withheld, and
        they are not labelled as the deployment's current tally.
      </span>
    </div>
  );
}
