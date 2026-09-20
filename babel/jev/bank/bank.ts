import type { RecordKind } from "../../contract.ts";
import { BankSchema, type Advisory, type Bank, type BankDocument, type Vote } from "./schema.ts";
import seed from "./questions.seed.json";

/*
  THE BANK AS IT SHIPS.

  `bank/questions/*.md` is the source an operator reviews and `bank/questions.seed.json` is the
  artifact a hub carries, because nothing a hub runs reads a repository file. The two are held
  together by `tools/seed-questions.ts check`, which refuses any drift between them — an
  assessment cites `kind@version`, so a seed that no longer matches its documents would make
  every judgement it produced name a wording nobody can read back.

  The seed is parsed at load for the reason the manifest is: a malformed artifact should fail
  where it is read rather than at the first record it is asked about.
*/

export const BANK: Bank = BankSchema.parse(seed);

/** The document for a record kind. Every kind has one, which `BankSchema`'s length enforces. */
export function bankFor(kind: RecordKind): BankDocument {
  const document = BANK.documents.find((entry) => entry.kind === kind);
  if (document === undefined) throw new Error(`the bank holds no document for ${kind}`);
  return document;
}

/**
 * The sides that may vote on this kind — the admitted ones alone. A side that fires on almost
 * none or almost all of its kind answers the same way for everything, and a vote nobody can
 * lose is not an opinion. A caller wanting the whole block, retired sides included, reads
 * `bankFor(kind).votes` and is thereby saying so.
 */
export function votesFor(kind: RecordKind): readonly Vote[] {
  return bankFor(kind).votes.filter((vote) => vote.admitted);
}

/**
 * The advisories that may fire on this kind — the admitted ones alone, by the same rule and the
 * same band as `votesFor`. An advisory firing on almost none or almost all of its kind proposes
 * the same next action for everything, and a suggestion nobody could avoid is not advice.
 *
 * A kind with no admitted advisory returns nothing, which is how a voter produces nothing for a
 * kind the bank has not calibrated it on. A caller wanting the whole block, retired rows
 * included, reads `bankFor(kind).advisories` and is thereby saying so.
 */
export function advisoriesFor(kind: RecordKind): readonly Advisory[] {
  return bankFor(kind).advisories.filter((advisory) => advisory.admitted);
}
