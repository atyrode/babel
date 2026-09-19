import { useState, type FormEvent, type ReactElement } from "react";
import type { HostServices, PanelProps } from "@manifold/plugin";
import { usePolledResource } from "@manifold/plugin/hooks";
import type { MachineSummary } from "@manifold/protocol";
import { Cluster, Disclosure, ScrollRegion, Stack } from "@manifold/ui";
import { ACTIONS, FEED_PLUGIN_ID, INTEREST_STATES, asLaunchRequest, door } from "../contract.ts";
import {
  BABEL_NODE,
  ask,
  refusal,
  since,
  useNow,
  useShown,
  type CoverageRow,
  type FeedQuery,
  type ProfilesResult,
  type TopicResult,
  type TopicRow,
} from "./api.ts";
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
  a locator is evidence ABOUT a topic and never the topic. Which topic this is comes from the
  seat's own argument when it was opened for one, and from Home's selection when it was not.
*/

const ASKS: ReadonlyArray<{
  readonly value: string;
  readonly label: string;
  readonly asks: string;
}> = [
  { value: "retire", label: "Retire this topic", asks: "retire this" },
  { value: "split", label: "Split this topic", asks: "split this" },
  { value: "merge", label: "Merge this topic into…", asks: "merge this" },
];

export function TopicPanel({ host, arg }: PanelProps): ReactElement {
  const shown = useShown(arg, "topic");
  return (
    <ScrollRegion className={`plugin-${FEED_PLUGIN_ID.replaceAll(".", "_")}`} aria-label="Topic">
      <Stack className="babel-panel" gap="var(--babel-space-4)">
        {shown === "" ? (
          <div className="babel-state">
            <strong>No topic open</strong>
            <span>Press a topic in Home&apos;s rail, or on a row, and it is read here.</span>
          </div>
        ) : (
          <TopicView host={host} topic={shown} />
        )}
      </Stack>
    </ScrollRegion>
  );
}

function TopicView({ host, topic }: { host: HostServices; topic: string }): ReactElement {
  const [query, setQuery] = useState<FeedQuery>({
    ...EMPTY_QUERY,
    surface: "all",
    sort: "new",
    // A topic page is already one subject, so grouping it by subject would put every row
    // under one heading naming the page it is on.
    group: "none",
    topic,
  });
  const [failure, setFailure] = useState("");
  const now = useNow();

  const read = usePolledResource<TopicResult | null>(
    async () => ask(host, ACTIONS.topic, { topic }),
    RAIL_POLL_MS,
    {
      key: "atyrode.babel.topic",
      restartKey: topic,
      initial: null,
      topics: [BABEL_NODE],
      events: host.client,
      onError: (reason) => setFailure(refusal(reason)),
      onSuccess: () => setFailure(""),
    },
  );

  /*
    THE LIST FOLLOWS THE RAIL, and the narrowing is DERIVED rather than mirrored into state
    behind an effect. A prop copied into state by a commit is a commit where the header names
    one topic and the rows under it belong to the one the reader just left; deriving it means
    the first render of the new subject is already the new subject.

    Changing subject also returns the list to the top of it: `limit` is what "Show 15 more"
    raises, so a topic inheriting the depth the reader had paged the LAST one to opened
    part-way down a list he had never expanded. `offset` was the field this reset reached for
    and nothing on these surfaces moves it.
  */
  const narrowed: FeedQuery =
    query.topic === topic ? query : { ...query, topic, offset: 0, limit: EMPTY_QUERY.limit };

  const row = read.value?.topic ?? null;
  const proposed = read.value?.proposed ?? [];

  return (
    <FeedListing
      host={host}
      query={narrowed}
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
                No topic in this hub answers to that name. What follows is the feed narrowed to it,
                which is why it is empty: only you create a topic, and Babel proposes the identity.
              </p>
            )}
            {row !== null && row.posts === 0 && (
              <p className="babel-note">
                Nothing is filed under this topic yet. It exists, and no record has been said to be
                about it.
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
            <Coverage host={host} topic={row} rows={read.value?.coverage ?? []} />
            {row !== null && <AskBabel host={host} topic={row} />}
          </Stack>
        </header>
      }
    />
  );
}

/**
 * WHICH LENSES HAVE LOOKED HERE, WHICH NEVER HAVE — AND A RUN FOR ONE THAT NEVER HAS.
 *
 * The never-looked group is the reason this exists, so it is last and it is labelled: a grid
 * whose zeros were mixed in with its counts would need reading rather than glancing, and the
 * question it answers — what has nobody examined about this — is the one the surface could not
 * ask at all. A hub whose policy names no recipe renders nothing, the way an absent section
 * renders as nothing rather than as an empty heading.
 *
 * READING A BLANK CELL IS NOT ACTING ON ONE (#330), so the zeros the launch door would accept
 * carry the launch. Only those: a lens the operator turned off, or one his policy names without
 * a body, is named in the sentence above and offered nowhere, and a topic no entity answers to
 * has nothing to scope a run to.
 */
