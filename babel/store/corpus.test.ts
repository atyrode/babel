import { afterEach, beforeEach, expect, test } from "bun:test";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import { EMBEDDING_RUN_KIND, EMBEDDING_SERVICE, OPERATIONS, termsQuery } from "../contract.ts";
import { askEmbedding, embedder, type EmbeddingServices } from "../server/embed.ts";
import {
  PROBE_DEPTH,
  backfillVectors,
  ensureTerms,
  rebuildTerms,
  searchCorpus,
  type CorpusStore,
  type Embedder,
} from "./corpus.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";
import {
  excludeSessionWhenQuiescent,
  readExcludedCaptureIds,
  readExcludedRecordIds,
  readExcludedRunIds,
  sourcePrivacyCondition,
} from "./source-privacy.ts";
import { projectReview } from "../server/conductor.ts";

/*
  THE CORPUS INDEX (#337), against a real store and a real FTS5 table.

  What is worth proving here is not that a search returns rows. It is the five properties the
  decision names, each of which a plausible change would break silently:

  - THE BASELINE STILL WORKS WITH ZERO EGRESS. Keyword search answers with no policy installed and
    the invocation is never reached — asserted as a call count, because a version that called and
    caught the refusal would pass every assertion about the return value while making a request
    nobody authorized.
  - WHAT LEAVES IS THE TEXT. Asserted against the SERIALIZED request, not against intent: a
    planted identifier is in the record's row and must appear nowhere in the bytes.
  - THE MODEL IS RECORDED AND NEVER MIXED. A query embedded by one model does not compare against
    another's vectors, because two embedding spaces have no angle between them and the failure is
    a confident wrong order rather than an error.
  - PARTIAL IS USABLE. A third of a corpus answers over that third and says `partial`.
  - THE REBUILD IS ONE PASS FROM `records` ALONE, which is what makes the index derived state with
    no backup of its own.

  The store is the engine's own opener under every CHECK the schema declares, so a vector row this
  vocabulary does not admit fails at the insert.
*/

const NOW = Date.UTC(2026, 8, 19, 12, 0, 0);

/** A planted identifier. Nothing an embedding request carries may contain it. */
const PLANTED = "run_planted_by_the_test_never_leaves_the_hub";

let harness: TestStore;
let corpus: CorpusStore;

beforeEach(async () => {
  harness = await openTestStore(NOW);
  corpus = { db: harness.db };
});

afterEach(() => {
  harness.close();
});

async function record(
  id: string,
  kind: string,
  title: string,
  payload: Readonly<Record<string, unknown>>,
  runId = PLANTED,
): Promise<void> {
  await insert(harness.db, "records", {
    id,
    kind,
    root_id: id,
    seq: 0,
    run_id: runId,
    actor_kind: "run",
    actor_id: runId,
    title,
    created_at: new Date(NOW).toISOString(),
    payload: JSON.stringify(payload),
  });
}

/**
 * An embedder with no model behind it: one axis per keyword, so a text's position is decided by
 * which of the vocabulary's words it uses.
 *
 * A stub is right here and a real model would be wrong. What is under test is the index, the
 * ranking and the reporting; a model's own quality is not a property of this repository, and a
 * test that needed one would be a test that only runs where a credential does.
 */
const VOCABULARY = ["drain", "spend", "window", "session", "archive", "restic"] as const;

function stubEmbedder(model: string): Embedder {
  return async (text: string) => {
    const lowered = text.toLowerCase();
    const values = VOCABULARY.map((word) => (lowered.includes(word) ? 1 : -1));
    return { model, values };
  };
}

const READY: InstanceServiceDescription = {
  serviceId: EMBEDDING_SERVICE.serviceId,
  defaultOwner: null,
  owner: { machineId: "dev-01", name: "dev-01", online: true },
  configuration: {
    revision: "r3",
    pluginId: "atyrode.babel",
    enabled: true,
    policySha256: "b".repeat(64),
  },
  connected: true,
  state: "ready",
  reason: null,
};

/** The host, as much of it as this needs: a roster, one invocation, both recorded. */
function host(options: {
  readonly roster?: readonly InstanceServiceDescription[];
  readonly reply?: ServiceReply;
}): { readonly services: EmbeddingServices; readonly asks: unknown[] } {
  const asks: unknown[] = [];
  const answered: ServiceReply = {
    type: "service_result",
    requestId: "q1",
    ok: true,
    result: { embedding: [0.5, -0.5], model: "stub-embed-v1" },
  };
  return {
    asks,
    services: {
      listInstances: async () => ({ defaultOwner: null, services: [...(options.roster ?? [])] }),
      invokeInstance: async (args) => {
        asks.push(args);
        return options.reply ?? answered;
      },
    },
  };
}

/** One reply as the operator's own response projection would return it: named leaves. */
function projected(leaves: Readonly<Record<string, readonly number[] | string>>): ServiceReply {
  const result: Record<string, number[] | string> = {};
  for (const [leaf, value] of Object.entries(leaves)) {
    result[leaf] = typeof value === "string" ? value : [...value];
  }
  return { type: "service_result", requestId: "q1", ok: true, result };
}

async function seedThree(): Promise<void> {
  await record("fnd_00000001", "finding", "The drain stalls at zero", {
    pattern: "the fan never drains and the window is not spent",
    significance: "a spend nobody can account for",
  });
  await record("obs_00000002", "observation", "Archive verify is never run", {
    claim: "no restic archive has had its restore path exercised",
    impact: "an archive nobody has tested",
  });
  await record("pro_00000003", "proposal", "Record what a session cost", {
    outcome: "every session carries its own spend",
    problem: "a window is spent and nothing says on what",
  });
}

