import { NEXT_ACTIONS, RecordKindSchema } from "../../contract.ts";
import {
  admits,
  AdvisorySchema,
  BankDocumentSchema,
  ExemplarSchema,
  ROUTING_QUESTIONS,
  VOTERS,
  type Advisory,
  type BankDocument,
  type Condition,
  type Distribution,
  type Exemplar,
  type Question,
  type Routing,
  type Vote,
} from "./schema.ts";

/*
  READING A BANK DOCUMENT, AND REFUSING ONE THAT CANNOT BE TRUSTED.

  This is the dev-time half: `tools/seed-questions.ts` runs it over `bank/questions/*.md` and
  writes `bank/questions.seed.json`, which is the only thing the runtime reads. It is the same
  shape as `babel/tools/seed-recipes.ts` and for the same reason — a document declares
  its version, `versions.json` records it, an assessment cites it, and a document seeded under a
  number the manifest does not record would make every citation of it name something nobody can
  read back.

  The refusals are the substance. Each is a way a bank can be wrong that nothing downstream
  could notice:

  - A THRESHOLD WITH NO OBSERVED DISTRIBUTION is refused by name. This is the rule the case
    study states and the one worth enforcing mechanically: a threshold copied from a vendor's
    documentation is fitted to somebody else's corpus, and the only defence against one arriving
    is that a row without its own `n=` and `fires=` does not parse. A numeric threshold must
    also carry `mean=` and `sd=`; a categorical one has no mean to carry.
  - A ROUTING QUESTION IN THE THRESHOLDS BLOCK is refused, and so is one in the advisories
    block, and so is anything else in the routing block. The type system already makes a routing
    question untalliable and an advisory untalliable — neither `Routing` nor `Advisory` has
    `casts`, so `tally()` will not take either — and this closes the ways round it, which are
    writing the routing question as a vote, or as a suggestion, in the first place.
  - AN ADVISORY PROPOSING A WORD THAT IS NOT A NEXT ACTION is refused. `babel.suggest` writes a
    `next_actions` row and that vocabulary is closed, so a suggestion the door would reject is
    better caught in the document than at the call that was paid for.
  - A QUESTION DEFINED AND NEVER ASKED, or asked and never defined, is refused. The definitions
    are what an operator renders into the service policy's literals; one no row reaches is text
    going out that nothing reads back.
  - AN EXEMPLAR CLAIMING A RULING WITHOUT ONE is refused. An exemplar teaches the operator's
    taste, so an unruled record carried as though he had ruled on it teaches the panel its own
    opinion back.
*/

/** The kind a record id's family belongs to, which an exemplar's id has to agree with. */
const KIND_BY_PREFIX: Record<string, string> = {
  hyp: "hypothesis",
  obs: "observation",
  fnd: "finding",
  pro: "proposal",
};

/** The frontmatter block and the prose under it, or a refusal naming the file. */
function split(file: string, text: string): { front: string; body: string } {
  if (!text.startsWith("---\n")) {
    throw new Error(`${file}: no frontmatter block, so the document declares no kind or version`);
  }
  const close = text.indexOf("\n---\n", 3);
  if (close === -1) throw new Error(`${file}: the frontmatter block is never closed`);
  return { front: text.slice(4, close + 1), body: text.slice(close + 5).trim() };
}

/** One `key: value` line of a block. Lists and nested maps are nobody's business here. */
function field(block: string, key: string): string {
  for (const line of block.split("\n")) {
    if (!line.startsWith(`${key}:`)) continue;
    return line.slice(key.length + 1).trim();
  }
  return "";
}

/** The text under a `## Heading`, up to the next one. */
function section(file: string, body: string, heading: string): string {
  const at = body.indexOf(`\n## ${heading}\n`);
  if (at === -1) throw new Error(`${file}: the document has no ## ${heading} section`);
  const rest = body.slice(at + heading.length + 5);
  const next = rest.indexOf("\n## ");
  return (next === -1 ? rest : rest.slice(0, next)).trim();
}

/**
 * A table's body rows, cells trimmed and unquoted. The header and its rule are dropped by
 * position rather than by matching, because a document whose columns have been renamed should
 * fail on the cell it cannot read rather than silently shift by one.
 */
