import { z } from "zod";

/*
  THE CONTRACT BETWEEN THE HALVES of Babel's manifold plugins. Every id, door name, storage
  key, event kind and launch line is spelled here once and imported by `server.ts`, `web.ts`
  and `sessions/web.ts`; the kit inlines this module into every bundle, so the baseline is
  never a library the sub-plugin loads — it is a set of doors the sub-plugin calls through
  the host (docs/manifold-transition.md §7.1, "the baseline is not a library").
 */

// ---------------------------------------------------------------------------- plugin ids

/** The baseline: the doors that record a report run and list the record. */
export const BASELINE_ID = "atyrode.babel";
/** The sub-plugin: one panel over the baseline's doors; depends on the baseline `required`. */
export const SESSIONS_ID = "atyrode.babel.sessions";
/** RESERVED, not built (docs/manifold-transition.md §7): the profile ceremony (babel#160). */
export const CONFIGURE_ID = "atyrode.babel.configure";

/** The sub-plugin's one panel, keyed by LOCAL id in `contributes.panels` and `defineWebPlugin`. */
export const REPORTS_PANEL = "reports";

// ---------------------------------------------------------------------------- doors

/**
 * LOCAL action names, as `defineServerAction` takes them; the roster publishes
 * `${id}.${name}`. A local name has no dots (`LocalNameSchema`, assembly refuses
 * `runs.list`), so the list door follows the shipped `listX` spelling.
 */
export const RUN_ACTION = "run";
export const LIST_RUNS_ACTION = "listRuns";

/** FULL action names, as `host.action` and `button.action` take them. */
export const RUN_DOOR = `${BASELINE_ID}.${RUN_ACTION}`;
export const LIST_RUNS_DOOR = `${BASELINE_ID}.${LIST_RUNS_ACTION}`;

// ---------------------------------------------------------------------------- storage and events

/** One key per recorded run: `runs/<recordedAt ISO>-<runId>`; ISO-8601 UTC sorts as time. */
export const RUNS_KEY_PREFIX = "runs/";
/** How many run records the baseline keeps; the oldest beyond this are deleted on append. */
export const RUNS_CAP = 50;

/** Emitted on the plugin's own node when `run` records a report run. */
export const RUN_RECORDED_EVENT = "run_recorded";

// ---------------------------------------------------------------------------- the reports

/**
 * THE CLOSED MAP of what a run may launch: babel's read-only reports, each with the exact
 * argv it runs on the chosen machine with the agent's PATH. Anything else is refused by the
 * `run` door's own zod input (`invalid_args`). No report here mutates the archive.
 */
export const REPORT_ARGV = {
  "archive-status": ["babel", "archive", "status"],
  "archive-fleet": ["babel", "archive", "fleet"],
  "storage-status": ["babel", "storage", "status"],
  version: ["babel", "version"],
} as const satisfies Record<string, readonly [string, ...string[]]>;

export type ReportId = keyof typeof REPORT_ARGV;
export const REPORT_IDS = Object.keys(REPORT_ARGV) as [ReportId, ...ReportId[]];
export const ReportIdSchema = z.enum(REPORT_IDS);

/** What the panel labels each report; help text from `babel <report> --help`. */
export const REPORT_LABELS: Readonly<Record<ReportId, string>> = {
  "archive-status": "archive status - snapshots per host",
  "archive-fleet": "archive fleet - has every host published recently",
  "storage-status": "storage status - persistent repository configuration",
  version: "version - build identity",
};

// ---------------------------------------------------------------------------- door schemas

export const RunInput = z.strictObject({
  machineId: z.string().min(1).max(128),
  report: ReportIdSchema,
});
export type RunInput = z.infer<typeof RunInput>;

const argvSchema = z.array(z.string().min(1)).min(1).max(8);

export const RunResult = z.strictObject({
  runId: z.string().min(1),
  argv: argvSchema,
  recordedAt: z.iso.datetime(),
});
export type RunResult = z.infer<typeof RunResult>;

/** One stored record, the value under a `runs/` key; a superset of `RunResult`. */
export const RunRecord = z.strictObject({
  runId: z.string().min(1),
  machineId: z.string().min(1),
  report: ReportIdSchema,
  argv: argvSchema,
  recordedAt: z.iso.datetime(),
});
export type RunRecord = z.infer<typeof RunRecord>;

export const ListRunsInput = z.strictObject({});
/** Newest first, at most `RUNS_CAP` rows. */
export const ListRunsResult = z.strictObject({
  runs: z.array(RunRecord).max(RUNS_CAP),
});
export type ListRunsResult = z.infer<typeof ListRunsResult>;

// ---------------------------------------------------------------------------- the launch line

/** A word `sh` reads back unchanged without quoting; anything else is single-quoted. */
const SHELL_SAFE_WORD = /^[A-Za-z0-9_./:@%+=,-]+$/;

/**
 * Quotes one argv word for POSIX `sh`: single quotes, with an embedded single quote spelled
 * as `'\''` (close, escaped quote, reopen). Inside single quotes nothing else is special.
 */
export function shellQuote(word: string): string {
  if (SHELL_SAFE_WORD.test(word)) return word;
  return `'${word.replaceAll("'", String.raw`'\''`)}'`;
}

/** How long the tile stays readable after a report exits, in seconds. */
export const HOLD_SECONDS = 600;

/**
 * Every report exits at once, and a PTY whose program has exited is a blank tile. So the
 * terminal runs the report under `sh -c`, prints the exit status on its own line, and then
 * holds the PTY open with `sleep` (exec'd, so the shell is not a second process) for
 * `HOLD_SECONDS`. Every word of the report's argv passes through `shellQuote`, never raw.
 */
export function holdOpenArgv(argv: readonly string[]): readonly [string, string, string] {
  const command = argv.map(shellQuote).join(" ");
  return [
    "sh",
    "-c",
    `${command}; printf '\\n[babel exited %s]\\n' $?; exec sleep ${String(HOLD_SECONDS)}`,
  ];
}
