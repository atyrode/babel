import {
  JOB_OUTPUT_FILES,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  type MaterialEntry,
} from "../../contract.ts";
import {
  REFUSALS,
  RESULT_SCHEMA,
  ResultRefusal,
  type Candidate,
  type Consolidation,
  type Evidence,
  type ExploreResult,
  type Objection,
  type QuestionDraft,
} from "../../machine/results.ts";
import { mintId, titleCell, type Row } from "./rows.ts";

/*
  AN ACCEPTED EXPLORATION, TURNED INTO THE ROWS IT CLAIMED.

  This is the other half of a settlement, and until it existed the first half was the whole of
  it: `settleSession` read the answer, checked every citation against the material and wrote a
  receipt, and the hypotheses, observations, findings and proposals the model had actually
  produced went nowhere. Every record this plugin held was imported Go-era history; nothing it
  ran ever added one. Ported from `v0.4.0:internal/explore/{records.go,stages.go,questions.go}`, whose
  `putHypothesis`/`putObservation`/`putFinding`/`putProposal`/`schedule`/`ask` are the reference
  for what one answer becomes.

  THE DEVELOPMENT PATH IS THE STRUCTURE, not a convention laid over it (§4.2). A candidate is a
  hypothesis; an observation hangs off exactly one hypothesis as its `parent_id` and carries the
  locators that make it evidence; a finding CONSOLIDATES observations and nothing else; a
  proposal ADDRESSES the finding or the hypothesis it answers. Those four sentences are the
  edges, in that direction, and they are the direction the crossing wrote every existing edge in
  (`tools/import.ts`: `consolidates` finding→observation, `addresses` proposal→finding and
  proposal→hypothesis, `cites` record→session).

  NOTHING HERE IS A RULING. A run may write records, edges, statuses and questions, and may not
  write a `dispositions` row: a ruling is the operator's alone, and a table a run could reach
  would make Babel an agent that agrees with itself.

  EVERY IDENTIFIER IS MINTED FROM THE RUN AND THE MODEL'S OWN HANDLE ({@link mintId}), which is
  what the Go tree's resume ledger did with a table: the second settlement of one run mints the
  same identifiers, every insert is `INSERT OR IGNORE` keyed by them, and a retry neither
  duplicates a record nor loses one. The handles are the answer's `ref` fields; a family prefix
  is part of the digest, so a candidate and an observation sharing a handle are still two rows.
*/

/** The payload shape a record's own JSON declares, as {@link RESULT_SCHEMA}'s version. */
const PAYLOAD_SCHEMA = Number(RESULT_SCHEMA.slice(RESULT_SCHEMA.lastIndexOf("/") + 1));

/** What a settlement needs to know about the run whose answer it is writing. */
export interface ExploreSettlement {
  readonly runId: string;
  /** ISO instant every row is stamped with: one settlement is one moment. */
  readonly at: string;
  /** The material this run was served, which is what a `cites` edge resolves a locator through. */
  readonly sessions: readonly MaterialEntry[];
}

/** The rows one answer becomes, keyed by the output file the ingest binds to each table. */
export type ExploreRows = Readonly<Record<string, readonly Row[]>>;

export interface ExploreWrite {
  readonly rows: ExploreRows;
  /**
   * What was recorded and dropped rather than refused. A disposal naming a handle the result
   * never declared loses only its scheduling note, so the run's records stand — the Go tree
   * warned for the same reason (`stages.go`: "the candidate the note was about does not exist").
   */
  readonly notes: readonly string[];
}

/**
 * Every row one accepted exploration claimed, or the refusal that stops the whole answer.
 *
 * A refusal here is the same kind of event as a schema refusal: the model answered, the
 * deployment paid, and the answer did not stand — so the caller writes the receipt at cost and
 * inserts NOTHING. It is all-or-nothing on purpose. A finding whose observations were dropped is
 * a consolidation of records that do not exist, which is exactly the shape §4.2 forbids, and
 * half a development path is worse than none: nobody can tell later which half is missing.
 */
export function exploreRows(
  result: ExploreResult,
  settlement: ExploreSettlement,
): ExploreWrite | { refusal: ResultRefusal } {
  const rows: Record<string, Row[]> = {
    [JOB_OUTPUT_FILES.records]: [],
    [JOB_OUTPUT_FILES.edges]: [],
    [JOB_OUTPUT_FILES.statusEvents]: [],
    [JOB_OUTPUT_FILES.questions]: [],
  };
  const notes: string[] = [];
  const writer = new Writer(rows, notes, settlement);
  try {
    for (const candidate of result.candidates) writer.candidate(candidate);
    for (const objection of result.objections) writer.objection(objection);
    for (const consolidation of result.consolidations) writer.consolidation(consolidation);
    writer.schedule(result);
    for (const question of result.questions) writer.question(question);
  } catch (error) {
    if (error instanceof ResultRefusal) return { refusal: error };
    throw error;
  }
  return { rows, notes };
}

