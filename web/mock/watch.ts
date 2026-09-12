// The Watch surface: what is running, what thirty days produced, one run's
// receipt, and the spend ceilings (Contract W).
//
// It is its own file for ./phaseb.ts's reason: the live strip needs a body of
// state nothing else needs — thirty-one concurrent runs across four freshness
// classifications, a thirty-day series with gaps in it, and a conductor
// configuration that starts unset so the refusal and the remedy can both be
// walked in one browser.
//
// The fixtures are shaped by what the real server can actually answer, and two
// of its rules are the whole point of previewing this page here.
//
// An unrecorded figure is absent, never zero. A run in flight has no receipt
// yet, so it has no spend at all — not $0.00 — and a day whose receipts this
// machine does not hold has no cost, which draws as a gap. Every fixture below
// omits rather than zeroes, so the interface is exercised against the absence
// it has to render.
//
// Freshness grades the evidence and never the process. A row says a run was
// alive at its last heartbeat and nothing about now, so the fixture carries
// runs the synthetic host has not heard from in hours: they are not counted as
// running and they are not reported as dead, and the page has to be walkable
// in exactly that state.

import type {
  Ceilings,
  LaunchResult,
  LiveRun,
  RunDetail,
  SeriesDay,
  StopResult,
  WatchRunRow,
} from "../src/watchapi";

function json(body: unknown, status = 200): Response {
  return Response.json(body, { status, headers: { "Cache-Control": "no-store" } });
}

// MOCK_WATCH=busy (default) is the deployment the walk met: thirty-one runs in
// the shared catalog, most of them the same recipe, fifteen of them silent for
// hours. `quiet` is four runs and no silence, which is what the strip looks
// like when cards are the whole answer; `idle` is nothing running at all.
const mode = Bun.env.MOCK_WATCH ?? "busy";

const RECIPES = [
  "code-health-comprehensibility",
  "outcome-integrity",
  "mechanization-audit",
  "tooling-friction",
  "decision-provenance",
];

// One synthetic run in flight. The shape is the wire's: what the run said
// about itself, plus the two facts only this server holds — the pid of a child
// it started, and whether it can stop it.
function announced(index: number, freshness: string, age: number, started: number): LiveRun {
  const recipe = RECIPES[index % RECIPES.length];
  const kind = index % 7 === 0 ? "conductor" : index % 5 === 0 ? "evaluate" : "explore";
  return {
    run_id: `run_${(0x5f3c00 + index * 7919).toString(16)}`,
    kind,
    started_at: new Date(Date.now() - started * 1000).toISOString(),
    stage: kind === "conductor" ? "cycle" : recipe,
    recipe: kind === "evaluate" ? "" : recipe,
    state: "running",
    freshness,
    heartbeat_age_s: age,
    stoppable: false,
    // A run a quarter of an hour in has written something; a young one has
    // not, and the frontier answers zero for it rather than nothing. Both are
    // measurements, so both are numbers.
    records: started > 900 ? (index % 4) + 1 : 0,
    // No receipt exists until a run ends, so spend is absent on every row
    // here. The one exception is the conductor, whose earlier cycles are
    // receipted under the same run id.
    ...(kind === "conductor" ? { spend_usd: 0.42 + index * 0.11 } : {}),
    authority: {
      kind: index % 7 === 0 ? "duty" : "operator",
      ref: index % 7 === 0 ? "duty:babel-improves-babel" : "operator:demo",
    },
  };
}

// The children this synthetic server started: one that has named itself and
// one that has not yet, which is the state of every run for the first few
// seconds of its life.
const launched: LiveRun[] = [
  {
    run_id: "run_5f3cd41a",
    kind: "conductor",
    started_at: new Date(Date.now() - 2_460_000).toISOString(),
    stage: "cycle 7 · code-health-comprehensibility",
    recipe: "code-health-comprehensibility",
    state: "running",
    freshness: "fresh",
    heartbeat_age_s: 4,
    stoppable: true,
    pid: 48231,
    records: 11,
    spend_usd: 1.94,
    authority: { kind: "operator", ref: "operator:demo" },
  },
  {
    run_id: "",
    kind: "explore",
    started_at: new Date(Date.now() - 3_000).toISOString(),
    stage: "launching",
    state: "starting",
    stoppable: true,
    pid: 48307,
  },
];

