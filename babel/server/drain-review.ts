import type { SqlParam, SqlStatement } from "@manifold/plugin";
import { z } from "zod";
import { CodeProfileSchema, ENGINE_REFUSALS, LaunchInputSchema, OPERATIONS } from "../contract.ts";
import type { LaunchIdentity, Started } from "../doors/launch.ts";
import type { Policy, ReviewAssignment } from "../store/coordinator.ts";
import { directDrainAdmission, finishDirectLaunch, type DrainRow } from "../store/drains.ts";
import {
  finishClosedReview,
  projectReview,
  settleReviewSession,
  type PendingRun,
} from "./conductor.ts";
import type { DrainDeps } from "./drain.ts";
import {
  blindedLeak,
  composeReviewPrompt,
  reviewPreparation,
  type ReviewPreparation,
} from "./engine/review.ts";
import {
  PROMPT_LIMIT,
  promptBytes,
  type CodeJob,
  type EngineAnswer,
  type SessionRead,
} from "./engine/session.ts";

type Deps = Pick<DrainDeps, "store" | "coordinator" | "engine" | "chain" | "now">;
type Guard = { readonly sql: string; readonly params: readonly SqlParam[] };
type Parent = Omit<PendingRun, "job_id"> & {
  job_id: string | null;
  closure: string | null;
  payload: string;
};
const IntentSchema = z.object({
  drainId: z.string(),
  chain: z.string().nullable(),
  policyVersion: z.string(),
  request: z.object({
    profile: CodeProfileSchema,
    machineId: z.string(),
    prompt: z.string(),
    inferenceLimits: LaunchInputSchema.shape.inferenceLimits,
    postingKey: z.string(),
  }),
});
type Intent = z.infer<typeof IntentSchema>;

class OversizedReviewPrompt extends Error {}

function document(value: string | null): Record<string, unknown> {
  try {
    return JSON.parse(value ?? "{}") as Record<string, unknown>;
  } catch {
    return {};
  }
}
function intentOf(run: Parent): Intent {
  return IntentSchema.parse(document(run.preparation)["reviewDrain"]);
}
async function parent(deps: Deps, runId: string): Promise<Parent | null> {
  return (
    (
      await deps.store.db.query<Parent>(
        `SELECT id, job_id, machine_id, kind, container_id, prepare_job_id, started_at,
      profile, preparation, unreadable, closure, payload FROM runs WHERE id=?`,
        [runId],
      )
    )[0] ?? null
  );
}
function routeMatches(policy: Policy, row: DrainRow): boolean {
  return (
    policy.enabled &&
    policy.activityWeights.review > 0 &&
    policy.review !== undefined &&
    policy.review.machineId === row.machineId &&
    policy.review.profile.containerId === row.profile.profile.containerId &&
    policy.review.profile.expectedRevision === row.profile.profile.expectedRevision
  );
}
function admission(row: DrainRow, runId: string, version: string, now: number): Guard {
  const drain = directDrainAdmission(row.id, runId, now);
  return {
    sql: `${drain.sql} AND EXISTS (SELECT 1 FROM policies WHERE seq=(SELECT MAX(seq) FROM policies)
      AND version=? AND json_extract(payload,'$.enabled')=1
      AND json_extract(payload,'$.activityWeights.review')>0
      AND json_extract(payload,'$.review.machineId')=?
      AND json_extract(payload,'$.review.profile.containerId')=?
      AND json_extract(payload,'$.review.profile.expectedRevision')=?)`,
    params: [
      ...drain.params,
      version,
      row.machineId,
      row.profile.profile.containerId,
      row.profile.profile.expectedRevision,
    ],
  };
}
function preparation(
  assignment: ReviewAssignment,
  fence: number,
  recipe: { id: string; version: number },
  refinementDepth: number,
  maxRefinementDepth: number,
): ReviewPreparation {
  return {
    assignmentId: assignment.id,
    recordId: assignment.recordId,
    revisionId: assignment.recordId,
    rootId: assignment.rootId,
    kind: assignment.kind,
    role: assignment.role,
    lane: assignment.lane,
    policyVersion: assignment.policyVersion,
    fence,
    ordinal: assignment.ordinal,
    seed: assignment.seed,
    inputDigest: assignment.inputDigest,
    refinementDepth,
    maxRefinementDepth,
    blinded: true,
    recipe: { id: recipe.id, version: recipe.version },
  };
}

/**
 * Review ordinals share the durable drain's coordinator cycle and its day-window ceiling.
 * Refills and restarts never reset that budget; parent runs and Code posting keys stay unique.
 * The coordinator's grant transaction enforces the shared ceiling as it publishes the parent.
 */