test("a keyword search answers over records the trigger indexed, and names what it did not use", async () => {
  await seedThree();
  const answer = await searchCorpus(corpus, null, {
    query: "restic archive",
    limit: 10,
    kinds: [],
  });
  expect(answer.hits.map((hit) => hit.id)).toEqual(["obs_00000002"]);
  expect(answer.hits[0]?.via).toBe("keyword");
  expect(answer.hits[0]?.meaning).toBeNull();
  expect(answer.meaning).toBe("absent");
  expect(answer.meaningAbsent).toContain("no embedding service is installed");
  expect(answer.coverage.records).toBe(3);
  expect(answer.coverage.keyworded).toBe(3);
  expect(answer.coverage.embedded).toBe(0);
  // Nothing was compared, so nothing was cut: an answer the prefilter never touched must not
  // claim to be approximate, or "approximate" comes to mean "a vector exists somewhere".
  expect(answer.scanned).toBe(0);
  expect(answer.approximate).toBe(false);
});

/**
 * The shape #426 was reported from: a row imported before the frontier's guard (#416) existed,
 * carrying an id no input schema admits. `records_kept` refuses DELETE below the doors, so it is
 * permanent, and the read side is the only place it can be survivable.
 */
async function seedUnnameable(id = "rec_seed_001"): Promise<void> {
  await record(id, "finding", "The drain stalls at zero, again", {
    pattern: "the drain is the drain and the drain never drains",
    significance: "a spend nobody can account for",
  });
}

test("legacy damage cannot crowd valid records out of the candidate window", async () => {
  await seedThree();
  // More damaged rows than the keyword candidate window, all stronger keyword matches.
  // Filtering only the final fused slice would lose the valid result entirely.
  for (let index = 0; index < 40; index += 1) await seedUnnameable(`rec_seed_${String(index)}`);
  // SQLite text length/substr stop at NUL; a valid prefix must not hide an invalid suffix.
  await seedUnnameable("fnd_00000000\u0000hidden");
  const answer = await searchCorpus(corpus, null, { query: "drain", limit: 1, kinds: [] });
  expect(answer.hits.map((hit) => hit.id)).toEqual(["fnd_00000001"]);
  expect(answer.coverage.records).toBe(44);
  expect(answer.coverage.unnameable).toBe(41);
});

test("the count of what cannot be named is the store's, so a query that ranks none still says so", async () => {
  await seedThree();
  await seedUnnameable();
  // The damage is query-independent — every query failed, including one matching nothing the
  // bad row holds — so the account of it cannot be a property of the query either.
  const missed = await searchCorpus(corpus, null, { query: "kubernetes", limit: 10, kinds: [] });
  expect(missed.hits).toEqual([]);
  expect(missed.coverage.unnameable).toBe(1);
  // A query with no terms at all reaches neither index and must still answer rather than throw.
  const empty = await searchCorpus(corpus, null, { query: "  ", limit: 10, kinds: [] });
  expect(empty.hits).toEqual([]);
  expect(empty.coverage.unnameable).toBe(1);
  expect(empty.coverage.records).toBe(4);
});

test("with no embedding policy installed, a search makes no outbound call at all", async () => {
  await seedThree();
  const { services, asks } = host({ roster: [] });
  const answer = await searchCorpus(corpus, embedder(services), {
    query: "the drain spent a window",
    limit: 10,
    kinds: [],
  });
  // The whole of the first condition: the invocation is not reached, so an unconfigured hub sends
  // nothing, spends nothing and is indistinguishable from this feature not existing.
  expect(asks).toEqual([]);
  expect(answer.meaning).toBe("absent");
  expect(answer.hits.length).toBeGreaterThan(0);
});

test("an embedding request carries the record's prose and no identifier of any kind", async () => {
  await record("fnd_00000009", "finding", "The drain stalls at zero", {
    pattern: "the fan never drains",
    significance: "a spend nobody can account for",
  });
  const { services, asks } = host({ roster: [READY] });
  const report = await backfillVectors(corpus, embedder(services), new Date(NOW).toISOString(), 4);
  expect(report.embedded).toBe(1);
  expect(asks.length).toBe(1);
  const body = JSON.stringify(asks[0]);
  // Asserted against the bytes rather than against intent, the way the credential tests do it.
  expect(body).toContain("the fan never drains");
  expect(body).toContain("a spend nobody can account for");
  expect(body).not.toContain("fnd_00000009");
  expect(body).not.toContain(PLANTED);
  expect(body).not.toContain(new Date(NOW).toISOString());
  const sent = asks[0] as { readonly input: Record<string, unknown> };
  expect(Object.keys(sent.input)).toEqual([EMBEDDING_SERVICE.textField]);
});

test("a vector carries the model that made it, and refuses to exist without one", async () => {
  await record("fnd_00000010", "finding", "A window is spent", { pattern: "the drain runs dry" });
  const named = host({ roster: [READY], reply: projected({ embedding: [1, -1], model: "v1" }) });
  await backfillVectors(corpus, embedder(named.services), new Date(NOW).toISOString(), 4);
  // SQLite integers cross the boundary as bigints; the column is what is under test, not the tag.
  const rows = await harness.db.query<{ model: string; dims: bigint }>(
    "SELECT model, dims FROM record_vectors",
  );
  expect(rows.map((row) => ({ model: row.model, dims: Number(row.dims) }))).toEqual([
    { model: "v1", dims: 2 },
  ]);

  // A projection that omits the model leaf produces no vector at all. A row stored under a guessed
  // name is a row nobody can tell is stale, which is the whole reason the column exists.
  const anonymous = host({ roster: [READY], reply: projected({ embedding: [1, -1] }) });
  await expect(askEmbedding(anonymous.services, "a window is spent")).resolves.toBeNull();
});

