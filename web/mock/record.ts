// The record peel: one record, whole, in one read.
//
// GET /api/record/{id} is the read the record page makes, and the only one it
// makes. Every depth the page can open — the claim, the case, the evidence,
// the reception, the machinery — arrives in this one response, so digging is a
// disclosure in the browser rather than a second request. The four per-kind
// routes in ./phaseb.ts stay: they answer a different question (one record in
// the shape its own store holds it), and the listing pages still use them.
//
// The operator's stance is not written here any more, because there is no
// route for it: §8.7 makes the score Babel's reviewers' and the operator's
// acts the rulings, so POST /api/record/{id}/reception is gone. What he
// recorded before it went is still in the peel — the fixture below seeds one
// current stance and one earlier one — and the surface renders them read-only,
// which is what §4.12's append means when a write is retired.
//
// This file exists rather than living in ./phaseb.ts because the peel needs
// both the frontier fixtures and the evaluation projection, and ./evaluation.ts
// already imports from ./phaseb.ts — assembling it there would close that
// import into a cycle.

import type { EvidenceRef, Observation, Proposal } from "../src/api";
import { receptionOf } from "./evaluation";
import {
  chains,
  derivedStatus,
  findings,
  headOf,
  hypotheses,
  linksOf,
  phasebEmpty,
  receipts,
  reviewRecords,
} from "./phaseb";

type Kind = "hypothesis" | "finding" | "proposal" | "observation";

// The tone each standing carries, mirrored from the real handler: the
// judgement is the server's, so a page never decides for itself whether
// "superseded" reads as bad or merely neutral.
const STANDING_TONES: Record<string, "neutral" | "good" | "bad" | "warn"> = {
  new: "neutral",
  accepted: "good",
  rejected: "bad",
  deferred: "warn",
  "refine-requested": "warn",
  reopened: "warn",
  duplicate: "neutral",
  superseded: "neutral",
};

// The id names the kind. internal/frontier mints hyp_, obs_, fnd_ and pro_,
// and the real handler dispatches on exactly those four families, so an id
// this mock answers for is an id production would answer for too.
function kindOf(id: string): Kind | null {
  if (id.startsWith("hyp_")) return "hypothesis";
  if (id.startsWith("obs_")) return "observation";
  if (id.startsWith("fnd_")) return "finding";
  if (id.startsWith("pro_")) return "proposal";
  return null;
}

function proposalOf(id: string): Proposal | undefined {
  for (const detail of Object.values(findings)) {
    const found = detail.proposals.find((proposal) => proposal.id === id);
    if (found) return found;
  }
  return undefined;
}

function observationOf(id: string): Observation | undefined {
  for (const detail of Object.values(hypotheses)) {
    const found = detail.observations.find((observation) => observation.id === id);
    if (found) return found;
  }
  for (const detail of Object.values(findings)) {
    const found = detail.observations.find((observation) => observation.id === id);
    if (found) return found;
  }
  return undefined;
}

// The mock holds no content digest, so one is derived from the id. It is
// synthetic and says so by being stable: the page renders it at depth 5 as an
// identifier, never compares it with anything.
function digestOf(id: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < id.length; index += 1) {
    hash = (hash ^ id.charCodeAt(index)) * 0x01000193 >>> 0;
  }
  return `sha256:${hash.toString(16).padStart(8, "0").repeat(2)}`;
}

// LINK_TITLE_BYTES is the bound the real handler puts on a link's title: the
// machinery list is an index of other records, not a place to read their text.
const LINK_TITLE_BYTES = 240;

// What producing a record cost, by the run that produced it, as the real
// handler reads it out of that run's own receipt.
//
// The two runs answer differently on purpose, because the page has to say two
// different things. The discovery run's worker never reported its own session
// accounting: it has a duration and a model and no price, and the record page
// must say so in a sentence rather than print a word where a dollar figure
// goes. The challenge run priced itself, so its records carry the figures. A
// run neither of them names has no cost block at all, which is the third
// state: §9 seals a worker's accounting before a receipt leaves its host, so
// only the producing machine can price a record.
const RUN_COSTS: Record<string, Record<string, unknown>> = {
  "run_discovery-07": { usd: null, duration_s: 181.556, model: "claude-opus-5" },
  "run_challenge-08": {
    usd: 1.42,
    input_tokens: 184320,
    output_tokens: 12907,
    duration_s: 512.4,
    model: "claude-opus-5",
  },
};

