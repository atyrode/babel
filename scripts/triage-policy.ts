#!/usr/bin/env bun
/**
 * The tracker's invariants. `bun scripts/triage-policy.ts [--report|--fix] [--flow]
 * [--summary <path>] [--repo owner/name]`.
 *
 * Rules T1–T6 are the machine-checkable half of `docs/TRIAGE.md` §Label model; that document is
 * their meaning and this script is their proof. Only two of them write: `needs-triage` when an
 * open issue has no state, and `aging` when nobody has said anything for AGING_DAYS. Everything
 * else is reported for whoever runs the triage runbook, because choosing a state, naming a
 * blocker or writing a decision block is judgement, not bookkeeping. Nothing here ever closes
 * an issue.
 *
 * Aging reads comment timestamps, not `updatedAt`: a label edit — including this script's own —
 * bumps `updatedAt`, so an `updatedAt` rule would report every issue as fresh forever.
 */
import { $ } from "bun";

const STATE = ["needs-triage", "needs-operator", "agent-ready", "blocked"] as const;
const PRIORITY = ["p0", "p1", "p2", "p3"] as const;
/** No human activity for this long earns the `aging` signal (docs/TRIAGE.md §Label model). */
const AGING_DAYS = 14;
const DAY_MS = 86_400_000;
const FLOW_DAYS = 7;
const PAGE_LIMIT = 1000;
const DECISION_BLOCK = /^## Decision\b/m;
const NAMED_REFERENCE = /#\d+/;

interface Comment {
  readonly body: string;
  readonly createdAt: string;
  readonly author: string;
}

export interface Issue {
  readonly number: number;
  readonly title: string;
  readonly url: string;
  readonly body: string;
  readonly createdAt: string;
  readonly labels: readonly string[];
  readonly comments: readonly Comment[];
}

export interface Finding {
  readonly rule: string;
  readonly issue: number;
  readonly message: string;
  /** Present only when the rule can repair itself; `--fix` runs it, `--report` never does. */
  readonly repair?: {
    readonly flag: "--add-label" | "--remove-label";
    readonly label: string;
    readonly fixed: string;
  };
}

interface Row {
  readonly rule: string;
  readonly issue: number;
  readonly detail: string;
  readonly fixed: boolean;
}

const args = process.argv.slice(2);

function valueOf(flag: string): string | undefined {
  const index = args.indexOf(flag);
  if (index === -1) return undefined;
  const value = args[index + 1];
  if (value === undefined || value.startsWith("--")) throw new Error(`${flag} needs a value`);
  return value;
}

async function gh(command: readonly string[]): Promise<string> {
  const result = await $`gh ${command}`.quiet().nothrow();
  if (result.exitCode !== 0) {
    throw new Error(result.stderr.toString().trim() || `gh ${command.join(" ")} failed`);
  }
  return result.text();
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error("gh returned a non-object where an object was expected");
  }
  return value as Record<string, unknown>;
}

function text(value: unknown, key: string): string {
  const field = asRecord(value)[key];
  return typeof field === "string" ? field : "";
}

function asArray(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : [];
}

/** `gh` JSON is data from outside the type system; every field is narrowed before use. */
function toIssue(value: unknown): Issue {
  const record = asRecord(value);
  const number = record["number"];
  if (typeof number !== "number") throw new Error("gh returned an issue with no number");
  return {
    number,
    title: text(record, "title"),
    url: text(record, "url"),
    body: text(record, "body"),
    createdAt: text(record, "createdAt"),
    labels: asArray(record["labels"]).map((label) => text(label, "name")),
    comments: asArray(record["comments"]).map((comment) => {
      const author = asRecord(comment)["author"];
      return {
        body: text(comment, "body"),
        createdAt: text(comment, "createdAt"),
        author: author === null || author === undefined ? "" : text(author, "login"),
      };
    }),
  };
}

