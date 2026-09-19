import { expect, test } from "bun:test";
import type { GuestActions } from "@manifold/plugin-kit/server";
import type { InstanceServiceDescription, ServiceReply } from "@manifold/protocol";
import { ACTIONS, BABEL_PLUGIN_ID } from "../../contract.ts";
import { votesFor } from "../bank/bank.ts";
import { tally, type TallyResult } from "../bank/schema.ts";
import { JEV_CALL_CAP_BYTES, JEV_SERVICE, type JevServices } from "../server/credential.ts";
import { JevAnswers } from "../server/judge.ts";
import type { Standing } from "../tally/position.ts";
import { screenPass, screenRecord, sweepSize, type PassSuggestion } from "./pass.ts";
import type { ScreenedRecord, Screener, ScreenSubject, ScreenSuggestion } from "./screener.ts";

/*
  WHAT THE INTAKE SCREEN HAS TO DO, AND WHAT IT MUST NOT.

  Each case is a way the pass can be wrong that nothing downstream would notice:

  - JEV OFF IS TODAY'S BEHAVIOUR. With no service binding every record comes back unjudged, no
    voter is consulted and nothing is proposed. It is the acceptance criterion of #360 and the
    standing requirement of every Jev layer, and a pass that reported those records as "judged,
    nothing found" would be saying the corpus was checked when it was not.
  - A BROKEN VOTER IS VISIBLE. One that throws is counted `failed` by name, the record it threw
    on is named, and the pass carries on — with the other voters on that record and with the
    records after it. A guard that swallowed the throw would make a voter broken on everything
    look exactly like a voter with nothing to say.
  - ONE RECORD IS ONE BILL. The judgement is paid for once for the whole document, so adding a
    voter costs nothing. A voter that called out on its own would be a second bill for one state.
  - THE NUMBER A VOTER READS IS THE NUMBER THAT CAME BACK. Nothing rounds, clamps or re-scales
    between the reply and the voter, and a leaf that is not a string or a finite number is absent
    rather than present and wrong.
  - THE WRITE IS THE SUGGEST DOOR AND SAYS NOTHING ABOUT ITS AUTHOR. The suggester is resolved
    from the principal by the door; a field here naming one would be refused unread.
*/

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

/**
 * The host, with the binding the operator either installed or did not and the answer it either
 * gave or refused, counting invocations. `refused` is the shape of out-of-credit and of every
 * other reply the part cannot read: the service answered, and the answer is not a judgement.
 */
function host(options: {
  readonly bound?: boolean;
  readonly refused?: boolean;
  readonly result?: Answered;
}): {
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
        if (options.refused === true) {
          return {
            type: "service_result",
            requestId: `q${String(asks.length)}`,
            ok: false,
            refusal: "service_ceiling_exceeded",
          };
        }
        return {
          type: "service_result",
          requestId: `q${String(asks.length)}`,
          ok: true,
          result: options.result ?? { vague: 1 },
        };
      },
    },
  };
}

function record(id: string, text: string): ScreenedRecord {
  return { id, revision: 3, kind: "finding", title: "a finding", text };
}

/** A voter that says the same thing about everything, so the pass is what is under test. */
function always(id: string, seen?: ScreenSubject[]): Screener {
  return {
    id,
    kinds: ["finding"],
    screen: (subject) => {
      seen?.push(subject);
      return { kind: "develop-further", summary: `${id} spoke`, rationale: "because" };
    },
  };
}

const BROKEN: Screener = {
  id: "broken",
  kinds: ["finding"],
  screen: () => {
    throw new Error("read the wrong leaf");
  },
};

const SILENT: Screener = { id: "silent", kinds: ["finding"], screen: () => null };

/**
 * Every standing at zero, to be spread over with the ones a case expects. A report names all six
 * words always, so a case that listed only the interesting one would pass while the pass counted
 * the other five wrongly.
 */
const NO_STANDING: Record<Standing, number> = {
  unjudged: 0,
  unheard: 0,
  unremarked: 0,
  backed: 0,
  objected: 0,
  contested: 0,
};

