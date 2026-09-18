import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
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
  MaterialIndexSchema,
  type CodeProfile,
  type LaunchInput,
  type MaterialIndex,
  type OperationName,
  type PresetStart,
} from "../contract.ts";
import type { Coordinator, Policy } from "../store/coordinator.ts";
import {
  composeExplorePrompt,
  PARAM,
  PROMPT_VERSION,
  type Recipe,
} from "../server/engine/prompts.ts";
import { CODE_PLUGIN_ID } from "@atyrode/manifold-code";
import {
  PROMPT_LIMIT,
  promptBytes,
  type ActionsSlice,
  type CodeEngine,
} from "../server/engine/session.ts";
import {
  describeHost,
  type JobLaunch,
  type JobsSlice,
  type RunPlan,
} from "../server/conductor.ts";
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
    review-backlog   evaluate  a policy-managed draw; this door does not select its own work
    file-and-tidy    evaluate  the same draw
    keep-going       conductor the beat: one `atyrode.babel.scan`, Babel's own job, posted here

  THE DRAWN PRESETS ARE POLICY-MANAGED. A review is drawn by the coordinator — the lane, the
  fence, the reservation, the day's allowance — and dispatched with a blinded projection of
  the record under review. A door that selected one on demand would be a second implementation
  of the one thing the coordinator exists to arbitrate. So `review-backlog` and `file-and-tidy`
  answer {@link DRAW_MANAGED}: the conductor now dispatches them automatically under the
  installed review route, on its cadence or the next wake.
  A RUN IS STARTED IN TWO WAKES, and Manifold's own rule is why. A job input binds a SETTLED
  job's sealed output (ADR 0044): a binding whose source is still active is refused, and
  `prepare` is running the instant it is posted. So the press seals the material and records
  the run's intent, and {@link LaunchMachinery.postPrepared} — reached from the cycle after
  the conductor settled that preparation — composes the prompt from the material's own index
  and posts the session. The operator's button and the drain's fan take the same two wakes,
  because there is one launch path.

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
 * The native ceiling the jobs this door posts inherit: reading them back, the locations the
 * `prepare` it posts declares, and the read that says whether the machine can run any of it.
 *
 * `jobs:read` is also the one DELEGATE every door a cycle follows carries — `launch` is in
 * `server.ts`'s `WAKES`, and the dispatcher attenuates `ctx.jobs` to what the door declared, so
 * without it the cycle behind the press could read back no job, nothing would settle and the
 * fold that wake exists for would never happen.
 *
 * `machines:read` IS WHAT A PRESS ASKS BEFORE IT POSTS ANYTHING. `ready` describes the machine
 * the request names — connected, this plugin installed and ready, the operation not reported
 * unready — and pins the installation revision and artifact digest the posting carries, and all
 * of that is one `engine.jobs.describe`, a read that moved onto this narrower word
 * (atyrode/manifold#736) and became delegable with atyrode/manifold#740. Without it the bridge
 * refuses the describe, `describeHost` answers `cannot be described:
 * job_capability_absent:machines:read`, and the operator's press is refused before the machine
 * is ever asked — whatever authority his own key holds, because a delegate is the door's
 * ceiling and not the caller's grant. `doors/read.ts` carries the whole reasoning.
 */
const LAUNCH_DELEGATES = [
  "jobs:read",
  "locations:read",
  "locations:write",
  "machines:read",
] as const;

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
 * A drawn review is scheduled by the conductor rather than selected by this operator door.
 * `keep-going` supplies an immediate wake; the durable schedule supplies every later one.
 */
export const DRAW_MANAGED =
  "draw_managed: drawn reviews are dispatched automatically by the conductor under the " +
  "installed evaluation policy, Code profile and blinded projection. This launch door does " +
  "not bypass that shared claim and budget; use keep-going for an immediate wake or wait for " +
  "the policy cadence.";

/**
 * How many catalogued sessions one run is prepared over. The `prepare` job's whole input record
 * is bounded at 65,536 bytes (`JobRequestSchema.input`) and the selectors are the part of that
 * document which grows with the corpus; 120 of them is about 8 KB. A window holding more is
 * reported as what was taken out of what was there.
 */
const MAX_SELECTION = 120;

/**
 * HOW MANY BYTES OF LOG ONE PREPARATION MAY SEAL, and why it is 448 MiB under a 512 MiB job.
 *
 * The count above bounds the REQUEST; this bounds the OUTPUT, and they are different failures.
 * `prepare` seals the normalized record stream of every selected session into the material
 * lease, and a lease over the operation's `limits.outputBytes` is refused by the machine —
 * after it has read every one of those logs. The operator's remedy is a narrower window, and
 * he cannot guess it from an `output_too_large` on a job that already spent twenty minutes.
 *
 * THE NUMBER IS THE POST-MORTEM'S OWN. A catalogued session averages ~12 MB, and the two logs
 * that broke 2026-09-13 were 35 MB (a harness transcript) and 240 MB (a live Code session);
 * 120 sessions at that average is ~1.4 GB, so the count alone bounds nothing. The declared
 * job is 512 MiB, which holds about forty average sessions or two of the largest the corpus
 * has ever held, and is under the gigabyte this plugin's own database is allowed — a machine
 * that cannot spare half a gigabyte of tmpfs for a lease cannot run this operation at all.
 *
 * AND `outputBytes` IS THE AGGREGATE, WHICH IS WHY THIS IS NOT THAT NUMBER. The owner seals a
 * job's outputs against ONE running budget — `remainingBytes = limits.outputBytes`, minus
 * stdout, minus stderr, minus each sealed lease in turn, and a negative remainder is
 * `output_collection_refused` (`agent/src/job-owner.ts`). This operation writes TWO leases,
 * `outputs` and `material`, and each is sealed as a POSIX ustar archive: 512 bytes of header
 * plus padding to 512 for every member, and a 1,024-byte trailer. A selection admitted at
 * exactly the job's bound would therefore pack to the bound and be refused at the seal, after
 * the full read — the very failure this constant exists to move before the post.
 *
 * So the selection gets 87.5% of the job and the remaining 64 MiB is the framing, the
 * ordinary `outputs` lease and the two byte streams. `test/contract.test.ts` pins the
 * inequality with a margin, not just `<=`.
 *
 * It is checked against the catalogued `size` — what `scan` measured — because that is the
 * only figure the hub has before the job runs.
 */
export const MAX_MATERIAL_BYTES = 448 * 1024 * 1024;

/**
 * THE PREPARATION'S JOB ID, derived from the run's own so a retried start posts the same
 * preparation rather than a second one over the same sessions — and so the controller that
 * has to cancel it can name it without having kept the answer of the call that posted it.
 */
export function materialJobId(jobId: string): string {
  return `${jobId}_material`;
}

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

/**
 * What {@link LaunchMachinery.postPrepared} did about one waiting run: the Code job it posted,
 * or the sentence the run was closed with. Both are reported, because a wake nobody watched
 * has to leave its account on the row AND in the cycle's notes.
 */
export type Posted =
  | { readonly runId: string; readonly jobId: string }
  | { readonly runId: string; readonly refused: string };

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
  /**
   * EVERY RUN WHOSE MATERIAL IS SEALED AND WHOSE SESSION IS NOT POSTED YET, posted now.
   *
   * It is a second wake and not a continuation of the first because Manifold's job-inputs
   * primitive binds a SETTLED job's output (#592): the session cannot be posted while its own
   * preparation is still running. Called from the cycle, after the conductor has settled what
   * finished and before the drain decides whether to launch more.
   */
  postPrepared(jobs: JobsSlice, engine: CodeEngine, plan: RunPlan): Promise<readonly Posted[]>;
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
    const described = await describeHost(jobs, machineId, operationId);
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
   *      its own output — this is the run's evidence and it exists before the session does.
   *
   * …and the press stops there. Steps 4 and 5 — the PROMPT, composed around `/inputs/material`,
   * and the SESSION, `atyrode.code.runSession` with that material bound — belong to
   * {@link LaunchMachinery.postPrepared}, because the binding names a job that has SETTLED and
   * the preparation is still running here. Composing the prompt there is the better half of
   * that constraint: it is built from the index `prepare` actually wrote, with the real file
   * names and the digests a citation has to copy, rather than from selectors this end could
   * only guess the layout of.
   */
  async function startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    _engine: CodeEngine,
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
    const prepareJobId = materialJobId(identity.jobId);
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

    /*
      AND THE SESSION IS NOT POSTED HERE (#592). The job-inputs primitive binds a SETTLED
      job's sealed output: a binding whose source is still active is refused, and `prepare` is
      still running at this point — it was posted one statement ago. So the press ends with
      the preparation in flight and the run recorded as INTENT, and the session is posted by
      {@link postPrepared} on the wake that `prepare`'s own settlement causes.

      That is also why the prompt is not composed here. Composed now it could only name the
      selectors and guess the file names; composed on the settle it is built from the
      material's own index — the real file names, the real record counts, and the digests a
      citation has to copy — which is the document the hub then checks those citations
      against.
    */
    await store.db.run(
      `INSERT INTO runs(id, kind, machine_id, container_id, prepare_job_id, recipe_id, profile,
                        authority_kind, authority_id, preparation, started_at, records, payload)
       VALUES (?, ?, ?, ?, ?, '', ?, 'operator', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        identity.runId,
        PRESET_PLANS[input.preset].operationId,
        input.machineId,
        profile.containerId,
        prepareJobId,
        launchReport(input, profile),
        identity.authorityId,
        JSON.stringify({
          preset: input.preset,
          selected: prepared.rows.length,
          available: prepared.held,
          excluded: prepared.excluded,
          bytes: prepared.bytes,
          overBound: prepared.overBound,
          promptVersion: PROMPT_VERSION,
          recipes: recipes.map((recipe) => ({ id: recipe.id, version: recipe.version })),
          ...(input.preset === "explore-topic"
            ? { entityId: input.entityId ?? "" }
            : { sinceDays: input.sinceDays ?? 1 }),
        }),
        new Date(deps.now()).toISOString(),
        JSON.stringify({ closure: null, preparing: prepareJobId }),
      ],
    );
    store.touch();
    return { runId: identity.runId, jobId: prepareJobId };
  }

  /**
   * BABEL'S OWN LAUNCH REPORT, on the run row's `profile` column: the container it named, the
   * revision it was shown, and the account the request named when it named one.
   *
   * Code chooses the account and its session receipt reports none, so this column is the only
   * place "which window did that run spend" is written; a drain's total is the sum of the runs
   * that named its account (#267), and on 2026-09-13 nothing on the machine could answer that
   * question at all.
   */
  function launchReport(input: LaunchInput, profile: CodeProfile): string {
    const session = input.session;
    return JSON.stringify({
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
    });
  }

  /**
   * EVERY RUN WHOSE MATERIAL IS SEALED AND WHOSE SESSION IS NOT POSTED YET, posted now (#592).
   *
   * The job-inputs primitive binds a SETTLED job's output — a binding whose source is still
   * active is refused — so the session cannot be posted at the press, when `prepare` has only
   * just been handed to the machine. This is the other half: a wake that `prepare`'s own
   * settlement causes finds the run waiting on it and posts the session.
   *
   * IT IS IDEMPOTENT AND IT IS A QUERY, not a memory: the rows it acts on are exactly the ones
   * with a container, no job and a settled preparation, so a wake that ran twice in the same
   * second finds nothing the first did not already give a job id to.
   *
   * A PREPARATION THAT DID NOT COMPLETE CLOSES ITS RUN. There is no material to bind and no
   * second attempt that would change that: the selection is fixed and the machine has already
   * read it. Leaving the run open would leave an operator waiting on a session nothing will
   * ever post.
   */
  async function postPrepared(
    jobs: JobsSlice,
    engine: CodeEngine,
    plan: RunPlan,
  ): Promise<readonly Posted[]> {
    void jobs;
    void plan;
    const waiting = await store.db.query<PreparedRun>(
      `SELECT r.id AS id, r.machine_id AS machine_id, r.container_id AS container_id,
              r.prepare_job_id AS prepare_job_id, r.profile AS profile,
              r.preparation AS preparation, p.closure AS prepare_closure,
              p.payload AS prepare_payload
         FROM runs r JOIN runs p ON p.job_id = r.prepare_job_id
        WHERE r.closure IS NULL AND r.job_id IS NULL AND r.container_id IS NOT NULL
          AND p.closure IS NOT NULL
        ORDER BY r.started_at`,
    );
    const posted: Posted[] = [];
    for (const run of waiting) {
      const at = new Date(deps.now()).toISOString();
      const material = materialOf(run.prepare_payload);
      if (run.prepare_closure !== "completed" || material === null) {
        const reason =
          run.prepare_closure === "completed"
            ? `the preparation ${run.prepare_job_id ?? ""} sealed no material this run could read`
            : `the preparation ${run.prepare_job_id ?? ""} closed as ${run.prepare_closure ?? ""}`;
        await store.db.run(
          `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
          [at, JSON.stringify({ closure: "failed", reason }), run.id],
        );
        store.touch();
        posted.push({ runId: run.id, refused: reason });
        continue;
      }
      const intent = documentOf(run.preparation);
      const report = documentOf(run.profile);
      const asked = new Set(
        (Array.isArray(intent["recipes"]) ? intent["recipes"] : [])
          .map((entry) => (typeof entry === "object" && entry !== null ? String((entry as Record<string, unknown>)["id"]) : ""))
          .filter((id) => id !== ""),
      );
      const cookbook = await deps.cookbook();
      const recipes = Object.values(cookbook).filter((recipe) => asked.has(recipe.id));
      if (recipes.length === 0) {
        const reason = `this hub no longer holds the recipes this run was started with`;
        await store.db.run(
          `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
          [at, JSON.stringify({ closure: "failed", reason }), run.id],
        );
        store.touch();
        posted.push({ runId: run.id, refused: reason });
        continue;
      }
      // THE PROMPT IS BUILT FROM WHAT WAS ACTUALLY SEALED: the index's own file names, record
      // counts and digests, rather than the selectors the press could only guess from.
      const prompt = composeExplorePrompt({
        stage: "explore",
        recipes,
        sessions: material.sessions.map((entry) => ({
          selector: entry.selector,
          file: entry.file,
        })),
        preparationId: material.preparationId,
        params: {
          [PARAM.stage]: "explore",
          [PARAM.runId]: run.id,
          [PARAM.preparation]: material.preparationId,
        },
      });
      /*
        CODE BOUNDS A SESSION'S PROMPT IN BYTES, and the bound is the hub's own: a prompt is
        carried in the 64 KiB job-input map, which counts ENCODED bytes — so a character
        check would pass a prompt of legal length whose selectors and digests are multi-byte
        and have it refused at admission instead. Babel's composed prompt fits with room to
        spare; this stays because a longer contract, a bigger selection or a corpus of
        non-ASCII selectors is how it would stop fitting.

        Measured here, against CODE'S OWN published number, the run closes with both figures
        on it; left to Code's parse it closes with a Zod issue inside a sentence about a door
        being "asked for something it does not take", which is true and tells an operator
        nothing. It is not a thing a later wake fixes — the prompt is a function of the
        contract and the selection, both fixed by now — so the run closes rather than being
        retried.
      */
      const bytes = promptBytes(prompt);
      if (bytes > PROMPT_LIMIT) {
        const reason =
          `prompt_too_large: this run's prompt is ${String(bytes)} bytes and ` +
          `${CODE_PLUGIN_ID}.runSession takes ${String(PROMPT_LIMIT)}. The analysis contract ` +
          `and the stage's schema are most of it, so what moves is Code's bound or the ` +
          `contract itself — not this selection.`;
        await store.db.run(
          `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
          [at, JSON.stringify({ closure: "failed", reason }), run.id],
        );
        store.touch();
        posted.push({ runId: run.id, refused: reason });
        continue;
      }
      const answered = await engine.runSession({
        profile: {
          containerId: run.container_id ?? "",
          expectedRevision: Number(report["expectedRevision"] ?? 0),
        },
        machineId: run.machine_id ?? "",
        prompt,
        prepareJobId: run.prepare_job_id ?? "",
      });
      if (!answered.ok) {
        // A REFUSAL HERE IS FINAL, not a thing to retry on every wake for ever: the material
        // is sealed and immutable, the profile was named at the press, and nothing a later
        // wake could do changes what Code just said. The run closes carrying the sentence.
        await store.db.run(
          `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
          [at, JSON.stringify({ closure: "failed", reason: answered.refused }), run.id],
        );
        store.touch();
        posted.push({ runId: run.id, refused: answered.refused });
        continue;
      }
      await store.db.run(
        `UPDATE runs SET job_id = ?, payload = ? WHERE id = ? AND job_id IS NULL`,
        [
          answered.value.jobId,
          JSON.stringify({ closure: null, requestedAt: deps.now() }),
          run.id,
        ],
      );
      store.touch();
      posted.push({ runId: run.id, jobId: answered.value.jobId });
    }
    return posted;
  }

  return { startExplore, startBeat, postPrepared };
}

