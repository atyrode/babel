import type { GuestDatabase, GuestSqlRow } from "@manifold/plugin-kit";
import {
  ANALYSIS_BRIEF_BYTE_LIMIT,
  ANALYSIS_BRIEF_LIMIT,
  ANALYSIS_SOURCE_LIMIT,
  AnalysisBriefRecordSchema,
  CHALLENGE_RELATION,
  MATERIAL_HEADROOM_BYTES,
  MATERIAL_SCRATCH_COPIES,
  MaterialIndexSchema,
  MAX_MATERIAL_BYTES,
  OPERATIONS,
  RUNTIME_SCRATCH_BYTES,
  type AnalysisBriefRecord,
  type Stage,
} from "../contract.ts";
import type { StandingRemark } from "../server/engine/prompts.ts";
import { ANALYSABLE_SESSION } from "./exclusions.ts";
import { readExcludedRecordIds } from "./source-privacy.ts";

/**
 * A SESSION A PREPARATION CAN READ (#453), as a predicate over `sessions s`: one whose row names
 * an archived capture — the snapshot, the path inside it and the label it was taken under, with
 * the size and modification time the catalog recorded — and that is neither live nor excluded by
 * the caller. A preparation reads the archive and nothing else, so a row with no capture (an
 * imported one the catalog has not listed yet) is catalogued and not selectable. There is no
 * host condition: any machine holding the archive binding prepares any capture.
 */
export const ARCHIVED_CAPTURE =
  "s.live = 0 AND s.snapshot_id IS NOT NULL AND s.archive_path IS NOT NULL " +
  "AND s.archive_label IS NOT NULL AND s.size IS NOT NULL AND s.modified_at IS NOT NULL";

/**
 * THE MOST CATALOGUED BYTES ONE PREPARATION ON `machineId` MAY SEAL (#453):
 * `min(MAX_MATERIAL_BYTES, ⌊(capacity − MATERIAL_HEADROOM_BYTES) / (share × MATERIAL_SCRATCH_COPIES)⌋)`.
 *
 * `capacity` is the named-output scratch the machine's newest `catalog` or `prepare` receipt
 * measured (`outputCapacity.bytes`; no other kind reports one), so the bound follows the machine
 * rather than a constant. `share` is how many materials the lane asking may hold on that machine
 * at once — one for an operator's launch, the per-machine bound for the conductor's lanes, the
 * fan for a drain. A native seal needs a second copy of each material beside its raw files, so
 * both copies count against the same scratch. Until a machine reports capacity, use its declared
 * runtime scratch size; `prepare` still refuses a lease its own free-space measurement says will
 * not fit, before fetching.
 */
