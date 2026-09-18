import { beforeEach, describe, expect, test } from "bun:test";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { ACTIONS, door } from "../contract.ts";
import { forgetSelection, look } from "./api.ts";
import { TopicPanel } from "./topic.tsx";
import { fakeHost, feed, mount, pointAt, topic, type Fake } from "./testing.tsx";

/*
  THE TOPIC PANEL: the header, the stance, the asks — and under them the same feed, narrowed.

  The two facts §4.13 turns on are what the tests hold: nobody having said where he stands is
  a DIFFERENT answer from any of the four stances, and nothing here rewrites the ledger — an
  ask is words Babel answers with a proposal he rules on like any other.
*/

function hub(overrides: Record<string, (args: unknown) => unknown> = {}): Fake {
  return fakeHost({
    topic: () => topic(),
    feed: () => feed({ posts: feed().posts.slice(0, 1), total: 1 }),
    topics: () => ({ topics: [], proposed: [], unfiled: 0 }),
    pulse: () => ({
      since: "2026-09-12T00:00:00Z",
      today: { sessionsRead: 0, records: 0, votes: 0, proposals: 0, topicProposals: 0, ruled: 0 },
      reviewing: [],
      cycle: null,
    }),
    interest: () => ({}),
    tell: () => ({}),
    rule: () => ({ id: "pro_0000000a", standing: "accepted", seq: 1, plan: null }),
    ...overrides,
  });
}

beforeEach(() => {
  resetPolledResources();
  forgetSelection();
});

describe("the header", () => {
  test("says so when no topic is open, and reads nothing", async () => {
    const fake = hub();
    const view = await mount(<TopicPanel host={fake.host} />);
    expect(view.text()).toContain("No topic open");
    expect(fake.calls).toHaveLength(0);
    await view.unmount();
  });

  test("reads the topic the rail pointed at, and narrows the feed to it", async () => {
    const fake = hub();
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    expect(fake.last("topic")).toEqual({ topic: "ent_0000beef" });
    expect(view.one(".babel-topic-name").textContent).toBe("t/babel");
    expect(view.text()).toContain("42 posts");
    expect(view.text()).toContain("3 awaiting you");
    // No path is printed: the remote is the identity and the checkouts are a count.
    expect(view.text()).toContain("github.com/atyrode/babel");
    expect(view.text()).not.toContain("/home/alex/babel");
    expect(fake.last("feed")).toMatchObject({ topic: "ent_0000beef", surface: "all", sort: "new" });
    // It is the same list, so the sentence is over it and the rows are the feed's own.
    expect(view.all(".babel-row")).toHaveLength(1);
    expect(view.one(".babel-sentence").textContent).toContain("sorted by newest");
    await view.unmount();
  });

  test("a name no entity answers to is said, not drawn as an empty topic", async () => {
    const fake = hub({ topic: () => topic({ topic: null }) });
    look({ topic: "wandering" });
    const view = await mount(<TopicPanel host={fake.host} />);
    await view.settle();
    expect(view.text()).toContain("No topic in this hub answers to that name");
    await view.unmount();
  });

  test("a seat opened FOR a topic is pinned to it, whatever Home is looking at", async () => {
    const fake = hub();
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} arg={{ topic: "ent_0000cafe" }} />);
    expect(fake.last("topic")).toEqual({ topic: "ent_0000cafe" });
    await pointAt({ topic: "ent_0000beef" });
    expect(fake.to("topic")).toHaveLength(1);
    await view.unmount();
  });
});

