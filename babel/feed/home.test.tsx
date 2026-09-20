import { afterEach, beforeEach, describe, expect, setSystemTime, test } from "bun:test";
import type { OpenPanelOutcome, OpenPanelRequest } from "@manifold/plugin";
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
  // The loop's last verdict rides on the pulse (#328) and Home renders none of it; Watch does.
  cycle: null,
};

function hub(
  overrides: Record<string, (args: unknown) => unknown> = {},
  opens?: (request: OpenPanelRequest) => OpenPanelOutcome,
): Fake {
  return fakeHost(
    {
      feed: () => feed(),
      topics: () => topics(),
      pulse: () => PULSE,
      rule: () => ({
        id: "pro_0000000a",
        standing: "accepted",
        seq: 7,
        plan: null,
        refinement: null,
      }),
      comment: () => ({}),
      answer: () => ({}),
      ...overrides,
    },
    opens,
  );
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
      "qst_0000000c",
    ]);
    expect(view.one('[data-post="pro_0000000a"] .babel-claim').textContent).toBe(
      "Pin the engine profile before a run starts",
    );
    expect(view.one('[data-post="pro_0000000a"] .babel-kind').textContent).toBe("Proposal");
    expect(view.one('[data-post="pro_0000000a"] .babel-topic').textContent).toBe("t/babel");
    expect(view.one('[data-post="pro_0000000a"] .babel-comments').textContent).toBe("2 comments");
    // The unreviewed finding carries the ring and no figure.
    expect(view.one('[data-post="fnd_0000000b"] .babel-dot').getAttribute("data-tone")).toBe(
      "none",
    );
    expect(view.all('[data-post="fnd_0000000b"] .babel-score')).toHaveLength(0);
    // A row nobody is waiting on offers no acts and states no reason.
    expect(view.all('[data-post="fnd_0000000b"] [data-ruling]')).toHaveLength(0);
    expect(view.all('[data-post="fnd_0000000b"] .babel-why')).toHaveLength(0);
    // An absent optional judgement part adds neither a section nor a paid sweep.
    expect(view.all(".babel-jev-sweep")).toHaveLength(0);
    expect(fake.to("jev.sweep")).toHaveLength(0);
    await view.unmount();
  });

  test("candidate rows distinguish grounded objections from review-role opposition", async () => {
    const unchallenged = post({ id: "hyp_00000001", kind: "hypothesis", surface: "queue" });
    const challenged = post({
      id: "hyp_00000002",
      kind: "hypothesis",
      surface: "queue",
      challenges: { objections: 3, distinctRuns: 2 },
    });
    const fake = hub({ feed: () => feed({ posts: [unchallenged, challenged] }) });
    const view = await mount(<HomePanel host={fake.host} />);
    expect(view.one('[data-post="hyp_00000001"]').textContent).toContain("No recorded challenge");
    expect(view.one('[data-post="hyp_00000002"]').textContent).toContain("3 grounded objections");
    expect(view.one('[data-post="hyp_00000002"]').textContent).toContain("2 source runs");
    await view.unmount();
  });

  test("a row carries both the subject and standing axes", async () => {
    const crowded = post({
      reviewing: true,
      topics: [
        { id: "ent_0000beef", name: "babel" },
        { id: "ent_0000cafe", name: "tyrode-infra" },
        { id: "ent_0000f00d", name: "manifold" },
      ],
    });
    const fake = hub({ feed: () => feed({ posts: [crowded], total: 1, desk: 1 }) });
    const view = await mount(<HomePanel host={fake.host} />);
    expect(
      view.all('[data-post="pro_0000000a"] .babel-topic').map((chip) => chip.textContent),
    ).toEqual(["t/babel", "+2"]);
    // The status axis, which the row had no word for at all.
    expect(view.one('[data-post="pro_0000000a"] .babel-established').textContent).toBe("shaky");
    await view.unmount();
  });

  // The desk arrives grouped: a concept holds one heading with its records beneath it, the
  // heading says which key holds them together, and a record no key groups is still drawn.
  test("a grouped desk shows the key once and hides no ungrouped record", async () => {
    const fake = hub({
      feed: () =>
        feed({
          total: 2,
          groups: [
            {
              key: "ent_0000beef",
              keyKind: "topic",
              label: "t/babel",
              records: 4,
              posts: ["pro_0000000a", "fnd_0000000b"],
            },
            { key: "", keyKind: "none", label: "", records: 1, posts: ["qst_0000000c"] },
          ],
        }),
    });
    const view = await mount(<HomePanel host={fake.host} />);
    const heads = view.all(".babel-group-head").map((head) => head.textContent);
    // One heading for the concept, none for the record that is on its own.
    expect(heads).toHaveLength(1);
    expect(heads[0]).toContain("all filed under t/babel");
    // The group's true size, and the fact that the page is not carrying all of it.
    expect(heads[0]).toContain("4 records");
    expect(heads[0]).toContain("2 more under it");
    // Every post is still on the page, grouped or not.
    expect(view.all(".babel-row").map((row) => row.getAttribute("data-post"))).toEqual([
      "pro_0000000a",
      "fnd_0000000b",
      "qst_0000000c",
    ]);
    await view.unmount();
  });

  test("opens on the desk, reads it once, and says so", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    expect(fake.to("feed")).toHaveLength(1);
    expect(fake.last("feed")).toEqual({
      sort: "next",
      window: "day",
      kinds: [],
      surface: "desk",
      established: [],
      group: "topic",
      limit: 15,
      offset: 0,
    });
    // The default surface is the desk, and the desk's own size is beside the list's count.
    expect(view.one(".babel-sentence").textContent).toContain("your desk");
    expect(view.one(".babel-desk").textContent).toContain("2 on your desk");
    expect(view.one(".babel-sentence").textContent).toContain("sorted by next");
    expect(view.one(".babel-count").textContent).toContain("3");
    expect(view.one(".babel-pulse").textContent).toContain("Today Babel read 4 sessions");
    await view.unmount();
  });

  test("a question row offers the three answers and no rulings", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    expect(
      view.all('[data-post="qst_0000000c"] [data-answer]').map((button) => button.textContent),
    ).toEqual(["Answer", "I don't know", "Stop asking"]);
    expect(view.all('[data-post="qst_0000000c"] [data-ruling]')).toHaveLength(0);
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

  // The controversial order lists only what the reviewers took both sides on inside one role,
  // so an empty one has to say which emptiness it is. "Babel has not posted anything yet" over
  // a corpus of thousands is a lie, and "nothing is contested" left alone is a finding this
  // deployment has not earned — the study that asked for this order measured 4 split records in
  // 120 and then flagged its own number as possibly the question bank agreeing with itself.
  test("an empty controversial list says what it is empty of, and not that the corpus agrees", async () => {
    const fake = hub({ feed: () => feed({ posts: [], total: 0, desk: 0 }) });
    const view = await mount(<HomePanel host={fake.host} />);
    // The emptiness the page already had: a fact about the surface.
    expect(view.one(".babel-state strong").textContent).toBe("Nothing is waiting on you");
    await view.press('[data-pick="sort"]');
    await view.press('[data-sort="controversial"]');
    // An order computed over a period keeps its menu open, and an open menu holds the read —
    // a confirmation on screen must not have its row pulled out from under it.
    await view.press('[data-pick="sort"]');
    // The read taken while the menu was open was held, so the list arrives on the next live
    // read — the same way every other read after a gesture does here.
    await view.wait(50);
    fake.announce();
    await view.wait(200);
    expect(view.one(".babel-state strong").textContent).toBe("Nothing here is split");
    const said = view.one(".babel-state span").textContent;
    expect(said).toContain("both sides on inside one question");
    expect(said).toContain("asked one question several ways");
    await view.unmount();
  });
});

