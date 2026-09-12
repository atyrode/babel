import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { Link } from "react-router-dom";
import {
  addReviewContext,
  decideReview,
  type Disposition,
  type ReviewSubjectType,
} from "./api";
import { Badge, unescapeWhitespace, type Tone } from "./analysis";
import { errorMessage, formatDuration, formatTime } from "./format";
import {
  getComments,
  postComment,
  putReception,
  type Act,
  type Comment,
  type CommentThread,
  type EvidenceKind,
  type ModelReception,
  type ModelRole,
  type OperatorReception,
  type OperatorStance,
  type RecordCase,
  type RecordCost,
  type RecordEvidence,
  type RecordKind,
  type RecordMachinery,
  type RecordOrigin,
  type RecordPeel,
  type RecordReception,
  type RecordRelated,
  type RelatedRecord,
  type RoleReception,
  type Speaker,
  type StandingTone,
} from "./recordapi";
import { VoteArrows, type VoteStance } from "./vote";
import "./record.css";

// One record, peeled.
//
// "The complexity of the data is for Babel itself, the user really only needs
// the surface, and to be able to dig when needed." Everything in this file
// follows from that sentence. A record has five depths and the reader chooses
// one; he does not choose a page. Depth 1 is the claim, its standing and the
// acts it invites. Depth 2 is the case, in prose, and where it came from.
// Depth 3 is the evidence, in the words of whoever said it. Depth 4 is the
// reception. Depth 5 is the machinery — every id, digest, receipt and dollar
// the object carries.
//
// Decision 90 splits the register at depth 3: editorial above — serif claim,
// one measure of prose, evidence as pull-quotes — and an observatory below,
// where reception and machinery are tabular mono figures. The peel decides the
// register, so the CSS does too: record.css is this file's, styles.css is the
// shell's, and neither reaches into the other.
//
// The four rules that shape the code rather than the layout:
//
//   - Nothing here fetches on open. The whole record arrives in one response,
//     so opening depth 4 is a disclosure and never a request; a reader who
//     digs waits for nothing and a slow section cannot exist.
//   - An absent section is absent. Not an empty heading, not a zero: a
//     proposal that names no risk is not a proposal whose risks are none, and
//     a heading reading "What could go wrong" over nothing claims that nothing
//     could. Every renderer below returns null rather than a frame.
//   - Ids live at depth 5 only, with one deliberate exception: the connections
//     strip links to other records, and a link needs an identity. It shows the
//     other record's own words and keeps its id out of the sentence.
//   - The excerpt outranks the note. What a person said is the record's
//     evidence; what a model said about it is a gloss, set smaller, beneath.
//
// Class inventory, so the markup and the stylesheets can be checked against
// each other. The containers and utilities are the shell's (styles.css):
//
//   surface        a plain container
//   panel          a labelled group — the blocks inside a depth
//   quote          untrusted model text, always via unescapeWhitespace
//   peel           the <details> of one depth, with peel-body/peel-count
//   peel-list      a ul/ol of prose items
//   peel-cite      a source line
//   peel-rows      a dl of dt/dd machinery pairs
//   rule-bar       a segmented button group (Contract T primitive)
//   stat           a figure with a label (Contract T primitive)
//   kbd            a key hint (Contract T primitive)
//   long           on the h1: this headline is a statement, not a name, so
//                  the shell sets it at a reading size instead of display
//
// plus the surviving utilities — muted, secondary, mono, sr-only, spinner,
// primary-button, inline-error, untrusted-inline, badge tone-* through
// analysis.tsx's Badge. Everything prefixed `record-` is in record.css, which
// also holds the `vote` group — the arrows are src/vote.tsx's and the feed
// mounts the same component, so their rules live beside the record's rather
// than in a third stylesheet.

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

// The operator's three receptions, as the rule bar's first group. The stance is
// attributed, reversible and without authority, which is why it needs no
// confirmation and sits apart from the five that do.
const STANCES: Array<{ value: OperatorStance; label: string; key: string }> = [
  { value: "agree", label: "Agree", key: "a" },
  { value: "disagree", label: "Disagree", key: "d" },
  { value: "unsure", label: "Unsure", key: "u" },
];

