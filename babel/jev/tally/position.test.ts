import { expect, test } from "bun:test";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import { votesFor } from "../bank/bank.ts";
import type { Condition, Vote, Voter } from "../bank/schema.ts";
import { JEV_SERVICE, type JevServices } from "../server/credential.ts";
import { JevAnswers } from "../server/judge.ts";
import type { ScreenedRecord } from "../screen/screener.ts";
import { positionFor, positionOf } from "./position.ts";

/*
  WHAT A POSITION HAS TO KEEP APART, AND WHAT IT MUST NEVER INVENT.

  Each case here is a way a tally can be wrong that a number on its own cannot show:

  - AGREEMENT AND DISAGREEMENT ARE NOT THE SAME +1. Two voters backing a record one voter objects
    to sums to the same number as one voter backing it alone, and #406 measured what happens when
    a sort reads that number: the records the panel argued about land exactly where the records
    nobody found interesting sit. So `standing` is the answer and the sum is a detail.
  - MISSING DATA HAS NO NUMBER. Nobody judged it, and every voter judged it and shrugged, are the
    two states a net zero cannot tell apart. One is `unjudged` with no tally at all; the other is
    a real zero.
  - AN ANSWER OF THE WRONG SHAPE IS NOT AN ABSTENTION. A magnitude cut handed the word "high" and
    a named-choice cut handed the number 3 are both a reply the voter could not read — and the
    second would otherwise CAST, because `is-not "none"` is satisfied by any number.
  - ABSENT AND NEVER INSTALLED ARE ONE ANSWER. A caller that cannot reach Jev derives the same
    position a caller whose Jev is unbound is handed, so there is one path and not two.
*/

const RECORD: ScreenedRecord = {
  id: "fnd_00000001",
  revision: 4,
  kind: "finding",
  title: "a finding",
  text: "the contract check asserts the strings the controller emits",
};

const HYPOTHESIS: ScreenedRecord = {
  id: "hyp_00000001",
  revision: 9,
  kind: "hypothesis",
  title: "a hypothesis",
  text: "the receipt names a model the run never asked for",
};

/**
 * One row of a synthetic panel. The observed distribution is what the bank requires of every
 * threshold and is not what these cases are about: `admitted` is `true` because a caller passing
 * its own votes has already chosen its panel, exactly as `tally()` requires of one.
 */
function vote(voter: Voter, question: string, casts: "up" | "down", when: Condition): Vote {
  return {
    voter,
    question,
    casts,
    when,
    observed: { n: 100, fires: 25, mean: 0.5, sd: 0.2 },
    admitted: true,
  };
}

/**
 * FOUR VOTERS, ONE QUESTION EACH: three that can back and one that can object. It is synthetic on
 * purpose — the shipped bank's lines are calibration and would make every case here a statement
 * about the corpus rather than about the tally.
 */
const PANEL: readonly Vote[] = [
  vote("worth-of-attention", "qa", "up", { op: "at-least", value: 1 }),
  vote("concreteness", "qb", "up", { op: "at-least", value: 1 }),
  vote("actionability", "qc", "up", { op: "at-least", value: 1 }),
  vote("editorial", "qd", "down", { op: "at-most", value: 0 }),
];

/** The study's own answers for one imported hypothesis, verbatim from its published standing. */
const MEASURED_HYPOTHESIS = {
  worth_first: 2.81,
  specific: 0.8,
  contradicts_intent: 0.95,
  actionable: 0.9,
  evidence_strength: 0.66,
  recurring: 0.84,
  speculative: 0.63,
  self_referential: 0.95,
  fused_to_fix: 0.81,
  restates_known: 0.1,
  needs_arithmetic: 0.32,
  friction_kind: "ignored_constraint",
  temporal: "current",
} as const;

const POLICY = {
  revision: "r7",
  pluginId: "atyrode.babel.jev",
  enabled: true,
  policySha256: "a".repeat(64),
};

const READY: InstanceServiceDescription = {
  serviceId: JEV_SERVICE.serviceId,
  defaultOwner: null,
  owner: { machineId: "dev-01", name: "dev-01", online: true },
  configuration: POLICY,
  connected: true,
  state: "ready",
  reason: null,
};

/** What the host's own reply type admits as an answer: the projection's leaves, as JSON. */
type Answered = Extract<ServiceReply, { ok: true }>["result"];

