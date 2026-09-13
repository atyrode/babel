import { Cluster, Stack, Switcher } from "@manifold/ui";
import { since, usd, type PolicyResult } from "./api.ts";

/*
  THE CEILINGS — what bounds Babel's autonomy, and how much of today it has spent.

  A ceiling nobody chose is not a ceiling of nothing: a policy with no ceilings reads as
  "unset", and the loop refuses to run under it, which is a different sentence from "zero
  dollars". Both appear here as themselves.

  This section is a READING. Changing a ceiling is `setPolicy`, an act with its own door and its
  own audit row, and the place for it is Settings rather than beside a launch button — an
  operator raising the day's ceiling is not starting a run, and a panel that let the two share a
  form would let a mis-click do both.
*/

export interface CeilingsProps {
  readonly policy: PolicyResult | null;
  readonly now: number;
  /** A failed read, in the door's own words. */
  readonly note: string;
}

function Figure({ label, value, note }: { readonly label: string; readonly value: string; readonly note: string }) {
  return (
    <Stack gap="var(--babel-space-1)" className="plugin-atyrode_babel_watch__stat">
      <span className="plugin-atyrode_babel_watch__stat-label">{label}</span>
      <span className="plugin-atyrode_babel_watch__stat-value">{value}</span>
      <span className="plugin-atyrode_babel_watch__muted">{note}</span>
    </Stack>
  );
}

export function Ceilings({ policy, now, note }: CeilingsProps) {
  if (policy === null) {
    return (
      <Stack gap="var(--babel-space-2)" className="plugin-atyrode_babel_watch__section">
        <h2 className="plugin-atyrode_babel_watch__title">Ceilings</h2>
        <p className="plugin-atyrode_babel_watch__note">
          {note === "" ? "Reading the policy in force…" : note}
        </p>
      </Stack>
    );
  }
  const { ceilings, spentTodayUsd } = policy;
  const unset = policy.version === "";
  const share = ceilings.perDayUsd > 0 ? Math.min(1, spentTodayUsd / ceilings.perDayUsd) : 0;
  return (
    <Stack gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__section">
      <Stack gap="var(--babel-space-1)">
        <h2 className="plugin-atyrode_babel_watch__title">Ceilings</h2>
        <p className="plugin-atyrode_babel_watch__lede">
          {unset
            ? "No policy has been recorded, so nothing bounds a run yet and the loop will refuse to start."
            : `Policy ${policy.version} recorded ${since(policy.recordedAt, now)}${policy.actorId === "" ? "" : ` by ${policy.actorId}`}.`}
        </p>
      </Stack>
      {note === "" ? null : <p className="plugin-atyrode_babel_watch__note">{note}</p>}
      <Switcher threshold="26rem" gap="var(--babel-space-4)">
        <Figure
          label="Per run"
          value={unset ? "unset" : usd(ceilings.perRunUsd)}
          note="What one run may spend before it stops."
        />
        <Figure
          label="Per day"
          value={unset ? "unset" : usd(ceilings.perDayUsd)}
          note={`${usd(spentTodayUsd)} spent since midnight UTC.`}
        />
        <Figure
          label="At once"
          value={unset ? "unset" : String(ceilings.concurrent)}
          note="How many reviews may be claimed together."
        />
      </Switcher>
      {unset ? null : (
        <div
          className="plugin-atyrode_babel_watch__spend"
          role="meter"
          aria-valuemin={0}
          aria-valuemax={ceilings.perDayUsd}
          aria-valuenow={spentTodayUsd}
          aria-label="Spent today against the day's ceiling"
        >
          <span
            className="plugin-atyrode_babel_watch__spend-fill"
            style={{ width: `${(share * 100).toFixed(1)}%` }}
          />
        </div>
      )}
      {policy.lanes.length === 0 ? null : (
        <Cluster gap="var(--babel-space-3)" className="plugin-atyrode_babel_watch__lanes">
          {policy.lanes.map((lane) => (
            <span key={`${lane.lane}:${lane.role}`} className="plugin-atyrode_babel_watch__lane">
              <span className="plugin-atyrode_babel_watch__mono">{Math.round(lane.share * 100)}%</span> {lane.lane}
              {lane.role === "" ? "" : ` · ${lane.role}`}
            </span>
          ))}
        </Cluster>
      )}
    </Stack>
  );
}
