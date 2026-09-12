import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  getEvaluationList,
  getRealityInbox,
  getReviewQueue,
  type EvaluationItem,
  type QuestionSummary,
  type QueueItem,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { Badge, PartialListNotice, type Tone } from "../analysis";
import { kindLabel } from "../evaluation";
import { answerableStates } from "../reality";
import { SteeringSection } from "../steering";
import { RuleBar } from "../record";
import { putReception, type OperatorStance, type RecordKind } from "../recordapi";
import "../decide.css";

// Decide answers one question: what needs me?
//
// It replaces four surfaces that each held part of the answer — the review
// queue, the dashboard's review inbox, the ledger's question inbox, and the
// reconsiderations buried in the evaluation backlog's `reconsider` lane. They
// were four destinations because they come from four stores, which is Babel's
// problem and was never the reader's: an operator with something to rule on
// does not know, and must not have to know, which store is holding it.
//
// So there is one queue of mixed kinds, and it is the first thing on the page.
// Each row is one line of the record's own claim, the kind of thing it is, and
// why it is next — because a row is for deciding whether to open the thing,
// and the reason it is at the top is the one fact a reader cannot reconstruct
// for himself. Everything else is a peel down on the record's own page.
//
// The header is what the operator asked the page for: since you last looked,
// this much arrived, this much is waiting, this much was spent. Capture — the
// box he tells Babel what is going badly into — moved to a collapsed peel at
// the foot, because it is the second thing he does here and it used to push
// the queue a thousand pixels down the page.
//
// Triage happens without leaving the list: j/k move, Enter opens, a/d/u record
// a stance, r opens the record's own rule bar on the row. The same controls
// are on every row under the pointer, so the keyboard is a shortcut and never
// the only way in.

const PAGE_SIZE = 20;

// How many reconsiderations to draw. They are rare — a reconsideration is
// something changing about a record already decided — so one page of them is
// the whole set in practice, and drawing more would cost a request to render
// rows below the fold of a queue whose point is the top of it.
const RECONSIDER_LIMIT = 25;

// Where the "since you last looked" mark is kept, and where this browser
// remembers the stances it recorded.
//
// Both are local by necessity rather than by preference. The mark is one
// person's reading history on one machine and Babel stores no such thing; the
// stance echo exists because the queue row carries no reception, so a row the
// operator voted on a minute ago would come back from the server looking
// exactly like one he had never seen. The record's own page remains the
// authority for what was recorded — this is a receipt, not a store.
const SEEN_KEY = "babel.decide.seen";
const STANCE_KEY = "babel.decide.stance";

// How many stance receipts to keep. The queue is drained, so old entries name
// records that will never appear in it again; a few hundred covers every row
// an operator can see and keeps the key small.
const STANCE_LIMIT = 200;

// The widest window the spend figure will ask for. Contract W's series is
// per-day, so an operator returning after a year would otherwise ask for 365
// rows to add up four of them.
const SPEND_DAYS_MAX = 90;

// The window the spend figure covers when there is no mark yet — a first look
// has no "since", and a month is the period the rest of the surface reports
// spend over.
const SPEND_DAYS_DEFAULT = 30;

// The mark this visit reads from, captured once per page load.
//
// Following a row into a record and coming back is one visit and not two.
// Reading the mark on every mount would reset the figure to "0 new, moments
// ago" at exactly the moment the operator wants it — the return from the first
// record he opened. A reload or a new tab is a new visit and picks up the mark
// written on the way out.
let visitMark: string | null | undefined;

function visitSeen(): string | null {
  if (visitMark === undefined) visitMark = readStored(SEEN_KEY);
  return visitMark;
}

function readStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    // A browser that refuses storage loses the mark and nothing else: every
    // figure derived from it is simply not shown.
    return null;
  }
}

function writeStored(key: string, value: string) {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Same bargain as reading it.
  }
}

// One stance this browser recorded, and when.
interface StanceMark {
  stance: OperatorStance;
  at: string;
}

const STANCES: OperatorStance[] = ["agree", "disagree", "unsure"];

function readStances(): Record<string, StanceMark> {
  const raw = readStored(STANCE_KEY);
  if (!raw) return {};
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return {};
  }
  if (!parsed || typeof parsed !== "object") return {};
  // Every entry is validated on the way in, because this key is writable by
  // anything else running on the origin and a stance word is rendered.
  const marks: Record<string, StanceMark> = {};
  for (const [id, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (!value || typeof value !== "object") continue;
    const mark = value as { stance?: unknown; at?: unknown };
    if (typeof mark.at !== "string") continue;
    if (!STANCES.includes(mark.stance as OperatorStance)) continue;
    marks[id] = { stance: mark.stance as OperatorStance, at: mark.at };
  }
  return marks;
}

function writeStances(marks: Record<string, StanceMark>) {
  const kept = Object.entries(marks)
    .sort((left, right) => right[1].at.localeCompare(left[1].at))
    .slice(0, STANCE_LIMIT);
  writeStored(STANCE_KEY, JSON.stringify(Object.fromEntries(kept)));
}

