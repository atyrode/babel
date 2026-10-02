import type { GuestDatabase } from "@manifold/plugin-kit";
import { SessionExclusionsSchema } from "../contract.ts";

type Database = Pick<GuestDatabase, "query">;

/** Owner choices survive catalog replacement and apply to every capture of the selector. */
export async function readSessionExclusions(db: Database): Promise<readonly string[]> {
  const rows = await db.query<{ selector: string }>(
    "SELECT selector FROM session_exclusions ORDER BY selector",
  );
  return SessionExclusionsSchema.parse(rows.map((row) => row.selector));
}

export async function sessionIsExcluded(db: Database, selector: string): Promise<boolean> {
  return (
    await db.query("SELECT 1 FROM session_exclusions WHERE selector = ? LIMIT 1", [selector])
  ).length !== 0;
}

/** Eligibility over the existing `sessions s` alias; archive/catalog retention is unchanged. */
export const ANALYSABLE_SESSION =
  "NOT EXISTS (SELECT 1 FROM session_exclusions privacy WHERE privacy.selector = s.selector)";
