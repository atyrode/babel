/*
  The `evaluate` operation end to end against the fake engine: one drawn assignment in, an
  assessment out, plus whatever the role's answer makes durable.

  Two obligations carry the file. The blind is enforced by REFUSING THE LAUNCH — checked by the
  absence of the prompt file — because §4.12's blinding is what Babel served and not an
  instruction about attention. And §4.13's acts are proposals: a backlog pass that decided to
  retire a candidate writes a proposal and a plan and NO status event, because only the operator's
  acceptance applies one.
*/

import { afterEach, expect, test } from "bun:test";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Receipt } from "../contract.ts";
import { SCHEMA_V1 } from "../store/schema.ts";
import { evaluate, EvaluateInputSchema } from "./evaluate.ts";
import type { Row } from "./engine/rows.ts";
import type { OperationDeps } from "./explore.ts";
import type { OutputFile, OutputSink } from "./output.ts";

const FIXTURE = join(import.meta.dir, "engine", "fakeengine.ts");
const PROFILE = { id: "analysis", revision: 3 };

const RECIPE = { id: "reception-vote", version: 2, title: "Receive a record", body: "Read it and say what you think." };

const ASSIGNMENT = {
  id: "clm_0001",
  recordId: "pro_0123456789abcdef",
  revisionId: "pro_0123456789abcdef",
  rootId: "pro_0123456789abcdef",
  kind: "proposal",
  role: "reception",
  lane: "coverage",
  policyVersion: "pol_0003",
  contextVersion: "ctx_0007",
  blinded: false,
  fence: 4,
};

const TARGET = {
  kind: "proposal",
  id: "pro_0123456789abcdef",
  title: "make the drop loud",
  problem: "silent loss",
  outcome: "an error is recorded",
};

const directories: string[] = [];

afterEach(async () => {
  for (const directory of directories.splice(0)) await rm(directory, { recursive: true, force: true });
});

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

function expectRowShape(rows: readonly Row[], table: string): void {
  const columns = columnsOf(table).sort();
  expect(rows.length).toBeGreaterThan(0);
  for (const row of rows) expect(Object.keys(row).sort()).toEqual(columns);
}

interface Launched {
  sink: MemorySink;
  receipt: Receipt;
  promptPath: string;
}

/** One `evaluate` run against the fixture. */
async function launch(options: {
  result: unknown;
  assignment?: Record<string, unknown>;
  target?: unknown;
  previous?: unknown[];
  ledger?: unknown;
  backlog?: unknown;
  authored?: { target?: boolean; subjects?: string[] };
  deps?: OperationDeps;
}): Promise<Launched> {
  const directory = await mkdtemp(join(tmpdir(), "babel-evaluate-test-"));
  directories.push(directory);
  const promptPath = join(directory, "prompt.txt");
  const payloadPath = join(directory, "submission.json");
  await Bun.write(payloadPath, JSON.stringify(options.result));

  const input = EvaluateInputSchema.parse({
    runId: "run_evaluate_test",
    machineId: "dev-01",
    engine: {
      binary: process.execPath,
      args: [FIXTURE, "--fake-prompt-out", promptPath, "--fake-submit", payloadPath],
    },
    profile: PROFILE,
    assignment: { ...ASSIGNMENT, ...options.assignment },
    target: options.target ?? TARGET,
    previous: options.previous ?? [],
    ...(options.ledger === undefined ? {} : { ledger: options.ledger }),
    ...(options.backlog === undefined ? {} : { backlog: options.backlog }),
    ...(options.authored === undefined ? {} : { authored: options.authored }),
    recipe: RECIPE,
    sources: [{ kind: "session", selector: "omp/session-1", digest: "sha256:capture" }],
    caps: { toolCalls: 4, minutes: 0, perRunUsd: 0, idleMs: 15_000, handshakeMs: 15_000 },
  });
  const sink = new MemorySink();
  const receipt = await evaluate(input, sink, { workDir: directory, ...options.deps });
  return { sink, receipt, promptPath };
}

