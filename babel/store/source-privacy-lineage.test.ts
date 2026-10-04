import { afterEach, beforeEach, expect, test } from "bun:test";
import type { SqlParam, SqlStatement } from "@manifold/plugin";
import {
  NeighborhoodQuerySchema,
  OPERATIONS,
  PRIVACY_PROJECTION_BUILDING,
  type Harness,
} from "../contract.ts";
import { rule } from "./acts.ts";
import {
  excludeSessionWhenQuiescent,
  modelPrivacyGuard,
  readExcludedRecordIds,
  readExcludedRunIds,
  sourcePrivacyCondition,
} from "./source-privacy.ts";
import { backfillSourceDependencies, sourceDependenciesReady } from "./source-dependencies.ts";
import { sourcePrivacyClosure } from "./source-taint.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

const NOW = Date.UTC(2026, 9, 3, 12);
const AT = new Date(NOW).toISOString();
const OWNER = "synthetic-lineage-owner";
const PRIVATE = "omp/synthetic-applied-plan-source";
const MODEL_VALUE = "synthetic copied model value";
let harness: TestStore;

beforeEach(async () => {
  harness = await openTestStore(NOW);
});
afterEach(() => harness.close());

async function session(harnessName: Harness, sourceId: string): Promise<string> {
  const selector = `${harnessName}/${sourceId}`;
  await insert(harness.db, "sessions", {
    selector,
    host: "synthetic-lineage-host",
    harness: harnessName,
    source_id: sourceId,
    seen_at: AT,
  });
  return selector;
}

async function run(id: string, selectors: readonly string[], open = false): Promise<void> {
  await insert(harness.db, "runs", {
    id,
    kind: OPERATIONS.explore,
    preparation: JSON.stringify({ selectors }),
    started_at: AT,
    ...(open ? {} : { closure: "completed", finished_at: AT }),
    payload: JSON.stringify(open ? { posting: true } : {}),
  });
}

async function record(
  id: string,
  kind = "observation",
  payload: Record<string, unknown> = {},
  runId?: string,
): Promise<void> {
  await insert(harness.db, "records", {
    id,
    kind,
    root_id: id,
    seq: 0,
    actor_kind: runId === undefined ? "operator" : "run",
    actor_id: runId ?? OWNER,
    ...(runId === undefined ? {} : { run_id: runId }),
    title: `Synthetic ${id}`,
    payload: JSON.stringify(payload),
    created_at: AT,
  });
}

async function entity(id: string): Promise<void> {
  await insert(harness.db, "entities", {
    id,
    kind: "project",
    name: `Synthetic ${id}`,
    canonical_id: id,
    created_by: OWNER,
    created_at: AT,
  });
}

async function plan(args: {
  id: string;
  subjectId: string;
  kind: "topic" | "backlog" | "answer";
  operation: string;
  payload: Record<string, unknown>;
  proposingRun?: string | undefined;
  state?: "open" | "applied" | "declined";
  result?: string;
}): Promise<void> {
  await insert(harness.db, "plans", {
    id: args.id,
    kind: args.kind,
    subject_kind: args.kind === "answer" ? "question" : "proposal",
    subject_id: args.subjectId,
    operation: args.operation,
    payload: JSON.stringify(args.payload),
    proposed_by_kind: args.proposingRun === undefined ? "operator" : "run",
    proposed_by_id: args.proposingRun ?? OWNER,
    state: args.state ?? "open",
    ...(args.result === undefined ? {} : { result: args.result }),
    created_at: AT,
  });
}

async function defer(id: string): Promise<void> {
  await insert(harness.db, "status_events", {
    id: `sev_${id}`,
    record_id: id,
    seq: 1,
    status: "deferred",
    actor_kind: "operator",
    actor_id: OWNER,
    recorded_at: AT,
  });
}

async function exclude(selector: string): Promise<boolean> {
  const excluded = await excludeSessionWhenQuiescent(harness.db, selector, OWNER, AT);
  harness.store.touch();
  return excluded;
}

async function context(entityId: string) {
  return await harness.store.neighborhood(NeighborhoodQuerySchema.parse({ entityId }));
}

// These are only this test's temporary synthetic ledger bytes, never an installed store.
async function retainedLedger() {
  const tables = [
    "sessions",
    "runs",
    "records",
    "entities",
    "aliases",
    "facts",
    "fact_status",
    "resolutions",
    "resolution_members",
    "filings",
    "status_events",
    "dispositions",
    "questions",
    "plans",
  ];
  return await Promise.all(
    tables.map(async (table) => await harness.db.query(`SELECT * FROM ${table} ORDER BY rowid`)),
  );
}

function insertion(table: string, row: Readonly<Record<string, SqlParam>>): SqlStatement {
  const columns = Object.keys(row);
  return {
    sql: `INSERT INTO ${table}(${columns.join(",")}) VALUES(${columns.map(() => "?").join(",")})`,
    params: columns.map((column) => row[column] as SqlParam),
  };
}

interface PrivacyProbe {
  readonly kind: "record" | "run" | "capture" | "entity";
  readonly id: string;
  readonly queries: readonly SqlStatement[];
}

function privacyProbe(kind: PrivacyProbe["kind"], id: string): PrivacyProbe {
  const table = {
    record: "records",
    run: "runs",
    capture: "transcript_map_captures",
    entity: "entities",
  }[kind];
  const condition = sourcePrivacyCondition(kind, "id");
  const queries: SqlStatement[] = [
    {
      sql: `SELECT id FROM ${table} WHERE id=? AND (${condition.sql})`,
      params: [id, ...condition.params],
    },
  ];
  if (kind !== "entity") {
    const guard = modelPrivacyGuard(
      [],
      kind === "record" ? [id] : [],
      kind === "run" ? [id] : [],
      kind === "capture" ? [id] : [],
    );
    queries.push({ sql: `SELECT ? AS id WHERE ${guard.sql}`, params: [id, ...guard.params] });
  }
  return { kind, id, queries };
}

async function expectPersistedTaint(): Promise<void> {
  const reference = sourcePrivacyClosure();
  const [persisted, recursive] = await harness.db.batch([
    { sql: "SELECT kind,id FROM source_taint ORDER BY kind,id" },
    {
      sql: `${reference.sql} SELECT kind,id FROM tainted ORDER BY kind,id`,
      params: reference.params,
    },
  ]);
  expect(persisted).toEqual(recursive);
}