function rows(table: string): readonly (readonly string[])[] {
  const lines = table.split("\n").filter((line) => line.trimStart().startsWith("|"));
  return lines.slice(2).map((line) =>
    line
      .trim()
      .replace(/^\|/u, "")
      .replace(/\|$/u, "")
      .split("|")
      .map((cell) => cell.trim().replace(/^`|`$/gu, "")),
  );
}

/** `>= 2.7`, `<= 1.3`, `is current`, `is not none`. Anything else nobody can evaluate. */
function conditionOf(file: string, voter: string, cell: string): Condition {
  const numeric = /^(>=|<=)\s*(-?\d+(?:\.\d+)?)$/u.exec(cell);
  if (numeric?.[1] !== undefined && numeric[2] !== undefined) {
    return { op: numeric[1] === ">=" ? "at-least" : "at-most", value: Number(numeric[2]) };
  }
  const negated = /^is not (.+)$/u.exec(cell);
  if (negated?.[1] !== undefined) return { op: "is-not", value: negated[1] };
  const equal = /^is (.+)$/u.exec(cell);
  if (equal?.[1] !== undefined) return { op: "is", value: equal[1] };
  throw new Error(
    `${file}: ${voter}'s threshold reads "${cell}", which is not one of >= n, <= n, is x, is not x`,
  );
}

/**
 * THE DISTRIBUTION THAT JUSTIFIED THE THRESHOLD, or a refusal. `n` and `fires` are required of
 * every row, `mean` and `sd` of every numeric one. A row recording no distribution is not a
 * calibrated threshold, it is a number somebody liked.
 */
function distributionOf(file: string, voter: string, cell: string, numeric: boolean): Distribution {
  const read = (key: string): number | null => {
    const found = new RegExp(`\\b${key}=(-?\\d+(?:\\.\\d+)?)`, "u").exec(cell);
    return found?.[1] === undefined ? null : Number(found[1]);
  };
  const n = read("n");
  const fires = read("fires");
  if (n === null || fires === null) {
    throw new Error(
      `${file}: ${voter}'s threshold records no observed distribution (wants n= and fires=); ` +
        `a threshold nobody can trace to this corpus is a threshold from somebody else's`,
    );
  }
  const mean = read("mean");
  const sd = read("sd");
  if (numeric && (mean === null || sd === null)) {
    throw new Error(
      `${file}: ${voter}'s threshold cuts a magnitude and records no mean= and sd= to cut it at`,
    );
  }
  return { n, fires, mean, sd };
}

