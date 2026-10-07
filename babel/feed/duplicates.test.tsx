import "./dom.ts";
import { beforeEach, describe, expect, test } from "bun:test";
import { resetPolledResources } from "@manifold/plugin/hooks";
import {
  type DuplicateApplied,
  type DuplicateIntent,
  type DuplicatePlan,
  type DuplicatePreview,
  type DuplicateSweepReport,
  type RecordPeel,
} from "../contract.ts";
import { DuplicateAction } from "./duplicates.tsx";
import { JevSweep } from "./jev.tsx";
import { Peel } from "./peel.tsx";
import { Denial, fakeHost, mount, peel, post } from "./testing.tsx";

const intent: DuplicateIntent = {
  kind: "merge-duplicate-records",
  representative: "pro_00000001",
  members: [
    {
      recordId: "pro_00000001",
      revision: 1,
      fingerprint: "a".repeat(64),
      kind: "proposal",
      runId: "run-shared",
      sourceIds: ["source-shared"],
      unresolvedSources: 0,
    },
    {
      recordId: "pro_00000002",
      revision: 3,
      fingerprint: "b".repeat(64),
      kind: "proposal",
      runId: "run-shared",
      sourceIds: ["source-shared"],
      unresolvedSources: 0,
    },
    {
      recordId: "pro_00000003",
      revision: 0,
      fingerprint: "c".repeat(64),
      kind: "proposal",
      runId: "",
      sourceIds: [],
      unresolvedSources: 0,
    },
  ],
  pairs: [
    {
      a: "pro_00000001",
      b: "pro_00000002",
      evidence: "Both specify the same pre-launch profile lock.",
    },
    {
      a: "pro_00000001",
      b: "pro_00000003",
      evidence: "Same remedy with historical provenance missing.",
    },
    {
      a: "pro_00000002",
      b: "pro_00000003",
      evidence: "Matching verification criterion, not a topic merge.",
    },
  ],
  audit: { records: 3, distinctRuns: 1, distinctSources: 1, missingRuns: 1, missingSources: 1 },
};
const plan: DuplicatePlan = {
  candidates: intent.members.map((member) => ({
    ...member,
    title: "Pin a run profile",
    claim: "State the profile before launch.",
  })),
  eligible: 5,
  continuation: "pro_00000003",
  maxPairs: 3,
  newSuggestionsUpperBound: 1,
};
const report: DuplicateSweepReport = {
  candidates: 3,
  attempted: 3,
  judged: 3,
  truncated: false,
  stopped: "",
  suggestions: [
    {
      recordId: intent.representative,
      revision: 1,
      kind: "ask-question",
      subject: "",
      aspect: "duplicate-cluster",
      summary: "Link the repeated profile-lock remedy",
      rationale: "These are repeated records, not independent evidence.",
      basis: "fixture-bank",
      intent,
    },
  ],
};
const preview: DuplicatePreview = {
  nextActionId: "nxt_00000001",
  recordId: intent.representative,
  intent,
  fingerprint: "d".repeat(64),
  links: [
    { fromId: intent.representative, toId: "pro_00000002", kind: "corroborates", exists: true },
    { fromId: intent.representative, toId: "pro_00000003", kind: "corroborates", exists: false },
  ],
  state: "ready",
  reason: "",
  application: null,
};
const application: DuplicateApplied = {
  nextActionId: preview.nextActionId,
  recordId: preview.recordId,
  operatorId: "root-operator",
  at: "2026-09-29T10:00:00Z",
  links: preview.links.map(({ exists: _exists, ...link }) => link),
};
function duplicatePeel(): RecordPeel {
  return peel({
    post: post({ id: intent.representative }),
    nextActions: [
      {
        id: preview.nextActionId,
        kind: "ask-question",
        summary: "Link repeated records",
        rationale: "Review this exact cluster.",
        proposedBy: "atyrode.babel.jev",
        at: "2026-09-29T09:00:00Z",
        standing: "proposed",
        history: [],
        intent,
      },
    ],
  });
}
const onActed = (): void => undefined;

beforeEach(() => resetPolledResources());

