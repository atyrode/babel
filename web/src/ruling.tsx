import { useEffect, useRef, useState, type FormEvent, type RefObject } from "react";
import {
  addReviewContext,
  decideReview,
  inviteRecord,
  type Disposition,
  type ReviewSubjectType,
} from "./api";
import { errorMessage } from "./format";
import { postComment, type RecordKind } from "./recordapi";
import "./record.css";

// The operator's acts on one record, wherever he reads it.
//
// §8.7: "Babel votes; the operator rules." He has no stance and no arrows —
// "me voting is a subpar concept, since I would rather just triage the idea at
// this point" — so the acts a record offers are the rulings themselves and the
// question he asks about it. The same five acts belong on a feed row and on
// the post, which is why they are here rather than in either: a second
// confirmation flow over one append-only authority is a second idea of what
// "accept" means, and the first thing to drift.
//
// Two invariants this file exists to keep:
//
//   - A ruling is confirmed before it is recorded. It is an appended,
//     attributed event that cannot be edited or undone, so the button opens
//     one sentence saying what it does and the press that follows is the act.
//     Ask is not a ruling and is not confirmed: it records a question.
//   - Nothing here invents a route. Accept, reject, defer, duplicate and
//     reopen are /api/review/decide's vocabulary; refine is the existing
//     invitation (/api/record/invite), which carries no instruction because
//     what to do with the record is the next run's judgement; ask is a comment
//     with `question` for its kind.
//
// The stylesheet is record.css, for the reason stated there: one control, one
// set of rules, whichever surface mounts it.

// RuleAct is everything the operator can do to a record from the surface that
// lists it. The five dispositions are the review service's own words; `refine`
// and `ask` are the two acts that are not dispositions and are named apart
// from them for exactly that reason.
export type RuleAct = Disposition | "refine" | "ask";

