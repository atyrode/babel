import type { PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import type { MachineSummary } from "@manifold/protocol";
import { Stack } from "@manifold/ui";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { z } from "zod";
import {
  ACTIONS,
  AccountsQuerySchema,
  AccountsResultSchema,
  DrainQuerySchema,
  DrainStartResultSchema,
  DrainStatusResultSchema,
  LaunchResultSchema,
  PANELS,
  PRESET_REACHES_MODEL,
  PolicyResultSchema,
  RunsQuerySchema,
  RunsResultSchema,
  TopicsResultSchema,
  WATCH_PLUGIN_ID,
} from "../contract.ts";
import {
  INITIAL_DRAFT,
  INITIAL_DRAIN,
  NO_ACCOUNTS,
  act,
  drainStartRequest,
  drainStopInput,
  launchInput,
  launchRequest,
  read,
  sessionChoice,
  stopInput,
  unready,
  type AccountsResult,
  type DrainDraft,
  type DrainStatus,
  type LaunchAnswer,
  type LaunchDraft,
  type PolicyResult,
  type RunRow,
  type RunsResult,
  type TopicsResult,
} from "./api.ts";
import { Ceilings } from "./ceilings.tsx";
import { Drain } from "./drain.tsx";
import { Recipes } from "./recipes.tsx";
import { Runs } from "./runs.tsx";
import { Start } from "./start.tsx";

/*
  WATCH — the panel that helps the operator run Babel.

  Five sections in the order the questions are asked: what do I want to happen next, what is
  happening right now, how do I spend a window that is about to reset, what is Babel looking for,
  and what bounds it. There are no identifiers in the first one and no flags anywhere: a preset is
  a request, a knob is a number in the operator's units, and everything else on the screen is
  either something a run said or something the panel computed from what it said.

  Liveness is `usePolledResource`, one feed per resource, so two sections reading the same runs
  share one request; the elapsed clocks tick on the panel's own second while the runs and drains
  feeds poll every five. Nothing here holds a socket, and nothing polls while nothing is in
  flight.
*/

const RUNS_POLL_MS = 5_000;
const POLICY_POLL_MS = 60_000;
const TOPICS_POLL_MS = 60_000;
const MACHINES_POLL_MS = 30_000;
/** Which accounts a broker has observed changes when an account is enrolled, and not otherwise. */
const ACCOUNTS_POLL_MS = 60_000;
/** A dry preview is a read of the machine's own runtime report; it does not go stale quickly. */
const PREVIEW_POLL_MS = 120_000;
/** The clock the elapsed columns advance on. One second, because that is what "ticking" means. */
const TICK_MS = 1_000;
const RUNS_PAGE = 25;
/**
 * The drains feed polls on the runs feed's own five seconds, and for the same reason: a drain is
 * watched precisely while its jobs are running, and the go/no-go rule it exists to serve is "a
 * job says `at the model` within ninety seconds" (runbook §11.3). A slower poll would make the
 * ninety-second rule unanswerable from the screen.
 */
const DRAINS_POLL_MS = 5_000;
/** How many drains the panel lists: the running one, and enough history to compare against. */
const DRAINS_LISTED = 6;

const NO_RUNS: RunsResult = { runs: [], total: 0 };
const NO_DRAINS: readonly DrainStatus[] = [];
const NO_TOPICS: TopicsResult = { topics: [], proposed: [], unfiled: 0 };
const NO_MACHINES: readonly MachineSummary[] = [];

/** A denial's sentence, or the error's, as the note under a section. */
function noteOf(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

export function Watch({ host }: PanelProps) {
  const [draft, setDraft] = useState<LaunchDraft>(INITIAL_DRAFT);
  const [drainDraft, setDrainDraft] = useState<DrainDraft>(INITIAL_DRAIN);
  const [limit, setLimit] = useState(RUNS_PAGE);
  const [now, setNow] = useState(() => Date.now());
  const [starting, setStarting] = useState(false);
  const [startNote, setStartNote] = useState("");
  const [stopping, setStopping] = useState("");
  const [draining, setDraining] = useState(false);
  const [drainStopping, setDrainStopping] = useState("");
  const [drainNote, setDrainNote] = useState("");
  const [drainRead, setDrainRead] = useState("");
  /*
    TWO NOTES, NOT ONE. `readNote` is a failed read and is cleared by the next good answer;
    `stopNote` is what an act of the operator's said, and a successful refresh must not erase
    it — the refresh is the very thing his act asked for, and wiping the sentence on arrival is
    how "Asked run_7 to stop" disappeared half a second after he pressed the button.
  */
  const [runsNote, setRunsNote] = useState("");
  const [stopNote, setStopNote] = useState("");
  const [policyNote, setPolicyNote] = useState("");
  const [accountsNote, setAccountsNote] = useState("");
  const [drainAccountsNote, setDrainAccountsNote] = useState("");

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

  const drains = usePolledResource<readonly DrainStatus[]>(
    async () =>
      (
        await read(
          host,
          ACTIONS.drainStatus,
          DrainQuerySchema.parse({ limit: DRAINS_LISTED }),
          DrainStatusResultSchema,
        )
      ).drains,
    DRAINS_POLL_MS,
    {
      key: "atyrode.babel.drains",
      initial: NO_DRAINS,
      onError: (reason) => setDrainRead(noteOf(reason)),
      onSuccess: () => setDrainRead(""),
    },
  );

  /*
    WHICH ACCOUNTS THIS RUN COULD SPEND (#279), read per machine because a broker is a machine's
    own: the rows come from that host's enrolled credentials, and there is nothing to ask before
    one is picked.

    A FAILED READ IS THE SAME ANSWER AS AN UNAVAILABLE BROKER. The door already reports "nobody
    could be asked" as a reason rather than an empty list, and a refused dispatch — a hub whose
    caller may not read the service, a plugin half that is older than this panel — is that same
    situation arriving as an exception. Folding it into `unavailable` is what puts the typed
    fallback on screen instead of a dead select with a note nobody connects to it.
  */
  const accounts = usePolledResource<AccountsResult>(
    () =>
      read(
        host,
        ACTIONS.accounts,
        AccountsQuerySchema.parse({ machineId: draft.machineId }),
        AccountsResultSchema,
      ),
    ACCOUNTS_POLL_MS,
    {
      key: "atyrode.babel.accounts",
      initial: NO_ACCOUNTS,
      enabled: draft.machineId !== "",
      restartKey: draft.machineId,
      onError: (reason) => setAccountsNote(noteOf(reason)),
      onSuccess: () => setAccountsNote(""),
    },
  );
  const offered = useMemo<AccountsResult>(
    () => (accountsNote === "" ? accounts.value : { accounts: [], unavailable: accountsNote }),
    [accounts.value, accountsNote],
  );

  /*
    WHO WILL ANSWER IT. The pick is remade whenever the draft or the offered rows move — a poll
    can report the chosen account blocked between the choice and the press — and `needed` is the
    door's own table, so the one preset that reaches no model is never held up for a session it
    would never spend.
  */
  const needed = PRESET_REACHES_MODEL[draft.preset];
  const pick = useMemo(() => sessionChoice(draft, offered), [draft, offered]);
  const chosen = needed && pick.ok ? pick.session : null;

  /*
    AND WHICH ACCOUNT THE DRAIN WOULD SPEND. A second feed rather than a shared one, because the
    two forms name their own machine: a drain of dev-02 offered dev-01's credentials would be a
    picker listing rows that do not exist where its jobs will run. Both read the same door and
    both resolve through the same {@link sessionChoice}, which is the part that must not be two.
  */
  const drainAccounts = usePolledResource<AccountsResult>(
    () =>
      read(
        host,
        ACTIONS.accounts,
        AccountsQuerySchema.parse({ machineId: drainDraft.machineId }),
        AccountsResultSchema,
      ),
    ACCOUNTS_POLL_MS,
    {
      key: "atyrode.babel.drain.accounts",
      initial: NO_ACCOUNTS,
      enabled: drainDraft.machineId !== "",
      restartKey: drainDraft.machineId,
      onError: (reason) => setDrainAccountsNote(noteOf(reason)),
      onSuccess: () => setDrainAccountsNote(""),
    },
  );
  const drainOffered = useMemo<AccountsResult>(
    () =>
      drainAccountsNote === ""
        ? drainAccounts.value
        : { accounts: [], unavailable: drainAccountsNote },
    [drainAccounts.value, drainAccountsNote],
  );
  const drainPick = useMemo(
    () => sessionChoice(drainDraft, drainOffered),
    [drainDraft, drainOffered],
  );

  /*
    WHAT WILL RUN is a resource keyed on the request itself: change the machine, the preset or a
    knob and the dry read happens again, because the profile, the cost and the ceilings are
    facts about THAT request on THAT machine. `blocked` is why there is nothing to ask yet, and
    it is also the sentence the card shows in the line's place.
  */
  const blocked = unready(draft);
  const request = useMemo(() => (blocked === "" ? launchInput(draft, chosen) : null), [blocked, chosen, draft]);
  const [previewNote, setPreviewNote] = useState("");
  const preview = usePolledResource<LaunchAnswer | null>(
    () =>
      request === null
        ? Promise.resolve(null)
        : read(host, ACTIONS.launchPreview, request, LaunchResultSchema),
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
    THE MODEL THE MACHINE LAST RAN, offered as the field's starting point — once per machine, and
    never over the operator's own typing.

    `profile` is a RECORDED figure out of the newest receipt on that host, so it names a model
    that has actually answered there; a machine that has run nothing leaves the field empty,
    which is the honest shape of "nobody has run anything here yet" and is exactly the blank the
    picker exists to have the operator fill.
  */
  const prefilledFor = useRef("");
  useEffect(() => {
    const answer = preview.value;
    if (answer === null) return;
    const last = answer.profile?.model ?? "";
    if (last === "" || prefilledFor.current === answer.machineId) return;
    prefilledFor.current = answer.machineId;
    setDraft((current) =>
      current.session.model === "" ? { ...current, session: { ...current.session, model: last } } : current,
    );
  }, [preview.value]);

  /*
    The clock advances only while something is in flight. A panel that ticked over a page of
    receipts would be re-rendering a table of fixed numbers once a second forever — and a RUNNING
    DRAIN is in flight whatever its jobs are doing, because its own figures (the ETA against the
    deadline, how long it has been going) advance on the clock rather than on a poll.
  */
  const inFlight =
    runs.value.runs.some((run) => run.state === "queued" || run.state === "running") ||
    drains.value.some((drain) => drain.state === "running");
  useEffect(() => {
    if (!inFlight) return;
    const timer = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(timer);
  }, [inFlight]);

  const onDrainStart = useCallback(async () => {
    if (!drainPick.ok) return;
    setDraining(true);
    setDrainNote("");
    const outcome = await act(
      host,
      ACTIONS.drainStart,
      // The deadline is an instant computed at the press, from the minutes the operator set: a
      // form left open for ten minutes must not post a deadline ten minutes in the past.
      drainStartRequest(drainDraft, drainPick.session, Date.now()),
      DrainStartResultSchema,
    );
    setDraining(false);
    if (outcome.ok) {
      const started = outcome.value;
      setDrainNote(
        `Draining ${started.account} as ${started.drainId}: ${String(started.launched)} of ` +
          `${String(started.concurrent)} jobs launched.${started.note === "" ? "" : ` ${started.note}`}`,
      );
      setNow(Date.now());
      drains.refresh();
      runs.refresh();
      return;
    }
    setDrainNote(outcome.message);
  }, [drainDraft, drainPick, drains, host, runs]);

  const onDrainStop = useCallback(
    async (drain: DrainStatus) => {
      setDrainStopping(drain.drainId);
      const outcome = await act(host, ACTIONS.drainStop, drainStopInput(drain), z.unknown());
      setDrainStopping("");
      setDrainNote(
        outcome.ok
          ? `Asked ${drain.drainId} to stop: its jobs are cancelled and its overlay cleared.`
          : outcome.message,
      );
      drains.refresh();
      runs.refresh();
    },
    [drains, host, runs],
  );

  const onStart = useCallback(async () => {
    if (blocked !== "" || (needed && chosen === null)) return;
    setStarting(true);
    setStartNote("");
    const outcome = await act(host, ACTIONS.launch, launchRequest(draft, chosen), LaunchResultSchema);
    setStarting(false);
    if (outcome.ok) {
      setStartNote(`Started ${outcome.value.kind} as ${outcome.value.runId}.`);
      setNow(Date.now());
      runs.refresh();
      return;
    }
    setStartNote(outcome.message);
  }, [blocked, chosen, draft, host, needed, runs]);

  const onStop = useCallback(
    async (run: RunRow) => {
      setStopping(run.id);
      const outcome = await act(host, ACTIONS.stop, stopInput(run), z.unknown());
      setStopping("");
      setStopNote(
        outcome.ok ? `Asked ${run.id} to stop; it stops at its next safe point.` : outcome.message,
      );
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
        accounts={offered}
        session={needed ? pick : null}
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
      <Drain
        draft={drainDraft}
        drains={drains.value}
        machines={machines.value}
        topics={topics.value.topics}
        accounts={drainOffered}
        session={drainPick}
        now={now}
        starting={draining}
        stopping={drainStopping}
        note={drainNote === "" ? drainRead : drainNote}
        onDraft={setDrainDraft}
        onStart={onDrainStart}
        onStop={onDrainStop}
      />
      <Recipes recipes={policy.value?.recipes ?? []} now={now} note={policyNote} />
      <Ceilings policy={policy.value} now={now} note={policyNote} />
    </Stack>
  );
}

export default { id: WATCH_PLUGIN_ID, panels: { [PANELS.watch]: Watch } };