/**
 * The selector of each session in the material, by every path a locator may name it with.
 *
 * It is the same three spellings `unservedLocator` admits, and for the same reason: the
 * prompt names the material's files relative to its root, a model may cite either spelling, and
 * a `cites` edge addresses the SESSION rather than the file — so the path has to resolve to the
 * selector the catalog holds.
 */
function selectors(sessions: readonly MaterialEntry[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const entry of sessions) {
    out.set(entry.file, entry.selector);
    out.set(`${MATERIAL_SESSIONS}/${entry.file}`, entry.selector);
    out.set(`${MATERIAL_ROOT}/${MATERIAL_SESSIONS}/${entry.file}`, entry.selector);
  }
  return out;
}

/**
 * A durable identifier of one record family, as `RecordIdSchema` shapes them.
 *
 * A handle is resolved BY FAMILY rather than by looking like an identifier: a consolidation
 * citing `hyp_…` from the brief has skipped the development path just as surely as one citing a
 * candidate this result declared, and a check that admitted any record id would let it through.
 */
const recordId = (family: string): RegExp => new RegExp(`^${family}_[0-9a-f]{8,64}$`, "u");
const HYPOTHESIS_ID = recordId("hyp");
const OBSERVATION_ID = recordId("obs");

class Writer {
  private readonly hypotheses = new Map<string, string>();
  private readonly observations = new Map<string, string>();
  private readonly sessions: Map<string, string>;

  constructor(
    private readonly rows: Record<string, Row[]>,
    private readonly notes: string[],
    private readonly settlement: ExploreSettlement,
  ) {
    this.sessions = selectors(settlement.sessions);
  }

  /** One candidate: the hypothesis, its observations, and the change it proposes for itself. */
  candidate(candidate: Candidate): void {
    const id = this.mint("hyp", candidate.ref);
    this.hypotheses.set(candidate.ref, id);
    this.record(id, "hypothesis", candidate.hypothesis.statement, candidate.hypothesis);
    // THE LIFECYCLE STARTS AT UNTRIAGED, written with the record rather than inferred from its
    // absence: `status_events` is the history the frontier reads a standing off, and a
    // hypothesis with no event at all is one no listing can rank or defer.
    this.status(id, 0, "untriaged", "");
    for (const observation of candidate.observations) {
      const child = this.mint("obs", observation.ref);
      this.observations.set(observation.ref, child);
      this.record(child, "observation", observation.claim.claim, observation.claim, {
        parentId: id,
        recipe: observation.recipe,
      });
      this.cites(child, "observation", observation.claim.evidence);
    }
    const remedy = candidate.remedy;
    if (remedy === undefined) return;
    // §4.5's candidate proposal: the change this candidate asks for, addressing the claim beside
    // it and no finding. It runs after the hypothesis because it names it.
    const proposal = this.mint("pro", remedy.ref);
    this.record(proposal, "proposal", remedy.proposal.title, remedy.proposal);
    this.edge("addresses", proposal, "proposal", id, "hypothesis", 0, "");
  }

  /**
   * One challenger criticism: an observation when it carries locators, and a contradicting
   * candidate when it does not.
   *
   * The branch is §5.4's authority made mechanical, and it is the Go tree's (`putObjection`):
   * §4.3 forbids an evidence-free observation, so an ungrounded criticism becomes an idea to
   * investigate rather than a claim established by being asserted.
   */
  objection(objection: Objection): void {
    const target = this.hypothesis(objection.hypothesis, "objection");
    if (objection.claim.evidence.length > 0) {
      const id = this.mint("obs", objection.ref);
      this.observations.set(objection.ref, id);
      this.record(id, "observation", objection.claim.claim, objection.claim, {
        parentId: target,
        recipe: objection.recipe,
      });
      this.cites(id, "observation", objection.claim.evidence);
      return;
    }
    const id = this.mint("hyp", objection.ref);
    this.hypotheses.set(objection.ref, id);
    this.record(id, "hypothesis", objection.claim.claim, {
      statement: objection.claim.claim,
      origin_cues: [`challenger objection grounded in ${objection.grounds}`],
      provisional_labels: ["objection"],
      novelty: 0,
      priority: 0,
      notes: objection.claim.category,
    });
    this.status(id, 0, "untriaged", "");
    this.edge(
      "contradicts",
      id,
      "hypothesis",
      target,
      "hypothesis",
      0,
      `challenger objection resting on ${objection.grounds}`,
    );
  }

