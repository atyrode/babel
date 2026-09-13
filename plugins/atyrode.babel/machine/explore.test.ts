/*
  The `explore` operation end to end against the fake engine: a real launch, a real stdio
  conversation, real output files.

  The load-bearing assertion is that every row a run writes has exactly the columns schema.ts
  declares for its table — read out of SCHEMA_V1 here rather than retyped — because "the store's
  own row shapes" is the whole transport contract between the machine half and the hub, and a
  column either side renamed would otherwise fail silently at ingestion.
*/

import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Receipt } from "../contract.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import type { Row } from "./engine/rows.ts";
import { explore, ExploreInputSchema, type OperationDeps } from "./explore.ts";
import type { OutputFile, OutputSink } from "./output.ts";

const FIXTURE = join(import.meta.dir, "engine", "fakeengine.ts");
const PROFILE = { id: "analysis", revision: 3 };
const SESSION = { harness: "omp", sourceId: "session-1", selector: "omp/session-1", path: "/archive/omp/session-1.jsonl" };
const LOCATOR = { path: SESSION.path, line: 12, byte_offset: 480, digest: "sha256:abc" };

const directories: string[] = [];

afterEach(async () => {
  for (const directory of directories.splice(0)) await rm(directory, { recursive: true, force: true });
});

/** The sink a test reads instead of a directory. It is the seam `main.ts` fills with the real one. */
class MemorySink implements OutputSink {
  readonly files: Partial<Record<OutputFile, readonly unknown[]>> = {};
  receipted: Receipt | null = null;

  async write(file: OutputFile, rows: readonly unknown[]): Promise<void> {
    this.files[file] = rows;
  }

  async receipt(receipt: Receipt): Promise<void> {
    this.receipted = receipt;
  }

  rows(file: OutputFile): Row[] {
    return (this.files[file] ?? []) as Row[];
  }
}

/** The columns schema.ts declares for one table, in declaration order. */
function columnsOf(table: string): string[] {
  const statement = SCHEMA_V1.find((sql) => sql.startsWith(`CREATE TABLE ${table}(`));
  if (statement === undefined) throw new Error(`schema.ts declares no table ${table}`);
  const body = statement.slice(statement.indexOf("(") + 1, statement.lastIndexOf(")"));
  const parts: string[] = [];
  let depth = 0;
  let current = "";
  for (const character of body) {
    if (character === "(") depth += 1;
    if (character === ")") depth -= 1;
    if (character === "," && depth === 0) {
      parts.push(current);
      current = "";
      continue;
    }
    current += character;
  }
  parts.push(current);
  const constraints: Record<string, true> = { UNIQUE: true, PRIMARY: true, CHECK: true, FOREIGN: true };
  return parts
    .map((part) => part.trim().split(/\s+/)[0] ?? "")
    .filter((name) => name !== "" && constraints[name] !== true);
}

/** Every row of a file has exactly the table's columns — no more, no fewer. */
function expectRowShape(rows: readonly Row[], table: string): void {
  const columns = columnsOf(table).sort();
  expect(rows.length).toBeGreaterThan(0);
  for (const row of rows) expect(Object.keys(row).sort()).toEqual(columns);
}

const RECIPE = {
  id: "read-whats-new",
  version: 3,
  title: "Read what is new",
  body: "Read the sessions and say what changed.",
  stages: ["explore" as const],
};

const RESULT = {
  candidates: [
    {
      ref: "c1",
      hypothesis: {
        statement: "the publisher drops a message after two retries",
        origin_cues: ["two retries in the transcript"],
        novelty: 0.4,
        priority: 0.7,
      },
      observations: [
        {
          ref: "o1",
          recipe: { id: RECIPE.id, version: RECIPE.version },
          claim: {
            claim: "the retry loop exits without logging",
            confidence: "high",
            impact: "high",
            evidence: [{ locator: LOCATOR, note: "the loop breaks on the second failure" }],
            counter_evidence_absent: true,
          },
        },
      ],
      remedy: {
        ref: "r1",
        proposal: {
          title: "log the dropped message",
          problem: "a dropped message is invisible",
          outcome: "the drop is logged with its reason",
          impact: "moderate",
          classification: "private",
          supporting: [{ locator: LOCATOR, note: "the silent exit" }],
        },
      },
    },
  ],
  consolidations: [
    {
      ref: "con1",
      observations: ["o1"],
      finding: {
        title: "silent drops in the publisher",
        pattern: "retries exhaust and nothing is written",
        significance: "published records are lost without a trace",
        counter_evidence_absent: true,
      },
      proposal: {
        title: "make the drop loud",
        problem: "silent loss",
        outcome: "an error is recorded",
        impact: "high",
        classification: "private",
      },
    },
  ],
  deferred: [{ hypothesis: "c1", reason: "the second host was out of this pass's budget" }],
  questions: [
    {
      ref: "q1",
      subjects: ["dev-01"],
      predicates: ["service-placement"],
      hypothesis: "c1",
      prompt: "which host actually runs the publisher?",
      why_asked: "two transcripts name different hosts and the claim depends on it",
    },
  ],
};

