import { afterEach, beforeEach, expect, test } from "bun:test";
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
  const previouslyFiled = modelPrivacyGuard([], ["obs_00000077"]);
  expect(
    await harness.db.query(
      `SELECT 1 AS eligible WHERE ${previouslyFiled.sql}`,
      previouslyFiled.params,
    ),
  ).toEqual([{ eligible: 1n }]);
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
