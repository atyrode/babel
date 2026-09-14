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
} from "../contract.ts";
import { ActRefused, newId, setBudget } from "../store/acts.ts";
import {
  budgetChanges,
  perMachineBound,
  type Budget,
  type Coordinator,
} from "../store/coordinator.ts";
import { perRunUsd } from "../server/plan.ts";
import {
  drainOnMachine,
  drainStatus,
  insertDrain,
  readDrain,
  recentDrains,
  reconcileLive,
  recordLaunch,
  type DrainKnobs,
} from "../store/drains.ts";
import {
  drainIdentity,
  drainInput,
  drainOperation,
  endDrain,
  type DrainDeps,
} from "../server/drain.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE THREE DOORS A DRAIN IS RUN THROUGH: start one, read one, end one (#258).

  A DRAIN IS ONE OPERATOR DECISION AND THESE DOORS ARE ITS WHOLE SURFACE. On 2026-09-13 the
  decision — spend this account's remaining week before it resets — had no expression anywhere in
  the product, so it became four generations of shell loop with no target, no burn rate and no
  self-stop. `drain.start` is that decision stated once: the account, the preset, the fan, and
  where it stops.

  WHY `drain.start` IS GOVERNED AT THE OPERATION NODE, like `launch`. It posts jobs, so it
  declares `machines:run` and pairs it with a requirement whose target is the `operation` field
  of its own arguments: the host walks that path through the RAW arguments, discharges the
  capability there, and admits the dispatch against the operator's version-bound consent at that
  node (ADR 0035). The delegates are `launch`'s, for the same reason — they are the native
  ceiling the posted job inherits and what `onJobSettled` later reads its outputs back with.

  WHY `drain.stop` IS GOVERNED AT THE OPERATION NODE AND NOT AT A JOB. A drain holds several jobs
  and a declared requirement resolves to exactly ONE node (`plugin-host.ts` parses one
  `ManifoldRef` per target), so a stop that named a job could only ever cancel one of them. The
  hub reads a job's consent at its operation anyway (`job-service.ts` `consentFor` maps a job node
  to its operation before looking the row up), so consent at the operation is precisely what
  cancelling every job of this drain needs — and asking for it by name is honest about the
  breadth instead of borrowing it one job at a time.

  WHY `drain.status` IS A DRY READ. It answers what is draining, under `containers:read`, asking
  no machine anything: the panel polls it every five seconds while the operator watches, and
  requiring version-bound consent at a node merely to READ a burn rate is the interface unable to
  say what it is doing. Everything it reports comes from this plugin's own tables — the drain row,
  the runs the drain launched, and the conductor's fold of where each of them is.
*/

/** Posting jobs is governed at the operation node the request names; see the block above. */
const START_CAPS = ["machines:run"] as const;
const START_REQUIREMENTS = [{ cap: "machines:run" as const, target: ["operation"] }];
/** The native ceiling the launched jobs inherit; `doors/launch.ts` says why these are delegates. */
const START_DELEGATES = ["jobs:read", "locations:read", "locations:write"] as const;

/** Ending a drain cancels every job it holds, which is one consent at their shared operation. */
const STOP_CAPS = ["jobs:cancel"] as const;
const STOP_REQUIREMENTS = [{ cap: "jobs:cancel" as const, target: ["operation"] }];

/** A dry read of this plugin's own tables; it asks no machine anything. */
const STATUS_CAPS = ["containers:read"] as const;

/** Every act of a drain is news on this plugin's own node, as `doors/acts.ts` explains. */
const OWN_NODE = { kind: "plugin", pluginId: BABEL_PLUGIN_ID } as const;

const SPENDING: readonly string[] = DRAIN_SPENDING_PRESETS;

export interface DrainDoorDeps {
  readonly coordinator: Coordinator;
  /** The controller's own dependencies, over this dispatch's authority. */
  deps(ctx: Parameters<Door["handler"]>[0]): DrainDeps;
  /** The manifest's `concurrentJobs`, which is the ceiling an overlay is judged against. */
  readonly concurrentJobs: number;
  now(): number;
}

