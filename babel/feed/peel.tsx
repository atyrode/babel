import { useState, type ReactElement, type ReactNode } from "react";
import type { HostServices } from "@manifold/plugin";
import { Chip, Cluster, Disclosure, Stack } from "@manifold/ui";
import { ACTIONS, FeedPostSchema } from "../contract.ts";
import { ask, refusal, since, type RecordPeel } from "./api.ts";
import {
  KIND_LABELS,
  KIND_TONES,
  POST_ACTS,
  RowAnswer,
  RuleActs,
  type ActedHandler,
} from "./rows.tsx";
import { Votes } from "./votes.tsx";

import { JevPosition } from "./jev.tsx";
/*
  ONE RECORD, PEELED (§8.6).

  "The complexity of the data is for Babel itself, the user really only needs the surface, and
  to be able to dig when needed." Five depths and the reader chooses one, never a page: the
  claim and the acts it invites, the case in prose, the evidence in the words of whoever said
  it, the reception, and the machinery — every id and digest the object carries.

  Three rules shape the code rather than the layout:

    - NOTHING FETCHES ON OPEN. The whole peel arrives in one door call, so opening depth 4 is
      a disclosure and never a request; a reader who digs waits for nothing.
    - AN ABSENT SECTION IS ABSENT. Not an empty heading, not a nought: a proposal that names
      no risk is not a proposal whose risks are none.
    - IDS LIVE AT DEPTH 5, with one exception — the related strip links to other records, and
      a link needs an identity. It shows the other record's own words and keeps the id out.

  The excerpt outranks the note: what a person said is the record's evidence, and what a model
  said about it is a gloss, set smaller, beneath.
*/

/** Depths 1 and 2 are open because they are what the reader came for; digging is a choice. */
const INITIAL: readonly boolean[] = [true, true, false, false, false];

