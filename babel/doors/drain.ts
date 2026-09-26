import { defineServerAction } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  DRAIN_DEFAULT_TTL_MS,
  DRAIN_SPENDING_PRESETS,
  DrainQuerySchema,
  DrainStartRequestSchema,
  DrainStartResultSchema,
  DrainStatusResultSchema,
  DrainStopInputSchema,
  DrainStopResultSchema,
  EVENTS,
  PRESET_OPERATIONS,
  type DrainProfile,
  type DrainReportPayload,
  type DrainTarget,
} from "../contract.ts";
import { newId } from "../store/acts.ts";
import {
  accountName,
  drainOnMachine,
  drainStatus,
  insertDrain,
  readDrain,
  readDrainReport,
  recentDrains,
  reconcileLive,
  recordLaunch,
  type DrainKnobs,
  type DrainRow,
} from "../store/drains.ts";
import {
  drainIdentity,
  drainInput,
  drainOperation,
  endDrain,
  foldDrain,
  type DrainDeps,
} from "../server/drain.ts";
import type { BabelStore } from "../store/store.ts";
import type { Coordinator } from "../store/coordinator.ts";
import { defineDoor, type Door } from "./door.ts";
import { POSTING_DELEGATES, pressOperation } from "./launch.ts";

/*
  THE THREE DOORS A DRAIN IS RUN THROUGH: start one, read one, end one (#258).

  A DRAIN IS ONE OPERATOR DECISION AND THESE DOORS ARE ITS WHOLE SURFACE. On 2026-09-13 the
  decision — spend this account's remaining week before it resets — had no expression anywhere in
  the product, so it became four generations of shell loop with no target, no burn rate and no
  self-stop. `drain.start` is that decision stated once: the account, the preset, the fan, and
  where it stops.

  WHAT `drain.start` IS LENT: THE READ BEFORE THE FAN, AND THE POSTING ITSELF. It names no node
  today (#279, below), but it posts the first fan through `launchMachinery`'s own
  `startExplore`/`startBeat` — and the first thing either of those does is ask the machine
  whether it can run the operation at all, `engine.jobs.describe` behind `ready`. That read is
  `machines:read` since atyrode/manifold#736 and delegable since atyrode/manifold#740, and the
  dispatcher attenuates `ctx.jobs` to the door's own caps plus delegates — so a start that did
  not name it would be refused `job_capability_absent:machines:read` at the first slot and report
  "launched nothing" about a machine nobody ever asked. The posting that follows is Babel's own
  `prepare` or its beat, discharged at `engine.jobs.execute` against the same bridge, so the start
  carries everything that posting requires, `machines:run` (#448) and the locations, service and
  host network the archive operations declare (#453, `POSTING_DELEGATES` in `doors/launch.ts`).
  `doors/read.ts` carries the reasoning.

  WHY `drain.stop` IS GOVERNED AT THE OPERATION NODE AND NOT AT A JOB. A drain holds several jobs
  and a declared requirement resolves to exactly ONE node (`plugin-host.ts` parses one
  `ManifoldRef` per target), so a stop that named a job could only ever cancel one of them. The
  hub reads a job's consent at its operation anyway (`job-service.ts` `consentFor` maps a job node
  to its operation before looking the row up), so consent at the operation is precisely what
  cancelling every job of this drain needs — and asking for it by name is honest about the
  breadth instead of borrowing it one job at a time.

  WHY `drain.status` IS A DRY READ THAT STILL CARRIES DELEGATES. It answers what is draining,
  under `containers:read`, asking no machine anything: the panel polls it every five seconds while
  the operator watches, and requiring version-bound consent at a node merely to READ a burn rate
  is the interface unable to say what it is doing. Everything it reports comes from this plugin's
  own tables — the drain row, the runs the drain launched, and the conductor's fold of where each
  of them is.

  BUT IT IS ONE OF THE DOORS A CYCLE FOLLOWS (`server.ts`'s `WAKES`), and that is what the
  delegate is for: the dispatcher attenuates `ctx.jobs` to what the door declared, so a cycle
  behind a door with no `jobs:read` cannot read back a single job — every `jobs.status` in
  `reconcileRuns` refuses, nothing settles, and the `run_progress` fold this wake EXISTS for
  never happens. `pulse` and `runs` carry the same delegate for the same reason
  (`doors/read.ts`), and the same cycle's `machines:read`: it is the cycle, not this door, that
  describes a machine to keep the loop's beat registered, and the cycle that relaunches a
  drain's settled slot, so it carries what posting requires as well (#448, #453). It widens
  nothing: a delegate is the native ceiling the door's own job authority may reach, intersected
  with the caller's capabilities and the plugin's install grant, and the caller still needs only
  `containers:read`.
*/

