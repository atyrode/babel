import { useState, type FormEvent, type ReactElement } from "react";
import type { HostServices } from "@manifold/plugin";
import { Cluster, Stack } from "@manifold/ui";
import { ACTIONS } from "../contract.ts";
import { ask, refusal, since, type ThreadAct, type ThreadComment, type ThreadResult } from "./api.ts";

/*
  THE CONVERSATION UNDER THE POST (§8.7).

  A reviewer's contribution prose, a refinement, the operator's reason in his own words, the
  answer to a question and the reason on a reconsideration are all comments, threaded by what
  they relate to and shown newest-first; the store each line was written into is Babel's
  business. Replies nest EXACTLY ONE LEVEL — a thread that indents every answer walks off the
  right edge after four of them, and this is a discussion under a claim, not a tree.

  RULINGS ARE IN THE LIST AND ARE NOT COMMENTS. Accept, reject, defer, duplicate and reopen
  are append-only authority, so they render as attributed acts in their own chronological
  place, with their own shape: a decision that looked like an opinion would be the one
  confusion this thread cannot afford.

  The box records a reason and no polarity, and its toggle records a question instead: nothing
  the operator writes moves the score, because the score is Babel's reviewers'.
*/

/** How much conversation one read shows before the rest is one press away. */
const COMMENT_PAGE = 5;

/** The word a comment needs beside its author, and only when it disambiguates. */
const COMMENT_WORDS: Record<string, string> = {
  comment: "",
  question: "asked",
  contribution: "",
  refinement: "refined",
  answer: "answered",
  reconsideration: "reconsidered",
};

/** The rulings in the past tense: the thread shows what was done, not what is offered. */
const ACT_WORDS: Record<string, string> = {
  accept: "accepted this",
  reject: "rejected this",
  defer: "deferred this",
  duplicate: "marked this a duplicate",
  reopen: "reopened this",
  refine: "sent this back for refinement",
};

type Entry =
  | { readonly kind: "comment"; readonly at: number; readonly comment: ThreadComment }
  | { readonly kind: "act"; readonly at: number; readonly act: ThreadAct };

/** A time this build cannot read sorts last rather than dropping the line somebody wrote. */
function stamp(value: string): number {
  const at = Date.parse(value);
  return Number.isNaN(at) ? 0 : at;
}

/** A comment's whole subtree, flattened into the one level of indentation the thread has. */
function descendants(comment: ThreadComment): ThreadComment[] {
  const flat: ThreadComment[] = [];
  const walk = (rows: readonly ThreadComment[]): void => {
    for (const row of rows) {
      flat.push(row);
      walk(row.replies);
    }
  };
  walk(comment.replies);
  return flat.sort((left, right) => stamp(right.at) - stamp(left.at));
}

function Line({ comment, now }: { comment: ThreadComment; now: number }): ReactElement {
  const word = COMMENT_WORDS[comment.kind] ?? comment.kind;
  return (
    <div className="babel-comment-line" data-kind={comment.kind}>
      <p className="babel-comment-who">
        <span className="babel-comment-author">
          {comment.author.kind === "operator" ? "you" : comment.author.id}
        </span>
        {comment.role !== "" && <span className="babel-comment-role"> · {comment.role}</span>}
        {word !== "" && <span className="babel-comment-word"> · {word}</span>}
        <time className="babel-age" dateTime={comment.at}>
          {" "}
          · {since(comment.at, now)}
        </time>
      </p>
      <p className="babel-quote">{comment.text}</p>
    </div>
  );
}

export function Thread({
  host,
  id,
  thread,
  onPosted,
  now,
}: {
  host: HostServices;
  id: string;
  thread: ThreadResult | null;
  onPosted: () => void;
  now: number;
}): ReactElement {
  const [text, setText] = useState("");
  const [asking, setAsking] = useState(false);
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");
  const [all, setAll] = useState(false);

  async function submit(event: FormEvent): Promise<void> {
    event.preventDefault();
    const written = text.trim();
    if (written === "") return;
    setWorking(true);
    setFailure("");
    try {
      await ask(host, ACTIONS.comment, { id, text: written, kind: asking ? "question" : "comment" });
      setText("");
      onPosted();
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  const entries: Entry[] = [
    ...(thread?.comments ?? []).map((comment): Entry => ({ kind: "comment", at: stamp(comment.at), comment })),
    ...(thread?.acts ?? []).map((act): Entry => ({ kind: "act", at: stamp(act.at), act })),
  ].sort((left, right) => right.at - left.at);
  const shown = all ? entries : entries.slice(0, COMMENT_PAGE);

  return (
    <Stack className="babel-thread" gap="var(--babel-space-3)">
      <h2 className="babel-thread-head">
        The conversation
        {thread !== null && <span className="babel-peel-count">{thread.total.toLocaleString()}</span>}
      </h2>

      <form className="babel-comment-box" onSubmit={(event) => void submit(event)}>
        <label>
          {asking ? "What must Babel's next review of this record answer?" : "Your reason, kept verbatim"}
          <textarea
            value={text}
            rows={2}
            onInput={(event) => setText(event.currentTarget.value)}
            placeholder="Attributed to you. Nothing you write here moves the score."
          />
        </label>
        <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
          <button type="submit" className="babel-primary" disabled={working || text.trim() === ""}>
            {working ? "Recording…" : asking ? "Ask" : "Comment"}
          </button>
          <button type="button" aria-pressed={asking} onClick={() => setAsking(!asking)}>
            {asking ? "Write a comment instead" : "Ask Babel a question instead"}
          </button>
        </Cluster>
        {failure !== "" && (
          <p className="babel-error" role="alert">
            {failure}
          </p>
        )}
      </form>

      {thread === null ? (
        <p className="babel-note">Reading the conversation…</p>
      ) : entries.length === 0 ? (
        <p className="babel-note">Nothing has been said about this record yet.</p>
      ) : (
        <ol className="babel-comments">
          {shown.map((entry) =>
            entry.kind === "act" ? (
              <li className="babel-act-row" key={entry.act.id} data-act={entry.act.act}>
                <p>
                  <span className="babel-comment-author">{entry.act.by}</span>{" "}
                  {ACT_WORDS[entry.act.act] ?? entry.act.act}
                  <time className="babel-age" dateTime={entry.act.at}>
                    {" "}
                    · {since(entry.act.at, now)}
                  </time>
                </p>
                {entry.act.reason !== "" && <p className="babel-quote">{entry.act.reason}</p>}
              </li>
            ) : (
              <li className="babel-comment" key={entry.comment.id}>
                <Line comment={entry.comment} now={now} />
                {descendants(entry.comment).length > 0 && (
                  <ul className="babel-replies">
                    {descendants(entry.comment).map((reply) => (
                      <li key={reply.id}>
                        <Line comment={reply} now={now} />
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            ),
          )}
        </ol>
      )}
      {!all && entries.length > COMMENT_PAGE && (
        <button type="button" className="babel-link" onClick={() => setAll(true)}>
          Read the other {(entries.length - COMMENT_PAGE).toLocaleString()}
        </button>
      )}
    </Stack>
  );
}