describe("the stance", () => {
  test("shows what was recorded, with the attribution of the act that recorded it", async () => {
    const fake = hub();
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    expect(view.one(".babel-stance").textContent).toContain("Working on it");
    expect(view.one(".babel-stance").textContent).toContain("the rewrite");
    expect(view.one('[data-interest="working"]').getAttribute("aria-pressed")).toBe("true");
    await view.unmount();
  });

  test("nobody having said anything is its own answer, never a default stance", async () => {
    const rows = topic();
    const quiet = rows.topic;
    if (quiet === null) throw new Error("the fixture lost its topic");
    const fake = hub({
      topic: () =>
        topic({ topic: { ...quiet, interest: { state: "", reason: "", at: "", by: "" } } }),
    });
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    expect(view.one(".babel-note").textContent).toBeDefined();
    expect(view.text()).toContain("You have not said where you stand on this.");
    expect(view.all('[data-interest][aria-pressed="true"]')).toHaveLength(0);
    await view.unmount();
  });

  test("choosing a stance asks for the reason, then records it and re-reads", async () => {
    const fake = hub();
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    await view.press('[data-interest="not-now"]');
    expect(fake.to("interest")).toHaveLength(0);
    await view.type(".babel-interest input", "after the crossing");
    await view.press(".babel-interest .babel-primary");
    await view.settle();
    expect(fake.last("interest")).toEqual({
      entityId: "ent_0000beef",
      state: "not-now",
      reason: "after the crossing",
    });
    expect(fake.to("topic").length).toBeGreaterThan(1);
    await view.unmount();
  });

  test("an empty reason is lawful: the act is attributable without prose", async () => {
    const fake = hub();
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    await view.press('[data-interest="excluded"]');
    await view.press(".babel-interest .babel-primary");
    await view.settle();
    expect(fake.last("interest")).toEqual({
      entityId: "ent_0000beef",
      state: "excluded",
      reason: "",
    });
    await view.unmount();
  });
});

describe("asking Babel", () => {
  test("records what the operator wants in his own words, against the entity", async () => {
    const fake = hub();
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    await view.press(".babel-asks .disclosure__header");
    await view.press('[data-ask="merge"]');
    await view.type(".babel-asks input", "manifold");
    await view.type(".babel-asks textarea", "they are one repository now");
    await view.press(".babel-asks .babel-primary");
    await view.settle();
    expect(fake.last("tell")).toEqual({
      text: "Please merge this topic t/babel into t/manifold: they are one repository now",
      target: { kind: "entity", id: "ent_0000beef" },
    });
    // Nothing here rewrote the ledger: what happens next is a proposal he rules on.
    expect(view.text()).toContain("Babel's next filing run answers with a proposal");
    await view.unmount();
  });

  test("a topic Babel has proposed a change to says what it proposed", async () => {
    const fake = hub({
      topic: () =>
        topic({
          proposed: [
            {
              proposalId: "pro_0000eeee",
              title: "Retire t/babel",
              name: "babel",
              kind: "repository",
              operation: "retire",
              targets: [{ id: "ent_0000beef", name: "babel" }],
              runId: "run-2",
              posts: 42,
              why: "everything under it moved to the plugin",
            },
          ],
        }),
    });
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    expect(view.text()).toContain("Babel proposes to retire this");
    expect(view.text()).toContain("everything under it moved to the plugin");
    await view.unmount();
  });
});