test("a reception review writes the assessment row the coordinator reconciles from", async () => {
  const { sink, receipt, promptPath } = await launch({
    result: { vote: "support", uncertainty: "the second criterion is untested" },
  });

  expect(receipt.closure).toBe("completed");
  expect(receipt.kind).toBe("evaluate");
  expect(receipt.role).toBe("reception");
  expect(receipt.recipeId).toBe(RECIPE.id);
  expect(sink.receipted).toEqual(receipt);
  // An evaluation writes no status event at all: §4.13's acts are proposals, and a record's
  // lifecycle moves when the operator rules, not when a pass reaches an answer.
  expect(Object.keys(sink.files).sort()).toEqual([
    "assessments",
    "edges",
    "filings",
    "plans",
    "records",
    "steeringReplies",
  ]);
  expectRowShape(sink.rows("assessments"), "assessments");

  const assessment = sink.rows("assessments")[0];
  expect(assessment?.record_id).toBe(ASSIGNMENT.recordId);
  // The identity under review is the revision's own: a vote binds to the wording that was read.
  expect(assessment?.revision_id).toBe(ASSIGNMENT.revisionId);
  expect(assessment?.role).toBe("reception");
  expect(assessment?.vote).toBe("support");
  expect(assessment?.lane).toBe("coverage");
  expect(assessment?.claim_id).toBe("clm_0001");
  expect(assessment?.run_id).toBe("run_evaluate_test");
  const payload = JSON.parse(String(assessment?.payload));
  expect(payload.uncertainty).toBe("the second criterion is untested");
  expect(payload.recipe).toEqual({ id: RECIPE.id, version: RECIPE.version });

  // Nothing else moved: reception judges the record and writes no record of its own.
  expect(sink.rows("records")).toEqual([]);
  expect(sink.rows("plans")).toEqual([]);
  expect(receipt.counts["answer.vote.support"]).toBe(1);

  // The prompt states the assignment and the lane it came from, since this one is not blinded.
  const prompt = await Bun.file(promptPath).text();
  expect(prompt).toContain("babel.review.role = reception");
  expect(prompt).toContain("babel.review.lane = coverage");
  expect(prompt).toContain(RECIPE.body);
  expect(prompt).toContain(TARGET.title);
});

test("a blinded review is refused before any prompt when its read context carries a tally", async () => {
  const { sink, receipt, promptPath } = await launch({
    assignment: { blinded: true },
    target: { ...TARGET, reception: { support: 3, oppose: 1 } },
    result: { vote: "support" },
  });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("blinding");
  expect(receipt.reason).toContain("reception");
  // The remedy is the projection, so nothing was launched and nothing was written.
  expect(await Bun.file(promptPath).exists()).toBe(false);
  expect(sink.rows("assessments")).toEqual([]);
  expect(receipt.counts["jobs"]).toBe(0);
});

test("prior evaluations offered to a blinded review are refused too", async () => {
  const { receipt } = await launch({
    assignment: { blinded: true },
    previous: [{ kind: "comment", by: "run_0002", text: "it reads wider than the evidence" }],
    result: { vote: "oppose" },
  });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("prior evaluations were offered");
});

test("a blinded review is told it is blind and not told its lane", async () => {
  const { receipt, promptPath } = await launch({ assignment: { blinded: true }, result: { vote: "unsure" } });

  expect(receipt.closure).toBe("completed");
  const prompt = await Bun.file(promptPath).text();
  expect(prompt).toContain("babel.review.blinded = true");
  expect(prompt).toContain("taken blind");
  // A lane reserved for never-reviewed work says something about the target's prior evaluations.
  expect(prompt).not.toContain("babel.review.lane");
});

test("a skip is recorded as a gap rather than as an opposing vote", async () => {
  const { sink, receipt } = await launch({ result: { skip: "the evidence is unreachable from this machine" } });

  const assessment = sink.rows("assessments")[0];
  expect(assessment?.vote).toBeNull();
  expect(JSON.parse(String(assessment?.payload)).skip).toContain("unreachable");
  expect(receipt.counts["answer.skip"]).toBe(1);
});

test("a submission outside the role's authority is a failed closure with the reason", async () => {
  const { sink, receipt } = await launch({ result: { vote: "support", outcome: "verified" } });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("does not match its schema");
  expect(sink.rows("assessments")).toEqual([]);
});

test("a review that endorses work its own run authored is refused", async () => {
  const { receipt } = await launch({ result: { vote: "support" }, authored: { target: true } });

  expect(receipt.closure).toBe("failed");
  expect(receipt.reason).toContain("authored the record under review");
});

// ---------------------------------------------------------------------------- §4.13's filing pass

const FILING_ASSIGNMENT = { role: "filing", lane: "filing", recordId: "hyp_0123456789abcdef", revisionId: "hyp_0123456789abcdef", kind: "hypothesis" };
const LEDGER = {
  topics: [{ id: "ent_0000000000000001", name: "babel", kind: "project", aliases: ["the archive"] }],
  operator_asks: [{ id: "str_0001", topic: "babel", text: "split the web half out" }],
};

