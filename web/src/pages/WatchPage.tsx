import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  APIError,
  dismissAPIError,
  getAnalysisState,
  searchCorpus,
  type AnalysisState,
  type SearchHit,
} from "../api";
import { errorMessage, formatTime } from "../format";
import { AuthorityMark, Badge, Quoted } from "../analysis";
import { Bars, StackedBars, type Band, type Point } from "../charts";
import {
  ABSENT,
  RUN_KIND_LABELS,
  count,
  dollars,
  getWatchLive,
  getWatchRuns,
  getWatchSeries,
  launchRun,
  seconds,
  stopRun,
  type LaunchArgs,
  type LaunchKind,
  type LiveRun,
  type WatchLive,
  type WatchRunRow,
  type WatchSeries,
} from "../watchapi";
import "../watch.css";

// Watch is the control room and the observatory (SPEC.md §8.4 as revised by
// decision 90, and §8.6's register: observatory where Babel is watched).
//
// It answers three questions in this order, which is the order an operator
// asks them: what is happening right now, what do I want to happen next, and
// what has this deployment been producing and spending. The first is a live
// strip, the second is a launch form, the third is small multiples over thirty
// days — and a table of receipts underneath, each row opening the half of a run
// that never reached any page.
//
// Two things this page deliberately does not have.
//
// It has no machine dimension. The scope tabs it used to carry — "This
// machine" / "Every machine" — asked one question twice, and the operator
// ruled the machine out of the reading path: a run in flight is a run in
// flight, whichever host's process table holds it. The one place a host
// remains a legitimate subject is a backup *of* a machine, which is Settings ›
// Archive. What survives from the fleet view is the honesty it was built for:
// a row is a claim a process made at a moment, so every card states how old
// its last word is rather than painting a green light over an unknown.
//
// It has no "exploration is not startable here" box. Starting runs from this
// surface is in scope, and the only thing that may refuse one is the machine's
// own configuration — so the refusal is the server's own sentence, shown when
// the operator asks for a run, beside the link to the setting that lifts it.

const LIVE_POLL_MS = 5_000;
const SERIES_DAYS = 30;
const RUNS_LIMIT = 50;

// FRESHNESS_NOTE says what each classification does and does not establish.
// "lost" is the one that matters: nothing observed a death, and the interface
// never renders it as one.
const FRESHNESS_NOTE: Record<string, string> = {
  fresh: "Heartbeat seconds old.",
  recent: "Last word a minute or two ago.",
  stale: "No word recently. Running or finished — this host cannot tell.",
  lost: "Nothing heard for a long time. That is not the same as dead.",
};

// The four record kinds a day's output is composed of, in the order they
// stack: a hypothesis is the cheapest thing a run can make and a proposal the
// most considered, so the column reads bottom-up as work maturing.
const RECORD_BANDS: Array<{ key: "hypothesis" | "observation" | "finding" | "proposal"; label: string; color: string }> = [
  { key: "hypothesis", label: "hypotheses", color: "var(--accent)" },
  { key: "observation", label: "observations", color: "var(--info)" },
  { key: "finding", label: "findings", color: "var(--good)" },
  { key: "proposal", label: "proposals", color: "var(--warn)" },
];

interface LaunchField {
  key: keyof LaunchArgs;
  label: string;
  type: "text" | "number" | "bool";
  placeholder?: string;
  hint: string;
  // An identifier needs room a number does not.
  wide?: boolean;
  // The server refuses the launch without it, so the form refuses first.
  required?: boolean;
}

