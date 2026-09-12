import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { decideReview } from "./api";
import {
  getTopics,
  INTEREST_LABEL,
  OPERATION_LABEL,
  UNFILED,
  type InterestState,
  type TopicProposal,
  type TopicRow,
  type TopicsResponse,
} from "./feedapi";
import { errorMessage } from "./format";
import { openPalette } from "./palette";
import { SteeringSection } from "./steering";

// The chrome's own instruments, kept out of App.tsx so that file stays a
// router: what is running, how dense the interface is, and what the reader can
// press. None of them is a page, and none of them may fail loudly — a header
// that reports on the system must never be the reason the system looks broken.

// The header reads Contract W's presence endpoint and uses three of its
// fields. The rest of the payload belongs to Watch, which renders it properly;
// typing only what is consumed here keeps the shell independent of that page's
// client.
interface LiveRun {
  run_id: string;
  kind: string;
  spend_usd: number | null;
  // How old the last word from this run is, in internal/presence's own
  // vocabulary: fresh, stale, lost, finished. Absent for a child this server
  // launched a moment ago that has not announced itself yet.
  freshness?: string;
}

interface LiveResponse {
  runs?: LiveRun[] | null;
}

// Whether a row is something the header may call live. `freshness` grades the
// age of the evidence, never the health of a process, so this is the one
// question the shell is allowed to ask of it: was the run heard from recently
// enough that saying "live" is a report rather than a guess.
//
// A row that has not announced at all is live: it is a run this server started
// seconds ago, and its silence is its age, not a doubt. "stale" and "lost" are
// both excluded — presence is explicit that a lost row may be working, blocked
// or gone and that this host cannot tell, and a header that counted those
// would be asserting liveness nobody observed. On the walked deployment that
// was the whole defect: 33 rows on the endpoint, 16 with a fresh heartbeat,
// and a pill that said "33 runs".
function heardFromRecently(run: LiveRun): boolean {
  return run.freshness === undefined || run.freshness === "" ||
    run.freshness === "fresh" || run.freshness === "recent";
}

const LIVE_POLL_MS = 15_000;
// A poll that overtakes the bootstrap exchange is refused, and on a first load
// that is every request the shell makes. Retrying soon after a 401 costs one
// request and removes a fifteen-second hole where a live run is invisible.
const LIVE_RETRY_MS = 2_000;