// linkRow is one machinery edge: the relation, which way it points, the other
// record's id, and as much of its title as an index is allowed to carry.
function linkRow(
  edge: { kind: string; other: { id: string; label?: string } },
  direction: "from" | "to",
): Record<string, unknown> {
  const label = edge.other.label ?? "";
  const title = label.length > LINK_TITLE_BYTES ? `${label.slice(0, LINK_TITLE_BYTES)}…` : label;
  return { kind: edge.kind, direction, id: edge.other.id, ...(title ? { title } : {}) };
}

// cite turns one stored locator into a citation the page can open.
//
// The event index is the line minus one: internal/event stamps a 1-based
// record line and internal/transcript numbers the same records from zero. The
// href is computed here, as the real handler computes it, because the route a
// citation opens belongs to the server and not to the reader's client — and a
// locator this deployment cannot resolve to a session carries no href at all,
// which is how the page knows to say the excerpt is not locatable rather than
// offering a link that goes nowhere.
function cite(ref: EvidenceRef, kind: string): Record<string, unknown> {
  const line = ref.locator.line;
  const event = line > 0 ? line - 1 : 0;
  return {
    quote: ref.note ?? "",
    kind,
    ...(ref.selector ? { session_id: ref.selector } : {}),
    path: ref.locator.path,
    ...(line > 0 ? { line } : {}),
    event,
    ...(ref.selector
      ? { href: `#/sessions/${encodeURIComponent(ref.selector)}?event=${event}` }
      : {}),
  };
}

// cites maps a payload's whole citation list. It is a named pass rather than
// an inline map because every call site pairs a list with the side of the
// claim it is on, and §4.5 turns on that pairing being right.
function cites(refs: EvidenceRef[] | undefined, kind: string): Array<Record<string, unknown>> {
  return (refs ?? []).map((ref) => cite(ref, kind));
}

// Where a record came from, by the session it cites first.
//
// The real handler resolves the origin out of the sessions catalog: the first
// conversation a record cites that this deployment holds, with that session's
// own title, workspace, date and cost. The mock states the same three
// conversations ./serve.ts serves, keyed by the selector the citations carry,
// because the origin is what the record page's topic chip is filed under —
// §8.7 names a topic by the last element of the workspace — and a preview
// with no origin would preview a page with no topics.
//
// `cost_usd` and `turns` are null for the session whose harness recorded no
// usage, which is the absence the strip has to render as an absence.
const ORIGINS: Record<string, Record<string, unknown>> = {
  "codex/synthetic-alpha": {
    session_id: "codex/synthetic-alpha",
    session_title: "Design a resilient import pipeline",
    workspace: "/home/demo/projects/atlas",
    at: "2026-08-28T10:42:00Z",
    cost_usd: 4.182,
    turns: 96,
    href: "#/sessions/codex%2Fsynthetic-alpha",
  },
  "claude-code/synthetic-bravo": {
    session_id: "claude-code/synthetic-bravo",
    session_title: "Trace a cache invalidation regression",
    workspace: "/home/demo/projects/kepler",
    at: "2026-08-27T18:05:00Z",
    cost_usd: 0.42,
    turns: 11,
    href: "#/sessions/claude-code%2Fsynthetic-bravo",
  },
  "omp/synthetic-charlie": {
    session_id: "omp/synthetic-charlie",
    session_title: "",
    workspace: "/home/demo/scratch",
    at: "2026-08-22T08:30:00Z",
    cost_usd: null,
    turns: null,
    href: "#/sessions/omp%2Fsynthetic-charlie",
  },
};

// originOf takes the first citation whose session this deployment holds, in
// the order the record cites them: a record whose every citation is on another
// machine has no origin rather than a guessed one.
function originOf(refs: Array<EvidenceRef | undefined>): Record<string, unknown> | undefined {
  for (const ref of refs) {
    const origin = ref?.selector ? ORIGINS[ref.selector] : undefined;
    if (origin) return origin;
  }
  return undefined;
}

// What the operator recorded while the surface took stances. `current` is the
// last thing he said and `earlier` is what he said before it, newest first: a
// reception was appended like every other operator record, so a changed mind
// left the earlier position readable rather than replacing it — and the
// surface still reads both, because retiring a write does not delete what it
// wrote.
//
// It is seeded rather than written. The route that recorded a stance is gone,
// so the only way the read-only rendering is reachable in a browser is for
// the fixture to hold what the store holds on the operator's own machine: one
// record he agreed with after being unsure about it.
interface Stance {
  stance: string;
  reason?: string;
  at: string;
}
const current: Record<string, Stance | undefined> = {
  "pro_criteria-template": {
    stance: "agree",
    reason: "The pattern turned up twice more, which is what I said I was waiting for.",
    at: "2026-09-08T10:30:00Z",
  },
};
const earlier: Record<string, Stance[]> = {
  "pro_criteria-template": [
    { stance: "unsure", reason: "Two synthetic sessions is not a corpus.", at: "2026-08-30T09:20:00Z" },
  ],
};

