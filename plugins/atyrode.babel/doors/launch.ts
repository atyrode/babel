import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  ENGINE_PENDING,
  LaunchRequestSchema,
  LaunchResultSchema,
  PRESET_OPERATIONS,
  StopInputSchema,
  StopResultSchema,
  type LaunchInput,
  type OperationName,
} from "../contract.ts";
import type { Coordinator } from "../store/coordinator.ts";
import type { JobsSlice, RunPlan } from "../server/conductor.ts";
import type { BabelJobs } from "../server/plan.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE TWO DOORS WATCH POSTS TO: start one thing, stop one thing. Starting refuses.

  BABEL LAUNCHES NOTHING (#279). `atyrode.babel` depends on `atyrode.code`, which depends on
  `atyrode.omp`. Code owns the profiles — the model, the thinking level, the account — and Code
  launches omp. When the operator presses Babel's button, Babel either names a saved Code
  profile or opens Code's generator so the run is parametrized there, and then posts the run
  through Code's `runSession` door. Babel never composes a session and never launches omp.

  Two pieces of that are in flight elsewhere: atyrode/manifold#575, which gives a plugin's
  server a way to call a sibling plugin's door, and atyrode/code#170, which is that door. Until
  both land there is nothing for this file to call, so every path through it answers
  {@link ENGINE_PENDING} and starts nothing.

  WHAT IS DELIBERATELY STILL HERE. `startExplore` and `startBeat` are the launch path the drain
  controller (#258) posts through, and they stay so that the drain's fan and the operator's
  button refuse with ONE sentence rather than two — the drain is inert until the engine returns,
  not removed. `stop` stays because a run this deployment already started can still be running
  when the plugin is upgraded, and the operator has to be able to end it and release its claim.

  WHAT WENT. `launchPreview` (what a run would cost, out of Babel's own inference policy), the
  session picker's `session_required`, the inline preparation, the recipe selection, the posting
  itself and the `runs` row it wrote: every one of them was Babel deciding what a model run is,
  and all of them are Code's. They come back as ARGUMENTS to Code's door, not as code here.

  WHY `launch` STILL DECLARES `machines:run` AT A NODE. A governed capability is granted at a
  NODE and never over a workspace (ADR 0035), and the host walks `operation` through the RAW
  arguments and discharges the capability there BEFORE the handler is entered. Dropping the
  requirement would make the refusal unauthenticated; keeping it means the operator's consent is
  still what admits the request, and what he hears is why nothing was started.
*/

/** Governed, at the operation node the request names; see the block above. */
const LAUNCH_CAPS = ["machines:run"] as const;
const LAUNCH_REQUIREMENTS = [{ cap: "machines:run" as const, target: ["operation"] }];
/**
 * The native ceiling a launched job would inherit: reading it back, and its declared locations.
 * They are DELEGATES rather than caps — the ceiling this door's job authority carries, not a
 * second thing to ask the caller for — and they stay declared because the drain's controller
 * and this door share one authority shape, and a shape that changed with the launch would be a
 * second review of the plugin's whole machine half for a door that posts nothing.
 */
const LAUNCH_DELEGATES = ["jobs:read", "locations:read", "locations:write"] as const;

/** Stopping is governed at the JOB node, which the run row carries and the panel posts. */
const STOP_CAPS = ["jobs:cancel"] as const;
const STOP_REQUIREMENTS = [{ cap: "jobs:cancel" as const, target: ["job"] }];

/**
 * WHAT A CALLER THAT IS NOT A DISPATCH BRINGS INSTEAD OF A `ctx` (#258).
 *
 * A drain's controller runs inside `cycle()`, and one of that function's two real wakes is
 * `onJobSettled`, whose `GuestJobSettledCtx` carries storage, the database and the settled job's
 * own authority — no `newId`, no `principal`. So the ids and the authority are PARAMETERS here,
 * which also makes them deterministic for a controller that wants a retried tick to re-post the
 * same job rather than a second one.
 */
export interface LaunchIdentity {
  readonly runId: string;
  readonly jobId: string;
  /** What the `runs` row records as `authority_id`; `authority_kind` stays `operator`. */
  readonly authorityId: string;
}

/** What a start answered: the two ids, or the sentence naming why nothing was started. */
export type Started = { runId: string; jobId: string } | { refused: string };

/**
 * THE LAUNCH PATH, EXPOSED SO THERE IS EXACTLY ONE OF IT.
 *
 * The drain controller (#258) launches explores and beats through the same object the button
 * does, because two implementations of "start a run" disagreeing is how an operator's ceiling
 * gets spent twice — which is the whole subject of the post-mortem this lane comes from. Today
 * both methods answer the same refusal, which is the same property stated at zero runs.
 */
export interface LaunchMachinery {
  /** One explore, if Babel could post one. It answers {@link ENGINE_PENDING}. */
  startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
  /** One beat, if Babel could post one. It answers {@link ENGINE_PENDING}. */
  startBeat(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
}

export interface LaunchDeps {
  readonly coordinator: Coordinator;
  /** This dispatch's own job authority, narrowed to the verbs this plugin uses. */
  jobs(ctx: GuestCtx): BabelJobs;
  now(): number;
}

/**
 * EVERY START, WHICH IS ONE REFUSAL.
 *
 * It still takes the store and the deps, because the machinery is the seam Code's `runSession`
 * call is wired into and a seam that vanished would have to be rediscovered — and it reads
 * neither today. A refusal that consulted the store first would be a refusal whose answer
 * depended on state, and every operator watching it would have to ask which state.
 */
export function launchMachinery(_store: BabelStore, _deps: LaunchDeps): LaunchMachinery {
  return {
    startExplore: async (): Promise<Started> => ({ refused: ENGINE_PENDING }),
    startBeat: async (): Promise<Started> => ({ refused: ENGINE_PENDING }),
  };
}

export function launchDoors(store: BabelStore, deps: LaunchDeps): readonly Door[] {
  const launch = defineDoor(
    defineServerAction({
      name: ACTIONS.launch,
      title: "Start a run on a machine",
      caps: LAUNCH_CAPS,
      delegates: LAUNCH_DELEGATES,
      requirements: LAUNCH_REQUIREMENTS,
      input: LaunchRequestSchema,
      result: LaunchResultSchema,
    }),
    async (_ctx, input) => {
      const operationId: OperationName = PRESET_OPERATIONS[input.preset];
      // The host discharged `machines:run` at the node in the ARGUMENTS; this is the only place
      // that can say the node is the one this request is actually about. A request whose two
      // halves disagree is answered as itself rather than folded into the engine's absence: the
      // operator fixes a mismatched node, and hears nothing about it if the other sentence
      // covers it.
      if (
        input.operation.machineId !== input.machineId ||
        input.operation.operationId !== operationId
      ) {
        return {
          refused:
            `this launch names ${input.machineId}/${operationId} and asks for authority ` +
            `at ${input.operation.machineId}/${input.operation.operationId}`,
        };
      }
      // Everything else a launch used to check — the policy, the machine, the window, the
      // cookbook — is downstream of a run existing. None does.
      return { refused: ENGINE_PENDING };
    },
  );

  const stop = defineDoor(
    defineServerAction({
      name: ACTIONS.stop,
      title: "Stop a run",
      caps: STOP_CAPS,
      requirements: STOP_REQUIREMENTS,
      input: StopInputSchema,
      result: StopResultSchema,
    }),
    async (ctx, { runId, job, reason }) => {
      const rows = await store.db.query<{
        job_id: string | null;
        machine_id: string | null;
        kind: string;
        closure: string | null;
      }>(`SELECT job_id, machine_id, kind, closure FROM runs WHERE id = ?`, [runId]);
      const run = rows[0];
      if (run === undefined) return { refused: `no run ${runId}` };
      if (run.closure !== null) return { refused: `${runId} already ended: ${run.closure}` };
      const jobId = run.job_id ?? "";
      const machineId = run.machine_id ?? "";
      if (jobId === "" || machineId === "") {
        return { refused: `${runId} has no job on a machine to stop` };
      }
      // The caller was admitted at the node it POSTED; the row says which job this run is. A
      // request that authorized one job and named another is refused rather than reconciled.
      if (job.jobId !== jobId || job.machineId !== machineId || job.operationId !== run.kind) {
        return {
          refused:
            `${runId} is ${machineId}/${run.kind}/${jobId} and this stop asks for authority ` +
            `at ${job.machineId}/${job.operationId}/${job.jobId}`,
        };
      }
      try {
        await deps.jobs(ctx).cancel(job);
      } catch (error) {
        return { refused: `${machineId} refused to stop ${jobId}: ${message(error)}` };
      }
      // The run is closed here rather than left for the loop to notice: the operator asked for
      // it to stop, and a row that kept saying `running` until the next cycle would be the
      // interface disagreeing with the act he just performed. Closing it also releases what it
      // reserved, which is why the claim is settled in the same breath.
      const at = new Date(deps.now()).toISOString();
      await store.db.run(
        `UPDATE runs SET closure = 'stopped', finished_at = ?, payload = ? WHERE id = ?`,
        [
          at,
          JSON.stringify({
            closure: "stopped",
            stoppedBy: ctx.principal.id,
            reason,
            stoppedAt: at,
          }),
          runId,
        ],
      );
      const open = await store.db.query<{ id: string; run_id: string; fence: number }>(
        `SELECT id, run_id, fence FROM claims WHERE job_id = ? AND finished_at IS NULL`,
        [jobId],
      );
      for (const claim of open) {
        await deps.coordinator.finish({
          id: claim.id,
          runId: claim.run_id,
          fence: claim.fence,
          cost: 0,
          outcome: "skipped",
        });
      }
      store.touch();
      return { runId, jobId, machineId, closure: "stopped" as const };
    },
  );

  return [launch, stop];
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
