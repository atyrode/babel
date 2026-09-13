/*
  THE RECEIPT: what a run was asked, what it read, what it produced and what it cost — the last
  output file every operation writes (plan §4, SPEC.md §7). Ported from internal/explore/receipt.go
  and internal/worker/receipt.go, reduced to `contract.ts`'s ReceiptSchema, which is the shape the
  hub ingests.

  The profile block is the point of it. Code's runtime report states the profile, the model, the
  disclosure class and the cost per 1k BEFORE the first byte of the prompt is written, so the
  receipt records what actually ran from the same source Watch states it from — not what a caller
  asked for. A refused launch therefore still produces a receipt with a profile: the profile is
  what a refused engine saw, and the reason it was refused is beside it.

  What a receipt never carries is what a facility served. §9's asymmetry survives the rewrite for
  the reason it existed: the pipe carries content to the model because a model that cannot read a
  record cannot form an observation about it, while the durable record keeps locators, digests and
  decisions only. So the tool trail here is names, verdicts and reasons, and never a payload.
*/

import { ReceiptSchema, type Receipt } from "../../contract.ts";
import type { EngineOutcome } from "./client.ts";
import type { RuntimeReport } from "./launch.ts";

/** What one operation's receipt is assembled from. */
export interface ReceiptInput {
  runId: string;
  kind: Receipt["kind"];
  machineId: string;
  recipeId?: string;
  role?: string;
  /** The scope the run was fixed over: the preparation id and its selection, as the job named it. */
  preparation?: Record<string, unknown>;
  startedAt: Date;
  finishedAt: Date;
  closure: Receipt["closure"];
  /** Why it is not `completed`. A failed run without a reason is a receipt nobody can act on. */
  reason?: string;
  /** What the run produced, by output file: the numbers the hub reconciles its ingestion against. */
  counts: Readonly<Record<string, number>>;
  /** One per supervised job the operation ran; a multi-stage run has one per stage. */
  jobs: readonly EngineOutcome[];
}

/**
 * Assembles and validates one receipt. It is parsed through ReceiptSchema here rather than at the
 * sink so that a receipt Babel could not have written fails in the operation that built it, where
 * the reason is still in hand.
 */
export function buildReceipt(input: ReceiptInput): Receipt {
  let costUsd: number | null = null;
  let tokens: number | null = null;
  let submissions = 0;
  /** Per tool, how many calls were served and how many refused: the run's boundary, counted. */
  const tools: Record<string, number> = {};
  for (const job of input.jobs) {
    if (job.usage !== null) {
      costUsd = (costUsd ?? 0) + job.usage.costUsd;
      tokens = (tokens ?? 0) + job.usage.totalTokens;
    }
    for (const decision of job.tools) {
      const key = `${decision.tool}.${decision.allowed ? "served" : "refused"}`;
      tools[key] = (tools[key] ?? 0) + 1;
    }
    submissions += job.submissions;
  }
  const profile = profileOf(input.jobs);
  const reason = input.reason ?? failureReason(input.jobs);
  const receipt = {
    runId: input.runId,
    kind: input.kind,
    machineId: input.machineId,
    ...(input.recipeId === undefined ? {} : { recipeId: input.recipeId }),
    ...(input.role === undefined ? {} : { role: input.role }),
    ...(profile === null ? {} : { profile }),
    ...(input.preparation === undefined ? {} : { preparation: input.preparation }),
    startedAt: input.startedAt.toISOString(),
    finishedAt: input.finishedAt.toISOString(),
    closure: input.closure,
    ...(reason === "" ? {} : { reason }),
    ...(costUsd === null ? {} : { costUsd }),
    ...(tokens === null ? {} : { tokens }),
    counts: { ...input.counts, ...tools, jobs: input.jobs.length, submissions },
  };
  return ReceiptSchema.parse(receipt);
}

/**
 * What actually ran, from the runtime report of the last job that got one. The model and the cost
 * are the report's own: a profile block assembled from what the caller asked for would be the one
 * claim in the receipt nobody checked.
 */
function profileOf(jobs: readonly EngineOutcome[]): Record<string, unknown> | null {
  let report: RuntimeReport | null = null;
  let unknown: readonly string[] = [];
  for (const job of jobs) {
    if (job.finished !== null) {
      report = job.finished;
      unknown = job.runtimeUnknown;
    } else if (job.runtime !== null) {
      report = job.runtime;
      unknown = job.runtimeUnknown;
    }
  }
  if (report === null) return null;
  const metadata = report.metadata ?? {};
  return {
    id: report.profile.id,
    revision: report.profile.revision,
    model: metadata["model"] ?? "",
    provider: metadata["provider"] ?? "",
    disclosure: report.privacy?.disclosure ?? "",
    redactionRequired: report.privacy?.redaction_required ?? false,
    currency: report.cost?.currency ?? "",
    costPer1k: { input: report.cost?.input_per_1k ?? 0, output: report.cost?.output_per_1k ?? 0 },
    estimatedRun: report.cost?.estimated_run ?? 0,
    worker: `${report.worker.name}@${report.worker.version}`,
    containment: report.containment?.backend ?? "",
    escape: report.containment?.escape ?? "",
    ...(report.resources === null || report.resources === undefined ? {} : { resources: report.resources }),
    ...(unknown.length === 0 ? {} : { unknownRuntimeFields: [...unknown] }),
  };
}

/** The first job failure, as the code and the message an operator reads. */
function failureReason(jobs: readonly EngineOutcome[]): string {
  for (const job of jobs) {
    if (job.failure !== null) return `${job.failure.code}: ${job.failure.message}`;
  }
  return "";
}
