import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  INPUT_FIELD,
  LaunchRequestSchema,
  LaunchResultSchema,
  MATERIAL_OUTPUT,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  PRESET_OPERATIONS,
  PRESET_START,
  ProfilesQuerySchema,
  ProfilesResultSchema,
  StopInputSchema,
  StopResultSchema,
  materialFile,
  type CodeProfile,
  type LaunchInput,
  type OperationName,
  type PresetStart,
} from "../contract.ts";
import type { Coordinator, Policy } from "../store/coordinator.ts";
import {
  composeExplorePrompt,
  PARAM,
  PROMPT_VERSION,
  type PromptSession,
  type Recipe,
} from "../server/engine/prompts.ts";
import type { ActionsSlice, CodeEngine } from "../server/engine/session.ts";
import type { JobLaunch, JobsSlice, MachineReadiness, RunPlan } from "../server/conductor.ts";
import type { BabelJobs } from "../server/plan.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE THREE DOORS WATCH POSTS TO: read the saved Code profiles, start one thing, stop one thing.

  BABEL LAUNCHES NOTHING (#279). `atyrode.babel` depends on `atyrode.code`, which depends on
  `atyrode.omp`. Code owns the profiles — the model, the thinking level, the account — and Code
  posts the omp job. When the operator presses Babel's button he has already picked a saved Code
  profile (or parametrized one in Code's generator and come back), and this file turns that into
  one call of `atyrode.code.runSession` through `ctx.actions.call` on the declared dependency
  (ADR 0041). Babel's own picker, its own inference policy and its own price table are gone.

  WHAT BABEL STILL OWNS, and it is the whole of what a Babel run IS:

    the SELECTION   which of this machine's catalogued sessions the run reads, and which it may
                    not (`live`, and Babel's own transcripts — #262)
    the MATERIAL    that selection SEALED, as `prepare`'s own second output, so the session's
                    sandbox can bind it read-only at `/inputs/material`
    the PROMPT      the recipes, the answering protocol, the stage's schema and the material's
                    layout (`server/engine/prompts.ts`)
    the RUN ROW     one row per run, carrying Code's job, the container that answered and the
                    `prepare` job whose material it read

  WHICH PRESET BECOMES WHAT:

    read-whats-new   explore   one Code session over the sessions this machine saw in the window
    explore-topic    explore   one Code session over the sessions the topic's own records cite
    review-backlog   evaluate  a DRAWN review: refused by name — see below
    file-and-tidy    evaluate  the same draw
    keep-going       conductor the beat: one `atyrode.babel.scan`, Babel's own job, posted here

  THE DRAWN PRESETS ARE REFUSED BY NAME, and the refusal is honest rather than provisional. A
  review is drawn by the coordinator — the lane, the fence, the reservation, the day's allowance
  — and dispatched with a BLINDED projection of the record under review. The revert that made a
  run a Code session (#290) took the conductor's dispatch and that projection with it, and a door
  that drew and posted on its own would be a second implementation of the one thing the
  coordinator exists to arbitrate. So `review-backlog` and `file-and-tidy` answer
  {@link DRAW_PENDING}, which names the lane rather than pretending the engine is missing.

  ONE REFUSAL STANDS BETWEEN A COMPOSED RUN AND A POSTED ONE, and it is Manifold's:
  `MATERIAL_INPUT_PENDING` (`contract.ts`). Everything above the post is done and durable — the
  selection chosen, the material sealed, the prompt composed, the profile named — and what is
  missing is the primitive that binds one job's sealed output into another plugin's job. The
  refusal is raised inside `server/engine/session.ts`, at the one line that moves when the pin
  does, so the operator's button and the drain's fan hear one sentence rather than two halves.

  WHY `launch` DEMANDS NO NODE. A governed capability is granted at a NODE and never over a
  workspace (ADR 0035), and the host walks the requirement's target through the RAW arguments
  and discharges it BEFORE the handler is entered. `machines:run` at `atyrode.babel.explore` was
  exactly that — and no installation declares that operation any more, because the job a run
  becomes is CODE's, posted under `atyrode.omp`'s own operation and governed at Code's node by
  Code's own door. A requirement here would be consent at a node that cannot exist, and a
  refusal the caller cannot reach is not a refusal.

  The beat is the one job this file still posts itself, and it posts it as `atyrode.babel.scan`
  — an operation this manifest DOES declare. `machines:run` for it is discharged where every
  other job of this plugin's discharges it: at the effect, by `engine.jobs.execute`, against the
  authority this door's delegates carry.
*/

/** A dry act on this plugin's own rows plus one call onto Code, which reads containers. */
const LAUNCH_CAPS = ["containers:read"] as const;
/**
 * The native ceiling the jobs this door posts inherit: reading them back, and the locations the
 * `prepare` it posts declares. `jobs:read` is also the one DELEGATE every door a cycle follows
 * carries — `launch` is in `server.ts`'s `WAKES`, and the dispatcher attenuates `ctx.jobs` to
 * what the door declared, so without it the cycle behind the press could read back no job,
 * nothing would settle and the fold that wake exists for would never happen.
 */
const LAUNCH_DELEGATES = ["jobs:read", "locations:read", "locations:write"] as const;

/** Reading Code's saved profiles is a read of containers and nothing else. */
const PROFILES_CAPS = ["containers:read"] as const;

/** Closing a run is a write of this plugin's rows; the cancel is the door's own ceiling. */
const STOP_CAPS = ["containers:write"] as const;
const STOP_DELEGATES = ["jobs:cancel"] as const;

/** What a preset is, in one row: the run's kind, the operation it becomes, how it is started. */
interface PresetPlan {
  readonly kind: "explore" | "evaluate" | "conductor" | "prepare";
  readonly operationId: OperationName;
  readonly start: PresetStart;
}

const PRESET_PLANS: Record<LaunchInput["preset"], PresetPlan> = {
  "read-whats-new": { kind: "explore", operationId: PRESET_OPERATIONS["read-whats-new"], start: PRESET_START["read-whats-new"] },
  "explore-topic": { kind: "explore", operationId: PRESET_OPERATIONS["explore-topic"], start: PRESET_START["explore-topic"] },
  "review-backlog": { kind: "evaluate", operationId: PRESET_OPERATIONS["review-backlog"], start: PRESET_START["review-backlog"] },
  "file-and-tidy": { kind: "evaluate", operationId: PRESET_OPERATIONS["file-and-tidy"], start: PRESET_START["file-and-tidy"] },
  "keep-going": { kind: "conductor", operationId: PRESET_OPERATIONS["keep-going"], start: PRESET_START["keep-going"] },
};

/**
 * WHY A DRAWN REVIEW IS NOT STARTED HERE. It is not the engine that is missing — the engine is
 * Code and this file calls it — it is the coordinator's dispatch and the blinded projection the
 * revert removed with Babel's own launcher. Naming the lane is what lets an operator schedule
 * against it instead of pressing again.
 */
export const DRAW_PENDING =
  "draw_pending: a review is drawn by the coordinator and dispatched with a blinded projection " +
  "of the record under review, and that dispatch is not on this build: the revert that made a " +
  "Babel run a Code session (#290) removed it with Babel's own launcher. The explore lane runs " +
  "through Code; the evaluate lane returns with the coordinator's dispatch (#268).";

/**
 * How many catalogued sessions one run is prepared over. The `prepare` job's whole input record
 * is bounded at 65,536 bytes (`JobRequestSchema.input`) and the selectors are the part of that
 * document which grows with the corpus; 120 of them is about 8 KB. A window holding more is
 * reported as what was taken out of what was there.
 */
const MAX_SELECTION = 120;

/**
 * HOW MANY BYTES OF LOG ONE PREPARATION MAY SEAL, and why it is 512 MiB.
 *
 * The count above bounds the REQUEST; this bounds the OUTPUT, and they are different failures.
 * `prepare` seals the normalized record stream of every selected session into the material
 * lease, and a lease over the operation's `limits.outputBytes` is refused by the machine —
 * after it has read every one of those logs. The operator's remedy is a narrower window, and
 * he cannot guess it from an `output_too_large` on a job that already spent twenty minutes.
 *
 * THE NUMBER IS THE POST-MORTEM'S OWN. A catalogued session averages ~12 MB, and the two logs
 * that broke 2026-09-13 were 35 MB (a harness transcript) and 240 MB (a live Code session);
 * 120 sessions at that average is ~1.4 GB, so the count alone bounds nothing. 512 MiB holds
 * about forty average sessions, or two of the largest the corpus has ever held, and it is
 * under the gigabyte this plugin's own database is allowed — a machine that cannot spare half
 * a gigabyte of tmpfs for a lease cannot run this operation at all. The normalized stream is
 * SMALLER than the log it came from (one canonical record per line, no whitespace), so this is
 * a conservative bound on what is actually written.
 *
 * It is checked against the catalogued `size` — what `scan` measured — because that is the
 * only figure the hub has before the job runs. It must stay at or below the `outputBytes` the
 * manifest declares for `atyrode.babel.prepare`, and `test/contract.test.ts` pins that.
 */
export const MAX_MATERIAL_BYTES = 512 * 1024 * 1024;

/** The engine's own bound on one job's whole input record, in bytes. */
const MAX_INPUT_BYTES = 65_536;

const DAY_MS = 24 * 60 * 60 * 1000;

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
 * gets spent twice — which is the whole subject of the post-mortem this lane comes from.
 */
export interface LaunchMachinery {
  /**
   * One explore, as a Code session over sealed material. The `engine` is a parameter rather than
   * a dep because it is built from the CALLER's own `actions` slice: a settlement's hook and an
   * operator's dispatch reach Code under different authority, and the one that reaches it is the
   * one whose principal Code grades.
   */
  startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    engine: CodeEngine,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
  /** One beat — an `atyrode.babel.scan`, Babel's own job. It reaches no model and needs no Code. */
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
  /** Babel's side of Code's doors, over the authority of whoever is asking (ADR 0041). */
  engine(actions: ActionsSlice | undefined): CodeEngine;
  /**
   * THE COOKBOOK THIS HUB HOLDS, by recipe id: the methods an explore may be asked to perform.
   *
   * It is READ rather than held, because it is store state: the recipes live in the policy
   * document the operator installed — the same block Watch's Recipes section lists — and a
   * record captured when the server half was constructed would be the cookbook of whichever
   * policy happened to be in force at enable time. A hub whose policy names none is refused by
   * name below, which is the honest answer and not an empty run.
   */
  cookbook(): Promise<Readonly<Record<string, Recipe>>>;
  /** What a run of this operation runs under, given the policy in force. */
  plan(policy: Policy, operationId: OperationName): RunPlan;
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
  /** What `scan` measured the log at. The only figure the hub has before `prepare` runs. */
  size: number | bigint | null;
};

/** What one preset's window offered: the scope, its size, and what it was not allowed to read. */
interface Selected {
  readonly rows: readonly SessionRow[];
  /** Every catalogued session the window held, selectable or not. */
  readonly held: number;
  /** How many of those are live or Babel's own, and so were never candidates (#262). */
  readonly excluded: number;
  /** The catalogued bytes of the rows actually taken, which bounds the sealed material. */
  readonly bytes: number;
  /** How many selectable sessions the byte bound left out, on top of `MAX_SELECTION`. */
  readonly overBound: number;
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

export function launchMachinery(store: BabelStore, deps: LaunchDeps): LaunchMachinery {
  /**
   * The sessions one run is prepared over, newest first, how many the window held, and how many
   * of those a preparation may not read.
   *
   * WHAT IT NEVER SELECTS (#262). A row `scan` marked `live` is a log that was still being
   * appended when it was catalogued: its digest is already stale and a run reading it would
   * report "changed since the preparation was fixed", which is what every explore of 2026-09-13
   * reported. That one is unconditional — a moving file is not a scope. A row marked
   * `kind = 'agent'` is one of Babel's own runs' transcripts, catalogued and archived like every
   * other session (#177) and left out of a preset that reads the operator's work; `agentSessions`
   * is how a preset that studies Babel itself (#270) asks for them.
   *
   * `held` is what the window CONTAINED, exclusions included, so `excluded` is a number both the
   * run row and the refusal can state: "there is nothing here" and "there is nothing here a run
   * may read" are different facts, and the day this lane comes from was two hours of reading an
   * adjacent number as the one that was asked for.
   */
  async function selection(input: LaunchInput): Promise<Selected> {
    const cited =
      `FROM filings f
         JOIN edges e ON e.from_id = f.record_id AND e.kind = 'cites' AND e.to_kind = 'session'
         JOIN sessions s ON s.selector = e.to_id
        WHERE f.entity_id = ? AND f.withdrawn = 0 AND s.host = ?`;
    const recent = `FROM sessions s WHERE s.host = ? AND s.seen_at >= ?`;
    const topic = input.preset === "explore-topic";
    const scope = topic ? cited : recent;
    const params: readonly string[] = topic
      ? [input.entityId ?? "", input.machineId]
      : [input.machineId, new Date(deps.now() - (input.sinceDays ?? 1) * DAY_MS).toISOString()];
    const allowed = `AND s.live = 0${input.agentSessions ? "" : " AND s.kind = 'operator'"}`;

    const rows = await store.db.query<SessionRow>(
      `SELECT DISTINCT s.selector AS selector, s.harness AS harness, s.source_id AS source_id,
              s.content_digest AS content_digest, s.snapshot_id AS snapshot_id,
              s.seen_at AS seen_at, s.size AS size
         ${scope} ${allowed}
        ORDER BY s.seen_at DESC, s.selector
        LIMIT ?`,
      [...params, MAX_SELECTION + 1],
    );
    const counted = await store.db.query<{ held: number; selectable: number }>(
      `SELECT count(DISTINCT s.selector) AS held,
              count(DISTINCT CASE WHEN s.live = 0${input.agentSessions ? "" : " AND s.kind = 'operator'"}
                                  THEN s.selector END) AS selectable
         ${scope}`,
      [...params],
    );
    const held = Number(counted[0]?.held ?? 0);
    const selectable = Number(counted[0]?.selectable ?? 0);

    /*
      NEWEST FIRST UNTIL THE MATERIAL IS FULL. The count is not a bound on bytes: 120 sessions
      at the corpus's own average is over a gigabyte, and a selection that overran
      `prepare`'s `outputBytes` would be discovered by the machine AFTER it had read every one
      of those logs. Taking rows in the order they are already sorted — newest first, which is
      what every preset asks for — stops at the bound and SAYS how many it left, so the
      operator reads a narrower window rather than an `output_too_large`.

      A row with no catalogued size counts as nothing: `scan` measured every log it catalogued,
      so a NULL is an imported row, and refusing the run for a figure the crossing never
      carried would make old corpora unusable. It is the one place this bound is approximate,
      and `prepare` still refuses the lease if the seal really does overrun.
    */
    const taken: SessionRow[] = [];
    let bytes = 0;
    let overBound = 0;
    for (const row of rows.slice(0, MAX_SELECTION)) {
      const size = Number(row.size ?? 0);
      if (bytes + size > MAX_MATERIAL_BYTES) {
        overBound += 1;
        continue;
      }
      taken.push(row);
      bytes += size;
    }
    return { rows: taken, held, excluded: Math.max(0, held - selectable), bytes, overBound };
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
      `INSERT INTO runs(id, kind, machine_id, job_id, recipe_id, authority_kind,
                        authority_id, preparation, started_at, records, payload)
       VALUES (?, ?, ?, ?, ?, 'operator', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        run.runId,
        run.kind,
        launch.machineId,
        launch.jobId,
        run.recipeId,
        run.authorityId,
        JSON.stringify(run.preparation),
        at,
        JSON.stringify({ closure: null, requestedAt: deps.now() }),
      ],
    );
    store.touch();
    return null;
  }

  /**
   * The launch document, checked against the ceiling the engine holds one job's whole input
   * record to. A document over it is refused with the number, because the remedy — fewer
   * sessions, a narrower window — is the operator's and he cannot guess it from a machine's
   * `input_too_large`.
   */
  function document(value: unknown): { input: Record<string, string> } | { refused: string } {
    const input: Record<string, string> = { [INPUT_FIELD]: JSON.stringify(value) };
    const bytes = new TextEncoder().encode(JSON.stringify(input)).byteLength;
    if (bytes > MAX_INPUT_BYTES) {
      return {
        refused:
          `this launch's document is ${String(bytes)} bytes and one job's input holds ` +
          `${String(MAX_INPUT_BYTES)}: ask for a narrower window`,
      };
    }
    return { input };
  }

  /** The pins a posting carries, or the sentence naming why this machine cannot run this. */
  async function ready(
    jobs: JobsSlice,
    machineId: string,
    operationId: string,
  ): Promise<
    { pinned: { installationRevision?: string; artifactSha256?: string } } | { refused: string }
  > {
    const described = await host(jobs, machineId, operationId);
    if ("refused" in described) return described;
    const installation = described.readiness.installation;
    return {
      pinned:
        installation === null
          ? {}
          : {
              installationRevision: installation.revision,
              artifactSha256: installation.artifactSha256,
            },
    };
  }

  async function startBeat(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started> {
    const preset = PRESET_PLANS[input.preset];
    const admitted = await ready(jobs, input.machineId, preset.operationId);
    if ("refused" in admitted) return admitted;
    // Keep going: the beat is what wakes the hub, so starting the loop is starting one `scan`.
    // `minutes` is the operator's own bound on it, under the operation's ceiling.
    const asked = (input.minutes ?? 0) * 60_000;
    const timeoutMs = asked > 0 ? Math.min(asked, plan.limits.timeoutMs) : plan.limits.timeoutMs;
    const built = document({
      runId: identity.runId,
      machineId: input.machineId,
      roots: [],
      harnesses: [],
    });
    if ("refused" in built) return built;
    const refusal = await post(
      jobs,
      {
        jobId: identity.jobId,
        machineId: input.machineId,
        operationId: preset.operationId,
        input: built.input,
        outputs: [
          { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [identity.jobId] },
        ],
        limits: { ...plan.limits, timeoutMs },
        ...admitted.pinned,
      },
      {
        runId: identity.runId,
        kind: preset.operationId,
        recipeId: "",
        authorityId: identity.authorityId,
        preparation: { preset: input.preset, minutes: input.minutes ?? 0 },
      },
    );
    if (refusal !== null) return refusal;
    return { runId: identity.runId, jobId: identity.jobId };
  }

  /**
   * ONE EXPLORE, IN THE FIVE STEPS A BABEL RUN IS MADE OF.
   *
   *   1. the SELECTION, from this machine's catalog and nothing else;
   *   2. the RECIPES, the methods this hub holds;
   *   3. the MATERIAL: one `atyrode.babel.prepare` job, posted here, which seals the selection as
   *      its own output — this is the run's evidence and it exists before the session does;
   *   4. the PROMPT, composed around `/inputs/material`;
   *   5. the SESSION: `atyrode.code.runSession`, and the run row that records Code's job, the
   *      container that answered and the `prepare` job whose material it read.
   *
   * Step 5 is what {@link MATERIAL_INPUT_PENDING} still refuses, and the order is deliberate: the
   * material is sealed FIRST and its job id recorded, so when the pin moves the only thing that
   * has to happen is the binding. A run whose session was refused leaves a `prepare` job that ran
   * and a run row that says what it prepared, which is the catalog work Babel does anyway and not
   * a ghost: the conductor ingests its sessions and its receipt like any other job's.
   */
  async function startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    engine: CodeEngine,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started> {
    const profile = input.profile;
    if (profile === undefined) {
      return {
        refused:
          `profile_required: ${input.preset} reaches a model, and a model is a Code profile's. ` +
          `Pick a saved profile — or parametrize one in Code's generator and come back — and the ` +
          `launch carries its container and the revision you were shown.`,
      };
    }
    // An explore performs a method, and the method is a cookbook recipe's body.
    const asked = input.recipes;
    const recipes = Object.values(await deps.cookbook()).filter(
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
      // A window can hold sessions and offer none, in three ways that need three answers. A
      // log still being written, or one of Babel's own runs', is catalogued and not a
      // candidate (#262); a session larger than the whole material bound is a candidate the
      // lease cannot hold. Which of the three it is decides what the operator does next.
      if (prepared.overBound > 0) {
        return {
          refused:
            `material_too_large: every session this window offers is larger than the ` +
            `${String(Math.round(MAX_MATERIAL_BYTES / (1024 * 1024)))} MiB one preparation may ` +
            `seal (${String(prepared.overBound)} left out). A run reads what a job's sealed ` +
            `output can hold; ask for a narrower window, or archive the log that is too big to ` +
            `read in one piece.`,
        };
      }
      const left =
        prepared.excluded === 0
          ? ""
          : ` (${String(prepared.excluded)} of ${String(prepared.held)} catalogued there are ` +
            `still being written or Babel's own runs', which a preparation does not read)`;
      return {
        refused:
          input.preset === "explore-topic"
            ? `no session on ${input.machineId} is cited by anything filed under ${input.entityId ?? ""}${left}`
            : `${input.machineId} has catalogued no session in the last ${String(input.sinceDays ?? 1)} days${left}`,
      };
    }

    // THE MATERIAL IS ITS OWN JOB, and it is Babel's: `prepare` reads the machine's logs, seals
    // the normalized record stream per session and writes the index a citation's digest comes
    // from. Its id is DERIVED from the run's so a retried start posts the same preparation
    // rather than a second one over the same sessions.
    const prepareJobId = `${identity.jobId}_material`;
    const admitted = await ready(jobs, input.machineId, OPERATIONS.prepare);
    if ("refused" in admitted) return admitted;
    const built = document({
      runId: `${identity.runId}_material`,
      machineId: input.machineId,
      selectors: prepared.rows.map((row) => row.selector),
      ...(input.agentSessions === undefined ? {} : { agentSessions: input.agentSessions }),
    });
    if ("refused" in built) return built;
    const sealed = await post(
      jobs,
      {
        jobId: prepareJobId,
        machineId: input.machineId,
        operationId: OPERATIONS.prepare,
        input: built.input,
        outputs: [
          { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [prepareJobId] },
          {
            name: MATERIAL_OUTPUT,
            locationId: OUTPUT_LOCATION,
            components: [prepareJobId, MATERIAL_OUTPUT],
          },
        ],
        limits: plan.limits,
        ...admitted.pinned,
      },
      {
        runId: `${identity.runId}_material`,
        kind: OPERATIONS.prepare,
        recipeId: "",
        authorityId: identity.authorityId,
        preparation: {
          preset: input.preset,
          for: identity.runId,
          selected: prepared.rows.length,
          available: prepared.held,
          excluded: prepared.excluded,
          bytes: prepared.bytes,
          overBound: prepared.overBound,
        },
      },
    );
    if (sealed !== null) return sealed;

    // THE PROMPT SAYS ONLY WHAT IS ALREADY TRUE. `prepare` runs AFTER this posting, so the
    // digests, the record counts and the sizes are not known here and the prompt does not
    // invent them: it names the selector and the file the material will hold it in, and sends
    // the model to `index.json` for everything a citation needs. Both sides derive the file
    // name from the same ordered selectors with `materialFile`, which is what makes the two
    // agree without a round trip.
    const entries: PromptSession[] = prepared.rows.map((row, ordinal) => ({
      selector: row.selector,
      file: materialFile(ordinal, row.selector),
    }));
    const prompt = composeExplorePrompt({
      stage: "explore",
      recipes,
      sessions: entries,
      // The preparation's own id is content-addressed by `prepare` and is not known until it has
      // run; the prompt names the job that seals it, which is the identity a reader can follow.
      preparationId: prepareJobId,
      params: {
        [PARAM.stage]: "explore",
        [PARAM.runId]: identity.runId,
        [PARAM.preparation]: prepareJobId,
      },
    });

    const posted = await engine.runSession({
      profile,
      machineId: input.machineId,
      prompt,
      prepareJobId,
    });
    if (!posted.ok) return { refused: posted.refused };
    await recordSession(identity, input, profile, posted.value.jobId, prepareJobId, {
      preset: input.preset,
      selected: prepared.rows.length,
      available: prepared.held,
      excluded: prepared.excluded,
      bytes: prepared.bytes,
      overBound: prepared.overBound,
      promptVersion: PROMPT_VERSION,
      sessions: entries,
      recipes: recipes.map((recipe) => ({ id: recipe.id, version: recipe.version })),
      ...(input.preset === "explore-topic"
        ? { entityId: input.entityId ?? "" }
        : { sinceDays: input.sinceDays ?? 1 }),
    });
    return { runId: identity.runId, jobId: posted.value.jobId };
  }

  /**
   * The run row of a posted Code session. It is written after Code accepted the job, for the
   * reason {@link post} writes its own then: a row for a session nobody posted is a run an
   * operator waits for for ever.
   *
   * `job_id` is CODE's job and `container_id` is the workspace whose profile answered it. The
   * pair is not decoration: `code.readSession` takes exactly those two, and they are the whole of
   * how the conductor reconciles a job `ctx.jobs` refuses to read because it belongs to
   * `atyrode.omp`.
   *
   * `profile` is BABEL'S OWN LAUNCH REPORT — the container it named, and the account the
   * request named when it named one. Code chooses the account and its session receipt reports
   * none, so this column is the only place "which window did that run spend" is written; a
   * drain's total is the sum of the runs that named its account (#267), and on 2026-09-13
   * nothing on the machine could answer that question at all.
   */
  async function recordSession(
    identity: LaunchIdentity,
    input: LaunchInput,
    profile: CodeProfile,
    jobId: string,
    prepareJobId: string,
    preparation: Record<string, unknown>,
  ): Promise<void> {
    const session = input.session;
    await store.db.run(
      `INSERT INTO runs(id, kind, machine_id, job_id, container_id, prepare_job_id, recipe_id,
                        profile, authority_kind, authority_id, preparation, started_at, records,
                        payload)
       VALUES (?, ?, ?, ?, ?, ?, '', ?, 'operator', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        identity.runId,
        PRESET_PLANS[input.preset].operationId,
        input.machineId,
        jobId,
        profile.containerId,
        prepareJobId,
        JSON.stringify({
          containerId: profile.containerId,
          expectedRevision: profile.expectedRevision,
          ...(session === undefined
            ? {}
            : {
                account: {
                  provider: session.account.provider,
                  identityKey: session.account.identityKey,
                },
                askedModel: session.model,
              }),
        }),
        identity.authorityId,
        JSON.stringify(preparation),
        new Date(deps.now()).toISOString(),
        JSON.stringify({ closure: null, requestedAt: deps.now() }),
      ],
    );
    store.touch();
  }

  return { startExplore, startBeat };
}

export function launchDoors(store: BabelStore, deps: LaunchDeps): readonly Door[] {
  const machinery = launchMachinery(store, deps);

  const profiles = defineDoor(
    defineServerAction({
      name: ACTIONS.profiles,
      title: "The saved Code profiles",
      caps: PROFILES_CAPS,
      input: ProfilesQuerySchema,
      result: ProfilesResultSchema,
    }),
    async (ctx) => {
      const answered = await deps.engine(ctx.actions).profiles();
      // BOTH HALVES ARE ANSWERS. A hub with Code disabled and an operator who has saved no
      // profile are different situations with different remedies, and an empty list with no
      // word beside it reads as the second when it is the first.
      return answered.ok
        ? { profiles: [...answered.value], unavailable: "" }
        : { profiles: [], unavailable: answered.refused };
    },
  );

  const launch = defineDoor(
    defineServerAction({
      name: ACTIONS.launch,
      title: "Start a run on a machine",
      caps: LAUNCH_CAPS,
      delegates: LAUNCH_DELEGATES,
      input: LaunchRequestSchema,
      result: LaunchResultSchema,
    }),
    async (ctx, input) => {
      const preset = PRESET_PLANS[input.preset];
      // The `operation` field is what a governed requirement WOULD be discharged at, and this is
      // the only place that can say the node is the one this request is actually about. A request
      // whose two halves disagree is answered as itself rather than folded into a later refusal:
      // the operator fixes a mismatched node, and hears nothing about it if a broader sentence
      // covers it.
      if (
        input.operation.machineId !== input.machineId ||
        input.operation.operationId !== preset.operationId
      ) {
        return {
          refused:
            `this launch names ${input.machineId}/${preset.operationId} and asks for authority ` +
            `at ${input.operation.machineId}/${input.operation.operationId}`,
        };
      }
      const inForce = await deps.coordinator.policy();
      if (!inForce.policy.enabled) {
        return {
          refused:
            `the evaluation policy in force (${inForce.version}) is disabled, so Babel starts ` +
            `nothing; enable it and the launch runs under its ceilings`,
        };
      }
      if (preset.start === "draw") return { refused: DRAW_PENDING };

      const minted = await ctx.newId();
      const identity: LaunchIdentity = {
        runId: `run_${minted}`,
        jobId: `job_${minted}`,
        authorityId: ctx.principal.id,
      };
      const jobs = deps.jobs(ctx);
      const plan = deps.plan(inForce.policy, preset.operationId);
      const started =
        preset.start === "beat"
          ? await machinery.startBeat(identity, jobs, input, plan)
          : await machinery.startExplore(identity, jobs, deps.engine(ctx.actions), input, plan);
      if ("refused" in started) return started;
      return { ...started, machineId: input.machineId, kind: preset.kind };
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

  return [profiles, launch, stop];
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
