import { expect, test } from "bun:test";
import { JOB_OUTPUT_FILES, RECORD_RESTS_ON_ONE_RUN, type MaterialEntry } from "../../contract.ts";
import { parseExploreResult, type ExploreResult } from "../../machine/results.ts";
import { insert, openTestStore } from "../../store/testdb.ts";
import { correctionMarker, exploreRows, markerReferences } from "./records.ts";
import type { Cell, Row } from "./rows.ts";

/*
  WHAT A RECORD'S OWN FIRST WORDS ARE ALLOWED TO ASSERT ABOUT ANOTHER RECORD (#347).

  The grammar is exercised against the corpus's own eleven markers, quoted verbatim, because a
  parser proved against invented inputs proves the inventor's idea of the grammar. The two
  shapes it must reject are the expensive ones: a record that MENTIONS another record, and a
  possessive that reads the same as a target and means a different one.

  The edges are exercised through `exploreRows` — the only thing that turns an answer into rows
  — and the rows are then written to a real store, so `STRICT`, the append-only triggers and
  the column names are in force rather than assumed.
*/

const SESSION: MaterialEntry = {
  selector: "omp/s1",
  harness: "omp",
  sourceId: "s1",
  captureDigest: "sha256:cap",
  sourceDigest: "sha256:aaa",
  file: "s1.jsonl",
  records: 1,
  bytes: 10,
};

const LOCATOR = { path: "s1.jsonl", line: 1, byte_offset: 0, digest: "sha256:aaa" };

/** One candidate with one locator-backed observation, as the answer schema shapes it. */
function answer(options: {
  statement: string;
  claim: string;
  extra?: Record<string, unknown>;
}): ExploreResult {
  return parseExploreResult("explore", {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: options.statement },
        observations: [
          {
            ref: "o1",
            recipe: { id: "test-economics", version: 3 },
            claim: {
              claim: options.claim,
              confidence: "moderate",
              impact: "moderate",
              evidence: [{ locator: LOCATOR, note: "the line" }],
              counter_evidence_absent: true,
            },
          },
        ],
      },
    ],
    ...options.extra,
  });
}

function edgesOf(rows: Readonly<Record<string, readonly Row[]>>): readonly Row[] {
  return rows[JOB_OUTPUT_FILES.edges] ?? [];
}

function settle(result: ExploreResult, holds: readonly string[] = [], runId = "run_1") {
  const written = exploreRows(result, {
    runId,
    at: "2026-09-18T00:00:00.000Z",
    sessions: [SESSION],
    holds: new Set(holds),
  });
  if ("refusal" in written) throw new Error(`refused: ${written.refusal.message}`);
  return written;
}

// ------------------------------------------------------------------------------- the grammar

test("the grammar reads the corpus's own markers and nothing that merely resembles one", () => {
  // The two that name durable identifiers, verbatim from `frontier_hypothesis`.
  expect(
    correctionMarker(
      "CONTRADICTS frontier observation obs_4f5a3a81da954817d7136a96b39350db, which states " +
        'that after the owner-key exposure of 2026-08-28 "the required fix was never performed".',
    ),
  ).toEqual({
    relation: "contradicts",
    token: "CONTRADICTS",
    references: ["obs_4f5a3a81da954817d7136a96b39350db"],
  });
  // Two targets joined by `and`, and the prose after the colon contributes nothing.
  expect(
    correctionMarker(
      "CONTRADICTS hyp_d7ae09a99a97653083bc9d97c70304bd and hyp_adbd95c899af4671b6cc5c1cc35439ab " +
        "in their strongest form: the records a Babel brief names are present and retrievable.",
    )?.references,
  ).toEqual(["hyp_d7ae09a99a97653083bc9d97c70304bd", "hyp_adbd95c899af4671b6cc5c1cc35439ab"]);

  // A run-local handle, with the qualifier three of the corpus's corrections open with. `e17`
  // is an EVIDENCE handle inside an aside and is not a target: the list ends at the first
  // thing that is not a separator.
  expect(
    correctionMarker("CITATION CORRECTION for o2 (which cited e17 in error): the claim"),
  ).toEqual({ relation: "corrects", token: "CORRECTION", references: ["o2"] });

  // A marker naming only prose is a marker, and its emptiness is the fact worth reporting.
  expect(
    correctionMarker(
      "CORRECTION narrowing my earlier secret-residency claim on this candidate: /tmp/ok.txt " +
        "did not persist continuously from 17:31 to 20:50.",
    ),
  ).toEqual({ relation: "corrects", token: "CORRECTION", references: [] });
  expect(
    correctionMarker("CORRECTION of citations in the earlier secret-file claim on this candidate")
      ?.references,
  ).toEqual([]);

  // A POSSESSIVE IS NEVER A TARGET. `o12's citation handles` means o12 and `o1's sibling` means
  // a record o1 is not; the two are the same shape, so both are dropped rather than one of them
  // becoming an edge to the wrong record.
  expect(correctionMarker("CORRECTION of o12's citation handles (o12 cited e67/e68)")).toEqual({
    relation: "corrects",
    token: "CORRECTION",
    references: [],
  });
  expect(
    correctionMarker("CORRECTION of observation o1's sibling (supersedes the mis-cited claim)")
      ?.references,
  ).toEqual([]);
});

