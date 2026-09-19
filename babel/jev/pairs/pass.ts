import type { GuestActions } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  RecordKindSchema,
  RecordPeelSchema,
  SearchResultSchema,
  type PairsReport,
  type SearchQuery,
  type PairAnchor,
} from "../../contract.ts";
import { answersOf } from "../screen/screener.ts";
import {
  jevPolicyRevision,
  withinCallCap,
  type JevAnswerStore,
  type JevServices,
} from "../server/credential.ts";
import { askPair, pairBasis, pairInput } from "./ask.ts";
import { deliveries, detectPair, DETECTORS, type PairUncalibrated } from "./detect.ts";
import { statedCut, type PairDetector, type PairRecord } from "./pair.ts";
import { proposePairs, type PairProposal, type PairSearchAnswer } from "./propose.ts";

/*
  One bounded pass over named anchors. First reject unstated confidence cuts and unavailable
  policy, then hydrate exact revisions through record, retrieve candidates through search, and
  buy one answer per ordered pair for both detectors. The pool is caller-supplied, not exhaustive.

  Oversized pairs are skipped before invocation; a missing service answer stops further calls.
  No suggestion is a ruling, and this part has no write authority. The caller receives each
  finding with its counterpart and detector aspect so independent relations can coexist.
*/

/** What a pair pass needs: the judgement service, and the doors it reads and retrieves through. */
export interface PairPassDeps {
  readonly services: JevServices;
  readonly actions: GuestActions;
  /** The roster, overridable so a test can put one detector in front of the pass. */
  readonly detectors?: readonly PairDetector[];
  /** The memo the pair judgements are held in; left out, it is the server half's own. */
  readonly answers?: JevAnswerStore;
}

/** How a pass was asked to be bounded, in the door's own words. */
export interface PairAsk {
  readonly anchors: readonly PairAnchor[];
  readonly cuts: Readonly<Record<string, number>>;
  readonly judgements: number;
}

const NOTHING: PairsReport = {
  anchors: 0,
  searches: 0,
  candidates: 0,
  attempted: 0,
  judged: 0,
  truncated: false,
  meaning: "absent",
  absent: "",
  approximate: false,
  uncalibrated: [],
  suggestions: [],
  failed: [],
  stopped: "",
};

/**
 * One anchor's words and instant off the peel, or `null` because the doors would not serve it.
 *
 * The id, revision and kind are the CALLER's row and never the peel's: the peel's five depths
 * carry no `records.seq`, and its `post.kind` widens to the post vocabulary, so reading either
 * from it would be reading something else and calling it the record's.
 */
async function hydrate(actions: GuestActions, anchor: PairAnchor): Promise<PairRecord | null> {
  try {
    const peel = RecordPeelSchema.parse(
      await actions.call({
        plugin: BABEL_PLUGIN_ID,
        action: ACTIONS.record,
        input: { id: anchor.recordId },
      }),
    );
    // No words is no question. A pair judgement of an empty state is a judgement of nothing, and
    // the proposer would have nothing to build a query out of either.
    if (peel.claim.statement === "") return null;
    return {
      id: anchor.recordId,
      revision: anchor.revision,
      kind: RecordKindSchema.parse(anchor.kind),
      title: peel.post.title,
      text: peel.claim.statement,
      writtenAt: peel.post.createdAt,
    };
  } catch {
    return null;
  }
}

/**
 * ONE BOUNDED PASS OVER THE PAIRS OF THE ANCHORS IT WAS GIVEN.
 *
 * Every number in the report is the loop's own count rather than a derived one, because the
 * question a caller asks of it — "did this find nothing, or did it not get to look" — is exactly
 * the question a derived count cannot answer.
 */