// Contract W's series, declared beside its only reader here rather than in the
// shared client: /api/watch/series belongs to Watch, and this page consumes
// one field of it for one figure.
//
// `spend_usd` is nullable on the wire and the null is load-bearing — a day
// whose receipts hold no cost is unknown, not free — so a day that reports
// nothing contributes nothing and is not summed as zero.
interface WatchSeriesDay {
  day: string;
  spend_usd: number | null;
}

interface WatchSeries {
  days: WatchSeriesDay[] | null;
}

// One row of the merged queue, flattened from whichever store produced it.
//
// The ordering rule lives in `rank`, and it is deliberately a small integer
// rather than a score: this queue is not ranked by a policy, it is grouped by
// how stuck Babel is without an answer, and a decimal would invite the reader
// to argue with a precision that does not exist.
interface Row {
  key: string;
  rank: number;
  claim: string;
  href: string;
  // The kind of thing this is, and the row's only badge. Questions and
  // reconsiderations ride the same queue and are told apart by this word.
  kind: { label: string; tone: Tone };
  // Which kind of thing this is, in the vocabulary the chips and the
  // ordering use: the record's own kind, or "question". It is kept apart
  // from the badge above because the badge is what the row says and this is
  // what the page sorts and filters on.
  kindKey: string;
  // Why this row is next, in five words at most, from the basis the row's own
  // store returned. Never a score: the queue is grouped, not rated.
  why: string;
  whyTitle?: string;
  at: string;
  // Present when the row is a record. A question is answered on its own page
  // and carries neither a reception nor a disposition, so the triage controls
  // are absent for it rather than present and refused.
  record?: { id: string; kind: RecordKind };
}

// The record kinds a row can carry a stance and a ruling on. A reconsideration
// names its subject kind as a bare string, and a kind this build does not know
// is a row with no controls rather than a cast that lies.
const RECORD_KINDS: Record<string, true> = {
  proposal: true,
  finding: true,
  hypothesis: true,
  observation: true,
};

// The kinds the review queue holds, in the order the operator answers them.
//
// A proposal is a remedy addressed to him; a finding is a conclusion Babel
// drew and wants confirmed; a hypothesis is a candidate Babel is still
// developing on its own. Enrolment order alone buried the first under the
// last — Babel produces candidates faster than it produces remedies, so a
// queue ordered only by age is a report on Babel's throughput rather than on
// the operator's work. The weight is applied inside a rank and never across
// one: a question Babel is blocked on still comes before every record.
const QUEUE_KINDS = ["proposal", "finding", "hypothesis"] as const;

// The weight a kind carries inside a rank. Anything this build does not know
// the name of sorts after the three it does.
const KIND_WEIGHT: Record<string, number> = { proposal: 0, finding: 1, hypothesis: 2 };
const KIND_WEIGHT_OTHER = 3;

// What this page calls each kind when it is counting them. On Decide the
// question is which of these Babel is asking the operator to rule on and
// which it is still developing, and "candidate" is the word for the second;
// the record's own badge keeps the corpus vocabulary, which is why this is a
// word for the figure and the chip rather than a relabelling of the kind.
// The singulars are written down because English does not derive them and a
// figure reading "1 proposals" is a figure nobody proofread.
const KIND_WORDS: Record<string, { one: string; many: string }> = {
  proposal: { one: "proposal", many: "proposals" },
  finding: { one: "finding", many: "findings" },
  hypothesis: { one: "candidate", many: "candidates" },
};

// The queue's own filter. One chip per kind of thing in it, and the default
// is everything: a filter that hides part of the queue by default would be
// this page answering a question the operator did not ask.
const CHIPS: Array<{ key: string; label: string; hint: string }> = [
  {
    key: "",
    label: "Everything",
    hint: "Every kind, ordered so that at equal urgency a proposal comes before a candidate.",
  },
  {
    key: "proposal",
    label: "Proposals",
    hint: "Remedies Babel wrote for you to rule on. These are what a ruling is for.",
  },
  {
    key: "finding",
    label: "Findings",
    hint: "Conclusions Babel drew from its own evidence and wants confirmed.",
  },
  {
    key: "hypothesis",
    label: "Candidates",
    hint: "Hypotheses — what Babel is still developing rather than asking you to settle.",
  },
  {
    key: "question",
    label: "Questions",
    hint: "Things only you can answer. A question is answered on its own page.",
  },
];

// The age of something in two words. `formatTime`'s "6 days ago" spends a
// third of a five-word explanation on the tense.
function elapsed(at: string): string | null {
  const started = new Date(at).getTime();
  if (Number.isNaN(started)) return null;
  const seconds = Math.max(0, (Date.now() - started) / 1000);
  const spans: Array<[string, number]> = [
    ["year", 31_536_000],
    ["month", 2_592_000],
    ["week", 604_800],
    ["day", 86_400],
    ["hour", 3_600],
    ["minute", 60],
  ];
  for (const [unit, span] of spans) {
    if (seconds < span) continue;
    const value = Math.floor(seconds / span);
    return `${value} ${unit}${value === 1 ? "" : "s"}`;
  }
  return "moments";
}