test("a query embedded by one model is never compared against another model's vectors", async () => {
  await seedThree();
  const at = new Date(NOW).toISOString();
  await backfillVectors(corpus, stubEmbedder("stub-embed-v1"), at, 10);
  const stale = await searchCorpus(corpus, stubEmbedder("stub-embed-v2"), {
    query: "an unspent window",
    limit: 10,
    kinds: [],
  });
  // Nothing under v2 exists, so there was nothing to compare: the answer is keyword-only and says
  // so, and the three v1 rows are reported as pending work rather than silently ranked.
  expect(stale.scanned).toBe(0);
  expect(stale.meaning).toBe("absent");
  expect(stale.meaningAbsent).toContain("stub-embed-v2");
  expect(stale.coverage.stale).toBe(3);
  expect(stale.coverage.embedded).toBe(0);
  expect(stale.coverage.model).toBe("stub-embed-v2");
  // And the same query under the model that produced them does compare.
  const current = await searchCorpus(corpus, stubEmbedder("stub-embed-v1"), {
    query: "an unspent window",
    limit: 10,
    kinds: [],
  });
  expect(current.scanned).toBe(3);
  expect(current.meaning).toBe("full");
});

test("the meaning half surfaces a record that shares no word with the query", async () => {
  await seedThree();
  const at = new Date(NOW).toISOString();
  await backfillVectors(corpus, stubEmbedder("stub-embed-v1"), at, 10);
  const answer = await searchCorpus(corpus, stubEmbedder("stub-embed-v1"), {
    query: "restic",
    limit: 10,
    kinds: [],
  });
  const archive = answer.hits.find((hit) => hit.id === "obs_00000002");
  expect(archive?.via).toBe("both");
  // The two records that never say "restic" are reached by position alone, which is the capability
  // a keyword index cannot have however well it is tuned.
  const byMeaningOnly = answer.hits.filter((hit) => hit.via === "meaning").map((hit) => hit.id);
  expect(byMeaningOnly.sort()).toEqual(["fnd_00000001", "pro_00000003"]);
  expect(answer.hits[0]?.id).toBe("obs_00000002");
});

test("a half-embedded corpus answers over the half it has and says it is partial", async () => {
  await seedThree();
  const at = new Date(NOW).toISOString();
  // Bounded: one record a pass, which is what makes a 6,038-record backfill a sequence of ticks.
  const first = await backfillVectors(corpus, stubEmbedder("stub-embed-v1"), at, 1);
  expect(first.embedded).toBe(1);
  expect(first.remaining).toBe(2);
  const answer = await searchCorpus(corpus, stubEmbedder("stub-embed-v1"), {
    query: "the drain spent a window",
    limit: 10,
    kinds: [],
  });
  expect(answer.meaning).toBe("partial");
  expect(answer.meaningAbsent).toBe("");
  expect(answer.coverage.embedded).toBe(1);
  expect(answer.coverage.records).toBe(3);
  // It answers rather than refusing, and the keyword half is still exhaustive over the words.
  expect(answer.hits.length).toBeGreaterThan(0);
});

test("a backfill resumes where it stopped and never pays for the same record twice", async () => {
  await seedThree();
  const at = new Date(NOW).toISOString();
  const calls: string[] = [];
  const counted: Embedder = async (text) => {
    calls.push(text);
    return await stubEmbedder("stub-embed-v1")(text);
  };
  const first = await backfillVectors(corpus, counted, at, 2);
  expect(first.embedded).toBe(2);
  expect(first.remaining).toBe(1);
  const firstCalls = calls.length;
  const second = await backfillVectors(corpus, counted, at, 2);
  expect(second.embedded).toBe(1);
  expect(second.remaining).toBe(0);
  // Three records, three vectors, and the second pass cost one call rather than three: the
  // resumption is the pending query, so a tick that died costs the rows it had not written yet.
  expect(calls.length).toBe(firstCalls + 1);
  const rows = await harness.db.query<{ n: number }>("SELECT COUNT(*) AS n FROM record_vectors");
  expect(Number(rows[0]?.n)).toBe(3);
  // AND A PASS WITH NOTHING TO DO REACHES NO ORIGIN. A drain ticks for as long as an operator
  // gives it, so one paid call per tick on a corpus that is already covered would be a standing
  // cost with nothing to show for it — which is the objection the decision weighed.
  const settled = calls.length;
  const third = await backfillVectors(corpus, counted, at, 2);
  expect(third.embedded).toBe(0);
  expect(third.model).toBe("stub-embed-v1");
  expect(calls.length).toBe(settled);
});

