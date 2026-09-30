import { expect, test } from "bun:test";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import {
  JOB_OUTPUT_FILES,
  CITATION_CAPTURE_MAX_BYTES,
  CitationBackfillRowSchema,
  type CitationFactTask,
} from "../contract.ts";
import { citationBackfill } from "./citation-backfill.ts";
import { directorySink } from "./output.ts";
import type { Repo } from "./restic.ts";
import { sessionDigester } from "./session-records.ts";
import { syntheticArchive } from "./test/restic-fixture.ts";

const digest = (value: string): string =>
  new Bun.CryptoHasher("sha256").update(value).digest("hex");

// The cited revision stays immutable when the same live source changes and a newer snapshot arrives.
test("native backfill resolves one historical snapshot prefix and refuses an unavailable one", async () => {
  const archive = await syntheticArchive();
  try {
    const directory = join(archive.sessionRoot("omp"), "project");
    const path = join(directory, "synthetic-citation.jsonl");
    await mkdir(directory);
    const original = JSON.stringify({ type: "user", text: "a historical, archived quotation" });
    await writeFile(path, `${original}\n`);
    const first = await archive.snapshot("archive-host", [archive.sessionRoot("omp")]);
    const digester = sessionDigester();
    digester.write(new TextEncoder().encode(`${original}\n`));
    const measured = digester.finish();
    // A routine second backup may keep the same bytes under a different immutable snapshot.
    const identical = await archive.snapshot("archive-host", [archive.sessionRoot("omp")]);
    await writeFile(
      path,
      JSON.stringify({ type: "user", text: "a contradictory newer revision" }) + "\n",
    );
    await archive.snapshot("archive-host", [archive.sessionRoot("omp")]);

    const source = {
      host: "archive-host",
      harness: "omp" as const,
      sourceId: "project/synthetic-citation",
      selector: "omp/project/synthetic-citation",
      captureDigest: measured.captureDigest,
      sourceDigest: `sha256:${digest("a historical classified event stream")}`,
      snapshotId: first.id.slice(0, 8),
      path: null,
      label: "archive-host",
      sourceMode: "off" as const,
      sourceDetectors: null,
    };
    const task: CitationFactTask = {
      recordId: "historical-record",
      field: "evidence",
      ordinal: 0,
      citationDigest: `sha256:${digest("citation")}`,
      basisDigest: `sha256:${digest("preparation")}`,
      path,
      quote: "a historical, archived quotation",
      locator: { coordinates: "raw", line: 1, byteOffset: 0, digest: digest(original) },
      source,
      unavailable: null,
    };
    const output = join(archive.home, "citation-output");
    const receipt = await citationBackfill(
      {
        runId: "run_synthetic",
        machineId: "machine_synthetic",
        attemptId: "attempt_synthetic",
        tasks: [
          task,
          {
            ...task,
            ordinal: 1,
            source: { ...source, snapshotId: "00000000" },
          },
        ],
      },
      directorySink(output),
      async () => archive.repo,
    );
    const rows = CitationBackfillRowSchema.array().parse(
      await Bun.file(join(output, JOB_OUTPUT_FILES.citationFacts)).json(),
    );
    expect(receipt.kind).toBe("citationBackfill");
    expect(receipt.counts).toMatchObject({ available: 1, unavailable: 1 });
    expect(rows[0]?.result).toMatchObject({
      status: "available",
      sourceReading: "historical-events",
      check: { outcome: "verified" },
      source: { snapshotId: first.id, path },
    });
    expect(rows[0]?.result.excerpt?.text).toContain("a historical, archived quotation");
    expect(rows[0]?.result.measured?.sourceDigest).toBe(measured.sourceDigest);
    expect(rows[0]?.result.measured?.sourceDigest).not.toBe(source.sourceDigest);
    expect(rows[1]?.result).toMatchObject({
      status: "unavailable",
      reason: "snapshot-unavailable",
      check: { outcome: "unchecked" },
      excerpt: null,
    });
    const copies = join(archive.home, "citation-copies");
    await citationBackfill(
      {
        runId: "run_copies",
        machineId: "machine_synthetic",
        attemptId: "attempt_copies",
        tasks: [{ ...task, source: { ...source, snapshotId: null } }],
      },
      directorySink(copies),
      async () => archive.repo,
    );
    const copied = CitationBackfillRowSchema.array().parse(
      await Bun.file(join(copies, JOB_OUTPUT_FILES.citationFacts)).json(),
    );
    expect(copied[0]?.result).toMatchObject({
      status: "available",
      check: { outcome: "verified" },
      source: { snapshotId: [first.id, identical.id].sort()[0], path },
    });
    // Discovery order cannot make an unreadable competing copy disappear after a match.
    for (const failure of ["unreadable", "oversized"] as const) {
      for (const order of ["before", "after"] as const) {
        const readableId = (order === "before" ? "2" : "1").repeat(64);
        const competingId = (order === "before" ? "1" : "2").repeat(64);
        const destination = join(archive.home, `citation-${failure}-${order}`);
        const competing: Pick<Repo, "snapshots" | "lsTo" | "dumpTo"> = {
          snapshots: async () => [
            { ...first, id: readableId },
            { ...first, id: competingId },
          ],
          lsTo: async (id, sink) => {
            await sink({
              path,
              type: "file",
              size:
                id === competingId && failure === "oversized"
                  ? CITATION_CAPTURE_MAX_BYTES + 1
                  : Buffer.byteLength(original) + 1,
              modifiedAt: first.time,
            });
          },
          dumpTo: async (id, named, sink, options) => {
            if (id === readableId) return await archive.repo.dumpTo(first.id, named, sink, options);
            // Even a child that emits apparently matching bytes before failing is unavailable.
            await sink(new TextEncoder().encode(`${original}\n`));
            throw new Error("synthetic inaccessible competitor");
          },
        };
        const refused = await citationBackfill(
          {
            runId: "run_competing",
            machineId: "machine_synthetic",
            attemptId: "attempt_competing",
            tasks: [
              { ...task, source: { ...source, snapshotId: null } },
              { ...task, ordinal: 1, quote: "", source: { ...source, snapshotId: null } },
            ],
          },
          directorySink(destination),
          async () => competing,
        );
        const results = CitationBackfillRowSchema.array().parse(
          await Bun.file(join(destination, JOB_OUTPUT_FILES.citationFacts)).json(),
        );
        expect(refused.counts).toMatchObject({
          available: 0,
          unavailable: 2,
          unchecked: 1,
          unquoted: 1,
        });
        for (const [index, row] of results.entries()) {
          expect(row.result).toMatchObject({
            status: "unavailable",
            reason: failure === "unreadable" ? "archive-unavailable" : "capture-size-bound",
            check: { outcome: index === 0 ? "unchecked" : "unquoted" },
            source: null,
            excerpt: null,
          });
        }
      }
    }
    const oversized = join(archive.home, "citation-oversized");
    const oversizedRepo: Pick<Repo, "snapshots" | "lsTo" | "dumpTo"> = {
      snapshots: async () => [first],
      lsTo: async (_id, sink) => {
        await sink({
          path,
          type: "file",
          size: CITATION_CAPTURE_MAX_BYTES + 1,
          modifiedAt: "2026-09-29T00:00:00Z",
        });
      },
      dumpTo: async () => {
        throw new Error("oversized source should not be dumped");
      },
    };
    await citationBackfill(
      {
        runId: "run_oversized",
        machineId: "machine_synthetic",
        attemptId: "attempt_oversized",
        tasks: [task],
      },
      directorySink(oversized),
      async () => oversizedRepo,
    );
    const bounded = CitationBackfillRowSchema.array().parse(
      await Bun.file(join(oversized, JOB_OUTPUT_FILES.citationFacts)).json(),
    );
    expect(bounded[0]?.result).toMatchObject({
      status: "unavailable",
      reason: "capture-size-bound",
      check: { outcome: "unchecked" },
    });
    expect(await archive.locks()).toBe(0);
  } finally {
    await archive.close();
  }
}, 60_000);
