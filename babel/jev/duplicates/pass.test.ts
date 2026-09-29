import { expect, test } from "bun:test";
import type { GuestActions } from "@manifold/plugin-kit/server";
import type { InstanceServiceDescription, ServiceInput, ServiceReply } from "@manifold/protocol";
import {
  ACTIONS,
  DUPLICATE_JUDGEMENTS,
  DUPLICATE_QUESTION,
  DuplicatePlanInputSchema,
  DuplicateSweepInputSchema,
  DuplicateSweepReportSchema,
  JEV_ACTIONS,
  type DuplicateMember,
  type DuplicatePlan,
  type DuplicatePlanInput,
  type DuplicateSweepInput,
} from "../../contract.ts";
import { JEV_CALL_CAP_BYTES, JEV_SERVICE, type JevServices } from "../server/credential.ts";
import { JevAnswers } from "../server/judge.ts";
import { DUPLICATE_HANDLERS } from "./doors.ts";
import { duplicates, duplicatesPlan } from "./pass.ts";

type Candidate = DuplicatePlan["candidates"][number];

const READY: InstanceServiceDescription = {
  serviceId: JEV_SERVICE.serviceId,
  defaultOwner: null,
  owner: { machineId: "fixture", name: "fixture", online: true },
  configuration: {
    revision: "duplicates-r1",
    pluginId: "atyrode.babel.jev",
    enabled: true,
    policySha256: "a".repeat(64),
  },
  connected: true,
  state: "ready",
  reason: null,
};

function candidate(n: number, overrides: Partial<Candidate> = {}): Candidate {
  return {
    recordId: `fnd_${n.toString(16).padStart(8, "0")}`,
    revision: n,
    fingerprint: n.toString(16).padStart(64, "0"),
    kind: "finding",
    title: `Fixture claim ${String(n)}`,
    claim: `Claim ${String(n)} about deterministic archive checks`,
    runId: "run_shared",
    sourceIds: ["source_shared"],
    unresolvedSources: 0,
    ...overrides,
  };
}

function membersOf(rows: readonly Candidate[]): DuplicateMember[] {
  return rows.map((row) => ({
    recordId: row.recordId,
    revision: row.revision,
    fingerprint: row.fingerprint,
    kind: row.kind,
    runId: row.runId,
    sourceIds: row.sourceIds,
    unresolvedSources: row.unresolvedSources,
  }));
}

/** Only the baseline reading door is reachable; every attempted write fails this fixture. */
function baseline(rows: Candidate[], change?: (read: number, rows: Candidate[]) => Candidate[]) {
  const reads: DuplicatePlanInput[] = [];
  const actions: GuestActions = {
    call: async (args) => {
      if (args.action !== ACTIONS.duplicatePlan) throw new Error(`not a read: ${args.action}`);
      const input = DuplicatePlanInputSchema.parse(args.input);
      reads.push(input);
      const eligible = change?.(reads.length, rows) ?? rows;
      const selected = eligible.filter(
        (row) => input.ids.length === 0 || input.ids.includes(row.recordId),
      );
      let maxPairs = 0;
      for (const [index, a] of selected.entries()) {
        maxPairs += selected.slice(index + 1).filter((b) => a.kind === b.kind).length;
      }
      return {
        candidates: selected,
        eligible: eligible.length,
        continuation: "",
        maxPairs,
        newSuggestionsUpperBound: Math.floor(selected.length / 2),
      } satisfies DuplicatePlan;
    },
  };
  return { actions, reads };
}

/** What the host's own reply type admits as an answer: the projection's leaves, as JSON. */
type Answered = Extract<ServiceReply, { ok: true }>["result"];

interface HostOptions {
  roster?: InstanceServiceDescription[];
  answer?: (n: number, input: ServiceInput) => Answered | null;
}

function host(options: HostOptions = {}) {
  const asks: { operationId: string; input: ServiceInput }[] = [];
  const services: JevServices = {
    listInstances: async () => ({ defaultOwner: null, services: options.roster ?? [READY] }),
    invokeInstance: async (args) => {
      asks.push({ operationId: args.operationId, input: args.input });
      const result =
        options.answer === undefined
          ? { [DUPLICATE_QUESTION]: 0.95 }
          : options.answer(asks.length, args.input);
      if (result === null) {
        return {
          type: "service_result",
          requestId: `fixture-${String(asks.length)}`,
          ok: false,
          refusal: "service_upstream_refused",
        };
      }
      return {
        type: "service_result",
        requestId: `fixture-${String(asks.length)}`,
        ok: true,
        result,
      };
    },
  };
  return { services, asks };
}

