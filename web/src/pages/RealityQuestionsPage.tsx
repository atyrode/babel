import { useCallback, useEffect, useState, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { getRealityQuestions, type QuestionsResponse } from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import { classTone, questionStateTone } from "../reality";

// Every question the ledger has ever asked (SPEC.md §8.4).
//
// The inbox next door is what the operator still has to move, and it is right
// to be narrow. But a question does not stop existing when it leaves: an answer
// given last month is the provenance of the facts it produced, a declined
// question is the record of a refusal that suppresses re-asks, and a snoozed
// one is a decision to come back. Until this page each of those was stored and
// unreachable, which §8.4 counts as not being in the product.
function RealityQuestionsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const state = params.get("state") ?? "";
  const [data, setData] = useState<QuestionsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getRealityQuestions(state ? { state } : {})
      .then(setData)
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [state]);

  useEffect(load, [load]);

  const items = data?.items ?? [];
  // The census counts the whole ledger rather than the filtered page, so the
  // filter row can say what else is there — and so choosing a state that holds
  // nothing is impossible rather than merely disappointing.
  const states = data?.states ?? [];
  const total = states.reduce((sum, entry) => sum + entry.count, 0);

  return (
    <section className="page questions-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Reality Ledger</p>
          <h1>What you said</h1>
          <p className="subtitle">
            Every question Babel has asked about your world, newest first — answered, refused
            and deferred alike. What is still waiting on you is under What it needs.
          </p>
        </div>
        <div className="heading-meta">
          {data && (
            <span className="count-label">
              {state
                ? `${items.length.toLocaleString()} of ${total.toLocaleString()} questions`
                : `${total.toLocaleString()} ${total === 1 ? "question" : "questions"}`}
            </span>
          )}
        </div>
      </div>

      {states.length > 0 && (
        <div className="toolbar surface">
          <div className="filter-chips" aria-label="Filter by state">
            <button
              type="button"
              className={state === "" ? "chip active" : "chip"}
              onClick={() => setParams({})}
            >
              All {total}
            </button>
            {states.map((entry) => (
              <button
                type="button"
                key={entry.state}
                className={state === entry.state ? "chip active" : "chip"}
                onClick={() => setParams({ state: entry.state })}
              >
                {entry.state} {entry.count}
              </button>
            ))}
          </div>
        </div>
      )}

      {loading && !data && (
        <div className="surface state-note"><span className="spinner" /> Reading the ledger…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>The questions could not be loaded.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>{state ? `No ${state} questions` : "Babel has asked nothing yet"}</strong>
          <span>
            {state
              ? "No question in the ledger stands in that state."
              : "A question is asked when exploration meets knowledge about your systems that is " +
                "missing, stale, or contradictory. Nothing has needed asking so far."}
          </span>
        </div>
      )}

      {items.length > 0 && (
        <div className="surface flush">
          <div className="table-scroll">
            <table className="frontier-table">
              <thead>
                <tr>
                  <th>Question</th>
                  <th>About</th>
                  <th>State</th>
                  <th>Class</th>
                  <th className="numeric">Answers</th>
                  <th className="numeric">Plans</th>
                  <th>Asked</th>
                </tr>
              </thead>
              <tbody>
                {items.map((item) => {
                  const created = formatTime(item.created_at);
                  const to = `/ask/questions/${encodeURIComponent(item.id)}`;
                  // The prompt is a real link, so a click that landed on it
                  // has already been handled; following the row as well would
                  // navigate twice and break middle-click and modified
                  // clicks, which are the whole reason the link exists.
                  const open = (event: ReactMouseEvent | ReactKeyboardEvent) => {
                    if (event.target instanceof Element && event.target.closest("a")) return;
                    navigate(to);
                  };
                  return (
                    <tr
                      key={item.id}
                      tabIndex={0}
                      role="link"
                      onClick={open}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") open(event);
                      }}
                    >
                      {/* The prompt, and nothing under it. The identifier
                          used to sit here as a second line on every row: a
                          column of hex the eye had to skip past to read the
                          question. It is on the question's own page, under
                          the machinery disclosure. */}
                      <td className="statement-cell">
                        <Link className="ask-row-link untrusted-inline" to={to}>{item.prompt}</Link>
                      </td>
                      <td className="statement-cell">
                        {item.target_entity_ids.length === 0 ? (
                          <span className="muted">—</span>
                        ) : (
                          item.target_entity_ids.map((entityID, index) => (
                            <span key={entityID} className="untrusted-inline">
                              {index > 0 && ", "}
                              {item.about_name?.[index] || entityID}
                            </span>
                          ))
                        )}
                      </td>
                      <td>
                        <Badge label={item.state} tone={questionStateTone(item.state)} />
                        {/* "Waiting on you" is the ledger's own judgement,
                            carried in the row rather than re-derived here
                            from a list of states this page would have to
                            keep in step with §4.8. */}
                        {item.pending && <span className="secondary"> waiting on you</span>}
                      </td>
                      <td><Badge label={item.class} tone={classTone(item.class)} /></td>
                      <td className="numeric mono">{item.answers}</td>
                      <td className="numeric mono">{item.plans}</td>
                      <td>
                        {created
                          ? <span title={created.absolute}>{created.relative}</span>
                          : <span className="muted">—</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </section>
  );
}

export default RealityQuestionsPage;
