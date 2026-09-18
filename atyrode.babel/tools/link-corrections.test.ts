import { expect, test } from "bun:test";
import { insert, openTestStore } from "../store/testdb.ts";
import { planSweep, report, writeRepairs } from "./link-corrections.ts";

/*
  THE SWEEP OVER THE CORPUS THAT ALREADY CARRIES THE MARKERS (#347).

  The fixture is the corpus's own four shapes: a marker naming a record the store holds, one
  naming a record it does not, one naming a run-local handle, and one naming only prose. The
  last two are the majority case — nine of the operator's eleven markers — and the sweep's job
  there is to COUNT them rather than to guess, because the handle-to-row mapping died with the
  run that made it.
*/

const HELD = "hyp_d7ae09a99a97653083bc9d97c70304bd";
const MISSING = "obs_4f5a3a81da954817d7136a96b39350db";

const AT = "2026-09-18T00:00:00.000Z";

async function seed(
  db: Parameters<typeof insert>[0],
  id: string,
  kind: string,
  headline: string,
  field: string,
): Promise<void> {
  await insert(db, "records", {
    id,
    kind,
    root_id: id,
    seq: 0,
    run_id: "run_seed",
    actor_kind: "run",
    actor_id: "run_seed",
    // Truncated exactly as the column holds it, so the sweep is proven to read the payload and
    // not the cell: a marker's targets sit past 200 characters often enough to matter.
    title: headline.slice(0, 200),
    created_at: AT,
    payload: JSON.stringify({ [field]: headline }),
  });
}

async function corpus() {
  const store = await openTestStore(Date.parse(AT));
  await seed(
    store.db,
    HELD,
    "hypothesis",
    "The records a brief names are retrievable",
    "statement",
  );
  await seed(
    store.db,
    "fnd_00000000000000000000000000000001",
    "finding",
    `CONTRADICTS ${HELD} in its strongest form: retrieval works, but only by content`,
    "title",
  );
  await seed(
    store.db,
    "obs_00000000000000000000000000000002",
    "observation",
    `CONTRADICTS ${MISSING}, which states that the required fix was never performed`,
    "claim",
  );
  await seed(
    store.db,
    "obs_00000000000000000000000000000003",
    "observation",
    "CITATION CORRECTION for o2 (which cited e17 in error): the claim rests on e49",
    "claim",
  );
  await seed(
    store.db,
    "obs_00000000000000000000000000000004",
    "observation",
    "CORRECTION narrowing my earlier secret-residency claim on this candidate: /tmp/ok.txt",
    "claim",
  );
  return store;
}

test("the sweep writes the edges the corpus already stated, and only those", async () => {
  const store = await corpus();
  try {
    const plan = await planSweep(store.db);
    expect(plan.marked).toBe(4);
    expect(await writeRepairs(store.db, plan.repairs, AT)).toBe(1);

    expect(
      await store.db.query(
        `SELECT kind, from_id, to_id, to_kind, actor_kind, actor_id, note FROM edges`,
      ),
    ).toEqual([
      {
        kind: "contradicts",
        from_id: "fnd_00000000000000000000000000000001",
        to_id: HELD,
        to_kind: "hypothesis",
        // NOT a run: no run wrote this edge, and attributing the repair to one would make it
        // indistinguishable from what the settlement itself produced.
        actor_kind: "engine",
        actor_id: "link-corrections",
        note: "the record's own text opens CONTRADICTS",
      },
    ]);
  } finally {
    store.close();
  }
});

test("a second sweep writes nothing and reports zero", async () => {
  const store = await corpus();
  try {
    const first = await planSweep(store.db);
    expect(await writeRepairs(store.db, first.repairs, AT)).toBe(1);
    // A DIFFERENT INSTANT ON THE SECOND PASS. The identifier is a digest of the relation, so a
    // sweep run a month later must still recognise its own earlier work rather than write a
    // second edge for the same sentence.
    const again = await planSweep(store.db);
    expect(again.repairs).toHaveLength(1);
    expect(await writeRepairs(store.db, again.repairs, "2026-10-18T00:00:00.000Z")).toBe(0);
    expect(await store.db.query(`SELECT COUNT(*) AS n FROM edges`)).toEqual([{ n: 1n }]);
  } finally {
    store.close();
  }
});

test("a marker naming what the store cannot resolve is counted, by why, and never fails", async () => {
  const store = await corpus();
  try {
    const plan = await planSweep(store.db);
    expect(plan.dropped).toEqual([
      {
        recordId: "obs_00000000000000000000000000000002",
        reference: MISSING,
        why: "names a record this store does not hold",
      },
      {
        recordId: "obs_00000000000000000000000000000003",
        reference: "o2",
        why: "names a run-local handle whose run is settled, so the mapping no longer exists",
      },
      {
        recordId: "obs_00000000000000000000000000000004",
        reference: "",
        why: "names prose where an identifier belongs",
      },
    ]);

    // The operator reads the counts, so the counts are what the report leads with.
    const written = await writeRepairs(store.db, plan.repairs, AT);
    const text = report(plan, written);
    expect(text).toContain("1 edges written");
    expect(text).toContain("3 references dropped");
    expect(text).toContain(
      "1  names a run-local handle whose run is settled, so the mapping no longer exists",
    );
  } finally {
    store.close();
  }
});
