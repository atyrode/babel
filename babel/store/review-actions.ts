import type { SqlParam, SqlRow, SqlStatement } from "@manifold/plugin";
import { z } from "zod";
import {
  JOB_OUTPUT_FILES,
  JEV_REVIEW_CALLS,
  OPERATIONS,
  ReviewActionReceiptSchema,
  ReviewSubmissionSchema,
  ReviewJudgmentContextSchema,
  type ReviewJudgmentContextInput,
  type ReviewActionReceipt,
  type ReviewSubmission,
} from "../contract.ts";
import {
  MAX_REVIEW_ACTION_BYTES,
  MAX_REVIEW_ACTIONS,
  ReviewActionInputSchema,
  ResultRefusal,
  refusalReason,
  type ReviewActionInput,
  type ReviewResult,
} from "../machine/results.ts";
import { reviewPreparation, reviewRows, validateReviewAction } from "../server/engine/review.ts";
import { mintId, type Row } from "../server/engine/rows.ts";
import { ActRefused, refuseRow, stamp, type ActsStore } from "./acts.ts";
export type { ReviewSubmission } from "../contract.ts";

/** Decode only the pinned submission state, never infer tools from a final message. */
export function reviewSubmission(value: unknown): ReviewSubmission | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return null;
  const parsed = ReviewSubmissionSchema.safeParse(
    (value as Record<string, unknown>)["reviewSubmission"],
  );
  return parsed.success ? parsed.data : null;
}

export async function reviewActionStatus(
  store: ActsStore,
  runId: string,
): Promise<ReviewSubmission | null> {
  const rows = await store.db.query<{ payload: string }>("SELECT payload FROM runs WHERE id=?", [
    runId,
  ]);
  return rows[0] === undefined ? null : reviewSubmission(JSON.parse(rows[0].payload));
}

const ClaimSchema = z.strictObject({
  id: z.string().min(1),
  runId: z.string().min(1),
  fence: z.number().int().min(1),
});

interface HeldRun extends SqlRow {
  id: string;
  kind: string;
  authority_kind: string | null;
  authority_id: string | null;
  preparation: string | null;
  payload: string;
}

interface HeldAction extends SqlRow {
  action_key: string;
  kind: string;
  supersedes_key: string | null;
  input: string;
  receipt: string;
  effects: string;
}

