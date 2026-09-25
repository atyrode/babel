import type { Harness, SessionRow } from "../contract.ts";
import { claude, codex, omp, type Adapter } from "./adapters/index.ts";
import type { RecordSink } from "./output.ts";
import { metadataFold, userRequest } from "./recall-records.ts";
import { readNormalizedRecords } from "./session-index.ts";

/*
  WHAT A CAPTURE SAYS ABOUT ITSELF, read in the pass that seals it (#453).

  A preparation from the archive has no `scan` beside it to name its sessions, so the facts a
  catalog row carries beyond its capture — the title the harness recorded, the workspace, the
  harness's own usage totals — come out of the one pass `prepare` already makes over each
  capture. The fold reads the NORMALIZED, REDACTED stream the material is sealed from, never the
  raw bytes: a title holding a credential reaches the hub with the marker in its place, and the
  numbers are the same numbers either way, because redaction replaces spans of strings.

  The title and workspace rule is Recall's (`recall-records.ts`), spelled once for both readers
  of an archived capture; the usage rule is the adapter's own record fold. A kept reading is
  replayed through the same fold, so a session read from the cache states the same facts as the
  same session fetched.

  A fold never fails the pass it rides in. A stream it cannot frame — a record past the framer's
  bound — yields no facts at all rather than a partial total, and the row then says only which
  capture it describes.
*/

/** The facts a `sessions.json` row carries beside its capture; an absent one was not found. */
export type CaptureFacts = Pick<
  SessionRow,
  "title" | "title_provenance" | "workspace" | "cost_usd" | "total_tokens" | "turns" | "tool_errors"
>;

const ADAPTER: Record<Harness, Adapter> = { omp, codex, claude };

/**
 * A sink for one capture's normalized stream, and the facts it held. `finish` closes the sink
 * when its caller has not, so it may be asked either way.
 */
export function captureFacts(harness: Harness): {
  readonly sink: RecordSink;
  finish(): Promise<CaptureFacts>;
} {
  const metadata = metadataFold(harness);
  const usage = ADAPTER[harness].usage();
  const framed = readNormalizedRecords((_text, _position, parsed) => {
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return;
    const fields = parsed as Record<string, unknown>;
    metadata.observe(fields, userRequest(harness, fields));
    usage.record(fields);
  });
  let broken = false;
  const sink: RecordSink = {
    write(record) {
      if (broken) return;
      try {
        framed.write(record);
      } catch {
        broken = true;
      }
    },
    async close() {
      if (broken) return;
      try {
        await framed.close();
      } catch {
        broken = true;
      }
    },
  };
  return {
    sink,
    async finish() {
      await sink.close();
      if (broken) return {};
      const found = metadata.finish();
      const totals = usage.finish();
      const facts: CaptureFacts = {};
      if (found.title !== null && found.titleProvenance !== null) {
        facts.title = found.title;
        facts.title_provenance = found.titleProvenance;
      }
      if (found.workspace !== null) facts.workspace = found.workspace;
      // A total the row's schema could not hold — a log whose own arithmetic is garbage — is
      // left out rather than failing the whole row, which also names the capture.
      if (totals !== null) {
        const cost = totals.costUsd;
        if (cost !== null && Number.isFinite(cost) && cost >= 0) facts.cost_usd = cost;
        if (count(totals.totalTokens)) facts.total_tokens = totals.totalTokens;
        if (count(totals.turns)) facts.turns = totals.turns;
        if (count(totals.toolErrors)) facts.tool_errors = totals.toolErrors;
      }
      return facts;
    },
  };
}

/** Whether a total is one a row may carry: a whole, non-negative, exactly representable count. */
function count(value: number | null): value is number {
  return value !== null && Number.isSafeInteger(value) && value >= 0;
}
