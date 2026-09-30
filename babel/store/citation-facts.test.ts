import { afterEach, beforeEach, expect, test } from "bun:test";
import type { SqlParam } from "@manifold/plugin";
import { OPERATIONS, type CitationFactResult, type CitationFactTask } from "../contract.ts";
import { PREFLIGHT_DETECTORS } from "../machine/preflight.ts";
import {
  appendCitationFact,
  CITATION_FACT_MAX_PAGE_SIZE,
  citationFactReport,
  planCitationFacts,
  readCitationFacts,
} from "./citation-facts.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 8, 29);
const AT = new Date(NOW).toISOString();
const CAPTURE = `sha256:${"a".repeat(64)}`;
const SOURCE = `sha256:${"b".repeat(64)}`;
const SNAPSHOT = "c".repeat(64);
const RECORD = `sha256:${"d".repeat(64)}`;
let fixture: TestStore;
beforeEach(async () => {
  fixture = await openTestStore(NOW);
});
afterEach(() => fixture.close());

function legacySource(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    host: "archived-host",
    harness: "omp",
    source_id: "session-one",
    capture_digest: CAPTURE,
    source_digest: SOURCE,
    ...over,
  };
}
async function run(id = "run", selection: readonly unknown[] = [legacySource()]): Promise<void> {
  await insert(fixture.db, "runs", {
    id,
    kind: "explore",
    started_at: AT,
    preparation: JSON.stringify({ selection }),
    payload: "{}",
  });
}
function citation(quote?: string, over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    locator: {
      path: "omp/session-one.jsonl",
      line: 2,
      byte_offset: 43,
      digest: RECORD.slice(7),
      ...(quote === undefined ? {} : { quote }),
      ...over,
    },
    note: "historical note",
  };
}
async function record(
  id: string,
  payload: Record<string, unknown>,
  over: Record<string, SqlParam> = {},
): Promise<void> {
  await insert(fixture.db, "records", {
    id,
    root_id: id,
    kind: "observation",
    run_id: "run",
    actor_kind: "run",
    actor_id: "run",
    title: id,
    created_at: AT,
    payload: JSON.stringify(payload),
    ...over,
  });
}
function available(
  task: CitationFactTask,
  outcome: "verified" | "absent" | "unquoted" = "unquoted",
): CitationFactResult {
  if (task.source === null) throw new Error("fixture needs a resolved source");
  return {
    status: "available",
    reason: null,
    check: { outcome, detail: "" },
    sourceReading: task.locator.coordinates === "raw" ? "historical-events" : "normalized-records",
    source: {
      snapshotId: task.source.snapshotId ?? SNAPSHOT,
      path: task.source.path ?? "/archive/source.jsonl",
      label: task.source.label ?? task.source.host,
      host: task.source.host,
      harness: "omp",
      selector: task.source.selector,
      captureDigest: task.source.captureDigest,
      sourceDigest: task.source.sourceDigest,
      sourceMode: task.source.sourceMode ?? "off",
      sourceDetectors:
        task.source.sourceMode === "redact" || task.source.sourceMode === "refuse"
          ? (task.source.sourceDetectors ?? PREFLIGHT_DETECTORS)
          : null,
    },
    measured: {
      captureDigest: task.source.captureDigest,
      sourceDigest: task.source.sourceDigest,
      bytes: 100,
      records: 2,
    },
    position: {
      line: 2,
      byteOffset: 43,
      byteLength: 57,
      digest: RECORD,
      ...(task.locator.coordinates === "raw"
        ? {
            raw: {
              line: task.locator.line ?? 2,
              byteOffset: task.locator.byteOffset ?? 43,
              digest: task.locator.digest?.replace(/^sha256:/, "") ?? RECORD.slice(7),
            },
          }
        : {}),
    },
    excerpt: {
      text: "newly retrieved text",
      bytes: 20,
      maxBytes: 4096,
      truncated: false,
      trust: "archived-untrusted",
    },
    disclosure: {
      mode: "redact",
      detectors: PREFLIGHT_DETECTORS,
      version: "babel.citation-facts/1",
      redactions: 0,
    },
  };
}
function unavailable(task: CitationFactTask, reason = "archive-unavailable"): CitationFactResult {
  return {
    status: "unavailable",
    reason,
    sourceReading: task.locator.coordinates === "raw" ? "historical-events" : "normalized-records",
    check: { outcome: task.quote === null ? "unquoted" : "unchecked", detail: reason },
    source: null,
    measured: null,
    position: null,
    excerpt: null,
    disclosure: {
      mode: "redact",
      detectors: PREFLIGHT_DETECTORS,
      version: "babel.citation-facts/1",
      redactions: 0,
    },
  };
}
const metadata = (attemptId: string) => ({ attemptId, createdAt: AT });