export async function materialBound(
  db: GuestDatabase,
  machineId: string,
  share: number,
): Promise<number> {
  const watermark = String(
    (await db.query(`SELECT CAST(coalesce(max(rowid), 0) AS TEXT) AS row_id FROM runs`))[0]?.[
      "row_id"
    ] ?? "0",
  );
  let cursor: { rowId: string; startedAt: string } | null = null;
  let capacity: unknown;
  // Decode only this physical page, stopping at its first integer capacity. A later malformed
  // receipt must not turn an earlier successful LIMIT 1 selection into a JSON error.
  let turnAt = performance.now();
  for (;;) {
    // Seek the remaining timestamp ties separately from older rows. Both are covered by
    // runs_by_machine, including its implicit ascending rowid tie-breaker.
    const preceding: string =
      cursor === null
        ? ""
        : `same_time AS MATERIALIZED (
            SELECT rowid AS row_number, started_at FROM runs
              WHERE machine_id = ? AND started_at = ? AND rowid > CAST(? AS INTEGER)
                AND rowid <= CAST(? AS INTEGER)
              ORDER BY rowid LIMIT 8
          ), older AS MATERIALIZED (
            SELECT rowid AS row_number, started_at FROM runs
              WHERE machine_id = ? AND started_at < ? AND rowid <= CAST(? AS INTEGER)
              ORDER BY started_at DESC, rowid ASC
              LIMIT (8 - (SELECT count(*) FROM same_time))
          ),`;
    const page: string =
      cursor === null
        ? `SELECT rowid AS row_number, started_at FROM runs
            WHERE machine_id = ? AND rowid <= CAST(? AS INTEGER)
            ORDER BY started_at DESC, rowid ASC LIMIT 8`
        : `SELECT row_number, started_at FROM same_time
            UNION ALL SELECT row_number, started_at FROM older`;
    const rows: readonly Readonly<Record<string, unknown>>[] = await db.query(
      `WITH RECURSIVE ${preceding}
        page AS MATERIALIZED (${page}),
        ordered AS MATERIALIZED (
          SELECT row_number, started_at,
            row_number() OVER (ORDER BY started_at DESC, row_number ASC) AS position FROM page
        ), capacity AS (
          SELECT p.row_number, p.started_at, p.position,
            CASE WHEN json_type(r.payload, '$.outputCapacity.bytes') = 'integer'
              THEN json_extract(r.payload, '$.outputCapacity.bytes') END AS bytes
            FROM ordered p JOIN runs r ON r.rowid = p.row_number WHERE p.position = 1
          UNION ALL
          SELECT p.row_number, p.started_at, p.position,
            CASE WHEN json_type(r.payload, '$.outputCapacity.bytes') = 'integer'
              THEN json_extract(r.payload, '$.outputCapacity.bytes') END AS bytes
            FROM capacity previous JOIN ordered p ON p.position = previous.position + 1
            JOIN runs r ON r.rowid = p.row_number WHERE previous.bytes IS NULL
        )
        SELECT CAST(row_number AS TEXT) AS row_id, started_at, bytes FROM capacity
          ORDER BY position DESC LIMIT 1`,
      cursor === null
        ? [machineId, watermark]
        : [
            machineId,
            cursor.startedAt,
            cursor.rowId,
            watermark,
            machineId,
            cursor.startedAt,
            watermark,
          ],
    );
    const row = rows[0];
    if (row === undefined) break;
    capacity = row["bytes"];
    if (capacity !== undefined && capacity !== null) break;
    cursor = { rowId: String(row["row_id"]), startedAt: String(row["started_at"]) };
    if (performance.now() - turnAt >= 50) {
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      turnAt = performance.now();
    }
  }
  const available =
    capacity === undefined || capacity === null ? RUNTIME_SCRATCH_BYTES : Number(capacity);
  const shared = Math.floor(
    (available - MATERIAL_HEADROOM_BYTES) / (Math.max(1, share) * MATERIAL_SCRATCH_COPIES),
  );
  return Math.max(0, Math.min(MAX_MATERIAL_BYTES, shared));
}

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

/**
 * WHAT THE OPERATOR HAS TOLD BABEL, newest first and bounded: the rows `tell` wrote. The
 * `policy` door reads them back, and an analysis prompt quotes a selection of them
 * (`carriedSteering`, `server/engine/prompts.ts`) — so a brief is chosen to fit beside the same
 * remarks its prompt will carry, and there is one read for both.
 */
export async function operatorRemarks(db: GuestDatabase): Promise<StandingRemark[]> {
  const told = await db.query(
    `SELECT id, text, target_kind, target_id, recorded_at FROM steering
      WHERE actor_kind = 'operator'
      ORDER BY recorded_at DESC, id DESC LIMIT 20`,
  );
  return told.map((entry) => ({
    id: string(entry["id"]),
    text: string(entry["text"]),
    about:
      string(entry["target_id"]) === ""
        ? ""
        : `${string(entry["target_kind"])}:${string(entry["target_id"])}`,
    at: string(entry["recorded_at"]),
  }));
}