/*
  THE LENS NOBODY HAS POINTED HERE (#330). Reading a blank cell is not acting on one, so a zero
  the launch door would accept carries the launch — and it is the SAME launch: the same door,
  the same machine, the same Code profile at the revision it was shown, the same ceilings. A
  cell is a shorter way to ask for a run and must not be a shorter way to get one.
*/
describe("a lens that never looked here", () => {
  const PROFILES = {
    profiles: [
      {
        containerId: "ctr_workbench",
        revision: 7,
        model: "anthropic/claude-opus-5",
        thinking: "high",
        lastMachineId: "m-dev-01",
        accounts: [],
        resolved: true,
      },
    ],
    unavailable: "",
  };
  const STARTED = {
    runId: "run_a1b2",
    jobId: "job_c3d4",
    machineId: "m-dev-01",
    kind: "explore" as const,
  };

  const armed = async (fake: Fake) => {
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    // The fold is what asks for the machines and Code's profiles: a page nobody opened the
    // offer on reads neither.
    await view.press(".babel-coverage-offer .disclosure__header");
    await view.settle();
    return view;
  };

  test("a zero is offered and a count is not, and a lens turned off is named rather than offered", async () => {
    const fake = hub({ profiles: () => PROFILES });
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    await view.settle();
    // The prose still names every lens that never looked, whether or not it can be run…
    expect(view.text()).toContain("never looked: Test economics · Time sinks and token spend");
    // …and exactly one of them is a control: the one the launch door holds a recipe for. The
    // lens with two records is a reading and not work on offer.
    expect(view.all("[data-lens]").map((button) => button.getAttribute("data-lens"))).toEqual([
      "test-economics",
    ]);
    await view.unmount();
  });

  test("the press posts the whole launch document, with the recipe and the entity", async () => {
    const fake = hub({ profiles: () => PROFILES, launch: () => STARTED });
    const view = await armed(fake);

    // Nothing can be posted before a machine and a profile, and the clause says which is next.
    expect(view.one("[data-lens]").hasAttribute("disabled")).toBe(true);
    expect(view.text()).toContain("Pick a machine to run on.");
    await view.choose(".babel-coverage-offer [data-field='machine']", "m-dev-01");
    expect(view.text()).toContain("Pick the Code profile this run is posted on");
    await view.choose(".babel-coverage-offer [data-field='profile']", "ctr_workbench");
    expect(view.one("[data-lens]").hasAttribute("disabled")).toBe(false);

    await view.press("[data-lens='test-economics']");
    await view.settle();

    /*
      WHAT WAS POSTED IS THE WHOLE DOCUMENT, and it is spelled out here rather than sampled: the
      discipline is the machine, the profile pin and the operation node travelling together, and
      an assertion that only looked for the recipe would pass for a control that dropped the
      profile. `test/contract.test.ts` pins this document against the one Watch's own Start form
      builds for the same choices, so the two surfaces cannot drift into two disciplines.
    */
    expect(fake.last("launch")).toEqual({
      machineId: "m-dev-01",
      preset: "explore-topic",
      entityId: "ent_0000beef",
      recipes: ["test-economics"],
      profile: { containerId: "ctr_workbench", expectedRevision: 7 },
      operation: {
        kind: "operation",
        machineId: "m-dev-01",
        operationId: "atyrode.babel.explore",
      },
    });
    expect(view.one("[data-lens]").getAttribute("data-action")).toBe(door(ACTIONS.launch));
    expect(view.text()).toContain("Started Test economics as run_a1b2");
    await view.unmount();
  });

  test("a Code that could not be asked is its own sentence, and no press that could only fail", async () => {
    const fake = hub({
      profiles: () => ({
        profiles: [],
        unavailable: "engine_unavailable: atyrode.babel -> atyrode.code",
      }),
    });
    const view = await armed(fake);
    expect(view.one("[data-field='profiles-unavailable']").textContent).toContain(
      "atyrode.babel -> atyrode.code",
    );
    expect(view.all("[data-field='profile']")).toHaveLength(0);
    expect(view.one("[data-lens]").hasAttribute("disabled")).toBe(true);
    await view.unmount();
  });

  test("a policy naming no recipe offers nothing, rather than an empty control", async () => {
    const fake = hub({ topic: () => topic({ coverage: [] }), profiles: () => PROFILES });
    look({ topic: "ent_0000beef" });
    const view = await mount(<TopicPanel host={fake.host} />);
    await view.settle();
    expect(view.all(".babel-coverage")).toHaveLength(0);
    expect(view.all("[data-lens]")).toHaveLength(0);
    expect(view.text()).not.toContain("never looked");
    // An affordance for an empty set is worse than no affordance: nothing was even read for one.
    expect(fake.to("profiles")).toHaveLength(0);
    await view.unmount();
  });
});