async function run(
  rows: Candidate[],
  options: HostOptions = {},
  input: Partial<DuplicateSweepInput> = {},
) {
  const doors = baseline(rows);
  const live = host(options);
  const report = DuplicateSweepReportSchema.parse(
    await duplicates(
      { actions: doors.actions, services: live.services, answers: new JevAnswers() },
      { members: membersOf(rows), cut: 0.8, judgements: 64, ...input },
    ),
  );
  return { report, doors, live };
}

test("a free plan states an upper bound, then judgement returns one exact draft per cluster", async () => {
  const rows = [candidate(3), candidate(1), candidate(2)];
  const doors = baseline(rows);
  const live = host();
  const planHandler = DUPLICATE_HANDLERS[JEV_ACTIONS.duplicatesPlan]!;
  const ctx = { actions: doors.actions, services: live.services } as unknown as Parameters<
    typeof planHandler
  >[0];
  const plan = await planHandler(ctx, {} as never);
  expect(plan).toMatchObject({ plan: { maxPairs: 3, newSuggestionsUpperBound: 1 }, silent: "" });
  expect(live.asks).toEqual([]);

  const report = DuplicateSweepReportSchema.parse(
    await duplicates(
      { actions: doors.actions, services: live.services, answers: new JevAnswers() },
      { members: membersOf(rows), cut: 0.8, judgements: 64 },
    ),
  );
  expect(report).toMatchObject({ candidates: 3, attempted: 3, judged: 3, truncated: false });
  expect(report.suggestions).toHaveLength(1);
  const intent = report.suggestions[0]!.intent!;
  expect(intent.representative).toBe(rows[1]!.recordId);
  expect(intent.members.map((row) => row.recordId)).toEqual([
    rows[1]!.recordId,
    rows[2]!.recordId,
    rows[0]!.recordId,
  ]);
  expect(intent.pairs.map(({ a, b }) => [a, b])).toEqual([
    [rows[1]!.recordId, rows[2]!.recordId],
    [rows[1]!.recordId, rows[0]!.recordId],
    [rows[2]!.recordId, rows[0]!.recordId],
  ]);
  expect(intent.audit).toEqual({
    records: 3,
    distinctRuns: 1,
    distinctSources: 1,
    missingRuns: 0,
    missingSources: 0,
  });
  expect(intent.pairs.every((pair) => pair.evidence.includes("0.95"))).toBe(true);
  expect(live.asks).toHaveLength(3);
  expect(live.asks[0]).toEqual({
    operationId: JEV_SERVICE.operations.duplicate,
    input: {
      [JEV_SERVICE.pairFields.a]: rows[1]!.claim,
      [JEV_SERVICE.pairFields.b]: rows[2]!.claim,
    },
  });
  expect(doors.reads.slice(1).every((read) => read.ids.length === 3)).toBe(true);
});

test("separate connected clusters produce separate drafts, never one suggestion per pair", async () => {
  const rows = [candidate(1), candidate(2), candidate(3), candidate(4)];
  const { report } = await run(rows, {
    answer: (n) => ({ [DUPLICATE_QUESTION]: n === 1 || n === 6 ? 0.9 : 0.1 }),
  });
  expect(
    report.suggestions.map((row) => row.intent!.members.map((member) => member.recordId)),
  ).toEqual([
    [rows[0]!.recordId, rows[1]!.recordId],
    [rows[2]!.recordId, rows[3]!.recordId],
  ]);
});

test("positive chains with a nonduplicate internal pair or an unknown pair are withheld", async () => {
  const rows = [candidate(1), candidate(2), candidate(3)];
  const conflict = await run(rows, {
    answer: (n) => ({ [DUPLICATE_QUESTION]: n === 2 ? 0.1 : 0.95 }),
  });
  expect(conflict.report.judged).toBe(3);
  expect(conflict.report.suggestions).toEqual([]);

  const incomplete = await run(rows, {}, { judgements: 2 });
  expect(incomplete.report).toMatchObject({
    attempted: 2,
    judged: 2,
    truncated: true,
    suggestions: [],
  });
  expect(incomplete.live.asks).toHaveLength(2);
});

test("authoritative provenance counts distinct runs and sources, not repeated records", async () => {
  const rows = [
    candidate(1, { sourceIds: ["source_b", "source_a", "source_a"] }),
    candidate(2, { sourceIds: ["source_a"] }),
    candidate(3, { runId: "run_other", sourceIds: ["source_b"] }),
    candidate(4, { runId: "", sourceIds: [] }),
  ];
  const { report } = await run(rows);
  expect(report.suggestions[0]!.intent!.audit).toEqual({
    records: 4,
    distinctRuns: 2,
    distinctSources: 2,
    missingRuns: 1,
    missingSources: 1,
  });
  expect(report.suggestions[0]!.intent!.members[0]!.sourceIds).toEqual(["source_a", "source_b"]);
});