// Every fixture is synthetic SQL; no archive bindings, machine sessions or credentials are read.
test("positions retain payload field, ordinal and exact older revision; pages resume from missing rows", async () => {
  await run();
  await record("old", {
    evidence: [citation(), citation("the exact historic words")],
    counter_evidence: [citation()],
  });
  await record(
    "new",
    { supporting: [citation()], conflicting: [citation()] },
    { kind: "proposal", root_id: "old", supersedes_id: "old", seq: 1 },
  );
  const before = await fixture.db.query("SELECT * FROM records ORDER BY id");
  const planned = await planCitationFacts(fixture.db, { limit: 2 });
  expect({
    ...planned,
    tasks: planned.tasks.map(({ recordId, field, ordinal }) => ({ recordId, field, ordinal })),
  }).toEqual({
    total: 5,
    completed: 0,
    pending: 5,
    checkable: 5,
    unknown: 0,
    limit: 2,
    tasks: [
      { recordId: "new", field: "conflicting", ordinal: 0 },
      { recordId: "new", field: "supporting", ordinal: 0 },
    ],
  });
  for (const task of planned.tasks)
    await appendCitationFact(fixture.db, task, available(task), metadata("page-one"));
  const resumed = await planCitationFacts(fixture.db, { limit: 1000 });
  expect(resumed.limit).toBe(CITATION_FACT_MAX_PAGE_SIZE);
  expect(resumed.tasks.map(({ recordId, field, ordinal }) => [recordId, field, ordinal])).toEqual([
    ["old", "counter_evidence", 0],
    ["old", "evidence", 0],
    ["old", "evidence", 1],
  ]);
  expect(resumed).toMatchObject({ total: 5, pending: 3, completed: 2 });
  expect(resumed.tasks[2]).toMatchObject({
    quote: "the exact historic words",
    locator: { coordinates: "raw", line: 2, byteOffset: 43, digest: RECORD.slice(7) },
  });
  expect(await fixture.db.query("SELECT * FROM records ORDER BY id")).toEqual(before);
});

test("legacy per-run digests and host survive a null or conflicting current catalog", async () => {
  await run();
  await record("claim", { evidence: [citation()] });
  await insert(fixture.db, "sessions", {
    selector: "omp/session-one",
    host: "remapped-hub-machine",
    harness: "omp",
    source_id: "session-one",
    content_digest: "e".repeat(64),
    snapshot_id: null,
    seen_at: AT,
  });
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  expect(task).toMatchObject({
    quote: null,
    unavailable: null,
    source: {
      host: "archived-host",
      sourceId: "session-one",
      captureDigest: CAPTURE,
      sourceDigest: SOURCE,
      snapshotId: null,
      path: null,
    },
  });
  expect(task.locator.digest).not.toBe(task.source?.sourceDigest);
});

test("missing, ambiguous and invalid historical identities remain unknown, never borrowed from catalog", async () => {
  await run("ambiguous", [legacySource(), legacySource({ host: "another-host" })]);
  await run("unrelated", [legacySource({ source_id: "different-session" })]);
  await run("invalid", [legacySource({ source_digest: "not-a-digest" })]);
  await record("a", { evidence: [citation()] }, { run_id: "no-run" });
  await record("b", { evidence: [citation()] }, { run_id: "ambiguous" });
  await record("c", { evidence: [citation()] }, { run_id: "unrelated" });
  await record("d", { evidence: [citation()] }, { run_id: "invalid" });
  await record("e", { evidence: [citation(undefined, { line: 0, byte_offset: undefined })] });
  const plan = await planCitationFacts(fixture.db);
  expect(plan).toMatchObject({ total: 5, pending: 5, checkable: 0, unknown: 5 });
  expect(plan.tasks.map((task) => [task.source, task.unavailable])).toEqual([
    [null, "missing-preparation"],
    [null, "ambiguous-source"],
    [null, "missing-source"],
    [null, "invalid-source"],
    [null, "invalid-locator"],
  ]);
  await appendCitationFact(
    fixture.db,
    plan.tasks[0]!,
    unavailable(plan.tasks[0]!, "missing-preparation"),
    metadata("unknown"),
  );
  expect((await readCitationFacts(fixture.db, "a")).facts[0]?.result.check.outcome).toBe(
    "unquoted",
  );
});