// LiveIndicator is the header's mark that something is in flight, deployment
// wide, and a link to the page that can do something about it.
//
// It fetches directly rather than through ./api on purpose. api.ts publishes
// every failure to the error banner, and a background poll for a decoration is
// the one request in this application that must never accuse a page of being
// broken: the reader did not ask for it, and its failure costs them nothing.
export function LiveIndicator() {
  const [runs, setRuns] = useState<LiveRun[]>([]);

  useEffect(() => {
    let live = true;
    let timer = 0;
    // One quick retry after a refusal covers the first-load race with the
    // bootstrap; after that a refused page is a page with no session — a
    // spent launch link, a locked server — and it must not knock every two
    // seconds for as long as it stays open.
    let refusals = 0;

    async function poll(): Promise<void> {
      let next = LIVE_POLL_MS;
      try {
        const response = await fetch("/api/watch/live", {
          cache: "no-store",
          credentials: "same-origin",
        });
        // A build without the endpoint is not a failure and will not grow one
        // while this page is open, so the loop stops asking rather than
        // knocking on a missing door four times a minute.
        if (response.status === 404) return;
        if (response.status === 401) {
          refusals += 1;
          if (refusals === 1) next = LIVE_RETRY_MS;
        } else if (response.ok) {
          refusals = 0;
          const body = (await response.json()) as LiveResponse;
          if (!live) return;
          setRuns(Array.isArray(body.runs) ? body.runs : []);
        }
      } catch {
        // Offline, aborted, or malformed: the mark simply keeps its last state
        // and tries again on the next tick.
      }
      if (live) timer = window.setTimeout(poll, next);
    }

    void poll();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, []);

  // Every figure below is about the rows that were heard from recently, and
  // the ones that were not are neither counted nor thrown away: they are the
  // second half of the tooltip, which is where "17 rows still claim to be
  // running" belongs — it is news about presence, not about what is in flight.
  const inFlight = runs.filter(heardFromRecently);
  const doubted = runs.length - inFlight.length;
  if (inFlight.length === 0) return null;

  // Spend is summed over the in-flight runs that reported one. A run whose
  // receipt has no cost yet contributes nothing and is not counted as zero, so
  // the figure is "what is known to have been spent", never a claim about the
  // rest.
  let spend: number | null = null;
  for (const run of inFlight) {
    if (typeof run.spend_usd === "number") spend = (spend ?? 0) + run.spend_usd;
  }

  const kinds = [...new Set(inFlight.map((run) => run.kind).filter(Boolean))].join(", ");
  const title = [
    kinds ? `In flight: ${kinds}` : "Runs in flight",
    doubted > 0
      ? `${doubted} more ${doubted === 1 ? "row claims" : "rows claim"} to be running ` +
        "but have not been heard from"
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <Link className="live-indicator" to="/watch" title={title}>
      <span className="live-dot" aria-hidden="true" />
      {/* "live" rather than "runs": the number is how many runs were heard
          from recently, and the word has to say which set it counts. */}
      <span>{inFlight.length} live</span>
      {spend !== null && <span className="live-spend">${spend.toFixed(2)}</span>}
    </Link>
  );
}

// The width at which the header folds. It is the same number as the media
// query in styles.css that narrows the row, and the two must stay equal: this
// decides *what* is in the header, the stylesheet decides *where*, and a
// header that folded its controls at one width and its rows at another would
// be neither layout.
const NARROW_HEADER = "(max-width: 640px)";

function useNarrowHeader(): boolean {
  const [narrow, setNarrow] = useState(() => window.matchMedia(NARROW_HEADER).matches);
  useEffect(() => {
    // matchMedia hands out a new list object per call, so the list is made
    // here rather than during render: one subscription for the life of the
    // component instead of one per render.
    const query = window.matchMedia(NARROW_HEADER);
    // Re-read on mount as well as on change: a resize between the first
    // render and this effect would otherwise leave the wrong set of controls
    // in the header until the next one.
    setNarrow(query.matches);
    function onChange(event: MediaQueryListEvent) {
      setNarrow(event.matches);
    }
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return narrow;
}

// The width at which the topics stand beside the feed rather than folded
// above it. It is the same number as feed.css's rail query and as the
// stylesheet's own one-row header breakpoint: the page decides whether the
// rail is mounted at all, because a list rendered twice and hidden once is
// two lists to every reader who is not looking at pixels.
const WIDE_RAIL = "(min-width: 1024px)";

export function useWideViewport(): boolean {
  const [wide, setWide] = useState(() => window.matchMedia(WIDE_RAIL).matches);
  useEffect(() => {
    const query = window.matchMedia(WIDE_RAIL);
    setWide(query.matches);
    function onChange(event: MediaQueryListEvent) {
      setWide(event.matches);
    }
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return wide;
}

// How many topics the rail names before it stops. Twelve is what stands
// beside a feed without becoming the page's second list; the rest are one
// link away, and the link says so rather than the list trailing off.
const RAIL_TOPICS = 12;

// The topics, with their counts. §8.7's sixth destination that is not a page:
// the same list is the rail on a wide viewport and the fold above the feed on
// a narrow one, so it lives here with the shell's other instruments rather
// than inside the page that happens to mount it.
//
// What orders it is the operator's attention rather than the corpus's size
// (§4.13): what he is working on, what he is keeping an eye on, what he has
// said nothing about — then, folded, what he has parked and what he excluded,
// because *not interested is a signal, not a deletion* and a stance that
// hid the topic would be a deletion with extra steps.
//
// A topic is a name, a count and a stance. Nothing here prints a path: a
// binding's identity may be a checkout directory, and §4.13 is explicit that
// a locator is evidence about a topic and never the topic — so the identity
// travels in the row's title and nowhere else.
//
// Below the accepted topics is what Babel has proposed and nobody has ruled
// on. Those rows are shortcuts to an ordinary proposal record: accepting one
// is the same review decision the record page makes, because the operator
// ruled that everything about a topic goes through Babel's own chain.

// The three stances that read flat, in order, with the words the section
// carries. The other two are folded below them.
const RAIL_GROUPS: Array<{ state: string; label: string }> = [
  { state: "working", label: "Working on it" },
  { state: "watching", label: "Keep an eye" },
  { state: "", label: "Nothing said" },
];

// What one row of a proposal says it would do, from the proposal's own
// fields. The operation is the server's word and an unknown one is rendered as
// itself: a plan this build has no sentence for must still be readable and
// rulable, because the ruling is on the proposal rather than on the sentence.
function proposalLine(proposal: TopicProposal): string {
  const subject = proposal.name || proposal.targets?.[0]?.name || "";
  const into = proposal.targets?.[1]?.name ?? "";
  switch (proposal.operation) {
    case "create":
      return `New topic t/${subject}`;
    case "merge":
      return into ? `Merge t/${subject} into t/${into}` : `Merge t/${subject}`;
    case "split":
      return `Split t/${subject}`;
    case "retire":
      return `Retire t/${subject}`;
    default:
      return `${OPERATION_LABEL[proposal.operation] ?? proposal.operation} t/${subject}`;
  }
}

export function TopicList({ current }: { current: string }) {
  const [answer, setAnswer] = useState<TopicsResponse | null>(null);
  const [failed, setFailed] = useState(false);
  // What this browser ruled on a proposal, in place of the acts, until the
  // next read: a permanent act that left the row looking exactly as it did is
  // an act the operator performs twice.
  const [ruled, setRuled] = useState<Record<string, string>>({});
  // Which proposal's reason box is open. Declining keeps the reason verbatim
  // and the ruling refuses one without it, so the box is where the reason is
  // written rather than a prompt the server has to reject first.
  const [declining, setDeclining] = useState<string>("");
  const [reason, setReason] = useState("");
  const [working, setWorking] = useState("");
  const [failure, setFailure] = useState<string>("");
  // Bumped by every act, so the read below runs again and the row lands in
  // the list the act moved it into.
  const [acted, setActed] = useState(0);

  // Re-read when the reader moves, after every act, and once a minute while
  // he stays: the feed index behind these counts rebuilds every sixty seconds
  // and the session catalog can still be scanning when the page first opens,
  // so a rail read once at mount would show a day-one deployment under a full
  // feed.
  useEffect(() => {
    let live = true;
    const read = () => {
      getTopics()
        .then((next) => {
          if (live) {
            setAnswer(next);
            setFailed(false);
          }
        })
        .catch(() => {
          if (live && answer === null) setFailed(true);
        });
    };
    read();
    const timer = window.setInterval(read, 60_000);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, [acted, current]);

  // One ruling, on the proposal's own record, through the route the record
  // page uses. Nothing here writes to the ledger directly: the row is a
  // shortcut to a decision, and what the decision then does to the ledger is
  // the answer's own account of it.
  async function rule(proposal: TopicProposal, disposition: "accept" | "reject", note: string) {
    setWorking(proposal.proposal_id);
    setFailure("");
    try {
      const result = await decideReview({
        subject: { type: "proposal", id: proposal.proposal_id },
        disposition,
        note: note || undefined,
      });
      const outcome = result.topic;
      const said = disposition === "accept" ? "Accepted" : "Declined";
      // The ruling and the ledger act are two facts and can part company. A
      // ruling that stands over an act that did not land is exactly what the
      // operator has to be told, because the proposal is gone and the topic
      // is not there.
      const ledger = outcome?.error
        ? ` · the ruling stands, the ledger act did not: ${outcome.error}`
        : outcome?.applied
          ? ` · ${outcome.filed ?? 0} filed`
          : "";
      setRuled((current_) => ({ ...current_, [proposal.proposal_id]: `${said}${ledger}` }));
      setDeclining("");
      setReason("");
      setActed((count) => count + 1);
    } catch (reason_) {
      setFailure(errorMessage(reason_));
    } finally {
      setWorking("");
    }
  }

  // A rail that could not be read says so in one line and takes no more room
  // than that: the feed beside it is fine, and a failed decoration must not
  // read as a failed page.
  if (failed) return <p className="topic-note">The topics could not be read.</p>;
  if (!answer) return <p className="topic-note">Reading the topics…</p>;

  const topics = answer.topics ?? [];
  const proposed = (answer.proposed ?? []).filter((row) => !(row.proposal_id in ruled));
  const flat = topics.filter((topic) => !PARKED.includes(topic.interest.state));
  const shown = flat.slice(0, RAIL_TOPICS);
  const row = (topic: TopicRow) => (
    <li key={topic.id || topic.name}>
      <Link
        to={`/t/${encodeURIComponent(topic.name)}`}
        aria-current={current === topic.name ? "page" : undefined}
        title={topicTitle(topic)}
      >
        <span>t/{topic.name}</span>
        <span className="topic-count">{topic.posts.toLocaleString()}</span>
      </Link>
    </li>
  );

  return (
    <>
      <ul className="topic-list">
        <li>
          <Link to="/" aria-current={current === "" ? "page" : undefined}>
            All posts
          </Link>
        </li>
      </ul>
      {RAIL_GROUPS.map(({ state, label }) => {
        const group = shown.filter((topic) => interestOf(topic) === state);
        if (group.length === 0) return null;
        return (
          <section className="topic-group" key={label || "unset"}>
            <p className="topic-group-label">{label}</p>
            <ul className="topic-list">{group.map(row)}</ul>
          </section>
        );
      })}
      {/* Parked and excluded, folded with their counts. They are here rather
          than gone because §4.13 keeps them: the topic, its filings and its
          history all survive a stance, and a reader has to be able to find
          the thing he parked. */}
      {PARKED.map((state) => {
        const group = topics.filter((topic) => topic.interest.state === state);
        if (group.length === 0) return null;
        return (
          <details className="peel topic-fold" key={state}>
            <summary>
              {state === "not-now" ? "Not now" : "Excluded"}
              <span className="peel-count">{group.length.toLocaleString()}</span>
            </summary>
            <div className="peel-body">
              <ul className="topic-list">{group.map(row)}</ul>
            </div>
          </details>
        );
      })}
      {/* The posts nothing has filed. They are in the feed rather than hidden
          (§8.7) and this is the filter that selects exactly them — the triage
          backlog, not a bin; a deployment with none says nothing. */}
      {answer.unfiled > 0 && (
        <ul className="topic-list">
          <li>
            <Link
              to={`/?topic=${UNFILED}&needs=all`}
              aria-current={current === UNFILED ? "page" : undefined}
              title="Posts nothing has said what they are about. Unfiled is the triage backlog, not a bin."
            >
              <span>no topic</span>
              <span className="topic-count">{answer.unfiled.toLocaleString()}</span>
            </Link>
          </li>
        </ul>
      )}
      {flat.length > RAIL_TOPICS && (
        <Link className="topic-all" to="/t">
          all {topics.length.toLocaleString()} topics →
        </Link>
      )}

      {(proposed.length > 0 || Object.keys(ruled).length > 0) && (
        <section className="topic-group topic-proposed">
          <p className="topic-group-label">Babel proposes</p>
          <ul className="topic-list">
            {proposed.map((proposal) => (
              <li key={proposal.proposal_id} className="topic-proposal">
                <Link
                  to={`/r/${encodeURIComponent(proposal.proposal_id)}`}
                  title={proposal.title}
                  data-proposal={proposal.proposal_id}
                >
                  <span>{proposalLine(proposal)}</span>
                  <span className="topic-count">{proposal.posts.toLocaleString()}</span>
                </Link>
                <p className="topic-why">
                  {proposal.why}
                  {proposal.run_id && <> · by {proposal.run_id}</>}
                </p>
                {declining === proposal.proposal_id ? (
                  <form
                    className="topic-reason"
                    onSubmit={(event) => {
                      event.preventDefault();
                      void rule(proposal, "reject", reason.trim());
                    }}
                  >
                    <label>
                      Why this is not a topic (kept verbatim)
                      <input
                        value={reason}
                        onChange={(event) => setReason(event.target.value)}
                        // The ruling refuses a decline with no reason, so the
                        // control says so rather than letting the server say
                        // it.
                        required
                        autoFocus
                      />
                    </label>
                    <div className="topic-reason-acts">
                      <button type="submit" disabled={working === proposal.proposal_id}>
                        Decline
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          setDeclining("");
                          setReason("");
                        }}
                      >
                        Cancel
                      </button>
                    </div>
                  </form>
                ) : (
                  <div className="topic-acts">
                    <button
                      type="button"
                      data-topic-act="accept"
                      disabled={working === proposal.proposal_id}
                      title="Rule accept on this proposal. Babel performs the change and files what it named."
                      onClick={() => void rule(proposal, "accept", "")}
                    >
                      Accept
                    </button>
                    <button
                      type="button"
                      data-topic-act="decline"
                      onClick={() => {
                        setDeclining(proposal.proposal_id);
                        setReason("");
                      }}
                    >
                      Decline
                    </button>
                  </div>
                )}
              </li>
            ))}
            {Object.entries(ruled).map(([id, said]) => (
              <li key={id} className="topic-ruled" data-topic-ruled={id}>
                {said}
              </li>
            ))}
          </ul>
          {failure && (
            <p className="inline-error" role="alert">
              {failure}
            </p>
          )}
        </section>
      )}
    </>
  );
}

// The two stances that fold. They are the ones the operator has said he is
// not spending attention on, and folding is what keeps the rail about what he
// is.
const PARKED = ["not-now", "excluded"];

// Which flat group a topic belongs in. A stance this build has no word for
// reads as nothing said rather than as a refusal, which is the same
// distinction §4.12 draws about feedback: silence is not opposition.
function interestOf(topic: TopicRow): string {
  const state = topic.interest.state;
  return state === "working" || state === "watching" ? state : "";
}

// What the row says when the pointer rests on it: how much is filed, how much
// of it is waiting, and what the name is bound to. The binding is here rather
// than in the row because it can be a path, and a path is a locator rather
// than a topic (§4.13).
function topicTitle(topic: TopicRow): string {
  const facts = [`${topic.posts.toLocaleString()} posts`];
  if (topic.awaiting > 0) facts.push(`${topic.awaiting.toLocaleString()} waiting on you`);
  if (topic.binding) facts.push(`${topic.binding.kind}: ${topic.binding.identity}`);
  if (topic.interest.state) facts.push(INTEREST_LABEL[topic.interest.state as InterestState] ?? topic.interest.state);
  return facts.join(" · ");
}

// ShellControls is the right end of the header: search, the box the operator
// tells Babel what is going badly into, and one menu for the two things that
// are neither — the keys, and the stop.
//
// The menu exists because the header used to carry six controls at four
// weights: Tell Babel, Search, a density switch, a "?", a bordered stop, and
// the live mark, none of them ranked against the others. Two of those are
// things the operator reaches for while reading — say what is going badly,
// find a record — and they stay in the row as themselves. The other two are
// asked for once: what can I press, and end this session. Those are behind
// the …, which is the quietest control on the surface because it is the least
// used.
//
// Below NARROW_HEADER search and the capture box join them, because a 390px
// row cannot hold a field, a button, a menu and three destinations — and what
// the operator loses is a click, not a capability.
//
// Tell Babel is here rather than on a page because of where it used to be:
// folded at the foot of the queue, which meant the operator could only
// complain from the one surface he complained about. #115's box is reachable
// from everywhere now, and it is the same box and the same write.
export function ShellControls({
  onKeyHints,
  onTell,
  onLock,
  stopping,
}: {
  onKeyHints: () => void;
  onTell: () => void;
  // Ending the session. It lives in App because App is what the stop replaces
  // with the terminal note; the header only offers it.
  onLock: () => void;
  stopping: boolean;
}) {
  const narrow = useNarrowHeader();
  const [open, setOpen] = useState(false);
  const host = useRef<HTMLDivElement | null>(null);
  const opener = useRef<HTMLButtonElement | null>(null);
  const menu = useRef<HTMLDivElement | null>(null);

  // The set of items changes with the viewport, so a menu left open across a
  // resize would be showing a different menu than the one that was opened.
  useEffect(() => setOpen(false), [narrow]);

  // The menu is worked by keyboard or it is not a control: opening it moves
  // the focus into it, Tab walks the items the browser's own way, and Escape
  // closes it and hands the keyboard back to the button that opened it.
  useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLElement>("button")?.focus();
    function onPointerDown(event: PointerEvent) {
      if (!host.current?.contains(event.target as Node)) setOpen(false);
    }
    function onKey(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      event.preventDefault();
      setOpen(false);
      opener.current?.focus();
    }
    // A Tab that leaves the menu closes it rather than leaving an open panel
    // behind the reader.
    function onFocusOut(event: FocusEvent) {
      if (!host.current?.contains(event.relatedTarget as Node | null)) setOpen(false);
    }
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKey);
    host.current?.addEventListener("focusout", onFocusOut);
    const surface = host.current;
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKey);
      surface?.removeEventListener("focusout", onFocusOut);
    };
  }, [open]);

  return (
    <>
      {/* Search is drawn as the field it opens rather than as a magnifier
          glyph: U+2315 is missing from most Linux font stacks and renders as a
          tofu box, and a control the operator cannot name is a control they do
          not press. The key sits at the field's end, which is also how they
          learn it. */}
      {!narrow && (
        <button
          type="button"
          className="shell-search"
          onClick={openPalette}
          title="Search records, sessions, entities and questions"
        >
          <span>Search</span>
          <kbd className="kbd">⌘K</kbd>
        </button>
      )}
      {/* The one the operator reaches for while reading something else: what
          is going badly is said where it is noticed. */}
      {!narrow && (
        <button
          type="button"
          className="shell-tell"
          onClick={onTell}
          title="Say what is going badly. It opens nothing and assigns nothing."
        >
          Tell Babel
        </button>
      )}
      <div className="shell-menu-host" ref={host}>
        <button
          type="button"
          className="shell-menu-button"
          ref={opener}
          onClick={() => setOpen((current) => !current)}
          aria-expanded={open}
          aria-haspopup="menu"
          title={narrow ? "Search, Tell Babel, keys and the stop" : "Keys and the stop"}
          aria-label="More controls"
        >
          <span aria-hidden="true">…</span>
        </button>
        {open && (
          <div
            className="surface shell-menu"
            role="menu"
            aria-label="More controls"
            ref={menu}
          >
            {narrow && (
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setOpen(false);
                  openPalette();
                }}
              >
                <span>Search</span>
                <kbd className="kbd">⌘K</kbd>
              </button>
            )}
            {narrow && (
              <button
                type="button"
                role="menuitem"
                className="shell-menu-tell"
                onClick={() => {
                  setOpen(false);
                  onTell();
                }}
              >
                <span>Tell Babel</span>
                <span className="shell-menu-meta">what is going badly</span>
              </button>
            )}
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                onKeyHints();
              }}
            >
              <span>Keys</span>
              <kbd className="kbd">?</kbd>
            </button>
            {/* The stop, and the only irreversible item on the surface. It
                keeps its own confirmation in App — a native dialogue — so
                reaching it through a menu costs it nothing. */}
            <button
              type="button"
              role="menuitem"
              className="shell-menu-stop"
              disabled={stopping}
              onClick={() => {
                setOpen(false);
                onLock();
              }}
              title="Revoke this session and stop this server"
            >
              <span>{stopping ? "Stopping…" : "Lock & stop"}</span>
              <span className="shell-menu-meta">ends the session</span>
            </button>
          </div>
        )}
      </div>
    </>
  );
}

