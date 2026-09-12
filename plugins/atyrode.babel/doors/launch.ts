import { z } from "zod";
import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  INPUT_FIELD,
  LaunchRequestSchema,
  LaunchResultSchema,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  StopInputSchema,
  StopResultSchema,
  type LaunchInput,
  type OperationName,
} from "../contract.ts";
import type { Coordinator, Policy } from "../store/coordinator.ts";
import type {
  Conductor,
  JobLaunch,
  JobsSlice,
  MachineReadiness,
  Recipe,
  RunPlan,
} from "../server/conductor.ts";
import { perRunUsd, type BabelJobs } from "../server/plan.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE TWO DOORS WATCH POSTS TO: start one thing, stop one thing.

  A preset is a NAMED REQUEST rather than a set of flags (watch/api.ts): "read what's new · the
  last 1 day" instead of "explore --preparation p_3f2a --recipe … --develop 3". This file is
  where a preset becomes one job on one machine, and the mapping is the whole of it:

    read-whats-new   explore   one explore over the sessions this machine saw in the window
    explore-topic    explore   one explore over the sessions the topic's own records cite
    review-backlog   evaluate  the loop's own draw: the coordinator picks, the conductor claims
    file-and-tidy    evaluate  the same draw — §4.13's filing and backlog lanes are lanes of it
    keep-going       conductor the beat: one `scan`, whose settlement is what wakes the hub

  THE DRAWN PRESETS GO THROUGH THE CONDUCTOR RATHER THAN AROUND IT. A door that drew, claimed
  and dispatched on its own would be a second implementation of the one thing the coordinator
  exists to arbitrate — the lane, the fence, the reservation and the day's allowance — and two
  of those disagreeing is how an operator's ceiling gets spent twice. So `review-backlog` and
  `file-and-tidy` run cycles of the loop and answer with the first job the cycle asked for; the
  refusal, when there is none, is the cycle's own sentence about why nothing was drawn.

  PREVIEW EXECUTES NOTHING AND ASKS NOTHING. Watch polls it while the operator is still choosing,
  so it answers from the store and the policy alone: the profile is the one the newest receipt
  RECORDED (what actually ran last), the ceilings are the policy's, and no machine is described
  and no job is started. A preview that reached the host would make a form that is merely open
  cost round trips on a machine, and a preview that stated a profile nobody had run would be
  Watch telling the operator what it hopes will happen.

  WHY NEITHER DOOR DECLARES `machines:run`. Every governed capability — `machines:run`,
  `jobs:read`, `jobs:cancel` — has to be discharged at a REFERENCE (ADR 0035), and a door
  declaring one without `requirements` is refused outright by the dispatcher
  ("governed actions require resource targets and explicit consent", plugin-host.ts). A
  requirement's target is resolved by walking own properties of the RAW arguments — for an
  isolated plugin the engine parses the input as `z.unknown()` and the guest parses it again —
  so the machine would have to arrive as `{ kind: "machine", machineId }` in the arguments the
  Watch panel posts, and it posts `machineId` as a plain string. These doors therefore carry
  `containers:write`, like the nine acts, and the governed authority is discharged where the
  effect is: the caller's own credential at `ctx.jobs.execute` and `ctx.jobs.cancel`.
*/

/** A launch is a write into the workspace this plugin serves; the job's own authority is the caller's. */
const LAUNCH_CAPS = ["containers:write"] as const;

/** What a preset is, in one row: the run's kind, the operation it becomes, how it is started. */
type Start = "explore" | "draw" | "beat";
interface PresetPlan {
  readonly kind: "explore" | "evaluate" | "conductor" | "prepare";
  readonly operationId: OperationName;
  readonly start: Start;
}

const PRESET_PLANS: Record<LaunchInput["preset"], PresetPlan> = {
  "read-whats-new": { kind: "explore", operationId: OPERATIONS.explore, start: "explore" },
  "explore-topic": { kind: "explore", operationId: OPERATIONS.explore, start: "explore" },
  "review-backlog": { kind: "evaluate", operationId: OPERATIONS.evaluate, start: "draw" },
  "file-and-tidy": { kind: "evaluate", operationId: OPERATIONS.evaluate, start: "draw" },
  "keep-going": { kind: "conductor", operationId: OPERATIONS.scan, start: "beat" },
};

/**
 * How many catalogued sessions one explore is prepared over. A job's whole input record is
 * bounded at 65,536 bytes (`JobRequestSchema.input`), the selection is the part of the document
 * that grows with the corpus, and 120 rows of it is about 14 KB — leaving the recipe bodies the
 * room they need. A window holding more is reported as what was taken out of what was there.
 */
const MAX_SELECTION = 120;

/** The engine's own bound on one job's input record, in bytes. */
const MAX_INPUT_BYTES = 65_536;