/**
 * WHAT EACH DOOR ASKS ITS CALLER FOR, and why none of it is governed at a node today (#279).
 *
 * A drain's jobs are `atyrode.babel.explore`, and no installation declares that operation any
 * more: the host discharges a requirement's target against the RAW arguments BEFORE the
 * handler runs, so `machines:run` at that node refuses the dispatch "explicit version-bound
 * consent required" and the operator never hears `engine_pending` — nor, on a drain v0.3.0
 * left running, can he stop it at all. `doors/launch.ts` says the whole of it.
 *
 * So a start asks `containers:read` and carries `machines:read` and what posting requires as
 * DELEGATES — the read the launch path makes before it posts, and the posting — and a stop asks
 * `containers:write` — closing the row is a write of this plugin's own rows — and carries
 * `jobs:cancel` as a DELEGATE, the native ceiling its own job authority may reach. The hub still
 * checks consent at the effect: a cancel it will not admit is reported by name rather than
 * assumed. The governed requirements return with the node they are discharged at, which is
 * Code's operation, once its door posts the job.
 */
const START_CAPS = ["containers:read"] as const;
const START_DELEGATES = ["machines:read", ...POSTING_DELEGATES] as const;

const STOP_CAPS = ["containers:write"] as const;
const STOP_DELEGATES = ["jobs:cancel"] as const;

/** A dry read of this plugin's own tables; it asks no machine anything. */
const STATUS_CAPS = ["containers:read"] as const;
/** …but a cycle follows it, and a cycle that cannot read a job, describe a machine or post a
 *  drain's next slot folds nothing, keeps no cadence and relaunches nothing; see above. */
const STATUS_DELEGATES = ["jobs:read", "machines:read", ...POSTING_DELEGATES] as const;

/** Every act of a drain is news on this plugin's own node, as `doors/acts.ts` explains. */
const OWN_NODE = { kind: "plugin", pluginId: BABEL_PLUGIN_ID } as const;

const SPENDING: readonly string[] = DRAIN_SPENDING_PRESETS;

export interface DrainDoorDeps {
  readonly coordinator: Coordinator;
  /** The controller's own dependencies, over this dispatch's authority. */
  deps(ctx: Parameters<Door["handler"]>[0]): DrainDeps;
  /** The manifest's `concurrentJobs`: the most jobs of one operation a machine runs at once. */
  readonly concurrentJobs: number;
  now(): number;
}