function Coverage({
  host,
  topic,
  rows,
}: {
  host: HostServices;
  topic: TopicRow | null;
  rows: readonly CoverageRow[];
}): ReactElement | null {
  if (rows.length === 0) return null;
  const looked = rows.filter((row) => row.records > 0);
  const never = rows.filter((row) => row.records === 0);
  const offers = never.filter((row) => row.runnable);
  return (
    <section className="babel-coverage">
      {looked.length > 0 && (
        <p className="babel-note">
          {looked.map((row) => `${row.title || row.recipeId} (${String(row.records)})`).join(" · ")}
        </p>
      )}
      {never.length > 0 && (
        <p className="babel-note">
          never looked: {never.map((row) => row.title || row.recipeId).join(" · ")}
        </p>
      )}
      {topic !== null && offers.length > 0 && (
        <LookHere host={host} topic={topic} lenses={offers} />
      )}
    </section>
  );
}

/** What a launch cannot be posted without, in the order an operator would supply it. */
const NO_MACHINE = "Pick a machine to run on.";
const NO_PROFILE =
  "Pick the Code profile this run is posted on — the model, the thinking level and the account " +
  "are its.";

/**
 * THE LAUNCH BEHIND A BLANK CELL, and it is the launch Watch posts (#330).
 *
 * A run reaches a model, so it is a Code session and it needs a machine and a Code profile —
 * and this form asks for both, because a control that posted a run by a shorter route than the
 * Start form would be a hole in the spend discipline rather than a convenience. What the press
 * sends is `lensLaunch`'s request, through the same `launch` door, under the same ceilings, with
 * the profile revision the operator was shown; the only thing this surface knows that Watch's
 * form does not is WHICH lens and WHICH topic, which is the whole of what a coverage cell adds.
 *
 * The machines and the profiles are read when the fold OPENS and after a refusal, never polled:
 * neither changes while a reader is deciding, and a topic page that polled them would pay for a
 * control nobody opened. A refused press re-reads them, because the likeliest refusal is Code's
 * list having moved under the choice — which is what `code_stale_preferences` says and what a
 * re-read explains.
 */