/** How many cycles a drawn preset may run before it answers with what it got. */
const MAX_CYCLES = 8;

const DAY_MS = 24 * 60 * 60 * 1000;

/** The receipt's own profile block, as `LaunchResultSchema` states it. */
const DescribedProfileSchema = z.object({
  id: z.string().min(1),
  revision: z.number().int(),
  model: z.string().default(""),
  disclosure: z.string().default(""),
  costPer1k: z
    .object({ input: z.number().default(0), output: z.number().default(0) })
    .default({ input: 0, output: 0 }),
});

export interface LaunchDeps {
  readonly coordinator: Coordinator;
  /**
   * The cookbook this hub holds, by recipe id: what an explore may be asked to perform. It is
   * the plan's own cookbook, passed whole rather than by role, because an explore selects
   * LENSES by id and only a review selects one by the role it is drawn for.
   */
  readonly cookbook: Readonly<Record<string, Recipe>>;
  /** This dispatch's own job authority, narrowed to the verbs this plugin uses. */
  jobs(ctx: GuestCtx): BabelJobs;
  /** What a run of this operation runs under, given the policy in force. */
  plan(policy: Policy, operationId: OperationName): RunPlan;
  /** One cycle of the loop over this dispatch's slice: the same conductor the plugin wires. */
  cycle(jobs: JobsSlice, plan: RunPlan): Conductor;
  now(): number;
}

/** One catalogued session as the selection reads it; a type, so it is a row the store can hold. */
type SessionRow = {
  selector: string;
  harness: string;
  source_id: string;
  content_digest: string | null;
  snapshot_id: string | null;
  seen_at: string;
};