test("a record with no text is recorded as done, so no later pass offers it again", async () => {
  // One record with text beside it, because a blank record's row still has to name a model and a
  // pass learns the model from a record that has something to send. A corpus of nothing but blank
  // records costs no call at all, which is the other half of the same rule.
  await record("fnd_00000005", "finding", "A window is unspent", { pattern: "the drain runs dry" });
  await record("obs_00000004", "observation", "", {});
  const at = new Date(NOW).toISOString();
  const report = await backfillVectors(corpus, stubEmbedder("stub-embed-v1"), at, 4);
  expect(report.empty).toBe(1);
  expect(report.embedded).toBe(1);
  expect(report.remaining).toBe(0);
  const rows = await harness.db.query<{ dims: bigint; reason: string }>(
    "SELECT dims, reason FROM record_vectors WHERE record_id = 'obs_00000004'",
  );
  expect(Number(rows[0]?.dims)).toBe(0);
  expect(String(rows[0]?.reason)).toContain("no text");
  // It is covered rather than pending, so a corpus with blank records does not read as
  // permanently half-built, and it is never a search candidate because it carries no direction.
  const answer = await searchCorpus(corpus, stubEmbedder("stub-embed-v1"), {
    query: "the drain",
    limit: 10,
    kinds: [],
  });
  expect(answer.coverage.empty).toBe(1);
  expect(answer.coverage.embedded).toBe(1);
  expect(answer.meaning).toBe("full");
  expect(answer.hits.map((hit) => hit.id)).not.toContain("obs_00000004");
});

test("a corpus of nothing but blank records costs no call", async () => {
  await record("obs_00000006", "observation", "", {});
  await record("obs_00000007", "observation", "", {});
  const calls: string[] = [];
  const counted: Embedder = async (text) => {
    calls.push(text);
    return { model: "stub-embed-v1", values: [1, -1] };
  };
  const report = await backfillVectors(corpus, counted, new Date(NOW).toISOString(), 4);
  // No model is known and nothing has text to send, so there is nothing to learn a model from and
  // no reason to pay to learn one: the rows wait for a pass in which some record has text.
  expect(calls).toEqual([]);
  expect(report.embedded).toBe(0);
  expect(report.empty).toBe(0);
  expect(report.unanswered).toBe(2);
  expect(await harness.db.query("SELECT record_id FROM record_vectors")).toEqual([]);
});

test("an answer drawn through the prefilter says so, and one that was not does not", async () => {
  // One more record than the prefilter carries forward, so the sketch actually cuts. A sign-bit
  // sketch keeps the quadrant and throws away every magnitude, so its order is a correlate of
  // the angle rather than the angle: the honest report is that a better match may be outside the
  // slice that was scored, and a caller that must not miss reads the keyword half.
  for (let n = 0; n <= PROBE_DEPTH; n += 1) {
    const id = `fnd_${n.toString(16).padStart(8, "0")}`;
    await record(id, "finding", `A window is unspent ${String(n)}`, {
      pattern: n % 2 === 0 ? "the drain runs dry" : "the archive is never verified",
    });
  }
  const at = new Date(NOW).toISOString();
  let remaining = 1;
  while (remaining > 0) {
    remaining = (await backfillVectors(corpus, stubEmbedder("stub-embed-v1"), at, 200)).remaining;
  }
  const answer = await searchCorpus(corpus, stubEmbedder("stub-embed-v1"), {
    query: "the drain",
    limit: 5,
    kinds: [],
  });
  expect(answer.coverage.embedded).toBe(PROBE_DEPTH + 1);
  expect(answer.scanned).toBe(PROBE_DEPTH + 1);
  expect(answer.rescored).toBe(PROBE_DEPTH);
  expect(answer.approximate).toBe(true);
  expect(answer.meaning).toBe("full");
  expect(answer.hits).toHaveLength(5);
});

test("the keyword index rebuilds in one pass from records alone", async () => {
  await seedThree();
  // A store that reached this shape by addition holds every record and no term: the trigger only
  // ever fires for a record written after it existed, and the crossing's 6,038 predate it.
  await harness.db.run("DELETE FROM record_terms");
  const empty = await searchCorpus(corpus, null, { query: "restic", limit: 10, kinds: [] });
  expect(empty.hits).toEqual([]);
  expect(empty.coverage.keyworded).toBe(0);

  const written = await rebuildTerms(corpus);
  expect(written).toBe(3);
  const found = await searchCorpus(corpus, null, { query: "restic", limit: 10, kinds: [] });
  expect(found.hits.map((hit) => hit.id)).toEqual(["obs_00000002"]);
  // Idempotent, because a rebuild deletes first: a crashed pass costs a repeat, not duplicates.
  expect(await rebuildTerms(corpus)).toBe(3);
  const again = await searchCorpus(corpus, null, { query: "restic", limit: 10, kinds: [] });
  expect(again.hits.length).toBe(1);
});

test("the gap between records and terms is what triggers a rebuild, and nothing else does", async () => {
  await seedThree();
  expect(await ensureTerms(corpus)).toBe(0);
  await harness.db.run("DELETE FROM record_terms WHERE record_id = ?", ["obs_00000002"]);
  expect(await ensureTerms(corpus)).toBe(3);
  expect(await ensureTerms(corpus)).toBe(0);
});

test("a kind filter narrows the answer without narrowing what was searched", async () => {
  await seedThree();
  const answer = await searchCorpus(corpus, null, {
    query: "window spend drain",
    limit: 10,
    kinds: ["proposal"],
  });
  expect(answer.hits.every((hit) => hit.id.startsWith("pro_"))).toBe(true);
  expect(answer.hits.length).toBe(1);
});