describe("the sentence", () => {
  test("order and period controls expose the active selection", async () => {
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
    expect(view.one('[data-sort="top"]').getAttribute("aria-checked")).toBe("true");
    await view.press('[data-window="week"]');
    await view.settle();
    await view.press('[data-pick="sort"]');
    expect(view.one('[data-window="week"]').getAttribute("aria-checked")).toBe("true");
    await view.unmount();
  });

  test("switching from a decaying browsing mode to the shelf restores all-time reception", async () => {
    const view = await mount(<HomePanel host={hub().host} />);
    await view.press('[data-pick="surface"]');
    await view.press('[data-surface="all"]');
    await view.press('[data-pick="sort"]');
    await view.press('[data-sort="rising"]');
    await view.press('[data-pick="surface"]');
    await view.press('[data-surface="shelf"]');
    await view.press('[data-pick="sort"]');
    expect(view.all("[data-sort]").map((button) => button.getAttribute("data-sort"))).toEqual([
      "next",
      "new",
      "top",
      "controversial",
    ]);
    expect(view.one('[data-sort="top"]').getAttribute("aria-checked")).toBe("true");
    expect(view.one('[data-window="all"]').getAttribute("aria-checked")).toBe("true");
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

  test("`m` leaves the desk for everything, and the order follows it", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("m");
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ surface: "all", sort: "hot" });
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
    expect(view.one('[data-post="pro_0000000a"] .babel-confirm').textContent).toContain(
      "Endorse this record",
    );
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
        plan: {
          kind: "topic",
          operation: "create",
          applied: true,
          declined: false,
          entityId: "ent_0000dead",
        },
        refinement: null,
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
    await view.press('[data-post="qst_0000000c"] [data-answer="answered"]');
    expect(view.one(".babel-confirm").textContent).toContain("read by the answer interpreter");
    // An empty answer records nothing: the outcome that carries words requires them.
    await view.press(".babel-confirm .babel-primary");
    expect(fake.to("answer")).toHaveLength(0);
    await view.type(".babel-confirm textarea", "the shared catalog is retired");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("answer")).toEqual({
      id: "qst_0000000c",
      outcome: "answered",
      text: "the shared catalog is retired",
    });
    expect(view.one('[data-post="qst_0000000c"] .babel-acted').textContent).toBe("answered");
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
    expect(view.one('[data-post="qst_0000000c"] .babel-confirm').textContent).toContain(
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

  test("a claim under the pointer opens the same record, in a seat of its own", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-open="fnd_0000000b"]');
    expect(looking().recordId).toBe("fnd_0000000b");
    expect(fake.opened).toEqual([
      { panelId: "atyrode.babel.feed.record", arg: { recordId: "fnd_0000000b" } },
    ]);
    await view.unmount();
  });

  test("a topic on a row opens it exactly as the rail's own row does", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="pro_0000000a"] .babel-topic');
    expect(looking().topic).toBe("ent_0000beef");
    expect(fake.opened).toEqual([
      { panelId: "atyrode.babel.feed.topic", arg: { topic: "ent_0000beef" } },
    ]);
    await view.unmount();
  });
});

