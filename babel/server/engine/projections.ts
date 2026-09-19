import type { Projection } from "../../contract.ts";

/*
  §4.6'S OUTPUT PROJECTIONS: a record rendered for a destination (#341).

  Nothing in Babel could render a record out of Babel. §4.6 specifies the destinations and
  nothing built them, which is what "Babel drafts; the operator acts" rests on: a proposal the
  operator cannot carry anywhere is a proposal he has to retype.

  THERE IS NO PUBLISH VERB HERE, IN ANY SPELLING. A projection is text and a filename. It opens
  no issue, writes into no repository, posts nothing and launches no agent at a destination. The
  door that serves it (`doors/export.ts`) asks for `containers:read` and delegates nothing, so
  the authority to reach anything outside this plugin's own rows is not held rather than merely
  unused. §4.6 exists to refuse exactly the shape that would make this a publisher.

  RENDERING NEVER CHANGES THE CANONICAL RECORD. Every function here is pure: rows in, text out.

  REDACTION IS BY THE RECORD'S CLASSIFICATION, AND §4.6 IS WHAT DECIDES WHERE IT BITES. Of the
  three destinations it names, one leaves this deployment and two do not, and the section says
  which: the issue draft is the one it calls SANITIZED, while the agent brief is the one it
  requires to carry "evidence locators an agent can open rather than excerpts it must trust" —
  an agent Babel's operator runs, on his own machines, reading his own transcripts. An operator
  note is for the operator. So a classification governs the issue draft and governs nothing
  else, and that is one rule rather than a table of three.
*/

/** One session a record cites, as the catalog holds it. */
export interface ExportSession {
  readonly selector: string;
  readonly title: string;
  readonly workspace: string;
  readonly digest: string;
}

/** One record as a projection reads it: its own columns, its payload, and what it cites. */
export interface ExportRecord {
  readonly id: string;
  readonly kind: string;
  readonly rootId: string;
  readonly seq: number;
  readonly title: string;
  readonly createdAt: string;
  readonly runId: string;
  /** The revision this one replaced, and "" when it is the first. */
  readonly supersedesId: string;
  /** The revision that replaced this one, and "" when it is the newest. */
  readonly supersededById: string;
  /** The newest ruling, and "" when nobody has ruled. */
  readonly standing: string;
  readonly payload: Record<string, unknown>;
  readonly sessions: readonly ExportSession[];
}

/** A rendered projection: the file's name, its bytes, and what the classification kept out. */
export interface Projected {
  readonly classification: string;
  readonly filename: string;
  readonly withheld: readonly string[];
  readonly text: string;
}

/**
 * WHICH DESTINATIONS LEAVE THIS DEPLOYMENT. It is the whole input to the redaction decision, so
 * it is a table of its own: a fourth destination added later has to answer this question before
 * it can be rendered at all, rather than inheriting whichever default was convenient.
 */
const LEAVES: Record<Projection, boolean> = {
  "issue-draft": true,
  "agent-brief": false,
  "operator-note": false,
};

/**
 * WHICH PROJECTIONS ARE A PROPOSAL'S ALONE. §4.6 renders a PROPOSAL for a destination: a problem,
 * a proposed outcome and acceptance criteria are a proposal's fields, and an issue draft of a
 * hypothesis would be a change request assembled out of a guess. The operator note is the
 * exception because it has no destination: it is the record written out for the person who holds
 * it, whichever kind it is.
 */
const PROPOSAL_ONLY: Record<Projection, boolean> = {
  "issue-draft": true,
  "agent-brief": true,
  "operator-note": false,
};

/** How a destination is named in a sentence to the operator, since the keys are not prose. */
const DESTINATION: Record<Projection, string> = {
  "issue-draft": "an issue draft",
  "agent-brief": "an agent brief",
  "operator-note": "an operator note",
};

