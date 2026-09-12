import { useEffect, useRef, useState, type FormEvent, type ReactElement } from "react";
import type { HostServices } from "@manifold/plugin";
import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS, type PostKind, type Ruling } from "../contract.ts";
import { ask, look, refusal, since, type FeedPost } from "./api.ts";
import { Votes } from "./votes.tsx";

/*
  ONE POST, as a row, and the acts it invites.

  Two lines and a gutter: the gutter is what Babel's reviewers said (votes.tsx), line one is
  the claim, line two is what a reader decides with — the kind, where it is filed, how old it
  is, how much has been said about it, and, when it waits on him, why in five words.

  THE ACTS ARE HIDDEN until the row is under the pointer, holds the keyboard, or is the row
  `j`/`k` put the focus on: five controls on every waiting row is seventy-five buttons on the
  first screen. A RULING IS CONFIRMED BEFORE IT IS RECORDED — it is an appended, attributed
  event that cannot be edited — so the button opens one sentence saying what it does and the
  press that follows is the act. The keys press these same controls rather than posting, so a
  ruling recorded by keyboard goes through the same confirmation as one recorded by click.

  A QUESTION IS NOT RULED ON. §4.8 gives it exactly three outcomes, and they are offered here,
  in place of the rulings, with the words typed into a box that unfolds under the row — so
  answering the question at the top of the feed costs no panel change.
*/

/** The kind's tone: the quiet half of the palette, `question` amber because it is addressed to him. */
export const KIND_TONES: Record<PostKind, string> = {
  proposal: "accent",
  finding: "good",
  hypothesis: "info",
  question: "warn",
};

export const KIND_LABELS: Record<PostKind, string> = {
  proposal: "Proposal",
  finding: "Finding",
  hypothesis: "Hypothesis",
  question: "Question",
};

/** An act on a record from a surface that lists it: the rulings, plus the question. */
export type RuleAct = Ruling | "ask";

interface ActWord {
  readonly label: string;
  readonly key: string;
  readonly confirm: string;
  readonly done: string;
}

/**
 * What each act is called, what it does in one sentence, and the key that presses it. The
 * sentence is shown at the moment of confirming and nowhere else: five permanent acts
 * explained before the reader has chosen one is a paragraph of chrome over a list.
 */
export const ACTS: Record<RuleAct, ActWord> = {
  accept: {
    label: "Accept",
    key: "y",
    confirm: "Endorse this record for projection and follow-on work. The event is appended permanently.",
    done: "accepted",
  },
  reject: {
    label: "Reject",
    key: "n",
    confirm: "Record disagreement. The record is kept, visibly rejected, and the event is appended permanently.",
    done: "rejected",
  },
  defer: {
    label: "Defer",
    key: "d",
    confirm: "Not now. The record stays readable with its history and the event is appended permanently.",
    done: "deferred",
  },
  duplicate: {
    label: "Duplicate",
    key: "",
    confirm: "Point this record at an original, which you name below. The event is appended permanently.",
    done: "marked duplicate",
  },
  reopen: {
    label: "Reopen",
    key: "",
    confirm:
      "Undecide it. The earlier decision stays in the history, the record's standing returns to new, and your reason is required.",
    done: "reopened",
  },
  refine: {
    label: "Refine",
    key: "f",
    confirm:
      "Ask Babel to work this record further. The invitation carries no instruction — refine, question, amend or abandon is the next run's judgement — and it decides nothing.",
    done: "sent back for refinement",
  },
  ask: {
    label: "Ask",
    key: "q",
    confirm: "",
    done: "asked",
  },
};

/**
 * The acts a row offers: the four rulings §8.7 names and the question. Duplicate needs
 * another record's id and reopen needs a reason for a decision already made, and neither is a
 * thing to do while skimming — they stay on the record, where both are in front of the reader.
 */
export const ROW_ACTS: readonly RuleAct[] = ["accept", "reject", "defer", "refine", "ask"];

/** The acts the record itself offers: everything, because the reader is on the object. */
export const POST_ACTS: readonly RuleAct[] = [
  "accept",
  "reject",
  "defer",
  "duplicate",
  "reopen",
  "refine",
  "ask",
];

/** The keys that press an act, for the surfaces that have a focused record. */
export const RULE_KEYS: Record<string, RuleAct> = Object.fromEntries(
  (Object.keys(ACTS) as RuleAct[]).filter((act) => ACTS[act].key !== "").map((act) => [ACTS[act].key, act]),
);