test("one large archived preparation is read once per page, not duplicated per citation", async () => {
  await run("run", [legacySource({ unrelatedHistoricalMetadata: "x".repeat(150_000) })]);
  await record("claim", { evidence: Array.from({ length: 40 }, () => citation()) });
  const plan = await planCitationFacts(fixture.db);
  expect(plan).toMatchObject({ total: 40, pending: 40, checkable: 40 });
  expect(plan.tasks[0]?.source).toMatchObject({ captureDigest: CAPTURE, sourceDigest: SOURCE });
  const task = plan.tasks[0]!;
  await appendCitationFact(fixture.db, task, available(task), metadata("large-preparation"));
  expect(await citationFactReport(fixture.db)).toMatchObject({
    total: 40,
    completed: 1,
    pending: 39,
    available: 1,
  });
});

test("unavailable attempts retry explicitly and preserve history; checked absent is not unreachable", async () => {
  await run();
  await record("claim", { evidence: [citation("a genuinely historical quote")] });
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  await appendCitationFact(fixture.db, task, unavailable(task), metadata("first"));
  expect((await planCitationFacts(fixture.db)).pending).toBe(0);
  const retry = await planCitationFacts(fixture.db, { retryUnavailable: true });
  expect(retry).toMatchObject({ total: 1, pending: 1, completed: 0, checkable: 1 });
  await appendCitationFact(
    fixture.db,
    retry.tasks[0]!,
    available(task, "absent"),
    metadata("retry"),
  );
  expect((await planCitationFacts(fixture.db, { retryUnavailable: true })).pending).toBe(0);
  expect(
    (await readCitationFacts(fixture.db, "claim")).facts.map((fact) => [
      fact.attemptId,
      fact.result.status,
      fact.result.check.outcome,
    ]),
  ).toEqual([
    ["first", "unavailable", "unchecked"],
    ["retry", "available", "absent"],
  ]);
});

test("attempt replay is idempotent and conflicting replay refuses without altering an earlier fact", async () => {
  await run();
  await record("claim", { evidence: [citation()] });
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  const result = available(task);
  const first = await appendCitationFact(fixture.db, task, result, metadata("stable"));
  const replay = await appendCitationFact(fixture.db, task, result, {
    attemptId: "stable",
    createdAt: new Date(NOW + 1000).toISOString(),
  });
  expect(first.outcome).toBe("appended");
  expect(replay).toEqual({ outcome: "duplicate", fact: first.fact });
  await expect(
    appendCitationFact(fixture.db, task, unavailable(task), metadata("stable")),
  ).rejects.toThrow("fact-conflict");
  expect(await appendCitationFact(fixture.db, task, result, metadata("new-attempt"))).toEqual({
    outcome: "duplicate",
    fact: first.fact,
  });
  await expect(
    appendCitationFact(fixture.db, task, unavailable(task), metadata("downgrade")),
  ).rejects.toThrow("fact-conflict");
  expect(await readCitationFacts(fixture.db, "claim")).toEqual({
    facts: [first.fact],
    nextAfter: null,
  });
  await expect(fixture.db.run("UPDATE citation_facts SET status='unavailable'")).rejects.toThrow(
    "append-only",
  );
  await expect(fixture.db.run("DELETE FROM citation_facts")).rejects.toThrow("append-only");
});

test("changed task or preparation refuses before persistence; raw offset is never invented", async () => {
  await run();
  await record("claim", { evidence: [citation(undefined, { byte_offset: undefined })] });
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  expect(task.locator).not.toHaveProperty("byteOffset");
  await expect(
    appendCitationFact(
      fixture.db,
      { ...task, path: "different" },
      available(task),
      metadata("tampered"),
    ),
  ).rejects.toThrow("task-mismatch");
  await fixture.db.run("UPDATE runs SET preparation=? WHERE id='run'", [
    JSON.stringify({ selection: [legacySource({ capture_digest: `sha256:${"f".repeat(64)}` })] }),
  ]);
  await expect(
    appendCitationFact(fixture.db, task, available(task), metadata("stale")),
  ).rejects.toThrow("task-mismatch");
  expect(await readCitationFacts(fixture.db, "claim")).toEqual({ facts: [], nextAfter: null });
});