/**
 * An offer the frontier could not make, and which bound refused it: no archived capture fits the
 * machine's MATERIAL bound, or the PROMPT the run would be posted with cannot fit Code's bound.
 * A prompt refusal says what its smallest form was and what that measured, because the two have
 * different remedies: a `record` did not fit even alone — an explore's one session with no
 * brief, or a challenge's target — and a `pair` is a synthesis whose record fits alone while no
 * two-run pair it belongs to does, which is the least a synthesis is.
 */
export type AnalysisRefusal =
  | { readonly missing: string; readonly stage: Stage; readonly bound: "material" }
  | {
      readonly missing: string;
      readonly stage: Stage;
      readonly bound: "prompt";
      readonly smallest: "record" | "pair";
      /** The encoded bytes that smallest prompt measured. */
      readonly bytes: number;
    };

/**
 * WHAT A RUN OF `stage` OVER THIS BRIEF AND THESE SESSIONS WOULD BE POSTED WITH, measured: the
 * prompt's encoded bytes, composed with the stage's recipe, its contract and the operator's
 * remarks, and the most bytes Code takes. The coordinator supplies both
 * (`server/engine/prompts.ts`, `analysisPromptBytes`); selection only asks, once per record it
 * weighs.
 */
export interface PromptBound {
  readonly limit: number;
  bytes(stage: Stage, brief: readonly AnalysisBriefRecord[], selectors: readonly string[]): number;
}

/** Page through the entire frontier, retaining only identifiers between pages. Offers are
 * streamed so the caller can apply settlement, cooldown and caps before retaining payloads.
 * The consumer may stop each stage independently through wants, before its next offer is built.
 * Whole records are admitted, never clipped; synthesis reserves two original runs first.
 *
 * A record is admitted only while the prompt the run will be posted with still fits the
 * `prompt` bound, measured with the sessions the brief so far would prepare. One that does not
 * fit is skipped whole, as one over the brief's byte bound is, and a smaller one behind it is
 * still weighed. An offer whose smallest form cannot fit is refused with that form and its
 * measure ({@link AnalysisRefusal}) rather than prepared and closed `prompt_too_large` after the
 * preparation has been paid for.
 *
 * Material is offered from archived captures only, wherever they were recorded (#453): the
 * routed machine prepares them from the archive, so `machineId` bounds the material by that
 * machine's measured scratch at `share` concurrent materials ({@link materialBound}) and says
 * nothing about where a session came from. */