test("a filing under an existing topic is one filings row and nothing else", async () => {
  const { sink, receipt } = await launch({
    assignment: FILING_ASSIGNMENT,
    ledger: LEDGER,
    result: { filing: { entity: "babel", rationale: "every session it cites is a checkout of it" } },
  });

  expect(receipt.closure).toBe("completed");
  expect(receipt.role).toBe("filing");
  expectRowShape(sink.rows("filings"), "filings");
  const filing = sink.rows("filings")[0];
  expect(filing?.record_id).toBe(FILING_ASSIGNMENT.recordId);
  expect(filing?.entity_id).toBe("babel");
  expect(filing?.rationale).toContain("checkout");
  expect(filing?.author_kind).toBe("run");
  expect(filing?.heuristic).toBe(0);
  expect(filing?.withdrawn).toBe(0);
  expect(sink.rows("plans")).toEqual([]);
  expect(receipt.counts["answer.filing.filed"]).toBe(1);
});

test("about nothing in particular is an answer, and therefore a row", async () => {
  const { sink, receipt } = await launch({
    assignment: FILING_ASSIGNMENT,
    ledger: LEDGER,
    result: { no_topic: { reason: "this is about the run's own process, not about a thing in the world" } },
  });

  const filing = sink.rows("filings")[0];
  expect(filing?.entity_id).toBe("");
  expect(filing?.rationale).toContain("run's own process");
  expect(receipt.counts["answer.filing.no-topic"]).toBe(1);
});

test("a topic proposal is a proposal record plus an open plan, and files nothing", async () => {
  const { sink, receipt } = await launch({
    assignment: FILING_ASSIGNMENT,
    ledger: LEDGER,
    result: {
      topic: {
        operation: "create",
        name: "manifold",
        kind: "repository",
        identity: "github.com/atyrode/manifold",
        remote: "git@github.com:atyrode/manifold.git",
        aliases: ["the hub"],
        reasoning: "the record is about a repository no listed entity names",
        considered: ["babel"],
      },
    },
  });

  expect(receipt.closure).toBe("completed");
  expectRowShape(sink.rows("records"), "records");
  expectRowShape(sink.rows("plans"), "plans");
  const proposal = sink.rows("records")[0];
  expect(proposal?.kind).toBe("proposal");
  expect(proposal?.title).toBe("Create the topic manifold");
  expect(proposal?.actor_kind).toBe("run");

  const plan = sink.rows("plans")[0];
  expect(plan?.kind).toBe("topic");
  expect(plan?.subject_kind).toBe("proposal");
  expect(plan?.subject_id).toBe(proposal?.id as string);
  expect(plan?.operation).toBe("create");
  expect(plan?.state).toBe("open");
  expect(plan?.ruled_by).toBeNull();
  expect(plan?.dedupe_key).toBe("github.com/atyrode/manifold");
  expect(JSON.parse(String(plan?.payload)).record).toBe(FILING_ASSIGNMENT.recordId);

  // The record stays unfiled until the operator accepts, which is what keeps it drawable.
  expect(sink.rows("filings")).toEqual([]);
  expect(sink.rows("edges").some((row) => row.kind === "addresses" && row.to_id === FILING_ASSIGNMENT.recordId)).toBe(
    true,
  );
  expect(receipt.counts["answer.filing.topic.create"]).toBe(1);
});

test("a merge proposal carries its two targets and creates nothing", async () => {
  const { sink } = await launch({
    assignment: FILING_ASSIGNMENT,
    ledger: LEDGER,
    result: {
      topic: {
        operation: "merge",
        targets: ["ent_0000000000000002", "ent_0000000000000001"],
        reasoning: "both names answer to one repository",
      },
    },
  });

  const plan = sink.rows("plans")[0];
  expect(plan?.operation).toBe("merge");
  expect(plan?.dedupe_key).toBe("ent_0000000000000002+ent_0000000000000001");
  expect(sink.rows("records")[0]?.title).toBe("Merge ent_0000000000000002 into ent_0000000000000001");
  expect(sink.rows("filings")).toEqual([]);
});

test("an ask the pass judges wrong is answered where the operator asked it", async () => {
  const { sink, receipt } = await launch({
    assignment: FILING_ASSIGNMENT,
    ledger: LEDGER,
    result: {
      no_change: {
        ask_id: "str_0001",
        reason: "the web half is not a separate thing; the records under it cite the same checkout",
      },
    },
  });

  expectRowShape(sink.rows("steeringReplies"), "steering");
  const reply = sink.rows("steeringReplies")[0];
  expect(reply?.root_id).toBe("str_0001");
  expect(reply?.reply_to_id).toBe("str_0001");
  expect(reply?.actor_kind).toBe("run");
  expect(reply?.text).toContain("not a separate thing");
  // Nothing about the ledger moved.
  expect(sink.rows("filings")).toEqual([]);
  expect(sink.rows("plans")).toEqual([]);
  expect(receipt.counts["answer.filing.no-change"]).toBe(1);
});

