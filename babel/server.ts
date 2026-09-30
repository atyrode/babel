import { AsyncLocalStorage } from "node:async_hooks";
import { createHash } from "node:crypto";
import {
  defineServerPlugin,
  type GuestCtx,
  type GuestDatabase,
  type GuestStorage,
  type ServerHandler,
  type ServerPluginDef,
} from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  BeatChainSchema,
  beatChainKey,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  INPUT_FIELD,
  MACHINE_OPERATIONS,
  MAP_DRAIN_PRESET,
  OPERATIONS,
  RECALL_SERVICE_ID,
  type OperationName,
  TRANSCRIPT_MAP_CATALOG_ADMISSION_KEY,
  TranscriptMapCatalogAdmissionSchema,
  TranscriptMapConfigSchema,
  type TranscriptMapCatalogAdmission,
} from "./contract.ts";
import { babelDoors } from "./doors/index.ts";
import { declaredServices } from "./doors/services.ts";
import { enableChain, launchMachinery, principalChain, type LaunchDeps } from "./doors/launch.ts";
import type { Recipe } from "./server/engine/prompts.ts";
import { codeEngine, type ActionsSlice } from "./server/engine/session.ts";
import { drainTick, type DrainDeps } from "./server/drain.ts";
import { liveDrainCapacity, type DrainAdmission } from "./server/drain-admission.ts";
import { embedder, type EmbeddingServices } from "./server/embed.ts";
import {
  BEAT_OPERATION,
  CONDUCTOR_SCHEDULE_ID,
  conductor,
  describeHost,
  describeMapHost,
  reconcileCitationBackfill,
  SCHEDULE_LIFETIME_MS,
  STANDING_RUN,
  type Conductor,
  type KeysSlice,
  type MachinesSlice,
  type RunPlan,
} from "./server/conductor.ts";
import {
  ENABLE_WITHOUT_JOBS,
  HOOK_WITHOUT_MACHINES,
  jobCeiling,
  jobsSlice,
  machinesSlice,
  runPlan,
  unaskable,
  unauthorized,
  type BabelJobs,
} from "./server/plan.ts";
import { coordinator, perMachineBound, type Policy } from "./store/coordinator.ts";
import { ReviewReadings, type ReadingMetadata } from "./server/review-readings.ts";
import { SCHEMA_ADDITIONS, SCHEMA_V1 } from "./store/schema.ts";
import { ensureTerms } from "./store/corpus.ts";
import { activeDrains, deadlineOf, readDrain, type DrainRow } from "./store/drains.ts";
import { openStore } from "./store/store.ts";
import manifestJson from "./manifest.json";

/*
  THE SERVER HALF OF atyrode.babel: the store, the doors over it, and the loop behind them.

  The store is the plugin's own SQLite file (ADR 0034), asked for by `database` in the manifest
  and served as `ctx.database`. Three facts about that handle shape this file:

  - IT ARRIVES WITH A CONTEXT, not at import. The doors are built once, at load, from one
    `BabelStore`; so the store is opened over a FACADE whose three verbs delegate to whichever
    handle the current dispatch (or the enable hook) holds.
  - A HARDENED ROW'S HANDLE DIES WITH ITS REQUEST (the kit's call factory is closed once the
    request has answered), so the facade resolves per dispatch through an `AsyncLocalStorage`
    rather than capturing one handle for the process. In-realm the engine's handle is the same
    object every time and the store below it never notices the difference.
  - THE TABLES ARE MADE IN `onEnable`, not by a migration. `planDataMigration` answers `ok` for
    a plugin whose stored data version is null — a fresh install — so a migration chain never
    runs on a first enable; the chain exists for a MAJOR bump over data that already exists. The
    name of the shape this enable leaves is recorded as a key so the next one has a predecessor
    to read. An ADDITIVE shape — a column with a default or a table nothing older reads, a MINOR
    version, which `planDataMigration` passes both ways and runs nothing for — is applied here
    too, by name: `SCHEMA_ADDITIONS` is what a store an earlier enable created is missing, and a
    fresh one already has.

  THE LOOP HAS NO CLOCK. A plugin may not poll as an alternate scheduler (`docs/PLUGINS.md`),
  and a server half has no timer of its own. Enable, a settled native job or a write-authorized
  policy installation or launch may run a full conductor cycle. Read-only pulse, runs and drain
  status dispatches observe progress at a thirty-second floor without posting work; neither
  a reader nor a burst of panel polls can authorize a new Code session.
*/

/**
 * The name of the shape an enable leaves behind: `SCHEMA_V1` plus every column, table, index and
 * trigger `SCHEMA_ADDITIONS` names. `STORE_DATA_VERSION` is the version it reaches, and
 * `2026-09-28-store-v1-history-indexes` — recorded under the same key by the enable before it — is
 * its predecessor.
 */
const STORE_MIGRATION = "2026-09-29-store-v1-review-actions";
/** Where that name is recorded. The engine's own `$migration:` ledger is the engine's to write. */
const SCHEMA_KEY = "schema";
/** One table of the schema, asked for by name: present means this file has been created. */
const SENTINEL_TABLE = "records";

/**
 * WHAT A CALL'S OWN CONTEXT SERVES THIS PLUGIN: its tables, keys and optional reading metadata.
 *
 * These handles belong to this call (a hardened row closes them once the request has answered),
 * so they are resolved through `AsyncLocalStorage`, not captured for a later wake. Only tables
 * and keys have an enable fallback; reading metadata always comes from the current wake.
 */
type Bound = {
  readonly database: GuestDatabase;
  readonly storage: GuestStorage;
  readonly readingMetadata?: ReadingMetadata;
  readonly machines?: Pick<GuestCtx["machines"], "inventory"> | undefined;
  readonly jobs?: Pick<GuestCtx["jobs"], "listRuns"> | undefined;
};

const dispatched = new AsyncLocalStorage<Bound>();
/** Only the enabled store and keys may outlive a call, never its metadata authority. */
let enabled: Pick<Bound, "database" | "storage"> | undefined;

function bound(): Bound {
  const held = dispatched.getStore() ?? enabled;
  if (held === undefined) {
    throw new Error(`${BABEL_PLUGIN_ID}: no database is bound to this call`);
  }
  return held;
}

/**
 * The store's view of the database: the ADR's three verbs, each answered by the handle that
 * belongs to the call making it. Bounds and refusals stay the engine's — this adds nothing but
 * the indirection the point above requires.
 */
const database: PluginDatabase = {
  pluginId: BABEL_PLUGIN_ID,
  query: async <Row extends SqlRow = SqlRow>(
    sql: string,
    params?: readonly SqlParam[],
  ): Promise<readonly Row[]> => await bound().database.query<Row>(sql, params),
  run: async (sql: string, params?: readonly SqlParam[]) => await bound().database.run(sql, params),
  batch: async (statements: readonly SqlStatement[]) => await bound().database.batch(statements),
};

/** The loop's view of this plugin's keys: where the day's tally of unspent cycles is kept. */
const keys: KeysSlice = {
  get: async (key: string): Promise<string | null> => await bound().storage.get(key),
  set: async (key: string, value: string): Promise<void> => {
    await bound().storage.set(key, value);
  },
};

const store = openStore(database);
const manifest = PluginManifestSchema.parse(manifestJson);
/**
 * THE CEILING, READ ONCE, FROM THE MANIFEST THIS BUNDLE SHIPS: `limits.concurrentJobs` on the
 * operations it declares, or `null` when none of them declares one, which is the state today
 * (#279) — the two that did were the two a launcher posted. The coordinator governs inside it
 * and the acts that write a bound refuse above it, so the hub-side governor and the
 * machine-side ceiling are one number rather than two that drift (#281); where there is no
 * number, there is no bound, and no policy is refused against one nobody wrote.
 */
