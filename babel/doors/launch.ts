import type { SqlStatement } from "@manifold/plugin";
import { HostCallError } from "@manifold/plugin-kit/errors";
import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  ACTIVITIES,
  AnalysisWorkSchema,
  AnalysisClaimSchema,
  ANALYSIS_BRIEF_BYTE_LIMIT,
  ArchiveLabelSchema,
  CaptureSessionSchema,
  type AnalysisWork,
  INPUT_FIELD,
  LaunchRequestSchema,
  LaunchInputSchema,
  LaunchResultSchema,
  MATERIAL_OUTPUT,
  OPERATIONS,
  TRANSCRIPT_MAP_SESSION_OPERATION,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  PREPARE_INPUT_MAX_BYTES,
  PRESET_OPERATIONS,
  PRESET_START,
  ProfilesQuerySchema,
  ProfilesResultSchema,
  MACHINE_OPERATIONS,
  SnapshotIdSchema,
  StopInputSchema,
  StopResultSchema,
  StartMapCatalogRequestSchema,
  StartMapCatalogResultSchema,
  type TranscriptMapCatalogAdmission,
  VerifyRequestSchema,
  VerifyResultSchema,
  MaterialIndexSchema,
  type CaptureGroup,
  type CodeProfile,
  type LaunchInput,
  type MaterialIndex,
  type OperationName,
  type PresetStart,
  type VerifyInput,
  ENGINE_REFUSALS,
} from "../contract.ts";
import { ARCHIVED_CAPTURE, materialBound } from "../store/analysis.ts";
import { perMachineBound, type Coordinator, type Policy } from "../store/coordinator.ts";
import {
  carriedSteering,
  composeExplorePrompt,
  PARAM,
  PROMPT_VERSION,
  type Recipe,
} from "../server/engine/prompts.ts";
import {
  composeTitlePrompt,
  declinedTitles,
  MAX_TITLE_BATCH,
  offeredSelectors,
  TITLE_PROMPT_VERSION,
  titleStatements,
} from "../server/engine/titles.ts";
import { CODE_PLUGIN_ID } from "@atyrode/manifold-code";
import {
  PROMPT_LIMIT,
  promptBytes,
  type ActionsSlice,
  type CodeEngine,
} from "../server/engine/session.ts";
import {
  describeHost,
  describeMapHost,
  type InferenceUsage,
  type JobLaunch,
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

    the SELECTION   which archived captures the run reads, and which it may not (a session the
                    catalog has not listed from the archive, and Babel's own transcripts — #262)
    the MATERIAL    that selection SEALED, as `prepare`'s own second output, so the session's
                    sandbox can bind it read-only at `/inputs/material`
    the PROMPT      the recipes, the answering protocol, the stage's schema and the material's
                    layout (`server/engine/prompts.ts`)
    the RUN ROW     one row per run, carrying Code's job, the container that answered and the
                    `prepare` job whose material it read

  WHICH PRESET BECOMES WHAT:

    read-whats-new   explore   one Code session over the sessions written in the window
    explore-topic    explore   one Code session over the sessions the topic's own records cite
    review-backlog   evaluate  a policy-managed draw; this door does not select its own work
    file-and-tidy    evaluate  the same draw
    keep-going       conductor the beat: one archive catalog, Babel's own job, posted here

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

  The press posts Babel's OWN jobs — an explore's preparation (`atyrode.babel.prepare`) and the
  beat (`keep-going`'s operation), both operations this manifest DOES declare — and `machines:run`
  for them is discharged at the effect, by `engine.jobs.execute`, against the authority this
  door's delegates carry. That is why `machines:run` is one of them (#448): without it the hub
  refused every posting `authority_or_consent_refused` however privileged the caller was.
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
 *
 * `machines:run` IS WHAT THE POSTING ITSELF IS DISCHARGED AGAINST (#448). `engine.jobs.execute`
 * admits Babel's own `prepare` or its beat only when this bridge carries it; the host still
 * intersects it with the caller's own capabilities and still requires the operator's
 * version-bound consent at the operation node, so it lends nothing the caller does not hold.
 */
const LAUNCH_DELEGATES = [
  "jobs:read",
  "locations:read",
  "locations:write",
  "machines:read",
  "machines:run",
] as const;

/** Reading Code's saved profiles is a read of containers and nothing else. */
const PROFILES_CAPS = ["containers:read"] as const;

/** Closing a run is a write of this plugin's rows; the cancel is the door's own ceiling. */
const STOP_CAPS = ["containers:write"] as const;
const STOP_DELEGATES = ["jobs:cancel"] as const;

/** Asking a machine to read its archive writes one of this plugin's run rows; the job itself
 *  is discharged at the effect, under the same delegates a launch posts with. */
const VERIFY_CAPS = ["containers:write"] as const;

/**
 * The two catalogued values a restore is built from, as restic and the catalog spell them.
 *
 * They are checked at the door rather than trusted because `sessions` holds IMPORTED rows too
 * (`tools/import.ts`), and the Go deployment's snapshot and digest columns are its own
 * spellings. `machine/verify.ts` states the same shapes in its input schema; a document that
 * failed there would fail as a job on a machine nobody is watching.
 */
const RESTIC_SNAPSHOT = /^(latest|[0-9a-f]{8,64})$/;
const CONTENT_DIGEST = /^sha256:[0-9a-f]{64}$/;

/** What a preset is, in one row: the run's kind, the operation it becomes, how it is started. */
interface PresetPlan {
  readonly kind: "explore" | "evaluate" | "conductor" | "prepare";
  readonly operationId: OperationName;
  readonly start: PresetStart;
}

const PRESET_PLANS: Record<LaunchInput["preset"], PresetPlan> = {
  "read-whats-new": {
    kind: "explore",
    operationId: PRESET_OPERATIONS["read-whats-new"],
    start: PRESET_START["read-whats-new"],
  },
  "explore-topic": {
    kind: "explore",
    operationId: PRESET_OPERATIONS["explore-topic"],
    start: PRESET_START["explore-topic"],
  },
  "review-backlog": {
    kind: "evaluate",
    operationId: PRESET_OPERATIONS["review-backlog"],
    start: PRESET_START["review-backlog"],
  },
  "file-and-tidy": {
    kind: "evaluate",
    operationId: PRESET_OPERATIONS["file-and-tidy"],
    start: PRESET_START["file-and-tidy"],
  },
  "keep-going": {
    kind: "conductor",
    operationId: PRESET_OPERATIONS["keep-going"],
    start: PRESET_START["keep-going"],
  },
};

/**
 * THE OPERATION A PRESS'S OWN JOB RUNS UNDER, which is the one its limits are planned for (#449).
 *
 * A preset's `operationId` names what the run IS — `atyrode.babel.explore` for an explore — and
 * no installation declares that operation any more (#279): the only job an explore posts itself
 * is the preparation that seals its material. Planning an explore press for the explore operation
 * fell back to `DEFAULT_LIMITS`, above `prepare`'s declared ceiling, and the hub refused every
 * preparation `limit_exceeded`. The conductor's analysis stages already plan for `prepare`; the
 * operator's press and the drain now plan for the same operation, through this one answer.
 */
export function pressOperation(preset: LaunchInput["preset"]): OperationName {
  return PRESET_PLANS[preset].start === "explore"
    ? OPERATIONS.prepare
    : PRESET_PLANS[preset].operationId;
}

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
 * How many catalogued sessions one run is prepared over. It is the ceiling on the window's rows;
 * what one preparation is actually handed is bounded again by its encoded input
 * (`PREPARE_INPUT_MAX_BYTES`) and by the material bound, and a window holding more is reported
 * as what was taken out of what was there.
 */
const MAX_SELECTION = 120;

/** When a session was last written, as far as the store can say: the archive's word first. */
const RECENT = "COALESCE(s.modified_at, s.archived_at, s.seen_at)";

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
  readonly materialJobId?: string;
  /** The operator or owning conductor cycle recorded as the run's authority. */
  readonly authorityId: string;
}

/**
 * WHY NOTHING WAS STARTED: the sentence an operator reads, and — when the HUB is who said no —
 * its own word for the refusal beside it.
 *
 * The pair exists because a caller that must BEHAVE differently for one refusal cannot get
 * there from prose. The drain adopts a job the hub already holds under a derived id
 * (`job_digest_conflict`) and ends the round for everything else, and a substring match on the
 * sentence made the operator's wording load-bearing. It is the same split `machine/results.ts`
 * draws for a submission — {@link refusalCode} beside the message the model reads — and the
 * same one `EngineAnswer` carries for Code's refusals.
 *
 * `code` IS PRESENT EXACTLY WHEN THE HUB REFUSED, whatever it said; it is absent when the
 * sentence is Babel's own — an unready machine, a document over the input ceiling, a window
 * offering nothing. So its absence means one thing and never "the hub refused in a way this
 * file did not recognise", which a vocabulary check here would have made it mean.
 */
export interface Refused {
  readonly refused: string;
  readonly code?: string;
  /** Cancellation was not confirmed; retain the existing grant until the job is terminal. */
  readonly pending?: boolean;
}

/** What a start answered: the two ids, or why nothing was started. */
export type Started = { runId: string; jobId: string } | Refused;

/** What a verification answered: its run, its job, and the snapshot it will restore from —
 *  empty when it only checks the repository. */
export type Verified =
  { readonly runId: string; readonly jobId: string; readonly snapshotId: string } | Refused;

/**
 * THE LAUNCH PATH, EXPOSED SO THERE IS EXACTLY ONE OF IT.
 *
 * The drain controller (#258) launches explores and beats through the same object the button
 * does, because two implementations of "start a run" disagreeing is how an operator's ceiling
 * gets spent twice — which is the whole subject of the post-mortem this lane comes from.
 */

/**
 * What {@link LaunchMachinery.postPrepared} did about one waiting run: the Code job it posted,
 * the sentence the run was closed with, or the sentence it settled with when nothing was left
 * for a model to do (a titling run whose sessions all recorded their own titles, #453). All are
 * reported, because a wake nobody watched has to leave its account on the row AND in the
 * cycle's notes.
 */
export type Posted =
  | { readonly runId: string; readonly jobId: string }
  | { readonly runId: string; readonly refused: string }
  | { readonly runId: string; readonly settled: string };

export interface LaunchMachinery {
  /**
   * One explore, as a Code session over sealed material. The `engine` is a parameter rather than
   * a dep because it is built from the CALLER's own `actions` slice: a settlement's hook and an
   * operator's dispatch reach Code under different authority, and the one that reaches it is the
   * one whose principal Code grades.
   */
  startExplore(
    identity: LaunchIdentity,
    jobs: BabelJobs,
    engine: CodeEngine,
    input: LaunchInput,
    plan: RunPlan,
    analysis?: AnalysisWork,
  ): Promise<Started>;
  /** One beat — `keep-going`'s operation, Babel's own job. It reaches no model and needs no Code. */
  startBeat(
    identity: LaunchIdentity,
    jobs: BabelJobs,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
  /**
   * ONE VERIFICATION of the archive (#338) — an `atyrode.babel.verify`, Babel's own job, which
   * reads the repository and restores nothing into it. It is here beside the other two because
   * a run row is written for it by the same statement: one path posts this plugin's jobs, so
   * the conductor settles a verification exactly as it settles the beat.
   */
  startVerify(
    identity: LaunchIdentity,
    jobs: BabelJobs,
    input: VerifyInput,
    plan: RunPlan,
  ): Promise<Verified>;
  /**
   * EVERY RUN WHOSE MATERIAL IS SEALED AND WHOSE SESSION IS NOT POSTED YET, posted now.
   *
   * It is a second wake and not a continuation of the first because Manifold's job-inputs
   * primitive binds a SETTLED job's output (#592): the session cannot be posted while its own
   * preparation is still running. Called from the cycle, after the conductor has settled what
   * finished and before the drain decides whether to launch more.
   */
  postPrepared(jobs: BabelJobs, engine: CodeEngine, plan: RunPlan): Promise<readonly Posted[]>;
  /**
   * NAMING THE SESSIONS WHOSE OWN LOGS CARRY NO TITLE, IF THIS CYCLE MAY SPEND ON IT (#342).
   *
   * Phase one of the same two-phase shape an explore has: a `prepare` over the untitled
   * sessions, and a run row recorded as intent. {@link postPrepared} posts the Code session on
   * the wake that preparation's settlement causes, and the conductor's settlement writes the
   * titles. `cycleRunId` is the tick this is charged to, so the per-cycle ceiling is measured
   * against the reviews the same cycle already dispatched.
   *
   * It answers null far more often than not: no route, no untitled session, one already in
   * flight, or no allowance left. Those are the normal states and none of them is a note.
   */
  inferTitles(jobs: BabelJobs, engine: CodeEngine, cycleRunId: string): Promise<Posted | null>;
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

/** One archived capture as the selection reads it; a type, so it is a row the store can hold. */
type SessionRow = {
  selector: string;
  harness: string;
  source_id: string;
  archive_label: string;
  archive_path: string;
  snapshot_id: string;
  /** The catalogued observation: the bytes restic recorded, and the file's own modification
   *  time in `CaptureInstantSchema`'s one spelling. The only figures the hub has before
   *  `prepare` runs. */
  size: number | bigint;
  modified_at: string;
};

/** What one preset's window held: its captures newest first, and what it could not offer. */
interface Window {
  readonly rows: readonly SessionRow[];
  /** Every catalogued session the window held, selectable or not. */
  readonly held: number;
  /** How many of those name no archived capture or are Babel's own, so were never candidates. */
  readonly excluded: number;
}

/** What one preparation is handed out of a window, under both of its bounds. */
interface Captured {
  /** The captures, grouped by the snapshot that holds them: `prepare`'s own input. */
  readonly captures: readonly CaptureGroup[];
  /** The sessions taken, newest first: the run row's list, and an analysis's exact scope. */
  readonly selectors: readonly string[];
  /** The catalogued bytes of the captures taken, which bounds the sealed material. */
  readonly bytes: number;
  /** The material bound they were taken under, for the sentence that reports it. */
  readonly bound: number;
  /** How many candidates a bound left out, on top of `MAX_SELECTION`. */
  readonly overBound: number;
  /** Rows the store holds as captures in a shape `prepare` would refuse; never handed over. */
  readonly unreadable: number;
}

const ENCODER = new TextEncoder();

/** The bytes `text` adds to a job's input record once it is part of the one input string. */
function encodedBytes(text: string): number {
  return ENCODER.encode(JSON.stringify(text)).byteLength - 2;
}

/**
 * THE CAPTURES ONE PREPARATION IS HANDED, newest first, until a bound is reached (#453).
 *
 * TWO BOUNDS, BOTH MEASURED BEFORE ANYTHING IS POSTED. The material bound is catalogued bytes:
 * 120 sessions at the corpus's own average is over a gigabyte, and a selection that overran
 * what the machine's scratch can seal would be discovered after the machine had fetched every
 * one of them. A capture that would carry the material past `bound` is left out and counted,
 * and a smaller, older one may still fit. The input bound is the record `document` posts: each
 * capture adds its path and observation, and its snapshot's group the first time, to
 * `prepare`'s one input string, and the selection stops at the first capture that would carry
 * the encoded record past `PREPARE_INPUT_MAX_BYTES`, counting it and every one after it. Both
 * land in `overBound`, so the operator reads a narrower window rather than an
 * `output_too_large` or an oversized job input.
 *
 * The encoded length is kept exactly rather than re-encoded per row: JSON escapes a string one
 * character at a time, so a record's encoded length is its pieces' encoded lengths added up.
 * `base` is the rest of the document, which the captures are added to last.
 */
function captureSelection(
  base: Readonly<Record<string, unknown>>,
  rows: readonly SessionRow[],
  bound: number,
): Captured {
  const groups = new Map<string, CaptureGroup>();
  const selectors: string[] = [];
  let encoded = ENCODER.encode(
    JSON.stringify({ [INPUT_FIELD]: JSON.stringify({ ...base, captures: [] }) }),
  ).byteLength;
  let bytes = 0;
  let overBound = 0;
  let unreadable = 0;
  for (const [index, row] of rows.entries()) {
    const session = CaptureSessionSchema.safeParse({
      harness: row.harness,
      sourceId: row.source_id,
      path: row.archive_path,
      size: Number(row.size),
      modifiedAt: Date.parse(row.modified_at),
    });
    const snapshotId = SnapshotIdSchema.safeParse(row.snapshot_id);
    const label = ArchiveLabelSchema.safeParse(row.archive_label);
    if (!session.success || !snapshotId.success || !label.success) {
      unreadable += 1;
      continue;
    }
    if (bytes + session.data.size > bound) {
      overBound += 1;
      continue;
    }
    const group = groups.get(snapshotId.data);
    const added =
      group === undefined
        ? encodedBytes(
            (groups.size === 0 ? "" : ",") +
              JSON.stringify({
                snapshotId: snapshotId.data,
                label: label.data,
                sessions: [session.data],
              }),
          )
        : encodedBytes(`,${JSON.stringify(session.data)}`);
    if (encoded + added > PREPARE_INPUT_MAX_BYTES) {
      overBound += rows.length - index;
      break;
    }
    encoded += added;
    if (group === undefined) {
      groups.set(snapshotId.data, {
        snapshotId: snapshotId.data,
        label: label.data,
        sessions: [session.data],
      });
    } else {
      group.sessions.push(session.data);
    }
    selectors.push(row.selector);
    bytes += session.data.size;
  }
  return { captures: [...groups.values()], selectors, bytes, bound, overBound, unreadable };
}

export function launchMachinery(store: BabelStore, deps: LaunchDeps): LaunchMachinery {
  /**
   * The captures one run may be prepared over, newest first, how many sessions the window held,
   * and how many of those a preparation may not read.
   *
   * WHAT IT NEVER SELECTS. A session whose row names no archived capture: a preparation reads
   * the archive and nothing else (#453), so a row the catalog has not listed yet — an imported
   * one, most often — is catalogued and not a candidate. A row marked `kind = 'agent'` is one of
   * Babel's own runs' transcripts, catalogued and archived like every other session (#177) and
   * left out of a preset that reads the operator's work; `agentSessions` is how a preset that
   * studies Babel itself (#270) asks for them. A `live` row never is, and no capture is live.
   *
   * THERE IS NO MACHINE IN IT. Any machine holding the archive binding prepares any capture, so
   * the window is the archive's and not the launch machine's. "Recent" is when the session was
   * last written — the archived file's own modification time, else when it was archived — and
   * not `seen_at`, which is now the catalog's clock and would make every session look new.
   *
   * `held` is what the window CONTAINED, exclusions included, so `excluded` is a number both the
   * run row and the refusal can state: "there is nothing here" and "there is nothing here a run
   * may read" are different facts, and the day this lane comes from was two hours of reading an
   * adjacent number as the one that was asked for.
   */
  async function selection(input: LaunchInput, analysis?: AnalysisWork): Promise<Window> {
    const cited = `FROM filings f
         JOIN edges e ON e.from_id = f.record_id AND e.kind = 'cites' AND e.to_kind = 'session'
         JOIN sessions s ON s.selector = e.to_id
        WHERE f.entity_id = ? AND f.withdrawn = 0`;
    const recent = `FROM sessions s WHERE ${RECENT} >= ?`;
    const topic = input.preset === "explore-topic";
    const scope =
      analysis === undefined
        ? topic
          ? cited
          : recent
        : `FROM sessions s WHERE s.selector IN (${analysis.selectors.map(() => "?").join(", ")})`;
    const params: readonly string[] =
      analysis !== undefined
        ? [...analysis.selectors]
        : topic
          ? [input.entityId ?? ""]
          : [new Date(deps.now() - (input.sinceDays ?? 1) * DAY_MS).toISOString()];
    const allowed = `${ARCHIVED_CAPTURE}${input.agentSessions && analysis === undefined ? "" : " AND s.kind = 'operator'"}`;

    const rows = await store.db.query<SessionRow>(
      `SELECT DISTINCT s.selector AS selector, s.harness AS harness, s.source_id AS source_id,
              s.archive_label AS archive_label, s.archive_path AS archive_path,
              s.snapshot_id AS snapshot_id, s.size AS size, s.modified_at AS modified_at,
              ${RECENT} AS recent
         ${scope} AND ${allowed}
        ORDER BY recent DESC, s.selector
        LIMIT ?`,
      [...params, MAX_SELECTION],
    );
    const counted = await store.db.query<{ held: number; selectable: number }>(
      `SELECT count(DISTINCT s.selector) AS held,
              count(DISTINCT CASE WHEN ${allowed} THEN s.selector END) AS selectable
         ${scope}`,
      [...params],
    );
    const held = Number(counted[0]?.held ?? 0);
    const selectable = Number(counted[0]?.selectable ?? 0);
    return { rows, held, excluded: Math.max(0, held - selectable) };
  }

  /**
   * One native request, retained before execute for conductor work so a lost response remains
   * pollable. Only a typed pre-admission refusal plus an authoritative absent job releases it.
   *
   * A refusal here is the HUB's, so it carries the hub's word as well as the sentence: this is
   * the one place a posting's two accounts of itself are still together, and a caller reading
   * the word back out of the sentence would be reading a line written for a person.
   */
  async function post(
    jobs: BabelJobs,
    launch: JobLaunch,
    run: {
      readonly runId: string;
      readonly kind: string;
      readonly recipeId: string;
      readonly authorityId: string;
      readonly preparation: Record<string, unknown>;
      readonly authorityKind?: "operator" | "conductor";
    },
  ): Promise<Refused | null> {
    const at = new Date(deps.now()).toISOString();
    const retain = async () =>
      await store.db.run(
        `INSERT INTO runs(id, kind, machine_id, job_id, recipe_id, authority_kind,
                        authority_id, preparation, started_at, records, payload)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
        [
          run.runId,
          run.kind,
          launch.machineId,
          launch.jobId,
          run.recipeId,
          run.authorityKind ?? "operator",
          run.authorityId,
          JSON.stringify(run.preparation),
          at,
          JSON.stringify({ closure: null, requestedAt: deps.now() }),
        ],
      );
    // A governed native post already has its fenced parent. Retain its pollable job row
    // before execute too, so a lost transport response can be reconciled by the usual loop.
    if (run.authorityKind === "conductor") {
      try {
        await retain();
      } catch (error) {
        return { refused: `Babel could not retain the native intent: ${message(error)}` };
      }
    }
    try {
      await jobs.execute(launch);
    } catch (error) {
      const refused = `${launch.machineId} refused the job: ${message(error)}`;
      let absent = false;
      if (run.authorityKind === "conductor" && nativeAdmissionRefusal(error)) {
        try {
          await jobs.status({
            kind: "job",
            machineId: launch.machineId,
            operationId: launch.operationId,
            jobId: launch.jobId,
          });
        } catch (statusError) {
          absent = nativeFailureToken(statusError, "jobs.status") === "job_not_started";
        }
      }
      if (run.authorityKind === "conductor" && absent) {
        await store.db.run(
          `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ?
           WHERE id = ? AND job_id = ? AND closure IS NULL`,
          [at, JSON.stringify({ closure: "failed", reason: refused }), run.runId, launch.jobId],
        );
        store.touch();
      }
      return {
        refused,
        code: hubRefusal(error),
        ...(run.authorityKind === "conductor" && !absent ? { pending: true } : {}),
      };
    }
    try {
      if (run.authorityKind !== "conductor") await retain();
    } catch (error) {
      try {
        await jobs.cancel({
          kind: "job",
          machineId: launch.machineId,
          operationId: launch.operationId,
          jobId: launch.jobId,
        });
      } catch (cancelError) {
        return {
          refused: `Babel could not retain job ${launch.jobId}: ${message(error)}; cancellation is unconfirmed: ${message(cancelError)}`,
          pending: true,
        };
      }
      return {
        refused: `the job was cancelled because Babel could not retain it: ${message(error)}`,
      };
    }
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
    jobs: BabelJobs,
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
    jobs: BabelJobs,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started> {
    const preset = PRESET_PLANS[input.preset];
    const admitted = await ready(jobs, input.machineId, preset.operationId);
    if ("refused" in admitted) return admitted;
    // Keep going: the beat is what wakes the hub, so starting the loop is starting one beat.
    // `minutes` is the operator's own bound on it, under the operation's ceiling.
    const asked = (input.minutes ?? 0) * 60_000;
    const timeoutMs = asked > 0 ? Math.min(asked, plan.limits.timeoutMs) : plan.limits.timeoutMs;
    const built = document({ runId: identity.runId, machineId: input.machineId });
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
   * ONE VERIFICATION of the archive on one machine (#338), and the session it proves.
   *
   * THE CAPTURE AND THE DIGEST COME OUT OF THE CATALOG, not out of the request. `sessions`
   * already holds which snapshot and path hold a session and what a reading of those bytes
   * digested to, so the operator asks with a selector and the machine is told facts this hub can
   * be held to. A request that named its own digest would be a comparison against whatever the
   * asker believed, which proves nothing about the archive.
   *
   * ANY MACHINE HOLDING THE ARCHIVE BINDING MAY VERIFY ANY CAPTURE (#453): the repository is one,
   * and a snapshot reads the same from every machine that can open it. The catalogued path is
   * passed when the catalogued snapshot is the one read, so the machine lists that path rather
   * than the whole snapshot; a snapshot the operator names instead is listed whole, because the
   * path the catalog recorded belongs to a different capture.
   */
  async function startVerify(
    identity: LaunchIdentity,
    jobs: BabelJobs,
    input: VerifyInput,
    plan: RunPlan,
  ): Promise<Verified> {
    let restore: {
      snapshotId: string;
      selector: string;
      digest: string;
      target: string;
      path?: string;
    } | null = null;
    if (input.session !== undefined) {
      const asked = input.session;
      const rows = await store.db.query<{
        snapshot_id: string | null;
        archive_path: string | null;
        content_digest: string | null;
      }>(`SELECT snapshot_id, archive_path, content_digest FROM sessions WHERE selector = ?`, [
        asked.selector,
      ]);
      const row = rows[0];
      if (row === undefined) return { refused: `no catalogued session ${asked.selector}` };
      const snapshotId = asked.snapshotId !== "" ? asked.snapshotId : (row.snapshot_id ?? "");
      if (snapshotId === "") {
        return {
          refused:
            `${asked.selector} names no archived snapshot, so there is nothing to restore it ` +
            `from; wait for the catalog to list it from the archive, or name a snapshot`,
        };
      }
      if (!RESTIC_SNAPSHOT.test(snapshotId)) {
        // An imported corpus carries the Go deployment's own snapshot column, which is not a
        // restic id. Refusing here is the earlier and clearer answer: the machine would refuse
        // the same document, twenty seconds later, as a job that failed to parse its input.
        return {
          refused:
            `the snapshot recorded for ${asked.selector} is "${snapshotId}", which is not a ` +
            `restic snapshot id; name the snapshot to read`,
        };
      }
      // A digest this hub cannot use is dropped rather than passed on: the machine still
      // compares the restored bytes against the snapshot's own, and `counts.digestCompared`
      // says whether the CATALOG was part of the comparison. An imported row is the case —
      // its digest column is the Go deployment's spelling — and it is also the session most
      // likely to be worth restoring, so refusing it outright would be the wrong trade.
      const catalogued = row.content_digest ?? "";
      const path = snapshotId === row.snapshot_id ? (row.archive_path ?? "") : "";
      restore = {
        snapshotId,
        selector: asked.selector,
        digest: CONTENT_DIGEST.test(catalogued) ? catalogued : "",
        target: asked.target,
        ...(path === "" ? {} : { path }),
      };
    }

    const admitted = await ready(jobs, input.machineId, MACHINE_OPERATIONS.verify);
    if ("refused" in admitted) return admitted;
    const built = document({
      runId: identity.runId,
      machineId: input.machineId,
      readData: input.readData,
      ...(restore === null ? {} : { restore }),
    });
    if ("refused" in built) return built;
    const refusal = await post(
      jobs,
      {
        jobId: identity.jobId,
        machineId: input.machineId,
        operationId: MACHINE_OPERATIONS.verify,
        input: built.input,
        outputs: [
          { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [identity.jobId] },
        ],
        limits: plan.limits,
        ...admitted.pinned,
      },
      {
        runId: identity.runId,
        kind: MACHINE_OPERATIONS.verify,
        recipeId: "",
        authorityId: identity.authorityId,
        preparation: {
          readData: input.readData,
          ...(restore === null ? {} : { session: restore.selector, snapshot: restore.snapshotId }),
        },
      },
    );
    if (refusal !== null) return refusal;
    return {
      runId: identity.runId,
      jobId: identity.jobId,
      snapshotId: restore?.snapshotId ?? "",
    };
  }

  /**
   * THE UNTITLED ARCHIVED SESSIONS, NEWEST FIRST, AND NOTHING ELSE (#342).
   *
   * `title IS NULL` and not "no good title": a title the harness recorded and one this tree
   * derived offline are both free and both outrank a guess that costs money, so neither is
   * ever replaced. A row already answered in `session_titles` is not offered again whatever
   * that answer was — a title, or the reason there is none — because the second half of
   * "inferred once" is that a session the model declined is not re-offered on the next wake.
   *
   * The catalog reads no transcript, so a capture's recorded title arrives only when a
   * preparation reads it (#453): the lane's `prepare` is also what finds the titles the harness
   * recorded, and {@link postPrepared} asks the model about the rest alone. The exclusions are
   * `prepare`'s own: only an archived capture can be read, from whichever label recorded it,
   * and a session of `kind = 'agent'` is one of Babel's own runs' transcripts, which `prepare`
   * refuses by name unless a caller asked for them — and naming Babel's own analysis logs is
   * not what this lane is for.
   */
  async function untitled(): Promise<readonly SessionRow[]> {
    return await store.db.query<SessionRow>(
      `SELECT s.selector AS selector, s.harness AS harness, s.source_id AS source_id,
              s.archive_label AS archive_label, s.archive_path AS archive_path,
              s.snapshot_id AS snapshot_id, s.size AS size, s.modified_at AS modified_at
         FROM sessions s
        WHERE ${ARCHIVED_CAPTURE} AND s.kind = 'operator' AND s.title IS NULL
          AND NOT EXISTS (SELECT 1 FROM session_titles t WHERE t.selector = s.selector)
        ORDER BY ${RECENT} DESC, s.selector
        LIMIT ?`,
      [MAX_TITLE_BATCH],
    );
  }

  /**
   * WHETHER THIS CYCLE MAY SPEND ON A TITLE, judged against the policy's own two ceilings and
   * the same ledger a review is admitted against (`coordinator.spend`).
   *
   * ONE TITLING RUN RESERVES WHAT ONE REVIEW RESERVES — the per-cycle allowance divided by the
   * batch — because it is one Code session and there is no second figure to invent. What it
   * has actually charged the day is read off the run rows: a settled run counts what it cost, a
   * run still open counts its full reservation, which is the same "reported cost where it
   * settled, the reservation where it did not" rule the claims ledger is summed by. Reading it
   * from `runs` rather than from `claims` is what keeps this lane out of the review
   * coordinator's table: a claim is one reviewer's grant on one record in one role, and a
   * titling batch is none of those.
   *
   * THE PER-CYCLE TEST IS THE CYCLE'S OWN CHARGES, so a tick that already filled its batch with
   * reviews names nothing: the two lanes compete for one allowance rather than each having a
   * private one. A deployment whose day is spent infers nothing and says so.
   */
  async function admitTitling(
    policy: Policy,
    cycleRunId: string,
    at: number,
  ): Promise<{ readonly reserved: number } | { readonly refused: string }> {
    const reserved =
      policy.batchSize <= 0 ? policy.perCycleCost : policy.perCycleCost / policy.batchSize;
    const spent = await deps.coordinator.spend(at);
    const day = new Date(at).toISOString().slice(0, 10);
    const charged = await store.db.query<{ charged: number | null }>(
      `SELECT COALESCE(SUM(COALESCE(cost_usd, ?)), 0) AS charged
         FROM runs WHERE kind = ? AND started_at >= ? AND started_at < ?`,
      [reserved, OPERATIONS.title, `${day}T00:00:00.000Z`, `${day}T23:59:59.999Z`],
    );
    const named = Number(charged[0]?.charged ?? 0);
    if (spent.total + named + reserved > policy.dailyCost) {
      return {
        refused:
          `daily ceiling ${policy.dailyCost.toFixed(4)} reached: ` +
          `${(spent.total + named).toFixed(4)} committed today and a title reserves ` +
          `${reserved.toFixed(4)}, so nothing is named`,
      };
    }
    const cycle = spent.byRun[cycleRunId] ?? 0;
    if (cycle + reserved > policy.perCycleCost) {
      return {
        refused:
          `per-cycle ceiling ${policy.perCycleCost.toFixed(4)} reached: cycle ${cycleRunId} ` +
          `has ${cycle.toFixed(4)} charged and a title reserves ${reserved.toFixed(4)}`,
      };
    }
    return { reserved };
  }

  /**
   * ONE TITLING RUN, POSTED IF THIS CYCLE MAY AFFORD ONE.
   *
   * The gates, in the order a reader should meet them: the policy must be enabled, because a
   * disabled policy spends nothing at all (§14); it must name a review route, because the
   * profile there is the only model authorization the operator has given and a lane that
   * reached for a second one would be a second spend nobody metered; one titling run at a
   * time, deployment-wide, because concurrency here buys nothing an operator asked for; the
   * ceilings; and finally a session that actually needs a name.
   * A routed cookbook remains available to explicit runs when every autonomous activity has
   * zero weight; keeping those recipes is not permission to name sessions in the background.
   */
  async function inferTitles(
    jobs: BabelJobs,
    engine: CodeEngine,
    cycleRunId: string,
  ): Promise<Posted | null> {
    const policy = (await deps.coordinator.policy()).policy;
    const route = policy.review;
    if (
      !policy.enabled ||
      route === undefined ||
      !ACTIVITIES.some((activity) => policy.activityWeights[activity] > 0)
    )
      return null;
    const open = await store.db.query<{ n: number | bigint }>(
      `SELECT COUNT(*) AS n FROM runs WHERE kind = ? AND closure IS NULL`,
      [OPERATIONS.title],
    );
    if (Number(open[0]?.n ?? 0) > 0) return null;

    const at = deps.now();
    const candidates = await untitled();
    if (candidates.length === 0) return null;
    const runId = `run_title_${String(at)}`;
    // The lane shares the routed machine's scratch with the conductor's other lanes, so its
    // material is bounded to the same per-machine share theirs is.
    const base = { runId: `${runId}_material`, machineId: route.machineId };
    const captured = captureSelection(
      base,
      candidates,
      await materialBound(store.db, route.machineId, perMachineBound(policy)),
    );
    if (captured.selectors.length === 0) return null;
    const admitted = await admitTitling(policy, cycleRunId, at);
    if ("refused" in admitted) return { runId: "", refused: admitted.refused };

    const prepareJobId = materialJobId(`job_title_${String(at)}`);
    const selectors = captured.selectors;
    const built = document({ ...base, captures: captured.captures });
    if ("refused" in built) return { runId, refused: built.refused };
    const checked = await engine.checkProfile(route.profile);
    if (!checked.ok) return { runId: "", refused: checked.refused };
    const ready = await describeHost(jobs, route.machineId, OPERATIONS.prepare);
    if ("refused" in ready) return { runId, refused: ready.refused };
    const installation = ready.readiness.installation;
    const sealed = await post(
      jobs,
      {
        jobId: prepareJobId,
        machineId: route.machineId,
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
        limits: deps.plan(policy, OPERATIONS.prepare).limits,
        ...(installation === null
          ? {}
          : {
              installationRevision: installation.revision,
              artifactSha256: installation.artifactSha256,
            }),
      },
      {
        runId: `${runId}_material`,
        kind: OPERATIONS.prepare,
        recipeId: "",
        authorityId: cycleRunId,
        preparation: { for: runId, titles: selectors.length },
      },
    );
    if (sealed !== null) return { runId, refused: sealed.refused };

    // THE SELECTORS TRAVEL ON THE RUN ROW, and they are what makes the answer checkable and
    // the work bounded: the settlement writes one `session_titles` row per selector NAMED HERE
    // — never per selector the model chose to answer about — so a reply about a session this
    // run never read is refused and a session the model ignored is still answered.
    await store.db.run(
      `INSERT INTO runs(id, kind, machine_id, container_id, prepare_job_id, recipe_id, profile,
                        authority_kind, authority_id, preparation, started_at, records, payload)
       VALUES (?, ?, ?, ?, ?, '', ?, 'conductor', ?, ?, ?, 0, ?)
       ON CONFLICT(id) DO NOTHING`,
      [
        runId,
        OPERATIONS.title,
        route.machineId,
        route.profile.containerId,
        prepareJobId,
        JSON.stringify({
          containerId: route.profile.containerId,
          expectedRevision: route.profile.expectedRevision,
        }),
        cycleRunId,
        JSON.stringify({
          titles: { selectors, reserved: admitted.reserved },
          promptVersion: TITLE_PROMPT_VERSION,
        }),
        new Date(at).toISOString(),
        JSON.stringify({ closure: null, preparing: prepareJobId }),
      ],
    );
    store.touch();
    return { runId, jobId: prepareJobId };
  }

  /**
   * ONE EXPLORE, IN THE FIVE STEPS A BABEL RUN IS MADE OF.
   *
   *   1. the SELECTION, archived captures from the catalog and nothing else;
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
    jobs: BabelJobs,
    engine: CodeEngine,
    input: LaunchInput,
    plan: RunPlan,
    analysis?: AnalysisWork,
  ): Promise<Started> {
    if (analysis !== undefined) {
      const parsed = AnalysisWorkSchema.safeParse(analysis);
      if (!parsed.success) return { refused: "invalid analysis authority" };
      if (
        new TextEncoder().encode(JSON.stringify(parsed.data.brief)).byteLength >
        ANALYSIS_BRIEF_BYTE_LIMIT
      ) {
        return { refused: "the analysis brief exceeds its whole-record byte bound" };
      }
      analysis = parsed.data;
    }
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
    const window = await selection(input, analysis);
    // What the preparation is handed besides its captures, which are added to it last.
    const base = {
      runId: `${identity.runId}_material`,
      machineId: input.machineId,
      ...(input.agentSessions === undefined ? {} : { agentSessions: input.agentSessions }),
    };
    const prepared = captureSelection(
      base,
      window.rows,
      await materialBound(store.db, input.machineId, plan.materials ?? 1),
    );
    const excluded = window.excluded + prepared.unreadable;
    if (
      analysis !== undefined &&
      (prepared.selectors.length !== new Set(analysis.selectors).size || prepared.overBound > 0)
    ) {
      return {
        refused: "the exact analysis selection is no longer available within the material bound",
      };
    }
    if (prepared.selectors.length === 0) {
      // A window can hold sessions and offer none, in three ways that need three answers. A
      // session the catalog has not listed from the archive, or one of Babel's own runs', is
      // catalogued and not a candidate (#262, #453); a session larger than the whole material
      // bound is a candidate the lease cannot hold. Which of the three it is decides what the
      // operator does next.
      if (prepared.overBound > 0) {
        return {
          refused:
            `material_too_large: every session this window offers is larger than the ` +
            `${String(Math.round(prepared.bound / (1024 * 1024)))} MiB one preparation on ` +
            `${input.machineId} may seal (${String(prepared.overBound)} left out). A run reads ` +
            `what a job's sealed output on that machine can hold; ask for a narrower window.`,
        };
      }
      const left =
        excluded === 0
          ? ""
          : ` (${String(excluded)} of ${String(window.held)} catalogued there name no archived ` +
            `capture yet or are Babel's own runs', which a preparation does not read)`;
      return {
        refused:
          input.preset === "explore-topic"
            ? `no archived session is cited by anything filed under ${input.entityId ?? ""}${left}`
            : `the archive holds no session written in the last ${String(input.sinceDays ?? 1)} days${left}`,
      };
    }

    // THE MATERIAL IS ITS OWN JOB, and it is Babel's: `prepare` streams the selected captures
    // out of the archive, seals the normalized record stream per session and writes the index a
    // citation's digest comes from. Its id is DERIVED from the run's so a retried start posts
    // the same preparation rather than a second one over the same sessions.
    const prepareJobId = identity.materialJobId ?? materialJobId(identity.jobId);
    const built = document({ ...base, captures: prepared.captures });
    if ("refused" in built) return built;
    // Check locally eligible work before spending time sealing its material (#255).
    const checked = await engine.checkProfile(profile);
    if (!checked.ok) return { refused: checked.refused };
    const admitted = await ready(jobs, input.machineId, OPERATIONS.prepare);
    if ("refused" in admitted) return admitted;
    if (analysis !== undefined) {
      const refused = await analysisAuthority(
        analysis,
        prepareJobId,
        input.machineId,
        profile,
        recipes.map((recipe) => recipe.id),
      );
      if (refused !== null) return { refused };
    }
    // Analysis intent precedes every native post. This INSERT and the reaper's unposted
    // finish contend on the same claim, so a finished run-less grant can never post late.
    const retainParent = async () =>
      await store.db.run(
        `INSERT INTO runs(id, kind, machine_id, container_id, prepare_job_id, recipe_id, profile,
                        authority_kind, authority_id, preparation, started_at, records, payload)
         SELECT ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, 0, ?
         ${
           analysis === undefined
             ? ""
             : `WHERE EXISTS (
           SELECT 1 FROM claims WHERE id = ? AND run_id = ? AND fence = ? AND job_id = ?
             AND role = ? AND finished_at IS NULL AND expires_at > ?
         )`
         }
         ON CONFLICT(id) DO NOTHING`,
        [
          identity.runId,
          PRESET_PLANS[input.preset].operationId,
          input.machineId,
          profile.containerId,
          prepareJobId,
          launchReport(input, profile),
          analysis === undefined ? "operator" : "conductor",
          identity.authorityId,
          JSON.stringify({
            preset: input.preset,
            ...(analysis === undefined ? {} : { analysis }),
            selectors: prepared.selectors,
            selected: prepared.selectors.length,
            available: window.held,
            excluded,
            bytes: prepared.bytes,
            overBound: prepared.overBound,
            promptVersion: PROMPT_VERSION,
            recipes: recipes.map((recipe) => ({ id: recipe.id, version: recipe.version })),
            ...(analysis !== undefined
              ? {}
              : input.preset === "explore-topic"
                ? { entityId: input.entityId ?? "" }
                : { sinceDays: input.sinceDays ?? 1 }),
          }),
          new Date(deps.now()).toISOString(),
          JSON.stringify({ closure: null, preparing: prepareJobId }),
          ...(analysis === undefined
            ? []
            : [
                analysis.claim.id,
                analysis.claim.runId,
                analysis.claim.fence,
                prepareJobId,
                `analysis:${analysis.stage}`,
                new Date(deps.now()).toISOString(),
              ]),
        ],
      );
    if (analysis !== undefined) {
      const retained = await retainParent();
      if (retained.changes === 0)
        return {
          refused: "the analysis parent or its grant changed before preparation",
          pending: true,
        };
    }
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
        authorityKind: analysis === undefined ? "operator" : "conductor",
        preparation: {
          preset: input.preset,
          for: identity.runId,
          selected: prepared.selectors.length,
          available: window.held,
          excluded,
          bytes: prepared.bytes,
          overBound: prepared.overBound,
        },
      },
    );
    if (sealed !== null) {
      if (analysis === undefined) return sealed;
      await store.db.run(
        `UPDATE runs SET payload = ?, closure = ?, finished_at = ?
         WHERE id = ? AND job_id IS NULL AND closure IS NULL`,
        [
          JSON.stringify({ closure: sealed.pending ? null : "failed", reason: sealed.refused }),
          sealed.pending ? null : "failed",
          sealed.pending ? null : new Date(deps.now()).toISOString(),
          identity.runId,
        ],
      );
      return sealed;
    }

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
    try {
      if (analysis === undefined) await retainParent();
    } catch (error) {
      try {
        await jobs.cancel({
          kind: "job",
          machineId: input.machineId,
          operationId: OPERATIONS.prepare,
          jobId: prepareJobId,
        });
      } catch (cancelError) {
        return {
          refused: `Babel could not retain the parent: ${message(error)}; preparation cancellation is unconfirmed: ${message(cancelError)}`,
          pending: true,
        };
      }
      await store.db.run(
        `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ? WHERE id = ?`,
        [
          new Date(deps.now()).toISOString(),
          JSON.stringify({ closure: "failed", reason: message(error) }),
          `${identity.runId}_material`,
        ],
      );
      return {
        refused: `the preparation was cancelled because Babel could not retain its parent: ${message(error)}`,
      };
    }
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
      ...(input.inferenceLimits === undefined ? {} : { inferenceLimits: input.inferenceLimits }),
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
   * THE PROMPT ONE WAITING RUN IS POSTED WITH, chosen by the kind of run it is.
   *
   * Both kinds are composed HERE and not at the press, and for the same reason: the prompt is
   * built from the index `prepare` actually wrote — the real file names — rather than from
   * selectors the press could only guess the layout of.
   *
   * A titling run may need no prompt at all: its preparation read every sealed session's own
   * recorded title, and those are answered by the capture rather than by a model. It says how
   * many, and which named sessions are still unanswered, so the run settles without a session.
   */
  async function composeFor(
    run: PreparedRun,
    material: MaterialIndex,
  ): Promise<
    | { readonly prompt: string; readonly preparation: Record<string, unknown> }
    | { readonly recorded: number; readonly unanswered: readonly string[] }
    | { readonly refused: string }
  > {
    const intent = documentOf(run.preparation);
    const parsed =
      intent["analysis"] === undefined
        ? undefined
        : AnalysisWorkSchema.safeParse(intent["analysis"]);
    if (parsed !== undefined && !parsed.success)
      return { refused: "invalid persisted analysis authority" };
    const analysis = parsed?.data;
    if (run.kind === OPERATIONS.title) {
      // Only the sessions the preparation SEALED are named. A log that vanished between the
      // catalog and the machine is not in the material and cannot be titled; it is still on
      // the run's own list, and the settlement answers it with the reason rather than leaving
      // it for the next cycle to offer again.
      //
      // AND ONLY THE ONES STILL UNTITLED (#453). The catalog reads no transcript, so this run's
      // own preparation is what read the titles the harness recorded, and the hub ingested its
      // `sessions.json` before this wake: a session holding a title now is answered by its own
      // log, and paying a model to name it would be the guess outranking the session's word
      // about itself. It leaves the run's list, so the settlement answers exactly the sessions
      // the model was asked about and the ones it could not be.
      const offered = offeredSelectors(intent);
      const recorded = new Set(
        offered.length === 0
          ? []
          : (
              await store.db.query<{ selector: string }>(
                `SELECT selector FROM sessions WHERE title IS NOT NULL
                    AND selector IN (${offered.map(() => "?").join(", ")})`,
                offered,
              )
            ).map((row) => row.selector),
      );
      const asked = offered.filter((selector) => !recorded.has(selector));
      const open = new Set(asked);
      const subjects = material.sessions
        .filter((entry) => open.has(entry.selector))
        .map((entry) => ({ selector: entry.selector, file: entry.file }));
      if (subjects.length === 0) {
        if (recorded.size > 0) return { recorded: recorded.size, unanswered: asked };
        return {
          refused:
            `the preparation ${run.prepare_job_id ?? ""} sealed none of the ` +
            `${String(offered.length)} session(s) this run was to name`,
        };
      }
      const titles = intent["titles"];
      return {
        prompt: composeTitlePrompt({
          subjects,
          preparationId: material.preparationId,
          params: { [PARAM.runId]: run.id, [PARAM.preparation]: material.preparationId },
        }),
        preparation: {
          ...intent,
          titles: {
            ...(typeof titles === "object" && titles !== null && !Array.isArray(titles)
              ? titles
              : {}),
            selectors: asked,
          },
        },
      };
    }
    const asked = new Set(
      (Array.isArray(intent["recipes"]) ? intent["recipes"] : [])
        .map((entry) =>
          typeof entry === "object" && entry !== null
            ? String((entry as Record<string, unknown>)["id"])
            : "",
        )
        .filter((id) => id !== ""),
    );
    const cookbook = await deps.cookbook();
    const recipes = Object.values(cookbook).filter((recipe) => asked.has(recipe.id));
    if (recipes.length === 0) {
      return { refused: `this hub no longer holds the recipes this run was started with` };
    }
    if (analysis !== undefined) {
      const pinned = Array.isArray(intent["recipes"]) ? intent["recipes"] : [];
      if (
        recipes.length !== pinned.length ||
        pinned.some(
          (entry: unknown) =>
            typeof entry !== "object" ||
            entry === null ||
            !("id" in entry) ||
            !("version" in entry) ||
            !recipes.some((recipe) => recipe.id === entry.id && recipe.version === entry.version),
        )
      ) {
        return {
          refused: "the installed analysis recipe changed after preparation was authorized",
        };
      }
    }
    // AND FROM WHAT THE OPERATOR HAS TOLD BABEL (#331). `tell` wrote those rows and the
    // `policy` door reads them back; this is that same read and there is no second one. The
    // prompt quotes a bounded selection of them as evidence — `carriedSteering` is the rule —
    // and the same call says which ones, so the receipt records what the run was told.
    const told = (await store.policy()).steering;
    const params = {
      [PARAM.stage]: analysis?.stage ?? "explore",
      [PARAM.briefHypotheses]:
        analysis?.brief
          .filter((record) => record.kind === "hypothesis")
          .map((record) => record.id)
          .join(",") ?? "",
      [PARAM.briefObservations]:
        analysis?.brief
          .filter((record) => record.kind === "observation" && record.objectionTo.length === 0)
          .map((record) => record.id)
          .join(",") ?? "",
      [PARAM.briefObjections]:
        analysis?.brief
          .filter((record) => record.objectionTo.length > 0)
          .map((record) => record.id)
          .join(",") ?? "",
      [PARAM.runId]: run.id,
      [PARAM.preparation]: material.preparationId,
    };
    return {
      prompt: composeExplorePrompt({
        stage: analysis?.stage ?? "explore",
        ...(analysis === undefined
          ? {}
          : {
              related: {
                framing: "Untrusted prior claims offered to this stage; not newly served evidence.",
                records: analysis.brief,
              },
            }),
        recipes,
        sessions: material.sessions.map((entry) => ({
          selector: entry.selector,
          file: entry.file,
        })),
        preparationId: material.preparationId,
        params,
        steering: told,
      }),
      // WHAT THE PROMPT QUOTED HIM AS SAYING, ONTO THE RUN ROW (#331). The run's own document
      // is the only thing that reaches the settlement — the prompt is Code's job's input and
      // nothing reads it back — and the settlement is where the receipt is written.
      preparation: { ...intent, steering: carriedSteering(told, params) },
    };
  }

  /**
   * ONE WAITING RUN CLOSED BEFORE IT EVER HAD A SESSION, with the sentence saying why.
   *
   * A TITLING RUN ALSO ANSWERS ITS SELECTORS HERE (#342), and that is the half without which
   * this lane would be a loop: a session offered and not answered is a session the next cycle
   * offers again, so a preparation that failed would post another preparation over the same
   * batch on every wake, for ever. The row records the reason rather than a title, so the
   * work happened once and an operator can see what it cost and why it produced nothing.
   */
  async function close(
    run: PreparedRun,
    at: string,
    reason: string,
    spent = false,
    posting = false,
  ): Promise<readonly Posted[]> {
    const document = documentOf(run.preparation)["analysis"];
    const parsed = AnalysisWorkSchema.safeParse(document);
    const claim = AnalysisClaimSchema.safeParse(
      typeof document === "object" && document !== null && "claim" in document
        ? document.claim
        : undefined,
    );
    const statements: SqlStatement[] = [
      {
        sql: `UPDATE runs SET closure = 'failed', finished_at = ?, payload = ?
              WHERE id = ? AND job_id IS NULL AND closure IS NULL
                AND (? OR COALESCE(json_extract(payload, '$.posting'), 0) = 0)
              RETURNING id`,
        params: [
          at,
          JSON.stringify({
            closure: "failed",
            reason,
            ...(parsed.success ? { stage: parsed.data.stage } : {}),
          }),
          run.id,
          posting ? 1 : 0,
        ],
      },
    ];
    if (run.kind === OPERATIONS.title) {
      statements.push(
        ...titleStatements({
          runId: run.id,
          at,
          titles: declinedTitles(offeredSelectors(documentOf(run.preparation)), reason),
        }),
      );
    }
    const closed = await store.db.batch(statements);
    if ((closed[0]?.length ?? 0) === 0) return [];
    await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
    if (claim.success) {
      if (spent) await deps.coordinator.abandon({ ...claim.data, reason, now: deps.now() });
      else
        await deps.coordinator.finish({
          ...claim.data,
          cost: 0,
          outcome: "failed",
          now: deps.now(),
        });
    }
    store.touch();
    return [{ runId: run.id, refused: reason }];
  }

  /**
   * ONE TITLING RUN SETTLED WITHOUT A SESSION, because its preparation found every sealed
   * session's own recorded title and no model has anything left to name (#453).
   *
   * It completes at no cost, which is the truth — nothing reached a model — and what keeps the
   * day's title allowance from counting a reservation nobody spent. The sessions it named and
   * could not read are answered with the reason, as a closed run answers them, so none is
   * offered again; the titled ones need no answer, because their title is the catalog's now.
   */
  async function settleRecorded(
    run: PreparedRun,
    at: string,
    recorded: number,
    unanswered: readonly string[],
  ): Promise<readonly Posted[]> {
    const reason = `the preparation ${run.prepare_job_id ?? ""} sealed no reading of this session`;
    const closed = await store.db.batch([
      {
        sql: `UPDATE runs SET closure = 'completed', finished_at = ?, cost_usd = 0, payload = ?
              WHERE id = ? AND job_id IS NULL AND closure IS NULL
                AND COALESCE(json_extract(payload, '$.posting'), 0) = 0
              RETURNING id`,
        params: [at, JSON.stringify({ closure: "completed", counts: { recorded } }), run.id],
      },
      ...titleStatements({ runId: run.id, at, titles: declinedTitles(unanswered, reason) }),
    ]);
    if ((closed[0]?.length ?? 0) === 0) return [];
    await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
    store.touch();
    return [
      {
        runId: run.id,
        settled: `every session this run sealed recorded its own title (${String(recorded)}), so no model was asked`,
      },
    ];
  }

  /** A continuation spends only while the same live grant and route still authorize it. */
  async function analysisAuthority(
    analysis: AnalysisWork,
    jobId: string,
    machineId: string,
    profile: CodeProfile,
    recipes: readonly string[],
  ): Promise<string | null> {
    const policy = (await deps.coordinator.policy()).policy;
    const route = policy.review;
    if (
      !policy.enabled ||
      policy.activityWeights[analysis.stage] <= 0 ||
      route === undefined ||
      route.machineId !== machineId ||
      route.profile.containerId !== profile.containerId ||
      route.profile.expectedRevision !== profile.expectedRevision ||
      !recipes.includes(route.stageRecipes[analysis.stage] ?? "")
    ) {
      return "the current policy no longer authorizes this analysis continuation";
    }
    const held = await store.db.query<{ id: string }>(
      `SELECT id FROM claims WHERE id = ? AND run_id = ? AND fence = ? AND job_id = ?
         AND finished_at IS NULL AND expires_at > ?`,
      [
        analysis.claim.id,
        analysis.claim.runId,
        analysis.claim.fence,
        jobId,
        new Date(deps.now()).toISOString(),
      ],
    );
    if (held.length === 0) return "the analysis claim is expired, finished or taken over";
    const renewed = await deps.coordinator.renew({ ...analysis.claim, now: deps.now() });
    return renewed.outcome === "refused" ? renewed.refusal.detail : null;
  }

  /**
   * EVERY RUN WHOSE MATERIAL IS SEALED AND WHOSE SESSION IS NOT POSTED YET, posted now (#592).
   *
   * The job-inputs primitive binds a SETTLED job's output — a binding whose source is still
   * active is refused — so the session cannot be posted at the press, when `prepare` has only
   * just been handed to the machine. This is the other half: a wake that `prepare`'s own
   * settlement causes finds the run waiting on it and posts the session.
   *
   * Each waiting parent is claimed atomically after readiness checks and before posting.
   * Overlapping wakes can read the same row, but only one may post: Code has no caller
   * idempotency key. An interrupted post stays reserved, never retried.
   *
   * A PREPARATION THAT DID NOT COMPLETE CLOSES ITS RUN. There is no material to bind and no
   * second attempt that would change that: the selection is fixed and the machine has already
   * read it. Leaving the run open would leave an operator waiting on a session nothing will
   * ever post.
   */
  async function postPrepared(
    jobs: BabelJobs,
    engine: CodeEngine,
    plan: RunPlan,
  ): Promise<readonly Posted[]> {
    void jobs;
    void plan;
    const waiting = await store.db.query<PreparedRun>(
      `SELECT r.id AS id, r.kind AS kind, r.machine_id AS machine_id,
              r.container_id AS container_id, r.prepare_job_id AS prepare_job_id,
              r.profile AS profile, r.preparation AS preparation, p.closure AS prepare_closure,
              p.payload AS prepare_payload
         FROM runs r JOIN runs p ON p.job_id = r.prepare_job_id
        WHERE r.closure IS NULL AND r.job_id IS NULL AND r.container_id IS NOT NULL
          AND p.closure IS NOT NULL
          AND r.kind != '${TRANSCRIPT_MAP_SESSION_OPERATION}'
        ORDER BY r.started_at`,
    );
    const posted: Posted[] = [];
    for (const run of waiting) {
      const at = new Date(deps.now()).toISOString();
      let modelRequested = false;
      let unconfirmedJob: string | null = null;
      try {
        const material = materialOf(run.prepare_payload);
        if (run.prepare_closure !== "completed" || material === null) {
          const reason =
            run.prepare_closure === "completed"
              ? `the preparation ${run.prepare_job_id ?? ""} sealed no material this run could read`
              : `the preparation ${run.prepare_job_id ?? ""} closed as ${run.prepare_closure ?? ""}`;
          posted.push(...(await close(run, at, reason)));
          continue;
        }
        const report = documentOf(run.profile);
        const inferenceLimits = LaunchInputSchema.shape.inferenceLimits.parse(
          report["inferenceLimits"],
        );
        const intent = documentOf(run.preparation);
        const parsed =
          intent["analysis"] === undefined
            ? undefined
            : AnalysisWorkSchema.safeParse(intent["analysis"]);
        if (parsed !== undefined && !parsed.success) {
          posted.push(...(await close(run, at, "invalid persisted analysis authority")));
          continue;
        }
        const analysis = parsed?.data;
        if (
          analysis !== undefined &&
          material.sessions.some((entry) => !analysis.selectors.includes(entry.selector))
        ) {
          posted.push(
            ...(await close(run, at, "the sealed material exceeds the exact analysis selection")),
          );
          continue;
        }
        const composed = await composeFor(run, material);
        if ("recorded" in composed) {
          posted.push(...(await settleRecorded(run, at, composed.recorded, composed.unanswered)));
          continue;
        }
        if ("refused" in composed) {
          posted.push(...(await close(run, at, composed.refused)));
          continue;
        }
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
        const bytes = promptBytes(composed.prompt);
        if (bytes > PROMPT_LIMIT) {
          const reason =
            `prompt_too_large: this run's prompt is ${String(bytes)} bytes and ` +
            `${CODE_PLUGIN_ID}.runSession takes ${String(PROMPT_LIMIT)}. The analysis contract ` +
            `and the stage's schema are most of it, so what moves is Code's bound or the ` +
            `contract itself — not this selection.`;
          posted.push(...(await close(run, at, reason)));
          continue;
        }
        const profile = {
          containerId: run.container_id ?? "",
          expectedRevision: Number(report["expectedRevision"] ?? 0),
        };
        const recipes = (Array.isArray(intent["recipes"]) ? intent["recipes"] : []).flatMap(
          (recipe: unknown) =>
            typeof recipe === "object" &&
            recipe !== null &&
            "id" in recipe &&
            typeof recipe.id === "string"
              ? [recipe.id]
              : [],
        );
        if (analysis !== undefined) {
          const checked = await engine.checkProfile(profile);
          const refusal = !checked.ok
            ? checked.refused
            : await analysisAuthority(
                analysis,
                run.prepare_job_id ?? "",
                run.machine_id ?? "",
                profile,
                recipes,
              );
          if (refusal !== null) {
            posted.push(...(await close(run, at, refusal)));
            continue;
          }
        }
        // The parent is the serialization boundary; neither AsyncLocalStorage nor Code's
        // runSession deduplicates concurrent calls. Claim it only after readiness checks.
        // Activation is checked in the same transaction so disablement during preparation
        // leaves an unposted intent resumable instead of authorizing a new model session.
        const owned = await store.db.batch([
          {
            sql: `UPDATE runs SET payload = json_set(payload, '$.posting', json('true'))
             WHERE id = ? AND job_id IS NULL AND closure IS NULL
               AND COALESCE(json_extract(payload, '$.posting'), 0) = 0
               AND (SELECT json_extract(payload, '$.enabled')
                      FROM policies ORDER BY seq DESC LIMIT 1) = 1
             ${
               analysis === undefined
                 ? ""
                 : `AND EXISTS (
               SELECT 1 FROM claims WHERE id = ? AND run_id = ? AND fence = ? AND job_id = ?
                 AND finished_at IS NULL AND expires_at > ?
             )`
             }
             RETURNING id`,
            params: [
              run.id,
              ...(analysis === undefined
                ? []
                : [
                    analysis.claim.id,
                    analysis.claim.runId,
                    analysis.claim.fence,
                    run.prepare_job_id ?? "",
                    new Date(deps.now()).toISOString(),
                  ]),
            ],
          },
          {
            // Publish the uncertain interval in Watch's existing progress projection in
            // the same transaction as the marker, including a process crash before reply.
            sql: `INSERT INTO run_progress(run_id, job_id, stage, message, since, updated_at)
              SELECT id, '', 'posting unconfirmed', 'Code posting is unresolved; the job may be live. Its reservation remains held, and Stop cannot release it.', ?, ''
                FROM runs WHERE id = ? AND closure IS NULL AND job_id IS NULL
                  AND json_extract(payload, '$.posting') = 1
              ON CONFLICT(run_id) DO NOTHING`,
            params: [at, run.id],
          },
        ]);
        if ((owned[0]?.length ?? 0) === 0) continue;
        modelRequested = true;
        const answered = await engine.runSession({
          profile: {
            containerId: run.container_id ?? "",
            expectedRevision: Number(report["expectedRevision"] ?? 0),
          },
          machineId: run.machine_id ?? "",
          prompt: composed.prompt,
          prepareJobId: run.prepare_job_id ?? "",
          ...(inferenceLimits === undefined ? {} : { inferenceLimits }),
        });
        if (!answered.ok) {
          // A lost or unusable posting response is not proof that Code bought no session.
          if (answered.code === ENGINE_REFUSALS.unconfirmed) throw new Error(answered.refused);
          // A REFUSAL HERE IS FINAL, not a thing to retry on every wake for ever: the material
          // is sealed and immutable, the profile was named at the press, and nothing a later
          // wake could do changes what Code just said. The run closes carrying the sentence.
          posted.push(...(await close(run, at, answered.refused, false, true)));
          continue;
        }
        // The run's own document is the only thing that reaches the settlement — the prompt is
        // Code's job's input and nothing reads it back — so whatever the composition decided
        // this run was told travels on the row with the intent it is part of.
        try {
          if (analysis !== undefined) {
            const refusal = await analysisAuthority(
              analysis,
              run.prepare_job_id ?? "",
              run.machine_id ?? "",
              profile,
              recipes,
            );
            if (refusal !== null) throw new Error(refusal);
            const bound = await deps.coordinator.bind({
              ...analysis.claim,
              jobId: answered.value.jobId,
              previousJobId: run.prepare_job_id ?? "",
              now: deps.now(),
            });
            if (bound.outcome === "refused") throw new Error(bound.refusal.detail);
          }
          const retained = await store.db.run(
            `UPDATE runs SET job_id = ?, preparation = ?, payload = ? WHERE id = ? AND job_id IS NULL AND closure IS NULL
         ${analysis === undefined ? "" : `AND EXISTS (SELECT 1 FROM claims WHERE id = ? AND run_id = ? AND fence = ? AND job_id = ? AND finished_at IS NULL)`}
         RETURNING id`,
            [
              answered.value.jobId,
              JSON.stringify(composed.preparation),
              JSON.stringify({ closure: null, requestedAt: deps.now() }),
              run.id,
              ...(analysis === undefined
                ? []
                : [
                    analysis.claim.id,
                    analysis.claim.runId,
                    analysis.claim.fence,
                    answered.value.jobId,
                  ]),
            ],
          );
          if (retained.changes === 0)
            throw new Error("the parent or its analysis claim changed before retention");
        } catch (error) {
          unconfirmedJob = answered.value.jobId;
          let cancellation: string | null = null;
          try {
            const cancelled = await engine.cancelSession({
              containerId: run.container_id ?? "",
              jobId: answered.value.jobId,
            });
            if (!cancelled.ok) cancellation = cancelled.refused;
          } catch (cancelError) {
            cancellation = message(cancelError);
          }
          if (cancellation !== null) {
            const reason = `session ${answered.value.jobId} could not be bound or retained: ${message(error)}; cancellation is unconfirmed: ${cancellation}`;
            // Keep polling the actual job without granting it a new fence or opening its slot.
            const retained = await store.db.run(
              `UPDATE runs SET payload = ?, job_id = ? WHERE id = ? AND closure IS NULL AND job_id IS NULL`,
              [JSON.stringify({ closure: null, reason }), answered.value.jobId, run.id],
            );
            if (retained.changes === 0) throw new Error(reason);
            await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
            store.touch();
            posted.push({ runId: run.id, refused: reason });
            continue;
          }
          unconfirmedJob = null;
          posted.push(
            ...(await close(
              run,
              at,
              `the newly posted session was cancelled: ${message(error)}`,
              true,
              true,
            )),
          );
          continue;
        }
        await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [run.id]);
        store.touch();
        posted.push({ runId: run.id, jobId: answered.value.jobId });
      } catch (error) {
        if (unconfirmedJob !== null) {
          posted.push({
            runId: run.id,
            refused: `session ${unconfirmedJob} remains unconfirmed and its grant is retained: ${message(error)}`,
          });
        } else if (modelRequested) {
          // No returned job id means an interrupted transport, not a confirmed rejection.
          // Keep the durable posting marker and the parent reservation: retrying could buy
          // another session while the first is still running.
          const reason = `session posting remains unconfirmed: ${message(error)}`;
          await store.db.run(
            `UPDATE runs SET payload = json_set(payload, '$.reason', ?)
             WHERE id = ? AND closure IS NULL AND job_id IS NULL`,
            [reason, run.id],
          );
          await store.db.run(`UPDATE run_progress SET message = ? WHERE run_id = ?`, [
            reason,
            run.id,
          ]);
          store.touch();
          posted.push({ runId: run.id, refused: reason });
        } else
          posted.push(...(await close(run, at, message(error), modelRequested, modelRequested)));
      }
    }
    return posted;
  }

  return { startExplore, startBeat, startVerify, postPrepared, inferTitles };
}

/** A run waiting on its preparation, as the poster reads one. */
type PreparedRun = {
  id: string;
  /** The operation this run is: an explore, or a titling batch (#342). */
  kind: string;
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

/**
 * A free mapping wake has its own exact admission targets. A scan launch cannot lend its
 * operation grant to the private mapping service, and a reader must never acquire that grant.
 */
export function mapCatalogDoor(
  coordinator: Coordinator,
  advance: (ctx: GuestCtx, admission: TranscriptMapCatalogAdmission) => Promise<readonly string[]>,
): Door {
  return defineDoor(
    defineServerAction({
      name: ACTIONS.startMapCatalog,
      title: "Start free transcript-map catalog and planning",
      caps: ["machines:run", "operations:invoke", "services:invoke", "network:host"],
      delegates: ["machines:read", "jobs:read", "locations:write"],
      requirements: [
        { cap: "machines:run", target: ["operation"] },
        { cap: "operations:invoke", target: ["operation"] },
        { cap: "network:host", target: ["operation"] },
        { cap: "services:invoke", target: ["target"] },
      ],
      input: StartMapCatalogRequestSchema,
      result: StartMapCatalogResultSchema,
    }),
    async (ctx, { operation, target }) => {
      const { policy } = await coordinator.policy();
      if (
        !policy.enabled ||
        policy.mapping?.executorMachineId !== operation.machineId ||
        policy.mapping.sourceMachineId !== target.machineId
      )
        return {
          refused: "The enabled mapping policy must name the requested source owner and executor.",
        };
      const binding = await describeMapHost(ctx.jobs, policy.mapping, OPERATIONS.mapCatalog);
      if ("refused" in binding) return binding;
      return {
        sourceMachineId: target.machineId,
        executorMachineId: operation.machineId,
        notes: [
          ...(await advance(ctx, {
            route: policy.mapping,
            serviceBinding: binding.serviceBinding,
            resourceBindingDigest: binding.resourceBindingDigest,
          })),
        ],
      };
    },
  );
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
      const plan = deps.plan(inForce.policy, pressOperation(input.preset));
      const started =
        preset.start === "beat"
          ? await machinery.startBeat(identity, jobs, input, plan)
          : await machinery.startExplore(identity, jobs, deps.engine(ctx.actions), input, plan);
      // THE DOOR REPORTS THE SENTENCE AND NOTHING ELSE. The hub's word is for a caller inside
      // this half that must act on one refusal differently; an operator acts on the sentence.
      if ("refused" in started) return { refused: started.refused };
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
        posting: number | bigint | null;
      }>(
        `SELECT job_id, prepare_job_id, machine_id, kind, closure, container_id,
                json_extract(payload, '$.posting') AS posting
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
      const postingRefusal = `${runId} has an unresolved Code posting; its job may be live, so Stop cannot safely release the reservation`;
      if (jobId === "" && Number(run.posting) === 1) return { refused: postingRefusal };
      if (machineId === "" || (jobId === "" && !preparing)) {
        return { refused: `${runId} has no job on a machine to stop` };
      }
      // The caller was admitted at the node it POSTED; the row says which job this run is. A
      // request that authorized one job and named another is refused rather than reconciled.
      const node = preparing
        ? {
            jobId: prepareJobId,
            operationId:
              run.kind === TRANSCRIPT_MAP_SESSION_OPERATION
                ? OPERATIONS.mapPrepare
                : OPERATIONS.prepare,
          }
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
      let inference: InferenceUsage | null = null;
      let cost: number | null = preparing || container === "" ? 0 : null;
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
        const cancelled = answered.value;
        if (cancelled.jobId !== jobId || cancelled.machineId !== machineId) {
          return {
            refused: `${runId} cancellation answered a different job; no closure was applied`,
          };
        }
        // A cancellation acknowledgement is not a settlement. Leave the row and its progress
        // reachable by the conductor until the owner's final meter can be read.
        if (!["exited", "interrupted", "cancelled", "refused"].includes(cancelled.state)) {
          return {
            refused: `${runId} cancellation was requested, but its job is still ${cancelled.state}; its reservation remains held`,
          };
        }
        if (
          cancelled.state === "exited" &&
          (cancelled.result?.exitCode == null || cancelled.result.exitCode === 0)
        ) {
          return {
            refused: `${runId} completed before cancellation; its receipt and charge await reconciliation`,
          };
        }
        inference = cancelled.result?.usage?.inference ?? null;
        cost = inference === null ? null : inference.costMicros / 1_000_000;
      }
      // Close only after cancellation is terminal. A missing Code meter is unknown spend,
      // not a free run: keep the run's cost null and charge each claim's reservation.
      const at = new Date(deps.now()).toISOString();
      const closed = await store.db.run(
        // `AND closure IS NULL` for the same reason the read above refuses a closed run: two
        // stops, or a stop racing the run's own ending, write the first closure and not the
        // second. For a PREPARING run this write is the whole stop: it is the row the posting
        // wake reads, so once it is closed no session can be posted for it.
        `UPDATE runs SET closure = 'stopped', finished_at = ?, payload = ?, cost_usd = ?, tokens = ?
          WHERE id = ? AND closure IS NULL
            AND (NOT ? OR (job_id IS NULL AND COALESCE(json_extract(payload, '$.posting'), 0) = 0))
            AND job_id IS ? AND machine_id IS ? AND container_id IS ? AND kind = ?`,
        [
          at,
          JSON.stringify({
            closure: "stopped",
            stoppedBy: ctx.principal.id,
            reason,
            stoppedAt: at,
            ...(inference === null ? {} : { inference }),
          }),
          cost,
          inference === null ? null : inference.inputTokens + inference.outputTokens,
          runId,
          preparing ? 1 : 0,
          run.job_id,
          run.machine_id,
          run.container_id,
          run.kind,
        ],
      );
      if (closed.changes === 0) {
        return {
          refused: preparing
            ? postingRefusal
            : `${runId} changed while cancellation was awaited; no closure was applied`,
        };
      }
      await store.db.run(`DELETE FROM run_progress WHERE run_id = ?`, [runId]);
      const open = await store.db.query<{
        id: string;
        run_id: string;
        fence: number | bigint;
        reserved_cost: number;
      }>(
        `SELECT id, run_id, fence, reserved_cost FROM claims WHERE job_id = ? AND finished_at IS NULL`,
        [jobId],
      );
      for (const claim of open) {
        await deps.coordinator.finish({
          id: claim.id,
          runId: claim.run_id,
          fence: claim.fence,
          cost: cost ?? Number(claim.reserved_cost),
          outcome: "skipped",
        });
      }
      store.touch();
      return { runId, jobId, machineId, closure: "stopped" as const };
    },
  );

  const verify = defineDoor(
    defineServerAction({
      name: ACTIONS.verify,
      title: "Verify the archive on a machine",
      caps: VERIFY_CAPS,
      delegates: LAUNCH_DELEGATES,
      input: VerifyRequestSchema,
      result: VerifyResultSchema,
    }),
    async (ctx, input) => {
      // The node this request is authorized at has to be the node it is about, for the reason
      // `launch` states: the host discharges the requirement against the raw arguments, so a
      // request whose two halves disagree is answered as itself rather than folded into a
      // later refusal.
      if (
        input.operation.machineId !== input.machineId ||
        input.operation.operationId !== MACHINE_OPERATIONS.verify
      ) {
        return {
          refused:
            `this verification names ${input.machineId}/${MACHINE_OPERATIONS.verify} and asks ` +
            `for authority at ${input.operation.machineId}/${input.operation.operationId}`,
        };
      }
      const inForce = await deps.coordinator.policy();
      if (!inForce.policy.enabled) {
        // A disabled policy ingests nothing, so the job would run on the machine and its
        // verdict would never be folded into the run row the operator reads it from.
        return {
          refused:
            `the evaluation policy in force (${inForce.version}) is disabled, so no receipt ` +
            `would be ingested and the verification's verdict would go nowhere`,
        };
      }
      const minted = await ctx.newId();
      const started = await machinery.startVerify(
        { runId: `run_${minted}`, jobId: `job_${minted}`, authorityId: ctx.principal.id },
        deps.jobs(ctx),
        input,
        deps.plan(inForce.policy, MACHINE_OPERATIONS.verify),
      );
      if ("refused" in started) return { refused: started.refused };
      return { ...started, machineId: input.machineId };
    },
  );

  return [profiles, launch, verify, stop];
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * THE HUB'S OWN WORD FOR A POSTING IT REFUSED, taken off the thing it threw.
 *
 * The hub raises its vocabulary bare — `throw new Error("job_digest_conflict")` in
 * `packages/server/src/job-store.ts` — and across the isolate boundary the kit re-raises it as
 * a {@link HostCallError} whose `detail` is that word VERBATIM and whose `message` has the host
 * method glued in front of it. So the word is read off the error's own fields, never off a
 * sentence: `post`'s sentence is written for the operator and names the machine first, and a
 * caller matching on it would have made the wording a contract.
 *
 * A TYPED REFUSAL IS READ FIRST, so the day the SDK grows one this function reads it and no
 * other file changes. `refusal` is the name the hub already uses for the word it refuses a
 * posting with — `authority.decision.refusal` on the job it answers when the refusal is
 * returned rather than raised (`JobRunState`) — and a raised one would arrive spelled the same.
 *
 * It is exported for one reader: the launch DOUBLE the drain controller's own tests run
 * against. A fake that derived the word differently would keep those tests green over a
 * `post` that had stopped carrying it.
 */
