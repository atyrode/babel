import { useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  addReviewContext,
  decideReview,
  type Disposition,
  type ReviewSubjectType,
} from "./api";
import { Badge, FallibilityNote, unescapeWhitespace, type Tone } from "./analysis";
import { errorMessage, formatTime } from "./format";
import {
  putReception,
  type EvidenceKind,
  type ModelReception,
  type ModelRole,
  type OperatorStance,
  type RecordCase,
  type RecordEvidence,
  type RecordKind,
  type RecordMachinery,
  type RecordPeel,
  type RecordReception,
  type StandingTone,
} from "./recordapi";

// One record, peeled.
//
// "The complexity of the data is for Babel itself, the user really only needs
// the surface, and to be able to dig when needed." Everything in this file
// follows from that sentence. A record has five depths and the reader chooses
// one; he does not choose a page. Depth 1 is the claim, its standing and the
// single act it wants. Depth 2 is the case, in prose, with no identifiers in
// it. Depth 3 is the evidence. Depth 4 is the reception. Depth 5 is the
// machinery — every id, digest and receipt the object carries.
//
// The three rules that shape the code rather than the layout:
//
//   - Nothing here fetches. The whole record arrives in one response, so
//     opening depth 4 is a disclosure and never a request; a reader who digs
//     waits for nothing and a slow section cannot exist.
//   - An absent section is absent. Not an empty heading, not a zero: a
//     proposal that names no risk is not a proposal whose risks are none, and
//     a heading reading "What could go wrong" over nothing claims that nothing
//     could. Every renderer below returns null rather than a frame.
//   - Ids live at depth 5 only. A reader at depths 1-3 sees no hex, because a
//     person deciding whether a suggestion is right has no use for its digest,
//     and a page that shows him one is asking him to rule on an identifier.
//
// Class inventory, so the markup and the stylesheet can be checked against
// each other. styles.css is NavShell's file and every rule named here lives in
// it; this file adds no stylesheet of its own:
//
//   surface        a plain container
//   panel          a labelled group — the blocks inside a depth
//   quote          untrusted model text, always via unescapeWhitespace
//   peel           the <details> of one depth
//   peel-open      that <details> while open
//   peel-body      the body wrapper inside a <details>
//   peel-count     the count in a <summary>
//   peel-voice     the operator's stance row and its reason field
//   peel-stance    one stance button; selected is [aria-pressed="true"]
//   peel-list      a ul/ol of prose items
//   peel-cite      an evidence source line
//   peel-rows      a dl of dt/dd machinery pairs
//
// plus the surviving utilities — muted, secondary, mono, sr-only, spinner,
// primary-button, inline-error, untrusted-inline, badge tone-* through
// analysis.tsx's Badge — and the disposition form's own four names, which move
// here with the form itself: disposition-set, disposition-option, active,
// decide-field.

// standingTone maps the record's own four-tone judgement onto the interface's
// colour scale. The judgement is the server's: whether "superseded" reads as
// bad or merely neutral is a product decision and belongs where the standing
// is computed, not in a switch that each page would get subtly differently.
function standingTone(tone: StandingTone): Tone {
  switch (tone) {
    case "good":
      return "green";
    case "bad":
      return "red";
    case "warn":
      return "amber";
    default:
      return "neutral";
  }
}

const KIND_WORDS: Record<RecordKind, string> = {
  proposal: "Proposal",
  finding: "Finding",
  hypothesis: "Hypothesis",
  observation: "Observation",
};

// What the standing means, in the words a reader needs at depth 1. The badge
// says which standing it is; this says what that standing does to the record,
// which is the part an operator deciding whether to care actually needs.
const STANDING_SENTENCES: Record<string, string> = {
  new: "Nobody has ruled on this yet.",
  accepted: "Accepted — endorsed for projection and follow-on work.",
  rejected: "Rejected. The record is kept, visibly.",
  deferred: "Deferred. Not now; it stays in the queue's history.",
  duplicate: "A duplicate. It points at an original record.",
  reopened: "Reopened. The earlier ruling stays in the history, and this is undecided again.",
  "refine-requested":
    "Rejected with a refinement requested — a run has been authorized to try the record again.",
  superseded: "Superseded by a later revision of the same record.",
};