// The first few words of a model's or a policy's sentence, for the one line of
// a row that has to be scannable. The whole sentence rides the row's title, so
// nothing is lost by cutting it here.
function clipWords(text: string, words: number): string {
  const parts = text.trim().split(/\s+/);
  if (parts.length <= words) return parts.join(" ");
  return `${parts.slice(0, words).join(" ")}…`;
}

// The stance in the words the page speaks it in. Three call sites — the
// button, the mark on the row, and the announcement — have to agree, because
// an operator reading "you are unsure" beside a button labelled "Unsure" must
// be reading about the same act.
function stanceWord(stance: OperatorStance): string {
  switch (stance) {
    case "agree":
      return "agree";
    case "disagree":
      return "disagree";
    default:
      return "are unsure";
  }
}

// How many days of series to ask for to cover the mark. One day minimum,
// because a mark set an hour ago still needs today's row.
function spendDays(seen: string | null): number {
  if (!seen) return SPEND_DAYS_DEFAULT;
  const since = new Date(seen).getTime();
  if (Number.isNaN(since)) return SPEND_DAYS_DEFAULT;
  const days = Math.ceil((Date.now() - since) / 86_400_000);
  return Math.min(SPEND_DAYS_MAX, Math.max(1, days));
}

function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

function DecidePage() {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const [queue, setQueue] = useState<QueueItem[] | null>(null);
  // How many of each kind are awaiting a ruling, counted by the store rather
  // than by the rows on this page: the queue serves a window and the figure
  // is about the whole backlog. A kind whose read was refused is null and is
  // reported as unread, because a queue of unknown depth is not an empty one.
  const [counts, setCounts] = useState<Record<string, number | null>>({});
  const [degraded, setDegraded] = useState(false);
  const [questions, setQuestions] = useState<QuestionSummary[] | null>(null);
  const [reconsider, setReconsider] = useState<EvaluationItem[] | null>(null);
  const [reconsiderTotal, setReconsiderTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  // Whether the shared client has finished a request, which is how this page
  // knows the bootstrap exchange is behind it.
  const [settled, setSettled] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // `undefined` is no answer at all — this deployment serves no series, or the
  // read failed — and the figure is then not drawn. `null` is an answer with
  // nothing in it, which says so rather than claiming nothing was spent.
  const [spend, setSpend] = useState<number | null | undefined>(undefined);

  const [seen] = useState(visitSeen);
  const [stances, setStances] = useState<Record<string, StanceMark>>(readStances);
  const [focus, setFocus] = useState(-1);
  const [ruling, setRuling] = useState<string | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState<string | null>(null);
  const rows = useRef(new Map<string, HTMLLIElement>());

  const page = Math.max(0, Number(params.get("page") ?? 0) || 0);

  // Leaving the page moves the mark. It is written on the way out rather than
  // on arrival so that what the operator saw on this visit stays counted as
  // new for the whole of it.
  useEffect(
    () => () => {
      const now = new Date().toISOString();
      visitMark = now;
      writeStored(SEEN_KEY, now);
    },
    [],
  );

  // Five reads, three stores, and a failure in one must not blank the
  // others: an operator whose ledger is unreachable still has records
  // enrolled for a ruling, and a page that refused to show them would be
  // reporting the ledger's outage as an empty inbox. Only a total failure
  // is an error.
  //
  // The queue is read once per kind rather than once, and the three reads
  // are what make this page honest about its own depth. A single read
  // returns the window the server chose to serve, which is the oldest page
  // of a backlog two thousand candidates deep: every proposal in it was
  // invisible, and the one figure on the page said 2,499 without saying
  // what those were. Each typed read carries its kind's own total, so the
  // figure can state its parts, and each carries that kind's own oldest
  // page, so the merged queue can put a proposal above a candidate that was
  // enrolled first.
  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    const reviews = Promise.allSettled(QUEUE_KINDS.map((type) => getReviewQueue({ type })));
    const inbox = getRealityInbox();
    const changed = getEvaluationList({
      lane: "reconsider",
      sort: "reconsider",
      limit: RECONSIDER_LIMIT,
    });
    Promise.allSettled([reviews, inbox, changed])
      .then(([reviewed, asked, reopened]) => {
        const answers = reviewed.status === "fulfilled" ? reviewed.value : [];
        const items: QueueItem[] = [];
        // Another host's committed reviews are appended to every typed read,
        // because they are an attributed appendix and not part of the type
        // the query named. Three reads would draw each of them three times.
        const drawn: Record<string, true> = {};
        const counted: Record<string, number | null> = {};
        let refused = 0;
        let partial = false;
        QUEUE_KINDS.forEach((type, index) => {
          const answer = answers[index];
          if (!answer || answer.status === "rejected") {
            counted[type] = null;
            refused += 1;
            return;
          }
          const page = answer.value.items ?? [];
          counted[type] = answer.value.total ?? page.length;
          if (answer.value.sync_degraded === true) partial = true;
          for (const item of page) {
            const id = `${item.subject.type}-${item.subject.id}`;
            if (drawn[id]) continue;
            drawn[id] = true;
            items.push(item);
          }
        });
        setQueue(refused === QUEUE_KINDS.length ? null : items);
        setCounts(counted);
        setDegraded(partial);
        if (asked.status === "fulfilled") {
          setQuestions(
            (asked.value.items ?? []).filter((item) => answerableStates.includes(item.state)),
          );
        } else {
          setQuestions(null);
        }
        if (reopened.status === "fulfilled") {
          setReconsider(reopened.value.items ?? []);
          setReconsiderTotal(reopened.value.total ?? 0);
        } else {
          setReconsider(null);
        }
        if (
          refused === QUEUE_KINDS.length &&
          asked.status === "rejected" &&
          reopened.status === "rejected"
        ) {
          const first = answers.find((answer) => answer.status === "rejected");
          setError(errorMessage(first?.status === "rejected" ? first.reason : asked.reason));
        }
      })
      .finally(() => {
        setLoading(false);
        setSettled(true);
      });
  }, []);

  useEffect(load, [load]);

  // The spend figure. It is one number from a sibling surface's series and it
  // is optional in the strong sense: a deployment whose build has no Watch API
  // answers 404 and this page shows three figures instead of four.
  //
  // It is a bare fetch rather than the shared client, and that is the point of
  // it: the client publishes every failure to the chrome's error banner, and a
  // 404 for a figure the operator did not ask for is not an error he needs to
  // see. This read asks whether the endpoint is there, so its absence is an
  // answer rather than a fault.
  //
  // It waits for the queue to answer because the shared client performs §2.7's
  // bootstrap exchange: a request that overtook it would be refused for want
  // of a session cookie and read as a missing endpoint.
  useEffect(() => {
    if (!settled) return;
    let live = true;
    const from = seen ? seen.slice(0, 10) : "";
    fetch(`/api/watch/series?days=${spendDays(seen)}`, {
      cache: "no-store",
      credentials: "same-origin",
    })
      .then(async (response) => {
        if (!response.ok) throw new Error(`series unavailable: ${response.status}`);
        return (await response.json()) as WatchSeries;
      })
      .then((answer) => {
        if (!live) return;
        let total: number | null = null;
        for (const day of answer.days ?? []) {
          if (day.day < from) continue;
          if (typeof day.spend_usd !== "number") continue;
          total = (total ?? 0) + day.spend_usd;
        }
        setSpend(total);
      })
      .catch(() => {
        if (live) setSpend(undefined);
      });
    return () => {
      live = false;
    };
  }, [seen, settled]);

  const merged = useMemo(
    () => merge(queue ?? [], questions ?? [], reconsider ?? []),
    [queue, questions, reconsider],
  );

  // What arrived since the mark, counted from the timestamps the rows already
  // carry. It is deliberately the count of rows and not of records in the
  // corpus: this is the queue's own arrivals, which is what "since you last
  // looked" means on a page about the queue.
  const arrived = useMemo(() => {
    if (!seen) return null;
    return merged.filter((row) => row.at > seen).length;
  }, [merged, seen]);

  // How long ago the mark was set, in the same two words the rows use for
  // their own ages. `formatTime` renders anything under a minute as "now",
  // and "new since now" is not a sentence.
  const since = seen ? elapsed(seen) : null;

  // What the chips select. An unknown value in the URL is not an error and
  // not an empty queue: it selects everything, which is what a reader who
  // typed a word into the address bar meant.
  const asked = params.get("kind") ?? "";
  const chosen = CHIPS.some((chip) => chip.key === asked) ? asked : "";
  const visible = chosen ? merged.filter((row) => row.kindKey === chosen) : merged;

  // How deep the queue is, by kind and in total. The total is the sum of the
  // kinds that answered and never of all of them: a store that refused is
  // absent from both the figure and its parts, so the arithmetic on screen
  // is the arithmetic the reader can check.
  const awaiting = useMemo(() => {
    let total: number | null = null;
    for (const type of QUEUE_KINDS) {
      const count = counts[type];
      if (typeof count === "number") total = (total ?? 0) + count;
    }
    return total;
  }, [counts]);

  // The figure's parts, in the order the queue ranks them. A kind the store
  // reported none of is left out rather than set beside the real counts as a
  // zero, and a kind that did not answer says so: an unread store is not an
  // empty one, and the two must not read alike.
  const split: ReactNode[] = [];
  for (const type of QUEUE_KINDS) {
    const count = counts[type];
    const words = KIND_WORDS[type];
    if (count === null) {
      split.push(
        <span key={type}>
          <span className="not-observed">{words.many} unread</span>
        </span>,
      );
      continue;
    }
    if (!count) continue;
    split.push(
      <span key={type}>
        <strong>{count.toLocaleString()}</strong> {count === 1 ? words.one : words.many}
      </span>,
    );
  }

  const shown = visible.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const pages = Math.ceil(visible.length / PAGE_SIZE);
  const focused = focus >= 0 ? shown[focus] : undefined;
  const focusKey = focused?.key ?? null;

  const act = useCallback(
    async (row: Row, stance: OperatorStance) => {
      if (!row.record) {
        setAnnouncement("A question is answered on its own page. It carries no stance.");
        return;
      }
      const id = row.record.id;
      setPending(row.key);
      setAnnouncement(null);
      try {
        await putReception(id, stance);
        const mark: StanceMark = { stance, at: new Date().toISOString() };
        setStances((previous) => {
          const next = { ...previous, [id]: mark };
          writeStances(next);
          return next;
        });
        setAnnouncement(`Recorded: you ${stanceWord(stance)}. It decides nothing.`);
      } catch (reason) {
        setAnnouncement(`The stance was not recorded: ${errorMessage(reason)}`);
      } finally {
        setPending(null);
      }
    },
    [],
  );

  // The focus ring is a real DOM focus, so the browser scrolls the row into
  // view and a screen reader follows the same row the ring is on. It is not
  // re-taken when something inside the row already holds it: a pointer user
  // who clicked a stance button on the row would otherwise have it snatched
  // back the instant the ring moved to his row.
  useEffect(() => {
    if (!focusKey) return;
    const element = rows.current.get(focusKey);
    if (!element || element.contains(document.activeElement)) return;
    element.focus();
  }, [focusKey]);

  // Turning the page, changing the filter or reloading the queue drops the
  // ring rather than moving it onto whatever row inherited the index.
  useEffect(() => {
    setFocus(-1);
    setRuling(null);
  }, [page, chosen]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (isTyping(event.target)) return;
      if (shown.length === 0) return;
      const target = event.target;
      const inControl =
        target instanceof HTMLElement && target.closest("a, button, summary") !== null;
      switch (event.key) {
        case "j":
          event.preventDefault();
          setFocus((current) => Math.min(shown.length - 1, current + 1));
          return;
        case "k":
          event.preventDefault();
          setFocus((current) => (current <= 0 ? 0 : current - 1));
          return;
        case "Enter": {
          // A link or a button under the cursor has its own meaning for
          // Enter, and this must not double it.
          if (inControl || !focused) return;
          event.preventDefault();
          navigate(focused.href);
          return;
        }
        case "a":
        case "d":
        case "u": {
          if (!focused) return;
          event.preventDefault();
          const stance: OperatorStance =
            event.key === "a" ? "agree" : event.key === "d" ? "disagree" : "unsure";
          void act(focused, stance);
          return;
        }
        case "r": {
          if (!focused) return;
          if (!focused.record) {
            setAnnouncement("A question is answered on its own page. There is nothing to rule.");
            return;
          }
          event.preventDefault();
          setRuling((current) => (current === focused.key ? null : focused.key));
          return;
        }
        default:
          return;
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [act, focused, navigate, shown.length]);

  function turn(next: number) {
    const query = new URLSearchParams(params);
    if (next > 0) query.set("page", String(next));
    else query.delete("page");
    setParams(query);
  }

  // Choosing a kind is a navigation, so it is in the URL: a filtered queue is
  // a thing an operator reloads, shares and walks back out of with the
  // browser's own Back button. It starts at the first page, because page two
  // of a list that just became one page long is an empty queue.
  function choose(next: string) {
    const query = new URLSearchParams(params);
    if (next) query.set("kind", next);
    else query.delete("kind");
    query.delete("page");
    setParams(query);
  }

  return (
    <section className="page decide-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Your queue</p>
          <h1>What needs me?</h1>
        </div>
      </div>

      {/* What the operator came for, in four figures. The first is the only
          one about him rather than about the corpus: it is what arrived while
          he was away, which is the question he opens Babel with. */}
      <div className="decide-stats">
        {arrived !== null && (
          <Stat
            label={since ? `new since ${since} ago` : "new since your last look"}
            value={arrived.toLocaleString()}
            note="Rows that arrived in the queue after you left it."
            title={formatTime(seen)?.absolute}
            hero
          />
        )}
        <Stat
          label="awaiting a ruling"
          value={awaiting === null ? null : awaiting.toLocaleString()}
          note="Records Babel developed far enough to ask you about."
          split={split.length > 0 ? <>{split}</> : undefined}
        />
        <Stat
          label="questions for you"
          value={questions === null ? null : questions.length.toLocaleString()}
          note="Things only you can answer, so Babel stopped guessing."
        />
        <Stat
          label="worth reconsidering"
          value={reconsider === null ? null : reconsiderTotal.toLocaleString()}
          note="Records you already decided that something has changed about."
        />
        {spend !== undefined && (
          <Stat
            label={seen ? "spent since then" : `spent, ${SPEND_DAYS_DEFAULT} days`}
            value={spend === null ? null : `$${spend.toFixed(2)}`}
            note="Model spend from this machine's own receipts."
          />
        )}
      </div>

      {degraded && <PartialListNotice />}

      {loading && merged.length === 0 && (
        <div className="surface state-note"><span className="spinner" /> Reading what is waiting…</div>
      )}
      {error && (
        <div className="surface state-note error-state">
          <strong>Nothing could be read.</strong>
          <span>{error}</span>
          <button type="button" onClick={load}>Try again</button>
        </div>
      )}
      {!loading && !error && merged.length === 0 && (
        <div className="surface state-note empty-state">
          <span className="empty-icon" aria-hidden="true">◇</span>
          <strong>Nothing awaits a decision</strong>
          <span>
            Records arrive here when exploration develops them far enough to be worth a ruling.{" "}
            <Link to="/">Read the feed</Link> in the meantime.
          </span>
        </div>
      )}

      {merged.length > 0 && (
        <>
          {/* One chip per kind of thing in the queue. Everything is the
              default and stays the default: the operator rules on proposals,
              but the candidates under them are what Babel is developing into
              the next ones, and a page that hid them by default would be
              choosing for him. */}
          <div className="decide-chips" role="group" aria-label="Kind">
            {CHIPS.map((chip) => (
              <button
                type="button"
                key={chip.key || "all"}
                data-chip={chip.key || "all"}
                className={chosen === chip.key ? "chip active" : "chip"}
                aria-pressed={chosen === chip.key}
                title={chip.hint}
                onClick={() => choose(chip.key)}
              >
                {chip.label}
              </button>
            ))}
          </div>
          <div className="decide-bar">
            <p
              className="decide-order"
              title={
                "Questions Babel is blocked on come first, then decisions something has changed " +
                "about, then records enrolled for a ruling. Inside each of those, a proposal " +
                "comes before a finding, a finding before a candidate, and the longest wait " +
                "first. Curiosities are last. Each kind is read one page deep, oldest enrolled " +
                "first, so the figures above are the backlog and the rows below are its head."
              }
            >
              Blocked first, then what changed, then what has waited longest — and at equal
              urgency a proposal outranks a finding outranks a candidate.
            </p>
            <p className="decide-keys">
              <span><kbd className="kbd">j</kbd><kbd className="kbd">k</kbd> move</span>
              <span><kbd className="kbd">↵</kbd> open</span>
              <span>
                <kbd className="kbd">a</kbd>
                <kbd className="kbd">d</kbd>
                <kbd className="kbd">u</kbd> stance
              </span>
              <span><kbd className="kbd">r</kbd> rule</span>
            </p>
            {/* Every act on a row happens in place, so the page says what it
                did — in the bar that stays on screen while the queue scrolls
                under it, because a confirmation below twenty rows is a
                confirmation the operator never sees. */}
            <p className="decide-said" role="status" aria-live="polite">
              {announcement}
            </p>
          </div>
          {shown.length > 0 && (
            <ol className="decide-queue">
              {shown.map((row, index) => (
                <QueueRow
                  row={row}
                  key={row.key}
                  index={index}
                  focused={index === focus}
                  mark={row.record ? stances[row.record.id] : undefined}
                  pending={pending === row.key}
                  ruling={ruling === row.key}
                  onFocus={() => setFocus(index)}
                  onStance={(stance) => void act(row, stance)}
                  onRule={() => setRuling((current) => (current === row.key ? null : row.key))}
                  onActed={(message) => {
                    setAnnouncement(message);
                    load();
                  }}
                  register={(element) => {
                    if (element) rows.current.set(row.key, element);
                    else rows.current.delete(row.key);
                  }}
                />
              ))}
            </ol>
          )}
          {visible.length === 0 && (
            <div className="surface state-note empty-state">
              <span className="empty-icon" aria-hidden="true">◇</span>
              <strong>Nothing of this kind is waiting</strong>
              <span>
                That is a statement about the filter, not about the queue.{" "}
                <button type="button" className="link-button" onClick={() => choose("")}>
                  Show everything
                </button>
                .
              </span>
            </div>
          )}
        </>
      )}


      {pages > 1 && (
        <div className="pager surface">
          <button type="button" disabled={page === 0} onClick={() => turn(page - 1)}>
            ← Previous
          </button>
          <span
            className="muted"
            title={
              "The queue is read one page deep per kind, oldest enrolled first. How much is " +
              "waiting altogether is the figure above."
            }
          >
            {(page * PAGE_SIZE + 1).toLocaleString()}–
            {Math.min(page * PAGE_SIZE + shown.length, visible.length).toLocaleString()} of{" "}
            {visible.length.toLocaleString()}
          </span>
          <button type="button" disabled={page + 1 >= pages} onClick={() => turn(page + 1)}>
            Next →
          </button>
        </div>
      )}

      {/* #115's capture box rides this surface by operator decision
          (2026-08-31) and stays folded: the operator who came to decide is the
          operator with something to say, but he came to decide. */}
      <details className="peel decide-tell">
        <summary>
          Tell Babel
          <span className="peel-count">say what is going badly</span>
        </summary>
        <div className="peel-body">
          <SteeringSection />
        </div>
      </details>
    </section>
  );
}

