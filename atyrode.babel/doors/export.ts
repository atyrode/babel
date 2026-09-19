import { defineServerAction } from "@manifold/plugin-kit/server";
import type { SqlRow } from "@manifold/plugin";
import { ACTIONS, ExportInputSchema, ExportResultSchema } from "../contract.ts";
import {
  projectRecord,
  type ExportRecord,
  type ExportSession,
} from "../server/engine/projections.ts";
import type { ActsStore } from "../store/acts.ts";
import { defineDoor, type Door } from "./door.ts";

/*
  THE DOOR A RECORD LEAVES BABEL THROUGH (§4.6, #341), and the one it does not.

  Nothing rendered a record or the ledger out of Babel, which is what "Babel drafts; the operator
  acts" depends on: a proposal he cannot carry to an issue tracker, an agent or his own notes is
  one he has to retype out of a web panel.

  IT IS A READING DOOR, and its authority is the proof of that. `containers:read` and no
  delegates: it cannot post a job, cannot call another plugin's door, cannot reach a network. So
  "nothing is published" is not a promise this file keeps — it is a capability it does not hold,
  and the host is what enforces it. The answer is a filename and its bytes, for the operator to
  save. Babel opens no issue, writes into no repository, and launches no agent at a destination.

  It is its own group rather than a tenth act in `acts.ts` or a tenth question in `read.ts`: an
  export writes nothing, so it is not an act, and it answers with a rendered document rather than
  with the store's own projections, so it is not one of the reading doors either. A reader looking
  for "what can leave" should find one file.
*/

const exportAction = defineServerAction({
  name: ACTIONS.export,
  title: "Render a record for a destination, as a file the operator takes",
  caps: ["containers:read"],
  input: ExportInputSchema,
  result: ExportResultSchema,
});

interface RecordRow extends SqlRow {
  id: string;
  kind: string;
  root_id: string;
  seq: number;
  supersedes_id: string | null;
  title: string;
  created_at: string;
  run_id: string | null;
  payload: string;
}

export function exportDoors(store: ActsStore): readonly Door[] {
  return [
    defineDoor(exportAction, async (_ctx, { id, projection }) => {
      const rows = await store.db.query<RecordRow>(
        `SELECT id, kind, root_id, seq, supersedes_id, title, created_at, run_id, payload
           FROM records WHERE id = ? LIMIT 1`,
        [id],
      );
      const row = rows[0];
      // A record this deployment does not hold is a refusal naming the identifier: an empty
      // document would be a file the operator saves and reads as a record that says nothing.
      if (row === undefined) return { refused: `no record ${id}` };
      let payload: unknown;
      try {
        payload = JSON.parse(row.payload);
      } catch {
        payload = {};
      }
      const replaced = await store.db.query<{ id: string }>(
        `SELECT id FROM records WHERE supersedes_id = ? LIMIT 1`,
        [id],
      );
      const ruling = await store.db.query<{ disposition: string }>(
        `SELECT disposition FROM dispositions WHERE record_id = ? ORDER BY seq DESC LIMIT 1`,
        [id],
      );
      // The sessions the record cites, joined the way `server/conductor.ts`'s `project()` joins
      // them: the `cites` edges are what the record reached for, and the catalog is what can say
      // where those bytes are now.
      const cited = await store.db.query<{
        selector: string;
        title: string | null;
        workspace: string | null;
        content_digest: string | null;
      }>(
        `SELECT s.selector, s.title, s.workspace, s.content_digest
           FROM edges e JOIN sessions s ON s.selector = e.to_id
          WHERE e.from_id = ? AND e.kind = 'cites' AND e.to_kind = 'session'
          ORDER BY e.position, s.selector`,
        [id],
      );
      const sessions: ExportSession[] = cited.map((session) => ({
        selector: session.selector,
        title: session.title ?? "",
        workspace: session.workspace ?? "",
        digest: session.content_digest ?? "",
      }));
      const record: ExportRecord = {
        id: row.id,
        kind: row.kind,
        rootId: row.root_id,
        seq: Number(row.seq),
        title: row.title,
        createdAt: row.created_at,
        runId: row.run_id ?? "",
        supersedesId: row.supersedes_id ?? "",
        supersededById: replaced[0]?.id ?? "",
        standing: ruling[0]?.disposition ?? "",
        payload:
          typeof payload === "object" && payload !== null && !Array.isArray(payload)
            ? (payload as Record<string, unknown>)
            : {},
        sessions,
      };
      const projected = projectRecord(record, projection);
      if ("refused" in projected) return projected;
      return {
        recordId: record.id,
        projection,
        classification: projected.classification,
        filename: projected.filename,
        contentType: "text/markdown",
        withheld: [...projected.withheld],
        text: projected.text,
      };
    }),
  ];
}
