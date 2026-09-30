import "../babel/feed/dom.ts";
import { afterEach, beforeEach, expect, test } from "bun:test";
import type { HostServices } from "@manifold/plugin";
import { resetPolledResources } from "@manifold/plugin/hooks";
import type { GuestCtx } from "@manifold/plugin-kit/server";
import { act } from "react";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  OPERATIONS,
  door,
  type Activity,
  type ActivityWeights,
  type Ruling,
} from "../babel/contract.ts";
import { allocationDoors } from "../babel/doors/allocation.ts";
import { fakeHost, mount, type DoorCall, type Mounted } from "../babel/feed/testing.tsx";
import { stamp, type ActsStore } from "../babel/store/acts.ts";
import { DEFAULT_POLICY } from "../babel/store/coordinator.ts";
import { insert, openTestStore, type TestStore } from "../babel/store/testdb.ts";
import { Allocation } from "../babel/watch/allocation.tsx";
import { Watch } from "../babel/watch/web.tsx";

const NOW = Date.UTC(2026, 8, 29, 12);
const PREVIEW = `[data-action="${door(ACTIONS.previewAllocation)}"]`;
const SAVE = `[data-action="${door(ACTIONS.saveAllocation)}"]`;
const CURRENT = '[data-field="allocation-preview"]';
const SAVED = '[data-field="allocation-saved"]';
const NAME = '[data-field="allocation-name"]';
const REASON = '[data-field="allocation-reason"]';
const views: Mounted[] = [];
const stores: TestStore[] = [];

beforeEach(() => resetPolledResources());
afterEach(async () => {
  for (const view of views.splice(0)) await view.unmount();
  for (const store of stores.splice(0)) store.close();
  resetPolledResources();
});

async function fixture(
  weights: ActivityWeights = { review: 0.3, explore: 0.2, challenge: 0.3, synthesize: 0.2, map: 0 },
  enabled = true,
) {
  const harness = await openTestStore(NOW);
  stores.push(harness);
  const policy = {
    ...DEFAULT_POLICY,
    enabled,
    version: "authorized-baseline",
    activityWeights: weights,
    coverageShare: 0.1,
    explorationShare: 0.1,
    discoveryShare: 0.1,
  };
  await insert(harness.db, "policies", {
    version: policy.version,
    seq: 1,
    actor_id: "owner",
    reason: "synthetic authority",
    payload: JSON.stringify(policy),
    recorded_at: stamp(NOW),
  });
  const store: ActsStore = {
    db: harness.db,
    now: () => NOW,
    touch: () => {
      throw new Error("allocation must not wake or invalidate work");
    },
  };
  const doors = allocationDoors(store);
  const calls: DoorCall[] = [];
  const access = { owner: true, unavailable: false };
  const fake = fakeHost({});
  const host: HostServices = {
    ...fake.host,
    client: {
      ...fake.host.client,
      action: async (name: string, args: unknown) => {
        const entry = doors.find(
          (candidate) => `${BABEL_PLUGIN_ID}.${candidate.action.name}` === name,
        );
        if (!entry) return await fake.host.client.action(name, args);
        calls.push({ name, args });
        if (access.unavailable)
          return {
            ok: false,
            denial: { rule: "unavailable", message: "Allocation backend unavailable" },
          };
        const context = {
          auth: { isRoot: access.owner },
          principal: { id: "owner" },
        } as unknown as GuestCtx;
        const result = await entry.handler(context, entry.action.input.parse(args) as never);
        if (typeof result === "object" && result !== null && "refused" in result) {
          return { ok: false, denial: { rule: "refused", message: String(result.refused) } };
        }
        return { ok: true, result: entry.action.result.parse(result) };
      },
    },
  };
  return { harness, host, calls, access, otherCalls: fake.calls, announce: fake.announce };
}

async function show(host: HostServices, wholeWatch = false) {
  const view = await mount(wholeWatch ? <Watch host={host} /> : <Allocation host={host} />);
  views.push(view);
  return view;
}

async function submit(view: Mounted, selector: string) {
  await act(async () => {
    const form = view.one(selector).closest("form");
    if (form === null) throw new Error(`No form at ${selector}`);
    form.requestSubmit();
  });
  await view.settle();
}