const CONCURRENT_JOBS = jobCeiling(manifest);
const reviewReadings = new ReviewReadings(store, () => store.now());
const coordinated = coordinator(
  store,
  () => store.now(),
  CONCURRENT_JOBS,
  async () => await reviewReadings.snapshot(dispatched.getStore()?.readingMetadata),
);

const drainAdmission: DrainAdmission = async (machineId, operationId) => {
  // Exploration's native preparation is an applicable operation even though Code posts its
  // session. An unrelated operation's ceiling is not a limit on this drain.
  const operations =
    operationId === OPERATIONS.explore ? [operationId, MACHINE_OPERATIONS.prepare] : [operationId];
  let ceiling: number | null = null;
  for (const id of operations) {
    const declared = manifest.machine?.operations[id]?.limits.concurrentJobs;
    if (declared !== undefined) ceiling = Math.min(ceiling ?? declared, declared);
  }
  const current = dispatched.getStore();
  return await liveDrainCapacity(current?.machines, machineId, ceiling, current?.jobs);
};

/**
 * WHAT A RUN OF AN OPERATION RUNS UNDER: its declared limits, and whether the owner may meter
 * it. Neither a cookbook nor a session is here any more (#279) — the method a run performs and
 * the model it performs it with belong to the Code profile the operator picks, and Code's own
 * `runSession` door is what posts the job.
 */
function planFor(policy: Policy, operationId: OperationName): RunPlan {
  return runPlan({ manifest, policy, operationId });
}

function loop(
  jobs: BabelJobs,
  machines: MachinesSlice,
  actions: ActionsSlice | undefined,
  plan: RunPlan,
  catalogPlan: RunPlan,
  mapPreparePlan: RunPlan,
  nativeDispatch: boolean,
  // The account chain this wake acts for (#470): what the loop posts carries it.
  chain: string | null,
  mappingDrainId?: string,
): Conductor {
  const engine = codeEngine(actions, dispatched.getStore()?.readingMetadata?.host);
  return conductor({
    store,
    coordinator: coordinated,
    jobs,
    drainAdmission,
    machines,
    // A run that reaches a model is CODE's job, and `onJobSettled` is delivered only to the
    // plugin that started one: the loop learns what became of a session by asking Code, over
    // the authority of whatever wake it is running on (#279).
    engine,
    dispatchAnalysis: async (assignment, claim, cycleRunId, identity) => {
      const policy = (await coordinated.policy()).policy;
      const route = policy.review;
      if (!policy.enabled || route === undefined)
        return { refused: "analysis policy is no longer enabled" };
      const recipe = route.stageRecipes[assignment.activity];
      if (recipe === undefined) return { refused: "the analysis stage no longer has a recipe" };
      return await machinery.startExplore(
        identity,
        jobs,
        engine,
        {
          preset: "read-whats-new",
          machineId: route.machineId,
          profile: route.profile,
          recipes: [recipe],
        },
        // The conductor's lanes share the routed machine's scratch at the per-machine bound, so
        // each material is bounded to its share of it (#453).
        { ...planFor(policy, MACHINE_OPERATIONS.prepare), materials: perMachineBound(policy) },
        {
          stage: assignment.activity,
          selectors: [...assignment.selectors],
          brief: [...assignment.brief],
          claim: { id: claim.id, runId: cycleRunId, fence: claim.fence },
        },
      );
    },
    keys,
    plan,
    catalogPlan,
    mapPreparePlan,
    nativeDispatch,
    chain,
    ...(mappingDrainId === undefined ? {} : { mappingDrainId }),
    now: () => store.now(),
  });
}

/**
 * THE COOKBOOK THIS HUB HOLDS, read off the policy's review route.
 *
 * It is the SAME block Watch's Recipes section lists, so the methods an explore performs and
 * the methods the panel names are one list and never two. A recipe needs a BODY to be a method:
 * an entry carrying only a label is not an instruction. Policies written before routed review
 * keep their former top-level cookbook as read-only compatibility; every newly installed policy
 * has to carry the recipes in its review route.
 */
async function cookbook(): Promise<Readonly<Record<string, Recipe>>> {
  const rows = await store.db.query<{ payload: string }>(
    `SELECT payload FROM policies ORDER BY seq DESC LIMIT 1`,
  );
  const payload = rows[0]?.payload;
  if (payload === undefined) return {};
  let held: unknown;
  let mapping: Record<string, unknown> | undefined;
  try {
    const parsed = JSON.parse(payload) as Record<string, unknown>;
    const review = parsed["review"];
    const routed =
      typeof review === "object" && review !== null && !Array.isArray(review)
        ? (review as Record<string, unknown>)["recipes"]
        : undefined;
    held = Array.isArray(routed) ? routed : parsed["recipes"];
    const configuredMapping = parsed["mapping"];
    if (
      typeof configuredMapping === "object" &&
      configuredMapping !== null &&
      !Array.isArray(configuredMapping)
    )
      mapping = configuredMapping as Record<string, unknown>;
  } catch {
    return {};
  }
  if (!Array.isArray(held)) return {};
  const cookbook: Record<string, Recipe> = {};
  for (const entry of held) {
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) continue;
    const recipe = entry as Record<string, unknown>;
    const id = recipe["id"];
    const body = recipe["body"];
    const title = recipe["title"];
    const version = recipe["version"];
    if (typeof id !== "string" || id === "") continue;
    if (typeof body !== "string" || body.trim() === "") continue;
    if (recipe["enabled"] === false) continue;
    // Mapping methods never become exploration methods, including the implicit all-recipes case.
    if (id === mapping?.["generateRecipe"] || id === mapping?.["reviewRecipe"]) continue;
    cookbook[id] = {
      id,
      version: typeof version === "number" && Number.isFinite(version) ? version : 0,
      ...(typeof title === "string" && title !== "" ? { title } : {}),
      body,
    };
  }
  return cookbook;
}

/**
 * WHAT A START AND A STOP REACH THE WORLD THROUGH, declared once. The doors and the drain's
 * controller take the SAME object: two of them would be two answers to what a run is. The
 * engine is Code (#279), reached with `ctx.actions.call` on the declared dependency, and it is
 * built per caller because the principal Code grades is the one whose request is in flight.
 */
const LAUNCH_DEPS: LaunchDeps = {
  coordinator: coordinated,
  drainAdmission,
  jobs: (ctx) => jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
  engine: (actions) => codeEngine(actions),
  cookbook,
  plan: planFor,
  now: () => store.now(),
};

/** The launch path every start goes through, doors and drain controller alike (#258). */
const machinery = launchMachinery(store, LAUNCH_DEPS);

/**
 * One native cadence per admitted machine. Keep its immutable template until configuration or
 * pins change or renewal is due; replacement is the SDK's, never a plugin timer or a scan.
 */