export async function startDrainReview(
  deps: Deps,
  row: DrainRow,
  identity: LaunchIdentity,
): Promise<Started> {
  let run = await parent(deps, identity.runId);
  let published = false;
  if (run === null) {
    const publicationId = crypto.randomUUID();
    const policy = (await deps.coordinator.policy(deps.now())).policy;
    if (!routeMatches(policy, row))
      return { refused: "review route no longer matches this drain's authority" };
    const route = policy.review!;
    const drawn = await deps.coordinator.draw({
      runId: row.id,
      machines: [row.machineId],
      only: "review",
      now: deps.now(),
    });
    if (drawn.outcome === "gap")
      return {
        refused: `${drawn.gap.reason}: ${drawn.gap.detail}`,
        code:
          drawn.gap.reason === "no-candidates" || drawn.gap.reason === "no-lane"
            ? "no_eligible_work"
            : drawn.gap.reason,
      };
    if (drawn.assignment.activity !== "review")
      throw new Error("review-only draw returned another activity");
    const assignment = drawn.assignment;
    const recipe = route.recipes.find((entry) => entry.id === route.roleRecipes[assignment.role]);
    if (recipe === undefined)
      return { refused: `review role ${assignment.role} has no installed recipe` };
    const projection = await projectReview(deps.store, assignment.recordId);
    if (projection === null || blindedLeak(projection.target) !== "")
      return { refused: "review projection is unavailable or not blinded" };
    const payload = projection.target["payload"] as Record<string, unknown> | null;
    const depth = (payload?.["refinement"] as { depth?: unknown } | undefined)?.depth;
    const refinementDepth =
      typeof depth === "number" && Number.isInteger(depth) && depth >= 0 ? depth : 0;
    const guard = admission(row, identity.runId, policy.version, deps.now());
    const claimed = await deps.coordinator
      .claim({
        assignment,
        runId: row.id,
        jobId: identity.jobId,
        now: deps.now(),
        publication: {
          guard: {
            sql: `${guard.sql} AND NOT EXISTS (SELECT 1 FROM runs WHERE id=?)`,
            params: [...guard.params, identity.runId],
          },
          statements(fence): readonly SqlStatement[] {
            const review = preparation(
              assignment,
              fence,
              recipe,
              refinementDepth,
              route.maxRefinementDepth ?? 2,
            );
            const prompt = composeReviewPrompt({
              assignment,
              preparation: review,
              recipe,
              projection,
            });
            const bytes = promptBytes(prompt);
            if (bytes > PROMPT_LIMIT)
              throw new OversizedReviewPrompt(
                `the ${assignment.role} review prompt is ${String(bytes)} bytes and Code accepts ` +
                  `${String(PROMPT_LIMIT)}; shorten recipe ${recipe.id} rather than dropping the record contract`,
              );
            const intent: Intent = {
              drainId: row.id,
              chain: identity.chain,
              policyVersion: policy.version,
              request: {
                profile: route.profile,
                machineId: row.machineId,
                prompt,
                postingKey: identity.runId,
                ...(row.knobs.inferenceLimits === undefined
                  ? {}
                  : { inferenceLimits: row.knobs.inferenceLimits }),
              },
            };
            return [
              {
                sql: `INSERT INTO runs(id,kind,machine_id,container_id,prepare_job_id,recipe_id,profile,
                authority_kind,authority_id,preparation,started_at,records,payload)
              SELECT ?,?,?,?,?,?,?,'operator',?,?,?,0,? WHERE EXISTS (
                SELECT 1 FROM claims WHERE id=? AND run_id=? AND fence=? AND finished_at IS NULL)
              ON CONFLICT(id) DO NOTHING`,
                params: [
                  identity.runId,
                  OPERATIONS.evaluate,
                  row.machineId,
                  route.profile.containerId,
                  identity.jobId,
                  recipe.id,
                  JSON.stringify(row.profile),
                  identity.authorityId,
                  JSON.stringify({ review, reviewDrain: intent }),
                  new Date(deps.now()).toISOString(),
                  JSON.stringify({
                    closure: null,
                    posting: true,
                    postingChain: identity.chain,
                    publicationId,
                    requestedAt: deps.now(),
                  }),
                  assignment.id,
                  row.id,
                  fence,
                ],
              },
              {
                sql: `UPDATE drain_launches SET reserved_cost_micros=MAX(COALESCE(reserved_cost_micros,0),?)
              WHERE run_id=? AND EXISTS (SELECT 1 FROM runs WHERE id=?)`,
                params: [
                  Math.ceil(assignment.reservedCost * 1_000_000),
                  identity.runId,
                  identity.runId,
                ],
              },
            ];
          },
        },
      })
      .catch((error: unknown) => {
        // Prompt composition happens before the claim transaction. Only this known preflight
        // refusal retires the unused slot; database/publication failures remain visible errors.
        if (!(error instanceof OversizedReviewPrompt)) throw error;
        return {
          outcome: "refused" as const,
          refusal: { reason: "invalid" as const, detail: error.message },
        };
      });
    run = await parent(deps, identity.runId);
    if (run === null)
      return {
        refused:
          claimed.outcome === "refused"
            ? claimed.refusal.detail
            : "review intent was not published",
      };
    published =
      claimed.outcome === "granted" && document(run.payload)["publicationId"] === publicationId;
  }
  if (run.closure !== null) {
    await finishClosedReview(deps, run);
    return { runId: run.id, jobId: run.job_id ?? identity.jobId };
  }
  if (run.job_id !== null) return { runId: run.id, jobId: run.job_id };
  const intent = intentOf(run);
  // The owner chain is part of Code's keyed identity, not merely provenance.
  if (intent.chain === null ? !published : intent.chain !== deps.chain)
    return { refused: "review posting waits for its original account chain", pending: true };
  const review = reviewPreparation(document(run.preparation))!;
  const guard = admission(row, run.id, intent.policyVersion, deps.now());
  const allowed = await deps.store.db.query(
    `SELECT 1 WHERE ${guard.sql}
    AND EXISTS (SELECT 1 FROM claims WHERE id=? AND run_id=? AND fence=? AND finished_at IS NULL AND expires_at>?)
    AND EXISTS (SELECT 1 FROM runs WHERE id=? AND closure IS NULL AND COALESCE(json_extract(payload,'$.stopRequested'),0)=0)`,
    [
      ...guard.params,
      review.assignmentId,
      intent.drainId,
      review.fence,
      new Date(deps.now()).toISOString(),
      run.id,
    ],
  );
  if (allowed.length === 0) {
    await stopDrainReview(deps, row, run.id, "review admission is no longer authorized");
    return { refused: "review admission is no longer authorized", pending: true };
  }
  let answered;
  try {
    answered = await deps.engine.runSession(intent.request);
  } catch (error) {
    return { refused: `review posting unresolved: ${String(error)}`, pending: true };
  }
  if (!answered.ok) {
    // Even a refusal may be a lost acknowledgement; only retiring the key proves no spend.
    await stopDrainReview(deps, row, run.id, answered.refused);
    return { refused: answered.refused, code: answered.code, pending: true };
  }
  const held = await publishJob(deps, row, run, answered.value);
  if (!held)
    await stopDrainReview(deps, row, run.id, "review posting acknowledged after authority ended");
  return { runId: run.id, jobId: answered.value.jobId };
}