// Keep the guard/reader statements reusable, and observe them before the mutation commits.
// The recursive reference supplies the expected privacy decision, not the maintained table.
async function commitPrivacyStep(
  statements: readonly SqlStatement[],
  probes: readonly PrivacyProbe[] = [],
): Promise<void> {
  const reference = sourcePrivacyClosure();
  const results = await harness.db.batch([
    ...statements,
    { sql: "SELECT kind,id FROM source_taint ORDER BY kind,id" },
    {
      sql: `${reference.sql} SELECT kind,id FROM tainted ORDER BY kind,id`,
      params: reference.params,
    },
    ...probes.flatMap(({ queries }) => queries),
  ]);
  const recursive = results[statements.length + 1]!;
  expect(results[statements.length]).toEqual(recursive);
  let index = statements.length + 2;
  for (const probe of probes) {
    const denied = recursive.some(({ kind, id }) => kind === probe.kind && id === probe.id);
    for (let query = 0; query < probe.queries.length; query++) {
      expect(results[index++]).toEqual(denied ? [] : [{ id: probe.id }]);
    }
  }
  await expectPersistedTaint();
}

test("the first durable ban activates reused reader and model guards without bypassing prospective taint", async () => {
  await run("run_first_ban", [PRIVATE]);
  await record("obs_00000060", "observation", { claim: MODEL_VALUE }, "run_first_ban");
  await record("obs_00000061", "observation", { claim: "Synthetic independent sibling" });
  const privacy = sourcePrivacyCondition("record", "id");
  const guard = modelPrivacyGuard([], [], ["run_first_ban"]);
  const subjects = async () =>
    await harness.db.query(
      `SELECT id FROM records WHERE ${privacy.sql} ORDER BY id`,
      privacy.params,
    );
  const admission = async () =>
    await harness.db.query(`SELECT 1 AS eligible WHERE ${guard.sql}`, guard.params);

  expect(await subjects()).toEqual([{ id: "obs_00000060" }, { id: "obs_00000061" }]);
  expect(await admission()).toEqual([{ eligible: 1n }]);
  expect(await readExcludedRunIds(harness.db, [PRIVATE])).toEqual(new Set(["run_first_ban"]));

  expect(await exclude(PRIVATE)).toBe(true);
  expect(await subjects()).toEqual([{ id: "obs_00000061" }]);
  expect(await admission()).toEqual([]);
});

test("a newly appended citation is quarantined in its write transaction without refreshing a taint cache", async () => {
  await run("run_projected_private", [PRIVATE]);
  await record("obs_00000070", "observation", {}, "run_projected_private");
  const guard = modelPrivacyGuard([], ["obs_00000071"]);
  expect(await exclude(PRIVATE)).toBe(true);
  expect(await harness.db.query(`SELECT 1 AS eligible WHERE ${guard.sql}`, guard.params)).toEqual([
    { eligible: 1n },
  ]);

  await record("obs_00000071", "observation", {
    evidence: [{ kind: "observation", id: "obs_00000070" }],
  });
  expect(await harness.db.query(`SELECT 1 AS eligible WHERE ${guard.sql}`, guard.params)).toEqual(
    [],
  );
  expect(await readExcludedRecordIds(harness.db)).toEqual(
    new Set(["obs_00000070", "obs_00000071"]),
  );
});

test("removing one of two mutable provenance rows keeps the shared privacy edge until both change", async () => {
  await run("run_shared_private", [PRIVATE]);
  await record("obs_00000072");
  for (const id of ["fil_shared_first", "fil_shared_second"]) {
    await insert(harness.db, "filings", {
      id,
      record_id: "obs_00000072",
      entity_id: "ent_00000072",
      rationale: "Synthetic shared provenance",
      author_kind: "run",
      author_id: "run_shared_private",
      created_at: AT,
    });
  }
  expect(await exclude(PRIVATE)).toBe(true);
  const guard = modelPrivacyGuard([], ["obs_00000072"]);
  const admission = async () =>
    await harness.db.query(`SELECT 1 AS eligible WHERE ${guard.sql}`, guard.params);
  expect(await admission()).toEqual([]);

  await harness.db.run(
    "UPDATE filings SET author_kind='operator',author_id=? WHERE id='fil_shared_first'",
    [OWNER],
  );
  expect(await admission()).toEqual([]);
  await harness.db.run(
    "UPDATE filings SET author_kind='operator',author_id=? WHERE id='fil_shared_second'",
    [OWNER],
  );
  expect(await admission()).toEqual([{ eligible: 1n }]);
});

test("a catalog-dependent reference activates when its record arrives after the ban", async () => {
  await run("run_late_record", [PRIVATE]);
  await insert(harness.db, "assessments", {
    id: "asm_late_record",
    record_id: "obs_00000073",
    revision_id: "obs_00000073",
    run_id: "run_late_record",
    role: "synthetic",
    payload: "{}",
    recorded_at: AT,
  });
  await insert(harness.db, "questions", {
    id: "qst_00000073",
    kind: "acquire-context",
    class: "curiosity",
    text: "Synthetic late record question",
    why: "Synthetic catalog arrival",
    raised_by_kind: "operator",
    raised_by_id: OWNER,
    payload: JSON.stringify({ subjects: ["obs_00000073"] }),
    created_at: AT,
  });
  expect(await exclude(PRIVATE)).toBe(true);
  const privacy = sourcePrivacyCondition("question", "id");
  const visible = async () =>
    await harness.db.query(`SELECT id FROM questions WHERE ${privacy.sql}`, privacy.params);
  expect(await visible()).toEqual([{ id: "qst_00000073" }]);
  await record("obs_00000073");
  expect(await visible()).toEqual([]);
});