export function drainDoors(store: BabelStore, doorDeps: DrainDoorDeps): readonly Door[] {
  /** A drain's own report, or null while it is still running: there is nothing final to report. */
  const reportOf = async (row: DrainRow): Promise<DrainReportPayload | null> =>
    row.state === "running" || row.state === "closing"
      ? null
      : await readDrainReport(store, row.id);

  const start = defineDoor(
    defineServerAction({
      name: ACTIONS.drainStart,
      title: "Drain a usage window on purpose",
      caps: START_CAPS,
      delegates: START_DELEGATES,
      input: DrainStartRequestSchema,
      result: DrainStartResultSchema,
    }),
    async (ctx, input) => {
      const operationId = PRESET_OPERATIONS[input.preset];
      // The host discharged `machines:run` at the node in the ARGUMENTS, so this is the only
      // place that can say the node is the one the request is about; a request whose two halves
      // disagree is refused rather than reconciled (`doors/launch.ts` says the whole of it).
      if (
        input.operation.machineId !== input.machineId ||
        input.operation.operationId !== operationId
      ) {
        return {
          refused:
            `this drain names ${input.machineId}/${operationId} and asks for authority at ` +
            `${input.operation.machineId}/${input.operation.operationId}`,
        };
      }
      if (
        input.target.costMicros === undefined &&
        input.target.outputTokens === undefined &&
        input.target.deadline === undefined &&
        input.maxJobs === undefined
      ) {
        return {
          refused:
            "a drain needs a target: a cost in micro-dollars, a number of output tokens, a " +
            "deadline, or maxJobs. A drain without one is not a drain, it is a loop (runbook §11.1)",
        };
      }
      const requested =
        input.target.deadline === undefined ? null : Date.parse(input.target.deadline);
      if (requested !== null && !Number.isFinite(requested)) {
        return { refused: `${JSON.stringify(input.target.deadline)} is not an instant` };
      }
      const at = doorDeps.now();
      if (requested !== null && requested <= at) {
        return { refused: `the deadline ${input.target.deadline ?? ""} has already passed` };
      }
      /*
        THE FAN IS BOUNDED AGAINST THE MANIFEST, HERE, AND BY THE DRAIN'S OWN JOBS IN THE
        CONTROLLER — never by the coordinator, which cannot see a drain's jobs at all.

        `concurrentJobs` is `limits.concurrentJobs` on the operation this preset posts: the hub
        refuses every posting past it at `execute` (atyrode/manifold#551), so a fan above it would
        spend the drain's first round on refusals. It is refused by name instead. What keeps the
        fan AT the number the operator asked for is `tickDrain`, which launches only into the
        slots its own `live` jobs leave free — a drain's jobs take no claim (the direct presets go
        `ready` → `post` → `jobs.execute`), so no admission bound in `coordinator.ts` has ever
        counted one.
      */
      if (input.concurrent > doorDeps.concurrentJobs) {
        return {
          refused:
            `this drain's fan of ${String(input.concurrent)} cannot be admitted: it is above the ` +
            `${String(doorDeps.concurrentJobs)} jobs a machine runs at once under this plugin's ` +
            `manifest, and the hub refuses every posting past that at execute`,
        };
      }
      // A PRESET THAT SPENDS NOTHING CANNOT MEET A SPEND TARGET, so one is refused rather than
      // started as a fan nothing will ever stop: `keep-going` is the beat, it reaches no model,
      // and its metered spend is zero for as long as it runs.
      if (!SPENDING.includes(input.preset) && requested === null && input.maxJobs === undefined) {
        return {
          refused:
            `the ${input.preset} preset reaches no model, so its metered spend stays at zero ` +
            `and a cost or token target is never met: give this drain a deadline or maxJobs`,
        };
      }
      const held = await drainOnMachine(store, input.machineId);
      if (held !== null) {
        // ONE DRAIN PER MACHINE. Two controllers fanning one host is the 2026-09-13 failure with
        // better manners: each would count only its own jobs against its own bound, and the sum
        // is what the machine actually runs.
        return {
          refused:
            `${input.machineId} is already draining under ${held.id}, started ${held.startedAt}: ` +
            `stop that one before starting another`,
        };
      }
      const inForce = await doorDeps.coordinator.policy(at);
      if (!inForce.policy.enabled) {
        return {
          refused:
            `the evaluation policy in force (${inForce.version}) is disabled, so Babel starts ` +
            `nothing; enable it and the drain runs under its ceilings`,
        };
      }

      /*
        A DRAIN SETS NO BUDGET OVERLAY, and #260's own reasoning is why.

        AN OVERLAY MOVES ADMISSION NUMBERS, AND A DRAIN'S JOBS CONSULT NONE OF THEM. The three
        presets a drain fans out are launched DIRECTLY — `startExplore`/`startBeat` go `ready` →
        `post` → `jobs.execute` — so they take no claim, and `openClaims`/`activeInBatch`, which
        is all admission counts, never sees one. Raising `concurrentPerMachine` for the drain's
        TTL therefore bounded nothing of the drain's: what it did was raise the CONDUCTOR's
        review bound to the fan's size on every online machine (`cap = bound * machines.length`),
        so a drain on one host widened another host's review fan for two hours — a number moved
        for something that does not read it, which is the exact failure #260 exists to remove.

        WHAT ONE RUN MAY SPEND IS THE POLICY'S, UNTOUCHED. `perRunUsd(standing)` is the ceiling
        every job of this drain inherits through its plan, and a drain changes how many runs
        happen at once and never what one run is allowed (#268) — so there is nothing left for an
        overlay to carry: moving `perCycleCost` alone would divide that ceiling by the batch and
        break exactly the invariant. A heavier run is a profile change; a longer drain is a
        deadline. The standing `policies` row and the `budgets` table are both untouched, and the
        row records no overlay because there is none to unwind.
      */
      // EVERY DRAIN CARRIES A DEADLINE, whether the operator named one or not: two hours is the
      // 2026-09-13 drain's own length, and a drain that outlives the window it exists to spend is
      // what the operation was written against. The instant is on the row, so what stops it is one
      // of its own targets rather than somebody remembering to.
      const deadline = requested ?? at + DRAIN_DEFAULT_TTL_MS;
      const deadlineAt = new Date(deadline).toISOString();
      const target: DrainTarget = { ...input.target, deadline: deadlineAt };
      const note =
        requested === null
          ? `this drain names no deadline, so it stops at ${deadlineAt} whatever it has spent`
          : "";

      const knobs: DrainKnobs = {
        recipes: input.recipes,
        ...(input.sinceDays === undefined ? {} : { sinceDays: input.sinceDays }),
        ...(input.entityId === undefined ? {} : { entityId: input.entityId }),
        ...(input.minutes === undefined ? {} : { minutes: input.minutes }),
        ...(input.agentSessions === undefined ? {} : { agentSessions: input.agentSessions }),
        ...(input.maxJobs === undefined ? {} : { maxJobs: input.maxJobs }),
        ...(input.inferenceLimits === undefined ? {} : { inferenceLimits: input.inferenceLimits }),
      };

      /*
        WHAT CODE SAYS THIS PROFILE WILL SPEND, COPIED ONCE, HERE (#267, #279).

        Babel chooses no model and no account, so the only honest record of what a fan is
        burning is Code's own, read at the moment the operator presses and written on the row
        as a LEDGER ENTRY — not re-read per job, because a controller that asked again between
        the first job and the ninetieth would report whatever the profile had become rather
        than what was started, which is the class of drift the whole operation exists against.

        A profile Code does not list is refused here: the operator picked from a list, and a
        container that has since gone is a press against something that no longer exists.
        Code answering nothing at all is a different refusal and says so.
      */
      const doorDepsAtStart = doorDeps.deps(ctx);
      const listed = await doorDepsAtStart.engine.profiles();
      if (!listed.ok) return { refused: listed.refused };
      const named = listed.value.find(
        (candidate) => candidate.containerId === input.profile.containerId,
      );
      if (named === undefined) {
        return {
          refused:
            `no_such_profile: Code lists no workspace ${input.profile.containerId}. Pick a ` +
            `profile from the list this panel read, or parametrize one in Code's generator.`,
        };
      }
      const ledger: DrainProfile = {
        profile: input.profile,
        model: named.model,
        thinking: named.thinking,
        accounts: [...named.accounts],
        resolved: named.resolved,
      };

      const drainId = newId("drn");
      await insertDrain(store, {
        id: drainId,
        machineId: input.machineId,
        preset: input.preset,
        profile: ledger,
        knobs,
        concurrent: input.concurrent,
        target,
        startedBy: ctx.principal.id,
      });

      /*
        THE FIRST FAN IS POSTED HERE rather than left to the next wake, and that is the whole of
        the go/no-go rule: the operator presses the button and within ninety seconds a job says
        `at the model` (runbook §11.3). A drain whose first job waited for a settlement that
        cannot happen — because nothing is in flight to settle — would never start at all.

        A refusal on the first slot is the DRAIN's refusal and not a note: the operator is
        standing at the button, and "started, holding nothing, for the reason below" is the
        answer the 2026-09-13 fans never gave. The row is closed as `failed`, so a refused start
        leaves nothing running and nothing to clear up.
      */
      const row = await readDrain(store, drainId);
      if (row === null) return { refused: `the drain row for ${drainId} was not written` };
      const deps = doorDepsAtStart;
      // The fan holds `concurrent` materials on the machine at once: each is bounded to that
      // share of its scratch, as every later tick's are (#453).
      const plan = {
        ...deps.plan(inForce.policy, pressOperation(input.preset), ledger),
        materials: input.concurrent,
      };
      const request = drainInput(row);
      const live: { runId: string; jobId: string; launchedAt: number }[] = [];
      let refused = "";
      for (let slot = 0; slot < Math.min(input.concurrent, input.maxJobs ?? Infinity); slot += 1) {
        const identity = drainIdentity(row, slot);
        const started = SPENDING.includes(input.preset)
          ? await deps.launch.startExplore(identity, deps.jobs, deps.engine, request, plan)
          : await deps.launch.startBeat(identity, deps.jobs, request, plan);
        if ("refused" in started) {
          refused = started.refused;
          break;
        }
        const job = { runId: started.runId, jobId: started.jobId, launchedAt: at };
        await recordLaunch(store, drainId, job, slot);
        live.push(job);
      }
      if (live.length === 0) {
        await endDrain(deps, row, "failed", `nothing could be launched: ${refused}`, []);
        return { refused: `this drain launched nothing: ${refused}` };
      }
      store.touch();
      ctx.emit(OWN_NODE, EVENTS.runChanged, {
        drainId,
        machineId: input.machineId,
        launched: live.length,
      });
      return {
        drainId,
        machineId: input.machineId,
        preset: input.preset,
        concurrent: input.concurrent,
        launched: live.length,
        deadline: deadlineAt,
        account: accountName(ledger),
        model: ledger.model,
        note:
          refused === ""
            ? note
            : `${note === "" ? "" : `${note}; `}only ${String(live.length)} of ${String(input.concurrent)} started: ${refused}`,
      };
    },
  );

  const status = defineDoor(
    defineServerAction({
      name: ACTIONS.drainStatus,
      title: "Read what is draining",
      caps: STATUS_CAPS,
      delegates: STATUS_DELEGATES,
      input: DrainQuerySchema,
      result: DrainStatusResultSchema,
    }),
    async (_ctx, query) => {
      /*
        THE REPORT TRAVELS WITH THE DRAIN THAT LEFT IT (#270), and only with the NEWEST ended
        one. A drain asked for by name gets its own — that read is a reader opening one drain —
        and a listing gets exactly one, because the panel's question is "what did the last drain
        do" and six payloads on a five-second poll is a listing paying for pages nobody opened.
        Every other row carries null, which is the honest answer for a drain still running and
        for one that ended before this record existed.
      */
      if (query.drainId !== undefined) {
        const row = await readDrain(store, query.drainId);
        if (row === null) return { refused: `no drain ${query.drainId}` };
        return { drains: [await drainStatus(store, row, await reportOf(row))] };
      }
      const rows = await recentDrains(store, query.limit);
      const drains = [];
      let reported = false;
      for (const row of rows) {
        const report = reported ? null : await reportOf(row);
        if (report !== null) reported = true;
        drains.push(await drainStatus(store, row, report));
      }
      return { drains };
    },
  );

  const stop = defineDoor(
    defineServerAction({
      name: ACTIONS.drainStop,
      title: "Stop a drain",
      caps: STOP_CAPS,
      delegates: STOP_DELEGATES,
      input: DrainStopInputSchema,
      result: DrainStopResultSchema,
    }),
    async (ctx, input) => {
      let row = await readDrain(store, input.drainId);
      if (row === null) return { refused: `no drain ${input.drainId}` };
      // A CLOSING DRAIN IS STILL STOPPABLE, and this is the only door that can do it: it has
      // stopped launching, but the jobs it could not cancel — a settlement's tick holds no
      // `jobs:cancel` — are still running, and this caller holds the capability at their
      // operation. The ending it was closed with stands: a stop that cancels the stragglers of a
      // drain that met its target did not change why it ended.
      if (row.state !== "running" && row.state !== "closing") {
        return { refused: `${input.drainId} already ended as ${row.state}: ${row.reason}` };
      }
      const operationId = drainOperation(row.preset);
      // The caller was admitted at the node it POSTED; the row says which operation this drain's
      // jobs are. A request that authorized one operation and named another is refused rather
      // than reconciled: cancelling under borrowed authority is not a thing this door does.
      if (
        input.operation.machineId !== row.machineId ||
        input.operation.operationId !== operationId
      ) {
        return {
          refused:
            `${input.drainId} runs ${row.machineId}/${operationId} and this stop asks for ` +
            `authority at ${input.operation.machineId}/${input.operation.operationId}`,
        };
      }
      // What it is actually holding, now: a job that settled between the panel's last poll and
      // this press is not something to cancel, and asking the hub to would be a refusal reported
      // as a failure of the stop.
      const seen = await reconcileLive(store, row.live);
      /*
        AND WHAT SETTLED IN THAT WINDOW IS FOLDED BEFORE THE ROW IS CLOSED. Only the fold
        releases settled jobs; closing uses the persisted held set so an overlapping launch
        cannot be lost. The fold refreshes its snapshot if another wake changed that set.
      */
      const folded = await foldDrain(store, row, seen, doorDeps.now());
      row = folded.row;
      if (row.state !== "running" && row.state !== "closing") {
        return { refused: `${input.drainId} already ended as ${row.state}: ${row.reason}` };
      }
      const reason =
        row.state === "closing"
          ? row.reason
          : input.reason === ""
            ? `stopped by ${ctx.principal.id}`
            : input.reason;
      const ending = row.state === "closing" && row.ending !== "" ? row.ending : "stopped";
      const ended = await endDrain(doorDeps.deps(ctx), row, ending, reason, folded.seen.holding);
      ctx.emit(OWN_NODE, EVENTS.runChanged, {
        drainId: row.id,
        machineId: row.machineId,
        cancelled: ended.cancelled,
      });
      return {
        drainId: row.id,
        state: ended.state,
        cancelled: ended.cancelled,
        note: [...folded.notes, ...ended.notes].join("; "),
      };
    },
  );

  return [start, status, stop];
}