// The §4.12 assessment roles, as what the reviewer was asked. A role is the
// question a run answered, and naming it is what keeps four assessments from
// reading as four votes on the same thing.
const ROLE_WORDS: Record<ModelRole, string> = {
  reception: "whether it holds up",
  evidence: "whether the evidence supports it",
  challenge: "the case against it",
  comparison: "how it compares with others",
  outcome: "what came of it",
  relevance: "whether it matters",
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

// Who said the words in a pull-quote. The operator is "you" nowhere here: a
// cited session may be anybody's, so the speaker is named by what they are in
// the conversation rather than by an identity Babel would be inventing.
const SPEAKER_WORDS: Record<Speaker, string> = {
  user: "The person",
  assistant: "The model",
  tool: "A tool's output",
};

// What a reviewer's vote says. Kept separate from the operator's words on
// purpose: a run voting support and a person agreeing are not the same act,
// and §4.12 separates them by attribution. Nothing sums the two.
const MODEL_STANCE_WORDS: Record<string, string> = {
  support: "supports it",
  oppose: "opposes it",
  unsure: "is unsure",
};

// The five §4.7 dispositions and the one sentence each needs at the moment of
// confirming it. The vocabulary is unchanged — it is the review service's —
// and so is the requirement to confirm; what changed is the size of the act.
// Five radios carrying two sentences each, two textareas and a full-width
// button occupied 677 pixels and 95 words before a reader had decided
// anything; this is five buttons and, on the one he presses, a sentence.
//
// `reject-and-refine` is deliberately absent: it authorizes a refinement
// request and belongs to the CLI until this surface grows the full guidance
// flow. `reopen` is the one that opens rather than closes, and its sentence
// says so plainly.
const DISPOSITIONS: Array<{ value: Disposition; label: string; confirm: string }> = [
  {
    value: "accept",
    label: "Accept",
    confirm: "Endorse this record for projection and follow-on work. The event is appended permanently.",
  },
  {
    value: "reject",
    label: "Reject",
    confirm: "Record disagreement. The record is kept, visibly rejected, and the event is appended permanently.",
  },
  {
    value: "defer",
    label: "Defer",
    confirm: "Not now. The record stays in the queue's history and the event is appended permanently.",
  },
  {
    value: "duplicate",
    label: "Duplicate",
    confirm: "Point this record at an original, which you name below. The event is appended permanently.",
  },
  {
    value: "reopen",
    label: "Reopen",
    confirm:
      "Undecide it. The earlier decision stays in the history, the record's status returns to new, and your reason is required.",
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
// The open state is the page's rather than the element's, because Contract K
// gives the reader `1`-`5` to toggle a depth from anywhere on the page: a
// <details> that owned its own state could be opened by a key and then
// disagree with the key the next time it was pressed.
function Peel({
  title,
  count,
  note,
  open,
  onToggle,
  children,
}: {
  title: string;
  count?: number;
  note?: string;
  open: boolean;
  onToggle: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <details
      className={open ? "peel peel-open" : "peel"}
      open={open}
      onToggle={(event) => onToggle(event.currentTarget.open)}
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

// Figure is one labelled number in the observatory register: the design
// system's `.stat`, with the value in tabular mono so two of them line up.
function Figure({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="stat">
      <span className="stat-label">{label}</span>
      <strong className="stat-value">{value}</strong>
      {note && <span className="stat-note">{note}</span>}
    </div>
  );
}

// money renders a dollar figure. It takes a number rather than a nullable one
// on purpose: an absent cost is not a dollar figure with a word where the
// digits go, and each caller here says the absence in its own register — the
// cost block in a sentence, the origin strip by leaving the clause out. A
// "$0.00" would be neither, and this is the one page where a fabricated zero
// would be a claim about money.
function money(value: number): string {
  return `$${value.toFixed(2)}`;
}

// RuleBar is the whole act of deciding, in one bar.
//
// Two groups, never one: the operator's reception on the left and the §4.7
// authority on the right, separated by a rule because agreeing is not
// accepting. A stance posts immediately and optimistically — it is attributed,
// reversible and decides nothing, so there is nothing to confirm — and a
// ruling opens a one-sentence confirmation where the button was, because a
// disposition is an appended, attributed event that cannot be edited or
// undone.
//
// It is exported because the same act belongs on a queue row: Contract K gives
// Decide and Read `a`/`d`/`u` and `r` on the focused row, and a second
// implementation of this bar would be a second confirmation flow over one
// authority. Nothing in it renders a heading or a container, so it drops into
// a row as well as into a page.
export function RuleBar({
  id,
  kind,
  stance: recorded,
  onActed,
  onStance,
  barRef,
  withStance = true,
}: {
  id: string;
  kind: RecordKind;
  stance?: OperatorStance;
  onActed: (message: string) => void;
  // onStance hands back what the store recorded — its stance and its own
  // timestamp, never the request's — so a page can show the act while the
  // projection it reads receptions out of is still catching up with it. A
  // queue row has nowhere to show it and passes nothing.
  onStance?: (recorded: OperatorReception) => void;
  // barRef lets the page that owns the keyboard reach the real controls
  // rather than reimplementing what they do. The record page's `a`/`d`/`u`
  // press this bar's own buttons, so a stance recorded by key and a stance
  // recorded by click are the same code path — including the optimistic
  // selection and the refusal handling.
  barRef?: RefObject<HTMLDivElement | null>;
  // withStance drops the left-hand group for a surface that takes the
  // operator's stance somewhere else. §8.7 makes the arrows the stance and
  // puts them at the top of the post, so the record page offers them once and
  // this bar carries the ruling alone; a queue row has no arrows and keeps
  // both groups.
  withStance?: boolean;
}) {
  const subject = reviewSubject(kind);
  const [stance, setStance] = useState<OperatorStance | undefined>(recorded);
  const [pending, setPending] = useState<OperatorStance | null>(null);
  const [stanceError, setStanceError] = useState<string | null>(null);
  const [ruling, setRuling] = useState<Disposition | null>(null);

  useEffect(() => setStance(recorded), [recorded]);

  async function choose(next: OperatorStance) {
    const previous = stance;
    setStance(next);
    setPending(next);
    setStanceError(null);
    try {
      const stored = await putReception(id, next);
      onStance?.({ stance: stored.stance, at: stored.at });
      onActed(`Your stance is recorded: ${next}. It decides nothing.`);
    } catch (error) {
      setStance(previous);
      setStanceError(errorMessage(error));
    } finally {
      setPending(null);
    }
  }

  // An observation invites no ruling, so a bar with the stance taken out of it
  // has nothing left to offer and renders nothing at all rather than an empty
  // row of gaps.
  if (!withStance && !subject) return null;

  return (
    <>
      <div className="record-acts" ref={barRef}>
        {withStance && (
          <>
            <div className="rule-bar" role="group" aria-label="Your stance on this record">
              {STANCES.map((option) => (
                <button
                  type="button"
                  key={option.value}
                  data-stance={option.value}
                  className={stance === option.value ? "active" : undefined}
                  aria-pressed={stance === option.value}
                  disabled={pending !== null}
                  onClick={() => choose(option.value)}
                >
                  {pending === option.value && <span className="spinner small" />}
                  {option.label}
                </button>
              ))}
            </div>
            {/* The whole explanation of the left-hand group. A reception is
                attributed, reversible and without authority; three words say
                that and a paragraph would make it sound like more than it
                is. */}
            <span className="record-acts-label">your take · decides nothing</span>
          </>
        )}
        {subject && (
          <>
            {withStance && <span className="record-acts-split" aria-hidden="true" />}
            <div className="rule-bar" role="group" aria-label={`Rule on this ${subject}`}>
              {DISPOSITIONS.map((option) => (
                <button
                  type="button"
                  key={option.value}
                  data-ruling={option.value}
                  className={ruling === option.value ? "active" : undefined}
                  aria-expanded={ruling === option.value}
                  onClick={() => setRuling(ruling === option.value ? null : option.value)}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <span className="record-acts-label">the ruling · permanent</span>
          </>
        )}
      </div>
      {stanceError && (
        <p className="inline-error" role="alert">
          {stanceError}
        </p>
      )}
      {ruling && subject && (
        <RuleConfirm
          disposition={ruling}
          subject={subject}
          id={id}
          onCancel={() => setRuling(null)}
          onDecided={(message) => {
            setRuling(null);
            onActed(message);
          }}
        />
      )}
    </>
  );
}

// RuleConfirm is the confirmation, and it is the whole of it: one sentence
// saying what the ruling does, the note the reviewer may leave, and two
// buttons.
//
// The requirement it keeps is unchanged. A disposition is still confirmed
// before it is recorded, still appended rather than edited, still attributed
// to the launch session's operator; a reopen still requires a reason, because
// the service refuses one without and asking here says why instead of letting
// the server say no. What is gone is the ballot: the reader has already
// decided, and the form's job is to take the decision rather than to present
// the options again.
//
// The guidance field stays, folded. It is the one input that is not about this
// decision — attributed context is what a later refinement run will see — so
// it is available and out of the way, rather than removed or in the path.
function RuleConfirm({
  disposition,
  subject,
  id,
  onCancel,
  onDecided,
}: {
  disposition: Disposition;
  subject: ReviewSubjectType;
  id: string;
  onCancel: () => void;
  onDecided: (message: string) => void;
}) {
  const option = DISPOSITIONS.find((entry) => entry.value === disposition);
  const [note, setNote] = useState("");
  const [contextText, setContextText] = useState("");
  const [duplicateOf, setDuplicateOf] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const confirmRef = useRef<HTMLButtonElement | null>(null);

  // The confirmation takes the focus it asks for. A panel that appeared under
  // the pointer while the keyboard stayed where it was would be a dialogue a
  // keyboard reader could not answer.
  useEffect(() => confirmRef.current?.focus(), []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSubmitting(true);
    setFailure(null);
    try {
      let contextId: string | undefined;
      if (contextText.trim()) {
        contextId = (await addReviewContext(contextText.trim())).id;
      }
      const result = await decideReview({
        subject: { type: subject, id },
        disposition,
        contextId,
        duplicateOfId: disposition === "duplicate" ? duplicateOf.trim() || undefined : undefined,
        note: note.trim() || undefined,
      });
      onDecided(`Recorded ${disposition}. The record's status is now ${result.status}.`);
    } catch (reason) {
      setFailure(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form className="record-confirm" onSubmit={submit}>
      <p>{option?.confirm}</p>
      {disposition === "duplicate" && (
        <label>
          The original record's id
          <input
            value={duplicateOf}
            onChange={(event) => setDuplicateOf(event.target.value)}
            placeholder="The record this duplicates"
            required
          />
        </label>
      )}
      <label>
        {disposition === "reopen"
          ? "Why the earlier decision stopped holding (required)"
          : "Note (optional, recorded with the event)"}
        <textarea
          value={note}
          onChange={(event) => setNote(event.target.value)}
          rows={2}
          required={disposition === "reopen"}
        />
      </label>
      <details>
        <summary className="record-acts-label">Attach guidance for later runs</summary>
        <label>
          Attributed context
          <textarea
            value={contextText}
            onChange={(event) => setContextText(event.target.value)}
            rows={2}
            placeholder="Guidance later refinement runs will see. Guidance is never evidence."
          />
        </label>
      </details>
      <div className="record-confirm-acts">
        <button type="submit" className="primary-button" ref={confirmRef} disabled={submitting}>
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : `Confirm ${disposition}`}
        </button>
        <button type="button" onClick={onCancel} disabled={submitting}>
          Cancel
        </button>
      </div>
      {failure && (
        <p className="inline-error" role="alert">
          {failure}
        </p>
      )}
    </form>
  );
}

// ClaimPeel is depth 1: the claim, what its standing does to it, and the one
// act it invites. The ruling lives here rather than at the foot of the page,
// because the act follows the reading and a reader who has to scroll past four
// depths to rule is being asked to rule on his memory of the claim. The
// operator's other voice — his vote — is the arrows in the post header, where
// §8.7 puts it and where a reader arriving from the feed already found it.
function ClaimPeel({
  record,
  open,
  onToggle,
  onActed,
  barRef,
}: {
  record: RecordPeel;
  open: boolean;
  onToggle: (open: boolean) => void;
  onActed: (message: string) => void;
  barRef: RefObject<HTMLDivElement | null>;
}) {
  const standing = record.standing;
  const action = record.action;

  return (
    <Peel title="The claim" open={open} onToggle={onToggle}>
      {/* The claim, unless the heading is already it. A hypothesis and an
          observation are bare claims — the server sends the same sentence as
          both title and claim rather than manufacturing a second line — and
          printing it twice, one line apart, would read as two claims. */}
      {record.claim && record.claim !== record.title && (
        <p className="quote untrusted-inline record-claim">{unescapeWhitespace(record.claim)}</p>
      )}
      {standing && (
        <p className="record-standing">
          {STANDING_SENTENCES[standing.label] ?? `Its standing is ${standing.label}.`}
        </p>
      )}

      {/* The ruling alone. The operator's stance is the arrows in the post
          header — §8.7 makes them the vote — and a second set of stance
          buttons one depth down would be two controls recording one thing. */}
      <RuleBar
        id={record.id}
        kind={record.kind}
        onActed={onActed}
        barRef={barRef}
        withStance={false}
      />

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
//
// The origin strip closes it. Where an argument came from is part of reading
// the argument: the same case is worth more when it grew out of an hour of the
// operator's own work than when it grew out of a passing remark, and until now
// the page could not say which.
function CasePeel({
  detail,
  origin,
  open,
  onToggle,
}: {
  detail: RecordCase;
  origin: RecordOrigin | undefined;
  open: boolean;
  onToggle: (open: boolean) => void;
}) {
  return (
    <Peel title="The case" open={open} onToggle={onToggle}>
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
      <OriginStrip origin={origin} />
    </Peel>
  );
}

// OriginStrip is one line: born from this conversation, in this workspace, on
// this day, for this much. Every part of it is the session's own fact, and the
// title links to the transcript at the cited record.
function OriginStrip({ origin }: { origin: RecordOrigin | undefined }) {
  if (!origin) return null;
  const at = formatTime(origin.at);
  const title = origin.session_title ? unescapeWhitespace(origin.session_title) : "an untitled session";
  return (
    <p className="record-origin">
      <span>Born from</span>
      <cite>{origin.href ? <a href={origin.href}>{title}</a> : title}</cite>
      {origin.workspace && (
        <>
          <span>in</span>
          <span className="record-origin-where">{origin.workspace}</span>
        </>
      )}
      {at && <span>· {at.absolute}</span>}
      {origin.cost_usd !== null && (
        <span>
          · <span className="record-figure">{money(origin.cost_usd)}</span>
        </span>
      )}
      {origin.turns !== null && <span>· {origin.turns} turns</span>}
    </p>
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

// EvidencePeel is depth 3: what the record rests on, in the words of whoever
// said it, each excerpt one click from the transcript line it came from.
//
// The excerpt is the hero and the note is a gloss. The server recovers the
// cited bytes from the session log at the locator and checks them against the
// digest the citation carries, so what is quoted here is provably the material
// the record cited — and when it cannot be recovered the note and the link
// stand alone, exactly as they did before.
//
// The link is a plain anchor with the href the server computed. The route a
// citation opens belongs to the router, and a client that reassembled the
// fragment from a session id and an event index would have to be edited every
// time that route changed — and would be the only place on the page that
// needed the session id, which lives at depth 5.
function EvidencePeel({
  items,
  open,
  onToggle,
}: {
  items: RecordEvidence[];
  open: boolean;
  onToggle: (open: boolean) => void;
}) {
  const quoted = items.filter((item) => item.excerpt).length;
  return (
    <Peel
      title="The evidence"
      count={items.length}
      note={quoted > 0 ? `${quoted} quoted from the transcript` : undefined}
      open={open}
      onToggle={onToggle}
    >
      <ul className="record-evidence">
        {items.map((item, index) => {
          const conflicting = item.kind === "conflicting" || item.kind === "counter-evidence";
          return (
            <li
              className={conflicting ? "record-counter" : undefined}
              key={`${index}-${item.href ?? item.line ?? ""}`}
            >
              {item.kind && <p className="record-side">{EVIDENCE_SIDES[item.kind]}</p>}
              {item.excerpt ? (
                <>
                  <blockquote className="record-excerpt quote untrusted-inline">
                    {unescapeWhitespace(item.excerpt)}
                  </blockquote>
                  <p className="record-speaker">
                    {item.speaker && <strong>{SPEAKER_WORDS[item.speaker]}</strong>}
                    {item.session_title && (
                      <>
                        {item.speaker ? ", in " : "In "}
                        <span className="untrusted-inline">
                          {unescapeWhitespace(item.session_title)}
                        </span>
                      </>
                    )}
                  </p>
                </>
              ) : null}
              {item.quote && (
                <p className="record-note quote untrusted-inline">{unescapeWhitespace(item.quote)}</p>
              )}
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
  if (item.excerpt) {
    if (item.line) return `Read it in context, at line ${item.line}`;
    return "Read it in context";
  }
  if (item.line) return `Read it in the transcript, at line ${item.line}`;
  if (item.event !== undefined) return `Read it in the transcript, at event ${item.event}`;
  return "Read it in the transcript";
}

// RelatedStrip is every connection the record has that its own words do not
// state, between the evidence and the reception.
//
// It is a strip rather than a depth because a reader does not open it: he
// glances at it while deciding, and each of the five relations changes the
// decision differently. Three other proposals answer this problem and two were
// already rejected; Babel suspects this restates a candidate from March; a
// later revision replaced this wording; the rest of the run's output is one
// click away. None of that is in the record, and all of it is in the stores.
// RELATED_SHOWN bounds each group of the strip. A run that wrote twenty records
// would otherwise push the reception and the machinery a screen down, and the
// strip exists to be glanced at, not read; the run's own page lists all of them.
const RELATED_SHOWN = 6;

function RelatedStrip({ related }: { related: RecordRelated | undefined }) {
  if (!related) return null;
  const groups: Array<{ label: string; items: RelatedRecord[] | undefined; overlap?: boolean; standing?: boolean }> = [
    { label: "Other remedies for this problem", items: related.addressing, standing: true },
    { label: "Babel suspects this restates", items: related.duplicates, overlap: true },
    { label: "Replaces", items: related.supersedes },
    { label: "Replaced by", items: related.superseded_by },
    { label: "Made in the same run", items: related.siblings },
  ].filter((group) => group.items && group.items.length > 0);
  if (groups.length === 0) return null;

  return (
    <section className="record-related" aria-label="Connections">
      <h2>The connections</h2>
      {groups.map((group) => (
        <div className="record-related-group" key={group.label}>
          <h3>
            {group.label}
            <span className="muted"> · {group.items?.length}</span>
          </h3>
          <ul>
            {(group.items ?? []).slice(0, RELATED_SHOWN).map((item) => (
              <li key={item.id}>
                <Link to={`/r/${encodeURIComponent(item.id)}`}>
                  {item.title ? (
                    <span className="untrusted-inline">{unescapeWhitespace(item.title)}</span>
                  ) : (
                    <span className="mono">{item.id}</span>
                  )}
                </Link>
                {group.standing && item.standing && (
                  <Badge label={item.standing} tone={standingBadge(item.standing)} />
                )}
                {group.overlap && item.overlap !== undefined && (
                  <span className="record-figure">{item.overlap.toFixed(2)} overlap</span>
                )}
                {item.kind && <span className="muted">{KIND_WORDS[item.kind] ?? item.kind}</span>}
              </li>
            ))}
            {(group.items?.length ?? 0) > RELATED_SHOWN && (
              <li className="muted">
                and {(group.items?.length ?? 0) - RELATED_SHOWN} more; the run's page under Watch lists every one
              </li>
            )}
          </ul>
        </div>
      ))}
    </section>
  );
}

// standingBadge tints a competing remedy's standing. It is the same mapping
// the record's own badge uses, applied to a label rather than to the server's
// tone: the related strip carries standings the server did not tone, and a
// second table here is the price of not asking it to tone a list of five.
function standingBadge(label: string): Tone {
  switch (label) {
    case "accepted":
      return "green";
    case "rejected":
      return "red";
    case "deferred":
    case "refine-requested":
    case "reopened":
      return "amber";
    default:
      return "neutral";
  }
}

// ReceptionPeel is depth 4: who received the claim and what they said, in the
// observatory register.
//
// The operator's own stance is first and separate, and the reviewers are a
// table of figures rather than a list of sentences. That is the whole shape of
// §4.12's boundary made visible: a person agrees with something he chose to
// read, and a run votes on content it was served under a claim — and now the
// table says which question each run was answering, because four supports
// across four roles are four answers to four different questions.
function ReceptionPeel({
  reception,
  open,
  onToggle,
}: {
  reception: RecordReception;
  open: boolean;
  onToggle: (open: boolean) => void;
}) {
  const operator = reception.operator;
  const earlier = reception.history ?? [];
  const reviewers = reception.model ?? [];
  const decisions = reception.decisions ?? [];
  const byRole = reception.by_role ?? [];
  const counts = reception.counts;
  // The count is how many things are in here, not a tally of anything: the
  // operator's own stance, the reviewers' assessments and the rulings are
  // three kinds of act and nothing on this page adds them together. His
  // earlier stances are not counted — they are the same voice, superseded.
  const entries = (operator ? 1 : 0) + reviewers.length + decisions.length;
  const operatorAt = formatTime(operator?.at);
  // The summary carries the reader's own position, so a stance is visible
  // before this depth is opened. That absence was the whole complaint: he
  // agreed, the button stayed pressed, and nothing else on the record
  // acknowledged that he had said anything. The contested mark keeps its
  // place beside it — they are two different facts about the same depth.
  const note = [
    operator ? `you: ${operator.stance}` : undefined,
    reception.contested ? "a role is contested" : undefined,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <Peel
      title="The reception"
      count={entries}
      note={note || undefined}
      open={open}
      onToggle={onToggle}
    >
      {(operator || earlier.length > 0) && (
        <section className="panel">
          {operator ? (
            <>
              {/* The lead line of the depth, and the first thing in it: what
                  he said and when he said it. It is above Babel's reviewers
                  and above the rulings because it is the one line on this
                  page the reader wrote himself. */}
              <p className="record-you">
                You: <strong>{operator.stance}</strong>
                {operatorAt && (
                  <span className="record-you-when">
                    {" · "}
                    <time dateTime={operator.at} title={operatorAt.absolute}>
                      {operatorAt.relative}
                    </time>
                  </span>
                )}
              </p>
              {operator.reason && (
                <p className="quote untrusted-inline">{unescapeWhitespace(operator.reason)}</p>
              )}
              <p className="muted record-you-note">
                A reception is attributed, reversible and decides nothing
                {decisions.length > 0 ? "; the rulings that do are below." : "."}
              </p>
            </>
          ) : (
            <p>You hold no stance on this now.</p>
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

      {byRole.length > 0 && <RoleTable byRole={byRole} />}

      {reviewers.length > 0 && (
        <section className="panel">
          <h3>Babel's reviewers, one by one</h3>
          {counts && (
            <p className="muted">
              {counts.support} support, {counts.oppose} oppose, {counts.unsure} unsure across
              Babel's own runs — never counting your stance, and never summed across the
              questions above.
            </p>
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

// RoleTable is the reception as an instrument: one row per question a reviewer
// was asked, figures in tabular mono, and the arguments against folded under
// the row that is contested.
//
// A role with support on one side and opposition on the other is the one thing
// a flat tally cannot say, so it is marked and its opposing rationales are the
// only prose in the table.
function RoleTable({ byRole }: { byRole: RoleReception[] }) {
  return (
    <section className="panel">
      <h3>What each reviewer was asked</h3>
      <table className="record-roles">
        <thead>
          <tr>
            <th scope="col">The question</th>
            <th scope="col" className="record-num">
              Support
            </th>
            <th scope="col" className="record-num">
              Oppose
            </th>
            <th scope="col" className="record-num">
              Unsure
            </th>
          </tr>
        </thead>
        <tbody>
          {byRole.map((role) => {
            const contested = role.support > 0 && role.oppose > 0;
            const rationales = role.opposing_rationales ?? [];
            return (
              <tr key={role.role} className={contested ? "record-contested" : undefined}>
                <td>
                  {ROLE_WORDS[role.role] ?? role.role}
                  {contested && <span className="muted"> · reviewers disagree</span>}
                  {rationales.length > 0 && (
                    <details className="record-rationales">
                      <summary>
                        The case against ({rationales.length})
                      </summary>
                      <ul>
                        {rationales.map((rationale, index) => (
                          <li className="quote untrusted-inline" key={`${index}-${rationale.slice(0, 24)}`}>
                            {unescapeWhitespace(rationale)}
                          </li>
                        ))}
                      </ul>
                    </details>
                  )}
                </td>
                <td className={figureClass(role.support, "record-support")}>{role.support}</td>
                <td className={figureClass(role.oppose, "record-oppose")}>{role.oppose}</td>
                <td className={figureClass(role.unsure)}>{role.unsure}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}

// figureClass dims a zero. Nought opposed is a real answer and it must not
// shout as loudly as a count does.
function figureClass(value: number, tint?: string): string {
  const classes = ["record-num"];
  if (value === 0) classes.push("record-zero");
  else if (tint) classes.push(tint);
  return classes.join(" ");
}

// ReviewerLine says what a run was asked and how it answered, and does not
// name it. The actor is a run id — an opaque identifier with no honest display
// name, because inventing a friendly label for a run would be Babel naming its
// own reviewers — so it is numbered here and identified at depth 5, where the
// number and the id sit together.
function ReviewerLine({ reviewer, index }: { reviewer: ModelReception; index: number }) {
  const at = formatTime(reviewer.at);
  const role = ROLE_WORDS[reviewer.role] ?? reviewer.role;
  const stance = MODEL_STANCE_WORDS[reviewer.stance] ?? reviewer.stance;
  return (
    <>
      <span>
        Reviewer {index + 1}, asked {role}: {stance}
        {at && <span className="secondary"> · {at.relative}</span>}
      </span>
      {reviewer.rationale && (
        <p className="quote untrusted-inline">{unescapeWhitespace(reviewer.rationale)}</p>
      )}
    </>
  );
}

// MachineryPeel is depth 5: everything that identifies the object, what it
// cost to produce, and nothing a reader needs to understand it. It is one
// section rather than six so that a person debugging has one place to open,
// and it is collapsed so that a person reading never opens it.
function MachineryPeel({
  record,
  open,
  onToggle,
}: {
  record: RecordPeel;
  open: boolean;
  onToggle: (open: boolean) => void;
}) {
  const machinery: RecordMachinery = record.machinery ?? {};
  const created = formatTime(machinery.created_at);
  const links = machinery.links ?? [];
  const receipts = machinery.receipts ?? [];
  const revisions = machinery.revisions ?? [];
  const reviewers = record.reception?.model ?? [];
  const located = (record.evidence ?? []).filter((item) => item.session_id || item.path);

  return (
    <Peel title="The machinery" open={open} onToggle={onToggle}>
      <CostBlock cost={machinery.cost} />

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
          <h3>Review receipts</h3>
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

// CostBlock is what producing this record cost, as figures.
//
// Absent for a record this machine did not produce, because §9 seals the
// worker's accounting before a receipt leaves its host — so an absent block
// means nobody here can price it. A readable receipt that carries no price is
// a different fact and gets a different treatment: one small sentence, and no
// SPENT figure at all. "Unpriced" set as a 32-pixel mono figure is an unknown
// wearing a measurement's clothes, and the reader scanning the row of
// statistics reads the word as the amount.
function CostBlock({ cost }: { cost: RecordCost | undefined }) {
  if (!cost) return null;
  const tokens = (cost.input_tokens ?? 0) + (cost.output_tokens ?? 0);
  const spent = cost.usd ?? undefined;
  return (
    <section className="panel">
      <h3>What it cost to produce</h3>
      <div className="record-cost">
        {spent !== undefined && <Figure label="spent" value={money(spent)} />}
        {tokens > 0 && (
          <Figure
            label="tokens"
            value={tokens.toLocaleString()}
            note={`${(cost.input_tokens ?? 0).toLocaleString()} in · ${(cost.output_tokens ?? 0).toLocaleString()} out`}
          />
        )}
        {cost.duration_s !== undefined && cost.duration_s > 0 && (
          <Figure label="took" value={formatDuration(cost.duration_s * 1000)} />
        )}
        {cost.model && <Figure label="model" value={cost.model} />}
      </div>
      {/* Which of the two absences this is, in the record's own terms. A run
          whose receipt reports tokens and no dollars was priced by nobody; a
          run whose receipt reports neither recorded no usage at all. Neither
          of them is a free run and neither is a zero. */}
      {spent === undefined && (
        <p className="muted record-unpriced">
          {tokens > 0
            ? "the engine reported no price for what it used"
            : "the run recorded no usage"}
        </p>
      )}
    </section>
  );
}

// The five depths, in reading order. The array is the keyboard's map as well
// as the page's: Contract K gives a reader `1`-`5` to open and close a depth,
// and an index that meant one thing to the key handler and another to the
// renderer would be a page whose keys drift from its layout.
const DEPTHS = 5;

// Depths 1 and 2 are open because they are what the reader came for; the rest
// are folded because digging is a choice.
const INITIAL_DEPTHS = [true, true, false, false, false];

// withRecordedStance is the reception as the reader must see it the instant
// after he acts.
//
// His stance is durable the moment the write returns; the reception the page
// reads back is a projection over this instance's evaluation records, and on
// a machine with lanes writing it is seconds behind that write. Between the
// two the page said nothing at all — no operator in the response is no depth
// four for a record Babel has no reviewers for — so the only trace of the act
// was the button staying pressed.
//
// So the confirmed write stands in until the read catches up. It is the
// store's own echo and not the request's: the route answers with the stance
// it recorded and the time it recorded it, so what stands in here is a fact
// about the store rather than an optimism about it. A later read carrying a
// stance at least as new replaces it, and the stance it displaces becomes the
// first of the earlier ones — which is what §4.12's append says happened.
function withRecordedStance(
  reception: RecordReception | undefined,
  recorded: OperatorReception | undefined,
): RecordReception | undefined {
  if (!recorded) return reception;
  const served = reception?.operator;
  if (served && !recordedBefore(served.at, recorded.at)) return reception;
  const history = served ? [served, ...(reception?.history ?? [])] : reception?.history;
  return { ...reception, operator: recorded, ...(history?.length ? { history } : {}) };
}

// recordedBefore orders two recorded times, treating a time this build cannot
// read as the older of the two: the confirmation in hand is a fact, and an
// unparseable timestamp beside it is not a reason to keep showing an answer
// the operator has already replaced.
function recordedBefore(earlier: string, later: string): boolean {
  const left = Date.parse(earlier);
  const right = Date.parse(later);
  if (Number.isNaN(left)) return true;
  if (Number.isNaN(right)) return false;
  return left < right;
}

// RecordPeels is the record itself, five depths deep, with the connections
// strip between the evidence and the reception.
export function RecordPeels({
  record,
  recorded,
  voteRef,
  onActed,
}: {
  record: RecordPeel;
  // What the store confirmed on this page's own vote, held by the page until
  // a read carries it. The arrows live in the post header, so the confirmation
  // arrives from above rather than from a control inside these depths.
  recorded?: OperatorReception;
  // The header's arrows, so `a`/`d`/`u` press the real control rather than
  // reimplementing what it does: a vote recorded by key and a vote recorded by
  // click are then the same code path, optimistic update and refusal included.
  voteRef: RefObject<HTMLDivElement | null>;
  onActed: (message: string) => void;
}) {
  const reception = withRecordedStance(record.reception, recorded);
  const evidence = record.evidence ?? [];
  const [open, setOpen] = useState<boolean[]>(INITIAL_DEPTHS);
  const bar = useRef<HTMLDivElement | null>(null);
  const received = Boolean(
    reception &&
      (reception.operator ||
        reception.history?.length ||
        reception.model?.length ||
        reception.decisions?.length ||
        reception.by_role?.length),
  );

  const toggle = useCallback((depth: number) => {
    setOpen((current) => current.map((value, index) => (index === depth ? !value : value)));
  }, []);

  const setDepth = useCallback((depth: number, value: boolean) => {
    setOpen((current) => current.map((entry, index) => (index === depth ? value : entry)));
  }, []);

  // An act the reader performs is an act he has to be able to see. A ruling
  // lands at depth 4, which is folded until he opens it, so deciding would
  // otherwise change nothing on the page except the button he pressed.
  const acted = useCallback(
    (message: string) => {
      setDepth(3, true);
      onActed(message);
    },
    [onActed, setDepth],
  );

  // Contract K, for this page. The handler presses the real controls rather
  // than duplicating what they do: a vote recorded by key goes through the
  // same optimistic post and the same refusal handling as a vote recorded by
  // click, and `r` moves the focus to the ruling the reader is about to make
  // instead of recording one for him — a permanent, attributed event is never
  // one keystroke away.
  useEffect(() => {
    function press(node: HTMLElement | null | undefined) {
      if (!node) return;
      node.focus();
      node.click();
    }

    function onKey(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target as HTMLElement | null;
      if (target) {
        const tag = target.tagName;
        if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || target.isContentEditable) {
          return;
        }
      }
      const depth = Number.parseInt(event.key, 10);
      if (Number.isInteger(depth) && depth >= 1 && depth <= DEPTHS) {
        toggle(depth - 1);
        event.preventDefault();
        return;
      }
      if (event.key === "a" || event.key === "d") {
        const stance = event.key === "a" ? "agree" : "disagree";
        press(voteRef.current?.querySelector<HTMLElement>(`[data-stance="${stance}"]`));
        event.preventDefault();
        return;
      }
      if (event.key === "u") {
        // Unsure is what a lit arrow records when it is pressed again, so the
        // key presses the vote he has cast rather than a third control that
        // no longer exists. With no vote cast there is nothing to withdraw.
        press(voteRef.current?.querySelector<HTMLElement>('[aria-pressed="true"]'));
        event.preventDefault();
        return;
      }
      if (event.key === "r") {
        setDepth(0, true);
        // The claim may have been folded, so the control is reached on the
        // next frame rather than in this one, when it may not be mounted yet.
        requestAnimationFrame(() => bar.current?.querySelector<HTMLElement>("[data-ruling]")?.focus());
        event.preventDefault();
      }
    }

    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [setDepth, toggle, voteRef]);

  return (
    <div className="surface">
      <ClaimPeel
        record={record}
        open={open[0]}
        onToggle={(value) => setDepth(0, value)}
        onActed={acted}
        barRef={bar}
      />
      {hasCase(record.case) && (
        <CasePeel
          detail={record.case}
          origin={record.origin}
          open={open[1]}
          onToggle={(value) => setDepth(1, value)}
        />
      )}
      {/* A record with no case still says where it came from: a candidate is a
          bare claim, and the conversation it was born in is the most useful
          thing on its page. */}
      {!hasCase(record.case) && <OriginStrip origin={record.origin} />}
      {evidence.length > 0 && (
        <EvidencePeel
          items={evidence}
          open={open[2]}
          onToggle={(value) => setDepth(2, value)}
        />
      )}
      <RelatedStrip related={record.related} />
      {received && reception && (
        <ReceptionPeel
          reception={reception}
          open={open[3]}
          onToggle={(value) => setDepth(3, value)}
        />
      )}
      <MachineryPeel record={record} open={open[4]} onToggle={(value) => setDepth(4, value)} />

      {/* How a reader learns the page has a keyboard. It is the last thing on
          the page and the smallest type on it, because it is never what he
          came for. */}
      <p className="record-keys">
        <span>
          <kbd className="kbd">1</kbd>–<kbd className="kbd">5</kbd> depth
        </span>
        <span>
          <kbd className="kbd">a</kbd>
          <kbd className="kbd">d</kbd> vote
        </span>
        <span>
          <kbd className="kbd">u</kbd> withdraw
        </span>
        <span>
          <kbd className="kbd">r</kbd> rule
        </span>
      </p>
    </div>
  );
}

// LONG_CLAIM is where a headline stops being one.
//
// A record that wrote itself a title wrote a name: a few words, which is what
// display type is for. A record that did not is headed by its own statement —
// a hypothesis and an observation write no name, and the server hands their
// statement over as the title as well — and a statement is a sentence that
// routinely runs to several hundred characters. Past this length the shell's
// display size stops being display type and becomes eight lines of it, so the
// heading says it is long and the shell sets it at an editorial reading size.
// The test is the length rather than the presence of a title, because the
// length is what breaks the type.
const LONG_CLAIM = 120;

// RecordHeading is the post: the score with the operator's arrows, what kind
// of thing this is, where it stands, the sentence it is, and the one line of
// facts §8.7 gives a post — its topics, its author and its age.
//
// It is the feed row opened. The arrows are the same component the feed
// mounts, the topics are the same chips, and the claim is the same line: a
// reader who clicked a row finds the row he clicked at the top of the page,
// which is the whole of "the shape of a post".
//
// The two badges are the only ones here — standing and kind — and nothing else
// wears one. The heading is the record's own words, so it carries the quoted
// frame even as an h1, and it is set in the editorial face at one measure:
// decision 90 makes depth 1 editorial, and the claim is what that decision is
// about. It is the title when the record wrote one; a hypothesis and an
// observation write none — the statement is the record, not a name for it — so
// their claim is the heading instead. The kind is not a fallback heading: the
// badge beside it already says "Observation", and an h1 repeating that would
// name the class of thing twice and the thing itself never.
export function RecordHeading({
  record,
  recorded,
  voteRef,
  onVoted,
}: {
  record: RecordPeel;
  // The stance this page has recorded and the read has not caught up with,
  // exactly as depth 4 uses it: the arrows seed from the reception, and the
  // reception the page shows includes his own act the moment it is confirmed.
  recorded?: OperatorReception;
  voteRef?: RefObject<HTMLDivElement | null>;
  onVoted?: (recorded: OperatorReception) => void;
}) {
  const standing = record.standing;
  const headline = record.title ?? record.claim;
  const long = headline !== undefined && headline.length > LONG_CLAIM;
  const tally = receptionTally(withRecordedStance(record.reception, recorded));
  const topics = topicsOf(record.origin);
  const runID = record.machinery?.run_id;
  const age = formatTime(record.machinery?.created_at);
  return (
    <header className="surface record-post">
      {/* The arrows take the left column of the post, where a reader coming
          from the feed already found them — and under them the four words
          §8.6 requires of the operator's own voice. They are here rather than
          inside the arrows because the arrows are also a feed row's, and a
          sentence repeated down twenty-five rows is noise; on the page where
          he acts on one record, it is the thing he has to be told. */}
      <div className="record-post-vote" ref={voteRef}>
        <VoteArrows
          id={record.id}
          score={tally.score}
          support={tally.support}
          oppose={tally.oppose}
          unsure={tally.unsure}
          you={tally.you}
          onVoted={(next) =>
            onVoted?.({ stance: next.you as OperatorStance, at: new Date().toISOString() })
          }
        />
        <span className="record-acts-label record-post-note">your vote · decides nothing</span>
      </div>
      <div className="record-post-body">
        <div className="heading-badges">
          <Badge label={KIND_WORDS[record.kind] ?? record.kind} tone="neutral" />
          {standing && <Badge label={standing.label} tone={standingTone(standing.tone)} />}
        </div>
        {headline ? (
          <h1 className={`quote untrusted-inline record-claim${long ? " long" : ""}`}>
            {unescapeWhitespace(headline)}
          </h1>
        ) : (
          <h1>{KIND_WORDS[record.kind] ?? "Record"}</h1>
        )}
        {/* One line, at most three facts: where it is filed, who wrote it and
            how old it is. A record whose origin this deployment cannot resolve
            has no topic and says nothing in its place. */}
        {(topics.length > 0 || runID || age) && (
          <p className="record-post-meta">
            {topics.map((topic) => (
              <Link className="chip record-topic" key={topic} to={`/t/${encodeURIComponent(topic)}`}>
                t/{topic}
              </Link>
            ))}
            {runID && (
              <span>
                by{" "}
                <Link className="record-post-run" to={`/watch/runs/${encodeURIComponent(runID)}`}>
                  {runID}
                </Link>
              </span>
            )}
            {age && (
              <time dateTime={record.machinery?.created_at} title={age.absolute}>
                {age.relative}
              </time>
            )}
          </p>
        )}
      </div>
    </header>
  );
}

// receptionTally is §8.7's one number, out of depth four.
//
// "The score is support minus oppose across all of them, because a vote is a
// vote and the operator is one voter among Babel's reviewers." Babel's side is
// the reception tally the evaluation store keeps for this revision, and the
// per-role rows stand in when the store sent none: `counts` is omitted rather
// than zeroed when it is empty, and a record whose votes only reached the page
// as role rows would otherwise score nought with five supports on screen.
//
// The operator is added as the one vote he is. Nothing here sums his stance
// into Babel's own figures — the depth-4 table still reads model-only, which
// is §4.12's boundary — and the arrows subtract him back out for the
// breakdown, so the number is one number and its parts stay attributed.
function receptionTally(reception: RecordReception | undefined): {
  score: number;
  support: number;
  oppose: number;
  unsure: number;
  you: VoteStance;
} {
  const roles = reception?.by_role ?? [];
  const babel =
    reception?.counts ??
    roles.reduce(
      (total, role) => ({
        support: total.support + role.support,
        oppose: total.oppose + role.oppose,
        unsure: total.unsure + role.unsure,
      }),
      { support: 0, oppose: 0, unsure: 0 },
    );
  const you: VoteStance = reception?.operator?.stance ?? "";
  const support = babel.support + (you === "agree" ? 1 : 0);
  const oppose = babel.oppose + (you === "disagree" ? 1 : 0);
  const unsure = babel.unsure + (you === "unsure" ? 1 : 0);
  return { score: support - oppose, support, oppose, unsure, you };
}

// topicsOf files a record under the topics its evidence came from.
//
// §8.7: "today a topic is a repository: the workspace of the sessions a record
// cites, named by its last path element, so the same project seen from two
// machines is one topic". The peel carries one origin — the first cited
// session this deployment holds — so a record page files under one topic and
// the feed, which resolves the whole lineage, may file the same record under
// more. The list shape is what keeps those two from being different ideas.
//
// Nothing here assumes the name is a directory. It is the last element of
// whatever the workspace string is, and a workspace this deployment did not
// record leaves the record untopiced rather than hidden.
function topicsOf(origin: RecordOrigin | undefined): string[] {
  const workspace = origin?.workspace?.replace(/[/\\]+$/u, "") ?? "";
  if (!workspace) return [];
  const name = workspace.split(/[/\\]/u).pop() ?? "";
  return name ? [name] : [];
}

// The conversation under the post.
//
// §8.7: "a reviewer's contribution prose, a refinement, the operator's reason
// in his own words, the answer to a question and the reason on a
// reconsideration are all comments, threaded by what they relate to and shown
// newest-first under the record's five depths, with a box the operator writes
// into". Five kinds of record and one thread, because they are one thing to
// read: the store each line was written into is Babel's business, which is the
// sentence this whole surface is built on.
//
// Rulings are in the list and are not comments. Accept, reject, defer,
// duplicate and reopen are §4.7's append-only authority, so they render as
// attributed acts in their own chronological place, with their own shape and
// their own class: a decision that looked like an opinion would be the one
// confusion this thread cannot afford.
//
// The box records a reason and no polarity. The operator's vote is the arrows
// at the top of the post and nothing else moves the score, so writing here
// says something without voting — which is exactly what §8.6's third act is.

// COMMENT_PAGE is how much conversation one page shows. §8.6's density rule
// bounds the page at roughly three screens, and a record Babel reviewed four
// times with a refinement round runs past that on comments alone.
const COMMENT_PAGE = 20;

// The word a comment needs beside its author, and only when it disambiguates.
// A reviewer's prose and the operator's own reason are what a comment is, so
// they are unlabelled; a refinement, an answer and a reconsideration are three
// different acts wearing the same shape, so each says which it is.
const COMMENT_WORDS: Record<Comment["kind"], string> = {
  contribution: "",
  reason: "",
  refinement: "refinement",
  answer: "answer",
  reconsideration: "reconsidered",
};

// The five rulings in the past tense, because the thread shows what was done
// rather than offering to do it. The control that offers them is the rule bar
// at depth 1, and it keeps the imperative.
const ACT_WORDS: Record<Act["act"], string> = {
  accept: "accepted",
  reject: "rejected",
  defer: "deferred",
  duplicate: "marked duplicate",
  reopen: "reopened",
};

// One row of the thread: a comment or an act, with the time it sorts by
// already parsed, because the merge orders both by one clock.
interface ThreadEntry {
  at: number;
  comment?: Comment;
  act?: Act;
}

export function RecordThread({
  id,
  reload = 0,
  onPosted,
}: {
  id: string;
  // A ruling recorded on this page appends to the thread, so the page that
  // records one bumps this and the thread reads itself again. It is a counter
  // rather than a callback because the thread owns its own read.
  reload?: number;
  onPosted?: (message: string) => void;
}) {
  const [thread, setThread] = useState<CommentThread | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [posting, setPosting] = useState(false);
  const [shown, setShown] = useState(COMMENT_PAGE);

  useEffect(() => {
    let live = true;
    setThread(null);
    setFailure(null);
    setShown(COMMENT_PAGE);
    getComments(id)
      .then((value) => {
        if (live) setThread(value);
      })
      .catch((reason) => {
        if (live) setFailure(errorMessage(reason));
      });
    return () => {
      live = false;
    };
  }, [id, reload]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const written = text.trim();
    if (!written) return;
    setPosting(true);
    setFailure(null);
    try {
      const result = await postComment(id, written);
      // His own words go in at the top, where the thread's order puts them,
      // rather than the page reading itself again: the response is the record
      // the store wrote, so showing it is showing a fact about the store.
      setThread((current) => ({
        comments: [result.comment, ...(current?.comments ?? [])],
        acts: current?.acts ?? [],
        total: (current?.total ?? 0) + 1,
      }));
      setText("");
      onPosted?.("Your comment is recorded. It changes no vote.");
    } catch (reason) {
      setFailure(errorMessage(reason));
    } finally {
      setPosting(false);
    }
  }

  const comments = thread?.comments ?? [];
  const entries = threadEntries(comments, thread?.acts ?? []);
  const visible = entries.slice(0, shown);
  const total = thread?.total ?? 0;
  const operators = operatorIDs(comments);

  return (
    <section className="surface record-thread" id="comments">
      <h2>{total === 1 ? "1 comment" : `${total} comments`}</h2>

      <form className="record-comment-form" onSubmit={submit}>
        <textarea
          aria-label="Your comment"
          value={text}
          rows={3}
          onChange={(event) => setText(event.target.value)}
          placeholder="Your own words, kept verbatim."
        />
        <div className="record-comment-acts">
          <button type="submit" className="primary-button" disabled={posting || !text.trim()}>
            {posting && <span className="spinner small" />}
            {posting ? "Recording…" : "Comment"}
          </button>
          {/* What writing here does and does not do, in the register the rule
              bar uses for the same distinction one depth up. */}
          <span className="record-acts-label">kept verbatim · changes no vote</span>
        </div>
      </form>

      {failure && (
        <p className="inline-error" role="alert">
          {failure}
        </p>
      )}

      {thread === null && !failure && (
        <p className="muted">
          <span className="spinner" /> Reading the thread…
        </p>
      )}
      {thread !== null && entries.length === 0 && <p className="muted">No comments yet.</p>}

      {visible.length > 0 && (
        <ol className="record-thread-list">
          {visible.map((entry) =>
            entry.comment ? (
              <CommentRow key={entry.comment.id} comment={entry.comment} />
            ) : entry.act ? (
              <ActRow key={entry.act.id} act={entry.act} operators={operators} />
            ) : null,
          )}
        </ol>
      )}

      {entries.length > visible.length && (
        <button type="button" onClick={() => setShown((current) => current + COMMENT_PAGE)}>
          Show {Math.min(COMMENT_PAGE, entries.length - visible.length)} more
        </button>
      )}
    </section>
  );
}

// CommentRow is one line of the conversation and its answers.
//
// Replies nest exactly one level. A thread that indents every answer walks off
// the right edge after four of them, and §8.6's density rule is what says one
// level is enough: this is a discussion under a claim, not a tree the reader
// has to navigate.
function CommentRow({ comment }: { comment: Comment }) {
  const replies = descendants(comment);
  return (
    <li className="record-comment">
      <CommentLine comment={comment} />
      {replies.length > 0 && (
        <ol className="record-replies">
          {replies.map((reply) => (
            <li className="record-comment" key={reply.id}>
              <CommentLine comment={reply} />
            </li>
          ))}
        </ol>
      )}
    </li>
  );
}

// CommentLine is who said it, in what capacity, when — and then what they
// said, verbatim, inside the quoted frame every piece of untrusted prose on
// this page wears. The operator is "you" and a run is its own name, which
// reaches its run page: §8.7 makes a run the author of what it wrote.
function CommentLine({ comment }: { comment: Comment }) {
  const at = formatTime(comment.at);
  const word = COMMENT_WORDS[comment.kind] ?? "";
  const who = comment.author.kind === "operator" ? "you" : comment.author.id;
  const role = comment.role ? (ROLE_WORDS[comment.role as ModelRole] ?? comment.role) : "";
  return (
    <>
      <p className="record-comment-by">
        {comment.author.href ? (
          <Link className="record-comment-who" to={comment.author.href}>
            {who}
          </Link>
        ) : (
          <span className="record-comment-who">{who}</span>
        )}
        {role && <> · asked {role}</>}
        {word && <> · {word}</>}
        {at && (
          <>
            {" · "}
            <time dateTime={comment.at} title={at.absolute}>
              {at.relative}
            </time>
          </>
        )}
      </p>
      <p className="quote untrusted-inline record-comment-text">
        {unescapeWhitespace(comment.text)}
      </p>
    </>
  );
}

// ActRow is one ruling, in the thread, as the act it is: one line, attributed,
// dated, with the reason it carried. It is not a comment and does not look
// like one — §8.7 calls the rulings "the moderator's log" — so it wears its
// own class and the thread's own shape stops at the indent.
function ActRow({ act, operators }: { act: Act; operators: Set<string> }) {
  const at = formatTime(act.at);
  return (
    <li className="record-ruling">
      <p>
        <strong>{ACT_WORDS[act.act] ?? act.act}</strong>
        {act.by && <> by {operators.has(act.by) ? "you" : act.by}</>}
        {at && (
          <>
            {" · "}
            <time dateTime={act.at} title={at.absolute}>
              {at.relative}
            </time>
          </>
        )}
        {act.reason && <> · <span className="untrusted-inline">{unescapeWhitespace(act.reason)}</span></>}
      </p>
    </li>
  );
}

// threadEntries merges the conversation and the log into one newest-first
// list. §8.7 puts the rulings in the thread rather than beside it, so they
// sort by the same clock as the comments and land where they happened.
function threadEntries(comments: Comment[], acts: Act[]): ThreadEntry[] {
  const entries: ThreadEntry[] = [
    ...comments.map((comment) => ({ at: timeValue(comment.at), comment })),
    ...acts.map((act) => ({ at: timeValue(act.at), act })),
  ];
  return entries.sort((left, right) => (left.at === right.at ? 0 : right.at - left.at));
}

// descendants flattens a comment's whole subtree into the one level of
// indentation the thread has, newest first.
function descendants(comment: Comment): Comment[] {
  const out: Comment[] = [];
  function walk(list: Comment[]) {
    for (const reply of list) {
      out.push(reply);
      walk(reply.replies ?? []);
    }
  }
  walk(comment.replies ?? []);
  return out.sort((left, right) => {
    const [first, second] = [timeValue(left.at), timeValue(right.at)];
    return first === second ? 0 : second - first;
  });
}

// operatorIDs is who "you" is, as the payload itself says it.
//
// The client never holds the operator's own identity: §4.12 resolves the
// author server-side and the write routes refuse an author field, which is
// what stops a caller from recording under another name. So a ruling reads as
// "by you" only when the same thread carries an operator-authored line under
// that id, and carries the recorded id otherwise — on a deployment with two
// operators, guessing would attribute one man's decision to the other.
function operatorIDs(comments: Comment[]): Set<string> {
  const ids = new Set<string>();
  function walk(list: Comment[]) {
    for (const comment of list) {
      if (comment.author.kind === "operator" && comment.author.id) ids.add(comment.author.id);
      walk(comment.replies ?? []);
    }
  }
  walk(comments);
  return ids;
}

// timeValue orders a thread by its own clock, treating a time this build
// cannot read as the oldest thing in the list rather than dropping the line:
// an unparseable timestamp is a reason to sort a comment last, never a reason
// to hide what somebody said.
function timeValue(value: string): number {
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? Number.NEGATIVE_INFINITY : parsed;
}