async function publishJob(deps: Deps, row: DrainRow, run: Parent, job: CodeJob): Promise<boolean> {
  const intent = intentOf(run);
  const review = reviewPreparation(document(run.preparation))!;
  const guard = admission(row, run.id, intent.policyVersion, deps.now());
  const result = await deps.store.db.batch([
    deps.coordinator.bindStatement({
      id: review.assignmentId,
      runId: intent.drainId,
      fence: review.fence,
      jobId: job.jobId,
      previousJobId: run.prepare_job_id!,
      now: deps.now(),
      guard: {
        sql: `${guard.sql} AND EXISTS (SELECT 1 FROM runs WHERE id=? AND closure IS NULL
        AND COALESCE(json_extract(payload,'$.stopRequested'),0)=0)`,
        params: [...guard.params, run.id],
      },
    }),
    {
      sql: `UPDATE runs SET job_id=?,payload=json_set(payload,'$.posting',json('false'))
        WHERE changes()>0 AND id=? AND closure IS NULL AND (job_id IS NULL OR job_id=?) RETURNING id`,
      params: [job.jobId, run.id, job.jobId],
    },
  ]);
  if ((result[1]?.length ?? 0) > 0) return true;
  // Retain a late acknowledgement without binding new result authority. Its bill is still owed.
  await deps.store.db.run(
    `UPDATE runs SET job_id=?,payload=json_set(payload,'$.stopRequested',json('true'))
    WHERE id=? AND closure IS NULL AND job_id IS NULL`,
    [job.jobId, run.id],
  );
  return false;
}