// Sixteen in flight and fifteen nothing has been heard from — the arithmetic
// the walk met, and the reason the headline, the cards, the table and the peel
// all have to agree about what "in flight" counts.
function liveRuns(): LiveRun[] {
  if (mode === "idle") return [];
  if (mode === "quiet") {
    return [
      launched[0],
      announced(1, "fresh", 6, 480),
      announced(2, "recent", 64, 1_320),
      announced(3, "fresh", 11, 240),
    ];
  }
  const rows: LiveRun[] = [...launched];
  // Thirteen this host has heard from in the last minute or two, three that
  // have gone quiet without going silent, fifteen silent for hours.
  for (let index = 0; index < 11; index += 1) {
    const fresh = index % 3 !== 0;
    rows.push(announced(index + 4, fresh ? "fresh" : "recent", fresh ? 3 + index : 50 + index * 9, 300 + index * 420));
  }
  for (let index = 0; index < 3; index += 1) {
    rows.push(announced(index + 20, "stale", 420 + index * 130, 4_800 + index * 900));
  }
  for (let index = 0; index < 15; index += 1) {
    rows.push(announced(index + 30, "lost", 9_000 + index * 1_700, 20_000 + index * 3_600));
  }
  return rows;
}

// Thirty days of output. The shape is deliberately uneven — weekends are
// quiet, one day is a burst, five days carry no receipt at all — because a
// chart of a straight line previews nothing about whether the chart is
// readable.
function seriesDays(days: number): SeriesDay[] {
  const rows: SeriesDay[] = [];
  const midnight = new Date();
  midnight.setUTCHours(0, 0, 0, 0);
  for (let back = days - 1; back >= 0; back -= 1) {
    const at = new Date(midnight.getTime() - back * 86_400_000);
    const day = at.toISOString().slice(0, 10);
    const weekend = at.getUTCDay() === 0 || at.getUTCDay() === 6;
    const burst = back === 4;
    const scale = weekend ? 0.3 : 1;
    const hypothesis = Math.round((burst ? 21 : 4 + ((back * 5) % 9)) * scale);
    const observation = Math.round((burst ? 34 : 6 + ((back * 7) % 13)) * scale);
    const finding = Math.round((burst ? 6 : back % 4) * scale);
    const proposal = Math.round((burst ? 4 : back % 3 === 0 ? 1 : 0) * scale);
    rows.push({
      day,
      records: { hypothesis, observation, finding, proposal },
      reviews: Math.round((back % 5 === 0 ? 9 : 2 + (back % 4)) * scale),
      sessions: Math.round((3 + (back % 6)) * scale),
      // Five days in the middle of the window have no local receipt, which is
      // unknown rather than free: the bar is a gap and the total says how many
      // days it covers.
      ...(back >= 11 && back <= 15 ? {} : { spend_usd: Number((burst ? 6.4 : 0.4 + ((back * 13) % 47) / 20).toFixed(2)) }),
    });
  }
  return rows;
}

// The receipts listing. Every figure a receipt cannot carry is omitted on at
// least one row: a run whose engine reported no usage has no cost, and a run
// the record index cannot answer for has no output count — which is a
// different statement from a run that wrote nothing.
function runRows(limit: number): WatchRunRow[] {
  const rows: WatchRunRow[] = [];
  for (let index = 0; index < Math.min(limit, 14); index += 1) {
    const kind = index % 6 === 0 ? "conductor" : index % 4 === 0 ? "evaluate" : "explore";
    const priced = index % 5 !== 2;
    const answered = index % 7 !== 3;
    rows.push({
      receipt_id: `rcpt_${(0x9a10 + index * 613).toString(16)}`,
      run_id: `run_${(0x4d21f0 + index * 4177).toString(16)}`,
      revision: index === 3 ? 2 : 1,
      recorded_at: new Date(Date.now() - (index + 1) * 5_400_000).toISOString(),
      kind,
      authority: {
        kind: index % 6 === 0 ? "duty" : "operator",
        ref: index % 6 === 0 ? "duty:babel-improves-babel" : "operator:demo",
      },
      counts: {
        retrieval: 4 + ((index * 3) % 17),
        deferred: index % 3,
        rejected: index % 4 === 0 ? 2 : 0,
        failures: index % 9 === 4 ? 1 : 0,
        redactions: 0,
        tool_requests: 12 + index,
        tools_denied: index % 5 === 0 ? 1 : 0,
      },
      duration_s: 120 + ((index * 97) % 900),
      ...(priced ? { cost_usd: Number((0.18 + index * 0.23).toFixed(2)) } : {}),
      ...(answered ? { outputs: (index * 3) % 9 } : {}),
    });
  }
  return rows;
}

