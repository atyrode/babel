import type { GuestDatabase } from "@manifold/plugin-kit";
import {
  ANALYSIS_BRIEF_BYTE_LIMIT,
  ANALYSIS_BRIEF_LIMIT,
  ANALYSIS_SOURCE_LIMIT,
  AnalysisBriefRecordSchema,
  CHALLENGE_RELATION,
  MaterialIndexSchema,
  MAX_MATERIAL_BYTES,
  OPERATIONS,
  type AnalysisBriefRecord,
  type Stage,
} from "../contract.ts";

/** A bounded offer of immutable claims and real, independently selectable material. */
export interface AnalysisOffer {
  readonly stage: Stage;
  readonly recordId: string;
  readonly rootId: string;
  readonly kind: string;
  readonly selectors: readonly string[];
  readonly brief: readonly AnalysisBriefRecord[];
  readonly fingerprint: string;
}

const FRONTIER_LIMIT = 256;
// Payloads alone can reach 16 KiB each; leave room below the database response-byte ceiling.
const RECORD_SCAN_LIMIT = 64;
const encoder = new TextEncoder();
const string = (value: unknown): string => (typeof value === "string" ? value : "");

function object(value: unknown): Record<string, unknown> | null {
  if (typeof value !== "string") return null;
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

interface Held {
  readonly record: AnalysisBriefRecord;
  readonly root: string;
  readonly parent: string;
  readonly topics: readonly string[];
  readonly cited: readonly string[];
  readonly sources: readonly string[];
}

/**
 * Selection is deliberately finite. Whole records are admitted, never clipped to fit. A
 * synthesis group has an actual shared candidate, active topic or cited session; counting
 * arbitrary observations is not provenance. A preparation's material is in its native
 * receipt, not in the parent Code run's answer or its preparation intent.
 */
export async function analysisOffers(
  db: GuestDatabase,
  machineId: string,
  stages: readonly Stage[],
  eligible: ReadonlySet<string>,
  filings: ReadonlyMap<string, { topics: readonly string[] }>,
  activeTopics: ReadonlySet<string>,
): Promise<{ offers: AnalysisOffer[]; missing: string[] }> {
  const offers: AnalysisOffer[] = [];
  const missing: string[] = [];
  const sessions = await db.query(
    `SELECT selector, content_digest, snapshot_id, modified_at, size FROM sessions
      WHERE host = ? AND live = 0 AND kind = 'operator'
      ORDER BY seen_at DESC, selector LIMIT ?`,
    [machineId, FRONTIER_LIMIT],
  );
  const catalog = new Map(sessions.map((row) => [string(row["selector"]), row]));
  const sources = (selectors: readonly string[]): string[] => {
    const selected: string[] = [];
    let bytes = 0;
    for (const selector of [...new Set(selectors)].sort()) {
      const row = catalog.get(selector);
      if (row === undefined) continue;
      const size = Number(row["size"] ?? 0);
      if (!Number.isFinite(size) || size < 0 || bytes + size > MAX_MATERIAL_BYTES) continue;
      if (selected.length === ANALYSIS_SOURCE_LIMIT) break;
      selected.push(selector);
      bytes += size;
    }
    return selected;
  };
  const offer = (
    stage: Stage,
    root: string,
    records: readonly Held[],
    material: readonly string[],
  ): void => {
    const selectors = sources(material);
    if (selectors.length === 0) {
      missing.push(root);
      return;
    }
    const brief = records.map((held) => held.record).sort((a, b) => a.id.localeCompare(b.id));
    const first = records.find((record) => record.root === root) ?? records[0];
    const fingerprint = JSON.stringify([
      machineId,
      selectors.map((selector) => {
        const row = catalog.get(selector)!;
        return [
          selector,
          row["content_digest"] ||
            row["snapshot_id"] || [
              row["modified_at"],
              row["size"] == null ? null : Number(row["size"]),
            ],
        ];
      }),
      brief,
    ]);
    offers.push({
      stage,
      recordId: first?.record.id ?? root,
      rootId: root,
      kind: first?.record.kind ?? "session",
      selectors,
      brief,
      fingerprint,
    });
  };

  if (stages.includes("explore")) {
    for (const row of sessions) {
      const selector = string(row["selector"]);
      // A source filed under an excluded topic must not escape through exploration.
      const linked = await db.query(
        `SELECT DISTINCT r.root_id, parent.root_id AS parent_root
           FROM edges e JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
           LEFT JOIN records parent ON parent.id = r.parent_id
          WHERE e.kind = 'cites' AND e.to_kind = 'session' AND e.to_id = ? LIMIT ?`,
        [selector, FRONTIER_LIMIT + 1],
      );
      if (linked.length > FRONTIER_LIMIT) continue;
      if (
        linked.some(
          (row) =>
            !eligible.has(string(row["root_id"])) ||
            (string(row["parent_root"]) !== "" && !eligible.has(string(row["parent_root"]))),
        )
      )
        continue;
      offer("explore", selector, [], [selector]);
    }
  }
  if (!stages.some((stage) => stage !== "explore")) return { offers, missing };

  const rows = await db.query(
    `SELECT r.id, r.root_id, r.kind, r.parent_id, r.run_id, r.title, r.payload
       FROM records r
      WHERE r.kind IN ('hypothesis', 'observation')
        AND length(CAST(r.payload AS BLOB)) <= ?
        AND NOT EXISTS (SELECT 1 FROM records newer WHERE newer.root_id = r.root_id
                         AND (newer.seq > r.seq OR (newer.seq = r.seq AND
                           (newer.created_at > r.created_at OR (newer.created_at = r.created_at AND newer.id > r.id)))))
      ORDER BY r.created_at DESC, r.id LIMIT ?`,
    [ANALYSIS_BRIEF_BYTE_LIMIT, RECORD_SCAN_LIMIT],
  );
  const held: Held[] = [];
  const runSources = new Map<string, readonly string[]>();
  for (const row of rows) {
    const root = string(row["root_id"]);
    if (!eligible.has(root)) continue;
    const parentId = string(row["parent_id"]);
    if (
      parentId !== "" &&
      !eligible.has(parentId) &&
      !rows.some((parent) => parent["id"] === parentId && eligible.has(string(parent["root_id"])))
    )
      continue;
    const id = string(row["id"]);
    const payload = object(row["payload"]);
    if (payload === null) continue;
    const edges = await db.query(
      `SELECT kind, to_kind, to_id FROM edges
        WHERE from_id = ? AND from_kind = ? AND kind IN ('cites', 'contradicts', ?)
        ORDER BY kind, to_id LIMIT ?`,
      [id, string(row["kind"]), CHALLENGE_RELATION, FRONTIER_LIMIT + 1],
    );
    if (edges.length > FRONTIER_LIMIT) continue;
    const parsed = AnalysisBriefRecordSchema.safeParse({
      id,
      kind: row["kind"],
      runId: string(row["run_id"]) || null,
      summary: string(row["title"]),
      payload,
      objectionTo: [
        ...new Set(
          edges
            .filter((edge) => edge["kind"] !== "cites" && edge["to_kind"] === "hypothesis")
            .map((edge) => string(edge["to_id"])),
        ),
      ],
    });
    if (!parsed.success) continue;
    const cited = edges
      .filter((edge) => edge["kind"] === "cites" && edge["to_kind"] === "session")
      .map((edge) => string(edge["to_id"]));
    const runId = parsed.data.runId;
    let fallback = runId === null ? [] : runSources.get(runId);
    if (fallback === undefined && runId !== null) {
      const receipts = await db.query(
        `SELECT p.payload FROM runs source JOIN runs p ON p.job_id = source.prepare_job_id
          WHERE source.id = ? AND p.kind = ? AND p.closure = 'completed'
          ORDER BY p.started_at DESC LIMIT 1`,
        [runId, OPERATIONS.prepare],
      );
      const receipt = object(receipts[0]?.["payload"]);
      const material = MaterialIndexSchema.safeParse(receipt?.["material"]);
      fallback =
        material.success && material.data.machineId === machineId
          ? material.data.sessions.map((entry) => entry.selector)
          : [];
      runSources.set(runId, fallback);
    }
    fallback ??= [];
    // Cited sources may be older than the exploration frontier. Look up exact selectors, never
    // widen a missing citation into a time-window selection.
    for (const selector of new Set([...cited, ...fallback])) {
      if (catalog.has(selector)) continue;
      const exact = await db.query(
        `SELECT selector, content_digest, snapshot_id, modified_at, size FROM sessions
          WHERE selector = ? AND host = ? AND live = 0 AND kind = 'operator'`,
        [selector, machineId],
      );
      if (exact[0] !== undefined) catalog.set(selector, exact[0]);
    }
    held.push({
      record: parsed.data,
      root,
      parent: string(row["parent_id"]),
      topics: filings.get(root)?.topics ?? [],
      cited,
      sources: [...new Set([...cited, ...fallback])],
    });
  }

  const bounded = (records: readonly Held[]): Held[] => {
    const out: Held[] = [];
    const ids = new Set<string>();
    let bytes = 2;
    for (const row of records) {
      if (ids.has(row.record.id)) continue;
      const size =
        encoder.encode(JSON.stringify(row.record)).byteLength + (out.length === 0 ? 0 : 1);
      if (bytes + size > ANALYSIS_BRIEF_BYTE_LIMIT) continue;
      if (out.length === ANALYSIS_BRIEF_LIMIT) break;
      out.push(row);
      ids.add(row.record.id);
      bytes += size;
    }
    return out;
  };
  const hypotheses = held.filter((row) => row.record.kind === "hypothesis");
  if (stages.includes("challenge")) {
    for (const target of hypotheses) {
      const related = held.filter(
        (row) =>
          row.parent === target.record.id || row.record.objectionTo.includes(target.record.id),
      );
      // Prior objections get a place before supporting observations; a challenge should not
      // pay to rediscover an objection already on the ledger.
      related.sort(
        (a, b) =>
          Number(b.record.objectionTo.length > 0) - Number(a.record.objectionTo.length > 0) ||
          a.record.id.localeCompare(b.record.id),
      );
      const brief = bounded([target, ...related]);
      if (!brief.includes(target)) continue;
      offer(
        "challenge",
        target.root,
        brief,
        brief.flatMap((row) => row.sources),
      );
    }
  }
  if (stages.includes("synthesize")) {
    const groups = new Map<string, Held[]>();
    for (const observation of held) {
      if (observation.record.kind !== "observation" || observation.record.runId === null) continue;
      const parent = hypotheses.find((row) => row.record.id === observation.parent);
      const keys = [
        ...(parent === undefined ? [] : [`candidate:${parent.root}`]),
        ...observation.topics
          .filter((topic) => activeTopics.has(topic))
          .map((topic) => `entity:${topic}`),
        ...observation.cited
          .filter((selector) => catalog.has(selector))
          .map((selector) => `session:${selector}`),
      ];
      for (const key of keys) {
        const group = groups.get(key) ?? [];
        group.push(observation);
        groups.set(key, group);
      }
    }
    const seen = new Set<string>();
    for (const [key, group] of [...groups].sort(([a], [b]) => a.localeCompare(b))) {
      // Round-robin the source runs first so a prolific source cannot crowd the independent
      // second run out of a finite brief.
      group.sort((a, b) => a.record.id.localeCompare(b.record.id));
      const runs = new Set<string>();
      const first = group.filter((row) => {
        const run = row.record.runId!;
        if (runs.has(run)) return false;
        runs.add(run);
        return true;
      });
      const brief = bounded([...first, ...group]);
      if (new Set(brief.map((row) => row.record.runId)).size < 2) continue;
      const signature = brief
        .map((row) => row.record.id)
        .sort()
        .join(",");
      if (seen.has(signature)) continue;
      seen.add(signature);
      const parent = key.startsWith("candidate:")
        ? hypotheses.find((row) => `candidate:${row.root}` === key)
        : undefined;
      const complete = parent === undefined ? brief : bounded([parent, ...brief]);
      if (
        new Set(
          complete
            .filter((row) => row.record.kind === "observation")
            .map((row) => row.record.runId),
        ).size < 2
      )
        continue;
      offer(
        "synthesize",
        parent !== undefined && complete.includes(parent) ? parent.root : brief[0]!.root,
        complete,
        complete.flatMap((row) => row.sources),
      );
    }
  }
  return { offers, missing };
}