function receptionBlock(id: string): Record<string, unknown> | undefined {
  const model = receptionOf(id);
  const review = reviewRecords.find((record) => record.subject.id === id);
  const decisions = (review?.decisions ?? []).map((decision) => ({
    disposition: decision.disposition,
    by: decision.reviewer_id,
    at: decision.recorded_at,
    ...(decision.note ? { note: decision.note } : {}),
  }));
  const mine = current[id];
  const mineEarlier = earlier[id] ?? [];
  if (!model && decisions.length === 0 && !mine && mineEarlier.length === 0) return undefined;
  return {
    ...(mine ? { operator: mine } : {}),
    ...(mineEarlier.length > 0 ? { history: mineEarlier } : {}),
    ...(model ?? {}),
    ...(decisions.length > 0 ? { decisions } : {}),
  };
}

function machineryBlock(input: {
  id: string;
  runID: string;
  schema: number;
  createdAt: string;
  kind: Kind;
}): Record<string, unknown> {
  const chain = chains[input.id];
  const receipt = receipts.find((entry) => entry.run_id === input.runID);
  const graph = linksOf(input.kind, input.id);
  const links = [
    ...graph.cites.edges.map((edge) => linkRow(edge, "to")),
    ...graph.cited_by.edges.map((edge) => linkRow(edge, "from")),
  ];
  const head = headOf(input.id);
  return {
    ...(head ? { revision: head } : {}),
    digest: digestOf(input.id),
    schema: input.schema,
    created_at: input.createdAt,
    run_id: input.runID,
    ...(RUN_COSTS[input.runID] ? { cost: RUN_COSTS[input.runID] } : {}),
    // The policy a run acted under, when its receipt recorded one. An older
    // receipt written before receipts carried an authority has none, and the
    // row is then absent rather than blank.
    ...(receipt?.authority.ref ? { policy_version: receipt.authority.ref } : {}),
    ...(links.length > 0 ? { links } : {}),
    ...(receipt
      ? {
          receipts: [
            {
              id: receipt.receipt_id,
              stage: input.kind === "hypothesis" ? "discovery" : "consolidation",
              // The mock's receipts carry no price, which is one of the three
              // states the real field has: a worker that could not price
              // itself reports that rather than a zero.
              cost: "unpriced",
              at: receipt.recorded_at,
            },
          ],
        }
      : {}),
    ...(chain
      ? {
          revisions: chain.entries.map((entry) => ({ id: entry.id, at: entry.recorded_at })),
        }
      : {}),
  };
}

// standingBlock answers where a record stands. An observation has none: §6.7
// makes it evidence rather than a review subject, so there is nothing to rule
// on and nothing to say about a ruling that cannot happen.
function standingBlock(id: string, kind: Kind): Record<string, unknown> | undefined {
  if (kind === "observation") return undefined;
  const head = headOf(id);
  const review = reviewRecords.find((record) => record.subject.id === id);
  // A wording a later revision replaced reads as superseded whatever the
  // review says about the record: the reader is looking at text that is no
  // longer the record's own.
  const label = head && head !== id ? "superseded" : review ? derivedStatus(review) : "new";
  return { label, tone: STANDING_TONES[label] ?? "neutral" };
}