/** A field name from the store, read as the question it answers. */
function label(key: string): string {
  const words = key.replaceAll("_", " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const COUNTED = ["no", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"];

/**
 * WHAT A CITATION'S VERDICT SAYS TO A READER (#348), in the words a person acts on rather than
 * the vocabulary the record stores. `unquoted` has no entry: a citation that quoted nothing is
 * rendered as a citation without a pull-quote, and telling a reader what was not checked about
 * a quote that does not exist is noise.
 */
const VERDICTS: Record<string, string> = {
  verified: "found at this line",
  moved: "found elsewhere in this session",
  absent: "not found in this session",
  unchecked: "not checked",
};

/** A count as a word up to nine, because a sentence reads and a numeral tallies. */
function counted(value: number): string {
  return COUNTED[value] ?? String(value);
}

/**
 * WHAT A RECORD RESTS ON, AS A SENTENCE.
 *
 * "three observations, from one run" and "three observations, from three runs" are the same
 * count and different evidence, and the surface said only the count. The second clause is
 * always present when there is anything to say: a reader who has to notice an absence to learn
 * that three supports came from one reading is a reader the page misled.
 */
function corroboration(of: { supports: number; distinctRuns: number }): string {
  const supports = `${counted(of.supports)} support${of.supports === 1 ? "" : "s"}`;
  const runs = `${counted(of.distinctRuns)} run${of.distinctRuns === 1 ? "" : "s"}`;
  return `${supports}, from ${runs}`;
}

/**
 * WHICH CODEBASE, AND ON WHOSE WORD, AS A SENTENCE.
 *
 * 42.7% of this deployment's records cannot say which codebase they concern, and the ones that
 * can were saying it nowhere. The two provenances read as different sentences on purpose: a
 * repository Babel probed in the cited session's own workspace is something this deployment
 * saw, and a repository a transcript mentioned is something a conversation said. One phrasing
 * for both would let the weaker claim borrow the stronger one's authority.
 *
 * The name only. The commit and the issue link are identifiers and live at depth five (§8.6),
 * where a reader debugging Babel is; a reader deciding whether this is worth his time needs
 * the project, not forty characters of it.
 */
function repository(of: RecordPeel["repository"][number]): string {
  return of.provenance === "observed"
    ? `observed in ${of.remote}`
    : `named in the evidence, not observed: ${of.remote}`;
}

type ProposedAction = RecordPeel["nextActions"][number];

/**
 * THE VOCABULARY AS WHAT IT ASKS FOR. The stored words are wire values a run writes; a reader
 * deciding whether to accept one reads the request, not the enum.
 */
const NEXT_ACTION_ASKS: Record<ProposedAction["kind"], string> = {
  "draft-issue": "Draft an issue",
  "propose-reality-fact": "Propose a fact for the ledger",
  "store-memory": "Keep this as a memory",
  "ask-question": "Put a question to you",
  "develop-further": "Explore this further",
};

/**
 * ONE PROPOSED NEXT ACTION, AND THE OPERATOR'S ANSWER TO IT (#340).
 *
 * It sits at the claim depth beside the rulings, because it is the same kind of decision and
 * the reader is already there. Two presses and an optional note: every action is a proposal
 * until a person authorizes it, and there is no third answer that would half-authorize one.
 *
 * ACCEPTING ONE DOES NOTHING BUT RECORD THE ACCEPTANCE. Publishing, applying and writing to a
 * source repository are outside Babel, so the sentence under the buttons says so rather than
 * letting a press imply an issue was opened.
 *
 * The standing shown after a press is the one the door returned; the panel re-reads on the
 * same event, so this is what the row says for the moment between the two.
 */
function ProposedNextAction({
  host,
  action,
  onActed,
}: {
  host: HostServices;
  action: ProposedAction;
  onActed: ActedHandler;
}): ReactElement {
  const [standing, setStanding] = useState(action.standing);
  const [note, setNote] = useState("");
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");

  async function answer(decision: "accepted" | "declined"): Promise<void> {
    setWorking(true);
    setFailure("");
    try {
      const result = await ask(host, ACTIONS.decide, {
        nextActionId: action.id,
        decision,
        note: note.trim(),
      });
      setStanding(result.standing);
      setNote("");
      onActed("decide", decision, `${NEXT_ACTION_ASKS[action.kind]} — ${decision}.`);
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  return (
    <section className="babel-next-action" data-standing={standing}>
      <p className="babel-next-action-ask">
        <span className="babel-next-action-kind">{NEXT_ACTION_ASKS[action.kind]}</span>{" "}
        {action.summary}
      </p>
      {action.rationale !== "" && <p className="babel-note">{action.rationale}</p>}
      {action.history.length > 0 && (
        <ul className="babel-history">
          {action.history.map((entry) => (
            <li key={`${entry.at}-${entry.decision}`}>
              <span className="babel-history-stance">{entry.decision}</span>
              {entry.note !== "" && <span className="babel-quote"> {entry.note}</span>}
            </li>
          ))}
        </ul>
      )}
      <div className="babel-acts-text" role="group" aria-label="Answer this proposed action">
        <button
          type="button"
          data-decision="accepted"
          disabled={working || standing === "accepted"}
          onClick={() => void answer("accepted")}
        >
          Accept
        </button>
        <button
          type="button"
          data-decision="declined"
          disabled={working || standing === "declined"}
          onClick={() => void answer("declined")}
        >
          Decline
        </button>
        <input
          type="text"
          value={note}
          aria-label="Why, in your own words"
          placeholder="why, if it matters"
          onInput={(event) => setNote(event.currentTarget.value)}
        />
      </div>
      <p className="babel-note">
        Accepting records that you accepted it. Babel publishes nothing and opens nothing.
      </p>
      {failure !== "" && (
        <p className="babel-refusal" role="alert">
          {failure}
        </p>
      )}
    </section>
  );
}

function Depth({
  index,
  title,
  count,
  open,
  onToggle,
  children,
}: {
  index: number;
  title: string;
  count?: number;
  open: readonly boolean[];
  onToggle: (index: number) => void;
  children: ReactNode;
}): ReactElement {
  return (
    <Disclosure
      className="babel-peel"
      data-depth={index + 1}
      open={open[index] === true}
      onOpenChange={() => onToggle(index)}
      header={
        <>
          <span className="babel-peel-title">{title}</span>
          {/* A nought is not a count. The evidence depth opens for a repository alone when a
              hypothesis has no excerpt of its own, and "The evidence 0" reads as a claim about
              the record rather than as a section with nothing tallied in it. */}
          {count !== undefined && count > 0 && (
            <span className="babel-peel-count">{count.toLocaleString()}</span>
          )}
        </>
      }
    >
      {children}
    </Disclosure>
  );
}

export function Peel({
  host,
  peel,
  onActed,
  now,
}: {
  host: HostServices;
  peel: RecordPeel;
  onActed: ActedHandler;
  now: number;
}): ReactElement {
  const [open, setOpen] = useState<readonly boolean[]>(INITIAL);
  const toggle = (index: number): void =>
    setOpen((current) => current.map((value, at) => (at === index ? !value : value)));
  const post = peel.post;
  // An observation is a record the peel can open and the feed never lists. Every other top row
  // is still the exact FeedPost the votes strip reads; parsing after the discriminant keeps the
  // wider record-door contract out of the feed component.
  const feedPost = post.kind === "observation" ? null : FeedPostSchema.parse(post);
  const fields = Object.entries(peel.case);
  const machinery = Object.entries(peel.machinery);
  const age = since(post.createdAt, now);

  return (
    <Stack className="babel-record" gap="var(--babel-space-4)">
      <header className="babel-record-head">
        <Cluster gap="var(--babel-space-3)" align="start">
          {feedPost !== null && <Votes post={feedPost} ticked={false} />}
          <Stack gap="var(--babel-space-2)">
            <h1 className="babel-record-claim">
              {post.title === "" ? peel.claim.statement : post.title}
            </h1>
            <Cluster className="babel-facts" gap="var(--babel-space-2)" align="baseline">
              <span
                className="babel-kind"
                data-tone={post.kind === "observation" ? "quiet" : KIND_TONES[post.kind]}
              >
                {post.kind === "observation" ? "Observation" : KIND_LABELS[post.kind]}
              </span>
              <Chip className="babel-standing" data-standing={peel.claim.standing}>
                {peel.claim.standing}
              </Chip>
              {post.topics.map((topic) => (
                <span className="babel-topic" key={topic.id}>
                  t/{topic.name}
                </span>
              ))}
              {age !== "" && (
                <time className="babel-age" dateTime={post.createdAt}>
                  {age}
                </time>
              )}
              {post.author !== null && <span className="babel-note">by {post.author.runId}</span>}
            </Cluster>
          </Stack>
        </Cluster>
      </header>

      <Depth index={0} title="The claim" open={open} onToggle={toggle}>
        <Stack gap="var(--babel-space-3)">
          <p className="babel-quote">{peel.claim.statement}</p>
          {peel.claim.act !== "" && <p className="babel-note">{peel.claim.act}</p>}
          {peel.plan !== null && (
            <p className="babel-plan">
              Carries a {peel.plan.kind} plan · {peel.plan.operation} · {peel.plan.state}
            </p>
          )}
          {post.kind === "question" ? (
            <RowAnswer host={host} id={post.id} onActed={onActed} />
          ) : post.kind === "observation" ? null : (
            <RuleActs host={host} id={post.id} acts={POST_ACTS} onActed={onActed} />
          )}
          {/* WHAT A RUN PROPOSED BE DONE, under the ruling and never above it: the record's
              standing is the question the reader came with, and what to do about it is the
              one that follows. An empty list renders as nothing at all. */}
          {peel.nextActions.map((action) => (
            <ProposedNextAction key={action.id} host={host} action={action} onActed={onActed} />
          ))}
        </Stack>
      </Depth>

      {fields.length > 0 && (
        <Depth index={1} title="The case" open={open} onToggle={toggle}>
          <Stack gap="var(--babel-space-3)">
            {fields.map(([key, value]) => (
              <section className="babel-field" key={key}>
                <p className="babel-field-label">{label(key)}</p>
                {typeof value === "string" ? (
                  <p className="babel-quote">{value}</p>
                ) : (
                  <ul className="babel-field-list">
                    {value.map((item, index) => (
                      <li className="babel-quote" key={`${key}-${String(index)}`}>
                        {item}
                      </li>
                    ))}
                  </ul>
                )}
              </section>
            ))}
          </Stack>
        </Depth>
      )}

      {(peel.evidence.length > 0 || peel.repository.length > 0) && (
        <Depth
          index={2}
          title="The evidence"
          count={peel.evidence.length}
          open={open}
          onToggle={toggle}
        >
          {/* WHAT IT RESTS ON, IN WORDS, before the excerpts. Prose rather than a badge: a
              badge is rationed to standing and kind, and this is the sentence that decides
              whether the excerpts below are three readings or one restated. */}
          {peel.corroboration.supports > 0 && (
            <p className="babel-corroboration">{corroboration(peel.corroboration)}</p>
          )}
          {/* AND WHICH CODEBASE, which a hypothesis has no excerpt of its own to carry: its
              observations hold the citations, so the depth opens for the repository alone and
              counts nothing, because a count of no excerpts is a nought rather than a fact. */}
          {peel.repository.map((of) => (
            <p className="babel-repository" key={of.remote} data-provenance={of.provenance}>
              {repository(of)}
            </p>
          ))}
          <Stack gap="var(--babel-space-4)">
            {peel.evidence.map((item, index) => (
              <figure
                className="babel-evidence"
                data-verification={item.verification === "" ? undefined : item.verification}
                key={`${item.excerpt.slice(0, 24)}-${String(index)}`}
              >
                {/* A CITATION THAT QUOTED NOTHING SHOWS NO PULL-QUOTE. An empty one reads as a
                    person who said nothing, and most of the imported corpus quoted nothing. */}
                {item.excerpt !== "" && (
                  <blockquote className="babel-quote">{item.excerpt}</blockquote>
                )}
                <figcaption className="babel-note">
                  {item.speaker}
                  {item.session !== null && (
                    <>
                      {" · "}
                      <a href={item.session.href}>{item.session.title || item.session.selector}</a>
                    </>
                  )}
                  {item.line !== null && <> · line {item.line}</>}
                  {/* WHAT BABEL FOUND WHEN IT LOOKED (#348). It is beside the line because it
                      is a fact about the locator, and it is words rather than a badge: badges
                      are rationed to standing and kind, and "not found in this session" is a
                      sentence a reader has to actually read. */}
                  {VERDICTS[item.verification] !== undefined && (
                    <> · {VERDICTS[item.verification]}</>
                  )}
                </figcaption>
                {item.note !== "" && <p className="babel-evidence-note">{item.note}</p>}
              </figure>
            ))}
          </Stack>
        </Depth>
      )}

      {/* A strip rather than a depth: a reader glances at it while deciding, and each
          relation changes the decision differently. */}
      {peel.related.length > 0 && (
        <Cluster className="babel-related" gap="var(--babel-space-2)">
          {peel.related.map((related) => (
            <span className="babel-related-row" key={`${related.relation}-${related.id}`}>
              <span className="babel-related-word">{related.relation}</span> {related.title}
            </span>
          ))}
        </Cluster>
      )}

      <Depth index={3} title="The reception" open={open} onToggle={toggle}>
        <Stack gap="var(--babel-space-3)">
          <JevPosition host={host} id={post.id} detail />
          {peel.reception.contested && (
            <p className="babel-contested-note">Babel&apos;s reviewers are split on this.</p>
          )}
          {peel.reception.byRole.length === 0 ? (
            <p className="babel-note">No reviewer has assessed this revision.</p>
          ) : (
            <table className="babel-roles">
              <thead>
                <tr>
                  <th scope="col">What the reviewer was asked</th>
                  <th scope="col">support</th>
                  <th scope="col">oppose</th>
                  <th scope="col">unsure</th>
                </tr>
              </thead>
              <tbody>
                {peel.reception.byRole.map((row) => (
                  <tr key={row.role} data-role={row.role}>
                    <th scope="row">
                      {row.role}
                      {row.opposingRationales.length > 0 && (
                        <ul className="babel-rationales">
                          {row.opposingRationales.map((why, index) => (
                            <li className="babel-quote" key={`${row.role}-${String(index)}`}>
                              {why}
                            </li>
                          ))}
                        </ul>
                      )}
                    </th>
                    <td>{row.support}</td>
                    <td>{row.oppose}</td>
                    <td>{row.unsure}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {peel.reception.operatorHistory.length > 0 && (
            <ul className="babel-history">
              {peel.reception.operatorHistory.map((entry, index) => (
                <li key={`${entry.at}-${String(index)}`}>
                  <span className="babel-history-stance">{entry.stance}</span>
                  {entry.reason !== "" && <span className="babel-quote"> {entry.reason}</span>}
                  <time className="babel-age" dateTime={entry.at}>
                    {" "}
                    {since(entry.at, now)}
                  </time>
                </li>
              ))}
            </ul>
          )}
        </Stack>
      </Depth>

      {machinery.length > 0 && (
        <Depth index={4} title="The machinery" open={open} onToggle={toggle}>
          <dl className="babel-rows">
            {machinery.map(([key, value]) => (
              <div key={key}>
                <dt>{label(key)}</dt>
                <dd className="babel-mono">{value}</dd>
              </div>
            ))}
          </dl>
        </Depth>
      )}
    </Stack>
  );
}