async function pin(view: Mounted, activity: Activity, fraction: string) {
  await view.type(`${CURRENT} [data-activity="${activity}"] input`, fraction);
  await submit(view, `${CURRENT} [data-activity="${activity}"] form`);
}

function proposed(view: Mounted, activity: Activity, saved = false) {
  return view.one(
    `${saved ? SAVED : CURRENT} [data-activity="${activity}"] [data-field="proposed"]`,
  ).textContent;
}

async function outcome(
  harness: TestStore,
  id: string,
  activity: Activity | null,
  decision: Ruling,
  administrative = false,
) {
  const runId = activity === null ? null : `run_${id}`;
  if (runId !== null)
    await insert(harness.db, "runs", {
      id: runId,
      kind: activity === "review" ? OPERATIONS.evaluate : OPERATIONS.explore,
      started_at: stamp(NOW - 1000),
      payload: "{}",
      preparation: JSON.stringify(
        activity === "review"
          ? { review: { role: "reception" } }
          : { analysis: { stage: activity } },
      ),
    });
  await insert(harness.db, "records", {
    id,
    kind: "proposal",
    root_id: id,
    seq: 0,
    run_id: runId,
    actor_kind: "run",
    actor_id: runId ?? "unknown",
    title: "synthetic improvement",
    created_at: stamp(NOW - 1000),
    payload: JSON.stringify(administrative ? { topic: { operation: "create" } } : {}),
  });
  await insert(harness.db, "dispositions", {
    id: `ruling_${id}`,
    record_id: id,
    seq: 1,
    disposition: decision,
    actor_id: "owner",
    recorded_at: stamp(NOW),
  });
}

async function authority(harness: TestStore) {
  return await harness.db.query(`SELECT
    (SELECT json_group_array(json_object('version',version,'payload',payload)) FROM policies) AS policies,
    (SELECT count(*) FROM budgets) AS budgets,
    (SELECT count(*) FROM claims) AS claims,
    (SELECT count(*) FROM runs) AS runs,
    (SELECT count(*) FROM drains) AS drains`);
}

test("Watch previews genuine outcomes, pins multiple fractions, saves and replays without admitting work", async () => {
  const hub = await fixture();
  for (let i = 0; i < 8; i += 1) {
    await outcome(hub.harness, `review_${String(i)}`, "review", "accept");
    await outcome(hub.harness, `challenge_${String(i)}`, "challenge", "reject");
  }
  await outcome(hub.harness, "sparse", "synthesize", "accept");
  await outcome(hub.harness, "administrative", "review", "accept", true);
  await outcome(hub.harness, "unattributed", null, "accept");
  await outcome(hub.harness, "deferred", "review", "defer");
  const before = await authority(hub.harness);
  const view = await show(hub.host);
  expect(hub.calls).toEqual([]);
  await view.press(PREVIEW);
  expect(
    view.one(`${CURRENT} [data-activity="review"] [data-field="sample"]`).textContent,
  ).toContain("8 accepted / 0 rejected · 8 eligible");
  expect(
    view.one(`${CURRENT} [data-activity="challenge"] [data-field="sample"]`).textContent,
  ).toContain("0 accepted / 8 rejected · 8 eligible");
  expect(
    view.one(`${CURRENT} [data-activity="synthesize"] [data-field="sample"]`).textContent,
  ).toContain("sparse evidence · damping 0.00%");
  expect(view.one(`${CURRENT} [data-exclusion="administrative"] dd`).textContent).toBe("1");
  expect(view.one(`${CURRENT} [data-exclusion="no-producing-activity"] dd`).textContent).toBe("1");
  expect(view.one(`${CURRENT} [data-exclusion="not-decided"] dd`).textContent).toBe("1");
  expect(view.one(`${CURRENT} [data-inventory="eligible-work"]`).textContent).toContain("unknown");
  expect(view.one(`${CURRENT} [data-field="allocation-window"]`).textContent).toContain(
    "2026-09-22T12:00:00.000Z",
  );
  expect(view.one(`${CURRENT} [data-activity="map"] input`).hasAttribute("disabled")).toBe(true);
  expect(view.one(`${CURRENT} [data-activity="explore"] input`).getAttribute("min")).toBe("0.2");
  await pin(view, "challenge", "0.2");
  await pin(view, "synthesize", "0.1");
  expect(proposed(view, "review")).toBe("50.00%");
  expect(proposed(view, "explore")).toBe("20.00%");
  expect(proposed(view, "challenge")).toBe("20.00%");
  expect(proposed(view, "synthesize")).toBe("10.00%");
  expect(
    view.one(`${CURRENT} [data-activity="review"] [data-field="displacement"]`).textContent,
  ).toBe("+20.00 pp");
  await view.type(NAME, "review-first");
  await view.type(REASON, "Keep exploration while preferring accepted improvements");
  await submit(view, ".plugin-atyrode_babel_watch__allocation-save");
  expect(proposed(view, "challenge", true)).toBe("20.00%");
  expect(proposed(view, "synthesize", true)).toBe("10.00%");
  expect(view.one(SAVED).textContent).toContain("owner");
  expect(view.one(SAVED).textContent).toContain(
    "Keep exploration while preferring accepted improvements",
  );
  expect(await authority(hub.harness)).toEqual(before);
  expect(await hub.harness.db.query("SELECT version FROM allocation_plans")).toEqual([
    { version: "review-first" },
  ]);
  // Change today's world, then read the immutable version: replay must not be a fresh preview.
  await outcome(hub.harness, "later", "challenge", "accept");
  await view.press('[data-action="allocation-read-latest"]');
  expect(proposed(view, "review", true)).toBe("50.00%");
  await view.type('[data-field="allocation-version"]', "missing");
  await submit(view, '[data-action="allocation-read-named"]');
  expect(view.all(SAVED)).toEqual([]);
  expect(view.text()).toContain("No saved allocation named missing");
  await view.type('[data-field="allocation-version"]', "review-first");
  await submit(view, '[data-action="allocation-read-named"]');
  expect(proposed(view, "synthesize", true)).toBe("10.00%");
  expect(
    hub.calls.every((call) =>
      [
        door(ACTIONS.previewAllocation),
        door(ACTIONS.saveAllocation),
        door(ACTIONS.allocationVersion),
      ].some((name) => name === call.name),
    ),
  ).toBe(true);
  expect(hub.otherCalls).toEqual([]);
});