/** The suggestions a pass produced, in the order it produced them. */
function collector(): {
  readonly written: PassSuggestion[];
  readonly deliver: (suggestion: PassSuggestion) => Promise<void>;
} {
  const written: PassSuggestion[] = [];
  return {
    written,
    deliver: async (suggestion) => {
      written.push(suggestion);
      await Promise.resolve();
    },
  };
}

test("with the part unbound every record is unjudged, no voter runs and nothing is proposed", async () => {
  const seen: ScreenSubject[] = [];
  const off = host({ bound: false });
  const sink = collector();
  const report = await screenPass(
    off.services,
    [record("fnd_00000001", "a claim"), record("fnd_00000002", "another claim")],
    sink.deliver,
    { screeners: [always("chatty", seen)], answers: new JevAnswers() },
  );
  expect(off.asks).toHaveLength(0);
  expect(seen).toHaveLength(0);
  expect(sink.written).toHaveLength(0);
  // NOT JUDGED YET, never judged and found wanting. The two numbers say different things and a
  // pass that reported these as judged would be claiming a check it never paid for — and no
  // record has a standing, because a standing is what a panel that was shown a judgement holds.
  expect(report).toEqual({
    read: 2,
    judged: 0,
    unjudged: 2,
    suggested: 0,
    failed: [],
    standings: { ...NO_STANDING, unjudged: 2 },
  });
});

test("a voter that throws is reported by name and the pass finishes the sweep", async () => {
  const live = host({});
  const sink = collector();
  const report = await screenPass(
    live.services,
    [record("fnd_00000001", "first"), record("fnd_00000002", "second")],
    sink.deliver,
    { screeners: [BROKEN, always("steady")], answers: new JevAnswers() },
  );
  // Both records were judged and both were screened: the throw ended neither the record nor
  // the sweep, and the voter after the broken one still spoke on each.
  expect(report.judged).toBe(2);
  expect(report.suggested).toBe(2);
  expect(sink.written.map((suggestion) => suggestion.recordId)).toEqual([
    "fnd_00000001",
    "fnd_00000002",
  ]);
  // And the failure is attributable: which voter, which record, and what it said.
  expect(report.failed).toEqual([
    { screener: "broken", recordId: "fnd_00000001", reason: "read the wrong leaf" },
    { screener: "broken", recordId: "fnd_00000002", reason: "read the wrong leaf" },
  ]);
});

test("a silent voter and a broken one are not the same report", async () => {
  const live = host({});
  const sink = collector();
  const report = await screenPass(live.services, [record("fnd_00000001", "first")], sink.deliver, {
    screeners: [SILENT],
    answers: new JevAnswers(),
  });
  // The projection answered `vague` alone, which no voter for a finding thresholds, so the
  // record was judged and the panel was left with nothing readable: UNHEARD, and not a shrug.
  expect(report).toEqual({
    read: 1,
    judged: 1,
    unjudged: 0,
    suggested: 0,
    failed: [],
    standings: { ...NO_STANDING, unheard: 1 },
  });
});

test("one record is one call however many voters are asked", async () => {
  const live = host({});
  const sink = collector();
  const report = await screenPass(
    live.services,
    [record("fnd_00000001", "first"), record("fnd_00000002", "second")],
    sink.deliver,
    {
      screeners: [always("a"), always("b"), always("c")],
      answers: new JevAnswers(),
    },
  );
  // Three voters, two records, two invocations: the cost is the state and not the question.
  expect(live.asks).toEqual(["first", "second"]);
  expect(report.suggested).toBe(6);
  // Every suggestion carries the record and the revision the LOOP read, never the voter's.
  expect(new Set(sink.written.map((suggestion) => suggestion.revision))).toEqual(new Set([3]));
  expect(sink.written.map((suggestion) => suggestion.screener)).toEqual([
    "a",
    "b",
    "c",
    "a",
    "b",
    "c",
  ]);
});