async function catalogSchedule(
  jobs: BabelJobs,
  machineId: string,
  policy: Policy,
  admission: TranscriptMapCatalogAdmission | null,
): Promise<string[]> {
  const notes: string[] = [];
  const machineKey = createHash("sha256").update(machineId).digest("hex").slice(0, 32);
  const scheduleId = `${BABEL_PLUGIN_ID}.map-catalog.${machineKey}`;
  try {
    const registered = (await jobs.schedules()).filter(
      (row) =>
        row.scheduleId === scheduleId &&
        row.machineId === machineId &&
        row.operationId === MACHINE_OPERATIONS.mapCatalog,
    );
    if (!policy.enabled || policy.mapping?.executorMachineId !== machineId || admission === null) {
      for (const row of registered)
        await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
      return notes;
    }
    const intervalMs = policy.cadenceSeconds * 1000;
    const described = await describeMapHost(jobs, policy.mapping, MACHINE_OPERATIONS.mapCatalog);
    if ("refused" in described) return [`catalog cadence: ${described.refused}`];
    if (
      JSON.stringify(admission.route) !==
        JSON.stringify(TranscriptMapConfigSchema.parse(policy.mapping)) ||
      admission.resourceBindingDigest !== described.resourceBindingDigest ||
      JSON.stringify(admission.serviceBinding) !== JSON.stringify(described.serviceBinding)
    ) {
      for (const row of registered)
        await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
      return ["catalog admission changed; startMapCatalog is required again"];
    }
    const installation = described.readiness.installation;
    const limits = planFor(policy, MACHINE_OPERATIONS.mapCatalog).limits;
    const configuration = createHash("sha256")
      .update(
        JSON.stringify({
          machineId,
          sourceMachineId: policy.mapping.sourceMachineId,
          route: policy.mapping,
          serviceBinding: described.serviceBinding,
          resourceBindingDigest: described.resourceBindingDigest,
          intervalMs,
          limits,
          installationRevision: installation?.revision,
          artifactSha256: installation?.artifactSha256,
        }),
      )
      .digest("hex");
    const at = store.now();
    if (
      registered.some(
        (row) => row.revision.startsWith(`${configuration}.`) && row.expiresAt - at > intervalMs,
      )
    )
      return notes;
    const revision = `${configuration}.${String(at)}`;
    await jobs.schedule({
      jobId: `catalog_${createHash("sha256").update(`${scheduleId}.${revision}`).digest("hex")}`,
      machineId,
      operationId: MACHINE_OPERATIONS.mapCatalog,
      input: {
        [INPUT_FIELD]: JSON.stringify({
          kind: "catalog-wake",
          sourceMachineId: policy.mapping.sourceMachineId,
          executorMachineId: machineId,
        }),
      },
      outputs: [],
      limits,
      resourceBindingDigest: described.resourceBindingDigest,
      expectedServiceBindings: { [RECALL_SERVICE_ID]: admission.serviceBinding },
      ...(installation === null
        ? {}
        : {
            installationRevision: installation.revision,
            artifactSha256: installation.artifactSha256,
          }),
      scheduleId,
      revision,
      firstNominalAt: at + intervalMs,
      intervalMs,
      deadlineMs: intervalMs,
      expiresAt: at + SCHEDULE_LIFETIME_MS,
      offlinePolicy: "coalesce-one",
    });
  } catch (error) {
    notes.push(`catalog cadence: ${message(error)}`);
  }
  return notes;
}

/** Only the explicitly admitted machine may continue this free lane, including after settlement. */
async function catalogCycle(
  jobs: BabelJobs,
  machineId: string,
  explicitAdmission?: TranscriptMapCatalogAdmission,
): Promise<readonly string[]> {
  const { policy, standing } = await coordinated.policy();
  const parsed = TranscriptMapCatalogAdmissionSchema.safeParse(
    explicitAdmission ??
      JSON.parse((await keys.get(TRANSCRIPT_MAP_CATALOG_ADMISSION_KEY)) ?? "null"),
  );
  const admission =
    parsed.success &&
    policy.enabled &&
    policy.mapping !== undefined &&
    parsed.data.route.executorMachineId === machineId &&
    JSON.stringify(parsed.data.route) ===
      JSON.stringify(TranscriptMapConfigSchema.parse(policy.mapping))
      ? parsed.data
      : null;
  if (explicitAdmission !== undefined && admission !== null)
    await keys.set(TRANSCRIPT_MAP_CATALOG_ADMISSION_KEY, JSON.stringify(admission));
  const notes = await catalogSchedule(jobs, machineId, standing, admission);
  return [
    ...notes,
    ...(await loop(
      jobs,
      unaskable(HOOK_WITHOUT_MACHINES),
      undefined,
      planFor(policy, BEAT_OPERATION),
      planFor(policy, MACHINE_OPERATIONS.mapCatalog),
      planFor(policy, MACHINE_OPERATIONS.mapPrepare),
      // The free catalog lane never enters paid dispatch, whatever authority settled it, and
      // posts nothing a chain would be asked about.
      false,
      null,
    ).tickCatalog(machineId, admission ?? undefined)),
  ];
}

/**
 * WHY A MAPPING DRAIN CANNOT START, or null. The free catalog's cadence is the native wake that
 * refills a mapping drain's fan once its Code sessions settle — a Code session settling wakes
 * Code, never Babel — so a drain is refused while that cadence is not admitted for the route.
 */
async function catalogAdmitted(policy: Policy): Promise<string | null> {
  if (!policy.enabled || policy.mapping === undefined)
    return "the policy in force installs no transcript-mapping route";
  const parsed = TranscriptMapCatalogAdmissionSchema.safeParse(
    JSON.parse((await keys.get(TRANSCRIPT_MAP_CATALOG_ADMISSION_KEY)) ?? "null"),
  );
  return parsed.success &&
    JSON.stringify(parsed.data.route) ===
      JSON.stringify(TranscriptMapConfigSchema.parse(policy.mapping))
    ? null
    : `start the free catalog for this route (${ACTIONS.startMapCatalog}) first: its cadence is what refills a mapping drain`;
}

/** The scheduler id of one paid mapping drain's own cadence. */
function mapDrainWakeId(drainId: string): string {
  return `${BABEL_PLUGIN_ID}.map-drain.${createHash("sha256").update(drainId).digest("hex").slice(0, 32)}`;
}

/** The lifetimes a drain's cadence is registered for, longest first (see `mapDrainWake`). */
const CADENCE_LIFETIMES_MS = [
  SCHEDULE_LIFETIME_MS,
  7 * 24 * 60 * 60 * 1000,
  24 * 60 * 60 * 1000,
  6 * 60 * 60 * 1000,
  60 * 60 * 1000,
] as const;

/** The drain uses the declared beat operation, not a new machine operation or a policy beat. */
const DRAIN_SCHEDULE_PREFIX = `${BEAT_OPERATION}.drain.`;

/** A drain's cadence cannot be mistaken for the policy's single conductor schedule. */
function ordinaryDrainWakeId(drainId: string): string {
  return `${DRAIN_SCHEDULE_PREFIX}${createHash("sha256").update(drainId).digest("hex").slice(0, 32)}`;
}

/**
 * Code sessions settle in Code, not Babel. A drain with no weighted standing activities must
 * therefore carry its own native wake, under the credential that started its first fan. Register
 * before spending that fan, and keep the cadence past its deadline until every held job settles.
 */
