import { beforeEach, describe, expect, test } from "bun:test";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { forgetSelection, look } from "./api.ts";
import { RecordPanel } from "./record.tsx";
import { Denial, fakeHost, mount, peel, thread, topics, type Fake } from "./testing.tsx";

/*
  THE RECORD PANEL: the peel, the filing desk and the thread.

  What is tested is the peel's own promises — nothing fetches on open, an absent section is
  absent, ids live at depth 5 — and the two acts that write: a ruling from the record, and a
  filing, which is a link with a rationale rather than a checkbox.
*/

function hub(overrides: Record<string, (args: unknown) => unknown> = {}): Fake {
  return fakeHost({
    record: () => peel(),
    thread: () => thread(),
    topics: () => topics(),
    rule: () => ({ id: "pro_0000000a", standing: "accepted", seq: 9, plan: null }),
    comment: () => ({}),
    file: () => ({}),
    unfile: () => ({}),
    ...overrides,
  });
}

beforeEach(() => {
  resetPolledResources();
  forgetSelection();
});

describe("what it is looking at", () => {
  test("says so when nothing is open, and reads nothing", async () => {
    const fake = hub();
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(view.text()).toContain("No record open");
    expect(fake.calls).toHaveLength(0);
    await view.unmount();
  });

  test("follows the selection Home points at it", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(fake.last("record")).toEqual({ id: "pro_0000000a" });
    expect(fake.last("thread")).toEqual({ id: "pro_0000000a" });
    expect(view.one(".babel-record-claim").textContent).toBe("Pin the engine profile before a run starts");
    await view.unmount();
  });

  test("a refused read says so and offers the read again", async () => {
    const fake = hub({
      record: () => {
        throw new Denial("no record answers to that id");
      },
    });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    await view.settle();
    expect(view.text()).toContain("no record answers to that id");
    await view.unmount();
  });
});

describe("the peel", () => {
  test("draws the claim, the case, the evidence, the reception and the machinery in one read", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    // One read for the whole record: opening a depth is a disclosure, never a request.
    expect(fake.to("record")).toHaveLength(1);
    expect(view.all("[data-depth]").map((depth) => depth.getAttribute("data-depth"))).toEqual([
      "1",
      "2",
      "3",
      "4",
      "5",
    ]);
    expect(view.text()).toContain("A run should state its profile, model and ceiling");
    // A field name from the store is read as the question it answers.
    expect(view.all(".babel-field-label").map((label) => label.textContent)).toEqual([
      "Problem",
      "Verification criteria",
    ]);
    expect(view.one(".babel-evidence blockquote").textContent).toBe(
      "I want to know what it will cost before I press go.",
    );
    expect(view.one(".babel-evidence figcaption").textContent).toContain("the operator");
    expect(view.one(".babel-evidence figcaption a").getAttribute("href")).toBe("/s/omp-1");
    // The reception is a table of what each reviewer was asked, and the opposition is prose.
    expect(view.all(".babel-roles tbody tr").map((row) => row.getAttribute("data-role"))).toEqual([
      "reception",
      "challenge",
    ]);
    expect(view.text()).toContain("The report is not available on every machine.");
    expect(view.text()).toContain("Babel's reviewers are split on this.");
    // Ids live at depth 5, and the related strip carries the other record's own words.
    expect(view.one('[data-depth="5"]').textContent).toContain("pro_0000000a");
    expect(view.one(".babel-related").textContent).toContain("Cost is invisible until a run ends");
    await view.unmount();
  });

  test("a section the record does not hold is absent rather than an empty heading", async () => {
    const fake = hub({
      record: () => peel({ case: {}, evidence: [], machinery: {}, related: [] }),
    });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(view.all("[data-depth]").map((depth) => depth.getAttribute("data-depth"))).toEqual(["1", "4"]);
    expect(view.all(".babel-related")).toHaveLength(0);
    await view.unmount();
  });

  test("the record offers the acts a row cannot: duplicate and reopen", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(view.all("[data-ruling]").map((button) => button.getAttribute("data-ruling"))).toEqual([
      "accept",
      "reject",
      "defer",
      "duplicate",
      "reopen",
      "refine",
      "ask",
    ]);
    await view.press('[data-ruling="duplicate"]');
    // Duplicate names the original, which is why it is not offered on a row.
    await view.type(".babel-confirm input", "pro_0000000f");
    await view.press(".babel-confirm .babel-primary");
    await view.settle();
    expect(fake.last("rule")).toEqual({
      id: "pro_0000000a",
      ruling: "duplicate",
      note: "",
      duplicateOf: "pro_0000000f",
    });
    // The record is read again, so what it says about itself is the store's answer.
    expect(fake.to("record").length).toBeGreaterThan(1);
    await view.unmount();
  });

  test("a ruling by key goes through the same confirmation as one by click", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    view.one(".babel-record-root").focus();
    await view.key("d");
    expect(fake.to("rule")).toHaveLength(0);
    expect(view.one(".babel-confirm").textContent).toContain("Not now.");
    await view.unmount();
  });
});