test("resumable backfill fences incomplete history, covers concurrent writes and preserves rowid boundaries", async () => {
  for (const [rowid, id] of [
    ["-9223372036854775808", "run_historical_min"],
    ["9223372036854775807", "run_historical_max"],
  ]) {
    await harness.db.run(
      `INSERT INTO runs(rowid,id,kind,preparation,started_at,closure,payload)
        VALUES(CAST(? AS INTEGER),?,?,?,?,?,?)`,
      [
        rowid!,
        id!,
        OPERATIONS.explore,
        JSON.stringify({ selectors: [PRIVATE] }),
        AT,
        "completed",
        "{}",
      ],
    );
  }
  await record("obs_00000074", "observation", {}, "run_historical_min");
  await record("obs_00000075", "observation", {}, "run_historical_max");
  await record("fnd_00000076", "finding");
  await record("obs_00000077");
  expect((await harness.store.index()).posts.map(({ post }) => post.id)).toEqual(["fnd_00000076"]);
  // The old base rows predate projection installation; only its new triggers are active.
  await harness.db.run("UPDATE source_dependency_progress SET complete=0,last_rowid=NULL");
  await harness.db.run("DELETE FROM source_dependencies");
  const independent = modelPrivacyGuard([], ["fnd_00000076"]);
  const admission = async () =>
    await harness.db.query(`SELECT 1 AS eligible WHERE ${independent.sql}`, independent.params);
  expect(await admission()).toEqual([{ eligible: 1n }]);
  expect(await backfillSourceDependencies(harness.db, 0)).toBe(false);
  await expect(readExcludedRunIds(harness.db, [PRIVATE])).rejects.toThrow(
    PRIVACY_PROJECTION_BUILDING,
  );

  await run("run_during_backfill", [PRIVATE]);
  await record("obs_00000078", "observation", {}, "run_during_backfill");
  await insert(harness.db, "filings", {
    id: "fil_during_backfill",
    record_id: "obs_00000077",
    entity_id: "ent_00000077",
    rationale: "Synthetic provenance written before history is complete",
    author_kind: "run",
    author_id: "run_during_backfill",
    created_at: AT,
  });
  await insert(harness.db, "session_exclusions", {
    selector: PRIVATE,
    actor_id: OWNER,
    recorded_at: AT,
  });
  // An existing ledger activates the conservative fence, including unrelated model work.
  expect(await admission()).toEqual([]);
  expect(await harness.store.record("fnd_00000076")).toBeNull();
  expect((await harness.store.index()).posts).toEqual([]);

  expect(await backfillSourceDependencies(harness.db)).toBe(true);
  expect(await sourceDependenciesReady(harness.db)).toBe(true);
  await expectPersistedTaint();
  expect(await readExcludedRecordIds(harness.db)).toEqual(
    new Set(["obs_00000074", "obs_00000075", "obs_00000077", "obs_00000078"]),
  );
  expect(await admission()).toEqual([{ eligible: 1n }]);
  // No touch or clock advance: durable readiness invalidates the previously empty feed.
  expect((await harness.store.index()).posts.map(({ post }) => post.id)).toEqual(["fnd_00000076"]);

  await harness.db.run(
    "UPDATE filings SET author_kind='operator',author_id=? WHERE id='fil_during_backfill'",
    [OWNER],
  );
  await expectPersistedTaint();
  const previouslyFiled = modelPrivacyGuard([], ["obs_00000077"]);
  expect(
    await harness.db.query(
      `SELECT 1 AS eligible WHERE ${previouslyFiled.sql}`,
      previouslyFiled.params,
    ),
  ).toEqual([{ eligible: 1n }]);
});

test("restoring a removed progress row rebuilds bans retained while history was incomplete", async () => {
  await session("omp", "synthetic-applied-plan-source");
  await record("obs_00000180", "observation", { evidence: [{ selector: PRIVATE }] });
  await record("obs_00000181");
  expect(await harness.store.record("obs_00000180")).toMatchObject({
    post: { id: "obs_00000180" },
  });
  await harness.db.run("DELETE FROM source_dependency_progress WHERE origin='runs'");
  await insert(harness.db, "session_exclusions", {
    selector: PRIVATE,
    actor_id: OWNER,
    recorded_at: AT,
  });
  expect(await harness.store.record("obs_00000181")).toBeNull();
  await harness.db.run("INSERT INTO source_dependency_progress(origin,complete) VALUES('runs',1)");
  await expectPersistedTaint();
  expect(await harness.store.record("obs_00000180")).toBeNull();
  expect(await harness.store.record("obs_00000181")).toMatchObject({
    post: { id: "obs_00000181" },
  });
});

test("a ban keeps bare legacy records hidden when a colliding source joins the catalog", async () => {
  // OMP source IDs can contain a slash without being canonical references.
  const bare = "-synthetic-lineage/shared";
  const privateSelector = await session("omp", bare);
  await record("obs_00000001", "observation", {
    claim: "Synthetic legacy citation",
    evidence: [{ kind: "session", id: bare }],
  });
  await run("run_legacy_input", [bare]);
  await record(
    "obs_00000002",
    "observation",
    { claim: "Synthetic uncited derivative" },
    "run_legacy_input",
  );
  await record("obs_00000003", "observation", {
    claim: "Synthetic exact independent citation",
    evidence: [{ selector: `codex/${bare}` }],
  });

  expect(await harness.store.record("obs_00000001")).toMatchObject({
    post: { id: "obs_00000001" },
  });
  expect(await exclude(privateSelector)).toBe(true);
  expect(await readExcludedRecordIds(harness.db)).toEqual(
    new Set(["obs_00000001", "obs_00000002"]),
  );
  expect(await harness.store.record("obs_00000001")).toBeNull();

  const beforeGrowth = await retainedLedger();
  await session("codex", bare);
  const afterGrowth = await retainedLedger();
  expect(afterGrowth[0]?.[0]).toEqual(beforeGrowth[0]?.[0]);
  expect(afterGrowth.slice(1)).toEqual(beforeGrowth.slice(1));
  expect(await readExcludedRecordIds(harness.db)).toEqual(
    new Set(["obs_00000001", "obs_00000002"]),
  );
  expect(await readExcludedRunIds(harness.db)).toEqual(new Set(["run_legacy_input"]));
  expect(await harness.store.record("obs_00000001")).toBeNull();
  expect(await harness.store.record("obs_00000002")).toBeNull();
  expect(await harness.store.record("obs_00000003")).toMatchObject({
    post: { id: "obs_00000003" },
  });
  const guard = modelPrivacyGuard([], ["obs_00000003"]);
  expect(await harness.db.query(`SELECT 1 AS eligible WHERE ${guard.sql}`, guard.params)).toEqual([
    { eligible: 1n },
  ]);
});

