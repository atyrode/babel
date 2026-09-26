import type { PluginDatabase, SqlParam, SqlStatement } from "@manifold/plugin";
import type { SessionRow } from "../contract.ts";

/*
  WHAT THE ARCHIVE SAYS A SESSION IS, AS THE STORE KEEPS IT (#453).

  `sessions.json` carries one row per session, each naming the capture that holds it — the
  snapshot, the path inside it and the label it was taken under — and, from a preparation, what
  reading that capture found (`contract.ts`, `SessionRowSchema`). This is the one function that
  writes those rows, and it writes a row only while the row names the session's current capture
  or a newer one:

  - no row yet: insert it;
  - the same observation (`archive_path`, `size` and `modified_at` equal): keep the capture the
    store already names and its `content_digest`, so a kept reading and every cache keyed on it
    stay valid, and apply the row's facts;
  - a different observation from a newer snapshot, or a row that names no capture yet (an
    imported or scanned session): move the row onto the new capture and clear `content_digest`,
    which described the old bytes. The facts stay until a reading of the new capture replaces
    them;
  - a different observation from an older snapshot: nothing. A backfill of older snapshots and a
    preparation of a capture the catalog has since moved past both land here.

  A FACT A ROW DOES NOT CARRY NEVER ERASES ONE THE STORE HOLDS. An absent title, workspace, usage
  figure or digest is "this reading found none", not "there is none". A title moves with its
  provenance, so a model-inferred title (#342) survives a reading that found no title, and a
  recorded one replaces it.

  THE HOST IS DERIVED, NEVER WRITTEN BY A MACHINE. A label is restic's `--host`, a machine's name;
  `archive_labels` is the operator's mapping of names to hub machine ids, and a row is hosted
  where the label of the capture it ends up naming is mapped. With no mapping, a row the store
  already holds keeps its host — an imported row keeps its Go host name until `rehostSessions`
  moves it — and a new one is hosted at `''`, which the hub already reads as "not a machine".

  THREE GUARDED STATEMENTS PER ROW, in one transaction per batch: the update that keeps a
  capture, the update that moves one, and the insert. Each is conditional on exactly its own case
  and returns the row it wrote, so at most one of them applies and every count below is what
  SQLite did rather than what a read beforehand predicted. A replay of the same rows is the same
  observation and writes nothing but `seen_at` and the facts it already wrote.

  Instants are compared with `julianday`, not as text: a row this function writes spells them one
  way (`CaptureInstantSchema`), but an `archive` row the store already holds carries restic's own
  offset and nanoseconds, and the two spellings do not sort together.
*/

/** What one ingestion did, case by case, and how many of its rows name a label nobody mapped. */
export interface SessionsUpserted {
  readonly inserted: number;
  readonly moved: number;
  readonly kept: number;
  /** Rows naming an older snapshot's different observation: the store was left as it was. */
  readonly ignored: number;
  /** Rows whose `archive_label` no `archive_labels` row maps, whatever became of them. */
  readonly unmapped: number;
}

/** What this needs of the store: the database, and the feed index's invalidation. */
export interface SessionsStore {
  readonly db: PluginDatabase;
  touch(): void;
}

/** Rows per batch: three statements each, under the engine's 256-statement bound. */
const ROWS_PER_BATCH = 80;
/** Labels per lookup, under the engine's 999-parameter bound. */
const LABELS_PER_QUERY = 900;

/**
 * The facts a reading found, applied without erasing what it did not find. The provenance
 * follows the title: it changes only where a title arrives with it.
 */
const FACTS =
  `title = COALESCE(?, title), ` +
  `title_provenance = CASE WHEN ? IS NULL THEN title_provenance ELSE ? END, ` +
  `workspace = COALESCE(?, workspace), ` +
  `cost_usd = COALESCE(?, cost_usd), ` +
  `total_tokens = COALESCE(?, total_tokens), ` +
  `turns = COALESCE(?, turns), ` +
  `tool_errors = COALESCE(?, tool_errors)`;

function facts(row: SessionRow): SqlParam[] {
  const title = row.title ?? null;
  return [
    title,
    title,
    title === null ? null : (row.title_provenance ?? null),
    row.workspace ?? null,
    row.cost_usd ?? null,
    row.total_tokens ?? null,
    row.turns ?? null,
    row.tool_errors ?? null,
  ];
}

/** The same observation: keep the capture already named, its digest, and the host it maps to. */
const KEEP =
  `UPDATE sessions SET ` +
  `host = COALESCE((SELECT machine_id FROM archive_labels WHERE label = sessions.archive_label), host), ` +
  `kind = ?, live = 0, content_digest = COALESCE(?, content_digest), ${FACTS}, seen_at = ? ` +
  `WHERE selector = ? AND archive_path = ? AND size = ? AND modified_at = ? ` +
  `RETURNING selector`;

