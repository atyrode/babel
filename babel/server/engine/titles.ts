import type { SqlStatement } from "@manifold/plugin";
import { z } from "zod";

import {
  MATERIAL_INDEX,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  type TitleProvenance,
} from "../../contract.ts";
import { MAX_TITLE_RUNES, boundedTitle } from "../../machine/adapters/codex-title.ts";
import { REFUSALS } from "../../machine/results.ts";
import { ANSWER_FENCE, answerOf } from "./prompts.ts";

/*
  NAMING THE SESSIONS WHOSE OWN LOGS CARRY NO TITLE (#342).

  `sessions.title` holds whatever the adapter could read: a title the harness wrote
  ("recorded"), or one this tree computed offline from the log's own values ("derived"). A
  Codex thread opened by a front end that records neither leaves the column NULL for ever, and
  a cross-host listing of several hundred of those reads as a column of opaque selectors.

  THERE IS ONE WAY TO REACH A MODEL AND THIS USES IT. Babel binds no model service, holds no
  credential and has no provider client: a run that reaches a model is a CODE SESSION, posted
  by `atyrode.code.runSession` over a profile the operator configured (#279,
  `server/engine/session.ts`). A titler with a second road to a model would be a second spend
  nobody meters and Babel's first credential, which is exactly what the Go product refused to
  build for this same feature ("It is also not a direct provider call from inside Babel").

  SO A TITLING RUN IS AN ORDINARY BABEL RUN IN EVERY RESPECT BUT ITS PROMPT. It selects
  sessions from the catalog, seals them with one `atyrode.babel.prepare` job — the only way
  session text reaches a sandbox at all — posts a Code session with that material bound, and
  settles into a receipt. What is different is only what it is asked for and what it writes:
  one line per session, into `session_titles`, and never a record.

  THE ANSWER IS PER SESSION AND A DECLINE IS NOT A FAILURE. One unreadable log must not waste
  the tokens every other session in the batch was read with, so the contract gives the model a
  per-entry `error` and Babel records that as the session's answer. What IS refused outright is
  a reply naming a session this run never offered: an answer about bytes nobody served is the
  same fault `unservedLocator` refuses on the exploration path, and admitting one would let a
  model write a title onto any row in the catalog.
*/

/** The prompt this module composes, recorded in every titling receipt (§7). */
export const TITLE_PROMPT_VERSION = "babel.title-prompt/1";

/**
 * HOW MANY SESSIONS ONE TITLING RUN NAMES.
 *
 * Twenty, which is the bound the Go path defaulted to for the same work and for the same
 * reason: everything about this lane is meant to be readable before it runs, and a batch an
 * operator cannot finish reading in the run row is not a disclosure. It also keeps the sealed
 * material small — twenty logs against a preparation that may hold 448 MiB — and the prompt
 * far inside Code's own byte bound, so the run fails on nothing but the model.
 */
export const MAX_TITLE_BATCH = 20;

/**
 * THE WORD THIS LANE WRITES INTO `sessions.title_provenance`.
 *
 * The vocabulary is the contract's `TITLE_PROVENANCES`, and the third of its three words is one
 * no machine row carries: the contract's session row refuses it, because a model summary is a
 * guess that cost money. This lane is its one writer.
 */
export const TITLE_INFERRED: TitleProvenance = "inferred";

/** One session a titling run was offered, as the prompt names it. */
export interface TitleSubject {
  readonly selector: string;
  /** The file inside the sealed material's `sessions/` directory, from the preparation's index. */
  readonly file: string;
}

/** One session's answer: the model's title, or the reason there is none. Never both. */
export interface InferredTitle {
  readonly selector: string;
  readonly title: string;
  readonly reason: string;
}

/**
 * THE ONE PLACE AN INFERRED TITLE IS WRITTEN, as statements for the caller's own batch.
 *
 * Two writers reach it and they are two different endings of one run: the conductor's
 * settlement, when a session answered, and the poster's, when the run closed before a session
 * ever existed. Both write the same shape because both are answering the same question for
 * the same selectors, and a second spelling of it is how a lane that is supposed to ask once
 * comes to ask twice.
 *
 * `ON CONFLICT DO NOTHING` makes "the first answer stands" a property of the statement rather
 * than of the order two paths happened to run in, and it is what makes a replayed settlement
 * a no-op. Re-inferring a session is therefore an operator act — delete the row — which is
 * the ceremony the retired product spelled `babel sessions title clear`.
 *
 * THE CATALOG IS ONLY OVERLAID WHERE IT IS STILL BLANK. `WHERE title IS NULL` is the whole of
 * "a read title always wins": between this run's press and its answer a preparation may have
 * read a title the harness itself recorded, and that one is the session's own word about itself
 * where this is a guess that cost money. The `session_titles` row is written either way, so
 * what was paid for stays readable even when it is not what the listing shows.
 */