test("ambiguous bare source work blocks either matching ban and leaves unknown reservations intact", async () => {
  const bare = "synthetic-ambiguous-work";
  const omp = await session("omp", bare);
  const codex = await session("codex", bare);
  await run("run_ambiguous_open", [bare], true);
  await run("run_unrelated_open", ["claude/synthetic-unrelated-open"], true);
  const originals = await retainedLedger();

  expect(await readExcludedRunIds(harness.db, [omp])).toEqual(new Set(["run_ambiguous_open"]));
  expect(await readExcludedRunIds(harness.db, [codex])).toEqual(new Set(["run_ambiguous_open"]));
  expect(await exclude(omp)).toBe(false);
  expect(await exclude(codex)).toBe(false);
  expect(await exclude("omp/synthetic-independent-ban")).toBe(true);
  expect(await retainedLedger()).toEqual(originals);
  expect(
    await harness.db.query(
      "SELECT id, closure, json_extract(payload, '$.posting') AS posting FROM runs ORDER BY id",
    ),
  ).toEqual([
    { id: "run_ambiguous_open", closure: null, posting: 1n },
    { id: "run_unrelated_open", closure: null, posting: 1n },
  ]);
});

test("an exact canonical open run stays eligible for unrelated colliding bans even before catalog arrival", async () => {
  const bare = "synthetic-exact-work";
  const privateSelector = await session("omp", bare);
  const canonical = `codex/${bare}`;
  // A legacy source ID that happens to spell another harness's canonical selector is not it.
  const misleading = await session("claude", canonical);
  await run("run_exact_open", [canonical], true);
  const originals = await retainedLedger();

  expect(await exclude(misleading)).toBe(true);
  expect(await exclude(privateSelector)).toBe(true);
  expect(await readExcludedRunIds(harness.db)).toEqual(new Set());
  const guard = modelPrivacyGuard([], [], ["run_exact_open"]);
  expect(await harness.db.query(`SELECT 1 AS eligible WHERE ${guard.sql}`, guard.params)).toEqual([
    { eligible: 1n },
  ]);
  expect(await retainedLedger()).toEqual(originals);
  await session("codex", bare);
  expect(await readExcludedRunIds(harness.db)).toEqual(new Set());
  expect(await harness.db.query("SELECT closure FROM runs WHERE id = 'run_exact_open'")).toEqual([
    { closure: null },
  ]);
});

for (const origin of ["proposal", "proposing-run"] as const) {
  test(`accepting a promotion preserves ${origin} provenance despite operator fact authority`, async () => {
    await run("run_private_proposer", [PRIVATE]);
    await record(
      "pro_00000010",
      "proposal",
      {},
      origin === "proposal" ? "run_private_proposer" : undefined,
    );
    await record("hyp_00000010", "hypothesis", { problem: "Synthetic independent candidate" });
    await defer("hyp_00000010");
    await entity("ent_00000010");
    await entity("ent_00000011");
    await plan({
      id: "pln_promotion",
      subjectId: "pro_00000010",
      kind: "backlog",
      operation: "promote",
      proposingRun: origin === "proposing-run" ? "run_private_proposer" : undefined,
      payload: {
        reasoning: "Synthetic copied proposal reasoning",
        hypotheses: ["hyp_00000010"],
        fact: {
          entityId: "ent_00000010",
          predicate: "owner",
          value: MODEL_VALUE,
          note: "Synthetic model note",
        },
      },
    });

    const accepted = await rule(
      harness.store,
      { id: "pro_00000010", ruling: "accept", note: "Synthetic acceptance" },
      OWNER,
    );
    expect(accepted.plan).toMatchObject({
      operation: "promote",
      applied: true,
      entityId: "ent_00000010",
    });
    expect((await context("ent_00000010")).facts).toMatchObject([
      {
        entityId: "ent_00000010",
        value: MODEL_VALUE,
        authorityKind: "operator",
        authorityId: OWNER,
      },
    ]);
    const originals = await retainedLedger();

    expect(await exclude(PRIVATE)).toBe(true);
    expect((await context("ent_00000010")).state).toBe("missing");
    expect((await context("ent_00000010")).facts).toEqual([]);
    expect((await context("ent_00000011")).nodes.map(({ id }) => id)).toEqual(["ent_00000011"]);
    expect(await harness.store.record("hyp_00000010")).toBeNull();
    expect(await retainedLedger()).toEqual(originals);
  });
}

test("accepting an operator-attributed split quarantines copied names, resolution reasoning and heuristic filings", async () => {
  await run("run_split_source", [PRIVATE]);
  await record("pro_00000020", "proposal", {}, "run_split_source");
  await record("obs_00000020", "observation", { claim: "Synthetic independent filed record" });
  await entity("ent_00000020");
  await entity("ent_00000021");
  await plan({
    id: "pln_split",
    subjectId: "pro_00000020",
    kind: "topic",
    operation: "split",
    payload: {
      name: "Synthetic model-derived part",
      reasoning: MODEL_VALUE,
      targets: ["ent_00000020"],
      binding: [{ predicate: "description", value: MODEL_VALUE }],
      records: [{ id: "obs_00000020", rationale: MODEL_VALUE }],
    },
  });
  const accepted = await rule(
    harness.store,
    { id: "pro_00000020", ruling: "accept", note: "Synthetic acceptance" },
    OWNER,
  );
  expect(accepted.plan).toMatchObject({ operation: "split", applied: true });
  const partId = accepted.plan?.entityId ?? "";
  expect((await context(partId)).nodes).toMatchObject([
    { id: partId, name: "Synthetic model-derived part" },
  ]);
  expect(await harness.db.query("SELECT reason, actor_id FROM resolutions")).toEqual([
    { reason: MODEL_VALUE, actor_id: OWNER },
  ]);
  expect(await harness.db.query("SELECT rationale, author_kind, heuristic FROM filings")).toEqual([
    { rationale: MODEL_VALUE, author_kind: "heuristic", heuristic: 1n },
  ]);
  const originals = await retainedLedger();

  expect(await exclude(PRIVATE)).toBe(true);
  expect((await context(partId)).state).toBe("missing");
  expect((await context("ent_00000020")).state).toBe("missing");
  expect(await harness.store.record("obs_00000020")).toBeNull();
  expect((await context("ent_00000021")).nodes.map(({ id }) => id)).toEqual(["ent_00000021"]);
  expect(await retainedLedger()).toEqual(originals);
});

