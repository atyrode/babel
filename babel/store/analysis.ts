import type { GuestDatabase, GuestSqlRow } from "@manifold/plugin-kit";
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

interface Head {
  readonly id: string;
  readonly root: string;
  readonly kind: string;
  readonly parent: string;
  readonly runId: string | null;
  readonly cited: readonly string[];
  readonly objectionTo: readonly string[];
}

/** Page through the entire frontier, retaining only identifiers between pages. Offers are
 * streamed so the caller can apply settlement, cooldown and caps before retaining payloads.
 * Whole records are admitted, never clipped; synthesis reserves two original runs first. */
export async function* analysisOffers(
  db: GuestDatabase,
  machineId: string,
  stages: readonly Stage[],
  eligible: ReadonlySet<string>,
  filings: ReadonlyMap<string, { topics: readonly string[] }>,
  activeTopics: ReadonlySet<string>,
): AsyncGenerator<AnalysisOffer | { missing: string }> {
  const source = async (selector: string): Promise<GuestSqlRow | undefined> => {
    const rows = await db.query(
      `SELECT selector, content_digest, snapshot_id, modified_at, size FROM sessions
        WHERE selector = ? AND host = ? AND live = 0 AND kind = 'operator'`,
      [selector, machineId],
    );
    return rows[0];
  };
  const offer = async (
    stage: Stage,
    root: string,
    brief: readonly AnalysisBriefRecord[],
    material: readonly string[],
    recordId = root,
    kind = "session",
  ): Promise<AnalysisOffer | { missing: string }> => {
    const selected: GuestSqlRow[] = [];
    let bytes = 0;
    for (const selector of [...new Set(material)].sort()) {
      const row = await source(selector);
      if (row === undefined) continue;
      const size = Number(row["size"] ?? 0);
      if (!Number.isFinite(size) || size < 0 || bytes + size > MAX_MATERIAL_BYTES) continue;
      selected.push(row);
      bytes += size;
      if (selected.length === ANALYSIS_SOURCE_LIMIT) break;
    }
    if (selected.length === 0) return { missing: root };
    const sorted = [...brief].sort((a, b) => a.id.localeCompare(b.id));
    return {
      stage,
      recordId,
      rootId: root,
      kind,
      selectors: selected.map((row) => string(row["selector"])),
      brief: sorted,
      fingerprint: JSON.stringify([
        machineId,
        selected.map((row) => [
          row["selector"],
          row["content_digest"] ||
            row["snapshot_id"] || [
              row["modified_at"],
              row["size"] == null ? null : Number(row["size"]),
            ],
        ]),
        sorted,
      ]),
    };
  };

  if (stages.includes("explore")) {
    let cursor = "";
    for (;;) {
      const page = await db.query(
        `SELECT selector FROM sessions WHERE host = ? AND live = 0 AND kind = 'operator'
          AND selector > ? ORDER BY selector LIMIT ?`,
        [machineId, cursor, FRONTIER_LIMIT],
      );
      for (const row of page) {
        const selector = string(row["selector"]);
        const linked = await db.query(
          `SELECT DISTINCT r.root_id, parent.root_id AS parent_root
             FROM edges e JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
             LEFT JOIN records parent ON parent.id = r.parent_id
            WHERE e.kind = 'cites' AND e.to_kind = 'session' AND e.to_id = ? LIMIT ?`,
          [selector, FRONTIER_LIMIT + 1],
        );
        if (
          linked.length > FRONTIER_LIMIT ||
          linked.some(
            (link) =>
              !eligible.has(string(link["root_id"])) ||
              (string(link["parent_root"]) !== "" && !eligible.has(string(link["parent_root"]))),
          )
        )
          continue;
        yield await offer("explore", selector, [], [selector]);
      }
      if (page.length < FRONTIER_LIMIT) break;
      cursor = string(page[page.length - 1]!["selector"]);
    }
  }
  if (!stages.some((stage) => stage !== "explore")) return;

  // Metadata may span pages; payloads never do. This also lets related observations join a
  // candidate or one another even when their creation times are arbitrarily far apart.
  const heads = new Map<string, Head>();
  let cursor = "";
  for (;;) {
    const page = await db.query(
      `SELECT r.id, r.root_id, r.kind, r.parent_id, r.run_id, parent.root_id AS parent_root
         FROM records r LEFT JOIN records parent ON parent.id = r.parent_id
        WHERE r.id > ? AND r.kind IN ('hypothesis', 'observation')
          AND length(CAST(r.payload AS BLOB)) <= ?
          AND NOT EXISTS (SELECT 1 FROM records newer WHERE newer.root_id = r.root_id
            AND (newer.seq > r.seq OR (newer.seq = r.seq AND
              (newer.created_at > r.created_at OR (newer.created_at = r.created_at AND newer.id > r.id)))))
        ORDER BY r.id LIMIT ?`,
      [cursor, ANALYSIS_BRIEF_BYTE_LIMIT, RECORD_SCAN_LIMIT],
    );
    for (const row of page) {
      const root = string(row["root_id"]);
      if (
        !eligible.has(root) ||
        (string(row["parent_id"]) !== "" && !eligible.has(string(row["parent_root"])))
      )
        continue;
      const id = string(row["id"]);
      const edges = await db.query(
        `SELECT kind, to_kind, to_id FROM edges WHERE from_id = ? AND from_kind = ?
          AND kind IN ('cites', 'contradicts', ?) ORDER BY kind, to_id LIMIT ?`,
        [id, string(row["kind"]), CHALLENGE_RELATION, FRONTIER_LIMIT + 1],
      );
      if (edges.length > FRONTIER_LIMIT) continue;
      heads.set(id, {
        id,
        root,
        kind: string(row["kind"]),
        parent: string(row["parent_id"]),
        runId: string(row["run_id"]) || null,
        cited: edges
          .filter((edge) => edge["kind"] === "cites" && edge["to_kind"] === "session")
          .map((edge) => string(edge["to_id"])),
        objectionTo: [
          ...new Set(
            edges
              .filter((edge) => edge["kind"] !== "cites" && edge["to_kind"] === "hypothesis")
              .map((edge) => string(edge["to_id"])),
          ),
        ],
      });
    }
    if (page.length < RECORD_SCAN_LIMIT) break;
    cursor = string(page[page.length - 1]!["id"]);
  }

  const bounded = async (
    ids: Iterable<string>,
    initial: readonly AnalysisBriefRecord[] = [],
  ): Promise<AnalysisBriefRecord[]> => {
    const out = [...initial];
    const seen = new Set(initial.map((row) => row.id));
    let bytes = encoder.encode(JSON.stringify(initial)).byteLength;
    for (const id of ids) {
      if (out.length === ANALYSIS_BRIEF_LIMIT) break;
      if (seen.has(id)) continue;
      seen.add(id);
      const head = heads.get(id);
      if (head === undefined) continue;
      const rows = await db.query(`SELECT title, payload FROM records WHERE id = ?`, [id]);
      const payload = object(rows[0]?.["payload"]);
      if (payload === null) continue;
      const parsed = AnalysisBriefRecordSchema.safeParse({
        id,
        kind: head.kind,
        runId: head.runId,
        summary: string(rows[0]?.["title"]),
        payload,
        objectionTo: head.objectionTo,
      });
      if (!parsed.success) continue;
      const size = encoder.encode(JSON.stringify(parsed.data)).byteLength + (out.length ? 1 : 0);
      if (bytes + size > ANALYSIS_BRIEF_BYTE_LIMIT) continue;
      out.push(parsed.data);
      bytes += size;
    }
    return out;
  };
  const material = async (brief: readonly AnalysisBriefRecord[]): Promise<string[]> => {
    const selectors = new Set<string>();
    const runs = new Set<string>();
    for (const record of brief) {
      for (const selector of heads.get(record.id)!.cited) selectors.add(selector);
      if (record.runId === null || runs.has(record.runId)) continue;
      runs.add(record.runId);
      const receipts = await db.query(
        `SELECT p.payload FROM runs source JOIN runs p ON p.job_id = source.prepare_job_id
          WHERE source.id = ? AND p.kind = ? AND p.closure = 'completed'
          ORDER BY p.started_at DESC LIMIT 1`,
        [record.runId, OPERATIONS.prepare],
      );
      const receipt = object(receipts[0]?.["payload"]);
      const parsed = MaterialIndexSchema.safeParse(receipt?.["material"]);
      if (parsed.success && parsed.data.machineId === machineId)
        for (const entry of parsed.data.sessions) selectors.add(entry.selector);
    }
    return [...selectors];
  };
  const related = new Map<string, string[]>();
  for (const head of heads.values()) {
    for (const target of new Set([head.parent, ...head.objectionTo])) {
      if (target === "") continue;
      const group = related.get(target) ?? [];
      group.push(head.id);
      related.set(target, group);
    }
  }
  if (stages.includes("challenge")) {
    for (const target of heads.values()) {
      if (target.kind !== "hypothesis") continue;
      const relatedIds = [...(related.get(target.id) ?? [])].sort(
        (a, b) =>
          Number(heads.get(b)!.objectionTo.length > 0) -
            Number(heads.get(a)!.objectionTo.length > 0) || a.localeCompare(b),
      );
      const brief = await bounded([target.id, ...relatedIds]);
      if (!brief.some((record) => record.id === target.id)) continue;
      yield await offer(
        "challenge",
        target.root,
        brief,
        await material(brief),
        target.id,
        target.kind,
      );
    }
  }
  if (stages.includes("synthesize")) {
    const groups = new Map<string, string[]>();
    for (const head of heads.values()) {
      if (head.kind !== "observation" || head.runId === null) continue;
      const parent = heads.get(head.parent);
      const keys = [
        ...(parent?.kind === "hypothesis" ? [`candidate:${parent.id}`] : []),
        ...(filings.get(head.root)?.topics ?? [])
          .filter((topic) => activeTopics.has(topic))
          .map((topic) => `entity:${topic}`),
      ];
      for (const selector of head.cited)
        if ((await source(selector)) !== undefined) keys.push(`session:${selector}`);
      for (const key of keys) {
        const group = groups.get(key) ?? [];
        group.push(head.id);
        groups.set(key, group);
      }
    }
    const seen = new Set<string>();
    for (const [key, group] of [...groups].sort(([a], [b]) => a.localeCompare(b))) {
      if (new Set(group.map((id) => heads.get(id)!.runId)).size < 2) continue;
      const pending = new Set(group);
      while (pending.size > 0) {
        const anchor = pending.values().next().value!;
        pending.delete(anchor);
        let pair: AnalysisBriefRecord[] = [];
        for (const partner of group) {
          if (heads.get(partner)!.runId === heads.get(anchor)!.runId) continue;
          pair = await bounded([anchor, partner]);
          if (pair.length === 2) break;
        }
        if (pair.length !== 2) continue;
        // Reserve two original runs before adding target context and both forms of critique.
        // Each subsequent window includes unoffered observations, not the same first 24 forever.
        const parents = [...new Set(pair.map((row) => heads.get(row.id)!.parent))].filter(
          (id) => heads.get(id)?.kind === "hypothesis",
        );
        let brief = pair;
        for (const parent of parents) {
          brief = await bounded([parent], brief);
          if (!brief.some((row) => row.id === parent)) continue;
          const objections = (related.get(parent) ?? []).filter((id) =>
            heads.get(id)!.objectionTo.includes(parent),
          );
          brief = await bounded(objections, brief);
        }
        brief = await bounded(pending, brief);
        for (const row of brief) pending.delete(row.id);
        const signature = brief
          .map((row) => row.id)
          .sort()
          .join(",");
        if (seen.has(signature)) continue;
        seen.add(signature);
        const parent = key.startsWith("candidate:")
          ? heads.get(key.slice("candidate:".length))
          : undefined;
        const target =
          parent !== undefined && brief.some((row) => row.id === parent.id)
            ? parent
            : heads.get(anchor)!;
        yield await offer(
          "synthesize",
          target.root,
          brief,
          await material(brief),
          target.id,
          target.kind,
        );
      }
    }
  }
}