export function titleStatements(input: {
  readonly runId: string;
  readonly at: string;
  readonly titles: readonly InferredTitle[];
}): readonly SqlStatement[] {
  const statements: SqlStatement[] = [];
  for (const answer of input.titles) {
    statements.push({
      sql:
        `INSERT INTO session_titles(selector, title, reason, run_id, inferred_at) ` +
        `VALUES (?, ?, ?, ?, ?) ON CONFLICT(selector) DO NOTHING`,
      params: [answer.selector, answer.title, answer.reason, input.runId, input.at],
    });
    if (answer.title === "") continue;
    statements.push({
      sql: `UPDATE sessions SET title = ?, title_provenance = ? WHERE selector = ? AND title IS NULL`,
      params: [answer.title, TITLE_INFERRED, answer.selector],
    });
  }
  return statements;
}

/**
 * Every offered session answered, given what the material actually sealed and what the run
 * never got as far as asking. It is the failure half of {@link readTitleAnswer}: a run whose
 * preparation failed, whose Code session was refused or whose answer was refused outright
 * still answers every selector it named, because a selector with no row is one the next cycle
 * offers again.
 */
export function declinedTitles(
  selectors: readonly string[],
  reason: string,
): readonly InferredTitle[] {
  return selectors.map((selector) => ({ selector, title: "", reason }));
}

/** The selectors a titling run's own document named, or none when the row carries no list. */
export function offeredSelectors(preparation: Record<string, unknown> | undefined): string[] {
  const titles = preparation?.["titles"];
  if (typeof titles !== "object" || titles === null || Array.isArray(titles)) return [];
  const named = (titles as Record<string, unknown>)["selectors"];
  if (!Array.isArray(named)) return [];
  return named.filter((entry): entry is string => typeof entry === "string" && entry !== "");
}

/**
 * What the model returns. `title` and `error` are both optional so one unreadable session can
 * be declined without failing the batch; an entry carrying neither is a decline with no reason
 * given, which is recorded as such rather than treated as a title.
 */
const TitleAnswerSchema = z.strictObject({
  titles: z
    .array(
      z.strictObject({
        selector: z.string().trim().min(1).max(400),
        title: z.string().max(4_000).default(""),
        error: z.string().max(1_000).default(""),
      }),
    )
    .max(MAX_TITLE_BATCH),
});

/** The JSON Schema the prompt carries, so the contract is stated where it is answered. */
const ANSWER_SCHEMA = {
  type: "object",
  required: ["titles"],
  additionalProperties: false,
  properties: {
    titles: {
      type: "array",
      items: {
        type: "object",
        required: ["selector"],
        additionalProperties: false,
        properties: {
          selector: { type: "string", description: "the session's selector, copied exactly" },
          title: { type: "string", description: "one line naming what the session was for" },
          error: { type: "string", description: "why this session cannot be named" },
        },
      },
    },
  },
} as const;

/**
 * ONE ANSWER PER SESSION THE RUN WAS OFFERED, or the sentence refusing the whole submission.
 *
 * A selector the answer never mentions is not silently dropped: it comes back with the reason
 * saying so, because the caller records an answer for every session it offered and a session
 * with no row would be offered again on the next cycle, for ever.
 */
export function readTitleAnswer(
  finalMessage: string,
  offered: readonly string[],
): { readonly titles: readonly InferredTitle[] } | { readonly refused: string } {
  const answer = answerOf(finalMessage);
  if ("refused" in answer) return { refused: `${REFUSALS.schema}: ${answer.refused}` };
  let payload: unknown;
  try {
    payload = JSON.parse(answer.json);
  } catch (error) {
    const said = error instanceof Error ? error.message : String(error);
    return { refused: `${REFUSALS.schema}: the ${ANSWER_FENCE} block is not JSON: ${said}` };
  }
  const parsed = TitleAnswerSchema.safeParse(payload);
  if (!parsed.success) {
    const issue = parsed.error.issues[0];
    const where = issue === undefined ? "" : `${issue.path.join(".")}: ${issue.message}`;
    return { refused: `${REFUSALS.schema}: the answer is not the title contract (${where})` };
  }

  const wanted = new Set(offered);
  const answered = new Map<string, InferredTitle>();
  for (const entry of parsed.data.titles) {
    if (!wanted.has(entry.selector)) {
      return {
        refused:
          `${REFUSALS.unknownReference}: the answer names ${entry.selector}, which this run ` +
          `was not offered; a title may only be written onto a session the run was served`,
      };
    }
    if (answered.has(entry.selector)) {
      return {
        refused: `${REFUSALS.schema}: the answer names ${entry.selector} twice`,
      };
    }
    const title = boundedTitle(entry.title);
    answered.set(entry.selector, {
      selector: entry.selector,
      title,
      reason:
        title !== ""
          ? ""
          : entry.error.trim() !== ""
            ? entry.error.trim()
            : "the run returned no usable title for this session",
    });
  }
  return {
    titles: offered.map(
      (selector) =>
        answered.get(selector) ?? {
          selector,
          title: "",
          reason: "the run's answer said nothing about this session",
        },
    ),
  };
}