// What each act is called, what it does in one sentence, and the key that
// presses it. The sentence is shown at the moment of confirming and nowhere
// else: a page that explained five permanent acts before the reader had chosen
// one was #234's measured defect.
const ACTS: Array<{ value: RuleAct; label: string; key?: string; confirm: string }> = [
  {
    value: "accept",
    label: "Accept",
    key: "y",
    confirm: "Endorse this record for projection and follow-on work. The event is appended permanently.",
  },
  {
    value: "reject",
    label: "Reject",
    key: "n",
    confirm: "Record disagreement. The record is kept, visibly rejected, and the event is appended permanently.",
  },
  {
    value: "defer",
    label: "Defer",
    key: "d",
    confirm: "Not now. The record stays readable with its history and the event is appended permanently.",
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
  {
    value: "refine",
    label: "Refine",
    key: "f",
    confirm:
      "Ask Babel to work this record further. The invitation carries no instruction — refine, question, amend or abandon is the next run's judgement — and it decides nothing.",
  },
  {
    value: "ask",
    label: "Ask",
    key: "q",
    confirm: "",
  },
];

// The acts a row of a listing offers: the four rulings §8.7 names and the
// question. Duplicate needs another record's identifier and reopen needs a
// reason for a decision already made, and neither is a thing to do from a row
// while skimming — they stay on the record, which is where the reader has the
// other record and the earlier decision in front of him.
export const ROW_ACTS: RuleAct[] = ["accept", "reject", "defer", "refine", "ask"];

// The acts the record itself offers: everything, because the reader is on the
// object and has read it.
export const POST_ACTS: RuleAct[] = [
  "accept",
  "reject",
  "defer",
  "duplicate",
  "reopen",
  "refine",
  "ask",
];

// The keys that press an act, for the two surfaces that have a focused record:
// the feed's row and the post. They press the real control rather than posting
// on their own, so a ruling recorded by key goes through the same confirmation
// as one recorded by click — a permanent, attributed event is never one
// keystroke away.
export const RULE_KEYS: Record<string, RuleAct> = Object.fromEntries(
  ACTS.filter((act) => act.key).map((act) => [act.key as string, act.value]),
);

// What an act is called once it is done. The row that recorded one shows this
// in place of the controls until the next read, because a list that looked
// exactly the same after a permanent act is a list an operator rules on twice.
export const ACT_DONE: Record<RuleAct, string> = {
  accept: "accepted",
  reject: "rejected",
  defer: "deferred",
  duplicate: "marked duplicate",
  reopen: "reopened",
  refine: "sent back for refinement",
  ask: "asked",
};

// reviewSubject answers whether a record kind can carry a review decision.
// internal/review answers "this record kind carries no review decision" for an
// observation, so no disposition is ever asked about one and the ruling
// controls are absent rather than present and refused. A question about it is
// still a question, so Ask survives the absence.
export function reviewSubject(kind: RecordKind): ReviewSubjectType | null {
  switch (kind) {
    case "proposal":
    case "finding":
    case "hypothesis":
      return kind;
    default:
      return null;
  }
}

// RuleActs is the bar of acts and whatever one of them opened.
//
// Nothing in it renders a heading or a container, so it drops into a feed row
// as readily as into a depth of the record. It has two appearances and they
// are named here rather than restyled by whichever stylesheet mounts it: the
// segmented bar on the record, where the reader has read the thing and the
// acts are the page's business, and a row of text actions on a listing, where
// they appear under the pointer on one row of fifteen and a bar of five
// bordered buttons would be the row shouting.
export function RuleActs({
  id,
  kind,
  acts = POST_ACTS,
  onActed,
  barRef,
  label,
  plain,
}: {
  id: string;
  kind: RecordKind;
  // Which acts this surface offers, in the order it offers them.
  acts?: RuleAct[];
  // What was recorded: the act, and the sentence the surface announces. The
  // act is handed back because a listing shows what it did in place of the
  // controls, and a page that had to parse the sentence for it would be
  // reading its own prose.
  onActed: (act: RuleAct, message: string) => void;
  // The real controls, so a page that owns the keyboard presses them instead
  // of reimplementing what they do.
  barRef?: RefObject<HTMLDivElement | null>;
  // The four words under the bar that say what the group is. A row has no
  // space for them and passes none.
  label?: string;
  // Render the acts as a row of text actions rather than as a segmented bar.
  plain?: boolean;
}) {
  const subject = reviewSubject(kind);
  const [open, setOpen] = useState<RuleAct | null>(null);

  // An act whose authority this kind cannot carry is not offered. Ask is
  // offered for every kind, because a question is about the record rather than
  // about its standing.
  const offered = ACTS.filter(
    (act) => acts.includes(act.value) && (act.value === "ask" || subject !== null),
  );
  if (offered.length === 0) return null;

  return (
    <>
      <div className={plain ? "record-acts record-acts-text" : "record-acts"} ref={barRef}>
        <div
          className={plain ? undefined : "rule-bar"}
          role="group"
          aria-label={`Act on this ${kind}`}
        >
          {offered.map((act) => (
            <button
              type="button"
              key={act.value}
              data-ruling={act.value}
              className={open === act.value ? "active" : undefined}
              aria-expanded={open === act.value}
              title={act.value === "ask" ? "Ask Babel about this record" : act.confirm}
              onClick={() => setOpen(open === act.value ? null : act.value)}
            >
              {act.label}
            </button>
          ))}
        </div>
        {label && <span className="record-acts-label">{label}</span>}
      </div>
      {/* The confirmation unfolds where the button was, so the claim being
          ruled on is still on screen. The fold is a wrapper rather than a
          property of the panel because a grid row is what can be animated from
          nothing to its own height. */}
      {open === "ask" && (
        <div className="record-confirm-fold">
          <AskBox
            id={id}
            onCancel={() => setOpen(null)}
            onAsked={(message) => {
              setOpen(null);
              onActed("ask", message);
            }}
          />
        </div>
      )}
      {open && open !== "ask" && subject && (
        <div className="record-confirm-fold">
          <ActConfirm
            act={open}
            subject={subject}
            id={id}
            onCancel={() => setOpen(null)}
            onDecided={(message) => {
              setOpen(null);
              onActed(open, message);
            }}
          />
        </div>
      )}
    </>
  );
}

// ActConfirm is the confirmation, and it is the whole of it: one sentence
// saying what the act does, whatever that act needs, and two buttons.
//
// The requirement it keeps is unchanged. A disposition is confirmed before it
// is recorded, appended rather than edited, attributed to the launch session's
// operator; a reopen requires a reason, because the service refuses one
// without and asking here says why instead of letting the server say no. A
// refinement carries no field at all: #87's invitation says a record deserves
// attention and deliberately does not say what to do about it, and the route
// refuses a body that smuggles an instruction in.
//
// The guidance field stays, folded, for the dispositions. It is the one input
// that is not about this decision — attributed context is what a later
// refinement run will see — so it is available and out of the way.
function ActConfirm({
  act,
  subject,
  id,
  onCancel,
  onDecided,
}: {
  act: Exclude<RuleAct, "ask">;
  subject: ReviewSubjectType;
  id: string;
  onCancel: () => void;
  onDecided: (message: string) => void;
}) {
  const option = ACTS.find((entry) => entry.value === act);
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
      if (act === "refine") {
        // The head the surface rendered is the revision this act confirms:
        // the feed and the record both read chain heads, so the id on screen
        // is it, and a chain that moved since answers 409 with its own
        // wording rather than writing against a revision nobody saw.
        const result = await inviteRecord({ type: subject, id }, id);
        onDecided(`Sent back for refinement. The instruction is ${result.instruction}`);
        return;
      }
      let contextId: string | undefined;
      if (contextText.trim()) {
        contextId = (await addReviewContext(contextText.trim())).id;
      }
      const result = await decideReview({
        subject: { type: subject, id },
        disposition: act,
        contextId,
        duplicateOfId: act === "duplicate" ? duplicateOf.trim() || undefined : undefined,
        note: note.trim() || undefined,
      });
      onDecided(`Recorded ${act}. The record's status is now ${result.status}.`);
    } catch (reason) {
      setFailure(errorMessage(reason));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form className="record-confirm" onSubmit={submit}>
      <p>{option?.confirm}</p>
      {act === "duplicate" && (
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
      {act !== "refine" && (
        <label>
          {act === "reopen"
            ? "Why the earlier decision stopped holding (required)"
            : "Note (optional, recorded with the event)"}
          <textarea
            value={note}
            onChange={(event) => setNote(event.target.value)}
            rows={2}
            required={act === "reopen"}
          />
        </label>
      )}
      {act !== "refine" && (
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
      )}
      <div className="record-confirm-acts">
        <button type="submit" className="primary-button" ref={confirmRef} disabled={submitting}>
          {submitting && <span className="spinner small" />}
          {submitting ? "Recording…" : `Confirm ${act}`}
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

// AskBox is the question, in one line.
//
// §8.7: ask is "a question to Babel about this record, recorded as a comment
// Babel's next review of the record must answer". It is not confirmed, because
// it authorizes nothing and decides nothing — it is the operator's own words,
// kept verbatim, with a marker on them that a later review can find.
function AskBox({
  id,
  onCancel,
  onAsked,
}: {
  id: string;
  onCancel: () => void;
  onAsked: (message: string) => void;
}) {
  const [text, setText] = useState("");
  const [posting, setPosting] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => inputRef.current?.focus(), []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const written = text.trim();
    if (!written) return;
    setPosting(true);
    setFailure(null);
    try {
      await postComment(id, written, "question");
      onAsked("Your question is recorded. Babel's next review of this record must answer it.");
    } catch (reason) {
      setFailure(errorMessage(reason));
    } finally {
      setPosting(false);
    }
  }

  return (
    <form className="record-confirm record-ask" onSubmit={submit}>
      <label>
        What do you want to know about this record?
        <input
          ref={inputRef}
          value={text}
          onChange={(event) => setText(event.target.value)}
          placeholder="Kept verbatim. Babel's next review must answer it."
        />
      </label>
      <div className="record-confirm-acts">
        <button type="submit" className="primary-button" disabled={posting || !text.trim()}>
          {posting && <span className="spinner small" />}
          {posting ? "Recording…" : "Ask"}
        </button>
        <button type="button" onClick={onCancel} disabled={posting}>
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