/** A different observation, newer than the capture already named or where none is: move. */
const MOVE =
  `UPDATE sessions SET ` +
  `host = COALESCE((SELECT machine_id FROM archive_labels WHERE label = ?), host), ` +
  `kind = ?, live = 0, archive_label = ?, archive_path = ?, snapshot_id = ?, archived_at = ?, ` +
  `size = ?, modified_at = ?, content_digest = ?, ${FACTS}, seen_at = ? ` +
  `WHERE selector = ? AND NOT (archive_path IS ? AND size IS ? AND modified_at IS ?) ` +
  `AND (archive_path IS NULL OR julianday(archived_at) IS NULL ` +
  `OR julianday(?) > julianday(archived_at)) ` +
  `RETURNING selector`;

/** No row yet. `live` is 0 because a capture never moves. */
const INSERT =
  `INSERT INTO sessions(selector, host, harness, source_id, kind, live, archive_label, ` +
  `archive_path, snapshot_id, archived_at, size, modified_at, content_digest, title, ` +
  `title_provenance, workspace, cost_usd, total_tokens, turns, tool_errors, seen_at) ` +
  `VALUES (?, COALESCE((SELECT machine_id FROM archive_labels WHERE label = ?), ''), ?, ?, ?, 0, ` +
  `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ` +
  `ON CONFLICT(selector) DO NOTHING RETURNING selector`;

/** The three statements for one row, in the order the cases are decided: keep, move, insert. */
function statements(row: SessionRow, seenAt: string): readonly SqlStatement[] {
  const digest = row.content_digest ?? null;
  const title = row.title ?? null;
  return [
    {
      sql: KEEP,
      params: [
        row.kind,
        digest,
        ...facts(row),
        seenAt,
        row.selector,
        row.archive_path,
        row.size,
        row.modified_at,
      ],
    },
    {
      sql: MOVE,
      params: [
        row.archive_label,
        row.kind,
        row.archive_label,
        row.archive_path,
        row.snapshot_id,
        row.archived_at,
        row.size,
        row.modified_at,
        digest,
        ...facts(row),
        seenAt,
        row.selector,
        row.archive_path,
        row.size,
        row.modified_at,
        row.archived_at,
      ],
    },
    {
      sql: INSERT,
      params: [
        row.selector,
        row.archive_label,
        row.harness,
        row.source_id,
        row.kind,
        row.archive_label,
        row.archive_path,
        row.snapshot_id,
        row.archived_at,
        row.size,
        row.modified_at,
        digest,
        title,
        title === null ? null : (row.title_provenance ?? null),
        row.workspace ?? null,
        row.cost_usd ?? null,
        row.total_tokens ?? null,
        row.turns ?? null,
        row.tool_errors ?? null,
        seenAt,
      ],
    },
  ];
}

/**
 * Writes catalogued sessions, each only while it names its session's current capture or a newer
 * one. `rows` are rows `SessionRowSchema` admitted; `seenAt` is the instant this ingestion ran,
 * written as `seen_at` on every row it inserts, moves or keeps.
 */
export async function upsertSessionRows(
  store: SessionsStore,
  rows: readonly SessionRow[],
  seenAt: string,
): Promise<SessionsUpserted> {
  if (rows.length === 0) return { inserted: 0, moved: 0, kept: 0, ignored: 0, unmapped: 0 };
  const mapped = await mappedLabels(store.db, rows);
  const written = { kept: 0, moved: 0, inserted: 0 };
  for (let at = 0; at < rows.length; at += ROWS_PER_BATCH) {
    const batch = rows.slice(at, at + ROWS_PER_BATCH).flatMap((row) => statements(row, seenAt));
    const results = await store.db.batch(batch);
    results.forEach((result, index) => {
      const applied = result.length;
      if (index % 3 === 0) written.kept += applied;
      else if (index % 3 === 1) written.moved += applied;
      else written.inserted += applied;
    });
  }
  const changed = written.kept + written.moved + written.inserted;
  if (changed > 0) store.touch();
  return {
    ...written,
    ignored: rows.length - changed,
    unmapped: rows.filter((row) => !mapped.has(row.archive_label)).length,
  };
}

/** Which of these rows' labels the operator has mapped to a machine. */
async function mappedLabels(
  db: PluginDatabase,
  rows: readonly SessionRow[],
): Promise<ReadonlySet<string>> {
  const labels = [...new Set(rows.map((row) => row.archive_label))];
  const mapped = new Set<string>();
  for (let at = 0; at < labels.length; at += LABELS_PER_QUERY) {
    const asked = labels.slice(at, at + LABELS_PER_QUERY);
    const held = await db.query<{ label: string }>(
      `SELECT label FROM archive_labels WHERE label IN (${asked.map(() => "?").join(", ")})`,
      asked,
    );
    for (const row of held) mapped.add(row.label);
  }
  return mapped;
}
