import type { GuestActions } from "@manifold/plugin-kit/server";
import {
  ACTIONS,
  BABEL_PLUGIN_ID,
  DUPLICATE_QUESTION,
  DuplicatePlanInputSchema,
  DuplicatePlanSchema,
  DuplicateSweepInputSchema,
  type DuplicateIntent,
  type DuplicateMember,
  type DuplicatePlan,
  type DuplicatePlanInput,
  type DuplicateSweepInput,
  type DuplicateSweepPlan,
  type DuplicateSweepReport,
  type SuggestInput,
} from "../../contract.ts";
import { degreeOf } from "../pairs/pair.ts";
import {
  jevPolicyRevision,
  withinCallCap,
  type JevAnswerStore,
  type JevServices,
} from "../server/credential.ts";
import { askDuplicate, duplicateBasis, duplicateInput, DUPLICATE_WORDING_VERSION } from "./ask.ts";

export interface DuplicateDeps {
  readonly actions: GuestActions;
  readonly services: JevServices;
  readonly answers?: JevAnswerStore;
}

type Candidate = DuplicatePlan["candidates"][number];
type Pair = readonly [Candidate, Candidate];
interface JudgedPair {
  readonly a: string;
  readonly b: string;
  readonly confidence: number;
}

const NOTHING: DuplicateSweepReport = {
  candidates: 0,
  attempted: 0,
  judged: 0,
  truncated: false,
  suggestions: [],
  stopped: "",
};

async function readPlan(actions: GuestActions, input: DuplicatePlanInput): Promise<DuplicatePlan> {
  return DuplicatePlanSchema.parse(
    await actions.call({ plugin: BABEL_PLUGIN_ID, action: ACTIONS.duplicatePlan, input }),
  );
}

/** Free, and unavailable Jev never changes what the baseline reads or ranks. */
export async function duplicatesPlan(
  deps: DuplicateDeps,
  input: DuplicatePlanInput,
): Promise<DuplicateSweepPlan> {
  const parsed = DuplicatePlanInputSchema.parse(input);
  if ((await jevPolicyRevision(deps.services)) === null) {
    return { plan: null, silent: "the judgement service is not available" };
  }
  try {
    return { plan: await readPlan(deps.actions, parsed), silent: "" };
  } catch {
    return { plan: null, silent: "the duplicate candidate reading door is not available" };
  }
}

/** Only the baseline's exact reread can supply text, kind and provenance. */
async function reread(
  actions: GuestActions,
  expected: ReadonlyMap<string, DuplicateMember>,
): Promise<Candidate[] | null> {
  try {
    const plan = await readPlan(actions, {
      ids: [...expected.keys()],
      after: "",
      limit: expected.size,
    });
    if (plan.candidates.length !== expected.size) return null;
    const seen = new Set<string>();
    for (const candidate of plan.candidates) {
      const member = expected.get(candidate.recordId);
      if (
        member === undefined ||
        seen.has(candidate.recordId) ||
        member.revision !== candidate.revision ||
        member.fingerprint !== candidate.fingerprint
      ) {
        return null;
      }
      seen.add(candidate.recordId);
    }
    return plan.candidates.sort((a, b) =>
      a.recordId < b.recordId ? -1 : a.recordId > b.recordId ? 1 : 0,
    );
  } catch {
    return null;
  }
}

function memberOf(candidate: Candidate): DuplicateMember {
  return {
    recordId: candidate.recordId,
    revision: candidate.revision,
    fingerprint: candidate.fingerprint,
    kind: candidate.kind,
    runId: candidate.runId,
    sourceIds: [...new Set(candidate.sourceIds)].sort(),
    unresolvedSources: candidate.unresolvedSources,
  };
}

/** Only resolved provenance enters distinct sets; partly unresolved evidence stays visibly missing. */
function auditOf(members: readonly DuplicateMember[]): DuplicateIntent["audit"] {
  const runs = new Set<string>();
  const sources = new Set<string>();
  let missingRuns = 0;
  let missingSources = 0;
  for (const member of members) {
    if (member.runId === "") missingRuns += 1;
    else runs.add(member.runId);
    if (member.sourceIds.length === 0 || member.unresolvedSources > 0) missingSources += 1;
    for (const sourceId of member.sourceIds) sources.add(sourceId);
  }
  return {
    records: members.length,
    distinctRuns: runs.size,
    distinctSources: sources.size,
    missingRuns,
    missingSources,
  };
}

/**
 * A positive chain is not enough: A≈B and B≈C must never overrule A≠C. Withhold the entire
 * connected component unless every internal pair was judged above the caller's cut. Unknown
 * pairs, including those left by the spend ceiling, are not silent positive evidence.
 */