test("operator prose becomes a query rather than syntax", async () => {
  // Every one of these is an FTS5 operator or a bare quote, and a search box that raised
  // `fts5: syntax error` at a hyphen would be a search box nobody uses twice.
  // A one-character token is dropped: `3` is a term bm25 would score against every record that
  // happens to contain a numeral, which is noise wearing a query's clothes.
  expect(termsQuery(`"drain" NEAR/3 -spend*`)).toBe(`"drain" OR "near" OR "spend"`);
  expect(termsQuery("  ")).toBe("");
  const answer = await searchCorpus(corpus, null, { query: `AND OR " *`, limit: 10, kinds: [] });
  expect(answer.hits).toEqual([]);
});

test("uncited prepared sources quarantine whole records and later run lineage while an independent sibling embeds", async () => {
  const selector = "omp/privacy-source-synthetic";
  await insert(harness.db, "runs", {
    id: "run_private_material",
    kind: OPERATIONS.prepare,
    job_id: "job_private_material",
    started_at: new Date(NOW).toISOString(),
    closure: "completed",
    payload: JSON.stringify({ material: { sessions: [{ selector }] } }),
  });
  await insert(harness.db, "runs", {
    id: "run_private_source",
    kind: OPERATIONS.explore,
    prepare_job_id: "job_private_material",
    started_at: new Date(NOW).toISOString(),
    closure: "completed",
    payload: "{}",
  });
  await record(
    "obs_000000a1",
    "observation",
    "Synthetic private summary",
    {
      claim: "Synthetic withheld material",
    },
    "run_private_source",
  );
  await insert(harness.db, "runs", {
    id: "run_later_source",
    kind: OPERATIONS.explore,
    preparation: JSON.stringify({ analysis: { brief: [{ id: "obs_000000a1" }] } }),
    started_at: new Date(NOW).toISOString(),
    closure: "completed",
    payload: "{}",
  });
  await record(
    "fnd_000000a2",
    "finding",
    "Synthetic later summary",
    {
      pattern: "Synthetic derivative",
    },
    "run_later_source",
  );
  await record(
    "obs_000000a3",
    "observation",
    "Independent synthetic sibling",
    {
      claim: "Allowed independent claim",
    },
    "run_allowed_source",
  );
  expect(await readExcludedRecordIds(harness.db)).toEqual(new Set());
  expect(await readExcludedRecordIds(harness.db, [selector])).toEqual(
    new Set(["obs_000000a1", "fnd_000000a2"]),
  );
  expect(
    await excludeSessionWhenQuiescent(
      harness.db,
      selector,
      "synthetic-owner",
      new Date(NOW).toISOString(),
    ),
  ).toBe(true);
  expect(await projectReview(harness.store, "obs_000000a1")).toBeNull();
  expect((await projectReview(harness.store, "obs_000000a3"))?.target["id"]).toBe("obs_000000a3");
  const service = host({ roster: [READY] });
  await rebuildTerms(corpus);
  const report = await backfillVectors(
    corpus,
    embedder(service.services),
    new Date(NOW).toISOString(),
    1,
  );
  expect(report.embedded).toBe(1);
  expect(service.asks).toHaveLength(1);
  expect(await harness.db.query("SELECT record_id FROM record_vectors")).toEqual([
    { record_id: "obs_000000a3" },
  ]);
  expect(await harness.db.query("SELECT id FROM records ORDER BY id")).toEqual([
    { id: "fnd_000000a2" },
    { id: "obs_000000a1" },
    { id: "obs_000000a3" },
  ]);
});

test("source exclusion refuses an affected unknown reservation but not an unrelated one", async () => {
  const selector = "codex/privacy-reservation-synthetic";
  await insert(harness.db, "runs", {
    id: "run_uncertain_private",
    kind: OPERATIONS.explore,
    preparation: JSON.stringify({ selectors: [selector] }),
    started_at: new Date(NOW).toISOString(),
    payload: JSON.stringify({ posting: true }),
  });
  expect(await readExcludedRunIds(harness.db, [selector])).toContain("run_uncertain_private");
  expect(
    await excludeSessionWhenQuiescent(
      harness.db,
      selector,
      "synthetic-owner",
      new Date(NOW).toISOString(),
    ),
  ).toBe(false);
  expect(
    await excludeSessionWhenQuiescent(
      harness.db,
      "omp/independent-synthetic",
      "synthetic-owner",
      new Date(NOW).toISOString(),
    ),
  ).toBe(true);
  expect(
    await harness.db.query(
      "SELECT closure,json_extract(payload,'$.posting') AS posting FROM runs WHERE id='run_uncertain_private'",
    ),
  ).toEqual([{ closure: null, posting: 1n }]);
});

test("an exclusion recorded during embedding readiness blocks the actual service admission", async () => {
  await record(
    "obs_000000b1",
    "observation",
    "Synthetic held embedding",
    { claim: "Synthetic private claim" },
    "run_embedding_fixture",
  );
  await insert(harness.db, "edges", {
    id: "edg_embedding_private",
    kind: "cites",
    from_kind: "observation",
    from_id: "obs_000000b1",
    to_kind: "session",
    to_id: "omp/embed-race-synthetic",
    actor_kind: "run",
    actor_id: "run_embedding_fixture",
    created_at: new Date(NOW).toISOString(),
  });
  await rebuildTerms(corpus);
  const service = host({ roster: [READY] });
  const roster = service.services.listInstances;
  service.services.listInstances = async (request) => {
    expect(
      await excludeSessionWhenQuiescent(
        harness.db,
        "omp/embed-race-synthetic",
        "synthetic-owner",
        new Date(NOW).toISOString(),
      ),
    ).toBe(true);
    return await roster(request);
  };
  const report = await backfillVectors(
    corpus,
    embedder(service.services),
    new Date(NOW).toISOString(),
  );
  expect(report.embedded).toBe(0);
  expect(service.asks).toEqual([]);
  expect(await harness.db.query("SELECT record_id FROM record_vectors")).toEqual([]);
});