  /** One consolidation: what recurs across observations, and the change it asks for. */
  consolidation(consolidation: Consolidation): void {
    const supports = consolidation.observations.map((ref) => this.observation(ref));
    const id = this.mint("fnd", consolidation.ref);
    this.record(id, "finding", consolidation.finding.title, consolidation.finding);
    for (const [position, support] of supports.entries()) {
      this.edge("consolidates", id, "finding", support, "observation", position, "");
    }
    const proposal = consolidation.proposal;
    if (proposal === undefined) return;
    const proposalId = this.mint("pro", `${consolidation.ref}/proposal`);
    this.record(proposalId, "proposal", proposal.title, proposal);
    this.edge("addresses", proposalId, "proposal", id, "finding", 0, "");
  }

  /**
   * What this run said it would not develop. §5.2 keeps it to scheduling: a deferred or rejected
   * candidate keeps its record and its wording, and only the lifecycle history changes.
   *
   * A rejection outranks a deferral of the same candidate — the stronger verdict is the one the
   * stage reached — and each candidate takes exactly one event, at `seq` 1, after the
   * `untriaged` its creation wrote.
   */
  schedule(result: ExploreResult): void {
    const scheduled = new Map<string, { status: string; reason: string }>();
    for (const disposal of result.deferred) {
      const id = this.scheduled(disposal.hypothesis, "deferral");
      if (id === "") continue;
      scheduled.set(id, { status: "deferred", reason: disposal.reason });
    }
    for (const disposal of result.rejected) {
      const id = this.scheduled(disposal.hypothesis, "rejection");
      if (id === "") continue;
      scheduled.set(id, { status: "rejected", reason: disposal.reason });
    }
    for (const [id, verdict] of scheduled) this.status(id, 1, verdict.status, verdict.reason);
  }

  /**
   * One thing the corpus could not settle and a person can (§4.8).
   *
   * A run may raise one at all because it authorizes nothing: it is a request that somebody else
   * authorize something, so accepting it costs the ledger no authority. Two things follow, and
   * both are the Go tree's (`questions.go`). The KIND is always `acquire-context` — the ledger's
   * other kinds are conditions only the ledger's own machinery can see. The CLASS is derived
   * from the work rather than declared: a question blocking a candidate this run wrote ranks
   * above one that satisfies curiosity, and a self-graded class would make every question
   * blocking within a week.
   *
   * The subjects stay strings in the payload. A run does not mint identity (§4.8 puts entity
   * creation behind an attributed operator act), so a subject naming something the ledger does
   * not hold is a gap in the ledger to read, not an `entities` row this run may create.
   */
  question(draft: QuestionDraft): void {
    const blocked =
      draft.hypothesis === "" ? "" : (this.hypotheses.get(draft.hypothesis) ?? draft.hypothesis);
    this.rows[JOB_OUTPUT_FILES.questions]?.push({
      id: mintId("qst", this.settlement.runId, draft.ref),
      kind: "acquire-context",
      class: blocked === "" ? "curiosity" : "blocking",
      text: draft.prompt,
      why: draft.why_asked,
      dedupe_key: null,
      raised_by_kind: "run",
      raised_by_id: this.settlement.runId,
      payload: JSON.stringify({
        schema: PAYLOAD_SCHEMA,
        prompt: draft.prompt,
        why_asked: draft.why_asked,
        subjects: draft.subjects,
        predicates: draft.predicates,
        sensitivity: "routine",
        expected_authority: "operator",
        work: blocked === "" ? [] : [{ kind: "hypothesis", id: blocked, blocking: true }],
      }),
      created_at: this.settlement.at,
    });
  }

  // -------------------------------------------------------------------------- resolution

  private mint(prefix: string, ref: string): string {
    return mintId(prefix, this.settlement.runId, ref);
  }

  /** A handle naming a candidate: one this result declared, or a durable id the brief named. */
  private hypothesis(ref: string, what: string): string {
    const local = this.hypotheses.get(ref);
    if (local !== undefined) return local;
    if (HYPOTHESIS_ID.test(ref)) return ref;
    throw new ResultRefusal(
      REFUSALS.unknownReference,
      `the ${what} names ${ref}, which is neither a candidate this result declared nor a hypothesis identifier`,
    );
  }

