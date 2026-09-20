import {
  JOB_OUTPUT_FILES,
  MATERIAL_ROOT,
  MATERIAL_SESSIONS,
  type MaterialEntry,
} from "../../contract.ts";
import {
  RESULT_SCHEMA,
  type Candidate,
  type Consolidation,
  type Evidence,
  type ExploreResult,
  type Objection,
  type QuestionDraft,
} from "../../machine/results.ts";
import type { CitationCheck } from "./citations.ts";
import { mintId, recordRow, titleCell, type RecordKind, type Row } from "./rows.ts";

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

  NOTHING HERE IS A RULING. A run may write records, edges, statuses, questions and the NEXT
  ACTIONS it proposes, and may not write a `dispositions` or a `next_action_rulings` row: a
  ruling is the operator's alone, and a table a run could reach would make Babel an agent that
  agrees with itself. The boundary is not this comment — `contract.ts`'s `INGESTIBLE_TABLES` is
  the closed set the ingest may name, and neither ledger is in it.

  EVERY IDENTIFIER IS MINTED FROM THE RUN AND THE MODEL'S OWN HANDLE ({@link mintId}), which is
  what the Go tree's resume ledger did with a table: the second settlement of one run mints the
  same identifiers, every insert is `INSERT OR IGNORE` keyed by them, and a retry neither
  duplicates a record nor loses one. The handles are the answer's `ref` fields; a family prefix
  is part of the digest, so a candidate and an observation sharing a handle are still two rows.
*/

/** The payload shape a record's own JSON declares, as {@link RESULT_SCHEMA}'s version. */
const PAYLOAD_SCHEMA = Number(RESULT_SCHEMA.slice(RESULT_SCHEMA.lastIndexOf("/") + 1));

/**
 * The four keys a claim payload carries citations under: an observation's own evidence and its
 * counter-evidence, and a proposal's supporting and conflicting material. A finding's is
 * `counter_evidence` too — it rests on observations and has no evidence of its own.
 */
const CITED_FIELDS = ["evidence", "counter_evidence", "supporting", "conflicting"] as const;