export async function* analysisOffers(
  db: GuestDatabase,
  machineId: string,
  share: number,
  stages: readonly Stage[],
  eligible: ReadonlySet<string>,
  filings: ReadonlyMap<string, { topics: readonly string[] }>,
  activeTopics: ReadonlySet<string>,
  prompt: PromptBound,
  wants: (stage: Stage) => boolean = () => true,
): AsyncGenerator<AnalysisOffer | AnalysisRefusal> {
  const bound = await materialBound(db, machineId, share);
  const excludedRecords = await readExcludedRecordIds(db);
  let turnAt = performance.now();
  const sources = new Map<string, Promise<GuestSqlRow | undefined>>();
  const source = (selector: string): Promise<GuestSqlRow | undefined> => {
    const cached = sources.get(selector);
    if (cached !== undefined) return cached;
    const pending = db
      .query(
        `SELECT s.selector, s.content_digest, s.snapshot_id, s.modified_at, s.size FROM sessions s
          WHERE s.selector = ? AND ${ARCHIVED_CAPTURE} AND ${ANALYSABLE_SESSION} AND s.kind = 'operator'`,
        [selector],
      )
      .then((rows) => rows[0]);
    sources.set(selector, pending);
    return pending;
  };
  // The captures one preparation of this material would seal: the archived ones, in selector
  // order, while they fit the machine's bound and the source limit.
  const selection = async (material: readonly string[]): Promise<GuestSqlRow[]> => {
    const selected: GuestSqlRow[] = [];
    let bytes = 0;
    for (const selector of [...new Set(material)].sort()) {
      const row = await source(selector);
      if (row === undefined) continue;
      const size = Number(row["size"] ?? 0);
      if (!Number.isFinite(size) || size < 0 || bytes + size > bound) continue;
      selected.push(row);
      bytes += size;
      if (selected.length === ANALYSIS_SOURCE_LIMIT) break;
    }
    return selected;
  };
  const offer = async (
    stage: Stage,
    root: string,
    brief: readonly AnalysisBriefRecord[],
    material: readonly string[],
    recordId = root,
    kind = "session",
  ): Promise<AnalysisOffer | AnalysisRefusal> => {
    const selected = await selection(material);
    if (selected.length === 0) return { missing: root, stage, bound: "material" };
    const selectors = selected.map((row) => string(row["selector"]));
    // A challenge's or synthesis's brief was chosen by `bounded` against this same measure, so
    // this refuses only an explore: its smallest prompt is its one session and no brief.
    const bytes = prompt.bytes(stage, brief, selectors);
    if (bytes > prompt.limit) {
      return { missing: recordId, stage, bound: "prompt", smallest: "record", bytes };
    }
    const sorted = [...brief].sort((a, b) => a.id.localeCompare(b.id));
    return {
      stage,
      recordId,
      rootId: root,
      kind,
      selectors,
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

  if (stages.includes("explore") && wants("explore")) {
    let cursor = "";
    explore: for (;;) {
      if (!wants("explore")) break;
      const page = await db.query(
        `SELECT s.selector FROM sessions s WHERE ${ARCHIVED_CAPTURE} AND ${ANALYSABLE_SESSION} AND s.kind = 'operator'
          AND s.selector > ? ORDER BY s.selector LIMIT ?`,
        [cursor, FRONTIER_LIMIT],
      );
      for (let start = 0; start < page.length; start += 8) {
        if (!wants("explore")) break explore;
        const chunk = page.slice(start, start + 8);
        // Each indexed inner read keeps its own frontier bound before aggregation.
        const links = await db.query(
          `SELECT input.key slot,json_extract(link.value,'$[0]') root_id,
                  json_extract(link.value,'$[1]') parent_root
             FROM json_each(?) input JOIN json_each((
               SELECT json_group_array(json_array(root_id,parent_root)) FROM (
                 SELECT DISTINCT r.root_id,parent.root_id parent_root
                   FROM edges e JOIN records r ON r.id=e.from_id AND r.kind=e.from_kind
                   LEFT JOIN records parent ON parent.id=r.parent_id
                  WHERE e.kind='cites' AND e.to_kind='session' AND e.to_id=input.value LIMIT ?
               )
             )) link ORDER BY input.key,link.key`,
          [JSON.stringify(chunk.map((row) => string(row["selector"]))), FRONTIER_LIMIT + 1],
        );
        const grouped: GuestSqlRow[][] = Array.from({ length: chunk.length }, () => []);
        for (const link of links) grouped[Number(link["slot"])]!.push(link);
        for (let index = 0; index < chunk.length; index++) {
          if (performance.now() - turnAt >= 50) {
            await new Promise<void>((resolve) => setTimeout(resolve, 0));
            turnAt = performance.now();
          }
          if (!wants("explore")) break explore;
          const selector = string(chunk[index]!["selector"]);
          const linked = grouped[index]!;
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
      }
      if (page.length < FRONTIER_LIMIT) break;
      cursor = string(page[page.length - 1]!["selector"]);
    }
  }
  if (!stages.some((stage) => stage !== "explore" && wants(stage))) return;

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
    const edgePages = new Map<string, GuestSqlRow[]>();
    const eligibleRows = page.filter((row) => {
      const root = string(row["root_id"]);
      return (
        eligible.has(root) &&
        (string(row["parent_id"]) === "" || eligible.has(string(row["parent_root"]))) &&
        !excludedRecords.has(string(row["id"]))
      );
    });
    for (let start = 0; start < eligibleRows.length; start += 8) {
      if (performance.now() - turnAt >= 50) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        turnAt = performance.now();
      }
      const chunk = eligibleRows.slice(start, start + 8);
      for (const row of chunk) edgePages.set(string(row["id"]), []);
      const edges = await db.query(
        `SELECT input.key slot,json_extract(edge.value,'$[0]') kind,
                json_extract(edge.value,'$[1]') to_kind,json_extract(edge.value,'$[2]') to_id
           FROM json_each(?) input JOIN json_each((
             SELECT json_group_array(json_array(kind,to_kind,to_id)) FROM (
               SELECT kind,to_kind,to_id FROM edges
                WHERE from_id=json_extract(input.value,'$.id') AND from_kind=json_extract(input.value,'$.kind')
                  AND kind IN ('cites','contradicts',?) ORDER BY kind,to_id LIMIT ?
             )
           )) edge ORDER BY input.key,edge.key`,
        [
          JSON.stringify(chunk.map((row) => ({ id: row["id"], kind: row["kind"] }))),
          CHALLENGE_RELATION,
          FRONTIER_LIMIT + 1,
        ],
      );
      for (const edge of edges)
        edgePages.get(string(chunk[Number(edge["slot"])]!["id"]))!.push(edge);
    }
    for (const row of page) {
      if (performance.now() - turnAt >= 50) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        turnAt = performance.now();
      }
      const root = string(row["root_id"]);
      if (
        !eligible.has(root) ||
        (string(row["parent_id"]) !== "" && !eligible.has(string(row["parent_root"])))
      )
        continue;
      const id = string(row["id"]);
      if (excludedRecords.has(id)) continue;
      const edges = edgePages.get(id)!;
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

  // What each source run was served, read once however many briefs name that run: a brief is
  // measured once for every record it weighs, and each measure needs the material it implies.
  const served = new Map<string, Promise<readonly string[]>>();
  const servedTo = (runId: string): Promise<readonly string[]> => {
    const cached = served.get(runId);
    if (cached !== undefined) return cached;
    const pending = db
      .query(
        `SELECT p.payload FROM runs source JOIN runs p ON p.job_id = source.prepare_job_id
          WHERE source.id = ? AND p.kind = ? AND p.closure = 'completed'
          ORDER BY p.started_at DESC LIMIT 1`,
        [runId, OPERATIONS.prepare],
      )
      .then((receipts) => {
        const receipt = object(receipts[0]?.["payload"]);
        const parsed = MaterialIndexSchema.safeParse(receipt?.["material"]);
        // Whichever machine sealed it: a capture reads the same from any of them.
        return parsed.success ? parsed.data.sessions.map((entry) => entry.selector) : [];
      });
    served.set(runId, pending);
    return pending;
  };
  const material = async (brief: readonly AnalysisBriefRecord[]): Promise<string[]> => {
    const selectors = new Set<string>();
    for (const record of brief) {
      for (const selector of heads.get(record.id)!.cited) selectors.add(selector);
      if (record.runId === null) continue;
      for (const selector of await servedTo(record.runId)) selectors.add(selector);
    }
    return [...selectors];
  };
  // Records' title/payload cannot change or disappear: records_immutable/records_kept enforce
  // that ledger property. Decode each whole brief fact once per offer search, not once per trial
  // pair. No exclusion, capture-currentness or admission decision is retained here.
  type DecodedBrief = { readonly record: AnalysisBriefRecord; readonly bytes: number };
  const decoded = new Map<string, DecodedBrief | undefined>();
  const briefRecord = (id: string, row: GuestSqlRow | undefined): DecodedBrief | undefined => {
    if (decoded.has(id)) return decoded.get(id);
    const head = heads.get(id);
    if (head === undefined) return undefined;
    const payload = object(row?.["payload"]);
    const parsed =
      payload === null
        ? null
        : AnalysisBriefRecordSchema.safeParse({
            id,
            kind: head.kind,
            runId: head.runId,
            summary: string(row?.["title"]),
            payload,
            objectionTo: head.objectionTo,
          });
    const fact =
      parsed?.success === true
        ? { record: parsed.data, bytes: encoder.encode(JSON.stringify(parsed.data)).byteLength }
        : undefined;
    decoded.set(id, fact);
    return fact;
  };
  async function* briefFacts(
    ids: Iterable<string>,
    seen: Set<string>,
    out: readonly AnalysisBriefRecord[],
  ): AsyncGenerator<DecodedBrief | undefined> {
    const iterator = ids[Symbol.iterator]();
    let done = false;
    while (!done && out.length < ANALYSIS_BRIEF_LIMIT) {
      const page: string[] = [];
      const room = Math.min(8, ANALYSIS_BRIEF_LIMIT - out.length);
      while (page.length < room) {
        const next = iterator.next();
        if (next.done === true) {
          done = true;
          break;
        }
        if (seen.has(next.value)) continue;
        seen.add(next.value);
        if (heads.has(next.value)) page.push(next.value);
      }
      const missing = page.filter((id) => !decoded.has(id));
      if (missing.length > 0) {
        // A title exceeding the whole brief bound cannot fit even alone. Together with the
        // metadata page's payload bound, eight whole rows stay below the SDK result budget.
        const rows = await db.query(
          `SELECT id,title,payload FROM records
             WHERE id IN (SELECT value FROM json_each(?)) AND length(CAST(title AS BLOB))<=?`,
          [JSON.stringify(missing), ANALYSIS_BRIEF_BYTE_LIMIT],
        );
        const byId = new Map<string, GuestSqlRow>();
        for (const row of rows) byId.set(string(row["id"]), row);
        for (const id of missing) briefRecord(id, byId.get(id));
      }
      for (const id of page) yield decoded.get(id);
    }
  }
  /*
    WHOLE RECORDS, IN `ids` ORDER AFTER `initial`, while the brief is within its count and byte
    bounds AND the stage's prompt over it — with the sessions it would prepare — still fits.
    `crowded` names the records skipped for the prompt alone, each with the bytes the prompt
    measured with it, which is how an offer whose smallest form cannot fit is told from one whose
    record was never there to offer, and what it is refused with.
  */
  const bounded = async (
    stage: Stage,
    ids: Iterable<string>,
    initial: readonly AnalysisBriefRecord[] = [],
  ): Promise<{
    brief: AnalysisBriefRecord[];
    crowded: { readonly id: string; readonly bytes: number }[];
  }> => {
    const out = [...initial];
    const crowded: { readonly id: string; readonly bytes: number }[] = [];
    const seen = new Set(initial.map((row) => row.id));
    let bytes = encoder.encode(JSON.stringify(initial)).byteLength;
    for await (const fact of briefFacts(ids, seen, out)) {
      if (performance.now() - turnAt >= 50) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        turnAt = performance.now();
      }
      if (fact === undefined) continue;
      const record = fact.record;
      const size = fact.bytes + (out.length ? 1 : 0);
      if (bytes + size > ANALYSIS_BRIEF_BYTE_LIMIT) continue;
      const brief = [...out, record];
      const sessions = await selection(await material(brief));
      const composed = prompt.bytes(
        stage,
        brief,
        sessions.map((row) => string(row["selector"])),
      );
      if (composed > prompt.limit) {
        crowded.push({ id: record.id, bytes: composed });
        continue;
      }
      out.push(record);
      bytes += size;
    }
    return { brief: out, crowded };
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
  if (stages.includes("challenge") && wants("challenge")) {
    for (const target of heads.values()) {
      if (!wants("challenge")) break;
      if (target.kind !== "hypothesis") continue;
      const relatedIds = [...(related.get(target.id) ?? [])].sort(
        (a, b) =>
          Number(heads.get(b)!.objectionTo.length > 0) -
            Number(heads.get(a)!.objectionTo.length > 0) || a.localeCompare(b),
      );
      const { brief, crowded } = await bounded("challenge", [target.id, ...relatedIds]);
      if (!brief.some((record) => record.id === target.id)) {
        const alone = crowded.find((entry) => entry.id === target.id);
        if (alone !== undefined) {
          yield {
            missing: target.id,
            stage: "challenge",
            bound: "prompt",
            smallest: "record",
            bytes: alone.bytes,
          };
        }
        continue;
      }
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
  if (stages.includes("synthesize") && wants("synthesize")) {
    const groups = new Map<string, string[]>();
    for (const head of heads.values()) {
      if (performance.now() - turnAt >= 50) {
        await new Promise<void>((resolve) => setTimeout(resolve, 0));
        turnAt = performance.now();
      }
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
    /*
      A SYNTHESIS IS AT LEAST TWO OBSERVATIONS FROM TWO RUNS, so a record that fits alone can
      still have no synthesis that fits: every pair it is in outgrows the bound. That is told
      apart from a record too large alone, and both are said once, after every group has been
      tried — a record may pair in one group and not in another, and only one that pairs in
      none is refused. `unpaired` keeps the smallest pair each one measured.
    */
    const alone = new Map<string, number>();
    const unpaired = new Map<string, number>();
    const paired = new Set<string>();
    synthesize: for (const [key, group] of [...groups].sort(([a], [b]) => a.localeCompare(b))) {
      if (new Set(group.map((id) => heads.get(id)!.runId)).size < 2) continue;
      const pending = new Set(group);
      while (pending.size > 0) {
        if (!wants("synthesize")) break synthesize;
        const anchor = pending.values().next().value!;
        pending.delete(anchor);
        let pair: AnalysisBriefRecord[] = [];
        for (const partner of group) {
          if (heads.get(partner)!.runId === heads.get(anchor)!.runId) continue;
          const tried = await bounded("synthesize", [anchor, partner]);
          pair = tried.brief;
          if (pair.length === 2) break;
          const own = tried.crowded.find((entry) => entry.id === anchor);
          if (own !== undefined) {
            alone.set(anchor, own.bytes);
            break;
          }
          for (const entry of tried.crowded) {
            unpaired.set(anchor, Math.min(unpaired.get(anchor) ?? entry.bytes, entry.bytes));
          }
        }
        if (pair.length !== 2) continue;
        // Reserve two original runs before adding target context and both forms of critique.
        // Each subsequent window includes unoffered observations, not the same first 24 forever.
        const parents = [...new Set(pair.map((row) => heads.get(row.id)!.parent))].filter(
          (id) => heads.get(id)?.kind === "hypothesis",
        );
        let brief = pair;
        for (const parent of parents) {
          brief = (await bounded("synthesize", [parent], brief)).brief;
          if (!brief.some((row) => row.id === parent)) continue;
          const objections = (related.get(parent) ?? []).filter((id) =>
            heads.get(id)!.objectionTo.includes(parent),
          );
          brief = (await bounded("synthesize", objections, brief)).brief;
        }
        brief = (await bounded("synthesize", pending, brief)).brief;
        for (const row of brief) {
          pending.delete(row.id);
          paired.add(row.id);
        }
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
    if (!wants("synthesize")) return;
    for (const [id, bytes] of alone) {
      if (paired.has(id)) continue;
      yield { missing: id, stage: "synthesize", bound: "prompt", smallest: "record", bytes };
    }
    for (const [id, bytes] of unpaired) {
      if (paired.has(id)) continue;
      yield { missing: id, stage: "synthesize", bound: "prompt", smallest: "pair", bytes };
    }
  }
}