export function hubRefusal(error: unknown): string {
  if (!(error instanceof Error)) return String(error);
  const typed: unknown = Reflect.get(error, "refusal");
  if (typeof typed === "string" && typed !== "") return typed;
  return error instanceof HostCallError ? error.detail : error.message;
}

export function nativeFailureToken(
  error: unknown,
  method: "jobs.execute" | "jobs.status",
): string | undefined {
  if (error instanceof HostCallError) return error.method === method ? error.detail : undefined;
  if (
    error instanceof Error &&
    error.name === "ServiceError" &&
    Reflect.get(error, "code") === "forbidden"
  )
    return error.message;
  return undefined;
}

/**
 * Pinned hub JobService.build rejects these before reservation. A typed hub error alone is not
 * enough: execute can also throw after commit while notifying or dispatching. A status probe
 * after an arbitrary transport failure cannot prove the request was never admitted.
 */
export function nativeAdmissionRefusal(error: unknown): boolean {
  switch (nativeFailureToken(error, "jobs.execute")) {
    case "unknown_operation":
    case "installation_changed":
    case "resource_bindings_changed":
    case "invalid_revisioned_input":
    case "invalid_input":
    case "invalid_limits":
    case "limit_exceeded":
    case "output_parent_changed":
    case "duplicate_output":
    case "invalid_output_binding":
      return true;
    default:
      return false;
  }
}