interface Launched {
  sink: MemorySink;
  receipt: Receipt;
  promptPath: string;
}

/** One `explore` run against the fixture. */
async function launch(
  options: {
    fake?: readonly string[];
    result?: unknown;
    runId?: string;
    stages?: ("explore" | "challenge" | "synthesize")[];
    recipes?: unknown[];
    deps?: OperationDeps;
  } = {},
): Promise<Launched> {
  const directory = await mkdtemp(join(tmpdir(), "babel-explore-test-"));
  directories.push(directory);
  const promptPath = join(directory, "prompt.txt");
  const payloadPath = join(directory, "submission.json");
  await Bun.write(payloadPath, JSON.stringify(options.result ?? RESULT));

  const input = ExploreInputSchema.parse({
    runId: options.runId ?? "run_explore_test",
    machineId: "dev-01",
    engine: {
      binary: process.execPath,
      args: [FIXTURE, "--fake-prompt-out", promptPath, "--fake-submit", payloadPath, ...(options.fake ?? [])],
    },
    profile: PROFILE,
    preparation: { id: "prep_0001", selection: [{ ...SESSION, digest: "sha256:capture" }] },
    recipes: options.recipes ?? [RECIPE],
    stages: options.stages ?? ["explore"],
    caps: { toolCalls: 8, minutes: 0, perRunUsd: 0, idleMs: 15_000, handshakeMs: 15_000 },
  });
  const sink = new MemorySink();
  const receipt = await explore(input, sink, { workDir: directory, ...options.deps });
  return { sink, receipt, promptPath };
}

