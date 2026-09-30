import type { GuestCtx } from "@manifold/plugin-kit/server";
import { MachineInventorySchema } from "@manifold/protocol";
import type { JobsSlice } from "./conductor.ts";
import { machineOpenWork, type DrainsStore } from "../store/drains.ts";
export type DrainCapacity =
  | { readonly limit: number; readonly activeJobIds: readonly string[] }
  | { readonly refused: string };

/** A read under this wake's credential, never a retained machine fact. */
export type DrainAdmission = (machineId: string, operationId: string) => Promise<DrainCapacity>;

export async function liveDrainCapacity(
  machines: Pick<GuestCtx["machines"], "inventory"> | undefined,
  machineId: string,
  operationCeiling: number | null,
  jobs: Pick<JobsSlice, "listRuns">,
): Promise<DrainCapacity> {
  const unavailable = (detail: string) => ({
    refused: `physical-core admission unavailable for ${machineId}: ${detail}; existing work is retained`,
  });
  if (machines === undefined) return unavailable("this wake has no machine inventory");
  try {
    const answer = await machines.inventory();
    if (!answer.ok) return unavailable(`${answer.code}: ${answer.message}`);
    const parsed = MachineInventorySchema.safeParse(answer.value);
    if (!parsed.success) return unavailable("invalid machine inventory");
    const machine = parsed.data.machines.find((entry) => entry.id === machineId);
    if (machine === undefined) return unavailable("machine is absent");
    if (!machine.online || machine.revoked) return unavailable("machine is offline or revoked");
    const cores = machine.physicalCoreCount;
    if (!Number.isSafeInteger(cores) || cores === undefined || cores <= 0)
      return unavailable("online physical core count is unknown or invalid");
    // Native cadences exist before settlement writes their Babel run. Keep this observation
    // in the wake, and pass only identities to the store's atomic occupancy predicate.
    const activeJobIds = new Set<string>();
    let cursor: string | undefined;
    do {
      const page = await jobs.listRuns({
        machineId,
        limit: 100,
        ...(cursor === undefined ? {} : { cursor }),
      });
      for (const { job } of page.runs) {
        if (
          job !== null &&
          job.state !== "exited" &&
          job.state !== "interrupted" &&
          job.state !== "cancelled" &&
          job.state !== "refused"
        )
          activeJobIds.add(job.jobId);
      }
      cursor = page.nextCursor ?? undefined;
    } while (cursor !== undefined);
    return { limit: Math.min(operationCeiling ?? cores, cores), activeJobIds: [...activeJobIds] };
  } catch {
    return unavailable("machine inventory or native occupancy read failed");
  }
}

/** A retained slot may resume, but never buy more work above the current live ceiling. */
export async function drainPostingRefusal(
  store: DrainsStore,
  admission: DrainAdmission | undefined,
  runId: string,
  operationId: string,
): Promise<string | null> {
  const drain = (
    await store.db.query<{ machine_id: string }>(
      `SELECT d.machine_id FROM drains d, json_each(d.live) l
        WHERE json_extract(l.value, '$.runId') = ?
       UNION SELECT machine_id FROM runs WHERE id = ? AND json_extract(payload, '$.standing') = 1
       LIMIT 1`,
      [runId, runId],
    )
  )[0];
  if (drain === undefined) return null;
  if (admission === undefined)
    return "physical-core admission unavailable on this wake; existing work is retained";
  const capacity = await admission(drain.machine_id, operationId);
  if ("refused" in capacity) return capacity.refused;
  const open = machineOpenWork(drain.machine_id, capacity.activeJobIds);
  const held = await store.db.query(`SELECT 1 WHERE (${open.sql}) <= ?`, [
    ...open.params,
    capacity.limit,
  ]);
  return held.length > 0
    ? null
    : `physical-core admission holds new postings: machine work exceeds the live ceiling of ${String(capacity.limit)}; existing work is retained`;
}