test("accepted consolidation keeps copied operator status reasons connected to the settled records", async () => {
  await run("run_consolidation_source", [PRIVATE]);
  await record("pro_00000030", "proposal", {}, "run_consolidation_source");
  await record("hyp_00000030", "hypothesis", { problem: "Synthetic deferred candidate" });
  await defer("hyp_00000030");
  await record("obs_00000031", "observation", { claim: "Synthetic independent sibling" });
  await plan({
    id: "pln_consolidation",
    subjectId: "pro_00000030",
    kind: "backlog",
    operation: "consolidate",
    payload: { reasoning: MODEL_VALUE, hypotheses: ["hyp_00000030"] },
  });
  const accepted = await rule(
    harness.store,
    { id: "pro_00000030", ruling: "accept", note: "Synthetic acceptance" },
    OWNER,
  );
  expect(accepted.plan).toMatchObject({ operation: "consolidate", applied: true });
  expect(
    await harness.db.query("SELECT status, reason, actor_kind FROM status_events WHERE seq = 2"),
  ).toEqual([{ status: "promoted", reason: MODEL_VALUE, actor_kind: "operator" }]);
  expect(await harness.store.record("hyp_00000030")).toMatchObject({
    post: { id: "hyp_00000030" },
  });
  const originals = await retainedLedger();

  expect(await exclude(PRIVATE)).toBe(true);
  expect(await harness.store.record("hyp_00000030")).toBeNull();
  expect(await harness.store.record("obs_00000031")).toMatchObject({
    post: { id: "obs_00000031" },
  });
  expect(await retainedLedger()).toEqual(originals);
});

test("an imported accepted topic's raw entity result retains proposal lineage", async () => {
  await record("pro_00000040", "proposal", { evidence: [{ selector: PRIVATE }] });
  await entity("ent_00000040");
  await entity("ent_00000041");
  await plan({
    id: "pro_00000040",
    subjectId: "pro_00000040",
    kind: "topic",
    operation: "create",
    payload: {
      name: "Synthetic imported proposal name",
      entity_kind: "project",
      evidence_weight: 1,
    },
    state: "applied",
    result: "ent_00000040",
  });
  expect((await context("ent_00000040")).nodes.map(({ id }) => id)).toEqual(["ent_00000040"]);
  const originals = await retainedLedger();

  expect(await exclude(PRIVATE)).toBe(true);
  expect((await context("ent_00000040")).state).toBe("missing");
  expect((await context("ent_00000041")).nodes.map(({ id }) => id)).toEqual(["ent_00000041"]);
  expect(await retainedLedger()).toEqual(originals);
});

test("imported answer action fact results retain question lineage without a run attribution or result document", async () => {
  await insert(harness.db, "questions", {
    id: "qst_00000050",
    kind: "acquire-context",
    class: "curiosity",
    text: "Synthetic imported question",
    why: "Synthetic imported source context",
    raised_by_kind: "operator",
    raised_by_id: OWNER,
    payload: JSON.stringify({ subjects: [PRIVATE] }),
    created_at: AT,
  });
  await entity("ent_00000050");
  await entity("ent_00000051");
  await insert(harness.db, "facts", {
    id: "fct_imported_action",
    entity_id: "ent_00000050",
    predicate: "owner",
    value: MODEL_VALUE,
    valid_from: AT,
    observed_at: AT,
    authority_kind: "operator",
    authority_id: OWNER,
    recorded_at: AT,
  });
  // Exactly the retained shape emitted by tools/import.ts, not a new plan-storage format.
  await insert(harness.db, "plans", {
    id: "pln_imported_answer",
    kind: "answer",
    subject_kind: "question",
    subject_id: "qst_00000050",
    operation: "assert-fact",
    payload: JSON.stringify({
      schema: 1,
      answer_id: "ans_synthetic_import",
      interpreter_version: 1,
      actions: [
        {
          id: "pac_synthetic_import",
          position: 0,
          kind: "assert-fact",
          state: "applied",
          result_id: "fct_imported_action",
          applied_at: AT,
          payload: {},
        },
      ],
    }),
    proposed_by_kind: "engine",
    proposed_by_id: "interpreter/1",
    state: "applied",
    ruled_by: OWNER,
    ruled_at: AT,
    result: null,
    created_at: AT,
  });
  expect((await context("ent_00000050")).facts).toMatchObject([
    { value: MODEL_VALUE, authorityKind: "operator" },
  ]);
  const originals = await retainedLedger();

  expect(await exclude(PRIVATE)).toBe(true);
  expect((await context("ent_00000050")).state).toBe("missing");
  expect((await context("ent_00000051")).nodes.map(({ id }) => id)).toEqual(["ent_00000051"]);
  expect(await retainedLedger()).toEqual(originals);
});

