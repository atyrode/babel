import type { PluginDatabase, SqlParam } from "@manifold/plugin";
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

  THREE GUARDED STATEMENTS PER DISTINCT-SESSION PAGE, in one transaction per batch: the update
  that keeps captures, the update that moves them, and the insert. Each is conditional on exactly
  its own case and returns the rows it wrote, so at most one applies per input. Duplicate selectors
  start a new page, preserving input order and the facts successive observations retain. A replay
  writes nothing but `seen_at` and the facts it already wrote.

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

/** Three statements per bounded page; duplicate selectors cannot share one page. */
const ROWS_PER_BATCH = 48;
/** Labels per lookup, under the engine's 999-parameter bound. */
const LABELS_PER_QUERY = 900;

/**
 * The facts a reading found, applied without erasing what it did not find. The provenance
 * follows the title: it changes only where a title arrives with it.
 */
const INPUTS = `WITH inputs AS MATERIALIZED (
  SELECT jsonb(value) AS document FROM json_each(?)
)`;
const FACTS =
  `title = COALESCE(json_extract(input.document,'$.title'), title), ` +
  `title_provenance = CASE WHEN json_extract(input.document,'$.title') IS NULL THEN title_provenance
    ELSE json_extract(input.document,'$.title_provenance') END, ` +
  `workspace = COALESCE(json_extract(input.document,'$.workspace'), workspace), ` +
  `cost_usd = COALESCE(json_extract(input.document,'$.cost_usd'), cost_usd), ` +
  `total_tokens = COALESCE(json_extract(input.document,'$.total_tokens'), total_tokens), ` +
  `turns = COALESCE(json_extract(input.document,'$.turns'), turns), ` +
  `tool_errors = COALESCE(json_extract(input.document,'$.tool_errors'), tool_errors)`;

/** Same observations retain their old capture and digest, including after recatalogue. */
const KEEP =
  `${INPUTS} UPDATE sessions SET ` +
  `host = COALESCE((SELECT machine_id FROM archive_labels WHERE label = sessions.archive_label), host), ` +
  `kind = json_extract(input.document,'$.kind'), live = 0,
    content_digest = COALESCE(json_extract(input.document,'$.content_digest'), content_digest),
    ${FACTS}, seen_at = ? ` +
  `FROM inputs input WHERE sessions.selector = json_extract(input.document,'$.selector')
    AND archive_path = json_extract(input.document,'$.archive_path')
    AND size = json_extract(input.document,'$.size')
    AND modified_at = json_extract(input.document,'$.modified_at')
    RETURNING selector`;

/** Different observations move only past an older capture or a row with no capture. */
const MOVE =
  `${INPUTS} UPDATE sessions SET ` +
  `host = COALESCE((SELECT machine_id FROM archive_labels
    WHERE label = json_extract(input.document,'$.archive_label')), host), ` +
  `kind = json_extract(input.document,'$.kind'), live = 0,
    archive_label = json_extract(input.document,'$.archive_label'),
    archive_path = json_extract(input.document,'$.archive_path'),
    snapshot_id = json_extract(input.document,'$.snapshot_id'),
    archived_at = json_extract(input.document,'$.archived_at'),
    size = json_extract(input.document,'$.size'),
    modified_at = json_extract(input.document,'$.modified_at'),
    content_digest = json_extract(input.document,'$.content_digest'),
    ${FACTS}, seen_at = ? ` +
  `FROM inputs input WHERE sessions.selector = json_extract(input.document,'$.selector')
    AND NOT (archive_path IS json_extract(input.document,'$.archive_path')
      AND size IS json_extract(input.document,'$.size')
      AND modified_at IS json_extract(input.document,'$.modified_at'))
    AND (archive_path IS NULL OR julianday(archived_at) IS NULL
      OR julianday(json_extract(input.document,'$.archived_at')) > julianday(archived_at))
    RETURNING selector`;

/** New rows derive their host from the archive label rather than any submitted host. */
const INSERT = `${INPUTS} INSERT INTO sessions(selector, host, harness, source_id, kind, live, archive_label,
    archive_path, snapshot_id, archived_at, size, modified_at, content_digest, title,
    title_provenance, workspace, cost_usd, total_tokens, turns, tool_errors, seen_at)
    SELECT json_extract(input.document,'$.selector'),
      COALESCE((SELECT machine_id FROM archive_labels
        WHERE label = json_extract(input.document,'$.archive_label')), ''),
      json_extract(input.document,'$.harness'), json_extract(input.document,'$.source_id'),
      json_extract(input.document,'$.kind'), 0, json_extract(input.document,'$.archive_label'),
      json_extract(input.document,'$.archive_path'), json_extract(input.document,'$.snapshot_id'),
      json_extract(input.document,'$.archived_at'), json_extract(input.document,'$.size'),
      json_extract(input.document,'$.modified_at'), json_extract(input.document,'$.content_digest'),
      json_extract(input.document,'$.title'),
      CASE WHEN json_extract(input.document,'$.title') IS NULL THEN NULL
        ELSE json_extract(input.document,'$.title_provenance') END,
      json_extract(input.document,'$.workspace'), json_extract(input.document,'$.cost_usd'),
      json_extract(input.document,'$.total_tokens'), json_extract(input.document,'$.turns'),
      json_extract(input.document,'$.tool_errors'), ?
    FROM inputs input WHERE 1 ON CONFLICT(selector) DO NOTHING RETURNING selector`;

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
  for (let at = 0; at < rows.length;) {
    const selectors = new Set<string>();
    let end = at;
    while (end < rows.length && end - at < ROWS_PER_BATCH) {
      const selector = rows[end]!.selector;
      if (selectors.has(selector)) break;
      selectors.add(selector);
      end += 1;
    }
    const params: readonly SqlParam[] = [JSON.stringify(rows.slice(at, end)), seenAt];
    const results = await store.db.batch([
      { sql: KEEP, params },
      { sql: MOVE, params },
      { sql: INSERT, params },
    ]);
    written.kept += results[0]?.length ?? 0;
    written.moved += results[1]?.length ?? 0;
    written.inserted += results[2]?.length ?? 0;
    at = end;
    // In-realm database promises resolve synchronously; an await alone never serves hub I/O.
    await new Promise<void>((resolve) => setImmediate(resolve));
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