/** A run waiting on its preparation, as the poster reads one. */
type PreparedRun = {
  id: string;
  machine_id: string | null;
  container_id: string | null;
  prepare_job_id: string | null;
  profile: string | null;
  preparation: string | null;
  prepare_closure: string | null;
  prepare_payload: string;
};

/** A JSON column as an object, or an empty one; a column nobody can parse names nothing. */
function documentOf(value: string | null): Record<string, unknown> {
  if (value === null || value === "") return {};
  let held: unknown;
  try {
    held = JSON.parse(value);
  } catch {
    return {};
  }
  return typeof held === "object" && held !== null && !Array.isArray(held)
    ? (held as Record<string, unknown>)
    : {};
}

/** The material index a settled `prepare` wrote into its own receipt, or null. */
function materialOf(payload: string): MaterialIndex | null {
  const parsed = MaterialIndexSchema.safeParse(documentOf(payload)["material"]);
  return parsed.success ? parsed.data : null;
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
      if (preset.start === "draw") return { refused: DRAW_MANAGED };

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
        prepare_job_id: string | null;
        machine_id: string | null;
        kind: string;
        closure: string | null;
        container_id: string | null;
      }>(
        `SELECT job_id, prepare_job_id, machine_id, kind, closure, container_id
           FROM runs WHERE id = ?`,
        [runId],
      );
      const run = rows[0];
      if (run === undefined) return { refused: `no run ${runId}` };
      if (run.closure !== null) return { refused: `${runId} already ended: ${run.closure}` };
      const jobId = run.job_id ?? "";
      const machineId = run.machine_id ?? "";
      const container = run.container_id ?? "";
      const prepareJobId = run.prepare_job_id ?? "";
      /*
        A RUN STILL PREPARING HAS NO SESSION TO NAME (#592). It is started in two wakes and
        the first posts only `atyrode.babel.prepare`, so between them the one job this run
        has is its preparation — at `prepare`'s own node, which is where the panel asks. A
        door that refused here (there was no job to name, and it said so) left the posting
        wake free to post the session AFTER the operator pressed stop.
      */
      const preparing = jobId === "" && prepareJobId !== "";
      if (machineId === "" || (jobId === "" && !preparing)) {
        return { refused: `${runId} has no job on a machine to stop` };
      }
      // The caller was admitted at the node it POSTED; the row says which job this run is. A
      // request that authorized one job and named another is refused rather than reconciled.
      const node = preparing
        ? { jobId: prepareJobId, operationId: OPERATIONS.prepare as string }
        : { jobId, operationId: run.kind };
      if (
        job.jobId !== node.jobId ||
        job.machineId !== machineId ||
        job.operationId !== node.operationId
      ) {
        return {
          refused:
            `${runId} is ${machineId}/${node.operationId}/${node.jobId} and this stop asks ` +
            `for authority at ${job.machineId}/${job.operationId}/${job.jobId}`,
        };
      }
      /*
        A CODE SESSION IS CANCELLED THROUGH CODE (#279). Its job belongs to `atyrode.omp` and
        `ctx.jobs.cancel` is bound to the calling plugin's id, so the hub's verb would refuse
        every run of the lane that reaches a model. `container_id` says which lane this run is
        in; `cancelSession` is idempotent on a job that has already settled, so a Stop that
        raced the run's own ending answers the job rather than an error.

        A RUN STILL PREPARING IS STOPPED AT ITS PREPARATION, and the row below is what makes
        that stick: `postPrepared` posts a session for every open row whose material sealed,
        so a Stop that only cancelled the preparation would be answered by the next wake
        posting the session anyway — the operator pressing stop and the account spending
        afterwards, which is the 2026-09-13 failure this lane exists not to repeat.
      */
      if (preparing || container === "") {
        // BABEL'S OWN JOB, either way: the beat's, or the preparation of a run that has not
        // reached a session. `job` is the node the caller was admitted at and the row just
        // agreed with, so it is what the cancel names.
        try {
          await deps.jobs(ctx).cancel(job);
        } catch (error) {
          return { refused: `${machineId} refused to stop ${job.jobId}: ${message(error)}` };
        }
      } else {
        const answered = await deps.engine(ctx.actions).cancelSession({
          containerId: container,
          jobId,
        });
        if (!answered.ok) return { refused: answered.refused };
      }
      // The run is closed here rather than left for the loop to notice: the operator asked for
      // it to stop, and a row that kept saying `running` until the next cycle would be the
      // interface disagreeing with the act he just performed. Closing it also releases what it
      // reserved, which is why the claim is settled in the same breath.
      const at = new Date(deps.now()).toISOString();
      await store.db.run(
        // `AND closure IS NULL` for the same reason the read above refuses a closed run: two
        // stops, or a stop racing the run's own ending, write the first closure and not the
        // second. For a PREPARING run this write is the whole stop: it is the row the posting
        // wake reads, so once it is closed no session can be posted for it.
        `UPDATE runs SET closure = 'stopped', finished_at = ?, payload = ?
          WHERE id = ? AND closure IS NULL`,
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
