/*
  THE RECEIPT: what a run was asked, what it read, what it produced and what it cost — the last
  output file every operation writes (plan §4, SPEC.md §7). Ported from internal/explore/receipt.go
  and internal/worker/receipt.go, reduced to `contract.ts`'s ReceiptSchema, which is the shape the
  hub ingests.

  The profile block is the point of it. It is BABEL'S OWN launch report now (#279): the model the
  run was asked for, the thinking level, the account it spent, the boundary this process observed
  around itself and — after the engine exits — the exit status, the models that actually answered
  and the NAMED cause of a failure. Code's runtime-info sidecar is gone with Code's engine,
  and what replaced it is better evidence rather than worse: every field is something the
  launcher observed or was handed as a job input, not an assertion by the process being judged.
  A refused launch therefore still produces a receipt with a profile, which is what a refused
  engine was asked to be, and the reason it was refused is beside it.

  What a receipt never carries is what a facility served. §9's asymmetry survives the rewrite for
  the reason it existed: the pipe carries content to the model because a model that cannot read a
  record cannot form an observation about it, while the durable record keeps locators, digests and
  decisions only. So the tool trail here is names, verdicts and reasons, and never a payload.
*/

import { ReceiptSchema, type Receipt } from "../../contract.ts";
import type { EngineOutcome } from "./client.ts";
import type { LaunchReport } from "./launch.ts";

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
  /** Every model that answered across the run's stages, first heard from first (#261). */
  const models: string[] = [];
  /** Per tool, how many calls were served and how many refused: the run's boundary, counted. */
  const tools: Record<string, number> = {};
  for (const job of input.jobs) {
    if (job.usage !== null) {
      costUsd = (costUsd ?? 0) + job.usage.costUsd;
      tokens = (tokens ?? 0) + job.usage.totalTokens;
    }
    for (const model of job.models) if (!models.includes(model)) models.push(model);
    for (const decision of job.tools) {
      const key = `${decision.tool}.${decision.allowed ? "served" : "refused"}`;
      tools[key] = (tools[key] ?? 0) + 1;
    }
    submissions += job.submissions;
  }
  const profile = profileOf(input.jobs);
  const session = sessionOf(input.jobs);
  const reason = input.reason ?? failureReason(input.jobs);
  const receipt = {
    runId: input.runId,
    kind: input.kind,
    machineId: input.machineId,
    ...(input.recipeId === undefined ? {} : { recipeId: input.recipeId }),
    ...(input.role === undefined ? {} : { role: input.role }),
    ...(profile === null ? {} : { profile }),
    ...(session === null ? {} : session),
    ...(input.preparation === undefined ? {} : { preparation: input.preparation }),
    startedAt: input.startedAt.toISOString(),
    finishedAt: input.finishedAt.toISOString(),
    closure: input.closure,
    ...(reason === "" ? {} : { reason }),
    ...(costUsd === null ? {} : { costUsd }),
    ...(tokens === null ? {} : { tokens }),
    ...(models.length === 0 ? {} : { models }),
    counts: { ...input.counts, ...tools, jobs: input.jobs.length, submissions },
  };
  return ReceiptSchema.parse(receipt);
}

/**
 * WHAT ACTUALLY RAN, from the launch report of the last job that wrote one — the post-exit copy
 * when there is one, because that is the copy carrying the exit status, the models that answered
 * and the named cause.
 */
function profileOf(jobs: readonly EngineOutcome[]): Record<string, unknown> | null {
  let report: LaunchReport | null = null;
  let unknown: readonly string[] = [];
  for (const job of jobs) {
    if (job.finished !== null) {
      report = job.finished;
      unknown = job.reportUnknown;
    } else if (job.report !== null) {
      report = job.report;
      unknown = job.reportUnknown;
    }
  }
  if (report === null) return null;
  return {
    schema: report.schema,
    engine: `${report.engine.name}@${report.engine.version}`,
    model: report.session.model,
    thinking: report.session.thinking ?? "",
    provider: report.session.account.provider,
    account: report.session.account.identityKey,
    containment: report.containment?.backend ?? "",
    escape: report.containment?.escape ?? "",
    retries: report.retries,
    ...(report.failure === "" ? {} : { failure: report.failure, failureReason: report.reason }),
    ...(report.exit_code === null || report.exit_code === undefined
      ? {}
      : { exitCode: report.exit_code }),
    ...(unknown.length === 0 ? {} : { unknownReportFields: [...unknown] }),
  };
}

/**
 * THE FLAT PAIR EVERY READER WANTS: whose window this run spent, and which model it asked for
 * (#267, #279). The profile block above says the same two things in the launcher's words; these
 * are what the run row, the drain's fold and the receipt page read, because "drain THIS account"
 * is answered by summing the runs that named it and by nothing else.
 */
function sessionOf(
  jobs: readonly EngineOutcome[],
): { account: { provider: string; identityKey: string }; model: string } | null {
  for (const job of jobs) {
    const report = job.finished ?? job.report;
    if (report === null) continue;
    return {
      account: {
        provider: report.session.account.provider,
        identityKey: report.session.account.identityKey,
      },
      model: report.session.model,
    };
  }
  return null;
}

/** The first job failure, as the code and the message an operator reads. */
function failureReason(jobs: readonly EngineOutcome[]): string {
  for (const job of jobs) {
    if (job.failure !== null) return `${job.failure.code}: ${job.failure.message}`;
  }
  return "";
}