/** §4.8's three outcomes, each with the one sentence that says what recording it does next. */
export const ANSWER_OUTCOMES: ReadonlyArray<{
  readonly value: "answered" | "unknown" | "declined";
  readonly label: string;
  readonly note: string;
  readonly verb: string;
  readonly done: string;
}> = [
  {
    value: "answered",
    label: "Answer",
    note:
      "Kept verbatim and attributed to you, then read by the answer interpreter. What it proposes changes nothing until you accept the plan.",
    verb: "Record answer",
    done: "answered",
  },
  {
    value: "unknown",
    label: "I don't know",
    note:
      "Closes the question with nothing to interpret, and stops Babel asking it again until materially new evidence turns up.",
    verb: "Record that you don't know",
    done: "recorded that you don't know",
  },
  {
    value: "declined",
    label: "Stop asking",
    note:
      "Refuses the question. It stays on the record, visibly declined, and is suppressed until materially new evidence justifies asking again.",
    verb: "Decline the question",
    done: "declined",
  },
];

/**
 * The why, shortened to the half that distinguishes it. The store sends a standing and a
 * wait, and on the front page the first clause was "never ruled on" for almost every row —
 * fifteen rows opening with the same four words is a column of noise where the reason goes.
 */
function whyShort(why: string): string {
  const parts = why
    .split("·")
    .map((part) => part.trim())
    .filter((part) => part !== "" && part !== "never ruled on");
  if (parts.length < 2) return parts[0] ?? "";
  const last = parts[parts.length - 1] ?? "";
  const head = parts.slice(0, -1).join(" · ");
  const waited = /^waiting\s+(?<age>.+)$/u.exec(last);
  return waited === null ? `${head} · ${last}` : `${head} ${waited.groups?.["age"] ?? ""}`;
}

/** What a row recorded, until the next read carries the store's own answer. */
export interface Acted {
  readonly act: string;
  readonly done: string;
  readonly at: number;
}

export interface ActedHandler {
  (act: RuleAct | "answer", done: string, message: string): void;
}

// ---------------------------------------------------------------------------- the acts

export function RuleActs({
  host,
  id,
  acts,
  onActed,
  plain,
}: {
  host: HostServices;
  id: string;
  acts: readonly RuleAct[];
  onActed: ActedHandler;
  /** A row draws text actions; the record draws a segmented bar. */
  plain?: boolean;
}): ReactElement {
  const [open, setOpen] = useState<RuleAct | null>(null);
  return (
    <>
      <div className={plain ? "babel-acts-text" : "babel-acts-bar"} role="group" aria-label="Act on this record">
        {acts.map((act) => (
          <button
            type="button"
            key={act}
            data-ruling={act}
            className={open === act ? "active" : undefined}
            aria-expanded={open === act}
            title={act === "ask" ? "Ask Babel about this record" : ACTS[act].confirm}
            onClick={() => setOpen(open === act ? null : act)}
          >
            {ACTS[act].label}
          </button>
        ))}
      </div>
      {open === "ask" && (
        <AskBox
          host={host}
          id={id}
          onCancel={() => setOpen(null)}
          onAsked={(message) => {
            setOpen(null);
            onActed("ask", ACTS.ask.done, message);
          }}
        />
      )}
      {open !== null && open !== "ask" && (
        <ActConfirm
          host={host}
          id={id}
          act={open}
          onCancel={() => setOpen(null)}
          onDecided={(message) => {
            setOpen(null);
            onActed(open, ACTS[open].done, message);
          }}
        />
      )}
    </>
  );
}

/**
 * The confirmation, and the whole of it: one sentence saying what the act does, whatever that
 * act needs, and two buttons. A reopen requires a reason because the door refuses one without,
 * and asking here says why rather than letting the hub say no.
 */