// One receipt whole. The run page reads this, and what it has to be able to
// show is the half of a run that reaches no other page: the queries it ran,
// what it fetched, what it declined and why, and the records it produced.
function runDetail(runID: string): RunDetail {
  const seed = [...runID].reduce((sum, character) => sum + character.charCodeAt(0), 0);
  const kind = seed % 6 === 0 ? "conductor" : seed % 4 === 0 ? "evaluate" : "explore";
  const recipe = RECIPES[seed % RECIPES.length];
  const startedAt = new Date(Date.now() - 7_200_000).toISOString();
  return {
    receipt_id: `rcpt_${(seed * 977).toString(16)}`,
    run_id: runID,
    preparation_id: "prep_2f8c41d9a07b",
    revision: 1,
    recorded_at: new Date(Date.now() - 6_900_000).toISOString(),
    kind,
    authority: { kind: "operator", ref: "operator:demo" },
    counts: {
      retrieval: 3,
      deferred: 1,
      rejected: 1,
      failures: 1,
      redactions: 0,
      tool_requests: 19,
      tools_denied: 1,
    },
    timing: {
      started_at: startedAt,
      finished_at: new Date(Date.now() - 6_930_000).toISOString(),
      duration_s: 268.4,
    },
    resources: { cpu_s: 41.2, max_rss_bytes: 412_000_000, sandbox_bytes_written: 2_300_000, tool_calls: 19 },
    usage: {
      cost_usd: 1.42,
      input_tokens: 184_320,
      output_tokens: 12_884,
      profile: "code-sonnet-4.6",
      model: "claude-sonnet-4-6",
    },
    cookbook: [
      { id: recipe, version: 3, kind: "recipe" },
      { id: "contradiction-first", version: 2, kind: "lens" },
    ],
    retrieval: [
      {
        index: 1,
        at: startedAt,
        query: "copy paste multi line commands",
        tool: "corpus.search",
        scope: "prep_2f8c41d9a07b",
        results: [
          {
            rank: 1,
            evidence: {
              note: "the operator naming the friction in his own words: too hard to paste multi-line commands",
              locator: { path: "sessions/omp-2026-08-14.jsonl", line: 346, digest: "sha256:6f1c…" },
            },
          },
        ],
      },
      {
        index: 2,
        at: startedAt,
        query: "heredoc quoting failure",
        tool: "corpus.search",
        scope: "prep_2f8c41d9a07b",
        results: [],
      },
    ],
    research: [
      {
        url: "https://example.invalid/posix-sh-quoting",
        retrieved_at: startedAt,
        media_type: "text/html",
        bytes: 48_211,
        digest: "sha256:b21e…",
        truncated: false,
      },
    ],
    candidates: [
      {
        id: "hyp_declined_4a11",
        disposition: "deferred",
        at: startedAt,
        reason:
          "Two sessions mention the same friction, which is not enough to separate a habit from a defect. Deferred rather than rejected: a third instance would settle it.",
      },
    ],
    failures: [
      {
        stage: "challenge",
        code: "worker_timeout",
        at: startedAt,
        message: "the challenger pass exceeded its wall clock and was abandoned; the cycle kept what discovery had already committed",
      },
    ],
    versions: {
      capability: { sandbox: "4", tool: "6", repository: "2", public_research: "3" },
      job: { job: 7, prompt: "explore/2026-08-01", schema: "7" },
      policy: { redaction: "5", disclosure: "3" },
    },
    // The records this run produced. The listing's honest "cannot answer" case
    // is exercised by the runs table instead: here the index answers, and what
    // it answers with is what the run page has to show.
    outputs: [
      { id: "hyp_9c41d7", kind: "hypothesis", title: "Multi-line command entry is where operators lose work" },
      { id: "obs_41a7f2", kind: "observation", title: "Three sessions show the same paste failure" },
      { id: "pro_7712ba", kind: "proposal", title: "Offer a scratch script instead of a paste target" },
    ],
  };
}