// One figure with its label, the parts it is made of, and the sentence that
// says what it is about rather than how Babel derived it.
//
// A store that did not answer says so. A zero here would claim nothing is
// waiting, which is a different thing from not having looked.
//
// The split exists because one number answered the wrong question. "2,499
// awaiting a ruling" is a figure an operator cannot act on: nearly all of it
// is candidates Babel is still developing, and the two hundred proposals
// addressed to him are the part he came for. A figure whose parts are
// different kinds of work is two facts pretending to be one.
function Stat({
  label,
  value,
  note,
  split,
  title,
  hero,
}: {
  label: string;
  value: string | null;
  note: string;
  // What the figure is made of, in the same order the queue ranks them.
  split?: ReactNode;
  title?: string;
  // The one figure that is about the operator rather than about the corpus.
  // It is marked rather than positional because it is conditional: a first
  // look has no "since", and whatever lands first must not inherit the size.
  hero?: boolean;
}) {
  return (
    <div className={hero ? "stat big decide-stat" : "stat decide-stat"}>
      <span className="stat-label" title={title}>{label}</span>
      <strong className="stat-value">
        {value === null ? <span className="not-observed" title={note}>unread</span> : value}
      </strong>
      {split && <span className="decide-split">{split}</span>}
      <span className="stat-note">{note}</span>
    </div>
  );
}