async function ordinaryDrainWake(
  jobs: BabelJobs,
  row: DrainRow,
  policy: Policy,
): Promise<{ readonly ok: boolean; readonly notes: readonly string[] }> {
  const refused = (why: string) => ({
    ok: false,
    notes: [`drain ${row.id} cadence: ${why}`],
  });
  if (!policy.enabled) return refused("the policy is disabled");
  const scheduleId = ordinaryDrainWakeId(row.id);
  try {
    const described = await describeHost(jobs, row.machineId, BEAT_OPERATION);
    if ("refused" in described) return refused(described.refused);
    const installation = described.readiness.installation;
    const intervalMs = Math.max(1, policy.cadenceSeconds) * 1000;
    const limits = planFor(policy, BEAT_OPERATION).limits;
    const configuration = createHash("sha256")
      .update(
        JSON.stringify({
          drainId: row.id,
          machineId: row.machineId,
          intervalMs,
          limits,
          installationRevision: installation?.revision,
          artifactSha256: installation?.artifactSha256,
        }),
      )
      .digest("hex");
    const at = store.now();
    const registered = (await jobs.schedules()).filter((entry) => entry.scheduleId === scheduleId);
    if (
      registered.some(
        (entry) =>
          entry.revision.startsWith(`${configuration}.`) && entry.expiresAt - at > intervalMs,
      )
    )
      return { ok: true, notes: [] };
    const revision = `${configuration}.${String(at)}`;
    const deadline = deadlineOf(row.target);
    const floor = Math.max(at + intervalMs, deadline === null ? 0 : deadline + 2 * intervalMs);
    for (const lifetime of CADENCE_LIFETIMES_MS) {
      const expiresAt = at + lifetime;
      if (expiresAt <= floor) break;
      try {
        await jobs.schedule({
          jobId: `drainwake_${createHash("sha256").update(`${scheduleId}.${revision}`).digest("hex")}`,
          machineId: row.machineId,
          operationId: BEAT_OPERATION,
          input: { [INPUT_FIELD]: JSON.stringify({ machineId: row.machineId }) },
          outputs: [
            { name: OUTPUT_BINDING, locationId: OUTPUT_LOCATION, components: [BEAT_OPERATION] },
          ],
          limits,
          ...(installation === null
            ? {}
            : {
                installationRevision: installation.revision,
                artifactSha256: installation.artifactSha256,
              }),
          scheduleId,
          revision,
          firstNominalAt: at + intervalMs,
          intervalMs,
          deadlineMs: intervalMs,
          expiresAt,
          offlinePolicy: "coalesce-one",
        });
        return { ok: true, notes: [] };
      } catch (error) {
        if (!message(error).includes("schedule-expiry-ceiling")) throw error;
      }
    }
    return refused("the credential ends before the drain's deadline can be settled");
  } catch (error) {
    return refused(message(error));
  }
}

/** Stop ended drains' cadences; renew only the drain whose own credential woke this cycle. */
async function ordinaryDrainWakes(jobs: BabelJobs, ownId?: string): Promise<string[]> {
  const notes: string[] = [];
  try {
    const live = new Map(
      (await activeDrains(store))
        .filter((row) => row.preset !== MAP_DRAIN_PRESET)
        .map((row) => [ordinaryDrainWakeId(row.id), row] as const),
    );
    for (const entry of await jobs.schedules()) {
      if (!entry.scheduleId.startsWith(DRAIN_SCHEDULE_PREFIX) || live.has(entry.scheduleId))
        continue;
      await jobs.disableSchedule({ scheduleId: entry.scheduleId, revision: entry.revision });
    }
    const own = ownId === undefined ? undefined : live.get(ordinaryDrainWakeId(ownId));
    if (own !== undefined)
      notes.push(
        ...(await ordinaryDrainWake(jobs, own, (await coordinated.policy()).policy)).notes,
      );
  } catch (error) {
    notes.push(`ordinary drain cadence: ${message(error)}`);
  }
  return notes;
}

/**
 * A PAID MAPPING DRAIN'S OWN WAKE (#469). A Code session settling wakes Code, not Babel, so a
 * drain needs a native cadence to settle its sessions and refill its fan — and that cadence is
 * posted under the drain's own credential, at the `map-prepare` node the press was admitted at,
 * so every settlement it wakes carries exactly what the press discharged: the Code workspace
 * and the broker read a session needs. The free catalog's cadence never does this; it carries
 * no paid authority and must not borrow any.
 *
 * `jobs` must be a slice holding the drain's authority (the press, or a wake of the drain's own
 * jobs). A registration already current is left alone; a new revision replaces it before its
 * lifetime runs out.
 *
 * THE CADENCE OUTLIVES THE DEADLINE. The deadline ends admission, not settlement: a session
 * still at the model when it passes must be read, settled and folded, and nothing else wakes a
 * drain whose standing weights are zero. So the cadence runs until the drain has ended — every
 * wake disables the cadence of an ended drain — while every spend it could make is refused past
 * the drain's own bounds (`mappingDrainId` in `server/conductor.ts`). The hub refuses an expiry
 * past the registering credential's own, and a plugin cannot read that ceiling, so the
 * registration steps down {@link CADENCE_LIFETIMES_MS} and keeps the longest it accepts — but
 * never one that ends before the deadline has been passed by two intervals: a cadence that
 * cannot settle what the drain admitted is not a cadence, and `ok` says so.
 */
async function mapDrainWake(
  jobs: BabelJobs,
  drainId: string,
  policy: Policy,
  deadline: number | null,
): Promise<{ readonly ok: boolean; readonly notes: readonly string[] }> {
  const route = policy.mapping;
  if (!policy.enabled || route === undefined) return { ok: false, notes: [] };
  const scheduleId = mapDrainWakeId(drainId);
  const refused = (why: string) => ({
    ok: false,
    notes: [`mapping drain ${drainId} cadence: ${why}`],
  });
  try {
    const described = await describeMapHost(jobs, route, MACHINE_OPERATIONS.mapPrepare);
    if ("refused" in described) return refused(described.refused);
    const installation = described.readiness.installation;
    const intervalMs = policy.cadenceSeconds * 1000;
    const limits = planFor(policy, MACHINE_OPERATIONS.mapPrepare).limits;
    const configuration = createHash("sha256")
      .update(
        JSON.stringify({
          drainId,
          route,
          serviceBinding: described.serviceBinding,
          resourceBindingDigest: described.resourceBindingDigest,
          intervalMs,
          limits,
          installationRevision: installation?.revision,
          artifactSha256: installation?.artifactSha256,
        }),
      )
      .digest("hex");
    const at = store.now();
    const registered = (await jobs.schedules()).filter((row) => row.scheduleId === scheduleId);
    if (
      registered.some(
        (row) => row.revision.startsWith(`${configuration}.`) && row.expiresAt - at > intervalMs,
      )
    )
      return { ok: true, notes: [] };
    const revision = `${configuration}.${String(at)}`;
    const floor = Math.max(at + intervalMs, deadline === null ? 0 : deadline + 2 * intervalMs);
    for (const lifetime of CADENCE_LIFETIMES_MS) {
      const expiresAt = at + lifetime;
      if (expiresAt <= floor) break;
      try {
        await jobs.schedule({
          jobId: `mapwake_${createHash("sha256").update(`${scheduleId}.${revision}`).digest("hex")}`,
          machineId: route.executorMachineId,
          operationId: MACHINE_OPERATIONS.mapPrepare,
          input: {
            [INPUT_FIELD]: JSON.stringify({
              kind: "drain-wake",
              drainId,
              executorMachineId: route.executorMachineId,
            }),
          },
          outputs: [],
          limits,
          resourceBindingDigest: described.resourceBindingDigest,
          expectedServiceBindings: { [RECALL_SERVICE_ID]: described.serviceBinding },
          ...(installation === null
            ? {}
            : {
                installationRevision: installation.revision,
                artifactSha256: installation.artifactSha256,
              }),
          scheduleId,
          revision,
          firstNominalAt: at + intervalMs,
          intervalMs,
          deadlineMs: intervalMs,
          expiresAt,
          offlinePolicy: "coalesce-one",
        });
        return { ok: true, notes: [] };
      } catch (error) {
        if (!message(error).includes("schedule-expiry-ceiling")) throw error;
      }
    }
    return refused(
      "the credential it runs under ends before the drain's deadline could be settled past",
    );
  } catch (error) {
    return refused(message(error));
  }
}

/**
 * Every paid mapping drain's cadence, reconciled on a wake: a cadence whose drain has ended is
 * disabled on any wake, because stopping one spends nothing; the waking drain's own is renewed,
 * and only that one — a registration carries the credential that made it, and another drain's
 * cadence re-registered under this one would wake that drain under the wrong principal.
 */
