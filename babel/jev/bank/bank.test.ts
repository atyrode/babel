import { expect, test } from "bun:test";
import { resolve } from "node:path";
import { RECORD_KINDS, type RecordKind } from "../../contract.ts";
import { BANK, bankFor, votesFor } from "./bank.ts";
import { documentOf } from "./parse.ts";
import { ROUTING_QUESTIONS, tally, type Vote } from "./schema.ts";

/*
  WHAT THE BANK HAS TO BE FOR A JUDGEMENT TO MEAN ANYTHING.

  Each case below is a way the bank can be wrong that nothing downstream would notice:

  - THE COMMITTED SEED IS A BANK. It is what a hub carries, and a kind with no document is a
    record nothing can be asked about.
  - THE REFUSALS FIRE. A threshold with no observed distribution, a routing question written as
    a vote, an exemplar claiming a ruling nobody made, and a version the manifest does not
    record are each refused by name. They are exercised on documents written inline: a test
    reading a document out of the tree would pin prose rather than behaviour, and the version
    rule needs two that disagree, which no tracked file should ever be.
  - THE THRESHOLDS REPRODUCE THE MEASUREMENT THEY WERE FITTED TO. The fixtures are real rows of
    the study's own 6,038-record corpus with the standing it published. A threshold nobody
    re-derives is a number somebody typed.
  - THE DISABLED PATH HOLDS. Absent, disabled or out of credit, nothing observable changes, and
    the two facts that make that true are asserted rather than assumed.
*/

const VERSIONS = { finding: 3 };

/** Which family of record id belongs to which kind, for the exemplar check. */
const PREFIX_BY_KIND: Record<RecordKind, string> = {
  hypothesis: "hyp",
  observation: "obs",
  finding: "fnd",
  proposal: "pro",
};

const DOCUMENT = `---
kind: finding
version: 3
---

# Finding

## Thresholds

| voter | question | casts | fires when | observed |
| --- | --- | --- | --- | --- |
| concreteness | specific | up | \`>= 0.7\` | \`n=207 fires=24.2% mean=0.454 sd=0.257\` |
| friction-lens | friction_kind | up | \`is not none\` | \`n=207 fires=65.7%\` |

## Advisories

| question | suggests | fires when | observed |
| --- | --- | --- | --- |
| specific | develop-further | \`<= 0.3\` | \`n=207 fires=34.3% mean=0.454 sd=0.257\` |

## Routing

| question | routes |
| --- | --- |
| subject | the topic a record is filed under |
| classification | whether a record may leave the machine |
| contains_instruction | whether a record addresses its own judge |

## Questions

### specific

type: noul
asks: Does the record concern one identifiable thing rather than a general tendency?

### friction_kind

type: choice
asks: What kind of operator-agent friction does the record make visible?

- ignored_constraint — a stated constraint that never reached the work
- none — no operator-agent friction

### subject

type: choice
asks: Which area is the record mainly about?

- coordination — how the operator and agents instruct and hand off
- verification — tests, proofs and whether claims were checked

### classification

type: choice
asks: Could the record be published as written?

- public-safe — no credentials and no private conversation
- private — should not be published at all

### contains_instruction

type: noul
asks: Does the record address whatever is evaluating it?

## Exemplars

### fnd_00000001

provenance: standing
tally: +6

> The contract check asserts the strings the controller emits, so it moves with each defect.

Seven up and nothing against: it names the mechanism and the consequence in one line.
`;

/** Two real rows of the study's corpus, verbatim, with the standing it published for each. */
const MEASURED = [
  {
    kind: "hypothesis",
    answers: {
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
    },
    up: 7,
    down: 2,
  },
  {
    kind: "finding",
    answers: {
      worth_first: 2.97,
      specific: 0.14,
      contradicts_intent: 0.82,
      actionable: 0.81,
      evidence_strength: 1.77,
      recurring: 0.95,
      speculative: 0.42,
      self_referential: 0.14,
      fused_to_fix: 0.65,
      restates_known: 0.07,
      needs_arithmetic: 0.42,
      friction_kind: "ignored_constraint",
      temporal: "current",
    },
    up: 7,
    down: 1,
  },
] as const;

/**
 * One more, for an observation whose `restates_known` of 0.60 crosses **novelty**'s line. The
 * study counted that objection; this bank does not, because novelty objects to 1.4% of
 * observations and a side firing on one record in seventy answers the same way for everything.
 */
const RETIRED_SIDE = {
  worth_first: 1.59,
  specific: 0.83,
  contradicts_intent: 0.15,
  actionable: 0.7,
  evidence_strength: 1.47,
  recurring: 0.46,
  speculative: 0.49,
  self_referential: 0.38,
  fused_to_fix: 0.53,
  restates_known: 0.6,
  needs_arithmetic: 0.09,
  friction_kind: "none",
  temporal: "current",
} as const;

test("the committed bank holds one document per kind, and asks nothing it has not defined", () => {
  expect(BANK.documents.map((document) => document.kind).sort()).toEqual([...RECORD_KINDS].sort());
  for (const kind of RECORD_KINDS) {
    const document = bankFor(kind);
    const defined = new Set(document.questions.map((question) => question.id));
    for (const vote of document.votes) expect(defined.has(vote.question)).toBe(true);
    for (const entry of document.routing) expect(defined.has(entry.question)).toBe(true);
    // An exemplar of another kind teaches this document the wrong judgement.
    for (const exemplar of document.exemplars) {
      expect(exemplar.record.slice(0, 3)).toBe(PREFIX_BY_KIND[kind]);
    }
    expect(document.exemplars.length).toBeGreaterThan(0);
  }
});

