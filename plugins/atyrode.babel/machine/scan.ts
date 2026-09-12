import { z } from "zod";

import type { Receipt } from "../contract.ts";
import type { OutputSink } from "./output.ts";
import {
  ADAPTERS,
  HARNESSES,
  type Adapter,
  type Harness,
  type SessionFacts,
  type SessionRef,
} from "./adapters/index.ts";
import { repositoryObserver } from "./repository.ts";

/*
  THE `scan` OPERATION (plan §4): the sessions on this machine, as catalog rows.

  It replaces `babel scan` and the per-machine catalog refresh. What it writes is
  JOB_OUTPUT_FILES.sessions — one row per session in the store's own `sessions` shape — and a
  receipt. It reads the live session files in place, never writes into them, and never fails
  over one session: a log that vanished mid-scan, a directory that turned unreadable, a
  transcript whose head is garbage each cost their own row and nothing else.

  Two things the rows deliberately do NOT carry. `snapshot_id` and `archived_at` are absent
  keys rather than nulls, because the `archive` operation observes those and a scan that wrote
  nulls for them would erase an archive's answer on the next beat. And the reasons an adapter
  could not observe a field have no column: they are the scan's own evidence, so they are
  counted into the receipt instead of being flattened into the row.
*/

export const ScanInputSchema = z.strictObject({
  /** The enrolled machine this scan runs on; it is what `sessions.host` records. */
  machineId: z.string().trim().min(1).max(120),
  /**
   * The run this scan is. A launched run is given one; a scheduled beat's input is fixed at
   * registration and carries none, so the machine half mints it and the receipt is what tells
   * the hub which run its job was.
   */
  runId: z.string().trim().min(1).max(120).optional(),
  /** Where to look. Empty means every adapter's default roots on this machine. */
  roots: z.array(z.string().trim().min(1).max(4096)).max(64).default([]),
  /** Which harnesses to read. Empty means all of them. */
  harnesses: z.array(z.enum(HARNESSES)).max(HARNESSES.length).default([]),
});
export type ScanInput = z.infer<typeof ScanInputSchema>;

/**
 * One catalog row, in the store's own column names (store/schema.ts `sessions`). The machine
 * half speaks the table's shape so ingestion is a `batch` and nothing on the way in
 * reinterprets a value.
 */
export interface SessionCatalogRow {
  selector: string;
  host: string;
  harness: Harness;
  source_id: string;
  title: string | null;
  title_provenance: string | null;
  workspace: string | null;
  repository_identity: string | null;
  repository_remote: string | null;
  repository_reason: string | null;
  modified_at: string | null;
  size: number;
  cost_usd: number | null;
  total_tokens: number | null;
  turns: number | null;
  tool_errors: number | null;
  content_digest: string;
  seen_at: string;
}

/**
 * How many sessions are described at once. Describing one is a full read of its log, so the
 * work is IO-bound and a handful in flight keeps a corpus of thousands off one spindle-at-a-
 * time pace; more would only queue on the same disk.
 */
const DESCRIBE_CONCURRENCY = 4;

export async function scan(input: ScanInput, out: OutputSink): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  const runId = input.runId ?? "run_" + crypto.randomUUID().replaceAll("-", "");
  const wanted = input.harnesses;
  const adapters = ADAPTERS.filter((adapter) => wanted.length === 0 || wanted.includes(adapter.harness));

  const refs: SessionRef[] = [];
  const byHarness: Partial<Record<Harness, Adapter>> = {};
  for (const adapter of adapters) {
    byHarness[adapter.harness] = adapter;
    const roots = input.roots.length > 0 ? input.roots : adapter.defaultRoots();
    refs.push(...(await adapter.discover(roots)));
  }

  const repositories = repositoryObserver();
  const rows: SessionCatalogRow[] = [];
  const failures: string[] = [];
  const counts = {
    sessions: 0,
    titled: 0,
    with_workspace: 0,
    with_repository: 0,
    repositories: 0,
    with_usage: 0,
    bytes: 0,
    skipped: 0,
  };
  const identities = new Set<string>();
  const perHarness: Record<string, number> = {};

  let next = 0;
  const workers = Array.from({ length: Math.min(DESCRIBE_CONCURRENCY, refs.length) }, async () => {
    for (;;) {
      const index = next++;
      const ref = refs[index];
      if (ref === undefined) return;
      const adapter = byHarness[ref.harness];
      if (adapter === undefined) continue;
      let facts: SessionFacts;
      try {
        facts = await adapter.describe(ref);
      } catch (cause) {
        // A session that cannot be read is one row missing, not a failed scan.
        counts.skipped++;
        failures.push(`${ref.selector}: ${cause instanceof Error ? cause.message : String(cause)}`);
        continue;
      }
      const repository = await repositories.observe(facts.workspace);
      rows.push({
        selector: ref.selector,
        host: input.machineId,
        harness: ref.harness,
        source_id: ref.sourceId,
        title: facts.title,
        title_provenance: facts.titleProvenance,
        workspace: facts.workspace,
        repository_identity: repository.identity,
        repository_remote: repository.remote === "" ? null : repository.remote,
        repository_reason: repository.reason,
        modified_at: facts.modifiedAt,
        size: facts.size,
        cost_usd: facts.usage?.costUsd ?? null,
        total_tokens: facts.usage?.totalTokens ?? null,
        turns: facts.usage?.turns ?? null,
        tool_errors: facts.usage?.toolErrors ?? null,
        content_digest: facts.contentDigest,
        seen_at: startedAt,
      });
      counts.sessions++;
      counts.bytes += facts.size;
      perHarness[ref.harness] = (perHarness[ref.harness] ?? 0) + 1;
      if (facts.title !== null) counts.titled++;
      if (facts.workspace !== null) counts.with_workspace++;
      if (facts.usage !== null) counts.with_usage++;
      if (repository.identity !== null) {
        counts.with_repository++;
        identities.add(repository.identity);
      }
    }
  });
  await Promise.all(workers);
  counts.repositories = identities.size;
  rows.sort((a, b) => (a.selector < b.selector ? -1 : a.selector > b.selector ? 1 : 0));

  await out.write("sessions", rows);
  const receipt: Receipt = {
    runId,
    kind: "scan",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure: "completed",
    counts: { ...counts, ...perHarness },
    // A scan that skipped sessions completed and says so: the count is in `counts`, the first
    // few names are here, because "which one" is the question an operator asks next.
    ...(failures.length > 0 ? { reason: failures.slice(0, 5).join("; ") } : {}),
  };
  await out.receipt(receipt);
  return receipt;
}