test("partly unresolved provenance stays missing and caller counts cannot clear it", async () => {
  const rows = [
    candidate(1, { sourceIds: ["source_known"], unresolvedSources: 2 }),
    candidate(2, { sourceIds: ["source_known"] }),
  ];
  const { report } = await run(
    rows,
    {
      answer: () => ({
        [DUPLICATE_QUESTION]: 0.95,
        unresolvedSources: 0,
        audit: { missingSources: 0, distinctSources: 3 },
      }),
    },
    { members: membersOf(rows).map((member) => ({ ...member, unresolvedSources: 0 })) },
  );
  expect(report.suggestions[0]!.intent!.members[0]!.unresolvedSources).toBe(2);
  expect(report.suggestions[0]!.intent!.audit).toEqual({
    records: 2,
    distinctRuns: 1,
    distinctSources: 1,
    missingRuns: 0,
    missingSources: 1,
  });
});

test("caller and model fields cannot choose members, revisions, representative or provenance", async () => {
  const rows = [candidate(1), candidate(2)];
  const forged = membersOf(rows).map((member) => ({
    ...member,
    kind: "proposal" as const,
    runId: `forged-${member.recordId}`,
    sourceIds: ["forged-source"],
    unresolvedSources: 99,
  }));
  const { report } = await run(
    rows,
    {
      answer: () => ({
        [DUPLICATE_QUESTION]: 0.95,
        representative: "fnd_deadbeef",
        members: [{ recordId: "fnd_deadbeef", revision: 999, runId: "model-run" }],
        audit: { records: 999, distinctRuns: 999 },
        links: [{ fromId: "fnd_deadbeef", toId: rows[0]!.recordId, kind: "supersedes" }],
      }),
    },
    { members: forged },
  );
  expect(report.suggestions[0]).toMatchObject({
    recordId: rows[0]!.recordId,
    revision: rows[0]!.revision,
    subject: "",
    aspect: "duplicate-cluster",
    intent: {
      representative: rows[0]!.recordId,
      members: membersOf(rows),
      audit: { records: 2, distinctRuns: 1, distinctSources: 1 },
    },
  });
});

test("repeated record ids spend once, but contradictory revision assertions spend nothing", async () => {
  const rows = [candidate(1), candidate(2)];
  const members = membersOf(rows);
  const repeated = await run(rows, {}, { members: [members[0]!, members[0]!, members[1]!] });
  expect(repeated.report).toMatchObject({ candidates: 1, attempted: 1, judged: 1 });
  expect(repeated.report.suggestions[0]!.intent!.audit.records).toBe(2);
  const conflicting = await run(
    rows,
    {},
    { members: [members[0]!, { ...members[0]!, revision: 99 }, members[1]!] },
  );
  expect(conflicting.live.asks).toEqual([]);
  expect(conflicting.report.suggestions).toEqual([]);
});

test("unavailable or disabled services read nothing, and no credit returns no drafts", async () => {
  const rows = [candidate(1), candidate(2)];
  for (const roster of [[], [{ ...READY, state: "stopped" as const }]]) {
    const live = host({ roster });
    const doors = baseline(rows);
    const deps = { actions: doors.actions, services: live.services, answers: new JevAnswers() };
    expect(await duplicatesPlan(deps, { after: "", ids: [], limit: 24 })).toMatchObject({
      plan: null,
    });
    const report = await duplicates(deps, { members: membersOf(rows), cut: 0.8, judgements: 64 });
    expect(report.suggestions).toEqual([]);
    expect(doors.reads).toEqual([]);
    expect(live.asks).toEqual([]);
  }
  const dry = await run(rows, { answer: () => null });
  expect(dry.report).toMatchObject({ attempted: 1, judged: 0, suggestions: [] });
  expect(dry.live.asks).toHaveLength(1);

  const stopped = await run([candidate(1), candidate(2), candidate(3)], {
    answer: (n) => (n === 2 ? null : { [DUPLICATE_QUESTION]: 0.95 }),
  });
  expect(stopped.report).toMatchObject({ attempted: 2, judged: 1, suggestions: [] });
  expect(stopped.live.asks).toHaveLength(2);
});

test("a changed fingerprint or ineligible record is reread before any paid call", async () => {
  const rows = [candidate(1), candidate(2), candidate(3)];
  for (const changed of [
    [candidate(1, { fingerprint: "f".repeat(64) }), rows[1]!, rows[2]!],
    rows.slice(0, 2),
  ]) {
    const live = host();
    const doors = baseline(rows, (read) => (read >= 2 ? changed : rows));
    const report = await duplicates(
      { actions: doors.actions, services: live.services, answers: new JevAnswers() },
      { members: membersOf(rows), cut: 0.8, judgements: 64 },
    );
    expect(live.asks).toEqual([]);
    expect(report.suggestions).toEqual([]);
  }
});

