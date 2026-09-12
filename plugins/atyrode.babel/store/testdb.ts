/*
  A REAL DATABASE FOR THE TESTS.

  The read model is SQL, so a test against a hand-written fake would assert the fake. These tests
  open the engine's own `openPluginDatabase` (ADR 0034) on a temporary directory and apply
  `SCHEMA_V1`, which means every `STRICT` column, every CHECK and every append-only trigger is in
  force: a fixture that writes a ruling this vocabulary does not admit fails at the insert rather
  than at the assertion.

  The engine lives in a sibling checkout rather than in `node_modules`, so it is resolved at
  runtime and not imported: `MANIFOLD_CHECKOUT` when the operator says where it is, then the
  sibling beside this repository, then the two usual names in the home directory. An isolated
  worktree has no sibling, which is exactly why the list is a list.
*/

import { existsSync, mkdtempSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import type { PluginDatabase, SqlParam } from "@manifold/plugin";
import { SCHEMA_V1 } from "./schema.ts";
import { openStore, type BabelStore } from "./store.ts";

/** What the engine's opener answers with, narrowed to what a test needs of it. */
interface DatabaseHandle extends PluginDatabase {
  close(): void;
}

const CHECKOUTS: readonly (string | undefined)[] = [
  process.env["MANIFOLD_CHECKOUT"],
  resolve(import.meta.dir, "../../../../manifold-db"),
  resolve(import.meta.dir, "../../../../manifold"),
  join(homedir(), "manifold-db"),
  join(homedir(), "manifold"),
];

const OPENER = "packages/server/src/plugin-database.ts";

/** Where the engine that owns `openPluginDatabase` is checked out on this machine. */
export function manifoldCheckout(): string {
  for (const candidate of CHECKOUTS) {
    if (candidate !== undefined && candidate !== "" && existsSync(join(candidate, OPENER))) {
      return candidate;
    }
  }
  throw new Error(
    `no manifold checkout holding ${OPENER}; set MANIFOLD_CHECKOUT to the one with the plugin database`,
  );
}

/** One temporary store with the first migration applied, and the hands to take it down. */
export interface TestStore {
  readonly db: PluginDatabase;
  readonly store: BabelStore;
  /** The instant every read is measured from; the tests move it rather than the wall clock. */
  at(milliseconds: number): void;
  close(): void;
}

export async function openTestStore(nowMs: number): Promise<TestStore> {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-store-"));
  // Dynamic because the specifier is genuinely runtime-selected: the engine is a sibling
  // checkout whose location differs between a developer's tree, CI and an isolated worktree, and
  // a static path would resolve in exactly one of the three.
  const engine: unknown = await import(join(manifoldCheckout(), OPENER));
  if (typeof engine !== "object" || engine === null || !("openPluginDatabase" in engine)) {
    throw new Error(`${OPENER} does not export openPluginDatabase`);
  }
  const open = engine.openPluginDatabase;
  if (typeof open !== "function") throw new Error("openPluginDatabase is not callable");
  const openDatabase = open as (options: { dataDir: string; pluginId: string }) => DatabaseHandle;
  const db = openDatabase({
    dataDir,
    pluginId: "atyrode.babel",
  });
  // One batch, as the plugin's own enable hook applies it: a half-created schema is not a store.
  await db.batch(SCHEMA_V1.map((sql) => ({ sql })));
  let clock = nowMs;
  return {
    db,
    store: openStore(db, () => clock),
    at: (milliseconds) => {
      clock = milliseconds;
    },
    close: () => {
      db.close();
      rmSync(dataDir, { recursive: true, force: true });
    },
  };
}

/**
 * Inserts one row, naming its columns from the object's own keys.
 *
 * The fixtures are written as the store's rows rather than through a writer, because the writers
 * are other slices and a read model asserted against them would be asserting two things at once.
 */
export async function insert(
  db: PluginDatabase,
  table: string,
  row: Readonly<Record<string, SqlParam>>,
): Promise<void> {
  const columns = Object.keys(row);
  await db.run(
    `INSERT INTO ${table}(${columns.join(",")}) VALUES(${columns.map(() => "?").join(",")})`,
    columns.map((column) => row[column] as SqlParam),
  );
}
