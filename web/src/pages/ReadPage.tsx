import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import {
  getEvaluationList,
  type EvaluationCoverageCounts,
  type EvaluationItem,
  type EvaluationListResponse,
  type EvaluationReception,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge } from "../analysis";
import {
  coverageLabel,
  CoverageSummary,
  kindLabel,
  laneLabel,
  laneTone,
  Reception,
  roleBasis,
  roleLabel,
  sortBasis,
  sortLabel,
  StaleNotice,
} from "../evaluation";
import "../read.css";

// Read answers one question: what has Babel found?
//
// It replaces four listings — findings, proposals, the hypothesis frontier and
// the evaluation backlog — that differed in which subset of one corpus they
// drew and in nothing else a reader cares about. Four destinations for one
// question is how the reader ended up needing to know the record kinds before
// he could look at the output; the kinds are a filter on one list now, which
// is what they always were.
//
// Every filter and every ordering those four pages offered survives as a
// control here, but not as a query form. Five dropdowns is a database client:
// the first thing an operator wanted was a Proposals chip, and a dropdown
// hides both the vocabulary and the fact that anything is selected. So kind
// and standing are chips — the choice and the options are the same pixels —
// the ordering is a menu that names what each order is computed from, and the
// two facets nobody starts from, coverage and review role, are behind a
// disclosure that says when one of them is on.
//
// The frontier's exploration statuses — untriaged, queued, investigating,
// promoted — are absent, and their absence is deliberate: they are the
// pipeline's own bookkeeping about a candidate, not a standing anybody rules
// on, and the one that matters to a reader rides each hypothesis row as a
// fact. What a reader filters by is the standing.
//
// Paging is the server's, and the snapshot is pinned in the URL, because §8.5
// requires pagination to stay consistent while publication continues. Without
// the pin, page two is cut from a differently ordered set and rows silently
// move between pages. Every control writes the URL for the ordinary reason:
// a filtered list is a thing an operator reloads, shares and walks back
// through with the browser's own Back button.

const PAGE_SIZE = 25;

// Plurals for the summary line. They are written down rather than derived
// because English does not derive them — a hypothesis does not take an s —
// and the singular labels are the server's vocabulary rather than this page's.
// A kind this build has no plural for reads as its own label, which is wrong
// English and still the right value.
const KIND_PLURALS: Record<string, string> = {
  proposal: "proposals",
  hypothesis: "hypotheses",
  observation: "observations",
  finding: "findings",
  evaluation: "evaluations",
};

// What the list is made of, beside how many rows it has.
//
// It is a separate read per kind rather than a field of the listing, because
// the listing answers "how many match this query" and this line answers "what
// is in the corpus" — and an operator looking at 90 proposals wants to know
// there are 5,544 records behind them. The reads are `limit=1` counts and they
// depend only on the facets other than kind, so flipping a kind chip — the
// thing an operator does most here — costs nothing at all.
//
// A count that did not answer is absent from the line rather than zero.
interface Breakdown {
  facets: string;
  total: number | null;
  byKind: Record<string, number>;
}