/** What a settlement needs to know about the run whose answer it is writing. */
export interface ExploreSettlement {
  readonly runId: string;
  /** ISO instant every row is stamped with: one settlement is one moment. */
  readonly at: string;
  /** The material this run was served, which is what a `cites` edge resolves a locator through. */
  readonly sessions: readonly MaterialEntry[];
  /**
   * The record identifiers this hub already holds, of those a marker in this answer names
   * ({@link markerReferences}). A marker pointing at a record nobody has is dropped with a note
   * rather than minting an edge into nothing, and the caller supplies the set because the rows
   * are built synchronously and the store is not.
   */
  readonly holds: ReadonlySet<string>;
  /**
   * WHAT BECAME OF EACH CITATION'S QUOTED TEXT (`./citations.ts`, `checkCitations`), keyed on
   * the evidence object the answer carries.
   *
   * It is here rather than on the evidence itself because a run may not mint its own verdict:
   * the submitted shape has no field for one, and the map is built by the settlement out of
   * bytes the model never touched. The Writer copies each verdict into the payload beside the
   * citation it belongs to, which is where a reader of the record finds it.
   */
  readonly checks: ReadonlyMap<Evidence, CitationCheck>;
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
 * Every row one accepted exploration claimed.
 *
 * NOTHING HERE REFUSES, and that is a property to keep rather than an omission. Every rule about
 * an item — a reference that resolves, a citation that was served, §4.2's path — is stated once,
 * in `machine/results.ts`'s `itemRefusal`, and what reaches here is the subset that already
 * cleared it. This file used to re-judge the same shapes on the way to the rows and answer
 * DIFFERENTLY: a consolidation resting on a proposal was `development-path` there and
 * `unknown-reference: no observation f1` here, for one submission, depending on which of the two
 * saw it. The Go tree's worst evaluation bug was one rule stated three times (F8), so the second
 * copy is gone and the caller has no refusal branch left to write.
 *
 * What is still here is RESOLUTION: a handle becomes the identifier this settlement minted for
 * it, and a durable identifier stays itself. A handle the validator admitted resolves to one or
 * the other by construction — and the subset it admitted is closed under the development path,
 * so a finding whose observations were refused never arrives here without them.
 */
export function exploreRows(result: ExploreResult, settlement: ExploreSettlement): ExploreWrite {
  const rows: Record<string, Row[]> = {
    [JOB_OUTPUT_FILES.records]: [],
    [JOB_OUTPUT_FILES.edges]: [],
    [JOB_OUTPUT_FILES.statusEvents]: [],
    [JOB_OUTPUT_FILES.questions]: [],
    [JOB_OUTPUT_FILES.nextActions]: [],
  };
  const notes: string[] = [];
  const writer = new Writer(rows, notes, settlement);
  for (const candidate of result.candidates) writer.candidate(candidate);
  for (const objection of result.objections) writer.objection(objection);
  for (const consolidation of result.consolidations) writer.consolidation(consolidation);
  writer.schedule(result);
  for (const question of result.questions) writer.question(question);
  writer.corrections();
  writer.nextActions(result);
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

/*
  WHAT A RECORD SAYS ABOUT ANOTHER RECORD IN ITS OWN FIRST WORDS (#347).

  Records in the corpus open with an explicit self-correction marker — `CONTRADICTS hyp_…`,
  `CITATION CORRECTION for o2 (which cited e17 in error): …`. The correction is stated; the
  EDGE is not, so a record that announces what it supersedes is, to every reader and every
  query, unrelated to it. Matching a literal opening token and writing the edge is string
  handling, and it repairs a graph no scorer could repair by reading it.

  WHAT THE 6,038-RECORD CORPUS ACTUALLY CARRIES, counted rather than assumed, because the
  grammar is shaped by it: eleven records carry a marker. TWO open `CONTRADICTS` and name
  durable identifiers. NINE open `CORRECTION` or `CITATION CORRECTION` and name no durable
  identifier at all — six name a RUN-LOCAL HANDLE (`o2`, `o12`) and three name only prose
  ("my earlier secret-residency claim"). So the premise that a correction marker points at a
  record id is false; the handle is the form the models actually use, and a handle is
  resolvable only HERE, while the settlement that mints it still exists.

  THE GRAMMAR IS NARROW BECAUSE A FALSE EDGE ASSERTS A RELATIONSHIP NOBODY STATED, which is
  strictly worse than no edge at all — nothing in the corpus tells a later reader that an edge
  was inferred. Four narrowings, each paid for by a real record:

    - The token opens the text. A record that MENTIONS `obs_…` mid-sentence is discussing it,
      not correcting it.
    - A handle is letters then digits (`o12`), never a bare word. `CORRECTION of the citation
      in the earlier claim` would otherwise offer `of`, `the` and `citation` as targets.
    - A POSSESSIVE IS NOT A TARGET. `CORRECTION of o12's citation handles` means o12, but
      `CORRECTION of observation o1's sibling` means a record o1 is not — the two are the same
      shape, and English is the only thing that tells them apart. Both are dropped; one of the
      three possessives in the corpus would have been a false edge.
    - The targets are a contiguous list right after the token. `for o2 (which cited e17 in
      error)` names o2 and NOT e17, which is an evidence handle inside an aside.

  A marker whose targets do not parse is not a parse failure: it is a marker naming prose, and
  it leaves a note like any other unresolvable one.
*/

/** The relation a marker asks for, as the `edges.kind` vocabulary spells it. */
export type MarkerRelation = "contradicts" | "corrects";

/** What a record's opening words claim about other records. */
export interface CorrectionMarker {
  readonly relation: MarkerRelation;
  /** The opening token as the record spells it, which is what a note and an edge quote. */
  readonly token: string;
  /** Every reference the marker's head names, in order. Empty when it named only prose. */
  readonly references: readonly string[];
}

const MARKER_RELATIONS: Readonly<Record<string, MarkerRelation>> = {
  CONTRADICTS: "contradicts",
  CORRECTS: "corrects",
  CORRECTION: "corrects",
};

/**
 * The opening token, after at most one all-capital qualifier — `CITATION CORRECTION` is the
 * form three of the corpus's nine corrections use, and a qualifier is the whole of the
 * difference between it and `CORRECTION`.
 */
const MARKER_OPENING = /^\s*(?:[A-Z]{2,20}[ \t]+)?(CONTRADICTS|CORRECTS|CORRECTION)(?![\w'’])/u;

/** A durable record identifier, or a run-local handle: letters then digits, never a word. */
const REFERENCE = String.raw`(?:(?:hyp|obs|fnd|pro)_[0-9a-f]{8,64}|[a-z]{1,4}[0-9]{1,4})(?![\w'’])`;

/**
 * The head: up to three lowercase connective words (`of`, `frontier observation`, `for`), then
 * a contiguous list of references separated by nothing but a comma or `and`.
 */
const MARKER_HEAD = new RegExp(
  String.raw`^(?:[ \t]+[a-z][a-z-]{0,14}){0,3}[ \t]+(${REFERENCE}(?:(?:,| and|, and)[ \t]+${REFERENCE})*)`,
  "u",
);

const REFERENCES = new RegExp(REFERENCE, "gu");

/**
 * The marker a record's own text opens with, or `null` when it opens with none.
 *
 * This is the whole grammar and the only place it is spelled. A marker with a token and no
 * parseable reference answers with an empty {@link CorrectionMarker.references}, because "it
 * said CORRECTION and named nothing" is a fact worth a note and not the same fact as "it said
 * nothing".
 */
export function correctionMarker(text: string): CorrectionMarker | null {
  const opening = MARKER_OPENING.exec(text);
  const token = opening?.[1];
  const relation = token === undefined ? undefined : MARKER_RELATIONS[token];
  if (opening === null || token === undefined || relation === undefined) return null;
  const head = MARKER_HEAD.exec(text.slice(opening[0].length));
  const list = head?.[1];
  if (list === undefined) return { relation, token, references: [] };
  return { relation, token, references: list.match(REFERENCES) ?? [] };
}

/**
 * Every durable record identifier the markers in one answer name.
 *
 * The caller resolves these against the store and hands back what it holds
 * ({@link ExploreSettlement.holds}); a handle is not here because a handle is resolved against
 * the settlement's own declarations and never against the table.
 */
export function markerReferences(result: ExploreResult): readonly string[] {
  const out = new Set<string>();
  for (const text of markedTexts(result)) {
    for (const reference of correctionMarker(text)?.references ?? []) {
      if (DURABLE_REFERENCE.test(reference)) out.add(reference);
    }
  }
  return [...out];
}

const DURABLE_REFERENCE = /^(hyp|obs|fnd|pro)_[0-9a-f]{8,64}$/u;

const FAMILIES: Readonly<Record<string, string>> = {
  hyp: "hypothesis",
  obs: "observation",
  fnd: "finding",
  pro: "proposal",
};

/**
 * A durable reference as an edge's target: the family prefix is the record kind, which is what
 * makes `to_kind` derivable without asking the store a second question.
 */
function durable(reference: string): { id: string; kind: string } | null {
  const family = FAMILIES[reference.slice(0, reference.indexOf("_"))];
  return family === undefined ? null : { id: reference, kind: family };
}

/**
 * Every text that becomes a record's own headline, which is where a marker sits.
 *
 * It enumerates the same six places {@link Writer} turns into a `records` row. A drift between
 * the two costs a durable target its existence check, which drops the edge with a note — the
 * safe direction, and the reason this is a plain list rather than a shared walk.
 */
function markedTexts(result: ExploreResult): readonly string[] {
  const out: string[] = [];
  for (const candidate of result.candidates) {
    out.push(candidate.hypothesis.statement);
    for (const observation of candidate.observations) out.push(observation.claim.claim);
    if (candidate.remedy !== undefined) out.push(candidate.remedy.proposal.title);
  }
  for (const objection of result.objections) out.push(objection.claim.claim);
  for (const consolidation of result.consolidations) {
    out.push(consolidation.finding.title);
    if (consolidation.proposal !== undefined) out.push(consolidation.proposal.title);
  }
  return out;
}

class Writer {
  private readonly hypotheses = new Map<string, string>();
  private readonly observations = new Map<string, string>();
  /** Every handle this result declared, by the row it became: the marker pass resolves here. */
  private readonly declared = new Map<string, { id: string; kind: string }>();
  /** One entry per record written, kept until every handle in the answer has been declared. */
  private readonly marked: { id: string; kind: string; text: string }[] = [];
  /** The edge identifiers already emitted, so one relation is one row however it was reached. */
  private readonly minted = new Set<string>();
  /** Every record id this settlement minted: what a support's run is known from. */
  private readonly ownRecords = new Set<string>();
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
    // A candidate rests on nothing yet: its observations hang off it, and the edge that carries
    // evidence points at a session rather than at a record.
    this.record(id, "hypothesis", candidate.hypothesis.statement, candidate.hypothesis, {
      supports: [],
    });
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
        supports: [],
      });
      this.cites(child, "observation", observation.claim.evidence);
    }
    const remedy = candidate.remedy;
    if (remedy === undefined) return;
    // §4.5's candidate proposal: the change this candidate asks for, addressing the claim beside
    // it and no finding. It runs after the hypothesis because it names it.
    const proposal = this.mint("pro", remedy.ref);
    this.record(proposal, "proposal", remedy.proposal.title, remedy.proposal, {
      supports: [id],
    });
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
    // The candidate it attacks: the row this settlement minted, or the durable identifier the
    // brief named. `machine/results.ts` has already refused anything that is neither.
    const target = this.hypotheses.get(objection.hypothesis) ?? objection.hypothesis;
    if (objection.claim.evidence.length > 0) {
      const id = this.mint("obs", objection.ref);
      this.observations.set(objection.ref, id);
      this.record(id, "observation", objection.claim.claim, objection.claim, {
        parentId: target,
        recipe: objection.recipe,
        supports: [],
      });
      this.cites(id, "observation", objection.claim.evidence);
      return;
    }
    const id = this.mint("hyp", objection.ref);
    this.hypotheses.set(objection.ref, id);
    this.record(
      id,
      "hypothesis",
      objection.claim.claim,
      {
        statement: objection.claim.claim,
        origin_cues: [`challenger objection grounded in ${objection.grounds}`],
        provisional_labels: ["objection"],
        novelty: 0,
        priority: 0,
        notes: objection.claim.category,
      },
      // An objection CONTRADICTS the candidate it attacks, which is disagreement rather than
      // support: the read-time count is over `consolidates` and `addresses` alone.
      { supports: [] },
    );
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
    const supports = consolidation.observations.map((ref) => this.observations.get(ref) ?? ref);
    const id = this.mint("fnd", consolidation.ref);
    this.record(id, "finding", consolidation.finding.title, consolidation.finding, { supports });
    for (const [position, support] of supports.entries()) {
      this.edge("consolidates", id, "finding", support, "observation", position, "");
    }
    const proposal = consolidation.proposal;
    if (proposal === undefined) return;
    const proposalId = this.mint("pro", `${consolidation.ref}/proposal`);
    this.record(proposalId, "proposal", proposal.title, proposal, { supports: [id] });
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

  /**
   * Every edge a record's own opening words asked for ({@link correctionMarker}).
   *
   * It runs AFTER the whole answer is walked because a marker may name a handle declared later
   * in the same result — a consolidation correcting an observation the model listed after it
   * is ordinary — and a pass interleaved with creation would resolve by luck of ordering.
   *
   * NOTHING HERE REFUSES. A marker is a claim about the corpus, not about this answer's
   * integrity: a run that referred to a record since removed, or wrote prose where an
   * identifier belongs, has still produced the records it produced. Every unresolvable marker
   * leaves a note and the answer stands.
   */
  corrections(): void {
    for (const source of this.marked) {
      const marker = correctionMarker(source.text);
      if (marker === null) continue;
      if (marker.references.length === 0) {
        this.notes.push(
          `${source.id} opens ${marker.token} and names no identifier, so nothing was linked`,
        );
        continue;
      }
      for (const [position, reference] of marker.references.entries()) {
        const target = this.marks(source.id, marker.token, reference);
        if (target === null) continue;
        this.edge(
          marker.relation,
          source.id,
          source.kind,
          target.id,
          target.kind,
          position,
          `the record's own text opens ${marker.token}`,
        );
      }
    }
  }

  /**
   * WHAT THIS RUN PROPOSED BE DONE NEXT, as `next_actions` rows (#340).
   *
   * It runs last, for the reason {@link corrections} does: a proposal may name a handle the
   * model declared later in the same result, and resolving as the answer is walked would
   * resolve by luck of ordering.
   *
   * NOTHING HERE IS A RULING, and nothing here is carried out. A row is a rendered choice the
   * operator accepts or declines through a door of his own, and the acceptance lands in
   * `next_action_rulings`, which no output file reaches. §4.6 keeps publishing, applying and
   * writing to a source repository outside Babel entirely, so accepting a `draft-issue` opens
   * no issue: this plugin holds no credential for one and has no network path to it.
   *
   * A PROPOSAL ABOUT A RECORD THIS HUB DOES NOT HOLD IS DROPPED WITH A NOTE rather than
   * refused. `parseExploreResult` has already refused anything that is neither a handle this
   * result declared nor a record identifier, so what reaches here is a well-formed reference
   * to a record that is not in this deployment — and the records the run did produce are
   * unaffected by a suggestion about one that is not.
   *
   * The identifier is a digest of the run, the kind and the subject, so a retry mints the same
   * row and a model proposing the same action on the same record twice proposes it once.
   */
  nextActions(result: ExploreResult): void {
    for (const action of result.next_actions) {
      const local = this.declared.get(action.record);
      const target = local?.id ?? (this.settlement.holds.has(action.record) ? action.record : null);
      if (target === null) {
        this.notes.push(
          `a ${action.kind} was proposed on ${action.record}, which this hub does not hold, so it was dropped`,
        );
        continue;
      }
      this.rows[JOB_OUTPUT_FILES.nextActions]?.push({
        id: mintId("nxt", this.settlement.runId, `${action.kind}|${target}`),
        record_id: target,
        kind: action.kind,
        proposed_by_kind: "run",
        proposed_by_id: this.settlement.runId,
        summary: titleCell(action.summary),
        created_at: this.settlement.at,
        payload: JSON.stringify({
          schema: PAYLOAD_SCHEMA,
          summary: action.summary,
          rationale: action.rationale,
          ...(action.workspace === "" ? {} : { workspace: action.workspace }),
        }),
      });
    }
  }

  /**
   * What one reference in a marker points at: a handle this result declared, then a durable
   * identifier this hub holds, then nothing with a note saying which of the two it failed.
   *
   * A record may not mark ITSELF. A model restating its own handle inside its own claim is
   * a self-loop, and an edge from a record to itself is read by every consumer as a relation.
   */
  private marks(
    from: string,
    token: string,
    reference: string,
  ): { id: string; kind: string } | null {
    const local = this.declared.get(reference);
    const target = local ?? (this.settlement.holds.has(reference) ? durable(reference) : null);
    if (target === null) {
      this.notes.push(
        DURABLE_REFERENCE.test(reference)
          ? `${from} opens ${token} against ${reference}, which this hub does not hold, so it was dropped`
          : `${from} opens ${token} against ${reference}, which this result did not declare, so it was dropped`,
      );
      return null;
    }
    if (target.id === from) return null;
    return target;
  }

  // -------------------------------------------------------------------------- resolution

  private mint(prefix: string, ref: string): string {
    const id = mintId(prefix, this.settlement.runId, ref);
    // THE HANDLE-TO-ROW MAPPING EXISTS ONLY HERE, for the length of one settlement. Nine of
    // the corpus's eleven self-correction markers name a handle, and the sweep over the
    // imported corpus cannot resolve one: the run that coined `o12` settled long ago and
    // nothing wrote the mapping down. Keeping it for the marker pass is the whole reason a
    // correction written from now on becomes an edge and a correction written before does not.
    const family = FAMILIES[prefix];
    if (family !== undefined) this.declared.set(ref, { id, kind: family });
    return id;
  }

  /*
    A HANDLE BECOMES WHAT THIS SETTLEMENT MINTED FOR IT, and a durable identifier stays itself.
    Both resolutions used to be guarded here by a refusal of their own — "neither a candidate
    this result declared nor a hypothesis identifier", "no observation f1" — over shapes
    `machine/results.ts` had already judged, in its own words and sometimes under a different
    code. The guards are gone with the second copy: what arrives here is the subset the one
    validator admitted, closed under the development path.
  */

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

  /**
   * One record, with what it rests on stated rather than inferred: `supports` names the records
   * its `consolidates` or `addresses` edges will point at, and it is required so that a claim
   * shape added later has to answer the question ({@link recordRow}).
   */
  private record(
    id: string,
    kind: RecordKind,
    title: string,
    payload: unknown,
    of: {
      readonly parentId?: string;
      readonly recipe?: { readonly id: string; readonly version: number };
      readonly supports: readonly string[];
    },
  ): void {
    this.rows[JOB_OUTPUT_FILES.records]?.push(
      recordRow({
        id,
        kind,
        runId: this.settlement.runId,
        at: this.settlement.at,
        title,
        payload: {
          schema: PAYLOAD_SCHEMA,
          ...this.checked(payload as Record<string, unknown>),
        },
        supports: of.supports,
        ownRecords: this.ownRecords,
        parentId: of.parentId ?? null,
        recipe: of.recipe ?? null,
      }),
    );
    // WHAT THIS SETTLEMENT WROTE ITSELF, which is how a later claim in the same answer knows
    // that a record it rests on carries this run's `run_id` without asking the store.
    this.ownRecords.add(id);
    // THE RECORD'S OWN WORDS, kept for the marker pass. `titleCell` bounds the column at 200
    // characters and a marker that fell across that boundary would be read as prose, so what
    // is parsed is the text the model wrote and not the cell the table holds.
    this.marked.push({ id, kind, text: title });
  }

  /**
   * THE PAYLOAD WITH EACH CITATION'S VERDICT BESIDE IT (#348).
   *
   * The four keys are the four places a claim shape carries citations, and they are listed
   * rather than walked for the same reason {@link markedTexts} is a list: a shape added later
   * must come here and say whether its citations are checked, and a reflective walk would
   * answer "yes" for a field nobody had thought about.
   *
   * A verdict is written only where the settlement made one, so an imported record and a
   * record whose citations nothing could read keep the shape they had — and `verified` is
   * never implied by absence.
   */
  private checked(payload: Record<string, unknown>): Record<string, unknown> {
    if (this.settlement.checks.size === 0) return payload;
    let out = payload;
    for (const field of CITED_FIELDS) {
      const held = payload[field];
      if (!Array.isArray(held)) continue;
      const written = held.map((item: unknown) => {
        const check = this.settlement.checks.get(item as Evidence);
        return check === undefined ? item : { ...(item as object), verification: check };
      });
      if (out === payload) out = { ...payload };
      out[field] = written;
    }
    return out;
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
    // ONE RELATION IS ONE ROW, however many ways the answer reached it. An objection that also
    // opens CONTRADICTS against the hypothesis it attacks asks for the same edge twice, and the
    // identifier is a digest of the relation rather than of the path taken to it — so the
    // second row would collide at the ingest's `INSERT OR IGNORE` and be counted as written.
    const id = mintId("edg", this.settlement.runId, `${kind}|${fromId}|${toId}`);
    if (this.minted.has(id)) return;
    this.minted.add(id);
    this.rows[JOB_OUTPUT_FILES.edges]?.push({
      id,
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