// The keys Babel answers to. This list is the contract, not a description of
// it: every entry here is implemented by the shell, the listings or the record
// page, and an entry that stops being true is a bug in this file.
const KEY_HINTS: { group: string; keys: { press: string[]; does: string }[] }[] = [
  {
    group: "Anywhere",
    keys: [
      { press: ["⌘K", "Ctrl+K"], does: "Search records, sessions, entities and open questions" },
      { press: ["?"], does: "These key hints" },
      { press: ["Esc"], does: "Close whatever is open" },
    ],
  },
  {
    group: "The feed",
    keys: [
      { press: ["j", "k"], does: "Move down and up the posts" },
      { press: ["Enter"], does: "Open the focused post" },
      { press: ["s"], does: "Change the order — and, for top and controversial, the period" },
      { press: ["c"], does: "Choose which kinds of post are in the list" },
      { press: ["m"], does: "Switch between what needs you and everything" },
      { press: ["y", "n", "d"], does: "Accept, reject or defer the focused post — each confirmed first" },
      { press: ["f"], does: "Send the focused post back to Babel for refinement" },
      { press: ["q"], does: "Ask Babel a question about the focused post" },
    ],
  },
  {
    group: "A post",
    keys: [
      { press: ["y", "n", "d"], does: "Accept, reject or defer — each confirmed first" },
      { press: ["f"], does: "Send it back for refinement" },
      { press: ["q"], does: "Ask Babel about it" },
      { press: ["r"], does: "Move to the acts, and choose there" },
      { press: ["1", "…", "5"], does: "Open or close a depth" },
    ],
  },
];