function LookHere({
  host,
  topic,
  lenses,
}: {
  host: HostServices;
  topic: TopicRow;
  lenses: readonly CoverageRow[];
}): ReactElement {
  const [open, setOpen] = useState(false);
  const [machines, setMachines] = useState<readonly MachineSummary[]>([]);
  /*
    NOT ASKED YET IS ITS OWN STATE. Code's list has three — refused, empty, and a list — and a
    fold nobody opened has put no question at all, which must not print "you have saved none"
    as the answer to it.
  */
  const [profiles, setProfiles] = useState<ProfilesResult | null>(null);
  const [machineId, setMachineId] = useState("");
  const [containerId, setContainerId] = useState("");
  const [starting, setStarting] = useState("");
  const [note, setNote] = useState("");
  const [failure, setFailure] = useState("");

  async function readWhatALaunchNeeds(): Promise<void> {
    try {
      const [held, saved] = await Promise.all([
        host.client.machines(),
        ask(host, ACTIONS.profiles, {}),
      ]);
      setMachines(held);
      setProfiles(saved);
    } catch (failed) {
      setFailure(refusal(failed));
    }
  }

  const saved = profiles?.profiles ?? [];
  const unavailable = profiles?.unavailable ?? "";
  const profile = saved.find((row) => row.containerId === containerId) ?? null;
  const blocked =
    unavailable !== ""
      ? unavailable
      : machineId === ""
        ? NO_MACHINE
        : profile === null
          ? NO_PROFILE
          : "";

  async function start(lens: CoverageRow): Promise<void> {
    if (profile === null || machineId === "") return;
    setStarting(lens.recipeId);
    setNote("");
    setFailure("");
    try {
      const started = await ask(
        host,
        ACTIONS.launch,
        // The preset that scopes an explore to one entity, that entity, and the one recipe —
        // and nothing else this surface decides. The node, the ceilings, the selection and the
        // spend are the same ones Watch's form posts under.
        asLaunchRequest({
          machineId,
          preset: "explore-topic",
          entityId: topic.id,
          recipes: [lens.recipeId],
          profile: { containerId: profile.containerId, expectedRevision: profile.revision },
        }),
      );
      setNote(
        `Started ${lens.title || lens.recipeId} as ${started.runId} — Code's job is ` +
          `${started.jobId}. Watch follows it.`,
      );
    } catch (failed) {
      setFailure(refusal(failed));
      await readWhatALaunchNeeds();
    } finally {
      setStarting("");
    }
  }

  return (
    <Disclosure
      className="babel-coverage-offer"
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) void readWhatALaunchNeeds();
      }}
      header="Point a lens that never looked here"
    >
      <Stack className="babel-confirm" gap="var(--babel-space-2)">
        <p className="babel-note">
          One Code session over this topic&apos;s own sessions, performing that one recipe. It is
          the launch Watch posts, on the machine and the Code profile you pick here, under the
          ceilings in force.
        </p>
        <label>
          Machine
          <select
            data-field="machine"
            value={machineId}
            onChange={(event) => setMachineId(event.currentTarget.value)}
          >
            <option value="">Pick a machine…</option>
            {machines.map((machine) => (
              <option key={machine.id} value={machine.id} disabled={!machine.online}>
                {machine.name}
                {machine.online ? "" : " · offline"}
              </option>
            ))}
          </select>
        </label>
        {/*
          CODE'S LIST AND ITS ABSENCE ARE BOTH ANSWERS, and the third state — Code answered and
          the operator has saved none — is its own sentence: an empty picker with no word beside
          it reads as the last when it is the first.
        */}
        {profiles === null ? null : unavailable !== "" ? (
          <p className="babel-note" data-field="profiles-unavailable">
            {unavailable}
          </p>
        ) : saved.length === 0 ? (
          <p className="babel-note" data-field="profiles-empty">
            Code holds no saved profile yet. Open a workspace in Code, choose the model, the
            thinking level and the account there, and it appears here.
          </p>
        ) : (
          <label>
            Code profile
            <select
              data-field="profile"
              value={containerId}
              onChange={(event) => setContainerId(event.currentTarget.value)}
            >
              <option value="">Pick a Code profile…</option>
              {saved.map((row) => (
                <option key={row.containerId} value={row.containerId}>
                  {row.containerId}
                  {row.model === "" ? " · no selection Code can review" : ` · ${row.model}`}
                </option>
              ))}
            </select>
          </label>
        )}
        <div className="babel-acts-bar" role="group" aria-label="Lenses that never looked here">
          {lenses.map((lens) => (
            <button
              type="button"
              key={lens.recipeId}
              data-lens={lens.recipeId}
              data-action={door(ACTIONS.launch)}
              disabled={starting !== "" || blocked !== ""}
              onClick={() => void start(lens)}
            >
              {starting === lens.recipeId ? "Starting…" : `Look for ${lens.title || lens.recipeId}`}
            </button>
          ))}
        </div>
        {blocked !== "" && <p className="babel-note">{blocked}</p>}
        {note !== "" && <p className="babel-note">{note}</p>}
        {failure !== "" && (
          <p className="babel-error" role="alert">
            {failure}
          </p>
        )}
      </Stack>
    </Disclosure>
  );
}

/** What the topic is bound to, in one muted line: the remote as a name, checkouts as a count. */
function Binding({ topic }: { topic: TopicRow }): ReactElement {
  if (topic.binding === null) return <span className="babel-note">no binding recorded</span>;
  const paths = topic.binding.paths;
  return (
    <span
      className="babel-note"
      title={paths.length === 0 ? undefined : `${paths.length} checkouts seen`}
    >
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
      await ask(host, ACTIONS.interest, {
        entityId: topic.id,
        state: chosen,
        reason: reason.trim(),
      });
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
            <input
              value={reason}
              onInput={(event) => setReason(event.currentTarget.value)}
              autoFocus
            />
          </label>
          <Cluster className="babel-confirm-acts" gap="var(--babel-space-3)">
            <button type="submit" className="babel-primary" disabled={working}>
              {working
                ? "Recording…"
                : `Record ${(INTEREST_LABEL[chosen] ?? chosen).toLowerCase()}`}
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
          {topic.interest.reason !== "" && (
            <span className="babel-quote"> {topic.interest.reason}</span>
          )}
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
      setSaid(
        "Recorded. Babel's next filing run answers with a proposal you rule on like any other.",
      );
      setInto("");
      setReason("");
    } catch (failed) {
      setFailure(refusal(failed));
    } finally {
      setWorking(false);
    }
  }

  return (
    <Disclosure
      className="babel-asks"
      open={open}
      onOpenChange={setOpen}
      header="Ask Babel to change this topic"
    >
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
          <textarea
            value={reason}
            rows={2}
            onInput={(event) => setReason(event.currentTarget.value)}
          />
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