/*
  THE SEATS (#533). A gesture that OPENS something asks the host for a tile of this plugin's
  own carrying what it was opened for, and points the selection at the same thing so the tiles
  that carry no argument follow along. `j`/`k` only point: a seat per row the reader scrolled
  past is a workspace nobody asked for.
*/
describe("the seats", () => {
  test("↵ opens the record in a seat of its own, and `j`/`k` only point", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("j");
    await view.key("Enter");
    expect(fake.opened).toEqual([
      { panelId: "atyrode.babel.feed.record", arg: { recordId: "pro_0000000a" } },
    ]);
    expect(looking().recordId).toBe("pro_0000000a");
    await view.key("j");
    expect(looking().recordId).toBe("fnd_0000000b");
    expect(fake.opened).toHaveLength(1);
    await view.unmount();
  });

  test("a topic in the rail opens the topic panel for it", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-topic="ent_0000cafe"]');
    expect(fake.opened).toEqual([
      { panelId: "atyrode.babel.feed.topic", arg: { topic: "ent_0000cafe" } },
    ]);
    expect(looking().topic).toBe("ent_0000cafe");
    await view.unmount();
  });

  test("a view holding no seat says so, and the selection moves anyway", async () => {
    const fake = hub({}, () => ({ ok: false, refused: "no_tile" }));
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("j");
    await view.key("Enter");
    expect(view.one(".babel-toast").textContent).toContain("nowhere to open it");
    // A refusal is not a ruling: there is nothing to take back, so no way back is offered.
    expect(view.all(".babel-toast button")).toHaveLength(0);
    expect(looking().recordId).toBe("pro_0000000a");
    await view.unmount();
  });

  test("the rail's refusal lands in the same toast", async () => {
    const fake = hub({}, () => ({ ok: false, refused: "no_tile" }));
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-topic="ent_0000cafe"]');
    expect(view.one(".babel-toast").textContent).toContain("nowhere to open it");
    expect(looking().topic).toBe("ent_0000cafe");
    await view.unmount();
  });

  test("a refusal that is not about a seat is silent, and the peek follows regardless", async () => {
    const fake = hub({}, () => ({ ok: false, refused: "other_plugin" }));
    const view = await mount(<HomePanel host={fake.host} />);
    await view.key("j");
    await view.key("Enter");
    expect(view.all(".babel-toast")).toHaveLength(0);
    expect(looking().recordId).toBe("pro_0000000a");
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
    expect(
      view.all(".babel-topic-list button").map((row) => row.getAttribute("data-topic")),
    ).toEqual(["ent_0000beef", "ent_0000cafe"]);
    await view.press('[data-topic="ent_0000cafe"]');
    expect(looking().topic).toBe("ent_0000cafe");
    await view.unmount();
  });

  test("what is filed under nothing is a filter over the feed, not a topic", async () => {
    const fake = hub();
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press(".babel-topic-unfiled");
    await view.settle();
    expect(fake.last("feed")).toMatchObject({ topic: "unfiled", surface: "all", sort: "new" });
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
    expect(view.one(".babel-topic-why").textContent).toContain(
      "four records cite the same checkout",
    );
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
        post({
          id: "hyp_0000000e",
          kind: "hypothesis",
          title: "Restic prune has never run",
          awaiting: false,
          why: "",
        }),
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
    expect(view.one('[data-post="pro_0000000a"] .babel-score').hasAttribute("data-ticked")).toBe(
      true,
    );
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

/*
  THE TWO RENDER DEFECTS THE LINTER FOUND HERE (#345), as the reader meets them: a clock that
  only moved when the hub did, and a hold on the shared feed that never went false again.
*/
describe("the clock and the hold", () => {
  afterEach(() => setSystemTime());

  test("attention ages while the hub says nothing, without borrowing the record date", async () => {
    /*
      The clock is held still and moved by hand, which is what makes this a statement about
      the surface: the hub answers the same thing throughout, and nothing but time passes.
      09:00:59.100 puts the wall clock's next minute 900ms of real time out, which is when
      the page is owed its next reading of the clock.
    */
    const base = Date.parse("2026-09-12T09:00:59.100Z");
    setSystemTime(new Date(base));
    const stamp = new Date(base - 59_000).toISOString();
    const fake = hub({
      feed: () =>
        feed({
          posts: [
            post({ attention: { at: stamp, basis: "evidence" } }),
            post({ id: "pro_0000000d", attention: { at: null, basis: null } }),
          ],
          total: 2,
        }),
    });
    const view = await mount(<HomePanel host={fake.host} />);
    expect(view.one('[data-post="pro_0000000a"] time').getAttribute("datetime")).toBe(stamp);
    expect(view.one('[data-post="pro_0000000a"] .babel-age').textContent).toContain("just now");
    expect(view.all('[data-post="pro_0000000d"] time')).toHaveLength(0);
    expect(view.one('[data-post="pro_0000000d"] .babel-age').textContent).toContain("unknown");
    setSystemTime(new Date(base + 5 * 60_000));
    await view.wait(1_200);
    expect(view.one('[data-post="pro_0000000a"] .babel-age').textContent).toContain("5m ago");
    expect(view.one('[data-post="pro_0000000d"] .babel-age').textContent).toContain("unknown");
    await view.unmount();
  });

  test("a recorded act holds the list while it is read, and lets the world back in after", async () => {
    const base = Date.parse("2026-09-12T09:00:00.000Z");
    setSystemTime(new Date(base));
    let answer = feed();
    const fake = hub({ feed: () => answer });
    const view = await mount(<HomePanel host={fake.host} />);
    await view.press('[data-post="qst_0000000c"] [data-answer="answered"]');
    await view.type(".babel-confirm textarea", "the shared catalog is retired");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(view.one('[data-post="qst_0000000c"] .babel-acted').textContent).toBe("answered");

    answer = feed({
      posts: [post({ id: "hyp_0000000e", title: "Restic prune has never run", awaiting: false })],
      total: 1,
    });
    fake.announce();
    await view.wait(120);
    // The confirmation is on screen and is being read: the list holds still under it.
    expect(view.all('[data-post="hyp_0000000e"]')).toHaveLength(0);

    // Six seconds later it is no longer a gesture. The hold is the reader's, not the
    // session's, so the answer lands — and it lands on every other reader of this feed too.
    setSystemTime(new Date(base + 6_000));
    fake.announce();
    await view.wait(400);
    expect(view.all('[data-post="hyp_0000000e"]')).toHaveLength(1);
    await view.unmount();
  });
});