async function mapDrainWakes(
  jobs: BabelJobs,
  policy: Policy,
  drainId: string | undefined,
): Promise<string[]> {
  const notes: string[] = [];
  try {
    const live = new Map(
      (await activeDrains(store))
        .filter((drain) => drain.preset === MAP_DRAIN_PRESET)
        .map((drain) => [mapDrainWakeId(drain.id), drain] as const),
    );
    for (const row of await jobs.schedules()) {
      if (!row.scheduleId.startsWith(`${BABEL_PLUGIN_ID}.map-drain.`) || live.has(row.scheduleId))
        continue;
      await jobs.disableSchedule({ scheduleId: row.scheduleId, revision: row.revision });
    }
    const own = drainId === undefined ? undefined : live.get(mapDrainWakeId(drainId));
    if (own !== undefined)
      notes.push(...(await mapDrainWake(jobs, own.id, policy, deadlineOf(own.target))).notes);
  } catch (error) {
    notes.push(`mapping drain cadence: ${message(error)}`);
  }
  return notes;
}

/**
 * THE PAID MAPPING DRAIN A SETTLED `map-prepare` JOB BELONGS TO (#470): its cadence names it by
 * schedule, and a preparation by the run that drain holds. That settlement carries the drain's
 * own credential, and every paid spend on it is for that drain's runs alone.
 */
async function settledMapDrain(job: {
  readonly jobId: string;
  readonly scheduleId?: string | undefined;
}): Promise<string | undefined> {
  const drains = (await activeDrains(store)).filter((drain) => drain.preset === MAP_DRAIN_PRESET);
  if (job.scheduleId !== undefined)
    return drains.find((drain) => mapDrainWakeId(drain.id) === job.scheduleId)?.id;
  const runs = await store.db.query<{ id: string }>(`SELECT id FROM runs WHERE prepare_job_id=?`, [
    job.jobId,
  ]);
  return drains.find((drain) =>
    drain.live.some((held) => runs.some((run) => run.id === held.runId)),
  )?.id;
}

/**
 * THE ACCOUNT CHAIN A SETTLED JOB'S HOOK ACTS FOR (#470), or null for none. The hook is handed
 * the settled job's own credential, which is the credential of the wake that posted the job, so
 * its chain is the one recorded when that job was posted: the beat's at its registration, for
 * the revision the occurrence ran under; any other job's on the run rows that hold it or its
 * preparation. A job nothing recorded a chain for — posted before chains were recorded, or by
 * a schedule of another lane — has none, and neither has one whose rows disagree: such a wake
 * may post, and never asks again under a key it cannot prove it posted.
 */
async function settledChain(job: {
  readonly jobId: string;
  readonly scheduleId?: string | undefined;
  readonly revision?: string | undefined;
}): Promise<string | null> {
  if (job.scheduleId !== undefined) {
    if (job.scheduleId !== CONDUCTOR_SCHEDULE_ID) return null;
    let kept: unknown;
    try {
      kept = JSON.parse((await keys.get(beatChainKey(job.revision ?? ""))) ?? "null");
    } catch {
      return null;
    }
    const beat = BeatChainSchema.safeParse(kept);
    return beat.success && beat.data.revision === job.revision ? beat.data.chain : null;
  }
  const rows = await store.db.query<{ chain: string | null }>(
    `SELECT chain FROM runs WHERE job_id = ? OR prepare_job_id = ?`,
    [job.jobId, job.jobId],
  );
  const chains = new Set(rows.map((row) => row.chain));
  const [only] = chains;
  return chains.size === 1 && only !== undefined ? only : null;
}

/**
 * The controller's dependencies over one wake's own authority (#258, #279).
 *
 * `embed` is `null` on a wake that holds no service authority, which is every background one
 * (#337): `GuestServices` is a dispatch's, and the settled-job hook is served jobs and actions
 * and nothing else. So the corpus backfill rides a tick the operator's own dispatch woke, and a
 * settlement's tick launches and embeds nothing.
 */
function draining(
  jobs: BabelJobs,
  actions: ActionsSlice | undefined,
  chain: string | null,
  services?: EmbeddingServices | undefined,
): DrainDeps {
  return {
    store,
    coordinator: coordinated,
    launch: machinery,
    admission: drainAdmission,
    jobs,
    engine: codeEngine(actions),
    chain,
    plan: planFor,
    embed: services === undefined ? null : embedder(services),
    now: () => store.now(),
  };
}

/**
 * ONE CYCLE, over the authority the caller brought: a job slice to reach machines through, and
 * the one machine question the loop asks outside a job (what a catalogued folder is, #535).
 *
 * The loop has no clock of its own — a plugin may not poll as an alternate scheduler — so a
 * cycle happens when something has already woken this half: a door the operator knocked on, or
 * one of this plugin's own jobs settling. Every step of it is idempotent, which is what makes
 * that safe: two cycles in the same second do the work of one.
 *
 * THE DRAIN MOVES ON AFTER THE CONDUCTOR, and the order is the point (#258): the conductor is
 * what settles a finished job and writes what it metered, so a controller that read the runs
 * table first would decide whether to launch another against last cycle's numbers. A settlement
 * is also the wake that matters to a drain, because a settlement is exactly when a slot opens.
 *
 * A mapping drain's own wake is not this: see {@link mapDrainCycle}.
 */
async function cycle(
  jobs: BabelJobs,
  machines: MachinesSlice,
  actions: ActionsSlice | undefined,
  // The account chain this wake acts for (#470; `principalChain` in `doors/launch.ts`): what the
  // cycle posts is recorded under it, and a lost posting is asked again only under its own.
  chain: string | null,
  services?: EmbeddingServices | undefined,
  // Only a hook's slice — the settled job's own authority, or the installer's at enable — can
  // post native work; a door's bridge is attenuated to that door's delegates.
  nativeDispatch = false,
): Promise<void> {
  const policy = (await coordinated.policy()).policy;
  // The beat is the only job this loop still posts itself, so its operation is what the plan's
  // limits are read for; a run that reaches a model is Code's to post (#279). Mapping's native
  // work uses each of its operations' own declared limits.
  const plan = planFor(policy, BEAT_OPERATION);
  const report = await loop(
    jobs,
    machines,
    actions,
    plan,
    planFor(policy, MACHINE_OPERATIONS.mapCatalog),
    planFor(policy, MACHINE_OPERATIONS.mapPrepare),
    nativeDispatch,
    chain,
  ).tick();
  /*
    WHY THIS CYCLE DID WHAT IT DID. The loop's own verdict was visible nowhere: a cycle that
    drew nothing, or stopped on a gap, or refused a dispatch, left no trace outside the tick
    report it returned to this caller, and no door reports it. An operator watching a preview
    where nothing happens could not tell "no candidate is eligible" from "the route names no
    profile" from "the batch is full" without reading the store.
  */
  if (report.stop !== null)
    console.warn(
      `${BABEL_PLUGIN_ID}: cycle ${report.cycleRunId} stopped: ${report.stop.reason}: ${report.stop.detail}`,
    );
  if (report.parked !== null)
    console.warn(
      `${BABEL_PLUGIN_ID}: cycle ${report.cycleRunId} parked on ${report.parked.reason} ` +
        `(${String(report.parked.barren)} barren, ${String(report.parked.spent)} paid for and ` +
        `refused): ${report.parked.detail}`,
    );
  for (const gap of report.gaps.slice(0, 8))
    console.warn(
      `${BABEL_PLUGIN_ID}: cycle ${report.cycleRunId} gap ${gap.recordId}/${gap.role || "-"}: ${gap.reason}: ${gap.detail}`,
    );
  for (const refusal of report.refused)
    console.warn(
      `${BABEL_PLUGIN_ID}: cycle ${report.cycleRunId} refused ${refusal.recordId}: ${refusal.reason}: ${refusal.detail}`,
    );
  for (const note of report.notes)
    console.warn(`${BABEL_PLUGIN_ID}: cycle ${report.cycleRunId}: ${note}`);
  /*
    …AND THEN THE SESSIONS WHOSE MATERIAL IS NOW SEALED (#592). A job-inputs binding names a
    SETTLED job's output, so a session cannot be posted while its own `prepare` is still
    running: the press leaves the preparation in flight and the run recorded as intent, and
    this is the wake that turns it into a Code session. It runs AFTER the conductor, because
    the conductor is what settled that preparation and wrote the index this reads.
  */
  const engine = codeEngine(actions);
  for (const posted of await machinery.postPrepared(jobs, engine, plan, chain)) {
    if ("refused" in posted) {
      console.warn(`${BABEL_PLUGIN_ID}: run ${posted.runId}: ${posted.refused}`);
    } else if ("waiting" in posted) {
      console.warn(`${BABEL_PLUGIN_ID}: run ${posted.runId}: ${posted.waiting}`);
    }
  }
  /*
    …AND ONE TITLING RUN, IF THIS CYCLE MAY AFFORD ONE (#342). A session whose own log records
    no title never gets one from its capture, so the only way it gets one at all is a model — which
    in Babel is a Code session like every other model call. It is posted here, after the
    conductor has drawn and dispatched, so the ceilings it is admitted against already include
    everything this cycle committed to reviewing.
  */
  const named = await machinery.inferTitles(jobs, engine, report.cycleRunId, chain);
  if (named !== null && "refused" in named) {
    console.warn(`${BABEL_PLUGIN_ID}: no session was named this cycle: ${named.refused}`);
  }
  for (const report of await drainTick(draining(jobs, actions, chain, services))) {
    for (const note of report.notes) {
      console.warn(`${BABEL_PLUGIN_ID}: drain ${report.drainId}: ${note}`);
    }
  }
  // After the controller, so a drain it just ended loses its cadence on this same wake.
  for (const note of await mapDrainWakes(jobs, policy, undefined))
    console.warn(`${BABEL_PLUGIN_ID}: ${note}`);
  for (const note of await ordinaryDrainWakes(jobs)) console.warn(`${BABEL_PLUGIN_ID}: ${note}`);
}