function QueueRow({
  row,
  index,
  focused,
  mark,
  pending,
  ruling,
  onFocus,
  onStance,
  onRule,
  onActed,
  register,
}: {
  row: Row;
  index: number;
  focused: boolean;
  mark: StanceMark | undefined;
  pending: boolean;
  ruling: boolean;
  onFocus: () => void;
  onStance: (stance: OperatorStance) => void;
  onRule: () => void;
  onActed: (message: string) => void;
  register: (element: HTMLLIElement | null) => void;
}) {
  const recorded = mark ? formatTime(mark.at) : null;
  return (
    <li
      className="decide-row"
      // The ring is the same state whether the keyboard or the pointer put it
      // there, so both drive one attribute rather than two styles.
      data-focused={focused ? "" : undefined}
      data-stance={mark?.stance}
      tabIndex={-1}
      ref={register}
      onFocus={onFocus}
      aria-label={`${row.kind.label}: ${row.claim}`}
    >
      <Link className="decide-claim untrusted-inline" to={row.href}>
        {row.claim}
      </Link>
      <span className="decide-meta">
        <Badge label={row.kind.label} tone={row.kind.tone} />
        <span className="decide-why" title={row.whyTitle}>{row.why}</span>
        {mark && (
          <span
            className="decide-mark"
            title={recorded ? `Recorded ${recorded.absolute}` : undefined}
          >
            you {stanceWord(mark.stance)}
          </span>
        )}
      </span>
      {/* The controls a pointer reaches for, labelled with the keys that do
          the same thing: an operator who clicks "A" twice has been told what
          to press the third time. The letters keep the reserved column narrow
          enough that the claim keeps the width of the row. */}
      {row.record && !ruling && (
        <span className="decide-acts">
          <span className="rule-bar" role="group" aria-label="Your stance on this record">
            {STANCES.map((stance) => (
              <button
                type="button"
                key={stance}
                className={mark?.stance === stance ? "active" : undefined}
                aria-pressed={mark?.stance === stance}
                aria-label={`Record that you ${stanceWord(stance)}`}
                disabled={pending}
                onClick={() => onStance(stance)}
                title={`${stance} — record that you ${stanceWord(stance)}. It decides nothing.`}
              >
                {stance.charAt(0)}
              </button>
            ))}
          </span>
          <button
            type="button"
            className="decide-rule-open"
            aria-label="Rule on this record"
            title="rule — the five dispositions, with their confirmation"
            onClick={onRule}
          >
            r
          </button>
        </span>
      )}
      {row.record && ruling && (
        <div className="decide-ruling">
          <RuleBar
            id={row.record.id}
            kind={row.record.kind}
            stance={mark?.stance}
            onActed={onActed}
          />
          <button type="button" className="decide-rule-close" onClick={onRule}>
            Close
          </button>
        </div>
      )}
      {/* The index is the row's position under the ring, so a reader who
          pressed j four times can see where he is. */}
      <span className="decide-index" aria-hidden="true">{index + 1}</span>
    </li>
  );
}