test("impossible and stale edits retain the last plan and every pin; stale save never becomes success", async () => {
  const hub = await fixture();
  const view = await show(hub.host);
  await view.press(PREVIEW);
  await pin(view, "challenge", "0.2");
  await view.type(NAME, "retained-name");
  await view.type(REASON, "retained reason");
  await pin(view, "synthesize", "0.95");
  expect(view.text()).toContain("no permitted remainder");
  expect(proposed(view, "challenge")).toBe("20.00%");
  expect(view.one(SAVE).hasAttribute("disabled")).toBe(true);
  await pin(view, "synthesize", "0.1");
  await outcome(hub.harness, "arrived", "review", "accept");
  await pin(view, "challenge", "0.25");
  expect(view.text()).toContain("basis is stale");
  expect(proposed(view, "challenge")).toBe("20.00%");
  await view.press(PREVIEW);
  expect((view.one(NAME) as HTMLInputElement).value).toBe("retained-name");
  expect((view.one(REASON) as HTMLTextAreaElement).value).toBe("retained reason");
  expect(proposed(view, "synthesize")).toBe("10.00%");
  await pin(view, "challenge", "0.25");
  expect(proposed(view, "synthesize")).toBe("10.00%");
  expect(proposed(view, "review")).toBe("45.00%");
  await outcome(hub.harness, "arrived-before-save", "review", "accept");
  await submit(view, ".plugin-atyrode_babel_watch__allocation-save");
  expect(view.text()).toContain("Save refused or unavailable");
  expect(view.all(SAVED)).toEqual([]);
  expect(await hub.harness.db.query("SELECT version FROM allocation_plans")).toEqual([]);
  await view.press(PREVIEW);
  await submit(view, ".plugin-atyrode_babel_watch__allocation-save");
  expect(proposed(view, "challenge", true)).toBe("25.00%");
  expect(proposed(view, "synthesize", true)).toBe("10.00%");
});

