import type { PluginDatabase } from "@manifold/plugin";

/*
  THE ONE HANDLE every slice reads through: the plugin's database (ADR 0034) and the clock,
  with the read methods the store slice exposes. Migrations are not here — the shape is made
  once, by the plugin's enable hook (`../server.ts`), from `schema.ts`.

  STUB (Scaffold, P0): this file belongs to the store slice; replace it with that version.
*/

export interface BabelStore {
  readonly db: PluginDatabase;
  now(): number;
}

export function openStore(db: PluginDatabase, now: () => number = Date.now): BabelStore {
  return { db, now };
}
