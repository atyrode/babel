import { Cluster, Stack } from "@manifold/ui";
import { GAP_NOTE, HEALTHY_STOP, STOP_NOTE, figure, since, type CycleReport } from "./api.ts";

/*
  WHY NOTHING RAN (#328).

  The conductor has always known why a cycle spent nothing — it stops on one reason and declines
  each candidate for another — and every word of it went to the hub's log and nowhere else. So a
  deployment where nothing happens looked, from this panel, exactly like a deployment that is
  broken: an empty runs table and no sentence anywhere. The operator's recourse was to read a
  journal, which is the recourse a control room exists to remove.

  A CYCLE THAT SPENT NORMALLY SAYS NOTHING HERE. The whole value of the section is that its
  presence means something, and a heading that appears on every healthy cycle to report "no
  problems" is the same absence of information as the silence it replaced, only louder. So the
  section renders when the loop stopped for a reason that is not `batch` — the one stop that IS
  the loop working — or when it declined at least one candidate, and otherwise renders nothing.

  THE GAPS ARE COUNTED AND NOT LISTED (`server/conductor.ts`'s `keepCycle`). A cycle contending
  with a second conductor declines every candidate it looks at, and four hundred rows saying
  `claimed` is a worse answer than one row saying so four hundred times. The first instance of
  each reason comes with the count, because the count says how much and the record says where.
*/

export interface CycleProps {
  /** The last cycle as the pulse door answers for it; null until one has run. */
  readonly cycle: CycleReport | null;
  readonly now: number;
  /** A failed read, in the door's own words. */
  readonly note: string;
}

export function Cycle({ cycle, now, note }: CycleProps) {
  const stop = cycle?.stop ?? null;
  const gaps = cycle?.gaps ?? [];
  const spoke = (stop !== null && stop.reason !== HEALTHY_STOP) || gaps.length > 0;
  // A panel that cannot read the pulse must say so rather than quietly explaining nothing: the
  // failed read is the one thing shown here that is not about the loop.
  if (!spoke && note === "") return null;
  return (
    <Stack
      gap="var(--babel-space-3)"
      className="plugin-atyrode_babel_watch__section plugin-atyrode_babel_watch__cycle"
    >
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Last cycle</h2>
        {cycle === null ? null : (
          <p className="plugin-atyrode_babel_watch__lede">
            {stop === null ? "It drew nothing and recorded no stop." : STOP_NOTE[stop.reason]}
          </p>
        )}
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      {cycle === null ? null : (
        <p className="plugin-atyrode_babel_watch__mono plugin-atyrode_babel_watch__muted">
          {since(cycle.at, now)}
          {stop === null || stop.detail === "" ? "" : ` · ${stop.detail}`}
        </p>
      )}
      {gaps.length === 0 ? null : (
        <ul className="plugin-atyrode_babel_watch__gaps">
          {gaps.map((gap) => (
            <li key={gap.reason} className="plugin-atyrode_babel_watch__gap">
              <Cluster gap="var(--babel-space-2)">
                <span className="plugin-atyrode_babel_watch__mono">
                  {figure(gap.count)} × {gap.reason}
                </span>
                <span>{GAP_NOTE[gap.reason]}</span>
              </Cluster>
              <p className="plugin-atyrode_babel_watch__muted">
                {gap.recordId === "" ? gap.detail : `${gap.recordId} — ${gap.detail}`}
              </p>
            </li>
          ))}
        </ul>
      )}
    </Stack>
  );
}