test("the answer a voter reads is the answer that came back, unrounded and unrescaled", async () => {
  const seen: ScreenSubject[] = [];
  const live = host({
    result: { vague: 0.5, overclaims: 3, note: "hedged", deep: { nested: 1 }, bad: Number.NaN },
  });
  const sink = collector();
  await screenPass(live.services, [record("fnd_00000001", "first")], sink.deliver, {
    screeners: [always("reader", seen)],
    answers: new JevAnswers(),
  });
  // 0.5 arrives as 0.5. A coercion that guessed which scale an answer was on is how a `<= 1`
  // cut comes to fire on an entire corpus. And a leaf that is neither a string nor a finite
  // number is ABSENT, which is what lets a voter read `undefined` as "unanswered" and be right.
  expect(seen[0]?.answers).toEqual({ vague: 0.5, overclaims: 3, note: "hedged" });
});

test("a score reaches the tally unrounded, end to end from the host's own reply", async () => {
  /*
    THE FULL PATH, not the coercion alone: the host's reply, through `answersOf`, into the same
    `answers` the voters read and `tally()` sums, and on into the STANDING the pass reports.
    `concreteness` backs a finding at `>= 0.7` and objects at `<= 0.3`, so four points one
    hundredth apart pin every way the number could be quietly changed — truncation drops 0.7 to 0
    and loses the backing, rounding lifts 0.69 to 1 and invents one, and any rescale moves at
    least one of the four across its line. Each of those four crossings moves a WORD an operator
    reads, which is what makes this the whole path rather than the arithmetic alone.
  */
  const seen: ScreenSubject[] = [];
  const asked: Record<string, TallyResult> = {};
  const stood: string[] = [];
  for (const specific of [0.7, 0.69, 0.3, 0.31]) {
    const live = host({ result: { specific } });
    const sink = collector();
    const report = await screenPass(
      live.services,
      [record("fnd_00000001", "first")],
      sink.deliver,
      {
        screeners: [always(`at-${String(specific)}`, seen)],
        answers: new JevAnswers(),
      },
    );
    const answers = seen[seen.length - 1]?.answers ?? {};
    asked[String(specific)] = tally(votesFor("finding"), answers);
    stood.push(
      Object.entries(report.standings)
        .filter(([, count]) => count > 0)
        .map(([standing]) => standing)
        .join(),
    );
  }
  expect(seen.map((subject) => subject.answers.specific)).toEqual([0.7, 0.69, 0.3, 0.31]);
  expect(asked["0.7"]?.backed).toEqual(["concreteness"]);
  expect(asked["0.69"]?.backed).toEqual([]);
  expect(asked["0.69"]?.objected).toEqual([]);
  expect(asked["0.3"]?.objected).toEqual(["concreteness"]);
  expect(asked["0.31"]?.objected).toEqual([]);
  // And the same four numbers reach the pass's own report as four standings, two of which are
  // the record having been heard and not remarked on — a real zero, and not the `unjudged` a
  // record nobody judged gets.
  expect(stood).toEqual(["backed", "unremarked", "objected", "unremarked"]);
});

test("a voter that failed is never counted as agreement", () => {
  /*
    A CRASH IS NOT A VOTE, and the way that would be lost is a position built from "every voter
    that did not object". The bank's own rows are unmoved by a throw — a voter that throws
    produces no row and there is nothing for the sum to read — so the backing is identical with
    the broken voter on the roster and without it, and the hole is reported beside it rather than
    folded into it. An operator reading `backed` can see the roster was not whole.
  */
  const backing = { specific: 0.7 };
  const whole = screenRecord(record("fnd_00000001", "first"), backing, [always("steady")]);
  const holed = screenRecord(record("fnd_00000001", "first"), backing, [BROKEN, always("steady")]);
  expect(holed.position.backed).toEqual(["concreteness"]);
  expect(holed.position.backed).toEqual(whole.position.backed);
  expect(holed.position.up).toBe(whole.position.up);
  expect(holed.position.failed).toEqual(["broken"]);
  expect(whole.position.failed).toEqual([]);
  // The record is still backed by the panel, and the position names the wording that was judged.
  expect(holed.position.standing).toBe("backed");
  expect(holed.position.revision).toBe(3);
});

