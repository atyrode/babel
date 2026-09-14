import { z } from "zod";
import { defineServerAction, type GuestCtx } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  INFERENCE_SERVICE,
  INPUT_FIELD,
  LaunchInputSchema,
  LaunchRequestSchema,
  LaunchResultSchema,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  PRESET_OPERATIONS,
  PRESET_REACHES_MODEL,
  StopInputSchema,
  StopResultSchema,
  type LaunchInput,
  type OperationName,
  type SessionChoice,
} from "../contract.ts";
import type { Coordinator, Policy } from "../store/coordinator.ts";
import type {
  Conductor,
  JobLaunch,
  JobsSlice,
  MachineReadiness,
  MachinesSlice,
  Recipe,
  RunPlan,
} from "../server/conductor.ts";
import {
  launchProfile,
  perRunUsd,
  sessionInputs,
  sessionPreview,
  sessionShortfall,
  unreadable,
  type BabelJobs,
  type ServicesSlice,
} from "../server/plan.ts";
import type { BabelStore } from "../store/store.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE THREE DOORS WATCH POSTS TO: read what a run would be, start one thing, stop one thing.

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

  WHY `launch` DECLARES `machines:run` AT A NODE. A governed capability is granted at a NODE and
  never over a workspace (ADR 0035), and the dispatcher attenuates a door's `ctx.jobs` to exactly
  the capabilities the door DECLARED before the handler runs. A `launch` declaring only
  `containers:write` therefore reached `ctx.jobs.describe` with no `machines:run` at all and was
  refused `governed_authority_refused` on the first machine it asked about — the door was not
  denied, its own hands were empty.

  So the door declares `machines:run` and pairs it with `requirements: [{cap, target:
  ["operation"]}]`. The host walks `operation` through the RAW arguments, parses it as a
  `ManifoldRef`, discharges `machines:run` there through the ordinary waterfall, and then admits
  the dispatch against the operator's version-bound CONSENT for this plugin at that node. That is
  why the panel posts the node instead of a machine id: a door with a governed cap and no
  requirement is refused outright ("governed actions require resource targets and explicit
  consent"), and a requirement whose target is a bare string is `invalid authority target`.

  `jobs:read` and the two `locations:` capabilities are DELEGATES rather than caps: they are the
  native ceiling this door's job authority carries, not a second thing to ask the caller for.
  The engine re-derives them at the effect — `execute` requires `machines:run` at the operation
  and `locations:read`/`locations:write` at every location the operation declares, each against
  its own consent — and the credential the job then carries is THIS attenuated authority, which
  is what `onJobSettled` later reads the sealed outputs back with. Declaring them as caps would
  demand a requirement apiece (assembly refuses caps and requirements that do not pair) and so a
  consent for `jobs:read` at a node nobody asks a question about.

  THE DRY READ IS ITS OWN DOOR. `launchPreview` answers what a run would be — the recorded
  profile, the model, the ceilings — under `containers:read`, describing no machine and starting
  no job. It cannot live on `launch`: the requirement is discharged before the handler is
  entered, so a preview there would need a node AND the operator's consent at it merely to say
  what a launch would cost, and Watch polls it while he is still choosing.
*/

/** Governed, at the operation node the request names; see the block above. */
const LAUNCH_CAPS = ["machines:run"] as const;
const LAUNCH_REQUIREMENTS = [{ cap: "machines:run" as const, target: ["operation"] }];
/**
 * The native ceiling the launched job inherits: reading it back, and its declared locations.
 * `machines:read` is NOT here and cannot be: `NATIVE_DELEGATE_CAPS` does not admit it (see
 * `doors/read.ts`), so the credential a drawn job carries into `onJobSettled` does not carry it
 * either — and a settled hook is served no machines slice to spend it through in any case.
 */
const LAUNCH_DELEGATES = ["jobs:read", "locations:read", "locations:write"] as const;

/** Stopping is governed at the JOB node, which the run row carries and the panel posts. */
const STOP_CAPS = ["jobs:cancel"] as const;
const STOP_REQUIREMENTS = [{ cap: "jobs:cancel" as const, target: ["job"] }];

/** A dry read of the store and the policy; it asks no machine anything. */
const PREVIEW_CAPS = ["containers:read"] as const;

/** What a preset is, in one row: the run's kind, the operation it becomes, how it is started. */
type Start = "explore" | "draw" | "beat";
interface PresetPlan {
  readonly kind: "explore" | "evaluate" | "conductor" | "prepare";
  readonly operationId: OperationName;
  readonly start: Start;
}

const PRESET_PLANS: Record<LaunchInput["preset"], PresetPlan> = {
  "read-whats-new": { kind: "explore", operationId: PRESET_OPERATIONS["read-whats-new"], start: "explore" },
  "explore-topic": { kind: "explore", operationId: PRESET_OPERATIONS["explore-topic"], start: "explore" },
  "review-backlog": { kind: "evaluate", operationId: PRESET_OPERATIONS["review-backlog"], start: "draw" },
  "file-and-tidy": { kind: "evaluate", operationId: PRESET_OPERATIONS["file-and-tidy"], start: "draw" },
  "keep-going": { kind: "conductor", operationId: PRESET_OPERATIONS["keep-going"], start: "beat" },
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

/**
 * WHAT A CALLER WITH NO SERVICES SLICE IS TOLD. It is the truthful answer rather than a
 * pessimistic one: nobody was asked, so nothing is known about whether a policy is installed.
 */
const PREVIEW_WITHOUT_SERVICES =
  "this caller holds no services slice, so the machine's service configuration was never read";

/*
  WHICH PRESETS REACH A MODEL, and therefore need a session (#279), is `PRESET_REACHES_MODEL` in
  the contract rather than a table here: the panel hides its session picker by the same answer
  this door refuses a session-less launch by, and two tables would be two answers.
*/

/** Babel's own launch profile, as the `runs` row records it and `lastProfile` reads it back. */
const LaunchProfileSchema = z.object({
  model: z.string().default(""),
  thinking: z.string().default(""),
  account: z.string().default(""),
});

/**
 * WHAT A CALLER THAT IS NOT A DISPATCH BRINGS INSTEAD OF A `ctx` (#258).
 *
 * The `launch` door mints these from `ctx.newId()` and names the operator as the authority. A
 * drain's controller cannot: it runs inside `cycle()`, and one of that function's two real wakes
 * is `onJobSettled`, whose `GuestJobSettledCtx` carries storage, the database and the settled
 * job's own authority — no `newId`, no `principal`. So the ids and the authority are PARAMETERS
 * here, which also makes them deterministic for a controller that wants a retried tick to
 * re-post the same job rather than a second one.
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
 * The drain controller (#258) launches explores and beats by the same claims, the same receipts
 * and the same `runs` rows as the button does, because two implementations of "start a run"
 * disagreeing is how an operator's ceiling gets spent twice — which is the whole subject of the
 * post-mortem this issue comes from. So the door's own body is this object's methods, and the
 * door is a thin wrapper that adds the authority check and the ids.
 */
export interface LaunchMachinery {
  /** What a preset on a machine would be, before it is anything: the answer both doors share. */
  prospect(
    input: LaunchInput,
    services?: ServicesSlice | undefined,
  ): Promise<{
    plan: RunPlan;
    policy: Policy;
    version: string;
    answer: Omit<z.infer<typeof LaunchResultSchema>, "runId" | "jobId">;
  }>;
  /** One explore over the preset's own inline selection, posted and recorded. */
  startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
  /** One beat — a `scan` — posted and recorded. It reaches no model and carries no session. */
  startBeat(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started>;
}

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
  /** The one machine question a cycle asks outside a job: what a catalogued folder is (#535). */
  machines(ctx: GuestCtx): MachinesSlice;
  /**
   * What the machine's owner installed under a service id, so a preview can say whether there
   * is a lane to a model at all and what it is priced at (ADR 0038). It is a slice for the
   * same reason the others are: an ordinary dispatch is not admitted to read a machine's
   * service configuration, and that refusal is an ANSWER here rather than a failure.
   */
  services(ctx: GuestCtx): ServicesSlice;
  /** What a run of this operation runs under, given the policy in force and the session asked. */
  plan(policy: Policy, operationId: OperationName, session?: SessionChoice | undefined): RunPlan;
  /** One cycle of the loop over this dispatch's slices: the same conductor the plugin wires. */
  cycle(jobs: JobsSlice, machines: MachinesSlice, plan: RunPlan): Conductor;
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

/** What one preset's window offered: the scope, its size, and what it was not allowed to read. */
interface Selected {
  readonly rows: readonly SessionRow[];
  /** Every catalogued session the window held, selectable or not. */
  readonly held: number;
  /** How many of those are live or Babel's own, and so were never candidates (#262). */
  readonly excluded: number;
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

/**
 * THE SESSION A RUN THAT REACHES A MODEL MUST NAME, or the refusal (#279).
 *
 * Two refusals and not one, because the operator does two different things about them: a
 * request with no session at all is a picker he has not used yet, and a session whose model and
 * account name two different providers is a pair that cannot resolve — omp routes by the
 * model's provider and the broker verifies the pool under the account's, so such a run would
 * fail on the machine with `account_unavailable` after the job was posted and a claim taken.
 */
function sessionFor(input: LaunchInput, plan: RunPlan): SessionChoice | { refused: string } {
  const session = input.session ?? plan.session;
  if (session === null || session === undefined) {
    return {
      refused:
        `session_required: ${input.preset} reaches a model, so the launch must name one — a ` +
        `model, a thinking level and the account to spend`,
    };
  }
  const shortfall = sessionShortfall(session);
  return shortfall === "" ? session : { refused: shortfall };
}

export function launchMachinery(store: BabelStore, deps: LaunchDeps): LaunchMachinery {
  /**
   * What actually ran on this machine, most recently: the launch profile the newest receipt
   * there recorded — the model asked for, the thinking level, the account spent.
   *
   * It is per machine and it is a RECORDED figure, both for the same reason. A block assembled
   * from what this hub WOULD ask for would be the one line nobody had checked, and a figure
   * from another machine would be a claim about a run nobody made there. A machine that has run
   * nothing answers `null`, which is the honest shape of "it will tell us when it starts".
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
      const described = LaunchProfileSchema.safeParse(block);
      if (!described.success) continue;
      const { model, thinking, account } = described.data;
      if (model === "" && account === "") continue;
      return { model, thinking, account };
    }
    return null;
  }


  /**
   * The sessions one explore is prepared over, newest first, how many the window held, and how
   * many of those a preparation may not read.
   *
   * WHAT IT NEVER SELECTS (#262). A row `scan` marked `live` is a log that was still being
   * appended when it was catalogued: its digest is already stale and a run reading it would
   * report "changed since the preparation was fixed", which is what every explore of
   * 2026-09-13 reported. That one is unconditional — a moving file is not a scope. A row
   * marked `kind = 'agent'` is one of Babel's own runs' transcripts, catalogued and archived
   * like every other session (#177) and left out of a preset that reads the operator's work;
   * `agentSessions` is how a preset that studies Babel itself (#270) asks for them.
   *
   * `held` is what the window CONTAINED, exclusions included, so `excluded` is a number the
   * run row and the refusal can both state: "there is nothing here" and "there is nothing here
   * a run may read" are different facts, and the day this issue comes from was two hours of
   * reading an adjacent number as the one that was asked for.
   *
   * One WHERE, two predicates: the preset decides what the window IS, and the exclusion applies
   * to whichever window that is. Two queries that spelled the same join differently would be
   * two chances for the count and the selection to disagree.
   */
  async function selection(input: LaunchInput): Promise<Selected> {
    const limit = MAX_SELECTION;
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
    const selectable = `AND s.live = 0${input.agentSessions ? "" : " AND s.kind = 'operator'"}`;

    const rows = await store.db.query<SessionRow>(
      `SELECT DISTINCT s.selector AS selector, s.harness AS harness, s.source_id AS source_id,
              s.content_digest AS content_digest, s.snapshot_id AS snapshot_id, s.seen_at AS seen_at
         ${scope} ${selectable}
        ORDER BY s.seen_at DESC, s.selector
        LIMIT ?`,
      [...params, limit + 1],
    );
    const counted = await store.db.query<{ held: number; selectable: number }>(
      `SELECT count(DISTINCT s.selector) AS held,
              count(DISTINCT CASE WHEN s.live = 0${input.agentSessions ? "" : " AND s.kind = 'operator'"}
                                  THEN s.selector END) AS selectable
         ${scope}`,
      [...params],
    );
    const held = Number(counted[0]?.held ?? 0);
    const allowed = Number(counted[0]?.selectable ?? 0);
    return { rows: rows.slice(0, limit), held, excluded: Math.max(0, held - allowed) };
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
      readonly profile: Record<string, string>;
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

  /**
   * The launch document and every other job input, checked against the ceiling the engine holds
   * one job's whole input record to.
   *
   * The session's three fields are counted here rather than assumed small, because the bound is
   * over the RECORD: `JobRequestSchema.input` is 65,536 bytes for all of it, and the owner's
   * materialization of `models`/`config` into the job's home is bounded by the same number
   * again (`agent/src/job-inputs.ts`). A document sized against `input` alone would post a job
   * the machine refuses for a reason the operator cannot act on.
   */
  function document(
    value: unknown,
    session: SessionChoice | null,
  ): { input: Record<string, string> } | { refused: string } {
    const text = JSON.stringify(value);
    const input: Record<string, string> = {
      [INPUT_FIELD]: text,
      ...(session === null ? {} : sessionInputs(session)),
    };
    const bytes = new TextEncoder().encode(JSON.stringify(input)).byteLength;
    if (bytes > MAX_INPUT_BYTES) {
      return {
        refused:
          `this launch's document is ${String(bytes)} bytes and one job's input holds ` +
          `${String(MAX_INPUT_BYTES)}: ask for fewer recipes, or a narrower window`,
      };
    }
    return { input };
  }

  /**
   * What a preset on a machine would be, before it is anything: the answer both doors share.
   *
   * `services` is optional because only a dispatch holds one. A caller with none — the drain's
   * controller, which asks for the plan and not for the sentence above a button — gets the
   * `unreadable` answer for the session block, which is the truthful one: nobody was asked.
   */
  async function prospect(
    input: LaunchInput,
    services?: ServicesSlice | undefined,
  ): Promise<{
    plan: RunPlan;
    policy: Policy;
    version: string;
    answer: Omit<z.infer<typeof LaunchResultSchema>, "runId" | "jobId">;
  }> {
    const preset = PRESET_PLANS[input.preset];
    const inForce = await deps.coordinator.policy();
    const policy = inForce.policy;
    const plan = deps.plan(policy, preset.operationId, input.session);
    const reader = services ?? unreadable(PREVIEW_WITHOUT_SERVICES);
    return {
      plan,
      policy,
      version: inForce.version,
      answer: {
        machineId: input.machineId,
        kind: preset.kind,
        profile: await lastProfile(input.machineId),
        ceiling: { perRunUsd: perRunUsd(policy), perDayUsd: policy.dailyCost },
        session: sessionPreview({
          session: input.session ?? null,
          outcome: await reader.policy(input.machineId, INFERENCE_SERVICE.serviceId),
          ceilingMicros: plan.limits.inference?.costMicros ?? null,
        }),
      },
    };
  }

  /** The pins a posting carries, or the sentence naming why this machine cannot run this. */
  async function ready(
    jobs: JobsSlice,
    machineId: string,
    operationId: string,
  ): Promise<
    | { pinned: { installationRevision?: string; artifactSha256?: string } }
    | { refused: string }
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
    const built = document(
      { runId: identity.runId, machineId: input.machineId, roots: [], harnesses: [] },
      null,
    );
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
        // A beat reaches no model, so its profile is the honest empty one rather than a
        // session it never spent.
        profile: launchProfile(null),
        authorityId: identity.authorityId,
        preparation: { preset: input.preset, minutes: input.minutes ?? 0 },
      },
    );
    if (refusal !== null) return refusal;
    return { runId: identity.runId, jobId: identity.jobId };
  }

  async function startExplore(
    identity: LaunchIdentity,
    jobs: JobsSlice,
    input: LaunchInput,
    plan: RunPlan,
  ): Promise<Started> {
    const preset = PRESET_PLANS[input.preset];
    const chosen = sessionFor(input, plan);
    if ("refused" in chosen) return chosen;
    const admitted = await ready(jobs, input.machineId, preset.operationId);
    if ("refused" in admitted) return admitted;

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
      // A window can hold sessions and offer none: a log still being written, or one of
      // Babel's own runs', is catalogued and not a candidate (#262). Which of the two it is
      // decides what the operator does next, so the refusal says it.
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
    const built = document(
      {
        runId: identity.runId,
        machineId: input.machineId,
        engine: { binary: plan.engine.binary, args: plan.engine.args ?? [], cwd: "" },
        session: chosen,
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
      },
      chosen,
    );
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
        limits: plan.limits,
        ...admitted.pinned,
      },
      {
        runId: identity.runId,
        kind: preset.operationId,
        recipeId: recipes[0]?.id ?? "",
        profile: launchProfile(chosen),
        authorityId: identity.authorityId,
        preparation: {
          preset: input.preset,
          selected: prepared.rows.length,
          available: prepared.held,
          excluded: prepared.excluded,
          ...(input.preset === "explore-topic"
            ? { entityId: input.entityId ?? "" }
            : { sinceDays: input.sinceDays ?? 1 }),
          recipes: recipes.map((recipe) => ({ id: recipe.id, version: recipe.version })),
        },
      },
    );
    if (refusal !== null) return refusal;
    return { runId: identity.runId, jobId: identity.jobId };
  }

  return { prospect, startExplore, startBeat };
}

