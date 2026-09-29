import {
  ArchivedCitationInputSchema,
  CitationBackfillRowSchema,
  CITATION_OUTCOMES,
  CITATION_FACTS_VERSION,
  CITATION_CAPTURE_MAX_BYTES,
  JOB_OUTPUT_FILES,
  type ArchivedCitationFacts,
  type CitationFactTask,
  type CitationFactResult,
  type CitationBackfillInput,
  type CitationBackfillRow,
  type Receipt,
} from "../contract.ts";
import { checkQuote } from "../server/engine/citations.ts";
import { babelSnapshot, capturesOf } from "./archive-listing.ts";
import { inspectArchivedCitation } from "./citation-facts.ts";
import type { OutputSink } from "./output.ts";
import type { Repo, Snapshot } from "./restic.ts";
import { PREFLIGHT_DETECTORS } from "./preflight.ts";

const SNAPSHOT_LIMIT = 2048;
const CAPTURE_LIMIT = 64;
/** Total archive nodes visited across this task's snapshots, not per snapshot. */
const LISTING_LIMIT = 50_000;

function unavailable(task: CitationFactTask, reason: string): CitationFactResult {
  const check = checkQuote(task.quote ?? "", [], 0);
  return {
    status: "unavailable",
    reason,
    check:
      check.outcome === CITATION_OUTCOMES.unquoted
        ? check
        : { outcome: CITATION_OUTCOMES.unchecked, detail: reason },
    source: null,
    sourceReading: task.locator.coordinates === "raw" ? "historical-events" : "normalized-records",
    measured: null,
    position: null,
    excerpt: null,
    disclosure: {
      mode: "redact",
      detectors: PREFLIGHT_DETECTORS,
      version: CITATION_FACTS_VERSION,
      redactions: 0,
    },
  };
}

/** Never select the latest capture. Discover identities, then prove the retained raw digest. */
async function resolve(
  repo: Pick<Repo, "snapshots" | "lsTo" | "dumpTo">,
  task: CitationFactTask,
  snapshots: readonly Snapshot[],
): Promise<CitationFactResult> {
  const source = task.source;
  if (task.unavailable !== null || source === null)
    return unavailable(task, task.unavailable ?? "invalid-source");
  const selected = snapshots.filter(
    (snapshot) =>
      babelSnapshot(snapshot) &&
      snapshot.host === source.host &&
      (source.snapshotId === null || snapshot.id.startsWith(source.snapshotId)),
  );
  if (selected.length === 0) return unavailable(task, "snapshot-unavailable");
  // A retained prefix that names two immutable snapshots names neither one.
  if (source.snapshotId !== null && selected.length > 1)
    return unavailable(task, "snapshot-ambiguous");
  if (selected.length > SNAPSHOT_LIMIT) return unavailable(task, "snapshot-search-bound");

  let candidates = 0;
  let listed = 0;
  let oversized = false;
  let unreadable = false;
  let matched: ArchivedCitationFacts | null = null;
  try {
    // Equivalent archived copies select one stable source, not the newest capture.
    selected.sort((left, right) => left.id.localeCompare(right.id));
    for (const snapshot of selected) {
      if (listed >= LISTING_LIMIT) return unavailable(task, "listing-search-bound");
      const paths: { path: string; size: number }[] = [];
      await capturesOf(
        repo,
        snapshot,
        {
          entry() {
            listed++;
          },
          capture(session, node) {
            if (
              session.selector === source.selector &&
              session.harness === source.harness &&
              (source.path === null || source.path === node.path) &&
              paths.length <= CAPTURE_LIMIT
            )
              paths.push({ path: node.path, size: node.size });
          },
        },
        { maxEntries: LISTING_LIMIT - listed },
      );
      for (const { path, size } of paths) {
        if (++candidates > CAPTURE_LIMIT) return unavailable(task, "capture-search-bound");
        if (size > CITATION_CAPTURE_MAX_BYTES) {
          oversized = true;
          continue;
        }
        const parsed = ArchivedCitationInputSchema.safeParse({
          snapshotId: snapshot.id,
          path,
          label: source.label ?? snapshot.host,
          host: snapshot.host,
          harness: source.harness,
          selector: source.selector,
          captureDigest: source.captureDigest,
          sourceDigest: source.sourceDigest,
          sourceMode: source.sourceMode,
          sourceDetectors: source.sourceDetectors,
          locator: task.locator,
          quote: task.quote ?? "",
        });
        if (!parsed.success) return unavailable(task, "invalid-source");
        const result = await inspectArchivedCitation(repo, parsed.data);
        if (result.reason === "source-unavailable") {
          unreadable = true;
          continue;
        }
        if (result.measured?.captureDigest !== source.captureDigest) continue;
        if (
          matched !== null &&
          (matched.source.path !== result.source.path ||
            matched.status !== result.status ||
            matched.reason !== result.reason ||
            matched.position?.digest !== result.position?.digest ||
            matched.measured?.sourceDigest !== result.measured?.sourceDigest)
        )
          return unavailable(task, "ambiguous-source");
        matched ??= result;
      }
    }
  } catch {
    // Restic diagnostics may contain paths, secrets or transcript fragments.
    return unavailable(
      task,
      listed >= LISTING_LIMIT ? "listing-search-bound" : "archive-unavailable",
    );
  }
  return (
    matched ??
    unavailable(
      task,
      unreadable
        ? "archive-unavailable"
        : oversized
          ? "capture-size-bound"
          : candidates === 0
            ? "missing-source"
            : "capture-digest-mismatch",
    )
  );
}

/** An owner-initiated native job reads only tagged, attributed archived captures. */
export async function citationBackfill(
  input: CitationBackfillInput,
  out: OutputSink,
  archive: () => Promise<Pick<Repo, "snapshots" | "lsTo" | "dumpTo">>,
): Promise<Receipt> {
  const startedAt = new Date().toISOString();
  let repo: Pick<Repo, "snapshots" | "lsTo" | "dumpTo"> | null = null;
  let snapshots: readonly Snapshot[] = [];
  if (input.tasks.some((task) => task.unavailable === null && task.source !== null)) {
    try {
      repo = await archive();
      snapshots = await repo.snapshots();
    } catch {
      // An inaccessible archive is explicit; known historical omissions still take precedence.
      repo = null;
    }
  }
  const rows: CitationBackfillRow[] = [];
  const counts: Record<string, number> = {
    positions: input.tasks.length,
    available: 0,
    unavailable: 0,
  };
  for (const task of input.tasks) {
    const result =
      task.unavailable !== null || task.source === null
        ? unavailable(task, task.unavailable ?? "invalid-source")
        : repo === null
          ? unavailable(task, "archive-unavailable")
          : await resolve(repo, task, snapshots);
    rows.push(CitationBackfillRowSchema.parse({ task, result }));
    counts[result.status] = (counts[result.status] ?? 0) + 1;
    counts[result.check.outcome] = (counts[result.check.outcome] ?? 0) + 1;
  }
  await out.write("citationFacts", rows);
  const receipt: Receipt = {
    runId: input.runId,
    kind: "citationBackfill",
    machineId: input.machineId,
    startedAt,
    finishedAt: new Date().toISOString(),
    closure: "completed",
    counts: { ...counts, [JOB_OUTPUT_FILES.citationFacts]: rows.length },
  };
  await out.receipt(receipt);
  return receipt;
}