test("an ambiguous embedding invocation retains its source reservation and is not replayed", async () => {
  await record(
    "obs_000000b2",
    "observation",
    "Synthetic uncertain embedding",
    { claim: "Synthetic claim" },
    "run_embedding_uncertain",
  );
  await insert(harness.db, "edges", {
    id: "edg_embedding_uncertain",
    kind: "cites",
    from_kind: "observation",
    from_id: "obs_000000b2",
    to_kind: "session",
    to_id: "omp/embed-uncertain-synthetic",
    actor_kind: "run",
    actor_id: "run_embedding_uncertain",
    created_at: new Date(NOW).toISOString(),
  });
  await rebuildTerms(corpus);
  const service = host({ roster: [READY] });
  let calls = 0;
  service.services.invokeInstance = async () => {
    calls++;
    throw new Error("synthetic interrupted response");
  };
  await backfillVectors(corpus, embedder(service.services), new Date(NOW).toISOString());
  await record(
    "obs_000000b3",
    "observation",
    "Independent embedding after uncertain delivery",
    { claim: "An unrelated record still gets its vector" },
    "run_embedding_independent",
  );
  service.services.invokeInstance = async () => {
    calls++;
    return {
      type: "service_result",
      requestId: "synthetic-sibling",
      ok: true,
      result: { embedding: [0.5, -0.5], model: "stub-embed-v1" },
    };
  };
  const resumed = await backfillVectors(
    corpus,
    embedder(service.services),
    new Date(NOW).toISOString(),
  );
  expect(resumed.embedded).toBe(1);
  await backfillVectors(corpus, embedder(service.services), new Date(NOW).toISOString());
  expect(calls).toBe(2);
  expect(await harness.db.query("SELECT record_id FROM record_vectors")).toEqual([
    { record_id: "obs_000000b3" },
  ]);
  expect(
    await excludeSessionWhenQuiescent(
      harness.db,
      "omp/embed-uncertain-synthetic",
      "synthetic-owner",
      new Date(NOW).toISOString(),
    ),
  ).toBe(false);
  expect(
    await harness.db.query("SELECT closure FROM runs WHERE kind=? AND closure IS NULL", [
      EMBEDDING_RUN_KIND,
    ]),
  ).toEqual([{ closure: null }]);
});

test("private generated metadata and typed saved graph references cannot re-enter reader or model context", async () => {
  const at = new Date(NOW).toISOString();
  await insert(harness.db, "runs", {
    id: "run_metadata_private",
    kind: OPERATIONS.explore,
    preparation: JSON.stringify({ selectors: ["omp/metadata-synthetic"] }),
    started_at: at,
    closure: "completed",
    finished_at: at,
    payload: "{}",
  });
  await record(
    "obs_000000b1",
    "observation",
    "Synthetic source-derived observation",
    {},
    "run_metadata_private",
  );
  await record(
    "obs_000000b2",
    "observation",
    "Synthetic fact-linked observation",
    {},
    "run_fact_linked",
  );
  await record(
    "obs_000000b3",
    "observation",
    "Synthetic independent observation",
    {},
    "run_independent_metadata",
  );
  for (const [id, createdBy] of [
    ["ent_000000b1", "run_metadata_private"],
    ["ent_000000b2", "synthetic-owner"],
    ["ent_000000b3", "synthetic-owner"],
  ] as const) {
    await insert(harness.db, "entities", {
      id,
      kind: "topic",
      name: "Synthetic topic",
      canonical_id: id,
      created_by: createdBy,
      created_at: at,
    });
  }
  await insert(harness.db, "facts", {
    id: "fact_private_synthetic",
    entity_id: "ent_000000b2",
    predicate: "description",
    value: "Synthetic generated fact",
    valid_from: at,
    observed_at: at,
    authority_kind: "run",
    authority_id: "run_metadata_private",
    recorded_at: at,
  });
  await insert(harness.db, "filings", {
    id: "fil_synthetic_metadata",
    record_id: "obs_000000b2",
    entity_id: "ent_000000b2",
    rationale: "Synthetic relationship",
    author_kind: "operator",
    author_id: "synthetic-owner",
    created_at: at,
  });
  for (const [id, raisedBy, payload] of [
    ["qst_000000b1", "run_metadata_private", {}],
    ["qst_000000b2", "synthetic-owner", { work: [{ kind: "observation", id: "obs_000000b1" }] }],
    ["qst_000000b3", "synthetic-owner", { work: [{ kind: "observation", id: "obs_000000b3" }] }],
  ] as const) {
    await insert(harness.db, "questions", {
      id,
      kind: "acquire-context",
      class: "curiosity",
      text: "Synthetic question",
      why: "Synthetic reason",
      raised_by_kind: raisedBy === "synthetic-owner" ? "operator" : "run",
      raised_by_id: raisedBy,
      payload: JSON.stringify(payload),
      created_at: at,
    });
  }
  for (const [id, value] of [
    ["capture_synthetic_entity", { entityId: "ent_000000b1" }],
    ["capture_synthetic_question", { questionId: "qst_000000b2" }],
    ["capture_synthetic_public", { entityId: "ent_000000b3", questionId: "qst_000000b3" }],
    [
      "capture_synthetic_run",
      { fromKind: "entity", fromId: "ent_000000b3", toKind: "run", toId: "run_metadata_private" },
    ],
    ["capture_synthetic_root", { kind: "root", id: "obs_000000b1" }],
  ] as const) {
    await insert(harness.db, "transcript_map_captures", {
      id,
      host: "synthetic-host",
      harness: "babel-neighborhood",
      session: `neighborhood/${id}`,
      captured_at: at,
      payload: "{}",
    });
    await insert(harness.db, "transcript_map_neighborhood_inputs", {
      capture_id: id,
      query_key: id,
      text: `${JSON.stringify({ kind: "nodes", value })}\n`,
    });
  }
  expect(
    await excludeSessionWhenQuiescent(harness.db, "omp/metadata-synthetic", "synthetic-owner", at),
  ).toBe(true);
  for (const [kind, table, allowed] of [
    ["entity", "entities", "ent_000000b3"],
    ["question", "questions", "qst_000000b3"],
    ["record", "records", "obs_000000b3"],
  ] as const) {
    const privacy = sourcePrivacyCondition(kind, "id");
    expect(
      await harness.db.query(`SELECT id FROM ${table} WHERE ${privacy.sql}`, privacy.params),
    ).toEqual([{ id: allowed }]);
  }
  expect([...(await readExcludedCaptureIds(harness.db))].sort()).toEqual([
    "capture_synthetic_entity",
    "capture_synthetic_question",
    "capture_synthetic_root",
    "capture_synthetic_run",
  ]);
});