for (const seed of [0x541, 0xa11ce]) {
  test(`persisted taint matches recursive privacy through seeded mutable graph writes (${seed})`, async () => {
    let random = seed;
    const choose = (count: number) => {
      random = (Math.imul(random, 1664525) + 1013904223) >>> 0;
      return (random >>> 16) % count;
    };
    const runIds = Array.from({ length: 8 }, (_, index) => `run_random_${index}`);
    const recordIds = Array.from(
      { length: 8 },
      (_, index) => `obs_${(100 + index).toString(16).padStart(8, "0")}`,
    );
    const probes = recordIds.map((id) => privacyProbe("record", id));
    const activeRuns = new Set(runIds);
    const activeQuestions = new Set<string>();
    const runRow = (id: string, preparation: Record<string, unknown>) =>
      insertion("runs", {
        id,
        kind: OPERATIONS.explore,
        preparation: JSON.stringify(preparation),
        started_at: AT,
        finished_at: AT,
        closure: "completed",
        payload: "{}",
      });
    const questionRow = (id: string, subject: string, runId: string) =>
      insertion("questions", {
        id,
        kind: "acquire-context",
        class: "curiosity",
        text: "Synthetic randomized question",
        why: "Synthetic mutable graph",
        raised_by_kind: "run",
        raised_by_id: runId,
        payload: JSON.stringify({ subjects: [subject] }),
        created_at: AT,
      });
    await commitPrivacyStep(
      [
        runRow("run_random_source", { selectors: [PRIVATE] }),
        ...runIds.map((id, index) =>
          runRow(id, index < 3 ? { review: { titleRunIds: [runIds[(index + 2) % 3]] } } : {}),
        ),
        ...recordIds.map((id, index) =>
          insertion("records", {
            id,
            kind: "observation",
            root_id: id,
            seq: 0,
            actor_kind: "run",
            actor_id: runIds[index]!,
            run_id: runIds[index]!,
            title: "Synthetic randomized descendant",
            payload: "{}",
            created_at: AT,
          }),
        ),
        insertion("entities", {
          id: "ent_00000064",
          kind: "project",
          name: "Synthetic shared filing target",
          canonical_id: "ent_00000064",
          created_by: OWNER,
          created_at: AT,
        }),
        ...["fil_random_first", "fil_random_second"].map((id) =>
          insertion("filings", {
            id,
            record_id: recordIds[0]!,
            entity_id: "ent_00000064",
            rationale: "Synthetic shared source",
            author_kind: "run",
            author_id: "run_random_source",
            created_at: AT,
          }),
        ),
      ],
      probes,
    );
    await commitPrivacyStep(
      [insertion("session_exclusions", { selector: PRIVATE, actor_id: OWNER, recorded_at: AT })],
      probes,
    );
    expect(await readExcludedRecordIds(harness.db)).toEqual(new Set(recordIds.slice(0, 3)));
    expect(await harness.store.record(recordIds[2]!)).toBeNull();

    // The duplicate provenance row must survive removal of the first support.
    await commitPrivacyStep(
      [
        {
          sql: "UPDATE filings SET author_kind='operator',author_id=? WHERE id='fil_random_first'",
          params: [OWNER],
        },
      ],
      probes,
    );
    expect(await harness.store.record(recordIds[2]!)).toBeNull();
    await commitPrivacyStep(
      [
        {
          sql: "UPDATE filings SET author_kind='operator',author_id=? WHERE id='fil_random_second'",
          params: [OWNER],
        },
      ],
      probes,
    );
    expect(await readExcludedRecordIds(harness.db)).toEqual(new Set());
    expect(await harness.store.record(recordIds[2]!)).toMatchObject({ post: { id: recordIds[2] } });

    // A new tainted input must reach pre-existing descendants and their cycle.
    await commitPrivacyStep(
      [
        {
          sql: "UPDATE runs SET preparation=? WHERE id=?",
          params: [
            JSON.stringify({ review: { titleRunIds: [runIds[2], "run_random_source"] } }),
            runIds[0]!,
          ],
        },
      ],
      probes,
    );
    expect(await readExcludedRecordIds(harness.db)).toEqual(new Set(recordIds.slice(0, 3)));
    await commitPrivacyStep(
      [
        {
          sql: "UPDATE runs SET preparation=? WHERE id=?",
          params: [JSON.stringify({ review: { titleRunIds: [runIds[2]] } }), runIds[0]!],
        },
      ],
      probes,
    );
    expect(await readExcludedRecordIds(harness.db)).toEqual(new Set());

    for (let step = 0; step < 56; step++) {
      const slot = choose(runIds.length);
      const id = runIds[slot]!;
      const questionId = `qst_random_${choose(4)}`;
      const preparation = {
        selectors: choose(3) === 0 ? [PRIVATE] : ["codex/synthetic-unrelated-random"],
        review: { titleRunIds: [runIds[choose(runIds.length)], runIds[choose(runIds.length)]] },
      };
      switch (step % 7) {
        case 0:
        case 1:
          if (activeRuns.has(id)) {
            await commitPrivacyStep(
              [
                {
                  sql: "UPDATE runs SET preparation=?,payload=? WHERE id=?",
                  params: [
                    JSON.stringify(preparation),
                    JSON.stringify(
                      choose(2) === 0 ? {} : { review: { titleRunIds: ["run_random_source"] } },
                    ),
                    id,
                  ],
                },
              ],
              probes,
            );
          } else {
            await commitPrivacyStep([runRow(id, preparation)], probes);
            activeRuns.add(id);
          }
          break;
        case 2:
          if (activeRuns.has(id)) {
            await commitPrivacyStep([{ sql: "DELETE FROM runs WHERE id=?", params: [id] }], probes);
            activeRuns.delete(id);
          } else {
            await commitPrivacyStep([runRow(id, preparation)], probes);
            activeRuns.add(id);
          }
          break;
        case 3: {
          const subject = choose(2) === 0 ? recordIds[choose(recordIds.length)]! : PRIVATE;
          if (activeQuestions.has(questionId)) {
            await commitPrivacyStep(
              [
                {
                  sql: "UPDATE questions SET payload=?,raised_by_id=? WHERE id=?",
                  params: [JSON.stringify({ subjects: [subject] }), id, questionId],
                },
              ],
              probes,
            );
          } else {
            await commitPrivacyStep([questionRow(questionId, subject, id)], probes);
            activeQuestions.add(questionId);
          }
          break;
        }
        case 4:
          if (activeQuestions.has(questionId)) {
            await commitPrivacyStep(
              [{ sql: "DELETE FROM questions WHERE id=?", params: [questionId] }],
              probes,
            );
            activeQuestions.delete(questionId);
          } else {
            await commitPrivacyStep([questionRow(questionId, recordIds[slot]!, id)], probes);
            activeQuestions.add(questionId);
          }
          break;
        case 5:
          await commitPrivacyStep(
            [
              {
                sql: "UPDATE filings SET record_id=?,author_kind=?,author_id=? WHERE id=?",
                params: [
                  recordIds[slot]!,
                  choose(2) === 0 ? "operator" : "run",
                  id,
                  choose(2) === 0 ? "fil_random_first" : "fil_random_second",
                ],
              },
            ],
            probes,
          );
          break;
        case 6:
          await commitPrivacyStep(
            [
              {
                sql: "UPDATE entities SET name=? WHERE id='ent_00000064'",
                params: [`Synthetic unrelated rename ${step}`],
              },
              {
                sql: "UPDATE runs SET cost_usd=? WHERE id='run_random_source'",
                params: [step / 1000],
              },
            ],
            probes,
          );
          break;
      }
    }
  });
}