test("changes after a paid answer discard drafts and prevent the next paid call", async () => {
  const rows = [candidate(1), candidate(2), candidate(3)];
  const live = host();
  const doors = baseline(rows, (read) =>
    read >= 3 ? [candidate(1, { revision: 9 }), ...rows.slice(1)] : rows,
  );
  const report = await duplicates(
    { actions: doors.actions, services: live.services, answers: new JevAnswers() },
    { members: membersOf(rows), cut: 0.8, judgements: 64 },
  );
  expect(live.asks).toHaveLength(1);
  expect(report).toMatchObject({ attempted: 1, judged: 1, suggestions: [] });
});

test("the explicit cut, record kinds, call ceiling and per-call cap bound judgement", async () => {
  const rows = [candidate(1), candidate(2)];
  const below = await run(rows, { answer: () => ({ [DUPLICATE_QUESTION]: 0.799 }) });
  expect(below.report.suggestions).toEqual([]);
  const boundary = await run(rows, { answer: () => ({ [DUPLICATE_QUESTION]: 0.8 }) });
  expect(boundary.report.suggestions).toHaveLength(1);
  expect(DuplicateSweepInputSchema.safeParse({ members: membersOf(rows) }).success).toBe(false);
  expect(
    DuplicateSweepInputSchema.safeParse({
      members: membersOf(rows),
      cut: 0.8,
      judgements: 65,
    }).success,
  ).toBe(false);

  const mixed = await run([
    rows[0]!,
    candidate(2, { recordId: "hyp_00000002", kind: "hypothesis" }),
  ]);
  expect(mixed.report).toMatchObject({ candidates: 0, attempted: 0, suggestions: [] });
  expect(mixed.live.asks).toEqual([]);
  const bounded = await run(Array.from({ length: 12 }, (_, index) => candidate(index + 1)));
  expect(bounded.live.asks).toHaveLength(DUPLICATE_JUDGEMENTS);
  expect(bounded.report).toMatchObject({
    candidates: 66,
    attempted: 64,
    judged: 64,
    truncated: true,
    suggestions: [],
  });
  const oversized = await run([rows[0]!, candidate(2, { claim: "x".repeat(JEV_CALL_CAP_BYTES) })]);
  expect(oversized.live.asks).toEqual([]);
  expect(oversized.report).toMatchObject({ attempted: 1, judged: 0, suggestions: [] });
});

test("invalid model degrees are silence rather than positive matches", async () => {
  for (const value of [-1, 2, "0.95", { score: 0.95 }, null]) {
    const { report } = await run([candidate(1), candidate(2)], {
      answer: () => ({ [DUPLICATE_QUESTION]: value }),
    });
    expect(report).toMatchObject({ attempted: 1, judged: 0, suggestions: [] });
  }
});

test("repeat judgement reuses only paid answers under the same policy and immutable texts", async () => {
  const rows = [candidate(1), candidate(2)];
  const live = host();
  const doors = baseline(rows);
  const deps = { actions: doors.actions, services: live.services, answers: new JevAnswers() };
  const input = { members: membersOf(rows), cut: 0.8, judgements: 64 };
  const first = await duplicates(deps, input);
  const repeated = await duplicates(deps, { ...input, members: [...input.members].reverse() });
  expect(repeated.suggestions).toEqual(first.suggestions);
  expect(live.asks).toHaveLength(1);
});

test("the final reread refuses a cluster changed while its last judgement was outstanding", async () => {
  const rows = [candidate(1), candidate(2)];
  const live = host();
  const doors = baseline(rows, (read) =>
    read >= 3 ? [candidate(1, { fingerprint: "f".repeat(64) }), rows[1]!] : rows,
  );
  const report = await duplicates(
    { actions: doors.actions, services: live.services, answers: new JevAnswers() },
    { members: membersOf(rows), cut: 0.8, judgements: 64 },
  );
  expect(live.asks).toHaveLength(1);
  expect(report).toMatchObject({ attempted: 1, judged: 1, suggestions: [] });
});

test("a policy changed after planning cannot buy answers attributed to the former revision", async () => {
  const rows = [candidate(1), candidate(2)];
  const doors = baseline(rows);
  const live = host();
  let rosters = 0;
  const services: JevServices = {
    ...live.services,
    listInstances: async () => {
      rosters += 1;
      return {
        defaultOwner: null,
        services: [
          rosters === 1
            ? READY
            : { ...READY, configuration: { ...READY.configuration!, revision: "duplicates-r2" } },
        ],
      };
    },
  };
  const report = await duplicates(
    { actions: doors.actions, services, answers: new JevAnswers() },
    { members: membersOf(rows), cut: 0.8, judgements: 64 },
  );
  expect(live.asks).toEqual([]);
  expect(report).toMatchObject({ attempted: 1, judged: 0, suggestions: [] });
});
