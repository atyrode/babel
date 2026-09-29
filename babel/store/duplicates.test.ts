import { afterEach, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { PluginDatabase, SqlStatement } from "@manifold/plugin";
import { openPluginDatabase } from "@manifold/server/plugin-database";
import { BABEL_PLUGIN_ID, type DuplicateIntent, type DuplicateMember } from "../contract.ts";
import {
  DEFAULT_POLICY,
  decide,
  importLedger,
  rule,
  setPolicy,
  stamp,
  suggest,
  type ActsStore,
  type SuggestArgs,
} from "./acts.ts";
import { duplicateApply, duplicatePlan, duplicatePreview } from "./duplicates.ts";
import { SCHEMA_ADDITIONS, SCHEMA_V1 } from "./schema.ts";
import { openStore } from "./store.ts";

const cleanup: string[] = [];
const A = "fnd_00000001";
const B = "fnd_00000002";
const C = "fnd_00000003";
const H1 = "hyp_00000001";
const H2 = "hyp_00000002";
const AT = Date.UTC(2026, 8, 29, 12);
const PLAN = { after: "", limit: 24, ids: [] };

afterEach(() => {
  for (const path of cleanup.splice(0)) rmSync(path, { recursive: true, force: true });
});

async function fixture(ids = [A, B, C]): Promise<ActsStore> {
  const dataDir = mkdtempSync(join(tmpdir(), "babel-duplicates-"));
  cleanup.push(dataDir);
  const db = openPluginDatabase({ dataDir, pluginId: BABEL_PLUGIN_ID });
  for (const sql of SCHEMA_V1) await db.run(sql);
  const store = { db, now: () => AT, touch: () => {} };
  await setPolicy(
    store,
    {
      ...DEFAULT_POLICY,
      suggesters: [
        { principalId: "jev", pluginId: "atyrode.babel.jev", note: "operator admission" },
        { principalId: "other", pluginId: "other.adviser", note: "independent adviser" },
      ],
    },
    "",
    "owner",
    16,
  );
  for (const id of ids) await seed(store, id);
  return store;
}

async function seed(
  store: ActsStore,
  id: string,
  options: {
    kind?: string;
    root?: string;
    supersedes?: string;
    seq?: number;
    parent?: string;
    payload?: string;
    run?: string | null;
  } = {},
): Promise<void> {
  await store.db.run(
    `INSERT INTO records(id, kind, root_id, supersedes_id, seq, parent_id, run_id,
    actor_kind, actor_id, title, created_at, payload) VALUES(?, ?, ?, ?, ?, ?, ?, 'run', 'writer', 'same claim', ?, ?)`,
    [
      id,
      options.kind ?? "finding",
      options.root ?? id,
      options.supersedes ?? null,
      options.seq ?? 0,
      options.parent ?? null,
      options.run === undefined ? "same-run" : options.run,
      stamp(AT),
      options.payload ?? '{"pattern":"same claim"}',
    ],
  );
}

async function intentOf(store: ActsStore, ids = [A, B, C]): Promise<DuplicateIntent> {
  const plan = await duplicatePlan(store, { ...PLAN, ids });
  const members: DuplicateMember[] = plan.candidates.map(
    ({ recordId, revision, fingerprint, kind, runId, sourceIds, unresolvedSources }) => ({
      recordId,
      revision,
      fingerprint,
      kind,
      runId,
      sourceIds,
      unresolvedSources,
    }),
  );
  const representative = members[0]?.recordId;
  if (representative === undefined) throw new Error("fixture has no representative");
  return {
    kind: "merge-duplicate-records",
    representative,
    members,
    pairs: members.slice(1).map((member) => ({
      a: representative,
      b: member.recordId,
      evidence: "same claim and scope",
    })),
    audit: {
      records: 999,
      distinctRuns: 999,
      distinctSources: 999,
      missingRuns: 999,
      missingSources: 999,
    },
  };
}

function suggestion(intent: DuplicateIntent): SuggestArgs {
  return {
    recordId: intent.representative,
    revision: 0,
    kind: "ask-question",
    subject: "",
    aspect: "duplicate-cluster",
    summary: "Link duplicate claims without erasing them",
    rationale: "same claim",
    basis: "bank-1",
    intent,
  };
}

async function propose(store: ActsStore, ids = [A, B, C]): Promise<string> {
  return (await suggest(store, suggestion(await intentOf(store, ids)), "jev")).id;
}

async function cite(
  store: ActsStore,
  id: string,
  source: string,
  edgeId: string,
  kind = "finding",
): Promise<void> {
  await store.db.run(
    `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, created_at)
    VALUES(?, 'cites', ?, ?, 'session', ?, 'run', 'writer', ?)`,
    [edgeId, kind, id, source, stamp(AT)],
  );
}

test("free plans page the eligible retained corpus and exact rereads never substitute members", async () => {
  const store = await fixture();
  await rule(store, { id: B, ruling: "defer", note: "later" }, "owner");
  const first = await duplicatePlan(store, { ...PLAN, limit: 1 });
  expect(first).toMatchObject({
    eligible: 2,
    continuation: A,
    maxPairs: 0,
    newSuggestionsUpperBound: 0,
  });
  expect(first.candidates.map((member) => member.recordId)).toEqual([A]);
  expect(
    (await duplicatePlan(store, { ...PLAN, after: first.continuation })).candidates.map(
      (member) => member.recordId,
    ),
  ).toEqual([C]);
  expect((await duplicatePlan(store, { ...PLAN, ids: [B, "fnd_99999999"] })).candidates).toEqual(
    [],
  );
  await rule(store, { id: B, ruling: "reopen", note: "reconsider" }, "owner");
  const id = await propose(store);
  expect((await duplicatePlan(store, PLAN)).candidates).toEqual([]);
  expect(
    (await duplicatePlan(store, { ...PLAN, ids: [B, C] })).candidates.map(
      (member) => member.recordId,
    ),
  ).toEqual([B, C]);
  expect((await duplicatePreview(store, id)).state).toBe("ready");
});

test("duplicate judgement text retains differing remedies and scope behind identical problems", async () => {
  const store = await fixture([]);
  await seed(store, "pro_00000001", {
    kind: "proposal",
    payload: JSON.stringify({
      problem: "The queue is overloaded",
      outcome: "Increase concurrency",
      scope: ["interactive jobs"],
    }),
  });
  await seed(store, "pro_00000002", {
    kind: "proposal",
    payload: JSON.stringify({
      problem: "The queue is overloaded",
      outcome: "Reduce concurrency",
      scope: ["background jobs"],
    }),
  });
  const plan = await duplicatePlan(store, PLAN);
  expect(plan.candidates[0]?.claim).toContain("Increase concurrency");
  expect(plan.candidates[0]?.claim).toContain("interactive jobs");
  expect(plan.candidates[1]?.claim).toContain("Reduce concurrency");
  expect(plan.candidates[1]?.claim).toContain("background jobs");
});

test("audit is recomputed from original runs and verified source identities, never supplied numbers", async () => {
  const store = await fixture();
  await store.db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, seen_at) VALUES('omp/session-1', 'machine', 'omp', 'session-1', ?)`,
    [stamp(AT)],
  );
  await cite(store, A, "omp/session-1", "cite-a");
  await cite(store, B, "session-1", "cite-b");
  await cite(store, C, "unresolved-capture", "cite-c");
  await cite(store, A, "unresolved-capture", "cite-a-unresolved");
  await cite(store, A, "unresolved-capture", "cite-a-unresolved-repeat");
  const id = await propose(store);
  const preview = await duplicatePreview(store, id);
  expect(preview.intent.audit).toEqual({
    records: 3,
    distinctRuns: 1,
    distinctSources: 1,
    missingRuns: 0,
    missingSources: 2,
  });
  expect(preview.intent.members.map((member) => member.sourceIds)).toEqual([
    ["omp/session-1"],
    ["omp/session-1"],
    [],
  ]);
  expect(preview.intent.members.map((member) => member.unresolvedSources)).toEqual([1, 0, 1]);
  expect((await openStore(store.db, store.now).record(A))?.nextActions[0]?.intent).toEqual(
    preview.intent,
  );
});

test("hypothesis child citations survive cycles and shared sources do not inflate duplicate support", async () => {
  const store = await fixture([]);
  for (const id of [H1, H2]) await seed(store, id, { kind: "hypothesis" });
  await seed(store, "obs_00000001", {
    kind: "observation",
    parent: H1,
    run: "child-run-1",
  });
  await seed(store, "obs_00000002", {
    kind: "observation",
    parent: H2,
    run: "child-run-2",
  });
  await store.db.run(
    `INSERT INTO sessions(selector, host, harness, source_id, seen_at) VALUES
    ('omp/session-1', 'machine', 'omp', 'session-1', ?),
    ('omp/session-2', 'machine', 'omp', 'session-2', ?)`,
    [stamp(AT), stamp(AT)],
  );
  await cite(store, "obs_00000001", "omp/session-1", "child-cite-1", "observation");
  await cite(store, "obs_00000002", "session-1", "child-cite-shared", "observation");
  await cite(store, "obs_00000002", "omp/session-2", "child-cite-2", "observation");
  await store.db.run(
    `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, created_at)
    VALUES('child-cycle', 'derived_from', 'observation', 'obs_00000001', 'hypothesis', ?, 'run', 'writer', ?)`,
    [H1, stamp(AT)],
  );
  const originals = await store.db.query("SELECT * FROM records ORDER BY id");
  const intent = await intentOf(store, [H1, H2]);
  expect(intent.members.map((member) => member.sourceIds)).toEqual([
    ["omp/session-1"],
    ["omp/session-1", "omp/session-2"],
  ]);
  const id = (await suggest(store, suggestion(intent), "jev")).id;
  const preview = await duplicatePreview(store, id);
  expect(preview.intent.audit).toEqual({
    records: 2,
    distinctRuns: 1,
    distinctSources: 2,
    missingRuns: 0,
    missingSources: 0,
  });
  const applied = await duplicateApply(
    store,
    { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
    "owner",
  );
  expect(applied.links).toEqual([{ fromId: H1, toId: H2, kind: "corroborates" }]);
  expect(await store.db.query("SELECT * FROM records ORDER BY id")).toEqual(originals);
  expect((await openStore(store.db, store.now).record(H1))?.corroboration).toEqual({
    supports: 1,
    distinctRuns: 1,
  });
});

test("forged member content hashes and provenance are refused rather than persisted", async () => {
  const store = await fixture();
  const intent = await intentOf(store);
  const first = intent.members[0];
  if (first === undefined) throw new Error("no fixture member");
  await expect(
    suggest(
      store,
      suggestion({
        ...intent,
        members: [{ ...first, fingerprint: "0".repeat(64) }, ...intent.members.slice(1)],
      }),
      "jev",
    ),
  ).rejects.toThrow(/fingerprint/);
  await expect(
    suggest(
      store,
      suggestion({
        ...intent,
        members: [
          { ...first, runId: "invented-run", sourceIds: ["invented-session"] },
          ...intent.members.slice(1),
        ],
      }),
      "jev",
    ),
  ).rejects.toThrow(/provenance/);
  await expect(
    suggest(
      store,
      suggestion({ ...intent, members: [{ ...first, revision: 1 }, ...intent.members.slice(1)] }),
      "jev",
    ),
  ).rejects.toThrow(/representative|revision/);
  expect(await store.db.query("SELECT id FROM next_actions")).toEqual([]);
});

test("mixed kinds, repeated members, disconnected and ungrounded clusters cannot be suggested", async () => {
  const store = await fixture();
  const intent = await intentOf(store);
  const first = intent.members[0];
  if (first === undefined) throw new Error("no fixture member");
  for (const invalid of [
    { ...intent, members: [first, first] },
    { ...intent, pairs: intent.pairs.slice(0, 1) },
    { ...intent, pairs: intent.pairs.map((pair) => ({ ...pair, evidence: " " })) },
    {
      ...intent,
      members: intent.members.map((member, index) =>
        index === 0 ? { ...member, kind: "proposal" as const } : member,
      ),
    },
  ])
    await expect(suggest(store, suggestion(invalid), "jev")).rejects.toThrow();
  expect(await store.db.query("SELECT id FROM next_actions")).toEqual([]);
});

test("canonical cluster dedup survives concurrent submissions, another adviser and a changed representative", async () => {
  const store = await fixture();
  const intent = await intentOf(store);
  const [one, two] = await Promise.all([
    suggest(store, suggestion(intent), "jev"),
    suggest(
      store,
      suggestion({
        ...intent,
        representative: B,
        members: [...intent.members].reverse(),
        pairs: [...intent.pairs].reverse(),
      }),
      "other",
    ),
  ]);
  expect(two.id).toBe(one.id);
  expect(await store.db.query("SELECT id FROM next_actions")).toEqual([{ id: one.id }]);
  expect(await store.db.query("SELECT id FROM edges")).toEqual([]);
});

test("explicit application appends only immutable links, preserves originals, and repeats the exact durable effect", async () => {
  const store = await fixture();
  const id = await propose(store);
  const original = await store.db.query("SELECT * FROM records ORDER BY id");
  const preview = await duplicatePreview(store, id);
  await decide(
    store,
    { nextActionId: id, decision: "accepted", note: "not execution authority" },
    "owner",
  );
  expect(await store.db.query("SELECT id FROM edges")).toEqual([]);
  const args = { nextActionId: id, fingerprint: preview.fingerprint, confirm: true as const };
  await expect(
    duplicateApply(store, { ...args, fingerprint: "f".repeat(64) }, "owner"),
  ).rejects.toThrow(/fingerprint/);
  const result = await duplicateApply(store, args, "owner");
  expect(result.links).toEqual([
    { fromId: A, toId: B, kind: "corroborates" },
    { fromId: A, toId: C, kind: "corroborates" },
  ]);
  expect(await store.db.query("SELECT * FROM records ORDER BY id")).toEqual(original);
  expect(await store.db.query("SELECT * FROM dispositions")).toEqual([]);
  expect(await store.db.query("SELECT * FROM status_events")).toEqual([]);
  expect((await openStore(store.db, store.now).record(A))?.corroboration).toEqual({
    supports: 2,
    distinctRuns: 1,
  });
  await rule(store, { id: B, ruling: "reject", note: "later decision" }, "owner");
  await seed(store, "fnd_00000004", { root: C, supersedes: C, seq: 1 });
  expect(await duplicateApply(store, args, "another-owner")).toEqual(result);
  expect((await duplicatePreview(store, id)).application).toEqual(result);
  await expect(
    duplicateApply(store, { ...args, fingerprint: "0".repeat(64) }, "owner"),
  ).rejects.toThrow(/fingerprint/);
  expect(await store.db.query("SELECT COUNT(*) AS n FROM edges")).toEqual([{ n: 2n }]);
  await expect(
    store.db.run("UPDATE edges SET note = 'edited' WHERE kind = 'corroborates'"),
  ).rejects.toThrow(/immutable/);
  await expect(store.db.run("DELETE FROM duplicate_applications")).rejects.toThrow(/append-only/);
  await expect(
    store.db.run("UPDATE duplicate_applications SET operator_id = 'other'"),
  ).rejects.toThrow(/append-only/);
});

test("stale nonrepresentative revisions and changed original provenance invalidate preview without writes", async () => {
  const store = await fixture();
  const id = await propose(store);
  const before = await duplicatePreview(store, id);
  await cite(store, B, "new-unresolved-source", "new-citation");
  expect((await duplicatePreview(store, id)).state).toBe("refused");
  await expect(
    duplicateApply(
      store,
      { nextActionId: id, fingerprint: before.fingerprint, confirm: true },
      "owner",
    ),
  ).rejects.toThrow(/changed/);
  expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
  expect(await store.db.query("SELECT id FROM edges WHERE kind = 'corroborates'")).toEqual([]);
  await seed(store, "fnd_00000004", { root: C, supersedes: C, seq: 1 });
  expect((await duplicatePreview(store, id)).reason).toMatch(/superseded|eligible/);
});

test("new provenance admits an attributable correction without deleting the stale suggestion", async () => {
  const store = await fixture();
  const previous = await propose(store);
  await cite(store, B, "new-unresolved-source", "new-citation");
  expect((await duplicatePlan(store, PLAN)).candidates.map((member) => member.recordId)).toEqual([
    A,
    B,
    C,
  ]);
  const corrected = await suggest(store, suggestion(await intentOf(store)), "jev");
  expect(corrected.id).not.toBe(previous);
  expect(corrected.supersedes).toBe(previous);
  expect((await duplicatePreview(store, previous)).state).toBe("refused");
  expect((await duplicatePreview(store, corrected.id)).state).toBe("ready");
  expect(
    (await openStore(store.db, store.now).record(A))?.nextActions.map((action) => action.id),
  ).toEqual([corrected.id]);
  expect(
    (await store.db.query("SELECT id FROM next_actions ORDER BY id")).map((row) => row["id"]),
  ).toEqual([previous, corrected.id].sort());
});

function intercept(
  store: ActsStore,
  before: (statements: readonly SqlStatement[]) => Promise<void>,
): ActsStore {
  const db: PluginDatabase = {
    pluginId: store.db.pluginId,
    query: store.db.query.bind(store.db),
    run: store.db.run.bind(store.db),
    batch: async (statements) => {
      await before(statements);
      return await store.db.batch(statements);
    },
  };
  return { ...store, db };
}

test("atomic application guard refuses a ruling racing the pre-read and creates no partial links", async () => {
  const store = await fixture();
  const id = await propose(store);
  const preview = await duplicatePreview(store, id);
  const racing = intercept(store, async (statements) => {
    if (
      statements.some((statement) => statement.sql.includes("INSERT INTO duplicate_applications"))
    ) {
      await rule(store, { id: C, ruling: "defer", note: "racing decision" }, "owner");
    }
  });
  await expect(
    duplicateApply(
      racing,
      { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
      "owner",
    ),
  ).rejects.toThrow(/changed/);
  expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
  expect(await store.db.query("SELECT id FROM edges")).toEqual([]);
});

test("new child provenance invalidates a held hypothesis preview even when attachment races application", async () => {
  for (const racing of [false, true]) {
    const store = await fixture([]);
    for (const id of [H1, H2]) await seed(store, id, { kind: "hypothesis" });
    await store.db.run(
      `INSERT INTO sessions(selector, host, harness, source_id, seen_at)
      VALUES('omp/late-session', 'machine', 'omp', 'late-session', ?)`,
      [stamp(AT)],
    );
    const originals = await store.db.query("SELECT * FROM records ORDER BY id");
    const id = await propose(store, [H1, H2]);
    const preview = await duplicatePreview(store, id);
    expect(preview.state).toBe("ready");
    const append = async (): Promise<void> => {
      await seed(store, "obs_00000003", { kind: "observation", parent: H2 });
      await cite(store, "obs_00000003", "omp/late-session", "late-child-cite", "observation");
    };
    const applying = racing
      ? intercept(store, async (statements) => {
          if (
            statements.some((statement) =>
              statement.sql.includes("INSERT INTO duplicate_applications"),
            )
          )
            await append();
        })
      : store;
    if (!racing) await append();
    await expect(
      duplicateApply(
        applying,
        { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
        "owner",
      ),
    ).rejects.toThrow(/changed/);
    expect((await duplicatePreview(store, id)).state).toBe("refused");
    expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
    expect(await store.db.query("SELECT id FROM edges WHERE kind = 'corroborates'")).toEqual([]);
    expect(
      await store.db.query("SELECT * FROM records WHERE kind = 'hypothesis' ORDER BY id"),
    ).toEqual(originals);
    expect(
      await store.db.query("SELECT id, parent_id FROM records WHERE kind = 'observation'"),
    ).toEqual([{ id: "obs_00000003", parent_id: H2 }]);
    expect(await store.db.query("SELECT id FROM edges WHERE kind = 'cites'")).toEqual([
      { id: "late-child-cite" },
    ]);
  }
});

test("a changed link effect invalidates both the preview token and a racing atomic application", async () => {
  for (const racing of [false, true]) {
    const store = await fixture();
    const id = await propose(store);
    const preview = await duplicatePreview(store, id);
    const append = async (): Promise<void> => {
      await store.db.run(
        `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id,
        actor_kind, actor_id, created_at)
        VALUES('existing-link', 'corroborates', 'finding', ?, 'finding', ?, 'operator', 'other-owner', ?)`,
        [A, B, stamp(AT)],
      );
    };
    const applying = racing
      ? intercept(store, async (statements) => {
          if (
            statements.some((statement) =>
              statement.sql.includes("INSERT INTO duplicate_applications"),
            )
          )
            await append();
        })
      : store;
    if (!racing) await append();
    await expect(
      duplicateApply(
        applying,
        {
          nextActionId: id,
          fingerprint: preview.fingerprint,
          confirm: true,
        },
        "owner",
      ),
    ).rejects.toThrow(/changed/);
    expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
    expect(await store.db.query("SELECT id FROM edges")).toEqual([{ id: "existing-link" }]);
  }
});

test("atomic snapshot guard refuses provenance appended after validation, including suggestion admission", async () => {
  const store = await fixture();
  const intent = await intentOf(store);
  const racing = intercept(store, async (statements) => {
    if (statements.some((statement) => statement.sql.includes("INSERT INTO next_actions"))) {
      await cite(store, B, "racing-source", "racing-citation");
    }
  });
  await expect(suggest(racing, suggestion(intent), "jev")).rejects.toThrow(/changed/);
  expect(await store.db.query("SELECT id FROM next_actions")).toEqual([]);
  const id = await propose(store);
  const preview = await duplicatePreview(store, id);
  const applying = intercept(store, async (statements) => {
    if (
      statements.some((statement) => statement.sql.includes("INSERT INTO duplicate_applications"))
    ) {
      await cite(store, C, "second-source", "second-citation");
    }
  });
  await expect(
    duplicateApply(
      applying,
      { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
      "owner",
    ),
  ).rejects.toThrow(/changed/);
  expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
  expect(await store.db.query("SELECT id FROM edges WHERE kind = 'corroborates'")).toEqual([]);
});

test("a link insertion failure rolls back both earlier links and the application receipt", async () => {
  const store = await fixture();
  const id = await propose(store);
  const preview = await duplicatePreview(store, id);
  await store.db.run(
    `CREATE TRIGGER fail_second_link BEFORE INSERT ON edges WHEN NEW.kind = 'corroborates' AND NEW.to_id = '${C}' BEGIN SELECT RAISE(ABORT, 'second edge refused'); END`,
  );
  await expect(
    duplicateApply(
      store,
      { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
      "owner",
    ),
  ).rejects.toThrow(/second edge refused/);
  expect(await store.db.query("SELECT id FROM edges")).toEqual([]);
  expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
});

test("existing edges are not copied and missing runs do not manufacture independent corroboration", async () => {
  const store = await fixture([A, B]);
  await seed(store, C, { run: null });
  await store.db.run(
    `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, created_at)
    VALUES('old-link', 'corroborates', 'finding', ?, 'finding', ?, 'operator', 'owner', ?)`,
    [A, B, stamp(AT)],
  );
  const id = await propose(store);
  const preview = await duplicatePreview(store, id);
  expect(preview.links.map((link) => link.exists)).toEqual([true, false]);
  const applied = await duplicateApply(
    store,
    { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
    "owner",
  );
  expect(applied.links.map((link) => link.toId)).toEqual([B, C]);
  expect(await store.db.query("SELECT COUNT(*) AS n FROM edges")).toEqual([{ n: 2n }]);
  expect((await openStore(store.db, store.now).record(A))?.corroboration).toEqual({
    supports: 2,
    distinctRuns: 1,
  });
  expect(preview.intent.audit).toMatchObject({ distinctRuns: 1, missingRuns: 1 });
});

test("application authority cannot arrive in an import or a run's ordinary next action", async () => {
  const store = await fixture();
  await expect(
    importLedger(store, { source: "forged", table: "duplicate_applications", rows: [] }),
  ).rejects.toThrow(/no table/);
  const intent = await intentOf(store);
  await store.db.run(
    `INSERT INTO next_actions(id, record_id, kind, proposed_by_kind, proposed_by_id, summary, created_at, payload)
    VALUES('nxt_00000001', ?, 'ask-question', 'run', 'model-run', 'forged authority', ?, ?)`,
    [A, stamp(AT), JSON.stringify({ intent })],
  );
  await expect(duplicatePreview(store, "nxt_00000001")).rejects.toThrow(
    /no admitted duplicate intent/,
  );
  expect(await store.db.query("SELECT * FROM duplicate_applications")).toEqual([]);
});

test("additive upgrade creates the same atomic protections without rewriting existing records", async () => {
  const store = await fixture([A, B]);
  const original = await store.db.query("SELECT * FROM records ORDER BY id");
  for (const addition of [...SCHEMA_ADDITIONS].reverse()) {
    if (addition.object.startsWith("duplicate_") || addition.object === "corroborates_immutable") {
      await store.db.run(
        `DROP ${addition.sql.startsWith("CREATE TABLE") ? "TABLE" : addition.sql.startsWith("CREATE INDEX") ? "INDEX" : "TRIGGER"} ${addition.object}`,
      );
    }
  }
  for (const addition of SCHEMA_ADDITIONS) {
    if (addition.object.startsWith("duplicate_") || addition.object === "corroborates_immutable")
      await store.db.run(addition.sql);
  }
  const id = await propose(store, [A, B]);
  const preview = await duplicatePreview(store, id);
  await duplicateApply(
    store,
    { nextActionId: id, fingerprint: preview.fingerprint, confirm: true },
    "owner",
  );
  expect(await store.db.query("SELECT * FROM records ORDER BY id")).toEqual(original);
  await expect(store.db.run("DELETE FROM duplicate_applications")).rejects.toThrow(/append-only/);
});

test("without duplicate links legacy corroboration and absent generic intent remain unchanged", async () => {
  const store = await fixture([A, B]);
  for (const id of ["support-one", "support-two"]) {
    await store.db.run(
      `INSERT INTO edges(id, kind, from_kind, from_id, to_kind, to_id, actor_kind, actor_id, created_at)
      VALUES(?, 'consolidates', 'finding', ?, 'finding', ?, 'run', 'writer', ?)`,
      [id, A, B, stamp(AT)],
    );
  }
  const generic = await suggest(
    store,
    {
      recordId: A,
      revision: 0,
      kind: "ask-question",
      subject: "",
      aspect: "",
      summary: "An ordinary next action",
      rationale: "",
      basis: "",
    },
    "jev",
  );
  const peel = await openStore(store.db, store.now).record(A);
  expect(peel?.corroboration).toEqual({ supports: 2, distinctRuns: 1 });
  expect(peel?.nextActions.find((action) => action.id === generic.id)).not.toHaveProperty("intent");
  expect(peel).not.toHaveProperty("proposalIntent");
});

test("proposal intent comes only from declared plans or valid refinement payloads, never its title", async () => {
  const store = await fixture([]);
  const generic = "pro_00000001";
  const topic = "pro_00000002";
  const backlog = "pro_00000003";
  const refinement = "pro_00000004";
  await seed(store, generic, {
    kind: "proposal",
    payload: '{"title":"Merge these topics and refine this record","problem":"generic"}',
  });
  await seed(store, topic, { kind: "proposal" });
  await seed(store, backlog, { kind: "proposal" });
  await seed(store, refinement, {
    kind: "proposal",
    payload: JSON.stringify({
      refinement: {
        targetRecordId: generic,
        targetRevisionId: generic,
        depth: 1,
        targetPath: "/title",
        reason: "more precise",
        replacement: "precise wording",
        sourceRole: "reception",
      },
    }),
  });
  for (const [id, kind, operation] of [
    [topic, "topic", "merge"],
    [backlog, "backlog", "retire"],
  ]) {
    await store.db.run(
      `INSERT INTO plans(id, kind, subject_kind, subject_id, operation, payload,
      proposed_by_kind, proposed_by_id, created_at) VALUES(?, ?, 'proposal', ?, ?, '{}', 'run', 'writer', ?)`,
      [`plan-${String(id)}`, String(kind), String(id), String(operation), stamp(AT)],
    );
  }
  const read = openStore(store.db, store.now);
  expect(await read.record(generic)).not.toHaveProperty("proposalIntent");
  expect((await read.record(topic))?.proposalIntent).toEqual({ kind: "topic", operation: "merge" });
  expect((await read.record(backlog))?.proposalIntent).toEqual({
    kind: "backlog",
    operation: "retire",
  });
  expect((await read.record(refinement))?.proposalIntent).toEqual({ kind: "record-refinement" });
});