  /**
   * A handle naming an observation, with §4.2's path enforced.
   *
   * A consolidation resting on a CANDIDATE is the mandatory path skipped rather than a missing
   * reference, and the two are separate refusals because they are separate mistakes: one says
   * the synthesizer cited a guess as if it were evidence, the other says it cited nothing at all.
   */
  private observation(ref: string): string {
    const local = this.observations.get(ref);
    if (local !== undefined) return local;
    if (this.hypotheses.has(ref) || HYPOTHESIS_ID.test(ref)) {
      throw new ResultRefusal(
        REFUSALS.developmentPath,
        `${ref} is a candidate hypothesis, not a locator-backed observation`,
      );
    }
    if (OBSERVATION_ID.test(ref)) return ref;
    throw new ResultRefusal(REFUSALS.unknownReference, `no observation ${ref}`);
  }

  /** A handle a disposal named, or the empty string with the note saying it was dropped. */
  private scheduled(ref: string, what: string): string {
    const local = this.hypotheses.get(ref);
    if (local !== undefined) return local;
    this.notes.push(
      `the ${what} of ${ref} names no candidate this run declared, so it was dropped`,
    );
    return "";
  }

  // -------------------------------------------------------------------------- rows

  private record(
    id: string,
    kind: string,
    title: string,
    payload: unknown,
    of?: { parentId?: string; recipe?: { id: string; version: number } },
  ): void {
    this.rows[JOB_OUTPUT_FILES.records]?.push({
      id,
      kind,
      // A NEW RECORD IS ITS OWN ROOT AT SEQUENCE ZERO. A correction is a supersession — the
      // table refuses an update by trigger — so nothing a run writes ever revises a row.
      root_id: id,
      supersedes_id: null,
      seq: 0,
      parent_id: of?.parentId ?? null,
      run_id: this.settlement.runId,
      // ONLY AN OBSERVATION CARRIES A RECIPE, which is what the answer attributes and what the
      // crossing wrote (`tools/import.ts`: the recipe columns are an observation's alone). A
      // hypothesis, a finding and a proposal are reached THROUGH a method and are not products
      // of one, and naming the run's first recipe on them would be an attribution nobody made.
      recipe_id: of?.recipe?.id ?? null,
      recipe_version: of?.recipe?.version ?? null,
      actor_kind: "run",
      actor_id: this.settlement.runId,
      title: titleCell(title),
      created_at: this.settlement.at,
      payload: JSON.stringify({ schema: PAYLOAD_SCHEMA, ...(payload as Record<string, unknown>) }),
    });
  }

  private status(recordId: string, seq: number, status: string, reason: string): void {
    this.rows[JOB_OUTPUT_FILES.statusEvents]?.push({
      id: mintId("ste", this.settlement.runId, `${recordId}|${String(seq)}`),
      record_id: recordId,
      seq,
      status,
      run_id: this.settlement.runId,
      actor_kind: "run",
      actor_id: this.settlement.runId,
      reason: reason === "" ? null : reason,
      recorded_at: this.settlement.at,
    });
  }

  /**
   * The sessions one claim reached for, as edges.
   *
   * The edge carries the SESSION and nothing about where inside it: a citation's line and its
   * note live in the record's own payload, which is what makes a claim readable on a hub that
   * cannot open the conversation it names (`store/store.ts`, `citations`). Only supporting
   * evidence mints an edge — counter-evidence is the claim's own honesty about itself, and
   * counting it as material reached for would inflate every corroboration measure with it.
   *
   * A locator whose path the material does not name cannot be reached here: the caller has
   * already refused the whole answer for it, which is the only reason this can skip silently.
   */
  private cites(fromId: string, fromKind: string, evidence: readonly Evidence[]): void {
    for (const [position, cited] of evidence.entries()) {
      const selector = this.sessions.get(cited.locator.path);
      if (selector === undefined) continue;
      this.edge("cites", fromId, fromKind, selector, "session", position, cited.note);
    }
  }

  private edge(
    kind: string,
    fromId: string,
    fromKind: string,
    toId: string,
    toKind: string,
    position: number,
    note: string,
  ): void {
    this.rows[JOB_OUTPUT_FILES.edges]?.push({
      id: mintId("edg", this.settlement.runId, `${kind}|${fromId}|${toId}`),
      kind,
      from_kind: fromKind,
      from_id: fromId,
      to_kind: toKind,
      to_id: toId,
      position,
      note: note === "" ? null : note,
      actor_kind: "run",
      actor_id: this.settlement.runId,
      created_at: this.settlement.at,
    });
  }
}