/** The newest thing a person did: filing it, or saying something on it. */
function lastActivity(issue: Issue): number {
  let newest = Date.parse(issue.createdAt);
  for (const comment of issue.comments) {
    const at = Date.parse(comment.createdAt);
    if (Number.isFinite(at) && at > newest) newest = at;
  }
  return newest;
}

/**
 * The whole rule engine, pure: issues in, findings out. Exported because the live tracker
 * cannot exercise every rule on demand — nothing on it is 14 days quiet today — so T5's
 * boundary is provable only against constructed issues.
 */
export function evaluate(issues: readonly Issue[], now: number): readonly Finding[] {
  const findings: Finding[] = [];
  for (const issue of issues) {
    const states = STATE.filter((label) => issue.labels.includes(label));
    const priorities = PRIORITY.filter((label) => issue.labels.includes(label));
    const tracking = issue.labels.includes("tracking");

    // T1 — every open issue is in exactly one state, so the tracker is partitioned, not a bag.
    if (!tracking) {
      if (states.length === 0) {
        findings.push({
          rule: "T1",
          issue: issue.number,
          message: "no state label",
          repair: { flag: "--add-label", label: "needs-triage", fixed: "added needs-triage" },
        });
      } else if (states.length > 1) {
        findings.push({
          rule: "T1",
          issue: issue.number,
          message: `multiple state labels: ${states.join(", ")}`,
        });
      }
    }

    // T2 — "ready" means an agent can pick it up knowing where it lands and how urgent it is.
    if (issue.labels.includes("agent-ready")) {
      if (priorities.length === 0) {
        findings.push({ rule: "T2", issue: issue.number, message: "agent-ready without priority" });
      }
      const exempt = issue.labels.includes("documentation") || issue.labels.includes("process");
      if (!exempt && !issue.labels.some((label) => label.startsWith("area:"))) {
        findings.push({ rule: "T2", issue: issue.number, message: "agent-ready without area" });
      }
    }

    // T3 — a blocker nobody named is indistinguishable from an abandoned issue. The name may
    // arrive later than the body, because a blocker usually does, so a comment counts.
    if (
      issue.labels.includes("blocked") &&
      !NAMED_REFERENCE.test(issue.body) &&
      !issue.comments.some((comment) => NAMED_REFERENCE.test(comment.body))
    ) {
      findings.push({
        rule: "T3",
        issue: issue.number,
        message: "blocked without a named issue/PR",
      });
    }

    // T4 — a hold without a written question cannot be answered, so it never leaves the tracker.
    if (
      issue.labels.includes("needs-operator") &&
      !DECISION_BLOCK.test(issue.body) &&
      !issue.comments.some((comment) => DECISION_BLOCK.test(comment.body))
    ) {
      findings.push({
        rule: "T4",
        issue: issue.number,
        message: "needs-operator without ## Decision block",
      });
    }

    // T5 — a signal, never a closer: silence is worth seeing, and speaking clears it.
    if (!tracking && !issue.labels.includes("blocked")) {
      const quiet = now - lastActivity(issue) > AGING_DAYS * DAY_MS;
      const labelled = issue.labels.includes("aging");
      if (quiet && !labelled) {
        findings.push({
          rule: "T5",
          issue: issue.number,
          message: "aging label missing",
          repair: { flag: "--add-label", label: "aging", fixed: "added aging" },
        });
      } else if (!quiet && labelled) {
        findings.push({
          rule: "T5",
          issue: issue.number,
          message: "aging label stale",
          repair: { flag: "--remove-label", label: "aging", fixed: "removed aging" },
        });
      }
    }

    // T6 — two priorities is no priority.
    if (priorities.length > 1) {
      findings.push({
        rule: "T6",
        issue: issue.number,
        message: `multiple priorities: ${priorities.join(", ")}`,
      });
    }
  }
  return findings.sort((a, b) => a.rule.localeCompare(b.rule) || a.issue - b.issue);
}

function renderRows(rows: readonly Row[]): string {
  if (rows.length === 0) return "";
  return [
    "| rule | issue | detail |",
    "| --- | --- | --- |",
    ...rows.map((row) => `| ${row.rule} | #${String(row.issue)} | ${row.detail} |`),
    "",
  ].join("\n");
}