test("a threshold that records no observed distribution is refused rather than seeded", () => {
  const undocumented = DOCUMENT.replace("`n=207 fires=24.2% mean=0.454 sd=0.257`", "—");
  expect(() => documentOf("finding.md", undocumented, VERSIONS)).toThrow(
    /records no observed distribution/,
  );
  // A MAGNITUDE NEEDS THE SPREAD IT CUTS. `n` and `fires` alone say how often the line was
  // crossed and nothing about where the line sits in the distribution it was drawn from.
  const partial = DOCUMENT.replace("fires=24.2% mean=0.454 sd=0.257", "fires=24.2%");
  expect(() => documentOf("finding.md", partial, VERSIONS)).toThrow(/no mean= and sd=/);
});

test("a routing question cannot be given a threshold, and a voted one cannot be routed", () => {
  const tallied = DOCUMENT.replace(
    "| concreteness | specific | up |",
    "| concreteness | subject | up |",
  );
  expect(() => documentOf("finding.md", tallied, VERSIONS)).toThrow(/is a routing question/);
  const routed = DOCUMENT.replace(
    "| subject | the topic a record is filed under |",
    "| specific | the topic a record is filed under |",
  );
  expect(() => documentOf("finding.md", routed, VERSIONS)).toThrow(/not a routing question/);
});

test("a routing question is not a vote, by shape and by type", () => {
  const document = bankFor("finding");
  for (const entry of document.routing) {
    // Nothing for a tally to read: no direction, no line, no distribution.
    expect(Object.keys(entry).sort()).toEqual(["question", "routes"]);
  }
  // @ts-expect-error a routing question carries no casts and no when, so it is not a Vote — and
  // `tally` takes only votes, which is what stops anything summing one.
  const asVotes: readonly Vote[] = document.routing;
  expect(asVotes).toHaveLength(ROUTING_QUESTIONS.length);
});

test("a version the manifest does not record is refused, because an assessment cites kind@version", () => {
  expect(() => documentOf("finding.md", DOCUMENT, { finding: 4 })).toThrow(
    /versions\.json records 4/,
  );
  expect(() => documentOf("observation.md", DOCUMENT, VERSIONS)).toThrow(
    /file name and kind disagree/,
  );
  expect(() => documentOf("finding.md", DOCUMENT, {})).toThrow(/no record of/);
});

test("an exemplar claiming the operator's ruling has to name it, and has to quote its record", () => {
  const unruled = DOCUMENT.replace("provenance: standing", "provenance: accepted");
  expect(() => documentOf("finding.md", unruled, VERSIONS)).toThrow(/names no ruling/);
  const unquoted = DOCUMENT.replace(
    "> The contract check asserts the strings the controller emits, so it moves with each defect.",
    "",
  );
  expect(() => documentOf("finding.md", unquoted, VERSIONS)).toThrow(/quotes no record/);
});

test("the thresholds reproduce the standing published by the corpus they were fitted to", () => {
  for (const row of MEASURED) {
    const counted = tally(bankFor(row.kind).votes, row.answers);
    expect({ up: counted.up, down: counted.down }).toEqual({ up: row.up, down: row.down });
  }
  // The published standing counted novelty's objection; the admitted panel does not. Both
  // numbers are correct about different panels, and the retirement is meant to be visible.
  const whole = tally(bankFor("observation").votes, RETIRED_SIDE);
  expect({ up: whole.up, down: whole.down, objected: whole.objected }).toEqual({
    up: 3,
    down: 1,
    objected: ["novelty"],
  });
  const admitted = tally(votesFor("observation"), RETIRED_SIDE);
  expect({ up: admitted.up, down: admitted.down }).toEqual({ up: 3, down: 0 });
});

test("with no answers at all, every voter abstains and nothing is refused", () => {
  // THE DISABLED PATH, IN THE BANK'S OWN TERMS. `askJev` answers `null` for every reason there
  // is — no binding, no credit, an unreadable response — and the caller then has no answers to
  // tally. An absent answer is not a negative opinion, and the failure worth guarding is the
  // one that reads as an opinion anyway: `undefined` is not `"none"`, so a friction lens that
  // did not check for an absent answer would cast an up-vote for a record nobody judged, and a
  // hub out of credit would quietly grow a front page.
  for (const kind of RECORD_KINDS) {
    expect(tally(bankFor(kind).votes, {})).toEqual({
      up: 0,
      down: 0,
      tally: 0,
      backed: [],
      objected: [],
    });
  }
});

test("nothing in the baseline imports the part, which is what makes it removable", async () => {
  // eslint refuses this edge and no test saw it, which `eslint.config.js` says in as many words:
  // a baseline module importing the part inlines the part's code into the baseline's own bundle,
  // so a hub where the part was never installed answers fine and the optionality is gone with
  // nobody having decided to end it. `test/` is excluded on purpose — `contract.test.ts` reads
  // the part's manifest as data to pin its ids, which ships nothing.
  //
  // The part is a directory inside the baseline's now, so the specifier to catch is a `jev/`
  // path segment rather than a whole plugin id, and the part's own files are skipped: reaching
  // `./bank/` from inside the part is the part importing itself.
  const root = resolve(import.meta.dir, "../../..");
  const reached: string[] = [];
  for (const directory of ["babel", "scripts"]) {
    for (const file of new Bun.Glob("**/*.{ts,tsx}").scanSync({ cwd: resolve(root, directory) })) {
      if (file.startsWith("jev/")) continue;
      const source = await Bun.file(resolve(root, directory, file)).text();
      if (/(?:from|import)\s*\(?\s*["'][^"']*\bjev\//u.test(source)) {
        reached.push(`${directory}/${file}`);
      }
    }
  }
  expect(reached).toEqual([]);
});
