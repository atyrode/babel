import { describe, expect, test } from "bun:test";
import { attachServerGuest, type GuestCtx } from "@manifold/plugin-kit/server";
import type { IsolateChildFrame, IsolateDispatchCtx, IsolateHostFrame } from "@manifold/protocol";
import {
  BASELINE_ID,
  LIST_RUNS_ACTION,
  LIST_RUNS_DOOR,
  REPORT_ARGV,
  RUN_ACTION,
  RUN_DOOR,
  RUN_RECORDED_EVENT,
  RUNS_CAP,
  RUNS_KEY_PREFIX,
  RunRecord,
  RunResult,
} from "../atyrode.babel/contract.ts";
import { def, handlers } from "../atyrode.babel/server.ts";

/*
  The baseline's doors against a fake ctx: storage is a Map, the clock and the id are ours,
  the machine roster is a Set. Then the whole server half over the kit's in-memory transport,
  which is how the supervisor will drive it — the `loaded` frame and the `invalid_args` rung.
 */

interface Fake {
  readonly ctx: GuestCtx;
  readonly storage: Map<string, string>;
  readonly emitted: { kind: string; payload: unknown }[];
  tick(): void;
}

function fake(): Fake {
  const storage = new Map<string, string>();
  const online: readonly string[] = ["dev-01"];
  const emitted: Fake["emitted"] = [];
  let now = Date.UTC(2026, 8, 5, 22, 0, 0);
  let ids = 0;
  const principal = { id: "p1", kind: "human", name: "Ada", color: "#e03131" } as const;
  const ctx: GuestCtx = {
    pluginId: BASELINE_ID,
    principal,
    auth: {
      principal,
      caps: ["terminals:spawn", "containers:read"],
      containerScope: null,
      isRoot: false,
      allows: () => Promise.resolve(true),
    },
    containerScope: null,
    outsideScope: () => Promise.resolve(null),
    now: () => now,
    newId: () => {
      ids += 1;
      return Promise.resolve(`run-${String(ids).padStart(3, "0")}`);
    },
    storage: {
      pluginId: BASELINE_ID,
      get: (key) => Promise.resolve(storage.get(key) ?? null),
      set: (key, value) => {
        storage.set(key, value);
        return Promise.resolve();
      },
      delete: (key) => {
        storage.delete(key);
        return Promise.resolve();
      },
      keys: (prefix = "") =>
        Promise.resolve([...storage.keys()].filter((key) => key.startsWith(prefix))),
    },
    emit: (_ref, kind, payload) => {
      emitted.push({ kind, payload });
    },
    machines: { isOnline: (id) => Promise.resolve(online.includes(id)) },
    placement: { place: () => Promise.reject(new Error("not expected")) },
    host: {
      roster: () => Promise.reject(new Error("not expected")),
      enabled: () => Promise.resolve(true),
    },
  };
  return {
    ctx,
    storage,
    emitted,
    tick: () => {
      now += 1_000;
    },
  };
}

const run = handlers[RUN_ACTION];
const listRuns = handlers[LIST_RUNS_ACTION];

describe(RUN_DOOR, () => {
  test("refuses a machine that is not online and records nothing", async () => {
    const f = fake();
    const outcome = await run(f.ctx, { machineId: "ws-linux", report: "version" });
    expect(outcome).toEqual({ refused: 'machine "ws-linux" is not online' });
    expect(f.storage.size).toBe(0);
    expect(f.emitted).toEqual([]);
  });

  test("records the run under runs/<recordedAt>-<runId>, emits, and answers the exact argv", async () => {
    const f = fake();
    const outcome = await run(f.ctx, { machineId: "dev-01", report: "archive-fleet" });
    const result = RunResult.parse(outcome);
    expect(result).toEqual({
      runId: "run-001",
      argv: ["babel", "archive", "fleet"],
      recordedAt: "2026-09-05T22:00:00.000Z",
    });

    const key = `${RUNS_KEY_PREFIX}2026-09-05T22:00:00.000Z-run-001`;
    expect([...f.storage.keys()]).toEqual([key]);
    expect(RunRecord.parse(JSON.parse(f.storage.get(key) ?? ""))).toEqual({
      runId: "run-001",
      machineId: "dev-01",
      report: "archive-fleet",
      argv: ["babel", "archive", "fleet"],
      recordedAt: "2026-09-05T22:00:00.000Z",
    });
    expect(f.emitted).toEqual([
      {
        kind: RUN_RECORDED_EVENT,
        payload: {
          runId: "run-001",
          machineId: "dev-01",
          report: "archive-fleet",
          recordedAt: "2026-09-05T22:00:00.000Z",
        },
      },
    ]);
  });

  test("keeps the newest RUNS_CAP records and deletes the oldest beyond it", async () => {
    const f = fake();
    for (let i = 0; i < RUNS_CAP + 3; i += 1) {
      await run(f.ctx, { machineId: "dev-01", report: "version" });
      f.tick();
    }
    const keys = [...f.storage.keys()].sort();
    expect(keys).toHaveLength(RUNS_CAP);
    // The first three (run-001..003) are gone; the fourth is now the oldest kept.
    expect(keys[0]).toEndWith("-run-004");
    expect(keys.at(-1)).toEndWith(`-run-${String(RUNS_CAP + 3).padStart(3, "0")}`);
  });
});

