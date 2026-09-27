import type { PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import type { MachineSummary } from "@manifold/protocol";
import { ScrollRegion, Stack } from "@manifold/ui";
import { useCallback, useEffect, useMemo, useState } from "react";
import { z } from "zod";
import {
  ACTIONS,
  DrainQuerySchema,
  DrainStartResultSchema,
  DrainStatusResultSchema,
  DrainStopResultSchema,
  LaunchResultSchema,
  PANELS,
  PolicyResultSchema,
  ProfilesResultSchema,
  PulseResultSchema,
  RunsQuerySchema,
  RunsResultSchema,
  ServicesInstalledSchema,
  ServicesPreviewSchema,
  TopicsResultSchema,
  WATCH_PLUGIN_ID,
} from "../contract.ts";
import {
  DRAIN_CARDS,
  INITIAL_DRAIN,
  INITIAL_LAUNCH,
  act,
  chosenProfile,
  drainStartRequest,
  drainStopInput,
  mapDrainStartRequest,
  mappingRouteOf,
  launchRequest,
  read,
  serviceOrigins,
  stopInput,
  type DrainDraft,
  type DrainStatus,
  type LaunchDraft,
  type PolicyResult,
  type ProfilesResult,
  type RunRow,
  type RunsResult,
  type ServicesPreview,
  type TopicsResult,
  type WatchPulse,
} from "./api.ts";
import { Archive } from "./archive.tsx";
import { Ceilings } from "./ceilings.tsx";
import { Cycle } from "./cycle.tsx";
import { Drain } from "./drain.tsx";
import { Recipes } from "./recipes.tsx";
import { Runs } from "./runs.tsx";
import { Services } from "./services.tsx";
import { Start } from "./start.tsx";

/*
  WATCH — the panel that helps the operator run Babel.

  Five sections in the order the questions are asked: what can I start, what is happening right
  now, how do I spend a window that is about to reset, what is Babel looking for, and what bounds
  it. The FIRST one answers "nothing, and here is why": a Babel run is a Code session and Code's
  `runSession` door does not exist yet (#279, `watch/start.tsx`). It is a sentence rather than a
  form, because a form whose button always refused would make the operator discover that by
  pressing it.

  Liveness is `usePolledResource`, one feed per resource, so two sections reading the same runs
  share one request; the elapsed clocks tick on the panel's own second while the runs and drains
  feeds poll every five. Nothing here holds a socket, and nothing polls while nothing is in
  flight.
*/

const RUNS_POLL_MS = 5_000;
const POLICY_POLL_MS = 60_000;
const TOPICS_POLL_MS = 60_000;
const MACHINES_POLL_MS = 30_000;
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
/**
 * How often the saved Code profiles are re-read. It is the RETURN PATH of the generator link:
 * the operator leaves for Code, changes the model or the account on a workspace and comes
 * back, and nothing tells this panel he did — so the list is re-read on the machines' own
 * half-minute, which is short enough that a revision he just bumped is the one the button
 * carries and long enough that reading the page does not poll Code.
 */
const PROFILES_POLL_MS = 30_000;
/**
 * How often the last cycle's verdict is re-read (#328). Thirty seconds, because reading the
 * pulse is one of the four dispatches that WAKE the loop (`server.ts`'s `WAKES`) and the wake
 * is floored at thirty seconds anyway: a faster poll would cost a round trip to learn nothing
 * new, and a slower one would leave "why did nothing run" stale while an operator waited for
 * the answer to change.
 */
const CYCLE_POLL_MS = 30_000;

const NO_RUNS: RunsResult = { runs: [], total: 0 };
const NO_DRAINS: readonly DrainStatus[] = [];
const NO_TOPICS: TopicsResult = { topics: [], proposed: [], unfiled: 0 };
const NO_MACHINES: readonly MachineSummary[] = [];
const NO_PROFILES: ProfilesResult = { profiles: [], unavailable: "" };

/** A denial's sentence, or the error's, as the note under a section. */
function noteOf(reason: unknown): string {
  return reason instanceof Error ? reason.message : String(reason);
}

export function Watch({ host }: PanelProps) {
  const [draft, setDraft] = useState<LaunchDraft>(INITIAL_LAUNCH);
  const [drainDraft, setDrainDraft] = useState<DrainDraft>(INITIAL_DRAIN);
  const [limit, setLimit] = useState(RUNS_PAGE);
  const [now, setNow] = useState(() => Date.now());
  const [stopping, setStopping] = useState("");
  const [starting, setStarting] = useState(false);
  const [startNote, setStartNote] = useState("");
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
  const [profilesNote, setProfilesNote] = useState("");
  const [cycleNote, setCycleNote] = useState("");
  /*
    THE SERVICE POLICY IS READ ON DEMAND, not polled (#400). It is an owner-only read of a
    machine's whole service configuration, and there is no machine to read until the operator
    picks one — so a poll would be a refusal every thirty seconds for every viewer who is not
    the owner, about a machine nobody named. The press is the read, which is also what makes
    "write the file, then check" a sentence the panel can honour.
  */
  const [serviceMachine, setServiceMachine] = useState("");
  const [serviceDrafts, setServiceDrafts] = useState<Readonly<Record<string, string>>>({});
  const [servicePreview, setServicePreview] = useState<ServicesPreview | null>(null);
  /* The same two notes the runs feed keeps apart: a failed read, which the next good answer
     clears, and what the operator's own act said, which a refresh must not erase — the refresh
     is the very thing the act asked for. */
  const [serviceRead, setServiceRead] = useState("");
  const [serviceNote, setServiceNote] = useState("");
  const [checking, setChecking] = useState(false);
  const [installing, setInstalling] = useState(false);

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

  /*
    THE LAST CYCLE'S OWN VERDICT (#328) AND THE ARCHIVE'S UNMAPPED LABELS (#453). The pulse door
    carries both beside today's counts, and only those two are kept here: Home renders the
    counts, and a control room asking the same door twice for two halves of one answer would be
    two wakes of the loop for one question.
  */
  const pulse = usePolledResource<WatchPulse | null>(
    async () => {
      const { cycle, archive } = await read(host, ACTIONS.pulse, {}, PulseResultSchema);
      return { cycle, archive };
    },
    CYCLE_POLL_MS,
    {
      key: "atyrode.babel.cycle",
      initial: null,
      onError: (reason) => setCycleNote(noteOf(reason)),
      onSuccess: () => setCycleNote(""),
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

  const machines = usePolledResource<readonly MachineSummary[]>(
    () => host.client.machines(),
    MACHINES_POLL_MS,
    {
      key: "atyrode.babel.machines",
      initial: NO_MACHINES,
    },
  );

  /*
    THE SAVED CODE PROFILES, and the sentence saying Code could not be asked. Both halves are
    the door's answer and both are rendered: the panel never turns "Code is not installed" into
    an empty list, which reads as "you have saved none" and sends an operator to the wrong fix.
    A failed READ (the door itself refusing) is the third case, and it lands the same way —
    as the sentence under the section, through `unavailable`.
  */
  const profiles = usePolledResource<ProfilesResult>(
    () => read(host, ACTIONS.profiles, {}, ProfilesResultSchema),
    PROFILES_POLL_MS,
    {
      key: "atyrode.babel.profiles",
      initial: NO_PROFILES,
      onError: (reason) => setProfilesNote(noteOf(reason)),
      onSuccess: () => setProfilesNote(""),
    },
  );

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
    WHICH PROFILE THE DRAIN WOULD SPEND, resolved out of the list the panel is showing — the
    same door and the same rows the Start section reads. Babel reads no broker: the model and
    the account belong to the Code profile, and what the drain records about them is Code's
    own report copied at the press.
  */
  const drainProfiles: ProfilesResult =
    profilesNote === "" ? profiles.value : { profiles: [], unavailable: profilesNote };
  const drainProfile = useMemo(
    () => chosenProfile(drainDraft, drainProfiles.profiles),
    [drainDraft, drainProfiles],
  );

  /*
    The clock advances only while something is in flight. A panel that ticked over a page of
    receipts would be re-rendering a table of fixed numbers once a second forever — and a RUNNING
    DRAIN is in flight whatever its jobs are doing, because its own figures (the ETA against the
    deadline, how long it has been going) advance on the clock rather than on a poll.
  */
  const inFlight =
    runs.value.runs.some((run) => run.state === "queued" || run.state === "running") ||
    drains.value.some((drain) => drain.state === "running" || drain.state === "closing");
  useEffect(() => {
    if (!inFlight) return;
    const timer = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(timer);
  }, [inFlight]);

  /*
    THE START. The Code profile is resolved out of the list the panel is showing, so what the
    button carries is the container AND the revision the operator was looking at: a profile
    that moved between the read and the press is refused `code_stale_preferences` by Code
    itself, which is what the next poll of `profiles` then explains.
  */
  const onStart = useCallback(async () => {
    setStarting(true);
    setStartNote("");
    const outcome = await act(
      host,
      ACTIONS.launch,
      launchRequest(draft, chosenProfile(draft, profiles.value.profiles)),
      LaunchResultSchema,
    );
    setStarting(false);
    setStartNote(
      outcome.ok
        ? `Started ${outcome.value.kind} as ${outcome.value.runId} — Code's job is ${outcome.value.jobId}.`
        : outcome.message,
    );
    if (outcome.ok) {
      setNow(Date.now());
      runs.refresh();
    } else profiles.refresh();
  }, [draft, host, profiles, runs]);

  const mappingRoute = useMemo(() => mappingRouteOf(policy.value), [policy.value]);
  const onDrainStart = useCallback(async () => {
    const mapping = DRAIN_CARDS[drainDraft.preset].knob === "route";
    // The deadline is an instant computed at the press, from the minutes the operator set: a
    // form left open for ten minutes must not post a deadline ten minutes in the past.
    const press = mapping
      ? mappingRoute === null
        ? null
        : ([
            ACTIONS.mapDrainStart,
            mapDrainStartRequest(drainDraft, mappingRoute, Date.now()),
          ] as const)
      : drainProfile === null
        ? null
        : ([ACTIONS.drainStart, drainStartRequest(drainDraft, drainProfile, Date.now())] as const);
    if (press === null) return;
    setDraining(true);
    setDrainNote("");
    const outcome = await act(host, press[0], press[1], DrainStartResultSchema);
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
  }, [drainDraft, drainProfile, drains, host, mappingRoute, runs]);

  /*
    THE STOP'S ANSWER IS READ, NOT ASSUMED. The door returns `cancelled` and a `note` precisely
    because a stop can be honoured in part — a job the hub would not cancel names itself there —
    and a screen that said "its jobs are cancelled" over a refusal would be a progress claim
    without its number, on the one screen built to stop those (runbook §11.6, rule 2). A drain
    whose jobs are still running ends as `closing` and says so.
  */
  const onDrainStop = useCallback(
    async (drain: DrainStatus) => {
      setDrainStopping(drain.drainId);
      const outcome = await act(
        host,
        ACTIONS.drainStop,
        drainStopInput(drain),
        DrainStopResultSchema,
      );
      setDrainStopping("");
      if (outcome.ok) {
        const stopped = outcome.value;
        const jobs = `${String(stopped.cancelled)} of ${String(drain.jobsLive)} in-flight job(s) cancelled`;
        const ending =
          stopped.state === "closing"
            ? "it ends when their receipts land"
            : `it ended as ${stopped.state}`;
        setDrainNote(
          `Asked ${stopped.drainId} to stop: ${jobs}, ${ending}.` +
            `${stopped.note === "" ? "" : ` ${stopped.note}`}`,
        );
      } else setDrainNote(outcome.message);
      drains.refresh();
      runs.refresh();
    },
    [drains, host, runs],
  );

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

  /*
    CHECK, which is the read and the readiness in one press: `previewServices` composes the
    policy out of the manifest and reports, per service, what is installed and whether the
    machine's own credential came up. A refusal — the commonest being that this principal is
    not the hub's owner — lands as the note under the section, like every other refused read
    on this panel.
  */
  const onServiceCheck = useCallback(async () => {
    setChecking(true);
    const outcome = await act(
      host,
      ACTIONS.previewServices,
      { machineId: serviceMachine, origins: serviceOrigins(serviceDrafts) },
      ServicesPreviewSchema,
    );
    setChecking(false);
    setServicePreview(outcome.ok ? outcome.value : null);
    setServiceRead(outcome.ok ? "" : outcome.message);
  }, [host, serviceDrafts, serviceMachine]);

  /*
    THE PRESS CARRIES THE PREVIEW IT WAS READ FROM — the revision and the digest — so a policy
    that moved between the two is refused by name rather than overwritten. The answer replaces
    the preview with a fresh read, because the revision the operator now holds is the one the
    install just minted and a second press against the old one would refuse.
  */
  const onServiceInstall = useCallback(async () => {
    if (servicePreview === null) return;
    setInstalling(true);
    const outcome = await act(
      host,
      ACTIONS.installServices,
      {
        machineId: serviceMachine,
        origins: serviceOrigins(serviceDrafts),
        expectedRevision: servicePreview.expectedRevision,
        previewDigest: servicePreview.previewDigest,
      },
      ServicesInstalledSchema,
    );
    setInstalling(false);
    setServiceNote(
      outcome.ok
        ? `Installed ${outcome.value.services.map((service) => service.serviceId).join(", ")} on ` +
            `${outcome.value.machineId}. Re-install the jobs so their service bindings carry the ` +
            `new fingerprint.`
        : outcome.message,
    );
    if (outcome.ok) await onServiceCheck();
  }, [host, onServiceCheck, serviceDrafts, serviceMachine, servicePreview]);

  return (
    <ScrollRegion className="plugin-atyrode_babel_watch" aria-label="Watch">
      <Stack gap="var(--babel-space-6)">
        {/*
        A FAILED READ IS THE SAME SENTENCE AS A REFUSED ONE: `profiles` answers `unavailable`
        when Code refused, and when the DOOR refused the read threw and the note holds it. Both
        reach the two sections in one field rather than as two differently-shaped absences,
        because the operator's remedy is the same kind of thing either way.
      */}
        <Start
          draft={draft}
          machines={machines.value}
          topics={topics.value.topics}
          recipes={policy.value?.recipes ?? []}
          profiles={drainProfiles}
          starting={starting}
          note={startNote}
          onDraft={setDraft}
          onStart={onStart}
          onOpen={(uri) => host.navigate(uri)}
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
        {/*
        UNDER THE RUNS, because it explains their absence: an operator reads an empty table and
        the next thing on the page is why it is empty. It renders nothing at all when the last
        cycle spent normally.
      */}
        <Cycle cycle={pulse.value?.cycle ?? null} now={now} note={cycleNote} />
        {/*
        UNDER THE CYCLE, from the same read: which of the archive's host labels no machine is
        mapped to. It renders nothing when every label is mapped.
      */}
        <Archive archive={pulse.value?.archive ?? null} />
        <Drain
          draft={drainDraft}
          drains={drains.value}
          machines={machines.value}
          topics={topics.value.topics}
          profiles={drainProfiles}
          mappingRoute={mappingRoute}
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
        {/*
        LAST, under the ceilings, because it is the least frequent thing an operator does here:
        a service policy is installed once per machine and then re-read only when something has
        stopped working. It is on this panel rather than a settings page of its own for the
        reason the archive's own steps are — what it configures is what the machines run.
      */}
        <Services
          preview={servicePreview}
          drafts={serviceDrafts}
          machines={machines.value}
          machineId={serviceMachine}
          checking={checking}
          installing={installing}
          note={serviceNote === "" ? serviceRead : serviceNote}
          onMachine={(machineId) => {
            setServiceMachine(machineId);
            // A preview belongs to one machine; keeping it while the picker moved would show one
            // machine's readiness under another's name.
            setServicePreview(null);
            setServiceNote("");
            setServiceRead("");
          }}
          onDraft={(serviceId, origin) =>
            setServiceDrafts((held) => ({ ...held, [serviceId]: origin }))
          }
          onCheck={() => {
            // A read the OPERATOR asked for clears what the last act said; the read the install
            // makes for itself does not, which is why this is here and not inside `onServiceCheck`.
            setServiceNote("");
            void onServiceCheck();
          }}
          onInstall={onServiceInstall}
        />
      </Stack>
    </ScrollRegion>
  );
}

export default { id: WATCH_PLUGIN_ID, panels: { [PANELS.watch]: Watch } };