/**
 * One ordinary drain's own wake. Observe completed jobs first, then post only the prepared runs
 * held by this drain and refill only its fan. A drain-start credential is not a conductor policy
 * installation; using it to draw unrelated standing work would transfer authority.
 */
async function ordinaryDrainCycle(
  jobs: BabelJobs,
  actions: ActionsSlice | undefined,
  row: DrainRow,
): Promise<void> {
  const policy = (await coordinated.policy()).policy;
  const chain = principalChain(row.startedBy);
  for (const note of await loop(
    jobs,
    unaskable(HOOK_WITHOUT_MACHINES),
    actions,
    planFor(policy, BEAT_OPERATION),
    planFor(policy, MACHINE_OPERATIONS.mapCatalog),
    planFor(policy, MACHINE_OPERATIONS.mapPrepare),
    false,
    chain,
  ).observe())
    console.warn(`${BABEL_PLUGIN_ID}: drain ${row.id}: ${note}`);
  const own = await readDrain(store, row.id);
  if (own !== null && (own.state === "running" || own.state === "closing")) {
    const runIds = new Set(own.live.map((held) => held.runId));
    for (const posted of await machinery.postPrepared(
      jobs,
      codeEngine(actions),
      planFor(policy, BEAT_OPERATION),
      chain,
      runIds,
    )) {
      if ("refused" in posted)
        console.warn(`${BABEL_PLUGIN_ID}: drain ${row.id} run ${posted.runId}: ${posted.refused}`);
      else if ("waiting" in posted)
        console.warn(`${BABEL_PLUGIN_ID}: drain ${row.id} run ${posted.runId}: ${posted.waiting}`);
    }
    for (const report of await drainTick(draining(jobs, actions, chain), undefined, true, row.id))
      for (const note of report.notes) console.warn(`${BABEL_PLUGIN_ID}: drain ${row.id}: ${note}`);
  }
  for (const note of await ordinaryDrainWakes(jobs, row.id))
    console.warn(`${BABEL_PLUGIN_ID}: ${note}`);
}
/** Read-only polls reconcile progress; the next native/write wake owns every new posting. */
async function observeCycle(
  jobs: BabelJobs,
  machines: MachinesSlice,
  actions: ActionsSlice | undefined,
  chain: string | null,
  services?: EmbeddingServices | undefined,
): Promise<void> {
  const policy = (await coordinated.policy()).policy;
  for (const note of await loop(
    jobs,
    machines,
    actions,
    planFor(policy, BEAT_OPERATION),
    planFor(policy, MACHINE_OPERATIONS.mapCatalog),
    planFor(policy, MACHINE_OPERATIONS.mapPrepare),
    false,
    chain,
  ).observe())
    console.warn(`${BABEL_PLUGIN_ID}: observation: ${note}`);
  for (const report of await drainTick(draining(jobs, actions, chain, services), undefined, false))
    for (const note of report.notes)
      console.warn(`${BABEL_PLUGIN_ID}: drain ${report.drainId}: ${note}`);
}

/**
 * A `map-prepare` JOB'S OWN WAKE, and nothing else: what a settled `map-prepare` job — one of a
 * drain's preparations, its cadence, or a preparation of the standing mapping lane — is
 * delivered for (#469, #470).
 *
 * THE HOOK'S TABLES ARE GONE AT TWO SECONDS. The host closes a settled-job hook's data lease at
 * its lifecycle bound whether the hook has returned or not, and the hook runs on without them
 * (Manifold's `jobSettled` in `packages/server/src/plugin-host.ts`, `runHook` in
 * `packages/plugin/src/lifecycle.ts`). The whole cycle behind this wake polled every open run
 * and drew review work before it reached the one act only this wake may perform — posting a
 * prepared session, which spends the lane's own credential — so on a preview every
 * preparation finished and not one session was posted. This wake is therefore the mapping lane
 * alone, in the order posting needs: its runs settled and its prepared sessions posted, then
 * its lane's dead claims reaped and its free slots refilled (`Conductor.tickMapDrains`), then
 * the mapping drains' controller, then the cadences. The reap is here because with every
 * activity weight at zero no beat runs, and on a hub nobody is watching this wake is the only
 * cycle there is. The rest of the loop is a full cycle's: a door the operator knocks on, the
 * beat's own settlement.
 *
 * A drain's wake is scoped by its drain, which is a chain in all but name, so it carries no
 * chain of its own. A standing preparation's wake carries the chain its job was posted under
 * (`settledChain`), exactly as an analysis preparation's does: the standing lane posts under
 * the authority the review and analysis lanes post with. A job of neither — an ended drain's
 * late preparation or cadence — spends for no lane: its wake only reconciles.
 */