describe("the thread", () => {
  test("merges the conversation and the log, newest first, replies one level deep", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    const entries = view.all(".babel-comments > li");
    // The ruling is the newest entry and is an act, not a comment.
    expect(entries[0]?.className).toContain("babel-act-row");
    expect(entries[0]?.getAttribute("data-act")).toBe("defer");
    expect(entries[0]?.textContent).toContain("deferred this");
    expect(entries[0]?.textContent).toContain("waiting on the engine report");
    expect(entries[1]?.className).toContain("babel-comment");
    expect(view.one(".babel-replies").textContent).toContain("Then the launch form says so instead of guessing.");
    // The operator is "you"; a run is its own name.
    expect(view.one(".babel-replies .babel-comment-author").textContent).toBe("you");
    await view.unmount();
  });

  test("the box records a comment, and its toggle records a question instead", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    await view.type(".babel-comment-box textarea", "the ceiling is the part I care about");
    await view.press(".babel-comment-box .babel-primary");
    await view.settle();
    expect(fake.last("comment")).toEqual({
      id: "pro_0000000a",
      text: "the ceiling is the part I care about",
      kind: "comment",
    });
    expect(fake.to("thread").length).toBeGreaterThan(1);
    await view.press('.babel-comment-box button[aria-pressed="false"]');
    await view.type(".babel-comment-box textarea", "which machines have the engine?");
    await view.press(".babel-comment-box .babel-primary");
    await view.settle();
    expect(fake.last("comment")).toEqual({
      id: "pro_0000000a",
      text: "which machines have the engine?",
      kind: "question",
    });
    await view.unmount();
  });
});

describe("the filing desk", () => {
  test("reads the topics only when the fold opens, then files with a rationale", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(fake.to("topics")).toHaveLength(0);
    await view.press(".babel-filing .disclosure__header");
    await view.settle();
    expect(fake.to("topics")).toHaveLength(1);
    // Filing does not mint a topic: it is a list of the ones the ledger holds.
    expect(view.all(".babel-filing option").map((option) => option.textContent)).toEqual([
      "Choose a topic",
      "t/babel",
      "t/manifold",
    ]);
    await view.type(".babel-filing select", "ent_0000cafe");
    await view.type(".babel-filing textarea", "it is about the plugin host");
    await view.press(".babel-filing .babel-primary");
    await view.settle();
    expect(fake.last("file")).toEqual({
      id: "pro_0000000a",
      entity: "ent_0000cafe",
      rationale: "it is about the plugin host",
    });
    await view.unmount();
  });

  test("withdrawing a filing is a row with a reason, not an absence", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    await view.press(".babel-filing .disclosure__header");
    await view.press('[data-unfile="ent_0000beef"]');
    await view.type(".babel-filing textarea", "it was never about babel itself");
    await view.press(".babel-filing .babel-primary");
    await view.settle();
    expect(fake.last("unfile")).toEqual({
      id: "pro_0000000a",
      entity: "ent_0000beef",
      reason: "it was never about babel itself",
    });
    await view.unmount();
  });
});