test("a run writes every output file in the store's row shapes", async () => {
  const { sink, receipt, promptPath } = await launch();

  expect(receipt.closure).toBe("completed");
  expect(receipt.kind).toBe("explore");
  expect(receipt.machineId).toBe("dev-01");
  expect(receipt.runId).toBe("run_explore_test");
  expect(sink.receipted).toEqual(receipt);

  // Every file the operation declares is written, even when a list is empty.
  expect(Object.keys(sink.files).sort()).toEqual(["edges", "questions", "records", "statusEvents"]);
  expectRowShape(sink.rows("records"), "records");
  expectRowShape(sink.rows("edges"), "edges");
  expectRowShape(sink.rows("statusEvents"), "status_events");
  expectRowShape(sink.rows("questions"), "questions");

  // The prompt carried the recipe verbatim, the stage, and the sessions the run may read.
  const prompt = await Bun.file(promptPath).text();
  expect(prompt).toContain(RECIPE.body);
  expect(prompt).toContain("babel.stage = explore");
  expect(prompt).toContain(SESSION.selector);

  // The chain: a hypothesis, its observation, the remedy addressing it, the finding, its proposal.
  const records = sink.rows("records");
  const kinds = records.map((row) => row.kind);
  expect(kinds.filter((kind) => kind === "hypothesis")).toHaveLength(1);
  expect(kinds.filter((kind) => kind === "observation")).toHaveLength(1);
  expect(kinds.filter((kind) => kind === "finding")).toHaveLength(1);
  expect(kinds.filter((kind) => kind === "proposal")).toHaveLength(2);

  const hypothesis = records.find((row) => row.kind === "hypothesis");
  const observation = records.find((row) => row.kind === "observation");
  const finding = records.find((row) => row.kind === "finding");
  expect(hypothesis?.id).toMatch(/^hyp_[0-9a-f]{32}$/);
  expect(hypothesis?.root_id).toBe(hypothesis?.id as string);
  expect(hypothesis?.supersedes_id).toBeNull();
  expect(hypothesis?.actor_kind).toBe("run");
  expect(hypothesis?.actor_id).toBe("run_explore_test");
  expect(hypothesis?.title).toBe("the publisher drops a message after two retries");
  expect(JSON.parse(String(hypothesis?.payload)).novelty).toBe(0.4);
  // §4.3: an observation hangs off exactly one hypothesis, and carries its recipe provenance.
  expect(observation?.parent_id).toBe(hypothesis?.id as string);
  expect(observation?.recipe_id).toBe(RECIPE.id);
  expect(observation?.recipe_version).toBe(RECIPE.version);

  // The cites edge names the session by the selector the catalog holds, not by the locator path.
  const edges = sink.rows("edges");
  const cites = edges.find((row) => row.kind === "cites" && row.from_id === observation?.id);
  expect(cites?.to_kind).toBe("session");
  expect(cites?.to_id).toBe(SESSION.selector);
  expect(cites?.note).toBe("the loop breaks on the second failure");
  expect(edges.some((row) => row.kind === "addresses" && row.to_id === hypothesis?.id)).toBe(true);
  const consolidates = edges.find((row) => row.kind === "consolidates");
  expect(consolidates?.from_id).toBe(finding?.id as string);
  expect(consolidates?.to_id).toBe(observation?.id as string);
  expect(consolidates?.position).toBe(0);
  expect(edges.some((row) => row.kind === "derived_from" && row.to_id === finding?.id)).toBe(true);

  // The lifecycle is append-only: untriaged on emission, then the promotion, then the deferral.
  const statuses = sink.rows("statusEvents").filter((row) => row.record_id === hypothesis?.id);
  expect(statuses.map((row) => [row.seq, row.status])).toEqual([
    [1, "untriaged"],
    [2, "promoted"],
    [3, "deferred"],
  ]);
  expect(statuses[2]?.reason).toBe("the second host was out of this pass's budget");

  // A question that holds up a candidate is blocking, and names the record it holds up.
  const question = sink.rows("questions")[0];
  expect(question?.kind).toBe("acquire-context");
  expect(question?.class).toBe("blocking");
  expect(question?.text).toBe("which host actually runs the publisher?");
  expect(JSON.parse(String(question?.payload)).blocks).toBe(hypothesis?.id);

  // The receipt states what actually ran, from Code's own report, and what the run produced.
  expect(receipt.profile?.["id"]).toBe(PROFILE.id);
  expect(receipt.profile?.["model"]).toBe("synthetic-1");
  expect(receipt.profile?.["costPer1k"]).toEqual({ input: 0.001, output: 0.002 });
  expect(receipt.profile?.["containment"]).toBe("synthetic-bwrap");
  expect(receipt.counts["records"]).toBe(records.length);
  expect(receipt.counts["hypotheses"]).toBe(1);
  expect(receipt.counts["findings"]).toBe(1);
  expect(receipt.counts["questions"]).toBe(1);
  expect(receipt.tokens).toBe(1540);
  expect(receipt.costUsd).toBeCloseTo(0.0123, 6);
});

test("a refused containment stops before any prompt and the receipt carries the reason", async () => {
  const { sink, receipt, promptPath } = await launch({ fake: ["--fake-containment", "weak"] });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("containment");
  expect(receipt.reason).toContain("network default-deny");
  expect(await Bun.file(promptPath).exists()).toBe(false);
  // Nothing was produced, and the empty files say so rather than being absent.
  expect(sink.rows("records")).toEqual([]);
  expect(receipt.counts["records"]).toBe(0);
  // The profile is still recorded: it is what the refused engine was launched under.
  expect(receipt.profile?.["id"]).toBe(PROFILE.id);
});

test("a malformed result is a failed closure carrying the refusal the model was given", async () => {
  const { sink, receipt } = await launch({ result: { candidates: "not a list" } });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("explore:");
  expect(receipt.reason).toContain("does not match its schema");
  expect(sink.rows("records")).toEqual([]);
  expect(receipt.counts["submissions"]).toBe(1);
});

test("a result naming a recipe the stage did not select is refused", async () => {
  const { receipt } = await launch({
    result: {
      candidates: [
        {
          ref: "c1",
          hypothesis: { statement: "s" },
          observations: [
            {
              ref: "o1",
              recipe: { id: "some-other-recipe", version: 1 },
              claim: {
                claim: "c",
                confidence: "low",
                impact: "low",
                evidence: [{ locator: LOCATOR, note: "n" }],
                counter_evidence_absent: true,
              },
            },
          ],
        },
      ],
    },
  });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("some-other-recipe@1");
});