test("typed payload citations and later private annotations quarantine whole records before corpus ranking", async () => {
  const at = new Date(NOW).toISOString();
  const source = "omp/attachments-synthetic";
  await insert(harness.db, "runs", {
    id: "run_private_attachments",
    kind: OPERATIONS.explore,
    preparation: JSON.stringify({ selectors: [source] }),
    started_at: at,
    closure: "completed",
    finished_at: at,
    payload: "{}",
  });
  for (let index = 1; index <= 7; index++) {
    const payload =
      index === 1
        ? { evidence: [{ selector: source }] }
        : index === 2
          ? {
              counter_evidence: [{ source: { harness: "omp", sourceId: "attachments-synthetic" } }],
            }
          : {};
    await record(
      `obs_000000c${index}`,
      "observation",
      "Synthetic session archive record",
      payload,
      `run_original_attachment_${index}`,
    );
  }
  await insert(harness.db, "assessments", {
    id: "assessment_private_synthetic",
    record_id: "obs_000000c3",
    revision_id: "obs_000000c3",
    run_id: "run_private_attachments",
    role: "judge",
    payload: JSON.stringify({ rationale: "Synthetic private assessment" }),
    recorded_at: at,
  });
  await insert(harness.db, "entities", {
    id: "ent_000000c1",
    kind: "topic",
    name: "Synthetic annotation topic",
    canonical_id: "ent_000000c1",
    created_by: "synthetic-owner",
    created_at: at,
  });
  await insert(harness.db, "filings", {
    id: "fil_private_annotation",
    record_id: "obs_000000c4",
    entity_id: "ent_000000c1",
    rationale: "Synthetic private filing",
    author_kind: "run",
    author_id: "run_private_attachments",
    created_at: at,
  });
  await insert(harness.db, "next_actions", {
    id: "nxt_private_annotation",
    record_id: "obs_000000c5",
    kind: "draft-issue",
    proposed_by_kind: "run",
    proposed_by_id: "run_private_attachments",
    summary: "Synthetic private proposal",
    created_at: at,
    payload: "{}",
  });
  await insert(harness.db, "status_events", {
    id: "status_private_annotation",
    record_id: "obs_000000c6",
    seq: 0,
    status: "open",
    run_id: "run_private_attachments",
    actor_kind: "run",
    actor_id: "run_private_attachments",
    reason: "Synthetic private status",
    recorded_at: at,
  });
  await rebuildTerms(corpus);
  await backfillVectors(corpus, stubEmbedder("stub-source-privacy"), at, 10);
  expect(await excludeSessionWhenQuiescent(harness.db, source, "synthetic-owner", at)).toBe(true);
  expect([...(await readExcludedRecordIds(harness.db))].sort()).toEqual([
    "obs_000000c1",
    "obs_000000c2",
    "obs_000000c3",
    "obs_000000c4",
    "obs_000000c5",
    "obs_000000c6",
  ]);
  const answer = await searchCorpus(corpus, stubEmbedder("stub-source-privacy"), {
    query: "session archive",
    limit: 1,
    kinds: [],
  });
  expect(answer.hits.map((hit) => hit.id)).toEqual(["obs_000000c7"]);
  expect(answer).toMatchObject({
    scanned: 1,
    rescored: 1,
    coverage: { records: 1, keyworded: 1, embedded: 1 },
  });
  const retained = await harness.db.query<{ records: number; vectors: number }>(
    "SELECT (SELECT COUNT(*) FROM records) AS records, (SELECT COUNT(*) FROM record_vectors) AS vectors",
  );
  expect({ records: Number(retained[0]?.records), vectors: Number(retained[0]?.vectors) }).toEqual({
    records: 7,
    vectors: 7,
  });
});

