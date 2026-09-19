import { expect, test } from "bun:test";
import { evaluate, type Issue } from "./triage-policy.ts";

/*
  THE RULES, PROVEN AGAINST CONSTRUCTED ISSUES.

  `evaluate` is exported for exactly this reason, and the upstream comment says why: the live
  tracker cannot exercise every rule on demand, because nothing on it is fourteen days quiet at
  the moment you want to check T5. A rule whose boundary has never been crossed is a rule nobody
  has seen work.

  Each test therefore builds the issue the rule is about and asserts both sides of the boundary,
  including the two rules that repair themselves — because a `--fix` that repaired the wrong
  thing would be worse than no fix at all.
*/

const NOW = Date.parse("2026-09-18T12:00:00.000Z");
const DAY = 86_400_000;

function issue(over: Partial<Issue> = {}): Issue {
  return {
    number: 1,
    title: "a record rests on one run and the peel does not say so",
    url: "https://github.com/atyrode/babel/issues/1",
    body: "## Problem\n\n…\n\n## Acceptance\n\n…",
    createdAt: new Date(NOW - DAY).toISOString(),
    labels: ["agent-ready", "p2", "area:store"],
    comments: [],
    ...over,
  };
}

const rules = (found: readonly { rule: string }[]) => found.map((finding) => finding.rule).sort();

test("T1: an open issue with no state is repaired to needs-triage, and two states is a judgement", () => {
  const none = evaluate([issue({ labels: [] })], NOW);
  expect(rules(none)).toEqual(["T1"]);
  // The one rule that writes, because choosing a state is judgement but noticing its absence is
  // bookkeeping.
  expect(none[0]?.repair).toEqual({
    flag: "--add-label",
    label: "needs-triage",
    fixed: "added needs-triage",
  });

  const both = evaluate([issue({ labels: ["agent-ready", "blocked", "p2", "area:store"] })], NOW);
  expect(rules(both)).toContain("T1");
  // Two states cannot be repaired by a script: which one is wrong is the question.
  expect(both.find((finding) => finding.rule === "T1")?.repair).toBeUndefined();
});

test("T1: a tracking umbrella is exempt, because an epic is not in one state", () => {
  expect(evaluate([issue({ labels: ["tracking"] })], NOW)).toEqual([]);
});

test("T2: agent-ready means an agent knows where it lands and how urgent it is", () => {
  expect(rules(evaluate([issue({ labels: ["agent-ready", "area:store"] })], NOW))).toEqual(["T2"]);
  expect(rules(evaluate([issue({ labels: ["agent-ready", "p2"] })], NOW))).toEqual(["T2"]);
  // Both missing is two findings, not one: they are two different omissions to fix.
  expect(evaluate([issue({ labels: ["agent-ready"] })], NOW)).toHaveLength(2);
  expect(evaluate([issue()], NOW)).toEqual([]);
});

test("T2: a documentation or process issue is ready without an area, because it lands nowhere", () => {
  const docs = issue({ labels: ["agent-ready", "p3", "documentation"] });
  expect(evaluate([docs], NOW)).toEqual([]);
  expect(evaluate([issue({ labels: ["agent-ready", "p3", "process"] })], NOW)).toEqual([]);
});

test("T3: a blocker nobody named is indistinguishable from an abandoned issue", () => {
  const unnamed = issue({ labels: ["blocked"], body: "waiting on the other thing" });
  expect(rules(evaluate([unnamed], NOW))).toEqual(["T3"]);
  const named = issue({ labels: ["blocked"], body: "Blocked by #333 until the part exists." });
  expect(evaluate([named], NOW)).toEqual([]);

  // A blocker named in a comment counts: what an issue waits on is usually discovered after
  // it was filed, and rewriting the body to record it loses the date the discovery happened.
  const inComment = issue({
    labels: ["blocked"],
    body: "this needs something else first",
    comments: [
      {
        body: "Blocked by #370, which scaffolds the part.",
        createdAt: new Date(NOW).toISOString(),
        author: "atyrode",
      },
    ],
  });
  expect(evaluate([inComment], NOW)).toEqual([]);
});

test("T4: a hold without a written question can never be answered", () => {
  const silent = issue({ labels: ["needs-operator"], body: "not sure about this one" });
  expect(rules(evaluate([silent], NOW))).toEqual(["T4"]);

  const inBody = issue({
    labels: ["needs-operator"],
    body: "## Decision\nQuestion: does this still apply?\n",
  });
  expect(evaluate([inBody], NOW)).toEqual([]);

  // A block in a comment counts: a hold is usually raised after the issue was filed.
  const inComment = issue({
    labels: ["needs-operator"],
    body: "no block here",
    comments: [
      {
        body: "## Decision\nQuestion: ship it?\n",
        createdAt: new Date(NOW).toISOString(),
        author: "atyrode",
      },
    ],
  });
  expect(evaluate([inComment], NOW)).toEqual([]);
});