test("persisted taint follows delayed sessions, title history and mutable prepare bindings", async () => {
  const bare = "synthetic-delayed-live-source";
  const selector = `omp/${bare}`;
  const titled = "codex/synthetic-live-title";
  const job = "job_synthetic_live_prepare";
  const probes = [
    privacyProbe("record", "obs_00000080"),
    privacyProbe("record", "obs_00000081"),
    privacyProbe("record", "obs_00000082"),
    privacyProbe("record", "obs_00000083"),
  ];
  await commitPrivacyStep(
    [
      insertion("runs", {
        id: "run_live_source",
        kind: OPERATIONS.explore,
        preparation: JSON.stringify({ selectors: [selector] }),
        started_at: AT,
        closure: "completed",
        payload: "{}",
      }),
      insertion("runs", {
        id: "run_live_producer",
        kind: OPERATIONS.prepare,
        started_at: AT,
        closure: "completed",
        payload: JSON.stringify({ material: { sessions: [{ selector }] } }),
      }),
      insertion("runs", {
        id: "run_live_consumer",
        kind: OPERATIONS.explore,
        prepare_job_id: job,
        started_at: AT,
        closure: "completed",
        payload: "{}",
      }),
      insertion("runs", {
        id: "run_live_title",
        kind: OPERATIONS.explore,
        preparation: JSON.stringify({
          material: { sessions: [{ selector: titled, title: MODEL_VALUE }] },
        }),
        started_at: AT,
        closure: "completed",
        payload: "{}",
      }),
      ...[
        ["obs_00000080", "run_live_consumer"],
        ["obs_00000081", "run_live_title"],
        ["obs_00000082", null],
        ["obs_00000083", null],
      ].map(([id, runId]) =>
        insertion("records", {
          id: id!,
          kind: "observation",
          root_id: id!,
          actor_kind: runId === null ? "operator" : "run",
          actor_id: runId ?? OWNER,
          run_id: runId ?? null,
          title: "Synthetic live join descendant",
          payload: JSON.stringify(
            id === "obs_00000083" ? { evidence: [{ kind: "session", id: bare }] } : {},
          ),
          created_at: AT,
        }),
      ),
      insertion("sessions", {
        selector: titled,
        harness: "codex",
        source_id: "synthetic-live-title",
        host: "synthetic-lineage-host",
        title: MODEL_VALUE,
        title_provenance: "recorded",
        seen_at: AT,
      }),
      insertion("edges", {
        id: "edg_live_current_title",
        kind: "cites",
        from_kind: "observation",
        from_id: "obs_00000082",
        to_kind: "session",
        to_id: titled,
        actor_kind: "operator",
        actor_id: OWNER,
        created_at: AT,
      }),
    ],
    probes,
  );
  await commitPrivacyStep(
    [insertion("session_exclusions", { selector, actor_id: OWNER, recorded_at: AT })],
    probes,
  );
  expect(await readExcludedRecordIds(harness.db)).toEqual(new Set());
  await commitPrivacyStep(
    [{ sql: "UPDATE runs SET job_id=? WHERE id='run_live_producer'", params: [job] }],
    probes,
  );
  expect(await harness.store.record("obs_00000080")).toBeNull();
  await commitPrivacyStep(
    [{ sql: "UPDATE runs SET prepare_job_id=NULL WHERE id='run_live_consumer'" }],
    probes,
  );
  expect(await harness.store.record("obs_00000080")).not.toBeNull();
  await commitPrivacyStep(
    [
      { sql: "UPDATE runs SET prepare_job_id=? WHERE id='run_live_consumer'", params: [job] },
      { sql: "UPDATE runs SET payload='{}' WHERE id='run_live_producer'" },
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000080")).not.toBeNull();
  await commitPrivacyStep(
    [
      {
        sql: "UPDATE runs SET payload=? WHERE id='run_live_producer'",
        params: [JSON.stringify({ material: { sessions: [{ selector }] } })],
      },
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000080")).toBeNull();

  await commitPrivacyStep(
    [
      insertion("sessions", {
        selector,
        harness: "omp",
        source_id: bare,
        host: "synthetic-lineage-host",
        seen_at: AT,
      }),
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000083")).toBeNull();
  await commitPrivacyStep(
    [{ sql: "DELETE FROM sessions WHERE selector=?", params: [selector] }],
    probes,
  );
  expect(await harness.store.record("obs_00000083")).not.toBeNull();
  await commitPrivacyStep(
    [
      insertion("sessions", {
        selector,
        harness: "omp",
        source_id: bare,
        host: "synthetic-lineage-host",
        seen_at: AT,
      }),
      insertion("session_titles", {
        selector: titled,
        title: MODEL_VALUE,
        run_id: "run_live_source",
        inferred_at: AT,
      }),
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000081")).toBeNull();
  expect(await harness.store.record("obs_00000082")).not.toBeNull();
  await commitPrivacyStep(
    [{ sql: "UPDATE sessions SET title_provenance='inferred' WHERE selector=?", params: [titled] }],
    probes,
  );
  expect(await harness.store.record("obs_00000082")).toBeNull();
  await commitPrivacyStep(
    [
      {
        sql: "UPDATE session_titles SET title=? WHERE selector=?",
        params: ["Synthetic replacement title", titled],
      },
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000081")).not.toBeNull();
  expect(await harness.store.record("obs_00000082")).not.toBeNull();
  await commitPrivacyStep(
    [{ sql: "UPDATE session_titles SET title=? WHERE selector=?", params: [MODEL_VALUE, titled] }],
    probes,
  );
  await commitPrivacyStep(
    [
      {
        sql: "UPDATE session_titles SET run_id='run_live_title' WHERE selector=?",
        params: [titled],
      },
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000081")).not.toBeNull();
  expect(await harness.store.record("obs_00000082")).not.toBeNull();
  await commitPrivacyStep(
    [{ sql: "DELETE FROM session_titles WHERE selector=?", params: [titled] }],
    probes,
  );
  await commitPrivacyStep(
    [
      insertion("session_titles", {
        selector: titled,
        title: MODEL_VALUE,
        run_id: "run_live_source",
        inferred_at: AT,
      }),
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000082")).toBeNull();
});

test("persisted taint follows shared map bindings and replaced applied-output catalogs", async () => {
  const probes = [
    privacyProbe("capture", "cap_synthetic_public"),
    privacyProbe("record", "obs_00000090"),
    privacyProbe("entity", "ent_00000090"),
    privacyProbe("entity", "ent_00000091"),
  ];
  await commitPrivacyStep(
    [
      insertion("runs", {
        id: "run_map_source",
        kind: OPERATIONS.explore,
        preparation: JSON.stringify({ selectors: [PRIVATE] }),
        started_at: AT,
        closure: "completed",
        payload: "{}",
      }),
      insertion("records", {
        id: "pro_00000090",
        kind: "proposal",
        root_id: "pro_00000090",
        actor_kind: "run",
        actor_id: "run_map_source",
        run_id: "run_map_source",
        title: "Synthetic applied map source",
        payload: "{}",
        created_at: AT,
      }),
      insertion("records", {
        id: "obs_00000090",
        kind: "observation",
        root_id: "obs_00000090",
        actor_kind: "operator",
        actor_id: OWNER,
        title: "Synthetic map descendant",
        payload: JSON.stringify({ captureId: "cap_synthetic_public" }),
        created_at: AT,
      }),
      ...["private", "public"].flatMap((label) => [
        insertion("transcript_map_captures", {
          id: `cap_synthetic_${label}`,
          host: "synthetic-lineage-host",
          harness: "omp",
          session: label === "private" ? PRIVATE : "omp/synthetic-independent-map",
          captured_at: AT,
          payload: "{}",
        }),
        insertion("transcript_map_plans", {
          id: `map_synthetic_${label}`,
          capture_id: `cap_synthetic_${label}`,
          payload: "{}",
          complete: 1,
          created_at: AT,
        }),
        ...[0, 1].map((position) =>
          insertion("transcript_map_nodes", {
            id: `node_synthetic_${label}_${position}`,
            plan_id: `map_synthetic_${label}`,
            position,
            level: 0,
            ordinal: position,
            byte_offset: position,
            byte_length: 1,
            payload: "{}",
          }),
        ),
        insertion("transcript_map_versions", {
          id: `version_synthetic_${label}`,
          plan_id: `map_synthetic_${label}`,
          machine_id: "synthetic-lineage-host",
          contract_digest: "synthetic-contract",
          generation: 1,
          payload: "{}",
          policy: "{}",
          created_at: AT,
        }),
        insertion("transcript_map_summaries", {
          id: `summary_synthetic_${label}`,
          version_id: `version_synthetic_${label}`,
          node_id: `node_synthetic_${label}_0`,
          reuse_key: `synthetic-reuse-${label}`,
          payload: "{}",
          text: "Synthetic reusable summary",
          created_at: AT,
        }),
      ]),
      ...["ent_00000090", "ent_00000091"].map((id) =>
        insertion("entities", {
          id,
          kind: "project",
          name: "Synthetic mutable applied target",
          canonical_id: id,
          created_by: OWNER,
          created_at: AT,
        }),
      ),
      insertion("plans", {
        id: "pln_mutable_output",
        kind: "topic",
        subject_kind: "proposal",
        subject_id: "pro_00000090",
        operation: "create",
        proposed_by_kind: "operator",
        proposed_by_id: OWNER,
        state: "applied",
        payload: "{}",
        result: JSON.stringify({ factId: "fct_delayed_output" }),
        created_at: AT,
      }),
    ],
    probes,
  );
  await commitPrivacyStep(
    [insertion("session_exclusions", { selector: PRIVATE, actor_id: OWNER, recorded_at: AT })],
    probes,
  );
  await commitPrivacyStep(
    [
      ...[0, 1].map((position) =>
        insertion("transcript_map_bindings", {
          version_id: "version_synthetic_public",
          node_id: `node_synthetic_public_${position}`,
          summary_id: "summary_synthetic_private",
          input_key: "synthetic-binding-input",
        }),
      ),
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000090")).toBeNull();
  await commitPrivacyStep(
    [{ sql: "DELETE FROM transcript_map_bindings WHERE node_id='node_synthetic_public_0'" }],
    probes,
  );
  expect(await harness.store.record("obs_00000090")).toBeNull();
  await commitPrivacyStep(
    [
      {
        sql: "UPDATE transcript_map_bindings SET summary_id='summary_synthetic_public' WHERE node_id='node_synthetic_public_1'",
      },
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000090")).not.toBeNull();
  await commitPrivacyStep(
    [
      {
        sql: "UPDATE transcript_map_bindings SET summary_id='summary_synthetic_private' WHERE node_id='node_synthetic_public_1'",
      },
    ],
    probes,
  );
  await commitPrivacyStep(
    [
      {
        sql: "UPDATE transcript_map_bindings SET input_key='synthetic-unrelated-key' WHERE node_id='node_synthetic_public_1'",
      },
    ],
    probes,
  );
  expect(await harness.store.record("obs_00000090")).toBeNull();
  await commitPrivacyStep(
    [{ sql: "DELETE FROM transcript_map_bindings WHERE node_id='node_synthetic_public_1'" }],
    probes,
  );
  expect(await harness.store.record("obs_00000090")).not.toBeNull();

  const fact = {
    id: "fct_delayed_output",
    entity_id: "ent_00000090",
    predicate: "owner",
    value: MODEL_VALUE,
    valid_from: AT,
    observed_at: AT,
    authority_kind: "operator",
    authority_id: OWNER,
    recorded_at: AT,
  };
  await commitPrivacyStep([insertion("facts", fact)], probes);
  expect((await context("ent_00000090")).state).toBe("missing");
  await commitPrivacyStep(
    [{ sql: "UPDATE facts SET entity_id='ent_00000091' WHERE id='fct_delayed_output'" }],
    probes,
  );
  expect((await context("ent_00000090")).nodes.map(({ id }) => id)).toEqual(["ent_00000090"]);
  expect((await context("ent_00000091")).state).toBe("missing");
  await commitPrivacyStep([{ sql: "DELETE FROM facts WHERE id='fct_delayed_output'" }], probes);
  await commitPrivacyStep([insertion("facts", fact)], probes);
  await commitPrivacyStep(
    [{ sql: "UPDATE plans SET result='ent_00000091' WHERE id='pln_mutable_output'" }],
    probes,
  );
  expect((await context("ent_00000090")).nodes.map(({ id }) => id)).toEqual(["ent_00000090"]);
  expect((await context("ent_00000091")).state).toBe("missing");
  await commitPrivacyStep(
    [{ sql: "UPDATE plans SET state='declined' WHERE id='pln_mutable_output'" }],
    probes,
  );
  expect((await context("ent_00000091")).nodes.map(({ id }) => id)).toEqual(["ent_00000091"]);
  await commitPrivacyStep(
    [{ sql: "UPDATE plans SET state='applied' WHERE id='pln_mutable_output'" }],
    probes,
  );
  await commitPrivacyStep([{ sql: "DELETE FROM plans WHERE id='pln_mutable_output'" }], probes);
  await commitPrivacyStep(
    [
      insertion("plans", {
        id: "pln_mutable_output",
        kind: "topic",
        subject_kind: "observation",
        subject_id: "obs_00000090",
        operation: "create",
        proposed_by_kind: "operator",
        proposed_by_id: OWNER,
        state: "applied",
        payload: "{}",
        result: "ent_00000091",
        created_at: AT,
      }),
    ],
    probes,
  );
  expect((await context("ent_00000091")).nodes.map(({ id }) => id)).toEqual(["ent_00000091"]);
});
