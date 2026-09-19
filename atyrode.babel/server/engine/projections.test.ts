import { describe, expect, test } from "bun:test";
import { projectRecord, type ExportRecord } from "./projections.ts";

/*
  §4.6'S THREE PROJECTIONS (#341). What is asserted here is what each destination CARRIES — a
  reader of an issue draft, an agent reading a brief and the operator reading his own note need
  different things out of one record — and what a classification keeps out of the one destination
  that leaves this deployment.
*/

const PAYLOAD: Record<string, unknown> = {
  title: "the drain retries a batch it already wrote",
  problem: "a worker that lost its lease rewrites the batch it had already committed",
  outcome: "carry the lease fence into the write so a stale worker's batch is refused",
  impact: "high",
  classification: "public-safe",
  uncertainty: "untested above 64 workers",
  risks: ["a fence too narrow refuses a live worker"],
  prerequisites: ["the receiver keeps the fence"],
  verification_criteria: ["a stale worker's batch is refused", "a live worker's batch lands"],
  open_questions: ["does the receiver keep the fence?"],
  supporting: [
    {
      locator: { path: "sessions/0001-omp-s1.jsonl", line: 412, digest: "a".repeat(64) },
      note: "the worker wrote batch 7 twice",
    },
  ],
  conflicting: [
    {
      locator: { path: "sessions/0002-omp-s2.jsonl", line: 8, digest: "b".repeat(64) },
      note: "one run saw no duplicate",
    },
  ],
};

function proposal(payload: Record<string, unknown> = {}): ExportRecord {
  return {
    id: "pro_00000001",
    kind: "proposal",
    rootId: "pro_00000001",
    seq: 0,
    title: "the drain retries a batch it already wrote",
    createdAt: "2026-09-12T12:00:00.000000000Z",
    runId: "run_9",
    supersedesId: "",
    supersededById: "",
    standing: "",
    payload: { ...PAYLOAD, ...payload },
    sessions: [
      {
        selector: "omp/s1",
        title: "the drain session",
        workspace: "/srv/babel",
        digest: "a".repeat(64),
      },
    ],
  };
}

/** The projection's text, or the assertion that it was not refused, in one place. */
function rendered(record: ExportRecord, projection: Parameters<typeof projectRecord>[1]): string {
  const projected = projectRecord(record, projection);
  if ("refused" in projected) throw new Error(`refused: ${projected.refused}`);
  return projected.text;
}

describe("each destination carries what its reader needs", () => {
  test("the issue draft states the change, what it would achieve and how it is checked", () => {
    const text = rendered(proposal(), "issue-draft");

    expect(text).toContain("# the drain retries a batch it already wrote");
    expect(text).toContain("a worker that lost its lease rewrites the batch");
    expect(text).toContain("## What it would achieve");
    expect(text).toContain("carry the lease fence into the write");
    expect(text).toContain("- a stale worker's batch is refused");
    expect(text).toContain("`sessions/0001-omp-s1.jsonl:412`");
    // Nothing of this deployment travels with a draft but the record's own identifier: a draft
    // is read by whoever the operator shows it to, and a path on his machine is not part of the
    // argument.
    expect(text).not.toContain("/srv/babel");
    expect(text).toContain("Nothing here has been published or filed anywhere.");
  });

  test("the agent brief carries locators to open and no reception to agree with", () => {
    const text = rendered(
      { ...proposal(), standing: "accept", supersedesId: "pro_00000000" },
      "agent-brief",
    );

    // §4.6's own list: the problem, the proposed outcome, acceptance criteria, and the locators.
    expect(text).toContain("## Problem");
    expect(text).toContain("## Proposed outcome");
    expect(text).toContain("## Acceptance criteria");
    expect(text).toContain("## Evidence to open");
    expect(text).toContain("`sessions/0001-omp-s1.jsonl:412`");
    expect(text).toContain("session `omp/s1`, in /srv/babel");
    expect(text).toContain(`digest \`${"a".repeat(64)}\``);
    expect(text).toContain("an excerpt is not evidence");
    // An agent told Babel's reviewers liked this is being asked to agree with them, and what it
    // is asked for is whether the evidence holds.
    expect(text).not.toContain("## Standing");
    expect(text).not.toContain("ruled ");
  });

  test("the operator note carries the case, the standing and the revision chain", () => {
    const text = rendered(
      {
        ...proposal(),
        seq: 2,
        standing: "defer",
        supersedesId: "pro_00000000",
        supersededById: "pro_00000002",
      },
      "operator-note",
    );

    expect(text).toContain("- problem: a worker that lost its lease");
    expect(text).toContain("- impact: high");
    expect(text).toContain("- uncertainty: untested above 64 workers");
    expect(text).toContain("ruled defer.");
    expect(text).toContain(
      "revision 2 of pro_00000001, supersedes pro_00000000, superseded by pro_00000002",
    );
    expect(text).toContain("counter-evidence · `sessions/0002-omp-s2.jsonl:8`");
  });

  test("an unruled record says so, rather than rendering a blank standing", () => {
    expect(rendered(proposal(), "operator-note")).toContain("nothing has been ruled on this.");
  });

  test("a section a record has nothing for is absent, not an empty heading", () => {
    const thin = rendered(
      { ...proposal(), payload: { problem: "it stalls", outcome: "fence it" } },
      "agent-brief",
    );

    expect(thin).toContain("## Problem");
    expect(thin).not.toContain("## Risks");
    expect(thin).not.toContain("## Evidence to open");
  });
});