test("T5: silence past fourteen days is a signal, and speaking clears it", () => {
  const quiet = issue({ createdAt: new Date(NOW - 15 * DAY).toISOString() });
  const found = evaluate([quiet], NOW);
  expect(rules(found)).toEqual(["T5"]);
  expect(found[0]?.repair).toEqual({ flag: "--add-label", label: "aging", fixed: "added aging" });

  // A comment is activity; the issue's own age is not. This is the case an `updatedAt` rule
  // gets wrong, and the reason the script reads comment timestamps instead.
  const spokenOn = issue({
    createdAt: new Date(NOW - 100 * DAY).toISOString(),
    comments: [
      { body: "still relevant", createdAt: new Date(NOW - DAY).toISOString(), author: "atyrode" },
    ],
  });
  expect(evaluate([spokenOn], NOW)).toEqual([]);

  // And the signal is removed once someone speaks, so a stale `aging` is itself a finding.
  const stale = evaluate([issue({ labels: [...issue().labels, "aging"] })], NOW);
  expect(stale[0]?.repair).toEqual({
    flag: "--remove-label",
    label: "aging",
    fixed: "removed aging",
  });
});

test("T5: the boundary is exactly fourteen days, and a blocked issue is never aged", () => {
  const justInside = issue({ createdAt: new Date(NOW - 14 * DAY + 1000).toISOString() });
  expect(evaluate([justInside], NOW)).toEqual([]);
  const justOutside = issue({ createdAt: new Date(NOW - 14 * DAY - 1000).toISOString() });
  expect(rules(evaluate([justOutside], NOW))).toEqual(["T5"]);

  // Waiting on a named blocker is not silence worth reporting: the issue is quiet because it
  // is supposed to be.
  const waiting = issue({
    labels: ["blocked"],
    body: "Blocked by #333.",
    createdAt: new Date(NOW - 90 * DAY).toISOString(),
  });
  expect(evaluate([waiting], NOW)).toEqual([]);
});

test("T6: two priorities is no priority", () => {
  const both = issue({ labels: ["agent-ready", "p1", "p3", "area:feed"] });
  expect(rules(evaluate([both], NOW))).toEqual(["T6"]);
});

test("a tracker in good order reports nothing at all", () => {
  const clean: readonly Issue[] = [
    issue({ number: 10 }),
    issue({ number: 11, labels: ["tracking"] }),
    issue({ number: 12, labels: ["blocked", "design"], body: "Blocked by #10." }),
    issue({
      number: 13,
      labels: ["needs-operator", "design"],
      body: "## Decision\nQuestion: is this still wanted?\n",
    }),
    issue({ number: 14, labels: ["needs-triage"] }),
  ];
  expect(evaluate(clean, NOW)).toEqual([]);
});

test("T7: a blocker that closed is not a blocker, and nothing else notices", () => {
  // The live set is what the caller read back from the tracker: open issues AND open pull
  // requests, because a blocker is as often one as the other and they share a numbering space.
  const live = new Set([10, 11]);
  const spent = issue({ labels: ["blocked"], body: "Blocked by #99, which shipped." });
  expect(rules(evaluate([spent], NOW, live))).toEqual(["T7"]);

  // One live blocker still blocks. A rule that fired on "any blocker closed" would unblock an
  // issue that is genuinely waiting, which is worse than the staleness it set out to catch.
  const partly = issue({ labels: ["blocked"], body: "Blocked by #99 and blocked by #10." });
  expect(evaluate([partly], NOW, live)).toEqual([]);

  // A mention is not a dependency: T3 is satisfied by any `#N`, and this must not be.
  const mentions = issue({ labels: ["blocked"], body: "Related to #99. Blocked by #10." });
  expect(evaluate([mentions], NOW, live)).toEqual([]);

  // A blocker in another repository is real and its state is not knowable here, so the rule
  // declines rather than guessing — `owner/repo#N` is not this repository's #N.
  const elsewhere = issue({
    labels: ["blocked"],
    body: "Blocked by atyrode/manifold#99, upstream.",
  });
  expect(evaluate([elsewhere], NOW, live)).toEqual([]);

  // And the rule is off entirely when the caller could not supply the set, rather than
  // reporting every blocked issue as spent.
  expect(evaluate([spent], NOW)).toEqual([]);
});

test("T7: the blocker may be named in a comment, because that is where it usually arrives", () => {
  const later = issue({
    labels: ["blocked"],
    body: "this needs something else first",
    comments: [
      {
        body: "Blocked by #99, which scaffolds the part.",
        createdAt: new Date(NOW).toISOString(),
        author: "atyrode",
      },
    ],
  });
  expect(rules(evaluate([later], NOW, new Set([10])))).toEqual(["T7"]);
});
