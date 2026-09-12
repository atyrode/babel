import { beforeEach, describe, expect, test } from "bun:test";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { forgetSelection, looking } from "./api.ts";
import { HomePanel } from "./home.tsx";
import { Denial, fakeHost, feed, mount, post, topics, type Fake } from "./testing.tsx";

/*
  HOME, as the operator uses it.

  Every test here is a gesture and its consequence: what the panel DREW, and what it ASKED
  THE HUB. Nothing asserts a piece of state — a ruling is proved by the `rule` call and the
  row leaving, an answer by the `answer` call, a menu by the next `feed` read's arguments.
*/

const PULSE = {
  since: "2026-09-12T00:00:00Z",
  today: { sessionsRead: 4, records: 9, votes: 21, proposals: 3, topicProposals: 1, ruled: 2 },
  reviewing: [],
};

function hub(overrides: Record<string, (args: unknown) => unknown> = {}): Fake {
  return fakeHost({
    feed: () => feed(),
    topics: () => topics(),
    pulse: () => PULSE,
    rule: () => ({ id: "pro_0000000a", standing: "accepted", seq: 7, plan: null }),
    comment: () => ({}),
    answer: () => ({}),
    ...overrides,
  });
}

beforeEach(() => {
  resetPolledResources();
  forgetSelection();
});

describe("the list", () => {
  test("draws one row per post, with its kind, its age, its reason and its reviewers", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    const rows = view.all(".babel-row");
    expect(rows.map((row) => row.getAttribute("data-post"))).toEqual([
      "pro_0000000a",
      "fnd_0000000b",
      "que_0000000c",
    ]);
    expect(view.one('[data-post="pro_0000000a"] .babel-claim').textContent).toBe(
      "Pin the engine profile before a run starts",
    );
    expect(view.one('[data-post="pro_0000000a"] .babel-kind').textContent).toBe("Proposal");
    expect(view.one('[data-post="pro_0000000a"] .babel-topic').textContent).toBe("t/babel");
    expect(view.one('[data-post="pro_0000000a"] .babel-comments').textContent).toBe("2 comments");
    // "never ruled on" is dropped: fifteen rows opening with the same four words say nothing.
    expect(view.one('[data-post="pro_0000000a"] .babel-why').textContent).toBe("waiting 3d");
    // The unreviewed finding carries the ring and no figure.
    expect(view.one('[data-post="fnd_0000000b"] .babel-dot').getAttribute("data-tone")).toBe("none");
    expect(view.all('[data-post="fnd_0000000b"] .babel-score')).toHaveLength(0);
    // A row nobody is waiting on offers no acts and states no reason.
    expect(view.all('[data-post="fnd_0000000b"] [data-ruling]')).toHaveLength(0);
    expect(view.all('[data-post="fnd_0000000b"] .babel-why')).toHaveLength(0);
    await view.unmount();
  });

  test("reads the feed once for the panel, and the sentence says what it is ranked by", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    expect(fake.to("feed")).toHaveLength(1);
    expect(fake.last("feed")).toEqual({
      sort: "next",
      window: "day",
      kinds: [],
      needs: "me",
      limit: 15,
      offset: 0,
    });
    expect(view.one(".babel-sentence").textContent).toContain("what needs me");
    expect(view.one(".babel-sentence").textContent).toContain("sorted by next");
    expect(view.one(".babel-count").textContent).toContain("3");
    expect(view.one(".babel-pulse").textContent).toContain("Today Babel read 4 sessions");
    await view.unmount();
  });

  test("a question row offers the three answers and no rulings", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    expect(view.all('[data-post="que_0000000c"] [data-answer]').map((button) => button.textContent)).toEqual([
      "Answer",
      "I don't know",
      "Stop asking",
    ]);
    expect(view.all('[data-post="que_0000000c"] [data-ruling]')).toHaveLength(0);
    await view.unmount();
  });

  test("a refused read says so over the rows and offers the read again", async () => {
    const fake = hub({
      feed: () => {
        throw new Denial("the store is not open yet");
      },
    });
    const view = await mount(<HomePanel host={fake.host} />);
    await view.settle();
    expect(view.text()).toContain("The feed could not be read.");
    expect(view.text()).toContain("the store is not open yet");
    await view.unmount();
  });
});

