import type { GuestDatabase } from "@manifold/plugin-kit";
import type { InstanceServiceDescription } from "@manifold/protocol";
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
    (await db.query("SELECT 1 FROM session_exclusions WHERE selector = ? LIMIT 1", [selector]))
      .length !== 0
  );
}

/**
 * Reader doors fail closed throughout native policy cutover. A successful owner CAS is evidence
 * only for that exact policy digest and the exact exclusions it carried; adding another ban or
 * changing the service configuration invalidates it immediately.
 */
export async function recallEnforcesSessionExclusions(
  db: Database,
  description: InstanceServiceDescription,
): Promise<boolean> {
  const exclusions = await readSessionExclusions(db);
  if (exclusions.length === 0) return true;
  if (!description.configuration?.enabled || description.state !== "ready") return false;
  const receipts = await db.query<{ selectors: string }>(
    "SELECT selectors FROM session_exclusion_enforcements WHERE policy_sha256 = ?",
    [description.configuration.policySha256],
  );
  const receipt = receipts[0];
  if (receipt === undefined) return false;
  const covered = SessionExclusionsSchema.parse(JSON.parse(receipt.selectors));
  return exclusions.every((selector) => covered.includes(selector));
}

export async function recordSessionExclusionEnforcement(
  db: Pick<GuestDatabase, "run">,
  policySha256: string,
  selectors: readonly string[],
  recordedAt: string,
): Promise<void> {
  await db.run(
    `INSERT INTO session_exclusion_enforcements(policy_sha256, selectors, recorded_at)
     VALUES (?, ?, ?) ON CONFLICT(policy_sha256) DO NOTHING`,
    [policySha256, JSON.stringify(SessionExclusionsSchema.parse(selectors)), recordedAt],
  );
}

/** Eligibility over the existing `sessions s` alias; archive/catalog retention is unchanged. */
export const ANALYSABLE_SESSION =
  "NOT EXISTS (SELECT 1 FROM session_exclusions privacy WHERE privacy.selector = s.selector)";