// The operator's three receptions. The explanatory sentence is four words
// because that is the honest length: a paragraph explaining that an opinion is
// not an authority would be longer than the opinion.
const STANCES: Array<{ value: OperatorStance; label: string }> = [
  { value: "agree", label: "Agree" },
  { value: "disagree", label: "Disagree" },
  { value: "unsure", label: "Unsure" },
];

// The §4.12 assessment roles, as what the reviewer was asked. A role is the
// question a run answered, and naming it is what keeps four assessments from
// reading as four votes on the same thing.
const ROLE_WORDS: Record<ModelRole, string> = {
  reception: "on whether it holds up",
  evidence: "on whether the evidence supports it",
  challenge: "challenging it",
  comparison: "comparing it with others",
  outcome: "on what came of it",
  relevance: "on whether it matters",
};

// Which side of the claim an excerpt is on, said in words. §4.5 requires a
// proposal to state what conflicts with it, so the side is stated first and
// never left to the tint alone: a reader skimming must not take conflicting
// material for support.
const EVIDENCE_SIDES: Record<EvidenceKind, string> = {
  supporting: "Supports the claim",
  evidence: "Cited as evidence",
  conflicting: "Conflicts with the claim",
  "counter-evidence": "Counter-evidence, cited by the record against itself",
};

// What a reviewer's vote says. Kept separate from the operator's words on
// purpose: a run voting support and a person agreeing are not the same act,
// and §4.12 separates them by attribution. Nothing sums the two.
const MODEL_STANCE_WORDS: Record<string, string> = {
  support: "supports it",
  oppose: "opposes it",
  unsure: "is unsure",
};

// The five §4.7 dispositions, each with the sentence a reviewer needs before
// choosing it, carried here unchanged from the review page this record page
// replaces. `reject-and-refine` is deliberately absent: it authorizes a
// refinement request and belongs to the CLI until this surface grows the full
// guidance flow.
//
// `reopen` is the one that opens rather than closes, and its sentence says so
// plainly: it is offered because an operator who is told a record has been
// reconsidered needs somewhere to act on that, and accepting a record he has
// not re-read would be the only alternative.
const DISPOSITIONS: Array<{ value: Disposition; label: string; hint: string }> = [
  { value: "accept", label: "Accept", hint: "Endorse this record for projection and follow-on work." },
  { value: "reject", label: "Reject", hint: "Record disagreement. The record is kept, visibly rejected." },
  { value: "defer", label: "Defer", hint: "Not now. The record stays in the queue's history." },
  { value: "duplicate", label: "Duplicate", hint: "Points at an original record, which you name below." },
  {
    value: "reopen",
    label: "Reopen",
    hint: "Undecide it. The earlier decision stays in the history, the status returns to new, and your reason is required.",
  },
];

// reviewSubject answers whether a record kind can carry a review decision.
// internal/review answers "this record kind carries no review decision" for an
// observation, so no disposition is ever asked about one and the ruling
// control is absent rather than present and refused.
function reviewSubject(kind: RecordKind): ReviewSubjectType | null {
  switch (kind) {
    case "proposal":
    case "finding":
    case "hypothesis":
      return kind;
    default:
      return null;
  }
}