test("a citation this run was not served is refused before it becomes durable", async () => {
  const { receipt } = await launch({ deps: { served: () => false } });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("was not served");
});

test("citations are counted as unverified when no facility served anything", async () => {
  const { receipt } = await launch();
  // Wave 1 registers no evidence tool, so the honest record is how many locators nobody checked
  // rather than a claim that they were verified.
  expect(receipt.counts["unverifiedCitations"]).toBeGreaterThan(0);
});

test("record ids are derived, so a re-delivered run ingests once", async () => {
  const first = await launch({ runId: "run_stable" });
  const second = await launch({ runId: "run_stable" });

  expect(first.sink.rows("records").map((row) => row.id)).toEqual(second.sink.rows("records").map((row) => row.id));
  expect(first.sink.rows("edges").map((row) => row.id)).toEqual(second.sink.rows("edges").map((row) => row.id));

  const other = await launch({ runId: "run_other" });
  expect(other.sink.rows("records")[0]?.id).not.toBe(first.sink.rows("records")[0]?.id);
});

test("a stage no selected recipe declares is skipped and recorded, not failed", async () => {
  const { receipt } = await launch({ stages: ["explore", "challenge"] });

  expect(receipt.closure).toBe("completed");
  expect(receipt.counts["skipped.challenge"]).toBe(1);
  expect(receipt.counts["jobs"]).toBe(1);
});

test("a challenge stage turns an ungrounded objection into a contradicting candidate", async () => {
  const { sink, receipt } = await launch({
    stages: ["challenge"],
    recipes: [{ ...RECIPE, id: "challenge-it", stages: ["challenge"] }],
    result: {
      objections: [
        {
          ref: "j1",
          hypothesis: "hyp_0123456789abcdef",
          grounds: "alternative",
          recipe: { id: "challenge-it", version: RECIPE.version },
          claim: {
            claim: "a queue would drop nothing at all",
            confidence: "moderate",
            impact: "moderate",
            evidence: [],
            counter_evidence_absent: true,
          },
        },
      ],
    },
  });

  expect(receipt.closure).toBe("completed");
  const records = sink.rows("records");
  expect(records).toHaveLength(1);
  expect(records[0]?.kind).toBe("hypothesis");
  const contradicts = sink.rows("edges").find((row) => row.kind === "contradicts");
  expect(contradicts?.from_id).toBe(records[0]?.id as string);
  expect(contradicts?.to_id).toBe("hyp_0123456789abcdef");
});

test("a challenge stage turns a locator-backed objection into a counter-observation", async () => {
  const { sink } = await launch({
    stages: ["challenge"],
    recipes: [{ ...RECIPE, id: "challenge-it", stages: ["challenge"] }],
    result: {
      objections: [
        {
          ref: "j1",
          hypothesis: "hyp_0123456789abcdef",
          grounds: "evidence",
          recipe: { id: "challenge-it", version: RECIPE.version },
          claim: {
            claim: "the transcript shows three retries, not two",
            confidence: "high",
            impact: "moderate",
            evidence: [{ locator: LOCATOR, note: "three attempts are logged" }],
            counter_evidence_absent: true,
          },
        },
      ],
    },
  });

  const observation = sink.rows("records")[0];
  expect(observation?.kind).toBe("observation");
  expect(observation?.parent_id).toBe("hyp_0123456789abcdef");
  expect(sink.rows("edges").some((row) => row.kind === "contradicts" && row.from_id === observation?.id)).toBe(true);
  expect(sink.rows("edges").some((row) => row.kind === "cites" && row.to_id === SESSION.selector)).toBe(true);
});

test("a deferral of a record from the brief appends with seq 0, which the hub resolves", async () => {
  const { sink } = await launch({
    result: { candidates: [], deferred: [{ hypothesis: "hyp_0123456789abcdef", reason: "nobody has time for it" }] },
  });

  const status = sink.rows("statusEvents")[0];
  expect(status?.record_id).toBe("hyp_0123456789abcdef");
  expect(status?.seq).toBe(0);
  expect(status?.status).toBe("deferred");
});
