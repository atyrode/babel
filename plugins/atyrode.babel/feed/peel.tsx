import { useState, type ReactElement, type ReactNode } from "react";
import type { HostServices } from "@manifold/plugin";
import { Chip, Cluster, Disclosure, Stack } from "@manifold/ui";
import { since, type RecordPeel } from "./api.ts";
import { KIND_LABELS, KIND_TONES, POST_ACTS, RowAnswer, RuleActs, type ActedHandler } from "./rows.tsx";
import { Votes } from "./votes.tsx";

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
          {count !== undefined && <span className="babel-peel-count">{count.toLocaleString()}</span>}
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
  const fields = Object.entries(peel.case);
  const machinery = Object.entries(peel.machinery);
  const age = since(post.createdAt, now);

  return (
    <Stack className="babel-record" gap="var(--babel-space-4)">
      <header className="babel-record-head">
        <Cluster gap="var(--babel-space-3)" align="start">
          <Votes post={post} ticked={false} />
          <Stack gap="var(--babel-space-2)">
            <h1 className="babel-record-claim">{post.title === "" ? peel.claim.statement : post.title}</h1>
            <Cluster className="babel-facts" gap="var(--babel-space-2)" align="baseline">
              <span className="babel-kind" data-tone={KIND_TONES[post.kind]}>
                {KIND_LABELS[post.kind]}
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
          ) : (
            <RuleActs host={host} id={post.id} acts={POST_ACTS} onActed={onActed} />
          )}
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

      {peel.evidence.length > 0 && (
        <Depth index={2} title="The evidence" count={peel.evidence.length} open={open} onToggle={toggle}>
          <Stack gap="var(--babel-space-4)">
            {peel.evidence.map((item, index) => (
              <figure className="babel-evidence" key={`${item.excerpt.slice(0, 24)}-${String(index)}`}>
                <blockquote className="babel-quote">{item.excerpt}</blockquote>
                <figcaption className="babel-note">
                  {item.speaker}
                  {item.session !== null && (
                    <>
                      {" · "}
                      <a href={item.session.href}>{item.session.title || item.session.selector}</a>
                    </>
                  )}
                  {item.line !== null && <> · line {item.line}</>}
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