/**
 * What the classification keeps out of this projection.
 *
 * `refuse` is the DEFAULT rather than a case, and that is the safety of the feature: a record
 * whose classification is `private`, absent, or a word this build has never heard is not rendered
 * for a destination outside Babel. An unrecognized classification treated as publishable would
 * be the one bug here that cannot be taken back.
 */
function redactionOf(
  classification: string,
  projection: Projection,
): "none" | "evidence" | "refuse" {
  if (!LEAVES[projection]) return "none";
  if (classification === "public-safe") return "none";
  if (classification === "redaction-required") return "evidence";
  return "refuse";
}

/** One citation as the record's payload carries it (`machine/results.ts`'s `EvidenceSchema`). */
interface Citation {
  readonly path: string;
  readonly line: number;
  readonly digest: string;
  readonly note: string;
  readonly counter: boolean;
}

/**
 * The record's citations, supporting before conflicting, read leniently.
 *
 * The keys are the kind's own, which is the same reading `store/store.ts`'s peel makes, and the
 * fields are taken rather than parsed for the same reason it takes them: two thirds of this
 * deployment's corpus crossed from the Go tree and its payloads were never validated by this
 * contract, so a strict parse would export an imported record with its evidence missing — which
 * is worse than exporting a citation whose byte offset nobody can use.
 *
 * A FINDING CARRIES ONLY ITS COUNTER-EVIDENCE, and that is the store's shape rather than an
 * omission here: §4.4 makes a finding a consolidation, so its evidence is the observations it
 * consolidates and its own payload states only what argues against it.
 */
function citationsOf(record: ExportRecord): readonly Citation[] {
  const out: Citation[] = [];
  const take = (key: string, counter: boolean): void => {
    const held = record.payload[key];
    if (!Array.isArray(held)) return;
    for (const item of held) {
      if (typeof item !== "object" || item === null) continue;
      const row = item as Record<string, unknown>;
      const locator =
        typeof row["locator"] === "object" && row["locator"] !== null
          ? (row["locator"] as Record<string, unknown>)
          : {};
      const path = typeof locator["path"] === "string" ? locator["path"] : "";
      // §4.3 makes evidence inseparable from its locator, so a note with nothing to open is not
      // a citation and is not rendered as one.
      if (path === "") continue;
      out.push({
        path,
        line: typeof locator["line"] === "number" ? locator["line"] : 0,
        digest: typeof locator["digest"] === "string" ? locator["digest"] : "",
        note: typeof row["note"] === "string" ? row["note"] : "",
        counter,
      });
    }
  };
  if (record.kind === "observation") {
    take("evidence", false);
    take("counter_evidence", true);
  } else if (record.kind === "finding") {
    take("counter_evidence", true);
  } else if (record.kind === "proposal") {
    take("supporting", false);
    take("conflicting", true);
  }
  return out;
}

/** A string field of the payload, or "" where the kind has no answer for it. */
function field(record: ExportRecord, key: string): string {
  const held = record.payload[key];
  return typeof held === "string" ? held.trim() : "";
}

/** A list field of the payload, empty entries dropped. */
function list(record: ExportRecord, key: string): readonly string[] {
  const held = record.payload[key];
  if (!Array.isArray(held)) return [];
  const out: string[] = [];
  for (const item of held) {
    if (typeof item === "string" && item.trim() !== "") out.push(item.trim());
  }
  return out;
}

/** A heading and its lines, or nothing at all: an empty section reads as a record that is thin. */
function section(heading: string, lines: readonly string[]): readonly string[] {
  return lines.length === 0 ? [] : [`## ${heading}`, "", ...lines, ""];
}

/**
 * One citation as a line an agent can act on: the locator, then the session the catalog resolved
 * it to. The digest is there because it is what proves the bytes have not moved — a locator
 * without it is a path that may now say something else.
 */