export function drainDoors(store: BabelStore, doorDeps: DrainDoorDeps): readonly Door[] {
  const start = defineDoor(
    defineServerAction({
      name: ACTIONS.drainStart,
      title: "Drain a usage window on purpose",
      caps: START_CAPS,
      delegates: START_DELEGATES,
      requirements: START_REQUIREMENTS,
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
        input.target.deadline === undefined
      ) {
        return {
          refused:
            "a drain needs a target: a cost in micro-dollars, a number of output tokens, or a " +
            "deadline. A drain without one is not a drain, it is a loop (runbook §11.1)",
        };
      }
      const deadline =
        input.target.deadline === undefined ? null : Date.parse(input.target.deadline);
      if (deadline !== null && !Number.isFinite(deadline)) {
        return { refused: `${JSON.stringify(input.target.deadline)} is not an instant` };
      }
      const at = doorDeps.now();
      if (deadline !== null && deadline <= at) {
        return { refused: `the deadline ${input.target.deadline ?? ""} has already passed` };
      }
      // A PRESET THAT SPENDS NOTHING CANNOT MEET A SPEND TARGET, so one is refused rather than
      // started as a fan nothing will ever stop: `keep-going` is a `scan`, it reaches no model,
      // and its metered spend is zero for as long as it runs.
      if (!SPENDING.includes(input.preset) && deadline === null) {
        return {
          refused:
            `the ${input.preset} preset reaches no model, so its metered spend stays at zero ` +
            `and a cost or token target is never met: give this drain a deadline`,
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
        THE OVERLAY THE DRAIN RUNS UNDER (#260), and the two numbers it moves.

        `concurrentPerMachine` is the fan: it is the ONE admission knob, what the coordinator
        bounds a machine by, and what makes the loop's own draws and this drain's jobs count
        against one number rather than two that add up past what the machine runs.

        `perCycleCost` moves WITH it, to exactly `perRunUsd(standing) * concurrent`, because
        `applyBudget` mirrors the bound into the batch and `perRunUsd` is the cycle's allowance
        DIVIDED by the batch: raising the fan alone would quietly cut what one run may spend to a
        fraction of it. So a drain changes how many runs happen at once and never what one run is
        allowed — which is the invariant that makes "a heavier review is a profile change, not a
        drain change" true (#268).

        `dailyCost` is deliberately NOT moved. A drain's jobs are launched directly and consult
        no daily allowance; raising it would be an operator moving a number nothing reads, which
        is the exact failure #260 exists to remove, rebuilt one layer up.
      */
      const standing = inForce.standing;
      const expiresAt = deadline ?? at + DRAIN_DEFAULT_TTL_MS;
      const perCycleCost = perRunUsd(standing) * input.concurrent;
      const wanted: Budget = {
        id: "",
        createdAt: at,
        expiresAt,
        perCycleCost,
        dailyCost: null,
        concurrentPerMachine: input.concurrent,
        reason: input.reason,
      };
      let budgetId = "";
      let note = "";
      if (budgetChanges(standing, wanted).length === 0) {
        // A drain asking for what the standing policy already admits needs no exception, and an
        // overlay that moves no number is refused by `setBudget` for that reason. Saying so is
        // better than storing a no-op nobody can tell from a mistake.
        note =
          `no overlay was set: the standing policy already admits ` +
          `${String(perMachineBound(standing))} at once on a machine`;
      } else {
        try {
          const overlaid = await setBudget(
            store,
            {
              expiresAt: new Date(expiresAt).toISOString(),
              perCycleCost,
              concurrentPerMachine: input.concurrent,
              reason: input.reason,
            },
            ctx.principal.id,
            doorDeps.concurrentJobs,
          );
          budgetId = overlaid.id;
        } catch (error) {
          return {
            refused:
              error instanceof ActRefused
                ? `this drain's fan of ${String(input.concurrent)} cannot be admitted: ${error.message}`
                : `this drain's overlay could not be set: ${message(error)}`,
          };
        }
      }

      const knobs: DrainKnobs = {
        recipes: input.recipes,
        ...(input.sinceDays === undefined ? {} : { sinceDays: input.sinceDays }),
        ...(input.entityId === undefined ? {} : { entityId: input.entityId }),
        ...(input.minutes === undefined ? {} : { minutes: input.minutes }),
        ...(input.agentSessions === undefined ? {} : { agentSessions: input.agentSessions }),
      };
      const drainId = newId("drn");
      await insertDrain(store, {
        id: drainId,
        machineId: input.machineId,
        preset: input.preset,
        session: input.session,
        knobs,
        concurrent: input.concurrent,
        target: input.target,
        startedBy: ctx.principal.id,
        budgetId,
      });

      /*
        THE FIRST FAN IS POSTED HERE rather than left to the next wake, and that is the whole of
        the go/no-go rule: the operator presses the button and within ninety seconds a job says
        `at the model` (runbook §11.3). A drain whose first job waited for a settlement that
        cannot happen — because nothing is in flight to settle — would never start at all.

        A refusal on the first slot is the DRAIN's refusal and not a note: the operator is
        standing at the button, and "started, holding nothing, for the reason below" is the
        answer the 2026-09-13 fans never gave. The overlay and the row are unwound, so a refused
        start leaves nothing behind to clear up.
      */
      const row = await readDrain(store, drainId);
      if (row === null) return { refused: `the drain row for ${drainId} was not written` };
      const deps = doorDeps.deps(ctx);
      const plan = deps.plan(inForce.policy, operationId, input.session);
      const request = drainInput(row);
      const live: { runId: string; jobId: string; launchedAt: number }[] = [];
      let refused = "";
      for (let slot = 0; slot < input.concurrent; slot += 1) {
        const identity = drainIdentity(row, slot);
        const started = SPENDING.includes(input.preset)
          ? await deps.launch.startExplore(identity, deps.jobs, request, plan)
          : await deps.launch.startBeat(identity, deps.jobs, request, plan);
        if ("refused" in started) {
          refused = started.refused;
          break;
        }
        const job = { runId: started.runId, jobId: started.jobId, launchedAt: at };
        await recordLaunch(store, drainId, job, live);
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
        budgetId,
        account: input.session.account.identityKey,
        model: input.session.model,
        note: refused === "" ? note : `${note === "" ? "" : `${note}; `}only ${String(live.length)} of ${String(input.concurrent)} started: ${refused}`,
      };
    },
  );

  const status = defineDoor(
    defineServerAction({
      name: ACTIONS.drainStatus,
      title: "Read what is draining",
      caps: STATUS_CAPS,
      input: DrainQuerySchema,
      result: DrainStatusResultSchema,
    }),
    async (_ctx, query) => {
      if (query.drainId !== undefined) {
        const row = await readDrain(store, query.drainId);
        if (row === null) return { refused: `no drain ${query.drainId}` };
        return { drains: [await drainStatus(store, row)] };
      }
      const rows = await recentDrains(store, query.limit);
      const drains = [];
      for (const row of rows) drains.push(await drainStatus(store, row));
      return { drains };
    },
  );

  const stop = defineDoor(
    defineServerAction({
      name: ACTIONS.drainStop,
      title: "Stop a drain",
      caps: STOP_CAPS,
      requirements: STOP_REQUIREMENTS,
      input: DrainStopInputSchema,
      result: DrainStopResultSchema,
    }),
    async (ctx, input) => {
      const row = await readDrain(store, input.drainId);
      if (row === null) return { refused: `no drain ${input.drainId}` };
      if (row.state !== "running") {
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
      const reason = input.reason === "" ? `stopped by ${ctx.principal.id}` : input.reason;
      const ended = await endDrain(doorDeps.deps(ctx), row, "stopped", reason, seen.holding);
      ctx.emit(OWN_NODE, EVENTS.runChanged, {
        drainId: row.id,
        machineId: row.machineId,
        cancelled: ended.cancelled,
      });
      return {
        drainId: row.id,
        state: "stopped" as const,
        cancelled: ended.cancelled,
        note: ended.notes.join("; "),
      };
    },
  );

  return [start, status, stop];
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
