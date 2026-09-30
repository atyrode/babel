import { createHash } from "node:crypto";
import {
  ACTIVITIES,
  ALLOCATION_ALGORITHM,
  ALLOCATION_LOOKBACK_MS,
  ALLOCATION_MAX_DAMPING,
  ALLOCATION_MAX_PLAN_BYTES,
  ALLOCATION_MAX_POLICY_BYTES,
  ALLOCATION_MAX_PROVENANCE,
  ALLOCATION_MAX_PROVENANCE_BYTES,
  ALLOCATION_MAX_SAVED_BYTES,
  ALLOCATION_MIN_SAMPLE,
  ALLOCATION_PRIOR_SAMPLE,
  AllocationPreviewSchema,
  AllocationVersionSchema,
  OPERATIONS,
  STAGES,
  type Activity,
  type AllocationPreview,
  type AllocationPreviewInput,
  type AllocationSnapshot,
  type AllocationVersion,
  type SaveAllocationInput,
} from "../contract.ts";
import { ActRefused, stamp, type ActsStore } from "./acts.ts";
import { DEFAULT_POLICY, PolicySchema } from "./coordinator.ts";

/*
 * No coordinator, engine or job handle enters this module. A plan is spend intent, not new
 * settings: even saving writes only allocation_plans, leaving policy identity and fencing alone.
 * All snapshot inputs are selected by ONE statement. The same watermark is a predicate on the
 * INSERT, so an intervening decision, inventory change, policy or other save cannot be lost.
 */
// UTC nanoseconds remain text-comparable. SQLite normalizes an offset's whole second, while
// its %f discards sub-millisecond precision: preserve the original fractional digits across
// the whole-minute timezone shift so a future ruling cannot enter at the inclusive upper edge.
// Invalid timestamps are counted as unobserved, never as rejected proposals.
const DECISION_FRACTION_SQL = `CASE
  WHEN instr(substr(d.recorded_at, 21), '+') > 0 THEN
    substr(substr(d.recorded_at, 21), 1, instr(substr(d.recorded_at, 21), '+') - 1)
  WHEN instr(substr(d.recorded_at, 21), '-') > 0 THEN
    substr(substr(d.recorded_at, 21), 1, instr(substr(d.recorded_at, 21), '-') - 1)
  ELSE rtrim(substr(d.recorded_at, 21), 'Zz') END`;
const DECISION_TIME_SQL = `CASE WHEN julianday(d.recorded_at) IS NULL THEN NULL
  WHEN substr(d.recorded_at, -1) = 'Z' AND substr(d.recorded_at, 20, 1) = '.'
    THEN substr(d.recorded_at, 1, 19) || '.' ||
      substr(substr(d.recorded_at, 21, length(d.recorded_at) - 21) || '000000000', 1, 9) || 'Z'
  ELSE strftime('%Y-%m-%dT%H:%M:%S', d.recorded_at) || '.' ||
    CASE WHEN substr(d.recorded_at, 20, 1) = '.'
      THEN substr((${DECISION_FRACTION_SQL}) || '000000000', 1, 9)
      ELSE '000000000' END || 'Z' END`;
const WATERMARK_SQL = `SELECT json_object(
  'policy', (SELECT coalesce(max(seq), 0) FROM policies),
  'plans', (SELECT coalesce(max(seq), 0) FROM allocation_plans),
  'records', (SELECT coalesce(max(rowid), 0) FROM records),
  'rulings', (SELECT coalesce(max(rowid), 0) FROM dispositions),
  'administration', (SELECT coalesce(max(rowid), 0) FROM plans),
  'assessments', (SELECT coalesce(max(rowid), 0) FROM assessments),
  'sessions', (SELECT count(*) FROM sessions),
  'runs', (SELECT coalesce(max(rowid), 0) FROM runs),
  'clock', (SELECT coalesce(max(revision), 0) FROM allocation_basis_clock))`;

/**
 * Only roots with a ruling in the window need their older decision streak consulted. SQL
 * reduces history before crossing the host boundary: complete counts plus bounded examples,
 * not a truncated sample used as a denominator. Repeating a pre-cutoff acceptance remains old.
 */