// Property order is not part of a JSON action's identity; array order and every value are.
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (typeof value === "object" && value !== null) {
    const object = value as Record<string, unknown>;
    return `{${Object.keys(object)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonical(object[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}
/** Resolve the host's actor to exactly one persisted assignment, never model-supplied scope. */
async function heldReview(store: ActsStore, actor: { runId: string; agentId: string }) {
  const runs = await store.db.query<HeldRun>(
    `SELECT id,kind,authority_kind,authority_id,preparation,payload FROM runs
      WHERE json_extract(payload,'$.reviewSubmission.mode')='tools'
        AND json_extract(payload,'$.reviewSubmission.agentRunId')=?`,
    [actor.runId],
  );
  const run = runs[0];
  if (run === undefined || runs.length !== 1)
    throw new ActRefused("this Agent Run holds no typed review assignment");
  const payload = JSON.parse(run.payload) as Record<string, unknown>;
  const submission = reviewSubmission(payload);
  if (submission?.mode !== "tools" || submission.agentId !== actor.agentId)
    throw new ActRefused("this Agent does not hold the persisted review assignment");
  return { run, payload, submission };
}

/** Shared live fence for review writes and optional judgment before/after a provider await. */
function reviewAuthority(run: HeldRun, payload: Record<string, unknown>, at: string) {
  const prepared = JSON.parse(run.preparation ?? "null") as Record<string, unknown> | null;
  const preparation = reviewPreparation(prepared);
  const claim = ClaimSchema.safeParse(payload["claim"]);
  const target = prepared?.["reviewTarget"];
  if (
    run.kind !== OPERATIONS.evaluate ||
    run.authority_kind !== "conductor" ||
    preparation === null ||
    !claim.success ||
    claim.data.runId !== run.authority_id ||
    claim.data.id !== preparation.assignmentId ||
    claim.data.fence !== preparation.fence ||
    typeof target !== "object" ||
    target === null ||
    Array.isArray(target) ||
    (target as Record<string, unknown>)["id"] !== preparation.revisionId
  )
    throw new ActRefused("the persisted run has no authoritative review preparation and claim");
  const guard = `EXISTS(SELECT 1 FROM runs r JOIN claims c ON c.id=?
    WHERE r.id=? AND r.payload=? AND r.preparation=? AND r.kind=?
      AND r.authority_kind='conductor' AND r.authority_id=c.run_id
      AND r.closure IS NULL AND r.finished_at IS NULL
      AND coalesce(json_extract(r.payload,'$.stopRequested'),0)=0
      AND c.run_id=? AND c.fence=? AND c.finished_at IS NULL AND c.expires_at>?
      AND c.record_id=? AND c.role=? AND c.lane=? AND c.policy_version=?
      AND (r.job_id IS NULL OR r.job_id=c.job_id))`;
  const guardParams: SqlParam[] = [
    claim.data.id,
    run.id,
    run.payload,
    run.preparation,
    OPERATIONS.evaluate,
    claim.data.runId,
    claim.data.fence,
    at,
    preparation.recordId,
    preparation.role,
    preparation.lane,
    preparation.policyVersion,
  ];
  return { preparation, target, guard, guardParams };
}

function duplicate(action: HeldAction, input: string): ReviewActionReceipt {
  if (action.input !== input)
    throw new ActRefused(
      "this action key already names different work; use a new key and supersedes",
    );
  return ReviewActionReceiptSchema.parse(JSON.parse(action.receipt));
}

const TABLES: Readonly<Record<string, string>> = {
  [JOB_OUTPUT_FILES.records]: "records",
  [JOB_OUTPUT_FILES.edges]: "edges",
  [JOB_OUTPUT_FILES.assessments]: "assessments",
  [JOB_OUTPUT_FILES.filings]: "filings",
  [JOB_OUTPUT_FILES.plans]: "plans",
  [JOB_OUTPUT_FILES.steeringReplies]: "steering",
};

/**
 * One accepted action is one immediate database transaction. Every insert and the payload CAS
 * share the exact live authority predicate. A concurrent stop, takeover or submission either
 * precedes all these writes or follows their commit; no partial acknowledgement can escape.
 */
export async function reviewAction(
  store: ActsStore,
  actor: { runId: string; agentId: string },
  input: ReviewActionInput,
): Promise<ReviewActionReceipt> {
  const encoded = JSON.stringify(input);
  if (new TextEncoder().encode(encoded).byteLength > MAX_REVIEW_ACTION_BYTES) {
    throw new ActRefused(`a review action exceeds ${String(MAX_REVIEW_ACTION_BYTES)} bytes`);
  }
  const parsed = ReviewActionInputSchema.safeParse(input);
  if (!parsed.success) throw new ActRefused(`invalid review action: ${parsed.error.message}`);
  const action = parsed.data;
  const document = canonical(action);
  if (new TextEncoder().encode(document).byteLength > MAX_REVIEW_ACTION_BYTES) {
    throw new ActRefused(`a review action exceeds ${String(MAX_REVIEW_ACTION_BYTES)} bytes`);
  }
  const { run, payload, submission } = await heldReview(store, actor);
  const history = await store.db.query<HeldAction>(
    "SELECT action_key,kind,supersedes_key,input,receipt,effects FROM review_actions WHERE run_id=? ORDER BY sequence",
    [run.id],
  );
  const prior = history.find((held) => held.action_key === action.key);
  // A receipt is durable even after native settlement, lease expiry or a lost acknowledgement.
  // This branch is read-only, still requires the exact trusted Agent/Run, and never replays rows.
  if (prior !== undefined) return duplicate(prior, document);
  if (submission.complete || submission.state === "completed")
    throw new ActRefused("the review already declared completion");
  if (
    history.length >= MAX_REVIEW_ACTIONS ||
    (action.kind !== "complete" && history.length >= MAX_REVIEW_ACTIONS - 1)
  ) {
    throw new ActRefused(
      "the review action limit is reached; the last call is reserved for completion",
    );
  }
  const at = stamp(store.now());
  const { preparation, target, guard, guardParams } = reviewAuthority(run, payload, at);

  const superseded = new Set(
    history.flatMap((held) => (held.supersedes_key === null ? [] : [held.supersedes_key])),
  );
  const active = history.filter(
    (held) => held.kind !== "complete" && !superseded.has(held.action_key),
  );
  const supersedes = action.kind === "complete" ? undefined : action.supersedes;
  const predecessor =
    supersedes === undefined ? undefined : active.find((held) => held.action_key === supersedes);
  if (supersedes !== undefined && (predecessor === undefined || predecessor.kind !== action.kind)) {
    throw new ActRefused(
      "a correction must supersede an active action of the same kind in this review",
    );
  }
  if (
    action.kind === "assessment" &&
    active.some((held) => held.kind === "assessment" && held !== predecessor)
  ) {
    throw new ActRefused("the review already has an active assessment; correct it with supersedes");
  }
  if (action.kind === "complete") {
    if (!active.some((held) => held.kind === "assessment")) {
      throw new ActRefused(
        "completion requires an active assessment or skip of the assigned record",
      );
    }
    const keys = new Set(action.actions);
    if (
      active.length === 0 ||
      keys.size !== action.actions.length ||
      keys.size !== active.length ||
      active.some((held) => !keys.has(held.action_key))
    ) {
      throw new ActRefused(
        "completion must name exactly every active accepted action key, once each",
      );
    }
  }

  const live = await store.db.query(`SELECT 1 AS live WHERE ${guard}`, guardParams);
  if (live.length !== 1)
    throw new ActRefused("the review claim is stale, expired, cancelled or settled");
  const previousReceipt =
    predecessor === undefined
      ? undefined
      : ReviewActionReceiptSchema.parse(JSON.parse(predecessor.receipt));
  const previousEffects =
    predecessor === undefined
      ? {}
      : z.record(z.string(), z.array(z.string())).parse(JSON.parse(predecessor.effects));
  const effects: Record<string, string[]> = {};
  const statements: SqlStatement[] = [];
  let assessmentId: string | undefined;
  let proposalId: string | undefined;
  if (action.kind !== "complete") {
    const authored = await store.db.query<{ id: string }>("SELECT id FROM records WHERE run_id=?", [
      run.id,
    ]);
    let result: ReviewResult;
    try {
      result = validateReviewAction(preparation, action, target, {
        target: authored.some((row) => row.id === preparation.revisionId),
        subjects: Object.fromEntries(authored.map((row) => [row.id, true as const])),
      });
    } catch (error) {
      if (error instanceof ResultRefusal) throw new ActRefused(refusalReason(error));
      throw error;
    }
    const rows: Record<string, readonly Row[]> = {
      ...reviewRows(preparation, result, run.id, at, {
        key: action.key,
        ...(previousReceipt?.assessmentId === undefined
          ? {}
          : { supersedesAssessmentId: previousReceipt.assessmentId }),
      }),
    };
    const previousFilingId = previousEffects["filings"]?.[0];
    if (previousFilingId !== undefined) {
      const filing = rows[JOB_OUTPUT_FILES.filings]?.[0];
      if (filing !== undefined) filing["supersedes_id"] = previousFilingId;
      else {
        const previous = await store.db.query<{ entity_id: string }>(
          "SELECT entity_id FROM filings WHERE id=?",
          [previousFilingId],
        );
        if (previous[0] === undefined) throw new Error("a review action lost its filing");
        // An assessment changing from a filing to a skip or proposal withdraws its own filing.
        // It cannot erase another author's filing or manufacture an operator decision.
        rows[JOB_OUTPUT_FILES.filings] = [
          {
            id: mintId("fil", run.id, `review-action|${action.key}|withdrawal`),
            record_id: preparation.recordId,
            entity_id: previous[0].entity_id,
            rationale: `Superseded by review action ${action.key}`,
            author_kind: "run",
            author_id: run.id,
            heuristic: 0,
            withdrawn: 1,
            supersedes_id: previousFilingId,
            created_at: at,
          },
        ];
      }
    }
    // Refinements are proposals, not extra assessments or votes.
    for (const [file, values] of Object.entries(rows)) {
      if (action.kind === "refinement" && file === JOB_OUTPUT_FILES.assessments) continue;
      const table = TABLES[file];
      if (table === undefined) throw new Error(`no review action table for ${file}`);
      for (const row of values) {
        const refused = refuseRow(table, row);
        if (refused !== null) throw new ActRefused(refused.message);
        (effects[table] ??= []).push(String(row["id"]));
        if (table === "assessments") assessmentId = String(row["id"]);
        if (table === "records") {
          proposalId = String(row["id"]);
          if (previousReceipt?.proposalId !== undefined) {
            const previous = await store.db.query<{ root_id: string; seq: number | bigint }>(
              "SELECT root_id,seq FROM records WHERE id=?",
              [previousReceipt.proposalId],
            );
            if (previous[0] === undefined)
              throw new Error("a review action receipt lost its proposal");
            row["root_id"] = previous[0].root_id;
            row["supersedes_id"] = previousReceipt.proposalId;
            row["seq"] = Number(previous[0].seq) + 1;
          }
        }
        const columns = Object.keys(row);
        statements.push({
          sql: `INSERT INTO ${table}(${columns.join(",")}) SELECT ${columns.map(() => "?").join(",")} WHERE ${guard}`,
          params: [...columns.map((column) => row[column] as SqlParam), ...guardParams],
        });
      }
    }
  }
  const receipt: ReviewActionReceipt = {
    key: action.key,
    kind: action.kind,
    runId: run.id,
    sequence: history.length + 1,
    recordedAt: at,
    completed: action.kind === "complete",
    ...(assessmentId === undefined ? {} : { assessmentId }),
    ...(proposalId === undefined ? {} : { proposalId }),
    ...(supersedes === undefined ? {} : { supersedes }),
  };
  const next: ReviewSubmission = {
    ...submission,
    state: "partial",
    complete: action.kind === "complete",
    actions: submission.actions + (action.kind === "complete" ? 0 : 1),
  };
  statements.push(
    {
      sql: `INSERT INTO review_actions(run_id,action_key,sequence,kind,supersedes_key,input,receipt,effects)
      SELECT ?,?,?,?,?,?,?,? WHERE ${guard}`,
      params: [
        run.id,
        action.key,
        receipt.sequence,
        action.kind,
        supersedes ?? null,
        document,
        JSON.stringify(receipt),
        JSON.stringify(effects),
        ...guardParams,
      ],
    },
    {
      sql: `UPDATE runs SET payload=?,records=(SELECT count(*) FROM records WHERE run_id=?)
      WHERE id=? AND ${guard} RETURNING id`,
      params: [
        JSON.stringify({ ...payload, reviewSubmission: next }),
        run.id,
        run.id,
        ...guardParams,
      ],
    },
  );
  const committed = await store.db.batch(statements);
  if (committed.at(-1)?.length !== 1) {
    const concurrent = await store.db.query<HeldAction>(
      "SELECT action_key,kind,supersedes_key,input,receipt,effects FROM review_actions WHERE run_id=? AND action_key=?",
      [run.id, action.key],
    );
    if (concurrent[0] !== undefined) return duplicate(concurrent[0], document);
    throw new ActRefused(
      "the review changed before this action committed; reread the refusal before retrying",
    );
  }
  store.touch();
  return receipt;
}

/**
 * Jev may consume one of the admitted attempt slots or recheck that reservation. This changes
 * no record, assessment, proposal or ruling. Attempts remain consumed on refusal or restart:
 * unknown provider completion is never an invitation to replay a call.
 */
export async function reviewJudgmentContext(
  store: ActsStore,
  actor: { runId: string; agentId: string },
  input: ReviewJudgmentContextInput,
) {
  const { run, payload, submission } = await heldReview(store, actor);
  const reservations = submission.judgments;
  if (reservations === undefined || submission.complete || submission.state === "completed")
    throw new ActRefused("this review has no active optional judgment permission");
  const { preparation, guard, guardParams } = reviewAuthority(run, payload, stamp(store.now()));
  const reservation = { key: input.key, stateDigest: input.stateDigest };
  const context = ReviewJudgmentContextSchema.parse({
    ...reservation,
    agentRunId: actor.runId,
    agentId: actor.agentId,
    runId: run.id,
    recordId: preparation.revisionId,
    kind: preparation.kind,
    role: preparation.role,
    stage: "review",
    assignmentId: preparation.assignmentId,
    fence: preparation.fence,
  });
  if (input.phase === "reserve") {
    if (
      reservations.length >= JEV_REVIEW_CALLS ||
      reservations.some((row) => row.key === input.key)
    )
      throw new ActRefused("optional judgment limit reached or this key was already attempted");
    const accepted = await store.db.query(
      `UPDATE runs SET payload=json_set(payload,'$.reviewSubmission.judgments',json(?))
        WHERE id=? AND ${guard} RETURNING id`,
      [JSON.stringify([...reservations, reservation]), run.id, ...guardParams],
    );
    if (accepted.length !== 1)
      throw new ActRefused("the review changed, stopped or lost its claim before judgment");
    store.touch();
  } else {
    if (!reservations.some((row) => row.key === input.key && row.stateDigest === input.stateDigest))
      throw new ActRefused("this judgment has no matching reservation");
    const live = await store.db.query(`SELECT 1 AS live WHERE ${guard}`, guardParams);
    if (live.length !== 1)
      throw new ActRefused("the review claim is stale, expired, cancelled or settled");
  }
  return context;
}