function clusterSuggestions(
  candidates: readonly Candidate[],
  judgements: readonly JudgedPair[],
  cut: number,
  revision: string,
): { suggestions: SuggestInput[]; withheld: number } {
  const neighbours = new Map<string, Set<string>>();
  for (const pair of judgements) {
    if (pair.confidence < cut) continue;
    for (const [a, b] of [
      [pair.a, pair.b],
      [pair.b, pair.a],
    ] as const) {
      const adjacent = neighbours.get(a) ?? new Set<string>();
      adjacent.add(b);
      neighbours.set(a, adjacent);
    }
  }
  const visited = new Set<string>();
  const suggestions: SuggestInput[] = [];
  const basis = duplicateBasis(revision, cut);
  let withheld = 0;
  for (const candidate of candidates) {
    if (visited.has(candidate.recordId) || !neighbours.has(candidate.recordId)) continue;
    const component = new Set<string>();
    const pending = [candidate.recordId];
    while (pending.length > 0) {
      const id = pending.pop()!;
      if (component.has(id)) continue;
      component.add(id);
      visited.add(id);
      for (const neighbour of neighbours.get(id) ?? []) pending.push(neighbour);
    }
    const pairs = judgements.filter((pair) => component.has(pair.a) && component.has(pair.b));
    if (
      pairs.length !== (component.size * (component.size - 1)) / 2 ||
      pairs.some((pair) => pair.confidence < cut)
    ) {
      withheld += 1;
      continue;
    }
    const members = candidates.filter((row) => component.has(row.recordId)).map(memberOf);
    // The representative is a stable address for the cluster, never a model's chosen winner.
    const representative = members[0]!;
    const audit = auditOf(members);
    const intent: DuplicateIntent = {
      kind: "merge-duplicate-records",
      representative: representative.recordId,
      members,
      pairs: pairs.map((pair) => ({
        a: pair.a,
        b: pair.b,
        evidence:
          `Jev ${DUPLICATE_QUESTION}@${String(DUPLICATE_WORDING_VERSION)} judged the same substantive ` +
          `claim, subject and scope at ${String(pair.confidence)} against the supplied cut ` +
          `${String(cut)}. Both immutable members were read from Babel before judgement.`,
      })),
      audit,
    };
    suggestions.push({
      recordId: representative.recordId,
      revision: representative.revision,
      kind: "ask-question",
      subject: "",
      aspect: "duplicate-cluster",
      summary: `Review ${String(members.length)} near-duplicate records as one cluster`,
      rationale:
        `Every pair in this connected cluster met the supplied duplicate cut ${String(cut)}. ` +
        `${String(audit.records)} records represent ${String(audit.distinctRuns)} distinct known ` +
        `runs and ${String(audit.distinctSources)} distinct known sources; ` +
        `${String(audit.missingRuns)} records lack run provenance and ` +
        `${String(audit.missingSources)} have absent or unresolved source provenance. ` +
        `Repeated records are not independent corroboration. ${representative.recordId} is the deterministic ` +
        `representative, not a preferred claim. This is advisory only; no link, ruling, ` +
        `reading or rank has changed.`,
      basis,
      intent,
    });
  }
  return { suggestions, withheld };
}

/** Explicit judgement only; every effect remains in the operator's separate delivery/apply path. */
export async function duplicates(
  deps: DuplicateDeps,
  input: DuplicateSweepInput,
): Promise<DuplicateSweepReport> {
  const ask = DuplicateSweepInputSchema.parse(input);
  const revision = await jevPolicyRevision(deps.services);
  if (revision === null) {
    return {
      ...NOTHING,
      stopped: "the judgement service is not available; nothing was read or spent",
    };
  }
  const expected = new Map<string, DuplicateMember>();
  for (const member of ask.members) {
    const earlier = expected.get(member.recordId);
    if (
      earlier !== undefined &&
      (earlier.revision !== member.revision || earlier.fingerprint !== member.fingerprint)
    ) {
      return { ...NOTHING, stopped: "the request names conflicting revisions of one record" };
    }
    expected.set(member.recordId, member);
  }
  if (expected.size < 2) {
    return { ...NOTHING, stopped: "a duplicate cluster needs at least two distinct records" };
  }
  const candidates = await reread(deps.actions, expected);
  if (candidates === null) {
    return {
      ...NOTHING,
      stopped: "the planned members changed or are no longer eligible; plan again",
    };
  }
  const pairs: Pair[] = [];
  for (let index = 0; index < candidates.length; index += 1) {
    const a = candidates[index]!;
    for (let other = index + 1; other < candidates.length; other += 1) {
      const b = candidates[other]!;
      if (a.kind === b.kind) pairs.push([a, b]);
    }
  }
  const report: DuplicateSweepReport = {
    ...NOTHING,
    candidates: pairs.length,
    truncated: pairs.length > ask.judgements,
  };
  const judged: JudgedPair[] = [];
  let oversized = 0;
  const budget = Math.min(pairs.length, ask.judgements);
  for (let index = 0; index < budget; index += 1) {
    const [a, b] = pairs[index]!;
    // Revalidate the entire exact set before each potential paid call, not only at plan time.
    if ((await reread(deps.actions, expected)) === null) {
      return { ...report, stopped: "the planned members changed during judgement; plan again" };
    }
    report.attempted += 1;
    if (!withinCallCap(duplicateInput(a, b))) {
      oversized += 1;
      continue;
    }
    const answer = await askDuplicate(deps.services, a, b, {
      revision,
      ...(deps.answers === undefined ? {} : { answers: deps.answers }),
    });
    const raw = answer?.[DUPLICATE_QUESTION];
    const confidence = degreeOf(typeof raw === "number" ? raw : undefined);
    if (confidence === null) {
      // No service / no funding / unreadable answer: never return partial drafts as success.
      return {
        ...report,
        stopped: "Jev did not answer under the planned policy; no drafts were made",
      };
    }
    report.judged += 1;
    judged.push({ a: a.recordId, b: b.recordId, confidence });
  }
  const current = await reread(deps.actions, expected);
  if (current === null) {
    return { ...report, stopped: "the planned members changed during judgement; plan again" };
  }
  const clustered = clusterSuggestions(current, judged, ask.cut, revision);
  const reasons: string[] = [];
  if (report.truncated) reasons.push("the judgement ceiling left candidate pairs unjudged");
  if (oversized > 0) reasons.push(`${String(oversized)} pairs exceeded the per-call size cap`);
  if (clustered.withheld > 0) {
    reasons.push(
      `${String(clustered.withheld)} connected components had unjudged or nonduplicate pairs`,
    );
  }
  return { ...report, suggestions: clustered.suggestions, stopped: reasons.join("; ") };
}