function peel(id: string): Record<string, unknown> | null {
  const kind = kindOf(id);
  if (!kind || phasebEmpty) return null;

  const standing = standingBlock(id, kind);
  const action = kind === "observation" ? undefined : { verb: "rule", label: "Rule on this" };

  if (kind === "hypothesis") {
    const detail = hypotheses[id];
    if (!detail) return null;
    const payload = detail.hypothesis.payload;
    // A hypothesis is a bare claim: no case, no evidence of its own. The
    // origin cues, the labels and the two ranking numbers are sorting signals
    // rather than an argument, and a case panel built from them would dress a
    // guess as one.
    return {
      id,
      kind,
      title: payload.statement,
      claim: payload.statement,
      ...(standing ? { standing } : {}),
      ...(action ? { action } : {}),
      ...(receptionBlock(id) ? { reception: receptionBlock(id) } : {}),
      machinery: machineryBlock({
        id,
        runID: detail.hypothesis.run_id,
        schema: detail.hypothesis.schema_version,
        createdAt: detail.hypothesis.created_at,
        kind,
      }),
    };
  }

  if (kind === "finding") {
    const detail = findings[id];
    if (!detail) return null;
    const payload = detail.finding.payload;
    const substance = {
      ...(payload.significance ? { impact: payload.significance } : {}),
      ...(payload.scope?.length ? { scope: payload.scope.join(", ") } : {}),
    };
    const evidence = cites(payload.counter_evidence, "counter-evidence");
    const origin = originOf(payload.counter_evidence ?? []);
    return {
      id,
      kind,
      title: payload.title,
      claim: payload.pattern,
      ...(standing ? { standing } : {}),
      ...(action ? { action } : {}),
      // A finding proposes nothing, so it has no problem and no outcome: the
      // remedy is a separate proposal with its own standing.
      ...(Object.keys(substance).length > 0 ? { case: substance } : {}),
      ...(origin ? { origin } : {}),
      ...(evidence.length > 0 ? { evidence } : {}),
      ...(receptionBlock(id) ? { reception: receptionBlock(id) } : {}),
      machinery: machineryBlock({
        id,
        runID: detail.finding.run_id,
        schema: detail.finding.schema_version,
        createdAt: detail.finding.created_at,
        kind,
      }),
    };
  }

  if (kind === "proposal") {
    const proposal = proposalOf(id);
    if (!proposal) return null;
    const payload = proposal.payload;
    const substance = {
      problem: payload.problem,
      outcome: payload.outcome,
      impact: payload.impact,
      ...(payload.estimated_scope ? { scope: payload.estimated_scope } : {}),
      classification: payload.classification,
      ...(payload.uncertainty ? { uncertainty: payload.uncertainty } : {}),
      ...(payload.verification_criteria?.length
        ? { verification: payload.verification_criteria }
        : {}),
      ...(payload.risks?.length ? { risks: payload.risks } : {}),
      ...(payload.open_questions?.length ? { open_questions: payload.open_questions } : {}),
      ...(payload.prerequisites?.length ? { prerequisites: payload.prerequisites } : {}),
      ...(payload.targets?.length ? { targets: payload.targets } : {}),
    };
    const cited = [...(payload.supporting ?? []), ...(payload.conflicting ?? [])];
    const evidence = [
      ...cites(payload.supporting, "supporting"),
      ...cites(payload.conflicting, "conflicting"),
    ];
    const origin = originOf(cited);
    return {
      id,
      kind,
      title: payload.title,
      claim: payload.outcome,
      ...(standing ? { standing } : {}),
      ...(action ? { action } : {}),
      case: substance,
      ...(origin ? { origin } : {}),
      ...(evidence.length > 0 ? { evidence } : {}),
      ...(receptionBlock(id) ? { reception: receptionBlock(id) } : {}),
      machinery: machineryBlock({
        id,
        runID: proposal.run_id,
        schema: proposal.schema_version,
        createdAt: proposal.created_at,
        kind,
      }),
    };
  }

  const observation = observationOf(id);
  if (!observation) return null;
  const payload = observation.payload;
  const cited = [...(payload.evidence ?? []), ...(payload.counter_evidence ?? [])];
  const evidence = [
    ...cites(payload.evidence, "evidence"),
    ...cites(payload.counter_evidence, "counter-evidence"),
  ];
  const origin = originOf(cited);
  return {
    id,
    kind,
    title: payload.claim,
    claim: payload.claim,
    case: { impact: payload.impact, classification: payload.category },
    ...(origin ? { origin } : {}),
    ...(evidence.length > 0 ? { evidence } : {}),
    machinery: machineryBlock({
      id,
      runID: observation.run_id,
      schema: observation.schema_version,
      createdAt: observation.created_at,
      kind: "observation",
    }),
  };
}

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

export async function recordResponse(request: Request, url: URL): Promise<Response | null> {
  const path = url.pathname;
  if (!path.startsWith("/api/record/")) return null;
  const rest = path.slice("/api/record/".length);
  // The sub-resource routes — revisions, dispositions, links, invite, revive,
  // comments — are answered by ./phaseb.ts and ./comments.ts, which run
  // first. What reaches here is an id, and a path with a separator left in it
  // is some retired sub-resource — /reception is one — which falls through to
  // the unknown-route answer rather than being served here.
  const id = decodeURIComponent(rest);
  if (!id || id.includes("/")) return null;

  // A well-formed id whose family this surface cannot open, and an id whose
  // record this deployment does not hold, are different answers: the first is
  // a refusal to route, the second is an absence. Both sentences are the real
  // server's own.
  if (!kindOf(id)) {
    return json({ error: "that identifier names no record kind this surface can open" }, 400);
  }
  const record = peel(id);
  if (!record) return json({ error: "no record with that identifier" }, 404);


  if (request.method !== "GET") return json({ error: "method not allowed" }, 405);
  return json(record);
}