// KeyHints is a dialog over the page the reader is already on: the question
// "what can I press here" is asked in place and answered in place.
export function KeyHints({ onClose }: { onClose: () => void }) {
  return (
    <div
      className="shell-dialog"
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      onClick={onClose}
    >
      <div className="surface shell-dialog-panel" onClick={(event) => event.stopPropagation()}>
        <h2>Keys</h2>
        <p className="muted">
          Every list and every record can be worked without the mouse. Keys are ignored while
          you are typing in a field.
        </p>
        {KEY_HINTS.map((section) => (
          <section className="keyhints-group" key={section.group}>
            <h3>{section.group}</h3>
            <ul className="keyhints-list">
              {section.keys.map((hint) => (
                <li key={hint.does}>
                  <span>
                    {hint.press.map((key) => (
                      <kbd className="kbd" key={key}>
                        {key}
                      </kbd>
                    ))}
                  </span>
                  <span>{hint.does}</span>
                </li>
              ))}
            </ul>
          </section>
        ))}
        <button type="button" className="keyhints-close" onClick={onClose}>
          Close
        </button>
      </div>
    </div>
  );
}

// What the dialog's focus may move between while it is open. It is the
// browser's own idea of a focusable control, minus the ones a modal must not
// hand the keyboard to.
const FOCUSABLE =
  "a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), summary, [tabindex]:not([tabindex='-1'])";

