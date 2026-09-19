import { afterEach, beforeEach, expect, test } from "bun:test";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import { EMBEDDING_SERVICE } from "../contract.ts";
import { askEmbedding, embedder, type EmbeddingServices } from "../server/embed.ts";
import {
  PROBE_DEPTH,
  backfillVectors,
  ensureTerms,
  rebuildTerms,
  searchCorpus,
  termsQuery,
  type CorpusStore,
  type Embedder,
} from "./corpus.ts";
import { insert, openTestStore, type TestStore } from "./testdb.ts";

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
  payload: Readonly<Record<string, string>>,
): Promise<void> {
  await insert(harness.db, "records", {
    id,
    kind,
    root_id: id,
    seq: 0,
    run_id: PLANTED,
    actor_kind: "run",
    actor_id: PLANTED,
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
