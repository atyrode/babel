import { afterEach, beforeEach, describe, expect, setSystemTime, test } from "bun:test";
import { resetPolledResources } from "@manifold/plugin/hooks";
import { forgetSelection, look } from "./api.ts";
import { RecordPanel } from "./record.tsx";
import {
  Denial,
  fakeHost,
  mount,
  peel,
  pointAt,
  post,
  thread,
  topics,
  type Fake,
} from "./testing.tsx";

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
    rule: () => ({
      id: "pro_0000000a",
      standing: "accepted",
      seq: 9,
      plan: null,
      refinement: null,
    }),
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

  test("a seat opened for nothing follows the selection Home points at it", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(fake.last("record")).toEqual({ id: "pro_0000000a" });
    expect(fake.last("thread")).toEqual({ id: "pro_0000000a" });
    expect(view.one(".babel-record-claim").textContent).toBe(
      "Pin the engine profile before a run starts",
    );
    // It keeps following: a tile a principal placed by hand IS §8.7's peek pane, walked by
    // Home's own `j`/`k`.
    await pointAt({ recordId: "fnd_0000000b" });
    expect(fake.last("record")).toEqual({ id: "fnd_0000000b" });
    await view.unmount();
  });

  test("a seat opened FOR a record is pinned to it, whatever Home is looking at", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} arg={{ recordId: "fnd_0000000b" }} />);
    expect(fake.last("record")).toEqual({ id: "fnd_0000000b" });
    await pointAt({ recordId: "qst_0000000c" });
    // The store moved and this seat did not read again: two records are two tiles, each
    // reading its own argument.
    expect(fake.to("record")).toHaveLength(1);
    expect(fake.last("record")).toEqual({ id: "fnd_0000000b" });
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
  test("opens the grounded objection and its actual source receipt independently of review votes", async () => {
    const candidate = post({
      id: "hyp_00000001",
      kind: "hypothesis",
      challenges: { objections: 1, distinctRuns: 1 },
    });
    const fake = hub({
      record: () =>
        peel({
          post: candidate,
          challenges: [
            {
              id: "hyp_00000002",
              kind: "hypothesis",
              runId: "independent-challenger",
              grounds: "missing-check",
              summary: "The rollback path was not checked.",
            },
          ],
        }),
      run: () => ({
        run: null,
        receipt: {
          runId: "independent-challenger",
          stage: "challenge",
          note: "An independently recorded receipt.",
        },
      }),
    });
    const view = await mount(<RecordPanel host={fake.host} arg={{ recordId: candidate.id }} />);
    expect(view.text()).toContain("1 grounded objection");
    expect(view.text()).toContain("Ground: missing-check");
    expect(fake.to("run")).toHaveLength(0);
    await view.press(".babel-challenge-run .disclosure__header");
    expect(view.text()).toContain("An independently recorded receipt.");
    expect(fake.last("run")).toEqual({ id: "independent-challenger" });
    await view.press(".babel-field-list .babel-link");
    expect(fake.opened.at(-1)?.arg).toEqual({ recordId: "hyp_00000002" });
    await view.unmount();
  });

  test("source-run read failure can be retried and missing historical receipts stay explicit", async () => {
    let refused = true;
    const fake = hub({
      record: () =>
        peel({
          post: post({ kind: "hypothesis", challenges: { objections: 1, distinctRuns: 1 } }),
          challenges: [
            {
              id: "obs_00000002",
              kind: "observation",
              runId: "historical-source",
              grounds: "evidence",
              summary: "The transcript shows a different result.",
            },
          ],
        }),
      run: () => {
        if (refused) throw new Denial("This source run cannot be read.");
        return { run: null, receipt: null };
      },
    });
    const view = await mount(<RecordPanel host={fake.host} arg={{ recordId: "hyp_00000001" }} />);
    await view.press(".babel-challenge-run .disclosure__header");
    expect(view.text()).toContain("This source run cannot be read.");
    refused = false;
    await view.press(".babel-challenge-run .disclosure__header");
    await view.press(".babel-challenge-run .disclosure__header");
    expect(view.text()).toContain("No historical run row");
    expect(view.text()).toContain("No receipt is held");
    await view.unmount();
  });

  test("review-role opposition alone leaves the opened candidate without a recorded challenge", async () => {
    const fake = hub({ record: () => peel({ post: post({ kind: "hypothesis" }) }) });
    const view = await mount(<RecordPanel host={fake.host} arg={{ recordId: "hyp_00000001" }} />);
    expect(view.text()).toContain("No recorded challenge");
    expect(view.all(".babel-challenge-run")).toHaveLength(0);
    await view.unmount();
  });

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
      record: () => peel({ case: {}, evidence: [], repository: [], machinery: {}, related: [] }),
    });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(view.all("[data-depth]").map((depth) => depth.getAttribute("data-depth"))).toEqual([
      "1",
      "4",
    ]);
    expect(view.all(".babel-related")).toHaveLength(0);
    await view.unmount();
  });

  test("an excerpt the bytes do not carry says so where the excerpt is read", async () => {
    /*
      #348's whole point is that a fabricated quote must not read like a real one. The page is
      where a person meets the claim, so the verdict is beside the words rather than in a log
      nobody opens — and a citation that quoted nothing renders no pull-quote at all, because
      an empty blockquote reads as a person who said nothing.
    */
    const fake = hub({
      record: () =>
        peel({
          evidence: [
            {
              excerpt: "we deleted the snapshot on purpose",
              speaker: "",
              session: null,
              note: "the claim rests on this sentence",
              line: 12,
              verification: "absent",
            },
            {
              excerpt: "",
              speaker: "",
              session: null,
              note: "an imported record, written before anything looked",
              line: null,
              verification: "",
            },
          ],
        }),
    });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    const cited = view.all(".babel-evidence");
    expect(cited[0]?.getAttribute("data-verification")).toBe("absent");
    expect(cited[0]?.textContent).toContain("not found in this session");
    // An unchecked citation is not a clean one, and neither is rendered as the other: the
    // second carries no quote, no verdict and no claim about either.
    expect(cited[1]?.getAttribute("data-verification")).toBeNull();
    expect(view.all(".babel-evidence blockquote")).toHaveLength(1);
    await view.unmount();
  });

  test("which codebase it is about is read where the evidence is, and says on whose word", async () => {
    const fake = hub();
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    // A repository Babel probed in the cited session's own workspace.
    expect(view.one(".babel-repository").textContent).toBe("observed in github.com/atyrode/babel");
    // The commit is an identifier and belongs at depth 5, so the sentence carries none of it.
    expect(view.one('[data-depth="3"]').textContent).not.toContain("9c44aaf");
    await view.unmount();
  });

  test("a repository a transcript only named does not read as one Babel saw", async () => {
    const fake = hub({
      record: () =>
        peel({
          // A hypothesis carries no citation of its own — its observations hold those — so the
          // depth has to open for the repository alone, or the one thing the page knows about
          // which project the claim is about would be unreachable.
          evidence: [],
          repository: [
            {
              remote: "github.com/tyrode/tyrode-infra",
              commit: "",
              reference: "",
              provenance: "named",
            },
          ],
        }),
    });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    const line = view.one(".babel-repository");
    expect(line.textContent).toBe(
      "named in the evidence, not observed: github.com/tyrode/tyrode-infra",
    );
    expect(line.getAttribute("data-provenance")).toBe("named");
    expect(view.all('[data-depth="3"] .babel-peel-count')).toHaveLength(0);
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

  test("a proposed next action is read where the record is, and answered there", async () => {
    const fake = hub({
      decide: () => ({
        id: "nxt_0000000a",
        recordId: "pro_0000000a",
        standing: "declined",
        seq: 1,
        at: "2026-09-12T10:00:00Z",
      }),
    });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    // The kind reads as what it asks for, not as the wire value a run wrote.
    expect(view.one(".babel-next-action").textContent).toContain("Draft an issue");
    expect(view.one(".babel-next-action").textContent).toContain(
      "Draft an issue for stating a run's profile before the first byte.",
    );
    // And it says what an acceptance is, because a press that implied Babel opened an issue
    // would be the one misreading that costs an operator something outside Babel.
    expect(view.one(".babel-next-action").textContent).toContain("Babel publishes nothing");

    await view.type('.babel-next-action input[type="text"]', "not this quarter");
    await view.press('.babel-next-action [data-decision="declined"]');
    await view.settle();
    expect(fake.last("decide")).toEqual({
      nextActionId: "nxt_0000000a",
      decision: "declined",
      note: "not this quarter",
    });
    // The answer stands where the record is read, and the record is read again behind it.
    expect(view.one(".babel-next-action").getAttribute("data-standing")).toBe("declined");
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
    expect(view.one(".babel-replies").textContent).toContain(
      "Then the launch form says so instead of guessing.",
    );
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

describe("the clock it reads ages against", () => {
  afterEach(() => setSystemTime());

  test("an age ages while the record itself does not change (#345)", async () => {
    // The clock is held still and moved by hand: the hub answers the same peel throughout and
    // nothing but time passes. 09:00:59.100 puts the page's next reading of the clock 900ms
    // of real time out.
    const base = Date.parse("2026-09-12T09:00:59.100Z");
    setSystemTime(new Date(base));
    const stamp = new Date(base - 59_000).toISOString();
    const fake = hub({ record: () => peel({ post: post({ createdAt: stamp }) }) });
    look({ recordId: "pro_0000000a" });
    const view = await mount(<RecordPanel host={fake.host} />);
    expect(view.one(".babel-age").textContent).toContain("just now");
    setSystemTime(new Date(base + 5 * 60_000));
    await view.wait(1_200);
    expect(view.one(".babel-age").textContent).toContain("5m ago");
    await view.unmount();
  });
});
