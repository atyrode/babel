import { useEffect, useState, type FormEvent, type ReactElement } from "react";
import type { HostServices, PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import { Cluster, Disclosure, ScrollRegion, Stack } from "@manifold/ui";
import { ACTIONS, FEED_PLUGIN_ID, INTEREST_STATES } from "../contract.ts";
import { BABEL_NODE, ask, refusal, since, useSelection, type FeedQuery, type TopicResult, type TopicRow } from "./api.ts";
import { EMPTY_QUERY, FeedListing } from "./home.tsx";
import { INTEREST_LABEL, INTEREST_MEANS, RAIL_POLL_MS } from "./rail.tsx";

/*
  ONE TOPIC: what it is, where the operator stands toward it, and the feed narrowed to it
  (§4.13).

  The panel is the feed with a header, not a second listing — same sorts, same rows, same
  pagination, narrowed by the same query. Two things live here and nowhere else:

    - THE STANCE. The operator's interest is an attributed fact about HIM, offered on the
      topic's own surface: the one act on a topic that is still directly his.
    - THE ASKS. Retiring, splitting and merging go THROUGH Babel: the form records what he
      wants in his own words, Babel's next filing run answers with a proposal, and he rules on
      that proposal like any other. There is no button here that rewrites the ledger, because
      a topic change is a judgement Babel has to make its case for.

  Nothing prints a path: a binding's identity can be the directory every worktree shares, and
  a locator is evidence ABOUT a topic and never the topic.
*/

const ASKS: ReadonlyArray<{ readonly value: string; readonly label: string; readonly asks: string }> = [
  { value: "retire", label: "Retire this topic", asks: "retire this" },
  { value: "split", label: "Split this topic", asks: "split this" },
  { value: "merge", label: "Merge this topic into…", asks: "merge this" },
];

export function TopicPanel({ host }: PanelProps): ReactElement {
  const selection = useSelection();
  return (
    <ScrollRegion className={`plugin-${FEED_PLUGIN_ID.replaceAll(".", "_")}`} aria-label="Topic">
      <Stack className="babel-panel" gap="var(--babel-space-4)">
        {selection.topic === "" ? (
          <div className="babel-state">
            <strong>No topic open</strong>
            <span>Press a topic in Home&apos;s rail, or on a row, and it is read here.</span>
          </div>
        ) : (
          <TopicView host={host} topic={selection.topic} />
        )}
      </Stack>
    </ScrollRegion>
  );
}

function TopicView({ host, topic }: { host: HostServices; topic: string }): ReactElement {
  const [query, setQuery] = useState<FeedQuery>({ ...EMPTY_QUERY, needs: "all", sort: "new", topic });
  const [failure, setFailure] = useState("");
  const [now, setNow] = useState(() => Date.now());

  const read = usePolledResource<TopicResult | null>(async () => ask(host, ACTIONS.topic, { topic }), RAIL_POLL_MS, {
    key: "atyrode.babel.topic",
    restartKey: topic,
    initial: null,
    topics: [BABEL_NODE],
    events: host.client,
    onError: (reason) => setFailure(refusal(reason)),
    onSuccess: () => setFailure(""),
  });

  // The list follows the rail: pointing the panels at another topic re-narrows the feed.
  useEffect(() => {
    setQuery((current) => ({ ...current, topic, offset: 0 }));
    setNow(Date.now());
  }, [topic]);

  const row = read.value?.topic ?? null;
  const proposed = read.value?.proposed ?? [];

  return (
    <FeedListing
      host={host}
      query={query}
      onQuery={setQuery}
      heading={
        <header className="babel-topic-header">
          <Stack gap="var(--babel-space-2)">
            <p className="babel-eyebrow">Topic</p>
            <h1 className="babel-topic-name">t/{row?.name ?? topic}</h1>
            {row !== null && (
              <Cluster className="babel-facts" gap="var(--babel-space-2)" align="baseline">
                <span className="babel-kind" data-tone="info">
                  {row.kind}
                </span>
                <span className="babel-note">
                  {row.posts.toLocaleString()} {row.posts === 1 ? "post" : "posts"}
                  {row.awaiting > 0 && ` · ${row.awaiting.toLocaleString()} awaiting you`}
                </span>
                <Binding topic={row} />
              </Cluster>
            )}
            {failure !== "" && <p className="babel-note">{failure}</p>}
            {read.value !== null && row === null && (
              <p className="babel-note">
                No topic in this hub answers to that name. What follows is the feed narrowed to it, which is why it
                is empty: only you create a topic, and Babel proposes the identity.
              </p>
            )}
            {row !== null && row.posts === 0 && (
              <p className="babel-note">
                Nothing is filed under this topic yet. It exists, and no record has been said to be about it.
              </p>
            )}
            {row !== null && (
              <Interest host={host} topic={row} now={now} onStated={() => read.refresh()} />
            )}
            {proposed.length > 0 && (
              <ul className="babel-topic-list">
                {proposed.map((proposal) => (
                  <li className="babel-topic-why" key={proposal.proposalId}>
                    Babel proposes to {proposal.operation} this · {proposal.why}
                  </li>
                ))}
              </ul>
            )}
            {row !== null && <AskBabel host={host} topic={row} />}
          </Stack>
        </header>
      }
    />
  );
}

/** What the topic is bound to, in one muted line: the remote as a name, checkouts as a count. */
function Binding({ topic }: { topic: TopicRow }): ReactElement {
  if (topic.binding === null) return <span className="babel-note">no binding recorded</span>;
  const paths = topic.binding.paths;
  return (
    <span className="babel-note" title={paths.length === 0 ? undefined : `${paths.length} checkouts seen`}>
      {topic.binding.remote === "" ? topic.binding.kind : topic.binding.remote}
      {paths.length > 0 && ` · ${paths.length.toLocaleString()} checkouts`}
    </span>
  );
}

/**
 * The stance, as §4.13 spells it: four words, the current one pressed, and a reason kept
 * verbatim. Nobody having said anything is a DIFFERENT answer from any of the four and is
 * shown as one. The reason is optional for all four: an operator who says "not now" has said
 * something attributable, and refusing the act for want of prose loses a lawful stance.
 */
function Interest({
  host,
  topic,
  now,
  onStated,
}: {
  host: HostServices;
  topic: TopicRow;
  now: number;
  onStated: () => void;
}): ReactElement {
  const [chosen, setChosen] = useState("");
  const [reason, setReason] = useState("");
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");
  const current = topic.interest.state;

  async function submit(event: FormEvent): Promise<void> {
    event.preventDefault();
    if (chosen === "") return;
    setWorking(true);
    setFailure("");
    try {
      await ask(host, ACTIONS.interest, { entityId: topic.id, state: chosen, reason: reason.trim() });
      setChosen("");
      setReason("");
      onStated();
    } catch (failed) {
      setFailure(refusal(failed));
    } finally {
      setWorking(false);
    }
  }

  return (
    <div className="babel-interest">
      <div className="babel-acts-bar" role="group" aria-label="Interest">
        {INTEREST_STATES.map((state) => (
          <button
            type="button"
            key={state}
            data-interest={state}
            aria-pressed={chosen === "" ? current === state : chosen === state}
            title={INTEREST_MEANS[state]}
            onClick={() => {
              setChosen(state === chosen ? "" : state);
              setReason("");
              setFailure("");
            }}
          >
            {INTEREST_LABEL[state]}
          </button>
        ))}
      </div>
      <span className="babel-note">Your interest · Babel spends where you say</span>
      {chosen !== "" && (
        <form className="babel-confirm" onSubmit={(event) => void submit(event)}>
          <label>
            Why {(INTEREST_LABEL[chosen] ?? chosen).toLowerCase()}? Kept verbatim, and optional.
            <input value={reason} onInput={(event) => setReason(event.currentTarget.value)} autoFocus />
          </label>
          <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
            <button type="submit" className="babel-primary" disabled={working}>
              {working ? "Recording…" : `Record ${(INTEREST_LABEL[chosen] ?? chosen).toLowerCase()}`}
            </button>
            <button type="button" onClick={() => setChosen("")} disabled={working}>
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
      {current === "" ? (
        <p className="babel-note">You have not said where you stand on this.</p>
      ) : (
        <p className="babel-stance">
          {INTEREST_LABEL[current] ?? current}
          {topic.interest.reason !== "" && <span className="babel-quote"> {topic.interest.reason}</span>}
          {topic.interest.by !== "" && ` · ${topic.interest.by}`}
          {topic.interest.at !== "" && ` · ${since(topic.interest.at, now)}`}
        </p>
      )}
    </div>
  );
}

/** The three acts on a topic's identity, folded, and each of them an ask through `tell`. */
function AskBabel({ host, topic }: { host: HostServices; topic: TopicRow }): ReactElement {
  const [open, setOpen] = useState(false);
  const [act, setAct] = useState("retire");
  const [into, setInto] = useState("");
  const [reason, setReason] = useState("");
  const [working, setWorking] = useState(false);
  const [said, setSaid] = useState("");
  const [failure, setFailure] = useState("");
  const chosen = ASKS.find((entry) => entry.value === act);

  async function submit(event: FormEvent): Promise<void> {
    event.preventDefault();
    if (chosen === undefined) return;
    const words = [
      `Please ${chosen.asks} topic t/${topic.name}`,
      act === "merge" && into.trim() !== "" ? ` into t/${into.trim()}` : "",
      reason.trim() === "" ? "." : `: ${reason.trim()}`,
    ].join("");
    setWorking(true);
    setFailure("");
    try {
      await ask(host, ACTIONS.tell, { text: words, target: { kind: "entity", id: topic.id } });
      setSaid("Recorded. Babel's next filing run answers with a proposal you rule on like any other.");
      setInto("");
      setReason("");
    } catch (failed) {
      setFailure(refusal(failed));
    } finally {
      setWorking(false);
    }
  }

  return (
    <Disclosure className="babel-asks" open={open} onOpenChange={setOpen} header="Ask Babel to change this topic">
      <form className="babel-confirm" onSubmit={(event) => void submit(event)}>
        <div className="babel-acts-bar" role="group" aria-label="Ask">
          {ASKS.map((entry) => (
            <button
              type="button"
              key={entry.value}
              data-ask={entry.value}
              aria-pressed={act === entry.value}
              onClick={() => setAct(entry.value)}
            >
              {entry.label}
            </button>
          ))}
        </div>
        {act === "merge" && (
          <label>
            Into which topic
            <input value={into} onInput={(event) => setInto(event.currentTarget.value)} />
          </label>
        )}
        <label>
          Why, in your own words
          <textarea value={reason} rows={2} onInput={(event) => setReason(event.currentTarget.value)} />
        </label>
        <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
          <button type="submit" className="babel-primary" disabled={working}>
            {working ? "Recording…" : "Tell Babel"}
          </button>
        </Cluster>
        {said !== "" && <p className="babel-note">{said}</p>}
        {failure !== "" && (
          <p className="babel-error" role="alert">
            {failure}
          </p>
        )}
      </form>
    </Disclosure>
  );
}