// Peel is one depth: a native <details>, because everything the disclosure
// needs — a focusable control, the expanded state announced to a screen
// reader, the open state in the DOM for CSS — is already there. A div with a
// click handler and aria-expanded would be a reimplementation with fewer
// keyboard bindings.
//
// The open state is mirrored into React state so `peel-open` tracks the native
// `open` attribute; the stylesheet defines both and either would do, but a
// reader collapsing a depth should not depend on which one a browser honours.
function Peel({
  title,
  count,
  note,
  open: initiallyOpen = false,
  children,
}: {
  title: string;
  count?: number;
  note?: string;
  open?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(initiallyOpen);
  return (
    <details
      className={open ? "peel peel-open" : "peel"}
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary>
        {title}
        {count !== undefined && <span className="peel-count">{count}</span>}
        {note && <span className="muted">{note}</span>}
      </summary>
      <div className="peel-body">{children}</div>
    </details>
  );
}

// Prose renders one of the record's paragraphs under the question it answers.
// The text is the model's, so it goes through the sanitizer's inverse and
// renders inside `quote`: a reader can always tell the record's wording from
// Babel's chrome. A field the record does not hold renders nothing.
function Prose({ label, text }: { label: string; text: string | undefined }) {
  if (!text || !text.trim()) return null;
  return (
    <section className="panel">
      <h3>{label}</h3>
      <p className="quote untrusted-inline">{unescapeWhitespace(text)}</p>
    </section>
  );
}

// Points renders one of the record's lists under the question it answers. An
// empty list is not a list of nothing, so it renders nothing at all.
function Points({ label, items }: { label: string; items: string[] | undefined }) {
  if (!items || items.length === 0) return null;
  return (
    <section className="panel">
      <h3>{label}</h3>
      <ul className="peel-list">
        {items.map((item, index) => (
          <li className="quote untrusted-inline" key={`${index}-${item}`}>
            {unescapeWhitespace(item)}
          </li>
        ))}
      </ul>
    </section>
  );
}

// Row is one machinery pair. Absent means absent here too: a record with no
// policy version shows no policy row rather than a dash, because a dash reads
// as a value that failed to load.
function Row({ label, value, mono }: { label: string; value: string | undefined; mono?: boolean }) {
  if (!value) return null;
  return (
    <>
      <dt>{label}</dt>
      <dd className={mono ? "mono" : undefined}>{value}</dd>
    </>
  );
}

// OperatorVoice is the missing upvote, and it is honest about being one.
//
// It posts an attributed operator reception and it decides nothing — the
// authority to rule stays with the disposition events beside it. The control
// is optimistic because it is cheap and reversible: the stance a reader
// clicked is selected immediately, and a refused post puts the previous one
// back and shows the server's own sentence rather than leaving a lie on
// screen. There is no confirmation, because there is nothing to confirm.
function OperatorVoice({
  id,
  stance: recorded,
  reason: recordedReason,
  onRecorded,
}: {
  id: string;
  stance: OperatorStance | undefined;
  reason: string | undefined;
  onRecorded: (message: string) => void;
}) {
  const [stance, setStance] = useState<OperatorStance | undefined>(recorded);
  const [reason, setReason] = useState(recordedReason ? unescapeWhitespace(recordedReason) : "");
  const [pending, setPending] = useState<OperatorStance | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  async function choose(next: OperatorStance) {
    const previous = stance;
    setStance(next);
    setPending(next);
    setFailure(null);
    try {
      await putReception(id, next, reason);
      onRecorded(`Your stance is recorded: ${next}. It decides nothing.`);
    } catch (error) {
      setStance(previous);
      setFailure(errorMessage(error));
    } finally {
      setPending(null);
    }
  }

  return (
    <section className="panel">
      <h3>Your take</h3>
      <div className="peel-voice" role="group" aria-label="Your stance on this record">
        {STANCES.map((option) => (
          <button
            type="button"
            className="peel-stance"
            key={option.value}
            aria-pressed={stance === option.value}
            disabled={pending !== null}
            onClick={() => choose(option.value)}
          >
            {pending === option.value && <span className="spinner small" />}
            {option.label}
          </button>
        ))}
        {/* The whole explanation. A reception is attributed, reversible, and
            without authority; four words say that and a paragraph would only
            make it sound like more than it is. */}
        <p className="muted">Your take. Decides nothing.</p>
        <label className="decide-field">
          Why <span className="muted">(optional, kept verbatim)</span>
          <textarea value={reason} onChange={(event) => setReason(event.target.value)} rows={2} />
        </label>
      </div>
      {failure && <p className="inline-error" role="alert">{failure}</p>}
    </section>
  );
}

// RuleForm is the §4.7 authority, carried here from the review page with its
// behaviour intact: the same five dispositions, the same confirmation, the same
// append-only semantics, the same attributed-context field. It moved because
// the record moved; nothing about what it does changed.
function RuleForm({
  type,
  id,
  onDecided,
}: {
  type: ReviewSubjectType;
  id: string;
  onDecided: (message: string) => void;
}) {
  const [disposition, setDisposition] = useState<Disposition>("accept");
  const [note, setNote] = useState("");
  const [contextText, setContextText] = useState("");
  const [duplicateOf, setDuplicateOf] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    // The prompt says what this decision does, and a reopen does something
    // the other four do not: it returns the record to undecided. An operator
    // confirming "reopen" must be told that and not the generic sentence.
    const prompt = disposition === "reopen"
      ? `Reopen this ${type}?\n\nThe decision you are reopening stays in the history — nothing ` +
        "is edited or removed — and the record's status returns to new, so it can be decided " +
        "again on its merits."
      : `Record "${disposition}" for this ${type}?\n\nReview decisions are append-only: the ` +
        "event is recorded permanently, and reconsidering later appends another event rather " +
        "than replacing this one.";
    if (!window.confirm(prompt)) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      let contextId: string | undefined;
      if (contextText.trim()) {
        contextId = (await addReviewContext(contextText.trim())).id;
      }
      const result = await decideReview({
        subject: { type, id },
        disposition,
        contextId,
        duplicateOfId: disposition === "duplicate" ? duplicateOf.trim() || undefined : undefined,
        note: note.trim() || undefined,
      });
      onDecided(`Recorded ${disposition}. The record's status is now ${result.status}.`);
      setNote("");
      setContextText("");
      setDuplicateOf("");
    } catch (reason) {
      setSubmitError(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      <p className="muted">
        A disposition is an appended, attributed event — not a toggle. It cannot be edited or
        undone, only followed by another event.
      </p>
      {/* The form's grid lives on the form itself: the rule it used to carry
          hung off a `*-card` container class that no longer exists. */}
      <form className="decide-form" onSubmit={submit}>
        <fieldset className="disposition-set">
          <legend className="sr-only">Disposition</legend>
          {DISPOSITIONS.map((option) => (
            <label
              className={disposition === option.value ? "disposition-option active" : "disposition-option"}
              key={option.value}
            >
              <input
                type="radio"
                name="disposition"
                value={option.value}
                checked={disposition === option.value}
                onChange={() => setDisposition(option.value)}
              />
              <span>
                <strong>{option.label}</strong>
                <span className="muted">{option.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>

        {disposition === "duplicate" && (
          <label className="decide-field">
            Original record ID
            <input
              value={duplicateOf}
              onChange={(event) => setDuplicateOf(event.target.value)}
              placeholder="The record this duplicates"
              required
            />
          </label>
        )}

        {/* The note is the reviewer's own words, and optional on the four
            closing decisions. A reopen requires it: the service refuses a
            reopen with no reason, and asking here says why rather than
            letting the server say no. */}
        <label className="decide-field">
          Note{" "}
          <span className="muted">
            {disposition === "reopen"
              ? "(required: why the earlier decision stopped holding)"
              : "(optional, recorded with the event)"}
          </span>
          <textarea
            value={note}
            onChange={(event) => setNote(event.target.value)}
            rows={2}
            required={disposition === "reopen"}
          />
        </label>

        <label className="decide-field">
          Attributed context <span className="muted">(optional)</span>
          <textarea
            value={contextText}
            onChange={(event) => setContextText(event.target.value)}
            rows={2}
            placeholder="Guidance later refinement runs will see. Guidance is never evidence."
          />
        </label>

        <button type="submit" className="primary-button" disabled={submitting}>
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : `Record ${disposition}`}
        </button>
        {submitError && <p className="inline-error" role="alert">{submitError}</p>}
      </form>
    </>
  );
}

// ClaimPeel is depth 1: the sentence, the standing, and the one act the record
// wants. The operator's two voices live here rather than at the foot of the
// page, because the act follows the reading and a reader who has to scroll
// past four depths to rule is being asked to rule on his memory of the claim.
//
// The heavy control stays folded until it is asked for. The record wants one
// act and names it; the five dispositions with their consequences appear when
// the operator says he is ruling, which keeps depth 1 to the claim without
// putting the authority on another page.
function ClaimPeel({
  record,
  onActed,
}: {
  record: RecordPeel;
  onActed: (message: string) => void;
}) {
  const subject = reviewSubject(record.kind);
  const standing = record.standing;
  const action = record.action;

  return (
    <Peel title="The claim" open>
      {/* The claim, unless the heading is already it. A hypothesis and an
          observation are bare claims — the server sends the same sentence as
          both title and claim rather than manufacturing a second line — and
          printing it twice, one line apart, would read as two claims. */}
      {record.claim && record.claim !== record.title && (
        <p className="quote untrusted-inline">{unescapeWhitespace(record.claim)}</p>
      )}
      {standing && (
        <p>{STANDING_SENTENCES[standing.label] ?? `Its standing is ${standing.label}.`}</p>
      )}

      {/* §1's frame, beside the claim rather than on an about page: what a
          reader has in front of him is a creative, fallible, incomplete
          interpretation recorded for his review, and the sentence that says so
          belongs where the claim is. It is the same note every analytical
          surface in the app carries, not a variant written for this one. */}
      <FallibilityNote />

      <OperatorVoice
        id={record.id}
        stance={record.reception?.operator?.stance}
        reason={record.reception?.operator?.reason}
        onRecorded={onActed}
      />

      {/* The act the record wants, named by the record itself and folded
          until it is asked for. Depth 1 is the claim; five dispositions with
          their consequences spread under a one-sentence claim would bury it.
          Opening the control is a disclosure like every other on this page,
          so ruling still costs no page change. */}
      {action?.verb === "rule" && subject && (
        <Peel title={action.label}>
          <RuleForm type={subject} id={record.id} onDecided={onActed} />
        </Peel>
      )}

      {/* A question is answered where answers are written, which is the one
          act on this page that is not about this object: the operator is
          adding a fact to Reality, not reading this record more deeply. */}
      {action?.verb === "answer" && (
        <p>
          <Link to={`/ask/questions/${encodeURIComponent(record.id)}`}>{action.label}</Link>
        </p>
      )}
    </Peel>
  );
}

// CasePeel is depth 2: the argument, in prose, with no identifier in it. The
// labels are questions rather than field names — `verification_criteria` is
// "how you would know it worked" — because the reader deciding needs the
// question and the schema name answers a different one.
function CasePeel({ detail }: { detail: RecordCase }) {
  return (
    <Peel title="The case" open>
      <Prose label="The problem" text={detail.problem} />
      <Prose label="What it proposes" text={detail.outcome} />
      {/* One field, two shapes: a proposal and an observation record a
          three-valued grading here and a finding records its significance in
          prose, so the label has to read correctly over "moderate" and over a
          sentence. "How much it matters" does; "Why it matters" does not. */}
      <Prose label="How much it matters" text={detail.impact} />
      <Prose label="Where it applies" text={detail.scope} />
      <Prose label="What kind of thing this is" text={detail.classification} />
      <Prose label="What is uncertain about it" text={detail.uncertainty} />
      <Points label="How you would know it worked" items={detail.verification} />
      <Points label="What could go wrong" items={detail.risks} />
      <Points label="What is still unanswered" items={detail.open_questions} />
      <Points label="What it needs first" items={detail.prerequisites} />
      {detail.targets && detail.targets.length > 0 && (
        <section className="panel">
          <h3>What it would change</h3>
          <ul className="peel-list">
            {detail.targets.map((target, index) => (
              <li className="quote untrusted-inline" key={`${index}-${target.system}`}>
                <strong>{unescapeWhitespace(target.system)}</strong>
                {target.rationale && <> — {unescapeWhitespace(target.rationale)}</>}
                {target.confidence && (
                  <span className="secondary"> Confidence: {target.confidence}.</span>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
    </Peel>
  );
}

// hasCase answers whether the case holds anything at all. A record whose case
// is empty gets no depth 2, rather than a heading over nothing.
function hasCase(detail: RecordCase | undefined): detail is RecordCase {
  if (!detail) return false;
  return Boolean(
    detail.problem ||
      detail.outcome ||
      detail.impact ||
      detail.scope ||
      detail.classification ||
      detail.uncertainty ||
      detail.verification?.length ||
      detail.risks?.length ||
      detail.open_questions?.length ||
      detail.prerequisites?.length ||
      detail.targets?.length,
  );
}

// EvidencePeel is depth 3: what the record rests on, quoted, each excerpt one
// click from the transcript line it came from.
//
// The link is a plain anchor with the href the server computed. The route a
// citation opens belongs to the router, and a client that reassembled the
// fragment from a session id and an event index would have to be edited every
// time that route changed — and would be the only place on the page that
// needed the session id, which lives at depth 5.
function EvidencePeel({ items }: { items: RecordEvidence[] }) {
  return (
    <Peel title="The evidence" count={items.length}>
      <ul className="peel-list">
        {items.map((item, index) => {
          const conflicting = item.kind === "conflicting" || item.kind === "counter-evidence";
          return (
            <li
              className={conflicting ? "peel-counter" : undefined}
              key={`${index}-${item.href ?? item.line ?? ""}`}
            >
              {item.kind && <p className="peel-cite">{EVIDENCE_SIDES[item.kind]}</p>}
              <p className="quote untrusted-inline">{unescapeWhitespace(item.quote)}</p>
              <p className="peel-cite">
                {item.href ? (
                  <a href={item.href}>{citationLabel(item)}</a>
                ) : (
                  // An excerpt Babel cannot locate is still evidence; it is
                  // evidence a reader cannot check, and saying so is more use
                  // than a link that goes nowhere.
                  <span className="muted">Not locatable in this deployment.</span>
                )}
              </p>
            </li>
          );
        })}
      </ul>
    </Peel>
  );
}

// citationLabel names where an excerpt came from without naming a session id.
// The line, or the event, is the part a reader checking evidence uses; the
// identifier is the part the machinery uses.
function citationLabel(item: RecordEvidence): string {
  if (item.line) return `Read it in the transcript, at line ${item.line}`;
  if (item.event !== undefined) return `Read it in the transcript, at event ${item.event}`;
  return "Read it in the transcript";
}

// ReceptionPeel is depth 4: who received the claim and what they said.
//
// Three blocks, never one. The operator's own stance is his; the reviewers'
// assessments are runs'; the rulings are the authority. §4.12 keeps them apart
// by attribution, so they are separate panels with their own words — a person
// agreeing and a run voting support are not the same act, and no number on
// this page adds one to the other.
function ReceptionPeel({ reception }: { reception: RecordReception }) {
  const operator = reception.operator;
  const earlier = reception.history ?? [];
  const reviewers = reception.model ?? [];
  const decisions = reception.decisions ?? [];
  const counts = reception.counts;
  // The count is how many things are in here, not a tally of anything: the
  // operator's own stance, the reviewers' assessments and the rulings are
  // three kinds of act and nothing on this page adds them together. His
  // earlier stances are not counted — they are the same voice, superseded.
  const entries = (operator ? 1 : 0) + reviewers.length + decisions.length;
  const operatorAt = formatTime(operator?.at);

  return (
    <Peel
      title="The reception"
      count={entries}
      note={reception.contested ? "reviewers disagree" : undefined}
    >
      {(operator || earlier.length > 0) && (
        <section className="panel">
          <h3>You</h3>
          {operator ? (
            <p>
              You said <strong>{operator.stance}</strong>
              {operatorAt && <span className="secondary">, {operatorAt.relative}</span>}. A
              reception decides nothing; your rulings are below.
            </p>
          ) : (
            <p>You hold no stance on this now.</p>
          )}
          {operator?.reason && (
            <p className="quote untrusted-inline">{unescapeWhitespace(operator.reason)}</p>
          )}
          {/* What he used to say, kept rather than replaced. A reception is
              appended like everything else here, so changing his mind leaves
              the earlier position readable instead of rewriting it. */}
          {earlier.length > 0 && (
            <>
              <p className="muted">Earlier you said:</p>
              <ul className="peel-list">
                {earlier.map((entry) => {
                  const at = formatTime(entry.at);
                  return (
                    <li key={entry.at}>
                      <span>
                        <strong>{entry.stance}</strong>
                        {at && <span className="secondary">, {at.relative}</span>}
                      </span>
                      {entry.reason && (
                        <p className="quote untrusted-inline">{unescapeWhitespace(entry.reason)}</p>
                      )}
                    </li>
                  );
                })}
              </ul>
            </>
          )}
        </section>
      )}

      {reviewers.length > 0 && (
        <section className="panel">
          <h3>Babel's reviewers</h3>
          {counts && (
            <p className="muted">
              {counts.support} support, {counts.oppose} oppose, {counts.unsure} unsure — across
              Babel's own runs, and never counting your stance.
            </p>
          )}
          {reception.contested && (
            <p>Contested: the reviewers do not agree with each other.</p>
          )}
          <ol className="peel-list">
            {reviewers.map((reviewer, index) => (
              <li key={`${index}-${reviewer.actor}`}>
                <ReviewerLine reviewer={reviewer} index={index} />
              </li>
            ))}
          </ol>
        </section>
      )}

      {decisions.length > 0 && (
        <section className="panel">
          <h3>Rulings</h3>
          <p className="muted">
            In the order they were recorded. None is edited or removed; a later ruling is
            appended beside the earlier one.
          </p>
          <ol className="peel-list">
            {decisions.map((decision, index) => {
              const at = formatTime(decision.at);
              return (
                <li key={`${index}-${decision.at}`}>
                  <span>
                    <strong>{decision.disposition}</strong>
                    {decision.by && <> by {decision.by}</>}
                    {at && <span className="secondary"> · {at.relative}</span>}
                  </span>
                  {decision.note && (
                    <p className="quote untrusted-inline">{unescapeWhitespace(decision.note)}</p>
                  )}
                </li>
              );
            })}
          </ol>
        </section>
      )}
    </Peel>
  );
}

// ReviewerLine says what a run was asked and how it answered, and does not
// name it. The actor is a run id — an opaque identifier with no honest display
// name, because inventing a friendly label for a run would be Babel naming its
// own reviewers — so it is numbered here and identified at depth 5, where the
// number and the id sit together.
function ReviewerLine({ reviewer, index }: { reviewer: ModelReception; index: number }) {
  const at = formatTime(reviewer.at);
  const role = ROLE_WORDS[reviewer.role] ?? `on ${reviewer.role}`;
  const stance = MODEL_STANCE_WORDS[reviewer.stance] ?? reviewer.stance;
  return (
    <>
      <span>
        Reviewer {index + 1}, {role}: {stance}
        {at && <span className="secondary"> · {at.relative}</span>}
      </span>
      {reviewer.rationale && (
        <p className="quote untrusted-inline">{unescapeWhitespace(reviewer.rationale)}</p>
      )}
    </>
  );
}

// MachineryPeel is depth 5: everything that identifies the object, and nothing
// a reader needs to understand it. It is one section rather than five so that
// a person debugging has one place to open, and it is collapsed so that a
// person reading never opens it.
function MachineryPeel({ record }: { record: RecordPeel }) {
  const machinery: RecordMachinery = record.machinery ?? {};
  const created = formatTime(machinery.created_at);
  const links = machinery.links ?? [];
  const receipts = machinery.receipts ?? [];
  const revisions = machinery.revisions ?? [];
  const reviewers = record.reception?.model ?? [];
  const located = (record.evidence ?? []).filter((item) => item.session_id || item.path);

  return (
    <Peel title="The machinery">
      <dl className="peel-rows">
        <Row label="Record" value={record.id} mono />
        <Row label="Kind" value={record.kind} />
        <Row label="Standing" value={record.standing?.label} />
        <Row label="Revision" value={machinery.revision} mono />
        <Row label="Digest" value={machinery.digest} mono />
        <Row label="Schema" value={machinery.schema?.toString()} />
        <Row label="Created" value={created ? created.absolute : undefined} />
        <Row label="Run" value={machinery.run_id} mono />
        <Row label="Policy" value={machinery.policy_version} mono />
        <Row label="Published by" value={machinery.host} mono />
      </dl>

      {revisions.length > 0 && (
        <section className="panel">
          <h3>Revisions</h3>
          <ul className="peel-list">
            {revisions.map((revision) => {
              const at = formatTime(revision.at);
              return (
                <li key={revision.id}>
                  <span className="mono">{revision.id}</span>
                  {at && <span className="secondary"> · {at.absolute}</span>}
                </li>
              );
            })}
          </ul>
        </section>
      )}

      {links.length > 0 && (
        <section className="panel">
          <h3>Links</h3>
          <ul className="peel-list">
            {links.map((link) => (
              <li key={`${link.direction}-${link.kind}-${link.id}`}>
                {/* The other record, reachable. An edge is a record too, so
                    following one is the same /r/ route rather than a lookup
                    the reader has to perform himself. The relation and its
                    direction are named separately from the id: "supersedes,
                    inbound" is the fact, and the id is how to find it. */}
                {link.title ? (
                  <>
                    <Link to={`/r/${encodeURIComponent(link.id)}`}>
                      {unescapeWhitespace(link.title)}
                    </Link>
                    <span className="mono secondary"> {link.id}</span>
                  </>
                ) : (
                  <Link className="mono" to={`/r/${encodeURIComponent(link.id)}`}>
                    {link.id}
                  </Link>
                )}
                <span className="secondary">
                  {" "}
                  {link.kind}, {link.direction === "from" ? "inbound" : "outbound"}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {receipts.length > 0 && (
        <section className="panel">
          <h3>Receipts</h3>
          <ul className="peel-list">
            {receipts.map((receipt) => {
              const at = formatTime(receipt.at);
              return (
                <li key={receipt.id}>
                  <span className="mono">{receipt.id}</span>
                  {receipt.stage && <span className="secondary"> · {receipt.stage}</span>}
                  {receipt.cost && <span className="secondary"> · {receipt.cost}</span>}
                  {at && <span className="secondary"> · {at.absolute}</span>}
                </li>
              );
            })}
          </ul>
        </section>
      )}

      {reviewers.length > 0 && (
        <section className="panel">
          <h3>Reviewers</h3>
          {/* The numbering at depth 4 and the run ids here are the same list
              in the same order, which is what makes a numbered reviewer
              traceable to the run that wrote the assessment. */}
          <dl className="peel-rows">
            {reviewers.map((reviewer, index) => (
              <Row key={`${index}-${reviewer.actor}`} label={`Reviewer ${index + 1}`} value={reviewer.actor} mono />
            ))}
          </dl>
        </section>
      )}

      {located.length > 0 && (
        <section className="panel">
          <h3>Cited sessions</h3>
          <dl className="peel-rows">
            {located.map((item, index) => (
              <Row
                key={`${index}-${item.session_id ?? item.path ?? ""}`}
                label={`Excerpt ${index + 1}`}
                value={[item.session_id, item.path, item.line ? `line ${item.line}` : undefined]
                  .filter(Boolean)
                  .join(" · ")}
                mono
              />
            ))}
          </dl>
        </section>
      )}
    </Peel>
  );
}

// RecordPeels is the record itself, five depths deep. The order is the reading
// order — claim, case, evidence, reception, machinery — and the two shallow
// depths are open because they are what the reader came for.
export function RecordPeels({
  record,
  onActed,
}: {
  record: RecordPeel;
  onActed: (message: string) => void;
}) {
  const reception = record.reception;
  const evidence = record.evidence ?? [];
  const received = Boolean(
    reception &&
      (reception.operator ||
        reception.history?.length ||
        reception.model?.length ||
        reception.decisions?.length),
  );

  return (
    <div className="surface">
      <ClaimPeel record={record} onActed={onActed} />
      {hasCase(record.case) && <CasePeel detail={record.case} />}
      {evidence.length > 0 && <EvidencePeel items={evidence} />}
      {received && reception && <ReceptionPeel reception={reception} />}
      <MachineryPeel record={record} />
    </div>
  );
}

// RecordHeading is the record's identity: what kind of thing it is, where it
// stands, and the sentence it is. The two badges are the only ones on the page
// — standing and kind — and nothing else wears one.
//
// The heading is the record's own words, so it carries the quoted frame even
// as an h1. It is the title when the record wrote one; a hypothesis and an
// observation write none — the statement is the record, not a name for it —
// so their claim is the heading instead. The kind is not a fallback heading:
// the badge beside it already says "Observation", and an h1 repeating that
// would name the class of thing twice and the thing itself never.
export function RecordHeading({ record }: { record: RecordPeel }) {
  const standing = record.standing;
  const headline = record.title ?? record.claim;
  return (
    <header className="surface">
      <div className="heading-badges">
        <Badge label={KIND_WORDS[record.kind] ?? record.kind} tone="neutral" />
        {standing && <Badge label={standing.label} tone={standingTone(standing.tone)} />}
      </div>
      {headline ? (
        <h1 className="quote untrusted-inline">{unescapeWhitespace(headline)}</h1>
      ) : (
        <h1>{KIND_WORDS[record.kind] ?? "Record"}</h1>
      )}
    </header>
  );
}