test("the record comes out of the pass exactly as it went in, judged or not", async () => {
  /*
    STRICTLY OPTIONAL IS TWO CLAIMS AND THIS IS THE SECOND. The first is that no voter runs when
    Jev is absent; this is that screening never touches the record either way, so a record
    screened with Jev absent is indistinguishable from one nothing screened at all. The pass
    holds the only reference there is, so if it annotated, normalised or re-wrote a field this
    is where it would show.
  */
  const subject = record("fnd_00000001", "a claim");
  const before = structuredClone(subject);
  const sink = collector();
  // Judged, and by a voter that has something to say about it.
  const judged = await screenPass(host({}).services, [subject], sink.deliver, {
    screeners: [always("chatty")],
    answers: new JevAnswers(),
  });
  expect(subject).toEqual(before);
  expect(judged.suggested).toBe(1);

  // And the three absences, which are the whole of "Jev off": no binding at all, a service that
  // refused, and a record too large to send. Each is one `null`, each leaves the record alone,
  // and each produces the SAME report — so no caller can tell which absence it met, because
  // none of them is a judgement.
  const reports = [
    await screenPass(host({ bound: false }).services, [subject], sink.deliver, {
      screeners: [always("chatty")],
      answers: new JevAnswers(),
    }),
    await screenPass(host({ refused: true }).services, [subject], sink.deliver, {
      screeners: [always("chatty")],
      answers: new JevAnswers(),
    }),
    await screenPass(
      host({}).services,
      [record("fnd_00000001", "x".repeat(JEV_CALL_CAP_BYTES))],
      sink.deliver,
      { screeners: [always("chatty")], answers: new JevAnswers() },
    ),
  ];
  const off = {
    read: 1,
    judged: 0,
    unjudged: 1,
    suggested: 0,
    failed: [],
    standings: { ...NO_STANDING, unjudged: 1 },
  };
  expect(reports).toEqual([off, off, off]);
  expect(subject).toEqual(before);
  // Nothing was delivered by any of the three, so the sink holds only the judged pass's one.
  expect(sink.written).toHaveLength(1);
});

test("the only call this part makes into Babel is the one that sizes a sweep", async () => {
  const calls: { plugin: string; action: string; input: unknown }[] = [];
  const actions: GuestActions = {
    call: async (args) => {
      calls.push(args);
      return await Promise.resolve({
        suggester: "atyrode.babel.jev",
        outstanding: 4,
        answered: 1,
        judged: 900,
        unjudged: 5138,
      });
    },
  };
  // What one more sweep would add, stated before it adds anything — and nothing in this bundle
  // adds anything: `babel.suggest` declares `containers:write`, which is outside the part's own
  // ceiling, so the pass hands its suggestions to a caller-supplied function instead.
  expect(await sweepSize(actions)).toEqual({ outstanding: 4, judged: 900, unjudged: 5138 });
  expect(calls).toEqual([{ plugin: BABEL_PLUGIN_ID, action: ACTIONS.suggestions, input: {} }]);
});

test("a voter is asked only about the kinds it speaks for", async () => {
  const seen: ScreenSubject[] = [];
  const live = host({});
  const sink = collector();
  const proposals: Screener = {
    id: "proposals-only",
    kinds: ["proposal"],
    screen: (subject): ScreenSuggestion | null => {
      seen.push(subject);
      return null;
    },
  };
  const report = await screenPass(
    live.services,
    [record("fnd_00000001", "a finding")],
    sink.deliver,
    { screeners: [proposals], answers: new JevAnswers() },
  );
  expect(seen).toHaveLength(0);
  // Judged all the same: the judgement is the document's and not any one voter's.
  expect(report.judged).toBe(1);
});