test("a record that mentions another record is discussing it, not correcting it", () => {
  expect(
    correctionMarker(
      "The retrieval path never returns hyp_d7ae09a99a97653083bc9d97c70304bd by identifier, " +
        "which contradicts the brief.",
    ),
  ).toBeNull();
  // Lower case is prose; the marker is a literal token.
  expect(correctionMarker("contradicts hyp_d7ae09a99a97653083bc9d97c70304bd")).toBeNull();
  // A word that merely starts with the token is not the token.
  expect(
    correctionMarker("CONTRADICTORY findings in obs_4f5a3a81da954817d7136a96b39350db"),
  ).toBeNull();
  // The token has to open the text, not appear in it.
  expect(correctionMarker("As noted, CONTRADICTS hyp_00000000aaaa")).toBeNull();
});

test("only durable identifiers are asked of the store; handles never are", () => {
  const result = answer({
    statement: "CONTRADICTS hyp_d7ae09a99a97653083bc9d97c70304bd in its strongest form",
    claim: "CITATION CORRECTION for h1 (which cited e17 in error): the claim rests elsewhere",
  });
  expect(markerReferences(result)).toEqual(["hyp_d7ae09a99a97653083bc9d97c70304bd"]);
});

// ------------------------------------------------------------------------------- the edges

test("a record correcting a handle this answer declared gets the edge at creation", async () => {
  // The observation corrects the candidate beside it by the handle the answer gave it, which is
  // the form nine of the corpus's eleven markers use and the only place it is still resolvable.
  const written = settle(
    answer({
      statement: "The advisory notices lag by construction",
      claim: "CITATION CORRECTION for h1 (which cited e17 in error): the handles were misread",
    }),
  );
  const corrects = edgesOf(written.rows).filter((row) => row["kind"] === "corrects");
  expect(corrects).toHaveLength(1);
  const edge = corrects[0];
  expect(edge?.["from_kind"]).toBe("observation");
  expect(edge?.["to_kind"]).toBe("hypothesis");
  expect(edge?.["note"]).toBe("the record's own text opens CORRECTION");
  // The target is the ROW the handle became, not the handle.
  const records = written.rows[JOB_OUTPUT_FILES.records] ?? [];
  expect(edge?.["to_id"]).toBe(records[0]?.["id"] as Cell);
  expect(written.notes).toEqual([]);

  // The row the writer produced is one the store's own schema takes.
  const store = await openTestStore(Date.parse("2026-09-18T00:00:00.000Z"));
  try {
    for (const row of records) await insert(store.db, "records", row);
    for (const row of edgesOf(written.rows)) await insert(store.db, "edges", row);
    expect(
      await store.db.query<{ kind: string; to_id: string }>(
        `SELECT kind, to_id FROM edges WHERE kind = 'corrects'`,
      ),
    ).toEqual([{ kind: "corrects", to_id: String(records[0]?.["id"]) }]);
  } finally {
    store.close();
  }
});

