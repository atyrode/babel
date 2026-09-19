import { expect, test } from "bun:test";
import { closeSync, readFileSync } from "node:fs";
import { mkdtempSync, openSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { WorkerProgressSchema } from "@manifold/protocol";
import { RUN_STAGES } from "../contract.ts";
import { CONTEXT_FD_ENV, openProgress, stageShortfall, type ProgressSink } from "./progress.ts";

/*
  THE FRAME THE OWNER READS.

  A stage the owner cannot parse is not a bad label: `JobContext` fails that channel with
  `context_protocol_error` and the job is cancelled. So the assertion that matters is not that a
  line was written but that every line Babel writes PARSES AGAINST THE PINNED SCHEMA — the very
  `WorkerProgressSchema` the owner parses it with — which is what these tests do for each of the
  three stages the machine half reports.
*/

/** Every line a channel wrote, parsed as the owner parses them: newline-delimited JSON. */
function lines(written: readonly string[]): unknown[] {
  const out: unknown[] = [];
  for (const chunk of written) {
    for (const line of chunk.split("\n")) {
      if (line !== "") out.push(JSON.parse(line));
    }
  }
  return out;
}

function collector(): { sink: ProgressSink; written: string[] } {
  const written: string[] = [];
  return { sink: { write: (line) => written.push(line) }, written };
}

test("every stage the machine half reports is a frame the owner's own schema admits", () => {
  const { sink, written } = collector();
  const progress = openProgress({ sink });

  progress.report({ stage: RUN_STAGES.preparing, message: "3 of 927", fraction: 3 / 927 });
  progress.report({ stage: RUN_STAGES.atModel, message: "explore" });
  progress.report({ stage: RUN_STAGES.submitting, message: "12 records" });

  const frames = lines(written);
  expect(frames).toHaveLength(3);
  expect(frames.map((frame) => WorkerProgressSchema.parse(frame).stage)).toEqual([
    "preparing",
    "at the model",
    "submitting",
  ]);
  // Every frame is one line and the absent fields are absent, not null: the owner's schema is
  // strict, and `fraction: null` would fail the channel exactly as a bad stage would.
  expect(WorkerProgressSchema.parse(frames[0])).toEqual({
    type: "progress",
    stage: "preparing",
    message: "3 of 927",
    fraction: 3 / 927,
  });
  expect(WorkerProgressSchema.parse(frames[1])).toEqual({
    type: "progress",
    stage: "at the model",
    message: "explore",
  });
  expect(progress.written).toBe(3);
  expect(progress.refused).toBe(0);
});

test("a frame the owner would refuse is never written, because the refusal is the job", () => {
  const { sink, written } = collector();
  const progress = openProgress({ sink });

  progress.report({ stage: "At The Model" });
  progress.report({ stage: " preparing" });
  progress.report({ stage: "x".repeat(65) });
  progress.report({ stage: RUN_STAGES.preparing, message: "a\nb" });
  progress.report({ stage: RUN_STAGES.preparing, fraction: 1.5 });

  expect(written).toEqual([]);
  expect(progress.refused).toBe(5);
  expect(progress.written).toBe(0);
  // And each one is refused for its own reason, so a defect names itself.
  expect(stageShortfall({ stage: "At The Model" })).toContain("stage");
  expect(stageShortfall({ stage: RUN_STAGES.preparing, message: "a\nb" })).toContain(
    "control character",
  );
  expect(stageShortfall({ stage: RUN_STAGES.preparing, fraction: 1.5 })).toContain("fraction");
  expect(stageShortfall({ stage: RUN_STAGES.atModel, message: "explore", fraction: 1 })).toBe("");
});

test("a run launched by an owner writes its frames to the inherited descriptor", () => {
  // A real descriptor rather than the collector: the whole point of this module is that it
  // writes bytes to a file descriptor the owner handed it, and `writeSync` on a socket that
  // takes partial writes is the thing a fake sink cannot exercise. A file is what a test can
  // open, and the byte stream it leaves is what the owner's frame reader consumes.
  const directory = mkdtempSync(join(tmpdir(), "babel-progress-"));
  const path = join(directory, "channel");
  const fd = openSync(path, "w");
  try {
    const progress = openProgress({ env: { [CONTEXT_FD_ENV]: String(fd) } });
    progress.report({ stage: RUN_STAGES.preparing });
    progress.report({ stage: RUN_STAGES.atModel, message: "evidence" });
    expect(progress.written).toBe(2);
  } finally {
    closeSync(fd);
  }
  const frames = lines([readFileSync(path, "utf8")]);
  expect(frames.map((frame) => WorkerProgressSchema.parse(frame).stage)).toEqual([
    "preparing",
    "at the model",
  ]);
  rmSync(directory, { recursive: true, force: true });
});

test("a run nobody is watching reports into nothing rather than failing", () => {
  const progress = openProgress({ env: {} });
  progress.report({ stage: RUN_STAGES.atModel });
  expect(progress.written).toBe(0);
  expect(progress.refused).toBe(0);

  // A descriptor the environment names badly is the same case: a hand-run, not a broken job.
  const nonsense = openProgress({ env: { [CONTEXT_FD_ENV]: "stdout" } });
  nonsense.report({ stage: RUN_STAGES.atModel });
  expect(nonsense.written).toBe(0);
});

test("a channel that dies mid-run stays silent instead of taking the run down with it", () => {
  let calls = 0;
  const progress = openProgress({
    sink: {
      write: () => {
        calls += 1;
        throw new Error("EPIPE");
      },
    },
  });
  progress.report({ stage: RUN_STAGES.preparing });
  progress.report({ stage: RUN_STAGES.atModel });
  progress.report({ stage: RUN_STAGES.submitting });
  // One attempt, then silence: a channel that failed once will fail every time, and the run's
  // work is not this line.
  expect(calls).toBe(1);
  expect(progress.written).toBe(0);
});