/** The `### <id>` blocks of a section, each as its heading and the text under it. */
function blocks(body: string): readonly { readonly head: string; readonly text: string }[] {
  return body
    .split(/^### /mu)
    .slice(1)
    .map((part) => {
      const newline = part.indexOf("\n");
      return {
        head: (newline === -1 ? part : part.slice(0, newline)).trim(),
        text: newline === -1 ? "" : part.slice(newline + 1).trim(),
      };
    });
}

function questionsOf(file: string, body: string): readonly Question[] {
  const found: Question[] = [];
  for (const { head, text } of blocks(section(file, body, "Questions"))) {
    const type = field(text, "type");
    if (type !== "score" && type !== "choice" && type !== "noul") {
      throw new Error(`${file}: question ${head} declares type "${type}", which is not a shape`);
    }
    const asks = field(text, "asks");
    if (asks === "") throw new Error(`${file}: question ${head} asks nothing`);
    const criteria = text
      .split("\n")
      .filter((line) => line.startsWith("- "))
      .map((line) => line.slice(2).trim());
    // A SCORE OR A CHOICE IS ITS LEVELS. Asked without them it returns a number whose meaning
    // the assessor invented, which is the degenerate yes/no form the study measured at 92.8%.
    if (type !== "noul" && criteria.length < 2) {
      throw new Error(`${file}: question ${head} is a ${type} and describes fewer than two levels`);
    }
    found.push({ id: head, type, asks, criteria });
  }
  if (found.length === 0) throw new Error(`${file}: the document defines no questions`);
  return found;
}

function votesOf(file: string, body: string): readonly Vote[] {
  const found: Vote[] = [];
  for (const row of rows(section(file, body, "Thresholds"))) {
    const [named, question, casts, when, observed] = row;
    if (
      named === undefined ||
      question === undefined ||
      casts === undefined ||
      when === undefined
    ) {
      throw new Error(`${file}: a thresholds row has fewer than five cells: ${row.join(" | ")}`);
    }
    const voter = VOTERS.find((known) => known === named);
    if (voter === undefined) {
      throw new Error(`${file}: ${named} is not one of the bank's voters`);
    }
    // THE ROUTING QUESTIONS ARE NOT OPINIONS. Written as a vote here they would be tallied as
    // one, which is the single mistake this separation exists to prevent.
    if ((ROUTING_QUESTIONS as readonly string[]).includes(question)) {
      throw new Error(
        `${file}: ${question} is a routing question and cannot be given a threshold; it says ` +
          `where a record goes, not whether it is any good`,
      );
    }
    if (casts !== "up" && casts !== "down") {
      throw new Error(`${file}: ${voter} casts "${casts}", which is neither up nor down`);
    }
    const condition = conditionOf(file, voter, when);
    const numeric = condition.op === "at-least" || condition.op === "at-most";
    const distribution = distributionOf(file, voter, observed ?? "", numeric);
    found.push({
      voter,
      question,
      casts,
      when: condition,
      observed: distribution,
      admitted: admits(distribution),
    });
  }
  if (found.length === 0) throw new Error(`${file}: the thresholds block holds no voter`);
  return found;
}

/**
 * The suggestion block. Every row carries a cut and the distribution that justified it, exactly
 * as a threshold does, because the argument is the same one: a line nobody can trace to this
 * corpus is a line from somebody else's, and a suggestion is a thing the operator has to read
 * and answer. The block may hold no rows — a kind nothing advises on advises nothing — but it
 * has to exist, so that deleting every suggestion in a document is an edit a reviewer sees
 * rather than a heading that quietly went missing.
 */
function advisoriesOf(file: string, body: string): readonly Advisory[] {
  const found: Advisory[] = [];
  for (const row of rows(section(file, body, "Advisories"))) {
    const [question, suggests, when, observed] = row;
    if (question === undefined || suggests === undefined || when === undefined) {
      throw new Error(`${file}: an advisories row has fewer than four cells: ${row.join(" | ")}`);
    }
    // A LABEL IS NOT A REASON TO PROPOSE ANYTHING. Where a record is filed and whether it may be
    // published are not views on what should happen to it, so the routing questions are kept out
    // of this block for the reason they are kept out of the thresholds one.
    if ((ROUTING_QUESTIONS as readonly string[]).includes(question)) {
      throw new Error(
        `${file}: ${question} is a routing question and cannot be given a threshold; it says ` +
          `where a record goes, not what should happen to it`,
      );
    }
    if (suggests !== "none" && !(NEXT_ACTIONS as readonly string[]).includes(suggests)) {
      throw new Error(
        `${file}: ${question} suggests "${suggests}", which is not one of the next actions ` +
          `babel.suggest can write`,
      );
    }
    const condition = conditionOf(file, question, when);
    const numeric = condition.op === "at-least" || condition.op === "at-most";
    const distribution = distributionOf(file, question, observed ?? "", numeric);
    found.push(
      AdvisorySchema.parse({
        question,
        suggests: suggests === "none" ? null : suggests,
        when: condition,
        observed: distribution,
        admitted: admits(distribution),
      }),
    );
  }
  return found;
}

function routingOf(file: string, body: string): readonly Routing[] {
  const table = rows(section(file, body, "Routing"));
  for (const row of table) {
    const question = row[0];
    if (question === undefined || !(ROUTING_QUESTIONS as readonly string[]).includes(question)) {
      throw new Error(`${file}: ${question ?? "an empty cell"} is not a routing question`);
    }
  }
  return ROUTING_QUESTIONS.map((question) => {
    const routes = table.find((row) => row[0] === question)?.[1];
    if (routes === undefined || routes === "") {
      throw new Error(`${file}: the routing block does not say what ${question} routes`);
    }
    return { question, routes };
  });
}

function exemplarsOf(file: string, kind: string, body: string): readonly Exemplar[] {
  const found: Exemplar[] = [];
  for (const { head, text } of blocks(section(file, body, "Exemplars"))) {
    if (KIND_BY_PREFIX[head.slice(0, 3)] !== kind) {
      throw new Error(`${file}: exemplar ${head} is not a ${kind}`);
    }
    const lines = text.split("\n");
    const quoted = lines
      .filter((line) => line.startsWith("> "))
      .map((line) => line.slice(2).trim())
      .join(" ")
      .trim();
    if (quoted === "") throw new Error(`${file}: exemplar ${head} quotes no record`);
    const why = lines
      .filter(
        (line) =>
          line.trim() !== "" && !line.startsWith(">") && !/^(provenance|ruling|tally):/u.test(line),
      )
      .join(" ")
      .trim();
    if (why === "") throw new Error(`${file}: exemplar ${head} says nothing about why it is one`);
    const provenance = field(text, "provenance");
    const standing = field(text, "tally");
    const ruling = field(text, "ruling");
    // AN EXEMPLAR SAYS WHOSE JUDGEMENT IT RECORDS. `standing` is the panel's own tally and is
    // not the operator's taste; a ruled provenance is his and has to name his word.
    if (provenance === "standing" && standing === "") {
      throw new Error(`${file}: exemplar ${head} stands at no tally`);
    }
    if (provenance !== "standing" && ruling === "") {
      throw new Error(
        `${file}: exemplar ${head} claims provenance "${provenance}" and names no ruling`,
      );
    }
    found.push(
      ExemplarSchema.parse({
        record: head,
        provenance,
        ruling: ruling === "" ? null : ruling,
        tally: standing === "" ? null : Number(standing),
        text: quoted,
        why,
      }),
    );
  }
  if (found.length === 0) {
    throw new Error(`${file}: the document carries no exemplar, so it teaches only its own prose`);
  }
  return found;
}

/** Reads one bank document, or refuses it by name. */
export function documentOf(
  file: string,
  text: string,
  versions: Readonly<Record<string, number>>,
): BankDocument {
  const { front, body } = split(file, text);
  const kind = field(front, "kind");
  if (kind !== file.replace(/\.md$/u, "")) {
    throw new Error(
      `${file}: the frontmatter calls it ${kind}, so the file name and kind disagree`,
    );
  }
  const declared = Number(field(front, "version"));
  const recorded = versions[kind];
  if (recorded === undefined) throw new Error(`${file}: versions.json holds no record of ${kind}`);
  // AN ASSESSMENT CITES `kind@version`. A bank seeded under a number the manifest does not
  // record would make every judgement it produced name a wording nobody can read back.
  if (declared !== recorded) {
    throw new Error(
      `${file}: the document declares version ${String(declared)} and versions.json records ` +
        `${String(recorded)}; change the document and its record together`,
    );
  }
  const questions = questionsOf(file, body);
  const votes = votesOf(file, body);
  const advisories = advisoriesOf(file, body);
  const routing = routingOf(file, body);
  const defined = new Set(questions.map((question) => question.id));
  const asked = new Set([
    ...votes.map((vote) => vote.question),
    ...advisories.map((advisory) => advisory.question),
    ...routing.map((entry) => entry.question),
  ]);
  for (const question of asked) {
    if (!defined.has(question)) throw new Error(`${file}: ${question} is asked and never defined`);
  }
  for (const question of defined) {
    if (!asked.has(question)) {
      throw new Error(`${file}: question ${question} is defined and never asked of anything`);
    }
  }
  const heading = body.split("\n").find((line) => line.startsWith("# "));
  return BankDocumentSchema.parse({
    kind: RecordKindSchema.parse(kind),
    version: recorded,
    title: heading === undefined ? kind : heading.slice(2).trim(),
    questions,
    votes,
    advisories,
    routing,
    exemplars: exemplarsOf(file, kind, body),
  });
}