/** Cancellation is not proof until Code reports a terminal job or retires an unused key. */
export async function stopDrainReview(
  deps: Deps,
  row: DrainRow,
  runId: string,
  reason: string,
): Promise<{ cancelled: boolean; notes: string[] }> {
  let run = await parent(deps, runId);
  if (run === null) {
    // Claim+parent are atomic; fencing the slot also fences any competing publication.
    await finishDirectLaunch(deps.store, runId, reason);
    return { cancelled: true, notes: [] };
  }
  if (run.closure !== null) {
    await finishClosedReview(deps, run);
    return { cancelled: true, notes: [] };
  }
  await deps.store.db.run(
    `UPDATE runs SET payload=json_set(payload,'$.stopRequested',json('true'),'$.stopReason',?) WHERE id=? AND closure IS NULL`,
    [reason, runId],
  );
  const intent = intentOf(run);
  if (run.job_id === null) {
    if (intent.chain === null || intent.chain !== deps.chain)
      return {
        cancelled: false,
        notes: [`${runId}: unresolved review posting needs its original account chain`],
      };
    const retired = await deps.engine.runSession({ ...intent.request, adoptOnly: true });
    if (!retired.ok) {
      if (retired.code !== ENGINE_REFUSALS.postingUnknown)
        return {
          cancelled: false,
          notes: [`${runId}: review key retirement unconfirmed: ${retired.refused}`],
        };
      const review = reviewPreparation(document(run.preparation))!;
      // Positive key retirement bars all in-flight keyed retries; zero is now evidence, not a guess.
      await deps.store.db.run(
        `UPDATE runs SET closure='stopped',finished_at=?,cost_usd=0,
        payload=json_set(payload,'$.posting',json('false'),'$.closure','stopped','$.costUsd',0,'$.reason',?)
        WHERE id=? AND job_id IS NULL AND closure IS NULL`,
        [new Date(deps.now()).toISOString(), reason, runId],
      );
      run = (await parent(deps, runId))!;
      if (run.closure !== null) {
        await deps.coordinator.finish({
          id: review.assignmentId,
          runId: intent.drainId,
          fence: review.fence,
          cost: 0,
          outcome: "skipped",
          now: deps.now(),
        });
        return { cancelled: true, notes: [] };
      }
    } else {
      await publishJob(deps, row, run, retired.value);
      run = (await parent(deps, runId))!;
    }
  }
  if (run.job_id === null)
    return { cancelled: false, notes: [`${runId}: review posting remains unresolved`] };
  let cancelled: EngineAnswer<CodeJob>;
  try {
    cancelled = await deps.engine.cancelSession({
      containerId: intent.request.profile.containerId,
      jobId: run.job_id,
    });
  } catch (error) {
    cancelled = { ok: false, code: ENGINE_REFUSALS.unconfirmed, refused: String(error) };
  }
  let observed: EngineAnswer<SessionRead>;
  try {
    observed = await deps.engine.readSession({
      containerId: intent.request.profile.containerId,
      jobId: run.job_id,
    });
  } catch (error) {
    observed = { ok: false, code: ENGINE_REFUSALS.unconfirmed, refused: String(error) };
  }
  const terminal = (job: CodeJob) =>
    ["exited", "interrupted", "cancelled", "refused"].includes(job.state);
  // A positive terminal cancel response carries its own meter even when the subsequent read fails.
  if (!observed.ok && cancelled.ok && terminal(cancelled.value)) {
    const silence: SessionRead["silence"] =
      cancelled.value.state === "cancelled"
        ? "omp_session_cancelled"
        : cancelled.value.state === "refused"
          ? "omp_session_refused"
          : cancelled.value.state === "interrupted"
            ? "omp_session_interrupted"
            : "omp_session_unsealed";
    const read: SessionRead = { job: cancelled.value, session: null, silence };
    observed = { ok: true, value: read };
  }
  if (!observed.ok || !terminal(observed.value.job))
    return {
      cancelled: false,
      notes: [
        `${runId}: review cancellation/terminal receipt unconfirmed${cancelled.ok ? "" : `: ${cancelled.refused}`}`,
      ],
    };
  const notes: string[] = [];
  await settleReviewSession(
    deps,
    deps.now(),
    { ...run, job_id: run.job_id },
    observed.value,
    observed.value.job.state === "cancelled" ? "stopped" : "failed",
    reviewPreparation(document(run.preparation))!,
    [],
    [],
    notes,
    { paid: new Map(), free: new Map() },
  );
  return { cancelled: true, notes };
}

/** Finish the receipt/claim boundary before the controller removes a closed slot. */
export async function reconcileDrainReview(deps: Deps, runId: string): Promise<void> {
  const run = await parent(deps, runId);
  if (run !== null && run.closure !== null) await finishClosedReview(deps, run);
}