test("a marker resolves a handle declared later in the same answer", () => {
  // The candidate is written before the consolidation that names it, so a pass interleaved with
  // creation would resolve this one by luck of ordering and the reverse case not at all.
  const result = parseExploreResult("explore", {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: "CONTRADICTS c1 on the same evidence" },
        observations: [
          {
            ref: "o1",
            recipe: { id: "test-economics", version: 3 },
            claim: {
              claim: "the wrapper tests only its own control flow",
              confidence: "moderate",
              impact: "moderate",
              evidence: [{ locator: LOCATOR, note: "the line" }],
              counter_evidence_absent: true,
            },
          },
        ],
      },
    ],
    consolidations: [
      {
        ref: "c1",
        observations: ["o1"],
        finding: {
          title: "verification is shallow",
          pattern: "stubs",
          counter_evidence_absent: true,
        },
      },
    ],
  });
  const written = settle(result);
  const contradicts = edgesOf(written.rows).filter((row) => row["kind"] === "contradicts");
  expect(contradicts).toHaveLength(1);
  expect(contradicts[0]?.["to_kind"]).toBe("finding");
});

test("a durable identifier this hub holds is linked and one it does not is dropped with a note", () => {
  const held = "hyp_d7ae09a99a97653083bc9d97c70304bd";
  const missing = "obs_4f5a3a81da954817d7136a96b39350db";
  const result = answer({
    statement: `CONTRADICTS ${held} and ${missing} in their strongest form`,
    claim: "the records a brief names are retrievable only by content",
  });

  const linked = settle(result, [held]);
  const contradicts = edgesOf(linked.rows).filter((row) => row["kind"] === "contradicts");
  expect(contradicts.map((row) => row["to_id"])).toEqual([held]);
  expect(linked.notes).toEqual([
    expect.stringContaining(`${missing}, which this hub does not hold`) as unknown as string,
  ]);

  // NOTHING IS REFUSED for it. The run produced its records; what it referred to is the
  // corpus's business, and a settlement that failed here would lose an answer over a citation
  // of something since removed.
  expect(linked.rows[JOB_OUTPUT_FILES.records]).toHaveLength(2);
});

test("a marker naming prose, or a handle nobody declared, leaves a note and no edge", () => {
  const written = settle(
    answer({
      statement: "CORRECTION narrowing my earlier secret-residency claim on this candidate",
      claim: "CORRECTS o9 which this answer never declared",
    }),
  );
  expect(edgesOf(written.rows).filter((row) => row["kind"] === "corrects")).toHaveLength(0);
  expect(written.notes).toEqual([
    expect.stringContaining("opens CORRECTION and names no identifier") as unknown as string,
    expect.stringContaining("against o9, which this result did not declare") as unknown as string,
  ]);
});

// -------------------------------------------------- what a record rests on, at creation (#329)

/*
  A FINDING RESTING ON ONE RUN IS MARKED AS RESTING ON ONE RUN, AND IS STILL WRITTEN.

  Three observations under one finding read as corroboration; three from one run are one reading
  restated. The store could always count it (`store/store.ts`, `corroborationOf`) and nothing
  could rank or filter on it, because the fact lived nowhere a query reaches. These hold both
  halves: that the writer states it, and that it states the same thing the store's own count
  does — two definitions of resting on one run that disagreed would be worse than none.
*/

/** One locator-backed observation, as the answer schema shapes it. */
function observed(ref: string, claim: string) {
  return {
    ref,
    recipe: { id: "test-economics", version: 3 },
    claim: {
      claim,
      confidence: "moderate",
      impact: "moderate",
      evidence: [{ locator: LOCATOR, note: "the line" }],
      counter_evidence_absent: true,
    },
  };
}

/** An answer whose finding consolidates exactly the observation handles named. */
function consolidating(observations: readonly string[]): ExploreResult {
  return parseExploreResult("explore", {
    candidates: [
      {
        ref: "h1",
        hypothesis: { statement: "the advisory notices lag by construction" },
        observations: [observed("o1", "the first reading"), observed("o2", "the second reading")],
      },
    ],
    consolidations: [
      {
        ref: "c1",
        observations,
        finding: {
          title: "the lag is structural",
          pattern: "every notice lands after the window it describes",
          counter_evidence_absent: true,
        },
      },
    ],
  });
}