const FEEDBACK_SQL = `WITH
  instants AS (
    SELECT d.id, d.record_id, d.disposition, d.seq AS ruling_seq, d.recorded_at,
      r.root_id, r.seq AS revision_seq, ${DECISION_TIME_SQL} AS instant
    FROM dispositions d JOIN records r ON r.id = d.record_id WHERE r.kind = 'proposal'
  ),
  window_roots AS (
    SELECT DISTINCT root_id FROM instants WHERE instant >= ?1 AND instant <= ?2
  ),
  ordered AS (
    SELECT i.*,
      lag(disposition) OVER (PARTITION BY i.root_id ORDER BY instant, revision_seq, ruling_seq, id) AS previous,
      count(*) OVER (PARTITION BY i.root_id) - 1 AS superseded
    FROM instants i JOIN window_roots w ON w.root_id = i.root_id
    WHERE instant <= ?2
  ),
  changed AS (
    SELECT *, row_number() OVER (PARTITION BY root_id
      ORDER BY instant DESC, revision_seq DESC, ruling_seq DESC, id DESC) AS latest
    FROM ordered WHERE previous IS NULL OR previous <> disposition
  ),
  attributed AS (
    SELECT c.*, r.run_id, d.actor_id,
      CASE WHEN r.actor_kind = 'run' AND r.actor_id = r.run_id THEN
        CASE WHEN u.kind = '${OPERATIONS.evaluate}' AND json_valid(u.preparation)
            AND coalesce(json_extract(u.preparation, '$.review.role'), '') <> '' THEN 'review'
          WHEN u.kind = '${OPERATIONS.explore}' AND json_valid(u.preparation)
            AND json_extract(u.preparation, '$.analysis.stage') IN (${STAGES.map((stage) => `'${stage}'`).join(",")})
            THEN json_extract(u.preparation, '$.analysis.stage') END END AS activity,
      CASE WHEN EXISTS (SELECT 1 FROM records sibling JOIN plans p ON p.subject_id = sibling.id
          WHERE sibling.root_id = r.root_id)
        OR (json_valid(r.payload) AND (json_type(r.payload, '$.topic') IS NOT NULL
          OR json_type(r.payload, '$.backlog') IS NOT NULL)) THEN 1 ELSE 0 END AS administrative
    FROM changed c JOIN records r ON r.id = c.record_id
    JOIN dispositions d ON d.id = c.id
    LEFT JOIN runs u ON u.id = r.run_id WHERE latest = 1
  ),
  classified AS (
    SELECT *, CASE WHEN administrative THEN 'administrative'
      WHEN disposition NOT IN ('accept','reject') THEN 'not-decided'
      WHEN trim(actor_id) = '' THEN 'no-operator'
      WHEN instant < ?1 THEN 'outside-window'
      WHEN activity IS NULL THEN 'no-producing-activity' ELSE 'counted' END AS excluded
    FROM attributed
  ),
  examples AS (
    SELECT * FROM classified
    WHERE length(CAST(root_id AS BLOB)) + length(CAST(record_id AS BLOB)) +
      length(CAST(id AS BLOB)) + length(CAST(recorded_at AS BLOB)) +
      length(CAST(actor_id AS BLOB)) + length(CAST(coalesce(run_id, '') AS BLOB)) <= ${ALLOCATION_MAX_PROVENANCE_BYTES}
    ORDER BY root_id LIMIT ${ALLOCATION_MAX_PROVENANCE}
  )
  SELECT
    (SELECT json_group_array(json_object('activity',activity,'accepted',accepted,'rejected',rejected))
      FROM (SELECT activity, sum(disposition = 'accept') AS accepted, sum(disposition = 'reject') AS rejected
        FROM classified WHERE excluded = 'counted' GROUP BY activity)) AS outcomes,
    json_object(
      'complete', json('true'),
      'historicalRulings', (SELECT count(*) FROM instants WHERE instant < ?1),
      'windowRulings', (SELECT count(*) FROM instants WHERE instant >= ?1 AND instant <= ?2),
      'futureRulings', (SELECT count(*) FROM instants WHERE instant > ?2),
      'invalidTimestampRulings', (SELECT count(*) FROM instants WHERE instant IS NULL),
      'candidateRoots', (SELECT count(*) FROM classified),
      'supersededRulings', (SELECT coalesce(sum(superseded), 0) FROM classified),
      'exclusions', json((SELECT json_group_array(json_object('reason',excluded,'roots',n))
        FROM (SELECT excluded, count(*) AS n FROM classified GROUP BY excluded ORDER BY excluded))),
      'provenanceLimit', ${ALLOCATION_MAX_PROVENANCE},
      'provenanceReturned', (SELECT count(*) FROM examples),
      'provenanceOmitted', (SELECT count(*) FROM classified) - (SELECT count(*) FROM examples)
    ) AS coverage,
    (SELECT json_group_array(json_object(
      'rootId', root_id, 'recordId', record_id, 'rulingId', id,
      'decision', disposition, 'at', recorded_at, 'operatorId', actor_id,
      'runId', run_id, 'activity', activity,
      'attribution', CASE WHEN activity IS NULL THEN NULL ELSE 'records.run_id + runs.kind/preparation' END,
      'excluded', excluded, 'supersededDecisions', superseded)) FROM examples) AS feedback`;

