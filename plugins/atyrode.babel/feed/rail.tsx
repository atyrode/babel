import { useState, type ReactElement } from "react";
import type { HostServices } from "@manifold/plugin";
import { Cluster, Disclosure, Stack } from "@manifold/ui";
import { usePolledResource } from "@manifold/plugin/hooks";
import { ACTIONS } from "../contract.ts";
import {
  BABEL_NODE,
  ask,
  look,
  refusal,
  type PulseResult,
  type TopicProposal,
  type TopicRow,
  type TopicsResult,
} from "./api.ts";

/*
  THE TOPICS, and the pulse over them.

  §4.13: a topic is a ledger entity something has been filed under, or one the operator has
  said where he stands on. An entity with neither is a subject Babel recognised in a session,
  and a rail listing those is a list of everything Babel has ever seen named — so they are not
  a destination here. Interest divides the list, and the two parked stances fold with their
  counts, because the topic, its filings and its history all survive a stance and a reader has
  to be able to find the thing he parked.

  What Babel proposes about a topic is ruled on here, in the rail, through the same `rule`
  door a record's own acts use: a topic change is an ordinary proposal through the chain.
*/

export const INTEREST_LABEL: Record<string, string> = {
  working: "Working on it",
  watching: "Keep an eye",
  "not-now": "Not now",
  excluded: "Excluded",
  "": "Nothing said",
};

export const INTEREST_MEANS: Record<string, string> = {
  working: "Babel files into it and analysis may spend on it.",
  watching: "Babel keeps filing into it and spends nothing there.",
  "not-now": "Parked: the review lane draws elsewhere. Nothing is deleted.",
  excluded: "Left out of analysis. Not interested is a signal, not a deletion.",
};

/** How many topics the rail carries before the rest are one press away. */
const RAIL_TOPICS = 12;

/** The two stances that fold: parked is not gone, and the rail says so with a count. */
const PARKED: readonly string[] = ["not-now", "excluded"];

/** The flat groups, in the order the rail reads them. */
const RAIL_GROUPS: ReadonlyArray<{ readonly state: string; readonly label: string }> = [
  { state: "working", label: "Working on it" },
  { state: "watching", label: "Keep an eye" },
  { state: "", label: "Nothing said" },
];

/** How often the rail and the pulse re-read when no event has arrived. */
export const RAIL_POLL_MS = 30_000;

function TopicLink({ topic, current }: { topic: TopicRow; current: string }): ReactElement {
  const facts = [`${topic.posts.toLocaleString()} posts`];
  if (topic.binding !== null) facts.push(`${topic.binding.kind}: ${topic.binding.identity}`);
  if (topic.interest.state !== "") facts.push(INTEREST_LABEL[topic.interest.state] ?? topic.interest.state);
  return (
    <li>
      <button
        type="button"
        data-topic={topic.id}
        aria-current={current === topic.id || current === topic.name ? "page" : undefined}
        title={facts.join(" · ")}
        onClick={() => look({ topic: topic.id })}
      >
        <span>t/{topic.name}</span>
        <span className="babel-topic-count">{topic.posts.toLocaleString()}</span>
      </button>
    </li>
  );
}

function Proposed({
  host,
  proposal,
  onRuled,
}: {
  host: HostServices;
  proposal: TopicProposal;
  onRuled: (said: string) => void;
}): ReactElement {
  const [working, setWorking] = useState(false);
  const [failure, setFailure] = useState("");

  async function rule(ruling: "accept" | "reject"): Promise<void> {
    setWorking(true);
    setFailure("");
    try {
      const result = await ask(host, ACTIONS.rule, { id: proposal.proposalId, ruling, note: "" });
      // The ruling and the ledger act are two facts and can part company; that is exactly
      // what the operator has to be told, because the proposal is gone and the topic is not.
      const ledger =
        result.plan === null || result.plan.applied
          ? ""
          : ` · the ruling stands, the ledger act did not: ${result.plan.error ?? "declined"}`;
      onRuled(`${ruling === "accept" ? "Accepted" : "Declined"}${ledger}`);
    } catch (reason) {
      setFailure(refusal(reason));
    } finally {
      setWorking(false);
    }
  }

  return (
    <li className="babel-topic-proposal">
      <p className="babel-topic-why">
        {proposal.operation} · {proposal.name} · {proposal.why}
      </p>
      <Cluster className="babel-topic-acts" gap="var(--babel-space-3)">
        <button type="button" data-proposal={proposal.proposalId} disabled={working} onClick={() => void rule("accept")}>
          Accept
        </button>
        <button type="button" disabled={working} onClick={() => void rule("reject")}>
          Decline
        </button>
      </Cluster>
      {failure !== "" && (
        <p className="babel-error" role="alert">
          {failure}
        </p>
      )}
    </li>
  );
}