function ReadPage() {
  const [params, setParams] = useSearchParams();
  const { pathname } = useLocation();
  const [data, setData] = useState<EvaluationListResponse | null>(null);
  const [breakdown, setBreakdown] = useState<Breakdown | null>(null);
  const [ordering, setOrdering] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const sort = params.get("sort") ?? "";
  const lane = params.get("lane") ?? "";
  const kind = params.get("kind") ?? "";
  const coverage = params.get("coverage") ?? "";
  const role = params.get("role") ?? "";
  const snapshot = params.get("snapshot") ?? "";
  const offset = Math.max(0, Number(params.get("offset") ?? 0) || 0);

  // fetched is the query the rows on screen came from. It exists because
  // pinning the snapshot below rewrites the URL, and without it that rewrite
  // would re-request the identical page: the server answered from a snapshot,
  // the client wrote that snapshot into the query, and the query changed. The
  // second request returned the same rows and replaced every one of them,
  // which cost a round trip and detached whatever the operator was about to
  // click.
  const fetched = useRef("");
  const requested = JSON.stringify({ sort, lane, kind, coverage, role, snapshot, offset });

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getEvaluationList({ sort, lane, kind, coverage, role, snapshot, limit: PAGE_SIZE, offset })
      .then((answer) => {
        fetched.current = JSON.stringify({
          sort, lane, kind, coverage, role, snapshot: answer.snapshot || snapshot, offset,
        });
        setData(answer);
      })
      .catch((reason) => setError(errorMessage(reason)))
      .finally(() => setLoading(false));
  }, [sort, lane, kind, coverage, role, snapshot, offset]);

  useEffect(() => {
    if (fetched.current === requested) return;
    load();
  }, [load, requested]);

  // The first answer pins the ordering the rest of the paging walks. It is a
  // replacing navigation: pinning is not something the operator did, so it
  // must not cost him a Back press to undo.
  //
  // Both path guards are load-bearing rather than defensive. The pin writes a
  // URL built from this page's own captured query, and `replace` overwrites
  // whatever entry is current — so if the answer lands in the same tick as a
  // click into a record, the pin replaces the record's URL with the listing's
  // and the reader is silently returned to the list he just left. Measured:
  // walking /findings/:id straight after /findings reproduced it every time.
  //
  // The router's pathname is the route this render was built from, and the
  // second check is the route the browser is on at the instant the pin runs.
  // They differ for one batch when the answer and the click land together:
  // React has re-rendered on the new data but not yet on the new location, so
  // the first guard still reads "/read" while the address bar already holds
  // the record. Measured too, as a browser suite failure that only appeared
  // once the listing made a second read and the timing shifted.
  useEffect(() => {
    if (pathname !== "/read") return;
    if (!data || snapshot || !data.snapshot) return;
    if (window.location.hash.replace(/^#/u, "").split("?")[0] !== "/read") return;
    const next = new URLSearchParams(params);
    next.set("snapshot", data.snapshot);
    setParams(next, { replace: true });
  }, [data, snapshot, params, setParams, pathname]);

  const vocabulary = data?.vocabulary;
  const kinds = vocabulary?.kinds;
  const facets = `${lane}|${coverage}|${role}`;

  // The breakdown is keyed by the facets it was counted under, so a kind chip
  // never invalidates it: the numbers on the summary line are what the corpus
  // holds under the standing and coverage in force, and the kind chips choose
  // between them.
  useEffect(() => {
    if (!kinds || kinds.length === 0) return;
    if (breakdown?.facets === facets) return;
    let live = true;
    Promise.allSettled([
      getEvaluationList({ lane, coverage, role, limit: 1 }),
      ...kinds.map((name) => getEvaluationList({ kind: name, lane, coverage, role, limit: 1 })),
    ]).then((answers) => {
      if (!live) return;
      const byKind: Record<string, number> = {};
      answers.slice(1).forEach((answer, index) => {
        const name = kinds[index];
        if (answer.status === "fulfilled") byKind[name] = answer.value.total;
      });
      const all = answers[0];
      setBreakdown({
        facets,
        total: all.status === "fulfilled" ? all.value.total : null,
        byKind,
      });
    });
    return () => {
      live = false;
    };
  }, [breakdown, coverage, facets, kinds, lane, role]);

  // select changes one facet and starts the ordering again from its first
  // page. The snapshot is dropped with it: a snapshot identifies one ranked
  // set, and carrying it onto a different filter would page through an
  // ordering that no longer exists.
  function select(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    next.delete("offset");
    next.delete("snapshot");
    setParams(next);
  }

  function page(nextOffset: number) {
    const next = new URLSearchParams(params);
    if (nextOffset > 0) next.set("offset", String(nextOffset));
    else next.delete("offset");
    setParams(next);
  }

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const shownSort = data?.query.sort || "recommended";
  const basis = sortBasis(shownSort);

  // Proposals first. The rest keep the order internal/evaluation defines,
  // which is the corpus's own order of development; `sort` is stable, so
  // hoisting one name leaves the others alone.
  const kindOrder = useMemo(
    () =>
      [...(kinds ?? [])].sort(
        (left, right) => Number(right === "proposal") - Number(left === "proposal"),
      ),
    [kinds],
  );

  // Records on this page where reviewers said both things. It is counted here
  // and said to be counted here, because the listing carries no contested
  // total: the page's own rows are what this client can honestly add up.
  const contested = items.filter(
    (item) => item.reception.support > 0 && item.reception.oppose > 0,
  ).length;

  const hidden = [
    coverage ? coverageLabel(coverage) : "",
    role ? roleLabel(role) : "",
  ].filter(Boolean);

  return (
    <section className="page read-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Output</p>
          <h1>What has Babel found?</h1>
        </div>
      </div>

      {vocabulary && (
        <div className="read-controls">
          <div className="read-chips" role="group" aria-label="Kind">
            <button
              type="button"
              className={kind === "" ? "chip active" : "chip"}
              aria-pressed={kind === ""}
              onClick={() => select("kind", "")}
            >
              Everything
            </button>
            {kindOrder.map((name) => (
              <button
                type="button"
                key={name}
                data-chip={`kind-${name}`}
                className={kind === name ? "chip active" : "chip"}
                aria-pressed={kind === name}
                onClick={() => select("kind", kind === name ? "" : name)}
              >
                {kindLabel(name)}
              </button>
            ))}
          </div>

          <div className="read-chips" role="group" aria-label="Standing">
            <button
              type="button"
              className={lane === "" ? "chip active" : "chip"}
              aria-pressed={lane === ""}
              onClick={() => select("lane", "")}
            >
              Any standing
            </button>
            {vocabulary.lanes.map((name) => (
              <button
                type="button"
                key={name}
                data-chip={`lane-${name}`}
                className={lane === name ? "chip active" : "chip"}
                aria-pressed={lane === name}
                title={laneLabel(name)}
                onClick={() => select("lane", lane === name ? "" : name)}
              >
                {shortLane(name)}
              </button>
            ))}
          </div>

          <div className="read-trailing">
            {/* The ordering, and what each ordering is computed from. §8.5
                requires every order to name its basis; a menu can carry the
                sentence beside the choice, which is the one thing five
                dropdowns could not do. */}
            <details
              className="read-order"
              open={ordering}
              onToggle={(event) => setOrdering(event.currentTarget.open)}
            >
              <summary data-control="sort">Order: {sortLabel(shownSort)}</summary>
              <div className="surface read-menu">
                {vocabulary.sorts.map((name) => (
                  <button
                    type="button"
                    key={name}
                    data-order={name}
                    className={name === shownSort ? "active" : undefined}
                    aria-pressed={name === shownSort}
                    onClick={() => {
                      select("sort", name === "recommended" ? "" : name);
                      setOrdering(false);
                    }}
                  >
                    <strong>{sortLabel(name)}</strong>
                    <span>{sortBasis(name)}</span>
                  </button>
                ))}
              </div>
            </details>

            {/* Coverage and review role. They are folded because nobody starts
                a reading from them, and the fold names what is on: a hidden
                filter an operator forgot about is a list he cannot explain. */}
            <details className="read-more">
              <summary>
                More
                {hidden.length > 0 && <span className="read-on">{hidden.join(" · ")}</span>}
              </summary>
              <div className="read-more-body">
                <div className="read-chips" role="group" aria-label="Reviewed">
                  <span className="read-chips-label">Reviewed</span>
                  <button
                    type="button"
                    className={coverage === "" ? "chip active" : "chip"}
                    aria-pressed={coverage === ""}
                    onClick={() => select("coverage", "")}
                  >
                    Any
                  </button>
                  {vocabulary.coverage.map((name) => (
                    <button
                      type="button"
                      key={name}
                      className={coverage === name ? "chip active" : "chip"}
                      aria-pressed={coverage === name}
                      onClick={() => select("coverage", coverage === name ? "" : name)}
                    >
                      {coverageLabel(name)}
                    </button>
                  ))}
                </div>
                <div className="read-chips" role="group" aria-label="By role">
                  <span className="read-chips-label">By role</span>
                  <button
                    type="button"
                    className={role === "" ? "chip active" : "chip"}
                    aria-pressed={role === ""}
                    onClick={() => select("role", "")}
                  >
                    Every role
                  </button>
                  {vocabulary.roles.map((name) => (
                    <button
                      type="button"
                      key={name}
                      className={role === name ? "chip active" : "chip"}
                      aria-pressed={role === name}
                      title={roleBasis(name)}
                      onClick={() => select("role", role === name ? "" : name)}
                    >
                      {roleLabel(name)}
                    </button>
                  ))}
                </div>
              </div>
            </details>
          </div>
        </div>
      )}

      {data && (
        <p className="read-summary">
          <span className="read-figure">
            <strong>{(breakdown?.total ?? total).toLocaleString()}</strong> records
          </span>
          {kindOrder.map((name) => {
            const count = breakdown?.byKind[name];
            // A kind the corpus holds none of is not a fact about the corpus
            // worth a figure: "0 evaluations" beside four real counts reads
            // as a measurement of an absence nobody took. A count that did
            // not answer is absent for the same reason and not the same one.
            if (!count) return null;
            const word =
              count === 1
                ? kindLabel(name).toLocaleLowerCase()
                : (KIND_PLURALS[name] ?? kindLabel(name).toLocaleLowerCase());
            return (
              <span className="read-figure" key={name}>
                <strong>{count.toLocaleString()}</strong> {word}
              </span>
            );
          })}
          {contested > 0 && (
            <span
              className="read-figure"
              title="Records on this page where reviewers recorded both support and opposition."
            >
              <strong>{contested.toLocaleString()}</strong> contested here
            </span>
          )}
          {kind || lane || coverage || role ? (
            <span className="read-figure read-matching">
              <strong>{total.toLocaleString()}</strong> match this view
            </span>
          ) : null}
        </p>
      )}

      {basis && <p className="read-basis">{basis}</p>}

      {data && <StaleNotice stale={data.stale} unavailable={data.unavailable} />}

      {loading && !data && (
        <div className="surface state-note"><span className="spinner" /> Reading the output…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>The output could not be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && items.length === 0 && (lane || kind || coverage || role ? (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing matches this view</strong>
          <span>
            That is a statement about the filters, not about what Babel has found.{" "}
            <button type="button" className="link-button" onClick={() => setParams(new URLSearchParams())}>
              Clear them
            </button>
            .
          </span>
        </div>
      ) : (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Babel has not found anything yet</strong>
          <span>
            Output appears here as exploration records it. Nothing is running until a run is
            started under <Link to="/watch">Watch</Link>.
          </span>
        </div>
      ))}

      {items.length > 0 && (
        <ol className="read-list">
          {items.map((item) => (
            <OutputRow item={item} key={`${item.artifact.subject.kind}-${item.artifact.subject.id}`} />
          ))}
        </ol>
      )}

      {data && (total > PAGE_SIZE || offset > 0) && (
        <div className="pager surface">
          <button type="button" disabled={offset === 0} onClick={() => page(Math.max(0, offset - PAGE_SIZE))}>
            ← Previous
          </button>
          <span className="muted">
            {(offset + 1).toLocaleString()}–{Math.min(offset + items.length, total).toLocaleString()} of{" "}
            {total.toLocaleString()}
          </span>
          <button
            type="button"
            disabled={offset + items.length >= total}
            onClick={() => page(offset + PAGE_SIZE)}
          >
            Next →
          </button>
        </div>
      )}

      {/* §8.5's coverage inventory. It was a page of its own and is a peel
          here, because "what has nobody read" is a question about this list
          rather than a destination beside it — and because the counts arrive
          with every page of the listing, so opening it costs no request. */}
      {data && (
        <details className="peel coverage-peel">
          <summary>
            What has nobody read yet?
            <span className="peel-count">
              {data.coverage.unreviewed.toLocaleString()} unreviewed
            </span>
          </summary>
          <div className="peel-body">
            <CoverageSummary coverage={data.coverage} />
            {vocabulary && (
              <RoleTotals roles={vocabulary.roles} byRole={data.coverage.by_role} />
            )}
            <p className="muted">
              What analysis is allowed to spend reviewing this is in{" "}
              <Link to="/settings?section=policy">Settings</Link>.
            </p>
          </div>
        </details>
      )}
    </section>
  );
}

// A standing in chip length. The full sentence a lane carries — "Accepted —
// awaiting implementation" — is a caption and not a control; it stays on the
// chip's title, where an operator who wants the consequence can read it.
function shortLane(name: string): string {
  const head = laneLabel(name).split(" — ")[0];
  if (head.length <= 16) return head;
  const words = name.replace(/[-_]/g, " ");
  return words.charAt(0).toLocaleUpperCase() + words.slice(1);
}

// One row: the record's own claim, three facts, and the shape of its
// reception.
//
// The three facts are what decide whether to open it — what kind of thing it
// is, where it stands, and who has said anything about it. The score it was
// ranked by is deliberately not among them: a decimal beside a vote count
// reads as a measurement of the idea, when it is a position in one ordering
// under one recorded policy. Everything else this record holds is a peel down
// on its own page, which is one click away and never four.
//
// A record nobody has reviewed carries nothing at all in the reception slot.
// The words "no reviews yet" were on every row of a corpus that is mostly
// unreviewed — twenty-five rows of the same sentence, which is a fact about
// the review budget and not about any row on the page. The absence is still
// stated where it is the question being asked: the coverage filter, the peel
// below the list, and the record's own page all name it. A skip is not a
// review and not a vote, so it is the one thing here that prints without one:
// somebody tried and could not.
function OutputRow({ item }: { item: EvaluationItem }) {
  const created = formatTime(item.artifact.created_at);
  const { reviews, skips } = item.reception;
  return (
    <li className="read-row" data-item={item.artifact.subject.id}>
      <Link
        className="read-claim untrusted-inline"
        to={`/r/${encodeURIComponent(item.artifact.subject.id)}`}
        title={created ? `Recorded ${created.absolute}` : undefined}
      >
        {item.artifact.title || "a record with no title recorded"}
      </Link>
      <span className="read-facts">
        <Badge label={kindLabel(item.artifact.subject.kind)} tone="neutral" />
        <Badge label={laneLabel(item.lane)} tone={laneTone(item.lane)} />
        {reviews > 0 && (
          <>
            <ReceptionSpark reception={item.reception} />
            <Reception reception={item.reception} />
          </>
        )}
        {reviews === 0 && skips > 0 && (
          <span
            className="read-skipped"
            title="Reviews that could not be completed. A skip is not a vote and is not a review."
          >
            {skips} skipped
          </span>
        )}
      </span>
    </li>
  );
}

// The shape of one row's reception, in about twenty pixels.
//
// Three bars, one per thing a reviewer can say, drawn against the largest of
// them. It is not a time series and it is not a percentage, and both absences
// are deliberate: the listing carries the totals and no history of them, so a
// line with a slope would be a trend this client invented, and a normalized
// bar would report one supporting vote as unanimity. A vote nobody cast is a
// baseline tick rather than nothing, so absence reads as absence rather than
// as a missing bar.
//
// A record nobody has reviewed gets no chart at all. §4.12's rule holds here
// as it does in `Reception`: three zeroes would read as unopposed.
function ReceptionSpark({ reception }: { reception: EvaluationReception }) {
  if (reception.reviews === 0) return null;
  const votes: Array<[string, number]> = [
    ["support", reception.support],
    ["oppose", reception.oppose],
    ["unsure", reception.unsure],
  ];
  const most = Math.max(...votes.map(([, count]) => count));
  if (most === 0) return null;
  return (
    <span
      className="spark read-spark"
      title={`${reception.support} support · ${reception.oppose} oppose · ${reception.unsure} unsure, against the largest of the three`}
    >
      <svg viewBox="0 0 100 24" preserveAspectRatio="none" aria-hidden="true">
        {votes.map(([name, count], index) => {
          const height = Math.max(2, (count / most) * 24);
          return (
            <rect
              key={name}
              className={`read-spark-${name}`}
              x={index * 37}
              y={24 - height}
              width={26}
              height={height}
            />
          );
        })}
      </svg>
    </span>
  );
}

// The deployment-wide inventory, role by role.
//
// Coverage is role-specific because §4.12 makes it so: a reception vote says
// how an idea was received and discharges no evidence check, and an outcome
// verification is owed only where something was accepted. One number for six
// different questions would report a satisfied obligation nobody met.
//
// A role the projection reported nothing about is unknown rather than zero.
// Printing zeroes for it would report an obligation nobody measured as one
// nobody owes.
function RoleTotals({
  roles,
  byRole,
}: {
  roles: string[];
  byRole: Record<string, EvaluationCoverageCounts> | null;
}) {
  return (
    <table className="role-totals">
      <thead>
        <tr>
          <th>Role</th>
          <th className="numeric">Never reviewed</th>
          <th className="numeric">Due</th>
          <th className="numeric">Overdue</th>
          <th className="numeric">Blocked</th>
          <th className="numeric">No evaluator</th>
          <th className="numeric">Reviewed</th>
        </tr>
      </thead>
      <tbody>
        {roles.map((name) => {
          const counts = byRole?.[name];
          return (
            <tr key={name}>
              <td>
                <strong>{roleLabel(name)}</strong>
                <span className="secondary">{roleBasis(name)}</span>
              </td>
              {counts ? (
                <>
                  <td className="numeric mono">{counts.unreviewed}</td>
                  <td className="numeric mono">{counts.due}</td>
                  <td className="numeric mono">{counts.overdue}</td>
                  <td className="numeric mono">{counts.blocked}</td>
                  <td className="numeric mono">{counts.unsupported}</td>
                  <td className="numeric mono">{counts.reviewed}</td>
                </>
              ) : (
                <td className="muted" colSpan={6}>not reported by this projection</td>
              )}
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export default ReadPage;