test.each([
  ["0001-session.jsonl", "redact"],
  ["sessions/0001-session.jsonl", "redact"],
  ["/inputs/material/sessions/0001-session.jsonl", "redact"],
  ["sessions/0001-session.jsonl", "refuse"],
])("completed material resolves %s with %s provenance without rewriting the citation", async (path, mode) => {
  await run("consumer", []);
  await fixture.db.run("UPDATE runs SET prepare_job_id='prepared-job' WHERE id='consumer'");
  await insert(fixture.db, "runs", {
    id: "prepared",
    kind: OPERATIONS.prepare,
    job_id: "prepared-job",
    closure: "completed",
    started_at: AT,
    payload: JSON.stringify({
      material: {
        sessions: [
          {
            selector: "omp/session-one",
            harness: "omp",
            sourceId: "session-one",
            file: "0001-session.jsonl",
            captureDigest: CAPTURE,
            sourceDigest: SOURCE,
            origin: { label: "archived-host", snapshotId: SNAPSHOT, path: "/archive/exact.jsonl" },
          },
        ],
      },
      preflight: { mode, detectors: PREFLIGHT_DETECTORS },
    }),
  });
  await record(
    "claim",
    {
      evidence: [
        citation(undefined, {
          path,
          digest: SOURCE,
          byte_offset: undefined,
        }),
      ],
    },
    { run_id: "consumer" },
  );
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  expect(task).toMatchObject({
    unavailable: null,
    locator: { coordinates: "normalized" },
    source: {
      snapshotId: SNAPSHOT,
      path: "/archive/exact.jsonl",
      label: "archived-host",
      sourceMode: mode,
      sourceDetectors: PREFLIGHT_DETECTORS,
    },
  });
  const before = await fixture.db.query("SELECT payload FROM records WHERE id='claim'");
  const result = available(task);
  await expect(
    appendCitationFact(
      fixture.db,
      task,
      { ...result, source: { ...result.source!, sourceMode: "off", sourceDetectors: null } },
      metadata("wrong-mode"),
    ),
  ).rejects.toThrow("invalid-fact");
  await expect(
    appendCitationFact(
      fixture.db,
      task,
      { ...result, source: { ...result.source!, sourceDetectors: "unknown/99" } },
      metadata("wrong-detectors"),
    ),
  ).rejects.toThrow("invalid-fact");
  await appendCitationFact(fixture.db, task, result, metadata("retained"));
  expect((await readCitationFacts(fixture.db, "claim")).facts[0]?.result.source).toMatchObject({
    sourceMode: mode,
    sourceDetectors: PREFLIGHT_DETECTORS,
  });
  expect(await fixture.db.query("SELECT payload FROM records WHERE id='claim'")).toEqual(before);
});

test("facts cannot convert unquoted to verified, confuse unreachable with absent, or store mismatched excerpts", async () => {
  await run();
  await record("claim", { evidence: [citation(), citation("the old quoted content")] });
  const tasks = (await planCitationFacts(fixture.db)).tasks;
  await expect(
    appendCitationFact(
      fixture.db,
      tasks[0]!,
      available(tasks[0]!, "verified"),
      metadata("invented-quote"),
    ),
  ).rejects.toThrow("invalid-fact");
  await expect(
    appendCitationFact(
      fixture.db,
      tasks[1]!,
      { ...unavailable(tasks[1]!), check: { outcome: "absent", detail: "" } },
      metadata("false-negative"),
    ),
  ).rejects.toThrow("invalid-fact");
  const result = available(tasks[0]!);
  await expect(
    appendCitationFact(
      fixture.db,
      tasks[0]!,
      { ...result, excerpt: { ...result.excerpt!, text: "not the claimed byte length" } },
      metadata("bad-bytes"),
    ),
  ).rejects.toThrow("invalid-fact");
  await expect(
    appendCitationFact(
      fixture.db,
      tasks[0]!,
      { ...result, source: { ...result.source!, snapshotId: "latest" } },
      metadata("mutable-source"),
    ),
  ).rejects.toThrow("invalid-fact");
  await expect(
    appendCitationFact(
      fixture.db,
      tasks[0]!,
      {} as CitationFactResult,
      metadata("malformed-native"),
    ),
  ).rejects.toThrow("invalid-fact");
  expect(await readCitationFacts(fixture.db, "claim")).toEqual({ facts: [], nextAfter: null });
});