describe(LIST_RUNS_DOOR, () => {
  test("answers newest first and skips a value outside the record's shape", async () => {
    const f = fake();
    await run(f.ctx, { machineId: "dev-01", report: "version" });
    f.tick();
    await run(f.ctx, { machineId: "dev-01", report: "storage-status" });
    f.tick();
    await run(f.ctx, { machineId: "dev-01", report: "archive-status" });
    f.storage.set(`${RUNS_KEY_PREFIX}2026-09-05T22:00:03.000Z-junk`, '{"not":"a record"}');

    const { runs } = await listRuns(f.ctx);
    expect(runs.map((r) => r.report)).toEqual(["archive-status", "storage-status", "version"]);
    expect(runs[0]?.argv).toEqual([...REPORT_ARGV["archive-status"]]);
  });

  test("answers an empty list before any run", async () => {
    expect(await listRuns(fake().ctx)).toEqual({ runs: [] });
  });
});

// ---------------------------------------------------------------------------- over the transport

interface FakeHost {
  send(frame: IsolateHostFrame): void;
  next(): Promise<IsolateChildFrame>;
}

function host(): FakeHost {
  const queue: IsolateChildFrame[] = [];
  const waiting: ((frame: IsolateChildFrame) => void)[] = [];
  let listener: (frame: unknown) => void = () => {};
  attachServerGuest(def, {
    send: (frame) => {
      const waiter = waiting.shift();
      if (waiter === undefined) queue.push(frame);
      else waiter(frame);
    },
    onMessage: (next) => {
      listener = next;
    },
    exit: () => {},
    warn: () => {},
  });
  return {
    send: (frame) => listener(frame),
    next: () => {
      const queued = queue.shift();
      if (queued !== undefined) return Promise.resolve(queued);
      const { promise, resolve } = Promise.withResolvers<IsolateChildFrame>();
      waiting.push(resolve);
      return promise;
    },
  };
}

const dispatchCtx: IsolateDispatchCtx = {
  principal: { id: "p1", kind: "human", name: "Ada", color: "#e03131" },
  caps: ["terminals:spawn", "containers:read"],
  isRoot: false,
  containerScope: null,
  now: 1_000,
};

describe("the server half over the kit's transport", () => {
  test("loads with both doors published under the baseline id, the report enum in the JSON Schema", async () => {
    const fake = host();
    fake.send({ t: "load", pluginId: BASELINE_ID, manifest: def.manifest, dir: "/nowhere" });
    const loaded = await fake.next();
    expect(loaded.t).toBe("loaded");
    if (loaded.t !== "loaded") return;
    expect(loaded.actions.map((action) => action.name)).toEqual([RUN_DOOR, LIST_RUNS_DOOR]);
    expect(loaded.actions[0]?.caps).toEqual(["terminals:spawn"]);
    expect(loaded.actions[1]?.caps).toEqual(["containers:read"]);
    // The kit's zod generated this from the contract's zod: the two copies interoperate.
    expect(loaded.actions[0]?.input).toMatchObject({
      type: "object",
      required: ["machineId", "report"],
      properties: {
        report: { enum: ["archive-status", "archive-fleet", "storage-status", "version"] },
      },
    });
    expect(loaded.hooks).toEqual({ onEnable: false, onDisable: false, onAssemblyChanged: false });
  });

  test("a report outside the closed map is refused invalid_args before the handler runs", async () => {
    const fake = host();
    fake.send({ t: "load", pluginId: BASELINE_ID, manifest: def.manifest, dir: "/nowhere" });
    await fake.next();
    fake.send({
      t: "dispatch",
      id: "d1",
      action: RUN_ACTION,
      args: { machineId: "dev-01", report: "archive-push" },
      ctx: dispatchCtx,
    });
    const dispatched = await fake.next();
    expect(dispatched).toMatchObject({
      t: "dispatched",
      id: "d1",
      outcome: { ok: false, rule: "invalid_args" },
    });
  });

  test("a well-formed run rides the ctx calls out and the record and emission back", async () => {
    const fake = host();
    fake.send({ t: "load", pluginId: BASELINE_ID, manifest: def.manifest, dir: "/nowhere" });
    await fake.next();
    fake.send({
      t: "dispatch",
      id: "d2",
      action: RUN_ACTION,
      args: { machineId: "dev-01", report: "version" },
      ctx: dispatchCtx,
    });
    const answers: Record<string, unknown> = {
      "machines.isOnline": true,
      newId: "run-xyz",
      "storage.set": null,
      "storage.keys": ["runs/1970-01-01T00:00:01.000Z-run-xyz"],
    };
    const methods: string[] = [];
    for (;;) {
      const frame = await fake.next();
      if (frame.t === "dispatched") {
        expect(frame).toEqual({
          t: "dispatched",
          id: "d2",
          outcome: {
            ok: true,
            result: {
              runId: "run-xyz",
              argv: ["babel", "version"],
              recordedAt: "1970-01-01T00:00:01.000Z",
            },
            emits: [
              {
                ref: { kind: "plugin", pluginId: BASELINE_ID },
                kind: RUN_RECORDED_EVENT,
                payload: {
                  runId: "run-xyz",
                  machineId: "dev-01",
                  report: "version",
                  recordedAt: "1970-01-01T00:00:01.000Z",
                },
              },
            ],
          },
        });
        break;
      }
      if (frame.t !== "call") throw new Error(`unexpected ${frame.t}`);
      methods.push(frame.method);
      fake.send({ t: "reply", id: frame.id, ok: true, result: answers[frame.method] ?? null });
    }
    expect(methods).toEqual(["machines.isOnline", "newId", "storage.set", "storage.keys"]);
  });
});
