import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS, RUN_STAGES, door } from "../contract.ts";
import {
  FRESHNESS_NOTE,
  RUN_KIND_LABELS,
  STAGE_NOTE,
  STALLED_NOTE,
  ageClause,
  elapsedClock,
  elapsedSince,
  figure,
  since,
  tokenClause,
  usd,
  type RunRow,
} from "./api.ts";

/*
  THE RUNS — what is happening right now, and what this deployment has produced.

  Two tables, one register. The live one is the observatory: mono figures, one row per run, a
  dot where the hub has heard from it lately, an elapsed clock that advances between polls
  because it is computed from the start instant against the panel's own clock rather than from a
  number the server sent. The ended one is the ledger: what it cost, what it wrote, how it
  closed.

  Freshness is a claim about a heartbeat and never about a process. `lost` means nothing has
  been heard for a long time — which is not a death, and the row says so in its title rather
  than painting a red light over an unknown. The port keeps that honesty from the standalone
  page, where it was the whole point of the fleet view.
*/

const IN_FLIGHT: Record<string, boolean> = { queued: true, running: true };

export interface RunsProps {
  readonly runs: readonly RunRow[];
  readonly total: number;
  /** The panel's clock, ticked once a second while anything is in flight. */
  readonly now: number;
  /** The run whose stop is in flight, or empty. */
  readonly stopping: string;
  /** A failed read or a refused stop, in the door's own words. */
  readonly note: string;
  /** The whole row, because a stop is authorized at the run's own job node, not at its id. */
  readonly onStop: (run: RunRow) => void;
  readonly onMore: () => void;
}