export async function pairPass(deps: PairPassDeps, ask: PairAsk): Promise<PairsReport> {
  const detectors = deps.detectors ?? DETECTORS;
  // STEP 1: WHO CAN SPEAK, before anything is read or paid for. An unstated line is reported and
  // never silently obeyed, and a pass where every detector is silent buys nothing by running.
  const uncalibrated: PairUncalibrated[] = detectors
    .filter((detector) => statedCut(ask.cuts, detector.question) === null)
    .map((detector) => ({ detector: detector.id, question: detector.question }));
  if (uncalibrated.length === detectors.length) {
    return {
      ...NOTHING,
      uncalibrated,
      stopped:
        "no detector has a stated cut on this deployment, so nothing was read and nothing was " +
        "spent: state a measured line per question before asking for a pass",
    };
  }
  // STEP 2: IS THERE A POLICY AT ALL, before a record is read and before a penny is spent. It
  // is also what the suggestions will be MARKED with: the revision goes into the basis, so a
  // pass cannot attribute an answer to a policy it never saw, and `askPair` re-checks it on
  // every call so one replaced mid-pass is an absence rather than a mislabelled answer.
  const revision = await jevPolicyRevision(deps.services);
  if (revision === null) {
    return {
      ...NOTHING,
      uncalibrated,
      stopped:
        "no judgement service is bound and ready for the pair operation, so nothing was read " +
        "and nothing was spent",
    };
  }
  const basis = pairBasis(revision, ask.cuts);
  // STEP 3: THE ANCHORS' OWN WORDS. Naming one twice reads it once, because the proposal's pool
  // is keyed by id and a second copy would only cost a read.
  const seen = new Set<string>();
  const records: PairRecord[] = [];
  let unreadable = 0;
  for (const anchor of ask.anchors) {
    if (seen.has(anchor.recordId)) continue;
    seen.add(anchor.recordId);
    const record = await hydrate(deps.actions, anchor);
    if (record === null) {
      unreadable += 1;
      continue;
    }
    records.push(record);
  }
  if (records.length < 2) {
    return {
      ...NOTHING,
      uncalibrated,
      stopped:
        `a pair needs two records and ${String(records.length)} of ` +
        `${String(ask.anchors.length)} anchors could be read: ${String(unreadable)} were not ` +
        `served by the reading doors`,
    };
  }
  // STEP 4: THE CANDIDATES. The judgement budget is the proposal's ceiling, so the loop stops
  // searching at the point it has as many pairs as it may pay for rather than proposing a
  // hundred and discarding them after the searches were made.
  let proposal: PairProposal;
  try {
    proposal = await proposePairs(
      records,
      async (query: SearchQuery): Promise<PairSearchAnswer> =>
        SearchResultSchema.parse(
          await deps.actions.call({
            plugin: BABEL_PLUGIN_ID,
            action: ACTIONS.search,
            input: { query: query.query, limit: query.limit, kinds: [...query.kinds] },
          }),
        ),
      { cap: ask.judgements },
    );
  } catch (error) {
    return {
      ...NOTHING,
      uncalibrated,
      stopped: `the corpus could not be searched: ${
        error instanceof Error ? error.message : String(error)
      }`,
    };
  }
  const report: PairsReport = {
    ...NOTHING,
    anchors: proposal.anchors,
    searches: proposal.searches,
    candidates: proposal.pairs.length,
    truncated: proposal.truncated,
    meaning: proposal.meaning,
    absent: proposal.absent,
    approximate: proposal.approximate,
    uncalibrated,
  };
  const suggestions: PairsReport["suggestions"] = [];
  const failed: PairsReport["failed"] = [];
  let attempted = 0;
  let judged = 0;
  let stopped = "";
  // STEP 5: ONE PAID CALL PER ORDERED PAIR, AND BOTH RELATIONS OFF IT.
  for (const pair of proposal.pairs) {
    attempted += 1;
    // Too large to be worth sending is knowable before the roster is read, and it is THIS pair's
    // absence rather than the deployment's: counting it as attempted and moving on is what keeps
    // one oversized pair from reading as "no service bound" and ending the pass.
    if (!withinCallCap(pairInput(pair))) continue;
    const answer = await askPair(deps.services, pair, {
      revision,
      ...(deps.answers ? { answers: deps.answers } : {}),
    });
    if (answer === null) {
      stopped =
        `jev did not judge ${pair.a.id} and ${pair.b.id} under policy revision ${revision}; ` +
        `the pass stopped`;
      break;
    }
    judged += 1;
    const detected = detectPair(pair, answersOf(answer), ask.cuts, detectors);
    for (const failure of detected.failed) {
      failed.push({
        detector: failure.detector,
        records: [...failure.records],
        reason: failure.reason,
      });
    }
    for (const detection of detected.detections) {
      for (const suggestion of deliveries(detection, pair)) {
        suggestions.push({
          recordId: suggestion.recordId,
          revision: suggestion.revision,
          kind: suggestion.kind,
          subject: suggestion.counterpart,
          aspect: suggestion.detector,
          summary: suggestion.summary,
          rationale: suggestion.rationale,
          detector: suggestion.detector,
          basis,
        });
      }
    }
  }
  return { ...report, attempted, judged, suggestions, failed, stopped };
}