describe("a classification redacts the one destination that leaves", () => {
  test("redaction-required withholds the evidence from an issue draft and names the count", () => {
    const projected = projectRecord(
      proposal({ classification: "redaction-required" }),
      "issue-draft",
    );
    if ("refused" in projected) throw new Error(`refused: ${projected.refused}`);

    expect(projected.withheld).toEqual([
      "2 evidence locators, withheld because this record is classified redaction-required",
    ]);
    expect(projected.text).not.toContain("sessions/0001-omp-s1.jsonl");
    expect(projected.text).not.toContain("the worker wrote batch 7 twice");
    // The record's own argument still travels: what is withheld is what it rests on.
    expect(projected.text).toContain("carry the lease fence into the write");
    expect(projected.text).toContain("2 citations are withheld");
  });

  test("the same classification withholds nothing from the destinations that stay", () => {
    for (const projection of ["agent-brief", "operator-note"] as const) {
      const projected = projectRecord(
        proposal({ classification: "redaction-required" }),
        projection,
      );
      if ("refused" in projected) throw new Error(`refused: ${projected.refused}`);
      expect(projected.withheld).toEqual([]);
      expect(projected.text).toContain("`sessions/0001-omp-s1.jsonl:412`");
    }
  });

  test("private, absent and unrecognized classifications all refuse an issue draft", () => {
    // The default is refusal rather than a case: a classification this build has never heard,
    // treated as publishable, is the one mistake here that cannot be taken back.
    for (const classification of ["private", "", "confidential-ish"]) {
      const projected = projectRecord(proposal({ classification }), "issue-draft");
      expect("refused" in projected && projected.refused).toContain(
        "an issue draft leaves this deployment",
      );
    }
    expect(projectRecord(proposal({ classification: "private" }), "operator-note")).toHaveProperty(
      "text",
    );
  });
});

describe("§4.6 renders a proposal, and an operator note renders anything", () => {
  test("a finding is refused an issue draft and an agent brief, and exports as a note", () => {
    const finding: ExportRecord = {
      ...proposal(),
      id: "fnd_00000005",
      kind: "finding",
      rootId: "fnd_00000005",
      payload: {
        pattern: "every drain stalls on the third batch",
        scope: ["dev-01", "dev-02"],
        counter_evidence: [
          {
            locator: { path: "sessions/0003-omp-s3.jsonl", line: 4, digest: "c".repeat(64) },
            note: "one fleet never stalled",
          },
        ],
      },
    };

    for (const projection of ["issue-draft", "agent-brief"] as const) {
      const projected = projectRecord(finding, projection);
      expect("refused" in projected && projected.refused).toContain(
        "which are a proposal's; export it as an operator note",
      );
    }
    const note = rendered(finding, "operator-note");
    expect(note).toContain("every drain stalls on the third batch");
    expect(note).toContain("- scope: dev-01, dev-02");
    // A finding's own payload states only what argues against it (§4.4), and that is what it
    // exports: a section claiming supporting evidence it does not carry would be an invention.
    expect(note).toContain("counter-evidence · `sessions/0003-omp-s3.jsonl:4`");
  });
});

test("the filename names the destination and the record, and the type is markdown", () => {
  const projected = projectRecord(proposal(), "agent-brief");
  if ("refused" in projected) throw new Error(`refused: ${projected.refused}`);
  expect(projected.filename).toBe("agent-brief-pro_00000001.md");
  expect(projected.classification).toBe("public-safe");
});

test("a citation with a note and no locator is not evidence and is not rendered", () => {
  // §4.3 makes evidence inseparable from its locator, so a note with nothing to open is prose.
  const text = rendered(
    proposal({ supporting: [{ note: "somebody said so" }], conflicting: [] }),
    "agent-brief",
  );
  expect(text).not.toContain("somebody said so");
  expect(text).not.toContain("## Evidence to open");
});
