import { AsyncLocalStorage } from "node:async_hooks";
import {
  defineServerPlugin,
  type GuestDatabase,
  type ServerHandler,
  type ServerPluginDef,
} from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import type { PluginDatabase, SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import { BABEL_PLUGIN_ID } from "./contract.ts";
import { babelDoors } from "./doors/index.ts";
import { SCHEMA_V1 } from "./store/schema.ts";
import { openStore } from "./store/store.ts";
import manifestJson from "./manifest.json";

/*
  THE SERVER HALF OF atyrode.babel: the store, and the doors over it.

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
    runs on a first enable; the chain exists for a MAJOR bump over data that already exists.
    `2026-09-12-store-v1` is therefore the name of the shape this enable creates, recorded as a
    key so the next one is a real migration with a predecessor to read.
*/

/** The name of the shape `SCHEMA_V1` creates; `STORE_DATA_VERSION` is the version it reaches. */
const STORE_MIGRATION = "2026-09-12-store-v1";
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
const doors = babelDoors(store);

/**
 * Every door, with the calling context's own database bound for the length of its handler. A
 * dispatch the host served no database to runs anyway and fails at its first statement, by
 * name: a plugin that declared `database` and got none is a host bug, not a caller's refusal.
 */
const handlers: Record<string, ServerHandler> = {};
for (const [name, handler] of Object.entries(doors.handlers)) {
  handlers[name] = (ctx, args) => {
    const bound = ctx.database;
    return bound === undefined ? handler(ctx, args) : dispatched.run(bound, () => handler(ctx, args));
  };
}

export const plugin: ServerPluginDef = {
  manifest: PluginManifestSchema.parse(manifestJson),
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
      }
      await ctx.storage.set(SCHEMA_KEY, STORE_MIGRATION);
    },
    onDisable() {
      // The engine closes the file; holding a handle across a disable would keep a stale one.
      enabled = undefined;
    },
  },
};

export default plugin;
defineServerPlugin(plugin);