/** The host, bound or not, counting what it was asked so a case can prove nothing was spent. */
function host(options: { readonly bound?: boolean; readonly result?: Answered }): {
  readonly services: JevServices;
  readonly asks: string[];
} {
  const asks: string[] = [];
  return {
    asks,
    services: {
      listInstances: async () => ({
        defaultOwner: null,
        services: options.bound === false ? [] : [READY],
      }),
      invokeInstance: async (args) => {
        asks.push(String(args.input[JEV_SERVICE.stateField]));
        return {
          type: "service_result",
          requestId: `q${String(asks.length)}`,
          ok: true,
          result: options.result ?? {},
        };
      },
    },
  };
}

test("three voters agreeing, two against one, and a record nobody judged are three answers", () => {
  const agreed = positionOf(RECORD, { qa: 1, qb: 1, qc: 1, qd: 1 }, { votes: PANEL });
  expect(agreed.standing).toBe("backed");
  expect(agreed.backed).toEqual(["worth-of-attention", "concreteness", "actionability"]);
  expect({ up: agreed.up, down: agreed.down, tally: agreed.tally }).toEqual({
    up: 3,
    down: 0,
    tally: 3,
  });

  // TWO BACKED IT AND ONE OBJECTED, and the objection is named rather than netted away.
  const split = positionOf(RECORD, { qa: 1, qb: 1, qc: 0, qd: 0 }, { votes: PANEL });
  expect(split.standing).toBe("contested");
  expect(split.objected).toEqual(["editorial"]);
  expect({ up: split.up, down: split.down, tally: split.tally }).toEqual({
    up: 2,
    down: 1,
    tally: 1,
  });

  // NOBODY JUDGED IT: no sum exists, and the roster still says how big the panel it was not
  // shown to is.
  const absent = positionOf(RECORD, null, { votes: PANEL });
  expect(absent.standing).toBe("unjudged");
  expect(absent.tally).toBeNull();
  expect({ roster: absent.roster, heard: absent.heard, silent: absent.silent }).toEqual({
    roster: 4,
    heard: 0,
    silent: [],
  });

  // AND THE SUM ALONE CANNOT TELL THE SPLIT FROM A LONE BACKER (#406): both are +1, and a sort
  // over the number would rank the contested record as the quieter one. The word does not.
  const lone = positionOf(RECORD, { qa: 1, qb: 0, qc: 0, qd: 1 }, { votes: PANEL });
  expect([lone.tally, split.tally]).toEqual([1, 1]);
  expect([lone.standing, split.standing]).toEqual(["backed", "contested"]);
  expect([lone.objected, split.objected]).toEqual([[], ["editorial"]]);
});

test("a judgement that answered nothing is not a shrug, and a shrug is a real zero", () => {
  // JUDGED, AND NOT ONE OF THE PANEL'S QUESTIONS CAME BACK. A projection that answered something
  // else entirely is missing data: every voter is silent, and there is no sum to report.
  const nothing = positionOf(RECORD, { vague: 1 }, { votes: PANEL });
  expect(nothing.standing).toBe("unheard");
  expect(nothing.tally).toBeNull();
  expect(nothing.heard).toBe(0);
  expect(nothing.silent).toEqual([
    "worth-of-attention",
    "concreteness",
    "actionability",
    "editorial",
  ]);

  // JUDGED, EVERY VOTER HEARD, AND NONE OF THEM REACHED ITS LINE. That is a measurement, so it
  // has a number — and it is the one case where a zero is the truth.
  const shrug = positionOf(RECORD, { qa: 0, qb: 0, qc: 0, qd: 1 }, { votes: PANEL });
  expect(shrug.standing).toBe("unremarked");
  expect(shrug.tally).toBe(0);
  expect({ heard: shrug.heard, silent: shrug.silent }).toEqual({ heard: 4, silent: [] });

  // The two are indistinguishable by up and down alone, which is why the word and the nullable
  // sum both exist.
  expect([nothing.up, nothing.down]).toEqual([shrug.up, shrug.down]);
});