export function TopicRail({
  host,
  current,
  onUnfiled,
}: {
  host: HostServices;
  /** The topic the panels are looking at, so the rail can mark its own row. */
  current: string;
  /** Narrowing the list to what is filed under nothing is a feed filter, not a topic. */
  onUnfiled: () => void;
}): ReactElement {
  const [failure, setFailure] = useState("");
  const [ruled, setRuled] = useState<Record<string, string>>({});
  const [unfolded, setUnfolded] = useState<Record<string, boolean>>({});
  const topics = usePolledResource<TopicsResult | null>(
    async () => ask(host, ACTIONS.topics, {}),
    RAIL_POLL_MS,
    {
      key: "atyrode.babel.topics",
      initial: null,
      topics: [BABEL_NODE],
      events: host.client,
      onError: (reason) => setFailure(refusal(reason)),
      onSuccess: () => setFailure(""),
    },
  );

  const answer = topics.value;
  if (answer === null) {
    return <p className="babel-note">{failure === "" ? "Reading the topics…" : failure}</p>;
  }

  const named = answer.topics.filter((topic) => topic.posts > 0 || topic.interest.state !== "");
  const flat = named.filter((topic) => !PARKED.includes(topic.interest.state));
  const shown = flat.slice(0, RAIL_TOPICS);
  const groups = RAIL_GROUPS.map((group) => ({
    label: group.label,
    rows: shown.filter((topic) => (PARKED.includes(topic.interest.state) ? "" : topic.interest.state) === group.state),
  })).filter((group) => group.rows.length > 0);
  const proposed = answer.proposed.filter((row) => !(row.proposalId in ruled));

  return (
    <Stack className="babel-rail" gap="var(--babel-space-3)">
      {failure !== "" && <p className="babel-note">{failure}</p>}
      {/* The stance labels divide the list, so they render only when there is something to
          divide: one group under one label is a heading over the whole list. */}
      {groups.map((group) => (
        <section className="babel-topic-group" key={group.label}>
          {groups.length > 1 && <p className="babel-topic-group-label">{group.label}</p>}
          <ul className="babel-topic-list">
            {group.rows.map((topic) => (
              <TopicLink key={topic.id} topic={topic} current={current} />
            ))}
          </ul>
        </section>
      ))}
      {PARKED.map((state) => {
        const rows = named.filter((topic) => topic.interest.state === state);
        if (rows.length === 0) return null;
        return (
          <Disclosure
            className="babel-topic-fold"
            key={state}
            open={unfolded[state] === true}
            onOpenChange={(next) => setUnfolded((current) => ({ ...current, [state]: next }))}
            header={
              <>
                {INTEREST_LABEL[state] ?? state}
                <span className="babel-topic-group-count">{rows.length.toLocaleString()}</span>
              </>
            }
          >
            <ul className="babel-topic-list">
              {rows.map((topic) => (
                <TopicLink key={topic.id} topic={topic} current={current} />
              ))}
            </ul>
          </Disclosure>
        );
      })}
      {(proposed.length > 0 || Object.keys(ruled).length > 0) && (
        <section className="babel-topic-group babel-topic-proposed">
          <p className="babel-topic-group-label">
            Babel proposes
            {proposed.length > 0 && <span className="babel-topic-group-count">{proposed.length.toLocaleString()}</span>}
          </p>
          <ul className="babel-topic-list">
            {proposed.map((proposal) => (
              <Proposed
                key={proposal.proposalId}
                host={host}
                proposal={proposal}
                onRuled={(said) => {
                  setRuled((current_) => ({ ...current_, [proposal.proposalId]: said }));
                  topics.refresh();
                }}
              />
            ))}
          </ul>
          {Object.entries(ruled).map(([id, said]) => (
            <p className="babel-topic-ruled" key={id}>
              {said}
            </p>
          ))}
        </section>
      )}
      {answer.unfiled > 0 && (
        <button type="button" className="babel-topic-unfiled" onClick={onUnfiled}>
          {answer.unfiled.toLocaleString()} filed under nothing
        </button>
      )}
      {flat.length > RAIL_TOPICS && (
        <p className="babel-note">{named.length.toLocaleString()} topics in all</p>
      )}
    </Stack>
  );
}

/**
 * WHAT BABEL DID TODAY, in one line (§8.7's pulse). The rulings are the store's own count of
 * what was ruled; how many THIS reading recorded is the sentence's, beside the total, because
 * a projection with a stated freshness would go backwards while he worked.
 */
export function Pulse({ host }: { host: HostServices }): ReactElement | null {
  const pulse = usePolledResource<PulseResult | null>(async () => ask(host, ACTIONS.pulse, {}), RAIL_POLL_MS, {
    key: "atyrode.babel.pulse",
    initial: null,
    topics: [BABEL_NODE],
    events: host.client,
  });
  const value = pulse.value;
  if (value === null) return null;
  const today = value.today;
  return (
    <p className="babel-pulse">
      Today Babel read {today.sessionsRead.toLocaleString()} sessions · wrote {today.records.toLocaleString()} · voted{" "}
      {today.votes.toLocaleString()} · proposed {today.proposals.toLocaleString()}
      {today.topicProposals > 0 && <> · {today.topicProposals.toLocaleString()} about topics</>} · you ruled{" "}
      {today.ruled.toLocaleString()}
      {value.reviewing.length > 0 && (
        <span className="babel-pulse-reviewing" title={value.reviewing.map((row) => row.title).join(" · ")}>
          {" "}
          · {value.reviewing.length.toLocaleString()} under review
        </span>
      )}
    </p>
  );
}