function citationLines(
  citations: readonly Citation[],
  sessions: readonly ExportSession[],
  withWorkspace: boolean,
): readonly string[] {
  const byDigest = new Map<string, ExportSession>();
  for (const session of sessions) byDigest.set(session.digest, session);
  const out: string[] = [];
  for (const citation of citations) {
    const place = citation.line > 0 ? `${citation.path}:${String(citation.line)}` : citation.path;
    const side = citation.counter ? "counter-evidence · " : "";
    out.push(`- ${side}\`${place}\`${citation.note === "" ? "" : ` — ${citation.note}`}`);
    const session = byDigest.get(citation.digest);
    if (session !== undefined) {
      const where = withWorkspace && session.workspace !== "" ? `, in ${session.workspace}` : "";
      out.push(`  session \`${session.selector}\`${where}`);
    }
    if (citation.digest !== "") out.push(`  digest \`${citation.digest}\``);
  }
  return out;
}

/** The revision chain in one line, which is the lineage a reader needs and no more. */
function lineage(record: ExportRecord): string {
  const parts: string[] = [`revision ${String(record.seq)} of ${record.rootId}`];
  if (record.supersedesId !== "") parts.push(`supersedes ${record.supersedesId}`);
  if (record.supersededById !== "") parts.push(`superseded by ${record.supersededById}`);
  return parts.join(", ");
}

/**
 * ONE RECORD RENDERED FOR ONE DESTINATION, or the refusal that stops it.
 *
 * The refusals are the section's own two rules: §4.6 renders a proposal, so a destination that
 * asks for a problem and acceptance criteria is refused for a kind that has neither, and a
 * classification that does not permit a record to leave refuses the one destination that would
 * take it there. Neither is a failure — both are answers to the operator, naming what stopped.
 */
export function projectRecord(
  record: ExportRecord,
  projection: Projection,
): Projected | { readonly refused: string } {
  const classification = field(record, "classification");
  if (PROPOSAL_ONLY[projection] && record.kind !== "proposal") {
    return {
      refused:
        `${record.id} is a ${record.kind}, and ${DESTINATION[projection]} states a problem, a ` +
        "proposed outcome and acceptance criteria — which are a proposal's; export it as an " +
        "operator note",
    };
  }
  const redaction = redactionOf(classification, projection);
  if (redaction === "refuse") {
    return {
      refused:
        `${record.id} is classified ${classification === "" ? "nothing at all" : classification}` +
        `, and ${DESTINATION[projection]} leaves this deployment; only a public-safe record is ` +
        "rendered whole for one, and a redaction-required record is rendered without its evidence",
    };
  }
  const citations = citationsOf(record);
  const cited = citations.length;
  const withheld: string[] = [];
  if (redaction === "evidence" && cited > 0) {
    withheld.push(
      `${String(cited)} evidence ${cited === 1 ? "locator" : "locators"}, withheld because ` +
        "this record is classified redaction-required",
    );
  }
  const body =
    projection === "issue-draft"
      ? issueDraft(record, citations, redaction)
      : projection === "agent-brief"
        ? agentBrief(record, citations)
        : operatorNote(record, citations);
  return {
    classification,
    filename: `${projection}-${record.id}.md`,
    withheld,
    text: `${body
      .join("\n")
      .replace(/\n{3,}/gu, "\n\n")
      .trimEnd()}\n`,
  };
}

/**
 * The sanitized issue draft: what the change is, what it would achieve, how it would be checked,
 * and — where the classification permits it — what it rests on.
 *
 * It carries no identifier of this deployment beyond the record's own: no run, no machine, no
 * workspace path. A draft is read by whoever the operator shows it to, and a filesystem path on
 * his machine is not part of the argument.
 */