async function capture(store: ActsStore, at: number): Promise<AllocationSnapshot> {
  const rows = await store.db.query<{
    policy_version: string;
    policy_seq: number | bigint;
    policy_payload: string | null;
    policy_bytes: number | bigint;
    outcomes: string;
    coverage: string;
    watermark: string;
    feedback: string;
    inventories: string;
  }>(
    `WITH feedback_basis AS (${FEEDBACK_SQL}) SELECT
    coalesce((SELECT CASE WHEN length(CAST(version AS BLOB)) <= ${ALLOCATION_MAX_POLICY_BYTES}
      THEN version END FROM policies ORDER BY seq DESC LIMIT 1), '') AS policy_version,
    coalesce((SELECT seq FROM policies ORDER BY seq DESC LIMIT 1), 0) AS policy_seq,
    coalesce((SELECT length(CAST(payload AS BLOB)) + length(CAST(version AS BLOB))
      FROM policies ORDER BY seq DESC LIMIT 1), 0) AS policy_bytes,
    (SELECT CASE WHEN length(CAST(payload AS BLOB)) + length(CAST(version AS BLOB)) <= ${ALLOCATION_MAX_POLICY_BYTES}
      THEN payload END FROM policies ORDER BY seq DESC LIMIT 1) AS policy_payload,
    (${WATERMARK_SQL}) AS watermark,
    (SELECT outcomes FROM feedback_basis) AS outcomes,
    (SELECT coverage FROM feedback_basis) AS coverage,
    (SELECT feedback FROM feedback_basis) AS feedback,
    (SELECT json_group_array(json_object('key', key, 'count', n, 'source', source, 'meaning', meaning))
      FROM (
        SELECT kind AS key, count(*) AS n, 'records: latest logical root' AS source,
          'Recorded inventory only; not eligible work or a reward signal' AS meaning
          FROM records r WHERE NOT EXISTS
            (SELECT 1 FROM records newer WHERE newer.root_id = r.root_id AND newer.seq > r.seq)
          GROUP BY kind
        UNION ALL SELECT 'unreviewed-records', count(*), 'records minus assessments at this revision',
          'Counted reception gap; eligibility and permission are not inferred'
          FROM records r WHERE NOT EXISTS
            (SELECT 1 FROM records newer WHERE newer.root_id = r.root_id AND newer.seq > r.seq)
          AND NOT EXISTS (SELECT 1 FROM assessments a WHERE a.revision_id = r.id)
        UNION ALL SELECT 'catalogued-sessions', count(*), 'sessions',
          'Hub catalogue rows, not the size or completeness of the unseen archive' FROM sessions
      )) AS inventories`,
    [stamp(at - ALLOCATION_LOOKBACK_MS), stamp(at)],
  );
  const row = rows[0];
  if (!row) throw new Error("allocation snapshot returned no row");
  if (Number(row.policy_bytes) > ALLOCATION_MAX_POLICY_BYTES) {
    throw new ActRefused("the current policy exceeds the bounded allocation snapshot size");
  }
  const rawPolicy: unknown =
    row.policy_payload === null ? DEFAULT_POLICY : JSON.parse(row.policy_payload);
  const parsed = PolicySchema.safeParse(rawPolicy);
  if (!parsed.success)
    throw new ActRefused("the current policy is unreadable; allocation cannot invent a baseline");
  const inventories = JSON.parse(row.inventories) as AllocationSnapshot["inventories"];
  for (const kind of ["hypothesis", "observation", "finding", "proposal"]) {
    if (!inventories.some((inventory) => inventory.key === kind)) {
      inventories.push({
        key: kind,
        count: 0,
        source: "records: latest logical root",
        meaning: "No recorded roots of this kind; not a claim about unseen material",
      });
    }
  }
  inventories.push({
    key: "eligible-work",
    count: null,
    source: "not evaluated by arithmetic preview",
    meaning: "Unknown: no scheduler draw, archive probe or model admission is performed",
  });
  inventories.sort((left, right) => left.key.localeCompare(right.key));
  const outcomes = JSON.parse(row.outcomes) as AllocationSnapshot["outcomes"];
  return {
    at: new Date(at).toISOString(),
    cutoff: new Date(at - ALLOCATION_LOOKBACK_MS).toISOString(),
    lookbackMs: ALLOCATION_LOOKBACK_MS,
    policyVersion: row.policy_version,
    policySeq: Number(row.policy_seq),
    policy: rawPolicy as Record<string, unknown>,
    watermark: row.watermark,
    outcomes: ACTIVITIES.map(
      (activity) =>
        outcomes.find((outcome) => outcome.activity === activity) ?? {
          activity,
          accepted: 0,
          rejected: 0,
        },
    ),
    coverage: JSON.parse(row.coverage) as AllocationSnapshot["coverage"],
    feedback: JSON.parse(row.feedback) as AllocationSnapshot["feedback"],
    inventories,
  };
}

