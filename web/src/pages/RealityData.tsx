import { Link } from "react-router-dom";
import type { QuestionSummary } from "../api";

// What the Ask surface reads that the shared client does not describe yet,
// plus the two small renderers every Ask page needs for it.
//
// The fields below are served by internal/web/reality.go and are additive:
// they name things the ledger previously shipped as identifiers only. They
// are declared here as an augmentation rather than edited into api.ts because
// api.ts is one file several surfaces are growing at once; folding them into
// the interfaces they belong to is a merge, not a redesign.
declare module "../api" {
  interface QuestionSummary {
    // about_name names target_entity_ids positionally: index i is what a
    // reader calls target i, empty where the ledger cannot name it.
    about_name?: string[];
  }

  interface QuestionRow {
    about_name?: string[];
  }

  interface FactValueView {
    // object_name is what a reader calls an entity-valued fact's object, so
    // "contains the repository Babel" reads as a claim rather than as a
    // lookup task.
    object_name?: string;
  }

  interface EntityDetail {
    // candidates are the frontier records a context snapshot scoped to this
    // subject, newest first. It is the only stored link between the
    // ledger and the analysis that concerns it.
    candidates?: CandidateRow[];
  }
}

export interface CandidateRow {
  id: string;
  statement: string;
  status: string;
  created_at: string;
}

// Subject renders what a question is about as the name a reader knows, with
// the identifier kept only as the link's destination.
//
// The name can be absent — an identity the ledger no longer holds keeps its
// identifier and loses only its name — and when it is, the identifier is the
// whole truth and is shown as such. Nothing here prints both: a name followed
// by a hex string is the lookup task this page exists to remove.
export function Subject({ id, name }: { id: string; name?: string }) {
  return (
    <Link
      className={name ? "ask-subject untrusted-inline" : "ask-subject mono"}
      to={`/ask/entities/${encodeURIComponent(id)}`}
    >
      {name || id}
    </Link>
  );
}

// Subjects renders the whole "about" line of a question, from the two arrays
// the ledger serves side by side.
export function Subjects({ ids, names }: { ids: string[]; names?: string[] }) {
  if (ids.length === 0) return null;
  return (
    <p className="ask-about">
      <span className="ask-about-label">About</span>
      {ids.map((id, index) => (
        <span key={id}>
          {index > 0 && <span aria-hidden="true"> · </span>}
          <Subject id={id} name={names?.[index]} />
        </span>
      ))}
    </p>
  );
}

// Identifiers is the disclosure every Ask page hides its hex behind.
//
// An identifier is machinery: it is what a link resolves, what a merge is
// argued about and what an operator pastes into a command, and it is never
// what a record says. The record page made the same distinction by peeling
// its machinery away from its claim, and this is that rule applied to the
// ledger — the identifiers are one click away on every page, and on no page
// are they the first thing read.
export function Identifiers({ rows }: { rows: [string, string][] }) {
  return (
    <details className="ask-ids">
      <summary>Identifiers</summary>
      <dl>
        {rows.map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd className="mono">{value}</dd>
          </div>
        ))}
      </dl>
    </details>
  );
}

// scoreFactors is §4.8's five ranking factors in the ledger's own order.
//
// The order is fixed here rather than taken from the object's keys because a
// JSON object has no order at all, and a factor list that reshuffled between
// two questions would be unreadable as a comparison. Zero-valued factors are
// kept: "nothing about this is stale" is part of why a question ranks where
// it does, and dropping it leaves a reader unable to tell a factor that
// scored nothing from one this build does not measure.
export const scoreFactors: { key: string; label: string; why: string }[] = [
  { key: "affected-work", label: "Work it blocks", why: "candidates that cannot proceed until this is answered" },
  { key: "security-impact", label: "Sensitivity", why: "how much a wrong answer here would cost" },
  { key: "dependency-count", label: "Work that depends on it", why: "how much analysis referenced this question" },
  { key: "staleness", label: "Staleness", why: "days past the freshness of the facts behind it" },
  { key: "avoided-cost", label: "Cost it avoids", why: "spend the ledger expects an answer to save" },
];

// ScoreBreakdown is the arithmetic behind a question's place in the queue,
// shown so that the ordering can be argued with.
//
// §4.8 fixes the five factors and this is a rendering of them, never a second
// opinion: the bars are the served terms scaled against the largest one, and
// the total is the ledger's own score rather than this component's sum. A
// factor the ledger stopped serving disappears rather than reading as zero,
// because a missing measurement and a measured nothing are different claims.
export function ScoreBreakdown({ question }: { question: QuestionSummary }) {
  const terms = question.terms ?? {};
  const present = scoreFactors.filter((factor) => factor.key in terms);
  if (present.length === 0) return null;
  const widest = present.reduce((max, factor) => Math.max(max, Math.abs(terms[factor.key])), 0);

  return (
    <details className="why-rank">
      <summary>
        Why this is ranked here
        <span className="why-rank-score mono">{question.score}</span>
      </summary>
      <div className="why-rank-body">
        <p className="muted">
          Attention only. The five factors are §4.8's, the weights are the ledger's, and the
          total below is what it scored — not this page's arithmetic.
        </p>
        <ol className="rank-factors">
          {present.map((factor) => {
            const value = terms[factor.key];
            const share = widest === 0 ? 0 : (Math.abs(value) / widest) * 100;
            return (
              <li className="rank-factor" key={factor.key}>
                <span className="rank-factor-name">
                  {factor.label}
                  <span className="rank-factor-why">{factor.why}</span>
                </span>
                <span className="rank-track" aria-hidden="true">
                  <span
                    className={value < 0 ? "rank-fill negative" : "rank-fill"}
                    style={{ width: `${share}%` }}
                  />
                </span>
                <span className="rank-factor-weight mono">{value > 0 ? `+${value}` : value}</span>
              </li>
            );
          })}
        </ol>
        <p className="rank-total">
          <span>Total</span>
          <span className="mono">{question.score}</span>
        </p>
      </div>
    </details>
  );
}
