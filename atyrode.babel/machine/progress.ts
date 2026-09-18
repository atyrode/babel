import { writeSync } from "node:fs";
import { STAGE_MESSAGE_MAX, STAGE_PATTERN, type RunStage } from "../contract.ts";

/*
  WHERE THIS RUN IS, SAID OUT LOUD (#261, post-mortem F12).

  A Manifold job is invisible between `started` and its terminal state: the journal carries the
  hub's and the owner's facts, which say nothing about the work. So the workload says it itself.
  Every job is handed a private owner channel on the descriptor named by MANIFOLD_JOB_CONTEXT_FD
  — a socketpair the owner made, close-on-exec, newline-delimited JSON both ways — and one frame
  on it is fire-and-forget:

    {"type":"progress","stage":"at the model","message":"explore","fraction":0.5}

  The owner folds those to at most one `job_progress` event every five seconds per job, newest
  line wins (`JOB_PROGRESS_INTERVAL_MS`), and the hub journals it. Reporting often is therefore
  cheap and the only mistake available is reporting a stale phase.

  WHY THE FRAME IS WRITTEN HERE RATHER THAN THROUGH THE SDK. `@manifold/sdk`'s
  `openWorkerContext().reportProgress` writes exactly this line, and it is the sanctioned way —
  but adopting the channel means adopting the whole of it: a duplex socket, a reply queue, the
  service-call protocol and the strict initial context frame, none of which the machine half
  speaks (it reaches its one service through a materialized binding file, `machine/restic.ts`).
  `tsconfig.json` has no `@manifold/sdk` alias either, and `pack.sh` builds this half
  with a plain `bun build` that inlines everything it imports, so importing the SDK would put
  the protocol package inside `machine.js` for one line of JSON. The line is written here, and
  `machine/progress.test.ts` proves each frame against the pinned `WorkerProgressSchema`.

  THE ONE RULE THAT MATTERS: an INVALID frame is not a bad label, it is a dead job. The owner
  parses this channel strictly and fails it with `context_protocol_error`, which cancels the
  run (`packages/agent/src/job-context.ts:73-89`); the SDK validates before writing for exactly
  that reason and this does the same. A frame that does not pass is dropped with one line on
  stderr — a run must never die of how it described itself, and it must never lie by reporting
  a stage the owner refused.
*/

/** The descriptor the owner passes its private channel on. */
export const CONTEXT_FD_ENV = "MANIFOLD_JOB_CONTEXT_FD";

/** One line the workload writes about itself. `stage` is the only required word. */
export interface StageReport {
  readonly stage: RunStage | string;
  readonly message?: string | undefined;
  readonly fraction?: number | undefined;
}

export interface ProgressChannel {
  /** Says where the run is. Never throws and never blocks on an answer: there is none. */
  report(report: StageReport): void;
  /** How many frames reached the descriptor; the tests' and a hand-run's only observable. */
  readonly written: number;
  /** How many were refused by the rules above, which is always a defect in Babel. */
  readonly refused: number;
}

/** Where the frames go. A test hands its own; a job's is the inherited descriptor. */
export interface ProgressSink {
  write(line: string): void;
}

/**
 * Why this frame cannot be written, or empty when it can.
 *
 * The rules are `WorkerProgressSchema`'s, which are `JobProgressEventSchema`'s: a stage of 1–64
 * characters of lowercase `[a-z0-9 ._-]` without a leading or trailing space, a message of at
 * most 256 characters carrying no control character, and a fraction inside 0..1.
 */
export function stageShortfall(report: StageReport): string {
  if (!STAGE_PATTERN.test(report.stage)) return `stage ${JSON.stringify(report.stage)}`;
  const message = report.message;
  if (message !== undefined) {
    if (message.length > STAGE_MESSAGE_MAX)
      return `a message of ${String(message.length)} characters`;
    if (/\p{Cc}/u.test(message)) return "a message carrying a control character";
  }
  const fraction = report.fraction;
  if (fraction !== undefined && !(Number.isFinite(fraction) && fraction >= 0 && fraction <= 1)) {
    return `a fraction of ${String(fraction)}`;
  }
  return "";
}

/**
 * The descriptor's sink, or null when this process was not launched by an owner.
 *
 * A machine operation runs identically under `bun machine/main.ts scan …` on a developer's
 * shell, where there is no channel at all, so the absence is ordinary and silent. Writes are
 * `writeSync` on the inherited socket rather than an adopted `node:net` handle: the owner's
 * socketpair is blocking, one line is at most a few hundred bytes, and a handle would hold the
 * event loop open past the work.
 */
function descriptorSink(raw: string | undefined): ProgressSink | null {
  if (raw === undefined || !/^[0-9]{1,10}$/.test(raw)) return null;
  const fd = Number(raw);
  if (!Number.isSafeInteger(fd) || fd < 3) return null;
  return {
    write(line: string): void {
      const bytes = Buffer.from(line, "utf8");
      let sent = 0;
      while (sent < bytes.length) {
        const wrote = writeSync(fd, bytes, sent, bytes.length - sent);
        if (wrote <= 0) return;
        sent += wrote;
      }
    },
  };
}

/**
 * Opens the run's progress channel. A run without one reports into nothing, which is what a
 * hand-run and every unit test that does not ask for a sink get.
 */
export function openProgress(
  options: {
    readonly sink?: ProgressSink | null;
    readonly env?: Readonly<Record<string, string | undefined>>;
  } = {},
): ProgressChannel {
  const sink =
    options.sink === undefined
      ? descriptorSink((options.env ?? process.env)[CONTEXT_FD_ENV])
      : options.sink;
  let written = 0;
  let refused = 0;
  let silenced = false;
  return {
    report(report: StageReport): void {
      const shortfall = stageShortfall(report);
      if (shortfall !== "") {
        refused += 1;
        process.stderr.write(`babel: a progress frame was not written: ${shortfall}\n`);
        return;
      }
      if (sink === null || silenced) return;
      try {
        const frame = {
          type: "progress",
          stage: report.stage,
          ...(report.message === undefined ? {} : { message: report.message }),
          ...(report.fraction === undefined ? {} : { fraction: report.fraction }),
        };
        sink.write(`${JSON.stringify(frame)}\n`);
        written += 1;
      } catch {
        // The owner is gone, or the socket will not take another byte. Either way the run's
        // work is not this line, and a channel that failed once will fail every time.
        silenced = true;
      }
    },
    get written() {
      return written;
    },
    get refused() {
      return refused;
    },
  };
}

/** A channel that reports nothing, for the operations a caller drives without one. */
export const SILENT: ProgressChannel = { report: () => undefined, written: 0, refused: 0 };
