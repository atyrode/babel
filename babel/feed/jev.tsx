import { useEffect, useRef, useState, useSyncExternalStore, type ReactElement } from "react";
import type { HostServices } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Cluster, Stack } from "@manifold/ui";
import {
  ACTIONS, JEV_ACTIONS, JEV_PLUGIN_ID, JEV_SWEEP_BATCH, SweepPlanSchema, SweptSchema,
  SuggestedSchema, door, type RecordPosition, type Swept,
} from "../contract.ts";
import { refusal } from "./api.ts";

// Readings belong to this browser connection, not to Babel's store or rankings. A reload loses
// them; delivered suggestions remain durable. Never share a reading across hub connections.
interface Readings {
  readonly positions: Map<string, RecordPosition>;
  readonly listeners: Set<() => void>;
  version: number;
}
const CONNECTIONS = new WeakMap<HostServices["client"], Readings>();
function readings(host: HostServices): Readings {
  let value = CONNECTIONS.get(host.client);
  if (value === undefined) {
    value = { positions: new Map(), listeners: new Set(), version: 0 };
    CONNECTIONS.set(host.client, value);
  }
  return value;
}
function notify(value: Readings): void {
  value.version += 1;
  for (const listener of value.listeners) listener();
}
function clear(value: Readings): void {
  if (value.positions.size === 0) return;
  value.positions.clear();
  notify(value);
}
function usePosition(host: HostServices, id: string): RecordPosition | undefined {
  const value = readings(host);
  useSyncExternalStore(
    (listener) => { value.listeners.add(listener); return () => value.listeners.delete(listener); },
    () => value.version,
  );
  return value.positions.get(id);
}

export function JevPosition({ host, id, detail = false }: {
  host: HostServices; id: string; detail?: boolean;
}): ReactElement | null {
  const position = usePosition(host, id);
  if (position === undefined || position.standing === "unjudged") return null;
  return (
    <div className="babel-jev-position" data-standing={position.standing}>
      <p>
        Jev: <strong>{position.standing}</strong>
        {position.tally !== null && <> · {position.up} back · {position.down} object</>}
        {position.heard < position.roster && <> · {position.heard}/{position.roster} heard</>}
      </p>
      {detail && (
        <dl className="babel-rows">
          <div><dt>Backed</dt><dd>{position.backed.join(", ") || "none"}</dd></div>
          <div><dt>Objected</dt><dd>{position.objected.join(", ") || "none"}</dd></div>
          <div><dt>Silent</dt><dd>{position.silent.join(", ") || "none"}</dd></div>
          {position.failed.length > 0 && <div><dt>Failed advisers</dt><dd>{position.failed.join(", ")}</dd></div>}
        </dl>
      )}
    </div>
  );
}