test("a voter the reply left nothing readable for is silent, and a silent voter cannot vote", () => {
  // A MAGNITUDE CUT HANDED A WORD. The voter was asked and could not answer; the three that
  // could are the whole of what the position claims, and `roster` says so.
  const worded = positionOf(RECORD, { qa: "high", qb: 1, qc: 1, qd: 1 }, { votes: PANEL });
  expect(worded.silent).toEqual(["worth-of-attention"]);
  expect({ roster: worded.roster, heard: worded.heard, up: worded.up }).toEqual({
    roster: 4,
    heard: 3,
    up: 2,
  });

  /*
    AND THE ONE THAT WOULD OTHERWISE CAST. `is-not` is satisfied by anything that is not the
    named option, and the number 3 is not the string "none" — so a tally taken over every row
    rather than over the READABLE rows would record an up-vote from a voter handed an answer to
    a question it does not ask. That is a vote invented out of a malformed reply.
  */
  const choice: readonly Vote[] = [
    vote("friction-lens", "friction_kind", "up", { op: "is-not", value: "none" }),
  ];
  const numbered = positionOf(RECORD, { friction_kind: 3 }, { votes: choice });
  expect(numbered.standing).toBe("unheard");
  expect({ up: numbered.up, tally: numbered.tally, silent: numbered.silent }).toEqual({
    up: 0,
    tally: null,
    silent: ["friction-lens"],
  });
  // The answer the question does ask for still votes, so the guard is a shape check and not a
  // second threshold.
  const named = positionOf(RECORD, { friction_kind: "ignored_constraint" }, { votes: choice });
  expect({ standing: named.standing, up: named.up, tally: named.tally }).toEqual({
    standing: "backed",
    up: 1,
    tally: 1,
  });
});

test("the shipped panel is counted in voters and not in rows, and says who objected", () => {
  // The study published this hypothesis at seven up and two down, and the admitted panel
  // reproduces it (`bank.test.ts` holds the same row against the whole block). What #355 adds is
  // the rest of the sentence: it is CONTESTED, and these two are who objected.
  const position = positionOf(HYPOTHESIS, MEASURED_HYPOTHESIS);
  expect({ up: position.up, down: position.down, standing: position.standing }).toEqual({
    up: 7,
    down: 2,
    standing: "contested",
  });
  expect(position.objected).toEqual(["scope", "editorial"]);
  expect(position.tally).toBe(5);
  // A two-sided voter writes a row per side, so the bank holds more rows than it has voters. The
  // denominator is the panel: a position reading "seven of seventeen" would describe a panel that
  // does not exist, and `heard + silent === roster` is the invariant that keeps it honest.
  expect(position.roster).toBeLessThan(votesFor("hypothesis").length);
  expect(position.heard + position.silent.length).toBe(position.roster);
  expect(position.heard).toBe(position.roster);
  // The position is about the wording that was judged, never about the record in general.
  expect({ recordId: position.recordId, revision: position.revision }).toEqual({
    recordId: "hyp_00000001",
    revision: 9,
  });
});

test("a position with Jev absent is the same answer as a position with no Jev installed", async () => {
  const off = host({ bound: false });
  const unbound = await positionFor(off.services, HYPOTHESIS, { answers: new JevAnswers() });
  // Nothing was spent, and the answer is the one a caller with no part at all derives for itself:
  // one value, one code path, and no consumer branching on whether Jev exists.
  expect(off.asks).toHaveLength(0);
  expect(unbound).toEqual(positionOf(HYPOTHESIS, null));
  expect(unbound.standing).toBe("unjudged");
  expect(unbound.tally).toBeNull();
});

test("the read path surfaces the panel's position from one judgement of one record", async () => {
  const live = host({ result: MEASURED_HYPOTHESIS });
  const held = new JevAnswers();
  const position = await positionFor(live.services, HYPOTHESIS, { answers: held });
  expect(position.standing).toBe("contested");
  expect(position.objected).toEqual(["scope", "editorial"]);
  expect({ up: position.up, down: position.down }).toEqual({ up: 7, down: 2 });
  // The record's own words are what was sent, and asking again about the same wording under the
  // same bank costs nothing: a caller may ask for a position wherever it reads a record.
  expect(live.asks).toEqual([HYPOTHESIS.text]);
  const again = await positionFor(live.services, HYPOTHESIS, { answers: held });
  expect(live.asks).toHaveLength(1);
  expect(again).toEqual(position);
});