// ---------------------------------------------------------------------------- §4.13's backlog pass

const BACKLOG_ASSIGNMENT = {
  role: "backlog",
  lane: "backlog",
  recordId: "hyp_0123456789abcdef",
  revisionId: "hyp_0123456789abcdef",
  kind: "hypothesis",
};
const MATERIAL = {
  candidate: { id: "hyp_0123456789abcdef", statement: "the publisher drops a message", status: "deferred" },
  siblings: [{ id: "hyp_beefbeefbeefbeef", statement: "the publisher drops a message after two retries" }],
};

test("a backlog act is a proposal and a plan, and settles nothing by itself", async () => {
  const { sink, receipt } = await launch({
    assignment: BACKLOG_ASSIGNMENT,
    backlog: MATERIAL,
    result: {
      retire: { reason: "the publisher it describes was removed and no observation was ever recorded against it" },
    },
  });

  expect(receipt.closure).toBe("completed");
  const proposal = sink.rows("records")[0];
  expect(proposal?.kind).toBe("proposal");
  expect(proposal?.title).toBe("Retire this candidate");
  const plan = sink.rows("plans")[0];
  expect(plan?.kind).toBe("backlog");
  expect(plan?.operation).toBe("retire");
  expect(plan?.state).toBe("open");
  // The status the plan would append travels in the payload; it is NOT written now.
  expect(JSON.parse(String(plan?.payload)).status).toBe("retired");
  expect(sink.files["statusEvents"]).toBeUndefined();
  expect(receipt.counts["answer.backlog.retire"]).toBe(1);
});

test("a supersession names the candidate that says it better without moving either", async () => {
  const { sink } = await launch({
    assignment: BACKLOG_ASSIGNMENT,
    backlog: MATERIAL,
    result: { supersede: { by: "hyp_beefbeefbeefbeef", reason: "it says the same thing with the retry count" } },
  });

  const plan = sink.rows("plans")[0];
  expect(plan?.operation).toBe("supersede");
  expect(JSON.parse(String(plan?.payload)).supersede.by).toBe("hyp_beefbeefbeefbeef");
  expect(JSON.parse(String(plan?.payload)).status).toBe("superseded");
  expect(sink.files["statusEvents"]).toBeUndefined();
});

test("keeping a candidate exactly as it is writes only the assessment", async () => {
  const { sink, receipt } = await launch({
    assignment: BACKLOG_ASSIGNMENT,
    backlog: MATERIAL,
    result: { keep: { reason: "the question it asks is still open and nobody has had the time" } },
  });

  expect(sink.rows("assessments")).toHaveLength(1);
  expect(sink.rows("records")).toEqual([]);
  expect(sink.rows("plans")).toEqual([]);
  expect(receipt.counts["answer.backlog.keep"]).toBe(1);
});

test("a promotion names the entity, the predicate and the observation it rests on", async () => {
  const { sink } = await launch({
    assignment: BACKLOG_ASSIGNMENT,
    backlog: MATERIAL,
    result: {
      promote: {
        observation: "obs_0123456789abcdef",
        entity: "dev-01",
        predicate: "service-placement",
        value: "publisher",
        reason: "it stays true until the service moves",
      },
    },
  });

  const plan = sink.rows("plans")[0];
  expect(plan?.operation).toBe("promote");
  const payload = JSON.parse(String(plan?.payload));
  expect(payload.promote.entity).toBe("dev-01");
  expect(payload.promote.predicate).toBe("service-placement");
  expect(payload.status).toBe("promoted");
});

test("the backlog prompt carries the material and the receipt carries the draw", async () => {
  const { receipt, promptPath } = await launch({
    assignment: BACKLOG_ASSIGNMENT,
    backlog: MATERIAL,
    result: { keep: { reason: "still open" } },
  });

  const prompt = await Bun.file(promptPath).text();
  expect(prompt).toContain("babel.review.role = backlog");
  expect(prompt).toContain("hyp_beefbeefbeefbeef");
  expect(prompt).toContain("exactly one of five fields");
  expect(receipt.preparation?.["assignment"]).toBe("clm_0001");
  expect(receipt.preparation?.["lane"]).toBe("backlog");
  expect(receipt.preparation?.["fence"]).toBe(4);
  expect(receipt.preparation?.["blindingPolicy"]).toBe("babel.evaluation-blinding/1");
});