/** No read spends. Only the explicit sweep press invokes Jev; delivery is another explicit act. */
export function JevSweep({ host }: { host: HostServices }): ReactElement | null {
  const value = readings(host);
  const [running, setRunning] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [dry, setDry] = useState(false);
  const [message, setMessage] = useState("");
  const [proposals, setProposals] = useState<Swept["suggestions"]>([]);
  const after = useRef("");
  const basis = useRef("");
  const stop = useRef(false);
  const busy = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; stop.current = true; };
  }, []);
  const plan = usePolledResource(async () => {
    try {
      const result = await host.client.action(`${JEV_PLUGIN_ID}.${JEV_ACTIONS.sweepPlan}`, {});
      const parsed = result.ok ? SweepPlanSchema.safeParse(result.result) : null;
      if (parsed?.success && parsed.data.silent === "") {
        const nextBasis = parsed.data.kinds.map((kind) => kind.basis).join("/");
        if (basis.current !== nextBasis) {
          basis.current = nextBasis;
          after.current = "";
          clear(value);
        }
        return parsed.data;
      }
    } catch {
      // A missing/disabled optional part must not disturb the feed's own reading path.
    }
    clear(value);
    return null;
  }, 15_000, {
    key: "babel-jev-plan",
    initial: null,
    hold: () => busy.current,
  });

  async function run(): Promise<void> {
    if (busy.current || plan.value === null) return;
    busy.current = true;
    stop.current = false;
    setRunning(true);
    let remaining = plan.value.unjudged;
    let judged = 0;
    let unread = 0;
    try {
      while (remaining > 0 && !stop.current) {
        const previous = after.current;
        const result = await host.client.action(`${JEV_PLUGIN_ID}.${JEV_ACTIONS.sweep}`, {
          after: after.current, limit: Math.min(JEV_SWEEP_BATCH, remaining),
        });
        if (!result.ok) throw new Error(result.denial.message);
        const batch = SweptSchema.parse(result.result);
        if (!mounted.current) break;
        for (const position of batch.positions) value.positions.set(position.recordId, position);
        if (batch.positions.length > 0) notify(value);
        setProposals((held) => [...held, ...batch.suggestions]);
        judged += batch.judged;
        unread += batch.unjudged;
        remaining -= batch.read;
        if (batch.continuation !== "") after.current = batch.continuation;
        setMessage(`${judged} judged; ${unread} not judged. Suggestions await submission.`);
        if (batch.stopped !== "") {
          setMessage(`${judged} judged; ${unread} not judged. ${batch.stopped}`);
          if (batch.read === 0) after.current = "";
          if (batch.unjudged > 0 && batch.judged === 0) {
            clear(value);
            setDry(true);
          }
          break;
        }
        if (batch.read === 0 || after.current === previous) break;
      }
      if (remaining <= 0) after.current = "";
    } catch (error) {
      if (mounted.current) setMessage(refusal(error));
    } finally {
      busy.current = false;
      if (mounted.current) { setRunning(false); plan.refresh(); }
    }
  }

  async function submit(): Promise<void> {
    if (busy.current) return;
    busy.current = true;
    setSubmitting(true);
    let delivered = 0;
    try {
      for (const proposal of proposals) {
        if (!mounted.current) break;
        const { screener: _screener, ...input } = proposal;
        const answer = await host.client.action(door(ACTIONS.suggest), input);
        if (!answer.ok) throw new Error(answer.denial.message);
        SuggestedSchema.parse(answer.result);
        delivered += 1;
      }
      if (mounted.current) setMessage(`Submitted ${delivered} suggestions for your decision.`);
    } catch (error) {
      if (mounted.current) setMessage(`${delivered} submitted. ${refusal(error)}`);
    } finally {
      busy.current = false;
      if (mounted.current) {
        setProposals((held) => held.slice(delivered));
        setSubmitting(false);
        plan.refresh();
      }
    }
  }

  if (plan.value === null || dry) return null;
  return (
    <Stack className="babel-jev-sweep" gap="var(--babel-space-2)">
      <p>Jev can judge {plan.value.unjudged.toLocaleString()} pending records, including observations.</p>
      <p className="babel-note">
        Up to one paid call per record, in batches of {JEV_SWEEP_BATCH}. Readings last for this browser
        session; only submitted suggestions are saved. Babel’s ordering and your rulings do not change.
      </p>
      <Cluster gap="var(--babel-space-2)">
        {running ? (
          <button type="button" onClick={() => { stop.current = true; setMessage("Stopping after this batch."); }}>Stop sweep</button>
        ) : (
          <button type="button" disabled={submitting || proposals.length > 0 || plan.value.unjudged === 0} onClick={() => void run()}>
            Judge pending corpus
          </button>
        )}
        {proposals.length > 0 && (
          <button type="button" disabled={running || submitting} onClick={() => void submit()}>
            Submit {proposals.length} suggestions
          </button>
        )}
        {proposals.length > 0 && (
          <button type="button" disabled={running || submitting} onClick={() => setProposals([])}>
            Discard unsent preview
          </button>
        )}
      </Cluster>
      {message !== "" && <p role="status">{message}</p>}
    </Stack>
  );
}
