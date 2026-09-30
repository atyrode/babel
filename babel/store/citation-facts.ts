import { createHash } from "node:crypto";
import type { PluginDatabase, SqlParam, SqlRow } from "@manifold/plugin";
import {
  CitationFactTaskSchema,
  CitationFactSourceSchema,
  CitationFactResultSchema,
  MAX_CITATION_QUOTE,
  OPERATIONS,
  PreflightModeSchema,
  type CitationOutcome,
  type CitationField,
  type CitationUnavailable,
  type CitationFactTask,
  type CitationFactResult,
  type CitationFactSource,
} from "../contract.ts";
import { claim } from "../machine/adapters/index.ts";

export const CITATION_FACT_PAGE_SIZE = 25;
export const CITATION_FACT_MAX_PAGE_SIZE = 100;
export interface CitationFact {
  readonly seq: number;
  readonly attemptId: string;
  readonly createdAt: string;
  readonly task: CitationFactTask;
  readonly result: CitationFactResult;
}
export interface CitationFactPlan {
  readonly total: number;
  readonly completed: number;
  readonly pending: number;
  /** Pending positions with sufficient historical identity to attempt an archive read. */
  readonly checkable: number;
  readonly unknown: number;
  readonly limit: number;
  readonly tasks: readonly CitationFactTask[];
}
export interface CitationFactPlanOptions {
  readonly limit?: number;
  readonly recordId?: string;
  /** Explicit retry; earlier unavailable attempts remain in the ledger. */
  readonly retryUnavailable?: boolean;
}
export interface CitationFactReport {
  readonly total: number;
  readonly completed: number;
  readonly pending: number;
  readonly available: number;
  readonly unavailable: number;
  readonly quoteOutcomes: Readonly<Record<CitationOutcome, number>>;
  readonly unavailableReasons: Readonly<Record<string, number>>;
}
export class CitationFactError extends Error {
  constructor(readonly reason: "task-mismatch" | "fact-conflict" | "invalid-fact") {
    super(`citation facts: ${reason}`);
  }
}

type ObjectValue = Record<string, unknown>;
const object = (value: unknown): ObjectValue =>
  typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as ObjectValue)
    : {};
const text = (value: unknown): string => (typeof value === "string" ? value : "");
const parse = (value: unknown): ObjectValue => {
  try {
    return object(JSON.parse(text(value)));
  } catch {
    return {};
  }
};
const array = (value: unknown): readonly unknown[] => (Array.isArray(value) ? value : []);
const hash = (value: string): string =>
  `sha256:${createHash("sha256").update(value).digest("hex")}`;