function ActConfirm({
  host,
  id,
  act,
  onCancel,
  onDecided,
}: {
  host: HostServices;
  id: string;
  act: Ruling;
  onCancel: () => void;
  onDecided: (message: string) => void;
}): ReactElement {
  const [note, setNote] = useState("");
  const [duplicateOf, setDuplicateOf] = useState("");
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");
  const confirm = useRef<HTMLButtonElement | null>(null);

  // The confirmation takes the focus it asks for: a panel that appeared under the pointer
  // while the keyboard stayed on the row is a dialogue a keyboard reader cannot answer.
  useEffect(() => confirm.current?.focus(), []);

  async function submit(event: FormEvent): Promise<void> {
    event.preventDefault();
    setWorking(true);
    setFailure("");
    try {
      const result = await ask(host, ACTIONS.rule, {
        id,
        ruling: act,
        note: note.trim(),
        ...(act === "duplicate" ? { duplicateOf: duplicateOf.trim() } : {}),
      });
      const applied =
        result.plan === null
          ? ""
          : result.plan.applied
            ? ` The ${result.plan.kind} plan was applied.`
            : result.plan.declined
              ? ` The ${result.plan.kind} plan was declined.`
              : ` The ${result.plan.kind} plan was not applied: ${result.plan.error ?? "no reason given"}.`;
      onDecided(`Recorded ${act}. The record's standing is now ${result.standing}.${applied}`);
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  /*
    Every field on these surfaces answers `onInput`. For a text field that is the same moment
    React's `onChange` fires — the native `input` event, on every keystroke — and naming the
    event the browser actually sends is what lets the panel be driven the way a reader drives
    it, by the field's own event rather than by React's alias for it.
  */

  return (
    <form className="babel-confirm" onSubmit={(event) => void submit(event)}>
      <p>{ACTS[act].confirm}</p>
      {act === "duplicate" && (
        <label>
          The original record&apos;s id
          <input
            value={duplicateOf}
            onInput={(event) => setDuplicateOf(event.currentTarget.value)}
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
            rows={2}
            required={act === "reopen"}
            onInput={(event) => setNote(event.currentTarget.value)}
          />
        </label>
      )}
      <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
        <button type="submit" className="babel-primary" ref={confirm} disabled={working}>
          {working ? "Recording…" : `Confirm ${act}`}
        </button>
        <button type="button" onClick={onCancel} disabled={working}>
          Cancel
        </button>
      </Cluster>
      {failure !== "" && (
        <p className="babel-error" role="alert">
          {failure}
        </p>
      )}
    </form>
  );
}

/**
 * The question, in one line. It is not confirmed: it authorizes nothing and decides nothing —
 * it is the operator's own words, kept verbatim, with a marker a later review must answer.
 */
function AskBox({
  host,
  id,
  onCancel,
  onAsked,
}: {
  host: HostServices;
  id: string;
  onCancel: () => void;
  onAsked: (message: string) => void;
}): ReactElement {
  const [text, setText] = useState("");
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");
  const field = useRef<HTMLInputElement | null>(null);

  useEffect(() => field.current?.focus(), []);

  async function submit(event: FormEvent): Promise<void> {
    event.preventDefault();
    const written = text.trim();
    if (written === "") return;
    setWorking(true);
    setFailure("");
    try {
      await ask(host, ACTIONS.comment, { id, text: written, kind: "question" });
      onAsked("Your question is recorded. Babel's next review of this record must answer it.");
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  return (
    <form className="babel-confirm" onSubmit={(event) => void submit(event)}>
      <label>
        What do you want to know about this record?
        <input
          ref={field}
          value={text}
          onInput={(event) => setText(event.currentTarget.value)}
          placeholder="Kept verbatim. Babel's next review must answer it."
        />
      </label>
      <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
        <button type="submit" className="babel-primary" disabled={working || text.trim() === ""}>
          {working ? "Recording…" : "Ask"}
        </button>
        <button type="button" onClick={onCancel} disabled={working}>
          Cancel
        </button>
      </Cluster>
      {failure !== "" && (
        <p className="babel-error" role="alert">
          {failure}
        </p>
      )}
    </form>
  );
}

/** The three things an operator can do with a question, on the row where he reads it. */
export function RowAnswer({
  host,
  id,
  onActed,
}: {
  host: HostServices;
  id: string;
  onActed: ActedHandler;
}): ReactElement {
  const [outcome, setOutcome] = useState("");
  const [text, setText] = useState("");
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");
  const chosen = ANSWER_OUTCOMES.find((entry) => entry.value === outcome);

  async function record(): Promise<void> {
    if (chosen === undefined || working) return;
    if (chosen.value === "answered" && text.trim() === "") return;
    setWorking(true);
    setFailure("");
    try {
      await ask(host, ACTIONS.answer, { id, outcome: chosen.value, text: text.trim() });
      setOutcome("");
      setText("");
      onActed("answer", chosen.done, `Answer recorded. The question is ${chosen.done}.`);
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  return (
    <>
      <div className="babel-acts-text" role="group" aria-label="Answer this question">
        {ANSWER_OUTCOMES.map((entry) => (
          <button
            type="button"
            key={entry.value}
            data-answer={entry.value}
            className={entry.value === outcome ? "active" : undefined}
            aria-expanded={entry.value === outcome}
            title={entry.note}
            onClick={() => setOutcome(entry.value === outcome ? "" : entry.value)}
          >
            {entry.label}
          </button>
        ))}
      </div>
      {chosen !== undefined && (
        <form
          className="babel-confirm"
          onSubmit={(event) => {
            event.preventDefault();
            void record();
          }}
        >
          <p>{chosen.note}</p>
          <label>
            {chosen.value === "answered" ? "Your answer" : "Why, if you want to say (optional)"}
            <textarea
              value={text}
              rows={2}
              autoFocus
              placeholder="Kept verbatim and attributed to you. ⌘↵ records it."
              onInput={(event) => setText(event.currentTarget.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
                  event.preventDefault();
                  void record();
                }
                if (event.key === "Escape") {
                  event.preventDefault();
                  setOutcome("");
                }
              }}
            />
          </label>
          <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
            <button
              type="submit"
              className="babel-primary"
              disabled={working || (chosen.value === "answered" && text.trim() === "")}
            >
              {working ? "Recording…" : chosen.verb}
            </button>
            <button type="button" onClick={() => setOutcome("")} disabled={working}>
              Cancel
            </button>
          </Cluster>
          {failure !== "" && (
            <p className="babel-error" role="alert">
              {failure}
            </p>
          )}
        </form>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------- the row

export function FeedRow({
  host,
  post,
  focused,
  selected,
  acted,
  ticked,
  arrived,
  leaving,
  now,
  onFocus,
  onOpen,
  onActed,
  register,
}: {
  host: HostServices;
  post: FeedPost;
  focused: boolean;
  /** Whether the peek pane is showing this post right now. */
  selected: boolean;
  acted: Acted | undefined;
  ticked: boolean;
  arrived: boolean;
  leaving: boolean;
  now: number;
  onFocus: () => void;
  onOpen: () => void;
  onActed: ActedHandler;
  register: (element: HTMLLIElement | null) => void;
}): ReactElement {
  const [first, second, ...rest] = post.topics;
  const why = post.awaiting ? whyShort(post.why) : "";
  const age = since(post.createdAt, now);
  return (
    <li
      className="babel-row"
      data-post={post.id}
      data-kind={post.kind}
      data-focused={focused ? "" : undefined}
      data-selected={selected ? "" : undefined}
      data-awaiting={post.awaiting ? "" : undefined}
      data-arrived={arrived ? "" : undefined}
      data-leaving={leaving ? "" : undefined}
      tabIndex={-1}
      ref={register}
      onFocus={onFocus}
      aria-label={`${KIND_LABELS[post.kind]}: ${post.title}`}
    >
      <Votes post={post} ticked={ticked} />
      <Stack className="babel-row-body" gap="var(--babel-space-1)">
        <button type="button" className="babel-claim" data-open={post.id} onClick={onOpen}>
          {post.title === "" ? "a record with no title recorded" : post.title}
        </button>
        <Cluster className="babel-facts" gap="var(--babel-space-2)" align="baseline">
          <span className="babel-kind" data-tone={KIND_TONES[post.kind]}>
            {KIND_LABELS[post.kind]}
          </span>
          {first !== undefined && (
            <button type="button" className="babel-topic" onClick={() => look({ topic: first.id })}>
              t/{first.name}
            </button>
          )}
          {second !== undefined && (
            <button type="button" className="babel-topic" onClick={() => look({ topic: second.id })}>
              t/{second.name}
            </button>
          )}
          {rest.length > 0 && (
            <span className="babel-topic" title={rest.map((topic) => `t/${topic.name}`).join(" · ")}>
              +{rest.length}
            </span>
          )}
          {age !== "" && (
            <time className="babel-age" dateTime={post.createdAt}>
              {age}
            </time>
          )}
          {post.comments > 0 && (
            <span className="babel-comments">
              {post.comments} {post.comments === 1 ? "comment" : "comments"}
            </span>
          )}
          {why !== "" && <span className="babel-why">{why}</span>}
          {post.reviewing && (
            <span
              className="babel-reviewing"
              title="a reviewer is reading this now"
              aria-label="a reviewer is reading this now"
            />
          )}
        </Cluster>
        {acted !== undefined && (
          <span className="babel-acted" data-act={acted.act}>
            {acted.done}
          </span>
        )}
        {acted === undefined && post.awaiting && post.kind !== "question" && (
          <div className="babel-acts">
            <RuleActs host={host} id={post.id} acts={ROW_ACTS} onActed={onActed} plain />
          </div>
        )}
        {acted === undefined && post.awaiting && post.kind === "question" && (
          <div className="babel-acts">
            <RowAnswer host={host} id={post.id} onActed={onActed} />
          </div>
        )}
      </Stack>
    </li>
  );
}