export interface TitlePromptInput {
  readonly subjects: readonly TitleSubject[];
  readonly preparationId: string;
  readonly params: Readonly<Record<string, string>>;
}

/**
 * The whole of what a titling session is told. It is deliberately short: the job is one
 * summarizing judgement per file, the material section is the same description an exploration
 * gets because it is the same mount, and there is no cookbook, no evidence contract and no
 * record vocabulary — none of which a title needs, and every one of which would invite the
 * model to produce something this lane refuses to write.
 */
export function composeTitlePrompt(input: TitlePromptInput): string {
  const parts: string[] = ["# Babel session titles\n\n"];
  parts.push(
    "Each session below is a transcript whose own log records no title. Read enough of each ",
    "one to say what it was for, and answer with one line per session.\n\n",
  );

  parts.push("## What a title is\n\n");
  parts.push(
    "One line, in the operator's own terms, naming the work: what was being built, read, ",
    "fixed or decided. Sentence case, no trailing period, no quotation marks. Name the ",
    "subject rather than the activity: prefer 'Restic archive retention on dev-01' to ",
    "'A conversation about backups', and never describe the transcript itself. Keep it under ",
    `${String(MAX_TITLE_RUNES)} characters; Babel cuts a longer one at a word boundary.\n\n`,
  );
  parts.push(
    "A session you cannot name is an `error` entry and not a guess. A title invented from a ",
    "log you could not read is worse than the selector it replaces, because a reader cannot ",
    "tell the two apart.\n\n",
  );

  parts.push("## How to answer\n\n");
  parts.push(
    `End your last message with one \`${ANSWER_FENCE}\` fenced block holding the complete `,
    "answer and nothing after the closing fence. The block is the answer: there is no tool to ",
    "call and no other channel. If you write more than one such block, the last one is taken.\n\n",
  );
  parts.push(
    "Copy each `selector` exactly as it is listed below. An entry naming a session that is ",
    "not in this run's list refuses the whole answer, and every session you leave out is ",
    "recorded as unnamed.\n\n",
  );
  parts.push(`${ANSWER_FENCE}\n`, `${JSON.stringify(ANSWER_SCHEMA, null, 2)}\n`, "```\n\n");

  parts.push(
    "## Parameters\n\n",
    "Every parameter this run carries, one per line.\n\n",
    paramsBlock(input.params),
  );

  parts.push("## The material\n\n");
  parts.push(
    `Everything this run may read is at \`${MATERIAL_ROOT}\`, mounted read-only. Read it with `,
    "your own file tools; there is nothing else to search and no network to fetch from. ",
    `\`${MATERIAL_INDEX}\` names the selection and \`${MATERIAL_SESSIONS}/<file>\` holds one `,
    "session's records, one canonical JSON record per line, in the order the harness wrote ",
    `them. The preparation is \`${input.preparationId}\` and it is immutable.\n\n`,
  );
  parts.push(
    "A record may carry `[[babel-redacted:<class>@<line>:<offset>+<length>]]` where a likely ",
    "credential was. Those bytes are not available to this run at any path. Infer nothing ",
    "about what a marker contained, and never put one in a title.\n\n",
  );
  for (const subject of input.subjects) {
    parts.push(`- ${subject.selector} — \`${MATERIAL_SESSIONS}/${subject.file}\`\n`);
  }
  parts.push("\n");
  return parts.join("");
}

function paramsBlock(params: Readonly<Record<string, string>>): string {
  const keys = Object.keys(params).sort();
  const lines = keys.map((key) => `${key} = ${params[key] ?? ""}`);
  return `[babel-params]\n${lines.join("\n")}${lines.length === 0 ? "" : "\n"}[end]\n\n`;
}