function LiveTable({
  runs,
  now,
  stopping,
  onStop,
}: {
  readonly runs: readonly RunRow[];
  readonly now: number;
  readonly stopping: string;
  readonly onStop: (run: RunRow) => void;
}) {
  return (
    <div className="plugin-atyrode_babel_watch__table-scroll">
      <table className="plugin-atyrode_babel_watch__table">
        <thead>
          <tr>
            <th>Run</th>
            <th>Stage</th>
            <th className="plugin-atyrode_babel_watch__numeric">Elapsed</th>
            <th className="plugin-atyrode_babel_watch__numeric">Calls</th>
            <th className="plugin-atyrode_babel_watch__numeric">In / out / cache</th>
            <th className="plugin-atyrode_babel_watch__numeric">Spent</th>
            <th>Model</th>
            <th className="plugin-atyrode_babel_watch__numeric">Records</th>
            <th>Last word</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => {
            const seconds = elapsedSince(run.startedAt, now);
            const heard = run.freshness === "fresh" || run.freshness === "recent";
            // The stage's own clock, ticking against the panel's: "at the model" is a claim
            // about a phase, and "at the model since 11m 40s" is the one an operator acts on.
            const progress = run.progress;
            const inStage = progress === null ? null : elapsedSince(progress.since, now);
            return (
              <tr key={run.id} className="plugin-atyrode_babel_watch__live-row">
                <td>
                  <Cluster gap="var(--babel-space-2)">
                    {heard ? <span className="plugin-atyrode_babel_watch__dot" aria-hidden="true" /> : null}
                    <span className="plugin-atyrode_babel_watch__mono">{run.id}</span>
                    <span className="plugin-atyrode_babel_watch__muted">
                      {RUN_KIND_LABELS[run.kind] ?? run.kind}
                    </span>
                  </Cluster>
                </td>
                <td className="plugin-atyrode_babel_watch__stage">
                  {progress === null ? (
                    <span className="plugin-atyrode_babel_watch__muted">no word yet</span>
                  ) : (
                    <Cluster gap="var(--babel-space-2)">
                      <span title={STAGE_NOTE[progress.stage] ?? ""}>{progress.stage}</span>
                      {inStage === null ? null : (
                        <span className="plugin-atyrode_babel_watch__muted plugin-atyrode_babel_watch__stage-since">
                          {elapsedClock(inStage)}
                        </span>
                      )}
                      {progress.stalled ? (
                        <span className="plugin-atyrode_babel_watch__stalled" title={STALLED_NOTE}>
                          stalled
                        </span>
                      ) : null}
                    </Cluster>
                  )}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__elapsed">
                  {seconds === null ? "—" : elapsedClock(seconds)}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {progress === null ? "—" : figure(progress.calls)}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {progress === null ? "—" : tokenClause(progress)}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {progress === null ? "—" : usd(progress.costUsd)}
                </td>
                <td className="plugin-atyrode_babel_watch__mono">
                  {progress === null || progress.lastModel === "" ? "—" : progress.lastModel}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric">{figure(run.records)}</td>
                <td
                  className="plugin-atyrode_babel_watch__mono plugin-atyrode_babel_watch__last-word"
                  title={FRESHNESS_NOTE[run.freshness] ?? ""}
                >
                  {ageClause(run.lastWord, now)}
                </td>
                <td>
                  <button
                    type="button"
                    className="plugin-atyrode_babel_watch__quiet"
                    data-action={door(ACTIONS.stop)}
                    disabled={stopping === run.id}
                    onClick={() => onStop(run)}
                  >
                    {stopping === run.id ? "Stopping…" : "Stop"}
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function EndedTable({ runs, now }: { readonly runs: readonly RunRow[]; readonly now: number }) {
  return (
    <div className="plugin-atyrode_babel_watch__table-scroll">
      <table className="plugin-atyrode_babel_watch__table">
        <thead>
          <tr>
            <th>Run</th>
            <th>Recipe</th>
            <th>Ended</th>
            <th className="plugin-atyrode_babel_watch__numeric">Took</th>
            <th className="plugin-atyrode_babel_watch__numeric">Records</th>
            <th className="plugin-atyrode_babel_watch__numeric">Tokens</th>
            <th className="plugin-atyrode_babel_watch__numeric">Spend</th>
            <th>Closed</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => {
            const started = Date.parse(run.startedAt);
            const finished = Date.parse(run.finishedAt);
            const took = Number.isFinite(started) && Number.isFinite(finished) ? (finished - started) / 1000 : null;
            return (
              <tr key={run.id}>
                <td>
                  <Cluster gap="var(--babel-space-2)">
                    <span className="plugin-atyrode_babel_watch__mono">{run.id}</span>
                    <span className="plugin-atyrode_babel_watch__muted">
                      {RUN_KIND_LABELS[run.kind] ?? run.kind}
                    </span>
                  </Cluster>
                </td>
                <td className="plugin-atyrode_babel_watch__mono">{run.recipe === "" ? "—" : run.recipe}</td>
                <td className="plugin-atyrode_babel_watch__mono">{since(run.finishedAt, now)}</td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {took === null ? "—" : elapsedClock(took)}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {figure(run.records)}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {run.tokens === null ? "—" : figure(run.tokens)}
                </td>
                <td className="plugin-atyrode_babel_watch__numeric plugin-atyrode_babel_watch__mono">
                  {run.costUsd === null ? "—" : usd(run.costUsd)}
                </td>
                <td>
                  <span className={`plugin-atyrode_babel_watch__closed is-${run.state}`}>{run.state}</span>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/**
 * The one sentence above the tables. Before #261 it counted rows and heartbeats, which is what
 * the 2026-09-13 drain had: twenty-six jobs "in flight", every one of them reading a corpus, no
 * engine anywhere, and a header that said they were fine. So it says how many have reached a
 * model and how many said they had and went quiet, and nothing else changes.
 */
function lede(live: readonly RunRow[]): string {
  if (live.length === 0) return "Nothing running. Every row below is a receipt.";
  const atModel = live.filter((run) => run.progress?.stage === RUN_STAGES.atModel).length;
  const stalled = live.filter((run) => run.progress?.stalled === true).length;
  const heardFrom = live.filter((run) => run.freshness === "fresh" || run.freshness === "recent").length;
  const clauses = [`${live.length} in flight`, `${atModel} at the model`];
  if (stalled > 0) clauses.push(`${stalled} stalled`);
  if (heardFrom !== live.length) clauses.push(`${heardFrom} heard from lately`);
  return `${clauses.join(", ")}.`;
}

export function Runs({ runs, total, now, stopping, note, onStop, onMore }: RunsProps) {
  const live = runs.filter((run) => IN_FLIGHT[run.state] === true);
  const ended = runs.filter((run) => IN_FLIGHT[run.state] !== true);
  return (
    <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__section">
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Runs</h2>
        <p className="plugin-atyrode_babel_watch__lede">{lede(live)}</p>
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      {live.length === 0 ? null : <LiveTable runs={live} now={now} stopping={stopping} onStop={onStop} />}
      {ended.length === 0 ? (
        <p className="plugin-atyrode_babel_watch__muted">No run has finished here yet.</p>
      ) : (
        <Stack gap="var(--babel-space-2)">
          {/* Two tables of six columns stack into one wall unless the second one says what it is. */}
          {live.length === 0 ? null : <span className="plugin-atyrode_babel_watch__stat-label">Receipts</span>}
          <EndedTable runs={ended} now={now} />
        </Stack>
      )}
      {total > runs.length ? (
        <Cluster gap="var(--babel-space-2)">
          <span className="plugin-atyrode_babel_watch__muted">
            {figure(total - runs.length)} older {total - runs.length === 1 ? "run" : "runs"} in the store.
          </span>
          <button type="button" className="plugin-atyrode_babel_watch__quiet" onClick={onMore}>
            Read more
          </button>
        </Cluster>
      ) : null}
    </Stack>
  );
}