describe("duplicate planning and delivery", () => {
  test.each(["absent", "disabled", "unfunded"])(
    "%s Jev leaves no empty duplicate or paid surface",
    async (state) => {
      const fake = fakeHost(
        state === "absent"
          ? {}
          : {
              "jev.duplicatesPlan": () => ({ plan: null, silent: state }),
            },
      );
      const view = await mount(<JevSweep host={fake.host} />);
      expect(view.all(".babel-jev-duplicates")).toHaveLength(0);
      expect(fake.to("jev.duplicates")).toHaveLength(0);
      expect(fake.to("suggest")).toHaveLength(0);
      await view.unmount();
    },
  );

  test("the free page, bounded judgement, submission and graph authority are separate gestures", async () => {
    const fake = fakeHost({
      "jev.duplicatesPlan": () => ({ plan, silent: "" }),
      "jev.duplicates": () => report,
      suggest: () => ({
        id: preview.nextActionId,
        recordId: preview.recordId,
        revision: 1,
        kind: "ask-question",
        subject: "",
        aspect: "duplicate-cluster",
        suggester: "p1",
        supersedes: "",
        at: "2026-09-29T09:00:00Z",
        outstanding: 1,
      }),
      duplicatePreview: () => preview,
      duplicateApply: () => application,
    });
    const view = await mount(<JevSweep host={fake.host} />);
    expect(fake.to("jev.duplicates")).toHaveLength(0);
    expect(view.all(".babel-duplicate-members li")).toHaveLength(3);
    expect(view.one(".babel-duplicate-members").textContent).toContain("revision 3");
    expect(view.one(".babel-duplicate-members").textContent).toContain("b".repeat(64));
    expect(view.one("[data-duplicate-judge]").hasAttribute("disabled")).toBe(true);
    await view.type('input[min="0"]', "0.96");
    await view.type(`input[max="64"]`, "3");
    await view.press("[data-duplicate-judge]");
    expect(fake.last("jev.duplicates")).toEqual({
      members: intent.members,
      cut: 0.96,
      judgements: 3,
    });
    expect(fake.to("jev.duplicates")).toHaveLength(1);
    expect(fake.to("suggest")).toHaveLength(0);
    expect(fake.to("duplicateApply")).toHaveLength(0);
    expect(view.all(".babel-duplicate-draft")).toHaveLength(1);
    expect(view.one(".babel-duplicate-evidence").textContent).toContain(
      intent.pairs[0]?.evidence ?? "",
    );
    expect(view.one(".babel-duplicate-audit").textContent).toContain("1 distinct sources");
    expect(view.one(".babel-duplicate-audit").textContent).toContain("1 records missing runs");
    expect(view.one("[data-duplicate-judge]").hasAttribute("disabled")).toBe(true);
    await view.press("[data-duplicate-submit]");
    expect(view.all(".babel-duplicate-draft")).toHaveLength(0);
    expect(view.all(".babel-duplicate-saved")).toHaveLength(1);
    expect(fake.to("duplicatePreview")).toHaveLength(0);
    expect(fake.to("duplicateApply")).toHaveLength(0);
    await view.press("[data-duplicate-preview]");
    expect(view.one('[data-exists="true"]').textContent).toContain("pro_00000002");
    expect(view.one('[data-exists="false"]').textContent).toContain("pro_00000003");
    expect(fake.to("duplicateApply")).toHaveLength(0);
    await view.press("[data-duplicate-apply]");
    expect(view.one(".babel-duplicate-action").getAttribute("data-state")).toBe("applied");
    expect(view.one(".babel-duplicate-application").textContent).toContain("root-operator");
    await view.unmount();
  });

  test("an incomplete bounded pass reports the stop without auto-continuing or offering withheld drafts", async () => {
    const fake = fakeHost({
      "jev.duplicatesPlan": () => ({ plan, silent: "" }),
      "jev.duplicates": () => ({
        ...report,
        attempted: 1,
        judged: 1,
        truncated: true,
        suggestions: [],
        stopped: "Cluster withheld: two pairs remain unjudged.",
      }),
    });
    const view = await mount(<JevSweep host={fake.host} />);
    await view.type('input[min="0"]', "0.9");
    await view.type('input[max="64"]', "1");
    await view.press("[data-duplicate-judge]");
    expect(view.text()).toContain("two pairs remain unjudged");
    expect(view.all("[data-duplicate-submit]")).toHaveLength(0);
    expect(fake.to("jev.duplicates")).toHaveLength(1);
    await view.press("[data-duplicate-next]");
    expect(fake.last("jev.duplicatesPlan")).toEqual({
      after: plan.continuation,
      limit: 24,
      ids: [],
    });
    expect(fake.to("jev.duplicates")).toHaveLength(1);
    await view.unmount();
  });
});

