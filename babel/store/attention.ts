import type { PluginDatabase, SqlRow } from "@manifold/plugin";
import { ObjectionGroundSchema, OPERATIONS, type PostAttention } from "../contract.ts";

/** Attention is an ordering projection, never a change to standing or evidence weight. */
export interface AttentionIndex {
  readonly records: ReadonlyMap<string, PostAttention>;
  readonly questions: ReadonlyMap<string, PostAttention>;
}

type Time = number | null;
type Sources = Map<string, Time>;
interface RecordMetadata {
  readonly root: string;
  readonly kind: string;
  readonly parent: string;
  readonly run: string;
  readonly actor: string;
  readonly at: Time;
}

const UNKNOWN: PostAttention = { at: null, basis: null };
const PAGE = 256;
const text = (value: unknown): string => (typeof value === "string" ? value : "");

/** Metadata only, in bounded pages to exhaustion. In particular there is no history cap:
 * forgetting an old citation would turn the next copy into apparently new evidence. */
async function scan(db: PluginDatabase, sql: string, visit: (row: SqlRow) => void): Promise<void> {
  let after: number | bigint = 0;
  for (;;) {
    const page: readonly (SqlRow & { cursor: number | bigint })[] = await db.query(
      `${sql} LIMIT ?`,
      [after, PAGE],
    );
    const last = page[page.length - 1];
    if (last === undefined) return;
    for (const row of page) visit(row);
    after = last.cursor;
    if (page.length < PAGE) return;
  }
}

/** Unknown first history is absorbing: a later copy cannot repair its missing date. */
function first(sources: Sources, source: string, at: Time): void {
  const previous = sources.get(source);
  sources.set(
    source,
    previous === undefined ? at : previous === null || at === null ? null : Math.min(previous, at),
  );
}

function introduced(citation: Time, relation: Time): Time {
  return citation === null || relation === null ? null : Math.max(citation, relation);
}

/** First durable introduction of each independently identified source under each claim.
 * Catalog and preparation dates are intentionally not even selected. All revisions contribute
 * history; only an explicit operator act or a new source can renew a root. */