/** Pure replay: inventories are displayed, never scored. Sparse activities retain their baseline. */
export function replayAllocation(
  snapshot: AllocationSnapshot,
  edits: AllocationPreviewInput["edits"] = [],
): AllocationPreview {
  const policy = PolicySchema.parse(snapshot.policy);
  const totalWeight = policy.enabled
    ? ACTIVITIES.reduce((sum, activity) => sum + policy.activityWeights[activity], 0)
    : 0;
  const slices: AllocationPreview["slices"] = ACTIVITIES.map((activity) => {
    const baseline = totalWeight === 0 ? 0 : policy.activityWeights[activity] / totalWeight;
    const { accepted, rejected } = snapshot.outcomes.find(
      (outcome) => outcome.activity === activity,
    )!;
    const sample = accepted + rejected;
    // Protect explore's full configured share, and review's internal coverage/discovery/random
    // exploration reservations in absolute budget terms. A disabled duty has no protected floor.
    const floor =
      activity === "explore"
        ? baseline
        : activity === "review"
          ? baseline *
            Math.min(
              1,
              Math.max(0, policy.coverageShare + policy.explorationShare + policy.discoveryShare),
            )
          : 0;
    return {
      activity,
      enabled: baseline > 0,
      baseline,
      protected: floor,
      proposed: baseline,
      displacement: 0,
      accepted,
      rejected,
      sample,
      rate: sample === 0 ? null : accepted / sample,
      damping: 0,
      evidence:
        baseline === 0
          ? "disabled"
          : sample === 0
            ? "missing"
            : sample < ALLOCATION_MIN_SAMPLE
              ? "sparse"
              : "sufficient",
    };
  });
  const adjustable = slices.filter(
    (slice) => slice.evidence === "sufficient" && slice.baseline > slice.protected,
  );
  if (adjustable.length >= 2) {
    const minimumSample = Math.min(...adjustable.map((slice) => slice.sample));
    const damping =
      (ALLOCATION_MAX_DAMPING * minimumSample) / (minimumSample + ALLOCATION_PRIOR_SAMPLE);
    const mass = adjustable.reduce((sum, slice) => sum + slice.baseline - slice.protected, 0);
    const score = adjustable.reduce(
      (sum, slice) => sum + (slice.baseline - slice.protected) * (0.5 + slice.rate!),
      0,
    );
    for (const slice of adjustable) {
      const free = slice.baseline - slice.protected;
      slice.proposed =
        slice.protected +
        free * (1 - damping) +
        (damping * mass * free * (0.5 + slice.rate!)) / score;
      slice.damping = damping;
    }
  }
  const pinned = new Set<Activity>();
  for (const edit of edits) {
    const slice = slices.find((candidate) => candidate.activity === edit.activity)!;
    if (pinned.has(edit.activity)) throw new ActRefused("an allocation slice is edited twice");
    if (!slice.enabled && edit.fraction !== 0)
      throw new ActRefused(`${edit.activity} is disabled by the configured policy`);
    if (
      !Number.isFinite(edit.fraction) ||
      edit.fraction < slice.protected - 1e-12 ||
      edit.fraction > 1
    ) {
      throw new ActRefused(
        `${edit.activity} must retain its protected fraction ${String(slice.protected)}`,
      );
    }
    slice.proposed = Math.max(slice.protected, edit.fraction);
    pinned.add(edit.activity);
  }
  if (edits.length > 0) {
    const available = totalWeight > 0 ? 1 : 0;
    const remaining = slices.filter((slice) => !pinned.has(slice.activity) && slice.enabled);
    const required =
      slices
        .filter((slice) => pinned.has(slice.activity))
        .reduce((sum, slice) => sum + slice.proposed, 0) +
      remaining.reduce((sum, slice) => sum + slice.protected, 0);
    const free = available - required;
    if (free < -1e-12 || (remaining.length === 0 && Math.abs(free) > 1e-12)) {
      throw new ActRefused(
        "edited fractions leave no permitted remainder; retain disabled and protected shares",
      );
    }
    const weighted = remaining.reduce((sum, slice) => sum + slice.proposed - slice.protected, 0);
    const fallback = remaining.reduce((sum, slice) => sum + slice.baseline, 0);
    for (const slice of remaining) {
      slice.proposed =
        slice.protected +
        Math.max(0, free) *
          (weighted > 0
            ? (slice.proposed - slice.protected) / weighted
            : slice.baseline / fallback);
    }
  }
  for (const slice of slices) {
    slice.proposed = Math.min(1, Math.max(slice.protected, slice.proposed));
    slice.displacement = slice.proposed - slice.baseline;
  }
  const digest = createHash("sha256").update(JSON.stringify(snapshot)).digest("hex");
  return AllocationPreviewSchema.parse({
    algorithm: ALLOCATION_ALGORITHM,
    basis: { at: snapshot.at, digest },
    snapshot,
    edits,
    minimumSample: ALLOCATION_MIN_SAMPLE,
    priorSample: ALLOCATION_PRIOR_SAMPLE,
    maximumDamping: ALLOCATION_MAX_DAMPING,
    discretionaryFraction:
      totalWeight === 0
        ? 0
        : Math.max(0, 1 - slices.reduce((sum, slice) => sum + slice.protected, 0)),
    unallocatedFraction: totalWeight === 0 ? 1 : 0,
    slices,
    caveats: [
      "Fractions express proposed spend, not invocation counts or a spending guarantee. No cost-per-activity basis is available.",
      "The scheduler currently draws by relative activity weights (and mapping slot weights), not these spend fractions; saved plans are not consumed by it.",
      "Eight attributable decided roots per activity and two adjustable activities are required. Damping is 0.25 × minimum sample / (minimum sample + 32); sparse and missing evidence retain baseline.",
      "The inclusive seven-day cutoff is an observation age limit, not a cadence, waiting period or budget duration. Acceptance records a choice, not verified success.",
      "Explore's baseline share and review's configured coverage, exploration and discovery reservations are protected. Zero weights stay disabled; a disabled or all-zero policy authorizes no allocation.",
      "Preview and save start no work and change no policy, spending ceiling, cap, focus, disclosure or settings authority. Recorded inventories do not earn allocation.",
      `Complete seven-day outcome counts drive arithmetic. At most ${ALLOCATION_MAX_PROVENANCE} provenance examples are retained; summarized coverage reports omitted examples and excluded history, never sampled denominators.`,
    ],
  });
}