// The launch forms, one per startable kind. Every field is one of the CLI's own
// flags under its own name, because this is the same capability and not a
// second one: an operator who knows `babel conductor run --until 60m
// --concurrent 3` recognises this form, and a receipt cannot record that a run
// was started from a browser under different words.
//
// The field sets are exactly what each kind's command accepts. That is a
// constraint rather than a style: the launch route refuses an argument the
// chosen kind has no flag for, so a form offering `--until` to an exploration
// would be offering a refusal.
//
// Nothing here carries a default. A blank field is not sent at all, so the
// machine's configured setting decides — which is the difference between "the
// operator asked for one concurrent cycle" and "the operator said nothing
// about concurrency".
const LAUNCH_FORMS: Array<{
  kind: LaunchKind;
  blurb: string;
  fields: LaunchField[];
}> = [
  {
    kind: "explore",
    blurb: "One exploration pass over a corpus scope that already exists.",
    fields: [
      { key: "preparation", label: "Preparation", type: "text", placeholder: "prep_…", wide: true, required: true, hint: "The corpus scope to explore. Fixed by `babel prepare`; a recent one is on any run's receipt." },
      { key: "recipe", label: "Recipe", type: "text", placeholder: "recipe id", wide: true, hint: "One cookbook recipe to run. Blank runs the enabled default set." },
      { key: "develop", label: "Develop", type: "number", placeholder: "0", hint: "Cap the candidates developed in this pass." },
      { key: "retrievals", label: "Retrievals", type: "number", placeholder: "0", hint: "Cap the corpus searches served." },
      { key: "fetches", label: "Fetches", type: "number", placeholder: "0", hint: "Cap the public documents fetched." },
      { key: "challenge", label: "Challenge", type: "bool", hint: "Run the independent challenger pass." },
      { key: "synthesize", label: "Synthesize", type: "bool", hint: "Run the synthesis pass, which is what promotes findings. Needs the challenger." },
    ],
  },
  {
    kind: "evaluate",
    blurb: "One review, drawn from the coverage inventory under the evaluation policy. Two reviews are two launches.",
    fields: [
      { key: "retrievals", label: "Retrievals", type: "number", placeholder: "0", hint: "Cap the corpus searches this review serves." },
      { key: "fetches", label: "Fetches", type: "number", placeholder: "0", hint: "Cap the public documents this review fetches." },
      { key: "correct", label: "Re-review", type: "text", placeholder: "record id", wide: true, hint: "Re-review one record and supersede the statement made about it. Blank draws from the inventory." },
    ],
  },
  {
    kind: "conductor",
    blurb: "The loop: cycles until the clock runs out, with a share spent on review and consolidation.",
    fields: [
      { key: "until", label: "Until", type: "text", placeholder: "60m", hint: "A duration, a time today, or an RFC3339 timestamp." },
      { key: "concurrent", label: "Concurrent", type: "number", placeholder: "1", hint: "Run this many cycles at a time." },
      { key: "evaluate", label: "Evaluate 1 in", type: "number", placeholder: "0", hint: "Guarantee one evaluation cycle in every N." },
      { key: "consolidate", label: "Consolidate 1 in", type: "number", placeholder: "0", hint: "Guarantee one consolidation cycle in every N. Needs the challenger and the synthesizer." },
      { key: "challenge", label: "Challenge", type: "bool", hint: "Run the challenger over each cycle's exploration." },
      { key: "synthesize", label: "Synthesize", type: "bool", hint: "Run the synthesizer, which is what promotes findings. Needs the challenger." },
      { key: "once", label: "Once", type: "bool", hint: "Run exactly one cycle and stop." },
    ],
  },
];

// The two dependencies the CLI enforces inside the child process rather than at
// the launch boundary. Checked here because the alternative is a child that
// exits with the reason in its own log and a live strip that shows a run
// appearing and vanishing: the operator would have to read a process log to
// learn that a dial he set is not allowed on its own.
function unmetDependency(args: LaunchArgs): string | null {
  if (args.synthesize && !args.challenge) {
    return "The synthesizer runs over the challenger's output, so Challenge has to be on too.";
  }
  if ((args.consolidate ?? 0) > 0 && !(args.challenge && args.synthesize)) {
    return "Consolidation reads challenged, synthesized cycles, so Challenge and Synthesize both have to be on.";
  }
  return null;
}

const HARNESSES = ["omp", "codex", "claude-code"];