test("appending facts leaves all imported counts, edges and lifecycle history untouched", async () => {
  await run();
  await record("claim", { evidence: [citation()] });
  await insert(fixture.db, "edges", {
    id: "cite",
    kind: "cites",
    from_kind: "observation",
    from_id: "claim",
    to_kind: "session",
    to_id: "omp/session-one",
    actor_kind: "run",
    actor_id: "run",
    created_at: AT,
  });
  await insert(fixture.db, "status_events", {
    id: "status",
    record_id: "claim",
    seq: 0,
    status: "untriaged",
    actor_kind: "run",
    actor_id: "run",
    recorded_at: AT,
  });
  await insert(fixture.db, "imports", {
    id: "import",
    source: "synthetic-legacy.db",
    table_name: "records",
    rows: 1,
    imported_at: AT,
  });
  const tables = ["records", "runs", "edges", "status_events", "imports"];
  const before = await Promise.all(
    tables.map((table) => fixture.db.query(`SELECT * FROM ${table}`)),
  );
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  await appendCitationFact(fixture.db, task, available(task), metadata("sidecar"));
  const after = await Promise.all(
    tables.map((table) => fixture.db.query(`SELECT * FROM ${table}`)),
  );
  expect(after).toEqual(before);
});

test("historical adapter paths resolve full source ids without treating a UUID substring as identity", async () => {
  const ompId = "-synthetic-project/2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001";
  const codexId =
    "sessions/2026/01/02/rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-000000000001.jsonl";
  await run("adapters", [
    legacySource({ source_id: ompId }),
    legacySource({ harness: "codex", source_id: codexId }),
  ]);
  await record(
    "claim",
    {
      evidence: [
        citation(undefined, { path: `/synthetic/.omp/agent/sessions/${ompId}.jsonl` }),
        citation(undefined, { path: `/synthetic/.codex/${codexId}` }),
        citation(undefined, { path: "/unrelated/prefix-session-one.jsonl" }),
      ],
    },
    { run_id: "adapters" },
  );
  const plan = await planCitationFacts(fixture.db);
  expect(plan.tasks.map((task) => [task.source?.sourceId, task.unavailable])).toEqual([
    [ompId, null],
    [codexId, null],
    [undefined, "missing-source"],
  ]);
});

test("historical event digest remains provenance while normalized retrieval has its own measured digest", async () => {
  await run();
  await record("claim", { evidence: [citation()] });
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  const result = available(task);
  const measured = { ...result.measured!, sourceDigest: `sha256:${"e".repeat(64)}` };
  await appendCitationFact(fixture.db, task, { ...result, measured }, metadata("historical"));
  const fact = (await readCitationFacts(fixture.db, "claim")).facts[0]!;
  expect(fact.result.source?.sourceDigest).toBe(SOURCE);
  expect(fact.result.measured?.sourceDigest).toBe(measured.sourceDigest);
  expect(fact.result.sourceReading).toBe("historical-events");
});

test("coverage counts only latest exact facts and exposes no source text; history pages independently", async () => {
  await run();
  await record("claim", {
    evidence: [citation(), citation("the submitted historical quote"), citation()],
  });
  const tasks = (await planCitationFacts(fixture.db)).tasks;
  await appendCitationFact(fixture.db, tasks[0]!, unavailable(tasks[0]!), metadata("unreachable"));
  await appendCitationFact(fixture.db, tasks[0]!, available(tasks[0]!), metadata("retry"));
  await appendCitationFact(
    fixture.db,
    tasks[1]!,
    unavailable(tasks[1]!, "digest-mismatch"),
    metadata("mismatch"),
  );
  expect(await citationFactReport(fixture.db)).toEqual({
    total: 3,
    completed: 2,
    pending: 1,
    available: 1,
    unavailable: 1,
    quoteOutcomes: { verified: 0, moved: 0, absent: 0, unquoted: 2, unchecked: 1 },
    unavailableReasons: { "digest-mismatch": 1 },
  });
  const first = await readCitationFacts(fixture.db, "claim", { limit: 1 });
  expect(first.facts.map((fact) => fact.attemptId)).toEqual(["unreachable"]);
  expect(first.nextAfter).toBe(first.facts[0]!.seq);
  const rest = await readCitationFacts(fixture.db, "claim", {
    after: first.nextAfter!,
    limit: 1000,
  });
  expect(rest.facts.map((fact) => fact.attemptId)).toEqual(["retry", "mismatch"]);
  expect(rest.nextAfter).toBeNull();
  await fixture.db.run("UPDATE runs SET preparation=? WHERE id='run'", [
    JSON.stringify({ selection: [legacySource({ host: "different-history" })] }),
  ]);
  expect(await planCitationFacts(fixture.db)).toMatchObject({ total: 3, pending: 3, completed: 0 });
  expect(await citationFactReport(fixture.db)).toMatchObject({
    total: 3,
    pending: 3,
    completed: 0,
    available: 0,
    unavailable: 0,
  });
});