describe("saved duplicate authority", () => {
  test("partially resolved provenance keeps known sources distinct from unresolved citations", async () => {
    const partial: DuplicateIntent = {
      ...intent,
      members: intent.members.map((member) =>
        member.recordId === intent.representative ? { ...member, unresolvedSources: 2 } : member,
      ),
      audit: { ...intent.audit, missingSources: 2 },
    };
    const fake = fakeHost({});
    const view = await mount(
      <DuplicateAction host={fake.host} nextActionId={preview.nextActionId} intent={partial} />,
    );
    const representative = view.one(".babel-duplicate-members li:first-child");
    expect(representative.textContent).toContain("Sources: source-shared");
    expect(representative.textContent).toContain("Unresolved cited sessions: 2");
    expect(view.all(".babel-duplicate-unresolved")).toHaveLength(1);
    expect(view.one(".babel-duplicate-audit").textContent).toContain("1 distinct sources");
    expect(view.one(".babel-duplicate-audit").textContent).toContain(
      "2 records with missing or unresolved sources",
    );
    await view.unmount();
  });

  test("ledger decisions never link; explicit preview confirms once and reopens the durable application", async () => {
    let applied = false;
    const fake = fakeHost({
      decide: () => ({
        id: preview.nextActionId,
        recordId: preview.recordId,
        standing: "accepted",
        seq: 1,
        at: application.at,
      }),
      duplicatePreview: () => (applied ? { ...preview, state: "applied", application } : preview),
      duplicateApply: () => {
        applied = true;
        return application;
      },
    });
    let view = await mount(
      <Peel host={fake.host} peel={duplicatePeel()} now={0} onActed={onActed} />,
    );
    expect(fake.to("duplicatePreview")).toHaveLength(0);
    await view.press('[data-decision="accepted"]');
    expect(view.one(".babel-next-action").getAttribute("data-standing")).toBe("accepted");
    expect(fake.to("duplicateApply")).toHaveLength(0);
    expect(view.all("[data-duplicate-apply]")).toHaveLength(0);
    await view.press("[data-duplicate-preview]");
    expect(view.one(".babel-duplicate-effect").textContent).toContain(preview.fingerprint);
    expect(fake.to("duplicateApply")).toHaveLength(0);
    await view.press("[data-duplicate-apply]");
    expect(fake.last("duplicateApply")).toEqual({
      nextActionId: preview.nextActionId,
      fingerprint: preview.fingerprint,
      confirm: true,
    });
    expect(view.all("[data-duplicate-apply]")).toHaveLength(0);
    await view.unmount();
    view = await mount(<Peel host={fake.host} peel={duplicatePeel()} now={0} onActed={onActed} />);
    await view.press("[data-duplicate-preview]");
    expect(view.one(".babel-duplicate-action").getAttribute("data-state")).toBe("applied");
    expect(view.one(".babel-duplicate-application").textContent).toContain(application.at);
    expect(view.all("[data-duplicate-apply]")).toHaveLength(0);
    expect(fake.to("duplicateApply")).toHaveLength(1);
    await view.unmount();
  });

  test("an apply refused as stale clears confirmation and exposes the refused fresh preview", async () => {
    let stale = false;
    const fake = fakeHost({
      duplicatePreview: () =>
        stale ? { ...preview, state: "refused", reason: "Member revision changed." } : preview,
      duplicateApply: () => {
        stale = true;
        throw new Denial("Member revision changed.");
      },
    });
    const view = await mount(
      <Peel host={fake.host} peel={duplicatePeel()} now={0} onActed={onActed} />,
    );
    await view.press("[data-duplicate-preview]");
    await view.press("[data-duplicate-apply]");
    expect(view.one('[role="alert"]').textContent).toContain("Member revision changed");
    expect(view.one(".babel-duplicate-action").getAttribute("data-state")).toBe("refused");
    expect(view.all("[data-duplicate-apply]")).toHaveLength(0);
    await view.press("[data-duplicate-preview]");
    expect(view.one(".babel-duplicate-effect").textContent).toContain("Member revision changed");
    expect(view.all("[data-duplicate-apply]")).toHaveLength(0);
    expect(fake.to("duplicateApply")).toHaveLength(1);
    await view.unmount();
  });

  test("a non-operator refusal offers neither confirmation nor a graph write", async () => {
    const fake = fakeHost({
      duplicatePreview: () => {
        throw new Denial("Root operator required.");
      },
    });
    const view = await mount(
      <Peel host={fake.host} peel={duplicatePeel()} now={0} onActed={onActed} />,
    );
    await view.press("[data-duplicate-preview]");
    expect(view.one('[role="alert"]').textContent).toContain("Root operator required");
    expect(view.all("[data-duplicate-apply]")).toHaveLength(0);
    expect(fake.to("duplicateApply")).toHaveLength(0);
    await view.unmount();
  });
});

describe("declared proposal intent", () => {
  const cases: { intent: RecordPeel["proposalIntent"]; label: string }[] = [
    { intent: undefined, label: "Generic improvement" },
    { intent: { kind: "generic-improvement" }, label: "Generic improvement" },
    { intent: { kind: "record-refinement" }, label: "Record refinement" },
    { intent: { kind: "topic", operation: "create" }, label: "Topic create" },
    { intent: { kind: "topic", operation: "split" }, label: "Topic split" },
    { intent: { kind: "topic", operation: "merge" }, label: "Topic merge" },
    { intent: { kind: "topic", operation: "retire" }, label: "Topic retire" },
    { intent: { kind: "backlog", operation: "promote" }, label: "Backlog promote" },
  ];
  for (const entry of cases) {
    test(`${entry.label} follows the declared payload, never title prose`, async () => {
      const fake = fakeHost({});
      const value = peel({
        post: post({ kind: "proposal", title: "Merge duplicate topics and refine their records" }),
        ...(entry.intent === undefined ? {} : { proposalIntent: entry.intent }),
      });
      const view = await mount(<Peel host={fake.host} peel={value} now={0} onActed={onActed} />);
      expect(view.one(".babel-proposal-intent").textContent).toBe(entry.label);
      expect(view.all(".babel-duplicate-action")).toHaveLength(0);
      await view.unmount();
    });
  }
});