describe("the sentence", () => {
  test("choosing an order re-reads the feed under it", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-pick="sort"]');
    expect(view.all("[data-sort]").map((button) => button.getAttribute("data-sort"))).toEqual([
      "next",
      "hot",
      "new",
      "top",
      "controversial",
      "rising",
    ]);
    await view.press('[data-sort="top"]');
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ sort: "top", window: "day" });
    // An order computed over a period keeps its menu open, because it needs the period.
    expect(view.all("[data-window]").length).toBeGreaterThan(0);
    await view.press('[data-window="week"]');
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ sort: "top", window: "week" });
    expect(view.one(".babel-sentence").textContent).toContain("sorted by top · this week");
    await view.unmount();
  });

  test("the kinds are a set: the menu stays open and each press widens the read", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-pick="kinds"]');
    await view.press('[data-kind="proposal"]');
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ kinds: ["proposal"] });
    await view.press('[data-kind="finding"]');
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ kinds: ["proposal", "finding"] });
    expect(view.one(".babel-sentence").textContent).toContain("proposals and findings");
    await view.unmount();
  });

  test("`m` turns the filter off, and the order follows it", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("m");
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ needs: "all", sort: "hot" });
    expect(view.one(".babel-sentence").textContent).toContain("everything");
    await view.unmount();
  });

  test("`s` and `c` open the two menus the sentence carries", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("s");
    expect(view.one('[data-pick="sort"]').getAttribute("aria-expanded")).toBe("true");
    await view.key("Escape");
    await view.key("c");
    expect(view.one('[data-pick="kinds"]').getAttribute("aria-expanded")).toBe("true");
    await view.unmount();
  });
});

describe("ruling", () => {
  test("asks for confirmation, posts `rule`, folds the row and offers the way back", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] [data-ruling="accept"]');
    // The press opens the sentence; it does not record anything.
    expect(fake.to("rule")).toHaveLength(0);
    expect(view.one(".babel-confirm").textContent).toContain("appended permanently");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("rule")).toEqual({ id: "pro_0000000a", ruling: "accept", note: "" });
    expect(view.one(".babel-said").textContent).toContain("The record's standing is now accepted");
    // Under "what needs me" the list is what is left to do: the row leaves, the count ticks
    // down, and the way back is a ruling rather than a deletion.
    expect(view.one('[data-post="pro_0000000a"]').hasAttribute("data-leaving")).toBe(true);
    expect(view.one(".babel-count").textContent).toContain("2");
    expect(view.one(".babel-toast").textContent).toContain("accepted");
    expect(view.one(".babel-sentence").textContent).toContain("ruled today 1");
    await view.wait(300);
    expect(view.all('[data-post="pro_0000000a"]')).toHaveLength(0);
    await view.press(".babel-toast button");
    await view.settle();
    expect(fake.last("rule")).toEqual({
      id: "pro_0000000a",
      ruling: "reopen",
      note: "reopened from the feed",
    });
    await view.unmount();
  });

  test("the note travels with the ruling, and a refusal stays on the row", async () => {
    const fake = hub({
      rule: () => {
        throw new Denial("that chain moved since you read it");
      },
    });
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] [data-ruling="reject"]');
    await view.type(".babel-confirm textarea", "the evidence is one remark");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("rule")).toEqual({
      id: "pro_0000000a",
      ruling: "reject",
      note: "the evidence is one remark",
    });
    expect(view.one(".babel-error").textContent).toBe("that chain moved since you read it");
    // Nothing left the list: the act was refused, so the row is still waiting on him.
    expect(view.all('[data-post="pro_0000000a"]')).toHaveLength(1);
    expect(view.all(".babel-toast")).toHaveLength(0);
    await view.unmount();
  });

  test("`y` presses the row's own control rather than posting", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("j");
    await view.key("y");
    expect(fake.to("rule")).toHaveLength(0);
    expect(view.one('[data-post="pro_0000000a"] .babel-confirm').textContent).toContain("Endorse this record");
    await view.unmount();
  });

  test("refine is a ruling and carries no note field", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] [data-ruling="refine"]');
    expect(view.all(".babel-confirm textarea")).toHaveLength(0);
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("rule")).toEqual({ id: "pro_0000000a", ruling: "refine", note: "" });
    await view.unmount();
  });

  test("a ruling that applied a plan says what the ledger did", async () => {
    const fake = hub({
      rule: () => ({
        id: "pro_0000000a",
        standing: "accepted",
        seq: 8,
        plan: { kind: "topic", operation: "create", applied: true, declined: false, entityId: "ent_0000dead" },
      }),
    });
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] [data-ruling="accept"]');
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(view.one(".babel-said").textContent).toContain("The topic plan was applied.");
    await view.unmount();
  });
});

describe("asking and answering", () => {
  test("a question row answers inline, through the answer door", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="que_0000000c"] [data-answer="answered"]');
    expect(view.one(".babel-confirm").textContent).toContain("read by the answer interpreter");
    // An empty answer records nothing: the outcome that carries words requires them.
    await view.press(".babel-confirm .babel-primary");
    expect(fake.to("answer")).toHaveLength(0);
    await view.type(".babel-confirm textarea", "the shared catalog is retired");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("answer")).toEqual({
      id: "que_0000000c",
      outcome: "answered",
      text: "the shared catalog is retired",
    });
    expect(view.one('[data-post="que_0000000c"] .babel-acted').textContent).toBe("answered");
    await view.unmount();
  });

  test("`a` opens the answer on a question row and nothing on a record row", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("j");
    await view.key("a");
    expect(view.all(".babel-confirm")).toHaveLength(0);
    await view.key("j");
    await view.key("j");
    await view.key("a");
    expect(view.one('[data-post="que_0000000c"] .babel-confirm').textContent).toContain(
      "read by the answer interpreter",
    );
    await view.unmount();
  });

  test("asking is a comment with a question for its kind, and the count moves", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] [data-ruling="ask"]');
    await view.type(".babel-confirm input", "which machine has restic?");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("comment")).toEqual({
      id: "pro_0000000a",
      text: "which machine has restic?",
      kind: "question",
    });
    expect(view.one('[data-post="pro_0000000a"] .babel-comments').textContent).toBe("3 comments");
    await view.unmount();
  });
});