function issueDraft(
  record: ExportRecord,
  citations: readonly Citation[],
  redaction: "none" | "evidence",
): readonly string[] {
  const problem = field(record, "problem");
  const outcome = field(record, "outcome");
  return [
    `# ${record.title}`,
    "",
    ...(problem === "" ? [] : [problem, ""]),
    ...section("What it would achieve", outcome === "" ? [] : [outcome]),
    ...section(
      "How it would be checked",
      list(record, "verification_criteria").map((line) => `- ${line}`),
    ),
    ...section(
      "Open questions",
      list(record, "open_questions").map((line) => `- ${line}`),
    ),
    ...section(
      "Evidence",
      redaction === "evidence"
        ? citations.length === 0
          ? []
          : [
              `${String(citations.length)} citations are withheld: this record is classified ` +
                "redaction-required, and the locators name private conversations.",
            ]
        : citationLines(citations, record.sessions, false),
    ),
    "---",
    "",
    `Drafted by Babel from ${record.id}. Nothing here has been published or filed anywhere.`,
  ];
}

/**
 * The agent brief, in §4.6's own terms: the problem, the proposed outcome, acceptance criteria,
 * and evidence locators an agent can open rather than excerpts it must trust.
 *
 * It carries no reception — no tally, no rank, no vote. An agent told that Babel's reviewers
 * liked a proposal is being asked to agree with them, and what it is being asked for is whether
 * the evidence holds.
 */
function agentBrief(record: ExportRecord, citations: readonly Citation[]): readonly string[] {
  const problem = field(record, "problem");
  const outcome = field(record, "outcome");
  return [
    `# ${record.title}`,
    "",
    ...section("Problem", problem === "" ? [] : [problem]),
    ...section("Proposed outcome", outcome === "" ? [] : [outcome]),
    ...section(
      "Acceptance criteria",
      list(record, "verification_criteria").map((line) => `- ${line}`),
    ),
    ...section(
      "Prerequisites",
      list(record, "prerequisites").map((line) => `- ${line}`),
    ),
    ...section(
      "Risks",
      list(record, "risks").map((line) => `- ${line}`),
    ),
    ...section(
      "Open questions",
      list(record, "open_questions").map((line) => `- ${line}`),
    ),
    ...section("Evidence to open", citationLines(citations, record.sessions, true)),
    "---",
    "",
    `${record.id}, ${lineage(record)}, written ${record.createdAt} by ${record.runId}.`,
    "Open the locators; an excerpt is not evidence. Babel proposes, and this is not authority",
    "to implement anything.",
  ];
}

/**
 * The operator note: the record as the person who holds it reads it — the claim, the case, where
 * it stands, its revision chain and everything it cites.
 *
 * It has no destination, so nothing is withheld from it. That is not a gap in the redaction: a
 * classification says what may LEAVE, and this projection does not go anywhere.
 */
function operatorNote(record: ExportRecord, citations: readonly Citation[]): readonly string[] {
  const claim =
    field(record, "outcome") ||
    field(record, "pattern") ||
    field(record, "claim") ||
    field(record, "statement");
  const labels: readonly (readonly [string, string])[] = [
    ["problem", field(record, "problem")],
    ["impact", field(record, "impact")],
    ["confidence", field(record, "confidence")],
    ["scope", [...list(record, "scope")].join(", ") || field(record, "estimated_scope")],
    ["classification", field(record, "classification")],
    ["uncertainty", field(record, "uncertainty")],
  ];
  return [
    `# ${record.title}`,
    "",
    ...(claim === "" ? [] : [claim, ""]),
    ...section(
      "The case",
      labels.filter(([, value]) => value !== "").map(([label, value]) => `- ${label}: ${value}`),
    ),
    ...section("Standing", [
      record.standing === "" ? "nothing has been ruled on this." : `ruled ${record.standing}.`,
    ]),
    ...section("Lineage", [lineage(record)]),
    ...section(
      "Risks",
      list(record, "risks").map((line) => `- ${line}`),
    ),
    ...section(
      "Open questions",
      list(record, "open_questions").map((line) => `- ${line}`),
    ),
    ...section("Evidence", citationLines(citations, record.sessions, true)),
    "---",
    "",
    `${record.id}, a ${record.kind} written ${record.createdAt} by ${record.runId}.`,
  ];
}