export function launchDoors(store: BabelStore, deps: LaunchDeps): readonly Door[] {
  /**
   * What actually ran on this machine, most recently: the profile block the newest receipt
   * there recorded. Watch states the model, the disclosure and the price per 1k from it.
   *
   * It is per machine and it is a RECORDED figure, both for the same reason. `analysis@3` is a
   * reference Code resolves — the model behind it, and its price, are the machine's Code's to
   * report — so a profile assembled from what this hub would ask for would be the one line in
   * the block nobody had checked, and a figure from another machine would be a claim about a
   * Code nobody ran. A machine that has run nothing answers `null`, which is the honest shape
   * of "it will tell us when it starts".
   */
  async function lastProfile(machineId: string): Promise<z.infer<typeof LaunchResultSchema>["profile"]> {
    const rows = await store.db.query<{ payload: string }>(
      `SELECT payload FROM runs WHERE machine_id = ? AND closure = 'completed'
        ORDER BY started_at DESC, id DESC LIMIT 20`,
      [machineId],
    );
    for (const row of rows) {
      let payload: unknown;
      try {
        payload = JSON.parse(row.payload);
      } catch {
        continue;
      }
      const block =
        payload !== null && typeof payload === "object" ? Reflect.get(payload, "profile") : null;
      const described = DescribedProfileSchema.safeParse(block);
      if (!described.success) continue;
      const { id, revision, model, disclosure, costPer1k } = described.data;
      return { id, revision, model, disclosure, costPer1k };
    }
    return null;
  }

  /** The machine, as the engine describes it, or the sentence saying why it cannot run this. */
  async function host(
    jobs: JobsSlice,
    machineId: string,
    operationId: string,
  ): Promise<{ readiness: MachineReadiness } | { refused: string }> {
    let described: MachineReadiness;
    try {
      described = await jobs.describe({ machineId, pluginId: BABEL_PLUGIN_ID });
    } catch (error) {
      return { refused: `${machineId} cannot be described: ${message(error)}` };
    }
    if (!described.connected) return { refused: `${machineId} is offline` };
    const installed = described.installation;
    if (installed === null) return { refused: `${machineId} has no Babel installed` };
    if (!installed.enabled || !installed.ready) {
      return { refused: `Babel on ${machineId} is installed but not ready to run` };
    }
    const operation = described.operations?.[operationId];
    if (operation?.ready === false) {
      return {
        refused: `${operationId} is not ready on ${machineId}${
          operation.reason === null ? "" : `: ${operation.reason}`
        }`,
      };
    }
    return { readiness: described };
  }

  /** The sessions one explore is prepared over, newest first, and how many there were. */
  async function selection(
    input: LaunchInput,
  ): Promise<{ rows: readonly SessionRow[]; available: number }> {
    const limit = MAX_SELECTION;
    if (input.preset === "explore-topic") {
      const entityId = input.entityId ?? "";
      const rows = await store.db.query<SessionRow>(
        `SELECT DISTINCT s.selector AS selector, s.harness AS harness, s.source_id AS source_id,
                s.content_digest AS content_digest, s.snapshot_id AS snapshot_id, s.seen_at AS seen_at
           FROM filings f
           JOIN edges e ON e.from_id = f.record_id AND e.kind = 'cites' AND e.to_kind = 'session'
           JOIN sessions s ON s.selector = e.to_id
          WHERE f.entity_id = ? AND f.withdrawn = 0 AND s.host = ?
          ORDER BY s.seen_at DESC, s.selector
          LIMIT ?`,
        [entityId, input.machineId, limit + 1],
      );
      return { rows: rows.slice(0, limit), available: rows.length };
    }
    const since = new Date(deps.now() - (input.sinceDays ?? 1) * DAY_MS).toISOString();
    const rows = await store.db.query<SessionRow>(
      `SELECT selector, harness, source_id, content_digest, snapshot_id, seen_at
         FROM sessions WHERE host = ? AND seen_at >= ?
        ORDER BY seen_at DESC, selector LIMIT ?`,
      [input.machineId, since, limit + 1],
    );
    return { rows: rows.slice(0, limit), available: rows.length };
  }

  /**
   * One job request, posted and recorded. The run row is written AFTER the engine accepted the
   * job and never before: a row for a job that was refused is a run an operator would wait for
   * and the loop would poll for ever.
   */
  async function post(
    jobs: JobsSlice,
    launch: JobLaunch,
    run: {
      readonly runId: string;
      readonly kind: string;
      readonly recipeId: string;
      readonly profile: RunPlan["profile"];
      readonly authorityId: string;
      readonly preparation: Record<string, unknown>;
    },
  ): Promise<{ refused: string } | null> {
    try {
      await jobs.execute(launch);
    } catch (error) {
      return { refused: `${launch.machineId} refused the job: ${message(error)}` };
    }
    const at = new Date(deps.now()).toISOString();
    await store.db.run(
      `INSERT INTO runs(id, kind, machine_id, job_id, recipe_id, profile, authority_kind,
                        authority_id, preparation, started_at, records, payload)
       VALUES (?, ?, ?, ?, ?, ?, 'operator', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        run.runId,
        run.kind,
        launch.machineId,
        launch.jobId,
        run.recipeId,
        JSON.stringify(run.profile),
        run.authorityId,
        JSON.stringify(run.preparation),
        at,
        JSON.stringify({ closure: null, requestedAt: deps.now() }),
      ],
    );
    store.touch();
    return null;
  }

  /** The launch document, checked against the ceiling the engine holds one job's input to. */
  function document(value: unknown): { input: Record<string, string> } | { refused: string } {
    const text = JSON.stringify(value);
    const bytes = new TextEncoder().encode(JSON.stringify({ [INPUT_FIELD]: text })).byteLength;
    if (bytes > MAX_INPUT_BYTES) {
      return {
        refused:
          `this launch's document is ${String(bytes)} bytes and one job's input holds ` +
          `${String(MAX_INPUT_BYTES)}: ask for fewer recipes, or a narrower window`,
      };
    }
    return { input: { [INPUT_FIELD]: text } };
  }

  const launch = defineDoor(
    defineServerAction({
      name: ACTIONS.launch,
      title: "Start a run on a machine",
      caps: LAUNCH_CAPS,
      input: LaunchRequestSchema,
      result: LaunchResultSchema,
    }),
    async (ctx, input) => {
      const preset = PRESET_PLANS[input.preset];
      const inForce = await deps.coordinator.policy();
      const policy = inForce.policy;
      const plan = deps.plan(policy, preset.operationId);
      const ceiling = { perRunUsd: perRunUsd(policy), perDayUsd: policy.dailyCost };
      const answer = {
        machineId: input.machineId,
        kind: preset.kind,
        profile: await lastProfile(input.machineId),
        ceiling,
      };
      if (input.preview) return { runId: "", jobId: "", ...answer };
      if (!policy.enabled) {
        return {
          refused:
            `the evaluation policy in force (${inForce.version}) is disabled, so Babel starts ` +
            `nothing; enable it and the launch runs under its ceilings`,
        };
      }

      const jobs = deps.jobs(ctx);
      const described = await host(jobs, input.machineId, preset.operationId);
      if ("refused" in described) return described;
      const installation = described.readiness.installation;
      const pinned =
        installation === null
          ? {}
          : {
              installationRevision: installation.revision,
              artifactSha256: installation.artifactSha256,
            };

      if (preset.start === "draw") {
        // The coordinator decides what is reviewed and the conductor claims and dispatches it;
        // this only says how many cycles the operator asked for. A cycle that draws nothing
        // ends the loop rather than spinning: the second cycle would draw the same nothing.
        const loop = deps.cycle(jobs, plan);
        const requested: { runId: string; jobId: string; machineId: string }[] = [];
        let why = "";
        for (let cycle = 0; cycle < Math.min(input.draws ?? 1, MAX_CYCLES); cycle += 1) {
          const report = await loop.tick();
          requested.push(...report.requested);
          if (report.requested.length === 0) {
            why =
              report.refused[0]?.detail ??
              report.stop?.detail ??
              report.stop?.reason ??
              "nothing was drawn";
            break;
          }
        }
        const first = requested[0];
        if (first === undefined) return { refused: `nothing to review: ${why}` };
        return { runId: first.runId, jobId: first.jobId, ...answer, machineId: first.machineId };
      }

      const minted = await ctx.newId();
      const jobId = `job_${minted}`;
      const runId = `run_${minted}`;
      const outputs = [
        { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [jobId] },
      ];

      if (preset.start === "beat") {
        // Keep going: the beat is what wakes the hub, so starting the loop is starting one
        // `scan`. `minutes` is the operator's own bound on it, under the operation's ceiling.
        const asked = (input.minutes ?? 0) * 60_000;
        const timeoutMs =
          asked > 0 ? Math.min(asked, plan.limits.timeoutMs) : plan.limits.timeoutMs;
        const built = document({
          runId,
          machineId: input.machineId,
          roots: [],
          harnesses: [],
        });
        if ("refused" in built) return built;
        const refusal = await post(
          jobs,
          {
            jobId,
            machineId: input.machineId,
            operationId: preset.operationId,
            input: built.input,
            outputs,
            limits: { ...plan.limits, timeoutMs },
            ...pinned,
          },
          {
            runId,
            kind: preset.operationId,
            recipeId: "",
            profile: plan.profile,
            authorityId: ctx.principal.id,
            preparation: { preset: input.preset, minutes: input.minutes ?? 0 },
          },
        );
        if (refusal !== null) return refusal;
        return { runId, jobId, ...answer };
      }

      // An explore performs a method, and the method is a cookbook recipe's body. This hub
      // holds no cookbook (server/plan.ts says where it is and why it is not here), so an
      // explore is refused by name rather than started with nothing to do.
      const asked = input.recipes;
      const recipes = Object.values(deps.cookbook).filter(
        (recipe) => asked.length === 0 || asked.includes(recipe.id),
      );
      if (recipes.length === 0) {
        return {
          refused:
            asked.length === 0
              ? "no cookbook recipe is installed on this hub, so an explore has no method to run"
              : `this hub holds no recipe called ${asked.join(", ")}`,
        };
      }
      const prepared = await selection(input);
      if (prepared.rows.length === 0) {
        return {
          refused:
            input.preset === "explore-topic"
              ? `no session on ${input.machineId} is cited by anything filed under ${input.entityId ?? ""}`
              : `${input.machineId} has catalogued no session in the last ${String(input.sinceDays ?? 1)} days`,
        };
      }
      const built = document({
        runId,
        machineId: input.machineId,
        engine: { binary: plan.engine.binary, args: plan.engine.args ?? [], cwd: "" },
        profile: plan.profile,
        preparation: {
          id: "",
          selection: prepared.rows.map((row) => ({
            harness: row.harness,
            sourceId: row.source_id,
            selector: row.selector,
            digest: row.content_digest ?? "",
            snapshot: row.snapshot_id ?? "",
          })),
        },
        recipes,
        stages: ["explore"],
        caps: {
          toolCalls: plan.caps.toolCalls,
          minutes: 0,
          perRunUsd: plan.caps.perRunUsd,
          idleMs: plan.caps.idleMs,
          handshakeMs: plan.caps.handshakeMs,
        },
        requireContainment: plan.requireContainment,
      });
      if ("refused" in built) return built;
      const refusal = await post(
        jobs,
        {
          jobId,
          machineId: input.machineId,
          operationId: preset.operationId,
          input: built.input,
          outputs,
          limits: plan.limits,
          ...pinned,
        },
        {
          runId,
          kind: preset.operationId,
          recipeId: recipes[0]?.id ?? "",
          profile: plan.profile,
          authorityId: ctx.principal.id,
          preparation: {
            preset: input.preset,
            selected: prepared.rows.length,
            available: prepared.available,
            ...(input.preset === "explore-topic"
              ? { entityId: input.entityId ?? "" }
              : { sinceDays: input.sinceDays ?? 1 }),
            recipes: recipes.map((recipe) => ({ id: recipe.id, version: recipe.version })),
          },
        },
      );
      if (refusal !== null) return refusal;
      return { runId, jobId, ...answer };
    },
  );

  const stop = defineDoor(
    defineServerAction({
      name: ACTIONS.stop,
      title: "Stop a run",
      caps: LAUNCH_CAPS,
      input: StopInputSchema,
      result: StopResultSchema,
    }),
    async (ctx, { runId, reason }) => {
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
      try {
        await deps.jobs(ctx).cancel({
          kind: "job",
          machineId,
          operationId: run.kind,
          jobId,
        });
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