// merge flattens three stores into one order.
//
// Rank 0 is a question Babel is blocked on: it has stopped rather than guessed,
// and every other row is work that is merely waiting. Rank 1 is a decision the
// operator already made that something has changed about — cheap to rule on,
// because he has read the record before. Rank 2 is the enrolled queue. Rank 3
// is a question that is not blocking anything, which is the only group here
// that is genuinely optional.
//
// Within a rank the kind decides before the age does: at equal urgency a
// proposal outranks a finding outranks a candidate, because a proposal is a
// remedy addressed to the operator and a candidate is something Babel is
// still developing on its own. Within a kind the oldest is first, on the
// ordinary grounds that a queue nobody drains from the bottom is a queue
// with a permanent bottom — and a row that carries no enrolment time at all,
// which is every row another host committed, sorts after the dated ones
// rather than claiming the longest wait.
//
// Each row's `why` is built from the fields the row's own endpoint returned and
// from nothing else. The review queue is ordered by how long something has
// waited and how often it has been ruled on; the reconsider lane carries
// §8.5's why-now sentences; a question carries the class that decides whether
// Babel is stuck. Those are the three bases, and a row states its own.
function merge(
  queue: QueueItem[],
  questions: QuestionSummary[],
  reconsider: EvaluationItem[],
): Row[] {
  const rows: Row[] = [];

  for (const item of questions) {
    const asked = formatTime(item.created_at);
    const blocking = item.class === "blocking";
    const age = elapsed(item.created_at);
    const stuck = blocking
      ? "blocks a run"
      : item.class === "maintenance"
        ? "upkeep"
        : "curiosity";
    rows.push({
      key: `q-${item.id}`,
      rank: blocking ? 0 : 3,
      claim: item.prompt || "a question with no prompt recorded",
      href: `/ask/questions/${encodeURIComponent(item.id)}`,
      kind: { label: "Question", tone: blocking ? "amber" : "cyan" },
      kindKey: "question",
      why: [stuck, age ? `asked ${age}` : null].filter(Boolean).join(" · "),
      whyTitle: item.why_asked || asked?.absolute,
      at: item.created_at,
    });
  }

  for (const item of reconsider) {
    const subject = item.artifact.subject;
    const at = item.artifact.created_at;
    const age = elapsed(at);
    const reception = item.reception;
    const contested = reception.support > 0 && reception.oppose > 0;
    const reason = item.reasons?.[0];
    // The why-now sentence is the store's own where there is one; where there
    // is not, the lane itself is the reason and says so in two words.
    const head = contested ? "contested" : reason ? clipWords(reason, 3) : "something changed";
    const tail =
      reception.reviews > 0
        ? `${reception.reviews} ${reception.reviews === 1 ? "review" : "reviews"}`
        : age
          ? `decided ${age}`
          : null;
    rows.push({
      key: `x-${subject.kind}-${subject.id}`,
      rank: 1,
      claim: item.artifact.title || "a record with no title recorded",
      href: `/r/${encodeURIComponent(subject.id)}`,
      kind: { label: kindLabel(subject.kind), tone: "violet" },
      kindKey: subject.kind,
      why: [head, tail].filter(Boolean).join(" · "),
      whyTitle: reason,
      at,
      record: RECORD_KINDS[subject.kind]
        ? { id: subject.id, kind: subject.kind as RecordKind }
        : undefined,
    });
  }

  for (const item of queue) {
    const enrolled = formatTime(item.enrolled_at);
    const age = elapsed(item.enrolled_at);
    // A merged row arrives without the append-only decision history, which is
    // derived beside the record and does not travel with it. Its ruling count
    // is absent rather than zero: a record decided on another host is not one
    // nobody has looked at.
    const derived = item.local_host !== false;
    const head = !derived
      ? "held elsewhere"
      : item.refinements > 0
        ? `refined ${item.refinements}×`
        : item.decisions === 0
          ? "never ruled on"
          : `${item.decisions} ${item.decisions === 1 ? "ruling" : "rulings"}`;
    const standing = derived && item.status && item.status !== "new" ? item.status : null;
    rows.push({
      key: `r-${item.subject.type}-${item.subject.id}`,
      rank: 2,
      claim: item.excerpt || `a ${item.subject.type} with no summary recorded`,
      href: `/r/${encodeURIComponent(item.subject.id)}`,
      kind: { label: kindLabel(item.subject.type), tone: "neutral" },
      kindKey: item.subject.type,
      why: [head, standing, age ? `waiting ${age}` : null].filter(Boolean).join(" · "),
      whyTitle: enrolled ? `Enrolled ${enrolled.absolute}` : undefined,
      at: item.enrolled_at,
      record: { id: item.subject.id, kind: item.subject.type },
    });
  }

  rows.sort((left, right) => {
    if (left.rank !== right.rank) return left.rank - right.rank;
    const weight =
      (KIND_WEIGHT[left.kindKey] ?? KIND_WEIGHT_OTHER) -
      (KIND_WEIGHT[right.kindKey] ?? KIND_WEIGHT_OTHER);
    if (weight !== 0) return weight;
    if (!left.at || !right.at) return left.at ? -1 : right.at ? 1 : 0;
    return left.at.localeCompare(right.at);
  });
  return rows;
}

export default DecidePage;