export async function readAttention(db: PluginDatabase, nowMs: number): Promise<AttentionIndex> {
  const date = (value: unknown): Time => {
    const valueText = text(value);
    if (valueText.trim() === "") return null;
    const at = Date.parse(valueText);
    return Number.isFinite(at) && at <= nowMs ? at : null;
  };
  const records = new Map<string, PostAttention>();
  const questions = new Map<string, PostAttention>();
  const metadata = new Map<string, RecordMetadata>();
  const citations = new Map<string, Sources>();
  const relations = new Map<string, Sources>();
  const renew = (
    map: Map<string, PostAttention>,
    id: string,
    at: Time,
    basis: "evidence" | "operator" | "question",
  ): void => {
    if (at === null) return;
    const previous = map.get(id);
    if (previous === undefined) return;
    const previousAt = previous.at === null ? -Infinity : Date.parse(previous.at);
    if (at > previousAt || (at === previousAt && basis === "operator")) {
      map.set(id, { at: new Date(at).toISOString(), basis });
    }
  };
  await scan(
    db,
    `SELECT rowid AS cursor, id, root_id, kind, parent_id, run_id, actor_kind, created_at
    FROM records WHERE rowid > ? ORDER BY rowid`,
    (row) => {
      const root = text(row["root_id"]);
      const held: RecordMetadata = {
        root,
        kind: text(row["kind"]),
        parent: text(row["parent_id"]),
        run: text(row["run_id"]),
        actor: text(row["actor_kind"]),
        at: date(row["created_at"]),
      };
      metadata.set(text(row["id"]), held);
      if (!records.has(root)) records.set(root, UNKNOWN);
      if (held.actor === "operator") renew(records, root, held.at, "operator");
    },
  );
  const rootOf = (id: unknown): string =>
    metadata.get(text(id))?.root ?? (records.has(text(id)) ? text(id) : "");
  const relate = (claim: string, support: string, at: Time): void => {
    if (claim === support) return;
    let links = relations.get(claim);
    if (links === undefined) relations.set(claim, (links = new Map()));
    first(links, support, at);
  };
  // Parent membership is introduced by the original observation, not its latest revision.
  for (const held of metadata.values()) {
    const parent = metadata.get(held.parent);
    if (held.kind === "observation" && parent?.kind === "hypothesis")
      relate(parent.root, held.root, held.at);
  }

  const catalog = new Map<string, string>();
  const agentSources = new Set<string>();
  const identity = (harness: unknown, source: unknown): string => {
    const h = text(harness),
      s = text(source);
    return h !== "" && s !== "" ? JSON.stringify([h, s]) : "";
  };
  await scan(
    db,
    `SELECT rowid AS cursor, selector, harness, source_id, kind
    FROM sessions WHERE rowid > ? ORDER BY rowid`,
    (row) => {
      const source = identity(row["harness"], row["source_id"]);
      catalog.set(text(row["selector"]), source);
      if (row["kind"] === "agent") agentSources.add(source);
    },
  );

  // A retained material entry may recover a missing selector when the producing run served
  // that exact endpoint. Apply its definite identity to older citations too, so losing an old
  // run receipt cannot make a later repeat new. A selection alone is never implicit evidence.
  const recovered = new Map<string, string>();
  await scan(
    db,
    `SELECT e.rowid AS cursor, e.to_id AS selector,
      MIN(json_extract(CASE WHEN entry.type = 'object' THEN entry.value ELSE '{}' END, '$.harness')) AS harness,
      MIN(json_extract(CASE WHEN entry.type = 'object' THEN entry.value ELSE '{}' END, '$.sourceId')) AS source_id,
      COUNT(DISTINCT json_array(
        json_extract(CASE WHEN entry.type = 'object' THEN entry.value ELSE '{}' END, '$.harness'),
        json_extract(CASE WHEN entry.type = 'object' THEN entry.value ELSE '{}' END, '$.sourceId'))) AS identities
    FROM edges e JOIN records r ON r.id = e.from_id AND r.kind = e.from_kind
    JOIN runs source ON source.id = r.run_id
    JOIN runs p ON p.job_id = source.prepare_job_id
    JOIN json_each(CASE WHEN json_valid(p.payload) THEN p.payload ELSE '{}' END, '$.material.sessions') entry
    WHERE e.rowid > ? AND e.kind = 'cites' AND e.to_kind = 'session'
      AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.selector = e.to_id)
      AND p.kind = '${OPERATIONS.prepare}' AND p.closure = 'completed'
      AND json_extract(CASE WHEN entry.type = 'object' THEN entry.value ELSE '{}' END, '$.selector') = e.to_id
    GROUP BY e.rowid ORDER BY e.rowid`,
    (row) => {
      const selector = text(row["selector"]);
      const source =
        Number(row["identities"]) === 1 ? identity(row["harness"], row["source_id"]) : "";
      const previous = recovered.get(selector);
      recovered.set(selector, previous === undefined || previous === source ? source : "");
    },
  );

  await scan(
    db,
    `SELECT rowid AS cursor, id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, note, created_at
    FROM edges WHERE rowid > ? AND kind IN ('cites','consolidates','addresses','challenges','corrects','contradicts') ORDER BY rowid`,
    (row) => {
      const from = metadata.get(text(row["from_id"]));
      if (from === undefined || from.kind !== row["from_kind"]) return;
      const at = date(row["created_at"]),
        kind = text(row["kind"]);
      if (kind === "cites" && row["to_kind"] === "session") {
        const source = catalog.get(text(row["to_id"])) ?? recovered.get(text(row["to_id"]));
        if (source === undefined || source === "" || agentSources.has(source)) return;
        let held = citations.get(from.root);
        if (held === undefined) citations.set(from.root, (held = new Map()));
        first(held, source, at);
        return;
      }
      const to = metadata.get(text(row["to_id"]));
      if (to === undefined || to.kind !== row["to_kind"]) return;
      if (kind === "consolidates" || kind === "addresses") relate(from.root, to.root, at);
      else if (
        kind === "corrects" ||
        kind === "contradicts" ||
        (kind === "challenges" &&
          to.kind === "hypothesis" &&
          (from.kind === "observation" || from.kind === "hypothesis") &&
          ObjectionGroundSchema.safeParse(row["note"]).success &&
          !(from.kind === "hypothesis" && row["note"] === "evidence") &&
          row["actor_kind"] === "run" &&
          from.run !== "" &&
          row["actor_id"] === from.run)
      ) {
        // A correction/contradiction is stated in the source record itself. A later repair
        // merely indexes those words; a refinement copies the link, not an act on its target.
        // Held objections renew only when they actually reach independently sourced material.
        relate(to.root, from.root, kind === "challenges" ? at : from.at);
      }
    },
  );

  for (const root of records.keys()) {
    const held: Sources = new Map();
    const path = new Set<string>();
    const walk = (id: string, depth: number, through: Time): void => {
      if (path.has(id)) return;
      path.add(id);
      for (const [source, at] of citations.get(id) ?? [])
        first(held, source, introduced(at, through));
      if (depth < 3)
        for (const [support, at] of relations.get(id) ?? [])
          walk(support, depth + 1, introduced(at, through));
      path.delete(id);
    };
    walk(root, 0, -Infinity);
    for (const at of held.values()) renew(records, root, at, "evidence");
  }

  // These are operator ledgers by schema/door contract; opaque actor IDs need no new registry.
  await scan(
    db,
    `SELECT rowid AS cursor, record_id, recorded_at FROM dispositions WHERE rowid > ? ORDER BY rowid`,
    (row) => renew(records, rootOf(row["record_id"]), date(row["recorded_at"]), "operator"),
  );
  await scan(
    db,
    `SELECT rowid AS cursor, record_id, recorded_at FROM feedback WHERE rowid > ? ORDER BY rowid`,
    (row) => renew(records, rootOf(row["record_id"]), date(row["recorded_at"]), "operator"),
  );
  await scan(
    db,
    `SELECT rowid AS cursor, record_id, created_at FROM filings WHERE rowid > ? AND author_kind = 'operator' ORDER BY rowid`,
    (row) => renew(records, rootOf(row["record_id"]), date(row["created_at"]), "operator"),
  );
  await scan(
    db,
    `SELECT r.rowid AS cursor, a.record_id, r.recorded_at FROM next_action_rulings r JOIN next_actions a ON a.id = r.next_action_id WHERE r.rowid > ? ORDER BY r.rowid`,
    (row) => renew(records, rootOf(row["record_id"]), date(row["recorded_at"]), "operator"),
  );
  await scan(
    db,
    `SELECT rowid AS cursor, id, created_at FROM questions WHERE rowid > ? ORDER BY rowid`,
    (row) => {
      const id = text(row["id"]);
      questions.set(id, UNKNOWN);
      renew(questions, id, date(row["created_at"]), "question");
    },
  );
  await scan(
    db,
    `SELECT rowid AS cursor, question_id, recorded_at FROM answers WHERE rowid > ? ORDER BY rowid`,
    (row) => renew(questions, text(row["question_id"]), date(row["recorded_at"]), "operator"),
  );
  // Legacy events retain the actor's role in actor_id; native events identify the answer's
  // authenticated author. Other lifecycle events lack enough attribution to renew attention.
  await scan(
    db,
    `SELECT e.rowid AS cursor, e.question_id, e.recorded_at FROM question_events e WHERE e.rowid > ? AND (e.actor_id = 'operator' OR EXISTS
    (SELECT 1 FROM answers a WHERE a.question_id = e.question_id AND a.actor_id = e.actor_id
      AND a.recorded_at = e.recorded_at)) ORDER BY e.rowid`,
    (row) => renew(questions, text(row["question_id"]), date(row["recorded_at"]), "operator"),
  );
  await scan(
    db,
    `SELECT rowid AS cursor, target_kind, target_id, recorded_at FROM steering WHERE rowid > ? AND actor_kind = 'operator' ORDER BY rowid`,
    (row) => {
      if (row["target_kind"] === "record")
        renew(records, rootOf(row["target_id"]), date(row["recorded_at"]), "operator");
      if (row["target_kind"] === "question")
        renew(questions, text(row["target_id"]), date(row["recorded_at"]), "operator");
    },
  );
  return { records, questions };
}