/** Opened/closed per day, the open tracker by state, and how long the queue's tail has waited. */
async function renderFlow(
  open: readonly Issue[],
  now: number,
  repoArgs: readonly string[],
): Promise<string> {
  const all = asArray(
    JSON.parse(
      await gh([
        "issue",
        "list",
        ...repoArgs,
        "--state",
        "all",
        "--limit",
        String(PAGE_LIMIT),
        "--json",
        "number,createdAt,closedAt,labels",
      ]),
    ),
  );
  const lines = ["## Flow", "", "| day (UTC) | opened | closed |", "| --- | --- | --- |"];
  for (let back = FLOW_DAYS - 1; back >= 0; back--) {
    const day = new Date(now - back * DAY_MS).toISOString().slice(0, 10);
    const opened = all.filter((issue) => text(issue, "createdAt").startsWith(day)).length;
    const closed = all.filter((issue) => text(issue, "closedAt").startsWith(day)).length;
    lines.push(`| ${day} | ${String(opened)} | ${String(closed)} |`);
  }
  lines.push("", "| open by state | count |", "| --- | --- |");
  for (const label of [...STATE, "tracking", "aging"]) {
    const count = open.filter((issue) => issue.labels.includes(label)).length;
    lines.push(`| ${label} | ${String(count)} |`);
  }
  const oldest = open
    .filter((issue) => issue.labels.includes("needs-triage"))
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))[0];
  lines.push(
    "",
    oldest === undefined
      ? "Oldest needs-triage: none"
      : `Oldest needs-triage: #${String(oldest.number)}, ${String(Math.floor((now - Date.parse(oldest.createdAt)) / DAY_MS))}d`,
  );
  if (all.length >= PAGE_LIMIT) {
    lines.push(
      `Truncated: the history fetch hit ${String(PAGE_LIMIT)} issues; counts are partial.`,
    );
  }
  lines.push("");
  return lines.join("\n");
}

if (import.meta.main) {
  const fix = args.includes("--fix");
  const flow = args.includes("--flow");
  const repo = valueOf("--repo");
  const summaryPath = valueOf("--summary");
  const repoArgs = repo === undefined ? [] : ["--repo", repo];

  const issues = asArray(
    JSON.parse(
      await gh([
        "issue",
        "list",
        ...repoArgs,
        "--state",
        "open",
        "--limit",
        String(PAGE_LIMIT),
        "--json",
        "number,title,labels,body,createdAt,updatedAt,comments,url",
      ]),
    ),
  ).map(toIssue);

  const now = Date.now();
  const rows: Row[] = [];
  for (const finding of evaluate(issues, now)) {
    const { repair } = finding;
    if (fix && repair !== undefined) {
      await gh(["issue", "edit", String(finding.issue), ...repoArgs, repair.flag, repair.label]);
      rows.push({
        rule: finding.rule,
        issue: finding.issue,
        detail: `fixed: ${repair.fixed}`,
        fixed: true,
      });
      continue;
    }
    rows.push({ rule: finding.rule, issue: finding.issue, detail: finding.message, fixed: false });
  }

  const violations = rows.filter((row) => !row.fixed).length;
  const summary = `Triage policy: ${String(violations)} violations, ${String(rows.length - violations)} fixes, ${String(issues.length)} open issues`;
  const table = renderRows(rows);
  const flowText = flow ? await renderFlow(issues, now, repoArgs) : "";

  if (table !== "") process.stdout.write(`${table}\n`);
  console.log(summary);
  if (flowText !== "") process.stdout.write(`\n${flowText}`);

  if (summaryPath !== undefined) {
    const file = Bun.file(summaryPath);
    const existing = (await file.exists()) ? await file.text() : "";
    const appended = flowText === "" ? "" : `\n${flowText}`;
    await Bun.write(summaryPath, `${existing}${table}\n${summary}\n${appended}`);
  }

  process.exit(violations > 0 ? 1 : 0);
}
