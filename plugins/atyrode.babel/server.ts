import { AsyncLocalStorage } from "node:async_hooks";
import {
  defineServerPlugin,
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
  DRAIN_CONCURRENT_MAX,
  MACHINE_OPERATIONS,
  type OperationName,
} from "./contract.ts";
import { babelDoors } from "./doors/index.ts";
import { launchMachinery, type LaunchDeps } from "./doors/launch.ts";
import type { Recipe } from "./server/engine/prompts.ts";
import { codeEngine, type ActionsSlice } from "./server/engine/session.ts";
import { drainTick, type DrainDeps } from "./server/drain.ts";
import {
  conductor,
  type Conductor,
  type JobsSlice,
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
import { coordinator, type Policy } from "./store/coordinator.ts";
import { SCHEMA_ADDITIONS, SCHEMA_V1 } from "./store/schema.ts";
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
  and a server half has no timer of its own, so `conductor.tick()` is called by something that
  has ALREADY woken this half: one of the plugin's own jobs settling (`onJobSettled`, #505 — the
  one wake a background half gets), a door the operator knocked on, or the enable itself. Every
  step of a cycle is idempotent, which is what makes that safe; what keeps it from becoming the
  forbidden timer is that only three doors wake it and no two dispatches inside half a minute
  wake it twice.
*/

/**
 * The name of the shape an enable leaves behind: `SCHEMA_V1` plus every column and table
 * `SCHEMA_ADDITIONS` names. `STORE_DATA_VERSION` is the version it reaches, and
 * `2026-09-14-store-v1-drains` — recorded under the same key by the enable before it — is its
 * predecessor.
 */
const STORE_MIGRATION = "2026-09-14-store-v1-code-session-silence";
/** Where that name is recorded. The engine's own `$migration:` ledger is the engine's to write. */
const SCHEMA_KEY = "schema";
/** One table of the schema, asked for by name: present means this file has been created. */
const SENTINEL_TABLE = "records";

/**
 * WHAT A CALL'S OWN CONTEXT SERVES THIS PLUGIN: its tables and its keys, for the length of it.
 *
 * Both are per-call for the same reason (a hardened row's handles are closed once the request
 * has answered), so both are resolved through the same `AsyncLocalStorage` rather than captured
 * for the process. In-realm the engine's handles are the same objects every time and the store
 * and the loop below never notice the difference.
 */
type Bound = { readonly database: GuestDatabase; readonly storage: GuestStorage };

const dispatched = new AsyncLocalStorage<Bound>();
/** What the enable hook was given: what a lifecycle hook and a schedule read through. */
let enabled: Bound | undefined;

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
/**
 * WHAT BOUNDS A DRAIN'S FAN while no operation declares a ceiling: the contract's own
 * `DRAIN_CONCURRENT_MAX`, which is what `DrainStartRequestSchema` already admits. A door that
 * took `null` as "unbounded" would let one machine be asked for any number of jobs at once.
 */