const canonical = (value: unknown): string => {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (typeof value === "object" && value !== null) {
    const held = value as ObjectValue;
    return `{${Object.keys(held)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonical(held[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value) ?? "null";
};
const digest = (value: unknown): string => {
  const held = text(value);
  return /^(?:sha256:)?[0-9a-f]{64}$/.test(held) ? `sha256:${held.replace(/^sha256:/, "")}` : "";
};
const nullable = (value: unknown): string | null => text(value) || null;
const number = (value: unknown, minimum: number): number | undefined =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= minimum ? value : undefined;

// The producing run's completed preparation receipts, in stable order. No sessions catalog join:
// an imported session's catalog digest can belong to a different historical revision.
const MATERIALS_SQL = `(SELECT json_group_array(json_object('id',p.id,'payload',p.payload))
  FROM (SELECT p.id,p.payload FROM runs p WHERE p.job_id=producer.prepare_job_id
    AND p.kind='${OPERATIONS.prepare}' AND p.closure='completed' ORDER BY p.id) p)`;
const POSITIONS_SQL = `WITH positions AS (
  SELECT r.id AS record_id,producer.id AS producer_id,
    fields.key AS field,CAST(citation.key AS INTEGER) AS ordinal,
    CASE WHEN citation.type='object' THEN citation.value ELSE '{}' END AS citation
  FROM records r LEFT JOIN runs producer ON producer.id=r.run_id
  JOIN json_each(CASE WHEN json_valid(r.payload) THEN r.payload ELSE '{}' END) fields
  JOIN json_each(CASE WHEN fields.type='array' THEN fields.value ELSE '[]' END) citation
  WHERE fields.type='array' AND (
    (r.kind='observation' AND fields.key IN ('evidence','counter_evidence')) OR
    (r.kind='finding' AND fields.key='counter_evidence') OR
    (r.kind='proposal' AND fields.key IN ('supporting','conflicting')))
)`;
type PositionRow = SqlRow & {
  record_id: string;
  field: CitationField;
  ordinal: number | bigint;
  citation: string;
  preparation: string | null;
  materials: string;
  producer_id: string | null;
  latest_status: string | null;
  latest_seq: number | bigint | null;
  latest_citation_digest: string | null;
  latest_basis_digest: string | null;
  latest_outcome: CitationOutcome | null;
  latest_reason: string | null;
};
type IndexedPositionRow = SqlRow &
  Pick<
    PositionRow,
    | "record_id"
    | "field"
    | "ordinal"
    | "citation"
    | "producer_id"
    | "latest_status"
    | "latest_seq"
    | "latest_citation_digest"
    | "latest_basis_digest"
    | "latest_outcome"
    | "latest_reason"
  >;
type SourceMetadata = Pick<PositionRow, "preparation" | "materials">;
/** Fetch the producing run's retained provenance once, not once for every citation position. */
async function sourceMetadata(
  db: PluginDatabase,
  producerId: string | null,
): Promise<SourceMetadata> {
  if (producerId === null) return { preparation: null, materials: "[]" };
  const rows = await db.query<SqlRow & SourceMetadata>(
    `SELECT producer.preparation,${MATERIALS_SQL} AS materials
     FROM runs producer WHERE producer.id=?`,
    [producerId],
  );
  return {
    preparation: rows[0]?.preparation ?? null,
    materials: rows[0]?.materials ?? "[]",
  };
}
interface Candidate {
  readonly entry: ObjectValue;
  readonly mode: unknown;
  readonly detectors: unknown;
  readonly raw: boolean;
}
function candidates(row: PositionRow): readonly Candidate[] {
  const preparation = parse(row.preparation);
  const out: Candidate[] = array(preparation["selection"]).map((value) => {
    const entry = object(value);
    return {
      entry,
      mode: preparation["sourceMode"],
      detectors: preparation["sourceDetectors"],
      raw: "source_id" in entry || "source_digest" in entry,
    };
  });
  let receipts: readonly unknown[] = [];
  try {
    receipts = array(JSON.parse(row.materials));
  } catch {
    /* Invalid metadata stays unknown. */
  }
  for (const receipt of receipts) {
    const payload = parse(object(receipt)["payload"]);
    const report = object(payload["preflight"]);
    for (const value of array(object(payload["material"])["sessions"])) {
      out.push({
        entry: object(value),
        mode: report["mode"],
        detectors: report["detectors"],
        raw: false,
      });
    }
  }
  return out;
}
function matches(entry: ObjectValue, path: string): boolean {
  const file = text(entry["file"]);
  if (
    file !== "" &&
    (path === file || path === `sessions/${file}` || path.endsWith(`/sessions/${file}`))
  )
    return true;
  const id = text(entry["sourceId"] ?? entry["source_id"]);
  const harness = text(entry["harness"]);
  const selector = text(entry["selector"]) || `${harness}/${id}`;
  // Explicitly suppress live existence checks: this is identity from a historical path only.
  const claimed = claim(path, () => false);
  if (claimed !== null) return claimed.harness === harness && claimed.sourceId === id;
  const stem = path.replace(/\.(?:jsonl|ndjson|json)$/u, "");
  return (
    id !== "" &&
    (stem === selector || stem.endsWith(`/${selector}`) || stem === id || stem.endsWith(`/${id}`))
  );
}
function resolved(candidate: Candidate): CitationFactSource | null {
  const e = candidate.entry;
  const origin = object(e["origin"]);
  const host = text(e["host"]) || text(origin["label"]);
  const harness = text(e["harness"]);
  const id = text(e["sourceId"] ?? e["source_id"]);
  const captureDigest = digest(e["captureDigest"] ?? e["capture_digest"]);
  const sourceDigest = digest(e["sourceDigest"] ?? e["source_digest"]);
  const snapshotId = nullable(
    origin["snapshotId"] ?? e["snapshotId"] ?? e["snapshot_id"] ?? e["snapshot"],
  );
  const path = nullable(origin["path"] ?? e["archivePath"] ?? e["archive_path"]);
  const mode = PreflightModeSchema.safeParse(candidate.mode);
  const parsed = CitationFactSourceSchema.safeParse({
    host,
    harness,
    sourceId: id,
    selector: text(e["selector"]) || `${harness}/${id}`,
    captureDigest,
    sourceDigest,
    snapshotId,
    path,
    label: nullable(origin["label"] ?? e["label"]) ?? host,
    sourceMode: mode.success ? mode.data : null,
    sourceDetectors: nullable(candidate.detectors),
  });
  return parsed.success ? parsed.data : null;
}
function taskOf(row: PositionRow): CitationFactTask {
  const citation = parse(row.citation);
  const locator = object(citation["locator"]);
  const path = text(locator["path"]);
  const line = number(locator["line"], 1);
  const byteOffset = number(locator["byte_offset"] ?? locator["byteOffset"], 0);
  const all = candidates(row);
  const matching = all.filter((candidate) => matches(candidate.entry, path));
  const sources = matching.map(resolved);
  const first = sources[0] ?? null;
  const ambiguous =
    matching.length > 1 &&
    matching.some(
      (candidate, index) =>
        canonical(sources[index]) !== canonical(first) || candidate.raw !== matching[0]?.raw,
    );
  const raw = matching[0]?.raw === true;
  const invalidLocator =
    path === "" ||
    (line === undefined && byteOffset === undefined) ||
    (locator["line"] !== undefined && line === undefined) ||
    ((locator["byte_offset"] !== undefined || locator["byteOffset"] !== undefined) &&
      byteOffset === undefined) ||
    (raw && !/^[0-9a-f]{64}$/.test(text(locator["digest"]))) ||
    text(locator["quote"]).length > MAX_CITATION_QUOTE ||
    (locator["quote"] !== undefined &&
      locator["quote"] !== null &&
      typeof locator["quote"] !== "string");
  const unavailable: CitationUnavailable | null = invalidLocator
    ? "invalid-locator"
    : all.length === 0
      ? "missing-preparation"
      : matching.length === 0
        ? "missing-source"
        : ambiguous
          ? "ambiguous-source"
          : first === null
            ? "invalid-source"
            : null;
  return {
    recordId: row.record_id,
    field: row.field,
    ordinal: Number(row.ordinal),
    citationDigest: hash(canonical(citation)),
    basisDigest: hash(canonical([row.preparation, row.materials])),
    path,
    quote:
      typeof locator["quote"] === "string" && locator["quote"] !== "" ? locator["quote"] : null,
    locator: {
      coordinates: raw ? "raw" : "normalized",
      ...(line === undefined ? {} : { line }),
      ...(byteOffset === undefined ? {} : { byteOffset }),
      ...(typeof locator["digest"] === "string" ? { digest: locator["digest"] } : {}),
      ...(typeof locator["recordDigest"] === "string"
        ? { recordDigest: locator["recordDigest"] }
        : {}),
    },
    source: unavailable === null ? first : null,
    unavailable,
  };
}

/** Shared bounded scan, including only metadata from the latest fact, never its excerpt. */
async function* positions(db: PluginDatabase, recordId?: string): AsyncGenerator<PositionRow> {
  let after: readonly SqlParam[] = ["", "", -1];
  for (;;) {
    const rows = await db.query<IndexedPositionRow>(
      `${POSITIONS_SQL}
      SELECT p.*,f.seq AS latest_seq,f.status AS latest_status,f.citation_digest AS latest_citation_digest,
        json_extract(f.task,'$.basisDigest') AS latest_basis_digest,
        f.quote_outcome AS latest_outcome,json_extract(f.result,'$.reason') AS latest_reason
      FROM positions p LEFT JOIN citation_facts f ON f.seq=(
        SELECT max(previous.seq) FROM citation_facts previous WHERE previous.record_id=p.record_id
          AND previous.field=p.field AND previous.ordinal=p.ordinal)
      WHERE (? IS NULL OR p.record_id=?) AND (p.record_id,p.field,p.ordinal) > (?,?,?)
      ORDER BY p.record_id,p.field,p.ordinal LIMIT 32`,
      [recordId ?? null, recordId ?? null, ...after],
    );
    const metadata = new Map<string, SourceMetadata>();
    for (const row of rows) {
      let source = row.producer_id === null ? undefined : metadata.get(row.producer_id);
      if (source === undefined) {
        source = await sourceMetadata(db, row.producer_id);
        if (row.producer_id !== null) {
          metadata.set(row.producer_id, source);
          if (metadata.size > 4) metadata.delete(metadata.keys().next().value!);
        }
      }
      yield { ...row, ...source };
    }
    const last = rows.at(-1);
    if (last === undefined || rows.length < 32) return;
    after = [last.record_id, last.field, last.ordinal];
  }
}

/** Counts all actual positions, retaining only a bounded page of genuinely missing facts. */
export async function planCitationFacts(
  db: PluginDatabase,
  options: CitationFactPlanOptions = {},
): Promise<CitationFactPlan> {
  const requested = options.limit ?? CITATION_FACT_PAGE_SIZE;
  if (!Number.isSafeInteger(requested) || requested < 1)
    throw new CitationFactError("invalid-fact");
  const limit = Math.min(requested, CITATION_FACT_MAX_PAGE_SIZE);
  let total = 0;
  let pending = 0;
  let checkable = 0;
  const selected: { task: CitationFactTask; priority: number }[] = [];
  for await (const row of positions(db, options.recordId)) {
    total++;
    const task = taskOf(row);
    const current =
      row.latest_citation_digest === task.citationDigest &&
      row.latest_basis_digest === task.basisDigest;
    if (
      current &&
      row.latest_status !== null &&
      !(options.retryUnavailable && row.latest_status === "unavailable")
    )
      continue;
    pending++;
    if (task.unavailable === null) checkable++;
    const priority = current && options.retryUnavailable ? Number(row.latest_seq ?? 0) : 0;
    if (selected.length < limit || priority < selected[selected.length - 1]!.priority) {
      selected.push({ task, priority });
      if (options.retryUnavailable) selected.sort((a, b) => a.priority - b.priority);
      if (selected.length > limit) selected.pop();
    }
  }
  return {
    total,
    completed: total - pending,
    pending,
    checkable,
    unknown: pending - checkable,
    limit,
    tasks: selected.map(({ task }) => task),
  };
}

/** Publicly safe aggregate coverage: no source locator, submitted text or newly retrieved text. */
export async function citationFactReport(
  db: PluginDatabase,
  options: Pick<CitationFactPlanOptions, "recordId"> = {},
): Promise<CitationFactReport> {
  let total = 0;
  let available = 0;
  let unavailable = 0;
  const quoteOutcomes: Record<CitationOutcome, number> = {
    verified: 0,
    moved: 0,
    absent: 0,
    unquoted: 0,
    unchecked: 0,
  };
  const unavailableReasons: Record<string, number> = Object.create(null) as Record<string, number>;
  for await (const row of positions(db, options.recordId)) {
    total++;
    const task = taskOf(row);
    const current =
      row.latest_citation_digest === task.citationDigest &&
      row.latest_basis_digest === task.basisDigest;
    let outcome: CitationOutcome =
      task.quote === null || task.quote.trim() === "" ? "unquoted" : "unchecked";
    if (current && row.latest_status !== null) {
      outcome = row.latest_outcome ?? outcome;
      if (row.latest_status === "available") available++;
      else {
        unavailable++;
        const reason = row.latest_reason ?? "unavailable";
        unavailableReasons[reason] =
          (Object.hasOwn(unavailableReasons, reason) ? unavailableReasons[reason]! : 0) + 1;
      }
    }
    quoteOutcomes[outcome]++;
  }
  const completed = available + unavailable;
  return {
    total,
    completed,
    pending: total - completed,
    available,
    unavailable,
    quoteOutcomes,
    unavailableReasons,
  };
}

function validateResult(task: CitationFactTask, result: CitationFactResult): void {
  const unquoted = task.quote === null || task.quote.trim() === "";
  if (
    (unquoted && result.check.outcome !== "unquoted") ||
    (!unquoted && result.check.outcome === "unquoted")
  )
    throw new CitationFactError("invalid-fact");
  if (result.status === "unavailable") {
    if (
      !result.reason ||
      result.excerpt !== null ||
      result.position !== null ||
      (!unquoted && result.check.outcome !== "unchecked")
    )
      throw new CitationFactError("invalid-fact");
    return;
  }
  const source = result.source;
  const expected = task.source;
  const position = result.position;
  const excerpt = result.excerpt;
  const measured = result.measured;
  if (
    task.unavailable !== null ||
    expected === null ||
    source === null ||
    position === null ||
    excerpt === null ||
    measured === null ||
    result.reason !== null
  )
    throw new CitationFactError("invalid-fact");
  if (
    source.host !== expected.host ||
    source.harness !== expected.harness ||
    source.selector !== expected.selector ||
    source.captureDigest !== expected.captureDigest ||
    source.sourceDigest !== expected.sourceDigest ||
    (expected.snapshotId !== null && !source.snapshotId.startsWith(expected.snapshotId)) ||
    (expected.path !== null && source.path !== expected.path) ||
    (expected.label !== null && source.label !== expected.label) ||
    source.sourceMode !== (expected.sourceMode ?? "off") ||
    ((expected.sourceMode === "redact" || expected.sourceMode === "refuse") &&
      expected.sourceDetectors !== null &&
      source.sourceDetectors !== expected.sourceDetectors) ||
    measured.captureDigest !== expected.captureDigest ||
    (task.locator.coordinates === "normalized" &&
      measured.sourceDigest !== expected.sourceDigest) ||
    position.line > measured.records ||
    position.byteOffset + position.byteLength > Number.MAX_SAFE_INTEGER ||
    excerpt.bytes !== Buffer.byteLength(excerpt.text)
  )
    throw new CitationFactError("invalid-fact");
  const locator = task.locator;
  if (locator.coordinates === "raw") {
    const raw = position.raw;
    if (
      raw === undefined ||
      locator.digest === undefined ||
      raw.digest !== locator.digest ||
      (locator.line !== undefined && raw.line !== locator.line) ||
      (locator.byteOffset !== undefined && raw.byteOffset !== locator.byteOffset)
    ) {
      throw new CitationFactError("invalid-fact");
    }
  } else if (
    (locator.line !== undefined && position.line !== locator.line) ||
    (locator.byteOffset !== undefined &&
      locator.byteOffset !== 0 &&
      position.byteOffset !== locator.byteOffset) ||
    (locator.digest !== undefined && digest(locator.digest) !== source.sourceDigest)
  ) {
    throw new CitationFactError("invalid-fact");
  }
  if (locator.recordDigest !== undefined && locator.recordDigest !== position.digest) {
    throw new CitationFactError("invalid-fact");
  }
}
type FactRow = SqlRow & {
  seq: number | bigint;
  attempt_id: string;
  created_at: string;
  task: string;
  result: string;
};
const factOf = (row: FactRow): CitationFact => ({
  seq: Number(row.seq),
  attemptId: row.attempt_id,
  createdAt: row.created_at,
  task: JSON.parse(row.task) as CitationFactTask,
  result: JSON.parse(row.result) as CitationFactResult,
});

/** Owner-gated callers page private excerpts; neither the query nor the result is unbounded. */
export async function readCitationFacts(
  db: PluginDatabase,
  recordId: string,
  options: { readonly after?: number; readonly limit?: number } = {},
): Promise<{ readonly facts: readonly CitationFact[]; readonly nextAfter: number | null }> {
  const after = options.after ?? 0;
  const requested = options.limit ?? CITATION_FACT_PAGE_SIZE;
  if (
    !Number.isSafeInteger(after) ||
    after < 0 ||
    !Number.isSafeInteger(requested) ||
    requested < 1
  ) {
    throw new CitationFactError("invalid-fact");
  }
  const limit = Math.min(requested, CITATION_FACT_MAX_PAGE_SIZE);
  const rows = await db.query<FactRow>(
    `SELECT seq,attempt_id,created_at,task,result FROM citation_facts
    WHERE record_id=? AND seq>? ORDER BY seq LIMIT ?`,
    [recordId, after, limit],
  );
  const facts = rows.map(factOf);
  const last = facts.at(-1);
  const remaining =
    last === undefined || facts.length < limit
      ? []
      : await db.query(`SELECT seq FROM citation_facts WHERE record_id=? AND seq>? LIMIT 1`, [
          recordId,
          last.seq,
        ]);
  return { facts, nextAfter: remaining.length === 0 ? null : last!.seq };
}

/** Replay is idempotent by attempt id; a new retry appends rather than erasing unreachable history. */
export async function appendCitationFact(
  db: PluginDatabase,
  task: CitationFactTask,
  result: CitationFactResult,
  metadata: { readonly attemptId: string; readonly createdAt: string },
): Promise<{ readonly outcome: "appended" | "duplicate"; readonly fact: CitationFact }> {
  if (!metadata.attemptId || !Number.isFinite(Date.parse(metadata.createdAt)))
    throw new CitationFactError("invalid-fact");
  if (!CitationFactTaskSchema.safeParse(task).success) throw new CitationFactError("invalid-fact");
  const parsed = CitationFactResultSchema.safeParse(result);
  if (!parsed.success) throw new CitationFactError("invalid-fact");
  validateResult(task, parsed.data);
  if (
    parsed.data.sourceReading !==
    (task.locator.coordinates === "raw" ? "historical-events" : "normalized-records")
  ) {
    throw new CitationFactError("invalid-fact");
  }
  const key = [task.recordId, task.field, task.ordinal, metadata.attemptId] as const;
  const taskJson = canonical(task);
  const resultJson = canonical(parsed.data);
  const existing = async (): Promise<CitationFact | null> => {
    const rows = await db.query<FactRow>(
      `SELECT seq,attempt_id,created_at,task,result FROM citation_facts
      WHERE record_id=? AND field=? AND ordinal=? AND attempt_id=?`,
      key,
    );
    const held = rows[0];
    if (held === undefined) return null;
    if (held.task !== taskJson || held.result !== resultJson)
      throw new CitationFactError("fact-conflict");
    return factOf(held);
  };
  const terminal = async (): Promise<CitationFact | null> => {
    const rows = await db.query<FactRow>(
      `SELECT seq,attempt_id,created_at,task,result FROM citation_facts
      WHERE record_id=? AND field=? AND ordinal=? AND task=? AND status='available' ORDER BY seq LIMIT 1`,
      [task.recordId, task.field, task.ordinal, taskJson],
    );
    const held = rows[0];
    if (held === undefined) return null;
    if (held.result !== resultJson) throw new CitationFactError("fact-conflict");
    return factOf(held);
  };
  const duplicate = await existing();
  if (duplicate !== null) return { outcome: "duplicate", fact: duplicate };
  const rows = await db.query<IndexedPositionRow>(
    `${POSITIONS_SQL} SELECT *,NULL AS latest_status FROM positions
    WHERE record_id=? AND field=? AND ordinal=?`,
    [task.recordId, task.field, task.ordinal],
  );
  const indexed = rows[0];
  const row =
    indexed === undefined
      ? undefined
      : { ...indexed, ...(await sourceMetadata(db, indexed.producer_id)) };
  if (row === undefined || canonical(taskOf(row)) !== taskJson)
    throw new CitationFactError("task-mismatch");
  const settled = await terminal();
  if (settled !== null) return { outcome: "duplicate", fact: settled };
  const written = await db.run(
    `INSERT INTO citation_facts
    (record_id,field,ordinal,attempt_id,citation_digest,task,result,status,quote_outcome,created_at)
    SELECT ?,?,?,?,?,?,?,?,?,? WHERE EXISTS (
      SELECT 1 FROM records r LEFT JOIN runs producer ON producer.id=r.run_id WHERE r.id=?
      AND producer.preparation IS ? AND ${MATERIALS_SQL}=?)
    AND NOT EXISTS (SELECT 1 FROM citation_facts
      WHERE record_id=? AND field=? AND ordinal=? AND task=? AND status='available')
    ON CONFLICT(record_id,field,ordinal,attempt_id) DO NOTHING`,
    [
      ...key,
      task.citationDigest,
      taskJson,
      resultJson,
      result.status,
      result.check.outcome,
      metadata.createdAt,
      task.recordId,
      row.preparation,
      row.materials,
      task.recordId,
      task.field,
      task.ordinal,
      taskJson,
    ],
  );
  const fact = await existing();
  if (fact === null) {
    const raced = await terminal();
    if (raced !== null) return { outcome: "duplicate", fact: raced };
    throw new CitationFactError("task-mismatch");
  }
  return { outcome: Number(written.changes) === 0 ? "duplicate" : "appended", fact };
}