/** An observation an earlier run wrote, which is the only kind a brief can name. */
const EARLIER = "obs_1c0d2f5aa3b64e5d9fbb0d1f0e7c4a21";

function recordsOf(rows: Readonly<Record<string, readonly Row[]>>): readonly Row[] {
  return rows[JOB_OUTPUT_FILES.records] ?? [];
}

function payloadOf(row: Row | undefined): Record<string, unknown> {
  return JSON.parse(String(row?.["payload"] ?? "{}")) as Record<string, unknown>;
}

function findingIn(rows: Readonly<Record<string, readonly Row[]>>): Row | undefined {
  return recordsOf(rows).find((row) => row["kind"] === "finding");
}

test("a finding resting on this run's own observations is marked, and is written anyway", () => {
  const written = settle(consolidating(["o1", "o2"]));

  // IT IS NOT REFUSED, and may never be: 175 of the 207 findings in this deployment's corpus
  // rest on a single run, so a rule that rejected the shape would reject most of the corpus it
  // was written for. `settle` throws on a refusal, and all four rows are here.
  expect(recordsOf(written.rows)).toHaveLength(4);
  expect(payloadOf(findingIn(written.rows))[RECORD_RESTS_ON_ONE_RUN]).toBe(true);

  // A hypothesis and an observation rest on no RECORD at all — an observation's evidence points
  // at a session — so they carry no determination rather than a false one.
  const hypothesis = recordsOf(written.rows).find((row) => row["kind"] === "hypothesis");
  expect(payloadOf(hypothesis)).not.toHaveProperty(RECORD_RESTS_ON_ONE_RUN);
});

test("a finding that also rests on an earlier run's observation is not marked", () => {
  const written = settle(consolidating(["o1", EARLIER]));
  expect(payloadOf(findingIn(written.rows))[RECORD_RESTS_ON_ONE_RUN]).toBe(false);
});

test("the mark at creation and the store's own count are one definition", async () => {
  const single = settle(consolidating(["o1", "o2"]));
  const spread = settle(consolidating(["o1", EARLIER]), [], "run_2");
  const store = await openTestStore(Date.parse("2026-09-18T00:00:00.000Z"));
  try {
    await insert(store.db, "records", {
      id: EARLIER,
      kind: "observation",
      root_id: EARLIER,
      seq: 0,
      run_id: "run_0",
      actor_kind: "run",
      actor_id: "run_0",
      title: "a reading an earlier run wrote",
      created_at: "2026-09-17T00:00:00.000Z",
      payload: JSON.stringify({ schema: 1, claim: "it happened" }),
    });
    for (const rows of [single.rows, spread.rows]) {
      for (const row of recordsOf(rows)) await insert(store.db, "records", row);
      for (const row of edgesOf(rows)) await insert(store.db, "edges", row);
    }

    const restsOnOne = findingIn(single.rows);
    const restsOnTwo = findingIn(spread.rows);
    const one = await store.store.record(String(restsOnOne?.["id"]));
    const two = await store.store.record(String(restsOnTwo?.["id"]));
    expect(one?.corroboration).toEqual({ supports: 2, distinctRuns: 1 });
    expect(two?.corroboration).toEqual({ supports: 2, distinctRuns: 2 });
    expect(payloadOf(restsOnOne)[RECORD_RESTS_ON_ONE_RUN]).toBe(
      one?.corroboration.distinctRuns === 1,
    );
    expect(payloadOf(restsOnTwo)[RECORD_RESTS_ON_ONE_RUN]).toBe(
      two?.corroboration.distinctRuns === 1,
    );

    // The whole point of persisting it: a query can now select on it, which no join at read
    // time let a listing do.
    const marked = await store.db.query<{ id: string }>(
      `SELECT id FROM records WHERE json_extract(payload, '$.${RECORD_RESTS_ON_ONE_RUN}') = 1`,
    );
    expect(marked.map((row) => row.id)).toEqual([String(restsOnOne?.["id"])]);
  } finally {
    store.close();
  }
});