describe("the peek", () => {
  test("↵ opens the focused row, and `j`/`k` walk the peek with the list", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("j");
    expect(view.one(".babel-row[data-focused]").getAttribute("data-post")).toBe("pro_0000000a");
    expect(looking().recordId).toBe("");
    await view.key("Enter");
    expect(looking().recordId).toBe("pro_0000000a");
    await view.key("j");
    expect(looking().recordId).toBe("fnd_0000000b");
    await view.key("k");
    expect(looking().recordId).toBe("pro_0000000a");
    expect(view.one(".babel-row[data-selected]").getAttribute("data-post")).toBe("pro_0000000a");
    await view.unmount();
  });

  test("a claim under the pointer opens the same peek", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-open="fnd_0000000b"]');
    expect(looking().recordId).toBe("fnd_0000000b");
    await view.unmount();
  });

  test("a topic on a row points the topic panel at it", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] .babel-topic');
    expect(looking().topic).toBe("ent_0000beef");
    await view.unmount();
  });
});

describe("the rail", () => {
  test("groups the topics by the operator's stance and marks the open one", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    expect(view.all(".babel-topic-group-label").map((label) => label.textContent)).toEqual([
      "Working on it",
      "Nothing said",
    ]);
    expect(view.all(".babel-topic-list button").map((row) => row.getAttribute("data-topic"))).toEqual([
      "ent_0000beef",
      "ent_0000cafe",
    ]);
    await view.press('[data-topic="ent_0000cafe"]');
    expect(looking().topic).toBe("ent_0000cafe");
    await view.unmount();
  });

  test("what is filed under nothing is a filter over the feed, not a topic", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press(".babel-topic-unfiled");
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ topic: "unfiled", needs: "all", sort: "new" });
    await view.unmount();
  });

  test("a proposed topic is ruled on in the rail, through the same rule door", async () => {
    const fake = hub({
      topics: () =>
        topics({
          proposed: [
            {
              proposalId: "pro_0000ffff",
              title: "Create t/manifold-db",
              name: "manifold-db",
              kind: "repository",
              operation: "create",
              targets: [],
              runId: "run-1",
              posts: 4,
              why: "four records cite the same checkout",
            },
          ],
        }),
    });
    const view = await mount(<HomePanel host={fake.host} />);
    expect(view.one(".babel-topic-why").textContent).toContain("four records cite the same checkout");
    await view.press('[data-proposal="pro_0000ffff"]');
    await view.settle();
    expect(fake.last("rule")).toEqual({ id: "pro_0000ffff", ruling: "accept", note: "" });
    expect(view.one(".babel-topic-ruled").textContent).toBe("Accepted");
    await view.unmount();
  });
});

describe("what the last read did", () => {
  test("an event brings a live read: the arrival haloes, the moved score ticks", async () => {
    let answer = feed();
    const fake = hub({ feed: () => answer });
    const view = await mount(<HomePanel host={fake.host} />);
    expect(fake.to("feed")).toHaveLength(1);
    answer = feed({
      posts: [
        post({ id: "hyp_0000000e", kind: "hypothesis", title: "Restic prune has never run", awaiting: false, why: "" }),
        post({ score: 4 }),
        ...feed().posts.slice(1),
      ],
      total: 4,
    });
    // The channel is up, so the timer is off: this read happens because the baseline said
    // something happened on its node, and for no other reason.
    fake.announce();
    await view.wait(120);
    expect(fake.to("feed")).toHaveLength(2);
    expect(view.one('[data-post="hyp_0000000e"]').hasAttribute("data-arrived")).toBe(true);
    expect(view.one('[data-post="pro_0000000a"] .babel-score').hasAttribute("data-ticked")).toBe(true);
    expect(view.one('[data-post="pro_0000000a"] .babel-score').textContent).toBe("4");
    await view.unmount();
  });

  test("a row that stopped waiting folds out of the list on a live read", async () => {
    let answer = feed();
    const fake = hub({ feed: () => answer });
    const view = await mount(<HomePanel host={fake.host} />);
    answer = feed({ posts: feed().posts.slice(1), total: 2 });
    fake.announce();
    await view.wait(120);
    expect(view.one('[data-post="pro_0000000a"]').hasAttribute("data-leaving")).toBe(true);
    await view.wait(300);
    expect(view.all('[data-post="pro_0000000a"]')).toHaveLength(0);
    await view.unmount();
  });

  test("changing the question is not an arrival: the reader's own gesture is not announced", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-pick="sort"]');
    await view.press('[data-sort="new"]');
    await view.settle();
    expect(view.all("[data-arrived]")).toHaveLength(0);
    expect(view.all("[data-leaving]")).toHaveLength(0);
    await view.unmount();
  });
});