// TellBabel is #115's capture box, over whatever page the operator is on.
//
// It used to be a peel at the foot of the mod queue, which put the one control
// for "this is going badly" on the one surface it was most often about. §8.7
// leaves the box exactly as it was — the same component, the same write, the
// same refusal to acquire a status — and moves where it is reached from: the
// header, which is every page.
//
// The keyboard cannot leave it while it is open, and Escape closes it. A modal
// a tab press walks out of behind is a modal a keyboard reader loses, and the
// box has a textarea in it: the one dialogue on this surface somebody types
// into.
export function TellBabel({ onClose }: { onClose: () => void }) {
  const panel = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    panel.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus();

    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const targets = [...(panel.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])];
      if (targets.length === 0) return;
      const first = targets[0];
      const last = targets[targets.length - 1];
      const active = document.activeElement;
      // Only the two ends are steered. Everything between them is the
      // browser's own order, which is the order a reader expects.
      if (!event.shiftKey && active === last) {
        event.preventDefault();
        first.focus();
      } else if (event.shiftKey && (active === first || !panel.current?.contains(active))) {
        event.preventDefault();
        last.focus();
      }
    }

    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      // The control that opened the dialogue gets the keyboard back, so
      // closing it leaves the reader where he was.
      opener?.focus();
    };
  }, [onClose]);

  return (
    <div
      className="shell-dialog"
      role="dialog"
      aria-modal="true"
      aria-label="Tell Babel what is going badly"
      onClick={onClose}
    >
      <div
        className="surface shell-dialog-panel shell-tell-panel"
        ref={panel}
        onClick={(event) => event.stopPropagation()}
      >
        <div className="section-heading">
          <div>
            <p className="eyebrow">Steering pressure</p>
            <h2>Tell Babel</h2>
          </div>
          <button type="button" onClick={onClose} title="Close (Esc)">
            Close
          </button>
        </div>
        <SteeringSection />
      </div>
    </div>
  );
}

// ShellFooter carries the §1 frame — once, for the whole application. It used
// to be a dashed box beside every analytical panel, which on a record page
// meant four copies of the same caveat on one screen (see analysis.tsx).
export function ShellFooter({ version }: { version: string }) {
  return (
    <footer className="app-footer">
      <p>
        Babel's analytical output is fallible interpretation, not established fact: it is
        creative, incomplete, and recorded for human review. Follow the evidence locators before
        believing a claim.
      </p>
      <p className="footer-keys">
        <span className="mono">{version}</span>
        <span aria-hidden="true">·</span>
        <span>
          press <kbd className="kbd">?</kbd> for keys
        </span>
      </p>
    </footer>
  );
}