test("plan counts past the scan page and returns only a capped restartable missing page", async () => {
  await run();
  await record("large", { evidence: Array.from({ length: 205 }, () => citation()) });
  const first = await planCitationFacts(fixture.db, { limit: 1000 });
  expect(first).toMatchObject({ total: 205, pending: 205, checkable: 205, unknown: 0, limit: 100 });
  expect(first.tasks.map((task) => task.ordinal)).toEqual(
    Array.from({ length: 100 }, (_, ordinal) => ordinal),
  );
  for (const task of first.tasks)
    await appendCitationFact(fixture.db, task, unavailable(task), metadata("first-page"));
  const second = await planCitationFacts(fixture.db, { limit: 1000 });
  expect(second).toMatchObject({ total: 205, pending: 105, completed: 100 });
  expect(second.tasks.map((task) => task.ordinal)).toEqual(
    Array.from({ length: 100 }, (_, ordinal) => ordinal + 100),
  );
});

test("legacy snapshot prefixes remain explicit constraints and never accept a different resolved capture", async () => {
  await run("prefix", [legacySource({ snapshot: SNAPSHOT.slice(0, 8) })]);
  await record("claim", { evidence: [citation()] }, { run_id: "prefix" });
  const task = (await planCitationFacts(fixture.db)).tasks[0]!;
  expect(task.source?.snapshotId).toBe(SNAPSHOT.slice(0, 8));
  const result = available(task);
  const resolved = { ...result, source: { ...result.source!, snapshotId: SNAPSHOT } };
  await appendCitationFact(fixture.db, task, resolved, metadata("resolved-prefix"));
  await expect(
    appendCitationFact(
      fixture.db,
      task,
      {
        ...resolved,
        source: { ...resolved.source, snapshotId: "e".repeat(64) },
      },
      metadata("other-prefix"),
    ),
  ).rejects.toThrow("invalid-fact");
});

test("bounded retry pages rotate past repeated unreachable positions rather than starving later attempts", async () => {
  await run();
  await record("claim", { evidence: [citation(), citation(), citation()] });
  const tasks = (await planCitationFacts(fixture.db)).tasks;
  for (const task of tasks)
    await appendCitationFact(fixture.db, task, unavailable(task), metadata("initial"));
  const first = (await planCitationFacts(fixture.db, { retryUnavailable: true, limit: 1 }))
    .tasks[0]!;
  expect(first.ordinal).toBe(0);
  await appendCitationFact(fixture.db, first, unavailable(first), metadata("still-unreachable"));
  const next = await planCitationFacts(fixture.db, { retryUnavailable: true, limit: 1 });
  expect(next.tasks.map((task) => task.ordinal)).toEqual([1]);
});

test("oversized submitted quotes remain exact but unavailable rather than truncated or relabeled unquoted", async () => {
  await run();
  const quote = "q".repeat(2049);
  await record("claim", { evidence: [citation(quote)] });
  const plan = await planCitationFacts(fixture.db);
  expect(plan).toMatchObject({ total: 1, checkable: 0, unknown: 1 });
  const task = plan.tasks[0]!;
  expect(task.quote).toBe(quote);
  expect(task.unavailable).toBe("invalid-locator");
  await appendCitationFact(
    fixture.db,
    task,
    unavailable(task, "invalid-locator"),
    metadata("oversized"),
  );
  expect((await readCitationFacts(fixture.db, "claim")).facts[0]?.result.check.outcome).toBe(
    "unchecked",
  );
});