test("canonical exclusions cover legacy bare session citations without removing their records", async () => {
  const at = new Date(NOW).toISOString();
  await insert(harness.db, "sessions", {
    selector: "omp/legacy-private-synthetic",
    host: "synthetic-host",
    harness: "omp",
    source_id: "legacy-private-synthetic",
    seen_at: at,
  });
  await record(
    "obs_000000d1",
    "observation",
    "Legacy private citation",
    {
      claim: "Synthetic forbidden corpus material",
    },
    "run_legacy_private_synthetic",
  );
  await record(
    "obs_000000d2",
    "observation",
    "Independent corpus material",
    {
      claim: "Synthetic allowed corpus material",
    },
    "run_legacy_public_synthetic",
  );
  await insert(harness.db, "edges", {
    id: "edg_legacy_source_synthetic",
    kind: "cites",
    from_kind: "observation",
    from_id: "obs_000000d1",
    to_kind: "session",
    to_id: "legacy-private-synthetic",
    actor_kind: "operator",
    actor_id: "synthetic-owner",
    created_at: at,
  });
  expect(
    await excludeSessionWhenQuiescent(
      harness.db,
      "omp/legacy-private-synthetic",
      "synthetic-owner",
      at,
    ),
  ).toBe(true);
  expect(await projectReview(harness.store, "obs_000000d1")).toBeNull();
  expect(
    (await searchCorpus(corpus, null, { query: "corpus material", limit: 10, kinds: [] })).hits.map(
      (hit) => hit.id,
    ),
  ).toEqual(["obs_000000d2"]);
  expect(await harness.db.query("SELECT record_id FROM record_terms")).toEqual([
    { record_id: "obs_000000d2" },
  ]);
  expect(await harness.db.query("SELECT id FROM records ORDER BY id")).toEqual([
    { id: "obs_000000d1" },
    { id: "obs_000000d2" },
  ]);
});

test("a concurrent exclusion discards ranks derived from the previous private keyword corpus", async () => {
  const at = new Date(NOW).toISOString();
  await record(
    "obs_000000f1",
    "observation",
    "Private keyword statistics",
    {
      claim: "Synthetic corpus material corpus material",
      evidence: [{ selector: "omp/ranking-private-synthetic" }],
    },
    "run_ranking_private_synthetic",
  );
  await record(
    "obs_000000f2",
    "observation",
    "Independent keyword match",
    {
      claim: "Synthetic corpus material",
    },
    "run_ranking_public_synthetic",
  );
  await expect(
    searchCorpus(
      corpus,
      async () => {
        expect(
          await excludeSessionWhenQuiescent(
            harness.db,
            "omp/ranking-private-synthetic",
            "synthetic-owner",
            at,
          ),
        ).toBe(true);
        return null;
      },
      { query: "corpus material", limit: 10, kinds: [] },
    ),
  ).rejects.toThrow("Conversation privacy policy changed during search.");
  const current = await searchCorpus(corpus, null, {
    query: "corpus material",
    limit: 10,
    kinds: [],
  });
  expect(current.hits.map((hit) => hit.id)).toEqual(["obs_000000f2"]);
  expect(current.coverage.records).toBe(1);
});

test("recorded source titles remain usable while historical consumers of excluded inferred titles stay quarantined", async () => {
  const at = new Date(NOW).toISOString();
  await insert(harness.db, "runs", {
    id: "run_title_batch_synthetic",
    kind: OPERATIONS.title,
    started_at: at,
    closure: "completed",
    preparation: JSON.stringify({
      titles: { selectors: ["omp/title-private-synthetic", "omp/title-public-synthetic"] },
    }),
    payload: "{}",
  });
  await insert(harness.db, "sessions", {
    selector: "omp/title-public-synthetic",
    host: "synthetic-host",
    harness: "omp",
    source_id: "title-public-synthetic",
    title: "Recorded public source title",
    title_provenance: "recorded",
    seen_at: at,
  });
  await insert(harness.db, "session_titles", {
    selector: "omp/title-public-synthetic",
    title: "Old generated source title",
    reason: "",
    run_id: "run_title_batch_synthetic",
    inferred_at: at,
  });
  await record(
    "obs_000000e1",
    "observation",
    "Fresh independent source record",
    { claim: "Fresh independently observed material" },
    "run_fresh_title_source_synthetic",
  );
  await insert(harness.db, "edges", {
    id: "edg_fresh_title_synthetic",
    kind: "cites",
    from_kind: "observation",
    from_id: "obs_000000e1",
    to_kind: "session",
    to_id: "omp/title-public-synthetic",
    actor_kind: "operator",
    actor_id: "synthetic-owner",
    created_at: at,
  });
  await insert(harness.db, "runs", {
    id: "run_historical_title_consumer_synthetic",
    kind: OPERATIONS.evaluate,
    started_at: at,
    closure: "completed",
    payload: "{}",
    preparation: JSON.stringify({
      review: {
        recordId: "obs_000000e1",
        titleRunIds: ["run_title_batch_synthetic"],
      },
    }),
  });
  await record(
    "obs_000000e2",
    "observation",
    "Historical generated-title consumer",
    { claim: "Synthetic historical derivation" },
    "run_historical_title_consumer_synthetic",
  );
  expect(
    await excludeSessionWhenQuiescent(
      harness.db,
      "omp/title-private-synthetic",
      "synthetic-owner",
      at,
    ),
  ).toBe(true);
  expect([...(await readExcludedRecordIds(harness.db))]).toEqual(["obs_000000e2"]);
  const projected = await projectReview(harness.store, "obs_000000e1");
  expect(projected?.titleRunIds).toEqual([]);
  expect(projected?.sources[0]?.["title"]).toBe("Recorded public source title");
  expect(await projectReview(harness.store, "obs_000000e2")).toBeNull();
});