// LiveCard is one run in flight. Everything on it is either something the run
// said or something this page can compute from what it said: elapsed ticks
// client-side from the start time, and the figures are absent rather than zero
// until the run reports them.
function LiveCard({
  run,
  now,
  stopping,
  onStop,
}: {
  run: LiveRun;
  now: number;
  stopping: boolean;
  onStop: (run: LiveRun) => void;
}) {
  const started = Date.parse(run.started_at);
  const elapsed = Number.isFinite(started) ? seconds(Math.max(0, (now - started) / 1000)) : ABSENT;
  const alive = run.freshness === "fresh" || run.freshness === "recent" || run.freshness === undefined;
  const note = run.freshness ? FRESHNESS_NOTE[run.freshness] : undefined;
  const heartbeat = run.heartbeat_age_s == null ? null : seconds(run.heartbeat_age_s);

  return (
    <article className="panel live-card">
      <header className="live-card-head">
        <span className="live-card-kind">
          {alive && <span className="live-dot" aria-hidden="true" />}
          {RUN_KIND_LABELS[run.kind ?? ""] ?? run.kind ?? "Run"}
        </span>
        {run.state && <Badge label={run.state} tone={run.state === "running" ? "green" : "neutral"} />}
      </header>

      <p className="live-card-stage">{run.stage || "no stage reported"}</p>

      <div className="live-card-figures">
        <div className="stat">
          <span className="stat-label">elapsed</span>
          <strong className="stat-value">{elapsed}</strong>
        </div>
        <div className="stat">
          <span className="stat-label">spend</span>
          <strong className="stat-value">{dollars(run.spend_usd)}</strong>
        </div>
        <div className="stat">
          <span className="stat-label">records</span>
          <strong className="stat-value">{count(run.records)}</strong>
        </div>
      </div>

      <p className="live-card-word">
        {heartbeat ? `Last word ${heartbeat} ago.` : "No heartbeat recorded yet."}
        {note ? ` ${note}` : ""}
      </p>

      <footer className="live-card-foot">
        <Link className="live-card-open" to={`/watch/runs/${encodeURIComponent(run.run_id)}`}>
          Open the receipt →
        </Link>
        {run.authority && <AuthorityMark authority={run.authority} />}
        {/* Stop is offered only where it can actually act: a run this server
            launched, whose child process it holds. A row from another machine
            carries no pid and is not stoppable, and a button that posted a
            number from another host's process table would stop whatever
            happens to hold that number here. */}
        {run.stoppable && run.pid != null && (
          <button
            type="button"
            className="danger-button live-card-stop"
            disabled={stopping}
            onClick={() => onStop(run)}
            title="Ask this run to stop at its next safe point"
          >
            {stopping && <span className="spinner small" />}
            {stopping ? "Stopping…" : "Stop"}
          </button>
        )}
      </footer>
    </article>
  );
}

// SeriesPanel is one of the four small multiples: a label, the last day's
// figure as the readable number, the shape of thirty days, and one line saying
// what the window holds.
function SeriesPanel({
  label,
  value,
  note,
  footer,
  children,
}: {
  label: string;
  value: string;
  note?: string;
  footer: string;
  children: ReactNode;
}) {
  return (
    <article className="panel series-panel">
      <div className="stat">
        <span className="stat-label">{label}</span>
        <strong className="stat-value">{value}</strong>
        {note && <span className="stat-note">{note}</span>}
      </div>
      {children}
      <p className="series-footer">{footer}</p>
    </article>
  );
}