export function launchDoors(store: BabelStore, deps: LaunchDeps): readonly Door[] {
  const machinery = launchMachinery(store, deps);

  const preview = defineDoor(
    defineServerAction({
      name: ACTIONS.launchPreview,
      title: "Read what a run would be",
      caps: PREVIEW_CAPS,
      input: LaunchInputSchema,
      result: LaunchResultSchema,
    }),
    async (ctx, input) => {
      const { answer } = await machinery.prospect(input, deps.services(ctx));
      return { runId: "", jobId: "", ...answer };
    },
  );

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
    async (ctx, input) => {
      const preset = PRESET_PLANS[input.preset];
      const { plan, policy, version, answer } = await machinery.prospect(
        input,
        deps.services(ctx),
      );
      // The host discharged `machines:run` at the node in the ARGUMENTS; this is the only place
      // that can say the node is the one this request is actually about. The engine re-checks
      // consent at the operation it is really asked to run, so a mismatch is never authority
      // this earns — it is a request whose two halves disagree, and answering it would be
      // starting something the operator did not authorize by name.
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
      if (!policy.enabled) {
        return {
          refused:
            `the evaluation policy in force (${version}) is disabled, so Babel starts ` +
            `nothing; enable it and the launch runs under its ceilings`,
        };
      }
      // A preset that reaches a model is refused here, before a machine is described, when the
      // request names no session: nothing about a model's availability changes the answer.
      if (PRESET_REACHES_MODEL[input.preset]) {
        const chosen = sessionFor(input, plan);
        if ("refused" in chosen) return chosen;
      }

      const jobs = deps.jobs(ctx);

      if (preset.start === "draw") {
        // The coordinator decides what is reviewed and the conductor claims and dispatches it;
        // this only says how many cycles the operator asked for. A cycle that draws nothing
        // ends the loop rather than spinning: the second cycle would draw the same nothing.
        const described = await host(jobs, input.machineId, preset.operationId);
        if ("refused" in described) return described;
        const loop = deps.cycle(jobs, deps.machines(ctx), plan);
        const requested: { runId: string; jobId: string; machineId: string }[] = [];
        let why = "";
        for (let cycle = 0; cycle < Math.min(input.draws ?? 1, MAX_CYCLES); cycle += 1) {
          const report = await loop.tick();
          requested.push(...report.requested);
          if (report.requested.length === 0) {
            // A PARKED LOOP IS ALSO THIS DOOR'S ANSWER. There is one loop and one park (F16:
            // the Go tree let a scripted fan bypass a parked conductor), so an operator who
            // presses the button hears the loop's own verdict first, in its own words.
            why =
              report.parked?.reason ??
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
      const identity: LaunchIdentity = {
        runId: `run_${minted}`,
        jobId: `job_${minted}`,
        authorityId: ctx.principal.id,
      };
      const started =
        preset.start === "beat"
          ? await machinery.startBeat(identity, jobs, input, plan)
          : await machinery.startExplore(identity, jobs, input, plan);
      if ("refused" in started) return started;
      return { ...started, ...answer };
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

  return [preview, launch, stop];
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
