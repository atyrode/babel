import "./dom.ts";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { afterEach, expect, test } from "bun:test";
import { useState } from "react";
import { JEV_ACTIONS, JEV_PLUGIN_ID, type RecipeStanding } from "../../contract.ts";
import { Recipes } from "../recipes.tsx";
import { Watch } from "../web.tsx";
import { MACHINES, POLICY, fakeHost, runsResult, watchDoors } from "./host.ts";
import { click, mount, settle, unmountAll } from "./render.tsx";

/*
  THE RECIPE THAT HAS NEVER RUN IS THE ONE WORTH SEEING (#344).

  The roster used to be a `GROUP BY` over the runs table, so the fifteen recipes the operator
  installed and nothing had performed were not on the screen at all — the panel could report
  what had happened and nothing else. Now they are rows, and a row saying nothing happened has
  to read as a state rather than as a small number among large ones, or it is the same absence
  wearing a zero.
*/

const ROW = ".plugin-atyrode_babel_watch__recipe";

afterEach(async () => {
  await unmountAll();
  resetPolledResources();
});

async function open(): Promise<HTMLElement> {
  const fake = fakeHost(watchDoors({ runs: () => runsResult([]), policy: () => POLICY }), MACHINES);
  const root = await mount(<Watch host={fake.host} />);
  await settle();
  return root;
}

function section(root: HTMLElement): HTMLElement {
  // Every section on the panel carries the same section and lede classes, so the Recipes one is
  // reached through the only list of recipes.
  const found = root
    .querySelector(".plugin-atyrode_babel_watch__recipes")
    ?.closest(".plugin-atyrode_babel_watch__section");
  if (found === null || found === undefined) {
    throw new Error("the Recipes section is not on the screen");
  }
  return found as HTMLElement;
}

test("a recipe in force that nothing has performed is on the screen, marked as never run", async () => {
  const root = await open();
  const rows = [...root.querySelectorAll(ROW)];
  expect(rows).toHaveLength(2);

  // The nameless one is the lens the policy holds and no run has ever taken up: it is shown by
  // its id, marked, and carries no "ran ..." line, because nothing ran.
  const never = rows[1];
  if (never === undefined) throw new Error("the never-run recipe is not on the screen");
  expect(never.querySelector(".plugin-atyrode_babel_watch__never")?.textContent).toBe("never run");
  expect(never.querySelector(".plugin-atyrode_babel_watch__mono")).toBeNull();
  expect(never.querySelector(".plugin-atyrode_babel_watch__recipe-name")?.textContent).toBe(
    "babel-tunes-itself",
  );

  // And the one with runs behind it is not marked, and still says when it last looked.
  const ran = rows[0];
  if (ran === undefined) throw new Error("the performed recipe is not on the screen");
  expect(ran.querySelector(".plugin-atyrode_babel_watch__never")).toBeNull();
  expect(ran.querySelector(".plugin-atyrode_babel_watch__mono")?.textContent).toContain("42 runs");

  // The count is in the lede too, because how much of the cookbook has never been opened is a
  // reading of its own and one nobody would assemble by counting badges.
  expect(section(root).querySelector(".plugin-atyrode_babel_watch__lede")?.textContent).toContain(
    "1 of 2 enabled, 1 never run",
  );
});

test("absent, disabled and cold optional readings leave the recipe surface identical", async () => {
  const name = `${JEV_PLUGIN_ID}.${JEV_ACTIONS.recipeStanding}`;
  const absent = fakeHost({}, []);
  const disabled = fakeHost(
    {
      [name]: () => {
        throw new Error("dependency_unavailable");
      },
    },
    [],
  );
  const cold = fakeHost({ [name]: () => null }, []);
  const surfaces: string[] = [];
  for (const fake of [absent, disabled, cold]) {
    const root = await mount(<Recipes host={fake.host} recipes={POLICY.recipes} now={0} note="" />);
    await settle();
    surfaces.push(root.innerHTML);
    expect(root.querySelector("[data-recipe-standing]")).toBeNull();
  }
  expect(surfaces[1]).toBe(surfaces[0]);
  expect(surfaces[2]).toBe(surfaces[0]);
});

test("cached readings cannot survive a switch to a connection without Jev", async () => {
  const counts = {
    knownCached: 1,
    missingOrEvicted: 0,
    notInspected: 0,
    unjudged: null,
    bands: { unjudged: 0, unheard: 1, unremarked: 0, backed: 0, objected: 0, contested: 0 },
  };
  const reading: RecipeStanding = {
    observedAt: "2026-09-29T12:00:00.000Z",
    bankVersion: 1,
    policyRevision: "fixture-r7",
    basis: [],
    funding: "unknown",
    coverage: "partial-cache",
    total: 1,
    eligible: 1,
    multiRecipe: 0,
    excluded: 0,
    counts,
    recipes: [{ recipeId: POLICY.recipes[0]!.id, eligible: 1, ...counts }],
  };
  const warm = fakeHost({ [`${JEV_PLUGIN_ID}.${JEV_ACTIONS.recipeStanding}`]: () => reading }, []);
  const absent = fakeHost({}, []);
  function SwitchConnection() {
    const [host, setHost] = useState(warm.host);
    return (
      <>
        <button onClick={() => setHost(absent.host)}>Switch connection</button>
        <Recipes host={host} recipes={POLICY.recipes} now={0} note="" />
      </>
    );
  }
  const root = await mount(<SwitchConnection />);
  await settle();
  expect(root.querySelector("[data-recipe-standing]")?.textContent).toMatch(/funding unknown/i);
  await click(root.querySelector("button"));
  await settle();
  const baseline = await mount(
    <Recipes host={absent.host} recipes={POLICY.recipes} now={0} note="" />,
  );
  await settle();
  expect(section(root).outerHTML).toBe(section(baseline).outerHTML);
});