function WatchPage() {
  const [live, setLive] = useState<WatchLive | null>(null);
  const [liveError, setLiveError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [stoppingPid, setStoppingPid] = useState<number | null>(null);
  const [stopNote, setStopNote] = useState<string | null>(null);

  const [series, setSeries] = useState<WatchSeries | null>(null);
  const [seriesError, setSeriesError] = useState<string | null>(null);

  const [runs, setRuns] = useState<WatchRunRow[] | null>(null);
  const [runsError, setRunsError] = useState<string | null>(null);

  const [launchKind, setLaunchKind] = useState<LaunchKind>("conductor");
  const [entries, setEntries] = useState<Record<string, string>>({});
  const [launching, setLaunching] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const [launchError, setLaunchError] = useState<string | null>(null);
  const [launched, setLaunched] = useState<string | null>(null);

  const [analysis, setAnalysis] = useState<AnalysisState | null>(null);

  const [searchText, setSearchText] = useState("");
  const [harness, setHarness] = useState<string | null>(null);
  const [hits, setHits] = useState<SearchHit[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState<string | null>(null);

  const loadLive = useCallback(async () => {
    try {
      const value = await getWatchLive();
      setLive(value);
      setLiveError(null);
    } catch (reason) {
      setLiveError(errorMessage(reason));
    }
  }, []);

  // The live read polls, and it polls only while somebody is looking: a tab
  // left open overnight would otherwise spend a request every five seconds on
  // a page nobody is reading, against a server whose whole job is to be cheap
  // enough to leave running. Coming back to the tab reads immediately rather
  // than waiting out the interval, because the first thing a returning
  // operator wants is the current state and not the five-second-old one.
  useEffect(() => {
    let mounted = true;
    let timer = 0;
    const schedule = () => {
      window.clearTimeout(timer);
      if (mounted) timer = window.setTimeout(tick, LIVE_POLL_MS);
    };
    async function tick() {
      if (!mounted) return;
      if (document.visibilityState === "visible") await loadLive();
      schedule();
    }
    void tick();
    const onVisibility = () => {
      if (document.visibilityState === "visible") void tick();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      mounted = false;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [loadLive]);

  const running = live?.runs.length ?? 0;

  // Elapsed ticks locally rather than arriving from the server, so a card
  // counts up smoothly between five-second reads. The clock runs only while
  // something is on it.
  useEffect(() => {
    if (running === 0) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [running]);

  useEffect(() => {
    let mounted = true;
    getWatchSeries(SERIES_DAYS)
      .then((value) => {
        if (mounted) setSeries(value);
      })
      .catch((reason) => {
        if (mounted) setSeriesError(errorMessage(reason));
      });
    return () => {
      mounted = false;
    };
  }, []);

  const loadRuns = useCallback(async () => {
    try {
      const value = await getWatchRuns(RUNS_LIMIT);
      setRuns(value.runs ?? []);
      setRunsError(null);
    } catch (reason) {
      setRunsError(errorMessage(reason));
    }
  }, []);

  useEffect(() => {
    void loadRuns();
  }, [loadRuns]);

  useEffect(() => {
    let mounted = true;
    getAnalysisState()
      .then((value) => {
        if (mounted) setAnalysis(value);
      })
      .catch(() => undefined);
    return () => {
      mounted = false;
    };
  }, []);

  const form = LAUNCH_FORMS.find((entry) => entry.kind === launchKind) ?? LAUNCH_FORMS[0];

  async function stop(run: LiveRun) {
    if (run.pid == null) return;
    const label = RUN_KIND_LABELS[run.kind ?? ""] ?? "run";
    if (
      !window.confirm(
        `Stop this ${label.toLowerCase()}?\n\nIt stops at its next safe point and keeps everything it ` +
          `has already committed. Nothing is killed, so a cycle in flight finishes writing.`,
      )
    ) {
      return;
    }
    setStoppingPid(run.pid);
    setStopNote(null);
    try {
      const result = await stopRun(run.pid);
      setStopNote(result.detail ?? "Stop requested. The run ends at its next safe point.");
      await loadLive();
    } catch (reason) {
      setStopNote(errorMessage(reason));
    } finally {
      setStoppingPid(null);
    }
  }

  async function submitLaunch(event: FormEvent) {
    event.preventDefault();
    const args: LaunchArgs = {};
    for (const field of form.fields) {
      const raw = entries[`${form.kind}.${field.key}`] ?? "";
      if (field.type === "bool") {
        if (raw === "on") Object.assign(args, { [field.key]: true });
        continue;
      }
      const text = raw.trim();
      if (!text) continue;
      if (field.type === "number") {
        const value = Number(text);
        if (!Number.isFinite(value)) continue;
        Object.assign(args, { [field.key]: value });
        continue;
      }
      // `recipe` is repeatable on the command line, so it travels as a list
      // even when the operator named one.
      Object.assign(args, { [field.key]: field.key === "recipe" ? [text] : text });
    }

    const unmet = unmetDependency(args);
    if (unmet) {
      setLaunchError(unmet);
      setRefusal(null);
      setLaunched(null);
      return;
    }

    setLaunching(true);
    setRefusal(null);
    setLaunchError(null);
    setLaunched(null);
    try {
      const result = await launchRun({ kind: form.kind, args });
      // A run outlives the request that created it, so this reports what was
      // started and then hands the operator to the live strip rather than
      // pretending the launch is the run.
      const named = result.run_id ? `run ${result.run_id}` : "a run that has not named itself yet";
      const pid = result.pid == null ? "" : ` (pid ${result.pid})`;
      setLaunched(result.detail ?? `Started ${named}${pid}. It appears above as soon as it announces.`);
      await loadLive();
    } catch (reason) {
      // 409 is the machine refusing under its own configuration — no ceilings,
      // no authorized review, no worker. The server's sentence is the whole
      // message, rendered verbatim, because it names the remedy and this page
      // must not paraphrase a refusal it did not author.
      if (reason instanceof APIError && reason.status === 409) {
        setRefusal(reason.message);
        // The shell's banner reports a request that failed, and this one did
        // not fail: the machine answered, correctly, that it is not configured
        // to do this. Showing both puts the same sentence on the page twice —
        // once as prose with its remedy, once as a collapsed single line — so
        // the page that can render it properly takes ownership of it.
        dismissAPIError();
      } else setLaunchError(errorMessage(reason));
    } finally {
      setLaunching(false);
    }
  }

  async function submitSearch(event: FormEvent) {
    event.preventDefault();
    const q = searchText.trim();
    if (!q) return;
    setSearching(true);
    setSearchError(null);
    try {
      const response = await searchCorpus({ q, harness: harness ?? undefined, limit: 20 });
      setHits(response.hits);
    } catch (reason) {
      setSearchError(errorMessage(reason));
    } finally {
      setSearching(false);
    }
  }

  // Stable across renders so the band and title memos below actually memoize:
  // `series?.days ?? []` is a fresh array every render, and the second poll of
  // the live read would otherwise rebuild thirty days of chart geometry.
  const days = useMemo(() => series?.days ?? [], [series]);
  const last = days.length > 0 ? days[days.length - 1] : null;

  const bands: Band[] = useMemo(
    () =>
      RECORD_BANDS.map((band) => ({
        key: band.key,
        label: band.label,
        color: band.color,
        values: days.map((day) => day.records?.[band.key] ?? 0),
      })),
    [days],
  );

  const dayTitles = useMemo(
    () =>
      days.map((day) => {
        const parts = RECORD_BANDS.map((band) => `${day.records?.[band.key] ?? 0} ${band.label}`);
        return `${day.day} — ${parts.join(", ")}`;
      }),
    [days],
  );

  const reviewValues: Point[] = days.map((day) => day.reviews);
  const sessionValues: Point[] = days.map((day) => day.sessions);
  const spendValues: Point[] = days.map((day) => day.spend_usd ?? null);

  const recordTotal = days.reduce(
    (sum, day) => sum + RECORD_BANDS.reduce((inner, band) => inner + (day.records?.[band.key] ?? 0), 0),
    0,
  );
  const lastRecordTotal = last ? RECORD_BANDS.reduce((sum, band) => sum + (last.records?.[band.key] ?? 0), 0) : null;
  const reviewTotal = days.reduce((sum, day) => sum + (day.reviews ?? 0), 0);
  const sessionTotal = days.reduce((sum, day) => sum + (day.sessions ?? 0), 0);
  const spendKnown = days.filter((day) => day.spend_usd != null);
  const spendTotal = spendKnown.reduce((sum, day) => sum + (day.spend_usd ?? 0), 0);

  const drain = live?.drain;
  const drainAt = formatTime(drain?.last_at);
  const cookbook = analysis?.cookbook ?? [];

  return (
    <section className="page watch-page">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Control room</p>
          <h1>
            {running > 0 && <span className="live-dot" aria-hidden="true" />}
            {running === 0
              ? "Nothing is running"
              : `${running} ${running === 1 ? "run" : "runs"} in flight`}
          </h1>
        </div>
        {drain && (
          <p className="watch-drain" title="Publication is automatic: every run's output is sealed and published as the process exits.">
            {count(drain.published)} published · {count(drain.sealed)} sealed · {count(drain.pending)} pending
            {drainAt && (
              <>
                {" · last attempt "}
                <time dateTime={drain.last_at} title={drainAt.absolute}>
                  {drainAt.relative}
                </time>
              </>
            )}
          </p>
        )}
      </div>

      {liveError && (
        <div className="surface state-note error-state" role="status">
          <strong>What is running could not be read.</strong>
          <span>{liveError}</span>
          <button type="button" onClick={() => void loadLive()}>
            Try again
          </button>
        </div>
      )}

      {running > 0 && (
        <div className="live-strip">
          {live?.runs.map((run) => (
            <LiveCard
              key={`${run.run_id}:${run.pid ?? "remote"}`}
              run={run}
              now={now}
              stopping={stoppingPid != null && stoppingPid === run.pid}
              onStop={stop}
            />
          ))}
        </div>
      )}

      {stopNote && (
        <p className="watch-note" role="status">
          {stopNote}
        </p>
      )}

      <article className="surface launch-surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Start</p>
            <h2>Ask for a run</h2>
          </div>
        </div>

        <div className="rule-bar" role="group" aria-label="What to start">
          {LAUNCH_FORMS.map((entry) => (
            <button
              type="button"
              key={entry.kind}
              className={entry.kind === form.kind ? "active" : undefined}
              aria-pressed={entry.kind === form.kind}
              onClick={() => {
                setLaunchKind(entry.kind);
                setRefusal(null);
                setLaunchError(null);
                setLaunched(null);
              }}
            >
              {RUN_KIND_LABELS[entry.kind]}
            </button>
          ))}
        </div>

        <p className="launch-blurb">{form.blurb}</p>

        <form className="launch-form" onSubmit={submitLaunch}>
          <div className="launch-fields">
            {form.fields.map((field) => {
              const id = `launch-${form.kind}-${String(field.key)}`;
              const value = entries[`${form.kind}.${String(field.key)}`] ?? "";
              if (field.type === "bool") {
                return (
                  <label className="launch-field launch-toggle" key={id} htmlFor={id} title={field.hint}>
                    <input
                      id={id}
                      type="checkbox"
                      checked={value === "on"}
                      onChange={(event) =>
                        setEntries((current) => ({
                          ...current,
                          [`${form.kind}.${String(field.key)}`]: event.target.checked ? "on" : "",
                        }))
                      }
                    />
                    <span>{field.label}</span>
                  </label>
                );
              }
              return (
                <label
                  className={field.wide ? "launch-field launch-wide" : "launch-field"}
                  key={id}
                  htmlFor={id}
                  title={field.hint}
                >
                  <span className="launch-label">{field.label}</span>
                  <input
                    id={id}
                    type={field.type === "number" ? "number" : "text"}
                    inputMode={field.type === "number" ? "numeric" : undefined}
                    min={field.type === "number" ? 0 : undefined}
                    value={value}
                    placeholder={field.placeholder}
                    required={field.required}
                    autoComplete="off"
                    onChange={(event) =>
                      setEntries((current) => ({
                        ...current,
                        [`${form.kind}.${String(field.key)}`]: event.target.value,
                      }))
                    }
                  />
                </label>
              );
            })}
          </div>
          <button type="submit" className="launch-submit" disabled={launching}>
            {launching && <span className="spinner small" />}
            {launching ? "Starting…" : `Start ${RUN_KIND_LABELS[form.kind].toLowerCase()}`}
          </button>
        </form>

        <p className="launch-footnote">
          A run started here is the run the terminal starts: the same ceilings, the same profile, the same
          grants, receipted the same way, attributed to you.
        </p>

        {/* The one machine state worth saying before the operator asks for a
            run: a machine with no durable analysis storage records nothing a
            run produces. It is one line rather than the empty-state card the
            page it replaced carried, because it is a configuration fact and
            not a failure. */}
        {analysis && !analysis.configured && (
          <p className="launch-footnote">
            Durable analysis storage is not configured here, so nothing a run produces would be recorded.
            Run <code>babel storage configure</code> first.
          </p>
        )}

        {refusal && (
          <div className="surface state-note launch-refusal" role="alert">
            <strong>This machine refused to start it</strong>
            <span className="launch-verbatim">{refusal}</span>
            <Link to="/settings?section=ceilings">Open Settings › Ceilings →</Link>
          </div>
        )}
        {launchError && (
          <p className="inline-error" role="alert">
            {launchError}
          </p>
        )}
        {launched && (
          <p className="watch-note" role="status">
            {launched}
          </p>
        )}
      </article>

      <article className="surface series-surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Thirty days</p>
            <h2>What it produced, and what it cost</h2>
          </div>
          <span className="count-label">{days.length} days</span>
        </div>

        {seriesError && !series && (
          <div className="surface state-note error-state" role="status">
            <strong>The daily series could not be read.</strong>
            <span>{seriesError}</span>
          </div>
        )}

        {series && days.length === 0 && (
          <p className="muted">No day in the window carries a record, a review or a session.</p>
        )}

        {days.length > 0 && (
          <>
            <div className="series-multiples">
              <SeriesPanel
                label="records"
                value={count(lastRecordTotal)}
                note={last ? `on ${last.day}` : undefined}
                footer={`${count(recordTotal)} over ${days.length} days`}
              >
                <StackedBars
                  bands={bands}
                  titles={dayTitles}
                  label="Records published per day, by kind"
                  height="3rem"
                />
                <ul className="series-legend">
                  {RECORD_BANDS.map((band) => (
                    <li key={band.key}>
                      <span className="series-swatch" style={{ background: band.color }} aria-hidden="true" />
                      {band.label}
                    </li>
                  ))}
                </ul>
              </SeriesPanel>

              <SeriesPanel
                label="reviews"
                value={count(last?.reviews)}
                note={last ? `on ${last.day}` : undefined}
                footer={`${count(reviewTotal)} over ${days.length} days`}
              >
                <Bars
                  values={reviewValues}
                  titles={days.map((day) => `${day.day} — ${day.reviews} reviews`)}
                  label="Reviews per day"
                  color="var(--accent)"
                  height="3rem"
                />
              </SeriesPanel>

              <SeriesPanel
                label="sessions"
                value={count(last?.sessions)}
                note={last ? `on ${last.day}` : undefined}
                footer={`${count(sessionTotal)} over ${days.length} days`}
              >
                <Bars
                  values={sessionValues}
                  titles={days.map((day) => `${day.day} — ${day.sessions} sessions`)}
                  label="Sessions recorded per day"
                  color="var(--info)"
                  height="3rem"
                />
              </SeriesPanel>

              {/* Spend's gaps are the honest part. A day whose receipts this
                  machine does not hold is unknown, not free, so it draws as a
                  faint tick and is excluded from the total — which is why the
                  footer says how many days the figure covers. */}
              <SeriesPanel
                label="spend"
                value={dollars(last?.spend_usd)}
                note={last ? `on ${last.day}` : undefined}
                footer={
                  spendKnown.length === days.length
                    ? `${dollars(spendTotal)} over ${days.length} days`
                    : `${dollars(spendTotal)} over the ${spendKnown.length} of ${days.length} days with receipts here`
                }
              >
                <Bars
                  values={spendValues}
                  titles={days.map((day) =>
                    day.spend_usd == null
                      ? `${day.day} — no receipt on this machine says`
                      : `${day.day} — ${dollars(day.spend_usd)}`,
                  )}
                  label="Spend per day"
                  color="var(--warn)"
                  height="3rem"
                />
              </SeriesPanel>
            </div>
          </>
        )}
      </article>

      <article className="surface runs-surface">
        <div className="section-heading">
          <div>
            <p className="eyebrow">Receipts</p>
            <h2>Recent runs</h2>
          </div>
          {runs && <span className="count-label">{runs.length}</span>}
        </div>

        {runsError && !runs && (
          <div className="surface state-note error-state" role="status">
            <strong>The run receipts could not be read.</strong>
            <span>{runsError}</span>
            <button type="button" onClick={() => void loadRuns()}>
              Try again
            </button>
          </div>
        )}

        {runs && runs.length === 0 && (
          <p className="muted">
            No run has recorded a receipt yet. A receipt appears here as soon as one is written — including a
            failed run's, which keeps its receipt too.
          </p>
        )}

        {runs && runs.length > 0 && (
          <div className="table-scroll">
            <table className="runs-table">
              <thead>
                <tr>
                  <th>Run</th>
                  <th>Recorded</th>
                  <th>Why</th>
                  <th className="numeric">Duration</th>
                  <th className="numeric">Cost</th>
                  <th className="numeric">Outputs</th>
                  <th className="numeric">Retrievals</th>
                  <th className="numeric">Failures</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((row) => {
                  const recorded = formatTime(row.recorded_at);
                  return (
                    <tr key={row.receipt_id || row.run_id}>
                      <td>
                        <Link className="runs-open" to={`/watch/runs/${encodeURIComponent(row.run_id)}`}>
                          <span className="mono">{row.run_id}</span>
                        </Link>
                        <span className="secondary">
                          {RUN_KIND_LABELS[row.kind ?? ""] ?? "kind not recorded"}
                          {row.revision != null && row.revision > 1 ? ` · rev ${row.revision}` : ""}
                        </span>
                      </td>
                      <td>
                        {recorded ? (
                          <time dateTime={row.recorded_at} title={recorded.absolute}>
                            {recorded.relative}
                          </time>
                        ) : (
                          <span className="muted">{ABSENT}</span>
                        )}
                      </td>
                      <td>
                        <AuthorityMark authority={row.authority} />
                      </td>
                      <td className="numeric mono">{seconds(row.duration_s)}</td>
                      <td className="numeric mono">{dollars(row.cost_usd)}</td>
                      <td className="numeric mono" title={row.outputs == null ? "The record index cannot answer for this run." : undefined}>
                        {count(row.outputs)}
                      </td>
                      <td className="numeric mono">{count(row.counts?.retrieval)}</td>
                      <td className="numeric mono">
                        {row.counts?.failures ? (
                          <span className="runs-failures">{row.counts.failures}</span>
                        ) : (
                          count(row.counts?.failures)
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </article>

      {/* The cookbook and the corpus search were two panels on the page Watch
          replaced. They are the machinery behind a run rather than the state of
          one, so they are peeled: present, addressable, and not in the way of
          the three questions above. */}
      <details className="peel">
        <summary>
          Recipes<span className="peel-count">{cookbook.length}</span>
        </summary>
        <div className="peel-body">
          <p className="muted">
            Versioned, reviewable investigation guidance. A recipe structures exploration without
            constraining what discovery may propose; a draft is simply not enabled by default.
          </p>
          {cookbook.length === 0 ? (
            <p className="muted">No cookbook assets are loaded.</p>
          ) : (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Recipe</th>
                    <th>Kind</th>
                    <th>Enabled</th>
                    <th>Scope</th>
                    <th>Stages</th>
                  </tr>
                </thead>
                <tbody>
                  {cookbook.map((recipe) => (
                    <tr key={recipe.id}>
                      <td>
                        <strong>{recipe.title}</strong>
                        <span className="secondary mono">
                          {recipe.id} · v{recipe.version}
                        </span>
                      </td>
                      <td>
                        <Badge label={recipe.kind} tone={recipe.kind === "lens" ? "cyan" : "neutral"} />
                      </td>
                      <td>
                        {recipe.default ? <Badge label="default" tone="green" /> : <Badge label="draft" tone="neutral" />}
                      </td>
                      <td>{recipe.scope.join(", ")}</td>
                      <td>{recipe.stages.join(", ")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </details>

      <details className="peel">
        <summary>Search the corpus</summary>
        <div className="peel-body">
          <p className="muted">
            Full-text matches over normalized archive events. Results are matches, not rankings: the API
            carries no relevance score, and position in this list says nothing about evidence strength.
          </p>
          <form className="search-form" onSubmit={submitSearch}>
            <label className="search-field">
              <span className="sr-only">Search the corpus</span>
              <input
                type="search"
                value={searchText}
                onChange={(event) => setSearchText(event.target.value)}
                placeholder="Search normalized events…"
                autoComplete="off"
              />
            </label>
            <div className="filter-chips" aria-label="Filter by harness">
              <button type="button" className={!harness ? "chip active" : "chip"} onClick={() => setHarness(null)}>
                All
              </button>
              {HARNESSES.map((name) => (
                <button
                  type="button"
                  className={harness === name ? "chip active" : "chip"}
                  onClick={() => setHarness(name)}
                  key={name}
                >
                  {name}
                </button>
              ))}
            </div>
            <button type="submit" disabled={searching || !searchText.trim()}>
              {searching && <span className="spinner small" />}
              {searching ? "Searching…" : "Search"}
            </button>
          </form>

          {searchError && (
            <p className="inline-error" role="alert">
              Search failed: {searchError}
            </p>
          )}
          {hits !== null &&
            !searchError &&
            (hits.length === 0 ? (
              <p className="muted">No events matched.</p>
            ) : (
              <ul className="hit-list">
                {hits.map((hit) => {
                  const time = formatTime(hit.time);
                  return (
                    <li className="hit-entry" key={`${hit.selector}-${hit.index}`}>
                      <div className="hit-heading">
                        <span className="harness-badge">{hit.harness}</span>
                        <span className="kind-label">{hit.kind}</span>
                        {hit.role && <span className="kind-label">{hit.role}</span>}
                        {hit.partial && <Badge label="torn record" tone="amber" />}
                        {time && (
                          <time dateTime={hit.time} title={time.absolute}>
                            {time.relative}
                          </time>
                        )}
                      </div>
                      <Quoted label="Archive excerpt — untrusted, bounded" text={hit.text}>
                        <div className="hit-footing">
                          <span className="evidence-locator mono">
                            {hit.locator.path}
                            {hit.locator.line > 0 ? `:${hit.locator.line}` : ""}
                          </span>
                          <Link className="evidence-open" to={`/sessions/${encodeURIComponent(hit.selector)}`}>
                            Open session →
                          </Link>
                        </div>
                      </Quoted>
                    </li>
                  );
                })}
              </ul>
            ))}
        </div>
      </details>
    </section>
  );
}

export default WatchPage;