const DRAIN_FAN = CONCURRENT_JOBS ?? DRAIN_CONCURRENT_MAX;
const coordinated = coordinator(store, () => store.now(), CONCURRENT_JOBS);

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
  jobs: JobsSlice,
  machines: MachinesSlice,
  actions: ActionsSlice | undefined,
  plan: RunPlan,
): Conductor {
  return conductor({
    store,
    coordinator: coordinated,
    jobs,
    machines,
    // A run that reaches a model is CODE's job, and `onJobSettled` is delivered only to the
    // plugin that started one: the loop learns what became of a session by asking Code, over
    // the authority of whatever wake it is running on (#279).
    engine: codeEngine(actions),
    keys,
    plan,
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
  try {
    const parsed = JSON.parse(payload) as Record<string, unknown>;
    const review = parsed["review"];
    const routed =
      typeof review === "object" && review !== null && !Array.isArray(review)
        ? (review as Record<string, unknown>)["recipes"]
        : undefined;
    held = Array.isArray(routed) ? routed : parsed["recipes"];
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
  jobs: (ctx) => jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
  engine: (actions) => codeEngine(actions),
  cookbook,
  plan: planFor,
  now: () => store.now(),
};

/** The launch path every start goes through, doors and drain controller alike (#258). */
const machinery = launchMachinery(store, LAUNCH_DEPS);

/** The controller's dependencies over one wake's own authority (#258, #279). */
function draining(jobs: BabelJobs, actions: ActionsSlice | undefined): DrainDeps {
  return {
    store,
    coordinator: coordinated,
    launch: machinery,
    jobs,
    engine: codeEngine(actions),
    plan: planFor,
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
 */
async function cycle(
  jobs: BabelJobs,
  machines: MachinesSlice,
  actions: ActionsSlice | undefined,
): Promise<void> {
  const policy = (await coordinated.policy()).policy;
  // The beat is the only job this loop still posts itself, so its operation is what the plan's
  // limits are read for; a run that reaches a model is Code's to post (#279).
  const plan = planFor(policy, MACHINE_OPERATIONS.scan);
  await loop(jobs, machines, actions, plan).tick();
  /*
    …AND THEN THE SESSIONS WHOSE MATERIAL IS NOW SEALED (#592). A job-inputs binding names a
    SETTLED job's output, so a session cannot be posted while its own `prepare` is still
    running: the press leaves the preparation in flight and the run recorded as intent, and
    this is the wake that turns it into a Code session. It runs AFTER the conductor, because
    the conductor is what settled that preparation and wrote the index this reads.
  */
  for (const posted of await machinery.postPrepared(jobs, codeEngine(actions), plan)) {
    if ("refused" in posted) {
      console.warn(`${BABEL_PLUGIN_ID}: run ${posted.runId}: ${posted.refused}`);
    }
  }
  for (const report of await drainTick(draining(jobs, actions))) {
    for (const note of report.notes) {
      console.warn(`${BABEL_PLUGIN_ID}: drain ${report.drainId}: ${note}`);
    }
  }
}

/**
 * The doors a cycle follows. They are the ones an operator watches and the ones that start work
 * — never every read: `feed`, `record` and `thread` are opened dozens of times while a page is
 * being read, and a cycle behind each of them would turn a reader into a scheduler.
 *
 * A DISPATCH IS ALSO THE ONLY CYCLE THAT CAN SEE A RUNNING JOB, which is why these matter more
 * than they look. `follow` is the one verb `GuestHookJobs` omits — "a live subscription belongs
 * to a dispatch, not to a hook" (`plugin-kit/src/server.ts`) — and it is the only read the hub
 * serves for a job that has not finished: `journal` refuses that job `job_unfinished`. So the
 * slice a door's cycle is given carries it and folds where each in-flight run is; the slice a
 * settlement's hook is given does not, and that cycle ingests what ended and says nothing about
 * what has not (#261).
 *
 * `drainStatus` is here for exactly that reason (#258). A settlement wakes the drain on its own
 * hook, but that hook cannot fold where a RUNNING job is, and a drain is watched precisely while
 * its jobs are running: without this wake the panel's tokens-per-minute would advance only when
 * something finished, and "flat for three minutes" — the one no-go the runbook names — would be
 * a fact about the wake rather than about the drain. The floor below still applies, so the
 * panel's five-second poll costs one cycle every thirty seconds.
 *
 * EVERY DOOR IN THIS LIST MUST DELEGATE `jobs:read`. The dispatcher attenuates `ctx.jobs` to what
 * the door declared, so a cycle behind one that does not can read back no job at all: nothing
 * settles, nothing is folded, and the wake is worse than none because `woke` is one floor shared
 * by every poller. It is exported so `server.test.ts` holds the list itself to that.
 */
export const WAKES: Record<string, true> = {
  [ACTIONS.pulse]: true,
  [ACTIONS.runs]: true,
  [ACTIONS.launch]: true,
  [ACTIONS.drainStatus]: true,
};

/**
 * How often a DISPATCH may wake the loop, at most.
 *
 * Watch polls `runs` every five seconds while it is open, so a cycle behind every one of those
 * dispatches would be a thirty-times-an-hour scheduler wearing a reader's clothes — the alternate
 * scheduler `docs/PLUGINS.md` forbids, built out of somebody else's poll. The wake that matters
 * is a settlement, and it arrives on its own hook; this is the safety net under the wake that
 * did not arrive — a hook that overran its two-second bound, a hub restarted mid-run — so it is
 * floored at thirty seconds, six times the panel's own poll, and never floors a settlement or
 * the operator's own launch.
 */
const WAKE_FLOOR_MS = 30_000;
let woke = 0;

const doors = babelDoors(
  store,
  LAUNCH_DEPS,
  {
    coordinator: coordinated,
    deps: (ctx) =>
      draining(
        jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
        ctx.actions,
      ),
    concurrentJobs: DRAIN_FAN,
    now: () => store.now(),
  },
  CONCURRENT_JOBS,
);

/**
 * Every door, with the calling context's own database bound for the length of its handler, and
 * a cycle behind the three that warrant one. A dispatch the host served no database to runs
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
    return await dispatched.run({ database: served, storage: ctx.storage }, async () => {
      const produced = await handler(ctx, args);
      const at = ctx.now();
      if (wakes && at - woke >= WAKE_FLOOR_MS) {
        woke = at;
        try {
          await cycle(
            jobsSlice(ctx.jobs, (node, receive) => ctx.jobs.follow(node, receive)),
            machinesSlice(ctx.machines),
            ctx.actions,
          );
        } catch (error) {
          console.warn(`${BABEL_PLUGIN_ID}: the cycle after ${name} failed: ${message(error)}`);
        }
      }
      return produced;
    });
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
      // One batch, so a store either exists whole or was never begun; 58 statements against a
      // bound of 256, which is the reason the schema may stay one list.
      if (Number(created[0]?.n ?? 0) === 0) {
        await database.batch(SCHEMA_V1.map((sql) => ({ sql })));
      } else {
        // A store an earlier shape created reaches this one by what it is missing and nothing
        // else. SQLite has no `ADD COLUMN IF NOT EXISTS` and no `CREATE TABLE IF NOT EXISTS`
        // worth trusting here, so each addition is asked for by name first — a column of its
        // table, or the table itself when it names no column: this runs on every enable and
        // must do nothing on all but one of them.
        const pending: SqlStatement[] = [];
        for (const addition of SCHEMA_ADDITIONS) {
          const held =
            addition.column === undefined
              ? await database.query<{ n: number }>(
                  "SELECT count(*) AS n FROM sqlite_master WHERE type = 'table' AND name = ?",
                  [addition.table],
                )
              : await database.query<{ n: number }>(
                  "SELECT count(*) AS n FROM pragma_table_info(?) WHERE name = ?",
                  [addition.table, addition.column],
                );
          if (Number(held[0]?.n ?? 0) === 0) pending.push({ sql: addition.sql });
        }
        if (pending.length > 0) await database.batch(pending);
      }
      await ctx.storage.set(SCHEMA_KEY, STORE_MIGRATION);
      /*
        AN ENABLED POLICY REGISTERS ITS BEAT HERE (#534). The hook's context carries the job
        slice its installer's credential was restored for — every job verb but the live
        subscription, the three schedule verbs among them — so the cadence an enable owns is
        registered by the enable, rather than waiting for the first dispatch or the first
        settlement to notice there is none. A hook whose installer has been revoked is served
        none, and then the cycle runs against a slice that refuses every verb and records the
        refusal: what it can still do is the store's own half of the work.

        No hook is served a MACHINES slice either way — `machines.repository` is a dispatch's to
        ask — so the folders this cycle would have identified are left for a cycle a door wakes.
      */
      const installer = ctx.jobs;
      try {
        await dispatched.run({ database, storage: ctx.storage }, async () => {
          await cycle(
            installer === undefined ? unauthorized(ENABLE_WITHOUT_JOBS) : jobsSlice(installer),
            unaskable(HOOK_WITHOUT_MACHINES),
            ctx.actions,
          );
        });
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
      if (job.pluginId !== BABEL_PLUGIN_ID) return;
      const database = ctx.database;
      if (database === undefined) {
        throw new Error(`${BABEL_PLUGIN_ID}: a settled job was served without the plugin's tables`);
      }
      await dispatched.run({ database, storage: ctx.storage }, async () => {
        await cycle(jobsSlice(ctx.jobs), unaskable(HOOK_WITHOUT_MACHINES), ctx.actions);
      });
    },
  },
};

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export default plugin;
defineServerPlugin(plugin);
