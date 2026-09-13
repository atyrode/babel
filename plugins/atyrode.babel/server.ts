import { AsyncLocalStorage } from "node:async_hooks";
import {
  defineServerPlugin,
  type GuestDatabase,
  type ServerHandler,
  type ServerPluginDef,
} from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import { ACTIONS, BABEL_PLUGIN_ID, OPERATIONS, type OperationName } from "./contract.ts";
import { babelDoors } from "./doors/index.ts";
import {
  conductor,
  type Conductor,
  type JobsSlice,
  type MachinesSlice,
  type Recipe,
  type RunPlan,
} from "./server/conductor.ts";
import {
  ENABLE_WITHOUT_JOBS,
  HOOK_WITHOUT_MACHINES,
  jobsSlice,
  machinesSlice,
  runPlan,
  unaskable,
  unauthorized,
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
    to read. An ADDITIVE shape — a column with a default, a MINOR version, which `planDataMigration`
    passes both ways and runs nothing for — is applied here too, by column name: `SCHEMA_ADDITIONS`
    is what a store an earlier enable created is missing, and a fresh one already has.

  THE LOOP HAS NO CLOCK. A plugin may not poll as an alternate scheduler (`docs/PLUGINS.md`),
  and a server half has no timer of its own, so `conductor.tick()` is called by something that
  has ALREADY woken this half: one of the plugin's own jobs settling (`onJobSettled`, #505 — the
  one wake a background half gets), a door the operator knocked on, or the enable itself. Every
  step of a cycle is idempotent, which is what makes that safe; what keeps it from becoming the
  forbidden timer is that only three doors wake it and no two dispatches inside half a minute
  wake it twice.
*/

/**
 * The name of the shape an enable leaves behind: `SCHEMA_V1` plus every column
 * `SCHEMA_ADDITIONS` names. `STORE_DATA_VERSION` is the version it reaches, and
 * `2026-09-12-store-v1` — recorded under the same key by the first enable — is its predecessor.
 */
const STORE_MIGRATION = "2026-09-13-store-v1-sessions-live-kind";
/** Where that name is recorded. The engine's own `$migration:` ledger is the engine's to write. */
const SCHEMA_KEY = "schema";
/** One table of the schema, asked for by name: present means this file has been created. */
const SENTINEL_TABLE = "records";

const dispatched = new AsyncLocalStorage<GuestDatabase>();
/** The handle the enable hook was given: what a lifecycle hook and a schedule read through. */
let enabled: GuestDatabase | undefined;

function tables(): GuestDatabase {
  const database = dispatched.getStore() ?? enabled;
  if (database === undefined) {
    throw new Error(`${BABEL_PLUGIN_ID}: no database is bound to this call`);
  }
  return database;
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
  ): Promise<readonly Row[]> => await tables().query<Row>(sql, params),
  run: async (sql: string, params?: readonly SqlParam[]) => await tables().run(sql, params),
  batch: async (statements: readonly SqlStatement[]) => await tables().batch(statements),
};

const store = openStore(database);
const manifest = PluginManifestSchema.parse(manifestJson);
const coordinated = coordinator(store, () => store.now());

/**
 * THE COOKBOOK THIS HUB HOLDS, and the recipe each review role performs.
 *
 * Both are empty, and that is a statement rather than a stub: a recipe's BODY is the method a
 * run performs, `cookbook/recipes` in this repository is 273 KB of markdown against the
 * 65,536-byte ceiling on one job's whole input record, and neither the store (there is no
 * table) nor the policy (`PolicySchema` is strict) carries one. So nothing here can name a
 * method, and the two places that would use one refuse BY NAME instead of inventing it: the
 * launch door tells the operator no cookbook is installed, and a drawn review is reported as
 * `no-recipe` in the cycle's own report. Installing a cookbook is one wiring change here.
 */
const COOKBOOK: Readonly<Record<string, Recipe>> = {};
const ROLE_RECIPES: Readonly<Record<string, string>> = {};

function planFor(policy: Policy, operationId: OperationName): RunPlan {
  return runPlan({ manifest, policy, cookbook: COOKBOOK, roles: ROLE_RECIPES, operationId });
}

function loop(jobs: JobsSlice, machines: MachinesSlice, plan: RunPlan): Conductor {
  return conductor({
    store,
    coordinator: coordinated,
    jobs,
    machines,
    plan,
    now: () => store.now(),
  });
}

/**
 * ONE CYCLE, over the authority the caller brought: a job slice to reach machines through, and
 * the one machine question the loop asks outside a job (what a catalogued folder is, #535).
 *
 * The loop has no clock of its own — a plugin may not poll as an alternate scheduler — so a
 * cycle happens when something has already woken this half: a door the operator knocked on, or
 * one of this plugin's own jobs settling. Every step of it is idempotent, which is what makes
 * that safe: two cycles in the same second do the work of one.
 */
async function cycle(jobs: JobsSlice, machines: MachinesSlice): Promise<void> {
  const policy = (await coordinated.policy()).policy;
  await loop(jobs, machines, planFor(policy, OPERATIONS.evaluate)).tick();
}

/**
 * The doors a cycle follows. They are the two an operator watches and the one that starts work
 * — never every read: `feed`, `record` and `thread` are opened dozens of times while a page is
 * being read, and a cycle behind each of them would turn a reader into a scheduler.
 */
const WAKES: Record<string, true> = {
  [ACTIONS.pulse]: true,
  [ACTIONS.runs]: true,
  [ACTIONS.launch]: true,
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

const doors = babelDoors(store, {
  coordinator: coordinated,
  cookbook: COOKBOOK,
  jobs: (ctx) => jobsSlice(ctx.jobs),
  machines: (ctx) => machinesSlice(ctx.machines),
  plan: planFor,
  cycle: loop,
  now: () => store.now(),
});

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
    const bound = ctx.database;
    if (bound === undefined) return await handler(ctx, args);
    return await dispatched.run(bound, async () => {
      const produced = await handler(ctx, args);
      const at = ctx.now();
      if (wakes && at - woke >= WAKE_FLOOR_MS) {
        woke = at;
        try {
          await cycle(jobsSlice(ctx.jobs), machinesSlice(ctx.machines));
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
      enabled = database;
      const created = await database.query<{ n: number }>(
        "SELECT count(*) AS n FROM sqlite_master WHERE type = 'table' AND name = ?",
        [SENTINEL_TABLE],
      );
      // One batch, so a store either exists whole or was never begun; 58 statements against a
      // bound of 256, which is the reason the schema may stay one list.
      if (Number(created[0]?.n ?? 0) === 0) {
        await database.batch(SCHEMA_V1.map((sql) => ({ sql })));
      } else {
        // A store an earlier shape created reaches this one by the columns it is missing and
        // nothing else. SQLite has no `ADD COLUMN IF NOT EXISTS`, so the column is asked for by
        // name first: this runs on every enable and must do nothing on all but one of them.
        const pending: SqlStatement[] = [];
        for (const addition of SCHEMA_ADDITIONS) {
          const held = await database.query<{ n: number }>(
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
        await dispatched.run(database, async () => {
          await cycle(
            installer === undefined ? unauthorized(ENABLE_WITHOUT_JOBS) : jobsSlice(installer),
            unaskable(HOOK_WITHOUT_MACHINES),
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
      await dispatched.run(database, async () => {
        await cycle(jobsSlice(ctx.jobs), unaskable(HOOK_WITHOUT_MACHINES));
      });
    },
  },
};

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export default plugin;
defineServerPlugin(plugin);