// The conductor configuration this synthetic machine holds. It starts unset,
// because that is the state the launch refusal exists for and the state the
// Settings section has to be walkable in: no ceilings, a refusal with a
// remedy, and a form that lifts it.
const ceilings: Ceilings = {
  configured: false,
  currency: "USD",
  serendipity_floor: 4,
  interval_seconds: 3_600,
  slice_sessions: 3,
  consolidate_one_in: 0,
  consolidate_roots: 5,
  evaluate_one_in: 0,
  evaluate_cadence: "1h0m0s",
  babel_improves_babel: false,
  babel_tunes_itself: false,
  babel_triages_the_queue: false,
  path: "/home/demo/.config/babel/conductor.json",
};

// The command's own refusals, verbatim, flattened the way internal/cli
// flattens them for the wire. They are the product: a browser that
// paraphrased "--per-cycle 9.00 is above --per-day 5.00" would be previewing
// an interface the real server cannot produce.
const UNCONFIGURED_CONDUCTOR =
  "the conductor has no budget ceilings, so it will not run. " +
  "Autonomy here is budget-bounded, not trust-bounded: a loop that may spend without a stated " +
  "limit is a loop nobody set a limit on. Both ceilings are mandatory and neither has a default. " +
  "babel conductor configure --per-cycle 0.50 --per-day 5.00 " +
  'Everything else still works: "babel prepare" and "babel explore" run one exploration when you ' +
  "ask for one, which is what Babel did before the loop existed.";

interface CeilingBody {
  per_cycle?: number;
  per_day?: number;
  currency?: string;
  floor?: number;
  interval?: string;
  slice_sessions?: number;
  consolidate?: number;
  consolidate_roots?: number;
  evaluate?: number;
  evaluate_cadence?: string;
  babel_improves_babel?: boolean;
  babel_tunes_itself?: boolean;
  babel_triages_the_queue?: boolean;
}

// The duration `--interval` takes. The real refusal is the flag package's, so
// this mirrors its wording rather than inventing a friendlier one.
function parseDuration(text: string): number | null {
  const matched = text.trim().match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+(?:\.\d+)?)s)?$/);
  if (!matched || !matched[0]) return null;
  const [, hours, minutes, seconds] = matched;
  if (!hours && !minutes && !seconds) return null;
  return Number(hours ?? 0) * 3600 + Number(minutes ?? 0) * 60 + Number(seconds ?? 0);
}

// configure applies one `conductor configure` invocation to the stored
// document, incrementally and with the command's own validation: a field the
// request did not name changes nothing, and a configuration the command would
// refuse is refused here in the same words.
function configure(body: CeilingBody): string | null {
  const perCycle = body.per_cycle ?? ceilings.per_cycle ?? 0;
  const perDay = body.per_day ?? ceilings.per_day ?? 0;
  if (perCycle <= 0 || perDay <= 0) {
    return "the conductor refuses to run without explicit ceilings: pass --per-cycle AMOUNT and --per-day AMOUNT";
  }
  if (perCycle > perDay) {
    return `--per-cycle ${perCycle.toFixed(2)} is above --per-day ${perDay.toFixed(2)}, which would refuse every cycle`;
  }
  if (body.floor != null && body.floor < 0) return "--floor cannot be negative";
  let interval = ceilings.interval_seconds;
  if (body.interval != null) {
    const parsed = parseDuration(body.interval);
    if (parsed == null) {
      return `invalid value "${body.interval}" for flag -interval: time: invalid duration "${body.interval}"`;
    }
    interval = parsed;
  }
  ceilings.configured = true;
  ceilings.per_cycle = perCycle;
  ceilings.per_day = perDay;
  ceilings.currency = (body.currency ?? ceilings.currency ?? "USD").toUpperCase();
  if (body.floor != null && body.floor > 0) ceilings.serendipity_floor = body.floor;
  ceilings.interval_seconds = interval;
  if (body.slice_sessions != null && body.slice_sessions > 0) ceilings.slice_sessions = body.slice_sessions;
  if (body.consolidate != null) ceilings.consolidate_one_in = body.consolidate;
  if (body.consolidate_roots != null && body.consolidate_roots > 0) {
    ceilings.consolidate_roots = body.consolidate_roots;
  }
  if (body.evaluate != null) ceilings.evaluate_one_in = body.evaluate;
  if (body.evaluate_cadence != null) ceilings.evaluate_cadence = body.evaluate_cadence;
  if (body.babel_improves_babel != null) ceilings.babel_improves_babel = body.babel_improves_babel;
  if (body.babel_tunes_itself != null) ceilings.babel_tunes_itself = body.babel_tunes_itself;
  if (body.babel_triages_the_queue != null) {
    ceilings.babel_triages_the_queue = body.babel_triages_the_queue;
  }
  ceilings.configured_at = new Date().toISOString();
  return null;
}

