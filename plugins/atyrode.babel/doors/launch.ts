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

  WHY NEITHER DOOR DEMANDS A NODE ANY MORE, which is the whole of whether the refusal is
  REACHABLE. A governed capability is granted at a NODE and never over a workspace (ADR 0035),
  and the host walks the requirement's target through the RAW arguments and discharges it
  BEFORE the handler is entered. `machines:run` at `atyrode.babel.explore` was exactly that —
  and no installation declares that operation any more, so the host refuses the dispatch
  "explicit version-bound consent required" at a node that cannot exist, and the operator never
  hears why nothing was started. A refusal the caller cannot reach is not a refusal.

  So `launch` asks the caller for `containers:read`, which is what the reading doors ask and
  what this door now does: it reads nothing of a machine and starts nothing. The governed
  `machines:run` requirement comes back with the node it is discharged at — Code's operation,
  once its door posts the job — and not before.

  `stop` is the same problem with a live subject: a run this deployment already started carries
  the node it was posted at, and that node is gone too. It asks `containers:write` — an act on
  this plugin's own rows, which is what closing a run and releasing its claim is — and carries
  `jobs:cancel` as a DELEGATE, the native ceiling its own job authority may reach. The hub
  still checks consent at the effect: a cancel it will not admit is refused by name, the
  handler reports that sentence and the run row stays open rather than being closed over a
  cancellation that never happened.
*/

/** A dry act on this plugin's own rows: it starts nothing, so it asks for nothing governed. */
const LAUNCH_CAPS = ["containers:read"] as const;
/**
 * …and it still carries the one DELEGATE every door a cycle follows carries. `launch` is in
 * `server.ts`'s `WAKES`, and the dispatcher attenuates `ctx.jobs` to what the door declared:
 * without `jobs:read` the cycle behind the press could read back no job, nothing would settle
 * and the fold that wake exists for would never happen (`doors/read.ts` says the whole of it).
 * The two `locations:` delegates went with the posting: they were the launched job's ceiling.
 */
const LAUNCH_DELEGATES = ["jobs:read"] as const;

/** Closing a run is a write of this plugin's rows; the cancel is the door's own ceiling. */
const STOP_CAPS = ["containers:write"] as const;
const STOP_DELEGATES = ["jobs:cancel"] as const;

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
      delegates: STOP_DELEGATES,
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