async function mapDrainCycle(
  jobs: BabelJobs,
  actions: ActionsSlice | undefined,
  job: {
    readonly jobId: string;
    readonly scheduleId?: string | undefined;
    readonly revision?: string | undefined;
  },
): Promise<void> {
  const policy = (await coordinated.policy()).policy;
  const mappingDrainId = await settledMapDrain(job);
  const standing =
    mappingDrainId === undefined &&
    job.scheduleId === undefined &&
    (
      await store.db.query(`SELECT 1 FROM runs WHERE prepare_job_id=? AND ${STANDING_RUN}`, [
        job.jobId,
      ])
    ).length > 0;
  const moved = await loop(
    jobs,
    unaskable(HOOK_WITHOUT_MACHINES),
    actions,
    planFor(policy, BEAT_OPERATION),
    planFor(policy, MACHINE_OPERATIONS.mapCatalog),
    planFor(policy, MACHINE_OPERATIONS.mapPrepare),
    mappingDrainId !== undefined || standing,
    standing ? await settledChain(job) : null,
    mappingDrainId,
  ).tickMapDrains();
  for (const note of moved.notes) console.warn(`${BABEL_PLUGIN_ID}: mapping: ${note}`);
  for (const report of await drainTick(draining(jobs, actions, null), MAP_DRAIN_PRESET)) {
    for (const note of report.notes) {
      console.warn(`${BABEL_PLUGIN_ID}: drain ${report.drainId}: ${note}`);
    }
  }
  // After the controller, so a drain it just ended loses its cadence on this same wake.
  for (const note of await mapDrainWakes(jobs, policy, mappingDrainId))
    console.warn(`${BABEL_PLUGIN_ID}: ${note}`);
}

/**
 * Read-only polls fold live jobs and settled receipts without scheduling or posting anything.
 * A hook has no `follow`, so the panel's five-second drain poll still needs its own observation
 * at most once per thirty seconds to measure a running job's rate. A write-authorized launch
 * or policy installation, and the installed beat's own settled-job hook, run the full cycle:
 * only those wakes can preserve the Code-workspace write cap a prepared session needs.
 *
 * Every listed door declares `jobs:read` because its own dispatch slice is attenuated to its
 * caps and delegates. Read-only parts may call pulse, runs and drainStatus without acquiring
 * container write authority; their observations cannot post new work.
 */
export const WAKES: Record<string, true> = {
  [ACTIONS.pulse]: true,
  [ACTIONS.runs]: true,
  [ACTIONS.drainStatus]: true,
  [ACTIONS.launch]: true,
  [ACTIONS.setPolicy]: true,
};
const READ_WAKES: Record<string, true> = {
  [ACTIONS.pulse]: true,
  [ACTIONS.runs]: true,
  [ACTIONS.drainStatus]: true,
};
const WAKE_FLOOR_MS = 30_000;
let observedAt = 0;

const doors = babelDoors(
  store,
  LAUNCH_DEPS,
  {
    coordinator: coordinated,
    deps: (ctx) =>
      draining(
        jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
        ctx.actions,
        principalChain(ctx.principal.id),
        ctx.services,
      ),
    startOrdinary: async (ctx, row) =>
      await ordinaryDrainWake(
        jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
        row,
        (await coordinated.policy()).policy,
      ),
    stopOrdinary: async (ctx) => {
      for (const note of await ordinaryDrainWakes(
        jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
      ))
        console.warn(`${BABEL_PLUGIN_ID}: ${note}`);
    },
    startMapping: async (ctx, drainId) => {
      const { policy } = await coordinated.policy();
      const refusal = await catalogAdmitted(policy);
      if (refusal !== null) return { refused: refusal };
      const jobs = jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive));
      // The drain's own wake comes FIRST, registered by this press so it carries this press's
      // authority. A drain whose sessions nothing could ever settle must not buy one: without a
      // cadence past its deadline the press launches nothing and the door ends it.
      const row = await readDrain(store, drainId);
      if (row === null) return { launched: 0, notes: [`the drain row for ${drainId} is gone`] };
      const cadence = await mapDrainWake(jobs, drainId, policy, deadlineOf(row.target));
      if (!cadence.ok) return { launched: 0, notes: [...cadence.notes] };
      const started = await loop(
        jobs,
        machinesSlice(ctx.machines),
        ctx.actions,
        planFor(policy, BEAT_OPERATION),
        planFor(policy, MACHINE_OPERATIONS.mapCatalog),
        planFor(policy, MACHINE_OPERATIONS.mapPrepare),
        true,
        null,
        drainId,
      ).tickMapDrains();
      // The cadence registered above may already have launched this drain's first fan on a wake
      // of its own, leaving this tick nothing to add: what the drain launched is its durable
      // cursor, not this call's count, so a spending drain is never ended as launching nothing.
      const launched = Math.max(
        started.launched,
        (await readDrain(store, drainId))?.jobsLaunched ?? 0,
      );
      return { launched, notes: [...started.notes] };
    },
    now: () => store.now(),
  },
  CONCURRENT_JOBS,
  // THE SERVICES THIS BUNDLE BINDS, read off the same manifest the ceiling is (#400). The
  // `services` block on `archive` and `verify` is the declaration; the composer behind the two
  // owner doors turns it into the policy, so the binding and the policy cannot be edited apart.
  declaredServices(manifest),
  async (ctx, admission) =>
    await catalogCycle(
      jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
      admission.route.executorMachineId,
      admission,
    ),
  reviewReadings,
);

/**
 * Every door, with the calling context's own database bound for the length of its handler, and
 * an observation or cycle behind the five that warrant one. A dispatch the host served no database to runs
 * anyway and fails at its first statement, by name: a plugin that declared `database` and got
 * none is a host bug, not a caller's refusal.
 *
 * The cycle runs AFTER the door has produced its answer and can never change it: the door
 * answered the caller's question, and a loop that stumbled — on a machine that has gone, on an
 * output it cannot read — is not that caller's problem. It is awaited rather than left running,
 * because the handles a dispatch holds are closed the moment it replies.
 */
const handlers: Record<string, ServerHandler> = {};
for (const [name, handler] of Object.entries(doors.handlers)) {
  const wakes = Object.hasOwn(WAKES, name);
  handlers[name] = async (ctx, args) => {
    const served = ctx.database;
    if (served === undefined) return await handler(ctx, args);
    return await dispatched.run(
      {
        database: served,
        storage: ctx.storage,
        machines: ctx.machines,
        jobs: ctx.jobs,
        readingMetadata: {
          host: ctx.host,
          services: { listInstances: (input) => ctx.services.listInstances(input) },
        },
      },
      async () => {
        const produced = await handler(ctx, args);
        const at = ctx.now();
        const refused =
          produced !== null && typeof produced === "object" && Object.hasOwn(produced, "refused");
        if (wakes && !refused) {
          const observing = Object.hasOwn(READ_WAKES, name);
          if (!observing || at - observedAt >= WAKE_FLOOR_MS) {
            if (observing) observedAt = at;
            try {
              const jobs = jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive));
              const machines = machinesSlice(ctx.machines);
              const chain = principalChain(ctx.principal.id);
              if (observing) await observeCycle(jobs, machines, ctx.actions, chain, ctx.services);
              else await cycle(jobs, machines, ctx.actions, chain, ctx.services);
            } catch (error) {
              console.warn(`${BABEL_PLUGIN_ID}: the cycle after ${name} failed: ${message(error)}`);
            }
          }
        }
        return produced;
      },
    );
  };
}

