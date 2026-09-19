#!/usr/bin/env bun
/*
  THE QUESTION BANK, AS PLUGIN DATA.

  `bank/questions/*.md` is the reviewable source: one document per record kind, each declaring a
  version that `bank/versions.json` records, each carrying the wording that goes out, the line at
  which each answer becomes a vote, the distribution that justified that line, and the exemplars
  the operator's own rulings will eventually fill. `bank/questions.seed.json` is what the runtime
  imports, because nothing a hub runs reads a repository file.

  Three jobs and no fourth:

    bun atyrode.babel.jev/tools/seed-questions.ts import   reads bank/ and writes bank/questions.seed.json
    bun atyrode.babel.jev/tools/seed-questions.ts check    refuses any drift between the two
    bun atyrode.babel.jev/tools/seed-questions.ts policy   prints the wording an operator installs

  `check` is the gate. An assessment cites `kind@version`, so a seed that no longer matches the
  documents it was generated from would make every judgement name a wording nobody can read back
  — the same failure `seed-recipes.ts check` exists to prevent for a claim citing `recipe@version`,
  and deliberately the same mechanism.

  IT INSTALLS NOTHING. `policy` prints; the operator installs. The service policy also carries the
  endpoint, the credential reference and the spend the calls are made under, none of which is a
  file's to supply.

  It is a dev-time Bun CLI and never enters a packed artifact.
*/

import { resolve } from "node:path";
import type { Bank, BankDocument } from "../bank/schema.ts";
import { documentOf } from "../bank/parse.ts";

const BANK_DIR = resolve(import.meta.dir, "../bank");
const SEED_PATH = resolve(BANK_DIR, "questions.seed.json");

const ABOUT =
  "The question bank: what Jev is asked of each record kind, at what line each answer becomes a " +
  "vote, and the distribution over this corpus that justified the line. An assessment cites " +
  "kind@version; the thresholds are one deployment's calibration and not Babel's behaviour.";

interface Manifest {
  readonly bank?: { readonly version?: number };
  readonly documents?: readonly { readonly kind?: string; readonly version?: number }[];
}

async function read(path: string): Promise<string> {
  const file = Bun.file(path);
  if (!(await file.exists())) throw new Error(`${path} does not exist`);
  return await file.text();
}

/** Reads `bank/` into the seed document, refusing every document that disagrees with itself. */
async function imported(): Promise<Bank> {
  const manifest = JSON.parse(await read(resolve(BANK_DIR, "versions.json"))) as Manifest;
  const versions: Record<string, number> = {};
  for (const entry of manifest.documents ?? []) {
    if (typeof entry.kind === "string" && typeof entry.version === "number") {
      versions[entry.kind] = entry.version;
    }
  }
  if (Object.keys(versions).length === 0) throw new Error("bank/versions.json records no document");
  const bankVersion = manifest.bank?.version;
  if (typeof bankVersion !== "number") throw new Error("bank/versions.json declares no version");
  const documents: BankDocument[] = [];
  const files = [...new Bun.Glob("*.md").scanSync({ cwd: resolve(BANK_DIR, "questions") })].sort();
  for (const name of files) {
    documents.push(documentOf(name, await read(resolve(BANK_DIR, "questions", name)), versions));
  }
  // EVERY RECORDED KIND HAS TO BE PRESENT. A manifest naming a document the directory no longer
  // holds is a kind the sweep would think it can judge and no call could ask about.
  const missing = Object.keys(versions)
    .filter((kind) => !documents.some((document) => document.kind === kind))
    .sort();
  if (missing.length > 0) {
    throw new Error(`versions.json records kinds with no document: ${missing.join(", ")}`);
  }
  return { about: ABOUT, version: bankVersion, documents };
}

function report(bank: Bank): void {
  for (const document of bank.documents) {
    const retired = document.votes.filter((vote) => !vote.admitted);
    process.stdout.write(
      `  ${document.kind}@${String(document.version)} ` +
        `${String(document.questions.length)} questions, ` +
        `${String(document.votes.length - retired.length)} of ${String(document.votes.length)} sides admitted, ` +
        `${String(document.exemplars.length)} exemplars\n`,
    );
    for (const vote of retired) {
      process.stdout.write(
        `    not admitted: ${vote.voter} ${vote.casts} on ${vote.question}, ` +
          `fires on ${String(vote.observed.fires)}% of ${String(vote.observed.n)}\n`,
      );
    }
  }
}

async function main(argv: readonly string[]): Promise<number> {
  const [job] = argv;
  if (job === "import") {
    const bank = await imported();
    await Bun.write(SEED_PATH, `${JSON.stringify(bank, null, 2)}\n`);
    process.stdout.write(`bank version ${String(bank.version)} written to ${SEED_PATH}\n`);
    report(bank);
    return 0;
  }
  if (job === "check") {
    const bank = await imported();
    const committed = await read(SEED_PATH);
    if (committed !== `${JSON.stringify(bank, null, 2)}\n`) {
      process.stderr.write(
        "bank/questions.seed.json no longer matches bank/questions/: an assessment cites " +
          "kind@version, so run `bun atyrode.babel.jev/tools/seed-questions.ts import` and bump " +
          "the versions the edit changed\n",
      );
      return 1;
    }
    process.stdout.write(`bank version ${String(bank.version)} agrees with its documents\n`);
    report(bank);
    return 0;
  }
  if (job === "policy") {
    const bank = JSON.parse(await read(SEED_PATH)) as Bank;
    // THE WORDING ALONE. The endpoint, the credential reference and the spend ceiling belong to
    // the operator's own service policy, and a tool that printed them would be choosing them.
    process.stdout.write(
      `${JSON.stringify(
        bank.documents.map((document) => ({
          kind: document.kind,
          version: document.version,
          questions: document.questions,
        })),
        null,
        2,
      )}\n`,
    );
    return 0;
  }
  process.stderr.write(
    "usage: seed-questions.ts import | seed-questions.ts check | seed-questions.ts policy\n" +
      "  import  reads bank/versions.json and bank/questions/*.md into bank/questions.seed.json\n" +
      "  check   exits 1 when the seed and the documents disagree\n" +
      "  policy  prints the wording an operator renders into the service policy's literals\n",
  );
  return 2;
}

if (import.meta.main) {
  try {
    process.exit(await main(process.argv.slice(2)));
  } catch (error) {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exit(1);
  }
}
