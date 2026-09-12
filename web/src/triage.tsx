import { Link } from "react-router-dom";
import { unescapeWhitespace } from "./analysis";
import type { TriageAdvice as Advice } from "./api";
import { formatTime } from "./format";

// Babel's own advice about a proposal, shown on the page where the operator
// decides.
//
// The block is deliberately not a verdict panel. It leads with the argument
// against acting, because a proposal arrives already argued for and the case
// nobody else wrote down is the only thing a second reading can add; the rank
// is stated as a place in a pile rather than as a score; and the cluster is a
// list of records to compare, not a merge somebody performed. Every id is a
// link, so "this says the same thing as that" can be checked rather than
// believed.
//
// Nothing here is a disposition and nothing here can become one. The decide
// action lives on the review page and takes the operator's own identity; this
// is an opinion sitting beside the record, which the operator is free to
// ignore, and it is phrased that way on purpose.

function AdviceCard({ advice, subject }: { advice: Advice; subject: string }) {
  const recorded = formatTime(advice.recorded_at);
  // Read from an alternative, the advice is about the record it was offered
  // instead of. Saying so is the only account that page has of where it came
  // from, and reading it as advice about itself would invert the argument.
  const aboutThis = advice.proposal_id === subject;
  return (
    <li className="triage-advice">
      <p className="triage-standing">
        {aboutThis ? (
          <>
            Babel read this alongside {advice.cohort === 1 ? "nothing else" : `${advice.cohort} proposals`}
            {advice.cohort > 1 && <> and would read it <strong>{ordinal(advice.rank)}</strong></>}.
          </>
        ) : (
          <>
            Babel offered this instead of{" "}
            <Link className="mono" to={`/r/${encodeURIComponent(advice.proposal_id)}`}>
              {advice.proposal_id}
            </Link>
            . The argument below is against that record, not this one.
          </>
        )}
      </p>
      {advice.ranking && (
        <p className="untrusted-inline">{unescapeWhitespace(advice.ranking)}</p>
      )}
      <div className="counter-evidence">
        <h4 className="counter-heading">The case against acting on it</h4>
        <p className="untrusted-inline">{unescapeWhitespace(advice.counter_argument)}</p>
      </div>
      {advice.cluster.length > 0 && (
        <p className="triage-cluster">
          <span className="muted">Says much the same as</span>{" "}
          {advice.cluster.map((id, index) => (
            <span key={id}>
              {index > 0 && " · "}
              <Link className="mono" to={`/r/${encodeURIComponent(id)}`}>{id}</Link>
            </span>
          ))}{" "}
          <span className="muted">— worth comparing before deciding either way.</span>
        </p>
      )}
      {aboutThis && advice.alternative_id && (
        <p className="triage-alternative">
          <span className="muted">Babel also wrote a different proposal for the same material:</span>{" "}
          <Link className="mono" to={`/r/${encodeURIComponent(advice.alternative_id)}`}>
            {advice.alternative_id}
          </Link>
          <span className="muted">
            {" "}— a separate record, reviewed on its own terms. This one is untouched.
          </span>
        </p>
      )}
      <p className="muted triage-provenance">
        <span className="mono">{advice.run_id}</span>
        {recorded && <> · {recorded.relative}</>}
      </p>
    </li>
  );
}

function ordinal(rank: number): string {
  const tens = rank % 100;
  if (tens >= 11 && tens <= 13) return `${rank}th`;
  switch (rank % 10) {
    case 1:
      return `${rank}st`;
    case 2:
      return `${rank}nd`;
    case 3:
      return `${rank}rd`;
    default:
      return `${rank}th`;
  }
}

export function TriageAdvice({
  advice,
  subject,
}: {
  advice: Advice[] | undefined;
  subject: string;
}) {
  if (!advice || advice.length === 0) return null;
  return (
    <article className="surface">
      <p className="eyebrow">Babel's own reading</p>
      <h2>What Babel thinks of this, before you decide</h2>
      <p className="muted">
        Advice, not a decision. Babel may rank, cluster, argue against and propose an alternative;
        accepting, rejecting, deferring and marking a duplicate remain yours, and nothing here has
        been recorded against the record.
      </p>
      <ul className="triage-list">
        {advice.map((one) => (
          <AdviceCard key={one.id} advice={one} subject={subject} />
        ))}
      </ul>
    </article>
  );
}