export const plugin: ServerPluginDef = {
  manifest,
  actions: doors.actions,
  handlers,
  lifecycle: {
    async onEnable(ctx) {
      const database = ctx.database;
      if (database === undefined) {
        throw new Error(`${BABEL_PLUGIN_ID}: its manifest declares a database and none was served`);
      }
      enabled = { database, storage: ctx.storage };
      const created = await database.query<{ n: number }>(
        "SELECT count(*) AS n FROM sqlite_master WHERE type = 'table' AND name = ?",
        [SENTINEL_TABLE],
      );
      // One batch, so a store either exists whole or was never begun; 65 statements against a
      // bound of 256, which is the reason the schema may stay one list.
      if (Number(created[0]?.n ?? 0) === 0) {
        await database.batch(SCHEMA_V1.map((sql) => ({ sql })));
      } else {
        // A store an earlier shape created reaches this one by what it is missing and nothing
        // else. SQLite has no `ADD COLUMN IF NOT EXISTS` and no `CREATE TABLE IF NOT EXISTS`
        // worth trusting here, so each addition is asked for by name first — a column of its
        // table, or the schema object itself when it names no column: this runs on every enable
        // and must do nothing on all but one of them. The object query carries no `type`
        // filter, because a table, an index and a trigger share one namespace in SQLite and an
        // append-only table whose triggers could not be added would append by convention. A
        // removal is asked the same question and is due on the other answer, in the same batch.
        const pending: SqlStatement[] = [];
        const createdObjects = new Set<string>();
        for (const addition of SCHEMA_ADDITIONS) {
          // Whole-table additions already create the current shape. Later column steps are
          // for pre-existing tables, not for the stale database view before this batch.
          if (addition.column !== undefined && createdObjects.has(addition.object)) continue;
          const held =
            addition.column === undefined
              ? await database.query<{ n: number }>(
                  "SELECT count(*) AS n FROM sqlite_master WHERE name = ?",
                  [addition.object],
                )
              : await database.query<{ n: number }>(
                  "SELECT count(*) AS n FROM pragma_table_info(?) WHERE name = ?",
                  [addition.object, addition.column],
                );
          const present = Number(held[0]?.n ?? 0) > 0;
          if (present === (addition.removes === true)) {
            pending.push({ sql: addition.sql });
            if (addition.column === undefined && !addition.removes) {
              createdObjects.add(addition.object);
            }
          }
        }
        if (pending.length > 0) await database.batch(pending);
      }
      await ctx.storage.set(SCHEMA_KEY, STORE_MIGRATION);
      /*
        THE KEYWORD INDEX IS BROUGHT CURRENT HERE (#337), because an enable is the one moment
        that is certain to happen once per shape and needs nobody's account. A store that reached
        this shape by addition holds every record and no term — the 6,038 of the crossing among
        them — and `record_terms_follow` only ever fires for a record written after it existed.
        The counts inside are the whole condition, so every later enable pays for two of them.

        It cannot fail an enable. A store whose keyword index could not be written is a store
        whose searches answer by meaning or not at all, which is a degraded deployment rather
        than a refused one; the drain's own duty tries again on every tick.
      */
      try {
        const seeded = await ensureTerms(store);
        if (seeded > 0) {
          console.warn(
            `${BABEL_PLUGIN_ID}: the keyword index was built over ${String(seeded)} records`,
          );
        }
      } catch (error) {
        console.warn(`${BABEL_PLUGIN_ID}: the keyword index could not be built: ${message(error)}`);
      }
      /*
        AN ENABLED POLICY REGISTERS ITS BEAT HERE (#534). The hook's context carries the job
        slice its installer's credential was restored for — every job verb but the live
        subscription, the three schedule verbs among them — so the cadence an enable owns is
        registered by the enable, rather than waiting for the first dispatch or the first
        settlement to notice there is none. A hook whose installer has been revoked is served
        none, and then the cycle runs against a slice that refuses every verb and records the
        refusal: what it can still do is the store's own half of the work.

        Hooks may read wake-local inventory, roster and service metadata under their own
        credential, but have no service effects or repository reads. The folders this cycle
        would have identified are left for a cycle a door wakes. Metadata never enters the
        retained enable state.
      */
      const installer = ctx.jobs;
      try {
        await dispatched.run(
          {
            database,
            storage: ctx.storage,
            machines: ctx.machines,
            jobs: ctx.jobs,
            ...(ctx.host === undefined || ctx.services === undefined
              ? {}
              : { readingMetadata: { host: ctx.host, services: ctx.services } }),
          },
          async () => {
            await cycle(
              installer === undefined ? unauthorized(ENABLE_WITHOUT_JOBS) : jobsSlice(installer),
              unaskable(HOOK_WITHOUT_MACHINES),
              ctx.actions,
              // The installer is not observable, so each enable is an account chain of its own:
              // no later enable, and no door, finishes a posting this one made (#470).
              enableChain(),
              undefined,
              installer !== undefined,
            );
          },
        );
      } catch (error) {
        console.warn(`${BABEL_PLUGIN_ID}: the cycle at enable failed: ${message(error)}`);
      }
    },
    onDisable() {
      // The engine closes the file; holding a handle across a disable would keep a stale one.
      enabled = undefined;
    },
    /**
     * THE ONE WAKE A BACKGROUND HALF GETS (#505). A job this plugin started reached a terminal
     * state, and the hook is handed the job's OWN authority to read it back with — so the cycle
     * that ingests its outputs runs under the credential the job ran under rather than under
     * some later caller's.
     *
     * A job another plugin started is not this plugin's to ingest. The host delivers a settled
     * job only to the plugin that started it, so this is a second door on the same rule rather
     * than the only one: what it costs is a string comparison, and what it buys is that a
     * mis-delivered frame does nothing at all.
     */
    async onJobSettled(ctx, job) {
      if (job.pluginId !== BABEL_PLUGIN_ID || job.operationId === MACHINE_OPERATIONS.recall) return;
      const database = ctx.database;
      if (database === undefined) {
        throw new Error(`${BABEL_PLUGIN_ID}: a settled job was served without the plugin's tables`);
      }
      await dispatched.run(
        {
          database,
          storage: ctx.storage,
          machines: ctx.machines,
          jobs: ctx.jobs,
          ...(ctx.host === undefined || ctx.services === undefined
            ? {}
            : { readingMetadata: { host: ctx.host, services: ctx.services } }),
        },
        async () => {
          if (job.operationId === MACHINE_OPERATIONS.citationBackfill) {
            // This wake may ingest only its own native receipt, never authorize a paid cycle.
            const result = await reconcileCitationBackfill(store, jobsSlice(ctx.jobs), {
              kind: "job",
              machineId: job.machineId,
              operationId: job.operationId,
              jobId: job.jobId,
            });
            for (const note of result?.notes ?? [])
              console.warn(`${BABEL_PLUGIN_ID}: citation ${job.jobId}: ${note}`);
          } else if (job.operationId === MACHINE_OPERATIONS.mapCatalog) {
            // The free lane: it carries no paid authority, so it never refills a paid drain.
            for (const note of await catalogCycle(jobsSlice(ctx.jobs), job.machineId))
              console.warn(`${BABEL_PLUGIN_ID}: catalog ${job.machineId}: ${note}`);
          } else if (job.operationId === MACHINE_OPERATIONS.mapPrepare) {
            // A `map-prepare` job is a drain's preparation or cadence, posted under that drain's
            // credential and woken for that drain alone, or a preparation of the standing lane,
            // woken under the chain it was posted with (#469, #470).
            await mapDrainCycle(jobsSlice(ctx.jobs), ctx.actions, job);
          } else if (job.scheduleId?.startsWith(DRAIN_SCHEDULE_PREFIX)) {
            const jobs = jobsSlice(ctx.jobs);
            const own = (await activeDrains(store)).find(
              (row) =>
                row.preset !== MAP_DRAIN_PRESET &&
                row.machineId === job.machineId &&
                job.operationId === BEAT_OPERATION &&
                ordinaryDrainWakeId(row.id) === job.scheduleId,
            );
            if (own === undefined) {
              for (const note of await ordinaryDrainWakes(jobs))
                console.warn(`${BABEL_PLUGIN_ID}: ${note}`);
            } else {
              await ordinaryDrainCycle(jobs, ctx.actions, own);
            }
          } else {
            await cycle(
              jobsSlice(ctx.jobs),
              unaskable(HOOK_WITHOUT_MACHINES),
              ctx.actions,
              // The settled job's own credential is the one that posted it: its recorded chain.
              await settledChain(job),
              undefined,
              true,
            );
          }
        },
      );
    },
  },
};

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export default plugin;
defineServerPlugin(plugin);
