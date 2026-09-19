import { expect, test } from "bun:test";
import { deliveryOrder, type Packed } from "./delivery-order.ts";

/*
  WHAT THE RELEASE JOB NO LONGER KNOWS BY HEART. The delivery order used to be a list of ids in
  `release.yml`, and the only thing that checked it was the operator's memory; it is now read off
  the packed bundles' own declared dependencies. These are the properties the workflow now rests
  on, over constructed manifests rather than a packed tree: a real `dist` would pin this family's
  present shape, and the point of the change is that a family it has never seen also comes out
  right.
*/

/** A manifest as the ordering sees it: an id, what it requires, and the bytes it was read from. */
function packed(id: string, ...requires: readonly string[]): Packed {
  const file = `dist/${id}.manifold-plugin.json`;
  return { id, requiredDependencies: requires, sha256: id, source: file, file };
}

/** The four-family closure a tag delivers today: omp, then Code, then Babel and its parts. */
const CLOSURE: readonly Packed[] = [
  packed("atyrode.babel.jev", "atyrode.babel"),
  packed("atyrode.babel", "atyrode.code"),
  packed("atyrode.babel.feed", "atyrode.babel"),
  packed("atyrode.babel.watch", "atyrode.babel"),
  packed("atyrode.code", "atyrode.omp", "atyrode.omp.accounts", "atyrode.omp.gateway"),
  packed("atyrode.code.accounts", "atyrode.code", "atyrode.omp.accounts"),
  packed("atyrode.code.generator", "atyrode.code"),
  packed("atyrode.code.usage", "atyrode.code", "atyrode.omp.accounts"),
  packed("atyrode.omp"),
  packed("atyrode.omp.accounts", "atyrode.omp"),
  packed("atyrode.omp.gateway", "atyrode.omp"),
];

/** Every prerequisite present in the set — declared or a namespace parent — comes first. */
function prerequisiteAfterConsumer(order: readonly Packed[]): string | undefined {
  const at = new Map(order.map((bundle, index) => [bundle.id, index]));
  for (const [index, bundle] of order.entries()) {
    const needed = [...bundle.requiredDependencies];
    for (let dot = bundle.id.lastIndexOf("."); dot > 0; dot = bundle.id.lastIndexOf(".", dot - 1)) {
      needed.push(bundle.id.slice(0, dot));
    }
    for (const id of needed) {
      const supplier = at.get(id);
      if (supplier !== undefined && supplier > index) return `${bundle.id} before ${id}`;
    }
  }
  return undefined;
}

test("a dependency and a baseline are delivered before whatever needs them", () => {
  const { order, external } = deliveryOrder(CLOSURE);
  expect(order).toHaveLength(CLOSURE.length);
  expect(prerequisiteAfterConsumer(order)).toBeUndefined();
  expect(external).toEqual([]);
  // Only omp is free of a prerequisite present here, so the receiver's first call is fixed.
  expect(order[0]?.id).toBe("atyrode.omp");
});

test("a plugin nobody wrote down is delivered, in its place", () => {
  // The whole observable difference: a new part reaches the preview with no edit to release.yml.
  const order = deliveryOrder([...CLOSURE, packed("atyrode.babel.ledger", "atyrode.babel")]).order;
  expect(order.map((bundle) => bundle.id)).toContain("atyrode.babel.ledger");
  expect(prerequisiteAfterConsumer(order)).toBeUndefined();
});

test("a dependency that is not in the set keeps its place and is named, not dropped", () => {
  /*
    The cross-family case. The job delivers this repository's bundles beside a dependency
    family's, so the two halves are ordered together — but a bundle whose prerequisite is absent
    is one the hub already has, and ordering it last "to be safe" would put Babel's parts before
    Babel. Only the hub decides whether an external prerequisite is available.
  */
  const { order, external } = deliveryOrder(
    CLOSURE.filter((bundle) => bundle.id.startsWith("atyrode.babel")),
  );
  expect(order.map((bundle) => bundle.id)).toEqual([
    "atyrode.babel",
    "atyrode.babel.feed",
    "atyrode.babel.jev",
    "atyrode.babel.watch",
  ]);
  expect(external).toEqual(["atyrode.code"]);
});

test("a cycle is refused, naming the bundles it blocks", () => {
  const order = (): unknown =>
    deliveryOrder([
      packed("atyrode.one", "atyrode.two"),
      packed("atyrode.two", "atyrode.one"),
      packed("atyrode.three"),
    ]);
  expect(order).toThrow(/dependency cycle/);
  expect(order).toThrow(/atyrode\.one, atyrode\.two/);
});

test("the same id from two directories is refused rather than delivered twice", () => {
  // `deps/` carrying a copy of a bundle `dist/` also carries is a closure built wrong, and the
  // second install would replace the first with bytes nothing verified in that position.
  const twice: Packed = { ...packed("atyrode.babel", "atyrode.code"), file: "deps/babel.json" };
  expect(() => deliveryOrder([...CLOSURE, twice])).toThrow(/duplicate bundle ids: atyrode\.babel/);
});