test("quiet and all-zero budgets distinguish unknown feedback from disabled, unallocated shares", async () => {
  const quiet = await fixture();
  const view = await show(quiet.host);
  await view.press(PREVIEW);
  expect(proposed(view, "review")).toBe("30.00%");
  expect(
    view.one(`${CURRENT} [data-activity="review"] [data-field="sample"]`).textContent,
  ).toContain("unknown (no denominator)");
  expect(view.one(`${CURRENT} [data-exclusion="counted"] dd`).textContent).toBe("0");
  expect(view.one(`${CURRENT} [data-inventory="proposal"]`).textContent).toContain("proposal: 0");
  expect(view.one(`${CURRENT} [data-inventory="eligible-work"]`).textContent).toContain("unknown");
  const zero = await fixture({ review: 0, explore: 0, challenge: 0, synthesize: 0, map: 0 });
  const empty = await show(zero.host);
  await empty.press(PREVIEW);
  expect(empty.one('[data-field="allocation-unallocated"]').textContent).toBe("100.00%");
  for (const activity of ["review", "explore", "challenge", "synthesize", "map"] as const) {
    expect(proposed(empty, activity)).toBe("0.00%");
    expect(
      empty.one(`${CURRENT} [data-activity="${activity}"] input`).hasAttribute("disabled"),
    ).toBe(true);
  }
  await empty.press('[data-action="allocation-read-latest"]');
  expect(empty.text()).toContain("No saved allocation version exists yet");
  expect(await zero.harness.db.query("SELECT id FROM runs")).toEqual([]);
  const disabled = await fixture(
    { review: 0.3, explore: 0.2, challenge: 0.3, synthesize: 0.2, map: 0 },
    false,
  );
  const disabledView = await show(disabled.host);
  await disabledView.press(PREVIEW);
  expect(proposed(disabledView, "review")).toBe("0.00%");
  expect(disabledView.one('[data-field="allocation-unallocated"]').textContent).toBe("100.00%");
  expect(
    disabledView.one(`${CURRENT} [data-activity="review"] input`).hasAttribute("disabled"),
  ).toBe(true);
  expect(
    disabledView.one(`${CURRENT} [data-activity="review"] [data-field="sample"]`).textContent,
  ).toContain("disabled evidence");
});

test("unavailable and nonowner reads never masquerade as a zero-data plan or expose saved inputs", async () => {
  const hub = await fixture();
  hub.access.unavailable = true;
  const view = await show(hub.host);
  await view.press(PREVIEW);
  expect(view.text()).toContain("Allocation backend unavailable");
  expect(view.all(CURRENT)).toEqual([]);
  expect(view.all(SAVE)).toEqual([]);
  hub.access.unavailable = false;
  hub.access.owner = false;
  await view.press(PREVIEW);
  expect(view.text()).toContain("owner's act");
  await view.press('[data-action="allocation-read-latest"]');
  expect(view.text()).toContain("owner's records");
  expect(view.all(SAVED)).toEqual([]);
  expect(view.all(CURRENT)).toEqual([]);
  expect(await hub.harness.db.query("SELECT id FROM runs")).toEqual([]);
  expect(await hub.harness.db.query("SELECT version FROM allocation_plans")).toEqual([]);
});

test("Watch refreshes do not overwrite typed slices; changing the owner route clears sensitive proposal state", async () => {
  const hub = await fixture();
  const view = await show(hub.host, true);
  await view.press(PREVIEW);
  await pin(view, "challenge", "0.2");
  await view.type(`${CURRENT} [data-activity="synthesize"] input`, "0.1");
  await view.type(NAME, "unfinished");
  const reads = hub.calls.length;
  await act(async () => {
    hub.announce();
  });
  await view.settle();
  expect(hub.calls.length).toBe(reads);
  expect(
    (view.one(`${CURRENT} [data-activity="synthesize"] input`) as HTMLInputElement).value,
  ).toBe("0.1");
  expect(proposed(view, "challenge")).toBe("20.00%");
  await act(async () => {
    view.root.render(
      <Watch host={{ ...hub.host, principal: { ...hub.host.principal, id: "other-owner" } }} />,
    );
  });
  expect(view.all(CURRENT)).toEqual([]);
  expect(view.all(SAVED)).toEqual([]);
  expect(hub.calls.length).toBe(reads);
  expect(
    hub.otherCalls.some((call) =>
      [door(ACTIONS.launch), door(ACTIONS.drainStart), door(ACTIONS.setPolicy)].some(
        (name) => name === call.name,
      ),
    ),
  ).toBe(false);
});
