import type { PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import type { MachineSummary } from "@manifold/protocol";
import { Stack } from "@manifold/ui";
import { useCallback, useEffect, useMemo, useState } from "react";
import { z } from "zod";
import {
  ACTIONS,
  LaunchResultSchema,
  PANELS,
  RunsResultSchema,
  TopicsResultSchema,
  WATCH_PLUGIN_ID,
} from "../contract.ts";
import {
  INITIAL_DRAFT,
  LaunchRequestSchema,
  PolicyResultSchema,
  RunsQuerySchema,
  StopInputSchema,
  act,
  launchInput,
  read,
  unready,
  type LaunchAnswer,
  type LaunchDraft,
  type PolicyResult,
  type RunsResult,
  type TopicsResult,
} from "./api.ts";
import { Ceilings } from "./ceilings.tsx";
import { Recipes } from "./recipes.tsx";
import { Runs } from "./runs.tsx";
import { Start } from "./start.tsx";

/*
  WATCH — the panel that helps the operator run Babel.

  Four sections in the order the questions are asked: what do I want to happen next, what is
  happening right now, what is Babel looking for, and what bounds it. There are no identifiers
  in the first one and no flags anywhere: a preset is a request, a knob is a number in the
  operator's units, and everything else on the screen is either something a run said or
  something the panel computed from what it said.

  Liveness is `usePolledResource`, one feed per resource, so two sections reading the same runs
  share one request; the elapsed clocks tick on the panel's own second while the runs feed polls
  every five. Nothing here holds a socket, and nothing polls while nothing is in flight.
*/

const RUNS_POLL_MS = 5_000;
const POLICY_POLL_MS = 60_000;
const TOPICS_POLL_MS = 60_000;
const MACHINES_POLL_MS = 30_000;
/** A dry preview is a read of the machine's own runtime report; it does not go stale quickly. */
const PREVIEW_POLL_MS = 120_000;
/** The clock the elapsed columns advance on. One second, because that is what "ticking" means. */
const TICK_MS = 1_000;
const RUNS_PAGE = 25;

const NO_RUNS: RunsResult = { runs: [], total: 0 };
const NO_TOPICS: TopicsResult = { topics: [], proposed: [], unfiled: 0 };
const NO_MACHINES: readonly MachineSummary[] = [];

/** A denial's sentence, or the error's, as the note under a section. */
function noteOf(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

export function Watch({ host }: PanelProps) {
  const [draft, setDraft] = useState<LaunchDraft>(INITIAL_DRAFT);
  const [limit, setLimit] = useState(RUNS_PAGE);
  const [now, setNow] = useState(() => Date.now());
  const [starting, setStarting] = useState(false);
  const [startNote, setStartNote] = useState("");
  const [stopping, setStopping] = useState("");
  /*
    TWO NOTES, NOT ONE. `readNote` is a failed read and is cleared by the next good answer;
    `stopNote` is what an act of the operator's said, and a successful refresh must not erase
    it — the refresh is the very thing his act asked for, and wiping the sentence on arrival is
    how "Asked run_7 to stop" disappeared half a second after he pressed the button.
  */
  const [runsNote, setRunsNote] = useState("");
  const [stopNote, setStopNote] = useState("");
  const [policyNote, setPolicyNote] = useState("");

  const runs = usePolledResource<RunsResult>(
    () => read(host, ACTIONS.runs, RunsQuerySchema.parse({ limit }), RunsResultSchema),
    RUNS_POLL_MS,
    {
      key: "atyrode.babel.runs",
      initial: NO_RUNS,
      restartKey: limit,
      onError: (reason) => setRunsNote(noteOf(reason)),
      onSuccess: () => setRunsNote(""),
    },
  );

  const policy = usePolledResource<PolicyResult | null>(
    () => read(host, ACTIONS.policy, {}, PolicyResultSchema),
    POLICY_POLL_MS,
    {
      key: "atyrode.babel.policy",
      initial: null,
      onError: (reason) => setPolicyNote(noteOf(reason)),
      onSuccess: () => setPolicyNote(""),
    },
  );

  const topics = usePolledResource<TopicsResult>(
    () => read(host, ACTIONS.topics, {}, TopicsResultSchema),
    TOPICS_POLL_MS,
    { key: "atyrode.babel.topics", initial: NO_TOPICS },
  );

  const machines = usePolledResource<readonly MachineSummary[]>(() => host.client.machines(), MACHINES_POLL_MS, {
    key: "atyrode.babel.machines",
    initial: NO_MACHINES,
  });

  /*
    WHAT WILL RUN is a resource keyed on the request itself: change the machine, the preset or a
    knob and the dry read happens again, because the profile, the cost and the ceilings are
    facts about THAT request on THAT machine. `blocked` is why there is nothing to ask yet, and
    it is also the sentence the card shows in the line's place.
  */
  const blocked = unready(draft);
  const request = useMemo(() => (blocked === "" ? launchInput(draft) : null), [blocked, draft]);
  const [previewNote, setPreviewNote] = useState("");
  const preview = usePolledResource<LaunchAnswer | null>(
    () =>
      request === null
        ? Promise.resolve(null)
        : read(host, ACTIONS.launch, LaunchRequestSchema.parse({ ...request, preview: true }), LaunchResultSchema),
    PREVIEW_POLL_MS,
    {
      key: "atyrode.babel.launch.preview",
      initial: null,
      enabled: request !== null,
      restartKey: request === null ? null : JSON.stringify(request),
      onError: (reason) => setPreviewNote(noteOf(reason)),
      onSuccess: () => setPreviewNote(""),
    },
  );

  /*
    The clock advances only while something is in flight. A panel that ticked over a page of
    receipts would be re-rendering a table of fixed numbers once a second forever.
  */
  const inFlight = runs.value.runs.some((run) => run.state === "queued" || run.state === "running");
  useEffect(() => {
    if (!inFlight) return;
    const timer = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(timer);
  }, [inFlight]);

  const onStart = useCallback(async () => {
    if (blocked !== "") return;
    setStarting(true);
    setStartNote("");
    const outcome = await act(host, ACTIONS.launch, launchInput(draft), LaunchResultSchema);
    setStarting(false);
    if (outcome.ok) {
      setStartNote(`Started ${outcome.value.kind} as ${outcome.value.runId}.`);
      setNow(Date.now());
      runs.refresh();
      return;
    }
    setStartNote(outcome.message);
  }, [blocked, draft, host, runs]);

  const onStop = useCallback(
    async (runId: string) => {
      setStopping(runId);
      const outcome = await act(host, ACTIONS.stop, StopInputSchema.parse({ runId }), z.unknown());
      setStopping("");
      setStopNote(outcome.ok ? `Asked ${runId} to stop; it stops at its next safe point.` : outcome.message);
      runs.refresh();
    },
    [host, runs],
  );

  return (
    <Stack gap="var(--babel-space-6)" className="plugin-atyrode_babel_watch">
      <Start
        draft={draft}
        machines={machines.value}
        topics={topics.value.topics}
        recipes={policy.value?.recipes ?? []}
        preview={preview.value}
        previewNote={blocked === "" ? previewNote : blocked}
        starting={starting}
        note={startNote}
        onDraft={setDraft}
        onStart={onStart}
      />
      <Runs
        runs={runs.value.runs}
        total={runs.value.total}
        now={now}
        stopping={stopping}
        note={runsNote === "" ? stopNote : runsNote}
        onStop={onStop}
        onMore={() => setLimit((current) => current + RUNS_PAGE)}
      />
      <Recipes recipes={policy.value?.recipes ?? []} now={now} note={policyNote} />
      <Ceilings policy={policy.value} now={now} note={policyNote} />
    </Stack>
  );
}

export default { id: WATCH_PLUGIN_ID, panels: { [PANELS.watch]: Watch } };