// Children this preview started, so a launch and the stop that follows it are
// one gesture in the browser rather than two unrelated fixtures.
const started: LiveRun[] = [];
const stopped = new Set<number>();
let nextPID = 49_000;

export async function watchResponse(request: Request, url: URL): Promise<Response | null> {
  const path = url.pathname;
  if (!path.startsWith("/api/watch/")) return null;
  const rest = path.slice("/api/watch/".length);
  const { method } = request;

  if (rest === "live" && method === "GET") {
    const rows = [...started, ...liveRuns()].filter((run) => run.pid == null || !stopped.has(run.pid));
    return json({
      runs: rows,
      drain: {
        last_at: new Date(Date.now() - 34_000).toISOString(),
        published: 118,
        sealed: 3,
        pending: 2,
      },
      presence: { available: true },
      launcher: { available: true },
    });
  }

  if (rest === "series" && method === "GET") {
    const days = Math.min(90, Math.max(1, Number(url.searchParams.get("days") ?? 30)));
    return json({
      days: seriesDays(days),
      sources: { records: { available: true }, reviews: { available: true }, sessions: { available: true }, spend: { available: true } },
    });
  }

  if (rest === "runs" && method === "GET") {
    const limit = Math.min(50, Math.max(1, Number(url.searchParams.get("limit") ?? 50)));
    const rows = runRows(limit);
    return json({ runs: rows, total: rows.length });
  }

  if (rest.startsWith("runs/") && method === "GET") {
    const runID = decodeURIComponent(rest.slice("runs/".length));
    if (!runID.startsWith("run_")) return json({ error: "no receipt for that run" }, 404);
    return json(runDetail(runID));
  }

  if (rest === "ceilings") {
    if (method === "GET") return json(ceilings);
    if (method === "POST") {
      const body = (await request.json().catch(() => ({}))) as CeilingBody;
      const refused = configure(body);
      if (refused) return json({ error: refused }, 400);
      return json(ceilings);
    }
    return json({ error: "unsupported method" }, 400);
  }

  if (rest === "launch" && method === "POST") {
    const body = (await request.json().catch(() => ({}))) as { kind?: string };
    const kind = body.kind ?? "";
    // The machine's own configuration is the only thing that may refuse a
    // launch, and on an unconfigured one it does — in the command's words,
    // with the remedy this preview can then perform in Settings.
    if ((kind === "conductor" || kind === "evaluate") && !ceilings.configured) {
      return json({ error: UNCONFIGURED_CONDUCTOR }, 409);
    }
    if (kind === "evaluate" && !ceilings.babel_triages_the_queue) {
      return json(
        {
          error:
            "review work is not authorized on this machine, so Babel will not form judgements " +
            'about records you have not ruled on. Authorize it with "babel conductor configure ' +
            '--babel-triages-the-queue"',
        },
        409,
      );
    }
    const pid = (nextPID += 1);
    started.unshift({
      run_id: "",
      kind,
      started_at: new Date().toISOString(),
      stage: "launching",
      state: "starting",
      stoppable: true,
      pid,
    });
    const result: LaunchResult = {
      pid,
      detail: `Started a ${kind} as pid ${pid}. It appears above as soon as it announces.`,
    };
    return json(result);
  }

  if (rest === "stop" && method === "POST") {
    const body = (await request.json().catch(() => ({}))) as { pid?: number };
    const pid = body.pid ?? 0;
    if (!pid) return json({ error: "a pid is required" }, 400);
    stopped.add(pid);
    const result: StopResult = {
      stopped: true,
      pid,
      method: "stop-file",
      detail: "the run was asked to stop at its next safe point; the work in flight finishes and is receipted",
    };
    return json(result);
  }

  return null;
}