export async function previewAllocation(
  store: ActsStore,
  input: AllocationPreviewInput,
): Promise<AllocationPreview> {
  const at = input.basis ? Date.parse(input.basis.at) : store.now();
  if (!Number.isFinite(at) || at > store.now())
    throw new ActRefused("allocation preview time is invalid or in the future");
  const plan = replayAllocation(await capture(store, at), input.edits);
  if (input.basis && input.basis.digest !== plan.basis.digest) {
    throw new ActRefused(
      "allocation basis is stale; compute a fresh preview before editing or saving",
    );
  }
  if (Buffer.byteLength(JSON.stringify(plan)) > ALLOCATION_MAX_PLAN_BYTES) {
    throw new ActRefused("the allocation plan exceeds its bounded snapshot size");
  }
  return plan;
}

export async function saveAllocation(
  store: ActsStore,
  input: SaveAllocationInput,
  operator: string,
): Promise<AllocationVersion> {
  if (!operator.trim()) throw new ActRefused("an allocation version needs its owner");
  const plan = await previewAllocation(store, input);
  const at = stamp(store.now());
  const payload = JSON.stringify(plan);
  const savedBytes =
    Buffer.byteLength(payload) +
    Buffer.byteLength(input.name) +
    Buffer.byteLength(operator) +
    Buffer.byteLength(input.reason) +
    256;
  const rows = await store.db.query<{ seq: number | bigint }>(
    `INSERT INTO allocation_plans(version, seq, actor_id, reason, policy_version, payload, recorded_at)
     SELECT ?, coalesce((SELECT max(seq) FROM allocation_plans), 0) + 1, ?, ?, ?, ?, ?
     WHERE (${WATERMARK_SQL}) = ?
       AND coalesce((SELECT version FROM policies ORDER BY seq DESC LIMIT 1), '') = ?
       AND coalesce((SELECT sum(length(CAST(payload AS BLOB)) + length(CAST(version AS BLOB)) +
         length(CAST(actor_id AS BLOB)) + length(CAST(reason AS BLOB)) + 256)
         FROM allocation_plans), 0) + ? <= ${ALLOCATION_MAX_SAVED_BYTES}
     ON CONFLICT(version) DO NOTHING RETURNING seq`,
    [
      input.name,
      operator,
      input.reason,
      plan.snapshot.policyVersion,
      payload,
      at,
      plan.snapshot.watermark,
      plan.snapshot.policyVersion,
      savedBytes,
    ],
  );
  if (!rows[0])
    throw new ActRefused(
      "allocation version already exists, its basis is stale, or the bounded saved-plan capacity is full; no version was written",
    );
  return {
    version: input.name,
    seq: Number(rows[0].seq),
    actorId: operator,
    reason: input.reason,
    recordedAt: at,
    plan,
  };
}

export async function allocationVersion(
  store: ActsStore,
  version?: string,
): Promise<AllocationVersion | null> {
  const rows = await store.db.query<{
    version: string;
    seq: number | bigint;
    actor_id: string;
    reason: string;
    recorded_at: string;
    payload: string;
  }>(
    `SELECT version, seq, actor_id, reason, recorded_at, payload FROM allocation_plans
     ${version === undefined ? "" : "WHERE version = ?"} ORDER BY seq DESC LIMIT 1`,
    version === undefined ? [] : [version],
  );
  const row = rows[0];
  return row
    ? AllocationVersionSchema.parse({
        version: row.version,
        seq: Number(row.seq),
        actorId: row.actor_id,
        reason: row.reason,
        recordedAt: row.recorded_at,
        plan: JSON.parse(row.payload),
      })
    : null;
}
